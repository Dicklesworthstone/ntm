package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These tests exercise the complete native Run surface, not the isolated
// scheduler callbacks. They require the repository's Go toolchain/dependencies.
func TestParallelChildDAGRunPublishesProducerOutputs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	cfg := DefaultExecutorConfig("parallel-dependency-test")
	cfg.ProjectDir = t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-dependencies",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{
				ID:        "consumer",
				DependsOn: []string{"group_producer"},
				Command:   `test '${steps.group_producer.output}' = ready && test '${vars.named}' = ready && printf consumed`,
			},
			{ID: "producer", Command: "printf ready", OutputVar: "named"},
		}}}},
	}
	workflow.Settings.Limits.SubstepParallelMax = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := NewExecutor(cfg).Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := state.Steps["group_consumer"]; got.Status != StatusCompleted || got.Output != "consumed" {
		t.Fatalf("consumer result: %+v", got)
	}
	if workflow.Steps[0].Parallel.Steps[0].DependsOn[0] != "group_producer" || workflow.Steps[0].Parallel.Steps[1].ID != "producer" {
		t.Fatal("runtime scheduling mutated the authored workflow")
	}
}

func TestParallelChildDAGRunContinueBlocksFailedDependencyEffects(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	cfg := DefaultExecutorConfig("parallel-failure-test")
	cfg.ProjectDir = t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-failure-dependencies",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{ID: "group", OnError: ErrorActionContinue, Parallel: ParallelSpec{Steps: []Step{
			{ID: "grandchild", DependsOn: []string{"consumer"}, Command: "printf forbidden > grandchild-effect"},
			{ID: "consumer", DependsOn: []string{"producer"}, Command: "printf forbidden > consumer-effect"},
			{ID: "producer", Command: "exit 13"},
			{ID: "independent", Command: "printf allowed > independent-effect"},
		}}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := NewExecutor(cfg).Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("continue workflow failed: %v", err)
	}
	for _, child := range []string{"consumer", "grandchild"} {
		got := state.Steps["group_"+child]
		if got.Status != StatusSkipped || got.SkipKind != SkipKindFailedDependency {
			t.Errorf("%s result: %+v", child, got)
		}
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, child+"-effect")); !os.IsNotExist(err) {
			t.Errorf("failed dependency did not prevent %s effect: %v", child, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "independent-effect")); err != nil || string(data) != "allowed" {
		t.Fatalf("independent work suppressed: data=%q err=%v", data, err)
	}
}

func TestParallelChildDAGContinueDoesNotHideCancellation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	cfg := DefaultExecutorConfig("parallel-cancellation-test")
	cfg.ProjectDir = t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "parallel-mixed-cancellation", Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "group", OnError: ErrorActionContinue, Parallel: ParallelSpec{Steps: []Step{
			{ID: "failure", Command: "exit 13"},
			{ID: "blocked", Command: "sleep 60"},
		}}}},
	}
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID: "parallel-mixed-cancellation", WorkflowID: workflow.Name,
		Status: StatusRunning, StartedAt: time.Now(),
		Steps: make(map[string]StepResult), Variables: make(map[string]interface{}),
	}
	e.limits.SubstepParallelMax = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan StepResult, 1)
	go func() { done <- e.executeParallel(ctx, &workflow.Steps[0], workflow) }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
waitFailure:
	for {
		select {
		case result := <-done:
			t.Fatalf("group returned before cancellation: %+v", result)
		case <-deadline.C:
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("group did not acknowledge cancellation after timeout")
			}
			t.Fatal("failed sibling was not checkpointed")
		case <-ticker.C:
			e.stateMu.RLock()
			failed := e.state.Steps["group_failure"].Status == StatusFailed
			e.stateMu.RUnlock()
			if failed {
				break waitFailure
			}
		}
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != StatusCancelled {
			t.Fatalf("continue hid cancellation: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled parallel group did not join its children")
	}
}

// Native end-to-end coverage for the new receipt field, SaveState/LoadState,
// Resume alias rebuilding, and the actual foreach/command lifecycles.
func TestParallelChildDAGResumeRestoresCollectedOutputWithoutReplay(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	cfg := DefaultExecutorConfig("parallel-collection-resume-test")
	cfg.ProjectDir = t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "parallel-collection-resume", Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "consumer", DependsOn: []string{"each"}, Command: `test -f allow-consumer && test '${vars.values.0}' = one && test '${vars.values.1}' = two && printf consumed`},
			{ID: "each", OutputVar: "values", Foreach: &ForeachConfig{
				Items: "${vars.items}", As: "item", Steps: []Step{
					{ID: "work", Command: `printf ran >> producer-effects; printf '%s' '${item}'`, OutputVar: "values"},
				},
			}},
		}}}},
	}
	workflow.Settings.Limits.SubstepParallelMax = 1
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := NewExecutor(cfg).Run(ctx, workflow, map[string]interface{}{"items": []interface{}{"one", "two"}}, nil)
	if err == nil || first == nil || first.Steps["group_each"].Status != StatusCompleted {
		t.Fatalf("first attempt did not complete collection and fail consumer: state=%+v err=%v", first, err)
	}
	prior, err := LoadState(cfg.ProjectDir, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if prior.ParallelState["group"].ChildCheckpoints["group_each"] == "" {
		t.Fatal("native checkpoint lost the completed collection receipt")
	}
	// This represents the summary substitution that ordinary Resume must not
	// trust as the source of a previously completed foreach collection.
	prior.Variables["values"] = "Foreach completed"
	prior.Variables["values_parsed"] = "stale"
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "allow-consumer"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.ResumeOptions = ResumeOptions{Mode: ResumeModeRestartFailed}
	resumed, err := NewExecutor(cfg).Resume(ctx, workflow, prior, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if result := resumed.Steps["group_consumer"]; result.Status != StatusCompleted || result.Output != "consumed" {
		t.Fatalf("consumer did not receive restored collection: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "producer-effects"))
	if err != nil || string(data) != "ranran" {
		t.Fatalf("foreach replayed completed effects: %q %v", data, err)
	}
	if _, exists := resumed.Variables["values_parsed"]; exists {
		t.Fatal("retained collection restored stale parsed data")
	}
}

func TestParallelChildDAGSnapshotOwnsCompletionReceiptMap(t *testing.T) {
	e := &Executor{state: &ExecutionState{ParallelState: map[string]ParallelGroupState{
		"group": {StepID: "group", ChildCheckpoints: map[string]string{"group_child": "immutable receipt"}},
	}}}
	snapshot := e.snapshotState()
	snapshot.ParallelState["group"].ChildCheckpoints["group_child"] = "changed by snapshot caller"
	if e.state.ParallelState["group"].ChildCheckpoints["group_child"] != "immutable receipt" {
		t.Fatal("snapshot shares the live parallel receipt map")
	}
}

// Unlike the earlier interrupted-body probe, the entire outer producer is
// completed here. Resume must restore its nested collections before dispatching
// the independent top-level consumer, without entering the producer again.
func TestParallelChildDAGResumeCompletedOuterRestoresNestedCollections(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	for _, mode := range []OutputVarMode{OutputVarModeAggregate, OutputVarModeCollect, OutputVarModeLast} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := DefaultExecutorConfig("completed-outer-resume")
			cfg.ProjectDir = t.TempDir()
			check := `test '${vars.values.0}' = one && test '${vars.values.1}' = two`
			if mode == OutputVarModeCollect {
				check = `test '${vars.values.one}' = one && test '${vars.values.two}' = two`
			}
			if mode == OutputVarModeLast {
				check = `test '${vars.values}' = two`
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "completed-outer-resume", Settings: DefaultWorkflowSettings(),
				Steps: []Step{
					{ID: "outer", Parallel: ParallelSpec{Steps: []Step{
						{ID: "inner", Parallel: ParallelSpec{Steps: []Step{
							{ID: "each", OutputVar: "values", Foreach: &ForeachConfig{
								Items: "${vars.items}", As: "item", OutputVarMode: mode,
								Steps: []Step{{ID: "work", OutputVar: "values", Command: `printf effect >> producer-effects; printf '%s' '${item}'`}},
							}},
						}}},
					}}},
					{ID: "consumer", DependsOn: []string{"outer"}, Command: `test -f allow-consumer && ` + check + ` && printf consumed`},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			first, err := NewExecutor(cfg).Run(ctx, workflow, map[string]interface{}{"items": []interface{}{"one", "two"}}, nil)
			if err == nil || first == nil || first.Steps["outer"].Status != StatusCompleted {
				t.Fatalf("first attempt: state=%+v error=%v", first, err)
			}
			prior, err := LoadState(cfg.ProjectDir, first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			prior.Variables["values"], prior.Variables["values_parsed"] = "stale summary", "stale parsed"
			if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "allow-consumer"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cfg.ResumeOptions = ResumeOptions{Mode: ResumeModeRestartFailed}
			resumed, err := NewExecutor(cfg).Resume(ctx, workflow, prior, nil)
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if got := resumed.Steps["consumer"]; got.Status != StatusCompleted || got.Output != "consumed" {
				t.Fatalf("consumer: %+v", got)
			}
			if got, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "producer-effects")); err != nil || string(got) != "effecteffect" {
				t.Fatalf("completed producer was repeated: %q %v", got, err)
			}
			if _, exists := resumed.Variables["values_parsed"]; exists {
				t.Fatal("stale parsed alias survived restoration")
			}
		})
	}
}

// Exercise the native Run/LoadState/Resume and shell journals. This test is
// shipped with the patch but cannot run in the reduced validation harness.
func TestParallelChildDAGResumeFinishesSuccessHooksWithoutRepeatingProducer(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires a POSIX shell")
	}
	cfg := DefaultExecutorConfig("parallel-hook-recovery")
	cfg.ProjectDir = t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "parallel-hook-recovery", Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "group", Parallel: ParallelSpec{Steps: []Step{
			{ID: "producer", Command: "printf 'producer\\n' >> hook-effects; printf value", OnSuccess: []Step{
				{ID: "first", Command: "printf 'first\\n' >> hook-effects"},
				{ID: "second", Command: "touch hook-started; while [ ! -f resume-ok ]; do sleep 0.01; done; printf 'second\\n' >> hook-effects"},
			}},
			{ID: "consumer", DependsOn: []string{"producer"}, Command: "printf 'consumer\\n' >> hook-effects; printf done"},
		}}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type runResult struct {
		state *ExecutionState
		err   error
	}
	done := make(chan runResult, 1)
	go func() {
		state, err := NewExecutor(cfg).Run(ctx, workflow, nil, nil)
		done <- runResult{state, err}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
waitHook:
	for {
		select {
		case r := <-done:
			t.Fatalf("Run returned before interruptible hook: %v, state=%+v", r.err, r.state)
		case <-ctx.Done():
			t.Fatal("hook did not reach its cancellation boundary")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "hook-started")); err == nil {
				break waitHook
			}
		}
	}
	cancel()
	var first runResult
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not join interrupted hook command")
	}
	if first.state == nil || first.state.Status != StatusCancelled {
		t.Fatalf("cancelled Run: state=%+v error=%v", first.state, first.err)
	}
	prior, err := LoadState(cfg.ProjectDir, first.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "resume-ok"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	resumeCtx, stopResume := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopResume()
	state, err := NewExecutor(cfg).Resume(resumeCtx, workflow, prior, nil)
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("Resume: state=%+v error=%v", state, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "hook-effects"))
	if err != nil || string(data) != "producer\nfirst\nsecond\nconsumer\n" {
		t.Fatalf("lost or repeated effects: %q; %v", data, err)
	}
}
