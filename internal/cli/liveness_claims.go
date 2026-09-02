package cli

import (
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/cli
// (bd-g2-claims-backlog-o787y). Each claim references the real function on
// the read path; see internal/config/liveness.go for the contract.
// Pre-existing per-call-site claims (memory.send_*, cass.context.*,
// retry.agent_mail.*) stay in their own files.
func init() {
	// Agent Mail client + daemon wiring (mail.go, spawn.go, monitor.go).
	config.RegisterReader("agent_mail.enabled", newAgentMailClient)
	config.RegisterReader("agent_mail.url", newAgentMailClient)
	config.RegisterReader("agent_mail.token", newAgentMailClient)
	config.RegisterReader("agent_mail.auto_register", agentMailRegistrationEnabled)
	config.RegisterReader("agent_mail.supervisor_enabled", shouldSuperviseAgentMailDaemon)

	// Account rotation flows (rotate.go, coordinator.go).
	config.RegisterReader("rotation.auto_open_browser", rotateAllLimited)
	config.RegisterReader("rotation.continuation_prompt", executeReauthRotation)
	config.RegisterReader("rotation.usage_percent_threshold", loadCoordinatorRuntimeConfigWithNTM)
	config.RegisterReader("rotation.auto_confirm", loadCoordinatorRuntimeConfigWithNTM)
	// SuggestNextAccount matches on provider/email and surfaces alias
	// (rotate.go via config.RotationConfig.SuggestNextAccount).
	config.RegisterReader("rotation.accounts.provider", rotateAllLimited)
	config.RegisterReader("rotation.accounts.email", rotateAllLimited)
	config.RegisterReader("rotation.accounts.alias", rotateAllLimited)

	// UBS bug watch (bugs_watch.go).
	config.RegisterReader("bugs.interval", newBugsWatchCmd)
	config.RegisterReader("bugs.push_routing", runBugsWatch)
	config.RegisterReader("bugs.cooldown_minutes", runBugsWatch)

	// Robot output plumbing (root.go, pane_work_token.go).
	config.RegisterReader("robot.verbosity", resolveRobotVerbosity)
	config.RegisterReader("robot.output.format", resolveRobotFormat)
	config.RegisterReader("robot.semantic.stamp", semanticStampEnabled)
	config.RegisterReader("robot.semantic.window_minutes", resolveSemanticWindow)

	// Tmux session shape (create.go, add.go).
	config.RegisterReader("tmux.default_panes", runCreate)
	config.RegisterReader("tmux.history_limit", runCreate)
	config.RegisterReader("tmux.pane_init_delay_ms", executeAdd)

	// Top-level knobs.
	config.RegisterReader("palette_file", paletteWatchPaths)
	config.RegisterReader("projects_base", normalizeConfiguredProjectBase)
	config.RegisterReader("context.ms_skills", newContextBuildCmd)

	// Session recovery (spawn.go).
	for _, key := range []string{
		"recovery.enabled",
		"recovery.timeout_seconds",
		"recovery.include_beads_context",
		"recovery.include_agent_mail",
		"recovery.include_cm_memories",
		"recovery.max_recovery_tokens",
	} {
		config.RegisterReader(key, buildRecoveryContext)
	}
	config.RegisterReader("recovery.auto_inject_on_spawn", spawnSessionLogicContextWithOutput)
	config.RegisterReader("recovery.max_cm_rules", getMemoryContext)
	config.RegisterReader("recovery.max_cm_snippets", getMemoryContext)

	// Coordinator runtime config bridge (coordinator.go).
	for _, key := range []string{
		"coordinator.poll_interval",
		"coordinator.digest_interval",
		"coordinator.auto_assign",
		"coordinator.idle_threshold",
		"coordinator.assign_only_idle",
		"coordinator.conflict_notify",
		"coordinator.conflict_negotiate",
		"coordinator.send_digests",
		"coordinator.human_agent",
		"coordinator.mail_nudge",
		"coordinator.nudge_cooldown_seconds",
		"coordinator.nudge_message",
	} {
		config.RegisterReader(key, coordinatorConfigFromTOML)
	}

	// Checkpoints before mutating operations (add.go, send.go).
	for _, key := range []string{
		"checkpoints.enabled",
		"checkpoints.before_add_agents",
		"checkpoints.scrollback_lines",
		"checkpoints.include_git",
		"checkpoints.max_auto_checkpoints",
	} {
		config.RegisterReader(key, executeAdd)
	}
	config.RegisterReader("checkpoints.before_broadcast", runSendInternal)

	// Resilience monitor opt-in at spawn (spawn.go).
	config.RegisterReader("resilience.auto_restart", spawnSessionLogicContextWithOutput)

	// File reservation watcher (dashboard.go).
	for _, key := range []string{
		"file_reservation.enabled",
		"file_reservation.auto_reserve",
		"file_reservation.auto_release_idle_minutes",
		"file_reservation.notify_on_conflict",
		"file_reservation.extend_on_activity",
		"file_reservation.default_ttl_minutes",
		"file_reservation.poll_interval_seconds",
		"file_reservation.capture_lines",
		"file_reservation.debug",
	} {
		config.RegisterReader(key, startDashboardReservationWatcher)
	}

	// CASS engine base settings (send_cass.go; cass.context.* claimed there).
	config.RegisterReader("cass.enabled", robotSendCASSOptions)
	config.RegisterReader("cass.binary_path", robotSendCASSOptions)
	config.RegisterReader("cass.timeout", robotSendCASSOptions)

	// Assignment strategy (assign.go; template keys claimed in internal/robot).
	config.RegisterReader("assign.strategy", runAssign)
	config.RegisterReader("assign.idle_threshold", runWatchMode)

	// Send defaults (send.go).
	config.RegisterReader("send.base_prompt", resolveBasePrompt)
	config.RegisterReader("send.base_prompt_file", resolveBasePrompt)

	// Preflight strict mode (preflight.go).
	config.RegisterReader("preflight.strict", newPreflightCmd)

	// Gemini post-spawn setup (add.go, spawn.go).
	for _, key := range []string{
		"gemini_setup.auto_select_pro_model",
		"gemini_setup.ready_timeout_seconds",
		"gemini_setup.model_select_timeout_seconds",
		"gemini_setup.verbose",
	} {
		config.RegisterReader(key, executeAdd)
	}

	// Startup temp-file cleanup (cleanup.go, wired from root.go).
	config.RegisterReader("cleanup.auto_clean_on_startup", MaybeRunStartupCleanup)
	config.RegisterReader("cleanup.max_age_hours", MaybeRunStartupCleanup)
	config.RegisterReader("cleanup.verbose", MaybeRunStartupCleanup)

	// Encryption at rest: root.go's PersistentPreRunE builds
	// encryption.KeyConfig from cfg.Encryption and resolves keys; the
	// enabled flag gates that whole path.
	for _, key := range []string{
		"encryption.enabled",
		"encryption.key_source",
		"encryption.key_env",
		"encryption.key_file",
		"encryption.key_command",
		"encryption.key_format",
	} {
		config.RegisterReader(key, encryption.ResolveKey)
	}
	config.RegisterReader("encryption.active_key_id", encryption.ResolveKeyring)
	config.RegisterReader("encryption.keyring", encryption.ResolveKeyring)

	// Redaction: cfg.Redaction.ToRedactionLibConfig() feeds
	// redaction.ScanAndRedact throughout cli/coordinator (root.go wiring;
	// claim from here because internal/config imports internal/redaction).
	config.RegisterReader("redaction.mode", redaction.ScanAndRedact)
	config.RegisterReader("redaction.allowlist", redaction.ScanAndRedact)
	config.RegisterReader("redaction.extra_patterns", redaction.ScanAndRedact)
	config.RegisterReader("redaction.disabled_categories", redaction.ScanAndRedact)

	// Command hooks: the runtime engine re-decodes the same [[command_hooks]]
	// TOML tables via hooks.LoadAllCommandHooks (internal/hooks/config.go),
	// reached from hooks.NewExecutorFromConfig in send/add/create.
	// command_hooks.description has no reader anywhere → bd-6otuk.
	for _, key := range []string{
		"command_hooks.name",
		"command_hooks.event",
		"command_hooks.command",
		"command_hooks.enabled",
		"command_hooks.timeout",
		"command_hooks.workdir",
		"command_hooks.env",
		"command_hooks.continue_on_error",
	} {
		config.RegisterReader(key, hooks.LoadAllCommandHooks)
	}

	// Model resolution: ResolveModel → cfg.Models.GetModelName reads the
	// per-provider default_* fallbacks (agent_spec.go).
	config.RegisterReader("models.default_claude", ResolveModel)
	config.RegisterReader("models.default_codex", ResolveModel)

	// Agent launch command templates (spawn.go, add.go).
	for _, key := range []string{
		"agents.claude",
		"agents.codex",
		"agents.gemini",
		"agents.antigravity",
		"agents.grok",
		"agents.grok_policy",
		"agents.ollama",
		"agents.cursor",
		"agents.windsurf",
		"agents.aider",
		"agents.oc",
	} {
		config.RegisterReader(key, spawnAgentCommandTemplate)
	}

	// Per-agent-type default prompts (spawn.go → DefaultPrompts.ResolveForType).
	for _, key := range []string{
		"prompts.cc_default",
		"prompts.cc_default_file",
		"prompts.cod_default",
		"prompts.cod_default_file",
		"prompts.gmi_default",
		"prompts.gmi_default_file",
		"prompts.agy_default",
		"prompts.agy_default_file",
	} {
		config.RegisterReader(key, resolveSpawnPanePrompt)
	}

	// Integrations wired at spawn/send/serve time.
	config.RegisterReader("integrations.dcg.enabled", maybeBlockSendWithDCG)
	config.RegisterReader("integrations.dcg.binary_path", spawnSessionLogicContextWithOutput)
	config.RegisterReader("integrations.dcg.custom_blocklist", spawnSessionLogicContextWithOutput)
	config.RegisterReader("integrations.dcg.custom_whitelist", spawnSessionLogicContextWithOutput)
	config.RegisterReader("integrations.process_triage.enabled", runServe)
	config.RegisterReader("integrations.rch.enabled", spawnSessionLogicContextWithOutput)
	config.RegisterReader("integrations.rch.binary_path", spawnSessionLogicContextWithOutput)
	config.RegisterReader("integrations.rch.intercept_patterns", spawnSessionLogicContextWithOutput)
	// bv subprocess timeout: root.go PersistentPreRunE installs it into
	// internal/bv via ConfigureCommandTimeout (GH#253).
	config.RegisterReader("integrations.bv.timeout_seconds", bv.ConfigureCommandTimeout)

	// Swarm launch stagger (swarm.go).
	config.RegisterReader("swarm.stagger_delay_ms", runSwarm)

	// Swarm global-auth clobber escape hatch (swarm.go): the config value
	// seeds the --force-global-auth-clobber flag default (bd-6otuk fixed the
	// prior wiring bug where the flag's hard-coded false overwrote the config
	// value before any read).
	config.RegisterReader("swarm.force_global_auth_clobber", newSwarmCmd)

	// Ensemble defaults consumed in EVERY build by the --robot-ensemble-spawn
	// dispatch (root.go applyRobotEnsembleConfigDefaults); under
	// -tags ensemble_experimental the same keys also feed the real spawn
	// paths (ensemble_spawn.go, robot/ensemble_spawn.go). The remaining
	// ensemble.* keys are read only under the build tag — see
	// liveness_claims_ensemble.go and the permanent entries in
	// ci/allowlists/config.txt (bd-6otuk).
	config.RegisterReader("ensemble.default_ensemble", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.agent_mix", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.assignment", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.allow_advanced", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.mode_tier_default", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.budget.total", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.budget.per_agent", applyRobotEnsembleConfigDefaults)
	config.RegisterReader("ensemble.cache.enabled", applyRobotEnsembleConfigDefaults)
}
