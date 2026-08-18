// Package tracker provides state change tracking for delta snapshot queries.
// It maintains a ring buffer of state changes with configurable size and age limits.
package tracker

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ChangeType represents the type of state change
type ChangeType string

const (
	ChangeAgentOutput    ChangeType = "agent_output"
	ChangeAgentState     ChangeType = "agent_state"
	ChangeBeadUpdate     ChangeType = "bead_update"
	ChangeMailReceived   ChangeType = "mail_received"
	ChangeAlert          ChangeType = "alert"
	ChangePaneCreated    ChangeType = "pane_created"
	ChangePaneRemoved    ChangeType = "pane_removed"
	ChangeSessionCreated ChangeType = "session_created"
	ChangeSessionRemoved ChangeType = "session_removed"
	ChangeFileChange     ChangeType = "file_change"
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
	cursor  int  // Next write position (oldest element if full)
	full    bool // Whether the buffer has wrapped around
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

	// Prune old entries if we haven't wrapped yet.
	// Once full, we overwrite oldest instead of shifting.
	if !t.full {
		t.pruneOld()
	}

	if len(t.changes) < t.maxSize {
		t.changes = append(t.changes, change)
	} else {
		t.full = true
		t.changes[t.cursor] = change
		t.cursor = (t.cursor + 1) % t.maxSize
	}
}

// Since returns all changes since the given timestamp
func (t *StateTracker) Since(ts time.Time) []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, 0)
	count := len(t.changes)
	if count == 0 {
		return result
	}

	start := 0
	if t.full {
		start = t.cursor
	}

	for i := 0; i < count; i++ {
		idx := (start + i) % count
		change := t.changes[idx]
		if change.Timestamp.After(ts) {
			// Deep copy Details to prevent data races
			newChange := change
			if change.Details != nil {
				newChange.Details = make(map[string]interface{}, len(change.Details))
				for k, v := range change.Details {
					newChange.Details[k] = v
				}
			}
			result = append(result, newChange)
		}
	}
	return result
}

// All returns all tracked changes
func (t *StateTracker) All() []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	count := len(t.changes)
	result := make([]StateChange, 0, count)

	start := 0
	if t.full {
		start = t.cursor
	}

	for i := 0; i < count; i++ {
		idx := (start + i) % count
		change := t.changes[idx]
		// Deep copy Details to prevent data races
		newChange := change
		if change.Details != nil {
			newChange.Details = make(map[string]interface{}, len(change.Details))
			for k, v := range change.Details {
				newChange.Details[k] = v
			}
		}
		result = append(result, newChange)
	}
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
	t.cursor = 0
	t.full = false
}

// pruneOld removes changes older than maxAge (must be called with lock held)
func (t *StateTracker) pruneOld() {
	if len(t.changes) == 0 || t.full {
		// Circular buffer doesn't support easy middle-pruning without shifting.
		// For StateTracker, we rely on maxSize to bound memory, and filter by age during query.
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
		surviving := make([]StateChange, len(t.changes)-keepFrom, t.maxSize)
		copy(surviving, t.changes[keepFrom:])
		t.changes = surviving
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
	Type    ChangeType `json:"type"`
	Session string     `json:"session,omitempty"`
	Pane    string     `json:"pane,omitempty"`
	Count   int        `json:"count"`
	FirstAt time.Time  `json:"first_at"`
	LastAt  time.Time  `json:"last_at"`
}

// Coalesce merges consecutive changes of the same type for the same pane
// into summary entries. Useful for reducing output volume.
func (t *StateTracker) Coalesce() []CoalescedChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	count := len(t.changes)
	if count == 0 {
		return nil
	}

	result := make([]CoalescedChange, 0)
	var current *CoalescedChange

	start := 0
	if t.full {
		start = t.cursor
	}

	for i := 0; i < count; i++ {
		idx := (start + i) % count
		change := t.changes[idx]

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
	count := len(t.changes)
	if count == 0 {
		return result
	}

	start := 0
	if t.full {
		start = t.cursor
	}

	for i := 0; i < count; i++ {
		idx := (start + i) % count
		change := t.changes[idx]
		if change.Timestamp.After(ts) && change.Type == changeType {
			// Deep copy Details
			newChange := change
			if change.Details != nil {
				newChange.Details = make(map[string]interface{}, len(change.Details))
				for k, v := range change.Details {
					newChange.Details[k] = v
				}
			}
			result = append(result, newChange)
		}
	}
	return result
}

// Helper functions for common change types

// FileState captures minimal file metadata for change detection.
type FileState struct {
	ModTime   time.Time `json:"mod_time"`
	Size      int64     `json:"size"`
	GitStatus string    `json:"git_status,omitempty"` // ??, M, A, etc.
}

// FileChangeType indicates what happened to a file.
type FileChangeType string

const (
	FileAdded    FileChangeType = "added"
	FileModified FileChangeType = "modified"
	FileDeleted  FileChangeType = "deleted"
)

// FileChange represents a single file change between two snapshots.
type FileChange struct {
	Path   string         `json:"path"`
	Type   FileChangeType `json:"type"`
	Before *FileState     `json:"before,omitempty"`
	After  *FileState     `json:"after,omitempty"`
}

// fileEntry is an internal representation used during snapshotting.
type fileEntry struct {
	path  string
	state FileState
}

// SnapshotOptions controls how directory snapshots are taken.
type SnapshotOptions struct {
	// IgnoreHidden skips files/dirs beginning with '.'
	IgnoreHidden bool
	// IgnorePaths are path prefixes (absolute) to skip.
	IgnorePaths []string
	// IgnoreGitIgnored attempts to skip files ignored by git (best-effort).
	IgnoreGitIgnored bool
}

// isGitRepo checks if a directory is a git repository
func isGitRepo(root string) bool {
	gitDir := filepath.Join(root, ".git")
	_, err := os.Stat(gitDir)
	return err == nil
}

// RecordedFileChange captures file changes with attribution metadata.
type RecordedFileChange struct {
	Timestamp time.Time  `json:"timestamp"`
	Session   string     `json:"session"`
	Agents    []string   `json:"agents,omitempty"`
	Change    FileChange `json:"change"`
}

// FileChangeStore keeps a bounded buffer of recent file changes.
type FileChangeStore struct {
	mu      sync.RWMutex
	limit   int
	entries []RecordedFileChange
	cursor  int  // Next write position (oldest element if full)
	full    bool // Whether the buffer has wrapped around
}

// NewFileChangeStore creates a store with the provided capacity.
func NewFileChangeStore(limit int) *FileChangeStore {
	if limit <= 0 {
		limit = 500
	}
	return &FileChangeStore{
		limit:   limit,
		entries: make([]RecordedFileChange, 0, limit),
	}
}

// Add records a change and prunes oldest entries when over capacity.
func (s *FileChangeStore) Add(entry RecordedFileChange) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	if s.limit <= 0 {
		return
	}

	if len(s.entries) < s.limit {
		s.entries = append(s.entries, entry)
	} else {
		s.full = true
		s.entries[s.cursor] = entry
		s.cursor = (s.cursor + 1) % s.limit
	}
}

// Since returns changes after the provided timestamp.
func (s *FileChangeStore) Since(ts time.Time) []RecordedFileChange {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results := make([]RecordedFileChange, 0)
	count := len(s.entries)
	if count == 0 {
		return results
	}

	start := 0
	if s.full {
		start = s.cursor
	}

	for i := 0; i < count; i++ {
		idx := (start + i) % count
		e := s.entries[idx]
		if e.Timestamp.After(ts) {
			results = append(results, e)
		}
	}
	return results
}

// All returns a copy of all recorded changes.
func (s *FileChangeStore) All() []RecordedFileChange {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := len(s.entries)
	results := make([]RecordedFileChange, count)

	if !s.full {
		copy(results, s.entries)
	} else {
		// Reconstruct order: entries[cursor] is oldest
		n := copy(results, s.entries[s.cursor:])
		copy(results[n:], s.entries[:s.cursor])
	}
	return results
}

// GlobalFileChanges is the shared change store.
var GlobalFileChanges = NewFileChangeStore(500)

// MaxConcurrentFileRecords limits pending RecordFileChanges goroutines to prevent unbounded growth
const MaxConcurrentFileRecords = 50

// fileRecordSem limits concurrent RecordFileChanges operations
var fileRecordSem = make(chan struct{}, MaxConcurrentFileRecords)

// RecordedChangesSince returns file changes after the provided timestamp.
func RecordedChangesSince(ts time.Time) []RecordedFileChange {
	return GlobalFileChanges.Since(ts)
}

// RecordedChanges returns all recorded file changes.
func RecordedChanges() []RecordedFileChange {
	return GlobalFileChanges.All()
}
