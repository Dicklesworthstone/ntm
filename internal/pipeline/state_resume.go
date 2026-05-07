package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ResumeMode controls how persisted state is interpreted when execution is
// resumed after interruption.
type ResumeMode string

const (
	ResumeModeContinue      ResumeMode = "continue"
	ResumeModeRestartFailed ResumeMode = "restart-failed"
	ResumeModeForceIter     ResumeMode = "force-iter"
)

// ResumeRosterChangePolicy decides whether a resume may target a different
// tmux session than the one recorded in the prior state.
type ResumeRosterChangePolicy string

const (
	ResumeRosterAbort   ResumeRosterChangePolicy = "abort"
	ResumeRosterProceed ResumeRosterChangePolicy = "proceed"
)

// ResumeOptions configures Executor.Resume. By default completed step
// outputs are preserved; set Reset=true to clear prior step outputs and
// re-run the workflow from the beginning while retaining non-step input
// variables. KeepState is the historical alias and remains honored, but new
// callers should rely on the default-keep semantics. After
// normalizeResumeOptions, KeepState reflects the effective value.
type ResumeOptions struct {
	Mode ResumeMode `json:"mode,omitempty"`
	// KeepState preserves completed step outputs across resume. When the
	// caller passes a non-zero options struct without setting KeepState=true,
	// normalizeResumeOptions still defaults to keep-state unless Reset=true
	// is set explicitly (bd-uyjdn). The field stays exported so existing
	// JSON-serialized state continues to round-trip and CLI callers that
	// already wire --keep-state continue to work unchanged.
	KeepState bool `json:"keep_state,omitempty"`
	// Reset, when true, opts the caller into the legacy KeepState=false
	// behavior: completed Steps, durable foreach/parallel state, and step
	// variables are cleared before resume.
	Reset          bool                     `json:"reset,omitempty"`
	MaxResumeAge   time.Duration            `json:"max_resume_age,omitempty"`
	OnRosterChange ResumeRosterChangePolicy `json:"on_roster_change,omitempty"`
	StepID         string                   `json:"step_id,omitempty"`
	Iteration      int                      `json:"iteration,omitempty"`
}

// ForeachIterationState records durable progress for loop/foreach-style
// iteration steps. CompletedIterationIDs use the stable "<step>_iter<N>" form.
type ForeachIterationState struct {
	StepID                string    `json:"step_id"`
	CurrentIteration      int       `json:"current_iteration"`
	Total                 int       `json:"total"`
	CompletedIterationIDs []string  `json:"completed_iteration_ids,omitempty"`
	StartedAt             time.Time `json:"started_at,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
}

// ParallelGroupState records the persisted progress of a parallel step group.
type ParallelGroupState struct {
	StepID             string    `json:"step_id"`
	Total              int       `json:"total"`
	CompletedStepIDs   []string  `json:"completed_step_ids,omitempty"`
	FailedStepIDs      []string  `json:"failed_step_ids,omitempty"`
	InFlightStepIDs    []string  `json:"in_flight_step_ids,omitempty"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
	AllSubstepsSettled bool      `json:"all_substeps_settled,omitempty"`
}

// ScopeFrame is the serializable view of active loop/branch variable scopes.
type ScopeFrame struct {
	Kind      string                 `json:"kind"`
	Name      string                 `json:"name,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// InFlightStepState marks work that started but had not produced a finished
// StepResult at the last checkpoint.
type InFlightStepState struct {
	StepID    string    `json:"step_id"`
	Kind      string    `json:"kind,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Iteration int       `json:"iteration,omitempty"`
	Output    string    `json:"output,omitempty"`
}

func defaultResumeOptions() ResumeOptions {
	return ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}
}

func normalizeResumeOptions(opts ResumeOptions) (ResumeOptions, error) {
	if opts == (ResumeOptions{}) {
		return defaultResumeOptions(), nil
	}
	if opts.Mode == "" {
		opts.Mode = ResumeModeContinue
	}
	if opts.OnRosterChange == "" {
		opts.OnRosterChange = ResumeRosterAbort
	}
	// bd-uyjdn: a non-zero ResumeOptions that omits KeepState used to leave
	// it at the Go zero value (false), silently disabling keep-state for
	// callers that only set Mode or MaxResumeAge. Default to keep-state
	// here; callers must opt into reset via Reset=true.
	opts.KeepState = !opts.Reset

	switch opts.Mode {
	case ResumeModeContinue, ResumeModeRestartFailed:
	case ResumeModeForceIter:
		if strings.TrimSpace(opts.StepID) == "" {
			return opts, fmt.Errorf("resume mode force-iter requires StepID")
		}
		if opts.Iteration < 0 {
			return opts, fmt.Errorf("resume mode force-iter requires a non-negative Iteration")
		}
	default:
		return opts, fmt.Errorf("unknown resume mode %q", opts.Mode)
	}

	switch opts.OnRosterChange {
	case ResumeRosterAbort, ResumeRosterProceed:
	default:
		return opts, fmt.Errorf("unknown resume roster-change policy %q", opts.OnRosterChange)
	}

	return opts, nil
}

// ResumeWithOptions resumes using explicit policy without requiring callers to
// mutate ExecutorConfig directly.
func (e *Executor) ResumeWithOptions(ctx context.Context, workflow *Workflow, prior *ExecutionState, opts ResumeOptions, progress chan<- ProgressEvent) (*ExecutionState, error) {
	previous := e.config.ResumeOptions
	e.config.ResumeOptions = opts
	defer func() { e.config.ResumeOptions = previous }()
	return e.Resume(ctx, workflow, prior, progress)
}

func (e *Executor) applyResumeOptions(workflow *Workflow, opts ResumeOptions) error {
	opts, err := normalizeResumeOptions(opts)
	if err != nil {
		return err
	}

	e.stateMu.RLock()
	state := e.state
	if state == nil {
		e.stateMu.RUnlock()
		return fmt.Errorf("resume state is nil")
	}
	checkpoint := resumeCheckpointTime(state)
	priorSession := state.Session
	targetSession := e.config.Session
	e.stateMu.RUnlock()

	if opts.MaxResumeAge > 0 && !checkpoint.IsZero() && time.Since(checkpoint) > opts.MaxResumeAge {
		return fmt.Errorf("resume state %q is older than MaxResumeAge %s", state.RunID, opts.MaxResumeAge)
	}

	if priorSession != "" && targetSession != "" && priorSession != targetSession && opts.OnRosterChange != ResumeRosterProceed {
		return fmt.Errorf("resume state was captured for session %q but target session is %q; set OnRosterChange=proceed to override", priorSession, targetSession)
	}
	if targetSession != "" && priorSession != targetSession && opts.OnRosterChange == ResumeRosterProceed {
		e.stateMu.Lock()
		if e.state != nil {
			e.state.Session = targetSession
		}
		e.stateMu.Unlock()
	}

	if !opts.KeepState {
		e.resetResumeState(workflow)
		return nil
	}

	if opts.Mode == ResumeModeForceIter {
		e.forceResumeIteration(opts.StepID, opts.Iteration)
	}

	return nil
}

func (e *Executor) resetResumeState(workflow *Workflow) {
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	e.state.Steps = make(map[string]StepResult)
	e.state.ForeachState = nil
	e.state.ParallelState = nil
	e.state.ScopeStack = nil
	e.state.InFlightSteps = nil
	e.state.CurrentStep = ""
	e.state.Errors = nil
	e.stateMu.Unlock()

	e.varMu.Lock()
	if e.state != nil && e.state.Variables != nil {
		clearWorkflowStepVariables(e.state.Variables, workflow)
	}
	e.varMu.Unlock()
}

func clearWorkflowStepVariables(vars map[string]interface{}, workflow *Workflow) {
	for key := range vars {
		if strings.HasPrefix(key, "steps.") {
			delete(vars, key)
		}
	}
	if workflow == nil {
		return
	}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, step := range steps {
			if step.OutputVar != "" {
				delete(vars, step.OutputVar)
				delete(vars, step.OutputVar+"_parsed")
			}
			if len(step.Parallel.Steps) > 0 {
				walk(step.Parallel.Steps)
			}
			if step.Loop != nil {
				walk(step.Loop.Steps)
			}
			if step.Foreach != nil {
				walk(step.Foreach.Steps)
			}
			if step.ForeachPane != nil {
				walk(step.ForeachPane.Steps)
			}
		}
	}
	walk(workflow.Steps)
}

func (e *Executor) forceResumeIteration(stepID string, iteration int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	state.StepID = stepID
	state.CurrentIteration = iteration
	state.CompletedIterationIDs = filterCompletedIterationsBefore(state.CompletedIterationIDs, stepID, iteration)
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state

	prefix := fmt.Sprintf("%s_iter", stepID)
	for id := range e.state.Steps {
		if iterationIndexFromID(id, prefix) >= iteration {
			delete(e.state.Steps, id)
		}
	}
}

func filterCompletedIterationsBefore(ids []string, stepID string, iteration int) []string {
	prefix := fmt.Sprintf("%s_iter", stepID)
	out := ids[:0]
	for _, id := range ids {
		idx := iterationIndexFromID(id, prefix)
		if idx >= 0 && idx < iteration {
			out = append(out, id)
		}
	}
	return out
}

func iterationIndexFromID(id, prefix string) int {
	if !strings.HasPrefix(id, prefix) {
		return -1
	}
	rest := strings.TrimPrefix(id, prefix)
	for i, r := range rest {
		if r < '0' || r > '9' {
			if i == 0 {
				return -1
			}
			var idx int
			if _, err := fmt.Sscanf(rest[:i], "%d", &idx); err != nil {
				return -1
			}
			return idx
		}
	}
	var idx int
	if _, err := fmt.Sscanf(rest, "%d", &idx); err != nil {
		return -1
	}
	return idx
}

func (e *Executor) markStepInFlight(stepID, kind string, iteration int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.InFlightSteps == nil {
		e.state.InFlightSteps = make(map[string]InFlightStepState)
	}
	e.state.InFlightSteps[stepID] = InFlightStepState{
		StepID:    stepID,
		Kind:      kind,
		StartedAt: time.Now(),
		Iteration: iteration,
	}
}

func (e *Executor) clearStepInFlight(stepID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.InFlightSteps == nil {
		return
	}
	delete(e.state.InFlightSteps, stepID)
	if len(e.state.InFlightSteps) == 0 {
		e.state.InFlightSteps = nil
	}
}

func (e *Executor) beginForeachState(stepID string, total int) int {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return 0
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	now := time.Now()
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = now
	}
	state.Total = total
	state.UpdatedAt = now
	// bd-p12ti: CompletedIterationIDs is the durable record of work safe to
	// skip; CurrentIteration is only a cursor and must not jump past an
	// incomplete gap. Always start at the first incomplete iteration so a
	// persisted state with a gap (partial writes, force-resume edge cases,
	// future out-of-order completion metadata) does not silently drop the
	// missing iteration on resume.
	start := firstIncompleteIteration(state.CompletedIterationIDs, stepID, total)
	state.CurrentIteration = start
	e.state.ForeachState[stepID] = state
	return start
}

func firstIncompleteIteration(completed []string, stepID string, total int) int {
	if total <= 0 {
		return 0
	}
	done := make(map[int]bool, len(completed))
	prefix := fmt.Sprintf("%s_iter", stepID)
	for _, id := range completed {
		idx := iterationIndexFromID(id, prefix)
		if idx >= 0 {
			done[idx] = true
		}
	}
	for i := 0; i < total; i++ {
		if !done[i] {
			return i
		}
	}
	return total
}

func (e *Executor) markForeachIterationStarted(stepID string, iteration, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	state.CurrentIteration = iteration
	state.Total = total
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

func (e *Executor) markForeachIterationCompleted(stepID string, iteration, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = time.Now()
	}
	iterID := loopIterationID(stepID, iteration)
	if !stringSliceContains(state.CompletedIterationIDs, iterID) {
		state.CompletedIterationIDs = append(state.CompletedIterationIDs, iterID)
	}
	state.CurrentIteration = iteration + 1
	state.Total = total
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

func loopIterationID(stepID string, iteration int) string {
	return fmt.Sprintf("%s_iter%d", stepID, iteration)
}

func iterationSucceeded(results []StepResult, shouldBreak bool) bool {
	if shouldBreak {
		return false
	}
	for _, result := range results {
		switch result.Status {
		case StatusFailed, StatusCancelled, StatusRunning, StatusPending:
			return false
		}
	}
	return true
}

func shouldCompleteForeachIteration(ctx context.Context, results []StepResult, shouldBreak bool) bool {
	if !iterationSucceeded(results, shouldBreak) {
		return false
	}
	if ctx.Err() == nil {
		return true
	}
	return len(results) > 0
}

func (e *Executor) beginParallelState(stepID string, total int) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || stepID == "" {
		return
	}
	if e.state.ParallelState == nil {
		e.state.ParallelState = make(map[string]ParallelGroupState)
	}
	state := e.state.ParallelState[stepID]
	now := time.Now()
	if state.StepID == "" {
		state.StepID = stepID
		state.StartedAt = now
	}
	state.Total = total
	state.UpdatedAt = now
	e.state.ParallelState[stepID] = state
}

func (e *Executor) markParallelSubstepStarted(groupID, stepID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = appendUniqueString(state.InFlightStepIDs, stepID)
	state.UpdatedAt = time.Now()
	e.state.ParallelState[groupID] = state
}

func (e *Executor) markParallelSubstepFinished(groupID, stepID string, status ExecutionStatus) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = removeString(state.InFlightStepIDs, stepID)
	switch status {
	case StatusCompleted, StatusSkipped:
		state.CompletedStepIDs = appendUniqueString(state.CompletedStepIDs, stepID)
	case StatusFailed, StatusCancelled:
		state.FailedStepIDs = appendUniqueString(state.FailedStepIDs, stepID)
	}
	state.UpdatedAt = time.Now()
	e.state.ParallelState[groupID] = state
}

func (e *Executor) completeParallelState(groupID string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil || e.state.ParallelState == nil {
		return
	}
	state := e.state.ParallelState[groupID]
	state.InFlightStepIDs = nil
	state.AllSubstepsSettled = true
	state.CompletedAt = time.Now()
	state.UpdatedAt = state.CompletedAt
	e.state.ParallelState[groupID] = state
}

func (e *Executor) pushScopeFrameLocked(frame ScopeFrame) {
	if e.state == nil {
		return
	}
	e.state.ScopeStack = append(e.state.ScopeStack, frame)
}

func (e *Executor) popScopeFrameLocked() {
	if e.state == nil || len(e.state.ScopeStack) == 0 {
		return
	}
	e.state.ScopeStack = e.state.ScopeStack[:len(e.state.ScopeStack)-1]
}

func appendUniqueString(values []string, value string) []string {
	if value == "" || stringSliceContains(values, value) {
		return values
	}
	return append(values, value)
}

func stringSliceContains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func removeString(values []string, value string) []string {
	out := values[:0]
	for _, current := range values {
		if current != value {
			out = append(out, current)
		}
	}
	return out
}

func resumeCheckpointTime(state *ExecutionState) time.Time {
	if state == nil {
		return time.Time{}
	}
	if !state.LastCheckpointAt.IsZero() {
		return state.LastCheckpointAt
	}

	checkpoint := state.StartedAt
	// bd-05l02: legacy state files often record only UpdatedAt and have no
	// child timestamps (cancelled/persisted before any step result, or a
	// minimal fixture). Without this, resumeCheckpointTime would return
	// the zero time and applyResumeOptions would skip the MaxResumeAge
	// check, letting stale legacy state bypass the resume safety contract.
	checkpoint = mostRecentTime(checkpoint, state.UpdatedAt)
	checkpoint = mostRecentTime(checkpoint, state.FinishedAt)

	for _, result := range state.Steps {
		checkpoint = mostRecentTime(checkpoint, stepResultCheckpointTime(result))
	}
	for _, execErr := range state.Errors {
		checkpoint = mostRecentTime(checkpoint, execErr.Timestamp)
	}
	for _, foreach := range state.ForeachState {
		checkpoint = mostRecentTime(checkpoint, foreach.StartedAt)
		checkpoint = mostRecentTime(checkpoint, foreach.UpdatedAt)
	}
	for _, parallel := range state.ParallelState {
		checkpoint = mostRecentTime(checkpoint, parallel.StartedAt)
		checkpoint = mostRecentTime(checkpoint, parallel.UpdatedAt)
		checkpoint = mostRecentTime(checkpoint, parallel.CompletedAt)
	}
	for _, inFlight := range state.InFlightSteps {
		checkpoint = mostRecentTime(checkpoint, inFlight.StartedAt)
	}

	return checkpoint
}

func stepResultCheckpointTime(result StepResult) time.Time {
	checkpoint := mostRecentTime(result.StartedAt, result.FinishedAt)
	if result.Error != nil {
		checkpoint = mostRecentTime(checkpoint, result.Error.Timestamp)
	}
	return checkpoint
}

func mostRecentTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if b.After(a) {
		return b
	}
	return a
}
