// Package robot provides machine-readable output for AI agents.
// is_working.go implements the --robot-is-working command for detecting agent work state.
package robot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Robot Is-Working Command (bd-16ptx)
// =============================================================================
//
// The is-working command is the DIRECT ANSWER to:
//
//   "NEVER interrupt agents doing useful work!!!"
//
// Before ANY restart action, a controller agent must be able to ask:
// "Is this agent actively working?" This command provides that answer
// with structured, actionable output.

// IsWorkingOptions configures the is-working command.
type IsWorkingOptions struct {
	Session       string // Session name (required)
	Panes         []int  // Pane indices to check (empty = all non-control panes)
	LinesCaptured int    // Number of lines to capture (default: 100)
	Verbose       bool   // Include raw sample in output
}

// DefaultIsWorkingOptions returns sensible defaults.
func DefaultIsWorkingOptions() IsWorkingOptions {
	return IsWorkingOptions{
		LinesCaptured: 100,
		Verbose:       false,
	}
}

// IsWorkingQuery contains the query parameters for reproducibility.
type IsWorkingQuery struct {
	PanesRequested []int `json:"panes_requested"`
	LinesCaptured  int   `json:"lines_captured"`
}

// WorkIndicators contains the patterns that matched for each category.
type WorkIndicators struct {
	Work  []string `json:"work"`
	Limit []string `json:"limit"`
}

// PaneWorkStatus contains the work state for a single pane.
type PaneWorkStatus struct {
	AgentType            string         `json:"agent_type"`
	IsWorking            bool           `json:"is_working"`
	IsIdle               bool           `json:"is_idle"`
	IsRateLimited        bool           `json:"is_rate_limited"`
	IsContextLow         bool           `json:"is_context_low"`
	ContextRemaining     *float64       `json:"context_remaining,omitempty"`
	Confidence           float64        `json:"confidence"`
	Indicators           WorkIndicators `json:"indicators"`
	Recommendation       string         `json:"recommendation"`
	RecommendationReason string         `json:"recommendation_reason"`
	RawSample            string         `json:"raw_sample,omitempty"` // Only with --verbose
}

// IsWorkingSummary provides aggregate statistics across all panes.
type IsWorkingSummary struct {
	TotalPanes       int              `json:"total_panes"`
	WorkingCount     int              `json:"working_count"`
	IdleCount        int              `json:"idle_count"`
	RateLimitedCount int              `json:"rate_limited_count"`
	ContextLowCount  int              `json:"context_low_count"`
	ErrorCount       int              `json:"error_count"`
	ByRecommendation map[string][]int `json:"by_recommendation"`
}

// IsWorkingOutput is the response for --robot-is-working.
type IsWorkingOutput struct {
	RobotResponse
	Session string                    `json:"session"`
	Query   IsWorkingQuery            `json:"query"`
	Panes   map[string]PaneWorkStatus `json:"panes"`
	Summary IsWorkingSummary          `json:"summary"`
}

// PrintIsWorking outputs the work state for specified panes in a session.
// This is a thin wrapper around GetIsWorking() for CLI output.
func PrintIsWorking(opts IsWorkingOptions) error {
	output, err := GetIsWorking(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// getRecommendationReason provides human-readable explanation for each recommendation.
func getRecommendationReason(state *agent.AgentState) string {
	rec := state.GetRecommendation()
	switch rec {
	case agent.RecommendDoNotInterrupt:
		return "Agent is actively producing output"
	case agent.RecommendSafeToRestart:
		return "Agent is idle"
	case agent.RecommendContextLowContinue:
		if state.ContextRemaining != nil {
			return fmt.Sprintf("Working but low context (%.0f%%)", *state.ContextRemaining)
		}
		return "Working but low context"
	case agent.RecommendRateLimitedWait:
		return "Agent hit rate limit"
	case agent.RecommendErrorState:
		return "Agent in error state"
	default:
		return "Could not determine agent state"
	}
}

// ParsePanesArg parses the --panes argument.
// Accepts "all", empty string, or comma-separated integers.
func ParsePanesArg(panesArg string) ([]int, error) {
	if panesArg == "" || strings.ToLower(panesArg) == "all" {
		return []int{}, nil // Empty means "all non-control panes"
	}

	parts := strings.Split(panesArg, ",")
	panes := make([]int, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid pane index '%s': %w", part, err)
		}
		if idx < 0 {
			return nil, fmt.Errorf("pane index must be non-negative, got %d", idx)
		}
		panes = append(panes, idx)
	}

	return panes, nil
}

// GetIsWorking returns the work state for specified panes in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetIsWorking(opts IsWorkingOptions) (*IsWorkingOutput, error) {
	output := &IsWorkingOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Query: IsWorkingQuery{
			PanesRequested: opts.Panes,
			LinesCaptured:  opts.LinesCaptured,
		},
		Panes: make(map[string]PaneWorkStatus),
		Summary: IsWorkingSummary{
			ByRecommendation: make(map[string][]int),
		},
	}

	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	// Get all panes in session
	allPanes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	// Determine which panes to check
	panesToCheck := opts.Panes
	if len(panesToCheck) == 0 {
		// Default: all panes except pane 0 (control pane)
		for _, p := range allPanes {
			if p.Index > 0 { // Skip control pane
				panesToCheck = append(panesToCheck, p.Index)
			}
		}
	}

	// Validate requested panes exist
	paneExists := make(map[int]bool)
	for _, p := range allPanes {
		paneExists[p.Index] = true
	}

	// Create parser
	parser := agent.NewParser()

	// Process each pane
	for _, paneIdx := range panesToCheck {
		paneKey := strconv.Itoa(paneIdx)

		if !paneExists[paneIdx] {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				Recommendation:       string(agent.RecommendErrorState),
				RecommendationReason: fmt.Sprintf("Pane %d not found in session", paneIdx),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			output.Summary.ErrorCount++
			continue
		}

		// Capture pane output
		target, err := tmux.FormatPaneTarget(opts.Session, paneIdx)
		if err != nil {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				Recommendation:       string(agent.RecommendErrorState),
				RecommendationReason: fmt.Sprintf("Failed to get window index: %v", err),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			output.Summary.ErrorCount++
			continue
		}
		content, err := tmux.CapturePaneOutput(target, opts.LinesCaptured)
		if err != nil {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				Recommendation:       string(agent.RecommendErrorState),
				RecommendationReason: fmt.Sprintf("Failed to capture output: %v", err),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			output.Summary.ErrorCount++
			continue
		}

		// Parse the output
		state, err := parser.Parse(content)
		if err != nil {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				Recommendation:       string(agent.RecommendUnknown),
				RecommendationReason: fmt.Sprintf("Parse failed: %v", err),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			continue
		}

		// Build the pane status
		status := PaneWorkStatus{
			AgentType:        string(state.Type),
			IsWorking:        state.IsWorking,
			IsIdle:           state.IsIdle,
			IsRateLimited:    state.IsRateLimited,
			IsContextLow:     state.IsContextLow,
			ContextRemaining: state.ContextRemaining,
			Confidence:       state.Confidence,
			Indicators: WorkIndicators{
				Work:  state.WorkIndicators,
				Limit: state.LimitIndicators,
			},
			Recommendation:       string(state.GetRecommendation()),
			RecommendationReason: getRecommendationReason(state),
		}

		// Ensure indicators are never nil
		if status.Indicators.Work == nil {
			status.Indicators.Work = []string{}
		}
		if status.Indicators.Limit == nil {
			status.Indicators.Limit = []string{}
		}

		if opts.Verbose {
			status.RawSample = state.RawSample
		}

		output.Panes[paneKey] = status

		// Update summary
		if state.IsWorking {
			output.Summary.WorkingCount++
		}
		if state.IsIdle {
			output.Summary.IdleCount++
		}
		if state.IsRateLimited {
			output.Summary.RateLimitedCount++
		}
		if state.IsContextLow {
			output.Summary.ContextLowCount++
		}

		rec := string(state.GetRecommendation())
		if output.Summary.ByRecommendation[rec] == nil {
			output.Summary.ByRecommendation[rec] = []int{}
		}
		output.Summary.ByRecommendation[rec] = append(output.Summary.ByRecommendation[rec], paneIdx)
	}

	output.Summary.TotalPanes = len(panesToCheck)
	output.Query.PanesRequested = panesToCheck

	return output, nil
}

// Ensure consistent timestamp formatting for all robot output
func init() {
	_ = time.RFC3339 // Reference to ensure import
}
