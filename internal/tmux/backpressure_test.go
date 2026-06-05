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

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
