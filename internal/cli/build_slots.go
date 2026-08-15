package cli

// Build-slot lease reconciliation on worktree-mode transitions (ntm-83dz).
//
// Build-slot leases belong to the MCP Agent Mail server; NTM itself never
// calls acquire_build_slot. The leases NTM is responsible for are the ones
// acquired by the pane identities its own orchestration registered (and
// whose registration tokens it persisted in the session agent registry).
// When a spawn flips a session's isolation mode (--worktrees on a session
// that previously ran without it, or vice versa), holder identities from
// the previous topology lose their panes while their leases — up to an
// hour of TTL — stay live and block other agents' builds.
//
// This reconciliation is strictly best-effort: it lists active leases from
// the only listing surface the server provides (the shared on-disk
// archive; the server has no list tool or resource), releases the ones
// held by this session's now-paneless identities via the real
// release_build_slot tool, and warns about the ones it cannot release.
// Any failure — Agent Mail down, archive missing, no registry — degrades
// to a note and never blocks the spawn.

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type buildSlotReconcileDeps struct {
	loadRegistry func(session string, projectKeys ...string) (*agentmail.SessionAgentRegistry, error)
	listLeases   func(projectKey string, now time.Time) ([]agentmail.BuildSlotLease, error)
	release      func(ctx context.Context, projectKey string, registry *agentmail.SessionAgentRegistry, lease agentmail.BuildSlotLease) error
}

func defaultBuildSlotReconcileDeps() buildSlotReconcileDeps {
	return buildSlotReconcileDeps{
		loadRegistry: agentmail.LoadBestSessionAgentRegistry,
		listLeases: func(projectKey string, now time.Time) ([]agentmail.BuildSlotLease, error) {
			return agentmail.ListActiveBuildSlotLeases(agentmail.DefaultBuildSlotArchiveRoot(), projectKey, now)
		},
		release: func(ctx context.Context, projectKey string, registry *agentmail.SessionAgentRegistry, lease agentmail.BuildSlotLease) error {
			client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
			registry.HydrateClientTokens(client)
			releaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			result, err := client.ReleaseBuildSlot(releaseCtx, projectKey, lease.Agent, lease.Slot, lease.Branch)
			if err != nil {
				return err
			}
			if !result.Released {
				return fmt.Errorf("server reported lease not released (may already be inactive)")
			}
			return nil
		},
	}
}

// buildSlotReconcileResult summarizes one reconciliation pass for logging.
type buildSlotReconcileResult struct {
	Transition bool     // isolation mode changed for this session
	Released   []string // "slot/agent" released successfully
	Warnings   []string // leases we detected but could not (or must not) release
	Skipped    string   // non-empty degraded-source note when the pass could not run
}

// reconcileBuildSlotLeasesForTransition releases build-slot leases held by
// this session's registered identities that no longer have a live pane,
// when the isolation mode changed (prevWorktrees != useWorktrees).
// prevWorktrees must be snapshotted BEFORE this spawn provisions any
// worktrees, or a shared→worktree transition masks itself. panes is the
// session's current pane list (pre-existing panes stay live and keep their
// leases). Never returns an error: everything is folded into the result.
func reconcileBuildSlotLeasesForTransition(ctx context.Context, session, projectDir string, prevWorktrees, useWorktrees bool, panes []tmux.Pane, deps buildSlotReconcileDeps) buildSlotReconcileResult {
	result := buildSlotReconcileResult{}

	if prevWorktrees == useWorktrees {
		return result // no isolation-mode change; leases are the holders' business
	}
	result.Transition = true

	registry, err := deps.loadRegistry(session, projectDir)
	if err != nil || registry == nil {
		// Benign and common (fresh session, or Agent Mail never used):
		// nothing NTM-orchestrated to reconcile, and nothing to log.
		return result
	}

	leases, err := deps.listLeases(projectDir, time.Now().UTC())
	if err != nil {
		result.Skipped = fmt.Sprintf("build-slot lease listing unavailable (%v); continuing without reconciliation", err)
		return result
	}
	if len(leases) == 0 {
		return result
	}

	liveNames := map[string]bool{}
	for _, pane := range panes {
		if name, ok := registry.GetAgent(pane.Title, pane.ID); ok {
			liveNames[name] = true
		}
	}
	knownNames := map[string]bool{}
	for _, name := range registry.Agents {
		knownNames[name] = true
	}
	for _, name := range registry.PaneIDMap {
		knownNames[name] = true
	}

	for _, lease := range leases {
		label := fmt.Sprintf("%s/%s", lease.Slot, lease.Agent)
		if !knownNames[lease.Agent] {
			continue // another session's or orchestrator's lease; not ours to touch
		}
		if liveNames[lease.Agent] {
			continue // holder still has a live pane; lease is legitimate
		}
		if registry.RegistrationToken(lease.Agent) == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"stale build-slot lease %s survives the mode switch: no registration token persisted; release via the Agent Mail release_build_slot MCP tool or ntm --robot-diagnose", label))
			continue
		}
		if releaseErr := deps.release(ctx, projectDir, registry, lease); releaseErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"failed to release stale build-slot lease %s: %v", label, releaseErr))
			continue
		}
		result.Released = append(result.Released, label)
	}
	return result
}

// reconcileBuildSlotLeasesOnSpawn is the spawn-path entry point.
// prevWorktrees is the pre-provisioning snapshot of
// worktrees.SessionHasWorktrees — the session's previous isolation mode as
// recorded by its surviving worktree root (which persists across `ntm
// kill`, so killed-and-respawned sessions transition correctly too). Logs
// results and audit-logs every release. Best-effort by construction.
func reconcileBuildSlotLeasesOnSpawn(ctx context.Context, session, projectDir string, prevWorktrees, useWorktrees bool, panes []tmux.Pane) {
	result := reconcileBuildSlotLeasesForTransition(ctx, session, projectDir, prevWorktrees, useWorktrees, panes, defaultBuildSlotReconcileDeps())
	if !result.Transition {
		return
	}

	for _, label := range result.Released {
		_ = audit.LogEvent(session, audit.EventTypeStateChange, audit.ActorSystem, "build_slot.release", map[string]interface{}{
			"source":  "worktree_mode_transition",
			"lease":   label,
			"project": projectDir,
			"success": true,
		}, nil)
	}
	if !IsJSONOutput() {
		if len(result.Released) > 0 {
			output.PrintInfof("Worktree mode changed: released %d stale build-slot lease(s): %v", len(result.Released), result.Released)
		}
		for _, warning := range result.Warnings {
			output.PrintWarningf("Worktree mode changed: %s", warning)
		}
		if result.Skipped != "" {
			output.PrintInfof("Build-slot reconciliation skipped: %s", result.Skipped)
		}
	}
}
