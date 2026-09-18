//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlledForegroundResumeUsesFrozenWorkflow(t *testing.T) {
	for _, fileBacked := range []bool{false, true} {
		t.Run(fmt.Sprintf("file_backed=%v", fileBacked), func(t *testing.T) {
			root := t.TempDir()
			cfg := DefaultExecutorConfig("foreground-snapshot")
			cfg.ProjectDir, cfg.RunID = root, "run-frozen"
			workflow := &Workflow{SchemaVersion: "2.0", Name: "foreground-snapshot", Steps: []Step{
				{ID: "once", Command: fmt.Sprintf("printf x >> '%s'", filepath.Join(root, "once"))},
				{ID: "finish", DependsOn: []string{"once"}, Command: fmt.Sprintf("test -f '%s' && printf original > '%s'", filepath.Join(root, "gate"), filepath.Join(root, "result"))},
			}}
			if fileBacked {
				cfg.WorkflowFile = filepath.Join(root, "source.yaml")
				data, err := marshalWorkflowSnapshot(workflow)
				if err != nil {
					t.Fatal(err)
				}
				writeSnapshotTemplate(t, cfg.WorkflowFile, string(data))
			}
			originalCommand := workflow.Steps[1].Command
			st, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || st == nil || st.Status != StatusFailed || st.Steps["once"].Status != StatusCompleted {
				t.Fatalf("expected a resumable failure after one completed step: %+v %v", st, err)
			}
			if st.WorkflowFile == cfg.WorkflowFile || filepath.Base(filepath.Dir(st.WorkflowFile)) != workflowSnapshotDir {
				t.Fatalf("foreground run did not retain a managed definition: %q", st.WorkflowFile)
			}
			// Neither changing the caller's object nor editing its source may
			// change what recovery executes. The completed step must not replay.
			workflow.Steps[1].Command = fmt.Sprintf("printf changed > '%s'", filepath.Join(root, "result"))
			if fileBacked {
				data, err := marshalWorkflowSnapshot(workflow)
				if err != nil {
					t.Fatal(err)
				}
				writeSnapshotTemplate(t, cfg.WorkflowFile, string(data))
			}
			writeSnapshotTemplate(t, filepath.Join(root, "gate"), "ready")
			owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
			if err != nil || !validation.Valid {
				t.Fatalf("load frozen recovery definition: %v %+v", err, validation)
			}
			if frozen.Steps[1].Command != originalCommand {
				t.Fatal("recovery loaded the edited definition")
			}
			cfg.WorkflowFile = prior.WorkflowFile
			cfg.ResumeOptions.Mode = ResumeModeRestartFailed
			resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
			if err != nil || resumed == nil || resumed.Status != StatusCompleted {
				t.Fatalf("resume frozen foreground run: %+v %v", resumed, err)
			}
			for name, want := range map[string]string{"once": "x", "result": "original"} {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, want %q: %v", name, got, want, err)
				}
			}
		})
	}
}

func TestControlledForegroundFreezesSourceRelativeTemplates(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultExecutorConfig("foreground-template")
	cfg.ProjectDir, cfg.RunID = root, "run-template"
	cfg.WorkflowFile = filepath.Join(root, "definitions", "flow.yaml")
	writeSnapshotTemplate(t, filepath.Join(root, "definitions", "prompt.md"), "source ${TASK} {{PARAM}}")
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "wrong project fallback")
	workflow := &Workflow{SchemaVersion: "2.0", Name: "foreground-template", Steps: []Step{
		{ID: "prompt", Agent: "claude", Template: "prompt.md", When: "false"},
	}}
	st, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || st == nil || st.Status != StatusCompleted {
		t.Fatalf("run skipped prompt step: %+v %v", st, err)
	}
	if workflow.Steps[0].Template != "prompt.md" {
		t.Fatal("snapshotting mutated the caller's workflow")
	}
	writeSnapshotTemplate(t, filepath.Join(root, "definitions", "prompt.md"), "edited")
	frozen, validation, err := LoadResumeWorkflow(st.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("load foreground template bundle: %v %+v", err, validation)
	}
	if !managedTemplateSnapshot(frozen.Steps[0].Template) {
		t.Fatal("foreground prompt dependency stayed live")
	}
	got, err := os.ReadFile(frozen.Steps[0].Template)
	if err != nil || string(got) != "source ${TASK} {{PARAM}}" {
		t.Fatalf("lost source precedence or rendered the template prematurely: %q %v", got, err)
	}
}

func TestControlledForegroundSnapshotFailureStartsNoWork(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%v", corrupt), func(t *testing.T) {
			root := t.TempDir()
			cfg := DefaultExecutorConfig("foreground-rejected")
			cfg.ProjectDir, cfg.RunID = root, "run-rejected"
			workflow := &Workflow{SchemaVersion: "2.0", Name: "rejected", Steps: []Step{
				{ID: "must-not-run", Command: fmt.Sprintf("printf dispatched > '%s'", filepath.Join(root, "marker"))},
			}}
			var evidence string
			if corrupt {
				_, path, err := SnapshotWorkflow(context.Background(), root, workflow)
				if err != nil {
					t.Fatal(err)
				}
				evidence = path
			} else {
				evidence = filepath.Join(pipelineStateDir(root), workflowSnapshotDir)
			}
			writeSnapshotTemplate(t, evidence, "retain damaged evidence")
			st, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || st != nil || !strings.Contains(err.Error(), "snapshot foreground pipeline") {
				t.Fatalf("snapshot failure entered executor: %+v %v", st, err)
			}
			for _, path := range []string{filepath.Join(root, "marker"), pipelineStatePath(root, cfg.RunID)} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected run created %s: %v", path, err)
				}
			}
			got, err := os.ReadFile(evidence)
			if err != nil || string(got) != "retain damaged evidence" {
				t.Fatalf("snapshot failure modified evidence: %q %v", got, err)
			}
			owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
			if err != nil {
				t.Fatalf("snapshot failure leaked run ownership: %v", err)
			}
			owner.Close()
		})
	}
}
