package metrics

import (
	"os"
	"testing"
)

// TestMain stubs the conflict source for the whole package.
//
// GenerateReport counts file conflicts through tracker.ConflictsSince, which
// opens the shared state.db at ~/.config/ntm/state.db. A test run must never
// read the developer's real database — and a test asserting "0 conflicts" would
// otherwise pass or fail depending on what their agents happened to be doing.
func TestMain(m *testing.M) {
	countFileConflicts = func(string) int64 { return 0 }
	os.Exit(m.Run())
}

// stubFileConflicts makes the conflict source return n for one test.
func stubFileConflicts(t *testing.T, n int64) {
	t.Helper()
	previous := countFileConflicts
	countFileConflicts = func(string) int64 { return n }
	t.Cleanup(func() { countFileConflicts = previous })
}

// The metric must follow the tracker rather than a counter of its own. Nothing
// ever incremented the old c.fileConflicts field, so file_conflicts reported 0
// — and its Tier-0 target reported a green "met" — for every session ever run.
func TestGenerateReportCountsConflictsFromTracker(t *testing.T) {
	stubFileConflicts(t, 3)

	c := NewCollector(nil, "sess")
	t.Cleanup(c.Close)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.FileConflicts != 3 {
		t.Errorf("FileConflicts = %d, want 3", report.FileConflicts)
	}
}

// The Tier-0 target has to be able to fail, which is the whole point of a target.
func TestFileConflictTargetCanFail(t *testing.T) {
	stubFileConflicts(t, 4)

	c := NewCollector(nil, "sess")
	t.Cleanup(c.Close)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	var found bool
	for _, tc := range report.TargetComparison {
		if tc.Metric != "file_conflicts" {
			continue
		}
		found = true
		if tc.Current != 4 {
			t.Errorf("file_conflicts current = %.1f, want 4", tc.Current)
		}
		if tc.Status == "met" {
			t.Errorf("4 conflicts against a target of %.1f reported status %q", tc.Target, tc.Status)
		}
	}
	if !found {
		t.Error("no file_conflicts target comparison was emitted")
	}
}

func TestFileConflictTargetMetWhenClean(t *testing.T) {
	stubFileConflicts(t, 0)

	c := NewCollector(nil, "sess")
	t.Cleanup(c.Close)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	for _, tc := range report.TargetComparison {
		if tc.Metric == "file_conflicts" && tc.Status != "met" {
			t.Errorf("0 conflicts reported status %q, want met", tc.Status)
		}
	}
}

// The count is scoped to the collector's session so a per-session report does
// not inherit another session's conflicts.
func TestGenerateReportPassesSessionToConflictSource(t *testing.T) {
	previous := countFileConflicts
	var gotSession string
	countFileConflicts = func(session string) int64 {
		gotSession = session
		return 0
	}
	t.Cleanup(func() { countFileConflicts = previous })

	c := NewCollector(nil, "my-session")
	t.Cleanup(c.Close)
	if _, err := c.GenerateReport(); err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if gotSession != "my-session" {
		t.Errorf("conflict source received session %q, want %q", gotSession, "my-session")
	}
}
