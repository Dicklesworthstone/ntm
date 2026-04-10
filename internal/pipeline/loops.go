// Package pipeline provides workflow execution for AI agent orchestration.
// loops.go implements loop constructs for workflow steps: for-each, while, and times loops.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LoopExecutor handles execution of loop constructs within workflows.
type LoopExecutor struct {
	executor *Executor
}

// NewLoopExecutor creates a new loop executor for the given workflow executor.
func NewLoopExecutor(executor *Executor) *LoopExecutor {
	return &LoopExecutor{executor: executor}
}

// LoopResult contains the result of loop execution.
type LoopResult struct {
	Status      ExecutionStatus
	Iterations  int
	Results     []StepResult  // Individual iteration results
	Collected   []interface{} // Collected outputs if Collect is specified
	Error       *StepError
	BreakReason string // Non-empty if loop exited via break
	FinishedAt  time.Time
}

// ErrLoopBreak is returned when a loop is exited via break control.
type ErrLoopBreak struct {
	Reason string
}

func (e *ErrLoopBreak) Error() string {
	if e.Reason != "" {
		return "loop break: " + e.Reason
	}
	return "loop break"
}

// ErrLoopContinue is returned when an iteration should be skipped.
type ErrLoopContinue struct{}

func (e *ErrLoopContinue) Error() string {
	return "loop continue"
}

// ErrMaxIterations is returned when the max iterations limit is reached.
type ErrMaxIterations struct {
	Limit int
}

func (e *ErrMaxIterations) Error() string {
	return fmt.Sprintf("max iterations limit reached (%d)", e.Limit)
}

// ExecuteLoop executes a loop step and returns the aggregated result.
func (le *LoopExecutor) ExecuteLoop(ctx context.Context, step *Step, workflow *Workflow) LoopResult {
	loop := step.Loop
	if loop == nil {
		return LoopResult{
			Status: StatusFailed,
			Error: &StepError{
				Type:      "loop",
				Message:   "step has no loop configuration",
				Timestamp: time.Now(),
			},
			FinishedAt: time.Now(),
		}
	}

	// Determine loop type and execute
	// Priority: items > while > times (times: 0 is valid for immediate completion)
	switch {
	case loop.Items != "":
		return le.executeForEach(ctx, step, loop, workflow)
	case loop.While != "":
		return le.executeWhile(ctx, step, loop, workflow)
	default:
		// Default to times loop (Times: 0 means zero iterations = immediate completion)
		return le.executeTimes(ctx, step, loop, workflow)
	}
}

// executeForEach implements for-each loop iteration over an array.
func (le *LoopExecutor) executeForEach(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow) LoopResult {
	result := LoopResult{
		Status:    StatusRunning,
		Results:   make([]StepResult, 0),
		Collected: make([]interface{}, 0),
	}

	// Resolve items expression to get the array
	items, err := le.resolveItems(loop.Items)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   fmt.Sprintf("failed to resolve items: %v", err),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	// Determine loop variable name
	varName := loop.As
	if varName == "" {
		varName = "item"
	}

	total := len(items)

	// Calculate max iterations
	maxIterations := loop.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}
	if total > maxIterations {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   fmt.Sprintf("items count (%d) exceeds max_iterations (%d)", total, maxIterations),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	le.executor.emitProgress("loop_start", step.ID,
		fmt.Sprintf("Starting for-each loop with %d items", total), le.executor.calculateProgress())

	// Iterate over items
	for i, item := range items {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			le.clearLoopVars(varName)
			return result
		default:
		}

		// Set loop variables
		le.setLoopVars(varName, item, i, total)

		// Execute nested steps
		iterResult, shouldBreak, shouldContinue := le.executeIteration(ctx, step, loop, workflow, i)

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		// Collect output if configured
		if loop.Collect != "" && len(iterResult) > 0 {
			lastResult := iterResult[len(iterResult)-1]
			if lastResult.ParsedData != nil {
				result.Collected = append(result.Collected, lastResult.ParsedData)
			} else if lastResult.Output != "" {
				result.Collected = append(result.Collected, lastResult.Output)
			}
		}

		if shouldBreak {
			if len(iterResult) > 0 && iterResult[len(iterResult)-1].Status == StatusFailed {
				result.Error = iterResult[len(iterResult)-1].Error
			} else {
				result.BreakReason = "break statement"
			}
			break
		}

		if shouldContinue {
			continue
		}

		// Handle delay between iterations
		if loop.Delay.Duration > 0 && i < total-1 {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				le.clearLoopVars(varName)
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Store collected results if configured
	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

	le.clearLoopVars(varName)

	if result.Error != nil {
		result.Status = StatusFailed
	} else {
		result.Status = StatusCompleted
	}
	result.FinishedAt = time.Now()

	le.executor.emitProgress("loop_complete", step.ID,
		fmt.Sprintf("For-each loop completed: %d iterations", result.Iterations),
		le.executor.calculateProgress())

	return result
}

// executeWhile implements while loop with condition evaluation.
func (le *LoopExecutor) executeWhile(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow) LoopResult {
	result := LoopResult{
		Status:    StatusRunning,
		Results:   make([]StepResult, 0),
		Collected: make([]interface{}, 0),
	}

	// While loops require max_iterations for safety
	maxIterations := loop.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	varName := loop.As
	if varName == "" {
		varName = "item"
	}

	le.executor.emitProgress("loop_start", step.ID,
		fmt.Sprintf("Starting while loop (max %d iterations)", maxIterations),
		le.executor.calculateProgress())

	// Iterate while condition is true
	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			le.clearLoopVars(varName)
			return result
		default:
		}

		// Evaluate while condition
		shouldSkip, err := le.executor.evaluateCondition(loop.While)
		if err != nil {
			result.Status = StatusFailed
			result.Error = &StepError{
				Type:      "loop",
				Message:   fmt.Sprintf("failed to evaluate while condition: %v", err),
				Timestamp: time.Now(),
			}
			result.FinishedAt = time.Now()
			le.clearLoopVars(varName)
			return result
		}

		// EvaluateCondition returns true if step should be SKIPPED (condition is false)
		if shouldSkip {
			// Condition is false, exit loop
			break
		}

		// Set loop variables (for while loops, item is the iteration count)
		le.setLoopVars(varName, i, i, maxIterations)

		// Execute nested steps
		iterResult, shouldBreak, shouldContinue := le.executeIteration(ctx, step, loop, workflow, i)

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		// Collect output if configured
		if loop.Collect != "" && len(iterResult) > 0 {
			lastResult := iterResult[len(iterResult)-1]
			if lastResult.ParsedData != nil {
				result.Collected = append(result.Collected, lastResult.ParsedData)
			} else if lastResult.Output != "" {
				result.Collected = append(result.Collected, lastResult.Output)
			}
		}

		if shouldBreak {
			if len(iterResult) > 0 && iterResult[len(iterResult)-1].Status == StatusFailed {
				result.Error = iterResult[len(iterResult)-1].Error
			} else {
				result.BreakReason = "break statement"
			}
			break
		}

		if shouldContinue {
			continue
		}

		// Handle delay between iterations
		if loop.Delay.Duration > 0 {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				le.clearLoopVars(varName)
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Check if we hit max iterations without condition becoming false
	if result.Iterations >= maxIterations {
		// Evaluate condition one more time to see if it's still true
		shouldSkip, _ := le.executor.evaluateCondition(loop.While)
		if !shouldSkip {
			// Condition is still true, we hit the limit
			result.Error = &StepError{
				Type:      "loop",
				Message:   fmt.Sprintf("while loop reached max_iterations limit (%d)", maxIterations),
				Timestamp: time.Now(),
			}
		}
	}

	// Store collected results if configured
	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

	le.clearLoopVars(varName)

	if result.Error != nil {
		result.Status = StatusFailed
	} else {
		result.Status = StatusCompleted
	}
	result.FinishedAt = time.Now()

	le.executor.emitProgress("loop_complete", step.ID,
		fmt.Sprintf("While loop completed: %d iterations", result.Iterations),
		le.executor.calculateProgress())

	return result
}

// executeTimes implements a simple repeat N times loop.
func (le *LoopExecutor) executeTimes(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow) LoopResult {
	result := LoopResult{
		Status:    StatusRunning,
		Results:   make([]StepResult, 0),
		Collected: make([]interface{}, 0),
	}

	times := loop.Times
	if times <= 0 {
		result.Status = StatusCompleted
		result.FinishedAt = time.Now()
		return result
	}

	// Apply max iterations limit
	maxIterations := loop.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}
	if times > maxIterations {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   fmt.Sprintf("times (%d) exceeds max_iterations (%d)", times, maxIterations),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	varName := loop.As
	if varName == "" {
		varName = "item"
	}

	le.executor.emitProgress("loop_start", step.ID,
		fmt.Sprintf("Starting times loop (%d iterations)", times),
		le.executor.calculateProgress())

	// Iterate N times
	for i := 0; i < times; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			le.clearLoopVars(varName)
			return result
		default:
		}

		// Set loop variables
		le.setLoopVars(varName, i, i, times)

		// Execute nested steps
		iterResult, shouldBreak, shouldContinue := le.executeIteration(ctx, step, loop, workflow, i)

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		// Collect output if configured
		if loop.Collect != "" && len(iterResult) > 0 {
			lastResult := iterResult[len(iterResult)-1]
			if lastResult.ParsedData != nil {
				result.Collected = append(result.Collected, lastResult.ParsedData)
			} else if lastResult.Output != "" {
				result.Collected = append(result.Collected, lastResult.Output)
			}
		}

		if shouldBreak {
			if len(iterResult) > 0 && iterResult[len(iterResult)-1].Status == StatusFailed {
				result.Error = iterResult[len(iterResult)-1].Error
			} else {
				result.BreakReason = "break statement"
			}
			break
		}

		if shouldContinue {
			continue
		}

		// Handle delay between iterations
		if loop.Delay.Duration > 0 && i < times-1 {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				le.clearLoopVars(varName)
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Store collected results if configured
	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

	le.clearLoopVars(varName)

	if result.Error != nil {
		result.Status = StatusFailed
	} else {
		result.Status = StatusCompleted
	}
	result.FinishedAt = time.Now()

	le.executor.emitProgress("loop_complete", step.ID,
		fmt.Sprintf("Times loop completed: %d iterations", result.Iterations),
		le.executor.calculateProgress())

	return result
}

// executeIteration executes a single loop iteration (all nested steps).
// Returns the step results, whether to break, and whether to continue.
func (le *LoopExecutor) executeIteration(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow, iterIndex int) ([]StepResult, bool, bool) {
	results := make([]StepResult, 0, len(loop.Steps))

	for _, nestedStep := range loop.Steps {
		select {
		case <-ctx.Done():
			return results, false, false
		default:
		}

		// Check for loop control statements
		if nestedStep.LoopControl == LoopControlBreak {
			// Evaluate condition if present
			if nestedStep.When != "" {
				skip, err := le.executor.evaluateCondition(nestedStep.When)
				if err == nil && !skip {
					// Condition is true, break
					return results, true, false
				}
			} else {
				// Unconditional break
				return results, true, false
			}
			continue
		}

		if nestedStep.LoopControl == LoopControlContinue {
			// Evaluate condition if present
			if nestedStep.When != "" {
				skip, err := le.executor.evaluateCondition(nestedStep.When)
				if err == nil && !skip {
					// Condition is true, continue
					return results, false, true
				}
			} else {
				// Unconditional continue
				return results, false, true
			}
			continue
		}

		// Execute the nested step
		// Create a unique step ID for this iteration to avoid conflicts
		iteratedStep := nestedStep
		iteratedStep.ID = fmt.Sprintf("%s_iter%d_%s", step.ID, iterIndex, nestedStep.ID)

		result := le.executor.executeStep(ctx, &iteratedStep, workflow)
		results = append(results, result)

		// Store result in state
		le.executor.stateMu.Lock()
		le.executor.state.Steps[iteratedStep.ID] = result
		le.executor.stateMu.Unlock()

		// Handle step failure based on error action
		if result.Status == StatusFailed {
			onError := resolveErrorAction(nestedStep.OnError, workflow.Settings.OnError)

			switch onError {
			case ErrorActionFail, ErrorActionFailFast:
				// Stop loop on failure
				return results, true, false
			case ErrorActionContinue:
				// Continue with next step in iteration
			}
		}
	}

	return results, false, false
}

// resolveItems resolves an items expression to an array of values.
func (le *LoopExecutor) resolveItems(expr string) ([]interface{}, error) {
	// Substitute variables in the expression
	resolved := le.executor.substituteVariables(expr)

	// Check if it's a direct array in Variables
	le.executor.varMu.RLock()
	defer le.executor.varMu.RUnlock()

	// Try to resolve as a variable reference
	sub := NewSubstitutor(le.executor.state, le.executor.config.Session, le.executor.state.WorkflowID)

	// Strip ${ and } if present
	varPath := resolved
	if strings.HasPrefix(resolved, "${") && strings.HasSuffix(resolved, "}") {
		varPath = resolved[2 : len(resolved)-1]
	}

	// Try to look up directly in variables first
	if val, ok := le.executor.state.Variables[varPath]; ok {
		return toInterfaceSlice(val)
	}

	// Try to resolve through substitutor. This may read from state.Steps, so acquire stateMu.
	le.executor.stateMu.RLock()
	val, err := sub.Substitute(expr)
	le.executor.stateMu.RUnlock()

	if err != nil {
		return nil, err
	}

	// Parse the result
	return parseItemsString(val)
}

// toInterfaceSlice converts various array types to []interface{}.
func toInterfaceSlice(v interface{}) ([]interface{}, error) {
	switch arr := v.(type) {
	case []interface{}:
		return arr, nil
	case []string:
		result := make([]interface{}, len(arr))
		for i, s := range arr {
			result[i] = s
		}
		return result, nil
	case []int:
		result := make([]interface{}, len(arr))
		for i, n := range arr {
			result[i] = n
		}
		return result, nil
	case []float64:
		result := make([]interface{}, len(arr))
		for i, n := range arr {
			result[i] = n
		}
		return result, nil
	case string:
		return parseItemsString(arr)
	default:
		return nil, fmt.Errorf("cannot iterate over type %T", v)
	}
}

// parseItemsString parses a string representation of items.
// Supports comma-separated values and JSON arrays.
func parseItemsString(s string) ([]interface{}, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return []interface{}{}, nil
	}

	// Try JSON array first
	if strings.HasPrefix(s, "[") {
		var arr []interface{}
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr, nil
		}
	}

	// Split by comma
	parts := strings.Split(s, ",")
	result := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result, nil
}

// setLoopVars sets loop context variables.
func (le *LoopExecutor) setLoopVars(varName string, item interface{}, index, total int) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	SetLoopVars(le.executor.state, varName, item, index, total)
}

// clearLoopVars removes loop context variables.
func (le *LoopExecutor) clearLoopVars(varName string) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	ClearLoopVars(le.executor.state, varName)
}

// storeCollected stores collected loop results in a variable.
func (le *LoopExecutor) storeCollected(varName string, collected []interface{}) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	le.executor.state.Variables[varName] = collected
}
