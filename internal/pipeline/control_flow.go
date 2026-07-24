package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// resolveBranch evaluates the branch predicate and returns the matching key.
// Two modes:
//   - Literal: "fresh-pass" → returns "fresh-pass"
//   - Shell expression: "$(cmd)" → executes cmd, returns trimmed stdout
//
// Variable substitution is applied before evaluation. bd-lwb25: ctx-aware
// so a `branch:` predicate nested inside a foreach max_rounds body sees
// the round overlay from withRoundOverrides instead of falling back to
// the (post-bd-2ubxp.20 unpopulated) state.Variables["round"].
func (e *Executor) resolveBranch(ctx context.Context, step *Step) (string, error) {
	expr := e.substituteVariablesCtx(ctx, step.Branch)

	if strings.HasPrefix(expr, "$(") && strings.HasSuffix(expr, ")") {
		shellCmd := expr[2 : len(expr)-1]
		slog.Info("branch shell predicate executing",
			"run_id", e.state.RunID,
			"workflow", e.state.WorkflowID,
			"step_id", step.ID,
			"agent_type", "branch",
			"command", shellCmd,
		)

		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
		configureCommandProcessGroup(cmd)
		cmd.Cancel = func() error { return cancelCommandProcessGroup(cmd) }
		if e.config.ProjectDir != "" {
			cmd.Dir = e.config.ProjectDir
		}

		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("branch shell command failed: %w", err)
		}

		return strings.TrimSpace(string(out)), nil
	}

	return expr, nil
}

// lookupBranch looks up the key in step.Branches, falling back to "default".
func lookupBranch(branches map[string]interface{}, key string) (interface{}, error) {
	if val, ok := branches[key]; ok {
		return val, nil
	}
	if val, ok := branches["default"]; ok {
		return val, nil
	}
	return nil, fmt.Errorf("branch produced no matching key: %s", key)
}

// parseBranchSteps converts an interface{} branch value into a slice of Steps.
// Handles both single-step maps and lists of step maps via JSON round-trip.
func parseBranchSteps(val interface{}, parentID, branchKey string) ([]Step, error) {
	data, err := json.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal branch value: %w", err)
	}

	// Try list first
	var steps []Step
	if err := json.Unmarshal(data, &steps); err == nil && len(steps) > 0 {
		for i := range steps {
			if steps[i].ID == "" {
				steps[i].ID = fmt.Sprintf("%s.%s.%d", parentID, branchKey, i)
			}
		}
		return steps, nil
	}

	// Try single step
	var single Step
	if err := json.Unmarshal(data, &single); err == nil {
		if single.ID == "" {
			single.ID = fmt.Sprintf("%s.%s.0", parentID, branchKey)
		}
		return []Step{single}, nil
	}

	return nil, fmt.Errorf("branch value for key %q is neither a step nor a list of steps", branchKey)
}

func scopedChildStepID(parentID, childID string, ordinal int) string {
	if parentID == "" {
		if childID != "" {
			return childID
		}
		return fmt.Sprintf("child_%d", ordinal)
	}
	if childID == "" {
		return fmt.Sprintf("%s_child_%d", parentID, ordinal)
	}
	if childID == parentID || strings.HasPrefix(childID, parentID+"_") || strings.HasPrefix(childID, parentID+".") {
		return childID
	}
	return fmt.Sprintf("%s_%s", parentID, childID)
}

// executeBranch resolves the branch predicate, looks up the matching branch,
// parses the branch body into steps, and executes them sequentially.
func (e *Executor) executeBranch(ctx context.Context, step *Step, workflow *Workflow) StepResult {
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	slog.Info("branch step starting",
		"run_id", e.state.RunID,
		"workflow", e.state.WorkflowID,
		"step_id", step.ID,
		"agent_type", "branch",
	)

	// bd-g40ad: sanitize the predicate string before truncating. step.Branch
	// is raw author config today, but the same pattern is applied across
	// every dry-run banner so a future substitution change cannot reopen
	// the operator-terminal hijack vector bd-82zsc / bd-g40ad close.
	if e.config.DryRun {
		result.Status = StatusCompleted
		result.Output = dryRunOutput(step, "Would evaluate branch predicate: "+truncatePrompt(SanitizeDescriptionForTerminal(step.Branch), 80))
		result.FinishedAt = time.Now()
		return result
	}

	key, err := e.resolveBranch(ctx, step)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "branch",
			Message:   fmt.Sprintf("branch predicate failed: %v", err),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		slog.Error("branch predicate failed",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"error", err,
		)
		return result
	}

	branchVal, lookupErr := lookupBranch(step.Branches, key)
	if lookupErr != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "branch",
			Message:   lookupErr.Error(),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		slog.Error("branch lookup failed",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"branch_key", key,
			"error", lookupErr,
		)
		return result
	}

	slog.Info("branch step resolved",
		"run_id", e.state.RunID,
		"step_id", step.ID,
		"branch_key", key,
	)

	branchSteps, parseErr := parseBranchSteps(branchVal, step.ID, key)
	if parseErr != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "branch",
			Message:   fmt.Sprintf("failed to parse branch body: %v", parseErr),
			Timestamp: time.Now(),
		}
		result.FinishedAt = time.Now()
		return result
	}

	// Snapshot variables before the branch body for scoping.
	//
	// The restore is a targeted diff, never a whole-map replacement. A branch is
	// reachable from a foreach body, and `parallel: true` runs those iterations
	// concurrently — so replacing state.Variables with a pre-branch snapshot
	// discarded every output_var and steps.<id>.* key a sibling iteration wrote
	// while this body was running. Ownership is tracked explicitly instead:
	// bodyStepIDs is appended to as the loop below assigns each concrete ID, and
	// ownedOutputVars holds the body's declared output_var names.
	//
	// bd-afwly's on_failure runtime action contract still requires
	// ${runtime.<id>_failure_action} keys set inside the body to survive — they
	// are global signaling, not branch-local data. Those keys are never
	// branch-owned by the rules above, so they survive; the explicit stash below
	// additionally covers a runtime key the body *modified* rather than added.
	var bodyStepIDs []string
	ownedOutputVars := branchBodyOutputVars(branchSteps)
	e.varMu.RLock()
	varSnapshot := captureAllVariables(e.state.Variables)
	e.varMu.RUnlock()
	defer func() {
		e.varMu.Lock()
		runtimePersist := extractRuntimeKeys(e.state.Variables)
		restoreBranchVariables(e.state, varSnapshot, bodyStepIDs, ownedOutputVars)
		mergeRuntimeKeys(e.state, runtimePersist)
		e.varMu.Unlock()
	}()

	// Execute branch steps sequentially
	var outputs []string
	allPassed := true
	for i, bs := range branchSteps {
		select {
		case <-ctx.Done():
			result.Status = StatusCancelled
			result.FinishedAt = time.Now()
			return result
		default:
		}

		slog.Info("branch body step executing",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"branch_key", key,
			"body_step", bs.ID,
			"iteration", i,
		)

		bs.ID = scopedChildStepID(step.ID, bs.ID, i+1)
		// Record the concrete ID so the deferred restore knows which steps.*
		// keys this branch owns. Nested constructs namespace their own children
		// under this ID, so a prefix match covers them too.
		bodyStepIDs = append(bodyStepIDs, bs.ID)
		sr := e.executeStepOnce(ctx, &bs, workflow)

		// bd-afwly: branch body steps now honor the on_failure runtime
		// action / recovery contract used by top-level executeStep, so
		// fallback_to_ntm_inbox / suppress_failure work the same inside
		// a branch as outside it.
		if sr.Status == StatusFailed {
			sr = e.executeOnFailureAction(&bs, sr)
			if sr.Status == StatusFailed {
				sr = e.executeOnFailureRecovery(ctx, &bs, workflow, sr)
			}
		}
		outputs = append(outputs, sr.Output)

		e.stateMu.Lock()
		e.state.Steps[bs.ID] = sr
		e.stateMu.Unlock()

		// bd-2g48y: branch body steps reach this seam via executeStepOnce
		// (not the executeStep retry loop), so the OnSuccess hook at
		// executor.go:847 was never fired for an on_success chain attached
		// to a branch body step. Fire it here on the same Completed
		// contract (matches command-step parents and bd-h8lc4's top-level
		// Parallel/Loop fix). bs.ID is already namespaced via
		// scopedChildStepID above so OnSuccess child results land at
		// <branch.ID>_<branchKey>_<chosenChild>_on_success_<...>.
		if sr.Status == StatusCompleted {
			e.runOnSuccessSteps(ctx, &bs, workflow)
		}

		if sr.Status == StatusFailed || sr.Status == StatusCancelled {
			allPassed = false
			result.Status = sr.Status
			result.Error = sr.Error
			break
		}
	}

	if allPassed {
		result.Status = StatusCompleted
	}
	result.Output = strings.Join(outputs, "\n")
	result.FinishedAt = time.Now()
	return result
}

// executeOnFailureRecovery dispatches a structured on_failure fallback:
//
//	on_failure:
//	  pane: 1
//	  template: recover.md
//	  params: {KEY: value}
//
// The original failed result remains failed unless suppress_failure is true
// and the recovery template dispatch completes successfully.
func (e *Executor) executeOnFailureRecovery(ctx context.Context, step *Step, workflow *Workflow, failed StepResult) StepResult {
	if step == nil || len(step.OnFailure.Fallback) == 0 {
		return failed
	}

	recoveryStep, suppressFailure, err := e.recoveryStepFromFallback(ctx, step)
	if err != nil {
		return e.recordRecoveryFailure(failed, step.ID, err)
	}

	e.stepLogger(step).Info(EventOnFailureFire,
		FieldRecoveryPane, recoveryStep.Pane.Index,
		FieldRecoveryTemplate, recoveryStep.Template,
	)

	recoveryResult := e.executeTemplate(ctx, recoveryStep, workflow)
	e.stateMu.Lock()
	e.state.Steps[recoveryStep.ID] = recoveryResult
	e.stateMu.Unlock()

	if recoveryResult.Status != StatusCompleted {
		return e.recordRecoveryFailure(failed, step.ID, recoveryResultError(recoveryResult))
	}
	if suppressFailure {
		failed.Status = StatusCompleted
		failed.Error = nil
		failed.Output = recoveryResult.Output
		failed.FinishedAt = time.Now()
	}
	return failed
}

// maxOnSuccessDepth caps recursion through Step.OnSuccess chains so a
// pipeline author cannot accidentally write an infinite loop by
// referencing the parent step in its own success branch (bd-w6nth.7).
const maxOnSuccessDepth = 5

type onSuccessDepthKey struct{}

func onSuccessDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(onSuccessDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func withOnSuccessDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, onSuccessDepthKey{}, depth)
}

func onSuccessChildStepID(parentID, childID string, ordinal int) string {
	if parentID == "" {
		if childID != "" {
			return childID
		}
		return fmt.Sprintf("on_success_%d", ordinal)
	}
	if childID == "" {
		return fmt.Sprintf("%s_on_success_%d", parentID, ordinal)
	}
	return fmt.Sprintf("%s_on_success_%s", parentID, childID)
}

// runOnSuccessSteps walks Step.OnSuccess sequentially after a parent
// step has reached StatusCompleted. Each child runs through executeStep
// (so its own OnSuccess chain fires recursively) up to maxOnSuccessDepth
// total. Depth is tracked in the context so recursion through executeStep
// is bounded even though the call site stays simple. Failures inside an
// OnSuccess child are logged but NEVER change the parent's Status —
// they're side-effect dispatches (notifications, hand-offs) rather
// than gating steps (bd-w6nth.7).
func (e *Executor) runOnSuccessSteps(ctx context.Context, parent *Step, workflow *Workflow) {
	if parent == nil || len(parent.OnSuccess) == 0 {
		return
	}
	depth := onSuccessDepth(ctx)
	if depth >= maxOnSuccessDepth {
		slog.Warn("on_success recursion depth limit reached; skipping further chain",
			"run_id", e.state.RunID,
			"workflow", workflow.Name,
			"parent_step_id", parent.ID,
			"depth", depth,
			"max_depth", maxOnSuccessDepth,
		)
		return
	}

	childCtx := withOnSuccessDepth(ctx, depth+1)

	for i := range parent.OnSuccess {
		child := parent.OnSuccess[i]
		child.ID = onSuccessChildStepID(parent.ID, child.ID, i+1)

		result := e.executeStep(childCtx, &child, workflow)
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}

		e.stateMu.Lock()
		e.state.Steps[child.ID] = result
		e.state.UpdatedAt = time.Now()
		if result.Status == StatusFailed && result.Error != nil {
			slog.Warn("on_success step failed",
				"run_id", e.state.RunID,
				"workflow", workflow.Name,
				"parent_step_id", parent.ID,
				"step_id", child.ID,
				"error", result.Error.Message,
			)
		}
		e.stateMu.Unlock()

		if child.OutputVar != "" && result.Status == StatusCompleted {
			e.varMu.Lock()
			e.state.Variables[child.OutputVar] = result.Output
			if result.ParsedData != nil {
				e.state.Variables[child.OutputVar+"_parsed"] = result.ParsedData
			}
			StoreStepOutput(e.state, child.ID, result.Output, result.ParsedData)
			e.varMu.Unlock()
		}
	}
}

// runPostPipelineSteps iterates Workflow.PostPipelineSteps in order after
// the main step graph has finished. Each step runs through the regular
// executeStep machinery so it inherits retries, on_failure, and routing.
// Failures here are persisted into state.Steps but DO NOT change the
// overall pipeline status — post-pipeline steps are for cleanup and
// notification, not gating (bd-w6nth.5).
func (e *Executor) runPostPipelineSteps(ctx context.Context, workflow *Workflow) {
	if workflow == nil || len(workflow.PostPipelineSteps) == 0 {
		return
	}

	for i := range workflow.PostPipelineSteps {
		step := workflow.PostPipelineSteps[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("post_pipeline_%d", i+1)
		}

		e.stateMu.Lock()
		e.state.CurrentStep = step.ID
		e.state.UpdatedAt = time.Now()
		e.stateMu.Unlock()

		result := e.executeStep(ctx, &step, workflow)
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}

		e.stateMu.Lock()
		e.state.Steps[step.ID] = result
		e.state.UpdatedAt = time.Now()
		if result.Status == StatusFailed && result.Error != nil {
			slog.Warn("post_pipeline_step failed",
				"run_id", e.state.RunID,
				"workflow", workflow.Name,
				"step_id", step.ID,
				"error", result.Error.Message,
			)
			e.state.Errors = append(e.state.Errors, ExecutionError{
				StepID:    step.ID,
				Type:      "post_pipeline",
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

func (e *Executor) executeOnFailureAction(step *Step, failed StepResult) StepResult {
	if step == nil {
		return failed
	}
	action := strings.TrimSpace(step.OnFailure.Action)
	if action == "" || knownFailureAction(action) {
		return failed
	}

	key := runtimeFailureActionKey(step.ID)
	variableRef := "runtime." + key

	e.varMu.Lock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}
	e.state.Variables[variableRef] = action
	runtimeVars, _ := e.state.Variables["runtime"].(map[string]interface{})
	if runtimeVars == nil {
		runtimeVars = make(map[string]interface{})
		e.state.Variables["runtime"] = runtimeVars
	}
	runtimeVars[key] = action
	e.varMu.Unlock()

	e.stepLogger(step).Info(EventOnFailureFire,
		FieldRuntimeVariable, variableRef,
		FieldFailureAction, action,
	)

	failed.Status = StatusSkipped
	failed.Error = nil
	failed.SkipReason = fmt.Sprintf("on_failure set ${%s}=%q", variableRef, action)
	failed.SkipKind = SkipKindOnFailureAction
	failed.FinishedAt = time.Now()
	return failed
}

func (e *Executor) substituteRuntimeVariables(s string) string {
	escaped := escapedPattern.ReplaceAllString(s, escapePlaceholder)
	result := varPattern.ReplaceAllStringFunc(escaped, func(match string) string {
		expr := strings.TrimSpace(match[2 : len(match)-1])
		varPath, defaultVal, hasDefault := parseDefault(expr)
		if !strings.HasPrefix(varPath, "runtime.") {
			return match
		}
		key := strings.TrimPrefix(varPath, "runtime.")
		if value, ok := e.runtimeVariable(key); ok {
			return formatValue(value)
		}
		if hasDefault {
			return defaultVal
		}
		return ""
	})
	return strings.ReplaceAll(result, escapePlaceholder, `\${`)
}

func (e *Executor) runtimeVariable(key string) (interface{}, bool) {
	if e.state == nil || e.state.Variables == nil {
		return nil, false
	}
	if value, ok := e.state.Variables["runtime."+key]; ok {
		return value, true
	}
	runtimeVars, ok := e.state.Variables["runtime"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	value, ok := runtimeVars[key]
	return value, ok
}

func runtimeFailureActionKey(stepID string) string {
	return stepID + "_failure_action"
}

func knownFailureAction(action string) bool {
	switch ErrorAction(action) {
	case ErrorActionFail, ErrorActionFailFast, ErrorActionContinue, ErrorActionRetry:
		return true
	default:
		return false
	}
}

// recoveryStepFromFallback constructs a synthetic recovery template step
// from the on_failure.fallback config. bd-lwb25: ctx is threaded through
// every substituteVariables call so a recovery template / pane / wait /
// param value referencing ${round} (or any round-overlay binding) on a
// step nested inside a foreach max_rounds body resolves against the
// per-iteration overlay instead of falling back to the unpopulated
// state.Variables["round"].
func (e *Executor) recoveryStepFromFallback(ctx context.Context, step *Step) (*Step, bool, error) {
	fallback := step.OnFailure.Fallback
	template, ok := recoveryStringValue(fallback["template"])
	if !ok || strings.TrimSpace(template) == "" {
		return nil, false, fmt.Errorf("on_failure recovery requires non-empty template")
	}
	template = e.substituteVariablesCtx(ctx, template)

	pane, err := e.recoveryPaneSpec(ctx, fallback["pane"])
	if err != nil {
		return nil, false, err
	}

	params, err := e.recoveryParams(ctx, fallback["params"])
	if err != nil {
		return nil, false, err
	}

	wait := WaitNone
	if rawWait, ok := recoveryStringValue(fallback["wait"]); ok && strings.TrimSpace(rawWait) != "" {
		wait = WaitCondition(e.substituteVariablesCtx(ctx, rawWait))
	}

	return &Step{
		ID:       step.ID + ".on_failure",
		Name:     "on_failure recovery for " + step.ID,
		Template: template,
		Pane:     pane,
		Params:   params,
		Wait:     wait,
	}, recoveryBoolValue(fallback["suppress_failure"]), nil
}

func (e *Executor) recoveryPaneSpec(ctx context.Context, raw interface{}) (PaneSpec, error) {
	switch v := raw.(type) {
	case int:
		if v > 0 {
			return PaneSpec{Index: v}, nil
		}
	case int64:
		if v > 0 {
			return PaneSpec{Index: int(v)}, nil
		}
	case float64:
		if v > 0 && v == float64(int(v)) {
			return PaneSpec{Index: int(v)}, nil
		}
	case json.Number:
		n, err := v.Int64()
		if err == nil && n > 0 {
			return PaneSpec{Index: int(n)}, nil
		}
	case string:
		resolved := strings.TrimSpace(e.substituteVariablesCtx(ctx, v))
		n, err := strconv.Atoi(resolved)
		if err == nil && n > 0 {
			return PaneSpec{Index: n}, nil
		}
		return PaneSpec{}, fmt.Errorf("on_failure pane %q must resolve to a positive pane index", v)
	}
	return PaneSpec{}, fmt.Errorf("on_failure recovery requires positive pane index")
}

func (e *Executor) recoveryParams(ctx context.Context, raw interface{}) (map[string]interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	params, ok := raw.(map[string]interface{})
	if !ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("on_failure params must be a mapping: %w", err)
		}
		if err := json.Unmarshal(data, &params); err != nil {
			return nil, fmt.Errorf("on_failure params must be a mapping: %w", err)
		}
	}

	resolved := make(map[string]interface{}, len(params))
	for key, val := range params {
		resolved[key] = e.recoveryParamValue(ctx, val)
	}
	return resolved, nil
}

func (e *Executor) recoveryParamValue(ctx context.Context, val interface{}) interface{} {
	switch v := val.(type) {
	case string:
		return e.substituteVariablesCtx(ctx, v)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = e.recoveryParamValue(ctx, item)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = e.recoveryParamValue(ctx, item)
		}
		return out
	default:
		return val
	}
}

func (e *Executor) recordRecoveryFailure(failed StepResult, stepID string, err error) StepResult {
	msg := fmt.Sprintf("on_failure recovery failed: %v", err)
	e.stateMu.Lock()
	e.state.Errors = append(e.state.Errors, ExecutionError{
		StepID:    stepID,
		Type:      "on_failure",
		Message:   msg,
		Timestamp: time.Now(),
		Fatal:     false,
	})
	e.stateMu.Unlock()

	if failed.Error == nil {
		failed.Error = &StepError{
			Type:      "on_failure",
			Message:   msg,
			Timestamp: time.Now(),
		}
		return failed
	}
	if failed.Error.Details != "" {
		failed.Error.Details += "\n"
	}
	failed.Error.Details += msg
	return failed
}

func recoveryResultError(result StepResult) error {
	if result.Error != nil {
		return fmt.Errorf("%s", result.Error.Message)
	}
	return fmt.Errorf("recovery step %s ended with status %s", result.StepID, result.Status)
}

func recoveryStringValue(raw interface{}) (string, bool) {
	s, ok := raw.(string)
	return s, ok
}

func recoveryBoolValue(raw interface{}) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
