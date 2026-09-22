package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	}
	if err := ctx.Err(); err != nil {
		result.Status = StatusCancelled
		result.SkipKind = SkipKindCancelled
		result.SkipReason = err.Error()
		result.FinishedAt = time.Now()
		return true
	}
	return false
}

// finishExecution is shared by new and resumed runs. Settle all command
// cleanup and persist the final outcome BEFORE emitting a terminal event or
// notification. Otherwise a WaitNone cleanup save (or the final save itself)
// can fail after consumers have already been told the workflow succeeded.
func (e *Executor) finishExecution(ctx context.Context, workflow *Workflow, err error) (*ExecutionState, error) {
	err = joinCheckpointError(err, e.checkpointFailure())
	// Observe cancellation before stopping normal fire-and-forget commands.
	// Cancelling their lifetime on successful completion is not a cancelled run.
	cancelled := ctx.Err() != nil
	if cancelled {
		if err == nil {
			err = ctx.Err()
		}
		e.finalizeCancelledWorkflow(ctx, workflow)
	}
	e.Cancel()
	e.backgroundCommandWG.Wait()
	checkpointErr := e.checkpointFailure()
	err = joinCheckpointError(err, checkpointErr)

	status, notification := StatusCompleted, NotifyCompleted
	switch {
	case checkpointErr != nil || (err != nil && !cancelled):
		status, notification = StatusFailed, NotifyFailed
	case cancelled:
		status, notification = StatusCancelled, NotifyCancelled
	}
	e.stateMu.Lock()
	e.state.Status = status
	e.state.FinishedAt = time.Now()
	e.state.UpdatedAt = e.state.FinishedAt
	e.state.CurrentStep = ""
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
			e.state.Errors = append(e.state.Errors, ExecutionError{
				Type: "execution", Message: message,
				Timestamp: e.state.FinishedAt, Fatal: true,
			})
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

	var header struct {
		StateSchemaVersion int `json:"state_schema_version,omitempty"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parse pipeline state header: %w", err)
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

	return &state, nil
}

func marshalStateForDisk(state *ExecutionState) ([]byte, error) {
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

// CleanupStates removes pipeline state files older than the provided duration.
// Returns the number of deleted state files.
func CleanupStates(projectDir string, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("olderThan must be greater than zero")
	}

	dir := pipelineStateDir(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pipeline state dir: %w", err)
	}

	cutoff := time.Now().Add(-olderThan)
	deleted := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				return deleted, fmt.Errorf("remove pipeline state: %w", err)
			}
			deleted++
		}
	}

	return deleted, nil
}
