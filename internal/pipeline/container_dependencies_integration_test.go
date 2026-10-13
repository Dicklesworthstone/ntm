//go:build unix

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContainerDependenciesControlledAdmissionWaitsForProducer(t *testing.T) {
	cfg := DefaultExecutorConfig("dependency-admission")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-admission"
	cfg.GlobalTimeout = 5 * time.Second
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "admission", Steps: []Step{
		{ID: "a_group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "consume", DependsOn: []string{"z_prepare"}, Command: "test -f input && printf consumed > result"},
		}}},
		{ID: "z_prepare", Command: "printf ready > input"},
	}}
	// Force deterministic scheduling: the original graph admits a_group first
	// and runs its child before the producer. No sleeps or timing assumptions.
	workflow.Settings.Limits.MaxParallelSteps = 1
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("admission: %+v %v", state, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "result"))
	if err != nil || string(data) != "consumed" {
		t.Fatalf("consumer did not run after producer: %q %v", data, err)
	}
	producer, child := state.Steps["z_prepare"], state.Steps["a_group_consume"]
	if child.StartedAt.Before(producer.FinishedAt) {
		t.Fatal("child started before producer finished")
	}
}

func TestContainerDependenciesControlledFailureSkipsWholeGroup(t *testing.T) {
	cfg := DefaultExecutorConfig("dependency-failure")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-admission-failure"
	cfg.GlobalTimeout = 5 * time.Second
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "failure-admission", Steps: []Step{
		{ID: "a_group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "consume", DependsOn: []string{"z_prepare"}, Command: "printf unsafe > forbidden"},
		}}},
		{ID: "after", DependsOn: []string{"a_group"}, Command: "printf unsafe > downstream"},
		{ID: "z_prepare", Command: "exit 9", OnError: ErrorActionContinue},
		{ID: "unrelated", Command: "printf ok > unrelated"},
	}}
	workflow.Settings.Limits.MaxParallelSteps = 1
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("continue-mode admission: %+v %v", state, err)
	}
	for _, name := range []string{"forbidden", "downstream"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !os.IsNotExist(err) {
			t.Fatalf("blocked effect %s exists: %v", name, err)
		}
	}
	for _, id := range []string{"a_group", "after"} {
		if state.Steps[id].SkipKind != SkipKindFailedDependency {
			t.Fatalf("%s lost dependency failure: %+v", id, state.Steps[id])
		}
	}
	if state.Steps["unrelated"].Status != StatusCompleted {
		t.Fatal("independent work was blocked")
	}
}

func TestContainerDependenciesControlledCycleStartsNoWork(t *testing.T) {
	cfg := DefaultExecutorConfig("dependency-cycle")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-admission-cycle"
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "cyclic-admission", Steps: []Step{
		{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "consume", DependsOn: []string{"prepare"}, Command: "printf unsafe > forbidden"},
		}}},
		{ID: "prepare", DependsOn: []string{"group"}, Command: "printf unsafe > prepared"},
	}}
	_, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil {
		t.Fatal("accepted cycle through container ownership")
	}
	for _, name := range []string{"forbidden", "prepared"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !os.IsNotExist(err) {
			t.Fatalf("cyclic workflow dispatched %s: %v", name, err)
		}
	}
}

func TestContainerDependenciesControlledResumeRetainsProducer(t *testing.T) {
	cfg := DefaultExecutorConfig("dependency-resume")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-admission-resume"
	cfg.GlobalTimeout = 5 * time.Second
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "resume-admission", Steps: []Step{
		{ID: "a_group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "consume", DependsOn: []string{"z_prepare"}, Command: "test -f release && test -f input && printf done > result"},
		}}},
		{ID: "z_prepare", Command: "printf x >> once; printf input > input"},
	}}
	workflow.Settings.Limits.MaxParallelSteps = 1
	first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || first == nil || first.Status != StatusFailed || first.Steps["z_prepare"].Status != StatusCompleted {
		t.Fatalf("first attempt did not checkpoint producer before failed consumer: %+v %v", first, err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("load frozen workflow: %v %+v", err, valid)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	for name, want := range map[string]string{"once": "x", "result": "done"} {
		got, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, want %q: %v", name, got, want, err)
		}
	}
}
