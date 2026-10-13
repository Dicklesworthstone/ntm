package events

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// EventEmitter provides non-blocking emission of BusEvents to an EventBus.
//
// Design notes:
// - Emit() never waits for queue capacity (drops when buffer is full).
// - Events are published on a single worker goroutine.
// - Consumers should subscribe via EventBus subscriptions.
type EventEmitter struct {
	bus *EventBus
	ch  chan BusEvent

	dropped atomic.Int64
	// pending counts accepted events whose publication is still in progress.
	pending atomic.Int64
	// Enqueue receipts are recorded in channel order under mu. Because there
	// is one publisher, published is a completed prefix of those receipts;
	// Flush can wait for a fixed prefix rather than an ever-growing queue.
	accepted  atomic.Uint64
	published atomic.Uint64

	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu     sync.RWMutex
	closed bool
}

// NewEventEmitter creates an emitter for the given bus. If bus is nil, DefaultBus is used.
func NewEventEmitter(bus *EventBus, buffer int) *EventEmitter {
	if bus == nil {
		bus = DefaultBus
	}
	if buffer < 1 {
		buffer = 256
	}
	return &EventEmitter{
		bus: bus,
		ch:  make(chan BusEvent, buffer),
	}
}

// Start launches the background publisher loop (idempotent).
func (e *EventEmitter) Start() {
	e.startOnce.Do(func() {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return
		}
		e.wg.Add(1)
		e.mu.Unlock()

		go func() {
			defer e.wg.Done()
			for ev := range e.ch {
				e.bus.Publish(ev)
				e.pending.Add(-1)
				e.published.Add(1)
			}
		}()
	})
}

// Emit enqueues an event for async publish. If the buffer is full, the event is dropped.
func (e *EventEmitter) Emit(ev BusEvent) {
	if ev == nil {
		return
	}
	e.Start()

	// Serialize successful sends and their receipts. With concurrent senders,
	// counting receipts out of channel order could let an earlier completion
	// stand in for a later accepted event that Flush still needs to publish.
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}

	e.pending.Add(1)
	select {
	case e.ch <- ev:
		e.accepted.Add(1)
		e.mu.Unlock()
	default:
		e.pending.Add(-1)
		n := e.dropped.Add(1)
		e.mu.Unlock()
		// EventType and logging can invoke user code. Never hold the enqueue
		// lock across diagnostics, which may themselves emit another event.
		// Avoid log spam: emit only for the first drop and then every 1000 drops.
		if n == 1 || n%1000 == 0 {
			slog.Default().Debug("event emitter dropped events (buffer full)", "dropped", n, "event_type", ev.EventType())
		}
	}
}

// Flush waits until every event Emit accepted before the call has been
// published to the bus (and so is in its history), or timeout passes. It
// reports whether that accepted-event snapshot was published; later emissions
// do not extend the wait and require another Flush. A short-lived process
// calls it before exiting so its events are not lost with the worker goroutine.
func (e *EventEmitter) Flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	target := e.accepted.Load()
	for e.published.Load() < target {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

var (
	defaultEmitterOnce sync.Once
	defaultEmitter     *EventEmitter
)

// DefaultEmitter returns the global default emitter publishing into DefaultBus.
func DefaultEmitter() *EventEmitter {
	defaultEmitterOnce.Do(func() {
		defaultEmitter = NewEventEmitter(DefaultBus, 1024)
	})
	return defaultEmitter
}
