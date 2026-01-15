// Package coordinator implements active session coordination for multi-agent workflows.
// It transforms the NTM session coordinator from a passive identity holder to an
// active coordinator that monitors agents, detects conflicts, and assigns work.
package coordinator

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SessionCoordinator manages agent coordination for a tmux session.
type SessionCoordinator struct {
	mu sync.RWMutex

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
	ctx    context.Context
	cancel context.CancelFunc
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

// New creates a new SessionCoordinator.
func New(session, projectKey string, mailClient *agentmail.Client, agentName string) *SessionCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionCoordinator{
		session:    session,
		agentName:  agentName,
		projectKey: projectKey,
		mailClient: mailClient,
		agents:     make(map[string]*AgentState),
		config:     DefaultCoordinatorConfig(),
		events:     make(chan CoordinatorEvent, 100),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// WithConfig sets the coordinator configuration.
func (c *SessionCoordinator) WithConfig(cfg CoordinatorConfig) *SessionCoordinator {
	c.config = cfg
	return c
}

// Start begins coordinator operations.
func (c *SessionCoordinator) Start(ctx context.Context) error {
	// Validate and fix interval configuration to prevent ticker panics.
	// time.NewTicker requires a positive duration.
	if c.config.PollInterval < MinPollInterval {
		c.config.PollInterval = DefaultCoordinatorConfig().PollInterval
	}
	if c.config.DigestInterval < MinDigestInterval {
		c.config.DigestInterval = DefaultCoordinatorConfig().DigestInterval
	}

	// Create derived context
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Initialize monitor
	c.monitor = NewAgentMonitor(c.session, c.mailClient, c.projectKey)

	// Start monitoring goroutine
	go c.monitorLoop()

	// Start digest goroutine if enabled
	if c.config.SendDigests {
		go c.digestLoop()
	}

	return nil
}

// Stop halts coordinator operations.
func (c *SessionCoordinator) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
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
func (c *SessionCoordinator) monitorLoop() {
	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()

	// Initial update
	c.updateAgentStates()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateAgentStates()
		}
	}
}

// updateAgentStates refreshes the state of all agents.
func (c *SessionCoordinator) updateAgentStates() {
	// Get panes from tmux
	panes, err := tmux.GetPanes(c.session)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Track which panes we've seen
	seenPanes := make(map[string]bool)

	for _, pane := range panes {
		seenPanes[pane.ID] = true

		// Detect agent type from pane title
		agentType := detectAgentType(pane.Title)
		if agentType == "" {
			continue // Not an agent pane
		}

		// Get or create agent state
		agent, exists := c.agents[pane.ID]
		if !exists {
			agent = &AgentState{
				PaneID:    pane.ID,
				PaneIndex: pane.Index,
				AgentType: agentType,
				Healthy:   true,
			}
			c.agents[pane.ID] = agent
		}

		// Update state using the monitor
		if c.monitor != nil {
			state := c.monitor.GetAgentStatus(pane.ID, agentType)
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

	switch {
	case agent.Status == robot.StateWaiting && prevStatus != robot.StateWaiting:
		eventType = EventAgentIdle
	case agent.Status == robot.StateGenerating || agent.Status == robot.StateThinking:
		eventType = EventAgentBusy
	case agent.Status == robot.StateError:
		eventType = EventAgentError
	case prevStatus == robot.StateError && agent.Status != robot.StateError:
		eventType = EventAgentRecovered
	default:
		return // No event for this transition
	}

	select {
	case c.events <- CoordinatorEvent{
		Type:      eventType,
		Timestamp: time.Now(),
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
}

// digestLoop periodically sends digest summaries.
func (c *SessionCoordinator) digestLoop() {
	ticker := time.NewTicker(c.config.DigestInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.SendDigest(c.ctx); err != nil {
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

// detectAgentType determines agent type from pane title.
func detectAgentType(title string) string {
	title = strings.ToLower(title)

	if strings.Contains(title, "claude") || strings.Contains(title, "cc") {
		return "cc"
	}
	if strings.Contains(title, "codex") || strings.Contains(title, "cod") {
		return "cod"
	}
	if strings.Contains(title, "gemini") || strings.Contains(title, "gmi") {
		return "gmi"
	}
	return ""
}
