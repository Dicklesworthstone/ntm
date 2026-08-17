package tmux

import (
	"strings"
	"testing"
)

// TestEnsurePaneBorderStatus verifies the H8 visibility fix
// (bd-ws7-docs-ux-truth-tqh3l.8): pane-border-status is enabled for the
// session (so pane titles are visible on stock tmux), the option is strictly
// session-local (the global window default is never mutated), and an existing
// explicit preference is respected.
func TestEnsurePaneBorderStatus(t *testing.T) {
	skipIfNoTmux(t)
	session := createTestSession(t)

	globalBefore, gerr := DefaultClient.Run("show-options", "-g", "-w", "-v", "pane-border-status")

	if err := EnsurePaneBorderStatus(session); err != nil {
		t.Fatalf("EnsurePaneBorderStatus: %v", err)
	}

	effective, err := DefaultClient.Run("show-options", "-A", "-w", "-v", "-t", session, "pane-border-status")
	if err != nil {
		t.Fatalf("show-options: %v", err)
	}
	switch strings.TrimSpace(effective) {
	case "top", "bottom":
	default:
		t.Errorf("effective pane-border-status = %q, want top or bottom", strings.TrimSpace(effective))
	}

	// Session-local contract: the global window default must be untouched.
	if gerr == nil {
		globalAfter, err := DefaultClient.Run("show-options", "-g", "-w", "-v", "pane-border-status")
		if err != nil {
			t.Fatalf("show-options -g after: %v", err)
		}
		if strings.TrimSpace(globalAfter) != strings.TrimSpace(globalBefore) {
			t.Errorf("global pane-border-status mutated: %q -> %q", globalBefore, globalAfter)
		}
	}
}

// TestEnsurePaneBorderStatusRespectsExistingPreference verifies an explicit
// user choice (bottom) on the session's window is left alone.
func TestEnsurePaneBorderStatusRespectsExistingPreference(t *testing.T) {
	skipIfNoTmux(t)
	session := createTestSession(t)

	if err := DefaultClient.RunSilent("set-option", "-w", "-t", session, "pane-border-status", "bottom"); err != nil {
		t.Fatalf("preset pane-border-status: %v", err)
	}
	if err := EnsurePaneBorderStatus(session); err != nil {
		t.Fatalf("EnsurePaneBorderStatus: %v", err)
	}
	out, err := DefaultClient.Run("show-options", "-A", "-w", "-v", "-t", session, "pane-border-status")
	if err != nil {
		t.Fatalf("show-options: %v", err)
	}
	if got := strings.TrimSpace(out); got != "bottom" {
		t.Errorf("pane-border-status = %q, want existing preference %q preserved", got, "bottom")
	}
}
