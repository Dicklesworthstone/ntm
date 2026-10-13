//go:build unix

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func branchChildFixture(root string) (ExecutorConfig, *Workflow) {
	cfg := DefaultExecutorConfig("branch-child-recovery")
	cfg.ProjectDir, cfg.RunID = root, "run-branch-child"
	cfg.GlobalTimeout, cfg.DefaultTimeout = 10*time.Second, 3*time.Second
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "branch-child-recovery", Steps: []Step{{
		ID: "route", Branch: "$(printf x >> selections; cat choice)", Branches: map[string]interface{}{
			"a": []Step{
				{ID: "once", Command: "printf x >> once; printf '{\"n\":7}'", OutputVar: "packet", OutputParse: OutputParse{Type: "json"},
					OnSuccess: []Step{{ID: "receipt", Command: "printf x >> receipt"}}},
				{ID: "finish", Command: "test -f release && printf '%s' '${vars.packet_parsed.n}' > marker"},
			},
			"b": []Step{{ID: "wrong", Command: "printf wrong > wrong"}},
		},
	}}}
	return cfg, workflow
}

func branchChildExecutor(t *testing.T, root string, workflow *Workflow) *Executor {
	t.Helper()
	cfg, _ := branchChildFixture(root)
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{RunID: cfg.RunID, WorkflowID: workflow.Name, Session: cfg.Session, Status: StatusRunning,
		Steps: make(map[string]StepResult), Variables: make(map[string]interface{})}
	e.limits = workflow.Settings.Limits.EffectiveLimits()
	return e
}

func branchChildWrite(t *testing.T, root, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func branchChildRead(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, want %q: %v", name, data, want, err)
	}
}

// Re-exec the actual public foreground entry and resume engine in different
// processes, so neither in-memory variables nor a living original executor
// can make recovery appear to work.
func TestBranchChildRecoveryProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--branch-child" {
		return
	}
	mode, root := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	cfg, workflow := branchChildFixture(root)
	if mode == "first" {
		state, err := RunControlledPipeline(context.Background(), workflow, map[string]interface{}{"packet": "outer"}, cfg, nil)
		if err == nil || state == nil || state.Status != StatusFailed {
			t.Fatalf("expected interrupted branch: %+v %v", state, err)
		}
		return
	}
	owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(root, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("load saved workflow: %v %+v", err, validation)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	state, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("resume branch: %+v %v", state, err)
	}
	if state.Variables["packet"] != "outer" {
		t.Fatalf("local output escaped recovery scope: %#v", state.Variables["packet"])
	}
	if _, exists := state.Variables["packet_parsed"]; exists {
		t.Fatal("parsed local output escaped recovery scope")
	}
}

func TestBranchChildRecoverySurvivesProcessExit(t *testing.T) {
	root := t.TempDir()
	branchChildWrite(t, root, "choice", "a")
	for _, mode := range []string{"first", "resume"} {
		if mode == "resume" {
			branchChildWrite(t, root, "choice", "b")
			branchChildWrite(t, root, "release", "go")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBranchChildRecoveryProcess$", "--", "--branch-child", mode, root)
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s process: %v\n%s", mode, err, output)
		}
	}
	for name, want := range map[string]string{"once": "x", "receipt": "x", "selections": "x", "marker": "7"} {
		branchChildRead(t, root, name, want)
	}
	if _, err := os.Stat(filepath.Join(root, "wrong")); !os.IsNotExist(err) {
		t.Fatal("recovery selected new work")
	}
}

func TestBranchChildRecoveryRetainsConditionalCompletionAndTimestamps(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	body := workflow.Steps[0].Branches["a"].([]Step)
	body[0].When = "${vars.enabled}"
	branchChildWrite(t, root, "choice", "a")
	e := branchChildExecutor(t, root, workflow)
	e.state.Variables["enabled"] = true
	if first := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); first.Status != StatusFailed {
		t.Fatalf("first: %+v", first)
	}
	before := e.state.Steps["route_once"]
	prior, err := LoadState(root, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	recovered := branchChildExecutor(t, root, workflow)
	recovered.state = prior
	recovered.state.Variables["enabled"] = false
	branchChildWrite(t, root, "release", "go")
	result := recovered.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("restored child output unavailable: %+v", result)
	}
	after := recovered.state.Steps["route_once"]
	if after.Status != StatusCompleted || !after.FinishedAt.Equal(before.FinishedAt) || after.Attempts != before.Attempts {
		t.Fatalf("recovery reevaluated a settled child's condition or lifecycle: before=%+v after=%+v", before, after)
	}
	branchChildRead(t, root, "marker", "7")
}

func TestBranchChildRecoveryKeepsStableSkippedDecision(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branches["a"] = []Step{
		{ID: "skip", Command: "printf forbidden > forbidden", When: "${vars.enabled}"},
		{ID: "finish", Command: "test -f release"},
	}
	branchChildWrite(t, root, "choice", "a")
	e := branchChildExecutor(t, root, workflow)
	e.state.Variables["enabled"] = false
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusFailed {
		t.Fatalf("first: %+v", result)
	}
	e.state.Variables["enabled"] = true
	branchChildWrite(t, root, "release", "go")
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusCompleted {
		t.Fatalf("retry: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("recovery executed a previously skipped child")
	}
	if e.state.Steps["route_skip"].SkipKind != SkipKindWhenCondition {
		t.Fatal("lost original skip evidence")
	}
}

func TestBranchChildRecoveryParentRetryDoesNotReplayCompletedWork(t *testing.T) {
	root := t.TempDir()
	cfg, workflow := branchChildFixture(root)
	workflow.Steps[0].OnError, workflow.Steps[0].RetryCount = ErrorActionRetry, 1
	workflow.Steps[0].RetryDelay = Duration{Duration: time.Millisecond}
	body := workflow.Steps[0].Branches["a"].([]Step)
	body[1].Command = "if [ ! -f tried ]; then printf x > tried; exit 9; fi; printf ok"
	branchChildWrite(t, root, "choice", "a")
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("retry: %+v %v", state, err)
	}
	for _, name := range []string{"once", "receipt", "selections"} {
		branchChildRead(t, root, name, "x")
	}
	if state.Steps["route"].Attempts != 2 {
		t.Fatal("parent retry was not exercised")
	}
}

func TestBranchChildRecoveryFinishesOnlyMissingHooks(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Steps[0].Branches["a"] = []Step{{ID: "once", Command: "printf duplicate >> once", OutputVar: "value",
		OnSuccess: []Step{{ID: "first", Command: "printf duplicate >> hook1"}, {ID: "second", Command: "printf '%s' '${steps.route_once.output}' > hook2"}}}}
	e := branchChildExecutor(t, root, workflow)
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	// A native command receipt can restore this completed result while its
	// hooks were interrupted, before a branch-level child receipt existed.
	now := time.Now()
	e.state.Steps["route_once"] = StepResult{StepID: "route_once", Status: StatusCompleted, Output: "saved", StartedAt: now, FinishedAt: now}
	e.state.Steps["route_once_on_success_first"] = StepResult{StepID: "route_once_on_success_first", Status: StatusCompleted, StartedAt: now, FinishedAt: now}
	branchChildWrite(t, root, "once", "original")
	branchChildWrite(t, root, "hook1", "original")
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusCompleted {
		t.Fatalf("hooks: %+v", result)
	}
	branchChildRead(t, root, "once", "original")
	branchChildRead(t, root, "hook1", "original")
	branchChildRead(t, root, "hook2", "saved")
}

func TestBranchChildRecoveryRefusesDamagedCompletion(t *testing.T) {
	for _, damage := range []string{"missing_result", "changed_result", "foreign_alias", "changed_alias", "unknown_child", "foreign_scope"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			_, workflow := branchChildFixture(root)
			branchChildWrite(t, root, "choice", "a")
			e := branchChildExecutor(t, root, workflow)
			if first := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); first.Status != StatusFailed {
				t.Fatalf("first: %+v", first)
			}
			id := branchDecisionID("route")
			saved := e.state.Steps[id]
			metadata := saved.ParsedData.(map[string]interface{})
			switch damage {
			case "missing_result":
				delete(e.state.Steps, "route_once")
			case "changed_result":
				child := e.state.Steps["route_once"]
				child.Output = "corrupt"
				e.state.Steps["route_once"] = child
			default:
				// Work with serialized metadata so this test also compiles on
				// the original decision-only implementation for baseline probes.
				children, _ := metadata["children"].(map[string]interface{})
				if children == nil {
					children = map[string]interface{}{}
					metadata["children"] = children
				}
				receipt, _ := children["route_once"].(map[string]interface{})
				if receipt == nil {
					receipt = map[string]interface{}{"result_hash": strings.Repeat("0", 64)}
					children["route_once"] = receipt
				}
				if damage == "foreign_alias" {
					receipt["outputs"] = map[string]interface{}{"unrelated": "wrong"}
				}
				if damage == "changed_alias" {
					receipt["outputs"] = map[string]interface{}{"packet": "wrong"}
				}
				if damage == "unknown_child" {
					children["route_unknown"] = receipt
				}
				if damage == "foreign_scope" {
					metadata["scope"] = map[string]interface{}{"unrelated": map[string]interface{}{"exists": true, "value": "wrong"}}
				}
			}
			e.state.Steps[id] = saved
			branchChildWrite(t, root, "release", "go")
			result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
			if result.Status != StatusFailed {
				t.Fatalf("accepted damaged completion (%s): %+v", damage, result)
			}
			if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
				t.Fatal("corrupt recovery reached later child")
			}
			branchChildRead(t, root, "once", "x")
		})
	}
}

func TestBranchChildRecoveryCopiesSavedOutputValues(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	branchChildWrite(t, root, "choice", "a")
	e := branchChildExecutor(t, root, workflow)
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusFailed {
		t.Fatalf("first: %+v", result)
	}
	snapshot := e.snapshotState()
	entry := snapshot.Steps[branchDecisionID("route")].ParsedData.(map[string]interface{})
	children, ok := entry["children"].(map[string]interface{})
	if !ok {
		t.Fatal("missing child alias checkpoint")
	}
	child := children["route_once"].(map[string]interface{})
	child["outputs"].(map[string]interface{})["packet_parsed"].(map[string]interface{})["n"] = float64(99)
	original := e.state.Steps[branchDecisionID("route")].ParsedData.(map[string]interface{})
	outputs := original["children"].(map[string]interface{})["route_once"].(map[string]interface{})["outputs"].(map[string]interface{})
	if outputs["packet_parsed"].(map[string]interface{})["n"] != float64(7) {
		t.Fatal("snapshot aliases live branch recovery data")
	}
}

func TestBranchChildRecoveryConcurrentBranchesKeepIndependentScope(t *testing.T) {
	root := t.TempDir()
	cfg, workflow := branchChildFixture(root)
	workflow.Steps = nil
	for _, id := range []string{"left", "right"} {
		workflow.Steps = append(workflow.Steps, Step{ID: id, Branch: "a", Branches: map[string]interface{}{
			"a": []Step{{ID: "once", Command: fmt.Sprintf("printf x >> %s.once; printf %s", id, id), OutputVar: id + "_value"},
				{ID: "finish", Command: fmt.Sprintf("test -f release && printf '%%s' '${vars.%s_value}' > %s.done", id, id)}},
		}})
	}
	e := branchChildExecutor(t, root, workflow)
	e.state.Variables["left_value"], e.state.Variables["right_value"] = "left-outer", "right-outer"
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			branchChildWrite(t, root, "release", "go")
		}
		var wg sync.WaitGroup
		for i := range workflow.Steps {
			wg.Add(1)
			go func(step *Step) {
				defer wg.Done()
				result := e.executeBranch(context.Background(), step, workflow)
				want := StatusFailed
				if pass == 1 {
					want = StatusCompleted
				}
				if result.Status != want {
					t.Errorf("%s pass %d: %+v", step.ID, pass, result)
				}
			}(&workflow.Steps[i])
		}
		wg.Wait()
	}
	for _, id := range []string{"left", "right"} {
		branchChildRead(t, root, id+".once", "x")
		branchChildRead(t, root, id+".done", id)
		if e.state.Variables[id+"_value"] != id+"-outer" {
			t.Fatalf("%s outer alias changed", id)
		}
	}
	state, err := LoadState(root, cfg.RunID)
	if err != nil || len(state.Steps) < 6 {
		t.Fatalf("lost concurrent checkpoint: %+v %v", state, err)
	}
}

func TestBranchChildRecoveryCheckpointFailureStopsNextChild(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Settings.OnError = ErrorActionContinue
	workflow.Steps[0].Branches["a"] = []Step{
		{ID: "break", Command: "mv .ntm/pipelines saved; printf blocked > .ntm/pipelines"},
		{ID: "next", Command: "printf forbidden > forbidden"},
	}
	e := branchChildExecutor(t, root, workflow)
	result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || e.checkpointFailure() == nil {
		t.Fatalf("save failure did not stop branch: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("branch advanced after failed checkpoint")
	}
}

func TestBranchChildRecoveryExplicitStateResetStartsNewDecision(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	branchChildWrite(t, root, "choice", "a")
	e := branchChildExecutor(t, root, workflow)
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusFailed {
		t.Fatalf("first: %+v", result)
	}
	// The branch journal lives in Steps: replacing the complete execution
	// state removes it too. Native Reset/force-iter policy has separate tests.
	e = branchChildExecutor(t, root, workflow)
	branchChildWrite(t, root, "choice", "b")
	if result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow); result.Status != StatusCompleted {
		t.Fatalf("reset: %+v", result)
	}
	branchChildRead(t, root, "selections", "xx")
	branchChildRead(t, root, "wrong", "wrong")
}

func TestBranchChildRecoveryReceiptAndResultAreAtomic(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	branchChildWrite(t, root, "choice", "a")
	e := branchChildExecutor(t, root, workflow)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			snapshot := e.snapshotState()
			data, err := json.Marshal(snapshot)
			if err != nil {
				t.Error(err)
				return
			}
			var roundtrip ExecutionState
			if err := json.Unmarshal(data, &roundtrip); err != nil {
				t.Error(err)
				return
			}
			if result, exists := roundtrip.Steps["route_once"]; exists && result.Status == StatusCompleted {
				metadata, _ := roundtrip.Steps[branchDecisionID("route")].ParsedData.(map[string]interface{})
				children, _ := metadata["children"].(map[string]interface{})
				if children["route_once"] == nil {
					t.Error("snapshot paired settled child with no alias checkpoint")
					return
				}
			}
		}
	}()
	result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	close(stop)
	<-done
	if result.Status != StatusFailed {
		t.Fatalf("first: %+v", result)
	}
	metadata := e.state.Steps[branchDecisionID("route")].ParsedData.(map[string]interface{})
	if _, ok := metadata["children"]; !ok {
		t.Fatal("no completion metadata was published")
	}
}

func TestBranchChildRecoveryUnmarkedAggregateDoesNotGuess(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Steps[0].Branches["a"] = []Step{{ID: "fanout", OutputVar: "collected", Foreach: &ForeachConfig{Steps: []Step{{ID: "body", Command: "true"}}}}}
	e := branchChildExecutor(t, root, workflow)
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.state.Steps["route_fanout"] = StepResult{StepID: "route_fanout", Status: StatusCompleted, Output: "summary, not aggregate", StartedAt: now, FinishedAt: now}
	e.state.Variables["collected"] = []string{"unverified old shape"}
	before := e.state.Steps["route_fanout"]
	result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || result.Error == nil || !strings.Contains(result.Error.Message, "aggregate output checkpoint") {
		t.Fatalf("guessed aggregate: %+v", result)
	}
	if !reflect.DeepEqual(before, e.state.Steps["route_fanout"]) {
		t.Fatal("refusal changed the completed child's evidence")
	}
}

func TestBranchChildRecoveryRejectsAuthoredIdentityCollision(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	e := branchChildExecutor(t, root, workflow)
	// Capture the decision first so existing legacy-evidence refusal does not
	// mask the independently-authored identity collision being exercised.
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	workflow.Steps = append(workflow.Steps, Step{ID: "route_once", Command: "true"})
	e.graph = NewDependencyGraph(workflow)
	now := time.Now()
	e.state.Steps["route_once"] = StepResult{StepID: "route_once", Status: StatusCompleted, Output: "unrelated", StartedAt: now, FinishedAt: now}
	result := e.executeBranch(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || result.Error == nil || !strings.Contains(result.Error.Message, "collides") {
		t.Fatalf("adopted unrelated main-work result: %+v", result)
	}
	if e.state.Steps["route_once"].Output != "unrelated" {
		t.Fatal("overwrote unrelated result")
	}
	if _, err := os.Stat(filepath.Join(root, "once")); !os.IsNotExist(err) {
		t.Fatal("identity collision dispatched work")
	}
}

func TestBranchChildCheckpointSerializationFailureFencesExecution(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Steps[0].Branches["a"] = []Step{{ID: "fanout", OutputVar: "collected", Foreach: &ForeachConfig{Steps: []Step{{ID: "body", Command: "true"}}}}}
	e := branchChildExecutor(t, root, workflow)
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	path := pipelineStatePath(root, e.state.RunID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	child := workflow.Steps[0].Branches["a"].([]Step)[0]
	child.ID = "route_fanout"
	now := time.Now()
	result := StepResult{StepID: child.ID, Status: StatusCompleted, Output: "summary", StartedAt: now, FinishedAt: now}
	// A native aggregate can contain a value its producer failed to serialize.
	// Receipt preparation must fail before publishing a completed StepResult.
	e.state.Variables["collected"] = func() {}
	err = e.checkpointBranchChild(context.Background(), "route", &child, result)
	var unsupported *json.UnsupportedTypeError
	if !errors.Is(err, ErrCheckpointFailed) || !errors.As(err, &unsupported) {
		t.Fatalf("serialization failure lost its fatal checkpoint cause: %v", err)
	}
	if _, published := e.state.Steps[child.ID]; published {
		t.Fatal("failed receipt preparation published a completed child")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(before) != string(after) {
		t.Fatalf("serialization failure changed the durable checkpoint: %v", readErr)
	}
	delete(e.state.Variables, "collected")
	if !errors.Is(e.persistState(), ErrCheckpointFailed) {
		t.Fatal("repairing serialization silently resumed the failed attempt")
	}
	var next StepResult
	if !e.stopBeforeDispatch(context.Background(), &next) || next.Error == nil || next.Error.Type != "checkpoint" {
		t.Fatal("failed receipt did not fence later work")
	}
}

func TestBranchChildCheckpointWaitNonePreservesAliasesButNotSettlement(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Steps[0].Branches["a"] = []Step{{ID: "sidecar", Command: "sleep 1", Wait: WaitNone, OutputVar: "launch"}}
	e := branchChildExecutor(t, root, workflow)
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	child := workflow.Steps[0].Branches["a"].([]Step)[0]
	child.ID = "route_sidecar"
	now := time.Now()
	result := StepResult{StepID: child.ID, Status: StatusCompleted, Output: "Command started", StartedAt: now, FinishedAt: now}
	e.state.Variables["launch_parsed"] = "stale"
	if err := e.checkpointBranchChild(context.Background(), "route", &child, result); err != nil {
		t.Fatal(err)
	}
	if e.state.Variables["launch"] != result.Output {
		t.Fatal("fire-and-forget launch output was lost")
	}
	if _, exists := e.state.Variables["launch_parsed"]; exists {
		t.Fatal("launch without parsed data retained a stale alias")
	}
	decision, err := decodeBranchDecision(branchDecisionID("route"), e.state.Steps[branchDecisionID("route")])
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := decision.Children[child.ID]; exists {
		t.Fatal("fire-and-forget launch was recorded as settled work")
	}
	if _, adopted, err := e.adoptBranchChild(context.Background(), decision, &child, workflow); err != nil || adopted {
		t.Fatalf("branch bypassed the native command journal for live work: adopted=%v error=%v", adopted, err)
	}
}

func TestBranchChildCheckpointAggregateRoundTripRestoresOriginalShape(t *testing.T) {
	root := t.TempDir()
	_, workflow := branchChildFixture(root)
	workflow.Steps[0].Branch = "a"
	workflow.Steps[0].Branches["a"] = []Step{{ID: "fanout", OutputVar: "collected", Foreach: &ForeachConfig{Steps: []Step{{ID: "body", Command: "true"}}}}}
	e := branchChildExecutor(t, root, workflow)
	if _, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0]); err != nil {
		t.Fatal(err)
	}
	child := workflow.Steps[0].Branches["a"].([]Step)[0]
	child.ID = "route_fanout"
	now := time.Now()
	result := StepResult{StepID: child.ID, Status: StatusCompleted, Output: "Foreach completed: 2/2", StartedAt: now, FinishedAt: now}
	e.state.Variables["collected"] = []string{"alpha", "beta"}
	e.state.Variables["collected_parsed"] = map[string]interface{}{"count": 2}
	if err := e.checkpointBranchChild(context.Background(), "route", &child, result); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(root, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	e.state = prior
	e.state.Variables["collected"] = "reconstructed summary must not win"
	decision, _, err := e.prepareBranchDecision(context.Background(), &workflow.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, adopted, err := e.adoptBranchChild(context.Background(), decision, &child, workflow); err != nil || !adopted {
		t.Fatalf("could not adopt checkpointed aggregate: %v %v", adopted, err)
	}
	if !reflect.DeepEqual(e.state.Variables["collected"], []interface{}{"alpha", "beta"}) {
		t.Fatalf("aggregate shape replaced by summary: %#v", e.state.Variables["collected"])
	}
	parsed := e.state.Variables["collected_parsed"].(map[string]interface{})
	if parsed["count"] != float64(2) {
		t.Fatalf("lost aggregate parsed alias: %#v", parsed)
	}
	parsed["count"] = float64(99)
	if decision.Children[child.ID].Outputs["collected_parsed"].(map[string]interface{})["count"] != float64(2) {
		t.Fatal("restored aggregate aliases mutate the saved receipt")
	}
}
