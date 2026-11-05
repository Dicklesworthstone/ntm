package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	pipelineStateDirName = "pipelines"

	// PipelineStateSchemaVersion is the on-disk schema version for persisted
	// pipeline execution state files.
	PipelineStateSchemaVersion = 1
)

// ErrCheckpointFailed means execution stopped because recovery state could not
// be made durable. Retrying individual steps or on_error: continue must never
// hide this failure: the last saved checkpoint may precede external effects.
var ErrCheckpointFailed = errors.New("pipeline checkpoint persistence failed")

// Only explicit, bounded on_cancel steps may dispatch after a checkpoint
// failure. They release resources; ordinary retries and success hooks do not.
type checkpointCleanupKey struct{}

func (e *Executor) resetCheckpointFailure() {
	e.persistMu.Lock()
	e.checkpointErr = nil
	e.persistMu.Unlock()
}

func (e *Executor) checkpointFailure() error {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	return e.checkpointErr
}

func joinCheckpointError(err, checkpointErr error) error {
	if checkpointErr == nil || errors.Is(err, checkpointErr) {
		return err
	}
	return errors.Join(err, checkpointErr)
}

// backgroundCommandFailure is read after all cleanup workers have joined.
// Stable ordering keeps concurrent sidecar failures useful in saved outcomes;
// Wrapping preserves timeout, cancellation, and pipe-drain errors for callers.
func (e *Executor) backgroundCommandFailure() error {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	ids := make([]string, 0, len(e.backgroundCommandErrors))
	for id := range e.backgroundCommandErrors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var failures []error
	for _, id := range ids {
		failures = append(failures, fmt.Errorf("background command %q failed: %w", id, e.backgroundCommandErrors[id]))
	}
	return errors.Join(failures...)
}

// After all execution and cleanup workers have joined, a handled step failure
// still cannot turn uncertain process settlement into a successful workflow.
// Inspect the durable receipts without changing their evidence or replay rules.
func (e *Executor) unsettledProcessExecutionFailure() error {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return nil
	}
	var commandIDs, selectorIDs []string
	for id, record := range e.state.CommandExecutions {
		if record.Status != commandExecutionSettled {
			commandIDs = append(commandIDs, id)
		}
	}
	for id, record := range e.state.CommandEvaluations {
		if !commandEvaluationHasOutcome(record) {
			selectorIDs = append(selectorIDs, id)
		}
	}
	sort.Strings(commandIDs)
	sort.Strings(selectorIDs)
	var failures []error
	for _, id := range commandIDs {
		failures = append(failures, uncertainCommandExecutionError(id, e.state.CommandExecutions[id].ProcessID))
	}
	for _, id := range selectorIDs {
		failures = append(failures, uncertainCommandEvaluationError(id))
	}
	return errors.Join(failures...)
}

// persistState serializes snapshot/save/publication as one transaction. The
// timestamp in memory advances only after SaveState has synced the checkpoint.
// Once a save fails, its error is sticky for this attempt, even if cleanup can
// later save a FAILED checkpoint. Never silently resume dispatch after storage
// recovers, and never write state during a dry run.
func (e *Executor) persistState() error {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	if e.config.DryRun {
		return nil
	}
	if e.checkpointErr != nil {
		e.stateMu.Lock()
		if e.state != nil {
			e.state.Status = StatusFailed
		}
		e.stateMu.Unlock()
	}
	snapshot := e.snapshotState()
	if snapshot == nil {
		return e.checkpointErr
	}
	projectDir := e.config.ProjectDir
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return e.failCheckpointLocked(snapshot.RunID, err)
		}
		projectDir = cwd
	}
	now := time.Now()
	snapshot.LastCheckpointAt = now
	if snapshot.UpdatedAt.Before(now) {
		snapshot.UpdatedAt = now
	}
	if err := SaveState(projectDir, snapshot); err != nil {
		return e.failCheckpointLocked(snapshot.RunID, err)
	}
	e.stateMu.Lock()
	e.state.LastCheckpointAt = now
	// A concurrent worker may have changed state while the snapshot was saved.
	// Do not roll its newer activity timestamp backwards.
	if e.state.UpdatedAt.Before(now) {
		e.state.UpdatedAt = now
	}
	e.stateMu.Unlock()
	return e.checkpointErr
}

// Caller holds persistMu, never stateMu. Preserve the first cause (including
// errors.Is/As), cancel active work, and retain one diagnostic rather than
// flooding state/logs when parallel workers discover the same storage fault.
func (e *Executor) failCheckpointLocked(runID string, cause error) error {
	first := e.checkpointErr == nil
	if first {
		e.checkpointErr = fmt.Errorf("%w for run %q: %w", ErrCheckpointFailed, runID, cause)
	}
	e.stateMu.Lock()
	if e.state != nil {
		e.state.Status = StatusFailed
		if first {
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type: "checkpoint", Message: e.checkpointErr.Error(),
				Timestamp: time.Now(), Fatal: true,
			})
		}
	}
	e.stateMu.Unlock()
	e.Cancel()
	if first {
		slog.Error("pipeline checkpoint failed; stopping execution", "run_id", runID, "error", e.checkpointErr)
	}
	return e.checkpointErr
}

// stopBeforeDispatch is checked after checkpointing and at the final command
// start/pane-paste boundary. Already-dispatched work is cancelled and joined by
// its existing owner; this is not a rollback or an exactly-once guarantee.
func (e *Executor) stopBeforeDispatch(ctx context.Context, result *StepResult) bool {
	cleanup, _ := ctx.Value(checkpointCleanupKey{}).(bool)
	if !cleanup {
		if err := e.checkpointFailure(); err != nil {
			result.Status = StatusFailed
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "checkpoint", Message: err.Error(), Timestamp: result.FinishedAt}
			return true
		}
		if err := e.reservationProtectionFailure(); err != nil {
			result.Status = StatusFailed
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "reservation_protection", Message: err.Error(), Timestamp: result.FinishedAt}
			return true
		}
	}
	if err := ctx.Err(); err != nil {
		result.Status = StatusCancelled
		result.SkipKind = SkipKindCancelled
		result.SkipReason = err.Error()
		result.FinishedAt = time.Now()
		return true
	}
	release, _ := ctx.Value(reservationReleaseKey{}).(bool)
	if !cleanup && !release && !e.config.DryRun {
		if _, err := e.refreshReservationProtection(ctx); err != nil {
			if ctx.Err() != nil {
				result.Status = StatusCancelled
				result.SkipKind, result.SkipReason = SkipKindCancelled, ctx.Err().Error()
				result.FinishedAt = time.Now()
				return true
			}
			err = e.failReservationProtection(err)
			result.Status = StatusFailed
			result.FinishedAt = time.Now()
			result.Error = &StepError{Type: "reservation_protection", Message: err.Error(), Timestamp: result.FinishedAt}
			return true
		}
	}
	return false
}

// finishExecution is shared by new and resumed runs. Settle all command
// cleanup and persist the final outcome BEFORE emitting a terminal event or
// notification. Otherwise a WaitNone cleanup save (or the final save itself)
// can fail after consumers have already been told the workflow succeeded.
func (e *Executor) finishExecution(ctx context.Context, workflow *Workflow, err error) (*ExecutionState, error) {
	err = errors.Join(err, e.stepAdmissionFailure(ctx))
	// Both new and resumed attempts must finish their authored handoff and
	// output checks before publishing the terminal state. Ordinary main-step
	// failures still run post hooks; cancellation uses on_cancel instead.
	e.stateMu.Lock()
	e.state.OutputValidation = nil
	e.stateMu.Unlock()
	if ctx.Err() == nil && e.checkpointFailure() == nil {
		tailErr := e.runPostPipelineSteps(ctx, workflow)
		err = errors.Join(err, tailErr)
		if tailErr == nil && ctx.Err() == nil && e.checkpointFailure() == nil {
			// Hooks may create the artifacts declared by Workflow.Outputs.
			e.validateDeclaredOutputs(workflow)
		}
	}

	// Normal completion must settle any renewal already sent before it
	// cancels the heartbeat's readback. Otherwise the last step can finish
	// between mutation and verification, hiding a failed or unrecorded lease.
	// External cancellation already reaches the worker's context immediately.
	e.stopReservationProtection()
	if e.reservationFinalCheck && ctx.Err() == nil && e.checkpointFailure() == nil && e.reservationProtectionFailure() == nil {
		// The final step can finish after expiry before a due timer runs.
		// Verify remaining held IDs before publishing success. The exemption
		// belongs only to attempts that began as cleanup-only recovery.
		if _, verifyErr := e.refreshReservationProtection(ctx); verifyErr != nil && ctx.Err() == nil {
			err = errors.Join(err, e.failReservationProtection(verifyErr))
		}
	}
	protectionErr := e.reservationProtectionFailure()
	err = joinCheckpointError(err, e.checkpointFailure())
	// Observe cancellation before stopping normal fire-and-forget commands.
	// Cancelling their lifetime on successful completion is not a cancelled run.
	cancelled := ctx.Err() != nil || protectionErr != nil
	if cancelled {
		if err == nil {
			err = ctx.Err()
		}
		e.finalizeCancelledWorkflow(ctx, workflow)
	}
	e.Cancel()
	e.backgroundCommandWG.Wait()
	backgroundErr := e.backgroundCommandFailure()
	err = errors.Join(err, backgroundErr)
	checkpointErr := e.checkpointFailure()
	err = joinCheckpointError(err, checkpointErr)
	err = errors.Join(err, protectionErr)
	if err == nil {
		err = e.unsettledProcessExecutionFailure()
	}

	status, notification := StatusCompleted, NotifyCompleted
	switch {
	case checkpointErr != nil || protectionErr != nil || (err != nil && !cancelled):
		status, notification = StatusFailed, NotifyFailed
	case cancelled:
		status, notification = StatusCancelled, NotifyCancelled
	}
	e.stateMu.Lock()
	e.state.Status = status
	e.state.FinishedAt = time.Now()
	e.state.UpdatedAt = e.state.FinishedAt
	e.state.CurrentStep = ""
	// Cancellation and checkpoint failures already have their own terminal
	// classification, but must not hide a sidecar's timeout or uncertain cleanup.
	// Otherwise the combined execution error below persists this diagnostic.
	if backgroundErr != nil && (cancelled || checkpointErr != nil) {
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type: "command_cleanup", Message: backgroundErr.Error(),
			Timestamp: e.state.FinishedAt, Fatal: true,
		})
	}
	// The returned error disappears with a foreground launcher or detached
	// worker. Persist its terminal cause for later status/list queries, which
	// read Errors rather than reconstructing it from individual step results.
	// Checkpoint failures already record their own fatal diagnostic; handled
	// step failures and cancellation must not gain a new execution failure.
	if status == StatusFailed && !cancelled && checkpointErr == nil && err != nil {
		message := err.Error()
		recorded := false
		for _, existing := range e.state.Errors {
			if existing.Fatal && existing.Message == message {
				recorded = true
				break
			}
		}
		if !recorded {
			// The terminal cause outranks earlier warnings, but a trailing
			// on_failure diagnostic explains why recovery also failed. Keep
			// that more specific cause last for public status consumers.
			insertAt := len(e.state.Errors)
			for insertAt > 0 && e.state.Errors[insertAt-1].Type == "on_failure" {
				insertAt--
			}
			e.state.Errors = append(e.state.Errors, ExecutionError{})
			copy(e.state.Errors[insertAt+1:], e.state.Errors[insertAt:])
			e.state.Errors[insertAt] = ExecutionError{
				Type: "execution", Message: message,
				Timestamp: e.state.FinishedAt, Fatal: true,
			}
		}
	}
	e.stateMu.Unlock()
	if saveErr := e.persistState(); saveErr != nil {
		err = joinCheckpointError(err, saveErr)
		notification = NotifyFailed
	}
	e.stateMu.Lock()
	pending := e.prepareNotification(workflow, notification)
	e.stateMu.Unlock()
	e.deliverNotification(pending)
	if err != nil {
		e.emitProgress("workflow_error", "", err.Error(), e.calculateProgress())
	} else {
		e.emitProgress("workflow_complete", "", "Workflow completed successfully", 1.0)
	}
	return e.state, err
}

func pipelineStateDir(projectDir string) string {
	return filepath.Join(projectDir, ".ntm", pipelineStateDirName)
}

// validateRunID rejects run IDs that would escape the pipeline state
// directory. Generated run IDs are basename-shaped (e.g.
// "run-20260507-143015-abcd1234"); CLI/from-state inputs and any injected
// ExecutorConfig.RunID flow through here before they touch disk so a
// hostile or malformed value cannot reach LoadState/SaveState. Rejects
// path separators (`/`, `\\`), NUL bytes, and the special parents
// "." / "..", and is intentionally stricter than filepath.Clean so a
// platform-specific separator quirk cannot smuggle a traversal through.
func validateRunID(runID string) error {
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	if runID == "." || runID == ".." {
		return fmt.Errorf("invalid run id %q: reserved path component", runID)
	}
	if strings.ContainsAny(runID, "/\\\x00") {
		return fmt.Errorf("invalid run id %q: must not contain path separators or NUL", runID)
	}
	return nil
}

func pipelineStatePath(projectDir, runID string) string {
	return filepath.Join(pipelineStateDir(projectDir), fmt.Sprintf("%s.json", runID))
}

// SaveState persists the execution state to .ntm/pipelines/<run-id>.json.
func SaveState(projectDir string, state *ExecutionState) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	if err := validateRunID(state.RunID); err != nil {
		return err
	}

	dir := pipelineStateDir(projectDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pipeline state dir: %w", err)
	}

	data, err := marshalStateForDisk(state)
	if err != nil {
		return fmt.Errorf("marshal pipeline state: %w", err)
	}

	path := pipelineStatePath(projectDir, state.RunID)

	if err := util.AtomicWriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write pipeline state: %w", err)
	}

	return nil
}

// LoadState loads execution state from .ntm/pipelines/<run-id>.json.
func LoadState(projectDir, runID string) (*ExecutionState, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	path := pipelineStatePath(projectDir, runID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pipeline state: %w", err)
	}

	return decodeState(data, runID)
}

// decodeState is shared by ordinary loading and bounded cleanup inspection.
// Neither decoding path may reinterpret a different run's recovery evidence.
func decodeState(data []byte, runID string) (*ExecutionState, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	var header struct {
		StateSchemaVersion int `json:"state_schema_version,omitempty"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parse pipeline state header: %w", err)
	}
	if header.StateSchemaVersion < 0 {
		return nil, fmt.Errorf("invalid negative pipeline state schema version %d", header.StateSchemaVersion)
	}
	if header.StateSchemaVersion > PipelineStateSchemaVersion {
		return nil, fmt.Errorf("pipeline state schema version %d is newer than supported version %d", header.StateSchemaVersion, PipelineStateSchemaVersion)
	}

	var state ExecutionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse pipeline state: %w", err)
	}

	if state.RunID == "" {
		state.RunID = runID
	} else if state.RunID != runID {
		return nil, fmt.Errorf("pipeline state run id %q does not match requested run id %q", state.RunID, runID)
	}

	if err := validateBranchDecisions(&state); err != nil {
		return nil, err
	}
	if err := validateStepAdmissions(state.StepAdmissions); err != nil {
		return nil, err
	}
	if err := validateSuccessHooks(&state); err != nil {
		return nil, err
	}

	return &state, nil
}

func marshalStateForDisk(state *ExecutionState) ([]byte, error) {
	if err := validateStepAdmissions(state.StepAdmissions); err != nil {
		return nil, err
	}
	if err := validateBranchDecisions(state); err != nil {
		return nil, err
	}
	if err := validateSuccessHooks(state); err != nil {
		return nil, err
	}

	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	version, err := json.Marshal(PipelineStateSchemaVersion)
	if err != nil {
		return nil, err
	}
	doc["state_schema_version"] = version

	return json.MarshalIndent(doc, "", "  ")
}

// StateCleanupEntry reports a decision without exposing commands or captured
// output. Reason is a stable code; retained files remain available for recovery.
type StateCleanupEntry struct {
	RunID  string `json:"run_id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// StateCleanupResult describes the same eligibility checks in preview and
// deletion mode. Eligible includes deleted files and files that would be deleted.
type StateCleanupResult struct {
	Deleted  int                 `json:"deleted"`
	Eligible int                 `json:"eligible"`
	Retained int                 `json:"retained"`
	Entries  []StateCleanupEntry `json:"entries"`
}

const stateCleanupMaxBytes int64 = 64 << 20

// CleanupStates removes only old, terminal checkpoints whose owned effects are
// settled. Age alone cannot discard the receipts that prevent unsafe replay.
// Preview uses the same ownership, decoding, and settlement checks as deletion.
func CleanupStates(ctx context.Context, projectDir string, olderThan time.Duration, dryRun bool) (*StateCleanupResult, error) {
	result := &StateCleanupResult{Entries: make([]StateCleanupEntry, 0)}
	if olderThan <= 0 {
		return result, fmt.Errorf("olderThan must be greater than zero")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(projectDir) == "" {
		return result, fmt.Errorf("project directory is required for pipeline cleanup")
	}
	projectDir = normalizeLockRoot(projectDir)
	entries, err := os.ReadDir(pipelineStateDir(projectDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("read pipeline state dir: %w", err)
	}

	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		runID := strings.TrimSuffix(entry.Name(), ".json")
		decision := StateCleanupEntry{RunID: runID, Action: "retained", Reason: "unreadable"}
		info, infoErr := entry.Info()
		var cleanupErr error
		if infoErr == nil {
			decision, cleanupErr = cleanupStateCandidate(ctx, projectDir, runID, info, cutoff, dryRun)
		}
		result.Entries = append(result.Entries, decision)
		switch decision.Action {
		case "deleted":
			result.Deleted++
			result.Eligible++
		case "would_delete":
			result.Eligible++
		default:
			result.Retained++
		}
		if cleanupErr != nil {
			return result, cleanupErr
		}
	}
	return result, nil
}

// The directory snapshot is only a prefilter. Ownership is held while reading
// and through the final identity check and unlink. Cleanup never publishes an
// executor owner token, acknowledges cancellation, or unlinks rendezvous locks.
func cleanupStateCandidate(ctx context.Context, projectDir, runID string, observed os.FileInfo, cutoff time.Time, dryRun bool) (StateCleanupEntry, error) {
	entry := StateCleanupEntry{RunID: runID, Action: "retained"}
	retain := func(reason string) (StateCleanupEntry, error) {
		entry.Reason = reason
		return entry, nil
	}
	if err := ctx.Err(); err != nil {
		entry.Reason = "cancelled"
		return entry, err
	}
	if err := validateRunID(runID); err != nil {
		return retain("invalid_run_id")
	}
	if observed == nil || !observed.Mode().IsRegular() {
		return retain("not_regular")
	}
	if !observed.ModTime().Before(cutoff) {
		return retain("recent")
	}
	if observed.Size() > stateCleanupMaxBytes {
		return retain("too_large")
	}

	lock, _, err := acquireRunLock(ctx, projectDir, runID)
	if err != nil {
		if ctx.Err() != nil {
			entry.Reason = "cancelled"
			return entry, ctx.Err()
		}
		if errors.Is(err, ErrRunAlreadyOwned) {
			return retain("owned")
		}
		return retain("lock_unavailable")
	}
	defer lock.unlockAndClose()

	path := pipelineStatePath(projectDir, runID)
	current, err := os.Lstat(path)
	if err != nil || !sameCleanupStateFile(observed, current) {
		return retain("changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return retain("unreadable")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !sameCleanupStateFile(current, opened) {
		_ = file.Close()
		return retain("changed")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, stateCleanupMaxBytes+1))
	afterRead, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return retain("unreadable")
	}
	if int64(len(data)) > stateCleanupMaxBytes {
		return retain("too_large")
	}
	if !sameCleanupStateFile(opened, afterRead) {
		return retain("changed")
	}
	state, err := decodeState(data, runID)
	if err != nil {
		return retain("invalid_state")
	}
	if reason := stateCleanupRetentionReason(state); reason != "" {
		return retain(reason)
	}

	// SaveState replaces the inode atomically. A refreshed checkpoint from
	// an older caller that did not use run control must also be preserved.
	current, err = os.Lstat(path)
	if err != nil || !sameCleanupStateFile(observed, current) || !current.ModTime().Before(cutoff) {
		return retain("changed")
	}
	if err := ctx.Err(); err != nil {
		entry.Reason = "cancelled"
		return entry, err
	}
	entry.Reason = "settled"
	if dryRun {
		entry.Action = "would_delete"
		return entry, nil
	}
	if err := os.Remove(path); err != nil {
		entry.Reason = "remove_failed"
		return entry, fmt.Errorf("remove pipeline state %q: %w", runID, err)
	}
	entry.Action = "deleted"
	return entry, nil
}

func sameCleanupStateFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && before.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func cleanupReceiptIDs[T any](records map[string]T) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Positive receipt evidence outranks a failed/cancelled scheduler result.
// Conversely, a terminal scheduler result never proves that an owned process,
// prompt, message, or reservation has settled.
func stateCleanupRetentionReason(state *ExecutionState) string {
	if state == nil {
		return "invalid_state"
	}
	switch state.Status {
	case StatusCompleted, StatusFailed, StatusCancelled:
	default:
		return "nonterminal"
	}
	if state.StartedAt.IsZero() || state.FinishedAt.IsZero() || state.FinishedAt.Before(state.StartedAt) {
		return "unfinished"
	}
	if state.Status == StatusCancelled && state.CancelledAt == nil {
		return "unfinished"
	}
	if state.CancelledAt != nil && (state.CancelledAt.IsZero() || state.CancelledAt.Before(state.StartedAt) || state.CancelledAt.After(state.FinishedAt)) {
		return "unfinished"
	}
	if state.CurrentStep != "" || len(state.InFlightSteps) != 0 {
		return "in_flight"
	}
	for _, step := range state.Steps {
		switch step.Status {
		case StatusCompleted, StatusFailed, StatusCancelled, StatusSkipped:
		default:
			return "unfinished_step"
		}
		if step.FinishedAt.IsZero() {
			return "unfinished_step"
		}
	}
	for id, group := range state.ParallelState {
		if group.StepID != id || !group.AllSubstepsSettled || group.CompletedAt.IsZero() ||
			group.CompletedAt.Before(group.StartedAt) || len(group.InFlightStepIDs) != 0 {
			return "in_flight"
		}
	}
	for _, executionErr := range state.Errors {
		if executionErr.Type == "checkpoint" {
			// Bounded on_cancel commands may run without receipts after a
			// persistence failure, so absent receipts cannot prove cleanup.
			return "checkpoint_failure"
		}
	}
	if state.CommandEvaluationVersion != commandEvaluationVersion {
		return "unsupported_journal"
	}
	if err := validateSuccessHooks(state); err != nil {
		return "invalid_success_hook_receipt"
	}
	for _, record := range state.SuccessHooks {
		if !successHooksSettled(record) {
			return "unfinished_success_hooks"
		}
	}
	for _, id := range cleanupReceiptIDs(state.CommandExecutions) {
		record := state.CommandExecutions[id]
		if err := validateCommandExecution(id, record); err != nil {
			return "invalid_command_receipt"
		}
		if _, err := commandExecutionRecoveryEntry(state, record); err != nil {
			return "invalid_command_receipt"
		}
		if record.Status != commandExecutionSettled {
			return "unsettled_command"
		}
	}
	for _, id := range cleanupReceiptIDs(state.CommandEvaluations) {
		record := state.CommandEvaluations[id]
		if err := validateCommandEvaluation(id, record); err != nil {
			return "invalid_selector_receipt"
		}
		if _, err := commandEvaluationRecoveryEntry(state, record); err != nil {
			return "invalid_selector_receipt"
		}
		if !commandEvaluationHasOutcome(record) {
			return "unsettled_selector"
		}
	}
	for _, id := range cleanupReceiptIDs(state.AgentDeliveries) {
		record := state.AgentDeliveries[id]
		if err := validateAgentDelivery(id, record, state.Session); err != nil {
			return "invalid_agent_receipt"
		}
		if _, err := agentDeliveryRecoveryEntry(state, record); err != nil {
			return "invalid_agent_receipt"
		}
		if record.Status != agentDeliveryNotSent && record.Status != agentDeliveryCompleted {
			return "unfinished_agent_delivery"
		}
	}
	for _, id := range cleanupReceiptIDs(state.MailSends) {
		record := state.MailSends[id]
		if err := validateMailSend(id, record); err != nil {
			return "invalid_mail_receipt"
		}
		if record.Status != mailSendSent && record.Status != mailSendRejected {
			return "unfinished_mail_send"
		}
	}
	identities := make(map[[2]int]agentmail.FileReservation)
	for _, id := range cleanupReceiptIDs(state.ReservationSteps) {
		record := state.ReservationSteps[id]
		if err := validateReservationStep(id, record); err != nil {
			return "invalid_reservation_receipt"
		}
		if record.Status != reservationReleased && record.Status != reservationRejected {
			return "unfinished_reservation"
		}
		for _, grant := range record.Granted {
			key := [2]int{grant.ProjectID, grant.ID}
			if prior, exists := identities[key]; exists && !sameReservationLease(prior, grant) {
				return "invalid_reservation_receipt"
			}
			identities[key] = grant
		}
	}
	return ""
}
