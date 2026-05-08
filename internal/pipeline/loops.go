// Package pipeline provides workflow execution for AI agent orchestration.
// loops.go implements loop constructs for workflow steps: for-each, while, and times loops.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	maxIterations, err := le.resolveMaxIterations(step, loop)
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

		scope := le.pushLoopVars(varName, item, i, total)

		// Execute nested steps
		iterResult, shouldBreak, shouldContinue, completedAllSteps := le.executeIteration(ctx, step, loop, workflow, i)
		le.popLoopVars(scope)
		le.executor.clearStepInFlight(loopIterationID(step.ID, i))

		result.Results = append(result.Results, iterResult...)
		result.Iterations++
		if shouldCompleteForeachIteration(ctx, iterResult, shouldBreak, completedAllSteps) {
			le.executor.markForeachIterationCompleted(step.ID, i, total)
		}
		le.executor.persistState()

		// Collect output if configured. bd-t3q8a: persist each iteration's
		// collected entry on the foreach state so a subsequent resume can
		// reconstruct outputs from iterations that completed before the
		// interruption — without this, a mid-loop resume would silently
		// drop pre-resume entries when storeCollected runs at the end.
		if loop.Collect != "" && len(iterResult) > 0 {
			lastResult := iterResult[len(iterResult)-1]
			var collectedValue interface{}
			if lastResult.ParsedData != nil {
				collectedValue = lastResult.ParsedData
			} else if lastResult.Output != "" {
				collectedValue = lastResult.Output
			}
			if collectedValue != nil {
				result.Collected = append(result.Collected, collectedValue)
				le.executor.appendForeachCollectedOutput(step.ID, collectedValue)
				le.executor.persistState()
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
	maxIterations, err := le.resolveMaxIterations(step, loop)
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

	// Iterate while condition is true
	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		// Evaluate while condition. bd-ypo73: ctx-aware so a `loop.while`
		// expression nested inside a foreach max_rounds body sees the
		// round overlay from withRoundOverrides.
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

		// EvaluateCondition returns true if step should be SKIPPED (condition is false)
		if shouldSkip {
			// Condition is false, exit loop
			break
		}

		scope := le.pushLoopVars(varName, i, i, maxIterations)

		// Execute nested steps. while/times do not persist iteration
		// completion records, so the bd-vq8bc completedAllSteps signal is
		// not consulted here.
		iterResult, shouldBreak, shouldContinue, _ := le.executeIteration(ctx, step, loop, workflow, i)
		le.popLoopVars(scope)

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
				return result
			case <-time.After(loop.Delay.Duration):
			}
		}
	}

	// Check if we hit max iterations without condition becoming false
	if result.Iterations >= maxIterations {
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
	maxIterations, err := le.resolveMaxIterations(step, loop)
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

	// Iterate N times
	for i := 0; i < times; i++ {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		scope := le.pushLoopVars(varName, i, i, times)

		// Execute nested steps. while/times do not persist iteration
		// completion records, so the bd-vq8bc completedAllSteps signal is
		// not consulted here.
		iterResult, shouldBreak, shouldContinue, _ := le.executeIteration(ctx, step, loop, workflow, i)
		le.popLoopVars(scope)

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

func (le *LoopExecutor) resolveMaxIterations(step *Step, loop *LoopConfig) (int, error) {
	if loop == nil {
		return DefaultMaxIterations, nil
	}
	return le.resolveIntOrExpr(step, "loop.max_iterations", &loop.MaxIterations, DefaultMaxIterations)
}

// resolveIntOrExpr resolves an IntOrExpr field. The fallback is only used when
// the field is absent (nil or zero-valued) — an explicit literal or expression
// that fails to resolve to a positive integer returns an error so the loop
// step can fail closed. Silently substituting the default would let a typo in
// a safety cap (e.g. ${defaults.unknown}) raise an intended lower bound and
// run substantially more iterations than the author configured.
func (le *LoopExecutor) resolveIntOrExpr(step *Step, field string, value *IntOrExpr, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if value.Expr == "" {
		if value.Value > 0 {
			return value.Value, nil
		}
		// Field absent (zero value): use the default safety cap.
		value.Value = fallback
		return fallback, nil
	}

	resolved, err := le.substituteIntExpr(value.Expr)
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
					FieldSubstitutionResolved, value.Expr,
					"requested", parsed,
					"cap", fallback,
					"hint", "set max_iterations to a literal integer to opt out of the cap",
				)
				parsed = fallback
			}
			value.Value = parsed
			return parsed, nil
		}
	}

	le.executor.stepLogger(step).Warn(EventSubstWarn,
		FieldSubstitutionKey, field,
		FieldSubstitutionResolved, value.Expr,
		"error", err,
	)
	return 0, fmt.Errorf("resolve %s expression %q: %w", field, value.Expr, err)
}

func (le *LoopExecutor) substituteIntExpr(expr string) (string, error) {
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
	return sub.SubstituteStrict(le.executor.substituteRuntimeVariables(expr))
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
				// Surface a failed control-condition like other failures: log
				// and stop the iteration. Mirrors foreach behaviour for
				// failed when-condition evaluation on a control-only step.
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
				return results, true, false, false
			case ErrorActionContinue:
				// Continue with next step in iteration
			}
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

	// Check if it's a direct array in Variables
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

// pushLoopVars sets loop context variables and returns the scope needed to
// restore the previous outer loop context.
func (le *LoopExecutor) pushLoopVars(varName string, item interface{}, index, total int) VariableScope {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	scope := CaptureVariableScope(le.executor.state.Variables, loopScopeKeys(varName)...)
	SetLoopVars(le.executor.state, varName, item, index, total)
	le.executor.pushScopeFrameLocked(ScopeFrame{
		Kind: StepKindLoop,
		Name: varName,
		Variables: map[string]interface{}{
			"loop." + varName: item,
			"loop.item":       item,
			"loop.index":      index,
			"loop.count":      total,
			"loop.first":      index == 0,
			"loop.last":       index == total-1,
		},
	})
	return scope
}

// popLoopVars restores the previous loop context.
func (le *LoopExecutor) popLoopVars(scope VariableScope) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	scope.Restore(le.executor.state.Variables)
	le.executor.popScopeFrameLocked()
}

// storeCollected stores collected loop results in a variable.
func (le *LoopExecutor) storeCollected(varName string, collected []interface{}) {
	le.executor.varMu.Lock()
	defer le.executor.varMu.Unlock()
	le.executor.state.Variables[varName] = collected
}
