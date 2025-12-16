// Package watcher provides file watching with debouncing using fsnotify.
package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrClosed is returned when operations are called on a closed Watcher.
var ErrClosed = errors.New("watcher: watcher is closed")

// DefaultPollInterval is used when falling back to polling.
const DefaultPollInterval = time.Second

// EventType represents the type of file system event.
type EventType uint32

const (
	// Create is triggered when a file or directory is created.
	Create EventType = 1 << iota
	// Write is triggered when a file is modified.
	Write
	// Remove is triggered when a file or directory is removed.
	Remove
	// Rename is triggered when a file or directory is renamed.
	Rename
	// Chmod is triggered when file permissions change.
	Chmod
	// All events.
	All = Create | Write | Remove | Rename | Chmod
)

// Event represents a file system event.
type Event struct {
	// Path is the absolute path to the file or directory.
	Path string
	// Type is the type of event.
	Type EventType
	// IsDir is true if the event is for a directory.
	IsDir bool
}

// eventTypeFromFsnotify converts fsnotify.Op to EventType.
func eventTypeFromFsnotify(op fsnotify.Op) EventType {
	var t EventType
	if op.Has(fsnotify.Create) {
		t |= Create
	}
	if op.Has(fsnotify.Write) {
		t |= Write
	}
	if op.Has(fsnotify.Remove) {
		t |= Remove
	}
	if op.Has(fsnotify.Rename) {
		t |= Rename
	}
	if op.Has(fsnotify.Chmod) {
		t |= Chmod
	}
	return t
}

// Handler is called when a file system event occurs.
// Multiple events may be coalesced into a single call due to debouncing.
type Handler func(events []Event)

// ErrorHandler is called when a watch error occurs.
type ErrorHandler func(err error)

// fileMeta stores file metadata for poll-based change detection.
type fileMeta struct {
	ModTime time.Time
	Size    int64
	Mode    os.FileMode
	IsDir   bool
}

// Watcher watches files and directories for changes.
type Watcher struct {
	fsWatcher    *fsnotify.Watcher
	debouncer    *Debouncer
	handler      Handler
	errorHandler ErrorHandler
	eventFilter  EventType
	recursive    bool

	// Poll mode fields (for environments where fsnotify is unavailable)
	pollMode     bool
	pollInterval time.Duration
	snapshots    map[string]fileMeta
	closeCh      chan struct{}
	forcePoll    bool

	mu            sync.Mutex
	watchedPaths  map[string]bool
	ignorePaths   []string
	pendingEvents []Event
	closed        bool
}

// New creates a new Watcher.
// By default, all event types are watched. Use WithEventFilter to filter events.
// Use WithRecursive to watch directories recursively.
func New(handler Handler, opts ...Option) (*Watcher, error) {
	w := &Watcher{
		debouncer:    NewDebouncer(DefaultDebounceDuration),
		handler:      handler,
		eventFilter:  All,
		watchedPaths: make(map[string]bool),
		pollInterval: DefaultPollInterval,
	}

	for _, opt := range opts {
		opt(w)
	}

	if !w.forcePoll {
		fsWatcher, err := fsnotify.NewWatcher()
		if err == nil {
			w.fsWatcher = fsWatcher
		} else {
			if w.errorHandler != nil {
				w.errorHandler(fmt.Errorf("fsnotify unavailable, using polling fallback: %w", err))
			}
			w.pollMode = true
		}
	} else {
		w.pollMode = true
	}

	if w.pollMode {
		if w.pollInterval <= 0 {
			w.pollInterval = DefaultPollInterval
		}
		w.snapshots = make(map[string]fileMeta)
		w.closeCh = make(chan struct{})
		go w.runPoll()
	} else {
		go w.run()
	}

	return w, nil
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithDebounceDuration sets the debounce duration for coalescing events.
// Use a real time.Duration to avoid hidden unit conversions.
func WithDebounceDuration(d time.Duration) Option {
	return func(w *Watcher) {
		if d > 0 {
			w.debouncer = NewDebouncer(d)
		}
	}
}

// WithDebouncer sets a custom debouncer.
func WithDebouncer(d *Debouncer) Option {
	return func(w *Watcher) {
		if d != nil {
			w.debouncer = d
		}
	}
}

// WithEventFilter sets which event types to watch.
func WithEventFilter(filter EventType) Option {
	return func(w *Watcher) {
		w.eventFilter = filter
	}
}

// WithRecursive enables recursive watching of directories.
func WithRecursive(recursive bool) Option {
	return func(w *Watcher) {
		w.recursive = recursive
	}
}

// WithIgnorePaths sets patterns to ignore (matched against file/dir name).
func WithIgnorePaths(patterns []string) Option {
	return func(w *Watcher) {
		w.ignorePaths = patterns
	}
}

// WithErrorHandler sets the error handler.
func WithErrorHandler(handler ErrorHandler) Option {
	return func(w *Watcher) {
		w.errorHandler = handler
	}
}

// WithPollInterval sets the polling interval (used when polling mode is active).
func WithPollInterval(d time.Duration) Option {
	return func(w *Watcher) {
		if d > 0 {
			w.pollInterval = d
		}
	}
}

// WithPolling forces polling mode (useful for tests or environments without fsnotify support).
func WithPolling(force bool) Option {
	return func(w *Watcher) {
		w.forcePoll = force
		if force {
			w.pollMode = true
		}
	}
}

// Add adds a path to the watcher.
// If the path is a directory and recursive is enabled, all subdirectories are also watched.
func (w *Watcher) Add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrClosed
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	if w.watchedPaths[absPath] {
		return nil // Already watching
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	if w.pollMode {
		if err := w.snapshotPath(absPath, info); err != nil {
			return err
		}
		w.watchedPaths[absPath] = true
		return nil
	}

	if info.IsDir() && w.recursive {
		return w.addRecursive(absPath)
	}

	if err := w.fsWatcher.Add(absPath); err != nil {
		return err
	}
	w.watchedPaths[absPath] = true

	return nil
}

// isIgnored checks if the path should be ignored.
func (w *Watcher) isIgnored(path string) bool {
	name := filepath.Base(path)
	for _, pattern := range w.ignorePaths {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// addRecursive adds a directory and all its subdirectories to the watcher.
// Must be called with w.mu held.
func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip directories we can't access, but continue walking
			if w.errorHandler != nil {
				w.errorHandler(fmt.Errorf("walking %s: %w", path, err))
			}
			return filepath.SkipDir
		}
		if d.IsDir() {
			// Check ignores
			if w.isIgnored(path) {
				return filepath.SkipDir
			}

			if w.watchedPaths[path] {
				return nil
			}
			if w.pollMode {
				info, statErr := os.Stat(path)
				if statErr != nil {
					if w.errorHandler != nil {
						w.errorHandler(fmt.Errorf("stat %s: %w", path, statErr))
					}
					return nil
				}
				if snapErr := w.snapshotPath(path, info); snapErr != nil && w.errorHandler != nil {
					w.errorHandler(snapErr)
				}
				w.watchedPaths[path] = true
			} else {
				if err := w.fsWatcher.Add(path); err != nil {
					// Report error but continue
					if w.errorHandler != nil {
						w.errorHandler(fmt.Errorf("watching %s: %w", path, err))
					}
					// If we can't watch this directory, we probably can't watch its children?
					// fsnotify might fail for other reasons (limit reached).
					// We'll try to continue.
					return nil
				}
				w.watchedPaths[path] = true
			}
		}
		return nil
	})
}

// Remove removes a path from the watcher.
func (w *Watcher) Remove(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrClosed
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	if !w.watchedPaths[absPath] {
		return nil // Not watching
	}

	if w.pollMode {
		delete(w.watchedPaths, absPath)
		delete(w.snapshots, absPath)
		return nil
	}

	if err := w.fsWatcher.Remove(absPath); err != nil {
		return err
	}
	delete(w.watchedPaths, absPath)

	return nil
}

// Close stops the watcher and releases resources.
func (w *Watcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil
	}

	w.closed = true
	w.debouncer.Cancel()
	if w.pollMode {
		close(w.closeCh)
		return nil
	}
	return w.fsWatcher.Close()
}

// WatchedPaths returns a slice of all currently watched paths.
func (w *Watcher) WatchedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	paths := make([]string, 0, len(w.watchedPaths))
	for p := range w.watchedPaths {
		paths = append(paths, p)
	}
	return paths
}

// run processes events from fsnotify.
func (w *Watcher) run() {
	for {
		select {
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			if w.errorHandler != nil {
				w.errorHandler(err)
			}
		}
	}
}

// runPoll processes events by periodically scanning watched paths when fsnotify is unavailable.
func (w *Watcher) runPoll() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.pollOnce()
		case <-w.closeCh:
			return
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(fsEvent fsnotify.Event) {
	eventType := eventTypeFromFsnotify(fsEvent.Op)

	// Filter events
	if eventType&w.eventFilter == 0 {
		return
	}

	// Check if it's a directory
	isDir := false
	if info, err := os.Stat(fsEvent.Name); err == nil {
		isDir = info.IsDir()
	}

	event := Event{
		Path:  fsEvent.Name,
		Type:  eventType,
		IsDir: isDir,
	}

	// If recursive and a new directory was created, watch it
	if w.recursive && isDir && eventType&Create != 0 {
		// Check ignores
		if w.isIgnored(fsEvent.Name) {
			return
		}

		w.mu.Lock()
		if !w.closed && !w.watchedPaths[fsEvent.Name] {
			if err := w.fsWatcher.Add(fsEvent.Name); err != nil {
				// Report error via error handler if available
				if w.errorHandler != nil {
					w.errorHandler(err)
				}
			} else {
				w.watchedPaths[fsEvent.Name] = true
			}
		}
		w.mu.Unlock()
	}

	// If a watched directory was removed, clean up
	if eventType&Remove != 0 || eventType&Rename != 0 {
		w.mu.Lock()
		if w.watchedPaths[fsEvent.Name] {
			delete(w.watchedPaths, fsEvent.Name)
		}
		w.mu.Unlock()
	}

	w.mu.Lock()
	w.pendingEvents = append(w.pendingEvents, event)
	w.mu.Unlock()

	w.debouncer.Trigger(func() {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		toDeliver := w.pendingEvents
		w.pendingEvents = nil
		w.mu.Unlock()

		if len(toDeliver) > 0 && w.handler != nil {
			w.handler(toDeliver)
		}
	})
}

// pollOnce scans watched paths and emits events for changes since the last scan.
func (w *Watcher) pollOnce() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	// Snapshot the roots we intend to scan
	roots := make([]string, 0, len(w.watchedPaths))
	for p := range w.watchedPaths {
		roots = append(roots, p)
	}
	w.mu.Unlock()

	// Scan the filesystem (slow operation, done without lock)
	currentSnapshot := make(map[string]fileMeta)
	for _, root := range roots {
		entries, err := snapshotEntries(root, w.recursive, w.isIgnored)
		if err != nil {
			if w.errorHandler != nil {
				w.errorHandler(err)
			}
			continue
		}
		for p, meta := range entries {
			currentSnapshot[p] = meta
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return
	}

	var events []Event

	// 1. Detect Creates and Updates
	for path, meta := range currentSnapshot {
		// Verify the path is still covered by current watched paths
		// (Avoid re-adding files if Remove() was called during scan)
		if !w.isWatched(path) {
			continue
		}

		prev, ok := w.snapshots[path]
		if !ok {
			if Create&w.eventFilter != 0 {
				events = append(events, Event{Path: path, Type: Create, IsDir: meta.IsDir})
			}
			w.snapshots[path] = meta
			continue
		}

		eventType := EventType(0)
		if meta.ModTime != prev.ModTime || meta.Size != prev.Size {
			eventType |= Write
		}
		if meta.Mode != prev.Mode {
			eventType |= Chmod
		}
		if eventType != 0 {
			if eventType&w.eventFilter != 0 {
				events = append(events, Event{Path: path, Type: eventType, IsDir: meta.IsDir})
			}
			w.snapshots[path] = meta
		}
	}

	// 2. Detect Removes
	// We only consider removals for paths that fall under the roots we *actually scanned*.
	// This prevents deleting entries for new roots added via Add() during the scan.
	for path, prev := range w.snapshots {
		// If it exists in current scan, it's not removed
		if _, ok := currentSnapshot[path]; ok {
			continue
		}

		// Check if this path belongs to one of the roots we scanned
		// If it does, and it's missing from scan, it must have been deleted.
		if isPathUnderRoots(path, roots) {
			if Remove&w.eventFilter != 0 {
				events = append(events, Event{Path: path, Type: Remove, IsDir: prev.IsDir})
			}
			delete(w.snapshots, path)
		}
	}

	if len(events) == 0 {
		return
	}

	w.pendingEvents = append(w.pendingEvents, events...)

	w.debouncer.Trigger(func() {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		toDeliver := w.pendingEvents
		w.pendingEvents = nil
		w.mu.Unlock()

		if len(toDeliver) > 0 && w.handler != nil {
			w.handler(toDeliver)
		}
	})
}

// isWatched checks if path is covered by any currently watched path.
// Must be called with w.mu held.
func (w *Watcher) isWatched(path string) bool {
	// Exact match
	if w.watchedPaths[path] {
		return true
	}

	// Check if parent directory is watched (for non-recursive watching of files in a dir)
	dir := filepath.Dir(path)
	if w.watchedPaths[dir] {
		return true
	}

	// Recursive check
	if w.recursive {
		for root := range w.watchedPaths {
			if strings.HasPrefix(path, root+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
}

// isPathUnderRoots checks if path is under any of the provided roots.
func isPathUnderRoots(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// snapshotPath seeds the snapshot map for a newly added path in polling mode.
func (w *Watcher) snapshotPath(path string, info os.FileInfo) error {
	entries, err := entriesForPath(path, info, w.recursive, w.isIgnored)
	if err != nil {
		return err
	}
	for p, meta := range entries {
		w.snapshots[p] = meta
	}
	return nil
}

// snapshotEntries returns the current metadata for the watched root (and children if recursive).
func snapshotEntries(root string, recursive bool, isIgnored func(string) bool) (map[string]fileMeta, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	return entriesForPath(root, info, recursive, isIgnored)
}

func entriesForPath(root string, info os.FileInfo, recursive bool, isIgnored func(string) bool) (map[string]fileMeta, error) {
	entries := make(map[string]fileMeta)

	add := func(path string, fi os.FileInfo) {
		entries[path] = fileMeta{
			ModTime: fi.ModTime(),
			Size:    fi.Size(),
			Mode:    fi.Mode(),
			IsDir:   fi.IsDir(),
		}
	}

	// Don't add if root itself is ignored (though unlikely if called explicitly)
	if isIgnored != nil && isIgnored(root) {
		return entries, nil
	}

	add(root, info)

	if !info.IsDir() {
		return entries, nil
	}

	if recursive {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			if isIgnored != nil && isIgnored(path) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			fi, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			add(path, fi)
			return nil
		})
		if err != nil {
			return nil, err
		}
		return entries, nil
	}

	// Non-recursive: include immediate children
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, d := range dirEntries {
		path := filepath.Join(root, d.Name())
		if isIgnored != nil && isIgnored(path) {
			continue
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return nil, statErr
		}
		add(path, fi)
	}
	return entries, nil
}
