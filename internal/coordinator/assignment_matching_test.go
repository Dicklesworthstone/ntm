package coordinator

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func TestMaximumWeightAssignmentBeatsGreedy(t *testing.T) {
	edges := []assignmentMatchEdge{
		{"a", "critical", 0.90}, {"a", "specialist", 0.80},
		{"b", "critical", 0.85}, {"b", "specialist", 0.10},
	}
	got := maximumWeightAssignment(edges)
	assertAssignmentMatching(t, edges, got, 1.65)
	if pairs := assignmentMatchingPairs(edges, got); !reflect.DeepEqual(pairs, []string{"a:specialist", "b:critical"}) {
		t.Fatalf("pairings = %v", pairs)
	}
	// Run the incumbent greedy policy on exactly the same graph, not on a
	// different benchmark fixture or an assumed baseline score.
	greedy := incumbentGreedyAssignment(edges)
	if score := assignmentMatchingScore(edges, greedy); math.Abs(score-1.0) > 1e-12 {
		t.Fatalf("incumbent score = %v, want 1.0", score)
	}
}

func TestMaximumWeightAssignmentSparseAndRectangular(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edges []assignmentMatchEdge
		want  float64
	}{
		{"empty", nil, 0},
		{"single", []assignmentMatchEdge{{"a", "x", 0.5}}, 0.5},
		{"more tasks", []assignmentMatchEdge{{"a", "x", 1}, {"a", "y", 3}, {"a", "z", 2}}, 3},
		{"more agents", []assignmentMatchEdge{{"a", "x", 1}, {"b", "x", 3}, {"c", "x", 2}}, 3},
		{"disconnected", []assignmentMatchEdge{{"a", "x", 1}, {"b", "y", 2}, {"c", "z", 3}}, 6},
		{"augmenting path", []assignmentMatchEdge{{"a", "x", 4}, {"a", "y", 3}, {"b", "x", 3}}, 6},
		{"do not maximize count instead of value", []assignmentMatchEdge{{"a", "x", 100}, {"a", "y", 1}, {"b", "x", 1}}, 100},
		{"duplicate pair", []assignmentMatchEdge{{"a", "x", 1}, {"a", "x", 4}, {"a", "y", 3}, {"b", "x", 3}}, 6},
		{"all invalid", []assignmentMatchEdge{{"a", "x", -1}, {"b", "x", 0}, {"", "x", 3}, {"a", " ", 4}}, 0},
		{"nonfinite", []assignmentMatchEdge{{"a", "x", math.NaN()}, {"a", "y", math.Inf(1)}, {"b", "x", math.Inf(-1)}, {"b", "y", 2}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAssignmentMatching(t, tc.edges, maximumWeightAssignment(tc.edges), tc.want)
		})
	}
}

func TestMaximumWeightAssignmentInputOrderDoesNotBreakTies(t *testing.T) {
	edges := []assignmentMatchEdge{
		{"b", "y", 1}, {"a", "y", 1}, {"c", "x", 1},
		{"b", "x", 1}, {"c", "y", 1}, {"a", "x", 1},
	}
	want := assignmentMatchingPairs(edges, maximumWeightAssignment(edges))
	rng := rand.New(rand.NewSource(73))
	for i := 0; i < 100; i++ {
		rng.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
		got := assignmentMatchingPairs(edges, maximumWeightAssignment(edges))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: pairings %v, want %v", i, got, want)
		}
	}
}

func TestMaximumWeightAssignmentPreservesInput(t *testing.T) {
	edges := []assignmentMatchEdge{{"b", "x", 2}, {"a", "x", 1}}
	before := append([]assignmentMatchEdge(nil), edges...)
	maximumWeightAssignment(edges)
	if !reflect.DeepEqual(edges, before) {
		t.Fatal("solver mutated caller's candidates")
	}
}

func TestMaximumWeightAssignmentFiniteExtremeScores(t *testing.T) {
	for _, scale := range []float64{math.SmallestNonzeroFloat64, 1e-200, 1, 1e200, math.MaxFloat64 / 8} {
		edges := []assignmentMatchEdge{
			{"a", "x", 4 * scale}, {"a", "y", 3 * scale}, {"b", "x", 3 * scale},
		}
		pairs := assignmentMatchingPairs(edges, maximumWeightAssignment(edges))
		if !reflect.DeepEqual(pairs, []string{"a:y", "b:x"}) {
			t.Fatalf("scale %v: %v", scale, pairs)
		}
	}
}

func TestMaximumWeightAssignmentMatchesExhaustiveOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(8247))
	for trial := 0; trial < 600; trial++ {
		agents, tasks := 1+rng.Intn(5), 1+rng.Intn(5)
		var edges []assignmentMatchEdge
		for a := 0; a < agents; a++ {
			for b := 0; b < tasks; b++ {
				if rng.Intn(4) == 0 {
					continue
				}
				edges = append(edges, assignmentMatchEdge{
					agentID: fmt.Sprintf("a%d", a), taskID: fmt.Sprintf("t%d", b),
					score: float64(1+rng.Intn(100)) / 16,
				})
			}
		}
		got := maximumWeightAssignment(edges)
		want := exhaustiveAssignmentScore(edges)
		t.Run(fmt.Sprintf("graph_%03d", trial), func(t *testing.T) {
			assertAssignmentMatching(t, edges, got, want)
		})
	}
}

func assertAssignmentMatching(t *testing.T, edges []assignmentMatchEdge, selected []int, want float64) {
	t.Helper()
	agents, tasks := make(map[string]bool), make(map[string]bool)
	for _, index := range selected {
		if index < 0 || index >= len(edges) {
			t.Fatalf("invalid candidate index %d", index)
		}
		edge := edges[index]
		if !validAssignmentMatchEdge(edge) {
			t.Fatalf("selected ineligible edge %+v", edge)
		}
		if agents[edge.agentID] || tasks[edge.taskID] {
			t.Fatalf("non-exclusive matching: %+v", selected)
		}
		agents[edge.agentID], tasks[edge.taskID] = true, true
	}
	if got := assignmentMatchingScore(edges, selected); math.Abs(got-want) > 1e-10 {
		t.Fatalf("score = %v, want exhaustive optimum %v; edges=%+v, selected=%v", got, want, edges, selected)
	}
}

func assignmentMatchingScore(edges []assignmentMatchEdge, selected []int) float64 {
	total := 0.0
	for _, index := range selected {
		total += edges[index].score
	}
	return total
}

func assignmentMatchingPairs(edges []assignmentMatchEdge, selected []int) []string {
	var pairs []string
	for _, index := range selected {
		pairs = append(pairs, edges[index].agentID+":"+edges[index].taskID)
	}
	sort.Strings(pairs)
	return pairs
}

// Enumerate all legal matchings, including leaving each agent idle. This oracle
// shares neither the Hungarian algorithm nor its cost normalization.
func exhaustiveAssignmentScore(edges []assignmentMatchEdge) float64 {
	byAgent := make(map[string][]assignmentMatchEdge)
	for _, edge := range edges {
		byAgent[edge.agentID] = append(byAgent[edge.agentID], edge)
	}
	var agents []string
	for id := range byAgent {
		agents = append(agents, id)
	}
	sort.Strings(agents)
	used := make(map[string]bool)
	var visit func(int) float64
	visit = func(row int) float64 {
		if row == len(agents) {
			return 0
		}
		best := visit(row + 1)
		for _, edge := range byAgent[agents[row]] {
			if used[edge.taskID] {
				continue
			}
			used[edge.taskID] = true
			candidate := edge.score + visit(row+1)
			used[edge.taskID] = false
			if candidate > best {
				best = candidate
			}
		}
		return best
	}
	return visit(0)
}

// The original production policy: sort every edge descending, then accept the
// first edge that uses neither an assigned pane nor an assigned task.
func incumbentGreedyAssignment(edges []assignmentMatchEdge) []int {
	indices := make([]int, len(edges))
	for i := range indices {
		indices[i] = i
	}
	for i := 0; i < len(indices)-1; i++ {
		for j := i + 1; j < len(indices); j++ {
			if edges[indices[j]].score > edges[indices[i]].score {
				indices[i], indices[j] = indices[j], indices[i]
			}
		}
	}
	agents, tasks := make(map[string]bool), make(map[string]bool)
	var selected []int
	for _, i := range indices {
		edge := edges[i]
		if !agents[edge.agentID] && !tasks[edge.taskID] {
			selected = append(selected, i)
			agents[edge.agentID], tasks[edge.taskID] = true, true
		}
	}
	return selected
}

func BenchmarkAssignmentMatchingComparison(b *testing.B) {
	rng := rand.New(rand.NewSource(713))
	var edges []assignmentMatchEdge
	for a := 0; a < 20; a++ {
		for task := 0; task < 100; task++ {
			edges = append(edges, assignmentMatchEdge{fmt.Sprintf("a%02d", a), fmt.Sprintf("t%03d", task), rng.Float64()})
		}
	}
	b.Run("incumbent_greedy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			incumbentGreedyAssignment(edges)
		}
	})
	b.Run("maximum_weight", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			maximumWeightAssignment(edges)
		}
	})
}
