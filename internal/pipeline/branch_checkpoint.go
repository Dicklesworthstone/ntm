package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	branchDecisionSuffix  = ".__ntm_branch_decision"
	branchDecisionAgent   = "branch_decision"
	branchDecisionVersion = 1
)

// A branch decision is a completed synthetic evaluation step, not a completed
// branch body. Keeping it in Steps makes the decision and child outcomes part
// of the SAME atomic checkpoint. Normal resume retains it through the runtime
// namespace resolver; reset and force-iteration replay clear it with its scope.
// ParsedData contains only JSON values so snapshotState deep-copies the record.
// No mutable workflow variables or a second per-run state file carry authority.
type branchDecision struct {
	Version        int                              `json:"version"`
	ParentID       string                           `json:"parent_id"`
	DefinitionHash string                           `json:"definition_hash"`
	SelectorKey    string                           `json:"selector_key"`
	BranchKey      string                           `json:"branch_key"`
	Children       map[string]branchChildCheckpoint `json:"children,omitempty"`
	Scope          map[string]branchScopeValue      `json:"scope,omitempty"`
}

// A child receipt binds restored aliases to the exact settled StepResult.
// HooksSettled distinguishes a returned lifecycle from a command result that
// became durable just before a crash interrupted its success hooks.
type branchChildCheckpoint struct {
	ResultHash   string                 `json:"result_hash"`
	Outputs      map[string]interface{} `json:"outputs,omitempty"`
	HooksSettled bool                   `json:"hooks_settled"`
}

type branchScopeValue struct {
	Exists bool        `json:"exists"`
	Value  interface{} `json:"value,omitempty"`
}

func branchDecisionID(parentID string) string { return parentID + branchDecisionSuffix }

func isBranchDecisionID(id string) bool { return strings.HasSuffix(id, branchDecisionSuffix) }

// The synthetic evaluation is never a separately scheduled graph node. Giving
// it branch structure also keeps resume's leaf-delivery guards from mistaking
// an internal checkpoint for a prompt that may have been sent to an agent.
func branchDecisionRuntimeStep(parentID, id string, parent *Step) (*Step, string, bool) {
	if parent.Branch == "" || len(parent.Branches) == 0 || id != branchDecisionID(parentID) {
		return nil, "", false
	}
	return &Step{ID: id, Branch: parent.Branch, Branches: parent.Branches}, id, true
}

func branchDefinitionHash(step *Step) (string, error) {
	data, err := json.Marshal(step)
	if err != nil {
		return "", fmt.Errorf("hash branch definition: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func decodeBranchDecision(id string, result StepResult) (branchDecision, error) {
	var decision branchDecision
	invalid := func(reason string) (branchDecision, error) {
		return branchDecision{}, fmt.Errorf("invalid branch decision checkpoint %q: %s", id, reason)
	}
	if !isBranchDecisionID(id) || result.StepID != id || result.Status != StatusCompleted ||
		result.AgentType != branchDecisionAgent || result.Error != nil || result.RerunOnResume ||
		result.StartedAt.IsZero() || result.FinishedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) {
		return invalid("missing or inconsistent completed evaluation evidence")
	}
	data, err := json.Marshal(result.ParsedData)
	if err != nil {
		return invalid(err.Error())
	}
	if err := json.Unmarshal(data, &decision); err != nil {
		return invalid(err.Error())
	}
	if decision.Version != branchDecisionVersion || decision.ParentID == "" || branchDecisionID(decision.ParentID) != id || decision.SelectorKey != result.Output {
		return invalid("unsupported version or mismatched evaluation identity")
	}
	hash, err := hex.DecodeString(decision.DefinitionHash)
	if err != nil || len(hash) != sha256.Size || decision.DefinitionHash != strings.ToLower(decision.DefinitionHash) {
		return invalid("missing or invalid definition hash")
	}
	for childID, child := range decision.Children {
		digest, err := hex.DecodeString(child.ResultHash)
		if childID == id || childID == decision.ParentID ||
			(!strings.HasPrefix(childID, decision.ParentID+"_") && !strings.HasPrefix(childID, decision.ParentID+".")) ||
			err != nil || len(digest) != sha256.Size || child.ResultHash != strings.ToLower(child.ResultHash) {
			return invalid("invalid child completion identity or result hash")
		}
	}
	for key, value := range decision.Scope {
		if key == "" || (!value.Exists && value.Value != nil) {
			return invalid("inconsistent saved variable scope")
		}
	}
	return decision, nil
}

func validateBranchDecisions(state *ExecutionState) error {
	for id, result := range state.Steps {
		if isBranchDecisionID(id) {
			if _, err := decodeBranchDecision(id, result); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareBranchDecision returns the original selector result and selected body.
// Even default fallback records BOTH keys. Anonymous child IDs use the matched
// map key, so the dispatcher and recovery namespace resolver agree even when
// the selector returns an arbitrary string that falls back to "default".
// A selector is evaluated only if there is no previous decision or child work.
// Its successful decision must be durable before any child can be dispatched.
func (e *Executor) prepareBranchDecision(ctx context.Context, step *Step) (branchDecision, []Step, error) {
	fail := func(err error) (branchDecision, []Step, error) { return branchDecision{}, nil, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	digest, err := branchDefinitionHash(step)
	if err != nil {
		return fail(err)
	}
	id := branchDecisionID(step.ID)
	if e.graph != nil {
		if _, exists := e.graph.GetStep(id); exists {
			return fail(fmt.Errorf("branch decision identity %q collides with an authored step", id))
		}
	}
	e.stateMu.RLock()
	prior, exists := e.state.Steps[id]
	prior.ParsedData = cloneInterfaceValue(prior.ParsedData)
	e.stateMu.RUnlock()
	var decision branchDecision
	startedAt := time.Now()
	if exists {
		decision, err = decodeBranchDecision(id, prior)
		if err != nil {
			return fail(err)
		}
		if decision.DefinitionHash != digest {
			return fail(fmt.Errorf("branch %q definition changed since its decision was checkpointed; restore the original workflow or explicitly reset the run", step.ID))
		}
	} else {
		if e.branchHasChildEvidence(step) {
			return fail(fmt.Errorf("branch %q has child execution evidence but no saved decision; refusing to guess a path or replay work. Inspect the prior effects before deliberately using --keep-state=false", step.ID))
		}
		key, err := e.resolveBranch(ctx, step)
		if err != nil {
			return fail(fmt.Errorf("branch predicate failed: %w", err))
		}
		matched := key
		if _, found := step.Branches[key]; !found {
			matched = "default"
		}
		if _, err := lookupBranch(step.Branches, key); err != nil {
			return fail(err)
		}
		decision = branchDecision{Version: branchDecisionVersion, ParentID: step.ID, DefinitionHash: digest, SelectorKey: key, BranchKey: matched}
	}
	body, found := step.Branches[decision.BranchKey]
	if !found {
		return fail(fmt.Errorf("saved branch %q is missing from step %q; refusing fallback during recovery", decision.BranchKey, step.ID))
	}
	steps, err := parseBranchSteps(body, step.ID, decision.BranchKey)
	if err != nil {
		return fail(fmt.Errorf("failed to parse branch body: %w", err))
	}
	seen := map[string]bool{id: true, step.ID: true}
	children := make(map[string]*Step, len(steps))
	for i := range steps {
		childID := scopedChildStepID(step.ID, steps[i].ID, i+1)
		if seen[childID] {
			return fail(fmt.Errorf("branch %q has a duplicate or reserved child identity %q", step.ID, childID))
		}
		if e.graph != nil {
			if _, exists := e.graph.GetStep(childID); exists {
				return fail(fmt.Errorf("branch child identity %q collides with an independently authored step", childID))
			}
		}
		seen[childID] = true
		children[childID] = &steps[i]
	}
	for childID, receipt := range decision.Children {
		child := children[childID]
		if child == nil {
			return fail(fmt.Errorf("branch %q has completion evidence for unknown child %q", step.ID, childID))
		}
		for name := range receipt.Outputs {
			if child.OutputVar == "" || (name != child.OutputVar && name != child.OutputVar+"_parsed") {
				return fail(fmt.Errorf("branch child %q contains foreign output alias %q", childID, name))
			}
		}
		e.stateMu.RLock()
		result, recorded := e.state.Steps[childID]
		result.ParsedData = cloneInterfaceValue(result.ParsedData)
		if result.Error != nil {
			copy := cloneStepErrorValue(*result.Error)
			result.Error = &copy
		}
		e.stateMu.RUnlock()
		if !recorded {
			return fail(fmt.Errorf("branch child %q completion checkpoint has lost its saved result; refusing to replay its effects", childID))
		}
		if !shouldRerunStep(result) {
			if result.StepID == "" {
				result.StepID = childID
			}
			digest, err := branchChildHash(result, receipt.Outputs)
			if err != nil || digest != receipt.ResultHash {
				return fail(fmt.Errorf("branch child %q result differs from its completion checkpoint; refusing stale recovery", childID))
			}
		}
	}
	owned := branchBodyOutputVars(steps)
	for name := range decision.Scope {
		if _, ok := owned[name]; !ok {
			return fail(fmt.Errorf("branch %q contains foreign scope alias %q", step.ID, name))
		}
	}
	if !exists {
		// Resume rebuilds step aliases before reaching the branch. Remember the
		// original shadowed values so that reconstruction does not turn a local
		// output into a global one when the resumed body leaves its scope.
		decision.Scope = make(map[string]branchScopeValue, len(owned))
		e.varMu.RLock()
		for name := range owned {
			value, exists := e.state.Variables[name]
			decision.Scope[name] = branchScopeValue{Exists: exists, Value: cloneInterfaceValue(value)}
		}
		e.varMu.RUnlock()
		// Use ordinary JSON values, not a typed struct containing live maps.
		encoded, err := json.Marshal(decision)
		if err != nil {
			return fail(err)
		}
		var metadata map[string]interface{}
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			return fail(err)
		}
		e.stateMu.Lock()
		e.state.Steps[id] = StepResult{
			StepID: id, Status: StatusCompleted, AgentType: branchDecisionAgent,
			StartedAt: startedAt, FinishedAt: time.Now(), Output: decision.SelectorKey, ParsedData: metadata,
		}
		e.stateMu.Unlock()
		if err := e.persistState(); err != nil {
			return fail(err)
		}
	}
	return decision, steps, ctx.Err()
}

func branchChildHash(result StepResult, outputs map[string]interface{}) (string, error) {
	data, err := json.Marshal(struct {
		Result  StepResult             `json:"result"`
		Outputs map[string]interface{} `json:"outputs,omitempty"`
	}{result, outputs})
	if err != nil {
		return "", fmt.Errorf("hash branch child result: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// adoptBranchChild never treats in-flight, failed, cancelled or explicitly
// rerunnable work as settled. Those cases still enter the canonical executor
// and its command/agent journals. A saved completion cannot justify replaying
// the child's external work merely to rebuild a variable or finish a hook.
func (e *Executor) adoptBranchChild(ctx context.Context, decision branchDecision, child *Step, workflow *Workflow) (StepResult, bool, error) {
	if child.Command != "" && child.Wait == WaitNone {
		// A synchronous Completed result proves only launch for WaitNone.
		// The canonical command journal owns its still-running process group.
		return StepResult{}, false, nil
	}
	e.stateMu.RLock()
	prior, exists := e.state.Steps[child.ID]
	_, inFlight := e.state.InFlightSteps[child.ID]
	prior.ParsedData = cloneInterfaceValue(prior.ParsedData)
	if prior.Error != nil {
		copy := cloneStepErrorValue(*prior.Error)
		prior.Error = &copy
	}
	e.stateMu.RUnlock()
	if !exists || inFlight || shouldRerunStep(prior) || (prior.Status != StatusCompleted && prior.Status != StatusSkipped) {
		return StepResult{}, false, nil
	}
	if prior.StepID != "" && prior.StepID != child.ID {
		return StepResult{}, false, fmt.Errorf("branch child %q has a mismatched saved result", child.ID)
	}
	prior.StepID = child.ID
	receipt, captured := decision.Children[child.ID]
	if captured {
		digest, err := branchChildHash(prior, receipt.Outputs)
		if err != nil {
			return StepResult{}, false, err
		}
		if digest != receipt.ResultHash {
			return StepResult{}, false, fmt.Errorf("branch child %q result differs from its completion checkpoint; refusing to replay or restore stale outputs", child.ID)
		}
	}
	if prior.Status == StatusCompleted {
		if !captured && child.OutputVar != "" && stepOwnsForeachOutputVar(child) {
			return StepResult{}, false, fmt.Errorf("completed branch child %q lacks its aggregate output checkpoint; inspect the prior output before an explicit reset", child.ID)
		}
		e.varMu.Lock()
		StoreStepOutput(e.state, child.ID, prior.Output, prior.ParsedData)
		if child.OutputVar != "" {
			delete(e.state.Variables, child.OutputVar)
			delete(e.state.Variables, child.OutputVar+"_parsed")
			if captured {
				for name, value := range receipt.Outputs {
					e.state.Variables[name] = cloneInterfaceValue(value)
				}
			} else {
				e.state.Variables[child.OutputVar] = prior.Output
				if prior.ParsedData != nil {
					e.state.Variables[child.OutputVar+"_parsed"] = cloneInterfaceValue(prior.ParsedData)
				}
			}
		}
		e.varMu.Unlock()
		if !captured || !receipt.HooksSettled {
			// A native command journal can reconstruct the result of a child
			// interrupted during hooks. The existing hook runner adopts its
			// own completed children; do not run the original command again.
			e.runOnSuccessSteps(ctx, child, workflow)
		}
	}
	return prior, true, nil
}

// Publish child completion and its local output shape in the SAME state update.
// snapshotState takes stateMu then varMu too, so a sibling's checkpoint cannot
// capture a completed child with an older alias receipt. Only the usual
// persistState performs disk IO and trips the sticky checkpoint failure fence.
func (e *Executor) checkpointBranchChild(ctx context.Context, parentID string, child *Step, result StepResult) error {
	e.stateMu.Lock()
	e.varMu.Lock()
	runID := e.state.RunID
	id := branchDecisionID(parentID)
	evaluation := e.state.Steps[id]
	decision, err := decodeBranchDecision(id, evaluation)
	outputs := make(map[string]interface{})
	if err == nil {
		// Prepare all values before publishing any new completion. A marshal
		// failure must not leave readers with a completed child and no receipt.
		result.ParsedData = cloneInterfaceValue(result.ParsedData)
		if result.Error != nil {
			copy := cloneStepErrorValue(*result.Error)
			result.Error = &copy
		}
		if result.Status == StatusCompleted && child.OutputVar != "" {
			if stepOwnsForeachOutputVar(child) {
				for _, name := range []string{child.OutputVar, child.OutputVar + "_parsed"} {
					if value, exists := e.state.Variables[name]; exists {
						outputs[name] = cloneInterfaceValue(value)
					}
				}
			} else {
				outputs[child.OutputVar] = result.Output
				if result.ParsedData != nil {
					outputs[child.OutputVar+"_parsed"] = result.ParsedData
				}
			}
		}
		if decision.Children == nil {
			decision.Children = make(map[string]branchChildCheckpoint)
		}
		// A fire-and-forget command has not settled at its synchronous return.
		// Its native command journal and later RerunOnResume flag own recovery.
		if !(child.Command != "" && child.Wait == WaitNone) &&
			(result.Status == StatusCompleted || (result.Status == StatusSkipped && !shouldRerunStep(result))) {
			var digest string
			digest, err = branchChildHash(result, outputs)
			if err == nil {
				decision.Children[child.ID] = branchChildCheckpoint{ResultHash: digest, Outputs: outputs, HooksSettled: ctx.Err() == nil}
			}
		} else {
			delete(decision.Children, child.ID)
		}
		if err == nil {
			var encoded []byte
			encoded, err = json.Marshal(decision)
			if err == nil {
				var metadata map[string]interface{}
				err = json.Unmarshal(encoded, &metadata)
				if err == nil {
					evaluation.ParsedData = metadata
					e.state.Steps[child.ID] = result
					e.state.Steps[id] = evaluation
					e.state.UpdatedAt = time.Now()
					if result.Status == StatusCompleted {
						StoreStepOutput(e.state, child.ID, result.Output, result.ParsedData)
						if child.OutputVar != "" {
							delete(e.state.Variables, child.OutputVar)
							delete(e.state.Variables, child.OutputVar+"_parsed")
							for name, value := range outputs {
								e.state.Variables[name] = value
							}
						}
					}
				}
			}
		}
	}
	e.varMu.Unlock()
	e.stateMu.Unlock()
	if err != nil {
		// This is a checkpoint boundary after external effects, even when
		// serialization (rather than disk IO) is what failed. Fence retries
		// and other workers through the same sticky checkpoint error.
		e.persistMu.Lock()
		defer e.persistMu.Unlock()
		return e.failCheckpointLocked(runID, err)
	}
	return e.persistState()
}

// Old checkpoints cannot identify a branch by child name: different branches
// may use the same authored ID, and completed results may have been pruned by
// resume. Include durable launch and composite receipts before evaluating a
// new selector. The owning run lock is held by all public execution surfaces.
func (e *Executor) branchHasChildEvidence(parent *Step) bool {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	belongs := func(id string) bool {
		if !strings.HasPrefix(id, parent.ID+"_") && !strings.HasPrefix(id, parent.ID+".") {
			return false
		}
		_, _, ok := resolveScopedRuntimeBranchStep(parent.ID, id, parent.Branches)
		return ok
	}
	for id := range e.state.Steps {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.InFlightSteps {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.CommandExecutions {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.AgentDeliveries {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.MailSends {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.ReservationSteps {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.ForeachState {
		if belongs(id) {
			return true
		}
	}
	for id := range e.state.ParallelState {
		if belongs(id) {
			return true
		}
	}
	return false
}
