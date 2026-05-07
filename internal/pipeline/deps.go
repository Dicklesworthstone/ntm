package pipeline

import (
	"fmt"
	"sort"
)

// DependencyGraph represents step dependencies for execution ordering
type DependencyGraph struct {
	steps         map[string]*Step    // step ID -> step
	edges         map[string][]string // step ID -> steps it depends on
	reverse       map[string][]string // step ID -> steps that depend on it
	inDegree      map[string]int      // step ID -> number of dependencies
	remainingDeps map[string]int      // step ID -> unresolved dependency count
	ready         []string            // sorted ready step IDs
	executed      map[string]bool     // step ID -> has been executed
	executedCount int                 // number of executed steps
	failed        map[string]bool     // step ID -> has failed (for CONTINUE mode)
	// container is the immediate parent container ID for steps nested in a
	// parallel/loop body. Top-level steps are absent from this map.
	// bd-mb6rd uses this to reject cross-container depends_on edges that
	// would otherwise let downstream steps unblock before the container
	// itself ran the nested body.
	container map[string]string
}

// DependencyError represents an error in the dependency graph
type DependencyError struct {
	Type    string   `json:"type"`  // cycle, missing_dep, unreachable
	Steps   []string `json:"steps"` // affected step IDs
	Message string   `json:"message"`
}

func (e DependencyError) Error() string {
	return e.Message
}

// ExecutionPlan contains the resolved execution order
type ExecutionPlan struct {
	Order  []string          `json:"order"`  // Step IDs in execution order
	Levels [][]string        `json:"levels"` // Parallelizable levels
	Errors []DependencyError `json:"errors,omitempty"`
	Valid  bool              `json:"valid"`
}

// NewDependencyGraph creates a dependency graph from workflow steps
func NewDependencyGraph(workflow *Workflow) *DependencyGraph {
	g := &DependencyGraph{
		steps:         make(map[string]*Step),
		edges:         make(map[string][]string),
		reverse:       make(map[string][]string),
		inDegree:      make(map[string]int),
		remainingDeps: make(map[string]int),
		executed:      make(map[string]bool),
		failed:        make(map[string]bool),
		container:     make(map[string]string),
	}

	// Add all steps including parallel sub-steps
	var addSteps func(steps []Step, parent string)
	addSteps = func(steps []Step, parent string) {
		for i := range steps {
			step := &steps[i]
			g.steps[step.ID] = step
			g.edges[step.ID] = step.DependsOn
			g.inDegree[step.ID] = len(step.DependsOn)
			g.remainingDeps[step.ID] = len(step.DependsOn)
			if parent != "" {
				g.container[step.ID] = parent
			}

			// Build reverse edges
			for _, dep := range step.DependsOn {
				g.reverse[dep] = append(g.reverse[dep], step.ID)
			}

			// Handle parallel sub-steps
			if len(step.Parallel.Steps) > 0 {
				addSteps(step.Parallel.Steps, step.ID)
			}

			// Handle loop sub-steps
			if step.Loop != nil {
				addSteps(step.Loop.Steps, step.ID)
			}
		}
	}

	addSteps(workflow.Steps, "")
	for id, remaining := range g.remainingDeps {
		if remaining == 0 {
			g.addReady(id)
		}
	}
	return g
}

// Validate checks the dependency graph for errors
func (g *DependencyGraph) Validate() []DependencyError {
	var errors []DependencyError

	// Check for missing dependencies and cross-container references.
	// bd-mb6rd: a step inside a parallel/loop body must not be depended on
	// from outside that container — graph nodes for body steps share the
	// flat dependency space, so a stale edge would let downstream steps
	// unblock before the container itself executed the body. Allowed
	// edges: (a) any depends_on whose target is top-level; (b) intra-
	// container sibling depends_on (same parent). Reject everything else
	// during validation so misconfigured workflows fail fast instead of
	// silently running steps with missing/stale nested outputs.
	edgeIDs := make([]string, 0, len(g.edges))
	for id := range g.edges {
		edgeIDs = append(edgeIDs, id)
	}
	sort.Strings(edgeIDs)
	for _, id := range edgeIDs {
		deps := g.edges[id]
		refererParent := g.container[id]
		for _, dep := range deps {
			if _, exists := g.steps[dep]; !exists {
				errors = append(errors, DependencyError{
					Type:    "missing_dep",
					Steps:   []string{id, dep},
					Message: fmt.Sprintf("step %q depends on non-existent step %q", id, dep),
				})
				continue
			}
			refereeParent := g.container[dep]
			if refereeParent == "" {
				// Top-level target — always reachable.
				continue
			}
			if refereeParent == refererParent {
				// Same container — sibling dependency is fine.
				continue
			}
			errors = append(errors, DependencyError{
				Type:    "nested_body_dep",
				Steps:   []string{id, dep},
				Message: fmt.Sprintf("step %q depends on nested body step %q (inside container %q); top-level depends_on must reference the container, not its children", id, dep, refereeParent),
			})
		}
	}

	// Check for cycles
	if cycles := g.detectCycles(); len(cycles) > 0 {
		for _, cycle := range cycles {
			errors = append(errors, DependencyError{
				Type:    "cycle",
				Steps:   cycle,
				Message: fmt.Sprintf("circular dependency: %v", cycle),
			})
		}
	}

	// Check for unreachable steps (after cycle detection)
	if len(errors) == 0 {
		unreachable := g.findUnreachable()
		for _, id := range unreachable {
			errors = append(errors, DependencyError{
				Type:    "unreachable",
				Steps:   []string{id},
				Message: fmt.Sprintf("step %q is unreachable (depends on steps that form a cycle)", id),
			})
		}
	}

	return errors
}

// detectCycles finds all cycles in the dependency graph using DFS
func (g *DependencyGraph) detectCycles() [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(node string)
	dfs = func(node string) {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, dep := range g.edges[node] {
			if !visited[dep] {
				dfs(dep)
			} else if recStack[dep] {
				// Found cycle - extract it
				cycleStart := -1
				for i, n := range path {
					if n == dep {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cycle := make([]string, len(path)-cycleStart+1)
					copy(cycle, path[cycleStart:])
					cycle[len(cycle)-1] = dep // Complete the cycle
					cycles = append(cycles, cycle)
				}
			}
		}

		path = path[:len(path)-1]
		recStack[node] = false
	}

	for node := range g.steps {
		if !visited[node] {
			dfs(node)
		}
	}

	return cycles
}

// findUnreachable finds steps that can never be executed
func (g *DependencyGraph) findUnreachable() []string {
	// A step is unreachable if it has dependencies that don't exist
	// or if all paths to it go through a cycle
	// For now, we check for missing dependencies (cycles detected separately)
	var unreachable []string

	for id, deps := range g.edges {
		for _, dep := range deps {
			if _, exists := g.steps[dep]; !exists {
				unreachable = append(unreachable, id)
				break
			}
		}
	}

	return unreachable
}

// Resolve performs topological sort and returns execution plan
func (g *DependencyGraph) Resolve() ExecutionPlan {
	plan := ExecutionPlan{
		Order:  make([]string, 0),
		Levels: make([][]string, 0),
		Valid:  true,
	}

	// Validate first
	if errors := g.Validate(); len(errors) > 0 {
		plan.Errors = errors
		plan.Valid = false
		return plan
	}

	// Kahn's algorithm for topological sort with level tracking
	inDegree := make(map[string]int)
	for id, degree := range g.inDegree {
		inDegree[id] = degree
	}

	// Find initial nodes with no dependencies
	queue := make([]string, 0)
	for id, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue) // Deterministic order

	for len(queue) > 0 {
		// Current level contains all steps with resolved dependencies
		level := make([]string, len(queue))
		copy(level, queue)
		sort.Strings(level)
		plan.Levels = append(plan.Levels, level)

		nextQueue := make([]string, 0)

		for _, node := range queue {
			plan.Order = append(plan.Order, node)

			// Reduce in-degree for dependent steps
			for _, dependent := range g.reverse[node] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					nextQueue = append(nextQueue, dependent)
				}
			}
		}

		queue = nextQueue
		sort.Strings(queue) // Deterministic order
	}

	// Check if all steps are included
	if len(plan.Order) != len(g.steps) {
		// Some steps couldn't be scheduled (shouldn't happen after validation)
		plan.Valid = false
		for id := range g.steps {
			found := false
			for _, scheduled := range plan.Order {
				if scheduled == id {
					found = true
					break
				}
			}
			if !found {
				plan.Errors = append(plan.Errors, DependencyError{
					Type:    "unschedulable",
					Steps:   []string{id},
					Message: fmt.Sprintf("step %q could not be scheduled", id),
				})
			}
		}
	}

	return plan
}

// GetReadySteps returns steps that are ready to execute
func (g *DependencyGraph) GetReadySteps() []string {
	ready := make([]string, len(g.ready))
	copy(ready, g.ready)
	return ready
}

// MarkExecuted marks a step as executed
func (g *DependencyGraph) MarkExecuted(id string) error {
	if _, exists := g.steps[id]; !exists {
		return fmt.Errorf("step %q not found", id)
	}
	if g.executed[id] {
		return nil
	}
	g.executed[id] = true
	g.executedCount++
	g.removeReady(id)
	for _, dependent := range g.reverse[id] {
		if g.remainingDeps[dependent] > 0 {
			g.remainingDeps[dependent]--
		}
		if g.remainingDeps[dependent] == 0 {
			g.addReady(dependent)
		}
	}
	return nil
}

// IsExecuted returns whether a step has been executed
func (g *DependencyGraph) IsExecuted(id string) bool {
	return g.executed[id]
}

// ExecutedCount returns the number of executed steps.
func (g *DependencyGraph) ExecutedCount() int {
	return g.executedCount
}

// MarkFailed marks a step as failed (for CONTINUE mode dependency tracking)
func (g *DependencyGraph) MarkFailed(id string) error {
	if _, exists := g.steps[id]; !exists {
		return fmt.Errorf("step %q not found", id)
	}
	g.failed[id] = true
	return nil
}

// IsFailed returns whether a step has failed
func (g *DependencyGraph) IsFailed(id string) bool {
	return g.failed[id]
}

// HasFailedDependency returns true if any of the step's dependencies failed
func (g *DependencyGraph) HasFailedDependency(id string) bool {
	for _, dep := range g.edges[id] {
		if g.failed[dep] {
			return true
		}
	}
	return false
}

// GetFailedDependencies returns the list of failed dependencies for a step
func (g *DependencyGraph) GetFailedDependencies(id string) []string {
	var failed []string
	for _, dep := range g.edges[id] {
		if g.failed[dep] {
			failed = append(failed, dep)
		}
	}
	return failed
}

// GetStep returns a step by ID
func (g *DependencyGraph) GetStep(id string) (*Step, bool) {
	step, exists := g.steps[id]
	return step, exists
}

// GetDependencies returns the dependencies for a step
func (g *DependencyGraph) GetDependencies(id string) []string {
	return g.edges[id]
}

// GetDependents returns steps that depend on the given step
func (g *DependencyGraph) GetDependents(id string) []string {
	return g.reverse[id]
}

// Size returns the number of steps in the graph
func (g *DependencyGraph) Size() int {
	return len(g.steps)
}

// ResolveWorkflow is a convenience function to create a graph and resolve it
func ResolveWorkflow(workflow *Workflow) ExecutionPlan {
	graph := NewDependencyGraph(workflow)
	return graph.Resolve()
}

func (g *DependencyGraph) addReady(id string) {
	if g.executed[id] {
		return
	}
	idx := sort.SearchStrings(g.ready, id)
	if idx < len(g.ready) && g.ready[idx] == id {
		return
	}
	g.ready = append(g.ready, "")
	copy(g.ready[idx+1:], g.ready[idx:])
	g.ready[idx] = id
}

func (g *DependencyGraph) removeReady(id string) {
	idx := sort.SearchStrings(g.ready, id)
	if idx >= len(g.ready) || g.ready[idx] != id {
		return
	}
	copy(g.ready[idx:], g.ready[idx+1:])
	g.ready = g.ready[:len(g.ready)-1]
}
