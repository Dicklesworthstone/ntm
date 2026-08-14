package cli

import (
	"context"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// init wires the robot restart engine to the gated Agent Mail registration
// flow so every relaunch path — `ntm respawn`/`ntm restart`, `ntm robot
// restart-pane`, and health auto-restart — restores pane identities the same
// way spawn creates them (bd-vb7s3). The hook lives in cli because the gated
// helpers (agentMailRegistrationEnabled, registerSpawnedAgents) depend on the
// cli config global, and robot cannot import cli without a cycle.
func init() {
	robot.SetRestartPaneIdentityHook(registerRelaunchedPanes)
}

// registerRelaunchedPanes re-runs the gated Agent Mail registration flow for
// panes that were killed and relaunched. registerSpawnedAgents reuses prior
// identities from the session agent registry (#69), rewrites the canonical and
// legacy identity files (the legacy copy lives in /tmp and may have been
// cleaned), and re-persists the registry so resolve_pane_identity keeps
// working after a restart. Best-effort by construction: Agent Mail being
// disabled or unavailable never fails the restart (mirrors spawn).
func registerRelaunchedPanes(ctx context.Context, session string, panes []robot.RestartedAgentPane) {
	if len(panes) == 0 || !agentMailRegistrationEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := resolveWorkspaceProjectDirForExplicitSession(ctx, session)
	if err != nil {
		if !IsJSONOutput() {
			output.PrintWarningf("Agent Mail re-registration skipped for restarted panes: %v", err)
		}
		return
	}
	registerSessionAgent(ctx, session, dir)
	_ = registerSpawnedAgents(ctx, dir, session, relaunchRegistrationInputs(panes))
}

// relaunchRegistrationInputs translates restarted panes into the exact
// registration input used by spawn, add, and adopt. A relaunch does not know
// the resolved model, so the title-parsed variant stands in; the canonical
// pane ID and title are what the registry keys on for identity reuse.
func relaunchRegistrationInputs(panes []robot.RestartedAgentPane) []spawnedAgentInfo {
	agents := make([]spawnedAgentInfo, 0, len(panes))
	for _, pane := range panes {
		agents = append(agents, spawnedAgentInfo{
			paneIndex: pane.PaneIndex,
			paneID:    pane.PaneID,
			paneTitle: pane.PaneTitle,
			agentType: pane.AgentType,
			model:     pane.Variant,
		})
	}
	return agents
}
