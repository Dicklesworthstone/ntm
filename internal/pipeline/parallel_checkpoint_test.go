package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parallelCheckpointExample(mode OutputVarMode) (Step, Step, StepResult, map[string]interface{}) {
	child := Step{ID: "group_each", Foreach: &ForeachConfig{OutputVarMode: mode}, OutputVar: "values"}
	parent := Step{ID: "group", Parallel: ParallelSpec{Steps: []Step{child}}}
	now := time.Now().UTC().Truncate(time.Microsecond)
	result := StepResult{
		StepID: child.ID, Status: StatusCompleted, Output: "Foreach completed",
		StartedAt: now, FinishedAt: now,
		ParsedData: []foreachIterationResult{{Index: 0, Item: "alpha", Results: []StepResult{
			{StepID: "group_each_iter0_work", Status: StatusCompleted, Output: "answer", ParsedData: map[string]interface{}{"count": 1}},
		}}},
	}
	bindings := map[string]interface{}{"values": []string{"answer"}, "values_parsed": []interface{}{map[string]interface{}{"count": float64(1)}}}
	switch mode {
	case OutputVarModeCollect:
		bindings = map[string]interface{}{"values": map[string]string{"alpha": "answer"}, "values_parsed": map[string]interface{}{"alpha": map[string]interface{}{"count": float64(1)}}}
	case OutputVarModeLast:
		bindings = map[string]interface{}{"values": "answer", "values_parsed": map[string]interface{}{"count": float64(1)}}
	}
	return parent, child, result, bindings
}

func TestParallelChildCheckpointRoundTripPreservesCollections(t *testing.T) {
	for _, mode := range []OutputVarMode{OutputVarModeAggregate, OutputVarModeCollect, OutputVarModeLast} {
		t.Run(string(mode), func(t *testing.T) {
			parent, child, result, want := parallelCheckpointExample(mode)
			raw, err := encodeParallelChildCheckpoint(&parent, &child, result, want, true)
			if err != nil {
				t.Fatal(err)
			}
			// Match LoadState's conversion of nested structs to generic JSON.
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var loaded StepResult
			if err := json.Unmarshal(encoded, &loaded); err != nil {
				t.Fatal(err)
			}
			got, _, err := decodeParallelChildCheckpoint(&parent, &child, loaded, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("restored=%#v want=%#v", got, want)
			}
		})
	}
}

func TestParallelChildCheckpointEmptyCollectionsAndAbsentLast(t *testing.T) {
	for _, mode := range []OutputVarMode{OutputVarModeAggregate, OutputVarModeCollect, OutputVarModeLast} {
		t.Run(string(mode), func(t *testing.T) {
			parent, child, result, _ := parallelCheckpointExample(mode)
			bindings := map[string]interface{}{}
			if mode == OutputVarModeAggregate {
				bindings["values"] = []string{}
			}
			if mode == OutputVarModeCollect {
				bindings["values"] = map[string]string{}
			}
			raw, err := encodeParallelChildCheckpoint(&parent, &child, result, bindings, true)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := decodeParallelChildCheckpoint(&parent, &child, result, raw)
			if err != nil || !reflect.DeepEqual(got, bindings) {
				t.Fatalf("empty binding changed: got=%#v error=%v", got, err)
			}
		})
	}
}

func TestParallelChildCheckpointRejectsChangedEvidence(t *testing.T) {
	for _, change := range []string{"version", "malformed", "trailing", "unknown-field", "parent", "child", "result", "output", "unsettled", "identity"} {
		t.Run(change, func(t *testing.T) {
			parent, child, result, bindings := parallelCheckpointExample(OutputVarModeAggregate)
			raw, err := encodeParallelChildCheckpoint(&parent, &child, result, bindings, true)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "version":
				raw = strings.Replace(raw, `"version":2`, `"version":99`, 1)
			case "malformed":
				raw = "{"
			case "trailing":
				raw += " {}"
			case "unknown-field":
				raw = strings.Replace(raw, `"version":2`, `"version":2,"future":true`, 1)
			case "parent":
				parent.OnError = ErrorActionContinue
			case "child":
				child.OutputVar = "other"
			case "result":
				result.Output = "different"
			case "output":
				raw = strings.Replace(raw, "answer", "changed", 1)
			case "unsettled":
				result.RerunOnResume = true
			case "identity":
				result.StepID = "another_child"
			}
			if got, _, err := decodeParallelChildCheckpoint(&parent, &child, result, raw); err == nil || got != nil {
				t.Fatalf("accepted %s: got=%#v err=%v", change, got, err)
			}
		})
	}
}

func TestParallelChildCheckpointRejectsUnownedOrMalformedBindings(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     OutputVarMode
		bindings map[string]interface{}
	}{
		{"missing-capture", OutputVarModeAggregate, nil},
		{"missing-primary", OutputVarModeAggregate, map[string]interface{}{}},
		{"foreign-name", OutputVarModeAggregate, map[string]interface{}{"values": []string{}, "other": "data"}},
		{"wrong-shape", OutputVarModeAggregate, map[string]interface{}{"values": "summary"}},
		{"non-string", OutputVarModeAggregate, map[string]interface{}{"values": []interface{}{false}}},
		{"parsed-length", OutputVarModeAggregate, map[string]interface{}{"values": []string{"one"}, "values_parsed": []interface{}{}}},
		{"foreign-parsed-key", OutputVarModeCollect, map[string]interface{}{"values": map[string]string{"one": "yes"}, "values_parsed": map[string]interface{}{"two": true}}},
		{"non-string-collect", OutputVarModeCollect, map[string]interface{}{"values": map[string]interface{}{"one": false}}},
		{"absent-last-with-parsed", OutputVarModeLast, map[string]interface{}{"values_parsed": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, child, result, _ := parallelCheckpointExample(test.mode)
			if _, err := encodeParallelChildCheckpoint(&parent, &child, result, test.bindings, true); err == nil {
				t.Fatal("invalid binding was checkpointed")
			}
		})
	}
}

func TestParallelChildCheckpointCaptureHasExactRuntimeOwner(t *testing.T) {
	capture := &parallelOutputCapture{stepID: "group_each"}
	ctx := context.WithValue(context.Background(), parallelOutputCaptureKey{}, capture)
	state := &ExecutionState{Variables: map[string]interface{}{"values": []string{"own"}, "values_parsed": []interface{}{map[string]interface{}{"ok": true}}}}
	owned := snapshotForeachOutputBindings(state, "values")
	captureParallelForeachOutputs(ctx, "group_each", owned)
	state.Variables["values"].([]string)[0] = "mutated"
	state.Variables["values_parsed"].([]interface{})[0].(map[string]interface{})["ok"] = false
	captureParallelForeachOutputs(ctx, "group_each_on_success_hook", map[string]interface{}{"values": "hook"})
	captureParallelForeachOutputs(ctx, "group_each_iter0_work", map[string]interface{}{"values": "nested"})
	if capture.outputs["values"].([]string)[0] != "own" || capture.outputs["values_parsed"].([]interface{})[0].(map[string]interface{})["ok"] != true {
		t.Fatalf("snapshot aliased another owner: %#v", capture.outputs)
	}
}

func TestParallelChildCheckpointAggregationClearsStaleParsedAliases(t *testing.T) {
	for _, mode := range []OutputVarMode{OutputVarModeAggregate, OutputVarModeCollect} {
		t.Run(string(mode), func(t *testing.T) {
			_, child, _, _ := parallelCheckpointExample(mode)
			e := &Executor{state: &ExecutionState{Variables: map[string]interface{}{"values_parsed": "stale"}}}
			plans := []foreachIterationPlan{{Steps: []Step{{ID: "body", OutputVar: "values"}}}}
			iterations := []foreachIterationResult{{Index: 0, Item: "alpha", Results: []StepResult{{StepID: "body", Status: StatusCompleted, Output: "fresh"}}}}
			bindings := e.storeForeachOutputVars(&child, child.Foreach, plans, iterations)
			if _, ok := bindings["values_parsed"]; ok {
				t.Fatal("captured stale parsed alias")
			}
			if _, ok := e.state.Variables["values_parsed"]; ok {
				t.Fatal("left stale parsed alias in runtime")
			}
		})
	}
}

func TestParallelChildCheckpointClonePreservesEmptyShapesAndOwnership(t *testing.T) {
	original := map[string]interface{}{
		"empty-list": []string{}, "empty-map": map[string]string{}, "empty-data": map[string]interface{}{},
		"list": []string{"one"}, "map": map[string]string{"one": "answer"},
		"nested":   []interface{}{map[string]string{"one": "nested"}},
		"nil-list": []string(nil), "nil-map": map[string]string(nil),
		"nil-data": map[string]interface{}(nil), "nil-slice": []interface{}(nil),
	}
	cloned := cloneInterfaceMap(original)
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("clone changed JSON shapes:\n%s\n%s", before, after)
	}
	original["list"].([]string)[0] = "changed"
	original["map"].(map[string]string)["one"] = "changed"
	original["nested"].([]interface{})[0].(map[string]string)["one"] = "changed"
	if cloned["list"].([]string)[0] != "one" || cloned["map"].(map[string]string)["one"] != "answer" || cloned["nested"].([]interface{})[0].(map[string]string)["one"] != "nested" {
		t.Fatalf("collection snapshot aliases its source: %#v", cloned)
	}
}

func TestParallelChildCheckpointEmptyAggregationCanReload(t *testing.T) {
	for _, mode := range []OutputVarMode{OutputVarModeAggregate, OutputVarModeCollect} {
		t.Run(string(mode), func(t *testing.T) {
			parent, child, result, _ := parallelCheckpointExample(mode)
			e := &Executor{state: &ExecutionState{Variables: map[string]interface{}{"values_parsed": "stale"}}}
			bindings := e.storeForeachOutputVars(&child, child.Foreach, nil, nil)
			raw, err := encodeParallelChildCheckpoint(&parent, &child, result, bindings, true)
			if err != nil {
				t.Fatal(err)
			}
			loaded, _, err := decodeParallelChildCheckpoint(&parent, &child, result, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(bindings, loaded) {
				t.Fatalf("empty aggregate changed across checkpoint: before=%#v after=%#v", bindings, loaded)
			}
		})
	}
}

func TestParallelHooksSettlementCannotBeForgedByFlagChange(t *testing.T) {
	parent := Step{ID: "group", Parallel: ParallelSpec{Steps: []Step{{ID: "producer", OnSuccess: []Step{{ID: "notify"}}}}}}
	children, _, _ := parallelChildDefinitions(&parent)
	child := children[0]
	result := StepResult{StepID: child.ID, Status: StatusCompleted, StartedAt: time.Now(), FinishedAt: time.Now()}
	raw, err := encodeParallelChildCheckpoint(&parent, &child, result, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err = json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	doc["hooks_settled"] = true
	changed, _ := json.Marshal(doc)
	if _, _, err := decodeParallelChildCheckpoint(&parent, &child, result, string(changed)); err == nil {
		t.Fatal("changed lifecycle evidence accepted")
	}
}
