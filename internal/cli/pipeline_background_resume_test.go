//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func TestPipelineBackgroundResumeWorker(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) > i+3 && os.Args[i+1] == "__pipeline-worker" {
			root, id := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
			if err := pipeline.RunBackgroundWorker(context.Background(), root, id, os.Stdin); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func usePipelineResumeTestWorker(t *testing.T) {
	t.Helper()
	previous := pipeline.BackgroundWorkerFlags
	pipeline.BackgroundWorkerFlags = func() ([]string, error) {
		return []string{"-test.run=^TestPipelineBackgroundResumeWorker$", "--"}, nil
	}
	t.Cleanup(func() { pipeline.BackgroundWorkerFlags = previous })
}

func TestPipelineResumeBackgroundFlagDefaultsToForeground(t *testing.T) {
	cmd := newPipelineResumeCmd()
	value, err := cmd.Flags().GetBool("background")
	if err != nil || value {
		t.Fatalf("background flag missing or changed the foreground default: %v %v", value, err)
	}
}

func TestPipelineResumeBackgroundCommandRetainsRecoveryPolicy(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%v", reset), func(t *testing.T) {
			root := inPipelineControlProject(t)
			usePipelineResumeTestWorker(t)
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "cli-background-resume", Steps: []pipeline.Step{
				{ID: "once", Command: "printf duplicate >> once"},
				{ID: "finish", DependsOn: []string{"once"}, Command: "while [ ! -f release ]; do sleep 0.01; done; printf done > marker"},
			}}
			_, locator, err := pipeline.SnapshotWorkflow(context.Background(), root, workflow)
			if err != nil {
				t.Fatal(err)
			}
			prior := &pipeline.ExecutionState{
				RunID: "run-cli-background", WorkflowID: workflow.Name, WorkflowFile: locator,
				Session: "saved-session", Status: pipeline.StatusFailed, UpdatedAt: time.Now(),
				Steps: map[string]pipeline.StepResult{"once": {Status: pipeline.StatusCompleted, Output: "saved"}},
			}
			if err := pipeline.SaveState(root, prior); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "once"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = pipeline.RequestRunCancellation(ctx, root, prior.RunID)
			})
			cmd := newPipelineResumeCmd()
			cmd.SetContext(context.Background())
			if err := cmd.Flags().Set("background", "true"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Flags().Set("mode", "restart-failed"); err != nil {
				t.Fatal(err)
			}
			if reset {
				if err := cmd.Flags().Set("keep-state", "false"); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := cmd.RunE(cmd, []string{prior.RunID}); err != nil {
				t.Fatalf("CLI did not hand off recovery to the worker: %v", err)
			}
			var response struct {
				Success    bool   `json:"success"`
				Background bool   `json:"background"`
				RunID      string `json:"run_id"`
				Session    string `json:"session"`
				Status     string `json:"status"`
				Mode       string `json:"mode"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil || !response.Success || !response.Background || response.RunID != prior.RunID || response.Session != prior.Session || response.Status != "pending" || response.Mode != "restart-failed" {
				t.Fatalf("bad CLI startup envelope: %s %v", output.String(), err)
			}
			if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			completed := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				st, err := pipeline.LoadState(root, prior.RunID)
				if err == nil && st.Status == pipeline.StatusCompleted {
					owner, err := pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
					if err == nil {
						owner.Close()
						completed = true
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !completed {
				t.Fatal("detached CLI resume did not finish")
			}
			want := "x"
			if reset {
				want += "duplicate"
			}
			data, err := os.ReadFile(filepath.Join(root, "once"))
			if err != nil || string(data) != want {
				t.Fatalf("CLI lost explicit keep/reset policy: %q want %q: %v", data, want, err)
			}
		})
	}
}

func TestPipelineResumeBackgroundRejectsLiveOwner(t *testing.T) {
	root := inPipelineControlProject(t)
	usePipelineResumeTestWorker(t)
	previous := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previous })
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-held")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(root, ".ntm", "pipelines", "run-held.json")
	if err := os.WriteFile(path, []byte("checkpoint must not be parsed"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("background", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"run-held"}); !errors.Is(err, pipeline.ErrRunAlreadyOwned) {
		t.Fatalf("CLI lost the worker ownership conflict: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "checkpoint must not be parsed" {
		t.Fatal("rejected background resume changed the checkpoint")
	}
}

func TestPipelineBackgroundForeachCancelSettlesOwnedGroupsBeforeResume(t *testing.T) {
	root := inPipelineControlProject(t)
	pipelineRecoveryTmux(t, root, "2718", "")
	usePipelineResumeTestWorker(t)
	previousJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previousJSON })
	const workflow = `schema_version: "2.0"
name: bounded-foreach-cli
settings:
  limits:
    max_concurrent_foreach: 2
  on_cancel:
    - id: cleanup
      command: printf cleaned >> cleanup-effects
steps:
  - id: once
    command: printf once >> prefix-effects
  - id: fanout
    depends_on: [once]
    foreach:
      items: '[0,1,2,3,4,5,6,7]'
      parallel: true
      max_concurrent: 8
      steps:
        - id: work
          command: |
            printf 'start\n' >> item-${item}.effects
            if [ ! -f release ]; then
              sleep 60 &
              child=$!
              trap 'wait "$child"; exit 0' TERM
              printf '%s\n' "$child" > child-${item}.pid
              printf '%s\n' "$$" > leader-${item}.pid
              wait "$child"
            fi
            printf 'done\n' >> item-${item}.effects
  - id: finish
    depends_on: [fanout]
    command: printf done > completed-marker
`
	path := filepath.Join(root, "bounded-foreach.yaml")
	if err := os.WriteFile(path, []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	run := newPipelineRunCmd()
	run.SilenceUsage, run.SilenceErrors = true, true
	run.SetOut(io.Discard)
	run.SetErr(io.Discard)
	run.SetArgs([]string{path, "--session=delivery-session", "--background"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	output, err := captureStdout(t, func() error { return run.ExecuteContext(ctx) })
	cancel()
	if err != nil {
		t.Fatalf("CLI did not launch the bounded foreach workflow: %s %v", output, err)
	}
	var runID string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Run ID:") {
			runID = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Run ID:"))
		}
	}
	if runID == "" {
		t.Fatalf("CLI did not report the background run ID: %s", output)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = pipeline.RequestRunCancellation(ctx, root, runID)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			owner, err := pipeline.AcquireRunControl(context.Background(), root, runID)
			if err == nil {
				owner.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("background foreach worker retained ownership after test cleanup")
	})
	awaitState := func(want pipeline.ExecutionStatus) *pipeline.ExecutionState {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var last *pipeline.ExecutionState
		for time.Now().Before(deadline) {
			state, err := pipeline.LoadState(root, runID)
			if err == nil {
				last = state
				if state.Status == want {
					owner, err := pipeline.AcquireRunControl(context.Background(), root, runID)
					if err == nil {
						owner.Close()
						return state
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("background foreach did not settle as %s: %+v", want, last)
		return nil
	}
	var running *pipeline.ExecutionState
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, err := pipeline.LoadState(root, runID)
		ready := err == nil && state.Steps["once"].Status == pipeline.StatusCompleted
		for i := 0; ready && i < 2; i++ {
			_, childErr := os.Stat(filepath.Join(root, fmt.Sprintf("child-%d.pid", i)))
			_, leaderErr := os.Stat(filepath.Join(root, fmt.Sprintf("leader-%d.pid", i)))
			ready = childErr == nil && leaderErr == nil && state.CommandExecutions[fmt.Sprintf("fanout_iter%d_work", i)].Status == "running"
		}
		if ready {
			running = state
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running == nil {
		t.Fatal("the first two foreach command groups did not reach their barriers")
	}
	for i := 2; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("item-%d.effects", i))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("iteration %d exceeded the configured concurrency before cancellation: %v", i, err)
		}
	}
	prefix := running.CommandExecutions["once"]
	stop := newPipelineCancelCmd()
	stop.SilenceUsage, stop.SilenceErrors = true, true
	stop.SetOut(io.Discard)
	stop.SetErr(io.Discard)
	stop.SetArgs([]string{runID})
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	output, err = captureStdout(t, func() error { return stop.ExecuteContext(ctx) })
	cancel()
	if err != nil || !strings.Contains(output, "Cancellation acknowledged") {
		t.Fatalf("CLI cancellation was not acknowledged: %s %v", output, err)
	}
	stopped := awaitState(pipeline.StatusCancelled)
	if data, err := os.ReadFile(filepath.Join(root, "cleanup-effects")); err != nil || string(data) != "cleaned" {
		t.Fatalf("cancellation failed to run its cleanup hook: %q %v", data, err)
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("fanout_iter%d_work", i)
		if i >= 2 {
			if _, exists := stopped.CommandExecutions[id]; exists {
				t.Fatalf("queued iteration %d created a command receipt after cancellation", i)
			}
			if _, exists := stopped.Steps[id]; exists {
				t.Fatalf("queued iteration %d entered its body after cancellation", i)
			}
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("item-%d.effects", i))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("queued iteration %d produced effects after cancellation: %v", i, err)
			}
			continue
		}
		receipt := stopped.CommandExecutions[id]
		if receipt.Status != "settled" || receipt.SettledAt.IsZero() || receipt.Result.Status != pipeline.StatusCancelled {
			t.Fatalf("cancelled owned group retained an unresolved receipt: %+v", receipt)
		}
		for _, kind := range []string{"leader", "child"} {
			data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("%s-%d.pid", kind, i)))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid %s PID for iteration %d: %q %v", kind, i, data, err)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("owned %s %d survived acknowledged cancellation and worker settlement: %v", kind, pid, err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	resume := newPipelineResumeCmd()
	resume.SilenceUsage, resume.SilenceErrors = true, true
	resume.SetOut(io.Discard)
	resume.SetErr(io.Discard)
	resume.SetArgs([]string{runID, "--background", "--mode=restart-failed"})
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	output, err = captureStdout(t, func() error { return resume.ExecuteContext(ctx) })
	cancel()
	if err != nil {
		t.Fatalf("CLI could not explicitly resume the settled cancellation: %s %v", output, err)
	}
	final := awaitState(pipeline.StatusCompleted)
	if data, err := os.ReadFile(filepath.Join(root, "prefix-effects")); err != nil || string(data) != "once" || !final.CommandExecutions["once"].StartedAt.Equal(prefix.StartedAt) {
		t.Fatalf("resume replayed the completed prefix: %q %v", data, err)
	}
	for i := 0; i < 8; i++ {
		want := "start\ndone\n"
		if i < 2 {
			want = "start\n" + want
		}
		data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("item-%d.effects", i)))
		if err != nil || string(data) != want {
			t.Fatalf("resumed iteration %d repeated queued work or lost its explicit retry: %q, want %q: %v", i, data, want, err)
		}
		if receipt := final.CommandExecutions[fmt.Sprintf("fanout_iter%d_work", i)]; receipt.Status != "settled" || receipt.Result.Status != pipeline.StatusCompleted {
			t.Fatalf("resumed iteration %d left a stranded command receipt: %+v", i, receipt)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "completed-marker")); err != nil || string(data) != "done" {
		t.Fatalf("resumed foreach did not release its dependent step: %q %v", data, err)
	}
}
