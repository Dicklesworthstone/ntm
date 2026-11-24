package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestAttentionReplayOverflowDrainsPrefixBeforeReconnect(t *testing.T) {
	t.Parallel()
	events := []robot.AttentionEvent{
		{Cursor: 1, Type: "mail.pending"},
		{Cursor: 2, Type: "mail.pending"},
		{Cursor: 3, Type: "mail.pending"},
	}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		feed := &fakeAttentionStreamFeed{
			stats:        robot.JournalStats{Count: 3, OldestCursor: 1, NewestCursor: 3, RetentionPeriod: time.Hour},
			replayEvents: events, replayNewest: 3,
		}
		client := &WSClient{
			conn: conn, send: make(chan []byte, 2), topics: make(map[string]struct{}),
			attentionFeed: feed, attentionOverflow: make(chan struct{}, 1),
		}
		_, request, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			t.Errorf("read subscribe: %v", err)
			return
		}
		// Hold the writer until the subscribe finishes to saturate a known queue.
		client.handleMessage(request)
		if len(client.attentionOverflow) == 0 {
			close(client.send)
		}
		client.writePump()
	}))
	defer server.Close()

	read := func(since int, wantClose int) []map[string]any {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(WSMessage{Type: WSMsgSubscribe, RequestID: "resume",
			Data: map[string]any{"topics": []string{"attention"}, "since_cursor": since},
		}); err != nil {
			t.Fatal(err)
		}
		var frames []map[string]any
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, wantClose) {
					t.Fatalf("read since %d: %v", since, err)
				}
				return frames
			}
			for _, line := range strings.Split(string(data), "\n") {
				var frame map[string]any
				if err := json.Unmarshal([]byte(line), &frame); err != nil {
					t.Fatal(err)
				}
				frames = append(frames, frame)
			}
		}
	}

	first := read(0, websocket.CloseTryAgainLater)
	if len(first) != 2 || first[0]["seq"] != float64(1) || first[1]["seq"] != float64(2) {
		t.Fatalf("partial replay must deliver only its ordered prefix: %#v", first)
	}
	next := read(int(first[1]["seq"].(float64)), websocket.CloseNoStatusReceived)
	if len(next) != 2 || next[0]["seq"] != float64(3) || next[1]["type"] != "ack" {
		t.Fatalf("resumed replay must deliver the missing event and acknowledge: %#v", next)
	}
}

func TestAttentionLiveOverflowCannotAdvanceCursor(t *testing.T) {
	t.Parallel()
	sub := &WSAttentionSub{Active: true}
	client := &WSClient{attentionSub: sub, send: make(chan []byte, 1), attentionOverflow: make(chan struct{}, 1)}
	client.deliverAttentionEvent(robot.AttentionEvent{Cursor: 1, Type: "mail.pending"}, sub)
	client.deliverAttentionEvent(robot.AttentionEvent{Cursor: 2, Type: "mail.pending"}, sub)
	<-client.send
	client.deliverAttentionEvent(robot.AttentionEvent{Cursor: 3, Type: "mail.pending"}, sub)
	if sub.Active || sub.Cursor != 1 || sub.DroppedCount != 1 || len(client.attentionOverflow) != 1 || len(client.send) != 0 {
		t.Fatalf("live delivery crossed a missing cursor: %+v", sub)
	}
}

func TestAttentionBufferedLiveOverflowRejectsSubscribe(t *testing.T) {
	t.Parallel()
	feed := &fakeAttentionStreamFeed{
		stats: robot.JournalStats{Count: 1, OldestCursor: 1, NewestCursor: 1, RetentionPeriod: time.Hour},
		replayEvents: []robot.AttentionEvent{{Cursor: 1, Type: "mail.pending"}},
	}
	feed.onReplay = func() {
		feed.subscriber(robot.AttentionEvent{Cursor: 2, Type: "mail.pending"})
	}
	client := &WSClient{topics: make(map[string]struct{}), send: make(chan []byte, 1), attentionOverflow: make(chan struct{}, 1)}
	result := client.handleAttentionSubscribeWithFeed(feed, WSMessage{Data: map[string]any{"since_cursor": 0}}, []string{"attention"})
	if result["error_code"] != "ATTENTION_BACKPRESSURE" || result["subscribed"] != nil || feed.subscriber != nil || len(client.attentionOverflow) != 1 {
		t.Fatalf("overflow while draining buffered live frame: %v", result)
	}
}
