// Package tracker provides state change tracking for delta snapshot queries.
// It maintains a ring buffer of state changes with configurable size and age limits.
package tracker

import (
	"sync"
	"time"
)

// ChangeType represents the type of state change
type ChangeType string

const (
	ChangeAgentOutput ChangeType = "agent_output"
	ChangeAgentState  ChangeType = "agent_state"
	ChangeBeadUpdate  ChangeType = "bead_update"
	ChangeMailReceived ChangeType = "mail_received"
	ChangeAlert       ChangeType = "alert"
	ChangePaneCreated ChangeType = "pane_created"
	ChangePaneRemoved ChangeType = "pane_removed"
	ChangeSessionCreated ChangeType = "session_created"
	ChangeSessionRemoved ChangeType = "session_removed"
)

// StateChange represents a single state change event
type StateChange struct {
	Timestamp time.Time              `json:"timestamp"`
	Type      ChangeType             `json:"type"`
	Session   string                 `json:"session,omitempty"`
	Pane      string                 `json:"pane,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// StateTracker maintains a ring buffer of state changes
type StateTracker struct {
	changes []StateChange
	maxAge  time.Duration
	maxSize int
	mu      sync.RWMutex
}

// DefaultMaxSize is the default maximum number of changes to retain
const DefaultMaxSize = 1000

// DefaultMaxAge is the default maximum age of changes to retain
const DefaultMaxAge = 5 * time.Minute

// New creates a new StateTracker with default settings
func New() *StateTracker {
	return NewWithConfig(DefaultMaxSize, DefaultMaxAge)
}

// NewWithConfig creates a new StateTracker with custom settings
func NewWithConfig(maxSize int, maxAge time.Duration) *StateTracker {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	return &StateTracker{
		changes: make([]StateChange, 0, maxSize),
		maxAge:  maxAge,
		maxSize: maxSize,
	}
}

// Record adds a new state change to the tracker
func (t *StateTracker) Record(change StateChange) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Set timestamp if not provided
	if change.Timestamp.IsZero() {
		change.Timestamp = time.Now()
	}

	// Prune old entries first
	t.pruneOld()

	// If at capacity, remove oldest
	if len(t.changes) >= t.maxSize {
		t.changes = t.changes[1:]
	}

	t.changes = append(t.changes, change)
}

// Since returns all changes since the given timestamp
func (t *StateTracker) Since(ts time.Time) []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, 0)
	for _, change := range t.changes {
		if change.Timestamp.After(ts) {
			result = append(result, change)
		}
	}
	return result
}

// All returns all tracked changes
func (t *StateTracker) All() []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, len(t.changes))
	copy(result, t.changes)
	return result
}

// Count returns the number of tracked changes
func (t *StateTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.changes)
}

// Clear removes all tracked changes
func (t *StateTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.changes = make([]StateChange, 0, t.maxSize)
}

// pruneOld removes changes older than maxAge (must be called with lock held)
func (t *StateTracker) pruneOld() {
	if len(t.changes) == 0 {
		return
	}

	cutoff := time.Now().Add(-t.maxAge)
	keepFrom := 0
	for i, change := range t.changes {
		if change.Timestamp.After(cutoff) {
			keepFrom = i
			break
		}
		keepFrom = i + 1
	}

	if keepFrom > 0 && keepFrom <= len(t.changes) {
		t.changes = t.changes[keepFrom:]
	}
}

// Prune manually triggers pruning of old entries
func (t *StateTracker) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneOld()
}

// CoalescedChange represents multiple changes merged into one summary
type CoalescedChange struct {
	Type       ChangeType `json:"type"`
	Session    string     `json:"session,omitempty"`
	Pane       string     `json:"pane,omitempty"`
	Count      int        `json:"count"`
	FirstAt    time.Time  `json:"first_at"`
	LastAt     time.Time  `json:"last_at"`
}

// Coalesce merges consecutive changes of the same type for the same pane
// into summary entries. Useful for reducing output volume.
func (t *StateTracker) Coalesce() []CoalescedChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.changes) == 0 {
		return nil
	}

	result := make([]CoalescedChange, 0)
	var current *CoalescedChange

	for _, change := range t.changes {
		// Check if we can merge with current
		if current != nil &&
			current.Type == change.Type &&
			current.Session == change.Session &&
			current.Pane == change.Pane {
			// Merge
			current.Count++
			current.LastAt = change.Timestamp
		} else {
			// Start new group
			if current != nil {
				result = append(result, *current)
			}
			current = &CoalescedChange{
				Type:    change.Type,
				Session: change.Session,
				Pane:    change.Pane,
				Count:   1,
				FirstAt: change.Timestamp,
				LastAt:  change.Timestamp,
			}
		}
	}

	if current != nil {
		result = append(result, *current)
	}

	return result
}

// SinceByType returns changes since the given timestamp, filtered by type
func (t *StateTracker) SinceByType(ts time.Time, changeType ChangeType) []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, 0)
	for _, change := range t.changes {
		if change.Timestamp.After(ts) && change.Type == changeType {
			result = append(result, change)
		}
	}
	return result
}

// SinceBySession returns changes since the given timestamp for a specific session
func (t *StateTracker) SinceBySession(ts time.Time, session string) []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, 0)
	for _, change := range t.changes {
		if change.Timestamp.After(ts) && change.Session == session {
			result = append(result, change)
		}
	}
	return result
}

// Helper functions for common change types

// RecordAgentOutput records an agent output change
func (t *StateTracker) RecordAgentOutput(session, pane, output string) {
	t.Record(StateChange{
		Type:    ChangeAgentOutput,
		Session: session,
		Pane:    pane,
		Details: map[string]interface{}{
			"output_length": len(output),
		},
	})
}

// RecordAgentState records an agent state change (idle, active, error)
func (t *StateTracker) RecordAgentState(session, pane, state string) {
	t.Record(StateChange{
		Type:    ChangeAgentState,
		Session: session,
		Pane:    pane,
		Details: map[string]interface{}{
			"state": state,
		},
	})
}

// RecordAlert records an alert
func (t *StateTracker) RecordAlert(session, pane, alertType, message string) {
	t.Record(StateChange{
		Type:    ChangeAlert,
		Session: session,
		Pane:    pane,
		Details: map[string]interface{}{
			"alert_type": alertType,
			"message":    message,
		},
	})
}

// RecordPaneCreated records a new pane
func (t *StateTracker) RecordPaneCreated(session, pane, agentType string) {
	t.Record(StateChange{
		Type:    ChangePaneCreated,
		Session: session,
		Pane:    pane,
		Details: map[string]interface{}{
			"agent_type": agentType,
		},
	})
}

// RecordSessionCreated records a new session
func (t *StateTracker) RecordSessionCreated(session string) {
	t.Record(StateChange{
		Type:    ChangeSessionCreated,
		Session: session,
	})
}
