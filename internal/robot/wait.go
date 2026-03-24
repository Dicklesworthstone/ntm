// Package robot provides machine-readable output for AI agents and automation.
// wait.go implements the --robot-wait command for waiting on agent states.
package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WaitOptions configures the robot wait operation.
type WaitOptions struct {
	Session           string
	Condition         string // idle, complete, generating, healthy, attention, action_required, etc.
	Timeout           time.Duration
	PollInterval      time.Duration
	PaneIndices       []int  // Empty = all panes
	AgentType         string // Empty = all types
	WaitForAny        bool   // If true, wait for ANY; otherwise wait for ALL
	ExitOnError       bool   // If true, exit immediately on ERROR state
	CountN            int    // With WaitForAny, wait for at least N agents (default 1)
	RequireTransition bool   // If true, agents must leave and return to target state
	SinceCursor       int64  // Attention-based conditions only fire for events after this cursor
	Profile           string // Filter profile for attention-based conditions (operator, debug, minimal, alerts)
}

// WaitResponse is the JSON output for --robot-wait.
type WaitResponse struct {
	RobotResponse
	Session       string          `json:"session"`
	Condition     string          `json:"condition"`
	WaitedSeconds float64         `json:"waited_seconds"`
	Agents        []WaitAgentInfo `json:"agents,omitempty"`
	AgentsPending []string        `json:"agents_pending,omitempty"`

	// WakePayload contains details about what triggered the wakeup (for attention conditions).
	WakePayload *WaitWakePayload `json:"wake_payload,omitempty"`

	// CursorInfo provides cursor handoff for attention-based conditions.
	CursorInfo *WaitCursorInfo `json:"cursor_info,omitempty"`
}

// WaitWakePayload describes what triggered the wait to complete (for attention conditions).
type WaitWakePayload struct {
	// MatchedCondition is the specific condition that triggered the wake.
	MatchedCondition string `json:"matched_condition"`

	// TriggerEvent is the attention event that caused the wake (if applicable).
	TriggerEvent *AttentionEvent `json:"trigger_event,omitempty"`

	// TriggerCount is the count of events matching the condition (for aggregates).
	TriggerCount int `json:"trigger_count,omitempty"`

	// Details provides condition-specific context.
	Details map[string]any `json:"details,omitempty"`
}

// WaitCursorInfo provides cursor handoff information for follow-up commands.
type WaitCursorInfo struct {
	// ObservedCursor is the cursor value when the condition was detected.
	ObservedCursor int64 `json:"observed_cursor"`

	// NextCursor is the cursor to use for follow-up commands.
	NextCursor int64 `json:"next_cursor"`

	// OldestCursor is the oldest cursor still available in the feed.
	OldestCursor int64 `json:"oldest_cursor,omitempty"`
}

// WaitAgentInfo describes an agent's state when the wait completed or timed out.
type WaitAgentInfo struct {
	Pane      string `json:"pane"`
	State     string `json:"state"`
	MetAt     string `json:"met_at,omitempty"` // RFC3339 timestamp
	AgentType string `json:"agent_type,omitempty"`
}

// Wait condition constants - pane-based conditions
const (
	WaitConditionIdle        = "idle"
	WaitConditionComplete    = "complete"
	WaitConditionGenerating  = "generating"
	WaitConditionHealthy     = "healthy"
	WaitConditionStalled     = "stalled"
	WaitConditionRateLimited = "rate_limited"
)

// Wait condition constants - attention-based conditions (require --attention-cursor)
const (
	WaitConditionAttention           = "attention"
	WaitConditionActionRequired      = "action_required"
	WaitConditionMailPending         = "mail_pending"
	WaitConditionMailAckRequired     = "mail_ack_required"
	WaitConditionContextHot          = "context_hot"
	WaitConditionReservationConflict = "reservation_conflict"
	WaitConditionFileConflict        = "file_conflict"
	WaitConditionSessionChanged      = "session_changed"
	WaitConditionPaneChanged         = "pane_changed"
)

// CompleteIdleThreshold is the time without activity to consider "complete".
const CompleteIdleThreshold = 5 * time.Second

// GetWait executes the wait operation and returns the response data.
// Returns the response and exit code (0=success, 1=timeout, 2=error, 3=agent error).
func GetWait(opts WaitOptions) (*WaitResponse, int) {
	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		return &WaitResponse{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:   opts.Session,
			Condition: opts.Condition,
		}, 2
	}

	// Validate condition — check for unsupported conditions with specific guidance
	if !isValidWaitCondition(opts.Condition) {
		hint := "Valid conditions: idle, complete, generating, healthy, stalled, rate_limited, " +
			"attention, action_required, mail_pending, mail_ack_required, context_hot, " +
			"reservation_conflict, file_conflict, session_changed, pane_changed"
		errMsg := fmt.Sprintf("invalid condition '%s'", opts.Condition)

		// Provide specific guidance for known unsupported conditions
		if isUnsupportedWaitCondition(opts.Condition) {
			hint = fmt.Sprintf("Condition '%s' is deliberately unsupported. "+
				"Use --robot-capabilities to see rationale.", opts.Condition)
			errMsg = fmt.Sprintf("unsupported condition '%s'", opts.Condition)
		}

		return &WaitResponse{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("%s", errMsg),
				ErrCodeInvalidFlag,
				hint,
			),
			Session:   opts.Session,
			Condition: opts.Condition,
		}, 2
	}

	// Parse conditions once and split pane-vs-attention semantics. Mixed waits
	// are ANDed across both surfaces.
	conditions := strings.Split(opts.Condition, ",")
	paneConditions, attentionConditions := splitWaitConditions(conditions)
	hasAttention := len(attentionConditions) > 0

	// Set default count for --any mode
	if opts.WaitForAny && opts.CountN <= 0 {
		opts.CountN = 1
	}

	// Start waiting
	startTime := time.Now()
	deadline := startTime.Add(opts.Timeout)

	// Create activity monitor
	monitor := NewActivityMonitor(nil)

	// Track state transitions when RequireTransition is enabled
	// Key: paneID, Value: true if agent was in target state at start AND has since left it
	sawTransition := make(map[string]bool)
	initiallyInTarget := make(map[string]bool)
	firstPoll := true
	var lastPending []string
	var lastAttentionResult *AttentionConditionResult

	for {
		// Check timeout
		if time.Now().After(deadline) {
			elapsed := time.Since(startTime)
			resp := &WaitResponse{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("timeout after %v", opts.Timeout),
					ErrCodeTimeout,
					"Try increasing --timeout (or deprecated --wait-timeout) or check agent status with --robot-activity",
				),
				Session:       opts.Session,
				Condition:     opts.Condition,
				WaitedSeconds: elapsed.Seconds(),
				AgentsPending: append([]string(nil), lastPending...),
			}
			if hasAttention {
				if lastAttentionResult == nil {
					lastAttentionResult = newAttentionConditionResult("", opts.SinceCursor, opts.SinceCursor, 0)
				}
				resp.CursorInfo = buildWaitCursorInfo(lastAttentionResult)
			}
			return resp, 1
		}

		var activities []*AgentActivity
		needPaneState := len(paneConditions) > 0 || opts.ExitOnError || opts.RequireTransition
		if needPaneState {
			panes, err := tmux.GetPanes(opts.Session)
			if err != nil {
				return &WaitResponse{
					RobotResponse: NewErrorResponse(
						fmt.Errorf("failed to list panes: %w", err),
						ErrCodeInternalError,
						"",
					),
					Session:   opts.Session,
					Condition: opts.Condition,
				}, 2
			}

			filteredPanes := filterWaitPanes(panes, opts)
			if len(filteredPanes) == 0 {
				return &WaitResponse{
					RobotResponse: NewErrorResponse(
						fmt.Errorf("no panes match the filter criteria"),
						ErrCodePaneNotFound,
						"Check --panes (or deprecated --wait-panes) and --type (or deprecated --wait-type) filters",
					),
					Session:   opts.Session,
					Condition: opts.Condition,
				}, 2
			}

			for _, pane := range filteredPanes {
				classifier := monitor.GetOrCreate(pane.ID)
				// Use detectAgentType which maps short forms (cc->claude, cod->codex, gmi->gemini)
				if at := detectAgentType(pane.Title); at != "" && at != "unknown" {
					classifier.SetAgentType(at)
				}
				activity, err := classifier.Classify()
				if err != nil {
					// Pane may have disappeared, continue.
					continue
				}
				activities = append(activities, activity)
			}

			if opts.RequireTransition && len(paneConditions) > 0 {
				for _, a := range activities {
					inTarget := meetsAllWaitConditions(a, paneConditions)
					if firstPoll {
						initiallyInTarget[a.PaneID] = inTarget
					} else if initiallyInTarget[a.PaneID] && !inTarget {
						sawTransition[a.PaneID] = true
					}
				}
			}

			if opts.ExitOnError {
				for _, a := range activities {
					if a.State == StateError {
						elapsed := time.Since(startTime)
						return &WaitResponse{
							RobotResponse: NewErrorResponse(
								fmt.Errorf("agent error detected in pane '%s'", a.PaneID),
								"AGENT_ERROR",
								"Check agent output with --robot-tail",
							),
							Session:       opts.Session,
							Condition:     opts.Condition,
							WaitedSeconds: elapsed.Seconds(),
							Agents: []WaitAgentInfo{{
								Pane:      a.PaneID,
								State:     string(a.State),
								AgentType: a.AgentType,
							}},
						}, 3
					}
				}
			}
		}
		firstPoll = false

		paneMet := len(paneConditions) == 0
		var matching []WaitAgentInfo
		if len(paneConditions) > 0 {
			var met bool
			met, matching, lastPending = checkWaitConditionMetWithTransition(activities, opts, paneConditions, initiallyInTarget, sawTransition)
			paneMet = met
		} else {
			lastPending = nil
		}

		attentionMet := !hasAttention
		if hasAttention {
			lastAttentionResult = checkAttentionConditions(attentionConditions, opts.SinceCursor, opts.Session, opts.Profile)
			if lastAttentionResult != nil && lastAttentionResult.CursorExpired != nil {
				cursorErr := lastAttentionResult.CursorExpired
				details := cursorErr.ToDetails()
				return &WaitResponse{
					RobotResponse: NewErrorResponse(
						cursorErr,
						ErrCodeCursorExpired,
						details.ResyncCommand,
					),
					Session:   opts.Session,
					Condition: opts.Condition,
					CursorInfo: &WaitCursorInfo{
						ObservedCursor: opts.SinceCursor,
						NextCursor:     details.EarliestCursor,
						OldestCursor:   details.EarliestCursor,
					},
				}, 2
			}
			attentionMet = lastAttentionResult != nil && lastAttentionResult.Met
		}

		if attentionMet && paneMet {
			elapsed := time.Since(startTime)
			return &WaitResponse{
				RobotResponse: NewRobotResponse(true),
				Session:       opts.Session,
				Condition:     opts.Condition,
				WaitedSeconds: elapsed.Seconds(),
				Agents:        matching,
				WakePayload:   buildWaitWakePayload(lastAttentionResult),
				CursorInfo:    buildWaitCursorInfo(lastAttentionResult),
			}, 0
		}

		// Sleep and poll again
		time.Sleep(opts.PollInterval)
	}
}

// PrintWait executes the wait operation and outputs JSON.
// Returns exit code: 0 = success, 1 = timeout, 2 = error, 3 = agent error
func PrintWait(opts WaitOptions) int {
	resp, exitCode := GetWait(opts)
	outputJSON(resp)
	return exitCode
}

// isUnsupportedWaitCondition checks if the condition is a known unsupported
// condition that was deliberately considered and rejected. This provides
// better error messages than a generic "invalid condition" for conditions
// that operators might reasonably try.
func isUnsupportedWaitCondition(condition string) bool {
	parts := strings.Split(condition, ",")
	for _, part := range parts {
		p := strings.TrimSpace(part)
		for _, uc := range UnsupportedConditions() {
			if p == uc.Name {
				return true
			}
		}
	}
	return false
}

// isValidWaitCondition checks if the condition string is valid.
func isValidWaitCondition(condition string) bool {
	// Handle composed conditions (comma-separated)
	parts := strings.Split(condition, ",")
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if !isSingleValidWaitCondition(p) {
			return false
		}
	}
	return len(parts) > 0
}

// isSingleValidWaitCondition checks if a single condition name is valid.
func isSingleValidWaitCondition(condition string) bool {
	switch condition {
	// Pane-based conditions
	case WaitConditionIdle, WaitConditionComplete, WaitConditionGenerating, WaitConditionHealthy,
		WaitConditionStalled, WaitConditionRateLimited:
		return true
	// Attention-based conditions
	case WaitConditionAttention, WaitConditionActionRequired, WaitConditionMailPending,
		WaitConditionMailAckRequired, WaitConditionContextHot, WaitConditionReservationConflict,
		WaitConditionFileConflict, WaitConditionSessionChanged, WaitConditionPaneChanged:
		return true
	default:
		return false
	}
}

// isAttentionBasedCondition returns true if the condition requires the attention feed.
func isAttentionBasedCondition(condition string) bool {
	switch condition {
	case WaitConditionAttention, WaitConditionActionRequired, WaitConditionMailPending,
		WaitConditionMailAckRequired, WaitConditionContextHot, WaitConditionReservationConflict,
		WaitConditionFileConflict, WaitConditionSessionChanged, WaitConditionPaneChanged:
		return true
	default:
		return false
	}
}

// hasAttentionBasedConditions returns true if any of the conditions require the attention feed.
func hasAttentionBasedConditions(conditions []string) bool {
	for _, c := range conditions {
		if isAttentionBasedCondition(strings.TrimSpace(c)) {
			return true
		}
	}
	return false
}

func splitWaitConditions(conditions []string) ([]string, []string) {
	paneConditions := make([]string, 0, len(conditions))
	attentionConditions := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		trimmed := strings.TrimSpace(condition)
		if trimmed == "" {
			continue
		}
		if isAttentionBasedCondition(trimmed) {
			attentionConditions = append(attentionConditions, trimmed)
			continue
		}
		paneConditions = append(paneConditions, trimmed)
	}
	return paneConditions, attentionConditions
}

// filterWaitPanes filters panes based on wait options.
func filterWaitPanes(panes []tmux.Pane, opts WaitOptions) []tmux.Pane {
	var result []tmux.Pane

	// Build pane index set for quick lookup
	paneIndexSet := make(map[int]bool)
	for _, idx := range opts.PaneIndices {
		paneIndexSet[idx] = true
	}

	for _, pane := range panes {
		// Skip panes without a recognized agent type (user pane, unknown)
		// Use detectAgentType which maps short forms (cc->claude, cod->codex, gmi->gemini)
		agentType := detectAgentType(pane.Title)
		if agentType == "" || agentType == "unknown" {
			continue
		}

		// Filter by specific pane indices
		if len(opts.PaneIndices) > 0 && !paneIndexSet[pane.Index] {
			continue
		}

		// Filter by agent type
		if opts.AgentType != "" {
			if !strings.EqualFold(agentType, opts.AgentType) {
				continue
			}
		}

		result = append(result, pane)
	}

	return result
}

// checkWaitConditionMet checks if the wait condition is satisfied.
// Returns: met (bool), matching agents, pending agents
func checkWaitConditionMet(activities []*AgentActivity, opts WaitOptions) (bool, []WaitAgentInfo, []string) {
	if len(activities) == 0 {
		return false, nil, nil
	}

	// Parse composed conditions
	conditions := strings.Split(opts.Condition, ",")

	var matchingAgents []WaitAgentInfo
	var pendingAgents []string

	now := time.Now()

	for _, activity := range activities {
		if meetsAllWaitConditions(activity, conditions) {
			matchingAgents = append(matchingAgents, WaitAgentInfo{
				Pane:      activity.PaneID,
				State:     string(activity.State),
				MetAt:     FormatTimestamp(now),
				AgentType: activity.AgentType,
			})
		} else {
			pendingAgents = append(pendingAgents, activity.PaneID)
		}
	}

	// Determine if condition is met based on --any vs ALL
	if opts.WaitForAny {
		// With --any, need at least CountN agents matching
		return len(matchingAgents) >= opts.CountN, matchingAgents, pendingAgents
	}

	// Default: ALL agents must match (no pending)
	return len(pendingAgents) == 0 && len(matchingAgents) > 0, matchingAgents, pendingAgents
}

// checkWaitConditionMetWithTransition is like checkWaitConditionMet but handles
// RequireTransition mode. When RequireTransition is true, agents that were in
// the target state initially must leave and return to that state before being
// considered as matching.
func checkWaitConditionMetWithTransition(
	activities []*AgentActivity,
	opts WaitOptions,
	conditions []string,
	initiallyInTarget map[string]bool,
	sawTransition map[string]bool,
) (bool, []WaitAgentInfo, []string) {
	if len(activities) == 0 {
		return false, nil, nil
	}

	var matchingAgents []WaitAgentInfo
	var pendingAgents []string

	now := time.Now()

	for _, activity := range activities {
		meetsCondition := meetsAllWaitConditions(activity, conditions)

		// For RequireTransition mode, check if agent needs to have transitioned
		if opts.RequireTransition && initiallyInTarget[activity.PaneID] {
			// Agent was in target state at start - only count as matching if it
			// has since left the target state and come back
			if !sawTransition[activity.PaneID] {
				// Agent hasn't left target state yet - still pending
				pendingAgents = append(pendingAgents, activity.PaneID)
				continue
			}
			// Agent has transitioned - now check if it's back in target state
			if !meetsCondition {
				pendingAgents = append(pendingAgents, activity.PaneID)
				continue
			}
		} else if !meetsCondition {
			// Normal case or agent wasn't initially in target state
			pendingAgents = append(pendingAgents, activity.PaneID)
			continue
		}

		// Agent meets condition (and transition requirement if applicable)
		matchingAgents = append(matchingAgents, WaitAgentInfo{
			Pane:      activity.PaneID,
			State:     string(activity.State),
			MetAt:     FormatTimestamp(now),
			AgentType: activity.AgentType,
		})
	}

	// Determine if condition is met based on --any vs ALL
	if opts.WaitForAny {
		return len(matchingAgents) >= opts.CountN, matchingAgents, pendingAgents
	}

	return len(pendingAgents) == 0 && len(matchingAgents) > 0, matchingAgents, pendingAgents
}

// meetsAllWaitConditions checks if an activity meets all specified conditions.
func meetsAllWaitConditions(activity *AgentActivity, conditions []string) bool {
	for _, cond := range conditions {
		c := strings.TrimSpace(cond)
		if !meetsSingleWaitCondition(activity, c) {
			return false
		}
	}
	return true
}

// meetsSingleWaitCondition checks if an activity meets a single pane-based condition.
// Attention-based conditions are handled separately via checkAttentionCondition.
func meetsSingleWaitCondition(activity *AgentActivity, condition string) bool {
	switch condition {
	case WaitConditionIdle:
		return activity.State == StateWaiting

	case WaitConditionComplete:
		// Must be waiting AND no recent output
		if activity.State != StateWaiting {
			return false
		}
		// Check last output time - must be older than threshold
		if activity.LastOutput.IsZero() {
			return true // No output recorded = complete
		}
		return time.Since(activity.LastOutput) >= CompleteIdleThreshold

	case WaitConditionGenerating:
		return activity.State == StateGenerating

	case WaitConditionHealthy:
		// Not ERROR and not STALLED
		return activity.State != StateError && activity.State != StateStalled

	case WaitConditionStalled:
		return activity.State == StateStalled

	case WaitConditionRateLimited:
		// Rate limited is detected via specific patterns in the agent output
		// For now, we map it to the stalled state with rate-limit indicators
		// This is a placeholder until we have proper rate-limit detection
		return activity.State == StateError && activity.RateLimited

	default:
		// Attention-based conditions don't apply to individual pane activity
		return false
	}
}

// =============================================================================
// Attention-Based Condition Checking
// =============================================================================

// AttentionConditionResult holds the result of checking attention-based conditions.
type AttentionConditionResult struct {
	Met            bool
	Condition      string
	TriggerEvent   *AttentionEvent
	TriggerCount   int
	Details        map[string]any
	ObservedCursor int64
	NextCursor     int64
	OldestCursor   int64
	CursorExpired  *CursorExpiredError
}

type singleAttentionConditionMatch struct {
	Condition    string
	TriggerEvent *AttentionEvent
	TriggerCount int
}

func newAttentionConditionResult(condition string, observedCursor, nextCursor, oldestCursor int64) *AttentionConditionResult {
	return &AttentionConditionResult{
		Condition:      condition,
		ObservedCursor: observedCursor,
		NextCursor:     nextCursor,
		OldestCursor:   oldestCursor,
		Details:        make(map[string]any),
	}
}

// checkAttentionConditions checks if all requested attention-based conditions
// are met. Multiple attention conditions are ANDed together.
func checkAttentionConditions(conditions []string, sinceCursor int64, session, profile string) *AttentionConditionResult {
	feed := GetAttentionFeed()
	if feed == nil {
		return nil
	}

	stats := feed.Stats()
	result := newAttentionConditionResult("", sinceCursor, stats.NewestCursor, stats.OldestCursor)

	// Replay events since the cursor.
	events, newestCursor, err := feed.Replay(sinceCursor, 1000)
	result.NextCursor = newestCursor
	if err != nil {
		if cursorErr, ok := err.(*CursorExpiredError); ok {
			result.CursorExpired = cursorErr
			result.OldestCursor = cursorErr.EarliestCursor
			result.NextCursor = cursorErr.EarliestCursor
		}
		return result
	}

	result.Details["raw_event_count"] = len(events)

	// Filter by session if specified
	if session != "" {
		filtered := make([]AttentionEvent, 0)
		for _, ev := range events {
			if ev.Session == session {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	events = filterAttentionEventsByProfile(events, profile)
	events = filterVisibleAttentionEvents(events)

	result.Details["scanned_event_count"] = len(events)
	if session != "" {
		result.Details["session"] = session
	}
	if profile != "" {
		result.Details["profile"] = profile
	}

	matchedConditions := make([]string, 0, len(conditions))
	matchCounts := make(map[string]int, len(conditions))
	var decisiveMatch *singleAttentionConditionMatch

	for _, cond := range conditions {
		c := strings.TrimSpace(cond)
		if !isAttentionBasedCondition(c) {
			continue
		}

		match := checkSingleAttentionCondition(c, events)
		if match == nil {
			return result
		}

		matchedConditions = append(matchedConditions, c)
		matchCounts[c] = match.TriggerCount
		if decisiveMatch == nil || (match.TriggerEvent != nil && decisiveMatch.TriggerEvent != nil &&
			match.TriggerEvent.Cursor > decisiveMatch.TriggerEvent.Cursor) {
			decisiveMatch = match
		}
	}

	if decisiveMatch == nil {
		return result
	}

	result.Met = true
	result.Condition = decisiveMatch.Condition
	result.TriggerEvent = decisiveMatch.TriggerEvent
	result.TriggerCount = decisiveMatch.TriggerCount
	result.ObservedCursor = decisiveMatch.TriggerEvent.Cursor
	result.Details["matched_conditions"] = matchedConditions
	result.Details["match_count_by_condition"] = matchCounts
	return result
}

// checkSingleAttentionCondition checks a single attention-based condition.
func checkSingleAttentionCondition(condition string, events []AttentionEvent) *singleAttentionConditionMatch {
	count := 0
	var firstMatch *AttentionEvent

	for i := range events {
		if !attentionEventMatchesWaitCondition(condition, events[i]) {
			continue
		}
		count++
		if firstMatch == nil {
			matched := cloneAttentionEvent(events[i])
			firstMatch = &matched
		}
	}

	if firstMatch == nil {
		return nil
	}

	return &singleAttentionConditionMatch{
		Condition:    condition,
		TriggerEvent: firstMatch,
		TriggerCount: count,
	}
}

func attentionEventMatchesWaitCondition(condition string, event AttentionEvent) bool {
	switch condition {
	case WaitConditionAttention:
		return attentionActionabilityRank(event.Actionability) >= attentionActionabilityRank(ActionabilityInteresting)
	case WaitConditionActionRequired:
		return event.Actionability == ActionabilityActionRequired
	case WaitConditionMailPending:
		return event.Category == EventCategoryMail && event.Type == EventTypeMailReceived
	case WaitConditionMailAckRequired:
		return event.Category == EventCategoryMail && event.Type == EventTypeMailAckRequired
	case WaitConditionContextHot:
		return attentionEventSignal(event) == attentionSignalContextHot
	case WaitConditionReservationConflict:
		return attentionEventSignal(event) == attentionSignalReservationConflict || isReservationConflictAttentionEvent(event)
	case WaitConditionFileConflict:
		return attentionEventSignal(event) == attentionSignalFileConflict || isFileConflictAttentionEvent(event)
	case WaitConditionSessionChanged:
		return attentionEventSignal(event) == attentionSignalSessionChanged
	case WaitConditionPaneChanged:
		return attentionEventSignal(event) == attentionSignalPaneChanged
	default:
		return false
	}
}

func attentionEventSignal(event AttentionEvent) string {
	if signal := attentionStringDetail(event.Details, "signal"); signal != "" {
		return signal
	}
	signal, _, _ := deriveAttentionSignal(event)
	return signal
}

func buildWaitWakePayload(result *AttentionConditionResult) *WaitWakePayload {
	if result == nil || !result.Met {
		return nil
	}
	return &WaitWakePayload{
		MatchedCondition: result.Condition,
		TriggerEvent:     result.TriggerEvent,
		TriggerCount:     result.TriggerCount,
		Details:          result.Details,
	}
}

func buildWaitCursorInfo(result *AttentionConditionResult) *WaitCursorInfo {
	if result == nil {
		return nil
	}
	return &WaitCursorInfo{
		ObservedCursor: result.ObservedCursor,
		NextCursor:     result.NextCursor,
		OldestCursor:   result.OldestCursor,
	}
}

// countByActionability counts events with a specific actionability level.
func countByActionability(events []AttentionEvent, level Actionability) int {
	count := 0
	for _, ev := range events {
		if ev.Actionability == level {
			count++
		}
	}
	return count
}

// countByType counts events with a specific event type.
func countByType(events []AttentionEvent, eventType EventType) int {
	count := 0
	for _, ev := range events {
		if ev.Type == eventType {
			count++
		}
	}
	return count
}

// countByCategory counts events with a specific category.
func countByCategory(events []AttentionEvent, category EventCategory) int {
	count := 0
	for _, ev := range events {
		if ev.Category == category {
			count++
		}
	}
	return count
}
