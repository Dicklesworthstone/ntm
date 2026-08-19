// Package state provides durable SQLite-backed storage for NTM orchestration state.
// This file integrates timeline tracking and persistence with the session lifecycle.
package state

import (
	"sync"
)

// TimelineLifecycle manages the integration between TimelineTracker and
// TimelinePersister for automatic persistence during session lifecycle.
type TimelineLifecycle struct {
	mu          sync.Mutex
	lifecycleMu sync.Mutex
	tracker     *TimelineTracker
	persister   *TimelinePersister
	stopped     bool

	// activeSessions tracks sessions with active checkpointing
	activeSessions map[string]struct{}
}

// NewTimelineLifecycle creates a new lifecycle manager with the given tracker and persister.
// If tracker is nil, uses the global tracker. If persister is nil, uses the default persister.
func NewTimelineLifecycle(tracker *TimelineTracker, persister *TimelinePersister) (*TimelineLifecycle, error) {
	if tracker == nil {
		tracker = GetGlobalTimelineTracker()
	}

	if persister == nil {
		var err error
		persister, err = GetDefaultTimelinePersister()
		if err != nil {
			return nil, err
		}
	}

	return &TimelineLifecycle{
		tracker:        tracker,
		persister:      persister,
		activeSessions: make(map[string]struct{}),
	}, nil
}

// StartSession begins timeline tracking and periodic persistence for a session.
// This should be called when a session is spawned.
func (l *TimelineLifecycle) StartSession(sessionID string) {
	normalizedSessionID, err := validateTimelineSessionID(sessionID)
	if err != nil {
		return
	}

	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()

	if l.stopped {
		return
	}

	l.mu.Lock()
	if _, active := l.activeSessions[normalizedSessionID]; active {
		l.mu.Unlock()
		return // Already tracking
	}
	l.activeSessions[normalizedSessionID] = struct{}{}
	l.mu.Unlock()

	l.persister.StartCheckpoint(normalizedSessionID, l.tracker)
}

// EndSession finalizes timeline persistence for a session.
// This should be called when a session is killed.
func (l *TimelineLifecycle) EndSession(sessionID string) error {
	normalizedSessionID, err := validateTimelineSessionID(sessionID)
	if err != nil {
		return err
	}

	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()

	if l.stopped {
		return nil
	}

	l.mu.Lock()
	delete(l.activeSessions, normalizedSessionID)
	l.mu.Unlock()

	return l.persister.FinalizeSession(normalizedSessionID, l.tracker)
}

// Global singleton lifecycle manager
var (
	globalTimelineLifecycle     *TimelineLifecycle
	globalTimelineLifecycleOnce sync.Once
	globalTimelineLifecycleErr  error
)

// GetGlobalTimelineLifecycle returns the singleton lifecycle manager.
func GetGlobalTimelineLifecycle() (*TimelineLifecycle, error) {
	globalTimelineLifecycleOnce.Do(func() {
		globalTimelineLifecycle, globalTimelineLifecycleErr = NewTimelineLifecycle(nil, nil)
	})
	return globalTimelineLifecycle, globalTimelineLifecycleErr
}

// StartSessionTimeline is a convenience function to start timeline tracking for a session.
// It uses the global lifecycle manager.
func StartSessionTimeline(sessionID string) error {
	if _, err := validateTimelineSessionID(sessionID); err != nil {
		return err
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		return err
	}
	lifecycle.StartSession(sessionID)
	return nil
}

// EndSessionTimeline is a convenience function to finalize timeline for a session.
// It uses the global lifecycle manager.
func EndSessionTimeline(sessionID string) error {
	if _, err := validateTimelineSessionID(sessionID); err != nil {
		return err
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		return err
	}
	return lifecycle.EndSession(sessionID)
}
