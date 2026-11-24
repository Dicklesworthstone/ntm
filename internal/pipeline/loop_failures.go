package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"
)

const loopFailureCheckpointVersion = 1

func loopFailureRecoveryError(format string, args ...interface{}) error {
	return fmt.Errorf("loop failure recovery: %s", fmt.Sprintf(format, args...))
}

// These checks need no workflow and run on both sides of the state-file
// boundary. Neither a lone marker nor a lone journal entry authorizes replay.
func validateConsumedLoopFailures(state *ExecutionState) error {
	if state == nil {
		return nil
	}
	for id, result := range state.Steps {
		if result.ConsumedByLoop == "" {
			continue
		}
		parent, exists := state.ForeachState[result.ConsumedByLoop]
		if _, consumed := parent.ConsumedFailures[id]; !exists || !consumed {
			return loopFailureRecoveryError("consumed child %q lost its loop journal %q", id, result.ConsumedByLoop)
		}
	}
	for parentID, parent := range state.ForeachState {
		for id, receipt := range parent.ConsumedFailures {
			result, exists := state.Steps[id]
			if parentID == "" || parent.StepID != parentID || iterationIndexFromID(id, parentID+"_iter") < 0 ||
				receipt.Version != loopFailureCheckpointVersion || receipt.Policy != ErrorActionContinue ||
				!validCommandEvaluationHash(receipt.StepHash) || !validCommandEvaluationHash(receipt.DefinitionHash) ||
				!validCommandEvaluationHash(receipt.LoopHash) || !validCommandEvaluationHash(receipt.ResultHash) || !validCommandEvaluationHash(receipt.NativeHash) ||
				!exists || result.StepID != id || result.ConsumedByLoop != parentID || result.Status != StatusFailed ||
				result.RerunOnResume || result.StartedAt.IsZero() || result.FinishedAt.IsZero() ||
				result.FinishedAt.Before(result.StartedAt) || unsettledSuccessHookError(result.Error) != nil {
				return loopFailureRecoveryError("invalid consumed failure checkpoint for %q", id)
			}
			digest, err := parallelCheckpointHash(result)
			if err != nil || digest != receipt.ResultHash {
				return loopFailureRecoveryError("consumed failure %q differs from its checkpoint", id)
			}
		}
	}
	return nil
}

// Bind the complete authored loop, including its entry predicate and body.
// resolveIntOrExpr caches a resolved/default bound in Value; that cache is not
// a change to an expression or to an omitted default in the authored workflow.
func loopFailureParentHash(graph *DependencyGraph, state *ExecutionState, parent *Step) (string, error) {
	authored := parent
	if graph != nil {
		resolve, err := successHookRuntimeStepResolver(graph, state)
		if err != nil {
			return "", err
		}
		var found bool
		authored, found = resolve(parent.ID)
		if !found {
			return "", loopFailureRecoveryError("loop %q lost its authored definition", parent.ID)
		}
	}
	if authored.Loop == nil {
		return "", loopFailureRecoveryError("step %q is no longer a loop", parent.ID)
	}
	loop := *authored.Loop
	if loop.MaxIterations.Expr != "" {
		loop.MaxIterations.Value = 0
	} else if loop.MaxIterations.Value == 0 {
		loop.MaxIterations.Value = DefaultMaxIterations
	}
	return parallelCheckpointHash(struct {
		StepID string      `json:"step_id"`
		Loop   *LoopConfig `json:"loop"`
	}{parent.ID, &loop})
}

// A consumed body failure proves this iteration passed its entry predicate.
// Resume finishes that admitted body even if an earlier child changed the
// condition's variables. It does not admit the next iteration or a forced reset.
func (e *Executor) resumeConsumedLoopIteration(parent *Step, workflow *Workflow, iteration int) (bool, error) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return false, nil
	}
	progress := e.state.ForeachState[parent.ID]
	if stringSliceContains(progress.CompletedIterationIDs, loopIterationID(parent.ID, iteration)) {
		return false, nil
	}
	for id := range progress.ConsumedFailures {
		if iterationIndexFromID(id, parent.ID+"_iter") == iteration {
			// Direct ExecuteLoop callers also cross this boundary. Validate
			// before earlier body children can run under changed controls.
			if _, err := prepareConsumedLoopFailures(e.graph, e.state, &workflow.Settings.OnError); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func loopFailureOwns(step *Step, id string) bool {
	if id == step.ID {
		return true
	}
	_, _, owns := resolveScopedRuntimeChildren(step.ID, id, step)
	return owns
}

// Caller holds stateMu. Bind the set of native receipts, including descendants,
// and validate their settlement independently. Hashing identities rather than
// mutable receipts permits a held reservation to be released by a later step.
func loopFailureNativeHash(state *ExecutionState, step *Step) (string, error) {
	var identities []string
	for id := range state.InFlightSteps {
		if loopFailureOwns(step, id) {
			return "", loopFailureRecoveryError("consumed child %q still has in-flight work %q", step.ID, id)
		}
	}
	for id, result := range state.Steps {
		if !loopFailureOwns(step, id) {
			continue
		}
		if result.RerunOnResume || result.Status == StatusCancelled || result.Status == StatusRunning ||
			result.Status == StatusPending || unsettledSuccessHookError(result.Error) != nil {
			return "", loopFailureRecoveryError("child %q has unfinished descendant %q", step.ID, id)
		}
		definition := step
		if id != step.ID {
			definition, _, _ = resolveScopedRuntimeChildren(step.ID, id, step)
			digest, err := parallelCheckpointHash(result)
			if err != nil {
				return "", err
			}
			identities = append(identities, "result:"+id+":"+digest)
		}
		if definition == nil || result.Status != StatusFailed {
			continue
		}
		kind := stepKind(definition)
		_, command := state.CommandExecutions[id]
		_, agent := state.AgentDeliveries[id]
		if (kind != StepKindCommand || command) &&
			((kind != StepKindPrompt && kind != StepKindTemplate) || agent) {
			continue
		}
		// The live consumption checkpoint can attest that dispatch was
		// never reached for these explicit predispatch failures. Bind that
		// proof to the exact result; do not infer it from a legacy failure.
		if !loopFailureBeforeDispatch(kind, result) {
			return "", loopFailureRecoveryError("failed child %q has lost its native dispatch receipt", id)
		}
		result.ConsumedByLoop = ""
		digest, err := parallelCheckpointHash(result)
		if err != nil {
			return "", err
		}
		identities = append(identities, "predispatch:"+id+":"+digest)
	}
	for id, record := range state.CommandExecutions {
		if !loopFailureOwns(step, id) {
			continue
		}
		if err := validateCommandExecution(id, record); err != nil {
			return "", err
		}
		if record.Status != commandExecutionSettled || record.Result.RerunOnResume || record.Result.Status == StatusCancelled ||
			unsettledSuccessHookError(record.Result.Error) != nil {
			return "", loopFailureRecoveryError("child %q has unsettled command %q", step.ID, id)
		}
		if record.ResolutionAction != "" {
			if _, err := commandExecutionRecoveryEntry(state, record); err != nil {
				return "", err
			}
		}
		identities = append(identities, "command:"+id+":"+record.StepHash)
	}
	for id, record := range state.CommandEvaluations {
		if !loopFailureOwns(step, id) {
			continue
		}
		if err := validateCommandEvaluation(id, record); err != nil {
			return "", err
		}
		if !commandEvaluationHasOutcome(record) {
			return "", loopFailureRecoveryError("child %q has unsettled selector %q", step.ID, id)
		}
		if record.ResolutionAction != "" {
			if _, err := commandEvaluationRecoveryEntry(state, record); err != nil {
				return "", err
			}
		}
		identities = append(identities, "selector:"+id+":"+record.StepHash)
	}
	for id, record := range state.AgentDeliveries {
		if !loopFailureOwns(step, id) {
			continue
		}
		if err := validateAgentDelivery(id, record, state.Session); err != nil {
			return "", err
		}
		if record.Status != agentDeliveryCompleted && record.Status != agentDeliveryNotSent {
			return "", loopFailureRecoveryError("child %q has unfinished agent delivery %q", step.ID, id)
		}
		if record.ResolutionAction != "" {
			if _, err := agentDeliveryRecoveryEntry(state, record); err != nil {
				return "", err
			}
		}
		identities = append(identities, "agent:"+id+":"+record.StepHash)
	}
	for id, record := range state.MailSends {
		if !loopFailureOwns(step, id) {
			continue
		}
		if err := validateMailSend(id, record); err != nil {
			return "", err
		}
		if record.Status == mailSendSending || record.Result.RerunOnResume || record.Result.Status == StatusCancelled ||
			unsettledSuccessHookError(record.Result.Error) != nil {
			return "", loopFailureRecoveryError("child %q has an unsettled mail send %q", step.ID, id)
		}
		identities = append(identities, "mail:"+id+":"+record.StepHash)
	}
	for id, record := range state.ReservationSteps {
		if !loopFailureOwns(step, id) {
			continue
		}
		if err := validateReservationStep(id, record); err != nil {
			return "", err
		}
		if record.Status == reservationAcquiring || record.Status == reservationUncertain ||
			record.Status == reservationReleasing || record.Status == reservationPartial ||
			record.Result.RerunOnResume || record.Result.Status == StatusCancelled || unsettledSuccessHookError(record.Result.Error) != nil {
			return "", loopFailureRecoveryError("child %q has an unsettled reservation %q", step.ID, id)
		}
		identities = append(identities, "reservation:"+id+":"+record.StepHash)
	}
	for id, record := range state.SuccessHooks {
		if !loopFailureOwns(step, id) {
			continue
		}
		if !successHooksSettled(record) {
			return "", loopFailureRecoveryError("child %q has unfinished success hooks %q", step.ID, id)
		}
		identities = append(identities, "success:"+id+":"+record.StepHash)
	}
	sort.Strings(identities)
	return parallelCheckpointHash(identities)
}

func loopFailureBeforeDispatch(kind string, result StepResult) bool {
	if result.Error == nil {
		return false
	}
	switch result.Error.Type {
	case "condition":
		return result.Attempts == 0
	case "substitution", "validation", "limit":
		return true
	case "prompt", "template", "routing", "limit_exceeded":
		return kind == StepKindPrompt || kind == StepKindTemplate
	default:
		return false
	}
}

// Validate every record before resume mutates the checkpoint, including those
// hidden by a completed iteration or parent. Dynamic definitions are checked
// against their authored identity here and their materialized identity again
// immediately before adoption inside the original iteration scope.
func prepareConsumedLoopFailures(graph *DependencyGraph, state *ExecutionState, inherited *ErrorAction) (map[string]bool, error) {
	consumed := make(map[string]bool)
	if err := validateConsumedLoopFailures(state); err != nil {
		return nil, err
	}
	if state == nil || len(state.ForeachState) == 0 {
		return consumed, nil
	}
	resolve, err := successHookRuntimeStepResolver(graph, state)
	if err != nil {
		return nil, err
	}
	for parentID, parent := range state.ForeachState {
		if len(parent.ConsumedFailures) == 0 {
			continue
		}
		owner, found := resolve(parentID)
		if !found || owner.Loop == nil {
			return nil, loopFailureRecoveryError("loop %q lost its authored definition", parentID)
		}
		runtimeParent := *owner
		runtimeParent.ID = parentID
		loopHash, err := loopFailureParentHash(graph, state, &runtimeParent)
		if err != nil {
			return nil, err
		}
		for id, receipt := range parent.ConsumedFailures {
			if receipt.LoopHash != loopHash {
				return nil, loopFailureRecoveryError("consumed failure %q belongs to a different loop definition", id)
			}
			iteration := iterationIndexFromID(id, parentID+"_iter")
			var child *Step
			for _, authored := range owner.Loop.Steps {
				if fmt.Sprintf("%s_iter%d_%s", parentID, iteration, authored.ID) == id {
					definition := cloneStep(authored)
					definition.ID = id
					child = &definition
					break
				}
			}
			if child == nil {
				return nil, loopFailureRecoveryError("consumed failure %q is no longer a direct loop child", id)
			}
			digest, err := commandDefinitionHash(child)
			if err != nil || digest != receipt.DefinitionHash {
				return nil, loopFailureRecoveryError("consumed failure %q belongs to a different authored definition", id)
			}
			if inherited != nil && resolveErrorAction(child.OnError, *inherited) != receipt.Policy {
				return nil, loopFailureRecoveryError("consumed failure %q no longer allows on_error=continue", id)
			}
			native, err := loopFailureNativeHash(state, child)
			if err != nil {
				return nil, err
			}
			if native != receipt.NativeHash {
				return nil, loopFailureRecoveryError("consumed failure %q has lost or changed its native receipts", id)
			}
			consumed[id] = true
			for descendant := range state.Steps {
				if loopFailureOwns(child, descendant) {
					consumed[descendant] = true
				}
			}
		}
	}
	return consumed, nil
}

func (e *Executor) loadConsumedLoopFailure(parent *Step, child *Step, workflow *Workflow) (StepResult, bool, error) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return StepResult{}, false, nil
	}
	receipt, exists := e.state.ForeachState[parent.ID].ConsumedFailures[child.ID]
	if !exists {
		if result := e.state.Steps[child.ID]; result.ConsumedByLoop != "" {
			return StepResult{}, false, loopFailureRecoveryError("child %q lost its consumption receipt", child.ID)
		}
		return StepResult{}, false, nil
	}
	if err := validateConsumedLoopFailures(e.state); err != nil {
		return StepResult{}, false, err
	}
	loopHash, err := loopFailureParentHash(e.graph, e.state, parent)
	if err != nil || loopHash != receipt.LoopHash {
		return StepResult{}, false, loopFailureRecoveryError("consumed failure %q changed its enclosing loop definition", child.ID)
	}
	digest, err := commandDefinitionHash(child)
	if err != nil || digest != receipt.StepHash || resolveErrorAction(child.OnError, workflow.Settings.OnError) != receipt.Policy {
		return StepResult{}, false, loopFailureRecoveryError("consumed failure %q changed its materialized definition or error policy", child.ID)
	}
	native, err := loopFailureNativeHash(e.state, child)
	if err != nil {
		return StepResult{}, false, err
	}
	if native != receipt.NativeHash {
		return StepResult{}, false, loopFailureRecoveryError("consumed failure %q has lost or changed its native receipts", child.ID)
	}
	return cloneSuccessHookResult(e.state.Steps[child.ID]), true, nil
}

// Record the original failure and the decision to continue in one checkpoint,
// before a later child can produce effects. Persistence failure stops the loop;
// it must not replace the original result bound by the receipt.
func (e *Executor) checkpointConsumedLoopFailure(ctx context.Context, parent *Step, child *Step, result StepResult) (StepResult, error) {
	parentID := parent.ID
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if result.Status != StatusFailed || result.RerunOnResume || unsettledSuccessHookError(result.Error) != nil {
		return result, loopFailureRecoveryError("child %q has no settled failure to consume", child.ID)
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return result, loopFailureRecoveryError("child %q has no execution state", child.ID)
	}
	native, err := loopFailureNativeHash(e.state, child)
	var definition, materialized, loopHash, digest string
	if err == nil {
		loopHash, err = loopFailureParentHash(e.graph, e.state, parent)
	}
	if err == nil {
		definition, err = successHookAuthoredHash(e.graph, e.state, child, e.graph != nil)
	}
	if err == nil {
		materialized, err = commandDefinitionHash(child)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		result = cloneSuccessHookResult(result)
		result.ConsumedByLoop = parentID
		digest, err = parallelCheckpointHash(result)
	}
	if err == nil {
		if e.state.ForeachState == nil {
			e.state.ForeachState = make(map[string]ForeachIterationState)
		}
		parent := e.state.ForeachState[parentID]
		parent.StepID = parentID
		if parent.ConsumedFailures == nil {
			parent.ConsumedFailures = make(map[string]LoopFailureCheckpoint)
		}
		parent.ConsumedFailures[child.ID] = LoopFailureCheckpoint{
			Version: loopFailureCheckpointVersion, Policy: ErrorActionContinue,
			StepHash: materialized, DefinitionHash: definition, LoopHash: loopHash, ResultHash: digest, NativeHash: native,
		}
		parent.UpdatedAt = time.Now()
		e.state.ForeachState[parentID] = parent
		e.state.Steps[child.ID] = result
	}
	e.stateMu.Unlock()
	if err != nil {
		return result, err
	}
	return result, e.persistState()
}
