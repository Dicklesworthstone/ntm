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
