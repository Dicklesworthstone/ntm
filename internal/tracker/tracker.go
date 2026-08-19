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
