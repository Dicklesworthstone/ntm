package cli

import (
	"context"
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/spawnidentity"
)

// init wires the robot restart engine to the gated Agent Mail registration
// flow so every relaunch path — `ntm respawn`/`ntm restart`, `ntm robot
// restart-pane`, and health auto-restart — restores pane identities the same
// way spawn creates them (bd-vb7s3). The hook lives in cli because the
// relaunch flow also needs cli-only helpers (workspace project resolution,
// the session-level registration) bound to the cli config global, and robot
// cannot import cli without a cycle.
// registerRelaunchedPanes re-runs the gated Agent Mail registration flow for
// panes that were killed and relaunched. registerSpawnedAgents reuses prior
// identities from the session agent registry (#69), rewrites the canonical and
// legacy identity files (the legacy copy lives in /tmp and may have been
// cleaned), and re-persists the registry so resolve_pane_identity keeps
// working after a restart. Best-effort by construction: Agent Mail being
// disabled or unavailable never fails the restart (mirrors spawn).
//
// Human invocations print the flow's warnings. Machine-output invocations
// (--robot-*, --json) own stdout, so the warnings are returned instead and the
// restart engine adds them to the response envelope's warnings.
func registerRelaunchedPanes(ctx context.Context, session string, panes []robot.RestartedAgentPane) []string {
	if len(panes) == 0 || !spawnidentity.RegistrationEnabled(cfg) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var collector *identityWarningCollector
	if isMachineOutput(ctx) {
		collector = &identityWarningCollector{}
		ctx = withIdentityWarningCollector(ctx, collector)
	}
	dir, err := resolveWorkspaceProjectDirForExplicitSession(ctx, session)
	if err != nil {
		message := fmt.Sprintf("Agent Mail re-registration skipped for restarted panes: %v", err)
		if collector != nil {
			collector.add(message)
		} else if !IsJSONOutput() && !isCLIIdentityOutputSuppressed(ctx) {
			output.PrintWarningf("%s", message)
		}
		return collector.list()
	}
	registerSessionAgent(ctx, session, dir)
	_ = registerSpawnedAgents(ctx, dir, session, relaunchRegistrationInputs(panes))
	return collector.list()
}

// relaunchRegistrationInputs translates restarted panes into the exact
// registration input used by spawn, add, and adopt. A relaunch does not know
// the resolved model, so the title-parsed variant stands in; the canonical
// pane ID and title are what the registry keys on for identity reuse.
func relaunchRegistrationInputs(panes []robot.RestartedAgentPane) []spawnidentity.Agent {
	agents := make([]spawnidentity.Agent, 0, len(panes))
	for _, pane := range panes {
		agents = append(agents, spawnidentity.Agent{
			PaneIndex: pane.PaneIndex,
			PaneID:    pane.PaneID,
			PaneTitle: pane.PaneTitle,
			AgentType: pane.AgentType,
			Model:     pane.Variant,
		})
	}
	return agents
}
