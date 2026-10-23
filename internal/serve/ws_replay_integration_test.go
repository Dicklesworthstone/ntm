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

func TestWSReplayIntegration_ReconnectReplaysSparseMultipleTopics(t *testing.T) {
	stateStore := newMigratedStateStore(t)
	srv := New(Config{StateStore: stateStore})
	defer srv.Stop()
	httpSrv := httptest.NewServer(srv.Router())
	defer httpSrv.Close()

	const sessionTopic = "sessions:sparse-replay"
	const paneTopic = "panes:sparse-replay:0"
	topics := []string{sessionTopic, "panes:sparse-replay:*", paneTopic}
	conn1 := dialWS(t, httpSrv)
	defer conn1.Close()
	reader1 := newWSFrameReader(t, conn1, "sparse-before-disconnect")
	subscribeWS(t, conn1, topics, -1)
	if frame := reader1.next(2 * time.Second); frame["type"] != "ack" {
		t.Fatalf("initial subscribe: %v", frame)
	}
	srv.wsHub.Publish(sessionTopic, "session.updated", "before disconnect")
	initial := reader1.next(2 * time.Second)
	if initial["type"] != "event" {
		t.Fatalf("initial live event: %v", initial)
	}
	cursor := frameSeq(t, initial)
	conn1.Close()

	// Add durable history while disconnected. Go through Store directly so
	// the publisher's bounded live queue cannot drop our unrelated fixtures.
	// The old multi-topic replay consumed its entire limit before reaching
	// any matching topic and falsely acknowledged an empty successful replay.
	store := srv.wsHub.eventStore()
	if store == nil {
		t.Fatal("server did not attach the durable event store")
	}
	for i := 0; i < wsReplayLimit+5; i++ {
		if _, err := store.Store("panes:unrelated:0", "pane.output", i); err != nil {
			t.Fatal(err)
		}
	}
	var expected []*WSStoredEvent
	for _, topic := range []string{sessionTopic, paneTopic, "panes:sparse-replay:1"} {
		event, err := store.Store(topic, "test.updated", map[string]interface{}{"topic": topic})
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, event)
	}

	conn2 := dialWS(t, httpSrv)
	defer conn2.Close()
	reader2 := newWSFrameReader(t, conn2, "sparse-reconnected")
	subscribeWS(t, conn2, topics, cursor)
	for _, want := range expected {
		frame := reader2.next(2 * time.Second)
		if frame["type"] != "event" || frame["replay"] != true || frame["topic"] != want.Topic {
			t.Fatalf("missing matching replay event cursor=%d topic=%s: %v", want.Seq, want.Topic, frame)
		}
		if got := frameSeq(t, frame); got != want.Seq {
			t.Fatalf("replay cursor=%d, want %d", got, want.Seq)
		}
	}
	// The exact pane topic overlaps the wildcard; it must appear only once.
	ack := reader2.next(2 * time.Second)
	if ack["type"] != "ack" {
		t.Fatalf("expected acknowledgement after the complete filtered replay, got %v", ack)
	}
	data, ok := ack["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("ack has no data: %v", ack)
	}
	replay, ok := data["replay"].(map[string]interface{})
	if !ok || replay["reset"] != false || replay["replayed"] != float64(len(expected)) {
		t.Fatalf("incorrect filtered replay acknowledgement: %v", ack)
	}
	lastSeq := expected[len(expected)-1].Seq
	if replay["cursor"] != float64(lastSeq) {
		t.Fatalf("replay acknowledgement cursor=%v, want %d", replay["cursor"], lastSeq)
	}

	srv.wsHub.Publish(paneTopic, "pane.output", "live after filtered replay")
	live := reader2.next(2 * time.Second)
	if live["type"] != "event" || live["replay"] != nil || frameSeq(t, live) != lastSeq+1 {
		t.Fatalf("live delivery did not continue after filtered replay: %v", live)
	}
}

func TestWSHub_ReplayIsCompleteOrReset(t *testing.T) {
	tests := []struct {
		name       string
		events     int
		capacity   int
		occupied   int
		wantReason string
	}{
		{name: "exact fit including ack", events: 3, capacity: 4},
		{name: "reserve ack slot", events: 3, capacity: 3, wantReason: "replay_overflow"},
		{name: "existing queued frames reduce space", events: 3, capacity: 5, occupied: 2, wantReason: "replay_overflow"},
		{name: "exact fit with queued frame", events: 3, capacity: 5, occupied: 1},
		{name: "at replay limit", events: wsReplayLimit, capacity: wsReplayLimit + 1},
		{name: "over replay limit despite sufficient queue", events: wsReplayLimit + 1, capacity: wsReplayLimit + 3, wantReason: "replay_limit_exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: wsReplayLimit + 2})
			defer store.Stop()
			const topic = "panes:bounded-replay:0"
			for i := 0; i < tt.events; i++ {
				if _, err := store.Store(topic, "pane.output", i); err != nil {
					t.Fatal(err)
				}
			}
			hub := NewWSHub()
			hub.SetEventStore(store)
			go hub.Run()
			defer hub.Stop()
			// No writePump drains this queue, making admission and the slot
			// reserved for the real subscribe acknowledgement deterministic.
			client := &WSClient{id: "bounded-replay", hub: hub, send: make(chan []byte, tt.capacity), topics: make(map[string]struct{})}
			for i := 0; i < tt.occupied; i++ {
				client.send <- []byte(`{"type":"pong"}`)
			}
			client.handleSubscribe(WSMessage{
				RequestID: "bounded-replay-request",
				Data: map[string]interface{}{
					"topics": []interface{}{topic},
					"since":  float64(0),
				},
			})

			wantReplayed := tt.events
			wantQueued := tt.occupied + tt.events + 1
			if tt.wantReason != "" {
				wantReplayed = 0
				wantQueued = tt.occupied + 2 // reset followed by the ack
			}
			if len(client.send) != wantQueued {
				t.Fatalf("queued %d frames, want %d (complete replay or explicit reset, then ack)", len(client.send), wantQueued)
			}
			for i := 0; i < tt.occupied; i++ {
				if frame := queuedWSReplayFrame(t, client); frame["type"] != "pong" {
					t.Fatalf("preexisting queued frame was changed: %v", frame)
				}
			}
			if tt.wantReason != "" {
				frame := queuedWSReplayFrame(t, client)
				if frame["type"] != "stream.reset" || frame["reason"] != tt.wantReason {
					t.Fatalf("partial history must reset before delivering any replay: %v", frame)
				}
				if frame["current_seq"] != float64(tt.events) {
					t.Fatalf("reset cursor=%v, want %d", frame["current_seq"], tt.events)
				}
			} else {
				for i := 0; i < tt.events; i++ {
					frame := queuedWSReplayFrame(t, client)
					if frame["type"] != "event" || frame["replay"] != true || frameSeq(t, frame) != int64(i+1) {
						t.Fatalf("replay %d missing, out of order, or unmarked: %v", i, frame)
					}
				}
			}
			ack := queuedWSReplayFrame(t, client)
			if ack["type"] != "ack" || ack["request_id"] != "bounded-replay-request" {
				t.Fatalf("replay did not reserve space for its acknowledgement: %v", ack)
			}
			data, ok := ack["data"].(map[string]interface{})
			if !ok {
				t.Fatalf("ack has no data: %v", ack)
			}
			replay, ok := data["replay"].(map[string]interface{})
			if !ok || replay["reset"] != (tt.wantReason != "") || replay["replayed"] != float64(wantReplayed) {
				t.Fatalf("partial replay must never be acknowledged as success: %v", ack)
			}
			if replay["cursor"] != float64(tt.events) {
				t.Fatalf("ack cursor=%v, want %d", replay["cursor"], tt.events)
			}
			if len(client.send) != 0 {
				t.Fatalf("unexpected %d frames after replay acknowledgement", len(client.send))
			}
		})
	}
}

func TestWSHub_ReplayCompletionPrecedesQueuedLiveEvent(t *testing.T) {
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 8})
	defer store.Stop()
	const topic = "panes:replay-ack:0"
	for i := 0; i < 2; i++ {
		if _, err := store.Store(topic, "pane.output", i); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewWSHub()
	hub.SetEventStore(store)
	go hub.Run()
	defer hub.Stop()
	client := &WSClient{id: "replay-ack", hub: hub, send: make(chan []byte, 4), topics: make(map[string]struct{})}
	if !hub.RegisterClient(client) {
		t.Fatal("register replay client")
	}
	entered := make(chan struct{})
	allowAck := make(chan struct{})
	finished := make(chan wsReplayResult, 1)
	go func() {
		finished <- hub.SubscribeWithReplay(client, []string{topic}, 0, func(result wsReplayResult) bool {
			close(entered)
			select {
			case <-allowAck:
			case <-hub.done:
				return false
			}
			return client.sendAck("serialized-replay", map[string]interface{}{
				"replay": map[string]interface{}{"replayed": result.replayed, "reset": result.reset, "cursor": result.cursor},
			})
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("replay did not invoke its completion callback")
	}
	// The next broadcast is ready while completion is deliberately blocked.
	// The hub must keep it behind the subscribe acknowledgement.
	hub.Publish(topic, "pane.output", "live queued before ack")
	if got := store.CurrentSeq(); got != 2 {
		t.Fatalf("hub processed a live event before replay completion: cursor=%d", got)
	}
	if len(client.send) != 2 {
		t.Fatalf("expected only the two replay frames before completion, got %d", len(client.send))
	}
	select {
	case result := <-finished:
		t.Fatalf("replay returned before its acknowledgement was queued: %+v", result)
	default:
	}
	close(allowAck)
	select {
	case result := <-finished:
		if result.reset || result.replayed != 2 || result.cursor != 2 {
			t.Fatalf("unexpected completed replay: %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replay did not return after its acknowledgement was queued")
	}
	for i := 0; i < 4; i++ {
		var raw []byte
		select {
		case raw = <-client.send:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for ordered frame %d", i)
		}
		var frame map[string]interface{}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		switch {
		case i < 2:
			if frame["type"] != "event" || frame["replay"] != true || frameSeq(t, frame) != int64(i+1) {
				t.Fatalf("replay frame %d: %v", i, frame)
			}
		case i == 2:
			if frame["type"] != "ack" || frame["request_id"] != "serialized-replay" {
				t.Fatalf("queued live broadcast passed the replay acknowledgement: %v", frame)
			}
		default:
			if frame["type"] != "event" || frame["replay"] != nil || frameSeq(t, frame) != 3 {
				t.Fatalf("live delivery did not follow the replay acknowledgement: %v", frame)
			}
		}
	}
}

func TestWSHub_ReplayFailedCompletionRetainsResetBarrier(t *testing.T) {
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 8})
	defer store.Stop()
	const topic = "panes:failed-replay-ack:0"
	for i := 0; i < 2; i++ {
		if _, err := store.Store(topic, "pane.output", i); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewWSHub()
	hub.SetEventStore(store)
	defer hub.Stop()
	client := &WSClient{id: "failed-replay-ack", hub: hub, send: make(chan []byte, 3), topics: make(map[string]struct{})}
	hub.clients[client] = struct{}{}
	completed := false
	req := &wsReplayRequest{
		client: client, topics: []string{topic}, since: 0, resp: make(chan wsReplayResult, 1),
		complete: func(result wsReplayResult) bool {
			completed = true
			if result.reset || result.replayed != 2 {
				t.Fatalf("replay failed before the completion callback: %+v", result)
			}
			// A concurrent control frame consumed the reserved ack slot.
			if !client.trySend([]byte(`{"type":"pong"}`), nil) {
				t.Fatal("replay did not reserve an acknowledgement slot")
			}
			return client.sendAck("failed-replay", map[string]interface{}{"replayed": result.replayed})
		},
	}
	hub.handleReplay(req)
	result := <-req.resp
	if !completed || !result.reset || result.replayed != 2 || result.cursor != 2 {
		t.Fatalf("failed acknowledgement was reported as successful completion: called=%v result=%+v", completed, result)
	}
	if _, pending := hub.pendingResets[client]; !pending {
		t.Fatal("failed completion lost the replay reset barrier")
	}
	for i := 0; i < 2; i++ {
		frame := queuedWSReplayFrame(t, client)
		if frame["type"] != "event" || frame["replay"] != true || frameSeq(t, frame) != int64(i+1) {
			t.Fatalf("replay prefix %d changed after ack failure: %v", i, frame)
		}
	}
	if frame := queuedWSReplayFrame(t, client); frame["type"] != "pong" {
		t.Fatalf("expected the control frame that filled the ack slot: %v", frame)
	}
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "pane.output", Data: "live after failed ack"})
	reset := queuedWSReplayFrame(t, client)
	if reset["type"] != "stream.reset" || reset["reason"] != "replay_overflow" {
		t.Fatalf("live delivery bypassed a failed replay acknowledgement: %v", reset)
	}
	live := queuedWSReplayFrame(t, client)
	if live["type"] != "event" || live["replay"] != nil || frameSeq(t, live) != 3 {
		t.Fatalf("live delivery did not follow the completion reset: %v", live)
	}
}

func TestWSHub_ReplayOverflowResetSurvivesBackpressure(t *testing.T) {
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 8})
	defer store.Stop()
	const topic = "panes:replay-reset:0"
	for i := 0; i < 3; i++ {
		if _, err := store.Store(topic, "pane.output", i); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewWSHub()
	hub.SetEventStore(store)
	defer hub.Stop()
	client := &WSClient{id: "full-replay", hub: hub, send: make(chan []byte, 2), topics: make(map[string]struct{})}
	hub.clients[client] = struct{}{}
	client.send <- []byte(`{"type":"pong"}`)
	client.send <- []byte(`{"type":"pong"}`)
	req := &wsReplayRequest{client: client, topics: []string{topic}, since: 0, resp: make(chan wsReplayResult, 1)}
	hub.handleReplay(req)
	result := <-req.resp
	if !result.reset || result.replayed != 0 || result.cursor != 3 {
		t.Fatalf("full queue falsely accepted a partial replay: %+v", result)
	}
	if _, pending := hub.pendingResets[client]; !pending {
		t.Fatal("full queue lost the required replay reset")
	}
	for i := 0; i < 2; i++ {
		if frame := queuedWSReplayFrame(t, client); frame["type"] != "pong" {
			t.Fatalf("overflow emitted a partial replay: %v", frame)
		}
	}
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "pane.output", Data: "live after overflow"})
	reset := queuedWSReplayFrame(t, client)
	if reset["type"] != "stream.reset" || reset["reason"] != "replay_overflow" {
		t.Fatalf("live delivery bypassed the replay reset barrier: %v", reset)
	}
	live := queuedWSReplayFrame(t, client)
	if live["type"] != "event" || live["replay"] != nil || frameSeq(t, live) != 4 {
		t.Fatalf("live delivery did not resume after the reset: %v", live)
	}
}

func TestWSHub_ReplayInvalidPayloadResetsBeforeDelivery(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewWSEventStore(db, WSEventStoreConfig{BufferSize: 8})
	defer store.Stop()
	const topic = "panes:invalid-replay:0"
	for i := 0; i < 3; i++ {
		if _, err := store.Store(topic, "pane.output", i); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt a later durable payload, so streaming frames while marshaling
	// would leak a valid prefix before discovering the replay cannot complete.
	if _, err := db.Exec("UPDATE ws_events SET data = ? WHERE seq = ?", "{", 2); err != nil {
		t.Fatal(err)
	}
	hub := NewWSHub()
	hub.SetEventStore(store)
	go hub.Run()
	defer hub.Stop()
	client := &WSClient{id: "invalid-replay", hub: hub, send: make(chan []byte, 4), topics: make(map[string]struct{})}
	client.handleSubscribe(WSMessage{
		RequestID: "invalid-replay-request",
		Data:      map[string]interface{}{"topics": []interface{}{topic}, "since": float64(0)},
	})
	if len(client.send) != 2 {
		t.Fatalf("invalid history queued %d frames, want only reset and ack", len(client.send))
	}
	reset := queuedWSReplayFrame(t, client)
	if reset["type"] != "stream.reset" || reset["reason"] != "replay_failed" {
		t.Fatalf("invalid history delivered a partial replay: %v", reset)
	}
	ack := queuedWSReplayFrame(t, client)
	if ack["type"] != "ack" {
		t.Fatalf("expected ack after replay failure, got %v", ack)
	}
	data, ok := ack["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("ack has no data: %v", ack)
	}
	replay, ok := data["replay"].(map[string]interface{})
	if !ok || replay["reset"] != true || replay["replayed"] != float64(0) {
		t.Fatalf("invalid history was acknowledged as successful replay: %v", ack)
	}
}

func queuedWSReplayFrame(t *testing.T, client *WSClient) map[string]interface{} {
	t.Helper()
	select {
	case raw := <-client.send:
		var frame map[string]interface{}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode queued frame %q: %v", raw, err)
		}
		return frame
	default:
		t.Fatal("expected a queued WebSocket frame")
		return nil
	}
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

func TestWSReplayIntegration_PersistenceFailureResetsInsteadOfRewinding(t *testing.T) {
	stateStore := newMigratedStateStore(t)
	srv := New(Config{StateStore: stateStore})
	defer srv.Stop()
	httpSrv := httptest.NewServer(srv.Router())
	defer httpSrv.Close()
	conn := dialWS(t, httpSrv)
	defer conn.Close()
	reader := newWSFrameReader(t, conn, "write-failure")
	const topic = "panes:durable:0"
	subscribeWS(t, conn, []string{topic}, -1)
	if frame := reader.next(2 * time.Second); frame["type"] != "ack" {
		t.Fatalf("subscribe: %v", frame)
	}
	var last int64
	for i := 0; i < 3; i++ {
		srv.wsHub.Publish(topic, "pane.output", i)
		last = frameSeq(t, reader.next(2*time.Second))
	}
	if _, err := stateStore.DB().Exec(`CREATE TRIGGER reject_ws_event BEFORE INSERT ON ws_events
		WHEN NEW.data = '"cannot persist"'
		BEGIN SELECT RAISE(FAIL, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	srv.wsHub.Publish(topic, "pane.output", "cannot persist")
	frame := reader.next(2 * time.Second)
	if frame["type"] != "stream.reset" || frame["reason"] != "persistence_failed" {
		t.Fatalf("failed durable write must explicitly reset the stream: %v", frame)
	}
	if frame["current_seq"] != float64(last) {
		t.Fatalf("reset changed durable cursor: %v, want %d", frame, last)
	}
	// A later successful event remains resumable in the original cursor domain.
	srv.wsHub.Publish(topic, "pane.output", "recovered")
	frame = reader.next(2 * time.Second)
	if frame["type"] != "event" || frameSeq(t, frame) <= last {
		t.Fatalf("cursor did not recover monotonically: %v", frame)
	}
}

func TestWSHub_PersistenceResetSurvivesBackpressure(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewWSEventStore(db, DefaultWSEventStoreConfig())
	defer store.Stop()
	hub := NewWSHub()
	hub.SetEventStore(store)
	defer hub.Stop()
	const topic = "panes:slow:0"
	client := &WSClient{id: "slow-reset", hub: hub, send: make(chan []byte, 2), topics: map[string]struct{}{topic: {}}}
	hub.clients[client] = struct{}{}
	// Drive the real hub publication path with a deterministically full queue.
	for i := 0; i < 2; i++ {
		hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "pane.output", Data: i})
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_ws_event BEFORE INSERT ON ws_events
		WHEN NEW.data = '"cannot persist"'
		BEGIN SELECT RAISE(FAIL, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "pane.output", Data: "cannot persist"})
	if len(hub.pendingResets) != 1 {
		t.Fatal("full queue lost the mandatory reset")
	}
	<-client.send
	<-client.send
	hub.broadcastEvent(&WSEvent{Type: WSMsgEvent, Topic: topic, EventType: "pane.output", Data: "recovered"})
	var reset, event map[string]interface{}
	if err := json.Unmarshal(<-client.send, &reset); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(<-client.send, &event); err != nil {
		t.Fatal(err)
	}
	if reset["type"] != "stream.reset" || reset["reason"] != "persistence_failed" {
		t.Fatalf("later event bypassed reset barrier: %v", reset)
	}
	if event["type"] != "event" || event["seq"] != float64(3) {
		t.Fatalf("live continuation after reset: %v", event)
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
