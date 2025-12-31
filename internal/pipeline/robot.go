// Package pipeline provides workflow execution for AI agent orchestration.
// robot.go implements the --robot-pipeline-* APIs for machine-readable output.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// PipelineRegistry tracks running pipeline executions
var (
	pipelineRegistry = make(map[string]*PipelineExecution)
	pipelineMu       sync.RWMutex
)

// Robot error codes
const (
	ErrCodeInvalidFlag     = "INVALID_FLAG"
	ErrCodeSessionNotFound = "SESSION_NOT_FOUND"
	ErrCodeInternalError   = "INTERNAL_ERROR"
)

// RobotResponse is the base structure for robot command outputs
type RobotResponse struct {
	Success   bool   `json:"success"`
	Timestamp string `json:"timestamp"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

// NewRobotResponse creates a new robot response
func NewRobotResponse(success bool) RobotResponse {
	return RobotResponse{
		Success:   success,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// NewErrorResponse creates an error robot response
func NewErrorResponse(err error, code string, hint string) RobotResponse {
	return RobotResponse{
		Success:   false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     err.Error(),
		ErrorCode: code,
		Hint:      hint,
	}
}

// PipelineExecution tracks a running pipeline
type PipelineExecution struct {
	RunID       string                  `json:"run_id"`
	WorkflowID  string                  `json:"workflow_id"`
	Session     string                  `json:"session"`
	Status      string                  `json:"status"`
	StartedAt   time.Time               `json:"started_at"`
	FinishedAt  *time.Time              `json:"finished_at,omitempty"`
	CurrentStep string                  `json:"current_step,omitempty"`
	Progress    PipelineProgress        `json:"progress"`
	Steps       map[string]PipelineStep `json:"steps"`
	Error       string                  `json:"error,omitempty"`

	// Internal
	executor *Executor
	cancelFn context.CancelFunc
}

// PipelineProgress tracks overall progress
type PipelineProgress struct {
	Completed int     `json:"completed"`
	Running   int     `json:"running"`
	Pending   int     `json:"pending"`
	Failed    int     `json:"failed"`
	Skipped   int     `json:"skipped"`
	Total     int     `json:"total"`
	Percent   float64 `json:"percent"`
}

// PipelineStep represents step status in pipeline output
type PipelineStep struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Agent       string `json:"agent,omitempty"`
	PaneUsed    string `json:"pane_used,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	OutputLines int    `json:"output_lines,omitempty"`
	Error       string `json:"error,omitempty"`
}

// PipelineRunOptions configures a pipeline run
type PipelineRunOptions struct {
	WorkflowFile string                 // Path to workflow YAML/TOML file
	Session      string                 // Tmux session name
	Variables    map[string]interface{} // Runtime variables
	DryRun       bool                   // Validate without executing
	Background   bool                   // Run in background
}

// PipelineRunOutput is the response for --robot-pipeline-run
type PipelineRunOutput struct {
	RobotResponse
	RunID      string           `json:"run_id"`
	WorkflowID string           `json:"workflow_id"`
	Session    string           `json:"session"`
	Status     string           `json:"status"`
	DryRun     bool             `json:"dry_run,omitempty"`
	Progress   PipelineProgress `json:"progress,omitempty"`
	AgentHints *PipelineHints   `json:"_agent_hints,omitempty"`
}

// PipelineStatusOutput is the response for --robot-pipeline=run-id
type PipelineStatusOutput struct {
	RobotResponse
	RunID       string                  `json:"run_id"`
	WorkflowID  string                  `json:"workflow_id"`
	Session     string                  `json:"session"`
	Status      string                  `json:"status"`
	StartedAt   string                  `json:"started_at"`
	FinishedAt  string                  `json:"finished_at,omitempty"`
	DurationMs  int64                   `json:"duration_ms,omitempty"`
	CurrentStep string                  `json:"current_step,omitempty"`
	Progress    PipelineProgress        `json:"progress"`
	Steps       map[string]PipelineStep `json:"steps"`
	Error       string                  `json:"error,omitempty"`
	AgentHints  *PipelineHints          `json:"_agent_hints,omitempty"`
}

// PipelineListOutput is the response for --robot-pipeline-list
type PipelineListOutput struct {
	RobotResponse
	Pipelines  []PipelineSummary `json:"pipelines"`
	AgentHints *PipelineHints    `json:"_agent_hints,omitempty"`
}

// PipelineSummary is a brief summary for listing
type PipelineSummary struct {
	RunID      string           `json:"run_id"`
	WorkflowID string           `json:"workflow_id"`
	Session    string           `json:"session"`
	Status     string           `json:"status"`
	StartedAt  string           `json:"started_at"`
	FinishedAt string           `json:"finished_at,omitempty"`
	Progress   PipelineProgress `json:"progress"`
}

// PipelineCancelOutput is the response for --robot-pipeline-cancel
type PipelineCancelOutput struct {
	RobotResponse
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// PipelineHints provides guidance for AI agents
type PipelineHints struct {
	Summary     string   `json:"summary"`
	NextAction  string   `json:"next_action,omitempty"`
	StatusCmd   string   `json:"status_cmd,omitempty"`
	CancelCmd   string   `json:"cancel_cmd,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// PrintPipelineRun starts a pipeline and returns status
func PrintPipelineRun(opts PipelineRunOptions) int {
	output := PipelineRunOutput{}

	// Validate inputs
	if opts.WorkflowFile == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("workflow file is required"),
			ErrCodeInvalidFlag,
			"Provide a workflow file: ntm --robot-pipeline-run=workflow.yaml",
		)
		outputJSON(output)
		return 1
	}

	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session is required"),
			ErrCodeInvalidFlag,
			"Provide a session: ntm --robot-pipeline-run=workflow.yaml --session=mysession",
		)
		outputJSON(output)
		return 1
	}

	// Load and validate workflow
	workflow, validationResult, err := LoadAndValidate(opts.WorkflowFile)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to load workflow: %w", err),
			ErrCodeInvalidFlag,
			"Check workflow file syntax and path",
		)
		outputJSON(output)
		return 1
	}

	if !validationResult.Valid {
		errMsg := "workflow validation failed"
		if len(validationResult.Errors) > 0 {
			errMsg = validationResult.Errors[0].Message
		}
		output.RobotResponse = NewErrorResponse(
			errors.New(errMsg),
			ErrCodeInvalidFlag,
			"Fix workflow validation errors",
		)
		outputJSON(output)
		return 1
	}

	// Create executor
	execCfg := DefaultExecutorConfig(opts.Session)
	execCfg.DryRun = opts.DryRun
	executor := NewExecutor(execCfg)

	// Create context
	ctx, cancel := context.WithCancel(context.Background())

	// Create progress channel
	progress := make(chan ProgressEvent, 100)

	// Start execution
	if opts.Background {
		// Background execution - register and return immediately
		runID := GenerateRunID()

		// Configure executor with the pre-generated RunID
		execCfg.RunID = runID
		executor = NewExecutor(execCfg) // Recreate with RunID

		exec := &PipelineExecution{
			RunID:      runID,
			WorkflowID: workflow.Name,
			Session:    opts.Session,
			Status:     "running",
			StartedAt:  time.Now(),
			Steps:      make(map[string]PipelineStep),
			Progress: PipelineProgress{
				Total:   len(workflow.Steps),
				Pending: len(workflow.Steps),
			},
			executor: executor,
			cancelFn: cancel,
		}

		registerPipeline(exec)

		go func() {
			defer cancel()
			defer close(progress)

			state, _ := executor.Run(ctx, workflow, opts.Variables, progress)
			updatePipelineFromState(runID, state)
		}()

		output.RobotResponse = NewRobotResponse(true)
		output.RunID = exec.RunID
		output.WorkflowID = workflow.Name
		output.Session = opts.Session
		output.Status = "running"
		output.DryRun = opts.DryRun
		output.Progress = exec.Progress
		output.AgentHints = &PipelineHints{
			Summary:   fmt.Sprintf("Started pipeline '%s' in background", workflow.Name),
			StatusCmd: fmt.Sprintf("ntm --robot-pipeline=%s", exec.RunID),
			CancelCmd: fmt.Sprintf("ntm --robot-pipeline-cancel=%s", exec.RunID),
		}

		outputJSON(output)
		return 0
	}

	// Foreground execution - run to completion
	state, err := executor.Run(ctx, workflow, opts.Variables, progress)
	cancel()
	close(progress)

	if state == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("execution failed: %v", err),
			ErrCodeInternalError,
			"Check workflow and session configuration",
		)
		outputJSON(output)
		return 1
	}

	// Build response
	output.RobotResponse = NewRobotResponse(state.Status == StatusCompleted)
	if state.Status == StatusFailed {
		if len(state.Errors) > 0 {
			output.RobotResponse.Error = state.Errors[0].Message
		} else if err != nil {
			output.RobotResponse.Error = err.Error()
		}
		output.RobotResponse.ErrorCode = ErrCodeInternalError
	}

	output.RunID = state.RunID
	output.WorkflowID = state.WorkflowID
	output.Session = opts.Session
	output.Status = string(state.Status)
	output.DryRun = opts.DryRun
	output.Progress = calculateProgress(state)
	output.AgentHints = &PipelineHints{
		Summary: fmt.Sprintf("Pipeline '%s' %s", workflow.Name, state.Status),
	}

	outputJSON(output)

	if state.Status == StatusCompleted {
		return 0
	}
	return 1
}

// PrintPipelineStatus outputs the status of a running/completed pipeline
func PrintPipelineStatus(runID string) int {
	output := PipelineStatusOutput{}

	if runID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("run_id is required"),
			ErrCodeInvalidFlag,
			"Provide a run ID: ntm --robot-pipeline=run-20241230-123456-abcd",
		)
		outputJSON(output)
		return 1
	}

	exec := getPipeline(runID)
	if exec == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("pipeline not found: %s", runID),
			ErrCodeSessionNotFound,
			"Use 'ntm --robot-pipeline-list' to see available pipelines",
		)
		outputJSON(output)
		return 1
	}

	// Get current state
	var state *ExecutionState
	if exec.executor != nil {
		state = exec.executor.GetState()
	}

	output.RobotResponse = NewRobotResponse(true)
	output.RunID = exec.RunID
	output.WorkflowID = exec.WorkflowID
	output.Session = exec.Session
	output.Status = exec.Status
	output.StartedAt = exec.StartedAt.Format(time.RFC3339)

	if exec.FinishedAt != nil {
		output.FinishedAt = exec.FinishedAt.Format(time.RFC3339)
		output.DurationMs = exec.FinishedAt.Sub(exec.StartedAt).Milliseconds()
	} else {
		output.DurationMs = time.Since(exec.StartedAt).Milliseconds()
	}

	if state != nil {
		output.CurrentStep = state.CurrentStep
		output.Progress = calculateProgress(state)
		output.Steps = convertSteps(state)
		if len(state.Errors) > 0 {
			output.Error = state.Errors[len(state.Errors)-1].Message
		}
	} else {
		output.Progress = exec.Progress
		output.Steps = exec.Steps
		output.Error = exec.Error
	}

	// Generate hints
	output.AgentHints = &PipelineHints{
		Summary: fmt.Sprintf("Pipeline '%s' is %s (%.0f%% complete)",
			exec.WorkflowID, exec.Status, output.Progress.Percent),
	}

	if exec.Status == "running" {
		output.AgentHints.CancelCmd = fmt.Sprintf("ntm --robot-pipeline-cancel=%s", runID)
		output.AgentHints.Suggestions = append(output.AgentHints.Suggestions, "Wait for completion or cancel")
	}

	outputJSON(output)
	return 0
}

// PrintPipelineList outputs all tracked pipelines
func PrintPipelineList() int {
	output := PipelineListOutput{
		RobotResponse: NewRobotResponse(true),
		Pipelines:     []PipelineSummary{},
	}

	pipelineMu.RLock()
	for _, exec := range pipelineRegistry {
		summary := PipelineSummary{
			RunID:      exec.RunID,
			WorkflowID: exec.WorkflowID,
			Session:    exec.Session,
			Status:     exec.Status,
			StartedAt:  exec.StartedAt.Format(time.RFC3339),
			Progress:   exec.Progress,
		}
		if exec.FinishedAt != nil {
			summary.FinishedAt = exec.FinishedAt.Format(time.RFC3339)
		}
		output.Pipelines = append(output.Pipelines, summary)
	}
	pipelineMu.RUnlock()

	// Count by status
	running := 0
	completed := 0
	failed := 0
	for _, p := range output.Pipelines {
		switch p.Status {
		case "running":
			running++
		case "completed":
			completed++
		case "failed", "cancelled":
			failed++
		}
	}

	output.AgentHints = &PipelineHints{
		Summary: fmt.Sprintf("%d pipelines: %d running, %d completed, %d failed",
			len(output.Pipelines), running, completed, failed),
	}

	if running == 0 && len(output.Pipelines) == 0 {
		output.AgentHints.Suggestions = append(output.AgentHints.Suggestions,
			"Start a pipeline with: ntm --robot-pipeline-run=workflow.yaml --session=mysession")
	}

	outputJSON(output)
	return 0
}

// PrintPipelineCancel cancels a running pipeline
func PrintPipelineCancel(runID string) int {
	output := PipelineCancelOutput{}

	if runID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("run_id is required"),
			ErrCodeInvalidFlag,
			"Provide a run ID: ntm --robot-pipeline-cancel=run-20241230-123456-abcd",
		)
		outputJSON(output)
		return 1
	}

	exec := getPipeline(runID)
	if exec == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("pipeline not found: %s", runID),
			ErrCodeSessionNotFound,
			"Use 'ntm --robot-pipeline-list' to see available pipelines",
		)
		outputJSON(output)
		return 1
	}

	// Check if already finished
	if exec.Status != "running" {
		output.RobotResponse = NewRobotResponse(true)
		output.RunID = runID
		output.Status = exec.Status
		output.Message = fmt.Sprintf("Pipeline already %s, nothing to cancel", exec.Status)
		outputJSON(output)
		return 0
	}

	// Cancel the execution
	if exec.cancelFn != nil {
		exec.cancelFn()
	}
	if exec.executor != nil {
		exec.executor.Cancel()
	}

	// Update status
	pipelineMu.Lock()
	exec.Status = "cancelled"
	now := time.Now()
	exec.FinishedAt = &now
	pipelineMu.Unlock()

	output.RobotResponse = NewRobotResponse(true)
	output.RunID = runID
	output.Status = "cancelled"
	output.Message = "Pipeline cancelled successfully"

	outputJSON(output)
	return 0
}

// Helper functions

func calculateProgress(state *ExecutionState) PipelineProgress {
	if state == nil {
		return PipelineProgress{}
	}

	progress := PipelineProgress{}

	// Count steps from state
	for _, result := range state.Steps {
		switch result.Status {
		case StatusCompleted:
			progress.Completed++
		case StatusRunning:
			progress.Running++
		case StatusFailed:
			progress.Failed++
		case StatusSkipped:
			progress.Skipped++
		case StatusPending:
			progress.Pending++
		}
		progress.Total++
	}

	// Calculate percent
	if progress.Total > 0 {
		done := progress.Completed + progress.Failed + progress.Skipped
		progress.Percent = float64(done) / float64(progress.Total) * 100
	}

	return progress
}

func convertSteps(state *ExecutionState) map[string]PipelineStep {
	steps := make(map[string]PipelineStep)

	for id, result := range state.Steps {
		step := PipelineStep{
			ID:       id,
			Status:   string(result.Status),
			Agent:    result.AgentType,
			PaneUsed: result.PaneUsed,
		}

		if !result.StartedAt.IsZero() {
			step.StartedAt = result.StartedAt.Format(time.RFC3339)
		}
		if !result.FinishedAt.IsZero() {
			step.FinishedAt = result.FinishedAt.Format(time.RFC3339)
			step.DurationMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		}
		if result.Output != "" {
			step.OutputLines = countLines(result.Output)
		}
		if result.Error != nil {
			step.Error = result.Error.Message
		}

		steps[id] = step
	}

	return steps
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 1
	for _, c := range s {
		if c == '\n' {
			count++
		}
	}
	return count
}

func registerPipeline(exec *PipelineExecution) {
	pipelineMu.Lock()
	pipelineRegistry[exec.RunID] = exec
	pipelineMu.Unlock()
}

// RegisterPipeline registers a pipeline execution (exported for CLI)
func RegisterPipeline(exec *PipelineExecution) {
	registerPipeline(exec)
}

func getPipeline(runID string) *PipelineExecution {
	pipelineMu.RLock()
	defer pipelineMu.RUnlock()
	return pipelineRegistry[runID]
}

// GetPipelineExecution returns a pipeline by run ID (exported for CLI)
func GetPipelineExecution(runID string) *PipelineExecution {
	return getPipeline(runID)
}

// GetAllPipelines returns all tracked pipelines (exported for CLI)
func GetAllPipelines() []*PipelineExecution {
	pipelineMu.RLock()
	defer pipelineMu.RUnlock()

	result := make([]*PipelineExecution, 0, len(pipelineRegistry))
	for _, exec := range pipelineRegistry {
		result = append(result, exec)
	}
	return result
}

func updatePipelineFromState(runID string, state *ExecutionState) {
	if state == nil {
		return
	}

	pipelineMu.Lock()
	defer pipelineMu.Unlock()

	exec, exists := pipelineRegistry[runID]
	if !exists {
		return
	}

	exec.Status = string(state.Status)
	exec.CurrentStep = state.CurrentStep

	// Calculate progress but preserve original Total if it was set correctly
	newProgress := calculateProgress(state)
	if exec.Progress.Total > newProgress.Total {
		// Keep original total from workflow.Steps, recalculate percent
		newProgress.Total = exec.Progress.Total
		if newProgress.Total > 0 {
			done := newProgress.Completed + newProgress.Failed + newProgress.Skipped
			newProgress.Percent = float64(done) / float64(newProgress.Total) * 100
		}
	}
	exec.Progress = newProgress

	exec.Steps = convertSteps(state)

	if !state.FinishedAt.IsZero() {
		exec.FinishedAt = &state.FinishedAt
	}

	if len(state.Errors) > 0 {
		exec.Error = state.Errors[len(state.Errors)-1].Message
	}
}

// UpdatePipelineFromState updates a registered pipeline from execution state (exported for CLI)
func UpdatePipelineFromState(runID string, state *ExecutionState) {
	updatePipelineFromState(runID, state)
}

// ParsePipelineVars parses JSON variable string into map
func ParsePipelineVars(varsJSON string) (map[string]interface{}, error) {
	if varsJSON == "" {
		return nil, nil
	}

	var vars map[string]interface{}
	if err := json.Unmarshal([]byte(varsJSON), &vars); err != nil {
		return nil, fmt.Errorf("invalid JSON for pipeline-vars: %w", err)
	}

	return vars, nil
}

// ClearPipelineRegistry clears the pipeline registry (for testing)
func ClearPipelineRegistry() {
	pipelineMu.Lock()
	pipelineRegistry = make(map[string]*PipelineExecution)
	pipelineMu.Unlock()
}

// outputJSON writes JSON to stdout
func outputJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
