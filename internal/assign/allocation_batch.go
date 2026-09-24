package assign

// allocationBatchWorker scopes an identity to its session. Display names can
// recur across sessions; treating them as global wastes independent capacity.
type allocationBatchWorker struct {
	session string
	id      string
}

// allocationBatchEdge is a feasible, scored bead-to-worker assignment. Edges
// arrive in the planner's stable preference order; score is in thousandths.
type allocationBatchEdge struct {
	bead   string
	worker allocationBatchWorker
	score  int
}

// selectAllocationBatch returns indexes into edges, in their original order.
// Each bead and worker may occur once, and session capacities apply to the
// entire batch, not independently to every candidate. A missing session limit
// is unbounded; an explicit zero means no remaining capacity. Inputs are never
// mutated, allowing the same snapshot to be previewed repeatedly.
func selectAllocationBatch(edges []allocationBatchEdge, slots map[string]int, limit int) []int {
	selected := make([]int, 0)
	if limit <= 0 {
		return selected
	}
	usedBeads := make(map[string]bool)
	usedWorkers := make(map[allocationBatchWorker]bool)
	usedSessions := make(map[string]int)
	for i, edge := range edges {
		if len(selected) >= limit {
			break
		}
		if usedBeads[edge.bead] || usedWorkers[edge.worker] {
			continue
		}
		if capacity, bounded := slots[edge.worker.session]; bounded && usedSessions[edge.worker.session] >= capacity {
			continue
		}
		selected = append(selected, i)
		usedBeads[edge.bead] = true
		usedWorkers[edge.worker] = true
		usedSessions[edge.worker.session]++
	}
	return selected
}
