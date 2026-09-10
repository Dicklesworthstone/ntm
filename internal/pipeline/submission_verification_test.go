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
