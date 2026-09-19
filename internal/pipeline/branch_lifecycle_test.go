//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func branchLifecycleConfig(t *testing.T) ExecutorConfig {
	t.Helper()
	cfg := DefaultExecutorConfig("branch-lifecycle")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-branch-lifecycle"
	cfg.GlobalTimeout, cfg.DefaultTimeout = 5*time.Second, 3*time.Second
	return cfg
}

func branchLifecycleWorkflow(children ...Step) *Workflow {
	return &Workflow{SchemaVersion: SchemaVersion, Name: "branch-lifecycle", Steps: []Step{{
		ID: "route", Branch: "selected", Branches: map[string]interface{}{"selected": children},
	}}}
}

func assertBranchFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, want %q: %v", name, data, want, err)
	}
}

func assertBranchFileAbsent(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected side effect %s: %v", name, err)
	}
}

func TestBranchLifecycleHonorsChildWhen(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "never", Command: "printf forbidden > forbidden", When: "false", OnSuccess: []Step{{ID: "hook", Command: "printf forbidden > hook"}}},
		Step{ID: "after", Command: "printf reached > after"},
	)
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("branch condition: %+v %v", state, err)
	}
	assertBranchFileAbsent(t, cfg.ProjectDir, "forbidden")
	assertBranchFileAbsent(t, cfg.ProjectDir, "hook")
	assertBranchFile(t, cfg.ProjectDir, "after", "reached")
	if got := state.Steps["route_never"]; got.Status != StatusSkipped || got.SkipKind != SkipKindWhenCondition {
		t.Fatalf("condition did not produce a structured skip: %+v", got)
	}
}

func TestBranchLifecycleRetriesAndFiresSuccessHookOnce(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherited=%v", inherited), func(t *testing.T) {
			cfg := branchLifecycleConfig(t)
			child := Step{
				ID: "work", Command: "printf x >> attempts; if [ ! -f tried ]; then printf tried > tried; exit 7; fi; printf recovered",
				OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond},
				OnSuccess: []Step{{ID: "receipt", Command: "printf '%s' '${steps.route_work.output}' >> hook"}},
			}
			workflow := branchLifecycleWorkflow(child)
			if inherited {
				workflow.Settings.OnError = ErrorActionRetry
				children := workflow.Steps[0].Branches["selected"].([]Step)
				children[0].OnError = ""
			}
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("branch retry: %+v %v", state, err)
			}
			assertBranchFile(t, cfg.ProjectDir, "attempts", "xx")
			assertBranchFile(t, cfg.ProjectDir, "hook", "recovered")
			if got := state.Steps["route_work"]; got.Attempts != 2 || got.Error != nil || got.Output != "recovered" {
				t.Fatalf("lost canonical retry result: %+v", got)
			}
		})
	}
}

func TestBranchLifecycleExhaustedRetryPreservesFailureAndStopsSequence(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "bad", Command: "printf x >> attempts; printf partial; exit 9", OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}},
		Step{ID: "after", Command: "printf forbidden > forbidden"},
	)
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("exhausted branch retry reported success: %+v %v", state, err)
	}
	assertBranchFile(t, cfg.ProjectDir, "attempts", "xxx")
	assertBranchFileAbsent(t, cfg.ProjectDir, "forbidden")
	got := state.Steps["route_bad"]
	if got.Attempts != 3 || got.Output != "partial" || got.Error == nil || got.Error.Type != "exit" {
		t.Fatalf("lost partial output, exit diagnostics, or attempt count: %+v", got)
	}
}

func TestBranchLifecyclePublishesLocalOutputsBeforeNextChild(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "producer", Command: "printf '{\"ok\":true}'", OutputVar: "payload", OutputParse: OutputParse{Type: "json"}},
		Step{ID: "consumer", Command: "printf '%s|%s' '${vars.payload}' '${steps.route_producer.output}' > received"},
	)
	vars := map[string]interface{}{"payload": "outer", "payload_parsed": "outer-parsed"}
	state, err := RunControlledPipeline(context.Background(), workflow, vars, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("branch producer/consumer: %+v %v", state, err)
	}
	assertBranchFile(t, cfg.ProjectDir, "received", "{\"ok\":true}|{\"ok\":true}")
	if state.Variables["payload"] != "outer" || state.Variables["payload_parsed"] != "outer-parsed" {
		t.Fatalf("branch did not restore shadowed values: %+v", state.Variables)
	}
	if !reflect.DeepEqual(state.Steps["route_producer"].ParsedData, map[string]interface{}{"ok": true}) {
		t.Fatal("parsed child data was not retained in its result")
	}
}

func TestBranchLifecycleDoesNotLeakNewParsedOutput(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	state, err := RunControlledPipeline(context.Background(), branchLifecycleWorkflow(
		Step{ID: "producer", Command: "printf '{\"ok\":true}'", OutputVar: "local", OutputParse: OutputParse{Type: "json"}},
	), nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("branch local data: %+v %v", state, err)
	}
	for _, key := range []string{"local", "local_parsed", "steps.route_producer.output", "steps.route_producer.data"} {
		if _, ok := state.Variables[key]; ok {
			t.Fatalf("branch-local key %q escaped its scope", key)
		}
	}
}

func TestBranchLifecycleRejectsUnresolvedSelectorBeforeShellOrDefault(t *testing.T) {
	for _, shell := range []bool{false, true} {
		t.Run(fmt.Sprintf("shell=%v", shell), func(t *testing.T) {
			cfg := branchLifecycleConfig(t)
			workflow := branchLifecycleWorkflow(Step{ID: "unused", Command: "true"})
			workflow.Steps[0].Branch = "${vars.missing}"
			if shell {
				workflow.Steps[0].Branch = "$(printf side-effect > selector-effect; printf '%s' '${vars.missing}')"
			}
			workflow.Steps[0].Branches["default"] = []Step{{ID: "fallback", Command: "printf forbidden > fallback-effect"}}
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || state == nil || state.Status != StatusFailed {
				t.Fatalf("unresolved selector chose work: %+v %v", state, err)
			}
			assertBranchFileAbsent(t, cfg.ProjectDir, "selector-effect")
			assertBranchFileAbsent(t, cfg.ProjectDir, "fallback-effect")
			if got := state.Steps["route"].Error; got == nil || !strings.Contains(got.Message, "substitution failed") {
				t.Fatalf("lost selector substitution diagnostic: %+v", got)
			}
		})
	}
}

func TestBranchLifecycleCheckpointsBeforeChildDispatch(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(Step{ID: "never", Command: "printf forbidden > forbidden"})
	// The selector succeeds but makes the next checkpoint unwritable. The
	// child must not start merely because its parent was checkpointed earlier.
	workflow.Steps[0].Branch = "$(mv .ntm/pipelines/run-branch-lifecycle.json saved-checkpoint; mkdir .ntm/pipelines/run-branch-lifecycle.json; printf selected)"
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if !errors.Is(err, ErrCheckpointFailed) || state == nil || state.Status != StatusFailed {
		t.Fatalf("child admission did not surface checkpoint failure: %+v %v", state, err)
	}
	assertBranchFileAbsent(t, cfg.ProjectDir, "forbidden")
}

func TestBranchLifecycleCheckpointFailureStopsNextChild(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "damage", Command: "mv .ntm/pipelines/run-branch-lifecycle.json saved-checkpoint; mkdir .ntm/pipelines/run-branch-lifecycle.json; printf retained"},
		Step{ID: "never", Command: "printf forbidden > forbidden"},
	)
	workflow.Settings.OnError = ErrorActionContinue
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if !errors.Is(err, ErrCheckpointFailed) || state == nil || state.Status != StatusFailed {
		t.Fatalf("completion checkpoint failure was hidden: %+v %v", state, err)
	}
	assertBranchFileAbsent(t, cfg.ProjectDir, "forbidden")
	if state.Steps["route_damage"].Output != "retained" {
		t.Fatal("completed work lost its in-memory result on storage failure")
	}
}

type branchLifecycleOutcome struct {
	state *ExecutionState
	err   error
}

func TestBranchLifecyclePersistsCompletedChildWhileNextChildRuns(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "first", Command: "printf completed", OutputVar: "first_output"},
		Step{ID: "waiting", Command: "printf ready > waiting; while [ ! -f release ]; do sleep 0.01; done; printf finished"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan branchLifecycleOutcome, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		st, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
		done <- branchLifecycleOutcome{st, err}
	}()
	t.Cleanup(func() { cancel(); <-exited })
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "waiting")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second branch child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	checkpoint, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if first := checkpoint.Steps["route_first"]; first.Status != StatusCompleted || first.Output != "completed" {
		t.Fatalf("running branch has no completed-child checkpoint: %+v", checkpoint)
	}
	if _, ok := checkpoint.InFlightSteps["route_waiting"]; !ok {
		t.Fatal("running child has no in-flight marker")
	}
	if _, ok := checkpoint.InFlightSteps["route_first"]; ok {
		t.Fatal("completed child remained in flight")
	}
	if checkpoint.Variables["first_output"] != "completed" {
		t.Fatal("completed child's output was not checkpointed")
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil || out.state.Status != StatusCompleted {
		t.Fatalf("branch did not finish: %+v", out)
	}
}

func TestBranchLifecycleScopeDoesNotUndoSiblingUpdates(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(Step{ID: "wait", Command: "printf ready > branch-ready; while [ ! -f release ]; do sleep 0.01; done"})
	workflow.Steps = append(workflow.Steps, Step{ID: "sibling", Command: "while [ ! -f branch-ready ]; do sleep 0.01; done; printf newer", OutputVar: "outside"})
	executor := NewExecutor(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan branchLifecycleOutcome, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		st, err := executor.Run(ctx, workflow, map[string]interface{}{"outside": "older"}, nil)
		done <- branchLifecycleOutcome{st, err}
	}()
	t.Cleanup(func() { cancel(); <-exited })
	deadline := time.Now().Add(4 * time.Second)
	for {
		if st := executor.GetState(); st != nil && st.Variables["outside"] == "newer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sibling did not update its output while the branch was running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil || out.state.Status != StatusCompleted || out.state.Variables["outside"] != "newer" {
		t.Fatalf("branch rolled back a completed sibling's update: %+v %v", out.state, out.err)
	}
}

func TestBranchLifecycleScopeDoesNotResurrectUnrelatedDeletedKey(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{"owned": "old-local", "unrelated": "old-sibling"}}
	snapshot := captureAllVariables(state.Variables)
	state.Variables["owned"] = "new-local"
	delete(state.Variables, "unrelated")
	restoreBranchVariables(state, snapshot, nil, branchBodyOutputVars([]Step{{OutputVar: "owned"}}))
	if state.Variables["owned"] != "old-local" {
		t.Fatal("lost restoration of owned output")
	}
	if _, ok := state.Variables["unrelated"]; ok {
		t.Fatal("branch resurrected an unrelated deleted key")
	}
}

func TestBranchLifecycleRuntimeRecoverySignalSurvivesScope(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	workflow := branchLifecycleWorkflow(
		Step{ID: "recover", Command: "exit 3", OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"}},
		Step{ID: "after", Command: "printf after > after"},
	)
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("branch recovery: %+v %v", state, err)
	}
	if got := state.Steps["route_recover"]; got.Status != StatusSkipped || got.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("lost recovery action: %+v", got)
	}
	if state.Variables["runtime.route_recover_failure_action"] != "fallback_to_ntm_inbox" {
		t.Fatal("scope erased global recovery signal")
	}
	assertBranchFile(t, cfg.ProjectDir, "after", "after")
}

func TestBranchLifecycleDirectDispatchWithoutOuterGraph(t *testing.T) {
	cfg := branchLifecycleConfig(t)
	e := NewExecutor(cfg)
	e.state = &ExecutionState{RunID: cfg.RunID, WorkflowID: "direct", Steps: make(map[string]StepResult), Variables: make(map[string]interface{})}
	workflow := branchLifecycleWorkflow(Step{ID: "child", Command: "printf done > direct"})
	result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("direct branch dispatch: %+v", result)
	}
	assertBranchFile(t, cfg.ProjectDir, "direct", "done")
}
