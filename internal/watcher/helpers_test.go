package watcher

import (
	"context"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Filesystem-only test helpers moved with their implementation to
// internal/fs/watcher. Reservation helpers remain at the orchestration layer.

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
