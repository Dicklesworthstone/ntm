package tmux

import (
	"strings"
	"sync"
	"time"
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
