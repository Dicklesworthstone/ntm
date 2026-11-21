package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestForeachContinueAggregatesAllIterationFailures(t *testing.T) {
	t.Parallel()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-error-aggregation",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:      "fanout",
		OnError: ErrorActionContinue,
		Foreach: &ForeachConfig{
			Items: `["ok-0","bad-1","bad-2","ok-3","bad-4"]`,
			Steps: []Step{{
				ID:      "maybe",
				Command: `case '${item}' in bad-*) echo '${item} failed'; exit 7;; *) printf '%s' '${item}';; esac`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, want completed; error=%+v", result.Status, result.Error)
	}
	if result.Error == nil {
		t.Fatal("foreach error = nil, want aggregate error metadata")
	}
	if got := len(result.Error.Aggregated); got != 3 {
		t.Fatalf("aggregated errors = %d, want 3: %+v", got, result.Error.Aggregated)
	}
	if !strings.Contains(result.Error.Message, "3 of 5 iterations failed") {
		t.Fatalf("message = %q, want aggregate count", result.Error.Message)
	}
	if !strings.Contains(result.Error.Details, `"iteration":1`) || !strings.Contains(result.Error.Details, `"iteration":4`) {
		t.Fatalf("details = %q, want iteration summaries", result.Error.Details)
	}
}

func TestForeachFailFastAggregatesFirstFailureAndCancelsRemaining(t *testing.T) {
	t.Parallel()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-fail-fast-aggregation",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:      "fanout",
		OnError: ErrorActionFailFast,
		Foreach: &ForeachConfig{
			Items: `["bad-0","bad-1","bad-2","bad-3","bad-4"]`,
			Steps: []Step{{
				ID:      "maybe",
				Command: `echo '${item} failed'; exit 7`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusFailed {
		t.Fatalf("foreach status = %s, want failed", result.Status)
	}
	if result.Error == nil || len(result.Error.Aggregated) != 1 {
		t.Fatalf("aggregate error = %+v, want exactly one failed iteration", result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 5 {
		t.Fatalf("iterations = %d, want 5 including cancelled remaining", len(iterations))
	}
	for _, iteration := range iterations[1:] {
		if len(iteration.Results) != 1 || iteration.Results[0].Status != StatusCancelled {
			t.Fatalf("remaining iteration = %#v, want cancelled result", iteration)
		}
	}
}

func TestParallelAggregatesMultipleSubstepFailures(t *testing.T) {
	t.Parallel()
	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	cfg.ProjectDir = t.TempDir()
	e := NewExecutor(cfg)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-error-aggregation",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "parallel_group",
			Parallel: ParallelSpec{Steps: []Step{
				{ID: "ok", Prompt: "ok"},
				{ID: "bad_one", PromptFile: "/definitely/missing-one.txt"},
				{ID: "bad_two", PromptFile: "/definitely/missing-two.txt"},
				{ID: "ok_two", Prompt: "ok"},
				{ID: "ok_three", Prompt: "ok"},
			}},
		}},
	}
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID:         "test-run",
		WorkflowID:    workflow.Name,
		Status:        StatusRunning,
		StartedAt:     time.Now(),
		Steps:         make(map[string]StepResult),
		Variables:     make(map[string]interface{}),
		ParallelState: make(map[string]ParallelGroupState),
	}

	result := e.executeParallel(context.Background(), &workflow.Steps[0], workflow)

	if result.Status != StatusFailed {
		t.Fatalf("parallel status = %s, want failed", result.Status)
	}
	if result.Error == nil {
		t.Fatal("parallel error = nil, want aggregate error")
	}
	if got := len(result.Error.Aggregated); got != 2 {
		t.Fatalf("aggregated errors = %d, want 2: %+v", got, result.Error.Aggregated)
	}
	if !strings.Contains(result.Error.Message, "2 of 5 parallel steps failed") {
		t.Fatalf("message = %q, want parallel failure count", result.Error.Message)
	}
	if !strings.Contains(result.Error.Details, "bad_one") || !strings.Contains(result.Error.Details, "bad_two") {
		t.Fatalf("details = %q, want both failed sub-step IDs", result.Error.Details)
	}
}

// TestForeachStructuredFailureDetails covers bd-wio84: foreach failures must
// emit kind=foreach / step_id=... / reason=... details that mirror the
// stepRuntimeError contract used by command and template steps.
func TestForeachStructuredFailureDetails(t *testing.T) {
	t.Parallel()
	t.Run("source failure", func(t *testing.T) {
		t.Parallel()
		got := foreachStructuredError("fanout", "foreach_source", "boom", "")
		if got.Type != "foreach_source" {
			t.Errorf("Type = %q, want foreach_source", got.Type)
		}
		for _, want := range []string{"kind=foreach", "step_id=fanout", "reason=boom"} {
			if !strings.Contains(got.Details, want) {
				t.Errorf("Details %q missing %q", got.Details, want)
			}
		}
	})
	t.Run("limit failure", func(t *testing.T) {
		t.Parallel()
		got := foreachStructuredError("fanout", "limit", "200 iterations exceed max_foreach_iterations 100", "shrink the source")
		if !strings.Contains(got.Details, "hint=shrink the source") {
			t.Errorf("Details %q missing hint", got.Details)
		}
	})
	t.Run("iteration failure carries iteration index", func(t *testing.T) {
		t.Parallel()
		got := foreachStructuredErrorAtIteration("fanout", "foreach", "iter-3 failed", "", 3)
		if !strings.Contains(got.Details, "iteration=3") {
			t.Errorf("Details %q missing iteration=3", got.Details)
		}
	})
	t.Run("iteration index omitted when negative", func(t *testing.T) {
		t.Parallel()
		got := foreachStructuredErrorAtIteration("fanout", "foreach", "no iter info", "", -1)
		if strings.Contains(got.Details, "iteration=") {
			t.Errorf("Details %q should not include iteration field", got.Details)
		}
	})
}

// TestForeachAggregateErrorIncludesIterationZero is the bd-u92d2 regression:
// when the foreach failure happens at index 0 the JSON details must still
// carry "iteration":0 instead of omitting the field via json:",omitempty".
func TestForeachAggregateErrorIncludesIterationZero(t *testing.T) {
	t.Parallel()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-iter-zero-aggregation",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:      "fanout",
		OnError: ErrorActionContinue,
		Foreach: &ForeachConfig{
			Items: `["bad-0","ok-1"]`,
			Steps: []Step{{
				ID:      "maybe",
				Command: `case '${item}' in bad-*) echo '${item} failed'; exit 7;; *) printf '%s' '${item}';; esac`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Error == nil {
		t.Fatal("foreach error = nil, want aggregate error metadata")
	}
	if !strings.Contains(result.Error.Details, `"iteration":0`) {
		t.Fatalf("details = %q, want iteration zero preserved (bd-u92d2)", result.Error.Details)
	}
}
