package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
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
	ProjectsBase       string                `toml:"projects_base"`
	Theme              string                `toml:"theme"`               // UI Theme (mocha, macchiato, nord, latte, auto)
	HelpVerbosity      string                `toml:"help_verbosity"`      // Help verbosity: minimal or full (default: full)
	PaletteFile        string                `toml:"palette_file"`        // Path to command_palette.md (optional)
	SuggestionsEnabled bool                  `toml:"suggestions_enabled"` // Show contextual CLI suggestions
	Agents             AgentConfig           `toml:"agents"`
	Palette            []PaletteCmd          `toml:"palette"`
	PaletteState       PaletteState          `toml:"palette_state"`
	Tmux               TmuxConfig            `toml:"tmux"`
	Robot              RobotConfig           `toml:"robot"`
	AgentMail          AgentMailConfig       `toml:"agent_mail"`
	Integrations       IntegrationsConfig    `toml:"integrations"` // External tool integrations (dcg, caam, etc.)
	Models             ModelsConfig          `toml:"models"`
	Alerts             AlertsConfig          `toml:"alerts"`
	Checkpoints        CheckpointsConfig     `toml:"checkpoints"`
	Notifications      notify.Config         `toml:"notifications"`
	Resilience         ResilienceConfig      `toml:"resilience"`
	Health             HealthConfig          `toml:"health"`           // Health monitoring configuration
	Scanner            ScannerConfig         `toml:"scanner"`          // UBS scanner configuration
	CASS               CASSConfig            `toml:"cass"`             // CASS integration configuration
	Accounts           AccountsConfig        `toml:"accounts"`         // Multi-account management
	Rotation           RotationConfig        `toml:"rotation"`         // Account rotation configuration
	GeminiSetup        GeminiSetupConfig     `toml:"gemini_setup"`     // Gemini post-spawn setup
	ContextRotation    ContextRotationConfig `toml:"context_rotation"` // Context window rotation
	SessionRecovery    SessionRecoveryConfig `toml:"recovery"`         // Smart session recovery
	Cleanup            CleanupConfig         `toml:"cleanup"`          // Temp file cleanup configuration
	FileReservation    FileReservationConfig `toml:"file_reservation"` // Auto file reservation via Agent Mail
	Memory             MemoryConfig          `toml:"memory"`           // CASS Memory (cm) integration
	Assign             AssignConfig          `toml:"assign"`           // Assignment strategy configuration
	Ensemble           EnsembleConfig        `toml:"ensemble"`         // Reasoning ensemble defaults
	Swarm              SwarmConfig           `toml:"swarm"`            // Weighted multi-project agent swarm
	SpawnPacing        SpawnPacingConfig     `toml:"spawn_pacing"`     // Spawn scheduler pacing configuration
	Redaction          RedactionConfig       `toml:"redaction"`        // Secrets/PII redaction configuration

	// Runtime-only fields (populated by project config merging)
	ProjectDefaults map[string]int `toml:"-"`
}

// RobotConfig holds defaults for robot output behavior.
type RobotConfig struct {
	Verbosity string            `toml:"verbosity"` // terse, default, or debug
	Output    RobotOutputConfig `toml:"output"`    // Output format configuration
}

// RobotOutputConfig holds configuration for robot mode output format.
type RobotOutputConfig struct {
	Format     string `toml:"format"`     // Output format: "json" or "toon"
	Pretty     bool   `toml:"pretty"`     // Pretty print output (adds whitespace for readability)
	Timestamps bool   `toml:"timestamps"` // Include timestamps in output
	Compress   bool   `toml:"compress"`   // Compression for large outputs
}

// DefaultRobotOutputConfig returns sensible robot output defaults.
func DefaultRobotOutputConfig() RobotOutputConfig {
	return RobotOutputConfig{
		Format:     "json", // JSON for backwards compatibility
		Pretty:     false,  // Compact by default
		Timestamps: true,   // Include timestamps
		Compress:   false,  // No compression by default
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
	}
}

// CheckpointsConfig holds configuration for automatic checkpoints
type CheckpointsConfig struct {
	Enabled               bool `toml:"enabled"`                  // Master toggle for auto-checkpoints
	BeforeBroadcast       bool `toml:"before_broadcast"`         // Auto-checkpoint before sending to all agents
	BeforeAddAgents       int  `toml:"before_add_agents"`        // Auto-checkpoint when adding >= N agents (0 = disabled)
	MaxAutoCheckpoints    int  `toml:"max_auto_checkpoints"`     // Max auto-checkpoints per session (rotation)
	ScrollbackLines       int  `toml:"scrollback_lines"`         // Lines of scrollback to capture
	IncludeGit            bool `toml:"include_git"`              // Capture git state in auto-checkpoints
	AutoCheckpointOnSpawn bool `toml:"auto_checkpoint_on_spawn"` // Auto-checkpoint when spawning session
	IntervalMinutes       int  `toml:"interval_minutes"`         // Periodic checkpoint interval (0 = disabled)
	OnRotation            bool `toml:"on_rotation"`              // Checkpoint before context rotation
	OnError               bool `toml:"on_error"`                 // Checkpoint when agent error detected
}

// DefaultCheckpointsConfig returns sensible checkpoint defaults
func DefaultCheckpointsConfig() CheckpointsConfig {
	return CheckpointsConfig{
		Enabled:               true,
		BeforeBroadcast:       true,
		BeforeAddAgents:       3,  // Auto-checkpoint when adding 3+ agents
		MaxAutoCheckpoints:    10, // Keep last 10 auto-checkpoints per session
		ScrollbackLines:       500,
		IncludeGit:            true,
		AutoCheckpointOnSpawn: false, // Don't checkpoint empty sessions by default
		IntervalMinutes:       0,     // Disabled by default (no periodic checkpoints)
		OnRotation:            true,  // Checkpoint before rotation by default
		OnError:               true,  // Checkpoint on error by default
	}
}

// AlertsConfig holds configuration for the alert system
type AlertsConfig struct {
	Enabled              bool    `toml:"enabled"`                // Master toggle for alerts
	AgentStuckMinutes    int     `toml:"agent_stuck_minutes"`    // Minutes without output before alerting
	DiskLowThresholdGB   float64 `toml:"disk_low_threshold_gb"`  // Minimum free disk space (GB)
	MailBacklogThreshold int     `toml:"mail_backlog_threshold"` // Unread messages before alerting
	BeadStaleHours       int     `toml:"bead_stale_hours"`       // Hours before in-progress bead is stale
	ResolvedPruneMinutes int     `toml:"resolved_prune_minutes"` // How long to keep resolved alerts
}

// DefaultAlertsConfig returns sensible alert defaults
func DefaultAlertsConfig() AlertsConfig {
	return AlertsConfig{
		Enabled:              true,
		AgentStuckMinutes:    5,
		DiskLowThresholdGB:   5.0,
		MailBacklogThreshold: 10,
		BeadStaleHours:       24,
		ResolvedPruneMinutes: 60,
	}
}

// ResilienceConfig holds configuration for agent auto-restart and recovery
type ResilienceConfig struct {
	AutoRestart         bool            `toml:"auto_restart"`           // Enable automatic agent restart on crash
	MaxRestarts         int             `toml:"max_restarts"`           // Max restarts per agent before giving up
	RestartDelaySeconds int             `toml:"restart_delay_seconds"`  // Seconds to wait before restarting
	HealthCheckSeconds  int             `toml:"health_check_seconds"`   // Seconds between health checks
	NotifyOnCrash       bool            `toml:"notify_on_crash"`        // Send notification when agent crashes
	NotifyOnMaxRestarts bool            `toml:"notify_on_max_restarts"` // Notify when max restarts exceeded
	RateLimit           RateLimitConfig `toml:"rate_limit"`             // Rate limit detection configuration
}

// RateLimitConfig holds configuration for rate limit detection
type RateLimitConfig struct {
	Detect   bool     `toml:"detect"`   // Enable rate limit detection
	Notify   bool     `toml:"notify"`   // Send notification on rate limit
	Patterns []string `toml:"patterns"` // Custom patterns to detect (in addition to defaults)
}

// DefaultResilienceConfig returns sensible resilience defaults
func DefaultResilienceConfig() ResilienceConfig {
	return ResilienceConfig{
		AutoRestart:         false, // Disabled by default, opt-in via --auto-restart
		MaxRestarts:         3,     // Stop after 3 restart attempts
		RestartDelaySeconds: 30,    // Wait 30 seconds before restarting
		HealthCheckSeconds:  10,    // Check health every 10 seconds
		NotifyOnCrash:       true,  // Notify on crash by default
		NotifyOnMaxRestarts: true,  // Notify when max restarts exceeded
		RateLimit: RateLimitConfig{
			Detect:   true, // Detect rate limits by default
			Notify:   true, // Notify on rate limit by default
			Patterns: nil,  // Use default patterns (rate limit, 429, too many requests, quota exceeded)
		},
	}
}

// HealthConfig holds configuration for agent health monitoring.
// This is separate from ResilienceConfig which handles crash recovery;
// HealthConfig focuses on proactive monitoring and stall detection.
type HealthConfig struct {
	Enabled            bool `toml:"enabled"`              // Master toggle for health monitoring
	CheckInterval      int  `toml:"check_interval"`       // Seconds between health checks
	StallThreshold     int  `toml:"stall_threshold"`      // Seconds without output before agent is stalled
	AutoRestart        bool `toml:"auto_restart"`         // Auto-restart on unhealthy state
	MaxRestarts        int  `toml:"max_restarts"`         // Max restart attempts before giving up
	RestartBackoffBase int  `toml:"restart_backoff_base"` // Initial restart delay (seconds)
	RestartBackoffMax  int  `toml:"restart_backoff_max"`  // Maximum restart delay (seconds)
}

// DefaultHealthConfig returns sensible health monitoring defaults
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Enabled:            true,  // Health monitoring enabled by default
		CheckInterval:      10,    // Check every 10 seconds
		StallThreshold:     300,   // 5 minutes without output = stalled
		AutoRestart:        false, // Disabled by default, opt-in
		MaxRestarts:        3,     // Stop after 3 restart attempts
		RestartBackoffBase: 30,    // Initial 30 second delay
		RestartBackoffMax:  300,   // Max 5 minute delay (exponential backoff)
	}
}

// ValidateHealthConfig validates the health monitoring configuration
func ValidateHealthConfig(cfg *HealthConfig) error {
	if cfg.CheckInterval < 1 {
		return fmt.Errorf("check_interval must be at least 1 second, got %d", cfg.CheckInterval)
	}
	if cfg.StallThreshold < cfg.CheckInterval {
		return fmt.Errorf("stall_threshold (%d) must be >= check_interval (%d)",
			cfg.StallThreshold, cfg.CheckInterval)
	}
	if cfg.MaxRestarts < 0 {
		return fmt.Errorf("max_restarts must be non-negative, got %d", cfg.MaxRestarts)
	}
	if cfg.RestartBackoffBase < 1 {
		return fmt.Errorf("restart_backoff_base must be at least 1 second, got %d", cfg.RestartBackoffBase)
	}
	if cfg.RestartBackoffMax < cfg.RestartBackoffBase {
		return fmt.Errorf("restart_backoff_max (%d) must be >= restart_backoff_base (%d)",
			cfg.RestartBackoffMax, cfg.RestartBackoffBase)
	}
	return nil
}

// AccountEntry represents a single account for a provider
type AccountEntry struct {
	Email    string `toml:"email"`
	Alias    string `toml:"alias"`
	Priority int    `toml:"priority"`
}

// AccountsConfig holds multi-account management configuration
type AccountsConfig struct {
	StateFile          string         `toml:"state_file"`           // Path to account state JSON
	AutoRotate         bool           `toml:"auto_rotate"`          // Auto-rotate on limit detection
	ResetBufferMinutes int            `toml:"reset_buffer_minutes"` // Minutes before reset to consider available
	Claude             []AccountEntry `toml:"claude"`               // Claude accounts
	Codex              []AccountEntry `toml:"codex"`                // Codex accounts
	Gemini             []AccountEntry `toml:"gemini"`               // Gemini accounts
}

// DefaultAccountsConfig returns the default accounts configuration
func DefaultAccountsConfig() AccountsConfig {
	return AccountsConfig{
		StateFile:          "~/.config/ntm/account_state.json",
		AutoRotate:         true,
		ResetBufferMinutes: 15,
		Claude:             nil,
		Codex:              nil,
		Gemini:             nil,
	}
}

// RotationAccount represents a configured account for rotation
type RotationAccount struct {
	Provider string `toml:"provider"` // claude, codex, gemini
	Email    string `toml:"email"`    // Account email
	Alias    string `toml:"alias"`    // Short name for display (optional)
	Priority int    `toml:"priority"` // Lower = higher priority (optional, default by order)
}

// RotationThresholds defines when to trigger account rotation
type RotationThresholds struct {
	WarningPercent        int     `toml:"warning_percent"`          // Show warning at this quota %
	CriticalPercent       int     `toml:"critical_percent"`         // Consider limited at this %
	RestartIfTokensAbove  float64 `toml:"restart_if_tokens_above"`  // Restart if tokens exceed this
	RestartIfSessionHours int     `toml:"restart_if_session_hours"` // Restart after N hours
}

// RotationDashboard defines dashboard display settings for rotation
type RotationDashboard struct {
	ShowQuotaBars     bool `toml:"show_quota_bars"`     // Show quota bars in dashboard
	ShowAccountStatus bool `toml:"show_account_status"` // Show account status
	ShowResetTimers   bool `toml:"show_reset_timers"`   // Show reset countdown
}

// RotationConfig holds account rotation configuration
type RotationConfig struct {
	Enabled            bool               `toml:"enabled"`             // Master toggle
	PreferRestart      bool               `toml:"prefer_restart"`      // Prefer restart over switch
	AutoOpenBrowser    bool               `toml:"auto_open_browser"`   // Auto-open browser for auth
	AutoTrigger        bool               `toml:"auto_trigger"`        // Show notification when rate limit detected
	AutoInitiate       bool               `toml:"auto_initiate"`       // Automatically start rotation (aggressive)
	ContinuationPrompt string             `toml:"continuation_prompt"` // Prompt template on rotation
	Accounts           []RotationAccount  `toml:"accounts"`            // Configured accounts per provider
	Thresholds         RotationThresholds `toml:"thresholds"`
	Dashboard          RotationDashboard  `toml:"dashboard"`
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
		PreferRestart:      true,  // Restart is cleaner than switch
		AutoOpenBrowser:    false, // Don't auto-open browser
		ContinuationPrompt: "Continue where you left off. Previous context: {{.Context}}",
		Thresholds: RotationThresholds{
			WarningPercent:        80,
			CriticalPercent:       95,
			RestartIfTokensAbove:  100000,
			RestartIfSessionHours: 8,
		},
		Dashboard: RotationDashboard{
			ShowQuotaBars:     true,
			ShowAccountStatus: true,
			ShowResetTimers:   true,
		},
	}
}

// CASSConfig holds configuration for CASS (Coding Agent Session Search) integration
type CASSConfig struct {
	Enabled          bool   `toml:"enabled"`            // Master switch - disable all CASS features
	ShowInstallHints bool   `toml:"show_install_hints"` // Show installation hints when CASS not found
	BinaryPath       string `toml:"binary_path"`        // Path to cass binary (auto-detect from PATH if empty)
	Timeout          int    `toml:"timeout"`            // Timeout for CASS operations (seconds)

	Context    CASSContextConfig   `toml:"context"`    // Context injection settings
	Duplicates CASSDuplicateConfig `toml:"duplicates"` // Duplicate detection settings
	Search     CASSSearchConfig    `toml:"search"`     // Search defaults
	TUI        CASSTUIConfig       `toml:"tui"`        // TUI settings
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

// CASSDuplicateConfig holds settings for duplicate detection
type CASSDuplicateConfig struct {
	Enabled             bool    `toml:"enabled"`              // Check for duplicates before sending
	SimilarityThreshold float64 `toml:"similarity_threshold"` // 0-1, higher = stricter matching
	LookbackDays        int     `toml:"lookback_days"`        // How far back to check
	PromptOnMatch       bool    `toml:"prompt_on_match"`      // Ask user before proceeding
}

// CASSSearchConfig holds default search settings
type CASSSearchConfig struct {
	DefaultLimit  int    `toml:"default_limit"`  // Default number of search results
	DefaultFields string `toml:"default_fields"` // Default field selection
	IncludeMeta   bool   `toml:"include_meta"`   // Include metadata in results
}

// CASSTUIConfig holds TUI-related CASS settings
type CASSTUIConfig struct {
	ShowActivitySparkline bool `toml:"show_activity_sparkline"` // Show activity sparkline in status bar
	ShowStatusIndicator   bool `toml:"show_status_indicator"`   // Show CASS health indicator
}

// DefaultCASSConfig returns the default CASS configuration
func DefaultCASSConfig() CASSConfig {
	return CASSConfig{
		Enabled:          true,
		ShowInstallHints: true,
		BinaryPath:       "", // Auto-detect from PATH
		Timeout:          30,

		Context: CASSContextConfig{
			Enabled:            true,
			MaxSessions:        3,
			LookbackDays:       30,
			MaxTokens:          2000,
			MinRelevance:       0.5, // Only include results with >= 50% relevance
			SkipIfContextAbove: 80,  // Skip injection if context usage > 80%
			PreferSameProject:  true,
		},
		Duplicates: CASSDuplicateConfig{
			Enabled:             true,
			SimilarityThreshold: 0.7,
			LookbackDays:        7,
			PromptOnMatch:       true,
		},
		Search: CASSSearchConfig{
			DefaultLimit:  10,
			DefaultFields: "summary",
			IncludeMeta:   true,
		},
		TUI: CASSTUIConfig{
			ShowActivitySparkline: true,
			ShowStatusIndicator:   true,
		},
	}
}

// AgentConfig defines the commands for each agent type
type AgentConfig struct {
	Claude       string            `toml:"claude"`
	Codex        string            `toml:"codex"`
	Gemini       string            `toml:"gemini"`
	Cursor       string            `toml:"cursor"`
	Windsurf     string            `toml:"windsurf"`
	Aider        string            `toml:"aider"`
	Plugins      map[string]string `toml:"plugins"` // Custom agent commands keyed by type
	DefaultCount int               `toml:"default_count"`
}

// ContextRotationConfig holds configuration for automatic context window rotation
type ContextRotationConfig struct {
	Enabled              bool    `toml:"enabled"`                // Master toggle for context rotation
	WarningThreshold     float64 `toml:"warning_threshold"`      // 0.0-1.0, warn when context usage exceeds this
	RotateThreshold      float64 `toml:"rotate_threshold"`       // 0.0-1.0, rotate agent when usage exceeds this
	SummaryMaxTokens     int     `toml:"summary_max_tokens"`     // Max tokens for handoff summary
	MinSessionAgeSec     int     `toml:"min_session_age_sec"`    // Don't rotate agents younger than this
	TryCompactFirst      bool    `toml:"try_compact_first"`      // Try to compact before rotating
	RequireConfirm       bool    `toml:"require_confirm"`        // Require user confirmation before rotating
	ConfirmTimeoutSec    int     `toml:"confirm_timeout_sec"`    // Seconds to wait for confirmation (0 = no auto-rotate)
	DefaultConfirmAction string  `toml:"default_confirm_action"` // Action if timeout expires: "rotate", "ignore", "compact"
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
	Enabled             bool `toml:"enabled"`               // Master toggle for recovery context injection
	IncludeAgentMail    bool `toml:"include_agent_mail"`    // Include recent Agent Mail messages
	IncludeCMMemories   bool `toml:"include_cm_memories"`   // Include CM procedural memories
	IncludeBeadsContext bool `toml:"include_beads_context"` // Include BV task status
	MaxRecoveryTokens   int  `toml:"max_recovery_tokens"`   // Cap recovery context size
	AutoInjectOnSpawn   bool `toml:"auto_inject_on_spawn"`  // Send automatically on spawn
	StaleThresholdHours int  `toml:"stale_threshold_hours"` // Ignore context older than this
	MaxCMRules          int  `toml:"max_cm_rules"`          // Max CM rules to include (default: 10)
	MaxCMSnippets       int  `toml:"max_cm_snippets"`       // Max CM history snippets (default: 3)
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
		StaleThresholdHours: 24,   // Consider context up to 24 hours old
		MaxCMRules:          10,   // Max CM rules to include
		MaxCMSnippets:       3,    // Max CM history snippets
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
	Enabled               bool `toml:"enabled"`                   // Master toggle for auto file reservation
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
	Enabled             bool `toml:"enabled"`               // Master toggle for memory integration
	IncludeInRecovery   bool `toml:"include_in_recovery"`   // Include memory context in session recovery
	MaxRules            int  `toml:"max_rules"`             // Maximum number of rules to inject
	IncludeAntiPatterns bool `toml:"include_anti_patterns"` // Include anti-patterns in context
	IncludeHistory      bool `toml:"include_history"`       // Include historical snippets
	QueryTimeoutSeconds int  `toml:"query_timeout_seconds"` // Timeout for cm command
}

// DefaultMemoryConfig returns sensible defaults for memory integration.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Enabled:             true, // Enabled by default (when cm is available)
		IncludeInRecovery:   true, // Include in session recovery context
		MaxRules:            10,   // Cap number of rules to inject
		IncludeAntiPatterns: true, // Include anti-patterns by default
		IncludeHistory:      true, // Include historical snippets
		QueryTimeoutSeconds: 5,    // 5 second timeout for cm queries
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
	DefaultPanes    int    `toml:"default_panes"`
	PaletteKey      string `toml:"palette_key"`
	PaneInitDelayMs int    `toml:"pane_init_delay_ms"` // Delay before sending keys to new panes
	// ActivityIndicators control pane border activity coloring.
	ActivityIndicators ActivityIndicatorConfig `toml:"activity_indicators"`
}

// ActivityIndicatorConfig controls tmux pane border color thresholds.
type ActivityIndicatorConfig struct {
	Enabled        bool `toml:"enabled"`         // Master toggle for activity indicators
	ActiveSeconds  int  `toml:"active_seconds"`  // Seconds since activity to be considered active
	StalledSeconds int  `toml:"stalled_seconds"` // Seconds since activity to be considered stalled
}

// DefaultActivityIndicatorConfig returns sensible defaults for pane activity indicators.
func DefaultActivityIndicatorConfig() ActivityIndicatorConfig {
	return ActivityIndicatorConfig{
		Enabled:        true,
		ActiveSeconds:  30,
		StalledSeconds: 120,
	}
}

// ValidateActivityIndicatorConfig validates activity indicator thresholds.
func ValidateActivityIndicatorConfig(cfg *ActivityIndicatorConfig) error {
	if cfg.ActiveSeconds < 1 {
		return fmt.Errorf("active_seconds must be at least 1, got %d", cfg.ActiveSeconds)
	}
	if cfg.StalledSeconds <= cfg.ActiveSeconds {
		return fmt.Errorf("stalled_seconds (%d) must be greater than active_seconds (%d)", cfg.StalledSeconds, cfg.ActiveSeconds)
	}
	return nil
}

// AgentMailConfig holds Agent Mail server settings
type AgentMailConfig struct {
	Enabled      bool   `toml:"enabled"`       // Master toggle
	URL          string `toml:"url"`           // Server endpoint
	Token        string `toml:"token"`         // Bearer token
	AutoRegister bool   `toml:"auto_register"` // Auto-register sessions as agents
	ProgramName  string `toml:"program_name"`  // Program identifier for registration
}

// IntegrationsConfig holds external tool integration settings.
type IntegrationsConfig struct {
	DCG           DCGConfig           `toml:"dcg"`
	CAAM          CAAMConfig          `toml:"caam"`           // CAAM (Coding Agent Account Manager) integration
	RCH           RCHConfig           `toml:"rch"`            // RCH (Remote Compilation Helper) integration
	Caut          CautConfig          `toml:"caut"`           // caut (Cloud API Usage Tracker) integration
	ProcessTriage ProcessTriageConfig `toml:"process_triage"` // pt (process_triage) Bayesian health classification
	Rano          RanoConfig          `toml:"rano"`           // rano network observer for per-agent API tracking
}

// DCGConfig holds configuration for the DCG (destructive_commit_guard) integration.
type DCGConfig struct {
	Enabled         bool     `toml:"enabled"`
	BinaryPath      string   `toml:"binary_path"`
	CustomBlocklist []string `toml:"custom_blocklist"`
	CustomWhitelist []string `toml:"custom_whitelist"`
	AuditLog        string   `toml:"audit_log"`
	AllowOverride   bool     `toml:"allow_override"`
}

// AssignConfig holds configuration for the ntm assign command
type AssignConfig struct {
	Strategy string `toml:"strategy"` // Default strategy: balanced, speed, quality, dependency, round-robin
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
		Caut:          DefaultCautConfig(),
		ProcessTriage: DefaultProcessTriageConfig(),
		Rano:          DefaultRanoConfig(),
	}
}

// CAAMConfig holds configuration for CAAM (Coding Agent Account Manager) integration.
// CAAM provides automatic account rotation when rate limits are hit.
type CAAMConfig struct {
	Enabled           bool     `toml:"enabled"`             // Enable CAAM account management
	BinaryPath        string   `toml:"binary_path"`         // Path to caam binary (optional, defaults to PATH lookup)
	AutoRotate        bool     `toml:"auto_rotate"`         // Enable automatic account rotation on rate limit
	Providers         []string `toml:"providers"`           // Providers to manage (empty = all available)
	RateLimitPatterns []string `toml:"rate_limit_patterns"` // Custom rate limit detection patterns
	AccountCooldown   int      `toml:"account_cooldown"`    // Cooldown before retrying same account (seconds)
	AlertThreshold    int      `toml:"alert_threshold"`     // Alert threshold (percentage of limit)
}

// DefaultCAAMConfig returns sensible defaults for CAAM integration.
func DefaultCAAMConfig() CAAMConfig {
	return CAAMConfig{
		Enabled:           true,                                   // Enabled by default (when caam is available)
		BinaryPath:        "",                                     // Default to PATH lookup
		AutoRotate:        true,                                   // Auto-rotate on rate limit by default
		Providers:         []string{"claude", "openai", "gemini"}, // Manage all major providers
		RateLimitPatterns: nil,                                    // Use built-in patterns
		AccountCooldown:   300,                                    // 5 minute cooldown
		AlertThreshold:    80,                                     // Alert at 80% of limit
	}
}

// RCHConfig holds configuration for RCH (Remote Compilation Helper) integration.
// RCH provides build offloading to remote workers for faster compilation.
type RCHConfig struct {
	Enabled           bool     `toml:"enabled"`            // Enable RCH build offloading
	BinaryPath        string   `toml:"binary_path"`        // Path to rch binary (optional, defaults to PATH lookup)
	MinBuildTime      int      `toml:"min_build_time"`     // Minimum build time (seconds) to consider remote; builds faster than this run locally
	InterceptPatterns []string `toml:"intercept_patterns"` // Commands to intercept (regex patterns)
	FallbackLocal     bool     `toml:"fallback_local"`     // Fallback to local build on RCH failure
	ShowLocation      bool     `toml:"show_location"`      // Show build location in output
	PreferredWorker   string   `toml:"preferred_worker"`   // Worker preference (by name or "auto")
}

// DefaultRCHConfig returns sensible defaults for RCH integration.
func DefaultRCHConfig() RCHConfig {
	return RCHConfig{
		Enabled:      true, // Enabled by default (when rch is available)
		BinaryPath:   "",   // Default to PATH lookup
		MinBuildTime: 10,   // Only offload builds expected to take 10+ seconds
		InterceptPatterns: []string{
			"^cargo (build|test|check)",
			"^go (build|test)",
			"^npm run build",
			"^make",
		},
		FallbackLocal:   true,   // Fallback to local if remote fails
		ShowLocation:    true,   // Show where build ran
		PreferredWorker: "auto", // Auto-select best worker
	}
}

// CautConfig holds configuration for caut (Cloud API Usage Tracker) integration.
// caut tracks API usage, quotas, and spending across cloud providers.
type CautConfig struct {
	Enabled          bool     `toml:"enabled"`            // Enable caut usage tracking integration
	BinaryPath       string   `toml:"binary_path"`        // Path to caut binary (optional, defaults to PATH lookup)
	PollInterval     int      `toml:"poll_interval"`      // Polling interval in seconds
	AlertThreshold   int      `toml:"alert_threshold"`    // Alert threshold (percentage of quota)
	Providers        []string `toml:"providers"`          // Providers to track (empty = all available)
	PerAgentTracking bool     `toml:"per_agent_tracking"` // Enable per-agent usage attribution
	Currency         string   `toml:"currency"`           // Cost display currency
}

// DefaultCautConfig returns sensible defaults for caut integration.
func DefaultCautConfig() CautConfig {
	return CautConfig{
		Enabled:          true,  // Enabled by default (when caut is available)
		BinaryPath:       "",    // Default to PATH lookup
		PollInterval:     60,    // Poll every 60 seconds
		AlertThreshold:   80,    // Alert at 80% quota usage
		Providers:        nil,   // Track all available providers
		PerAgentTracking: true,  // Enable per-agent tracking if supported
		Currency:         "USD", // Default to USD
	}
}

// ProcessTriageConfig holds configuration for process_triage (pt) integration.
// pt uses Bayesian classification to identify useful, abandoned, and zombie processes.
type ProcessTriageConfig struct {
	Enabled        bool   `toml:"enabled"`         // Enable process triage integration
	BinaryPath     string `toml:"binary_path"`     // Path to pt binary (optional, defaults to PATH lookup)
	CheckInterval  int    `toml:"check_interval"`  // How often to check processes (seconds)
	IdleThreshold  int    `toml:"idle_threshold"`  // Seconds of idle before considering abandoned
	StuckThreshold int    `toml:"stuck_threshold"` // Seconds stuck before considering zombie
	OnStuck        string `toml:"on_stuck"`        // Action when stuck: "alert", "kill", "ignore"
	UseRanoData    bool   `toml:"use_rano_data"`   // Use rano network data to improve classification
}

// DefaultProcessTriageConfig returns sensible defaults for process_triage integration.
func DefaultProcessTriageConfig() ProcessTriageConfig {
	return ProcessTriageConfig{
		Enabled:        true,    // Enabled by default (when pt is available)
		BinaryPath:     "",      // Default to PATH lookup
		CheckInterval:  30,      // Check every 30 seconds
		IdleThreshold:  300,     // 5 minutes idle = abandoned candidate
		StuckThreshold: 600,     // 10 minutes stuck = zombie candidate
		OnStuck:        "alert", // Alert by default, don't auto-kill
		UseRanoData:    true,    // Use rano data when available
	}
}

// ValidateProcessTriageConfig validates the process_triage configuration.
func ValidateProcessTriageConfig(cfg *ProcessTriageConfig) error {
	if cfg == nil {
		return nil
	}

	// Skip validation for unconfigured/zero-valued configs (use defaults)
	if !cfg.Enabled && cfg.CheckInterval == 0 && cfg.IdleThreshold == 0 && cfg.StuckThreshold == 0 && cfg.OnStuck == "" {
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

	if cfg.CheckInterval < 5 {
		return fmt.Errorf("check_interval must be at least 5 seconds, got %d", cfg.CheckInterval)
	}

	if cfg.IdleThreshold < 30 {
		return fmt.Errorf("idle_threshold must be at least 30 seconds, got %d", cfg.IdleThreshold)
	}

	if cfg.StuckThreshold < cfg.IdleThreshold {
		return fmt.Errorf("stuck_threshold (%d) must be >= idle_threshold (%d)", cfg.StuckThreshold, cfg.IdleThreshold)
	}

	validActions := map[string]bool{"alert": true, "kill": true, "ignore": true}
	if !validActions[cfg.OnStuck] {
		return fmt.Errorf("on_stuck must be 'alert', 'kill', or 'ignore', got %q", cfg.OnStuck)
	}

	return nil
}

// RanoConfig holds configuration for the rano network observer integration.
// rano monitors network activity per process, enabling per-agent API tracking.
type RanoConfig struct {
	Enabled        bool     `toml:"enabled"`          // Enable rano network monitoring integration
	BinaryPath     string   `toml:"binary_path"`      // Path to rano binary (optional, defaults to PATH lookup)
	PollIntervalMs int      `toml:"poll_interval_ms"` // Polling interval in milliseconds
	Providers      []string `toml:"providers"`        // Track these providers (empty = all known: anthropic, openai, google)
	PersistHistory bool     `toml:"persist_history"`  // Persist historical network data
	HistoryDays    int      `toml:"history_days"`     // Days to retain historical data
}

// DefaultRanoConfig returns sensible defaults for rano integration.
func DefaultRanoConfig() RanoConfig {
	return RanoConfig{
		Enabled:        true,                                      // Enabled by default (when rano is available)
		BinaryPath:     "",                                        // Default to PATH lookup
		PollIntervalMs: 1000,                                      // Poll every second
		Providers:      []string{"anthropic", "openai", "google"}, // Track major AI providers
		PersistHistory: true,                                      // Keep historical data
		HistoryDays:    7,                                         // Retain for a week
	}
}

// ValidateRanoConfig validates the rano configuration.
func ValidateRanoConfig(cfg *RanoConfig) error {
	if cfg == nil {
		return nil
	}

	// Skip validation for unconfigured/zero-valued configs (use defaults)
	if !cfg.Enabled && cfg.PollIntervalMs == 0 && len(cfg.Providers) == 0 {
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

	if cfg.PollIntervalMs < 100 {
		return fmt.Errorf("poll_interval_ms must be at least 100ms, got %d", cfg.PollIntervalMs)
	}

	if cfg.HistoryDays < 0 {
		return fmt.Errorf("history_days must be non-negative, got %d", cfg.HistoryDays)
	}

	return nil
}

// ModelsConfig holds model alias configuration for each agent type
type ModelsConfig struct {
	DefaultClaude string            `toml:"default_claude"` // Default model for Claude
	DefaultCodex  string            `toml:"default_codex"`  // Default model for Codex
	DefaultGemini string            `toml:"default_gemini"` // Default model for Gemini
	Claude        map[string]string `toml:"claude"`         // Claude model aliases
	Codex         map[string]string `toml:"codex"`          // Codex model aliases
	Gemini        map[string]string `toml:"gemini"`         // Gemini model aliases
}

// DefaultModels returns the default model configuration with sensible aliases
func DefaultModels() ModelsConfig {
	return ModelsConfig{
		DefaultClaude: "claude-opus-4-5-20251101",
		DefaultCodex:  "gpt-5.2-codex",
		DefaultGemini: "gemini-3-pro-preview",
		Claude: map[string]string{
			"opus":      "claude-opus-4-5-20251101",
			"sonnet":    "claude-sonnet-4-20250514",
			"haiku":     "claude-haiku-3-20240307",
			"architect": "claude-opus-4-5-20251101",
			"fast":      "claude-sonnet-4-20250514",
		},
		Codex: map[string]string{
			"gpt4":  "gpt-4",
			"gpt5":  "gpt-5.2-codex",
			"o1":    "o1",
			"o3":    "o3",
			"turbo": "gpt-4-turbo",
			"codex": "gpt-5.2-codex",
		},
		Gemini: map[string]string{
			"pro":    "gemini-3-pro-preview",
			"flash":  "gemini-3-flash",
			"flash2": "gemini-2.0-flash",
		},
	}
}

// GetModelName resolves a model alias to its full model name.
// Returns the alias itself if no mapping is found.
func (m *ModelsConfig) GetModelName(agentType, alias string) string {
	if alias == "" {
		// Return default if no alias specified
		switch strings.ToLower(agentType) {
		case "claude", "cc":
			return m.DefaultClaude
		case "codex", "cod":
			return m.DefaultCodex
		case "gemini", "gmi":
			return m.DefaultGemini
		}
		return ""
	}

	// Check agent-specific aliases
	var aliases map[string]string
	switch strings.ToLower(agentType) {
	case "claude", "cc":
		aliases = m.Claude
	case "codex", "cod":
		aliases = m.Codex
	case "gemini", "gmi":
		aliases = m.Gemini
	}

	if aliases != nil {
		if fullName, ok := aliases[strings.ToLower(alias)]; ok {
			return fullName
		}
	}

	// Return the alias as-is (assume it's a full model name)
	return alias
}

// IsPersonaName checks if the given name is a known persona.
// Currently returns false as personas are not yet fully implemented.
// TODO: Implement persona configuration and checking
func (c *Config) IsPersonaName(name string) bool {
	// Personas are not yet implemented - return false for now
	// When personas are implemented, this will check against:
	// 1. Project personas (.ntm/personas.toml)
	// 2. User personas (~/.config/ntm/personas.toml)
	// 3. Built-in personas
	return false
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

// DefaultProjectsBase returns the default projects directory
func DefaultProjectsBase() string {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			// Fallback to /tmp when home directory is unavailable
			return filepath.Join(os.TempDir(), "Developer")
		}
		return filepath.Join(home, "Developer")
	}
	// Linux/other: use /tmp to avoid polluting project directories
	return os.TempDir()
}

// findPaletteMarkdown searches for a command_palette.md file in standard locations
// Search order: ~/.config/ntm/command_palette.md, then ./command_palette.md
func findPaletteMarkdown() string {
	// Check ~/.config/ntm/command_palette.md (user customization)
	configDir := filepath.Dir(DefaultPath())
	mdPath := filepath.Join(configDir, "command_palette.md")
	if _, err := os.Stat(mdPath); err == nil {
		return mdPath
	}

	// Check current working directory (project-specific)
	if cwd, err := os.Getwd(); err == nil {
		cwdPath := filepath.Join(cwd, "command_palette.md")
		if _, err := os.Stat(cwdPath); err == nil {
			return cwdPath
		}
	}

	return ""
}

// DetectPalettePath returns the palette markdown path to use, if any.
// Precedence: explicit cfg.PaletteFile, then auto-discovered markdown.
func DetectPalettePath(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.PaletteFile != "" {
		return cfg.PaletteFile
	}
	return findPaletteMarkdown()
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

		// Comment: starts with # but not ## or ###
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "##") {
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
		ProjectsBase:       projectsBase,
		SuggestionsEnabled: true,
		Agents:             DefaultAgentTemplates(),
		Tmux: TmuxConfig{
			DefaultPanes:       10,
			PaletteKey:         "F6",
			PaneInitDelayMs:    1000,
			ActivityIndicators: DefaultActivityIndicatorConfig(),
		},
		Robot: DefaultRobotConfig(),
		AgentMail: AgentMailConfig{
			Enabled:      true,
			URL:          DefaultAgentMailURL,
			Token:        "",
			AutoRegister: true,
			ProgramName:  "ntm",
		},
		Integrations:    DefaultIntegrationsConfig(),
		Models:          DefaultModels(),
		Alerts:          DefaultAlertsConfig(),
		Checkpoints:     DefaultCheckpointsConfig(),
		Notifications:   notify.DefaultConfig(),
		Resilience:      DefaultResilienceConfig(),
		Health:          DefaultHealthConfig(),
		Scanner:         DefaultScannerConfig(),
		CASS:            DefaultCASSConfig(),
		Accounts:        DefaultAccountsConfig(),
		Rotation:        DefaultRotationConfig(),
		GeminiSetup:     DefaultGeminiSetupConfig(),
		ContextRotation: DefaultContextRotationConfig(),
		SessionRecovery: DefaultSessionRecoveryConfig(),
		Cleanup:         DefaultCleanupConfig(),
		FileReservation: DefaultFileReservationConfig(),
		Memory:          DefaultMemoryConfig(),
		Assign:          DefaultAssignConfig(),
		Ensemble:        DefaultEnsembleConfig(),
		Swarm:           DefaultSwarmConfig(),
		Redaction:       DefaultRedactionConfig(),
	}

	// Try to load palette from markdown file
	if mdPath := findPaletteMarkdown(); mdPath != "" {
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
	if path == "" {
		path = DefaultPath()
	}

	// 1. Initialize with defaults
	cfg := Default()

	// 2. Read and unmarshal TOML over defaults
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
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

	// Accounts/Rotation Env Overrides
	if autoRotate := os.Getenv("NTM_ACCOUNTS_AUTO_ROTATE"); autoRotate != "" {
		cfg.Accounts.AutoRotate = autoRotate == "1" || autoRotate == "true"
	}
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
	if staleHours := os.Getenv("NTM_RECOVERY_STALE_HOURS"); staleHours != "" {
		if n, err := strconv.Atoi(staleHours); err == nil && n > 0 {
			cfg.SessionRecovery.StaleThresholdHours = n
		}
	}

	// 4. Palette Precedence: Markdown > TOML > Default
	// Default() already loaded Markdown if available.
	// Unmarshal() might have overwritten cfg.Palette with TOML entries.
	// We need to re-check Markdown to enforce Markdown > TOML.

	mdPath := cfg.PaletteFile
	if mdPath == "" {
		mdPath = findPaletteMarkdown()
	} else {
		mdPath = ExpandHome(mdPath)
	}

	if mdPath != "" {
		if mdCmds, err := LoadPaletteFromMarkdown(mdPath); err == nil && len(mdCmds) > 0 {
			cfg.Palette = mdCmds
		}
	}

	return cfg, nil
}

// CreateDefault creates a default config file
func CreateDefault() (string, error) {
	path := DefaultPath()

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
	if cfg.Agents.Cursor != "" {
		fmt.Fprintf(w, "cursor = %q\n", cfg.Agents.Cursor)
	}
	if cfg.Agents.Windsurf != "" {
		fmt.Fprintf(w, "windsurf = %q\n", cfg.Agents.Windsurf)
	}
	if cfg.Agents.Aider != "" {
		fmt.Fprintf(w, "aider = %q\n", cfg.Agents.Aider)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[tmux]")
	fmt.Fprintln(w, "# Tmux-specific settings")
	fmt.Fprintf(w, "default_panes = %d\n", cfg.Tmux.DefaultPanes)
	fmt.Fprintf(w, "palette_key = %q\n", cfg.Tmux.PaletteKey)
	fmt.Fprintf(w, "pane_init_delay_ms = %d  # Delay before send-keys to new panes\n", cfg.Tmux.PaneInitDelayMs)
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
	fmt.Fprintf(w, "pretty = %t\n", cfg.Robot.Output.Pretty)
	fmt.Fprintf(w, "timestamps = %t\n", cfg.Robot.Output.Timestamps)
	fmt.Fprintf(w, "compress = %t\n", cfg.Robot.Output.Compress)
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
	fmt.Fprintf(w, "program_name = %q\n", cfg.AgentMail.ProgramName)
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
	if cfg.Integrations.DCG.AuditLog != "" {
		fmt.Fprintf(w, "audit_log = %q\n", cfg.Integrations.DCG.AuditLog)
	} else {
		fmt.Fprintln(w, "# audit_log = \"~/.ntm/dcg_audit.log\"")
	}
	fmt.Fprintf(w, "allow_override = %t\n", cfg.Integrations.DCG.AllowOverride)
	fmt.Fprintln(w)

	// Write models configuration
	fmt.Fprintln(w, "[models]")
	fmt.Fprintln(w, "# Default models when no specifier given")
	fmt.Fprintf(w, "default_claude = %q\n", cfg.Models.DefaultClaude)
	fmt.Fprintf(w, "default_codex = %q\n", cfg.Models.DefaultCodex)
	fmt.Fprintf(w, "default_gemini = %q\n", cfg.Models.DefaultGemini)
	fmt.Fprintln(w)

	// Write Claude model aliases
	fmt.Fprintln(w, "[models.claude]")
	fmt.Fprintln(w, "# Claude model aliases (e.g., --cc=2:opus)")
	for alias, fullName := range cfg.Models.Claude {
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write Codex model aliases
	fmt.Fprintln(w, "[models.codex]")
	fmt.Fprintln(w, "# Codex model aliases (e.g., --cod=2:max)")
	for alias, fullName := range cfg.Models.Codex {
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write Gemini model aliases
	fmt.Fprintln(w, "[models.gemini]")
	fmt.Fprintln(w, "# Gemini model aliases (e.g., --gmi=1:flash)")
	for alias, fullName := range cfg.Models.Gemini {
		fmt.Fprintf(w, "%s = %q\n", alias, fullName)
	}
	fmt.Fprintln(w)

	// Write alerts configuration
	fmt.Fprintln(w, "[alerts]")
	fmt.Fprintln(w, "# Alert system configuration for proactive problem detection")
	fmt.Fprintf(w, "enabled = %t\n", cfg.Alerts.Enabled)
	fmt.Fprintf(w, "agent_stuck_minutes = %d    # Minutes without output before alerting\n", cfg.Alerts.AgentStuckMinutes)
	fmt.Fprintf(w, "disk_low_threshold_gb = %.1f  # Minimum free disk space (GB)\n", cfg.Alerts.DiskLowThresholdGB)
	fmt.Fprintf(w, "mail_backlog_threshold = %d  # Unread messages before alerting\n", cfg.Alerts.MailBacklogThreshold)
	fmt.Fprintf(w, "bead_stale_hours = %d       # Hours before in-progress bead is stale\n", cfg.Alerts.BeadStaleHours)
	fmt.Fprintf(w, "resolved_prune_minutes = %d # How long to keep resolved alerts\n", cfg.Alerts.ResolvedPruneMinutes)
	fmt.Fprintln(w)

	// Write checkpoints configuration
	fmt.Fprintln(w, "[checkpoints]")
	fmt.Fprintln(w, "# Automatic checkpoint configuration for risky operations")
	fmt.Fprintf(w, "enabled = %t                    # Master toggle for auto-checkpoints\n", cfg.Checkpoints.Enabled)
	fmt.Fprintf(w, "before_broadcast = %t           # Auto-checkpoint before sending to all agents\n", cfg.Checkpoints.BeforeBroadcast)
	fmt.Fprintf(w, "before_add_agents = %d            # Auto-checkpoint when adding >= N agents (0 = disabled)\n", cfg.Checkpoints.BeforeAddAgents)
	fmt.Fprintf(w, "max_auto_checkpoints = %d        # Max auto-checkpoints per session (rotation)\n", cfg.Checkpoints.MaxAutoCheckpoints)
	fmt.Fprintf(w, "scrollback_lines = %d           # Lines of scrollback to capture\n", cfg.Checkpoints.ScrollbackLines)
	fmt.Fprintf(w, "include_git = %t               # Capture git state in auto-checkpoints\n", cfg.Checkpoints.IncludeGit)
	fmt.Fprintf(w, "auto_checkpoint_on_spawn = %t   # Auto-checkpoint when spawning session\n", cfg.Checkpoints.AutoCheckpointOnSpawn)
	fmt.Fprintf(w, "interval_minutes = %d           # Periodic checkpoint interval (0 = disabled)\n", cfg.Checkpoints.IntervalMinutes)
	fmt.Fprintf(w, "on_rotation = %t               # Checkpoint before context rotation\n", cfg.Checkpoints.OnRotation)
	fmt.Fprintf(w, "on_error = %t                  # Checkpoint when agent error detected\n", cfg.Checkpoints.OnError)
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

	// Write resilience configuration
	fmt.Fprintln(w, "[resilience]")
	fmt.Fprintln(w, "# Agent auto-restart and recovery configuration")
	fmt.Fprintf(w, "auto_restart = %t           # Enable automatic agent restart on crash\n", cfg.Resilience.AutoRestart)
	fmt.Fprintf(w, "max_restarts = %d            # Max restarts per agent before giving up\n", cfg.Resilience.MaxRestarts)
	fmt.Fprintf(w, "restart_delay_seconds = %d  # Seconds to wait before restarting\n", cfg.Resilience.RestartDelaySeconds)
	fmt.Fprintf(w, "health_check_seconds = %d   # Seconds between health checks\n", cfg.Resilience.HealthCheckSeconds)
	fmt.Fprintf(w, "notify_on_crash = %t       # Send notification when agent crashes\n", cfg.Resilience.NotifyOnCrash)
	fmt.Fprintf(w, "notify_on_max_restarts = %t # Notify when max restarts exceeded\n", cfg.Resilience.NotifyOnMaxRestarts)
	fmt.Fprintln(w)

	// Write rate limit sub-configuration
	fmt.Fprintln(w, "[resilience.rate_limit]")
	fmt.Fprintln(w, "# Rate limit detection configuration")
	fmt.Fprintf(w, "detect = %t   # Enable rate limit detection\n", cfg.Resilience.RateLimit.Detect)
	fmt.Fprintf(w, "notify = %t   # Send notification on rate limit\n", cfg.Resilience.RateLimit.Notify)
	if len(cfg.Resilience.RateLimit.Patterns) > 0 {
		patternItems := make([]string, 0, len(cfg.Resilience.RateLimit.Patterns))
		for _, p := range cfg.Resilience.RateLimit.Patterns {
			patternItems = append(patternItems, fmt.Sprintf("%q", p))
		}
		fmt.Fprintf(w, "patterns = [%s]  # Custom patterns (in addition to defaults)\n", strings.Join(patternItems, ", "))
	} else {
		fmt.Fprintln(w, "# patterns = [\"custom pattern\"]  # Custom patterns (in addition to defaults)")
	}
	fmt.Fprintln(w)

	// Write accounts configuration
	fmt.Fprintln(w, "[accounts]")
	fmt.Fprintln(w, "# Multi-account management for quota rotation")
	fmt.Fprintf(w, "state_file = %q            # Path to account state JSON\n", cfg.Accounts.StateFile)
	fmt.Fprintf(w, "auto_rotate = %t            # Auto-rotate when limit detected\n", cfg.Accounts.AutoRotate)
	fmt.Fprintf(w, "reset_buffer_minutes = %d   # Minutes before reset to consider available\n", cfg.Accounts.ResetBufferMinutes)
	fmt.Fprintln(w)

	// Write Claude accounts if any
	if len(cfg.Accounts.Claude) > 0 {
		for _, acct := range cfg.Accounts.Claude {
			fmt.Fprintln(w, "[[accounts.claude]]")
			fmt.Fprintf(w, "email = %q\n", acct.Email)
			fmt.Fprintf(w, "alias = %q\n", acct.Alias)
			fmt.Fprintf(w, "priority = %d\n", acct.Priority)
			fmt.Fprintln(w)
		}
	} else {
		fmt.Fprintln(w, "# [[accounts.claude]]")
		fmt.Fprintln(w, "# email = \"primary@gmail.com\"")
		fmt.Fprintln(w, "# alias = \"main\"")
		fmt.Fprintln(w, "# priority = 1")
		fmt.Fprintln(w)
	}

	// Write rotation configuration
	fmt.Fprintln(w, "[rotation]")
	fmt.Fprintln(w, "# Account rotation and restart configuration")
	fmt.Fprintf(w, "enabled = %t               # Master toggle\n", cfg.Rotation.Enabled)
	fmt.Fprintf(w, "prefer_restart = %t        # Prefer restart over account switch\n", cfg.Rotation.PreferRestart)
	fmt.Fprintf(w, "auto_open_browser = %t     # Auto-open browser for auth\n", cfg.Rotation.AutoOpenBrowser)
	fmt.Fprintf(w, "continuation_prompt = %q\n", cfg.Rotation.ContinuationPrompt)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[rotation.thresholds]")
	fmt.Fprintf(w, "warning_percent = %d        # Show warning at this quota %%\n", cfg.Rotation.Thresholds.WarningPercent)
	fmt.Fprintf(w, "critical_percent = %d       # Consider limited at this %%\n", cfg.Rotation.Thresholds.CriticalPercent)
	fmt.Fprintf(w, "restart_if_tokens_above = %.0f  # Restart if tokens exceed this\n", cfg.Rotation.Thresholds.RestartIfTokensAbove)
	fmt.Fprintf(w, "restart_if_session_hours = %d   # Restart after N hours\n", cfg.Rotation.Thresholds.RestartIfSessionHours)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[rotation.dashboard]")
	fmt.Fprintf(w, "show_quota_bars = %t       # Show quota bars in dashboard\n", cfg.Rotation.Dashboard.ShowQuotaBars)
	fmt.Fprintf(w, "show_account_status = %t   # Show account status\n", cfg.Rotation.Dashboard.ShowAccountStatus)
	fmt.Fprintf(w, "show_reset_timers = %t     # Show reset countdown\n", cfg.Rotation.Dashboard.ShowResetTimers)
	fmt.Fprintln(w)

	// Write health monitoring configuration
	fmt.Fprintln(w, "[health]")
	fmt.Fprintln(w, "# Agent health monitoring configuration")
	fmt.Fprintln(w, "# Proactive monitoring to detect stalled, unresponsive, or unhealthy agents")
	fmt.Fprintf(w, "enabled = %t                # Master toggle for health monitoring\n", cfg.Health.Enabled)
	fmt.Fprintf(w, "check_interval = %d          # Seconds between health checks\n", cfg.Health.CheckInterval)
	fmt.Fprintf(w, "stall_threshold = %d        # Seconds without output before agent is stalled\n", cfg.Health.StallThreshold)
	fmt.Fprintf(w, "auto_restart = %t           # Auto-restart on unhealthy state\n", cfg.Health.AutoRestart)
	fmt.Fprintf(w, "max_restarts = %d            # Max restart attempts before giving up\n", cfg.Health.MaxRestarts)
	fmt.Fprintf(w, "restart_backoff_base = %d   # Initial restart delay (seconds)\n", cfg.Health.RestartBackoffBase)
	fmt.Fprintf(w, "restart_backoff_max = %d    # Maximum restart delay (seconds)\n", cfg.Health.RestartBackoffMax)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[scanner]")
	fmt.Fprintln(w, "# UBS scanner configuration")
	if cfg.Scanner.UBSPath != "" {
		fmt.Fprintf(w, "ubs_path = %q\n", cfg.Scanner.UBSPath)
	} else {
		fmt.Fprintln(w, "# ubs_path = \"\"  # Auto-detect from PATH")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[scanner.defaults]")
	fmt.Fprintf(w, "timeout = %q\n", cfg.Scanner.Defaults.Timeout)
	fmt.Fprintf(w, "parallel = %t\n", cfg.Scanner.Defaults.Parallel)
	fmt.Fprintf(w, "exclude = %s\n", renderTOMLStringArray(cfg.Scanner.Defaults.Exclude))
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

	// Write context rotation configuration
	fmt.Fprintln(w, "[context_rotation]")
	fmt.Fprintln(w, "# Context window rotation configuration")
	fmt.Fprintln(w, "# Monitors agent context usage and rotates before exhaustion")
	fmt.Fprintf(w, "enabled = %t                    # Master toggle for context rotation\n", cfg.ContextRotation.Enabled)
	fmt.Fprintf(w, "warning_threshold = %.2f        # Warn when context usage exceeds this (0.0-1.0)\n", cfg.ContextRotation.WarningThreshold)
	fmt.Fprintf(w, "rotate_threshold = %.2f         # Rotate agent when usage exceeds this (0.0-1.0)\n", cfg.ContextRotation.RotateThreshold)
	fmt.Fprintf(w, "summary_max_tokens = %d        # Max tokens for handoff summary\n", cfg.ContextRotation.SummaryMaxTokens)
	fmt.Fprintf(w, "min_session_age_sec = %d        # Don't rotate agents younger than this\n", cfg.ContextRotation.MinSessionAgeSec)
	fmt.Fprintf(w, "try_compact_first = %t         # Try to compact before rotating\n", cfg.ContextRotation.TryCompactFirst)
	fmt.Fprintf(w, "require_confirm = %t           # Require user confirmation before rotating\n", cfg.ContextRotation.RequireConfirm)
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

// GetProjectDir returns the project directory for a session
func (c *Config) GetProjectDir(session string) string {
	base := ExpandHome(c.ProjectsBase)
	return filepath.Join(base, session)
}

// SetProjectsBase sets the projects_base in the config file.
// If the config file doesn't exist, it creates one with defaults.
// The path can use ~ for home directory (which will be preserved in config).
func SetProjectsBase(path string) error {
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

	configPath := DefaultPath()

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
		}
	case "tmux":
		if len(parts) < 2 {
			return cfg.Tmux, nil
		}
		switch parts[1] {
		case "default_panes":
			return cfg.Tmux.DefaultPanes, nil
		case "palette_key":
			return cfg.Tmux.PaletteKey, nil
		case "pane_init_delay_ms":
			return cfg.Tmux.PaneInitDelayMs, nil
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
		case "max_auto_checkpoints":
			return cfg.Checkpoints.MaxAutoCheckpoints, nil
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
	case "cass":
		if len(parts) < 2 {
			return cfg.CASS, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.CASS.Enabled, nil
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
	case "health":
		if len(parts) < 2 {
			return cfg.Health, nil
		}
		switch parts[1] {
		case "enabled":
			return cfg.Health.Enabled, nil
		case "check_interval":
			return cfg.Health.CheckInterval, nil
		case "stall_threshold":
			return cfg.Health.StallThreshold, nil
		case "auto_restart":
			return cfg.Health.AutoRestart, nil
		case "max_restarts":
			return cfg.Health.MaxRestarts, nil
		case "restart_backoff_base":
			return cfg.Health.RestartBackoffBase, nil
		case "restart_backoff_max":
			return cfg.Health.RestartBackoffMax, nil
		}
	}

	return nil, fmt.Errorf("unknown config path: %s", path)
}

// Reset removes the config file and creates a new one with defaults
func Reset() error {
	path := DefaultPath()

	// Remove existing file
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing config file: %w", err)
	}

	// Create new default config
	_, err := CreateDefault()
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
		if fmt.Sprintf("%v", def) != fmt.Sprintf("%v", cur) {
			diffs = append(diffs, ConfigDiff{
				Key:     path, // Use path as key for uniqueness
				Path:    path,
				Default: def,
				Current: cur,
				Source:  "config", // Could be enhanced to track actual source
			})
		}
	}

	// Top-level settings
	addDiff("projects_base", defaults.ProjectsBase, cfg.ProjectsBase)
	addDiff("theme", defaults.Theme, cfg.Theme)

	// Agents
	addDiff("agents.claude", defaults.Agents.Claude, cfg.Agents.Claude)
	addDiff("agents.codex", defaults.Agents.Codex, cfg.Agents.Codex)
	addDiff("agents.gemini", defaults.Agents.Gemini, cfg.Agents.Gemini)
	addDiff("agents.plugins", defaults.Agents.Plugins, cfg.Agents.Plugins)

	// Tmux
	addDiff("tmux.default_panes", defaults.Tmux.DefaultPanes, cfg.Tmux.DefaultPanes)
	addDiff("tmux.palette_key", defaults.Tmux.PaletteKey, cfg.Tmux.PaletteKey)
	addDiff("tmux.pane_init_delay_ms", defaults.Tmux.PaneInitDelayMs, cfg.Tmux.PaneInitDelayMs)

	// Agent Mail
	addDiff("agent_mail.enabled", defaults.AgentMail.Enabled, cfg.AgentMail.Enabled)
	addDiff("agent_mail.url", defaults.AgentMail.URL, cfg.AgentMail.URL)
	addDiff("agent_mail.auto_register", defaults.AgentMail.AutoRegister, cfg.AgentMail.AutoRegister)

	// Integrations (DCG)
	addDiff("integrations.dcg.enabled", defaults.Integrations.DCG.Enabled, cfg.Integrations.DCG.Enabled)
	addDiff("integrations.dcg.binary_path", defaults.Integrations.DCG.BinaryPath, cfg.Integrations.DCG.BinaryPath)
	addDiff("integrations.dcg.custom_blocklist", defaults.Integrations.DCG.CustomBlocklist, cfg.Integrations.DCG.CustomBlocklist)
	addDiff("integrations.dcg.custom_whitelist", defaults.Integrations.DCG.CustomWhitelist, cfg.Integrations.DCG.CustomWhitelist)
	addDiff("integrations.dcg.audit_log", defaults.Integrations.DCG.AuditLog, cfg.Integrations.DCG.AuditLog)
	addDiff("integrations.dcg.allow_override", defaults.Integrations.DCG.AllowOverride, cfg.Integrations.DCG.AllowOverride)

	// Alerts
	addDiff("alerts.enabled", defaults.Alerts.Enabled, cfg.Alerts.Enabled)
	addDiff("alerts.agent_stuck_minutes", defaults.Alerts.AgentStuckMinutes, cfg.Alerts.AgentStuckMinutes)
	addDiff("alerts.disk_low_threshold_gb", defaults.Alerts.DiskLowThresholdGB, cfg.Alerts.DiskLowThresholdGB)

	// Checkpoints
	addDiff("checkpoints.enabled", defaults.Checkpoints.Enabled, cfg.Checkpoints.Enabled)
	addDiff("checkpoints.before_broadcast", defaults.Checkpoints.BeforeBroadcast, cfg.Checkpoints.BeforeBroadcast)
	addDiff("checkpoints.max_auto_checkpoints", defaults.Checkpoints.MaxAutoCheckpoints, cfg.Checkpoints.MaxAutoCheckpoints)

	// Resilience
	addDiff("resilience.auto_restart", defaults.Resilience.AutoRestart, cfg.Resilience.AutoRestart)
	addDiff("resilience.max_restarts", defaults.Resilience.MaxRestarts, cfg.Resilience.MaxRestarts)

	// Context Rotation
	addDiff("context_rotation.enabled", defaults.ContextRotation.Enabled, cfg.ContextRotation.Enabled)
	addDiff("context_rotation.warning_threshold", defaults.ContextRotation.WarningThreshold, cfg.ContextRotation.WarningThreshold)
	addDiff("context_rotation.rotate_threshold", defaults.ContextRotation.RotateThreshold, cfg.ContextRotation.RotateThreshold)

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
	addDiff("cass.timeout", defaults.CASS.Timeout, cfg.CASS.Timeout)

	// CASS Context
	addDiff("cass.context.enabled", defaults.CASS.Context.Enabled, cfg.CASS.Context.Enabled)
	addDiff("cass.context.max_sessions", defaults.CASS.Context.MaxSessions, cfg.CASS.Context.MaxSessions)
	addDiff("cass.context.lookback_days", defaults.CASS.Context.LookbackDays, cfg.CASS.Context.LookbackDays)
	addDiff("cass.context.max_tokens", defaults.CASS.Context.MaxTokens, cfg.CASS.Context.MaxTokens)
	addDiff("cass.context.min_relevance", defaults.CASS.Context.MinRelevance, cfg.CASS.Context.MinRelevance)
	addDiff("cass.context.skip_if_context_above", defaults.CASS.Context.SkipIfContextAbove, cfg.CASS.Context.SkipIfContextAbove)
	addDiff("cass.context.prefer_same_project", defaults.CASS.Context.PreferSameProject, cfg.CASS.Context.PreferSameProject)

	// Health monitoring
	addDiff("health.enabled", defaults.Health.Enabled, cfg.Health.Enabled)
	addDiff("health.check_interval", defaults.Health.CheckInterval, cfg.Health.CheckInterval)
	addDiff("health.stall_threshold", defaults.Health.StallThreshold, cfg.Health.StallThreshold)
	addDiff("health.auto_restart", defaults.Health.AutoRestart, cfg.Health.AutoRestart)
	addDiff("health.max_restarts", defaults.Health.MaxRestarts, cfg.Health.MaxRestarts)
	addDiff("health.restart_backoff_base", defaults.Health.RestartBackoffBase, cfg.Health.RestartBackoffBase)
	addDiff("health.restart_backoff_max", defaults.Health.RestartBackoffMax, cfg.Health.RestartBackoffMax)

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

	// Validate ensemble defaults
	if err := ValidateEnsembleConfig(&cfg.Ensemble); err != nil {
		errs = append(errs, fmt.Errorf("ensemble: %w", err))
	}

	// Validate health monitoring
	if err := ValidateHealthConfig(&cfg.Health); err != nil {
		errs = append(errs, fmt.Errorf("health: %w", err))
	}

	// Validate robot output config
	if err := ValidateRobotOutputConfig(&cfg.Robot.Output); err != nil {
		errs = append(errs, fmt.Errorf("robot.output: %w", err))
	}

	// Validate DCG integration config
	if err := ValidateDCGConfig(&cfg.Integrations.DCG); err != nil {
		errs = append(errs, fmt.Errorf("integrations.dcg: %w", err))
	}

	// Validate ProcessTriage integration config
	if err := ValidateProcessTriageConfig(&cfg.Integrations.ProcessTriage); err != nil {
		errs = append(errs, fmt.Errorf("integrations.process_triage: %w", err))
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

	// Validate checkpoints
	if cfg.Checkpoints.MaxAutoCheckpoints < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.max_auto_checkpoints: must be non-negative, got %d", cfg.Checkpoints.MaxAutoCheckpoints))
	}
	if cfg.Checkpoints.ScrollbackLines < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.scrollback_lines: must be non-negative, got %d", cfg.Checkpoints.ScrollbackLines))
	}
	if cfg.Checkpoints.IntervalMinutes < 0 {
		errs = append(errs, fmt.Errorf("checkpoints.interval_minutes: must be non-negative, got %d", cfg.Checkpoints.IntervalMinutes))
	}

	// Validate resilience
	if cfg.Resilience.MaxRestarts < 0 {
		errs = append(errs, fmt.Errorf("resilience.max_restarts: must be non-negative, got %d", cfg.Resilience.MaxRestarts))
	}
	if cfg.Resilience.RestartDelaySeconds < 0 {
		errs = append(errs, fmt.Errorf("resilience.restart_delay_seconds: must be non-negative, got %d", cfg.Resilience.RestartDelaySeconds))
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

	return errs
}
