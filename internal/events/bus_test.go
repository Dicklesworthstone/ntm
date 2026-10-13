package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewEventBus(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(50)
	if bus == nil {
		t.Fatal("expected non-nil bus")
	}
	if bus.historySize != 50 {
		t.Errorf("expected history size 50, got %d", bus.historySize)
	}
}

func TestNewEventBus_DefaultSize(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(0)
	if bus.historySize != 100 {
		t.Errorf("expected default history size 100, got %d", bus.historySize)
	}
}

func TestEventBus_Subscribe(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received atomic.Int32

	bus.Subscribe("test_event", func(e BusEvent) {
		received.Add(1)
	})

	if bus.SubscriberCount("test_event") != 1 {
		t.Errorf("expected 1 subscriber, got %d", bus.SubscriberCount("test_event"))
	}
}

func TestEventBus_SubscribeAll(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received atomic.Int32

	bus.SubscribeAll(func(e BusEvent) {
		received.Add(1)
	})

	if bus.SubscriberCount("*") != 1 {
		t.Errorf("expected 1 wildcard subscriber, got %d", bus.SubscriberCount("*"))
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)

	unsub := bus.Subscribe("test_event", func(e BusEvent) {})

	if bus.SubscriberCount("test_event") != 1 {
		t.Errorf("expected 1 subscriber, got %d", bus.SubscriberCount("test_event"))
	}

	unsub()

	if bus.SubscriberCount("test_event") != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", bus.SubscriberCount("test_event"))
	}
}

func TestEventBus_Publish(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received atomic.Int32
	var wg sync.WaitGroup

	wg.Add(1)
	bus.Subscribe("test_event", func(e BusEvent) {
		received.Add(1)
		wg.Done()
	})

	event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
	bus.Publish(event)

	// Wait for async handler
	wg.Wait()

	if received.Load() != 1 {
		t.Errorf("expected 1 event received, got %d", received.Load())
	}
}

// WaitHandlers reports false while a handler Publish started is still running
// and true once it returns, so a consumer is unsubscribed only after the events
// already published have reached it.
func TestEventBus_WaitHandlersWaitsForAsyncHandlers(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	release := make(chan struct{})
	var handled atomic.Int32
	bus.SubscribeAll(func(BusEvent) {
		<-release
		handled.Add(1)
	})

	bus.Publish(BaseEvent{Type: "wait_handlers_test", Timestamp: time.Now()})
	if bus.WaitHandlers(20 * time.Millisecond) {
		t.Fatal("WaitHandlers returned true while the handler was blocked")
	}
	close(release)
	if !bus.WaitHandlers(2 * time.Second) {
		t.Fatal("WaitHandlers timed out after the handler was released")
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("handler ran %d times, want 1 before WaitHandlers returned", got)
	}
}

func TestEventBus_PublishSync(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received atomic.Int32

	bus.Subscribe("test_event", func(e BusEvent) {
		received.Add(1)
	})

	event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
	bus.PublishSync(event)

	// Should have received by now (sync)
	if received.Load() != 1 {
		t.Errorf("expected 1 event received, got %d", received.Load())
	}
}

func TestEventBus_WildcardSubscriber(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received atomic.Int32

	bus.SubscribeAll(func(e BusEvent) {
		received.Add(1)
	})

	event1 := BaseEvent{Type: "event_type_1", Timestamp: time.Now()}
	event2 := BaseEvent{Type: "event_type_2", Timestamp: time.Now()}

	bus.PublishSync(event1)
	bus.PublishSync(event2)

	if received.Load() != 2 {
		t.Errorf("expected 2 events received by wildcard, got %d", received.Load())
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received1, received2 atomic.Int32

	bus.Subscribe("test_event", func(e BusEvent) {
		received1.Add(1)
	})

	bus.Subscribe("test_event", func(e BusEvent) {
		received2.Add(1)
	})

	event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
	bus.PublishSync(event)

	if received1.Load() != 1 || received2.Load() != 1 {
		t.Errorf("expected both subscribers to receive, got %d and %d", received1.Load(), received2.Load())
	}
}

func TestEventBus_History(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(5)

	// Publish 3 events
	for i := 0; i < 3; i++ {
		event := BaseEvent{Type: "test_event", Timestamp: time.Now(), Session: "test"}
		bus.Publish(event)
	}

	history := bus.History(10)
	if len(history) != 3 {
		t.Errorf("expected 3 events in history, got %d", len(history))
	}
}

func TestEventBus_HistoryLimit(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(3)

	// Publish 5 events (exceeds history size)
	for i := 0; i < 5; i++ {
		event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
		bus.Publish(event)
	}

	history := bus.History(10)
	if len(history) != 3 {
		t.Errorf("expected 3 events in history (limit), got %d", len(history))
	}
}

func TestBaseEvent_Interface(t *testing.T) {
	t.Parallel()

	event := BaseEvent{
		Type:      "test_type",
		Timestamp: time.Now(),
		Session:   "test_session",
	}

	if event.EventType() != "test_type" {
		t.Errorf("expected type 'test_type', got %q", event.EventType())
	}

	if event.EventSession() != "test_session" {
		t.Errorf("expected session 'test_session', got %q", event.EventSession())
	}

	if event.EventTimestamp().IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestEventBus_ConcurrentPublish(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(100)
	var received atomic.Int32

	bus.Subscribe("test_event", func(e BusEvent) {
		received.Add(1)
	})

	// Publish concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
			bus.PublishSync(event)
		}()
	}

	wg.Wait()

	if received.Load() != 100 {
		t.Errorf("expected 100 events received, got %d", received.Load())
	}
}

func TestEventBus_ReentrantPublishAtCapacityDoesNotDeadlock(t *testing.T) {
	publishers := []struct {
		name string
		fn   func(*EventBus, BusEvent)
	}{
		{name: "Publish", fn: func(bus *EventBus, event BusEvent) { bus.Publish(event) }},
		{name: "PublishSync", fn: func(bus *EventBus, event BusEvent) { bus.PublishSync(event) }},
	}
	for _, outer := range publishers {
		for _, nested := range publishers {
			t.Run(outer.name+"/nested_"+nested.name, func(t *testing.T) {
				assertReentrantPublishAtCapacityDoesNotDeadlock(t, outer.fn, nested.fn)
			})
		}
	}
}

func assertReentrantPublishAtCapacityDoesNotDeadlock(
	t *testing.T,
	publishOuter func(*EventBus, BusEvent),
	publishNested func(*EventBus, BusEvent),
) {
	t.Helper()

	bus := NewEventBus(10)
	ready := make(chan struct{}, DefaultMaxConcurrentHandlers)
	release := make(chan struct{})
	completed := make(chan struct{}, DefaultMaxConcurrentHandlers)
	nestedCompleted := make(chan struct{}, DefaultMaxConcurrentHandlers)
	var nestedReceived atomic.Int32

	bus.Subscribe("nested", func(BusEvent) {
		nestedReceived.Add(1)
		nestedCompleted <- struct{}{}
	})
	bus.Subscribe("outer", func(BusEvent) {
		ready <- struct{}{}
		<-release
		publishNested(bus, BaseEvent{Type: "nested", Timestamp: time.Now()})
		completed <- struct{}{}
	})

	var publishers sync.WaitGroup
	publishers.Add(DefaultMaxConcurrentHandlers)
	for i := 0; i < DefaultMaxConcurrentHandlers; i++ {
		go func() {
			defer publishers.Done()
			publishOuter(bus, BaseEvent{Type: "outer", Timestamp: time.Now()})
		}()
	}

	readyDeadline := time.After(2 * time.Second)
	for i := 0; i < DefaultMaxConcurrentHandlers; i++ {
		select {
		case <-ready:
		case <-readyDeadline:
			t.Fatalf("only %d/%d outer handlers reached the saturation barrier", i, DefaultMaxConcurrentHandlers)
		}
	}
	close(release)

	outerDeadline := time.After(2 * time.Second)
	for i := 0; i < DefaultMaxConcurrentHandlers; i++ {
		select {
		case <-completed:
		case <-outerDeadline:
			t.Fatalf("only %d/%d reentrant outer handlers completed", i, DefaultMaxConcurrentHandlers)
		}
	}

	done := make(chan struct{})
	go func() {
		publishers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant publish deadlocked with every handler semaphore slot occupied")
	}

	nestedDeadline := time.After(2 * time.Second)
	for i := 0; i < DefaultMaxConcurrentHandlers; i++ {
		select {
		case <-nestedCompleted:
		case <-nestedDeadline:
			t.Fatalf("only %d/%d nested handlers completed", i, DefaultMaxConcurrentHandlers)
		}
	}

	if got := nestedReceived.Load(); got != DefaultMaxConcurrentHandlers {
		t.Fatalf("nested handlers received %d events, want %d", got, DefaultMaxConcurrentHandlers)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(bus.handlerSem) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(bus.handlerSem); got != 0 {
		t.Fatalf("handler semaphore retained %d slots after all handlers completed", got)
	}
}

func TestEventBus_ConcurrentSubscribe(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)

	// Subscribe concurrently
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Subscribe("test_event", func(e BusEvent) {})
		}()
	}

	wg.Wait()

	if bus.SubscriberCount("test_event") != 50 {
		t.Errorf("expected 50 subscribers, got %d", bus.SubscriberCount("test_event"))
	}
}

func TestEventBus_UnsubscribeMultiple(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var received1, received2, received3 atomic.Int32

	// Subscribe 3 handlers
	unsub1 := bus.Subscribe("test_event", func(e BusEvent) {
		received1.Add(1)
	})
	unsub2 := bus.Subscribe("test_event", func(e BusEvent) {
		received2.Add(1)
	})
	unsub3 := bus.Subscribe("test_event", func(e BusEvent) {
		received3.Add(1)
	})

	// Verify all 3 work
	event := BaseEvent{Type: "test_event", Timestamp: time.Now()}
	bus.PublishSync(event)

	if received1.Load() != 1 || received2.Load() != 1 || received3.Load() != 1 {
		t.Errorf("all handlers should have received, got %d, %d, %d",
			received1.Load(), received2.Load(), received3.Load())
	}

	// Unsubscribe #1 (first), then verify #2 and #3 still work correctly
	unsub1()
	bus.PublishSync(event)

	if received1.Load() != 1 { // Should NOT have increased
		t.Errorf("handler 1 should not receive after unsubscribe, got %d", received1.Load())
	}
	if received2.Load() != 2 || received3.Load() != 2 {
		t.Errorf("handlers 2 and 3 should have received, got %d and %d",
			received2.Load(), received3.Load())
	}

	// Unsubscribe #3 (last), then verify #2 still works
	unsub3()
	bus.PublishSync(event)

	if received3.Load() != 2 { // Should NOT have increased
		t.Errorf("handler 3 should not receive after unsubscribe, got %d", received3.Load())
	}
	if received2.Load() != 3 {
		t.Errorf("handler 2 should have received, got %d", received2.Load())
	}

	// Unsubscribe #2 (middle)
	unsub2()
	bus.PublishSync(event)

	if received2.Load() != 3 { // Should NOT have increased
		t.Errorf("handler 2 should not receive after unsubscribe, got %d", received2.Load())
	}

	// Verify subscriber count is 0
	if bus.SubscriberCount("test_event") != 0 {
		t.Errorf("expected 0 subscribers after all unsubscribed, got %d",
			bus.SubscriberCount("test_event"))
	}
}

func TestNewWebhookEvent_DefensiveCopiesDetails(t *testing.T) {
	t.Parallel()

	details := map[string]string{
		"severity": "warning",
		"agent":    "cc_1",
	}
	event := NewWebhookEvent("agent.rate_limit", "proj", "1", "cc_1", "rate limited", details)
	details["severity"] = "critical"

	if got, want := event.Details["severity"], "warning"; got != want {
		t.Fatalf("event.Details[severity] = %q, want %q", got, want)
	}
}

func waitForBusDelivery(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

// The CLI and webhook bridge use WaitHandlers before closing consumers. A
// PublishSync running on another goroutine is just as important to that drain
// as an asynchronous Publish, even though the publisher itself waits.
func TestEventBus_WaitHandlersIncludesSynchronousDelivery(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(10)
	entered := make(chan struct{})
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	var delivered atomic.Int32
	bus.SubscribeAll(func(BusEvent) {
		close(entered)
		<-block
		delivered.Add(1)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		bus.PublishSync(NewWebhookEvent(WebhookAgentCompleted, "proj", "%1", "worker", "done", nil))
	}()
	waitForBusDelivery(t, entered, "synchronous handler")
	if bus.WaitHandlers(20 * time.Millisecond) {
		t.Error("drain reported success before the synchronous consumer completed")
	}
	release()
	waitForBusDelivery(t, done, "synchronous publisher")
	if !bus.WaitHandlers(time.Second) || delivered.Load() != 1 {
		t.Fatal("drain did not observe completed synchronous delivery")
	}
}

// Fill a real handler slot, then keep the caller-runs fallback blocked after
// the asynchronous handler has left. The remaining inline consumer must not
// disappear from the shutdown drain just because it owns no semaphore slot.
func TestEventBus_WaitHandlersIncludesInlineDelivery(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(10)
	bus.handlerSem = make(chan struct{}, 1)
	asyncEntered := make(chan struct{})
	asyncBlock := make(chan struct{})
	releaseAsync := sync.OnceFunc(func() { close(asyncBlock) })
	defer releaseAsync()
	inlineEntered := make(chan struct{})
	inlineBlock := make(chan struct{})
	releaseInline := sync.OnceFunc(func() { close(inlineBlock) })
	defer releaseInline()
	var delivered atomic.Int32
	bus.Subscribe("occupy", func(BusEvent) {
		close(asyncEntered)
		<-asyncBlock
	})
	bus.Subscribe(WebhookSessionEnded, func(BusEvent) {
		close(inlineEntered)
		<-inlineBlock
		delivered.Add(1)
	})
	bus.Publish(BaseEvent{Type: "occupy"})
	waitForBusDelivery(t, asyncEntered, "asynchronous handler")
	done := make(chan struct{})
	go func() {
		defer close(done)
		bus.Publish(NewWebhookEvent(WebhookSessionEnded, "proj", "", "", "ended", nil))
	}()
	waitForBusDelivery(t, inlineEntered, "inline handler")
	releaseAsync()
	deadline := time.Now().Add(2 * time.Second)
	for len(bus.handlerSem) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(bus.handlerSem) != 0 {
		t.Fatal("asynchronous handler did not release its slot")
	}
	if bus.WaitHandlers(20 * time.Millisecond) {
		t.Error("drain reported success while the inline consumer was still blocked")
	}
	releaseInline()
	waitForBusDelivery(t, done, "inline publisher")
	if !bus.WaitHandlers(time.Second) || delivered.Load() != 1 {
		t.Fatal("drain did not observe completed inline delivery")
	}
}

type busDeliveryTypeEvent struct {
	BaseEvent
	typeFunc func() string
}

func (e busDeliveryTypeEvent) EventType() string { return e.typeFunc() }

func TestEventBus_WaitHandlersIncludesDispatchSetup(t *testing.T) {
	for _, synchronous := range []bool{false, true} {
		name := "Publish"
		if synchronous {
			name = "PublishSync"
		}
		t.Run(name, func(t *testing.T) {
			bus := NewEventBus(10)
			entered := make(chan struct{})
			block := make(chan struct{})
			release := sync.OnceFunc(func() { close(block) })
			defer release()
			var delivered atomic.Int32
			bus.SubscribeAll(func(BusEvent) { delivered.Add(1) })
			event := busDeliveryTypeEvent{typeFunc: func() string {
				close(entered)
				<-block
				return WebhookBeadCompleted
			}}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if synchronous {
					bus.PublishSync(event)
				} else {
					bus.Publish(event)
				}
			}()
			waitForBusDelivery(t, entered, "dispatch setup")
			if bus.WaitHandlers(20 * time.Millisecond) {
				t.Error("drain missed a publication before its first handler was scheduled")
			}
			release()
			waitForBusDelivery(t, done, "publisher")
			if !bus.WaitHandlers(time.Second) || delivered.Load() != 1 {
				t.Fatal("publication was not delivered exactly once after setup resumed")
			}
		})
	}
}

func TestEventBus_EventTypePanicDoesNotPoisonSubscriptions(t *testing.T) {
	for _, synchronous := range []bool{false, true} {
		name := "Publish"
		if synchronous {
			name = "PublishSync"
		}
		t.Run(name, func(t *testing.T) {
			bus := NewEventBus(10)
			event := busDeliveryTypeEvent{typeFunc: func() string { panic("bad event type") }}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("expected the event's panic to reach the publisher")
					}
				}()
				if synchronous {
					bus.PublishSync(event)
				} else {
					bus.Publish(event)
				}
			}()
			// Detect a leaked read lock without leaving a blocked Subscribe
			// goroutine behind when this regression runs on the old code.
			if !bus.mu.TryLock() {
				t.Fatal("EventType panic left the subscriber registry locked")
			}
			bus.mu.Unlock()
			if !bus.WaitHandlers(0) {
				t.Fatal("panicking publication leaked drain accounting")
			}
			var delivered int
			unsub := bus.Subscribe("healthy", func(BusEvent) { delivered++ })
			bus.PublishSync(BaseEvent{Type: "healthy"})
			unsub()
			if delivered != 1 {
				t.Fatalf("subsequent healthy delivery count = %d, want 1", delivered)
			}
		})
	}
}
