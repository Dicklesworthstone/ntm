package events

import (
	"sync"
	"testing"
	"time"
)

func TestEventEmitter_PublishesEvent(t *testing.T) {
	bus := NewEventBus(10)
	emitter := NewEventEmitter(bus, 8)
	defer emitter.Close()

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
	// registering >1 handler where the first blocks.
	bus.handlerSem = make(chan struct{}, 1)

	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once

	bus.Subscribe("test_event", func(e BusEvent) {
		once.Do(func() { close(started) })
		<-block
	})
	bus.Subscribe("test_event", func(e BusEvent) {})

	emitter := NewEventEmitter(bus, 2)
	defer emitter.Close()

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

	if emitter.Dropped() == 0 {
		close(block)
		t.Fatalf("expected dropped events when buffer is full")
	}

	close(block)
}

func TestEventEmitterCloseWaitsForPublisher(t *testing.T) {
	bus := NewEventBus(10)
	bus.handlerSem = make(chan struct{}, 1)

	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once

	bus.Subscribe("test_event", func(e BusEvent) {
		once.Do(func() { close(started) })
		<-block
	})
	bus.Subscribe("test_event", func(e BusEvent) {})

	emitter := NewEventEmitter(bus, 2)
	emitter.Emit(NewWebhookEvent("test_event", "sess", "1", "cc", "first", nil))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking handler to start")
	}

	closed := make(chan struct{})
	go func() {
		emitter.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned before blocked publish completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(block)

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after publish unblocked")
	}
}

func TestEventEmitterEmitAfterCloseDoesNotPanicOrPublish(t *testing.T) {
	bus := NewEventBus(10)
	emitter := NewEventEmitter(bus, 8)
	emitter.Close()
	emitter.Close()

	got := make(chan BusEvent, 1)
	unsub := bus.SubscribeAll(func(e BusEvent) {
		select {
		case got <- e:
		default:
		}
	})
	defer unsub()

	emitter.Emit(NewWebhookEvent("test_event", "sess", "1", "cc", "ignored", nil))

	select {
	case ev := <-got:
		t.Fatalf("unexpected publish after Close: %v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
