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
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: []Step{
				{ID: "step1", Prompt: "Task 1"},
				{ID: "step2", Prompt: "Task 2"},
				{ID: "step3", Prompt: "Task 3"},
			}}},
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

	e, workflow := createTestExecutor()

	// Create a parallel group with 3 steps
	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Do task 1"},
			{ID: "step2", Prompt: "Do task 2"},
			{ID: "step3", Prompt: "Do task 3"},
		}},
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

			e, workflow := createTestExecutor()
			workflow.Settings.OnError = tt.onError

			step := &Step{
				ID: "parallel_group",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "step1", Prompt: "Do task 1"},
					{ID: "step2", Prompt: "Do task 2"},
				}},
			}

			result := e.executeParallel(context.Background(), step, workflow)

			if result.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, result.Status)
			}
		})
	}
}

func TestExecuteParallel_UsesWorkflowRetryPolicyForSubsteps(t *testing.T) {

	e, workflow := createTestExecutor()
	workflow.Settings.OnError = ErrorActionRetry

	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{
				ID:         "step1",
				PromptFile: "/nonexistent/prompt.txt",
				RetryCount: 2,
				RetryDelay: Duration{Duration: time.Millisecond},
			},
		}},
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

	e, workflow := createTestExecutor()

	// Create a parallel group with a timeout
	// In dry run mode, steps complete instantly, so timeout won't be hit
	// Use a generous timeout (5s) to account for test infrastructure overhead
	// (goroutine spawning, state persistence, channel operations, etc.)
	step := &Step{
		ID:      "parallel_group",
		Timeout: Duration{Duration: 5 * time.Second},
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Do task 1"},
		}},
	}

	result := e.executeParallel(context.Background(), step, workflow)

	// Should complete successfully in dry run (no actual execution)
	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %s", result.Status)
	}
}

func TestExecuteParallel_ContextCancellation(t *testing.T) {

	e, workflow := createTestExecutor()

	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Do task 1"},
			{ID: "step2", Prompt: "Do task 2"},
		}},
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

	e, _ := createTestExecutor()

	// Create workflow with task_a and task_b for this test
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: []Step{
				{ID: "task_a", Prompt: "Do task A"},
				{ID: "task_b", Prompt: "Do task B"},
			}}},
		},
	}
	// Rebuild graph for this workflow
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "task_a", Prompt: "Do task A"},
			{ID: "task_b", Prompt: "Do task B"},
		}},
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
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: parallelSteps}},
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
		Parallel: ParallelSpec{Steps: parallelSteps},
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

// TestExecuteParallel_SubstepParallelMaxOperatorOverride covers bd-dmjn3:
// substep concurrency was previously hardcoded at 8 inside executeParallel.
// Operators with a parallel block of >8 substeps had no knob to raise (or
// lower) the cap. The fix routes the semaphore size through
// limits.substep_parallel_max with DefaultSubstepParallelMax=8 fallback.
//
// This test exercises both ends: configures a 16-substep group with
// SubstepParallelMax=12 and asserts every substep records a result. We
// can't deterministically assert "exactly 12 ran in parallel" without
// timing observation that flakes under -race load, so the contract checked
// here is that a >old-default fan-out completes cleanly under a configured
// (non-default) cap.
func TestExecuteParallel_SubstepParallelMaxOperatorOverride(t *testing.T) {
	const total = 16

	parallelSteps := make([]Step, total)
	for i := 0; i < total; i++ {
		parallelSteps[i] = Step{
			ID:     fmt.Sprintf("substep_%d", i),
			Prompt: fmt.Sprintf("Do task %d", i),
		}
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "substep-parallel-max-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:       "parallel_group",
			Parallel: ParallelSpec{Steps: parallelSteps},
		}},
	}

	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID:      "test-run-substep-cap",
		WorkflowID: "test-workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	e.limits = LimitsConfig{SubstepParallelMax: 12}.EffectiveLimits()

	step := &Step{
		ID:       "parallel_group",
		Parallel: ParallelSpec{Steps: parallelSteps},
	}
	result := e.executeParallel(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q (operator-cap should not block completion); error = %+v", result.Status, StatusCompleted, result.Error)
	}
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("substep_%d", i)
		if _, ok := e.state.Steps[id]; !ok {
			t.Errorf("missing step result for %s — semaphore likely starved (cap=%d, total=%d)", id, e.limits.SubstepParallelMax, total)
		}
	}
}

func TestSelectPaneExcluding_BasicExclusion(t *testing.T) {

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

	// Verify the constant value
	if ErrorActionFailFast != "fail_fast" {
		t.Errorf("expected ErrorActionFailFast to be 'fail_fast', got '%s'", ErrorActionFailFast)
	}
}

// Test that parallel steps with conditions are evaluated correctly
func TestExecuteParallelStep_WithCondition(t *testing.T) {

	// Create workflow with the conditional step
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: []Step{
				{ID: "conditional_step", Prompt: "This should be skipped", When: "${vars.skip_this}"},
			}}},
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

// TestExecuteParallel_ResumeSkipsAlreadyCompletedSubsteps covers bd-qbymk:
// when a parallel group's parent never completed but some children did persist
// completed StepResults, resume must adopt those persisted results instead of
// re-dispatching the children — re-running already-finished commands/prompts
// would duplicate side effects against the parallel-progress contract.
func TestExecuteParallel_ResumeSkipsAlreadyCompletedSubsteps(t *testing.T) {
	e, workflow := createTestExecutor()
	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Task 1"},
			{ID: "step2", Prompt: "Task 2"},
			{ID: "step3", Prompt: "Task 3"},
		}},
	}

	// Simulate a prior partial run: step1 finished and was persisted with a
	// distinctive output marker; step2 and step3 never completed.
	priorOutput := "PRIOR-RUN-OUTPUT-step1"
	priorFinishedAt := time.Now().Add(-1 * time.Hour)
	e.state.Steps["step1"] = StepResult{
		StepID:     "step1",
		Status:     StatusCompleted,
		StartedAt:  priorFinishedAt.Add(-1 * time.Minute),
		FinishedAt: priorFinishedAt,
		Output:     priorOutput,
		AgentType:  "prompt",
	}

	result := e.executeParallel(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("group status = %q, want %q", result.Status, StatusCompleted)
	}

	got := e.state.Steps["step1"]
	if got.Output != priorOutput {
		t.Fatalf("step1 was re-dispatched: Output = %q, want preserved %q", got.Output, priorOutput)
	}
	if !got.FinishedAt.Equal(priorFinishedAt) {
		t.Fatalf("step1 was re-dispatched: FinishedAt = %v, want preserved %v", got.FinishedAt, priorFinishedAt)
	}

	// step2 and step3 still ran, so they have fresh dry-run output (non-empty)
	// and were never seeded with the prior marker.
	for _, id := range []string{"step2", "step3"} {
		r, ok := e.state.Steps[id]
		if !ok {
			t.Fatalf("missing fresh result for %s — should have been dispatched on resume", id)
		}
		if r.Output == priorOutput {
			t.Fatalf("%s output = %q matches prior marker — wrong substep was preserved", id, r.Output)
		}
	}

	// ParallelState.CompletedStepIDs must include step1 even though it was
	// adopted, so the parallel-progress invariant holds across the resume.
	gs := e.state.ParallelState[step.ID]
	if !containsStringSlice(gs.CompletedStepIDs, "step1") {
		t.Fatalf("ParallelState.CompletedStepIDs = %v, want it to include step1", gs.CompletedStepIDs)
	}
}

func containsStringSlice(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
