// Package pipeline provides workflow execution for AI agent orchestration.
// executor.go implements the core execution engine for running workflows.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ExecutorConfig configures the executor behavior
type ExecutorConfig struct {
	Session          string        // Required: tmux session name
	ProjectDir       string        // Optional: project root for .ntm state
	WorkflowFile     string        // Optional: workflow file path for state persistence
	DefaultTimeout   time.Duration // Default step timeout (default: 5m)
	GlobalTimeout    time.Duration // Maximum workflow runtime (default: 30m)
	ProgressInterval time.Duration // Interval for progress updates (default: 1s)
	DryRun           bool          // If true, validate but don't execute
	Verbose          bool          // Enable verbose logging
	RunID            string        // Optional: pre-generated run ID (if empty, one is generated)
}

// MinProgressInterval is the minimum allowed progress interval to prevent ticker panics.
// time.NewTicker requires a positive duration.
const MinProgressInterval = 100 * time.Millisecond

// DefaultExecutorConfig returns sensible defaults
func DefaultExecutorConfig(session string) ExecutorConfig {
	return ExecutorConfig{
		Session:          session,
		DefaultTimeout:   5 * time.Minute,
		GlobalTimeout:    30 * time.Minute,
		ProgressInterval: 1 * time.Second,
		DryRun:           false,
		Verbose:          false,
	}
}

// Executor runs workflows with full orchestration support
type Executor struct {
	config   ExecutorConfig
	detector status.Detector
	router   *robot.Router
	scorer   *robot.AgentScorer
	notifier *Notifier
	loopExec *LoopExecutor

	// Runtime state (reset per execution)
	state    *ExecutionState
	stateMu  sync.RWMutex // Protects state.Steps for concurrent access
	varMu    sync.RWMutex // Protects state.Variables for concurrent access
	graph    *DependencyGraph
	progress chan<- ProgressEvent
	cancelFn context.CancelFunc
}

// NewExecutor creates a new workflow executor
func NewExecutor(config ExecutorConfig) *Executor {
	// Validate ProgressInterval to prevent ticker panics.
	// time.NewTicker requires a positive duration.
	if config.ProgressInterval < MinProgressInterval {
		config.ProgressInterval = DefaultExecutorConfig("").ProgressInterval
	}

	e := &Executor{
		config:   config,
		detector: status.NewDetector(),
		router:   robot.NewRouter(),
		scorer:   robot.NewAgentScorer(robot.DefaultRoutingConfig()),
	}
	e.loopExec = NewLoopExecutor(e)
	return e
}

// SetNotifier sets the notifier for sending notifications on workflow events.
func (e *Executor) SetNotifier(n *Notifier) {
	e.notifier = n
}

// Run executes a workflow with the given initial variables.
// Returns the final execution state and any fatal error.
// Progress events are sent to the provided channel if non-nil.
func (e *Executor) Run(ctx context.Context, workflow *Workflow, vars map[string]interface{}, progress chan<- ProgressEvent) (*ExecutionState, error) {
	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	e.cancelFn = cancel
	defer cancel()

	// Apply global timeout
	timeout := e.config.GlobalTimeout
	if workflow.Settings.Timeout.Duration > 0 {
		timeout = workflow.Settings.Timeout.Duration
	}
	ctx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	// Initialize execution state
	runID := e.config.RunID
	if runID == "" {
		runID = generateRunID()
	}
	e.state = &ExecutionState{
		RunID:        runID,
		WorkflowID:   workflow.Name,
		WorkflowFile: e.config.WorkflowFile,
		Session:      e.config.Session,
		Status:       StatusRunning,
		StartedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Steps:        make(map[string]StepResult),
		Variables:    make(map[string]interface{}),
		Errors:       []ExecutionError{},
	}
	e.progress = progress

	// Initialize variables with defaults and overrides
	for name, def := range workflow.Vars {
		if def.Default != nil {
			e.state.Variables[name] = def.Default
		}
	}
	for name, val := range vars {
		e.state.Variables[name] = val
	}

	e.persistState()

	// Build dependency graph
	e.graph = NewDependencyGraph(workflow)
	if errors := e.graph.Validate(); len(errors) > 0 {
		e.state.Status = StatusFailed
		for _, err := range errors {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "dependency",
				Message:   err.Message,
				Timestamp: time.Now(),
				Fatal:     true,
			})
		}
		e.persistState()
		return e.state, fmt.Errorf("workflow has dependency errors: %v", errors[0])
	}

	// Emit start event
	e.emitProgress("workflow_start", "", fmt.Sprintf("Starting workflow: %s", workflow.Name), 0)

	// Execute steps in dependency order
	err := e.executeWorkflow(ctx, workflow)

	// Finalize state
	e.state.FinishedAt = time.Now()
	e.state.UpdatedAt = time.Now()

	if err != nil {
		if ctx.Err() == context.Canceled {
			e.state.Status = StatusCancelled
			e.sendNotification(ctx, workflow, NotifyCancelled)
		} else if ctx.Err() == context.DeadlineExceeded {
			e.state.Status = StatusFailed
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "timeout",
				Message:   "workflow exceeded global timeout",
				Timestamp: time.Now(),
				Fatal:     true,
			})
			e.sendNotification(ctx, workflow, NotifyFailed)
		} else {
			e.state.Status = StatusFailed
			e.sendNotification(ctx, workflow, NotifyFailed)
		}
		e.emitProgress("workflow_error", "", err.Error(), e.calculateProgress())
	} else {
		e.state.Status = StatusCompleted
		e.sendNotification(ctx, workflow, NotifyCompleted)
		e.emitProgress("workflow_complete", "", "Workflow completed successfully", 1.0)
	}

	e.persistState()

	return e.state, err
}

// Resume continues execution from a previously persisted state.
func (e *Executor) Resume(ctx context.Context, workflow *Workflow, prior *ExecutionState, progress chan<- ProgressEvent) (*ExecutionState, error) {
	if prior == nil {
		return nil, fmt.Errorf("resume state is nil")
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	e.cancelFn = cancel
	defer cancel()

	// Apply global timeout
	timeout := e.config.GlobalTimeout
	if workflow.Settings.Timeout.Duration > 0 {
		timeout = workflow.Settings.Timeout.Duration
	}
	ctx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	e.state = prior
	if e.state.Steps == nil {
		e.state.Steps = make(map[string]StepResult)
	}
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}
	if e.state.RunID == "" {
		if e.config.RunID != "" {
			e.state.RunID = e.config.RunID
		} else {
			e.state.RunID = generateRunID()
		}
	}
	if e.state.WorkflowID == "" {
		e.state.WorkflowID = workflow.Name
	}
	if e.state.WorkflowFile == "" {
		e.state.WorkflowFile = e.config.WorkflowFile
	}
	if e.state.Session == "" {
		e.state.Session = e.config.Session
	}
	if e.state.StartedAt.IsZero() {
		e.state.StartedAt = time.Now()
	}
	e.state.Status = StatusRunning
	e.state.UpdatedAt = time.Now()
	e.state.FinishedAt = time.Time{}
	e.state.CurrentStep = ""

	e.progress = progress

	// Build dependency graph
	e.graph = NewDependencyGraph(workflow)
	if errors := e.graph.Validate(); len(errors) > 0 {
		e.state.Status = StatusFailed
		for _, err := range errors {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "dependency",
				Message:   err.Message,
				Timestamp: time.Now(),
				Fatal:     true,
			})
		}
		e.persistState()
		return e.state, fmt.Errorf("workflow has dependency errors: %v", errors[0])
	}

	e.applyResumeState()
	e.persistState()

	// Emit start event
	e.emitProgress("workflow_start", "", fmt.Sprintf("Resuming workflow: %s", workflow.Name), e.calculateProgress())

	// Execute steps in dependency order
	err := e.executeWorkflow(ctx, workflow)

	// Finalize state
	e.state.FinishedAt = time.Now()
	e.state.UpdatedAt = time.Now()

	if err != nil {
		if ctx.Err() == context.Canceled {
			e.state.Status = StatusCancelled
			e.sendNotification(ctx, workflow, NotifyCancelled)
		} else if ctx.Err() == context.DeadlineExceeded {
			e.state.Status = StatusFailed
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "timeout",
				Message:   "workflow exceeded global timeout",
				Timestamp: time.Now(),
				Fatal:     true,
			})
			e.sendNotification(ctx, workflow, NotifyFailed)
		} else {
			e.state.Status = StatusFailed
			e.sendNotification(ctx, workflow, NotifyFailed)
		}
		e.emitProgress("workflow_error", "", err.Error(), e.calculateProgress())
	} else {
		e.state.Status = StatusCompleted
		e.sendNotification(ctx, workflow, NotifyCompleted)
		e.emitProgress("workflow_complete", "", "Workflow completed successfully", 1.0)
	}

	e.persistState()

	return e.state, err
}

// Cancel cancels the current execution
func (e *Executor) Cancel() {
	if e.cancelFn != nil {
		e.cancelFn()
	}
}

// executeWorkflow runs all steps in dependency order
func (e *Executor) executeWorkflow(ctx context.Context, workflow *Workflow) error {
	totalSteps := e.graph.Size()

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Get ready steps
		ready := e.graph.GetReadySteps()
		if len(ready) == 0 {
			// Check if all steps are executed
			if e.graph.ExecutedCount() >= totalSteps {
				break // All done
			}
			// No ready steps but not all executed - something is wrong
			return fmt.Errorf("no steps ready but workflow incomplete")
		}

		// Execute ready steps (potentially in parallel if they're independent)
		// For now, execute one at a time for simplicity
		// TODO: Optimize with goroutine pool for truly parallel independent steps
		for _, stepID := range ready {
			step, exists := e.graph.GetStep(stepID)
			if !exists {
				continue
			}

			e.state.CurrentStep = stepID
			e.state.UpdatedAt = time.Now()

			// Execute the step
			result := e.executeStep(ctx, step, workflow)

			e.stateMu.Lock()
			e.state.Steps[stepID] = result
			e.stateMu.Unlock()

			// Store output in variables if configured
			if step.OutputVar != "" && result.Status == StatusCompleted {
				e.varMu.Lock()
				e.state.Variables[step.OutputVar] = result.Output
				if result.ParsedData != nil {
					e.state.Variables[step.OutputVar+"_parsed"] = result.ParsedData
				}
				StoreStepOutput(e.state, stepID, result.Output, result.ParsedData)
				e.varMu.Unlock()
			}

			// Mark as executed
			if err := e.graph.MarkExecuted(stepID); err != nil {
				return fmt.Errorf("failed to mark step %s as executed: %w", stepID, err)
			}

			e.state.UpdatedAt = time.Now()
			e.persistState()

			// Mark skipped steps as failed ONLY if skipped due to failed dependencies.
			// This ensures transitive dependents are also skipped (A fails -> B skipped -> C skipped).
			// Steps skipped due to `when` conditions should NOT mark downstream as failed.
			if result.Status == StatusSkipped && e.graph.HasFailedDependency(stepID) {
				_ = e.graph.MarkFailed(stepID)
			}

			// Handle failure based on error action
			if result.Status == StatusFailed {
				// Mark step as failed in dependency graph
				_ = e.graph.MarkFailed(stepID)

				onError := step.OnError
				if onError == "" {
					onError = workflow.Settings.OnError
				}
				if onError == "" {
					onError = ErrorActionFail
				}

				switch onError {
				case ErrorActionFail:
					return fmt.Errorf("step %s failed: %s", stepID, result.Error.Message)
				case ErrorActionContinue:
					// Continue to next step, dependents will be skipped
				case ErrorActionRetry:
					// Retry is handled within executeStep
				}
			}
		}
	}

	return nil
}

// executeStep runs a single step with retry logic
func (e *Executor) executeStep(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusPending,
		StartedAt: time.Now(),
		Attempts:  0,
	}

	// Check for failed dependencies (in CONTINUE mode, skip steps with failed deps)
	if e.graph.HasFailedDependency(step.ID) {
		failedDeps := e.graph.GetFailedDependencies(step.ID)
		result.Status = StatusSkipped
		result.SkipReason = fmt.Sprintf("dependency failed: %v", failedDeps)
		result.FinishedAt = time.Now()
		e.emitProgress("step_skip", step.ID, result.SkipReason, e.calculateProgress())
		return result
	}

	// Check conditional execution
	if step.When != "" {
		skip, err := e.evaluateCondition(step.When)
		if err != nil {
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "condition",
				Message:   fmt.Sprintf("failed to evaluate when condition: %v", err),
				Timestamp: time.Now(),
			}
			result.FinishedAt = time.Now()
			return result
		}
		if skip {
			result.Status = StatusSkipped
			result.SkipReason = fmt.Sprintf("condition '%s' evaluated to false", step.When)
			result.FinishedAt = time.Now()
			e.emitProgress("step_skip", step.ID, result.SkipReason, e.calculateProgress())
			return result
		}
	}

	// Handle parallel steps
	if len(step.Parallel) > 0 {
		return e.executeParallel(ctx, step, workflow)
	}

	// Handle loop steps
	if step.Loop != nil {
		return e.executeLoop(ctx, step, workflow)
	}

	// Calculate retry parameters
	maxAttempts := 1
	if step.OnError == ErrorActionRetry {
		maxAttempts = step.RetryCount + 1
		if maxAttempts < 1 {
			maxAttempts = 1
		}
	}

	retryDelay := step.RetryDelay.Duration
	if retryDelay == 0 {
		retryDelay = 5 * time.Second
	}

	// Execute with retries
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempts = attempt
		result.Status = StatusRunning

		e.emitProgress("step_start", step.ID,
			fmt.Sprintf("Executing step %s (attempt %d/%d)", step.ID, attempt, maxAttempts),
			e.calculateProgress())

		// Execute the step
		stepResult := e.executeStepOnce(ctx, step, workflow)

		if stepResult.Status == StatusCompleted {
			result = stepResult
			result.Attempts = attempt
			result.FinishedAt = time.Now()
			e.emitProgress("step_complete", step.ID,
				fmt.Sprintf("Step %s completed", step.ID), e.calculateProgress())
			return result
		}

		// Step failed
		result.Error = stepResult.Error

		if attempt < maxAttempts {
			// Wait before retry
			delay := e.calculateRetryDelay(retryDelay, attempt, step.RetryBackoff)
			e.emitProgress("step_retry", step.ID,
				fmt.Sprintf("Step %s failed, retrying in %s", step.ID, delay),
				e.calculateProgress())

			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				return result
			case <-time.After(delay):
				// Continue to retry
			}
		}
	}

	result.Status = StatusFailed
	result.FinishedAt = time.Now()
	e.emitProgress("step_error", step.ID,
		fmt.Sprintf("Step %s failed after %d attempts: %s", step.ID, result.Attempts, result.Error.Message),
		e.calculateProgress())

	return result
}

// executeStepOnce executes a step once without retry logic
func (e *Executor) executeStepOnce(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	// Get prompt (from prompt or prompt_file)
	prompt, err := e.resolvePrompt(step)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "prompt",
			Message:   fmt.Sprintf("failed to resolve prompt: %v", err),
			Timestamp: time.Now(),
		}
		return result
	}

	// Substitute variables in prompt
	prompt = e.substituteVariables(prompt)

	// Find target pane
	paneID, agentType, err := e.selectPane(step)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "routing",
			Message:   fmt.Sprintf("failed to select pane: %v", err),
			Timestamp: time.Now(),
		}
		return result
	}
	result.PaneUsed = paneID
	result.AgentType = agentType

	// Dry run mode - don't actually execute
	if e.config.DryRun {
		result.Status = StatusCompleted
		result.Output = "[DRY RUN] Would execute: " + truncatePrompt(prompt, 100)
		result.FinishedAt = time.Now()
		return result
	}

	// Capture state before sending
	beforeOutput, _ := tmux.CapturePaneOutput(paneID, 2000)

	// Send prompt
	if err := tmux.PasteKeys(paneID, prompt, true); err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "send",
			Message:   fmt.Sprintf("failed to send prompt: %v", err),
			Timestamp: time.Now(),
		}
		return result
	}

	// Handle wait condition
	waitCondition := step.Wait
	if waitCondition == "" {
		waitCondition = WaitCompletion
	}

	// Calculate step timeout
	timeout := e.config.DefaultTimeout
	if step.Timeout.Duration > 0 {
		timeout = step.Timeout.Duration
	}

	switch waitCondition {
	case WaitNone:
		// Fire and forget
		result.Status = StatusCompleted
		result.FinishedAt = time.Now()
		return result

	case WaitTime:
		// Just wait for timeout
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		case <-time.After(timeout):
			result.Status = StatusCompleted
			result.FinishedAt = time.Now()
		}

	case WaitCompletion, WaitIdle:
		// Wait for agent to return to idle
		if err := e.waitForIdle(ctx, paneID, timeout); err != nil {
			if ctx.Err() == context.Canceled {
				result.Status = StatusCancelled
			} else {
				result.Status = StatusFailed
				result.Error = &StepError{
					Type:       "timeout",
					Message:    fmt.Sprintf("timeout waiting for completion: %v", err),
					PaneOutput: e.captureErrorContext(paneID, 50),
					AgentState: e.detectAgentState(paneID),
					Timestamp:  time.Now(),
				}
			}
			result.FinishedAt = time.Now()
			return result
		}
	}

	// Capture output
	afterOutput, err := tmux.CapturePaneOutput(paneID, 2000)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "capture",
			Message:   fmt.Sprintf("failed to capture output: %v", err),
			Timestamp: time.Now(),
		}
		return result
	}

	result.Output = util.ExtractNewOutput(beforeOutput, afterOutput)

	// Parse output if configured
	if step.OutputVar != "" && step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		parsed, err := e.parseOutput(result.Output, step.OutputParse)
		if err != nil {
			// Non-fatal - just warn
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "parse",
				Message:   fmt.Sprintf("failed to parse output: %v", err),
				Timestamp: time.Now(),
				Fatal:     false,
			})
		} else {
			result.ParsedData = parsed
		}
	}

	result.Status = StatusCompleted
	result.FinishedAt = time.Now()
	return result
}

// executeParallel runs parallel sub-steps concurrently.
// Supports error modes: fail (wait all), fail_fast (cancel on first error), continue (ignore errors).
// Applies group-level timeout if step.Timeout is set.
// Coordinates agent selection to avoid using the same agent for multiple parallel steps.
func (e *Executor) executeParallel(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	// Determine error handling mode
	onError := step.OnError
	if onError == "" {
		onError = workflow.Settings.OnError
	}
	if onError == "" {
		onError = ErrorActionFail
	}

	// Apply group-level timeout if specified
	if step.Timeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, step.Timeout.Duration)
		defer cancel()
	}

	// Create cancellable context for fail_fast mode
	parallelCtx, cancelParallel := context.WithCancel(ctx)
	defer cancelParallel()

	e.emitProgress("parallel_start", step.ID,
		fmt.Sprintf("Starting parallel group with %d steps (on_error=%s)", len(step.Parallel), onError),
		e.calculateProgress())

	var wg sync.WaitGroup
	results := make([]StepResult, len(step.Parallel))
	var mu sync.Mutex
	var firstError error
	var cancelled bool

	// Track used panes to coordinate agent selection
	usedPanes := make(map[string]bool)
	var panesMu sync.Mutex

	// Semaphore to limit concurrency (max 8 parallel steps)
	sem := make(chan struct{}, 8)

	for i, pStep := range step.Parallel {
		wg.Add(1)
		go func(idx int, ps Step) {
			defer wg.Done()

			// Check if already cancelled (fail_fast mode)
			select {
			case <-parallelCtx.Done():
				mu.Lock()
				results[idx] = StepResult{
					StepID:     ps.ID,
					Status:     StatusCancelled,
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
					SkipReason: "cancelled due to parallel group failure",
				}
				e.stateMu.Lock()
				e.state.Steps[ps.ID] = results[idx]
				e.stateMu.Unlock()
				e.state.UpdatedAt = time.Now()
				e.persistState()
				mu.Unlock()
				return
			case sem <- struct{}{}: // Acquire token
				// Continue execution
			}
			defer func() { <-sem }() // Release token

			// Execute the step with pane coordination
			pResult := e.executeParallelStep(parallelCtx, &ps, workflow, usedPanes, &panesMu)

			mu.Lock()
			results[idx] = pResult
			e.stateMu.Lock()
			e.state.Steps[ps.ID] = pResult
			e.stateMu.Unlock()
			e.state.UpdatedAt = time.Now()
			e.persistState()

			// Handle fail_fast: cancel remaining steps on first error
			if pResult.Status == StatusFailed && onError == ErrorActionFailFast {
				if firstError == nil {
					firstError = fmt.Errorf("step %s failed", ps.ID)
					cancelled = true
					cancelParallel()
				}
			}
			mu.Unlock()
		}(i, pStep)
	}

	wg.Wait()

	// Aggregate results
	completed := 0
	failed := 0
	cancelledCount := 0
	for _, r := range results {
		switch r.Status {
		case StatusCompleted:
			completed++
		case StatusFailed:
			failed++
		case StatusCancelled:
			cancelledCount++
		}
	}

	result.FinishedAt = time.Now()

	// Store parallel group outputs for variable access
	// Results are accessible as ${steps.parallel_group.substep_id.output}
	groupOutputs := make(map[string]interface{})
	for _, r := range results {
		groupOutputs[r.StepID] = map[string]interface{}{
			"output":      r.Output,
			"status":      string(r.Status),
			"parsed_data": r.ParsedData,
		}
	}
	result.ParsedData = groupOutputs

	// Determine final status based on error mode and results
	if failed > 0 || cancelled {
		switch onError {
		case ErrorActionContinue:
			result.Status = StatusCompleted
			result.Output = fmt.Sprintf("Parallel group completed with %d/%d successful", completed, len(results))
		case ErrorActionFailFast:
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "parallel_fail_fast",
				Message:   fmt.Sprintf("%d failed, %d cancelled (fail_fast mode)", failed, cancelledCount),
				Timestamp: time.Now(),
			}
		default: // ErrorActionFail
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "parallel",
				Message:   fmt.Sprintf("%d of %d parallel steps failed", failed, len(results)),
				Timestamp: time.Now(),
			}
		}
	} else if ctx.Err() == context.DeadlineExceeded {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "parallel_timeout",
			Message:   fmt.Sprintf("parallel group timed out after %s", step.Timeout.Duration),
			Timestamp: time.Now(),
		}
	} else {
		result.Status = StatusCompleted
		result.Output = fmt.Sprintf("All %d parallel steps completed", len(results))
	}

	return result
}

// executeLoop executes a loop step and returns the result.
// Delegates to LoopExecutor for for-each, while, and times loop execution.
func (e *Executor) executeLoop(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	// Execute the loop
	loopResult := e.loopExec.ExecuteLoop(ctx, step, workflow)

	// Convert loop result to step result
	result.Status = loopResult.Status
	result.FinishedAt = loopResult.FinishedAt
	result.Error = loopResult.Error

	// Build output summary
	if loopResult.Status == StatusCompleted {
		result.Output = fmt.Sprintf("Loop completed: %d iterations", loopResult.Iterations)
		if loopResult.BreakReason != "" {
			result.Output += fmt.Sprintf(" (exited via %s)", loopResult.BreakReason)
		}
	}

	// Store collected data as parsed data
	if len(loopResult.Collected) > 0 {
		result.ParsedData = loopResult.Collected
	}

	return result
}

// executeParallelStep executes a single step within a parallel group,
// coordinating agent selection to avoid using the same agent for multiple parallel steps.
// Note: Nested parallel steps and loops are not supported.
func (e *Executor) executeParallelStep(ctx context.Context, step *Step, workflow *Workflow, usedPanes map[string]bool, panesMu *sync.Mutex) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	// Check for unsupported nested structures
	if len(step.Parallel) > 0 || step.Loop != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "validation",
			Message:   "nested parallel or loop steps are not supported within parallel groups",
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	// Check context before starting
	if ctx.Err() != nil {
		result.Status = StatusCancelled
		result.FinishedAt = time.Now()
		result.SkipReason = "context cancelled"
		return result
	}

	// Evaluate condition if present
	if step.When != "" {
		skip, err := e.evaluateCondition(step.When)
		if err != nil {
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "condition",
				Message:   fmt.Sprintf("condition evaluation failed: %v", err),
				Timestamp: time.Now(),
			}
			result.FinishedAt = time.Now()
			return result
		}
		if skip {
			result.Status = StatusSkipped
			result.SkipReason = fmt.Sprintf("condition '%s' evaluated to false", step.When)
			result.FinishedAt = time.Now()
			return result
		}
	}

	// Select pane with coordination to avoid reusing agents
	// We select once and reuse for retries to avoid "self-exclusion" issues
	paneID, agentType, err := e.selectPaneExcluding(step, usedPanes, panesMu)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "routing",
			Message:   fmt.Sprintf("failed to select agent: %v", err),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	result.PaneUsed = paneID
	result.AgentType = agentType

	// Mark pane as used for this parallel group
	panesMu.Lock()
	usedPanes[paneID] = true
	panesMu.Unlock()

	// Calculate retry parameters
	maxAttempts := 1
	if step.OnError == ErrorActionRetry {
		maxAttempts = step.RetryCount + 1
		if maxAttempts < 1 {
			maxAttempts = 1
		}
	}

	retryDelay := step.RetryDelay.Duration
	if retryDelay == 0 {
		retryDelay = 5 * time.Second
	}

	// Execute with retries
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempts = attempt
		result.Status = StatusRunning // Reset status for retry
		var afterOutput string        // Declare early to allow goto jumps
		var beforeOutput string       // Declare early to allow goto jumps

		// Initialize wait parameters early to avoid goto jumps over declarations
		waitCondition := step.Wait
		if waitCondition == "" {
			waitCondition = WaitCompletion
		}
		timeout := e.config.DefaultTimeout
		if step.Timeout.Duration > 0 {
			timeout = step.Timeout.Duration
		}

		e.emitProgress("step_start", step.ID,
			fmt.Sprintf("Executing parallel step %s (attempt %d/%d) on %s", step.ID, attempt, maxAttempts, agentType),
			e.calculateProgress())

		// --- Execution Logic (inlined from executeStepOnce) ---

		// Resolve and substitute prompt
		prompt, err := e.resolvePrompt(step)
		if err != nil {
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "prompt",
				Message:   err.Error(),
				Timestamp: time.Now(),
			}
			// Prompt resolution failure is likely permanent, but we follow retry logic
			goto HANDLE_RESULT
		}

		prompt = e.substituteVariables(prompt)

		// Dry run mode
		if e.config.DryRun {
			result.Status = StatusCompleted
			result.Output = "[DRY RUN] Would execute: " + truncatePrompt(prompt, 100)
			result.FinishedAt = time.Now()
			return result
		}

		// Capture state before sending
		beforeOutput, _ = tmux.CapturePaneOutput(paneID, 2000)

		// Send prompt
		if err := tmux.PasteKeys(paneID, prompt, true); err != nil {
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "send",
				Message:   fmt.Sprintf("failed to send prompt: %v", err),
				Timestamp: time.Now(),
			}
			goto HANDLE_RESULT
		}

		switch waitCondition {
		case WaitNone:
			result.Status = StatusCompleted
			result.FinishedAt = time.Now()
			// Success, proceed to capture/parse (though capture might be early)

		case WaitTime:
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.SkipReason = "context cancelled during wait"
				result.FinishedAt = time.Now()
				return result
			case <-time.After(timeout):
				result.Status = StatusCompleted
				result.FinishedAt = time.Now()
			}

		case WaitCompletion, WaitIdle:
			if err := e.waitForIdle(ctx, paneID, timeout); err != nil {
				if ctx.Err() != nil {
					result.Status = StatusCancelled
					result.SkipReason = "context cancelled during execution"
					result.FinishedAt = time.Now()
					return result
				}
				result.Status = StatusFailed
				result.Error = &StepError{
					Type:       "timeout",
					Message:    fmt.Sprintf("timeout waiting for completion: %v", err),
					PaneOutput: e.captureErrorContext(paneID, 50),
					AgentState: e.detectAgentState(paneID),
					Timestamp:  time.Now(),
				}
				goto HANDLE_RESULT
			}
		}

		// Capture output
		{
			var captureErr error
			afterOutput, captureErr = tmux.CapturePaneOutput(paneID, 2000)
			if captureErr != nil {
				result.Status = StatusFailed
				result.Error = &StepError{
					Type:       "capture",
					Message:    fmt.Sprintf("failed to capture output: %v", captureErr),
					PaneOutput: e.captureErrorContext(paneID, 50),
					AgentState: e.detectAgentState(paneID),
					Timestamp:  time.Now(),
				}
				goto HANDLE_RESULT
			}
		}

		result.Output = util.ExtractNewOutput(beforeOutput, afterOutput)
		result.Status = StatusCompleted
		result.FinishedAt = time.Now()

		// Parse output if configured
		if step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
			parsed, err := e.parseOutput(result.Output, step.OutputParse)
			if err != nil {
				e.emitProgress("step_warning", step.ID,
					fmt.Sprintf("output parse warning: %v", err),
					e.calculateProgress())
			} else {
				result.ParsedData = parsed
			}
		}

	HANDLE_RESULT:
		// Check success
		if result.Status == StatusCompleted {
			// Store output
			e.varMu.Lock()
			if step.OutputVar != "" {
				e.state.Variables[step.OutputVar] = result.Output
				if result.ParsedData != nil {
					e.state.Variables[step.OutputVar+"_parsed"] = result.ParsedData
				}
			}
			StoreStepOutput(e.state, step.ID, result.Output, result.ParsedData)
			e.varMu.Unlock()

			return result
		}

		// Handle retry
		if attempt < maxAttempts {
			delay := e.calculateRetryDelay(retryDelay, attempt, step.RetryBackoff)
			e.emitProgress("step_retry", step.ID,
				fmt.Sprintf("Parallel step %s failed, retrying in %s: %v", step.ID, delay, result.Error.Message),
				e.calculateProgress())

			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				return result
			case <-time.After(delay):
				// Continue loop
			}
		}
	}

	// Failed after all attempts
	return result
}

// selectPaneExcluding selects a pane for a step, excluding panes already in use by the parallel group.
// This ensures different agents are used for concurrent parallel steps.
func (e *Executor) selectPaneExcluding(step *Step, usedPanes map[string]bool, panesMu *sync.Mutex) (paneID string, agentType string, err error) {
	// In dry run mode, return dummy pane info
	if e.config.DryRun {
		return "dry-run-pane", "dry-run-agent", nil
	}

	// Explicit pane selection bypasses exclusion
	if step.Pane > 0 {
		panes, err := tmux.GetPanes(e.config.Session)
		if err != nil {
			return "", "", fmt.Errorf("failed to get panes: %w", err)
		}
		for _, p := range panes {
			if p.Index == step.Pane {
				return p.ID, string(p.Type), nil
			}
		}
		return "", "", fmt.Errorf("pane %d not found", step.Pane)
	}

	// Use ScoreAgents to get all scored agents
	agents, err := e.scorer.ScoreAgents(e.config.Session, step.Prompt)
	if err != nil {
		return "", "", fmt.Errorf("failed to score agents: %w", err)
	}

	// Filter by agent type if specified
	if step.Agent != "" {
		targetType := normalizeAgentType(step.Agent)
		filtered := make([]robot.ScoredAgent, 0, len(agents))
		for _, a := range agents {
			if a.AgentType == targetType {
				filtered = append(filtered, a)
			}
		}
		agents = filtered
	}

	// Filter out excluded agents and already-used panes
	panesMu.Lock()
	available := make([]robot.ScoredAgent, 0, len(agents))
	for _, a := range agents {
		if !a.Excluded && !usedPanes[a.PaneID] {
			available = append(available, a)
		}
	}
	panesMu.Unlock()

	// If all agents are used, allow reuse (fall back to original list minus excluded)
	if len(available) == 0 {
		for _, a := range agents {
			if !a.Excluded {
				available = append(available, a)
			}
		}
	}

	if len(available) == 0 {
		return "", "", fmt.Errorf("no suitable agents found")
	}

	// Select routing strategy
	strategy := robot.StrategyLeastLoaded
	if step.Route != "" {
		switch step.Route {
		case RouteLeastLoaded:
			strategy = robot.StrategyLeastLoaded
		case RouteFirstAvailable:
			strategy = robot.StrategyFirstAvailable
		case RouteRoundRobin:
			strategy = robot.StrategyRoundRobin
		}
	}

	// Route to best agent
	routeCtx := robot.RoutingContext{
		Prompt: step.Prompt,
	}
	routeResult := e.router.Route(available, strategy, routeCtx)
	if routeResult.Selected == nil {
		return "", "", fmt.Errorf("routing failed: %s", routeResult.Reason)
	}

	return routeResult.Selected.PaneID, routeResult.Selected.AgentType, nil
}

// selectPane finds the appropriate pane for a step
func (e *Executor) selectPane(step *Step) (paneID string, agentType string, err error) {
	// Explicit pane selection
	if step.Pane > 0 {
		panes, err := tmux.GetPanes(e.config.Session)
		if err != nil {
			return "", "", fmt.Errorf("failed to get panes: %w", err)
		}
		for _, p := range panes {
			if p.Index == step.Pane {
				return p.ID, string(p.Type), nil
			}
		}
		return "", "", fmt.Errorf("pane %d not found", step.Pane)
	}

	// Use ScoreAgents to get all scored agents
	agents, err := e.scorer.ScoreAgents(e.config.Session, step.Prompt)
	if err != nil {
		return "", "", fmt.Errorf("failed to score agents: %w", err)
	}

	// Filter by agent type if specified
	if step.Agent != "" {
		targetType := normalizeAgentType(step.Agent)
		filtered := make([]robot.ScoredAgent, 0, len(agents))
		for _, a := range agents {
			if a.AgentType == targetType {
				filtered = append(filtered, a)
			}
		}
		agents = filtered
	}

	// Filter out excluded agents
	available := make([]robot.ScoredAgent, 0, len(agents))
	for _, a := range agents {
		if !a.Excluded {
			available = append(available, a)
		}
	}

	if len(available) == 0 {
		return "", "", fmt.Errorf("no suitable agents found")
	}

	// Select routing strategy
	strategy := robot.StrategyLeastLoaded
	if step.Route != "" {
		switch step.Route {
		case RouteLeastLoaded:
			strategy = robot.StrategyLeastLoaded
		case RouteFirstAvailable:
			strategy = robot.StrategyFirstAvailable
		case RouteRoundRobin:
			strategy = robot.StrategyRoundRobin
		}
	}

	// Route to best agent
	routeCtx := robot.RoutingContext{
		Prompt: step.Prompt,
	}
	result := e.router.Route(available, strategy, routeCtx)
	if result.Selected == nil {
		return "", "", fmt.Errorf("routing failed: %s", result.Reason)
	}

	return result.Selected.PaneID, result.Selected.AgentType, nil
}

// resolvePrompt gets the prompt content from prompt or prompt_file
func (e *Executor) resolvePrompt(step *Step) (string, error) {
	if step.Prompt != "" {
		return step.Prompt, nil
	}
	if step.PromptFile != "" {
		data, err := os.ReadFile(step.PromptFile)
		if err != nil {
			return "", fmt.Errorf("failed to read prompt file: %w", err)
		}
		return string(data), nil
	}
	return "", fmt.Errorf("step has no prompt or prompt_file")
}

// substituteVariables replaces ${var} references with values.
// Uses the Substitutor for full variable resolution including:
// - Nested field access (${vars.data.nested.field})
// - Default values (${vars.x | "default"})
// - Escaping (\${literal})
// - Loop variables (${loop.item}, ${loop.index})
// Thread-safe: acquires read lock on Variables for concurrent access during parallel execution.
func (e *Executor) substituteVariables(s string) string {
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	sub := NewSubstitutor(e.state, e.config.Session, e.state.WorkflowID)
	result, _ := sub.Substitute(s)
	return result
}

// evaluateCondition evaluates a when condition.
// Returns true if step should be SKIPPED.
// Uses ConditionEvaluator for comprehensive condition support:
// - Boolean truthy check
// - Equality operators (==, !=)
// - Comparison operators (>, <, >=, <=)
// - Contains operator (contains)
// - Logical operators (AND, OR, NOT)
// - Type coercion for numeric comparisons
// Thread-safe: acquires read lock on Variables for concurrent access during parallel execution.
func (e *Executor) evaluateCondition(condition string) (bool, error) {
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	sub := NewSubstitutor(e.state, e.config.Session, e.state.WorkflowID)
	return EvaluateCondition(condition, sub)
}

// parseOutput parses step output according to the parse configuration.
// Uses the OutputParser for full parsing support including:
// - JSON parsing with embedded JSON extraction
// - YAML parsing
// - Regex with named groups
// - Line splitting
func (e *Executor) parseOutput(output string, parse OutputParse) (interface{}, error) {
	parser := NewOutputParser()
	return parser.Parse(output, parse)
}

// waitForIdle waits for an agent to return to idle state
func (e *Executor) waitForIdle(ctx context.Context, paneID string, timeout time.Duration) error {
	ticker := time.NewTicker(e.config.ProgressInterval)
	defer ticker.Stop()

	deadline := time.After(timeout)

	// Initial debounce to let agent start working
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout after %s", timeout)
		case <-ticker.C:
			state, err := e.detector.Detect(paneID)
			if err != nil {
				continue
			}
			if state.State == status.StateIdle {
				return nil
			}
		}
	}
}

// calculateRetryDelay calculates delay for a retry attempt
func (e *Executor) calculateRetryDelay(base time.Duration, attempt int, backoff string) time.Duration {
	switch backoff {
	case "exponential":
		// Exponential backoff: base * 2^(attempt-1)
		multiplier := math.Pow(2, float64(attempt-1))
		return time.Duration(float64(base) * multiplier)
	case "linear":
		// Linear backoff: base * attempt
		return base * time.Duration(attempt)
	default:
		// No backoff
		return base
	}
}

// calculateProgress returns overall workflow progress (0.0 to 1.0)
func (e *Executor) calculateProgress() float64 {
	if e.graph == nil {
		return 0.0
	}
	total := e.graph.Size()
	if total == 0 {
		return 1.0
	}
	e.stateMu.RLock()
	completed := 0
	for _, result := range e.state.Steps {
		if result.Status == StatusCompleted || result.Status == StatusFailed || result.Status == StatusSkipped {
			completed++
		}
	}
	e.stateMu.RUnlock()
	return float64(completed) / float64(total)
}

// emitProgress sends a progress event if channel is available
func (e *Executor) emitProgress(eventType, stepID, message string, progress float64) {
	if e.progress == nil {
		return
	}

	event := ProgressEvent{
		Type:      eventType,
		StepID:    stepID,
		Message:   message,
		Progress:  progress,
		Timestamp: time.Now(),
	}

	select {
	case e.progress <- event:
	default:
		// Don't block if channel is full
	}
}

// sendNotification sends a notification if configured and appropriate for the event.
func (e *Executor) sendNotification(ctx context.Context, workflow *Workflow, event NotificationEvent) {
	if e.notifier == nil {
		return
	}
	if !ShouldNotify(workflow.Settings, event) {
		return
	}

	payload := BuildPayloadFromState(e.state, workflow, event)
	// Use a short timeout context to avoid blocking workflow completion
	notifyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Ignore errors - notifications are best-effort
	_ = e.notifier.Notify(notifyCtx, payload)
}

// truncatePrompt truncates a prompt for display, respecting UTF-8 boundaries.
// Ensures the returned string is at most n bytes.
func truncatePrompt(s string, n int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")

	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return "..."[:n]
	}
	// Find the last rune boundary that allows for "..." suffix within n bytes.
	// targetLen is the max bytes for content (excluding "...")
	targetLen := n - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			// Previous position is the last safe boundary
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts fit within targetLen; use the last one
	return s[:prevI] + "..."
}

func (e *Executor) applyResumeState() {
	if e.state == nil || e.graph == nil {
		return
	}

	rerun := make(map[string]StepResult)
	for stepID, result := range e.state.Steps {
		if shouldRerunStep(result) {
			rerun[stepID] = result
			continue
		}
		if err := e.graph.MarkExecuted(stepID); err != nil {
			delete(e.state.Steps, stepID)
		}
	}

	if len(rerun) == 0 {
		return
	}

	for stepID := range rerun {
		delete(e.state.Steps, stepID)
		e.clearStepVariables(stepID)
	}
}

func shouldRerunStep(result StepResult) bool {
	switch result.Status {
	case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
		return true
	case StatusSkipped:
		if strings.HasPrefix(result.SkipReason, "dependency failed") {
			return true
		}
		if strings.HasPrefix(result.SkipReason, "cancelled") {
			return true
		}
	}
	return result.Status == ""
}

func (e *Executor) clearStepVariables(stepID string) {
	if e.state == nil || e.state.Variables == nil {
		return
	}

	delete(e.state.Variables, "steps."+stepID+".output")
	delete(e.state.Variables, "steps."+stepID+".data")

	if step, ok := e.graph.GetStep(stepID); ok && step.OutputVar != "" {
		delete(e.state.Variables, step.OutputVar)
		delete(e.state.Variables, step.OutputVar+"_parsed")
	}
}

func (e *Executor) snapshotState() *ExecutionState {
	if e.state == nil {
		return nil
	}

	snapshot := *e.state

	e.stateMu.RLock()
	if e.state.Steps != nil {
		snapshot.Steps = make(map[string]StepResult, len(e.state.Steps))
		for key, value := range e.state.Steps {
			snapshot.Steps[key] = value
		}
	}
	e.stateMu.RUnlock()

	e.varMu.RLock()
	if e.state.Variables != nil {
		snapshot.Variables = make(map[string]interface{}, len(e.state.Variables))
		for key, value := range e.state.Variables {
			snapshot.Variables[key] = value
		}
	}
	e.varMu.RUnlock()

	return &snapshot
}

func (e *Executor) persistState() {
	if e.state == nil {
		return
	}

	projectDir := e.config.ProjectDir
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			if e.config.Verbose {
				log.Printf("pipeline: unable to resolve project dir for state persistence: %v", err)
			}
			return
		}
		projectDir = cwd
	}

	snapshot := e.snapshotState()
	if snapshot == nil {
		return
	}

	if err := SaveState(projectDir, snapshot); err != nil && e.config.Verbose {
		log.Printf("pipeline: state persistence failed: %v", err)
	}
}

// GetState returns the current execution state (for monitoring)
func (e *Executor) GetState() *ExecutionState {
	return e.state
}

// captureErrorContext captures recent pane output for error debugging
func (e *Executor) captureErrorContext(paneID string, lines int) string {
	if paneID == "" || e.config.DryRun {
		return ""
	}
	output, err := tmux.CapturePaneOutput(paneID, lines)
	if err != nil {
		return ""
	}
	// Truncate to reasonable size
	if len(output) > 2000 {
		output = output[len(output)-2000:]
	}
	return output
}

// detectAgentState detects the current agent state for error context
func (e *Executor) detectAgentState(paneID string) string {
	if paneID == "" || e.config.DryRun {
		return ""
	}
	state, err := e.detector.Detect(paneID)
	if err != nil {
		return "unknown"
	}
	return string(state.State)
}

// Validate validates a workflow without executing it
func (e *Executor) Validate(workflow *Workflow) ValidationResult {
	return Validate(workflow)
}

// VariableContext provides access to workflow variables
type VariableContext struct {
	Vars     map[string]interface{}
	Steps    map[string]StepResult
	Session  string
	RunID    string
	Workflow string
}

// GetVariable retrieves a variable by reference path
func (vc *VariableContext) GetVariable(ref string) (interface{}, bool) {
	parts := strings.Split(ref, ".")
	if len(parts) == 0 {
		return nil, false
	}

	switch parts[0] {
	case "vars":
		if len(parts) >= 2 {
			val, ok := vc.Vars[parts[1]]
			return val, ok
		}
	case "steps":
		if len(parts) >= 3 {
			if result, ok := vc.Steps[parts[1]]; ok {
				switch parts[2] {
				case "output":
					return result.Output, true
				case "status":
					return string(result.Status), true
				case "pane":
					return result.PaneUsed, true
				}
			}
		}
	case "env":
		if len(parts) >= 2 {
			return os.Getenv(parts[1]), true
		}
	case "session":
		return vc.Session, true
	case "run_id":
		return vc.RunID, true
	case "workflow":
		return vc.Workflow, true
	}

	return nil, false
}

// SetVariable sets a variable value
func (vc *VariableContext) SetVariable(name string, value interface{}) {
	if vc.Vars == nil {
		vc.Vars = make(map[string]interface{})
	}
	vc.Vars[name] = value
}

// EvaluateString evaluates all variable references in a string
func (vc *VariableContext) EvaluateString(s string) string {
	// Uses package-level varPattern from variables.go
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		ref := match[2 : len(match)-1]
		if val, ok := vc.GetVariable(ref); ok {
			return fmt.Sprintf("%v", val)
		}
		return match
	})
}

// ParseBool parses a string as boolean
func ParseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
}

// ParseInt parses a string as integer with default
func ParseInt(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return val
}

// generateRunID creates a unique run ID using timestamp and random bytes
func generateRunID() string {
	return GenerateRunID()
}

// GenerateRunID creates a unique run ID using timestamp and random bytes (exported)
func GenerateRunID() string {
	timestamp := time.Now().Format("20060102-150405")
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback to timestamp-based if crypto/rand fails
		return fmt.Sprintf("run-%s-%x", timestamp, time.Now().UnixNano()%0xffffffff)
	}
	return fmt.Sprintf("run-%s-%s", timestamp, hex.EncodeToString(randBytes))
}
