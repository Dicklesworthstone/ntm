package state

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ReconcileResult summarizes what ReconcileSessions found and fixed.
type ReconcileResult struct {
	Checked    int      `json:"checked"`
	Terminated []string `json:"terminated,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// ReconcileSessions compares active sessions in the state store against
// live tmux sessions and marks any that no longer exist in tmux as
// terminated.  This handles the case where tmux sessions are destroyed
// externally (e.g. OOM kill, reboot, manual `tmux kill-session`).
//
// It should be called during startup and, optionally, periodically
// during long-running serve mode.
func (s *Store) ReconcileSessions() (*ReconcileResult, error) {
	activeSessions, err := s.ListSessions(string(SessionActive))
	if err != nil {
		return nil, fmt.Errorf("reconcile: list active sessions: %w", err)
	}

	if len(activeSessions) == 0 {
		return &ReconcileResult{}, nil
	}

	// Build a set of live tmux session names for O(1) lookup.
	liveSessions, err := tmux.ListSessions()
	if err != nil {
		// tmux might not be running at all — that's fine, it means
		// ALL active sessions in the store are stale.
		slog.Warn("reconcile: tmux.ListSessions failed, treating all active sessions as stale",
			"error", err, "active_count", len(activeSessions))
		liveSessions = nil
	}

	liveSet := make(map[string]struct{}, len(liveSessions))
	for _, ls := range liveSessions {
		liveSet[ls.Name] = struct{}{}
	}

	result := &ReconcileResult{
		Checked: len(activeSessions),
	}

	for _, sess := range activeSessions {
		if _, alive := liveSet[sess.Name]; alive {
			continue
		}

		// Session is in the store as "active" but does not exist in tmux.
		slog.Info("reconcile: marking stale session as terminated",
			"session_id", sess.ID, "session_name", sess.Name)

		sess.Status = SessionTerminated
		if updateErr := s.UpdateSession(&sess); updateErr != nil {
			errMsg := fmt.Sprintf("reconcile: update session %s (%s): %v", sess.Name, sess.ID, updateErr)
			slog.Error(errMsg)
			result.Errors = append(result.Errors, errMsg)
			continue
		}
		result.Terminated = append(result.Terminated, sess.Name)
	}

	if len(result.Terminated) > 0 {
		slog.Info("reconcile: completed",
			"checked", result.Checked,
			"terminated", len(result.Terminated),
			"errors", len(result.Errors),
			"timestamp", time.Now().UTC().Format(time.RFC3339))
	}

	return result, nil
}
