package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func checkpointTestExecutor(t *testing.T) (*Executor, context.Context) {
	t.Helper()
	cfg := DefaultExecutorConfig("checkpoint-test")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-checkpoint"
	e := NewExecutor(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.cancelFn = cancel
	e.state = &ExecutionState{
		RunID: cfg.RunID, WorkflowID: "checkpoint-test", Session: cfg.Session,
		Status: StatusRunning, Steps: make(map[string]StepResult), Variables: make(map[string]interface{}),
		StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	return e, ctx
}

// Preserve the previous checkpoint as evidence, then make atomic replacement
// fail deterministically. Permission-based tests are unreliable when run as root.
func blockCheckpointPath(t *testing.T, e *Executor) string {
	t.Helper()
	path := pipelineStatePath(e.config.ProjectDir, e.state.RunID)
	if _, err := os.Lstat(path); err == nil {
		if err := os.Rename(path, path+".previous"); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertCheckpointFailure(t *testing.T, state *ExecutionState, err error) {
	t.Helper()
	if !errors.Is(err, ErrCheckpointFailed) || state == nil || state.Status != StatusFailed {
		t.Fatalf("checkpoint failure was hidden: state=%+v err=%v", state, err)
	}
	count := 0
	for _, entry := range state.Errors {
		if entry.Type == "checkpoint" && entry.Fatal {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d fatal checkpoint diagnostics, want exactly one: %+v", count, state.Errors)
	}
}

func assertNoCompletionEvent(t *testing.T, events chan ProgressEvent) {
	t.Helper()
	close(events)
	for event := range events {
		if event.Type == "workflow_complete" {
			t.Fatalf("published false success: %+v", event)
		}
	}
}

func TestCheckpointFailureKeepsLastDurableTimestamp(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	good, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil || good.LastCheckpointAt.IsZero() {
		t.Fatalf("initial checkpoint: %+v %v", good, err)
	}
	blockCheckpointPath(t, e)
	failure := e.persistState()
	assertCheckpointFailure(t, e.GetState(), failure)
	if ctx.Err() != context.Canceled {
		t.Fatal("checkpoint failure did not cancel the execution context")
	}
	if !e.GetState().LastCheckpointAt.Equal(good.LastCheckpointAt) {
		t.Fatal("failed save advertised a fresh checkpoint")
	}
	var renameErr *os.LinkError
	if !errors.As(failure, &renameErr) {
		t.Fatalf("lost filesystem cause: %v", failure)
	}
}

func TestCheckpointFailureRemainsStickyAfterStorageRecovers(t *testing.T) {
	e, _ := checkpointTestExecutor(t)
	path := blockCheckpointPath(t, e)
	first := e.persistState()
	assertCheckpointFailure(t, e.GetState(), first)
	if err := os.Rename(path, path+".blocked"); err != nil {
		t.Fatal(err)
	}
	// A stale success decision from a parallel worker must not overwrite the
	// failed outcome just because the device can accept writes again.
	e.state.Status = StatusCompleted
	if err := e.persistState(); !errors.Is(err, first) {
		t.Fatalf("save recovery lost the first failure: %v (first %v)", err, first)
	}
	saved, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpointFailure(t, saved, first)
	if saved.LastCheckpointAt.IsZero() || !saved.LastCheckpointAt.Equal(e.GetState().LastCheckpointAt) {
		t.Fatal("successful failed-state checkpoint did not publish its actual timestamp")
	}
}

func TestCheckpointMarshalFailurePreservesPreviousFile(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	path := pipelineStatePath(e.config.ProjectDir, e.state.RunID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := e.GetState().LastCheckpointAt
	e.state.Variables["unsupported"] = make(chan int)
	failure := e.persistState()
	assertCheckpointFailure(t, e.GetState(), failure)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || !e.GetState().LastCheckpointAt.Equal(stamp) {
		t.Fatal("serialization failure replaced the last good checkpoint or advanced its timestamp")
	}
	if ctx.Err() == nil {
		t.Fatal("serialization failure left execution active")
	}
}

func TestCheckpointFailureIsSingleAndRaceSafeAcrossWriters(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	blockCheckpointPath(t, e)
	const workers = 24
	var wg sync.WaitGroup
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- e.persistState()
			_ = e.GetState()
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		assertCheckpointFailure(t, e.GetState(), err)
	}
	if ctx.Err() == nil {
		t.Fatal("concurrent failed saves left execution active")
	}
}

func TestCheckpointDryRunIsReadOnlyEvenForUnserializableState(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	e.config.DryRun = true
	e.state.Variables["unsupported"] = make(chan int)
	stamp := e.state.LastCheckpointAt
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(e.config.ProjectDir, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote state: %v", err)
	}
	if ctx.Err() != nil || e.state.Status != StatusRunning || !e.state.LastCheckpointAt.Equal(stamp) {
		t.Fatal("dry-run persistence changed execution or checkpoint metadata")
	}
}

func TestCheckpointFinalSaveCannotAnnounceSuccess(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	blockCheckpointPath(t, e)
	out := make(chan ProgressEvent, 10)
	e.progress = out
	state, err := e.finishExecution(ctx, &Workflow{Name: "checkpoint-test"}, nil)
	assertCheckpointFailure(t, state, err)
	assertNoCompletionEvent(t, out)
}

func TestCheckpointCleanupFailureCannotAnnounceSuccess(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	out := make(chan ProgressEvent, 10)
	e.progress = out
	e.backgroundCommandWG.Add(1)
	cleanupErr := make(chan error, 1)
	go func() {
		defer e.backgroundCommandWG.Done()
		<-ctx.Done()
		path := pipelineStatePath(e.config.ProjectDir, e.state.RunID)
		if err := os.Rename(path, path+".previous"); err != nil {
			cleanupErr <- err
			return
		}
		if err := os.Mkdir(path, 0700); err != nil {
			cleanupErr <- err
			return
		}
		cleanupErr <- e.persistState()
	}()
	state, err := e.finishExecution(ctx, &Workflow{Name: "checkpoint-test"}, nil)
	if workerErr := <-cleanupErr; !errors.Is(workerErr, ErrCheckpointFailed) {
		t.Fatalf("cleanup fixture did not fail its checkpoint: %v", workerErr)
	}
	assertCheckpointFailure(t, state, err)
	assertNoCompletionEvent(t, out)
}

func TestCheckpointSuccessEventFollowsCleanupAndDurableState(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	out := make(chan ProgressEvent, 10)
	e.progress = out
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	e.backgroundCommandWG.Add(1)
	go func() {
		defer e.backgroundCommandWG.Done()
		<-ctx.Done()
		close(cleanupStarted)
		<-releaseCleanup
		e.stateMu.Lock()
		e.state.Steps["cleanup"] = StepResult{StepID: "cleanup", Status: StatusCompleted, Output: "settled"}
		e.stateMu.Unlock()
		e.persistState()
	}()
	type outcome struct {
		state *ExecutionState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := e.finishExecution(ctx, &Workflow{Name: "checkpoint-test"}, nil)
		done <- outcome{state, err}
	}()
	<-cleanupStarted
	select {
	case event := <-out:
		t.Errorf("terminal event preceded cleanup: %+v", event)
	default:
	}
	close(releaseCleanup)
	result := <-done
	state, err := result.state, result.err
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("healthy finalization: %+v %v", state, err)
	}
	select {
	case event := <-out:
		if event.Type != "workflow_complete" {
			t.Fatalf("wrong terminal event: %+v", event)
		}
	default:
		t.Fatal("missing success event")
	}
	saved, err := LoadState(e.config.ProjectDir, state.RunID)
	if err != nil || saved.Status != StatusCompleted || saved.Steps["cleanup"].Output != "settled" {
		t.Fatalf("success preceded durable cleanup: %+v %v", saved, err)
	}
}

func TestCheckpointDispatchGuardAllowsOnlyBoundedCancellationCleanup(t *testing.T) {
	e, ctx := checkpointTestExecutor(t)
	blockCheckpointPath(t, e)
	e.persistState()
	var ordinary StepResult
	if !e.stopBeforeDispatch(context.Background(), &ordinary) || ordinary.Error == nil || ordinary.Error.Type != "checkpoint" {
		t.Fatal("ordinary work bypassed the checkpoint fence with a fresh context")
	}
	cleanup := context.WithValue(context.Background(), checkpointCleanupKey{}, true)
	var result StepResult
	if e.stopBeforeDispatch(cleanup, &result) {
		t.Fatal("explicit cancellation cleanup was blocked by the failed storage")
	}
	cancelledCleanup := context.WithValue(ctx, checkpointCleanupKey{}, true)
	if !e.stopBeforeDispatch(cancelledCleanup, &result) || result.Status != StatusCancelled {
		t.Fatal("cleanup ignored its own cancellation")
	}
}
