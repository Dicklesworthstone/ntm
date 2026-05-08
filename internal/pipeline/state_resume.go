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
//
// ItemsFingerprint (bd-3awat) records a content hash of the resolved items
// list at the time the foreach started. CompletedIterationIDs are keyed by
// integer index, so resuming against a list whose elements have shifted
// (resolved from a dynamic source between runs — vars expression, prior
// step output, glob, etc.) would silently apply old completion records to
// new items. Recording the fingerprint lets executeForEach refuse resume
// when the resolved items diverge from the original run.
//
// CollectedOutputs (bd-t3q8a) preserves the loop.Collect outputs for
// already-completed iterations so a mid-loop resume can reconstruct them
// before continuing. Without this the resumed loop would store loop.Collect
// using only the iterations that ran on the resume, silently dropping the
// outputs from iterations completed in the prior run.
//
// CompletedRounds (bd-r2pan) records the highest fully-completed round per
// iteration ID for foreach steps with max_rounds > 1. bd-qeatk's iteration-
// level CompletedIterationIDs only tracks iterations that finished every
// round; an iteration interrupted mid-rounds is NOT in that set, so on
// resume the iteration would re-dispatch from round 1, duplicating the
// side effects of any rounds that had completed before the interruption.
// CompletedRounds maps "<step>_iter<N>" → highest completed round so the
// resume loop can skip rounds whose watermark already advanced. Empty/
// missing entry means "no rounds completed", so legacy state files
// upgrade cleanly (their resumes re-run all rounds, matching pre-bd-r2pan
// behavior). Single-round iterations (max_rounds unset or 1) never write
// here.
type ForeachIterationState struct {
	StepID                string         `json:"step_id"`
	CurrentIteration      int            `json:"current_iteration"`
	Total                 int            `json:"total"`
	CompletedIterationIDs []string       `json:"completed_iteration_ids,omitempty"`
	ItemsFingerprint      string         `json:"items_fingerprint,omitempty"`
	CollectedOutputs      []interface{}  `json:"collected_outputs,omitempty"`
	CompletedRounds       map[string]int `json:"completed_rounds,omitempty"`
	StartedAt             time.Time      `json:"started_at,omitempty"`
	UpdatedAt             time.Time      `json:"updated_at,omitempty"`
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
	var prunedStepIDs []string
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return
	}
	if e.state.ForeachState == nil {
		e.state.ForeachState = make(map[string]ForeachIterationState)
	}
	state := e.state.ForeachState[stepID]
	state.StepID = stepID
	state.CurrentIteration = iteration
	state.CompletedIterationIDs = filterCompletedIterationsBefore(state.CompletedIterationIDs, stepID, iteration)
	// bd-t3q8a: sequential foreach (loops.go) appends collected outputs in
	// iteration order, so truncating to length=iteration drops the entries
	// for the iterations that are about to rerun. Without this, the
	// rerun would observe duplicated entries (prior + reran) in the final
	// loop.Collect store.
	if iteration >= 0 && iteration < len(state.CollectedOutputs) {
		state.CollectedOutputs = append([]interface{}(nil), state.CollectedOutputs[:iteration]...)
	}
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state

	prefix := fmt.Sprintf("%s_iter", stepID)
	for id := range e.state.Steps {
		if iterationIndexFromID(id, prefix) >= iteration {
			delete(e.state.Steps, id)
			prunedStepIDs = append(prunedStepIDs, id)
		}
	}
	e.stateMu.Unlock()

	// bd-a3fwf: deleting StepResults is not enough — Substitutor.resolveSteps
	// reads flat steps.<id>.output / steps.<id>.data variables (and per-step
	// output_var entries) before falling back to state.Steps. Without
	// scrubbing those flat keys, a forced rerun from iteration N can still
	// resolve ghost outputs from iterations >=N whose StepResults were
	// pruned. Clear variables outside stateMu to keep lock ordering consistent
	// with substituteVariablesStrict (varMu acquired separately, never nested
	// under stateMu — see snapshotState bd-xuxev note). Use a graph-nil-safe
	// inline path rather than executor.clearStepVariables: forceResumeIteration
	// can be invoked before the workflow graph is rebuilt (and in unit tests
	// the graph is never set), and the executor helper would NPE on
	// e.graph.GetStep(id) in those cases.
	if len(prunedStepIDs) > 0 {
		e.varMu.Lock()
		if e.state != nil && e.state.Variables != nil {
			for _, id := range prunedStepIDs {
				delete(e.state.Variables, "steps."+id+".output")
				delete(e.state.Variables, "steps."+id+".data")
				if e.graph != nil {
					if step, ok := e.graph.GetStep(id); ok && step.OutputVar != "" {
						delete(e.state.Variables, step.OutputVar)
						delete(e.state.Variables, step.OutputVar+"_parsed")
					}
				}
			}
		}
		e.varMu.Unlock()
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

// computeForeachItemsFingerprint returns a stable hash of the resolved
// foreach items so resume can detect drift when items resolve from a
// dynamic source (vars expression, prior step output, glob, etc.).
//
// Empty inputs hash deterministically (the empty-array form), matching the
// JSON encoding so an unchanged empty-source resume verifies cleanly.
// Unmarshalable values fall back to the Go-formatted form: a fingerprint
// is best-effort drift detection, not a security boundary.
func computeForeachItemsFingerprint(items []interface{}) string {
	if items == nil {
		items = []interface{}{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%#v", items))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// verifyForeachItemsFingerprint compares the supplied fingerprint against
// the one persisted on prior foreach state. Returns nil if no prior state
// exists, the fingerprints match, or there is no recorded fingerprint
// (legacy state files predate this field). Returns an error when the prior
// run completed at least one iteration with a fingerprint that no longer
// matches — that is the unsafe-resume case where iteration N's completion
// record applies to a different items[N] than the resume sees.
func (e *Executor) verifyForeachItemsFingerprint(stepID, fingerprint string) error {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return nil
	}
	prior, ok := e.state.ForeachState[stepID]
	if !ok {
		return nil
	}
	if prior.ItemsFingerprint == "" || len(prior.CompletedIterationIDs) == 0 {
		return nil
	}
	if prior.ItemsFingerprint == fingerprint {
		return nil
	}
	return fmt.Errorf("foreach %q items changed since prior run (completed=%d, prior_fingerprint=%s, current_fingerprint=%s); resume would apply old iteration records to different items",
		stepID, len(prior.CompletedIterationIDs), prior.ItemsFingerprint[:12], fingerprint[:12])
}

// appendForeachCollectedOutput persists a single loop.Collect output for
// a completed iteration. The slice on ForeachIterationState mirrors the
// in-memory LoopResult.Collected so a mid-loop resume can reconstruct
// outputs from iterations completed before the interruption (bd-t3q8a).
// Sequential foreach is the only caller; parallel foreach (foreach.go)
// has its own bookkeeping path.
func (e *Executor) appendForeachCollectedOutput(stepID string, value interface{}) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
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
	state.CollectedOutputs = append(state.CollectedOutputs, value)
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
}

// loadForeachCollectedOutputs returns a copy of the persisted collected
// outputs for a foreach step, or nil if none exist (bd-t3q8a). The copy
// isolates the caller's slice from concurrent appends to the underlying
// state.
func (e *Executor) loadForeachCollectedOutputs(stepID string) []interface{} {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return nil
	}
	state, ok := e.state.ForeachState[stepID]
	if !ok || len(state.CollectedOutputs) == 0 {
		return nil
	}
	out := make([]interface{}, len(state.CollectedOutputs))
	copy(out, state.CollectedOutputs)
	return out
}

// recordForeachItemsFingerprint stores the fingerprint of the resolved
// items on the foreach state. Idempotent: re-records the same fingerprint
// on resume when nothing has changed, so verifyForeachItemsFingerprint can
// catch drift on a subsequent resume even after a checkpointed run that
// was originally written before this field existed.
func (e *Executor) recordForeachItemsFingerprint(stepID, fingerprint string) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
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
	state.ItemsFingerprint = fingerprint
	state.UpdatedAt = time.Now()
	e.state.ForeachState[stepID] = state
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

// foreachIterationCompletedRounds returns the highest fully-completed round
// recorded for the given iteration ID, or 0 if none. Used by
// executeForeachIteration to skip rounds on resume that have already
// finished in a prior run (bd-r2pan).
func (e *Executor) foreachIterationCompletedRounds(stepID, iterID string) int {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil || e.state.ForeachState == nil {
		return 0
	}
	state, ok := e.state.ForeachState[stepID]
	if !ok || state.CompletedRounds == nil {
		return 0
	}
	return state.CompletedRounds[iterID]
}

// markForeachIterationRoundCompleted records that round N of the given
// iteration finished cleanly so a subsequent resume can skip it (bd-r2pan).
// Only called from the max_rounds path; single-round iterations don't write
// here so legacy state files stay byte-identical to pre-bd-r2pan output.
func (e *Executor) markForeachIterationRoundCompleted(stepID, iterID string, round int) {
	if stepID == "" || iterID == "" || round <= 0 {
		return
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
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
	if state.CompletedRounds == nil {
		state.CompletedRounds = make(map[string]int)
	}
	if cur := state.CompletedRounds[iterID]; round > cur {
		state.CompletedRounds[iterID] = round
	}
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

// shouldCompleteForeachIteration decides whether to record an iteration
// as durably complete. completedAllSteps (bd-vq8bc) is the explicit signal
// from executeIteration that every nested step in loop.Steps was processed,
// distinguishing a fully-executed iteration that observed late ctx
// cancellation (safe to checkpoint) from a partial iteration cancelled
// mid-body (must rerun on resume so the unfinished body steps still run).
//
// Replaces the previous len(results)>0 heuristic, which silently dropped
// remaining body steps when ctx fired between the success of step N and
// the dispatch of step N+1 — see bd-vq8bc impact analysis.
func shouldCompleteForeachIteration(ctx context.Context, results []StepResult, shouldBreak, completedAllSteps bool) bool {
	if !iterationSucceeded(results, shouldBreak) {
		return false
	}
	if ctx.Err() == nil {
		return true
	}
	return completedAllSteps
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
