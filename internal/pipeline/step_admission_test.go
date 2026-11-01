package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStepAdmissionBatchIsAtomicAndPhaseOwned(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.config.DryRun = true
	e.limits.MaxStepCountTotal = 3
	ctx := context.Background()
	if err := e.admitRuntimeIDs(ctx, []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	if err := e.admitRuntimeIDs(ctx, []string{"three", "four"}); !errors.Is(err, ErrStepCountLimit) {
		t.Fatalf("batch limit: %v", err)
	}
	if len(e.state.StepAdmissions.Work) != 2 || e.state.StepAdmissions.Work["three"] {
		t.Fatal("refused batch consumed partial capacity")
	}
	final := finalizationContext(ctx)
	if err := e.admitRuntimeIDs(final, []string{"cleanup", "release", "notify"}); err != nil {
		t.Fatalf("work exhaustion blocked independent finalization: %v", err)
	}
	if err := e.admitRuntimeIDs(final, []string{"one"}); err == nil {
		t.Fatal("cross-phase reuse was accepted")
	}
	e.stepAdmissionAttempt = &stepAdmissionAttempt{}
	e.limits.MaxStepCountTotal = 1
	if err := e.admitRuntimeIDs(ctx, []string{"one", "two"}); err != nil {
		t.Fatalf("existing over-limit identities were not reusable: %v", err)
	}
	if err := e.admitRuntimeIDs(ctx, []string{"new"}); !errors.Is(err, ErrStepCountLimit) {
		t.Fatalf("over-limit run admitted a new identity: %v", err)
	}
}

func TestForeachAdmissionCancellationPreservesRoundHistory(t *testing.T) {
	for _, boundary := range []string{"runtime_ids", "round_materialization"} {
		t.Run(boundary, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			const priorID = "expand_iter0_work_round1"
			const nextID = "expand_iter0_work_round2"
			prior := StepResult{StepID: priorID, Status: StatusCompleted, Output: "prior round"}
			e.state.Steps[priorID] = prior
			e.state.StepAdmissions = &StepAdmissionState{Version: 1, Work: map[string]bool{"expand": true, priorID: true}}
			e.state.ForeachState = map[string]ForeachIterationState{
				"expand": {StepID: "expand", CompletedRounds: map[string]int{"expand_iter0": 1}},
			}
			marker := filepath.Join(e.config.ProjectDir, "unexpected-body")
			parent := &Step{ID: "expand", Foreach: &ForeachConfig{MaxRounds: IntOrExpr{Value: 3}}}
			plan := foreachIterationPlan{Index: 0, Steps: []Step{{ID: "expand_iter0_work", Command: "printf dispatched > '" + marker + "'"}}}
			workflow := &Workflow{Name: e.state.WorkflowID}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var result foreachIterationResult
			if boundary == "runtime_ids" {
				cancel()
				result = e.executeForeachIteration(ctx, parent, workflow, plan, ErrorActionContinue)
				if e.runtimeIDAdmitted(nextID) {
					t.Fatal("cancelled admission reserved an unentered round")
				}
			} else {
				// Pause the real admission checkpoint after it has reserved the
				// next ID, then cancel before the round's copy lease is acquired.
				e.persistMu.Lock()
				done := make(chan foreachIterationResult, 1)
				go func() {
					done <- e.executeForeachIteration(ctx, parent, workflow, plan, ErrorActionContinue)
				}()
				deadline := time.Now().Add(5 * time.Second)
				for !e.runtimeIDAdmitted(nextID) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				admitted := e.runtimeIDAdmitted(nextID)
				cancel()
				e.persistMu.Unlock()
				select {
				case result = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled round did not return after its checkpoint")
				}
				if !admitted {
					t.Fatal("round did not reach its admission checkpoint")
				}
			}
			if result.SkipKind != SkipKindCancelled || !foreachIterationCancelled(result) || result.admissionFailure() != nil {
				t.Fatalf("pure admission cancellation cannot be normalized after a sibling break: %+v", result)
			}
			if len(result.Results) != 0 || len(e.state.Steps) != 1 || e.state.Steps[priorID].Output != prior.Output {
				t.Fatalf("cancelled boundary changed prior results or invented a child result: %+v", result)
			}
			if len(e.state.CommandExecutions) != 0 || len(e.state.InFlightSteps) != 0 {
				t.Fatal("cancelled round created launch or in-flight evidence")
			}
			if got := e.foreachIterationCompletedRounds("expand", "expand_iter0"); got != 1 {
				t.Fatalf("cancelled round advanced the completion watermark: %d", got)
			}
			if len(e.state.ForeachState["expand"].CompletedIterationIDs) != 0 {
				t.Fatal("cancelled round marked the iteration complete")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unentered round produced an effect: %v", err)
			}
		})
	}
}

func TestForeachCancellationCannotHideAdmissionFailure(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.config.DryRun = true
	e.limits.MaxStepCountTotal = 1
	if err := e.admitRuntimeIDs(context.Background(), []string{"used"}); err != nil {
		t.Fatal(err)
	}
	err := e.admitRuntimeIDs(context.Background(), []string{"denied"})
	if !errors.Is(err, ErrStepCountLimit) {
		t.Fatalf("expected a real capacity refusal: %v", err)
	}
	for _, nested := range []bool{false, true} {
		cause := &StepError{Type: "step_limit", Message: err.Error()}
		result := foreachIterationResult{
			Error:   err.Error(),
			Results: []StepResult{{StepID: "prior", Status: StatusCancelled}},
		}
		if nested {
			result.Results = append(result.Results, StepResult{Status: StatusFailed, Error: &StepError{Type: "foreach", Aggregated: []StepError{*cause}}})
		} else {
			result.admissionError = cause
		}
		if foreachIterationCancelled(result) || !result.failed() {
			t.Fatalf("prior child cancellation hid a later capacity refusal (nested=%t): %+v", nested, result)
		}
	}
}

func TestStepAdmissionLegacyImportExcludesSyntheticBookkeeping(t *testing.T) {
	e := newCommandTestExecutor(t)
	workflow := &Workflow{Name: e.state.WorkflowID, Steps: []Step{
		{ID: "batch", Loop: &LoopConfig{Items: "[0,1]", Steps: []Step{{ID: "loop_child", Command: "true"}}}},
		{ID: "fan", Parallel: ParallelSpec{Steps: []Step{{ID: "parallel_child", Command: "true"}}}},
	}}
	e.graph = NewDependencyGraph(workflow)
	for _, id := range []string{"batch", "fan", "loop_child", "parallel_child"} {
		e.state.Steps[id] = StepResult{StepID: id, Status: StatusSkipped, SkipKind: SkipKindStartFrom}
	}
	e.state.Steps["batch_iter0_loop_child"] = StepResult{StepID: "batch_iter0_loop_child", Status: StatusCompleted}
	e.state.InFlightSteps = map[string]InFlightStepState{
		"batch_iter1":            {StepID: "batch_iter1", Kind: StepKindLoop, Iteration: 1},
		"batch_iter1_loop_child": {StepID: "batch_iter1_loop_child", Kind: StepKindCommand, Iteration: -1},
	}
	// Native launch evidence still consumes capacity when a synthetic
	// result with the same identity would otherwise be excluded.
	digest, err := commandDefinitionHash(&workflow.Steps[1].Parallel.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	record := CommandExecutionState{Version: commandExecutionVersion, StepID: "parallel_child", StepHash: digest, StartedAt: time.Now(), Status: commandExecutionStarting}
	if err := validateCommandExecution(record.StepID, record); err != nil {
		t.Fatal(err)
	}
	e.state.CommandExecutions = map[string]CommandExecutionState{record.StepID: record}
	e.stateMu.Lock()
	err = e.seedStepAdmissionsLocked(workflow, e.graph)
	e.stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"batch", "fan", "batch_iter0_loop_child", "batch_iter1_loop_child", "parallel_child"}
	if len(e.state.StepAdmissions.Work) != len(want) || len(e.state.StepAdmissions.Finalization) != 0 {
		t.Fatalf("legacy import charged synthetic bookkeeping: %+v", e.state.StepAdmissions)
	}
	for _, id := range want {
		if !e.state.StepAdmissions.Work[id] {
			t.Fatalf("legacy import lost runtime evidence for %q", id)
		}
	}
	if e.state.CommandExecutions[record.StepID].Status != commandExecutionStarting {
		t.Fatal("capacity import changed unresolved execution evidence")
	}
}

func TestStepAdmissionWholeResetRetainsCapacityHistory(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.config.DryRun = true
	e.limits.MaxStepCountTotal = 2
	workflow := &Workflow{Name: e.state.WorkflowID, Steps: []Step{{ID: "once", Command: "true"}}}
	e.graph = NewDependencyGraph(workflow)
	e.state.StepAdmissions = &StepAdmissionState{Version: 1, Work: map[string]bool{"historic": true}}
	e.state.Steps["once"] = StepResult{StepID: "once", Status: StatusCompleted, Output: "prior"}
	if err := e.applyResumeOptions(workflow, ResumeOptions{Reset: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Steps) != 0 {
		t.Fatal("explicit reset retained executable results")
	}
	if len(e.state.StepAdmissions.Work) != 2 || !e.state.StepAdmissions.Work["historic"] || !e.state.StepAdmissions.Work["once"] {
		t.Fatalf("reset refunded history or discarded legacy evidence before import: %+v", e.state.StepAdmissions)
	}
	if err := e.admitRuntimeIDs(context.Background(), []string{"once", "historic"}); err != nil {
		t.Fatalf("reset charged existing identities again: %v", err)
	}
	if err := e.admitRuntimeIDs(context.Background(), []string{"new"}); !errors.Is(err, ErrStepCountLimit) {
		t.Fatalf("reset made room for an additional identity: %v", err)
	}
}

func TestStepAdmissionNilGraphPreflightPreservesFinalizationEvidence(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "preflight-finalization",
		Steps: []Step{{ID: "main", Command: "exit 7"}},
		PostPipelineSteps: []Step{{ID: "tail", Command: "printf tail >> tail-effects", OnSuccess: []Step{
			{ID: "child", Command: "printf child >> child-effects"},
		}}},
	}
	cfg := DefaultExecutorConfig("preflight-finalization")
	cfg.ProjectDir, cfg.RunID = root, "preflight-finalization-run"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("fixture did not leave failed main work and completed finalization: %+v %v", state, err)
	}
	receipts, err := json.Marshal(state.CommandExecutions)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := json.Marshal(state.StepAdmissions)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := os.ReadFile(pipelineStatePath(root, state.RunID))
	if err != nil {
		t.Fatal(err)
	}
	checker := NewExecutor(cfg) // Detached preflight has not built an executor graph.
	checker.state = state
	if err := checker.applyResumeOptions(workflow, ResumeOptions{}); err != nil {
		t.Fatalf("nil-graph preflight reclassified completed finalization: %v", err)
	}
	afterReceipts, err := json.Marshal(checker.state.CommandExecutions)
	if err != nil || !bytes.Equal(receipts, afterReceipts) {
		t.Fatal("preflight changed native command or success-hook receipts")
	}
	afterLedger, err := json.Marshal(checker.state.StepAdmissions)
	if err != nil || !bytes.Equal(ledger, afterLedger) {
		t.Fatalf("preflight changed admission phase ownership: %+v", checker.state.StepAdmissions)
	}
	for _, id := range []string{"tail", "tail_on_success_child"} {
		if !checker.state.StepAdmissions.Finalization[id] || checker.state.StepAdmissions.Work[id] || checker.state.CommandExecutions[id].Status != commandExecutionSettled {
			t.Fatalf("post-pipeline evidence lost its finalization owner: %s", id)
		}
	}
	if after, err := os.ReadFile(pipelineStatePath(root, state.RunID)); err != nil || !bytes.Equal(checkpoint, after) {
		t.Fatalf("preflight rewrote the checkpoint: %v", err)
	}
}

func TestStepAdmissionBackgroundPreflightHonorsMaterializationOverride(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "preflight-definition-limit", Steps: []Step{
		{ID: "first", Command: "true"}, {ID: "second", Command: "true"},
	}}
	workflow.Settings.Limits.MaxMaterializedStepNodes = 1
	_, path, err := SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	prior := &ExecutionState{RunID: "preflight-definition-run", WorkflowID: workflow.Name, WorkflowFile: path,
		Session: "preflight-definition", Status: StatusFailed, StartedAt: time.Now(),
		Steps: map[string]StepResult{}, Variables: map[string]interface{}{}, CommandEvaluationVersion: commandEvaluationVersion,
	}
	if err := SaveState(root, prior); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := os.ReadFile(pipelineStatePath(root, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	req := backgroundRequest{Resume: true, Config: backgroundExecutorConfig{RunID: prior.RunID, ProjectDir: root, Session: prior.Session}}
	if _, _, _, err := prepareBackgroundResume(owner.Context(), root, req); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("detached preflight ignored the frozen structural cap: %v", err)
	}
	req.Config.ResumeOptions.MaxMaterializedStepNodes = 2
	frozen, _, cfg, err := prepareBackgroundResume(owner.Context(), root, req)
	if err != nil || frozen == nil || frozen.Settings.Limits.MaxMaterializedStepNodes != 1 || cfg.ResumeOptions.MaxMaterializedStepNodes != 2 {
		t.Fatalf("detached preflight failed to apply an attempt-only override: workflow=%+v config=%+v err=%v", frozen, cfg, err)
	}
	if after, err := os.ReadFile(pipelineStatePath(root, prior.RunID)); err != nil || !bytes.Equal(checkpoint, after) {
		t.Fatalf("capacity preflight mutated the checkpoint: %v", err)
	}
}

func TestStepAdmissionPureControlRequiresCapacity(t *testing.T) {
	for _, limit := range []int{1, 2} {
		name := "refused"
		if limit == 2 {
			name = "exact_capacity"
		}
		t.Run(name, func(t *testing.T) {
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bounded-control", Steps: []Step{{
				ID: "outer", Loop: &LoopConfig{Times: 3, Steps: []Step{{ID: "stop", LoopControl: LoopControlBreak}}},
			}}}
			workflow.Settings.Limits.MaxStepCountTotal = limit
			cfg := DefaultExecutorConfig("bounded-control")
			cfg.DryRun, cfg.ProjectDir = true, t.TempDir()
			state, err := NewExecutor(cfg).Run(context.Background(), workflow, nil, nil)
			if limit == 1 {
				if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
					t.Fatalf("control directive bypassed admission: %+v %v", state, err)
				}
			} else if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("admitted control directive failed to break: %+v %v", state, err)
			}
			if len(state.StepAdmissions.Work) != limit || !state.StepAdmissions.Work["outer"] || state.StepAdmissions.Work["outer_iter0_stop"] != (limit == 2) {
				t.Fatalf("incorrect pure-control accounting: %+v", state.StepAdmissions)
			}
			if len(state.Steps) != 1 || len(state.CommandExecutions) != 0 || len(state.InFlightSteps) != 0 {
				t.Fatalf("pure control created a body result or launch evidence: %+v", state)
			}
		})
	}
}

func TestStepAdmissionPreCancelledAttemptReservesLaterCleanupRoot(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "run"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "pre-cancelled-admission",
				Settings: WorkflowSettings{Limits: LimitsConfig{MaxStepCountTotal: 3}, OnCancel: []Step{
					{ID: "expand_cleanup", Loop: &LoopConfig{Items: "[0,1,2]", Steps: []Step{
						{ID: "cleanup_leaf", Command: "printf '%s\\n' '${loop.index}' >> expanded-cleanup"},
					}}},
					{ID: "release", Command: "printf released > release-effects"},
				}},
				Steps: []Step{{ID: "never", Command: "printf forbidden > main-effects"}},
			}
			frozen, path, err := SnapshotWorkflow(context.Background(), root, workflow)
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultExecutorConfig("pre-cancelled-admission")
			cfg.ProjectDir, cfg.RunID, cfg.WorkflowFile = root, "pre-cancelled-admission-run", path
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var state *ExecutionState
			if resume {
				prior := &ExecutionState{RunID: cfg.RunID, WorkflowID: frozen.Name, WorkflowFile: path,
					Session: cfg.Session, Status: StatusFailed, StartedAt: time.Now(),
					Steps: map[string]StepResult{}, Variables: map[string]interface{}{},
				}
				state, err = NewExecutor(cfg).Resume(ctx, frozen, prior, nil)
			} else {
				state, err = NewExecutor(cfg).Run(ctx, frozen, nil, nil)
			}
			if !errors.Is(err, context.Canceled) || state == nil || state.Status != StatusCancelled {
				t.Fatalf("pre-cancellation changed outcome: %+v %v", state, err)
			}
			if state.StepAdmissions == nil || len(state.StepAdmissions.Work) != 0 || len(state.StepAdmissions.Finalization) != 3 {
				t.Fatalf("pre-cancelled capacity accounting: %+v", state.StepAdmissions)
			}
			for _, id := range []string{"expand_cleanup", "expand_cleanup_iter0_cleanup_leaf", "release"} {
				if !state.StepAdmissions.Finalization[id] {
					t.Fatalf("pre-cancelled cleanup omitted reserved identity %q", id)
				}
			}
			if state.Steps["expand_cleanup"].Status != StatusFailed || state.Steps["release"].Status != StatusCompleted || state.CommandExecutions["release"].Status != commandExecutionSettled {
				t.Fatalf("dynamic cleanup consumed the later release root: steps=%+v receipts=%+v", state.Steps, state.CommandExecutions)
			}
			for file, want := range map[string]string{"expanded-cleanup": "0\n", "release-effects": "released"} {
				if data, err := os.ReadFile(filepath.Join(root, file)); err != nil || string(data) != want {
					t.Fatalf("%s = %q, want %q: %v", file, data, want, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "main-effects")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-cancelled attempt launched main work: %v", err)
			}
		})
	}
}

func TestStepMaterializationLeaseIsBoundedAndAttemptOwned(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.limits.MaxMaterializedStepNodes = 3
	first, err := e.acquireStepMaterialization(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.acquireStepMaterialization(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireStepMaterialization(context.Background(), 1); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("nested allocation exceeded quota: %v", err)
	}
	first()
	first()
	if e.stepAdmissionAttempt.materialized != 1 {
		t.Fatal("idempotent release refunded another lease")
	}
	e.stepAdmissionAttempt = &stepAdmissionAttempt{}
	second()
	if e.stepAdmissionAttempt.materialized != 0 {
		t.Fatal("old lease decremented a new attempt")
	}
	if _, err := e.acquireDefinitionCopies(context.Background(), []Step{{ID: "leaf"}}, int(^uint(0)>>1)); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("huge multiplication was not rejected: %v", err)
	}
}

func TestStepDefinitionWalkCountsNestedAndRawBranches(t *testing.T) {
	rawForeach := map[string]interface{}{"id": "batch", "parallel": true, "foreach": map[string]interface{}{
		"items": "[1]", "steps": []interface{}{map[string]interface{}{"id": "work", "command": "exit 0"}},
	}}
	if count, err := definitionNodeCount(rawForeach, 2); err != nil || count != 2 {
		t.Fatalf("parallel foreach flag counted as a child: %d %v", count, err)
	}
	upper := map[string]interface{}{"ID": "outer", "Parallel": []interface{}{
		map[string]interface{}{"ID": "inner", "On_Success": []Step{{ID: "hook"}}},
	}}
	if _, err := definitionNodeCount(upper, 2); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("case-insensitive JSON fields escaped the limit: %v", err)
	}
	if count, err := definitionNodeCount(upper, 3); err != nil || count != 3 {
		t.Fatalf("case-insensitive raw shape = %d, %v", count, err)
	}
	typedBranches := map[string]interface{}{"id": "choose", "Branches": map[string][]Step{
		"selected": {{ID: "child", Foreach: &ForeachConfig{Steps: []Step{{ID: "leaf"}}}}},
	}}
	if count, err := definitionNodeCount(typedBranches, 3); err != nil || count != 3 {
		t.Fatalf("typed branch collection = %d, %v", count, err)
	}
	body := []Step{{ID: "outer", Parallel: ParallelSpec{Steps: []Step{{ID: "parallel", OnSuccess: []Step{{ID: "hook"}}}}},
		Branches: map[string]interface{}{"selected": []map[string]interface{}{{"id": "branch", "command": "exit 0"}}}}}
	if count, err := definitionNodeCount(body, 4); err != nil || count != 4 {
		t.Fatalf("literal tree = %d, %v", count, err)
	}
	if _, err := definitionNodeCount(body, 3); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("nested shape escaped limit: %v", err)
	}
	cyclic := &Step{ID: "cycle"}
	cyclic.Foreach = &ForeachConfig{Steps: []Step{*cyclic}}
	cyclic.Foreach.Steps[0].Foreach = cyclic.Foreach
	if _, err := definitionNodeCount(cyclic, 4); !errors.Is(err, ErrMaterializedStepLimit) {
		t.Fatalf("cyclic literal structure did not stop at the bound: %v", err)
	}
}

func TestRawBranchParallelForeachHonorsMaterializationLimit(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "raw-parallel-foreach", Steps: []Step{{
		ID: "choose", Branch: "selected", Branches: map[string]interface{}{"selected": map[string]interface{}{
			"id": "batch", "parallel": true, "foreach": map[string]interface{}{
				"items": "[1]", "steps": []interface{}{map[string]interface{}{"id": "work", "command": "printf x >> marker"}},
			},
		}},
	}}}
	workflow.Settings.Limits.MaxStepCountTotal = 4
	workflow.Settings.Limits.MaxMaterializedStepNodes = 8
	cfg := DefaultExecutorConfig("raw-parallel-foreach")
	cfg.ProjectDir, cfg.RunID = root, "raw-parallel-foreach-run"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("valid raw parallel foreach failed: %+v %v", state, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(data) != "x" {
		t.Fatalf("branch body did not execute exactly once: %q %v", data, err)
	}
}

func TestAdmissionDeniedCannotEraseExistingExecutionEvidence(t *testing.T) {
	for _, kind := range []string{"command", "selector"} {
		t.Run(kind, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			step := Step{ID: "original", Command: "exit 0"}
			if kind == "selector" {
				step.Command, step.Branch = "", "$(printf selected)"
			}
			digest, err := commandDefinitionHash(&step)
			if err != nil {
				t.Fatal(err)
			}
			e.state.CommandEvaluationVersion = commandEvaluationVersion
			e.state.Steps[step.ID] = StepResult{StepID: step.ID, Status: StatusFailed, SkipKind: SkipKindAdmissionDenied}
			if kind == "command" {
				record := CommandExecutionState{Version: commandExecutionVersion, StepID: step.ID, StepHash: digest, StartedAt: time.Now(), Status: commandExecutionStarting}
				if err := validateCommandExecution(step.ID, record); err != nil {
					t.Fatal(err)
				}
				e.state.CommandExecutions = map[string]CommandExecutionState{step.ID: record}
			} else {
				record := CommandEvaluationState{Version: commandEvaluationVersion, StepID: step.ID, StepHash: digest, InvocationHash: strings.Repeat("a", 64), StdoutLimit: 4, StderrLimit: 4, StartedAt: time.Now(), Status: commandExecutionStarting}
				if err := validateCommandEvaluation(step.ID, record); err != nil {
					t.Fatal(err)
				}
				e.state.CommandEvaluations = map[string]CommandEvaluationState{step.ID: record}
			}
			workflow := &Workflow{Name: e.state.WorkflowID, Steps: []Step{step}}
			e.graph = NewDependencyGraph(workflow)
			if err := e.applyResumeOptions(workflow, ResumeOptions{MaxStepCountTotal: 1000, MaxMaterializedStepNodes: 1000}); err == nil {
				t.Fatal("admission marker authorized replay over a durable launch receipt")
			}
		})
	}
}

func TestStepAdmissionDeniedSuccessHookResumesWithoutRepeatingParent(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "admission-hook", Steps: []Step{{
		ID: "once", Command: "printf x >> original; printf ready",
		OnSuccess: []Step{{ID: "hook", Command: "printf hook >> hooks"}},
	}}}
	workflow.Settings.Limits.MaxStepCountTotal = 1
	cfg := DefaultExecutorConfig("admission-hook")
	cfg.ProjectDir, cfg.RunID = root, "admission-hook-run"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed || state.Steps["once"].Output != "ready" {
		t.Fatalf("hook refusal lost completed parent: %+v %v", state, err)
	}
	if state.Steps["once"].SkipKind == SkipKindAdmissionDenied {
		t.Fatal("completed parent was mislabeled as never dispatched")
	}
	receipt, err := json.Marshal(state.CommandExecutions["once"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "hooks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied hook ran: %v", err)
	}
	prior, err := LoadState(root, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("frozen workflow: %v %+v", err, validation)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	owner, err := AcquireRunControl(ctx, root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	resumed, err := NewExecutor(cfg).ResumeWithOptions(owner.Context(), frozen, prior, ResumeOptions{MaxStepCountTotal: 2}, nil)
	if err != nil || resumed.Status != StatusCompleted {
		t.Fatalf("raised capacity did not finish hook: %+v %v", resumed, err)
	}
	after, err := json.Marshal(resumed.CommandExecutions["once"])
	if err != nil || !bytes.Equal(receipt, after) {
		t.Fatal("resume replaced the original settled command receipt")
	}
	for name, want := range map[string]string{"original": "x", "hooks": "hook"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
}
