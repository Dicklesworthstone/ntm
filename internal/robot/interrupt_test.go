package robot

import (
	"errors"
	"strings"
	"testing"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestSelectInterruptTargetsWindowAware documents the targeting fix at the heart
// of #172: on a multi-window / window-per-agent layout a bare --panes index
// selects the matching WINDOW rather than the window-local pane index. Every
// pane shares window-local index 0 here, so the pre-fix behavior either matched
// nothing (--panes=2) or broadcast to every window (--panes=0). The fix makes
// --panes=2 hit exactly the agent in window 2, and a genuinely-absent window
// (--panes=9) still resolves to an empty set so the fail-loud path triggers.
func TestSelectInterruptTargetsWindowAware(t *testing.T) {
	// Window-per-agent layout: three windows, each a single pane at index 0.
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentType("claude"), Title: "s__cc_1"},
		{ID: "%2", Index: 0, WindowIndex: 1, Type: tmux.AgentType("codex"), Title: "s__cod_1"},
		{ID: "%3", Index: 0, WindowIndex: 2, Type: tmux.AgentType("gemini"), Title: "s__gmi_1"},
	}

	// --panes=2 now selects the single pane in window 2 (was: matched nothing).
	got := selectInterruptTargets(panes, map[string]bool{"2": true}, false)
	if len(got) != 1 || got[0].ID != "%3" {
		t.Fatalf("expected --panes=2 to resolve to the window-2 pane %%3, got %v", got)
	}

	// A window that does not exist still resolves to an empty set (fail-loud).
	if got := selectInterruptTargets(panes, map[string]bool{"9": true}, false); len(got) != 0 {
		t.Fatalf("expected empty target set for absent window --panes=9, got %d", len(got))
	}

	// An explicit window.pane address resolves to exactly that pane.
	if got := selectInterruptTargets(panes, map[string]bool{"1.0": true}, false); len(got) != 1 || got[0].ID != "%2" {
		t.Fatalf("expected --panes=1.0 to resolve to %%2, got %v", got)
	}

	// A %N pane id resolves regardless of topology.
	if got := selectInterruptTargets(panes, map[string]bool{"%1": true}, false); len(got) != 1 || got[0].ID != "%1" {
		t.Fatalf("expected --panes=%%1 to resolve to %%1, got %v", got)
	}
}

func TestResolveInterruptTargetsSelectorsDeduplicateAndFailTyped(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentType("claude")},
		{ID: "%2", Index: 0, WindowIndex: 1, Type: tmux.AgentType("codex")},
		{ID: "%3", Index: 0, WindowIndex: 2, Type: tmux.AgentType("gemini")},
	}
	selected, err := resolveInterruptTargets(panes, []string{"2", "2.0", "%3"}, false)
	if err != nil {
		t.Fatalf("resolveInterruptTargets() error = %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "%3" {
		t.Fatalf("selected = %v, want one physical pane %%3", selected)
	}
	if _, err := resolveInterruptTargets(panes, []string{"9.0"}, false); err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v", err)
	}
	if _, err := resolveInterruptTargets(panes, []string{"1.x"}, false); err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("invalid selector error = %v", err)
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

func TestInterruptFollowUpRejectsMixedGrokTargetsBeforeActuation(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, Type: tmux.AgentClaude, Title: "proj__cc_1"},
		{ID: "%2", Index: 1, Type: tmux.AgentUnknown, Title: "proj__grok_1"},
	}
	err := validateInterruptFollowUpTargets(panes, panes, InterruptOptions{Session: "proj", Message: "continue"})
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrPromptDeliveryUnsupported {
		t.Fatalf("follow-up preflight error = %T %v, want %q", err, err, dispatchsvc.ErrPromptDeliveryUnsupported)
	}
	response := robotDispatchPrepareErrorResponse(err)
	if response.Success || response.ErrorCode != ErrCodeNotImplemented || ExitCodeForResponse(response) != 2 {
		t.Fatalf("interrupt response = %+v, want NOT_IMPLEMENTED / exit 2", response)
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
