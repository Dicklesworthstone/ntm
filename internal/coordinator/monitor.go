package coordinator

import (
	"context"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AgentMonitor tracks agent status using the status detector.
type AgentMonitor struct {
	session     string
	projectKey  string
	mailClient  *agentmail.Client
	detector    *status.UnifiedDetector
	activityMon *robot.ActivityMonitor
}

// AgentStatusResult holds the result of checking an agent's status.
type AgentStatusResult struct {
	Status       robot.AgentState `json:"status"`
	ContextUsage float64          `json:"context_usage"`
	LastActivity time.Time        `json:"last_activity"`
	Velocity     float64          `json:"velocity"`
	Healthy      bool             `json:"healthy"`
	ErrorMessage string           `json:"error_message,omitempty"`
}

// NewAgentMonitor creates a new agent monitor.
func NewAgentMonitor(session string, mailClient *agentmail.Client, projectKey string) *AgentMonitor {
	return &AgentMonitor{
		session:     session,
		projectKey:  projectKey,
		mailClient:  mailClient,
		detector:    status.NewDetector(),
		activityMon: robot.NewActivityMonitor(nil),
	}
}

// GetAgentStatus returns the current status of an agent pane.
func (m *AgentMonitor) GetAgentStatus(paneID, agentType string) AgentStatusResult {
	result := AgentStatusResult{
		Status:       robot.StateUnknown,
		ContextUsage: 0,
		LastActivity: time.Time{},
		Healthy:      true,
	}

	// Use the unified status detector
	agentStatus, err := m.detector.Detect(paneID)
	if err != nil {
		result.Healthy = false
		result.ErrorMessage = err.Error()
		return result
	}

	// Map status.AgentState to robot.AgentState
	result.Status = mapStatusToRobotState(agentStatus.State)
	result.LastActivity = agentStatus.LastActive
	result.Healthy = agentStatus.State != status.StateError

	// Use activity monitor for velocity
	classifier := m.activityMon.GetOrCreate(paneID)
	classifier.SetAgentType(agentType)
	activity, err := classifier.Classify()
	if err == nil {
		result.Velocity = activity.Velocity
		if activity.State == robot.StateError {
			result.Status = robot.StateError
			result.Healthy = false
		}
	}

	return result
}

// GetAllAgentStatuses returns status for all agent panes in the session.
func (m *AgentMonitor) GetAllAgentStatuses() (map[string]AgentStatusResult, error) {
	panes, err := tmux.GetPanes(m.session)
	if err != nil {
		return nil, err
	}

	results := make(map[string]AgentStatusResult)
	for _, pane := range panes {
		if pane.Type == tmux.AgentUser || pane.Type == tmux.AgentUnknown {
			continue // Skip non-agent panes
		}
		results[pane.ID] = m.GetAgentStatus(pane.ID, string(pane.Type))
	}

	return results, nil
}

// CheckAgentHealth returns a health summary for an agent.
func (m *AgentMonitor) CheckAgentHealth(paneID, agentType string) HealthCheck {
	agentStatus := m.GetAgentStatus(paneID, agentType)

	check := HealthCheck{
		PaneID:    paneID,
		AgentType: agentType,
		Healthy:   agentStatus.Healthy,
		Timestamp: time.Now(),
	}

	// Determine health issues
	if !agentStatus.Healthy {
		check.Issues = append(check.Issues, agentStatus.ErrorMessage)
	}
	if agentStatus.Status == robot.StateError {
		check.Issues = append(check.Issues, "agent in error state")
	}
	if agentStatus.Status == robot.StateStalled {
		check.Issues = append(check.Issues, "agent appears stalled")
	}
	if agentStatus.ContextUsage > 85 {
		check.Issues = append(check.Issues, "context usage high (>85%)")
	}

	return check
}

// HealthCheck represents the result of a health check.
type HealthCheck struct {
	PaneID    string    `json:"pane_id"`
	AgentType string    `json:"agent_type"`
	Healthy   bool      `json:"healthy"`
	Issues    []string  `json:"issues,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// GetReservationsForAgent returns file reservations held by an agent.
func (m *AgentMonitor) GetReservationsForAgent(ctx context.Context, agentMailName string) ([]string, error) {
	if m.mailClient == nil || agentMailName == "" {
		return nil, nil
	}

	reservations, err := m.mailClient.ListReservations(ctx, m.projectKey, agentMailName, true)
	if err != nil {
		return nil, err
	}

	var patterns []string
	for _, r := range reservations {
		if r.ReleasedTS == nil && time.Now().Before(r.ExpiresTS.Time) {
			patterns = append(patterns, r.PathPattern)
		}
	}

	return patterns, nil
}

// mapStatusToRobotState converts status.AgentState to robot.AgentState.
func mapStatusToRobotState(s status.AgentState) robot.AgentState {
	switch s {
	case status.StateIdle:
		return robot.StateWaiting
	case status.StateWorking:
		return robot.StateGenerating
	case status.StateError:
		return robot.StateError
	default:
		return robot.StateUnknown
	}
}
