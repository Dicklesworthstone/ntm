package pipeline

import (
	"testing"
)

// =============================================================================
// deps.go: findUnreachable - edge cases
// =============================================================================

func TestFindUnreachableMultipleMissing(t *testing.T) {

	// Create a graph where multiple steps depend on non-existent steps
	workflow := &Workflow{
		Steps: []Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"missing1"}},
			{ID: "c", DependsOn: []string{"missing2"}},
			{ID: "d", DependsOn: []string{"a"}},
		},
	}

	graph := NewDependencyGraph(workflow)
	unreachable := graph.findUnreachable()

	if len(unreachable) != 2 {
		t.Errorf("expected 2 unreachable steps, got %d: %v", len(unreachable), unreachable)
	}

	// Both b and c should be unreachable
	found := make(map[string]bool)
	for _, id := range unreachable {
		found[id] = true
	}
	if !found["b"] {
		t.Error("step 'b' should be unreachable")
	}
	if !found["c"] {
		t.Error("step 'c' should be unreachable")
	}
}

func TestFindUnreachableNoDeps(t *testing.T) {

	// All independent steps - none unreachable
	workflow := &Workflow{
		Steps: []Step{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
		},
	}

	graph := NewDependencyGraph(workflow)
	unreachable := graph.findUnreachable()

	if len(unreachable) != 0 {
		t.Errorf("expected 0 unreachable steps, got %d: %v", len(unreachable), unreachable)
	}
}

// =============================================================================
// deps.go: Resolve - levels parallelism
// =============================================================================

func TestResolveLevelsParallelism(t *testing.T) {

	// a and b are independent, c depends on both
	workflow := &Workflow{
		Steps: []Step{
			{ID: "a"},
			{ID: "b"},
			{ID: "c", DependsOn: []string{"a", "b"}},
			{ID: "d", DependsOn: []string{"c"}},
		},
	}

	plan := NewDependencyGraph(workflow).Resolve()

	if !plan.Valid {
		t.Fatalf("plan should be valid, got errors: %v", plan.Errors)
	}

	if len(plan.Levels) != 3 {
		t.Fatalf("expected 3 levels, got %d: %v", len(plan.Levels), plan.Levels)
	}

	// Level 0: a and b (parallel)
	if len(plan.Levels[0]) != 2 {
		t.Errorf("level 0 should have 2 steps, got %d: %v", len(plan.Levels[0]), plan.Levels[0])
	}

	// Level 1: c
	if len(plan.Levels[1]) != 1 || plan.Levels[1][0] != "c" {
		t.Errorf("level 1 should be [c], got %v", plan.Levels[1])
	}

	// Level 2: d
	if len(plan.Levels[2]) != 1 || plan.Levels[2][0] != "d" {
		t.Errorf("level 2 should be [d], got %v", plan.Levels[2])
	}
}

func TestResolveSingleStep(t *testing.T) {

	workflow := &Workflow{
		Steps: []Step{{ID: "only"}},
	}

	plan := NewDependencyGraph(workflow).Resolve()

	if !plan.Valid {
		t.Fatalf("plan should be valid")
	}

	if len(plan.Order) != 1 || plan.Order[0] != "only" {
		t.Errorf("order should be [only], got %v", plan.Order)
	}

	if len(plan.Levels) != 1 || len(plan.Levels[0]) != 1 {
		t.Errorf("should have 1 level with 1 step, got %v", plan.Levels)
	}
}

func TestResolveEmpty(t *testing.T) {

	workflow := &Workflow{Steps: []Step{}}

	plan := NewDependencyGraph(workflow).Resolve()

	if !plan.Valid {
		t.Error("empty workflow should be valid")
	}
	if len(plan.Order) != 0 {
		t.Errorf("expected empty order, got %v", plan.Order)
	}
}

// =============================================================================
// deps.go: Validate - combined errors
// =============================================================================

func TestValidateMissingDepAndCycle(t *testing.T) {

	workflow := &Workflow{
		Steps: []Step{
			{ID: "a", DependsOn: []string{"nonexistent"}},
			{ID: "b", DependsOn: []string{"c"}},
			{ID: "c", DependsOn: []string{"b"}},
		},
	}

	graph := NewDependencyGraph(workflow)
	errors := graph.Validate()

	// Should have both missing dep and cycle errors
	hasMissing := false
	hasCycle := false
	for _, e := range errors {
		if e.Type == "missing_dep" {
			hasMissing = true
		}
		if e.Type == "cycle" {
			hasCycle = true
		}
	}

	if !hasMissing {
		t.Error("should report missing dependency")
	}
	if !hasCycle {
		t.Error("should report cycle")
	}
}

// =============================================================================
// deps.go: GetReadySteps - with executed and failed
// =============================================================================

func TestGetReadyStepsWithFailedDeps(t *testing.T) {

	workflow := &Workflow{
		Steps: []Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
			{ID: "c"},
		},
	}

	graph := NewDependencyGraph(workflow)

	// Initially a and c are ready
	ready := graph.GetReadySteps()
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready steps initially, got %d: %v", len(ready), ready)
	}

	// Execute a, mark it failed
	_ = graph.MarkExecuted("a")
	_ = graph.MarkFailed("a")

	// b should now be ready (deps executed) but has failed dependency
	ready = graph.GetReadySteps()
	hasFailed := graph.HasFailedDependency("b")
	if !hasFailed {
		t.Error("b should have a failed dependency")
	}

	// GetFailedDependencies should return ["a"]
	failedDeps := graph.GetFailedDependencies("b")
	if len(failedDeps) != 1 || failedDeps[0] != "a" {
		t.Errorf("expected failed deps [a], got %v", failedDeps)
	}
}

// =============================================================================
// deps.go: GetDependencies and GetDependents
// =============================================================================

func TestGetDependenciesAndDependents(t *testing.T) {

	workflow := &Workflow{
		Steps: []Step{
			{ID: "root"},
			{ID: "mid", DependsOn: []string{"root"}},
			{ID: "leaf", DependsOn: []string{"mid"}},
		},
	}

	graph := NewDependencyGraph(workflow)

	// root has no dependencies
	if deps := graph.GetDependencies("root"); len(deps) != 0 {
		t.Errorf("root should have no dependencies, got %v", deps)
	}

	// root's dependents are [mid]
	if deps := graph.GetDependents("root"); len(deps) != 1 || deps[0] != "mid" {
		t.Errorf("root dependents should be [mid], got %v", deps)
	}

	// mid depends on root
	if deps := graph.GetDependencies("mid"); len(deps) != 1 || deps[0] != "root" {
		t.Errorf("mid dependencies should be [root], got %v", deps)
	}

	// leaf depends on mid
	if deps := graph.GetDependencies("leaf"); len(deps) != 1 || deps[0] != "mid" {
		t.Errorf("leaf dependencies should be [mid], got %v", deps)
	}

	// leaf has no dependents
	if deps := graph.GetDependents("leaf"); len(deps) != 0 {
		t.Errorf("leaf should have no dependents, got %v", deps)
	}

	// Size check
	if graph.Size() != 3 {
		t.Errorf("expected size 3, got %d", graph.Size())
	}
}

// =============================================================================
// deps.go: NewDependencyGraph with loop sub-steps
// =============================================================================

func TestNewDependencyGraphWithLoopSteps(t *testing.T) {

	workflow := &Workflow{
		Steps: []Step{
			{ID: "setup"},
			{
				ID:        "loop-step",
				DependsOn: []string{"setup"},
				Loop: &LoopConfig{
					Steps: []Step{
						{ID: "inner-a"},
						{ID: "inner-b", DependsOn: []string{"inner-a"}},
					},
				},
			},
		},
	}

	graph := NewDependencyGraph(workflow)

	// Should have all 4 steps
	if graph.Size() != 4 {
		t.Errorf("expected 4 steps, got %d", graph.Size())
	}

	// inner-b should depend on inner-a
	deps := graph.GetDependencies("inner-b")
	if len(deps) != 1 || deps[0] != "inner-a" {
		t.Errorf("inner-b should depend on inner-a, got %v", deps)
	}
}
