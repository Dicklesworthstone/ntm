//go:build unix

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackgroundResumeProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--" || len(os.Args) < i+4 {
			continue
		}
		args := os.Args[i+1:]
		mode, root, id := args[0], args[len(args)-2], args[len(args)-1]
		switch mode {
		case "__pipeline-worker":
			if err := RunBackgroundWorker(context.Background(), root, id, os.Stdin); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "resume":
			BackgroundWorkerFlags = func() ([]string, error) {
				return []string{"-test.run=^TestBackgroundResumeProcess$", "--"}, nil
			}
			result, err := StartBackgroundResume(context.Background(), root, id, "", ResumeOptions{Mode: ResumeModeRestartFailed})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			_ = json.NewEncoder(os.Stdout).Encode(result)
			os.Exit(0)
		case "cancel":
			if err := RequestRunCancellation(context.Background(), root, id); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
		return
	}
}

func resumeTestCommand(root, requestID string) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundResumeProcess$", "--", "__pipeline-worker", root, requestID)
	cmd.Dir = root
	return cmd, nil
}

func writeResumeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func resumeFixture(t *testing.T, id string) (string, *ExecutionState) {
	t.Helper()
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "detached-resume", Steps: []Step{
		{ID: "done", Command: "printf duplicate >> once"},
		{ID: "remaining", DependsOn: []string{"done"}, Command: "printf started > started; while [ ! -f release ]; do sleep 0.01; done; printf resumed > marker"},
	}}
	_, path, err := SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	state := &ExecutionState{
		RunID: id, WorkflowID: workflow.Name, WorkflowFile: path, Session: "saved-session",
		Status: StatusFailed, StartedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now(),
		Steps:     map[string]StepResult{"done": {Status: StatusCompleted, Output: "preserved output"}},
		Variables: map[string]interface{}{"payload": "spaces ' quotes \n lines"},
	}
	if err := SaveState(root, state); err != nil {
		t.Fatal(err)
	}
	writeResumeTestFile(t, filepath.Join(root, "once"), "x")
	for _, ext := range []string{"json", "ready", "log", "events"} {
		writeResumeTestFile(t, filepath.Join(pipelineStateDir(root), "background", id+"."+ext), "original launch evidence")
	}
	t.Cleanup(func() {
		// Let any successfully launched worker settle before TempDir cleanup.
		_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = RequestRunCancellation(ctx, root, id)
		prefix, err := runControlPrefix(root, id)
		if err != nil {
			return
		}
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			var owner runControlRecord
			if err := readRunControl(prefix+".owner", &owner); err != nil || !owner.Active {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("background resume did not retire ownership during cleanup")
	})
	return root, state
}

func awaitResumeState(t *testing.T, root, id string, status ExecutionStatus) *ExecutionState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, err := LoadState(root, id)
		if err == nil && state.Status == status {
			if status == StatusRunning {
				return state
			}
			prefix, err := runControlPrefix(root, id)
			var owner runControlRecord
			if err == nil && readRunControl(prefix+".owner", &owner) == nil && !owner.Active {
				return state
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, err := LoadState(root, id)
	t.Fatalf("resume did not reach %s: %+v %v", status, state, err)
	return nil
}

func TestBackgroundResumeSurvivesLauncherExit(t *testing.T) {
	root, prior := resumeFixture(t, "run-resume-exit")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	launcher := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackgroundResumeProcess$", "--", "resume", root, prior.RunID)
	launcher.Dir = root
	launcher.WaitDelay = time.Second
	raw, err := launcher.CombinedOutput()
	if err != nil {
		t.Fatalf("resume launcher: %v %s", err, raw)
	}
	var out PipelineExecution
	if err := json.Unmarshal(raw, &out); err != nil || out.RunID != prior.RunID || out.Session != prior.Session || out.WorkflowID != prior.WorkflowID || out.Status != "pending" {
		t.Fatalf("resume startup identity: %+v %v raw=%s", out, err, raw)
	}
	// CombinedOutput has returned: the launcher process is gone.
	awaitResumeState(t, root, prior.RunID, StatusRunning)
	writeResumeTestFile(t, filepath.Join(root, "release"), "go")
	state := awaitResumeState(t, root, prior.RunID, StatusCompleted)
	if state.Steps["done"].Output != "preserved output" || state.WorkflowFile != prior.WorkflowFile || state.Variables["payload"] != prior.Variables["payload"] {
		t.Fatalf("resume replaced saved work, input or workflow identity: %+v", state)
	}
	for name, expected := range map[string]string{"once": "x", "marker": "resumed"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != expected {
			t.Fatalf("%s = %q, expected %q: %v", name, data, expected, err)
		}
	}
	for _, ext := range []string{"json", "ready", "log", "events"} {
		data, err := os.ReadFile(filepath.Join(pipelineStateDir(root), "background", prior.RunID+"."+ext))
		if err != nil || string(data) != "original launch evidence" {
			t.Fatal("resume overwrote the original launch evidence")
		}
	}
}

func TestBackgroundResumeCancellationAndNewAttempt(t *testing.T) {
	root, prior := resumeFixture(t, "run-resume-cancel")
	for attempt := 0; attempt < 2; attempt++ {
		out, err := startDetachedResume(context.Background(), root, prior.RunID, "", ResumeOptions{}, resumeTestCommand)
		if err != nil || out.RunID != prior.RunID {
			t.Fatalf("resume attempt %d: %+v %v", attempt, out, err)
		}
		awaitResumeState(t, root, prior.RunID, StatusRunning)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		controller := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackgroundResumeProcess$", "--", "cancel", root, prior.RunID)
		raw, err := controller.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("fresh-process cancellation: %v %s", err, raw)
		}
		state := awaitResumeState(t, root, prior.RunID, StatusCancelled)
		if state.Steps["done"].Output != prior.Steps["done"].Output {
			t.Fatal("cancelled recovery lost completed output")
		}
	}
	files, err := filepath.Glob(filepath.Join(pipelineStateDir(root), "background", "resume-*.claimed"))
	if err != nil || len(files) != 2 {
		t.Fatalf("attempts did not receive independent identities: %v %v", files, err)
	}
	for _, claim := range files {
		path := strings.TrimSuffix(claim, ".claimed") + ".events"
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("resume attempt has no progress journal: %v", err)
		}
		_, finished, readErr := readBackgroundProgress(context.Background(), file, 0, func(ProgressEvent) {})
		info, statErr := file.Stat()
		closeErr := file.Close()
		if readErr != nil || !finished || statErr != nil || closeErr != nil {
			t.Fatalf("resume released ownership before draining its journal: finished=%v read=%v stat=%v close=%v", finished, readErr, statErr, closeErr)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("resume progress journal is not private")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled work passed the release gate: %v", err)
	}
}

// The shared launcher must preserve REST's startup-only context contract
// when adding detached resume; cancellation before Start must not spawn work.
func TestBackgroundResumeSharedLauncherPreservesStartupContext(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultExecutorConfig("context")
	cfg.ProjectDir, cfg.RunID = root, "run-context"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := LaunchBackgroundPipeline(ctx, nil, nil, cfg); out != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled public launch: %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled launch wrote artifacts: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "startup-context", Steps: []Step{{ID: "no", Command: "exit 99"}}}
	var child *exec.Cmd
	out, err := startDetachedPipeline(workflow, nil, cfg, func(root, id string) (*exec.Cmd, error) {
		cancel()
		child = exec.Command("/bin/sh", "-c", "printf spawned > must-not-spawn")
		child.Dir = root
		return child, nil
	}, ctx)
	if out != nil || !errors.Is(err, context.Canceled) || child == nil || child.Process != nil {
		t.Fatalf("cancelled startup spawned a worker: %+v %v child=%+v", out, err, child)
	}
}

func TestBackgroundResumeRefusesLiveOwnerBeforeReadingState(t *testing.T) {
	root, prior := resumeFixture(t, "run-resume-owned")
	owner, err := AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	writeResumeTestFile(t, pipelineStatePath(root, prior.RunID), "must not parse this checkpoint")
	out, err := startDetachedResume(context.Background(), root, prior.RunID, "", ResumeOptions{}, resumeTestCommand)
	if out != nil || !errors.Is(err, ErrRunAlreadyOwned) {
		t.Fatalf("owner contention lost its typed refusal or read the checkpoint: %+v %v", out, err)
	}
	data, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil || string(data) != "must not parse this checkpoint" {
		t.Fatal("rejected resume modified the checkpoint")
	}
}

func TestBackgroundResumeConcurrentAttemptsHaveOneOwner(t *testing.T) {
	root, prior := resumeFixture(t, "run-resume-concurrent")
	const attempts = 6
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := startDetachedResume(context.Background(), root, prior.RunID, "", ResumeOptions{}, resumeTestCommand)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrRunAlreadyOwned) {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d workers owned one saved run", winners)
	}
	writeResumeTestFile(t, filepath.Join(root, "release"), "go")
	awaitResumeState(t, root, prior.RunID, StatusCompleted)
}

func resumeRequestFixture(t *testing.T, root string, prior *ExecutionState, opts ResumeOptions) (backgroundRequest, string) {
	t.Helper()
	cfg := DefaultExecutorConfig("")
	req := backgroundRequest{Version: backgroundRequestVersion, Token: strings.Repeat("a", 32), Resume: true, ExpiresAt: time.Now().Add(10 * time.Second), Config: backgroundExecutorConfig{
		ProjectDir: root, RunID: prior.RunID, DefaultTimeout: cfg.DefaultTimeout,
		GlobalTimeout: cfg.GlobalTimeout, ProgressInterval: cfg.ProgressInterval, ResumeOptions: opts,
	}}
	id := "resume-" + req.Token
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewBackgroundFile(filepath.Join(pipelineStateDir(root), "background", id+".json"), data); err != nil {
		t.Fatal(err)
	}
	return req, id
}

func TestBackgroundResumeAuthorizationPreservesCheckpointAndConsumesAttempt(t *testing.T) {
	for _, input := range []string{"", "wrong-token\n"} {
		t.Run(fmt.Sprintf("input=%q", input), func(t *testing.T) {
			root, prior := resumeFixture(t, "run-unauthorized")
			before, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
			if err != nil {
				t.Fatal(err)
			}
			req, id := resumeRequestFixture(t, root, prior, ResumeOptions{Reset: true})
			err = RunBackgroundWorker(context.Background(), root, id, io.NopCloser(strings.NewReader(input)))
			if err == nil || !strings.Contains(err.Error(), "not authorized") {
				t.Fatalf("unauthorized recovery accepted: %v", err)
			}
			after, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed authorization changed the saved checkpoint")
			}
			readyPath := filepath.Join(pipelineStateDir(root), "background", id+".ready")
			receipt, err := os.ReadFile(readyPath)
			if err != nil {
				t.Fatal(err)
			}
			err = RunBackgroundWorker(context.Background(), root, id, io.NopCloser(strings.NewReader(req.Token+"\n")))
			if err == nil || !strings.Contains(err.Error(), "claim background resume attempt") {
				t.Fatalf("old attempt was reused: %v", err)
			}
			if data, err := os.ReadFile(readyPath); err != nil || !bytes.Equal(data, receipt) {
				t.Fatal("replay overwrote the original attempt receipt")
			}
			owner, err := AcquireRunControl(context.Background(), root, prior.RunID)
			if err != nil {
				t.Fatalf("aborted authorization leaked run ownership: %v", err)
			}
			owner.Close()
		})
	}
}

func TestBackgroundResumeRefusalsPreserveRecoveryEvidence(t *testing.T) {
	for _, failure := range []string{"completed", "workflow_identity", "corrupt_workflow", "missing_workflow", "stale", "roster"} {
		t.Run(failure, func(t *testing.T) {
			root, prior := resumeFixture(t, "run-refused")
			opts, session := ResumeOptions{}, ""
			switch failure {
			case "completed":
				prior.Status = StatusCompleted
			case "workflow_identity":
				prior.WorkflowID = "other-workflow"
			case "corrupt_workflow":
				writeResumeTestFile(t, prior.WorkflowFile, "corrupt evidence")
			case "missing_workflow":
				if err := os.Rename(prior.WorkflowFile, prior.WorkflowFile+".saved"); err != nil {
					t.Fatal(err)
				}
			case "stale":
				prior.StartedAt, prior.UpdatedAt = time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)
				opts.MaxResumeAge = time.Hour
			case "roster":
				session = "different-session"
			}
			if err := SaveState(root, prior); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
			if err != nil {
				t.Fatal(err)
			}
			out, err := startDetachedResume(context.Background(), root, prior.RunID, session, opts, resumeTestCommand)
			if err == nil || out != nil {
				t.Fatalf("invalid resume accepted: %+v %v", out, err)
			}
			after, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("resume refusal rewrote evidence")
			}
			if _, err := os.Stat(filepath.Join(root, "started")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected resume dispatched work")
			}
		})
	}
}

func TestBackgroundResumeExplicitResetAndRosterOverride(t *testing.T) {
	root, prior := resumeFixture(t, "run-reset")
	writeResumeTestFile(t, filepath.Join(root, "release"), "go")
	out, err := startDetachedResume(context.Background(), root, prior.RunID, "new-session", ResumeOptions{Reset: true, OnRosterChange: ResumeRosterProceed}, resumeTestCommand)
	if err != nil || out.Session != "new-session" {
		t.Fatalf("explicit recovery policy was lost: %+v %v", out, err)
	}
	state := awaitResumeState(t, root, prior.RunID, StatusCompleted)
	data, err := os.ReadFile(filepath.Join(root, "once"))
	if err != nil || string(data) != "xduplicate" || state.Session != "new-session" {
		t.Fatalf("reset/roster policy was not applied: %+v %q %v", state, data, err)
	}
}

func TestBackgroundResumeValidationDoesNotLaunch(t *testing.T) {
	for _, name := range []string{"cancelled", "bad_id", "empty_root", "bad_mode"} {
		t.Run(name, func(t *testing.T) {
			root, id, opts := t.TempDir(), "run-valid", ResumeOptions{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "cancelled":
				cancel()
			case "bad_id":
				id = "../escape"
			case "empty_root":
				root = ""
			case "bad_mode":
				opts.Mode = "invalid"
			}
			called := false
			_, err := startDetachedResume(ctx, root, id, "", opts, func(string, string) (*exec.Cmd, error) {
				called = true
				return nil, errors.New("must not launch")
			})
			if err == nil || called {
				t.Fatalf("validation reached the process builder: %v called=%v", err, called)
			}
			if root != "" {
				if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected request created artifacts")
				}
			}
		})
	}
}

func TestBackgroundResumeAuthorizationDeadlinePreservesCheckpoint(t *testing.T) {
	root, prior := resumeFixture(t, "run-auth-deadline")
	_, id := resumeRequestFixture(t, root, prior, ResumeOptions{Reset: true})
	before, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = RunBackgroundWorker(ctx, root, id, input)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("authorization wait did not expire: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("authorization timeout changed the saved checkpoint")
	}
}

func TestBackgroundResumeFailedLaunchCanUseNewAttempt(t *testing.T) {
	root, prior := resumeFixture(t, "run-launch-retry")
	before, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = startDetachedResume(context.Background(), root, prior.RunID, "", ResumeOptions{}, func(string, string) (*exec.Cmd, error) {
		return exec.Command("/bin/sh", "-c", "exit 7"), nil
	})
	if err == nil {
		t.Fatal("failed launcher reported success")
	}
	after, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed launch changed its recovery checkpoint")
	}
	writeResumeTestFile(t, filepath.Join(root, "release"), "go")
	if _, err := startDetachedResume(context.Background(), root, prior.RunID, "", ResumeOptions{}, resumeTestCommand); err != nil {
		t.Fatalf("a failed launch permanently prevented recovery: %v", err)
	}
	awaitResumeState(t, root, prior.RunID, StatusCompleted)
	attempts, err := filepath.Glob(filepath.Join(pipelineStateDir(root), "background", "resume-*.json"))
	if err != nil || len(attempts) != 2 {
		t.Fatalf("retry replaced rather than preserved the failed attempt: %v %v", attempts, err)
	}
}

func TestBackgroundResumeSharedLauncherStillCreatesNewRuns(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultExecutorConfig("fresh-session")
	cfg.ProjectDir, cfg.RunID = root, "run-fresh"
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "fresh-workflow", Steps: []Step{{ID: "mark", Command: "printf fresh > marker"}}}
	out, err := startDetachedPipeline(workflow, nil, cfg, resumeTestCommand)
	if err != nil || out.RunID != cfg.RunID || out.Session != cfg.Session || out.WorkflowID != workflow.Name {
		t.Fatalf("shared launcher regressed initial creation: %+v %v", out, err)
	}
	awaitResumeState(t, root, cfg.RunID, StatusCompleted)
	data, err := os.ReadFile(filepath.Join(root, "marker"))
	if err != nil || string(data) != "fresh" {
		t.Fatalf("new-run workload: %q %v", data, err)
	}
	if _, err := startDetachedPipeline(workflow, nil, cfg, resumeTestCommand); err == nil {
		t.Fatal("new-run creation replaced an existing completed run")
	}
}
