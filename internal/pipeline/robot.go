// Package pipeline provides workflow execution for AI agent orchestration.
// robot.go implements the --robot-pipeline-* APIs for machine-readable output.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func init() {
	robot.MustRegisterSchemaCommand("pipeline_run", PipelineRunOutput{})
	robot.MustRegisterSchemaCommand("pipeline_status", PipelineStatusOutput{})
	robot.MustRegisterSchemaCommand("pipeline_list", PipelineListOutput{})
	robot.MustRegisterSchemaCommand("pipeline_cancel", PipelineCancelOutput{})
}

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
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	return RobotResponse{
		Success:   false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     message,
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
	Completed      int              `json:"completed"`
	Running        int              `json:"running"`
	Pending        int              `json:"pending"`
	Failed         int              `json:"failed"`
	Skipped        int              `json:"skipped"`
	Total          int              `json:"total"`
	Percent        float64          `json:"percent"`
	SkipKindCounts map[SkipKind]int `json:"skip_kind_counts,omitempty"`
}

// PipelineStep represents step status in pipeline output
type PipelineStep struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	Agent       string   `json:"agent,omitempty"`
	PaneUsed    string   `json:"pane_used,omitempty"`
	StartedAt   string   `json:"started_at,omitempty"`
	FinishedAt  string   `json:"finished_at,omitempty"`
	DurationMs  int64    `json:"duration_ms,omitempty"`
	OutputLines int      `json:"output_lines,omitempty"`
	Error       string   `json:"error,omitempty"`
	SkipKind    SkipKind `json:"skip_kind,omitempty"`
	SkipReason  string   `json:"skip_reason,omitempty"`
}

// PipelineRunOptions configures a pipeline run
type PipelineRunOptions struct {
	WorkflowFile   string                 // Path to workflow YAML/TOML file
	Session        string                 // Tmux session name
	ProjectDir     string                 // Optional: project root for .ntm pipeline state
	Variables      map[string]interface{} // Runtime variables
	DryRun         bool                   // Validate without executing
	Background     bool                   // Run in background
	StartFromStep  string                 // Optional top-level step ID to start from
	FromState      string                 // Optional prior run ID to copy skipped outputs from
	StartFromState *ExecutionState        // Optional preloaded prior state
}

// PipelineRunOutput is the response for --robot-pipeline-run
type PipelineRunOutput struct {
	RobotResponse
	RunID       string              `json:"run_id"`
	WorkflowID  string              `json:"workflow_id"`
	Session     string              `json:"session"`
	Status      string              `json:"status"`
	DryRun      bool                `json:"dry_run,omitempty"`
	Warnings    []ParseError        `json:"warnings,omitempty"`
	Progress    PipelineProgress    `json:"progress,omitempty"`
	SideEffects *SideEffectManifest `json:"side_effect_manifest,omitempty"`
	AgentHints  *PipelineHints      `json:"_agent_hints,omitempty"`
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

// StartBackgroundPipeline registers and starts a background pipeline execution.
// The returned execution is the registry entry that status and cancel operations use.
func StartBackgroundPipeline(workflow *Workflow, vars map[string]interface{}, execCfg ExecutorConfig) *PipelineExecution {
	runID := execCfg.RunID
	if runID == "" {
		runID = GenerateRunID()
	}
	execCfg.RunID = runID

	executor := NewExecutor(execCfg)
	ctx, cancel := context.WithCancel(context.Background())
	progress := make(chan ProgressEvent, 100)

	exec := &PipelineExecution{
		RunID:      runID,
		WorkflowID: workflow.Name,
		Session:    execCfg.Session,
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

		state, _ := executor.Run(ctx, workflow, vars, progress)
		updatePipelineFromState(runID, state)
	}()

	return exec
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

	workflowPath := opts.WorkflowFile
	if abs, err := filepath.Abs(opts.WorkflowFile); err == nil {
		workflowPath = abs
	}

	// Load and validate workflow
	workflow, validationResult, err := LoadAndValidate(workflowPath)
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

	if opts.StartFromStep == "" && (opts.FromState != "" || opts.StartFromState != nil) {
		output.RobotResponse = NewErrorResponse(
			errors.New("--from-state requires --start-from"),
			ErrCodeInvalidFlag,
			"Provide a top-level step with --start-from when reusing prior state.",
		)
		outputJSON(output)
		return 1
	}

	varValidation, varErr := ValidateWorkflowVariables(workflow, opts.Variables)
	if varErr != nil {
		output.RobotResponse = NewErrorResponse(
			errors.New(varErr.Message),
			ErrCodeInvalidFlag,
			varErr.Hint,
		)
		outputJSON(output)
		return 1
	}
	if varValidation != nil {
		opts.Variables = varValidation.Variables
		output.Warnings = varValidation.Warnings
	}
	if opts.DryRun {
		manifest := BuildSideEffectManifest(workflow)
		output.SideEffects = &manifest
	}

	// Create executor
	execCfg := DefaultExecutorConfig(opts.Session)
	execCfg.DryRun = opts.DryRun
	if strings.TrimSpace(opts.ProjectDir) != "" {
		execCfg.ProjectDir = opts.ProjectDir
	} else if projectDir, err := os.Getwd(); err == nil {
		execCfg.ProjectDir = projectDir
	}
	execCfg.WorkflowFile = workflowPath
	execCfg.StartFromStep = opts.StartFromStep
	execCfg.StartFromState = opts.StartFromState
	if opts.FromState != "" {
		prior, err := LoadState(execCfg.ProjectDir, opts.FromState)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("--from-state: load run %q: %w", opts.FromState, err),
				ErrCodeInvalidFlag,
				"Check that the prior run ID exists under the pipeline state directory.",
			)
			outputJSON(output)
			return 1
		}
		execCfg.StartFromState = prior
	}
	executor := NewExecutor(execCfg)

	// Create context
	ctx, cancel := context.WithCancel(context.Background())

	// Create progress channel
	progress := make(chan ProgressEvent, 100)

	// Start execution
	if opts.Background {
		cancel()
		exec := StartBackgroundPipeline(workflow, opts.Variables, execCfg)

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
			output.Error = state.Errors[0].Message
		} else if err != nil {
			output.Error = err.Error()
		}
		output.ErrorCode = ErrCodeInternalError
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
		errMsg := "run_id is required"
		output.RobotResponse = NewErrorResponse(
			errors.New(errMsg),
			ErrCodeInvalidFlag,
			"Provide a run ID: ntm --robot-pipeline=run-20241230-123456-abcd",
		)
		// Set outer Error field to avoid shadowing from embedded RobotResponse
		output.Error = errMsg
		outputJSON(output)
		return 1
	}

	exec := GetPipelineSnapshot(runID)
	if exec == nil {
		errMsg := fmt.Sprintf("pipeline not found: %s", runID)
		output.RobotResponse = NewErrorResponse(
			errors.New(errMsg),
			ErrCodeSessionNotFound,
			"Use 'ntm --robot-pipeline-list' to see tracked pipelines. Completed runs are read from .ntm/pipelines/<run-id>.json (searched upward from the current directory), so run this from the project the pipeline executed in.",
		)
		// Set outer Error field to avoid shadowing from embedded RobotResponse
		output.Error = errMsg
		outputJSON(output)
		return 1
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

	output.CurrentStep = exec.CurrentStep
	output.Progress = exec.Progress
	output.Steps = exec.Steps
	output.Error = exec.Error

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

	for _, exec := range GetAllPipelineSnapshots() {
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
			if result.SkipKind != SkipKindNone {
				if progress.SkipKindCounts == nil {
					progress.SkipKindCounts = make(map[SkipKind]int)
				}
				progress.SkipKindCounts[result.SkipKind]++
			}
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
			ID:         id,
			Status:     string(result.Status),
			Agent:      result.AgentType,
			PaneUsed:   result.PaneUsed,
			SkipKind:   result.SkipKind,
			SkipReason: result.SkipReason,
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

// NewTrackedExecution builds a registry entry for a run whose goroutine and
// progress channel the CALLER owns, wiring the handles CancelPipeline needs.
//
// StartBackgroundPipeline owns its own progress channel, which `ntm serve`
// cannot use because it forwards progress events to WebSocket subscribers.
// Serve therefore drove pipeline.NewExecutor inline and nothing ever reached
// the registry: background runs came back 404 from GET and cancel, and their
// context had no cancel handle stored anywhere, so they kept driving tmux to
// completion with no way to stop them (bd-fresh-eyes-audit .26).
//
// The caller must call UpdatePipelineFromState when the run finishes.
func NewTrackedExecution(runID, workflowID, session string, totalSteps int, executor *Executor, cancel context.CancelFunc) *PipelineExecution {
	return &PipelineExecution{
		RunID:      runID,
		WorkflowID: workflowID,
		Session:    session,
		Status:     "running",
		StartedAt:  time.Now(),
		Steps:      make(map[string]PipelineStep),
		Progress: PipelineProgress{
			Total:   totalSteps,
			Pending: totalSteps,
		},
		executor: executor,
		cancelFn: cancel,
	}
}

func getPipeline(runID string) *PipelineExecution {
	pipelineMu.RLock()
	defer pipelineMu.RUnlock()
	return pipelineRegistry[runID]
}

func snapshotPipeline(exec *PipelineExecution) *PipelineExecution {
	if exec == nil {
		return nil
	}

	snapshot := *exec
	if exec.Steps != nil {
		snapshot.Steps = make(map[string]PipelineStep, len(exec.Steps))
		for key, value := range exec.Steps {
			snapshot.Steps[key] = value
		}
	}

	if exec.executor == nil {
		return &snapshot
	}

	state := exec.executor.GetState()
	if state == nil {
		return &snapshot
	}

	registryTerminal := snapshot.Status != "" && snapshot.Status != "running"
	if !registryTerminal {
		snapshot.Status = string(state.Status)
	}
	snapshot.CurrentStep = state.CurrentStep
	snapshot.Progress = progressFromState(state, exec.Progress.Total)
	snapshot.Steps = convertSteps(state)

	if !registryTerminal && !state.FinishedAt.IsZero() {
		finishedAt := state.FinishedAt
		snapshot.FinishedAt = &finishedAt
	}

	if len(state.Errors) > 0 && (!registryTerminal || snapshot.Error == "") {
		snapshot.Error = state.Errors[len(state.Errors)-1].Message
	}

	return &snapshot
}

// GetPipelineExecution returns a pipeline by run ID (exported for CLI)
func GetPipelineExecution(runID string) *PipelineExecution {
	return getPipeline(runID)
}

// GetPipelineSnapshot returns a read-only snapshot of a pipeline, including live executor state when available.
// When the in-memory registry has no entry (e.g. the run finished in a
// different ntm process), it falls back to the persisted state file under
// .ntm/pipelines/<run-id>.json, searching upward from the current directory
// (ntm#216).
func GetPipelineSnapshot(runID string) *PipelineExecution {
	pipelineMu.RLock()
	snapshot := snapshotPipeline(pipelineRegistry[runID])
	pipelineMu.RUnlock()
	if snapshot != nil {
		return snapshot
	}
	return loadPersistedSnapshot(runID)
}

// snapshotFromPersistedState converts a persisted ExecutionState (the
// .ntm/pipelines/<run-id>.json format written by Executor.persistState) into
// a read-only PipelineExecution snapshot so status/list surfaces can report
// runs that are no longer in this process's live registry (ntm#216). The
// reported status is the last persisted one — a run whose process died
// mid-flight will still read "running" until it is resumed or cleaned up.
func snapshotFromPersistedState(state *ExecutionState) *PipelineExecution {
	if state == nil {
		return nil
	}
	exec := &PipelineExecution{
		RunID:       state.RunID,
		WorkflowID:  state.WorkflowID,
		Session:     state.Session,
		Status:      string(state.Status),
		StartedAt:   state.StartedAt,
		CurrentStep: state.CurrentStep,
		Progress:    calculateProgress(state),
		Steps:       convertSteps(state),
	}
	if !state.FinishedAt.IsZero() {
		finishedAt := state.FinishedAt
		exec.FinishedAt = &finishedAt
	}
	if len(state.Errors) > 0 {
		exec.Error = state.Errors[len(state.Errors)-1].Message
	}
	return exec
}

// persistedStateRootFn resolves the project directory whose .ntm/pipelines
// directory holds persisted run state. Overridable in tests so registry-only
// assertions aren't polluted by state files under the test process's cwd.
var persistedStateRootFn = findPersistedStateRoot

// findPersistedStateRoot walks upward from the current working directory and
// returns the first directory containing a .ntm/pipelines state directory.
// This mirrors how the executor persists state (config.ProjectDir, defaulting
// to the run's cwd) so status lookups issued from the project root or any
// subdirectory find the same files. Returns "" when none exists.
func findPersistedStateRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if info, err := os.Stat(pipelineStateDir(dir)); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// loadPersistedSnapshot loads a single persisted run from disk, returning nil
// when no state file exists for the run ID.
func loadPersistedSnapshot(runID string) *PipelineExecution {
	if validateRunID(runID) != nil {
		return nil
	}
	root := persistedStateRootFn()
	if root == "" {
		return nil
	}
	state, err := LoadState(root, runID)
	if err != nil {
		return nil
	}
	return snapshotFromPersistedState(state)
}

// loadAllPersistedSnapshots loads every readable persisted run from the
// nearest .ntm/pipelines directory. Unreadable or malformed files are
// skipped — listing is best-effort.
func loadAllPersistedSnapshots() []*PipelineExecution {
	root := persistedStateRootFn()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(pipelineStateDir(root))
	if err != nil {
		return nil
	}
	var snapshots []*PipelineExecution
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		runID := strings.TrimSuffix(entry.Name(), ".json")
		if validateRunID(runID) != nil {
			continue
		}
		state, err := LoadState(root, runID)
		if err != nil {
			continue
		}
		if snapshot := snapshotFromPersistedState(state); snapshot != nil {
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots
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

// GetAllPipelineSnapshots returns stable, read-only pipeline snapshots sorted by start time descending.
// The result merges this process's live registry with persisted runs from
// .ntm/pipelines so completed runs from other processes remain listable
// (ntm#216). Registry entries win on run-ID conflicts (they carry live
// executor state).
func GetAllPipelineSnapshots() []*PipelineExecution {
	pipelineMu.RLock()
	snapshots := make([]*PipelineExecution, 0, len(pipelineRegistry))
	seen := make(map[string]bool, len(pipelineRegistry))
	for _, exec := range pipelineRegistry {
		snapshot := snapshotPipeline(exec)
		snapshots = append(snapshots, snapshot)
		seen[snapshot.RunID] = true
	}
	pipelineMu.RUnlock()

	for _, snapshot := range loadAllPersistedSnapshots() {
		if !seen[snapshot.RunID] {
			snapshots = append(snapshots, snapshot)
		}
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
	})

	return snapshots
}

// CancelPipeline cancels a running pipeline by run ID (exported for REST API).
//
// It reports whether a live execution handle was actually found and cancelled.
// Cancellation needs the in-process cancelFn/executor, which only exist in the
// registry of the process that started the run — but GetPipelineSnapshot falls
// back to the on-disk state file, so a caller can see a "running" pipeline that
// this process cannot stop (started by the `ntm pipeline` CLI, or by a server
// that has since restarted). Returning void made that case indistinguishable
// from a real cancellation, and the REST handler answered 200 "cancelled" for a
// run that kept going.
func CancelPipeline(runID string) bool {
	exec := getPipeline(runID)
	if exec == nil {
		return false
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

	return true
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

	exec.Progress = progressFromState(state, exec.Progress.Total)

	exec.Steps = convertSteps(state)

	if !state.FinishedAt.IsZero() {
		exec.FinishedAt = &state.FinishedAt
	}

	if len(state.Errors) > 0 {
		exec.Error = state.Errors[len(state.Errors)-1].Message
	}
}

func progressFromState(state *ExecutionState, preserveTotal int) PipelineProgress {
	progress := calculateProgress(state)
	if preserveTotal > progress.Total {
		progress.Total = preserveTotal
		if progress.Total > 0 {
			done := progress.Completed + progress.Failed + progress.Skipped
			progress.Percent = float64(done) / float64(progress.Total) * 100
		}
	}
	return progress
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
	_ = enc.Encode(v) // Error ignored: stdout output failure is unrecoverable
}
