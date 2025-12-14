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
)

// Config represents the main configuration
type Config struct {
	ProjectsBase  string            `toml:"projects_base"`
	Theme         string            `toml:"theme"`        // UI Theme (mocha, macchiato, nord, latte, auto)
	PaletteFile   string            `toml:"palette_file"` // Path to command_palette.md (optional)
	Agents        AgentConfig       `toml:"agents"`
	Palette       []PaletteCmd      `toml:"palette"`
	PaletteState  PaletteState      `toml:"palette_state"`
	Tmux          TmuxConfig        `toml:"tmux"`
	AgentMail     AgentMailConfig   `toml:"agent_mail"`
	Models        ModelsConfig      `toml:"models"`
	Alerts        AlertsConfig      `toml:"alerts"`
	Checkpoints   CheckpointsConfig `toml:"checkpoints"`
	Notifications notify.Config     `toml:"notifications"`
	Resilience    ResilienceConfig  `toml:"resilience"`
	Scanner       ScannerConfig     `toml:"scanner"`      // UBS scanner configuration
	CASS          CASSConfig        `toml:"cass"`         // CASS integration configuration
	Accounts      AccountsConfig    `toml:"accounts"`     // Multi-account management
	Rotation      RotationConfig    `toml:"rotation"`     // Account rotation configuration
	GeminiSetup   GeminiSetupConfig `toml:"gemini_setup"` // Gemini post-spawn setup

	// Runtime-only fields (populated by project config merging)
	ProjectDefaults map[string]int `toml:"-"`
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
	Enabled      bool `toml:"enabled"`       // Auto-inject context when spawning
	MaxSessions  int  `toml:"max_sessions"`  // Max past sessions to include
	LookbackDays int  `toml:"lookback_days"` // How far back to search
	MaxTokens    int  `toml:"max_tokens"`    // Token budget for context
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
			Enabled:      true,
			MaxSessions:  3,
			LookbackDays: 30,
			MaxTokens:    2000,
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
	Claude string `toml:"claude"`
	Codex  string `toml:"codex"`
	Gemini string `toml:"gemini"`
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
		ReadyTimeoutSeconds:       30,    // 30 seconds to wait for ready
		ModelSelectTimeoutSeconds: 10,    // 10 seconds for model menu
		Verbose:                   false, // Quiet by default
	}
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
	DefaultPanes int    `toml:"default_panes"`
	PaletteKey   string `toml:"palette_key"`
}

// AgentMailConfig holds Agent Mail server settings
type AgentMailConfig struct {
	Enabled      bool   `toml:"enabled"`       // Master toggle
	URL          string `toml:"url"`           // Server endpoint
	Token        string `toml:"token"`         // Bearer token
	AutoRegister bool   `toml:"auto_register"` // Auto-register sessions as agents
	ProgramName  string `toml:"program_name"`  // Program identifier for registration
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
		DefaultClaude: "claude-sonnet-4-20250514",
		DefaultCodex:  "gpt-4",
		DefaultGemini: "gemini-2.0-flash",
		Claude: map[string]string{
			"opus":      "claude-opus-4-20250514",
			"sonnet":    "claude-sonnet-4-20250514",
			"haiku":     "claude-haiku-3-20240307",
			"architect": "claude-opus-4-20250514",
			"fast":      "claude-sonnet-4-20250514",
		},
		Codex: map[string]string{
			"gpt4":  "gpt-4",
			"o1":    "o1",
			"turbo": "gpt-4-turbo",
			"max":   "gpt-5.1-codex-max",
		},
		Gemini: map[string]string{
			"pro":   "gemini-pro",
			"flash": "gemini-2.0-flash",
			"ultra": "gemini-ultra",
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
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ntm", "config.toml")
}

// DefaultProjectsBase returns the default projects directory
func DefaultProjectsBase() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Developer")
	}
	return "/data/projects"
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
			DefaultPanes: 10,
			PaletteKey:   "F6",
		},
		AgentMail: AgentMailConfig{
			Enabled:      true,
			URL:          DefaultAgentMailURL,
			Token:        "",
			AutoRegister: true,
			ProgramName:  "ntm",
		},
		Models:        DefaultModels(),
		Alerts:        DefaultAlertsConfig(),
		Checkpoints:   DefaultCheckpointsConfig(),
		Notifications: notify.DefaultConfig(),
		Resilience:    DefaultResilienceConfig(),
		Scanner:       DefaultScannerConfig(),
		CASS:          DefaultCASSConfig(),
		Accounts:      DefaultAccountsConfig(),
		Rotation:      DefaultRotationConfig(),
		GeminiSetup:   DefaultGeminiSetupConfig(),
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

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults for missing values
	if cfg.ProjectsBase == "" {
		cfg.ProjectsBase = DefaultProjectsBase()
	}
	// Environment variable override for projects base directory
	if envBase := os.Getenv("NTM_PROJECTS_BASE"); envBase != "" {
		cfg.ProjectsBase = envBase
	}
	if cfg.Agents.Claude == "" {
		cfg.Agents.Claude = Default().Agents.Claude
	}
	if cfg.Agents.Codex == "" {
		cfg.Agents.Codex = Default().Agents.Codex
	}
	if cfg.Agents.Gemini == "" {
		cfg.Agents.Gemini = Default().Agents.Gemini
	}
	if cfg.Tmux.DefaultPanes == 0 {
		cfg.Tmux.DefaultPanes = 10
	}
	if cfg.Tmux.PaletteKey == "" {
		cfg.Tmux.PaletteKey = "F6"
	}

	// Apply AgentMail defaults
	if cfg.AgentMail.URL == "" {
		cfg.AgentMail.URL = DefaultAgentMailURL
	}
	if cfg.AgentMail.ProgramName == "" {
		cfg.AgentMail.ProgramName = "ntm"
	}

	// Environment variable overrides for AgentMail
	if url := os.Getenv("AGENT_MAIL_URL"); url != "" {
		cfg.AgentMail.URL = url
	}
	if token := os.Getenv("AGENT_MAIL_TOKEN"); token != "" {
		cfg.AgentMail.Token = token
	}
	if enabled := os.Getenv("AGENT_MAIL_ENABLED"); enabled != "" {
		cfg.AgentMail.Enabled = enabled == "1" || enabled == "true"
	}

	// Apply Alerts defaults
	defaults := DefaultAlertsConfig()
	if cfg.Alerts.AgentStuckMinutes == 0 {
		cfg.Alerts.AgentStuckMinutes = defaults.AgentStuckMinutes
	}
	if cfg.Alerts.DiskLowThresholdGB == 0 {
		cfg.Alerts.DiskLowThresholdGB = defaults.DiskLowThresholdGB
	}
	if cfg.Alerts.MailBacklogThreshold == 0 {
		cfg.Alerts.MailBacklogThreshold = defaults.MailBacklogThreshold
	}
	if cfg.Alerts.BeadStaleHours == 0 {
		cfg.Alerts.BeadStaleHours = defaults.BeadStaleHours
	}
	if cfg.Alerts.ResolvedPruneMinutes == 0 {
		cfg.Alerts.ResolvedPruneMinutes = defaults.ResolvedPruneMinutes
	}

	// Apply Checkpoints defaults
	cpDefaults := DefaultCheckpointsConfig()
	// Note: Enabled defaults to false from TOML, but we want true by default
	// Only override if section is completely missing (checked by MaxAutoCheckpoints)
	if cfg.Checkpoints.MaxAutoCheckpoints == 0 {
		cfg.Checkpoints.MaxAutoCheckpoints = cpDefaults.MaxAutoCheckpoints
	}
	if cfg.Checkpoints.ScrollbackLines == 0 {
		cfg.Checkpoints.ScrollbackLines = cpDefaults.ScrollbackLines
	}
	// For bool fields, if checkpoints section is missing, apply defaults
	// We detect this by checking if MaxAutoCheckpoints was 0 (now set to default)
	if cfg.Checkpoints.MaxAutoCheckpoints == cpDefaults.MaxAutoCheckpoints && !cfg.Checkpoints.Enabled {
		// Section likely missing, apply all defaults
		cfg.Checkpoints = cpDefaults
	}

	// Apply Notifications defaults
	// If Events is empty, apply all defaults (section likely missing)
	if len(cfg.Notifications.Events) == 0 {
		cfg.Notifications = notify.DefaultConfig()
	}

	// Apply Resilience defaults
	// If MaxRestarts is 0, apply all defaults (section likely missing)
	if cfg.Resilience.MaxRestarts == 0 {
		cfg.Resilience = DefaultResilienceConfig()
	}

	// Apply Scanner defaults
	// If Timeout is empty, apply defaults (section likely missing)
	if cfg.Scanner.Defaults.Timeout == "" {
		cfg.Scanner = DefaultScannerConfig()
	}
	// Apply environment variable overrides for scanner
	applyEnvOverrides(&cfg.Scanner)

	// Apply CASS defaults for individual fields
	// We check each field separately to avoid overwriting user-specified values
	cassDefaults := DefaultCASSConfig()
	if cfg.CASS.Timeout == 0 {
		cfg.CASS.Timeout = cassDefaults.Timeout
	}
	// For nested configs, check if they appear unset (all zero values)
	if cfg.CASS.Context.MaxSessions == 0 && cfg.CASS.Context.LookbackDays == 0 {
		cfg.CASS.Context = cassDefaults.Context
	}
	if cfg.CASS.Duplicates.LookbackDays == 0 && cfg.CASS.Duplicates.SimilarityThreshold == 0 {
		cfg.CASS.Duplicates = cassDefaults.Duplicates
	}
	if cfg.CASS.Search.DefaultLimit == 0 {
		cfg.CASS.Search = cassDefaults.Search
	}
	// TUI booleans default to false in Go, but we want true by default.
	// If both are false (Go zero value), apply TUI defaults.
	// Users who explicitly want both disabled is an edge case we accept.
	if !cfg.CASS.TUI.ShowActivitySparkline && !cfg.CASS.TUI.ShowStatusIndicator {
		cfg.CASS.TUI = cassDefaults.TUI
	}

	// Apply environment variable overrides for CASS
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

	// Apply Accounts defaults
	accountsDefaults := DefaultAccountsConfig()
	if cfg.Accounts.StateFile == "" {
		cfg.Accounts.StateFile = accountsDefaults.StateFile
	}
	if cfg.Accounts.ResetBufferMinutes == 0 {
		cfg.Accounts.ResetBufferMinutes = accountsDefaults.ResetBufferMinutes
	}
	// AutoRotate defaults to true, so only set if entire section appears missing
	// We detect this by checking if StateFile was empty (now set to default)
	if cfg.Accounts.StateFile == accountsDefaults.StateFile && !cfg.Accounts.AutoRotate && len(cfg.Accounts.Claude) == 0 {
		cfg.Accounts.AutoRotate = accountsDefaults.AutoRotate
	}

	// Apply Rotation defaults
	rotationDefaults := DefaultRotationConfig()
	if cfg.Rotation.ContinuationPrompt == "" {
		cfg.Rotation.ContinuationPrompt = rotationDefaults.ContinuationPrompt
	}
	if cfg.Rotation.Thresholds.WarningPercent == 0 {
		cfg.Rotation.Thresholds.WarningPercent = rotationDefaults.Thresholds.WarningPercent
	}
	if cfg.Rotation.Thresholds.CriticalPercent == 0 {
		cfg.Rotation.Thresholds.CriticalPercent = rotationDefaults.Thresholds.CriticalPercent
	}
	if cfg.Rotation.Thresholds.RestartIfTokensAbove == 0 {
		cfg.Rotation.Thresholds.RestartIfTokensAbove = rotationDefaults.Thresholds.RestartIfTokensAbove
	}
	if cfg.Rotation.Thresholds.RestartIfSessionHours == 0 {
		cfg.Rotation.Thresholds.RestartIfSessionHours = rotationDefaults.Thresholds.RestartIfSessionHours
	}
	// Dashboard bools default to false; if all are false, apply defaults
	if !cfg.Rotation.Dashboard.ShowQuotaBars && !cfg.Rotation.Dashboard.ShowAccountStatus && !cfg.Rotation.Dashboard.ShowResetTimers {
		cfg.Rotation.Dashboard = rotationDefaults.Dashboard
	}

	// Environment variable overrides for accounts/rotation
	if autoRotate := os.Getenv("NTM_ACCOUNTS_AUTO_ROTATE"); autoRotate != "" {
		cfg.Accounts.AutoRotate = autoRotate == "1" || autoRotate == "true"
	}
	if rotationEnabled := os.Getenv("NTM_ROTATION_ENABLED"); rotationEnabled != "" {
		cfg.Rotation.Enabled = rotationEnabled == "1" || rotationEnabled == "true"
	}

	// Apply GeminiSetup defaults
	// If ReadyTimeoutSeconds is 0, the section was likely missing - apply all defaults
	geminiDefaults := DefaultGeminiSetupConfig()
	if cfg.GeminiSetup.ReadyTimeoutSeconds == 0 {
		cfg.GeminiSetup.ReadyTimeoutSeconds = geminiDefaults.ReadyTimeoutSeconds
	}
	if cfg.GeminiSetup.ModelSelectTimeoutSeconds == 0 {
		cfg.GeminiSetup.ModelSelectTimeoutSeconds = geminiDefaults.ModelSelectTimeoutSeconds
	}
	// AutoSelectProModel defaults to true, but Go's zero value is false.
	// If both timeouts were 0 (now set to defaults), assume section was missing and apply AutoSelectProModel default.
	if cfg.GeminiSetup.ReadyTimeoutSeconds == geminiDefaults.ReadyTimeoutSeconds &&
		cfg.GeminiSetup.ModelSelectTimeoutSeconds == geminiDefaults.ModelSelectTimeoutSeconds &&
		!cfg.GeminiSetup.AutoSelectProModel {
		// Section likely missing - apply all defaults including AutoSelectProModel=true
		cfg.GeminiSetup = geminiDefaults
	}

	// Environment variable override for Gemini setup
	if autoSelect := os.Getenv("NTM_GEMINI_AUTO_PRO"); autoSelect != "" {
		cfg.GeminiSetup.AutoSelectProModel = autoSelect == "1" || autoSelect == "true"
	}

	// Try to load palette from markdown file
	// This takes precedence over TOML [[palette]] entries
	mdPath := cfg.PaletteFile
	if mdPath == "" {
		mdPath = findPaletteMarkdown()
	} else {
		mdPath = ExpandHome(mdPath)
	}

	if mdPath != "" {
		if mdCmds, err := LoadPaletteFromMarkdown(mdPath); err == nil && len(mdCmds) > 0 {
			cfg.Palette = mdCmds
			return &cfg, nil
		}
	}

	// If no palette commands from TOML, use defaults
	if len(cfg.Palette) == 0 {
		cfg.Palette = defaultPaletteCommands()
	}

	return &cfg, nil
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
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if err := Print(Default(), f); err != nil {
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

	return os.WriteFile(path, []byte(updated), mode)
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
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[tmux]")
	fmt.Fprintln(w, "# Tmux-specific settings")
	fmt.Fprintf(w, "default_panes = %d\n", cfg.Tmux.DefaultPanes)
	fmt.Fprintf(w, "palette_key = %q\n", cfg.Tmux.PaletteKey)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "[agent_mail]")
	fmt.Fprintln(w, "# Agent Mail server settings for multi-agent coordination")
	fmt.Fprintln(w, "# Environment variables: AGENT_MAIL_URL, AGENT_MAIL_TOKEN, AGENT_MAIL_ENABLED")
	fmt.Fprintf(w, "enabled = %t\n", cfg.AgentMail.Enabled)
	fmt.Fprintf(w, "url = %q\n", cfg.AgentMail.URL)
	if cfg.AgentMail.Token != "" {
		fmt.Fprintf(w, "token = %q\n", cfg.AgentMail.Token)
	} else {
		fmt.Fprintln(w, "# token = \"\"  # Or set AGENT_MAIL_TOKEN env var")
	}
	fmt.Fprintf(w, "auto_register = %t\n", cfg.AgentMail.AutoRegister)
	fmt.Fprintf(w, "program_name = %q\n", cfg.AgentMail.ProgramName)
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

	// Write Gemini setup configuration
	fmt.Fprintln(w, "[gemini_setup]")
	fmt.Fprintln(w, "# Gemini CLI post-spawn setup configuration")
	fmt.Fprintln(w, "# When enabled, NTM automatically selects the Pro model after spawning Gemini agents")
	fmt.Fprintf(w, "auto_select_pro_model = %t       # Auto-select Pro model (Gemini 3) on spawn\n", cfg.GeminiSetup.AutoSelectProModel)
	fmt.Fprintf(w, "ready_timeout_seconds = %d       # Seconds to wait for Gemini CLI to be ready\n", cfg.GeminiSetup.ReadyTimeoutSeconds)
	fmt.Fprintf(w, "model_select_timeout_seconds = %d # Seconds to wait for model selection menu\n", cfg.GeminiSetup.ModelSelectTimeoutSeconds)
	fmt.Fprintf(w, "verbose = %t                     # Show debug output during setup\n", cfg.GeminiSetup.Verbose)
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
	if err := os.WriteFile(configPath, []byte(fileContents), 0644); err != nil {
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
