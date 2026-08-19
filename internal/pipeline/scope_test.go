package pipeline

import "testing"

func TestVariableScopeRestoresShadowedLoopLocals(t *testing.T) {
	vars := map[string]interface{}{
		"loop.item":  "outer",
		"loop.index": 2,
		"keep":       "unchanged",
	}

	scope := CaptureVariableScope(vars, loopScopeKeys("file")...)
	vars["loop.file"] = "inner-file"
	vars["loop.item"] = "inner"
	delete(vars, "loop.index")

	scope.Restore(vars)

	if vars["loop.item"] != "outer" {
		t.Fatalf("loop.item = %v, want outer", vars["loop.item"])
	}
	if vars["loop.index"] != 2 {
		t.Fatalf("loop.index = %v, want 2", vars["loop.index"])
	}
	if _, ok := vars["loop.file"]; ok {
		t.Fatalf("loop.file still present after restore: %v", vars["loop.file"])
	}
	if vars["keep"] != "unchanged" {
		t.Fatalf("unrelated variable changed: %v", vars["keep"])
	}
}

func TestLoopExecutorNestedScopesRestoreOuterItem(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	loopExec := NewLoopExecutor(executor)

	outerScope := loopExec.pushLoopVars("item", map[string]interface{}{"id": "outer"}, 0, 1)
	assertSubstituted(t, executor.state, "${item.id}", "outer")

	innerScope := loopExec.pushLoopVars("item", map[string]interface{}{"id": "inner"}, 0, 1)
	assertSubstituted(t, executor.state, "${item.id}", "inner")

	loopExec.popLoopVars(innerScope)
	assertSubstituted(t, executor.state, "${item.id}", "outer")

	loopExec.popLoopVars(outerScope)
	sub := NewSubstitutor(executor.state, "sess", "wf")
	if _, err := sub.Substitute("${item.id}"); err == nil {
		t.Fatal("expected item reference to be unavailable after popping outer scope")
	}
}

func TestLoopExecutorAliasScopeRestoresOuterAlias(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	loopExec := NewLoopExecutor(executor)

	outerScope := loopExec.pushLoopVars("file", "outer.go", 0, 1)
	assertSubstituted(t, executor.state, "${loop.file}", "outer.go")
	assertSubstituted(t, executor.state, "${item}", "outer.go")

	innerScope := loopExec.pushLoopVars("file", "inner.go", 0, 1)
	assertSubstituted(t, executor.state, "${loop.file}", "inner.go")
	assertSubstituted(t, executor.state, "${item}", "inner.go")

	loopExec.popLoopVars(innerScope)
	assertSubstituted(t, executor.state, "${loop.file}", "outer.go")
	assertSubstituted(t, executor.state, "${item}", "outer.go")

	loopExec.popLoopVars(outerScope)
	sub := NewSubstitutor(executor.state, "sess", "wf")
	if _, err := sub.Substitute("${loop.file}"); err == nil {
		t.Fatal("expected alias reference to be unavailable after popping outer scope")
	}
}

func assertSubstituted(t *testing.T, state *ExecutionState, input, want string) {
	t.Helper()
	sub := NewSubstitutor(state, "sess", "wf")
	got, err := sub.Substitute(input)
	if err != nil {
		t.Fatalf("Substitute(%q) returned error: %v", input, err)
	}
	if got != want {
		t.Fatalf("Substitute(%q) = %q, want %q", input, got, want)
	}
}

// A branch's restore must undo only its own writes. A branch is reachable from a
// `parallel: true` foreach body, so a whole-map restore discarded every
// output_var and steps.<id>.* key a sibling iteration wrote while the branch ran.
func TestRestoreBranchVariablesLeavesConcurrentSiblingWrites(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{
		"global": "before",
		"keep":   "same",
	}}
	snapshot := captureAllVariables(state.Variables)

	// The branch body writes its own keys and mutates a pre-existing one.
	bodyStepIDs := []string{"route_child_1", "route_child_2"}
	ownedOutputVars := branchBodyOutputVars([]Step{{ID: "emit", OutputVar: "branch_local"}})
	state.Variables["global"] = "branch-local"
	state.Variables["branch_local"] = "body output"
	state.Variables["steps.route_child_1.output"] = "body step output"
	state.Variables["steps.route_child_1_on_success_1.output"] = "nested output"

	// Meanwhile, concurrent sibling foreach iterations write their own keys.
	state.Variables["produced_c"] = "sibling output"
	state.Variables["steps.produce_iter_3.output"] = "sibling step output"
	state.Variables["runtime.route_failure_action"] = "fallback_to_ntm_inbox"

	restoreBranchVariables(state, snapshot, bodyStepIDs, ownedOutputVars)

	// Branch-owned keys are gone and mutations reverted.
	if state.Variables["global"] != "before" {
		t.Fatalf("global = %v, want before", state.Variables["global"])
	}
	if state.Variables["keep"] != "same" {
		t.Fatalf("keep = %v, want same", state.Variables["keep"])
	}
	for _, key := range []string{
		"branch_local",
		"steps.route_child_1.output",
		"steps.route_child_1_on_success_1.output",
	} {
		if _, ok := state.Variables[key]; ok {
			t.Fatalf("branch-owned key %q leaked after restore", key)
		}
	}

	// Sibling and global-signaling keys survive.
	for key, want := range map[string]interface{}{
		"produced_c":                   "sibling output",
		"steps.produce_iter_3.output":  "sibling step output",
		"runtime.route_failure_action": "fallback_to_ntm_inbox",
	} {
		got, ok := state.Variables[key]
		if !ok {
			t.Fatalf("concurrent sibling key %q was destroyed by the branch restore", key)
		}
		if got != want {
			t.Fatalf("%q = %v, want %v", key, got, want)
		}
	}
}

// Ownership must be decided by the executed body step IDs, not by a loose prefix
// on the branch step ID, or an unrelated sibling step whose name merely starts
// with the same text would be swept away.
func TestBranchOwnsVariableRequiresRecordedBodyStepID(t *testing.T) {
	bodyStepIDs := []string{"route_child_1"}
	owned := map[string]struct{}{"declared": {}}

	cases := map[string]bool{
		"declared":                                true,
		"steps.route_child_1.output":              true,
		"steps.route_child_1.data":                true,
		"steps.route_child_1_on_success_1.output": true,
		"steps.route_child_2.output":              false, // never executed
		"steps.route_sibling.output":              false, // unrelated step
		"steps.produce_iter_3.output":             false, // concurrent sibling
		"runtime.route_failure_action":            false, // global signaling
		"unrelated":                               false,
	}
	for key, want := range cases {
		if got := branchOwnsVariable(key, bodyStepIDs, owned); got != want {
			t.Fatalf("branchOwnsVariable(%q) = %t, want %t", key, got, want)
		}
	}
}
