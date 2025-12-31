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

func TestEventBus_EnableRobotMode(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	var buf bytes.Buffer

	unsub := bus.EnableRobotMode(&buf)
	defer unsub()

	event := NewProfileAssignedEvent("test-session", "agent1", "architect", "")
	bus.PublishSync(event)

	// Parse JSON output
	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if decoded["type"] != "profile_assigned" {
		t.Errorf("expected type 'profile_assigned', got %v", decoded["type"])
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

func TestProfileEvents(t *testing.T) {
	t.Parallel()

	t.Run("ProfileAssignedEvent", func(t *testing.T) {
		event := NewProfileAssignedEvent("session1", "agent1", "architect", "")
		if event.EventType() != "profile_assigned" {
			t.Errorf("expected type 'profile_assigned', got %q", event.EventType())
		}
		if event.AgentID != "agent1" {
			t.Errorf("expected agent_id 'agent1', got %q", event.AgentID)
		}
	})

	t.Run("ProfileSwitchedEvent", func(t *testing.T) {
		event := NewProfileSwitchedEvent("session1", "agent1", "architect", "implementer")
		if event.EventType() != "profile_switched" {
			t.Errorf("expected type 'profile_switched', got %q", event.EventType())
		}
	})
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

func TestCheckpointEvents(t *testing.T) {
	t.Parallel()

	t.Run("CheckpointCreatedEvent", func(t *testing.T) {
		event := NewCheckpointCreatedEvent("session1", "checkpoint1", "full", 1024000, 3)
		if event.EventType() != "checkpoint_created" {
			t.Errorf("expected type 'checkpoint_created', got %q", event.EventType())
		}
		if event.SizeBytes != 1024000 {
			t.Errorf("expected size 1024000, got %d", event.SizeBytes)
		}
	})

	t.Run("CheckpointRestoredEvent", func(t *testing.T) {
		event := NewCheckpointRestoredEvent("session1", "checkpoint1", 3)
		if event.EventType() != "checkpoint_restored" {
			t.Errorf("expected type 'checkpoint_restored', got %q", event.EventType())
		}
	})
}

func TestWorkflowEvents(t *testing.T) {
	t.Parallel()

	t.Run("WorkflowStartedEvent", func(t *testing.T) {
		event := NewWorkflowStartedEvent("session1", "code-review", "run123", []string{"agent1", "agent2"})
		if event.EventType() != "workflow_started" {
			t.Errorf("expected type 'workflow_started', got %q", event.EventType())
		}
		if len(event.Agents) != 2 {
			t.Errorf("expected 2 agents, got %d", len(event.Agents))
		}
	})

	t.Run("StageTransitionEvent", func(t *testing.T) {
		event := NewStageTransitionEvent("session1", "code-review", "run123", "design", "implement", "completion")
		if event.EventType() != "stage_transition" {
			t.Errorf("expected type 'stage_transition', got %q", event.EventType())
		}
	})

	t.Run("WorkflowPausedEvent", func(t *testing.T) {
		event := NewWorkflowPausedEvent("session1", "code-review", "run123", "user request")
		if event.EventType() != "workflow_paused" {
			t.Errorf("expected type 'workflow_paused', got %q", event.EventType())
		}
	})

	t.Run("WorkflowCompletedEvent", func(t *testing.T) {
		event := NewWorkflowCompletedEvent("session1", "code-review", "run123", 300, 5, true, "")
		if event.EventType() != "workflow_completed" {
			t.Errorf("expected type 'workflow_completed', got %q", event.EventType())
		}
		if event.DurationSec != 300 {
			t.Errorf("expected duration 300, got %d", event.DurationSec)
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

func TestGlobalFunctions(t *testing.T) {
	t.Parallel()

	var received atomic.Int32

	unsub := Subscribe("global_test", func(e BusEvent) {
		received.Add(1)
	})
	defer unsub()

	event := BaseEvent{Type: "global_test", Timestamp: time.Now()}
	PublishSync(event)

	if received.Load() != 1 {
		t.Errorf("expected 1 event received, got %d", received.Load())
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
