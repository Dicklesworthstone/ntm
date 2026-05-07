package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
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
		result.Error = &StepError{
			Type:      "limit",
			Message:   fmt.Sprintf("foreach step %q has %d iterations, exceeding max_foreach_iterations %d", step.ID, len(items), e.limits.MaxForeachIterations),
			Timestamp: time.Now(),
		}
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

	onError := resolveErrorAction(step.OnError, workflow.Settings.OnError)
	var iterations []foreachIterationResult
	if config.Parallel {
		iterations = e.executeForeachIterationsParallel(ctx, step, workflow, plans, onError, foreachMaxConcurrent(config, e.limits))
	} else {
		iterations = e.executeForeachIterationsSequential(ctx, step, workflow, plans, onError)
	}

	result.ParsedData = iterations
	total, dispatched, skipped, failed := countForeachIterations(iterations)
	result.Output = fmt.Sprintf("Foreach completed: %d/%d dispatched, %d skipped, %d failed", dispatched, total, skipped, failed)
	result.FinishedAt = time.Now()
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
		result.Error = &StepError{
			Type:      "cancelled",
			Message:   fmt.Sprintf("foreach step %q cancelled", step.ID),
			Timestamp: time.Now(),
		}
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
				paneID, paneIndex, paneVars, err := selectForeachPane(config.PaneStrategy, strategyPanes, panes, item, i)
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

func (e *Executor) executeForeachIterationsSequential(ctx context.Context, parent *Step, workflow *Workflow, plans []foreachIterationPlan, onError ErrorAction) []foreachIterationResult {
	results := make([]foreachIterationResult, 0, len(plans))
	for i, plan := range plans {
		if plan.Skipped {
			results = append(results, skippedForeachIteration(plan))
			continue
		}
		iterResult := e.executeForeachIteration(ctx, parent, workflow, plan)
		results = append(results, iterResult)
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

func (e *Executor) executeForeachIterationsParallel(ctx context.Context, parent *Step, workflow *Workflow, plans []foreachIterationPlan, onError ErrorAction, maxConcurrent int) []foreachIterationResult {
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
			continue
		}
		wg.Add(1)
		go func(i int, plan foreachIterationPlan) {
			defer wg.Done()
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
			iterResult := e.executeForeachIteration(runCtx, parent, workflow, plan)
			if isBreak() && foreachIterationCancelled(iterResult) {
				iterResult.SkipKind = SkipKindForeachBreak
				iterResult.SkipReason = "loop break"
				iterResult.Error = ""
			}
			results[i] = iterResult
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

func (e *Executor) executeForeachIteration(ctx context.Context, parent *Step, workflow *Workflow, plan foreachIterationPlan) foreachIterationResult {
	iterResult := foreachIterationResult{
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

	for i := range plan.Steps {
		select {
		case <-ctx.Done():
			cancelled := StepResult{
				StepID:     plan.Steps[i].ID,
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
			e.storeForeachNestedResult(&plan.Steps[i], cancelled)
			return iterResult
		default:
		}

		if foreachControlOnlyStep(plan.Steps[i]) {
			control, applies, err := e.foreachLoopControlDecision(plan.Steps[i])
			if err != nil {
				failed := failedForeachLoopControlCondition(plan.Steps[i], err)
				iterResult.Results = append(iterResult.Results, failed)
				e.storeForeachNestedResult(&plan.Steps[i], failed)
				iterResult.Error = resultErrorMessage(failed)
				return iterResult
			}
			if applies {
				iterResult.Control = control
				e.logForeachLoopControl(control, workflow, parent, plan)
				return iterResult
			}
			continue
		}

		step := plan.Steps[i]
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
			if resolveErrorAction(step.OnError, workflow.Settings.OnError) != ErrorActionContinue {
				iterResult.Error = resultErrorMessage(result)
				return iterResult
			}
		}

		if result.Status == StatusCompleted {
			if control, applies := foreachLoopControlValue(step); applies {
				iterResult.Control = control
				e.logForeachLoopControl(control, workflow, parent, plan)
				return iterResult
			}
		}
	}
	return markForeachIterationSkippedIfAllResultsSkipped(iterResult)
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
	maxConcurrent := limits.MaxConcurrentForeach
	if config.MaxConcurrent > 0 {
		maxConcurrent = config.MaxConcurrent
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
		return &StepError{
			Type:      "foreach",
			Message:   fmt.Sprintf("foreach step %q failed at iteration %d: %s", stepID, iteration.Index, message),
			Timestamp: time.Now(),
		}
	}
	return &StepError{
		Type:      "foreach",
		Message:   fmt.Sprintf("foreach step %q failed", stepID),
		Timestamp: time.Now(),
	}
}

func finishForeachFailure(result StepResult, typ, message string) StepResult {
	result.Status = StatusFailed
	result.Error = &StepError{Type: typ, Message: message, Timestamp: time.Now()}
	result.FinishedAt = time.Now()
	return result
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
