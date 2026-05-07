package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Integration tests for real pipeline execution without mocks.
// These tests use actual file I/O and the real Executor in DryRun mode.

// =============================================================================
// Test Real Pipeline YAML Parsing from Files (ntm-b3si)
// =============================================================================

func TestIntegration_ParseRealWorkflowFile_Simple(t *testing.T) {

	// Create a real workflow YAML file
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "simple-workflow.yaml")

	content := `
schema_version: "2.0"
name: simple-workflow
description: A simple integration test workflow

steps:
  - id: analyze
    agent: claude
    prompt: Analyze the requirements

  - id: implement
    agent: codex
    prompt: Implement based on analysis
    depends_on:
      - analyze

  - id: review
    agent: claude
    prompt: Review the implementation
    depends_on:
      - implement
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	// Parse the real file
	workflow, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	// Verify workflow structure
	if workflow.Name != "simple-workflow" {
		t.Errorf("workflow.Name = %q, want %q", workflow.Name, "simple-workflow")
	}
	if len(workflow.Steps) != 3 {
		t.Errorf("len(workflow.Steps) = %d, want 3", len(workflow.Steps))
	}

	// Verify step dependencies
	implementStep := workflow.Steps[1]
	if len(implementStep.DependsOn) != 1 || implementStep.DependsOn[0] != "analyze" {
		t.Errorf("implement step DependsOn = %v, want [analyze]", implementStep.DependsOn)
	}
}

func TestIntegration_ParseRealWorkflowFile_WithParallel(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "parallel-workflow.yaml")

	content := `
schema_version: "2.0"
name: parallel-workflow
description: Workflow with parallel execution

steps:
  - id: parallel_tasks
    parallel:
      - id: task_a
        agent: claude
        prompt: Handle task A

      - id: task_b
        agent: codex
        prompt: Handle task B

      - id: task_c
        agent: gemini
        prompt: Handle task C

  - id: combine
    agent: claude
    prompt: Combine all results from ${steps.parallel_tasks}
    depends_on:
      - parallel_tasks
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	if len(workflow.Steps) != 2 {
		t.Errorf("len(workflow.Steps) = %d, want 2", len(workflow.Steps))
	}

	parallelStep := workflow.Steps[0]
	if len(parallelStep.Parallel.Steps) != 3 {
		t.Errorf("len(parallelStep.Parallel.Steps) = %d, want 3", len(parallelStep.Parallel.Steps))
	}
}

func TestIntegration_ParseRealWorkflowFile_WithLoops(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "loop-workflow.yaml")

	content := `
schema_version: "2.0"
name: loop-workflow
description: Workflow with loop constructs

vars:
  files:
    type: array
    default: ["file1.go", "file2.go", "file3.go"]

steps:
  - id: process_files
    loop:
      items: "${vars.files}"
      as: file
      steps:
        - id: analyze_file
          agent: claude
          prompt: Analyze ${loop.file}

        - id: fix_file
          agent: codex
          prompt: Fix issues in ${loop.file}
          depends_on:
            - analyze_file

  - id: summary
    agent: claude
    prompt: Summarize all file processing
    depends_on:
      - process_files
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	// Verify loop step
	loopStep := workflow.Steps[0]
	if loopStep.Loop == nil {
		t.Fatal("loopStep.Loop should not be nil")
	}
	if loopStep.Loop.As != "file" {
		t.Errorf("loopStep.Loop.As = %q, want %q", loopStep.Loop.As, "file")
	}
	if len(loopStep.Loop.Steps) != 2 {
		t.Errorf("len(loopStep.Loop.Steps) = %d, want 2", len(loopStep.Loop.Steps))
	}
}

func TestIntegration_ParseRealWorkflowFile_WithVariables(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "vars-workflow.yaml")

	content := `
schema_version: "2.0"
name: vars-workflow
description: Workflow with variable definitions

vars:
  project_name:
    type: string
    default: "my-project"
    description: "Name of the project"

  max_retries:
    type: integer
    default: 3

  features:
    type: array
    default: ["auth", "api", "ui"]

settings:
  on_error: continue
  timeout: 15m

steps:
  - id: setup
    agent: claude
    prompt: Setup project ${vars.project_name}

  - id: implement
    agent: codex
    prompt: Implement features for ${vars.project_name}
    depends_on:
      - setup
    retry_count: 3
    on_error: retry
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	// Verify variables
	if len(workflow.Vars) != 3 {
		t.Errorf("len(workflow.Vars) = %d, want 3", len(workflow.Vars))
	}

	projectVar := workflow.Vars["project_name"]
	if projectVar.Type != "string" {
		t.Errorf("projectVar.Type = %q, want %q", projectVar.Type, "string")
	}
	if projectVar.Default != "my-project" {
		t.Errorf("projectVar.Default = %v, want %q", projectVar.Default, "my-project")
	}

	// Verify settings
	if workflow.Settings.OnError != ErrorActionContinue {
		t.Errorf("workflow.Settings.OnError = %q, want %q", workflow.Settings.OnError, ErrorActionContinue)
	}
}

func TestIntegration_ParseRealWorkflowFile_TOML(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "workflow.toml")

	content := `
schema_version = "2.0"
name = "toml-workflow"
description = "A TOML format workflow"

[[steps]]
id = "design"
agent = "claude"
prompt = "Design the architecture"

[[steps]]
id = "build"
agent = "codex"
prompt = "Build based on design"
depends_on = ["design"]
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Validation failed: %v", result.Errors)
	}

	if workflow.Name != "toml-workflow" {
		t.Errorf("workflow.Name = %q, want %q", workflow.Name, "toml-workflow")
	}
	if len(workflow.Steps) != 2 {
		t.Errorf("len(workflow.Steps) = %d, want 2", len(workflow.Steps))
	}
}

// =============================================================================
// Test Pipeline Stage Execution with DryRun Mode (ntm-s25s)
// =============================================================================

func TestIntegration_ExecuteWorkflow_DryRun_Simple(t *testing.T) {

	// Create workflow from a real file
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "exec-test.yaml")

	content := `
schema_version: "2.0"
name: exec-test-workflow
description: Test workflow execution

steps:
  - id: step1
    agent: claude
    prompt: First step

  - id: step2
    agent: codex
    prompt: Second step
    depends_on:
      - step1

  - id: step3
    agent: claude
    prompt: Third step
    depends_on:
      - step2
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	// Configure executor for dry run
	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir
	cfg.WorkflowFile = workflowPath

	executor := NewExecutor(cfg)

	// Execute workflow
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify execution completed
	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify all steps completed in dry run
	expectedSteps := []string{"step1", "step2", "step3"}
	for _, stepID := range expectedSteps {
		result, ok := state.Steps[stepID]
		if !ok {
			t.Errorf("missing step result for %s", stepID)
			continue
		}
		if result.Status != StatusCompleted {
			t.Errorf("step %s status = %v, want %v", stepID, result.Status, StatusCompleted)
		}
	}
}

func TestIntegration_ExecuteWorkflow_DryRun_WithParallel(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "parallel-exec.yaml")

	content := `
schema_version: "2.0"
name: parallel-exec-workflow

steps:
  - id: init
    agent: claude
    prompt: Initialize

  - id: parallel_work
    parallel:
      - id: worker_a
        agent: claude
        prompt: Worker A task

      - id: worker_b
        agent: codex
        prompt: Worker B task

      - id: worker_c
        agent: gemini
        prompt: Worker C task
    depends_on:
      - init

  - id: finalize
    agent: claude
    prompt: Finalize results
    depends_on:
      - parallel_work
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify all parallel steps completed
	parallelSteps := []string{"worker_a", "worker_b", "worker_c"}
	for _, stepID := range parallelSteps {
		result, ok := state.Steps[stepID]
		if !ok {
			t.Errorf("missing step result for parallel step %s", stepID)
			continue
		}
		if result.Status != StatusCompleted {
			t.Errorf("parallel step %s status = %v, want %v", stepID, result.Status, StatusCompleted)
		}
	}
}

func TestIntegration_ExecuteWorkflow_DryRun_WithVariables(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "vars-exec.yaml")

	content := `
schema_version: "2.0"
name: vars-exec-workflow

vars:
  environment:
    type: string
    default: "development"

  debug_mode:
    type: boolean
    default: true

steps:
  - id: setup
    agent: claude
    prompt: Setup for ${vars.environment} environment

  - id: configure
    agent: codex
    prompt: Configure with debug=${vars.debug_mode}
    depends_on:
      - setup
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	// Pass override variables
	vars := map[string]interface{}{
		"environment": "production",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, vars, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify variables were set correctly
	if state.Variables["environment"] != "production" {
		t.Errorf("state.Variables[environment] = %v, want %q", state.Variables["environment"], "production")
	}
	if state.Variables["debug_mode"] != true {
		t.Errorf("state.Variables[debug_mode] = %v, want true", state.Variables["debug_mode"])
	}
}

func TestIntegration_ExecuteWorkflow_DryRun_ProgressEvents(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "progress-exec.yaml")

	content := `
schema_version: "2.0"
name: progress-workflow

steps:
  - id: step1
    agent: claude
    prompt: Step 1

  - id: step2
    agent: codex
    prompt: Step 2
    depends_on:
      - step1
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	// Create progress channel
	progressCh := make(chan ProgressEvent, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, progressCh)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Drain progress channel
	close(progressCh)
	var events []ProgressEvent
	for event := range progressCh {
		events = append(events, event)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Should have received at least start and complete events
	if len(events) < 2 {
		t.Errorf("received %d progress events, want at least 2", len(events))
	}

	// Check for workflow_start event
	hasStart := false
	hasComplete := false
	for _, event := range events {
		if event.Type == "workflow_start" {
			hasStart = true
		}
		if event.Type == "workflow_complete" {
			hasComplete = true
		}
	}

	if !hasStart {
		t.Error("missing workflow_start progress event")
	}
	if !hasComplete {
		t.Error("missing workflow_complete progress event")
	}
}

// =============================================================================
// Test Pipeline Output Capture Between Stages (ntm-90z8)
// =============================================================================

func TestIntegration_OutputCapture_StepResults(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "output-workflow.yaml")

	content := `
schema_version: "2.0"
name: output-capture-workflow

steps:
  - id: producer
    agent: claude
    prompt: Produce some output

  - id: consumer
    agent: codex
    prompt: Consume output from ${steps.producer.output}
    depends_on:
      - producer
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify both steps have results
	producerResult, ok := state.Steps["producer"]
	if !ok {
		t.Fatal("missing producer step result")
	}
	if producerResult.Status != StatusCompleted {
		t.Errorf("producer status = %v, want %v", producerResult.Status, StatusCompleted)
	}

	consumerResult, ok := state.Steps["consumer"]
	if !ok {
		t.Fatal("missing consumer step result")
	}
	if consumerResult.Status != StatusCompleted {
		t.Errorf("consumer status = %v, want %v", consumerResult.Status, StatusCompleted)
	}
}

func TestIntegration_OutputCapture_StatePersistence(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "persist-workflow.yaml")

	content := `
schema_version: "2.0"
name: persist-workflow

steps:
  - id: step1
    agent: claude
    prompt: First step

  - id: step2
    agent: codex
    prompt: Second step
    depends_on:
      - step1
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir
	cfg.RunID = "test-persist-run"

	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify state was persisted to disk
	loadedState, err := LoadState(tmpDir, "test-persist-run")
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if loadedState.RunID != "test-persist-run" {
		t.Errorf("loaded RunID = %q, want %q", loadedState.RunID, "test-persist-run")
	}
	if loadedState.WorkflowID != "persist-workflow" {
		t.Errorf("loaded WorkflowID = %q, want %q", loadedState.WorkflowID, "persist-workflow")
	}
	if loadedState.Status != StatusCompleted {
		t.Errorf("loaded Status = %v, want %v", loadedState.Status, StatusCompleted)
	}

	// Verify step results were persisted
	if len(loadedState.Steps) != 2 {
		t.Errorf("loaded len(Steps) = %d, want 2", len(loadedState.Steps))
	}
}

func TestIntegration_OutputCapture_ParallelResults(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "parallel-output.yaml")

	content := `
schema_version: "2.0"
name: parallel-output-workflow

steps:
  - id: parallel_producers
    parallel:
      - id: producer_a
        agent: claude
        prompt: Produce A

      - id: producer_b
        agent: codex
        prompt: Produce B

  - id: aggregator
    agent: claude
    prompt: Aggregate from ${steps.parallel_producers.output}
    depends_on:
      - parallel_producers
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %v, want %v", state.Status, StatusCompleted)
	}

	// Verify all producers have results
	producers := []string{"producer_a", "producer_b"}
	for _, id := range producers {
		result, ok := state.Steps[id]
		if !ok {
			t.Errorf("missing result for producer %s", id)
			continue
		}
		if result.Status != StatusCompleted {
			t.Errorf("producer %s status = %v, want %v", id, result.Status, StatusCompleted)
		}
	}

	// Verify aggregator completed
	aggregatorResult, ok := state.Steps["aggregator"]
	if !ok {
		t.Fatal("missing aggregator step result")
	}
	if aggregatorResult.Status != StatusCompleted {
		t.Errorf("aggregator status = %v, want %v", aggregatorResult.Status, StatusCompleted)
	}
}

// =============================================================================
// Error Cases and Edge Conditions
// =============================================================================

func TestIntegration_ParseWorkflow_InvalidYAML(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "invalid.yaml")

	content := `
schema_version: "2.0"
name: invalid
steps:
  - id: step1
  this is not valid yaml
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	_, _, err := LoadAndValidate(workflowPath)
	if err == nil {
		t.Error("LoadAndValidate() should return error for invalid YAML")
	}
}

func TestIntegration_ParseWorkflow_MissingSchemaVersion(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "no-version.yaml")

	content := `
name: no-version-workflow
steps:
  - id: step1
    prompt: Test
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	_, result, err := LoadAndValidate(workflowPath)
	if err != nil {
		// Parse might fail
		return
	}
	if result.Valid {
		t.Error("Validation should fail for missing schema_version")
	}
}

func TestIntegration_ExecuteWorkflow_CyclicDependency(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "cyclic.yaml")

	content := `
schema_version: "2.0"
name: cyclic-workflow

steps:
  - id: step_a
    agent: claude
    prompt: Step A
    depends_on:
      - step_b

  - id: step_b
    agent: codex
    prompt: Step B
    depends_on:
      - step_a
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	_, result, _ := LoadAndValidate(workflowPath)
	if result.Valid {
		t.Error("Validation should fail for cyclic dependency")
	}

	// Find cycle error
	hasCycleError := false
	for _, e := range result.Errors {
		if e.Field == "depends_on" {
			hasCycleError = true
			break
		}
	}
	if !hasCycleError {
		t.Error("Should have a depends_on error for cycle")
	}
}

func TestIntegration_ExecuteWorkflow_ContextCancellation(t *testing.T) {

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "cancel-test.yaml")

	content := `
schema_version: "2.0"
name: cancel-workflow

steps:
  - id: step1
    agent: claude
    prompt: Step 1
`

	if err := os.WriteFile(workflowPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write workflow file: %v", err)
	}

	workflow, _, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error: %v", err)
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	cfg.ProjectDir = tmpDir

	executor := NewExecutor(cfg)

	// Create already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state, _ := executor.Run(ctx, workflow, nil, nil)

	// With pre-cancelled context, should be cancelled or failed
	if state.Status != StatusCancelled && state.Status != StatusFailed {
		t.Errorf("state.Status = %v, want StatusCancelled or StatusFailed", state.Status)
	}
}
