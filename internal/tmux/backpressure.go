package tmux

import (
	"context"
	"errors"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// CaptureBackpressureStats is the low-level capture evidence exported to the
// shared overload broker.
type CaptureBackpressureStats struct {
	Session      string
	Pane         string
	Target       string
	Lines        int
	Latency      time.Duration
	Err          error
	SourceLoaded bool
}

// CaptureBackpressureInput converts a tmux capture attempt into a shared
// backpressure surface row.
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
