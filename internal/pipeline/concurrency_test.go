package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// A waits inside iteration zero until B reaches iteration one. The next A
// query must still receive A's item/index, even while B's body remains active.
// The channels control execution order at the external query boundary; no
// sleeps or scheduler luck are needed to expose shared loop-variable writes.
func TestParallelLoopsKeepIterationBindingsIsolated(t *testing.T) {
	t.Parallel()
	aEntered, bEntered, aRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var observedMu sync.Mutex
	observed := make(map[string]int)
	cfg := DefaultExecutorConfig("parallel-loop-bindings")
	cfg.ProjectDir = t.TempDir()
	cfg.GlobalTimeout = 10 * time.Second
	cfg.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		label := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--label" {
				label = args[i+1]
			}
		}
		wait := func(ready <-chan struct{}) error {
			select {
			case <-ready:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var err error
		switch label {
		case "a-enter":
			close(aEntered)
			err = wait(bEntered)
		case "b-before":
			err = wait(aEntered)
		case "b-enter":
			close(bEntered)
			err = wait(aRead)
		default:
			observedMu.Lock()
			observed[label]++
			observedMu.Unlock()
			if strings.HasPrefix(label, "a:") {
				close(aRead)
			}
		}
		return []byte(`{"issues":[]}`), err
	}
	query := func(id, label string) Step {
		return Step{ID: id, BeadQuery: &BeadQueryStep{Label: StringOrList{label}}}
	}
	probe := "${item.name}:${loop.file.name}:${loop.index}:${loop.count}:${loop.first}:${loop.last}:${vars.stable}"
	bGate := query("gate", "b-enter")
	bGate.When = "${loop.index} == 1"
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-loop-bindings",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "a", Loop: &LoopConfig{
				Items: `[{"name":"alpha"}]`, As: "file",
				Steps: []Step{query("gate", "a-enter"), query("inspect", "a:"+probe)},
			}},
			query("b_pre", "b-before"),
			{ID: "b", DependsOn: []string{"b_pre"}, Loop: &LoopConfig{
				Items: `[{"name":"beta0"},{"name":"beta1"}]`, As: "file",
				Steps: []Step{bGate, query("inspect", "b:"+probe)},
			}},
		},
	}
	globals := map[string]interface{}{"stable": "unchanged", "loop.item": "global", "loop.index": 99}
	state, runErr := NewExecutor(cfg).Run(context.Background(), workflow, globals, nil)
	observedMu.Lock()
	defer observedMu.Unlock()
	want := map[string]int{
		"a:alpha:alpha:0:1:true:true:unchanged":  1,
		"b:beta0:beta0:0:2:true:false:unchanged": 1,
		"b:beta1:beta1:1:2:false:true:unchanged": 1,
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("parallel loop queries consumed another iteration's bindings: got %#v, want %#v; run error=%v", observed, want, runErr)
	}
	if runErr != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("Run() = %+v, %v", state, runErr)
	}
	for key, want := range globals {
		if !reflect.DeepEqual(state.Variables[key], want) {
			t.Fatalf("global %s changed: got %#v, want %#v", key, state.Variables[key], want)
		}
	}
	for _, key := range []string{"loop.file", "loop.count", "loop.first", "loop.last"} {
		if _, ok := state.Variables[key]; ok {
			t.Fatalf("local key %s leaked into shared variables", key)
		}
	}
	if len(state.ScopeStack) != 0 || len(state.InFlightSteps) != 0 {
		t.Fatalf("completed loops retained active scopes/iterations: scopes=%+v, in_flight=%+v", state.ScopeStack, state.InFlightSteps)
	}
	persisted, err := LoadState(cfg.ProjectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.ForeachState["a"].CompletedIterationIDs) != 1 || len(persisted.ForeachState["b"].CompletedIterationIDs) != 2 {
		t.Fatalf("isolating variables lost durable iteration progress: %+v", persisted.ForeachState)
	}
	if persisted.Variables["stable"] != "unchanged" || persisted.Variables["loop.item"] != "global" || fmt.Sprint(persisted.Variables["loop.index"]) != "99" {
		t.Fatalf("persisted globals changed: %+v", persisted.Variables)
	}
}

func TestLoopContextPreservesNestedAliasesAndRounds(t *testing.T) {
	t.Parallel()
	query := func(id, label string) Step {
		return Step{ID: id, BeadQuery: &BeadQueryStep{Label: StringOrList{label}}}
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "nested-loop-bindings",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{ID: "outer", Loop: &LoopConfig{
			Items: `[{"name":"outer","cap":2,"children":[{"name":"first"},{"name":"second"}]}]`, As: "group",
			Steps: []Step{
				query("before", "before:${loop.group.name}:${item.name}:${loop.index}"),
				{ID: "inner", Loop: &LoopConfig{
					Items: "${loop.group.children}", As: "file", MaxIterations: IntOrExpr{Expr: "${loop.group.cap}"},
					Steps: []Step{query("inspect", "inner:${loop.group.name}:${loop.file.name}:${item.name}:${loop.index}:${loop.count}")},
				}},
				{ID: "rounds", Foreach: &ForeachConfig{
					Items: "${loop.group.children}", As: "entry", MaxRounds: IntOrExpr{Expr: "${loop.group.cap}"},
					Steps: []Step{{ID: "repeat", Loop: &LoopConfig{
						Times: 1, As: "repeat", MaxIterations: IntOrExpr{Expr: "${round}"},
						Steps: []Step{query("inspect", "round:${entry.name}:${loop.group.name}:${loop.repeat}:${round}:${loop.round}:${item}")},
					}}},
				}},
				query("after", "after:${loop.group.name}:${item.name}:${loop.index}:${loop.file | absent}"),
			},
		}}},
	}
	cfg := DefaultExecutorConfig("nested-loop-bindings")
	cfg.ProjectDir = t.TempDir()
	var observed []string
	cfg.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--label" {
				observed = append(observed, args[i+1])
			}
		}
		return []byte(`{"issues":[]}`), nil
	}
	state, err := NewExecutor(cfg).Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("Run() = %+v, %v; queries=%q", state, err, observed)
	}
	want := []string{
		"before:outer:outer:0",
		"inner:outer:first:first:0:2",
		"inner:outer:second:second:1:2",
		"round:first:outer:0:1:1:0",
		"round:first:outer:0:2:2:0",
		"round:second:outer:0:1:1:0",
		"round:second:outer:0:2:2:0",
		"after:outer:outer:0:absent",
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("nested bindings = %q, want %q", observed, want)
	}
	for key := range state.Variables {
		if strings.HasPrefix(key, "loop.") || key == "round" || key == "rounds_remaining" {
			t.Fatalf("nested binding %s leaked into global state", key)
		}
	}
}

// Foreach materialization must read outer loop aliases from the execution
// context before defaults or unrelated shared variables can freeze wrong values.
func TestForeachMaterializationPreservesOuterLoopBindings(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared-shadow-%v", shared), func(t *testing.T) {
			t.Parallel()
			query := func(id, label string) Step {
				return Step{ID: id, BeadQuery: &BeadQueryStep{Label: StringOrList{label}}}
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "foreach-outer-bindings",
				Settings:      DefaultWorkflowSettings(),
				Steps: []Step{{ID: "outer", Loop: &LoopConfig{
					Items: `[{"name":"outer","cap":2,"children":[{"name":"child"}]}]`,
					As:    "group",
					Steps: []Step{
						{ID: "children", Foreach: &ForeachConfig{
							Items: "${loop.group.children}", As: "entry",
							Steps: []Step{
								query("inspect", "direct:${loop.group.name | fallback}:${item.name}:${entry.name}:${loop.item.name}:${loop.index}"),
								{ID: "repeat", Loop: &LoopConfig{
									Until: "test '${loop.index}' -eq 1", MaxIterations: IntOrExpr{Expr: "${loop.group.cap | 1}"},
									Steps: []Step{query("inspect", "repeat:${loop.group.name | fallback}:${entry.name}:${item}")},
								}},
							},
						}},
						query("after", "after:${loop.group.name}:${item.name}:${loop.index}"),
					},
				}}},
			}
			cfg := DefaultExecutorConfig("foreach-outer-bindings")
			cfg.ProjectDir = t.TempDir()
			cfg.GlobalTimeout = 10 * time.Second
			var observed []string
			cfg.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "--label" {
						observed = append(observed, args[i+1])
					}
				}
				return []byte(`{"issues":[]}`), nil
			}
			globals := map[string]interface{}{"stable": "unchanged"}
			if shared {
				globals["loop.group"] = map[string]interface{}{"name": "shared", "cap": 3}
			}
			state, err := NewExecutor(cfg).Run(context.Background(), workflow, globals, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("Run() = %+v, %v; queries=%q", state, err, observed)
			}
			want := []string{
				"direct:outer:child:child:child:0",
				"repeat:outer:child:0",
				"repeat:outer:child:1",
				"after:outer:outer:0",
			}
			if !reflect.DeepEqual(observed, want) {
				t.Fatalf("materialized outer bindings = %q, want %q", observed, want)
			}
			for key, value := range globals {
				if !reflect.DeepEqual(state.Variables[key], value) {
					t.Fatalf("global %s changed: got %#v, want %#v", key, state.Variables[key], value)
				}
			}
			if _, present := state.Variables["loop.group"]; present != shared {
				t.Fatal("materialization leaked an outer binding into workflow globals")
			}
		})
	}
}

func TestApplyResumeStateConcurrentWithSnapshots(t *testing.T) {
	const stepCount = 200

	workflow := &Workflow{
		Name:  "resume-concurrency",
		Steps: make([]Step, 0, stepCount),
	}
	steps := make(map[string]StepResult, stepCount)
	variables := make(map[string]interface{}, stepCount*2)

	for i := 0; i < stepCount; i++ {
		stepID := fmt.Sprintf("step-%03d", i)
		outputVar := "out_" + stepID
		workflow.Steps = append(workflow.Steps, Step{ID: stepID, OutputVar: outputVar})

		status := StatusCompleted
		if i%3 == 0 {
			status = StatusFailed
		}
		steps[stepID] = StepResult{StepID: stepID, Status: status, Output: "value"}
		variables["steps."+stepID+".output"] = "value"
		variables["steps."+stepID+".data"] = map[string]interface{}{"ok": true}
		variables[outputVar] = "value"
		variables[outputVar+"_parsed"] = map[string]interface{}{"ok": true}
	}
	for i := 0; i < 50; i++ {
		stepID := fmt.Sprintf("orphan-%03d", i)
		steps[stepID] = StepResult{StepID: stepID, Status: StatusCompleted}
	}

	executor := NewExecutor(DefaultExecutorConfig("session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "run-concurrency",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps:      steps,
		Variables:  variables,
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				if snapshot := executor.snapshotState(); snapshot == nil {
					t.Error("snapshotState returned nil")
					return
				}
			}
		}()
	}

	close(start)
	executor.applyResumeState()
	wg.Wait()

	executor.stateMu.RLock()
	for stepID, result := range executor.state.Steps {
		if result.Status == StatusFailed {
			t.Fatalf("rerunnable step %s still present after applyResumeState", stepID)
		}
	}
	if _, ok := executor.state.Steps["orphan-000"]; ok {
		t.Fatal("orphan step still present after applyResumeState")
	}
	executor.stateMu.RUnlock()

	executor.varMu.RLock()
	if _, ok := executor.state.Variables["out_step-000"]; ok {
		t.Fatal("rerunnable output var still present after applyResumeState")
	}
	if _, ok := executor.state.Variables["steps.step-000.output"]; ok {
		t.Fatal("rerunnable steps.* output still present after applyResumeState")
	}
	if _, ok := executor.state.Variables["out_step-001"]; !ok {
		t.Fatal("completed output var should be preserved")
	}
	executor.varMu.RUnlock()
}

// bd-gtb5p: when applyResumeState removes a persisted step result because
// graph.MarkExecuted failed (the workflow definition no longer contains
// that step), the corresponding flat steps.<id>.output / steps.<id>.data
// variables must be purged so a downstream ${steps.orphan.output} cannot
// resolve through ghost data after a workflow rename.
func TestApplyResumeStateClearsOrphanStepVariables(t *testing.T) {
	workflow := &Workflow{
		Name:  "resume-orphan-cleanup",
		Steps: []Step{{ID: "live"}},
	}

	executor := NewExecutor(DefaultExecutorConfig("session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "run-orphan",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"live":   {StepID: "live", Status: StatusCompleted, Output: "ok"},
			"orphan": {StepID: "orphan", Status: StatusCompleted, Output: "stale"},
		},
		Variables: map[string]interface{}{
			"steps.live.output":   "ok",
			"steps.orphan.output": "stale",
			"steps.orphan.data":   map[string]interface{}{"key": "stale"},
		},
	}

	executor.applyResumeState()

	if _, ok := executor.state.Steps["orphan"]; ok {
		t.Fatal("orphan step result not removed")
	}
	if _, ok := executor.state.Variables["steps.orphan.output"]; ok {
		t.Fatal("steps.orphan.output ghost variable not cleared after orphan removal")
	}
	if _, ok := executor.state.Variables["steps.orphan.data"]; ok {
		t.Fatal("steps.orphan.data ghost variable not cleared after orphan removal")
	}
	if got := executor.state.Variables["steps.live.output"]; got != "ok" {
		t.Fatalf("live step variable got mutated: %v", got)
	}
}
