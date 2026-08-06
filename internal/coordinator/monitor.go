package coordinator

import (
	"context"
	"errors"
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
	observer    *status.SessionObserver
	activityMon *robot.ActivityMonitor
}

// AgentStatusResult holds the result of checking an agent's status.
type AgentStatusResult struct {
	Status              robot.AgentState            `json:"status"`
	LastKnownStatus     robot.AgentState            `json:"last_known_status,omitempty"`
	ContextUsage        float64                     `json:"context_usage"`
	LastActivity        time.Time                   `json:"last_activity"`
	ObservedAt          time.Time                   `json:"observed_at"`
	LastKnownObservedAt time.Time                   `json:"last_known_observed_at,omitempty"`
	Freshness           status.ObservationFreshness `json:"freshness"`
	Confidence          float64                     `json:"confidence"`
	Velocity            float64                     `json:"velocity"`
	Healthy             bool                        `json:"healthy"`
	SafeToDispatch      bool                        `json:"safe_to_dispatch"`
	ErrorMessage        string                      `json:"error_message,omitempty"`
}

// NewAgentMonitor creates a new agent monitor.
func NewAgentMonitor(session string, mailClient *agentmail.Client, projectKey string) *AgentMonitor {
	detector := status.NewDetector()
	observer := status.NewSessionObserverWithDependencies(
		detector,
		status.DefaultSessionObserverConfig(detector.Config()),
		status.SessionObserverDependencies{
			ListPanes: func(_ context.Context, session string) ([]tmux.PaneActivity, error) {
				return getPanesWithActivity(session)
			},
			CapturePane: func(ctx context.Context, paneID string, _ int) (string, error) {
				return captureForHealthCheckWithCtx(ctx, paneID)
			},
		},
	)
	return &AgentMonitor{
		session:     session,
		projectKey:  projectKey,
		mailClient:  mailClient,
		detector:    detector,
		observer:    observer,
		activityMon: robot.NewActivityMonitor(nil),
	}
}

// ObserveSession returns the canonical point-in-time observation used by the
// coordinator. Individual capture failures are represented in the result.
func (m *AgentMonitor) ObserveSession(ctx context.Context) (status.SessionObservation, error) {
	if m.observer == nil {
		return status.SessionObservation{}, errors.New("session observer is not configured")
	}
	return m.observer.Observe(ctx, m.session)
}

// GetAgentStatus returns the current status of an agent pane.
func (m *AgentMonitor) GetAgentStatus(paneID, agentType string) AgentStatusResult {
	// This legacy method still performs its own capture.
	// For better performance, use GetAgentStatusWithOutput.
	result := AgentStatusResult{
		Status:       robot.StateUnknown,
		ContextUsage: 0,
		LastActivity: time.Time{},
		Freshness:    status.FreshnessUnavailable,
		Healthy:      false,
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
	result.ObservedAt = agentStatus.UpdatedAt
	result.Freshness = status.FreshnessFresh
	result.Confidence = monitorStateConfidence(agentStatus.State)
	result.Healthy = agentStatus.IsHealthy()
	result.ContextUsage = agentStatus.ContextUsage
	result.SafeToDispatch = result.Status == robot.StateWaiting && result.Confidence >= 0.75

	// Use activity monitor for velocity
	classifier := m.activityMon.GetOrCreate(paneID)
	classifier.SetAgentType(agentType)
	activity, err := classifier.Classify()
	if err == nil {
		result.Velocity = activity.Velocity
		if activity.State == robot.StateError {
			result.Status = robot.StateError
			result.Healthy = false
			result.SafeToDispatch = false
		}
	}

	return result
}

// GetAgentStatusWithOutput calculates status using provided output and metadata.
// This allows avoiding redundant tmux captures in the coordinator loop.
func (m *AgentMonitor) GetAgentStatusWithOutput(paneID, paneName, agentType string, output string, lastActivity time.Time) AgentStatusResult {
	result := AgentStatusResult{
		Status:       robot.StateUnknown,
		ContextUsage: 0,
		LastActivity: lastActivity,
		Freshness:    status.FreshnessFresh,
		Healthy:      false,
	}

	// 1. Static analysis using UnifiedDetector
	agentStatus := m.detector.Analyze(paneID, paneName, agentType, output, lastActivity)

	// Map status.AgentState to robot.AgentState
	result.Status = mapStatusToRobotState(agentStatus.State)
	result.ObservedAt = agentStatus.UpdatedAt
	result.Confidence = monitorStateConfidence(agentStatus.State)
	result.Healthy = agentStatus.IsHealthy()
	result.ContextUsage = agentStatus.ContextUsage
	result.SafeToDispatch = result.Status == robot.StateWaiting && result.Confidence >= 0.75

	// 2. Activity/Velocity analysis using ActivityMonitor
	classifier := m.activityMon.GetOrCreate(paneID)
	classifier.SetAgentType(agentType)

	// Inject the already-captured output
	activity, err := classifier.ClassifyWithOutput(output)
	if err == nil {
		result.Velocity = activity.Velocity
		// If activity monitor detects error state (via hysteresis etc), respect it
		if activity.State == robot.StateError {
			result.Status = robot.StateError
			result.Healthy = false
			result.SafeToDispatch = false
		}
	}

	return result
}

// GetAllAgentStatuses returns status for all agent panes in the session.
func (m *AgentMonitor) GetAllAgentStatuses() (map[string]AgentStatusResult, error) {
	observation, err := m.ObserveSession(context.Background())
	if err != nil {
		return nil, err
	}

	results := make(map[string]AgentStatusResult)
	for _, pane := range observation.Panes {
		if pane.AgentType == string(tmux.AgentUser) || pane.AgentType == string(tmux.AgentUnknown) {
			continue // Skip non-agent panes
		}
		results[pane.Pane.ID] = m.resultFromPaneObservation(pane)
	}

	return results, nil
}

func (m *AgentMonitor) resultFromPaneObservation(pane status.PaneObservation) AgentStatusResult {
	current := pane.Current
	result := AgentStatusResult{
		Status:         mapStatusToRobotState(current.Status.State),
		ContextUsage:   current.Status.ContextUsage,
		LastActivity:   current.Status.LastActive,
		ObservedAt:     current.ObservedAt,
		Freshness:      current.Freshness,
		Confidence:     current.Confidence,
		Healthy:        current.Freshness == status.FreshnessFresh && current.Status.IsHealthy(),
		SafeToDispatch: pane.SafeToDispatch(),
		ErrorMessage:   current.Error,
	}
	if pane.LastKnown != nil {
		result.LastKnownStatus = mapStatusToRobotState(pane.LastKnown.Status.State)
		result.LastKnownObservedAt = pane.LastKnown.ObservedAt
	}
	if current.Freshness == status.FreshnessFresh {
		classifier := m.activityMon.GetOrCreate(pane.Pane.ID)
		classifier.SetAgentType(pane.AgentType)
		if activity, err := classifier.ClassifyWithOutput(pane.RawOutput); err == nil {
			result.Velocity = activity.Velocity
		}
	}
	return result
}

func monitorStateConfidence(state status.AgentState) float64 {
	if state == status.StateUnknown {
		return 0.25
	}
	return 0.95
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

	reservations, err := m.mailClient.ListReservations(ctx, m.projectKey, agentMailName, false)
	if err != nil {
		return nil, err
	}

	return activeReservationPatterns(reservations, time.Now()), nil
}

// activeReservationPatterns returns every reservation that has not been
// explicitly released and is either known-future or has an unknown expiry.
// Unknown expiry must stay visible: the coordinator cannot safely claim an
// agent is unreserved merely because an Agent Mail timestamp was absent or
// could not be decoded.
func activeReservationPatterns(reservations []agentmail.FileReservation, now time.Time) []string {
	var patterns []string
	for _, r := range reservations {
		if reservationActiveAt(r, now) {
			patterns = append(patterns, r.PathPattern)
		}
	}
	return patterns
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
