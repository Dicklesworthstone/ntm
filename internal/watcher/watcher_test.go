package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestNewWatcher(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Either fsWatcher is set (fsnotify mode) or pollMode is enabled (fallback)
	if w.fsWatcher == nil && !w.pollMode {
		t.Error("fsWatcher should not be nil when not in poll mode")
	}
	if w.debouncer == nil {
		t.Error("debouncer should not be nil")
	}
	if w.eventFilter != All {
		t.Errorf("eventFilter = %v, want %v", w.eventFilter, All)
	}
}

func TestWatcherWithOptions(t *testing.T) {
	debouncer := NewDebouncer(500 * time.Millisecond)
	errorHandler := func(err error) {
		// Error handler for testing
	}

	w, err := New(
		func(events []Event) {},
		WithDebouncer(debouncer),
		WithEventFilter(Create|Write),
		WithRecursive(true),
		WithErrorHandler(errorHandler),
	)
	if err != nil {
		t.Fatalf("New() with options failed: %v", err)
	}
	defer w.Close()

	if w.debouncer != debouncer {
		t.Error("debouncer not set correctly")
	}
	if w.eventFilter != Create|Write {
		t.Errorf("eventFilter = %v, want %v", w.eventFilter, Create|Write)
	}
	if !w.recursive {
		t.Error("recursive should be true")
	}
	if w.errorHandler == nil {
		t.Error("errorHandler should not be nil")
	}
}

func TestWatcherAddRemove(t *testing.T) {
	tmpDir := t.TempDir()

	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Add directory
	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	paths := w.WatchedPaths()
	if len(paths) != 1 {
		t.Errorf("WatchedPaths() = %v, want 1 path", paths)
	}

	// Add again should be no-op
	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() again failed: %v", err)
	}
	paths = w.WatchedPaths()
	if len(paths) != 1 {
		t.Errorf("WatchedPaths() after duplicate add = %v, want 1 path", paths)
	}

	// Remove
	if err := w.Remove(tmpDir); err != nil {
		t.Fatalf("Remove() failed: %v", err)
	}

	paths = w.WatchedPaths()
	if len(paths) != 0 {
		t.Errorf("WatchedPaths() after remove = %v, want 0 paths", paths)
	}
}

func TestWatcherRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	w, err := New(
		func(events []Event) {},
		WithRecursive(true),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	paths := w.WatchedPaths()
	if len(paths) != 2 {
		t.Errorf("WatchedPaths() = %v, want 2 paths (root + subdir)", paths)
	}
}

func TestWatcherRecursiveRemove(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	nestedFile := filepath.Join(subDir, "file.txt")
	if err := os.WriteFile(nestedFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "fsnotify-or-fallback",
			opts: []Option{WithRecursive(true)},
		},
		{
			name: "polling-forced",
			opts: []Option{WithRecursive(true), WithPolling(true)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := New(func(events []Event) {}, tt.opts...)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}
			defer w.Close()

			if err := w.Add(tmpDir); err != nil {
				t.Fatalf("Add() failed: %v", err)
			}

			paths := w.WatchedPaths()
			if len(paths) != 2 {
				t.Fatalf("WatchedPaths() = %v, want 2 paths (root + subdir)", paths)
			}

			if err := w.Remove(tmpDir); err != nil {
				t.Fatalf("Remove() failed: %v", err)
			}

			paths = w.WatchedPaths()
			if len(paths) != 0 {
				t.Fatalf("WatchedPaths() after remove = %v, want 0 paths", paths)
			}

			if w.pollMode {
				for p := range w.snapshots {
					if strings.HasPrefix(p, tmpDir+string(os.PathSeparator)) || p == tmpDir {
						t.Fatalf("expected poll snapshot cleanup, found %q", p)
					}
				}
			}
		})
	}
}

func TestWatcherEvents(t *testing.T) {
	tmpDir := t.TempDir()

	var mu sync.Mutex
	var receivedEvents []Event
	eventReceived := make(chan struct{}, 10)

	w, err := New(
		func(events []Event) {
			mu.Lock()
			receivedEvents = append(receivedEvents, events...)
			mu.Unlock()
			select {
			case eventReceived <- struct{}{}:
			default:
			}
		},
		WithDebouncer(NewDebouncer(50*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	// Create a file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Wait for event
	select {
	case <-eventReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(receivedEvents) == 0 {
		t.Error("expected at least one event")
		return
	}

	// Check that we got a create event
	found := false
	for _, e := range receivedEvents {
		if filepath.Base(e.Path) == "test.txt" && e.Type&Create != 0 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Create event for test.txt, got %+v", receivedEvents)
	}
}

func TestWatcherEventFilter(t *testing.T) {
	tmpDir := t.TempDir()

	var mu sync.Mutex
	var receivedEvents []Event
	eventReceived := make(chan struct{}, 10)

	// Only watch for Write events
	w, err := New(
		func(events []Event) {
			mu.Lock()
			receivedEvents = append(receivedEvents, events...)
			mu.Unlock()
			select {
			case eventReceived <- struct{}{}:
			default:
			}
		},
		WithEventFilter(Write),
		WithDebouncer(NewDebouncer(50*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	// Create a file (should be filtered out)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Give it a moment for Create event to (not) be processed
	time.Sleep(200 * time.Millisecond)

	// Modify the file (should trigger Write event)
	if err := os.WriteFile(testFile, []byte("world"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Wait for event
	select {
	case <-eventReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()

	// Check that we only got Write events
	for _, e := range receivedEvents {
		if e.Type&Write == 0 {
			t.Errorf("expected only Write events, got %+v", e)
		}
	}
}

func TestWatcherClose(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Close should succeed
	if err := w.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Close again should be no-op
	if err := w.Close(); err != nil {
		t.Fatalf("Close() again failed: %v", err)
	}

	// Operations after close should return ErrClosed
	tmpDir := t.TempDir()
	if err := w.Add(tmpDir); err != ErrClosed {
		t.Errorf("Add() after close = %v, want %v", err, ErrClosed)
	}
	if err := w.Remove(tmpDir); err != ErrClosed {
		t.Errorf("Remove() after close = %v, want %v", err, ErrClosed)
	}
}

func TestEventType(t *testing.T) {
	tests := []struct {
		name       string
		eventType  EventType
		wantCreate bool
		wantWrite  bool
		wantRemove bool
	}{
		{"Create", Create, true, false, false},
		{"Write", Write, false, true, false},
		{"Remove", Remove, false, false, true},
		{"Create|Write", Create | Write, true, true, false},
		{"All", All, true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.eventType&Create != 0; got != tt.wantCreate {
				t.Errorf("Create = %v, want %v", got, tt.wantCreate)
			}
			if got := tt.eventType&Write != 0; got != tt.wantWrite {
				t.Errorf("Write = %v, want %v", got, tt.wantWrite)
			}
			if got := tt.eventType&Remove != 0; got != tt.wantRemove {
				t.Errorf("Remove = %v, want %v", got, tt.wantRemove)
			}
		})
	}
}

func TestWatcherNonExistentPath(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Try to add non-existent path
	err = w.Add("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("Add() for non-existent path should fail")
	}
}

func TestWatcherPollingCreateWriteRemove(t *testing.T) {
	tmpDir := t.TempDir()

	var mu sync.Mutex
	var received []Event

	w, err := New(
		func(events []Event) {
			mu.Lock()
			received = append(received, events...)
			mu.Unlock()
		},
		WithPolling(true),
		WithPollInterval(20*time.Millisecond),
		WithDebouncer(NewDebouncer(10*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	testFile := filepath.Join(tmpDir, "poll.txt")

	waitFor := func(cond func() bool, msg string) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", msg)
	}

	contains := func(path string, typ EventType) bool {
		for _, e := range received {
			if e.Path == path && e.Type&typ != 0 {
				return true
			}
		}
		return false
	}

	// Create
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	waitFor(func() bool { return contains(testFile, Create) }, "create event")

	// Modify
	if err := os.WriteFile(testFile, []byte("world"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	waitFor(func() bool { return contains(testFile, Write) }, "write event")

	// Remove
	if err := os.Remove(testFile); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	waitFor(func() bool { return contains(testFile, Remove) }, "remove event")
}

func TestWatcherPollingRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	var mu sync.Mutex
	var received []Event

	w, err := New(
		func(events []Event) {
			mu.Lock()
			received = append(received, events...)
			mu.Unlock()
		},
		WithPolling(true),
		WithRecursive(true),
		WithPollInterval(20*time.Millisecond),
		WithDebouncer(NewDebouncer(10*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	testFile := filepath.Join(subDir, "nested.txt")
	if err := os.WriteFile(testFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	waitFor := func(cond func() bool, msg string) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", msg)
	}

	waitFor(func() bool {
		for _, e := range received {
			if e.Path == testFile && e.Type&Create != 0 {
				return true
			}
		}
		return false
	}, "recursive create")
}

func TestEventTypeFromFsnotify(t *testing.T) {
	tests := []struct {
		op       fsnotify.Op
		expected EventType
	}{
		{fsnotify.Create, Create},
		{fsnotify.Write, Write},
		{fsnotify.Remove, Remove},
		{fsnotify.Rename, Rename},
		{fsnotify.Chmod, Chmod},
		{fsnotify.Create | fsnotify.Write, Create | Write},
	}

	for _, tt := range tests {
		got := eventTypeFromFsnotify(tt.op)
		if got != tt.expected {
			t.Errorf("eventTypeFromFsnotify(%v) = %v, want %v", tt.op, got, tt.expected)
		}
	}
}

func TestWatcherIgnorePaths(t *testing.T) {
	tmpDir := t.TempDir()
	ignoredDir := filepath.Join(tmpDir, "ignored")
	if err := os.Mkdir(ignoredDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	var mu sync.Mutex
	var received []Event
	eventReceived := make(chan struct{}, 10)

	w, err := New(
		func(events []Event) {
			mu.Lock()
			received = append(received, events...)
			mu.Unlock()
			select {
			case eventReceived <- struct{}{}:
			default:
			}
		},
		WithRecursive(true),
		WithIgnorePaths([]string{"ignored"}),
		WithDebouncer(NewDebouncer(50*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	// Create file in ignored dir
	ignoredFile := filepath.Join(ignoredDir, "should_be_ignored.txt")
	if err := os.WriteFile(ignoredFile, []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create file in root (should be detected)
	rootFile := filepath.Join(tmpDir, "root.txt")
	if err := os.WriteFile(rootFile, []byte("detected"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Wait for event
	select {
	case <-eventReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()

	for _, e := range received {
		if e.Path == ignoredFile {
			t.Errorf("received event for ignored file: %s", e.Path)
		}
	}
}
