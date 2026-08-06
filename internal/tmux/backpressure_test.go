package tmux

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

func TestCaptureBackpressureInputMapsSlowCapture(t *testing.T) {
	input := CaptureBackpressureInput(CaptureBackpressureStats{
		Session: "proj",
		Pane:    "2",
		Latency: 1500 * time.Millisecond,
	})
	snap := backpressure.Evaluate([]backpressure.SurfaceInput{input}, backpressure.SnapshotOptions{})

	requireEqual(t, snap.Surfaces[0].Surface, backpressure.SurfaceTmuxCapture)
	requireEqual(t, snap.Surfaces[0].ReasonCodes, []backpressure.ReasonCode{backpressure.ReasonSlowCapture})
	requireEqual(t, snap.Surfaces[0].Decision, backpressure.DecisionDefer)
}

func TestCaptureBackpressureInputTimeoutUsesCommandTimeout(t *testing.T) {
	input := CaptureBackpressureInput(CaptureBackpressureStats{
		Target: "%1",
		Err:    context.DeadlineExceeded,
	})

	requireEqual(t, input.Pane, "%1")
	requireEqual(t, input.LatencyMS, DefaultCommandTimeout.Milliseconds())
}

func TestClientCaptureBackpressureInputsUseLiveCaptureAttempts(t *testing.T) {
	client := NewClient("")
	client.recordCaptureBackpressure("proj:0.1", 20, 1500*time.Millisecond, nil)
	client.recordCaptureBackpressure("%2", 50, 10*time.Millisecond, context.DeadlineExceeded)
	client.recordCaptureBackpressure("proj:0.1", 20, 2500*time.Millisecond, nil)

	inputs := client.CaptureBackpressureInputs()
	requireEqual(t, len(inputs), 2)
	requireEqual(t, inputs[0].Surface, backpressure.SurfaceTmuxCapture)
	requireEqual(t, inputs[0].Session, "")
	requireEqual(t, inputs[0].Pane, "%2")
	requireEqual(t, inputs[1].Session, "proj")
	requireEqual(t, inputs[1].Pane, "proj:0.1")
	requireEqual(t, inputs[1].LatencyMS, int64(2500))

	snapshot := backpressure.Evaluate(inputs, backpressure.SnapshotOptions{})
	requireEqual(t, snapshot.Decision, backpressure.DecisionDegrade)
	requireEqual(t, snapshot.Surfaces[1].Surface, backpressure.SurfaceTmuxCapture)
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
