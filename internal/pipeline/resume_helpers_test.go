package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testAgentDelivery(stepID, status string) AgentDeliveryState {
	started := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	record := AgentDeliveryState{
		Version: agentDeliveryVersion, StepID: stepID, Kind: StepKindPrompt,
		Session: "resume-session", Endpoint: "local", PaneID: "%17", PanePID: 2718, AgentType: "claude",
		StepHash: strings.Repeat("a", 64), PromptHash: strings.Repeat("b", 64),
		PromptEchoHash: strings.Repeat("c", 64),
		Status:         status, BeforeOutput: "previous task", StartedAt: started,
	}
	if status == agentDeliveryDelivered || status == agentDeliveryCompleted {
		record.DeliveredAt = started.Add(time.Second)
	}
	if status == agentDeliveryCompleted {
		record.CompletedAt = started.Add(2 * time.Second)
		record.Output = "current result"
	}
	return record
}

func TestAgentDeliveryJournalPersistsAndReturnsCopies(t *testing.T) {
	cfg := DefaultExecutorConfig("resume-session")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID: "agent-delivery-roundtrip", WorkflowID: "delivery-workflow", Session: cfg.Session,
		Status: StatusRunning, Steps: map[string]StepResult{}, Variables: map[string]interface{}{},
	}
	for _, status := range []string{agentDeliverySending, agentDeliveryDelivered, agentDeliveryCompleted} {
		record := testAgentDelivery("review", status)
		if err := executor.saveAgentDelivery(record); err != nil {
			t.Fatalf("save %s: %v", status, err)
		}
		persisted, err := LoadState(cfg.ProjectDir, executor.state.RunID)
		if err != nil {
			t.Fatalf("load %s: %v", status, err)
		}
		if got := persisted.AgentDeliveries["review"]; !reflect.DeepEqual(got, record) {
			t.Fatalf("persisted %s receipt = %#v, want %#v", status, got, record)
		}
		loaded, ok := executor.loadAgentDelivery("review")
		if !ok {
			t.Fatal("saved delivery missing")
		}
		loaded.Status = "mutated"
		loaded.Output = "mutated"
		again, _ := executor.loadAgentDelivery("review")
		if !reflect.DeepEqual(again, record) {
			t.Fatalf("caller mutated journal through load: %#v", again)
		}
	}
	if _, ok := executor.loadAgentDelivery("unknown"); ok {
		t.Fatal("unknown step has a delivery")
	}

	// A failed durable write must propagate to the dispatch caller. Keeping the
	// intent in memory still prevents another attempt from treating it as new.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	executor.config.ProjectDir = blocked
	if err := executor.saveAgentDelivery(testAgentDelivery("next", agentDeliverySending)); !errors.Is(err, ErrCheckpointFailed) {
		t.Fatalf("failed save = %v, want ErrCheckpointFailed", err)
	}
	if _, ok := executor.loadAgentDelivery("next"); !ok {
		t.Fatal("failed persistence erased in-memory dispatch intent")
	}
}

func TestAgentStepHashUsesCompleteStableDefinition(t *testing.T) {
	step := Step{ID: "review", Template: "review", Params: map[string]interface{}{"a": "first", "b": "second"}}
	hash, err := agentStepHash(&step)
	if err != nil {
		t.Fatal(err)
	}
	copy := cloneStep(step)
	copy.Params = map[string]interface{}{"b": "second", "a": "first"}
	got, err := agentStepHash(&copy)
	if err != nil || got != hash {
		t.Fatalf("equivalent definition hash = %q, %v, want %q", got, err, hash)
	}
	copy.Params["a"] = "changed"
	got, err = agentStepHash(&copy)
	if err != nil || got == hash {
		t.Fatalf("changed definition hash = %q, %v, original %q", got, err, hash)
	}
	copy.Params["unsupported"] = make(chan int)
	if _, err := agentStepHash(&copy); err == nil {
		t.Fatal("non-serializable definition was accepted")
	}
	if _, err := agentStepHash(nil); err == nil {
		t.Fatal("nil definition was accepted")
	}
}

func TestResumeRejectsInvalidAgentDeliveriesBeforeReset(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AgentDeliveryState)
	}{
		{"unknown status", func(r *AgentDeliveryState) { r.Status = "maybe-sent" }},
		{"unsupported version", func(r *AgentDeliveryState) { r.Version++ }},
		{"different step", func(r *AgentDeliveryState) { r.StepID = "other" }},
		{"different session", func(r *AgentDeliveryState) { r.Session = "other" }},
		{"missing endpoint", func(r *AgentDeliveryState) { r.Endpoint = "" }},
		{"empty remote endpoint", func(r *AgentDeliveryState) { r.Endpoint = "ssh: " }},
		{"unsupported endpoint", func(r *AgentDeliveryState) { r.Endpoint = "socket:other" }},
		{"command kind", func(r *AgentDeliveryState) { r.Kind = StepKindCommand }},
		{"logical pane", func(r *AgentDeliveryState) { r.PaneID = "resume-session:0.1" }},
		{"missing PID", func(r *AgentDeliveryState) { r.PanePID = 0 }},
		{"missing agent", func(r *AgentDeliveryState) { r.AgentType = "" }},
		{"invalid step hash", func(r *AgentDeliveryState) { r.StepHash = "broken" }},
		{"invalid prompt hash", func(r *AgentDeliveryState) { r.PromptHash = strings.Repeat("z", 64) }},
		{"oversized baseline", func(r *AgentDeliveryState) { r.BeforeOutput = strings.Repeat("x", agentDeliveryMaxCaptureBytes+1) }},
		{"oversized result", func(r *AgentDeliveryState) { r.Output = strings.Repeat("x", agentDeliveryMaxOutputBytes+1) }},
		{"missing start", func(r *AgentDeliveryState) { r.StartedAt = time.Time{} }},
		{"missing delivery", func(r *AgentDeliveryState) { r.DeliveredAt = time.Time{} }},
		{"completion before delivery", func(r *AgentDeliveryState) { r.CompletedAt = r.StartedAt }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, opts := range []ResumeOptions{{Reset: true}, {Mode: ResumeModeRestartFailed}, {Mode: ResumeModeForceIter, StepID: "fanout", Iteration: 0}} {
				executor := NewExecutor(DefaultExecutorConfig("resume-session"))
				record := testAgentDelivery("fanout_iter0_work", agentDeliveryCompleted)
				tc.mutate(&record)
				executor.state = &ExecutionState{
					Session: "resume-session", Steps: map[string]StepResult{record.StepID: {Status: StatusFailed}},
					AgentDeliveries: map[string]AgentDeliveryState{"fanout_iter0_work": record},
				}
				err := executor.applyResumeOptions(nil, opts)
				if err == nil || !strings.Contains(err.Error(), "invalid agent delivery") {
					t.Fatalf("opts %#v: invalid journal accepted: %v", opts, err)
				}
				if tc.name == "unknown status" && !strings.Contains(err.Error(), "unknown delivery status") {
					t.Fatalf("unknown status diagnosis = %v", err)
				}
				if got := executor.state.AgentDeliveries["fanout_iter0_work"]; !reflect.DeepEqual(got, record) {
					t.Fatalf("opts %#v discarded invalid dispatch evidence", opts)
				}
				if len(executor.state.Steps) != 1 || executor.state.ForeachState != nil {
					t.Fatalf("opts %#v mutated state before validation", opts)
				}
			}
		})
	}
}

func TestResumeAgentDeliveryPolicies(t *testing.T) {
	for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
		t.Run(string(mode), func(t *testing.T) {
			executor := NewExecutor(DefaultExecutorConfig("resume-session"))
			executor.state = &ExecutionState{
				Session: "resume-session", Steps: map[string]StepResult{},
				InFlightSteps:   map[string]InFlightStepState{"inflight": {StepID: "inflight", Kind: StepKindPrompt}},
				AgentDeliveries: map[string]AgentDeliveryState{},
			}
			for _, status := range []ExecutionStatus{StatusCompleted, StatusFailed, StatusCancelled, StatusRunning, StatusPending} {
				id := string(status)
				executor.state.Steps[id] = StepResult{StepID: id, Status: status}
				executor.state.AgentDeliveries[id] = testAgentDelivery(id, agentDeliveryDelivered)
			}
			executor.state.AgentDeliveries["inflight"] = testAgentDelivery("inflight", agentDeliverySending)
			executor.state.AgentDeliveries["receipt-only"] = testAgentDelivery("receipt-only", agentDeliverySending)
			executor.state.AgentDeliveries["completed-receipt"] = testAgentDelivery("completed-receipt", agentDeliveryCompleted)
			executor.state.Steps["completed-receipt"] = StepResult{Status: StatusRunning}
			executor.state.InFlightSteps["completed-receipt"] = InFlightStepState{StepID: "completed-receipt", Kind: StepKindPrompt}
			if err := executor.applyResumeOptions(nil, ResumeOptions{Mode: mode}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"completed", "failed", "cancelled", "running", "pending", "inflight", "receipt-only", "completed-receipt"} {
				_, exists := executor.loadAgentDelivery(id)
				want := mode == ResumeModeContinue || id == "completed" || id == "receipt-only" || id == "completed-receipt"
				if exists != want {
					t.Fatalf("receipt %q present = %t, want %t", id, exists, want)
				}
			}
		})
	}
}

func TestResumeAgentDeliveryCannotMoveSessionsWithoutReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		executor := NewExecutor(DefaultExecutorConfig("new-session"))
		record := testAgentDelivery("review", agentDeliveryDelivered)
		executor.state = &ExecutionState{
			Session: "resume-session", Steps: map[string]StepResult{"review": {Status: StatusRunning}},
			AgentDeliveries: map[string]AgentDeliveryState{"review": record},
		}
		err := executor.applyResumeOptions(nil, ResumeOptions{Reset: reset, OnRosterChange: ResumeRosterProceed})
		if reset {
			if err != nil || executor.state.Session != "new-session" || len(executor.state.AgentDeliveries) != 0 {
				t.Fatalf("explicit reset failed to migrate: session=%q receipts=%#v err=%v", executor.state.Session, executor.state.AgentDeliveries, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "explicit reset") {
				t.Fatalf("preserved delivery moved to another session: %v", err)
			}
			if executor.state.Session != "resume-session" || executor.state.AgentDeliveries["review"] != record {
				t.Fatal("refused session migration mutated delivery identity")
			}
		}
	}
}

func TestResumeLegacyAgentLeafRequiresExplicitRestart(t *testing.T) {
	workflow := &Workflow{Name: "legacy", Steps: []Step{
		{ID: "prompt", Prompt: "review"},
		{ID: "template", Template: "review"},
		{ID: "command", Command: "echo allowed"},
		{ID: "group", Parallel: ParallelSpec{Steps: []Step{{ID: "nested", Prompt: "review nested"}}}},
	}}
	for _, stepID := range []string{"prompt", "template", "group_nested", "command"} {
		t.Run(stepID, func(t *testing.T) {
			for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
				executor := NewExecutor(DefaultExecutorConfig("resume-session"))
				executor.graph = NewDependencyGraph(workflow)
				executor.state = &ExecutionState{
					Session: "resume-session", Steps: map[string]StepResult{stepID: {StepID: stepID, Status: StatusRunning}},
				}
				err := executor.applyResumeOptions(workflow, ResumeOptions{Mode: mode})
				wantErr := mode == ResumeModeContinue && stepID != "command"
				if (err != nil) != wantErr {
					t.Fatalf("mode %s error = %v, want error %t", mode, err, wantErr)
				}
				if wantErr && !strings.Contains(err.Error(), "no durable delivery evidence") {
					t.Fatalf("missing recovery diagnosis: %v", err)
				}
			}
		})
	}
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		Session:       "resume-session",
		InFlightSteps: map[string]InFlightStepState{"group_nested": {StepID: "group_nested", Kind: StepKindPrompt}},
	}
	if err := executor.applyResumeOptions(workflow, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "no durable delivery evidence") {
		t.Fatalf("legacy in-flight leaf without a StepResult was replayable: %v", err)
	}
}

func TestResumeResetClearsDurableWorkStateAndStepVariables(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:       "run-reset",
		WorkflowID:  "resume-reset",
		Status:      StatusRunning,
		CurrentStep: "fanout",
		Steps: map[string]StepResult{
			"top": {StepID: "top", Status: StatusCompleted, Output: "old"},
		},
		Variables: map[string]interface{}{
			"input":                "keep",
			"top_out":              "drop",
			"top_out_parsed":       map[string]interface{}{"drop": true},
			"parallel_out":         "drop",
			"loop_out":             "drop",
			"foreach_out":          "drop",
			"foreach_pane_out":     "drop",
			"steps.top.output":     "drop",
			"steps.top.parsed.foo": "drop",
		},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {StepID: "fanout", CurrentIteration: 2, Total: 4},
		},
		ParallelState: map[string]ParallelGroupState{
			"group": {StepID: "group", Total: 2, InFlightStepIDs: []string{"child"}},
		},
		ScopeStack:      []ScopeFrame{{Kind: StepKindLoop, Name: "item"}},
		InFlightSteps:   map[string]InFlightStepState{"fanout": {StepID: "fanout", Kind: "foreach"}},
		AgentDeliveries: map[string]AgentDeliveryState{"top": testAgentDelivery("top", agentDeliveryCompleted)},
		Errors:          []ExecutionError{{StepID: "top", Message: "old failure"}},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-reset",
		Steps: []Step{{
			ID:        "top",
			OutputVar: "top_out",
			Parallel:  ParallelSpec{Steps: []Step{{ID: "parallel-child", OutputVar: "parallel_out"}}},
			Loop: &LoopConfig{
				Steps: []Step{{ID: "loop-child", OutputVar: "loop_out"}},
			},
			Foreach:     &ForeachConfig{Steps: []Step{{ID: "foreach-child", OutputVar: "foreach_out"}}},
			ForeachPane: &ForeachConfig{Steps: []Step{{ID: "foreach-pane-child", OutputVar: "foreach_pane_out"}}},
		}},
	}

	executor.resetResumeState(workflow)

	if executor.state.CurrentStep != "" {
		t.Fatalf("CurrentStep = %q, want empty", executor.state.CurrentStep)
	}
	if len(executor.state.Steps) != 0 {
		t.Fatalf("Steps = %#v, want empty map", executor.state.Steps)
	}
	if executor.state.AgentDeliveries != nil {
		t.Fatalf("agent deliveries survived reset: %#v", executor.state.AgentDeliveries)
	}
	if executor.state.ForeachState != nil || executor.state.ParallelState != nil || executor.state.ScopeStack != nil || executor.state.InFlightSteps != nil || executor.state.Errors != nil {
		t.Fatalf("resume bookkeeping not cleared: foreach=%#v parallel=%#v scopes=%#v in_flight=%#v errors=%#v",
			executor.state.ForeachState, executor.state.ParallelState, executor.state.ScopeStack, executor.state.InFlightSteps, executor.state.Errors)
	}
	if got := executor.state.Variables["input"]; got != "keep" {
		t.Fatalf("input variable = %#v, want preserved", got)
	}
	for _, key := range []string{
		"top_out", "top_out_parsed", "parallel_out", "loop_out",
		"foreach_out", "foreach_pane_out", "steps.top.output", "steps.top.parsed.foo",
	} {
		if _, ok := executor.state.Variables[key]; ok {
			t.Fatalf("variable %q survived reset: %#v", key, executor.state.Variables)
		}
	}
}

func TestForceResumeIterationPrunesFutureIterationState(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-force",
		WorkflowID: "resume-force",
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"fanout_iter0":      {StepID: "fanout_iter0", Status: StatusCompleted},
			"fanout_iter1":      {StepID: "fanout_iter1", Status: StatusCompleted},
			"fanout_iter2":      {StepID: "fanout_iter2", Status: StatusCompleted},
			"fanout_iter3_work": {StepID: "fanout_iter3_work", Status: StatusCompleted},
			"other_iter9":       {StepID: "other_iter9", Status: StatusCompleted},
		},
		AgentDeliveries: map[string]AgentDeliveryState{
			"fanout_iter0":      testAgentDelivery("fanout_iter0", agentDeliveryCompleted),
			"fanout_iter2":      testAgentDelivery("fanout_iter2", agentDeliveryCompleted),
			"fanout_iter4_work": testAgentDelivery("fanout_iter4_work", agentDeliverySending),
			"other_iter9":       testAgentDelivery("other_iter9", agentDeliveryDelivered),
		},
		// bd-a3fwf: seed flat substitution keys for both pre-pivot iterations
		// (must survive) and post-pivot iterations (must be scrubbed).
		Variables: map[string]interface{}{
			"steps.fanout_iter0.output":      "keep-prev",
			"steps.fanout_iter1.output":      "keep-prev",
			"steps.fanout_iter2.output":      "ghost",
			"steps.fanout_iter2.data":        map[string]interface{}{"v": 2},
			"steps.fanout_iter3_work.output": "ghost",
			"steps.fanout_iter3_work.data":   "ghost-data",
			"steps.other_iter9.output":       "unrelated-step-keep",
		},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:                "fanout",
				CurrentIteration:      4,
				Total:                 5,
				CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3", "other_iter0", "fanout_iterx"},
			},
		},
	}

	executor.forceResumeIteration("fanout", 2)
	for _, id := range []string{"fanout_iter0", "fanout_iter2", "fanout_iter4_work", "other_iter9"} {
		_, exists := executor.loadAgentDelivery(id)
		want := id == "fanout_iter0" || id == "other_iter9"
		if exists != want {
			t.Fatalf("receipt %q present = %t after force iteration, want %t", id, exists, want)
		}
	}

	state := executor.state.ForeachState["fanout"]
	if state.CurrentIteration != 2 {
		t.Fatalf("CurrentIteration = %d, want 2", state.CurrentIteration)
	}
	wantCompleted := []string{"fanout_iter0", "fanout_iter1"}
	if !reflect.DeepEqual(state.CompletedIterationIDs, wantCompleted) {
		t.Fatalf("CompletedIterationIDs = %#v, want %#v", state.CompletedIterationIDs, wantCompleted)
	}
	for _, removed := range []string{"fanout_iter2", "fanout_iter3_work"} {
		if _, ok := executor.state.Steps[removed]; ok {
			t.Fatalf("future step %q survived force iteration: %#v", removed, executor.state.Steps)
		}
	}
	for _, kept := range []string{"fanout_iter0", "fanout_iter1", "other_iter9"} {
		if _, ok := executor.state.Steps[kept]; !ok {
			t.Fatalf("step %q was removed unexpectedly: %#v", kept, executor.state.Steps)
		}
	}
	// bd-a3fwf: ghost variables for pruned iterations must be scrubbed so a
	// forced rerun does not resolve through stale future-iteration outputs.
	for _, ghost := range []string{
		"steps.fanout_iter2.output",
		"steps.fanout_iter2.data",
		"steps.fanout_iter3_work.output",
		"steps.fanout_iter3_work.data",
	} {
		if _, ok := executor.state.Variables[ghost]; ok {
			t.Fatalf("ghost variable %q survived force iteration: %#v", ghost, executor.state.Variables)
		}
	}
	for _, kept := range []string{
		"steps.fanout_iter0.output",
		"steps.fanout_iter1.output",
		"steps.other_iter9.output",
	} {
		if _, ok := executor.state.Variables[kept]; !ok {
			t.Fatalf("variable %q was scrubbed unexpectedly: %#v", kept, executor.state.Variables)
		}
	}
}

func TestResumeProgressBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-bookkeeping",
		WorkflowID: "resume-bookkeeping",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.markStepInFlight("fanout", "foreach", 3)
	if got := executor.state.InFlightSteps["fanout"]; got.StepID != "fanout" || got.Kind != "foreach" || got.Iteration != 3 {
		t.Fatalf("in-flight state = %#v, want fanout foreach iteration 3", got)
	}
	executor.clearStepInFlight("fanout")
	if executor.state.InFlightSteps != nil {
		t.Fatalf("InFlightSteps = %#v, want nil after clearing last entry", executor.state.InFlightSteps)
	}

	start := executor.beginForeachState("fanout", 4)
	if start != 0 {
		t.Fatalf("initial foreach start = %d, want 0", start)
	}
	executor.markForeachIterationCompleted("fanout", 0, 4)
	executor.markForeachIterationCompleted("fanout", 2, 4)
	// bd-p12ti: completed=[iter0, iter2] has a gap at iteration 1.
	// markForeachIterationCompleted(2) bumps CurrentIteration to 3, but the
	// durable completed set is authoritative — beginForeachState must start
	// at the first gap (1), not jump past it to the cursor.
	start = executor.beginForeachState("fanout", 4)
	if start != 1 {
		t.Fatalf("gap-aware foreach start = %d, want 1 (skip past gap is unsafe)", start)
	}
	if got := executor.state.ForeachState["fanout"].CurrentIteration; got != 1 {
		t.Fatalf("CurrentIteration after gap-aware begin = %d, want 1", got)
	}
	// bd-p12ti regression guard: with no gaps and CurrentIteration ahead,
	// resume legitimately starts at CurrentIteration.
	executor.markForeachIterationCompleted("fanout", 1, 4)
	start = executor.beginForeachState("fanout", 4)
	if start != 3 {
		t.Fatalf("contiguous-complete foreach start = %d, want 3", start)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter2"}, "fanout", 4); got != 1 {
		t.Fatalf("first gap incomplete iteration = %d, want 1", got)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3"}, "fanout", 4); got != 4 {
		t.Fatalf("all-complete first incomplete = %d, want 4", got)
	}

	if iterationSucceeded([]StepResult{{Status: StatusCompleted}, {Status: StatusSkipped}}, false) != true {
		t.Fatal("completed/skipped iteration was not treated as successful")
	}
	if iterationSucceeded([]StepResult{{Status: StatusCompleted}}, true) != false {
		t.Fatal("break-controlled iteration was treated as successful")
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldCompleteForeachIteration(cancelledCtx, nil, false, false) {
		t.Fatal("empty cancelled iteration was treated as completed")
	}
	// bd-vq8bc: late cancellation after every nested step ran
	// (completedAllSteps=true) is still safe to checkpoint.
	if !shouldCompleteForeachIteration(cancelledCtx, []StepResult{{Status: StatusCompleted}}, false, true) {
		t.Fatal("completed iteration was not checkpointed after late cancellation")
	}
	// bd-vq8bc: mid-iteration cancellation after a successful body step
	// must NOT mark the iteration complete; resume needs to rerun the
	// remaining body steps. The previous len(results)>0 heuristic
	// returned true for this case and silently dropped work.
	if shouldCompleteForeachIteration(cancelledCtx, []StepResult{{Status: StatusCompleted}}, false, false) {
		t.Fatal("partial cancelled iteration was treated as complete (bd-vq8bc regression)")
	}
	// Clean (non-cancelled) ctx with an intentional break/continue keeps
	// existing semantics — break is rejected via iterationSucceeded.
	if shouldCompleteForeachIteration(context.Background(), []StepResult{{Status: StatusCompleted}}, true, false) {
		t.Fatal("break-controlled iteration treated as complete")
	}
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled, StatusRunning, StatusPending} {
		if iterationSucceeded([]StepResult{{Status: status}}, false) {
			t.Fatalf("iteration status %s was treated as successful", status)
		}
	}
}

func TestResumeParallelAndScopeBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-parallel",
		WorkflowID: "resume-parallel",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.beginParallelState("group", 3)
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "b")
	executor.markParallelSubstepFinished("group", "a", StatusCompleted)
	executor.markParallelSubstepFinished("group", "b", StatusFailed)
	executor.completeParallelState("group")

	group := executor.state.ParallelState["group"]
	if !group.AllSubstepsSettled || group.CompletedAt.IsZero() {
		t.Fatalf("parallel group was not completed: %#v", group)
	}
	if !reflect.DeepEqual(group.CompletedStepIDs, []string{"a"}) {
		t.Fatalf("CompletedStepIDs = %#v, want [a]", group.CompletedStepIDs)
	}
	if !reflect.DeepEqual(group.FailedStepIDs, []string{"b"}) {
		t.Fatalf("FailedStepIDs = %#v, want [b]", group.FailedStepIDs)
	}
	if len(group.InFlightStepIDs) != 0 {
		t.Fatalf("InFlightStepIDs = %#v, want empty", group.InFlightStepIDs)
	}

	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindLoop, Name: "row"})
	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindParallel, Name: "group"})
	executor.popScopeFrameLocked()
	if got := executor.state.ScopeStack; len(got) != 1 || got[0].Name != "row" {
		t.Fatalf("ScopeStack after pop = %#v, want only row frame", got)
	}
	executor.popScopeFrameLocked()
	executor.popScopeFrameLocked()
	if len(executor.state.ScopeStack) != 0 {
		t.Fatalf("ScopeStack = %#v, want empty", executor.state.ScopeStack)
	}
}
