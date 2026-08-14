// Package robot provides machine-readable output for AI agents.
// is_working.go implements the --robot-is-working command for detecting agent work state.
package robot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/process"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
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
	Session       string   // Session name (required)
	Panes         []int    // Legacy bare pane indices (empty = all non-control panes)
	PaneSelectors []string // N, W.P, or %N selectors; takes precedence over Panes
	LinesCaptured int      // Number of lines to capture (default: 100)
	Verbose       bool     // Include raw sample in output

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
	PanesRequested     []string `json:"panes_requested"`
	SelectorsRequested []string `json:"selectors_requested,omitempty"`
	LinesCaptured      int      `json:"lines_captured"`
}

// WorkIndicators contains the patterns that matched for each category.
type WorkIndicators struct {
	Work  []string `json:"work"`
	Limit []string `json:"limit"`
}

// PaneWorkStatus contains the work state for a single pane.
type PaneWorkStatus struct {
	AgentType        string         `json:"agent_type"`
	IsWorking        bool           `json:"is_working"`
	IsIdle           bool           `json:"is_idle"`
	IsRateLimited    bool           `json:"is_rate_limited"`
	IsContextLow     bool           `json:"is_context_low"`
	ContextRemaining *float64       `json:"context_remaining,omitempty"`
	Confidence       float64        `json:"confidence"`
	Indicators       WorkIndicators `json:"indicators"`
	// IndicatorBasis identifies the authoritative signal that determined the
	// current working/idle verdict. It is intentionally separate from
	// Indicators, which is a diagnostic list of every matching parser pattern.
	IndicatorBasis        string `json:"indicator_basis"`
	Recommendation        string `json:"recommendation"`
	RecommendationReason  string `json:"recommendation_reason"`
	RawSample             string `json:"raw_sample,omitempty"` // Only with --verbose
	ObservationState      string `json:"observation_state"`
	ObservationFreshness  string `json:"observation_freshness"`
	ObservationObservedAt string `json:"observation_observed_at"`
	ObservationError      string `json:"observation_error,omitempty"`
	SafeToDispatch        bool   `json:"safe_to_dispatch"`

	// PaneStartedAt / AgentUptimeSeconds expose the pane's creation time
	// (tmux #{pane_start_time}) so age-based replacement policies (context
	// saturation, OC-011's "older than N days") work from robot output
	// instead of raw tmux format strings (ntm-qvpm). Absent when tmux did
	// not report a start time.
	PaneStartedAt      string `json:"pane_started_at,omitempty"`
	AgentUptimeSeconds int64  `json:"agent_uptime_seconds,omitempty"`

	// Pane process liveness (ntm-0g0b): silent CLI exits back to zsh are
	// invisible to buffered tails, so orchestrators previously audited
	// `tmux list-panes -F '#{pane_current_command} #{pane_pid}'` before
	// every state-changing action. AgentCLIDead is true when an agent-typed
	// pane's foreground process is a bare shell — anything sent to it lands
	// in zsh, not an agent.
	PanePID            int    `json:"pane_pid,omitempty"`
	PaneCurrentCommand string `json:"pane_current_command,omitempty"`
	AgentCLIDead       bool   `json:"agent_cli_dead,omitempty"`

	// UnsubmittedInput / QueuedMessages surface the composer state
	// (bd-v8dqd): a pane can be idle, blocking the swarm, and holding
	// prompts unread all at once, and no working/idle verdict shows it.
	// UnsubmittedInput: real text is sitting in the input box unsubmitted.
	// QueuedMessages: the TUI reports queued not-yet-processed messages.
	UnsubmittedInput bool `json:"unsubmitted_input,omitempty"`
	QueuedMessages   bool `json:"queued_messages,omitempty"`

	// SemanticProgress is the OPTIONAL, additive ground-truth signal (#199),
	// present only under --semantic and omitted entirely otherwise. It is
	// advisory: it never changes IsWorking/IsIdle/Recommendation above.
	SemanticProgress *SemanticProgress `json:"semantic_progress,omitempty"`
}

// IsWorkingSummary provides aggregate statistics across all panes.
type IsWorkingSummary struct {
	TotalPanes       int                 `json:"total_panes"`
	WorkingCount     int                 `json:"working_count"`
	IdleCount        int                 `json:"idle_count"`
	RateLimitedCount int                 `json:"rate_limited_count"`
	ContextLowCount  int                 `json:"context_low_count"`
	ErrorCount       int                 `json:"error_count"`
	ByRecommendation map[string][]string `json:"by_recommendation"`
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
func PrintIsWorking(ctx context.Context, opts IsWorkingOptions) error {
	output, err := GetIsWorking(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot is-working failed")
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

// applyLiveBusyOverride reconciles the parser's scrollback-wide flags with the
// current live window. When work is visibly in flight, prompt chrome is not an
// idle signal and an error match elsewhere in the capture is historical rather
// than the pane's current state. Rate-limit and context-low flags remain intact
// and therefore retain their normal recommendation precedence.
func applyLiveBusyOverride(content string, state *agent.AgentState, paneWidth int) bool {
	if state == nil || !isAIAgentLiveBusyWidth(content, string(state.Type), paneWidth) {
		return false
	}
	canonicalType := state.Type.Canonical()
	if state.IsInError && canonicalType != agent.AgentTypeClaudeCode {
		return false
	}
	state.IsWorking = true
	state.IsIdle = false
	if canonicalType == agent.AgentTypeClaudeCode {
		state.IsInError = false
	}
	return true
}

// workIndicatorBasis names the signal that determined the final verdict after
// the parser, live-window reconciliation, and canonical observation safety gate
// have all run. The value is deliberately a stable token so callers can audit
// a classification without reverse-engineering the individual pattern lists.
func workIndicatorBasis(
	state *agent.AgentState,
	liveBusy bool,
	parsed PaneWorkStatus,
	final PaneWorkStatus,
	observation statuspkg.PaneObservation,
) string {
	if final.IsRateLimited {
		return "rate_limit_indicator"
	}
	if final.Recommendation == string(agent.RecommendErrorState) {
		return "error_indicator"
	}
	if liveBusy {
		switch state.Type.Canonical() {
		case agent.AgentTypeClaudeCode:
			return "claude_live_spinner"
		case agent.AgentTypeCodex:
			return "codex_live_working_indicator"
		default:
			return "live_window_thinking"
		}
	}
	if final.IsWorking && !parsed.IsWorking && observation.Current.Status.State == statuspkg.StateWorking {
		return "canonical_working_observation"
	}
	if final.IsIdle && !parsed.IsIdle && observation.Current.Status.State == statuspkg.StateIdle {
		return "canonical_idle_observation"
	}
	if final.IsIdle {
		switch state.Type.Canonical() {
		case agent.AgentTypeClaudeCode:
			return "claude_finished_turn_prompt"
		case agent.AgentTypeCodex:
			return "codex_composer_placeholder"
		default:
			return "idle_prompt"
		}
	}
	if final.IsWorking {
		return "parser_work_indicator"
	}
	return "insufficient_evidence"
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

// ParsePaneSelectorsArg validates the shared N, W.P, and %N selector grammar.
// Resolution against a concrete session topology happens inside the command so
// missing selectors produce a typed PANE_NOT_FOUND response.
func ParsePaneSelectorsArg(panesArg string) ([]string, error) {
	if panesArg == "" || strings.EqualFold(strings.TrimSpace(panesArg), "all") {
		return []string{}, nil
	}

	parts := strings.Split(panesArg, ",")
	selectors := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for index, part := range parts {
		selector := strings.TrimSpace(part)
		if selector == "" {
			return nil, fmt.Errorf("invalid pane selector list %q: empty selector at position %d", panesArg, index+1)
		}
		parsed, err := tmux.ParsePaneSelector(selector)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[parsed.Raw]; exists {
			continue
		}
		seen[parsed.Raw] = struct{}{}
		selectors = append(selectors, parsed.Raw)
	}
	return selectors, nil
}

// GetIsWorking returns the work state for specified panes in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetIsWorking(ctx context.Context, opts IsWorkingOptions) (*IsWorkingOutput, error) {
	if opts.LinesCaptured <= 0 {
		opts.LinesCaptured = DefaultIsWorkingOptions().LinesCaptured
	}
	output := &IsWorkingOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Query: IsWorkingQuery{
			PanesRequested:     []string{},
			SelectorsRequested: append([]string(nil), opts.PaneSelectors...),
			LinesCaptured:      opts.LinesCaptured,
		},
		Panes: make(map[string]PaneWorkStatus),
		Summary: IsWorkingSummary{
			ByRecommendation: make(map[string][]string),
		},
	}
	if ctx == nil {
		output.RobotResponse = NewErrorResponse(errors.New("is-working context is required"), ErrCodeInternalError, "Retry the command")
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		return output, nil
	}

	// Validate session exists
	exists, err := tmux.SessionExistsContext(ctx, opts.Session)
	if err != nil {
		code := ErrCodeInternalError
		hint := "Check tmux session state"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = ErrCodeTimeout
			hint = "Retry after the cancellation or timeout condition clears"
		}
		output.RobotResponse = NewErrorResponse(err, code, hint)
		return output, nil
	}
	if !exists {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	observer := newRobotSessionObserver(opts.LinesCaptured)
	observation, err := observer.Observe(ctx, opts.Session)
	if err != nil {
		code := ErrCodeInternalError
		hint := "Check tmux session state"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = ErrCodeTimeout
			hint = "Retry after the cancellation or timeout condition clears"
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to observe panes: %w", err),
			code,
			hint,
		)
		return output, nil
	}
	allPanes := observationPanes(observation)
	observationsByID := observationPaneMap(observation)

	// Determine which panes to check. This is window-aware (#170): a
	// window-per-agent layout (N windows each with a single pane sharing
	// window-local index 0) must NOT collapse to one entry, and the legacy
	// "skip the global minimum index" control-pane heuristic excluded every
	// pane in that layout (they all share the min), reporting total_panes:0.
	selected, err := resolveIsWorkingPanes(opts.Session, allPanes, opts.PaneSelectors, opts.Panes)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			paneSelectorRobotErrorCode(err),
			"Use --robot-status or --robot-is-working without --panes to inspect canonical pane addresses",
		)
		return output, nil
	}

	// Whether the session spans multiple windows. When it does, a bare pane
	// index is no longer a unique key, so the response map is keyed by the
	// canonical "window.pane" address. Single-window sessions keep bare-index
	// keys for backward compatibility with existing consumers.
	multiWindow := sessionSpansMultipleWindows(allPanes)

	// Create parser
	parser := agent.NewParser()

	// Echo canonical resolved targets. Window-local pane indices are not
	// unique identities in multi-window sessions.
	requestedTargets := make([]string, 0, len(selected))

	// Process each selected pane.
	for _, sel := range selected {
		if err := ctx.Err(); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Inspect completed pane observations before retrying")
			return output, nil
		}
		paneKey := isWorkingPaneKey(sel, multiWindow)
		requestedTargets = append(requestedTargets, paneKey)

		if !sel.found {
			output.Panes[paneKey] = PaneWorkStatus{
				AgentType:            string(agent.AgentTypeUnknown),
				IndicatorBasis:       "pane_not_found",
				Recommendation:       string(agent.RecommendErrorState),
				RecommendationReason: fmt.Sprintf("Pane %s not found in session", paneKey),
				Confidence:           0.0,
				Indicators:           WorkIndicators{Work: []string{}, Limit: []string{}},
			}
			output.Summary.ErrorCount++
			continue
		}

		paneHint := sel.hint
		paneObservation, found := observationsByID[sel.id]
		if !found || paneObservation.Current.Freshness != statuspkg.FreshnessFresh || paneObservation.Current.Error != "" {
			workStatus := paneWorkStatusFromObservation(paneObservation)
			if !found {
				workStatus.ObservationState = string(statuspkg.StateUnknown)
				workStatus.ObservationFreshness = string(statuspkg.FreshnessUnavailable)
				workStatus.ObservationError = "pane missing from canonical observation"
			}
			workStatus.Recommendation = string(agent.RecommendErrorState)
			workStatus.RecommendationReason = "Current pane output is unavailable; inspect the pane before acting"
			workStatus.Confidence = 0
			workStatus.Indicators = WorkIndicators{Work: []string{}, Limit: []string{}}
			workStatus.IndicatorBasis = "observation_unavailable"
			if workStatus.AgentType == "" {
				workStatus.AgentType = string(agent.AgentTypeUnknown)
			}
			output.Panes[paneKey] = workStatus
			output.Summary.ErrorCount++
			continue
		}
		content := paneObservation.RawOutput

		if paneObservation.Current.Status.State == statuspkg.StateUnknown {
			// The richer parser below may still recognize provider-specific UI
			// chrome, but preserve the canonical state alongside that verdict.
			paneObservation.Current.Confidence = 0.25
		}

		// Parse the already-captured output. Hint with the tmux-tracked agent
		// type so we do not misclassify mixed historical scrollback.
		state, err := parser.ParseWithHint(content, paneHint)
		if err != nil {
			workStatus := paneWorkStatusFromObservation(paneObservation)
			workStatus.AgentType = string(agent.AgentTypeUnknown)
			workStatus.Recommendation = string(agent.RecommendUnknown)
			workStatus.RecommendationReason = fmt.Sprintf("Parse failed: %v", err)
			workStatus.ObservationError = err.Error()
			workStatus.Confidence = 0
			workStatus.Indicators = WorkIndicators{Work: []string{}, Limit: []string{}}
			workStatus.IndicatorBasis = "parse_error"
			output.Panes[paneKey] = workStatus
			output.Summary.ErrorCount++
			continue
		}

		// Build the pane status.
		workStatus := paneWorkStatusFromObservation(paneObservation)
		workStatus.AgentType = string(state.Type)
		workStatus.IsWorking = state.IsWorking
		workStatus.IsIdle = state.IsIdle
		workStatus.IsRateLimited = state.IsRateLimited
		workStatus.IsContextLow = state.IsContextLow
		workStatus.ContextRemaining = state.ContextRemaining
		workStatus.Confidence = state.Confidence
		workStatus.Indicators = WorkIndicators{
			Work:  state.WorkIndicators,
			Limit: state.LimitIndicators,
		}
		workStatus.Recommendation = string(state.GetRecommendation())
		workStatus.RecommendationReason = getRecommendationReason(state)

		// Composer visibility (bd-v8dqd): report unsubmitted input and
		// queued messages so a pane that is idle-but-blocking is legible.
		composer := tmux.InspectComposer(content, tmux.AgentType(state.Type))
		workStatus.UnsubmittedInput = composer.HoldsText
		workStatus.QueuedMessages = composer.QueuedMessages
		status := workStatus

		// Live-window THINKING override (#133). The legacy parser can mark a
		// pane idle when its prompt-pattern view does not see in-flight work
		// driven by another orchestrator, but `--robot-activity` would still
		// classify the same scrollback as THINKING from the trailing live
		// window. Without this override, --robot-is-working and downstream
		// --robot-agent-health (which reads our IsWorking) recommend
		// SAFE_TO_RESTART for a Codex pane that is actually mid-tool-call.
		// The canonical SessionObserver applies the same live-window gate
		// before dispatch; this brings the restart/health
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
		// Re-derive the recommendation from the corrected current state. A live
		// window supersedes stale prompt/error matches, while rate-limit and
		// context-low signals retain their normal precedence. ParseWithHint
		// returns a fresh *AgentState, so this mutation is local to this pane.
		liveBusy := applyLiveBusyOverride(content, state, paneObservation.Metadata.Width)
		if liveBusy {
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
		parsedStatus := status
		applyCanonicalWorkSafety(&status, paneObservation, liveBusy)
		status.IndicatorBasis = workIndicatorBasis(state, liveBusy, parsedStatus, status, paneObservation)

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
			repoDir := paneCurrentPathForTarget(ctx, sel.target)
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
			output.Summary.ByRecommendation[rec] = []string{}
		}
		output.Summary.ByRecommendation[rec] = append(output.Summary.ByRecommendation[rec], paneKey)
	}

	output.Summary.TotalPanes = len(selected)
	output.Query.PanesRequested = requestedTargets

	return output, nil
}

func newRobotSessionObserver(lines int) *statuspkg.SessionObserver {
	detectorConfig := statuspkg.DefaultConfig()
	if lines > 0 {
		detectorConfig.ScanLines = lines
	}
	detector := statuspkg.NewDetectorWithConfig(detectorConfig)
	observerConfig := statuspkg.DefaultSessionObserverConfig(detectorConfig)
	observerConfig.CaptureLines = lines
	return statuspkg.NewSessionObserverWithDependencies(detector, observerConfig, statuspkg.SessionObserverDependencies{})
}

func observationPanes(observation statuspkg.SessionObservation) []tmux.Pane {
	panes := make([]tmux.Pane, 0, len(observation.Panes))
	for _, pane := range observation.Panes {
		panes = append(panes, pane.Metadata)
	}
	return panes
}

func observationPaneMap(observation statuspkg.SessionObservation) map[string]statuspkg.PaneObservation {
	panes := make(map[string]statuspkg.PaneObservation, len(observation.Panes))
	for _, pane := range observation.Panes {
		panes[pane.Pane.StableKey()] = pane
	}
	return panes
}

func paneWorkStatusFromObservation(observation statuspkg.PaneObservation) PaneWorkStatus {
	result := PaneWorkStatus{
		AgentType:             observation.AgentType,
		IndicatorBasis:        "insufficient_evidence",
		ObservationState:      string(observation.Current.Status.State),
		ObservationFreshness:  string(observation.Current.Freshness),
		ObservationObservedAt: FormatTimestamp(observation.Current.ObservedAt),
		ObservationError:      observation.Current.Error,
		SafeToDispatch:        observation.SafeToDispatch(),
	}
	// Pane age comes from the shell PID's process start time: tmux has no
	// per-pane creation-time format variable, and the shell PID is replaced
	// on respawn — exactly the "current incarnation" semantics age-based
	// replacement policies need (ntm-qvpm). Best-effort: a vanished PID
	// simply omits the fields.
	if pid := observation.Metadata.PID; pid > 0 {
		if startedAt, err := process.StartTime(pid); err == nil {
			result.PaneStartedAt = FormatTimestamp(startedAt)
			if uptime := int64(time.Since(startedAt).Seconds()); uptime > 0 {
				result.AgentUptimeSeconds = uptime
			}
		}
	}
	result.PanePID = observation.Metadata.PID
	result.PaneCurrentCommand = observation.Metadata.Command
	result.AgentCLIDead = observation.Metadata.AgentCLIDead()
	return result
}

// applyCanonicalWorkSafety reconciles the parser verdict with the canonical
// observation. liveBusy reports whether the live-window THINKING override
// (#133) fired for this pane; it pins the working verdict so the idle arm can
// never talk a mid-tool-call pane back down to idle.
func applyCanonicalWorkSafety(workStatus *PaneWorkStatus, observation statuspkg.PaneObservation, liveBusy bool) {
	if workStatus == nil {
		return
	}
	switch observation.Current.Status.State {
	case statuspkg.StateWorking:
		workStatus.IsWorking = true
		workStatus.IsIdle = false
		workStatus.Recommendation = string(agent.RecommendDoNotInterrupt)
		workStatus.RecommendationReason = "Canonical live observation reports active work"
	case statuspkg.StateIdle:
		// #234: the canonical idle verdict has to be able to correct a stale
		// parser "working" reading the same way the working arm corrects a
		// stale idle one — otherwise a Codex pane sitting at its composer is
		// reported working forever and orchestrators never feed it.
		//
		// Fail closed everywhere it matters: only an actionable observation
		// counts (SafeToDispatch encodes fresh + high-confidence + idle, so the
		// detector's weak "looks idle" heuristics cannot flip a pane), the live
		// -window override wins, and rate-limit / error verdicts keep their
		// higher precedence so a walled or broken pane is never advertised as
		// free.
		if liveBusy || !observation.SafeToDispatch() {
			return
		}
		if workStatus.IsRateLimited || workStatus.Recommendation == string(agent.RecommendErrorState) {
			return
		}
		if workStatus.IsIdle && !workStatus.IsWorking {
			return
		}
		workStatus.IsWorking = false
		workStatus.IsIdle = true
		workStatus.Recommendation = string(agent.RecommendSafeToRestart)
		workStatus.RecommendationReason = "Canonical live observation reports the pane idle at its prompt"
	case statuspkg.StateUnknown:
		workStatus.IsWorking = false
		workStatus.IsIdle = false
		workStatus.Recommendation = string(agent.RecommendUnknown)
		workStatus.RecommendationReason = "Canonical live observation could not determine current state"
	}
}

func paneSelectorRobotErrorCode(err error) string {
	var selectorErr *tmux.PaneSelectorError
	if errors.As(err, &selectorErr) && selectorErr.Kind == tmux.PaneSelectorNotFound {
		return ErrCodePaneNotFound
	}
	return ErrCodeInvalidFlag
}

func resolveIsWorkingPanes(session string, allPanes []tmux.Pane, selectors []string, legacy []int) ([]selectedPane, error) {
	if len(selectors) == 0 && len(legacy) > 0 {
		selectors = make([]string, 0, len(legacy))
		for _, pane := range legacy {
			selectors = append(selectors, strconv.Itoa(pane))
		}
	}
	if len(selectors) == 0 {
		return selectIsWorkingPanes(session, allPanes, nil), nil
	}
	resolved, err := tmux.ResolvePaneSelectors(allPanes, selectors, false)
	if err != nil {
		return nil, err
	}
	selected := make([]selectedPane, 0, len(resolved))
	for _, pane := range resolved {
		selected = append(selected, buildSelectedPane(session, pane))
	}
	return selected, nil
}

// selectedPane bundles a chosen pane with the metadata GetIsWorking needs:
// its tmux target (window.pane address), the agent-type hint from tmux, and a
// found flag (false when the caller requested a pane index that doesn't exist).
type selectedPane struct {
	id          string
	WindowIndex int
	Index       int
	target      string
	hint        agent.AgentType
	found       bool
}

func buildSelectedPane(session string, pane tmux.Pane) selectedPane {
	return selectedPane{
		id:          pane.Ref().StableKey(),
		WindowIndex: pane.WindowIndex,
		Index:       pane.Index,
		target:      fmt.Sprintf("%s:%d.%d", session, pane.WindowIndex, pane.Index),
		hint:        agent.AgentType(paneAgentType(pane)).Canonical(),
		found:       true,
	}
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
		return buildSelectedPane(session, p)
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
func paneCurrentPathForTarget(ctx context.Context, target string) string {
	output, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-t", target, "-p", "#{pane_current_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// Ensure consistent timestamp formatting for all robot output
func init() {
	_ = time.RFC3339 // Reference to ensure import
}
