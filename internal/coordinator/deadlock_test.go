package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func deadlockClock() func() time.Time {
	t := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestDetectDeadlocks_EmptyGraph(t *testing.T) {
	t.Parallel()
	r := DetectDeadlocks(nil, DetectDeadlockOptions{Now: deadlockClock()})
	if !r.Success {
		t.Fatal("Success = false")
	}
	if len(r.Cycles) != 0 {
		t.Errorf("Cycles = %d, want 0", len(r.Cycles))
	}
	if r.NodeCount != 0 || r.EdgeCount != 0 {
		t.Errorf("NodeCount=%d EdgeCount=%d, want 0/0", r.NodeCount, r.EdgeCount)
	}
	// Arrays-never-null: the acyclic report must ENCODE with "cycles": [],
	// not "cycles": null — this envelope reaches the robot surface raw.
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"cycles":[]`; !strings.Contains(string(raw), want) {
		t.Errorf("acyclic report JSON %s missing %s", raw, want)
	}
}

func TestDetectDeadlocks_AcyclicChain(t *testing.T) {
	t.Parallel()
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B", Resource: "f1"},
		{Waiter: "B", Holder: "C", Resource: "f2"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 0 {
		t.Errorf("Cycles = %d, want 0 (acyclic)", len(r.Cycles))
	}
	if r.NodeCount != 3 {
		t.Errorf("NodeCount = %d, want 3", r.NodeCount)
	}
}

func TestDetectDeadlocks_SimpleTwoCycle(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 8, 11, 30, 0, 0, time.UTC)
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B", Resource: "fileA", Reason: "waiting for fileA", Since: since},
		{Waiter: "B", Holder: "A", Resource: "fileB", Reason: "waiting for fileB", Since: since.Add(5 * time.Minute)},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1", len(r.Cycles))
	}
	c := r.Cycles[0]
	want := []string{"A", "B"}
	if !equalSlice(c.Participants, want) {
		t.Errorf("Participants = %v, want %v", c.Participants, want)
	}
	if len(c.Resources) != 2 {
		t.Errorf("Resources = %v, want 2 entries", c.Resources)
	}
	if !c.OldestSince.Equal(since) {
		t.Errorf("OldestSince = %v, want %v", c.OldestSince, since)
	}
	if c.Suggestion == "" {
		t.Errorf("Suggestion empty for 2-cycle")
	}
}

func TestDetectDeadlocks_ThreeCycleRotation(t *testing.T) {
	t.Parallel()
	// path A->B->C->A; participants must rotate to start at A.
	edges := []WaitEdge{
		{Waiter: "B", Holder: "C"},
		{Waiter: "C", Holder: "A"},
		{Waiter: "A", Holder: "B"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1", len(r.Cycles))
	}
	want := []string{"A", "B", "C"}
	if !equalSlice(r.Cycles[0].Participants, want) {
		t.Errorf("Participants = %v, want %v (rotated)", r.Cycles[0].Participants, want)
	}
}

func TestDetectDeadlocks_TwoDisjointCycles(t *testing.T) {
	t.Parallel()
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B"}, {Waiter: "B", Holder: "A"},
		{Waiter: "X", Holder: "Y"}, {Waiter: "Y", Holder: "X"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 2 {
		t.Fatalf("Cycles = %d, want 2", len(r.Cycles))
	}
	// Sort order: A->B before X->Y.
	if r.Cycles[0].Participants[0] != "A" {
		t.Errorf("Cycles[0] starts at %s, want A", r.Cycles[0].Participants[0])
	}
	if r.Cycles[1].Participants[0] != "X" {
		t.Errorf("Cycles[1] starts at %s, want X", r.Cycles[1].Participants[0])
	}
}

func TestDetectDeadlocks_SelfLoop(t *testing.T) {
	t.Parallel()
	edges := []WaitEdge{
		{Waiter: "A", Holder: "A", Resource: "f", Reason: "self-pin"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1 (self-loop)", len(r.Cycles))
	}
	c := r.Cycles[0]
	if len(c.Participants) != 1 || c.Participants[0] != "A" {
		t.Errorf("Participants = %v, want [A]", c.Participants)
	}
	if c.Suggestion == "" {
		t.Error("self-loop produced empty Suggestion")
	}
}

func TestDetectDeadlocks_DeterministicOutput(t *testing.T) {
	t.Parallel()
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B"}, {Waiter: "B", Holder: "C"}, {Waiter: "C", Holder: "A"},
		{Waiter: "X", Holder: "Y"}, {Waiter: "Y", Holder: "X"},
	}
	r1 := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	r2 := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	a, _ := json.Marshal(r1)
	b, _ := json.Marshal(r2)
	if string(a) != string(b) {
		t.Errorf("output drifted between calls:\n%s\n%s", a, b)
	}
}

func TestDetectDeadlocks_DropsEdgesWithMissingEndpoints(t *testing.T) {
	t.Parallel()
	edges := []WaitEdge{
		{Waiter: "", Holder: "B"},
		{Waiter: "A", Holder: ""},
		{Waiter: "A", Holder: "B"}, {Waiter: "B", Holder: "A"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if r.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2 (bad edges dropped)", r.NodeCount)
	}
	if len(r.Cycles) != 1 {
		t.Errorf("Cycles = %d, want 1", len(r.Cycles))
	}
}

func TestDetectDeadlocks_SourcesAndWarningsSurface(t *testing.T) {
	t.Parallel()
	r := DetectDeadlocks(nil, DetectDeadlockOptions{
		Now: deadlockClock(),
		Sources: []SourceStatus{
			{Name: "agentmail", Available: false, Error: "broken"},
			{Name: "claims", Available: true, Edges: 0},
		},
		Warnings: []string{"agentmail unavailable: broken"},
	})
	if len(r.Sources) != 2 {
		t.Errorf("Sources = %d, want 2", len(r.Sources))
	}
	if len(r.Warnings) != 1 {
		t.Errorf("Warnings = %d, want 1", len(r.Warnings))
	}
}

func TestEdgesFromConflicts_BuildsWaiterToHolders(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 8, 11, 0, 0, 0, time.UTC)
	conflicts := []Conflict{
		{
			ID:         "c1",
			FilePath:   "internal/foo.go",
			DetectedAt: now,
			Holders: []Holder{
				{AgentName: "alice", Reason: "editing"},
				{AgentName: "bob", Reason: "reviewing"},
			},
		},
		{
			// no waiter -> dropped
			ID:       "c2",
			FilePath: "internal/bar.go",
			Holders:  []Holder{{AgentName: "carol"}},
		},
	}
	waiters := map[string]string{"c1": "carol"}
	edges := EdgesFromConflicts(conflicts, waiters)

	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(edges))
	}
	// carol waits for alice and bob.
	holders := map[string]bool{}
	for _, e := range edges {
		if e.Waiter != "carol" {
			t.Errorf("edge waiter = %s, want carol", e.Waiter)
		}
		holders[e.Holder] = true
		if e.Resource != "internal/foo.go" {
			t.Errorf("edge resource = %s, want internal/foo.go", e.Resource)
		}
		if !e.Since.Equal(now) {
			t.Errorf("edge since = %v, want %v", e.Since, now)
		}
	}
	if !holders["alice"] || !holders["bob"] {
		t.Errorf("missing holders: %v", holders)
	}
}

func TestEdgesFromConflicts_SkipsSelfHold(t *testing.T) {
	t.Parallel()
	conflicts := []Conflict{{
		ID:      "c1",
		Holders: []Holder{{AgentName: "alice"}},
	}}
	waiters := map[string]string{"c1": "alice"}
	edges := EdgesFromConflicts(conflicts, waiters)
	if len(edges) != 0 {
		t.Errorf("edges = %d, want 0 (self-hold dropped)", len(edges))
	}
}

// bd-6yomt: short-cycle suggestion must name the longest-waiting
// holder (the "to" of the edge with the smallest .Since), not the
// alphabetically-first participant. The canonical cycle ordering puts
// the alphabetically-first participant in Participants[0], so a
// well-targeted suggestion necessarily diverges from Participants[0]
// when the cycle's oldest edge is held by a non-first participant.
func TestDetectDeadlocks_TwoCycleSuggestionPicksLongestWaitingHolder(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 8, 11, 0, 0, 0, time.UTC)
	// A→B is older (since); B→A is newer (since+1h). Edge A→B's holder
	// is B — the longest-waiting upstream blocker. Suggestion must name
	// B even though canonicalization puts A in Participants[0].
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B", Resource: "fileA", Since: since},
		{Waiter: "B", Holder: "A", Resource: "fileB", Since: since.Add(time.Hour)},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1", len(r.Cycles))
	}
	c := r.Cycles[0]
	if !equalSlice(c.Participants, []string{"A", "B"}) {
		t.Fatalf("Participants = %v, want [A B]", c.Participants)
	}
	want := "ask B to release reservations first"
	if c.Suggestion != want {
		t.Errorf("Suggestion = %q, want %q (longest-waiting holder is B; A is alphabetically first but A→B is the oldest edge)", c.Suggestion, want)
	}

	// Symmetric case: flip which edge is older. Now A is the longest-
	// waiting holder and the suggestion should name A — coincidentally
	// matching the alphabetical fallback, but verifying the algorithm
	// doesn't always pick the same name regardless of input.
	edges = []WaitEdge{
		{Waiter: "A", Holder: "B", Resource: "fileA", Since: since.Add(time.Hour)},
		{Waiter: "B", Holder: "A", Resource: "fileB", Since: since},
	}
	r = DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("symmetric Cycles = %d, want 1", len(r.Cycles))
	}
	c = r.Cycles[0]
	want = "ask A to release reservations first"
	if c.Suggestion != want {
		t.Errorf("symmetric Suggestion = %q, want %q (longest-waiting holder is A)", c.Suggestion, want)
	}
}

// bd-6yomt: when no edge metadata is available (every edge has a zero
// .Since), suggestResolution must fall back to the alphabetically-
// first participant so canonical determinism is preserved instead of
// returning "" and breaking downstream consumers.
func TestDetectDeadlocks_TwoCycleSuggestionFallsBackToAlphaWithoutMetadata(t *testing.T) {
	t.Parallel()
	// No Since on either edge → no usable oldest timestamps.
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B"},
		{Waiter: "B", Holder: "A"},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1", len(r.Cycles))
	}
	want := "ask A to release reservations first"
	if got := r.Cycles[0].Suggestion; got != want {
		t.Errorf("Suggestion = %q, want alphabetical fallback %q", got, want)
	}
}

// bd-6yomt: cycles longer than 3 participants intentionally use the
// alphabetically-first participant for stability, regardless of edge
// timestamps. Pin this so a future tweak that extends the
// longest-waiting-holder strategy to long cycles cannot land silently.
func TestDetectDeadlocks_FourCycleSuggestionIsAlphabetical(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 8, 11, 0, 0, 0, time.UTC)
	edges := []WaitEdge{
		{Waiter: "A", Holder: "B", Since: since.Add(2 * time.Hour)},
		{Waiter: "B", Holder: "C", Since: since.Add(3 * time.Hour)},
		// C→D is the oldest edge; in a 2- or 3-cycle this would name D,
		// but in a 4-cycle the alphabetical fallback wins.
		{Waiter: "C", Holder: "D", Since: since},
		{Waiter: "D", Holder: "A", Since: since.Add(time.Hour)},
	}
	r := DetectDeadlocks(edges, DetectDeadlockOptions{Now: deadlockClock()})
	if len(r.Cycles) != 1 {
		t.Fatalf("Cycles = %d, want 1", len(r.Cycles))
	}
	want := "ask A to release reservations first"
	if got := r.Cycles[0].Suggestion; got != want {
		t.Errorf("Suggestion = %q, want %q (4-cycle uses alphabetical fallback)", got, want)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
