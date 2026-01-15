// Package robot provides machine-readable output for AI agents and automation.
// patterns.go implements a configurable pattern library for agent state detection.
package robot

import (
	"regexp"
	"sort"
	"sync"
)

// AgentState represents the detected state of an agent.
type AgentState string

const (
	// StateGenerating indicates agent is actively producing output (high velocity).
	StateGenerating AgentState = "GENERATING"

	// StateWaiting indicates agent is idle and ready for input.
	StateWaiting AgentState = "WAITING"

	// StateThinking indicates agent is processing (low velocity, thinking indicator).
	StateThinking AgentState = "THINKING"

	// StateError indicates an error condition was detected.
	StateError AgentState = "ERROR"

	// StateStalled indicates no output when activity was expected.
	StateStalled AgentState = "STALLED"

	// StateUnknown indicates insufficient signals to classify state.
	StateUnknown AgentState = "UNKNOWN"
)

// PatternCategory categorizes patterns for organization and filtering.
type PatternCategory string

const (
	CategoryIdle       PatternCategory = "idle"       // Idle/prompt patterns
	CategoryError      PatternCategory = "error"      // Error conditions
	CategoryThinking   PatternCategory = "thinking"   // Processing indicators
	CategoryCompletion PatternCategory = "completion" // Task completion signals
)

// Pattern represents a single pattern for detecting agent states.
type Pattern struct {
	Name        string          `json:"name" toml:"name"`
	Regex       *regexp.Regexp  `json:"-"` // Compiled regex (not serialized)
	RegexStr    string          `json:"regex" toml:"regex"`
	Agent       string          `json:"agent" toml:"agent"`       // "claude", "codex", "gemini", "*" for all
	State       AgentState      `json:"state" toml:"state"`       // Target state if matched
	Category    PatternCategory `json:"category" toml:"category"` // Pattern category
	Priority    int             `json:"priority" toml:"priority"` // Higher = checked first
	Description string          `json:"description" toml:"description"`
}

// PatternLibrary manages a collection of patterns for state detection.
type PatternLibrary struct {
	Version  string    `json:"version" toml:"pattern_version"`
	Patterns []Pattern `json:"patterns" toml:"patterns"`
	compiled bool
	mu       sync.RWMutex
}

// NewPatternLibrary creates a new pattern library with default patterns.
func NewPatternLibrary() *PatternLibrary {
	lib := &PatternLibrary{
		Version:  "1.0",
		Patterns: defaultPatterns(),
	}
	lib.Compile()
	return lib
}

// defaultPatterns returns the built-in default pattern set.
func defaultPatterns() []Pattern {
	return []Pattern{
		// ==================
		// IDLE PROMPTS (agent ready for input)
		// ==================
		// Claude patterns
		{Name: "claude_prompt", RegexStr: `(?i)claude\s*>?\s*$`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 100, Description: "Claude prompt"},
		{Name: "claude_code_prompt", RegexStr: `(?i)claude\s+code\s*>?\s*$`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 101, Description: "Claude Code prompt"},
		{Name: "claude_arrow_prompt", RegexStr: `╰─>\s*$`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 99, Description: "Claude arrow prompt"},
		// Claude Code TUI indicators (welcome screen and status line)
		{Name: "claude_code_version", RegexStr: `(?i)claude\s+code\s+v[\d.]+`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 102, Description: "Claude Code version banner"},
		{Name: "claude_welcome", RegexStr: `(?i)welcome\s+back`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 102, Description: "Claude Code welcome message"},
		{Name: "claude_bypass_status", RegexStr: `bypass\s+permissions\s+on`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 102, Description: "Claude Code bypass status"},
		{Name: "claude_try_prompt", RegexStr: `❯\s*Try\s+"`, Agent: "claude", State: StateWaiting, Category: CategoryIdle, Priority: 102, Description: "Claude Code try prompt"},

		// Codex patterns
		{Name: "codex_prompt", RegexStr: `(?i)codex\s*>?\s*$`, Agent: "codex", State: StateWaiting, Category: CategoryIdle, Priority: 100, Description: "Codex prompt"},
		{Name: "codex_dollar", RegexStr: `\$\s*$`, Agent: "codex", State: StateWaiting, Category: CategoryIdle, Priority: 50, Description: "Codex dollar prompt"},

		// Gemini patterns
		{Name: "gemini_prompt", RegexStr: `(?i)gemini\s*>?\s*$`, Agent: "gemini", State: StateWaiting, Category: CategoryIdle, Priority: 100, Description: "Gemini prompt"},
		{Name: "gemini_triple_arrow", RegexStr: `>>>\s*$`, Agent: "gemini", State: StateWaiting, Category: CategoryIdle, Priority: 90, Description: "Gemini triple arrow prompt"},

		// Generic shell prompts (user/fallback)
		{Name: "shell_dollar", RegexStr: `\$\s*$`, Agent: "*", State: StateWaiting, Category: CategoryIdle, Priority: 20, Description: "Shell dollar prompt"},
		{Name: "shell_percent", RegexStr: `%\s*$`, Agent: "*", State: StateWaiting, Category: CategoryIdle, Priority: 20, Description: "Shell percent prompt"},
		{Name: "shell_hash", RegexStr: `#\s*$`, Agent: "*", State: StateWaiting, Category: CategoryIdle, Priority: 20, Description: "Shell hash prompt"},
		{Name: "generic_angle", RegexStr: `>\s*$`, Agent: "*", State: StateWaiting, Category: CategoryIdle, Priority: 10, Description: "Generic angle prompt"},

		// ==================
		// ERROR PATTERNS (something went wrong)
		// ==================
		// Rate limits
		{Name: "rate_limit_text", RegexStr: `(?i)rate\s+limit`, Agent: "*", State: StateError, Category: CategoryError, Priority: 200, Description: "Rate limit text"},
		{Name: "http_429", RegexStr: `\b429\b`, Agent: "*", State: StateError, Category: CategoryError, Priority: 200, Description: "HTTP 429 status"},
		{Name: "too_many_requests", RegexStr: `(?i)too\s+many\s+requests`, Agent: "*", State: StateError, Category: CategoryError, Priority: 200, Description: "Too many requests"},
		{Name: "quota_exceeded", RegexStr: `(?i)quota\s+exceeded`, Agent: "*", State: StateError, Category: CategoryError, Priority: 200, Description: "Quota exceeded"},

		// API errors
		{Name: "api_error", RegexStr: `(?i)(?:api\s+)?error:\s*\S`, Agent: "*", State: StateError, Category: CategoryError, Priority: 180, Description: "API error"},
		{Name: "exception", RegexStr: `(?i)exception:\s*\S`, Agent: "*", State: StateError, Category: CategoryError, Priority: 180, Description: "Exception"},
		{Name: "failed_text", RegexStr: `(?i)\bfailed\b.*(?:to|with|:|$)`, Agent: "*", State: StateError, Category: CategoryError, Priority: 150, Description: "Failed operation"},

		// Crashes
		{Name: "panic", RegexStr: `(?i)^panic:`, Agent: "*", State: StateError, Category: CategoryError, Priority: 250, Description: "Go panic"},
		{Name: "sigsegv", RegexStr: `SIGSEGV`, Agent: "*", State: StateError, Category: CategoryError, Priority: 250, Description: "Segmentation fault"},
		{Name: "sigkill", RegexStr: `(?i)(?:killed|SIGKILL)`, Agent: "*", State: StateError, Category: CategoryError, Priority: 250, Description: "Process killed"},
		{Name: "process_exited", RegexStr: `(?i)(?:process|agent)\s+(?:exited|terminated|crashed)`, Agent: "*", State: StateError, Category: CategoryError, Priority: 240, Description: "Process exited"},

		// Auth
		{Name: "unauthorized", RegexStr: `(?i)unauthorized`, Agent: "*", State: StateError, Category: CategoryError, Priority: 190, Description: "Unauthorized"},
		{Name: "invalid_key", RegexStr: `(?i)invalid.*(?:api\s*)?key`, Agent: "*", State: StateError, Category: CategoryError, Priority: 190, Description: "Invalid API key"},
		{Name: "auth_failed", RegexStr: `(?i)authentication\s+(?:failed|error)`, Agent: "*", State: StateError, Category: CategoryError, Priority: 190, Description: "Authentication failed"},

		// Connection errors
		{Name: "connection_refused", RegexStr: `(?i)connection\s+refused`, Agent: "*", State: StateError, Category: CategoryError, Priority: 170, Description: "Connection refused"},
		{Name: "timeout_error", RegexStr: `(?i)(?:connection|request)\s+timed?\s*out`, Agent: "*", State: StateError, Category: CategoryError, Priority: 170, Description: "Timeout error"},
		{Name: "network_error", RegexStr: `(?i)network\s+error`, Agent: "*", State: StateError, Category: CategoryError, Priority: 170, Description: "Network error"},

		// ==================
		// THINKING INDICATORS (processing)
		// ==================
		// Spinners (braille pattern characters)
		{Name: "braille_spinner", RegexStr: `[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 80, Description: "Braille spinner"},
		{Name: "dots_spinner", RegexStr: `\.{3,}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 70, Description: "Dots spinner"},

		// Text indicators
		{Name: "thinking_text", RegexStr: `(?i)thinking\.{0,3}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 85, Description: "Thinking text"},
		{Name: "processing_text", RegexStr: `(?i)processing\.{0,3}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 85, Description: "Processing text"},
		{Name: "analyzing_text", RegexStr: `(?i)analyzing\.{0,3}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 85, Description: "Analyzing text"},
		{Name: "extended_thinking", RegexStr: `(?i)(?:thinking\s+deeply|extended\s+thinking)`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 90, Description: "Extended thinking"},

		// Loading indicators
		{Name: "loading_text", RegexStr: `(?i)loading\.{0,3}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 75, Description: "Loading text"},
		{Name: "waiting_text", RegexStr: `(?i)(?:please\s+)?wait(?:ing)?\.{0,3}$`, Agent: "*", State: StateThinking, Category: CategoryThinking, Priority: 75, Description: "Waiting text"},

		// ==================
		// COMPLETION SIGNALS (task finished)
		// ==================
		{Name: "done_text", RegexStr: `(?i)(?:^|\s)done[.!]?\s*$`, Agent: "*", State: StateWaiting, Category: CategoryCompletion, Priority: 60, Description: "Done text"},
		{Name: "complete_text", RegexStr: `(?i)(?:^|\s)(?:completed?|finished)[.!]?\s*$`, Agent: "*", State: StateWaiting, Category: CategoryCompletion, Priority: 60, Description: "Complete/Finished text"},
		{Name: "checkmark", RegexStr: `[✓✔]\s*$`, Agent: "*", State: StateWaiting, Category: CategoryCompletion, Priority: 65, Description: "Checkmark symbol"},
		{Name: "summary_header", RegexStr: `(?i)^(?:summary|changes\s+made):`, Agent: "*", State: StateWaiting, Category: CategoryCompletion, Priority: 55, Description: "Summary header"},
	}
}

// Compile compiles all regex patterns in the library.
// Returns an error if any pattern fails to compile.
func (lib *PatternLibrary) Compile() error {
	lib.mu.Lock()
	defer lib.mu.Unlock()

	for i := range lib.Patterns {
		if lib.Patterns[i].Regex == nil && lib.Patterns[i].RegexStr != "" {
			compiled, err := regexp.Compile(lib.Patterns[i].RegexStr)
			if err != nil {
				return err
			}
			lib.Patterns[i].Regex = compiled
		}
	}

	// Sort by priority (higher first)
	sort.Slice(lib.Patterns, func(i, j int) bool {
		return lib.Patterns[i].Priority > lib.Patterns[j].Priority
	})

	lib.compiled = true
	return nil
}

// Match tests a string against all applicable patterns and returns matches.
// agentType filters patterns; use "*" or empty string for all patterns.
func (lib *PatternLibrary) Match(content string, agentType string) []PatternMatch {
	lib.mu.RLock()
	defer lib.mu.RUnlock()

	var matches []PatternMatch

	for _, p := range lib.Patterns {
		// Skip patterns for other agents (unless pattern is for all agents)
		if p.Agent != "*" && p.Agent != "" && agentType != "" && p.Agent != agentType {
			continue
		}

		if p.Regex != nil && p.Regex.MatchString(content) {
			matches = append(matches, PatternMatch{
				Pattern:  p.Name,
				State:    p.State,
				Category: p.Category,
				Priority: p.Priority,
			})
		}
	}

	return matches
}

// MatchFirst returns the first (highest priority) matching pattern.
// Returns nil if no patterns match.
func (lib *PatternLibrary) MatchFirst(content string, agentType string) *PatternMatch {
	matches := lib.Match(content, agentType)
	if len(matches) == 0 {
		return nil
	}
	return &matches[0]
}

// MatchByCategory returns matches filtered by category.
func (lib *PatternLibrary) MatchByCategory(content string, agentType string, category PatternCategory) []PatternMatch {
	lib.mu.RLock()
	defer lib.mu.RUnlock()

	var matches []PatternMatch

	for _, p := range lib.Patterns {
		if p.Category != category {
			continue
		}

		// Skip patterns for other agents
		if p.Agent != "*" && p.Agent != "" && agentType != "" && p.Agent != agentType {
			continue
		}

		if p.Regex != nil && p.Regex.MatchString(content) {
			matches = append(matches, PatternMatch{
				Pattern:  p.Name,
				State:    p.State,
				Category: p.Category,
				Priority: p.Priority,
			})
		}
	}

	return matches
}

// HasError checks if content contains any error pattern.
func (lib *PatternLibrary) HasError(content string, agentType string) bool {
	matches := lib.MatchByCategory(content, agentType, CategoryError)
	return len(matches) > 0
}

// HasIdlePrompt checks if content contains an idle prompt pattern.
func (lib *PatternLibrary) HasIdlePrompt(content string, agentType string) bool {
	matches := lib.MatchByCategory(content, agentType, CategoryIdle)
	return len(matches) > 0
}

// HasThinkingIndicator checks if content contains a thinking indicator.
func (lib *PatternLibrary) HasThinkingIndicator(content string, agentType string) bool {
	matches := lib.MatchByCategory(content, agentType, CategoryThinking)
	return len(matches) > 0
}

// HasCompletionSignal checks if content contains a completion signal.
func (lib *PatternLibrary) HasCompletionSignal(content string, agentType string) bool {
	matches := lib.MatchByCategory(content, agentType, CategoryCompletion)
	return len(matches) > 0
}

// PatternMatch represents a successful pattern match.
type PatternMatch struct {
	Pattern  string          `json:"pattern"`
	State    AgentState      `json:"state"`
	Category PatternCategory `json:"category"`
	Priority int             `json:"priority"`
}

// AddPattern adds a new pattern to the library.
// The pattern is compiled immediately if the library is already compiled.
func (lib *PatternLibrary) AddPattern(p Pattern) error {
	lib.mu.Lock()
	defer lib.mu.Unlock()

	// Compile regex if needed
	if p.Regex == nil && p.RegexStr != "" {
		compiled, err := regexp.Compile(p.RegexStr)
		if err != nil {
			return err
		}
		p.Regex = compiled
	}

	lib.Patterns = append(lib.Patterns, p)

	// Re-sort by priority
	sort.Slice(lib.Patterns, func(i, j int) bool {
		return lib.Patterns[i].Priority > lib.Patterns[j].Priority
	})

	return nil
}

// GetPatterns returns a copy of all patterns.
func (lib *PatternLibrary) GetPatterns() []Pattern {
	lib.mu.RLock()
	defer lib.mu.RUnlock()

	result := make([]Pattern, len(lib.Patterns))
	copy(result, lib.Patterns)
	return result
}

// GetPatternsByCategory returns patterns filtered by category.
func (lib *PatternLibrary) GetPatternsByCategory(category PatternCategory) []Pattern {
	lib.mu.RLock()
	defer lib.mu.RUnlock()

	var result []Pattern
	for _, p := range lib.Patterns {
		if p.Category == category {
			result = append(result, p)
		}
	}
	return result
}

// GetPatternsByAgent returns patterns applicable to a specific agent.
func (lib *PatternLibrary) GetPatternsByAgent(agentType string) []Pattern {
	lib.mu.RLock()
	defer lib.mu.RUnlock()

	var result []Pattern
	for _, p := range lib.Patterns {
		if p.Agent == "*" || p.Agent == "" || p.Agent == agentType {
			result = append(result, p)
		}
	}
	return result
}

// PatternCount returns the total number of patterns.
func (lib *PatternLibrary) PatternCount() int {
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	return len(lib.Patterns)
}

// DefaultLibrary is the shared default pattern library.
var DefaultLibrary = NewPatternLibrary()

// MatchPatterns matches content against the default library.
func MatchPatterns(content string, agentType string) []PatternMatch {
	return DefaultLibrary.Match(content, agentType)
}

// MatchFirstPattern returns the first matching pattern from the default library.
func MatchFirstPattern(content string, agentType string) *PatternMatch {
	return DefaultLibrary.MatchFirst(content, agentType)
}

// HasErrorPattern checks for error patterns in the default library.
func HasErrorPattern(content string, agentType string) bool {
	return DefaultLibrary.HasError(content, agentType)
}

// HasIdlePattern checks for idle patterns in the default library.
func HasIdlePattern(content string, agentType string) bool {
	return DefaultLibrary.HasIdlePrompt(content, agentType)
}

// HasThinkingPattern checks for thinking patterns in the default library.
func HasThinkingPattern(content string, agentType string) bool {
	return DefaultLibrary.HasThinkingIndicator(content, agentType)
}
