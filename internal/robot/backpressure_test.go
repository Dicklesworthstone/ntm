package robot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

func TestCommandBackpressureInputMapsRobotCommand(t *testing.T) {
	input := CommandBackpressureInput(CommandBackpressureStats{
		Command:       "robot-status",
		Session:       "proj",
		Pane:          "1",
		QueueDepth:    8,
		QueueCapacity: 10,
		Latency:       1200 * time.Millisecond,
	})

	requireEqual(t, input.Surface, backpressure.SurfaceRobot)
	requireEqual(t, input.Command, "robot-status")
	requireEqual(t, input.Session, "proj")
	requireEqual(t, input.Pane, "1")
	requireEqual(t, input.LatencyMS, int64(1200))
}

func TestBuildBackpressureOutputCarriesRetryHint(t *testing.T) {
	snap := backpressure.Evaluate([]backpressure.SurfaceInput{
		{
			Surface:      backpressure.SurfaceRobot,
			Command:      "robot-tail",
			LatencyMS:    6000,
			SourceLoaded: true,
		},
	}, backpressure.SnapshotOptions{})

	out := BuildBackpressureOutput(snap)
	if out.Success {
		t.Fatalf("success = true, want false for degraded snapshot: %#v", out)
	}
	requireEqual(t, out.ErrorCode, ErrCodeResourceBusy)
	if len(out.Hint) < 1 || out.Snapshot.RetryAfterMS < 1 {
		t.Fatalf("missing retry hint: %#v", out)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["backpressure"]; !ok {
		t.Fatalf("robot output missing backpressure payload: %s", raw)
	}
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
