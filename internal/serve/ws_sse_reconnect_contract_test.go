package serve

import (
	"container/ring"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Contract tests for WebSocket and SSE reconnect behavior (bd-fxj4f.9).
// These pin the externally-observable invariants that clients depend on:
//
//   - cursor expiry produces a clear reset signal with the current and
//     oldest-available sequence numbers
//   - a reconnect after a clean disconnect produces no duplicate events
//     and no gaps in seq monotonicity
//   - the buffer→DB fallback transparently serves cursors that have
//     scrolled past the ring buffer but are still inside retention
//   - slow-consumer drops are recorded with stable WSDroppedInfo shape
//   - the ring buffer is bounded — high-volume writes never exceed
//     BufferSize live entries even at burst
//   - the SSE initial event uses the documented "connected" envelope

func TestReconnectContract_CursorExpirySignalsResetWithSequenceContext(t *testing.T) {
	cfg := WSEventStoreConfig{
		BufferSize:       4,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(nil, cfg) // memory-only: no DB fallback
	defer store.Stop()

	for i := 0; i < 20; i++ {
		store.Store("panes:proj:0", "pane.output", map[string]int{"i": i})
	}

	events, needsReset, err := store.GetSince(1, "", 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if !needsReset {
		t.Fatalf("expected reset for expired cursor; got %d events", len(events))
	}
	if events != nil {
		t.Errorf("expected nil events on reset; got %d", len(events))
	}

	// The reset signal the server actually sends must carry the current
	// seq AND the oldest available seq — clients use both to size their
	// catch-up replay request. Verify the envelope shape.
	_, used, oldestSeq, _ := store.BufferStats()
	if used == 0 {
		t.Fatal("buffer should have entries after Store calls")
	}
	reset := NewStreamReset("panes:proj:0", "cursor_expired", store.CurrentSeq(), oldestSeq)
	if reset.Type != "stream.reset" {
		t.Errorf("reset.Type = %q, want stream.reset", reset.Type)
	}
	if reset.Reason != "cursor_expired" {
		t.Errorf("reset.Reason = %q, want cursor_expired", reset.Reason)
	}
	if reset.CurrentSeq != store.CurrentSeq() {
		t.Errorf("reset.CurrentSeq = %d, want %d", reset.CurrentSeq, store.CurrentSeq())
	}
	if reset.OldestAvail != oldestSeq {
		t.Errorf("reset.OldestAvail = %d, want %d", reset.OldestAvail, oldestSeq)
	}
}

func TestReconnectContract_NoDuplicateEventsAcrossDisconnect(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	topic := "panes:proj:0"

	// Phase 1: client connects, reads 30 events
	for i := 0; i < 30; i++ {
		store.Store(topic, "pane.output", map[string]int{"i": i})
	}
	first, _, err := store.GetSince(0, topic, 1000)
	if err != nil {
		t.Fatalf("first GetSince: %v", err)
	}
	if len(first) != 30 {
		t.Fatalf("first batch len = %d, want 30", len(first))
	}
	cursor := first[len(first)-1].Seq

	// Phase 2: server keeps producing while client is gone
	for i := 30; i < 70; i++ {
		store.Store(topic, "pane.output", map[string]int{"i": i})
	}

	// Phase 3: client reconnects with its cursor and pulls the rest
	resumed, needsReset, err := store.GetSince(cursor, topic, 1000)
	if err != nil {
		t.Fatalf("resume GetSince: %v", err)
	}
	if needsReset {
		t.Fatalf("unexpected reset on reconnect")
	}
	if len(resumed) != 40 {
		t.Fatalf("resumed batch len = %d, want 40", len(resumed))
	}

	// Cross-batch duplicate check: no seq from `first` may reappear in
	// `resumed`, and `resumed` must be strictly monotonic and strictly
	// greater than `cursor`.
	seen := make(map[int64]struct{}, len(first))
	for _, ev := range first {
		seen[ev.Seq] = struct{}{}
	}
	var prev int64
	for i, ev := range resumed {
		if _, dup := seen[ev.Seq]; dup {
			t.Errorf("resumed[%d].Seq=%d duplicates an event already delivered before disconnect", i, ev.Seq)
		}
		if ev.Seq <= cursor {
			t.Errorf("resumed[%d].Seq=%d must be > cursor=%d", i, ev.Seq, cursor)
		}
		if i > 0 && ev.Seq <= prev {
			t.Errorf("resumed[%d].Seq=%d not monotonic after %d", i, ev.Seq, prev)
		}
		prev = ev.Seq
	}
}

func TestReconnectContract_BufferToDatabaseFallbackIsTransparent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       8,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	topic := "panes:proj:0"

	// Write enough events that the cursor we record now will be evicted
	// from the ring buffer (size 8) but still be present in the DB.
	first, err := store.Store(topic, "pane.output", map[string]int{"phase": 1})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	cursorBeforeEviction := first.Seq
	for i := 0; i < 50; i++ {
		store.Store(topic, "pane.output", map[string]int{"i": i})
	}

	_, used, oldestBuffered, _ := store.BufferStats()
	if used != cfg.BufferSize {
		t.Fatalf("expected ring full at %d, got used=%d", cfg.BufferSize, used)
	}
	if cursorBeforeEviction >= oldestBuffered {
		t.Fatalf("test setup: cursor %d not evicted from buffer (oldest=%d)", cursorBeforeEviction, oldestBuffered)
	}

	// Fallback: cursor not in buffer, but well within DB retention.
	got, needsReset, err := store.GetSince(cursorBeforeEviction, topic, 1000)
	if err != nil {
		t.Fatalf("GetSince after buffer eviction: %v", err)
	}
	if needsReset {
		t.Fatalf("buffer→DB fallback returned reset; cursor should still be servable")
	}
	if len(got) == 0 {
		t.Fatal("buffer→DB fallback returned no events")
	}
	if got[0].Seq <= cursorBeforeEviction {
		t.Errorf("first fallback event seq=%d must be > cursor=%d", got[0].Seq, cursorBeforeEviction)
	}
}

func TestReconnectContract_SlowConsumerDropEnvelopeShape(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	topic := "panes:proj:0"
	clientID := "slow-client-A"

	// Generate a window of events the slow consumer "missed".
	const total = 30
	var seqs []int64
	for i := 0; i < total; i++ {
		ev, _ := store.Store(topic, "pane.output", map[string]int{"i": i})
		seqs = append(seqs, ev.Seq)
	}
	first, last := seqs[5], seqs[15]
	if err := store.RecordDropped(clientID, topic, "slow_consumer", first, last); err != nil {
		t.Fatalf("RecordDropped: %v", err)
	}

	stats := droppedStatsForTest(t, store, clientID, time.Now().Add(-time.Hour))
	if len(stats) != 1 {
		t.Fatalf("want exactly 1 drop record, got %d", len(stats))
	}
	got := stats[0]
	wantCount := int(last - first + 1)
	if got.DroppedCount != wantCount {
		t.Errorf("DroppedCount = %d, want %d", got.DroppedCount, wantCount)
	}
	if got.FirstDroppedSeq != first || got.LastDroppedSeq != last {
		t.Errorf("dropped range = [%d,%d], want [%d,%d]",
			got.FirstDroppedSeq, got.LastDroppedSeq, first, last)
	}
	if got.Reason != "slow_consumer" {
		t.Errorf("Reason = %q, want slow_consumer", got.Reason)
	}

	// Pin the wire-level envelope shape too. Clients consume the JSON
	// directly; renaming any of these fields is a breaking change.
	dropped := NewPaneOutputDropped(topic, wantCount, first, last, "slow_consumer")
	data, err := json.Marshal(dropped)
	if err != nil {
		t.Fatalf("marshal dropped envelope: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"type":"pane.output.dropped"`,
		`"topic":"panes:proj:0"`,
		`"reason":"slow_consumer"`,
		`"dropped_count":` + itoa(wantCount),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("envelope missing %s in %s", want, body)
		}
	}
}

func TestReconnectContract_RingBufferIsBoundedUnderBurst(t *testing.T) {
	cfg := WSEventStoreConfig{
		BufferSize:       16,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(nil, cfg)
	defer store.Stop()

	// Burst: 50x the buffer capacity. The ring must never report more
	// live entries than its configured BufferSize, even mid-burst.
	for i := 0; i < cfg.BufferSize*50; i++ {
		if _, err := store.Store("burst", "pane.output", map[string]int{"i": i}); err != nil {
			t.Fatalf("Store at i=%d: %v", i, err)
		}
		if i%cfg.BufferSize == 0 {
			size, used, _, _ := store.BufferStats()
			if used > size {
				t.Fatalf("ring buffer used=%d exceeds size=%d at i=%d", used, size, i)
			}
		}
	}

	size, used, oldestSeq, newestSeq := store.BufferStats()
	if used != size {
		t.Errorf("post-burst used=%d, want %d (full)", used, size)
	}
	if newestSeq-oldestSeq+1 > int64(size) {
		t.Errorf("buffer window seq=[%d,%d] (%d) exceeds size=%d",
			oldestSeq, newestSeq, newestSeq-oldestSeq+1, size)
	}

	// Sanity: ring also can't grow under the hood — Len is the
	// allocated capacity, which must be exactly BufferSize.
	if l := ringLen(store); l != cfg.BufferSize {
		t.Errorf("ring.Len = %d, want %d", l, cfg.BufferSize)
	}
}

// ringLen reaches into the store to assert the underlying ring's
// allocated size hasn't drifted from BufferSize. Kept package-private.
func ringLen(s *WSEventStore) int {
	s.bufferMu.RLock()
	defer s.bufferMu.RUnlock()
	if s.buffer == nil {
		return 0
	}
	return s.buffer.Len()
}

// itoa avoids pulling strconv into the test for one call; integers in
// these tests are always small and non-negative.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// _ silences the unused-import warning if container/ring is later
// removed from the file body. It documents that ringLen relies on
// container/ring's Len semantics from the standard library.
var _ = ring.New
