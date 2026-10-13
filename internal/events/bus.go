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
	inflight    atomic.Int64  // active publish calls plus their unfinished async handlers
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
	// Keep a publication counted until every handler has been dispatched.
	// This covers setup, caller-runs backpressure, and gaps between launching
	// asynchronous handlers, when the handler count alone could reach zero.
	b.inflight.Add(1)
	defer b.inflight.Add(-1)

	// Event implementations must not run under the subscriber registry lock:
	// they can reenter the bus or panic without poisoning future subscriptions.
	eventType := event.EventType()

	// Add to history first
	b.historyMu.Lock()
	b.history.Value = event
	b.history = b.history.Next()
	b.historyMu.Unlock()

	// Get handlers under read lock
	b.mu.RLock()
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
		b.inflight.Add(1)
		go func(h EventHandler) {
			defer func() {
				// Release semaphore slot
				<-b.handlerSem
				b.inflight.Add(-1)
			}()
			invokeEventHandler(h, event, "handler")
		}(entry.handler)
	}
}

// WaitHandlers waits until active Publish and PublishSync calls and their
// handlers have returned, or timeout passes. This includes setup and handlers
// running inline under backpressure, not only asynchronously spawned handlers.
// Stop producers before a final drain: new publications can begin after an
// idle bus is observed. Do not call from a handler, which would wait for itself.
// A process drains before closing a webhook bridge or durable attention feed.
func (b *EventBus) WaitHandlers(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for b.inflight.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// PublishSync sends an event and waits for all handlers to complete
func (b *EventBus) PublishSync(event BusEvent) {
	// The synchronous caller owns all of its handlers until wg.Wait returns,
	// including inline handlers. Other goroutines must see that pending work.
	b.inflight.Add(1)
	defer b.inflight.Add(-1)

	// See Publish: invoking user-defined event methods under mu is unsafe.
	eventType := event.EventType()

	// Add to history first
	b.historyMu.Lock()
	b.history.Value = event
	b.history = b.history.Next()
	b.historyMu.Unlock()

	// Get handlers under read lock
	b.mu.RLock()
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
// Global Functions (using DefaultBus)
// ----------------------------------------------------------------

// Subscribe registers a handler on the default bus
func Subscribe(eventType string, handler EventHandler) UnsubscribeFunc {
	return DefaultBus.Subscribe(eventType, handler)
}

// Publish sends an event to the default bus
func Publish(event BusEvent) {
	DefaultBus.Publish(event)
}

// PublishSync sends an event to the default bus and waits for handlers
func PublishSync(event BusEvent) {
	DefaultBus.PublishSync(event)
}
