package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func inPipelineControlProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return root
}

func TestPipelineResumeRejectsLiveOwnerBeforeReadingState(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-held")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(root, ".ntm", "pipelines", "run-held.json")
	const untouched = "not even valid checkpoint JSON"
	if err := os.WriteFile(path, []byte(untouched), 0600); err != nil {
		t.Fatal(err)
	}

	for _, asJSON := range []bool{false, true} {
		jsonOutput = asJSON
		cmd := newPipelineResumeCmd()
		cmd.SetContext(context.Background())
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		previousStdout := os.Stdout
		os.Stdout = write
		err = cmd.RunE(cmd, []string{"run-held"})
		os.Stdout = previousStdout
		_ = write.Close()
		raw, readErr := io.ReadAll(read)
		_ = read.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err == nil || !strings.Contains(err.Error(), "pipeline run has a live owner") {
			t.Fatalf("resume read state or entered executor without ownership (json=%v): %v %s", asJSON, err, raw)
		}
		if asJSON {
			var out struct {
				Success bool   `json:"success"`
				Code    string `json:"error_code"`
			}
			if err := json.Unmarshal(raw, &out); err != nil || out.Success || out.Code != "PIPELINE_RUNNING" {
				t.Fatalf("bad ownership error envelope: %s %v", raw, err)
			}
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != untouched {
		t.Fatal("rejected resume changed checkpoint")
	}
}

func TestPipelineResumeMissingStateReleasesOwnership(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"run-missing"}); err == nil {
		t.Fatal("missing state resumed")
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-missing")
	if err != nil {
		t.Fatalf("failed resume leaked its lock: %v", err)
	}
	owner.Close()
}

func TestPipelineResumeRejectsDamagedManagedWorkflow(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	// Command-only workflow: no agent is driven. The session probe is a
	// deterministic tmux fixture; the workflow loader and hash check are real.
	fakeTmux := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nif [ \"$1\" = -V ]; then echo 'tmux 3.4'; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", fakeTmux)
	dir := filepath.Join(root, ".ntm", "pipelines", "workflow-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sha256-"+strings.Repeat("0", 64)+".yaml")
	workflow := "schema_version: \"2.0\"\nname: damaged\nsteps:\n  - id: mark\n    command: 'echo unsafe > resumed-marker'\n"
	if err := os.WriteFile(path, []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	prior := &pipeline.ExecutionState{RunID: "run-damaged", WorkflowID: "damaged", WorkflowFile: path, Session: "snapshot-session", Status: pipeline.StatusFailed, Steps: map[string]pipeline.StepResult{}}
	if err := pipeline.SaveState(root, prior); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	err = cmd.RunE(cmd, []string{prior.RunID})
	if err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("snapshot integrity was not enforced: %v", err)
	}
	if after, err := os.ReadFile(checkpointPath); err != nil || string(after) != string(before) {
		t.Fatal("snapshot rejection changed checkpoint")
	}
	if _, err := os.Stat(filepath.Join(root, "resumed-marker")); !os.IsNotExist(err) {
		t.Fatal("damaged workflow was executed")
	}
}

// Build the checkpoint through the real executor, including its one transport
// and submission verification. The watcher only cancels after delivery is on
// disk, so resume has to recover an actual interrupted dispatch.
func interruptedPipelineAgentDelivery(t *testing.T, root, kind string) *pipeline.ExecutionState {
	t.Helper()
	step := pipeline.Step{ID: "agent", Pane: pipeline.PaneSpec{Index: 1}, Prompt: "Work on this request", Wait: pipeline.WaitTime, Timeout: pipeline.Duration{Duration: time.Second}}
	if kind == "template" {
		path := filepath.Join(root, "review.md")
		if err := os.WriteFile(path, []byte("Work on this template request"), 0600); err != nil {
			t.Fatal(err)
		}
		step.Prompt, step.Template = "", path
	}
	workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "agent-recovery", Steps: []pipeline.Step{
		step,
		{ID: "consume", Command: "cat", Stdin: "${steps.agent.output}", DependsOn: []string{"agent"}},
	}}
	frozen, path, err := pipeline.SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	mock := pipeline.NewMockTmuxClient(tmux.Pane{ID: "%17", Index: 1, NTMIndex: 1, PID: 2718, Type: tmux.AgentClaude, Width: 120, Height: 40})
	t.Cleanup(mock.Reset)
	if err := mock.SetPaneOutput("%17", "OLD-ANSWER\n"); err != nil {
		t.Fatal(err)
	}
	cfg := pipeline.DefaultExecutorConfig("delivery-session")
	cfg.ProjectDir, cfg.WorkflowFile, cfg.RunID = root, path, pipeline.GenerateRunID()
	executor := pipeline.NewExecutor(cfg)
	executor.SetTmuxClient(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	watchDone := make(chan struct{})
	delivered := false
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := pipeline.LoadState(root, cfg.RunID)
				if err == nil && state.AgentDeliveries["agent"].Status == "delivered" {
					delivered = true
					cancel()
					return
				}
			}
		}
	}()
	state, runErr := executor.Run(ctx, frozen, nil, nil)
	cancel()
	<-watchDone
	if !delivered || runErr == nil || state == nil || state.Status != pipeline.StatusCancelled {
		t.Fatalf("initial dispatch did not stop after confirmed delivery: delivered=%t state=%+v err=%v", delivered, state, runErr)
	}
	history, err := mock.PasteHistory("%17")
	if err != nil || len(history) != 1 || len(mock.VerificationHistory()) != 1 {
		t.Fatalf("initial dispatch was not sent and verified exactly once: pastes=%+v verifications=%+v err=%v", history, mock.VerificationHistory(), err)
	}
	persisted, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || persisted.AgentDeliveries["agent"].Status != "delivered" {
		t.Fatalf("initial delivery was not durably resumable: %+v %v", persisted, err)
	}
	return persisted
}

func pipelineRecoveryTmux(t *testing.T, root, panePID, transcript string) string {
	t.Helper()
	logPath := filepath.Join(root, "recovery-tmux-calls")
	if err := os.WriteFile(filepath.Join(root, "recovery-pane-output"), []byte(transcript), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_PIPELINE_RECOVERY_ROOT", root)
	t.Setenv("NTM_PIPELINE_RECOVERY_PID", panePID)
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_PIPELINE_RECOVERY_ROOT/recovery-tmux-calls"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  list-sessions) echo 'delivery-session_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000' ;;
  list-panes) printf '%%17_NTM_SEP_1_NTM_SEP_delivery-session__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$NTM_PIPELINE_RECOVERY_PID" ;;
  capture-pane) cat "$NTM_PIPELINE_RECOVERY_ROOT/recovery-pane-output" ;;
  display-message) printf '%s\n' "$NTM_PIPELINE_RECOVERY_ROOT" ;;
  load-buffer|paste-buffer|send-keys) exit 97 ;;
esac
`
	bin := filepath.Join(root, "recovery-tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	return logPath
}

func TestPipelineResumeRecoversConfirmedAgentDeliveryWithoutResending(t *testing.T) {
	for _, kind := range []string{"prompt", "template"} {
		t.Run(kind, func(t *testing.T) {
			root := inPipelineControlProject(t)
			prior := interruptedPipelineAgentDelivery(t, root, kind)
			logPath := pipelineRecoveryTmux(t, root, "2718", prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-ANSWER\n")
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			cmd := newPipelineResumeCmd()
			cmd.SetArgs([]string{prior.RunID})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			raw, err := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			if err != nil {
				t.Fatalf("public resume failed: %v %s", err, raw)
			}
			var response struct {
				Success bool   `json:"success"`
				Status  string `json:"status"`
			}
			if err := json.Unmarshal([]byte(raw), &response); err != nil || !response.Success || response.Status != "completed" {
				t.Fatalf("public resume result = %s, %v", raw, err)
			}
			final, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || final.Status != pipeline.StatusCompleted {
				t.Fatalf("resume did not persist completion: %+v %v", final, err)
			}
			for _, stepID := range []string{"agent", "consume"} {
				if got := strings.TrimSpace(final.Steps[stepID].Output); got != "FRESH-ANSWER" {
					t.Fatalf("%s used stale or missing response: %q", stepID, got)
				}
			}
			if receipt := final.AgentDeliveries["agent"]; receipt.Status != "completed" || receipt.DeliveredAt != prior.AgentDeliveries["agent"].DeliveredAt {
				t.Fatalf("resume did not complete the original delivery: %+v", receipt)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil || !strings.Contains(string(calls), "capture-pane") {
				t.Fatalf("public resume did not observe original pane: %s %v", calls, err)
			}
			for _, transport := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(calls), transport) {
					t.Fatalf("public resume repeated transport %s: %s", transport, calls)
				}
			}
		})
	}
}

func TestPipelineResumeRejectsReplacedAgentProcessWithoutResending(t *testing.T) {
	root := inPipelineControlProject(t)
	prior := interruptedPipelineAgentDelivery(t, root, "prompt")
	receipt := prior.AgentDeliveries["agent"]
	logPath := pipelineRecoveryTmux(t, root, "2719", receipt.BeforeOutput+"\nFRESH-ANSWER\n")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := newPipelineResumeCmd()
	cmd.SetArgs([]string{prior.RunID})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
	if err == nil || !strings.Contains(raw+err.Error(), "changed process lifetime") {
		t.Fatalf("replaced process was not rejected: %v %s", err, raw)
	}
	final, err := pipeline.LoadState(root, prior.RunID)
	if err != nil || final.AgentDeliveries["agent"] != receipt || final.Steps["consume"].Status == pipeline.StatusCompleted {
		t.Fatalf("rejected recovery lost evidence or ran downstream work: %+v %v", final, err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"load-buffer", "paste-buffer", "send-keys"} {
		if strings.Contains(string(calls), transport) {
			t.Fatalf("replaced process received %s: %s", transport, calls)
		}
	}
}
