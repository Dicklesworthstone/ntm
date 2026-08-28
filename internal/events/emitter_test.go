package events

import (
	"sync"
	"testing"
	"time"
)

func TestEventEmitter_PublishesEvent(t *testing.T) {
	bus := NewEventBus(10)
	emitter := NewEventEmitter(bus, 8)
	defer closeEmitter(emitter)

	got := make(chan BusEvent, 1)
	unsub := bus.SubscribeAll(func(e BusEvent) {
		select {
		case got <- e:
		default:
		}
	})
	defer unsub()

	emitter.Emit(NewWebhookEvent("test_event", "sess", "1", "cc", "hello", nil))

	select {
	case ev := <-got:
		if ev.EventType() != "test_event" {
			t.Fatalf("expected event_type test_event, got %q", ev.EventType())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for event publish")
	}
}

func TestEventEmitter_DropsWhenBufferFull(t *testing.T) {
	bus := NewEventBus(10)
	// Force Publish() backpressure by shrinking the handler semaphore and
	// registering more blocking handlers than it can run asynchronously.
	bus.handlerSem = make(chan struct{}, 1)

	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once

	bus.Subscribe("test_event", func(e BusEvent) {
		once.Do(func() { close(started) })
		<-block
	})
	bus.Subscribe("test_event", func(e BusEvent) {
		<-block
	})

	emitter := NewEventEmitter(bus, 2)
	defer closeEmitter(emitter)

	// First event should wedge the emitter worker inside Publish().
	emitter.Emit(NewWebhookEvent("test_event", "sess", "1", "cc", "first", nil))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blocking handler to start")
	}

	for i := 0; i < 64; i++ {
		emitter.Emit(NewWebhookEvent("test_event", "sess", "1", "cc", "spam", nil))
	}

	if emitter.dropped.Load() == 0 {
		close(block)
		t.Fatalf("expected dropped events when buffer is full")
	}

	close(block)
}

// closeEmitter stops a test emitter's publisher goroutine.
func closeEmitter(e *EventEmitter) {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		close(e.ch)
	})
	e.wg.Wait()
}
