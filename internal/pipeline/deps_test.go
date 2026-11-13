package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestNewDependencyGraph(t *testing.T) {

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

func TestDependencyGraph_Validate_PostPipelinePhaseOwnership(t *testing.T) {
	command := func(id string) Step { return Step{ID: id, Command: "true"} }
	parallel := func(id, child string) Step {
		return Step{ID: id, Parallel: ParallelSpec{Steps: []Step{command(child)}}}
	}
	for _, tc := range []struct {
		name   string
		main   []Step
		post   []Step
		cancel []Step
		id     string
		owners []string
	}{
		{name: "main_explicit_post", main: []Step{command("finish")}, post: []Step{command("finish")}, id: "finish", owners: []string{"steps", "post_pipeline[0]"}},
		{name: "main_synthetic_post", main: []Step{command("post_pipeline_1")}, post: []Step{command("")}, id: "post_pipeline_1", owners: []string{"steps", "post_pipeline[0]"}},
		{name: "post_explicit_post", post: []Step{command("finish"), command("finish")}, id: "finish", owners: []string{"post_pipeline[0]", "post_pipeline[1]"}},
		{name: "post_synthetic_post", post: []Step{command("post_pipeline_2"), command("")}, id: "post_pipeline_2", owners: []string{"post_pipeline[0]", "post_pipeline[1]"}},
		{name: "post_explicit_cancel", post: []Step{command("release")}, cancel: []Step{command("release")}, id: "release", owners: []string{"post_pipeline[0]", "on_cancel[0]"}},
		{name: "synthetic_post_cancel", post: []Step{command("")}, cancel: []Step{command("post_pipeline_1")}, id: "post_pipeline_1", owners: []string{"post_pipeline[0]", "on_cancel[0]"}},
		{name: "post_synthetic_cancel", post: []Step{command("on_cancel_1")}, cancel: []Step{command("")}, id: "on_cancel_1", owners: []string{"post_pipeline[0]", "on_cancel[0]"}},
		{name: "post_owned_by_main_body", main: []Step{parallel("work", "publish")}, post: []Step{command("work_publish")}, id: "work_publish", owners: []string{"steps", "post_pipeline[0]"}},
		{name: "main_owned_by_post_body", main: []Step{command("finish_publish")}, post: []Step{parallel("finish", "publish")}, id: "finish_publish", owners: []string{"steps", "post_pipeline[0]"}},
		{name: "main_owned_by_synthetic_post_body", main: []Step{command("post_pipeline_1_publish")}, post: []Step{parallel("", "publish")}, id: "post_pipeline_1_publish", owners: []string{"steps", "post_pipeline[0]"}},
		{name: "post_owned_by_post_body", post: []Step{parallel("finish", "publish"), command("finish_publish")}, id: "finish_publish", owners: []string{"post_pipeline[0]", "post_pipeline[1]"}},
		{name: "post_owned_by_cancel_body", post: []Step{command("cleanup_release")}, cancel: []Step{parallel("cleanup", "release")}, id: "cleanup_release", owners: []string{"post_pipeline[0]", "on_cancel[0]"}},
		{name: "cancel_owned_by_post_body", post: []Step{parallel("finish", "release")}, cancel: []Step{command("finish_release")}, id: "finish_release", owners: []string{"post_pipeline[0]", "on_cancel[0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main := tc.main
			if main == nil {
				main = []Step{command("work")}
			}
			workflow := &Workflow{Steps: main, PostPipelineSteps: tc.post, Settings: WorkflowSettings{OnCancel: tc.cancel}}
			graph := NewDependencyGraph(workflow)
			requirePhaseOwnershipError(t, graph.Validate(), tc.id, tc.owners...)
			if plan := graph.Resolve(); plan.Valid {
				t.Fatal("ambiguous phase IDs produced an executable plan")
			}
		})
	}
}

func TestDependencyGraph_Validate_PostPipelinePhaseOwnershipPreservesDistinctScopes(t *testing.T) {
	for _, withPost := range []bool{false, true} {
		t.Run(fmt.Sprintf("on_cancel_only_collision_with_post_%t", withPost), func(t *testing.T) {
			workflow := &Workflow{
				Steps:    []Step{{ID: "cleanup_release", Command: "true"}},
				Settings: WorkflowSettings{OnCancel: []Step{{ID: "cleanup", Parallel: ParallelSpec{Steps: []Step{{ID: "release", Command: "true"}}}}}},
			}
			if withPost {
				workflow.PostPipelineSteps = []Step{{ID: "finish", Command: "true"}}
			}
			if errs := NewDependencyGraph(workflow).Validate(); len(errs) != 0 {
				t.Fatalf("post guard broadened the existing on_cancel-only ownership policy: %v", errs)
			}
		})
	}
	workflow := &Workflow{
		Steps:             []Step{{ID: "work", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{{ID: "publish", Command: "true"}}}}},
		PostPipelineSteps: []Step{{ID: "finish", Parallel: ParallelSpec{Steps: []Step{{ID: "publish", Command: "true"}}}}},
	}
	graph := NewDependencyGraph(workflow)
	state := &ExecutionState{Steps: map[string]StepResult{
		"work_iter0_publish": {StepID: "work_iter0_publish", Status: StatusCompleted},
		"finish_publish":     {StepID: "finish_publish", Status: StatusCompleted},
	}}
	if errs := graph.Validate(); len(errs) != 0 {
		t.Fatalf("distinct runtime namespaces rejected because they share an authored leaf name: %v", errs)
	}
	if errs := graph.validatePhaseOwnership(state); len(errs) != 0 {
		t.Fatalf("unambiguous persisted runtime IDs were rejected: %v", errs)
	}
	if _, scheduled := graph.GetStep("finish"); scheduled {
		t.Fatal("post hook became a main graph node")
	}
}

func TestDependencyGraph_Validate_PersistedDynamicPhaseOwnership(t *testing.T) {
	const id = "batch_iter0_publish"
	workflow := &Workflow{
		Name:              "dynamic-phase-ownership",
		Steps:             []Step{{ID: "batch", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{{ID: "publish", Command: "true"}}}}},
		PostPipelineSteps: []Step{{ID: "batch_iter0", Parallel: ParallelSpec{Steps: []Step{{ID: "publish", Command: "true"}}}}},
	}
	graph := NewDependencyGraph(workflow)
	if errs := graph.Validate(); len(errs) != 0 {
		t.Fatalf("fixture should expose its ambiguity only after a dynamic runtime ID exists: %v", errs)
	}
	for _, tc := range []struct {
		name string
		set  func(*ExecutionState)
	}{
		{"step_result", func(s *ExecutionState) {
			s.Steps = map[string]StepResult{id: {StepID: id, Status: StatusCompleted, Output: "keep"}}
		}},
		{"command_receipt", func(s *ExecutionState) { s.CommandExecutions = map[string]CommandExecutionState{id: {StepID: id}} }},
		{"agent_receipt", func(s *ExecutionState) { s.AgentDeliveries = map[string]AgentDeliveryState{id: {StepID: id}} }},
		{"in_flight", func(s *ExecutionState) { s.InFlightSteps = map[string]InFlightStepState{id: {StepID: id}} }},
		{"foreach_progress", func(s *ExecutionState) { s.ForeachState = map[string]ForeachIterationState{id: {StepID: id}} }},
		{"parallel_progress", func(s *ExecutionState) { s.ParallelState = map[string]ParallelGroupState{id: {StepID: id}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &ExecutionState{RunID: "run-phase-ownership", WorkflowID: workflow.Name}
			tc.set(state)
			// Ownership is checked before interpreting a record's payload,
			// including old or unfamiliar receipt shapes. Test every durable
			// namespace independently so a missing Steps entry is not a bypass.
			before, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			var loaded ExecutionState
			if err := json.Unmarshal(before, &loaded); err != nil {
				t.Fatal(err)
			}
			requirePhaseOwnershipError(t, graph.validatePhaseOwnership(&loaded), id, "steps", "post_pipeline[0]")
			executor := NewExecutor(DefaultExecutorConfig("test"))
			executor.state, executor.graph = &loaded, graph
			if err := executor.applyResumeOptions(workflow, ResumeOptions{Reset: true}); err == nil || !strings.Contains(err.Error(), "shared by workflow phases") {
				t.Fatalf("explicit reset bypassed phase ownership preflight: %v", err)
			}
			after, err := json.Marshal(&loaded)
			if err != nil || string(before) != string(after) {
				t.Fatalf("rejected resume discarded ambiguous durable evidence: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}

func requirePhaseOwnershipError(t *testing.T, errs []DependencyError, id string, owners ...string) {
	t.Helper()
	for _, err := range errs {
		if err.Type != "ambiguous_step_id" || len(err.Steps) != 1 || err.Steps[0] != id {
			continue
		}
		for _, owner := range owners {
			if !strings.Contains(err.Message, owner) {
				t.Fatalf("ownership error does not identify %q: %v", owner, err)
			}
		}
		return
	}
	t.Fatalf("expected ambiguity for runtime step %q, got %v", id, errs)
}

func TestDependencyGraph_Validate_MissingDep(t *testing.T) {

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

// TestDependencyGraph_Validate_NestedBodyDep covers bd-mb6rd: a top-level
// step that depends on the inline child of a parallel/loop container can
// otherwise become ready before the container ran the body, letting
// downstream steps consume missing or stale nested outputs. Validation
// rejects the cross-container edge and tells the operator to depend on the
// container itself.
func TestDependencyGraph_Validate_NestedBodyDep(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{ID: "prep", Prompt: "prep"},
			{
				ID:        "group",
				DependsOn: []string{"prep"},
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "inner", Prompt: "inner"},
				}},
			},
			{ID: "after", DependsOn: []string{"inner"}, Prompt: "after"},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()

	var found *DependencyError
	for i := range errors {
		if errors[i].Type == "nested_body_dep" {
			found = &errors[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected nested_body_dep error, got %v", errors)
	}
	if !(found.Steps[0] == "after" && found.Steps[1] == "inner") {
		t.Errorf("Steps = %v, want [after inner]", found.Steps)
	}
	if !strings.Contains(found.Message, "group") {
		t.Errorf("Message = %q, want it to mention container parent %q", found.Message, "group")
	}
}

// TestDependencyGraph_Validate_NestedBody_SiblingsAllowed asserts that
// intra-container sibling depends_on (parallel children depending on each
// other inside the same group) still validates: bd-mb6rd's rule rejects
// only cross-container references, not legitimate sibling ordering.
func TestDependencyGraph_Validate_NestedBody_SiblingsAllowed(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "group",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "alpha", Prompt: "alpha"},
					{ID: "beta", DependsOn: []string{"alpha"}, Prompt: "beta"},
				}},
			},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()
	for _, e := range errors {
		if e.Type == "nested_body_dep" {
			t.Errorf("intra-container sibling depends_on flagged as nested_body_dep: %v", e)
		}
	}
}

// TestDependencyGraph_Validate_NestedBody_NestedDepsTopLevelAllowed asserts
// that a nested body step depending on a top-level step is fine — the
// rule only fires when the *target* is nested in a container the *referer*
// isn't part of.
func TestDependencyGraph_Validate_NestedBody_NestedDepsTopLevelAllowed(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{ID: "prep", Prompt: "prep"},
			{
				ID: "group",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "inner", DependsOn: []string{"prep"}, Prompt: "inner"},
				}},
			},
		},
	}

	g := NewDependencyGraph(w)
	errors := g.Validate()
	for _, e := range errors {
		if e.Type == "nested_body_dep" {
			t.Errorf("nested-on-top-level depends_on flagged as nested_body_dep: %v", e)
		}
	}
}

func TestDependencyGraph_Validate_Cycle(t *testing.T) {

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

	w := &Workflow{
		Steps: []Step{
			{
				ID: "parallel_group",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "p1", Prompt: "parallel 1"},
					{ID: "p2", Prompt: "parallel 2"},
				}},
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

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	plan := NewDependencyGraph(w).Resolve()

	if !plan.Valid {
		t.Errorf("expected valid plan, got errors: %v", plan.Errors)
	}

	if len(plan.Order) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Order))
	}
}

func TestDependencyGraph_ComplexGraph(t *testing.T) {

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

func (g *DependencyGraph) IsFailed(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.failed[id]
}

func TestDependencyGraph_HasFailedDependency(t *testing.T) {

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

func TestDependencyGraph_ExecutedCount(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b"},
			{ID: "c", Prompt: "step c"},
		},
	}

	g := NewDependencyGraph(w)

	// Initially 0 executed
	if count := g.ExecutedCount(); count != 0 {
		t.Errorf("ExecutedCount() = %d, want 0", count)
	}

	// Mark a as executed
	g.MarkExecuted("a")
	if count := g.ExecutedCount(); count != 1 {
		t.Errorf("ExecutedCount() = %d, want 1", count)
	}

	// Mark b and c as executed
	g.MarkExecuted("b")
	g.MarkExecuted("c")
	if count := g.ExecutedCount(); count != 3 {
		t.Errorf("ExecutedCount() = %d, want 3", count)
	}
}

func TestDependencyGraph_MarkExecuted_OutOfOrderReplay(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b"},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
			{ID: "d", Prompt: "step d", DependsOn: []string{"c"}},
		},
	}

	g := NewDependencyGraph(w)

	if err := g.MarkExecuted("c"); err != nil {
		t.Fatal(err)
	}
	if err := g.MarkExecuted("a"); err != nil {
		t.Fatal(err)
	}
	if err := g.MarkExecuted("b"); err != nil {
		t.Fatal(err)
	}

	ready := g.GetReadySteps()
	if len(ready) != 1 || ready[0] != "d" {
		t.Fatalf("expected only d ready after replaying completed steps, got %v", ready)
	}
}

func TestDependencyGraph_MarkExecuted_Idempotent(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
		},
	}

	g := NewDependencyGraph(w)

	if err := g.MarkExecuted("a"); err != nil {
		t.Fatal(err)
	}
	if err := g.MarkExecuted("a"); err != nil {
		t.Fatal(err)
	}

	if count := g.ExecutedCount(); count != 1 {
		t.Fatalf("ExecutedCount() = %d, want 1", count)
	}

	ready := g.GetReadySteps()
	if len(ready) != 1 || ready[0] != "b" {
		t.Fatalf("expected only b ready after duplicate mark, got %v", ready)
	}
}

func TestFindUnreachable(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a", "nonexistent"}}, // depends on nonexistent
		},
	}

	g := NewDependencyGraph(w)
	errs := g.Validate()

	// Should find that b has a missing dependency
	found := false
	for _, e := range errs {
		if e.Type == "missing_dep" && len(e.Steps) > 0 && e.Steps[0] == "b" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected missing_dep error for step b, got %v", errs)
	}
}

func TestFindUnreachable_AllExist(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"a", "b"}},
		},
	}

	g := NewDependencyGraph(w)
	errs := g.Validate()

	// All dependencies exist, so no missing_dep errors
	for _, e := range errs {
		if e.Type == "missing_dep" {
			t.Errorf("unexpected missing_dep error: %v", e)
		}
	}
}

func TestResolve_WithCycle(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a", DependsOn: []string{"c"}},
			{ID: "b", Prompt: "step b", DependsOn: []string{"a"}},
			{ID: "c", Prompt: "step c", DependsOn: []string{"b"}}, // creates cycle
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if plan.Valid {
		t.Error("Resolve() should return invalid plan for cyclic graph")
	}
	if len(plan.Errors) == 0 {
		t.Error("Resolve() should return errors for cyclic graph")
	}

	// Check that cycle error is present
	found := false
	for _, e := range plan.Errors {
		if e.Type == "cycle" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected cycle error, got %v", plan.Errors)
	}
}

func TestResolve_WithLevels(t *testing.T) {

	w := &Workflow{
		Steps: []Step{
			{ID: "a", Prompt: "step a"},                                // level 0
			{ID: "b", Prompt: "step b"},                                // level 0
			{ID: "c", Prompt: "step c", DependsOn: []string{"a"}},      // level 1
			{ID: "d", Prompt: "step d", DependsOn: []string{"b"}},      // level 1
			{ID: "e", Prompt: "step e", DependsOn: []string{"c", "d"}}, // level 2
		},
	}

	g := NewDependencyGraph(w)
	plan := g.Resolve()

	if !plan.Valid {
		t.Errorf("Resolve() returned invalid plan: %v", plan.Errors)
	}

	// Should have at least 3 levels
	if len(plan.Levels) < 3 {
		t.Errorf("expected at least 3 levels, got %d", len(plan.Levels))
	}

	// First level should have a and b (no deps)
	if len(plan.Levels[0]) != 2 {
		t.Errorf("first level should have 2 steps (a, b), got %d", len(plan.Levels[0]))
	}

	// e should be in order after c and d
	eIdx := -1
	cIdx := -1
	dIdx := -1
	for i, id := range plan.Order {
		switch id {
		case "e":
			eIdx = i
		case "c":
			cIdx = i
		case "d":
			dIdx = i
		}
	}
	if eIdx < cIdx || eIdx < dIdx {
		t.Error("step e should come after c and d in execution order")
	}
}

func BenchmarkDependencyGraphScheduling(b *testing.B) {
	workflow := benchmarkDependencyWorkflow(32, 16)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		graph := NewDependencyGraph(workflow)
		for {
			ready := graph.GetReadySteps()
			if len(ready) == 0 {
				if graph.ExecutedCount() == graph.Size() {
					break
				}
				b.Fatalf("scheduler stalled with %d/%d executed", graph.ExecutedCount(), graph.Size())
			}
			for _, id := range ready {
				if err := graph.MarkExecuted(id); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}

func BenchmarkDependencyGraphSchedulingNaiveBaseline(b *testing.B) {
	workflow := benchmarkDependencyWorkflow(32, 16)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		graph := NewDependencyGraph(workflow)
		for {
			ready := getReadyStepsNaive(graph)
			if len(ready) == 0 {
				if graph.executedCount == graph.Size() {
					break
				}
				b.Fatalf("naive scheduler stalled with %d/%d executed", graph.executedCount, graph.Size())
			}
			for _, id := range ready {
				markExecutedNaive(graph, id)
			}
		}
	}
}

func benchmarkDependencyWorkflow(levels, width int) *Workflow {
	steps := make([]Step, 0, levels*width)
	for level := 0; level < levels; level++ {
		for slot := 0; slot < width; slot++ {
			id := fmt.Sprintf("l%02d_s%02d", level, slot)
			step := Step{ID: id, Prompt: id}
			if level > 0 {
				deps := make([]string, 0, width)
				for depSlot := 0; depSlot < width; depSlot++ {
					deps = append(deps, fmt.Sprintf("l%02d_s%02d", level-1, depSlot))
				}
				step.DependsOn = deps
			}
			steps = append(steps, step)
		}
	}
	return &Workflow{Steps: steps}
}

func getReadyStepsNaive(g *DependencyGraph) []string {
	var ready []string
	for id := range g.steps {
		if g.executed[id] {
			continue
		}

		allDepsExecuted := true
		for _, dep := range g.edges[id] {
			if !g.executed[dep] {
				allDepsExecuted = false
				break
			}
		}

		if allDepsExecuted {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	return ready
}

func markExecutedNaive(g *DependencyGraph, id string) {
	if g.executed[id] {
		return
	}
	g.executed[id] = true
	g.executedCount++
}
