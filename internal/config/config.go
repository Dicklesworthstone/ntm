package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// DefaultCodexModel is the model NTM uses when the operator requests a
	// Codex pane without specifying a model.
	DefaultCodexModel = "gpt-5.6-sol"
	// DefaultCodexReasoningEffort is the Codex reasoning budget used when no
	// persona or agent spec provides one.
	DefaultCodexReasoningEffort = "xhigh"
	// DefaultClaudeReasoningEffort is the Claude reasoning budget used when no
	// persona or agent spec provides one. Kept alongside the Codex default so
	// both agent types share one obvious place to change the house default.
	DefaultClaudeReasoningEffort = "xhigh"
)

// validSynthesisStrategies defines the canonical synthesis strategy names.
// This is kept in sync with ensemble.strategyRegistry to break the import cycle.
var validSynthesisStrategies = map[string]bool{
	"manual":         true,
	"adversarial":    true,
	"consensus":      true,
	"creative":       true,
	"analytical":     true,
	"deliberative":   true,
	"prioritized":    true,
	"dialectical":    true,
	"meta-reasoning": true,
	"voting":         true,
	"argumentation":  true,
}

// deprecatedSynthesisStrategies maps deprecated names to their replacements.
var deprecatedSynthesisStrategies = map[string]string{
	"debate":     "dialectical",
	"weighted":   "prioritized",
	"sequential": "manual",
	"best-of":    "prioritized",
}

// validateSynthesisStrategy validates a synthesis strategy name.
// Returns nil if valid, or an error with migration hints for deprecated names.
func validateSynthesisStrategy(name string) error {
	if validSynthesisStrategies[name] {
		return nil
	}
	if replacement, ok := deprecatedSynthesisStrategies[name]; ok {
		return fmt.Errorf("strategy %q is deprecated; use %q instead", name, replacement)
	}
	return fmt.Errorf("unknown synthesis strategy %q", name)
}

// Config represents the main configuration
type Config struct {
	ProjectsBase    string                `toml:"projects_base"`
	Theme           string                `toml:"theme"`          // UI Theme (mocha, macchiato, nord, latte, auto)
	HelpVerbosity   string                `toml:"help_verbosity"` // Help verbosity: minimal or full (default: full)
	PaletteFile     string                `toml:"palette_file"`   // Path to command_palette.md (optional)
	Agents          AgentConfig           `toml:"agents"`
	Palette         []PaletteCmd          `toml:"palette"`
	PaletteState    PaletteState          `toml:"palette_state"`
	Tmux            TmuxConfig            `toml:"tmux"`
	Robot           RobotConfig           `toml:"robot"`
	CommandHooks    []CommandHookConfig   `toml:"command_hooks"`
	AgentMail       AgentMailConfig       `toml:"agent_mail"`
	Integrations    IntegrationsConfig    `toml:"integrations"` // External tool integrations (dcg, caam, etc.)
	Models          ModelsConfig          `toml:"models"`
	Alerts          AlertsConfig          `toml:"alerts"`
	Checkpoints     CheckpointsConfig     `toml:"checkpoints"`
	Notifications   notify.Config         `toml:"notifications"`
	Resilience      ResilienceConfig      `toml:"resilience"`
	Scanner         ScannerConfig         `toml:"scanner"`          // UBS scanner configuration
	Bugs            BugsConfig            `toml:"bugs"`             // UBS bug push routing (ntm bugs watch)
	CASS            CASSConfig            `toml:"cass"`             // CASS integration configuration
	Rotation        RotationConfig        `toml:"rotation"`         // Account rotation configuration
	GeminiSetup     GeminiSetupConfig     `toml:"gemini_setup"`     // Gemini post-spawn setup
	Context         ContextConfig         `toml:"context"`          // Context pack options
	ContextRotation ContextRotationConfig `toml:"context_rotation"` // Context window rotation
	SessionRecovery SessionRecoveryConfig `toml:"recovery"`         // Smart session recovery
	Cleanup         CleanupConfig         `toml:"cleanup"`          // Temp file cleanup configuration
	FileReservation FileReservationConfig `toml:"file_reservation"` // Auto file reservation via Agent Mail
	Memory          MemoryConfig          `toml:"memory"`           // CASS Memory (cm) integration
	Assign          AssignConfig          `toml:"assign"`           // Assignment strategy configuration
	Ensemble        EnsembleConfig        `toml:"ensemble"`         // Reasoning ensemble defaults
	Swarm           SwarmConfig           `toml:"swarm"`            // Weighted multi-project agent swarm
	SpawnPacing     SpawnPacingConfig     `toml:"spawn_pacing"`     // Spawn scheduler pacing configuration
	Safety          SafetyConfig          `toml:"safety"`           // Safety profile selection + defaults
	Preflight       PreflightConfig       `toml:"preflight"`        // Prompt preflight/lint configuration
	Redaction       RedactionConfig       `toml:"redaction"`        // Secrets/PII redaction configuration
	Privacy         PrivacyConfig         `toml:"privacy"`          // Privacy mode configuration
	Encryption      EncryptionConfig      `toml:"encryption"`       // Encryption at rest for artifacts
	Send            SendConfig            `toml:"send"`             // Send command defaults
	Prompts         PromptsConfig         `toml:"prompts"`          // Per-agent-type default prompts
	Retry           RetryConfig           `toml:"retry"`            // Unified retry policy configuration
	Routing         RoutingConfig         `toml:"routing"`          // Agent routing/scoring weights
	Coordinator     CoordinatorConfig     `toml:"coordinator"`      // Session coordinator (digests, auto-assign, conflict handling)

	// Runtime-only fields (populated by project config merging)
	ProjectDefaults map[string]int `toml:"-"`
}

// CoordinatorConfig holds session coordinator settings.
// Mirrors internal/coordinator.CoordinatorConfig for TOML deserialization
// without import cycles. BurntSushi/toml decodes time.Duration fields from
// either nanosecond integers or duration strings (e.g. "30s", "5m").
type CoordinatorConfig struct {
	// Monitoring
	PollInterval   time.Duration `toml:"poll_interval"`   // How often to poll agent status (default: 5s)
	DigestInterval time.Duration `toml:"digest_interval"` // How often to send digests (default: 5m)

	// Work assignment
	AutoAssign     bool    `toml:"auto_assign"`      // Automatically assign work to idle agents
	IdleThreshold  float64 `toml:"idle_threshold"`   // Seconds of inactivity before considering idle
	AssignOnlyIdle bool    `toml:"assign_only_idle"` // Only assign to truly idle agents

	// Conflict handling
	ConflictNotify    bool `toml:"conflict_notify"`    // Notify when conflicts detected
	ConflictNegotiate bool `toml:"conflict_negotiate"` // Attempt automatic conflict resolution

	// Agent Mail
	SendDigests bool   `toml:"send_digests"` // Send periodic digests to human
	HumanAgent  string `toml:"human_agent"`  // Agent name to send digests to (default: "Human")
	MailNudge   bool   `toml:"mail_nudge"`   // Prompt idle panes that have unread Agent Mail

	// Mail nudge tunables (GH#231). NudgeCooldownSeconds is the per-pane
	// minimum spacing between nudges (default 60; non-positive values fall
	// back to 60). NudgeMessage optionally overrides the built-in nudge
	// prompt verbatim.
	NudgeCooldownSeconds int    `toml:"nudge_cooldown_seconds"`
	NudgeMessage         string `toml:"nudge_message"`
}

// DefaultCoordinatorConfig mirrors coordinator.DefaultCoordinatorConfig and
// MUST be kept in sync with it. Drift here causes config.Load() defaults to
// disagree with the runtime defaults exposed by `ntm coordinator status`.
func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		PollInterval:         5 * time.Second,
		DigestInterval:       5 * time.Minute,
		AutoAssign:           false,
		IdleThreshold:        30.0,
		AssignOnlyIdle:       true,
		ConflictNotify:       true,
		ConflictNegotiate:    false,
		SendDigests:          false,
		HumanAgent:           "Human",
		MailNudge:            false,
		NudgeCooldownSeconds: 60,
		NudgeMessage:         "",
	}
}

// RetryConfig provides unified retry policy settings. Individual subsystems
// can override the global defaults via subsystem-specific sections.
type RetryConfig struct {
	MaxAttempts    int     `toml:"max_attempts"`     // Global default max retry attempts (default: 3)
	InitialDelayMs int     `toml:"initial_delay_ms"` // Initial delay between retries in ms (default: 1000)
	MaxDelayMs     int     `toml:"max_delay_ms"`     // Maximum delay cap in ms (default: 30000)
	BackoffFactor  float64 `toml:"backoff_factor"`   // Exponential backoff multiplier (default: 2.0)
	Jitter         bool    `toml:"jitter"`           // Add random jitter to delays (default: false; opt-in)

	// Subsystem-specific overrides (inherit global values if zero/empty).
	// Only the subsystems with a live retry loop exist; the former
	// scheduler/completion/db/assign overrides were removed in v1.26.0
	// (bd-ws6-config-truth-ienmd.2) because no such retry loops ship.
	Webhook RetryOverride `toml:"webhook"`
	Alerts  RetryOverride `toml:"alerts"`
	// AgentMail governs the Agent Mail MCP busy-retry loop
	// (internal/agentmail callToolWithBusyRetry). Defaults preserve the
	// historical hardcoded behavior: 3 retries with a 500ms initial backoff.
	AgentMail RetryOverride `toml:"agent_mail"`
}

// RetryOverride allows per-subsystem overrides of the global retry policy.
// Zero values inherit from the global RetryConfig.
type RetryOverride struct {
	MaxAttempts    int `toml:"max_attempts"`
	InitialDelayMs int `toml:"initial_delay_ms"`
}

type CommandHookEvent string

const (
	ConfigHookPreSpawn     CommandHookEvent = "pre-spawn"
	ConfigHookPostSpawn    CommandHookEvent = "post-spawn"
	ConfigHookPreSend      CommandHookEvent = "pre-send"
	ConfigHookPostSend     CommandHookEvent = "post-send"
	ConfigHookPreAdd       CommandHookEvent = "pre-add"
	ConfigHookPostAdd      CommandHookEvent = "post-add"
	ConfigHookPreCreate    CommandHookEvent = "pre-create"
	ConfigHookPostCreate   CommandHookEvent = "post-create"
	ConfigHookPreKill      CommandHookEvent = "pre-kill"
	ConfigHookPostKill     CommandHookEvent = "post-kill"
	ConfigHookPreShutdown  CommandHookEvent = "pre-shutdown"
	ConfigHookPostShutdown CommandHookEvent = "post-shutdown"
)

func allCommandHookEvents() []CommandHookEvent {
	return []CommandHookEvent{
		ConfigHookPreSpawn, ConfigHookPostSpawn,
		ConfigHookPreSend, ConfigHookPostSend,
		ConfigHookPreAdd, ConfigHookPostAdd,
		ConfigHookPreCreate, ConfigHookPostCreate,
		ConfigHookPreKill, ConfigHookPostKill,
		ConfigHookPreShutdown, ConfigHookPostShutdown,
	}
}

func isValidCommandHookEvent(event string) bool {
	for _, valid := range allCommandHookEvents() {
		if CommandHookEvent(event) == valid {
			return true
		}
	}
	return false
}

type CommandHookDuration time.Duration

func (d *CommandHookDuration) UnmarshalText(text []byte) error {
	duration, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = CommandHookDuration(duration)
	return nil
}

func (d CommandHookDuration) Duration() time.Duration {
	return time.Duration(d)
}

type CommandHookConfig struct {
	Event           CommandHookEvent    `toml:"event"`
	Command         string              `toml:"command"`
	Timeout         CommandHookDuration `toml:"timeout"`
	Enabled         *bool               `toml:"enabled"`
	WorkDir         string              `toml:"workdir"`
	Name            string              `toml:"name"`
	ContinueOnError bool                `toml:"continue_on_error"`
	Env             map[string]string   `toml:"env"`
}

func (h CommandHookConfig) timeoutOrDefault() time.Duration {
	if h.Timeout.Duration() <= 0 {
		return 30 * time.Second
	}
	return h.Timeout.Duration()
}

func (h CommandHookConfig) Validate() error {
	if strings.TrimSpace(h.Command) == "" {
		return fmt.Errorf("hook command cannot be empty")
	}
	if !isValidCommandHookEvent(string(h.Event)) {
		return fmt.Errorf("invalid hook event: %q (valid: %v)", h.Event, allCommandHookEvents())
	}
	timeout := h.timeoutOrDefault()
	if timeout < 0 {
		return fmt.Errorf("hook timeout cannot be negative")
	}
	if timeout > 10*time.Minute {
		return fmt.Errorf("hook timeout exceeds maximum (%v)", 10*time.Minute)
	}
	return nil
}

func ValidateCommandHooks(hooks []CommandHookConfig) error {
	for i, hook := range hooks {
		if err := hook.Validate(); err != nil {
			return fmt.Errorf("command_hooks[%d]: %w", i, err)
		}
	}
	return nil
}

// DefaultRetryConfig returns sensible retry defaults that match current
// behavior across the codebase.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    3,
		InitialDelayMs: 1000,
		MaxDelayMs:     30000,
		BackoffFactor:  2.0,
		// Jitter defaults to false: no shipped retry loop ever jittered while
		// this section was reader-less, so wiring the knob (WS6-wire,
		// bd-ws6-config-truth-ienmd.1) keeps default behavior identical and
		// makes jitter strictly opt-in.
		Jitter:    false,
		Webhook:   RetryOverride{MaxAttempts: 5},
		AgentMail: RetryOverride{MaxAttempts: 3, InitialDelayMs: 500},
	}
}

// RetryPolicyFor returns the effective retry settings for a named subsystem.
// Subsystem overrides take precedence; missing values inherit from global defaults.
func (c *RetryConfig) RetryPolicyFor(subsystem string) (maxAttempts int, initialDelayMs int) {
	maxAttempts = c.MaxAttempts
	initialDelayMs = c.InitialDelayMs
	if maxAttempts == 0 {
		maxAttempts = 3
	}
	if initialDelayMs == 0 {
		initialDelayMs = 1000
	}

	var override RetryOverride
	switch subsystem {
	case "webhook":
		override = c.Webhook
	case "alerts":
		override = c.Alerts
	case "agent_mail":
		override = c.AgentMail
	}

	if override.MaxAttempts > 0 {
		maxAttempts = override.MaxAttempts
	}
	if override.InitialDelayMs > 0 {
		initialDelayMs = override.InitialDelayMs
	}
	return
}

// RoutingConfig holds agent routing/scoring configuration.
// Mirrors internal/robot.RoutingConfig for TOML deserialization without import cycles.
type RoutingConfig struct {
	ContextWeight        float64 `toml:"context_weight"`
	StateWeight          float64 `toml:"state_weight"`
	RecencyWeight        float64 `toml:"recency_weight"`
	AffinityEnabled      bool    `toml:"affinity_enabled"`
	AffinityBonus        float64 `toml:"affinity_bonus"`
	ExcludeContextAbove  float64 `toml:"exclude_context_above"`
	ExcludeIfGenerating  bool    `toml:"exclude_if_generating"`
	ExcludeIfRateLimited bool    `toml:"exclude_if_rate_limited"`
	ExcludeIfErrorState  bool    `toml:"exclude_if_error"`
}

// DefaultRoutingConfig returns the canonical routing defaults used when
// config files omit the [routing] section entirely.
func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		ContextWeight:        0.4,
		StateWeight:          0.4,
		RecencyWeight:        0.2,
		AffinityEnabled:      false,
		AffinityBonus:        20.0,
		ExcludeContextAbove:  85.0,
		ExcludeIfGenerating:  true,
		ExcludeIfRateLimited: true,
		ExcludeIfErrorState:  true,
	}
}

// RobotConfig holds defaults for robot output behavior.
type RobotConfig struct {
	Verbosity string            `toml:"verbosity"` // terse, default, or debug
	Output    RobotOutputConfig `toml:"output"`    // Output format configuration
	Semantic  SemanticConfig    `toml:"semantic"`  // Opt-in semantic-progress signal (#199)
}

// SemanticConfig configures the optional dispatch-time pane work-token
// semantic-progress signal (#199). Every field defaults OFF/conservative so the
// default --robot-is-working / --robot-activity poll path and the default
// send/assign dispatch prompts are entirely unchanged when the feature is off.
//
// Safety: the semantic signal is advisory only. It never flips is_working from
// true to false and never produces a kill/reassign recommendation on its own.
type SemanticConfig struct {
	// Stamp, when true, makes `ntm send` / `ntm assign` inject a per-pane
	// `NTM-Pane: <session>/<window>.<pane>` commit-trailer instruction into the
	// dispatched marching orders (and, when a bead id is cleanly known, a
	// best-effort bead label carrying the same pane identity). Default false, so
	// prompts are never polluted unless explicitly opted in. No git hooks or git
	// config are ever installed; the trailer is a marching-orders instruction.
	Stamp bool `toml:"stamp"`
	// WindowMinutes is the default look-back window (in minutes) used by
	// `--robot-is-working --semantic` when `--semantic-window` is not supplied.
	// Conservative by default so a legitimately-slow pane that simply hasn't
	// committed recently is not flagged as a suspected wedge.
	WindowMinutes int `toml:"window_minutes"`
}

// DefaultSemanticConfig returns the conservative, fully-off semantic defaults.
func DefaultSemanticConfig() SemanticConfig {
	return SemanticConfig{
		Stamp:         false, // opt-in: never pollute dispatch prompts by default
		WindowMinutes: 30,    // conservative look-back for the stale-commit tell
	}
}

// RobotOutputConfig holds configuration for robot mode output format.
// The pretty/timestamps/compress keys were deprecated in v1.28.0
// (bd-6otuk): only robot.output.format has a runtime reader.
type RobotOutputConfig struct {
	Format string `toml:"format"` // Output format: "json" or "toon"
}

// DefaultRobotOutputConfig returns sensible robot output defaults.
func DefaultRobotOutputConfig() RobotOutputConfig {
	return RobotOutputConfig{
		Format: "json", // JSON for backwards compatibility
	}
}

// ValidateRobotOutputConfig validates the robot output configuration.
func ValidateRobotOutputConfig(cfg *RobotOutputConfig) error {
	// Empty format is valid - defaults to "json"
	if cfg.Format == "" {
		return nil
	}
	validFormats := map[string]bool{"json": true, "toon": true, "auto": true}
	if !validFormats[cfg.Format] {
		return fmt.Errorf("invalid robot output format %q: must be \"json\", \"toon\", or \"auto\"", cfg.Format)
	}
	return nil
}

// DefaultRobotConfig returns sensible robot defaults.
func DefaultRobotConfig() RobotConfig {
	return RobotConfig{
		Verbosity: "default",
		Output:    DefaultRobotOutputConfig(),
		Semantic:  DefaultSemanticConfig(),
	}
}

// CheckpointsConfig holds configuration for automatic checkpoints
type CheckpointsConfig struct {
	Enabled            bool `toml:"enabled"`              // Top-level toggle for auto-checkpoints
	BeforeBroadcast    bool `toml:"before_broadcast"`     // Auto-checkpoint before sending to all agents
	BeforeAddAgents    int  `toml:"before_add_agents"`    // Auto-checkpoint when adding >= N agents (0 = disabled)
	MaxAutoCheckpoints int  `toml:"max_auto_checkpoints"` // Max auto-checkpoints per session (rotation)
	ScrollbackLines    int  `toml:"scrollback_lines"`     // Lines of scrollback to capture
	IncludeGit         bool `toml:"include_git"`          // Capture git state in auto-checkpoints
}

// DefaultCheckpointsConfig returns sensible checkpoint defaults
func DefaultCheckpointsConfig() CheckpointsConfig {
	return CheckpointsConfig{
		Enabled:            true,
		BeforeBroadcast:    true,
		BeforeAddAgents:    3,  // Auto-checkpoint when adding 3+ agents
		MaxAutoCheckpoints: 10, // Keep last 10 auto-checkpoints per session
		ScrollbackLines:    500,
		IncludeGit:         true,
	}
}

// AlertsConfig holds configuration for the alert system
type AlertsConfig struct {
	Enabled                 bool    `toml:"enabled"`                   // Top-level toggle for alerts
	AgentStuckMinutes       int     `toml:"agent_stuck_minutes"`       // Minutes without output before alerting
	DiskLowThresholdGB      float64 `toml:"disk_low_threshold_gb"`     // Minimum free disk space (GB)
	DiskFullHorizonHours    float64 `toml:"disk_full_horizon_hours"`   // Alert when disk is projected full within this many hours (0 = disabled)
	MailBacklogThreshold    int     `toml:"mail_backlog_threshold"`    // Unread messages before alerting
	BeadStaleHours          int     `toml:"bead_stale_hours"`          // Hours before in-progress bead is stale
	ContextWarningThreshold float64 `toml:"context_warning_threshold"` // Context usage percentage that triggers a warning
	ResolvedPruneMinutes    int     `toml:"resolved_prune_minutes"`    // How long to keep resolved alerts
}

// DefaultAlertsConfig returns sensible alert defaults
func DefaultAlertsConfig() AlertsConfig {
	return AlertsConfig{
		Enabled:                 true,
		AgentStuckMinutes:       5,
		DiskLowThresholdGB:      5.0,
		DiskFullHorizonHours:    0,
		MailBacklogThreshold:    10,
		BeadStaleHours:          24,
		ContextWarningThreshold: 75.0,
		ResolvedPruneMinutes:    60,
	}
}

// ResilienceConfig holds configuration for agent auto-restart and recovery
type ResilienceConfig struct {
	AutoRestart         bool            `toml:"auto_restart"`           // Enable automatic agent restart on crash
	MaxRestarts         int             `toml:"max_restarts"`           // Max restarts per agent before giving up
	RestartDelaySeconds int             `toml:"restart_delay_seconds"`  // Seconds to wait before restarting
	HealthCheckSeconds  int             `toml:"health_check_seconds"`   // Seconds between health checks
	CrashThreshold      int             `toml:"crash_threshold"`        // Consecutive failures before restart (text-based fallback path)
	NotifyOnCrash       bool            `toml:"notify_on_crash"`        // Send notification when agent crashes
	NotifyOnMaxRestarts bool            `toml:"notify_on_max_restarts"` // Notify when max restarts exceeded
	RateLimit           RateLimitConfig `toml:"rate_limit"`             // Rate limit detection configuration
}

// RateLimitConfig holds configuration for rate limit detection
type RateLimitConfig struct {
	Detect bool `toml:"detect"` // Enable rate limit detection
	Notify bool `toml:"notify"` // Send notification on rate limit
	// AutoRotate is a co-located convenience for callers who think of the
	// "switch accounts when a rate limit hits" behaviour as a property of
	// rate-limit handling rather than rotation. When true, it is folded into
	// `Rotation.Enabled` AND `Rotation.AutoTrigger` at config load — both
	// are required for the runtime monitor in
	// internal/resilience/monitor.go:494 to actually call
	// `triggerRotationAssistance` when a rate limit fires. Setting both
	// `[resilience.rate_limit] auto_rotate` and `[rotation] auto_trigger`
	// is supported; the OR of the two wins.
	AutoRotate bool `toml:"auto_rotate"`
}

// DefaultResilienceConfig returns sensible resilience defaults
func DefaultResilienceConfig() ResilienceConfig {
	return ResilienceConfig{
		AutoRestart:         false, // Disabled by default, opt-in via --auto-restart
		MaxRestarts:         3,     // Stop after 3 restart attempts
		RestartDelaySeconds: 30,    // Wait 30 seconds before restarting
		HealthCheckSeconds:  10,    // Check health every 10 seconds
		CrashThreshold:      3,     // 3 consecutive text-based failures before restart
		NotifyOnCrash:       true,  // Notify on crash by default
		NotifyOnMaxRestarts: true,  // Notify when max restarts exceeded
		RateLimit: RateLimitConfig{
			Detect: true, // Detect rate limits by default
			Notify: true, // Notify on rate limit by default
		},
	}
}

// The [accounts] section (AccountsConfig/AccountEntry) was deprecated in
// v1.28.0 (bd-6otuk): the whole section was parsed, validated, and printed
// but consumed by nothing at runtime. See deprecatedKnobPrefixes in
// removed_knobs.go.

// RotationAccount represents a configured account for rotation.
// The priority key was deprecated in v1.28.0 (bd-6otuk): it was validated
// but never used for ordering — accounts are used in the order written.
type RotationAccount struct {
	Provider string `toml:"provider"` // claude, codex, gemini
	Email    string `toml:"email"`    // Account email
	Alias    string `toml:"alias"`    // Short name for display (optional)
}

// RotationThresholds defines when to trigger account rotation
type RotationThresholds struct {
	WarningPercent        int     `toml:"warning_percent"`          // Show warning at this quota %
	CriticalPercent       int     `toml:"critical_percent"`         // Consider limited at this %
	RestartIfTokensAbove  float64 `toml:"restart_if_tokens_above"`  // Restart if tokens exceed this
	RestartIfSessionHours int     `toml:"restart_if_session_hours"` // Restart after N hours
}

// RotationConfig holds account rotation configuration
type RotationConfig struct {
	Enabled            bool               `toml:"enabled"`             // Top-level toggle
	AutoOpenBrowser    bool               `toml:"auto_open_browser"`   // Auto-open browser for auth
	AutoTrigger        bool               `toml:"auto_trigger"`        // Show notification when rate limit detected
	AutoInitiate       bool               `toml:"auto_initiate"`       // Automatically start rotation (aggressive)
	ContinuationPrompt string             `toml:"continuation_prompt"` // Prompt template on rotation
	Accounts           []RotationAccount  `toml:"accounts"`            // Configured accounts per provider
	Thresholds         RotationThresholds `toml:"thresholds"`

	// UsagePercentThreshold enables the coordinator's context-rotation trigger
	// (bd-rpmg8). When > 0, the session coordinator tick compares each agent
	// pane's TRANSCRIPT-SOURCED context usage percentage (ground truth from the
	// agent CLI's own session transcript — never scrollback estimates; that
	// minimum-confidence gate is fixed, not configurable) against this value
	// and enqueues a pending context rotation through the existing
	// pending/confirm machinery when exceeded. 0 (the default) disables the
	// trigger entirely: no transcript probing, no behavior change.
	UsagePercentThreshold float64 `toml:"usage_percent_threshold"`
	// AutoConfirm executes a context rotation enqueued by the usage trigger
	// immediately via the existing confirm path instead of leaving it pending
	// for a manual `ntm rotate context confirm`.
	AutoConfirm bool `toml:"auto_confirm"`
}

// GetAccountsForProvider returns all accounts for a given provider in priority order
func (c *RotationConfig) GetAccountsForProvider(provider string) []RotationAccount {
	var accounts []RotationAccount
	for _, acc := range c.Accounts {
		if acc.Provider == provider {
			accounts = append(accounts, acc)
		}
	}
	return accounts
}

// SuggestNextAccount returns the next account to use (first non-current account)
func (c *RotationConfig) SuggestNextAccount(provider, currentEmail string) *RotationAccount {
	for i, acc := range c.Accounts {
		if acc.Provider == provider && acc.Email != currentEmail {
			return &c.Accounts[i]
		}
	}
	return nil
}

// DefaultRotationConfig returns the default rotation configuration
func DefaultRotationConfig() RotationConfig {
	return RotationConfig{
		Enabled:            false, // Opt-in by default
		AutoOpenBrowser:    false, // Don't auto-open browser
		ContinuationPrompt: "Continue where you left off. Previous context: {{.Context}}",
		Thresholds: RotationThresholds{
			WarningPercent:        80,
			CriticalPercent:       95,
			RestartIfTokensAbove:  100000,
			RestartIfSessionHours: 8,
		},
		UsagePercentThreshold: 0,     // Context-rotation trigger OFF by default (bd-rpmg8)
		AutoConfirm:           false, // Enqueued rotations wait for manual confirm by default
	}
}

// ValidateRotationConfig validates account rotation configuration.
func ValidateRotationConfig(cfg *RotationConfig) error {
	if cfg.UsagePercentThreshold < 0 || cfg.UsagePercentThreshold > 100 {
		return fmt.Errorf("usage_percent_threshold: must be between 0 and 100, got %g", cfg.UsagePercentThreshold)
	}
	if cfg.Thresholds.WarningPercent < 0 || cfg.Thresholds.WarningPercent > 100 {
		return fmt.Errorf("thresholds.warning_percent: must be between 0 and 100, got %d", cfg.Thresholds.WarningPercent)
	}
	if cfg.Thresholds.CriticalPercent < 0 || cfg.Thresholds.CriticalPercent > 100 {
		return fmt.Errorf("thresholds.critical_percent: must be between 0 and 100, got %d", cfg.Thresholds.CriticalPercent)
	}
	if cfg.Thresholds.WarningPercent > cfg.Thresholds.CriticalPercent {
		return fmt.Errorf("thresholds.warning_percent (%d) must be <= thresholds.critical_percent (%d)", cfg.Thresholds.WarningPercent, cfg.Thresholds.CriticalPercent)
	}
	if cfg.Thresholds.RestartIfTokensAbove < 0 {
		return fmt.Errorf("thresholds.restart_if_tokens_above: must be non-negative, got %.0f", cfg.Thresholds.RestartIfTokensAbove)
	}
	if cfg.Thresholds.RestartIfSessionHours < 0 {
		return fmt.Errorf("thresholds.restart_if_session_hours: must be non-negative, got %d", cfg.Thresholds.RestartIfSessionHours)
	}
	for i, account := range cfg.Accounts {
		switch account.Provider {
		case "claude", "codex", "gemini", "antigravity":
		default:
			return fmt.Errorf("accounts[%d].provider: must be claude, codex, gemini, or antigravity, got %q", i, account.Provider)
		}
		if strings.TrimSpace(account.Email) == "" {
			return fmt.Errorf("accounts[%d].email: must not be empty", i)
		}
	}
	return nil
}

// CASSConfig holds configuration for CASS (Coding Agent Session Search) integration
// The cass.show_install_hints key and the [cass.duplicates]/[cass.search]/
// [cass.tui] tables were deprecated in v1.28.0 (bd-6otuk): parsed and echoed
// but consumed by nothing at runtime (duplicate checking reads CLI flags).
type CASSConfig struct {
	Enabled    bool   `toml:"enabled"`     // Top-level switch - disable all CASS features
	BinaryPath string `toml:"binary_path"` // Path to cass binary (auto-detect from PATH if empty)
	Timeout    int    `toml:"timeout"`     // Timeout for CASS operations (seconds)

	Context CASSContextConfig `toml:"context"` // Context injection settings
}

// CASSContextConfig holds settings for automatic context injection
type CASSContextConfig struct {
	Enabled            bool    `toml:"enabled"`               // Auto-inject context when spawning
	MaxSessions        int     `toml:"max_sessions"`          // Max past sessions to include (inject_limit)
	LookbackDays       int     `toml:"lookback_days"`         // How far back to search (max_age_days)
	MaxTokens          int     `toml:"max_tokens"`            // Token budget for context (max_inject_tokens)
	MinRelevance       float64 `toml:"min_relevance"`         // Minimum relevance score to include (0.0-1.0)
	SkipIfContextAbove float64 `toml:"skip_if_context_above"` // Skip injection if context usage exceeds this % (0-100)
	PreferSameProject  bool    `toml:"prefer_same_project"`   // Prefer results from same project
}

// DefaultCASSConfig returns the default CASS configuration
func DefaultCASSConfig() CASSConfig {
	return CASSConfig{
		Enabled:    true,
		BinaryPath: "", // Auto-detect from PATH
		Timeout:    30,

		Context: CASSContextConfig{
			Enabled:            true,
			MaxSessions:        3,
			LookbackDays:       30,
			MaxTokens:          2000,
			MinRelevance:       0.5, // Only include results with >= 50% relevance
			SkipIfContextAbove: 80,  // Skip injection if context usage > 80%
			PreferSameProject:  true,
		},
	}
}

// ValidateGeminiSetupConfig validates Gemini post-spawn setup settings.
func ValidateGeminiSetupConfig(cfg *GeminiSetupConfig) error {
	if cfg.ReadyTimeoutSeconds < 0 {
		return fmt.Errorf("ready_timeout_seconds: must be non-negative, got %d", cfg.ReadyTimeoutSeconds)
	}
	if cfg.ModelSelectTimeoutSeconds < 0 {
		return fmt.Errorf("model_select_timeout_seconds: must be non-negative, got %d", cfg.ModelSelectTimeoutSeconds)
	}
	return nil
}

// AgentConfig defines the commands for each agent type
type AgentConfig struct {
	Claude      string            `toml:"claude"`
	Codex       string            `toml:"codex"`
	Gemini      string            `toml:"gemini"`
	Antigravity string            `toml:"antigravity"` // Antigravity (agy) launch command — successor to the Gemini CLI
	Grok        string            `toml:"grok"`        // Official xAI Grok Build launch command
	Ollama      string            `toml:"ollama"`
	Cursor      string            `toml:"cursor"`
	Windsurf    string            `toml:"windsurf"`
	Aider       string            `toml:"aider"`
	Opencode    string            `toml:"oc"`      // Opencode (https://opencode.ai) launch command — see ntm#116
	Plugins     map[string]string `toml:"plugins"` // Custom agent commands keyed by type

	// ClaudeIsolateCredentials opts Claude panes into per-pane
	// CLAUDE_CONFIG_DIR isolation at spawn (GH#237). Claude Code rewrites the
	// shared ~/.claude/.credentials.json on every OAuth refresh, so N panes on
	// one subscription invalidate each other's refresh token and 401 in
	// cascade — taking the operator's own session down with them. Each pane
	// instead gets a config dir that links everything except the rotating
	// credential.
	ClaudeIsolateCredentials bool `toml:"claude_isolate_credentials"`

	// ClaudeTokenFile points at a file holding a non-rotating setup token
	// minted with `claude setup-token`. Isolation alone leaves panes with no
	// credential; this supplies the static one they all read. Kept OUTSIDE
	// the config dir on purpose so it is never linked into a pane.
	ClaudeTokenFile string `toml:"claude_token_file"`
}

// ContextConfig holds options for context-pack composition.
type ContextConfig struct {
	MSSkills bool `toml:"ms_skills"` // Include Meta Skill suggestions in context packs
}

// DefaultContextConfig returns sensible defaults for context-pack options.
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		MSSkills: false, // Disabled by default; opt-in only
	}
}

// ContextRotationConfig holds configuration for automatic context window rotation
type ContextRotationConfig struct {
	Enabled              bool                     `toml:"enabled"`                // Top-level toggle for context rotation
	WarningThreshold     float64                  `toml:"warning_threshold"`      // 0.0-1.0, warn when context usage exceeds this
	RotateThreshold      float64                  `toml:"rotate_threshold"`       // 0.0-1.0, rotate agent when usage exceeds this
	SummaryMaxTokens     int                      `toml:"summary_max_tokens"`     // Max tokens for handoff summary
	MinSessionAgeSec     int                      `toml:"min_session_age_sec"`    // Don't rotate agents younger than this
	TryCompactFirst      bool                     `toml:"try_compact_first"`      // Try to compact before rotating
	RequireConfirm       bool                     `toml:"require_confirm"`        // Require user confirmation before rotating
	ConfirmTimeoutSec    int                      `toml:"confirm_timeout_sec"`    // Seconds to wait for confirmation (0 = no auto-rotate)
	DefaultConfirmAction string                   `toml:"default_confirm_action"` // Action if timeout expires: "rotate", "ignore", "compact"
	Recovery             CompactionRecoveryConfig `toml:"recovery"`               // Compaction-recovery prompt behaviour (issue #113)
}

// CompactionRecoveryConfig holds configuration for the compaction recovery
// surface — i.e. the prompt that gets re-sent to a pane after a context
// rotation/compaction so the agent re-reads its setup. The runtime side
// of this lives in internal/status/recovery.go (RecoveryConfig); this
// type is the TOML bridge that #113 was asking for.
//
// Mapping back to the runtime fields:
//
//	cooldown_seconds        -> RecoveryConfig.Cooldown (time.Duration)
//	prompt                  -> RecoveryConfig.Prompt
//	max_recoveries_per_pane -> RecoveryConfig.MaxRecoveries
//	include_bead_context    -> RecoveryConfig.IncludeBeadContext
//	enabled                 -> gate before invoking recovery at all
type CompactionRecoveryConfig struct {
	Enabled              bool   `toml:"enabled"`                 // Top-level toggle for compaction recovery prompts
	CooldownSeconds      int    `toml:"cooldown_seconds"`        // Minimum seconds between recovery prompts per pane (0 = engine default)
	IncludeBeadContext   bool   `toml:"include_bead_context"`    // Include current Beads task context in the recovery prompt
	MaxRecoveriesPerPane int    `toml:"max_recoveries_per_pane"` // Cap on recovery prompts per pane before giving up (0 = engine default)
	Prompt               string `toml:"prompt"`                  // Override the recovery prompt sent on rotation
}

// DefaultCompactionRecoveryConfig returns sensible defaults for the recovery
// integration. The numeric zero values are treated by the runtime as "use the
// engine's hardcoded fallback", so emitting them here keeps DefaultLayer()
// minimal while still exposing the surface in configuration tooling.
func DefaultCompactionRecoveryConfig() CompactionRecoveryConfig {
	return CompactionRecoveryConfig{
		Enabled:              true,
		CooldownSeconds:      0,
		IncludeBeadContext:   true,
		MaxRecoveriesPerPane: 0,
		Prompt:               "",
	}
}

// DefaultContextRotationConfig returns sensible defaults for context rotation
func DefaultContextRotationConfig() ContextRotationConfig {
	return ContextRotationConfig{
		Enabled:              true,
		WarningThreshold:     0.80,     // Warn at 80%
		RotateThreshold:      0.95,     // Rotate at 95%
		SummaryMaxTokens:     2000,     // 2000 tokens for handoff summary
		MinSessionAgeSec:     300,      // 5 minutes minimum session age
		TryCompactFirst:      true,     // Try compaction before rotation
		RequireConfirm:       false,    // Don't require confirmation by default
		ConfirmTimeoutSec:    60,       // 60 seconds timeout for confirmation
		DefaultConfirmAction: "rotate", // Auto-rotate on timeout
		Recovery:             DefaultCompactionRecoveryConfig(),
	}
}

// ValidateContextRotationConfig validates the context rotation configuration
func ValidateContextRotationConfig(cfg *ContextRotationConfig) error {
	if cfg.WarningThreshold < 0.0 || cfg.WarningThreshold > 1.0 {
		return fmt.Errorf("warning_threshold must be between 0.0 and 1.0, got %f", cfg.WarningThreshold)
	}
	if cfg.RotateThreshold < 0.0 || cfg.RotateThreshold > 1.0 {
		return fmt.Errorf("rotate_threshold must be between 0.0 and 1.0, got %f", cfg.RotateThreshold)
	}
	if cfg.WarningThreshold >= cfg.RotateThreshold {
		return fmt.Errorf("warning_threshold (%f) must be less than rotate_threshold (%f)",
			cfg.WarningThreshold, cfg.RotateThreshold)
	}
	if cfg.SummaryMaxTokens < 500 || cfg.SummaryMaxTokens > 10000 {
		return fmt.Errorf("summary_max_tokens must be between 500 and 10000, got %d", cfg.SummaryMaxTokens)
	}
	if cfg.MinSessionAgeSec < 0 {
		return fmt.Errorf("min_session_age_sec must be non-negative, got %d", cfg.MinSessionAgeSec)
	}
	if cfg.ConfirmTimeoutSec < 0 {
		return fmt.Errorf("confirm_timeout_sec must be non-negative, got %d", cfg.ConfirmTimeoutSec)
	}
	validActions := map[string]bool{"rotate": true, "ignore": true, "compact": true, "": true}
	if !validActions[cfg.DefaultConfirmAction] {
		return fmt.Errorf("default_confirm_action must be 'rotate', 'ignore', or 'compact', got %q", cfg.DefaultConfirmAction)
	}
	return nil
}

// ValidateEnsembleConfig validates ensemble defaults in config.toml.
func ValidateEnsembleConfig(cfg *EnsembleConfig) error {
	if cfg == nil {
		return nil
	}

	if cfg.Assignment != "" {
		switch strings.ToLower(strings.TrimSpace(cfg.Assignment)) {
		case "round-robin", "affinity", "category", "explicit":
			// ok
		default:
			return fmt.Errorf("assignment must be one of round-robin, affinity, category, explicit; got %q", cfg.Assignment)
		}
	}

	if cfg.ModeTierDefault != "" {
		switch strings.ToLower(strings.TrimSpace(cfg.ModeTierDefault)) {
		case "core", "advanced", "experimental":
			// ok
		default:
			return fmt.Errorf("mode_tier_default must be core, advanced, or experimental; got %q", cfg.ModeTierDefault)
		}
	}

	if cfg.Synthesis.Strategy != "" {
		if err := validateSynthesisStrategy(cfg.Synthesis.Strategy); err != nil {
			return fmt.Errorf("synthesis.strategy: %w", err)
		}
	}

	if cfg.Synthesis.MinConfidence < 0 || cfg.Synthesis.MinConfidence > 1 {
		return fmt.Errorf("synthesis.min_confidence must be between 0.0 and 1.0, got %f", cfg.Synthesis.MinConfidence)
	}
	if cfg.Synthesis.MaxFindings < 0 {
		return fmt.Errorf("synthesis.max_findings must be non-negative, got %d", cfg.Synthesis.MaxFindings)
	}

	if cfg.Budget.PerAgent < 0 || cfg.Budget.Total < 0 || cfg.Budget.Synthesis < 0 || cfg.Budget.ContextPack < 0 {
		return fmt.Errorf("budget values must be non-negative")
	}
	if cfg.Budget.PerAgent > 0 && cfg.Budget.Total > 0 && cfg.Budget.PerAgent > cfg.Budget.Total {
		return fmt.Errorf("budget.per_agent (%d) must be <= budget.total (%d)", cfg.Budget.PerAgent, cfg.Budget.Total)
	}

	if cfg.Cache.TTLMinutes < 0 {
		return fmt.Errorf("cache.ttl_minutes must be non-negative, got %d", cfg.Cache.TTLMinutes)
	}
	if cfg.Cache.MaxEntries < 0 {
		return fmt.Errorf("cache.max_entries must be non-negative, got %d", cfg.Cache.MaxEntries)
	}

	if cfg.EarlyStop.MinAgents < 0 {
		return fmt.Errorf("early_stop.min_agents must be non-negative, got %d", cfg.EarlyStop.MinAgents)
	}
	if cfg.EarlyStop.WindowSize < 0 {
		return fmt.Errorf("early_stop.window_size must be non-negative, got %d", cfg.EarlyStop.WindowSize)
	}
	if cfg.EarlyStop.FindingsThreshold < 0 || cfg.EarlyStop.FindingsThreshold > 1 {
		return fmt.Errorf("early_stop.findings_threshold must be between 0.0 and 1.0, got %f", cfg.EarlyStop.FindingsThreshold)
	}
	if cfg.EarlyStop.SimilarityThreshold < 0 || cfg.EarlyStop.SimilarityThreshold > 1 {
		return fmt.Errorf("early_stop.similarity_threshold must be between 0.0 and 1.0, got %f", cfg.EarlyStop.SimilarityThreshold)
	}

	return nil
}

// GeminiSetupConfig holds configuration for Gemini post-spawn setup.
type GeminiSetupConfig struct {
	// AutoSelectProModel automatically selects Pro model after Gemini spawns.
	// When true, NTM sends /model, Down, Enter to select Pro mode.
	AutoSelectProModel bool `toml:"auto_select_pro_model"`

	// ReadyTimeoutSeconds is how long to wait for Gemini CLI to be ready.
	ReadyTimeoutSeconds int `toml:"ready_timeout_seconds"`

	// ModelSelectTimeoutSeconds is how long to wait for model menu.
	ModelSelectTimeoutSeconds int `toml:"model_select_timeout_seconds"`

	// Verbose enables debug output during setup.
	Verbose bool `toml:"verbose"`
}

// DefaultGeminiSetupConfig returns sensible defaults for Gemini setup.
func DefaultGeminiSetupConfig() GeminiSetupConfig {
	return GeminiSetupConfig{
		AutoSelectProModel:        true,  // Select Pro by default
		ReadyTimeoutSeconds:       60,    // 60 seconds to wait for ready (increased from 30 for slower networks)
		ModelSelectTimeoutSeconds: 20,    // 20 seconds for model menu (increased from 10 for reliability)
		Verbose:                   false, // Quiet by default
	}
}

// SessionRecoveryConfig holds configuration for smart session recovery context injection.
// This is used to provide agents with context when they start a new session.
type SessionRecoveryConfig struct {
	Enabled             bool `toml:"enabled"`               // Top-level toggle for recovery context injection
	IncludeAgentMail    bool `toml:"include_agent_mail"`    // Include recent Agent Mail messages
	IncludeCMMemories   bool `toml:"include_cm_memories"`   // Include CM procedural memories
	IncludeBeadsContext bool `toml:"include_beads_context"` // Include BV task status
	MaxRecoveryTokens   int  `toml:"max_recovery_tokens"`   // Cap recovery context size
	AutoInjectOnSpawn   bool `toml:"auto_inject_on_spawn"`  // Send automatically on spawn
	MaxCMRules          int  `toml:"max_cm_rules"`          // Max CM rules to include (default: 10)
	MaxCMSnippets       int  `toml:"max_cm_snippets"`       // Max CM history snippets (default: 3)
	TimeoutSeconds      int  `toml:"timeout_seconds"`       // Window for gathering recovery sources before degrading to partial recovery
}

// DefaultRecoveryTimeout bounds how long spawn waits for recovery sources
// (beads, Agent Mail, CM memories) before continuing without them.
const DefaultRecoveryTimeout = 5 * time.Second

// GetTimeout returns the recovery-gathering window as a duration, falling back
// to DefaultRecoveryTimeout when timeout_seconds is unset or non-positive.
func (c SessionRecoveryConfig) GetTimeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return DefaultRecoveryTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// DefaultSessionRecoveryConfig returns sensible defaults for session recovery.
func DefaultSessionRecoveryConfig() SessionRecoveryConfig {
	return SessionRecoveryConfig{
		Enabled:             true, // Enabled by default
		IncludeAgentMail:    true, // Include Agent Mail messages
		IncludeCMMemories:   true, // Include CM procedural memories
		IncludeBeadsContext: true, // Include bead/task context
		MaxRecoveryTokens:   2000, // Token budget for recovery context
		AutoInjectOnSpawn:   true, // Inject on spawn by default
		MaxCMRules:          10,   // Max CM rules to include
		MaxCMSnippets:       3,    // Max CM history snippets
		TimeoutSeconds:      5,    // Gather recovery sources for at most 5s, then degrade
	}
}

// CleanupConfig holds configuration for automatic temp file cleanup.
// NTM can accumulate temp files in /tmp from tests, atomic writes, and
// other operations. This config controls automatic cleanup on startup.
type CleanupConfig struct {
	AutoCleanOnStartup bool `toml:"auto_clean_on_startup"` // Clean stale temp files on startup
	MaxAgeHours        int  `toml:"max_age_hours"`         // Hours before a temp file is considered stale
	Verbose            bool `toml:"verbose"`               // Log cleanup operations
}

// DefaultCleanupConfig returns sensible defaults for temp file cleanup.
func DefaultCleanupConfig() CleanupConfig {
	return CleanupConfig{
		AutoCleanOnStartup: true, // Clean old temp files on startup
		MaxAgeHours:        24,   // Consider files older than 24h as stale
		Verbose:            false,
	}
}

// FileReservationConfig holds configuration for automatic file reservation via Agent Mail.
// When enabled, NTM monitors pane output for file edits and automatically reserves
// those files in Agent Mail, preventing other agents from conflicting edits.
type FileReservationConfig struct {
	Enabled               bool `toml:"enabled"`                   // Top-level toggle for auto file reservation
	AutoReserve           bool `toml:"auto_reserve"`              // Automatically reserve on edit detection
	AutoReleaseIdleMin    int  `toml:"auto_release_idle_minutes"` // Release reservations after this idle time
	NotifyOnConflict      bool `toml:"notify_on_conflict"`        // Show notification when conflict detected
	ExtendOnActivity      bool `toml:"extend_on_activity"`        // Extend TTL while agent is actively editing
	DefaultTTLMin         int  `toml:"default_ttl_minutes"`       // Default TTL for reservations
	PollIntervalSec       int  `toml:"poll_interval_seconds"`     // How often to poll pane output for edits
	CaptureLinesForDetect int  `toml:"capture_lines"`             // Lines of output to scan for file edits
	Debug                 bool `toml:"debug"`                     // Enable debug logging
}

// DefaultFileReservationConfig returns sensible defaults for file reservation.
func DefaultFileReservationConfig() FileReservationConfig {
	return FileReservationConfig{
		Enabled:               true,  // Enabled by default (when Agent Mail is available)
		AutoReserve:           true,  // Automatically reserve detected edits
		AutoReleaseIdleMin:    10,    // Release after 10 minutes of inactivity
		NotifyOnConflict:      true,  // Notify user on conflicts
		ExtendOnActivity:      true,  // Extend TTL while actively editing
		DefaultTTLMin:         15,    // 15-minute reservation TTL
		PollIntervalSec:       10,    // Poll every 10 seconds
		CaptureLinesForDetect: 100,   // Scan last 100 lines for file patterns
		Debug:                 false, // Debug logging disabled by default
	}
}

// ValidateFileReservationConfig validates the file reservation configuration.
func ValidateFileReservationConfig(cfg *FileReservationConfig) error {
	if cfg.AutoReleaseIdleMin < 1 && cfg.AutoReleaseIdleMin != 0 {
		return fmt.Errorf("auto_release_idle_minutes must be 0 (disabled) or at least 1, got %d", cfg.AutoReleaseIdleMin)
	}
	if cfg.DefaultTTLMin < 1 {
		return fmt.Errorf("default_ttl_minutes must be at least 1, got %d", cfg.DefaultTTLMin)
	}
	if cfg.PollIntervalSec < 1 {
		return fmt.Errorf("poll_interval_seconds must be at least 1, got %d", cfg.PollIntervalSec)
	}
	if cfg.CaptureLinesForDetect < 10 {
		return fmt.Errorf("capture_lines must be at least 10, got %d", cfg.CaptureLinesForDetect)
	}
	return nil
}

// MemoryConfig holds configuration for CASS Memory (cm) integration.
// When enabled, NTM can query the memory system for relevant context
// before starting tasks and include learned rules in session recovery.
type MemoryConfig struct {
	Enabled             bool `toml:"enabled"`               // Top-level toggle for memory integration
	IncludeInRecovery   bool `toml:"include_in_recovery"`   // Include memory context in session recovery
	MaxRules            int  `toml:"max_rules"`             // Maximum number of rules to inject
	QueryTimeoutSeconds int  `toml:"query_timeout_seconds"` // Timeout for cm command

	// Per-task rule injection at send time (--with-memory, bd-3j6hm).
	// send_injection makes robot sends inject rules by default (the
	// --with-memory flag enables it per call regardless); send_max_rules and
	// send_budget_tokens bound the injected block. They are send-scoped so
	// max_rules keeps governing recovery/spawn context independently.
	SendInjection    bool `toml:"send_injection"`     // Inject rules on robot sends by default
	SendMaxRules     int  `toml:"send_max_rules"`     // Max rules injected per send
	SendBudgetTokens int  `toml:"send_budget_tokens"` // Token budget for the injected block
}

// DefaultMemoryConfig returns sensible defaults for memory integration.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Enabled:             true,  // Enabled by default (when cm is available)
		IncludeInRecovery:   true,  // Include in session recovery context
		MaxRules:            10,    // Cap number of rules to inject
		QueryTimeoutSeconds: 5,     // 5 second timeout for cm queries
		SendInjection:       false, // Opt-in: --with-memory drives per-call injection
		SendMaxRules:        5,     // Top-N rules injected per send
		SendBudgetTokens:    1500,  // Token budget for the injected rules block
	}
}

// ValidateMemoryConfig validates the memory configuration.
func ValidateMemoryConfig(cfg *MemoryConfig) error {
	if cfg.MaxRules < 0 {
		return fmt.Errorf("max_rules must be non-negative, got %d", cfg.MaxRules)
	}
	if cfg.QueryTimeoutSeconds < 1 {
		return fmt.Errorf("query_timeout_seconds must be at least 1, got %d", cfg.QueryTimeoutSeconds)
	}
	if cfg.SendMaxRules < 0 {
		return fmt.Errorf("send_max_rules must be non-negative, got %d", cfg.SendMaxRules)
	}
	if cfg.SendBudgetTokens < 0 {
		return fmt.Errorf("send_budget_tokens must be non-negative, got %d", cfg.SendBudgetTokens)
	}
	return nil
}

// ValidateDCGConfig validates the DCG integration configuration.
func ValidateDCGConfig(cfg *DCGConfig) error {
	if cfg == nil {
		return nil
	}

	if cfg.BinaryPath != "" {
		path := ExpandHome(cfg.BinaryPath)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("binary_path: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("binary_path: %q is a directory", path)
		}
	}

	if cfg.AuditLog != "" {
		auditPath := ExpandHome(cfg.AuditLog)
		dir := filepath.Dir(auditPath)
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("audit_log: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("audit_log: %q is not a directory", dir)
		}
		if !dirWritable(info) {
			return fmt.Errorf("audit_log: directory not writable: %s", dir)
		}
	}

	return nil
}

func dirWritable(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	mode := info.Mode().Perm()
	return mode&0200 != 0 || mode&0020 != 0 || mode&0002 != 0
}

// PaletteCmd represents a command in the palette
type PaletteCmd struct {
	Key      string   `toml:"key"`
	Label    string   `toml:"label"`
	Prompt   string   `toml:"prompt"`
	Category string   `toml:"category,omitempty"`
	Tags     []string `toml:"tags,omitempty"`
}

// PaletteState stores user palette preferences (favorites/pins).
// This is persisted in config files under [palette_state].
type PaletteState struct {
	Pinned    []string `toml:"pinned,omitempty"`
	Favorites []string `toml:"favorites,omitempty"`
}

// TmuxConfig holds tmux-specific settings
type TmuxConfig struct {
	DefaultPanes    int `toml:"default_panes"`
	PaneInitDelayMs int `toml:"pane_init_delay_ms"` // Delay before sending keys to new panes
	HistoryLimit    int `toml:"history_limit"`      // Scrollback buffer lines per pane (default 50000)
}

// AgentMailConfig holds Agent Mail server settings
type AgentMailConfig struct {
	Enabled      bool   `toml:"enabled"`       // Top-level toggle
	URL          string `toml:"url"`           // Server endpoint
	Token        string `toml:"token"`         // Bearer token
	AutoRegister bool   `toml:"auto_register"` // Auto-register sessions as agents
	// SupervisorEnabled controls whether ntm spawns and manages the
	// `am serve-http` daemon under its supervisor. Default false keeps
	// Agent Mail ownership external: ntm may use the configured MCP URL,
	// but it will not start, stop, restart, or compete with a user-owned
	// `am` process on port 8765. Set to true only when you explicitly want
	// ntm to own the Agent Mail daemon lifecycle for a session.
	SupervisorEnabled *bool `toml:"supervisor_enabled,omitempty"`
}

// SupervisorEnabledOrDefault returns the effective value of
// SupervisorEnabled with the documented default-false semantics applied.
func (a AgentMailConfig) SupervisorEnabledOrDefault() bool {
	if a.SupervisorEnabled == nil {
		return false
	}
	return *a.SupervisorEnabled
}

// IntegrationsConfig holds external tool integration settings.
type IntegrationsConfig struct {
	DCG           DCGConfig           `toml:"dcg"`
	CAAM          CAAMConfig          `toml:"caam"`           // CAAM (Coding Agent Account Manager) integration
	RCH           RCHConfig           `toml:"rch"`            // RCH (Remote Compilation Helper) integration
	ProcessTriage ProcessTriageConfig `toml:"process_triage"` // pt (process_triage) Bayesian health classification
	Rano          RanoConfig          `toml:"rano"`           // rano network observer for per-agent API tracking
	XF            XFConfig            `toml:"xf"`             // xf (X/Twitter archive search) integration
	BV            BVConfig            `toml:"bv"`             // bv (beads_viewer) graph analysis integration
}

// BVConfig holds configuration for the bv (beads_viewer) integration.
// bv resolves from PATH; there is no enable switch because every bv surface
// already degrades gracefully when the binary is missing.
type BVConfig struct {
	// TimeoutSeconds bounds each bv subprocess ntm launches (--robot-insights,
	// --robot-triage, --robot-plan, --check-drift, ...). On medium repos the
	// cached robot-insights path can exceed the historical hard-coded 30s and
	// silently degrade the dependency-cycle check (GH#253). The NTM_BV_TIMEOUT
	// environment variable (whole seconds) overrides this value.
	TimeoutSeconds int `toml:"timeout_seconds"`
}

// DefaultBVConfig returns defaults for the bv integration. 30 seconds
// preserves the historical hard-coded timeout.
func DefaultBVConfig() BVConfig {
	return BVConfig{
		TimeoutSeconds: 30,
	}
}

// ValidateBVConfig validates bv integration configuration.
func ValidateBVConfig(cfg *BVConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive, got %d", cfg.TimeoutSeconds)
	}
	return nil
}

// DCGConfig holds configuration for the DCG (destructive_commit_guard) integration.
type DCGConfig struct {
	Enabled         bool     `toml:"enabled"`
	BinaryPath      string   `toml:"binary_path"`
	CustomBlocklist []string `toml:"custom_blocklist"` // Legacy: configure modern dcg packs directly
	CustomWhitelist []string `toml:"custom_whitelist"` // Legacy: configure modern dcg allowlists directly
	AuditLog        string   `toml:"audit_log"`        // Legacy: configure modern dcg logging directly
	AllowOverride   bool     `toml:"allow_override"`
}

// AssignConfig holds configuration for the ntm assign command
type AssignConfig struct {
	Strategy string `toml:"strategy"` // Default strategy: balanced, speed, quality, dependency, round-robin
	// PromptTemplate is an inline project/user-level default for the bulk-assign
	// dispatch prompt. When set (and no per-invocation --bulk-assign-template file
	// is supplied), it overrides the built-in template. Placeholders follow the
	// bulk-assign convention: {bead_id} {bead_title} {bead_type} {bead_deps}
	// {session} {pane}. Empty means "use the built-in default".
	PromptTemplate string `toml:"prompt_template"`
	// PromptTemplateFile points at a file holding the default bulk-assign dispatch
	// prompt. It takes precedence over PromptTemplate when both are set, and is
	// itself overridden by a per-invocation --bulk-assign-template path.
	PromptTemplateFile string `toml:"prompt_template_file"`
	// OperatorGatedLabels lists additional bead labels — merged with the
	// built-in operator-gate vocabulary (operator-gated, operator-action,
	// needs-operator, human-gated, human-input, business-input,
	// blocked-on-operator, blocked-on-ivan) — that mark work as requiring a
	// human decision.
	// Automated assignment (assign --auto/--watch, coordinator auto-assign)
	// never dispatches beads carrying any of these labels. Matching is
	// case-insensitive; extras extend the defaults and cannot remove them (#223).
	OperatorGatedLabels []string `toml:"operator_gated_labels"`
	// IdleThreshold sets how long an assigned agent may go without pane output
	// before `assign --watch` declares the assignment stalled/failed and frees
	// the pane for new work (GH#238). Duration string, e.g. "15m", "300s".
	// Empty means DefaultAssignIdleThreshold. Keep this comfortably above the
	// longest silent stretch your agents hit while waiting on external
	// subprocesses (review CLIs, long builds, remote verification) — a
	// too-small value falsely fails in-progress work and then injects the
	// NEXT bead's prompt into the still-working agent mid-task.
	IdleThreshold string `toml:"idle_threshold"`
}

// DefaultAssignIdleThreshold is the default watch-loop inactivity window
// before an in-flight assignment is stamped failed and its pane freed.
// 15 minutes, not the completion detector's historical 120s: agents routinely
// go 5–10+ minutes with zero pane output while an external subprocess runs
// (multi-model review CLIs, long builds, SSH remote verification), and a
// false failure is far worse than a late one — it corrupts the pane by
// injecting a second bead's prompt mid-task (GH#238).
const DefaultAssignIdleThreshold = 15 * time.Minute

// IdleThresholdDuration returns the configured idle threshold, falling back
// to DefaultAssignIdleThreshold when unset or invalid (a warning-worthy but
// never fatal condition: watch mode should not fail to start over a typo'd
// duration; validation reports it separately).
func (c AssignConfig) IdleThresholdDuration() time.Duration {
	raw := strings.TrimSpace(c.IdleThreshold)
	if raw == "" {
		return DefaultAssignIdleThreshold
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultAssignIdleThreshold
	}
	return d
}

// ValidAssignStrategies are the recognized assignment strategies
var ValidAssignStrategies = []string{"balanced", "speed", "quality", "dependency", "round-robin"}

// IsValidStrategy returns true if the strategy is recognized
func IsValidStrategy(strategy string) bool {
	for _, s := range ValidAssignStrategies {
		if s == strategy {
			return true
		}
	}
	return false
}

// ValidateAssignConfig validates the [assign] section.
func ValidateAssignConfig(cfg *AssignConfig) error {
	if cfg == nil {
		return nil
	}
	if raw := strings.TrimSpace(cfg.IdleThreshold); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("idle_threshold: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("idle_threshold: must be > 0, got %q", cfg.IdleThreshold)
		}
	}
	return nil
}

// DefaultAssignConfig returns the default assign configuration
func DefaultAssignConfig() AssignConfig {
	return AssignConfig{
		Strategy: "balanced",
	}
}

// EnsembleConfig holds configuration defaults for reasoning ensembles.
type EnsembleConfig struct {
	DefaultEnsemble string                  `toml:"default_ensemble"`
	AgentMix        string                  `toml:"agent_mix"`
	Assignment      string                  `toml:"assignment"`
	ModeTierDefault string                  `toml:"mode_tier_default"` // core|advanced|experimental
	AllowAdvanced   bool                    `toml:"allow_advanced"`
	Synthesis       EnsembleSynthesisConfig `toml:"synthesis"`
	Cache           EnsembleCacheConfig     `toml:"cache"`
	Budget          EnsembleBudgetConfig    `toml:"budget"`
	EarlyStop       EnsembleEarlyStopConfig `toml:"early_stop"`
}

// EnsembleSynthesisConfig configures synthesis defaults for ensembles.
type EnsembleSynthesisConfig struct {
	Strategy           string  `toml:"strategy"`
	MinConfidence      float64 `toml:"min_confidence"`
	MaxFindings        int     `toml:"max_findings"`
	IncludeRawOutputs  bool    `toml:"include_raw_outputs"`
	ConflictResolution string  `toml:"conflict_resolution"`
}

// EnsembleCacheConfig configures context pack caching defaults.
type EnsembleCacheConfig struct {
	Enabled          bool   `toml:"enabled"`
	TTLMinutes       int    `toml:"ttl_minutes"`
	CacheDir         string `toml:"cache_dir"`
	MaxEntries       int    `toml:"max_entries"`
	ShareAcrossModes bool   `toml:"share_across_modes"`
}

// EnsembleBudgetConfig configures token budgets for ensembles.
type EnsembleBudgetConfig struct {
	PerAgent    int `toml:"per_agent"`
	Total       int `toml:"total"`
	Synthesis   int `toml:"synthesis"`
	ContextPack int `toml:"context_pack"`
}

// EnsembleEarlyStopConfig configures early stop thresholds for ensembles.
type EnsembleEarlyStopConfig struct {
	Enabled             bool    `toml:"enabled"`
	MinAgents           int     `toml:"min_agents"`
	FindingsThreshold   float64 `toml:"findings_threshold"`
	SimilarityThreshold float64 `toml:"similarity_threshold"`
	WindowSize          int     `toml:"window_size"`
}

// DefaultEnsembleConfig returns the default ensemble configuration.
func DefaultEnsembleConfig() EnsembleConfig {
	return EnsembleConfig{
		DefaultEnsemble: "architecture-review",
		AgentMix:        "cc=3,cod=2,gmi=1",
		Assignment:      "affinity",
		ModeTierDefault: "core",
		AllowAdvanced:   false,
		Synthesis: EnsembleSynthesisConfig{
			Strategy: "deliberative",
		},
		Cache: EnsembleCacheConfig{
			Enabled:          true,
			TTLMinutes:       60,
			CacheDir:         "~/.cache/ntm/context-packs",
			MaxEntries:       32,
			ShareAcrossModes: true,
		},
		Budget: EnsembleBudgetConfig{
			PerAgent:    5000,
			Total:       30000,
			Synthesis:   8000,
			ContextPack: 2000,
		},
		EarlyStop: EnsembleEarlyStopConfig{
			Enabled:             true,
			MinAgents:           3,
			FindingsThreshold:   0.15,
			SimilarityThreshold: 0.7,
			WindowSize:          3,
		},
	}
}

// DefaultIntegrationsConfig returns sensible defaults for integrations.
func DefaultIntegrationsConfig() IntegrationsConfig {
	return IntegrationsConfig{
		DCG: DCGConfig{
			Enabled:         false,
			BinaryPath:      "",
			CustomBlocklist: nil,
			CustomWhitelist: nil,
			AuditLog:        "",
			AllowOverride:   true,
		},
		CAAM:          DefaultCAAMConfig(),
		RCH:           DefaultRCHConfig(),
		ProcessTriage: DefaultProcessTriageConfig(),
		Rano:          DefaultRanoConfig(),
		XF:            DefaultXFConfig(),
		BV:            DefaultBVConfig(),
	}
}

// CAAMConfig holds configuration for CAAM (Coding Agent Account Manager) integration.
// CAAM provides automatic account rotation when rate limits are hit.
// The enabled/auto_rotate/providers keys were deprecated in v1.28.0
// (bd-6otuk): caam availability is probed at call time and rotation is
// governed by [rotation] and the --auto-rotate-accounts flag.
type CAAMConfig struct {
	BinaryPath string `toml:"binary_path"` // Path to caam binary (optional, defaults to PATH lookup)

	// AutoFailover enables the coordinator's automatic account failover
	// (bd-um3uy): when a coordinator tick observes a banner-verified rate
	// limit on an agent pane whose reset lies beyond reset_horizon_minutes,
	// and a verified alternate caam account with headroom exists, the
	// coordinator switches accounts through the same machinery
	// --robot-switch-account uses. Default false: no caam probing, no new
	// subprocess calls, no behavior change.
	AutoFailover bool `toml:"auto_failover"`

	// ResetHorizonMinutes is the minimum detected-reset distance that
	// justifies an automatic failover: limits that reset sooner than this
	// are waited out instead of burning an account switch. A reset hint that
	// cannot be parsed into a time is treated as BEYOND the horizon (a
	// long-lived limit is the common case for unparseable phrasing, and the
	// remaining gates — verified alternate, per-pane hourly cooldown — bound
	// the blast radius). 0 fails over on any detected limit. Must be >= 0.
	ResetHorizonMinutes int `toml:"reset_horizon_minutes"`

	// FailoverProviders is the per-provider allow-list for auto-failover
	// (canonical names: "claude", "openai", "gemini"). Empty means NO
	// providers — the feature is doubly opt-in (auto_failover AND an
	// explicit allow-list). Deliberately separate from Providers above,
	// whose empty value means "all available".
	FailoverProviders []string `toml:"failover_providers"`
}

// DefaultCAAMConfig returns sensible defaults for CAAM integration.
func DefaultCAAMConfig() CAAMConfig {
	return CAAMConfig{
		BinaryPath: "", // Default to PATH lookup

		AutoFailover:        false, // Coordinator auto-failover OFF by default (bd-um3uy)
		ResetHorizonMinutes: 30,    // Only fail over when the reset is > 30 minutes away
		FailoverProviders:   nil,   // Empty allow-list = no providers (doubly opt-in)
	}
}

// ValidateCAAMConfig validates CAAM integration configuration.
func ValidateCAAMConfig(cfg *CAAMConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.ResetHorizonMinutes < 0 {
		return fmt.Errorf("reset_horizon_minutes must be >= 0, got %d", cfg.ResetHorizonMinutes)
	}
	return nil
}

// RCHConfig holds configuration for RCH (Remote Compilation Helper) integration.
// RCH provides build offloading to remote workers for faster compilation.
// The min_build_time/fallback_local/show_location/preferred_worker/
// dcg_whitelist keys were deprecated in v1.28.0 (bd-6otuk): parsed and
// echoed but consumed by nothing (dcg_whitelist was a documented legacy
// no-op).
type RCHConfig struct {
	Enabled           bool     `toml:"enabled"`            // Enable RCH build offloading
	BinaryPath        string   `toml:"binary_path"`        // Path to rch binary (optional, defaults to PATH lookup)
	InterceptPatterns []string `toml:"intercept_patterns"` // Commands to intercept (regex patterns)
}

// DefaultRCHConfig returns sensible defaults for RCH integration.
func DefaultRCHConfig() RCHConfig {
	return RCHConfig{
		Enabled:    true, // Enabled by default (when rch is available)
		BinaryPath: "",   // Default to PATH lookup
		InterceptPatterns: []string{
			"^cargo (build|test|check|rustc)",
			"^go (build|test)",
			"^npm run build",
			"^make",
		},
	}
}

// ProcessTriageConfig holds configuration for process_triage (pt) integration.
// pt uses Bayesian classification to identify useful, abandoned, and zombie processes.
// The binary_path key was deprecated in v1.28.0 (bd-6otuk): the pt adapter
// resolves the binary from PATH.
type ProcessTriageConfig struct {
	Enabled        bool `toml:"enabled"`         // Enable process triage integration
	CheckInterval  int  `toml:"check_interval"`  // How often to check processes (seconds)
	IdleThreshold  int  `toml:"idle_threshold"`  // Seconds of idle before considering abandoned
	StuckThreshold int  `toml:"stuck_threshold"` // Seconds stuck before considering zombie
	UseRanoData    bool `toml:"use_rano_data"`   // Use rano network data to improve classification
}

// DefaultProcessTriageConfig returns sensible defaults for process_triage integration.
func DefaultProcessTriageConfig() ProcessTriageConfig {
	return ProcessTriageConfig{
		Enabled:        true, // Enabled by default (when pt is available)
		CheckInterval:  30,   // Check every 30 seconds
		IdleThreshold:  300,  // 5 minutes idle = abandoned candidate
		StuckThreshold: 600,  // 10 minutes stuck = zombie candidate
		UseRanoData:    true, // Use rano data when available
	}
}

// ValidateProcessTriageConfig validates the process_triage configuration.
func ValidateProcessTriageConfig(cfg *ProcessTriageConfig) error {
	if cfg == nil {
		return nil
	}

	// Skip validation for unconfigured/zero-valued configs (use defaults)
	if !cfg.Enabled && cfg.CheckInterval == 0 && cfg.IdleThreshold == 0 && cfg.StuckThreshold == 0 {
		return nil
	}

	if cfg.CheckInterval < 5 {
		return fmt.Errorf("check_interval must be at least 5 seconds, got %d", cfg.CheckInterval)
	}

	if cfg.IdleThreshold < 30 {
		return fmt.Errorf("idle_threshold must be at least 30 seconds, got %d", cfg.IdleThreshold)
	}

	if cfg.StuckThreshold < cfg.IdleThreshold {
		return fmt.Errorf("stuck_threshold (%d) must be >= idle_threshold (%d)", cfg.StuckThreshold, cfg.IdleThreshold)
	}

	return nil
}

// RanoConfig holds configuration for the rano network observer integration.
// rano monitors network activity per process, enabling per-agent API tracking.
// The binary_path/providers keys were deprecated in v1.28.0 (bd-6otuk):
// the rano adapter resolves the binary from PATH and tracks all known
// providers.
type RanoConfig struct {
	Enabled        bool `toml:"enabled"`          // Enable rano network monitoring integration
	PollIntervalMs int  `toml:"poll_interval_ms"` // Polling interval in milliseconds
}

// DefaultRanoConfig returns sensible defaults for rano integration.
func DefaultRanoConfig() RanoConfig {
	return RanoConfig{
		Enabled:        true, // Enabled by default (when rano is available)
		PollIntervalMs: 1000, // Poll every second
	}
}

// ValidateRanoConfig validates the rano configuration.
func ValidateRanoConfig(cfg *RanoConfig) error {
	if cfg == nil {
		return nil
	}

	// Skip validation for unconfigured/zero-valued configs (use defaults)
	if !cfg.Enabled && cfg.PollIntervalMs == 0 {
		return nil
	}

	if cfg.PollIntervalMs < 100 {
		return fmt.Errorf("poll_interval_ms must be at least 100ms, got %d", cfg.PollIntervalMs)
	}

	return nil
}

// XFConfig holds configuration for xf (X/Twitter archive search) integration.
// xf enables querying local X/Twitter archives from NTM via robot/tool bridges.
// Only the enabled toggle remains: it gates the built-in xf-search palette
// entry (internal/robot GetPalette). The former bin_path/archive_path/
// default_mode knobs were removed in v1.26.0 (bd-ws6-config-truth-ienmd.2) —
// the shipped xf surfaces resolve the binary from PATH.
type XFConfig struct {
	Enabled bool `toml:"enabled"` // Enable xf integration (gates the xf-search palette entry)
}

// DefaultXFConfig returns sensible defaults for xf integration.
func DefaultXFConfig() XFConfig {
	return XFConfig{
		Enabled: true,
	}
}

// ModelsConfig holds model alias configuration for each agent type
type ModelsConfig struct {
	DefaultClaude string            `toml:"default_claude"` // Default model for Claude
	DefaultCodex  string            `toml:"default_codex"`  // Default model for Codex
	DefaultGemini string            `toml:"default_gemini"` // Default model for Gemini
	DefaultGrok   string            `toml:"default_grok"`   // Optional Grok Build default; empty delegates to the CLI
	DefaultOllama string            `toml:"default_ollama"` // Default model for Ollama
	Claude        map[string]string `toml:"claude"`         // Claude model aliases
	Codex         map[string]string `toml:"codex"`          // Codex model aliases
	Gemini        map[string]string `toml:"gemini"`         // Gemini model aliases
	Grok          map[string]string `toml:"grok"`           // Grok Build model aliases
	Ollama        map[string]string `toml:"ollama"`         // Ollama model aliases
	Cursor        map[string]string `toml:"cursor"`         // Cursor model aliases
	Windsurf      map[string]string `toml:"windsurf"`       // Windsurf model aliases
	Aider         map[string]string `toml:"aider"`          // Aider model aliases
	Opencode      map[string]string `toml:"opencode"`       // Opencode (oc) model aliases — see ntm#116
	// ContextLimits allows overriding built-in context window sizes for models.
	// Keys are model names (e.g., "claude-opus-4-6"), values are token counts.
	// These override the built-in defaults in internal/models/registry.go.
	ContextLimits map[string]int `toml:"context_limits"`
}

// DefaultModels returns the default model configuration with sensible aliases.
// Model IDs should match those in internal/agents/profiles.go (no date suffixes).
func DefaultModels() ModelsConfig {
	return ModelsConfig{
		DefaultClaude: "claude-opus-4-8",
		DefaultCodex:  DefaultCodexModel,
		DefaultGemini: "gemini-3-pro-preview",
		DefaultGrok:   "",
		DefaultOllama: "llama3",
		Claude: map[string]string{
			"opus":      "claude-opus-4-8",
			"sonnet":    "claude-sonnet-4-6",
			"haiku":     "claude-haiku-4-5",
			"architect": "claude-opus-4-8",
			"fast":      "claude-sonnet-4-6",
		},
		Codex: map[string]string{
			"gpt4":  "gpt-4",
			"gpt5":  DefaultCodexModel,
			"o1":    "o1",
			"o3":    "o3",
			"turbo": "gpt-4-turbo",
			"codex": DefaultCodexModel,
		},
		Gemini: map[string]string{
			"pro":    "gemini-3-pro-preview",
			"flash":  "gemini-3-flash",
			"flash2": "gemini-2.0-flash",
		},
		Grok: map[string]string{},
		Ollama: map[string]string{
			"llama3": "llama3",
			"phi3":   "phi3",
		},
	}
}
func canonicalModelLookupAgentType(agentType string) string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeAntigravity:
		return "antigravity"
	case agent.AgentTypeGrok:
		return "grok"
	case agent.AgentTypeOllama:
		return "ollama"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOpencode:
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}

// AntigravityRequiredModel is the ONLY model agy (Antigravity CLI) may run on.
// agy is hard-pinned to it everywhere (never a Flash/Anthropic/other tier) per
// the model guard (bd-47kjh.1.7); it is intentionally NOT user-configurable.
const AntigravityRequiredModel = "Gemini 3.1 Pro (High)"

// GetModelName resolves a model alias to its full model name.
// Returns the alias itself if no mapping is found.
// AliasesFor returns the alias -> full-model-name map for a canonical agent
// type ("claude", "codex", ...), or nil when the type has no alias table.
// The argument is not re-normalized; pass canonicalModelLookupAgentType output
// (GetModelName does) or an already-canonical provider name.
// AliasesFor returns the model alias table for an agent type. The agent type is
// normalized first, so short forms and aliases ("cc", "oc", "agy") resolve to
// the same table as their canonical names. antigravity has no table because its
// model is hard-pinned; use GetModelName for that.
func (m *ModelsConfig) AliasesFor(agentType string) map[string]string {
	switch canonicalModelLookupAgentType(agentType) {
	case "claude":
		return m.Claude
	case "codex":
		return m.Codex
	case "gemini":
		return m.Gemini
	case "grok":
		return m.Grok
	case "ollama":
		return m.Ollama
	case "cursor":
		return m.Cursor
	case "windsurf":
		return m.Windsurf
	case "aider":
		return m.Aider
	case "opencode":
		return m.Opencode
	}
	return nil
}

func (m *ModelsConfig) GetModelName(agentType, alias string) string {
	normalizedAgentType := canonicalModelLookupAgentType(agentType)

	// agy is hard-pinned: ignore any requested alias/model and always resolve to
	// the single allowed model (model guard, bd-47kjh.1.7).
	if normalizedAgentType == "antigravity" {
		return AntigravityRequiredModel
	}

	if alias == "" {
		// Return default if no alias specified.
		switch normalizedAgentType {
		case "claude":
			return m.DefaultClaude
		case "codex":
			return m.DefaultCodex
		case "gemini":
			return m.DefaultGemini
		case "grok":
			return m.DefaultGrok
		case "ollama":
			return m.DefaultOllama
		}
		return ""
	}

	// Check agent-specific aliases.
	aliases := m.AliasesFor(normalizedAgentType)

	if aliases != nil {
		if fullName, ok := aliases[strings.ToLower(alias)]; ok {
			return fullName
		}
	}

	// Return the alias as-is (assume it's a full model name).
	return alias
}

// IsPersonaName checks if the given name is a known persona by searching
// built-in personas, user personas (~/.config/ntm/personas.toml), and
// project personas (.ntm/personas.toml).
//
// Note: loads the persona registry from disk on each call. Avoid calling
// in tight loops; cache the result if checking multiple names.
func (c *Config) IsPersonaName(name string) bool {
	if name == "" {
		return false
	}
	projectDir := ""
	if c != nil {
		projectDir = c.GetProjectDir("")
	}
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil || registry == nil {
		return false
	}
	p, ok := registry.Get(name)
	return ok && p != nil
}

// DefaultPath returns the default config file path
func DefaultPath() string {
	if env := os.Getenv("NTM_CONFIG"); env != "" {
		return ExpandHome(env)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fallback to /tmp when home directory is unavailable (e.g., containers)
		home = os.TempDir()
	}
	return filepath.Join(home, ".config", "ntm", "config.toml")
}

// DefaultProjectsBase returns the default projects directory.
// Honors NTM_PROJECTS_BASE env var when set, allowing provisioning tools
// (e.g. ACFS) to override the compiled default without touching config.toml.
func DefaultProjectsBase() string {
	if envBase := os.Getenv("NTM_PROJECTS_BASE"); envBase != "" {
		return envBase
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fallback to /tmp only when home directory is truly unavailable (e.g., containers)
		return filepath.Join(os.TempDir(), "ntm_Dev")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Developer")
	}
	// Linux/other: use ~/ntm_Dev instead of /tmp to avoid data loss on reboot
	return filepath.Join(home, "ntm_Dev")
}

// findPaletteMarkdownForPathAndCWD searches for a command_palette.md file in
// standard locations for the selected config path and context directory. Search
// order: sibling of the active config file, then command_palette.md in cwd.
func findPaletteMarkdownForPathAndCWD(configPath, cwd string) string {
	if strings.TrimSpace(configPath) == "" {
		configPath = DefaultPath()
	}

	// Check the active config directory first (user customization)
	configDir := filepath.Dir(ExpandHome(configPath))
	mdPath := filepath.Join(configDir, "command_palette.md")
	if _, err := os.Stat(mdPath); err == nil {
		return mdPath
	}

	// Check the selected working directory (project-specific)
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	cwdPath := filepath.Join(cwd, "command_palette.md")
	if _, err := os.Stat(cwdPath); err == nil {
		return cwdPath
	}

	return ""
}

// findPaletteMarkdownForPath searches for a command_palette.md file in standard
// locations for the selected config path using the process cwd as fallback.
func findPaletteMarkdownForPath(configPath string) string {
	return findPaletteMarkdownForPathAndCWD(configPath, "")
}

func findPaletteMarkdown() string {
	return findPaletteMarkdownForPath(DefaultPath())
}

// DetectPalettePathForConfigPathAndCWD returns the palette markdown path to use
// for a selected config path and working directory, if any. Precedence:
// explicit cfg.PaletteFile, then auto-discovered markdown adjacent to that
// config path or in the provided cwd.
func DetectPalettePathForConfigPathAndCWD(cfg *Config, configPath, cwd string) string {
	if cfg == nil {
		return ""
	}
	if cfg.PaletteFile != "" {
		return cfg.PaletteFile
	}
	return findPaletteMarkdownForPathAndCWD(configPath, cwd)
}

// DetectPalettePathForConfigPath returns the palette markdown path to use for
// a selected config path, if any. Precedence: explicit cfg.PaletteFile, then
// auto-discovered markdown adjacent to that config path or in the current cwd.
func DetectPalettePathForConfigPath(cfg *Config, configPath string) string {
	return DetectPalettePathForConfigPathAndCWD(cfg, configPath, "")
}

// DetectPalettePath returns the palette markdown path to use, if any.
// Precedence: explicit cfg.PaletteFile, then auto-discovered markdown.
func DetectPalettePath(cfg *Config) string {
	return DetectPalettePathForConfigPath(cfg, DefaultPath())
}

// LoadPaletteFromMarkdown parses a command palette from markdown format.
// Format:
//
//	## Category Name
//	### command_key | Display Label
//	The prompt text (can be multiple lines)
//
// Lines starting with # (but not ## or ###) are treated as comments.
func LoadPaletteFromMarkdown(path string) ([]PaletteCmd, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var commands []PaletteCmd
	var currentCategory string
	var currentCmd *PaletteCmd
	var promptLines []string

	// Normalize line endings
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		// Check for category header: ## Category Name
		if strings.HasPrefix(line, "## ") {
			// Save previous command if exists
			if currentCmd != nil {
				currentCmd.Prompt = strings.TrimSpace(strings.Join(promptLines, "\n"))
				if currentCmd.Prompt != "" {
					commands = append(commands, *currentCmd)
				}
				currentCmd = nil
				promptLines = nil
			}
			currentCategory = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}

		// Check for command header: ### key | Label
		if strings.HasPrefix(line, "### ") {
			// Save previous command if exists
			if currentCmd != nil {
				currentCmd.Prompt = strings.TrimSpace(strings.Join(promptLines, "\n"))
				if currentCmd.Prompt != "" {
					commands = append(commands, *currentCmd)
				}
				promptLines = nil
			}

			// Parse key | label
			header := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			parts := strings.SplitN(header, "|", 2)
			if len(parts) != 2 {
				// Invalid format, skip this command
				currentCmd = nil
				continue
			}

			currentCmd = &PaletteCmd{
				Key:      strings.TrimSpace(parts[0]),
				Label:    strings.TrimSpace(parts[1]),
				Category: currentCategory,
			}
			continue
		}

		// Comment: starts with # but not ## or ### AND we are not inside a command block
		if currentCmd == nil && strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "##") {
			continue
		}

		// Otherwise, it's prompt content
		if currentCmd != nil {
			promptLines = append(promptLines, line)
		}
	}

	// Don't forget the last command
	if currentCmd != nil {
		currentCmd.Prompt = strings.TrimSpace(strings.Join(promptLines, "\n"))
		if currentCmd.Prompt != "" {
			commands = append(commands, *currentCmd)
		}
	}

	return commands, nil
}

// DefaultAgentMailURL is the default Agent Mail server URL.
const DefaultAgentMailURL = "http://127.0.0.1:8765/mcp/"

// Safety profiles are user-friendly presets that bundle multiple safety knobs.
// These should remain explicit mappings (no hidden magic) so users can reason about overrides.
const (
	SafetyProfileStandard = "standard"
	SafetyProfileSafe     = "safe"
	SafetyProfileParanoid = "paranoid"
)

// SafetyConfig controls safety profile selection.
type SafetyConfig struct {
	// Profile selects the safety profile preset:
	// - standard (default): preflight on, redaction=warn, privacy=off
	// - safe: preflight on, redaction=redact, stricter destructive command gating
	// - paranoid: preflight on, redaction=block, privacy=on by default
	Profile string `toml:"profile"`
}

// DefaultSafetyConfig returns sensible safety defaults.
func DefaultSafetyConfig() SafetyConfig {
	return SafetyConfig{Profile: SafetyProfileStandard}
}

// ValidateSafetyConfig validates the safety configuration.
func ValidateSafetyConfig(cfg *SafetyConfig) error {
	if cfg.Profile == "" {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Profile)) {
	case SafetyProfileStandard, SafetyProfileSafe, SafetyProfileParanoid:
		return nil
	default:
		return fmt.Errorf("invalid safety profile %q: must be %q, %q, or %q",
			cfg.Profile, SafetyProfileStandard, SafetyProfileSafe, SafetyProfileParanoid)
	}
}

// PreflightConfig controls prompt preflight (lint/validation) defaults.
// The preflight.enabled key was deprecated in v1.28.0 (bd-6otuk): no send
// path ever consulted it (it was surfaced in doctor output only).
type PreflightConfig struct {
	// Strict controls whether warnings are treated as errors by default.
	Strict bool `toml:"strict"`
}

// DefaultPreflightConfig returns sensible preflight defaults.
func DefaultPreflightConfig() PreflightConfig {
	return PreflightConfig{
		Strict: false,
	}
}

// ValidatePreflightConfig validates the preflight configuration.
func ValidatePreflightConfig(cfg *PreflightConfig) error {
	// No complex validation needed for boolean flags.
	return nil
}

type safetyProfileDefaults struct {
	preflightStrict  bool
	redactionMode    string
	privacyEnabled   bool
	dcgAllowOverride bool
}

var safetyProfileMap = map[string]safetyProfileDefaults{
	SafetyProfileStandard: {
		preflightStrict:  false,
		redactionMode:    "warn",
		privacyEnabled:   false,
		dcgAllowOverride: true,
	},
	SafetyProfileSafe: {
		preflightStrict:  false,
		redactionMode:    "redact",
		privacyEnabled:   false,
		dcgAllowOverride: false,
	},
	SafetyProfileParanoid: {
		preflightStrict:  true,
		redactionMode:    "block",
		privacyEnabled:   true,
		dcgAllowOverride: false,
	},
}

func normalizeSafetyProfile(profile string) string {
	p := strings.ToLower(strings.TrimSpace(profile))
	if p == "" {
		return SafetyProfileStandard
	}
	if _, ok := safetyProfileMap[p]; ok {
		return p
	}
	return SafetyProfileStandard
}

func applySafetyProfileDefaults(cfg *Config) {
	if cfg == nil {
		return
	}

	cfg.Safety.Profile = normalizeSafetyProfile(cfg.Safety.Profile)
	def := safetyProfileMap[cfg.Safety.Profile]

	cfg.Preflight.Strict = def.preflightStrict
	cfg.Redaction.Mode = def.redactionMode
	cfg.Privacy.Enabled = def.privacyEnabled
	cfg.Integrations.DCG.AllowOverride = def.dcgAllowOverride
}

// RedactionConfig holds configuration for secrets/PII redaction.
// This controls how NTM handles sensitive content in commands, mail, and exports.
type RedactionConfig struct {
	// Mode controls redaction behavior: off, warn, redact, block
	// - off: disable all scanning
	// - warn: log findings but don't modify content
	// - redact: replace sensitive content with placeholders
	// - block: fail operations if secrets detected
	Mode string `toml:"mode"`

	// Allowlist contains regex patterns that should NOT be flagged.
	// Use for known-safe patterns like test tokens or placeholder values.
	Allowlist []string `toml:"allowlist,omitempty"`

	// ExtraPatterns contains additional patterns to detect beyond defaults.
	// Map category names (e.g., "CUSTOM_TOKEN") to regex patterns.
	ExtraPatterns map[string][]string `toml:"extra_patterns,omitempty"`

	// DisabledCategories lists secret categories to skip during scanning.
	// Valid categories: OPENAI_KEY, ANTHROPIC_KEY, GITHUB_TOKEN, AWS_ACCESS_KEY,
	// AWS_SECRET_KEY, JWT, GOOGLE_API_KEY, PRIVATE_KEY, DATABASE_URL, PASSWORD,
	// GENERIC_API_KEY, GENERIC_SECRET, BEARER_TOKEN
	DisabledCategories []string `toml:"disabled_categories,omitempty"`
}

// DefaultRedactionConfig returns sensible redaction defaults.
func DefaultRedactionConfig() RedactionConfig {
	return RedactionConfig{
		Mode: "warn", // Safe default: detect but don't block
	}
}

// ValidateRedactionConfig validates the redaction configuration.
func ValidateRedactionConfig(cfg *RedactionConfig) error {
	switch cfg.Mode {
	case "", "off", "warn", "redact", "block":
		return nil
	default:
		return fmt.Errorf("invalid redaction mode %q: must be off, warn, redact, or block", cfg.Mode)
	}
}

// PrivacyConfig holds configuration for privacy mode.
// Privacy mode prevents persistence of sensitive session data.
type PrivacyConfig struct {
	// Enabled is the global default for privacy mode.
	// When true, all new sessions start in privacy mode unless overridden.
	Enabled bool `toml:"enabled"`

	// DisablePromptHistory prevents storing prompt/command history.
	DisablePromptHistory bool `toml:"disable_prompt_history"`

	// DisableEventLogs prevents writing event logs (or limits to minimal metadata).
	DisableEventLogs bool `toml:"disable_event_logs"`

	// DisableCheckpoints prevents automatic checkpoint creation.
	DisableCheckpoints bool `toml:"disable_checkpoints"`

	// DisableScrollbackCapture prevents scrollback persistence in support bundles.
	DisableScrollbackCapture bool `toml:"disable_scrollback_capture"`

	// RequireExplicitPersist requires --allow-persist flag for any persistence operations.
	// When true, operations that would write to disk fail unless explicitly allowed.
	RequireExplicitPersist bool `toml:"require_explicit_persist"`
}

// DefaultPrivacyConfig returns sensible privacy defaults.
// Privacy mode is opt-in by default.
func DefaultPrivacyConfig() PrivacyConfig {
	return PrivacyConfig{
		Enabled:                  false, // Privacy mode disabled by default
		DisablePromptHistory:     true,  // When enabled, disable history by default
		DisableEventLogs:         true,  // When enabled, disable event logs
		DisableCheckpoints:       true,  // When enabled, disable checkpoints
		DisableScrollbackCapture: true,  // When enabled, disable scrollback capture
		RequireExplicitPersist:   true,  // When enabled, require explicit --allow-persist
	}
}

// ValidatePrivacyConfig validates the privacy configuration.
func ValidatePrivacyConfig(cfg *PrivacyConfig) error {
	// No complex validation needed for boolean flags
	return nil
}

// EncryptionConfig controls encryption at rest for NTM artifacts
// (prompt history, event logs, checkpoint exports).
type EncryptionConfig struct {
	// Enabled is the top-level toggle for encryption at rest (default false).
	Enabled bool `toml:"enabled"`
	// KeySource selects how the encryption key is provided: env, file, or command.
	KeySource string `toml:"key_source"`
	// KeyEnv is the environment variable name holding the key (for key_source=env).
	KeyEnv string `toml:"key_env"`
	// KeyFile is the path to a file containing the key (for key_source=file).
	KeyFile string `toml:"key_file"`
	// KeyCommand is a shell command that prints the key to stdout (for key_source=command).
	KeyCommand string `toml:"key_command"`
	// KeyFormat is the encoding of the key material: hex or base64.
	KeyFormat string `toml:"key_format"`
	// ActiveKeyID selects which keyring entry to use for new writes (optional).
	ActiveKeyID string `toml:"active_key_id"`
	// Keyring maps key IDs to encoded key material for rotation support.
	Keyring map[string]string `toml:"keyring"`
}

// DefaultEncryptionConfig returns sensible encryption defaults (disabled).
func DefaultEncryptionConfig() EncryptionConfig {
	return EncryptionConfig{
		Enabled:   false,
		KeySource: "env",
		KeyEnv:    "NTM_ENCRYPTION_KEY",
		KeyFormat: "hex",
	}
}

// ValidateEncryptionConfig validates the encryption configuration.
func ValidateEncryptionConfig(cfg *EncryptionConfig) error {
	if !cfg.Enabled {
		return nil
	}
	switch cfg.KeySource {
	case "env", "file", "command":
		// valid
	case "":
		return fmt.Errorf("encryption.key_source is required when encryption is enabled")
	default:
		return fmt.Errorf("invalid encryption.key_source %q: must be env, file, or command", cfg.KeySource)
	}
	switch cfg.KeyFormat {
	case "hex", "base64", "":
		// valid (empty defaults to hex)
	default:
		return fmt.Errorf("invalid encryption.key_format %q: must be hex or base64", cfg.KeyFormat)
	}
	// A keyring without an active key would write with the key_source key while
	// reading only with keyring entries, so every new artifact would be
	// undecryptable. Refuse that configuration instead of losing data.
	if len(cfg.Keyring) > 0 {
		if cfg.ActiveKeyID == "" {
			return fmt.Errorf("encryption.active_key_id is required when encryption.keyring is set: name the keyring entry used for new writes")
		}
		if _, ok := cfg.Keyring[cfg.ActiveKeyID]; !ok {
			return fmt.Errorf("encryption.active_key_id %q not found in keyring", cfg.ActiveKeyID)
		}
	}
	return nil
}

// SendConfig holds defaults for the send command.
type SendConfig struct {
	BasePrompt     string `toml:"base_prompt"`      // Text prepended to all prompts
	BasePromptFile string `toml:"base_prompt_file"` // File whose contents are prepended to all prompts
}

// PromptsConfig holds per-agent-type default prompts (bd-2ywo).
type PromptsConfig struct {
	CCDefault      string `toml:"cc_default"`       // Default prompt for Claude agents
	CCDefaultFile  string `toml:"cc_default_file"`  // File path for Claude default prompt
	CodDefault     string `toml:"cod_default"`      // Default prompt for Codex agents
	CodDefaultFile string `toml:"cod_default_file"` // File path for Codex default prompt
	GmiDefault     string `toml:"gmi_default"`      // Default prompt for Gemini agents
	GmiDefaultFile string `toml:"gmi_default_file"` // File path for Gemini default prompt
	AgyDefault     string `toml:"agy_default"`      // Default prompt for Antigravity (agy) agents
	AgyDefaultFile string `toml:"agy_default_file"` // File path for Antigravity default prompt
}

// ResolveForType returns the default prompt for a given agent type string (cc, cod, gmi).
// It reads from the inline string first, falling back to the file if configured.
func (p PromptsConfig) ResolveForType(agentType string) (string, error) {
	var val, filePath string
	switch agentType {
	case "cc":
		val, filePath = p.CCDefault, p.CCDefaultFile
	case "cod":
		val, filePath = p.CodDefault, p.CodDefaultFile
	case "gmi":
		val, filePath = p.GmiDefault, p.GmiDefaultFile
	case "agy":
		val, filePath = p.AgyDefault, p.AgyDefaultFile
	default:
		return "", nil
	}
	if val != "" {
		return strings.TrimSpace(val), nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("reading prompts.%s_default_file: %w", agentType, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// ToRedactionLibConfig converts the config to the redaction library's Config type.
func (c *RedactionConfig) ToRedactionLibConfig() redaction.Config {
	mode := redaction.ModeWarn // default
	switch c.Mode {
	case "off":
		mode = redaction.ModeOff
	case "warn":
		mode = redaction.ModeWarn
	case "redact":
		mode = redaction.ModeRedact
	case "block":
		mode = redaction.ModeBlock
	}

	libCfg := redaction.Config{
		Mode:      mode,
		Allowlist: c.Allowlist,
	}

	// Convert extra patterns
	if len(c.ExtraPatterns) > 0 {
		libCfg.ExtraPatterns = make(map[redaction.Category][]string)
		for cat, patterns := range c.ExtraPatterns {
			libCfg.ExtraPatterns[redaction.Category(cat)] = patterns
		}
	}

	// Convert disabled categories
	if len(c.DisabledCategories) > 0 {
		libCfg.DisabledCategories = make([]redaction.Category, len(c.DisabledCategories))
		for i, cat := range c.DisabledCategories {
			libCfg.DisabledCategories[i] = redaction.Category(cat)
		}
	}

	return libCfg
}

// Default returns the default configuration.
// It tries to load the palette from a markdown file first, falling back to hardcoded defaults.
func Default() *Config {
	// Determine projects base: env var takes precedence
	projectsBase := DefaultProjectsBase()
	if envBase := os.Getenv("NTM_PROJECTS_BASE"); envBase != "" {
		projectsBase = envBase
	}

	cfg := &Config{
		ProjectsBase: projectsBase,
		Agents:       DefaultAgentTemplates(),
		Tmux: TmuxConfig{
			DefaultPanes:    10,
			PaneInitDelayMs: 1000,
			HistoryLimit:    50000,
		},
		Robot: DefaultRobotConfig(),
		AgentMail: AgentMailConfig{
			Enabled:      true,
			URL:          DefaultAgentMailURL,
			Token:        "",
			AutoRegister: true,
		},
		Integrations:    DefaultIntegrationsConfig(),
		Models:          DefaultModels(),
		Alerts:          DefaultAlertsConfig(),
		Checkpoints:     DefaultCheckpointsConfig(),
		Notifications:   notify.DefaultConfig(),
		Resilience:      DefaultResilienceConfig(),
		Scanner:         DefaultScannerConfig(),
		Bugs:            DefaultBugsConfig(),
		CASS:            DefaultCASSConfig(),
		Rotation:        DefaultRotationConfig(),
		GeminiSetup:     DefaultGeminiSetupConfig(),
		Context:         DefaultContextConfig(),
		ContextRotation: DefaultContextRotationConfig(),
		SessionRecovery: DefaultSessionRecoveryConfig(),
		Cleanup:         DefaultCleanupConfig(),
		FileReservation: DefaultFileReservationConfig(),
		Memory:          DefaultMemoryConfig(),
		Assign:          DefaultAssignConfig(),
		Ensemble:        DefaultEnsembleConfig(),
		Swarm:           DefaultSwarmConfig(),
		Safety:          DefaultSafetyConfig(),
		Preflight:       DefaultPreflightConfig(),
		Redaction:       DefaultRedactionConfig(),
		Privacy:         DefaultPrivacyConfig(),
		Encryption:      DefaultEncryptionConfig(),
		SpawnPacing:     DefaultSpawnPacingConfig(),
		Retry:           DefaultRetryConfig(),
		Routing:         DefaultRoutingConfig(),
		Coordinator:     DefaultCoordinatorConfig(),
	}

	// Apply safety profile defaults (standard/safe/paranoid).
	applySafetyProfileDefaults(cfg)

	// Try to load palette from markdown file
	if mdPath := findPaletteMarkdownForPath(DefaultPath()); mdPath != "" {
		if mdCmds, err := LoadPaletteFromMarkdown(mdPath); err == nil && len(mdCmds) > 0 {
			cfg.Palette = mdCmds
			return cfg
		}
	}

	// Fall back to hardcoded defaults
	cfg.Palette = defaultPaletteCommands()
	return cfg
}

func defaultPaletteCommands() []PaletteCmd {
	return []PaletteCmd{
		// Quick Actions
		{
			Key:      "fresh_review",
			Label:    "Fresh Eyes Review",
			Category: "Quick Actions",
			Prompt: `Take a step back and carefully reread the most recent code changes with fresh eyes.
Look for any obvious bugs, logical errors, or confusing patterns.
Fix anything you spot without waiting for direction.`,
		},
		{
			Key:      "fix_bug",
			Label:    "Fix the Bug",
			Category: "Quick Actions",
			Prompt: `Focus on diagnosing the root cause of the reported issue.
Don't just patch symptoms - find and fix the underlying problem.
Implement a real fix, not a workaround.`,
		},
		{
			Key:      "git_commit",
			Label:    "Commit Changes",
			Category: "Quick Actions",
			Prompt: `Commit all changed files with detailed, meaningful commit messages.
Group related changes logically. Push to the remote branch.`,
		},
		{
			Key:      "run_tests",
			Label:    "Run All Tests",
			Category: "Quick Actions",
			Prompt:   `Run the full test suite and fix any failing tests.`,
		},

		// Code Quality
		{
			Key:      "refactor",
			Label:    "Refactor Code",
			Category: "Code Quality",
			Prompt: `Review the current code for opportunities to improve:
- Extract reusable functions
- Simplify complex logic
- Improve naming
- Remove duplication
Make incremental improvements while preserving functionality.`,
		},
		{
			Key:      "add_types",
			Label:    "Add Type Annotations",
			Category: "Code Quality",
			Prompt: `Add comprehensive type annotations to the codebase.
Focus on function signatures, class attributes, and complex data structures.
Use generics where appropriate.`,
		},
		{
			Key:      "add_docs",
			Label:    "Add Documentation",
			Category: "Code Quality",
			Prompt: `Add comprehensive docstrings and comments to the codebase.
Document public APIs, complex algorithms, and non-obvious behavior.
Keep docs concise but complete.`,
		},

		// Coordination
		{
			Key:      "status_update",
			Label:    "Status Update",
			Category: "Coordination",
			Prompt: `Provide a brief status update:
1. What you just completed
2. What you're currently working on
3. Any blockers or questions
4. What you plan to do next`,
		},
		{
			Key:      "handoff",
			Label:    "Prepare Handoff",
			Category: "Coordination",
			Prompt: `Prepare a handoff document for another agent:
- Current state of the code
- What's working and what isn't
- Open issues and edge cases
- Recommended next steps`,
		},
		{
			Key:      "sync",
			Label:    "Sync with Main",
			Category: "Coordination",
			Prompt: `Pull latest changes from main branch and resolve any conflicts.
Run tests after merging to ensure nothing is broken.`,
		},
		{
			Key:      "check_project_inbox",
			Label:    "Check Project Inbox",
			Category: "Coordination",
			Prompt: `Check the project inbox for any new messages from other agents or the human overseer.
Run 'ntm mail inbox' to see the full list of messages.`,
		},

		// Investigation
		{
			Key:      "explain",
			Label:    "Explain This Code",
			Category: "Investigation",
			Prompt: `Explain how the current code works in detail.
Walk through the control flow, data transformations, and key design decisions.
Note any potential issues or areas for improvement.`,
		},
		{
			Key:      "find_issue",
			Label:    "Find the Issue",
			Category: "Investigation",
			Prompt: `Investigate the codebase to find potential issues:
- Logic errors
- Edge cases not handled
- Performance problems
- Security concerns
Report findings with specific file locations and line numbers.`,
		},
	}
}

// Load loads configuration from a file.
// Palette loading precedence:
//  1. Explicit palette_file from TOML config
//  2. Auto-discovered command_palette.md (~/.config/ntm/ or ./command_palette.md)
//  3. [[palette]] entries from TOML config
//  4. Hardcoded defaults
func Load(path string) (*Config, error) {
	return loadWithCWD(path, "")
}

func loadWithCWD(path, cwd string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}

	// 1. Initialize with defaults
	cfg := Default()

	// When the caller supplied an explicit working directory, do not let any
	// palette that Default() auto-discovered from the ambient process cwd leak
	// through. Reset to hardcoded defaults so the cwd-aware discovery below
	// (step 4) is the sole source of palette selection for this load.
	if strings.TrimSpace(cwd) != "" {
		cfg.Palette = defaultPaletteCommands()
	}

	// 2. Read and unmarshal TOML over defaults
	if data, err := os.ReadFile(path); err == nil {
		// Pre-scan safety profile so we can apply profile defaults before decoding the rest.
		// This lets explicit knob overrides in TOML take precedence over the selected profile.
		var pre struct {
			Safety SafetyConfig `toml:"safety"`
		}
		if err := toml.Unmarshal(data, &pre); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
		if pre.Safety.Profile != "" {
			cfg.Safety.Profile = pre.Safety.Profile
		}
		applySafetyProfileDefaults(cfg)

		md, err := toml.Decode(string(data), cfg)
		if err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
		if fields := undecodedConfigFields(md); len(fields) > 0 {
			// WS6-remove-finalize (bd-ws6-config-truth-ienmd.3): keys removed
			// in v1.26.0 warned for one release; since v1.27.0 each is a hard
			// strict-loader error with the same key + disposition text.
			// Genuinely unknown fields remain a hard error as before. Both
			// kinds are reported together in one error so a single failed
			// load lists everything the user must fix.
			//
			// Dead-knob batch two (bd-6otuk): the v1.28.0 deprecated keys are
			// on the warn leg of the same runway — load succeeds, value
			// ignored, one loud warning per key; they flip to errors in
			// v1.29.0.
			removed, deprecated, unknown := classifyUndecodedKeys(fields)
			if len(removed) > 0 || len(unknown) > 0 {
				var msgs []string
				if len(unknown) > 0 {
					msgs = append(msgs, "unknown field(s): "+strings.Join(unknown, ", "))
				}
				msgs = append(msgs, removedKnobErrorLines(removed)...)
				return nil, fmt.Errorf("parsing config: %s", strings.Join(msgs, "\n"))
			}
			warnDeprecatedKnobs(os.Stderr, deprecated)
		}

		// Canonicalize the profile string for stable downstream outputs (config show, robot status).
		// Do not re-apply profile defaults here: explicit knob overrides in TOML must win.
		cfg.Safety.Profile = normalizeSafetyProfile(cfg.Safety.Profile)

		// Fold the [resilience.rate_limit] auto_rotate alias into the canonical
		// rotation knobs the runtime monitor consults
		// (internal/resilience/monitor.go:494 gates on `Enabled && AutoTrigger`).
		// We flip BOTH because users who set the alias are opting into the
		// rate-limit-driven rotation behaviour wholesale; setting only
		// AutoTrigger without Enabled would silently no-op
		// (Rotation.Enabled defaults to false). The alias exists so users can
		// configure this intent co-located with the other rate-limit settings;
		// both forms set to true are an OR. See ntm#113.
		if cfg.Resilience.RateLimit.AutoRotate {
			cfg.Rotation.Enabled = true
			cfg.Rotation.AutoTrigger = true
		}

		// WS6-wire (bd-ws6-config-truth-ienmd.1): [recovery] is the single
		// section for session-recovery tuning; the overlapping memory.* keys
		// are aliased into it for one release with a deprecation warning and
		// will be removed in v1.27.0. An explicit [recovery] key always wins
		// over its memory.* alias.
		applyMemoryRecoveryAliases(cfg, &md, os.Stderr)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// 3. Apply Environment Variable Overrides (Env > TOML > Default)

	if envBase := os.Getenv("NTM_PROJECTS_BASE"); envBase != "" {
		cfg.ProjectsBase = envBase
	}

	// AgentMail Env Overrides
	if url := os.Getenv("AGENT_MAIL_URL"); url != "" {
		cfg.AgentMail.URL = url
	}
	if token := os.Getenv("AGENT_MAIL_TOKEN"); token != "" {
		cfg.AgentMail.Token = token
	}
	if enabled := os.Getenv("AGENT_MAIL_ENABLED"); enabled != "" {
		cfg.AgentMail.Enabled = enabled == "1" || enabled == "true"
	}

	// Scanner Env Overrides
	applyEnvOverrides(&cfg.Scanner)

	// CASS Env Overrides
	if enabled := os.Getenv("NTM_CASS_ENABLED"); enabled != "" {
		cfg.CASS.Enabled = enabled == "1" || enabled == "true"
	}
	if timeout := os.Getenv("NTM_CASS_TIMEOUT"); timeout != "" {
		var t int
		if _, err := fmt.Sscanf(timeout, "%d", &t); err == nil && t > 0 {
			cfg.CASS.Timeout = t
		}
	}
	if binary := os.Getenv("NTM_CASS_BINARY"); binary != "" {
		cfg.CASS.BinaryPath = binary
	}

	// BV Env Overrides (GH#253). NTM_BV_TIMEOUT (whole seconds) wins over
	// [integrations.bv] timeout_seconds; internal/bv also consults the same
	// variable at call time for paths that never load config.
	if timeout := os.Getenv("NTM_BV_TIMEOUT"); timeout != "" {
		if t, err := strconv.Atoi(strings.TrimSpace(timeout)); err == nil && t > 0 {
			cfg.Integrations.BV.TimeoutSeconds = t
		}
	}
	// CASS Context Env Overrides
	if contextEnabled := os.Getenv("NTM_CASS_CONTEXT_ENABLED"); contextEnabled != "" {
		cfg.CASS.Context.Enabled = contextEnabled == "1" || contextEnabled == "true"
	}
	if minRel := os.Getenv("NTM_CASS_MIN_RELEVANCE"); minRel != "" {
		if v, err := strconv.ParseFloat(minRel, 64); err == nil && v >= 0 && v <= 1 {
			cfg.CASS.Context.MinRelevance = v
		}
	}
	if skipAbove := os.Getenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE"); skipAbove != "" {
		if v, err := strconv.ParseFloat(skipAbove, 64); err == nil && v >= 0 && v <= 100 {
			cfg.CASS.Context.SkipIfContextAbove = v
		}
	}
	if preferSame := os.Getenv("NTM_CASS_PREFER_SAME_PROJECT"); preferSame != "" {
		cfg.CASS.Context.PreferSameProject = preferSame == "1" || preferSame == "true"
	}

	// Rotation Env Overrides
	if rotationEnabled := os.Getenv("NTM_ROTATION_ENABLED"); rotationEnabled != "" {
		cfg.Rotation.Enabled = rotationEnabled == "1" || rotationEnabled == "true"
	}

	// Gemini Env Overrides
	if autoSelect := os.Getenv("NTM_GEMINI_AUTO_PRO"); autoSelect != "" {
		cfg.GeminiSetup.AutoSelectProModel = autoSelect == "1" || autoSelect == "true"
	}

	// Session Recovery Env Overrides
	if recoveryEnabled := os.Getenv("NTM_RECOVERY_ENABLED"); recoveryEnabled != "" {
		cfg.SessionRecovery.Enabled = recoveryEnabled == "1" || recoveryEnabled == "true"
	}
	if includeAgentMail := os.Getenv("NTM_RECOVERY_INCLUDE_AGENT_MAIL"); includeAgentMail != "" {
		cfg.SessionRecovery.IncludeAgentMail = includeAgentMail == "1" || includeAgentMail == "true"
	}
	if includeCM := os.Getenv("NTM_RECOVERY_INCLUDE_CM"); includeCM != "" {
		cfg.SessionRecovery.IncludeCMMemories = includeCM == "1" || includeCM == "true"
	}
	if includeBeads := os.Getenv("NTM_RECOVERY_INCLUDE_BEADS"); includeBeads != "" {
		cfg.SessionRecovery.IncludeBeadsContext = includeBeads == "1" || includeBeads == "true"
	}
	if maxTokens := os.Getenv("NTM_RECOVERY_MAX_TOKENS"); maxTokens != "" {
		if n, err := strconv.Atoi(maxTokens); err == nil && n > 0 {
			cfg.SessionRecovery.MaxRecoveryTokens = n
		}
	}
	if autoInject := os.Getenv("NTM_RECOVERY_AUTO_INJECT"); autoInject != "" {
		cfg.SessionRecovery.AutoInjectOnSpawn = autoInject == "1" || autoInject == "true"
	}

	// 4. Palette Precedence: Markdown > TOML > Default
	// Default() already loaded Markdown if available.
	// Unmarshal() might have overwritten cfg.Palette with TOML entries.
	// We need to re-check Markdown to enforce Markdown > TOML.

	mdPath := cfg.PaletteFile
	if mdPath == "" {
		mdPath = findPaletteMarkdownForPathAndCWD(path, cwd)
	} else {
		mdPath = ExpandHome(mdPath)
	}

	if mdPath != "" {
		if mdCmds, err := LoadPaletteFromMarkdown(mdPath); err == nil && len(mdCmds) > 0 {
			cfg.Palette = mdCmds
		}
	}

	// Apply user-specified context limit overrides to the canonical registry.
	if len(cfg.Models.ContextLimits) > 0 {
		models.ApplyOverrides(cfg.Models.ContextLimits)
	}

	return cfg, nil
}

func undecodedConfigFields(md toml.MetaData) []string {
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, key.String())
	}
	sort.Strings(fields)
	return fields
}

// CreateDefault creates a default config file at path.
// If path is empty, the default config path is used.
func CreateDefault(path string) (string, error) {
	if path == "" {
		path = DefaultPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}

	// Check if file already exists
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("config file already exists: %s", path)
	}

	// Write default config
	var buffer strings.Builder
	if err := Print(Default(), &buffer); err != nil {
		return "", err
	}

	if err := util.AtomicWriteFile(path, []byte(buffer.String()), 0644); err != nil {
		return "", err
	}

	return path, nil
}

// UpsertPaletteState updates (or adds) the [palette_state] TOML table in the given config file.
// This preserves the rest of the file verbatim, avoiding re-encoding the full config.
func UpsertPaletteState(path string, state PaletteState) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	updated := upsertTOMLTable(string(data), "palette_state", renderPaletteStateTOML(state))

	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	return util.AtomicWriteFile(path, []byte(updated), mode)
}

func upsertTOMLTable(contents, tableName, tableBody string) string {
	lines := strings.Split(contents, "\n")

	header := "[" + tableName + "]"
	start := -1
	end := len(lines)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start == -1 {
			if trimmed == header {
				start = i
			}
			continue
		}

		// Stop at the next table header ([...] or [[...]]), but only after we found our table.
		if i > start && strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}

	if start != -1 {
		lines = append(lines[:start], lines[end:]...)
	}

	// Trim trailing empty lines so we can append cleanly.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n\n"
	}
	out += tableBody

	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

var configPersistenceMu sync.Mutex

// PersistTOMLKeys surgically writes `key = value` assignments inside [section]
// of the TOML file at path, preserving every other line — including comments
// and unrelated keys in the same section — verbatim (unlike upsertTOMLTable,
// which replaces a whole table body). A missing file is created (parent
// directory included); a missing section is appended at the end; an existing
// assignment is replaced in place; a new key is inserted directly after the
// section header. Values must already be rendered as TOML literals (e.g.
// `true`, `"30m"`). The rewritten content is parsed before the file is
// replaced, so a malformed result never reaches disk.
func PersistTOMLKeys(path, section string, keys [][2]string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path is empty")
	}
	sectionParts, err := validateTOMLBarePath(section, "config section")
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	seenKeys := make(map[string]struct{}, len(keys))
	for _, kv := range keys {
		if _, err := validateTOMLBarePath(kv[0], "config key"); err != nil || strings.Contains(kv[0], ".") {
			if err == nil {
				err = fmt.Errorf("config key %q must not be dotted", kv[0])
			}
			return err
		}
		if err := validateRenderedTOMLValue(kv[0], kv[1]); err != nil {
			return err
		}
		if _, duplicate := seenKeys[kv[0]]; duplicate {
			return fmt.Errorf("duplicate config key %q", kv[0])
		}
		seenKeys[kv[0]] = struct{}{}
	}

	resolvedPath, err := resolveConfigPersistencePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Serialize both goroutines and independent ntm processes across the full
	// read-modify-write transaction. Atomic rename alone prevents torn bytes but
	// does not prevent two toggles from losing one another's updates.
	configPersistenceMu.Lock()
	defer configPersistenceMu.Unlock()
	lockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unlock, err := acquireConfigPersistenceLock(lockCtx, resolvedPath+".lock")
	if err != nil {
		return fmt.Errorf("locking config %s: %w", path, err)
	}
	defer unlock()

	contents := ""
	mode := os.FileMode(0644)
	data, err := os.ReadFile(resolvedPath)
	switch {
	case err == nil:
		contents = string(data)
		info, statErr := os.Stat(resolvedPath)
		if statErr != nil {
			return fmt.Errorf("stat config %s: %w", path, statErr)
		}
		mode = info.Mode().Perm()
	case os.IsNotExist(err):
	default:
		return fmt.Errorf("reading config %s: %w", path, err)
	}

	if contents != "" {
		if err := validateNTMConfigTOML(contents); err != nil {
			return fmt.Errorf("refusing to update %s: existing config is invalid: %w", path, err)
		}
	}

	updated, err := upsertTOMLKeys(contents, sectionParts, keys)
	if err != nil {
		return fmt.Errorf("updating config %s: %w", path, err)
	}
	if err := validateNTMConfigTOML(updated); err != nil {
		return fmt.Errorf("refusing to write %s: updated config is invalid: %w", path, err)
	}

	return util.AtomicWriteFile(resolvedPath, []byte(updated), mode)
}

// upsertTOMLKeys applies per-key replacement/insertion inside [section],
// keeping all other lines (comments included) untouched.
func upsertTOMLKeys(contents string, sectionParts []string, keys [][2]string) (string, error) {
	lines := []string{}
	if contents != "" {
		lines = strings.Split(contents, "\n")
	}
	lineInfo := scanTOMLLines(lines)
	insertedLineSuffix := ""
	if strings.Contains(contents, "\r\n") {
		insertedLineSuffix = "\r"
	}

	header := "[" + strings.Join(sectionParts, ".") + "]"
	start := -1
	end := len(lines)
	for i, line := range lines {
		info := lineInfo[i]
		if info.startsInMultiline {
			continue
		}
		if start == -1 {
			if tomlHeaderMatches(line, info.commentAt, sectionParts) {
				start = i
			}
			continue
		}
		if isTOMLTableHeader(line, info.commentAt) {
			end = i
			break
		}
	}

	if start == -1 {
		updatedLines, handled, err := upsertRootDottedTOMLKeys(
			lines, lineInfo, sectionParts, keys, insertedLineSuffix,
		)
		if err != nil {
			return "", err
		}
		if handled {
			out := strings.Join(updatedLines, "\n")
			if !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return out, nil
		}

		// Section absent: append it (preceded by a blank separator line).
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 0 {
			lines = append(lines, insertedLineSuffix)
		}
		lines = append(lines, header+insertedLineSuffix)
		for _, kv := range keys {
			lines = append(lines, kv[0]+" = "+kv[1]+insertedLineSuffix)
		}
		return strings.Join(lines, "\n") + "\n", nil
	}

	type assignmentLocation struct {
		line      int
		equalsAt  int
		commentAt int
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, kv := range keys {
		wanted[kv[0]] = struct{}{}
	}
	locations := make(map[string]assignmentLocation, len(keys))
	for i := start + 1; i < end; i++ {
		info := lineInfo[i]
		if info.startsInMultiline {
			continue
		}
		key, equalsAt, ok := tomlAssignmentKey(lines[i], info.mask)
		if !ok {
			continue
		}
		if _, tracked := wanted[key]; !tracked {
			continue
		}
		locations[key] = assignmentLocation{line: i, equalsAt: equalsAt, commentAt: info.commentAt}
	}

	missing := make([]string, 0, len(keys))
	for _, kv := range keys {
		location, found := locations[kv[0]]
		if !found {
			missing = append(missing, kv[0]+" = "+kv[1]+insertedLineSuffix)
			continue
		}
		lines[location.line] = replaceTOMLAssignmentValue(
			lines[location.line], location.equalsAt, location.commentAt, kv[1],
		)
	}
	if len(missing) > 0 {
		insertAt := start + 1
		lines = append(lines, make([]string, len(missing))...)
		copy(lines[insertAt+len(missing):], lines[insertAt:len(lines)-len(missing)])
		copy(lines[insertAt:], missing)
	}

	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// upsertRootDottedTOMLKeys updates a section represented with root dotted
// assignments, for example `coordinator.auto_assign = false`. Once a section
// uses dotted assignments, missing requested keys are added in the same style
// before the first table header. A whole-section root assignment (normally an
// inline table) cannot be edited surgically without re-encoding its value, so
// it is rejected explicitly.
func upsertRootDottedTOMLKeys(
	lines []string,
	lineInfo []tomlLineInfo,
	sectionParts []string,
	keys [][2]string,
	insertedLineSuffix string,
) ([]string, bool, error) {
	type assignmentLocation struct {
		line      int
		equalsAt  int
		commentAt int
	}

	wanted := make(map[string]struct{}, len(keys))
	for _, kv := range keys {
		wanted[kv[0]] = struct{}{}
	}

	rootEnd := len(lines)
	dottedStyle := false
	locations := make(map[string]assignmentLocation, len(keys))
	for i, line := range lines {
		info := lineInfo[i]
		if info.startsInMultiline {
			continue
		}
		if isTOMLTableHeader(line, info.commentAt) {
			rootEnd = i
			break
		}

		path, equalsAt, ok := tomlAssignmentPath(line, info.mask)
		if !ok {
			continue
		}
		if len(path) <= len(sectionParts) && tomlPathHasPrefix(sectionParts, path) {
			return nil, false, fmt.Errorf(
				"section [%s] is encoded as root assignment %q; cannot surgically update a whole-section or parent inline value",
				strings.Join(sectionParts, "."), strings.Join(path, "."),
			)
		}
		if !tomlPathHasPrefix(path, sectionParts) || len(path) <= len(sectionParts) {
			continue
		}

		dottedStyle = true
		if len(path) != len(sectionParts)+1 {
			continue
		}
		key := path[len(sectionParts)]
		if _, tracked := wanted[key]; tracked {
			locations[key] = assignmentLocation{line: i, equalsAt: equalsAt, commentAt: info.commentAt}
		}
	}

	if !dottedStyle {
		return lines, false, nil
	}

	missing := make([]string, 0, len(keys))
	dottedSection := strings.Join(sectionParts, ".")
	for _, kv := range keys {
		location, found := locations[kv[0]]
		if !found {
			missing = append(missing, dottedSection+"."+kv[0]+" = "+kv[1]+insertedLineSuffix)
			continue
		}
		lines[location.line] = replaceTOMLAssignmentValue(
			lines[location.line], location.equalsAt, location.commentAt, kv[1],
		)
	}

	if len(missing) > 0 {
		insertAt := rootEnd
		for insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		lines = append(lines, make([]string, len(missing))...)
		copy(lines[insertAt+len(missing):], lines[insertAt:len(lines)-len(missing)])
		copy(lines[insertAt:], missing)
	}

	return lines, true, nil
}

func tomlPathHasPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

func validateTOMLBarePath(value, kind string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("%s is empty", kind)
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("%s %q contains an empty component", kind, value)
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '-' {
				continue
			}
			return nil, fmt.Errorf("%s %q is not a bare TOML key", kind, value)
		}
	}
	return parts, nil
}

func validateRenderedTOMLValue(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("config value for %q is empty", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("config value for %q must be one single-line TOML literal", key)
	}
	info := scanTOMLLines([]string{value})[0]
	if info.commentAt >= 0 {
		return fmt.Errorf("config value for %q must not contain a TOML comment", key)
	}

	const probe = "__ntm_persist_value__"
	var parsed map[string]interface{}
	if _, err := toml.Decode(probe+" = "+value+"\n", &parsed); err != nil {
		return fmt.Errorf("config value for %q is not a TOML literal: %w", key, err)
	}
	if len(parsed) != 1 {
		return fmt.Errorf("config value for %q must contain exactly one TOML literal", key)
	}
	if _, ok := parsed[probe]; !ok {
		return fmt.Errorf("config value for %q must contain exactly one TOML literal", key)
	}
	return nil
}

func resolveConfigPersistencePath(path string) (string, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		resolved, evalErr := filepath.EvalSymlinks(path)
		if evalErr != nil {
			return "", fmt.Errorf("resolving config symlink %s: %w", path, evalErr)
		}
		return resolved, nil
	case err == nil:
		return path, nil
	case os.IsNotExist(err):
		return path, nil
	default:
		return "", fmt.Errorf("inspecting config %s: %w", path, err)
	}
}

func validateNTMConfigTOML(contents string) error {
	var cfg Config
	md, err := toml.Decode(contents, &cfg)
	if err != nil {
		return fmt.Errorf("parsing TOML: %w", err)
	}
	if fields := undecodedConfigFields(md); len(fields) > 0 {
		// Same partition as the strict loader (WS6-remove-finalize): removed
		// knobs get their disposition text (what to delete and why); genuinely
		// unknown fields keep the unknown-field error. The v1.28.0 deprecated
		// batch (bd-6otuk) is warn-tier: tolerated here exactly as the strict
		// loader tolerates it, so `ntm config set` on a config carrying a
		// deprecated key still persists (the warning fires on every load).
		removed, _, unknown := classifyUndecodedKeys(fields)
		if len(removed) > 0 || len(unknown) > 0 {
			var msgs []string
			if len(unknown) > 0 {
				msgs = append(msgs, "unknown field(s): "+strings.Join(unknown, ", "))
			}
			msgs = append(msgs, removedKnobErrorLines(removed)...)
			return fmt.Errorf("%s", strings.Join(msgs, "\n"))
		}
	}
	return nil
}

type tomlLexMode uint8

const (
	tomlLexNormal tomlLexMode = iota
	tomlLexBasic
	tomlLexLiteral
	tomlLexMultilineBasic
	tomlLexMultilineLiteral
)

type tomlLineInfo struct {
	mask              string
	commentAt         int
	startsInMultiline bool
}

// scanTOMLLines masks strings and comments without changing byte offsets. The
// source has already passed the TOML parser, so this scanner only needs to
// identify which bytes are structural; it does not attempt to validate TOML.
func scanTOMLLines(lines []string) []tomlLineInfo {
	infos := make([]tomlLineInfo, len(lines))
	mode := tomlLexNormal
	for lineIndex, line := range lines {
		masked := []byte(line)
		info := tomlLineInfo{commentAt: -1}
		info.startsInMultiline = mode == tomlLexMultilineBasic || mode == tomlLexMultilineLiteral
		for i := 0; i < len(line); {
			switch mode {
			case tomlLexNormal:
				switch line[i] {
				case '#':
					info.commentAt = i
					for j := i; j < len(masked); j++ {
						masked[j] = ' '
					}
					i = len(line)
				case '"':
					if strings.HasPrefix(line[i:], `"""`) {
						for j := 0; j < 3; j++ {
							masked[i+j] = ' '
						}
						i += 3
						mode = tomlLexMultilineBasic
					} else {
						masked[i] = ' '
						i++
						mode = tomlLexBasic
					}
				case '\'':
					if strings.HasPrefix(line[i:], `'''`) {
						for j := 0; j < 3; j++ {
							masked[i+j] = ' '
						}
						i += 3
						mode = tomlLexMultilineLiteral
					} else {
						masked[i] = ' '
						i++
						mode = tomlLexLiteral
					}
				default:
					i++
				}
			case tomlLexBasic:
				masked[i] = ' '
				if line[i] == '\\' && i+1 < len(line) {
					masked[i+1] = ' '
					i += 2
				} else {
					if line[i] == '"' {
						mode = tomlLexNormal
					}
					i++
				}
			case tomlLexLiteral:
				masked[i] = ' '
				if line[i] == '\'' {
					mode = tomlLexNormal
				}
				i++
			case tomlLexMultilineBasic:
				if line[i] == '\\' && i+1 < len(line) {
					masked[i], masked[i+1] = ' ', ' '
					i += 2
					continue
				}
				if line[i] == '"' {
					run := 1
					for i+run < len(line) && line[i+run] == '"' {
						run++
					}
					for j := 0; j < run; j++ {
						masked[i+j] = ' '
					}
					i += run
					if run >= 3 {
						mode = tomlLexNormal
					}
					continue
				}
				masked[i] = ' '
				i++
			case tomlLexMultilineLiteral:
				if line[i] == '\'' {
					run := 1
					for i+run < len(line) && line[i+run] == '\'' {
						run++
					}
					for j := 0; j < run; j++ {
						masked[i+j] = ' '
					}
					i += run
					if run >= 3 {
						mode = tomlLexNormal
					}
					continue
				}
				masked[i] = ' '
				i++
			}
		}
		if mode == tomlLexBasic || mode == tomlLexLiteral {
			mode = tomlLexNormal
		}
		info.mask = string(masked)
		infos[lineIndex] = info
	}
	return infos
}

func isTOMLTableHeader(line string, commentAt int) bool {
	if commentAt >= 0 {
		line = line[:commentAt]
	}
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 3 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']'
}

func tomlHeaderMatches(line string, commentAt int, sectionParts []string) bool {
	if !isTOMLTableHeader(line, commentAt) {
		return false
	}
	if commentAt >= 0 {
		line = line[:commentAt]
	}
	header := strings.TrimSpace(line)
	if strings.HasPrefix(header, "[[") {
		return false
	}
	const probe = "__ntm_persist_probe__"
	var parsed map[string]interface{}
	if _, err := toml.Decode(header+"\n"+probe+" = true\n", &parsed); err != nil {
		return false
	}
	current := parsed
	for _, part := range sectionParts {
		next, ok := current[part].(map[string]interface{})
		if !ok {
			return false
		}
		current = next
	}
	value, ok := current[probe].(bool)
	return ok && value
}

func tomlAssignmentKey(line, mask string) (string, int, bool) {
	path, equalsAt, ok := tomlAssignmentPath(line, mask)
	if !ok || len(path) != 1 {
		return "", 0, false
	}
	return path[0], equalsAt, true
}

func tomlAssignmentPath(line, mask string) ([]string, int, bool) {
	equalsAt := strings.IndexByte(mask, '=')
	if equalsAt < 0 {
		return nil, 0, false
	}
	keyExpr := strings.TrimSpace(line[:equalsAt])
	if keyExpr == "" {
		return nil, 0, false
	}
	var parsed map[string]interface{}
	if _, err := toml.Decode(keyExpr+" = true\n", &parsed); err != nil {
		return nil, 0, false
	}
	path, ok := tomlBooleanProbePath(parsed)
	if !ok {
		return nil, 0, false
	}
	return path, equalsAt, true
}

func tomlBooleanProbePath(values map[string]interface{}) ([]string, bool) {
	if len(values) != 1 {
		return nil, false
	}
	for key, value := range values {
		if flag, ok := value.(bool); ok {
			if !flag {
				return nil, false
			}
			return []string{key}, true
		}
		nested, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		rest, ok := tomlBooleanProbePath(nested)
		if !ok {
			return nil, false
		}
		return append([]string{key}, rest...), true
	}
	return nil, false
}

func replaceTOMLAssignmentValue(line string, equalsAt, commentAt int, value string) string {
	lineSuffix := ""
	if strings.HasSuffix(line, "\r") {
		line = strings.TrimSuffix(line, "\r")
		lineSuffix = "\r"
	}
	valueStart := equalsAt + 1
	for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t') {
		valueStart++
	}
	spacing := line[equalsAt+1 : valueStart]
	if spacing == "" {
		spacing = " "
	}
	suffix := ""
	if commentAt >= 0 {
		suffixStart := commentAt
		for suffixStart > valueStart && (line[suffixStart-1] == ' ' || line[suffixStart-1] == '\t') {
			suffixStart--
		}
		suffix = line[suffixStart:]
	}
	return line[:equalsAt+1] + spacing + value + suffix + lineSuffix
}

func renderPaletteStateTOML(state PaletteState) string {
	return fmt.Sprintf(
		"[palette_state]\n"+
			"pinned = %s\n"+
			"favorites = %s\n",
		renderTOMLStringArray(state.Pinned),
		renderTOMLStringArray(state.Favorites),
	)
}

func renderTOMLStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}

	seen := make(map[string]bool, len(values))
	parts := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		parts = append(parts, strconv.Quote(v))
	}

	if len(parts) == 0 {
		return "[]"
	}
	return "[ " + strings.Join(parts, ", ") + " ]"
}

// Print writes config to a writer in TOML format
func Print(cfg *Config, w io.Writer) error {
	// Write a nicely formatted config file
	fmt.Fprintln(w, "# NTM (Named Tmux Manager) Configuration")
	fmt.Fprintln(w, "# https://github.com/Dicklesworthstone/ntm")
	fmt.Fprintln(w)

	sortedStringMapKeys := func(m map[string]string) []string {
		keys := make([]string, 0, len(m))
		for key := range m {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}
	sortedStringSliceMapKeys := func(m map[string][]string) []string {
		keys := make([]string, 0, len(m))
		for key := range m {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}

	fmt.Fprintf(w, "# Base directory for projects\n")
	fmt.Fprintf(w, "projects_base = %q\n", cfg.ProjectsBase)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# UI Theme (mocha, macchiato, nord, latte, auto)")
	if cfg.Theme != "" {
		fmt.Fprintf(w, "theme = %q\n", cfg.Theme)
	} else {
		fmt.Fprintln(w, "# theme = \"auto\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Help verbosity (minimal, full)")
	if cfg.HelpVerbosity != "" {
		fmt.Fprintf(w, "help_verbosity = %q\n", cfg.HelpVerbosity)
	} else {
		fmt.Fprintln(w, "# help_verbosity = \"full\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Show contextual CLI suggestions")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Path to command palette markdown file (optional)")
	fmt.Fprintln(w, "# If set, loads palette commands from this file instead of [[palette]] entries below")
	fmt.Fprintln(w, "# Searched automatically: ~/.config/ntm/command_palette.md, ./command_palette.md")
	if cfg.PaletteFile != "" {
		fmt.Fprintf(w, "palette_file = %q\n", cfg.PaletteFile)
	} else {
		fmt.Fprintln(w, "# palette_file = \"~/.config/ntm/command_palette.md\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Palette state (favorites/pins)")
	fmt.Fprintln(w, "# Managed by the command palette UI (ntm palette)")
	fmt.Fprintln(w, "[palette_state]")
	if len(cfg.PaletteState.Pinned) > 0 {
		fmt.Fprintf(w, "pinned = %s\n", renderTOMLStringArray(cfg.PaletteState.Pinned))
	} else {
		fmt.Fprintln(w, "# pinned = []")
	}
	if len(cfg.PaletteState.Favorites) > 0 {
		fmt.Fprintf(w, "favorites = %s\n", renderTOMLStringArray(cfg.PaletteState.Favorites))
	} else {
		fmt.Fprintln(w, "# favorites = []")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[agents]")
	fmt.Fprintln(w, "# Commands used to launch each agent type")
	fmt.Fprintf(w, "claude = %q\n", cfg.Agents.Claude)
	fmt.Fprintf(w, "codex = %q\n", cfg.Agents.Codex)
	fmt.Fprintf(w, "gemini = %q\n", cfg.Agents.Gemini)
	fmt.Fprintf(w, "grok = %q\n", cfg.Agents.Grok)
	if cfg.Agents.Antigravity != "" {
		fmt.Fprintf(w, "antigravity = %q\n", cfg.Agents.Antigravity)
	}
	if cfg.Agents.Cursor != "" {
		fmt.Fprintf(w, "cursor = %q\n", cfg.Agents.Cursor)
	}
	if cfg.Agents.Windsurf != "" {
		fmt.Fprintf(w, "windsurf = %q\n", cfg.Agents.Windsurf)
	}
	if cfg.Agents.Aider != "" {
		fmt.Fprintf(w, "aider = %q\n", cfg.Agents.Aider)
	}
	if cfg.Agents.Opencode != "" {
		fmt.Fprintf(w, "oc = %q\n", cfg.Agents.Opencode)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[tmux]")
	fmt.Fprintln(w, "# Tmux-specific settings")
	fmt.Fprintf(w, "default_panes = %d\n", cfg.Tmux.DefaultPanes)
	fmt.Fprintf(w, "pane_init_delay_ms = %d  # Delay before send-keys to new panes\n", cfg.Tmux.PaneInitDelayMs)
	fmt.Fprintf(w, "history_limit = %d       # Scrollback buffer lines per pane\n", cfg.Tmux.HistoryLimit)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[robot]")
	fmt.Fprintln(w, "# Robot output defaults (JSON/TOON)")
	if cfg.Robot.Verbosity != "" {
		fmt.Fprintf(w, "verbosity = %q\n", cfg.Robot.Verbosity)
	} else {
		fmt.Fprintln(w, "# verbosity = \"default\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[robot.output]")
	fmt.Fprintln(w, "# Robot output format settings")
	if cfg.Robot.Output.Format != "" {
		fmt.Fprintf(w, "format = %q\n", cfg.Robot.Output.Format)
	} else {
		fmt.Fprintln(w, "# format = \"json\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[agent_mail]")
	fmt.Fprintln(w, "# Agent Mail server settings for multi-agent coordination")
	fmt.Fprintln(w, "# Environment variables: AGENT_MAIL_URL, AGENT_MAIL_TOKEN, AGENT_MAIL_ENABLED")
	fmt.Fprintf(w, "enabled = %t\n", cfg.AgentMail.Enabled)
	fmt.Fprintf(w, "url = %q\n", cfg.AgentMail.URL)
	if cfg.AgentMail.Token != "" {
		// Mask token in output for security
		fmt.Fprintf(w, "token = \"********\"  # Token is masked. Set AGENT_MAIL_TOKEN env var or edit this file to update.\n")
	} else {
		fmt.Fprintln(w, "# token = \"\"  # Or set AGENT_MAIL_TOKEN env var")
	}
	fmt.Fprintf(w, "auto_register = %t\n", cfg.AgentMail.AutoRegister)
	fmt.Fprintln(w, "# If true, ntm starts/stops `am serve-http` for session monitors.")
	fmt.Fprintln(w, "# Default false prevents ntm from hijacking a user-owned Agent Mail server.")
	fmt.Fprintf(w, "supervisor_enabled = %t\n", cfg.AgentMail.SupervisorEnabledOrDefault())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations]")
	fmt.Fprintln(w, "# External tool integrations (dcg, caam, caut, etc.)")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.dcg]")
	fmt.Fprintln(w, "# Destructive Command Guard (dcg) settings")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Integrations.DCG.Enabled)
	if cfg.Integrations.DCG.BinaryPath != "" {
		fmt.Fprintf(w, "binary_path = %q\n", cfg.Integrations.DCG.BinaryPath)
	} else {
		fmt.Fprintln(w, "# binary_path = \"\"  # Auto-detect from PATH")
	}
	if len(cfg.Integrations.DCG.CustomBlocklist) > 0 {
		fmt.Fprintf(w, "custom_blocklist = %s\n", renderTOMLStringArray(cfg.Integrations.DCG.CustomBlocklist))
	} else {
		fmt.Fprintln(w, "custom_blocklist = []")
	}
	if len(cfg.Integrations.DCG.CustomWhitelist) > 0 {
		fmt.Fprintf(w, "custom_whitelist = %s\n", renderTOMLStringArray(cfg.Integrations.DCG.CustomWhitelist))
	} else {
		fmt.Fprintln(w, "custom_whitelist = []")
	}
	fmt.Fprintln(w, "# dcg_whitelist is legacy; modern DCG handles RCH hook commands directly")
	if cfg.Integrations.DCG.AuditLog != "" {
		fmt.Fprintf(w, "audit_log = %q\n", cfg.Integrations.DCG.AuditLog)
	} else {
		fmt.Fprintln(w, "# audit_log = \"~/.ntm/dcg_audit.log\"")
	}
	fmt.Fprintf(w, "allow_override = %t\n", cfg.Integrations.DCG.AllowOverride)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.caam]")
	fmt.Fprintln(w, "# Coding Agent Account Manager (caam) settings")
	if cfg.Integrations.CAAM.BinaryPath != "" {
		fmt.Fprintf(w, "binary_path = %q\n", cfg.Integrations.CAAM.BinaryPath)
	} else {
		fmt.Fprintln(w, "# binary_path = \"\"  # Auto-detect from PATH")
	}
	fmt.Fprintln(w, "# Coordinator auto-failover on detected rate limits (bd-um3uy).")
	fmt.Fprintln(w, "# Doubly opt-in: auto_failover must be true AND failover_providers non-empty.")
	fmt.Fprintf(w, "auto_failover = %t\n", cfg.Integrations.CAAM.AutoFailover)
	fmt.Fprintf(w, "reset_horizon_minutes = %d  # Only fail over when the reset is further away than this\n", cfg.Integrations.CAAM.ResetHorizonMinutes)
	fmt.Fprintf(w, "failover_providers = %s  # Allow-list (\"claude\", \"openai\", \"gemini\"); empty = none\n", renderTOMLStringArray(cfg.Integrations.CAAM.FailoverProviders))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.rch]")
	fmt.Fprintln(w, "# Remote Compilation Helper (rch) settings")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Integrations.RCH.Enabled)
	if cfg.Integrations.RCH.BinaryPath != "" {
		fmt.Fprintf(w, "binary_path = %q\n", cfg.Integrations.RCH.BinaryPath)
	} else {
		fmt.Fprintln(w, "# binary_path = \"\"  # Auto-detect from PATH")
	}
	fmt.Fprintf(w, "intercept_patterns = %s\n", renderTOMLStringArray(cfg.Integrations.RCH.InterceptPatterns))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.process_triage]")
	fmt.Fprintln(w, "# Process triage (pt) Bayesian process classification settings")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Integrations.ProcessTriage.Enabled)
	fmt.Fprintf(w, "check_interval = %d\n", cfg.Integrations.ProcessTriage.CheckInterval)
	fmt.Fprintf(w, "idle_threshold = %d\n", cfg.Integrations.ProcessTriage.IdleThreshold)
	fmt.Fprintf(w, "stuck_threshold = %d\n", cfg.Integrations.ProcessTriage.StuckThreshold)
	fmt.Fprintf(w, "use_rano_data = %t\n", cfg.Integrations.ProcessTriage.UseRanoData)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.rano]")
	fmt.Fprintln(w, "# rano network observer settings for per-agent API tracking")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Integrations.Rano.Enabled)
	fmt.Fprintf(w, "poll_interval_ms = %d\n", cfg.Integrations.Rano.PollIntervalMs)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.xf]")
	fmt.Fprintln(w, "# X/Twitter archive search (xf) settings")
	fmt.Fprintln(w, "# enabled gates the built-in xf-search palette entry; xf resolves from PATH")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Integrations.XF.Enabled)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[integrations.bv]")
	fmt.Fprintln(w, "# bv (beads_viewer) graph analysis settings; bv resolves from PATH")
	fmt.Fprintln(w, "# Timeout for each bv subprocess (robot-insights cycle check, triage, plan, drift).")
	fmt.Fprintln(w, "# Override per-invocation with NTM_BV_TIMEOUT=<seconds> (env wins).")
	fmt.Fprintf(w, "timeout_seconds = %d\n", cfg.Integrations.BV.TimeoutSeconds)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[safety]")
	fmt.Fprintln(w, "# Safety profile presets that set defaults for multiple knobs")
	fmt.Fprintln(w, "# profile = \"standard\"  # standard|safe|paranoid")
	fmt.Fprintf(w, "profile = %q\n", cfg.Safety.Profile)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[preflight]")
	fmt.Fprintln(w, "# Prompt preflight (lint/validation) defaults")
	fmt.Fprintf(w, "strict = %t\n", cfg.Preflight.Strict)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[redaction]")
	fmt.Fprintln(w, "# Secrets/PII redaction configuration: off|warn|redact|block")
	fmt.Fprintf(w, "mode = %q\n", cfg.Redaction.Mode)
	if len(cfg.Redaction.Allowlist) > 0 {
		fmt.Fprintf(w, "allowlist = %s\n", renderTOMLStringArray(cfg.Redaction.Allowlist))
	} else {
		fmt.Fprintln(w, "allowlist = []")
	}
	if len(cfg.Redaction.DisabledCategories) > 0 {
		fmt.Fprintf(w, "disabled_categories = %s\n", renderTOMLStringArray(cfg.Redaction.DisabledCategories))
	} else {
		fmt.Fprintln(w, "disabled_categories = []")
	}
	fmt.Fprintln(w, "# extra_patterns = { CUSTOM_TOKEN = [\"regex\"] }")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[privacy]")
	fmt.Fprintln(w, "# Privacy mode prevents persistence of sensitive session data")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Privacy.Enabled)
	fmt.Fprintf(w, "disable_prompt_history = %t\n", cfg.Privacy.DisablePromptHistory)
	fmt.Fprintf(w, "disable_event_logs = %t\n", cfg.Privacy.DisableEventLogs)
	fmt.Fprintf(w, "disable_checkpoints = %t\n", cfg.Privacy.DisableCheckpoints)
	fmt.Fprintf(w, "disable_scrollback_capture = %t\n", cfg.Privacy.DisableScrollbackCapture)
	fmt.Fprintf(w, "require_explicit_persist = %t\n", cfg.Privacy.RequireExplicitPersist)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[encryption]")
	fmt.Fprintln(w, "# Encryption-at-rest configuration for persisted artifacts")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Encryption.Enabled)
	fmt.Fprintf(w, "key_source = %q\n", cfg.Encryption.KeySource)
	fmt.Fprintf(w, "key_env = %q\n", cfg.Encryption.KeyEnv)
	if cfg.Encryption.KeyFile != "" {
		fmt.Fprintf(w, "key_file = %q\n", cfg.Encryption.KeyFile)
	} else {
		fmt.Fprintln(w, "# key_file = \"\"")
	}
	if cfg.Encryption.KeyCommand != "" {
		fmt.Fprintf(w, "key_command = %q\n", cfg.Encryption.KeyCommand)
	} else {
		fmt.Fprintln(w, "# key_command = \"\"")
	}
	fmt.Fprintf(w, "key_format = %q\n", cfg.Encryption.KeyFormat)
	if cfg.Encryption.ActiveKeyID != "" {
		fmt.Fprintf(w, "active_key_id = %q\n", cfg.Encryption.ActiveKeyID)
	} else {
		fmt.Fprintln(w, "# active_key_id = \"\"")
	}
	if len(cfg.Encryption.Keyring) > 0 {
		fmt.Fprintln(w, "[encryption.keyring]")
		for _, keyID := range sortedStringMapKeys(cfg.Encryption.Keyring) {
			value := cfg.Encryption.Keyring[keyID]
			fmt.Fprintf(w, "%q = %q\n", keyID, value)
		}
	} else {
		fmt.Fprintln(w, "# [encryption.keyring]")
		fmt.Fprintln(w, "# current = \"<encoded-key-material>\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[send]")
	fmt.Fprintln(w, "# Defaults prepended to outbound send/broadcast prompts")
	if cfg.Send.BasePrompt != "" {
		fmt.Fprintf(w, "base_prompt = %q\n", cfg.Send.BasePrompt)
	} else {
		fmt.Fprintln(w, "# base_prompt = \"\"")
	}
	if cfg.Send.BasePromptFile != "" {
		fmt.Fprintf(w, "base_prompt_file = %q\n", cfg.Send.BasePromptFile)
	} else {
		fmt.Fprintln(w, "# base_prompt_file = \"\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[prompts]")
	fmt.Fprintln(w, "# Per-agent-type default prompts")
	if cfg.Prompts.CCDefault != "" {
		fmt.Fprintf(w, "cc_default = %q\n", cfg.Prompts.CCDefault)
	} else {
		fmt.Fprintln(w, "# cc_default = \"\"")
	}
	if cfg.Prompts.CCDefaultFile != "" {
		fmt.Fprintf(w, "cc_default_file = %q\n", cfg.Prompts.CCDefaultFile)
	} else {
		fmt.Fprintln(w, "# cc_default_file = \"\"")
	}
	if cfg.Prompts.CodDefault != "" {
		fmt.Fprintf(w, "cod_default = %q\n", cfg.Prompts.CodDefault)
	} else {
		fmt.Fprintln(w, "# cod_default = \"\"")
	}
	if cfg.Prompts.CodDefaultFile != "" {
		fmt.Fprintf(w, "cod_default_file = %q\n", cfg.Prompts.CodDefaultFile)
	} else {
		fmt.Fprintln(w, "# cod_default_file = \"\"")
	}
	if cfg.Prompts.GmiDefault != "" {
		fmt.Fprintf(w, "gmi_default = %q\n", cfg.Prompts.GmiDefault)
	} else {
		fmt.Fprintln(w, "# gmi_default = \"\"")
	}
	if cfg.Prompts.GmiDefaultFile != "" {
		fmt.Fprintf(w, "gmi_default_file = %q\n", cfg.Prompts.GmiDefaultFile)
	} else {
		fmt.Fprintln(w, "# gmi_default_file = \"\"")
	}
	if cfg.Prompts.AgyDefault != "" {
		fmt.Fprintf(w, "agy_default = %q\n", cfg.Prompts.AgyDefault)
	} else {
		fmt.Fprintln(w, "# agy_default = \"\"")
	}
	if cfg.Prompts.AgyDefaultFile != "" {
		fmt.Fprintf(w, "agy_default_file = %q\n", cfg.Prompts.AgyDefaultFile)
	} else {
		fmt.Fprintln(w, "# agy_default_file = \"\"")
	}
	fmt.Fprintln(w)

	// Write models configuration
	fmt.Fprintln(w, "[models]")
	fmt.Fprintln(w, "# Default models when no specifier given")
	fmt.Fprintf(w, "default_claude = %q\n", cfg.Models.DefaultClaude)
	fmt.Fprintf(w, "default_codex = %q\n", cfg.Models.DefaultCodex)
	fmt.Fprintf(w, "default_gemini = %q\n", cfg.Models.DefaultGemini)
	fmt.Fprintf(w, "default_grok = %q  # Empty delegates model selection to Grok Build\n", cfg.Models.DefaultGrok)
	fmt.Fprintln(w)

	// Write Claude model aliases
	fmt.Fprintln(w, "[models.claude]")
	fmt.Fprintln(w, "# Claude model aliases (e.g., --cc=2:opus)")
	for _, alias := range sortedStringMapKeys(cfg.Models.Claude) {
		fullName := cfg.Models.Claude[alias]
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write Codex model aliases
	fmt.Fprintln(w, "[models.codex]")
	fmt.Fprintln(w, "# Codex model aliases (e.g., --cod=2:max)")
	for _, alias := range sortedStringMapKeys(cfg.Models.Codex) {
		fullName := cfg.Models.Codex[alias]
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write Gemini model aliases
	fmt.Fprintln(w, "[models.gemini]")
	fmt.Fprintln(w, "# Gemini model aliases (e.g., --gmi=1:flash)")
	for _, alias := range sortedStringMapKeys(cfg.Models.Gemini) {
		fullName := cfg.Models.Gemini[alias]
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Grok Build model availability is account- and release-dependent, so the
	// alias table is intentionally empty unless the operator configures it.
	fmt.Fprintln(w, "[models.grok]")
	fmt.Fprintln(w, "# Grok Build model aliases (e.g., --grok=1:MODEL_ID)")
	for _, alias := range sortedStringMapKeys(cfg.Models.Grok) {
		fullName := cfg.Models.Grok[alias]
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write alerts configuration
	fmt.Fprintln(w, "[alerts]")
	fmt.Fprintln(w, "# Alert system configuration for proactive problem detection")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Alerts.Enabled)
	fmt.Fprintf(w, "agent_stuck_minutes = %d    # Minutes without output before alerting\n", cfg.Alerts.AgentStuckMinutes)
	fmt.Fprintf(w, "disk_low_threshold_gb = %.1f  # Minimum free disk space (GB)\n", cfg.Alerts.DiskLowThresholdGB)
	fmt.Fprintf(w, "disk_full_horizon_hours = %.1f # Alert when disk is projected full within this many hours (0 = disabled)\n", cfg.Alerts.DiskFullHorizonHours)
	fmt.Fprintf(w, "mail_backlog_threshold = %d  # Unread messages before alerting\n", cfg.Alerts.MailBacklogThreshold)
	fmt.Fprintf(w, "bead_stale_hours = %d       # Hours before in-progress bead is stale\n", cfg.Alerts.BeadStaleHours)
	fmt.Fprintf(w, "context_warning_threshold = %.1f # Context usage percentage that triggers a warning\n", cfg.Alerts.ContextWarningThreshold)
	fmt.Fprintf(w, "resolved_prune_minutes = %d # How long to keep resolved alerts\n", cfg.Alerts.ResolvedPruneMinutes)
	fmt.Fprintln(w)

	// Write checkpoints configuration
	fmt.Fprintln(w, "[checkpoints]")
	fmt.Fprintln(w, "# Automatic checkpoint configuration for risky operations")
	fmt.Fprintf(w, "enabled = %t                    # Top-level toggle for auto-checkpoints\n", cfg.Checkpoints.Enabled)
	fmt.Fprintf(w, "before_broadcast = %t           # Auto-checkpoint before sending to all agents\n", cfg.Checkpoints.BeforeBroadcast)
	fmt.Fprintf(w, "before_add_agents = %d            # Auto-checkpoint when adding >= N agents (0 = disabled)\n", cfg.Checkpoints.BeforeAddAgents)
	fmt.Fprintf(w, "max_auto_checkpoints = %d        # Max auto-checkpoints per session (rotation)\n", cfg.Checkpoints.MaxAutoCheckpoints)
	fmt.Fprintf(w, "scrollback_lines = %d           # Lines of scrollback to capture\n", cfg.Checkpoints.ScrollbackLines)
	fmt.Fprintf(w, "include_git = %t               # Capture git state in auto-checkpoints\n", cfg.Checkpoints.IncludeGit)
	fmt.Fprintln(w)

	// Write notifications configuration
	fmt.Fprintln(w, "[notifications]")
	fmt.Fprintln(w, "# Notification system for agent events (errors, crashes, rate limits)")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.Enabled)
	// Serialize events as TOML array for validity
	eventItems := make([]string, 0, len(cfg.Notifications.Events))
	for _, e := range cfg.Notifications.Events {
		eventItems = append(eventItems, fmt.Sprintf("\"%s\"", e))
	}
	fmt.Fprintf(w, "events = [%s]  # Events to notify on\n", strings.Join(eventItems, ", "))
	fmt.Fprintf(w, "primary = %q\n", cfg.Notifications.Primary)
	fmt.Fprintf(w, "fallback = %q\n", cfg.Notifications.Fallback)
	fmt.Fprintln(w)

	if len(cfg.Notifications.Routing) > 0 {
		fmt.Fprintln(w, "[notifications.routing]")
		for _, key := range sortedStringSliceMapKeys(cfg.Notifications.Routing) {
			fmt.Fprintf(w, "%q = %s\n", key, renderTOMLStringArray(cfg.Notifications.Routing[key]))
		}
	} else {
		fmt.Fprintln(w, "# [notifications.routing]")
		fmt.Fprintln(w, "# \"agent.crashed\" = [ \"desktop\", \"filebox\" ]")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[notifications.desktop]")
	fmt.Fprintln(w, "# Desktop notifications (macOS/Linux)")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.Desktop.Enabled)
	fmt.Fprintf(w, "title = %q  # Default notification title\n", cfg.Notifications.Desktop.Title)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[notifications.webhook]")
	fmt.Fprintln(w, "# Webhook notifications (Slack, Discord, etc.)")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.Webhook.Enabled)
	if cfg.Notifications.Webhook.URL != "" {
		fmt.Fprintf(w, "url = %q\n", cfg.Notifications.Webhook.URL)
	} else {
		fmt.Fprintln(w, "# url = \"https://hooks.slack.com/...\"")
	}
	fmt.Fprintf(w, "method = %q\n", cfg.Notifications.Webhook.Method)
	fmt.Fprintf(w, "template = %q\n", cfg.Notifications.Webhook.Template)
	if len(cfg.Notifications.Webhook.Headers) > 0 {
		fmt.Fprintln(w, "[notifications.webhook.headers]")
		for _, key := range sortedStringMapKeys(cfg.Notifications.Webhook.Headers) {
			fmt.Fprintf(w, "%q = %q\n", key, cfg.Notifications.Webhook.Headers[key])
		}
	} else {
		fmt.Fprintln(w, "# [notifications.webhook.headers]")
		fmt.Fprintln(w, "# Authorization = \"Bearer <token>\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[notifications.shell]")
	fmt.Fprintln(w, "# Shell command notifications")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.Shell.Enabled)
	if cfg.Notifications.Shell.Command != "" {
		fmt.Fprintf(w, "command = %q\n", cfg.Notifications.Shell.Command)
	} else {
		fmt.Fprintln(w, "# command = \"~/bin/notify.sh\"")
	}
	fmt.Fprintf(w, "pass_json = %t  # Pass event as JSON to stdin\n", cfg.Notifications.Shell.PassJSON)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[notifications.log]")
	fmt.Fprintln(w, "# Log file notifications")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.Log.Enabled)
	fmt.Fprintf(w, "path = %q\n", cfg.Notifications.Log.Path)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[notifications.filebox]")
	fmt.Fprintln(w, "# File inbox notifications for offline review")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Notifications.FileBox.Enabled)
	fmt.Fprintf(w, "path = %q\n", cfg.Notifications.FileBox.Path)
	fmt.Fprintln(w)

	// Write resilience configuration
	fmt.Fprintln(w, "[resilience]")
	fmt.Fprintln(w, "# Agent auto-restart and recovery configuration")
	fmt.Fprintf(w, "auto_restart = %t           # Enable automatic agent restart on crash\n", cfg.Resilience.AutoRestart)
	fmt.Fprintf(w, "max_restarts = %d            # Max restarts per agent before giving up\n", cfg.Resilience.MaxRestarts)
	fmt.Fprintf(w, "restart_delay_seconds = %d  # Seconds to wait before restarting\n", cfg.Resilience.RestartDelaySeconds)
	fmt.Fprintf(w, "health_check_seconds = %d   # Seconds between health checks\n", cfg.Resilience.HealthCheckSeconds)
	fmt.Fprintf(w, "crash_threshold = %d        # Consecutive failures before restart\n", cfg.Resilience.CrashThreshold)
	fmt.Fprintf(w, "notify_on_crash = %t       # Send notification when agent crashes\n", cfg.Resilience.NotifyOnCrash)
	fmt.Fprintf(w, "notify_on_max_restarts = %t # Notify when max restarts exceeded\n", cfg.Resilience.NotifyOnMaxRestarts)
	fmt.Fprintln(w)

	// Write rate limit sub-configuration
	fmt.Fprintln(w, "[resilience.rate_limit]")
	fmt.Fprintln(w, "# Rate limit detection configuration")
	fmt.Fprintf(w, "detect = %t   # Enable rate limit detection\n", cfg.Resilience.RateLimit.Detect)
	fmt.Fprintf(w, "notify = %t   # Send notification on rate limit\n", cfg.Resilience.RateLimit.Notify)
	fmt.Fprintln(w)

	// Write rotation configuration
	fmt.Fprintln(w, "[rotation]")
	fmt.Fprintln(w, "# Account rotation and restart configuration")
	fmt.Fprintf(w, "enabled = %t               # Top-level toggle\n", cfg.Rotation.Enabled)
	fmt.Fprintf(w, "auto_open_browser = %t     # Auto-open browser for auth\n", cfg.Rotation.AutoOpenBrowser)
	fmt.Fprintf(w, "auto_trigger = %t          # Show notification when rate limit detected\n", cfg.Rotation.AutoTrigger)
	fmt.Fprintf(w, "auto_initiate = %t         # Automatically start rotation when possible\n", cfg.Rotation.AutoInitiate)
	fmt.Fprintf(w, "usage_percent_threshold = %.1f  # >0 enables coordinator context-rotation trigger on transcript-sourced usage %% (0 = off)\n", cfg.Rotation.UsagePercentThreshold)
	fmt.Fprintf(w, "auto_confirm = %t          # Auto-execute context rotations enqueued by the usage trigger\n", cfg.Rotation.AutoConfirm)
	fmt.Fprintf(w, "continuation_prompt = %q\n", cfg.Rotation.ContinuationPrompt)
	fmt.Fprintln(w)

	if len(cfg.Rotation.Accounts) > 0 {
		for _, acct := range cfg.Rotation.Accounts {
			fmt.Fprintln(w, "[[rotation.accounts]]")
			fmt.Fprintf(w, "provider = %q\n", acct.Provider)
			fmt.Fprintf(w, "email = %q\n", acct.Email)
			fmt.Fprintf(w, "alias = %q\n", acct.Alias)
			fmt.Fprintln(w)
		}
	} else {
		fmt.Fprintln(w, "# [[rotation.accounts]]")
		fmt.Fprintln(w, "# provider = \"claude\"")
		fmt.Fprintln(w, "# email = \"primary@example.com\"")
		fmt.Fprintln(w, "# alias = \"main\"")
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "[rotation.thresholds]")
	fmt.Fprintf(w, "warning_percent = %d        # Show warning at this quota %%\n", cfg.Rotation.Thresholds.WarningPercent)
	fmt.Fprintf(w, "critical_percent = %d       # Consider limited at this %%\n", cfg.Rotation.Thresholds.CriticalPercent)
	fmt.Fprintf(w, "restart_if_tokens_above = %.0f  # Restart if tokens exceed this\n", cfg.Rotation.Thresholds.RestartIfTokensAbove)
	fmt.Fprintf(w, "restart_if_session_hours = %d   # Restart after N hours\n", cfg.Rotation.Thresholds.RestartIfSessionHours)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[scanner]")
	fmt.Fprintln(w, "# UBS scanner configuration")
	if cfg.Scanner.UBSPath != "" {
		fmt.Fprintf(w, "ubs_path = %q\n", cfg.Scanner.UBSPath)
	} else {
		fmt.Fprintln(w, "# ubs_path = \"\"  # Auto-detect from PATH")
	}
	fmt.Fprintln(w)

	// The [scanner.defaults]/[scanner.thresholds.*]/[scanner.tools]/
	// [scanner.beads]/[scanner.notifications] TOML tables were deprecated in
	// v1.28.0 (bd-6otuk): only scanner.ubs_path has a runtime reader. They are
	// intentionally not printed so effective/default output cannot reintroduce
	// deprecated keys.

	fmt.Fprintln(w, "[bugs]")
	fmt.Fprintln(w, "# UBS bug push routing (ntm bugs watch)")
	fmt.Fprintf(w, "push_routing = %t  # Route NEW findings to reservation holders (opt-in)\n", cfg.Bugs.PushRouting)
	fmt.Fprintf(w, "interval = %q  # Scan interval for 'ntm bugs watch'\n", cfg.Bugs.EffectiveInterval().String())
	fmt.Fprintf(w, "cooldown_minutes = %d  # Minimum minutes between bug nudges to the same pane\n", cfg.Bugs.CooldownMinutes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[cass]")
	fmt.Fprintln(w, "# CASS (Coding Agent Session Search) configuration")
	fmt.Fprintf(w, "enabled = %t\n", cfg.CASS.Enabled)
	fmt.Fprintf(w, "timeout = %d\n", cfg.CASS.Timeout)
	if cfg.CASS.BinaryPath != "" {
		fmt.Fprintf(w, "binary_path = %q\n", cfg.CASS.BinaryPath)
	} else {
		fmt.Fprintln(w, "# binary_path = \"\"  # Auto-detect from PATH")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[cass.context]")
	fmt.Fprintln(w, "# Automatic CASS context injection settings")
	fmt.Fprintln(w, "# Environment variables: NTM_CASS_CONTEXT_ENABLED, NTM_CASS_MIN_RELEVANCE,")
	fmt.Fprintln(w, "#   NTM_CASS_SKIP_IF_CONTEXT_ABOVE, NTM_CASS_PREFER_SAME_PROJECT")
	fmt.Fprintf(w, "enabled = %t                # Auto-inject context when spawning (--with-cass/--no-cass)\n", cfg.CASS.Context.Enabled)
	fmt.Fprintf(w, "max_sessions = %d            # Max past sessions to include\n", cfg.CASS.Context.MaxSessions)
	fmt.Fprintf(w, "lookback_days = %d          # How far back to search\n", cfg.CASS.Context.LookbackDays)
	fmt.Fprintf(w, "max_tokens = %d            # Token budget for context\n", cfg.CASS.Context.MaxTokens)
	fmt.Fprintf(w, "min_relevance = %.2f        # Minimum relevance score (0.0-1.0)\n", cfg.CASS.Context.MinRelevance)
	fmt.Fprintf(w, "skip_if_context_above = %.0f  # Skip if context usage > this %% (0-100)\n", cfg.CASS.Context.SkipIfContextAbove)
	fmt.Fprintf(w, "prefer_same_project = %t   # Prefer results from same project\n", cfg.CASS.Context.PreferSameProject)
	fmt.Fprintln(w)

	// Write Gemini setup configuration
	fmt.Fprintln(w, "[gemini_setup]")
	fmt.Fprintln(w, "# Gemini CLI post-spawn setup configuration")
	fmt.Fprintln(w, "# When enabled, NTM automatically selects the Pro model after spawning Gemini agents")
	fmt.Fprintf(w, "auto_select_pro_model = %t       # Auto-select Pro model (Gemini 3) on spawn\n", cfg.GeminiSetup.AutoSelectProModel)
	fmt.Fprintf(w, "ready_timeout_seconds = %d       # Seconds to wait for Gemini CLI to be ready\n", cfg.GeminiSetup.ReadyTimeoutSeconds)
	fmt.Fprintf(w, "model_select_timeout_seconds = %d # Seconds to wait for model selection menu\n", cfg.GeminiSetup.ModelSelectTimeoutSeconds)
	fmt.Fprintf(w, "verbose = %t                     # Show debug output during setup\n", cfg.GeminiSetup.Verbose)
	fmt.Fprintln(w)

	// Write context pack options
	fmt.Fprintln(w, "[context]")
	fmt.Fprintln(w, "# Context pack composition options")
	fmt.Fprintf(w, "ms_skills = %t                  # Include Meta Skill suggestions in context packs\n", cfg.Context.MSSkills)
	fmt.Fprintln(w)

	// Write context rotation configuration
	fmt.Fprintln(w, "[context_rotation]")
	fmt.Fprintln(w, "# Context window rotation configuration")
	fmt.Fprintln(w, "# Monitors agent context usage and rotates before exhaustion")
	fmt.Fprintf(w, "enabled = %t                    # Top-level toggle for context rotation\n", cfg.ContextRotation.Enabled)
	fmt.Fprintf(w, "warning_threshold = %.2f        # Warn when context usage exceeds this (0.0-1.0)\n", cfg.ContextRotation.WarningThreshold)
	fmt.Fprintf(w, "rotate_threshold = %.2f         # Rotate agent when usage exceeds this (0.0-1.0)\n", cfg.ContextRotation.RotateThreshold)
	fmt.Fprintf(w, "summary_max_tokens = %d        # Max tokens for handoff summary\n", cfg.ContextRotation.SummaryMaxTokens)
	fmt.Fprintf(w, "min_session_age_sec = %d        # Don't rotate agents younger than this\n", cfg.ContextRotation.MinSessionAgeSec)
	fmt.Fprintf(w, "try_compact_first = %t         # Try to compact before rotating\n", cfg.ContextRotation.TryCompactFirst)
	fmt.Fprintf(w, "require_confirm = %t           # Require user confirmation before rotating\n", cfg.ContextRotation.RequireConfirm)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[recovery]")
	fmt.Fprintln(w, "# Smart session recovery context injection defaults")
	fmt.Fprintf(w, "enabled = %t\n", cfg.SessionRecovery.Enabled)
	fmt.Fprintf(w, "include_agent_mail = %t\n", cfg.SessionRecovery.IncludeAgentMail)
	fmt.Fprintf(w, "include_cm_memories = %t\n", cfg.SessionRecovery.IncludeCMMemories)
	fmt.Fprintf(w, "include_beads_context = %t\n", cfg.SessionRecovery.IncludeBeadsContext)
	fmt.Fprintf(w, "max_recovery_tokens = %d\n", cfg.SessionRecovery.MaxRecoveryTokens)
	fmt.Fprintf(w, "auto_inject_on_spawn = %t\n", cfg.SessionRecovery.AutoInjectOnSpawn)
	fmt.Fprintf(w, "max_cm_rules = %d\n", cfg.SessionRecovery.MaxCMRules)
	fmt.Fprintf(w, "max_cm_snippets = %d\n", cfg.SessionRecovery.MaxCMSnippets)
	fmt.Fprintf(w, "timeout_seconds = %d        # Seconds to gather recovery sources before degrading to partial recovery\n", cfg.SessionRecovery.TimeoutSeconds)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[cleanup]")
	fmt.Fprintln(w, "# Automatic temp-file cleanup defaults")
	fmt.Fprintf(w, "auto_clean_on_startup = %t\n", cfg.Cleanup.AutoCleanOnStartup)
	fmt.Fprintf(w, "max_age_hours = %d\n", cfg.Cleanup.MaxAgeHours)
	fmt.Fprintf(w, "verbose = %t\n", cfg.Cleanup.Verbose)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[assign]")
	fmt.Fprintln(w, "# Default ntm assign strategy")
	fmt.Fprintf(w, "strategy = %q\n", cfg.Assign.Strategy)
	fmt.Fprintln(w, "# Default bulk-assign dispatch prompt (inline). Empty = built-in template.")
	fmt.Fprintln(w, "# Placeholders: {bead_id} {bead_title} {bead_type} {bead_deps} {session} {pane}")
	fmt.Fprintf(w, "prompt_template = %q\n", cfg.Assign.PromptTemplate)
	fmt.Fprintln(w, "# File holding the default bulk-assign dispatch prompt (takes precedence over prompt_template).")
	fmt.Fprintf(w, "prompt_template_file = %q\n", cfg.Assign.PromptTemplateFile)
	fmt.Fprintln(w, "# Extra labels (merged with the built-in operator-gate vocabulary) that block automated assignment.")
	fmt.Fprintf(w, "operator_gated_labels = %s\n", renderTOMLStringArray(cfg.Assign.OperatorGatedLabels))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[spawn_pacing]")
	fmt.Fprintln(w, "# Spawn admission control (concurrency caps)")
	fmt.Fprintf(w, "enabled = %t\n", cfg.SpawnPacing.Enabled)
	fmt.Fprintf(w, "max_concurrent_spawns = %d\n", cfg.SpawnPacing.MaxConcurrentSpawns)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[spawn_pacing.agent_caps]")
	fmt.Fprintf(w, "claude_max_concurrent = %d\n", cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent)
	fmt.Fprintf(w, "codex_max_concurrent = %d\n", cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent)
	fmt.Fprintf(w, "gemini_max_concurrent = %d\n", cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[file_reservation]")
	fmt.Fprintln(w, "# Automatic Agent Mail file reservation settings")
	fmt.Fprintf(w, "enabled = %t\n", cfg.FileReservation.Enabled)
	fmt.Fprintf(w, "auto_reserve = %t\n", cfg.FileReservation.AutoReserve)
	fmt.Fprintf(w, "auto_release_idle_minutes = %d\n", cfg.FileReservation.AutoReleaseIdleMin)
	fmt.Fprintf(w, "notify_on_conflict = %t\n", cfg.FileReservation.NotifyOnConflict)
	fmt.Fprintf(w, "extend_on_activity = %t\n", cfg.FileReservation.ExtendOnActivity)
	fmt.Fprintf(w, "default_ttl_minutes = %d\n", cfg.FileReservation.DefaultTTLMin)
	fmt.Fprintf(w, "poll_interval_seconds = %d\n", cfg.FileReservation.PollIntervalSec)
	fmt.Fprintf(w, "capture_lines = %d\n", cfg.FileReservation.CaptureLinesForDetect)
	fmt.Fprintf(w, "debug = %t\n", cfg.FileReservation.Debug)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[memory]")
	fmt.Fprintln(w, "# cass-memory integration defaults")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Memory.Enabled)
	fmt.Fprintf(w, "include_in_recovery = %t\n", cfg.Memory.IncludeInRecovery)
	fmt.Fprintf(w, "max_rules = %d\n", cfg.Memory.MaxRules)
	fmt.Fprintf(w, "query_timeout_seconds = %d\n", cfg.Memory.QueryTimeoutSeconds)
	fmt.Fprintf(w, "send_injection = %t             # Inject rules on robot sends by default (--with-memory)\n", cfg.Memory.SendInjection)
	fmt.Fprintf(w, "send_max_rules = %d\n", cfg.Memory.SendMaxRules)
	fmt.Fprintf(w, "send_budget_tokens = %d\n", cfg.Memory.SendBudgetTokens)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[swarm]")
	fmt.Fprintln(w, "# Multi-project swarm allocation defaults")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Swarm.Enabled)
	fmt.Fprintf(w, "default_scan_dir = %q\n", cfg.Swarm.DefaultScanDir)
	fmt.Fprintf(w, "tier1_threshold = %d\n", cfg.Swarm.Tier1Threshold)
	fmt.Fprintf(w, "tier2_threshold = %d\n", cfg.Swarm.Tier2Threshold)
	fmt.Fprintf(w, "sessions_per_type = %d\n", cfg.Swarm.SessionsPerType)
	fmt.Fprintf(w, "panes_per_session = %d\n", cfg.Swarm.PanesPerSession)
	fmt.Fprintf(w, "stagger_delay_ms = %d\n", cfg.Swarm.StaggerDelayMs)
	fmt.Fprintf(w, "auto_rotate_accounts = %t\n", cfg.Swarm.AutoRotateAccounts)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[swarm.tier1_allocation]")
	fmt.Fprintf(w, "cc = %d\n", cfg.Swarm.Tier1Allocation.CC)
	fmt.Fprintf(w, "cod = %d\n", cfg.Swarm.Tier1Allocation.Cod)
	fmt.Fprintf(w, "gmi = %d\n", cfg.Swarm.Tier1Allocation.Gmi)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[swarm.tier2_allocation]")
	fmt.Fprintf(w, "cc = %d\n", cfg.Swarm.Tier2Allocation.CC)
	fmt.Fprintf(w, "cod = %d\n", cfg.Swarm.Tier2Allocation.Cod)
	fmt.Fprintf(w, "gmi = %d\n", cfg.Swarm.Tier2Allocation.Gmi)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[swarm.tier3_allocation]")
	fmt.Fprintf(w, "cc = %d\n", cfg.Swarm.Tier3Allocation.CC)
	fmt.Fprintf(w, "cod = %d\n", cfg.Swarm.Tier3Allocation.Cod)
	fmt.Fprintf(w, "gmi = %d\n", cfg.Swarm.Tier3Allocation.Gmi)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[ensemble]")
	fmt.Fprintln(w, "# Reasoning ensemble defaults (used when flags are not provided)")
	fmt.Fprintf(w, "default_ensemble = %q\n", cfg.Ensemble.DefaultEnsemble)
	fmt.Fprintf(w, "agent_mix = %q\n", cfg.Ensemble.AgentMix)
	fmt.Fprintf(w, "assignment = %q\n", cfg.Ensemble.Assignment)
	fmt.Fprintf(w, "mode_tier_default = %q  # core|advanced|experimental\n", cfg.Ensemble.ModeTierDefault)
	fmt.Fprintf(w, "allow_advanced = %t\n", cfg.Ensemble.AllowAdvanced)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[ensemble.synthesis]")
	fmt.Fprintln(w, "# Synthesis defaults (strategy + optional filters)")
	if cfg.Ensemble.Synthesis.Strategy != "" {
		fmt.Fprintf(w, "strategy = %q\n", cfg.Ensemble.Synthesis.Strategy)
	} else {
		fmt.Fprintln(w, "# strategy = \"deliberative\"")
	}
	if cfg.Ensemble.Synthesis.MinConfidence > 0 {
		fmt.Fprintf(w, "min_confidence = %.2f\n", cfg.Ensemble.Synthesis.MinConfidence)
	} else {
		fmt.Fprintln(w, "# min_confidence = 0.50")
	}
	if cfg.Ensemble.Synthesis.MaxFindings > 0 {
		fmt.Fprintf(w, "max_findings = %d\n", cfg.Ensemble.Synthesis.MaxFindings)
	} else {
		fmt.Fprintln(w, "# max_findings = 10")
	}
	fmt.Fprintf(w, "include_raw_outputs = %t\n", cfg.Ensemble.Synthesis.IncludeRawOutputs)
	if cfg.Ensemble.Synthesis.ConflictResolution != "" {
		fmt.Fprintf(w, "conflict_resolution = %q\n", cfg.Ensemble.Synthesis.ConflictResolution)
	} else {
		fmt.Fprintln(w, "# conflict_resolution = \"highlight\"")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[ensemble.cache]")
	fmt.Fprintln(w, "# Context pack caching defaults")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Ensemble.Cache.Enabled)
	fmt.Fprintf(w, "ttl_minutes = %d\n", cfg.Ensemble.Cache.TTLMinutes)
	if cfg.Ensemble.Cache.CacheDir != "" {
		fmt.Fprintf(w, "cache_dir = %q\n", cfg.Ensemble.Cache.CacheDir)
	} else {
		fmt.Fprintln(w, "# cache_dir = \"~/.cache/ntm/context-packs\"")
	}
	if cfg.Ensemble.Cache.MaxEntries > 0 {
		fmt.Fprintf(w, "max_entries = %d\n", cfg.Ensemble.Cache.MaxEntries)
	} else {
		fmt.Fprintln(w, "# max_entries = 32")
	}
	fmt.Fprintf(w, "share_across_modes = %t\n", cfg.Ensemble.Cache.ShareAcrossModes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[ensemble.budget]")
	fmt.Fprintln(w, "# Token budget defaults")
	fmt.Fprintf(w, "per_agent = %d\n", cfg.Ensemble.Budget.PerAgent)
	fmt.Fprintf(w, "total = %d\n", cfg.Ensemble.Budget.Total)
	fmt.Fprintf(w, "synthesis = %d\n", cfg.Ensemble.Budget.Synthesis)
	fmt.Fprintf(w, "context_pack = %d\n", cfg.Ensemble.Budget.ContextPack)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[ensemble.early_stop]")
	fmt.Fprintln(w, "# Early stop defaults for ensembles")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Ensemble.EarlyStop.Enabled)
	fmt.Fprintf(w, "min_agents = %d\n", cfg.Ensemble.EarlyStop.MinAgents)
	fmt.Fprintf(w, "findings_threshold = %.2f\n", cfg.Ensemble.EarlyStop.FindingsThreshold)
	fmt.Fprintf(w, "similarity_threshold = %.2f\n", cfg.Ensemble.EarlyStop.SimilarityThreshold)
	fmt.Fprintf(w, "window_size = %d\n", cfg.Ensemble.EarlyStop.WindowSize)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[coordinator]")
	fmt.Fprintln(w, "# Session coordinator (ntm coordinator run): monitoring, digests, auto-assign,")
	fmt.Fprintln(w, "# conflict handling, and the Agent Mail idle-pane nudge (GH#231)")
	fmt.Fprintf(w, "poll_interval = %q  # How often to poll agent status\n", cfg.Coordinator.PollInterval.String())
	fmt.Fprintf(w, "digest_interval = %q  # How often to send digests\n", cfg.Coordinator.DigestInterval.String())
	fmt.Fprintf(w, "auto_assign = %t  # Automatically assign work to idle agents\n", cfg.Coordinator.AutoAssign)
	fmt.Fprintf(w, "idle_threshold = %.1f  # Seconds of inactivity before considering idle\n", cfg.Coordinator.IdleThreshold)
	fmt.Fprintf(w, "assign_only_idle = %t  # Only assign to truly idle agents\n", cfg.Coordinator.AssignOnlyIdle)
	fmt.Fprintf(w, "conflict_notify = %t  # Notify when conflicts detected\n", cfg.Coordinator.ConflictNotify)
	fmt.Fprintf(w, "conflict_negotiate = %t  # Attempt automatic conflict resolution\n", cfg.Coordinator.ConflictNegotiate)
	fmt.Fprintf(w, "send_digests = %t  # Send periodic digests to the human agent\n", cfg.Coordinator.SendDigests)
	fmt.Fprintf(w, "human_agent = %q  # Agent name digests are sent to\n", cfg.Coordinator.HumanAgent)
	fmt.Fprintf(w, "mail_nudge = %t  # Nudge idle panes that have unread Agent Mail\n", cfg.Coordinator.MailNudge)
	fmt.Fprintf(w, "nudge_cooldown_seconds = %d  # Minimum seconds between nudges to the same pane\n", cfg.Coordinator.NudgeCooldownSeconds)
	fmt.Fprintf(w, "nudge_message = %q  # Optional override for the nudge prompt (empty = built-in)\n", cfg.Coordinator.NudgeMessage)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Command Palette entries")
	fmt.Fprintln(w, "# Add your own prompts here")
	fmt.Fprintln(w)

	// Group by category, preserving order of first occurrence
	categories := make(map[string][]PaletteCmd)
	var categoryOrder []string
	seenCategories := make(map[string]bool)

	for _, cmd := range cfg.Palette {
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		categories[cat] = append(categories[cat], cmd)
		if !seenCategories[cat] {
			seenCategories[cat] = true
			categoryOrder = append(categoryOrder, cat)
		}
	}

	// Write categories in order of first occurrence
	for _, cat := range categoryOrder {
		cmds := categories[cat]
		fmt.Fprintf(w, "# %s\n", cat)
		for _, cmd := range cmds {
			fmt.Fprintln(w, "[[palette]]")
			fmt.Fprintf(w, "key = %q\n", cmd.Key)
			fmt.Fprintf(w, "label = %q\n", cmd.Label)
			if cmd.Category != "" {
				fmt.Fprintf(w, "category = %q\n", cmd.Category)
			}
			// Use multi-line string for prompts
			fmt.Fprintf(w, "prompt = \"\"\"\n%s\"\"\"\n", cmd.Prompt)
			fmt.Fprintln(w)
		}
	}

	return nil
}

// ExpandHome expands the tilde (~) in a path to the user's home directory.
// Supports "~" and "~/path" formats.
func ExpandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}

	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}

	return path
}

// GetProjectDir returns the project directory for a session.
// Labels are stripped so that labeled sessions (e.g. "myproject--frontend")
// resolve to the same directory as the base session ("myproject").
func (c *Config) GetProjectDir(session string) string {
	base := ExpandHome(c.ProjectsBase)
	return filepath.Join(base, SessionBase(session))
}

// SetProjectsBase sets the projects_base in the config file at configPath.
// If configPath is empty, the default config path is used.
// If the config file doesn't exist, it creates one with defaults.
// The path can use ~ for home directory (which will be preserved in config).
func SetProjectsBase(configPath, path string) error {
	// Expand ~ in path for validation
	expandedPath := ExpandHome(path)

	// Validate path - must be absolute after expansion
	if !filepath.IsAbs(expandedPath) {
		return fmt.Errorf("path must be absolute: %s", path)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(expandedPath, 0755); err != nil {
		return fmt.Errorf("cannot create directory %s: %w", expandedPath, err)
	}

	if configPath == "" {
		configPath = DefaultPath()
	}

	// Ensure config directory exists
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Read existing config or use defaults
	var fileContents string
	if data, err := os.ReadFile(configPath); err == nil {
		fileContents = string(data)
	}

	// Store the original path (preserves ~ if used)
	fileContents = upsertTOMLKey(fileContents, "projects_base", path)

	// Write back
	if err := util.AtomicWriteFile(configPath, []byte(fileContents), 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// upsertTOMLKey updates or inserts a top-level TOML key.
func upsertTOMLKey(contents, key, value string) string {
	lines := strings.Split(contents, "\n")
	keyPrefix := key + " "
	keyEquals := key + "="
	found := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, keyPrefix) || strings.HasPrefix(trimmed, keyEquals) {
			// Replace existing line
			lines[i] = fmt.Sprintf("%s = %q", key, value)
			found = true
			break
		}
	}

	if !found {
		// Add at the beginning (after any comments at the top)
		insertIdx := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				insertIdx = i
				break
			}
			insertIdx = i + 1
		}

		newLine := fmt.Sprintf("%s = %q", key, value)
		if insertIdx >= len(lines) {
			lines = append(lines, newLine)
		} else {
			// Insert at position
			lines = append(lines[:insertIdx], append([]string{newLine}, lines[insertIdx:]...)...)
		}
	}

	result := strings.Join(lines, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result
}

// GetValue retrieves a configuration value by its dotted path (e.g., "alerts.enabled")
func GetValue(cfg *Config, path string) (interface{}, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	parts := strings.Split(path, ".")

	switch parts[0] {
	case "projects_base":
		return cfg.ProjectsBase, nil
	case "theme":
		return cfg.Theme, nil
	case "help_verbosity":
		return cfg.HelpVerbosity, nil
	case "palette_file":
		return cfg.PaletteFile, nil
	case "palette":
		return cfg.Palette, nil
	case "palette_state":
		if len(parts) < 2 {
			return cfg.PaletteState, nil
		}
		switch parts[1] {
		case "pinned":
			return cfg.PaletteState.Pinned, nil
		case "favorites":
			return cfg.PaletteState.Favorites, nil
		}
	case "agents":
		if len(parts) < 2 {
			return cfg.Agents, nil
		}
		switch parts[1] {
		case "claude":
			return cfg.Agents.Claude, nil
		case "codex":
			return cfg.Agents.Codex, nil
		case "gemini":
			return cfg.Agents.Gemini, nil
		case "antigravity":
			return cfg.Agents.Antigravity, nil
		case "grok":
			return cfg.Agents.Grok, nil
		case "cursor":
			return cfg.Agents.Cursor, nil
		case "windsurf":
			return cfg.Agents.Windsurf, nil
		case "aider":
			return cfg.Agents.Aider, nil
		case "oc":
			return cfg.Agents.Opencode, nil
		case "plugins":
			return cfg.Agents.Plugins, nil
		}
	case "tmux":
		if len(parts) < 2 {
			return cfg.Tmux, nil
		}
		switch parts[1] {
		case "default_panes":
			return cfg.Tmux.DefaultPanes, nil
		case "pane_init_delay_ms":
			return cfg.Tmux.PaneInitDelayMs, nil
		case "history_limit":
			return cfg.Tmux.HistoryLimit, nil
		}
	case "robot":
		if len(parts) < 2 {
			return cfg.Robot, nil
		}
		switch parts[1] {
		case "verbosity":
			return cfg.Robot.Verbosity, nil
		case "output":
			if len(parts) < 3 {
				return cfg.Robot.Output, nil
			}
			switch parts[2] {
			case "format":
				return cfg.Robot.Output.Format, nil
			}
		}
	case "agent_mail":
		if len(parts) < 2 {
			return cfg.AgentMail, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.AgentMail.Enabled, nil
		case "url":
			return cfg.AgentMail.URL, nil
		case "token":
			return "[redacted]", nil
		case "auto_register":
			return cfg.AgentMail.AutoRegister, nil
		case "supervisor_enabled":
			return cfg.AgentMail.SupervisorEnabledOrDefault(), nil
		}
	case "integrations":
		if len(parts) < 2 {
			return cfg.Integrations, nil
		}
		switch parts[1] {
		case "dcg":
			if len(parts) < 3 {
				return cfg.Integrations.DCG, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Integrations.DCG.Enabled, nil
			case "binary_path":
				return cfg.Integrations.DCG.BinaryPath, nil
			case "custom_blocklist":
				return cfg.Integrations.DCG.CustomBlocklist, nil
			case "custom_whitelist":
				return cfg.Integrations.DCG.CustomWhitelist, nil
			case "audit_log":
				return cfg.Integrations.DCG.AuditLog, nil
			case "allow_override":
				return cfg.Integrations.DCG.AllowOverride, nil
			}
		case "rano":
			if len(parts) < 3 {
				return cfg.Integrations.Rano, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Integrations.Rano.Enabled, nil
			case "poll_interval_ms":
				return cfg.Integrations.Rano.PollIntervalMs, nil
			}
		case "caam":
			if len(parts) < 3 {
				return cfg.Integrations.CAAM, nil
			}
			switch parts[2] {
			case "binary_path":
				return cfg.Integrations.CAAM.BinaryPath, nil
			case "auto_failover":
				return cfg.Integrations.CAAM.AutoFailover, nil
			case "reset_horizon_minutes":
				return cfg.Integrations.CAAM.ResetHorizonMinutes, nil
			case "failover_providers":
				return cfg.Integrations.CAAM.FailoverProviders, nil
			}
		case "rch":
			if len(parts) < 3 {
				return cfg.Integrations.RCH, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Integrations.RCH.Enabled, nil
			case "binary_path":
				return cfg.Integrations.RCH.BinaryPath, nil
			case "intercept_patterns":
				return cfg.Integrations.RCH.InterceptPatterns, nil
			}
		case "process_triage":
			if len(parts) < 3 {
				return cfg.Integrations.ProcessTriage, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Integrations.ProcessTriage.Enabled, nil
			case "check_interval":
				return cfg.Integrations.ProcessTriage.CheckInterval, nil
			case "idle_threshold":
				return cfg.Integrations.ProcessTriage.IdleThreshold, nil
			case "stuck_threshold":
				return cfg.Integrations.ProcessTriage.StuckThreshold, nil
			case "use_rano_data":
				return cfg.Integrations.ProcessTriage.UseRanoData, nil
			}
		case "xf":
			if len(parts) < 3 {
				return cfg.Integrations.XF, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Integrations.XF.Enabled, nil
			}
		case "bv":
			if len(parts) < 3 {
				return cfg.Integrations.BV, nil
			}
			switch parts[2] {
			case "timeout_seconds":
				return cfg.Integrations.BV.TimeoutSeconds, nil
			}
		}
	case "models":
		if len(parts) < 2 {
			return cfg.Models, nil
		}
		switch parts[1] {
		case "default_claude":
			return cfg.Models.DefaultClaude, nil
		case "default_codex":
			return cfg.Models.DefaultCodex, nil
		case "default_gemini":
			return cfg.Models.DefaultGemini, nil
		case "default_grok":
			return cfg.Models.DefaultGrok, nil
		case "claude":
			return cfg.Models.Claude, nil
		case "codex":
			return cfg.Models.Codex, nil
		case "gemini":
			return cfg.Models.Gemini, nil
		case "grok":
			return cfg.Models.Grok, nil
		}
	case "alerts":
		if len(parts) < 2 {
			return cfg.Alerts, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Alerts.Enabled, nil
		case "agent_stuck_minutes":
			return cfg.Alerts.AgentStuckMinutes, nil
		case "disk_low_threshold_gb":
			return cfg.Alerts.DiskLowThresholdGB, nil
		case "disk_full_horizon_hours":
			return cfg.Alerts.DiskFullHorizonHours, nil
		case "mail_backlog_threshold":
			return cfg.Alerts.MailBacklogThreshold, nil
		case "bead_stale_hours":
			return cfg.Alerts.BeadStaleHours, nil
		case "context_warning_threshold":
			return cfg.Alerts.ContextWarningThreshold, nil
		case "resolved_prune_minutes":
			return cfg.Alerts.ResolvedPruneMinutes, nil
		}
	case "checkpoints":
		if len(parts) < 2 {
			return cfg.Checkpoints, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Checkpoints.Enabled, nil
		case "before_broadcast":
			return cfg.Checkpoints.BeforeBroadcast, nil
		case "before_add_agents":
			return cfg.Checkpoints.BeforeAddAgents, nil
		case "max_auto_checkpoints":
			return cfg.Checkpoints.MaxAutoCheckpoints, nil
		case "scrollback_lines":
			return cfg.Checkpoints.ScrollbackLines, nil
		case "include_git":
			return cfg.Checkpoints.IncludeGit, nil
		}
	case "notifications":
		if len(parts) < 2 {
			return cfg.Notifications, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Notifications.Enabled, nil
		case "events":
			return cfg.Notifications.Events, nil
		case "primary":
			return cfg.Notifications.Primary, nil
		case "fallback":
			return cfg.Notifications.Fallback, nil
		case "routing":
			return cfg.Notifications.Routing, nil
		case "desktop":
			if len(parts) < 3 {
				return cfg.Notifications.Desktop, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Notifications.Desktop.Enabled, nil
			case "title":
				return cfg.Notifications.Desktop.Title, nil
			}
		case "webhook":
			if len(parts) < 3 {
				return cfg.Notifications.Webhook, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Notifications.Webhook.Enabled, nil
			case "url":
				return cfg.Notifications.Webhook.URL, nil
			case "template":
				return cfg.Notifications.Webhook.Template, nil
			case "method":
				return cfg.Notifications.Webhook.Method, nil
			case "headers":
				return cfg.Notifications.Webhook.Headers, nil
			}
		case "shell":
			if len(parts) < 3 {
				return cfg.Notifications.Shell, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Notifications.Shell.Enabled, nil
			case "command":
				return cfg.Notifications.Shell.Command, nil
			case "pass_json":
				return cfg.Notifications.Shell.PassJSON, nil
			}
		case "log":
			if len(parts) < 3 {
				return cfg.Notifications.Log, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Notifications.Log.Enabled, nil
			case "path":
				return cfg.Notifications.Log.Path, nil
			}
		case "filebox":
			if len(parts) < 3 {
				return cfg.Notifications.FileBox, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Notifications.FileBox.Enabled, nil
			case "path":
				return cfg.Notifications.FileBox.Path, nil
			}
		}
	case "resilience":
		if len(parts) < 2 {
			return cfg.Resilience, nil
		}
		switch parts[1] {
		case "auto_restart":
			return cfg.Resilience.AutoRestart, nil
		case "max_restarts":
			return cfg.Resilience.MaxRestarts, nil
		case "restart_delay_seconds":
			return cfg.Resilience.RestartDelaySeconds, nil
		case "health_check_seconds":
			return cfg.Resilience.HealthCheckSeconds, nil
		case "crash_threshold":
			return cfg.Resilience.CrashThreshold, nil
		case "notify_on_crash":
			return cfg.Resilience.NotifyOnCrash, nil
		case "notify_on_max_restarts":
			return cfg.Resilience.NotifyOnMaxRestarts, nil
		case "rate_limit":
			if len(parts) < 3 {
				return cfg.Resilience.RateLimit, nil
			}
			switch parts[2] {
			case "detect":
				return cfg.Resilience.RateLimit.Detect, nil
			case "notify":
				return cfg.Resilience.RateLimit.Notify, nil
			}
		}
	case "context_rotation":
		if len(parts) < 2 {
			return cfg.ContextRotation, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.ContextRotation.Enabled, nil
		case "warning_threshold":
			return cfg.ContextRotation.WarningThreshold, nil
		case "rotate_threshold":
			return cfg.ContextRotation.RotateThreshold, nil
		case "summary_max_tokens":
			return cfg.ContextRotation.SummaryMaxTokens, nil
		case "min_session_age_sec":
			return cfg.ContextRotation.MinSessionAgeSec, nil
		case "try_compact_first":
			return cfg.ContextRotation.TryCompactFirst, nil
		case "require_confirm":
			return cfg.ContextRotation.RequireConfirm, nil
		case "confirm_timeout_sec":
			return cfg.ContextRotation.ConfirmTimeoutSec, nil
		case "default_confirm_action":
			return cfg.ContextRotation.DefaultConfirmAction, nil
		}
	case "context":
		if len(parts) < 2 {
			return cfg.Context, nil
		}
		switch parts[1] {
		case "ms_skills":
			return cfg.Context.MSSkills, nil
		}
	case "recovery":
		if len(parts) < 2 {
			return cfg.SessionRecovery, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.SessionRecovery.Enabled, nil
		case "include_agent_mail":
			return cfg.SessionRecovery.IncludeAgentMail, nil
		case "include_cm_memories":
			return cfg.SessionRecovery.IncludeCMMemories, nil
		case "include_beads_context":
			return cfg.SessionRecovery.IncludeBeadsContext, nil
		case "max_recovery_tokens":
			return cfg.SessionRecovery.MaxRecoveryTokens, nil
		case "auto_inject_on_spawn":
			return cfg.SessionRecovery.AutoInjectOnSpawn, nil
		case "max_cm_rules":
			return cfg.SessionRecovery.MaxCMRules, nil
		case "max_cm_snippets":
			return cfg.SessionRecovery.MaxCMSnippets, nil
		case "timeout_seconds":
			return cfg.SessionRecovery.TimeoutSeconds, nil
		}
	case "cleanup":
		if len(parts) < 2 {
			return cfg.Cleanup, nil
		}
		switch parts[1] {
		case "auto_clean_on_startup":
			return cfg.Cleanup.AutoCleanOnStartup, nil
		case "max_age_hours":
			return cfg.Cleanup.MaxAgeHours, nil
		case "verbose":
			return cfg.Cleanup.Verbose, nil
		}
	case "assign":
		if len(parts) < 2 {
			return cfg.Assign, nil
		}
		switch parts[1] {
		case "strategy":
			return cfg.Assign.Strategy, nil
		case "prompt_template":
			return cfg.Assign.PromptTemplate, nil
		case "prompt_template_file":
			return cfg.Assign.PromptTemplateFile, nil
		case "operator_gated_labels":
			return append([]string(nil), cfg.Assign.OperatorGatedLabels...), nil
		}
	case "file_reservation":
		if len(parts) < 2 {
			return cfg.FileReservation, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.FileReservation.Enabled, nil
		case "auto_reserve":
			return cfg.FileReservation.AutoReserve, nil
		case "auto_release_idle_minutes":
			return cfg.FileReservation.AutoReleaseIdleMin, nil
		case "notify_on_conflict":
			return cfg.FileReservation.NotifyOnConflict, nil
		case "extend_on_activity":
			return cfg.FileReservation.ExtendOnActivity, nil
		case "default_ttl_minutes":
			return cfg.FileReservation.DefaultTTLMin, nil
		case "poll_interval_seconds":
			return cfg.FileReservation.PollIntervalSec, nil
		case "capture_lines":
			return cfg.FileReservation.CaptureLinesForDetect, nil
		case "debug":
			return cfg.FileReservation.Debug, nil
		}
	case "memory":
		if len(parts) < 2 {
			return cfg.Memory, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Memory.Enabled, nil
		case "include_in_recovery":
			return cfg.Memory.IncludeInRecovery, nil
		case "max_rules":
			return cfg.Memory.MaxRules, nil
		case "query_timeout_seconds":
			return cfg.Memory.QueryTimeoutSeconds, nil
		case "send_injection":
			return cfg.Memory.SendInjection, nil
		case "send_max_rules":
			return cfg.Memory.SendMaxRules, nil
		case "send_budget_tokens":
			return cfg.Memory.SendBudgetTokens, nil
		}
	case "privacy":
		if len(parts) < 2 {
			return cfg.Privacy, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Privacy.Enabled, nil
		case "disable_prompt_history":
			return cfg.Privacy.DisablePromptHistory, nil
		case "disable_event_logs":
			return cfg.Privacy.DisableEventLogs, nil
		case "disable_checkpoints":
			return cfg.Privacy.DisableCheckpoints, nil
		case "disable_scrollback_capture":
			return cfg.Privacy.DisableScrollbackCapture, nil
		case "require_explicit_persist":
			return cfg.Privacy.RequireExplicitPersist, nil
		}
	case "safety":
		if len(parts) < 2 {
			return cfg.Safety, nil
		}
		switch parts[1] {
		case "profile":
			return cfg.Safety.Profile, nil
		}
	case "preflight":
		if len(parts) < 2 {
			return cfg.Preflight, nil
		}
		switch parts[1] {
		case "strict":
			return cfg.Preflight.Strict, nil
		}
	case "redaction":
		if len(parts) < 2 {
			return cfg.Redaction, nil
		}
		switch parts[1] {
		case "mode":
			return cfg.Redaction.Mode, nil
		case "allowlist":
			return cfg.Redaction.Allowlist, nil
		case "extra_patterns":
			return cfg.Redaction.ExtraPatterns, nil
		case "disabled_categories":
			return cfg.Redaction.DisabledCategories, nil
		}
	case "encryption":
		if len(parts) < 2 {
			return cfg.Encryption, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Encryption.Enabled, nil
		case "key_source":
			return cfg.Encryption.KeySource, nil
		case "key_env":
			return cfg.Encryption.KeyEnv, nil
		case "key_file":
			return cfg.Encryption.KeyFile, nil
		case "key_command":
			return cfg.Encryption.KeyCommand, nil
		case "key_format":
			return cfg.Encryption.KeyFormat, nil
		case "active_key_id":
			return cfg.Encryption.ActiveKeyID, nil
		case "keyring":
			return cfg.Encryption.Keyring, nil
		}
	case "send":
		if len(parts) < 2 {
			return cfg.Send, nil
		}
		switch parts[1] {
		case "base_prompt":
			return cfg.Send.BasePrompt, nil
		case "base_prompt_file":
			return cfg.Send.BasePromptFile, nil
		}
	case "prompts":
		if len(parts) < 2 {
			return cfg.Prompts, nil
		}
		switch parts[1] {
		case "cc_default":
			return cfg.Prompts.CCDefault, nil
		case "cc_default_file":
			return cfg.Prompts.CCDefaultFile, nil
		case "cod_default":
			return cfg.Prompts.CodDefault, nil
		case "cod_default_file":
			return cfg.Prompts.CodDefaultFile, nil
		case "gmi_default":
			return cfg.Prompts.GmiDefault, nil
		case "gmi_default_file":
			return cfg.Prompts.GmiDefaultFile, nil
		case "agy_default":
			return cfg.Prompts.AgyDefault, nil
		case "agy_default_file":
			return cfg.Prompts.AgyDefaultFile, nil
		}
	case "spawn_pacing":
		if len(parts) < 2 {
			return cfg.SpawnPacing, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.SpawnPacing.Enabled, nil
		case "max_concurrent_spawns":
			return cfg.SpawnPacing.MaxConcurrentSpawns, nil
		case "agent_caps":
			if len(parts) < 3 {
				return cfg.SpawnPacing.AgentCaps, nil
			}
			switch parts[2] {
			case "claude_max_concurrent":
				return cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent, nil
			case "codex_max_concurrent":
				return cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent, nil
			case "gemini_max_concurrent":
				return cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent, nil
			}
		}
	case "swarm":
		if len(parts) < 2 {
			return cfg.Swarm, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Swarm.Enabled, nil
		case "default_scan_dir":
			return cfg.Swarm.DefaultScanDir, nil
		case "tier1_threshold":
			return cfg.Swarm.Tier1Threshold, nil
		case "tier2_threshold":
			return cfg.Swarm.Tier2Threshold, nil
		case "tier1_allocation":
			return cfg.Swarm.Tier1Allocation, nil
		case "tier2_allocation":
			return cfg.Swarm.Tier2Allocation, nil
		case "tier3_allocation":
			return cfg.Swarm.Tier3Allocation, nil
		case "sessions_per_type":
			return cfg.Swarm.SessionsPerType, nil
		case "panes_per_session":
			return cfg.Swarm.PanesPerSession, nil
		case "stagger_delay_ms":
			return cfg.Swarm.StaggerDelayMs, nil
		case "auto_rotate_accounts":
			return cfg.Swarm.AutoRotateAccounts, nil
		}
	case "coordinator":
		if len(parts) < 2 {
			return cfg.Coordinator, nil
		}
		switch parts[1] {
		case "poll_interval":
			return cfg.Coordinator.PollInterval, nil
		case "digest_interval":
			return cfg.Coordinator.DigestInterval, nil
		case "auto_assign":
			return cfg.Coordinator.AutoAssign, nil
		case "idle_threshold":
			return cfg.Coordinator.IdleThreshold, nil
		case "assign_only_idle":
			return cfg.Coordinator.AssignOnlyIdle, nil
		case "conflict_notify":
			return cfg.Coordinator.ConflictNotify, nil
		case "conflict_negotiate":
			return cfg.Coordinator.ConflictNegotiate, nil
		case "send_digests":
			return cfg.Coordinator.SendDigests, nil
		case "human_agent":
			return cfg.Coordinator.HumanAgent, nil
		case "mail_nudge":
			return cfg.Coordinator.MailNudge, nil
		case "nudge_cooldown_seconds":
			return cfg.Coordinator.NudgeCooldownSeconds, nil
		case "nudge_message":
			return cfg.Coordinator.NudgeMessage, nil
		}
	case "ensemble":
		if len(parts) < 2 {
			return cfg.Ensemble, nil
		}
		switch parts[1] {
		case "default_ensemble":
			return cfg.Ensemble.DefaultEnsemble, nil
		case "agent_mix":
			return cfg.Ensemble.AgentMix, nil
		case "assignment":
			return cfg.Ensemble.Assignment, nil
		case "mode_tier_default":
			return cfg.Ensemble.ModeTierDefault, nil
		case "allow_advanced":
			return cfg.Ensemble.AllowAdvanced, nil
		case "synthesis":
			if len(parts) < 3 {
				return cfg.Ensemble.Synthesis, nil
			}
			switch parts[2] {
			case "strategy":
				return cfg.Ensemble.Synthesis.Strategy, nil
			case "min_confidence":
				return cfg.Ensemble.Synthesis.MinConfidence, nil
			case "max_findings":
				return cfg.Ensemble.Synthesis.MaxFindings, nil
			case "include_raw_outputs":
				return cfg.Ensemble.Synthesis.IncludeRawOutputs, nil
			case "conflict_resolution":
				return cfg.Ensemble.Synthesis.ConflictResolution, nil
			}
		case "cache":
			if len(parts) < 3 {
				return cfg.Ensemble.Cache, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Ensemble.Cache.Enabled, nil
			case "ttl_minutes":
				return cfg.Ensemble.Cache.TTLMinutes, nil
			case "cache_dir":
				return cfg.Ensemble.Cache.CacheDir, nil
			case "max_entries":
				return cfg.Ensemble.Cache.MaxEntries, nil
			case "share_across_modes":
				return cfg.Ensemble.Cache.ShareAcrossModes, nil
			}
		case "budget":
			if len(parts) < 3 {
				return cfg.Ensemble.Budget, nil
			}
			switch parts[2] {
			case "per_agent":
				return cfg.Ensemble.Budget.PerAgent, nil
			case "total":
				return cfg.Ensemble.Budget.Total, nil
			case "synthesis":
				return cfg.Ensemble.Budget.Synthesis, nil
			case "context_pack":
				return cfg.Ensemble.Budget.ContextPack, nil
			}
		case "early_stop":
			if len(parts) < 3 {
				return cfg.Ensemble.EarlyStop, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.Ensemble.EarlyStop.Enabled, nil
			case "min_agents":
				return cfg.Ensemble.EarlyStop.MinAgents, nil
			case "findings_threshold":
				return cfg.Ensemble.EarlyStop.FindingsThreshold, nil
			case "similarity_threshold":
				return cfg.Ensemble.EarlyStop.SimilarityThreshold, nil
			case "window_size":
				return cfg.Ensemble.EarlyStop.WindowSize, nil
			}
		}
	case "bugs":
		if len(parts) < 2 {
			return cfg.Bugs, nil
		}
		switch parts[1] {
		case "push_routing":
			return cfg.Bugs.PushRouting, nil
		case "interval":
			return cfg.Bugs.Interval, nil
		case "cooldown_minutes":
			return cfg.Bugs.CooldownMinutes, nil
		}
	case "cass":
		if len(parts) < 2 {
			return cfg.CASS, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.CASS.Enabled, nil
		case "binary_path":
			return cfg.CASS.BinaryPath, nil
		case "timeout":
			return cfg.CASS.Timeout, nil
		case "context":
			if len(parts) < 3 {
				return cfg.CASS.Context, nil
			}
			switch parts[2] {
			case "enabled":
				return cfg.CASS.Context.Enabled, nil
			case "max_sessions":
				return cfg.CASS.Context.MaxSessions, nil
			case "lookback_days":
				return cfg.CASS.Context.LookbackDays, nil
			case "max_tokens":
				return cfg.CASS.Context.MaxTokens, nil
			case "min_relevance":
				return cfg.CASS.Context.MinRelevance, nil
			case "skip_if_context_above":
				return cfg.CASS.Context.SkipIfContextAbove, nil
			case "prefer_same_project":
				return cfg.CASS.Context.PreferSameProject, nil
			}
		}
	case "scanner":
		if len(parts) < 2 {
			return cfg.Scanner, nil
		}
		switch parts[1] {
		case "ubs_path":
			return cfg.Scanner.UBSPath, nil
		}
	case "rotation":
		if len(parts) < 2 {
			return cfg.Rotation, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Rotation.Enabled, nil
		case "auto_open_browser":
			return cfg.Rotation.AutoOpenBrowser, nil
		case "auto_trigger":
			return cfg.Rotation.AutoTrigger, nil
		case "auto_initiate":
			return cfg.Rotation.AutoInitiate, nil
		case "usage_percent_threshold":
			return cfg.Rotation.UsagePercentThreshold, nil
		case "auto_confirm":
			return cfg.Rotation.AutoConfirm, nil
		case "continuation_prompt":
			return cfg.Rotation.ContinuationPrompt, nil
		case "accounts":
			return cfg.Rotation.Accounts, nil
		case "thresholds":
			if len(parts) < 3 {
				return cfg.Rotation.Thresholds, nil
			}
			switch parts[2] {
			case "warning_percent":
				return cfg.Rotation.Thresholds.WarningPercent, nil
			case "critical_percent":
				return cfg.Rotation.Thresholds.CriticalPercent, nil
			case "restart_if_tokens_above":
				return cfg.Rotation.Thresholds.RestartIfTokensAbove, nil
			case "restart_if_session_hours":
				return cfg.Rotation.Thresholds.RestartIfSessionHours, nil
			}
		}
	case "gemini_setup":
		if len(parts) < 2 {
			return cfg.GeminiSetup, nil
		}
		switch parts[1] {
		case "auto_select_pro_model":
			return cfg.GeminiSetup.AutoSelectProModel, nil
		case "ready_timeout_seconds":
			return cfg.GeminiSetup.ReadyTimeoutSeconds, nil
		case "model_select_timeout_seconds":
			return cfg.GeminiSetup.ModelSelectTimeoutSeconds, nil
		case "verbose":
			return cfg.GeminiSetup.Verbose, nil
		}
	}

	return nil, fmt.Errorf("unknown config path: %s", path)
}

// Reset removes the config file at path and creates a new one with defaults.
// If path is empty, the default config path is used.
func Reset(path string) error {
	if path == "" {
		path = DefaultPath()
	}

	// Remove existing file
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing config file: %w", err)
	}

	// Create new default config
	_, err := CreateDefault(path)
	return err
}

// ConfigDiff represents a difference between current and default config
type ConfigDiff struct {
	Key     string      `json:"key"`
	Path    string      `json:"path"`
	Default interface{} `json:"default"`
	Current interface{} `json:"current"`
	Source  string      `json:"source"` // "global", "project", "env", "flag"
}

// Diff returns all configuration values that differ from defaults
func Diff(cfg *Config) []ConfigDiff {
	if cfg == nil {
		return nil
	}

	defaults := Default()
	var diffs []ConfigDiff

	// Helper to add diff if values differ
	// Key is set to path for uniqueness in JSON output
	addDiff := func(path string, def, cur interface{}) {
		if reflect.DeepEqual(def, cur) {
			return
		}
		diffs = append(diffs, ConfigDiff{
			Key:     path, // Use path as key for uniqueness
			Path:    path,
			Default: def,
			Current: cur,
			Source:  "config", // Could be enhanced to track actual source
		})
	}

	// Top-level settings
	addDiff("projects_base", defaults.ProjectsBase, cfg.ProjectsBase)
	addDiff("theme", defaults.Theme, cfg.Theme)
	addDiff("help_verbosity", defaults.HelpVerbosity, cfg.HelpVerbosity)
	addDiff("palette_file", defaults.PaletteFile, cfg.PaletteFile)
	addDiff("palette", defaults.Palette, cfg.Palette)
	addDiff("palette_state.pinned", defaults.PaletteState.Pinned, cfg.PaletteState.Pinned)
	addDiff("palette_state.favorites", defaults.PaletteState.Favorites, cfg.PaletteState.Favorites)

	// Agents
	addDiff("agents.claude", defaults.Agents.Claude, cfg.Agents.Claude)
	addDiff("agents.codex", defaults.Agents.Codex, cfg.Agents.Codex)
	addDiff("agents.gemini", defaults.Agents.Gemini, cfg.Agents.Gemini)
	addDiff("agents.grok", defaults.Agents.Grok, cfg.Agents.Grok)
	addDiff("agents.cursor", defaults.Agents.Cursor, cfg.Agents.Cursor)
	addDiff("agents.windsurf", defaults.Agents.Windsurf, cfg.Agents.Windsurf)
	addDiff("agents.aider", defaults.Agents.Aider, cfg.Agents.Aider)
	addDiff("agents.plugins", defaults.Agents.Plugins, cfg.Agents.Plugins)

	// Tmux
	addDiff("tmux.default_panes", defaults.Tmux.DefaultPanes, cfg.Tmux.DefaultPanes)
	addDiff("tmux.pane_init_delay_ms", defaults.Tmux.PaneInitDelayMs, cfg.Tmux.PaneInitDelayMs)
	addDiff("tmux.history_limit", defaults.Tmux.HistoryLimit, cfg.Tmux.HistoryLimit)

	// Robot
	addDiff("robot.verbosity", defaults.Robot.Verbosity, cfg.Robot.Verbosity)
	addDiff("robot.output.format", defaults.Robot.Output.Format, cfg.Robot.Output.Format)

	// Agent Mail
	addDiff("agent_mail.enabled", defaults.AgentMail.Enabled, cfg.AgentMail.Enabled)
	addDiff("agent_mail.url", defaults.AgentMail.URL, cfg.AgentMail.URL)
	addDiff("agent_mail.auto_register", defaults.AgentMail.AutoRegister, cfg.AgentMail.AutoRegister)
	addDiff("agent_mail.supervisor_enabled", defaults.AgentMail.SupervisorEnabledOrDefault(), cfg.AgentMail.SupervisorEnabledOrDefault())

	// Integrations (DCG)
	addDiff("integrations.dcg.enabled", defaults.Integrations.DCG.Enabled, cfg.Integrations.DCG.Enabled)
	addDiff("integrations.dcg.binary_path", defaults.Integrations.DCG.BinaryPath, cfg.Integrations.DCG.BinaryPath)
	addDiff("integrations.dcg.custom_blocklist", defaults.Integrations.DCG.CustomBlocklist, cfg.Integrations.DCG.CustomBlocklist)
	addDiff("integrations.dcg.custom_whitelist", defaults.Integrations.DCG.CustomWhitelist, cfg.Integrations.DCG.CustomWhitelist)
	addDiff("integrations.dcg.audit_log", defaults.Integrations.DCG.AuditLog, cfg.Integrations.DCG.AuditLog)
	addDiff("integrations.dcg.allow_override", defaults.Integrations.DCG.AllowOverride, cfg.Integrations.DCG.AllowOverride)
	addDiff("integrations.rano.enabled", defaults.Integrations.Rano.Enabled, cfg.Integrations.Rano.Enabled)
	addDiff("integrations.rano.poll_interval_ms", defaults.Integrations.Rano.PollIntervalMs, cfg.Integrations.Rano.PollIntervalMs)
	addDiff("integrations.caam.binary_path", defaults.Integrations.CAAM.BinaryPath, cfg.Integrations.CAAM.BinaryPath)
	addDiff("integrations.caam.auto_failover", defaults.Integrations.CAAM.AutoFailover, cfg.Integrations.CAAM.AutoFailover)
	addDiff("integrations.caam.reset_horizon_minutes", defaults.Integrations.CAAM.ResetHorizonMinutes, cfg.Integrations.CAAM.ResetHorizonMinutes)
	addDiff("integrations.caam.failover_providers", defaults.Integrations.CAAM.FailoverProviders, cfg.Integrations.CAAM.FailoverProviders)
	addDiff("integrations.rch.enabled", defaults.Integrations.RCH.Enabled, cfg.Integrations.RCH.Enabled)
	addDiff("integrations.rch.binary_path", defaults.Integrations.RCH.BinaryPath, cfg.Integrations.RCH.BinaryPath)
	addDiff("integrations.rch.intercept_patterns", defaults.Integrations.RCH.InterceptPatterns, cfg.Integrations.RCH.InterceptPatterns)
	addDiff("integrations.process_triage.enabled", defaults.Integrations.ProcessTriage.Enabled, cfg.Integrations.ProcessTriage.Enabled)
	addDiff("integrations.process_triage.check_interval", defaults.Integrations.ProcessTriage.CheckInterval, cfg.Integrations.ProcessTriage.CheckInterval)
	addDiff("integrations.process_triage.idle_threshold", defaults.Integrations.ProcessTriage.IdleThreshold, cfg.Integrations.ProcessTriage.IdleThreshold)
	addDiff("integrations.process_triage.stuck_threshold", defaults.Integrations.ProcessTriage.StuckThreshold, cfg.Integrations.ProcessTriage.StuckThreshold)
	addDiff("integrations.process_triage.use_rano_data", defaults.Integrations.ProcessTriage.UseRanoData, cfg.Integrations.ProcessTriage.UseRanoData)
	addDiff("integrations.bv.timeout_seconds", defaults.Integrations.BV.TimeoutSeconds, cfg.Integrations.BV.TimeoutSeconds)

	// Models
	addDiff("models.default_claude", defaults.Models.DefaultClaude, cfg.Models.DefaultClaude)
	addDiff("models.default_codex", defaults.Models.DefaultCodex, cfg.Models.DefaultCodex)
	addDiff("models.default_gemini", defaults.Models.DefaultGemini, cfg.Models.DefaultGemini)
	addDiff("models.default_grok", defaults.Models.DefaultGrok, cfg.Models.DefaultGrok)
	addDiff("models.claude", defaults.Models.Claude, cfg.Models.Claude)
	addDiff("models.codex", defaults.Models.Codex, cfg.Models.Codex)
	addDiff("models.gemini", defaults.Models.Gemini, cfg.Models.Gemini)
	addDiff("models.grok", defaults.Models.Grok, cfg.Models.Grok)

	// Alerts
	addDiff("alerts.enabled", defaults.Alerts.Enabled, cfg.Alerts.Enabled)
	addDiff("alerts.agent_stuck_minutes", defaults.Alerts.AgentStuckMinutes, cfg.Alerts.AgentStuckMinutes)
	addDiff("alerts.disk_low_threshold_gb", defaults.Alerts.DiskLowThresholdGB, cfg.Alerts.DiskLowThresholdGB)
	addDiff("alerts.disk_full_horizon_hours", defaults.Alerts.DiskFullHorizonHours, cfg.Alerts.DiskFullHorizonHours)
	addDiff("alerts.mail_backlog_threshold", defaults.Alerts.MailBacklogThreshold, cfg.Alerts.MailBacklogThreshold)
	addDiff("alerts.bead_stale_hours", defaults.Alerts.BeadStaleHours, cfg.Alerts.BeadStaleHours)
	addDiff("alerts.context_warning_threshold", defaults.Alerts.ContextWarningThreshold, cfg.Alerts.ContextWarningThreshold)
	addDiff("alerts.resolved_prune_minutes", defaults.Alerts.ResolvedPruneMinutes, cfg.Alerts.ResolvedPruneMinutes)

	// Checkpoints
	addDiff("checkpoints.enabled", defaults.Checkpoints.Enabled, cfg.Checkpoints.Enabled)
	addDiff("checkpoints.before_broadcast", defaults.Checkpoints.BeforeBroadcast, cfg.Checkpoints.BeforeBroadcast)
	addDiff("checkpoints.before_add_agents", defaults.Checkpoints.BeforeAddAgents, cfg.Checkpoints.BeforeAddAgents)
	addDiff("checkpoints.max_auto_checkpoints", defaults.Checkpoints.MaxAutoCheckpoints, cfg.Checkpoints.MaxAutoCheckpoints)
	addDiff("checkpoints.scrollback_lines", defaults.Checkpoints.ScrollbackLines, cfg.Checkpoints.ScrollbackLines)
	addDiff("checkpoints.include_git", defaults.Checkpoints.IncludeGit, cfg.Checkpoints.IncludeGit)

	// Notifications
	addDiff("notifications.enabled", defaults.Notifications.Enabled, cfg.Notifications.Enabled)
	addDiff("notifications.events", defaults.Notifications.Events, cfg.Notifications.Events)
	addDiff("notifications.primary", defaults.Notifications.Primary, cfg.Notifications.Primary)
	addDiff("notifications.fallback", defaults.Notifications.Fallback, cfg.Notifications.Fallback)
	addDiff("notifications.routing", defaults.Notifications.Routing, cfg.Notifications.Routing)
	addDiff("notifications.desktop.enabled", defaults.Notifications.Desktop.Enabled, cfg.Notifications.Desktop.Enabled)
	addDiff("notifications.desktop.title", defaults.Notifications.Desktop.Title, cfg.Notifications.Desktop.Title)
	addDiff("notifications.webhook.enabled", defaults.Notifications.Webhook.Enabled, cfg.Notifications.Webhook.Enabled)
	addDiff("notifications.webhook.url", defaults.Notifications.Webhook.URL, cfg.Notifications.Webhook.URL)
	addDiff("notifications.webhook.template", defaults.Notifications.Webhook.Template, cfg.Notifications.Webhook.Template)
	addDiff("notifications.webhook.method", defaults.Notifications.Webhook.Method, cfg.Notifications.Webhook.Method)
	addDiff("notifications.webhook.headers", defaults.Notifications.Webhook.Headers, cfg.Notifications.Webhook.Headers)
	addDiff("notifications.shell.enabled", defaults.Notifications.Shell.Enabled, cfg.Notifications.Shell.Enabled)
	addDiff("notifications.shell.command", defaults.Notifications.Shell.Command, cfg.Notifications.Shell.Command)
	addDiff("notifications.shell.pass_json", defaults.Notifications.Shell.PassJSON, cfg.Notifications.Shell.PassJSON)
	addDiff("notifications.log.enabled", defaults.Notifications.Log.Enabled, cfg.Notifications.Log.Enabled)
	addDiff("notifications.log.path", defaults.Notifications.Log.Path, cfg.Notifications.Log.Path)
	addDiff("notifications.filebox.enabled", defaults.Notifications.FileBox.Enabled, cfg.Notifications.FileBox.Enabled)
	addDiff("notifications.filebox.path", defaults.Notifications.FileBox.Path, cfg.Notifications.FileBox.Path)

	// Resilience
	addDiff("resilience.auto_restart", defaults.Resilience.AutoRestart, cfg.Resilience.AutoRestart)
	addDiff("resilience.max_restarts", defaults.Resilience.MaxRestarts, cfg.Resilience.MaxRestarts)
	addDiff("resilience.restart_delay_seconds", defaults.Resilience.RestartDelaySeconds, cfg.Resilience.RestartDelaySeconds)
	addDiff("resilience.health_check_seconds", defaults.Resilience.HealthCheckSeconds, cfg.Resilience.HealthCheckSeconds)
	addDiff("resilience.crash_threshold", defaults.Resilience.CrashThreshold, cfg.Resilience.CrashThreshold)
	addDiff("resilience.notify_on_crash", defaults.Resilience.NotifyOnCrash, cfg.Resilience.NotifyOnCrash)
	addDiff("resilience.notify_on_max_restarts", defaults.Resilience.NotifyOnMaxRestarts, cfg.Resilience.NotifyOnMaxRestarts)
	addDiff("resilience.rate_limit.detect", defaults.Resilience.RateLimit.Detect, cfg.Resilience.RateLimit.Detect)
	addDiff("resilience.rate_limit.notify", defaults.Resilience.RateLimit.Notify, cfg.Resilience.RateLimit.Notify)

	// Context pack options
	addDiff("context.ms_skills", defaults.Context.MSSkills, cfg.Context.MSSkills)

	// Recovery defaults
	addDiff("recovery.enabled", defaults.SessionRecovery.Enabled, cfg.SessionRecovery.Enabled)
	addDiff("recovery.include_agent_mail", defaults.SessionRecovery.IncludeAgentMail, cfg.SessionRecovery.IncludeAgentMail)
	addDiff("recovery.include_cm_memories", defaults.SessionRecovery.IncludeCMMemories, cfg.SessionRecovery.IncludeCMMemories)
	addDiff("recovery.include_beads_context", defaults.SessionRecovery.IncludeBeadsContext, cfg.SessionRecovery.IncludeBeadsContext)
	addDiff("recovery.max_recovery_tokens", defaults.SessionRecovery.MaxRecoveryTokens, cfg.SessionRecovery.MaxRecoveryTokens)
	addDiff("recovery.auto_inject_on_spawn", defaults.SessionRecovery.AutoInjectOnSpawn, cfg.SessionRecovery.AutoInjectOnSpawn)
	addDiff("recovery.max_cm_rules", defaults.SessionRecovery.MaxCMRules, cfg.SessionRecovery.MaxCMRules)
	addDiff("recovery.max_cm_snippets", defaults.SessionRecovery.MaxCMSnippets, cfg.SessionRecovery.MaxCMSnippets)
	addDiff("recovery.timeout_seconds", defaults.SessionRecovery.TimeoutSeconds, cfg.SessionRecovery.TimeoutSeconds)

	// Cleanup defaults
	addDiff("cleanup.auto_clean_on_startup", defaults.Cleanup.AutoCleanOnStartup, cfg.Cleanup.AutoCleanOnStartup)
	addDiff("cleanup.max_age_hours", defaults.Cleanup.MaxAgeHours, cfg.Cleanup.MaxAgeHours)
	addDiff("cleanup.verbose", defaults.Cleanup.Verbose, cfg.Cleanup.Verbose)

	// Assign defaults
	addDiff("assign.strategy", defaults.Assign.Strategy, cfg.Assign.Strategy)
	addDiff("assign.prompt_template", defaults.Assign.PromptTemplate, cfg.Assign.PromptTemplate)
	addDiff("assign.prompt_template_file", defaults.Assign.PromptTemplateFile, cfg.Assign.PromptTemplateFile)
	addDiff("assign.operator_gated_labels", defaults.Assign.OperatorGatedLabels, cfg.Assign.OperatorGatedLabels)

	// File reservation
	addDiff("file_reservation.enabled", defaults.FileReservation.Enabled, cfg.FileReservation.Enabled)
	addDiff("file_reservation.auto_reserve", defaults.FileReservation.AutoReserve, cfg.FileReservation.AutoReserve)
	addDiff("file_reservation.auto_release_idle_minutes", defaults.FileReservation.AutoReleaseIdleMin, cfg.FileReservation.AutoReleaseIdleMin)
	addDiff("file_reservation.notify_on_conflict", defaults.FileReservation.NotifyOnConflict, cfg.FileReservation.NotifyOnConflict)
	addDiff("file_reservation.extend_on_activity", defaults.FileReservation.ExtendOnActivity, cfg.FileReservation.ExtendOnActivity)
	addDiff("file_reservation.default_ttl_minutes", defaults.FileReservation.DefaultTTLMin, cfg.FileReservation.DefaultTTLMin)
	addDiff("file_reservation.poll_interval_seconds", defaults.FileReservation.PollIntervalSec, cfg.FileReservation.PollIntervalSec)
	addDiff("file_reservation.capture_lines", defaults.FileReservation.CaptureLinesForDetect, cfg.FileReservation.CaptureLinesForDetect)
	addDiff("file_reservation.debug", defaults.FileReservation.Debug, cfg.FileReservation.Debug)

	// Memory
	addDiff("memory.enabled", defaults.Memory.Enabled, cfg.Memory.Enabled)
	addDiff("memory.include_in_recovery", defaults.Memory.IncludeInRecovery, cfg.Memory.IncludeInRecovery)
	addDiff("memory.max_rules", defaults.Memory.MaxRules, cfg.Memory.MaxRules)
	addDiff("memory.query_timeout_seconds", defaults.Memory.QueryTimeoutSeconds, cfg.Memory.QueryTimeoutSeconds)
	addDiff("memory.send_injection", defaults.Memory.SendInjection, cfg.Memory.SendInjection)
	addDiff("memory.send_max_rules", defaults.Memory.SendMaxRules, cfg.Memory.SendMaxRules)
	addDiff("memory.send_budget_tokens", defaults.Memory.SendBudgetTokens, cfg.Memory.SendBudgetTokens)

	// Privacy
	addDiff("privacy.enabled", defaults.Privacy.Enabled, cfg.Privacy.Enabled)
	addDiff("privacy.disable_prompt_history", defaults.Privacy.DisablePromptHistory, cfg.Privacy.DisablePromptHistory)
	addDiff("privacy.disable_event_logs", defaults.Privacy.DisableEventLogs, cfg.Privacy.DisableEventLogs)
	addDiff("privacy.disable_checkpoints", defaults.Privacy.DisableCheckpoints, cfg.Privacy.DisableCheckpoints)
	addDiff("privacy.disable_scrollback_capture", defaults.Privacy.DisableScrollbackCapture, cfg.Privacy.DisableScrollbackCapture)
	addDiff("privacy.require_explicit_persist", defaults.Privacy.RequireExplicitPersist, cfg.Privacy.RequireExplicitPersist)

	// Safety, preflight, redaction, encryption
	addDiff("safety.profile", defaults.Safety.Profile, cfg.Safety.Profile)
	addDiff("preflight.strict", defaults.Preflight.Strict, cfg.Preflight.Strict)
	addDiff("redaction.mode", defaults.Redaction.Mode, cfg.Redaction.Mode)
	addDiff("redaction.allowlist", defaults.Redaction.Allowlist, cfg.Redaction.Allowlist)
	addDiff("redaction.disabled_categories", defaults.Redaction.DisabledCategories, cfg.Redaction.DisabledCategories)
	addDiff("encryption.enabled", defaults.Encryption.Enabled, cfg.Encryption.Enabled)
	addDiff("encryption.key_source", defaults.Encryption.KeySource, cfg.Encryption.KeySource)
	addDiff("encryption.key_env", defaults.Encryption.KeyEnv, cfg.Encryption.KeyEnv)
	addDiff("encryption.key_file", defaults.Encryption.KeyFile, cfg.Encryption.KeyFile)
	addDiff("encryption.key_command", defaults.Encryption.KeyCommand, cfg.Encryption.KeyCommand)
	addDiff("encryption.key_format", defaults.Encryption.KeyFormat, cfg.Encryption.KeyFormat)
	addDiff("encryption.active_key_id", defaults.Encryption.ActiveKeyID, cfg.Encryption.ActiveKeyID)

	// Send/prompt defaults
	addDiff("send.base_prompt", defaults.Send.BasePrompt, cfg.Send.BasePrompt)
	addDiff("send.base_prompt_file", defaults.Send.BasePromptFile, cfg.Send.BasePromptFile)
	addDiff("prompts.cc_default", defaults.Prompts.CCDefault, cfg.Prompts.CCDefault)
	addDiff("prompts.cc_default_file", defaults.Prompts.CCDefaultFile, cfg.Prompts.CCDefaultFile)
	addDiff("prompts.cod_default", defaults.Prompts.CodDefault, cfg.Prompts.CodDefault)
	addDiff("prompts.cod_default_file", defaults.Prompts.CodDefaultFile, cfg.Prompts.CodDefaultFile)
	addDiff("prompts.gmi_default", defaults.Prompts.GmiDefault, cfg.Prompts.GmiDefault)
	addDiff("prompts.gmi_default_file", defaults.Prompts.GmiDefaultFile, cfg.Prompts.GmiDefaultFile)
	addDiff("prompts.agy_default", defaults.Prompts.AgyDefault, cfg.Prompts.AgyDefault)
	addDiff("prompts.agy_default_file", defaults.Prompts.AgyDefaultFile, cfg.Prompts.AgyDefaultFile)

	// Context Rotation
	addDiff("context_rotation.enabled", defaults.ContextRotation.Enabled, cfg.ContextRotation.Enabled)
	addDiff("context_rotation.warning_threshold", defaults.ContextRotation.WarningThreshold, cfg.ContextRotation.WarningThreshold)
	addDiff("context_rotation.rotate_threshold", defaults.ContextRotation.RotateThreshold, cfg.ContextRotation.RotateThreshold)
	addDiff("context_rotation.summary_max_tokens", defaults.ContextRotation.SummaryMaxTokens, cfg.ContextRotation.SummaryMaxTokens)
	addDiff("context_rotation.min_session_age_sec", defaults.ContextRotation.MinSessionAgeSec, cfg.ContextRotation.MinSessionAgeSec)
	addDiff("context_rotation.try_compact_first", defaults.ContextRotation.TryCompactFirst, cfg.ContextRotation.TryCompactFirst)
	addDiff("context_rotation.require_confirm", defaults.ContextRotation.RequireConfirm, cfg.ContextRotation.RequireConfirm)
	addDiff("context_rotation.confirm_timeout_sec", defaults.ContextRotation.ConfirmTimeoutSec, cfg.ContextRotation.ConfirmTimeoutSec)
	addDiff("context_rotation.default_confirm_action", defaults.ContextRotation.DefaultConfirmAction, cfg.ContextRotation.DefaultConfirmAction)

	// Ensemble defaults
	addDiff("ensemble.default_ensemble", defaults.Ensemble.DefaultEnsemble, cfg.Ensemble.DefaultEnsemble)
	addDiff("ensemble.agent_mix", defaults.Ensemble.AgentMix, cfg.Ensemble.AgentMix)
	addDiff("ensemble.assignment", defaults.Ensemble.Assignment, cfg.Ensemble.Assignment)
	addDiff("ensemble.mode_tier_default", defaults.Ensemble.ModeTierDefault, cfg.Ensemble.ModeTierDefault)
	addDiff("ensemble.allow_advanced", defaults.Ensemble.AllowAdvanced, cfg.Ensemble.AllowAdvanced)
	addDiff("ensemble.synthesis.strategy", defaults.Ensemble.Synthesis.Strategy, cfg.Ensemble.Synthesis.Strategy)
	addDiff("ensemble.synthesis.min_confidence", defaults.Ensemble.Synthesis.MinConfidence, cfg.Ensemble.Synthesis.MinConfidence)
	addDiff("ensemble.synthesis.max_findings", defaults.Ensemble.Synthesis.MaxFindings, cfg.Ensemble.Synthesis.MaxFindings)
	addDiff("ensemble.synthesis.include_raw_outputs", defaults.Ensemble.Synthesis.IncludeRawOutputs, cfg.Ensemble.Synthesis.IncludeRawOutputs)
	addDiff("ensemble.synthesis.conflict_resolution", defaults.Ensemble.Synthesis.ConflictResolution, cfg.Ensemble.Synthesis.ConflictResolution)
	addDiff("ensemble.cache.enabled", defaults.Ensemble.Cache.Enabled, cfg.Ensemble.Cache.Enabled)
	addDiff("ensemble.cache.ttl_minutes", defaults.Ensemble.Cache.TTLMinutes, cfg.Ensemble.Cache.TTLMinutes)
	addDiff("ensemble.cache.cache_dir", defaults.Ensemble.Cache.CacheDir, cfg.Ensemble.Cache.CacheDir)
	addDiff("ensemble.cache.max_entries", defaults.Ensemble.Cache.MaxEntries, cfg.Ensemble.Cache.MaxEntries)
	addDiff("ensemble.cache.share_across_modes", defaults.Ensemble.Cache.ShareAcrossModes, cfg.Ensemble.Cache.ShareAcrossModes)
	addDiff("ensemble.budget.per_agent", defaults.Ensemble.Budget.PerAgent, cfg.Ensemble.Budget.PerAgent)
	addDiff("ensemble.budget.total", defaults.Ensemble.Budget.Total, cfg.Ensemble.Budget.Total)
	addDiff("ensemble.budget.synthesis", defaults.Ensemble.Budget.Synthesis, cfg.Ensemble.Budget.Synthesis)
	addDiff("ensemble.budget.context_pack", defaults.Ensemble.Budget.ContextPack, cfg.Ensemble.Budget.ContextPack)
	addDiff("ensemble.early_stop.enabled", defaults.Ensemble.EarlyStop.Enabled, cfg.Ensemble.EarlyStop.Enabled)
	addDiff("ensemble.early_stop.min_agents", defaults.Ensemble.EarlyStop.MinAgents, cfg.Ensemble.EarlyStop.MinAgents)
	addDiff("ensemble.early_stop.findings_threshold", defaults.Ensemble.EarlyStop.FindingsThreshold, cfg.Ensemble.EarlyStop.FindingsThreshold)
	addDiff("ensemble.early_stop.similarity_threshold", defaults.Ensemble.EarlyStop.SimilarityThreshold, cfg.Ensemble.EarlyStop.SimilarityThreshold)
	addDiff("ensemble.early_stop.window_size", defaults.Ensemble.EarlyStop.WindowSize, cfg.Ensemble.EarlyStop.WindowSize)

	// CASS
	addDiff("cass.enabled", defaults.CASS.Enabled, cfg.CASS.Enabled)
	addDiff("cass.binary_path", defaults.CASS.BinaryPath, cfg.CASS.BinaryPath)
	addDiff("cass.timeout", defaults.CASS.Timeout, cfg.CASS.Timeout)

	// CASS Context
	addDiff("cass.context.enabled", defaults.CASS.Context.Enabled, cfg.CASS.Context.Enabled)
	addDiff("cass.context.max_sessions", defaults.CASS.Context.MaxSessions, cfg.CASS.Context.MaxSessions)
	addDiff("cass.context.lookback_days", defaults.CASS.Context.LookbackDays, cfg.CASS.Context.LookbackDays)
	addDiff("cass.context.max_tokens", defaults.CASS.Context.MaxTokens, cfg.CASS.Context.MaxTokens)
	addDiff("cass.context.min_relevance", defaults.CASS.Context.MinRelevance, cfg.CASS.Context.MinRelevance)
	addDiff("cass.context.skip_if_context_above", defaults.CASS.Context.SkipIfContextAbove, cfg.CASS.Context.SkipIfContextAbove)
	addDiff("cass.context.prefer_same_project", defaults.CASS.Context.PreferSameProject, cfg.CASS.Context.PreferSameProject)

	// Scanner
	addDiff("scanner.ubs_path", defaults.Scanner.UBSPath, cfg.Scanner.UBSPath)

	// Bugs (UBS push routing)
	addDiff("bugs.push_routing", defaults.Bugs.PushRouting, cfg.Bugs.PushRouting)
	addDiff("bugs.interval", defaults.Bugs.Interval, cfg.Bugs.Interval)
	addDiff("bugs.cooldown_minutes", defaults.Bugs.CooldownMinutes, cfg.Bugs.CooldownMinutes)

	// Rotation
	addDiff("rotation.enabled", defaults.Rotation.Enabled, cfg.Rotation.Enabled)
	addDiff("rotation.auto_open_browser", defaults.Rotation.AutoOpenBrowser, cfg.Rotation.AutoOpenBrowser)
	addDiff("rotation.auto_trigger", defaults.Rotation.AutoTrigger, cfg.Rotation.AutoTrigger)
	addDiff("rotation.auto_initiate", defaults.Rotation.AutoInitiate, cfg.Rotation.AutoInitiate)
	addDiff("rotation.continuation_prompt", defaults.Rotation.ContinuationPrompt, cfg.Rotation.ContinuationPrompt)
	addDiff("rotation.accounts", defaults.Rotation.Accounts, cfg.Rotation.Accounts)
	addDiff("rotation.usage_percent_threshold", defaults.Rotation.UsagePercentThreshold, cfg.Rotation.UsagePercentThreshold)
	addDiff("rotation.auto_confirm", defaults.Rotation.AutoConfirm, cfg.Rotation.AutoConfirm)
	addDiff("rotation.thresholds.warning_percent", defaults.Rotation.Thresholds.WarningPercent, cfg.Rotation.Thresholds.WarningPercent)
	addDiff("rotation.thresholds.critical_percent", defaults.Rotation.Thresholds.CriticalPercent, cfg.Rotation.Thresholds.CriticalPercent)
	addDiff("rotation.thresholds.restart_if_tokens_above", defaults.Rotation.Thresholds.RestartIfTokensAbove, cfg.Rotation.Thresholds.RestartIfTokensAbove)
	addDiff("rotation.thresholds.restart_if_session_hours", defaults.Rotation.Thresholds.RestartIfSessionHours, cfg.Rotation.Thresholds.RestartIfSessionHours)

	// Gemini setup
	addDiff("gemini_setup.auto_select_pro_model", defaults.GeminiSetup.AutoSelectProModel, cfg.GeminiSetup.AutoSelectProModel)
	addDiff("gemini_setup.ready_timeout_seconds", defaults.GeminiSetup.ReadyTimeoutSeconds, cfg.GeminiSetup.ReadyTimeoutSeconds)
	addDiff("gemini_setup.model_select_timeout_seconds", defaults.GeminiSetup.ModelSelectTimeoutSeconds, cfg.GeminiSetup.ModelSelectTimeoutSeconds)
	addDiff("gemini_setup.verbose", defaults.GeminiSetup.Verbose, cfg.GeminiSetup.Verbose)

	// Swarm
	addDiff("swarm.enabled", defaults.Swarm.Enabled, cfg.Swarm.Enabled)
	addDiff("swarm.default_scan_dir", defaults.Swarm.DefaultScanDir, cfg.Swarm.DefaultScanDir)
	addDiff("swarm.tier1_threshold", defaults.Swarm.Tier1Threshold, cfg.Swarm.Tier1Threshold)
	addDiff("swarm.tier2_threshold", defaults.Swarm.Tier2Threshold, cfg.Swarm.Tier2Threshold)
	addDiff("swarm.tier1_allocation", defaults.Swarm.Tier1Allocation, cfg.Swarm.Tier1Allocation)
	addDiff("swarm.tier2_allocation", defaults.Swarm.Tier2Allocation, cfg.Swarm.Tier2Allocation)
	addDiff("swarm.tier3_allocation", defaults.Swarm.Tier3Allocation, cfg.Swarm.Tier3Allocation)
	addDiff("swarm.sessions_per_type", defaults.Swarm.SessionsPerType, cfg.Swarm.SessionsPerType)
	addDiff("swarm.panes_per_session", defaults.Swarm.PanesPerSession, cfg.Swarm.PanesPerSession)
	addDiff("swarm.stagger_delay_ms", defaults.Swarm.StaggerDelayMs, cfg.Swarm.StaggerDelayMs)
	addDiff("swarm.auto_rotate_accounts", defaults.Swarm.AutoRotateAccounts, cfg.Swarm.AutoRotateAccounts)

	// Spawn pacing
	addDiff("spawn_pacing.enabled", defaults.SpawnPacing.Enabled, cfg.SpawnPacing.Enabled)
	addDiff("spawn_pacing.max_concurrent_spawns", defaults.SpawnPacing.MaxConcurrentSpawns, cfg.SpawnPacing.MaxConcurrentSpawns)
	addDiff("spawn_pacing.agent_caps", defaults.SpawnPacing.AgentCaps, cfg.SpawnPacing.AgentCaps)

	// Coordinator
	addDiff("coordinator.poll_interval", defaults.Coordinator.PollInterval, cfg.Coordinator.PollInterval)
	addDiff("coordinator.digest_interval", defaults.Coordinator.DigestInterval, cfg.Coordinator.DigestInterval)
	addDiff("coordinator.auto_assign", defaults.Coordinator.AutoAssign, cfg.Coordinator.AutoAssign)
	addDiff("coordinator.idle_threshold", defaults.Coordinator.IdleThreshold, cfg.Coordinator.IdleThreshold)
	addDiff("coordinator.assign_only_idle", defaults.Coordinator.AssignOnlyIdle, cfg.Coordinator.AssignOnlyIdle)
	addDiff("coordinator.conflict_notify", defaults.Coordinator.ConflictNotify, cfg.Coordinator.ConflictNotify)
	addDiff("coordinator.conflict_negotiate", defaults.Coordinator.ConflictNegotiate, cfg.Coordinator.ConflictNegotiate)
	addDiff("coordinator.send_digests", defaults.Coordinator.SendDigests, cfg.Coordinator.SendDigests)
	addDiff("coordinator.human_agent", defaults.Coordinator.HumanAgent, cfg.Coordinator.HumanAgent)
	addDiff("coordinator.mail_nudge", defaults.Coordinator.MailNudge, cfg.Coordinator.MailNudge)
	addDiff("coordinator.nudge_cooldown_seconds", defaults.Coordinator.NudgeCooldownSeconds, cfg.Coordinator.NudgeCooldownSeconds)
	addDiff("coordinator.nudge_message", defaults.Coordinator.NudgeMessage, cfg.Coordinator.NudgeMessage)

	return diffs
}

// Validate checks the configuration for errors and returns all issues found
func Validate(cfg *Config) []error {
	if cfg == nil {
		return []error{fmt.Errorf("config is nil")}
	}

	var errs []error

	// Validate context rotation
	if err := ValidateContextRotationConfig(&cfg.ContextRotation); err != nil {
		errs = append(errs, fmt.Errorf("context_rotation: %w", err))
	}

	// Validate assign config
	if err := ValidateAssignConfig(&cfg.Assign); err != nil {
		errs = append(errs, fmt.Errorf("assign: %w", err))
	}

	// Validate ensemble defaults
	if err := ValidateEnsembleConfig(&cfg.Ensemble); err != nil {
		errs = append(errs, fmt.Errorf("ensemble: %w", err))
	}

	// Validate tmux activity indicators
	// Validate robot output config
	if err := ValidateRobotOutputConfig(&cfg.Robot.Output); err != nil {
		errs = append(errs, fmt.Errorf("robot.output: %w", err))
	}

	// Validate DCG integration config
	if err := ValidateDCGConfig(&cfg.Integrations.DCG); err != nil {
		errs = append(errs, fmt.Errorf("integrations.dcg: %w", err))
	}

	// Validate CAAM integration config
	if err := ValidateCAAMConfig(&cfg.Integrations.CAAM); err != nil {
		errs = append(errs, fmt.Errorf("integrations.caam: %w", err))
	}

	// Validate ProcessTriage integration config
	if err := ValidateProcessTriageConfig(&cfg.Integrations.ProcessTriage); err != nil {
		errs = append(errs, fmt.Errorf("integrations.process_triage: %w", err))
	}

	// Validate BV integration config
	if err := ValidateBVConfig(&cfg.Integrations.BV); err != nil {
		errs = append(errs, fmt.Errorf("integrations.bv: %w", err))
	}

	// Validate rano integration config
	if err := ValidateRanoConfig(&cfg.Integrations.Rano); err != nil {
		errs = append(errs, fmt.Errorf("integrations.rano: %w", err))
	}

	if err := ValidateCommandHooks(cfg.CommandHooks); err != nil {
		errs = append(errs, fmt.Errorf("command_hooks: %w", err))
	}

	// Validate safety profile and preflight configuration
	if err := ValidateSafetyConfig(&cfg.Safety); err != nil {
		errs = append(errs, fmt.Errorf("safety: %w", err))
	}
	if err := ValidatePreflightConfig(&cfg.Preflight); err != nil {
		errs = append(errs, fmt.Errorf("preflight: %w", err))
	}

	// Validate redaction configuration
	if err := ValidateRedactionConfig(&cfg.Redaction); err != nil {
		errs = append(errs, fmt.Errorf("redaction: %w", err))
	}

	// Validate privacy configuration
	if err := ValidatePrivacyConfig(&cfg.Privacy); err != nil {
		errs = append(errs, fmt.Errorf("privacy: %w", err))
	}

	// Validate encryption configuration
	if err := ValidateEncryptionConfig(&cfg.Encryption); err != nil {
		errs = append(errs, fmt.Errorf("encryption: %w", err))
	}

	// Validate spawn pacing config
	if err := ValidateSpawnPacingConfig(&cfg.SpawnPacing); err != nil {
		errs = append(errs, fmt.Errorf("spawn_pacing: %w", err))
	}

	// Validate file reservation config
	if err := ValidateFileReservationConfig(&cfg.FileReservation); err != nil {
		errs = append(errs, fmt.Errorf("file_reservation: %w", err))
	}

	// Validate scanner config
	if err := ValidateScannerConfig(&cfg.Scanner); err != nil {
		errs = append(errs, fmt.Errorf("scanner: %w", err))
	}

	// Validate account rotation config
	if err := ValidateRotationConfig(&cfg.Rotation); err != nil {
		errs = append(errs, fmt.Errorf("rotation: %w", err))
	}

	// Validate Gemini post-spawn setup config
	if err := ValidateGeminiSetupConfig(&cfg.GeminiSetup); err != nil {
		errs = append(errs, fmt.Errorf("gemini_setup: %w", err))
	}

	// Validate memory config
	if err := ValidateMemoryConfig(&cfg.Memory); err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
	}

	if cfg.SessionRecovery.MaxRecoveryTokens < 0 {
		errs = append(errs, fmt.Errorf("recovery.max_recovery_tokens: must be non-negative, got %d", cfg.SessionRecovery.MaxRecoveryTokens))
	}
	if cfg.SessionRecovery.MaxCMRules < 0 {
		errs = append(errs, fmt.Errorf("recovery.max_cm_rules: must be non-negative, got %d", cfg.SessionRecovery.MaxCMRules))
	}
	if cfg.SessionRecovery.MaxCMSnippets < 0 {
		errs = append(errs, fmt.Errorf("recovery.max_cm_snippets: must be non-negative, got %d", cfg.SessionRecovery.MaxCMSnippets))
	}
	if cfg.SessionRecovery.TimeoutSeconds < 0 {
		errs = append(errs, fmt.Errorf("recovery.timeout_seconds: must be non-negative, got %d", cfg.SessionRecovery.TimeoutSeconds))
	}
	if cfg.Cleanup.MaxAgeHours < 0 {
		errs = append(errs, fmt.Errorf("cleanup.max_age_hours: must be non-negative, got %d", cfg.Cleanup.MaxAgeHours))
	}
	if cfg.Assign.Strategy != "" && !IsValidStrategy(cfg.Assign.Strategy) {
		errs = append(errs, fmt.Errorf("assign.strategy: must be one of %s, got %q", strings.Join(ValidAssignStrategies, ", "), cfg.Assign.Strategy))
	}

	// Validate swarm config
	if err := ValidateSwarmConfig(&cfg.Swarm); err != nil {
		errs = append(errs, fmt.Errorf("swarm: %w", err))
	}

	// Validate projects_base if set
	if cfg.ProjectsBase != "" {
		expanded := ExpandHome(cfg.ProjectsBase)
		if !filepath.IsAbs(expanded) {
			errs = append(errs, fmt.Errorf("projects_base: must be an absolute path, got %q", cfg.ProjectsBase))
		}
	}

	if cfg.HelpVerbosity != "" {
		switch strings.ToLower(strings.TrimSpace(cfg.HelpVerbosity)) {
		case "minimal", "full":
			// ok
		default:
			errs = append(errs, fmt.Errorf("help_verbosity: must be \"minimal\" or \"full\", got %q", cfg.HelpVerbosity))
		}
	}

	// Validate alerts thresholds
	if cfg.Alerts.AgentStuckMinutes < 0 {
		errs = append(errs, fmt.Errorf("alerts.agent_stuck_minutes: must be non-negative, got %d", cfg.Alerts.AgentStuckMinutes))
	}
	if cfg.Alerts.DiskLowThresholdGB < 0 {
		errs = append(errs, fmt.Errorf("alerts.disk_low_threshold_gb: must be non-negative, got %.1f", cfg.Alerts.DiskLowThresholdGB))
	}
	if cfg.Alerts.DiskFullHorizonHours < 0 {
		errs = append(errs, fmt.Errorf("alerts.disk_full_horizon_hours: must be non-negative, got %.1f", cfg.Alerts.DiskFullHorizonHours))
	}
	if cfg.Alerts.MailBacklogThreshold < 0 {
		errs = append(errs, fmt.Errorf("alerts.mail_backlog_threshold: must be non-negative, got %d", cfg.Alerts.MailBacklogThreshold))
	}
	if cfg.Alerts.BeadStaleHours < 0 {
		errs = append(errs, fmt.Errorf("alerts.bead_stale_hours: must be non-negative, got %d", cfg.Alerts.BeadStaleHours))
	}
	if cfg.Alerts.ContextWarningThreshold < 0 || cfg.Alerts.ContextWarningThreshold > 100 {
		errs = append(errs, fmt.Errorf("alerts.context_warning_threshold: must be between 0 and 100, got %.1f", cfg.Alerts.ContextWarningThreshold))
	}
	if cfg.Alerts.ResolvedPruneMinutes < 0 {
		errs = append(errs, fmt.Errorf("alerts.resolved_prune_minutes: must be non-negative, got %d", cfg.Alerts.ResolvedPruneMinutes))
	}

	// Validate checkpoints
	if cfg.Checkpoints.MaxAutoCheckpoints < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.max_auto_checkpoints: must be non-negative, got %d", cfg.Checkpoints.MaxAutoCheckpoints))
	}
	if cfg.Checkpoints.BeforeAddAgents < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.before_add_agents: must be non-negative, got %d", cfg.Checkpoints.BeforeAddAgents))
	}
	if cfg.Checkpoints.ScrollbackLines < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.scrollback_lines: must be non-negative, got %d", cfg.Checkpoints.ScrollbackLines))
	}

	// Validate resilience
	if cfg.Resilience.MaxRestarts < 0 {
		errs = append(errs, fmt.Errorf("resilience.max_restarts: must be non-negative, got %d", cfg.Resilience.MaxRestarts))
	}
	if cfg.Resilience.RestartDelaySeconds < 0 {
		errs = append(errs, fmt.Errorf("resilience.restart_delay_seconds: must be non-negative, got %d", cfg.Resilience.RestartDelaySeconds))
	}
	if cfg.Resilience.HealthCheckSeconds < 0 {
		errs = append(errs, fmt.Errorf("resilience.health_check_seconds: must be non-negative, got %d", cfg.Resilience.HealthCheckSeconds))
	}
	if cfg.Resilience.CrashThreshold < 0 {
		errs = append(errs, fmt.Errorf("resilience.crash_threshold: must be non-negative, got %d", cfg.Resilience.CrashThreshold))
	}

	// Validate CASS timeout
	if cfg.CASS.Timeout < 0 {
		errs = append(errs, fmt.Errorf("cass.timeout: must be non-negative, got %d", cfg.CASS.Timeout))
	}

	// Validate CASS context settings
	if cfg.CASS.Context.MinRelevance < 0 || cfg.CASS.Context.MinRelevance > 1 {
		errs = append(errs, fmt.Errorf("cass.context.min_relevance: must be between 0.0 and 1.0, got %.2f", cfg.CASS.Context.MinRelevance))
	}
	if cfg.CASS.Context.SkipIfContextAbove < 0 || cfg.CASS.Context.SkipIfContextAbove > 100 {
		errs = append(errs, fmt.Errorf("cass.context.skip_if_context_above: must be between 0 and 100, got %.0f", cfg.CASS.Context.SkipIfContextAbove))
	}
	if cfg.CASS.Context.MaxSessions < 0 {
		errs = append(errs, fmt.Errorf("cass.context.max_sessions: must be non-negative, got %d", cfg.CASS.Context.MaxSessions))
	}
	if cfg.CASS.Context.MaxTokens < 0 {
		errs = append(errs, fmt.Errorf("cass.context.max_tokens: must be non-negative, got %d", cfg.CASS.Context.MaxTokens))
	}
	if cfg.CASS.Context.LookbackDays < 0 {
		errs = append(errs, fmt.Errorf("cass.context.lookback_days: must be non-negative, got %d", cfg.CASS.Context.LookbackDays))
	}
	// Validate tmux settings
	if cfg.Tmux.DefaultPanes < 1 {
		errs = append(errs, fmt.Errorf("tmux.default_panes: must be at least 1, got %d", cfg.Tmux.DefaultPanes))
	}
	if cfg.Tmux.PaneInitDelayMs < 0 {
		errs = append(errs, fmt.Errorf("tmux.pane_init_delay_ms: must be non-negative, got %d", cfg.Tmux.PaneInitDelayMs))
	}
	if cfg.Tmux.HistoryLimit < 0 {
		errs = append(errs, fmt.Errorf("tmux.history_limit: must be non-negative, got %d", cfg.Tmux.HistoryLimit))
	}

	return errs
}
