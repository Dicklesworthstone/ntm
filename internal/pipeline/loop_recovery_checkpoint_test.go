package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestLoopBreakRestoresDurableCheckpointWithoutBodyDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{"items", `items: '["first","second"]'`},
		{"times", "times: 2"},
		{"while", `while: "1 == 1"`},
		{"until", `until: "exit 1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wf, err := ParseString(fmt.Sprintf(`schema_version: "2.0"
name: checkpoint-break
steps:
  - id: iterate
    loop:
      %s
      max_iterations: 2
      collect: saved
      steps:
        - id: write
          command: "printf terminal"
          loop_control: break
`, tc.mode), "yaml")
			if err != nil {
				t.Fatal(err)
			}
			parent := &wf.Steps[0]
			e := createForeachTestExecutor(t, wf)
			e.beginForeachState(parent.ID, 2)
			e.markForeachIterationStarted(parent.ID, 0, 2)
			now := time.Now()
			child := StepResult{
				StepID: "iterate_iter0_write", Status: StatusCompleted,
				AgentType: "command", Output: "terminal", StartedAt: now, FinishedAt: now,
			}
			e.state.Steps[child.StepID] = child
			first := NewLoopExecutor(e).finishLoopBreak(parent, LoopResult{Results: []StepResult{child}}, 0, []StepResult{child})
			if first.Status != StatusCompleted || first.BreakReason == "" {
				t.Fatalf("checkpointing terminal decision failed: %+v", first)
			}
			prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := prior.Steps[parent.ID]; exists {
				t.Fatal("fixture already published its outer scheduler result")
			}
			if _, exists := prior.Variables["saved"]; exists {
				t.Fatal("fixture already published its aggregate variable")
			}
			r := createForeachTestExecutor(t, wf)
			r.state = prior
			want := []interface{}{"terminal"}
			for replay := 0; replay < 2; replay++ {
				got := NewLoopExecutor(r).ExecuteLoop(context.Background(), parent, wf)
				if got.Status != StatusCompleted || got.Iterations != 1 || got.BreakReason != "break statement" || len(got.Results) != 0 ||
					!reflect.DeepEqual(got.Collected, want) || !reflect.DeepEqual(r.state.Variables["saved"], want) {
					t.Fatalf("replay %d returned %+v, aggregate=%v", replay, got, r.state.Variables["saved"])
				}
				if len(r.state.CommandExecutions) != 0 {
					t.Fatal("restored break dispatched command work")
				}
			}
			parent.Loop.Steps[0].Command = "printf changed"
			if got := NewLoopExecutor(r).ExecuteLoop(context.Background(), parent, wf); got.Status != StatusFailed {
				t.Fatalf("changed loop consumed the old terminal decision: %+v", got)
			}
		})
	}
}

func TestSparseCollectionCheckpointRewindsExactPrefix(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, pivot := range []int{1, 2} {
			t.Run(fmt.Sprintf("legacy=%t/iteration=%d", legacy, pivot), func(t *testing.T) {
				t.Parallel()
				wf := &Workflow{Name: "sparse", Settings: DefaultWorkflowSettings(), Steps: []Step{{
					ID: "batch", Loop: &LoopConfig{Items: `["A","B","C"]`, Collect: "answers", Steps: []Step{{ID: "answer", Command: "printf output"}}},
				}}}
				e := createForeachTestExecutor(t, wf)
				e.beginForeachState("batch", 3)
				for i, output := range []string{"", "B", "C"} {
					e.markForeachIterationStarted("batch", i, 3, true)
					id := fmt.Sprintf("batch_iter%d_answer", i)
					e.state.Steps[id] = StepResult{StepID: id, Status: StatusCompleted, Output: output}
					if output == "" {
						e.markForeachIterationCompleted("batch", i, 3)
					} else {
						e.markForeachIterationCompleted("batch", i, 3, output)
					}
				}
				if legacy {
					progress := e.state.ForeachState["batch"]
					progress.CollectOffsets = nil
					e.state.ForeachState["batch"] = progress
				}
				if err := SaveState(e.config.ProjectDir, e.state); err != nil {
					t.Fatal(err)
				}
				prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
				if err != nil {
					t.Fatal(err)
				}
				r := createForeachTestExecutor(t, wf)
				r.state = prior
				if err := r.applyResumeOptions(wf, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: pivot}); err != nil {
					t.Fatal(err)
				}
				want := []interface{}{}
				if pivot == 2 {
					want = append(want, "B")
				}
				got := r.state.ForeachState["batch"].CollectedOutputs
				if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
					t.Fatalf("rewind retained %v, want %v", got, want)
				}
				for i := pivot; i < 3; i++ {
					r.markForeachIterationStarted("batch", i, 3, true)
					value := fmt.Sprintf("new-%d", i)
					r.markForeachIterationCompleted("batch", i, 3, value)
					want = append(want, value)
				}
				if got := r.loadForeachCollectedOutputs("batch"); !reflect.DeepEqual(got, want) {
					t.Fatalf("replayed aggregate %v, want %v", got, want)
				}
			})
		}
	}
}
