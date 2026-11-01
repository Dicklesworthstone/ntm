package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var ErrStepCountLimit = errors.New("pipeline runtime step admission limit exceeded")
var ErrMaterializedStepLimit = errors.New("pipeline materialized step limit exceeded")

// Structured step outcomes cross several container and recovery boundaries.
// Preserve their local hard-admission classification when a hook must return
// an error, without confusing an unrelated sibling's sticky attempt failure
// with a failure inside a completed container.
type stepAdmissionPropagationError struct{ message string }

func (e *stepAdmissionPropagationError) Error() string { return e.message }

func isStepAdmissionError(err error) bool {
	var propagated *stepAdmissionPropagationError
	return errors.Is(err, ErrStepCountLimit) || errors.Is(err, ErrMaterializedStepLimit) || errors.As(err, &propagated)
}

func findStepAdmissionError(err *StepError) *StepError {
	if err == nil {
		return nil
	}
	if err.Type == "step_limit" {
		return err
	}
	for i := range err.Aggregated {
		if cause := findStepAdmissionError(&err.Aggregated[i]); cause != nil {
			return cause
		}
	}
	return nil
}

// StepAdmissionState records capacity, never permission to replay effects.
// IDs remain admitted through scoped resets; a new run starts a new ledger.
// Finalization has its own equally bounded allowance so ordinary work cannot
// consume the capacity reserved for authored release and compensation roots.
type StepAdmissionState struct {
	Version      int             `json:"version"`
	Work         map[string]bool `json:"work,omitempty"`
	Finalization map[string]bool `json:"finalization,omitempty"`
}

type stepFinalizationKey struct{}
type materializationOwnerKey struct{}
type materializationOwner struct{ releases []func() }

// A branch decoder returns its body to the branch executor. Keep that body's
// lease with the consuming call, rather than releasing it at decoder return.
func withMaterializationOwner(ctx context.Context) (context.Context, func()) {
	owner := &materializationOwner{}
	return context.WithValue(ctx, materializationOwnerKey{}, owner), func() {
		for i := len(owner.releases) - 1; i >= 0; i-- {
			owner.releases[i]()
		}
		owner.releases = nil
	}
}

func (e *Executor) retainDefinitionMaterialization(ctx context.Context, value interface{}) (func(), error) {
	release, err := e.acquireDefinitionCopies(ctx, value, 1)
	if err != nil {
		return nil, err
	}
	if owner, ok := ctx.Value(materializationOwnerKey{}).(*materializationOwner); ok {
		owner.releases = append(owner.releases, release)
		return func() {}, nil
	}
	return release, nil
}

type stepAdmissionAttempt struct {
	materialized int
	failure      error
}

func finalizationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, stepFinalizationKey{}, true)
}

func isFinalization(ctx context.Context) bool {
	value, _ := ctx.Value(stepFinalizationKey{}).(bool)
	return value
}

func cloneStepAdmissions(source *StepAdmissionState) *StepAdmissionState {
	if source == nil {
		return nil
	}
	copy := &StepAdmissionState{Version: source.Version, Work: make(map[string]bool, len(source.Work)), Finalization: make(map[string]bool, len(source.Finalization))}
	for id, admitted := range source.Work {
		copy.Work[id] = admitted
	}
	for id, admitted := range source.Finalization {
		copy.Finalization[id] = admitted
	}
	return copy
}

func validateStepAdmissions(ledger *StepAdmissionState) error {
	if ledger == nil {
		return nil
	}
	if ledger.Version != 1 {
		return fmt.Errorf("unsupported step admission version %d", ledger.Version)
	}
	for _, entries := range []map[string]bool{ledger.Work, ledger.Finalization} {
		for id, admitted := range entries {
			if id == "" || id != strings.TrimSpace(id) || !admitted {
				return fmt.Errorf("invalid admitted runtime step identity %q", id)
			}
			if ledger.Work[id] && ledger.Finalization[id] {
				return fmt.Errorf("runtime step %q is admitted in conflicting phases", id)
			}
		}
	}
	return nil
}

func (e *Executor) stepAdmissionFailure(ctx context.Context) error {
	if isFinalization(ctx) {
		return nil
	}
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.stepAdmissionAttempt == nil {
		return nil
	}
	return e.stepAdmissionAttempt.failure
}

func (e *Executor) failStepAdmissionLocked(ctx context.Context, err error) error {
	if e.stepAdmissionAttempt == nil {
		e.stepAdmissionAttempt = &stepAdmissionAttempt{}
	}
	if !isFinalization(ctx) && e.stepAdmissionAttempt.failure == nil {
		e.stepAdmissionAttempt.failure = err
	}
	return err
}

// admitRuntimeIDs checks the whole batch before changing either phase's set.
// Existing IDs are free even for imported journals above today's limit. A
// failed batch adds no prospective launch markers or per-child failure records.
func (e *Executor) admitRuntimeIDs(ctx context.Context, ids []string) error {
	return e.admitRuntimeBatch(ctx, func(yield func(string) error) error {
		for _, id := range ids {
			if err := yield(id); err != nil {
				return err
			}
		}
		return nil
	})
}

// The iterator may generate a large expansion, but stops at the first denied
// identity. It must only enumerate IDs: stateMu stays held for atomic admission.
func (e *Executor) admitRuntimeBatch(ctx context.Context, visit func(func(string) error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return errors.New("runtime step admission requires execution state")
	}
	if e.stepAdmissionAttempt == nil {
		e.stepAdmissionAttempt = &stepAdmissionAttempt{}
	}
	if !isFinalization(ctx) && e.stepAdmissionAttempt.failure != nil {
		err := e.stepAdmissionAttempt.failure
		e.stateMu.Unlock()
		return err
	}
	if e.state.StepAdmissions == nil {
		e.state.StepAdmissions = &StepAdmissionState{Version: 1, Work: make(map[string]bool), Finalization: make(map[string]bool)}
	}
	ledger := e.state.StepAdmissions
	if ledger.Version != 1 {
		e.stateMu.Unlock()
		return fmt.Errorf("unsupported step admission version %d", ledger.Version)
	}
	if ledger.Work == nil {
		ledger.Work = make(map[string]bool)
	}
	if ledger.Finalization == nil {
		ledger.Finalization = make(map[string]bool)
	}
	entries, other := ledger.Work, ledger.Finalization
	if isFinalization(ctx) {
		entries, other = other, entries
	}
	limit := e.limits.EffectiveLimits().MaxStepCountTotal
	remaining := limit - len(entries)
	newIDs := make(map[string]bool)
	seen := make(map[string]bool)
	admissionErr := visit(func(id string) error {
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate runtime step admission %q", id)
		}
		seen[id] = true
		if other[id] {
			return fmt.Errorf("runtime step %q belongs to a different execution phase", id)
		}
		if entries[id] {
			return nil
		}
		if remaining <= len(newIDs) {
			return fmt.Errorf("%w: max_step_count_total=%d cannot admit additional runtime step %q; raise the attempt limit without resetting existing receipts", ErrStepCountLimit, limit, id)
		}
		newIDs[id] = true
		return nil
	})
	if admissionErr != nil {
		admissionErr = e.failStepAdmissionLocked(ctx, admissionErr)
		e.stateMu.Unlock()
		return admissionErr
	}
	for id := range newIDs {
		entries[id] = true
	}
	e.stateMu.Unlock()
	if len(newIDs) == 0 {
		return nil
	}
	err := e.persistState()
	cleanup, _ := ctx.Value(checkpointCleanupKey{}).(bool)
	if cleanup {
		// Preserve the established bounded best-effort on_cancel path when
		// storage has failed. Capacity is still enforced for this attempt.
		return nil
	}
	return err
}

func (e *Executor) runtimeIDAdmitted(id string) bool {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return e.state != nil && e.state.StepAdmissions != nil &&
		(e.state.StepAdmissions.Work[id] || e.state.StepAdmissions.Finalization[id])
}

func stepAdmissionResult(step *Step, err error) StepResult {
	now := time.Now()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StepResult{StepID: step.ID, Status: StatusCancelled, StartedAt: now, FinishedAt: now,
			SkipKind: SkipKindCancelledBeforeDispatch, SkipReason: err.Error()}
	}
	return StepResult{StepID: step.ID, Status: StatusFailed, StartedAt: now, FinishedAt: now, SkipKind: SkipKindAdmissionDenied,
		Error: &StepError{Type: "step_limit", Message: err.Error(), Timestamp: now}}
}

func stepAdmissionExecutionFailure(result StepResult, err error) StepResult {
	result.Status, result.FinishedAt = StatusFailed, time.Now()
	diagnostic := &StepError{Type: "step_limit", Message: err.Error(), Timestamp: result.FinishedAt}
	if result.Error != nil {
		diagnostic.Aggregated = []StepError{cloneStepErrorValue(*result.Error)}
	}
	result.Error = diagnostic
	return result
}

// A lease belongs to one attempt. Idempotent release cannot decrement the
// quota of a reused Executor. Nested planners refuse instead of deadlocking
// while waiting for capacity retained by their own ancestors.
func (e *Executor) acquireStepMaterialization(ctx context.Context, nodes int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.stateMu.Lock()
	if e.stepAdmissionAttempt == nil {
		e.stepAdmissionAttempt = &stepAdmissionAttempt{}
	}
	attempt := e.stepAdmissionAttempt
	limit := e.limits.EffectiveLimits().MaxMaterializedStepNodes
	if nodes < 0 || nodes > limit-attempt.materialized {
		err := e.failStepAdmissionLocked(ctx, fmt.Errorf("%w: max_materialized_step_nodes=%d cannot retain %d additional definition nodes (%d already retained)", ErrMaterializedStepLimit, limit, nodes, attempt.materialized))
		e.stateMu.Unlock()
		return nil, err
	}
	attempt.materialized += nodes
	e.stateMu.Unlock()
	released := false
	return func() {
		e.stateMu.Lock()
		defer e.stateMu.Unlock()
		if !released {
			released = true
			if e.stepAdmissionAttempt == attempt {
				attempt.materialized -= nodes
			}
		}
	}, nil
}

// definitionNodeCount walks literal structure without marshaling or cloning
// it. The queue itself is bounded by the node allowance, including raw branch
// bodies which otherwise allocate through a JSON round trip before dispatch.
func definitionNodeCount(value interface{}, limit int) (int, error) {
	var queue []interface{}
	count := 0
	tooLarge := func() error {
		return fmt.Errorf("%w: definition exceeds max_materialized_step_nodes=%d", ErrMaterializedStepLimit, limit)
	}
	var add func(interface{}) error
	add = func(value interface{}) error {
		switch values := value.(type) {
		case nil:
			return nil
		case []Step:
			if len(values) > limit-count-len(queue) {
				return tooLarge()
			}
			for i := range values {
				queue = append(queue, &values[i])
			}
		case []interface{}:
			if len(values) > limit-count-len(queue) {
				return tooLarge()
			}
			queue = append(queue, values...)
		case []map[string]interface{}:
			if len(values) > limit-count-len(queue) {
				return tooLarge()
			}
			for _, value := range values {
				queue = append(queue, value)
			}
		default:
			if count+len(queue) >= limit {
				return tooLarge()
			}
			queue = append(queue, value)
		}
		return nil
	}
	if err := add(value); err != nil {
		return 0, err
	}
	addBranches := func(value interface{}) error {
		if value == nil {
			return nil
		}
		branches := reflect.ValueOf(value)
		if branches.Kind() != reflect.Map || branches.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("unsupported literal branch collection %T", value)
		}
		if branches.Len() > limit-count-len(queue) {
			return tooLarge()
		}
		iterator := branches.MapRange()
		for iterator.Next() {
			if err := add(iterator.Value().Interface()); err != nil {
				return err
			}
		}
		return nil
	}
	for len(queue) > 0 {
		last := len(queue) - 1
		value := queue[last]
		queue[last] = nil
		queue = queue[:last]
		count++
		var children []interface{}
		switch node := value.(type) {
		case Step:
			copy := node
			count--
			if err := add(&copy); err != nil {
				return 0, err
			}
			continue
		case *Step:
			if node == nil {
				count--
				continue
			}
			children = append(children, node.OnSuccess, node.Parallel.Steps)
			if node.Loop != nil {
				children = append(children, node.Loop.Steps, node.Loop.Body)
			}
			for _, config := range []*ForeachConfig{node.Foreach, node.ForeachPane} {
				if config != nil {
					children = append(children, config.Steps, config.Body)
				}
			}
			if err := addBranches(node.Branches); err != nil {
				return 0, err
			}
		case map[string]interface{}:
			// The branch decoder uses encoding/json, whose struct field
			// matching is case-insensitive. Count every recognized spelling
			// conservatively, including duplicate case aliases.
			for key, value := range node {
				switch {
				case strings.EqualFold(key, "on_success"):
					if err := add(value); err != nil {
						return 0, err
					}
				case strings.EqualFold(key, "branches"):
					if err := addBranches(value); err != nil {
						return 0, err
					}
				case strings.EqualFold(key, "parallel"), strings.EqualFold(key, "loop"), strings.EqualFold(key, "foreach"), strings.EqualFold(key, "foreach_pane"):
					var nested []interface{}
					switch body := value.(type) {
					case nil, bool:
						// Parallel's boolean form carries no child definitions.
					case map[string]interface{}:
						for field, child := range body {
							if strings.EqualFold(field, "steps") || strings.EqualFold(field, "body") {
								if err := add(child); err != nil {
									return 0, err
								}
							}
						}
					case []interface{}, []Step, []map[string]interface{}:
						if strings.EqualFold(key, "parallel") {
							nested = append(nested, body)
						}
					case *ForeachConfig:
						if body != nil {
							nested = append(nested, body.Steps, body.Body)
						}
					case ForeachConfig:
						nested = append(nested, body.Steps, body.Body)
					case *LoopConfig:
						if body != nil {
							nested = append(nested, body.Steps, body.Body)
						}
					case LoopConfig:
						nested = append(nested, body.Steps, body.Body)
					case ParallelSpec:
						nested = append(nested, body.Steps)
					default:
						return 0, fmt.Errorf("unsupported literal %s configuration %T", key, value)
					}
					for _, child := range nested {
						if err := add(child); err != nil {
							return 0, err
						}
					}
				}
			}
		default:
			return 0, fmt.Errorf("unsupported literal step definition %T", value)
		}
		for _, child := range children {
			if err := add(child); err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

func (e *Executor) validateStepDefinitionCapacity(workflow *Workflow) error {
	remaining := e.limits.EffectiveLimits().MaxMaterializedStepNodes
	for _, steps := range [][]Step{workflow.Steps, workflow.Settings.OnCancel, workflow.PostPipelineSteps} {
		count, err := definitionNodeCount(steps, remaining)
		if err != nil {
			return err
		}
		remaining -= count
	}
	return nil
}

func (e *Executor) acquireDefinitionCopies(ctx context.Context, value interface{}, copies int) (func(), error) {
	limit := e.limits.EffectiveLimits().MaxMaterializedStepNodes
	count, err := definitionNodeCount(value, limit)
	if err == nil && (copies < 0 || (copies > 0 && count > limit/copies)) {
		err = fmt.Errorf("%w: %d copies exceed max_materialized_step_nodes=%d", ErrMaterializedStepLimit, copies, limit)
	}
	if err != nil {
		e.stateMu.Lock()
		err = e.failStepAdmissionLocked(ctx, err)
		e.stateMu.Unlock()
		return nil, err
	}
	return e.acquireStepMaterialization(ctx, count*copies)
}

// Import only identities evidenced by the saved state. This never infers that
// work terminated, and unknown legacy history cannot be reconstructed.
// Caller holds stateMu after normal resume evidence validation, before resets.
func (e *Executor) seedStepAdmissionsLocked(workflow *Workflow, graph *DependencyGraph) error {
	if err := validateStepAdmissions(e.state.StepAdmissions); err != nil {
		return err
	}
	ledger := cloneStepAdmissions(e.state.StepAdmissions)
	if ledger == nil {
		ledger = &StepAdmissionState{Version: 1, Work: make(map[string]bool), Finalization: make(map[string]bool)}
	}
	add := func(id string) {
		if id == "" || strings.HasSuffix(id, branchDecisionSuffix) {
			return
		}
		ownerID := strings.TrimSuffix(id, ".on_failure")
		_, cancel := resolveOnCancelRuntimeStep(workflow, ownerID)
		post := false
		if graph != nil {
			_, post = graph.resolvePostPipelineRuntimeStep(ownerID)
		}
		if cancel || post {
			ledger.Finalization[id] = true
		} else {
			ledger.Work[id] = true
		}
	}
	for id, result := range e.state.Steps {
		if result.SkipKind == SkipKindStartFrom {
			if _, _, placeholder := findStepContainer(workflow, id); placeholder {
				continue
			}
		}
		add(id)
	}
	for id := range e.state.CommandExecutions {
		add(id)
	}
	for id := range e.state.CommandEvaluations {
		add(id)
	}
	for id := range e.state.AgentDeliveries {
		add(id)
	}
	for id := range e.state.MailSends {
		add(id)
	}
	for id := range e.state.ReservationSteps {
		add(id)
	}
	for id := range e.state.ForeachState {
		add(id)
	}
	for id := range e.state.ParallelState {
		add(id)
	}
	for id, frame := range e.state.InFlightSteps {
		if frame.Kind == StepKindLoop && frame.Iteration >= 0 && strings.HasSuffix(id, fmt.Sprintf("_iter%d", frame.Iteration)) {
			continue
		}
		add(id)
	}
	if err := validateStepAdmissions(ledger); err != nil {
		return err
	}
	e.state.StepAdmissions = ledger
	return nil
}

func (e *Executor) admitFinalizationRoots(ctx context.Context, workflow *Workflow) error {
	var finalization []string
	for _, phase := range []struct {
		steps  []Step
		prefix string
	}{{workflow.Settings.OnCancel, "on_cancel"}, {workflow.PostPipelineSteps, "post_pipeline"}} {
		for i, step := range phase.steps {
			id := step.ID
			if id == "" {
				id = fmt.Sprintf("%s_%d", phase.prefix, i+1)
			}
			finalization = append(finalization, id)
		}
	}
	return e.admitRuntimeIDs(finalizationContext(ctx), finalization)
}

func (e *Executor) admitWorkflowRoots(ctx context.Context, workflow *Workflow) error {
	// Pre-cancelled attempts still run authored cleanup. Reserving its roots
	// grants only capacity, so preserve that complete reservation before the
	// ordinary admission observes cancellation and enters finalization.
	if err := e.admitFinalizationRoots(context.WithoutCancel(ctx), workflow); err != nil {
		return err
	}
	roots := make([]string, len(workflow.Steps))
	for i := range workflow.Steps {
		roots[i] = workflow.Steps[i].ID
	}
	return e.admitRuntimeIDs(ctx, roots)
}

func runtimeStepIDs(steps []Step) []string {
	ids := make([]string, len(steps))
	for i := range steps {
		ids[i] = steps[i].ID
	}
	return ids
}
