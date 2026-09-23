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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
}

func (c *cancelAgentObservation) CapturePaneOutput(target string, lines int) (string, error) {
	output, err := c.MockTmuxClient.CapturePaneOutput(target, lines)
	if delivery, ok := c.executor.loadAgentDelivery("work"); ok && delivery.Status == agentDeliveryDelivered && c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return output, err
}

func interruptedAgentDelivery(t *testing.T, template bool) (ExecutorConfig, *Workflow, *MockTmuxClient, *ExecutionState) {
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
	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, PID: 3131, Type: tmux.AgentCodex, Width: 100})
	if err := mock.SetPaneOutput("%1", "OLD-ANSWER\n"); err != nil {
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
