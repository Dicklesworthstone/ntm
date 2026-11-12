package ensemble

import (
	"testing"
	"time"
)

func TestTimebox_Ordering(t *testing.T) {
	start := time.Now()
	input := map[string]any{"start": start, "total": time.Second}
	logTestStartTimebox(t, input)

	deadline := timeboxDeadline(start, time.Second)
	logTestResultTimebox(t, deadline)

	assertTrueTimebox(t, "deadline after start", deadline.After(start))
}

func TestTimebox_Cutoff(t *testing.T) {
	deadline := time.Now().Add(-1 * time.Second)
	input := map[string]any{"deadline": deadline}
	logTestStartTimebox(t, input)

	expired := timeboxExpired(deadline, time.Now())
	logTestResultTimebox(t, expired)

	assertTrueTimebox(t, "expired true", expired)
}

func TestTimebox_PartialSynthesis(t *testing.T) {
	assignments := []ModeAssignment{
		{ModeID: "mode-a", Status: AssignmentPending},
		{ModeID: "mode-b", Status: AssignmentPending},
	}
	input := map[string]any{"assignments": assignments}
	logTestStartTimebox(t, input)

	skipped := markAssignmentsSkipped(assignments, []int{1}, "timebox reached")
	logTestResultTimebox(t, skipped)

	assertEqualTimebox(t, "skipped count", len(skipped), 1)
	assertEqualTimebox(t, "status updated", assignments[1].Status, AssignmentError)
}

func logTestStartTimebox(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultTimebox(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertTrueTimebox(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualTimebox(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}
