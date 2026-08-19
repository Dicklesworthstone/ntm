package watcher

import (
	"context"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Test-only helpers replicating removed production accessors/options so tests
// can exercise live behavior (polling fallback, watch topology, reservations).

// WithErrorHandler sets the error handler.
func WithErrorHandler(handler ErrorHandler) Option {
	return func(w *Watcher) {
		w.errorHandler = handler
	}
}

// WithPolling forces polling mode so tests can exercise the live polling
// fallback deterministically.
func WithPolling(force bool) Option {
	return func(w *Watcher) {
		w.forcePoll = force
		if force {
			w.pollMode = true
		}
	}
}

// WatchedPaths returns a slice of all currently watched paths.
func (w *Watcher) WatchedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	paths := make([]string, 0, len(w.watchedPaths))
	for p := range w.watchedPaths {
		paths = append(paths, p)
	}
	return paths
}

// Duration returns the debounce duration.
func (d *Debouncer) Duration() time.Duration {
	return d.duration
}

// OnFileEdit handles detected file edits by reserving files.
func (w *FileReservationWatcher) OnFileEdit(ctx context.Context, sessionName string, pane tmux.Pane, files []string) {
	w.onFileEdit(ctx, sessionName, pane, "", files, time.Now())
}

// GetActiveReservations returns a copy of all active reservations.
func (w *FileReservationWatcher) GetActiveReservations() map[string]*PaneReservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make(map[string]*PaneReservation, len(w.activeReservations))
	for k, v := range w.activeReservations {
		copied := *v
		copied.Files = make([]string, len(v.Files))
		copy(copied.Files, v.Files)
		copied.ReservationID = make([]int, len(v.ReservationID))
		copy(copied.ReservationID, v.ReservationID)
		result[k] = &copied
	}
	return result
}

// DefaultFileReservationConfigValues returns the default values for file
// reservation config used as a baseline by option tests.
func DefaultFileReservationConfigValues() FileReservationConfigValues {
	return FileReservationConfigValues{
		Enabled:               true,
		AutoReserve:           true,
		AutoReleaseIdleMin:    10,
		NotifyOnConflict:      true,
		ExtendOnActivity:      true,
		DefaultTTLMin:         15,
		PollIntervalSec:       10,
		CaptureLinesForDetect: 100,
		Debug:                 false,
	}
}
