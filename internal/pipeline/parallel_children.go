package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// runParallelChildren supplies the canonical step lifecycle and persistence
// boundaries to the body scheduler. It does not implement another executor.
func (e *Executor) runParallelChildren(ctx context.Context, step *Step, workflow *Workflow, onError ErrorAction) ([]StepResult, error) {
	children, tasks, err := parallelChildDefinitions(step)
	if err != nil {
		return nil, err
	}
	results := make([]StepResult, len(children))
	retained := make(map[int]bool)
	completedBodies := make(map[int]bool)
	recoverHooks := make([]bool, len(children))
	hookRecoveryErrors := make([]error, len(children))
	bindings := make([]map[string]interface{}, len(children))
	receipts := make([]string, len(children))
	hooksSettled := make([]bool, len(children))
	external := make(map[string]parallelTaskOutcome, len(workflow.Steps))
	if err := validateParallelAggregateAliases(children); err != nil {
		return nil, err
	}

	e.stateMu.RLock()
	groupState := e.state.ParallelState[step.ID]
	if len(groupState.ChildCheckpoints) > 0 && groupState.StepID != step.ID {
		e.stateMu.RUnlock()
		return nil, fmt.Errorf("parallel group %q has mismatched completion checkpoint identity %q", step.ID, groupState.StepID)
	}
	knownChildren := make(map[string]bool, len(children))
	for _, child := range children {
		knownChildren[child.ID] = true
	}
	for id := range groupState.ChildCheckpoints {
		if !knownChildren[id] {
			e.stateMu.RUnlock()
			return nil, fmt.Errorf("parallel group %q has a completion checkpoint for unknown child %q", step.ID, id)
		}
	}
	for _, root := range workflow.Steps {
		outcome := parallelTaskPending
		if saved, ok := e.state.Steps[root.ID]; ok {
			if saved.StepID != root.ID {
				e.stateMu.RUnlock()
				return nil, fmt.Errorf("external step %q has mismatched saved identity %q", root.ID, saved.StepID)
			}
			outcome = parallelResultOutcome(saved)
			if saved.RerunOnResume {
				outcome = parallelTaskPending
			}
		}
		external[root.ID] = outcome
	}
	for i := range children {
		saved, ok := e.state.Steps[children[i].ID]
		if raw, recorded := groupState.ChildCheckpoints[children[i].ID]; recorded {
			if !ok {
				e.stateMu.RUnlock()
				return nil, fmt.Errorf("parallel child %q lost its checkpointed result; refusing replay", children[i].ID)
			}
			var err error
			bindings[i], hooksSettled[i], err = decodeParallelChildCheckpoint(step, &children[i], saved, raw)
			if err != nil {
				e.stateMu.RUnlock()
				return nil, err
			}
			receipts[i] = raw
			if _, inFlight := e.state.InFlightSteps[children[i].ID]; inFlight || !retainParallelChild(&children[i], saved, false) {
				e.stateMu.RUnlock()
				return nil, fmt.Errorf("parallel child %q has contradictory completion and replay evidence", children[i].ID)
			}
		}
		if ok {
			if saved.StepID != children[i].ID {
				e.stateMu.RUnlock()
				return nil, fmt.Errorf("parallel child %q has mismatched saved identity %q; refusing replay", children[i].ID, saved.StepID)
			}
			_, inFlight := e.state.InFlightSteps[children[i].ID]
			if retainParallelChild(&children[i], saved, inFlight) {
				// Legacy scalar results still restore from their result itself.
				// A foreach summary cannot reconstruct its collection, however.
				if saved.Status == StatusCompleted && children[i].OutputVar != "" && stepOwnsForeachOutputVar(&children[i]) && receipts[i] == "" {
					e.stateMu.RUnlock()
					return nil, fmt.Errorf("completed parallel child %q lacks an aggregate output checkpoint; inspect its prior output before an explicit reset", children[i].ID)
				}
				results[i] = saved
				completedBodies[i] = true
				if saved.Status == StatusCompleted && len(children[i].OnSuccess) > 0 && !hooksSettled[i] {
					recoverHooks[i] = true
				} else {
					retained[i] = true
				}
			}
		}
	}
	e.stateMu.RUnlock()

	graph, err := newParallelTaskGraph(tasks, external)
	if err != nil {
		return nil, err
	}
	if err := graph.validateRetained(completedBodies); err != nil {
		return nil, err
	}
	if err := graph.validateRetained(retained); err != nil {
		return nil, err
	}

	// Explicit checkpoint-failure cleanup remains allowed by the canonical
	// lifecycle, but ordinary bodies cannot bypass a failed checkpoint.
	persist := func() error {
		err := e.persistState()
		cleanup, _ := ctx.Value(checkpointCleanupKey{}).(bool)
		if cleanup {
			return nil
		}
		return err
	}
	e.beginParallelState(step.ID, len(children))
	if err := persist(); err != nil {
		return results, err
	}

	usedPanes := make(map[string]bool)
	var panesMu sync.Mutex
	outputGroups := parallelOutputVarGroups(children)
	limit := e.limits.SubstepParallelMax
	if limit <= 0 {
		limit = DefaultSubstepParallelMax
	}
	err = graph.run(ctx, limit, onError == ErrorActionFailFast, retained,
		func(i int) error {
			e.markParallelSubstepStarted(step.ID, children[i].ID)
			return persist()
		},
		func(childCtx context.Context, i int) parallelTaskOutcome {
			if recoverHooks[i] {
				// Pending tails use bounded worker slots, not the serial restore
				// callback: unrelated ready children must remain runnable.
				e.varMu.Lock()
				StoreStepOutput(e.state, children[i].ID, results[i].Output, results[i].ParsedData)
				if name := children[i].OutputVar; name != "" && len(outputGroups[name]) == 1 {
					delete(e.state.Variables, name+"_parsed")
					if stepOwnsForeachOutputVar(&children[i]) {
						delete(e.state.Variables, name)
						for key, value := range bindings[i] {
							e.state.Variables[key] = cloneInterfaceValue(value)
						}
					} else {
						storeSingleParallelOutputVar(e.state, name, results[i])
					}
				}
				e.varMu.Unlock()
				hookRecoveryErrors[i] = e.resumeParallelSuccessHooks(childCtx, &children[i], workflow)
				hooksSettled[i] = hookRecoveryErrors[i] == nil
				receipts[i] = "" // Persist the new tail status after this attempt.
				if err := hookRecoveryErrors[i]; err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return parallelTaskCancelled
					}
					return parallelTaskFailed
				}
				return parallelTaskSucceeded
			}
			capture := &parallelOutputCapture{stepID: children[i].ID}
			childCtx = context.WithValue(childCtx, parallelOutputCaptureKey{}, capture)
			results[i] = e.executeParallelStep(childCtx, &children[i], workflow, usedPanes, &panesMu)
			bindings[i] = capture.outputs
			hooksSettled[i] = childCtx.Err() == nil && e.checkpointFailure() == nil
			return parallelResultOutcome(results[i])
		},
		func(i int, outcome parallelTaskOutcome, blocked []string) error {
			if results[i].StepID == "" {
				now := time.Now()
				results[i] = StepResult{StepID: children[i].ID, StartedAt: now, FinishedAt: now}
				switch outcome {
				case parallelTaskBlocked:
					results[i].Status = StatusSkipped
					results[i].SkipKind = SkipKindFailedDependency
					results[i].SkipReason = fmt.Sprintf("dependency failed: %v", blocked)
				case parallelTaskCancelled:
					results[i].Status = StatusCancelled
					results[i].SkipKind = SkipKindCancelled
					results[i].SkipReason = "cancelled before parallel child dispatch"
				default:
					return fmt.Errorf("parallel child %q produced no result", children[i].ID)
				}
			}
			var receiptErr error
			if retainParallelChild(&children[i], results[i], false) && receipts[i] == "" {
				receipts[i], receiptErr = encodeParallelChildCheckpoint(step, &children[i], results[i], bindings[i], hooksSettled[i])
			}
			e.stateMu.Lock()
			e.state.Steps[children[i].ID] = results[i]
			if receipts[i] != "" {
				if e.state.ParallelState == nil {
					e.state.ParallelState = make(map[string]ParallelGroupState)
				}
				group := e.state.ParallelState[step.ID]
				// Copy on write keeps every previously captured map immutable.
				checkpoints := make(map[string]string, len(group.ChildCheckpoints)+1)
				for id, raw := range group.ChildCheckpoints {
					checkpoints[id] = raw
				}
				checkpoints[children[i].ID] = receipts[i]
				group.ChildCheckpoints = checkpoints
				e.state.ParallelState[step.ID] = group
			}
			e.state.UpdatedAt = time.Now()
			e.stateMu.Unlock()
			if results[i].Status == StatusCompleted {
				// Rebuild canonical outputs for adopted results too. No global
				// unscoped aliases are introduced into concurrent nested bodies.
				e.varMu.Lock()
				StoreStepOutput(e.state, children[i].ID, results[i].Output, results[i].ParsedData)
				if name := children[i].OutputVar; name != "" && stepOwnsForeachOutputVar(&children[i]) && receiptErr == nil {
					// Captured at the foreach aggregation boundary, before hooks
					// or sibling work can overwrite shared runtime aliases.
					delete(e.state.Variables, name)
					delete(e.state.Variables, name+"_parsed")
					for key, value := range bindings[i] {
						e.state.Variables[key] = cloneInterfaceValue(value)
					}
				}
				if name := children[i].OutputVar; name != "" && len(outputGroups[name]) == 1 && !stepOwnsForeachOutputVar(&children[i]) {
					// A unique producer's named output is ready for its children.
					// Shared names still use the existing whole-group merge modes.
					delete(e.state.Variables, name+"_parsed")
					storeSingleParallelOutputVar(e.state, name, results[i])
				}
				e.varMu.Unlock()
			}
			e.markParallelSubstepFinished(step.ID, children[i].ID, results[i].Status)
			if receiptErr != nil {
				// This failure happened AFTER the producer's effects. Treat it
				// like the branch checkpoint boundary: latch the canonical fatal
				// storage error and cancel the whole workflow, not just this body.
				// The completed result above remains available for inspection.
				e.stateMu.RLock()
				runID := e.state.RunID
				e.stateMu.RUnlock()
				e.persistMu.Lock()
				receiptErr = e.failCheckpointLocked(runID, receiptErr)
				e.persistMu.Unlock()
			}
			if err := persist(); err != nil {
				return err
			}
			if hookRecoveryErrors[i] != nil {
				return hookRecoveryErrors[i]
			}
			if recoverHooks[i] && !hooksSettled[i] {
				if err := e.checkpointFailure(); err != nil {
					return err
				}
				return ctx.Err()
			}
			// A binding error cannot justify losing the completed result or
			// dispatching its consumers. Its missing receipt stops future
			// aggregate adoption without repeating the child's effects.
			return receiptErr
		})
	if err != nil {
		return results, err
	}
	e.completeParallelState(step.ID)
	return results, persist()
}

// A retained hook body may itself have been interrupted in its success tail.
// The ordinary walker skips that completed body, so visit its retained parents
// first, using the SAME depth and runtime-address rules. Only the canonical
// walker dispatches missing/unfinished hooks; no completed body is re-executed.
func (e *Executor) resumeParallelSuccessHooks(ctx context.Context, parent *Step, workflow *Workflow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkpointFailure(); err != nil {
		return err
	}
	depth := onSuccessDepth(ctx)
	if depth < maxOnSuccessDepth {
		childCtx := withOnSuccessDepth(ctx, depth+1)
		for i, child := range parent.OnSuccess {
			child.ID = onSuccessChildStepID(parent.ID, child.ID, i+1)
			e.stateMu.RLock()
			prior, exists := e.state.Steps[child.ID]
			_, inFlight := e.state.InFlightSteps[child.ID]
			e.stateMu.RUnlock()
			if exists && prior.StepID != child.ID {
				return fmt.Errorf("success hook %q has mismatched saved identity; refusing recovery", child.ID)
			}
			if exists && !shouldRerunStep(prior) && inFlight {
				return fmt.Errorf("success hook %q has contradictory in-flight completion evidence", child.ID)
			}
			if exists && prior.Status == StatusCompleted && !shouldRerunStep(prior) && len(child.OnSuccess) > 0 {
				if err := e.resumeParallelSuccessHooks(childCtx, &child, workflow); err != nil {
					return err
				}
			}
		}
	}
	e.runOnSuccessSteps(ctx, parent, workflow)
	if err := e.checkpointFailure(); err != nil {
		return err
	}
	return ctx.Err()
}

// A direct parallel child carries this private capture through its canonical
// lifecycle. Nested foreach bodies and success hooks have different IDs and
// cannot overwrite the parent's captured binding. The child joins its work
// before returning; its scheduler reads this capture only after that return.
type parallelOutputCaptureKey struct{}

type parallelOutputCapture struct {
	stepID  string
	outputs map[string]interface{}
}

func captureParallelForeachOutputs(ctx context.Context, stepID string, outputs map[string]interface{}) {
	capture, _ := ctx.Value(parallelOutputCaptureKey{}).(*parallelOutputCapture)
	if capture != nil && capture.stepID == stepID {
		capture.outputs = outputs
	}
}

// The caller holds varMu through aggregation and this snapshot. Returning an
// independent binding at that boundary avoids a later read of global aliases
// that another child or a success hook could already have replaced.
func snapshotForeachOutputBindings(state *ExecutionState, name string) map[string]interface{} {
	outputs := make(map[string]interface{}, 2)
	for _, key := range []string{name, name + "_parsed"} {
		if value, ok := state.Variables[key]; ok {
			outputs[key] = cloneInterfaceValue(value)
		}
	}
	return outputs
}

// Strings make receipts immutable across execution-state snapshots. Each
// receipt binds the complete child lifecycle result and its owned aggregate
// to both definitions. It lives in the SAME checkpoint as the child result,
// not a second state file, and follows its group's existing reset scope.
type parallelChildCheckpoint struct {
	Version        int                    `json:"version"`
	ParentHash     string                 `json:"parent_hash"`
	DefinitionHash string                 `json:"definition_hash"`
	ResultHash     string                 `json:"result_hash"`
	Outputs        map[string]interface{} `json:"outputs"`
	HooksSettled   bool                   `json:"hooks_settled"`
}

const parallelChildCheckpointVersion = 2

func parallelCheckpointHash(value interface{}) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Use the same generic JSON representation as LoadState. In particular,
	// nested typed foreach results become maps on disk; struct field order
	// must not make an unchanged result fail its own recovery check.
	var canonical interface{}
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", err
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func parallelChildResultHash(result StepResult, outputs map[string]interface{}, hooksSettled bool) (string, error) {
	return parallelCheckpointHash(struct {
		Result       StepResult             `json:"result"`
		Outputs      map[string]interface{} `json:"outputs,omitempty"`
		HooksSettled bool                   `json:"hooks_settled"`
	}{result, outputs, hooksSettled})
}

func encodeParallelChildCheckpoint(parent, child *Step, result StepResult, outputs map[string]interface{}, hooksSettled bool) (string, error) {
	if !retainParallelChild(child, result, false) || result.StepID != child.ID {
		return "", fmt.Errorf("parallel child %q has no settled lifecycle to checkpoint", child.ID)
	}
	if err := validateParallelChildBindings(child, result, outputs); err != nil {
		return "", err
	}
	parentHash, err := parallelCheckpointHash(parent)
	if err != nil {
		return "", fmt.Errorf("hash parallel group definition: %w", err)
	}
	definitionHash, err := parallelCheckpointHash(child)
	if err != nil {
		return "", fmt.Errorf("hash parallel child definition: %w", err)
	}
	// Skipped children and definitions with no hooks have no pending tail.
	hooksSettled = hooksSettled || result.Status != StatusCompleted || len(child.OnSuccess) == 0
	resultHash, err := parallelChildResultHash(result, outputs, hooksSettled)
	if err != nil {
		return "", fmt.Errorf("hash parallel child result: %w", err)
	}
	raw, err := json.Marshal(parallelChildCheckpoint{parallelChildCheckpointVersion, parentHash, definitionHash, resultHash, outputs, hooksSettled})
	return string(raw), err
}

func decodeParallelChildCheckpoint(parent, child *Step, result StepResult, raw string) (map[string]interface{}, bool, error) {
	invalid := func(reason string) (map[string]interface{}, bool, error) {
		return nil, false, fmt.Errorf("parallel child %q has invalid completion evidence: %s", child.ID, reason)
	}
	var receipt parallelChildCheckpoint
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return invalid(err.Error())
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF || receipt.Version != parallelChildCheckpointVersion {
		return invalid("unsupported version or trailing data")
	}
	if !retainParallelChild(child, result, false) || result.StepID != child.ID {
		return invalid("result is not the recorded completed lifecycle")
	}
	parentHash, err := parallelCheckpointHash(parent)
	if err != nil || parentHash != receipt.ParentHash {
		return invalid("parallel group definition changed")
	}
	definitionHash, err := parallelCheckpointHash(child)
	if err != nil || definitionHash != receipt.DefinitionHash {
		return invalid("child definition changed")
	}
	resultHash, err := parallelChildResultHash(result, receipt.Outputs, receipt.HooksSettled)
	if err != nil || resultHash != receipt.ResultHash {
		return invalid("result or output bindings changed")
	}
	if err := validateParallelChildBindings(child, result, receipt.Outputs); err != nil {
		return invalid(err.Error())
	}
	// Restore the same primary collection types produced by fresh execution,
	// not generic []interface{} / map[string]interface{} after JSON decoding.
	if child.OutputVar != "" && stepOwnsForeachOutputVar(child) {
		name := child.OutputVar
		switch value := receipt.Outputs[name].(type) {
		case []interface{}:
			values := make([]string, len(value))
			for i, item := range value {
				text, ok := item.(string)
				if !ok {
					return invalid("aggregate contains a non-string output")
				}
				values[i] = text
			}
			receipt.Outputs[name] = values
		case map[string]interface{}:
			values := make(map[string]string, len(value))
			for key, item := range value {
				text, ok := item.(string)
				if !ok {
					return invalid("collection contains a non-string output")
				}
				values[key] = text
			}
			receipt.Outputs[name] = values
		}
	}
	return receipt.Outputs, receipt.HooksSettled, nil
}

func validateParallelChildBindings(child *Step, result StepResult, outputs map[string]interface{}) error {
	name := child.OutputVar
	if result.Status != StatusCompleted || name == "" || !stepOwnsForeachOutputVar(child) {
		if len(outputs) > 0 {
			return fmt.Errorf("child %q contains foreign output bindings", child.ID)
		}
		return nil
	}
	if outputs == nil {
		return fmt.Errorf("completed parallel child %q lacks its captured aggregate output", child.ID)
	}
	for key := range outputs {
		if key != name && key != name+"_parsed" {
			return fmt.Errorf("child %q contains foreign output binding %q", child.ID, key)
		}
	}
	config, _ := foreachConfigForStep(child)
	mode := effectiveForeachOutputVarMode(child, config)
	value, exists := outputs[name]
	if !exists {
		if mode == OutputVarModeLast && len(outputs) == 0 {
			return nil // No last writer is an explicit absence, not stale data.
		}
		return fmt.Errorf("child %q is missing its primary collection", child.ID)
	}
	badShape := func() error { return fmt.Errorf("child %q has an invalid %s output binding", child.ID, mode) }
	switch mode {
	case OutputVarModeLast:
		if _, ok := value.(string); !ok {
			return badShape()
		}
	case OutputVarModeCollect:
		keys := make(map[string]bool)
		switch values := value.(type) {
		case map[string]string:
			if values == nil {
				return badShape()
			}
			for key := range values {
				keys[key] = true
			}
		case map[string]interface{}:
			if values == nil {
				return badShape()
			}
			for key, item := range values {
				if _, ok := item.(string); !ok {
					return badShape()
				}
				keys[key] = true
			}
		default:
			return badShape()
		}
		if parsed, exists := outputs[name+"_parsed"]; exists {
			values, ok := parsed.(map[string]interface{})
			if !ok {
				return badShape()
			}
			for key := range values {
				if !keys[key] {
					return badShape()
				}
			}
		}
	default:
		length := 0
		switch values := value.(type) {
		case []string:
			if values == nil {
				return badShape()
			}
			length = len(values)
		case []interface{}:
			if values == nil {
				return badShape()
			}
			length = len(values)
			for _, item := range values {
				if _, ok := item.(string); !ok {
					return badShape()
				}
			}
		default:
			return badShape()
		}
		if parsed, exists := outputs[name+"_parsed"]; exists {
			values, ok := parsed.([]interface{})
			if !ok || len(values) != length {
				return badShape()
			}
		}
	}
	return nil
}

// A foreach container owns a collection, not the scalar summary which the
// existing parallel shared-name merge consumes. Require distinct direct
// aliases rather than silently flattening a collection into those summaries.
func validateParallelAggregateAliases(children []Step) error {
	for i, child := range children {
		if child.OutputVar == "" || !stepOwnsForeachOutputVar(&child) {
			continue
		}
		for j, other := range children {
			if i == j || other.OutputVar == "" {
				continue
			}
			if child.OutputVar == other.OutputVar || child.OutputVar+"_parsed" == other.OutputVar || other.OutputVar+"_parsed" == child.OutputVar {
				return fmt.Errorf("foreach child %q and sibling %q have overlapping output aliases; use distinct collection names", child.ID, other.ID)
			}
		}
	}
	return nil
}

func retainParallelChild(child *Step, result StepResult, inFlight bool) bool {
	// WaitNone command completion proves launch, not settlement. As in branch
	// recovery, let the canonical command journal decide whether the original
	// process can be adopted, must remain unresolved, or may safely run again.
	if inFlight || (child.Command != "" && child.Wait == WaitNone) {
		return false
	}
	return !shouldRerunStep(result) && parallelResultOutcome(result) == parallelTaskSucceeded
}

// parallelChildDefinitions computes runtime addresses without editing the
// authored step or its nested slices. Static validation and body scheduling
// use this same identity rule, including anonymous and already-scoped IDs.
func parallelChildDefinitions(parent *Step) ([]Step, []parallelTask, error) {
	children := append([]Step(nil), parent.Parallel.Steps...)
	tasks := make([]parallelTask, len(children))
	for i := range children {
		authorID := children[i].ID
		children[i].ID = scopedChildStepID(parent.ID, authorID, i+1)
		if children[i].ID == parent.ID {
			return nil, nil, fmt.Errorf("parallel child %q collides with its containing step", children[i].ID)
		}
		tasks[i] = parallelTask{id: children[i].ID, aliases: []string{authorID}, dependsOn: children[i].DependsOn}
	}
	return children, tasks, nil
}

// resolveParallelChildDependencies keeps the outer workflow graph consistent
// with the scheduler that actually dispatches each body. Canonical graph keys
// remain authored IDs; a scoped sibling reference is translated only in the
// graph's private edge table. No workflow or receipt hash input is rewritten.
func (g *DependencyGraph) resolveParallelChildDependencies(workflow *Workflow) []DependencyError {
	external := make(map[string]parallelTaskOutcome, len(workflow.Steps))
	for _, root := range workflow.Steps {
		external[root.ID] = parallelTaskSucceeded // Structure only; runtime checks settlement.
	}
	var problems []DependencyError
	var visit func(*Step, string)
	visit = func(step *Step, runtimeID string) {
		if len(step.Parallel.Steps) > 0 {
			parent := *step
			parent.ID = runtimeID
			children, tasks, err := parallelChildDefinitions(&parent)
			var byName map[string]int
			if err == nil {
				err = validateParallelAggregateAliases(children)
			}
			if err == nil {
				byName, err = indexParallelTasks(tasks, external)
			}
			if err != nil {
				problems = append(problems, DependencyError{Type: "parallel_identity", Steps: []string{step.ID}, Message: err.Error()})
				return
			}
			for i := range step.Parallel.Steps {
				child := &step.Parallel.Steps[i]
				// A flat graph key cannot silently stand for two authored steps.
				if g.steps[child.ID] != child {
					problems = append(problems, DependencyError{Type: "parallel_identity", Steps: []string{child.ID}, Message: fmt.Sprintf("parallel child %q has a duplicate authored graph identity", child.ID)})
					continue
				}
				seen := make(map[string]bool)
				edges := make([]string, 0, len(child.DependsOn))
				for _, dep := range child.DependsOn {
					if sibling, ok := byName[dep]; ok {
						dep = step.Parallel.Steps[sibling].ID
					}
					if !seen[dep] {
						seen[dep] = true
						edges = append(edges, dep)
					}
				}
				g.edges[child.ID] = edges
				visit(child, children[i].ID)
			}
		}
		if step.Loop != nil {
			// Loop iteration addresses are materialized only at execution. Its
			// authored body still has a local namespace for static validation.
			for i := range step.Loop.Steps {
				child := &step.Loop.Steps[i]
				visit(child, child.ID)
			}
		}
	}
	for i := range workflow.Steps {
		visit(&workflow.Steps[i], workflow.Steps[i].ID)
	}

	// Reverse edges and readiness counters must describe the normalized edges,
	// not the original aliases. Rebuild before inherited container gates are
	// added and before anything can enter the ready queue.
	g.reverse = make(map[string][]string)
	ids := make([]string, 0, len(g.edges))
	for id := range g.edges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g.inDegree[id], g.remainingDeps[id] = len(g.edges[id]), len(g.edges[id])
		for _, dep := range g.edges[id] {
			g.reverse[dep] = append(g.reverse[dep], id)
		}
	}
	return problems
}

func parallelResultOutcome(result StepResult) parallelTaskOutcome {
	switch result.Status {
	case StatusCompleted:
		return parallelTaskSucceeded
	case StatusSkipped:
		if shouldRerunStep(result) {
			if result.SkipKind == SkipKindCancelled || strings.HasPrefix(result.SkipReason, "cancelled") {
				return parallelTaskCancelled
			}
			return parallelTaskBlocked
		}
		return parallelTaskSucceeded
	case StatusFailed:
		return parallelTaskFailed
	case StatusCancelled:
		return parallelTaskCancelled
	default:
		return parallelTaskPending
	}
}

// resumeOutputEvent restores an output at its recorded lifecycle boundary.
// Group merge events follow ordinary child writes at the same timestamp, and
// nested merges precede enclosing merges. This avoids map-iteration-dependent
// replacement of a shared collection with a single child's scalar result.
type resumeOutputEvent struct {
	stepID     string
	finishedAt time.Time
	output     string
	parsedData interface{}
	outputVar  string
	merge      bool
	depth      int
	outputs    map[string]interface{}
	clear      []string
}

// prepareCompletedParallelOutputs validates all binding evidence before resume
// marks graph nodes, prunes results, or changes output variables. A completed
// static outer parallel body will not enter runParallelChildren again; its
// nested binding receipts must therefore be consumed here. Dynamic bodies are
// restored when their actual materialized parent re-enters its own scheduler.
// Caller holds stateMu. Returned values do not alias checkpoint maps.
func prepareCompletedParallelOutputs(graph *DependencyGraph, state *ExecutionState) ([]resumeOutputEvent, map[string]bool, error) {
	var events []resumeOutputEvent
	owned := make(map[string]bool)
	if graph == nil || state == nil {
		return events, owned, nil
	}
	external := make(map[string]parallelTaskOutcome)
	var roots []string
	for id, step := range graph.steps {
		if graph.container[id] != "" {
			continue
		}
		outcome := parallelTaskPending
		if result, ok := state.Steps[id]; ok && !resumeResultInFlight(state, id, result) {
			outcome = parallelResultOutcome(result)
		}
		external[id] = outcome
		if len(step.Parallel.Steps) > 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	var visit func(*Step, int) error
	visit = func(parent *Step, depth int) error {
		group, ok := state.ParallelState[parent.ID]
		if !ok || group.StepID != parent.ID || group.Total != len(parent.Parallel.Steps) ||
			!group.AllSubstepsSettled || len(group.InFlightStepIDs) != 0 {
			return fmt.Errorf("completed parallel group %q lacks settled body evidence", parent.ID)
		}
		parentResult, ok := state.Steps[parent.ID]
		if !ok || parentResult.StepID != parent.ID || resumeResultInFlight(state, parent.ID, parentResult) ||
			(parentResult.Status != StatusCompleted && parentResult.Status != StatusFailed) {
			return fmt.Errorf("completed parallel group %q has inconsistent result evidence", parent.ID)
		}
		children, tasks, err := parallelChildDefinitions(parent)
		if err != nil {
			return err
		}
		if err := validateParallelAggregateAliases(children); err != nil {
			return err
		}
		body, err := newParallelTaskGraph(tasks, external)
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(children))
		retained := make(map[int]bool)
		results := make([]StepResult, len(children))
		// Reuse the canonical output merger against isolated variables. No
		// executor lifecycle, transport, checkpoint write or hook is invoked.
		scratch := &Executor{state: &ExecutionState{RunID: state.RunID, Variables: make(map[string]interface{})}}
		event := resumeOutputEvent{stepID: parent.ID, finishedAt: parentResult.FinishedAt, merge: true, depth: depth}
		for i := range children {
			child := &children[i]
			known[child.ID] = true
			result, ok := state.Steps[child.ID]
			if !ok || result.StepID != child.ID || resumeResultInFlight(state, child.ID, result) {
				return fmt.Errorf("completed parallel group %q has missing or unfinished child %q", parent.ID, child.ID)
			}
			if !parentResult.FinishedAt.IsZero() && result.FinishedAt.After(parentResult.FinishedAt) {
				return fmt.Errorf("completed parallel group %q precedes child %q settlement", parent.ID, child.ID)
			}
			switch result.Status {
			case StatusCompleted, StatusSkipped, StatusFailed:
			default:
				return fmt.Errorf("completed parallel group %q contains unsettled child %q", parent.ID, child.ID)
			}
			if child.Command != "" && child.Wait == WaitNone {
				return fmt.Errorf("completed parallel group %q hides background command %q; inspect its lifecycle before an explicit reset", parent.ID, child.ID)
			}
			results[i] = result
			retained[i] = retainParallelChild(child, result, false)
			var bindings map[string]interface{}
			if raw, recorded := group.ChildCheckpoints[child.ID]; recorded {
				var hooksSettled bool
				bindings, hooksSettled, err = decodeParallelChildCheckpoint(parent, child, result, raw)
				if err != nil {
					return err
				}
				if result.Status == StatusCompleted && len(child.OnSuccess) > 0 && !hooksSettled {
					return fmt.Errorf("completed parallel group %q hides unfinished success hooks for %q", parent.ID, child.ID)
				}
			} else if result.Status == StatusCompleted && stepOwnsForeachOutputVar(child) && child.OutputVar != "" {
				return fmt.Errorf("completed parallel child %q lacks an aggregate output checkpoint", child.ID)
			}
			if name := child.OutputVar; name != "" {
				if result.Status == StatusCompleted {
					event.clear = append(event.clear, name, name+"_parsed")
					if stepOwnsForeachOutputVar(child) {
						for key, value := range bindings {
							scratch.state.Variables[key] = cloneInterfaceValue(value)
						}
					}
				}
			}
			// A failed inner group can still have settled outputs under its
			// completed enclosing group's continue policy. Preserve those
			// terminal records too, so a second resume sees the same evidence.
			if len(child.Parallel.Steps) > 0 && result.Status != StatusSkipped {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
			owned[child.ID] = true
		}
		for id := range group.ChildCheckpoints {
			if !known[id] {
				return fmt.Errorf("completed parallel group %q contains foreign child checkpoint %q", parent.ID, id)
			}
		}
		if err := body.validateRetained(retained); err != nil {
			return err
		}
		for name, indices := range parallelOutputVarGroups(children) {
			// Aggregate/collect publish a value even if all writers failed;
			// a failed unique or last-mode writer publishes nothing and must
			// not erase a prior input or another producer's successful value.
			if len(indices) > 1 && parallelGroupOutputVarMode(children, indices) != OutputVarModeLast {
				event.clear = append(event.clear, name, name+"_parsed")
			}
		}
		scratch.storeParallelOutputVars(parent, results, buildParallelCompletionOrder(results))
		event.outputs = cloneInterfaceMap(scratch.state.Variables)
		events = append(events, event)
		return nil
	}
	for _, id := range roots {
		result, ok := state.Steps[id]
		if !ok || result.Status != StatusCompleted || resumeResultInFlight(state, id, result) {
			continue // Unfinished parents re-enter the ordinary body adapter.
		}
		// A legacy group with no progress record cannot establish its merge
		// boundary. Groups with settled progress can reconstruct scalar
		// merges, including an all-failed continue body with no successful
		// child receipts. Collections always require their binding receipt.
		if _, recorded := state.ParallelState[id]; !recorded {
			continue
		}
		parent := *graph.steps[id]
		parent.ID = id
		if err := visit(&parent, 0); err != nil {
			return nil, nil, err
		}
	}
	return events, owned, nil
}

// A nominal completed result is not settlement while a lifecycle marker or
// background-command cleanup still says it needs work. Check this BEFORE any
// graph completion or output restoration, not after deleting the marker.
func resumeResultInFlight(state *ExecutionState, id string, result StepResult) bool {
	if _, inFlight := state.InFlightSteps[id]; inFlight || result.RerunOnResume {
		return true
	}
	record, ok := state.CommandExecutions[id]
	return ok && record.Status == commandExecutionSettled && record.Background && record.Result.RerunOnResume
}

// restoreResumeOutputEvents is the sole writer for retained output rebuilds.
// Caller holds varMu. No event can dispatch work or consult mutable live aliases
// as evidence of what an already completed producer originally returned.
func restoreResumeOutputEvents(state *ExecutionState, events []resumeOutputEvent) {
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.finishedAt.Equal(b.finishedAt) {
			return a.finishedAt.Before(b.finishedAt)
		}
		if a.merge != b.merge {
			return !a.merge
		}
		if a.merge && a.depth != b.depth {
			return a.depth > b.depth
		}
		return a.stepID < b.stepID
	})
	for _, event := range events {
		if event.merge {
			for _, key := range event.clear {
				delete(state.Variables, key)
			}
			for key, value := range event.outputs {
				state.Variables[key] = cloneInterfaceValue(value)
			}
			continue
		}
		delete(state.Variables, "steps."+event.stepID+".data")
		StoreStepOutput(state, event.stepID, event.output, cloneInterfaceValue(event.parsedData))
		if event.outputVar != "" {
			state.Variables[event.outputVar] = event.output
			delete(state.Variables, event.outputVar+"_parsed")
			if event.parsedData != nil {
				state.Variables[event.outputVar+"_parsed"] = cloneInterfaceValue(event.parsedData)
			}
		}
	}
}
