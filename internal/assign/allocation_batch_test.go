package assign

import (
	"reflect"
	"testing"
)

func batchTestEdge(bead, session, worker string, score int) allocationBatchEdge {
	return allocationBatchEdge{bead: bead, worker: allocationBatchWorker{session: session, id: worker}, score: score}
}

func TestSelectAllocationBatchCapacity(t *testing.T) {
	edges := []allocationBatchEdge{
		batchTestEdge("urgent", "packed", "a", 950),
		batchTestEdge("next", "packed", "b", 900),
		batchTestEdge("next", "roomy", "c", 800),
		batchTestEdge("later", "roomy", "d", 700),
	}
	for _, tc := range []struct {
		name  string
		slots map[string]int
		limit int
		want  []int
	}{
		{"remaining session capacity", map[string]int{"packed": 1}, 4, []int{0, 2, 3}},
		{"all sessions bounded", map[string]int{"packed": 1, "roomy": 1}, 4, []int{0, 2}},
		{"full session", map[string]int{"packed": 0}, 4, []int{2, 3}},
		{"over capacity", map[string]int{"packed": -1}, 4, []int{2, 3}},
		{"unbounded", nil, 4, []int{0, 1, 3}},
		{"batch limit", nil, 1, []int{0}},
		{"zero batch limit", nil, 0, []int{}},
		{"negative batch limit", nil, -1, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]allocationBatchEdge(nil), edges...)
			got := selectAllocationBatch(edges, tc.slots, tc.limit)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selected %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(edges, before) {
				t.Fatal("selection mutated the candidate snapshot")
			}
			if again := selectAllocationBatch(edges, tc.slots, tc.limit); !reflect.DeepEqual(got, again) {
				t.Fatalf("selection is not repeatable: %v then %v", got, again)
			}
		})
	}
}

func TestSelectAllocationBatchIdentityAndDeduplication(t *testing.T) {
	edges := []allocationBatchEdge{
		batchTestEdge("one", "alpha", "cod-1", 950),
		batchTestEdge("one", "alpha", "cod-1", 950),
		batchTestEdge("two", "alpha", "cod-1", 900),
		batchTestEdge("two", "beta", "cod-1", 850),
		batchTestEdge("one", "gamma", "cod-2", 800),
	}
	if got := selectAllocationBatch(edges, nil, 5); !reflect.DeepEqual(got, []int{0, 3}) {
		t.Fatalf("selected %v, want distinct beads and session-scoped workers [0 3]", got)
	}
	if got := selectAllocationBatch(nil, nil, 1); len(got) != 0 {
		t.Fatalf("empty input selected %v", got)
	}
}

func TestSelectAllocationBatchRepairsGreedyChoices(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edges []allocationBatchEdge
		slots map[string]int
		limit int
		want  []int
	}{
		{
			name: "reserve specialist for the task with a poor fallback",
			edges: []allocationBatchEdge{
				batchTestEdge("flexible", "s", "specialist", 980),
				batchTestEdge("constrained", "s", "specialist", 950),
				batchTestEdge("flexible", "s", "generalist", 940),
				batchTestEdge("constrained", "s", "generalist", 250),
			},
			limit: 2,
			want:  []int{1, 2},
		},
		{
			name: "reassign flexible work to avoid stranding ready work",
			edges: []allocationBatchEdge{
				batchTestEdge("flexible", "s", "a", 900),
				batchTestEdge("flexible", "s", "b", 600),
				batchTestEdge("constrained", "s", "a", 500),
			},
			limit: 2,
			want:  []int{1, 2},
		},
		{
			name: "move work between sessions to preserve a scarce session slot",
			edges: []allocationBatchEdge{
				batchTestEdge("flexible", "scarce", "a", 950),
				batchTestEdge("flexible", "other", "b", 900),
				batchTestEdge("constrained", "scarce", "c", 850),
			},
			slots: map[string]int{"scarce": 1, "other": 1},
			limit: 2,
			want:  []int{1, 2},
		},
		{
			name: "a one-assignment limit still chooses the strongest pair",
			edges: []allocationBatchEdge{
				batchTestEdge("flexible", "s", "specialist", 980),
				batchTestEdge("constrained", "s", "specialist", 950),
				batchTestEdge("flexible", "s", "generalist", 940),
			},
			limit: 1,
			want:  []int{0},
		},
		{
			name: "zero scores retain feasible cardinality",
			edges: []allocationBatchEdge{
				batchTestEdge("flexible", "s", "a", 0),
				batchTestEdge("flexible", "s", "b", 0),
				batchTestEdge("constrained", "s", "a", 0),
			},
			limit: 2,
			want:  []int{1, 2},
		},
		{
			name: "repair a longer alternating path",
			edges: []allocationBatchEdge{
				batchTestEdge("one", "s", "a", 990),
				batchTestEdge("two", "s", "b", 980),
				batchTestEdge("three", "s", "c", 970),
				batchTestEdge("one", "s", "b", 900),
				batchTestEdge("two", "s", "c", 890),
				batchTestEdge("three", "s", "d", 880),
				batchTestEdge("four", "s", "a", 870),
			},
			limit: 4,
			want:  []int{3, 4, 5, 6},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectAllocationBatch(tc.edges, tc.slots, tc.limit)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

// exhaustiveBatchObjective enumerates assignments by bead. It deliberately
// uses no flow network, shortest-path code or production selection helper.
func exhaustiveBatchObjective(edges []allocationBatchEdge, slots map[string]int, limit int) (int, int) {
	beads := []string{}
	choices := make(map[string][]allocationBatchEdge)
	for _, edge := range edges {
		if _, exists := choices[edge.bead]; !exists {
			beads = append(beads, edge.bead)
		}
		choices[edge.bead] = append(choices[edge.bead], edge)
	}
	workers := make(map[allocationBatchWorker]bool)
	sessions := make(map[string]int)
	bestCount, bestScore := 0, 0
	var visit func(int, int, int)
	visit = func(bead, count, score int) {
		if bead == len(beads) || count >= limit {
			if count > bestCount || count == bestCount && score > bestScore {
				bestCount, bestScore = count, score
			}
			return
		}
		visit(bead+1, count, score)
		for _, edge := range choices[beads[bead]] {
			if workers[edge.worker] {
				continue
			}
			capacity, bounded := slots[edge.worker.session]
			if bounded && sessions[edge.worker.session] >= capacity {
				continue
			}
			workers[edge.worker] = true
			sessions[edge.worker.session]++
			value := edge.score
			if value < 0 {
				value = 0
			} else if value > 1000 {
				value = 1000
			}
			visit(bead+1, count+1, score+value)
			workers[edge.worker] = false
			sessions[edge.worker.session]--
		}
	}
	visit(0, 0, 0)
	return bestCount, bestScore
}

func TestSelectAllocationBatchMatchesExhaustiveOracle(t *testing.T) {
	// An explicit tiny PRNG keeps these cases reproducible across Go releases.
	seed := uint32(76129)
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for trial := 0; trial < 2000; trial++ {
		edges := []allocationBatchEdge{}
		beads, workers := next(4)+1, next(4)+1
		for bead := 0; bead < beads; bead++ {
			for worker := 0; worker < workers; worker++ {
				if next(4) == 0 {
					continue
				}
				edge := batchTestEdge(string(rune('a'+bead)), string(rune('x'+worker%2)), string(rune('m'+worker/2)), next(1201)-100)
				edges = append(edges, edge)
				if next(8) == 0 {
					edges = append(edges, edge)
				}
			}
		}
		slots := make(map[string]int)
		for _, session := range []string{"x", "y"} {
			if next(3) != 0 {
				slots[session] = next(5) - 1
			}
		}
		limit := next(7) - 1
		before := append([]allocationBatchEdge{}, edges...)
		beforeSlots := make(map[string]int)
		for session, capacity := range slots {
			beforeSlots[session] = capacity
		}
		got := selectAllocationBatch(edges, slots, limit)
		usedBeads := make(map[string]bool)
		usedWorkers := make(map[allocationBatchWorker]bool)
		usedSessions := make(map[string]int)
		score, previous := 0, -1
		for _, index := range got {
			if index <= previous || index >= len(edges) {
				t.Fatalf("trial %d: invalid or unordered edge indexes %v", trial, got)
			}
			previous = index
			edge := edges[index]
			if usedBeads[edge.bead] || usedWorkers[edge.worker] {
				t.Fatalf("trial %d: assignment reused a bead or worker: %v", trial, got)
			}
			usedBeads[edge.bead], usedWorkers[edge.worker] = true, true
			usedSessions[edge.worker.session]++
			if capacity, bounded := slots[edge.worker.session]; bounded && usedSessions[edge.worker.session] > capacity {
				t.Fatalf("trial %d: exceeded session capacity %v with %v", trial, slots, got)
			}
			score += min(1000, max(0, edge.score))
		}
		if len(got) > max(0, limit) {
			t.Fatalf("trial %d: %d assignments exceed limit %d", trial, len(got), limit)
		}
		wantCount, wantScore := exhaustiveBatchObjective(edges, slots, limit)
		if len(got) != wantCount || score != wantScore {
			t.Fatalf("trial %d: got (%d,%d), oracle (%d,%d); edges=%+v slots=%v limit=%d selected=%v", trial, len(got), score, wantCount, wantScore, edges, slots, limit, got)
		}
		if !reflect.DeepEqual(edges, before) || !reflect.DeepEqual(slots, beforeSlots) {
			t.Fatalf("trial %d: selection modified caller-owned inputs", trial)
		}
		if again := selectAllocationBatch(edges, slots, limit); !reflect.DeepEqual(got, again) {
			t.Fatalf("trial %d: selection is not deterministic: %v then %v", trial, got, again)
		}
	}
}

func TestSelectAllocationBatchSwarmCapacity(t *testing.T) {
	const beadCount, workerCount = 192, 48
	edges := make([]allocationBatchEdge, 0, beadCount*workerCount)
	for bead := 0; bead < beadCount; bead++ {
		for worker := 0; worker < workerCount; worker++ {
			edges = append(edges, batchTestEdge(string(rune(1000+bead)), string(rune('a'+worker%4)), string(rune(2000+worker)), 300+(bead*31+worker*17)%701))
		}
	}
	slots := map[string]int{"a": 8, "b": 8, "c": 8, "d": 8}
	got := selectAllocationBatch(edges, slots, beadCount)
	if len(got) != 32 {
		t.Fatalf("selected %d assignments, want all 32 available session slots", len(got))
	}
	counts := make(map[string]int)
	beads := make(map[string]bool)
	workers := make(map[allocationBatchWorker]bool)
	for _, index := range got {
		edge := edges[index]
		if beads[edge.bead] || workers[edge.worker] {
			t.Fatal("large batch reused a bead or worker")
		}
		beads[edge.bead], workers[edge.worker] = true, true
		counts[edge.worker.session]++
	}
	if !reflect.DeepEqual(counts, slots) {
		t.Fatalf("session allocation counts %v, want %v", counts, slots)
	}
}
