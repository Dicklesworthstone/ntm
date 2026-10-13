package pipeline

import (
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func dependencyAdmissionWorkflow() *Workflow {
	return &Workflow{SchemaVersion: SchemaVersion, Name: "container-prerequisites", Steps: []Step{
		{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "inner", Parallel: ParallelSpec{Steps: []Step{
				{ID: "consume", Command: "true", DependsOn: []string{"prepare"}},
			}}},
		}}},
		{ID: "prepare", Command: "true"},
		{ID: "after", Command: "true", DependsOn: []string{"group"}},
		{ID: "independent", Command: "true"},
	}}
}

func TestContainerDependenciesGateEveryEnclosingGroup(t *testing.T) {
	workflow := dependencyAdmissionWorkflow()
	before, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	graph := NewDependencyGraph(workflow)
	if problems := graph.Validate(); len(problems) != 0 {
		t.Fatal(problems)
	}
	for _, id := range []string{"group", "inner", "consume"} {
		if slices.Contains(graph.GetReadySteps(), id) {
			t.Fatalf("%s ready before its external prerequisite: %v", id, graph.GetReadySteps())
		}
		if !slices.Contains(graph.GetDependencies(id), "prepare") {
			t.Fatalf("%s lost prerequisite: %v", id, graph.GetDependencies(id))
		}
	}
	if !slices.Contains(graph.GetReadySteps(), "independent") {
		t.Fatal("unrelated work was serialized")
	}
	if err := graph.MarkExecuted("prepare"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"group", "inner", "consume"} {
		if !slices.Contains(graph.GetReadySteps(), id) {
			t.Fatalf("%s was not released by prepare", id)
		}
	}
	if slices.Contains(graph.GetReadySteps(), "after") {
		t.Fatal("downstream work skipped the group boundary")
	}
	after, err := json.Marshal(workflow)
	if err != nil || string(before) != string(after) {
		t.Fatal("graph construction rewrote authored workflow")
	}
}

func TestContainerDependenciesIncludeLoopBodies(t *testing.T) {
	workflow := dependencyAdmissionWorkflow()
	workflow.Steps[0].Parallel.Steps[0] = Step{ID: "loop", Loop: &LoopConfig{Steps: []Step{
		{ID: "consume", Command: "true", DependsOn: []string{"prepare"}},
	}}}
	graph := NewDependencyGraph(workflow)
	for _, id := range []string{"group", "loop"} {
		if !reflect.DeepEqual(graph.GetDependencies(id), []string{"prepare"}) {
			t.Fatalf("%s: %v", id, graph.GetDependencies(id))
		}
		if slices.Contains(graph.GetReadySteps(), id) {
			t.Fatalf("%s admitted too early", id)
		}
	}
}

func TestContainerDependenciesPropagatePrerequisiteFailure(t *testing.T) {
	graph := NewDependencyGraph(dependencyAdmissionWorkflow())
	if err := graph.MarkFailed("prepare"); err != nil {
		t.Fatal(err)
	}
	if err := graph.MarkExecuted("prepare"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"group", "inner", "consume"} {
		if !graph.HasFailedDependency(id) {
			t.Fatalf("%s could dispatch after external failure", id)
		}
		if !reflect.DeepEqual(graph.GetFailedDependencies(id), []string{"prepare"}) {
			t.Fatalf("%s: %v", id, graph.GetFailedDependencies(id))
		}
	}
	if graph.HasFailedDependency("independent") {
		t.Fatal("unrelated work inherited failure")
	}
}

func TestContainerDependenciesRejectImplicitCycles(t *testing.T) {
	for _, name := range []string{"dependent_consumer", "own_parent", "two_groups"} {
		t.Run(name, func(t *testing.T) {
			workflow := dependencyAdmissionWorkflow()
			switch name {
			case "dependent_consumer":
				workflow.Steps[1].DependsOn = []string{"group"}
			case "own_parent":
				workflow.Steps[0].Parallel.Steps[0].Parallel.Steps[0].DependsOn = []string{"group"}
			case "two_groups":
				workflow.Steps[1] = Step{ID: "prepare", Parallel: ParallelSpec{Steps: []Step{
					{ID: "other", Command: "true", DependsOn: []string{"group"}},
				}}}
			}
			plan := NewDependencyGraph(workflow).Resolve()
			if plan.Valid {
				t.Fatalf("accepted an execution cycle: %+v", plan)
			}
			found := false
			for _, problem := range plan.Errors {
				found = found || problem.Type == "cycle"
			}
			if !found {
				t.Fatalf("missing cycle diagnostic: %+v", plan.Errors)
			}
		})
	}
}

func TestContainerDependenciesDeduplicateWithoutMutatingBackingArrays(t *testing.T) {
	workflow := dependencyAdmissionWorkflow()
	backing := []string{"prepare", "sentinel", "unused"}
	workflow.Steps[0].DependsOn = backing[:1:3]
	workflow.Steps[0].Parallel.Steps = append(workflow.Steps[0].Parallel.Steps,
		Step{ID: "other", Command: "true", DependsOn: []string{"prepare", "second"}},
		Step{ID: "third", Command: "true", DependsOn: []string{"second"}},
	)
	workflow.Steps = append(workflow.Steps, Step{ID: "second", Command: "true"})
	for range 10 {
		graph := NewDependencyGraph(workflow)
		if !reflect.DeepEqual(graph.GetDependencies("group"), []string{"prepare", "second"}) {
			t.Fatal(graph.GetDependencies("group"))
		}
		if !reflect.DeepEqual(backing, []string{"prepare", "sentinel", "unused"}) {
			t.Fatalf("modified authored backing array: %v", backing)
		}
		for _, dep := range []string{"prepare", "second"} {
			count := 0
			for _, id := range graph.GetDependents(dep) {
				if id == "group" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%s has %d reverse group edges", dep, count)
			}
		}
		if err := graph.MarkExecuted("prepare"); err != nil {
			t.Fatal(err)
		}
		if err := graph.MarkExecuted("prepare"); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(graph.GetReadySteps(), "group") {
			t.Fatal("duplicate completion released another prerequisite")
		}
		if err := graph.MarkExecuted("second"); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(graph.GetReadySteps(), "group") {
			t.Fatal("deduplicated prerequisites did not release group")
		}
	}
}

func TestContainerDependenciesDoNotHoistSiblingOrForeignBodyEdges(t *testing.T) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "siblings", Steps: []Step{
		{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "first", Command: "true"},
			{ID: "second", Command: "true", DependsOn: []string{"first"}},
		}}},
		{ID: "other", Parallel: ParallelSpec{Steps: []Step{{ID: "foreign", Command: "true"}}}},
	}}
	graph := NewDependencyGraph(workflow)
	if errors := graph.Validate(); len(errors) != 0 {
		t.Fatal(errors)
	}
	if len(graph.GetDependencies("group")) != 0 {
		t.Fatal("group cannot wait for a child it has not started")
	}
	workflow.Steps[0].Parallel.Steps[1].DependsOn = []string{"foreign"}
	graph = NewDependencyGraph(workflow)
	found := false
	for _, problem := range graph.Validate() {
		found = found || problem.Type == "nested_body_dep"
	}
	if !found {
		t.Fatal("cross-container body edge was silently legalized")
	}
	workflow.Steps[0].Parallel.Steps[1].DependsOn = []string{"missing"}
	graph = NewDependencyGraph(workflow)
	found = false
	for _, problem := range graph.Validate() {
		found = found || problem.Type == "missing_dep"
	}
	if !found {
		t.Fatal("missing dependency diagnostic disappeared")
	}
}

func TestContainerDependenciesConcurrentCompletionIsIdempotent(t *testing.T) {
	graph := NewDependencyGraph(dependencyAdmissionWorkflow())
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if err := graph.MarkExecuted("prepare"); err != nil {
					t.Error(err)
				}
				_ = graph.GetReadySteps()
			}
		}()
	}
	wg.Wait()
	if graph.ExecutedCount() != 1 {
		t.Fatal(graph.ExecutedCount())
	}
	for _, id := range []string{"group", "inner", "consume"} {
		if !slices.Contains(graph.GetReadySteps(), id) {
			t.Fatalf("%s not released", id)
		}
	}
}
