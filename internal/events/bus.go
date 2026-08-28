package events

import (
	"container/ring"
	"encoding/json"
	"io"
	"log"
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

// DefaultMaxConcurrentHandlers limits goroutine spawning to prevent resource exhaustion
const DefaultMaxConcurrentHandlers = 100

// EventBus provides a centralized pub/sub system for NTM events
type EventBus struct {
	subscribers map[string][]handlerEntry
	nextID      atomic.Uint64
	mu          sync.RWMutex
	history     *ring.Ring
	historySize int
	historyMu   sync.RWMutex
	handlerSem  chan struct{} // semaphore to limit asynchronously spawned handlers
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
		handlerSem:  make(chan struct{}, DefaultMaxConcurrentHandlers),
	}
}

// DefaultBus is the global default event bus
var DefaultBus = NewEventBus(100)

const (
	// EventHumanZoom is emitted when a human zooms into a pane from the overlay.
	EventHumanZoom = "human.zoom"
	// EventHumanOverlayDismiss is emitted when a human dismisses the overlay.
	EventHumanOverlayDismiss = "human.overlay_dismiss"
)

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
				n := len(handlers)
				handlers[i] = handlers[n-1]
				handlers[n-1] = handlerEntry{} // Clear to prevent memory leak of the handler closure
				b.subscribers[eventType] = handlers[:n-1]
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

	// Call handlers outside of the lock with bounded goroutine creation.
	for _, entry := range entries {
		if !b.tryAcquireHandlerSlot() {
			// Caller-runs backpressure is required here: a handler may publish a
			// nested event. Blocking for capacity while every slot is held by
			// reentrant handlers creates a wait cycle in which no slot can be
			// released. Running inline applies backpressure without spawning another
			// goroutine and breaks that cycle.
			invokeEventHandler(entry.handler, event, "handler")
			continue
		}

		// The acquired slot bounds event-bus-owned handler goroutines.
		go func(h EventHandler) {
			defer func() {
				// Release semaphore slot
				<-b.handlerSem
			}()
			invokeEventHandler(h, event, "handler")
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

	// Call handlers synchronously with bounded goroutine creation.
	var wg sync.WaitGroup
	for _, entry := range entries {
		if !b.tryAcquireHandlerSlot() {
			// See Publish: waiting for a slot here can deadlock when every
			// in-flight handler is synchronously publishing another event.
			invokeEventHandler(entry.handler, event, "sync handler")
			continue
		}

		wg.Add(1)
		go func(h EventHandler) {
			defer wg.Done()
			defer func() {
				// Release semaphore slot
				<-b.handlerSem
			}()
			invokeEventHandler(h, event, "sync handler")
		}(entry.handler)
	}
	wg.Wait()
}

func (b *EventBus) tryAcquireHandlerSlot() bool {
	select {
	case b.handlerSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func invokeEventHandler(handler EventHandler, event BusEvent, label string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("event bus: %s panic recovered: %v", label, r)
		}
	}()
	handler(event)
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
		if r.Value == nil {
			break // Ring is not full yet, no more history
		}
		if event, ok := r.Value.(BusEvent); ok {
			events = append(events, event)
		}
		r = r.Prev()
	}
	return events
}

// EnableRobotMode enables JSON streaming of all events to a writer.
// Note: The handler uses a mutex to serialize Encode calls since
// json.Encoder is not safe for concurrent use by multiple goroutines.
// Encode errors are silently ignored (best-effort delivery to closed writers).
func (b *EventBus) EnableRobotMode(w io.Writer) UnsubscribeFunc {
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	return b.SubscribeAll(func(e BusEvent) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(e) // Best-effort: ignore errors on closed/broken writers
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

// HumanZoomEvent is emitted when a human zooms into a pane from the overlay.
type HumanZoomEvent struct {
	BaseEvent
	PaneIndex int    `json:"pane_index"`
	AgentType string `json:"agent_type,omitempty"`
	Cursor    int64  `json:"cursor,omitempty"`
}

// HumanOverlayDismissEvent is emitted when a human dismisses the overlay.
type HumanOverlayDismissEvent struct {
	BaseEvent
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Cursor          int64   `json:"cursor,omitempty"`
}

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

// ProfileSwitchedEvent is emitted when an agent's profile is changed
type ProfileSwitchedEvent struct {
	BaseEvent
	AgentID    string `json:"agent_id"`
	OldProfile string `json:"old_profile"`
	NewProfile string `json:"new_profile"`
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

// CheckpointRestoredEvent is emitted when a checkpoint is restored
type CheckpointRestoredEvent struct {
	BaseEvent
	Name       string `json:"name"`
	AgentCount int    `json:"agent_count"`
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

// StageTransitionEvent is emitted when workflow transitions between stages
type StageTransitionEvent struct {
	BaseEvent
	Workflow  string `json:"workflow"`
	RunID     string `json:"run_id"`
	FromStage string `json:"from_stage"`
	ToStage   string `json:"to_stage"`
	Trigger   string `json:"trigger,omitempty"` // What caused the transition
}

// WorkflowPausedEvent is emitted when a workflow is paused
type WorkflowPausedEvent struct {
	BaseEvent
	Workflow string `json:"workflow"`
	RunID    string `json:"run_id"`
	Reason   string `json:"reason"`
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
// Conflict Events
// ----------------------------------------------------------------

// ReservationConflictEvent is emitted when an agent's file reservation
// conflicts with another agent's existing reservation.
type ReservationConflictEvent struct {
	BaseEvent
	Path           string   `json:"path"`
	RequestorAgent string   `json:"requestor_agent"`
	RequestorPane  string   `json:"requestor_pane"`
	Holders        []string `json:"holders"`
}

// NewReservationConflictEvent creates a new reservation conflict event.
func NewReservationConflictEvent(session, path, requestorAgent, requestorPane string, holders []string) ReservationConflictEvent {
	return ReservationConflictEvent{
		BaseEvent: BaseEvent{
			Type:      "conflict.reservation",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Path:           path,
		RequestorAgent: requestorAgent,
		RequestorPane:  requestorPane,
		Holders:        cloneStringSlice(holders),
	}
}

// FileConflictEvent is emitted when multiple agents modify the same file
// concurrently, detected via the file watcher.
type FileConflictEvent struct {
	BaseEvent
	Path   string   `json:"path"`
	Agents []string `json:"agents"`
}

// NewFileConflictEvent creates a new file conflict event.
func NewFileConflictEvent(session, path string, agents []string) FileConflictEvent {
	return FileConflictEvent{
		BaseEvent: BaseEvent{
			Type:      "conflict.file",
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Path:   path,
		Agents: cloneStringSlice(agents),
	}
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
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
