package robot

import (
	"strings"
	"testing"

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
