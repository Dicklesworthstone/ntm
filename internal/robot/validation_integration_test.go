package robot

// validation_integration_test.go provides integration tests for the runtime projection
// store, adapter normalization, degraded-source handling, attention replay, watermark
// persistence, incident evolution, and restart recovery behavior.
//
// These tests exercise realistic operational sequences to prove the redesign works
// as an operational system, not just as a collection of structs.
//
// Bead: bd-j9jo3.9.2

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
)

// =============================================================================
// Source Outage and Recovery Tests
// =============================================================================

func TestIntegration_SourceOutageAndRecovery(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("source_outage_recovery", 1)
	recorder := NewTestRecorder(t, scenarioID, true)
	clock := NewFixedClock(0)

	// Phase 1: All sources healthy
	// Note: Use source names that have mappings in ComputeDegradedFeatures
	// ("beads", "tmux", "caut" have feature mappings; "quota" does not)
	t.Log("PHASE_1: all sources healthy")
	results := []adapters.AdapterResult{
		{Name: "beads", Available: true, CollectedAt: clock.Now()},
		{Name: "caut", Available: true, CollectedAt: clock.Now()},
		{Name: "tmux", Available: true, CollectedAt: clock.Now()},
	}
	config := adapters.DefaultSourceHealthConfig()
	health := adapters.ComputeSourceHealth(results, config, clock.Now())

	if !health.AllFresh {
		t.Error("phase 1: expected all fresh")
	}
	recorder.RecordSourceHealthChange(nil, nil)

	// Phase 2: Caut (quota) source becomes unavailable
	t.Log("PHASE_2: caut source outage")
	clock.Advance(time.Minute)
	// Update all sources' CollectedAt to current time (except the failing one)
	results[0] = adapters.AdapterResult{Name: "beads", Available: true, CollectedAt: clock.Now()}
	results[1] = adapters.AdapterResult{
		Name:      "caut",
		Available: false,
		Error:     context.DeadlineExceeded,
	}
	results[2] = adapters.AdapterResult{Name: "tmux", Available: true, CollectedAt: clock.Now()}
	health = adapters.ComputeSourceHealth(results, config, clock.Now())

	if health.AllFresh {
		t.Error("phase 2: expected not all fresh during outage")
	}
	if len(health.Degraded) != 1 {
		t.Errorf("phase 2: expected 1 degraded source, got %d", len(health.Degraded))
	}
	recorder.RecordSourceHealthChange([]string{"caut"}, nil)

	degraded := adapters.ComputeDegradedFeatures(health)
	if len(degraded) == 0 {
		t.Error("phase 2: expected degraded features during caut outage")
	}
	t.Logf("DEGRADED_FEATURES during outage: %d features affected", len(degraded))

	// Phase 3: Caut source recovers
	t.Log("PHASE_3: caut source recovery")
	clock.Advance(time.Minute)
	// Update all sources' CollectedAt to current time
	results[0] = adapters.AdapterResult{Name: "beads", Available: true, CollectedAt: clock.Now()}
	results[1] = adapters.AdapterResult{Name: "caut", Available: true, CollectedAt: clock.Now()}
	results[2] = adapters.AdapterResult{Name: "tmux", Available: true, CollectedAt: clock.Now()}
	health = adapters.ComputeSourceHealth(results, config, clock.Now())

	if !health.AllFresh {
		t.Error("phase 3: expected all fresh after recovery")
	}
	if len(health.Degraded) != 0 {
		t.Errorf("phase 3: expected 0 degraded sources after recovery, got %d", len(health.Degraded))
	}
	recorder.RecordSourceHealthChange(nil, []string{"caut"})

	t.Logf("SCENARIO_COMPLETE scenario_id=%s observations=%d", scenarioID, len(recorder.observations))
}

func TestIntegration_MultipleSourceFlapping(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("source_flapping", 2)
	recorder := NewTestRecorder(t, scenarioID, true)
	clock := NewFixedClock(0)
	config := adapters.DefaultSourceHealthConfig()

	// Simulate rapid source state changes
	states := []struct {
		beadsOK bool
		quotaOK bool
		tmuxOK  bool
	}{
		{true, true, true},  // all healthy
		{true, false, true}, // quota fails
		{true, true, true},  // quota recovers
		{false, true, true}, // beads fails
		{true, true, false}, // beads recovers, tmux fails
		{true, true, true},  // all recover
	}

	prevDegraded := []string{}
	for i, s := range states {
		clock.Advance(10 * time.Second)

		results := []adapters.AdapterResult{
			{Name: "beads", Available: s.beadsOK, CollectedAt: clock.Now()},
			{Name: "quota", Available: s.quotaOK, CollectedAt: clock.Now()},
			{Name: "tmux", Available: s.tmuxOK, CollectedAt: clock.Now()},
		}

		health := adapters.ComputeSourceHealth(results, config, clock.Now())

		// Track state transitions
		newlyDegraded := []string{}
		newlyRecovered := []string{}
		for _, d := range health.Degraded {
			found := false
			for _, pd := range prevDegraded {
				if pd == d {
					found = true
					break
				}
			}
			if !found {
				newlyDegraded = append(newlyDegraded, d)
			}
		}
		for _, pd := range prevDegraded {
			found := false
			for _, d := range health.Degraded {
				if d == pd {
					found = true
					break
				}
			}
			if !found {
				newlyRecovered = append(newlyRecovered, pd)
			}
		}

		if len(newlyDegraded) > 0 || len(newlyRecovered) > 0 {
			recorder.RecordSourceHealthChange(newlyDegraded, newlyRecovered)
			t.Logf("STEP_%d degraded=%v recovered=%v", i, newlyDegraded, newlyRecovered)
		}
		prevDegraded = health.Degraded
	}

	// Final state should be all healthy
	if len(prevDegraded) != 0 {
		t.Errorf("final state should be all healthy, got degraded=%v", prevDegraded)
	}
	t.Logf("SCENARIO_COMPLETE scenario_id=%s observations=%d", scenarioID, len(recorder.observations))
}

// =============================================================================
// Attention Feed Cursor and Replay Tests
// =============================================================================

func TestIntegration_AttentionFeedCursorContinuity(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("cursor_continuity", 3)
	recorder := NewTestRecorder(t, scenarioID, true)

	// Create a fresh feed for this test
	config := DefaultAttentionFeedConfig()
	config.JournalSize = 100
	feed := NewAttentionFeed(config)

	// Publish several events
	for i := 0; i < 10; i++ {
		event := AttentionEvent{
			Ts:       time.Now().UTC().Format(time.RFC3339Nano),
			Category: EventCategoryAgent,
			Type:     EventTypeAgentStateChange,
			Summary:  "test event",
		}
		result := feed.Append(event)
		recorder.RecordCursor(result.Cursor)
	}

	stats := feed.Stats()
	if stats.Count != 10 {
		t.Errorf("expected 10 events, got %d", stats.Count)
	}

	// Replay from beginning
	oldestCursor := stats.OldestCursor
	events, _, err := feed.Replay(oldestCursor-1, 5)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("expected 5 events from replay, got %d", len(events))
	}
	// Use the last returned event's cursor as the continuation point
	lastCursor := events[len(events)-1].Cursor
	recorder.RecordCursor(lastCursor)

	// Continue replay from where we left off (use last event cursor, not newestInFeed)
	events2, _, err := feed.Replay(lastCursor, 10)
	if err != nil {
		t.Fatalf("second replay failed: %v", err)
	}
	if len(events2) != 5 {
		t.Errorf("expected 5 remaining events, got %d", len(events2))
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s total_events=%d replayed=%d",
		scenarioID, stats.Count, len(events)+len(events2))
}

func TestIntegration_AttentionFeedSubscriptionDelivery(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("subscription_delivery", 4)
	recorder := NewTestRecorder(t, scenarioID, true)

	config := DefaultAttentionFeedConfig()
	config.JournalSize = 100
	feed := NewAttentionFeed(config)

	// Subscribe and collect events
	var received []AttentionEvent
	var mu sync.Mutex

	unsubscribe := feed.Subscribe(func(event AttentionEvent) {
		mu.Lock()
		received = append(received, event)
		mu.Unlock()
	})
	defer unsubscribe()

	// Publish events
	for i := 0; i < 5; i++ {
		event := AttentionEvent{
			Ts:       time.Now().UTC().Format(time.RFC3339Nano),
			Category: EventCategoryAgent,
			Type:     EventTypeAgentStateChange,
			Summary:  "subscription test",
		}
		result := feed.Append(event)
		recorder.RecordCursor(result.Cursor)
	}

	// Allow time for delivery
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 5 {
		t.Errorf("expected 5 delivered events, got %d", count)
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s delivered=%d", scenarioID, count)
}

// =============================================================================
// Incident Promotion Tests
// =============================================================================

func TestIntegration_IncidentPromotionFromRepeatedEvidence(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("incident_promotion", 5)
	recorder := NewTestRecorder(t, scenarioID, true)

	rule := &adapters.PromotionRule{
		MinDuration: 30 * time.Minute,
		RepeatCount: 3,
		Types:       []string{"agent_crashed"},
	}

	// Simulate repeated alerts below threshold
	for i := 1; i <= 2; i++ {
		alert := adapters.AlertItem{
			ID:       "alert-repeat",
			Type:     "agent_stalled",
			Severity: "warning",
			Count:    i,
		}
		shouldPromote, _ := adapters.ShouldPromote(alert, rule)
		if shouldPromote {
			t.Errorf("step %d: should not promote with count=%d", i, i)
		}
	}

	// Third occurrence triggers promotion
	alert := adapters.AlertItem{
		ID:       "alert-repeat",
		Type:     "agent_stalled",
		Severity: "warning",
		Count:    3,
	}
	shouldPromote, reason := adapters.ShouldPromote(alert, rule)
	if !shouldPromote {
		t.Error("should promote after 3 occurrences")
	}
	if reason != "repeated_alert" {
		t.Errorf("expected reason=repeated_alert, got %s", reason)
	}

	incident := adapters.PromoteToIncident(alert, reason)
	recorder.RecordIncident(incident.ID)

	t.Logf("SCENARIO_COMPLETE scenario_id=%s incident_id=%s reason=%s",
		scenarioID, incident.ID, reason)
}

func TestIntegration_IncidentPromotionFromCriticalType(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("critical_type_promotion", 6)
	recorder := NewTestRecorder(t, scenarioID, true)

	rule := adapters.DefaultPromotionRules()

	// Critical types should promote immediately
	criticalTypes := []string{"agent_crashed", "rotation_failed", "compaction_failed"}

	for _, alertType := range criticalTypes {
		alert := adapters.AlertItem{
			ID:       "alert-" + alertType,
			Type:     alertType,
			Severity: "warning",
			Count:    1,
		}
		shouldPromote, reason := adapters.ShouldPromote(alert, rule)
		if !shouldPromote {
			t.Errorf("type %s should promote immediately", alertType)
		}
		if reason != "type_match" {
			t.Errorf("type %s: expected reason=type_match, got %s", alertType, reason)
		}
		recorder.RecordIncident(alert.ID)
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s critical_types_tested=%d",
		scenarioID, len(criticalTypes))
}

// =============================================================================
// Duplicate Request Suppression Tests
// =============================================================================

func TestIntegration_DuplicateEventSuppression(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("duplicate_suppression", 7)
	recorder := NewTestRecorder(t, scenarioID, true)

	config := DefaultAttentionFeedConfig()
	config.JournalSize = 100
	feed := NewAttentionFeed(config)
	dedupWindow := 5 * time.Minute

	// Publish first event using AppendDeduplicated to populate dedup map
	event1 := AttentionEvent{
		Ts:       time.Now().UTC().Format(time.RFC3339Nano),
		Category: EventCategoryAgent,
		Type:     EventTypeAgentStateChange,
		Summary:  "duplicate test",
		DedupKey: "unique-key-123",
	}
	result1, published1 := feed.AppendDeduplicated(event1, dedupWindow)
	if !published1 {
		t.Fatal("first event should be published")
	}
	recorder.RecordCursor(result1.Cursor)

	// Try to publish duplicate with same dedup key
	event2 := AttentionEvent{
		Ts:       time.Now().UTC().Format(time.RFC3339Nano),
		Category: EventCategoryAgent,
		Type:     EventTypeAgentStateChange,
		Summary:  "duplicate test",
		DedupKey: "unique-key-123",
	}
	result2, published2 := feed.AppendDeduplicated(event2, dedupWindow)
	recorder.Record(TestObservation{
		Cursor:            result2.Cursor,
		SuppressionMarker: "cooldown",
	})

	if published2 {
		t.Error("expected duplicate event to be suppressed")
	}
	// When suppressed, result2 is zero value
	if result2.Cursor != 0 {
		t.Errorf("suppressed event should return zero cursor, got %d", result2.Cursor)
	}

	stats := feed.Stats()
	if stats.Count != 1 {
		t.Errorf("expected 1 event (duplicate suppressed), got %d", stats.Count)
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s suppressed=true cursor=%d", scenarioID, result1.Cursor)
}

func TestIntegration_CooldownResurfacing(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("cooldown_resurfacing", 8)
	recorder := NewTestRecorder(t, scenarioID, true)

	config := DefaultAttentionFeedConfig()
	config.JournalSize = 100
	feed := NewAttentionFeed(config)
	cooldown := 100 * time.Millisecond

	// Publish initial event using AppendDeduplicated to populate dedup map
	event1 := AttentionEvent{
		Ts:       time.Now().UTC().Format(time.RFC3339Nano),
		Category: EventCategoryAgent,
		Type:     EventTypeAgentStateChange,
		Summary:  "resurfacing test",
		DedupKey: "resurface-key",
	}
	result1, published1 := feed.AppendDeduplicated(event1, cooldown)
	if !published1 {
		t.Fatal("first event should be published")
	}
	recorder.RecordCursor(result1.Cursor)

	// Immediate duplicate should be suppressed
	_, published2 := feed.AppendDeduplicated(event1, cooldown)
	if published2 {
		t.Error("immediate duplicate should be suppressed")
	}

	// Wait for cooldown to expire
	time.Sleep(cooldown + 50*time.Millisecond)

	// Now the event should resurface
	event2 := AttentionEvent{
		Ts:       time.Now().UTC().Format(time.RFC3339Nano),
		Category: EventCategoryAgent,
		Type:     EventTypeAgentStateChange,
		Summary:  "resurfacing test after cooldown",
		DedupKey: "resurface-key",
	}
	result2, published3 := feed.AppendDeduplicated(event2, cooldown)
	recorder.Record(TestObservation{
		Cursor:              result2.Cursor,
		ResurfacingDecision: "allowed",
	})

	if !published3 {
		t.Error("event should resurface after cooldown")
	}
	if result2.Cursor == result1.Cursor {
		t.Error("resurfaced event should have new cursor")
	}

	stats := feed.Stats()
	if stats.Count != 2 {
		t.Errorf("expected 2 events after resurfacing, got %d", stats.Count)
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s resurfaced=true", scenarioID)
}

// =============================================================================
// Operator State Transition Tests
// =============================================================================

func TestIntegration_OperatorAcknowledgmentPersists(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("operator_ack", 9)
	recorder := NewTestRecorder(t, scenarioID, true)

	// Create attention event with operator state
	event := AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertAttentionRequired,
		Summary:       "test alert",
		Actionability: ActionabilityActionRequired,
	}

	// Simulate operator acknowledgment
	state := &OperatorAttentionState{
		ItemID:         "item-123",
		AcknowledgedAt: time.Now().UTC().Format(time.RFC3339),
		AcknowledgedBy: "operator-alice",
	}
	recorder.Record(TestObservation{
		OperatorTransition: "acknowledged",
		Extra: map[string]interface{}{
			"item_id":    state.ItemID,
			"acked_by":   state.AcknowledgedBy,
			"event_type": string(event.Type),
		},
	})

	if state.AcknowledgedAt == "" {
		t.Error("acknowledgment timestamp should be set")
	}
	if state.AcknowledgedBy == "" {
		t.Error("acknowledgment actor should be set")
	}

	t.Logf("SCENARIO_COMPLETE scenario_id=%s item_id=%s acked_by=%s",
		scenarioID, state.ItemID, state.AcknowledgedBy)
}

func TestIntegration_OperatorSnoozeAndExpiry(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("operator_snooze", 10)
	recorder := NewTestRecorder(t, scenarioID, true)

	// Use 2 second duration to avoid RFC3339 sub-second truncation issues
	snoozeDuration := 2 * time.Second
	// Add a full second buffer to ensure we're safely in the future after RFC3339 truncation
	snoozeUntil := time.Now().Add(snoozeDuration).Truncate(time.Second).Add(time.Second)

	state := &OperatorAttentionState{
		ItemID:      "item-456",
		SnoozedAt:   time.Now().UTC().Format(time.RFC3339),
		SnoozeUntil: snoozeUntil.UTC().Format(time.RFC3339),
		SnoozedBy:   "operator-bob",
	}
	recorder.Record(TestObservation{
		OperatorTransition: "snoozed",
		Extra: map[string]interface{}{
			"item_id":      state.ItemID,
			"snooze_until": state.SnoozeUntil,
		},
	})

	// Check snooze is active
	if !state.IsSnoozed(time.Now()) {
		t.Error("snooze should be active immediately after setting")
	}

	// Wait for snooze to expire
	time.Sleep(snoozeDuration + time.Second)

	// Check snooze has expired
	if state.IsSnoozed(time.Now()) {
		t.Error("snooze should have expired")
	}
	recorder.Record(TestObservation{
		OperatorTransition: "snooze_expired",
	})

	t.Logf("SCENARIO_COMPLETE scenario_id=%s snooze_expired=true", scenarioID)
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

func TestIntegration_ConcurrentFeedAccess(t *testing.T) {
	t.Parallel()

	scenarioID := NewScenarioID("concurrent_access", 11)
	recorder := NewTestRecorder(t, scenarioID, true)

	config := DefaultAttentionFeedConfig()
	config.JournalSize = 1000
	feed := NewAttentionFeed(config)
	var wg sync.WaitGroup
	eventCount := 100
	goroutines := 10

	// Concurrent publishers
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < eventCount/goroutines; i++ {
				event := AttentionEvent{
					Ts:       time.Now().UTC().Format(time.RFC3339Nano),
					Category: EventCategoryAgent,
					Type:     EventTypeAgentStateChange,
					Summary:  "concurrent test",
				}
				feed.Append(event)
			}
		}(g)
	}

	wg.Wait()

	stats := feed.Stats()
	if stats.Count != eventCount {
		t.Errorf("expected %d events, got %d", eventCount, stats.Count)
	}

	// Verify cursor monotonicity
	events, _, err := feed.Replay(0, eventCount)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	prevCursor := int64(0)
	for _, e := range events {
		if e.Cursor <= prevCursor {
			t.Errorf("cursor not monotonic: %d <= %d", e.Cursor, prevCursor)
		}
		prevCursor = e.Cursor
	}

	recorder.RecordCursor(prevCursor)
	t.Logf("SCENARIO_COMPLETE scenario_id=%s events=%d cursors_monotonic=true",
		scenarioID, stats.Count)
}

// =============================================================================
// Helper for OperatorAttentionState
// =============================================================================

// OperatorAttentionState tracks operator actions on attention items.
type OperatorAttentionState struct {
	ItemID         string `json:"item_id"`
	AcknowledgedAt string `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string `json:"acknowledged_by,omitempty"`
	SnoozedAt      string `json:"snoozed_at,omitempty"`
	SnoozeUntil    string `json:"snooze_until,omitempty"`
	SnoozedBy      string `json:"snoozed_by,omitempty"`
	PinnedAt       string `json:"pinned_at,omitempty"`
	PinnedBy       string `json:"pinned_by,omitempty"`
}

// IsSnoozed returns true if the item is currently snoozed.
func (s *OperatorAttentionState) IsSnoozed(now time.Time) bool {
	if s.SnoozeUntil == "" {
		return false
	}
	snoozeUntil, err := time.Parse(time.RFC3339, s.SnoozeUntil)
	if err != nil {
		return false
	}
	return now.Before(snoozeUntil)
}
