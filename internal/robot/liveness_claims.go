package robot

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/robot
// (bd-g2-claims-backlog-o787y). Each claim references the real function on
// the read path of the key; see internal/config/liveness.go for the
// contract. Per-call-site claims that predate this file (retry.alerts.*,
// integrations.xf.enabled) stay where they are.
func init() {
	// Alert thresholds: copied from cfg.Alerts into alerts.Config by
	// alertConfigForProject (robot.go), on the GetAlerts/status/snapshot path.
	for _, key := range []string{
		"alerts.enabled",
		"alerts.agent_stuck_minutes",
		"alerts.disk_low_threshold_gb",
		"alerts.mail_backlog_threshold",
		"alerts.bead_stale_hours",
		"alerts.context_warning_threshold",
		"alerts.resolved_prune_minutes",
	} {
		config.RegisterReader(key, alertConfigForProject)
	}
	// Disk-full trajectory horizon (disk.go).
	config.RegisterReader("alerts.disk_full_horizon_hours", attachSnapshotDisk)

	// Spawn admission control (spawn.go): only these spawn_pacing leaves are
	// consumed at runtime; the rest of the section is re-tagged as dead
	// (bd-6otuk).
	config.RegisterReader("spawn_pacing.enabled", collectSpawnAdmissionInputWithPanes)
	config.RegisterReader("spawn_pacing.max_concurrent_spawns", collectSpawnAdmissionInputWithPanes)
	config.RegisterReader("spawn_pacing.agent_caps.claude_max_concurrent", spawnAdmissionAgentLimit)
	config.RegisterReader("spawn_pacing.agent_caps.codex_max_concurrent", spawnAdmissionAgentLimit)
	config.RegisterReader("spawn_pacing.agent_caps.gemini_max_concurrent", spawnAdmissionAgentLimit)

	// Swarm snapshot surface (robot.go).
	config.RegisterReader("swarm.enabled", buildSwarmSnapshot)
	config.RegisterReader("swarm.default_scan_dir", buildSwarmSnapshotPlan)

	// Agent routing/scoring weights (routing.go).
	for _, key := range []string{
		"routing.context_weight",
		"routing.state_weight",
		"routing.recency_weight",
		"routing.affinity_enabled",
		"routing.affinity_bonus",
		"routing.exclude_context_above",
		"routing.exclude_if_generating",
		"routing.exclude_if_rate_limited",
		"routing.exclude_if_error",
	} {
		config.RegisterReader(key, NewAgentScorerFromConfig)
	}

	// Bulk-assign prompt template + operator gating (bulk_assign.go).
	config.RegisterReader("assign.prompt_template", GetBulkAssign)
	config.RegisterReader("assign.prompt_template_file", GetBulkAssign)
	config.RegisterReader("assign.operator_gated_labels", GetBulkAssign)

	// Command palette entries + persisted state (tui_parity.go).
	for _, key := range []string{
		"palette.key",
		"palette.label",
		"palette.category",
		"palette.prompt",
		"palette.tags",
		"palette_state.favorites",
		"palette_state.pinned",
	} {
		config.RegisterReader(key, GetPalette)
	}

	// Model alias maps: restartModelVars → cfg.Models.AliasesFor(agentType)
	// returns each per-provider map (restart_pane.go).
	for _, key := range []string{
		"models.claude",
		"models.codex",
		"models.gemini",
		"models.grok",
		"models.ollama",
		"models.cursor",
		"models.windsurf",
		"models.aider",
		"models.opencode",
	} {
		config.RegisterReader(key, restartModelVars)
	}
	// Per-provider default model fallbacks read directly (robot.go).
	config.RegisterReader("models.default_gemini", modelNameForPane)
	config.RegisterReader("models.default_grok", modelNameForPane)
	config.RegisterReader("models.default_ollama", modelNameForPane)
	config.RegisterReader("models.default_opencode", modelNameForPane)

	// DCG status surface (dcg_status.go).
	config.RegisterReader("integrations.dcg.allow_override", resolveDCGSettings)
	config.RegisterReader("integrations.dcg.audit_log", resolveDCGSettings)

	// Safety profile echoed on the robot status/snapshot surfaces (robot.go).
	config.RegisterReader("safety.profile", newStatusOutput)
}
