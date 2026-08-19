package dcg

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ClaudeHookConfig represents the Claude Code hooks configuration format.
// See: https://docs.anthropic.com/en/docs/claude-code/hooks
type ClaudeHookConfig struct {
	Hooks HooksSection `json:"hooks"`
}

// HooksSection contains the different hook types.
type HooksSection struct {
	PreToolUse []HookEntry `json:"PreToolUse,omitempty"`
}

// HookEntry represents a matcher group in Claude Code's hook configuration.
type HookEntry struct {
	Matcher string        `json:"matcher"` // Tool name to match (e.g., "Bash")
	Hooks   []HookHandler `json:"hooks"`   // Hook handlers to run when the matcher fires
}

// HookHandler represents a single hook command within a matcher group.
type HookHandler struct {
	Type    string `json:"type"`              // Hook handler type (e.g., "command")
	Command string `json:"command,omitempty"` // Command to run
	Timeout int    `json:"timeout,omitempty"` // Optional timeout in seconds
}

// DCGHookOptions configures how DCG hooks are generated.
type DCGHookOptions struct {
	// BinaryPath is the path to the dcg binary. If empty, "dcg" is used (PATH lookup).
	BinaryPath string

	// AuditLog is not supported by modern dcg hook-mode command generation.
	// Configure dcg's own config/allowlist files instead of passing inline policy flags.
	AuditLog string

	// Timeout is the hook timeout in seconds. Default is 5 seconds.
	Timeout int

	// CustomBlocklist is not supported by modern dcg hook-mode command generation.
	// Configure dcg's own pack/config files instead of passing inline policy flags.
	CustomBlocklist []string

	// CustomWhitelist is not supported by modern dcg hook-mode command generation.
	// Configure dcg's own allowlist files instead of passing inline policy flags.
	CustomWhitelist []string
}

// DefaultDCGHookOptions returns sensible defaults for DCG hook configuration.
func DefaultDCGHookOptions() DCGHookOptions {
	return DCGHookOptions{
		Timeout: 5,
	}
}

// GenerateHookConfig creates a Claude Code hook configuration for DCG.
// The generated hook intercepts Bash tool calls and validates them against DCG.
func GenerateHookConfig(opts DCGHookOptions) (*ClaudeHookConfig, error) {
	dcgBinary := opts.BinaryPath
	if dcgBinary == "" {
		dcgBinary = "dcg"
	}

	if opts.AuditLog != "" {
		return nil, fmt.Errorf("dcg hook generation does not support inline audit_log; configure dcg directly")
	}
	if len(opts.CustomBlocklist) > 0 {
		return nil, fmt.Errorf("dcg hook generation does not support inline custom_blocklist; configure dcg packs directly")
	}
	if len(opts.CustomWhitelist) > 0 {
		return nil, fmt.Errorf("dcg hook generation does not support inline custom_whitelist; configure dcg allowlists directly")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultDCGHookOptions().Timeout
	}

	config := &ClaudeHookConfig{
		Hooks: HooksSection{
			PreToolUse: []HookEntry{
				newCommandHookEntry("Bash", shellQuote(dcgBinary), timeout),
			},
		},
	}

	return config, nil
}

func newCommandHookEntry(matcher, command string, timeout int) HookEntry {
	return HookEntry{
		Matcher: matcher,
		Hooks: []HookHandler{
			{
				Type:    "command",
				Command: command,
				Timeout: timeout,
			},
		},
	}
}

// DCGAvailability tracks whether DCG is available and can be used for hooks.
type DCGAvailability struct {
	Available   bool
	BinaryPath  string
	Version     string
	LastChecked time.Time
	Error       string
}

var (
	dcgAvailabilityCache DCGAvailability
	dcgAvailabilityMutex sync.RWMutex
	dcgCacheTTL          = 5 * time.Minute
)

// CheckDCGAvailable checks if dcg is installed and available.
func CheckDCGAvailable(binaryPath string) DCGAvailability {
	dcgAvailabilityMutex.RLock()
	if time.Since(dcgAvailabilityCache.LastChecked) < dcgCacheTTL {
		cached := dcgAvailabilityCache
		dcgAvailabilityMutex.RUnlock()
		return cached
	}
	dcgAvailabilityMutex.RUnlock()

	result := checkDCGAvailabilityUncached(binaryPath)

	dcgAvailabilityMutex.Lock()
	dcgAvailabilityCache = result
	dcgAvailabilityMutex.Unlock()

	return result
}

func checkDCGAvailabilityUncached(binaryPath string) DCGAvailability {
	result := DCGAvailability{
		LastChecked: time.Now(),
	}

	binary := binaryPath
	if binary == "" {
		binary = "dcg"
	}

	// Check if binary exists
	path, err := exec.LookPath(binary)
	if err != nil {
		result.Error = fmt.Sprintf("dcg not found: %v", err)
		return result
	}

	result.BinaryPath = path
	result.Available = true

	// Try to get version
	cmd := exec.Command(path, "--version")
	output, err := cmd.Output()
	if err == nil {
		result.Version = strings.TrimSpace(string(output))
	}

	return result
}

// ShouldConfigureHooks determines if DCG hooks should be configured
// for an agent spawn based on DCG availability and configuration.
func ShouldConfigureHooks(dcgEnabled bool, binaryPath string) bool {
	if !dcgEnabled {
		return false
	}

	availability := CheckDCGAvailable(binaryPath)
	return availability.Available
}
