package dcg

import (
	"fmt"
	"strings"
)

const defaultRCHHookTimeout = 5

// RCHHookOptions configures how the RCH hook is generated.
type RCHHookOptions struct {
	// BinaryPath is the path to the rch binary. If empty, "rch" is used (PATH lookup).
	BinaryPath string

	// Patterns are regex patterns that determine whether ntm configures the hook.
	// Modern rch hook mode receives Claude's hook JSON on stdin and applies its
	// own interception policy.
	Patterns []string

	// Timeout is the hook timeout in seconds. Default is 5 seconds.
	Timeout int
}

// ShouldConfigureRCHHooks determines if RCH hooks should be configured.
func ShouldConfigureRCHHooks(rchEnabled bool, patterns []string) bool {
	if !rchEnabled {
		return false
	}
	return len(cleanRCHPatterns(patterns)) > 0
}

// GenerateRCHHookEntry creates a Claude Code hook entry for RCH interception.
func GenerateRCHHookEntry(opts RCHHookOptions) (HookEntry, error) {
	patterns := cleanRCHPatterns(opts.Patterns)
	if len(patterns) == 0 {
		return HookEntry{}, fmt.Errorf("rch hook requires at least one intercept pattern")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRCHHookTimeout
	}

	rchBinary := strings.TrimSpace(opts.BinaryPath)
	if rchBinary == "" {
		rchBinary = "rch"
	}

	return newCommandHookEntry("Bash", shellQuote(rchBinary), timeout), nil
}

func cleanRCHPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(patterns))
	cleaned := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		cleaned = append(cleaned, pattern)
	}
	return cleaned
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
