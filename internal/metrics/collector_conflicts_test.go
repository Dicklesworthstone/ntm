package metrics

import (
	"os"
	"sync"
	"testing"
)

var (
	sessionFileConflictsMu sync.RWMutex
	sessionFileConflicts   = map[string]int64{}
)

// TestMain stubs the conflict source for the whole package.
//
// GenerateReport counts file conflicts through tracker.ConflictsSince, which
// opens the shared state.db at ~/.config/ntm/state.db. A test run must never
// read the developer's real database — and a test asserting "0 conflicts" would
// otherwise pass or fail depending on what their agents happened to be doing.
func TestMain(m *testing.M) {
	fileConflictsMu.Lock()
	countFileConflicts = func(session string) int64 {
		sessionFileConflictsMu.RLock()
		n := sessionFileConflicts[session]
		sessionFileConflictsMu.RUnlock()
		return n
	}
	fileConflictsMu.Unlock()
	os.Exit(m.Run())
}

// stubFileConflicts makes the conflict source return n for one non-parallel test.
func stubFileConflicts(t *testing.T, n int64) {
	t.Helper()
	fileConflictsMu.Lock()
	previous := countFileConflicts
	countFileConflicts = func(string) int64 { return n }
	fileConflictsMu.Unlock()
	t.Cleanup(func() {
		fileConflictsMu.Lock()
		countFileConflicts = previous
		fileConflictsMu.Unlock()
	})
}

// stubFileConflictsForSession scopes a conflict stub to one session so parallel
// tests never clobber each other's global countFileConflicts handler.
func stubFileConflictsForSession(t *testing.T, session string, n int64) {
	t.Helper()
	sessionFileConflictsMu.Lock()
	prev, hadPrev := sessionFileConflicts[session]
	sessionFileConflicts[session] = n
	sessionFileConflictsMu.Unlock()
	t.Cleanup(func() {
		sessionFileConflictsMu.Lock()
		if hadPrev {
			sessionFileConflicts[session] = prev
		} else {
			delete(sessionFileConflicts, session)
		}
		sessionFileConflictsMu.Unlock()
	})
}

// The metric must follow the tracker rather than a counter of its own. Nothing
// ever incremented the old c.fileConflicts field, so file_conflicts reported 0
// — and its Tier-0 target reported a green "met" — for every session ever run.
func TestGenerateReportCountsConflictsFromTracker(t *testing.T) {
	stubFileConflicts(t, 3)

	c := NewCollector(nil, "sess")

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
	fileConflictsMu.Lock()
	previous := countFileConflicts
	var gotSession string
	countFileConflicts = func(session string) int64 {
		gotSession = session
		return 0
	}
	fileConflictsMu.Unlock()
	t.Cleanup(func() {
		fileConflictsMu.Lock()
		countFileConflicts = previous
		fileConflictsMu.Unlock()
	})

	c := NewCollector(nil, "my-session")
	if _, err := c.GenerateReport(); err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if gotSession != "my-session" {
		t.Errorf("conflict source received session %q, want %q", gotSession, "my-session")
	}
}
