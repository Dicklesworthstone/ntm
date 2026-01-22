package state

import (
	"sync"
	"testing"
	"time"
)

func TestNewTimelineTracker(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		tracker := NewTimelineTracker(nil)
		defer tracker.Stop()

		if tracker.config.MaxEventsPerAgent != 1000 {
			t.Errorf("expected MaxEventsPerAgent=1000, got %d", tracker.config.MaxEventsPerAgent)
		}
		if tracker.config.RetentionDuration != 24*time.Hour {
			t.Errorf("expected RetentionDuration=24h, got %v", tracker.config.RetentionDuration)
		}
	})

	t.Run("custom config", func(t *testing.T) {
		cfg := &TimelineConfig{
			MaxEventsPerAgent: 500,
			RetentionDuration: 12 * time.Hour,
			PruneInterval:     0, // disable background pruning
		}
		tracker := NewTimelineTracker(cfg)
		defer tracker.Stop()

		if tracker.config.MaxEventsPerAgent != 500 {
			t.Errorf("expected MaxEventsPerAgent=500, got %d", tracker.config.MaxEventsPerAgent)
		}
		if tracker.config.RetentionDuration != 12*time.Hour {
			t.Errorf("expected RetentionDuration=12h, got %v", tracker.config.RetentionDuration)
		}
	})
}

func TestRecordEvent(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	t.Run("first event", func(t *testing.T) {
		event := tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			AgentType: AgentTypeClaude,
			SessionID: "test-session",
			State:     TimelineWorking,
			Details:   map[string]string{"task": "review code"},
		})

		if event.PreviousState != "" {
			t.Errorf("expected empty PreviousState for first event, got %s", event.PreviousState)
		}
		if event.Duration != 0 {
			t.Errorf("expected zero Duration for first event, got %v", event.Duration)
		}
		if event.Timestamp.IsZero() {
			t.Error("expected Timestamp to be set")
		}
	})

	t.Run("subsequent event computes previous state and duration", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond)

		event := tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			AgentType: AgentTypeClaude,
			SessionID: "test-session",
			State:     TimelineIdle,
		})

		if event.PreviousState != TimelineWorking {
			t.Errorf("expected PreviousState=working, got %s", event.PreviousState)
		}
		if event.Duration <= 0 {
			t.Errorf("expected positive Duration, got %v", event.Duration)
		}
	})
}

func TestGetEvents(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	// Record some events
	now := time.Now()
	for i := 0; i < 5; i++ {
		tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			AgentType: AgentTypeClaude,
			SessionID: "test-session",
			State:     TimelineState([]string{"idle", "working", "waiting", "working", "idle"}[i]),
			Timestamp: now.Add(time.Duration(i) * time.Minute),
		})
	}

	t.Run("get all events", func(t *testing.T) {
		events := tracker.GetEvents(time.Time{})
		if len(events) != 5 {
			t.Errorf("expected 5 events, got %d", len(events))
		}
	})

	t.Run("get events since timestamp", func(t *testing.T) {
		events := tracker.GetEvents(now.Add(2 * time.Minute))
		if len(events) != 3 {
			t.Errorf("expected 3 events since t+2m, got %d", len(events))
		}
	})
}

func TestGetEventsForAgent(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	// Record events for multiple agents
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})

	events := tracker.GetEventsForAgent("cc_1", time.Time{})
	if len(events) != 2 {
		t.Errorf("expected 2 events for cc_1, got %d", len(events))
	}

	events = tracker.GetEventsForAgent("cc_2", time.Time{})
	if len(events) != 1 {
		t.Errorf("expected 1 event for cc_2, got %d", len(events))
	}

	events = tracker.GetEventsForAgent("nonexistent", time.Time{})
	if events != nil {
		t.Errorf("expected nil for nonexistent agent, got %v", events)
	}
}

func TestGetEventsForSession(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", SessionID: "session-1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", SessionID: "session-1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cod_1", SessionID: "session-2", State: TimelineWorking})

	events := tracker.GetEventsForSession("session-1", time.Time{})
	if len(events) != 2 {
		t.Errorf("expected 2 events for session-1, got %d", len(events))
	}

	events = tracker.GetEventsForSession("session-2", time.Time{})
	if len(events) != 1 {
		t.Errorf("expected 1 event for session-2, got %d", len(events))
	}
}

func TestGetCurrentState(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWaiting})

	state := tracker.GetCurrentState("cc_1")
	if state != TimelineWaiting {
		t.Errorf("expected current state=waiting, got %s", state)
	}

	state = tracker.GetCurrentState("nonexistent")
	if state != "" {
		t.Errorf("expected empty state for nonexistent agent, got %s", state)
	}
}

func TestGetAgentStates(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", State: TimelineIdle})
	tracker.RecordEvent(AgentEvent{AgentID: "cod_1", State: TimelineError})

	states := tracker.GetAgentStates()
	if len(states) != 3 {
		t.Errorf("expected 3 agents, got %d", len(states))
	}
	if states["cc_1"] != TimelineWorking {
		t.Errorf("expected cc_1=working, got %s", states["cc_1"])
	}
	if states["cc_2"] != TimelineIdle {
		t.Errorf("expected cc_2=idle, got %s", states["cc_2"])
	}
	if states["cod_1"] != TimelineError {
		t.Errorf("expected cod_1=error, got %s", states["cod_1"])
	}
}

func TestGetLastSeen(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking, Timestamp: now})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle, Timestamp: now.Add(time.Minute)})

	lastSeen := tracker.GetLastSeen("cc_1")
	if !lastSeen.Equal(now.Add(time.Minute)) {
		t.Errorf("expected lastSeen=%v, got %v", now.Add(time.Minute), lastSeen)
	}

	lastSeen = tracker.GetLastSeen("nonexistent")
	if !lastSeen.IsZero() {
		t.Errorf("expected zero time for nonexistent agent, got %v", lastSeen)
	}
}

func TestOnStateChange(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	var callbackEvents []AgentEvent
	var mu sync.Mutex

	tracker.OnStateChange(func(event AgentEvent) {
		mu.Lock()
		callbackEvents = append(callbackEvents, event)
		mu.Unlock()
	})

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})

	mu.Lock()
	count := len(callbackEvents)
	mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 callback invocations, got %d", count)
	}
}

func TestStats(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", State: TimelineWorking})

	stats := tracker.Stats()
	if stats.TotalAgents != 2 {
		t.Errorf("expected TotalAgents=2, got %d", stats.TotalAgents)
	}
	if stats.TotalEvents != 3 {
		t.Errorf("expected TotalEvents=3, got %d", stats.TotalEvents)
	}
	if stats.EventsByAgent["cc_1"] != 2 {
		t.Errorf("expected cc_1 events=2, got %d", stats.EventsByAgent["cc_1"])
	}
	if stats.EventsByState["working"] != 2 {
		t.Errorf("expected working events=2, got %d", stats.EventsByState["working"])
	}
	if stats.EventsByState["idle"] != 1 {
		t.Errorf("expected idle events=1, got %d", stats.EventsByState["idle"])
	}
}

func TestMaxEventsPerAgentPruning(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{
		MaxEventsPerAgent: 5,
		PruneInterval:     0,
	})
	defer tracker.Stop()

	// Record 10 events
	for i := 0; i < 10; i++ {
		tracker.RecordEvent(AgentEvent{
			AgentID: "cc_1",
			State:   TimelineState([]string{"idle", "working"}[i%2]),
			Details: map[string]string{"index": string(rune('0' + i))},
		})
	}

	events := tracker.GetEventsForAgent("cc_1", time.Time{})
	if len(events) != 5 {
		t.Errorf("expected 5 events after pruning, got %d", len(events))
	}

	// Verify we kept the most recent events (indices 5-9)
	// The first event should have index '5'
	if events[0].Details["index"] != "5" {
		t.Errorf("expected first event index=5, got %s", events[0].Details["index"])
	}
}

func TestTimePrune(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{
		RetentionDuration: 100 * time.Millisecond,
		PruneInterval:     0,
	})
	defer tracker.Stop()

	// Record an old event
	oldTime := time.Now().Add(-200 * time.Millisecond)
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking, Timestamp: oldTime})

	// Record a recent event
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})

	// Prune
	pruned := tracker.Prune()
	if pruned != 1 {
		t.Errorf("expected 1 event pruned, got %d", pruned)
	}

	events := tracker.GetEventsForAgent("cc_1", time.Time{})
	if len(events) != 1 {
		t.Errorf("expected 1 event after pruning, got %d", len(events))
	}
	if events[0].State != TimelineIdle {
		t.Errorf("expected remaining event state=idle, got %s", events[0].State)
	}
}

func TestClear(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", State: TimelineWorking})

	tracker.Clear()

	stats := tracker.Stats()
	if stats.TotalAgents != 0 {
		t.Errorf("expected TotalAgents=0 after clear, got %d", stats.TotalAgents)
	}
	if stats.TotalEvents != 0 {
		t.Errorf("expected TotalEvents=0 after clear, got %d", stats.TotalEvents)
	}
}

func TestRemoveAgent(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_2", State: TimelineWorking})

	tracker.RemoveAgent("cc_1")

	events := tracker.GetEventsForAgent("cc_1", time.Time{})
	if events != nil {
		t.Errorf("expected nil events for removed agent, got %v", events)
	}

	events = tracker.GetEventsForAgent("cc_2", time.Time{})
	if len(events) != 1 {
		t.Errorf("expected cc_2 events preserved, got %d", len(events))
	}

	stats := tracker.Stats()
	if stats.TotalAgents != 1 {
		t.Errorf("expected TotalAgents=1, got %d", stats.TotalAgents)
	}
}

func TestComputeStateDurations(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking, Timestamp: now})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle, Timestamp: now.Add(10 * time.Minute)})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking, Timestamp: now.Add(15 * time.Minute)})

	durations := tracker.ComputeStateDurations("cc_1", now, now.Add(20*time.Minute))

	// Working: 0-10min + 15-20min = 15min
	// Idle: 10-15min = 5min
	expectedWorking := 15 * time.Minute
	expectedIdle := 5 * time.Minute

	if durations[TimelineWorking] != expectedWorking {
		t.Errorf("expected working duration=%v, got %v", expectedWorking, durations[TimelineWorking])
	}
	if durations[TimelineIdle] != expectedIdle {
		t.Errorf("expected idle duration=%v, got %v", expectedIdle, durations[TimelineIdle])
	}
}

func TestGetStateTransitions(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWaiting})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle})

	transitions := tracker.GetStateTransitions("cc_1")

	// idle->working, working->waiting, waiting->working, working->idle
	if transitions["idle->working"] != 1 {
		t.Errorf("expected idle->working=1, got %d", transitions["idle->working"])
	}
	if transitions["working->waiting"] != 1 {
		t.Errorf("expected working->waiting=1, got %d", transitions["working->waiting"])
	}
	if transitions["waiting->working"] != 1 {
		t.Errorf("expected waiting->working=1, got %d", transitions["waiting->working"])
	}
	if transitions["working->idle"] != 1 {
		t.Errorf("expected working->idle=1, got %d", transitions["working->idle"])
	}
}

func TestStateFromAgentStatus(t *testing.T) {
	tests := []struct {
		input    AgentStatus
		expected TimelineState
	}{
		{AgentIdle, TimelineIdle},
		{AgentWorking, TimelineWorking},
		{AgentError, TimelineError},
		{AgentCrashed, TimelineStopped},
		{AgentStatus("unknown"), TimelineIdle}, // default
	}

	for _, tc := range tests {
		result := StateFromAgentStatus(tc.input)
		if result != tc.expected {
			t.Errorf("StateFromAgentStatus(%s) = %s, expected %s", tc.input, result, tc.expected)
		}
	}
}

func TestTimelineStateIsTerminal(t *testing.T) {
	tests := []struct {
		state    TimelineState
		terminal bool
	}{
		{TimelineIdle, false},
		{TimelineWorking, false},
		{TimelineWaiting, false},
		{TimelineError, true},
		{TimelineStopped, true},
	}

	for _, tc := range tests {
		result := tc.state.IsTerminal()
		if result != tc.terminal {
			t.Errorf("TimelineState(%s).IsTerminal() = %v, expected %v", tc.state, result, tc.terminal)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	var wg sync.WaitGroup
	const goroutines = 10
	const eventsPerGoroutine = 100

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := "cc_" + string(rune('0'+id))
			for j := 0; j < eventsPerGoroutine; j++ {
				tracker.RecordEvent(AgentEvent{
					AgentID:   agentID,
					SessionID: "test",
					State:     TimelineState([]string{"idle", "working"}[j%2]),
				})
			}
		}(i)
	}

	// Concurrent reads while writing
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				_ = tracker.GetEvents(time.Time{})
				_ = tracker.Stats()
				_ = tracker.GetAgentStates()
			}
		}()
	}

	wg.Wait()

	stats := tracker.Stats()
	expectedEvents := goroutines * eventsPerGoroutine
	if stats.TotalEvents != expectedEvents {
		t.Errorf("expected %d events, got %d", expectedEvents, stats.TotalEvents)
	}
	if stats.TotalAgents != goroutines {
		t.Errorf("expected %d agents, got %d", goroutines, stats.TotalAgents)
	}
}

// TestRecordEvent_RepeatedStates verifies that repeated state transitions are stored
// individually without compression. This documents the current behavior where each event
// is stored even if the state doesn't change, which is useful for tracking activity
// timestamps even when state remains the same.
func TestRecordEvent_RepeatedStates(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()

	// Record multiple events with the same state
	for i := 0; i < 5; i++ {
		tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			AgentType: AgentTypeClaude,
			SessionID: "test-session",
			State:     TimelineWorking,
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Details:   map[string]string{"iteration": string(rune('0' + i))},
		})
	}

	events := tracker.GetEventsForAgent("cc_1", time.Time{})

	// Current behavior: All events are stored (no compression)
	if len(events) != 5 {
		t.Errorf("expected 5 events (no compression), got %d", len(events))
	}

	// Verify PreviousState is set correctly even for same-state transitions
	for i, event := range events {
		if i == 0 {
			// First event has no previous state
			if event.PreviousState != "" {
				t.Errorf("event[0]: expected empty PreviousState, got %s", event.PreviousState)
			}
		} else {
			// Subsequent events should have previous state set
			if event.PreviousState != TimelineWorking {
				t.Errorf("event[%d]: expected PreviousState=working, got %s", i, event.PreviousState)
			}
		}
		// All events should have state=working
		if event.State != TimelineWorking {
			t.Errorf("event[%d]: expected State=working, got %s", i, event.State)
		}
	}

	// Verify durations are computed even for same-state transitions
	for i, event := range events {
		if i == 0 {
			if event.Duration != 0 {
				t.Errorf("event[0]: expected Duration=0, got %v", event.Duration)
			}
		} else {
			expectedDuration := time.Minute
			if event.Duration != expectedDuration {
				t.Errorf("event[%d]: expected Duration=%v, got %v", i, expectedDuration, event.Duration)
			}
		}
	}
}

// TestRecordEvent_StateTransitionDetails verifies that state transitions are recorded
// with proper details and triggers.
func TestRecordEvent_StateTransitionDetails(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	t.Run("with details and trigger", func(t *testing.T) {
		event := tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			State:     TimelineWorking,
			Details:   map[string]string{"task": "code review", "file": "main.go"},
			Trigger:   "user_command",
		})

		if event.Details["task"] != "code review" {
			t.Errorf("expected details[task]='code review', got %s", event.Details["task"])
		}
		if event.Details["file"] != "main.go" {
			t.Errorf("expected details[file]='main.go', got %s", event.Details["file"])
		}
		if event.Trigger != "user_command" {
			t.Errorf("expected trigger='user_command', got %s", event.Trigger)
		}
	})

	t.Run("nil details safe", func(t *testing.T) {
		event := tracker.RecordEvent(AgentEvent{
			AgentID: "cc_2",
			State:   TimelineIdle,
			Details: nil,
		})

		if event.Details != nil {
			t.Errorf("expected nil details to remain nil, got %v", event.Details)
		}
	})
}

// TestGetEventsInTimeRange tests filtering events by time range with various edge cases.
func TestGetEventsInTimeRange(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()

	// Create events at specific timestamps
	timestamps := []time.Duration{
		-10 * time.Minute,
		-5 * time.Minute,
		-2 * time.Minute,
		-1 * time.Minute,
		0,
	}

	for i, offset := range timestamps {
		tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			State:     TimelineState([]string{"idle", "working", "waiting", "working", "idle"}[i]),
			Timestamp: now.Add(offset),
		})
	}

	t.Run("exact boundary match", func(t *testing.T) {
		// Get events since exactly -5 minutes
		events := tracker.GetEvents(now.Add(-5 * time.Minute))
		if len(events) != 4 {
			t.Errorf("expected 4 events at boundary, got %d", len(events))
		}
	})

	t.Run("just after boundary", func(t *testing.T) {
		// Get events since just after -5 minutes
		events := tracker.GetEvents(now.Add(-5*time.Minute + time.Millisecond))
		if len(events) != 3 {
			t.Errorf("expected 3 events after boundary, got %d", len(events))
		}
	})

	t.Run("future timestamp returns none", func(t *testing.T) {
		events := tracker.GetEvents(now.Add(time.Hour))
		if len(events) != 0 {
			t.Errorf("expected 0 events for future timestamp, got %d", len(events))
		}
	})
}

// TestConcurrentCallbackSafety verifies that callbacks are called safely without deadlock.
func TestConcurrentCallbackSafety(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	var callbackCount int
	var mu sync.Mutex

	// Register a callback that also reads from the tracker
	tracker.OnStateChange(func(event AgentEvent) {
		mu.Lock()
		callbackCount++
		mu.Unlock()

		// This should not deadlock - callbacks are called after releasing lock
		_ = tracker.GetCurrentState(event.AgentID)
		_ = tracker.Stats()
	})

	// Record events concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tracker.RecordEvent(AgentEvent{
				AgentID: "cc_" + string(rune('0'+id)),
				State:   TimelineWorking,
			})
		}(i)
	}

	wg.Wait()

	mu.Lock()
	if callbackCount != 10 {
		t.Errorf("expected 10 callback invocations, got %d", callbackCount)
	}
	mu.Unlock()
}

func BenchmarkRecordEvent(b *testing.B) {
	tracker := NewTimelineTracker(&TimelineConfig{
		MaxEventsPerAgent: 10000,
		PruneInterval:     0,
	})
	defer tracker.Stop()

	event := AgentEvent{
		AgentID:   "cc_1",
		AgentType: AgentTypeClaude,
		SessionID: "bench-session",
		State:     TimelineWorking,
		Details:   map[string]string{"task": "benchmark"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.RecordEvent(event)
	}
}

func BenchmarkGetEvents(b *testing.B) {
	tracker := NewTimelineTracker(&TimelineConfig{
		MaxEventsPerAgent: 10000,
		PruneInterval:     0,
	})
	defer tracker.Stop()

	// Pre-populate with events
	for i := 0; i < 1000; i++ {
		tracker.RecordEvent(AgentEvent{
			AgentID: "cc_1",
			State:   TimelineState([]string{"idle", "working"}[i%2]),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tracker.GetEvents(time.Time{})
	}
}

// ============================================================================
// Marker Tests - Event markers for discrete timeline events
// ============================================================================

func TestMarkerTypeSymbol(t *testing.T) {
	tests := []struct {
		markerType MarkerType
		expected   string
	}{
		{MarkerPrompt, "▶"},
		{MarkerCompletion, "✓"},
		{MarkerError, "✗"},
		{MarkerStart, "◆"},
		{MarkerStop, "◆"},
		{MarkerType("unknown"), "•"},
	}

	for _, tc := range tests {
		result := tc.markerType.Symbol()
		if result != tc.expected {
			t.Errorf("MarkerType(%s).Symbol() = %s, expected %s", tc.markerType, result, tc.expected)
		}
	}
}

func TestMarkerTypeString(t *testing.T) {
	tests := []struct {
		markerType MarkerType
		expected   string
	}{
		{MarkerPrompt, "prompt"},
		{MarkerCompletion, "completion"},
		{MarkerError, "error"},
		{MarkerStart, "start"},
		{MarkerStop, "stop"},
	}

	for _, tc := range tests {
		result := tc.markerType.String()
		if result != tc.expected {
			t.Errorf("MarkerType(%s).String() = %s, expected %s", tc.markerType, result, tc.expected)
		}
	}
}

func TestAddMarker(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	t.Run("basic marker", func(t *testing.T) {
		marker := tracker.AddMarker(TimelineMarker{
			AgentID:   "cc_1",
			SessionID: "test-session",
			Type:      MarkerPrompt,
			Message:   "Test prompt message",
		})

		if marker.ID == "" {
			t.Error("expected marker to have an ID assigned")
		}
		if marker.Timestamp.IsZero() {
			t.Error("expected marker to have timestamp set")
		}
		if marker.AgentID != "cc_1" {
			t.Errorf("expected AgentID=cc_1, got %s", marker.AgentID)
		}
		if marker.Type != MarkerPrompt {
			t.Errorf("expected Type=prompt, got %s", marker.Type)
		}
	})

	t.Run("preserves custom ID", func(t *testing.T) {
		marker := tracker.AddMarker(TimelineMarker{
			ID:        "custom-id",
			AgentID:   "cc_1",
			Type:      MarkerCompletion,
		})

		if marker.ID != "custom-id" {
			t.Errorf("expected ID=custom-id, got %s", marker.ID)
		}
	})

	t.Run("with details", func(t *testing.T) {
		marker := tracker.AddMarker(TimelineMarker{
			AgentID: "cc_1",
			Type:    MarkerError,
			Message: "Error occurred",
			Details: map[string]string{"code": "500", "reason": "timeout"},
		})

		if marker.Details["code"] != "500" {
			t.Errorf("expected details[code]=500, got %s", marker.Details["code"])
		}
		if marker.Details["reason"] != "timeout" {
			t.Errorf("expected details[reason]=timeout, got %s", marker.Details["reason"])
		}
	})
}

func TestGetMarkers(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()

	// Add markers at different times
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt, Timestamp: now.Add(-20 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerCompletion, Timestamp: now.Add(-10 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt, Timestamp: now.Add(-5 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_2", Type: MarkerStart, Timestamp: now.Add(-2 * time.Minute)})

	t.Run("all markers", func(t *testing.T) {
		markers := tracker.GetMarkers(time.Time{}, time.Time{})
		if len(markers) != 4 {
			t.Errorf("expected 4 markers, got %d", len(markers))
		}
	})

	t.Run("markers since timestamp", func(t *testing.T) {
		markers := tracker.GetMarkers(now.Add(-10*time.Minute), time.Time{})
		if len(markers) != 3 {
			t.Errorf("expected 3 markers since -10m, got %d", len(markers))
		}
	})

	t.Run("markers in time range", func(t *testing.T) {
		markers := tracker.GetMarkers(now.Add(-15*time.Minute), now.Add(-5*time.Minute))
		if len(markers) != 2 {
			t.Errorf("expected 2 markers in range, got %d", len(markers))
		}
	})
}

func TestGetMarkersForAgent(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()

	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt, Timestamp: now.Add(-10 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_2", Type: MarkerPrompt, Timestamp: now.Add(-8 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerCompletion, Timestamp: now.Add(-5 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerError, Timestamp: now.Add(-2 * time.Minute)})

	markers := tracker.GetMarkersForAgent("cc_1", time.Time{}, time.Time{})
	if len(markers) != 3 {
		t.Errorf("expected 3 markers for cc_1, got %d", len(markers))
	}

	markers = tracker.GetMarkersForAgent("cc_2", time.Time{}, time.Time{})
	if len(markers) != 1 {
		t.Errorf("expected 1 marker for cc_2, got %d", len(markers))
	}

	markers = tracker.GetMarkersForAgent("nonexistent", time.Time{}, time.Time{})
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for nonexistent agent, got %d", len(markers))
	}
}

func TestGetMarkersForSession(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	now := time.Now()

	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", SessionID: "session-1", Type: MarkerPrompt, Timestamp: now.Add(-10 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_2", SessionID: "session-1", Type: MarkerPrompt, Timestamp: now.Add(-8 * time.Minute)})
	tracker.AddMarker(TimelineMarker{AgentID: "cod_1", SessionID: "session-2", Type: MarkerStart, Timestamp: now.Add(-5 * time.Minute)})

	markers := tracker.GetMarkersForSession("session-1", time.Time{}, time.Time{})
	if len(markers) != 2 {
		t.Errorf("expected 2 markers for session-1, got %d", len(markers))
	}

	markers = tracker.GetMarkersForSession("session-2", time.Time{}, time.Time{})
	if len(markers) != 1 {
		t.Errorf("expected 1 marker for session-2, got %d", len(markers))
	}
}

func TestOnMarkerAdd(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	var callbackMarkers []TimelineMarker
	var mu sync.Mutex

	tracker.OnMarkerAdd(func(marker TimelineMarker) {
		mu.Lock()
		callbackMarkers = append(callbackMarkers, marker)
		mu.Unlock()
	})

	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerCompletion})

	mu.Lock()
	count := len(callbackMarkers)
	mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 callback invocations, got %d", count)
	}
}

func TestPruneMarkers(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{
		RetentionDuration: 100 * time.Millisecond,
		PruneInterval:     0,
	})
	defer tracker.Stop()

	// Add an old marker
	oldTime := time.Now().Add(-200 * time.Millisecond)
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt, Timestamp: oldTime})

	// Add a recent marker
	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerCompletion})

	pruned := tracker.PruneMarkers()
	if pruned != 1 {
		t.Errorf("expected 1 marker pruned, got %d", pruned)
	}

	markers := tracker.GetMarkers(time.Time{}, time.Time{})
	if len(markers) != 1 {
		t.Errorf("expected 1 marker remaining, got %d", len(markers))
	}
	if markers[0].Type != MarkerCompletion {
		t.Errorf("expected remaining marker type=completion, got %s", markers[0].Type)
	}
}

func TestClearMarkers(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt})
	tracker.AddMarker(TimelineMarker{AgentID: "cc_2", Type: MarkerStart})

	tracker.ClearMarkers()

	markers := tracker.GetMarkers(time.Time{}, time.Time{})
	if len(markers) != 0 {
		t.Errorf("expected 0 markers after clear, got %d", len(markers))
	}
}

func TestMarkerConcurrentAccess(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	var wg sync.WaitGroup
	const goroutines = 10
	const markersPerGoroutine = 50

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := "cc_" + string(rune('0'+id))
			for j := 0; j < markersPerGoroutine; j++ {
				tracker.AddMarker(TimelineMarker{
					AgentID:   agentID,
					SessionID: "test",
					Type:      MarkerType([]string{"prompt", "completion"}[j%2]),
				})
			}
		}(i)
	}

	// Concurrent reads while writing
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < markersPerGoroutine; j++ {
				_ = tracker.GetMarkers(time.Time{}, time.Time{})
			}
		}()
	}

	wg.Wait()

	markers := tracker.GetMarkers(time.Time{}, time.Time{})
	expectedMarkers := goroutines * markersPerGoroutine
	if len(markers) != expectedMarkers {
		t.Errorf("expected %d markers, got %d", expectedMarkers, len(markers))
	}
}

func TestMarkerIDSequence(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	m1 := tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerPrompt})
	m2 := tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerCompletion})
	m3 := tracker.AddMarker(TimelineMarker{AgentID: "cc_1", Type: MarkerError})

	// IDs should be unique
	ids := map[string]bool{m1.ID: true, m2.ID: true, m3.ID: true}
	if len(ids) != 3 {
		t.Errorf("expected 3 unique marker IDs, got %d", len(ids))
	}

	// IDs should follow sequence pattern (m1, m2, m3, ...)
	if m1.ID != "m1" {
		t.Errorf("expected first marker ID=m1, got %s", m1.ID)
	}
	if m2.ID != "m2" {
		t.Errorf("expected second marker ID=m2, got %s", m2.ID)
	}
	if m3.ID != "m3" {
		t.Errorf("expected third marker ID=m3, got %s", m3.ID)
	}
}

func BenchmarkAddMarker(b *testing.B) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	marker := TimelineMarker{
		AgentID:   "cc_1",
		SessionID: "bench-session",
		Type:      MarkerPrompt,
		Message:   "benchmark prompt",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.AddMarker(marker)
	}
}

func BenchmarkGetMarkers(b *testing.B) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer tracker.Stop()

	// Pre-populate with markers
	for i := 0; i < 1000; i++ {
		tracker.AddMarker(TimelineMarker{
			AgentID: "cc_1",
			Type:    MarkerType([]string{"prompt", "completion"}[i%2]),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tracker.GetMarkers(time.Time{}, time.Time{})
	}
}
