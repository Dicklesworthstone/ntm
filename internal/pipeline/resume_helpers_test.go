package pipeline

import (
	"context"
	"reflect"
	"testing"
)

func TestResumeResetClearsDurableWorkStateAndStepVariables(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:       "run-reset",
		WorkflowID:  "resume-reset",
		Status:      StatusRunning,
		CurrentStep: "fanout",
		Steps: map[string]StepResult{
			"top": {StepID: "top", Status: StatusCompleted, Output: "old"},
		},
		Variables: map[string]interface{}{
			"input":                "keep",
			"top_out":              "drop",
			"top_out_parsed":       map[string]interface{}{"drop": true},
			"parallel_out":         "drop",
			"loop_out":             "drop",
			"foreach_out":          "drop",
			"foreach_pane_out":     "drop",
			"steps.top.output":     "drop",
			"steps.top.parsed.foo": "drop",
		},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {StepID: "fanout", CurrentIteration: 2, Total: 4},
		},
		ParallelState: map[string]ParallelGroupState{
			"group": {StepID: "group", Total: 2, InFlightStepIDs: []string{"child"}},
		},
		ScopeStack:    []ScopeFrame{{Kind: StepKindLoop, Name: "item"}},
		InFlightSteps: map[string]InFlightStepState{"fanout": {StepID: "fanout", Kind: "foreach"}},
		Errors:        []ExecutionError{{StepID: "top", Message: "old failure"}},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-reset",
		Steps: []Step{{
			ID:        "top",
			OutputVar: "top_out",
			Parallel:  ParallelSpec{Steps: []Step{{ID: "parallel-child", OutputVar: "parallel_out"}}},
			Loop: &LoopConfig{
				Steps: []Step{{ID: "loop-child", OutputVar: "loop_out"}},
			},
			Foreach:     &ForeachConfig{Steps: []Step{{ID: "foreach-child", OutputVar: "foreach_out"}}},
			ForeachPane: &ForeachConfig{Steps: []Step{{ID: "foreach-pane-child", OutputVar: "foreach_pane_out"}}},
		}},
	}

	executor.resetResumeState(workflow)

	if executor.state.CurrentStep != "" {
		t.Fatalf("CurrentStep = %q, want empty", executor.state.CurrentStep)
	}
	if len(executor.state.Steps) != 0 {
		t.Fatalf("Steps = %#v, want empty map", executor.state.Steps)
	}
	if executor.state.ForeachState != nil || executor.state.ParallelState != nil || executor.state.ScopeStack != nil || executor.state.InFlightSteps != nil || executor.state.Errors != nil {
		t.Fatalf("resume bookkeeping not cleared: foreach=%#v parallel=%#v scopes=%#v in_flight=%#v errors=%#v",
			executor.state.ForeachState, executor.state.ParallelState, executor.state.ScopeStack, executor.state.InFlightSteps, executor.state.Errors)
	}
	if got := executor.state.Variables["input"]; got != "keep" {
		t.Fatalf("input variable = %#v, want preserved", got)
	}
	for _, key := range []string{
		"top_out", "top_out_parsed", "parallel_out", "loop_out",
		"foreach_out", "foreach_pane_out", "steps.top.output", "steps.top.parsed.foo",
	} {
		if _, ok := executor.state.Variables[key]; ok {
			t.Fatalf("variable %q survived reset: %#v", key, executor.state.Variables)
		}
	}
}

func TestForceResumeIterationPrunesFutureIterationState(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-force",
		WorkflowID: "resume-force",
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"fanout_iter0":      {StepID: "fanout_iter0", Status: StatusCompleted},
			"fanout_iter1":      {StepID: "fanout_iter1", Status: StatusCompleted},
			"fanout_iter2":      {StepID: "fanout_iter2", Status: StatusCompleted},
			"fanout_iter3_work": {StepID: "fanout_iter3_work", Status: StatusCompleted},
			"other_iter9":       {StepID: "other_iter9", Status: StatusCompleted},
		},
		Variables: map[string]interface{}{},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:                "fanout",
				CurrentIteration:      4,
				Total:                 5,
				CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3", "other_iter0", "fanout_iterx"},
			},
		},
	}

	executor.forceResumeIteration("fanout", 2)

	state := executor.state.ForeachState["fanout"]
	if state.CurrentIteration != 2 {
		t.Fatalf("CurrentIteration = %d, want 2", state.CurrentIteration)
	}
	wantCompleted := []string{"fanout_iter0", "fanout_iter1"}
	if !reflect.DeepEqual(state.CompletedIterationIDs, wantCompleted) {
		t.Fatalf("CompletedIterationIDs = %#v, want %#v", state.CompletedIterationIDs, wantCompleted)
	}
	for _, removed := range []string{"fanout_iter2", "fanout_iter3_work"} {
		if _, ok := executor.state.Steps[removed]; ok {
			t.Fatalf("future step %q survived force iteration: %#v", removed, executor.state.Steps)
		}
	}
	for _, kept := range []string{"fanout_iter0", "fanout_iter1", "other_iter9"} {
		if _, ok := executor.state.Steps[kept]; !ok {
			t.Fatalf("step %q was removed unexpectedly: %#v", kept, executor.state.Steps)
		}
	}
}

func TestResumeProgressBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-bookkeeping",
		WorkflowID: "resume-bookkeeping",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.markStepInFlight("fanout", "foreach", 3)
	if got := executor.state.InFlightSteps["fanout"]; got.StepID != "fanout" || got.Kind != "foreach" || got.Iteration != 3 {
		t.Fatalf("in-flight state = %#v, want fanout foreach iteration 3", got)
	}
	executor.clearStepInFlight("fanout")
	if executor.state.InFlightSteps != nil {
		t.Fatalf("InFlightSteps = %#v, want nil after clearing last entry", executor.state.InFlightSteps)
	}

	start := executor.beginForeachState("fanout", 4)
	if start != 0 {
		t.Fatalf("initial foreach start = %d, want 0", start)
	}
	executor.markForeachIterationCompleted("fanout", 0, 4)
	executor.markForeachIterationCompleted("fanout", 2, 4)
	// bd-p12ti: completed=[iter0, iter2] has a gap at iteration 1.
	// markForeachIterationCompleted(2) bumps CurrentIteration to 3, but the
	// durable completed set is authoritative — beginForeachState must start
	// at the first gap (1), not jump past it to the cursor.
	start = executor.beginForeachState("fanout", 4)
	if start != 1 {
		t.Fatalf("gap-aware foreach start = %d, want 1 (skip past gap is unsafe)", start)
	}
	if got := executor.state.ForeachState["fanout"].CurrentIteration; got != 1 {
		t.Fatalf("CurrentIteration after gap-aware begin = %d, want 1", got)
	}
	// bd-p12ti regression guard: with no gaps and CurrentIteration ahead,
	// resume legitimately starts at CurrentIteration.
	executor.markForeachIterationCompleted("fanout", 1, 4)
	start = executor.beginForeachState("fanout", 4)
	if start != 3 {
		t.Fatalf("contiguous-complete foreach start = %d, want 3", start)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter2"}, "fanout", 4); got != 1 {
		t.Fatalf("first gap incomplete iteration = %d, want 1", got)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3"}, "fanout", 4); got != 4 {
		t.Fatalf("all-complete first incomplete = %d, want 4", got)
	}

	if iterationSucceeded([]StepResult{{Status: StatusCompleted}, {Status: StatusSkipped}}, false) != true {
		t.Fatal("completed/skipped iteration was not treated as successful")
	}
	if iterationSucceeded([]StepResult{{Status: StatusCompleted}}, true) != false {
		t.Fatal("break-controlled iteration was treated as successful")
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldCompleteForeachIteration(cancelledCtx, nil, false) {
		t.Fatal("empty cancelled iteration was treated as completed")
	}
	if !shouldCompleteForeachIteration(cancelledCtx, []StepResult{{Status: StatusCompleted}}, false) {
		t.Fatal("completed iteration was not checkpointed after late cancellation")
	}
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled, StatusRunning, StatusPending} {
		if iterationSucceeded([]StepResult{{Status: status}}, false) {
			t.Fatalf("iteration status %s was treated as successful", status)
		}
	}
}

func TestResumeParallelAndScopeBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-parallel",
		WorkflowID: "resume-parallel",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.beginParallelState("group", 3)
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "b")
	executor.markParallelSubstepFinished("group", "a", StatusCompleted)
	executor.markParallelSubstepFinished("group", "b", StatusFailed)
	executor.completeParallelState("group")

	group := executor.state.ParallelState["group"]
	if !group.AllSubstepsSettled || group.CompletedAt.IsZero() {
		t.Fatalf("parallel group was not completed: %#v", group)
	}
	if !reflect.DeepEqual(group.CompletedStepIDs, []string{"a"}) {
		t.Fatalf("CompletedStepIDs = %#v, want [a]", group.CompletedStepIDs)
	}
	if !reflect.DeepEqual(group.FailedStepIDs, []string{"b"}) {
		t.Fatalf("FailedStepIDs = %#v, want [b]", group.FailedStepIDs)
	}
	if len(group.InFlightStepIDs) != 0 {
		t.Fatalf("InFlightStepIDs = %#v, want empty", group.InFlightStepIDs)
	}

	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindLoop, Name: "row"})
	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindParallel, Name: "group"})
	executor.popScopeFrameLocked()
	if got := executor.state.ScopeStack; len(got) != 1 || got[0].Name != "row" {
		t.Fatalf("ScopeStack after pop = %#v, want only row frame", got)
	}
	executor.popScopeFrameLocked()
	executor.popScopeFrameLocked()
	if len(executor.state.ScopeStack) != 0 {
		t.Fatalf("ScopeStack = %#v, want empty", executor.state.ScopeStack)
	}
}
