package robot

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

func newTestAttentionFeed(t *testing.T) *AttentionFeed {
	t.Helper()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	t.Cleanup(feed.Stop)
	return feed
}

func mustLoggedAttentionEvent(t *testing.T, event ntmevents.Event) AttentionEvent {
	t.Helper()

	normalized, ok := NewLoggedAttentionEvent(event)
	if !ok {
		t.Fatalf("expected logged event %q to normalize", event.Type)
	}
	return normalized
}

func mustBusAttentionEvent(t *testing.T, event ntmevents.BusEvent) AttentionEvent {
	t.Helper()

	normalized, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatalf("expected bus event %T to normalize", event)
	}
	return normalized
}

func digestTestEvent(cursor int64, category EventCategory, eventType EventType, actionability Actionability, severity Severity, summary string) AttentionEvent {
	return AttentionEvent{
		Cursor:        cursor,
		Ts:            time.Date(2026, 3, 21, 4, 0, 0, 0, time.UTC).Add(time.Duration(cursor) * time.Second).Format(time.RFC3339Nano),
		Session:       "proj",
		Pane:          2,
		Category:      category,
		Type:          eventType,
		Actionability: actionability,
		Severity:      severity,
		Summary:       summary,
		Details: map[string]any{
			"pane_ref": "2",
		},
	}
}

// =============================================================================
// Cursor Allocator Tests
// =============================================================================

func TestCursorAllocator_Monotonic(t *testing.T) {
	alloc := NewCursorAllocator()

	// Cursors must be strictly increasing
	prev := int64(0)
	for i := 0; i < 1000; i++ {
		cur := alloc.Next()
		if cur <= prev {
			t.Errorf("cursor %d not greater than previous %d", cur, prev)
		}
		prev = cur
	}
}

func TestCursorAllocator_Current(t *testing.T) {
	alloc := NewCursorAllocator()

	// Current returns 0 before any allocations
	if got := alloc.Current(); got != 0 {
		t.Errorf("Current() before allocation = %d, want 0", got)
	}

	// Current returns the last allocated cursor
	c1 := alloc.Next()
	if got := alloc.Current(); got != c1 {
		t.Errorf("Current() after Next() = %d, want %d", got, c1)
	}

	c2 := alloc.Next()
	if got := alloc.Current(); got != c2 {
		t.Errorf("Current() after second Next() = %d, want %d", got, c2)
	}
}

func TestCursorAllocator_Concurrent(t *testing.T) {
	alloc := NewCursorAllocator()
	const goroutines = 100
	const iterations = 100

	seen := make(map[int64]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c := alloc.Next()
				mu.Lock()
				if seen[c] {
					t.Errorf("duplicate cursor %d", c)
				}
				seen[c] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	expected := goroutines * iterations
	if len(seen) != expected {
		t.Errorf("got %d unique cursors, want %d", len(seen), expected)
	}

	// Verify cursors are monotonic (all unique and starting from 1)
	for i := int64(1); i <= int64(expected); i++ {
		if !seen[i] {
			t.Errorf("cursor %d not allocated", i)
		}
	}
}

// =============================================================================
// Attention Journal Tests
// =============================================================================

func TestAttentionJournal_AppendAndReplay(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Append some events
	events := []AttentionEvent{
		{Cursor: 1, Ts: "2026-03-20T10:00:00Z", Summary: "Event 1"},
		{Cursor: 2, Ts: "2026-03-20T10:00:01Z", Summary: "Event 2"},
		{Cursor: 3, Ts: "2026-03-20T10:00:02Z", Summary: "Event 3"},
	}
	for _, e := range events {
		journal.Append(e)
	}

	// Replay from start (cursor 0)
	got, newest, err := journal.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay(0) error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Replay(0) returned %d events, want 3", len(got))
	}
	if newest != 3 {
		t.Errorf("newest cursor = %d, want 3", newest)
	}

	// Replay from cursor 1 (should get events 2 and 3)
	got, _, err = journal.Replay(1, 100)
	if err != nil {
		t.Fatalf("Replay(1) error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Replay(1) returned %d events, want 2", len(got))
	}

	// Replay from cursor -1 (start from "now")
	got, newest, err = journal.Replay(-1, 100)
	if err != nil {
		t.Fatalf("Replay(-1) error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Replay(-1) returned %d events, want 0", len(got))
	}
	if newest != 3 {
		t.Errorf("newest cursor from Replay(-1) = %d, want 3", newest)
	}
}

func TestAttentionJournal_Limit(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Append 10 events
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Replay with limit
	got, _, err := journal.Replay(0, 5)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d events, want 5", len(got))
	}

	// Events should be oldest first
	if got[0].Cursor != 1 {
		t.Errorf("first event cursor = %d, want 1", got[0].Cursor)
	}
}

func TestAttentionJournal_Wraparound(t *testing.T) {
	journal := NewAttentionJournal(5, time.Hour)

	// Append 10 events (will wrap around)
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Should only have the last 5 events
	stats := journal.Stats()
	if stats.Count != 5 {
		t.Errorf("count = %d, want 5", stats.Count)
	}
	if stats.OldestCursor < 6 {
		t.Errorf("oldest cursor = %d, want >= 6", stats.OldestCursor)
	}
	if stats.NewestCursor != 10 {
		t.Errorf("newest cursor = %d, want 10", stats.NewestCursor)
	}

	// Replay all should return 5 events
	got, _, err := journal.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d events, want 5", len(got))
	}
}

func TestAttentionJournal_CursorExpired(t *testing.T) {
	journal := NewAttentionJournal(5, time.Hour)

	// Append events to cause wraparound
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Try to replay from expired cursor
	_, _, err := journal.Replay(3, 100)
	if err == nil {
		t.Fatal("expected CursorExpiredError, got nil")
	}

	expErr, ok := err.(*CursorExpiredError)
	if !ok {
		t.Fatalf("expected *CursorExpiredError, got %T", err)
	}

	if expErr.RequestedCursor != 3 {
		t.Errorf("RequestedCursor = %d, want 3", expErr.RequestedCursor)
	}
	if expErr.EarliestCursor < 6 {
		t.Errorf("EarliestCursor = %d, want >= 6", expErr.EarliestCursor)
	}

	// Verify ToDetails
	details := expErr.ToDetails()
	if details.RequestedCursor != 3 {
		t.Errorf("details.RequestedCursor = %d, want 3", details.RequestedCursor)
	}
	if details.ResyncCommand != "ntm --robot-snapshot" {
		t.Errorf("unexpected ResyncCommand: %s", details.ResyncCommand)
	}
}

func TestAttentionJournal_Stats(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Initial stats
	stats := journal.Stats()
	if stats.Size != 100 {
		t.Errorf("Size = %d, want 100", stats.Size)
	}
	if stats.Count != 0 {
		t.Errorf("Count = %d, want 0", stats.Count)
	}

	// After appending
	journal.Append(AttentionEvent{Cursor: 1})
	journal.Append(AttentionEvent{Cursor: 2})

	stats = journal.Stats()
	if stats.Count != 2 {
		t.Errorf("Count = %d, want 2", stats.Count)
	}
	if stats.TotalAppended != 2 {
		t.Errorf("TotalAppended = %d, want 2", stats.TotalAppended)
	}
}

// =============================================================================
// Attention Feed Tests
// =============================================================================

func TestAttentionFeed_Append(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0, // Disable heartbeats for tests
	})
	defer feed.Stop()

	// Append event without cursor (feed assigns it)
	event := AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Summary:       "Test event",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
	}

	result := feed.Append(event)

	// Cursor should be assigned
	if result.Cursor == 0 {
		t.Error("cursor not assigned")
	}

	// Timestamp should be set
	if result.Ts == "" {
		t.Error("timestamp not set")
	}

	// Should be replayable
	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}
}

func TestAttentionFeed_Subscribe(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	received := make([]AttentionEvent, 0)
	var mu sync.Mutex

	unsub := feed.Subscribe(func(e AttentionEvent) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	// Append events
	feed.Append(AttentionEvent{Summary: "Event 1"})
	feed.Append(AttentionEvent{Summary: "Event 2"})

	mu.Lock()
	if len(received) != 2 {
		t.Errorf("received %d events, want 2", len(received))
	}
	mu.Unlock()

	// Unsubscribe
	unsub()

	// Further events should not be received
	feed.Append(AttentionEvent{Summary: "Event 3"})

	mu.Lock()
	if len(received) != 2 {
		t.Errorf("received %d events after unsub, want 2", len(received))
	}
	mu.Unlock()
}

func TestAttentionFeed_CurrentCursor(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	if got := feed.CurrentCursor(); got != 0 {
		t.Errorf("CurrentCursor() before any events = %d, want 0", got)
	}

	e1 := feed.Append(AttentionEvent{Summary: "Event 1"})
	if got := feed.CurrentCursor(); got != e1.Cursor {
		t.Errorf("CurrentCursor() = %d, want %d", got, e1.Cursor)
	}

	e2 := feed.Append(AttentionEvent{Summary: "Event 2"})
	if got := feed.CurrentCursor(); got != e2.Cursor {
		t.Errorf("CurrentCursor() = %d, want %d", got, e2.Cursor)
	}
}

func TestAttentionFeed_ConcurrentAppend(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				feed.Append(AttentionEvent{Summary: "Concurrent event"})
			}
		}()
	}
	wg.Wait()

	// All events should be in journal
	stats := feed.Stats()
	expected := int64(goroutines * iterations)
	if stats.TotalAppended != expected {
		t.Errorf("TotalAppended = %d, want %d", stats.TotalAppended, expected)
	}
}

func TestAttentionFeed_SubscriberPanicRecovery(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	var received atomic.Int32

	// Subscriber that panics
	feed.Subscribe(func(e AttentionEvent) {
		panic("test panic")
	})

	// Good subscriber
	feed.Subscribe(func(e AttentionEvent) {
		received.Add(1)
	})

	// Append should not crash despite panicking subscriber
	feed.Append(AttentionEvent{Summary: "Event 1"})

	if got := received.Load(); got != 1 {
		t.Errorf("good subscriber received = %d, want 1", got)
	}
}

func TestAttentionFeed_PublishTrackerChange(t *testing.T) {
	feed := newTestAttentionFeed(t)

	change := tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 5, 0, 0, time.UTC),
		Type:      tracker.ChangeAgentOutput,
		Session:   "proj",
		Pane:      "2",
		Details: map[string]interface{}{
			"line_count": 3,
		},
	}

	published := feed.PublishTrackerChange(change)
	t.Logf("published tracker event cursor=%d type=%s summary=%q", published.Cursor, published.Type, published.Summary)

	if published.Cursor == 0 {
		t.Fatal("tracker change was not assigned a cursor")
	}
	if published.Type != EventTypePaneOutput {
		t.Fatalf("tracker change type = %q, want %q", published.Type, EventTypePaneOutput)
	}
	if published.Pane != 2 {
		t.Fatalf("tracker change pane = %d, want 2", published.Pane)
	}
	if published.Details["pane_ref"] != "2" {
		t.Fatalf("tracker change pane_ref = %#v, want %q", published.Details["pane_ref"], "2")
	}

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d tracker events, want 1", len(replayed))
	}
	if replayed[0].Summary != published.Summary {
		t.Fatalf("replayed summary = %q, want %q", replayed[0].Summary, published.Summary)
	}
}

func TestAttentionFeed_PublishLoggedEvent_Suppressed(t *testing.T) {
	feed := newTestAttentionFeed(t)

	published, ok := feed.PublishLoggedEvent(ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 6, 0, 0, time.UTC),
		Type:      ntmevents.EventPromptSend,
		Session:   "proj",
		Data: map[string]interface{}{
			"pane_index": 1,
		},
	})
	t.Logf("suppressed logged event ok=%v published=%+v", ok, published)

	if ok {
		t.Fatal("prompt_send should be suppressed from the attention feed")
	}
	if stats := feed.Stats(); stats.TotalAppended != 0 {
		t.Fatalf("TotalAppended = %d, want 0 for suppressed logged event", stats.TotalAppended)
	}
}

func TestAttentionFeed_PublishBusHistoryOrdersOldestFirst(t *testing.T) {
	feed := newTestAttentionFeed(t)
	bus := ntmevents.NewEventBus(10)

	first := ntmevents.AlertEvent{
		BaseEvent: ntmevents.BaseEvent{
			Type:      "alert",
			Timestamp: time.Date(2026, 3, 21, 3, 7, 0, 0, time.UTC),
			Session:   "proj",
		},
		AlertID:   "alert-1",
		AlertType: "health",
		Severity:  "warning",
		Message:   "first warning",
	}
	second := ntmevents.AgentStallEvent{
		BaseEvent: ntmevents.BaseEvent{
			Type:      "agent_stall",
			Timestamp: time.Date(2026, 3, 21, 3, 8, 0, 0, time.UTC),
			Session:   "proj",
		},
		AgentID:       "cod-1",
		StallDuration: 45,
		LastActivity:  "waiting",
	}

	bus.PublishSync(first)
	bus.PublishSync(second)

	published := feed.PublishBusHistory(bus, 10)
	if len(published) != 2 {
		t.Fatalf("published %d bus history events, want 2", len(published))
	}
	t.Logf("published bus history summaries=%q then %q", published[0].Summary, published[1].Summary)
	if published[0].Details["alert_id"] != "alert-1" {
		t.Fatalf("first published history event = %#v, want alert-1 first", published[0].Details["alert_id"])
	}
	if published[1].Type != EventTypeAgentStalled {
		t.Fatalf("second published history type = %q, want %q", published[1].Type, EventTypeAgentStalled)
	}
	if published[0].Cursor >= published[1].Cursor {
		t.Fatalf("history cursors not increasing oldest-first: %d then %d", published[0].Cursor, published[1].Cursor)
	}
}

func TestAttentionFeed_SubscribeEventBus(t *testing.T) {
	feed := newTestAttentionFeed(t)
	bus := ntmevents.NewEventBus(10)

	unsubscribeBus := feed.SubscribeEventBus(bus)
	defer unsubscribeBus()

	bus.PublishSync(ntmevents.NewAgentErrorEvent("proj", "cc-1", "auth", "token expired"))

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d live bus events, want 1", len(replayed))
	}
	if replayed[0].Type != EventTypeAgentError {
		t.Fatalf("live bus event type = %q, want %q", replayed[0].Type, EventTypeAgentError)
	}

	unsubscribeBus()
	bus.PublishSync(ntmevents.NewAlertEvent("proj", "alert-2", "health", "warning", "after unsubscribe"))

	replayed, _, err = feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay after unsubscribe error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events after unsubscribe, want 1", len(replayed))
	}
}

// =============================================================================
// Event Builder Tests
// =============================================================================

func TestNewTrackerEvent_MailReceived(t *testing.T) {
	change := tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 9, 0, 0, time.UTC),
		Type:      tracker.ChangeMailReceived,
		Session:   "proj",
		Pane:      "1",
		Details: map[string]interface{}{
			"subject": "Need ack",
		},
	}

	event := NewTrackerEvent(change)
	if event.Category != EventCategoryMail {
		t.Fatalf("tracker mail category = %q, want %q", event.Category, EventCategoryMail)
	}
	if event.Type != EventTypeMailReceived {
		t.Fatalf("tracker mail type = %q, want %q", event.Type, EventTypeMailReceived)
	}
	if event.Pane != 1 {
		t.Fatalf("tracker mail pane = %d, want 1", event.Pane)
	}
}

func TestNewLoggedAttentionEvent_SessionCreate(t *testing.T) {
	event, ok := NewLoggedAttentionEvent(ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 10, 0, 0, time.UTC),
		Type:      ntmevents.EventSessionCreate,
		Session:   "proj",
	})
	if !ok {
		t.Fatal("session_create should map into the attention feed")
	}
	if event.Category != EventCategorySession {
		t.Fatalf("logged event category = %q, want %q", event.Category, EventCategorySession)
	}
	if event.Type != EventTypeSessionCreated {
		t.Fatalf("logged event type = %q, want %q", event.Type, EventTypeSessionCreated)
	}
	if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-status" {
		t.Fatalf("logged event next actions = %+v, want robot-status follow-up", event.NextActions)
	}
}

func TestNewBusAttentionEvent_AlertCritical(t *testing.T) {
	event, ok := NewBusAttentionEvent(ntmevents.NewAlertEvent("proj", "alert-3", "health", "critical", "disk full"))
	if !ok {
		t.Fatal("alert bus event should map into the attention feed")
	}
	if event.Category != EventCategoryAlert {
		t.Fatalf("bus alert category = %q, want %q", event.Category, EventCategoryAlert)
	}
	if event.Type != EventTypeAlertAttentionRequired {
		t.Fatalf("bus alert type = %q, want %q", event.Type, EventTypeAlertAttentionRequired)
	}
	if event.Severity != SeverityCritical {
		t.Fatalf("bus alert severity = %q, want %q", event.Severity, SeverityCritical)
	}
	if event.Actionability != ActionabilityActionRequired {
		t.Fatalf("bus alert actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
}

func TestAttentionSignalAnnotations_Table(t *testing.T) {
	timeline := time.Date(2026, 3, 21, 3, 20, 0, 0, time.UTC)

	tests := []struct {
		name               string
		event              AttentionEvent
		wantSignal         string
		wantReasonContains string
		wantMetadataKey    string
		wantActionability  Actionability
	}{
		{
			name: "session changed",
			event: mustLoggedAttentionEvent(t, ntmevents.Event{
				Timestamp: timeline,
				Type:      ntmevents.EventSessionCreate,
				Session:   "proj",
			}),
			wantSignal:         attentionSignalSessionChanged,
			wantReasonContains: "session lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name: "pane changed",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: timeline,
				Type:      tracker.ChangePaneCreated,
				Session:   "proj",
				Pane:      "4",
			}),
			wantSignal:         attentionSignalPaneChanged,
			wantReasonContains: "pane lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name:               "agent state changed",
			event:              NewAgentStateChangeEvent("proj", 2, "cc-2", "working", "idle", "activity_tracker"),
			wantSignal:         attentionSignalAgentStateChanged,
			wantReasonContains: "agent lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name:               "stalled",
			event:              mustBusAttentionEvent(t, ntmevents.NewAgentStallEvent("proj", "cod-1", 45, "waiting")),
			wantSignal:         attentionSignalStalled,
			wantReasonContains: "stall heuristic",
			wantMetadataKey:    "signal_threshold_seconds",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "context hot",
			event:              mustBusAttentionEvent(t, ntmevents.NewContextWarningEvent("proj", "cc-1", 92.5, 1200)),
			wantSignal:         attentionSignalContextHot,
			wantReasonContains: "90%",
			wantMetadataKey:    "signal_threshold_percent",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "rate limited",
			event:              mustBusAttentionEvent(t, ntmevents.NewWebhookEvent(ntmevents.WebhookAgentRateLimit, "proj", "2", "cc-1", "429 Too Many Requests", nil)),
			wantSignal:         attentionSignalRateLimited,
			wantReasonContains: "rate-limit",
			wantMetadataKey:    "signal_threshold_rationale",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "alert raised",
			event:              mustBusAttentionEvent(t, ntmevents.NewAlertEvent("proj", "alert-4", "health", "warning", "disk warm")),
			wantSignal:         attentionSignalAlertRaised,
			wantReasonContains: "alert emitted",
			wantActionability:  ActionabilityActionRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal, _ := tt.event.Details["signal"].(string)
			reason, _ := tt.event.Details["signal_reason"].(string)
			t.Logf("signal=%q reason=%q summary=%q actionability=%q details=%v", signal, reason, tt.event.Summary, tt.event.Actionability, tt.event.Details)

			if signal != tt.wantSignal {
				t.Fatalf("signal = %q, want %q", signal, tt.wantSignal)
			}
			if !strings.Contains(reason, tt.wantReasonContains) {
				t.Fatalf("signal_reason = %q, want substring %q", reason, tt.wantReasonContains)
			}
			if tt.wantMetadataKey != "" {
				if _, ok := tt.event.Details[tt.wantMetadataKey]; !ok {
					t.Fatalf("missing metadata key %q in details=%v", tt.wantMetadataKey, tt.event.Details)
				}
			}
			if tt.wantActionability != "" && tt.event.Actionability != tt.wantActionability {
				t.Fatalf("actionability = %q, want %q", tt.event.Actionability, tt.wantActionability)
			}
		})
	}
}

func TestAttentionSignal_NormalizesLifecycleActionabilityAndActions(t *testing.T) {
	tests := []struct {
		name       string
		event      AttentionEvent
		wantSignal string
	}{
		{
			name: "session created from tracker",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: time.Date(2026, 3, 21, 3, 23, 0, 0, time.UTC),
				Type:      tracker.ChangeSessionCreated,
				Session:   "proj",
			}),
			wantSignal: attentionSignalSessionChanged,
		},
		{
			name: "pane created from tracker",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: time.Date(2026, 3, 21, 3, 23, 30, 0, time.UTC),
				Type:      tracker.ChangePaneCreated,
				Session:   "proj",
				Pane:      "7",
			}),
			wantSignal: attentionSignalPaneChanged,
		},
		{
			name:       "agent state change from helper",
			event:      NewAgentStateChangeEvent("proj", 3, "cod-3", "idle", "working", "activity_tracker"),
			wantSignal: attentionSignalAgentStateChanged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Details["signal"]; got != tt.wantSignal {
				t.Fatalf("signal = %#v, want %q", got, tt.wantSignal)
			}
			if tt.event.Actionability != ActionabilityInteresting {
				t.Fatalf("actionability = %q, want %q", tt.event.Actionability, ActionabilityInteresting)
			}
			if tt.event.Severity != SeverityInfo {
				t.Fatalf("severity = %q, want %q", tt.event.Severity, SeverityInfo)
			}
			if len(tt.event.NextActions) != 1 {
				t.Fatalf("next_actions len = %d, want 1 (%v)", len(tt.event.NextActions), tt.event.NextActions)
			}
			if tt.event.NextActions[0].Action != "robot-status" {
				t.Fatalf("next action = %q, want robot-status", tt.event.NextActions[0].Action)
			}
		})
	}
}

func TestAttentionSignal_ContextHotThresholdBoundary(t *testing.T) {
	tests := []struct {
		name              string
		usagePercent      float64
		wantActionability Actionability
	}{
		{
			name:              "below threshold stays interesting",
			usagePercent:      89.9,
			wantActionability: ActionabilityInteresting,
		},
		{
			name:              "at threshold becomes action required",
			usagePercent:      90.0,
			wantActionability: ActionabilityActionRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := mustBusAttentionEvent(t, ntmevents.NewContextWarningEvent("proj", "cc-1", tt.usagePercent, 1200))
			t.Logf("usage=%.1f signal=%v reason=%v actionability=%q next_actions=%v", tt.usagePercent, event.Details["signal"], event.Details["signal_reason"], event.Actionability, event.NextActions)

			if event.Details["signal"] != attentionSignalContextHot {
				t.Fatalf("signal = %#v, want %q", event.Details["signal"], attentionSignalContextHot)
			}
			if event.Actionability != tt.wantActionability {
				t.Fatalf("actionability = %q, want %q", event.Actionability, tt.wantActionability)
			}
			if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-context" {
				t.Fatalf("next_actions = %v, want single robot-context action", event.NextActions)
			}
		})
	}
}

func TestAttentionSignal_DoesNotPromotePaneOutput(t *testing.T) {
	event := NewTrackerEvent(tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 21, 0, 0, time.UTC),
		Type:      tracker.ChangeAgentOutput,
		Session:   "proj",
		Pane:      "5",
		Details: map[string]interface{}{
			"line_count": 12,
		},
	})
	t.Logf("pane output summary=%q actionability=%q details=%v", event.Summary, event.Actionability, event.Details)

	if _, ok := event.Details["signal"]; ok {
		t.Fatalf("pane output should not derive a first-class signal: %v", event.Details)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Fatalf("pane output actionability = %q, want %q", event.Actionability, ActionabilityInteresting)
	}
}

func TestAttentionSignal_RateLimitedFromLoggedError(t *testing.T) {
	event := mustLoggedAttentionEvent(t, ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 22, 0, 0, time.UTC),
		Type:      ntmevents.EventError,
		Session:   "proj",
		Data: map[string]interface{}{
			"error_type": "rate_limit",
			"message":    "429 Too Many Requests",
		},
	})
	t.Logf("logged error signal=%v details=%v", event.Details["signal"], event.Details)

	if event.Details["signal"] != attentionSignalRateLimited {
		t.Fatalf("logged error signal = %#v, want %q", event.Details["signal"], attentionSignalRateLimited)
	}
	if event.Actionability != ActionabilityActionRequired {
		t.Fatalf("logged error actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
}

func TestAttentionSignal_AlertInfoPreservesSeverity(t *testing.T) {
	event := mustBusAttentionEvent(t, ntmevents.NewAlertEvent("proj", "alert-5", "health", "info", "background sync complete"))
	t.Logf("alert info signal=%v severity=%q actionability=%q next_actions=%v", event.Details["signal"], event.Severity, event.Actionability, event.NextActions)

	if event.Details["signal"] != attentionSignalAlertRaised {
		t.Fatalf("signal = %#v, want %q", event.Details["signal"], attentionSignalAlertRaised)
	}
	if event.Severity != SeverityInfo {
		t.Fatalf("severity = %q, want %q", event.Severity, SeverityInfo)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Fatalf("actionability = %q, want %q", event.Actionability, ActionabilityInteresting)
	}
	if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-status" {
		t.Fatalf("next_actions = %v, want single robot-status action", event.NextActions)
	}
}

func TestNewAgentStateChangeEvent(t *testing.T) {
	event := NewAgentStateChangeEvent("myproject", 2, "cc_1", "generating", "idle", "activity_tracker")

	if event.Session != "myproject" {
		t.Errorf("Session = %q, want %q", event.Session, "myproject")
	}
	if event.Pane != 2 {
		t.Errorf("Pane = %d, want 2", event.Pane)
	}
	if event.Category != EventCategoryAgent {
		t.Errorf("Category = %q, want %q", event.Category, EventCategoryAgent)
	}
	if event.Type != EventTypeAgentStateChange {
		t.Errorf("Type = %q, want %q", event.Type, EventTypeAgentStateChange)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Errorf("Actionability = %q, want %q (idle state)", event.Actionability, ActionabilityInteresting)
	}

	// Check details
	if event.Details["agent_id"] != "cc_1" {
		t.Errorf("details.agent_id = %v, want cc_1", event.Details["agent_id"])
	}
}

func TestNewFileConflictEvent(t *testing.T) {
	event := NewFileConflictEvent("myproject", "internal/robot/types.go", []string{"cc_1", "cc_2"})

	if event.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
	if event.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", event.Severity, SeverityError)
	}
	if len(event.NextActions) == 0 {
		t.Error("NextActions should not be empty for file conflicts")
	}
}

func TestBuildAttentionDigest_CoalescesPaneOutputBursts(t *testing.T) {
	events := []AttentionEvent{
		digestTestEvent(1, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 1"),
		digestTestEvent(2, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 2"),
		digestTestEvent(3, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 3"),
		digestTestEvent(4, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent became idle"),
	}

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest(events, 0, 4, opts)
	t.Logf("digest summary=%q suppressed=%+v trace=%+v", digest.Summary, digest.Suppressed, digest.Trace)

	if len(digest.Buckets.Interesting) != 2 {
		t.Fatalf("interesting bucket len = %d, want 2", len(digest.Buckets.Interesting))
	}

	var burst *AttentionDigestItem
	for i := range digest.Buckets.Interesting {
		if digest.Buckets.Interesting[i].Event.Type == EventTypePaneOutput {
			burst = &digest.Buckets.Interesting[i]
			break
		}
	}
	if burst == nil {
		t.Fatal("expected pane output burst item in interesting bucket")
	}
	if burst.SourceEventCount != 3 {
		t.Fatalf("burst.SourceEventCount = %d, want 3", burst.SourceEventCount)
	}
	if burst.SuppressedCount != 2 {
		t.Fatalf("burst.SuppressedCount = %d, want 2", burst.SuppressedCount)
	}
	if burst.SuppressionReason != attentionDigestSuppressionPaneOutputBurst {
		t.Fatalf("burst.SuppressionReason = %q, want %q", burst.SuppressionReason, attentionDigestSuppressionPaneOutputBurst)
	}
	if burst.Event.Summary != "3 output updates in proj pane 2" {
		t.Fatalf("burst summary = %q, want %q", burst.Event.Summary, "3 output updates in proj pane 2")
	}
	if digest.Suppressed.Total != 2 {
		t.Fatalf("suppressed total = %d, want 2", digest.Suppressed.Total)
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionPaneOutputBurst] != 2 {
		t.Fatalf("pane output suppression count = %d, want 2", digest.Suppressed.ByReason[attentionDigestSuppressionPaneOutputBurst])
	}

	hasCoalesced := false
	hasSuppressed := false
	for _, decision := range digest.Trace {
		if decision.Decision == "coalesced" {
			hasCoalesced = true
		}
		if decision.Decision == "suppressed" && decision.Reason == attentionDigestSuppressionPaneOutputBurst {
			hasSuppressed = true
		}
	}
	if !hasCoalesced || !hasSuppressed {
		t.Fatalf("trace missing coalesced/suppressed pane output decisions: %+v", digest.Trace)
	}
}

func TestBuildAttentionDigest_SuppressesLifecycleNoiseAndDuplicateAlerts(t *testing.T) {
	attached := digestTestEvent(1, EventCategorySession, EventTypeSessionAttached, ActionabilityInteresting, SeverityInfo, "session attached")
	attached.Pane = 0
	delete(attached.Details, "pane_ref")

	resized := digestTestEvent(2, EventCategoryPane, EventTypePaneResized, ActionabilityBackground, SeverityInfo, "pane resized")
	dupAlert1 := digestTestEvent(3, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 85%")
	dupAlert2 := digestTestEvent(4, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 85%")
	background := digestTestEvent(5, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, "checkpoint created")
	background.Pane = 0
	delete(background.Details, "pane_ref")

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest([]AttentionEvent{attached, resized, dupAlert1, dupAlert2, background}, 0, 5, opts)
	t.Logf("digest summary=%q suppressed=%+v trace=%+v", digest.Summary, digest.Suppressed, digest.Trace)

	if len(digest.Buckets.ActionRequired) != 1 {
		t.Fatalf("action_required bucket len = %d, want 1", len(digest.Buckets.ActionRequired))
	}
	alert := digest.Buckets.ActionRequired[0]
	if alert.SourceEventCount != 2 || alert.SuppressedCount != 1 {
		t.Fatalf("duplicate alert item = %+v, want source=2 suppressed=1", alert)
	}
	if alert.SuppressionReason != attentionDigestSuppressionDuplicateAlert {
		t.Fatalf("duplicate alert reason = %q, want %q", alert.SuppressionReason, attentionDigestSuppressionDuplicateAlert)
	}
	if len(digest.Buckets.Background) != 1 {
		t.Fatalf("background bucket len = %d, want 1", len(digest.Buckets.Background))
	}
	if digest.Suppressed.Total != 3 {
		t.Fatalf("suppressed total = %d, want 3", digest.Suppressed.Total)
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionLifecycleNoise] != 2 {
		t.Fatalf("lifecycle suppression count = %d, want 2", digest.Suppressed.ByReason[attentionDigestSuppressionLifecycleNoise])
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionDuplicateAlert] != 1 {
		t.Fatalf("duplicate alert suppression count = %d, want 1", digest.Suppressed.ByReason[attentionDigestSuppressionDuplicateAlert])
	}
}

func TestAttentionFeed_DigestPreservesCursorBoundariesAndImportantSignals(t *testing.T) {
	feed := newTestAttentionFeed(t)

	feed.Append(digestTestEvent(0, EventCategorySystem, EventType(DefaultTransportLiveness.HeartbeatType), ActionabilityBackground, SeverityDebug, "Heartbeat"))
	feed.Append(digestTestEvent(0, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 1"))
	feed.Append(digestTestEvent(0, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 2"))
	feed.Append(digestTestEvent(0, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%"))
	feed.Append(digestTestEvent(0, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent became idle"))

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.MinSeverity = SeverityDebug
	opts.ActionRequiredLimit = 1
	opts.InterestingLimit = 1
	opts.BackgroundLimit = 1

	digest, err := feed.Digest(0, opts)
	if err != nil {
		t.Fatalf("Digest error: %v", err)
	}
	t.Logf("digest summary=%q cursor_start=%d cursor_end=%d suppressed=%+v trace=%+v", digest.Summary, digest.CursorStart, digest.CursorEnd, digest.Suppressed, digest.Trace)

	if digest.CursorStart != 1 {
		t.Fatalf("CursorStart = %d, want 1", digest.CursorStart)
	}
	if digest.CursorEnd != 5 {
		t.Fatalf("CursorEnd = %d, want 5", digest.CursorEnd)
	}
	if len(digest.Buckets.ActionRequired) != 1 {
		t.Fatalf("action_required bucket len = %d, want 1", len(digest.Buckets.ActionRequired))
	}
	if digest.Buckets.ActionRequired[0].Event.Summary != "quota at 92%" {
		t.Fatalf("action_required summary = %q, want %q", digest.Buckets.ActionRequired[0].Event.Summary, "quota at 92%")
	}
	if !strings.HasPrefix(digest.Summary, "quota at 92%") {
		t.Fatalf("summary = %q, want prefix %q", digest.Summary, "quota at 92%")
	}
	if digest.Suppressed.Total < 2 {
		t.Fatalf("suppressed total = %d, want at least 2", digest.Suppressed.Total)
	}
}

func TestBuildAttentionDigest_SummaryStableAndBucketAssignment(t *testing.T) {
	heartbeat := digestTestEvent(1, EventCategorySystem, EventType(DefaultTransportLiveness.HeartbeatType), ActionabilityBackground, SeverityDebug, "Heartbeat")
	heartbeat.Pane = 0
	delete(heartbeat.Details, "pane_ref")

	alert := digestTestEvent(2, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%")
	bead := digestTestEvent(3, EventCategoryBead, EventTypeBeadUpdated, ActionabilityInteresting, SeverityInfo, "bead ready")
	output1 := digestTestEvent(4, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 1")
	output2 := digestTestEvent(5, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 2")
	background := digestTestEvent(6, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, "checkpoint created")
	background.Pane = 0
	delete(background.Details, "pane_ref")

	opts := DefaultAttentionDigestOptions()
	opts.MinSeverity = SeverityDebug
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest([]AttentionEvent{heartbeat, alert, bead, output1, output2, background}, 0, 6, opts)
	t.Logf("digest summary=%q buckets=%+v suppressed=%+v", digest.Summary, digest.Buckets, digest.Suppressed)

	if digest.CursorStart != 1 {
		t.Fatalf("CursorStart = %d, want 1", digest.CursorStart)
	}
	if digest.CursorEnd != 6 {
		t.Fatalf("CursorEnd = %d, want 6", digest.CursorEnd)
	}
	if digest.PeriodStart != heartbeat.Ts {
		t.Fatalf("PeriodStart = %q, want %q", digest.PeriodStart, heartbeat.Ts)
	}
	if digest.PeriodEnd != background.Ts {
		t.Fatalf("PeriodEnd = %q, want %q", digest.PeriodEnd, background.Ts)
	}
	if len(digest.Buckets.ActionRequired) != 1 || len(digest.Buckets.Interesting) != 2 || len(digest.Buckets.Background) != 1 {
		t.Fatalf("bucket sizes = action_required:%d interesting:%d background:%d, want 1/2/1",
			len(digest.Buckets.ActionRequired), len(digest.Buckets.Interesting), len(digest.Buckets.Background))
	}

	wantSummary := "quota at 92%; 1 action_required, 2 interesting, 1 background; 2 suppressed from 6 events"
	if digest.Summary != wantSummary {
		t.Fatalf("summary = %q, want %q", digest.Summary, wantSummary)
	}
}

// =============================================================================
// JSON Serialization Tests
// =============================================================================

func TestAttentionEvent_JSONSerialization(t *testing.T) {
	event := AttentionEvent{
		Cursor:        42,
		Ts:            "2026-03-20T10:00:00Z",
		Session:       "myproject",
		Pane:          2,
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "activity_tracker",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Test event",
		Details: map[string]any{
			"key": "value",
		},
		NextActions: []NextAction{
			{Action: "robot-tail", Args: "--session=myproject", Reason: "Check output"},
		},
	}

	// Serialize
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Deserialize
	var decoded AttentionEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	// Verify fields
	if decoded.Cursor != 42 {
		t.Errorf("Cursor = %d, want 42", decoded.Cursor)
	}
	if decoded.Session != "myproject" {
		t.Errorf("Session = %q, want %q", decoded.Session, "myproject")
	}
	if decoded.Category != EventCategoryAgent {
		t.Errorf("Category = %q, want %q", decoded.Category, EventCategoryAgent)
	}
}

func TestCursorExpiredDetails_JSONSerialization(t *testing.T) {
	details := CursorExpiredDetails{
		RequestedCursor: 42,
		EarliestCursor:  100,
		RetentionPeriod: "1h",
		ResyncCommand:   "ntm --robot-snapshot",
	}

	data, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded CursorExpiredDetails
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.RequestedCursor != 42 {
		t.Errorf("RequestedCursor = %d, want 42", decoded.RequestedCursor)
	}
	if decoded.ResyncCommand != "ntm --robot-snapshot" {
		t.Errorf("ResyncCommand = %q, want %q", decoded.ResyncCommand, "ntm --robot-snapshot")
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCursorAllocator_Next(b *testing.B) {
	alloc := NewCursorAllocator()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		alloc.Next()
	}
}

func BenchmarkCursorAllocator_NextParallel(b *testing.B) {
	alloc := NewCursorAllocator()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			alloc.Next()
		}
	})
}

func BenchmarkAttentionJournal_Append(b *testing.B) {
	journal := NewAttentionJournal(10000, time.Hour)
	event := AttentionEvent{Summary: "Benchmark event"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.Cursor = int64(i + 1)
		journal.Append(event)
	}
}

func BenchmarkAttentionJournal_Replay(b *testing.B) {
	journal := NewAttentionJournal(10000, time.Hour)
	// Pre-fill journal
	for i := int64(1); i <= 1000; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		journal.Replay(500, 100)
	}
}

func BenchmarkAttentionFeed_Append(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	event := AttentionEvent{
		Category: EventCategoryAgent,
		Summary:  "Benchmark event",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		feed.Append(event)
	}
}

// =============================================================================
// Conflict Event Tests (br-vdfjr)
// =============================================================================

func TestNewBusAttentionEvent_ReservationConflict(t *testing.T) {
	t.Parallel()

	event := ntmevents.NewReservationConflictEvent(
		"myproject", "internal/auth/handler.go", "BlueLake", "cc_1",
		[]string{"GreenCastle", "RedMountain"},
	)

	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("expected ReservationConflictEvent to normalize")
	}

	if att.Category != EventCategoryFile {
		t.Errorf("Category = %q, want %q", att.Category, EventCategoryFile)
	}
	if att.Type != EventTypeFileConflict {
		t.Errorf("Type = %q, want %q", att.Type, EventTypeFileConflict)
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", att.Actionability, ActionabilityActionRequired)
	}
	if att.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q", att.Severity, SeverityWarning)
	}
	if !strings.Contains(att.Summary, "reservation conflict") {
		t.Errorf("Summary should contain 'reservation conflict', got %q", att.Summary)
	}
	if !strings.Contains(att.Summary, "internal/auth/handler.go") {
		t.Errorf("Summary should contain file path, got %q", att.Summary)
	}
	if att.Details == nil {
		t.Fatal("Details must not be nil")
	}
	if att.Details["conflict_kind"] != "reservation" {
		t.Errorf("conflict_kind = %v, want 'reservation'", att.Details["conflict_kind"])
	}
	if att.Details["path"] != "internal/auth/handler.go" {
		t.Errorf("path = %v, want 'internal/auth/handler.go'", att.Details["path"])
	}
	if len(att.NextActions) == 0 {
		t.Error("NextActions must suggest follow-up commands")
	}
}

func TestNewBusAttentionEvent_FileConflict(t *testing.T) {
	t.Parallel()

	event := ntmevents.NewFileConflictEvent(
		"myproject", "cmd/main.go",
		[]string{"BlueLake", "GreenCastle"},
	)

	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("expected FileConflictEvent to normalize")
	}

	if att.Category != EventCategoryFile {
		t.Errorf("Category = %q, want %q", att.Category, EventCategoryFile)
	}
	if att.Type != EventTypeFileConflict {
		t.Errorf("Type = %q, want %q", att.Type, EventTypeFileConflict)
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", att.Actionability, ActionabilityActionRequired)
	}
	if !strings.Contains(att.Summary, "file conflict") {
		t.Errorf("Summary should contain 'file conflict', got %q", att.Summary)
	}
	if !strings.Contains(att.Summary, "cmd/main.go") {
		t.Errorf("Summary should contain file path, got %q", att.Summary)
	}
	if att.Details == nil {
		t.Fatal("Details must not be nil")
	}
	if att.Details["conflict_kind"] != "file" {
		t.Errorf("conflict_kind = %v, want 'file'", att.Details["conflict_kind"])
	}
	if len(att.NextActions) == 0 {
		t.Error("NextActions must suggest follow-up commands")
	}
}

func TestConflictEvents_FeedToJournal(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	// Publish a reservation conflict
	reservationEvt := ntmevents.NewReservationConflictEvent(
		"proj", "src/auth.go", "Agent1", "cc_1", []string{"Agent2"},
	)
	published, ok := feed.PublishBusEvent(reservationEvt)
	if !ok {
		t.Fatal("reservation conflict should be publishable")
	}
	if published.Cursor == 0 {
		t.Error("published event should have a cursor")
	}

	// Publish a file conflict
	fileEvt := ntmevents.NewFileConflictEvent("proj", "src/main.go", []string{"A", "B"})
	published2, ok := feed.PublishBusEvent(fileEvt)
	if !ok {
		t.Fatal("file conflict should be publishable")
	}
	if published2.Cursor <= published.Cursor {
		t.Errorf("cursor should be monotonically increasing: %d <= %d", published2.Cursor, published.Cursor)
	}

	// Verify replay returns both
	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events in journal, got %d", len(events))
	}
}

func TestConflictEvents_PartialObservability(t *testing.T) {
	t.Parallel()

	// Reservation conflict with empty holders — should still work
	event := ntmevents.NewReservationConflictEvent(
		"proj", "file.go", "Agent1", "cc_1", nil,
	)
	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("reservation conflict with nil holders should normalize")
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("even with no holders, actionability should be action_required")
	}

	// File conflict with single agent (edge case)
	event2 := ntmevents.NewFileConflictEvent("proj", "f.go", []string{"OnlyAgent"})
	att2, ok := NewBusAttentionEvent(event2)
	if !ok {
		t.Fatal("file conflict with one agent should normalize")
	}
	if att2.Details["conflict_kind"] != "file" {
		t.Errorf("conflict_kind should be 'file'")
	}
}

func BenchmarkBuildAttentionDigest_Burst(b *testing.B) {
	events := make([]AttentionEvent, 0, 4096)
	for i := 0; i < 4000; i++ {
		event := digestTestEvent(int64(i+1), EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update")
		events = append(events, event)
	}
	events = append(events, digestTestEvent(4001, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%"))

	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(events, 0, 4001, opts)
	}
}

// =============================================================================
// EventsOptions / filterEventsForRobot Tests (br-kpvhy)
// =============================================================================

func TestFilterEventsForRobot_NoFilters(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityInteresting, Severity: SeverityInfo},
		{Cursor: 2, Session: "other", Category: EventCategoryAlert, Actionability: ActionabilityActionRequired, Severity: SeverityWarning},
	}

	opts := EventsOptions{} // No filters
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events with no filters, got %d", len(result))
	}
}

func TestFilterEventsForRobot_SessionFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane},
		{Cursor: 2, Session: "other", Category: EventCategoryAlert},
		{Cursor: 3, Session: "proj", Category: EventCategorySystem},
	}

	opts := EventsOptions{Session: "proj"}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events for session 'proj', got %d", len(result))
	}
	for _, ev := range result {
		if ev.Session != "proj" {
			t.Errorf("expected session 'proj', got %q", ev.Session)
		}
	}
}

func TestFilterEventsForRobot_CategoryFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert},
		{Cursor: 3, Session: "proj", Category: EventCategorySystem},
		{Cursor: 4, Session: "proj", Category: EventCategoryPane},
	}

	opts := EventsOptions{CategoryFilter: []string{string(EventCategoryPane)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 pane events, got %d", len(result))
	}
	for _, ev := range result {
		if ev.Category != EventCategoryPane {
			t.Errorf("expected category 'pane', got %q", ev.Category)
		}
	}
}

func TestFilterEventsForRobot_ActionabilityFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Actionability: ActionabilityActionRequired},
		{Cursor: 2, Actionability: ActionabilityInteresting},
		{Cursor: 3, Actionability: ActionabilityBackground},
		{Cursor: 4, Actionability: ActionabilityActionRequired},
	}

	opts := EventsOptions{ActionabilityFilter: []string{string(ActionabilityActionRequired)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 action_required events, got %d", len(result))
	}
	for _, ev := range result {
		if ev.Actionability != ActionabilityActionRequired {
			t.Errorf("expected actionability 'action_required', got %q", ev.Actionability)
		}
	}
}

func TestFilterEventsForRobot_SeverityFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Severity: SeverityInfo},
		{Cursor: 2, Severity: SeverityWarning},
		{Cursor: 3, Severity: SeverityError},
		{Cursor: 4, Severity: SeverityWarning},
	}

	opts := EventsOptions{SeverityFilter: []string{string(SeverityWarning), string(SeverityError)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 3 {
		t.Errorf("expected 3 warning/error events, got %d", len(result))
	}
}

func TestFilterEventsForRobot_CombinedFilters(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityActionRequired},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert, Actionability: ActionabilityActionRequired},
		{Cursor: 3, Session: "other", Category: EventCategoryPane, Actionability: ActionabilityActionRequired},
		{Cursor: 4, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityInteresting},
	}

	opts := EventsOptions{
		Session:             "proj",
		CategoryFilter:      []string{string(EventCategoryPane)},
		ActionabilityFilter: []string{string(ActionabilityActionRequired)},
	}
	result := filterEventsForRobot(events, opts)

	if len(result) != 1 {
		t.Errorf("expected 1 event matching all filters, got %d", len(result))
	}
	if len(result) > 0 {
		if result[0].Cursor != 1 {
			t.Errorf("expected cursor 1, got %d", result[0].Cursor)
		}
	}
}

func TestFilterEventsForRobot_MultipleCategoryValues(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Category: EventCategoryPane},
		{Cursor: 2, Category: EventCategoryAlert},
		{Cursor: 3, Category: EventCategorySystem},
	}

	opts := EventsOptions{CategoryFilter: []string{string(EventCategoryPane), string(EventCategoryAlert)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events (pane or alert), got %d", len(result))
	}
}

func TestToStringSetForEvents_Empty(t *testing.T) {
	result := toStringSetForEvents(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}

	result = toStringSetForEvents([]string{})
	if result != nil {
		t.Errorf("expected nil for empty slice, got %v", result)
	}
}

func TestToStringSetForEvents_Values(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := toStringSetForEvents(input)

	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	for _, v := range input {
		if !result[v] {
			t.Errorf("expected %q in set", v)
		}
	}
}

func TestEventsOptions_Fields(t *testing.T) {
	// Verify the EventsOptions struct has expected fields
	opts := EventsOptions{
		SinceCursor:         100,
		Limit:               50,
		Session:             "test",
		CategoryFilter:      []string{"pane"},
		ActionabilityFilter: []string{"action_required"},
		SeverityFilter:      []string{"warning"},
	}

	if opts.SinceCursor != 100 {
		t.Errorf("expected SinceCursor 100, got %d", opts.SinceCursor)
	}
	if opts.Limit != 50 {
		t.Errorf("expected Limit 50, got %d", opts.Limit)
	}
	if opts.Session != "test" {
		t.Errorf("expected Session 'test', got %q", opts.Session)
	}
}

// =============================================================================
// DigestOptions / DigestResponse Tests (br-6tzh9)
// =============================================================================

func TestDigestOptions_Fields(t *testing.T) {
	t.Parallel()

	opts := DigestOptions{
		SinceCursor:         100,
		Session:             "test",
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
		IncludeTrace:        true,
	}

	if opts.SinceCursor != 100 {
		t.Errorf("expected SinceCursor 100, got %d", opts.SinceCursor)
	}
	if opts.Session != "test" {
		t.Errorf("expected Session 'test', got %q", opts.Session)
	}
	if opts.ActionRequiredLimit != 5 {
		t.Errorf("expected ActionRequiredLimit 5, got %d", opts.ActionRequiredLimit)
	}
	if opts.InterestingLimit != 4 {
		t.Errorf("expected InterestingLimit 4, got %d", opts.InterestingLimit)
	}
	if opts.BackgroundLimit != 3 {
		t.Errorf("expected BackgroundLimit 3, got %d", opts.BackgroundLimit)
	}
	if !opts.IncludeTrace {
		t.Error("expected IncludeTrace to be true")
	}
}

func TestDigestResponse_EmptyFeed(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	opts := DigestOptions{
		SinceCursor: 0,
	}

	// Build digest directly using feed.Digest
	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(opts.SinceCursor, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest.EventCount != 0 {
		t.Errorf("expected 0 events in empty feed, got %d", digest.EventCount)
	}
	if digest.CursorStart != 0 {
		t.Errorf("expected CursorStart 0, got %d", digest.CursorStart)
	}
	if digest.CursorEnd != 0 {
		t.Errorf("expected CursorEnd 0, got %d", digest.CursorEnd)
	}
	if len(digest.Buckets.ActionRequired) != 0 {
		t.Errorf("expected 0 action_required items, got %d", len(digest.Buckets.ActionRequired))
	}
	if len(digest.Buckets.Interesting) != 0 {
		t.Errorf("expected 0 interesting items, got %d", len(digest.Buckets.Interesting))
	}
}

func TestDigestResponse_WithEvents(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	// Add some events
	now := time.Now()
	events := []AttentionEvent{
		{
			Ts:            now.Add(-3 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Category:      EventCategoryPane,
			Type:          EventTypePaneOutput,
			Actionability: ActionabilityBackground,
			Severity:      SeverityInfo,
			Summary:       "Background output",
		},
		{
			Ts:            now.Add(-2 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Pane:          1,
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentError,
			Actionability: ActionabilityActionRequired,
			Severity:      SeverityError,
			Summary:       "Agent error detected",
		},
		{
			Ts:            now.Add(-1 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Pane:          2,
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityWarning,
			Summary:       "Agent waiting for prompt",
		},
	}
	for _, ev := range events {
		feed.Append(ev)
	}

	// Build digest
	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest.EventCount != 3 {
		t.Errorf("expected 3 events, got %d", digest.EventCount)
	}
	if digest.CursorStart < 1 {
		t.Errorf("expected CursorStart >= 1, got %d", digest.CursorStart)
	}
	if digest.CursorEnd < digest.CursorStart {
		t.Errorf("expected CursorEnd >= CursorStart, got start=%d end=%d",
			digest.CursorStart, digest.CursorEnd)
	}

	// Verify buckets contain expected items
	if len(digest.Buckets.ActionRequired) == 0 {
		t.Error("expected at least one action_required item")
	}
	if len(digest.Buckets.Interesting) == 0 {
		t.Error("expected at least one interesting item")
	}
}

func TestDigestResponse_CursorChaining(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	now := time.Now()

	// First batch
	feed.Append(AttentionEvent{
		Ts:            now.Add(-10 * time.Minute).Format(time.RFC3339),
		Session:       "proj",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "First batch event",
	})

	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}

	// First digest
	digest1, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("first digest error: %v", err)
	}
	if digest1.EventCount != 1 {
		t.Errorf("expected 1 event in first digest, got %d", digest1.EventCount)
	}

	// Add second batch after first digest
	feed.Append(AttentionEvent{
		Ts:            now.Add(-5 * time.Minute).Format(time.RFC3339),
		Session:       "proj",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Second batch event",
	})

	// Second digest from cursor_end of first
	digest2, err := feed.Digest(digest1.CursorEnd, digestOpts)
	if err != nil {
		t.Fatalf("second digest error: %v", err)
	}

	// Second digest should only have the new event
	if digest2.EventCount != 1 {
		t.Errorf("expected 1 event in second digest (cursor chaining), got %d", digest2.EventCount)
	}
	if digest2.CursorStart <= digest1.CursorEnd {
		t.Logf("cursor chaining: first.CursorEnd=%d second.CursorStart=%d",
			digest1.CursorEnd, digest2.CursorStart)
	}
}

func TestDigestResponse_SessionFilter(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	now := time.Now()

	// Add events from different sessions
	feed.Append(AttentionEvent{
		Ts:            now.Add(-2 * time.Minute).Format(time.RFC3339),
		Session:       "proj-a",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "Event from proj-a",
	})
	feed.Append(AttentionEvent{
		Ts:            now.Add(-1 * time.Minute).Format(time.RFC3339),
		Session:       "proj-b",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "Event from proj-b",
	})

	// Digest with session filter
	digestOpts := AttentionDigestOptions{
		Session:             "proj-a",
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only include events from proj-a
	if digest.EventCount != 1 {
		t.Errorf("expected 1 event for proj-a, got %d", digest.EventCount)
	}
}

// =============================================================================
// AttentionOptions / AttentionResponse Tests (br-t540i)
// =============================================================================

func TestAttentionOptions_Fields(t *testing.T) {
	// Verify AttentionOptions has all expected fields
	opts := AttentionOptions{
		SinceCursor:         100,
		Session:             "test-session",
		Timeout:             30 * time.Second,
		PollInterval:        500 * time.Millisecond,
		Condition:           "action_required",
		ActionRequiredLimit: 10,
		InterestingLimit:    5,
		BackgroundLimit:     2,
		IncludeTrace:        true,
	}

	if opts.SinceCursor != 100 {
		t.Errorf("SinceCursor: expected 100, got %d", opts.SinceCursor)
	}
	if opts.Session != "test-session" {
		t.Errorf("Session: expected test-session, got %s", opts.Session)
	}
	if opts.Timeout != 30*time.Second {
		t.Errorf("Timeout: expected 30s, got %v", opts.Timeout)
	}
	if opts.PollInterval != 500*time.Millisecond {
		t.Errorf("PollInterval: expected 500ms, got %v", opts.PollInterval)
	}
	if opts.Condition != "action_required" {
		t.Errorf("Condition: expected action_required, got %s", opts.Condition)
	}
}

func TestAttentionResponse_Fields(t *testing.T) {
	// Verify AttentionResponse has all expected fields
	resp := AttentionResponse{
		RobotResponse: RobotResponse{
			Success:      true,
			Timestamp:    "2026-03-21T00:00:00Z",
			Version:      "1.0.0",
			OutputFormat: "json",
		},
		WakeReason:       "attention",
		MatchedCondition: "action_required",
		TriggerEvent: &AttentionEvent{
			Cursor:        42,
			Type:          EventTypeMailReceived,
			Category:      EventCategoryMail,
			Actionability: ActionabilityActionRequired,
		},
		WaitedSeconds: 2.5,
		Digest: &AttentionDigest{
			CursorStart: 10,
			CursorEnd:   42,
			EventCount:  5,
		},
		CursorInfo: AttentionCursorInfo{
			StartCursor:  10,
			EndCursor:    42,
			OldestCursor: 1,
			NextCommand:  "ntm --robot-attention --since-cursor=42",
		},
	}

	if resp.WakeReason != "attention" {
		t.Errorf("WakeReason: expected attention, got %s", resp.WakeReason)
	}
	if resp.MatchedCondition != "action_required" {
		t.Errorf("MatchedCondition: expected action_required, got %s", resp.MatchedCondition)
	}
	if resp.WaitedSeconds != 2.5 {
		t.Errorf("WaitedSeconds: expected 2.5, got %f", resp.WaitedSeconds)
	}
	if resp.CursorInfo.EndCursor != 42 {
		t.Errorf("CursorInfo.EndCursor: expected 42, got %d", resp.CursorInfo.EndCursor)
	}
	if resp.Digest.EventCount != 5 {
		t.Errorf("Digest.EventCount: expected 5, got %d", resp.Digest.EventCount)
	}
}

func TestAttentionCursorInfo_NextCommand(t *testing.T) {
	// Verify NextCommand format is correct for cursor chaining
	info := AttentionCursorInfo{
		StartCursor:  10,
		EndCursor:    50,
		OldestCursor: 1,
		NextCommand:  "ntm --robot-attention --since-cursor=50 --session=proj",
	}

	if info.NextCommand == "" {
		t.Error("NextCommand should not be empty")
	}
	if info.EndCursor <= info.StartCursor {
		t.Error("EndCursor should be greater than StartCursor in normal flow")
	}
}

func TestAttentionResponse_TimeoutWake(t *testing.T) {
	// Verify timeout response has correct wake reason
	resp := AttentionResponse{
		RobotResponse: RobotResponse{Success: true},
		WakeReason:    "timeout",
		WaitedSeconds: 300.0,
		Digest:        &AttentionDigest{EventCount: 0},
		CursorInfo: AttentionCursorInfo{
			StartCursor: 50,
			EndCursor:   50,
		},
	}

	if resp.WakeReason != "timeout" {
		t.Errorf("WakeReason: expected timeout, got %s", resp.WakeReason)
	}
	if resp.MatchedCondition != "" {
		t.Errorf("MatchedCondition should be empty on timeout, got %s", resp.MatchedCondition)
	}
	if resp.TriggerEvent != nil {
		t.Error("TriggerEvent should be nil on timeout")
	}
}

func TestAttentionResponse_CursorExpired(t *testing.T) {
	// Verify cursor expired response has correct structure
	resp := AttentionResponse{
		RobotResponse: NewErrorResponse(
			&CursorExpiredError{RequestedCursor: 5, EarliestCursor: 100},
			ErrCodeCursorExpired,
			"ntm --robot-snapshot",
		),
		WakeReason: "cursor_expired",
		Digest:     &AttentionDigest{},
		CursorInfo: AttentionCursorInfo{
			StartCursor:  5,
			OldestCursor: 100,
			NextCommand:  "ntm --robot-snapshot",
		},
	}

	if resp.WakeReason != "cursor_expired" {
		t.Errorf("WakeReason: expected cursor_expired, got %s", resp.WakeReason)
	}
	if resp.RobotResponse.Success {
		t.Error("Success should be false on cursor expiry")
	}
	if resp.CursorInfo.OldestCursor != 100 {
		t.Errorf("OldestCursor should be 100, got %d", resp.CursorInfo.OldestCursor)
	}
}

func TestPrintDigest_ProfileFiltersBackgroundNoise(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySession,
		Type:          EventTypeSessionCreated,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "session created",
	})
	actionRequired := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "operator action required",
	})

	output, err := captureStdout(t, func() error {
		return PrintDigest(DigestOptions{
			SinceCursor:         0,
			Session:             "proj",
			Profile:             "operator",
			ActionRequiredLimit: 5,
			InterestingLimit:    4,
			BackgroundLimit:     3,
		})
	})
	if err != nil {
		t.Fatalf("PrintDigest returned error: %v", err)
	}

	var resp DigestResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("failed to decode digest response: %v\noutput=%s", err, output)
	}
	if !resp.Success {
		t.Fatalf("expected successful digest response, got %#v", resp)
	}
	if resp.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1 after operator profile filtering", resp.EventCount)
	}
	if len(resp.Buckets.ActionRequired) != 1 {
		t.Fatalf("ActionRequired bucket len = %d, want 1", len(resp.Buckets.ActionRequired))
	}
	if len(resp.Buckets.Background) != 0 {
		t.Fatalf("Background bucket len = %d, want 0 after filtering", len(resp.Buckets.Background))
	}
	if resp.Buckets.ActionRequired[0].Event.Cursor != actionRequired.Cursor {
		t.Fatalf("action bucket cursor = %d, want %d", resp.Buckets.ActionRequired[0].Event.Cursor, actionRequired.Cursor)
	}
}

func TestPrintAttention_ProfileShapesDigestAndNextCommand(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySession,
		Type:          EventTypeSessionCreated,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "session created",
	})
	actionRequired := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "operator action required",
	})

	output, err := captureStdout(t, func() error {
		if exitCode := PrintAttention(AttentionOptions{
			SinceCursor:         0,
			Session:             "proj",
			Timeout:             20 * time.Millisecond,
			PollInterval:        time.Millisecond,
			Condition:           WaitConditionActionRequired,
			Profile:             "operator",
			ActionRequiredLimit: 5,
			InterestingLimit:    4,
			BackgroundLimit:     3,
		}); exitCode != 0 {
			t.Fatalf("PrintAttention exit code = %d, want 0", exitCode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PrintAttention returned error: %v", err)
	}

	var resp AttentionResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("failed to decode attention response: %v\noutput=%s", err, output)
	}
	if !resp.Success {
		t.Fatalf("expected successful attention response, got %#v", resp)
	}
	if resp.WakeReason != "attention" {
		t.Fatalf("WakeReason = %q, want %q", resp.WakeReason, "attention")
	}
	if resp.MatchedCondition != WaitConditionActionRequired {
		t.Fatalf("MatchedCondition = %q, want %q", resp.MatchedCondition, WaitConditionActionRequired)
	}
	if resp.TriggerEvent == nil || resp.TriggerEvent.Cursor != actionRequired.Cursor {
		t.Fatalf("TriggerEvent = %#v, want cursor %d", resp.TriggerEvent, actionRequired.Cursor)
	}
	if resp.Digest == nil {
		t.Fatal("expected digest payload")
	}
	if resp.Digest.EventCount != 1 {
		t.Fatalf("Digest.EventCount = %d, want 1 after operator profile filtering", resp.Digest.EventCount)
	}
	if len(resp.Digest.Buckets.Background) != 0 {
		t.Fatalf("Digest background bucket len = %d, want 0", len(resp.Digest.Buckets.Background))
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--since-cursor=") {
		t.Fatalf("NextCommand = %q, want --since-cursor", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--session=proj") {
		t.Fatalf("NextCommand = %q, want session handoff", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--profile=operator") {
		t.Fatalf("NextCommand = %q, want profile handoff", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--attention-condition=action_required") {
		t.Fatalf("NextCommand = %q, want condition handoff", resp.CursorInfo.NextCommand)
	}
}

// =============================================================================
// Operator Loop Integration Tests (br-9bmtl)
// =============================================================================

func TestOperatorLoop_CursorChaining(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	// Step 1: Bootstrap — empty feed returns cursor 0
	events, latestCursor, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("initial replay error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events on empty feed, got %d", len(events))
	}
	if latestCursor != 0 {
		t.Errorf("expected cursor 0 on empty feed, got %d", latestCursor)
	}

	// Step 2: Add events and replay from cursor 0
	ev1 := feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStalled,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "agent stalled",
		Session:       "proj",
	})
	ev2 := feed.Append(AttentionEvent{
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "pane output",
		Session:       "proj",
	})

	events, latestCursor, err = feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if latestCursor != ev2.Cursor {
		t.Errorf("latest cursor = %d, want %d", latestCursor, ev2.Cursor)
	}

	// Step 3: Replay from ev1's cursor — should get only ev2
	events, latestCursor, err = feed.Replay(ev1.Cursor, 100)
	if err != nil {
		t.Fatalf("chained replay error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event from cursor %d, got %d", ev1.Cursor, len(events))
	}
	if len(events) > 0 && events[0].Cursor != ev2.Cursor {
		t.Errorf("expected event cursor %d, got %d", ev2.Cursor, events[0].Cursor)
	}

	// Step 4: Replay from latest — should get 0 events
	events, _, err = feed.Replay(latestCursor, 100)
	if err != nil {
		t.Fatalf("replay from latest error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from latest cursor, got %d", len(events))
	}
}

func TestOperatorLoop_ProfileFilteredEvents(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	// Add events at different actionability levels
	feed.Append(AttentionEvent{
		Category:      EventCategorySystem,
		Type:          EventTypeSystemHealthChange,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "system healthy",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent state changed",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "context hot",
	})

	events, _, _ := feed.Replay(0, 100)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Operator profile: min_actionability=interesting → should exclude background
	operatorProfile := GetProfile("operator")
	if operatorProfile == nil {
		t.Fatal("operator profile not found")
	}
	filters := ResolveEffectiveFilters("operator", ProfileFilters{})
	if filters.MinActionability != ActionabilityInteresting {
		t.Errorf("operator min_actionability = %q, want %q", filters.MinActionability, ActionabilityInteresting)
	}

	// Apply filters manually to verify
	var filtered []AttentionEvent
	for _, ev := range events {
		if filters.MatchesFilters(&ev) {
			filtered = append(filtered, ev)
		}
	}
	if len(filtered) != 2 {
		t.Errorf("operator profile should show 2 events (interesting+action_required), got %d", len(filtered))
	}

	// Debug profile: min_actionability=background → should include all
	debugFilters := ResolveEffectiveFilters("debug", ProfileFilters{})
	var debugFiltered []AttentionEvent
	for _, ev := range events {
		if debugFilters.MatchesFilters(&ev) {
			debugFiltered = append(debugFiltered, ev)
		}
	}
	if len(debugFiltered) != 3 {
		t.Errorf("debug profile should show all 3 events, got %d", len(debugFiltered))
	}

	// Minimal profile: min_severity=error AND min_actionability=action_required
	// The alert (severity=warning, actionability=action_required) does NOT meet min_severity=error.
	// So minimal profile should exclude all our test events (none have severity >= error).
	minFilters := ResolveEffectiveFilters("minimal", ProfileFilters{})
	var minFiltered []AttentionEvent
	for _, ev := range events {
		if minFilters.MatchesFilters(&ev) {
			minFiltered = append(minFiltered, ev)
		}
	}
	// None of our 3 events have severity >= error (warning < error), so 0 pass minimal.
	if len(minFiltered) != 0 {
		t.Errorf("minimal profile should show 0 events (none have severity>=error), got %d", len(minFiltered))
	}
}

func TestOperatorLoop_SnapshotSummaryConsistency(t *testing.T) {
	t.Parallel()

	feed := newTestAttentionFeed(t)

	// Add known events
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "critical alert",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent info",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "agent warning",
	})

	summary := buildSnapshotAttentionSummary(feed)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}

	// Verify counts match what we added
	if summary.TotalEvents != 3 {
		t.Errorf("TotalEvents = %d, want 3", summary.TotalEvents)
	}
	if summary.ActionRequiredCount != 2 {
		t.Errorf("ActionRequiredCount = %d, want 2", summary.ActionRequiredCount)
	}
	if summary.InterestingCount != 1 {
		t.Errorf("InterestingCount = %d, want 1", summary.InterestingCount)
	}

	// Category breakdown
	if summary.ByCategoryCount["alert"] != 1 {
		t.Errorf("ByCategoryCount[alert] = %d, want 1", summary.ByCategoryCount["alert"])
	}
	if summary.ByCategoryCount["agent"] != 2 {
		t.Errorf("ByCategoryCount[agent] = %d, want 2", summary.ByCategoryCount["agent"])
	}

	// TopItems should include both action_required events (capped at 3)
	if len(summary.TopItems) != 2 {
		t.Errorf("TopItems = %d, want 2", len(summary.TopItems))
	}

	// Unsupported signals should always be present
	if len(summary.UnsupportedSignals) == 0 {
		t.Error("expected unsupported signals for honest representation")
	}

	// Next steps should reference action_required events
	if len(summary.NextSteps) == 0 {
		t.Error("expected next-step hints")
	}
}
