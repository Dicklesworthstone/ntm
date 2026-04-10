// Package models provides a single canonical registry for AI model metadata,
// including context window limits and token budgets. All consumers of model
// context limits should import from this package rather than maintaining their
// own copies.
package models

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// DefaultContextLimit is the fallback when a model is not recognized.
const DefaultContextLimit = 128000

// dateSuffixRe strips date suffixes like -20260101 from model names.
var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// ContextLimits maps canonical model names to their context window sizes in tokens.
// These are approximate values based on published specifications.
var ContextLimits = map[string]int{
	// Anthropic Claude models
	"claude-sonnet-4":   200000,
	"claude-sonnet-4-5": 200000,
	"claude-sonnet-4-6": 200000,
	"claude-opus-4":     200000,
	"claude-opus-4-5":   200000,
	"claude-opus-4-6":   200000,
	"claude-haiku":      200000,
	"claude-haiku-4-5":  200000,
	"claude-3-opus":     200000,
	"claude-3-sonnet":   200000,
	"claude-3-haiku":    200000,
	"claude-3.5-sonnet": 200000,
	"claude-3-5-sonnet": 200000,
	"claude-3.5-haiku":  200000,
	"claude-3-5-haiku":  200000,

	// OpenAI models
	"gpt-4":       128000,
	"gpt-4-turbo": 128000,
	"gpt-4o":      128000,
	"gpt-4o-mini": 128000,
	"gpt-5":       256000,
	"gpt-5-codex": 256000,
	"gpt-5.1":     256000,
	"gpt-5.3":     256000,
	"o1":          128000,
	"o1-mini":     128000,
	"o1-preview":  128000,
	"o3":          200000,
	"o3-mini":     200000,
	"o4-mini":     128000,

	// Google Gemini models
	"gemini-2.0-flash":      1000000,
	"gemini-2.0-flash-lite": 1000000,
	"gemini-1.5-pro":        1000000,
	"gemini-1.5-flash":      1000000,
	"gemini-3-pro-preview":  1000000,
	"gemini-3-flash":        1000000,
	"gemini-pro":            32000,
}

// Aliases maps short names and common variants to canonical model names.
var Aliases = map[string]string{
	// Claude aliases
	"opus":          "claude-opus-4",
	"opus-4":        "claude-opus-4",
	"opus-4.5":      "claude-opus-4-5",
	"opus-4.6":      "claude-opus-4-6",
	"claude-opus":   "claude-opus-4",
	"claude-sonnet": "claude-sonnet-4",
	"sonnet":        "claude-sonnet-4",
	"sonnet-4":      "claude-sonnet-4",
	"sonnet-3.5":    "claude-3.5-sonnet",
	"sonnet-4.5":    "claude-sonnet-4-5",
	"sonnet-4.6":    "claude-sonnet-4-6",
	"haiku":         "claude-haiku",
	"haiku-3":       "claude-haiku",
	"haiku-4.5":     "claude-haiku-4-5",

	// OpenAI aliases
	"gpt4":       "gpt-4",
	"gpt4o":      "gpt-4o",
	"gpt4-turbo": "gpt-4-turbo",
	"gpt5":       "gpt-5",
	"codex":      "gpt-5-codex",

	// Gemini aliases
	"gemini":       "gemini-2.0-flash",
	"pro":          "gemini-1.5-pro",
	"flash":        "gemini-2.0-flash",
	"flash2":       "gemini-2.0-flash",
	"ultra":        "gemini-2.0-flash",
	"gemini-flash": "gemini-2.0-flash",
	"gemini-ultra": "gemini-2.0-flash",
}

var (
	registryMu sync.RWMutex
	sortedKeys []string
)

// rebuildSortedKeysLocked rebuilds the sortedKeys slice.
// Must be called with registryMu.Lock() held.
func rebuildSortedKeysLocked() {
	keys := make([]string, 0, len(ContextLimits))
	for k := range ContextLimits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	sortedKeys = keys
}

// ApplyOverrides merges user-provided context limit overrides into the
// built-in registry. Overrides take precedence over built-in values.
// Safe to call concurrently with GetContextLimit.
func ApplyOverrides(overrides map[string]int) {
	if len(overrides) == 0 {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()

	for model, limit := range overrides {
		ContextLimits[strings.ToLower(model)] = limit
	}

	// Force rebuild of sorted keys cache
	rebuildSortedKeysLocked()
}

// GetContextLimit returns the context window limit for a model identifier.
// Resolution order:
//  1. Exact match in ContextLimits
//  2. Alias resolution → exact match
//  3. Strip date suffix (e.g., -20260101) → exact match
//  4. Longest-prefix match in ContextLimits
//  5. DefaultContextLimit fallback
func GetContextLimit(model string) int {
	if model == "" {
		return DefaultContextLimit
	}

	registryMu.RLock()
	// Check if sortedKeys needs initialization
	if sortedKeys == nil {
		registryMu.RUnlock()
		registryMu.Lock()
		if sortedKeys == nil {
			rebuildSortedKeysLocked()
		}
		registryMu.Unlock()
		registryMu.RLock()
	}
	defer registryMu.RUnlock()

	lower := strings.ToLower(model)

	// 1. Exact match
	if limit, ok := ContextLimits[lower]; ok {
		return limit
	}

	// 2. Alias resolution
	if canonical, ok := Aliases[lower]; ok {
		if limit, ok := ContextLimits[canonical]; ok {
			return limit
		}
	}

	// 3. Strip date suffix
	stripped := dateSuffixRe.ReplaceAllString(lower, "")
	if stripped != lower {
		if limit, ok := ContextLimits[stripped]; ok {
			return limit
		}
		if canonical, ok := Aliases[stripped]; ok {
			if limit, ok := ContextLimits[canonical]; ok {
				return limit
			}
		}
	}

	// 4. Longest-prefix match
	for _, key := range sortedKeys {
		if strings.HasPrefix(lower, key) || strings.HasPrefix(stripped, key) {
			return ContextLimits[key]
		}
	}

	return DefaultContextLimit
}

// agentTypeBudgetPct defines what fraction of the model's context limit
// to allocate as a safe working budget per agent type. The remainder is
// reserved for system prompts, tool definitions, and overhead.
var agentTypeBudgetPct = map[string]float64{
	"cc":       0.90, // Claude: 90% of limit (well-documented system prompt overhead)
	"cod":      0.94, // Codex: 94% of limit
	"gmi":      0.10, // Gemini: 10% of 1M (still 100K tokens, avoids excessive context)
	"cursor":   0.85, // Cursor
	"windsurf": 0.85, // Windsurf
	"aider":    0.85, // Aider
	"ollama":   0.90, // Ollama
}

// agentTypeDefaultModels maps agent types to their default model for budget calculation.
var agentTypeDefaultModels = map[string]string{
	"cc":       "claude-opus-4",
	"cod":      "gpt-5-codex",
	"gmi":      "gemini-2.0-flash",
	"cursor":   "claude-3.5-sonnet",
	"windsurf": "claude-3.5-sonnet",
	"aider":    "claude-3.5-sonnet",
	"ollama":   "llama3",
}

// GetTokenBudget returns the safe working token budget for an agent type.
// This is derived from the model's context limit and a per-agent-type
// overhead percentage.
func GetTokenBudget(agentType string) int {
	agentType = string(agent.AgentType(agentType).Canonical())
	model, ok := agentTypeDefaultModels[agentType]
	if !ok {
		return 100000 // Safe default
	}

	limit := GetContextLimit(model)
	pct, ok := agentTypeBudgetPct[agentType]
	if !ok {
		pct = 0.78 // Conservative default
	}

	return int(float64(limit) * pct)
}
