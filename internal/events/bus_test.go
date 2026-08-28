package events

import (
	"bytes"
	"encoding/json"
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

func TestRotationEvents(t *testing.T) {
	t.Parallel()

	t.Run("ContextWarningEvent", func(t *testing.T) {
		event := NewContextWarningEvent("session1", "agent1", 75.5, 50000)
		if event.EventType() != "context_warning" {
			t.Errorf("expected type 'context_warning', got %q", event.EventType())
		}
		if event.UsagePercent != 75.5 {
			t.Errorf("expected usage 75.5, got %f", event.UsagePercent)
		}
	})

	t.Run("RotationStartedEvent", func(t *testing.T) {
		event := NewRotationStartedEvent("session1", "agent1", 85.0, "architect")
		if event.EventType() != "rotation_started" {
			t.Errorf("expected type 'rotation_started', got %q", event.EventType())
		}
	})

	t.Run("RotationCompletedEvent", func(t *testing.T) {
		event := NewRotationCompletedEvent("session1", "agent1", "agent2", 2000, true, "")
		if event.EventType() != "rotation_completed" {
			t.Errorf("expected type 'rotation_completed', got %q", event.EventType())
		}
		if !event.Success {
			t.Error("expected success to be true")
		}
	})
}

func TestAgentEvents(t *testing.T) {
	t.Parallel()

	t.Run("AgentStallEvent", func(t *testing.T) {
		event := NewAgentStallEvent("session1", "agent1", 120.5, "last prompt")
		if event.EventType() != "agent_stall" {
			t.Errorf("expected type 'agent_stall', got %q", event.EventType())
		}
	})

	t.Run("AgentErrorEvent", func(t *testing.T) {
		event := NewAgentErrorEvent("session1", "agent1", "rate_limit", "Rate limit exceeded")
		if event.EventType() != "agent_error" {
			t.Errorf("expected type 'agent_error', got %q", event.EventType())
		}
	})
}

func TestAlertEvent(t *testing.T) {
	t.Parallel()

	event := NewAlertEvent("session1", "alert123", "agent_stuck", "warning", "Agent stuck for 5 minutes")
	if event.EventType() != "alert" {
		t.Errorf("expected type 'alert', got %q", event.EventType())
	}
	if event.Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", event.Severity)
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

// =============================================================================
// Conflict Event Tests (br-vdfjr)
// =============================================================================

func TestNewReservationConflictEvent(t *testing.T) {
	t.Parallel()

	event := NewReservationConflictEvent("proj", "src/auth.go", "BlueLake", "cc_1", []string{"GreenCastle"})
	if event.EventType() != "conflict.reservation" {
		t.Errorf("EventType() = %q, want %q", event.EventType(), "conflict.reservation")
	}
	if event.EventSession() != "proj" {
		t.Errorf("EventSession() = %q, want %q", event.EventSession(), "proj")
	}
	if event.Path != "src/auth.go" {
		t.Errorf("Path = %q, want %q", event.Path, "src/auth.go")
	}
	if event.RequestorAgent != "BlueLake" {
		t.Errorf("RequestorAgent = %q, want %q", event.RequestorAgent, "BlueLake")
	}
	if len(event.Holders) != 1 || event.Holders[0] != "GreenCastle" {
		t.Errorf("Holders = %v, want [GreenCastle]", event.Holders)
	}
	if event.EventTimestamp().IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestNewFileConflictEvent(t *testing.T) {
	t.Parallel()

	event := NewFileConflictEvent("proj", "cmd/main.go", []string{"Agent1", "Agent2"})
	if event.EventType() != "conflict.file" {
		t.Errorf("EventType() = %q, want %q", event.EventType(), "conflict.file")
	}
	if event.EventSession() != "proj" {
		t.Errorf("EventSession() = %q, want %q", event.EventSession(), "proj")
	}
	if event.Path != "cmd/main.go" {
		t.Errorf("Path = %q, want %q", event.Path, "cmd/main.go")
	}
	if len(event.Agents) != 2 {
		t.Errorf("Agents count = %d, want 2", len(event.Agents))
	}
}

func TestConflictEvents_ImplementBusEvent(t *testing.T) {
	t.Parallel()

	// Compile-time interface check
	var _ BusEvent = ReservationConflictEvent{}
	var _ BusEvent = FileConflictEvent{}

	// Publish and receive through the bus
	bus := NewEventBus(10)
	var received atomic.Int32

	bus.SubscribeAll(func(event BusEvent) {
		received.Add(1)
	})

	bus.PublishSync(NewReservationConflictEvent("s", "f.go", "A", "p1", []string{"B"}))
	bus.PublishSync(NewFileConflictEvent("s", "g.go", []string{"A", "B"}))

	if received.Load() != 2 {
		t.Errorf("expected 2 events received, got %d", received.Load())
	}

	// Verify in history
	history := bus.History(10)
	if len(history) != 2 {
		t.Errorf("expected 2 events in history, got %d", len(history))
	}
}

func TestConflictEvents_JSONMarshal(t *testing.T) {
	t.Parallel()

	event := NewReservationConflictEvent("proj", "auth.go", "A", "cc_1", []string{"B", "C"})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if !bytes.Contains(data, []byte(`"conflict.reservation"`)) {
		t.Errorf("JSON should contain event type, got %s", data)
	}
	if !bytes.Contains(data, []byte(`"auth.go"`)) {
		t.Errorf("JSON should contain path, got %s", data)
	}
}

func TestNewReservationConflictEvent_DefensiveCopiesHolders(t *testing.T) {
	t.Parallel()

	holders := []string{"BlueLake", "GreenCastle"}
	event := NewReservationConflictEvent("proj", "auth.go", "A", "cc_1", holders)
	holders[0] = "mutated"

	if got, want := event.Holders[0], "BlueLake"; got != want {
		t.Fatalf("event.Holders[0] = %q, want %q", got, want)
	}
}

func TestNewFileConflictEvent_DefensiveCopiesAgents(t *testing.T) {
	t.Parallel()

	agents := []string{"Agent1", "Agent2"}
	event := NewFileConflictEvent("proj", "cmd/main.go", agents)
	agents[0] = "mutated"

	if got, want := event.Agents[0], "Agent1"; got != want {
		t.Fatalf("event.Agents[0] = %q, want %q", got, want)
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
