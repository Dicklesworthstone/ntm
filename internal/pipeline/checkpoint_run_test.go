//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func checkpointRunConfig(t *testing.T) ExecutorConfig {
	t.Helper()
	cfg := DefaultExecutorConfig("checkpoint-run")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-checkpoint"
	cfg.DefaultTimeout, cfg.GlobalTimeout = 5*time.Second, 15*time.Second
	return cfg
}

func TestCheckpointAdmissionFailureStartsNoWork(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			cfg := checkpointRunConfig(t)
			e := NewExecutor(cfg)
			if err := os.MkdirAll(pipelineStatePath(cfg.ProjectDir, cfg.RunID), 0700); err != nil {
				t.Fatal(err)
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Steps: []Step{
				{ID: "forbidden", Command: "printf unsafe > dispatched"},
			}, Settings: WorkflowSettings{OnCancel: []Step{{ID: "cleanup", Command: "printf unsafe > cleanup"}}}}
			events := make(chan ProgressEvent, 20)
			var state *ExecutionState
			var err error
			if resume {
				prior := &ExecutionState{RunID: cfg.RunID, WorkflowID: workflow.Name, Session: cfg.Session, Status: StatusFailed, Steps: map[string]StepResult{}}
				state, err = e.Resume(context.Background(), workflow, prior, events)
			} else {
				state, err = e.Run(context.Background(), workflow, nil, events)
			}
			assertCheckpointFailure(t, state, err)
			for _, name := range []string{"dispatched", "cleanup"} {
				if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed admission ran %s: %v", name, err)
				}
			}
			assertNoCompletionEvent(t, events)
		})
	}
}

func TestControlledPipelineCheckpointFailureStopsDependents(t *testing.T) {
	cfg := checkpointRunConfig(t)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Settings: WorkflowSettings{
		OnError:  ErrorActionContinue,
		OnCancel: []Step{{ID: "release", Command: "printf released > cleanup"}},
	}, Steps: []Step{
		{ID: "break-storage", Command: "mv .ntm/pipelines/run-checkpoint.json .ntm/pipelines/previous.json.saved; mkdir .ntm/pipelines/run-checkpoint.json; printf done > first", OnError: ErrorActionContinue},
		{ID: "forbidden", DependsOn: []string{"break-storage"}, Command: "printf unsafe > downstream"},
	}, PostPipelineSteps: []Step{{ID: "post", Command: "printf unsafe > post"}}}
	events := make(chan ProgressEvent, 100)
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, events)
	assertCheckpointFailure(t, state, err)
	if state.Steps["break-storage"].Status != StatusCompleted {
		t.Fatal("lost the outcome of work that really completed before the save failed")
	}
	for _, name := range []string{"downstream", "post"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("storage failure dispatched %s despite its checkpoint fence: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "cleanup")); err != nil || string(data) != "released" {
		t.Fatalf("failed storage prevented explicit resource cleanup: %q %v", data, err)
	}
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatalf("failed run leaked ownership after cleanup: %v", err)
	}
	owner.Close()
	assertNoCompletionEvent(t, events)
}

func TestCheckpointFailureCancelsAndJoinsRunningSibling(t *testing.T) {
	cfg := checkpointRunConfig(t)
	e := NewExecutor(cfg)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Settings: WorkflowSettings{
		OnError: ErrorActionContinue, Limits: LimitsConfig{MaxParallelSteps: 2},
	}, Steps: []Step{
		{ID: "sibling", Command: "trap 'printf stopped > stopped; exit 0' TERM; printf ready > ready; while :; do sleep 0.02; done"},
		{ID: "break-storage", Command: "while [ ! -f ready ]; do sleep 0.01; done; mv .ntm/pipelines/run-checkpoint.json .ntm/pipelines/previous.json.saved; mkdir .ntm/pipelines/run-checkpoint.json"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := e.Run(ctx, workflow, nil, nil)
	assertCheckpointFailure(t, state, err)
	if ctx.Err() != nil {
		t.Fatal("storage failure waited for the external timeout instead of cancelling active work")
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "stopped")); err != nil || string(data) != "stopped" {
		t.Fatalf("returned before the active sibling handled cancellation: %q %v", data, err)
	}
	if _, exists := state.Steps["sibling"]; !exists {
		t.Fatal("returned without joining and recording the cancelled sibling")
	}
}

func TestCheckpointExecutorCanStartNewAttemptAfterStorageRepair(t *testing.T) {
	cfg := checkpointRunConfig(t)
	e := NewExecutor(cfg)
	path := pipelineStatePath(cfg.ProjectDir, cfg.RunID)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Steps: []Step{{ID: "work", Command: "printf done > marker"}}}
	state, err := e.Run(context.Background(), workflow, nil, nil)
	assertCheckpointFailure(t, state, err)
	if err := os.Rename(path, path+".blocked"); err != nil {
		t.Fatal(err)
	}
	state, err = e.Run(context.Background(), workflow, nil, nil)
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("new attempt inherited the old sticky failure: %+v %v", state, err)
	}
	if len(state.Errors) != 0 {
		t.Fatalf("new attempt inherited old checkpoint diagnostics: %+v", state.Errors)
	}
}

func TestControlledCheckpointDryRunWritesNoArtifacts(t *testing.T) {
	cfg := checkpointRunConfig(t)
	cfg.DryRun = true
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "read-only", Steps: []Step{{ID: "work", Command: "printf unsafe > dispatched"}}}
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("dry run: %+v %v", state, err)
	}
	entries, err := os.ReadDir(cfg.ProjectDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry run wrote artifacts: %v %v", entries, err)
	}
	if !state.LastCheckpointAt.IsZero() {
		t.Fatal("dry run claimed a durable checkpoint")
	}
}

func TestCheckpointRunWaitNoneCleanupFailureCannotSucceed(t *testing.T) {
	cfg := checkpointRunConfig(t)
	e := NewExecutor(cfg)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Steps: []Step{
		{ID: "sidecar", Wait: WaitNone, Command: "trap 'mv .ntm/pipelines/run-checkpoint.json .ntm/pipelines/final.saved; mkdir .ntm/pipelines/run-checkpoint.json; exit 0' TERM; printf ready > sidecar.ready; while :; do sleep 0.02; done"},
		{ID: "finish", DependsOn: []string{"sidecar"}, Command: "while [ ! -f sidecar.ready ]; do sleep 0.01; done"},
	}}
	events := make(chan ProgressEvent, 100)
	state, err := e.Run(context.Background(), workflow, nil, events)
	assertCheckpointFailure(t, state, err)
	if !state.Steps["sidecar"].RerunOnResume {
		t.Fatal("sidecar cleanup did not settle before the run returned")
	}
	assertNoCompletionEvent(t, events)
}

func TestCheckpointHealthyCancellationRetainsCancelledOutcome(t *testing.T) {
	cfg := checkpointRunConfig(t)
	e := NewExecutor(cfg)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "checkpoint-run", Steps: []Step{
		{ID: "slow", Command: "sleep 30"},
	}, Settings: WorkflowSettings{OnCancel: []Step{{ID: "release", Command: "printf released > cleanup"}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	state, err := e.Run(ctx, workflow, nil, nil)
	if err == nil || errors.Is(err, ErrCheckpointFailed) || state.Status != StatusCancelled {
		t.Fatalf("healthy cancellation was reclassified as storage failure: %+v %v", state, err)
	}
	saved, err := LoadState(cfg.ProjectDir, state.RunID)
	if err != nil || saved.Status != StatusCancelled || saved.Steps["release"].Status != StatusCompleted {
		t.Fatalf("cancelled run did not persist completed cleanup: %+v %v", saved, err)
	}
}
