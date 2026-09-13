// Package tracker provides state change tracking for delta snapshot queries.
// It maintains a ring buffer of state changes with configurable size and age limits.
package tracker

import (
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
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

// Add appends a change, overwriting the oldest entry once the buffer is full.
//
// This is the store's only write path. It was deleted as dead code in 670f6380
// after its last caller went with the original capture pipeline, which left the
// readers below reporting an all-clear over a store nothing could fill; see
// git_recorder.go.
func (s *FileChangeStore) Add(entry RecordedFileChange) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.limit <= 0 {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	if len(s.entries) < s.limit {
		s.entries = append(s.entries, entry)
		return
	}
	s.full = true
	s.entries[s.cursor] = entry
	s.cursor = (s.cursor + 1) % s.limit
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

// The MaxConcurrentFileRecords/fileRecordSem pair that used to sit here bounded
// the goroutine RecordFileChanges spawned per dispatch. Both outlived that
// function's deletion, referenced nothing, and documented behavior the binary no
// longer had. GitRecorder samples synchronously from the caller's loop, so there
// are no per-dispatch goroutines left to bound.

// RecordedChangesSince returns file changes after the provided timestamp for
// the project the caller is standing in.
//
// Reads prefer the durable ledger (see persist.go): the session monitor records
// in its own process, so anything this process happens to hold in memory is at
// best a same-process fraction of the truth. The in-memory ring remains the
// fallback for when the shared store cannot be opened.
func RecordedChangesSince(ts time.Time) []RecordedFileChange {
	if changes, ok := durableChangesSince(ts); ok {
		return changes
	}
	return GlobalFileChanges.Since(ts)
}

// RecordedChanges returns recorded file changes for the current project within
// the durable ledger's retention window.
func RecordedChanges() []RecordedFileChange {
	if changes, ok := durableChangesSince(time.Now().Add(-state.FileChangeRetention)); ok {
		return changes
	}
	return GlobalFileChanges.All()
}
