package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParallelChildDAGGraphResolvesScopedSiblingAliases(t *testing.T) {
	workflow := &Workflow{Steps: []Step{{ID: "group", Parallel: ParallelSpec{Steps: []Step{
		{ID: "consumer", DependsOn: []string{"producer", "group_producer"}},
		{ID: "producer"},
	}}}}}
	before, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	g := NewDependencyGraph(workflow)
	plan := g.Resolve()
	if !plan.Valid {
		t.Fatalf("scoped dependency rejected: %+v", plan.Errors)
	}
	if got := g.edges["consumer"]; !reflect.DeepEqual(got, []string{"producer"}) {
		t.Fatalf("canonical edges=%v", got)
	}
	if g.inDegree["consumer"] != 1 || !reflect.DeepEqual(g.reverse["producer"], []string{"consumer"}) {
		t.Fatalf("dependency counted twice: degree=%d reverse=%v", g.inDegree["consumer"], g.reverse["producer"])
	}
	if err := g.MarkExecuted("producer"); err != nil {
		t.Fatal(err)
	}
	if g.remainingDeps["consumer"] != 0 {
		t.Fatalf("producer did not release consumer: %d", g.remainingDeps["consumer"])
	}
	after, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("graph resolution mutated workflow/hash input")
	}
}

func TestParallelChildDAGGraphNestedAliasesPreserveExternalGates(t *testing.T) {
	workflow := &Workflow{Steps: []Step{
		{ID: "external"},
		{ID: "outer", Parallel: ParallelSpec{Steps: []Step{
			{ID: "inner", Parallel: ParallelSpec{Steps: []Step{
				{ID: "consumer", DependsOn: []string{"outer_inner_producer"}},
				{ID: "producer", DependsOn: []string{"external"}},
			}}},
		}}},
	}}
	g := NewDependencyGraph(workflow)
	if plan := g.Resolve(); !plan.Valid {
		t.Fatalf("nested graph: %+v", plan.Errors)
	}
	if !reflect.DeepEqual(g.edges["consumer"], []string{"producer"}) {
		t.Fatalf("nested edge=%v", g.edges["consumer"])
	}
	for _, owner := range []string{"outer", "inner", "producer"} {
		if !reflect.DeepEqual(g.edges[owner], []string{"external"}) {
			t.Errorf("%s external gate=%v", owner, g.edges[owner])
		}
		if g.remainingDeps[owner] != 1 {
			t.Errorf("%s unexpectedly ready", owner)
		}
	}
	if err := g.MarkExecuted("external"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"outer", "inner", "producer"} {
		if g.remainingDeps[owner] != 0 {
			t.Errorf("external did not release %s", owner)
		}
	}
}

func TestParallelChildDAGGraphRejectsAmbiguousAndCyclicAliases(t *testing.T) {
	cases := []struct {
		name     string
		children []Step
		roots    []Step
	}{
		{"duplicate runtime", []Step{{ID: "producer"}, {ID: "group_producer"}}, nil},
		{"duplicate author", []Step{{ID: "producer"}, {ID: "producer"}}, nil},
		{"owner collision", []Step{{ID: "group"}}, nil},
		{"external collision", []Step{{ID: "producer"}}, []Step{{ID: "group_producer"}}},
		{"scoped self cycle", []Step{{ID: "producer", DependsOn: []string{"group_producer"}}}, nil},
		{"mixed cycle", []Step{{ID: "one", DependsOn: []string{"group_two"}}, {ID: "two", DependsOn: []string{"one"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workflow := &Workflow{Steps: append([]Step{{ID: "group", Parallel: ParallelSpec{Steps: tc.children}}}, tc.roots...)}
			if plan := NewDependencyGraph(workflow).Resolve(); plan.Valid {
				t.Fatalf("ambiguous graph accepted: %+v", plan)
			}
		})
	}
}

func TestParallelChildDAGGraphKeepsCrossContainerRejection(t *testing.T) {
	for _, dep := range []string{"producer", "first_producer"} {
		workflow := &Workflow{Steps: []Step{
			{ID: "first", Parallel: ParallelSpec{Steps: []Step{{ID: "producer"}}}},
			{ID: "second", Parallel: ParallelSpec{Steps: []Step{{ID: "consumer", DependsOn: []string{dep}}}}},
		}}
		if plan := NewDependencyGraph(workflow).Resolve(); plan.Valid {
			t.Fatalf("cross-container %q reference admitted", dep)
		}
	}
}

func TestParallelChildDAGRetentionExcludesUnsettledWork(t *testing.T) {
	complete := StepResult{StepID: "group_child", Status: StatusCompleted}
	for _, tc := range []struct {
		name     string
		child    Step
		result   StepResult
		inFlight bool
		want     bool
	}{
		{"settled child", Step{ID: "group_child"}, complete, false, true},
		{"in-flight child", Step{ID: "group_child"}, complete, true, false},
		{"background launch", Step{ID: "group_child", Command: "worker", Wait: WaitNone}, complete, false, false},
		{"ordinary command", Step{ID: "group_child", Command: "worker"}, complete, false, true},
		{"submitted agent", Step{ID: "group_child", Wait: WaitNone}, complete, false, true},
		{"needs replay", Step{ID: "group_child"}, StepResult{Status: StatusCompleted, RerunOnResume: true}, false, false},
		{"failed dependency", Step{ID: "group_child"}, StepResult{Status: StatusSkipped, SkipKind: SkipKindFailedDependency}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retainParallelChild(&tc.child, tc.result, tc.inFlight); got != tc.want {
				t.Fatalf("retain=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestParallelChildDAGMergePreservesOwnedForeachAggregate(t *testing.T) {
	for _, paneMode := range []bool{false, true} {
		child := Step{ID: "each", OutputVar: "values", Foreach: &ForeachConfig{}}
		if paneMode {
			child.Foreach = nil
			child.ForeachPane = &ForeachConfig{}
		}
		parent := Step{ID: "group", Parallel: ParallelSpec{Steps: []Step{child}}}
		values := []string{"one", "two"}
		parsed := []interface{}{map[string]interface{}{"value": 1}}
		e := &Executor{state: &ExecutionState{Variables: map[string]interface{}{"values": values, "values_parsed": parsed}}}
		e.storeParallelOutputVars(&parent, []StepResult{{StepID: "group_each", Status: StatusCompleted, Output: "Foreach completed"}}, nil)
		if !reflect.DeepEqual(e.state.Variables["values"], values) || !reflect.DeepEqual(e.state.Variables["values_parsed"], parsed) {
			t.Fatalf("foreach data was replaced by a status summary: %#v", e.state.Variables)
		}
	}
}
