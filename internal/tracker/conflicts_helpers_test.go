package tracker

import "time"

// Test-only helper moved out of conflicts.go for the G1 dead-code gate.
//
// ConflictsForProject superseded this when the work-coordination adapter — its
// only production caller — started passing the project it was configured with
// rather than resolving from the working directory, which is correct for a
// component that runs wherever the caller happens to be. The cwd-resolving form
// keeps its tests here without leaving unreachable code in the shipped binary.

// DetectConflictsRecent analyzes recorded file changes within the given window,
// for the project the caller is standing in.
func DetectConflictsRecent(window time.Duration) []Conflict {
	return DetectConflicts(RecordedChangesSince(time.Now().Add(-window)))
}
