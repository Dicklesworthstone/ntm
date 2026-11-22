package robot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestResolveInterruptTargetsSelectorsDeduplicateAndFailTyped(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentType("claude")},
		{ID: "%2", Index: 0, WindowIndex: 1, Type: tmux.AgentType("codex")},
		{ID: "%3", Index: 0, WindowIndex: 2, Type: tmux.AgentType("gemini")},
	}
	selected, err := resolveInterruptTargets(panes, []string{"2", "2.0", "%3"}, false, nil)
	if err != nil {
		t.Fatalf("resolveInterruptTargets() error = %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "%3" {
		t.Fatalf("selected = %v, want one physical pane %%3", selected)
	}
	if _, err := resolveInterruptTargets(panes, []string{"9.0"}, false, nil); err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v", err)
	}
	if _, err := resolveInterruptTargets(panes, []string{"1.x"}, false, nil); err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("invalid selector error = %v", err)
	}
}

func TestResolveInterruptTargetsTypeFilterNarrowsDefaultAndSelectedSets(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Type: tmux.AgentType("user")},
		{ID: "%1", Index: 1, Type: tmux.AgentType("claude")},
		{ID: "%2", Index: 2, Type: tmux.AgentType("codex")},
		{ID: "%3", Index: 3, Type: tmux.AgentType("codex")},
	}
	ids := func(selected []tmux.Pane) string {
		out := make([]string, 0, len(selected))
		for _, p := range selected {
			out = append(out, p.ID)
		}
		return strings.Join(out, ",")
	}

	selected, err := resolveInterruptTargets(panes, nil, false, []string{"cod"})
	if err != nil || ids(selected) != "%2,%3" {
		t.Fatalf("--type=cod over the default set = %q (err %v), want %%2,%%3", ids(selected), err)
	}
	selected, err = resolveInterruptTargets(panes, nil, false, []string{" claude ", "cc"})
	if err != nil || ids(selected) != "%1" {
		t.Fatalf("--type=claude,cc = %q (err %v), want %%1", ids(selected), err)
	}
	selected, err = resolveInterruptTargets(panes, []string{"%1", "%2"}, false, []string{"codex"})
	if err != nil || ids(selected) != "%2" {
		t.Fatalf("--panes=%%1,%%2 --type=codex = %q (err %v), want %%2", ids(selected), err)
	}
	selected, err = resolveInterruptTargets(panes, nil, false, []string{"aider"})
	if err != nil || len(selected) != 0 {
		t.Fatalf("--type=aider = %q (err %v), want no targets", ids(selected), err)
	}
	selected, err = resolveInterruptTargets(panes, nil, false, nil)
	if err != nil || ids(selected) != "%1,%2,%3" {
		t.Fatalf("no --type = %q (err %v), want every agent pane", ids(selected), err)
	}
}

func TestInterruptPaneStateUsesOnlyFreshCurrentObservation(t *testing.T) {
	now := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	unavailable := status.PaneObservation{
		Current: status.StateObservation{
			Status:     status.AgentStatus{State: status.StateUnknown},
			ObservedAt: now,
			Freshness:  status.FreshnessUnavailable,
			Error:      "capture failed",
		},
	}
	got := interruptPaneStateFromObservation(unavailable, "codex")
	if got.State != "unknown" || got.ObservationFreshness != "unavailable" {
		t.Fatalf("unavailable state = %+v", got)
	}
	if got.LastOutput != "" {
		t.Fatalf("unavailable state exposed last output %q", got.LastOutput)
	}

	working := status.PaneObservation{
		RawOutput: "codex>",
		Current: status.StateObservation{
			Status:     status.AgentStatus{State: status.StateWorking},
			ObservedAt: now,
			Freshness:  status.FreshnessFresh,
			Confidence: 0.95,
		},
	}
	got = interruptPaneStateFromObservation(working, "codex")
	if got.State != "active" {
		t.Fatalf("canonical working state = %+v, want active", got)
	}
}

// TestMarkInterruptFailuresFlipsEnvelope verifies the fail-loud behavior (#172):
// when one or more interrupt actions failed but the envelope still claims
// success, mark it failed; do not clobber an already-failed envelope; do not
// flip when there are no failures.
func TestMarkInterruptFailuresFlipsEnvelope(t *testing.T) {
	t.Run("flips on recorded failure", func(t *testing.T) {
		out := &InterruptOutput{
			RobotResponse: NewRobotResponse(true),
			Failed:        []InterruptError{{Pane: "1", Reason: "failed to send Ctrl+C"}},
		}
		markInterruptFailures(InterruptOptions{Session: "proj"}, out)
		if out.Success {
			t.Errorf("expected success=false after a failed action")
		}
		if out.ErrorCode != ErrCodeInternalError {
			t.Errorf("expected error_code=%q, got %q", ErrCodeInternalError, out.ErrorCode)
		}
		if out.Hint == "" {
			t.Errorf("expected a remediation hint")
		}
	})

	t.Run("no flip without failures", func(t *testing.T) {
		out := &InterruptOutput{RobotResponse: NewRobotResponse(true)}
		markInterruptFailures(InterruptOptions{Session: "proj"}, out)
		if !out.Success {
			t.Errorf("expected success to stay true with no failures")
		}
	})

	t.Run("does not clobber existing error envelope", func(t *testing.T) {
		out := &InterruptOutput{
			RobotResponse: NewErrorResponse(nil, ErrCodeTimeout, "increase timeout"),
			Failed:        []InterruptError{{Pane: "1", Reason: "boom"}},
		}
		markInterruptFailures(InterruptOptions{Session: "proj"}, out)
		if out.ErrorCode != ErrCodeTimeout {
			t.Errorf("expected timeout error_code preserved, got %q", out.ErrorCode)
		}
	})
}

// TestInterruptEmptyTargetHint verifies the empty-target remediation hint lists
// the panes that exist and warns about window-local addressing under --panes.
func TestInterruptEmptyTargetHint(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0},
		{ID: "%2", Index: 1, WindowIndex: 0},
	}
	hint := interruptEmptyTargetHint(InterruptOptions{Session: "proj", Panes: []string{"5"}}, panes)
	if !strings.Contains(hint, "window-local") {
		t.Errorf("expected window-local warning under --panes, got %q", hint)
	}
	if !strings.Contains(hint, "0") || !strings.Contains(hint, "1") {
		t.Errorf("expected present pane indices 0 and 1 in hint, got %q", hint)
	}

	hintNoFilter := interruptEmptyTargetHint(InterruptOptions{Session: "proj"}, panes)
	if strings.Contains(hintNoFilter, "window-local") {
		t.Errorf("did not expect window-local warning without --panes, got %q", hintNoFilter)
	}
}

// TestGetInterruptUnknownSessionFailsLoud exercises the real GetInterrupt path
// for a session that does not exist (no live tmux needed): it must report
// success:false / SESSION_NOT_FOUND.
func TestGetInterruptUnknownSessionFailsLoud(t *testing.T) {
	out, err := GetInterrupt(InterruptOptions{Session: "ntm-nonexistent-session-for-test-172"})
	if err != nil {
		t.Fatalf("GetInterrupt returned unexpected error: %v", err)
	}
	if out.Success {
		t.Errorf("expected success=false for nonexistent session")
	}
	if out.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("expected error_code=%q, got %q", ErrCodeSessionNotFound, out.ErrorCode)
	}
}

func TestInterruptFollowUpAppliesRedactionBeforeSideEffects(t *testing.T) {
	const secret = "hunter2hunter2"
	input := "continue with password=" + secret

	t.Run("block", func(t *testing.T) {
		opts := InterruptOptions{Message: input, Redaction: redaction.Config{Mode: redaction.ModeBlock}}
		output := &InterruptOutput{RobotResponse: NewRobotResponse(true), Failed: []InterruptError{}}
		if !interruptMessageBlocked(&opts, output) {
			t.Fatal("block mode authorized interrupt follow-up")
		}
		if opts.Message != "" || output.Success || output.ErrorCode != "SENSITIVE_DATA_BLOCKED" || output.Redaction == nil || output.Redaction.Action != "block" {
			t.Fatalf("blocked interrupt output=%+v opts=%+v", output, opts)
		}
		if strings.Contains(output.Message, secret) || strings.Contains(output.Error, secret) {
			t.Fatalf("blocked interrupt leaked secret: %+v", output)
		}
	})

	t.Run("redact", func(t *testing.T) {
		opts := InterruptOptions{Message: input, Redaction: redaction.Config{Mode: redaction.ModeRedact}}
		output := &InterruptOutput{RobotResponse: NewRobotResponse(true), Failed: []InterruptError{}}
		if interruptMessageBlocked(&opts, output) {
			t.Fatal("redact mode blocked sanitized follow-up")
		}
		if strings.Contains(opts.Message, secret) || strings.Contains(output.Message, secret) ||
			!strings.Contains(opts.Message, "[REDACTED:PASSWORD:") || output.Redaction == nil || output.Redaction.Action != "redact" {
			t.Fatalf("redacted interrupt output=%+v opts=%+v", output, opts)
		}
	})
}

// GH#251 phase 2: grok panes accept automated prompt delivery, so a follow-up
// message targeting a mixed claude+grok batch passes the preflight.
func TestInterruptFollowUpAcceptsMixedGrokTargets(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, Type: tmux.AgentClaude, Title: "proj__cc_1"},
		{ID: "%2", Index: 1, Type: tmux.AgentUnknown, Title: "proj__grok_1"},
	}
	if err := validateInterruptFollowUpTargets(panes, panes, InterruptOptions{Session: "proj", Message: "continue"}); err != nil {
		t.Fatalf("follow-up preflight error = %v, want nil (grok delivery is supported)", err)
	}
	if err := validateInterruptFollowUpTargets(panes, panes, InterruptOptions{Session: "proj"}); err != nil {
		t.Fatalf("message-less interrupt preflight = %v, want allowed", err)
	}
}

func TestObserveInterruptPollRefreshesActivityAndFailsClosed(t *testing.T) {
	pane := tmux.Pane{ID: "%41", Index: 0, WindowIndex: 2, Type: tmux.AgentUser}
	refreshed := time.Now().UTC().Add(-time.Second)
	observation := observeInterruptPoll(
		newRobotSessionObserver(10),
		"session",
		pane,
		func(target string, lines int) (string, error) {
			if target != pane.ID || lines != 10 {
				t.Fatalf("capture called with %q/%d", target, lines)
			}
			return "", nil
		},
		func(target string) (time.Time, error) {
			if target != pane.ID {
				t.Fatalf("activity called with %q", target)
			}
			return refreshed, nil
		},
	)
	if !observation.Current.Status.LastActive.Equal(refreshed) {
		t.Fatalf("LastActive=%v, want refreshed %v", observation.Current.Status.LastActive, refreshed)
	}

	unavailable := observeInterruptPoll(
		newRobotSessionObserver(10),
		"session",
		pane,
		func(string, int) (string, error) { return "", nil },
		func(string) (time.Time, error) { return time.Time{}, errors.New("activity unavailable") },
	)
	if unavailable.Current.Freshness != status.FreshnessUnavailable || unavailable.Current.Error == "" {
		t.Fatalf("activity failure authorized current state: %+v", unavailable.Current)
	}
}

func TestGetInterruptContext_PreCanceledReturnsCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := GetInterrupt(InterruptOptions{
		Context:   ctx,
		Session:   "any-session",
		Message:   "follow up",
		TimeoutMs: 100,
	})
	if err != nil {
		t.Fatalf("GetInterrupt(canceled) err = %v", err)
	}
	if out == nil || out.Success || out.ErrorCode != "CANCELED" || out.TimedOut {
		t.Fatalf("GetInterrupt(canceled) = %+v, want ErrorCode=CANCELED TimedOut=false", out)
	}

	dlCtx, dlCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dlCancel()
	dlOut, err := GetInterrupt(InterruptOptions{
		Context:   dlCtx,
		Session:   "any-session",
		TimeoutMs: 100,
	})
	if err != nil {
		t.Fatalf("GetInterrupt(deadline) err = %v", err)
	}
	if dlOut == nil || dlOut.Success || dlOut.ErrorCode != ErrCodeTimeout || dlOut.TimedOut {
		t.Fatalf("GetInterrupt(deadline) = %+v, want ErrorCode=%s TimedOut=false (pre-wait deadline)", dlOut, ErrCodeTimeout)
	}
}
