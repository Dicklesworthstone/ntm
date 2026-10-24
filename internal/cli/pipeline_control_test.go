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
