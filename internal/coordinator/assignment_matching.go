package coordinator

import (
	"math"
	"sort"
	"strings"
)

// assignmentMatchEdge is one eligible agent/task pairing. The returned matching
// contains indices into the input slice, so the caller retains the original
// recommendation, score breakdown, and dispatch metadata without reconstructing
// them from identifiers.
type assignmentMatchEdge struct {
	agentID string
	taskID  string
	score   float64
}

// maximumWeightAssignment finds a maximum-total-score, one-to-one assignment.
// Missing, nonpositive, and nonfinite edges are not eligible. Leaving an agent
// unassigned is allowed; it must never require taking an ineligible edge.
//
// The rectangular Hungarian algorithm uses the smaller partition as rows and
// adds one dummy column per row. Its time bound is O(min(A,T)^2 * (A+T)), rather
// than sorting all A*T candidates quadratically. Memory is O(A*T + A + T).
// Identifiers are sorted before solving, making ties independent of map order.
func maximumWeightAssignment(edges []assignmentMatchEdge) []int {
	agentSet := make(map[string]struct{})
	taskSet := make(map[string]struct{})
	maxScore := 0.0
	for _, edge := range edges {
		if !validAssignmentMatchEdge(edge) {
			continue
		}
		agentSet[edge.agentID] = struct{}{}
		taskSet[edge.taskID] = struct{}{}
		if edge.score > maxScore {
			maxScore = edge.score
		}
	}
	if len(agentSet) == 0 || len(taskSet) == 0 {
		return nil
	}

	agents := sortedAssignmentMatchIDs(agentSet)
	tasks := sortedAssignmentMatchIDs(taskSet)
	rows, columns := agents, tasks
	transposed := len(agents) > len(tasks)
	if transposed {
		rows, columns = tasks, agents
	}
	rowIndex := assignmentMatchIndices(rows)
	columnIndex := assignmentMatchIndices(columns)
	n, realColumns := len(rows), len(columns)

	// Store the input index plus one: zero means the pairing is absent. A
	// duplicate identity pair contributes only its highest-scoring edge.
	matrix := make([][]int, n)
	for i := range matrix {
		matrix[i] = make([]int, realColumns)
	}
	for i, edge := range edges {
		if !validAssignmentMatchEdge(edge) {
			continue
		}
		row, column := edge.agentID, edge.taskID
		if transposed {
			row, column = column, row
		}
		r, c := rowIndex[row], columnIndex[column]
		previous := matrix[r][c]
		if previous == 0 || edge.score > edges[previous-1].score {
			matrix[r][c] = i + 1
		}
	}

	// Dummy columns cost zero. An absent edge costs one and can never beat an
	// unused dummy. Normalize real costs into [-1, 0] so large finite scores
	// cannot overflow the potentials. Ordinary triage scores are near unity.
	cost := func(row, column int) float64 {
		if column >= realColumns {
			return 0
		}
		index := matrix[row][column]
		if index == 0 {
			return 1
		}
		return -edges[index-1].score / maxScore
	}
	m := realColumns + n
	u := make([]float64, n+1)
	v := make([]float64, m+1)
	owner := make([]int, m+1)
	previousColumn := make([]int, m+1)
	minimum := make([]float64, m+1)
	visited := make([]bool, m+1)

	for row := 1; row <= n; row++ {
		owner[0] = row
		for column := 0; column <= m; column++ {
			minimum[column] = math.Inf(1)
			visited[column] = false
		}
		column := 0
		for {
			visited[column] = true
			currentRow := owner[column]
			delta, nextColumn := math.Inf(1), 0
			for candidate := 1; candidate <= m; candidate++ {
				if visited[candidate] {
					continue
				}
				reduced := cost(currentRow-1, candidate-1) - u[currentRow] - v[candidate]
				if reduced < minimum[candidate] {
					minimum[candidate] = reduced
					previousColumn[candidate] = column
				}
				if minimum[candidate] < delta {
					delta, nextColumn = minimum[candidate], candidate
				}
			}
			for candidate := 0; candidate <= m; candidate++ {
				if visited[candidate] {
					u[owner[candidate]] += delta
					v[candidate] -= delta
				} else {
					minimum[candidate] -= delta
				}
			}
			column = nextColumn
			if owner[column] == 0 {
				break
			}
		}
		for column != 0 {
			previous := previousColumn[column]
			owner[column] = owner[previous]
			column = previous
		}
	}

	var selected []int
	for column := 1; column <= realColumns; column++ {
		if owner[column] == 0 {
			continue
		}
		if index := matrix[owner[column]-1][column-1]; index != 0 {
			selected = append(selected, index-1)
		}
	}
	// Publish in canonical identity order. The caller may stably sort this
	// much smaller result by score to retain highest-score-first dispatch.
	sort.Slice(selected, func(i, j int) bool {
		a, b := edges[selected[i]], edges[selected[j]]
		if a.agentID != b.agentID {
			return a.agentID < b.agentID
		}
		return a.taskID < b.taskID
	})
	return selected
}

func validAssignmentMatchEdge(edge assignmentMatchEdge) bool {
	return strings.TrimSpace(edge.agentID) != "" && strings.TrimSpace(edge.taskID) != "" &&
		edge.score > 0 && !math.IsNaN(edge.score) && !math.IsInf(edge.score, 0)
}

func sortedAssignmentMatchIDs(ids map[string]struct{}) []string {
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func assignmentMatchIndices(ids []string) map[string]int {
	indices := make(map[string]int, len(ids))
	for i, id := range ids {
		indices[id] = i
	}
	return indices
}
