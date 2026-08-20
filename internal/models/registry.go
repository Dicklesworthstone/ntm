// Package models provides a single canonical registry for AI model metadata,
// including context window limits and token budgets. All consumers of model
// context limits should import from this package rather than maintaining their
// own copies.
package models

import (
	"log/slog"
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
	"claude-opus-5":     200000,
	"claude-sonnet-5":   200000,
	"claude-fable-5":    200000,
	"claude-mythos-5":   200000,
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
	"gemini-3.7-flash":      1000000,
	"gemini-pro":            32000,
}

// Aliases maps short names and common variants to canonical model names.
var Aliases = map[string]string{
	// Claude aliases
	"opus":          "claude-opus-4",
	"opus-4":        "claude-opus-4",
	"opus-4.5":      "claude-opus-4-5",
	"opus-4.6":      "claude-opus-4-6",
	"opus-5":        "claude-opus-5",
	"sonnet-5":      "claude-sonnet-5",
	"fable":         "claude-fable-5",
	"fable-5":       "claude-fable-5",
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
		if limit <= 0 {
			// A non-positive limit is a division-by-zero waiting to happen in
			// every usage-percentage computation, and the resulting NaN/+Inf
			// cannot be JSON-encoded — which took down whole robot responses.
			// Ignore the override and keep the built-in value.
			slog.Warn("ignoring non-positive context limit override",
				"model", model, "limit", limit)
			continue
		}
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

// KnownModel reports whether a model identifier resolves in the registry
// through the same rules GetContextLimit uses (exact match, alias, date-suffix
// strip, or longest-prefix match) — i.e. whether GetContextLimit would return
// a real registry value rather than the DefaultContextLimit fallback.
func KnownModel(model string) bool {
	if model == "" {
		return false
	}
	registryMu.RLock()
	defer registryMu.RUnlock()

	lower := strings.ToLower(model)
	if _, ok := ContextLimits[lower]; ok {
		return true
	}
	if canonical, ok := Aliases[lower]; ok {
		if _, ok := ContextLimits[canonical]; ok {
			return true
		}
	}
	stripped := dateSuffixRe.ReplaceAllString(lower, "")
	if stripped != lower {
		if _, ok := ContextLimits[stripped]; ok {
			return true
		}
		if canonical, ok := Aliases[stripped]; ok {
			if _, ok := ContextLimits[canonical]; ok {
				return true
			}
		}
	}
	for key := range ContextLimits {
		if strings.HasPrefix(lower, key) || strings.HasPrefix(stripped, key) {
			return true
		}
	}
	return false
}

// SuggestModel returns the canonical registry identifier closest to an
// unrecognized model ID within a small edit distance, or "" when the ID is
// already known or no confident match exists. Field evidence shows agents
// mistyping near-miss model IDs on spawn (e.g. "claude-opus5"); a
// did-you-mean pointing at the real registry ID saves a discovery
// round-trip. Alias matches resolve to their canonical name.
func SuggestModel(model string) string {
	if model == "" || KnownModel(model) {
		return ""
	}
	registryMu.RLock()
	defer registryMu.RUnlock()

	lower := strings.ToLower(model)
	best := ""
	bestDist := len(lower)/3 + 2 // confidence bound scales with length
	// A suggestion that rewrites the whole input is noise, not a near miss:
	// without this cap, "x" suggested "o1" (distance 2 ≥ the entire input).
	if bestDist >= len(lower) {
		bestDist = len(lower) - 1
	}
	if bestDist < 1 {
		return ""
	}
	bestPrefix := -1
	bestSuffix := -1
	consider := func(candidate, canonical string) {
		d := editDistance(lower, candidate)
		if d > bestDist {
			return
		}
		// Ties break toward the candidate sharing the longest prefix with
		// the guess, then the longest suffix ("claude-opus5" should suggest
		// claude-opus-5, not the equally distant "claude-opus" alias), then
		// lexicographically for deterministic output across map iteration
		// orders.
		p := 0
		for p < len(lower) && p < len(candidate) && lower[p] == candidate[p] {
			p++
		}
		s := 0
		for s < len(lower) && s < len(candidate) && lower[len(lower)-1-s] == candidate[len(candidate)-1-s] {
			s++
		}
		better := d < bestDist
		if !better && d == bestDist {
			switch {
			case p != bestPrefix:
				better = p > bestPrefix
			case s != bestSuffix:
				better = s > bestSuffix
			default:
				better = best == "" || canonical < best
			}
		}
		if better {
			bestDist = d
			bestPrefix = p
			bestSuffix = s
			best = canonical
		}
	}
	for key := range ContextLimits {
		consider(key, key)
	}
	for alias, canonical := range Aliases {
		if _, ok := ContextLimits[canonical]; ok {
			consider(alias, canonical)
		}
	}
	return best
}

// editDistance is a plain O(len(a)*len(b)) Levenshtein distance over bytes;
// model IDs are short ASCII so this is cheap.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// agentTypeBudgetPct defines what fraction of the model's context limit
// to allocate as a safe working budget per agent type. The remainder is
// reserved for system prompts, tool definitions, and overhead.
var agentTypeBudgetPct = map[string]float64{
	"cc":       0.90, // Claude: 90% of limit (well-documented system prompt overhead)
	"cod":      0.94, // Codex: 94% of limit
	"gmi":      0.10, // Gemini: 10% of 1M (still 100K tokens, avoids excessive context)
	"agy":      0.10, // Antigravity (Gemini successor): mirrors Gemini's budget
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
	"agy":      "gemini-3.7-flash", // Antigravity pins Gemini 3.7 Flash
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
