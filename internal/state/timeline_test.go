package state

import (
	"sync"
	"testing"
	"time"
)

func TestNewTimelineTracker(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		tracker := NewTimelineTracker(nil)
		defer stopTrackerForTest(tracker)

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
		defer stopTrackerForTest(tracker)

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
	defer stopTrackerForTest(tracker)

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

func TestGetEventsForSession(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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
	defer stopTrackerForTest(tracker)

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

func TestOnStateChange(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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
	defer stopTrackerForTest(tracker)

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

func TestTimelineStateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state TimelineState
		want  string
	}{
		{TimelineIdle, "idle"},
		{TimelineWorking, "working"},
		{TimelineWaiting, "waiting"},
		{TimelineError, "error"},
		{TimelineStopped, "stopped"},
		{TimelineState("custom"), "custom"},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			got := tc.state.String()
			if got != tc.want {
				t.Errorf("TimelineState(%q).String() = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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
				_ = trackerEventsForTest(tracker, time.Time{})
				_ = tracker.Stats()
				_ = tracker.GetCurrentState("agent-1")
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

// TestRecordEvent_StateTransitionDetails verifies that state transitions are recorded
// with proper details and triggers.
func TestRecordEvent_StateTransitionDetails(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

	t.Run("with details and trigger", func(t *testing.T) {
		event := tracker.RecordEvent(AgentEvent{
			AgentID: "cc_1",
			State:   TimelineWorking,
			Details: map[string]string{"task": "code review", "file": "main.go"},
			Trigger: "user_command",
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

// TestConcurrentCallbackSafety verifies that callbacks are called safely without deadlock.
func TestConcurrentCallbackSafety(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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
	defer stopTrackerForTest(tracker)

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
	defer stopTrackerForTest(tracker)

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
			ID:      "custom-id",
			AgentID: "cc_1",
			Type:    MarkerCompletion,
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

func TestGetMarkersForSession(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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

func TestMarkerIDSequence(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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

func TestTimelineTrackerStopIdempotent(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	stopTrackerForTest(tracker)
	stopTrackerForTest(tracker)
}

func TestOnStateChange_PanicRecovery(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

	callCount := 0
	// Register a callback that panics
	tracker.OnStateChange(func(event AgentEvent) {
		callCount++
		panic("test panic")
	})
	// Register a second callback that should still run
	tracker.OnStateChange(func(event AgentEvent) {
		callCount++
	})

	// Should not panic
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking})

	if callCount != 2 {
		t.Errorf("expected both callbacks called (2), got %d", callCount)
	}
}

func TestStatsOldestNewest(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

	now := time.Now()
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineWorking, Timestamp: now.Add(-10 * time.Minute)})
	tracker.RecordEvent(AgentEvent{AgentID: "cc_1", State: TimelineIdle, Timestamp: now})

	stats := tracker.Stats()
	if !stats.OldestEvent.Equal(now.Add(-10 * time.Minute)) {
		t.Errorf("expected oldest=%v, got %v", now.Add(-10*time.Minute), stats.OldestEvent)
	}
	if !stats.NewestEvent.Equal(now) {
		t.Errorf("expected newest=%v, got %v", now, stats.NewestEvent)
	}
}

func TestStatsEmpty(t *testing.T) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

	stats := tracker.Stats()
	if stats.TotalAgents != 0 {
		t.Errorf("expected TotalAgents=0, got %d", stats.TotalAgents)
	}
	if stats.TotalEvents != 0 {
		t.Errorf("expected TotalEvents=0, got %d", stats.TotalEvents)
	}
	if !stats.OldestEvent.IsZero() {
		t.Error("expected zero OldestEvent for empty tracker")
	}
	if !stats.NewestEvent.IsZero() {
		t.Error("expected zero NewestEvent for empty tracker")
	}
}

func BenchmarkAddMarker(b *testing.B) {
	tracker := NewTimelineTracker(&TimelineConfig{PruneInterval: 0})
	defer stopTrackerForTest(tracker)

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

// stopTrackerForTest stops a test tracker's background prune goroutine.
func stopTrackerForTest(tr *TimelineTracker) {
	tr.stopOnce.Do(func() { close(tr.stopPrune) })
	tr.pruneWg.Wait()
}

// trackerEventsForTest returns retained events newer than since (all within
// retention when since is zero), sorted chronologically.
func trackerEventsForTest(tr *TimelineTracker, since time.Time) []AgentEvent {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	cutoff := since
	if cutoff.IsZero() {
		cutoff = time.Now().Add(-tr.config.RetentionDuration)
	}
	result := make([]AgentEvent, 0, len(tr.allEvents))
	for _, event := range tr.allEvents {
		if event.Timestamp.After(cutoff) || event.Timestamp.Equal(cutoff) {
			result = append(result, event)
		}
	}
	return result
}
