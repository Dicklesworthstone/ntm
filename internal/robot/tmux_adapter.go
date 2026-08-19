// Package robot provides machine-readable output for AI agents.
// tmux_adapter.go normalizes tmux session/pane data into canonical projection shapes.
//
// This adapter transforms raw tmux data into RuntimeSession and RuntimeAgent
// structures suitable for robot surfaces. It insulates the rest of the robot
// stack from tmux-specific mechanics while preserving ntm's tmux-first nature.
//
// Bead: bd-j9jo3.3.1
package robot

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Configuration
// =============================================================================

// TmuxAdapterConfig controls the normalization behavior.
type TmuxAdapterConfig struct {
	// SessionStaleness is how long session projections are considered fresh.
	// Default: 5 seconds
	SessionStaleness time.Duration

	// AgentStaleness is how long agent projections are considered fresh.
	// Default: 5 seconds
	AgentStaleness time.Duration

	// StallThreshold is seconds without output before an agent is considered stalled.
	// Default: 300 seconds (5 minutes)
	StallThreshold int

	// IdleThreshold is seconds of output silence that suggest idle state.
	// Default: 30 seconds
	IdleThreshold int

	// PatternLibrary for agent state detection. If nil, uses default patterns.
	PatternLibrary *PatternLibrary
}

// DefaultTmuxAdapterConfig returns sensible defaults.
func DefaultTmuxAdapterConfig() TmuxAdapterConfig {
	return TmuxAdapterConfig{
		SessionStaleness: 5 * time.Second,
		AgentStaleness:   5 * time.Second,
		StallThreshold:   300,
		IdleThreshold:    30,
		PatternLibrary:   nil, // Will use global default
	}
}

// =============================================================================
// Adapter
// =============================================================================

// TmuxAdapter normalizes tmux data into canonical projection shapes.
type TmuxAdapter struct {
	config TmuxAdapterConfig
	lib    *PatternLibrary
}

// NewTmuxAdapter creates a new adapter with the given configuration.
func NewTmuxAdapter(config TmuxAdapterConfig) *TmuxAdapter {
	lib := config.PatternLibrary
	if lib == nil {
		lib = NewPatternLibrary()
	}
	return &TmuxAdapter{
		config: config,
		lib:    lib,
	}
}

// =============================================================================
// Session Normalization
// =============================================================================

// NormalizeSession transforms a tmux.Session into a RuntimeSession.
func (a *TmuxAdapter) NormalizeSession(sess *tmux.Session, agents []Agent) *state.RuntimeSession {
	now := time.Now()

	// Parse creation time
	var createdAt *time.Time
	if sess.Created != "" {
		if t, err := time.Parse(time.RFC3339, sess.Created); err == nil {
			createdAt = &t
		}
	}

	// Count agent states
	var activeAgents, idleAgents, errorAgents int
	var lastActivity time.Time
	for _, agent := range agents {
		agentState := a.classifyAgentState(&agent)
		switch agentState {
		case state.AgentStateActive, state.AgentStateBusy:
			activeAgents++
		case state.AgentStateIdle:
			idleAgents++
		case state.AgentStateError:
			errorAgents++
		}
		if !agent.LastOutputTS.IsZero() && agent.LastOutputTS.After(lastActivity) {
			lastActivity = agent.LastOutputTS
		}
	}

	// Compute health status
	healthStatus, healthReason := a.computeSessionHealth(sess, agents, errorAgents)

	// Track last attached time (approximation: if attached, it's now)
	var lastAttachedAt *time.Time
	if sess.Attached {
		lastAttachedAt = &now
	}

	var lastActivityAt *time.Time
	if !lastActivity.IsZero() {
		lastActivityAt = &lastActivity
	}

	return &state.RuntimeSession{
		Name:           sess.Name,
		Label:          extractSessionLabel(sess.Name),
		ProjectPath:    sess.Directory,
		Attached:       sess.Attached,
		WindowCount:    sess.Windows,
		PaneCount:      len(sess.Panes),
		AgentCount:     len(agents),
		ActiveAgents:   activeAgents,
		IdleAgents:     idleAgents,
		ErrorAgents:    errorAgents,
		HealthStatus:   healthStatus,
		HealthReason:   healthReason,
		CreatedAt:      createdAt,
		LastAttachedAt: lastAttachedAt,
		LastActivityAt: lastActivityAt,
		CollectedAt:    now,
		StaleAfter:     now.Add(a.config.SessionStaleness),
	}
}

// =============================================================================
// Agent Normalization
// =============================================================================

// NormalizeAgent transforms a robot.Agent into a RuntimeAgent.
func (a *TmuxAdapter) NormalizeAgent(sessionName string, agent *Agent, outputTail string) *state.RuntimeAgent {
	now := time.Now()

	// Compute agent ID
	agentID := fmt.Sprintf("%s:%s", sessionName, agent.Pane)

	// Classify state using patterns and heuristics
	agentState := a.classifyAgentState(agent)
	stateReason := a.classifyStateReason(agent, agentState, outputTail)

	// Compute health
	healthStatus, healthReason := a.computeAgentHealth(agent, agentState)

	// Track last output
	var lastOutputAt *time.Time
	lastOutputAgeSec := -1
	if !agent.LastOutputTS.IsZero() {
		lastOutputAt = &agent.LastOutputTS
		lastOutputAgeSec = agent.SecondsSinceOutput
	}

	// Type confidence based on detection method
	typeConfidence, typeMethod := a.computeTypeConfidence(agent)

	return &state.RuntimeAgent{
		ID:               agentID,
		SessionName:      sessionName,
		Pane:             agent.Pane,
		AgentType:        normalizeAgentType(agent.Type),
		Variant:          agent.Variant,
		TypeConfidence:   typeConfidence,
		TypeMethod:       typeMethod,
		State:            agentState,
		StateReason:      stateReason,
		PreviousState:    "", // Would need state tracking
		StateChangedAt:   nil,
		LastOutputAt:     lastOutputAt,
		LastOutputAgeSec: lastOutputAgeSec,
		OutputTailLines:  agent.OutputLinesSinceLast,
		CurrentBead:      "", // Would need external enrichment
		PendingMail:      0,  // Would need Agent Mail integration
		AgentMailName:    agent.Name,
		CaptureError:     agent.CaptureError,
		HealthStatus:     healthStatus,
		HealthReason:     healthReason,
		CollectedAt:      now,
		StaleAfter:       now.Add(a.config.AgentStaleness),
	}
}

// NormalizeAgents transforms multiple robot.Agent structs.
func (a *TmuxAdapter) NormalizeAgents(sessionName string, agents []Agent, outputTails map[string]string) []state.RuntimeAgent {
	result := make([]state.RuntimeAgent, 0, len(agents))
	for i := range agents {
		tail := outputTails[agents[i].Pane]
		ra := a.NormalizeAgent(sessionName, &agents[i], tail)
		result = append(result, *ra)
	}
	return result
}

// =============================================================================
// State Classification
// =============================================================================

// classifyAgentState determines the agent's state from available signals.
func (a *TmuxAdapter) classifyAgentState(agent *Agent) state.AgentState {
	// Check for error conditions
	if agent.RateLimitDetected {
		return state.AgentStateError
	}

	// Check process state
	if agent.ProcessState == "Z" { // Zombie
		return state.AgentStateError
	}
	if agent.ProcessState == "T" { // Stopped/traced
		return state.AgentStateError
	}

	// Check for stalled state
	if agent.SecondsSinceOutput > a.config.StallThreshold {
		return state.AgentStateError // Stalled is an error condition
	}

	// Check for active output (low seconds since output + lines)
	if agent.SecondsSinceOutput < 5 && agent.OutputLinesSinceLast > 0 {
		return state.AgentStateBusy
	}

	// Check process activity
	if agent.ProcessState == "R" { // Running
		return state.AgentStateActive
	}

	// Context compaction detection
	if agent.ContextPercent > 95 {
		return state.AgentStateCompacting
	}

	// Check idle threshold
	if agent.SecondsSinceOutput > a.config.IdleThreshold {
		// Likely idle - would need pattern matching for confirmation
		return state.AgentStateIdle
	}

	// Default to active if we have recent output
	if agent.SecondsSinceOutput >= 0 && agent.SecondsSinceOutput < a.config.IdleThreshold {
		return state.AgentStateActive
	}

	return state.AgentStateUnknown
}

// classifyStateReason provides human-readable reason for the state.
func (a *TmuxAdapter) classifyStateReason(agent *Agent, agentState state.AgentState, outputTail string) string {
	switch agentState {
	case state.AgentStateError:
		if agent.RateLimitDetected {
			return fmt.Sprintf("rate limited: %s", agent.RateLimitMatch)
		}
		if agent.ProcessState == "Z" {
			return "process zombie"
		}
		if agent.ProcessState == "T" {
			return "process stopped"
		}
		if agent.SecondsSinceOutput > a.config.StallThreshold {
			return fmt.Sprintf("stalled: no output for %ds", agent.SecondsSinceOutput)
		}
		return "error detected"

	case state.AgentStateBusy:
		return fmt.Sprintf("generating output (%d lines in last %ds)", agent.OutputLinesSinceLast, agent.SecondsSinceOutput)

	case state.AgentStateActive:
		if agent.ProcessState == "R" {
			return "process running"
		}
		return "active"

	case state.AgentStateCompacting:
		return fmt.Sprintf("context at %.1f%%, likely compacting", agent.ContextPercent)

	case state.AgentStateIdle:
		// Use pattern library to detect prompt state
		if outputTail != "" && a.lib != nil {
			agentType := normalizeAgentType(agent.Type)
			match := a.lib.MatchOutput(outputTail, agentType)
			if match != nil && match.State == StateWaiting {
				return fmt.Sprintf("waiting at prompt: %s", match.Name)
			}
		}
		return fmt.Sprintf("idle for %ds", agent.SecondsSinceOutput)

	case state.AgentStateUnknown:
		return "insufficient signals"
	}
	return ""
}

// =============================================================================
// Health Computation
// =============================================================================

// computeSessionHealth determines overall session health.
func (a *TmuxAdapter) computeSessionHealth(sess *tmux.Session, agents []Agent, errorCount int) (state.HealthStatus, string) {
	if len(agents) == 0 {
		return state.HealthStatusUnknown, "no agents"
	}

	if errorCount > 0 {
		if errorCount == len(agents) {
			return state.HealthStatusCritical, fmt.Sprintf("all %d agents in error state", errorCount)
		}
		return state.HealthStatusWarning, fmt.Sprintf("%d/%d agents in error state", errorCount, len(agents))
	}

	// Check for rate limiting
	rateLimited := 0
	for _, agent := range agents {
		if agent.RateLimitDetected {
			rateLimited++
		}
	}
	if rateLimited > 0 {
		return state.HealthStatusWarning, fmt.Sprintf("%d agents rate limited", rateLimited)
	}

	return state.HealthStatusHealthy, ""
}

// computeAgentHealth determines individual agent health.
func (a *TmuxAdapter) computeAgentHealth(agent *Agent, agentState state.AgentState) (state.HealthStatus, string) {
	switch agentState {
	case state.AgentStateError:
		if agent.RateLimitDetected {
			return state.HealthStatusWarning, "rate limited"
		}
		return state.HealthStatusCritical, "error state"

	case state.AgentStateCompacting:
		return state.HealthStatusWarning, "context compacting"

	case state.AgentStateUnknown:
		return state.HealthStatusUnknown, "state unknown"

	default:
		// Check context usage
		if agent.ContextPercent > 80 {
			return state.HealthStatusWarning, fmt.Sprintf("context at %.0f%%", agent.ContextPercent)
		}
		return state.HealthStatusHealthy, ""
	}
}

// =============================================================================
// Type Detection
// =============================================================================

// computeTypeConfidence returns confidence and detection method.
func (a *TmuxAdapter) computeTypeConfidence(agent *Agent) (float64, string) {
	// If we have child PID, high confidence from process inspection
	if agent.ChildPID > 0 {
		return 0.95, "process"
	}

	// If we have a pane title with type, medium-high confidence
	if agent.Type != "" && agent.Type != "user" && agent.Type != "unknown" {
		return 0.85, "title"
	}

	// Low confidence fallback
	return 0.5, "heuristic"
}

// Note: normalizeAgentType is already defined in robot.go

// =============================================================================
// Helpers
// =============================================================================

// extractSessionLabel extracts the session label (the part after the canonical
// "--" separator) from a session name via the single-definition helper in
// internal/config/label.go (WS0-G6). Returns "" for unlabeled sessions.
// For example: "" from "myproject", "frontend" from "myproject--frontend".
func extractSessionLabel(name string) string {
	return config.SessionLabel(name)
}

// =============================================================================
// Pattern Matching Integration
// =============================================================================

// MatchOutput finds the best matching pattern for output text.
func (lib *PatternLibrary) MatchOutput(output string, agentType string) *Pattern {
	if lib == nil || !lib.compiled {
		return nil
	}

	lib.mu.RLock()
	defer lib.mu.RUnlock()

	for i := range lib.Patterns {
		p := &lib.Patterns[i]
		// Check agent type match
		if p.Agent != "*" && p.Agent != agentType {
			continue
		}
		// Check regex match
		if p.Regex != nil && p.Regex.MatchString(output) {
			return p
		}
	}
	return nil
}

// =============================================================================
// Batch Normalization
// =============================================================================

// NormalizedSnapshot holds normalized data for a full tmux snapshot.
type NormalizedSnapshot struct {
	Sessions    []state.RuntimeSession `json:"sessions"`
	Agents      []state.RuntimeAgent   `json:"agents"`
	CollectedAt time.Time              `json:"collected_at"`
}

// NormalizeSnapshot transforms a complete tmux state into normalized projections.
func (a *TmuxAdapter) NormalizeSnapshot(
	sessions []tmux.Session,
	agentsBySession map[string][]Agent,
	outputTailsByPane map[string]map[string]string, // session -> pane -> output
) *NormalizedSnapshot {
	now := time.Now()

	snapshot := &NormalizedSnapshot{
		Sessions:    make([]state.RuntimeSession, 0, len(sessions)),
		Agents:      make([]state.RuntimeAgent, 0),
		CollectedAt: now,
	}

	for i := range sessions {
		sess := &sessions[i]
		agents := agentsBySession[sess.Name]
		outputTails := outputTailsByPane[sess.Name]
		if outputTails == nil {
			outputTails = make(map[string]string)
		}

		// Normalize session
		rs := a.NormalizeSession(sess, agents)
		snapshot.Sessions = append(snapshot.Sessions, *rs)

		// Normalize agents
		normalizedAgents := a.NormalizeAgents(sess.Name, agents, outputTails)
		snapshot.Agents = append(snapshot.Agents, normalizedAgents...)
	}

	return snapshot
}
