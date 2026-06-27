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

	// Semantic, when true, enables the OPTIONAL ground-truth semantic-progress
	// signal (#199): per pane, attach SemanticProgress derived from
	// token-attributed git commits (and best-effort bead claims). It is OFF by
	// default; when off, GetIsWorking makes ZERO new git/br/tmux subprocess
	// calls and the output is byte-identical to the pre-feature behavior.
	Semantic bool
	// SemanticWindow is the look-back window for the semantic signal. Zero falls
	// back to the conservative default (defaultSemanticWindow). Only consulted
	// when Semantic is true.
	SemanticWindow time.Duration
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

	// SemanticProgress is the OPTIONAL, additive ground-truth signal (#199),
	// present only under --semantic and omitted entirely otherwise. It is
	// advisory: it never changes IsWorking/IsIdle/Recommendation above.
	SemanticProgress *SemanticProgress `json:"semantic_progress,omitempty"`
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

	// Determine which panes to check. This is window-aware (#170): a
	// window-per-agent layout (N windows each with a single pane sharing
	// window-local index 0) must NOT collapse to one entry, and the legacy
	// "skip the global minimum index" control-pane heuristic excluded every
	// pane in that layout (they all share the min), reporting total_panes:0.
	selected := selectIsWorkingPanes(opts.Session, allPanes, opts.Panes)

	// Whether the session spans multiple windows. When it does, a bare pane
	// index is no longer a unique key, so the response map is keyed by the
	// canonical "window.pane" address. Single-window sessions keep bare-index
	// keys for backward compatibility with existing consumers.
	multiWindow := sessionSpansMultipleWindows(allPanes)

	// Create parser
	parser := agent.NewParser()

	// Track requested pane indices for the query echo (backward-compatible
	// shape: []int of pane indices). Targets and metadata come from the
	// selected pane objects directly.
	requestedIndices := make([]int, 0, len(selected))

	// Process each selected pane.
	for _, sel := range selected {
		requestedIndices = append(requestedIndices, sel.Index)
		paneKey := isWorkingPaneKey(sel, multiWindow)

		if !sel.found {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				Recommendation:       string(agent.RecommendErrorState),
				RecommendationReason: fmt.Sprintf("Pane %s not found in session", paneKey),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			output.Summary.ErrorCount++
			continue
		}

		// Capture pane output. The target uses the window.pane address so it
		// resolves correctly regardless of pane-base-index / multi-window.
		paneIdx := sel.Index
		target := sel.target
		paneHint := sel.hint
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

		// Parse the output. Hint with the tmux-tracked agent type so we don't
		// misclassify Codex panes as Claude (or vice versa) when both agents'
		// chrome appears in the scrollback.
		state, err := parser.ParseWithHint(content, paneHint)
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

		// Live-window THINKING override (#133). The legacy parser can mark a
		// pane idle when its prompt-pattern view does not see in-flight work
		// driven by another orchestrator, but `--robot-activity` would still
		// classify the same scrollback as THINKING from the trailing live
		// window. Without this override, --robot-is-working and downstream
		// --robot-agent-health (which reads our IsWorking) recommend
		// SAFE_TO_RESTART for a Codex pane that is actually mid-tool-call.
		// `internal/cli/assign.go::determineAgentState` already applies the
		// same override before dispatch; this brings the restart/health
		// surfaces into agreement with --robot-activity / IsLiveBusy.
		//
		// Skip the override on user/unknown panes: the wildcard
		// CategoryThinking patterns in the library (braille spinner,
		// "loading…", "processing…", trailing dots) would otherwise falsely
		// flag normal shell output as agent work, and these panes have no
		// AI agent for --robot-is-working to reason about. Use state.Type
		// (parser's view, hint-confirmed) rather than the raw tmux hint so
		// content-detected agents on hint-less panes still get the override.
		//
		// Re-derive the recommendation from the corrected state so we keep
		// higher-priority signals (RateLimitedWait, ErrorState,
		// ContextLowContinue) when they apply, and only fall through to
		// DoNotInterrupt when the only thing the legacy parser was wrong
		// about was IsWorking/IsIdle. Mutating `state.IsWorking` and
		// `state.IsIdle` is safe: ParseWithHint returns a freshly-allocated
		// *AgentState per call, and only state.RawSample is read after this
		// point — that field is not touched by the override.
		isAIAgentPane := state.Type != "" &&
			state.Type != agent.AgentTypeUser &&
			state.Type != agent.AgentTypeUnknown
		if isAIAgentPane && IsLiveBusy(content, string(state.Type)) {
			state.IsWorking = true
			state.IsIdle = false
			status.IsWorking = true
			status.IsIdle = false
			status.Recommendation = string(state.GetRecommendation())
			status.RecommendationReason = getRecommendationReason(state)
			// Defensive copy: append might extend the underlying array of
			// state.WorkIndicators in place if it has spare capacity. Even
			// though the parser is not known to reuse slices today, copying
			// keeps the override marker stable regardless of parser internals.
			work := make([]string, len(status.Indicators.Work), len(status.Indicators.Work)+1)
			copy(work, status.Indicators.Work)
			status.Indicators.Work = append(work, "live_window_thinking")
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

		// OPTIONAL semantic-progress signal (#199). Computed ONLY under
		// --semantic, so the default poll path makes no extra git/br/tmux calls.
		// It is strictly additive and advisory: PaneSemanticProgress receives
		// status.IsWorking (the velocity verdict, post-override) purely as an
		// input to gate the advisory wedge string, and returns a value that has
		// no is_working field — so it is structurally impossible for this signal
		// to flip IsWorking/IsIdle or the recommendation. We attach it AFTER the
		// summary-affecting fields are finalized for exactly that reason.
		if opts.Semantic {
			repoDir := paneCurrentPathForTarget(sel.target)
			addr := PaneAddr{Session: opts.Session, Window: sel.WindowIndex, Pane: sel.Index}
			status.SemanticProgress = PaneSemanticProgress(addr, repoDir, opts.SemanticWindow, status.IsWorking, time.Now())
		}

		output.Panes[paneKey] = status

		// Update summary using overridden values so the live-window override
		// is reflected in fleet-level counts and the recommendation buckets.
		if status.IsWorking {
			output.Summary.WorkingCount++
		}
		if status.IsIdle {
			output.Summary.IdleCount++
		}
		if status.IsRateLimited {
			output.Summary.RateLimitedCount++
		}
		if status.IsContextLow {
			output.Summary.ContextLowCount++
		}

		rec := status.Recommendation
		if output.Summary.ByRecommendation[rec] == nil {
			output.Summary.ByRecommendation[rec] = []int{}
		}
		output.Summary.ByRecommendation[rec] = append(output.Summary.ByRecommendation[rec], paneIdx)
	}

	output.Summary.TotalPanes = len(selected)
	output.Query.PanesRequested = requestedIndices

	return output, nil
}

// selectedPane bundles a chosen pane with the metadata GetIsWorking needs:
// its tmux target (window.pane address), the agent-type hint from tmux, and a
// found flag (false when the caller requested a pane index that doesn't exist).
type selectedPane struct {
	WindowIndex int
	Index       int
	target      string
	hint        agent.AgentType
	found       bool
}

// selectIsWorkingPanes resolves the set of panes to inspect for
// --robot-is-working in a window-aware manner.
//
// When `requested` is non-empty, each requested bare index is matched against
// the session. In a single-window session this is unambiguous. In a
// multi-window session a bare index may match panes in several windows; we
// include every match so the caller is never silently narrowed to one pane
// (the dual of the adopt #170 fix). A requested index with no match yields a
// not-found marker entry so the error surfaces in the response.
//
// When `requested` is empty (the "all non-control panes" default), selection is
// grouped by window: within each window the lowest-index pane is treated as the
// control pane and skipped IF the window holds more than one pane. A
// window-per-agent layout (one pane per window) therefore includes every pane
// instead of excluding them all under the old global-minimum heuristic.
func selectIsWorkingPanes(session string, allPanes []tmux.Pane, requested []int) []selectedPane {
	build := func(p tmux.Pane) selectedPane {
		return selectedPane{
			WindowIndex: p.WindowIndex,
			Index:       p.Index,
			target:      fmt.Sprintf("%s:%d.%d", session, p.WindowIndex, p.Index),
			hint:        agent.AgentType(paneAgentType(p)).Canonical(),
			found:       true,
		}
	}

	if len(requested) > 0 {
		// Topology-aware bare-index resolution (#172), consistent with the
		// send/interrupt/restart-pane surfaces: on a multi-window session a bare
		// --panes index selects a whole WINDOW (so --panes=2 hits the agent in
		// window 2 of a window-per-agent layout instead of matching nothing),
		// while a single-window session keeps the window-local pane index.
		multiWindow := sessionSpansMultipleWindows(allPanes)
		key := func(p tmux.Pane) int {
			if multiWindow {
				return p.WindowIndex
			}
			return p.Index
		}
		byIndex := make(map[int][]tmux.Pane)
		for _, p := range allPanes {
			byIndex[key(p)] = append(byIndex[key(p)], p)
		}
		var out []selectedPane
		for _, idx := range requested {
			matches := byIndex[idx]
			if len(matches) == 0 {
				out = append(out, selectedPane{Index: idx, found: false})
				continue
			}
			for _, p := range matches {
				out = append(out, build(p))
			}
		}
		return out
	}

	// Default: all non-control panes, window-aware.
	panesByWindow := make(map[int][]tmux.Pane)
	var windowOrder []int
	for _, p := range allPanes {
		if _, seen := panesByWindow[p.WindowIndex]; !seen {
			windowOrder = append(windowOrder, p.WindowIndex)
		}
		panesByWindow[p.WindowIndex] = append(panesByWindow[p.WindowIndex], p)
	}

	var out []selectedPane
	for _, win := range windowOrder {
		wp := panesByWindow[win]
		if len(wp) <= 1 {
			// Single pane in this window: it is an agent pane, not a control
			// pane. Include it (window-per-agent layout).
			for _, p := range wp {
				out = append(out, build(p))
			}
			continue
		}
		// Multiple panes: skip this window's lowest-index pane (control pane).
		minIdx := wp[0].Index
		for _, p := range wp[1:] {
			if p.Index < minIdx {
				minIdx = p.Index
			}
		}
		for _, p := range wp {
			if p.Index != minIdx {
				out = append(out, build(p))
			}
		}
	}
	return out
}

// sessionSpansMultipleWindows reports whether the session's panes live in more
// than one window. When true, bare pane indices are not unique keys.
func sessionSpansMultipleWindows(allPanes []tmux.Pane) bool {
	if len(allPanes) == 0 {
		return false
	}
	first := allPanes[0].WindowIndex
	for _, p := range allPanes[1:] {
		if p.WindowIndex != first {
			return true
		}
	}
	return false
}

// isWorkingPaneKey returns the response-map key for a pane. Single-window
// sessions use the bare pane index (backward compatible). Multi-window sessions
// use the canonical "window.pane" address so window-per-agent layouts don't
// collapse multiple panes onto one key.
func isWorkingPaneKey(sel selectedPane, multiWindow bool) string {
	if !multiWindow {
		return strconv.Itoa(sel.Index)
	}
	return fmt.Sprintf("%d.%d", sel.WindowIndex, sel.Index)
}

// paneCurrentPathForTarget resolves a pane's working directory via tmux's
// pane_current_path. Used only by the --semantic path to locate the repo for
// token-attributed reads; returns "" on any failure (degrades to source none).
func paneCurrentPathForTarget(target string) string {
	output, err := tmux.DefaultClient.Run("display-message", "-t", target, "-p", "#{pane_current_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// Ensure consistent timestamp formatting for all robot output
func init() {
	_ = time.RFC3339 // Reference to ensure import
}
