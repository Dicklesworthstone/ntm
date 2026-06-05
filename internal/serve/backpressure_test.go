package serve

import (
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestWSHubBackpressureInputUsesQueueAndDropCounters(t *testing.T) {
	hub := NewWSHub()
	for i := 0; i < cap(hub.broadcast); i++ {
		hub.broadcast <- &WSEvent{Topic: "attention", EventType: "test"}
	}

	hub.Publish("attention", "test", map[string]any{"ok": true})
	input := hub.BackpressureInput()

	requireEqual(t, input.Surface, backpressure.SurfaceWebSocket)
	requireEqual(t, input.QueueDepth, cap(hub.broadcast))
	requireEqual(t, input.QueueCapacity, cap(hub.broadcast))
	requireEqual(t, input.DroppedCount, int64(1))
	if !input.SourceLoaded {
		t.Fatal("source should be loaded for live hub")
	}
}

func TestPreparedAttentionStreamBackpressureInput(t *testing.T) {
	stream := &preparedAttentionStream{
		eventCh: make(chan robot.AttentionEvent, 2),
	}
	stream.eventCh <- robot.AttentionEvent{}
	stream.droppedCount.Add(4)

	input := stream.BackpressureInput("proj")

	requireEqual(t, input.Surface, backpressure.SurfaceSSE)
	requireEqual(t, input.Session, "proj")
	requireEqual(t, input.QueueDepth, 1)
	requireEqual(t, input.QueueCapacity, 2)
	requireEqual(t, input.DroppedCount, int64(4))
}

func TestServerBackpressureInputsIncludesMissingRESTCounters(t *testing.T) {
	server := &Server{wsHub: NewWSHub()}
	inputs := server.BackpressureInputs()

	requireEqual(t, len(inputs), 2)
	requireEqual(t, inputs[0].Surface, backpressure.SurfaceWebSocket)
	if !inputs[0].SourceLoaded {
		t.Fatalf("first input = %#v, want loaded websocket", inputs[0])
	}
	requireEqual(t, inputs[1].Surface, backpressure.SurfaceREST)
	if inputs[1].SourceLoaded {
		t.Fatalf("second input = %#v, want missing rest source", inputs[1])
	}
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
