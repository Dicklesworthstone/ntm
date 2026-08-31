package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	bindingsMu  sync.RWMutex
	boundPanes  map[string]struct{}
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
	monitor := &AgentMonitor{}
	observer := status.NewSessionObserverWithDependencies(
		detector,
		status.DefaultSessionObserverConfig(detector.Config()),
		status.SessionObserverDependencies{
			ListPanes: func(ctx context.Context, session string) ([]tmux.PaneActivity, error) {
				return monitor.listPanes(ctx, session)
			},
			CapturePane: func(ctx context.Context, paneID string, _ int) (string, error) {
				return captureForHealthCheckWithCtx(ctx, paneID)
			},
		},
	)
	monitor.session = session
	monitor.projectKey = projectKey
	monitor.mailClient = mailClient
	monitor.detector = detector
	monitor.observer = observer
	monitor.activityMon = robot.NewActivityMonitor(nil)
	return monitor
}

// SetPaneBindings installs the exact stable pane IDs authorized by bootstrap.
func (m *AgentMonitor) SetPaneBindings(bindings map[string]agentmail.CoordinatorPaneBinding) {
	m.bindingsMu.Lock()
	defer m.bindingsMu.Unlock()
	m.boundPanes = make(map[string]struct{}, len(bindings))
	for paneID := range bindings {
		m.boundPanes[paneID] = struct{}{}
	}
}

func (m *AgentMonitor) bindingSnapshot() map[string]struct{} {
	m.bindingsMu.RLock()
	defer m.bindingsMu.RUnlock()
	result := make(map[string]struct{}, len(m.boundPanes))
	for paneID := range m.boundPanes {
		result[paneID] = struct{}{}
	}
	return result
}

// listPanes observes the primary session and, only when an authoritative pane
// is absent there, discovers its owning session by tmux-global stable pane ID.
// The coordinator still validates the bound PID and project directory before
// admitting the resulting observation.
func (m *AgentMonitor) listPanes(ctx context.Context, session string) ([]tmux.PaneActivity, error) {
	primary, err := getPanesWithActivity(session)
	if err != nil {
		return nil, err
	}
	missing := m.bindingSnapshot()
	if len(missing) == 0 {
		return primary, nil
	}
	for _, pane := range primary {
		delete(missing, pane.Pane.ID)
	}
	if len(missing) == 0 {
		return primary, nil
	}

	bySession, err := getAllPanesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovering bound pane sessions: %w", err)
	}
	result := append([]tmux.PaneActivity(nil), primary...)
	for candidateSession, panes := range bySession {
		if candidateSession == session {
			continue
		}
		needed := false
		for _, pane := range panes {
			if _, ok := missing[pane.ID]; ok {
				needed = true
				break
			}
		}
		if !needed {
			continue
		}
		observed, observeErr := getPanesWithActivity(candidateSession)
		if observeErr != nil {
			return nil, fmt.Errorf("observing bound panes in session %q: %w", candidateSession, observeErr)
		}
		for _, pane := range observed {
			if _, ok := missing[pane.Pane.ID]; !ok {
				continue
			}
			result = append(result, pane)
			delete(missing, pane.Pane.ID)
		}
	}
	return result, nil
}

// ObserveSession returns the canonical point-in-time observation used by the
// coordinator. Individual capture failures are represented in the result.
func (m *AgentMonitor) ObserveSession(ctx context.Context) (status.SessionObservation, error) {
	if m.observer == nil {
		return status.SessionObservation{}, errors.New("session observer is not configured")
	}
	return m.observer.Observe(ctx, m.session)
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
