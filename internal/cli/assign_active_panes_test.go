package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// A pane holding an active assignment must appear in the active-pane set so the
// idle-collection paths exclude it from new dispatch (the between-turns race fix).
func TestLoadActiveAssignmentPanes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	store := assignment.NewStore("race-test")
	if _, err := store.Assign("bead-A", "Some title", 3, "claude", "Agent", "prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}

	active := loadActiveAssignmentPanes("race-test")
	if _, ok := active[3]; !ok {
		t.Errorf("pane 3 (active assignment) missing from active set: %v", active)
	}
	if _, ok := active[5]; ok {
		t.Errorf("pane 5 (no assignment) wrongly present in active set")
	}
}

// An empty/missing store must yield an empty set (no panes excluded) so a fresh
// session with no assignments dispatches normally.
func TestLoadActiveAssignmentPanes_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if active := loadActiveAssignmentPanes("nonexistent-session"); len(active) != 0 {
		t.Errorf("expected empty active set for fresh session, got %v", active)
	}
}
