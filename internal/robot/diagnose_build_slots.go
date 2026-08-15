package robot

// Stale build-slot lease detection for --robot-diagnose (ntm-83dz).
//
// Build-slot leases are owned by the MCP Agent Mail server; NTM never
// acquires them itself. Agents spawned by NTM can, though — and when panes
// are torn down or a session's worktree mode flips, a holder identity can
// vanish while its (up to 1 hour) lease stays live. The diagnose pass
// lists active leases via the only listing surface the server provides
// (the shared on-disk archive; there is no list tool or resource),
// correlates holders against the session's agent registry and live panes,
// and reports orphans as auto-fixable attention items. --fix releases
// them through the real release_build_slot tool, authenticated with the
// holder's persisted registration token, and audit-logs the release.
//
// Every failure path degrades gracefully: an unreachable server or a
// missing archive yields a degraded-source note, never a diagnose error.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// BuildSlotDiagnosis summarizes the stale-build-slot check.
type BuildSlotDiagnosis struct {
	Checked        bool                  `json:"checked"`
	Source         string                `json:"source"` // "agent_mail_archive"
	Degraded       bool                  `json:"degraded,omitempty"`
	DegradedReason string                `json:"degraded_reason,omitempty"`
	ActiveLeases   int                   `json:"active_leases"`
	StaleLeases    []StaleBuildSlotLease `json:"stale_leases"`
}

// StaleBuildSlotLease is an active lease whose holder identity has no
// live pane in the diagnosed session.
type StaleBuildSlotLease struct {
	Slot        string `json:"slot"`
	Agent       string `json:"agent"`
	Branch      string `json:"branch,omitempty"`
	ExpiresTS   string `json:"expires_ts,omitempty"`
	AutoFixable bool   `json:"auto_fixable"`
	Reason      string `json:"reason"`
}

const buildSlotLeaseSource = "agent_mail_archive"

func defaultDiagnoseProjectKey() (string, error) {
	return os.Getwd()
}

func defaultListBuildSlotLeases(projectKey string, now time.Time) ([]agentmail.BuildSlotLease, error) {
	return agentmail.ListActiveBuildSlotLeases(agentmail.DefaultBuildSlotArchiveRoot(), projectKey, now)
}

func defaultLoadAgentRegistry(session string, projectKeys ...string) (*agentmail.SessionAgentRegistry, error) {
	return agentmail.LoadBestSessionAgentRegistry(session, projectKeys...)
}

func defaultReleaseBuildSlot(ctx context.Context, projectKey string, registry *agentmail.SessionAgentRegistry, lease agentmail.BuildSlotLease) error {
	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	registry.HydrateClientTokens(client)
	releaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := client.ReleaseBuildSlot(releaseCtx, projectKey, lease.Agent, lease.Slot, lease.Branch)
	if err != nil {
		return err
	}
	if !result.Released {
		return fmt.Errorf("server did not release build slot %q for %s (lease may already be inactive)", lease.Slot, lease.Agent)
	}
	return nil
}

// collectBuildSlotDiagnosis runs the stale-build-slot check. panes must be
// the session's full (unfiltered) pane list so holder liveness is judged
// against every pane, not just the one being diagnosed. Never returns an
// error: every failure is folded into a degraded-source note.
func collectBuildSlotDiagnosis(session string, panes []tmux.Pane, deps diagnoseDependencies) *BuildSlotDiagnosis {
	diag := &BuildSlotDiagnosis{
		Source:      buildSlotLeaseSource,
		StaleLeases: []StaleBuildSlotLease{},
	}

	projectKey, err := deps.projectKey()
	if err != nil || projectKey == "" {
		diag.Degraded = true
		diag.DegradedReason = fmt.Sprintf("project directory unavailable: %v", err)
		return diag
	}

	registry, err := deps.loadAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		// Without a registry we cannot tell which holders belonged to this
		// session, and we hold no tokens to release with. Not degraded —
		// sessions spawned without Agent Mail simply have nothing to check.
		diag.Checked = true
		diag.DegradedReason = "no session agent registry; orphan correlation skipped"
		return diag
	}

	leases, err := deps.listBuildSlotLeases(projectKey, time.Now().UTC())
	if err != nil {
		diag.Degraded = true
		if errors.Is(err, agentmail.ErrBuildSlotListingUnavailable) {
			diag.DegradedReason = "build-slot lease listing unsupported by Agent Mail server (no list tool/resource) and archive not found"
		} else {
			diag.DegradedReason = fmt.Sprintf("build-slot lease listing failed: %v", err)
		}
		return diag
	}

	diag.Checked = true
	diag.ActiveLeases = len(leases)
	if len(leases) == 0 {
		return diag
	}

	// An identity is live when a current pane maps to it, by pane ID or
	// pane title, in the session registry.
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
		if !knownNames[lease.Agent] {
			continue // holder belongs to another session/orchestrator; not ours to touch
		}
		if liveNames[lease.Agent] {
			continue // holder still has a live pane
		}
		fixable := registry.RegistrationToken(lease.Agent) != ""
		stale := StaleBuildSlotLease{
			Slot:        lease.Slot,
			Agent:       lease.Agent,
			Branch:      lease.Branch,
			AutoFixable: fixable,
			Reason: fmt.Sprintf(
				"build slot %q held by %s but that identity has no live pane in session %q",
				lease.Slot, lease.Agent, session),
		}
		if !lease.ExpiresTS.IsZero() {
			stale.ExpiresTS = lease.ExpiresTS.UTC().Format(time.RFC3339)
		}
		if !fixable {
			stale.Reason += "; no registration token persisted, release manually via the Agent Mail release_build_slot MCP tool"
		}
		diag.StaleLeases = append(diag.StaleLeases, stale)
	}
	return diag
}

// buildSlotRecommendations converts stale leases into diagnose
// recommendations (the attention items --fix acts on).
func buildSlotRecommendations(session string, diag *BuildSlotDiagnosis) []DiagnoseRecommendation {
	if diag == nil || len(diag.StaleLeases) == 0 {
		return nil
	}
	recs := make([]DiagnoseRecommendation, 0, len(diag.StaleLeases))
	for i := range diag.StaleLeases {
		stale := diag.StaleLeases[i]
		rec := DiagnoseRecommendation{
			Pane:        -1,
			PaneTarget:  fmt.Sprintf("build_slot:%s/%s", stale.Slot, stale.Agent),
			Status:      "stale_build_slot",
			Action:      "release_build_slot",
			Reason:      stale.Reason,
			AutoFixable: stale.AutoFixable,
			BuildSlot:   &stale,
		}
		if stale.AutoFixable {
			rec.FixCommand = fmt.Sprintf("ntm --robot-diagnose=%s --fix", session)
		} else {
			rec.FixCommand = fmt.Sprintf("release_build_slot(project_key=<project>, agent_name=%q, slot=%q) via the Agent Mail MCP server", stale.Agent, stale.Slot)
		}
		recs = append(recs, rec)
	}
	return recs
}

// executeBuildSlotRelease performs the --fix release for one stale-lease
// recommendation and audit-logs the outcome.
func executeBuildSlotRelease(ctx context.Context, session string, rec DiagnoseRecommendation, deps diagnoseDependencies) (bool, string) {
	if rec.BuildSlot == nil {
		return false, "Recommendation is missing build-slot details"
	}
	projectKey, err := deps.projectKey()
	if err != nil || projectKey == "" {
		return false, fmt.Sprintf("Failed to resolve project directory: %v", err)
	}
	registry, err := deps.loadAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		return false, "Session agent registry unavailable; cannot authenticate as lease holder"
	}
	lease := agentmail.BuildSlotLease{
		Slot:   rec.BuildSlot.Slot,
		Agent:  rec.BuildSlot.Agent,
		Branch: rec.BuildSlot.Branch,
	}
	releaseErr := deps.releaseBuildSlot(ctx, projectKey, registry, lease)
	_ = audit.LogEvent(session, audit.EventTypeStateChange, audit.ActorSystem, "build_slot.release", map[string]interface{}{
		"source":  "robot_diagnose_fix",
		"slot":    lease.Slot,
		"agent":   lease.Agent,
		"branch":  lease.Branch,
		"project": projectKey,
		"success": releaseErr == nil,
		"error": func() string {
			if releaseErr != nil {
				return releaseErr.Error()
			}
			return ""
		}(),
	}, nil)
	if releaseErr != nil {
		return false, fmt.Sprintf("Failed to release build slot %q held by %s: %v", lease.Slot, lease.Agent, releaseErr)
	}
	return true, fmt.Sprintf("Released stale build slot %q held by %s", lease.Slot, lease.Agent)
}
