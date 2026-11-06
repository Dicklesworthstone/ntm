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

// A killed executor cannot run its defers: its shell group survives while the
// kernel releases RunControl. Exercise the robot launcher and real detached
// resume worker, so an in-memory guard or a lock-only fix cannot pass.
func TestRobotPipelineCrashRequiresExplicitCommandReplay(t *testing.T) {
	root := t.TempDir()
	command := "printf 'attempt\\n' >> attempts; i=0; while [ ! -f release ] && [ \"$i\" -lt 1000 ]; do i=$((i+1)); sleep 0.01; done; printf 'finished\\n' >> finished; printf recovered"
	raw := fmt.Sprintf("schema_version: \"2.0\"\nname: command-crash\nsteps:\n  - id: command\n    command: %q\n", command)
	if err := os.WriteFile(filepath.Join(root, "workflow.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlledForegroundProcess$", "--", "foreground", root)
	cmd.Dir = root
	logFile, err := os.CreateTemp(t.TempDir(), "crash-log-")
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
		_ = os.WriteFile(filepath.Join(root, "release"), []byte("release"), 0600)
		cancel()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(root, "finished")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	var prior *ExecutionState
	deadline := time.Now().Add(5 * time.Second)
	for prior == nil && time.Now().Before(deadline) {
		entries, _ := os.ReadDir(pipelineStateDir(root))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			st, err := LoadState(root, strings.TrimSuffix(entry.Name(), ".json"))
			if err == nil && st.Status == StatusRunning && st.InFlightSteps["command"].Kind == StepKindCommand {
				if data, err := os.ReadFile(filepath.Join(root, "attempts")); err == nil && string(data) == "attempt\n" {
					prior = st
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if prior == nil {
		t.Fatal("robot pipeline never durably launched its command")
	}
	if err := cmd.Process.Kill(); err != nil { // Kill only the executor, not its command group.
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hard-killed robot pipeline exited successfully")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hard-killed robot process did not exit")
	}
	before, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	worker := func(project, id string) (*exec.Cmd, error) {
		return exec.Command(os.Args[0], "-test.run=^TestBackgroundProcess$", "--", "__pipeline-worker", project, id), nil
	}
	if _, err := startDetachedResume(ctx, root, prior.RunID, "", ResumeOptions{}, false, worker); err == nil || !strings.Contains(err.Error(), "unresolved launch") || !strings.Contains(err.Error(), "may still be running") {
		t.Fatalf("ordinary resume did not reject the ambiguous command: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected recovery rewrote the command's durable evidence")
	}
	if data, err := os.ReadFile(filepath.Join(root, "attempts")); err != nil || string(data) != "attempt\n" {
		t.Fatalf("ordinary resume repeated the external side effect: %q %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(root, "finished")); err == nil && string(data) == "finished\n" {
			finished = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !finished {
		t.Fatal("original command did not survive the executor crash and finish its side effects")
	}
	// Only after settling the old work do we deliberately authorize repetition.
	if _, err := startDetachedResume(ctx, root, prior.RunID, "", ResumeOptions{Mode: ResumeModeRestartFailed}, false, worker); err != nil {
		t.Fatalf("explicit replay did not start: %v", err)
	}
	final := awaitBackgroundState(t, root, prior.RunID, StatusCompleted)
	if data, err := os.ReadFile(filepath.Join(root, "attempts")); err != nil || string(data) != "attempt\nattempt\n" {
		t.Fatalf("explicit replay did not run exactly once: %q %v", data, err)
	}
	if final.Steps["command"].Output != "recovered" {
		t.Fatalf("explicit replay lost its durable result: %+v", final)
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
