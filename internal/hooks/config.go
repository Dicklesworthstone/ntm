// Config provides configuration loading for ntm command hooks.
// These hooks run before/after ntm commands like spawn, send, add, etc.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// CommandEvent represents an ntm command hook trigger point
type CommandEvent string

// Command hook events
const (
	EventPreSpawn     CommandEvent = "pre-spawn"
	EventPostSpawn    CommandEvent = "post-spawn"
	EventPreSend      CommandEvent = "pre-send"
	EventPostSend     CommandEvent = "post-send"
	EventPreAdd       CommandEvent = "pre-add"
	EventPostAdd      CommandEvent = "post-add"
	EventPreCreate    CommandEvent = "pre-create"
	EventPostCreate   CommandEvent = "post-create"
	EventPreKill      CommandEvent = "pre-kill"
	EventPostKill     CommandEvent = "post-kill"
	EventPreShutdown  CommandEvent = "pre-shutdown"
	EventPostShutdown CommandEvent = "post-shutdown"
)

// AllCommandEvents returns all valid command hook events
func AllCommandEvents() []CommandEvent {
	return []CommandEvent{
		EventPreSpawn, EventPostSpawn,
		EventPreSend, EventPostSend,
		EventPreAdd, EventPostAdd,
		EventPreCreate, EventPostCreate,
		EventPreKill, EventPostKill,
		EventPreShutdown, EventPostShutdown,
	}
}

// IsValidCommandEvent checks if an event string is a valid command hook event
func IsValidCommandEvent(event string) bool {
	for _, valid := range AllCommandEvents() {
		if CommandEvent(event) == valid {
			return true
		}
	}
	return false
}

// CommandHook represents a single command hook configuration
type CommandHook struct {
	// Event that triggers this hook (e.g., "pre-spawn", "post-send")
	Event CommandEvent `toml:"event"`

	// Command to execute (shell command)
	Command string `toml:"command"`

	// Timeout for command execution (default: 30s)
	Timeout Duration `toml:"timeout"`

	// Enabled controls whether this hook runs (default: true)
	Enabled *bool `toml:"enabled"` // Pointer to distinguish unset from false

	// WorkDir for command execution (optional, defaults to session project dir)
	WorkDir string `toml:"workdir"`

	// Description for documentation purposes
	Description string `toml:"description"`

	// Name is an optional identifier for the hook
	Name string `toml:"name"`

	// ContinueOnError determines if ntm continues if hook fails (default: false)
	ContinueOnError bool `toml:"continue_on_error"`

	// Env holds environment variables to set (merged with existing env)
	Env map[string]string `toml:"env"`
}

// Duration is a wrapper type for time.Duration that supports TOML parsing
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler for TOML duration parsing
func (d *Duration) UnmarshalText(text []byte) error {
	duration, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(duration)
	return nil
}

// MarshalText implements encoding.TextMarshaler for TOML duration serialization
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Duration returns the underlying time.Duration
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// CommandHookDefaults contains default values for command hooks
var CommandHookDefaults = struct {
	Timeout    time.Duration
	MaxTimeout time.Duration
	Enabled    bool
}{
	Timeout:    30 * time.Second,
	MaxTimeout: 10 * time.Minute,
	Enabled:    true,
}

// CommandHooksConfig holds all command hook configurations
type CommandHooksConfig struct {
	Hooks []CommandHook `toml:"command_hooks"`
}

// Validate checks if the command hook configuration is valid
func (h *CommandHook) Validate() error {
	if strings.TrimSpace(h.Command) == "" {
		return fmt.Errorf("hook command cannot be empty")
	}
	if !IsValidCommandEvent(string(h.Event)) {
		return fmt.Errorf("invalid hook event: %q (valid: %v)", h.Event, AllCommandEvents())
	}
	timeout := h.GetTimeout()
	if timeout < 0 {
		return fmt.Errorf("hook timeout cannot be negative")
	}
	if timeout > CommandHookDefaults.MaxTimeout {
		return fmt.Errorf("hook timeout exceeds maximum (%v)", CommandHookDefaults.MaxTimeout)
	}
	return nil
}

// GetTimeout returns the effective timeout for the hook
func (h *CommandHook) GetTimeout() time.Duration {
	if h.Timeout.Duration() <= 0 {
		return CommandHookDefaults.Timeout
	}
	return h.Timeout.Duration()
}

// IsEnabled returns whether the hook should run
func (h *CommandHook) IsEnabled() bool {
	if h.Enabled == nil {
		return CommandHookDefaults.Enabled
	}
	return *h.Enabled
}

// GetHooksForEvent returns all enabled hooks for a specific command event
func (c *CommandHooksConfig) GetHooksForEvent(event CommandEvent) []CommandHook {
	var result []CommandHook
	for _, h := range c.Hooks {
		if h.Event == event && h.IsEnabled() {
			result = append(result, h)
		}
	}
	return result
}

// HasHooksForEvent checks if there are any enabled hooks for a command event
func (c *CommandHooksConfig) HasHooksForEvent(event CommandEvent) bool {
	for _, h := range c.Hooks {
		if h.Event == event && h.IsEnabled() {
			return true
		}
	}
	return false
}

// Validate checks all hooks in the config
func (c *CommandHooksConfig) Validate() error {
	for i, h := range c.Hooks {
		if err := h.Validate(); err != nil {
			return fmt.Errorf("command_hooks[%d]: %w", i, err)
		}
	}
	return nil
}

// DefaultCommandHooksPath returns the default hooks config file path
func DefaultCommandHooksPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "hooks.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm", "hooks.toml")
}

// LoadCommandHooks loads command hooks configuration from a file.
// If path is empty, tries the default path.
// Returns an empty config (not an error) if no hooks file exists.
func LoadCommandHooks(path string) (*CommandHooksConfig, error) {
	if path == "" {
		path = DefaultCommandHooksPath()
	}

	// If file doesn't exist, return empty config (hooks are optional)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &CommandHooksConfig{Hooks: []CommandHook{}}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading hooks config: %w", err)
	}

	var cfg CommandHooksConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing hooks config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid hooks config: %w", err)
	}

	return &cfg, nil
}

// LoadCommandHooksFromTOML parses command hooks from TOML content directly
func LoadCommandHooksFromTOML(content string) (*CommandHooksConfig, error) {
	var cfg CommandHooksConfig
	if err := toml.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, fmt.Errorf("parsing hooks TOML: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid hooks config: %w", err)
	}

	return &cfg, nil
}

// LoadCommandHooksFromMainConfig extracts hooks from the main ntm config.
// This supports hooks defined inline in config.toml under [[command_hooks]]
func LoadCommandHooksFromMainConfig(mainConfigPath string) (*CommandHooksConfig, error) {
	if mainConfigPath == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			mainConfigPath = filepath.Join(xdg, "ntm", "config.toml")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, nil // No config to load
			}
			mainConfigPath = filepath.Join(home, ".config", "ntm", "config.toml")
		}
	}

	if _, err := os.Stat(mainConfigPath); os.IsNotExist(err) {
		return &CommandHooksConfig{Hooks: []CommandHook{}}, nil
	}

	data, err := os.ReadFile(mainConfigPath)
	if err != nil {
		return nil, fmt.Errorf("reading main config: %w", err)
	}

	var cfg CommandHooksConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing main config: %w", err)
	}

	// Validate only if we found hooks
	if len(cfg.Hooks) > 0 {
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("invalid hooks in main config: %w", err)
		}
	}

	return &cfg, nil
}

// DefaultCommandHooksDir returns the default hooks directory path
func DefaultCommandHooksDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "hooks")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm", "hooks")
}

// LoadHooksFromDirectory loads command hooks from all .toml files in a directory
func LoadHooksFromDirectory(dir string) (*CommandHooksConfig, error) {
	if dir == "" {
		dir = DefaultCommandHooksDir()
	}

	combined := &CommandHooksConfig{
		Hooks: []CommandHook{},
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return combined, nil
		}
		return nil, fmt.Errorf("reading hooks directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		cfg, err := LoadCommandHooks(path)
		if err != nil {
			return nil, fmt.Errorf("loading hook file %s: %w", entry.Name(), err)
		}
		combined.Hooks = append(combined.Hooks, cfg.Hooks...)
	}

	return combined, nil
}

// LoadAllCommandHooks loads hooks from hooks.toml, hooks/*.toml, and config.toml
func LoadAllCommandHooks() (*CommandHooksConfig, error) {
	var allHooks []CommandHook

	// 1. Load from dedicated hooks.toml
	hooksConfig, err := LoadCommandHooks("")
	if err != nil {
		return nil, err
	}
	allHooks = append(allHooks, hooksConfig.Hooks...)

	// 2. Load from hooks directory
	dirHooksConfig, err := LoadHooksFromDirectory("")
	if err != nil {
		return nil, err
	}
	allHooks = append(allHooks, dirHooksConfig.Hooks...)

	// 3. Load from main config
	mainConfig, err := LoadCommandHooksFromMainConfig("")
	if err != nil {
		return nil, err
	}
	allHooks = append(allHooks, mainConfig.Hooks...)

	return &CommandHooksConfig{Hooks: allHooks}, nil
}

// ExpandWorkDir expands the workdir for a command hook, substituting variables
func (h *CommandHook) ExpandWorkDir(sessionName, projectDir string) string {
	if h.WorkDir == "" {
		return projectDir
	}

	workDir := h.WorkDir

	// Expand ~/ prefix
	if strings.HasPrefix(workDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			workDir = filepath.Join(home, workDir[2:])
		}
		// If UserHomeDir fails, leave workDir with ~/ prefix (will fail cleanly downstream)
	}

	// Expand variables
	workDir = strings.ReplaceAll(workDir, "${SESSION}", sessionName)
	workDir = strings.ReplaceAll(workDir, "${PROJECT}", projectDir)
	workDir = os.ExpandEnv(workDir)

	return workDir
}

// EmptyCommandHooksConfig returns a new empty CommandHooksConfig
func EmptyCommandHooksConfig() *CommandHooksConfig {
	return &CommandHooksConfig{Hooks: []CommandHook{}}
}
