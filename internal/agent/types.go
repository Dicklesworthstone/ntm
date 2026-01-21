// Package agent provides types and utilities for parsing and managing AI agent state.
// This includes detecting agent status from terminal output, determining context usage,
// and recommending actions based on current state.
package agent

import "time"

// AgentType identifies which CLI agent is running in a pane.
type AgentType string

const (
	AgentTypeClaudeCode AgentType = "cc"      // Claude Code CLI
	AgentTypeCodex      AgentType = "cod"     // Codex CLI (OpenAI)
	AgentTypeGemini     AgentType = "gmi"     // Gemini CLI (Google)
	AgentTypeUnknown    AgentType = "unknown" // Unable to determine agent type
)

// String returns the agent type as a string.
func (t AgentType) String() string {
	return string(t)
}

// DisplayName returns a human-readable name for the agent type.
func (t AgentType) DisplayName() string {
	switch t {
	case AgentTypeClaudeCode:
		return "Claude Code"
	case AgentTypeCodex:
		return "Codex CLI"
	case AgentTypeGemini:
		return "Gemini CLI"
	default:
		return "Unknown"
	}
}

// IsValid returns true if this is a known agent type.
func (t AgentType) IsValid() bool {
	switch t {
	case AgentTypeClaudeCode, AgentTypeCodex, AgentTypeGemini:
		return true
	default:
		return false
	}
}

// AgentState represents the parsed state from agent terminal output.
// This is used to determine whether an agent is working, rate-limited,
// running low on context, or idle and ready for new work.
type AgentState struct {
	// Identity
	Type     AgentType `json:"agent_type"`
	ParsedAt time.Time `json:"parsed_at"`

	// Quantitative metrics (nil = unknown/not available)
	// Using pointers allows distinguishing "unknown" from "zero"
	ContextRemaining *float64 `json:"context_remaining,omitempty"` // 0-100 percentage remaining
	TokensUsed       *int64   `json:"tokens_used,omitempty"`       // Total tokens consumed
	MemoryMB         *float64 `json:"memory_mb,omitempty"`         // Memory usage (Gemini)

	// Qualitative state flags
	IsWorking     bool `json:"is_working"`     // Actively producing output (DO NOT INTERRUPT)
	IsRateLimited bool `json:"rate_limited"`   // Hit API usage limit (wait for reset)
	IsContextLow  bool `json:"context_low"`    // Below configured threshold
	IsIdle        bool `json:"is_idle"`        // Waiting for user input (safe to restart)
	IsInError     bool `json:"is_in_error"`    // Error state detected

	// Evidence for debugging and confidence calculation
	WorkIndicators  []string `json:"work_indicators,omitempty"`  // Patterns that indicate working
	LimitIndicators []string `json:"limit_indicators,omitempty"` // Patterns that indicate rate limiting
	RawSample       string   `json:"raw_sample,omitempty"`       // Last N chars for debugging

	// Confidence in this assessment (0.0-1.0)
	// Higher confidence means more pattern matches or explicit indicators
	Confidence float64 `json:"confidence"`
}

// Recommendation represents the recommended action based on agent state.
type Recommendation string

const (
	// RecommendDoNotInterrupt means the agent is actively working and should not be interrupted.
	// This is the highest priority state - never interrupt agents doing useful work.
	RecommendDoNotInterrupt Recommendation = "DO_NOT_INTERRUPT"

	// RecommendSafeToRestart means the agent is idle and can safely be restarted or given new work.
	RecommendSafeToRestart Recommendation = "SAFE_TO_RESTART"

	// RecommendContextLowContinue means the agent is working but running low on context.
	// Let it finish current work, then proactively restart before hitting limits.
	RecommendContextLowContinue Recommendation = "CONTEXT_LOW_CONTINUE"

	// RecommendRateLimitedWait means the agent hit an API usage limit.
	// Wait for the rate limit to reset before taking action.
	RecommendRateLimitedWait Recommendation = "RATE_LIMITED_WAIT"

	// RecommendErrorState means the agent is in an error state and may need intervention.
	RecommendErrorState Recommendation = "ERROR_STATE"

	// RecommendUnknown means we couldn't determine the agent state with confidence.
	RecommendUnknown Recommendation = "UNKNOWN"
)

// GetRecommendation derives the recommended action from the current state.
// Priority order is carefully chosen:
//  1. Rate limited -> wait (highest priority, nothing we can do)
//  2. Error state -> handle error
//  3. Working -> DO NOT INTERRUPT (critical user requirement)
//  4. Idle -> safe to restart
//  5. Unknown -> be cautious
func (s *AgentState) GetRecommendation() Recommendation {
	// Priority 1: Rate limited - must wait, nothing else matters
	if s.IsRateLimited {
		return RecommendRateLimitedWait
	}

	// Priority 2: Error state - needs attention
	if s.IsInError {
		return RecommendErrorState
	}

	// Priority 3: Working - NEVER interrupt useful work
	if s.IsWorking {
		if s.IsContextLow {
			// Working but running low - let finish, then restart
			return RecommendContextLowContinue
		}
		return RecommendDoNotInterrupt
	}

	// Priority 4: Idle - safe to take action
	if s.IsIdle {
		return RecommendSafeToRestart
	}

	// Default: couldn't determine state
	return RecommendUnknown
}

// String returns a human-readable representation of the recommendation.
func (r Recommendation) String() string {
	return string(r)
}

// IsActionable returns true if this recommendation suggests taking action
// (as opposed to waiting or being uncertain).
func (r Recommendation) IsActionable() bool {
	switch r {
	case RecommendSafeToRestart, RecommendErrorState:
		return true
	default:
		return false
	}
}

// RequiresWaiting returns true if this recommendation suggests waiting
// before taking action.
func (r Recommendation) RequiresWaiting() bool {
	switch r {
	case RecommendRateLimitedWait, RecommendContextLowContinue, RecommendDoNotInterrupt:
		return true
	default:
		return false
	}
}

// Parser defines the interface for extracting AgentState from terminal output.
// Implementations parse raw text captured from tmux panes and extract
// structured state information.
type Parser interface {
	// Parse analyzes terminal output and returns structured agent state.
	// The output parameter is raw text captured from a tmux pane.
	Parse(output string) (*AgentState, error)

	// DetectAgentType identifies which agent type produced the output.
	// This is useful when the agent type is not known in advance.
	DetectAgentType(output string) AgentType
}

// ParserConfig holds configuration for parser behavior.
type ParserConfig struct {
	// ContextLowThreshold is the percentage below which context is considered low.
	// Default: 20.0 (flag when below 20% remaining)
	ContextLowThreshold float64

	// SampleLength is the number of characters to keep in RawSample for debugging.
	// Default: 500
	SampleLength int
}

// DefaultParserConfig returns the default parser configuration.
func DefaultParserConfig() ParserConfig {
	return ParserConfig{
		ContextLowThreshold: 20.0,
		SampleLength:        500,
	}
}
