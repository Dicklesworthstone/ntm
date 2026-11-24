// Package pipeline provides workflow execution for AI agent orchestration.
// loops.go implements loop constructs for workflow steps: for-each, while, and times loops.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoopExecutor handles execution of loop constructs within workflows.
type LoopExecutor struct {
	executor  *Executor
	resolveMu sync.Mutex
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

	if restored, ok := le.restoreLoopBreak(step); ok {
		return restored
	}

	// Determine loop type and execute
	// Priority: items > while > until > times (times: 0 is valid for immediate completion)
	switch {
	case loop.Items != "":
		return le.executeForEach(ctx, step, loop, workflow)
	case loop.While != "":
		return le.executeWhile(ctx, step, loop, workflow)
	case loop.Until != "":
		return le.executeUntil(ctx, step, loop, workflow)
	default:
		// Default to times loop (Times: 0 means zero iterations = immediate completion)
		return le.executeTimes(ctx, step, loop, workflow)
	}
}

func loopCollectedValue(loop *LoopConfig, results []StepResult) interface{} {
	if loop.Collect == "" || len(results) == 0 {
		return nil
	}
	last := results[len(results)-1]
	if last.Status == StatusFailed || last.Status == StatusCancelled {
		return nil
	}
	if last.ParsedData != nil {
		return last.ParsedData
	}
	if last.Output != "" {
		return last.Output
	}
	return nil
}

// Publish the terminal control decision and its collected value in one state
// transition. The outer scheduler may not yet have checkpointed its result.
func (le *LoopExecutor) finishLoopBreak(step *Step, result LoopResult, iteration int, children []StepResult) LoopResult {
	e := le.executor
	e.stateMu.Lock()
	progress := e.state.ForeachState[step.ID]
	digest, err := loopFailureParentHash(nil, e.state, step)
	if err != nil {
		e.stateMu.Unlock()
		return loopBreakError(step, err)
	}
	progress.Break = &LoopBreakCheckpoint{Iteration: iteration, DefinitionHash: digest, PriorOutputs: len(progress.CollectedOutputs)}
	if value := loopCollectedValue(step.Loop, children); value != nil {
		progress.CollectedOutputs = append(progress.CollectedOutputs, value)
		result.Collected = append(result.Collected, value)
	}
	progress.UpdatedAt = time.Now()
	e.state.ForeachState[step.ID] = progress
	delete(e.state.InFlightSteps, loopIterationID(step.ID, iteration))
	e.stateMu.Unlock()
	if err := e.persistState(); err != nil {
		return loopBreakError(step, err)
	}
	result.Iterations = iteration + 1
	return le.completedLoopBreak(step, result)
}

func (le *LoopExecutor) restoreLoopBreak(step *Step) (LoopResult, bool) {
	e := le.executor
	e.stateMu.RLock()
	if e.state == nil {
		e.stateMu.RUnlock()
		return LoopResult{}, false
	}
	progress := e.state.ForeachState[step.ID]
	marker := progress.Break
	if marker == nil {
		e.stateMu.RUnlock()
		return LoopResult{}, false
	}
	digest, err := loopFailureParentHash(nil, e.state, step)
	if err == nil && (digest != marker.DefinitionHash || progress.StepID != step.ID ||
		marker.Iteration < 0 || marker.Iteration >= progress.Total || marker.Iteration != progress.CurrentIteration ||
		firstIncompleteIteration(progress.CompletedIterationIDs, step.ID, progress.Total) != marker.Iteration ||
		marker.PriorOutputs < 0 || marker.PriorOutputs > len(progress.CollectedOutputs) || len(progress.CollectedOutputs)-marker.PriorOutputs > 1) {
		err = fmt.Errorf("saved loop break differs from its definition or iteration checkpoint")
	}
	result := LoopResult{Iterations: marker.Iteration + 1, Collected: append([]interface{}{}, progress.CollectedOutputs...)}
	e.stateMu.RUnlock()
	if err != nil {
		return loopBreakError(step, err), true
	}
	return le.completedLoopBreak(step, result), true
}

func loopBreakError(step *Step, err error) LoopResult {
	return LoopResult{Status: StatusFailed, FinishedAt: time.Now(), Error: stepRuntimeError(step,
		StepKindLoop, "recovery", err.Error(), "inspect the terminal loop checkpoint before resetting", "")}
}

func (le *LoopExecutor) completedLoopBreak(step *Step, result LoopResult) LoopResult {
	if step.Loop.Collect != "" {
		le.storeCollected(step.Loop.Collect, result.Collected)
	}
	result.Status, result.BreakReason, result.FinishedAt = StatusCompleted, "break statement", time.Now()
	le.executor.emitProgress("loop_complete", step.ID, fmt.Sprintf("Loop completed: %d iterations (break)", result.Iterations), le.executor.calculateProgress())
	return result
}

// executeForEach implements for-each loop iteration over an array.
func (le *LoopExecutor) executeForEach(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow) LoopResult {
	result := LoopResult{
		Status:    StatusRunning,
		Results:   make([]StepResult, 0),
		Collected: make([]interface{}, 0),
	}

	// Resolve items expression to get the array. bd-lwb25: pass ctx so a
	// loop nested inside a foreach max_rounds body can resolve ${round}
	// in its items expression (e.g. `${vars.batches_${round}}`).
	items, err := le.resolveItems(ctx, loop.Items)
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
	maxIterations, err := le.resolveMaxIterations(ctx, step, loop)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   err.Error(),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
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

	// bd-3awat: CompletedIterationIDs are keyed by integer index. If items
	// resolve from a dynamic source (vars expression, prior step output,
	// glob, etc.) and the resolved list differs between the original run
	// and a resume, applying old completion records to different items at
	// the same index would silently produce wrong outputs. Fingerprint the
	// resolved items at start and refuse resume on drift; record the
	// fingerprint on first run and on a clean (no-op) resume so subsequent
	// resumes of legacy state files are also protected.
	itemsFingerprint := computeForeachItemsFingerprint(items)
	if err := le.executor.verifyForeachItemsFingerprint(step.ID, itemsFingerprint); err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   err.Error(),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}
	le.executor.recordForeachItemsFingerprint(step.ID, itemsFingerprint)

	startIndex := le.executor.beginForeachState(step.ID, total)
	le.executor.persistState()

	// bd-t3q8a: a mid-loop resume starts at the first incomplete iteration
	// with a fresh result.Collected. Without restoring outputs from
	// iterations completed in the prior run, storeCollected at the end of
	// the loop would silently overwrite the collected variable with only
	// the resumed iterations. Prepopulate from persisted state so the
	// final store sees every iteration's contribution.
	if loop.Collect != "" {
		if prior := le.executor.loadForeachCollectedOutputs(step.ID); len(prior) > 0 {
			result.Collected = append(result.Collected, prior...)
		}
	}

	// Iterate over items
	for i := startIndex; i < len(items); i++ {
		item := items[i]
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		le.executor.markForeachIterationStarted(step.ID, i, total)
		le.executor.markStepInFlight(loopIterationID(step.ID, i), StepKindLoop, i)
		le.executor.persistState()

		iterCtx := withLoopVars(ctx, varName, item, i, total)

		// Execute nested steps
		iterResult, shouldBreak, shouldContinue, completedAllSteps := le.executeIteration(iterCtx, step, loop, workflow, i)
		le.executor.clearStepInFlight(loopIterationID(step.ID, i))

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		if iterationHasCancelledStep(iterResult) || (ctx.Err() != nil && !completedAllSteps) {
			le.executor.persistState()
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		}

		if shouldBreak && (len(iterResult) == 0 || iterResult[len(iterResult)-1].Status != StatusFailed) {
			return le.finishLoopBreak(step, result, i, iterResult)
		}

		// Commit the collected entry with completion so every concurrent
		// checkpoint can reconstruct the output of iterations safe to skip.
		collectedValue := loopCollectedValue(loop, iterResult)
		if collectedValue != nil {
			result.Collected = append(result.Collected, collectedValue)
		}
		if shouldCompleteForeachIteration(ctx, iterResult, shouldBreak, completedAllSteps) {
			if collectedValue != nil {
				le.executor.markForeachIterationCompleted(step.ID, i, total, collectedValue)
			} else {
				le.executor.markForeachIterationCompleted(step.ID, i, total)
			}
		} else if collectedValue != nil {
			le.executor.appendForeachCollectedOutput(step.ID, collectedValue)
		}
		le.executor.persistState()

		if shouldBreak {
			result.Error = iterResult[len(iterResult)-1].Error
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
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Store collected results if configured
	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

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
	maxIterations, err := le.resolveMaxIterations(ctx, step, loop)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   err.Error(),
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
		fmt.Sprintf("Starting while loop (max %d iterations)", maxIterations),
		le.executor.calculateProgress())

	startIndex := le.executor.beginForeachState(step.ID, maxIterations)
	le.executor.persistState()
	if loop.Collect != "" {
		if prior := le.executor.loadForeachCollectedOutputs(step.ID); len(prior) > 0 {
			result.Collected = append(result.Collected, prior...)
		}
	}

	// Iterate while condition is true
	brokeEarly := false
	for i := startIndex; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		// A validated consumed failure proves this incomplete iteration
		// already passed its entry condition. Finish its remaining body
		// before evaluating variables that earlier children may have changed.
		resumeBody, err := le.executor.resumeConsumedLoopIteration(step, workflow, i)
		if err != nil {
			result.Status, result.FinishedAt = StatusFailed, time.Now()
			result.Error = stepRuntimeError(step, StepKindLoop, "recovery", err.Error(),
				"restore the consumed iteration's definition before resuming", "")
			return result
		}
		if !resumeBody {
			// Preserve the round overlay for loops nested in max_rounds.
			shouldSkip, err := le.executor.evaluateConditionCtx(ctx, loop.While)
			if err != nil {
				result.Status = StatusFailed
				result.Error = &StepError{
					Type:      "loop",
					Message:   fmt.Sprintf("failed to evaluate while condition: %v", err),
					Timestamp: time.Now(),
				}
				result.FinishedAt = time.Now()
				return result
			}
			if shouldSkip {
				brokeEarly = true
				break
			}
		}

		le.executor.markForeachIterationStarted(step.ID, i, maxIterations)
		le.executor.markStepInFlight(loopIterationID(step.ID, i), StepKindLoop, i)
		le.executor.persistState()

		iterCtx := withLoopVars(ctx, varName, i, i, maxIterations)

		iterResult, shouldBreak, shouldContinue, completedAllSteps := le.executeIteration(iterCtx, step, loop, workflow, i)
		le.executor.clearStepInFlight(loopIterationID(step.ID, i))

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		if iterationHasCancelledStep(iterResult) || (ctx.Err() != nil && !completedAllSteps) {
			le.executor.persistState()
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		}

		if shouldBreak && (len(iterResult) == 0 || iterResult[len(iterResult)-1].Status != StatusFailed) {
			return le.finishLoopBreak(step, result, i, iterResult)
		}

		// Collect output before publishing the iteration checkpoint.
		collectedValue := loopCollectedValue(loop, iterResult)
		if collectedValue != nil {
			result.Collected = append(result.Collected, collectedValue)
		}
		if shouldCompleteForeachIteration(ctx, iterResult, shouldBreak, completedAllSteps) {
			if collectedValue != nil {
				le.executor.markForeachIterationCompleted(step.ID, i, maxIterations, collectedValue)
			} else {
				le.executor.markForeachIterationCompleted(step.ID, i, maxIterations)
			}
		} else if collectedValue != nil {
			le.executor.appendForeachCollectedOutput(step.ID, collectedValue)
		}
		le.executor.persistState()

		if shouldBreak {
			brokeEarly = true
			result.Error = iterResult[len(iterResult)-1].Error
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
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Check if we hit max iterations without condition becoming false. Include
	// startIndex so resumed while loops enforce the same total iteration cap.
	if !brokeEarly && startIndex+result.Iterations >= maxIterations {
		// Evaluate condition one more time to see if it's still true.
		// bd-ypo73: ctx-aware for the same max_rounds overlay reasoning.
		shouldSkip, _ := le.executor.evaluateConditionCtx(ctx, loop.While)
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

// executeUntil executes loop body steps repeatedly until the `loop.Until` shell
// predicate exits 0 (post-condition loop bounded by max_iterations), persisting
// per-iteration resume checkpoints so --mode=continue skips already-completed
// iterations cleanly.
func (le *LoopExecutor) executeUntil(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow) LoopResult {
	result := LoopResult{
		Status:    StatusRunning,
		Results:   make([]StepResult, 0),
		Collected: make([]interface{}, 0),
	}

	maxIterations, err := le.resolveMaxIterations(ctx, step, loop)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   err.Error(),
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
		fmt.Sprintf("Starting until loop (max %d iterations)", maxIterations),
		le.executor.calculateProgress())

	startIndex := le.executor.beginForeachState(step.ID, maxIterations)
	le.executor.persistState()
	if loop.Collect != "" {
		if prior := le.executor.loadForeachCollectedOutputs(step.ID); len(prior) > 0 {
			result.Collected = append(result.Collected, prior...)
		}
	}

	conditionMet := false
	if startIndex > 0 {
		// The last iteration checkpoint can reach disk before the parent loop
		// result. Its predicate receipt is the durable termination decision;
		// starting another body here would repeat effects after a true predicate.
		untilStep := untilPredicateStep(step, loop, startIndex-1)
		var prior StepResult
		var ok bool
		if le.executor.config.DryRun {
			priorCtx := withLoopVars(ctx, varName, startIndex-1, startIndex-1, maxIterations)
			prior, ok = le.executor.executeCommand(priorCtx, &untilStep, workflow), true
		} else {
			prior, ok = le.loadUntilPredicateResult(&untilStep)
		}
		if !ok {
			result.Error = stepRuntimeError(&untilStep, StepKindCommand, "recovery",
				"completed until iteration has no predicate receipt",
				"inspect the saved iteration before explicitly resetting its replay scope", "")
		} else if prior.Status == StatusCompleted && prior.Error == nil {
			conditionMet = true
		} else if !untilPredicateExitedFalse(prior) {
			result.Error = untilPredicateFailure(&untilStep, prior)
		}
	}
	for i := startIndex; !conditionMet && result.Error == nil && i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		untilStep := untilPredicateStep(step, loop, i)
		untilRes, restored := le.loadUntilPredicateResult(&untilStep)
		if le.executor.config.DryRun {
			restored = false
		}
		if restored && (untilRes.Status != StatusCompleted || untilRes.Error != nil) && !untilPredicateExitedFalse(untilRes) {
			result.Error = untilPredicateFailure(&untilStep, untilRes)
			break
		}

		le.executor.markForeachIterationStarted(step.ID, i, maxIterations)
		le.executor.markStepInFlight(loopIterationID(step.ID, i), StepKindLoop, i)
		le.executor.persistState()

		iterCtx := withLoopVars(ctx, varName, i, i, maxIterations)

		var iterResult []StepResult
		var shouldBreak, shouldContinue, completedAllSteps bool
		if restored {
			// The predicate could only launch after the body returned. Restore
			// that durable body checkpoint, including continued failures, rather
			// than letting executeStep retry commands whose effects already ran.
			iterResult, result.Error = le.restoreUntilIteration(step, loop, workflow, i)
			if result.Error != nil {
				le.executor.clearStepInFlight(loopIterationID(step.ID, i))
				break
			}
			completedAllSteps = true
		} else {
			iterResult, shouldBreak, shouldContinue, completedAllSteps = le.executeIteration(iterCtx, step, loop, workflow, i)
		}
		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		if iterationHasCancelledStep(iterResult) || (ctx.Err() != nil && !completedAllSteps) {
			le.executor.clearStepInFlight(loopIterationID(step.ID, i))
			le.executor.persistState()
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		}

		if shouldBreak && (len(iterResult) == 0 || iterResult[len(iterResult)-1].Status != StatusFailed) {
			return le.finishLoopBreak(step, result, i, iterResult)
		}

		if shouldBreak {
			le.executor.clearStepInFlight(loopIterationID(step.ID, i))
			result.Error = iterResult[len(iterResult)-1].Error
			le.executor.persistState()
			break
		}

		// Evaluate the supervised `until` shell predicate with loop variables
		// still in scope so predicates can reference ${loop.index}/${item}.
		if !restored {
			untilRes = le.executor.executeCommand(iterCtx, &untilStep, workflow)
		}
		le.executor.clearStepInFlight(loopIterationID(step.ID, i))

		if untilRes.Status == StatusCancelled || ctx.Err() != nil {
			le.executor.persistState()
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		}

		// A failed supervisor, timeout, interrupted process, or invalid receipt
		// does not mean the shell predicate evaluated false. Only a proven
		// ordinary nonzero exit permits another iteration.
		if (untilRes.Status != StatusCompleted || untilRes.Error != nil) && !untilPredicateExitedFalse(untilRes) {
			result.Error = untilPredicateFailure(&untilStep, untilRes)
			le.executor.persistState()
			break
		}

		collectedValue := loopCollectedValue(loop, iterResult)
		if collectedValue != nil {
			result.Collected = append(result.Collected, collectedValue)
		}
		// Reaching a verified predicate means the body was consumed, including
		// failures explicitly allowed by on_error=continue. Publish completion
		// and its output under one lock so a concurrent checkpoint cannot
		// retain a completed iteration while losing its collected value.
		if ctx.Err() == nil || completedAllSteps {
			if collectedValue != nil {
				le.executor.markForeachIterationCompleted(step.ID, i, maxIterations, collectedValue)
			} else {
				le.executor.markForeachIterationCompleted(step.ID, i, maxIterations)
			}
		} else if collectedValue != nil {
			le.executor.appendForeachCollectedOutput(step.ID, collectedValue)
		}
		le.executor.persistState()

		if untilRes.Status == StatusCompleted {
			conditionMet = true
			break
		}

		if shouldContinue {
			continue
		}

		if loop.Delay.Duration > 0 && i < maxIterations-1 {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	if !conditionMet && result.Error == nil && result.BreakReason == "" {
		result.Error = &StepError{
			Type:      "loop",
			Message:   fmt.Sprintf("until loop reached max_iterations limit (%d) without predicate exiting 0", maxIterations),
			Timestamp: time.Now(),
		}
	}

	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

	if result.Error != nil {
		result.Status = StatusFailed
	} else {
		result.Status = StatusCompleted
	}
	result.FinishedAt = time.Now()

	le.executor.emitProgress("loop_complete", step.ID,
		fmt.Sprintf("Until loop completed: %d iterations", result.Iterations),
		le.executor.calculateProgress())

	return result
}

func untilPredicateStep(parent *Step, loop *LoopConfig, iteration int) Step {
	return Step{
		ID:      fmt.Sprintf("%s_iter%d_until", parent.ID, iteration),
		Command: loop.Until,
		Timeout: parent.Timeout,
		OnError: ErrorActionContinue,
	}
}

// A settled predicate proves that its body returned, but does not replace the
// body's output and dispatch evidence. Missing or ambiguous body checkpoints
// stop recovery; they never authorize replay after a terminal predicate.
func (le *LoopExecutor) restoreUntilIteration(parent *Step, loop *LoopConfig, workflow *Workflow, iteration int) ([]StepResult, *StepError) {
	results := make([]StepResult, 0, len(loop.Steps))
	for _, child := range loop.Steps {
		child.ID = fmt.Sprintf("%s_iter%d_%s", parent.ID, iteration, child.ID)
		refuse := func(reason string) ([]StepResult, *StepError) {
			return nil, stepRuntimeError(&child, StepKindLoop, "recovery", reason,
				"inspect the completed predicate and body checkpoint before explicitly resetting the iteration", "")
		}
		if foreachControlOnlyStep(child) {
			return refuse("cannot reconstruct an until body control decision from its predicate receipt")
		}
		le.executor.stateMu.RLock()
		prior, ok := le.executor.state.Steps[child.ID]
		_, inFlight := le.executor.state.InFlightSteps[child.ID]
		prior = cloneSuccessHookResult(prior)
		le.executor.stateMu.RUnlock()
		if !ok || prior.StepID != child.ID || inFlight || prior.RerunOnResume {
			return refuse("settled until predicate has no final body checkpoint")
		}
		switch prior.Status {
		case StatusCompleted, StatusSkipped:
		case StatusFailed:
			if resolveErrorAction(child.OnError, workflow.Settings.OnError) != ErrorActionContinue {
				return refuse("until predicate followed a body failure that does not allow continuation")
			}
		default:
			return refuse("settled until predicate has an unfinished body checkpoint")
		}
		if stepKind(&child) == StepKindCommand && prior.Status != StatusSkipped {
			record, ok := le.executor.loadCommandExecution(child.ID)
			if !ok {
				return refuse("until body command has lost its dispatch receipt")
			}
			if err := validateCommandExecution(child.ID, record); err != nil {
				return refuse(err.Error())
			}
			hash, err := commandDefinitionHash(&child)
			if err != nil || record.StepHash != hash || record.Status != commandExecutionSettled {
				return refuse("until body command receipt does not prove settlement of the saved definition")
			}
		}
		results = append(results, prior)
	}
	return results, nil
}

// loadUntilPredicateResult consumes settled predicate receipts without invoking
// executeCommand: that method may retry a failed command, but a predicate's
// nonzero exit is a completed decision whose effects must not be repeated.
func (le *LoopExecutor) loadUntilPredicateResult(step *Step) (StepResult, bool) {
	record, ok := le.executor.loadCommandExecution(step.ID)
	if !ok {
		return StepResult{}, false
	}
	reject := func(typ, reason string) (StepResult, bool) {
		return StepResult{
			StepID: step.ID, Status: StatusFailed, AgentType: "command",
			Error: stepRuntimeError(step, StepKindCommand, typ, reason,
				"inspect the saved predicate outcome before explicitly resetting its replay scope", ""),
			FinishedAt: time.Now(),
		}, true
	}
	if err := validateCommandExecution(step.ID, record); err != nil {
		return reject("recovery", err.Error())
	}
	stepHash, err := commandDefinitionHash(step)
	if err != nil {
		return reject("recovery", err.Error())
	}
	if record.StepHash != stepHash {
		return reject("recovery", "saved until predicate belongs to a different step definition")
	}
	if record.Status != commandExecutionSettled {
		return reject("execution_unknown", uncertainCommandExecutionError(step.ID, record.ProcessID).Error())
	}
	if record.Background || record.Result.RerunOnResume {
		return reject("recovery", "until predicate receipt does not prove a final foreground outcome")
	}
	result := record.Result
	if record.ResolutionAction != "" {
		le.executor.stateMu.RLock()
		_, err := commandExecutionRecoveryEntry(le.executor.state, record)
		le.executor.stateMu.RUnlock()
		if err != nil {
			return reject("recovery", err.Error())
		}
		if record.ResolutionExitCode != 0 {
			result.Error = commandExitStepError(step, record.ResolutionExitCode)
		}
	}
	return result, true
}

func untilPredicateExitedFalse(result StepResult) bool {
	if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "exit" {
		return false
	}
	for _, field := range strings.Fields(result.Error.Details) {
		if value, ok := strings.CutPrefix(field, "reason=exit_code="); ok {
			exitCode, err := strconv.Atoi(value)
			// ExitCode is -1 when a process was killed by a signal. That is an
			// interrupted predicate, not evidence that its condition was false.
			return err == nil && exitCode > 0 && exitCode <= 255
		}
	}
	return false
}

func untilPredicateFailure(step *Step, result StepResult) *StepError {
	if result.Error != nil {
		return result.Error
	}
	return stepRuntimeError(step, StepKindCommand, "recovery",
		fmt.Sprintf("until predicate returned %q without a verified exit outcome", result.Status),
		"inspect the saved predicate outcome before explicitly resetting its replay scope", "")
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
	maxIterations, err := le.resolveMaxIterations(ctx, step, loop)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "loop",
			Message:   err.Error(),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
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

	startIndex := le.executor.beginForeachState(step.ID, times)
	le.executor.persistState()
	if loop.Collect != "" {
		if prior := le.executor.loadForeachCollectedOutputs(step.ID); len(prior) > 0 {
			result.Collected = append(result.Collected, prior...)
		}
	}

	// Iterate N times
	for i := startIndex; i < times; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		le.executor.markForeachIterationStarted(step.ID, i, times)
		le.executor.markStepInFlight(loopIterationID(step.ID, i), StepKindLoop, i)
		le.executor.persistState()

		iterCtx := withLoopVars(ctx, varName, i, i, times)

		iterResult, shouldBreak, shouldContinue, completedAllSteps := le.executeIteration(iterCtx, step, loop, workflow, i)
		le.executor.clearStepInFlight(loopIterationID(step.ID, i))

		result.Results = append(result.Results, iterResult...)
		result.Iterations++

		if iterationHasCancelledStep(iterResult) || (ctx.Err() != nil && !completedAllSteps) {
			le.executor.persistState()
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		}

		if shouldBreak && (len(iterResult) == 0 || iterResult[len(iterResult)-1].Status != StatusFailed) {
			return le.finishLoopBreak(step, result, i, iterResult)
		}

		// Collect output before publishing the iteration checkpoint.
		collectedValue := loopCollectedValue(loop, iterResult)
		if collectedValue != nil {
			result.Collected = append(result.Collected, collectedValue)
		}
		if shouldCompleteForeachIteration(ctx, iterResult, shouldBreak, completedAllSteps) {
			if collectedValue != nil {
				le.executor.markForeachIterationCompleted(step.ID, i, times, collectedValue)
			} else {
				le.executor.markForeachIterationCompleted(step.ID, i, times)
			}
		} else if collectedValue != nil {
			le.executor.appendForeachCollectedOutput(step.ID, collectedValue)
		}
		le.executor.persistState()

		if shouldBreak {
			result.Error = iterResult[len(iterResult)-1].Error
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
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Store collected results if configured
	if loop.Collect != "" {
		le.storeCollected(loop.Collect, result.Collected)
	}

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

func (le *LoopExecutor) resolveMaxIterations(ctx context.Context, step *Step, loop *LoopConfig) (int, error) {
	if loop == nil {
		return DefaultMaxIterations, nil
	}
	maximum, err := le.resolveIntOrExpr(ctx, step, "loop.max_iterations", &loop.MaxIterations, DefaultMaxIterations)
	if err != nil {
		return 0, err
	}
	le.executor.stateMu.RLock()
	defer le.executor.stateMu.RUnlock()
	if le.executor.state != nil {
		progress := le.executor.state.ForeachState[step.ID]
		for id := range progress.ConsumedFailures {
			iteration := iterationIndexFromID(id, step.ID+"_iter")
			if iteration >= maximum && !stringSliceContains(progress.CompletedIterationIDs, loopIterationID(step.ID, iteration)) {
				return 0, loopFailureRecoveryError("loop %q has unfinished consumed iteration %d outside resolved max_iterations %d; restore the prior bound or explicitly reset its iteration", step.ID, iteration, maximum)
			}
		}
	}
	return maximum, nil
}

// resolveIntOrExpr resolves an IntOrExpr field. The fallback is only used when
// the field is absent (nil or zero-valued) — an explicit literal or expression
// that fails to resolve to a positive integer returns an error so the loop
// step can fail closed. Silently substituting the default would let a typo in
// a safety cap (e.g. ${defaults.unknown}) raise an intended lower bound and
// run substantially more iterations than the author configured.
func (le *LoopExecutor) resolveIntOrExpr(ctx context.Context, step *Step, field string, value *IntOrExpr, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	le.resolveMu.Lock()
	expr := value.Expr
	literal := value.Value
	if expr == "" {
		if literal > 0 {
			le.resolveMu.Unlock()
			return literal, nil
		}
		// Field absent (zero value): record and return default safety cap.
		value.Value = fallback
		le.resolveMu.Unlock()
		return fallback, nil
	}
	le.resolveMu.Unlock()

	resolved, err := le.substituteIntExpr(ctx, expr)
	if err == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(resolved))
		if parseErr != nil {
			err = fmt.Errorf("parse %q as positive integer: %w", resolved, parseErr)
		} else if parsed <= 0 {
			err = fmt.Errorf("parse %q as positive integer: value must be greater than zero", resolved)
		} else {
			// bd-c2l8s: clamp expression-resolved values above the safety
			// fallback so a misconfigured `loop.max_iterations:
			// ${vars.from_external}` cannot drive the body unbounded
			// (parity with bd-ltghx for foreach.max_rounds). Literal values
			// are not clamped — the workflow author chose them explicitly
			// and the parser already rejects negatives.
			if parsed > fallback {
				le.executor.stepLogger(step).Warn(EventSubstWarn,
					FieldSubstitutionKey, field,
					FieldSubstitutionResolved, expr,
					"requested", parsed,
					"cap", fallback,
					"hint", "set max_iterations to a literal integer to opt out of the cap",
				)
				parsed = fallback
			}
			le.resolveMu.Lock()
			value.Value = parsed
			le.resolveMu.Unlock()
			return parsed, nil
		}
	}

	le.executor.stepLogger(step).Warn(EventSubstWarn,
		FieldSubstitutionKey, field,
		FieldSubstitutionResolved, expr,
		"error", err,
	)
	return 0, fmt.Errorf("resolve %s expression %q: %w", field, expr, err)
}

func (le *LoopExecutor) substituteIntExpr(ctx context.Context, expr string) (string, error) {
	// bd-eslpu: lock order is stateMu before varMu (matches the canonical
	// pattern established in executor.go applyStartFrom and aligned in
	// foreach_max_rounds.go resolveForeachMaxRounds via bd-8wo27). The
	// previous order (varMu→stateMu) was an AB-BA pair against any
	// goroutine using the canonical order — sync.RWMutex's writer-
	// starvation guard would block a concurrent stateMu.RLock() behind
	// a pending stateMu.Lock(), at the same time the canonical-order
	// writer was waiting on varMu we held. The bd-8wo27 review missed
	// this site because the deferred-form `defer varMu.RUnlock()` line
	// between the two RLocks fooled regex-based scans.
	le.executor.stateMu.RLock()
	defer le.executor.stateMu.RUnlock()
	le.executor.varMu.RLock()
	defer le.executor.varMu.RUnlock()

	workflowID := ""
	if le.executor.state != nil {
		workflowID = le.executor.state.WorkflowID
	}
	sub := NewSubstitutor(le.executor.state, le.executor.config.Session, workflowID)
	sub.SetDefaults(le.executor.defaults)
	sub.SetMaxDepth(le.executor.limits.MaxSubstitutionDepth)
	sub.SetLocalOverrides(roundOverridesFromCtx(ctx))
	return sub.SubstituteStrict(le.executor.substituteRuntimeVariables(expr))
}

func iterationHasCancelledStep(results []StepResult) bool {
	for _, r := range results {
		if r.Status == StatusCancelled {
			return true
		}
	}
	return false
}

// executeIteration executes a single loop iteration (all nested steps).
// Returns the step results, whether to break, whether to continue, and
// whether every nested step in loop.Steps was actually processed.
//
// bd-vq8bc: the completedAllSteps signal lets shouldCompleteForeachIteration
// distinguish a fully-executed iteration that observed late ctx cancellation
// from a partial iteration cancelled mid-body. Without it, an iteration
// whose first body step succeeded before ctx.Done() fires would be marked
// complete and resume would silently skip the remaining body steps.
func (le *LoopExecutor) executeIteration(ctx context.Context, step *Step, loop *LoopConfig, workflow *Workflow, iterIndex int) ([]StepResult, bool, bool, bool) {
	if err := le.executor.admitRuntimeBatch(ctx, func(yield func(string) error) error {
		for _, child := range loop.Steps {
			if err := yield(fmt.Sprintf("%s_iter%d_%s", step.ID, iterIndex, child.ID)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if ctx.Err() != nil && err == ctx.Err() {
			return nil, false, false, false
		}
		// One container failure propagates through the existing loop result
		// contract. No refused child gets a result or an in-flight marker.
		failed := stepAdmissionExecutionFailure(StepResult{StepID: step.ID, StartedAt: time.Now()}, err)
		return []StepResult{failed}, true, false, false
	}
	results := make([]StepResult, 0, len(loop.Steps))

	for _, nestedStep := range loop.Steps {
		select {
		case <-ctx.Done():
			// Mid-iteration cancellation: more body steps remain to run.
			return results, false, false, false
		default:
		}

		// Pure-control-only steps (loop_control + optional when, no body)
		// apply their directive without dispatching a body. Steps that
		// carry real work (command/template/prompt/etc.) execute the body
		// first and only apply the directive after a successful run, so
		// loop_control no longer silently masks intended side effects
		// (matches the foreach contract from bd-9yuk0 / bd-1iabq).
		if foreachControlOnlyStep(nestedStep) {
			control, applies, condErr := loopControlAppliesForStep(ctx, nestedStep, le.executor)
			if condErr != nil {
				// Record a failed step result so the enclosing loop reports
				// StatusFailed with the condition error instead of succeeding.
				now := time.Now()
				results = append(results, StepResult{
					StepID:     fmt.Sprintf("%s_iter%d_%s", step.ID, iterIndex, nestedStep.ID),
					Status:     StatusFailed,
					StartedAt:  now,
					FinishedAt: now,
					Error: &StepError{
						Type:      "condition",
						Message:   fmt.Sprintf("failed to evaluate loop_control when condition: %v", condErr),
						Timestamp: now,
					},
				})
				return results, true, false, false
			}
			if applies {
				switch control {
				case LoopControlBreak:
					return results, true, false, false
				case LoopControlContinue:
					return results, false, true, false
				}
			}
			continue
		}

		// Keep the definition used for recovery immutable: nested containers
		// can materialize their own loop bounds while executing.
		iteratedStep := cloneStep(nestedStep)
		iteratedStep.ID = fmt.Sprintf("%s_iter%d_%s", step.ID, iterIndex, nestedStep.ID)

		result, consumed, err := le.executor.loadConsumedLoopFailure(step, &iteratedStep, workflow)
		if err != nil {
			now := time.Now()
			results = append(results, StepResult{
				StepID: step.ID, Status: StatusFailed, StartedAt: now, FinishedAt: now,
				Error: stepRuntimeError(step, StepKindLoop, "recovery", err.Error(),
					"restore the consumed failure checkpoint or explicitly reset its iteration", ""),
			})
			return results, true, false, false
		}
		if !consumed {
			executionStep := cloneStep(iteratedStep)
			result = le.executor.executeStep(ctx, &executionStep, workflow)
		}
		results = append(results, result)

		// Store result in state
		if le.executor.runtimeIDAdmitted(iteratedStep.ID) {
			le.executor.stateMu.Lock()
			le.executor.state.Steps[iteratedStep.ID] = result
			le.executor.stateMu.Unlock()
		}
		if findStepAdmissionError(result.Error) != nil {
			return results, true, false, false
		}
		if result.Status == StatusCancelled {
			return results, false, false, false
		}

		// Handle step failure based on error action
		if result.Status == StatusFailed {
			onError := resolveErrorAction(nestedStep.OnError, workflow.Settings.OnError)

			switch onError {
			case ErrorActionFail, ErrorActionFailFast, ErrorActionRetry:
				// Stop loop on failure (executeStep already exhausted retries
				// when onError is ErrorActionRetry).
				return results, true, false, false
			case ErrorActionContinue:
				if !consumed {
					result, err = le.executor.checkpointConsumedLoopFailure(ctx, step, &iteratedStep, result)
					results[len(results)-1] = result
					if err != nil {
						// Do not overwrite a durably bound child on checkpoint
						// failure; propagate a separate container failure.
						now := time.Now()
						results = append(results, StepResult{
							StepID: step.ID, Status: StatusFailed, StartedAt: now, FinishedAt: now,
							Error: stepRuntimeError(step, StepKindLoop, "recovery", err.Error(),
								"settle the child's effects and restore durable storage before resuming", ""),
						})
						return results, true, false, false
					}
				}
			}
		}

		// Like the main scheduler, the loop publishes successful named
		// outputs before the next child or predicate can read them. Foreach
		// children already published their aggregate shape; preserve it.
		if result.Status == StatusCompleted && iteratedStep.OutputVar != "" && !stepOwnsForeachOutputVar(&iteratedStep) {
			le.executor.varMu.Lock()
			storeOutputVar(le.executor.state, iteratedStep.OutputVar, result.Output, result.ParsedData)
			le.executor.varMu.Unlock()
		}

		// Apply the loop_control directive after the body has executed
		// successfully so workflows can express "do X then break" without
		// losing X.
		if result.Status == StatusCompleted {
			if control, applies := foreachLoopControlValue(nestedStep); applies {
				switch control {
				case LoopControlBreak:
					return results, true, false, false
				case LoopControlContinue:
					return results, false, true, false
				}
			}
		}
	}

	// Walked every step in loop.Steps without an early return: this
	// iteration's body fully completed (whether or not ctx was cancelled
	// after the last step finished).
	return results, false, false, true
}

// loopControlAppliesForStep evaluates a control-only step's optional when
// condition and reports whether the directive should fire. evaluateCondition
// returns "should skip" semantics (true means condition was false), so the
// control fires only when the condition resolves truthy (skip == false).
//
// bd-ypo73: takes ctx so a control-only step nested inside a foreach
// max_rounds body can resolve ${round}/${loop.round} from the round
// overlay attached by withRoundOverrides. Without it, a `loop_control`
// directive guarded by `when: "${round} > 1"` cannot resolve.
func loopControlAppliesForStep(ctx context.Context, step Step, e *Executor) (LoopControl, bool, error) {
	control, ok := foreachLoopControlValue(step)
	if !ok {
		return LoopControlNone, false, nil
	}
	if step.When == "" {
		return control, true, nil
	}
	skip, err := e.evaluateConditionCtx(ctx, step.When)
	if err != nil {
		return LoopControlNone, false, err
	}
	if skip {
		return LoopControlNone, false, nil
	}
	return control, true, nil
}

// resolveItems resolves an items expression to an array of values.
//
// bd-lwb25: ctx-aware so a `loop.items` expression nested inside a foreach
// max_rounds body sees the round overlay from withRoundOverrides. Both
// the early substituteVariables and the manual NewSubstitutor downstream
// pick up the round bindings via ctx, so `loop.items: ${vars.batches_${round}}`
// resolves per-round.
func (le *LoopExecutor) resolveItems(ctx context.Context, expr string) ([]interface{}, error) {
	// Substitute variables in the expression (ctx so round overlay applies).
	resolved := le.executor.substituteVariablesCtx(ctx, expr)

	// bd-6vp7y: hoist stateMu acquisition to the top so the order is
	// stateMu → varMu, matching the canonical convention bd-8wo27 and
	// bd-eslpu established. Pre-fix this site held varMu for the whole
	// function and briefly took stateMu mid-flight (around the
	// Substitute call), the same AB-BA pattern bd-eslpu fixed in
	// substituteIntExpr. Holding both for the function's duration is
	// slightly longer-held than before but eliminates the inverted
	// nesting; the cost is minimal because the function is short.
	le.executor.stateMu.RLock()
	defer le.executor.stateMu.RUnlock()
	le.executor.varMu.RLock()
	defer le.executor.varMu.RUnlock()

	// Try to resolve as a variable reference
	sub := NewSubstitutor(le.executor.state, le.executor.config.Session, le.executor.state.WorkflowID)
	sub.SetDefaults(le.executor.defaults)
	sub.SetMaxDepth(le.executor.limits.MaxSubstitutionDepth)
	if overrides := roundOverridesFromCtx(ctx); overrides != nil {
		sub.SetLocalOverrides(overrides)
	}

	// Strip ${ and } if present
	varPath := resolved
	if strings.HasPrefix(resolved, "${") && strings.HasSuffix(resolved, "}") {
		varPath = resolved[2 : len(resolved)-1]
	}

	// Try to look up directly in variables first
	if val, ok := le.executor.state.Variables[varPath]; ok {
		return toInterfaceSlice(val)
	}

	// Try to resolve through substitutor. May read from state.Steps;
	// stateMu is already held above.
	val, err := sub.Substitute(expr)
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

// withLoopVars binds an iteration to its own execution context. Parallel
// branches must never publish item/index values or LIFO scope frames to shared
// state: a mutex only orders writes and cannot keep a sibling's bindings out of
// the body. ForeachState and InFlightSteps retain durable iteration progress;
// returning to the parent context restores the outer bindings without writes.
func withLoopVars(ctx context.Context, varName string, item interface{}, index, total int) context.Context {
	return withRoundOverrides(ctx, map[string]interface{}{
		"loop." + varName: item,
		"loop.item":       item,
		"item":            item,
		"loop.index":      index,
		"loop.count":      total,
		"loop.first":      index == 0,
		"loop.last":       index == total-1,
	})
}

// storeCollected stores collected loop results in a variable.
func (le *LoopExecutor) storeCollected(varName string, collected []interface{}) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	le.executor.state.Variables[varName] = collected
}
