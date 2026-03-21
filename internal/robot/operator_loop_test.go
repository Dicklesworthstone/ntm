package robot

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// Attention Hint Tests (br-auag6: Terse attention indicators)
// These tests cover buildAttentionHintFromSummary which is unique to terse output
// =============================================================================

func TestBuildAttentionHintFromSummary_NilCase(t *testing.T) {
	t.Parallel()

	got := buildAttentionHintFromSummary(nil)
	if got != "clear" {
		t.Errorf("buildAttentionHintFromSummary(nil) = %q, want %q", got, "clear")
	}
}

func TestBuildAttentionHintFromSummary_ZeroEvents(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         0,
		ActionRequiredCount: 0,
		InterestingCount:    0,
	}
	got := buildAttentionHintFromSummary(summary)
	if got != "clear" {
		t.Errorf("buildAttentionHintFromSummary(zero events) = %q, want %q", got, "clear")
	}
}

func TestBuildAttentionHintFromSummary_OnlyActionRequired(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         3,
		ActionRequiredCount: 2,
		InterestingCount:    0,
	}
	got := buildAttentionHintFromSummary(summary)
	want := "2!action"
	if got != want {
		t.Errorf("buildAttentionHintFromSummary(2 action) = %q, want %q", got, want)
	}
}

func TestBuildAttentionHintFromSummary_OnlyInteresting(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         5,
		ActionRequiredCount: 0,
		InterestingCount:    5,
	}
	got := buildAttentionHintFromSummary(summary)
	want := "5?interest"
	if got != want {
		t.Errorf("buildAttentionHintFromSummary(5 interesting) = %q, want %q", got, want)
	}
}

func TestBuildAttentionHintFromSummary_MixedCounts(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         10,
		ActionRequiredCount: 3,
		InterestingCount:    7,
	}
	got := buildAttentionHintFromSummary(summary)
	want := "3!action 7?interest"
	if got != want {
		t.Errorf("buildAttentionHintFromSummary(mixed) = %q, want %q", got, want)
	}
}

func TestBuildAttentionHintFromSummary_AllBackground(t *testing.T) {
	t.Parallel()

	// Events exist but none are action_required or interesting (all background)
	summary := &SnapshotAttentionSummary{
		TotalEvents:         10,
		ActionRequiredCount: 0,
		InterestingCount:    0,
	}
	got := buildAttentionHintFromSummary(summary)
	if got != "clear" {
		t.Errorf("buildAttentionHintFromSummary(background only) = %q, want %q", got, "clear")
	}
}

func TestBuildAttentionHintFromSummary_LargeCounts(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         1000,
		ActionRequiredCount: 50,
		InterestingCount:    150,
	}
	got := buildAttentionHintFromSummary(summary)
	want := "50!action 150?interest"
	if got != want {
		t.Errorf("buildAttentionHintFromSummary(large counts) = %q, want %q", got, want)
	}
}

// =============================================================================
// Terse Output Attention Integration Tests (br-auag6)
// =============================================================================

func TestBuildAttentionHint_WithFeed(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	// No events - should be clear
	hint := buildAttentionHint()
	if hint != "clear" {
		t.Errorf("empty feed hint = %q, want %q", hint, "clear")
	}

	// Add action required event
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertAttentionRequired,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "critical alert",
	})

	hint = buildAttentionHint()
	if hint != "1!action" {
		t.Errorf("1 action hint = %q, want %q", hint, "1!action")
	}

	// Add interesting event
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "state change",
	})

	hint = buildAttentionHint()
	if hint != "1!action 1?interest" {
		t.Errorf("mixed hint = %q, want %q", hint, "1!action 1?interest")
	}
}

func TestBuildAttentionHint_NilFeed(t *testing.T) {
	t.Parallel()

	oldFeed := GetAttentionFeed()
	SetAttentionFeed(nil)
	defer SetAttentionFeed(oldFeed)

	hint := buildAttentionHint()
	if hint != "feed:unavail" {
		t.Errorf("nil feed hint = %q, want %q", hint, "feed:unavail")
	}
}

// =============================================================================
// Cursor Chaining Integration Tests (verifies operator loop cursor handoff)
// =============================================================================

func TestCursorChaining_BasicLoop(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	// Initial state - cursor 0
	events, cursor, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("initial replay error: %v", err)
	}
	if len(events) != 0 || cursor != 0 {
		t.Errorf("initial: events=%d cursor=%d, want 0, 0", len(events), cursor)
	}

	// Add event 1
	ev1 := feed.Append(AttentionEvent{Summary: "event 1"})

	// Replay from 0 gets event 1
	events, cursor, err = feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("replay from 0 error: %v", err)
	}
	if len(events) != 1 || cursor != ev1.Cursor {
		t.Errorf("after ev1: events=%d cursor=%d, want 1, %d", len(events), cursor, ev1.Cursor)
	}

	// Add event 2
	ev2 := feed.Append(AttentionEvent{Summary: "event 2"})

	// Replay from ev1.Cursor gets only event 2
	events, cursor, err = feed.Replay(ev1.Cursor, 100)
	if err != nil {
		t.Fatalf("replay from ev1 error: %v", err)
	}
	if len(events) != 1 || events[0].Cursor != ev2.Cursor {
		t.Errorf("chained: events=%d first=%d, want 1, %d", len(events), events[0].Cursor, ev2.Cursor)
	}

	// Replay from latest gets nothing
	events, _, err = feed.Replay(cursor, 100)
	if err != nil {
		t.Fatalf("replay from latest error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("from latest: events=%d, want 0", len(events))
	}
}

// =============================================================================
// Profile Options Field Tests (verifies profile field wiring)
// =============================================================================

func TestWaitOptions_ProfileFieldPresent(t *testing.T) {
	t.Parallel()

	opts := WaitOptions{
		Session:   "test",
		Condition: "idle",
		Profile:   "operator",
	}

	if opts.Profile != "operator" {
		t.Errorf("WaitOptions.Profile = %q, want %q", opts.Profile, "operator")
	}
}

func TestEventsOptions_ProfileFieldPresent(t *testing.T) {
	t.Parallel()

	opts := EventsOptions{
		Profile: "debug",
		Limit:   100,
	}

	if opts.Profile != "debug" {
		t.Errorf("EventsOptions.Profile = %q, want %q", opts.Profile, "debug")
	}
}

func TestDigestOptions_ProfileFieldPresent(t *testing.T) {
	t.Parallel()

	opts := DigestOptions{
		Profile: "minimal",
	}

	if opts.Profile != "minimal" {
		t.Errorf("DigestOptions.Profile = %q, want %q", opts.Profile, "minimal")
	}
}

func TestAttentionOptions_ProfileFieldPresent(t *testing.T) {
	t.Parallel()

	opts := AttentionOptions{
		Profile: "alerts",
	}

	if opts.Profile != "alerts" {
		t.Errorf("AttentionOptions.Profile = %q, want %q", opts.Profile, "alerts")
	}
}

// =============================================================================
// Snapshot Summary Category Counting
// =============================================================================

func TestBuildSnapshotAttentionSummary_CategoryBreakdown(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	feed.Append(AttentionEvent{Category: EventCategoryAlert, Actionability: ActionabilityActionRequired})
	feed.Append(AttentionEvent{Category: EventCategoryAlert, Actionability: ActionabilityActionRequired})
	feed.Append(AttentionEvent{Category: EventCategoryAgent, Actionability: ActionabilityInteresting})
	feed.Append(AttentionEvent{Category: EventCategoryMail, Actionability: ActionabilityInteresting})
	feed.Append(AttentionEvent{Category: EventCategoryPane, Actionability: ActionabilityBackground})

	summary := buildSnapshotAttentionSummary(feed)

	if summary.ByCategoryCount["alert"] != 2 {
		t.Errorf("ByCategoryCount[alert] = %d, want 2", summary.ByCategoryCount["alert"])
	}
	if summary.ByCategoryCount["agent"] != 1 {
		t.Errorf("ByCategoryCount[agent] = %d, want 1", summary.ByCategoryCount["agent"])
	}
	if summary.ByCategoryCount["mail"] != 1 {
		t.Errorf("ByCategoryCount[mail] = %d, want 1", summary.ByCategoryCount["mail"])
	}
	if summary.ByCategoryCount["pane"] != 1 {
		t.Errorf("ByCategoryCount[pane] = %d, want 1", summary.ByCategoryCount["pane"])
	}
}

// =============================================================================
// JSON Serialization Tests for Operator Loop Structures
// =============================================================================

func TestSnapshotAttentionItem_JSONSerialization(t *testing.T) {
	t.Parallel()

	item := SnapshotAttentionItem{
		Cursor:        42,
		Category:      "alert",
		Actionability: "action_required",
		Severity:      "critical",
		Summary:       "test summary",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded SnapshotAttentionItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Cursor != item.Cursor {
		t.Errorf("Cursor = %d, want %d", decoded.Cursor, item.Cursor)
	}
	if decoded.Summary != item.Summary {
		t.Errorf("Summary = %q, want %q", decoded.Summary, item.Summary)
	}
	if decoded.Actionability != item.Actionability {
		t.Errorf("Actionability = %q, want %q", decoded.Actionability, item.Actionability)
	}
}

func TestSnapshotAttentionSummary_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	summary := &SnapshotAttentionSummary{
		TotalEvents:         10,
		ActionRequiredCount: 3,
		InterestingCount:    5,
		ByCategoryCount: map[string]int{
			"alert": 4,
			"agent": 6,
		},
		TopItems: []SnapshotAttentionItem{
			{Cursor: 1, Summary: "item 1"},
			{Cursor: 2, Summary: "item 2"},
		},
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded SnapshotAttentionSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.TotalEvents != summary.TotalEvents {
		t.Errorf("TotalEvents = %d, want %d", decoded.TotalEvents, summary.TotalEvents)
	}
	if decoded.ActionRequiredCount != summary.ActionRequiredCount {
		t.Errorf("ActionRequiredCount = %d, want %d", decoded.ActionRequiredCount, summary.ActionRequiredCount)
	}
	if len(decoded.TopItems) != len(summary.TopItems) {
		t.Errorf("TopItems length = %d, want %d", len(decoded.TopItems), len(summary.TopItems))
	}
}

// =============================================================================
// Dashboard Output Attention Integration Tests (br-auag6)
// =============================================================================

func TestDashboardAttentionSummary_FromFeed(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	// Add events
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "test alert",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent change",
	})

	// Build summary
	summary := buildSnapshotAttentionSummary(feed)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}

	// Verify counts
	if summary.TotalEvents != 2 {
		t.Errorf("TotalEvents = %d, want 2", summary.TotalEvents)
	}
	if summary.ActionRequiredCount != 1 {
		t.Errorf("ActionRequiredCount = %d, want 1", summary.ActionRequiredCount)
	}
	if summary.InterestingCount != 1 {
		t.Errorf("InterestingCount = %d, want 1", summary.InterestingCount)
	}

	// Verify JSON serialization round-trips
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("JSON marshal error: %v", err)
	}

	var decoded SnapshotAttentionSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}

	if decoded.TotalEvents != summary.TotalEvents {
		t.Errorf("decoded TotalEvents = %d, want %d", decoded.TotalEvents, summary.TotalEvents)
	}
}
