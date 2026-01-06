package pipeline

import (
	"testing"
)

func TestNewDependencyGraph(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
		},
	}

	g := NewDependencyGraph(w)

	if g.Size() != 3 {
		t.Errorf("expected 3 steps, got %d", g.Size())
	}

	// Check edges
	deps := g.GetDependencies("c")
	if len(deps) != 2 {
		t.Errorf("expected 2 dependencies for c, got %d", len(deps))
	}

	// Check reverse edges
	dependents := g.GetDependents("a")
	if len(dependents) != 2 {
		t.Errorf("expected 2 dependents for a, got %d", len(dependents))
	}
}

func TestDependencyGraph_Validate_Valid(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()

	if len(errors) > 0 {
		t.Errorf("expected no errors, got %v", errors)
	}
}

func TestDependencyGraph_Validate_MissingDep(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a", DependsOn: []string{"missing"}},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()

	if len(errors) == 0 {
		t.Error("expected error for missing dependency")
	}

	found := false
	for _, e := range errors {
		if e.Type == "missing_dep" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected missing_dep error type")
	}
}

func TestDependencyGraph_Validate_Cycle(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a", DependsOn: []string{"c"}},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"b"}},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()

	if len(errors) == 0 {
		t.Error("expected error for cycle")
	}

	found := false
	for _, e := range errors {
		if e.Type == "cycle" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected cycle error type")
	}
}

func TestDependencyGraph_Resolve_Linear(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"b"}},
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	if len(plan.Order) != 3 {
		t.Errorf("expected 3 steps in order, got %d", len(plan.Order))
	}

	// Check order: a must come before b, b must come before c
	aIdx, bIdx, cIdx := -1, -1, -1
	for i, id := range plan.Order {
		switch id {
		case "a":
			aIdx = i
		case "b":
			bIdx = i
		case "c":
			cIdx = i
		}
	}

	if aIdx >= bIdx {
		t.Error("a should come before b")
	}
	if bIdx >= cIdx {
		t.Error("b should come before c")
	}
}

func TestDependencyGraph_Resolve_Parallel(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b"}, // No deps - parallel with a
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	// a and b should be in the same level (parallelizable)
	if len(plan.Levels) < 2 {
		t.Fatalf("expected at least 2 levels, got %d", len(plan.Levels))
	}

	firstLevel := plan.Levels[0]
	if len(firstLevel) != 2 {
		t.Errorf("expected 2 steps in first level, got %d", len(firstLevel))
	}

	// c should be in a later level
	cInFirstLevel := false
	for _, id := range firstLevel {
		if id == "c" {
			cInFirstLevel = true
		}
	}
	if cInFirstLevel {
		t.Error("c should not be in first level")
	}
}

func TestDependencyGraph_Resolve_Diamond(t *testing.T) {
	t.Parallel()

	// Diamond dependency: a -> b, a -> c, b -> d, c -> d
	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a"}},
			{ID: "d", Prompt: "step d", DependsOn: []string{"b", "c"}},
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	if len(plan.Order) != 4 {
		t.Errorf("expected 4 steps in order, got %d", len(plan.Order))
	}

	// a must be first, d must be last
	if plan.Order[0] != "a" {
		t.Errorf("expected a to be first, got %s", plan.Order[0])
	}
	if plan.Order[3] != "d" {
		t.Errorf("expected d to be last, got %s", plan.Order[3])
	}
}

func TestDependencyGraph_Resolve_WithCycle(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a", DependsOn: []string{"b"}},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if plan.Valid {
		t.Error("expected invalid plan for cycle")
	}
}

func TestDependencyGraph_GetReadySteps(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b"},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
		},
	}

	g := NewDependencyGraph(w)

	// Initially, a and b should be ready
	ready := g.GetReadySteps()
	if len(ready) != 2 {
		t.Errorf("expected 2 ready steps, got %d", len(ready))
	}

	// Mark a as executed
	if err := g.MarkExecuted("a"); err != nil {
		t.Fatal(err)
	}

	// Still b ready, c not ready yet
	ready = g.GetReadySteps()
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("expected only b ready, got %v", ready)
	}

	// Mark b as executed
	if err := g.MarkExecuted("b"); err != nil {
		t.Fatal(err)
	}

	// Now c should be ready
	ready = g.GetReadySteps()
	if len(ready) != 1 || ready[0] != "c" {
		t.Errorf("expected only c ready, got %v", ready)
	}
}

func TestDependencyGraph_MarkExecuted_NotFound(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
		},
	}

	g := NewDependencyGraph(w)
	err := g.MarkExecuted("nonexistent")

	if err == nil {
		t.Error("expected error for nonexistent step")
	}
}

func TestDependencyGraph_IsExecuted(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
		},
	}

	g := NewDependencyGraph(w)

	if g.IsExecuted("a") {
		t.Error("a should not be executed initially")
	}

	g.MarkExecuted("a")

	if !g.IsExecuted("a") {
		t.Error("a should be executed after marking")
	}
}

func TestDependencyGraph_GetStep(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
		},
	}

	g := NewDependencyGraph(w)

	step, exists := g.GetStep("a")
	if !exists {
		t.Error("expected step a to exist")
	}
	if step.ID != "a" {
		t.Errorf("expected step id 'a', got %q", step.ID)
	}

	_, exists = g.GetStep("nonexistent")
	if exists {
		t.Error("expected nonexistent step to not exist")
	}
}

func TestDependencyGraph_ParallelSubsteps(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{
				ID: "parallel_group",
				Parallel: []Step{
					{ID: "p1", Prompt: "parallel 1"},
					{ID: "p2", Prompt: "parallel 2"},
				},
			},
			{ID: "after", Prompt: "after", DependsOn: []string{"parallel_group"}},
		},
	}

	g := NewDependencyGraph(w)

	// Should include parallel substeps
	if g.Size() != 4 {
		t.Errorf("expected 4 steps (including parallel), got %d", g.Size())
	}

	_, exists := g.GetStep("p1")
	if !exists {
		t.Error("expected parallel substep p1 to exist")
	}
}

func TestResolveWorkflow(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	plan := ResolveWorkflow(w)

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	if len(plan.Order) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Order))
	}
}

func TestDependencyGraph_ComplexGraph(t *testing.T) {
	t.Parallel()

	// More complex graph with multiple paths
	w := &Workflow{
		Steps: []Step{
			{ID: "start", Prompt: "start"},
			{ID: "a", Prompt: "a", DependsOn: []string{"start"}},
			{ID: "b", Prompt: "b", DependsOn: []string{"start"}},
			{ID: "c", Prompt: "c", DependsOn: []string{"a"}},
			{ID: "d", Prompt: "d", DependsOn: []string{"a", "b"}},
			{ID: "e", Prompt: "e", DependsOn: []string{"c", "d"}},
			{ID: "end", Prompt: "end", DependsOn: []string{"e"}},
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	if len(plan.Order) != 7 {
		t.Errorf("expected 7 steps, got %d", len(plan.Order))
	}

	// Check that start is first and end is last
	if plan.Order[0] != "start" {
		t.Errorf("expected start first, got %s", plan.Order[0])
	}
	if plan.Order[6] != "end" {
		t.Errorf("expected end last, got %s", plan.Order[6])
	}
}

func TestDependencyGraph_SelfCycle(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a", DependsOn: []string{"a"}},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()

	if len(errors) == 0 {
		t.Error("expected error for self-cycle")
	}
}

func TestDependencyError_Error(t *testing.T) {
	t.Parallel()

	e := DependencyError{
		Type:    "cycle",
		Steps:   []string{"a", "b", "a"},
		Message: "circular dependency detected",
	}

	msg := e.Error()
	if msg != "circular dependency detected" {
		t.Errorf("expected error message, got %q", msg)
	}
}

func TestDependencyGraph_MarkFailed(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	g := NewDependencyGraph(w)

	// Initially not failed
	if g.IsFailed("a") {
		t.Error("a should not be failed initially")
	}

	// Mark as failed
	if err := g.MarkFailed("a"); err != nil {
		t.Fatalf("failed to mark step as failed: %v", err)
	}

	if !g.IsFailed("a") {
		t.Error("a should be failed after marking")
	}

	// Mark nonexistent should error
	if err := g.MarkFailed("nonexistent"); err == nil {
		t.Error("expected error for nonexistent step")
	}
}

func TestDependencyGraph_HasFailedDependency(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"b"}},
			{ID: "d", Prompt: "step d"}, // No deps
		},
	}

	g := NewDependencyGraph(w)

	// Initially no failed deps
	if g.HasFailedDependency("b") {
		t.Error("b should not have failed dependency initially")
	}

	// Mark a as failed
	g.MarkFailed("a")

	// b should have failed dependency
	if !g.HasFailedDependency("b") {
		t.Error("b should have failed dependency after a fails")
	}

	// c doesn't directly depend on a, but depends on b
	if g.HasFailedDependency("c") {
		t.Error("c should not have failed dependency (only checks direct deps)")
	}

	// d has no deps
	if g.HasFailedDependency("d") {
		t.Error("d should not have failed dependency")
	}
}

func TestDependencyGraph_GetFailedDependencies(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b"},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
		},
	}

	g := NewDependencyGraph(w)

	// Initially empty
	failed := g.GetFailedDependencies("c")
	if len(failed) != 0 {
		t.Errorf("expected 0 failed deps, got %d", len(failed))
	}

	// Mark a as failed
	g.MarkFailed("a")

	failed = g.GetFailedDependencies("c")
	if len(failed) != 1 {
		t.Errorf("expected 1 failed dep, got %d", len(failed))
	}
	if failed[0] != "a" {
		t.Errorf("expected failed dep 'a', got %q", failed[0])
	}

	// Mark b as failed too
	g.MarkFailed("b")

	failed = g.GetFailedDependencies("c")
	if len(failed) != 2 {
		t.Errorf("expected 2 failed deps, got %d", len(failed))
	}
}

func TestDependencyGraph_TransitiveFailure(t *testing.T) {
	t.Parallel()

	// Test transitive failure propagation: A -> B -> C
	// When A fails and B is marked as failed (skipped due to A), C should also detect failed dependency
	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"b"}},
		},
	}

	g := NewDependencyGraph(w)

	// Initially no failed deps
	if g.HasFailedDependency("b") {
		t.Error("b should not have failed dependency initially")
	}
	if g.HasFailedDependency("c") {
		t.Error("c should not have failed dependency initially")
	}

	// Mark a as failed
	g.MarkFailed("a")

	// b now has failed dependency
	if !g.HasFailedDependency("b") {
		t.Error("b should have failed dependency after a fails")
	}

	// c still doesn't have failed dependency (only b is in its deps, and b isn't failed yet)
	if g.HasFailedDependency("c") {
		t.Error("c should not have failed dependency (b hasn't been marked failed)")
	}

	// Now mark b as failed (simulating what executor does when skipping b due to a's failure)
	g.MarkFailed("b")

	// Now c should have failed dependency
	if !g.HasFailedDependency("c") {
		t.Error("c should have failed dependency after b is marked failed")
	}

	// Verify the transitive chain
	failedDeps := g.GetFailedDependencies("c")
	if len(failedDeps) != 1 || failedDeps[0] != "b" {
		t.Errorf("expected c's failed deps to be [b], got %v", failedDeps)
	}
}
