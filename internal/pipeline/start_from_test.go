// Package pipeline tests cover the --start-from / --from-state behaviour
// implemented in executor.go applyStartFrom.
package pipeline

import (
	"context"
	"strings"
	"testing"
)

// linearWorkflow builds a five-step workflow where each step depends on the
// previous one. The dependency-wise predecessors of any step are exactly the
// steps with smaller indices, which is what --start-from is specified to skip.
func linearWorkflow() *Workflow {
	return &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "start-from-linear",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "step1", Prompt: "1"},
			{ID: "step2", Prompt: "2", DependsOn: []string{"step1"}},
			{ID: "step3", Prompt: "3", DependsOn: []string{"step2"}},
			{ID: "step4", Prompt: "4", DependsOn: []string{"step3"}},
			{ID: "step5", Prompt: "5", DependsOn: []string{"step4"}},
		},
	}
}

func TestStartFrom_LinearPipeline_SkipsTransitiveDeps(t *testing.T) {
	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "step3"
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), linearWorkflow(), nil, nil)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %v, want Completed", state.Status)
	}

	want := map[string]ExecutionStatus{
		"step1": StatusSkipped,
		"step2": StatusSkipped,
		"step3": StatusCompleted,
		"step4": StatusCompleted,
		"step5": StatusCompleted,
	}
	for id, expected := range want {
		got, ok := state.Steps[id]
		if !ok {
			t.Errorf("step %s missing from state.Steps", id)
			continue
		}
		if got.Status != expected {
			t.Errorf("step %s status = %v, want %v", id, got.Status, expected)
		}
		if expected == StatusSkipped && got.SkipReason != StartFromSkipReason {
			t.Errorf("step %s SkipReason = %q, want %q", id, got.SkipReason, StartFromSkipReason)
		}
	}
}

func TestStartFrom_FromState_ReusesPriorOutputs(t *testing.T) {
	prior := &ExecutionState{
		RunID:     "prior-run",
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusCompleted, Output: "prior-step1-output"},
			"step2": {StepID: "step2", Status: StatusCompleted, Output: "prior-step2-output"},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "step3"
	cfg.StartFromState = prior
	e := NewExecutor(cfg)

	// step3 references step1's output; substitution must succeed.
	wf := linearWorkflow()
	wf.Steps[2].Prompt = "use ${steps.step1.output}"

	state, err := e.Run(context.Background(), wf, nil, nil)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}

	step3 := state.Steps["step3"]
	if step3.Status != StatusCompleted {
		t.Fatalf("step3.Status = %v, want Completed", step3.Status)
	}
	if !strings.Contains(step3.Output, "prior-step1-output") {
		t.Fatalf("step3 output = %q, want prior step1 output substituted in", step3.Output)
	}
	if state.Steps["step1"].Output != "prior-step1-output" {
		t.Errorf("skipped step1.Output = %q, want copied from prior", state.Steps["step1"].Output)
	}
}

func TestStartFrom_UnknownStep_ReturnsError(t *testing.T) {
	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "does-not-exist"
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), linearWorkflow(), nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want unknown-step error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("err = %v, want it to mention the unknown step id", err)
	}
	if state == nil || state.Status != StatusFailed {
		t.Errorf("state.Status = %v, want Failed", state)
	}
}

func TestStartFrom_InsideParallelBody_ReturnsError(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "start-from-parallel",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "head", Prompt: "head"},
			{
				ID:        "fanout",
				DependsOn: []string{"head"},
				Parallel: ParallelSpec{
					Steps: []Step{
						{ID: "child_a", Prompt: "a"},
						{ID: "child_b", Prompt: "b"},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "child_b"
	e := NewExecutor(cfg)

	_, err := e.Run(context.Background(), wf, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want parallel-body error")
	}
	if !strings.Contains(err.Error(), "parallel") || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("err = %v, want it to mention the parallel container and parent id", err)
	}
}

func TestStartFrom_InsideLoopBody_ReturnsError(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "start-from-loop",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "loop_step",
				Loop: &LoopConfig{
					Times: 3,
					Steps: []Step{
						{ID: "loop_child", Prompt: "x"},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "loop_child"
	e := NewExecutor(cfg)

	_, err := e.Run(context.Background(), wf, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want loop-body error")
	}
	if !strings.Contains(err.Error(), "loop") {
		t.Fatalf("err = %v, want it to mention loop container", err)
	}
}

// TestStartFrom_InsideForeachPaneBody_ReturnsForeachPaneRejection covers
// bd-ctz1z: findStepContainer used to recurse only Step.Foreach bodies and
// missed Step.ForeachPane. --start-from could therefore target a nested
// per-pane body step and bypass the top-level-only contract. The
// rejection now matches the foreach branch, with kind=foreach_pane in the
// error message.
func TestStartFrom_InsideForeachPaneBody_ReturnsForeachPaneRejection(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "start-from-foreach-pane",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "fanout_pane",
				ForeachPane: &ForeachConfig{
					Items: `[1,2]`,
					Steps: []Step{
						{ID: "per_pane_child", Prompt: "child"},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "per_pane_child"
	e := NewExecutor(cfg)

	_, err := e.Run(context.Background(), wf, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want foreach_pane-body error")
	}
	if !strings.Contains(err.Error(), "foreach_pane") || !strings.Contains(err.Error(), "fanout_pane") {
		t.Fatalf("err = %v, want it to mention the foreach_pane container and parent id", err)
	}
	if strings.Contains(err.Error(), "not found in workflow") {
		t.Fatalf("err = %v, want foreach_pane-body diagnostic, not graph 'not found' fallback", err)
	}
}

func TestStartFrom_InsideForeachBody_ReturnsForeachRejection(t *testing.T) {
	// Foreach body steps are not indexed in the dependency graph, so a naive
	// graph-only lookup would report them as "not found in workflow" instead of
	// surfacing the actual constraint: --start-from must target a top-level
	// step. Operators need the foreach-body diagnostic to know which parent
	// step to target instead.
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "start-from-foreach",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items: `["a","b"]`,
					Steps: []Step{
						{ID: "iter_child", Prompt: "child"},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "iter_child"
	e := NewExecutor(cfg)

	_, err := e.Run(context.Background(), wf, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want foreach-body error")
	}
	if !strings.Contains(err.Error(), "foreach") || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("err = %v, want it to mention the foreach container and parent id", err)
	}
	if strings.Contains(err.Error(), "not found in workflow") {
		t.Fatalf("err = %v, want foreach-body diagnostic, not graph 'not found' fallback", err)
	}
}

func TestStartFrom_NoTransitiveDeps_RunsAllRemaining(t *testing.T) {
	// Targeting the very first step is a no-op for skipping, but must not
	// crash and must still execute every step normally.
	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromStep = "step1"
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), linearWorkflow(), nil, nil)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	for _, id := range []string{"step1", "step2", "step3", "step4", "step5"} {
		if state.Steps[id].Status != StatusCompleted {
			t.Errorf("step %s status = %v, want Completed", id, state.Steps[id].Status)
		}
	}
}

func TestStartFrom_FromStateWithoutStartFrom_IsRejectedAtCLI(t *testing.T) {
	// The CLI rejects --from-state without --start-from. The executor itself
	// silently ignores StartFromState if StartFromStep is empty (no skip set
	// to apply to). Verify executor behaviour.
	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.StartFromState = &ExecutionState{
		Steps:     map[string]StepResult{"step1": {StepID: "step1", Output: "x"}},
		Variables: map[string]interface{}{},
	}
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), linearWorkflow(), nil, nil)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %v, want Completed", state.Status)
	}
	for _, id := range []string{"step1", "step2", "step3", "step4", "step5"} {
		if state.Steps[id].Status != StatusCompleted {
			t.Errorf("step %s status = %v, want Completed (StartFromStep empty)", id, state.Steps[id].Status)
		}
	}
}
