package pipeline

// Regression tests for GitHub issue #320.
//
// Pipeline dispatch pasted the prompt with a trailing Enter and went straight
// to the completion wait. When an agent TUI consumed that Enter as part of the
// bracketed paste, the instruction stayed in the composer and the pane read
// IDLE — so waitForIdle's consecutive-idle streak completed immediately and
// the step reported success with nothing submitted. Unattended runs advanced
// past a worker that was silently waiting for an operator.
//
// The contract pinned here: every dispatch path establishes submission before
// any completion wait; an unconfirmed submission fails the step explicitly and
// is never reported as completed; the prompt is never resent; and cancellation
// during verification is reported as cancelled rather than failed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// alwaysIdleDetector reports a pane that is sitting at its prompt. That is
// exactly what an agent looks like when its composer is holding an
// unsubmitted payload — the observation the old completion predicate accepted
// as proof of a finished turn.
type alwaysIdleDetector struct{}

func (alwaysIdleDetector) Detect(paneID string) (status.AgentStatus, error) {
	return status.AgentStatus{PaneID: paneID, State: status.StateIdle}, nil
}

func (alwaysIdleDetector) DetectAll(string) ([]status.AgentStatus, error) {
	return nil, nil
}

// errComposerStranded is what the shared verifier reports when the payload is
// still visibly in the composer after its bounded rescue Enter.
var errComposerStranded = errors.New("codex submission unconfirmed: prompt still in composer after rescue (pane %1); submit manually or retry")

// Keep the executor's deterministic pane transport, but run the production
// preflight against real captured-screen fixtures through the shared tmux gate.
type sharedReadinessTmuxClient struct {
	*MockTmuxClient
	preparations  int
	beforePrepare func(int) error
	endpoint      string
}

func (c *sharedReadinessTmuxClient) Endpoint() string {
	if c.endpoint == "" {
		return "local"
	}
	return c.endpoint
}

func (c *sharedReadinessTmuxClient) PrepareDelivery(ctx context.Context, pane tmux.Pane, agentType string) error {
	c.preparations++
	if c.beforePrepare != nil {
		if err := c.beforePrepare(c.preparations); err != nil {
			return dispatch.GuaranteeNoDeliveryActuation(err)
		}
	}
	return (realTmuxClient{}).PrepareDelivery(ctx, pane, agentType)
}

const readyPipelineComposer = "› \n  100% context left"
const browsingPipelineComposer = "› previous request\n\nBrowsing transcript · ↑↓/jk scroll · ↵ rewind · esc back"

func sharedReadinessExecutor(t *testing.T, command, capture string) (*Executor, *sharedReadinessTmuxClient, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("captured-screen tmux fixture requires a POSIX shell")
	}
	root := t.TempDir()
	screen, unexpected := filepath.Join(root, "screen"), filepath.Join(root, "unexpected-keys")
	if err := os.WriteFile(screen, []byte(capture), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "tmux")
	if err := os.WriteFile(binary, []byte(`#!/bin/sh
case " $* " in
  " display-message -p -t %1 [#{@ntm_rotation_token}] ") printf '[]\n' ;;
  *" capture-pane "*) cat "$NTM_PIPELINE_READY_SCREEN" ;;
  *) printf '%s\n' "$*" >> "$NTM_PIPELINE_READY_UNEXPECTED"; exit 64 ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binary)
	t.Setenv("NTM_PIPELINE_READY_SCREEN", screen)
	t.Setenv("NTM_PIPELINE_READY_UNEXPECTED", unexpected)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	cfg := DefaultExecutorConfig("preflight-pipeline")
	cfg.ProjectDir, cfg.RunID = root, "run-preflight"
	client := &sharedReadinessTmuxClient{MockTmuxClient: NewMockTmuxClient(tmux.Pane{
		ID: "%1", Index: 1, PID: 3131, Type: tmux.AgentCodex, Width: 100,
		Title: "preflight-pipeline__cod_1", Command: command,
	})}
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(client)
	return executor, client, screen, unexpected
}

func TestAgentDeliveryPreflightRefusalCanResume(t *testing.T) {
	for _, tc := range []struct {
		name, command, capture, want string
		late                         bool
	}{
		{"agent exited to shell", "zsh", readyPipelineComposer, "PANE_AGENT_DEAD", false},
		{"transcript browsing", "codex", browsingPipelineComposer, "browsing transcript", false},
		{"initializing", "codex", "Loading agent configuration...", "composer not visible", false},
		{"workspace trust", "codex", "Do you trust this folder?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm", "PANE_INTERACTIVE_GATE", false},
		{"modal after intent checkpoint", "codex", browsingPipelineComposer, "browsing transcript", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, template := range []bool{false, true} {
				t.Run(map[bool]string{false: "prompt", true: "template"}[template], func(t *testing.T) {
					initial := tc.capture
					if tc.late {
						initial = readyPipelineComposer
					}
					executor, client, screen, unexpected := sharedReadinessExecutor(t, tc.command, initial)
					client.beforePrepare = func(attempt int) error {
						if tc.late && attempt == 2 {
							return os.WriteFile(screen, []byte(tc.capture), 0600)
						}
						return nil
					}
					step := Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "perform the review", Wait: WaitNone}
					if template {
						step.Template = filepath.Join(executor.config.ProjectDir, "instructions.md")
						if err := os.WriteFile(step.Template, []byte(step.Prompt), 0600); err != nil {
							t.Fatal(err)
						}
						step.Prompt = ""
					}
					workflow := singleStepWorkflow("preflight-recovery", step)
					state, err := executor.Run(context.Background(), workflow, nil, nil)
					if err == nil || state == nil || state.Status != StatusFailed {
						t.Fatalf("blocked input was accepted: state=%+v error=%v", state, err)
					}
					result := state.Steps["work"]
					if result.Error == nil || result.Error.Type != "not_dispatched" || !strings.Contains(result.Error.Message, tc.want) {
						t.Fatalf("lost pre-actuation diagnosis: %+v", result)
					}
					history, historyErr := client.PasteHistory("%1")
					if historyErr != nil || len(history) != 0 || len(client.VerificationHistory()) != 0 {
						t.Fatalf("blocked pane received prompt bytes: pastes=%v error=%v", history, historyErr)
					}
					if data, err := os.ReadFile(unexpected); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("preflight actuated tmux keys: %q, %v", data, err)
					}
					prior, err := LoadState(executor.config.ProjectDir, state.RunID)
					if err != nil || prior.AgentDeliveries["work"].Status != agentDeliveryNotSent {
						t.Fatalf("non-delivery evidence was not durable: state=%+v error=%v", prior, err)
					}
					if err := os.WriteFile(screen, []byte(readyPipelineComposer), 0600); err != nil {
						t.Fatal(err)
					}
					client.AddPane(executor.config.Session, tmux.Pane{
						ID: "%1", Index: 1, PID: 3131, Type: tmux.AgentCodex, Width: 100,
						Title: "preflight-pipeline__cod_1", Command: "codex",
					})
					resumed := NewExecutor(executor.config)
					resumed.SetTmuxClient(client)
					state, err = resumed.Resume(context.Background(), workflow, prior, nil)
					history, historyErr = client.PasteHistory("%1")
					if err != nil || state == nil || state.Status != StatusCompleted || historyErr != nil || len(history) != 1 || history[0].Content != "perform the review" || !history[0].Enter {
						t.Fatalf("ordinary resume did not deliver once after recovery: state=%+v error=%v pastes=%v/%v", state, err, history, historyErr)
					}
					if len(client.VerificationHistory()) != 1 || state.AgentDeliveries["work"].Status != agentDeliveryCompleted {
						t.Fatalf("recovered delivery lost submission verification or completion: %+v", state.AgentDeliveries)
					}
				})
			}
		})
	}
}

func TestAgentDeliveryPreflightPreservesFinalTargetAndCancellationFence(t *testing.T) {
	for _, tc := range []struct {
		name, command, endpoint, phase string
		pid                            int
		cancel                         bool
	}{
		{"process replaced", "codex", "", agentDeliverySending, 4141, false},
		{"endpoint switched", "codex", "ssh:replacement", agentDeliverySending, 3131, false},
		{"CLI exited during capture", "zsh", "", agentDeliveryNotSent, 3131, false},
		{"cancelled during capture", "codex", "", agentDeliveryNotSent, 3131, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, client, _, _ := sharedReadinessExecutor(t, "codex", readyPipelineComposer)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client.beforePrepare = func(attempt int) error {
				if attempt == 2 {
					client.AddPane(executor.config.Session, tmux.Pane{
						ID: "%1", Index: 1, PID: tc.pid, Type: tmux.AgentCodex, Width: 100,
						Title: "preflight-pipeline__cod_1", Command: tc.command,
					})
					client.endpoint = tc.endpoint
					if tc.cancel {
						cancel()
					}
				}
				return nil
			}
			workflow := singleStepWorkflow("final-delivery-fence", Step{
				ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "must retain the original target", Wait: WaitNone,
			})
			state, err := executor.Run(ctx, workflow, nil, nil)
			if err == nil || state == nil || state.AgentDeliveries["work"].Status != tc.phase {
				t.Fatalf("final boundary lost its delivery evidence: state=%+v error=%v", state, err)
			}
			if tc.cancel && (!errors.Is(err, context.Canceled) || state.Status != StatusCancelled) {
				t.Fatalf("preflight cancellation lost its cancellation outcome: state=%+v error=%v", state, err)
			}
			history, historyErr := client.PasteHistory("%1")
			if historyErr != nil || len(history) != 0 || len(client.VerificationHistory()) != 0 {
				t.Fatalf("final readiness change reached transport: pastes=%v/%v", history, historyErr)
			}
			prior, err := LoadState(executor.config.ProjectDir, state.RunID)
			if err != nil || prior.AgentDeliveries["work"].Status != tc.phase {
				t.Fatalf("final boundary phase was not durable: state=%+v error=%v", prior, err)
			}
			if tc.phase == agentDeliverySending {
				resumed := NewExecutor(executor.config)
				resumed.SetTmuxClient(client)
				state, err = resumed.Resume(context.Background(), workflow, prior, nil)
				history, historyErr = client.PasteHistory("%1")
				if err == nil || state == nil || state.AgentDeliveries["work"].Status != agentDeliverySending || client.preparations != 2 || historyErr != nil || len(history) != 0 {
					t.Fatalf("ordinary resume redirected an uncertain delivery: state=%+v error=%v pastes=%v/%v preflights=%d", state, err, history, historyErr, client.preparations)
				}
			}
		})
	}
}

func TestAgentDeliveryPreflightRetriesOnlyNonDelivery(t *testing.T) {
	executor, client, screen, _ := sharedReadinessExecutor(t, "codex", browsingPipelineComposer)
	client.beforePrepare = func(attempt int) error {
		if attempt == 2 {
			return os.WriteFile(screen, []byte(readyPipelineComposer), 0600)
		}
		return nil
	}
	workflow := singleStepWorkflow("retry-unsent-prompt", Step{
		ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "perform one mutation", Wait: WaitNone,
		OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond},
	})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Status != StatusCompleted || state.Steps["work"].Attempts != 2 || client.preparations != 3 {
		t.Fatalf("proved-unsent attempt did not retry after readiness recovered: state=%+v error=%v preflights=%d", state, err, client.preparations)
	}
	assertNotResent(t, client.MockTmuxClient)
	if len(client.VerificationHistory()) != 1 {
		t.Fatalf("safe retry skipped or repeated submission verification: %+v", client.VerificationHistory())
	}
}

func TestAgentDeliveryReadyPreflightKeepsAmbiguousOutcome(t *testing.T) {
	executor, client, _, _ := sharedReadinessExecutor(t, "codex", readyPipelineComposer)
	client.SetSubmissionVerifier(func(string, string, string) error { return errComposerStranded })
	workflow := singleStepWorkflow("ready-but-uncertain", Step{
		ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "perform one mutation", Wait: WaitNone,
		OnError: ErrorActionRetry, RetryCount: 3, RetryDelay: Duration{Duration: time.Millisecond},
	})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil || state == nil || state.AgentDeliveries["work"].Status != agentDeliverySending || state.Steps["work"].Attempts != 1 || client.preparations != 2 {
		t.Fatalf("post-send uncertainty was treated as safe retry: state=%+v error=%v preflights=%d", state, err, client.preparations)
	}
	assertNotResent(t, client.MockTmuxClient)
	prior, err := LoadState(executor.config.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewExecutor(executor.config)
	resumed.SetTmuxClient(client)
	state, err = resumed.Resume(context.Background(), workflow, prior, nil)
	if err == nil || !strings.Contains(err.Error(), "delivery outcome is unknown") || client.preparations != 2 || state.AgentDeliveries["work"].Status != agentDeliverySending {
		t.Fatalf("ordinary resume re-entered dispatch for an uncertain prompt: state=%+v error=%v preflights=%d", state, err, client.preparations)
	}
	assertNotResent(t, client.MockTmuxClient)
}

func TestAgentDeliveryPreflightCheckpointFailurePreservesUncertainty(t *testing.T) {
	executor, client, screen, _ := sharedReadinessExecutor(t, "codex", readyPipelineComposer)
	var blocked string
	client.beforePrepare = func(attempt int) error {
		if attempt == 2 {
			blocked = blockCheckpointPath(t, executor)
			return os.WriteFile(screen, []byte(browsingPipelineComposer), 0600)
		}
		return nil
	}
	workflow := singleStepWorkflow("blocked-preflight-checkpoint", Step{
		ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "perform one mutation", Wait: WaitNone,
		OnError: ErrorActionRetry, RetryCount: 3, RetryDelay: Duration{Duration: time.Millisecond},
	})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	assertCheckpointFailure(t, state, err)
	history, historyErr := client.PasteHistory("%1")
	if historyErr != nil || len(history) != 0 || client.preparations != 2 {
		t.Fatalf("failed refusal checkpoint allowed another dispatch: pastes=%v/%v preflights=%d", history, historyErr, client.preparations)
	}
	data, err := os.ReadFile(blocked + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	var durable ExecutionState
	if err := json.Unmarshal(data, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.AgentDeliveries["work"].Status != agentDeliverySending {
		t.Fatalf("failed checkpoint replaced the last durable uncertainty barrier: %+v", durable.AgentDeliveries)
	}
}

func TestAgentDeliveryInitialReceiptFailurePreventsPreflight(t *testing.T) {
	executor, ctx := checkpointTestExecutor(t)
	if err := executor.persistState(); err != nil {
		t.Fatal(err)
	}
	blockCheckpointPath(t, executor)
	client := &sharedReadinessTmuxClient{MockTmuxClient: NewMockTmuxClient(tmux.Pane{
		ID: "%1", Index: 1, PID: 3131, Type: tmux.AgentCodex, Width: 100, Command: "codex",
	})}
	executor.SetTmuxClient(client)
	step := Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "must not reach the pane", Wait: WaitNone}
	hash, err := agentStepHash(&step)
	if err != nil {
		t.Fatal(err)
	}
	result := executor.executeAgentDelivery(ctx, &step, singleStepWorkflow("blocked-initial-receipt", step), StepKindPrompt, hash, step.Prompt, "%1", string(tmux.AgentCodex))
	history, historyErr := client.PasteHistory("%1")
	if result.Error == nil || result.Error.Type != "checkpoint" || !errors.Is(executor.checkpointFailure(), ErrCheckpointFailed) || client.preparations != 0 || historyErr != nil || len(history) != 0 {
		t.Fatalf("failed initial receipt allowed preflight or delivery: result=%+v preflights=%d pastes=%v/%v", result, client.preparations, history, historyErr)
	}
}

// strandedExecutor builds an executor whose panes are codex, whose detector
// always reads idle, and whose submission verification reports the composer
// still holding the payload.
func strandedExecutor(t *testing.T, session string) (*Executor, *MockTmuxClient) {
	t.Helper()
	cfg := DefaultExecutorConfig(session)
	cfg.ProgressInterval = 100 * time.Millisecond
	cfg.DefaultTimeout = 5 * time.Second
	executor := NewExecutor(cfg)

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex, Width: 120})
	mock.SetSubmissionVerifier(func(string, string, string) error { return errComposerStranded })
	executor.SetTmuxClient(mock)
	executor.SetDetector(alwaysIdleDetector{})
	return executor, mock
}

// submittingExecutor is the same fixture with a composer that submits
// normally, i.e. the verification confirms on its first look.
func submittingExecutor(t *testing.T, session string) (*Executor, *MockTmuxClient) {
	t.Helper()
	cfg := DefaultExecutorConfig(session)
	cfg.ProgressInterval = 100 * time.Millisecond
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex, Width: 120})
	executor.SetTmuxClient(mock)
	executor.SetDetector(alwaysIdleDetector{})
	return executor, mock
}

func singleStepWorkflow(name string, step Step) *Workflow {
	return &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          name,
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{step},
	}
}

// assertNotResent is the "never resend the whole prompt" half of the contract:
// bounded recovery is one bare Enter inside the verifier, never another paste.
func assertNotResent(t *testing.T, mock *MockTmuxClient) {
	t.Helper()
	history, err := mock.PasteHistory("%1")
	if err != nil {
		t.Fatalf("PasteHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("pane received %d pastes, want exactly 1 — recovery must not resend the prompt", len(history))
	}
}

// TestAgentStepFailsWhenPromptStaysInComposer is the core #320 property for
// the prompt dispatch path: with wait: completion and a pane that reads idle,
// an unsubmitted composer must fail the step instead of completing it.
func TestAgentStepFailsWhenPromptStaysInComposer(t *testing.T) {
	executor, mock := strandedExecutor(t, "stranded-prompt")

	workflow := singleStepWorkflow("stranded-prompt-workflow", Step{
		ID:     "repair",
		Pane:   PaneSpec{Index: 1},
		Prompt: "apply the repair instructions",
		Wait:   WaitCompletion,
	})

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	got := state.Steps["repair"]
	if got.Status == StatusCompleted {
		t.Fatal("step reported completed with the prompt still in the composer — wait: completion must not accept an idle composer as completion (#320)")
	}
	if got.Status != StatusFailed {
		t.Fatalf("step status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error == nil || got.Error.Type != "send" {
		t.Fatalf("step error = %+v, want a send-classified delivery failure", got.Error)
	}
	if !strings.Contains(got.Error.Message, "not submitted") {
		t.Errorf("step error message = %q, want it to say the prompt was not submitted", got.Error.Message)
	}
	assertNotResent(t, mock)
}

// alwaysWorkingDetector reports a pane that never finishes its turn.
type alwaysWorkingDetector struct{}

func (alwaysWorkingDetector) Detect(paneID string) (status.AgentStatus, error) {
	return status.AgentStatus{PaneID: paneID, State: status.StateWorking}, nil
}

func (alwaysWorkingDetector) DetectAll(string) ([]status.AgentStatus, error) {
	return nil, nil
}

// A failed send and an expired wait must keep the pane's evidence with the
// step error: the last thing the agent printed and whether it read idle or
// busy. Both were dropped when delivery moved to executeAgentDelivery, so
// `ntm pipeline status` and the pipelines API showed the failure without why.
func TestFailedAgentDeliveryRecordsPaneEvidence(t *testing.T) {
	const evidence = "provider error: 529 overloaded"
	tests := []struct {
		name      string
		executor  func(t *testing.T) (*Executor, *MockTmuxClient)
		wantType  string
		wantState string
	}{
		{
			name:      "send",
			executor:  func(t *testing.T) (*Executor, *MockTmuxClient) { return strandedExecutor(t, "evidence-send") },
			wantType:  "send",
			wantState: string(status.StateIdle),
		},
		{
			name: "timeout",
			executor: func(t *testing.T) (*Executor, *MockTmuxClient) {
				executor, mock := submittingExecutor(t, "evidence-timeout")
				executor.SetDetector(alwaysWorkingDetector{})
				return executor, mock
			},
			wantType:  "timeout",
			wantState: string(status.StateWorking),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor, mock := tt.executor(t)
			if err := mock.SetPaneOutput("%1", evidence); err != nil {
				t.Fatalf("SetPaneOutput: %v", err)
			}
			workflow := singleStepWorkflow("evidence-"+tt.name, Step{
				ID:      "work",
				Pane:    PaneSpec{Index: 1},
				Prompt:  "run the task",
				Wait:    WaitCompletion,
				Timeout: Duration{Duration: 500 * time.Millisecond},
			})

			state, _ := executor.Run(context.Background(), workflow, nil, nil)
			got := state.Steps["work"]
			if got.Status != StatusFailed || got.Error == nil || got.Error.Type != tt.wantType {
				t.Fatalf("step = status %q error %+v, want a failed %q step", got.Status, got.Error, tt.wantType)
			}
			if !strings.Contains(got.Error.PaneOutput, evidence) {
				t.Errorf("error pane output = %q, want the pane's last output %q", got.Error.PaneOutput, evidence)
			}
			if got.Error.AgentState != tt.wantState {
				t.Errorf("error agent state = %q, want %q", got.Error.AgentState, tt.wantState)
			}
		})
	}
}

// TestAgentStepVerifiesSubmissionBeforeWaiting: verification must happen, be
// handed the exact payload that was pasted and the pane's real width, and run
// once per send.
func TestAgentStepVerifiesSubmissionBeforeWaiting(t *testing.T) {
	executor, mock := submittingExecutor(t, "verify-before-wait")

	const prompt = "investigate the failing check"
	workflow := singleStepWorkflow("verify-before-wait-workflow", Step{
		ID:     "investigate",
		Pane:   PaneSpec{Index: 1},
		Prompt: prompt,
		Wait:   WaitNone,
	})

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["investigate"]; got.Status != StatusCompleted {
		t.Fatalf("step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}

	verifications := mock.VerificationHistory()
	if len(verifications) != 1 {
		t.Fatalf("VerifySubmission called %d time(s), want exactly 1 per dispatch", len(verifications))
	}
	if verifications[0].Target != "%1" || verifications[0].Message != prompt {
		t.Errorf("verification = %+v, want the pasted prompt on pane %%1", verifications[0])
	}
	if verifications[0].AgentType != string(tmux.AgentCodex) {
		t.Errorf("verification agent type = %q, want %q — the agent-appropriate verifier must be selected", verifications[0].AgentType, tmux.AgentCodex)
	}
	if verifications[0].PaneWidth != 120 {
		t.Errorf("verification pane width = %d, want the pane's real width 120", verifications[0].PaneWidth)
	}
	assertNotResent(t, mock)
}

// TestFastCompletingStepStillCompletes: a turn that finishes before
// verification looks at the pane leaves an empty composer, so verification
// confirms and the step completes. The new gate must not turn quick answers
// into false delivery failures.
func TestFastCompletingStepStillCompletes(t *testing.T) {
	executor, mock := submittingExecutor(t, "fast-completion")
	mock.SetSubmissionVerifier(func(target, _, _ string) error {
		return mock.AppendPaneOutput(target, "The answer is ready.\n")
	})

	workflow := singleStepWorkflow("fast-completion-workflow", Step{
		ID:     "quick",
		Pane:   PaneSpec{Index: 1},
		Prompt: "answer briefly",
		Wait:   WaitCompletion,
	})

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["quick"]; got.Status != StatusCompleted {
		t.Fatalf("step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
	assertNotResent(t, mock)
}

// Cancelling from the first post-delivery observation deterministically models
// a stopped runner while the submitted agent task remains alive in tmux.
type cancelAgentObservation struct {
	*MockTmuxClient
	executor *Executor
	cancel   context.CancelFunc
	stepID   string
}

func (c *cancelAgentObservation) CapturePaneOutput(target string, lines int) (string, error) {
	output, err := c.MockTmuxClient.CapturePaneOutput(target, lines)
	stepID := c.stepID
	if stepID == "" {
		stepID = "work"
	}
	if delivery, ok := c.executor.loadAgentDelivery(stepID); ok && delivery.Status == agentDeliveryDelivered && c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return output, err
}

func interruptedAgentDelivery(t *testing.T, template bool, agentTypes ...tmux.AgentType) (ExecutorConfig, *Workflow, *MockTmuxClient, *ExecutionState) {
	t.Helper()
	cfg := DefaultExecutorConfig("recover-agent")
	cfg.ProjectDir = t.TempDir()
	cfg.ProgressInterval = 100 * time.Millisecond
	step := Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "do this task", Wait: WaitTime, Timeout: Duration{Duration: 15 * time.Millisecond}}
	if template {
		path := filepath.Join(cfg.ProjectDir, "task.md")
		if err := os.WriteFile(path, []byte(step.Prompt), 0600); err != nil {
			t.Fatal(err)
		}
		step.Prompt, step.Template = "", path
	}
	workflow := singleStepWorkflow("agent-recovery", step)
	agentType := tmux.AgentCodex
	if len(agentTypes) > 0 {
		agentType = agentTypes[0]
	}
	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, PID: 3131, Type: agentType, Width: 100})
	initialOutput := "OLD-ANSWER\n"
	if agentType == tmux.AgentType("pi") {
		initialOutput = piStagePane(initialOutput, "")
	}
	if err := mock.SetPaneOutput("%1", initialOutput); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor.SetTmuxClient(&cancelAgentObservation{MockTmuxClient: mock, executor: executor, cancel: cancel})
	state, err := executor.Run(ctx, workflow, nil, nil)
	if !errors.Is(err, context.Canceled) || state.Status != StatusCancelled || state.AgentDeliveries["work"].Status != agentDeliveryDelivered {
		t.Fatalf("failed to interrupt a genuinely delivered prompt: state=%+v error=%v", state, err)
	}
	assertNotResent(t, mock)
	prior, err := LoadState(cfg.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, workflow, mock, prior
}

func TestAgentDeliveryResumeContinuesPromptAndTemplateWithoutSending(t *testing.T) {
	for _, template := range []bool{false, true} {
		t.Run(map[bool]string{false: "prompt", true: "template"}[template], func(t *testing.T) {
			cfg, workflow, mock, prior := interruptedAgentDelivery(t, template)
			if err := mock.AppendPaneOutput("%1", "NEW-ANSWER\n"); err != nil {
				t.Fatal(err)
			}
			if template {
				// The confirmed request is authoritative; recovery need not
				// rerender a mutable template to observe its existing result.
				if err := os.WriteFile(workflow.Steps[0].Template, []byte("different request"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			resumed := NewExecutor(cfg)
			resumed.SetTmuxClient(mock)
			state, err := resumed.Resume(context.Background(), workflow, prior, nil)
			if err != nil || state.Status != StatusCompleted || !strings.Contains(state.Steps["work"].Output, "NEW-ANSWER") || strings.Contains(state.Steps["work"].Output, "OLD-ANSWER") {
				t.Fatalf("recovery lost the original response boundary: %+v, %v", state, err)
			}
			assertNotResent(t, mock)
			if len(mock.VerificationHistory()) != 1 || state.AgentDeliveries["work"].Status != agentDeliveryCompleted {
				t.Fatalf("recovery submitted again or lost completion receipt: %+v", state.AgentDeliveries)
			}
		})
	}
}

func TestPiAgentDeliveryResumeUsesSavedPromptIdentity(t *testing.T) {
	for _, template := range []bool{false, true} {
		t.Run(map[bool]string{false: "prompt", true: "template"}[template], func(t *testing.T) {
			cfg, workflow, mock, prior := interruptedAgentDelivery(t, template, tmux.AgentType("pi"))
			if prior.AgentDeliveries["work"].PromptEchoLength != len("do this task") {
				t.Fatalf("receipt did not preserve bounded prompt identity: %+v", prior.AgentDeliveries["work"])
			}
			if err := mock.SetPaneOutput("%1", piStagePane("Pi v1.3.0\n\ndo this task\nNEW-ANSWER after repaint", "unsent draft")); err != nil {
				t.Fatal(err)
			}
			if template {
				// The original request's hash/length is authoritative. Resume
				// must not rerender this changed template to guess its echo.
				if err := os.WriteFile(workflow.Steps[0].Template, []byte("A different request"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			resumed := NewExecutor(cfg)
			resumed.SetTmuxClient(mock)
			state, err := resumed.Resume(context.Background(), workflow, prior, nil)
			if err != nil || state.Status != StatusCompleted {
				t.Fatalf("pi resume lost the delivered turn: state=%+v err=%v", state, err)
			}
			output := state.Steps["work"].Output
			if !strings.Contains(output, "NEW-ANSWER after repaint") || strings.Contains(output, "OLD-ANSWER") || strings.Contains(output, "unsent draft") || strings.Contains(output, "DeepSeek") {
				t.Fatalf("pi resume misattributed transcript/chrome: %q", output)
			}
			assertNotResent(t, mock)
			persisted, err := LoadState(cfg.ProjectDir, state.RunID)
			if err != nil || persisted.AgentDeliveries["work"].Status != agentDeliveryCompleted || persisted.AgentDeliveries["work"].Output != output {
				t.Fatalf("accepted repaint was not durable: state=%+v err=%v", persisted, err)
			}
		})
	}
}

func TestPiRenderedPromptEchoWaitsForActualAnswer(t *testing.T) {
	for _, tc := range []struct{ name, prompt, rendered string }{
		{"formatting", "Review **changes**", "Review changes"},
		{"bare references", "Review https://example.com/issue and contact reviewer@example.com.", "Review https://example.com/issue and contact reviewer@example.com."},
		{"rendered references", "Review <https://example.com/issue> and contact [reviewer@example.com](mailto:reviewer@example.com).", "Review https://example.com/issue and contact reviewer@example.com."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, mock := submittingExecutor(t, "pi-rendered-echo")
			executor.config.ProjectDir = t.TempDir()
			mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, PID: 4141, Type: tmux.AgentType("pi"), Width: 100})
			if err := mock.SetPaneOutput("%1", piStagePane("Previous turn", "")); err != nil {
				t.Fatal(err)
			}
			mock.SetSubmissionVerifier(func(target, _, _ string) error {
				return mock.SetPaneOutput(target, piStagePane(tc.rendered, "A verdict still in the composer"))
			})
			detector := &stageIdleDetector{}
			detector.onIdle = func(call int) {
				if call == idleStablePolls+1 {
					if err := mock.SetPaneOutput("%1", piStagePane(tc.rendered+"\nThe changes are correct.", "A verdict still in the composer")); err != nil {
						t.Fatal(err)
					}
				}
			}
			executor.SetDetector(detector)
			workflow := singleStepWorkflow("pi-rendered-echo", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: tc.prompt, Wait: WaitCompletion})
			state, err := executor.Run(context.Background(), workflow, nil, nil)
			if err != nil || state.Status != StatusCompleted || detector.calls <= idleStablePolls+1 {
				t.Fatalf("rendered prompt echo advanced without a fresh answer: %+v calls=%d err=%v", state, detector.calls, err)
			}
			if output := state.Steps["work"].Output; output != tc.rendered+"\nThe changes are correct." {
				t.Fatalf("rendered prompt response included draft text or lost the answer: %q", output)
			}
			assertNotResent(t, mock)
		})
	}
}

func TestPiRenderedPromptIdentitySurvivesTemplateResume(t *testing.T) {
	executor, mock := submittingExecutor(t, "pi-rendered-resume")
	executor.config.ProjectDir = t.TempDir()
	mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, PID: 4141, Type: tmux.AgentType("pi"), Width: 100})
	if err := mock.SetPaneOutput("%1", piStagePane("Previous turn", "")); err != nil {
		t.Fatal(err)
	}
	const prompt, rendered = "Review **changes**", "Review changes"
	path := filepath.Join(executor.config.ProjectDir, "task.md")
	if err := os.WriteFile(path, []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor.SetTmuxClient(&cancelAgentObservation{MockTmuxClient: mock, executor: executor, cancel: cancel})
	workflow := singleStepWorkflow("pi-rendered-resume", Step{ID: "work", Pane: PaneSpec{Index: 1}, Template: path, Wait: WaitTime, Timeout: Duration{Duration: time.Millisecond}})
	state, err := executor.Run(ctx, workflow, nil, nil)
	if !errors.Is(err, context.Canceled) || state.AgentDeliveries["work"].Status != agentDeliveryDelivered {
		t.Fatalf("fixture did not stop after confirmed delivery: %+v %v", state, err)
	}
	prior, err := LoadState(executor.config.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(rendered))
	receipt := prior.AgentDeliveries["work"]
	if receipt.PiPromptEchoHash != hex.EncodeToString(expected[:]) || receipt.PiPromptEchoLength != len(rendered) || receipt.PiPromptEchoHash == receipt.PromptEchoHash {
		t.Fatalf("rendered identity was not persisted separately from raw prompt: %+v", receipt)
	}
	if err := os.WriteFile(path, []byte("A different **request**"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mock.SetPaneOutput("%1", piStagePane(rendered+"\nThe changes are correct.", "UNSENT-DRAFT")); err != nil {
		t.Fatal(err)
	}
	resumed := NewExecutor(executor.config)
	resumed.SetTmuxClient(mock)
	final, err := resumed.Resume(context.Background(), workflow, prior, nil)
	if err != nil || final.Status != StatusCompleted || final.Steps["work"].Output != rendered+"\nThe changes are correct." {
		t.Fatalf("resume lost the delivered rendered identity: %+v %v", final, err)
	}
	assertNotResent(t, mock)
	if len(mock.VerificationHistory()) != 1 {
		t.Fatalf("resume verified a new submission: %+v", mock.VerificationHistory())
	}
}

func TestPiUnsupportedPromptRefusesBeforeDispatch(t *testing.T) {
	executor, mock := submittingExecutor(t, "pi-unsupported-prompt")
	executor.config.ProjectDir = t.TempDir()
	mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, PID: 4141, Type: tmux.AgentType("pi"), Width: 100})
	if err := mock.SetPaneOutput("%1", piStagePane("Previous turn", "")); err != nil {
		t.Fatal(err)
	}
	workflow := singleStepWorkflow("pi-unsupported-prompt", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "Review [the changes](https://example.com/changes)", Wait: WaitCompletion})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil || state.Status != StatusFailed || state.AgentDeliveries["work"].Status != agentDeliveryNotSent || state.Steps["work"].Error.Type != "not_dispatched" {
		t.Fatalf("unsupported pi echo did not retain proven non-delivery: %+v %v", state, err)
	}
	pastes, err := mock.PasteHistory("%1")
	if err != nil || len(pastes) != 0 || len(mock.VerificationHistory()) != 0 {
		t.Fatalf("unsupported pi echo was sent: %+v %v", pastes, err)
	}
	persisted, err := LoadState(executor.config.ProjectDir, state.RunID)
	if err != nil || persisted.AgentDeliveries["work"].Status != agentDeliveryNotSent {
		t.Fatalf("unsupported prompt lost durable non-delivery evidence: %+v %v", persisted, err)
	}
}

func TestPiAgentDeliveryRechecksFrameAfterFinalIdle(t *testing.T) {
	executor, mock := submittingExecutor(t, "pi-final-idle")
	executor.config.ProjectDir = t.TempDir()
	mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, PID: 4141, Type: tmux.AgentType("pi"), Width: 100})
	if err := mock.SetPaneOutput("%1", piStagePane("Previous turn", "")); err != nil {
		t.Fatal(err)
	}
	const prompt = "Review the inventory."
	complete := piStagePane(prompt+"\nThe inventory review is complete.", "")
	mock.SetSubmissionVerifier(func(target, _, _ string) error {
		return mock.SetPaneOutput(target, complete)
	})
	detector := &stageIdleDetector{}
	detector.onIdle = func(call int) {
		switch call {
		case idleStablePolls:
			// The screenshot after the final idle reading is incomplete.
			// Its draft must not use the previously accumulated answer to
			// publish success without a new trustworthy frame.
			if err := mock.SetPaneOutput("%1", prompt+"\n"+strings.Repeat("─", 48)+"\nA draft verdict"); err != nil {
				t.Fatal(err)
			}
		case idleStablePolls + 1:
			receipt, ok := executor.loadAgentDelivery("work")
			if !ok || receipt.Status != agentDeliveryDelivered {
				t.Fatalf("incomplete final frame published completion: %+v", receipt)
			}
			if err := mock.SetPaneOutput("%1", complete); err != nil {
				t.Fatal(err)
			}
		}
	}
	executor.SetDetector(detector)
	workflow := singleStepWorkflow("pi-final-idle", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: prompt, Wait: WaitCompletion})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil || state.Status != StatusCompleted || detector.calls <= idleStablePolls {
		t.Fatalf("final pi frame did not earn a new idle streak: state=%+v calls=%d err=%v", state, detector.calls, err)
	}
	if output := state.Steps["work"].Output; !strings.Contains(output, "inventory review is complete") || strings.Contains(output, "draft verdict") {
		t.Fatalf("final frame polluted response evidence: %q", output)
	}
	assertNotResent(t, mock)
}

func TestAgentDeliveryUnknownOutcomeRequiresExplicitRestart(t *testing.T) {
	executor, mock := strandedExecutor(t, "unknown-delivery")
	executor.config.ProjectDir = t.TempDir()
	workflow := singleStepWorkflow("unknown-delivery", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "perform one mutation", Wait: WaitNone, OnError: ErrorActionRetry, RetryCount: 3, RetryDelay: Duration{Duration: time.Millisecond}})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil || state.AgentDeliveries["work"].Status != agentDeliverySending || state.Steps["work"].Attempts != 1 {
		t.Fatalf("ambiguous transport automatically retried: %+v, %v", state, err)
	}
	assertNotResent(t, mock)
	prior, err := LoadState(executor.config.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewExecutor(executor.config)
	resumed.SetTmuxClient(mock)
	state, err = resumed.Resume(context.Background(), workflow, prior, nil)
	if err == nil || !strings.Contains(err.Error(), "delivery outcome is unknown") || state.AgentDeliveries["work"].Status != agentDeliverySending {
		t.Fatalf("ordinary resume discarded an ambiguous send: %+v, %v", state, err)
	}
	assertNotResent(t, mock)
	prior, err = LoadState(executor.config.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	mock.SetSubmissionVerifier(nil)
	restarted := NewExecutor(executor.config)
	restarted.SetTmuxClient(mock)
	state, err = restarted.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Mode: ResumeModeRestartFailed}, nil)
	history, historyErr := mock.PasteHistory("%1")
	if err != nil || state.Status != StatusCompleted || historyErr != nil || len(history) != 2 {
		t.Fatalf("explicit restart did not authorize exactly one new delivery: %+v, %v, pastes=%v/%v", state, err, history, historyErr)
	}
}

func TestAgentDeliveryResumeRejectsReplacedPaneOrMissingBoundary(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost boundary", true: "replaced process"}[replaced], func(t *testing.T) {
			cfg, workflow, mock, prior := interruptedAgentDelivery(t, false)
			var client TmuxClient = mock
			if replaced {
				// Preserve the original transport history while changing the
				// observed process occupying the same physical pane.
				client = &replacedAgentPane{MockTmuxClient: mock}
			} else if err := mock.SetPaneOutput("%1", "unrelated replacement screen"); err != nil {
				t.Fatal(err)
			}
			resumed := NewExecutor(cfg)
			resumed.SetTmuxClient(client)
			state, err := resumed.Resume(context.Background(), workflow, prior, nil)
			if err == nil || state.Status == StatusCompleted || state.AgentDeliveries["work"].Status != agentDeliveryDelivered {
				t.Fatalf("unsafe recovered observation accepted: %+v, %v", state, err)
			}
			assertNotResent(t, mock)
		})
	}
}

type replacedAgentPane struct{ *MockTmuxClient }

func (c *replacedAgentPane) GetPanes(session string) ([]tmux.Pane, error) {
	panes, err := c.MockTmuxClient.GetPanes(session)
	for i := range panes {
		panes[i].PID++
	}
	return panes, err
}

func TestAgentDeliveryCompletedOutputSurvivesMissingStepCheckpoint(t *testing.T) {
	cfg, workflow, mock, prior := interruptedAgentDelivery(t, false)
	if err := mock.AppendPaneOutput("%1", "NEW-ANSWER\n"); err != nil {
		t.Fatal(err)
	}
	resumed := NewExecutor(cfg)
	resumed.SetTmuxClient(mock)
	completed, err := resumed.Resume(context.Background(), workflow, prior, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := completed.Steps["work"].Output
	// Simulate the crash window after the durable completion receipt, before
	// the enclosing scheduler publishes the StepResult and its variables.
	completed.Status = StatusRunning
	completed.Steps = map[string]StepResult{}
	completed.Variables = map[string]interface{}{}
	completed.InFlightSteps = map[string]InFlightStepState{"work": {StepID: "work", Kind: StepKindPrompt}}
	if err := SaveState(cfg.ProjectDir, completed); err != nil {
		t.Fatal(err)
	}
	prior, err = LoadState(cfg.ProjectDir, completed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
		restored := NewExecutor(cfg)
		restored.SetTmuxClient(&replacedAgentPane{MockTmuxClient: mock})
		state, err := restored.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Mode: mode}, nil)
		if err != nil || state.Status != StatusCompleted || state.Steps["work"].Output != want || state.Variables["steps.work.output"] != want {
			t.Fatalf("%s lost completed response before enclosing checkpoint: %+v, %v", mode, state, err)
		}
		assertNotResent(t, mock)
	}
}

func TestAgentDeliveryIdlePromptEchoIsNotACompletedResponse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType tmux.AgentType
		echo      string
	}{
		{"plain echo", tmux.AgentCodex, "complete this task"},
		{"Claude composer and status", tmux.AgentClaude, "❯ complete this task\n\n❯ \n? for shortcuts"},
		{"Codex composer and status", tmux.AgentCodex, "› complete this task\n\n› \n100% context left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, mock := submittingExecutor(t, "echo-only")
			executor.config.ProjectDir = t.TempDir()
			executor.config.DefaultTimeout = 2300 * time.Millisecond
			mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, Type: tc.agentType, Width: 100})
			mock.SetSubmissionVerifier(func(target, _, _ string) error {
				return mock.SetPaneOutput(target, tc.echo)
			})
			workflow := singleStepWorkflow("echo-only", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "complete this task", Wait: WaitCompletion})
			state, err := executor.Run(context.Background(), workflow, nil, nil)
			if err == nil || state.Status == StatusCompleted || state.AgentDeliveries["work"].Status != agentDeliveryDelivered {
				t.Fatalf("idle prompt echo fabricated completion: %+v, %v", state, err)
			}
			assertNotResent(t, mock)
		})
	}
}

func TestAgentDeliveryAcceptsTranscriptAcrossComposerRedraw(t *testing.T) {
	executor, mock := submittingExecutor(t, "composer-redraw")
	executor.config.ProjectDir = t.TempDir()
	mock.AddPane("", tmux.Pane{ID: "%1", Index: 1, PID: 4141, Type: tmux.AgentClaude, Width: 100})
	if err := mock.SetPaneOutput("%1", "old conversation\n❯ \n? for shortcuts"); err != nil {
		t.Fatal(err)
	}
	mock.SetSubmissionVerifier(func(target, _, _ string) error {
		return mock.SetPaneOutput(target, "old conversation\n❯ complete this task\nThe answer is ready.\n❯ \n? for shortcuts")
	})
	workflow := singleStepWorkflow("composer-redraw", Step{ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "complete this task", Wait: WaitCompletion})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state.Status != StatusCompleted || !strings.Contains(state.Steps["work"].Output, "The answer is ready.") || strings.Contains(state.Steps["work"].Output, "old conversation") || strings.Contains(state.Steps["work"].Output, "shortcuts") {
		t.Fatalf("normal composer redraw lost the fresh response: %+v, %v", state, err)
	}
	assertNotResent(t, mock)
}

func TestAgentDeliveryWaitNoneCompletesWithoutClaimingResponse(t *testing.T) {
	executor, mock := submittingExecutor(t, "immediate-output")
	executor.config.ProjectDir = t.TempDir()
	mock.SetSubmissionVerifier(func(target, _, _ string) error {
		return mock.SetPaneOutput(target, `{"answer":"ready"}`)
	})
	workflow := singleStepWorkflow("immediate-output", Step{
		ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "answer briefly", Wait: WaitNone,
		OutputVar: "answer", OutputParse: OutputParse{Type: "json"},
	})
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state.Status != StatusCompleted || state.Steps["work"].Output != "" || state.Variables["answer"] != "" || state.Steps["work"].ParsedData != nil || len(state.Errors) != 0 {
		t.Fatalf("fire-and-forget claimed or parsed a response without waiting: %+v, %v", state, err)
	}
	if receipt := state.AgentDeliveries["work"]; receipt.Output != "" || receipt.Status != agentDeliveryCompleted {
		t.Fatalf("fire-and-forget delivery receipt is not complete with empty output: %+v", receipt)
	}
	assertNotResent(t, mock)
}

// TestTemplateStepFailsWhenPromptStaysInComposer covers the template dispatch
// path, which pasted and waited exactly like the prompt path.
func TestTemplateStepFailsWhenPromptStaysInComposer(t *testing.T) {
	dir := t.TempDir()
	templatePath := dir + "/repair.md"
	writeFile(t, templatePath, "render this repair instruction")

	executor, mock := strandedExecutor(t, "stranded-template")
	executor.config.ProjectDir = dir

	workflow := singleStepWorkflow("stranded-template-workflow", Step{
		ID:       "render",
		Pane:     PaneSpec{Index: 1},
		Template: templatePath,
		Wait:     WaitCompletion,
	})

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	got := state.Steps["render"]
	if got.Status == StatusCompleted {
		t.Fatal("template step reported completed with the rendered payload still in the composer (#320)")
	}
	if got.Status != StatusFailed {
		t.Fatalf("template step status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "not submitted") {
		t.Fatalf("template step error = %+v, want a not-submitted delivery failure", got.Error)
	}
	assertNotResent(t, mock)
}

// TestParallelSubStepFailsWhenPromptStaysInComposer covers the third dispatch
// path, the inlined parallel sub-step executor.
func TestParallelSubStepFailsWhenPromptStaysInComposer(t *testing.T) {
	executor, _ := strandedExecutor(t, "stranded-parallel")

	workflow := singleStepWorkflow("stranded-parallel-workflow", Step{
		ID:      "fan",
		OnError: ErrorActionFailFast,
		Parallel: ParallelSpec{
			Steps: []Step{{
				ID:     "leg",
				Pane:   PaneSpec{Index: 1},
				Prompt: "do the parallel leg",
				Wait:   WaitCompletion,
			}},
		},
	})

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	got, ok := state.Steps["fan_leg"]
	if !ok {
		t.Fatalf("parallel sub-step result missing; state=%+v", state.Steps)
	}
	if got.Status == StatusCompleted {
		t.Fatal("parallel sub-step reported completed with the prompt still in the composer (#320)")
	}
	if got.Status != StatusFailed {
		t.Fatalf("parallel sub-step status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "not submitted") {
		t.Fatalf("parallel sub-step error = %+v, want a not-submitted delivery failure", got.Error)
	}
}

// TestSubmissionVerificationCancellationIsCancelled: an operator interrupt
// during verification must be reported as cancelled, not as a delivery
// failure — the prompt's fate is unknown, not known-bad.
func TestSubmissionVerificationCancellationIsCancelled(t *testing.T) {
	cfg := DefaultExecutorConfig("verify-cancel")
	cfg.ProgressInterval = 100 * time.Millisecond
	cfg.DefaultTimeout = 5 * time.Second
	executor := NewExecutor(cfg)

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex, Width: 120})
	executor.SetTmuxClient(mock)
	executor.SetDetector(alwaysIdleDetector{})

	ctx, cancel := context.WithCancel(context.Background())
	mock.SetSubmissionVerifier(func(string, string, string) error {
		cancel()
		return context.Canceled
	})

	workflow := singleStepWorkflow("verify-cancel-workflow", Step{
		ID:     "interrupted",
		Pane:   PaneSpec{Index: 1},
		Prompt: "long running work",
		Wait:   WaitCompletion,
	})

	state, _ := executor.Run(ctx, workflow, nil, nil)
	got := state.Steps["interrupted"]
	if got.Status == StatusCompleted {
		t.Fatal("cancelled verification must never report completion")
	}
	if got.Status != StatusCancelled {
		t.Fatalf("step status = %q, want %q (error=%+v)", got.Status, StatusCancelled, got.Error)
	}
}

type endpointIdentityTmuxClient struct {
	*MockTmuxClient
	endpoint string
	captures int
}

func (m *endpointIdentityTmuxClient) Endpoint() string { return m.endpoint }

func (m *endpointIdentityTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	m.captures++
	return m.MockTmuxClient.CapturePaneOutput(target, lines)
}

func TestAgentDeliveryResumeRejectsDifferentTmuxEndpoint(t *testing.T) {
	cfg := DefaultExecutorConfig("same-session")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), GenerateRunID()
	workflow := singleStepWorkflow("endpoint-identity", Step{
		ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "Work on the original machine",
		Wait: WaitTime, Timeout: Duration{Duration: time.Second},
	})
	mock := NewMockTmuxClient(tmux.Pane{ID: "%17", Index: 1, PID: 2718, Type: tmux.AgentClaude, Width: 120})
	t.Cleanup(mock.Reset)
	if err := mock.SetPaneOutput("%17", "prior output\n"); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(&endpointIdentityTmuxClient{MockTmuxClient: mock, endpoint: "ssh:original-host"})
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
				state, err := LoadState(cfg.ProjectDir, cfg.RunID)
				if err == nil && state.AgentDeliveries["work"].Status == agentDeliveryDelivered {
					delivered = true
					cancel()
					return
				}
			}
		}
	}()
	state, err := executor.Run(ctx, workflow, nil, nil)
	cancel()
	<-watchDone
	if !delivered || err == nil || state == nil || state.Status != StatusCancelled {
		t.Fatalf("initial delivery was not interrupted after confirmation: delivered=%t state=%+v err=%v", delivered, state, err)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := prior.AgentDeliveries["work"]
	if receipt.Endpoint != "ssh:original-host" || receipt.Status != agentDeliveryDelivered {
		t.Fatalf("initial endpoint was not durably bound: %+v", receipt)
	}
	// Physical pane ID and PID collide across machines. Neither is permission
	// to observe a different endpoint or send the old prompt there.
	other := &endpointIdentityTmuxClient{MockTmuxClient: mock, endpoint: "ssh:replacement-host"}
	resumed := NewExecutor(cfg)
	resumed.SetTmuxClient(other)
	resumeCtx, cancelResume := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelResume()
	final, err := resumed.Resume(resumeCtx, workflow, prior, nil)
	if err == nil || final == nil || final.Status != StatusFailed {
		t.Fatalf("different endpoint was accepted: state=%+v err=%v", final, err)
	}
	if result := final.Steps["work"]; result.Error == nil || !strings.Contains(result.Error.Message, "endpoint") {
		t.Fatalf("endpoint rejection has no actionable explanation: %+v", result)
	}
	if other.captures != 0 || final.AgentDeliveries["work"] != receipt {
		t.Fatalf("recovery observed the wrong machine or changed its receipt: captures=%d receipt=%+v", other.captures, final.AgentDeliveries["work"])
	}
	history, err := mock.PasteHistory("%17")
	if err != nil || len(history) != 1 || len(mock.VerificationHistory()) != 1 {
		t.Fatalf("endpoint change repeated the original delivery: pastes=%+v verifications=%+v err=%v", history, mock.VerificationHistory(), err)
	}
}

func TestSuccessHookAgentRecoveryKeepsOriginalPromptDeliveries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native command tail fixture requires a POSIX shell")
	}
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered observation cancelled", true: "unconfirmed submission"}[uncertain], func(t *testing.T) {
			cfg := DefaultExecutorConfig("agent-hook-recovery")
			cfg.ProjectDir, cfg.RunID = t.TempDir(), GenerateRunID()
			cfg.DefaultTimeout, cfg.GlobalTimeout = 5*time.Second, 20*time.Second
			workflow := singleStepWorkflow("agent-hook-recovery", Step{
				ID: "work", Pane: PaneSpec{Index: 1}, Prompt: "parent request", Wait: WaitTime,
				Timeout: Duration{Duration: 15 * time.Millisecond}, OutputVar: "parent_output",
				OnSuccess: []Step{
					{ID: "complete", Pane: PaneSpec{Index: 2}, Prompt: "completed hook request", Wait: WaitTime, Timeout: Duration{Duration: 15 * time.Millisecond}},
					{ID: "pending", Pane: PaneSpec{Index: 3}, Prompt: "pending hook request", Wait: WaitTime, Timeout: Duration{Duration: 15 * time.Millisecond}},
					{ID: "tail", Command: "printf 'tail\\n' >> tail-effects"},
				},
			})
			workflow.Steps = append(workflow.Steps, Step{ID: "dependent", DependsOn: []string{"work"}, Command: "printf 'dependent\\n' >> dependent-effects"})
			workflow, path, err := SnapshotWorkflow(context.Background(), cfg.ProjectDir, workflow)
			if err != nil {
				t.Fatal(err)
			}
			cfg.WorkflowFile = path
			mock := NewMockTmuxClient(
				tmux.Pane{ID: "%1", Index: 1, PID: 3101, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				tmux.Pane{ID: "%2", Index: 2, PID: 3102, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				tmux.Pane{ID: "%3", Index: 3, PID: 3103, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
			)
			t.Cleanup(mock.Reset)
			for _, paneID := range []string{"%1", "%2", "%3"} {
				if err := mock.SetPaneOutput(paneID, "OLD-ANSWER\n"); err != nil {
					t.Fatal(err)
				}
			}
			mock.SetSubmissionVerifier(func(target, message, _ string) error {
				if target == "%3" {
					if uncertain {
						return errComposerStranded
					}
					return nil
				}
				return mock.AppendPaneOutput(target, "ANSWER: "+message+"\n")
			})
			assertOriginalDeliveries := func() {
				t.Helper()
				for paneID, prompt := range map[string]string{"%1": "parent request", "%2": "completed hook request", "%3": "pending hook request"} {
					history, err := mock.PasteHistory(paneID)
					if err != nil || len(history) != 1 || history[0].Content != prompt || !history[0].Enter {
						t.Fatalf("pane %s did not retain one original delivery: pastes=%+v error=%v", paneID, history, err)
					}
				}
				if got := len(mock.VerificationHistory()); got != 3 {
					t.Fatalf("submission verification repeated or skipped: %d, want 3", got)
				}
			}
			assertNoTail := func() {
				t.Helper()
				for _, name := range []string{"tail-effects", "dependent-effects"} {
					if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("unfinished hook allowed %s: data=%q error=%v", name, data, err)
					}
				}
			}
			executor := NewExecutor(cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor.SetTmuxClient(&cancelAgentObservation{MockTmuxClient: mock, executor: executor, cancel: cancel, stepID: "work_on_success_pending"})
			state, runErr := executor.Run(ctx, workflow, nil, nil)
			wantStatus := StatusCancelled
			if uncertain {
				wantStatus = StatusFailed
			}
			if runErr == nil || state == nil || state.Status != wantStatus || state.Steps["work"].Status != wantStatus {
				t.Fatalf("unfinished hook did not stop its parent: state=%+v error=%v", state, runErr)
			}
			if !uncertain && !errors.Is(runErr, context.Canceled) {
				t.Fatalf("observation cancellation lost its original cause: %v", runErr)
			}
			assertOriginalDeliveries()
			assertNoTail()
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			parentReceipt, completedReceipt := prior.AgentDeliveries["work"], prior.AgentDeliveries["work_on_success_complete"]
			if parentReceipt.Status != agentDeliveryCompleted || completedReceipt.Status != agentDeliveryCompleted {
				t.Fatalf("completed prompt receipts were not persisted: %+v", prior.AgentDeliveries)
			}
			if uncertain {
				resumed := NewExecutor(cfg)
				resumed.SetTmuxClient(mock)
				state, err = resumed.Resume(context.Background(), workflow, prior, nil)
				if err == nil || state == nil || state.Status != StatusFailed || state.AgentDeliveries["work_on_success_pending"].Status != agentDeliverySending {
					t.Fatalf("ordinary resume accepted an unconfirmed send: state=%+v error=%v", state, err)
				}
				assertOriginalDeliveries()
				assertNoTail()
			}
			if err := mock.AppendPaneOutput("%3", "RESUMED-ANSWER\n"); err != nil {
				t.Fatal(err)
			}
			if uncertain {
				inspection, err := InspectAgentDeliveryRecovery(context.Background(), cfg.ProjectDir, cfg.RunID)
				if err != nil || len(inspection.Deliveries) != 1 || inspection.Deliveries[0].StepID != "work_on_success_pending" || !inspection.Deliveries[0].CanAccept {
					t.Fatalf("unconfirmed hook cannot be inspected for operator recovery: inspection=%+v error=%v", inspection, err)
				}
				resolver := NewExecutor(cfg)
				resolver.SetTmuxClient(mock)
				accepted, err := resolver.acceptAgentDeliveryContext(context.Background(), "work_on_success_pending", inspection.Deliveries[0].ReceiptToken, "Observed the original hook task and its fresh answer in the original pane")
				if err != nil || accepted == nil || accepted.Replayed || accepted.Delivery.Status != agentDeliveryDelivered {
					t.Fatalf("explicit acceptance did not retain the original delivery: result=%+v error=%v", accepted, err)
				}
				assertOriginalDeliveries()
				assertNoTail()
			}
			for attempt := 0; attempt < 2; attempt++ {
				prior, err = LoadState(cfg.ProjectDir, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				resumed := NewExecutor(cfg)
				resumed.SetTmuxClient(mock)
				state, err = resumed.Resume(owner.Context(), workflow, prior, nil)
				owner.Close()
				if err != nil || state == nil || state.Status != StatusCompleted {
					t.Fatalf("resume %d failed after observing the original delivery: state=%+v error=%v", attempt+1, state, err)
				}
				assertOriginalDeliveries()
				if state.AgentDeliveries["work"] != parentReceipt || state.AgentDeliveries["work_on_success_complete"] != completedReceipt {
					t.Fatalf("resume %d replaced a completed prompt receipt", attempt+1)
				}
				if output := state.Steps["work_on_success_pending"].Output; !strings.Contains(output, "RESUMED-ANSWER") || strings.Contains(output, "OLD-ANSWER") {
					t.Fatalf("resume %d lost the pending response boundary: %q", attempt+1, output)
				}
				for name, want := range map[string]string{"tail-effects": "tail\n", "dependent-effects": "dependent\n"} {
					if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); err != nil || string(data) != want {
						t.Fatalf("resume %d changed %s more than once: data=%q error=%v", attempt+1, name, data, err)
					}
				}
			}
		})
	}
}

// This lifecycle uses actual persisted execution state and the real agent
// delivery executor. Its external effects are five distinct pane submissions,
// so it also runs where native process-group supervision is unavailable.
func TestSuccessHookPromptOnlyRecovery(t *testing.T) {
	for _, shape := range []string{"command equivalent", "branch", "parallel", "foreach", "nested hook"} {
		t.Run(shape, func(t *testing.T) {
			cfg := DefaultExecutorConfig("prompt-hook-recovery")
			cfg.ProjectDir, cfg.RunID = t.TempDir(), GenerateRunID()
			cfg.DefaultTimeout, cfg.GlobalTimeout = time.Second, 10*time.Second
			promptStep := func(id string, pane int, prompt string) Step {
				return Step{ID: id, Pane: PaneSpec{Index: pane}, Prompt: prompt, Wait: WaitTime,
					Timeout: Duration{Duration: time.Millisecond}}
			}
			work := promptStep("work", 1, "parent request")
			work.OutputVar = "body_output"
			complete := promptStep("complete", 2, "completed hook request")
			complete.OutputVar = "hook_output"
			pending := promptStep("pending", 3, "pending hook request")
			tail := promptStep("tail", 4, "tail request")
			work.OnSuccess = []Step{complete, pending, tail}
			root, parentID := work, "work"
			switch shape {
			case "branch":
				root = Step{ID: "route", Branch: "chosen", Branches: map[string]interface{}{"chosen": []Step{work}}}
				parentID = "route_work"
			case "parallel":
				root = Step{ID: "fan", Parallel: ParallelSpec{Steps: []Step{work}}}
				parentID = "fan_work"
			case "foreach":
				root = Step{ID: "each", Foreach: &ForeachConfig{Items: `["item"]`, Steps: []Step{work}}}
				parentID = "each_iter0_work"
			case "nested hook":
				complete.OnSuccess = []Step{pending, tail}
				root.OnSuccess = []Step{complete}
			}
			completeID := parentID + "_on_success_complete"
			pendingID := parentID + "_on_success_pending"
			if shape == "nested hook" {
				pendingID = completeID + "_on_success_pending"
			}
			dependent := promptStep("dependent", 5, "dependent request")
			dependent.DependsOn = []string{root.ID}
			workflow := singleStepWorkflow("prompt-hook-recovery", root)
			workflow.Steps = append(workflow.Steps, dependent)
			workflow, path, err := SnapshotWorkflow(context.Background(), cfg.ProjectDir, workflow)
			if err != nil {
				t.Fatal(err)
			}
			cfg.WorkflowFile = path
			panes := []tmux.Pane{
				{ID: "%1", Index: 1, PID: 4101, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				{ID: "%2", Index: 2, PID: 4102, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				{ID: "%3", Index: 3, PID: 4103, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				{ID: "%4", Index: 4, PID: 4104, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
				{ID: "%5", Index: 5, PID: 4105, Type: tmux.AgentCodex, Width: 100, Command: "codex"},
			}
			mock := NewMockTmuxClient(panes...)
			t.Cleanup(mock.Reset)
			for _, pane := range panes {
				if err := mock.SetPaneOutput(pane.ID, "OLD-ANSWER\n"); err != nil {
					t.Fatal(err)
				}
			}
			mock.SetSubmissionVerifier(func(target, message, _ string) error {
				if target == "%3" {
					return nil
				}
				return mock.AppendPaneOutput(target, "ANSWER: "+message+"\n")
			})
			assertDeliveries := func(t *testing.T, completed bool) {
				t.Helper()
				messages := []string{"parent request", "completed hook request", "pending hook request", "tail request", "dependent request"}
				for i, pane := range panes {
					want := 1
					if !completed && i >= 3 {
						want = 0
					}
					history, err := mock.PasteHistory(pane.ID)
					if err != nil || len(history) != want || (want == 1 && (history[0].Content != messages[i] || !history[0].Enter)) {
						t.Fatalf("pane %s submission history=%+v error=%v, want %d copies of %q", pane.ID, history, err, want, messages[i])
					}
				}
				want := 3
				if completed {
					want = 5
				}
				if got := len(mock.VerificationHistory()); got != want {
					t.Fatalf("submission verifications=%d, want %d", got, want)
				}
			}
			executor := NewExecutor(cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor.SetTmuxClient(&cancelAgentObservation{
				MockTmuxClient: mock, executor: executor, cancel: cancel, stepID: pendingID,
			})
			state, err := executor.Run(ctx, workflow, nil, nil)
			if !errors.Is(err, context.Canceled) || state == nil || state.Status != StatusCancelled {
				t.Fatalf("delivered hook cancellation lost its outcome: state=%+v error=%v", state, err)
			}
			if result := state.Steps[parentID]; result.Status != StatusCancelled || result.Error == nil || result.Error.Type != "cancelled" {
				t.Fatalf("interrupted parent was published as complete or misclassified: %+v", result)
			}
			assertDeliveries(t, false)
			prior, err := LoadState(cfg.ProjectDir, state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			parentReceipt, completeReceipt := prior.AgentDeliveries[parentID], prior.AgentDeliveries[completeID]
			if parentReceipt.Status != agentDeliveryCompleted || completeReceipt.Status != agentDeliveryCompleted || prior.AgentDeliveries[pendingID].Status != agentDeliveryDelivered {
				t.Fatalf("body/completed/pending delivery evidence was not persisted: %+v", prior.AgentDeliveries)
			}
			if err := mock.AppendPaneOutput("%3", "RESUMED-ANSWER\n"); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				prior, err = LoadState(cfg.ProjectDir, state.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 1 {
					// Rebuild the cache from receipts, discarding a parsed alias
					// that does not belong to this string-only completed hook.
					prior.Variables = map[string]interface{}{"hook_output_parsed": "stale"}
				}
				resumed := NewExecutor(cfg)
				resumed.SetTmuxClient(mock)
				state, err = resumed.Resume(context.Background(), workflow, prior, nil)
				if err != nil || state == nil || state.Status != StatusCompleted {
					t.Fatalf("resume %d failed to finish the remaining hook tail: state=%+v error=%v", attempt+1, state, err)
				}
				assertDeliveries(t, true)
				if state.AgentDeliveries[parentID] != parentReceipt || state.AgentDeliveries[completeID] != completeReceipt {
					t.Fatalf("resume %d replaced a completed native delivery receipt", attempt+1)
				}
				if output := state.Steps[pendingID].Output; !strings.Contains(output, "RESUMED-ANSWER") || strings.Contains(output, "OLD-ANSWER") {
					t.Fatalf("resume %d lost the pending prompt's original response boundary: %q", attempt+1, output)
				}
				if shape == "command equivalent" || shape == "nested hook" {
					for key, want := range map[string]string{
						"body_output": parentReceipt.Output, "hook_output": completeReceipt.Output,
						"steps." + parentID + ".output":   parentReceipt.Output,
						"steps." + completeID + ".output": completeReceipt.Output,
					} {
						if state.Variables[key] != want {
							t.Fatalf("resume %d did not rebuild %s: got=%v want=%q", attempt+1, key, state.Variables[key], want)
						}
					}
					if stale, ok := state.Variables["hook_output_parsed"]; ok {
						t.Fatalf("resume %d retained stale parsed output: %v", attempt+1, stale)
					}
				}
			}
			if shape != "command equivalent" {
				return
			}
			before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, state.RunID))
			if err != nil {
				t.Fatal(err)
			}
			for _, fault := range []string{"missing parent journal", "altered consumed result", "consumed prompt still pending", "changed hook definition"} {
				t.Run(fault, func(t *testing.T) {
					var damaged ExecutionState
					if err := json.Unmarshal(before, &damaged); err != nil {
						t.Fatal(err)
					}
					changed := *workflow
					changed.Steps = append([]Step(nil), workflow.Steps...)
					changed.Steps[0] = cloneStep(workflow.Steps[0])
					switch fault {
					case "missing parent journal":
						delete(damaged.SuccessHooks, parentID)
					case "altered consumed result":
						result := damaged.Steps[completeID]
						result.Output = "forged completion"
						damaged.Steps[completeID] = result
					case "consumed prompt still pending":
						receipt := damaged.AgentDeliveries[completeID]
						receipt.Status, receipt.CompletedAt = agentDeliveryDelivered, time.Time{}
						damaged.AgentDeliveries[completeID] = receipt
					case "changed hook definition":
						changed.Steps[0].OnSuccess[0].Prompt = "different instruction"
					}
					resumed := NewExecutor(cfg)
					resumed.SetTmuxClient(mock)
					if _, err := resumed.Resume(context.Background(), &changed, &damaged, nil); !errors.Is(err, errSuccessHookRecovery) {
						t.Fatalf("inconsistent recovery evidence was accepted: %v", err)
					}
					after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, state.RunID))
					if err != nil || string(before) != string(after) {
						t.Fatalf("rejected recovery changed durable evidence: error=%v", err)
					}
					assertDeliveries(t, true)
				})
			}
		})
	}
}
