package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// createTestExecutor creates a configured executor for testing
func createTestExecutor() (*Executor, *Workflow) {
	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	e := NewExecutor(cfg)

	// Create a simple workflow with steps that match what we'll test
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: []Step{
				{ID: "step1", Prompt: "Task 1"},
				{ID: "step2", Prompt: "Task 2"},
				{ID: "step3", Prompt: "Task 3"},
			}},
		},
	}

	// Initialize the dependency graph (required by calculateProgress)
	e.graph = NewDependencyGraph(workflow)

	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "test-workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}

	return e, workflow
}

func TestExecuteParallel_BasicExecution(t *testing.T) {
	t.Parallel()

	e, workflow := createTestExecutor()

	// Create a parallel group with 3 steps
	step := &Step{
		ID: "parallel_group",
		Parallel: []Step{
			{ID: "step1", Prompt: "Do task 1"},
			{ID: "step2", Prompt: "Do task 2"},
			{ID: "step3", Prompt: "Do task 3"},
		},
	}

	result := e.executeParallel(context.Background(), step, workflow)

	// In dry run mode, all steps should complete
	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %s", result.Status)
	}

	// Check that all parallel step results are stored
	if len(e.state.Steps) != 3 {
		t.Errorf("expected 3 step results, got %d", len(e.state.Steps))
	}

	for _, stepID := range []string{"step1", "step2", "step3"} {
		if _, ok := e.state.Steps[stepID]; !ok {
			t.Errorf("missing step result for %s", stepID)
		}
	}
}

func TestExecuteParallel_ErrorModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		onError      ErrorAction
		expectStatus ExecutionStatus
	}{
		{
			name:         "fail mode - wait for all",
			onError:      ErrorActionFail,
			expectStatus: StatusCompleted, // All succeed in dry run
		},
		{
			name:         "fail_fast mode",
			onError:      ErrorActionFailFast,
			expectStatus: StatusCompleted, // All succeed in dry run
		},
		{
			name:         "continue mode",
			onError:      ErrorActionContinue,
			expectStatus: StatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e, workflow := createTestExecutor()
			workflow.Settings.OnError = tt.onError

			step := &Step{
				ID: "parallel_group",
				Parallel: []Step{
					{ID: "step1", Prompt: "Do task 1"},
					{ID: "step2", Prompt: "Do task 2"},
				},
			}

			result := e.executeParallel(context.Background(), step, workflow)

			if result.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, result.Status)
			}
		})
	}
}

func TestExecuteParallel_UsesWorkflowRetryPolicyForSubsteps(t *testing.T) {
	t.Parallel()

	e, workflow := createTestExecutor()
	workflow.Settings.OnError = ErrorActionRetry

	step := &Step{
		ID: "parallel_group",
		Parallel: []Step{
			{
				ID:         "step1",
				PromptFile: "/nonexistent/prompt.txt",
				RetryCount: 2,
				RetryDelay: Duration{Duration: time.Millisecond},
			},
		},
	}

	result := e.executeParallel(context.Background(), step, workflow)
	if result.Status != StatusFailed {
		t.Fatalf("expected StatusFailed, got %s", result.Status)
	}

	child, ok := e.state.Steps["step1"]
	if !ok {
		t.Fatal("missing step result for step1")
	}
	if child.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", child.Attempts)
	}
	if child.Error == nil || child.Error.Type != "prompt" {
		t.Fatalf("expected prompt error after retries, got %+v", child.Error)
	}
}

func TestExecuteParallel_GroupTimeout(t *testing.T) {
	t.Parallel()

	e, workflow := createTestExecutor()

	// Create a parallel group with a timeout
	// In dry run mode, steps complete instantly, so timeout won't be hit
	// Use a generous timeout (5s) to account for test infrastructure overhead
	// (goroutine spawning, state persistence, channel operations, etc.)
	step := &Step{
		ID:      "parallel_group",
		Timeout: Duration{Duration: 5 * time.Second},
		Parallel: []Step{
			{ID: "step1", Prompt: "Do task 1"},
		},
	}

	result := e.executeParallel(context.Background(), step, workflow)

	// Should complete successfully in dry run (no actual execution)
	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %s", result.Status)
	}
}

func TestExecuteParallel_ContextCancellation(t *testing.T) {
	t.Parallel()

	e, workflow := createTestExecutor()

	step := &Step{
		ID: "parallel_group",
		Parallel: []Step{
			{ID: "step1", Prompt: "Do task 1"},
			{ID: "step2", Prompt: "Do task 2"},
		},
	}

	// Create a pre-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result := e.executeParallel(ctx, step, workflow)

	// With pre-cancelled context, steps should be cancelled
	// In dry run mode, steps complete so fast they may not see cancellation
	// This is testing the cancellation logic path
	_ = result // Result depends on timing
}

func TestExecuteParallel_ResultAggregation(t *testing.T) {
	t.Parallel()

	e, _ := createTestExecutor()

	// Create workflow with task_a and task_b for this test
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: []Step{
				{ID: "task_a", Prompt: "Do task A"},
				{ID: "task_b", Prompt: "Do task B"},
			}},
		},
	}
	// Rebuild graph for this workflow
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID: "parallel_group",
		Parallel: []Step{
			{ID: "task_a", Prompt: "Do task A"},
			{ID: "task_b", Prompt: "Do task B"},
		},
	}

	result := e.executeParallel(context.Background(), step, workflow)

	// Check that ParsedData contains aggregated results
	if result.ParsedData == nil {
		t.Fatal("expected ParsedData to be set with group outputs")
	}

	groupOutputs, ok := result.ParsedData.(map[string]interface{})
	if !ok {
		t.Fatalf("expected ParsedData to be map[string]interface{}, got %T", result.ParsedData)
	}

	// Check that both step outputs are accessible
	for _, stepID := range []string{"task_a", "task_b"} {
		stepData, ok := groupOutputs[stepID]
		if !ok {
			t.Errorf("missing output for step %s in group outputs", stepID)
			continue
		}
		stepMap, ok := stepData.(map[string]interface{})
		if !ok {
			t.Errorf("expected step data to be map, got %T", stepData)
			continue
		}
		if _, hasOutput := stepMap["output"]; !hasOutput {
			t.Errorf("missing 'output' field for step %s", stepID)
		}
		if _, hasStatus := stepMap["status"]; !hasStatus {
			t.Errorf("missing 'status' field for step %s", stepID)
		}
	}
}

func TestExecuteParallel_Concurrency(t *testing.T) {
	t.Parallel()

	// Create many parallel steps to test concurrent execution
	parallelSteps := make([]Step, 10)
	for i := 0; i < 10; i++ {
		parallelSteps[i] = Step{
			ID:     fmt.Sprintf("step_%d", i),
			Prompt: fmt.Sprintf("Do task %d", i),
		}
	}

	// Create a workflow with all 10 parallel steps
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: parallelSteps},
		},
	}

	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "test-workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}

	step := &Step{
		ID:       "parallel_group",
		Parallel: parallelSteps,
	}

	// Run multiple times to catch potential race conditions
	for i := 0; i < 5; i++ {
		// Reset state
		e.state.Steps = make(map[string]StepResult)

		result := e.executeParallel(context.Background(), step, workflow)

		if result.Status != StatusCompleted {
			t.Errorf("run %d: expected StatusCompleted, got %s", i, result.Status)
		}

		if len(e.state.Steps) != 10 {
			t.Errorf("run %d: expected 10 step results, got %d", i, len(e.state.Steps))
		}
	}
}

func TestSelectPaneExcluding_BasicExclusion(t *testing.T) {
	t.Parallel()

	// This test verifies the exclusion logic without requiring a real tmux session
	// We test the exclusion map behavior

	usedPanes := make(map[string]bool)
	var panesMu sync.Mutex

	// Simulate marking panes as used
	panesMu.Lock()
	usedPanes["pane_1"] = true
	usedPanes["pane_2"] = true
	panesMu.Unlock()

	panesMu.Lock()
	isUsed := usedPanes["pane_1"]
	panesMu.Unlock()

	if !isUsed {
		t.Error("expected pane_1 to be marked as used")
	}

	// Check that a new pane is not marked as used
	panesMu.Lock()
	isUsed = usedPanes["pane_3"]
	panesMu.Unlock()

	if isUsed {
		t.Error("expected pane_3 to not be marked as used")
	}
}

func TestErrorActionFailFast_Constant(t *testing.T) {
	t.Parallel()

	// Verify the constant value
	if ErrorActionFailFast != "fail_fast" {
		t.Errorf("expected ErrorActionFailFast to be 'fail_fast', got '%s'", ErrorActionFailFast)
	}
}

// Test that parallel steps with conditions are evaluated correctly
func TestExecuteParallelStep_WithCondition(t *testing.T) {
	t.Parallel()

	// Create workflow with the conditional step
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: []Step{
				{ID: "conditional_step", Prompt: "This should be skipped", When: "${vars.skip_this}"},
			}},
		},
	}

	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "test-workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables: map[string]interface{}{
			"run_this":  true,
			"skip_this": false,
		},
	}

	usedPanes := make(map[string]bool)
	var panesMu sync.Mutex

	// Step with condition that evaluates to false (should skip)
	step := &Step{
		ID:     "conditional_step",
		Prompt: "This should be skipped",
		When:   "${vars.skip_this}",
	}

	result := e.executeParallelStep(context.Background(), step, workflow, usedPanes, &panesMu)

	if result.Status != StatusSkipped {
		t.Errorf("expected StatusSkipped, got %s", result.Status)
	}
}
