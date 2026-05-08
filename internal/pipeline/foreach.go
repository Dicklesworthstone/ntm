package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type foreachIterationPlan struct {
	Index      int
	Item       interface{}
	PaneID     string
	PaneIndex  int
	PaneVars   map[string]interface{}
	Steps      []Step
	Skipped    bool
	SkipReason string
	SkipKind   SkipKind
}

type foreachIterationResult struct {
	Index      int                    `json:"index"`
	Item       interface{}            `json:"item,omitempty"`
	Pane       map[string]interface{} `json:"pane,omitempty"`
	Results    []StepResult           `json:"results,omitempty"`
	Control    LoopControl            `json:"loop_control,omitempty"`
	Skipped    bool                   `json:"skipped,omitempty"`
	SkipReason string                 `json:"skip_reason,omitempty"`
	SkipKind   SkipKind               `json:"skip_kind,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

func (r foreachIterationResult) failed() bool {
	if r.SkipKind == SkipKindForeachBreak {
		return false
	}
	for _, result := range r.Results {
		if result.Status == StatusFailed || result.Status == StatusCancelled {
			return true
		}
	}
	return r.Error != ""
}

func (e *Executor) executeForeach(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		AgentType: "foreach",
	}
	if e.state == nil {
		return finishForeachFailure(result, "state", "execution state is not initialized")
	}

	config, kind := foreachConfigForStep(step)
	if config == nil {
		return finishForeachFailure(result, "foreach", "step has no foreach configuration")
	}

	body, err := foreachBodySteps(step, config)
	if err != nil {
		return finishForeachFailure(result, "foreach", err.Error())
	}

	items, err := e.resolveForeachItems(ctx, config, kind)
	if err != nil {
		return finishForeachFailure(result, "foreach_source", err.Error())
	}
	if len(items) > e.limits.MaxForeachIterations {
		result.Status = StatusFailed
		result.Error = foreachStructuredError(step.ID, "limit",
			fmt.Sprintf("%d iterations exceed max_foreach_iterations %d", len(items), e.limits.MaxForeachIterations),
			"raise pipeline limits.max_foreach_iterations or shrink the iteration source")
		result.SkipKind = SkipKindLimit
		result.FinishedAt = time.Now()
		return result
	}

	slog.Info("foreach step starting",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "foreach",
		"iterations", len(items),
		"parallel", config.Parallel,
	)
	e.emitProgress("foreach_start", step.ID,
		fmt.Sprintf("Starting %s with %d iterations", kind, len(items)),
		e.calculateProgress())

	plans, err := e.prepareForeachIterations(ctx, step, config, kind, body, items)
	if err != nil {
		return finishForeachFailure(result, "foreach", err.Error())
	}

	// bd-qeatk: register foreach state so iterations that completed in a
	// prior run are skipped on resume instead of re-dispatching their bodies.
	// The cursor returned by beginForeachState is advisory; the per-iteration
	// CompletedIterationIDs set drives the skip decision below.
	e.beginForeachState(step.ID, len(plans))
	completedIters := e.foreachCompletedIterationIDs(step.ID)

	onError := resolveErrorAction(step.OnError, workflow.Settings.OnError)
	var iterations []foreachIterationResult
	if config.Parallel {
		iterations = e.executeForeachIterationsParallel(ctx, step, workflow, plans, onError, foreachMaxConcurrent(config, e.limits), completedIters)
	} else {
		iterations = e.executeForeachIterationsSequential(ctx, step, workflow, plans, onError, completedIters)
	}

	result.ParsedData = iterations
	total, dispatched, skipped, failed := countForeachIterations(iterations)
	result.Output = fmt.Sprintf("Foreach completed: %d/%d dispatched, %d skipped, %d failed", dispatched, total, skipped, failed)
	result.FinishedAt = time.Now()

	// bd-dg38m: enforce ForeachConfig.OutputVarMode / Step.OutputVarMode now
	// that all iterations have completed. Per-iteration writes through
	// storeForeachNestedResult are last-writer-wins, which silently loses
	// N-1 outputs for aggregate (the default) and collect modes. Replace
	// parent.OutputVar with the right shape — []string in iteration order
	// for aggregate, map[string]string keyed by item identity for collect.
	// Last mode keeps the per-iteration last-writer behavior (declared
	// non-deterministic for parallel foreach by validateForeachOutputVarMode).
	e.storeForeachOutputVars(step, config, plans, iterations)
	if failed > 0 {
		result.Error = aggregateForeachErrors(iterations, step.ID, total)
		if onError != ErrorActionContinue {
			result.Status = StatusFailed
			e.emitProgress("step_error", step.ID, result.Error.Message, e.calculateProgress())
			return result
		}
	}
	if ctx.Err() != nil {
		result.Status = StatusCancelled
		result.Error = foreachStructuredError(step.ID, "cancelled", "context cancelled", "")
		return result
	}
	result.Status = StatusCompleted
	e.emitProgress("foreach_complete", step.ID, result.Output, e.calculateProgress())
	slog.Info("foreach step completed",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", step.ID,
		"agent_type", "foreach",
		"iterations", total,
		"dispatched", dispatched,
		"skipped", skipped,
		"failed", failed,
	)
	return result
}

func foreachConfigForStep(step *Step) (*ForeachConfig, string) {
	if step.Foreach != nil {
		return step.Foreach, "foreach"
	}
	return step.ForeachPane, "foreach_pane"
}

func foreachBodySteps(parent *Step, config *ForeachConfig) ([]Step, error) {
	steps := config.Steps
	if len(steps) == 0 && len(config.Body) > 0 {
		steps = config.Body
	}
	if len(steps) > 0 {
		out := make([]Step, len(steps))
		copy(out, steps)
		return out, nil
	}
	if config.Template == "" {
		return nil, fmt.Errorf("foreach step %q has no steps or template", parent.ID)
	}
	return []Step{{
		ID:          "template",
		Agent:       parent.Agent,
		Pane:        parent.Pane,
		Route:       parent.Route,
		Template:    config.Template,
		Params:      cloneInterfaceMap(config.Params),
		Wait:        parent.Wait,
		Timeout:     parent.Timeout,
		OnError:     parent.OnError,
		RetryCount:  parent.RetryCount,
		RetryDelay:  parent.RetryDelay,
		OutputVar:   parent.OutputVar,
		OutputParse: parent.OutputParse,
	}}, nil
}

func (e *Executor) resolveForeachItems(ctx context.Context, config *ForeachConfig, kind string) ([]interface{}, error) {
	if kind == "foreach_pane" {
		panes, err := e.tmuxClient().GetPanes(e.config.Session)
		if err != nil {
			return nil, fmt.Errorf("foreach_pane source: %w", err)
		}
		items := make([]interface{}, 0, len(panes))
		for _, pane := range panes {
			items = append(items, paneMetadataFromTmuxPane(pane).variableMap())
		}
		return items, nil
	}

	resolver := IterationSourceResolver{
		ProjectDir: e.config.ProjectDir,
		RunBr:      e.config.BeadQueryRunBr,
	}
	switch {
	case config.Items != "":
		e.varMu.RLock()
		vars := captureAllVariables(e.state.Variables)
		e.varMu.RUnlock()
		return resolver.ResolveItems(ctx, config.Items, vars)
	case config.Beads != "":
		return resolver.ResolveBeads(ctx, config.Beads)
	case config.Pairs != "":
		return resolver.ResolvePairs(ctx, config.Pairs)
	case config.Debates != "":
		return resolver.ResolveDebates(ctx, config.Debates)
	case len(config.Models) > 0:
		models, err := resolver.ResolveModels(ctx, config.Models)
		if err != nil {
			return nil, err
		}
		items := make([]interface{}, 0, len(models))
		for _, model := range models {
			items = append(items, model)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("foreach has no iteration source")
	}
}

func (e *Executor) prepareForeachIterations(ctx context.Context, parent *Step, config *ForeachConfig, kind string, body []Step, items []interface{}) ([]foreachIterationPlan, error) {
	var panes []tmux.Pane
	var strategyPanes []paneStrategyPane
	var err error
	needsPaneAssignment := kind == "foreach_pane" || config.PaneStrategy != ""
	if needsPaneAssignment {
		panes, err = e.tmuxClient().GetPanes(e.config.Session)
		if err != nil && !e.config.DryRun {
			return nil, fmt.Errorf("load panes for foreach assignment: %w", err)
		}
		strategyPanes = foreachStrategyPanes(panes)
		strategyPanes = e.enrichStrategyPanesFromRoster(strategyPanes)
	}

	plans := make([]foreachIterationPlan, 0, len(items))
	for i, item := range items {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		plan := foreachIterationPlan{Index: i, Item: item}
		if needsPaneAssignment {
			if kind == "foreach_pane" {
				plan.PaneID, plan.PaneIndex, plan.PaneVars = paneAssignmentFromItem(item, panes)
			}
			if plan.PaneID == "" && config.PaneStrategy != "" {
				paneID, paneIndex, paneVars, err := e.selectForeachPaneWithState(config.PaneStrategy, strategyPanes, panes, item, i)
				if err != nil {
					return nil, fmt.Errorf("iteration %d pane assignment: %w", i, err)
				}
				plan.PaneID = paneID
				plan.PaneIndex = paneIndex
				plan.PaneVars = paneVars
			}
		}

		if config.Filter != "" {
			include, err := EvaluateForeachFilter(config.Filter, FilterContext{Item: item, Pane: plan.PaneVars})
			if err != nil {
				return nil, fmt.Errorf("iteration %d filter: %w", i, err)
			}
			if !include {
				plan.Skipped = true
				plan.SkipKind = SkipKindForeachFilter
				plan.SkipReason = fmt.Sprintf("foreach filter %q evaluated to false", config.Filter)
				plans = append(plans, plan)
				continue
			}
		}

		steps, err := e.materializeForeachSteps(parent, config, body, plan, len(items))
		if err != nil {
			return nil, fmt.Errorf("iteration %d materialize steps: %w", i, err)
		}
		plan.Steps = steps
		plans = append(plans, plan)
	}
	return plans, nil
}

func (e *Executor) materializeForeachSteps(parent *Step, config *ForeachConfig, body []Step, plan foreachIterationPlan, total int) ([]Step, error) {
	// bd-htmpq: serialize the push-substitute-pop sequence so concurrent
	// foreach materializations (parallel outer + sequential inner, etc.)
	// cannot race on the shared alias keys (foreachVarName, loop.*,
	// paneVariableKey) in state.Variables. Without this, two inner
	// materializations interleave their pushes against the same global
	// keys and the substitution sees the other goroutine's iteration value.
	e.foreachMaterializeMu.Lock()
	defer e.foreachMaterializeMu.Unlock()

	varName := foreachVarName(config)

	scope := e.pushForeachVars(varName, plan.Item, plan.Index, total, plan.PaneVars)
	defer e.popForeachVars(scope)

	steps := make([]Step, 0, len(body))
	for bodyIndex, nested := range body {
		materialized := cloneStep(nested)
		if materialized.ID == "" {
			materialized.ID = fmt.Sprintf("step%d", bodyIndex)
		}
		materialized.ID = fmt.Sprintf("%s_iter%d_%s", parent.ID, plan.Index, materialized.ID)
		if plan.PaneIndex > 0 && materialized.Pane.Index == 0 && materialized.Pane.Expr == "" {
			materialized.Pane.Index = plan.PaneIndex
		}
		e.substituteForeachStepFields(&materialized)
		steps = append(steps, materialized)
	}
	return steps, nil
}

func (e *Executor) pushForeachVars(varName string, item interface{}, index, total int, paneVars map[string]interface{}) VariableScope {
	e.varMu.Lock()
	defer e.varMu.Unlock()
	keys := append(loopScopeKeys(varName), paneVariableKey, varName)
	scope := CaptureVariableScope(e.state.Variables, keys...)
	SetLoopVars(e.state, varName, item, index, total)
	e.state.Variables[varName] = item
	if paneVars != nil {
		e.state.Variables[paneVariableKey] = cloneInterfaceMap(paneVars)
	}
	return scope
}

func (e *Executor) popForeachVars(scope VariableScope) {
	e.varMu.Lock()
	defer e.varMu.Unlock()
	scope.Restore(e.state.Variables)
}

// bd-2ubxp.19: foreachProgressTracker emits aggregate `foreach progress`
// events as iterations complete, giving operators visibility into long-running
// fan-outs without watching individual step events. The tracker is safe for
// concurrent use so the parallel runner can call iterationFinished() from
// goroutines.
type foreachProgressTracker struct {
	e        *Executor
	workflow *Workflow
	parent   *Step
	total    int
	interval int
	start    time.Time
	count    atomic.Int64
}

func newForeachProgressTracker(e *Executor, workflow *Workflow, parent *Step, total int) *foreachProgressTracker {
	return &foreachProgressTracker{
		e:        e,
		workflow: workflow,
		parent:   parent,
		total:    total,
		interval: foreachProgressInterval(total),
		start:    time.Now(),
	}
}

// foreachProgressInterval returns the iteration count between progress events.
// Spec: emit every 10% or every 5 iterations, whichever produces more frequent
// updates (i.e. the smaller interval). Floor at 1 so even tiny foreach steps
// emit at least one progress tick.
func foreachProgressInterval(total int) int {
	if total <= 0 {
		return 1
	}
	tenth := total / 10
	if total%10 != 0 {
		tenth++
	}
	if tenth < 1 {
		tenth = 1
	}
	interval := tenth
	if interval > 5 {
		interval = 5
	}
	return interval
}

func (p *foreachProgressTracker) iterationFinished() {
	if p == nil || p.total == 0 {
		return
	}
	completed := int(p.count.Add(1))
	due := completed%p.interval == 0 || completed == p.total
	if !due {
		return
	}
	slog.Info("foreach progress",
		"run_id", p.e.state.RunID,
		"workflow", p.workflow.Name,
		"step_id", p.parent.ID,
		"agent_type", "foreach",
		"completed", completed,
		"total", p.total,
		"elapsed_ms", time.Since(p.start).Milliseconds(),
	)
}

// foreachIterationStatusLabel summarizes an iteration outcome for log
// aggregation. Order matters: explicit loop control wins over the
// completion status because it captures the reason the iteration ended
// even when its body steps succeeded.
func foreachIterationStatusLabel(result foreachIterationResult) string {
	switch result.Control {
	case LoopControlBreak:
		return "break"
	case LoopControlContinue:
		return "continue"
	}
	if result.Error != "" {
		return "failed"
	}
	if result.Skipped {
		return "skipped"
	}
	if foreachIterationCancelled(result) {
		return "cancelled"
	}
	return "completed"
}

func (e *Executor) executeForeachIterationsSequential(ctx context.Context, parent *Step, workflow *Workflow, plans []foreachIterationPlan, onError ErrorAction, completedIters map[string]struct{}) []foreachIterationResult {
	results := make([]foreachIterationResult, 0, len(plans))
	progress := newForeachProgressTracker(e, workflow, parent, len(plans))
	for i, plan := range plans {
		if plan.Skipped {
			results = append(results, skippedForeachIteration(plan))
			progress.iterationFinished()
			continue
		}
		// bd-qeatk: a prior run already completed this iteration; skip
		// re-dispatch so commands/prompts don't duplicate side effects.
		if _, done := completedIters[loopIterationID(parent.ID, plan.Index)]; done {
			results = append(results, resumeCompletedForeachIteration(plan))
			progress.iterationFinished()
			continue
		}
		iterResult := e.executeForeachIteration(ctx, parent, workflow, plan, onError)
		results = append(results, iterResult)
		progress.iterationFinished()
		if !iterResult.failed() && iterResult.Control != LoopControlBreak {
			e.markForeachIterationCompleted(parent.ID, plan.Index, len(plans))
		}
		if iterResult.Control == LoopControlBreak {
			for _, remaining := range plans[i+1:] {
				results = append(results, foreachBreakSkippedIteration(remaining))
			}
			break
		}
		if iterResult.failed() && onError == ErrorActionFailFast {
			for _, remaining := range plans[i+1:] {
				results = append(results, cancelledForeachIteration(remaining))
			}
			break
		}
		if iterResult.failed() && onError != ErrorActionContinue {
			break
		}
	}
	return results
}

func (e *Executor) executeForeachIterationsParallel(ctx context.Context, parent *Step, workflow *Workflow, plans []foreachIterationPlan, onError ErrorAction, maxConcurrent int, completedIters map[string]struct{}) []foreachIterationResult {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]foreachIterationResult, len(plans))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var controlMu sync.Mutex
	breakSeen := false
	progress := newForeachProgressTracker(e, workflow, parent, len(plans))

	markBreak := func() {
		controlMu.Lock()
		breakSeen = true
		controlMu.Unlock()
		cancel()
	}
	isBreak := func() bool {
		controlMu.Lock()
		defer controlMu.Unlock()
		return breakSeen
	}

	for i, plan := range plans {
		if plan.Skipped {
			results[i] = skippedForeachIteration(plan)
			progress.iterationFinished()
			continue
		}
		// bd-qeatk: prior-run completion suppresses re-dispatch on resume.
		if _, done := completedIters[loopIterationID(parent.ID, plan.Index)]; done {
			results[i] = resumeCompletedForeachIteration(plan)
			progress.iterationFinished()
			continue
		}
		wg.Add(1)
		go func(i int, plan foreachIterationPlan) {
			defer wg.Done()
			defer progress.iterationFinished()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-runCtx.Done():
				if isBreak() {
					results[i] = foreachBreakSkippedIteration(plan)
				} else {
					results[i] = cancelledForeachIteration(plan)
				}
				return
			}
			iterResult := e.executeForeachIteration(runCtx, parent, workflow, plan, onError)
			if isBreak() && foreachIterationCancelled(iterResult) {
				iterResult.SkipKind = SkipKindForeachBreak
				iterResult.SkipReason = "loop break"
				iterResult.Error = ""
			}
			results[i] = iterResult
			if !iterResult.failed() && iterResult.Control != LoopControlBreak {
				e.markForeachIterationCompleted(parent.ID, plan.Index, len(plans))
			}
			if iterResult.Control == LoopControlBreak {
				markBreak()
				return
			}
			if iterResult.failed() && onError == ErrorActionFailFast {
				cancel()
			}
		}(i, plan)
	}
	wg.Wait()
	return results
}

// executeForeachIteration runs the body for a single foreach plan. parentOnError
// is the parent foreach step's resolved error action (already merged with the
// workflow default by the caller); nested body steps without their own
// on_error inherit it so a foreach with on_error: continue does not stall on
// the workflow default of fail (bd-ljx8s).
func (e *Executor) executeForeachIteration(ctx context.Context, parent *Step, workflow *Workflow, plan foreachIterationPlan, parentOnError ErrorAction) (iterResult foreachIterationResult) {
	iterResult = foreachIterationResult{
		Index:   plan.Index,
		Item:    plan.Item,
		Pane:    cloneInterfaceMap(plan.PaneVars),
		Results: make([]StepResult, 0, len(plan.Steps)),
	}

	slog.Info("foreach iteration starting",
		"run_id", e.state.RunID,
		"workflow", workflow.Name,
		"step_id", parent.ID,
		"agent_type", "foreach",
		"iteration", plan.Index,
		"pane_id", plan.PaneID,
	)
	// bd-2ubxp.19: emit per-iteration completion event with duration + status
	// so operators can track long-running foreach steps without inferring
	// progress from per-step events. Status reflects the iteration outcome
	// (completed / failed / skipped / cancelled / break / continue) so log
	// pipelines can aggregate.
	iterStart := time.Now()
	defer func() {
		slog.Info("foreach iteration completed",
			"run_id", e.state.RunID,
			"workflow", workflow.Name,
			"step_id", parent.ID,
			"agent_type", "foreach",
			"iteration", plan.Index,
			"pane_id", plan.PaneID,
			"duration_ms", time.Since(iterStart).Milliseconds(),
			"status", foreachIterationStatusLabel(iterResult),
		)
	}()

	// bd-2ubxp.14: max_rounds wraps each iteration's body in an inner round
	// loop. When unset the resolver returns 1 and behavior is identical to
	// the historical single-round path. Per-round IDs are suffixed so each
	// round's nested results land under unique keys in state.Steps.
	maxRounds, mrErr := e.resolveForeachMaxRounds(parent)
	if mrErr != nil {
		iterResult.Error = mrErr.Error()
		return iterResult
	}

	for round := 1; round <= maxRounds; round++ {
		releaseRound := e.pushRoundVars(round, maxRounds)

		stepsForRound := plan.Steps
		if maxRounds > 1 {
			stepsForRound = rewriteRoundStepIDs(plan.Steps, round)
		}

		exit := e.runForeachIterationRound(ctx, parent, workflow, plan, parentOnError, stepsForRound, &iterResult)
		releaseRound()
		if exit {
			return iterResult
		}
	}
	return markForeachIterationSkippedIfAllResultsSkipped(iterResult)
}

// runForeachIterationRound executes one round of an iteration's body steps.
// Returns true when the caller should stop running further rounds (ctx
// cancelled, error not handled by on_error: continue, or loop_control fired).
// Mutates iterResult in place by appending body step results, setting Control,
// and storing nested step state via storeForeachNestedResult.
//
// Extracted from executeForeachIteration so max_rounds (bd-2ubxp.14) can call
// the body loop once per round without duplicating the per-step ctx, control,
// skip, and on_error handling.
func (e *Executor) runForeachIterationRound(ctx context.Context, parent *Step, workflow *Workflow, plan foreachIterationPlan, parentOnError ErrorAction, stepsForRound []Step, iterResult *foreachIterationResult) bool {
	for i := range stepsForRound {
		select {
		case <-ctx.Done():
			cancelled := StepResult{
				StepID:     stepsForRound[i].ID,
				Status:     StatusCancelled,
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
				SkipKind:   SkipKindCancelled,
				Error: &StepError{
					Type:      "cancelled",
					Message:   "foreach iteration cancelled",
					Timestamp: time.Now(),
				},
			}
			iterResult.Results = append(iterResult.Results, cancelled)
			e.storeForeachNestedResult(&stepsForRound[i], cancelled)
			return true
		default:
		}

		if foreachControlOnlyStep(stepsForRound[i]) {
			control, applies, err := e.foreachLoopControlDecision(stepsForRound[i])
			if err != nil {
				failed := failedForeachLoopControlCondition(stepsForRound[i], err)
				iterResult.Results = append(iterResult.Results, failed)
				e.storeForeachNestedResult(&stepsForRound[i], failed)
				iterResult.Error = resultErrorMessage(failed)
				return true
			}
			if applies {
				iterResult.Control = control
				e.logForeachLoopControl(control, workflow, parent, plan)
				return true
			}
			continue
		}

		step := stepsForRound[i]
		result := e.executeForeachNestedStep(ctx, &step, workflow)
		iterResult.Results = append(iterResult.Results, result)
		e.storeForeachNestedResult(&step, result)

		if result.Status == StatusSkipped {
			if step.LoopControl == LoopControlBreak || step.LoopControl == LoopControlContinue {
				continue
			}
			slog.Info("foreach body step skipped",
				"run_id", e.state.RunID,
				"workflow", workflow.Name,
				"step_id", parent.ID,
				"agent_type", "foreach",
				"iteration", plan.Index,
				"body_step_id", step.ID,
				"skip_kind", result.SkipKind,
			)
			continue
		}

		if result.Status == StatusFailed || result.Status == StatusCancelled {
			if resolveErrorAction(step.OnError, parentOnError) != ErrorActionContinue {
				iterResult.Error = resultErrorMessage(result)
				return true
			}
		}

		if result.Status == StatusCompleted {
			if control, applies := foreachLoopControlValue(step); applies {
				iterResult.Control = control
				e.logForeachLoopControl(control, workflow, parent, plan)
				return true
			}
		}
	}
	return false
}

func markForeachIterationSkippedIfAllResultsSkipped(result foreachIterationResult) foreachIterationResult {
	if len(result.Results) == 0 || result.Error != "" || result.Control != LoopControlNone {
		return result
	}
	var skipKind SkipKind
	var skipReason string
	for _, stepResult := range result.Results {
		if stepResult.Status != StatusSkipped {
			return result
		}
		if skipKind == "" {
			skipKind = stepResult.SkipKind
			skipReason = stepResult.SkipReason
		}
	}
	result.Skipped = true
	result.SkipKind = skipKind
	result.SkipReason = skipReason
	return result
}

func (e *Executor) foreachLoopControl(step Step) (LoopControl, bool) {
	control, applies, _ := e.foreachLoopControlDecision(step)
	return control, applies
}

func (e *Executor) foreachLoopControlDecision(step Step) (LoopControl, bool, error) {
	control, ok := foreachLoopControlValue(step)
	if !ok {
		return LoopControlNone, false, nil
	}
	if step.When == "" {
		return control, true, nil
	}
	skip, err := e.evaluateCondition(step.When)
	if err != nil || skip {
		return LoopControlNone, false, err
	}
	return control, true, nil
}

func foreachLoopControlValue(step Step) (LoopControl, bool) {
	switch step.LoopControl {
	case LoopControlBreak, LoopControlContinue:
		return step.LoopControl, true
	default:
		return LoopControlNone, false
	}
}

func foreachControlOnlyStep(step Step) bool {
	if _, ok := foreachLoopControlValue(step); !ok {
		return false
	}
	return step.Command == "" &&
		step.Template == "" &&
		step.Prompt == "" &&
		step.PromptFile == "" &&
		step.Branch == "" &&
		len(step.Branches) == 0 &&
		len(step.Parallel.Steps) == 0 &&
		step.Loop == nil &&
		step.Foreach == nil &&
		step.ForeachPane == nil &&
		step.BeadQuery == nil &&
		len(step.mailStepKindNames()) == 0
}

func failedForeachLoopControlCondition(step Step, err error) StepResult {
	now := time.Now()
	return StepResult{
		StepID:     step.ID,
		Status:     StatusFailed,
		StartedAt:  now,
		FinishedAt: now,
		Error: &StepError{
			Type:      "condition",
			Message:   fmt.Sprintf("failed to evaluate when condition: %v", err),
			Timestamp: now,
		},
	}
}

func (e *Executor) logForeachLoopControl(control LoopControl, workflow *Workflow, parent *Step, plan foreachIterationPlan) {
	switch control {
	case LoopControlBreak:
		slog.Info("foreach iteration requested break",
			"run_id", e.state.RunID,
			"workflow", workflow.Name,
			"step_id", parent.ID,
			"agent_type", "foreach",
			"iteration", plan.Index,
			"pane_id", plan.PaneID,
		)
	case LoopControlContinue:
		slog.Debug("foreach iteration requested continue",
			"run_id", e.state.RunID,
			"workflow", workflow.Name,
			"step_id", parent.ID,
			"agent_type", "foreach",
			"iteration", plan.Index,
			"pane_id", plan.PaneID,
		)
	}
}

func (e *Executor) executeForeachNestedStep(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	if step.When != "" {
		skip, err := e.evaluateCondition(step.When)
		if err != nil {
			return StepResult{
				StepID:     step.ID,
				Status:     StatusFailed,
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
				Error: &StepError{
					Type:      "condition",
					Message:   fmt.Sprintf("failed to evaluate when condition: %v", err),
					Timestamp: time.Now(),
				},
			}
		}
		if skip {
			now := time.Now()
			return StepResult{
				StepID:     step.ID,
				Status:     StatusSkipped,
				StartedAt:  now,
				FinishedAt: now,
				SkipReason: fmt.Sprintf("condition %q evaluated to false", step.When),
				SkipKind:   SkipKindWhenCondition,
			}
		}
	}

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

	var result StepResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result = e.executeForeachNestedStepOnce(ctx, step, workflow)
		result.Attempts = attempt
		if result.Status == StatusCompleted || result.Status == StatusSkipped || result.Status == StatusCancelled {
			return result
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.FinishedAt = time.Now()
				return result
			case <-time.After(e.calculateRetryDelay(retryDelay, attempt, step.RetryBackoff)):
			}
		}
	}

	// Mirror executeStep's on_failure tail so foreach body steps honour the
	// same recovery contract as top-level steps. Without this, moving a step
	// from top-level into a foreach body silently disabled on_failure
	// actions (fallback_to_ntm_inbox / suppress) and recovery template
	// dispatches — which the brennerbot/incident workflows rely on for
	// per-item fallback handling.
	if result.Status == StatusFailed {
		result = e.executeOnFailureAction(step, result)
		if result.Status == StatusSkipped {
			return result
		}
		result = e.executeOnFailureRecovery(ctx, step, workflow, result)
	}
	return result
}

func (e *Executor) executeForeachNestedStepOnce(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	switch {
	case len(step.Parallel.Steps) > 0:
		return e.executeParallel(ctx, step, workflow)
	case step.Loop != nil:
		return e.executeLoop(ctx, step, workflow)
	default:
		return e.executeStepOnce(ctx, step, workflow)
	}
}

// storeForeachOutputVars aggregates per-iteration outputs into parent.OutputVar
// according to the effective output_var_mode (Step.OutputVarMode falls back to
// ForeachConfig.OutputVarMode falls back to aggregate). Called once after all
// iterations finish so the final state of parent.OutputVar matches the
// declared shape rather than whatever per-iteration write happened to land
// last under varMu (bd-dg38m).
//
//   - aggregate (default): []string in iteration order, []interface{} parsed
//     output if any iteration produced ParsedData.
//   - collect: map[string]string keyed by foreachIterationKey(iter), with
//     parent.OutputVar+"_parsed" mirror when any iteration has parsed data.
//   - last: keep per-iteration last-writer-wins (sequential foreach: the
//     final iteration; parallel: completion-order last, declared
//     non-deterministic by validateForeachOutputVarMode).
//
// Iterations whose body step at the matching index didn't reach
// StatusCompleted (skipped, failed, cancelled) contribute no entry — their
// output is intentionally absent from the aggregate so consumers can
// distinguish "no result" from "empty result".
func (e *Executor) storeForeachOutputVars(parent *Step, config *ForeachConfig, plans []foreachIterationPlan, iterations []foreachIterationResult) {
	if e == nil || e.state == nil || parent == nil || parent.OutputVar == "" {
		return
	}
	if config == nil {
		return
	}

	mode := effectiveForeachOutputVarMode(parent, config)
	if mode == OutputVarModeLast {
		// Last mode: leave whatever per-iteration storeForeachNestedResult
		// wrote last. Sequential foreach lands on the final iteration's
		// output deterministically; parallel foreach lands on completion-
		// order last (validation already warns this is non-deterministic).
		return
	}

	type collected struct {
		Iteration  int
		ItemKey    string
		Output     string
		ParsedData interface{}
	}
	items := make([]collected, 0, len(iterations))
	for i, iter := range iterations {
		if iter.Skipped || iter.Error != "" {
			continue
		}
		idx := matchingForeachBodyResultIndex(plans, i, parent.OutputVar)
		if idx < 0 || idx >= len(iter.Results) {
			continue
		}
		res := iter.Results[idx]
		if res.Status != StatusCompleted {
			continue
		}
		items = append(items, collected{
			Iteration:  iter.Index,
			ItemKey:    foreachIterationKey(iter),
			Output:     res.Output,
			ParsedData: res.ParsedData,
		})
	}

	e.varMu.Lock()
	defer e.varMu.Unlock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}

	switch mode {
	case OutputVarModeCollect:
		outputs := make(map[string]string, len(items))
		parsed := make(map[string]interface{}, len(items))
		hasParsed := false
		for _, it := range items {
			outputs[it.ItemKey] = it.Output
			if it.ParsedData != nil {
				parsed[it.ItemKey] = it.ParsedData
				hasParsed = true
			}
		}
		e.state.Variables[parent.OutputVar] = outputs
		if hasParsed {
			e.state.Variables[parent.OutputVar+"_parsed"] = parsed
		}
	default: // aggregate
		outputs := make([]string, 0, len(items))
		parsed := make([]interface{}, 0, len(items))
		hasParsed := false
		for _, it := range items {
			outputs = append(outputs, it.Output)
			parsed = append(parsed, it.ParsedData)
			if it.ParsedData != nil {
				hasParsed = true
			}
		}
		e.state.Variables[parent.OutputVar] = outputs
		if hasParsed {
			e.state.Variables[parent.OutputVar+"_parsed"] = parsed
		}
	}
}

// effectiveForeachOutputVarMode resolves the cascade Step.OutputVarMode →
// ForeachConfig.OutputVarMode → OutputVarModeAggregate.
func effectiveForeachOutputVarMode(parent *Step, config *ForeachConfig) OutputVarMode {
	if parent != nil && parent.OutputVarMode != "" {
		return normalizeOutputVarMode(parent.OutputVarMode)
	}
	if config != nil && config.OutputVarMode != "" {
		return normalizeOutputVarMode(config.OutputVarMode)
	}
	return OutputVarModeAggregate
}

// matchingForeachBodyResultIndex finds the first body step in plans[planIdx]
// whose declared OutputVar matches the foreach parent's OutputVar. Returns
// -1 when no body step writes to that variable (the parent's OutputVar is
// either set without a corresponding body step write, or unset).
//
// The materialized body step IDs end with "_<originalID>", so we match
// against the raw body step's OutputVar via the iteration plan, which still
// holds the substituted body steps.
func matchingForeachBodyResultIndex(plans []foreachIterationPlan, planIdx int, outputVar string) int {
	if planIdx < 0 || planIdx >= len(plans) {
		return -1
	}
	for i, step := range plans[planIdx].Steps {
		if step.OutputVar == outputVar {
			return i
		}
	}
	return -1
}

// foreachIterationKey derives a stable identity for an iteration suitable
// for keying into a collect-mode map. Prefers structured fields ("id",
// "key", "name") on map items; falls back to the raw string item; final
// fallback is "iter_<index>" so keys stay distinct even for opaque items.
func foreachIterationKey(iter foreachIterationResult) string {
	switch v := iter.Item.(type) {
	case map[string]interface{}:
		for _, key := range []string{"id", "key", "name"} {
			if val, ok := v[key]; ok {
				if s := fmt.Sprintf("%v", val); s != "" {
					return s
				}
			}
		}
	case map[interface{}]interface{}:
		for _, key := range []string{"id", "key", "name"} {
			if val, ok := v[key]; ok {
				if s := fmt.Sprintf("%v", val); s != "" {
					return s
				}
			}
		}
	case string:
		if v != "" {
			return v
		}
	}
	return fmt.Sprintf("iter_%d", iter.Index)
}

func (e *Executor) storeForeachNestedResult(step *Step, result StepResult) {
	e.stateMu.Lock()
	if e.state.Steps == nil {
		e.state.Steps = make(map[string]StepResult)
	}
	e.state.Steps[result.StepID] = result
	e.stateMu.Unlock()

	if result.Status != StatusCompleted {
		return
	}
	e.varMu.Lock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}
	if step.OutputVar != "" {
		e.state.Variables[step.OutputVar] = result.Output
		if result.ParsedData != nil {
			e.state.Variables[step.OutputVar+"_parsed"] = result.ParsedData
		}
	}
	StoreStepOutput(e.state, result.StepID, result.Output, result.ParsedData)
	e.varMu.Unlock()
}

func (e *Executor) substituteForeachStepFields(step *Step) {
	e.substituteForeachStepFieldsProtected(step, nil)
}

func (e *Executor) substituteForeachStepFieldsProtected(step *Step, protected map[string]struct{}) {
	step.Name = e.substituteForeachString(step.Name, protected)
	step.Description = e.substituteForeachString(step.Description, protected)
	step.Agent = e.substituteForeachString(step.Agent, protected)
	step.Prompt = e.substituteForeachString(step.Prompt, protected)
	step.PromptFile = e.substituteForeachString(step.PromptFile, protected)
	step.Command = e.substituteForeachString(step.Command, protected)
	step.Template = e.substituteForeachString(step.Template, protected)
	step.Wait = WaitCondition(e.substituteForeachString(string(step.Wait), protected))
	step.When = e.substituteForeachString(step.When, protected)
	step.Branch = e.substituteForeachString(step.Branch, protected)
	step.OutputVar = e.substituteForeachString(step.OutputVar, protected)
	step.Args = substituteForeachInterfaceMap(e, step.Args, protected)
	step.Params = substituteForeachInterfaceMap(e, step.Params, protected)
	step.TemplateParams = substituteForeachInterfaceMap(e, step.TemplateParams, protected)
	if step.Pane.Expr != "" {
		resolved := strings.TrimSpace(e.substituteForeachString(step.Pane.Expr, protected))
		if idx, err := strconv.Atoi(resolved); err == nil {
			step.Pane.Index = idx
			step.Pane.Expr = ""
		} else {
			step.Pane.Expr = resolved
		}
	}
	for i := range step.Parallel.Steps {
		e.substituteForeachStepFieldsProtected(&step.Parallel.Steps[i], protected)
	}
	if step.Loop != nil {
		for i := range step.Loop.Steps {
			e.substituteForeachStepFieldsProtected(&step.Loop.Steps[i], protected)
		}
		for i := range step.Loop.Body {
			e.substituteForeachStepFieldsProtected(&step.Loop.Body[i], protected)
		}
	}
	for _, config := range []*ForeachConfig{step.Foreach, step.ForeachPane} {
		if config == nil {
			continue
		}
		bodyProtected := cloneProtectedRoots(protected)
		bodyProtected[foreachVarName(config)] = struct{}{}
		bodyProtected["loop"] = struct{}{}
		if foreachVarName(config) == "item" && e.foreachLoopItemActive() {
			slog.Debug("nested foreach item alias shadows outer item",
				"run_id", e.state.RunID,
				"step_id", step.ID,
				"agent_type", "foreach",
			)
		}

		config.Items = e.substituteForeachString(config.Items, protected)
		config.Beads = e.substituteForeachString(config.Beads, protected)
		config.Pairs = e.substituteForeachString(config.Pairs, protected)
		config.Debates = e.substituteForeachString(config.Debates, protected)
		config.Filter = e.substituteForeachString(config.Filter, protected)
		// bd-8bujt: foreach.models is a StringOrList that can hold either
		// an inline literal list of model-family tokens or (single-entry)
		// a shell command emitting one family per line. Both forms must
		// receive the same protected-root substitution as items/beads/etc.
		// so workflow vars like ${vars.model_family} resolve before
		// ResolveForeachItems hands the list to the runtime.
		for i := range config.Models {
			config.Models[i] = e.substituteForeachString(config.Models[i], protected)
		}
		config.Template = e.substituteForeachString(config.Template, bodyProtected)
		config.Params = substituteForeachInterfaceMap(e, config.Params, bodyProtected)
		config.TemplateParams = substituteForeachInterfaceMap(e, config.TemplateParams, bodyProtected)
		for i := range config.Steps {
			e.substituteForeachStepFieldsProtected(&config.Steps[i], bodyProtected)
		}
		for i := range config.Body {
			e.substituteForeachStepFieldsProtected(&config.Body[i], bodyProtected)
		}
	}
}

func foreachVarName(config *ForeachConfig) string {
	if config == nil || config.As == "" {
		return "item"
	}
	return config.As
}

func cloneProtectedRoots(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in)+2)
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func (e *Executor) foreachLoopItemActive() bool {
	e.varMu.RLock()
	defer e.varMu.RUnlock()
	if e.state == nil || e.state.Variables == nil {
		return false
	}
	_, ok := e.state.Variables["loop.item"]
	return ok
}

func (e *Executor) substituteForeachString(s string, protected map[string]struct{}) string {
	if s == "" {
		return ""
	}

	type protectedRef struct {
		token string
		match string
	}
	var refs []protectedRef
	escaped := escapedPattern.ReplaceAllString(s, escapePlaceholder)
	rewritten := varPattern.ReplaceAllStringFunc(escaped, func(match string) string {
		expr := strings.TrimSpace(match[2 : len(match)-1])
		varPath, defaultVal, hasDefault := parseDefault(expr)
		root := foreachVarRoot(varPath)
		if _, ok := protected[root]; ok {
			token := fmt.Sprintf("\x00FOREACH_PROTECTED_%d\x00", len(refs))
			refs = append(refs, protectedRef{token: token, match: match})
			return token
		}
		value, found, err := e.resolveForeachAlias(varPath)
		if found {
			if err == nil {
				return formatValue(value)
			}
			if hasDefault {
				return defaultVal
			}
			return match
		}
		return match
	})

	out := e.substituteVariables(rewritten)
	for _, ref := range refs {
		out = strings.ReplaceAll(out, ref.token, ref.match)
	}
	return out
}

func foreachVarRoot(varPath string) string {
	varPath = strings.TrimSpace(varPath)
	if varPath == "" {
		return ""
	}
	if idx := strings.IndexByte(varPath, '.'); idx >= 0 {
		return varPath[:idx]
	}
	return varPath
}

func (e *Executor) resolveForeachAlias(varPath string) (interface{}, bool, error) {
	parts := strings.Split(strings.TrimSpace(varPath), ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil, false, nil
	}
	if knownSubstitutionNamespace(parts[0]) {
		return nil, false, nil
	}

	e.varMu.RLock()
	defer e.varMu.RUnlock()
	if e.state == nil || e.state.Variables == nil {
		return nil, false, nil
	}
	value, ok := e.state.Variables[parts[0]]
	if !ok {
		return nil, false, nil
	}
	if len(parts) == 1 {
		return value, true, nil
	}
	resolved, err := navigateNested(value, parts[1:])
	return resolved, true, err
}

func knownSubstitutionNamespace(root string) bool {
	switch root {
	case "vars", "steps", "env", "loop", "defaults", "item", "pane", "session", "run_id", "timestamp", "workflow", "runtime":
		return true
	default:
		return false
	}
}

func foreachMaxConcurrent(config *ForeachConfig, limits LimitsConfig) int {
	// bd-pwxh1: per-step foreach.max_concurrent is bounded by the global
	// settings.limits.max_concurrent_foreach cap. Treating it as an
	// override let a workflow set a low global safety cap and then put a
	// huge per-step value on a single foreach, which spawned goroutines
	// and shell/agent work far beyond the configured cap and defeated
	// the resource-limit contract for runaway pipelines.
	globalCap := limits.MaxConcurrentForeach
	maxConcurrent := globalCap
	if config.MaxConcurrent > 0 {
		maxConcurrent = config.MaxConcurrent
		if globalCap > 0 && maxConcurrent > globalCap {
			maxConcurrent = globalCap
		}
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return maxConcurrent
}

func countForeachIterations(results []foreachIterationResult) (total, dispatched, skipped, failed int) {
	total = len(results)
	for _, result := range results {
		if result.Skipped {
			skipped++
			continue
		}
		dispatched++
		if result.failed() {
			failed++
		}
	}
	return total, dispatched, skipped, failed
}

func firstForeachError(iterations []foreachIterationResult, stepID string) *StepError {
	for _, iteration := range iterations {
		if !iteration.failed() {
			continue
		}
		message := iteration.Error
		if message == "" {
			for _, result := range iteration.Results {
				if result.Status == StatusFailed || result.Status == StatusCancelled {
					message = resultErrorMessage(result)
					break
				}
			}
		}
		if message == "" {
			message = "iteration failed"
		}
		// bd-wio84: structured per-iteration failure metadata.
		return foreachStructuredErrorAtIteration(stepID, "foreach", message, "", iteration.Index)
	}
	return foreachStructuredError(stepID, "foreach", "iteration failure with no detail", "")
}

func finishForeachFailure(result StepResult, typ, message string) StepResult {
	// bd-wio84: emit the same kind=foreach step_id=... reason=... details
	// shape that stepRuntimeError produces for command/template steps so
	// robot consumers and operators get structured foreach failure
	// metadata instead of a bare message.
	result.Status = StatusFailed
	result.Error = foreachStructuredError(result.StepID, typ, message, "")
	result.FinishedAt = time.Now()
	return result
}

// foreachStructuredError builds a StepError whose Details encodes
// kind=foreach, step_id, reason, and (when available) the failing
// iteration index. Mirrors stepRuntimeError so the runtime-error contract
// from bd-tld34 covers foreach failures (bd-wio84).
func foreachStructuredError(stepID, typ, reason, hint string) *StepError {
	return foreachStructuredErrorAtIteration(stepID, typ, reason, hint, -1)
}

func foreachStructuredErrorAtIteration(stepID, typ, reason, hint string, iter int) *StepError {
	parts := []string{
		"kind=foreach",
		"step_id=" + stepID,
		"reason=" + reason,
	}
	message := fmt.Sprintf("foreach step %q failed: %s", stepID, reason)
	if iter >= 0 {
		parts = append(parts, fmt.Sprintf("iteration=%d", iter))
		message = fmt.Sprintf("foreach step %q failed at iteration %d: %s", stepID, iter, reason)
	}
	if hint != "" {
		parts = append(parts, "hint="+hint)
	}
	return &StepError{
		Type:      typ,
		Message:   message,
		Details:   strings.Join(parts, " "),
		Timestamp: time.Now(),
	}
}

func skippedForeachIteration(plan foreachIterationPlan) foreachIterationResult {
	return foreachIterationResult{
		Index:      plan.Index,
		Item:       plan.Item,
		Pane:       cloneInterfaceMap(plan.PaneVars),
		Skipped:    true,
		SkipReason: plan.SkipReason,
		SkipKind:   plan.SkipKind,
	}
}

func cancelledForeachIteration(plan foreachIterationPlan) foreachIterationResult {
	now := time.Now()
	return foreachIterationResult{
		Index: plan.Index,
		Item:  plan.Item,
		Pane:  cloneInterfaceMap(plan.PaneVars),
		Results: []StepResult{{
			StepID:     fmt.Sprintf("foreach_iter%d_cancelled", plan.Index),
			Status:     StatusCancelled,
			StartedAt:  now,
			FinishedAt: now,
			SkipKind:   SkipKindCancelled,
			Error: &StepError{
				Type:      "cancelled",
				Message:   "foreach iteration cancelled before dispatch",
				Timestamp: now,
			},
		}},
		Error: "foreach iteration cancelled before dispatch",
	}
}

func foreachBreakSkippedIteration(plan foreachIterationPlan) foreachIterationResult {
	return foreachIterationResult{
		Index:      plan.Index,
		Item:       plan.Item,
		Pane:       cloneInterfaceMap(plan.PaneVars),
		Skipped:    true,
		SkipReason: "loop break",
		SkipKind:   SkipKindForeachBreak,
	}
}

// resumeCompletedForeachIteration represents a foreach iteration whose body
// fully completed in a prior run. On resume the dispatch is suppressed so
// nested side-effects (commands, prompts) do not duplicate (bd-qeatk).
func resumeCompletedForeachIteration(plan foreachIterationPlan) foreachIterationResult {
	return foreachIterationResult{
		Index:      plan.Index,
		Item:       plan.Item,
		Pane:       cloneInterfaceMap(plan.PaneVars),
		Skipped:    true,
		SkipReason: "iteration completed in prior run; suppressed on resume",
		SkipKind:   SkipKindResumeAlreadyCompleted,
	}
}

// foreachCompletedIterationIDs returns the durable set of iteration IDs that
// completed in a prior run for the given foreach step. Used to suppress
// re-dispatch of those iterations on resume.
func (e *Executor) foreachCompletedIterationIDs(stepID string) map[string]struct{} {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return nil
	}
	st, ok := e.state.ForeachState[stepID]
	if !ok || len(st.CompletedIterationIDs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(st.CompletedIterationIDs))
	for _, id := range st.CompletedIterationIDs {
		out[id] = struct{}{}
	}
	return out
}

func foreachIterationCancelled(result foreachIterationResult) bool {
	if result.SkipKind == SkipKindCancelled {
		return true
	}
	if strings.Contains(strings.ToLower(result.Error), "cancelled") {
		return true
	}
	for _, stepResult := range result.Results {
		if stepResult.Status == StatusCancelled {
			return true
		}
	}
	return false
}

func resultErrorMessage(result StepResult) string {
	if result.Error != nil {
		return result.Error.Message
	}
	if result.SkipReason != "" {
		return result.SkipReason
	}
	if result.Status != "" {
		return string(result.Status)
	}
	return "unknown error"
}

func cloneStep(step Step) Step {
	step.Args = cloneInterfaceMap(step.Args)
	step.Params = cloneInterfaceMap(step.Params)
	step.TemplateParams = cloneInterfaceMap(step.TemplateParams)
	step.DependsOn = append([]string(nil), step.DependsOn...)
	step.After = append(AfterRef(nil), step.After...)
	step.OnSuccess = cloneSteps(step.OnSuccess)
	step.Parallel.Steps = cloneSteps(step.Parallel.Steps)
	if step.Loop != nil {
		loop := *step.Loop
		loop.Steps = cloneSteps(loop.Steps)
		loop.Body = cloneSteps(loop.Body)
		step.Loop = &loop
	}
	if step.Foreach != nil {
		fc := cloneForeachConfig(step.Foreach)
		step.Foreach = fc
	}
	if step.ForeachPane != nil {
		fc := cloneForeachConfig(step.ForeachPane)
		step.ForeachPane = fc
	}
	return step
}

func cloneSteps(steps []Step) []Step {
	if len(steps) == 0 {
		return nil
	}
	out := make([]Step, len(steps))
	for i, step := range steps {
		out[i] = cloneStep(step)
	}
	return out
}

func cloneForeachConfig(config *ForeachConfig) *ForeachConfig {
	if config == nil {
		return nil
	}
	clone := *config
	clone.Models = append(StringOrList(nil), config.Models...)
	clone.TemplateParams = cloneInterfaceMap(config.TemplateParams)
	clone.Params = cloneInterfaceMap(config.Params)
	clone.Steps = cloneSteps(config.Steps)
	clone.Body = cloneSteps(config.Body)
	return &clone
}

func cloneInterfaceMap(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneInterfaceValue(value)
	}
	return out
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(v)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = cloneInterfaceValue(item)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}

func substituteForeachInterfaceMap(e *Executor, in map[string]interface{}, protected map[string]struct{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = substituteForeachInterfaceValue(e, value, protected)
	}
	return out
}

func substituteForeachInterfaceValue(e *Executor, value interface{}, protected map[string]struct{}) interface{} {
	switch v := value.(type) {
	case string:
		return e.substituteForeachString(v, protected)
	case map[string]interface{}:
		return substituteForeachInterfaceMap(e, v, protected)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = substituteForeachInterfaceValue(e, item, protected)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = e.substituteForeachString(item, protected)
		}
		return out
	default:
		return value
	}
}

func substituteInterfaceMap(e *Executor, in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = substituteInterfaceValue(e, value)
	}
	return out
}

func substituteInterfaceValue(e *Executor, value interface{}) interface{} {
	switch v := value.(type) {
	case string:
		return e.substituteVariables(v)
	case map[string]interface{}:
		return substituteInterfaceMap(e, v)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = substituteInterfaceValue(e, item)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = e.substituteVariables(item)
		}
		return out
	default:
		return value
	}
}

func foreachStrategyPanes(panes []tmux.Pane) []paneStrategyPane {
	out := make([]paneStrategyPane, 0, len(panes))
	for _, pane := range panes {
		meta := paneMetadataFromTmuxPane(pane)
		excluded, _ := tagBool(pane.Tags, "excluded")
		out = append(out, paneStrategyPane{
			ID:          pane.ID,
			ModelFamily: meta.Model,
			Domains:     meta.Domains,
			Excluded:    excluded,
		})
	}
	return out
}

// snapshotAdjudicatorHistory returns a copy of the chronological list of
// rotate_adjudicator assignments made so far during this run.
func (e *Executor) snapshotAdjudicatorHistory() []string {
	if e == nil {
		return nil
	}
	e.adjudicatorMu.Lock()
	defer e.adjudicatorMu.Unlock()
	if len(e.adjudicatorHistory) == 0 {
		return nil
	}
	return append([]string(nil), e.adjudicatorHistory...)
}

// recordAdjudicator appends paneID to the rotate_adjudicator history. Empty
// IDs are ignored so a failed pane assignment does not poison the history.
func (e *Executor) recordAdjudicator(paneID string) {
	if e == nil || paneID == "" {
		return
	}
	e.adjudicatorMu.Lock()
	defer e.adjudicatorMu.Unlock()
	e.adjudicatorHistory = append(e.adjudicatorHistory, paneID)
}

// resetAdjudicatorHistory clears the rotate_adjudicator history. Called at
// the start of each Run so resumed/repeated invocations of the same Executor
// pick a deterministic first adjudicator instead of inheriting prior runs.
func (e *Executor) resetAdjudicatorHistory() {
	if e == nil {
		return
	}
	e.adjudicatorMu.Lock()
	defer e.adjudicatorMu.Unlock()
	e.adjudicatorHistory = nil
}

// selectForeachPaneWithState dispatches pane assignment with run-scoped
// state. For rotate_adjudicator, it threads the live adjudicator history
// into the strategy and records the chosen pane afterward so subsequent
// debate items receive balanced assignments. All other strategies delegate
// straight to selectForeachPane unchanged.
func (e *Executor) selectForeachPaneWithState(strategy string, strategyPanes []paneStrategyPane, panes []tmux.Pane, item interface{}, iterationIndex int) (string, int, map[string]interface{}, error) {
	if strings.TrimSpace(strategy) == "rotate_adjudicator" {
		history := e.snapshotAdjudicatorHistory()
		paneID, err := rotateAdjudicator(
			foreachPaneIDs(strategyPanes),
			foreachItemStrings(item, "champions", "champion_a", "champion_b"),
			history,
		)
		if err != nil {
			return "", 0, nil, err
		}
		index, vars := paneVarsForID(panes, paneID)
		e.recordAdjudicator(paneID)
		return paneID, index, vars, nil
	}
	return selectForeachPane(strategy, strategyPanes, panes, item, iterationIndex)
}

func selectForeachPane(strategy string, strategyPanes []paneStrategyPane, panes []tmux.Pane, item interface{}, iterationIndex int) (string, int, map[string]interface{}, error) {
	var paneID string
	var err error
	switch strings.TrimSpace(strategy) {
	case "", "round_robin":
		paneID, err = roundRobinPane(strategyPanes, iterationIndex)
	case "round_robin_by_domain":
		paneID, err = roundRobinByDomain(strategyPanes, foreachItemString(item, "domain", "id"), iterationIndex)
	case "rotate_adjudicator":
		paneID, err = rotateAdjudicator(foreachPaneIDs(strategyPanes), foreachItemStrings(item, "champions", "champion_a", "champion_b"), nil)
	case "by_model_family":
		// Normalize through the pane vocabulary so a canonical author family
		// like "cc" routes to a pane spawned with ModelFamily="opus", and a
		// verbose "claude-sonnet-4" routes to either "claude" or a bare
		// variant pane. Without this, byModelFamily exact-compares strings
		// and fails to route across the family/variant boundary.
		raw := foreachItemString(item, "model_family", "model", "family", "type")
		if mapped := mapModelFamilyToPaneVocabulary(raw, strategyPanes); mapped != "" {
			raw = mapped
		}
		paneID, err = byModelFamily(strategyPanes, raw)
	case "by_model_family_difference":
		var warned bool
		paneID, warned, err = byModelFamilyDifference(strategyPanes, foreachAuthorModelFamilyForPanes(item, strategyPanes))
		if warned {
			slog.Warn("foreach pane assignment fell back to first available pane",
				"strategy", strategy,
				"item", item,
			)
		}
	default:
		err = fmt.Errorf("unknown pane_assignment_strategy %q", strategy)
	}
	if err != nil {
		return "", 0, nil, err
	}
	index, vars := paneVarsForID(panes, paneID)
	return paneID, index, vars, nil
}

func foreachPaneIDs(panes []paneStrategyPane) []string {
	ids := make([]string, 0, len(panes))
	for _, pane := range panes {
		if pane.available() {
			ids = append(ids, pane.ID)
		}
	}
	return ids
}

func paneAssignmentFromItem(item interface{}, panes []tmux.Pane) (string, int, map[string]interface{}) {
	paneID := foreachItemString(item, "id", "pane_id")
	if paneID == "" {
		index := foreachItemInt(item, "index", "pane", "ntm_index")
		if index > 0 {
			for _, pane := range panes {
				if pane.Index == index || pane.NTMIndex == index {
					return pane.ID, pane.Index, paneMetadataFromTmuxPane(pane).variableMap()
				}
			}
			return fmt.Sprintf("%%%d", index), index, map[string]interface{}{"id": fmt.Sprintf("%%%d", index), "index": index}
		}
	}
	if paneID != "" {
		index, vars := paneVarsForID(panes, paneID)
		if vars == nil {
			vars = map[string]interface{}{"id": paneID, "pane_id": paneID}
		}
		return paneID, index, vars
	}
	return "", 0, nil
}

func paneVarsForID(panes []tmux.Pane, paneID string) (int, map[string]interface{}) {
	for _, pane := range panes {
		if pane.ID == paneID {
			return pane.Index, paneMetadataFromTmuxPane(pane).variableMap()
		}
	}
	return 0, nil
}

// foreachItemString resolves an item value by trying each candidate key in
// order. Blank values are treated as missing so fallback aliases take effect
// when the canonical key is present-but-empty (bd-rab6y) — e.g.
// {"model_family": "", "model": "codex"} should resolve to "codex" via the
// fallback rather than returning "" and failing routing.
func foreachItemString(item interface{}, keys ...string) string {
	switch v := item.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case map[string]interface{}:
		for _, key := range keys {
			value, ok := v[key]
			if !ok {
				continue
			}
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
				return s
			}
		}
	case map[string]string:
		for _, key := range keys {
			value, ok := v[key]
			if !ok {
				continue
			}
			if s := strings.TrimSpace(value); s != "" {
				return s
			}
		}
	}
	return ""
}

func foreachItemStrings(item interface{}, keys ...string) []string {
	var values []string
	if m, ok := item.(map[string]interface{}); ok {
		for _, key := range keys {
			value, exists := m[key]
			if !exists {
				continue
			}
			switch v := value.(type) {
			case []string:
				values = append(values, v...)
			case []interface{}:
				for _, item := range v {
					if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
						values = append(values, s)
					}
				}
			default:
				if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
					values = append(values, s)
				}
			}
		}
	}
	return values
}

func foreachItemInt(item interface{}, keys ...string) int {
	for _, key := range keys {
		value := ""
		if m, ok := item.(map[string]interface{}); ok {
			if raw, exists := m[key]; exists {
				value = fmt.Sprint(raw)
			}
		}
		if m, ok := item.(map[string]string); ok {
			if raw, exists := m[key]; exists {
				value = raw
			}
		}
		if value == "" {
			continue
		}
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return 0
}
