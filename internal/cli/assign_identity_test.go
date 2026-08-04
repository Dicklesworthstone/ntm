package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestAssignmentAgentIdentityForPanePrefersRegisteredIdentity covers GH#239
// defect 3: assign dispatch used to fabricate Session_type_index agent names
// instead of reading the spawn-time registered pane identity, so Agent Mail
// rejected reservations/deliveries with "Agent not found in project".
func TestAssignmentAgentIdentityForPanePrefersRegisteredIdentity(t *testing.T) {
	tmp := t.TempDir()
	// Force the canonical identity base dir into the sandbox. Cannot use
	// t.Parallel() alongside t.Setenv.
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	projectDir := t.TempDir()
	pane := tmux.Pane{ID: "%40", Index: 2, Type: tmux.AgentCodex}

	// No identity registered: synthetic fallback.
	if got := assignmentAgentIdentityForPane(projectDir, "demo", "codex", pane, false); got != "demo_codex_2" {
		t.Errorf("fallback name = %q, want %q", got, "demo_codex_2")
	}

	// Registered identity wins.
	if _, err := agentmail.WriteIdentity(projectDir, pane.ID, "GreenFalcon"); err != nil {
		t.Fatalf("WriteIdentity: %v", err)
	}
	if got := assignmentAgentIdentityForPane(projectDir, "demo", "codex", pane, false); got != "GreenFalcon" {
		t.Errorf("registered identity = %q, want %q", got, "GreenFalcon")
	}

	// Empty project dir degrades to the synthetic name (never panics).
	if got := assignmentAgentIdentityForPane("", "demo", "codex", pane, false); got != "demo_codex_2" {
		t.Errorf("empty projectDir name = %q, want %q", got, "demo_codex_2")
	}
}
