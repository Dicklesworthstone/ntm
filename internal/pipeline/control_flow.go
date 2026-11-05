package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
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
	expr, err := e.substituteVariablesStrictCtx(ctx, step.Branch)
	if err != nil {
		return "", fmt.Errorf("branch predicate substitution failed: %w", err)
	}

	if strings.HasPrefix(expr, "$(") && strings.HasSuffix(expr, ")") {
		shellCmd := expr[2 : len(expr)-1]
		slog.Info("branch shell predicate executing",
			"run_id", e.state.RunID,
			"workflow", e.state.WorkflowID,
			"step_id", step.ID,
			"agent_type", "branch",
			"command", shellCmd,
		)

		cmd := exec.Command("/bin/sh", "-c", shellCmd)
		if e.config.ProjectDir != "" {
			cmd.Dir = e.config.ProjectDir
		}

		// A partial key must not select a branch (including its default).
		// Share command-step ownership and enforce the workflow's byte limits.
		limits := e.limits.EffectiveLimits()
		evaluationCtx, cancel := e.selectorEvaluationContext(ctx, step)
		defer cancel()
		out, err := runCommandOutput(evaluationCtx, cmd, limits.MaxCommandStdoutBytes, limits.MaxCommandStderrBytes)
		if err != nil {
			return "", fmt.Errorf("branch shell command failed: %w", err)
		}

		key := strings.TrimSpace(string(out))
		if err := evaluationCtx.Err(); err != nil {
			return "", err
		}
		return key, nil
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
	ctx, releaseMaterializations := withMaterializationOwner(ctx)
	defer releaseMaterializations()
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}
	if e.stopBeforeDispatch(ctx, &result) {
		return result
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

	decision, branchSteps, err := e.prepareBranchDecision(ctx, step)
	if err != nil {
		result.Status = StatusFailed
		result.Error = &StepError{
			Type:      "branch",
			Message:   err.Error(),
			Timestamp: time.Now(),
		}
		if errors.Is(err, errCommandOutputUnsettled) {
			result.Error.Type = "execution_unknown"
		} else if isStepAdmissionError(err) {
			result.Error.Type = "step_limit"
		}
		result.FinishedAt = time.Now()
		slog.Error("branch decision failed",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"error", err,
		)
		return result
	}

	key := decision.SelectorKey
	slog.Info("branch step resolved",
		"run_id", e.state.RunID,
		"step_id", step.ID,
		"branch_key", key,
	)

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
	if varSnapshot == nil && len(decision.Scope) > 0 {
		varSnapshot = make(map[string]interface{})
	}
	for name, saved := range decision.Scope {
		if saved.Exists {
			varSnapshot[name] = cloneInterfaceValue(saved.Value)
		} else {
			delete(varSnapshot, name)
		}
	}
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
		// Preserve the identity fence for every recorded result, including
		// unfinished work that will re-enter the canonical lifecycle.
		e.stateMu.RLock()
		saved, recorded := e.state.Steps[bs.ID]
		e.stateMu.RUnlock()
		if recorded && saved.StepID != bs.ID {
			result.Status = StatusFailed
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "recovery", Message: fmt.Sprintf("branch child %q has mismatched saved result identity %q; refusing replay", bs.ID, saved.StepID), Timestamp: result.FinishedAt}
			return result
		}
		sr, adopted, err := e.adoptBranchChild(ctx, decision, &bs, workflow)
		if err != nil {
			result.Status = StatusFailed
			result.Output = strings.Join(outputs, "\n")
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "branch_recovery", Message: err.Error(), Timestamp: result.FinishedAt}
			if isStepAdmissionError(err) {
				result.Error.Type = "step_limit"
			}
			return result
		}
		if !adopted {
			// Unsettled children retain the canonical retry, cancellation and
			// external-effect receipt rules. Only proven completion is adopted.
			sr = e.executeStep(ctx, &bs, workflow)
		} else {
			slog.Info("branch child result adopted", "run_id", e.state.RunID, "step_id", bs.ID, "status", sr.Status)
		}
		outputs = append(outputs, sr.Output)

		// executeStep has retired this child's in-flight marker. Persist the
		// settled result before another child (or any of its side effects) can
		// begin. A storage fault is fatal even under on_error: continue.
		if err := e.checkpointBranchChild(ctx, step.ID, &bs, sr); err != nil {
			result.Output = strings.Join(outputs, "\n")
			result.Status = StatusFailed
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "checkpoint", Message: err.Error(), Timestamp: result.FinishedAt}
			return result
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
	// Even a nonsuppressing fallback creates owned child evidence that can
	// make an unresolved selector ineligible for explicit acceptance.
	if e.hasUnresolvedCommandExecution(step) || e.hasUnresolvedCommandEvaluation(step) {
		return failed
	}
	if delivery, exists := e.loadAgentDelivery(onFailureStepID(step.ID)); exists && delivery.Status != agentDeliveryNotSent {
		// An accepted child cannot suppress a failure caused by changing its
		// parent or its own materialized definition during recovery.
		if err := e.validateOnFailureDelivery(ctx, step, delivery); err != nil {
			return e.recordRecoveryFailure(failed, step.ID, err)
		}
	}
	if err := e.admitRuntimeIDs(ctx, []string{onFailureStepID(step.ID)}); err != nil {
		return e.recordRecoveryFailure(failed, step.ID, err)
	}
	release, err := e.acquireStepMaterialization(ctx, 1)
	if err != nil {
		return e.recordRecoveryFailure(failed, step.ID, err)
	}
	defer release()

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

var errStepProcessUnsettled = errors.New("step process settlement is uncertain")

var errSuccessHookRecovery = errors.New("success hook recovery evidence is incomplete")

const successHookStateVersion = 1

// The body and each consumed child are checkpointed before the next hook can
// run. The full parent definition binds the ordered hook identities; result
// hashes bind output aliases to the outcomes that actually produced them.
type SuccessHookState struct {
	Version        int                                   `json:"version"`
	StepID         string                                `json:"step_id"`
	StepHash       string                                `json:"step_hash"`
	DefinitionHash string                                `json:"definition_hash"`
	Kind           string                                `json:"kind"`
	Body           StepResult                            `json:"body"`
	BodyHash       string                                `json:"body_hash"`
	Outputs        map[string]interface{}                `json:"outputs,omitempty"`
	HookIDs        []string                              `json:"hook_ids"`
	Children       map[string]SuccessHookChildCheckpoint `json:"children,omitempty"`
}

type SuccessHookChildCheckpoint struct {
	ResultHash string                 `json:"result_hash"`
	Outputs    map[string]interface{} `json:"outputs,omitempty"`
	Background bool                   `json:"background,omitempty"`
	// A consumed WaitNone launch can deliberately restart on resume. An
	// interrupted restart must keep later consumed siblings without pretending
	// that the sidecar's replacement attempt has reached a settled outcome.
	Restarting bool `json:"restarting,omitempty"`
}

func successHooksSettled(record SuccessHookState) bool {
	if len(record.Children) != len(record.HookIDs) {
		return false
	}
	for _, receipt := range record.Children {
		if receipt.Restarting {
			return false
		}
	}
	return true
}

func successHookSidecarRerun(state *ExecutionState, id string, receipt SuccessHookChildCheckpoint) bool {
	if !receipt.Background {
		return false
	}
	command := state.CommandExecutions[id]
	return receipt.Restarting || state.Steps[id].RerunOnResume ||
		(command.Background && command.Status == commandExecutionSettled && command.Result.RerunOnResume)
}

func cloneSuccessHookResult(result StepResult) StepResult {
	result.ParsedData = cloneInterfaceValue(result.ParsedData)
	if result.Error != nil {
		copy := cloneStepErrorValue(*result.Error)
		result.Error = &copy
	}
	return result
}

func cloneSuccessHookOutputs(outputs map[string]interface{}) map[string]interface{} {
	if outputs == nil {
		return nil
	}
	copy := make(map[string]interface{}, len(outputs))
	for name, value := range outputs {
		copy[name] = cloneInterfaceValue(value)
	}
	return copy
}

func cloneSuccessHookState(record SuccessHookState) SuccessHookState {
	record.Body = cloneSuccessHookResult(record.Body)
	record.Outputs = cloneSuccessHookOutputs(record.Outputs)
	record.HookIDs = append([]string(nil), record.HookIDs...)
	if record.Children != nil {
		children := make(map[string]SuccessHookChildCheckpoint, len(record.Children))
		for id, receipt := range record.Children {
			receipt.Outputs = cloneSuccessHookOutputs(receipt.Outputs)
			children[id] = receipt
		}
		record.Children = children
	}
	return record
}

func successHookResultHash(result StepResult, outputs map[string]interface{}, background bool) (string, error) {
	if background {
		// Cancellation cleanup alone may set this bit after a WaitNone
		// command returned. Its launch receipt still owns process settlement
		// and authorizes the deliberate sidecar relaunch on resume.
		result.RerunOnResume = false
	}
	return parallelCheckpointHash(struct {
		Result     StepResult             `json:"result"`
		Outputs    map[string]interface{} `json:"outputs,omitempty"`
		Background bool                   `json:"background,omitempty"`
	}{result, outputs, background})
}

func successHookRecoveryError(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", errSuccessHookRecovery, fmt.Sprintf(format, args...))
}

// Structural checks also run at SaveState/LoadState, before the runtime
// definition is available. In-flight markers are checked at recovery, not
// while a sibling is taking a legitimate live execution snapshot.
func validateSuccessHookState(id string, record SuccessHookState, state *ExecutionState) error {
	if record.Version != successHookStateVersion || record.StepID != id || id == "" ||
		!validCommandEvaluationHash(record.StepHash) || !validCommandEvaluationHash(record.DefinitionHash) || record.Kind == "" || len(record.HookIDs) == 0 ||
		record.Body.StepID != id || record.Body.Status != StatusCompleted || unsettledSuccessHookError(record.Body.Error) != nil ||
		record.Body.StartedAt.IsZero() || record.Body.FinishedAt.IsZero() || record.Body.FinishedAt.Before(record.Body.StartedAt) {
		return successHookRecoveryError("invalid completed body checkpoint for %q", id)
	}
	digest, err := successHookResultHash(record.Body, record.Outputs, false)
	if err != nil || digest != record.BodyHash {
		return successHookRecoveryError("completed body %q differs from its checkpoint", id)
	}
	seen := make(map[string]bool, len(record.HookIDs))
	for i, childID := range record.HookIDs {
		if childID == "" || seen[childID] || !strings.HasPrefix(childID, id+"_on_success_") {
			return successHookRecoveryError("invalid hook identity %q for %q", childID, id)
		}
		seen[childID] = true
		receipt, consumed := record.Children[childID]
		if consumed != (i < len(record.Children)) {
			return successHookRecoveryError("hook consumption for %q is not an ordered prefix", id)
		}
		if !consumed {
			continue
		}
		result, exists := state.Steps[childID]
		if !exists || result.StepID != childID || result.SuccessHookParent != id || result.FinishedAt.IsZero() ||
			(result.Status != StatusCompleted && result.Status != StatusFailed && result.Status != StatusSkipped && !(receipt.Restarting && result.Status == StatusCancelled)) ||
			(receipt.Restarting && !receipt.Background) ||
			(!receipt.Background && result.RerunOnResume) ||
			(result.Status == StatusSkipped && shouldRerunStep(result)) {
			return successHookRecoveryError("consumed hook %q has lost its settled result", childID)
		}
		digest, err := successHookResultHash(result, receipt.Outputs, receipt.Background)
		if err != nil || digest != receipt.ResultHash {
			return successHookRecoveryError("consumed hook %q differs from its checkpoint", childID)
		}
	}
	if len(record.Children) > len(record.HookIDs) {
		return successHookRecoveryError("%q has consumption evidence for an unknown hook", id)
	}
	return nil
}

func validateSuccessHooks(state *ExecutionState) error {
	if state == nil {
		return nil
	}
	for id, result := range state.Steps {
		if result.SuccessHookParent == "" {
			continue
		}
		parent, exists := state.SuccessHooks[result.SuccessHookParent]
		if _, consumed := parent.Children[id]; !exists || !consumed {
			return successHookRecoveryError("consumed hook %q has lost its parent journal %q", id, result.SuccessHookParent)
		}
	}
	ids := make([]string, 0, len(state.SuccessHooks))
	for id := range state.SuccessHooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := validateSuccessHookState(id, state.SuccessHooks[id], state); err != nil {
			return err
		}
	}
	return nil
}

func validateSuccessHookDefinition(step *Step, record SuccessHookState) error {
	digest, err := commandDefinitionHash(step)
	if err != nil {
		return err
	}
	if record.StepID != step.ID || record.Kind != stepKind(step) || record.StepHash != digest || len(record.HookIDs) != len(step.OnSuccess) {
		return successHookRecoveryError("step %q definition changed after its body completed; restore the original workflow or explicitly reset the run", step.ID)
	}
	for i, child := range step.OnSuccess {
		id := onSuccessChildStepID(step.ID, child.ID, i+1)
		if record.HookIDs[i] != id {
			return successHookRecoveryError("step %q hook identities changed after completion", step.ID)
		}
		if receipt, exists := record.Children[id]; exists {
			if receipt.Background != (child.Command != "" && child.Wait == WaitNone) {
				return successHookRecoveryError("hook %q changed its background execution kind", id)
			}
			if err := validateSuccessHookOutputs(&child, receipt.Outputs); err != nil {
				return err
			}
		}
	}
	return validateSuccessHookOutputs(step, record.Outputs)
}

// The hook journal is affirmative completion evidence, including for an
// adopted legacy body. Any native receipt that remains must agree with it;
// adopting the body must not hide an unresolved or contradictory source.
// Caller holds stateMu.
func validateSuccessHookNativeBody(state *ExecutionState, step *Step, record SuccessHookState) error {
	invalid := func() error {
		return successHookRecoveryError("completed body %q contradicts its native execution receipt", record.StepID)
	}
	switch record.Kind {
	case StepKindCommand:
		if source, exists := state.CommandExecutions[record.StepID]; exists {
			if err := validateCommandExecution(record.StepID, source); err != nil {
				return err
			}
			recovered := false
			if source.Result.Status == StatusFailed && recoveryBoolValue(step.OnFailure.Fallback["suppress_failure"]) {
				if fallback, found := state.AgentDeliveries[onFailureStepID(record.StepID)]; found {
					if err := validateAgentDelivery(fallback.StepID, fallback, state.Session); err != nil {
						return err
					}
					if fallback.ResolutionAction != "" {
						if _, err := agentDeliveryRecoveryEntry(state, fallback); err != nil {
							return err
						}
					}
					recovered = fallback.StepID == onFailureStepID(record.StepID) && fallback.Kind == StepKindTemplate &&
						fallback.Status == agentDeliveryCompleted && !source.StartedAt.After(fallback.StartedAt) &&
						fallback.Output == record.Body.Output
				}
			}
			if source.StepHash != record.StepHash || source.Background != (step.Wait == WaitNone) ||
				(!source.Background && (source.Status != commandExecutionSettled || (source.Result.Status != StatusCompleted && !recovered))) {
				return invalid()
			}
			if source.ResolutionAction != "" {
				if _, err := commandExecutionRecoveryEntry(state, source); err != nil {
					return err
				}
			}
		}
	case StepKindPrompt, StepKindTemplate:
		if source, exists := state.AgentDeliveries[record.StepID]; exists {
			if err := validateAgentDelivery(record.StepID, source, state.Session); err != nil {
				return err
			}
			if source.StepHash != record.StepHash || source.Kind != record.Kind || source.Status != agentDeliveryCompleted ||
				source.PaneID != record.Body.PaneUsed || source.AgentType != record.Body.AgentType || source.Output != record.Body.Output {
				return invalid()
			}
			if source.ResolutionAction != "" {
				if _, err := agentDeliveryRecoveryEntry(state, source); err != nil {
					return err
				}
			}
		}
	case StepKindMailSend:
		if source, exists := state.MailSends[record.StepID]; exists {
			if err := validateMailSend(record.StepID, source); err != nil {
				return err
			}
			if source.StepHash != record.StepHash || source.Status != mailSendSent || source.Result.Status != StatusCompleted {
				return invalid()
			}
		}
	case StepKindReservationAcquire, StepKindReservationRelease:
		if source, exists := state.ReservationSteps[record.StepID]; exists {
			if err := validateReservationStep(record.StepID, source); err != nil {
				return err
			}
			if source.StepHash != record.StepHash || source.Kind != record.Kind || source.Result.Status != StatusCompleted ||
				(source.Status != reservationHeld && source.Status != reservationReleased) {
				return invalid()
			}
		}
	}
	return nil
}

// The authored identity is checked even for completed dynamic tails that will
// not be materialized again. The materialized identity remains a separate,
// stricter check before a pending body can be adopted in its original scope.
// Caller holds stateMu when state belongs to a live executor.
func successHookAuthoredHash(graph *DependencyGraph, state *ExecutionState, step *Step) (string, error) {
	if graph == nil {
		return commandDefinitionHash(step)
	}
	resolve, err := successHookRuntimeStepResolver(graph, state)
	if err != nil {
		return "", err
	}
	authored, found := resolve(step.ID)
	if !found {
		return "", successHookRecoveryError("parent %q has no authored workflow definition", step.ID)
	}
	definition := cloneStep(*authored)
	definition.ID = step.ID
	return commandDefinitionHash(&definition)
}

func successHookRuntimeStepResolver(graph *DependencyGraph, state *ExecutionState) (func(string) (*Step, bool), error) {
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return nil, err
	}
	return func(id string) (*Step, bool) {
		if step, found := resolve(id); found {
			return step, true
		}
		// Cleanup hooks are intentionally absent from main scheduling, but
		// own the same durable success-tail contract until the next attempt
		// explicitly resets their settled cleanup scope.
		if graph != nil {
			for i := range graph.onCancelSteps {
				if step, found := resolveHookRuntimeStep(&graph.onCancelSteps[i], "on_cancel", i+1, id); found {
					return step, true
				}
			}
		}
		return nil, false
	}, nil
}

func validateSuccessHookOutputs(step *Step, outputs map[string]interface{}) error {
	for name := range outputs {
		if step.OutputVar == "" || (name != step.OutputVar && name != step.OutputVar+"_parsed") {
			return successHookRecoveryError("step %q contains foreign output alias %q", step.ID, name)
		}
	}
	return nil
}

// Caller holds varMu. Foreach aggregates preserve their declared collection
// shape instead of replacing it with the step's diagnostic output string.
func successHookOutputBindings(state *ExecutionState, step *Step, result StepResult) map[string]interface{} {
	outputs := make(map[string]interface{})
	if result.Status != StatusCompleted || step.OutputVar == "" {
		return outputs
	}
	if stepOwnsForeachOutputVar(step) {
		return snapshotForeachOutputBindings(state, step.OutputVar)
	}
	outputs[step.OutputVar] = result.Output
	if result.ParsedData != nil {
		outputs[step.OutputVar+"_parsed"] = cloneInterfaceValue(result.ParsedData)
	}
	return outputs
}

// Caller holds varMu. Rebuilding aliases is deliberately shared by body and
// child adoption so later hooks observe the same values after a restart.
func restoreSuccessHookOutputs(state *ExecutionState, step *Step, result StepResult, outputs map[string]interface{}) {
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}
	if result.Status != StatusCompleted {
		return
	}
	StoreStepOutput(state, step.ID, result.Output, cloneInterfaceValue(result.ParsedData))
	if step.OutputVar != "" {
		delete(state.Variables, step.OutputVar)
		delete(state.Variables, step.OutputVar+"_parsed")
	}
	for name, value := range outputs {
		state.Variables[name] = cloneInterfaceValue(value)
	}
}

func (e *Executor) prepareSuccessHooks(ctx context.Context, parent *Step, body StepResult) error {
	if parent == nil || len(parent.OnSuccess) == 0 || onSuccessDepth(ctx) >= maxOnSuccessDepth {
		return nil
	}
	if body.StepID != parent.ID || body.Status != StatusCompleted {
		return successHookRecoveryError("cannot prepare hooks without the completed body of %q", parent.ID)
	}
	if cause := unsettledSuccessHookError(body.Error); cause != nil {
		return successHookRecoveryError("body %q still has unfinished recovery: %s", parent.ID, cause.Message)
	}
	digest, err := commandDefinitionHash(parent)
	if err != nil {
		return err
	}
	e.stateMu.Lock()
	e.varMu.Lock()
	record, exists := e.state.SuccessHooks[parent.ID]
	authoredHash, authoredErr := successHookAuthoredHash(e.graph, e.state, parent)
	if authoredErr != nil {
		e.varMu.Unlock()
		e.stateMu.Unlock()
		return authoredErr
	}
	if exists {
		err = validateSuccessHookState(parent.ID, record, e.state)
		if err == nil && record.DefinitionHash != authoredHash {
			err = successHookRecoveryError("step %q authored definition changed after its body completed", parent.ID)
		}
		if err == nil {
			err = validateSuccessHookDefinition(parent, record)
		}
	} else {
		record = SuccessHookState{Version: successHookStateVersion, StepID: parent.ID, StepHash: digest, DefinitionHash: authoredHash, Kind: stepKind(parent), Children: make(map[string]SuccessHookChildCheckpoint)}
		for i, child := range parent.OnSuccess {
			record.HookIDs = append(record.HookIDs, onSuccessChildStepID(parent.ID, child.ID, i+1))
		}
	}
	if err == nil && (!exists || (parent.Command != "" && parent.Wait == WaitNone)) {
		// A sidecar intentionally restarts with each eligible attempt. Its
		// native journal still gates relaunch, while consumed hooks survive.
		record.Body = cloneSuccessHookResult(body)
		record.Outputs = make(map[string]interface{})
		if stepOwnsForeachOutputVar(parent) && parent.OutputVar != "" {
			record.Outputs = successHookOutputBindings(e.state, parent, body)
			if capture, ok := ctx.Value(parallelOutputCaptureKey{}).(*parallelOutputCapture); ok && capture.stepID == parent.ID && capture.outputs != nil {
				record.Outputs = cloneSuccessHookOutputs(capture.outputs)
			}
		}
		record.BodyHash, err = successHookResultHash(record.Body, record.Outputs, false)
		if err == nil {
			if e.state.SuccessHooks == nil {
				e.state.SuccessHooks = make(map[string]SuccessHookState)
			}
			e.state.SuccessHooks[parent.ID] = record
			e.state.UpdatedAt = time.Now()
		}
	}
	e.varMu.Unlock()
	e.stateMu.Unlock()
	if err != nil {
		return err
	}
	// Cancellation after an observed completion does not erase the body.
	// This checkpoint has no external effect and must precede any hook.
	return e.persistState()
}

func (e *Executor) loadSuccessHookBody(step *Step) (StepResult, bool, error) {
	e.stateMu.RLock()
	record, exists := e.state.SuccessHooks[step.ID]
	if !exists {
		e.stateMu.RUnlock()
		return StepResult{}, false, nil
	}
	err := validateSuccessHookState(step.ID, record, e.state)
	if err == nil {
		var digest string
		digest, err = successHookAuthoredHash(e.graph, e.state, step)
		if err == nil && digest != record.DefinitionHash {
			err = successHookRecoveryError("step %q authored definition changed after its body completed", step.ID)
		}
	}
	if err == nil {
		err = validateSuccessHookDefinition(step, record)
	}
	if err == nil {
		err = validateSuccessHookNativeBody(e.state, step, record)
	}
	record = cloneSuccessHookState(record)
	e.stateMu.RUnlock()
	if err != nil {
		return StepResult{}, true, err
	}
	if step.Command != "" && step.Wait == WaitNone {
		return StepResult{}, false, nil
	}
	e.varMu.Lock()
	definition := *step
	if !stepOwnsForeachOutputVar(step) {
		// The containing scheduler publishes an ordinary body's named alias
		// after its hooks. Preserve that same visibility on resumed attempts.
		definition.OutputVar = ""
	}
	restoreSuccessHookOutputs(e.state, &definition, record.Body, record.Outputs)
	e.varMu.Unlock()
	return record.Body, true, nil
}

// Before pruning resume state, validate the complete consumption chain and
// identify every ancestor whose scheduler must reach an unfinished tail.
// Consumed failures remain inspectable but never become successful outputs.
// Caller holds stateMu; returned maps and output values do not alias state.
func prepareSuccessHookResume(graph *DependencyGraph, state *ExecutionState) (map[string]bool, map[string]bool, []resumeOutputEvent, error) {
	pending, consumed := make(map[string]bool), make(map[string]bool)
	var events []resumeOutputEvent
	fail := func(err error) (map[string]bool, map[string]bool, []resumeOutputEvent, error) {
		return nil, nil, nil, err
	}
	if err := validateSuccessHooks(state); err != nil {
		return fail(err)
	}
	if state == nil || len(state.SuccessHooks) == 0 {
		return pending, consumed, events, nil
	}
	resolve, err := successHookRuntimeStepResolver(graph, state)
	if err != nil {
		return fail(err)
	}
	ids := make([]string, 0, len(state.SuccessHooks))
	for id := range state.SuccessHooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := state.SuccessHooks[id]
		step, found := resolve(id)
		if !found {
			return fail(successHookRecoveryError("parent %q is missing from the workflow", id))
		}
		definition := cloneStep(*step)
		definition.ID = id
		authoredHash, err := commandDefinitionHash(&definition)
		if err != nil {
			return fail(err)
		}
		if authoredHash != record.DefinitionHash {
			return fail(successHookRecoveryError("parent %q authored definition changed after its body completed", id))
		}
		if err := validateSuccessHookNativeBody(state, &definition, record); err != nil {
			return fail(err)
		}
		if !successHookDynamicDefinition(state, id) {
			if err := validateSuccessHookDefinition(&definition, record); err != nil {
				return fail(err)
			}
		} else if len(record.HookIDs) != len(definition.OnSuccess) {
			return fail(successHookRecoveryError("dynamic parent %q changed its success-hook shape", id))
		}
		current, exists := state.Steps[id]
		if !exists || shouldRerunStep(current) || !successHooksSettled(record) {
			pending[id] = true
		}
		if command, exists := state.CommandExecutions[id]; exists && command.Background && command.Status == commandExecutionSettled && command.Result.RerunOnResume {
			pending[id] = true
		}
		if _, inFlight := state.InFlightSteps[id]; inFlight {
			pending[id] = true
		}
		events = append(events, successHookOutputEvent(id, record.Body, record.Outputs))
		for i, childID := range record.HookIDs {
			receipt, recorded := record.Children[childID]
			if !recorded {
				break // Structural validation requires an ordered prefix.
			}
			if _, inFlight := state.InFlightSteps[childID]; inFlight {
				return fail(successHookRecoveryError("consumed hook %q has contradictory in-flight evidence", childID))
			}
			childDefinition := definition.OnSuccess[i]
			childDefinition.ID = childID
			if !receipt.Restarting && successHookHasPendingEffects(state, &childDefinition) {
				return fail(successHookRecoveryError("consumed hook %q contradicts pending native execution evidence", childID))
			}
			child := state.Steps[childID]
			if nested, exists := state.SuccessHooks[childID]; exists && !successHooksSettled(nested) && !receipt.Restarting {
				return fail(successHookRecoveryError("consumed hook %q has an unfinished nested tail", childID))
			}
			consumed[childID] = true
			if successHookSidecarRerun(state, childID, receipt) {
				pending[id] = true
			}
			if child.Status == StatusCompleted {
				events = append(events, successHookOutputEvent(childID, child, receipt.Outputs))
			}
		}
	}

	// A body can be nested in a branch, parallel group, loop, or another
	// hook. An already-completed ancestor must not hide its pending tail.
	candidates := make(map[string]*Step)
	for id := range state.Steps {
		if step, found := resolve(id); found {
			candidates[id] = step
		}
	}
	if graph != nil {
		for id, step := range graph.steps {
			candidates[id] = step
		}
	}
	for changed := true; changed; {
		changed = false
		for parentID, parent := range candidates {
			if pending[parentID] || consumed[parentID] {
				continue
			}
			for childID := range pending {
				if _, _, owns := resolveScopedRuntimeChildren(parentID, childID, parent); owns {
					pending[parentID], changed = true, true
					break
				}
			}
		}
	}
	return pending, consumed, events, nil
}

func successHookDynamicDefinition(state *ExecutionState, id string) bool {
	for parentID := range state.ForeachState {
		if iterationIndexFromID(id, parentID+"_iter") >= 0 {
			return true
		}
	}
	return false
}

func successHookOutputEvent(id string, result StepResult, outputs map[string]interface{}) resumeOutputEvent {
	event := resumeOutputEvent{stepID: id, finishedAt: result.FinishedAt,
		output: result.Output, parsedData: cloneInterfaceValue(result.ParsedData)}
	if len(outputs) > 0 {
		event.merge = true
		event.outputs = cloneSuccessHookOutputs(outputs)
		event.outputs["steps."+id+".output"] = result.Output
		event.clear = append(event.clear, "steps."+id+".data")
		if result.ParsedData != nil {
			event.outputs["steps."+id+".data"] = cloneInterfaceValue(result.ParsedData)
		}
		for name := range outputs {
			event.clear = append(event.clear, name)
			// A string-only output must also remove a previous producer's
			// parsed alias. Captured bindings contain the named value and,
			// optionally, its _parsed companion; do not clear unrelated aliases.
			base := strings.TrimSuffix(name, "_parsed")
			if _, companion := outputs[base]; base == name || !companion {
				event.clear = append(event.clear, name+"_parsed")
			}
		}
		sort.Strings(event.clear)
	}
	return event
}

func unsettledProcessStepFailure(result StepResult, err error) StepResult {
	result.Status, result.FinishedAt = StatusFailed, time.Now()
	diagnostic := &StepError{Type: "execution_unknown", Message: err.Error(), Timestamp: result.FinishedAt}
	if result.Error != nil {
		diagnostic.Aggregated = []StepError{cloneStepErrorValue(*result.Error)}
	}
	result.Error = diagnostic
	return result
}

func successHookExecutionFailure(result StepResult, err error) StepResult {
	if isStepAdmissionError(err) {
		return stepAdmissionExecutionFailure(result, err)
	}
	if errors.Is(err, errStepProcessUnsettled) {
		return unsettledProcessStepFailure(result, err)
	}
	result.Status, result.FinishedAt = StatusFailed, time.Now()
	kind := "recovery"
	switch {
	case errors.Is(err, ErrCheckpointFailed):
		kind = "checkpoint"
	case errors.Is(err, errSuccessHookRecovery):
		kind = "recovery"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		result.Status = StatusCancelled
		result.SkipKind, result.SkipReason = SkipKindCancelled, err.Error()
		kind = "cancelled"
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
	}
	diagnostic := &StepError{Type: kind, Message: err.Error(), Timestamp: result.FinishedAt}
	if result.Error != nil {
		diagnostic.Aggregated = []StepError{cloneStepErrorValue(*result.Error)}
	}
	result.Error = diagnostic
	return result
}

// runOnSuccessSteps walks Step.OnSuccess sequentially after a parent
// step has reached StatusCompleted. Each child runs through executeStep
// (so its own OnSuccess chain fires recursively) up to maxOnSuccessDepth
// total. Depth is tracked in the context so recursion through executeStep
// is bounded even though the call site stays simple. Ordinary child failures
// are logged without changing the parent's status (bd-w6nth.7). Capacity
// refusal or uncertain process settlement leaves the parent failed with its
// native receipt intact so a later attempt can finish pending hooks without
// repeating the parent effect.
func (e *Executor) runOnSuccessSteps(ctx context.Context, parent *Step, workflow *Workflow) error {
	if parent == nil || len(parent.OnSuccess) == 0 {
		return nil
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
		return nil
	}

	childCtx := withOnSuccessDepth(ctx, depth+1)
	for i := range parent.OnSuccess {
		if err := e.stepAdmissionFailure(ctx); err != nil {
			return err
		}
		if err := e.checkpointFailure(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		child := parent.OnSuccess[i]
		child.ID = onSuccessChildStepID(parent.ID, child.ID, i+1)
		e.stateMu.RLock()
		record, prepared := e.state.SuccessHooks[parent.ID]
		var err error
		if !prepared {
			err = successHookRecoveryError("step %q has no completed-body checkpoint", parent.ID)
		} else {
			err = validateSuccessHookState(parent.ID, record, e.state)
			if err == nil {
				err = validateSuccessHookDefinition(parent, record)
			}
		}
		receipt, consumed := record.Children[child.ID]
		restartSidecar := consumed && successHookSidecarRerun(e.state, child.ID, receipt)
		prior, exists := e.state.Steps[child.ID]
		prior = cloneSuccessHookResult(prior)
		_, inFlight := e.state.InFlightSteps[child.ID]
		e.stateMu.RUnlock()
		if err != nil {
			return err
		}
		if exists && prior.StepID != child.ID {
			return successHookRecoveryError("hook %q has a mismatched saved result", child.ID)
		}
		if consumed && !restartSidecar {
			if inFlight {
				return successHookRecoveryError("consumed hook %q has contradictory in-flight evidence", child.ID)
			}
			e.varMu.Lock()
			restoreSuccessHookOutputs(e.state, &child, prior, receipt.Outputs)
			e.varMu.Unlock()
			continue
		}

		var result StepResult
		if exists && !shouldRerunStep(prior) && !restartSidecar {
			if inFlight {
				return successHookRecoveryError("completed hook %q still has in-flight evidence", child.ID)
			}
			result = prior
			if result.Status == StatusCompleted {
				// Older checkpoints may retain a completed hook body without a
				// settled tail. Enter the same walker recursively; never rerun
				// the completed body merely to reach a nested pending hook.
				if err := e.prepareSuccessHooks(childCtx, &child, result); err != nil {
					return err
				}
				if err := e.runOnSuccessSteps(childCtx, &child, workflow); err != nil {
					return err
				}
			}
		} else {
			result = e.executeStep(childCtx, &child, workflow)
		}
		pendingErr := e.successHookPendingError(ctx, &child, result)
		if !e.runtimeIDAdmitted(child.ID) {
			if pendingErr != nil {
				return pendingErr
			}
			return successHookRecoveryError("hook %q was not admitted", child.ID)
		}
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}
		if errors.Is(pendingErr, errStepProcessUnsettled) {
			result = unsettledProcessStepFailure(result, pendingErr)
		}
		if err := e.checkpointSuccessHook(parent, &child, result, pendingErr == nil); err != nil {
			return errors.Join(pendingErr, err)
		}
		if result.Status == StatusFailed && result.Error != nil {
			slog.Warn("on_success step failed", "run_id", e.state.RunID,
				"workflow", workflow.Name, "parent_step_id", parent.ID,
				"step_id", child.ID, "error", result.Error.Message)
		}
		if pendingErr != nil {
			return pendingErr
		}
	}
	if err := e.stepAdmissionFailure(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

// A returned ordinary failure is advisory. Unfinished orchestration or an
// uncertain external effect is not an advisory outcome and cannot advance
// the parent's consumed prefix, even when a sibling also cancelled this run.
func (e *Executor) successHookPendingError(ctx context.Context, child *Step, result StepResult) error {
	if cause := findStepAdmissionError(result.Error); cause != nil {
		return &stepAdmissionPropagationError{message: fmt.Sprintf("success hook %q exceeded capacity: %s", child.ID, cause.Message)}
	}
	if e.hasUnresolvedCommandExecutionForOutcome(child, &result) || e.hasUnresolvedCommandEvaluation(child) {
		return fmt.Errorf("%w for success hook %q", errStepProcessUnsettled, child.ID)
	}
	if err := e.checkpointFailure(); err != nil {
		return err
	}
	if err := e.reservationProtectionFailure(); err != nil {
		return err
	}
	e.stateMu.RLock()
	pending := successHookHasPendingEffects(e.state, child)
	if nested, exists := e.state.SuccessHooks[child.ID]; exists && !successHooksSettled(nested) {
		pending = true
	}
	e.stateMu.RUnlock()
	if pending {
		if result.Status == StatusCancelled {
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		return successHookRecoveryError("hook %q still has pending external work or success hooks", child.ID)
	}
	if result.Status == StatusCancelled {
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.Canceled
	}
	if cause := unsettledSuccessHookError(result.Error); cause != nil {
		return successHookRecoveryError("hook %q has unfinished recovery: %s", child.ID, cause.Message)
	}
	switch result.Status {
	case StatusCompleted, StatusFailed:
		return nil
	case StatusSkipped:
		if !shouldRerunStep(result) {
			return nil
		}
	}
	return successHookRecoveryError("hook %q did not reach a consumable outcome", child.ID)
}

func unsettledSuccessHookError(err *StepError) *StepError {
	if err == nil {
		return nil
	}
	switch err.Type {
	case "checkpoint", "recovery", "branch_recovery", "execution_unknown", "delivery_unknown", "reservation_protection", "step_limit", "cancelled":
		return err
	}
	for i := range err.Aggregated {
		if cause := unsettledSuccessHookError(&err.Aggregated[i]); cause != nil {
			return cause
		}
	}
	return nil
}

// Caller holds stateMu. Structural ownership includes nested fallbacks and
// containers, without treating a merely similar authored ID as a descendant.
func successHookHasPendingEffects(state *ExecutionState, step *Step) bool {
	owned := func(id string) bool {
		if id == step.ID {
			return true
		}
		_, _, found := resolveScopedRuntimeChildren(step.ID, id, step)
		return found
	}
	for id, record := range state.AgentDeliveries {
		if owned(id) && record.Status != agentDeliveryCompleted && record.Status != agentDeliveryNotSent {
			return true
		}
	}
	for id, record := range state.MailSends {
		if owned(id) && record.Status == mailSendSending {
			return true
		}
	}
	for id, record := range state.ReservationSteps {
		if owned(id) && (record.Status == reservationAcquiring || record.Status == reservationUncertain || record.Status == reservationReleasing || record.Status == reservationPartial) {
			return true
		}
	}
	return false
}

func (e *Executor) checkpointSuccessHook(parent, child *Step, result StepResult, consumed bool) error {
	e.stateMu.Lock()
	e.varMu.Lock()
	record, exists := e.state.SuccessHooks[parent.ID]
	var err error
	if !exists {
		err = successHookRecoveryError("parent %q lost its completed-body checkpoint", parent.ID)
	} else {
		result = cloneSuccessHookResult(result)
		outputs := successHookOutputBindings(e.state, child, result)
		if consumed {
			result.SuccessHookParent = parent.ID
			background := child.Command != "" && child.Wait == WaitNone
			var digest string
			digest, err = successHookResultHash(result, outputs, background)
			if err == nil {
				if record.Children == nil {
					record.Children = make(map[string]SuccessHookChildCheckpoint)
				}
				record.Children[child.ID] = SuccessHookChildCheckpoint{ResultHash: digest, Outputs: outputs, Background: background}
			}
		} else if previous, restarting := record.Children[child.ID]; restarting && previous.Background {
			result.SuccessHookParent = parent.ID
			var digest string
			digest, err = successHookResultHash(result, outputs, true)
			if err == nil {
				record.Children[child.ID] = SuccessHookChildCheckpoint{ResultHash: digest, Outputs: outputs, Background: true, Restarting: true}
			}
		}
		if err == nil {
			e.state.Steps[child.ID] = result
			e.state.SuccessHooks[parent.ID] = record
			e.state.UpdatedAt = time.Now()
			if consumed {
				restoreSuccessHookOutputs(e.state, child, result, outputs)
			}
		}
	}
	e.varMu.Unlock()
	e.stateMu.Unlock()
	if err != nil {
		return err
	}
	return e.persistState()
}

// runPostPipelineSteps iterates Workflow.PostPipelineSteps in order after
// the main step graph has finished. Each step runs through the regular
// executeStep machinery so it inherits retries, on_failure, and routing.
// Failures here are persisted into state.Steps but DO NOT change the
// overall pipeline status — post-pipeline steps are for cleanup and
// notification, not gating (bd-w6nth.5). Only ambiguous ownership of persisted
// step/dispatch identities is returned as a fatal workflow error.
func (e *Executor) runPostPipelineSteps(ctx context.Context, workflow *Workflow) error {
	ctx = finalizationContext(ctx)
	if workflow == nil || len(workflow.PostPipelineSteps) == 0 {
		return nil
	}
	checkOwnership := func() error {
		e.stateMu.RLock()
		defer e.stateMu.RUnlock()
		if e.graph != nil {
			if phaseErrors := e.graph.validatePhaseOwnership(e.state); len(phaseErrors) > 0 {
				return phaseErrors[0]
			}
		}
		return nil
	}

	for i := range workflow.PostPipelineSteps {
		if ctx.Err() != nil || e.checkpointFailure() != nil {
			return nil
		}
		// Main work or an earlier hook may materialize dynamic IDs after
		// initial preflight. Do not adopt them as a different hook's result.
		if err := checkOwnership(); err != nil {
			return err
		}
		step := workflow.PostPipelineSteps[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("post_pipeline_%d", i+1)
		}

		e.stateMu.Lock()
		if prior, exists := e.state.Steps[step.ID]; exists && !shouldRerunStep(prior) {
			// A previous attempt may have checkpointed only part of the
			// post-pipeline tail. Resume restores these outputs before the
			// main graph runs; do not repeat a completed handoff's effects.
			e.stateMu.Unlock()
			continue
		}
		e.state.CurrentStep = step.ID
		e.state.UpdatedAt = time.Now()
		e.stateMu.Unlock()

		result := e.executeStep(ctx, &step, workflow)
		if !e.runtimeIDAdmitted(step.ID) {
			continue
		}
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
	return checkOwnership()
}

func (e *Executor) executeOnFailureAction(step *Step, failed StepResult) StepResult {
	if step == nil {
		return failed
	}
	action := strings.TrimSpace(step.OnFailure.Action)
	if action == "" || knownFailureAction(action) {
		return failed
	}
	// A skipped source is treated as consumed output during recovery. Keep
	// the original failed result and variables until settlement is known.
	if e.hasUnresolvedCommandExecution(step) || e.hasUnresolvedCommandEvaluation(step) {
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
	return recoveryStepDefinition(step.ID, step.OnFailure.Fallback, func(value string) string {
		return e.substituteVariablesCtx(ctx, value)
	})
}

const onFailureStepSuffix = ".on_failure"

func onFailureStepID(parentID string) string { return parentID + onFailureStepSuffix }

// The namespace resolver and dispatcher share one definition constructor.
// A nil substitution function describes the frozen authoring structure only;
// unresolved pane expressions cannot be used to dispatch until materialized.
func recoveryStepDefinition(parentID string, fallback map[string]interface{}, substitute func(string) string) (*Step, bool, error) {
	static := substitute == nil
	if static {
		substitute = func(value string) string { return value }
	}
	template, ok := recoveryStringValue(fallback["template"])
	if !ok || strings.TrimSpace(template) == "" {
		return nil, false, fmt.Errorf("on_failure recovery requires non-empty template")
	}
	template = substitute(template)

	pane, err := recoveryPaneSpec(fallback["pane"], substitute, static)
	if err != nil {
		return nil, false, err
	}

	params, err := recoveryParams(fallback["params"], substitute)
	if err != nil {
		return nil, false, err
	}

	wait := WaitNone
	if rawWait, ok := recoveryStringValue(fallback["wait"]); ok && strings.TrimSpace(rawWait) != "" {
		wait = WaitCondition(substitute(rawWait))
	}

	return &Step{
		ID:       onFailureStepID(parentID),
		Name:     "on_failure recovery for " + parentID,
		Template: template,
		Pane:     pane,
		Params:   params,
		Wait:     wait,
	}, recoveryBoolValue(fallback["suppress_failure"]), nil
}

func recoveryPaneSpec(raw interface{}, substitute func(string) string, static bool) (PaneSpec, error) {
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
		resolved := strings.TrimSpace(substitute(v))
		n, err := strconv.Atoi(resolved)
		if err == nil && n > 0 {
			return PaneSpec{Index: n}, nil
		}
		if static && strings.Contains(resolved, "${") {
			return PaneSpec{Expr: resolved}, nil
		}
		return PaneSpec{}, fmt.Errorf("on_failure pane %q must resolve to a positive pane index", v)
	}
	return PaneSpec{}, fmt.Errorf("on_failure recovery requires positive pane index")
}

func recoveryParams(raw interface{}, substitute func(string) string) (map[string]interface{}, error) {
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
		resolved[key] = recoveryParamValue(val, substitute)
	}
	return resolved, nil
}

func recoveryParamValue(val interface{}, substitute func(string) string) interface{} {
	switch v := val.(type) {
	case string:
		return substitute(v)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = recoveryParamValue(item, substitute)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = recoveryParamValue(item, substitute)
		}
		return out
	default:
		return val
	}
}

func onFailureRuntimeStep(parentID, runtimeID string, parent *Step) (*Step, bool) {
	if runtimeID != onFailureStepID(parentID) || len(parent.OnFailure.Fallback) == 0 {
		return nil, false
	}
	child, _, err := recoveryStepDefinition(parentID, parent.OnFailure.Fallback, nil)
	return child, err == nil
}

// A fallback delivery proves the parent already reached failure recovery. Do
// not repeat its settled command merely to revisit an uncertain delivery or
// publish an already-completed suppressing fallback. Non-suppressing completed
// recovery retains the ordinary policy for retrying a failed command.
func (e *Executor) pendingOnFailureDelivery(step *Step) (AgentDeliveryState, bool) {
	if len(step.OnFailure.Fallback) == 0 {
		return AgentDeliveryState{}, false
	}
	record, exists := e.loadAgentDelivery(onFailureStepID(step.ID))
	return record, exists && record.Status != agentDeliveryNotSent &&
		(record.Status != agentDeliveryCompleted || recoveryBoolValue(step.OnFailure.Fallback["suppress_failure"]))
}

func (e *Executor) validateOnFailureDelivery(ctx context.Context, parent *Step, record AgentDeliveryState) error {
	if err := validateAgentDelivery(onFailureStepID(parent.ID), record, e.config.Session); err != nil {
		return err
	}
	if record.ResolutionAction != "" {
		e.stateMu.RLock()
		_, err := agentDeliveryRecoveryEntry(e.state, record)
		e.stateMu.RUnlock()
		if err != nil {
			return err
		}
	}
	child, _, err := e.recoveryStepFromFallback(ctx, parent)
	if err != nil {
		return err
	}
	digest, err := agentStepHash(child)
	if err != nil {
		return err
	}
	if record.Kind != StepKindTemplate || record.StepHash != digest {
		return fmt.Errorf("saved failure recovery for %q belongs to a different materialized definition; refusing to repeat its parent or delivery", parent.ID)
	}
	if parent.Command != "" {
		original, exists := e.loadCommandExecution(parent.ID)
		if !exists {
			return fmt.Errorf("failure recovery for %q has lost its original command receipt", parent.ID)
		}
		if err := validateCommandExecution(parent.ID, original); err != nil {
			return err
		}
		if original.ResolutionAction != "" {
			e.stateMu.RLock()
			_, err := commandExecutionRecoveryEntry(e.state, original)
			e.stateMu.RUnlock()
			if err != nil {
				return err
			}
		}
		digest, err := commandDefinitionHash(parent)
		if err != nil {
			return err
		}
		if original.Status != commandExecutionSettled || original.Result.Status != StatusFailed ||
			original.StepHash != digest || original.StartedAt.After(record.StartedAt) {
			return fmt.Errorf("failure recovery for %q no longer belongs to the original settled failed command; refusing to suppress or repeat it", parent.ID)
		}
	}
	return nil
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

	if failed.Error != nil {
		// The original native receipt can share this pointer. Recovery
		// diagnostics belong to the scheduler result, not its launch evidence.
		original := cloneStepErrorValue(*failed.Error)
		failed.Error = &original
		if failed.Error.Details != "" {
			failed.Error.Details += "\n"
		}
		failed.Error.Details += msg
	} else {
		failed.Error = &StepError{
			Type:      "on_failure",
			Message:   msg,
			Timestamp: time.Now(),
		}
	}
	if isStepAdmissionError(err) {
		failed.Error.Aggregated = append(failed.Error.Aggregated, StepError{Type: "step_limit", Message: err.Error(), Timestamp: time.Now()})
	}
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
