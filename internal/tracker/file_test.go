package tracker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotDirectory(t *testing.T) {
	// Create temp dir
	tmpDir, err := os.MkdirTemp("", "tracker_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create some files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	ignored := filepath.Join(tmpDir, ".git", "HEAD")

	if err := os.WriteFile(file1, []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(ignored), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignored, []byte("ref: refs/heads/main"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultSnapshotOptions(tmpDir)
	snapshot, err := SnapshotDirectory(tmpDir, opts)
	if err != nil {
		t.Fatalf("SnapshotDirectory failed: %v", err)
	}

	if len(snapshot) != 2 {
		t.Errorf("expected 2 files in snapshot, got %d", len(snapshot))
	}
	if _, ok := snapshot[file1]; !ok {
		t.Errorf("file1 not found in snapshot")
	}
	if _, ok := snapshot[file2]; !ok {
		t.Errorf("file2 not found in snapshot")
	}
	if _, ok := snapshot[ignored]; ok {
		t.Errorf("ignored file found in snapshot")
	}
}

func TestDetectFileChanges(t *testing.T) {
	// Setup timestamps
	now := time.Now()
	old := now.Add(-1 * time.Hour)

	before := map[string]FileState{
		"/path/to/unchanged": {ModTime: old, Size: 100},
		"/path/to/modified":  {ModTime: old, Size: 100},
		"/path/to/deleted":   {ModTime: old, Size: 100},
	}

	after := map[string]FileState{
		"/path/to/unchanged": {ModTime: old, Size: 100},
		"/path/to/modified":  {ModTime: now, Size: 105}, // Changed
		"/path/to/added":     {ModTime: now, Size: 50},  // New
	}

	changes := DetectFileChanges(before, after)

	if len(changes) != 3 {
		t.Errorf("expected 3 changes, got %d", len(changes))
	}

	changeMap := make(map[string]FileChangeType)
	for _, c := range changes {
		changeMap[c.Path] = c.Type
	}

	if changeMap["/path/to/modified"] != FileModified {
		t.Errorf("expected modified, got %v", changeMap["/path/to/modified"])
	}
	if changeMap["/path/to/added"] != FileAdded {
		t.Errorf("expected added, got %v", changeMap["/path/to/added"])
	}
	if changeMap["/path/to/deleted"] != FileDeleted {
		t.Errorf("expected deleted, got %v", changeMap["/path/to/deleted"])
	}
	if _, ok := changeMap["/path/to/unchanged"]; ok {
		t.Errorf("unchanged file shouldn't be in changes")
	}
}

func TestFileChangeStore(t *testing.T) {
	store := NewFileChangeStore(2)

	store.Add(RecordedFileChange{Session: "s1"})
	store.Add(RecordedFileChange{Session: "s2"})
	store.Add(RecordedFileChange{Session: "s3"}) // Should drop s1

	all := store.All()
	if len(all) != 2 {
		t.Errorf("expected 2 entries (limit), got %d", len(all))
	}
	if all[0].Session != "s2" {
		t.Errorf("expected oldest to be s2, got %s", all[0].Session)
	}
	if all[1].Session != "s3" {
		t.Errorf("expected newest to be s3, got %s", all[1].Session)
	}
}
