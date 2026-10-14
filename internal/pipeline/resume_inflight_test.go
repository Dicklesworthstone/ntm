package pipeline

import "testing"

// A command can persist its result while its enclosing lifecycle (for example
// success hooks) is still in flight. That result must not satisfy graph edges
// or republish output aliases before the lifecycle has actually settled.
func TestResumeInFlightCompletionNeverSatisfiesDependencies(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "top_level"
		if scoped {
			name = "parallel_child"
		}
		t.Run(name, func(t *testing.T) {
			producer := Step{ID: "producer", OutputVar: "named"}
			consumer := Step{ID: "consumer", DependsOn: []string{"producer"}}
			workflow := &Workflow{Steps: []Step{producer, consumer}}
			id := "producer"
			if scoped {
				workflow.Steps = []Step{{ID: "group", Parallel: ParallelSpec{Steps: []Step{producer, consumer}}}}
				id = "group_producer"
			}
			e := &Executor{
				graph: NewDependencyGraph(workflow),
				state: &ExecutionState{
					Steps:         map[string]StepResult{id: {StepID: id, Status: StatusCompleted, Output: "unsettled"}},
					InFlightSteps: map[string]InFlightStepState{id: {}},
					Variables: map[string]interface{}{
						"named": "unsettled", "named_parsed": "stale",
						"steps." + id + ".output": "unsettled", "steps." + id + ".data": "stale",
					},
				},
			}
			e.applyResumeState()
			if e.graph.IsExecuted("producer") {
				t.Error("in-flight producer marked executed")
			}
			for _, ready := range e.graph.GetReadySteps() {
				if ready == "consumer" {
					t.Error("consumer released by in-flight producer")
				}
			}
			if _, exists := e.state.Steps[id]; exists {
				t.Fatal("in-flight result retained")
			}
			for _, key := range []string{"named", "named_parsed", "steps." + id + ".output", "steps." + id + ".data"} {
				if _, exists := e.state.Variables[key]; exists {
					t.Errorf("in-flight output restored: %s", key)
				}
			}
		})
	}
}
