package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// bd-e1v97: `ntm changes --json` and `ntm conflicts --json` must emit []
// for empty results, never the bare token `null` (arrays-never-null
// convention). These CLI paths do not route through
// robot.EnsureArraysNeverNull, so the slices are initialized at the source.

func setupEmptyTrackerJSON(t *testing.T) {
	t.Helper()

	origStore := tracker.GlobalFileChanges
	tracker.GlobalFileChanges = tracker.NewFileChangeStore(100)
	t.Cleanup(func() { tracker.GlobalFileChanges = origStore })

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
}

func TestChangesJSONEmptyIsArray(t *testing.T) {
	setupEmptyTrackerJSON(t)

	out, err := captureStdout(t, func() error { return runChanges(t.Context(), "") })
	if err != nil {
		t.Fatalf("runChanges() error = %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Fatalf("empty changes --json output = %q, want []", got)
	}
}

func TestConflictsJSONEmptyIsArray(t *testing.T) {
	setupEmptyTrackerJSON(t)

	out, err := captureStdout(t, func() error { return runConflicts(t.Context(), "", "24h", 50) })
	if err != nil {
		t.Fatalf("runConflicts() error = %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Fatalf("empty conflicts --json output = %q, want []", got)
	}
}
