package serve

// Integration tests for the live WebSocket persistence/replay wiring
// (bd-xaysv): Server publication → WSEventStore → cursor replay on
// reconnect, dropped-event recording/reporting, and retention pruning.
//
// All persistence in this file goes through the REAL managed schema:
// state.Open + Migrate on a temp path (ws_events tables land via
// migration 006_ws_events.sql). No manual CREATE TABLE anywhere.

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// newMigratedStateStore opens a temp state store with all managed migrations
// applied — the same path production takes in internal/cli/serve.go.
func newMigratedStateStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close state store: %v", err)
		}
	})
	return store
}

// wsFrameReader reads frames from a websocket connection, transparently
// splitting the newline-batched messages produced by writePump's drain
// optimization, and logs every frame it sees.
type wsFrameReader struct {
	t       *testing.T
	conn    *websocket.Conn
	label   string
	pending [][]byte
}

func newWSFrameReader(t *testing.T, conn *websocket.Conn, label string) *wsFrameReader {
	return &wsFrameReader{t: t, conn: conn, label: label}
}

// next returns the next decoded frame, or fails the test after timeout.
func (r *wsFrameReader) next(timeout time.Duration) map[string]interface{} {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for len(r.pending) == 0 {
		if err := r.conn.SetReadDeadline(deadline); err != nil {
			r.t.Fatalf("[%s] set read deadline: %v", r.label, err)
		}
		_, msg, err := r.conn.ReadMessage()
		if err != nil {
			r.t.Fatalf("[%s] read frame: %v", r.label, err)
		}
		for _, line := range strings.Split(string(msg), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			r.pending = append(r.pending, []byte(line))
		}
	}
	raw := r.pending[0]
	r.pending = r.pending[1:]

	var frame map[string]interface{}
	if err := json.Unmarshal(raw, &frame); err != nil {
		r.t.Fatalf("[%s] decode frame %q: %v", r.label, raw, err)
	}
	r.t.Logf("WS_FRAME conn=%s frame=%s", r.label, raw)
	return frame
}

// dialWS opens a websocket client against the server's router.
func dialWS(t *testing.T, httpSrv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

// subscribeWS sends a subscribe message; since < 0 omits the replay cursor.
func subscribeWS(t *testing.T, conn *websocket.Conn, topics []string, since int64) {
	t.Helper()
	data := map[string]interface{}{"topics": topics}
	if since >= 0 {
		data["since"] = since
	}
	msg := map[string]interface{}{"type": "subscribe", "request_id": "req-1", "data": data}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
}

func frameSeq(t *testing.T, frame map[string]interface{}) int64 {
	t.Helper()
	f, ok := frame["seq"].(float64)
	if !ok {
		t.Fatalf("frame has no numeric seq: %v", frame)
	}
	return int64(f)
}

// TestWSReplayIntegration_ReconnectWithCursor proves the full live loop
// through a real Server: connect, receive events, disconnect mid-stream,
// publish more while gone, reconnect with the cursor from the last frame, and
// receive the gap exactly once (in order, marked replay) before live frames.
func TestWSReplayIntegration_ReconnectWithCursor(t *testing.T) {
	stateStore := newMigratedStateStore(t)
	srv := New(Config{StateStore: stateStore})
	defer srv.Stop()

	httpSrv := httptest.NewServer(srv.Router())
	defer httpSrv.Close()

	const topic = "panes:it:0"

	// --- Phase 1: connect and stream 5 live events -------------------------
	conn1 := dialWS(t, httpSrv)
	defer conn1.Close()
	r1 := newWSFrameReader(t, conn1, "conn1")

	subscribeWS(t, conn1, []string{topic}, -1)
	ack1 := r1.next(2 * time.Second)
	if ack1["type"] != "ack" {
		t.Fatalf("expected subscribe ack, got %v", ack1)
	}

	for i := 0; i < 5; i++ {
		srv.wsHub.Publish(topic, "pane.output", map[string]interface{}{"phase": 1, "i": i})
	}

	seen := make(map[int64]int) // seq -> delivery count across the whole test
	var lastSeq int64
	for i := 0; i < 5; i++ {
		frame := r1.next(2 * time.Second)
		if frame["type"] != "event" {
			t.Fatalf("expected event frame, got %v", frame)
		}
		seq := frameSeq(t, frame)
		if seq <= lastSeq {
			t.Fatalf("live seq not monotonic: %d after %d", seq, lastSeq)
		}
		if frame["replay"] != nil {
			t.Fatalf("live frame must not be marked replay: %v", frame)
		}
		seen[seq]++
		lastSeq = seq
	}
	t.Logf("WS_PHASE1 received=5 cursor=%d", lastSeq)

	// --- Phase 2: disconnect mid-stream, publish the gap -------------------
	conn1.Close()
	for i := 0; i < 4; i++ {
		srv.wsHub.Publish(topic, "pane.output", map[string]interface{}{"phase": 2, "i": i})
	}
	// Wait until the gap is persisted (the hub loop Store()s each event).
	wantSeq := lastSeq + 4
	waitDeadline := time.Now().Add(2 * time.Second)
	for {
		st := srv.wsHub.eventStore()
		if st != nil && st.CurrentSeq() >= wantSeq {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("gap events not persisted: store seq did not reach %d", wantSeq)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("WS_PHASE2 published gap seqs (%d,%d]", lastSeq, wantSeq)

	// --- Phase 3: reconnect with cursor, receive the gap exactly once ------
	conn2 := dialWS(t, httpSrv)
	defer conn2.Close()
	r2 := newWSFrameReader(t, conn2, "conn2")

	subscribeWS(t, conn2, []string{topic}, lastSeq)

	expect := lastSeq + 1
	for expect <= wantSeq {
		frame := r2.next(2 * time.Second)
		if frame["type"] != "event" {
			t.Fatalf("expected replay event frame, got %v", frame)
		}
		seq := frameSeq(t, frame)
		if seq != expect {
			t.Fatalf("replay out of order or gapped: got seq=%d want %d", seq, expect)
		}
		if frame["replay"] != true {
			t.Fatalf("replayed frame missing replay=true: %v", frame)
		}
		if seen[seq] != 0 {
			t.Fatalf("seq %d delivered more than once", seq)
		}
		seen[seq]++
		expect++
	}

	// The ack is queued after all replay frames: its arrival marks replay end.
	ack2 := r2.next(2 * time.Second)
	if ack2["type"] != "ack" {
		t.Fatalf("expected post-replay ack, got %v", ack2)
	}
	replayInfo, ok := ack2["data"].(map[string]interface{})["replay"].(map[string]interface{})
	if !ok {
		t.Fatalf("ack missing replay info: %v", ack2)
	}
	if got := replayInfo["replayed"].(float64); int(got) != 4 {
		t.Errorf("ack replay.replayed = %v, want 4", got)
	}
	if replayInfo["reset"] != false {
		t.Errorf("ack replay.reset = %v, want false", replayInfo["reset"])
	}
	if got := replayInfo["cursor"].(float64); int64(got) < wantSeq {
		t.Errorf("ack replay.cursor = %v, want >= %d", got, wantSeq)
	}

	// --- Phase 4: live streaming resumes seamlessly after replay -----------
	srv.wsHub.Publish(topic, "pane.output", map[string]interface{}{"phase": 4})
	frame := r2.next(2 * time.Second)
	if frame["type"] != "event" {
		t.Fatalf("expected live event after replay, got %v", frame)
	}
	seq := frameSeq(t, frame)
	if seq != wantSeq+1 {
		t.Fatalf("post-replay live seq = %d, want %d (gap-free continuation)", seq, wantSeq+1)
	}
	if frame["replay"] != nil {
		t.Fatalf("live frame after replay must not be marked replay: %v", frame)
	}
	if seen[seq] != 0 {
		t.Fatalf("seq %d delivered more than once", seq)
	}
	t.Logf("WS_PHASE4 gap replayed exactly once; live resumed at seq=%d", seq)
}

// TestWSReplayIntegration_NoStateStore_StreamReset proves graceful
// degradation: without a state store the socket still works, and a cursor
// resume request yields a stream.reset (replay unavailable) plus an ack
// flagged reset — never a hang or an error close.
func TestWSReplayIntegration_NoStateStore_StreamReset(t *testing.T) {
	srv := New(Config{}) // no StateStore: persistence disabled, logged once
	defer srv.Stop()

	httpSrv := httptest.NewServer(srv.Router())
	defer httpSrv.Close()

	conn := dialWS(t, httpSrv)
	defer conn.Close()
	r := newWSFrameReader(t, conn, "degraded")

	subscribeWS(t, conn, []string{"panes:it:0"}, 0)

	reset := r.next(2 * time.Second)
	if reset["type"] != "stream.reset" {
		t.Fatalf("expected stream.reset frame, got %v", reset)
	}
	if reset["reason"] != "replay_unavailable" {
		t.Errorf("reset reason = %v, want replay_unavailable", reset["reason"])
	}

	ack := r.next(2 * time.Second)
	if ack["type"] != "ack" {
		t.Fatalf("expected ack after reset, got %v", ack)
	}
	replayInfo, ok := ack["data"].(map[string]interface{})["replay"].(map[string]interface{})
	if !ok {
		t.Fatalf("ack missing replay info: %v", ack)
	}
	if replayInfo["reset"] != true {
		t.Errorf("ack replay.reset = %v, want true", replayInfo["reset"])
	}

	// Live streaming still works in degraded mode.
	srv.wsHub.Publish("panes:it:0", "pane.output", map[string]interface{}{"degraded": true})
	frame := r.next(2 * time.Second)
	if frame["type"] != "event" {
		t.Fatalf("expected live event in degraded mode, got %v", frame)
	}
}

// TestWSHub_SlowClientDropsRecordedAndReported proves that events dropped to
// a slow client are coalesced into ws_dropped_events (reason slow_client) and
// that the client receives a pane.output.dropped frame describing the gap
// once delivery succeeds again.
func TestWSHub_SlowClientDropsRecordedAndReported(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := DefaultWSEventStoreConfig()
	cfg.CleanupInterval = time.Hour
	evStore := NewWSEventStore(db, cfg)
	defer evStore.Stop()

	hub := NewWSHub()
	hub.SetEventStore(evStore)
	go hub.Run()
	defer hub.Stop()

	const topic = "panes:drop:0"
	client := &WSClient{
		id:     "slow-client",
		hub:    hub,
		send:   make(chan []byte, 2), // tiny buffer to force drops
		topics: map[string]struct{}{topic: {}},
	}
	if !hub.RegisterClient(client) {
		t.Fatal("register client")
	}

	// Fill the client's buffer (2), then overflow it (2 dropped).
	for i := 0; i < 4; i++ {
		hub.Publish(topic, "pane.output", map[string]interface{}{"i": i})
	}
	deadline := time.Now().Add(2 * time.Second)
	for hub.dropped.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 drops, got %d", hub.dropped.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if evStore.CurrentSeq() != 4 {
		t.Fatalf("store seq = %d, want 4", evStore.CurrentSeq())
	}

	// Drain the two delivered frames — the client catches up.
	for i := 0; i < 2; i++ {
		select {
		case msg := <-client.send:
			t.Logf("WS_FRAME conn=slow-client frame=%s", msg)
		case <-time.After(time.Second):
			t.Fatal("timed out draining delivered frames")
		}
	}

	// Next successful delivery flushes the drop range: DB record + report frame.
	hub.Publish(topic, "pane.output", map[string]interface{}{"i": 4})

	var frames []map[string]interface{}
	for len(frames) < 2 {
		select {
		case msg := <-client.send:
			t.Logf("WS_FRAME conn=slow-client frame=%s", msg)
			var frame map[string]interface{}
			if err := json.Unmarshal(msg, &frame); err != nil {
				t.Fatalf("decode frame %q: %v", msg, err)
			}
			frames = append(frames, frame)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event + drop report, got %d frames", len(frames))
		}
	}
	if frames[0]["type"] != "event" || int64(frames[0]["seq"].(float64)) != 5 {
		t.Fatalf("first frame after recovery should be the live event seq=5, got %v", frames[0])
	}
	report := frames[1]
	if report["type"] != "pane.output.dropped" {
		t.Fatalf("expected pane.output.dropped report, got %v", report)
	}
	if report["reason"] != "slow_client" {
		t.Errorf("report reason = %v, want slow_client", report["reason"])
	}
	if first, last := int64(report["first_seq"].(float64)), int64(report["last_seq"].(float64)); first != 3 || last != 4 {
		t.Errorf("report range = [%d,%d], want [3,4]", first, last)
	}
	if count := int(report["dropped_count"].(float64)); count != 2 {
		t.Errorf("report dropped_count = %d, want 2", count)
	}

	// The persisted record matches.
	stats := droppedStatsForTest(t, evStore, client.id, time.Now().Add(-time.Hour))
	if len(stats) != 1 {
		t.Fatalf("want exactly 1 dropped record, got %d", len(stats))
	}
	if stats[0].Topic != topic || stats[0].Reason != "slow_client" {
		t.Errorf("dropped record = %+v, want topic=%s reason=slow_client", stats[0], topic)
	}
	if stats[0].FirstDroppedSeq != 3 || stats[0].LastDroppedSeq != 4 || stats[0].DroppedCount != 2 {
		t.Errorf("dropped record range/count = [%d,%d]/%d, want [3,4]/2",
			stats[0].FirstDroppedSeq, stats[0].LastDroppedSeq, stats[0].DroppedCount)
	}
	t.Logf("WS_DROPPED persisted=%+v", stats[0])

	hub.UnregisterClient(client)
}

// TestWSEventStore_RetentionPruning proves the retention pass deletes events
// older than RetentionSeconds (and dropped-event records older than 24h) from
// the real migrated schema while keeping fresh rows replayable.
func TestWSEventStore_RetentionPruning(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       8,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour, // manual cleanup below; no ticker races
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	const topic = "panes:gc:0"
	for i := 0; i < 10; i++ {
		if _, err := store.Store(topic, "pane.output", map[string]interface{}{"i": i}); err != nil {
			t.Fatalf("store event %d: %v", i, err)
		}
	}
	if err := store.RecordDropped("gc-client", topic, "slow_client", 1, 2); err != nil {
		t.Fatalf("record dropped: %v", err)
	}

	// Backdate the first 5 events beyond retention, and the dropped record
	// beyond its fixed 24h window.
	if _, err := db.Exec("UPDATE ws_events SET created_at = ? WHERE seq <= 5", time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("backdate events: %v", err)
	}
	if _, err := db.Exec("UPDATE ws_dropped_events SET created_at = ?", time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("backdate dropped record: %v", err)
	}

	if err := store.cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var eventCount, droppedCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM ws_events").Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM ws_dropped_events").Scan(&droppedCount); err != nil {
		t.Fatalf("count dropped: %v", err)
	}
	if eventCount != 5 {
		t.Errorf("events after pruning = %d, want 5", eventCount)
	}
	if droppedCount != 0 {
		t.Errorf("dropped records after pruning = %d, want 0", droppedCount)
	}

	// Fresh rows are still replayable through the DB path.
	events, needsReset, err := store.GetSince(5, topic, 100)
	if err != nil {
		t.Fatalf("GetSince after pruning: %v", err)
	}
	if needsReset {
		t.Fatal("unexpected reset for cursor inside retained window")
	}
	if len(events) != 5 {
		t.Errorf("replayable events after pruning = %d, want 5", len(events))
	}
	for _, ev := range events {
		t.Logf("WS_RETAINED seq=%d topic=%s created_at=%s", ev.Seq, ev.Topic, ev.CreatedAt.Format(time.RFC3339))
	}
}
