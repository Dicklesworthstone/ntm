package events

import (
	"container/ring"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// BusEvent is the interface that all bus events must implement
type BusEvent interface {
	EventType() string
	EventTimestamp() time.Time
	EventSession() string
}

// EventHandler is a callback function for event subscriptions
type EventHandler func(BusEvent)

// UnsubscribeFunc is returned from Subscribe and can be called to unsubscribe
type UnsubscribeFunc func()

// handlerEntry wraps a handler with a unique ID for safe unsubscription
type handlerEntry struct {
	id      uint64
	handler EventHandler
}

// EventBus provides a centralized pub/sub system for NTM events
type EventBus struct {
	subscribers map[string][]handlerEntry
	nextID      atomic.Uint64
	mu          sync.RWMutex
	history     *ring.Ring
	historySize int
	historyMu   sync.RWMutex
}

// NewEventBus creates a new event bus with the specified history size
func NewEventBus(historySize int) *EventBus {
	if historySize < 1 {
		historySize = 100 // Default history size
	}
	return &EventBus{
		subscribers: make(map[string][]handlerEntry),
		history:     ring.New(historySize),
		historySize: historySize,
	}
}

// DefaultBus is the global default event bus
var DefaultBus = NewEventBus(100)

// Subscribe registers a handler for a specific event type
// Returns an unsubscribe function
func (b *EventBus) Subscribe(eventType string, handler EventHandler) UnsubscribeFunc {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID.Add(1)
	entry := handlerEntry{id: id, handler: handler}
	b.subscribers[eventType] = append(b.subscribers[eventType], entry)

	// Return unsubscribe function that finds handler by ID
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		handlers := b.subscribers[eventType]
		for i, h := range handlers {
			if h.id == id {
				// Remove handler by replacing with last and truncating
				handlers[i] = handlers[len(handlers)-1]
				b.subscribers[eventType] = handlers[:len(handlers)-1]
				return
			}
		}
	}
}

// SubscribeAll registers a handler for all events (wildcard)
func (b *EventBus) SubscribeAll(handler EventHandler) UnsubscribeFunc {
	return b.Subscribe("*", handler)
}

// Publish sends an event to all matching subscribers
func (b *EventBus) Publish(event BusEvent) {
	// Add to history first
	b.historyMu.Lock()
	b.history.Value = event
	b.history = b.history.Next()
	b.historyMu.Unlock()

	// Get handlers under read lock
	b.mu.RLock()
	eventType := event.EventType()
	entries := make([]handlerEntry, 0, len(b.subscribers[eventType])+len(b.subscribers["*"]))
	entries = append(entries, b.subscribers[eventType]...)
	entries = append(entries, b.subscribers["*"]...)
	b.mu.RUnlock()

	// Call handlers outside of lock
	for _, entry := range entries {
		// Run handler in goroutine for non-blocking publish
		go func(h EventHandler) {
			h(event)
		}(entry.handler)
	}
}

// PublishSync sends an event and waits for all handlers to complete
func (b *EventBus) PublishSync(event BusEvent) {
	// Add to history first
	b.historyMu.Lock()
	b.history.Value = event
	b.history = b.history.Next()
	b.historyMu.Unlock()

	// Get handlers under read lock
	b.mu.RLock()
	eventType := event.EventType()
	entries := make([]handlerEntry, 0, len(b.subscribers[eventType])+len(b.subscribers["*"]))
	entries = append(entries, b.subscribers[eventType]...)
	entries = append(entries, b.subscribers["*"]...)
	b.mu.RUnlock()

	// Call handlers synchronously
	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(h EventHandler) {
			defer wg.Done()
			h(event)
		}(entry.handler)
	}
	wg.Wait()
}

// History returns recent events (newest first)
func (b *EventBus) History(limit int) []BusEvent {
	if limit <= 0 || limit > b.historySize {
		limit = b.historySize
	}

	b.historyMu.RLock()
	defer b.historyMu.RUnlock()

	events := make([]BusEvent, 0, limit)
	// Walk backward through ring to get newest first
	r := b.history.Prev()
	for i := 0; i < limit; i++ {
		if r.Value != nil {
			if event, ok := r.Value.(BusEvent); ok {
				events = append(events, event)
			}
		}
		r = r.Prev()
	}
	return events
}

// EnableRobotMode enables JSON streaming of all events to a writer
func (b *EventBus) EnableRobotMode(w io.Writer) UnsubscribeFunc {
	enc := json.NewEncoder(w)
	return b.SubscribeAll(func(e BusEvent) {
		enc.Encode(e)
	})
}

// SubscriberCount returns the number of subscribers for an event type
func (b *EventBus) SubscriberCount(eventType string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[eventType])
}

// ----------------------------------------------------------------
// Base Event Implementation
// ----------------------------------------------------------------

// BaseEvent provides common fields for all events
type BaseEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session,omitempty"`
}

// EventType returns the event type
func (e BaseEvent) EventType() string { return e.Type }

// EventTimestamp returns the event timestamp
func (e BaseEvent) EventTimestamp() time.Time { return e.Timestamp }

// EventSession returns the session name
func (e BaseEvent) EventSession() string { return e.Session }

// ----------------------------------------------------------------
// Profile Events
// ----------------------------------------------------------------

// ProfileAssignedEvent is emitted when a profile is assigned to an agent
type ProfileAssignedEvent struct {
	BaseEvent
	AgentID  string `json:"agent_id"`
	Profile  string `json:"profile"`
	Previous string `json:"previous,omitempty"` // Empty if new
}

// NewProfileAssignedEvent creates a new profile assigned event
func NewProfileAssignedEvent(session, agentID, profile, previous string) ProfileAssignedEvent {
	return ProfileAssignedEvent{
		BaseEvent: BaseEvent{
			Type:      "profile_assigned",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:  agentID,
		Profile:  profile,
		Previous: previous,
	}
}

// ProfileSwitchedEvent is emitted when an agent's profile is changed
type ProfileSwitchedEvent struct {
	BaseEvent
	AgentID    string `json:"agent_id"`
	OldProfile string `json:"old_profile"`
	NewProfile string `json:"new_profile"`
}

// NewProfileSwitchedEvent creates a new profile switched event
func NewProfileSwitchedEvent(session, agentID, oldProfile, newProfile string) ProfileSwitchedEvent {
	return ProfileSwitchedEvent{
		BaseEvent: BaseEvent{
			Type:      "profile_switched",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:    agentID,
		OldProfile: oldProfile,
		NewProfile: newProfile,
	}
}

// ----------------------------------------------------------------
// Context Rotation Events
// ----------------------------------------------------------------

// ContextWarningEvent is emitted when context usage approaches threshold
type ContextWarningEvent struct {
	BaseEvent
	AgentID       string  `json:"agent_id"`
	UsagePercent  float64 `json:"usage_percent"`
	EstimatedRoom int64   `json:"estimated_room"` // Tokens remaining
}

// NewContextWarningEvent creates a new context warning event
func NewContextWarningEvent(session, agentID string, usagePercent float64, estimatedRoom int64) ContextWarningEvent {
	return ContextWarningEvent{
		BaseEvent: BaseEvent{
			Type:      "context_warning",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:       agentID,
		UsagePercent:  usagePercent,
		EstimatedRoom: estimatedRoom,
	}
}

// RotationStartedEvent is emitted when agent rotation begins
type RotationStartedEvent struct {
	BaseEvent
	AgentID      string  `json:"agent_id"`
	UsagePercent float64 `json:"usage_percent"`
	Profile      string  `json:"profile,omitempty"`
}

// NewRotationStartedEvent creates a new rotation started event
func NewRotationStartedEvent(session, agentID string, usagePercent float64, profile string) RotationStartedEvent {
	return RotationStartedEvent{
		BaseEvent: BaseEvent{
			Type:      "rotation_started",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:      agentID,
		UsagePercent: usagePercent,
		Profile:      profile,
	}
}

// RotationCompletedEvent is emitted when agent rotation completes
type RotationCompletedEvent struct {
	BaseEvent
	OldAgentID    string `json:"old_agent_id"`
	NewAgentID    string `json:"new_agent_id"`
	SummaryTokens int    `json:"summary_tokens"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
}

// NewRotationCompletedEvent creates a new rotation completed event
func NewRotationCompletedEvent(session, oldAgentID, newAgentID string, summaryTokens int, success bool, err string) RotationCompletedEvent {
	return RotationCompletedEvent{
		BaseEvent: BaseEvent{
			Type:      "rotation_completed",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		OldAgentID:    oldAgentID,
		NewAgentID:    newAgentID,
		SummaryTokens: summaryTokens,
		Success:       success,
		Error:         err,
	}
}

// ----------------------------------------------------------------
// Checkpoint Events
// ----------------------------------------------------------------

// CheckpointCreatedEvent is emitted when a checkpoint is created
type CheckpointCreatedEvent struct {
	BaseEvent
	Name       string `json:"name"`
	Level      string `json:"level"` // light, standard, full
	SizeBytes  int64  `json:"size_bytes"`
	AgentCount int    `json:"agent_count"`
}

// NewCheckpointCreatedEvent creates a new checkpoint created event
func NewCheckpointCreatedEvent(session, name, level string, sizeBytes int64, agentCount int) CheckpointCreatedEvent {
	return CheckpointCreatedEvent{
		BaseEvent: BaseEvent{
			Type:      "checkpoint_created",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Name:       name,
		Level:      level,
		SizeBytes:  sizeBytes,
		AgentCount: agentCount,
	}
}

// CheckpointRestoredEvent is emitted when a checkpoint is restored
type CheckpointRestoredEvent struct {
	BaseEvent
	Name       string `json:"name"`
	AgentCount int    `json:"agent_count"`
}

// NewCheckpointRestoredEvent creates a new checkpoint restored event
func NewCheckpointRestoredEvent(session, name string, agentCount int) CheckpointRestoredEvent {
	return CheckpointRestoredEvent{
		BaseEvent: BaseEvent{
			Type:      "checkpoint_restored",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Name:       name,
		AgentCount: agentCount,
	}
}

// ----------------------------------------------------------------
// Workflow Events
// ----------------------------------------------------------------

// WorkflowStartedEvent is emitted when a workflow begins
type WorkflowStartedEvent struct {
	BaseEvent
	Workflow string   `json:"workflow"`
	RunID    string   `json:"run_id"`
	Agents   []string `json:"agents"`
}

// NewWorkflowStartedEvent creates a new workflow started event
func NewWorkflowStartedEvent(session, workflow, runID string, agents []string) WorkflowStartedEvent {
	return WorkflowStartedEvent{
		BaseEvent: BaseEvent{
			Type:      "workflow_started",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Workflow: workflow,
		RunID:    runID,
		Agents:   agents,
	}
}

// StageTransitionEvent is emitted when workflow transitions between stages
type StageTransitionEvent struct {
	BaseEvent
	Workflow  string `json:"workflow"`
	RunID     string `json:"run_id"`
	FromStage string `json:"from_stage"`
	ToStage   string `json:"to_stage"`
	Trigger   string `json:"trigger,omitempty"` // What caused the transition
}

// NewStageTransitionEvent creates a new stage transition event
func NewStageTransitionEvent(session, workflow, runID, fromStage, toStage, trigger string) StageTransitionEvent {
	return StageTransitionEvent{
		BaseEvent: BaseEvent{
			Type:      "stage_transition",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Workflow:  workflow,
		RunID:     runID,
		FromStage: fromStage,
		ToStage:   toStage,
		Trigger:   trigger,
	}
}

// WorkflowPausedEvent is emitted when a workflow is paused
type WorkflowPausedEvent struct {
	BaseEvent
	Workflow string `json:"workflow"`
	RunID    string `json:"run_id"`
	Reason   string `json:"reason"`
}

// NewWorkflowPausedEvent creates a new workflow paused event
func NewWorkflowPausedEvent(session, workflow, runID, reason string) WorkflowPausedEvent {
	return WorkflowPausedEvent{
		BaseEvent: BaseEvent{
			Type:      "workflow_paused",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Workflow: workflow,
		RunID:    runID,
		Reason:   reason,
	}
}

// WorkflowCompletedEvent is emitted when a workflow completes
type WorkflowCompletedEvent struct {
	BaseEvent
	Workflow    string `json:"workflow"`
	RunID       string `json:"run_id"`
	DurationSec int    `json:"duration_sec"`
	StageCount  int    `json:"stage_count"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
}

// NewWorkflowCompletedEvent creates a new workflow completed event
func NewWorkflowCompletedEvent(session, workflow, runID string, durationSec, stageCount int, success bool, err string) WorkflowCompletedEvent {
	return WorkflowCompletedEvent{
		BaseEvent: BaseEvent{
			Type:      "workflow_completed",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Workflow:    workflow,
		RunID:       runID,
		DurationSec: durationSec,
		StageCount:  stageCount,
		Success:     success,
		Error:       err,
	}
}

// ----------------------------------------------------------------
// Agent Events
// ----------------------------------------------------------------

// AgentStallEvent is emitted when an agent appears stalled
type AgentStallEvent struct {
	BaseEvent
	AgentID       string  `json:"agent_id"`
	StallDuration float64 `json:"stall_duration_sec"`
	LastActivity  string  `json:"last_activity,omitempty"`
}

// NewAgentStallEvent creates a new agent stall event
func NewAgentStallEvent(session, agentID string, stallDuration float64, lastActivity string) AgentStallEvent {
	return AgentStallEvent{
		BaseEvent: BaseEvent{
			Type:      "agent_stall",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:       agentID,
		StallDuration: stallDuration,
		LastActivity:  lastActivity,
	}
}

// AgentErrorEvent is emitted when an agent encounters an error
type AgentErrorEvent struct {
	BaseEvent
	AgentID   string `json:"agent_id"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

// NewAgentErrorEvent creates a new agent error event
func NewAgentErrorEvent(session, agentID, errorType, message string) AgentErrorEvent {
	return AgentErrorEvent{
		BaseEvent: BaseEvent{
			Type:      "agent_error",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AgentID:   agentID,
		ErrorType: errorType,
		Message:   message,
	}
}

// ----------------------------------------------------------------
// Alert Events
// ----------------------------------------------------------------

// AlertEvent is emitted when an alert is triggered
type AlertEvent struct {
	BaseEvent
	AlertID   string `json:"alert_id"`
	AlertType string `json:"alert_type"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

// NewAlertEvent creates a new alert event
func NewAlertEvent(session, alertID, alertType, severity, message string) AlertEvent {
	return AlertEvent{
		BaseEvent: BaseEvent{
			Type:      "alert",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		AlertID:   alertID,
		AlertType: alertType,
		Severity:  severity,
		Message:   message,
	}
}

// ----------------------------------------------------------------
// Global Functions (using DefaultBus)
// ----------------------------------------------------------------

// Subscribe registers a handler on the default bus
func Subscribe(eventType string, handler EventHandler) UnsubscribeFunc {
	return DefaultBus.Subscribe(eventType, handler)
}

// SubscribeAll registers a handler for all events on the default bus
func SubscribeAll(handler EventHandler) UnsubscribeFunc {
	return DefaultBus.SubscribeAll(handler)
}

// Publish sends an event to the default bus
func Publish(event BusEvent) {
	DefaultBus.Publish(event)
}

// PublishSync sends an event to the default bus and waits for handlers
func PublishSync(event BusEvent) {
	DefaultBus.PublishSync(event)
}

// History returns recent events from the default bus
func History(limit int) []BusEvent {
	return DefaultBus.History(limit)
}
