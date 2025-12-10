package tracker

import (
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tracker := New()
	if tracker == nil {
		t.Fatal("New() returned nil")
	}
	if tracker.maxSize != DefaultMaxSize {
		t.Errorf("expected maxSize %d, got %d", DefaultMaxSize, tracker.maxSize)
	}
	if tracker.maxAge != DefaultMaxAge {
		t.Errorf("expected maxAge %v, got %v", DefaultMaxAge, tracker.maxAge)
	}
}

func TestNewWithConfig(t *testing.T) {
	tracker := NewWithConfig(100, 1*time.Minute)
	if tracker.maxSize != 100 {
		t.Errorf("expected maxSize 100, got %d", tracker.maxSize)
	}
	if tracker.maxAge != 1*time.Minute {
		t.Errorf("expected maxAge 1m, got %v", tracker.maxAge)
	}
}

func TestNewWithConfigDefaults(t *testing.T) {
	// Test that invalid values get defaults
	tracker := NewWithConfig(-1, -1)
	if tracker.maxSize != DefaultMaxSize {
		t.Errorf("expected default maxSize, got %d", tracker.maxSize)
	}
	if tracker.maxAge != DefaultMaxAge {
		t.Errorf("expected default maxAge, got %v", tracker.maxAge)
	}
}

func TestRecord(t *testing.T) {
	tracker := New()

	change := StateChange{
		Type:    ChangeAgentOutput,
		Session: "test-session",
		Pane:    "test-pane",
		Details: map[string]interface{}{"key": "value"},
	}

	tracker.Record(change)

	if tracker.Count() != 1 {
		t.Errorf("expected count 1, got %d", tracker.Count())
	}

	changes := tracker.All()
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != ChangeAgentOutput {
		t.Errorf("expected type %s, got %s", ChangeAgentOutput, changes[0].Type)
	}
	if changes[0].Session != "test-session" {
		t.Errorf("expected session 'test-session', got %s", changes[0].Session)
	}
}

func TestRecordSetsTimestamp(t *testing.T) {
	tracker := New()

	before := time.Now()
	tracker.Record(StateChange{Type: ChangeAgentState})
	after := time.Now()

	changes := tracker.All()
	if len(changes) != 1 {
		t.Fatal("expected 1 change")
	}

	if changes[0].Timestamp.Before(before) || changes[0].Timestamp.After(after) {
		t.Error("timestamp should be set automatically")
	}
}

func TestSince(t *testing.T) {
	tracker := New()

	// Record some changes with known times
	t1 := time.Now()
	tracker.Record(StateChange{
		Timestamp: t1.Add(-3 * time.Second),
		Type:      ChangeAgentOutput,
		Session:   "s1",
	})
	tracker.Record(StateChange{
		Timestamp: t1.Add(-1 * time.Second),
		Type:      ChangeAgentState,
		Session:   "s1",
	})
	tracker.Record(StateChange{
		Timestamp: t1,
		Type:      ChangeAlert,
		Session:   "s1",
	})

	// Get changes since 2 seconds ago
	changes := tracker.Since(t1.Add(-2 * time.Second))
	if len(changes) != 2 {
		t.Errorf("expected 2 changes since -2s, got %d", len(changes))
	}
}

func TestMaxSize(t *testing.T) {
	tracker := NewWithConfig(3, 1*time.Hour)

	for i := 0; i < 5; i++ {
		tracker.Record(StateChange{
			Type:    ChangeAgentOutput,
			Session: "s1",
			Details: map[string]interface{}{"i": i},
		})
	}

	if tracker.Count() != 3 {
		t.Errorf("expected count 3 (maxSize), got %d", tracker.Count())
	}

	// The oldest two should be gone (i=0, i=1)
	changes := tracker.All()
	for _, c := range changes {
		idx := c.Details["i"].(int)
		if idx < 2 {
			t.Errorf("expected oldest entries to be pruned, found i=%d", idx)
		}
	}
}

func TestMaxAge(t *testing.T) {
	// Use very short maxAge for testing
	tracker := NewWithConfig(100, 50*time.Millisecond)

	// Record an old change
	tracker.Record(StateChange{
		Timestamp: time.Now().Add(-100 * time.Millisecond),
		Type:      ChangeAgentOutput,
		Session:   "old",
	})

	// Wait a bit and record a new change (triggers pruning)
	time.Sleep(10 * time.Millisecond)
	tracker.Record(StateChange{
		Type:    ChangeAgentState,
		Session: "new",
	})

	changes := tracker.All()
	if len(changes) != 1 {
		t.Errorf("expected 1 change (old should be pruned), got %d", len(changes))
	}
	if len(changes) > 0 && changes[0].Session != "new" {
		t.Error("expected 'new' session to remain")
	}
}

func TestClear(t *testing.T) {
	tracker := New()
	tracker.Record(StateChange{Type: ChangeAgentOutput})
	tracker.Record(StateChange{Type: ChangeAgentState})

	if tracker.Count() != 2 {
		t.Errorf("expected 2 before clear, got %d", tracker.Count())
	}

	tracker.Clear()

	if tracker.Count() != 0 {
		t.Errorf("expected 0 after clear, got %d", tracker.Count())
	}
}

func TestCoalesce(t *testing.T) {
	tracker := New()

	// Add consecutive changes of same type for same pane
	now := time.Now()
	tracker.Record(StateChange{Timestamp: now, Type: ChangeAgentOutput, Session: "s1", Pane: "p1"})
	tracker.Record(StateChange{Timestamp: now.Add(1 * time.Second), Type: ChangeAgentOutput, Session: "s1", Pane: "p1"})
	tracker.Record(StateChange{Timestamp: now.Add(2 * time.Second), Type: ChangeAgentOutput, Session: "s1", Pane: "p1"})
	// Different type
	tracker.Record(StateChange{Timestamp: now.Add(3 * time.Second), Type: ChangeAgentState, Session: "s1", Pane: "p1"})
	// Back to first type
	tracker.Record(StateChange{Timestamp: now.Add(4 * time.Second), Type: ChangeAgentOutput, Session: "s1", Pane: "p1"})

	coalesced := tracker.Coalesce()
	if len(coalesced) != 3 {
		t.Errorf("expected 3 coalesced groups, got %d", len(coalesced))
	}

	// First group: 3 agent_output changes
	if coalesced[0].Type != ChangeAgentOutput || coalesced[0].Count != 3 {
		t.Errorf("expected first group: 3 agent_output, got %s count %d", coalesced[0].Type, coalesced[0].Count)
	}

	// Second group: 1 agent_state change
	if coalesced[1].Type != ChangeAgentState || coalesced[1].Count != 1 {
		t.Errorf("expected second group: 1 agent_state, got %s count %d", coalesced[1].Type, coalesced[1].Count)
	}

	// Third group: 1 agent_output change
	if coalesced[2].Type != ChangeAgentOutput || coalesced[2].Count != 1 {
		t.Errorf("expected third group: 1 agent_output, got %s count %d", coalesced[2].Type, coalesced[2].Count)
	}
}

func TestSinceByType(t *testing.T) {
	tracker := New()

	now := time.Now()
	tracker.Record(StateChange{Timestamp: now.Add(-2 * time.Second), Type: ChangeAgentOutput, Session: "s1"})
	tracker.Record(StateChange{Timestamp: now.Add(-1 * time.Second), Type: ChangeAgentState, Session: "s1"})
	tracker.Record(StateChange{Timestamp: now, Type: ChangeAgentOutput, Session: "s1"})

	changes := tracker.SinceByType(now.Add(-3*time.Second), ChangeAgentOutput)
	if len(changes) != 2 {
		t.Errorf("expected 2 agent_output changes, got %d", len(changes))
	}
}

func TestSinceBySession(t *testing.T) {
	tracker := New()

	now := time.Now()
	tracker.Record(StateChange{Timestamp: now.Add(-2 * time.Second), Type: ChangeAgentOutput, Session: "s1"})
	tracker.Record(StateChange{Timestamp: now.Add(-1 * time.Second), Type: ChangeAgentState, Session: "s2"})
	tracker.Record(StateChange{Timestamp: now, Type: ChangeAgentOutput, Session: "s1"})

	changes := tracker.SinceBySession(now.Add(-3*time.Second), "s1")
	if len(changes) != 2 {
		t.Errorf("expected 2 s1 changes, got %d", len(changes))
	}
}

func TestHelperFunctions(t *testing.T) {
	tracker := New()

	tracker.RecordAgentOutput("sess", "pane1", "hello world")
	tracker.RecordAgentState("sess", "pane1", "idle")
	tracker.RecordAlert("sess", "pane1", "error", "something went wrong")
	tracker.RecordPaneCreated("sess", "pane2", "claude")
	tracker.RecordSessionCreated("sess2")

	if tracker.Count() != 5 {
		t.Errorf("expected 5 changes from helpers, got %d", tracker.Count())
	}

	changes := tracker.All()

	// Check output change
	if changes[0].Type != ChangeAgentOutput {
		t.Error("first change should be agent_output")
	}
	if changes[0].Details["output_length"].(int) != 11 {
		t.Error("output length should be 11")
	}

	// Check state change
	if changes[1].Type != ChangeAgentState {
		t.Error("second change should be agent_state")
	}
	if changes[1].Details["state"].(string) != "idle" {
		t.Error("state should be 'idle'")
	}

	// Check alert
	if changes[2].Type != ChangeAlert {
		t.Error("third change should be alert")
	}
	if changes[2].Details["message"].(string) != "something went wrong" {
		t.Error("alert message mismatch")
	}

	// Check pane created
	if changes[3].Type != ChangePaneCreated {
		t.Error("fourth change should be pane_created")
	}
	if changes[3].Details["agent_type"].(string) != "claude" {
		t.Error("agent_type should be 'claude'")
	}

	// Check session created
	if changes[4].Type != ChangeSessionCreated {
		t.Error("fifth change should be session_created")
	}
}

func TestConcurrency(t *testing.T) {
	tracker := New()
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			tracker.Record(StateChange{Type: ChangeAgentOutput, Session: "test"})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = tracker.All()
			_ = tracker.Count()
			_ = tracker.Since(time.Now().Add(-1 * time.Hour))
		}
		done <- true
	}()

	<-done
	<-done

	// If we get here without deadlock or panic, concurrency is working
}

func TestPrune(t *testing.T) {
	tracker := NewWithConfig(100, 50*time.Millisecond)

	// Add old entries
	tracker.Record(StateChange{
		Timestamp: time.Now().Add(-100 * time.Millisecond),
		Type:      ChangeAgentOutput,
	})
	tracker.Record(StateChange{
		Timestamp: time.Now().Add(-80 * time.Millisecond),
		Type:      ChangeAgentOutput,
	})

	// Manually prune
	tracker.Prune()

	if tracker.Count() != 0 {
		t.Errorf("expected 0 after prune (all old), got %d", tracker.Count())
	}
}
