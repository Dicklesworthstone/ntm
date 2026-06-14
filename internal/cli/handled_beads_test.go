package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// loadHandledBeadIDs suppresses both active and recently-completed beads, so a
// fast bead that just closed is not re-dispatched (the double-dispatch race a
// bv open-check can miss under br lock contention). An old completion ages out.
func TestLoadHandledBeadIDs_RecentlyCompleted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore("handled-test")
	if _, err := store.Assign("bead-active", "Active", 2, "claude", "A2", "p"); err != nil {
		t.Fatalf("Assign active: %v", err)
	}
	if _, err := store.Assign("bead-fresh", "Fresh", 3, "claude", "A3", "p"); err != nil {
		t.Fatalf("Assign fresh: %v", err)
	}
	if err := store.MarkCompleted("bead-fresh"); err != nil {
		t.Fatalf("complete fresh: %v", err)
	}

	now := time.Now()
	handled := loadHandledBeadIDs("handled-test", 90*time.Second, now)
	if _, ok := handled["bead-active"]; !ok {
		t.Error("active bead missing from handled set — guard would allow double-dispatch")
	}
	if _, ok := handled["bead-fresh"]; !ok {
		t.Error("recently-completed bead missing — guard would allow double-dispatch of a just-closed bead")
	}
	aged := loadHandledBeadIDs("handled-test", 0, now.Add(time.Second))
	if _, ok := aged["bead-fresh"]; ok {
		t.Error("completed bead did not age out of the handled set")
	}
	if _, ok := aged["bead-active"]; !ok {
		t.Error("active bead must remain handled regardless of window")
	}
}
