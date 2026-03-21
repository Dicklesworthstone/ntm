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
