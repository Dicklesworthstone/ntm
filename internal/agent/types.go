// Package agent provides types and utilities for parsing and managing AI agent state.
// This includes detecting agent status from terminal output, determining context usage,
// and recommending actions based on current state.
package agent

import (
	"errors"
	"strings"
	"time"
)

// AgentType identifies which CLI agent is running in a pane.
type AgentType string

const (
	AgentTypeClaudeCode  AgentType = "cc"       // Claude Code CLI
	AgentTypeCodex       AgentType = "cod"      // Codex CLI (OpenAI)
	AgentTypeGemini      AgentType = "gmi"      // Gemini CLI (Google)
	AgentTypeAntigravity AgentType = "agy"      // Antigravity CLI (Google) — successor to the Gemini CLI
	AgentTypeGrok        AgentType = "grok"     // Grok Build CLI (xAI)
	AgentTypeOllama      AgentType = "ollama"   // Local Ollama CLI
	AgentTypeCursor      AgentType = "cursor"   // Cursor AI
	AgentTypeWindsurf    AgentType = "windsurf" // Windsurf IDE
	AgentTypeAider       AgentType = "aider"    // Aider CLI
	AgentTypeOpencode    AgentType = "oc"       // Opencode (https://opencode.ai) — see ntm#116
	AgentTypeOmp         AgentType = "omp"      // Oh My Pi (omp), the pi-agent fork (https://omp.sh)
	AgentTypePi          AgentType = "pi"       // Pi Agent CLI (distinct from omp)
	AgentTypeUser        AgentType = "user"     // User/Shell pane
	AgentTypeUnknown     AgentType = "unknown"  // Unable to determine agent type
)

// GrokPhaseOneCapabilityHint described the phase-one Grok Build integration
// boundary. Phase 2 (GH#251) implemented the interactive TUI protocol —
// composer-verified prompt delivery, submission verification, interrupt, and
// relaunch — against live captures of grok 1.0.5, so the phase-one refusals
// no longer fire. The constant remains for error-path callers that attach it
// as a hint; those paths are now unreachable for grok.
const GrokPhaseOneCapabilityHint = "Grok Build phase 2 (GH#251) implements automated prompt delivery, interrupt, and relaunch; update ntm if you see this refusal"

// PromptDeliveryCapabilityHint is the generic capability hint attached when an
// agent harness does not implement automated interactive prompt delivery.
const PromptDeliveryCapabilityHint = "Automated prompt delivery is not supported for this agent harness; inspect capability metadata before dispatching"

// GrokPromptDeliveryCapabilityHint is retained for legacy callers; the
// phase-one prompt-delivery refusal it annotated no longer fires (GH#251).
const GrokPromptDeliveryCapabilityHint = GrokPhaseOneCapabilityHint

// ErrAutomatedRelaunchNotImplemented is returned whenever an operation would
// automatically stop, restart, restore, or resume a Grok Build process.
var ErrAutomatedRelaunchNotImplemented = errors.New(GrokPhaseOneCapabilityHint)

// ErrAutomatedPromptDeliveryNotImplemented is returned before any automated
// input is written to a Grok Build pane.
var ErrAutomatedPromptDeliveryNotImplemented = errors.New(GrokPromptDeliveryCapabilityHint)

// String returns the agent type as a string.
func (t AgentType) String() string {
	return string(t)
}

// Canonical normalizes aliases and formatting drift into the canonical short form
// used throughout tmux metadata and parser dispatch.
func (t AgentType) Canonical() AgentType {
	trimmed := strings.TrimSpace(string(t))
	lower := strings.ToLower(trimmed)
	if canonical, ok := harnessAliasIndex[lower]; ok {
		return canonical
	}
	switch lower {
	case "user":
		return AgentTypeUser
	case "unknown":
		return AgentTypeUnknown
	default:
		return AgentType(trimmed)
	}
}

// DisplayName returns a human-readable name for the agent type.
func (t AgentType) DisplayName() string {
	if cap, ok := LookupHarness(t); ok {
		return cap.DisplayName
	}
	if t.Canonical() == AgentTypeUser {
		return "User"
	}
	return "Unknown"
}

// ProfileName returns the friendly display name for the agent type (short form).
func (t AgentType) ProfileName() string {
	if cap, ok := LookupHarness(t); ok {
		return cap.ProfileName
	}
	if t.Canonical() == AgentTypeUser {
		return "User"
	}
	// Capitalize first letter for unknown types
	s := string(t)
	if len(s) > 0 {
		r := []rune(s)
		return strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return s
}

// IsValid returns true if this is a known agent type.
func (t AgentType) IsValid() bool {
	if _, ok := LookupHarness(t); ok {
		return true
	}
	return t.Canonical() == AgentTypeUser
}

// ValidateAutomatedRelaunch fails closed for agent types whose interactive
// lifecycle protocol is not implemented. Callers must run this preflight for
// an entire target batch before mutating any pane or session.
//
// Grok Build passed this gate in phase 2 (GH#251): restart/relaunch reuses
// the configured launch template (`grok --always-approve …`), and the TUI's
// readiness/working markers are implemented in internal/agent and
// internal/robot pattern sets from live grok 1.0.5 captures.
func (t AgentType) ValidateAutomatedRelaunch() error {
	return nil
}

// ValidateAutomatedPromptDelivery fails closed for agent types whose
// interactive input protocol is not implemented. Callers must preflight the
// complete target batch before writing input to any pane.
//
// Grok Build passed this gate in phase 2 (GH#251): delivery is composer-gated
// (tmux.ComposerReadyForDelivery knows grok's bordered "│ ❯" composer) and
// submission-verified (tmux.VerifyGrokSubmissionContext), both proven against
// the live TUI.
func (t AgentType) ValidateAutomatedPromptDelivery() error {
	return nil
}

// NeedsDoubleEnter returns true if the agent type typically requires a double-Enter
// sequence to recognize input (often due to internal buffering or TUI quirks).
func (t AgentType) NeedsDoubleEnter() bool {
	if cap, ok := LookupHarness(t); ok {
		return cap.NeedsDoubleEnter
	}
	return false
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
	ContextRemaining   *float64 `json:"context_remaining,omitempty"`    // 0-100 percentage remaining
	TokensUsed         *int64   `json:"tokens_used,omitempty"`          // Total tokens consumed
	MemoryMB           *float64 `json:"memory_mb,omitempty"`            // Memory usage (Gemini)
	BackgroundJobCount int      `json:"background_job_count,omitempty"` // Jobs reported by the live provider UI

	// Qualitative state flags
	IsWorking     bool `json:"is_working"`   // Actively producing output (DO NOT INTERRUPT)
	IsRateLimited bool `json:"rate_limited"` // Hit API usage limit (wait for reset)
	IsContextLow  bool `json:"context_low"`  // Below configured threshold
	IsIdle        bool `json:"is_idle"`      // Waiting for user input (safe to restart)
	IsInError     bool `json:"is_in_error"`  // Error state detected

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

// Parser defines the interface for extracting AgentState from terminal output.
// Implementations parse raw text captured from tmux panes and extract
// structured state information.
type Parser interface {
	// Parse analyzes terminal output and returns structured agent state.
	// The output parameter is raw text captured from a tmux pane.
	Parse(output string) (*AgentState, error)

	// ParseWithHint analyzes terminal output with a known agent type hint.
	// This skips type detection if the hint is valid, improving performance and accuracy.
	ParseWithHint(output string, hint AgentType) (*AgentState, error)

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
