package state

import (
	"errors"
	"fmt"
	"log/slog"

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
		// If the circuit breaker is open, we cannot determine whether
		// sessions are alive or dead — skip reconciliation entirely
		// rather than aggressively terminating everything.
		if errors.Is(err, tmux.ErrCircuitOpen) {
			slog.Warn("reconcile: skipped, tmux circuit breaker is open",
				"active_count", len(activeSessions))
			return &ReconcileResult{Checked: 0}, nil
		}
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
			slog.Error("reconcile: failed to update session",
				"session_name", sess.Name, "session_id", sess.ID, "error", updateErr)
			result.Errors = append(result.Errors,
				fmt.Sprintf("%s (%s): %v", sess.Name, sess.ID, updateErr))
			continue
		}
		result.Terminated = append(result.Terminated, sess.Name)
	}

	if len(result.Terminated) > 0 {
		slog.Info("reconcile: completed",
			"checked", result.Checked,
			"terminated", len(result.Terminated),
			"errors", len(result.Errors))
	}

	return result, nil
}
