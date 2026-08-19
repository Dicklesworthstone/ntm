// Package coordinator implements active session coordination for multi-agent workflows.
// It transforms the NTM session coordinator from a passive identity holder to an
// active coordinator that monitors agents, detects conflicts, and assigns work.
package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
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
	mailClient        *agentmail.Client
	reservationClient coordinatorReservationClient

	atomicCoordinatorFactory    func(*assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator
	assignWorkFn                func(context.Context) ([]AssignmentResult, error)
	actionableRecommendationsFn func(context.Context, string, int) ([]bv.TriageRecommendation, error)
	workItemStatusFn            func(context.Context, string) (string, error)
	workItemDetailsFn           func(context.Context, string) (*bv.BeadAssignmentDetails, error)
	claimBeadForAssignmentFn    func(context.Context, string, string, string, []string) (bv.BeadClaimResult, error)
	releaseWorkItemClaimFn      func(context.Context, string, string, string) (bool, error)
	operatorGatedLabels         []string

	// Agent tracking
	agents     map[string]*AgentState
	lastUpdate time.Time

	// Monitors
	monitor *AgentMonitor

	// Context rotation trigger (bd-rpmg8). Nil unless
	// config.RotationUsageThreshold > 0; created lazily on the first cycle.
	rotation *rotationChecker

	// CAAM auto-failover trigger (bd-um3uy). Nil unless
	// [integrations.caam] auto_failover is true; created lazily on the
	// first cycle.
	caamFailover *failoverChecker

	// Agent Mail idle-pane nudge trigger (GH#231). Nil unless [coordinator]
	// mail_nudge is true AND an Agent Mail client exists; created lazily on
	// the first cycle.
	mailNudge *mailNudgeChecker

	// Conflict handling (bd-ws2-wire-or-delete-ykmcz.1). The detector is
	// created lazily on the first cycle where a conflict flag is enabled;
	// detectConflictsFn is a test seam. lastConflictOutcome implements the
	// per-conflicting-pair cooldown.
	conflictDetector    *ConflictDetector
	detectConflictsFn   func(context.Context) ([]Conflict, error)
	lastConflictOutcome map[string]time.Time

	// Configuration
	config CoordinatorConfig

	// Full NTM config, when the caller has one loaded. Only used by the
	// context rotation trigger (replacement launch commands and
	// [context_rotation] tunables); nil is fine.
	ntmConfig *config.Config

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
	PaneID                string                      `json:"pane_id"`
	PaneIndex             int                         `json:"pane_index"`
	AgentType             string                      `json:"agent_type"` // cc, cod, gmi
	AgentMailName         string                      `json:"agent_mail_name,omitempty"`
	Status                robot.AgentState            `json:"status"`
	LastKnownStatus       robot.AgentState            `json:"last_known_status,omitempty"`
	ContextUsage          float64                     `json:"context_usage"`
	LastActivity          time.Time                   `json:"last_activity"`
	ObservedAt            time.Time                   `json:"observed_at"`
	LastKnownObservedAt   time.Time                   `json:"last_known_observed_at,omitempty"`
	ObservationFreshness  status.ObservationFreshness `json:"observation_freshness"`
	ObservationConfidence float64                     `json:"observation_confidence"`
	ObservationError      string                      `json:"observation_error,omitempty"`
	SafeToDispatch        bool                        `json:"safe_to_dispatch"`
	CurrentTask           string                      `json:"current_task,omitempty"`
	Reservations          []string                    `json:"reservations,omitempty"`
	Healthy               bool                        `json:"healthy"`
	Profile               *persona.Persona            `json:"profile,omitempty"` // Agent's assigned profile for routing

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
	MailNudge   bool   `toml:"mail_nudge"`   // Prompt idle panes that have unread Agent Mail

	// Mail nudge tunables (GH#231). These mirror the [coordinator]
	// nudge_cooldown_seconds / nudge_message config keys; the CLI populates
	// them from config.Coordinator when starting a coordinator. A
	// non-positive cooldown falls back to the fixed 60s default; an empty
	// message uses the built-in nudge prompt.
	NudgeCooldown time.Duration `toml:"-"`
	NudgeMessage  string        `toml:"-"`

	// Context rotation trigger (bd-rpmg8). These mirror the [rotation]
	// usage_percent_threshold / auto_confirm config keys (NOT [coordinator]);
	// the CLI populates them from config.Rotation when starting a coordinator.
	// Zero threshold (the default) keeps the trigger fully off: no transcript
	// probing, no new subprocess calls, no behavior change.
	RotationUsageThreshold float64 `toml:"-"`
	RotationAutoConfirm    bool    `toml:"-"`
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
		MailNudge:         false, // Disabled by default - prompts a pane
		NudgeCooldown:     defaultMailNudgeCooldown,
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
	// EventConflictReleaseRequested marks a negotiation request SENT: the
	// engine asked the losing holder to release. It is NOT proof the holder
	// complied — the reservation may stay held. The old name for this event
	// ("conflict_resolved") over-claimed exactly that, so it was renamed
	// (bd-izuqq.2); nothing currently verifies an actual release, and no
	// event asserts one.
	EventConflictReleaseRequested CoordinatorEventType = "conflict_release_requested"
	EventWorkAssigned             CoordinatorEventType = "work_assigned"
	EventDigestSent               CoordinatorEventType = "digest_sent"
	EventDigestFailed             CoordinatorEventType = "digest_failed"
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
	c := &SessionCoordinator{
		session:             session,
		agentName:           agentName,
		projectKey:          projectKey,
		mailClient:          mailClient,
		reservationClient:   mailClient,
		operatorGatedLabels: append([]string(nil), bv.OperatorGatedLabelsForProject(projectKey)...),
		agents:              make(map[string]*AgentState),
		config:              DefaultCoordinatorConfig(),
		events:              make(chan CoordinatorEvent, 100),
		stopCh:              make(chan struct{}),
	}
	return c
}

// WithConfig sets the coordinator configuration.
func (c *SessionCoordinator) WithConfig(cfg CoordinatorConfig) *SessionCoordinator {
	c.config = cfg
	return c
}

// WithNTMConfig provides the loaded NTM config for subsystems that need more
// than CoordinatorConfig (currently only the context rotation trigger). Nil
// is accepted and keeps built-in defaults.
func (c *SessionCoordinator) WithNTMConfig(cfg *config.Config) *SessionCoordinator {
	c.ntmConfig = cfg
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

	// Perform initial update synchronously to ensure state is ready. A coordinator
	// that cannot observe its target session must not announce a healthy runtime.
	if err := c.updateAgentStatesContext(ctx); err != nil {
		c.mu.Lock()
		c.started = false
		c.stopCh = nil
		c.mu.Unlock()
		return fmt.Errorf("initial coordinator observation: %w", err)
	}

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

// GetIdleAgents returns agents that are idle and available for work.
func (c *SessionCoordinator) GetIdleAgents() []*AgentState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var idle []*AgentState
	now := time.Now()
	for _, agent := range c.agents {
		if agent.Status == robot.StateWaiting && agent.Healthy && agent.SafeToDispatch &&
			agent.ObservationFreshness == status.FreshnessFresh &&
			status.DispatchObservationIsCurrent(agent.ObservedAt, now) {
			if time.Since(agent.LastActivity).Seconds() >= c.config.IdleThreshold {
				agentCopy := *agent
				idle = append(idle, &agentCopy)
			}
		}
	}
	return idle
}

// getAssignmentCandidates applies the configured queueing policy while always
// requiring a current, healthy observation. When AssignOnlyIdle is false, busy
// agents may receive queued Agent Mail work, but stale/error panes remain
// ineligible.
func (c *SessionCoordinator) getAssignmentCandidates() []*AgentState {
	if c.config.AssignOnlyIdle {
		return c.GetIdleAgents()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	candidates := make([]*AgentState, 0, len(c.agents))
	for _, agent := range c.agents {
		if agent == nil || !agent.Healthy || agent.ObservationFreshness != status.FreshnessFresh ||
			!status.DispatchObservationIsCurrent(agent.ObservedAt, now) || agent.Status == robot.StateError || agent.Status == robot.StateUnknown {
			continue
		}
		agentCopy := *agent
		candidates = append(candidates, &agentCopy)
	}
	return candidates
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
			results, err := c.RunCycle(ctx)
			c.reportAssignmentCycle(results, err)
		}
	}
}

// RunCycle refreshes the canonical session observation and, when enabled,
// performs one auto-assignment pass from that exact fresh snapshot.
func (c *SessionCoordinator) RunCycle(ctx context.Context) ([]AssignmentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.monitor == nil {
		c.monitor = NewAgentMonitor(c.session, c.mailClient, c.projectKey)
	}
	if err := c.updateAgentStatesContext(ctx); err != nil {
		return nil, err
	}
	c.maybeCheckMailNudge(ctx)
	c.maybeCheckContextRotation(ctx)
	c.maybeCheckCaamFailover(ctx)
	// Conflict detection + negotiation behind the persisted flags
	// (bd-ws2-wire-or-delete-ykmcz.1): runs before the AutoAssign early
	// return so notify/negotiate work even when auto-assignment is off.
	c.runConflictCycle(ctx)
	if !c.config.AutoAssign {
		return nil, nil
	}
	assignWork := c.AssignWork
	if c.assignWorkFn != nil {
		assignWork = c.assignWorkFn
	}
	return assignWork(ctx)
}

// maybeCheckContextRotation runs the transcript-usage rotation trigger for
// this cycle (bd-rpmg8). DEFAULT-OFF GUARANTEE: with no [rotation]
// usage_percent_threshold configured (> 0), this returns before constructing
// the checker — zero new subprocess calls and zero behavior change.
func (c *SessionCoordinator) maybeCheckContextRotation(ctx context.Context) {
	c.mu.Lock()
	if c.config.RotationUsageThreshold <= 0 {
		c.mu.Unlock()
		return
	}
	if c.rotation == nil {
		c.rotation = newRotationChecker(c.session, c.projectKey, c.config, c.ntmConfig)
	}
	checker := c.rotation
	c.mu.Unlock()

	checker.runOnce(ctx)
}

// maybeCheckCaamFailover runs the CAAM auto-failover trigger for this cycle
// (bd-um3uy). DEFAULT-OFF GUARANTEE: with [integrations.caam] auto_failover
// unset (false) — or no loaded NTM config at all — this returns before
// constructing the checker: zero caam probing, zero new subprocess calls,
// zero behavior change.
func (c *SessionCoordinator) maybeCheckCaamFailover(ctx context.Context) {
	c.mu.Lock()
	if c.ntmConfig == nil || !c.ntmConfig.Integrations.CAAM.AutoFailover {
		c.mu.Unlock()
		return
	}
	if c.caamFailover == nil {
		c.caamFailover = newFailoverChecker(c.session, c.projectKey, c.ntmConfig.Integrations.CAAM)
	}
	checker := c.caamFailover
	c.mu.Unlock()

	checker.runOnce(ctx)
}

// maybeCheckMailNudge runs the Agent Mail idle-pane nudge trigger for this
// cycle (GH#231). DEFAULT-OFF GUARANTEE: with [coordinator] mail_nudge unset
// (false) — or no Agent Mail client at all — this returns before
// constructing the checker: zero inbox probing, zero new subprocess calls,
// zero behavior change.
func (c *SessionCoordinator) maybeCheckMailNudge(ctx context.Context) {
	c.mu.Lock()
	if !c.config.MailNudge || c.mailClient == nil {
		c.mu.Unlock()
		return
	}
	if c.mailNudge == nil {
		c.mailNudge = newMailNudgeChecker(c.session, c.projectKey, c.config, c.mailClient)
	}
	checker := c.mailNudge
	c.mu.Unlock()

	checker.runOnce(ctx)
}

func (c *SessionCoordinator) reportAssignmentCycle(results []AssignmentResult, err error) {
	if err != nil {
		if assignmentCycleErrorIsExpectedShutdown(err) {
			return
		}
		slog.Warn("coordinator auto-assignment failed", "session", c.session, "error", err)
		return
	}
	for _, result := range results {
		if !result.Success {
			beadID := ""
			if result.Assignment != nil {
				beadID = result.Assignment.BeadID
			}
			slog.Warn("coordinator work assignment failed", "session", c.session, "bead_id", beadID, "error", result.Error)
		}
	}
}

func assignmentCycleErrorIsExpectedShutdown(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !assignmentCycleErrorIsExpectedShutdown(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		child := wrapped.Unwrap()
		if child == nil {
			return false
		}
		return assignmentCycleErrorIsExpectedShutdown(child)
	}
	return errors.Is(err, context.Canceled)
}

func coordinatorPaneObservationError(paneID, message string) error {
	cause := error(errors.New(message))
	if message == context.Canceled.Error() {
		cause = context.Canceled
	}
	return fmt.Errorf("pane %s: %w", paneID, cause)
}

func (c *SessionCoordinator) updateAgentStatesContext(ctx context.Context) error {
	if c.monitor == nil {
		return errors.New("agent monitor is not configured")
	}
	observation, err := c.monitor.ObserveSession(ctx)
	if err != nil {
		c.markAgentObservationsUnavailable(observation.ObservedAt, err)
		return err
	}

	type agentUpdate struct {
		paneID    string
		paneTitle string
		paneIndex int
		agentType string
		status    AgentStatusResult
	}
	updates := make([]agentUpdate, 0, len(observation.Panes))
	var observationErrors []error
	for _, pane := range observation.Panes {
		if pane.AgentType == string(tmux.AgentUser) || pane.AgentType == string(tmux.AgentUnknown) {
			continue
		}
		state := c.monitor.resultFromPaneObservation(pane)
		updates = append(updates, agentUpdate{
			paneID:    pane.Pane.ID,
			paneTitle: pane.Metadata.Title,
			paneIndex: pane.Pane.PaneIndex,
			agentType: pane.AgentType,
			status:    state,
		})
		if state.Freshness != status.FreshnessFresh || state.ErrorMessage != "" {
			message := state.ErrorMessage
			if message == "" {
				message = fmt.Sprintf("observation freshness is %s", state.Freshness)
			}
			observationErrors = append(observationErrors, coordinatorPaneObservationError(pane.Pane.ID, message))
		}
	}
	registry, registryErr := agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
	if registryErr != nil {
		slog.Warn("coordinator could not load Agent Mail pane registry", "session", c.session, "error", registryErr)
	}

	c.mu.Lock()

	// Track which panes we've seen
	seenPanes := make(map[string]bool, len(updates))
	for _, update := range updates {
		seenPanes[update.paneID] = true
	}

	type transitionEvent struct {
		agent      *AgentState
		prevStatus robot.AgentState
	}
	var transitions []transitionEvent

	for _, update := range updates {
		// Get or create agent state
		agent, exists := c.agents[update.paneID]
		if !exists {
			agent = &AgentState{
				PaneID:    update.paneID,
				PaneIndex: update.paneIndex,
				AgentType: update.agentType,
			}
			c.agents[update.paneID] = agent
		}

		// Update state using pre-calculated status
		state := update.status
		prevStatus := agent.Status
		if state.LastKnownStatus == "" {
			switch {
			case agent.LastKnownStatus != "":
				state.LastKnownStatus = agent.LastKnownStatus
				state.LastKnownObservedAt = agent.LastKnownObservedAt
			case state.Freshness != status.FreshnessFresh && agent.ObservationFreshness == status.FreshnessFresh && agent.Status != robot.StateUnknown:
				state.LastKnownStatus = agent.Status
				state.LastKnownObservedAt = agent.ObservedAt
			}
		}
		agent.Status = state.Status
		agent.ContextUsage = state.ContextUsage
		agent.LastActivity = state.LastActivity
		agent.ObservedAt = state.ObservedAt
		agent.LastKnownStatus = state.LastKnownStatus
		agent.LastKnownObservedAt = state.LastKnownObservedAt
		agent.ObservationFreshness = state.Freshness
		agent.ObservationConfidence = state.Confidence
		agent.ObservationError = state.ErrorMessage
		agent.SafeToDispatch = state.SafeToDispatch
		agent.Healthy = state.Healthy
		if registry != nil {
			if agentName, ok := registry.GetAgent(update.paneTitle, update.paneID); ok {
				agent.AgentMailName = agentName
			} else {
				agent.AgentMailName = ""
			}
		}

		// Track events for state transitions
		if exists && prevStatus != agent.Status {
			// Copy agent state for safe event emission outside lock
			agentCopy := *agent
			transitions = append(transitions, transitionEvent{
				agent:      &agentCopy,
				prevStatus: prevStatus,
			})
		}
	}

	// Remove agents that no longer exist
	for paneID := range c.agents {
		if !seenPanes[paneID] {
			delete(c.agents, paneID)
		}
	}

	c.lastUpdate = observation.ObservedAt
	c.mu.Unlock()

	// Emit events outside the lock to prevent deadlocks if the event bus
	// applies backpressure and blocks on the handler semaphore.
	for _, transition := range transitions {
		c.emitEvent(transition.agent, transition.prevStatus)
	}
	return errors.Join(observationErrors...)
}

// markAgentObservationsUnavailable makes a whole-session observation failure
// visible without stamping the prior state as current. lastUpdate intentionally
// remains the time of the last completed topology observation.
func (c *SessionCoordinator) markAgentObservationsUnavailable(observedAt time.Time, observationErr error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, agent := range c.agents {
		if agent.ObservationFreshness == status.FreshnessFresh && agent.Status != robot.StateUnknown {
			agent.LastKnownStatus = agent.Status
			agent.LastKnownObservedAt = agent.ObservedAt
		}
		agent.Status = robot.StateUnknown
		agent.ObservedAt = observedAt
		agent.ObservationFreshness = status.FreshnessUnavailable
		agent.ObservationConfidence = 0
		agent.ObservationError = observationErr.Error()
		agent.SafeToDispatch = false
		agent.Healthy = false
	}
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
