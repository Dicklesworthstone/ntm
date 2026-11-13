package tmux

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

func (t *captureBackpressureTracker) inputs() []backpressure.SurfaceInput {
	if t == nil {
		return []backpressure.SurfaceInput{}
	}
	t.mu.RLock()
	entries := make([]captureBackpressureEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		entries = append(entries, entry)
	}
	t.mu.RUnlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].order < entries[j].order
	})
	inputs := make([]backpressure.SurfaceInput, 0, len(entries))
	for _, entry := range entries {
		inputs = append(inputs, CaptureBackpressureInput(entry.stats))
	}
	return inputs
}

func CaptureBackpressureInput(stats CaptureBackpressureStats) backpressure.SurfaceInput {
	loaded := stats.SourceLoaded
	if !loaded && (stats.Latency > 0 || stats.Err != nil || stats.Target != "" || stats.Session != "" || stats.Pane != "") {
		loaded = true
	}
	input := backpressure.SurfaceInput{
		Surface:      backpressure.SurfaceTmuxCapture,
		Session:      stats.Session,
		Pane:         firstNonEmpty(stats.Pane, stats.Target),
		LatencyMS:    stats.Latency.Milliseconds(),
		SourceLoaded: loaded,
	}
	if !loaded {
		input.MissingWarning = "tmux capture counters are not available."
	}
	if isCaptureTimeout(stats.Err) && input.LatencyMS == 0 {
		input.LatencyMS = DefaultCommandTimeout.Milliseconds()
	}
	return input
}

func (c *Client) CaptureBackpressureInputs() []backpressure.SurfaceInput {
	if c == nil {
		return []backpressure.SurfaceInput{}
	}
	return c.captureBackpressure.inputs()
}

func isCaptureTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return ClassifyCommandError(err).Kind == CommandErrorTimeout
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

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
	requireEqual(t, snapshot.Decision, backpressure.DecisionDefer)
	requireEqual(t, snapshot.Surfaces[1].Surface, backpressure.SurfaceTmuxCapture)
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
