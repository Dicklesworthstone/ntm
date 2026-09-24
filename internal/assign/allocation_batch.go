package assign

import "container/heap"

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

// selectAllocationBatch maximizes the number of feasible assignments, then
// their total score, and returns edge indexes in the original preference order.
// Eligibility and minimum scores have already been checked by the planner.
// Each bead and worker may occur once, and session capacities apply to the
// entire batch. A missing session limit is unbounded; an explicit zero means
// no remaining capacity. Inputs are never mutated.
//
// A min-cost flow network (source -> bead -> worker -> session -> sink) lets
// later assignments revise earlier choices. Greedy selection can strand a
// specialist-only task, or fill a session with work another session could do.
// Reverse residual edges repair both cases without relaxing any safety gate.
// Successive shortest paths with potentials take O(F E log V), where F is at
// most the number of workers and E is the number of feasible candidates.
func selectAllocationBatch(edges []allocationBatchEdge, slots map[string]int, limit int) []int {
	selected := make([]int, 0)
	if limit <= 0 || len(edges) == 0 {
		return selected
	}

	const source, sink = 0, 1
	graph := make(allocationFlowGraph, 2)
	beads := make(map[string]int)
	workers := make(map[allocationBatchWorker]int)
	sessions := make(map[string]int)
	type edgeRef struct{ node, edge int }
	refs := make([]edgeRef, len(edges))

	// Allocate nodes in candidate order, never map iteration order, so equal
	// cost paths have a deterministic tie-break inherited from the planner.
	for i, candidate := range edges {
		session, ok := sessions[candidate.worker.session]
		if !ok {
			session = len(graph)
			graph = append(graph, nil)
			sessions[candidate.worker.session] = session
			capacity := limit
			if remaining, bounded := slots[candidate.worker.session]; bounded {
				capacity = min(capacity, max(0, remaining))
			}
			graph.addEdge(session, sink, capacity, 0)
		}
		worker, ok := workers[candidate.worker]
		if !ok {
			worker = len(graph)
			graph = append(graph, nil)
			workers[candidate.worker] = worker
			graph.addEdge(worker, session, 1, 0)
		}
		bead, ok := beads[candidate.bead]
		if !ok {
			bead = len(graph)
			graph = append(graph, nil)
			beads[candidate.bead] = bead
			graph.addEdge(source, bead, 1, 0)
		}
		refs[i] = edgeRef{node: bead, edge: len(graph[bead])}
		// Nonnegative initial costs allow zero initial potentials. A constant
		// per assignment preserves maximum total score at every flow value.
		graph.addEdge(bead, worker, 1, int64(1000-min(1000, max(0, candidate.score))))
	}

	potential := make([]int64, len(graph))
	for flow := 0; flow < min(limit, len(workers), len(beads)); flow++ {
		if !graph.augment(source, sink, potential) {
			break
		}
	}
	for i, ref := range refs {
		if graph[ref.node][ref.edge].capacity == 0 {
			selected = append(selected, i)
		}
	}
	return selected
}

type allocationFlowEdge struct {
	to       int
	reverse  int
	capacity int
	cost     int64
}

type allocationFlowGraph [][]allocationFlowEdge

func (g allocationFlowGraph) addEdge(from, to, capacity int, cost int64) {
	forward := allocationFlowEdge{to: to, reverse: len(g[to]), capacity: capacity, cost: cost}
	backward := allocationFlowEdge{to: from, reverse: len(g[from]), cost: -cost}
	g[from] = append(g[from], forward)
	g[to] = append(g[to], backward)
}

func (g allocationFlowGraph) augment(source, sink int, potential []int64) bool {
	const infinity int64 = 1 << 60
	distance := make([]int64, len(g))
	previousNode := make([]int, len(g))
	previousEdge := make([]int, len(g))
	for node := range g {
		distance[node] = infinity
		previousNode[node] = -1
	}
	distance[source] = 0
	queue := &allocationFlowQueue{{node: source}}
	for queue.Len() > 0 {
		current := heap.Pop(queue).(allocationFlowVisit)
		if current.distance != distance[current.node] {
			continue
		}
		for index, edge := range g[current.node] {
			if edge.capacity <= 0 {
				continue
			}
			next := current.distance + edge.cost + potential[current.node] - potential[edge.to]
			if next >= distance[edge.to] {
				continue
			}
			distance[edge.to] = next
			previousNode[edge.to] = current.node
			previousEdge[edge.to] = index
			heap.Push(queue, allocationFlowVisit{node: edge.to, distance: next})
		}
	}
	if previousNode[sink] < 0 {
		return false
	}
	// Run Dijkstra to completion before updating potentials: partial distances
	// would make reduced costs negative on the next residual search.
	for node, dist := range distance {
		if dist < infinity {
			potential[node] += dist
		}
	}
	for node := sink; node != source; node = previousNode[node] {
		from, index := previousNode[node], previousEdge[node]
		edge := &g[from][index]
		edge.capacity--
		g[node][edge.reverse].capacity++
	}
	return true
}

type allocationFlowVisit struct {
	node     int
	distance int64
}

type allocationFlowQueue []allocationFlowVisit

func (q allocationFlowQueue) Len() int { return len(q) }
func (q allocationFlowQueue) Less(i, j int) bool {
	if q[i].distance != q[j].distance {
		return q[i].distance < q[j].distance
	}
	return q[i].node < q[j].node
}
func (q allocationFlowQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *allocationFlowQueue) Push(value any) {
	*q = append(*q, value.(allocationFlowVisit))
}
func (q *allocationFlowQueue) Pop() any {
	last := len(*q) - 1
	value := (*q)[last]
	*q = (*q)[:last]
	return value
}
