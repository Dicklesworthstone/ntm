//go:build unix

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Re-exec the real production launcher/worker in separate Go processes. The
// launcher exits before the test releases the workflow's command rendezvous.
// A goroutine-only implementation cannot pass: it dies with the launcher.
func TestBackgroundProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	args := os.Args[separator+1:]
	if len(args) < 3 {
		return
	}
	mode, root, id := args[0], args[len(args)-2], args[len(args)-1]
	if mode == "__pipeline-worker" {
		if err := RunBackgroundWorker(context.Background(), root, id, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if mode == "cancel" {
		os.Exit(PrintPipelineCancel(id))
	}
	if mode != "launch" && mode != "robot-launch" && mode != "launch-failure" {
		return
	}
	BackgroundWorkerFlags = func() ([]string, error) {
		if mode == "launch-failure" {
			return []string{"--not-a-go-test-flag"}, nil
		}
		return []string{"-test.run=^TestBackgroundProcess$", "--"}, nil
	}
	path := filepath.Join(root, "workflow.yaml")
	if mode == "robot-launch" {
		os.Exit(PrintPipelineRun(PipelineRunOptions{WorkflowFile: path, Session: "background", ProjectDir: root, Background: true}))
	}
	workflow, validation, err := LoadAndValidate(path)
	if err != nil || !validation.Valid {
		fmt.Fprintln(os.Stderr, validation, err)
		os.Exit(1)
	}
	cfg := DefaultExecutorConfig("background")
	cfg.ProjectDir, cfg.WorkflowFile, cfg.RunID = root, path, id
	result := StartBackgroundPipeline(workflow, map[string]interface{}{"payload": "spaces ' quotes \n lines"}, cfg)
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if result.Error != "" {
		os.Exit(1)
	}
	os.Exit(0)
}

func backgroundFixture(t *testing.T, fail bool) string {
	t.Helper()
	root := t.TempDir()
	release := filepath.Join(root, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	last := "printf completed > marker"
	if fail {
		last = "exit 9"
	}
	command := "printf started > started; while [ ! -f release ]; do sleep 0.01; done; " + last
	// Use the native workflow shape. Several schema fields have different
	// JSON and YAML marshalers, so JSON-marshaling a Workflow is not a valid
	// way to make a YAML fixture even though JSON syntax is valid YAML.
	raw := fmt.Sprintf("schema_version: \"2.0\"\nname: detached-lifetime\nsteps:\n  - id: gate\n    command: %q\n", command)
	if err := os.WriteFile(filepath.Join(root, "workflow.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func backgroundHelper(t *testing.T, mode, root, id string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackgroundProcess$", "--", mode, root, id)
	cmd.Dir = root
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func awaitBackgroundState(t *testing.T, root, id string, wanted ExecutionStatus) *ExecutionState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := LoadState(root, id)
		if err == nil && st.Status == wanted {
			if wanted == StatusPending || wanted == StatusRunning {
				return st
			}
			// The executor may publish a terminal checkpoint before cleanup
			// returns. Wait for ownership retirement before TempDir cleanup.
			prefix, err := runControlPrefix(root, id)
			var owner runControlRecord
			if err == nil && readRunControl(prefix+".owner", &owner) == nil && !owner.Active {
				return st
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, err := LoadState(root, id)
	t.Fatalf("run did not reach %s: state=%+v error=%v", wanted, st, err)
	return nil
}

func TestBackgroundPipelineSurvivesLauncherExit(t *testing.T) {
	for _, mode := range []string{"launch", "robot-launch"} {
		t.Run(mode, func(t *testing.T) {
			root := backgroundFixture(t, false)
			raw, err := backgroundHelper(t, mode, root, "run-survives")
			if err != nil {
				t.Fatalf("launch: %v %s", err, raw)
			}
			var out struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("bad launch output: %v %s", err, raw)
			}
			if out.RunID == "" || (out.Status != "pending" && out.Status != "running") {
				t.Fatalf("launch outcome: %s", raw)
			}
			// CombinedOutput returned: the original ntm process is GONE.
			if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("workflow escaped the release gate")
			}
			if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			st := awaitBackgroundState(t, root, out.RunID, StatusCompleted)
			if data, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(data) != "completed" {
				t.Fatalf("work did not survive launcher exit: %q %v", data, err)
			}
			if st.WorkflowFile == filepath.Join(root, "workflow.yaml") {
				t.Fatal("worker did not freeze its resumable workflow")
			}
			if _, _, err := LoadResumeWorkflow(st.WorkflowFile); err != nil {
				t.Fatalf("unresumable snapshot: %v", err)
			}
		})
	}
}

func TestBackgroundPipelineFailurePersistsRealOutcome(t *testing.T) {
	root := backgroundFixture(t, true)
	raw, err := backgroundHelper(t, "launch", root, "run-fails")
	if err != nil {
		t.Fatalf("launch: %v %s", err, raw)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	st := awaitBackgroundState(t, root, "run-fails", StatusFailed)
	if len(st.Errors) == 0 {
		t.Fatal("failed workflow has no error evidence")
	}
}

func TestBackgroundPipelineCanBeCanceledFromAnotherProcess(t *testing.T) {
	root := backgroundFixture(t, false)
	raw, err := backgroundHelper(t, "launch", root, "run-cancel")
	if err != nil {
		t.Fatalf("launch: %v %s", err, raw)
	}
	awaitBackgroundState(t, root, "run-cancel", StatusRunning)
	raw, err = backgroundHelper(t, "cancel", root, "run-cancel")
	if err != nil {
		t.Fatalf("public cancel surface: %v %s", err, raw)
	}
	var response PipelineCancelOutput
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("bad cancel output: %v %s", err, raw)
	}
	if !response.Success || response.Status != "cancellation_requested" {
		t.Fatalf("cancel fabricated a terminal outcome: %+v", response)
	}
	awaitBackgroundState(t, root, "run-cancel", StatusCancelled)
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled worker passed unreleased gate")
	}
}

func TestBackgroundPipelineRefusesDuplicateRunIdentity(t *testing.T) {
	root := backgroundFixture(t, false)
	raw, err := backgroundHelper(t, "launch", root, "run-once")
	if err != nil {
		t.Fatalf("launch: %v %s", err, raw)
	}
	awaitBackgroundState(t, root, "run-once", StatusRunning)
	path := filepath.Join(pipelineStateDir(root), "background", "run-once.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = backgroundHelper(t, "launch", root, "run-once")
	if err == nil {
		t.Fatalf("duplicate launch succeeded: %s", raw)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("duplicate launch replaced immutable request")
	}
	if err := RequestRunCancellation(context.Background(), root, "run-once"); err != nil {
		t.Fatal(err)
	}
	awaitBackgroundState(t, root, "run-once", StatusCancelled)
}

func TestBackgroundStartupFailureIsNotSuccess(t *testing.T) {
	root := backgroundFixture(t, false)
	raw, err := backgroundHelper(t, "launch-failure", root, "run-no-worker")
	if err == nil {
		t.Fatalf("invalid executable flags reported successful launch: %s", raw)
	}
	var out PipelineExecution
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad failure envelope: %v %s", err, raw)
	}
	if out.Status != "failed" || out.Error == "" || out.RunID != "run-no-worker" {
		t.Fatalf("lost launch failure: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed startup dispatched work")
	}
}

func writeBackgroundRequestFixture(t *testing.T, root, id string) backgroundRequest {
	t.Helper()
	workflow, _, err := LoadAndValidate(filepath.Join(root, "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, path, err := SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := prepareBackgroundDirectory(normalizeLockRoot(root))
	if err != nil {
		t.Fatal(err)
	}
	req := backgroundRequest{Version: backgroundRequestVersion, Token: strings.Repeat("a", 32), ExpiresAt: time.Now().Add(time.Second), Config: backgroundExecutorConfig{RunID: id, ProjectDir: normalizeLockRoot(root), WorkflowFile: path, Session: "background", GlobalTimeout: time.Minute, DefaultTimeout: time.Minute, ProgressInterval: time.Second}}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewBackgroundFile(filepath.Join(dir, id+".json"), data); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestBackgroundWorkerRequiresExplicitStartupAuthorization(t *testing.T) {
	for _, payload := range []string{"", "wrong\n", strings.Repeat("a", 32)} {
		t.Run(fmt.Sprintf("bytes-%d", len(payload)), func(t *testing.T) {
			root := backgroundFixture(t, false)
			writeBackgroundRequestFixture(t, root, "run-uncommitted")
			err := RunBackgroundWorker(context.Background(), root, "run-uncommitted", io.NopCloser(strings.NewReader(payload)))
			if err == nil {
				t.Fatal("worker ran without a complete matching token")
			}
			awaitBackgroundState(t, root, "run-uncommitted", StatusFailed)
			if _, err := os.Stat(filepath.Join(root, "started")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("uncommitted worker executed a step")
			}
			control, err := AcquireRunControl(context.Background(), root, "run-uncommitted")
			if err != nil {
				t.Fatalf("failed startup leaked ownership: %v", err)
			}
			control.Close()
		})
	}
}

func TestBackgroundWorkerDeadlineClosesAuthorizationPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := awaitBackgroundAuthorization(ctx, reader, "unused"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if _, err := writer.Write([]byte("unused\n")); err == nil {
		t.Fatal("authorization reader remained open after timeout")
	}
}

func TestBackgroundRequestAndLogPermissions(t *testing.T) {
	root := backgroundFixture(t, false)
	raw, err := backgroundHelper(t, "launch", root, "run-permissions")
	if err != nil {
		t.Fatalf("launch: %v %s", err, raw)
	}
	for _, ext := range []string{".json", ".ready", ".log"} {
		info, err := os.Stat(filepath.Join(pipelineStateDir(root), "background", "run-permissions"+ext))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private background artifact %s has mode %o", ext, info.Mode().Perm())
		}
	}
	if err := RequestRunCancellation(context.Background(), root, "run-permissions"); err != nil {
		t.Fatal(err)
	}
	awaitBackgroundState(t, root, "run-permissions", StatusCancelled)
}

func TestBackgroundDryRunCreatesNoWorkerArtifacts(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: "2.0", Name: "dry-background", Steps: []Step{{ID: "never", Command: "exit 99"}}}
	cfg := DefaultExecutorConfig("background")
	cfg.ProjectDir = root
	cfg.DryRun = true
	out := StartBackgroundPipeline(workflow, nil, cfg)
	if out.Error != "" || out.Status != "completed" {
		t.Fatalf("dry run failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote worker artifacts: %v", err)
	}
}

func TestBackgroundTemplateResolutionPreservesSourcePrecedence(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workflows")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(source, "prompt.md"), filepath.Join(root, "prompt.md"), filepath.Join(root, "fallback.md")} {
		if err := os.WriteFile(path, []byte("prompt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultExecutorConfig("templates")
	cfg.ProjectDir, cfg.WorkflowFile = root, filepath.Join(source, "workflow.yaml")
	workflow := &Workflow{
		Steps:             []Step{{ID: "beside-source", Template: "prompt.md"}, {ID: "project-fallback", Template: "fallback.md"}},
		PostPipelineSteps: []Step{{ID: "after", Template: "prompt.md"}},
	}
	workflow.Settings.OnCancel = []Step{{ID: "cleanup", Template: "fallback.md"}}
	workflow.Steps = append(workflow.Steps, Step{ID: "parallel"})
	workflow.Steps[2].Parallel.Steps = []Step{{ID: "nested", Template: "prompt.md"}}
	if err := resolveBackgroundTemplates(workflow, NewExecutor(cfg)); err != nil {
		t.Fatal(err)
	}
	if workflow.Steps[0].Template != filepath.Join(source, "prompt.md") || workflow.Steps[1].Template != filepath.Join(root, "fallback.md") {
		t.Fatalf("changed template search order: %+v", workflow.Steps)
	}
	if workflow.PostPipelineSteps[0].Template != filepath.Join(source, "prompt.md") || workflow.Settings.OnCancel[0].Template != filepath.Join(root, "fallback.md") {
		t.Fatal("lost lifecycle template paths")
	}
	if workflow.Steps[2].Parallel.Steps[0].Template != filepath.Join(source, "prompt.md") {
		t.Fatal("lost nested template path")
	}
	workflow.Steps[0].Template = "generated-${item}.md"
	if err := resolveBackgroundTemplates(workflow, NewExecutor(cfg)); err == nil {
		t.Fatal("guessed a future relative template location")
	}
	workflow.Steps[0].Template = filepath.Join(source, "generated-${item}.md")
	if err := resolveBackgroundTemplates(workflow, NewExecutor(cfg)); err != nil {
		t.Fatalf("absolute generated template rejected: %v", err)
	}
}
