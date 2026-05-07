// Package pipeline provides workflow execution for AI agent orchestration.
// executor.go implements the core execution engine for running workflows.
package pipeline

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// dispatchLogSeq is a process-local sequence counter that breaks ties when
// two dispatch logs land in the same second + step ID. Combined with the
// nanosecond timestamp it makes filenames sortable and unique even under
// retry/recovery loops or rapid foreach fan-out (bd-45fs8).
var dispatchLogSeq uint64

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
	BeadQueryRunBr   func(ctx context.Context, args []string) ([]byte, error)

	// StartFromStep, when non-empty, instructs Run() to mark every transitive
	// dependency of this step as StatusSkipped and begin actual execution at
	// this step. The step ID must refer to a top-level step (not nested inside
	// a parallel, loop, or foreach body) — otherwise Run() returns an error.
	StartFromStep string

	// StartFromState, when non-nil, supplies prior step results that are copied
	// into the synthetic Skipped entries created by StartFromStep so that
	// ${steps.X.output} references in later steps resolve to the prior values.
	StartFromState *ExecutionState

	// ResumeOptions controls how Resume interprets prior persisted state.
	ResumeOptions ResumeOptions
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
	defaults map[string]interface{}
	limits   LimitsConfig
	tmux     TmuxClient
	paneMeta *PaneMetadataLoader
	paneMu   sync.Mutex
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
		limits:   LimitsConfig{}.EffectiveLimits(),
		tmux:     realTmuxClient{},
	}
	e.loopExec = NewLoopExecutor(e)
	return e
}

// SetTmuxClient swaps the tmux transport used by the executor.
// Tests use this to install a deterministic in-memory tmux implementation.
func (e *Executor) SetTmuxClient(client TmuxClient) {
	if client == nil {
		client = realTmuxClient{}
	}
	e.tmux = client
	e.resetPaneMetadataLoader()
}

func (e *Executor) tmuxClient() TmuxClient {
	if e.tmux == nil {
		e.tmux = realTmuxClient{}
	}
	return e.tmux
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

	e.stateMu.Lock()
	e.state = &ExecutionState{
		RunID:         runID,
		WorkflowID:    workflow.Name,
		WorkflowFile:  e.config.WorkflowFile,
		Session:       e.config.Session,
		Status:        StatusRunning,
		StartedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Steps:         make(map[string]StepResult),
		Variables:     make(map[string]interface{}),
		Errors:        []ExecutionError{},
		ForeachState:  make(map[string]ForeachIterationState),
		ParallelState: make(map[string]ParallelGroupState),
		InFlightSteps: make(map[string]InFlightStepState),
	}
	e.progress = progress
	e.stateMu.Unlock()

	// Initialize variables with runtime overrides taking precedence over
	// workflow defaults, and validate declared VarType values before execution.
	preparedVars, err := PrepareWorkflowVariables(workflow, vars)
	if err != nil {
		e.stateMu.Lock()
		e.state.Status = StatusFailed
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type:      "variables",
			Message:   err.Error(),
			Timestamp: time.Now(),
			Fatal:     true,
		})
		e.stateMu.Unlock()
		e.persistState()
		return e.state, err
	}
	e.varMu.Lock()
	e.state.Variables = preparedVars
	e.varMu.Unlock()

	e.defaults = workflow.Defaults
	e.limits = workflow.Settings.Limits.EffectiveLimits()
	e.resetPaneMetadataLoader()

	e.persistState()

	// Build dependency graph
	e.graph = NewDependencyGraph(workflow)
	if errors := e.graph.Validate(); len(errors) > 0 {
		e.stateMu.Lock()
		e.state.Status = StatusFailed
		for _, err := range errors {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "dependency",
				Message:   err.Message,
				Timestamp: time.Now(),
				Fatal:     true,
			})
		}
		e.stateMu.Unlock()
		e.persistState()
		return e.state, fmt.Errorf("workflow has dependency errors: %v", errors[0])
	}

	if err := e.applyStartFrom(workflow); err != nil {
		e.stateMu.Lock()
		e.state.Status = StatusFailed
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type:      "start_from",
			Message:   err.Error(),
			Timestamp: time.Now(),
			Fatal:     true,
		})
		e.stateMu.Unlock()
		e.persistState()
		return e.state, err
	}

	// Emit start event
	e.emitProgress("workflow_start", "", workflowProgressMessage("Starting workflow", workflow), 0)

	// Execute steps in dependency order
	err = e.executeWorkflow(ctx, workflow)

	// Finalize state
	if err != nil {
		if ctx.Err() != nil {
			e.finalizeCancelledWorkflow(ctx, workflow)
		} else {
			e.stateMu.Lock()
			e.state.FinishedAt = time.Now()
			e.state.UpdatedAt = time.Now()
			e.state.Status = StatusFailed
			e.sendNotification(ctx, workflow, NotifyFailed)
			e.stateMu.Unlock()
		}
		e.emitProgress("workflow_error", "", err.Error(), e.calculateProgress())
	} else {
		e.stateMu.Lock()
		e.state.FinishedAt = time.Now()
		e.state.UpdatedAt = time.Now()
		e.state.Status = StatusCompleted
		e.sendNotification(ctx, workflow, NotifyCompleted)
		e.stateMu.Unlock()
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

	e.stateMu.Lock()
	e.state = prior
	if e.state.Steps == nil {
		e.state.Steps = make(map[string]StepResult)
	}
	e.stateMu.Unlock()

	e.varMu.Lock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}
	e.varMu.Unlock()

	e.stateMu.Lock()
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
	e.state.CancelledAt = nil
	e.state.CurrentStep = ""
	e.stateMu.Unlock()

	e.progress = progress
	e.defaults = workflow.Defaults
	e.limits = workflow.Settings.Limits.EffectiveLimits()

	// Build dependency graph
	e.graph = NewDependencyGraph(workflow)
	if errors := e.graph.Validate(); len(errors) > 0 {
		e.stateMu.Lock()
		e.state.Status = StatusFailed
		for _, err := range errors {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type:      "dependency",
				Message:   err.Message,
				Timestamp: time.Now(),
				Fatal:     true,
			})
		}
		e.stateMu.Unlock()
		e.persistState()
		return e.state, fmt.Errorf("workflow has dependency errors: %v", errors[0])
	}

	if err := e.applyResumeOptions(workflow, e.config.ResumeOptions); err != nil {
		e.stateMu.Lock()
		e.state.Status = StatusFailed
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type:      "resume",
			Message:   err.Error(),
			Timestamp: time.Now(),
			Fatal:     true,
		})
		e.stateMu.Unlock()
		e.persistState()
		return e.state, err
	}

	e.applyResumeState()
	e.persistState()

	// Emit start event
	e.emitProgress("workflow_start", "", workflowProgressMessage("Resuming workflow", workflow), e.calculateProgress())

	// Execute steps in dependency order
	err := e.executeWorkflow(ctx, workflow)

	// Finalize state
	if err != nil {
		if ctx.Err() != nil {
			e.finalizeCancelledWorkflow(ctx, workflow)
		} else {
			e.stateMu.Lock()
			e.state.FinishedAt = time.Now()
			e.state.UpdatedAt = time.Now()
			e.state.Status = StatusFailed
			e.sendNotification(ctx, workflow, NotifyFailed)
			e.stateMu.Unlock()
		}
		e.emitProgress("workflow_error", "", err.Error(), e.calculateProgress())
	} else {
		e.stateMu.Lock()
		e.state.FinishedAt = time.Now()
		e.state.UpdatedAt = time.Now()
		e.state.Status = StatusCompleted
		e.sendNotification(ctx, workflow, NotifyCompleted)
		e.stateMu.Unlock()
		e.emitProgress("workflow_complete", "", "Workflow completed successfully", 1.0)
	}

	e.persistState()

	return e.state, err
}

func (e *Executor) finalizeCancelledWorkflow(ctx context.Context, workflow *Workflow) {
	cancelledAt := time.Now()

	e.stateMu.Lock()
	e.state.Status = StatusCancelled
	if e.state.CancelledAt == nil {
		e.state.CancelledAt = &cancelledAt
	}
	e.state.UpdatedAt = cancelledAt
	if ctx.Err() == context.DeadlineExceeded {
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type:      "timeout",
			Message:   "workflow exceeded global timeout",
			Timestamp: cancelledAt,
			Fatal:     true,
		})
	}
	e.stateMu.Unlock()
	e.persistState()

	e.runOnCancelSteps(workflow)

	finishedAt := time.Now()
	e.stateMu.Lock()
	e.state.Status = StatusCancelled
	if e.state.CancelledAt == nil {
		e.state.CancelledAt = &cancelledAt
	}
	e.state.FinishedAt = finishedAt
	e.state.UpdatedAt = finishedAt
	e.state.CurrentStep = ""
	e.stateMu.Unlock()
	e.sendNotification(ctx, workflow, NotifyCancelled)
}

func (e *Executor) runOnCancelSteps(workflow *Workflow) {
	if workflow == nil || len(workflow.Settings.OnCancel) == 0 {
		return
	}

	cleanupWorkflow := *workflow
	for i := range workflow.Settings.OnCancel {
		step := workflow.Settings.OnCancel[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("on_cancel_%d", i+1)
		}

		e.stateMu.Lock()
		e.state.CurrentStep = step.ID
		e.state.UpdatedAt = time.Now()
		e.stateMu.Unlock()

		result := e.executeStep(context.Background(), &step, &cleanupWorkflow)
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}

		e.stateMu.Lock()
		e.state.Steps[step.ID] = result
		e.state.UpdatedAt = time.Now()
		if result.Status == StatusFailed && result.Error != nil {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "on_cancel",
				Message:   result.Error.Message,
				Timestamp: time.Now(),
				Fatal:     false,
			})
		}
		e.stateMu.Unlock()

		if step.OutputVar != "" && result.Status == StatusCompleted {
			e.varMu.Lock()
			e.state.Variables[step.OutputVar] = result.Output
			if result.ParsedData != nil {
				e.state.Variables[step.OutputVar+"_parsed"] = result.ParsedData
			}
			StoreStepOutput(e.state, step.ID, result.Output, result.ParsedData)
			e.varMu.Unlock()
		}

		e.persistState()
	}
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
		sort.SliceStable(ready, func(i, j int) bool {
			_, _, insideI := findStepContainer(workflow, ready[i])
			_, _, insideJ := findStepContainer(workflow, ready[j])
			return !insideI && insideJ
		})
		for _, stepID := range ready {
			step, exists := e.graph.GetStep(stepID)
			if !exists {
				continue
			}
			if _, _, inside := findStepContainer(workflow, stepID); inside {
				if err := e.graph.MarkExecuted(stepID); err != nil {
					return fmt.Errorf("failed to mark nested step %s as executed: %w", stepID, err)
				}
				continue
			}

			e.stateMu.Lock()
			e.state.CurrentStep = stepID
			e.state.UpdatedAt = time.Now()
			e.stateMu.Unlock()

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

				onError := resolveErrorAction(step.OnError, workflow.Settings.OnError)

				switch onError {
				case ErrorActionFail, ErrorActionFailFast:
					return fmt.Errorf("step %s failed: %s", stepID, result.Error.Message)
				case ErrorActionContinue:
					// Continue to next step, dependents will be skipped
				case ErrorActionRetry:
					// Retry is handled within executeStep
				}
			}

			if result.Status == StatusCancelled {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return context.Canceled
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
	e.markStepInFlight(step.ID, stepKind(step), -1)
	e.persistState()
	defer e.clearStepInFlight(step.ID)

	// Check for failed dependencies (in CONTINUE mode, skip steps with failed deps)
	if e.graph.HasFailedDependency(step.ID) {
		failedDeps := e.graph.GetFailedDependencies(step.ID)
		result.Status = StatusSkipped
		result.SkipReason = fmt.Sprintf("dependency failed: %v", failedDeps)
		result.SkipKind = SkipKindFailedDependency
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
			result.SkipKind = SkipKindWhenCondition
			result.FinishedAt = time.Now()
			e.emitProgress("step_skip", step.ID, result.SkipReason, e.calculateProgress())
			return result
		}
	}

	// Handle parallel steps
	if len(step.Parallel.Steps) > 0 {
		return e.executeParallel(ctx, step, workflow)
	}

	// Handle loop steps
	if step.Loop != nil {
		return e.executeLoop(ctx, step, workflow)
	}

	// Calculate retry parameters
	maxAttempts := 1
	if resolveErrorAction(step.OnError, workflow.Settings.OnError) == ErrorActionRetry {
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
			stepProgressMessage("Executing step", step, fmt.Sprintf("attempt %d/%d", attempt, maxAttempts)),
			e.calculateProgress())

		// Execute the step
		stepResult := e.executeStepOnce(ctx, step, workflow)

		if stepResult.Status == StatusCompleted {
			result = stepResult
			result.Attempts = attempt
			result.FinishedAt = time.Now()
			e.emitProgress("step_complete", step.ID,
				stepProgressMessage("Step completed", step, ""), e.calculateProgress())
			return result
		}

		if stepResult.Status == StatusCancelled {
			result = stepResult
			result.Attempts = attempt
			if result.FinishedAt.IsZero() {
				result.FinishedAt = time.Now()
			}
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
	result = e.executeOnFailureAction(step, result)
	if result.Status == StatusSkipped {
		e.emitProgress("step_skip", step.ID, result.SkipReason, e.calculateProgress())
		return result
	}
	result = e.executeOnFailureRecovery(ctx, step, workflow, result)
	if result.Status == StatusCompleted {
		e.emitProgress("step_complete", step.ID,
			stepProgressMessage("Step completed after on_failure recovery", step, ""), e.calculateProgress())
		return result
	}
	errorMessage := "unknown error"
	if result.Error != nil {
		errorMessage = result.Error.Message
	}
	e.emitProgress("step_error", step.ID,
		fmt.Sprintf("Step %s failed after %d attempts: %s", step.ID, result.Attempts, errorMessage),
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

	// Dispatch command steps to executeCommand.
	if step.Command != "" {
		return e.executeCommand(ctx, step, workflow)
	}

	// Dispatch template steps to executeTemplate.
	if step.Template != "" {
		return e.executeTemplate(ctx, step, workflow)
	}

	// Dispatch branch steps to resolveBranch + lookupBranch.
	if step.Branch != "" {
		return e.executeBranch(ctx, step, workflow)
	}

	// Dispatch structured br query steps.
	if step.BeadQuery != nil {
		return e.executeBeadQuery(ctx, step, workflow)
	}

	if step.Foreach != nil || step.ForeachPane != nil {
		return e.executeForeach(ctx, step, workflow)
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
		result.Output = dryRunOutput(step, "Would execute: "+truncatePrompt(prompt, 100))
		result.FinishedAt = time.Now()
		return result
	}

	// Capture state before sending
	beforeOutput, _ := e.tmuxClient().CapturePaneOutput(paneID, 2000)

	// Send prompt
	if err := e.tmuxClient().PasteKeys(paneID, prompt, true); err != nil {
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
			if ctx.Err() != nil {
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
	afterOutput, err := e.tmuxClient().CapturePaneOutput(paneID, 2000)
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
			e.stateMu.Lock()
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "parse",
				Message:   fmt.Sprintf("failed to parse output: %v", err),
				Timestamp: time.Now(),
				Fatal:     false,
			})
			e.stateMu.Unlock()
		} else {
			result.ParsedData = parsed
		}
	}

	result.Status = StatusCompleted
	result.FinishedAt = time.Now()
	return result
}

func stepRuntimeError(step *Step, kind, typ, reason, hint, details string) *StepError {
	detailParts := []string{
		"kind=" + kind,
		"step_id=" + step.ID,
		"reason=" + reason,
	}
	if hint != "" {
		detailParts = append(detailParts, "hint="+hint)
	}
	if details != "" {
		detailParts = append(detailParts, "details="+details)
	}

	return &StepError{
		Type:      typ,
		Message:   fmt.Sprintf("%s step %q failed: %s", kind, step.ID, reason),
		Details:   strings.Join(detailParts, " "),
		Timestamp: time.Now(),
	}
}

// executeCommand runs a shell command step via /bin/sh -c.
func (e *Executor) executeCommand(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		AgentType: "command",
	}

	expandedCmd := e.substituteVariables(step.Command)

	argEnv, err := argsToEnv(step.Args)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "command", "validation",
			err.Error(),
			"use POSIX-compatible arg keys when passing command args as environment variables",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	if e.config.DryRun {
		result.Status = StatusCompleted
		result.Output = dryRunOutput(step, "Would execute command: "+truncatePrompt(expandedCmd, 200))
		result.FinishedAt = time.Now()
		return result
	}

	slog.Info("command step starting",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "command",
	)

	timeout := e.config.DefaultTimeout
	if step.Timeout.Duration > 0 {
		timeout = step.Timeout.Duration
	}
	cmdCtx, cmdCancel := context.WithTimeout(ctx, timeout)
	defer cmdCancel()

	cmd := exec.Command("/bin/sh", "-c", expandedCmd)
	configureCommandProcessGroup(cmd)

	if e.config.ProjectDir != "" {
		cmd.Dir = e.config.ProjectDir
	}

	env := append(os.Environ(), argEnv...)
	cmd.Env = env

	var stdoutBuf, stderrBuf bytes.Buffer
	if step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stdoutBuf
	}

	waitCondition := step.Wait
	if waitCondition == "" {
		waitCondition = WaitCompletion
	}

	if err := cmd.Start(); err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "command", "command",
			fmt.Sprintf("start failed: %v", err),
			"check shell syntax, executable availability, and project_dir",
			err.Error())
		result.FinishedAt = time.Now()
		slog.Error("command step start failed",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"error", err,
		)
		return result
	}

	if waitCondition == WaitNone {
		go func() {
			cleanup := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if cleanup.Cancelled {
				slog.Warn(EventCommandCancelled,
					"run_id", e.runIDForLog(),
					"workflow", workflow.Name,
					"step_id", step.ID,
					"agent_type", "command",
					FieldDurationMS, time.Since(result.StartedAt).Milliseconds(),
					"bytes_captured", len(stdoutBuf.String()),
					FieldSignalSent, cleanup.SignalSent,
				)
			}
		}()
		result.Status = StatusCompleted
		result.FinishedAt = time.Now()
		slog.Info("command step fire-and-forget",
			"run_id", e.state.RunID,
			"step_id", step.ID,
		)
		return result
	}

	cleanup := waitCommandWithProcessGroupCleanup(cmdCtx, cmd)
	waitErr := cleanup.Err

	output := strings.TrimSpace(stdoutBuf.String())
	if cleanup.Cancelled {
		slog.Warn(EventCommandCancelled,
			"run_id", e.state.RunID,
			"workflow", workflow.Name,
			"step_id", step.ID,
			"agent_type", "command",
			FieldDurationMS, time.Since(result.StartedAt).Milliseconds(),
			"bytes_captured", len(stdoutBuf.String()),
			FieldSignalSent, cleanup.SignalSent,
		)
	}
	if int64(len(output)) > e.limits.MaxCommandStdoutBytes {
		slog.Warn("command stdout truncated",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"bytes_captured", len(output),
			"limit", e.limits.MaxCommandStdoutBytes,
		)
		output = output[:e.limits.MaxCommandStdoutBytes] + fmt.Sprintf("\n[TRUNCATED at %d bytes]", e.limits.MaxCommandStdoutBytes)
	}
	result.Output = output

	if waitErr != nil {
		if ctx.Err() != nil {
			result.Status = StatusCancelled
			errorType := "cancelled"
			reason := "cancelled by workflow context"
			if ctx.Err() == context.DeadlineExceeded {
				errorType = "timeout"
				reason = "workflow exceeded global timeout"
			}
			result.Error = stepRuntimeError(step, "command", errorType,
				reason,
				"retry the run if cancellation was not intentional",
				"")
			result.FinishedAt = time.Now()
			return result
		}
		if cmdCtx.Err() == context.DeadlineExceeded {
			result.Status = StatusFailed
			result.Error = stepRuntimeError(step, "command", "timeout",
				fmt.Sprintf("timed out after %s", timeout),
				"increase step.timeout or reduce command runtime",
				"")
			slog.Warn("command step timed out",
				"run_id", e.state.RunID,
				"step_id", step.ID,
				"timeout", timeout,
			)
			result.FinishedAt = time.Now()
			return result
		}
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.Status = StatusFailed
			result.Error = stepRuntimeError(step, "command", "exit",
				fmt.Sprintf("exit_code=%d", exitErr.ExitCode()),
				"inspect stdout/stderr and command arguments",
				fmt.Sprintf("exit_code=%d", exitErr.ExitCode()))
			slog.Warn("command step exited non-zero",
				"run_id", e.state.RunID,
				"step_id", step.ID,
				"exit_code", exitErr.ExitCode(),
			)
		} else {
			result.Status = StatusFailed
			result.Error = stepRuntimeError(step, "command", "command",
				fmt.Sprintf("wait failed: %v", waitErr),
				"check command process lifecycle and project_dir",
				waitErr.Error())
			slog.Error("command step failed",
				"run_id", e.state.RunID,
				"step_id", step.ID,
				"error", waitErr,
			)
		}
		result.FinishedAt = time.Now()
		return result
	}

	if step.OutputVar != "" && step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		parsed, err := e.parseOutput(output, step.OutputParse)
		if err != nil {
			e.stateMu.Lock()
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "parse",
				Message:   fmt.Sprintf("failed to parse output: %v", err),
				Timestamp: time.Now(),
				Fatal:     false,
			})
			e.stateMu.Unlock()
		} else {
			result.ParsedData = parsed
		}
	}

	result.Status = StatusCompleted
	result.FinishedAt = time.Now()
	slog.Info("command step completed",
		"run_id", e.state.RunID,
		"step_id", step.ID,
	)
	return result
}

// executeTemplate reads a template file, substitutes <KEY> placeholders with
// step Params/Args, validates declared placeholders, and dispatches the
// rendered text to a pane. Wait/timeout behavior mirrors the prompt path.
func (e *Executor) executeTemplate(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		AgentType: "template",
	}

	templatePath := e.resolveTemplatePath(step.Template)
	if templatePath == "" {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "template",
			fmt.Sprintf("template file not found: %s", step.Template),
			"place the template beside the workflow file, under project_dir, or use an absolute path",
			"")
		result.FinishedAt = time.Now()
		return result
	}

	content, err := os.ReadFile(templatePath)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "template",
			fmt.Sprintf("failed to read template: %v", err),
			"check template path permissions and project_dir",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	if int64(len(content)) > e.limits.MaxTemplateBytes {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "limit_exceeded",
			fmt.Sprintf("template file %s exceeds size limit (%d bytes > %d max)", step.Template, len(content), e.limits.MaxTemplateBytes),
			"raise settings.limits.max_template_bytes or split the template",
			"")
		result.FinishedAt = time.Now()
		slog.Error("pipeline.limit.exceeded",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"limit", "max_template_bytes",
			"actual", len(content),
			"max", e.limits.MaxTemplateBytes,
		)
		return result
	}

	reserved := ReservedPlaceholders(e.config.ProjectDir, e.config.Session)
	rendered, err := RenderTemplate(string(content), step.Params, step.Args, reserved)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "template",
			fmt.Sprintf("template rendering failed: %v", err),
			"provide every declared template parameter through params or args",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	rendered = e.substituteVariables(rendered)

	paneID, agentType, err := e.selectPane(step)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "routing",
			fmt.Sprintf("failed to select pane: %v", err),
			"set exactly one of pane, agent, or route and verify panes are available",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}
	result.PaneUsed = paneID
	result.AgentType = agentType

	if e.config.DryRun {
		result.Status = StatusCompleted
		result.Output = dryRunOutput(step,
			fmt.Sprintf("Would dispatch template %s (%d chars) to pane %s", step.Template, len(rendered), paneID))
		result.FinishedAt = time.Now()
		return result
	}

	slog.Info("template step starting",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "template",
		"pane_id", paneID,
		"template", step.Template,
	)

	if workflow.Settings.DispatchLoggingEnabled() {
		e.writeDispatchLog(step.ID, rendered, dispatchLogOptions{
			Template: step.Template,
			PaneID:   paneID,
			Session:  e.config.Session,
			Params:   step.Params,
			Args:     step.Args,
		})
	}

	beforeOutput, _ := e.tmuxClient().CapturePaneOutput(paneID, 2000)

	if ctx.Err() != nil {
		return e.markTemplateCancelled(&result, step, workflow, paneID, ctx.Err().Error())
	}

	if err := e.tmuxClient().PasteKeys(paneID, rendered, true); err != nil {
		if ctx.Err() != nil {
			return e.markTemplateCancelled(&result, step, workflow, paneID, ctx.Err().Error())
		}
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "send",
			fmt.Sprintf("failed to send rendered template: %v", err),
			"check that the target tmux pane still exists and accepts input",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	waitCondition := step.Wait
	if waitCondition == "" {
		waitCondition = WaitCompletion
	}

	timeout := e.config.DefaultTimeout
	if step.Timeout.Duration > 0 {
		timeout = step.Timeout.Duration
	}

	switch waitCondition {
	case WaitNone:
		result.Status = StatusCompleted
		result.FinishedAt = time.Now()
		return result

	case WaitTime:
		select {
		case <-ctx.Done():
			return e.markTemplateCancelled(&result, step, workflow, paneID, ctx.Err().Error())
		case <-time.After(timeout):
			result.Status = StatusCompleted
			result.FinishedAt = time.Now()
		}

	case WaitCompletion, WaitIdle:
		if err := e.waitForIdle(ctx, paneID, timeout); err != nil {
			if ctx.Err() != nil {
				return e.markTemplateCancelled(&result, step, workflow, paneID, ctx.Err().Error())
			} else {
				result.Status = StatusFailed
				result.Error = stepRuntimeError(step, "template", "timeout",
					fmt.Sprintf("timeout waiting for completion: %v", err),
					"increase step.timeout or change wait mode",
					err.Error())
				result.Error.PaneOutput = e.captureErrorContext(paneID, 50)
				result.Error.AgentState = e.detectAgentState(paneID)
			}
			result.FinishedAt = time.Now()
			return result
		}
	}

	afterOutput, err := e.tmuxClient().CapturePaneOutput(paneID, 2000)
	if err != nil {
		result.Status = StatusFailed
		result.Error = stepRuntimeError(step, "template", "capture",
			fmt.Sprintf("failed to capture output: %v", err),
			"check that the target tmux pane still exists",
			err.Error())
		result.FinishedAt = time.Now()
		return result
	}

	result.Output = util.ExtractNewOutput(beforeOutput, afterOutput)

	if step.OutputVar != "" && step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		parsed, err := e.parseOutput(result.Output, step.OutputParse)
		if err != nil {
			e.stateMu.Lock()
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "parse",
				Message:   fmt.Sprintf("failed to parse output: %v", err),
				Timestamp: time.Now(),
				Fatal:     false,
			})
			e.stateMu.Unlock()
		} else {
			result.ParsedData = parsed
		}
	}

	result.Status = StatusCompleted
	result.FinishedAt = time.Now()
	slog.Info("template step completed",
		"run_id", e.state.RunID,
		"step_id", step.ID,
		"pane_id", paneID,
	)
	return result
}

func (e *Executor) markTemplateCancelled(result *StepResult, step *Step, workflow *Workflow, paneID string, reason string) StepResult {
	if reason == "" {
		reason = "cancelled by workflow context"
	}
	result.Status = StatusCancelled
	result.SkipReason = reason
	result.SkipKind = SkipKindCancelled
	result.Error = stepRuntimeError(step, "template", "cancelled",
		reason,
		"retry the run if cancellation was not intentional",
		"")
	result.FinishedAt = time.Now()
	slog.Warn(EventTemplateCancelled,
		"run_id", e.runIDForLog(),
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "template",
		"pane_id", paneID,
		FieldDurationMS, result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
	)
	return *result
}

// resolveTemplatePath resolves a template reference to an absolute file path.
// Searches: workflow file directory, then ProjectDir, then treats it as absolute.
func (e *Executor) resolveTemplatePath(template string) string {
	if filepath.IsAbs(template) {
		if _, err := os.Stat(template); err == nil {
			return template
		}
		return ""
	}

	if e.config.WorkflowFile != "" {
		candidate := filepath.Join(filepath.Dir(e.config.WorkflowFile), template)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	if e.config.ProjectDir != "" {
		candidate := filepath.Join(e.config.ProjectDir, template)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

type dispatchLogOptions struct {
	Template string
	PaneID   string
	Session  string
	Params   map[string]interface{}
	Args     map[string]interface{}
}

// writeDispatchLog writes the rendered template content to session-logs/ in
// the audit format consumed by existing drift checks. Best-effort; failures are
// logged but not fatal.
func (e *Executor) writeDispatchLog(stepID, rendered string, opts ...dispatchLogOptions) {
	dir := e.config.ProjectDir
	if dir == "" {
		return
	}
	logDir := filepath.Join(dir, "session-logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		slog.Warn("failed to create session-logs dir", "run_id", e.runIDForLog(), "step_id", stepID, "error", err)
		return
	}

	var opt dispatchLogOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	// Nanosecond precision + a process-local sequence counter so two writes
	// from the same step in the same second (retry/recovery, foreach
	// fan-out, rapid repeated runs) cannot collide on a filename and silently
	// overwrite an earlier audit log (bd-45fs8). Sortable lexicographically.
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	seq := atomic.AddUint64(&dispatchLogSeq, 1)
	filename := filepath.Join(logDir, fmt.Sprintf("dispatch-%s-%06d-%s.log", ts, seq, sanitizeDispatchLogStepID(stepID)))
	if err := os.WriteFile(filename, []byte(formatDispatchLog(opt, rendered)), 0o644); err != nil {
		slog.Warn("failed to write dispatch log", "run_id", e.runIDForLog(), "step_id", stepID, "error", err, "path", filename)
	}
}

func (e *Executor) runIDForLog() string {
	if e.state == nil {
		return ""
	}
	return e.state.RunID
}

func formatDispatchLog(opt dispatchLogOptions, rendered string) string {
	var b strings.Builder
	b.WriteString("=== Dispatch ===\n")
	fmt.Fprintf(&b, "MO: %s\n", opt.Template)
	fmt.Fprintf(&b, "Target pane: %s\n", opt.PaneID)
	fmt.Fprintf(&b, "Target session: %s\n", opt.Session)
	b.WriteString("Params:\n")
	for _, key := range sortedDispatchParamKeys(opt.Args, opt.Params) {
		fmt.Fprintf(&b, "  %s=%v\n", key, dispatchParamValue(key, opt.Args, opt.Params))
	}
	b.WriteString("=== Rendered ===\n")
	b.WriteString(rendered)
	if rendered != "" && !strings.HasSuffix(rendered, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func sortedDispatchParamKeys(maps ...map[string]interface{}) []string {
	seen := make(map[string]bool)
	var keys []string
	for _, values := range maps {
		for key := range values {
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func dispatchParamValue(key string, args, params map[string]interface{}) interface{} {
	if params != nil {
		if value, ok := params[key]; ok {
			return value
		}
	}
	if args != nil {
		return args[key]
	}
	return nil
}

func sanitizeDispatchLogStepID(stepID string) string {
	if stepID == "" {
		return "step"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, stepID)
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
	onError := resolveErrorAction(step.OnError, workflow.Settings.OnError)

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
		stepProgressMessage("Starting parallel group", step, fmt.Sprintf("%d steps, on_error=%s", len(step.Parallel.Steps), onError)),
		e.calculateProgress())
	e.beginParallelState(step.ID, len(step.Parallel.Steps))
	e.persistState()

	var wg sync.WaitGroup
	results := make([]StepResult, len(step.Parallel.Steps))
	var mu sync.Mutex
	var firstError error
	var cancelled bool
	completionOrder := make([]string, 0, len(step.Parallel.Steps))

	// Track used panes to coordinate agent selection
	usedPanes := make(map[string]bool)
	var panesMu sync.Mutex

	// Semaphore to limit concurrency (max 8 parallel steps)
	sem := make(chan struct{}, 8)

	for i, pStep := range step.Parallel.Steps {
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
					SkipKind:   SkipKindCancelled,
				}
				e.stateMu.Lock()
				e.state.Steps[ps.ID] = results[idx]
				e.state.UpdatedAt = time.Now()
				e.stateMu.Unlock()
				mu.Unlock()
				e.markParallelSubstepFinished(step.ID, ps.ID, StatusCancelled)
				e.persistState()
				return
			case sem <- struct{}{}: // Acquire token
				// Continue execution
			}
			defer func() { <-sem }() // Release token

			// Execute the step with pane coordination
			e.markParallelSubstepStarted(step.ID, ps.ID)
			e.persistState()
			pResult := e.executeParallelStep(parallelCtx, &ps, workflow, usedPanes, &panesMu)

			mu.Lock()
			results[idx] = pResult
			completionOrder = append(completionOrder, pResult.StepID)
			e.stateMu.Lock()
			e.state.Steps[ps.ID] = pResult
			e.state.UpdatedAt = time.Now()
			e.stateMu.Unlock()

			// Handle fail_fast: cancel remaining steps on first error
			if pResult.Status == StatusFailed && onError == ErrorActionFailFast {
				if firstError == nil {
					firstError = fmt.Errorf("step %s failed", ps.ID)
					cancelled = true
					cancelParallel()
				}
			}
			mu.Unlock()

			e.markParallelSubstepFinished(step.ID, ps.ID, pResult.Status)
			e.persistState()
		}(i, pStep)
	}

	wg.Wait()
	e.completeParallelState(step.ID)

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
	e.storeParallelOutputVars(step, results, completionOrder)

	// Determine final status based on error mode and results
	if failed > 0 || cancelled {
		aggregatedErr := aggregateParallelErrors(results, len(results))
		switch onError {
		case ErrorActionContinue:
			result.Status = StatusCompleted
			result.Output = fmt.Sprintf("Parallel group completed with %d/%d successful", completed, len(results))
			result.Error = aggregatedErr
		case ErrorActionFailFast:
			result.Status = StatusFailed
			result.Error = aggregatedErr
			if result.Error == nil {
				result.Error = &StepError{
					Type:      "parallel_fail_fast",
					Message:   fmt.Sprintf("%d failed, %d cancelled (fail_fast mode)", failed, cancelledCount),
					Timestamp: time.Now(),
				}
			}
			result.Error.Type = "parallel_fail_fast"
		default: // ErrorActionFail or ErrorActionRetry after step-level retries are exhausted
			result.Status = StatusFailed
			result.Error = aggregatedErr
			if result.Error == nil {
				result.Error = &StepError{
					Type:      "parallel",
					Message:   fmt.Sprintf("%d of %d parallel steps failed", failed, len(results)),
					Timestamp: time.Now(),
				}
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
	e.markStepInFlight(step.ID, "parallel_step", -1)
	e.persistState()
	defer e.clearStepInFlight(step.ID)

	// Check for unsupported nested structures
	if len(step.Parallel.Steps) > 0 || step.Loop != nil {
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
		result.SkipKind = SkipKindCancelled
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
			result.SkipKind = SkipKindWhenCondition
			result.FinishedAt = time.Now()
			return result
		}
	}

	// Select pane with coordination to avoid reusing agents
	// We select once and reuse for retries to avoid "self-exclusion" issues
	paneID, agentType, err := e.selectAndMarkPane(step, usedPanes, panesMu)
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

	// Calculate retry parameters
	maxAttempts := 1
	if resolveErrorAction(step.OnError, workflow.Settings.OnError) == ErrorActionRetry {
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
			stepProgressMessage("Executing parallel step", step, fmt.Sprintf("attempt %d/%d on %s", attempt, maxAttempts, agentType)),
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
			result.Output = dryRunOutput(step, "Would execute: "+truncatePrompt(prompt, 100))
			result.FinishedAt = time.Now()
			return result
		}

		// Capture state before sending
		beforeOutput, _ = e.tmuxClient().CapturePaneOutput(paneID, 2000)

		// Send prompt
		if err := e.tmuxClient().PasteKeys(paneID, prompt, true); err != nil {
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
				result.SkipKind = SkipKindCancelled
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
					result.SkipKind = SkipKindCancelled
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
			afterOutput, captureErr = e.tmuxClient().CapturePaneOutput(paneID, 2000)
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

func resolveErrorAction(stepOnError, workflowOnError ErrorAction) ErrorAction {
	if stepOnError != "" {
		return stepOnError
	}
	if workflowOnError != "" {
		return workflowOnError
	}
	return ErrorActionFail
}

// selectAndMarkPane selects a pane for a step and marks it as used atomically.
// This prevents race conditions where multiple parallel steps select the same agent.
func (e *Executor) selectAndMarkPane(step *Step, usedPanes map[string]bool, panesMu *sync.Mutex) (paneID string, agentType string, err error) {
	// In dry run mode, return dummy pane info
	if e.config.DryRun {
		return "dry-run-pane", "dry-run-agent", nil
	}

	// Explicit pane selection bypasses exclusion. Pane.Expr (template form
	// like ${defaults.triage_pane}) requires variable substitution before
	// resolution; the executor's variable layer expands it elsewhere and
	// fills Pane.Index. If we reach this path with a non-empty Expr but
	// Index still 0, surface a clear error.
	if step.Pane.Expr != "" && step.Pane.Index == 0 {
		return "", "", fmt.Errorf("pane expression %q not yet resolved at pane-selection time", step.Pane.Expr)
	}
	if step.Pane.Index > 0 {
		panes, err := e.tmuxClient().GetPanes(e.config.Session)
		if err != nil {
			return "", "", fmt.Errorf("failed to get panes: %w", err)
		}
		for _, p := range panes {
			if p.Index == step.Pane.Index {
				// We still need to mark it as used to prevent others from picking it via auto-selection
				panesMu.Lock()
				usedPanes[p.ID] = true
				panesMu.Unlock()
				return p.ID, string(p.Type), nil
			}
		}
		return "", "", fmt.Errorf("pane %d not found", step.Pane.Index)
	}

	// Use ScoreAgents to get all scored agents (slow operation, do outside lock)
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

	// Begin atomic selection and marking
	panesMu.Lock()
	defer panesMu.Unlock()

	// Filter out excluded agents and already-used panes
	available := make([]robot.ScoredAgent, 0, len(agents))
	for _, a := range agents {
		if !a.Excluded && !usedPanes[a.PaneID] {
			available = append(available, a)
		}
	}

	// If all agents are used, allow reuse (fall back to original list minus excluded)
	// This degrades parallelism but prevents starvation
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

	// Mark as used
	paneID = routeResult.Selected.PaneID
	usedPanes[paneID] = true

	return paneID, routeResult.Selected.AgentType, nil
}

// selectPane finds the appropriate pane for a step
func (e *Executor) selectPane(step *Step) (paneID string, agentType string, err error) {
	// In dry run mode, return dummy pane info
	if e.config.DryRun {
		return "dry-run-pane", "dry-run-agent", nil
	}

	// Explicit pane selection. Pane.Expr (template form) needs variable
	// substitution before this path; surface a clear error if we got here
	// with an unresolved expression.
	if step.Pane.Expr != "" && step.Pane.Index == 0 {
		return "", "", fmt.Errorf("pane expression %q not yet resolved at pane-selection time", step.Pane.Expr)
	}
	if step.Pane.Index > 0 {
		panes, err := e.tmuxClient().GetPanes(e.config.Session)
		if err != nil {
			return "", "", fmt.Errorf("failed to get panes: %w", err)
		}
		for _, p := range panes {
			if p.Index == step.Pane.Index {
				return p.ID, string(p.Type), nil
			}
		}
		return "", "", fmt.Errorf("pane %d not found", step.Pane.Index)
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
// Thread-safe: acquires read locks on Variables and Steps for concurrent access during parallel execution.
func (e *Executor) substituteVariables(s string) string {
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	sub := NewSubstitutor(e.state, e.config.Session, e.state.WorkflowID)
	sub.SetDefaults(e.defaults)
	s = e.substituteRuntimeVariables(s)
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
// Thread-safe: acquires read locks on Variables and Steps for concurrent access during parallel execution.
func (e *Executor) evaluateCondition(condition string) (bool, error) {
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	sub := NewSubstitutor(e.state, e.config.Session, e.state.WorkflowID)
	sub.SetDefaults(e.defaults)
	condition = e.substituteRuntimeVariables(condition)
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
		if result.Status == StatusCompleted || result.Status == StatusFailed || result.Status == StatusSkipped || result.Status == StatusCancelled {
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

func workflowProgressMessage(action string, workflow *Workflow) string {
	if workflow == nil {
		return action
	}
	message := fmt.Sprintf("%s: %s", action, workflow.Name)
	if desc := shortDescription(workflow.Description); desc != "" {
		message += ": " + desc
	}
	return message
}

func stepProgressMessage(action string, step *Step, detail string) string {
	if step == nil {
		if detail == "" {
			return action
		}
		return fmt.Sprintf("%s (%s)", action, detail)
	}
	message := fmt.Sprintf("%s %s", action, step.ID)
	if desc := shortDescription(step.Description); desc != "" {
		message += ": " + desc
	}
	if detail != "" {
		message += " (" + detail + ")"
	}
	return message
}

func dryRunOutput(step *Step, action string) string {
	return fmt.Sprintf("%s\n[DRY RUN] %s", dryRunDispatchLine(step), action)
}

func dryRunDispatchLine(step *Step) string {
	if step == nil {
		return "▶"
	}
	if desc := shortDescription(step.Description); desc != "" {
		return fmt.Sprintf("▶ [%s] %s", step.ID, desc)
	}
	return fmt.Sprintf("▶ [%s]", step.ID)
}

func shortDescription(desc string) string {
	return truncatePrompt(strings.TrimSpace(desc), 80)
}

// describeForeach builds a one-line summary of a ForeachConfig for dry-run
// output and skip-reason messages. Picks the most-specific iteration source
// available so the operator can see at a glance what the step would loop over.
func describeForeach(fc *ForeachConfig) string {
	if fc == nil {
		return "(empty foreach)"
	}
	src := ""
	switch {
	case fc.Items != "":
		src = "items=" + truncatePrompt(fc.Items, 40)
	case fc.Beads != "":
		src = "beads=" + truncatePrompt(fc.Beads, 40)
	case fc.Pairs != "":
		src = "pairs=" + truncatePrompt(fc.Pairs, 40)
	case fc.Debates != "":
		src = "debates=" + truncatePrompt(fc.Debates, 40)
	case len(fc.Models) > 0:
		src = "models=[" + strings.Join(fc.Models, ",") + "]"
	default:
		src = "(no iteration source)"
	}
	if fc.Filter != "" {
		src += " filter=" + fc.Filter
	}
	if fc.PaneStrategy != "" {
		src += " strategy=" + fc.PaneStrategy
	}
	if fc.Template != "" {
		src += " template=" + fc.Template
	}
	return src
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
	if e.graph == nil {
		return
	}

	var rerunStepIDs []string

	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	for stepID, result := range e.state.Steps {
		if shouldRerunStep(result) {
			rerunStepIDs = append(rerunStepIDs, stepID)
			continue
		}
		if err := e.graph.MarkExecuted(stepID); err != nil {
			delete(e.state.Steps, stepID)
		}
	}
	for stepID := range e.state.InFlightSteps {
		rerunStepIDs = append(rerunStepIDs, stepID)
		delete(e.state.Steps, stepID)
	}
	e.state.InFlightSteps = nil

	for _, stepID := range rerunStepIDs {
		delete(e.state.Steps, stepID)
	}
	e.stateMu.Unlock()

	if len(rerunStepIDs) == 0 {
		return
	}

	for _, stepID := range rerunStepIDs {
		e.clearStepVariables(stepID)
	}
}

func shouldRerunStep(result StepResult) bool {
	switch result.Status {
	case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
		return true
	case StatusSkipped:
		if result.SkipKind == SkipKindFailedDependency || strings.HasPrefix(result.SkipReason, "dependency failed") {
			return true
		}
		if result.SkipKind == SkipKindCancelled || strings.HasPrefix(result.SkipReason, "cancelled") {
			return true
		}
	}
	return result.Status == ""
}

func (e *Executor) clearStepVariables(stepID string) {
	e.varMu.Lock()
	defer e.varMu.Unlock()

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
	e.stateMu.RLock()
	if e.state == nil {
		e.stateMu.RUnlock()
		return nil
	}

	snapshot := *e.state

	if e.state.Steps != nil {
		snapshot.Steps = make(map[string]StepResult, len(e.state.Steps))
		for key, value := range e.state.Steps {
			value.ParsedData = cloneInterfaceValue(value.ParsedData)
			snapshot.Steps[key] = value
		}
	}
	if e.state.ForeachState != nil {
		snapshot.ForeachState = make(map[string]ForeachIterationState, len(e.state.ForeachState))
		for key, value := range e.state.ForeachState {
			value.CompletedIterationIDs = append([]string(nil), value.CompletedIterationIDs...)
			snapshot.ForeachState[key] = value
		}
	}
	if e.state.ParallelState != nil {
		snapshot.ParallelState = make(map[string]ParallelGroupState, len(e.state.ParallelState))
		for key, value := range e.state.ParallelState {
			value.CompletedStepIDs = append([]string(nil), value.CompletedStepIDs...)
			value.FailedStepIDs = append([]string(nil), value.FailedStepIDs...)
			value.InFlightStepIDs = append([]string(nil), value.InFlightStepIDs...)
			snapshot.ParallelState[key] = value
		}
	}
	if e.state.InFlightSteps != nil {
		snapshot.InFlightSteps = make(map[string]InFlightStepState, len(e.state.InFlightSteps))
		for key, value := range e.state.InFlightSteps {
			snapshot.InFlightSteps[key] = value
		}
	}
	e.stateMu.RUnlock()

	e.varMu.RLock()
	if e.state.Variables != nil {
		snapshot.Variables = make(map[string]interface{}, len(e.state.Variables))
		for key, value := range e.state.Variables {
			snapshot.Variables[key] = cloneInterfaceValue(value)
		}
	}
	if e.state.ScopeStack != nil {
		snapshot.ScopeStack = make([]ScopeFrame, len(e.state.ScopeStack))
		for i, frame := range e.state.ScopeStack {
			snapshot.ScopeStack[i] = frame
			if frame.Variables != nil {
				snapshot.ScopeStack[i].Variables = make(map[string]interface{}, len(frame.Variables))
				for key, value := range frame.Variables {
					snapshot.ScopeStack[i].Variables[key] = cloneInterfaceValue(value)
				}
			}
		}
	}
	e.varMu.RUnlock()

	return &snapshot
}

func (e *Executor) persistState() {
	if e.state == nil {
		return
	}

	now := time.Now()
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	e.state.LastCheckpointAt = now
	e.state.UpdatedAt = now
	e.stateMu.Unlock()

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

// GetState returns a snapshot of the current execution state (for monitoring).
// Returns a copy to avoid racing with concurrent writers.
func (e *Executor) GetState() *ExecutionState {
	return e.snapshotState()
}

// captureErrorContext captures recent pane output for error debugging
func (e *Executor) captureErrorContext(paneID string, lines int) string {
	if paneID == "" || e.config.DryRun {
		return ""
	}
	output, err := e.tmuxClient().CapturePaneOutput(paneID, lines)
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

// StartFromSkipReason is the SkipReason set on synthetic step results
// generated when --start-from skips dependency-wise prior steps.
const StartFromSkipReason = "--start-from skipped"

// findStepContainer walks workflow.Steps recursively and reports whether
// stepID lives inside a parallel/loop/foreach body. The container kind helps
// the operator understand why --start-from was rejected.
func findStepContainer(workflow *Workflow, stepID string) (parentID, kind string, inside bool) {
	if workflow == nil {
		return "", "", false
	}
	var walk func(steps []Step, parent *Step, parentKind string) bool
	walk = func(steps []Step, parent *Step, parentKind string) bool {
		for i := range steps {
			s := &steps[i]
			if parent != nil && s.ID == stepID {
				parentID = parent.ID
				kind = parentKind
				inside = true
				return true
			}
			if len(s.Parallel.Steps) > 0 {
				if walk(s.Parallel.Steps, s, "parallel") {
					return true
				}
			}
			if s.Loop != nil && len(s.Loop.Steps) > 0 {
				if walk(s.Loop.Steps, s, "loop") {
					return true
				}
			}
			if s.Foreach != nil && len(s.Foreach.Steps) > 0 {
				if walk(s.Foreach.Steps, s, "foreach") {
					return true
				}
			}
		}
		return false
	}
	walk(workflow.Steps, nil, "")
	return
}

// applyStartFrom synthesizes Skipped step results for every transitive
// dependency of e.config.StartFromStep, copies prior outputs from
// e.config.StartFromState when available, and marks those steps executed in
// the dependency graph so e.executeWorkflow runs only at-or-after the target.
func (e *Executor) applyStartFrom(workflow *Workflow) error {
	target := e.config.StartFromStep
	if target == "" {
		return nil
	}

	// Check for nested-body containment first: foreach body steps are not
	// indexed in the dependency graph, so a graph-only lookup would misreport
	// them as "not found" instead of surfacing the foreach-body rejection.
	if parent, kind, inside := findStepContainer(workflow, target); inside {
		return fmt.Errorf("--start-from step %q is inside %s body of %q; --start-from must target a top-level step (parent: %q)", target, kind, parent, parent)
	}
	if _, ok := e.graph.GetStep(target); !ok {
		return fmt.Errorf("--start-from step %q not found in workflow", target)
	}

	// Compute transitive deps via BFS over the dependency graph.
	skipSet := make(map[string]struct{})
	queue := append([]string(nil), e.graph.GetDependencies(target)...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := skipSet[id]; seen {
			continue
		}
		skipSet[id] = struct{}{}
		queue = append(queue, e.graph.GetDependencies(id)...)
	}

	if len(skipSet) == 0 {
		// Nothing to skip; target had no dependencies.
		return nil
	}

	// Synthesize results in deterministic order so log output and persisted
	// state are reproducible across runs.
	skipped := make([]string, 0, len(skipSet))
	for id := range skipSet {
		skipped = append(skipped, id)
	}
	sort.Strings(skipped)

	now := time.Now()
	prior := e.config.StartFromState

	e.stateMu.Lock()
	e.varMu.Lock()
	for _, id := range skipped {
		result := StepResult{
			StepID:     id,
			Status:     StatusSkipped,
			SkipReason: StartFromSkipReason,
			SkipKind:   SkipKindStartFrom,
			StartedAt:  now,
			FinishedAt: now,
		}
		if prior != nil {
			if priorResult, ok := prior.Steps[id]; ok {
				result.Output = priorResult.Output
				result.ParsedData = priorResult.ParsedData
				result.PaneUsed = priorResult.PaneUsed
				result.AgentType = priorResult.AgentType
			}
		}
		e.state.Steps[id] = result

		// Make ${steps.X.output} resolvable by populating both the canonical
		// step-output path and any output_var the step declared.
		if result.Output != "" || result.ParsedData != nil {
			StoreStepOutput(e.state, id, result.Output, result.ParsedData)
			if step, ok := e.graph.GetStep(id); ok && step.OutputVar != "" {
				e.state.Variables[step.OutputVar] = result.Output
				if result.ParsedData != nil {
					e.state.Variables[step.OutputVar+"_parsed"] = result.ParsedData
				}
			}
		}
	}
	e.varMu.Unlock()
	e.stateMu.Unlock()

	for _, id := range skipped {
		if err := e.graph.MarkExecuted(id); err != nil {
			return fmt.Errorf("--start-from: mark %q executed: %w", id, err)
		}
	}

	slog.Info("pipeline.start_from.applied",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"target", target,
		"skipped_count", len(skipped),
		"with_prior_state", prior != nil,
	)
	return nil
}
