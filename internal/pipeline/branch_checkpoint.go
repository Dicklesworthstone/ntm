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
	Version        int    `json:"version"`
	ParentID       string `json:"parent_id"`
	DefinitionHash string `json:"definition_hash"`
	SelectorKey    string `json:"selector_key"`
	BranchKey      string `json:"branch_key"`
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
	for i := range steps {
		childID := scopedChildStepID(step.ID, steps[i].ID, i+1)
		if seen[childID] {
			return fail(fmt.Errorf("branch %q has a duplicate or reserved child identity %q", step.ID, childID))
		}
		seen[childID] = true
	}
	if !exists {
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
