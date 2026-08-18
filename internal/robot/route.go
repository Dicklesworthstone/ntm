// Package robot provides machine-readable output for AI agents and automation.
// route.go implements the --robot-route API for agent routing recommendations.
package robot

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// RoutingStateStore is the persistence port for per-session routing state
// (bd-ws1-truth-safety-l5ddi.10). *state.Store implements it. State is keyed
// by (session, filter key) so differently filtered candidate lists rotate
// independently (bd-88um4).
type RoutingStateStore interface {
	GetRoutingState(session, filterKey string) (*state.RoutingState, error)
	SaveRoutingState(*state.RoutingState) error
	PurgeRoutingStateOlderThan(maxAge time.Duration) (int64, error)
}

// openRoutingStateStore opens (and migrates) the state DB for routing state.
// Best-effort: callers proceed without persistence when it fails. This is the
// SEND path's opener: it will write routing state, so migrating here is fine.
func openRoutingStateStore() (*state.Store, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// openRoutingStateStoreReadOnly opens the state DB WITHOUT migrating, for the
// advisory route surface. 'ntm robot route' only READS routing state, and
// Migrate previously took the DB's exclusive write reservation (BEGIN
// IMMEDIATE) on every call — a polling swarm serialized on it, and past
// busy_timeout the open failed and routing silently degraded (bd-88um4). An
// unmigrated schema simply reads as "no routing history".
func openRoutingStateStoreReadOnly() (*state.Store, error) {
	return state.Open("")
}

// routingStateFilterKey derives the persistence key component for the filter
// set that shaped the candidate list. The rotation cursor is an index into
// the FILTERED list, so alternating sends with different --cc/--cod filters
// or --exclude sets must not share one cursor (bd-88um4). The empty key is
// the unfiltered path (and matches pre-021 legacy rows).
func routingStateFilterKey(opts RouteOptions) string {
	agentType := strings.ToLower(strings.TrimSpace(opts.AgentType))
	if agentType == "" && len(opts.ExcludePanes) == 0 {
		return ""
	}
	excludes := append([]int(nil), opts.ExcludePanes...)
	sort.Ints(excludes)
	parts := make([]string, 0, len(excludes))
	for _, idx := range excludes {
		parts = append(parts, strconv.Itoa(idx))
	}
	return "type=" + agentType + ";exclude=" + strings.Join(parts, ",")
}

// routingStateTTL bounds how long a routing-state row can sit untouched
// before the send path purges it. Rows for dead sessions otherwise accumulate
// forever, and a recreated session with the same name inherits a stale
// last_agent/cursor (bd-88um4). Routing state is only a rotation hint; a week
// of inactivity means the history is worthless anyway.
const routingStateTTL = 7 * 24 * time.Hour

// routingStrategyIsStateful reports whether a strategy consumes/advances
// per-session routing history.
func routingStrategyIsStateful(strategy StrategyName) bool {
	switch strategy {
	case StrategySticky, StrategyRoundRobin, StrategyRoundRobinAvailable:
		return true
	}
	return false
}

// routeWithSessionState is the single routing entry used by both the route
// surface and the send path. It loads persisted per-session routing state
// (LastAgent + rotation cursor) into the routing context, routes, and — when
// persist is true (the send path) — advances the persisted state to the
// selected pane so sticky and round-robin are REAL across sequential CLI
// invocations (bd-ws1-truth-safety-l5ddi.10). Persistence is best-effort and
// never fails the routing decision.
func routeWithSessionState(agents []ScoredAgent, opts RouteOptions, store RoutingStateStore, persist bool) RoutingResult {
	router := NewRouter()
	ctx := RoutingContext{
		Prompt:       opts.Prompt,
		LastAgent:    opts.LastAgent,
		ExcludePanes: opts.ExcludePanes,
		ExplicitPane: -1,
	}
	filterKey := routingStateFilterKey(opts)
	if store != nil && ctx.LastAgent == "" {
		rs, err := store.GetRoutingState(opts.Session, filterKey)
		switch {
		case err != nil:
			slog.Warn("[robot.route] cannot load persisted routing state", "session", opts.Session, "error", err)
		case rs != nil:
			ctx.LastAgent = rs.LastAgent
			if rs.RotationCursor >= 0 {
				ctx.RotationCursor = rs.RotationCursor
				ctx.HasRotationCursor = true
			}
		}
	}

	result := router.Route(agents, opts.Strategy, ctx)

	if persist && store != nil && result.Selected != nil && routingStrategyIsStateful(opts.Strategy) {
		cursor := -1
		for i := range agents {
			if agents[i].PaneID == result.Selected.PaneID {
				cursor = i
				break
			}
		}
		rs := &state.RoutingState{
			SessionName:    opts.Session,
			FilterKey:      filterKey,
			LastAgent:      result.Selected.PaneID,
			RotationCursor: cursor,
		}
		if err := store.SaveRoutingState(rs); err != nil {
			slog.Warn("[robot.route] cannot persist routing state",
				"session", opts.Session, "strategy", opts.Strategy, "error", err)
		} else {
			slog.Info("[robot.route] routing state persisted",
				"session", opts.Session, "strategy", opts.Strategy,
				"pane_id", result.Selected.PaneID, "pane_index", result.Selected.PaneIndex,
				"rotation_cursor", cursor, "filter_key", filterKey)
			// Opportunistic TTL purge on the write path only: rows for dead
			// sessions must not accumulate forever (bd-88um4). Best-effort.
			if purged, err := store.PurgeRoutingStateOlderThan(routingStateTTL); err == nil && purged > 0 {
				slog.Debug("[robot.route] purged stale routing state rows", "purged", purged)
			}
		}
	}
	return result
}

// RouteOptions configures the routing recommendation request.
type RouteOptions struct {
	Session      string         // Required: session name
	AgentType    string         // Optional: filter by agent type (claude/cc, codex/cod, gemini/gmi)
	Strategy     StrategyName   // Optional: routing strategy (default: least-loaded)
	ExcludePanes []int          // Optional: pane indices to exclude
	Prompt       string         // Optional: prompt for affinity matching
	LastAgent    string         // Optional: last used agent pane ID for sticky routing
	Config       *config.Config // Optional: loaded routing configuration
	NoPersist    bool           // Optional: preview only — never advance persisted routing state (e.g. send --dry-run)
}

// RouteOutput is the structured output for --robot-route.
type RouteOutput struct {
	RobotResponse                       // Embed standard response fields (success, timestamp, error)
	Session        string               `json:"session"`
	Strategy       StrategyName         `json:"strategy"`
	Recommendation *RouteRecommendation `json:"recommendation,omitempty"`
	Candidates     []RouteCandidate     `json:"candidates"`
	Excluded       []RouteExcluded      `json:"excluded,omitempty"`
	FallbackUsed   bool                 `json:"fallback_used,omitempty"`
	AgentHints     *RouteAgentHints     `json:"_agent_hints,omitempty"`
}

// RouteRecommendation contains the recommended agent for routing.
type RouteRecommendation struct {
	PaneID       string  `json:"pane_id"`
	PaneIndex    int     `json:"pane_index"`
	AgentType    string  `json:"agent_type"`
	Score        float64 `json:"score"`
	Reason       string  `json:"reason"`
	ContextUsage float64 `json:"context_usage"`
	State        string  `json:"state"`
}

// RouteCandidate represents an agent that was considered for routing.
type RouteCandidate struct {
	PaneID       string  `json:"pane_id"`
	PaneIndex    int     `json:"pane_index"`
	AgentType    string  `json:"agent_type"`
	Score        float64 `json:"score"`
	ContextUsage float64 `json:"context_usage"`
	State        string  `json:"state"`
	StateScore   float64 `json:"state_score"`
	RecencyScore float64 `json:"recency_score"`
}

// RouteExcluded represents an agent that was excluded from routing.
type RouteExcluded struct {
	PaneID    string `json:"pane_id"`
	PaneIndex int    `json:"pane_index"`
	AgentType string `json:"agent_type"`
	Reason    string `json:"reason"`
	State     string `json:"state,omitempty"`
}

// RouteAgentHints provides guidance for AI agents consuming route output.
type RouteAgentHints struct {
	Summary     string   `json:"summary"`
	SendCommand string   `json:"send_command,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// GetRoute executes the routing operation and returns the output data.
// Returns the output and exit code (0=success, 1=error).
func GetRoute(opts RouteOptions) (*RouteOutput, int) {
	output := &RouteOutput{
		Session:    opts.Session,
		Strategy:   opts.Strategy,
		Candidates: []RouteCandidate{},
		Excluded:   []RouteExcluded{},
	}

	// Validate session
	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-route=mysession",
		)
		return output, 1
	}

	// Validate strategy
	if opts.Strategy == "" {
		opts.Strategy = StrategyLeastLoaded
	}
	output.Strategy = opts.Strategy

	if !IsValidStrategy(opts.Strategy) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid strategy: %s", opts.Strategy),
			ErrCodeInvalidFlag,
			fmt.Sprintf("Valid strategies: %s", strings.Join(strategyNames(), ", ")),
		)
		return output, 1
	}

	// Check session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, 1
	}

	// Get all panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session is running",
		)
		return output, 1
	}

	contextUsage := getContextUsageByPane(opts.Session)

	// Create scorer and score agents. Reservation affinity is wired
	// best-effort from Agent Mail when enabled (bd-ws2-wire-or-delete-ykmcz.3).
	scorer := NewAgentScorerFromConfig(opts.Config)
	scorer.wireReservationAffinity(opts.Config, opts.Session)
	var agents []ScoredAgent

	for _, pane := range panes {
		agentType := routePaneAgentType(pane)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}

		// Filter by agent type if specified
		if !matchesAgentTypeFilter(agentType, opts.AgentType) {
			continue
		}

		// Get activity state
		classifier := scorer.monitor.GetOrCreate(pane.ID)
		classifier.SetAgentType(agentType)
		activity, err := classifier.Classify()
		if err != nil {
			// Add to excluded with error
			output.Excluded = append(output.Excluded, RouteExcluded{
				PaneID:    pane.ID,
				PaneIndex: pane.Index,
				AgentType: agentType,
				Reason:    fmt.Sprintf("failed to classify: %v", err),
			})
			continue
		}

		// Build scored agent from the activity classifier's authoritative state.
		agent := scoredAgentForRouting(pane, agentType, activity, contextUsage)

		// Calculate score components
		agent.ScoreDetail = scorer.calculateScoreComponents(&agent, opts.Prompt)

		// Check exclusion rules
		excluded, reason := excludeRouteAgent(scorer, &agent, pane)
		if excluded {
			agent.Excluded = true
			agent.ExcludeReason = reason
			agent.Score = 0
		} else {
			agent.Score = scorer.calculateFinalScore(&agent)
		}

		agents = append(agents, agent)
	}

	// Apply pane exclusions from options
	if len(opts.ExcludePanes) > 0 {
		agents = ExcludePanes(agents, opts.ExcludePanes)
	}

	// Build candidates and excluded lists
	for _, agent := range agents {
		if agent.Excluded {
			output.Excluded = append(output.Excluded, RouteExcluded{
				PaneID:    agent.PaneID,
				PaneIndex: agent.PaneIndex,
				AgentType: agent.AgentType,
				Reason:    agent.ExcludeReason,
				State:     string(agent.State),
			})
		} else {
			output.Candidates = append(output.Candidates, RouteCandidate{
				PaneID:       agent.PaneID,
				PaneIndex:    agent.PaneIndex,
				AgentType:    agent.AgentType,
				Score:        agent.Score,
				ContextUsage: agent.ContextUsage,
				State:        string(agent.State),
				StateScore:   agent.ScoreDetail.StateScore,
				RecencyScore: agent.ScoreDetail.RecencyScore,
			})
		}
	}

	// Route with persisted per-session state loaded (advisory surface: the
	// state is read, not advanced — only real sends advance it). The
	// read-only opener skips Migrate so this path never takes the state DB's
	// exclusive write reservation (bd-88um4).
	var stateStore RoutingStateStore
	if store, err := openRoutingStateStoreReadOnly(); err == nil {
		defer store.Close()
		stateStore = store
	} else {
		slog.Warn("[robot.route] routing state store unavailable", "session", opts.Session, "error", err)
	}
	result := routeWithSessionState(agents, opts, stateStore, false)
	output.FallbackUsed = result.FallbackUsed

	if result.Selected != nil {
		output.Recommendation = &RouteRecommendation{
			PaneID:       result.Selected.PaneID,
			PaneIndex:    result.Selected.PaneIndex,
			AgentType:    result.Selected.AgentType,
			Score:        result.Selected.Score,
			Reason:       result.Reason,
			ContextUsage: result.Selected.ContextUsage,
			State:        string(result.Selected.State),
		}
	}

	// Add agent hints
	output.AgentHints = generateRouteHints(opts, *output)

	output.RobotResponse = NewRobotResponse(true)
	return output, 0
}

// PrintRoute outputs routing recommendation as JSON.
// Returns 0 on success, 1 on error.
func PrintRoute(opts RouteOptions) int {
	output, exitCode := GetRoute(opts)
	return printLegacyRobotOutput(output, output.RobotResponse, exitCode, "robot route failed")
}

// excludeRouteAgent applies the scorer's exclusion rules plus pane-level
// facts the scorer cannot see. A pane whose agent CLI has exited back to a
// bare shell is unroutable regardless of its classified activity state: the
// delivery layer refuses it outright (PANE_AGENT_DEAD), so recommending it
// would hand the caller a send that cannot succeed (bd-fresh-eyes-audit .8).
func excludeRouteAgent(scorer *AgentScorer, agent *ScoredAgent, pane tmux.Pane) (bool, string) {
	if excluded, reason := scorer.checkExclusion(agent); excluded {
		return true, reason
	}
	if pane.AgentCLIDead() {
		return true, "agent CLI exited to a bare shell"
	}
	return false, ""
}

func routePaneAgentType(pane tmux.Pane) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

// generateRouteHints creates helpful hints for AI agents.
func generateRouteHints(opts RouteOptions, output RouteOutput) *RouteAgentHints {
	hints := &RouteAgentHints{}

	if output.Recommendation != nil {
		rec := output.Recommendation
		hints.Summary = fmt.Sprintf("Route to %s (pane %d) with score %.1f - %s",
			rec.AgentType, rec.PaneIndex, rec.Score, rec.State)
		hints.SendCommand = fmt.Sprintf("ntm --robot-send=%s --panes=%d --msg='YOUR_MESSAGE'",
			opts.Session, rec.PaneIndex)
	} else if len(output.Candidates) == 0 {
		hints.Summary = "No agents available for routing"
		if len(output.Excluded) > 0 {
			hints.Suggestions = append(hints.Suggestions, "All agents are excluded - check exclusion reasons")
		} else {
			hints.Suggestions = append(hints.Suggestions, "No agents found in session - spawn agents first")
		}
	} else {
		hints.Summary = fmt.Sprintf("%d candidates available, but strategy returned no selection", len(output.Candidates))
	}

	if output.FallbackUsed {
		hints.Suggestions = append(hints.Suggestions, "Primary strategy failed - fallback was used")
	}

	return hints
}

// strategyNames returns list of valid strategy names as strings.
func strategyNames() []string {
	names := GetStrategyNames()
	result := make([]string, len(names))
	for i, n := range names {
		result[i] = string(n)
	}
	return result
}

// ParseExcludePanes parses a comma-separated list of pane indices.
func ParseExcludePanes(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}

	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pane index '%s': %w", p, err)
		}
		result = append(result, idx)
	}
	return result, nil
}

// GetRouteRecommendation returns the routing recommendation without JSON output.
// This is used by the send command for smart routing integration.
func GetRouteRecommendation(opts RouteOptions) (*RouteRecommendation, error) {
	// Validate session
	if opts.Session == "" {
		return nil, fmt.Errorf("session name is required")
	}

	// Validate strategy
	if opts.Strategy == "" {
		opts.Strategy = StrategyLeastLoaded
	}
	if !IsValidStrategy(opts.Strategy) {
		return nil, fmt.Errorf("invalid strategy: %s", opts.Strategy)
	}

	// Check session exists
	if !tmux.SessionExists(opts.Session) {
		return nil, fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Get all panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	contextUsage := getContextUsageByPane(opts.Session)

	// Create scorer and score agents. Reservation affinity is wired
	// best-effort from Agent Mail when enabled (bd-ws2-wire-or-delete-ykmcz.3)
	// — this is the send path, so the bonus influences real dispatch.
	scorer := NewAgentScorerFromConfig(opts.Config)
	scorer.wireReservationAffinity(opts.Config, opts.Session)
	var agents []ScoredAgent

	for _, pane := range panes {
		agentType := routePaneAgentType(pane)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}

		// Filter by agent type if specified
		if !matchesAgentTypeFilter(agentType, opts.AgentType) {
			continue
		}

		// Get activity state
		classifier := scorer.monitor.GetOrCreate(pane.ID)
		classifier.SetAgentType(agentType)
		activity, err := classifier.Classify()
		if err != nil {
			continue // Skip agents that can't be classified
		}

		// Build scored agent from the activity classifier's authoritative state.
		agent := scoredAgentForRouting(pane, agentType, activity, contextUsage)

		// Calculate score components
		agent.ScoreDetail = scorer.calculateScoreComponents(&agent, opts.Prompt)

		// Check exclusion rules
		excluded, reason := excludeRouteAgent(scorer, &agent, pane)
		if excluded {
			agent.Excluded = true
			agent.ExcludeReason = reason
			agent.Score = 0
		} else {
			agent.Score = scorer.calculateFinalScore(&agent)
		}

		agents = append(agents, agent)
	}

	// Apply pane exclusions from options
	if len(opts.ExcludePanes) > 0 {
		agents = ExcludePanes(agents, opts.ExcludePanes)
	}

	// Route with persisted per-session state, ADVANCING it on selection: this
	// is the send path, so sticky and round-robin must be real across
	// sequential CLI invocations (bd-ws1-truth-safety-l5ddi.10).
	var stateStore RoutingStateStore
	if store, err := openRoutingStateStore(); err == nil {
		defer store.Close()
		stateStore = store
	} else {
		slog.Warn("[robot.route] routing state store unavailable", "session", opts.Session, "error", err)
	}
	result := routeWithSessionState(agents, opts, stateStore, !opts.NoPersist)
	if result.Selected == nil {
		return nil, nil // No agent available
	}

	return &RouteRecommendation{
		PaneID:       result.Selected.PaneID,
		PaneIndex:    result.Selected.PaneIndex,
		AgentType:    result.Selected.AgentType,
		Score:        result.Selected.Score,
		Reason:       result.Reason,
		ContextUsage: result.Selected.ContextUsage,
		State:        string(result.Selected.State),
	}, nil
}
