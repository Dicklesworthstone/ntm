package completion

import (
	"testing"
	"time"

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

// An idle/stalled assignment whose bead never closed must be reported as FAILED
// (so the pane is released and the bead reassigned), not as a success completion.
// A false success would silently drop the work and — since completed beads are
// suppressed from re-dispatch — strand it. Regression for the Haiku crash test.
func TestIdleTimeoutReportsFailure(t *testing.T) {
	store := assignment.NewStore("idle-fail-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("idle-fail-session", store, cfg)

	now := time.Now()
	a := &assignment.Assignment{BeadID: "bd-stall", Pane: 0, AgentType: "claude", AssignedAt: now}

	_ = d.checkIdle(a, "first", now)  // init
	_ = d.checkIdle(a, "second", now) // start burst (output changed)
	time.Sleep(15 * time.Millisecond)
	ev := d.checkIdle(a, "second", now) // unchanged past threshold
	if ev == nil {
		t.Fatal("expected an idle event after threshold")
	}
	if !ev.IsFailed {
		t.Errorf("idle-timeout event IsFailed=false; want true (stalled agent must fail+reassign, not falsely complete)")
	}
}

// The completion patterns match the DISPATCH PROMPT'S OWN TEXT (it instructs the
// agent to run `br close <id>`). This documents why a pattern match alone must
// NOT be treated as success without an actual bead-closed re-check: otherwise a
// crashed agent whose pane still shows the prompt is falsely marked completed.
func TestCompletionPatternMatchesPromptEcho(t *testing.T) {
	d := New("echo-session", nil)
	prompt := `You are a STRESS-TEST agent. Process bead bd-x by running ` +
		"`br close bd-x --reason ok`" + ` then STOP.`
	if !d.matchCompletionPatterns(prompt) {
		t.Fatal("expected the dispatch prompt to match a completion pattern (the hazard the bead-closed gate guards against)")
	}
}
