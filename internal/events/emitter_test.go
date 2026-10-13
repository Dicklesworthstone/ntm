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
// Flush reports false while the worker is stuck behind a handler and true once
// every accepted event has been published, so a short-lived process that
// flushes before exit finds all its events in the bus history.
func TestEventEmitter_FlushWaitsForQueuedEvents(t *testing.T) {
	bus := NewEventBus(300)
	emitter := NewEventEmitter(bus, 256)
	defer closeEmitter(emitter)

	// Every handler slot blocks, so the worker runs the next handler inline
	// and stalls with events still queued.
	release := make(chan struct{})
	unsub := bus.SubscribeAll(func(BusEvent) { <-release })
	defer unsub()

	const total = 200
	for i := 0; i < total; i++ {
		emitter.Emit(NewWebhookEvent("flush_test", "sess", "1", "cc", "queued", nil))
	}
	if emitter.Flush(50 * time.Millisecond) {
		t.Fatal("Flush reported a drained queue while the worker was blocked")
	}
	close(release)
	if !emitter.Flush(5 * time.Second) {
		t.Fatal("Flush timed out after the worker was released")
	}
	if got := len(bus.History(total)); got != total {
		t.Fatalf("bus history holds %d events after Flush, want %d", got, total)
	}
}

func closeEmitter(e *EventEmitter) {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		close(e.ch)
	})
	e.wg.Wait()
}

// Keep the queue nonempty throughout Flush without relying on scheduler sleeps:
// every publication is gated until a replacement event has been enqueued.
// A flush must still finish once its original prefix has been published.
func TestEventEmitter_FlushCompletesUnderContinuousTraffic(t *testing.T) {
	bus := NewEventBus(32)
	emitter := NewEventEmitter(bus, 8)
	defer closeEmitter(emitter)
	finished := make(chan struct{})
	defer close(finished)
	entered := make(chan struct{}, 1)
	advance := make(chan struct{})
	event := busDeliveryTypeEvent{typeFunc: func() string {
		select {
		case entered <- struct{}{}:
		case <-finished:
			return "traffic"
		}
		select {
		case <-advance:
		case <-finished:
		}
		return "traffic"
	}}
	emitter.Emit(event)
	waitForBusDelivery(t, entered, "first traffic publication")
	for i := 0; i < cap(emitter.ch); i++ {
		emitter.Emit(event)
	}
	flushed := make(chan bool, 1)
	go func() { flushed <- emitter.Flush(250 * time.Millisecond) }()
	deadline := time.After(2 * time.Second)
	select {
	case advance <- struct{}{}:
	case <-deadline:
		t.Fatal("first publication did not advance")
	}
	for {
		select {
		case ok := <-flushed:
			if !ok {
				t.Fatal("Flush waited for later traffic instead of its accepted-event snapshot")
			}
			if got := emitter.dropped.Load(); got != 0 {
				t.Fatalf("traffic harness dropped %d events, want none", got)
			}
			return
		case <-entered:
			// The worker consumed one queue slot before entering EventType.
			// Refill it before allowing that publication to finish.
			emitter.Emit(event)
			select {
			case advance <- struct{}{}:
			case <-deadline:
				t.Fatal("traffic publication did not advance")
			}
		case <-deadline:
			t.Fatal("Flush did not return within its deadline")
		}
	}
}

func TestEventEmitter_FlushAccountsForConcurrentDrops(t *testing.T) {
	bus := NewEventBus(1024)
	emitter := NewEventEmitter(bus, 2)
	defer closeEmitter(emitter)
	entered := make(chan struct{})
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	emitter.Emit(busDeliveryTypeEvent{typeFunc: func() string {
		close(entered)
		<-block
		return "first"
	}})
	waitForBusDelivery(t, entered, "blocked publication")
	const workers, each = 8, 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				emitter.Emit(BaseEvent{Type: "burst"})
			}
		}()
	}
	wg.Wait()
	want := 1 + workers*each - int(emitter.dropped.Load())
	if want != 1+cap(emitter.ch) {
		t.Fatalf("accepted %d events, want one active plus the full queue", want)
	}
	if emitter.Flush(0) {
		t.Error("Flush ignored the blocked accepted events")
	}
	release()
	if !emitter.Flush(2 * time.Second) {
		t.Fatal("Flush counted dropped events as unfinished work")
	}
	if got := len(bus.History(1024)); got != want {
		t.Fatalf("published %d events, want all %d accepted events", got, want)
	}
}

func TestEventEmitter_DropDiagnosticCanReenterEmitter(t *testing.T) {
	bus := NewEventBus(10)
	emitter := NewEventEmitter(bus, 1)
	defer closeEmitter(emitter)
	entered := make(chan struct{})
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	emitter.Emit(busDeliveryTypeEvent{typeFunc: func() string {
		close(entered)
		<-block
		return "first"
	}})
	waitForBusDelivery(t, entered, "blocked publication")
	emitter.Emit(BaseEvent{Type: "queued"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		emitter.Emit(busDeliveryTypeEvent{typeFunc: func() string {
			emitter.Emit(BaseEvent{Type: "nested"})
			return "drop"
		}})
	}()
	waitForBusDelivery(t, done, "reentrant drop diagnostic")
	if got := emitter.dropped.Load(); got != 2 {
		t.Fatalf("dropped %d events, want the original and nested attempt", got)
	}
}
