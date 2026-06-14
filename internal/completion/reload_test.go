package completion

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// Regression: the completion detector is constructed with one store instance,
// but assignments are recorded by a SEPARATE store instance in the dispatch path
// (executeAssignmentsEnhanced calls assignment.LoadStore itself). If the detector
// never reloads, it never observes those assignments, never marks their beads
// completed, and never releases their panes — so the autonomous watch loop dies
// after at most (pane count) dispatches. checkAll must reload the store from disk
// each poll. This test verifies the reload makes externally-written assignments
// visible to the detector's store (the mechanism the fix relies on).
func TestDetectorStoreReloadSeesExternalAssignments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "reload-test-session"

	// The detector's store, as handed in at construction — empty at watch start.
	detectorStore := assignment.NewStore(session)
	if err := detectorStore.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	d := New(session, detectorStore)

	// A SEPARATE store instance (as the dispatch path uses) records an assignment.
	dispatchStore, err := assignment.LoadStore(session)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if _, err := dispatchStore.Assign("bd-x", "Bead X", 2, "claude", "agent2", "prompt"); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// Before reload the detector's store is blind to it — that was the bug.
	if got := len(d.Store.ListActive()); got != 0 {
		t.Fatalf("pre-reload: detector store unexpectedly sees %d active (want 0)", got)
	}

	// The reload checkAll performs each poll must make it visible.
	if err := d.Store.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	active := d.Store.ListActive()
	if len(active) != 1 || active[0].BeadID != "bd-x" {
		t.Fatalf("post-reload: detector store sees %d active (want 1: bd-x)", len(active))
	}
}
