package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RestoreProgress records execution evidence, not executable recovery input.
// A before event is an uncertain boundary: a crash may occur before or after
// the action. An after/succeeded event confirms the call returned successfully,
// not that its effect can safely be repeated. Launch acceptance and observed
// process startup are separate stages. No commands or scrollback are included.
type RestoreProgress struct {
	CheckpointID  string `json:"checkpoint_id"`
	SourceSession string `json:"source_session"`
	Session       string `json:"session"`
	WorkingDir    string `json:"working_dir,omitempty"`
	PlannedPanes  int    `json:"planned_panes"`
	Sequence      uint64 `json:"sequence"`
	ObservedAt    string `json:"observed_at"`
	Stage         string `json:"stage"`
	Phase         string `json:"phase"`   // before | after | observed
	Outcome       string `json:"outcome"` // uncertain | succeeded
	PaneID        string `json:"pane_id,omitempty"`
	SourcePaneID  string `json:"source_pane_id,omitempty"`
	WindowIndex   int    `json:"window_index,omitempty"`
	PaneIndex     int    `json:"pane_index,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
}

// ErrRestoreProgress means recovery evidence could not be recorded. The
// operation is stopped; already-created sessions and panes are not rolled back.
var ErrRestoreProgress = errors.New("restore progress checkpoint failed")

type restoreProgressObserverKey struct{}
type restoreProgressRecorderKey struct{}

// WithRestoreProgress attaches a synchronous observer to checkpoint restores
// using ctx. Each restore gets its own ordered sequence and lifetime. The
// observer receives the execution context, including later-added journal hooks,
// and must not reenter that same restore. Returning an error (or panicking)
// stops subsequent actions, preserving the cause for errors.Is. Dry runs never
// invoke the observer; a nil observer leaves ctx unchanged.
func WithRestoreProgress(ctx context.Context, observer func(context.Context, RestoreProgress) error) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, restoreProgressObserverKey{}, observer)
}

type restoreProgressRecorder struct {
	mu       sync.Mutex
	identity RestoreProgress
	observer func(context.Context, RestoreProgress) error
	cancel   context.CancelCauseFunc
	sequence uint64
	failure  error
	closed   bool
}

func beginRestoreProgress(ctx context.Context, identity RestoreProgress, dryRun bool) (context.Context, func()) {
	observer, _ := ctx.Value(restoreProgressObserverKey{}).(func(context.Context, RestoreProgress) error)
	if observer == nil || dryRun {
		// Shadow an enclosing recorder too: nested restores and previews must
		// never borrow another operation's identity or sequence.
		return context.WithValue(ctx, restoreProgressRecorderKey{}, (*restoreProgressRecorder)(nil)), func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	recorder := &restoreProgressRecorder{identity: identity, observer: observer, cancel: cancel}
	ctx = context.WithValue(ctx, restoreProgressRecorderKey{}, recorder)
	return ctx, func() {
		recorder.mu.Lock()
		recorder.closed = true
		recorder.mu.Unlock()
		cancel(nil)
	}
}

func reportRestoreProgress(ctx context.Context, event RestoreProgress) error {
	recorder, _ := ctx.Value(restoreProgressRecorderKey{}).(*restoreProgressRecorder)
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.failure != nil {
		return recorder.failure
	}
	if recorder.closed {
		return fmt.Errorf("%w: observer is closed", ErrRestoreProgress)
	}
	recorder.sequence++
	event.CheckpointID = recorder.identity.CheckpointID
	event.SourceSession = recorder.identity.SourceSession
	event.Session = recorder.identity.Session
	event.WorkingDir = recorder.identity.WorkingDir
	event.PlannedPanes = recorder.identity.PlannedPanes
	event.Sequence = recorder.sequence
	event.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	err := callRestoreProgressObserver(ctx, recorder.observer, event)
	if err != nil {
		recorder.failure = fmt.Errorf("%w at %s/%s: %w", ErrRestoreProgress, event.Stage, event.Phase, err)
		recorder.cancel(recorder.failure)
	}
	return recorder.failure
}

func callRestoreProgressObserver(ctx context.Context, observer func(context.Context, RestoreProgress) error, event RestoreProgress) (err error) {
	defer func() {
		if value := recover(); value != nil {
			// A callback panic may contain credentials. Do not copy its payload
			// into execution history; the stage identifies the failed boundary.
			err = errors.New("restore progress observer panicked")
		}
	}()
	return observer(ctx, event)
}

// runRestoreMutation brackets one material action. Check cancellation again
// AFTER the before-write, which can block on storage. Always report the outcome
// after the call, even if cancellation raced it. The returned pane ID survives
// a failed after-write so callers can retain already-created recovery evidence.
func runRestoreMutation(ctx context.Context, event RestoreProgress, run func() (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.Join(err, context.Cause(ctx))
	}
	event.Phase, event.Outcome = "before", "uncertain"
	if err := reportRestoreProgress(ctx, event); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", errors.Join(err, context.Cause(ctx))
	}
	paneID, operationErr := run()
	paneID = strings.TrimSpace(paneID)
	if paneID != "" {
		event.PaneID = paneID
	}
	event.Phase = "after"
	if operationErr == nil {
		event.Outcome = "succeeded"
	}
	progressErr := reportRestoreProgress(ctx, event)
	return paneID, errors.Join(operationErr, progressErr)
}
