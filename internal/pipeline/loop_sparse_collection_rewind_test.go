package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestLoopRewindKeepsOnlyEarlierCollectedOutputs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "with-offsets"
		if legacy {
			name = "legacy-body-results"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wf := &Workflow{Name: "sparse", Settings: DefaultWorkflowSettings(), Steps: []Step{{
				ID: "batch", Loop: &LoopConfig{
					Items: `["A","B","C"]`, Collect: "answers",
					Steps: []Step{{ID: "answer", Command: `if [ "${item}" != A ]; then printf %s "${item}"; fi`}},
				},
			}}}
			e := createForeachTestExecutor(t, wf)
			ctx := context.Background()
			before := NewLoopExecutor(e).ExecuteLoop(ctx, &wf.Steps[0], wf)
			want := []interface{}{"B", "C"}
			if before.Status != StatusCompleted || !reflect.DeepEqual(before.Collected, want) {
				t.Fatalf("original loop = %+v", before)
			}
			saved, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			r := createForeachTestExecutor(t, wf)
			if err := json.Unmarshal(saved, r.state); err != nil {
				t.Fatal(err)
			}
			if legacy {
				progress := r.state.ForeachState["batch"]
				progress.CollectOffsets = nil
				r.state.ForeachState["batch"] = progress
			}
			if err := r.applyResumeOptions(wf, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1}); err != nil {
				t.Fatal(err)
			}
			if progress := r.state.ForeachState["batch"]; len(progress.CollectedOutputs) != 0 {
				t.Fatalf("rewind retained stale suffix: %v", progress.CollectedOutputs)
			}
			after := NewLoopExecutor(r).ExecuteLoop(ctx, &wf.Steps[0], wf)
			if after.Status != StatusCompleted || after.Iterations != 2 || !reflect.DeepEqual(after.Collected, want) {
				t.Fatalf("resumed loop = %+v", after)
			}
		})
	}
}

func TestLegacySparseRewindRejectsInconsistentAggregateBeforeMutation(t *testing.T) {
	wf := &Workflow{Name: "sparse", Settings: DefaultWorkflowSettings(), Steps: []Step{{
		ID: "batch", Loop: &LoopConfig{Items: `["A","B"]`, Collect: "answers", Steps: []Step{{ID: "answer", Command: "printf B"}}},
	}}}
	e := createForeachTestExecutor(t, wf)
	e.state.ForeachState = map[string]ForeachIterationState{
		"batch": {StepID: "batch", Total: 2, CurrentIteration: 2, CollectedOutputs: []interface{}{"unknown"}},
	}
	e.state.Steps["batch_iter1_answer"] = StepResult{StepID: "batch_iter1_answer", Status: StatusCompleted, Output: "B"}
	before, err := json.Marshal(e.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.applyResumeOptions(wf, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1}); err == nil {
		t.Fatal("accepted mismatched legacy aggregate")
	}
	after, err := json.Marshal(e.state)
	if err != nil || string(after) != string(before) {
		t.Fatalf("rejected rewind mutated the checkpoint: %v", err)
	}
}
