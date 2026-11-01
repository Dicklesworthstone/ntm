package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// Session resolution lists sessions before has-session, so the fixture
	// must report the checkpoint's session in list-sessions' -F format.
	fakeScript := "#!/bin/sh\ncase \"$1\" in\n  -V) echo 'tmux 3.4' ;;\n  list-sessions) echo 'snapshot-session" + tmux.FieldSeparator + "1" + tmux.FieldSeparator + "0" + tmux.FieldSeparator + "now' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(fakeTmux, []byte(fakeScript), 0700); err != nil {
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
	return pipelineAgentDeliveryCheckpoint(t, root, kind, false)
}

// An uncertain checkpoint comes from a real transport followed by a failed
// submission verifier, not a synthetic status edit. Both cases must preserve
// enough evidence to continue without submitting the original work again.
func pipelineAgentDeliveryCheckpoint(t *testing.T, root, kind string, uncertain bool) *pipeline.ExecutionState {
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
	if uncertain {
		mock.SetSubmissionVerifier(func(_, _, _ string) error {
			return errors.New("submission acknowledgment was lost")
		})
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
	wantStatus, wantDelivery := pipeline.StatusCancelled, "delivered"
	if uncertain {
		wantStatus, wantDelivery = pipeline.StatusFailed, "sending"
	}
	if (!uncertain && !delivered) || runErr == nil || state == nil || state.Status != wantStatus {
		t.Fatalf("initial dispatch did not stop after confirmed delivery: delivered=%t state=%+v err=%v", delivered, state, runErr)
	}
	history, err := mock.PasteHistory("%17")
	if err != nil || len(history) != 1 || len(mock.VerificationHistory()) != 1 {
		t.Fatalf("initial dispatch was not sent and verified exactly once: pastes=%+v verifications=%+v err=%v", history, mock.VerificationHistory(), err)
	}
	persisted, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || persisted.AgentDeliveries["agent"].Status != wantDelivery {
		t.Fatalf("initial delivery was not durably resumable: %+v %v", persisted, err)
	}
	return persisted
}

func executePipelineRecoveryCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPipelineCmd()
	// Match the real root command: a JSON failure is the complete response.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"recover"}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return output.String(), err
}

func TestPipelineRecoverAcceptsUncertainDeliveryThenResumesWithoutResending(t *testing.T) {
	for _, kind := range []string{"prompt", "template"} {
		t.Run(kind, func(t *testing.T) {
			root := inPipelineControlProject(t)
			prior := pipelineAgentDeliveryCheckpoint(t, root, kind, true)
			receipt := prior.AgentDeliveries["agent"]
			logPath := pipelineRecoveryTmux(t, root, "2718", receipt.BeforeOutput+"\nFRESH-ANSWER\n")
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			path := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := executePipelineRecoveryCommand(t, prior.RunID)
			if err != nil {
				t.Fatalf("inspect: %v %s", err, raw)
			}
			var inspection struct {
				Success    bool                                  `json:"success"`
				Deliveries []pipeline.AgentDeliveryRecoveryEntry `json:"deliveries"`
			}
			if err := json.Unmarshal([]byte(raw), &inspection); err != nil || !inspection.Success || len(inspection.Deliveries) != 1 {
				t.Fatalf("invalid inspection: %s %v", raw, err)
			}
			entry := inspection.Deliveries[0]
			if entry.StepID != "agent" || entry.Status != "sending" || entry.PaneID != "%17" || entry.PanePID != 2718 || entry.ReceiptToken == "" {
				t.Fatalf("inspection lost exact delivery identity: %+v", entry)
			}
			if strings.Contains(raw, "OLD-ANSWER") || strings.Contains(raw, "FRESH-ANSWER") || strings.Contains(raw, "Work on this") {
				t.Fatalf("inspection exposed prompt/transcript: %s", raw)
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatal("inspection changed execution state")
			}
			args := []string{prior.RunID, "--step-id", "agent", "--receipt", entry.ReceiptToken, "--accept-delivery", "--reason", "Observed the original agent answer the submitted request"}
			raw, err = executePipelineRecoveryCommand(t, args...)
			if err != nil {
				t.Fatalf("accept: %v %s", err, raw)
			}
			var accepted struct {
				Success  bool                                `json:"success"`
				Replayed bool                                `json:"replayed"`
				Delivery pipeline.AgentDeliveryRecoveryEntry `json:"delivery"`
			}
			if err := json.Unmarshal([]byte(raw), &accepted); err != nil || !accepted.Success || accepted.Replayed || accepted.Delivery.Status != "delivered" {
				t.Fatalf("invalid acceptance: %s %v", raw, err)
			}
			if accepted.Delivery.ResolutionReason != args[len(args)-1] || accepted.Delivery.ResolutionAction != "accept" || accepted.Delivery.ResolvedAt.IsZero() {
				t.Fatalf("acceptance lacks operator evidence: %+v", accepted)
			}
			afterAcceptance, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || afterAcceptance.Status != prior.Status || afterAcceptance.Steps["consume"].Status == pipeline.StatusCompleted {
				t.Fatalf("acceptance ran the pipeline: %+v %v", afterAcceptance, err)
			}
			if got := afterAcceptance.AgentDeliveries["agent"]; got.BeforeOutput != receipt.BeforeOutput || got.Output != receipt.Output || got.StartedAt != receipt.StartedAt {
				t.Fatalf("acceptance changed original response evidence: %+v", got)
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			if err != nil || json.Unmarshal([]byte(raw), &accepted) != nil || !accepted.Replayed {
				t.Fatalf("same decision did not replay: %v %s", err, raw)
			}
			cmd := newPipelineResumeCmd()
			cmd.SetArgs([]string{prior.RunID})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			raw, err = captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			if err != nil {
				t.Fatalf("resume accepted delivery: %v %s", err, raw)
			}
			final, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || final.Status != pipeline.StatusCompleted {
				t.Fatalf("resume did not complete: %+v %v", final, err)
			}
			for _, step := range []string{"agent", "consume"} {
				if got := strings.TrimSpace(final.Steps[step].Output); got != "FRESH-ANSWER" {
					t.Fatalf("step %s lost original response or reused stale output: %q", step, got)
				}
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			if err != nil || json.Unmarshal([]byte(raw), &accepted) != nil || !accepted.Replayed || accepted.Delivery.Status != "completed" {
				t.Fatalf("completed decision did not replay: %v %s", err, raw)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil || !strings.Contains(string(calls), "capture-pane") {
				t.Fatalf("original pane was not observed: %s %v", calls, err)
			}
			for _, transport := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(calls), transport) {
					t.Fatalf("recovery repeated %s: %s", transport, calls)
				}
			}
		})
	}
}

func TestPipelineRecoverRefusesIncompleteDecisionFlags(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, flags := range [][]string{
		{"--step-id", "agent"},
		{"--receipt", "stale"},
		{"--reason", "observed"},
		{"--accept-delivery"},
		{"--accept-delivery", "--step-id", "agent", "--receipt", "token", "--reason", "   "},
		{"--accept-delivery=false", "--step-id", "agent", "--receipt", "token", "--reason", "observed"},
	} {
		raw, err := executePipelineRecoveryCommand(t, append([]string{"run-invalid"}, flags...)...)
		var out struct {
			Success bool   `json:"success"`
			Code    string `json:"error_code"`
		}
		if err == nil || !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &out) != nil || out.Success || out.Code != "RECOVERY_REFUSED" {
			t.Fatalf("incomplete decision %v did not fail visibly: %s %v", flags, raw, err)
		}
	}
}

func TestPipelineRecoverRejectsLiveOwnerBeforeReadingState(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-held-recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(root, ".ntm", "pipelines", "run-held-recovery.json")
	const untouched = "invalid checkpoint must not even be read"
	if err := os.WriteFile(path, []byte(untouched), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := executePipelineRecoveryCommand(t, "run-held-recovery", "--accept-delivery", "--step-id", "agent", "--receipt", strings.Repeat("a", 64), "--reason", "Observed original delivery")
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"error_code"`
	}
	if !errors.Is(err, pipeline.ErrRunAlreadyOwned) || json.Unmarshal([]byte(raw), &out) != nil || out.Success || out.Code != "PIPELINE_RUNNING" {
		t.Fatalf("live owner was not refused: %s %v", raw, err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != untouched {
		t.Fatal("refused recovery changed checkpoint")
	}
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

// Capture a genuine running command journal, then prevent its settlement write
// after the command has produced its observable effect. The owned executor must
// finish and report a storage failure before the earlier checkpoint is restored.
// No command hash, PID, status, or outcome is invented by the recovery fixture.
func interruptedPipelineCommandOutcome(t *testing.T, root, output string, exitCode int, parse bool) *pipeline.ExecutionState {
	t.Helper()
	pipelineRecoveryTmux(t, root, "2718", "")
	if err := os.WriteFile(filepath.Join(root, "fixture-command-output"), []byte(output), 0600); err != nil {
		t.Fatal(err)
	}
	step := pipeline.Step{
		ID: "command", OutputVar: "recovered", OnError: pipeline.ErrorActionRetry,
		RetryCount: 3, RetryDelay: pipeline.Duration{Duration: time.Hour},
		Timeout: pipeline.Duration{Duration: 10 * time.Second},
		Command: "printf 'once\\n' >> command-effects\n" +
			"cat fixture-command-output > observed-command-output\n" +
			"cat observed-command-output\n" +
			"printf ready > command-ready\n" +
			"while [ ! -f command-release ]; do sleep 0.01; done\n" + fmt.Sprintf("exit %d", exitCode),
	}
	stdin := "${steps.command.output}"
	if parse {
		step.OutputParse = pipeline.OutputParse{Type: "json"}
		stdin = "${steps.command.data.answer}|${vars.recovered_parsed.answer}"
	}
	workflow := &pipeline.Workflow{
		SchemaVersion: pipeline.SchemaVersion, Name: "command-recovery-cli",
		Settings: pipeline.WorkflowSettings{OnError: pipeline.ErrorActionFail},
		Steps:    []pipeline.Step{step, {ID: "consume", DependsOn: []string{"command"}, Command: "printf ran > command-dependent; cat", Stdin: stdin}},
	}
	cfg := pipeline.DefaultExecutorConfig("delivery-session")
	cfg.ProjectDir, cfg.RunID = root, pipeline.GenerateRunID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	var final *pipeline.ExecutionState
	var runErr error
	go func() {
		defer close(done)
		final, runErr = pipeline.RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("original command owner did not finish cleanup")
		}
	}()
	statePath := filepath.Join(root, ".ntm", "pipelines", cfg.RunID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		prior, err := pipeline.LoadState(root, cfg.RunID)
		_, readyErr := os.Stat(filepath.Join(root, "command-ready"))
		if err == nil && readyErr == nil && prior.CommandExecutions["command"].Status == "running" {
			break
		}
		select {
		case <-done:
			t.Fatalf("original command finished before its running checkpoint: %+v %v", final, runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("original command never persisted its running checkpoint")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	savedPath := statePath + ".before-settlement"
	if err := os.Rename(statePath, savedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "command-release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original command did not finish after releasing its gate")
	}
	if !errors.Is(runErr, pipeline.ErrCheckpointFailed) || final == nil || final.Status != pipeline.StatusFailed {
		t.Fatalf("fixture did not lose settlement at the real storage boundary: %+v %v", final, runErr)
	}
	if _, err := os.Stat(filepath.Join(root, "command-dependent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependent work ran after settlement storage failed: %v", err)
	}
	if err := os.Rename(statePath, statePath+".blocked-directory"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedPath, statePath); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(statePath); err != nil || !bytes.Equal(data, before) {
		t.Fatalf("fixture did not restore its actual pre-settlement checkpoint: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "command-effects")); err != nil || string(data) != "once\n" {
		t.Fatalf("initial command ran more than once: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "observed-command-output")); err != nil || string(data) != output {
		t.Fatalf("original command did not publish the observed output: %q %v", data, err)
	}
	prior, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || prior.CommandExecutions["command"].Status != "running" {
		t.Fatalf("lost settlement is not resumable evidence: %+v %v", prior, err)
	}
	return prior
}

type pipelineCommandRecoveryResponse struct {
	Success   bool                                     `json:"success"`
	ErrorCode string                                   `json:"error_code"`
	Commands  []pipeline.CommandExecutionRecoveryEntry `json:"commands"`
	Command   pipeline.CommandExecutionRecoveryEntry   `json:"command"`
	Replayed  bool                                     `json:"replayed"`
}

func inspectPipelineCommandRecovery(t *testing.T, runID string) pipeline.CommandExecutionRecoveryEntry {
	t.Helper()
	raw, err := executePipelineRecoveryCommand(t, runID)
	var out pipelineCommandRecoveryResponse
	if err != nil || json.Unmarshal([]byte(raw), &out) != nil || !out.Success || len(out.Commands) != 1 {
		t.Fatalf("command recovery inspection failed: %s %v", raw, err)
	}
	entry := out.Commands[0]
	if entry.StepID != "command" || entry.Status != "running" || !entry.CanAccept || entry.ProcessID <= 0 || len(entry.ReceiptToken) != 64 {
		t.Fatalf("inspection lost the command's exact launch identity: %+v", entry)
	}
	for _, private := range []string{"fixture-command-output", "command-effects", "PRIVATE-COMMAND-OUTPUT", "answer"} {
		if strings.Contains(raw, private) {
			t.Fatalf("command inspection exposed execution contents %q: %s", private, raw)
		}
	}
	return entry
}

func TestPipelineRecoverCommandThenResumeWithoutRerun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		code   int
		parse  bool
	}{
		{"parsed output", "{\"answer\":42}\n", 0, true},
		{"explicit empty output", "", 0, false},
		{"accepted exit failure", "PRIVATE-COMMAND-OUTPUT\n", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := inPipelineControlProject(t)
			prior := interruptedPipelineCommandOutcome(t, root, tc.output, tc.code, tc.parse)
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			entry := inspectPipelineCommandRecovery(t, prior.RunID)
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
				t.Fatal("inspection changed the command checkpoint")
			}
			const reason = "Verified the original command and all descendants finished; retained their final output"
			outputFile := filepath.Join(root, "observed-command-output")
			args := []string{prior.RunID, "--accept-command", "--step-id", "command", "--receipt", entry.ReceiptToken,
				"--reason", reason, "--exit-code", fmt.Sprint(tc.code), "--output-file", outputFile}
			raw, err := executePipelineRecoveryCommand(t, args...)
			var accepted pipelineCommandRecoveryResponse
			if err != nil || json.Unmarshal([]byte(raw), &accepted) != nil || !accepted.Success || accepted.Replayed || accepted.Command.ResolutionAction != pipeline.CommandExecutionResolutionAccept || accepted.Command.ResolutionReason != reason || accepted.Command.ResolvedAt.IsZero() || accepted.Command.ExitCode == nil || *accepted.Command.ExitCode != tc.code || accepted.Command.OutputBytes != len(tc.output) {
				t.Fatalf("command acceptance failed: %s %v", raw, err)
			}
			saved, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || saved.Status != prior.Status || saved.Steps["command"].Status == pipeline.StatusCompleted || saved.Steps["consume"].Status == pipeline.StatusCompleted {
				t.Fatalf("acceptance executed or completed pipeline work: %+v %v", saved, err)
			}
			for _, name := range []string{"steps.command.output", "steps.command.data", "recovered", "recovered_parsed"} {
				if _, exists := saved.Variables[name]; exists {
					t.Fatalf("acceptance published dependent output %q before ordinary resume", name)
				}
			}
			if record := saved.CommandExecutions["command"]; record.Result.Output != tc.output || record.Result.ParsedData != nil || record.Result.Error != nil || record.ProcessID != prior.CommandExecutions["command"].ProcessID {
				t.Fatalf("acceptance replaced original identity or prematurely interpreted output: %+v", record)
			}
			acceptedBytes, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			var replay pipelineCommandRecoveryResponse
			if err != nil || json.Unmarshal([]byte(raw), &replay) != nil || !replay.Replayed || replay.Command.ReceiptToken != entry.ReceiptToken || replay.Command.ResolvedAt != accepted.Command.ResolvedAt {
				t.Fatalf("same command decision did not replay: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, acceptedBytes) {
				t.Fatal("command decision replay rewrote the checkpoint")
			}
			changedOutput := filepath.Join(root, "different-command-output")
			if err := os.WriteFile(changedOutput, []byte(tc.output+"different"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, change := range []struct{ flag, value string }{
				{"--receipt", strings.Repeat("0", 64)}, {"--reason", "A different observation"},
				{"--exit-code", fmt.Sprint((tc.code + 1) % 256)}, {"--output-file", changedOutput},
			} {
				conflict := append([]string(nil), args...)
				for i := range conflict {
					if conflict[i] == change.flag {
						conflict[i+1] = change.value
						break
					}
				}
				raw, err := executePipelineRecoveryCommand(t, conflict...)
				var refused pipelineCommandRecoveryResponse
				if !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &refused) != nil || refused.Success || refused.ErrorCode != "RECOVERY_REFUSED" {
					t.Fatalf("conflicting command decision %s was accepted: %s %v", change.flag, raw, err)
				}
				if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, acceptedBytes) {
					t.Fatalf("conflicting %s changed the saved decision", change.flag)
				}
			}
			cmd := newPipelineResumeCmd()
			cmd.SetArgs([]string{prior.RunID})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, resumeErr := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			final, err := pipeline.LoadState(root, prior.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == 0 {
				if resumeErr != nil || final.Status != pipeline.StatusCompleted || final.Steps["consume"].Status != pipeline.StatusCompleted {
					t.Fatalf("accepted command did not continue its workflow: %+v %s %v", final, raw, resumeErr)
				}
				want := ""
				if tc.parse {
					want = "42|42"
				}
				if final.Steps["consume"].Output != want || final.Variables["recovered"] != strings.TrimSpace(tc.output) {
					t.Fatalf("resume lost parsed/named/empty command output: result=%+v variables=%#v", final.Steps["consume"], final.Variables)
				}
			} else {
				result := final.Steps["command"]
				if resumeErr == nil || final.Status != pipeline.StatusFailed || result.Status != pipeline.StatusFailed || result.Attempts != 1 || result.Error == nil || result.Error.Type != "exit" || !strings.Contains(result.Error.Details, "exit_code=7") {
					t.Fatalf("accepted failure was retried, discarded or treated as success: %+v %s %v", final, raw, resumeErr)
				}
				if _, err := os.Stat(filepath.Join(root, "command-dependent")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dependent ran despite accepted command failure: %v", err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "command-effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("ordinary resume repeated original command side effects: %q %v", data, err)
			}
			settledBytes, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			if err != nil || json.Unmarshal([]byte(raw), &replay) != nil || !replay.Replayed || replay.Command.ResolvedAt != accepted.Command.ResolvedAt {
				t.Fatalf("settled command decision did not replay: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, settledBytes) {
				t.Fatal("settled command replay rewrote execution state")
			}
		})
	}
}

func TestPipelineRecoverCommandRejectsInvalidInputs(t *testing.T) {
	root := inPipelineControlProject(t)
	prior := interruptedPipelineCommandOutcome(t, root, "observed", 0, false)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	entry := inspectPipelineCommandRecovery(t, prior.RunID)
	outputFile := filepath.Join(root, "observed-command-output")
	base := []string{prior.RunID, "--accept-command", "--step-id", "command", "--receipt", entry.ReceiptToken, "--reason", "Verified completion"}
	withOutput := func(file string) []string {
		return append(append([]string(nil), base...), "--exit-code", "0", "--output-file", file)
	}
	oversized := filepath.Join(root, "oversized-command-output")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(pipeline.CommandRecoveryMaxOutputBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	invalidUTF8 := filepath.Join(root, "invalid-utf8-command-output")
	if err := os.WriteFile(invalidUTF8, []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"missing explicit exit":            append(append([]string(nil), base...), "--output-file", outputFile),
		"missing output file":              append(append([]string(nil), base...), "--exit-code", "0"),
		"both acceptance modes":            append(withOutput(outputFile), "--accept-delivery"),
		"command flags without acceptance": {prior.RunID, "--exit-code", "0", "--output-file", outputFile},
		"negative exit":                    append(withOutput(outputFile), "--exit-code", "-1"),
		"invalid exit":                     append(withOutput(outputFile), "--exit-code", "256"),
		"blank reason":                     append(withOutput(outputFile), "--reason", "  "),
		"missing file":                     withOutput(filepath.Join(root, "missing-output")),
		"directory":                        withOutput(root),
		"oversized":                        withOutput(oversized),
		"invalid UTF-8":                    withOutput(invalidUTF8),
	}
	statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := executePipelineRecoveryCommand(t, args...)
			var out pipelineCommandRecoveryResponse
			if !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &out) != nil || out.Success || out.ErrorCode != "RECOVERY_REFUSED" {
				t.Fatalf("invalid command decision was accepted: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
				t.Fatal("invalid input changed command launch evidence")
			}
		})
	}
}

func TestPipelineRecoverCommandOwnershipAndCancellation(t *testing.T) {
	root := inPipelineControlProject(t)
	prior := interruptedPipelineCommandOutcome(t, root, "", 0, false)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	entry := inspectPipelineCommandRecovery(t, prior.RunID)
	args := []string{prior.RunID, "--accept-command", "--step-id", "command", "--receipt", entry.ReceiptToken,
		"--reason", "Verified completion", "--exit-code", "0", "--output-file", filepath.Join(root, "observed-command-output")}
	statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	const invalidState = "live owner must be checked before reading this invalid checkpoint"
	if err := os.WriteFile(statePath, []byte(invalidState), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := executePipelineRecoveryCommand(t, args...)
	var out pipelineCommandRecoveryResponse
	if !errors.Is(err, pipeline.ErrRunAlreadyOwned) || json.Unmarshal([]byte(raw), &out) != nil || out.Success || out.ErrorCode != "PIPELINE_RUNNING" {
		t.Fatalf("command acceptance read state before ownership: %s %v", raw, err)
	}
	if after, err := os.ReadFile(statePath); err != nil || string(after) != invalidState {
		t.Fatal("live-owner refusal changed the checkpoint")
	}
	owner.Close()
	if err := os.WriteFile(statePath, before, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"recover"}, args...))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = cmd.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command acceptance did not preserve cancellation: %s %v", output.String(), err)
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
		t.Fatal("canceled acceptance persisted a command decision")
	}
	owner, err = pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatalf("canceled recovery retained run ownership: %v", err)
	}
	owner.Close()
}

// Hold a real selector after it has emitted its output, then obstruct the
// checkpoint replacement before releasing it. Restoring the exact saved launch
// intent models a lost completion publication without inventing invocation
// hashes, process identities, or evidence that the source ever ran.
func interruptedPipelineSelectorOutcome(t *testing.T, root, kind string, output []byte, exitCode int) *pipeline.ExecutionState {
	t.Helper()
	pipelineRecoveryTmux(t, root, "2718", "")
	if err := os.WriteFile(filepath.Join(root, "fixture-selector-output"), output, 0600); err != nil {
		t.Fatal(err)
	}
	script := "printf 'once\\n' >> selector-effects\n" +
		"cat fixture-selector-output > observed-selector-output\n" +
		"cat observed-selector-output\n" +
		"printf ready > selector-ready\n" +
		"while [ ! -f selector-release ]; do sleep 0.01; done\n" + fmt.Sprintf("exit %d", exitCode)
	step := pipeline.Step{
		ID: "select", OnError: pipeline.ErrorActionRetry, RetryCount: 3,
		RetryDelay: pipeline.Duration{Duration: time.Hour},
		Timeout:    pipeline.Duration{Duration: 10 * time.Second},
	}
	if kind == "foreach" {
		step.Foreach = &pipeline.ForeachConfig{
			Models: pipeline.StringOrList{"$(" + script + ")"},
			Steps: []pipeline.Step{{
				ID: "child", Command: "printf '%s\\n' '${item}' >> selector-children",
			}},
		}
	} else {
		step.Branch = "$(" + script + ")"
		step.Branches = map[string]interface{}{
			"PRIVATE-SELECTOR-KEY": map[string]interface{}{"id": "child", "command": "printf 'chosen\\n' >> selector-children"},
			"default":              map[string]interface{}{"id": "fallback", "command": "printf 'fallback\\n' >> selector-children"},
			"x\x00\uFFFD":          map[string]interface{}{"id": "normalized", "command": "printf 'wrong\\n' >> selector-children"},
			"x\x00\uFFFD\uFFFD":    map[string]interface{}{"id": "replaced", "command": "printf 'wrong\\n' >> selector-children"},
		}
	}
	if kind == "failure action" {
		step.OnFailure = pipeline.OnFailureSpec{Action: "fallback_to_ntm_inbox"}
	}
	workflow := &pipeline.Workflow{
		SchemaVersion: pipeline.SchemaVersion, Name: "selector-recovery-cli",
		Settings: pipeline.WorkflowSettings{OnError: pipeline.ErrorActionFail},
		Steps: []pipeline.Step{step, {
			ID: "consume", DependsOn: []string{"select"}, Command: "printf once > selector-dependent",
		}},
	}
	workflow.Settings.Limits.MaxCommandStdoutBytes = 64
	workflow.Settings.Limits.MaxCommandStderrBytes = 64
	cfg := pipeline.DefaultExecutorConfig("delivery-session")
	cfg.ProjectDir, cfg.RunID = root, pipeline.GenerateRunID()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	var final *pipeline.ExecutionState
	var runErr error
	go func() {
		defer close(done)
		final, runErr = pipeline.RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("original selector owner did not finish cleanup")
		}
	}()
	statePath := filepath.Join(root, ".ntm", "pipelines", cfg.RunID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		prior, err := pipeline.LoadState(root, cfg.RunID)
		_, readyErr := os.Stat(filepath.Join(root, "selector-ready"))
		if err == nil && readyErr == nil && prior.CommandEvaluations["select"].Status == "starting" {
			break
		}
		select {
		case <-done:
			t.Fatalf("selector finished before its durable intent: %+v %v", final, runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("selector did not reach its durable intent and output gate")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	savedPath := statePath + ".before-selector-settlement"
	if err := os.Rename(statePath, savedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "selector-release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original selector did not finish after its output gate opened")
	}
	if !errors.Is(runErr, pipeline.ErrCheckpointFailed) || final == nil || final.Status != pipeline.StatusFailed {
		t.Fatalf("fixture did not lose publication at the real checkpoint boundary: %+v %v", final, runErr)
	}
	for _, name := range []string{"selector-children", "selector-dependent"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("work %q ran after selector publication failed: %v", name, err)
		}
	}
	if err := os.Rename(statePath, statePath+".blocked-selector-directory"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedPath, statePath); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("fixture changed the saved selector intent: %v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(root, "selector-effects")); err != nil || string(actual) != "once\n" {
		t.Fatalf("initial selector did not execute exactly once: %q %v", actual, err)
	}
	if actual, err := os.ReadFile(filepath.Join(root, "observed-selector-output")); err != nil || !bytes.Equal(actual, output) {
		t.Fatalf("selector output artifact differs from its actual captured bytes: %q %v", actual, err)
	}
	prior, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || prior.CommandEvaluations["select"].Status != "starting" || prior.CommandEvaluations["select"].ProcessID != 0 {
		t.Fatalf("lost publication lacks the original selector intent: %+v %v", prior, err)
	}
	return prior
}

// Decode the public wire contract independently of the internal receipt type.
// Inspection and acceptance expose decision metadata, never selector stdout.
type pipelineSelectorRecoveryEntry struct {
	StepID           string    `json:"step_id"`
	Status           string    `json:"status"`
	ReceiptToken     string    `json:"receipt_token"`
	ProcessID        int       `json:"process_id"`
	CanAccept        bool      `json:"can_accept"`
	RefusalReason    string    `json:"refusal_reason"`
	ResolutionAction string    `json:"resolution_action"`
	ResolutionReason string    `json:"resolution_reason"`
	ResolvedAt       time.Time `json:"resolved_at"`
	ExitCode         *int      `json:"exit_code"`
	OutputBytes      int       `json:"output_bytes"`
	OutputHash       string    `json:"output_hash"`
}

type pipelineSelectorRecoveryResponse struct {
	Success   bool                            `json:"success"`
	ErrorCode string                          `json:"error_code"`
	Selectors []pipelineSelectorRecoveryEntry `json:"selectors"`
	Selector  pipelineSelectorRecoveryEntry   `json:"selector"`
	Replayed  bool                            `json:"replayed"`
}

func inspectPipelineSelectorRecovery(t *testing.T, runID string, output []byte) pipelineSelectorRecoveryEntry {
	t.Helper()
	raw, err := executePipelineRecoveryCommand(t, runID)
	var out pipelineSelectorRecoveryResponse
	if err != nil || json.Unmarshal([]byte(raw), &out) != nil || !out.Success || len(out.Selectors) != 1 {
		t.Fatalf("selector inspection failed: %s %v", raw, err)
	}
	entry := out.Selectors[0]
	if entry.StepID != "select" || len(entry.ReceiptToken) != 64 {
		t.Fatalf("inspection lost selector launch identity: %+v", entry)
	}
	for _, private := range []string{"fixture-selector-output", "selector-effects", string(output), base64.StdEncoding.EncodeToString(output)} {
		if private != "" && strings.Contains(raw, private) {
			t.Fatalf("selector inspection exposed execution contents %q: %s", private, raw)
		}
	}
	return entry
}

func TestPipelineRecoverSelectorThenResumeWithoutRerun(t *testing.T) {
	for _, tc := range []struct {
		name, kind, children string
		output               []byte
		code                 int
	}{
		{"branch selection", "branch", "chosen\n", []byte("PRIVATE-SELECTOR-KEY\n"), 0},
		{"raw binary output", "branch", "fallback\n", []byte{'x', 0, 0xff, 0xfe, '\n'}, 0},
		{"original foreach items", "foreach", "cc\ncod\n", []byte("cc\ncod\n"), 0},
		{"accepted nonzero", "branch", "", []byte("PRIVATE-SELECTOR-KEY\n"), 7},
		{"normal failure action", "failure action", "", []byte("PRIVATE-SELECTOR-KEY\n"), 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := inPipelineControlProject(t)
			prior := interruptedPipelineSelectorOutcome(t, root, tc.kind, tc.output, tc.code)
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			entry := inspectPipelineSelectorRecovery(t, prior.RunID, tc.output)
			if entry.Status != "starting" || entry.ProcessID != 0 || !entry.CanAccept {
				t.Fatalf("starting intent cannot be reconciled explicitly: %+v", entry)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
				t.Fatal("inspection changed selector execution evidence")
			}
			const reason = "Verified the original selector and every descendant finished; retained complete stdout and final exit status"
			args := []string{prior.RunID, "--accept-selector", "--step-id", "select", "--receipt", entry.ReceiptToken,
				"--reason", reason, "--exit-code", fmt.Sprint(tc.code), "--output-file", filepath.Join(root, "observed-selector-output")}
			raw, err := executePipelineRecoveryCommand(t, args...)
			var accepted pipelineSelectorRecoveryResponse
			if err != nil || json.Unmarshal([]byte(raw), &accepted) != nil || !accepted.Success || accepted.Replayed ||
				accepted.Selector.Status != "accepted" || accepted.Selector.ResolutionAction != "accept" ||
				accepted.Selector.ResolutionReason != reason || accepted.Selector.ResolvedAt.IsZero() ||
				accepted.Selector.ExitCode == nil || *accepted.Selector.ExitCode != tc.code || accepted.Selector.OutputBytes != len(tc.output) {
				t.Fatalf("selector acceptance lost the exact operator decision: %s %v", raw, err)
			}
			saved, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || saved.Status != prior.Status || saved.Steps["select"].Status == pipeline.StatusCompleted || len(saved.ForeachState) != 0 {
				t.Fatalf("acceptance dispatched or completed selector work: %+v %v", saved, err)
			}
			original, record := prior.CommandEvaluations["select"], saved.CommandEvaluations["select"]
			if record.Status != "accepted" || record.ProcessID != original.ProcessID || record.StartedAt != original.StartedAt ||
				record.FinishedAt != original.FinishedAt || record.InvocationHash != original.InvocationHash || !bytes.Equal(record.Output, tc.output) {
				t.Fatalf("operator acceptance fabricated native execution evidence or changed raw output: %+v", record)
			}
			for _, name := range []string{"selector-children", "selector-dependent"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("acceptance executed %q: %v", name, err)
				}
			}
			inspection := inspectPipelineSelectorRecovery(t, prior.RunID, tc.output)
			if inspection.Status != "accepted" || inspection.OutputBytes != len(tc.output) || len(inspection.OutputHash) != 64 {
				t.Fatalf("accepted selector inspection lost bounded output metadata: %+v", inspection)
			}
			acceptedBytes, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			var replay pipelineSelectorRecoveryResponse
			if err != nil || json.Unmarshal([]byte(raw), &replay) != nil || !replay.Replayed || replay.Selector.ResolvedAt != accepted.Selector.ResolvedAt {
				t.Fatalf("identical selector decision did not replay: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, acceptedBytes) {
				t.Fatal("selector decision replay rewrote the checkpoint")
			}
			changedOutput := filepath.Join(root, "different-selector-output")
			if err := os.WriteFile(changedOutput, append(append([]byte(nil), tc.output...), 'x'), 0600); err != nil {
				t.Fatal(err)
			}
			for _, change := range []struct{ flag, value string }{
				{"--receipt", strings.Repeat("0", 64)}, {"--reason", "A different observation"},
				{"--exit-code", fmt.Sprint((tc.code + 1) % 256)}, {"--output-file", changedOutput},
			} {
				conflict := append([]string(nil), args...)
				for i := range conflict {
					if conflict[i] == change.flag {
						conflict[i+1] = change.value
						break
					}
				}
				raw, err := executePipelineRecoveryCommand(t, conflict...)
				var refused pipelineSelectorRecoveryResponse
				if !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &refused) != nil || refused.Success || refused.ErrorCode != "RECOVERY_REFUSED" {
					t.Fatalf("conflicting selector decision %s was accepted: %s %v", change.flag, raw, err)
				}
				if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, acceptedBytes) {
					t.Fatalf("conflicting %s changed the accepted selector decision", change.flag)
				}
			}
			// The source would now select different work if resume executed it.
			// Only the explicitly accepted original bytes may drive its body.
			if err := os.WriteFile(filepath.Join(root, "fixture-selector-output"), []byte("gmi\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newPipelineResumeCmd()
			cmd.SetArgs([]string{prior.RunID})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, resumeErr := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			final, err := pipeline.LoadState(root, prior.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == 0 {
				if resumeErr != nil || final.Status != pipeline.StatusCompleted || final.Steps["consume"].Status != pipeline.StatusCompleted {
					t.Fatalf("accepted selector did not continue normally: %+v %s %v", final, raw, resumeErr)
				}
				if actual, err := os.ReadFile(filepath.Join(root, "selector-children")); err != nil || string(actual) != tc.children {
					t.Fatalf("resume selected changed work or repeated a child: %q want %q: %v", actual, tc.children, err)
				}
			} else if tc.kind == "failure action" {
				if resumeErr != nil || final.Status != pipeline.StatusCompleted || final.Steps["select"].Status != pipeline.StatusSkipped ||
					final.Steps["select"].SkipKind != pipeline.SkipKindOnFailureAction || final.Variables["runtime.select_failure_action"] != "fallback_to_ntm_inbox" ||
					final.Steps["consume"].Status != pipeline.StatusCompleted {
					t.Fatalf("accepted selector exit bypassed normal failure handling: %+v %s %v", final, raw, resumeErr)
				}
			} else {
				result := final.Steps["select"]
				if resumeErr == nil || final.Status != pipeline.StatusFailed || result.Status != pipeline.StatusFailed || result.Attempts != 1 || result.Error == nil {
					t.Fatalf("accepted selector failure was retried or treated as success: %+v %s %v", final, raw, resumeErr)
				}
				if _, err := os.Stat(filepath.Join(root, "selector-dependent")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dependent ran after accepted selector failure: %v", err)
				}
			}
			if tc.code != 0 {
				if _, err := os.Stat(filepath.Join(root, "selector-children")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed selector dispatched its body: %v", err)
				}
			}
			if actual, err := os.ReadFile(filepath.Join(root, "selector-effects")); err != nil || string(actual) != "once\n" {
				t.Fatalf("recovery repeated selector source effects: %q %v", actual, err)
			}
			settledBytes, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = executePipelineRecoveryCommand(t, args...)
			if err != nil || json.Unmarshal([]byte(raw), &replay) != nil || !replay.Replayed || replay.Selector.ResolvedAt != accepted.Selector.ResolvedAt {
				t.Fatalf("consumed selector decision did not replay: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, settledBytes) {
				t.Fatal("replaying a consumed selector decision changed the journal")
			}
		})
	}
}

func TestPipelineRecoverSelectorRejectsInvalidInputs(t *testing.T) {
	root := inPipelineControlProject(t)
	output := []byte("PRIVATE-SELECTOR-KEY\n")
	prior := interruptedPipelineSelectorOutcome(t, root, "branch", output, 0)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	entry := inspectPipelineSelectorRecovery(t, prior.RunID, output)
	base := []string{prior.RunID, "--accept-selector", "--step-id", "select", "--receipt", entry.ReceiptToken, "--reason", "Verified original selector completion"}
	withOutput := func(file string) []string {
		return append(append([]string(nil), base...), "--exit-code", "0", "--output-file", file)
	}
	outputFile := filepath.Join(root, "observed-selector-output")
	overWorkflowLimit := filepath.Join(root, "over-selector-workflow-limit")
	if err := os.WriteFile(overWorkflowLimit, bytes.Repeat([]byte{'x'}, 65), 0600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(root, "oversized-selector-output")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(pipeline.CommandRecoveryMaxOutputBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"missing explicit exit":           append(append([]string(nil), base...), "--output-file", outputFile),
		"missing output file":             append(append([]string(nil), base...), "--exit-code", "0"),
		"delivery and selector":           append(withOutput(outputFile), "--accept-delivery"),
		"command and selector":            append(withOutput(outputFile), "--accept-command"),
		"flags without acceptance":        {prior.RunID, "--exit-code", "0", "--output-file", outputFile},
		"disabled acceptance":             append(withOutput(outputFile), "--accept-selector=false"),
		"negative exit":                   append(withOutput(outputFile), "--exit-code", "-1"),
		"invalid exit":                    append(withOutput(outputFile), "--exit-code", "256"),
		"blank reason":                    append(withOutput(outputFile), "--reason", "  "),
		"missing step":                    append(withOutput(outputFile), "--step-id", ""),
		"missing receipt":                 append(withOutput(outputFile), "--receipt", ""),
		"stale receipt":                   append(withOutput(outputFile), "--receipt", strings.Repeat("0", 64)),
		"missing file":                    withOutput(filepath.Join(root, "missing-selector-output")),
		"directory":                       withOutput(root),
		"oversized global limit":          withOutput(oversized),
		"oversized frozen workflow limit": withOutput(overWorkflowLimit),
	}
	statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := executePipelineRecoveryCommand(t, args...)
			var refused pipelineSelectorRecoveryResponse
			if !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &refused) != nil || refused.Success || refused.ErrorCode != "RECOVERY_REFUSED" {
				t.Fatalf("invalid selector decision was accepted: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
				t.Fatal("invalid selector input changed the original launch evidence")
			}
		})
	}
}

func TestPipelineRecoverSelectorRejectsLegacyAndChangedWorkflow(t *testing.T) {
	for _, kind := range []string{"legacy intent", "changed frozen workflow"} {
		t.Run(kind, func(t *testing.T) {
			root := inPipelineControlProject(t)
			output := []byte("PRIVATE-SELECTOR-KEY\n")
			prior := interruptedPipelineSelectorOutcome(t, root, "branch", output, 0)
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			entry := inspectPipelineSelectorRecovery(t, prior.RunID, output)
			if kind == "legacy intent" {
				record := prior.CommandEvaluations["select"]
				record.Version, record.InvocationHash, record.StdoutLimit, record.StderrLimit = 1, "", 0, 0
				prior.CommandEvaluations["select"] = record
				prior.CommandEvaluationVersion = 1
				if err := pipeline.SaveState(root, prior); err != nil {
					t.Fatal(err)
				}
				entry = inspectPipelineSelectorRecovery(t, prior.RunID, output)
				if entry.CanAccept || entry.RefusalReason == "" {
					t.Fatalf("legacy selector lacks an explicit recovery refusal: %+v", entry)
				}
			} else {
				original, err := os.ReadFile(prior.WorkflowFile)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prior.WorkflowFile, append(original, []byte("\n# changed after launch\n")...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{prior.RunID, "--accept-selector", "--step-id", "select", "--receipt", entry.ReceiptToken,
				"--reason", "Verified original selector completion", "--exit-code", "0", "--output-file", filepath.Join(root, "observed-selector-output")}
			raw, err := executePipelineRecoveryCommand(t, args...)
			var refused pipelineSelectorRecoveryResponse
			if !errors.Is(err, errJSONFailure) || json.Unmarshal([]byte(raw), &refused) != nil || refused.Success || refused.ErrorCode != "RECOVERY_REFUSED" {
				t.Fatalf("ineligible selector recovery was accepted: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
				t.Fatal("ineligible selector recovery changed the checkpoint")
			}
		})
	}
}

func TestPipelineRecoverSelectorOwnershipAndCancellation(t *testing.T) {
	root := inPipelineControlProject(t)
	output := []byte("PRIVATE-SELECTOR-KEY\n")
	prior := interruptedPipelineSelectorOutcome(t, root, "branch", output, 0)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	entry := inspectPipelineSelectorRecovery(t, prior.RunID, output)
	args := []string{prior.RunID, "--accept-selector", "--step-id", "select", "--receipt", entry.ReceiptToken,
		"--reason", "Verified original selector completion", "--exit-code", "0", "--output-file", filepath.Join(root, "observed-selector-output")}
	statePath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	const invalidState = "selector owner must be checked before reading the checkpoint"
	if err := os.WriteFile(statePath, []byte(invalidState), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := executePipelineRecoveryCommand(t, args...)
	var refused pipelineSelectorRecoveryResponse
	if !errors.Is(err, pipeline.ErrRunAlreadyOwned) || json.Unmarshal([]byte(raw), &refused) != nil || refused.Success || refused.ErrorCode != "PIPELINE_RUNNING" {
		t.Fatalf("selector acceptance read state before checking live ownership: %s %v", raw, err)
	}
	if after, err := os.ReadFile(statePath); err != nil || string(after) != invalidState {
		t.Fatal("live-owner refusal changed selector state")
	}
	owner.Close()
	if err := os.WriteFile(statePath, before, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"recover"}, args...))
	var response bytes.Buffer
	cmd.SetOut(&response)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = cmd.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled selector acceptance lost its cancellation cause: %s %v", response.String(), err)
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, before) {
		t.Fatal("cancelled selector acceptance persisted a decision")
	}
	owner, err = pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatalf("cancelled selector acceptance retained ownership: %v", err)
	}
	owner.Close()
}

func pipelineCleanupCheckpoint(t *testing.T, root, runID, kind string) (string, []byte) {
	t.Helper()
	finished := time.Now().UTC().Add(-30 * 24 * time.Hour)
	started := finished.Add(-time.Hour)
	record := pipeline.CommandEvaluationState{
		Version: 2, StepID: "source", Status: "settled", ProcessID: 1,
		StepHash: strings.Repeat("a", 64), InvocationHash: strings.Repeat("b", 64),
		StdoutLimit: 64, StderrLimit: 64, StartedAt: started, FinishedAt: finished,
		// A complete empty stdout still has a positive byte-capture limit and
		// the real SHA-256 digest; cleanup must inspect valid settled evidence.
		OutputHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	step := pipeline.StepResult{
		StepID: "source", Status: pipeline.StatusCompleted, AgentType: "branch",
		StartedAt: started, FinishedAt: finished,
	}
	state := &pipeline.ExecutionState{
		RunID: runID, WorkflowID: "cleanup-fixture", Session: "cleanup-session",
		Status: pipeline.StatusCompleted, StartedAt: started, FinishedAt: finished,
		UpdatedAt: finished, LastCheckpointAt: finished, CommandEvaluationVersion: 2,
		Steps: make(map[string]pipeline.StepResult), Variables: make(map[string]interface{}),
	}
	switch kind {
	case "settled":
	case "running":
		state.Status, state.FinishedAt = pipeline.StatusRunning, time.Time{}
		step.Status, step.FinishedAt = pipeline.StatusRunning, time.Time{}
		record.Status, record.ProcessID, record.FinishedAt, record.OutputHash = "starting", 0, time.Time{}, ""
	case "unresolved":
		state.Status, step.Status = pipeline.StatusFailed, pipeline.StatusFailed
		record.Status, record.OutputHash = "unresolved", ""
		record.Error = "process-group observation remained uncertain after cleanup"
		step.Error = &pipeline.StepError{Type: "command", Message: record.Error, Timestamp: finished}
	default:
		t.Fatalf("unknown cleanup fixture kind %q", kind)
	}
	state.Steps[step.StepID] = step
	state.CommandEvaluations = map[string]pipeline.CommandEvaluationState{record.StepID: record}
	if err := pipeline.SaveState(root, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".ntm", "pipelines", runID+".json")
	if err := os.Chtimes(path, finished, finished); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, before
}

func executePipelineCleanupCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPipelineCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var commandOutput bytes.Buffer
	cmd.SetOut(&commandOutput)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"cleanup"}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stdout, err := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
	return stdout + commandOutput.String(), err
}

// Decode the CLI wire contract independently of the cleanup implementation.
type pipelineCleanupResponse struct {
	Success    bool   `json:"success"`
	DryRun     bool   `json:"dry_run"`
	Deleted    int    `json:"deleted"`
	Eligible   int    `json:"eligible"`
	Retained   int    `json:"retained"`
	OlderThan  string `json:"older_than"`
	Duration   string `json:"duration"`
	ProjectDir string `json:"project_dir"`
	Entries    []struct {
		RunID  string `json:"run_id"`
		Action string `json:"action"`
		Reason string `json:"reason"`
	} `json:"entries"`
}

func TestPipelineCleanupPreviewAndDeletionPreserveRecoveryFences(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	paths, original := make(map[string]string), make(map[string][]byte)
	for kind, runID := range map[string]string{
		"settled": "run-cleanup-settled", "running": "run-cleanup-running", "unresolved": "run-cleanup-unresolved",
	} {
		paths[runID], original[runID] = pipelineCleanupCheckpoint(t, root, runID, kind)
	}
	const corruptID = "run-cleanup-corrupt"
	paths[corruptID] = filepath.Join(root, ".ntm", "pipelines", corruptID+".json")
	original[corruptID] = []byte("{a damaged checkpoint must remain available for recovery")
	if err := os.WriteFile(paths[corruptID], original[corruptID], 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(paths[corruptID], old, old); err != nil {
		t.Fatal(err)
	}

	for _, dryRun := range []bool{true, false} {
		args := []string{"--older", "7d"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		raw, err := executePipelineCleanupCommand(t, args...)
		var response pipelineCleanupResponse
		if err != nil || json.Unmarshal([]byte(raw), &response) != nil || !response.Success {
			t.Fatalf("cleanup dry_run=%t failed: %s %v", dryRun, raw, err)
		}
		wantDeleted, eligibleAction := 1, "deleted"
		if dryRun {
			wantDeleted, eligibleAction = 0, "would_delete"
		}
		if response.DryRun != dryRun || response.Deleted != wantDeleted || response.Eligible != 1 || response.Retained != 3 ||
			response.OlderThan != "7d" || response.Duration != (7*24*time.Hour).String() || len(response.Entries) != 4 {
			t.Fatalf("cleanup report did not expose actual eligibility: %s", raw)
		}
		gotRoot, rootErr := filepath.EvalSymlinks(response.ProjectDir)
		wantRoot, err := filepath.EvalSymlinks(root)
		if rootErr != nil || err != nil || gotRoot != wantRoot {
			t.Fatalf("cleanup resolved a different project: %s %v %v", raw, rootErr, err)
		}
		seen := make(map[string]bool)
		for _, entry := range response.Entries {
			if _, exists := paths[entry.RunID]; !exists || seen[entry.RunID] || strings.TrimSpace(entry.Reason) == "" {
				t.Fatalf("cleanup returned an unknown, duplicate, or unexplained entry: %+v", entry)
			}
			seen[entry.RunID] = true
			wantAction := "retained"
			if entry.RunID == "run-cleanup-settled" {
				wantAction = eligibleAction
			}
			if entry.Action != wantAction {
				t.Fatalf("cleanup action for %s=%q, want %q: %s", entry.RunID, entry.Action, wantAction, raw)
			}
		}
		for runID, path := range paths {
			after, err := os.ReadFile(path)
			if !dryRun && runID == "run-cleanup-settled" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("eligible checkpoint was not removed: %v", err)
				}
			} else if err != nil || !bytes.Equal(after, original[runID]) {
				t.Fatalf("cleanup changed protected checkpoint %s (dry_run=%t): %v", runID, dryRun, err)
			}
		}
	}
	// Retrying cleanup cannot erase the recovery fences left by the first run.
	raw, err := executePipelineCleanupCommand(t, "--older", "7d")
	var repeated pipelineCleanupResponse
	if err != nil || json.Unmarshal([]byte(raw), &repeated) != nil || !repeated.Success ||
		repeated.Deleted != 0 || repeated.Eligible != 0 || repeated.Retained != 3 || len(repeated.Entries) != 3 {
		t.Fatalf("repeated cleanup lost retained evidence: %s %v", raw, err)
	}
}

func TestPipelineCleanupRetainsLiveOwnerUntilReleased(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	const runID = "run-cleanup-owner"
	path, before := pipelineCleanupCheckpoint(t, root, runID, "settled")
	owner, err := pipeline.AcquireRunControl(context.Background(), root, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, dryRun := range []bool{true, false} {
		args := []string{"--older", "7d"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		raw, err := executePipelineCleanupCommand(t, args...)
		var response pipelineCleanupResponse
		if err != nil || json.Unmarshal([]byte(raw), &response) != nil || !response.Success ||
			response.DryRun != dryRun || response.Deleted != 0 || response.Eligible != 0 || response.Retained != 1 || len(response.Entries) != 1 {
			t.Fatalf("cleanup did not retain the live owner's checkpoint: %s %v", raw, err)
		}
		entry := response.Entries[0]
		if entry.RunID != runID || entry.Action != "retained" || entry.Reason != "owned" {
			t.Fatalf("ownership refusal lost its identity or reason: %+v", entry)
		}
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("cleanup changed a live owner's state: %v", err)
		}
	}
	if other, err := pipeline.AcquireRunControl(context.Background(), root, runID); !errors.Is(err, pipeline.ErrRunAlreadyOwned) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("cleanup displaced the live ownership lock: %v", err)
	}
	owner.Close()
	raw, err := executePipelineCleanupCommand(t, "--older", "7d")
	var response pipelineCleanupResponse
	if err != nil || json.Unmarshal([]byte(raw), &response) != nil || !response.Success || response.Deleted != 1 || response.Eligible != 1 || response.Retained != 0 {
		t.Fatalf("settled checkpoint was not eligible after ownership ended: %s %v", raw, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released settled checkpoint remains after cleanup: %v", err)
	}
}

func TestPipelineCleanupHumanPreviewShowsEligibilityAndReasons(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	paths, original := make(map[string]string), make(map[string][]byte)
	for kind, runID := range map[string]string{"settled": "run-cleanup-preview-done", "unresolved": "run-cleanup-preview-uncertain"} {
		paths[runID], original[runID] = pipelineCleanupCheckpoint(t, root, runID, kind)
	}
	raw, err := executePipelineCleanupCommand(t, "--older", "7d", "--dry-run")
	var response pipelineCleanupResponse
	if err != nil || json.Unmarshal([]byte(raw), &response) != nil || !response.Success || response.Eligible != 1 || response.Retained != 1 || len(response.Entries) != 2 {
		t.Fatalf("preview did not inspect both checkpoint identities: %s %v", raw, err)
	}
	jsonOutput = false
	human, err := executePipelineCleanupCommand(t, "--older", "7d", "--dry-run")
	if err != nil || !strings.Contains(human, "1 state file(s) eligible") || !strings.Contains(human, "1 retained") {
		t.Fatalf("human preview omitted real cleanup counts: %s %v", human, err)
	}
	for _, entry := range response.Entries {
		if entry.Reason == "" || !strings.Contains(human, entry.RunID+": "+entry.Action+" ("+entry.Reason+")") {
			t.Fatalf("human preview omitted a run's eligibility reason: %+v\n%s", entry, human)
		}
	}
	for runID, path := range paths {
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, original[runID]) {
			t.Fatalf("human preview changed checkpoint %s: %v", runID, err)
		}
	}
}
