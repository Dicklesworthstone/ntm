package watcher

import (
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Lease liveness is independent of terminal byte velocity. A model can keep
// working without redrawing its frame; failed observations are not idle proof.
type reservationActivity uint8

const (
	reservationActivityUnknown reservationActivity = iota
	reservationActivityWorking
	reservationActivityIdle
	reservationActivityGone
)

type reservationActivityObservation struct {
	state      reservationActivity
	observedAt time.Time
}

// classifyReservationActivity uses the same detector as canonical status, but
// never supplies tmux's window-scoped activity timestamp: a busy sibling must
// not keep this pane's leases alive. Only the current viewport is classified.
// The detector's low-confidence short-line heuristic is not idle evidence.
func classifyReservationActivity(pane tmux.Pane, output string, now time.Time) reservationActivity {
	if pane.Height > 0 {
		lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
		if len(lines) > pane.Height {
			output = strings.Join(lines[len(lines)-pane.Height:], "\n")
		}
	}
	detected := status.NewDetector().AnalyzeAt(pane.ID, pane.Title, string(pane.Type), output, time.Time{}, now)
	switch detected.State {
	case status.StateWorking:
		return reservationActivityWorking
	case status.StateIdle:
		if status.DetectIdleFromOutput(output, string(pane.Type)) {
			return reservationActivityIdle
		}
	}
	return reservationActivityUnknown
}

// invalidateReservationActivity runs before topology I/O. A failed listing or
// capture must not leave a previous idle observation authorized for cleanup.
func (w *FileReservationWatcher) invalidateReservationActivity(paneID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paneActivity == nil {
		w.paneActivity = make(map[string]reservationActivityObservation)
	}
	if paneID != "" {
		w.paneActivity[paneID] = reservationActivityObservation{}
		return
	}
	for id := range w.activeReservations {
		w.paneActivity[id] = reservationActivityObservation{}
	}
}

func (w *FileReservationWatcher) observeReservationActivity(pane tmux.Pane, output string, now time.Time) bool {
	classify := w.classifyPaneActivity
	if classify == nil {
		classify = classifyReservationActivity
	}
	activity := classify(pane, output, now)
	name := w.resolveAgentName(pane.ID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paneActivity == nil {
		w.paneActivity = make(map[string]reservationActivityObservation)
	}
	w.paneActivity[pane.ID] = reservationActivityObservation{state: activity, observedAt: now}
	tracked := w.activeReservations[pane.ID]
	if activity != reservationActivityWorking || tracked == nil || name == "" || tracked.AgentName != name {
		return false
	}
	// This is fresh work evidence, not a fabricated output change. Keeping
	// LastOutput untouched preserves edit deduplication and pending recovery.
	tracked.LastActivity = now
	return !now.Before(tracked.nextHeartbeat)
}

// reconcileReservationActivityLocked is called only after a successful full
// topology scan. Disappearance permits the ordinary inactivity cleanup, not
// broad release or reassignment of another identity's reservations.
func (w *FileReservationWatcher) reconcileReservationActivityLocked(livePanes map[string]bool, now time.Time) {
	for id := range w.paneActivity {
		if !livePanes[id] {
			if _, held := w.activeReservations[id]; held {
				w.paneActivity[id] = reservationActivityObservation{state: reservationActivityGone, observedAt: now}
			} else {
				delete(w.paneActivity, id)
			}
		}
	}
}

// reservationIdleReleaseAllowedLocked applies the latest polling evidence to
// inactivity cleanup. Stop intentionally bypasses this gate to release owned
// leases. An entry is created before every production capture/acquisition.
func (w *FileReservationWatcher) reservationIdleReleaseAllowedLocked(paneID string, now time.Time) bool {
	observation, observed := w.paneActivity[paneID]
	if !observed {
		return true // Handles installed outside polling retain their inactivity policy.
	}
	if observation.state != reservationActivityIdle && observation.state != reservationActivityGone {
		return false
	}
	// Bound the age even when a later pane's network operation stalls a poll.
	maxAge := 2 * min(w.pollInterval, 15*time.Second)
	if maxAge < time.Second {
		maxAge = time.Second
	}
	age := now.Sub(observation.observedAt)
	return !observation.observedAt.IsZero() && age >= 0 && age <= maxAge
}
