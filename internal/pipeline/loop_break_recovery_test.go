package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoopBreakSurvivesBeforeParentCheckpoint(t *testing.T) {
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
name: break-test
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
			logPath := filepath.Join(t.TempDir(), "invocations")
			parent.Loop.Steps[0].Command = fmt.Sprintf("printf x >> %q; printf terminal", logPath)
			e := createForeachTestExecutor(t, wf)
			first := NewLoopExecutor(e).ExecuteLoop(context.Background(), parent, wf)
			want := []interface{}{"terminal"}
			if first.Status != StatusCompleted || first.BreakReason == "" || !reflect.DeepEqual(first.Collected, want) {
				t.Fatalf("first loop = %+v", first)
			}
			// Only the loop's own receipts have reached durable storage.
			// The outer scheduler has not published its StepResult yet.
			if _, published := e.state.Steps[parent.ID]; published {
				t.Fatal("fixture already has an outer loop result")
			}
			data, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			r := createForeachTestExecutor(t, wf)
			if err := json.Unmarshal(data, r.state); err != nil {
				t.Fatal(err)
			}
			delete(r.state.Variables, "saved")
			again := NewLoopExecutor(r).ExecuteLoop(context.Background(), parent, wf)
			if again.Status != StatusCompleted || again.Iterations != 1 || len(again.Results) != 0 || again.BreakReason != first.BreakReason ||
				!reflect.DeepEqual(again.Collected, want) || !reflect.DeepEqual(r.state.Variables["saved"], want) {
				t.Fatalf("resumed loop = %+v, collected variable = %v", again, r.state.Variables["saved"])
			}
			if got, err := os.ReadFile(logPath); err != nil || string(got) != "x" {
				t.Fatalf("command invocations = %q, %v", got, err)
			}
			parent.Loop.Steps[0].Command = "printf changed"
			if result := NewLoopExecutor(r).ExecuteLoop(context.Background(), parent, wf); result.Status != StatusFailed {
				t.Fatalf("changed definition accepted terminal checkpoint: %+v", result)
			}
			if tc.name == "items" {
				r.forceResumeIteration(parent.ID, 0)
				progress := r.state.ForeachState[parent.ID]
				if progress.Break != nil || len(progress.CollectedOutputs) != 0 {
					t.Fatalf("explicit rewind kept terminal evidence: %+v", progress)
				}
			}
		})
	}
}

func TestLoopBreakCannotRestoreAfterFailedPersistence(t *testing.T) {
	t.Parallel()

	wf := &Workflow{Name: "break-persist", Settings: DefaultWorkflowSettings(), Steps: []Step{{
		ID: "batch", Loop: &LoopConfig{Times: 2, Collect: "answers", Steps: []Step{{ID: "work", Command: "printf result"}}},
	}}}
	e := createForeachTestExecutor(t, wf)
	e.beginForeachState("batch", 2)
	e.markForeachIterationStarted("batch", 0, 2)
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, ".ntm"), []byte("block checkpoint directory"), 0600); err != nil {
		t.Fatal(err)
	}
	le := NewLoopExecutor(e)
	first := le.finishLoopBreak(&wf.Steps[0], LoopResult{}, 0, []StepResult{{Status: StatusCompleted, Output: "result"}})
	if first.Status != StatusFailed || e.checkpointFailure() == nil {
		t.Fatalf("failed save returned success: %+v", first)
	}
	if e.state.ForeachState["batch"].Break == nil {
		t.Fatal("failed save discarded terminal evidence")
	}
	if got := le.ExecuteLoop(context.Background(), &wf.Steps[0], wf); got.Status != StatusFailed {
		t.Fatalf("in-memory marker hid the persistence failure: %+v", got)
	}
	if _, published := e.state.Variables["answers"]; published {
		t.Fatal("failed checkpoint published collected output")
	}
}
