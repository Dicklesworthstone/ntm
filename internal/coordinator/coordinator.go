// Package coordinator implements active session coordination for multi-agent workflows.
// It transforms the NTM session coordinator from a passive identity holder to an
// active coordinator that monitors agents, detects conflicts, and assigns work.
package coordinator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	getPanesWithActivity         = tmux.GetPanesWithActivity
	captureForHealthCheckWithCtx = tmux.CaptureForHealthCheckContext
)

// SessionCoordinator manages agent coordination for a tmux session.
type SessionCoordinator struct {
	mu          sync.RWMutex
	lifecycleMu sync.Mutex

	// Identity
	session    string // tmux session name
	agentName  string // Agent Mail identity (e.g., "OrangeFox")
	projectKey string // Absolute path to working directory

	// Clients
	mailClient *agentmail.Client

	// Agent tracking
	agents     map[string]*AgentState
	lastUpdate time.Time

	// Monitors
	monitor *AgentMonitor

	// Configuration
	config CoordinatorConfig

	// Event channel for coordination actions
	events chan CoordinatorEvent

	// Control
	stopCh   chan struct{}
	wg       sync.WaitGroup
	started  bool
	stopping bool
}

// AgentState tracks the current state of an agent pane.
type AgentState struct {
	PaneID        string           `json:"pane_id"`
	PaneIndex     int              `json:"pane_index"`
	AgentType     string           `json:"agent_type"` // cc, cod, gmi
	AgentMailName string           `json:"agent_mail_name,omitempty"`
	Status        robot.AgentState `json:"status"`
	ContextUsage  float64          `json:"context_usage"`
	LastActivity  time.Time        `json:"last_activity"`
	CurrentTask   string           `json:"current_task,omitempty"`
	Reservations  []string         `json:"reservations,omitempty"`
	Healthy       bool             `json:"healthy"`
	Profile       *persona.Persona `json:"profile,omitempty"` // Agent's assigned profile for routing

	// Assignment tracking (from bd-1g5t8)
	// Assignments is the count of active assignments for this agent.
	// A value of -1 indicates tracking data is unavailable (fallback mode).
	Assignments    int       `json:"assignments"`
	LastAssignedAt time.Time `json:"last_assigned_at,omitempty"` // When this agent was last assigned work
}

// CoordinatorConfig holds configuration for the coordinator.
type CoordinatorConfig struct {
	// Monitoring
	PollInterval   time.Duration `toml:"poll_interval"`   // How often to poll agent status (default: 5s)
	DigestInterval time.Duration `toml:"digest_interval"` // How often to send digests (default: 5m)

	// Work assignment
	AutoAssign     bool    `toml:"auto_assign"`      // Automatically assign work to idle agents
	IdleThreshold  float64 `toml:"idle_threshold"`   // Seconds of inactivity before considering idle
	AssignOnlyIdle bool    `toml:"assign_only_idle"` // Only assign to truly idle agents

	// Conflict handling
	ConflictNotify    bool `toml:"conflict_notify"`    // Notify when conflicts detected
	ConflictNegotiate bool `toml:"conflict_negotiate"` // Attempt automatic conflict resolution

	// Agent Mail
	SendDigests bool   `toml:"send_digests"` // Send periodic digests to human
	HumanAgent  string `toml:"human_agent"`  // Agent name to send digests to (default: "Human")
}

// MinPollInterval is the minimum allowed poll interval to prevent ticker panics.
// time.NewTicker requires a positive duration.
const MinPollInterval = 100 * time.Millisecond

// MinDigestInterval is the minimum allowed digest interval.
const MinDigestInterval = 10 * time.Second

// DefaultCoordinatorConfig returns sensible defaults.
func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		PollInterval:      5 * time.Second,
		DigestInterval:    5 * time.Minute,
		AutoAssign:        false, // Disabled by default - opt-in
		IdleThreshold:     30.0,
		AssignOnlyIdle:    true,
		ConflictNotify:    true,
		ConflictNegotiate: false, // Manual resolution by default
		SendDigests:       false, // Disabled by default
		HumanAgent:        "Human",
	}
}

// CoordinatorEventType represents types of coordinator events.
type CoordinatorEventType string

const (
	EventAgentIdle        CoordinatorEventType = "agent_idle"
	EventAgentBusy        CoordinatorEventType = "agent_busy"
	EventAgentError       CoordinatorEventType = "agent_error"
	EventAgentRecovered   CoordinatorEventType = "agent_recovered"
	EventConflictDetected CoordinatorEventType = "conflict_detected"
	EventConflictResolved CoordinatorEventType = "conflict_resolved"
	EventWorkAssigned     CoordinatorEventType = "work_assigned"
	EventDigestSent       CoordinatorEventType = "digest_sent"
	EventDigestFailed     CoordinatorEventType = "digest_failed"
)

// CoordinatorEvent represents an event from the coordinator.
type CoordinatorEvent struct {
	Type      CoordinatorEventType `json:"type"`
	Timestamp time.Time            `json:"timestamp"`
	AgentID   string               `json:"agent_id,omitempty"`
	Details   map[string]any       `json:"details,omitempty"`
}

type busCoordinatorEvent struct {
	events.BaseEvent
	AgentID  string         `json:"agent_id,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
	PrevType string         `json:"prev_status,omitempty"`
	NewType  string         `json:"new_status,omitempty"`
}

// New creates a new SessionCoordinator.
func New(session, projectKey string, mailClient *agentmail.Client, agentName string) *SessionCoordinator {
	return &SessionCoordinator{
		session:    session,
		agentName:  agentName,
		projectKey: projectKey,
		mailClient: mailClient,
		agents:     make(map[string]*AgentState),
		config:     DefaultCoordinatorConfig(),
		events:     make(chan CoordinatorEvent, 100),
		stopCh:     make(chan struct{}),
	}
}

// WithConfig sets the coordinator configuration.
func (c *SessionCoordinator) WithConfig(cfg CoordinatorConfig) *SessionCoordinator {
	c.config = cfg
	return c
}

// Start begins coordinator operations.
func (c *SessionCoordinator) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate and fix interval configuration to prevent ticker panics.
	// time.NewTicker requires a positive duration.
	if c.config.PollInterval < MinPollInterval {
		c.config.PollInterval = DefaultCoordinatorConfig().PollInterval
	}
	if c.config.DigestInterval < MinDigestInterval {
		c.config.DigestInterval = DefaultCoordinatorConfig().DigestInterval
	}

	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	if c.started || c.stopping {
		c.mu.Unlock()
		return errors.New("coordinator already started")
	}

	stopCh := make(chan struct{})
	c.stopCh = stopCh
	c.started = true
	c.stopping = false
	c.mu.Unlock()

	// Initialize monitor
	c.monitor = NewAgentMonitor(c.session, c.mailClient, c.projectKey)

	// Perform initial update synchronously to ensure state is ready
	c.updateAgentStates()

	// Start monitoring goroutine
	c.wg.Add(1)
	go c.monitorLoop(ctx, stopCh)

	// Start digest goroutine if enabled
	if c.config.SendDigests {
		c.wg.Add(1)
		go c.digestLoop(ctx, stopCh)
	}

	return nil
}

// Stop halts coordinator operations.
func (c *SessionCoordinator) Stop() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	if !c.started || c.stopping {
		c.mu.Unlock()
		return
	}
	stopCh := c.stopCh
	c.started = false
	c.stopping = true
	c.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	c.wg.Wait()

	c.mu.Lock()
	c.stopCh = nil
	c.stopping = false
	c.mu.Unlock()
}

// Events returns the event channel for external listeners.
func (c *SessionCoordinator) Events() <-chan CoordinatorEvent {
	return c.events
}

// GetAgents returns the current state of all tracked agents.
func (c *SessionCoordinator) GetAgents() map[string]*AgentState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]*AgentState, len(c.agents))
	for k, v := range c.agents {
		agentCopy := *v
		result[k] = &agentCopy
	}
	return result
}

// GetAgentByPaneID returns the state of a specific agent.
func (c *SessionCoordinator) GetAgentByPaneID(paneID string) *AgentState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if agent, ok := c.agents[paneID]; ok {
		agentCopy := *agent
		return &agentCopy
	}
	return nil
}

// GetIdleAgents returns agents that are idle and available for work.
func (c *SessionCoordinator) GetIdleAgents() []*AgentState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var idle []*AgentState
	for _, agent := range c.agents {
		if agent.Status == robot.StateWaiting && agent.Healthy {
			if time.Since(agent.LastActivity).Seconds() >= c.config.IdleThreshold {
				agentCopy := *agent
				idle = append(idle, &agentCopy)
			}
		}
	}
	return idle
}

// monitorLoop periodically updates agent states.
func (c *SessionCoordinator) monitorLoop(ctx context.Context, stopCh <-chan struct{}) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
			c.updateAgentStates()
		}
	}
}

// updateAgentStates refreshes the state of all agents.
func (c *SessionCoordinator) updateAgentStates() {
	// 1. Get panes with activity from tmux (single call)
	// This returns both pane metadata and last activity timestamp
	panes, err := getPanesWithActivity(c.session)
	if err != nil {
		return
	}

	// Filter for agent panes to capture
	var agentPanes []tmux.PaneActivity
	for _, p := range panes {
		if p.Pane.Type != tmux.AgentUser && p.Pane.Type != tmux.AgentUnknown {
			agentPanes = append(agentPanes, p)
		}
	}

	// 2. Parallel capture of pane outputs
	// We use HealthCheck (50 lines) to provide enough context for both
	// the UnifiedDetector (patterns) and ActivityMonitor (velocity).
	type captureResult struct {
		paneID string
		output string
		err    error
	}

	resultsCh := make(chan captureResult, len(agentPanes))
	var wg sync.WaitGroup

	for _, p := range agentPanes {
		wg.Add(1)
		go func(paneID string) {
			defer wg.Done()
			// Short timeout for capture to prevent holding up the cycle
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			output, err := captureForHealthCheckWithCtx(ctx, paneID)
			resultsCh <- captureResult{paneID: paneID, output: output, err: err}
		}(p.Pane.ID)
	}

	wg.Wait()
	close(resultsCh)

	outputs := make(map[string]string)
	for res := range resultsCh {
		if res.err == nil {
			outputs[res.paneID] = res.output
		}
	}

	// 3. Calculate status updates (CPU bound, fast)
	type agentUpdate struct {
		paneID    string
		paneIndex int
		agentType string
		status    AgentStatusResult
	}

	var updates []agentUpdate
	if c.monitor != nil {
		updates = make([]agentUpdate, 0, len(agentPanes))
		for _, p := range agentPanes {
			output, ok := outputs[p.Pane.ID]
			if !ok {
				continue // Skip if capture failed
			}

			// Use the optimized method that accepts pre-captured output and activity
			state := c.monitor.GetAgentStatusWithOutput(
				p.Pane.ID,
				p.Pane.Title,
				string(p.Pane.Type),
				output,
				p.LastActivity,
			)

			updates = append(updates, agentUpdate{
				paneID:    p.Pane.ID,
				paneIndex: p.Pane.Index,
				agentType: string(p.Pane.Type),
				status:    state,
			})
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Track which panes we've seen
	seenPanes := make(map[string]bool, len(agentPanes))
	for _, pane := range agentPanes {
		seenPanes[pane.Pane.ID] = true
	}

	for _, update := range updates {
		// Get or create agent state
		agent, exists := c.agents[update.paneID]
		if !exists {
			agent = &AgentState{
				PaneID:    update.paneID,
				PaneIndex: update.paneIndex,
				AgentType: update.agentType,
				Healthy:   true,
			}
			c.agents[update.paneID] = agent
		}

		// Update state using pre-calculated status
		state := update.status
		prevStatus := agent.Status
		agent.Status = state.Status
		agent.ContextUsage = state.ContextUsage
		agent.LastActivity = state.LastActivity
		agent.Healthy = state.Healthy

		// Emit events for state transitions
		if exists && prevStatus != agent.Status {
			c.emitEvent(agent, prevStatus)
		}
	}

	// Remove agents that no longer exist
	for paneID := range c.agents {
		if !seenPanes[paneID] {
			delete(c.agents, paneID)
		}
	}

	c.lastUpdate = time.Now()
}

// emitEvent sends a coordinator event based on state transition.
func (c *SessionCoordinator) emitEvent(agent *AgentState, prevStatus robot.AgentState) {
	var eventType CoordinatorEventType

	var busType string
	switch {
	case prevStatus == robot.StateError && agent.Status != robot.StateError:
		eventType = EventAgentRecovered
		busType = "agent.recovered"
	case agent.Status == robot.StateWaiting && prevStatus != robot.StateWaiting:
		eventType = EventAgentIdle
		busType = "agent.idle"
	case agent.Status == robot.StateGenerating || agent.Status == robot.StateThinking:
		eventType = EventAgentBusy
		busType = "agent.busy"
	case agent.Status == robot.StateError:
		eventType = EventAgentError
		busType = "agent.error"
	default:
		return // No event for this transition
	}

	now := time.Now().UTC()
	details := map[string]any{
		"agent_type": agent.AgentType,
		"pane_index": agent.PaneIndex,
	}

	select {
	case c.events <- CoordinatorEvent{
		Type:      eventType,
		Timestamp: now,
		AgentID:   agent.PaneID,
		Details: map[string]any{
			"agent_type":  agent.AgentType,
			"prev_status": string(prevStatus),
			"new_status":  string(agent.Status),
			"pane_index":  agent.PaneIndex,
		},
	}:
	default:
		// Channel full, drop event
	}

	events.Publish(busCoordinatorEvent{
		BaseEvent: events.BaseEvent{
			Type:      busType,
			Timestamp: now,
			Session:   c.session,
		},
		AgentID:  agent.PaneID,
		Details:  details,
		PrevType: string(prevStatus),
		NewType:  string(agent.Status),
	})
}

// digestLoop periodically sends digest summaries.
func (c *SessionCoordinator) digestLoop(ctx context.Context, stopCh <-chan struct{}) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.config.DigestInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
			if err := c.SendDigest(ctx); err != nil {
				// Emit event for observability - don't stop the loop
				select {
				case c.events <- CoordinatorEvent{
					Type:      EventDigestFailed,
					Timestamp: time.Now(),
					Details: map[string]any{
						"error": err.Error(),
					},
				}:
				default:
				}
			}
		}
	}
}
