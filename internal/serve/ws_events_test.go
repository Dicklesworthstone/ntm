package serve

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Create schema
	schema := `
		CREATE TABLE ws_events (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL,
			event_type TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_ws_events_seq ON ws_events(seq);
		CREATE INDEX idx_ws_events_topic_seq ON ws_events(topic, seq);
		CREATE INDEX idx_ws_events_created_at ON ws_events(created_at);

		CREATE TABLE ws_dropped_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL,
			client_id TEXT NOT NULL,
			dropped_count INTEGER NOT NULL DEFAULT 1,
			first_dropped_seq INTEGER,
			last_dropped_seq INTEGER,
			reason TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_ws_dropped_client ON ws_dropped_events(client_id, created_at);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(dir)
	}

	return db, cleanup
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
	stats, err := store.GetDroppedStats("client-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("get dropped stats: %v", err)
	}

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

	if reset.Type != "stream.reset" {
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
	stats, err := store.GetDroppedStats(clientID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("get dropped stats: %v", err)
	}

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
		BufferSize:       1000,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Concurrent publishers
	numPublishers := 5
	eventsPerPublisher := 20
	done := make(chan bool)

	for p := 0; p < numPublishers; p++ {
		go func(publisherID int) {
			topic := "panes:proj:" + string(rune('0'+publisherID))
			for i := 0; i < eventsPerPublisher; i++ {
				store.Store(topic, "pane.output", map[string]interface{}{
					"publisher": publisherID,
					"idx":       i,
				})
			}
			done <- true
		}(p)
	}

	// Wait for all publishers
	for p := 0; p < numPublishers; p++ {
		<-done
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
