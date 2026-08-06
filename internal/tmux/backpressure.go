package tmux

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

const maxCaptureBackpressureRows = 256

// captureBackpressureTracker owns the live, bounded set of the most recent
// capture attempts. It is deliberately attached to a Client so remote and
// local tmux clients cannot overwrite each other's measurements.
type captureBackpressureTracker struct {
	mu        sync.RWMutex
	entries   map[string]captureBackpressureEntry
	nextOrder uint64
}

type captureBackpressureEntry struct {
	stats CaptureBackpressureStats
	order uint64
}

func newCaptureBackpressureTracker() *captureBackpressureTracker {
	return &captureBackpressureTracker{entries: make(map[string]captureBackpressureEntry)}
}

func (t *captureBackpressureTracker) record(stats CaptureBackpressureStats) {
	if t == nil || stats.Target == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextOrder++
	if _, exists := t.entries[stats.Target]; !exists && len(t.entries) >= maxCaptureBackpressureRows {
		var oldestTarget string
		var oldestOrder uint64
		for target, entry := range t.entries {
			if oldestTarget == "" || entry.order < oldestOrder {
				oldestTarget = target
				oldestOrder = entry.order
			}
		}
		delete(t.entries, oldestTarget)
	}
	t.entries[stats.Target] = captureBackpressureEntry{stats: stats, order: t.nextOrder}
}

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

// CaptureBackpressureInputs returns the current live capture measurements for
// this client. Each pane contributes its most recent real capture attempt.
func (c *Client) CaptureBackpressureInputs() []backpressure.SurfaceInput {
	if c == nil {
		return []backpressure.SurfaceInput{}
	}
	return c.captureBackpressure.inputs()
}

// DefaultCaptureBackpressureInputs exposes the local runtime client's current
// capture measurements to snapshot assemblers.
func DefaultCaptureBackpressureInputs() []backpressure.SurfaceInput {
	return DefaultClient.CaptureBackpressureInputs()
}

func (c *Client) recordCaptureBackpressure(target string, lines int, latency time.Duration, err error) {
	if c == nil {
		return
	}
	session, pane := captureBackpressureIdentity(target)
	c.captureBackpressure.record(CaptureBackpressureStats{
		Session:      session,
		Pane:         pane,
		Target:       target,
		Lines:        lines,
		Latency:      latency,
		Err:          err,
		SourceLoaded: true,
	})
}

func captureBackpressureIdentity(target string) (session, pane string) {
	pane = target
	if before, _, found := strings.Cut(target, ":"); found {
		return before, target
	}
	return "", pane
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
