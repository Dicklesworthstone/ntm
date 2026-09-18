//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlledForegroundProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) == i+3 && os.Args[i+1] == "foreground" {
			root := os.Args[i+2]
			os.Exit(PrintPipelineRun(PipelineRunOptions{WorkflowFile: filepath.Join(root, "workflow.yaml"), ProjectDir: root, Session: "foreground"}))
		}
	}
}

func TestRobotForegroundPipelineHasExternalCancellation(t *testing.T) {
	root := backgroundFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlledForegroundProcess$", "--", "foreground", root)
	cmd.Dir = root
	logFile, err := os.CreateTemp(t.TempDir(), "foreground-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})

	var id string
	deadline := time.Now().Add(5 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		entries, _ := os.ReadDir(pipelineStateDir(root))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				candidate := strings.TrimSuffix(entry.Name(), ".json")
				if st, err := LoadState(root, candidate); err == nil && st.Status == StatusRunning {
					id = candidate
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("foreground robot command never published a running checkpoint")
	}
	raw, err := backgroundHelper(t, "cancel", root, id)
	if err != nil || !strings.Contains(string(raw), "cancellation_requested") {
		t.Fatalf("fresh process could not cancel foreground run: %v %s", err, raw)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled foreground command returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground executor ignored cancellation")
	}
	st := awaitBackgroundState(t, root, id, StatusCancelled)
	if filepath.Base(filepath.Dir(st.WorkflowFile)) != workflowSnapshotDir {
		t.Fatal("foreground recovery still depends on a mutable workflow file")
	}
	if _, validation, err := LoadResumeWorkflow(st.WorkflowFile); err != nil || !validation.Valid {
		t.Fatalf("cancelled foreground run has no verified recovery definition: %v %+v", err, validation)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled foreground run completed blocked work")
	}
}

func TestControlledRunRejectsOwnedAndExistingIdentities(t *testing.T) {
	for _, held := range []bool{true, false} {
		t.Run(fmt.Sprintf("owned=%v", held), func(t *testing.T) {
			root := t.TempDir()
			cfg := DefaultExecutorConfig("controlled")
			cfg.ProjectDir, cfg.RunID = root, "run-exclusive"
			prior := &ExecutionState{RunID: cfg.RunID, WorkflowID: "prior", Status: StatusCompleted, Steps: map[string]StepResult{"done": {Status: StatusCompleted, Output: "retain me"}}}
			if err := SaveState(root, prior); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pipelineStatePath(root, cfg.RunID))
			if err != nil {
				t.Fatal(err)
			}
			if held {
				owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
			}
			workflow := &Workflow{SchemaVersion: "2.0", Name: "must-not-run", Steps: []Step{{ID: "no", Command: "exit 99"}}}
			st, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || st != nil {
				t.Fatalf("reused run entered executor: %v %v", st, err)
			}
			if held && !errors.Is(err, ErrRunAlreadyOwned) {
				t.Fatalf("lost ownership conflict: %v", err)
			}
			after, err := os.ReadFile(pipelineStatePath(root, cfg.RunID))
			if err != nil || string(before) != string(after) {
				t.Fatal("rejected run modified checkpoint")
			}
		})
	}
}

func TestControlledRunReadOnlyPathsDoNotCreateArtifacts(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			root := t.TempDir()
			cfg := DefaultExecutorConfig("controlled")
			cfg.ProjectDir, cfg.DryRun = root, dry
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !dry {
				cancel()
			}
			workflow := &Workflow{SchemaVersion: "2.0", Name: "read-only", Steps: []Step{{ID: "no", Command: "exit 99"}}}
			st, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			if dry && (err != nil || st == nil || st.Status != StatusCompleted) {
				t.Fatalf("dry run: %v %v", st, err)
			}
			if !dry && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost pre-cancellation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only path created state")
			}
		})
	}
}
