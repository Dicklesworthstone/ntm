//go:build unix

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

// Exercises the production inline/file execution and resume surfaces. The
// first command is deliberately non-idempotent: recovery must not repeat it.
func TestPipelineSnapshotMakesInlineAndFileRunsResumable(t *testing.T) {
	for _, fileBacked := range []bool{false, true} {
		t.Run(fmt.Sprintf("file=%v", fileBacked), func(t *testing.T) {
			dir := t.TempDir()
			marker, gate := filepath.Join(dir, "once"), filepath.Join(dir, "continue")
			workflow := &pipeline.Workflow{SchemaVersion: "2.0", Name: "recoverable", Steps: []pipeline.Step{
				{ID: "once", Command: fmt.Sprintf("echo once >> %q", marker)},
				{ID: "gate", DependsOn: []string{"once"}, Command: fmt.Sprintf("test -f %q", gate)},
			}}
			srv := &Server{projectDir: dir}
			var first pipeline.PipelineRunOutput
			source := filepath.Join(dir, "source.yaml")
			if fileBacked {
				data, err := json.Marshal(workflow)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, data, 0600); err != nil {
					t.Fatal(err)
				}
				first = srv.runPipelineWithResult(context.Background(), pipeline.PipelineRunOptions{WorkflowFile: source, Session: "snapshot", ProjectDir: dir})
			} else {
				first = srv.execPipelineInline(context.Background(), workflow, "snapshot", nil, false)
			}
			if first.Success || first.RunID == "" {
				t.Fatalf("gate should fail after first command: %+v", first)
			}
			prior, err := pipeline.LoadState(dir, first.RunID)
			if err != nil || prior.WorkflowFile == "" || prior.WorkflowFile == source {
				t.Fatalf("execution did not persist an independent definition: %+v %v", prior, err)
			}
			// Change both the old caller object and source file. Neither is
			// allowed to alter the command graph that recovery executes.
			workflow.Name = "replacement"
			workflow.Steps[1].Command = "exit 99"
			if err := os.WriteFile(source, []byte("not the original workflow"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gate, []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			// A new server instance has no knowledge of the originating request.
			restarted := &Server{projectDir: dir}
			rec := httptest.NewRecorder()
			restarted.handleResumePipeline(rec, pipelineControlRequest(http.MethodPost, first.RunID))
			if rec.Code != http.StatusOK {
				t.Fatalf("saved run cannot recover: %d %s", rec.Code, rec.Body.String())
			}
			final, err := pipeline.LoadState(dir, first.RunID)
			if err != nil || final.Status != pipeline.StatusCompleted || final.WorkflowID != "recoverable" {
				t.Fatalf("resumed definition changed or failed: %+v %v", final, err)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "once\n" {
				t.Fatalf("recovery repeated completed work: %q %v", data, err)
			}
		})
	}
}

func TestPipelineResumeRejectsDamagedSnapshotWithoutCheckpointMutation(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{projectDir: dir}
	out := srv.execPipelineInline(context.Background(), &pipeline.Workflow{SchemaVersion: "2.0", Name: "damaged", Steps: []pipeline.Step{{ID: "fail", Command: "exit 1"}}}, "snapshot", nil, false)
	prior, err := pipeline.LoadState(dir, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, ".ntm", "pipelines", out.RunID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prior.WorkflowFile, []byte(`{"name":"damaged","steps":[{"id":"fail","command":"true"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.handleResumePipeline(rec, pipelineControlRequest(http.MethodPost, out.RunID))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "hash mismatch") {
		t.Fatalf("damaged snapshot was executed: %d %s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected recovery modified saved checkpoint: %v", err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), dir, out.RunID)
	if err != nil {
		t.Fatalf("rejection leaked run ownership: %v", err)
	}
	owner.Close()
}

func TestPipelineSnapshotFailureDoesNotDispatchOrLeakBackgroundOwner(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, ".ntm", "pipelines")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "workflow-snapshots"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "must-not-execute")
	workflow := &pipeline.Workflow{SchemaVersion: "2.0", Name: "no-snapshot", Steps: []pipeline.Step{{ID: "work", Command: fmt.Sprintf("echo bad > %q", marker)}}}
	srv := &Server{projectDir: dir}
	out := srv.execPipelineInline(context.Background(), workflow, "snapshot", nil, true)
	if out.Success {
		t.Fatal("accepted background run without durable definition")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("work started despite snapshot failure: %v", err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), dir, out.RunID)
	if err != nil {
		t.Fatalf("background startup error leaked ownership: %v", err)
	}
	owner.Close()
}

func TestPipelineDryRunDoesNotPublishSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.yaml")
	data, err := json.Marshal(&pipeline.Workflow{SchemaVersion: "2.0", Name: "preview", Steps: []pipeline.Step{{ID: "work", Command: "true"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectDir: dir}
	out := srv.runPipelineWithResult(context.Background(), pipeline.PipelineRunOptions{Session: "preview", WorkflowFile: path, DryRun: true})
	if !out.Success || !out.DryRun {
		t.Fatalf("dry run failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ntm")); !os.IsNotExist(err) {
		t.Fatalf("preview created persistent execution artifacts: %v", err)
	}
}

func TestPipelineResumeSnapshotSymlinkCannotBypassHashCheck(t *testing.T) {
	dir := t.TempDir()
	workflow := &pipeline.Workflow{SchemaVersion: "2.0", Name: "symlinked", Steps: []pipeline.Step{{ID: "work", Command: "true"}}}
	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	mutable := filepath.Join(dir, "mutable.yaml")
	if err := os.WriteFile(mutable, data, 0600); err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Join(dir, ".ntm", "pipelines", "workflow-snapshots")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	locator := filepath.Join(snapshotDir, "sha256-"+strings.Repeat("0", 64)+".yaml")
	if err := os.Symlink(mutable, locator); err != nil {
		t.Fatal(err)
	}
	runID := pipeline.GenerateRunID()
	prior := &pipeline.ExecutionState{RunID: runID, WorkflowID: workflow.Name, WorkflowFile: locator, Session: "snapshot", Status: pipeline.StatusFailed}
	if err := pipeline.SaveState(dir, prior); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectDir: dir}
	out := srv.resumePipelineWithResult(context.Background(), runID, prior.Session, nil, prior)
	if out.Success || out.ErrorCode != ErrCodeInvalidWorkflow {
		t.Fatalf("resolving snapshot symlink bypassed integrity validation: %+v", out)
	}
}
