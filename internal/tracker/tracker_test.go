package tracker

import (
	"testing"
	"time"
)

// populateStore seeds a FileChangeStore directly. The production write path
// (FileChangeStore.Add) was removed as dead code; tests of the live read
// paths seed the ring buffer state in-package instead.
func populateStore(s *FileChangeStore, entries ...RecordedFileChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
}

func TestFileChangeStore_Since(t *testing.T) {
	store := NewFileChangeStore(10)

	now := time.Now()
	populateStore(store,
		RecordedFileChange{Timestamp: now.Add(-3 * time.Second), Session: "s1"},
		RecordedFileChange{Timestamp: now.Add(-2 * time.Second), Session: "s2"},
		RecordedFileChange{Timestamp: now.Add(-1 * time.Second), Session: "s3"},
	)

	changes := store.Since(now.Add(-2500 * time.Millisecond))
	if len(changes) != 2 {
		t.Errorf("expected 2 changes since -2.5s, got %d", len(changes))
	}
	if len(changes) >= 2 {
		if changes[0].Session != "s2" || changes[1].Session != "s3" {
			t.Errorf("got sessions %s, %s; want s2, s3", changes[0].Session, changes[1].Session)
		}
	}
}

func TestFileChangeStore_Since_Empty(t *testing.T) {
	store := NewFileChangeStore(10)
	changes := store.Since(time.Now().Add(-1 * time.Hour))
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for empty store, got %d", len(changes))
	}
}

func TestFileChangeStore_Since_Wrapped(t *testing.T) {
	store := NewFileChangeStore(3)

	now := time.Now()
	// Simulate a wrapped ring buffer: s1 was overwritten by s4, so the
	// physical layout is [s4, s2, s3] with the oldest entry at cursor 1.
	populateStore(store,
		RecordedFileChange{Timestamp: now.Add(-1 * time.Second), Session: "s4"},
		RecordedFileChange{Timestamp: now.Add(-3 * time.Second), Session: "s2"},
		RecordedFileChange{Timestamp: now.Add(-2 * time.Second), Session: "s3"},
	)
	store.mu.Lock()
	store.full = true
	store.cursor = 1
	store.mu.Unlock()

	changes := store.Since(now.Add(-5 * time.Second))
	if len(changes) != 3 {
		t.Errorf("expected 3 changes after wrap, got %d", len(changes))
	}
}

func TestFileChangeStore_All_NotWrapped(t *testing.T) {
	store := NewFileChangeStore(10)
	populateStore(store,
		RecordedFileChange{Timestamp: time.Now(), Session: "s1"},
		RecordedFileChange{Timestamp: time.Now(), Session: "s2"},
	)

	all := store.All()
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
	if all[0].Session != "s1" || all[1].Session != "s2" {
		t.Error("order should be preserved")
	}
}
