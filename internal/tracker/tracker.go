// Package tracker provides state change tracking for delta snapshot queries.
// It maintains a ring buffer of state changes with configurable size and age limits.
package tracker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// SinceBySession returns changes since the given timestamp for a specific session
func (t *StateTracker) SinceBySession(ts time.Time, session string) []StateChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]StateChange, 0)
	for _, change := range t.changes {
		if change.Timestamp.After(ts) && change.Session == session {
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

// DefaultSnapshotOptions provides conservative defaults (skip .git and common huge dirs).
func DefaultSnapshotOptions(root string) SnapshotOptions {
	return SnapshotOptions{
		IgnoreHidden: false,
		IgnorePaths: []string{
			filepath.Join(root, ".git"),
			filepath.Join(root, "node_modules"),
			filepath.Join(root, "__pycache__"),
			filepath.Join(root, ".venv"),
			filepath.Join(root, "target"),
			filepath.Join(root, "dist"),
			filepath.Join(root, "build"),
		},
		IgnoreGitIgnored: true,
	}
}

// isGitRepo checks if a directory is a git repository
func isGitRepo(root string) bool {
	gitDir := filepath.Join(root, ".git")
	_, err := os.Stat(gitDir)
	return err == nil
}

// SnapshotDirectory walks a directory and captures file modtime/size.
// Returns a map keyed by absolute path.
func SnapshotDirectory(root string, opts SnapshotOptions) (map[string]FileState, error) {
	// Optimization: If this is a git repo, use git status for fast scanning
	// This avoids walking the entire directory tree (stat storm).
	if isGitRepo(root) {
		if snap, err := SnapshotGit(root, opts); err == nil {
			return snap, nil
		}
		// If git status fails (e.g., corrupt repo, no git binary), fall back to standard walk
	}

	entries := make([]fileEntry, 0)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied or other error accessing path.
			// If it's a directory, skip it to avoid getting stuck.
			// Otherwise just skip this file.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip configured ignore prefixes
		for _, p := range opts.IgnorePaths {
			if path == p || strings.HasPrefix(path, p+string(filepath.Separator)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		// Optionally skip hidden entries
		if opts.IgnoreHidden {
			_, name := filepath.Split(path)
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		entries = append(entries, fileEntry{
			path: path,
			state: FileState{
				ModTime: info.ModTime(),
				Size:    info.Size(),
			},
		})
		return nil
	})

	if err != nil {
		return nil, err
	}

	ignored := map[string]bool{}
	if opts.IgnoreGitIgnored && len(entries) > 0 {
		if ig, err := gitCheckIgnored(root, entries); err == nil {
			ignored = ig
		}
	}

	snap := make(map[string]FileState, len(entries))
	for _, entry := range entries {
		if ignored[entry.path] {
			continue
		}
		snap[entry.path] = entry.state
	}

	return snap, nil
}

// SnapshotGit uses 'git status' to efficiently find changed/untracked files.
// This is O(1) git operation + O(N) stat calls for *dirty files only*,
// compared to O(M) stat calls for *all files* with WalkDir.
func SnapshotGit(root string, opts SnapshotOptions) (map[string]FileState, error) {
	// First check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git binary not found")
	}

	// git status --porcelain --no-renames (shows modified and untracked, splits renames)
	// Note: Avoid -uall flag which can cause memory issues on large repos
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain", "--no-renames")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git status start failed: %w", err)
	}

	snap := make(map[string]FileState)
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 4 {
			continue
		}

		// Format: XY PATH
		// XY are status codes (e.g. "M ", "??", " A")
		status := line[:2]
		relPath := line[3:]

		// Handle quoted paths from git (e.g. "file with spaces.txt")
		if len(relPath) >= 2 && strings.HasPrefix(relPath, "\"") && strings.HasSuffix(relPath, "\"") {
			if unquoted, err := strconv.Unquote(relPath); err == nil {
				relPath = unquoted
			} else {
				// Fallback: just strip quotes if unquote fails
				relPath = relPath[1 : len(relPath)-1]
			}
		}

		fullPath := filepath.Join(root, relPath)

		// Check ignore paths (though git usually handles this, opts might add extras)
		ignored := false
		for _, p := range opts.IgnorePaths {
			if fullPath == p || strings.HasPrefix(fullPath, p+string(filepath.Separator)) {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}

		// Stat the file to get metadata
		info, err := os.Stat(fullPath)
		if err != nil {
			// If file is deleted, os.Stat fails.
			// If status indicates deletion, preserve it so we can detect it later.
			if strings.Contains(status, "D") {
				snap[fullPath] = FileState{
					ModTime:   time.Time{}, // Zero time
					Size:      0,
					GitStatus: strings.TrimSpace(status),
				}
			}
			continue
		}
		if info.IsDir() {
			continue
		}

		snap[fullPath] = FileState{
			ModTime:   info.ModTime(),
			Size:      info.Size(),
			GitStatus: strings.TrimSpace(status),
		}
	}

	if err := cmd.Wait(); err != nil {
		// git status might exit non-zero if no repo? Check error message?
		// But IsGitRepository check usually precedes this.
		// If Wait fails, we might still have useful partial data, but safer to return error.
		return nil, fmt.Errorf("git status wait failed: %w", err)
	}

	return snap, nil
}

// gitCheckIgnored returns a set of paths that git considers ignored.
// Best-effort: failures return an empty map.
func gitCheckIgnored(root string, entries []fileEntry) (map[string]bool, error) {
	result := make(map[string]bool)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "-z", "--stdin")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close() // Clean up stdin pipe on stdout pipe failure
		return result, err
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()  // Clean up pipes on start failure
		stdout.Close() // (stdout pipe also needs closing)
		return result, err
	}

	// Write paths to stdin in background
	go func() {
		defer stdin.Close()
		for _, e := range entries {
			// Ignore errors here - if pipe closes, cmd has likely exited
			_, _ = stdin.Write([]byte(e.path))
			_, _ = stdin.Write([]byte{0}) // Null terminator for -z
		}
	}()

	// Read output incrementally
	scanner := bufio.NewScanner(stdout)
	scanner.Split(scanNullTerminated)

	for scanner.Scan() {
		text := scanner.Text()
		if text != "" {
			result[text] = true
		}
	}

	// Wait for command to finish
	_ = cmd.Wait()

	return result, nil
}

// scanNullTerminated is a split function for bufio.Scanner that splits on null bytes.
func scanNullTerminated(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		// We have a full null-terminated token.
		return i + 1, data[0:i], nil
	}
	// If we're at EOF, we have a final, non-terminated token. Return it.
	if atEOF {
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

// DetectFileChanges compares two snapshots and returns the delta.
func DetectFileChanges(before, after map[string]FileState) []FileChange {
	changes := make([]FileChange, 0)

	// Detect additions and modifications
	for path, afterState := range after {
		beforeState, existed := before[path]
		if !existed {
			// If missing from 'before', it's a candidate for Added.
			// However, if we used GitSnapshot, 'before' might only contain dirty files.
			// If 'after' has GitStatus "M", it means it existed before but was clean.
			// We treat this as Modified.
			changeType := FileAdded

			// If git says it's Deleted ("D"), it's a deletion of a previously clean file
			if strings.Contains(afterState.GitStatus, "D") {
				changes = append(changes, FileChange{
					Path:  path,
					Type:  FileDeleted,
					After: &afterState,
				})
				continue
			}

			// Heuristic: If git says it's Modified ("M"), treat as Modified even if missing from Before.
			if afterState.GitStatus == "M" || afterState.GitStatus == "MM" {
				changeType = FileModified
			}

			changes = append(changes, FileChange{
				Path:  path,
				Type:  changeType,
				After: &afterState,
			})
			continue
		}

		// Check for deletion if it existed in 'before' (dirty) and is now marked deleted in 'after'
		if strings.Contains(afterState.GitStatus, "D") {
			b := beforeState
			changes = append(changes, FileChange{
				Path:   path,
				Type:   FileDeleted,
				Before: &b,
				After:  &afterState,
			})
			continue
		}

		if !afterState.ModTime.Equal(beforeState.ModTime) || afterState.Size != beforeState.Size {
			// Modified
			b := beforeState
			a := afterState
			changes = append(changes, FileChange{
				Path:   path,
				Type:   FileModified,
				Before: &b,
				After:  &a,
			})
		}
	}

	// Detect deletions
	for path, beforeState := range before {
		if _, ok := after[path]; !ok {
			// Verify file is actually gone (handle git clean files missing from snapshot)
			if _, err := os.Stat(path); err == nil {
				continue
			}

			b := beforeState
			changes = append(changes, FileChange{
				Path:   path,
				Type:   FileDeleted,
				Before: &b,
			})
		}
	}

	return changes
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

// RecordFileChanges captures and stores file changes after a delay.
// It is best-effort and meant to attribute changes to targeted agents.
// Uses bounded concurrency to prevent goroutine accumulation.
func RecordFileChanges(session, root string, agents []string, before map[string]FileState, delay time.Duration) {
	if root == "" || len(before) == 0 {
		return
	}

	// Try to acquire semaphore slot; drop request if at capacity (best-effort)
	select {
	case fileRecordSem <- struct{}{}:
		// Got slot, proceed
	default:
		// At capacity, silently drop this recording request
		return
	}

	go func() {
		defer func() {
			// Release semaphore slot
			<-fileRecordSem
		}()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker: callback panic recovered: %v", r)
			}
		}()

		time.Sleep(delay)

		after, err := SnapshotDirectory(root, DefaultSnapshotOptions(root))
		if err != nil {
			return
		}

		changes := DetectFileChanges(before, after)
		if len(changes) == 0 {
			return
		}

		for _, change := range changes {
			GlobalFileChanges.Add(RecordedFileChange{
				Session:   session,
				Agents:    append([]string{}, agents...),
				Change:    change,
				Timestamp: time.Now(),
			})
		}
	}()
}
