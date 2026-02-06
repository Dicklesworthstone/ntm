package assignment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSave_Explicit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("save-test")
	store.Assign("bd-1", "Bead one", 1, "cc", "Agent1", "Do thing")

	// Explicit Save call (separate from auto-save in Assign)
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(store.path); err != nil {
		t.Fatalf("save file not found: %v", err)
	}

	// Load and verify
	store2, err := LoadStore("save-test")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	a := store2.Get("bd-1")
	if a == nil {
		t.Fatal("expected bd-1 after Save+Load")
	}
	if a.BeadTitle != "Bead one" {
		t.Errorf("BeadTitle = %q, want 'Bead one'", a.BeadTitle)
	}
}

func TestGetAll(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("getall-test")
	store.Assign("bd-a", "Alpha", 1, "cc", "Agent1", "Prompt1")
	store.Assign("bd-b", "Bravo", 2, "cod", "Agent2", "Prompt2")
	store.Assign("bd-c", "Charlie", 3, "gmi", "Agent3", "Prompt3")

	all := store.GetAll()
	if len(all) != 3 {
		t.Fatalf("GetAll len = %d, want 3", len(all))
	}

	// Verify returned as values (not pointers)
	ids := map[string]bool{}
	for _, a := range all {
		ids[a.BeadID] = true
	}
	for _, id := range []string{"bd-a", "bd-b", "bd-c"} {
		if !ids[id] {
			t.Errorf("missing %s in GetAll results", id)
		}
	}
}

func TestGetAll_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("empty-getall")
	all := store.GetAll()
	if len(all) != 0 {
		t.Errorf("GetAll len = %d, want 0 for empty store", len(all))
	}
}

// TestIsValidTransition_UnknownSource tests the !ok branch (invalid source status).
func TestIsValidTransition_UnknownSource(t *testing.T) {
	t.Parallel()

	got := isValidTransition(AssignmentStatus("nonexistent"), StatusWorking)
	if got {
		t.Error("isValidTransition with unknown source should return false")
	}
}

// TestIsValidTransition_TerminalStates tests that terminal states have no valid transitions.
func TestIsValidTransition_TerminalStates(t *testing.T) {
	t.Parallel()

	terminals := []AssignmentStatus{StatusCompleted, StatusReassigned}
	targets := []AssignmentStatus{StatusAssigned, StatusWorking, StatusCompleted, StatusFailed, StatusReassigned}

	for _, from := range terminals {
		for _, to := range targets {
			if isValidTransition(from, to) {
				t.Errorf("isValidTransition(%s, %s) = true, want false (terminal state)", from, to)
			}
		}
	}
}

func TestStorageDir_Fallback(t *testing.T) {
	// StorageDir has a fallback to TempDir when NTMDir fails.
	// When HOME is set, it should return a sessions path under ~/.ntm/sessions
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	dir := StorageDir()
	want := filepath.Join(tmpDir, ".ntm", "sessions")
	if dir != want {
		t.Errorf("StorageDir() = %q, want %q", dir, want)
	}
}
