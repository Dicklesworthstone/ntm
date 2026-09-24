package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIterationIndexFromIDRewindBoundary(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"batch_iter0", 0},
		{"batch_iter12", 12},
		{"batch_iter2_round3_work", 2},
		{"batch_iter2_nested_iter7_work", 2},
		{"batch_iter", -1},
		{"batch_iter-1_work", -1},
		{"batch_iter+1_work", -1},
		{"batch_iter1other", -1},
		{"batch_iter1.other", -1},
		{"batch_iter1-other", -1},
		{"other_batch_iter2", -1},
		{"batch_iter" + strings.Repeat("9", 80), -1},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if got := iterationIndexFromID(tc.id, "batch_iter"); got != tc.want {
				t.Fatalf("iterationIndexFromID(%q) = %d, want %d", tc.id, got, tc.want)
			}
		})
	}
}

func TestForceResumeIterationRewindsDurableSubtree(t *testing.T) {
	prior := &ExecutionState{
		ForeachState: map[string]ForeachIterationState{
			"batch": {
				StepID: "batch", CurrentIteration: 3, Total: 3,
				CompletedIterationIDs: []string{"batch_iter0", "batch_iter1", "batch_iter2"},
				CompletedRounds:       map[string]int{"batch_iter0": 3, "batch_iter1": 3, "batch_iter2": 2},
				CollectedOutputs:      []interface{}{"keep", "discard-1", "discard-2"},
				ItemsFingerprint:      strings.Repeat("a", 64),
			},
			"batch_iter0_nested": {StepID: "batch_iter0_nested", Total: 1},
			"batch_iter1_nested": {StepID: "batch_iter1_nested", CompletedIterationIDs: []string{"batch_iter1_nested_iter0"}},
			"unrelated":          {StepID: "unrelated", Total: 8},
		},
		Steps: map[string]StepResult{
			"batch_iter0_work": {}, "batch_iter1_work": {}, "batch_iter2_work": {},
			"batch_iter1other": {}, "unrelated": {},
		},
		AgentDeliveries: map[string]AgentDeliveryState{
			"batch_iter0_work": {}, "batch_iter1_work": {}, "batch_iter2_receipt_only": {},
			"batch_iter1other": {}, "unrelated": {},
		},
		InFlightSteps: map[string]InFlightStepState{
			"batch_iter0_work": {}, "batch_iter1_nested_iter0_work": {}, "unrelated": {},
		},
		ParallelState: map[string]ParallelGroupState{
			"batch_iter0_parallel": {AllSubstepsSettled: true},
			"batch_iter1_parallel": {AllSubstepsSettled: true, CompletedStepIDs: []string{"batch_iter1_parallel_work"}},
			"unrelated":            {Total: 9},
		},
		Variables: map[string]interface{}{
			"input": "keep", "steps.batch_iter0_work.output": "keep",
			"steps.batch_iter1_work.output": "discard", "steps.batch_iter2_receipt_only.data": "discard",
			"steps.batch_iter2_orphan.output": "discard", "steps.batch_iter1other.output": "keep",
		},
	}
	// Exercise state restored from JSON, rather than relying on live objects
	// that would not survive a process restart.
	encoded, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	var restored ExecutionState
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	e := &Executor{state: &restored}
	e.forceResumeIteration("batch", 1)

	loop := restored.ForeachState["batch"]
	if !reflect.DeepEqual(loop.CompletedIterationIDs, []string{"batch_iter0"}) {
		t.Errorf("completed iterations = %v", loop.CompletedIterationIDs)
	}
	if !reflect.DeepEqual(loop.CompletedRounds, map[string]int{"batch_iter0": 3}) {
		t.Errorf("round watermarks survived rewind: %v", loop.CompletedRounds)
	}
	if !reflect.DeepEqual(loop.CollectedOutputs, []interface{}{"keep"}) || loop.CurrentIteration != 1 || loop.Total != 3 {
		t.Errorf("incorrect rewind cursor or collected output: %+v", loop)
	}
	if loop.ItemsFingerprint != strings.Repeat("a", 64) {
		t.Error("rewind lost the fingerprint protecting preserved iterations")
	}
	for name, keys := range map[string][]string{
		"steps":      rewindTestKeys(restored.Steps),
		"deliveries": rewindTestKeys(restored.AgentDeliveries),
		"inflight":   rewindTestKeys(restored.InFlightSteps),
		"parallel":   rewindTestKeys(restored.ParallelState),
		"foreach":    rewindTestKeys(restored.ForeachState),
	} {
		for _, key := range keys {
			if strings.HasPrefix(key, "batch_iter1_") || strings.HasPrefix(key, "batch_iter2_") {
				t.Errorf("%s retained rewound checkpoint %q", name, key)
			}
		}
	}
	if _, ok := restored.Steps["batch_iter0_work"]; !ok {
		t.Error("lost an earlier iteration's result")
	}
	if _, ok := restored.AgentDeliveries["batch_iter1other"]; !ok {
		t.Error("deleted an unrelated agent's dispatch evidence")
	}
	if _, ok := restored.InFlightSteps["unrelated"]; !ok {
		t.Error("lost unrelated in-flight work")
	}
	if restored.ParallelState["unrelated"].Total != 9 || restored.ForeachState["unrelated"].Total != 8 {
		t.Error("modified unrelated nested state")
	}
	wantVars := map[string]interface{}{
		"input": "keep", "steps.batch_iter0_work.output": "keep", "steps.batch_iter1other.output": "keep",
	}
	if !reflect.DeepEqual(restored.Variables, wantVars) {
		t.Errorf("stale or missing variables after rewind: %#v", restored.Variables)
	}
}

func rewindTestKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestForceResumeIterationClearsOrphanOutputsWithoutResults(t *testing.T) {
	e := &Executor{state: &ExecutionState{
		Variables: map[string]interface{}{
			"steps.batch_iter0_work.output": "keep",
			"steps.batch_iter1_work.output": "discard",
			"steps.batch_iter2_work.data":   "discard",
			"input":                         "keep",
		},
	}}
	e.forceResumeIteration("batch", 1)
	want := map[string]interface{}{"steps.batch_iter0_work.output": "keep", "input": "keep"}
	if !reflect.DeepEqual(e.state.Variables, want) {
		t.Fatalf("orphan outputs were not rewound: %#v", e.state.Variables)
	}
}

func TestForceResumeIterationInvalidSelectionDoesNotDiscardEvidence(t *testing.T) {
	for _, tc := range []struct {
		step string
		iter int
	}{{"batch", -1}, {"", 0}, {"  ", 0}} {
		e := &Executor{state: &ExecutionState{
			AgentDeliveries: map[string]AgentDeliveryState{"unrelated": {StepID: "unrelated"}},
			Steps:           map[string]StepResult{"unrelated": {}},
		}}
		before, err := json.Marshal(e.state)
		if err != nil {
			t.Fatal(err)
		}
		e.forceResumeIteration(tc.step, tc.iter)
		after, err := json.Marshal(e.state)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("invalid force selection (%q, %d) mutated checkpoint", tc.step, tc.iter)
		}
	}
}
