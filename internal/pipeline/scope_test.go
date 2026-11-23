package pipeline

import (
	"context"
	"testing"
)

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
	root := context.Background()
	outer := withLoopVars(root, "item", map[string]interface{}{"id": "outer"}, 0, 1)
	assertSubstituted(t, executor, outer, "${item.id}", "outer")

	inner := withLoopVars(outer, "item", map[string]interface{}{"id": "inner"}, 0, 1)
	assertSubstituted(t, executor, inner, "${item.id}", "inner")
	assertSubstituted(t, executor, inner, "${loop.item.id}", "inner")
	assertSubstituted(t, executor, outer, "${item.id}", "outer")

	if _, err := executor.substituteVariablesStrictCtx(root, "${item.id}"); err == nil {
		t.Fatal("expected item reference to be unavailable outside the loop context")
	}
	if len(executor.state.Variables) != 0 || len(executor.state.ScopeStack) != 0 {
		t.Fatal("iteration bindings leaked into shared execution state")
	}
}

func TestLoopExecutorAliasScopeRestoresOuterAlias(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	root := context.Background()
	outer := withLoopVars(root, "file", "outer.go", 0, 1)
	assertSubstituted(t, executor, outer, "${loop.file}", "outer.go")
	assertSubstituted(t, executor, outer, "${item}", "outer.go")

	inner := withLoopVars(outer, "file", "inner.go", 0, 1)
	assertSubstituted(t, executor, inner, "${loop.file}", "inner.go")
	assertSubstituted(t, executor, inner, "${item}", "inner.go")
	assertSubstituted(t, executor, outer, "${loop.file}", "outer.go")
	assertSubstituted(t, executor, outer, "${item}", "outer.go")

	if _, err := executor.substituteVariablesStrictCtx(root, "${loop.file}"); err == nil {
		t.Fatal("expected alias reference to be unavailable outside the loop context")
	}
}

func assertSubstituted(t *testing.T, executor *Executor, ctx context.Context, input, want string) {
	t.Helper()
	got, err := executor.substituteVariablesStrictCtx(ctx, input)
	if err != nil {
		t.Fatalf("Substitute(%q) returned error: %v", input, err)
	}
	if got != want {
		t.Fatalf("Substitute(%q) = %q, want %q", input, got, want)
	}
}

func TestLoopContextNestedPathsShadowSharedValues(t *testing.T) {
	t.Parallel()
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables: map[string]interface{}{
			"loop.file": map[string]interface{}{"name": "shared", "missing": "must not leak"},
			"loop.item": "shared",
		},
		Steps: map[string]StepResult{},
	}
	item := map[string]interface{}{"name": "local", "children": []interface{}{"first", "second"}}
	ctx := withLoopVars(context.Background(), "file", item, 2, 3)
	for input, want := range map[string]string{
		"${item.name}":             "local",
		"${loop.item.name}":        "local",
		"${loop.file.name}":        "local",
		"${loop.file.children[1]}": "second",
		"${item.children[0]}":      "first",
		"${loop.index}":            "2",
		"${loop.count}":            "3",
		"${loop.first}":            "false",
		"${loop.last}":             "true",
	} {
		assertSubstituted(t, executor, ctx, input, want)
	}
	if _, err := executor.substituteVariablesStrictCtx(ctx, "${loop.file.missing}"); err == nil {
		t.Fatal("missing local field fell through to shared loop value")
	}
	assertSubstituted(t, executor, context.Background(), "${loop.file.name}", "shared")
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
	// A real branch can shadow a named value only by declaring that output.
	// Unrelated pre-existing keys may belong to concurrently running siblings.
	ownedOutputVars := branchBodyOutputVars([]Step{
		{ID: "emit", OutputVar: "branch_local"},
		{ID: "shadow", OutputVar: "global"},
	})
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
