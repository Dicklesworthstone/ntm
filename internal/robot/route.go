// Package robot provides machine-readable output for AI agents and automation.
// route.go implements the --robot-route API for agent routing recommendations.
package robot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// RouteOptions configures the routing recommendation request.
type RouteOptions struct {
	Session      string       // Required: session name
	AgentType    string       // Optional: filter by agent type (claude/cc, codex/cod, gemini/gmi)
	Strategy     StrategyName // Optional: routing strategy (default: least-loaded)
	ExcludePanes []int        // Optional: pane indices to exclude
	Prompt       string       // Optional: prompt for affinity matching
	LastAgent    string       // Optional: last used agent pane ID for sticky routing
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

// PrintRoute outputs routing recommendation as JSON.
// Returns 0 on success, 1 on error.
func PrintRoute(opts RouteOptions) int {
	output := RouteOutput{
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
		outputJSON(output)
		return 1
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
		outputJSON(output)
		return 1
	}

	// Check session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		outputJSON(output)
		return 1
	}

	// Get all panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session is running",
		)
		outputJSON(output)
		return 1
	}

	// Create scorer and score agents
	scorer := NewAgentScorer(DefaultRoutingConfig())
	var agents []ScoredAgent

	for _, pane := range panes {
		// Skip user pane
		// Use detectAgentType which maps short forms (cc->claude, cod->codex, gmi->gemini)
		agentType := detectAgentType(pane.Title)
		if agentType == "" || agentType == "unknown" {
			continue
		}

		// Filter by agent type if specified
		if opts.AgentType != "" && !strings.EqualFold(agentType, normalizeAgentType(opts.AgentType)) {
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

		// Build scored agent
		agent := ScoredAgent{
			PaneID:       pane.ID,
			AgentType:    agentType,
			PaneIndex:    pane.Index,
			State:        activity.State,
			Confidence:   activity.Confidence,
			Velocity:     activity.Velocity,
			LastActivity: activity.LastOutput,
			HealthState:  deriveHealthState(activity.State),
			RateLimited:  false,
		}

		// Calculate score components
		agent.ScoreDetail = scorer.calculateScoreComponents(&agent, opts.Prompt)

		// Check exclusion rules
		excluded, reason := scorer.checkExclusion(&agent)
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

	// Create router and get recommendation
	router := NewRouter()
	ctx := RoutingContext{
		Prompt:       opts.Prompt,
		LastAgent:    opts.LastAgent,
		ExcludePanes: opts.ExcludePanes,
		ExplicitPane: -1,
	}

	result := router.Route(agents, opts.Strategy, ctx)
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
	output.AgentHints = generateRouteHints(opts, output)

	output.RobotResponse = NewRobotResponse(true)
	outputJSON(output)
	return 0
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

	// Create scorer and score agents
	scorer := NewAgentScorer(DefaultRoutingConfig())
	var agents []ScoredAgent

	for _, pane := range panes {
		// Skip user pane
		// Use detectAgentType which maps short forms (cc->claude, cod->codex, gmi->gemini)
		agentType := detectAgentType(pane.Title)
		if agentType == "" || agentType == "unknown" {
			continue
		}

		// Filter by agent type if specified
		if opts.AgentType != "" && !strings.EqualFold(agentType, normalizeAgentType(opts.AgentType)) {
			continue
		}

		// Get activity state
		classifier := scorer.monitor.GetOrCreate(pane.ID)
		classifier.SetAgentType(agentType)
		activity, err := classifier.Classify()
		if err != nil {
			continue // Skip agents that can't be classified
		}

		// Build scored agent
		agent := ScoredAgent{
			PaneID:       pane.ID,
			AgentType:    agentType,
			PaneIndex:    pane.Index,
			State:        activity.State,
			Confidence:   activity.Confidence,
			Velocity:     activity.Velocity,
			LastActivity: activity.LastOutput,
			HealthState:  deriveHealthState(activity.State),
			RateLimited:  false,
		}

		// Calculate score components
		agent.ScoreDetail = scorer.calculateScoreComponents(&agent, opts.Prompt)

		// Check exclusion rules
		excluded, reason := scorer.checkExclusion(&agent)
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

	// Create router and get recommendation
	router := NewRouter()
	ctx := RoutingContext{
		Prompt:       opts.Prompt,
		LastAgent:    opts.LastAgent,
		ExcludePanes: opts.ExcludePanes,
		ExplicitPane: -1,
	}

	result := router.Route(agents, opts.Strategy, ctx)
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
