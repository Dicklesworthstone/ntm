package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// bd-68vr1: pre-fix runChanges used a non-stable sort.Slice on LastAt
// alone. When multiple conflicts shared a LastAt timestamp (realistic
// for batched commits or scripted changes), the relative Path order
// from bd-rfzj1's upstream sort was destroyed and replaced with
// algorithm-specific non-deterministic order — visible in the --json
// output of `ntm changes`. The fix adds an explicit Path tiebreaker.
//
// This test pins the deterministic order across many runs and
// asserts the post-fix Path tiebreak is correct for tied LastAt rows.
func TestSortConflictsByLastAtThenPath_DeterministicWithTies(t *testing.T) {
	t.Parallel()

	// Three conflicts sharing one LastAt timestamp, plus one strictly
	// newer. Newest must come first; the three tied rows must order
	// alphabetically by Path.
	tied := time.Date(2026, 5, 9, 4, 30, 0, 0, time.UTC)
	newest := tied.Add(time.Hour)

	build := func() []tracker.Conflict {
		// Construct in a deliberately unsorted order (mimicking the
		// upstream's Path-sorted feed mixed with a newer entry).
		return []tracker.Conflict{
			{Path: "/src/zzz.go", LastAt: tied, Severity: "warning"},
			{Path: "/src/aaa.go", LastAt: tied, Severity: "warning"},
			{Path: "/src/mmm.go", LastAt: tied, Severity: "warning"},
			{Path: "/x/y.go", LastAt: newest, Severity: "warning"},
		}
	}

	first := build()
	sortConflictsByLastAtThenPath(first)

	wantOrder := []string{"/x/y.go", "/src/aaa.go", "/src/mmm.go", "/src/zzz.go"}
	if len(first) != len(wantOrder) {
		t.Fatalf("len(conflicts) = %d, want %d", len(first), len(wantOrder))
	}
	for i, want := range wantOrder {
		if first[i].Path != want {
			t.Errorf("conflicts[%d].Path = %q, want %q (newest first then Path-tiebreak)",
				i, first[i].Path, want)
		}
	}

	// Repeat to flush out any non-determinism. Pre-fix this would
	// occasionally reorder the three tied rows; post-fix Path tiebreak
	// pins them.
	for iter := 0; iter < 30; iter++ {
		got := build()
		sortConflictsByLastAtThenPath(got)
		for i := range got {
			if got[i].Path != first[i].Path {
				t.Errorf("iter %d: conflicts[%d].Path = %q, want %q (non-deterministic for tied LastAt)",
					iter, i, got[i].Path, first[i].Path)
			}
		}
	}
}
