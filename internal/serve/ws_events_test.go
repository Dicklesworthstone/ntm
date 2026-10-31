package serve

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// setupTestDB opens a temp state store and applies the REAL managed
// migrations (the ws_events schema lands via 006_ws_events.sql), so these
// tests exercise the exact schema production runs against.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	cleanup := func() {
		if err := store.Close(); err != nil {
			t.Errorf("close state store: %v", err)
		}
	}

	return store.DB(), cleanup
}

func TestWSEventStore_StoreAndRetrieve(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       100,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour, // Long interval for tests
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store some events
	for i := 0; i < 10; i++ {
		data := map[string]interface{}{"index": i, "message": "test"}
		ev, err := store.Store("test.topic", "test.event", data)
		if err != nil {
			t.Fatalf("store event %d: %v", i, err)
		}
		t.Logf("WS_EVENTS_TEST: stored seq=%d topic=%s", ev.Seq, ev.Topic)
	}

	// Retrieve events from beginning
	events, needsReset, err := store.GetSince(0, "", 100)
	if err != nil {
		t.Fatalf("get since: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset signal")
	}
	if len(events) != 10 {
		t.Errorf("expected 10 events, got %d", len(events))
	}

	t.Logf("WS_EVENTS_TEST: retrieved %d events", len(events))

	// Retrieve events with cursor
	events, needsReset, err = store.GetSince(5, "", 100)
	if err != nil {
		t.Fatalf("get since 5: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset signal for cursor 5")
	}
	if len(events) != 5 {
		t.Errorf("expected 5 events after seq 5, got %d", len(events))
	}
}

func TestWSEventStore_RestartAfterEntireHistoryExpires(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	cfg := WSEventStoreConfig{BufferSize: 8, RetentionSeconds: 60, CleanupInterval: time.Hour}
	store := NewWSEventStore(db, cfg)
	for i := 0; i < 4; i++ {
		if _, err := store.Store("sessions:proj", "session.updated", i); err != nil {
			t.Fatal(err)
		}
	}
	last := store.CurrentSeq()
	if _, err := db.Exec("UPDATE ws_events SET created_at = ?", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.cleanup(); err != nil {
		t.Fatal(err)
	}
	// The in-memory ring must not resurrect expired history.
	if events, reset, err := store.GetSince(last-1, "", 10); err != nil || !reset || len(events) != 0 {
		t.Fatalf("expired history: events=%v reset=%v err=%v", events, reset, err)
	}
	store.Stop()

	restarted := NewWSEventStore(db, cfg)
	defer restarted.Stop()
	if got := restarted.CurrentSeq(); got != last {
		t.Fatalf("restart rewound high-water mark to %d, want %d", got, last)
	}
	if events, reset, err := restarted.GetSince(last, "", 10); err != nil || reset || len(events) != 0 {
		t.Fatalf("caught-up cursor on empty history: events=%v reset=%v err=%v", events, reset, err)
	}
	next, err := restarted.Store("sessions:proj", "session.updated", "after restart")
	if err != nil {
		t.Fatal(err)
	}
	if next.Seq <= last {
		t.Fatalf("reused cursor %d after %d", next.Seq, last)
	}
	events, reset, err := restarted.GetSince(last, "sessions:proj", 10)
	if err != nil || reset || len(events) != 1 || events[0].Seq != next.Seq {
		t.Fatalf("client cannot resume after idle restart: events=%v reset=%v err=%v", events, reset, err)
	}
}

func TestWSEventStore_SharedDatabaseAllocatesAndReplaysAllCursors(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	first := NewWSEventStore(db, DefaultWSEventStoreConfig())
	defer first.Stop()
	second := NewWSEventStore(db, DefaultWSEventStoreConfig())
	defer second.Stop()
	for i, store := range []*WSEventStore{first, second, first} {
		event, err := store.Store("sessions:proj", "session.updated", i)
		if err != nil || event.Seq != int64(i+1) {
			t.Fatalf("append %d: event=%+v err=%v", i, event, err)
		}
	}
	for _, store := range []*WSEventStore{first, second} {
		events, reset, err := store.GetSince(0, "", 10)
		if err != nil || reset || len(events) != 3 {
			t.Fatalf("shared replay: events=%v reset=%v err=%v", events, reset, err)
		}
		for i, event := range events {
			if event.Seq != int64(i+1) {
				t.Fatalf("event %d has cursor %d", i, event.Seq)
			}
		}
	}
}

func TestWSEventStore_FailedPersistenceDoesNotPublishCursor(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewWSEventStore(db, DefaultWSEventStoreConfig())
	defer store.Stop()
	first, err := store.Store("sessions:proj", "session.updated", "before failure")
	if err != nil {
		t.Fatal(err)
	}
	// An actual SQLite write failure, with reads still available.
	if _, err := db.Exec(`CREATE TRIGGER reject_ws_event BEFORE INSERT ON ws_events
		BEGIN SELECT RAISE(FAIL, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if event, err := store.Store("sessions:proj", "session.updated", "lost"); err == nil || event != nil {
		t.Fatalf("failed write reported success: event=%v err=%v", event, err)
	}
	if got := store.CurrentSeq(); got != first.Seq {
		t.Fatalf("failed write published cursor %d, want %d", got, first.Seq)
	}
	if _, used, _, _ := store.BufferStats(); used != 1 {
		t.Fatalf("failed write published an in-memory event: used=%d", used)
	}
	if events, reset, err := store.GetSince(first.Seq, "", 10); err != nil || reset || len(events) != 0 {
		t.Fatalf("failed event is replayable: events=%v reset=%v err=%v", events, reset, err)
	}
}

func TestWSEventStore_TopicFilter(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store events with different topics
	store.Store("sessions:proj1", "session.started", map[string]interface{}{})
	store.Store("sessions:proj2", "session.started", map[string]interface{}{})
	store.Store("panes:proj1:0", "pane.output", map[string]interface{}{})
	store.Store("panes:proj1:1", "pane.output", map[string]interface{}{})
	store.Store("global", "system.event", map[string]interface{}{})

	// Test exact topic match
	events, _, _ := store.GetSince(0, "global", 100)
	if len(events) != 1 {
		t.Errorf("expected 1 global event, got %d", len(events))
	}

	// Test wildcard topic match
	events, _, _ = store.GetSince(0, "sessions:*", 100)
	if len(events) != 2 {
		t.Errorf("expected 2 session events, got %d", len(events))
	}

	// Test pane wildcard
	events, _, _ = store.GetSince(0, "panes:*", 100)
	if len(events) != 2 {
		t.Errorf("expected 2 pane events, got %d", len(events))
	}

	t.Logf("WS_EVENTS_TEST: topic filtering passed")
}

func TestWSEventStore_FilteredReplayFindsSparseTopics(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var db *sql.DB
			if backend == "sqlite" {
				var cleanup func()
				db, cleanup = setupTestDB(t)
				defer cleanup()
			}
			store := NewWSEventStore(db, WSEventStoreConfig{BufferSize: 128, CleanupInterval: time.Hour})
			defer store.Stop()

			// Matching events start after more than limit*10 unrelated events.
			// Filtering an oversampled global prefix silently loses this history.
			for i := 0; i < 45; i++ {
				if _, err := store.Store("panes:unrelated:0", "pane.output", i); err != nil {
					t.Fatal(err)
				}
			}
			for _, topic := range []string{
				"sessions:target", // 46
				"panes:target:0",  // 47
				"sessions:other",  // 48
				"panes:target:1",  // 49
				"sessions:target", // 50
				"panes:other:0",   // 51
				"panes:target:2",  // 52
			} {
				if _, err := store.Store(topic, "test.event", nil); err != nil {
					t.Fatal(err)
				}
			}

			// Keep the public single-topic API covered as well as the hub's
			// multi-topic query, including overlapping patterns and pagination.
			tests := []struct {
				name   string
				topics []string
				since  int64
				limit  int
				want   []int64
			}{
				{name: "single exact", topics: []string{"sessions:target"}, limit: 2, want: []int64{46, 50}},
				{name: "single wildcard", topics: []string{"panes:target:*"}, limit: 2, want: []int64{47, 49}},
				{name: "multiple topics before limit", topics: []string{"sessions:target", "panes:target:*"}, limit: 3, want: []int64{46, 47, 49}},
				{name: "overlapping patterns once", topics: []string{"panes:target:0", "panes:target:*", "sessions:target"}, limit: 3, want: []int64{46, 47, 49}},
				{name: "resume filtered page", topics: []string{"sessions:target", "panes:target:*"}, since: 49, limit: 3, want: []int64{50, 52}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					var events []WSStoredEvent
					var reset bool
					var err error
					if len(tt.topics) == 1 {
						events, reset, err = store.GetSince(tt.since, tt.topics[0], tt.limit)
					} else {
						events, reset, err = store.getSinceTopics(tt.since, tt.topics, tt.limit)
					}
					if err != nil || reset {
						t.Fatalf("filtered replay: reset=%v err=%v", reset, err)
					}
					if len(events) != len(tt.want) {
						t.Fatalf("filtered replay returned %d events, want cursors %v: %+v", len(events), tt.want, events)
					}
					for i, want := range tt.want {
						if events[i].Seq != want {
							t.Errorf("event %d cursor=%d, want %d", i, events[i].Seq, want)
						}
					}
				})
			}
		})
	}
}

func TestWSEventStore_TopicMatchingPreservesLiteralCharacters(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var db *sql.DB
			if backend == "sqlite" {
				var cleanup func()
				db, cleanup = setupTestDB(t)
				defer cleanup()
			}
			store := NewWSEventStore(db, WSEventStoreConfig{BufferSize: 32, CleanupInterval: time.Hour})
			defer store.Stop()
			for _, topic := range []string{
				"panes:Build_%[x]?:0",        // 1
				"panes:build_%[x]?:0",        // 2: different case
				"panes:Build_Ax?:0",          // 3: SQL wildcard lookalike
				"panes:Build_%[x]?:1",        // 4
				"panes:Build_%[x]?:nested:0", // 5
				"panes:Build_%[x]?sibling:0", // 6: different prefix boundary
				"sessions:O'Reilly_%[team]",  // 7: literal SQL quote
				"panes:雪_%[x]?:0",            // 8: multibyte prefix
				"panes:雪_Ax:0",               // 9: SQL wildcard lookalike
				"panes:Build_*",              // 10: only ':*' is a prefix pattern
			} {
				if _, err := store.Store(topic, "test.event", nil); err != nil {
					t.Fatal(err)
				}
			}
			tests := []struct {
				name   string
				topics []string
				want   []int64
			}{
				{name: "exact case and punctuation", topics: []string{"panes:Build_%[x]?:0"}, want: []int64{1}},
				{name: "literal prefix wildcard", topics: []string{"panes:Build_%[x]?:*"}, want: []int64{1, 4, 5}},
				{name: "quoted exact topic", topics: []string{"sessions:O'Reilly_%[team]"}, want: []int64{7}},
				{name: "unicode prefix", topics: []string{"panes:雪_%[x]?:*"}, want: []int64{8}},
				{name: "literal embedded star", topics: []string{"panes:Build_*"}, want: []int64{10}},
				{name: "literal union", topics: []string{"panes:Build_%[x]?:*", "sessions:O'Reilly_%[team]"}, want: []int64{1, 4, 5, 7}},
				{name: "global wildcard with other filters", topics: []string{"absent", "*", "sessions:target"}, want: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
				{name: "unfiltered", want: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					events, reset, err := store.getSinceTopics(0, tt.topics, 100)
					if err != nil || reset {
						t.Fatalf("literal topic replay: reset=%v err=%v", reset, err)
					}
					if len(events) != len(tt.want) {
						t.Fatalf("literal matching returned %d events, want cursors %v: %+v", len(events), tt.want, events)
					}
					for i, want := range tt.want {
						if events[i].Seq != want {
							t.Errorf("event %d cursor=%d, want %d", i, events[i].Seq, want)
						}
					}
				})
			}
		})
	}
}

func TestWSEventStore_RingBufferOverflow(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       10, // Small buffer for testing
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store more events than buffer size
	for i := 0; i < 25; i++ {
		store.Store("test.topic", "test.event", map[string]interface{}{"index": i})
	}

	// Check buffer stats
	size, used, oldestSeq, newestSeq := store.BufferStats()
	t.Logf("WS_EVENTS_TEST: buffer stats size=%d used=%d oldest=%d newest=%d", size, used, oldestSeq, newestSeq)

	if size != 10 {
		t.Errorf("expected buffer size 10, got %d", size)
	}
	if used != 10 {
		t.Errorf("expected 10 used slots, got %d", used)
	}
	if newestSeq != 25 {
		t.Errorf("expected newest seq 25, got %d", newestSeq)
	}

	// Old cursor should still work via database
	events, needsReset, err := store.GetSince(0, "", 100)
	if err != nil {
		t.Fatalf("get since 0: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset - database should have all events")
	}
	if len(events) != 25 {
		t.Errorf("expected 25 events from db, got %d", len(events))
	}
}

func TestWSEventStore_DroppedEvents(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Record some dropped events
	err := store.RecordDropped("client-1", "panes:proj:0", "buffer_full", 10, 15)
	if err != nil {
		t.Fatalf("record dropped: %v", err)
	}

	err = store.RecordDropped("client-1", "panes:proj:0", "buffer_full", 20, 25)
	if err != nil {
		t.Fatalf("record dropped 2: %v", err)
	}

	// Get stats
	stats := droppedStatsForTest(t, store, "client-1", time.Now().Add(-time.Hour))

	if len(stats) != 1 { // Grouped by topic+reason
		t.Errorf("expected 1 grouped stat, got %d", len(stats))
	}

	if len(stats) > 0 {
		t.Logf("WS_EVENTS_TEST: dropped stats topic=%s count=%d", stats[0].Topic, stats[0].DroppedCount)
		if stats[0].DroppedCount != 12 { // 6 + 6
			t.Errorf("expected 12 dropped count, got %d", stats[0].DroppedCount)
		}
	}
}

func TestWSEventStore_MemoryOnly(t *testing.T) {
	// Test without database
	cfg := WSEventStoreConfig{
		BufferSize:       100,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(nil, cfg)
	defer store.Stop()

	// Store events
	for i := 0; i < 10; i++ {
		_, err := store.Store("test.topic", "test.event", map[string]interface{}{"i": i})
		if err != nil {
			t.Fatalf("store: %v", err)
		}
	}

	// Retrieve from buffer
	events, needsReset, err := store.GetSince(0, "", 100)
	if err != nil {
		t.Fatalf("get since: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset")
	}
	if len(events) != 10 {
		t.Errorf("expected 10 events, got %d", len(events))
	}

	t.Logf("WS_EVENTS_TEST: memory-only mode works, retrieved %d events", len(events))
}

func TestWSEventStore_CursorReset(t *testing.T) {
	cfg := WSEventStoreConfig{
		BufferSize:       10,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	// No database - memory only
	store := NewWSEventStore(nil, cfg)
	defer store.Stop()

	// Store events to fill buffer
	for i := 0; i < 15; i++ {
		store.Store("test.topic", "test.event", map[string]interface{}{"i": i})
	}

	// Very old cursor should trigger reset (no DB to fall back to)
	events, needsReset, err := store.GetSince(1, "", 100)
	if err != nil {
		t.Fatalf("get since: %v", err)
	}

	// Should get reset signal since cursor is too old for buffer and no DB
	if !needsReset {
		t.Errorf("expected reset signal for old cursor, got %d events instead", len(events))
	}

	t.Logf("WS_EVENTS_TEST: cursor reset detection works")
}

func TestStreamResetMessage(t *testing.T) {
	reset := NewStreamReset("sessions:proj1", "cursor_expired", 100, 50)

	switch reset.Type {
	case "stream.reset":
	default:
		t.Errorf("expected type stream.reset, got %s", reset.Type)
	}
	if reset.CurrentSeq != 100 {
		t.Errorf("expected current seq 100, got %d", reset.CurrentSeq)
	}
	if reset.OldestAvail != 50 {
		t.Errorf("expected oldest 50, got %d", reset.OldestAvail)
	}

	t.Logf("WS_EVENTS_TEST: stream.reset message type=%s reason=%s", reset.Type, reset.Reason)
}

func TestPaneOutputDroppedMessage(t *testing.T) {
	dropped := NewPaneOutputDropped("panes:proj:0", 5, 10, 14, "buffer_full")

	if dropped.Type != "pane.output.dropped" {
		t.Errorf("expected type pane.output.dropped, got %s", dropped.Type)
	}
	if dropped.DroppedCount != 5 {
		t.Errorf("expected dropped count 5, got %d", dropped.DroppedCount)
	}

	t.Logf("WS_EVENTS_TEST: pane.output.dropped topic=%s count=%d", dropped.Topic, dropped.DroppedCount)
}

// =============================================================================
// WebSocket Streaming Tests (bd-3ef1l)
// Tests for streaming, resume, and backpressure behaviors
// =============================================================================

func TestWSEventStore_SequenceContinuity(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store multiple pane output events
	topic := "panes:proj:0"
	for i := 0; i < 20; i++ {
		data := map[string]interface{}{
			"lines": []string{"line " + string(rune('A'+i))},
			"seq":   i,
		}
		_, err := store.Store(topic, "pane.output", data)
		if err != nil {
			t.Fatalf("store event %d: %v", i, err)
		}
	}

	// Retrieve all events for this topic
	events, needsReset, err := store.GetSince(0, topic, 100)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset signal")
	}

	// Verify sequence continuity - each event should have seq greater than previous
	if len(events) != 20 {
		t.Errorf("expected 20 events, got %d", len(events))
	}

	var lastSeq int64
	for i, ev := range events {
		if i > 0 && ev.Seq <= lastSeq {
			t.Errorf("sequence not monotonic: event %d has seq=%d, previous=%d", i, ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
	}

	t.Logf("WS_STREAMING_TEST: sequence continuity verified across %d events, range [%d, %d]",
		len(events), events[0].Seq, events[len(events)-1].Seq)
}

func TestWSEventStore_ReconnectWithCursor(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store initial batch of events
	topic := "panes:proj:0"
	for i := 0; i < 10; i++ {
		store.Store(topic, "pane.output", map[string]interface{}{"batch": 1, "idx": i})
	}

	// Simulate client reading events and tracking cursor
	events, _, _ := store.GetSince(0, topic, 100)
	if len(events) != 10 {
		t.Fatalf("expected 10 initial events, got %d", len(events))
	}
	clientCursor := events[len(events)-1].Seq
	t.Logf("WS_STREAMING_TEST: client read %d events, cursor=%d", len(events), clientCursor)

	// Simulate disconnect - store more events while "disconnected"
	for i := 0; i < 5; i++ {
		store.Store(topic, "pane.output", map[string]interface{}{"batch": 2, "idx": i})
	}

	// Simulate reconnect - retrieve events since cursor
	resumeEvents, needsReset, err := store.GetSince(clientCursor, topic, 100)
	if err != nil {
		t.Fatalf("get since cursor: %v", err)
	}
	if needsReset {
		t.Error("unexpected reset on reconnect")
	}

	// Should get exactly the 5 new events
	if len(resumeEvents) != 5 {
		t.Errorf("expected 5 events on reconnect, got %d", len(resumeEvents))
	}

	// Verify first resumed event is after our cursor
	if len(resumeEvents) > 0 && resumeEvents[0].Seq <= clientCursor {
		t.Errorf("first resumed event seq=%d should be > cursor=%d", resumeEvents[0].Seq, clientCursor)
	}

	t.Logf("WS_STREAMING_TEST: reconnect with cursor=%d retrieved %d events", clientCursor, len(resumeEvents))
}

func TestWSEventStore_BackpressureRecording(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	clientID := "slow-client-1"
	topic := "panes:proj:0"

	// Store some events
	var storedSeqs []int64
	for i := 0; i < 20; i++ {
		ev, _ := store.Store(topic, "pane.output", map[string]interface{}{"idx": i})
		storedSeqs = append(storedSeqs, ev.Seq)
	}

	// Simulate backpressure scenario: client is slow, events 5-15 are dropped
	firstDropped := storedSeqs[5]
	lastDropped := storedSeqs[15]

	err := store.RecordDropped(clientID, topic, "slow_consumer", firstDropped, lastDropped)
	if err != nil {
		t.Fatalf("record dropped: %v", err)
	}

	// Verify dropped event was recorded
	stats := droppedStatsForTest(t, store, clientID, time.Now().Add(-time.Hour))

	if len(stats) == 0 {
		t.Fatal("expected dropped stats to be recorded")
	}

	found := false
	for _, stat := range stats {
		if stat.Topic == topic && stat.Reason == "slow_consumer" {
			found = true
			expectedCount := int(lastDropped - firstDropped + 1)
			if stat.DroppedCount != expectedCount {
				t.Errorf("expected dropped count %d, got %d", expectedCount, stat.DroppedCount)
			}
			t.Logf("WS_STREAMING_TEST: backpressure recorded: topic=%s count=%d range=[%d,%d]",
				stat.Topic, stat.DroppedCount, stat.FirstDroppedSeq, stat.LastDroppedSeq)
		}
	}

	if !found {
		t.Error("expected to find dropped stats for slow_consumer")
	}
}

func TestWSEventStore_FanOutCorrectness(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Simulate multiple clients with different cursors
	type simClient struct {
		id     string
		cursor int64
	}
	clients := []simClient{
		{id: "client-1", cursor: 0},
		{id: "client-2", cursor: 0},
		{id: "client-3", cursor: 0},
	}

	// Store events
	topic := "panes:proj:0"
	for i := 0; i < 15; i++ {
		store.Store(topic, "pane.output", map[string]interface{}{"idx": i})
	}

	// Each client retrieves events and should get the same data
	var clientEventCounts []int
	for i := range clients {
		events, _, _ := store.GetSince(clients[i].cursor, topic, 100)
		clientEventCounts = append(clientEventCounts, len(events))

		// Update cursor
		if len(events) > 0 {
			clients[i].cursor = events[len(events)-1].Seq
		}
	}

	// All clients should receive the same number of events
	for i, count := range clientEventCounts {
		if count != 15 {
			t.Errorf("client %d expected 15 events, got %d", i, count)
		}
	}

	t.Logf("WS_STREAMING_TEST: fan-out verified, all %d clients received %d events each",
		len(clients), 15)

	// Store more events
	for i := 0; i < 5; i++ {
		store.Store(topic, "pane.output", map[string]interface{}{"idx": 15 + i})
	}

	// Clients with updated cursors should only get new events
	for i := range clients {
		events, _, _ := store.GetSince(clients[i].cursor, topic, 100)
		if len(events) != 5 {
			t.Errorf("client %d expected 5 new events, got %d", i, len(events))
		}
	}

	t.Logf("WS_STREAMING_TEST: fan-out with cursors verified, all clients received 5 new events")
}

func TestWSEventStore_ConcurrentPublish(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		// Force GetSince(0) to fall back to SQLite. If concurrent inserts
		// hit SQLITE_BUSY and are lost, the expected count below catches it.
		BufferSize:       10,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Concurrent publishers
	numPublishers := 5
	eventsPerPublisher := 20
	errs := make(chan error, numPublishers*eventsPerPublisher)
	var wg sync.WaitGroup

	for p := 0; p < numPublishers; p++ {
		wg.Add(1)
		go func(publisherID int) {
			defer wg.Done()
			topic := "panes:proj:" + string(rune('0'+publisherID))
			for i := 0; i < eventsPerPublisher; i++ {
				if _, err := store.Store(topic, "pane.output", map[string]interface{}{
					"publisher": publisherID,
					"idx":       i,
				}); err != nil {
					errs <- err
				}
			}
		}(p)
	}

	// Wait for all publishers
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("store event: %v", err)
		}
	}

	// Verify all events stored
	events, _, err := store.GetSince(0, "", 1000)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}

	expected := numPublishers * eventsPerPublisher
	if len(events) != expected {
		t.Errorf("expected %d events, got %d", expected, len(events))
	}

	// Verify sequence numbers are all unique and monotonic
	seenSeq := make(map[int64]bool)
	var maxSeq int64
	for _, ev := range events {
		if seenSeq[ev.Seq] {
			t.Errorf("duplicate seq number: %d", ev.Seq)
		}
		seenSeq[ev.Seq] = true
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}

	if maxSeq != int64(expected) {
		t.Errorf("expected max seq %d, got %d", expected, maxSeq)
	}

	t.Logf("WS_STREAMING_TEST: concurrent publish verified, %d events with unique seqs", len(events))
}

func TestWSEventStore_Stop_Idempotent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewWSEventStore(db, DefaultWSEventStoreConfig())

	store.Stop()
	store.Stop()
}

// droppedStatsForTest aggregates recorded drops directly from the database;
// the exported GetDroppedStats method was removed as dead code, and this
// keeps the live RecordDropped path verifiable.
func droppedStatsForTest(t *testing.T, s *WSEventStore, clientID string, since time.Time) []WSDroppedInfo {
	t.Helper()
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`
		SELECT topic, client_id, SUM(dropped_count), MIN(first_dropped_seq), MAX(last_dropped_seq), reason
		FROM ws_dropped_events
		WHERE client_id = ? AND created_at > ?
		GROUP BY topic, reason
		ORDER BY MAX(created_at) DESC`,
		clientID, since,
	)
	if err != nil {
		t.Fatalf("query dropped stats: %v", err)
	}
	defer rows.Close()

	var stats []WSDroppedInfo
	for rows.Next() {
		var info WSDroppedInfo
		var firstSeq, lastSeq sql.NullInt64
		if err := rows.Scan(&info.Topic, &info.ClientID, &info.DroppedCount, &firstSeq, &lastSeq, &info.Reason); err != nil {
			t.Fatalf("scan dropped info: %v", err)
		}
		if firstSeq.Valid {
			info.FirstDroppedSeq = firstSeq.Int64
		}
		if lastSeq.Valid {
			info.LastDroppedSeq = lastSeq.Int64
		}
		stats = append(stats, info)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	return stats
}

func TestWSRedactionProtectsLivePersistenceAndReplay(t *testing.T) {
	const credential = "0123456789abcdefghijklmnopqrstuv"
	const receipt = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	type record struct {
		Token      string `json:"api_token"`
		Diagnostic string `json:"diagnostic"`
		Count      uint64 `json:"count"`
	}
	typed := record{Token: credential, Diagnostic: "retain this diagnostic", Count: 9007199254740993}
	tests := []struct {
		name string
		data any
	}{
		{"generic map", map[string]any{"api_token": credential, "diagnostic": typed.Diagnostic, "count": typed.Count}},
		{"password marker stays stable", map[string]any{"password": credential, "diagnostic": typed.Diagnostic}},
		{"provider password marker stays stable", map[string]any{"password": wiringFakeSecret, "diagnostic": typed.Diagnostic}},
		{"existing marker and fresh secret", map[string]any{"password": "[REDACTED:OPENAI_KEY:0123abcd]", "api_token": credential, "diagnostic": typed.Diagnostic}},
		{"typed map", map[string]string{"api_token": credential, "diagnostic": typed.Diagnostic}},
		{"struct", typed},
		{"struct pointer", &typed},
		{"typed slice", []record{typed}},
		{"nested typed map", map[string]map[string]string{"config": {"api_token": credential, "diagnostic": typed.Diagnostic}}},
		{"raw JSON", json.RawMessage(`{"api_token":"` + credential + `","diagnostic":"retain this diagnostic","count":9007199254740993}`)},
		{"provider token", map[string]any{"message": wiringFakeSecret, "diagnostic": typed.Diagnostic}},
		{"secret key", map[string]any{wiringFakeSecret: "sensitive field name", "diagnostic": typed.Diagnostic}},
		{"receipt has no WS exception", map[string]any{"receipt_token": receipt, "diagnostic": typed.Diagnostic}},
	}

	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			db, cleanup := setupTestDB(t)
			defer cleanup()
			store := NewWSEventStore(db, DefaultWSEventStoreConfig())
			defer store.Stop()
			hub := NewWSHub()
			defer hub.Stop()
			hub.SetEventStore(store)
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
			const topic = "sessions:redaction"
			client := &WSClient{id: "redacted-live", hub: hub, send: make(chan []byte, 2), topics: map[string]struct{}{topic: {}}}
			hub.clients[client] = struct{}{}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					before, err := json.Marshal(tt.data)
					if err != nil {
						t.Fatal(err)
					}
					since := store.CurrentSeq()
					hub.Publish(topic, "diagnostic", tt.data)
					hub.broadcastEvent(<-hub.broadcast)
					var frame struct {
						Type WSMessageType   `json:"type"`
						Seq  int64           `json:"seq"`
						Data json.RawMessage `json:"data"`
					}
					select {
					case raw := <-client.send:
						if err := json.Unmarshal(raw, &frame); err != nil {
							t.Fatal(err)
						}
					default:
						t.Fatal("publication did not deliver a frame")
					}
					if frame.Type != WSMsgEvent || frame.Seq != since+1 {
						t.Fatalf("redaction lost event/cursor: %+v", frame)
					}
					for _, secret := range []string{credential, wiringFakeSecret, receipt} {
						if bytes.Contains(frame.Data, []byte(secret)) {
							t.Fatalf("credential escaped in %s mode via %s", mode, tt.name)
						}
					}
					if !bytes.Contains(frame.Data, []byte(typed.Diagnostic)) || !bytes.Contains(frame.Data, []byte("[REDACTED:")) {
						t.Fatalf("redaction lost useful diagnostic or marker: %s", frame.Data)
					}
					if bytes.Contains(before, []byte("9007199254740993")) && !bytes.Contains(frame.Data, []byte("9007199254740993")) {
						t.Fatalf("redaction rounded the exact numeric payload: %s", frame.Data)
					}
					var persisted string
					if err := db.QueryRow("SELECT data FROM ws_events WHERE seq = ?", frame.Seq).Scan(&persisted); err != nil {
						t.Fatal(err)
					}
					if persisted != string(frame.Data) {
						t.Fatalf("persisted and delivered forms differ: stored=%s wire=%s", persisted, frame.Data)
					}
					replayed := &WSClient{id: "redacted-replay", hub: hub, send: make(chan []byte, 2), topics: make(map[string]struct{})}
					req := &wsReplayRequest{client: replayed, topics: []string{topic}, since: since, resp: make(chan wsReplayResult, 1)}
					hub.handleReplay(req)
					result := <-req.resp
					if result.reset || result.replayed != 1 {
						t.Fatalf("redacted history was not replayable: %+v", result)
					}
					var replay WSEvent
					select {
					case raw := <-replayed.send:
						var data struct {
							Data json.RawMessage `json:"data"`
						}
						if err := json.Unmarshal(raw, &replay); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(raw, &data); err != nil || !bytes.Equal(data.Data, frame.Data) {
							t.Fatalf("replay changed sanitized data: %s, %v", raw, err)
						}
					default:
						t.Fatal("replay delivered no frame")
					}
					if !replay.Replay || replay.Seq != frame.Seq {
						t.Fatalf("replay lost its cursor/flag: %+v", replay)
					}
					after, err := json.Marshal(tt.data)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("redaction mutated the publisher's payload")
					}
				})
			}
		})
	}
}

func TestWSRedactionAppliesCurrentPolicyToHistoricalReplay(t *testing.T) {
	const credential = "0123456789abcdefghijklmnopqrstuv"
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			db, cleanup := setupTestDB(t)
			defer cleanup()
			store := NewWSEventStore(db, DefaultWSEventStoreConfig())
			defer store.Stop()
			const topic = "sessions:historical"
			original, err := store.Store(topic, "diagnostic", map[string]any{"api_token": credential, "diagnostic": "old useful detail"})
			if err != nil {
				t.Fatal(err)
			}
			hub := NewWSHub()
			defer hub.Stop()
			hub.SetEventStore(store)
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
			client := &WSClient{id: "current-policy", hub: hub, send: make(chan []byte, 2), topics: make(map[string]struct{})}
			req := &wsReplayRequest{client: client, topics: []string{topic}, since: 0, resp: make(chan wsReplayResult, 1)}
			hub.handleReplay(req)
			result := <-req.resp
			if result.reset || result.replayed != 1 || result.cursor != original.Seq {
				t.Fatalf("historical replay failed: %+v", result)
			}
			select {
			case raw := <-client.send:
				if !json.Valid(raw) || bytes.Contains(raw, []byte(credential)) || !bytes.Contains(raw, []byte("old useful detail")) || !bytes.Contains(raw, []byte("[REDACTED:")) {
					t.Fatalf("historical replay ignored active policy: %s", raw)
				}
			default:
				t.Fatal("historical replay delivered no frame")
			}
		})
	}
}

type changingWSPayload struct{ calls int }

func (p *changingWSPayload) MarshalJSON() ([]byte, error) {
	p.calls++
	if p.calls == 1 {
		return []byte(`{"message":"one immutable snapshot"}`), nil
	}
	return json.Marshal(map[string]string{"api_token": "0123456789abcdefghijklmnopqrstuv"})
}

type failingWSPayload struct{}

func (failingWSPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private diagnostic " + wiringFakeSecret)
}

func TestWSRedactionFreezesSerializedPayloadAndFailsClosed(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewWSEventStore(db, DefaultWSEventStoreConfig())
	defer store.Stop()
	hub := NewWSHub()
	defer hub.Stop()
	hub.SetEventStore(store)
	hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}})
	const topic = "sessions:serialization"
	client := &WSClient{id: "serialized-once", hub: hub, send: make(chan []byte, 2), topics: map[string]struct{}{topic: {}}}
	hub.clients[client] = struct{}{}
	payload := &changingWSPayload{}
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "diagnostic", Data: payload})
	frame := queuedWSReplayFrame(t, client)
	if payload.calls != 1 || frame["data"].(map[string]any)["message"] != "one immutable snapshot" {
		t.Fatalf("payload was encoded more than once: calls=%d frame=%v", payload.calls, frame)
	}
	var persisted string
	if err := db.QueryRow("SELECT data FROM ws_events WHERE seq = 1").Scan(&persisted); err != nil || persisted != `{"message":"one immutable snapshot"}` {
		t.Fatalf("frozen snapshot was not persisted: %q %v", persisted, err)
	}

	var logs lockedBuffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "diagnostic", Data: failingWSPayload{}})
	reset := queuedWSReplayFrame(t, client)
	if reset["type"] != "stream.reset" || reset["reason"] != "redaction_failed" || reset["current_seq"] != float64(1) {
		t.Fatalf("failed serialization was not an explicit stream reset: %v", reset)
	}
	if store.CurrentSeq() != 1 || strings.Contains(logs.String(), wiringFakeSecret) {
		t.Fatal("failed serialization persisted data, moved cursor, or exposed private diagnostics")
	}
}

func TestWSRedactionRejectsSensitiveRoutingMetadata(t *testing.T) {
	for _, field := range []string{"topic", "event_type"} {
		t.Run(field, func(t *testing.T) {
			store := NewWSEventStore(nil, DefaultWSEventStoreConfig())
			defer store.Stop()
			hub := NewWSHub()
			defer hub.Stop()
			hub.SetEventStore(store)
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}})
			event := &WSEvent{Type: WSMsgEvent, Topic: "sessions:metadata", EventType: "diagnostic", Data: "ordinary event data"}
			if field == "topic" {
				event.Topic += wiringFakeSecret
			} else {
				event.EventType += wiringFakeSecret
			}
			client := &WSClient{id: "metadata", hub: hub, send: make(chan []byte, 2), topics: map[string]struct{}{event.Topic: {}}}
			hub.clients[client] = struct{}{}
			var logs lockedBuffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })
			hub.broadcastEvent(event)
			reset := queuedWSReplayFrame(t, client)
			encoded, err := json.Marshal(reset)
			if err != nil {
				t.Fatal(err)
			}
			if reset["type"] != "stream.reset" || reset["reason"] != "redaction_failed" || bytes.Contains(encoded, []byte(wiringFakeSecret)) {
				t.Fatalf("sensitive routing identity was not safely refused: %s", encoded)
			}
			if store.CurrentSeq() != 0 || strings.Contains(logs.String(), wiringFakeSecret) {
				t.Fatal("sensitive routing metadata was persisted or logged")
			}
		})
	}
}

func TestWSRedactionAttentionLiveAndReplay(t *testing.T) {
	const credential = "0123456789abcdefghijklmnopqrstuv"
	event := robot.AttentionEvent{
		Cursor: 9007199254740993, Ts: time.Now().UTC().Format(time.RFC3339Nano),
		Session: "redaction", Category: "mail", Type: "mail.pending", Actionability: "action_required", Severity: "warning",
		Summary: "mail needs review: " + wiringFakeSecret,
		Details: map[string]any{"api_token": credential, "diagnostic": "keep this useful detail"},
	}
	before, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock, redaction.ModeWarn, redaction.ModeOff} {
		t.Run(string(mode), func(t *testing.T) {
			hub := NewWSHub()
			defer hub.Stop()
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}})
			for _, replay := range []bool{false, true} {
				sub := &WSAttentionSub{Active: true, Cursor: event.Cursor - 1, SinceCursor: event.Cursor - 1, topics: []string{"attention:redaction"}}
				client := &WSClient{id: "attention-redaction", hub: hub, send: make(chan []byte, 2), attentionSub: sub}
				if replay {
					if got, err := client.deliverAttentionReplay([]robot.AttentionEvent{event}, sub.topics, sub); got != 1 || err != nil {
						t.Fatalf("attention replay delivered %d events, want 1: %v", got, err)
					}
				} else {
					client.deliverAttentionEvent(event, sub)
					if sub.EventCount != 1 {
						t.Fatalf("attention live count = %d, want 1", sub.EventCount)
					}
				}
				if sub.Cursor != event.Cursor {
					t.Fatalf("attention cursor changed: %d", sub.Cursor)
				}
				select {
				case raw := <-client.send:
					if !json.Valid(raw) || !bytes.Contains(raw, []byte("9007199254740993")) || !bytes.Contains(raw, []byte("keep this useful detail")) || !bytes.Contains(raw, []byte("mail needs review: ")) {
						t.Fatalf("attention envelope lost useful data: %s", raw)
					}
					protect := mode == redaction.ModeRedact || mode == redaction.ModeBlock
					for _, secret := range []string{credential, wiringFakeSecret} {
						if bytes.Contains(raw, []byte(secret)) == protect {
							t.Fatalf("attention replay=%v did not honor %s mode", replay, mode)
						}
					}
				default:
					t.Fatal("attention delivered no frame")
				}
			}
		})
	}
	after, err := json.Marshal(event)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("attention redaction mutated the shared feed event")
	}
}

func TestWSRedactionAttentionFailureCannotAcknowledgeOrSkipHistory(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			hub := NewWSHub()
			defer hub.Stop()
			hub.setRedactionCfgForTest(&RedactionConfig{Enabled: true, Config: redaction.Config{
				Mode: mode, ExtraPatterns: map[redaction.Category][]string{"CUSTOM": {`"classified"\s*:\s*"[^"]*"`}},
			}})
			event := robot.AttentionEvent{Cursor: 1, Type: "mail.pending", Summary: "useful diagnostic", Details: map[string]any{"classified": "private material"}}
			sub := &WSAttentionSub{Active: true, topics: []string{"attention"}}
			client := &WSClient{id: "unsafe-attention", hub: hub, send: make(chan []byte, 2), attentionSub: sub}
			client.deliverAttentionEvent(event, sub)
			failure := queuedWSReplayFrame(t, client)
			if failure["type"] != "error" || failure["code"] != "REDACTION_FAILED" || sub.Active || sub.Cursor != 0 || sub.EventCount != 0 {
				t.Fatalf("unsafe attention event was not explicitly stopped: frame=%v sub=%+v", failure, sub)
			}
			client.deliverAttentionEvent(robot.AttentionEvent{Cursor: 2, Type: "mail.pending", Summary: "later ordinary event"}, sub)
			if len(client.send) != 0 || sub.Cursor != 0 {
				t.Fatal("later event silently skipped the unsafe attention cursor")
			}

			feed := &fakeAttentionStreamFeed{
				stats:        robot.JournalStats{Count: 1, OldestCursor: 1, NewestCursor: 1, RetentionPeriod: time.Hour},
				replayEvents: []robot.AttentionEvent{event}, replayNewest: 1,
			}
			replayClient := &WSClient{id: "unsafe-attention-replay", hub: hub, send: make(chan []byte, 2), topics: make(map[string]struct{})}
			result := replayClient.handleAttentionSubscribeWithFeed(feed, WSMessage{Data: map[string]any{"since_cursor": float64(0)}}, []string{"attention"})
			if result["error_code"] != "REDACTION_FAILED" || result["subscribed"] != nil || len(replayClient.send) != 0 {
				t.Fatalf("unsafe replay acknowledged success or delivered content: %v", result)
			}
			if feed.subscriber != nil || replayClient.attentionSub != nil || len(replayClient.Topics()) != 0 {
				t.Fatal("unsafe replay retained its live subscription")
			}
		})
	}
}
