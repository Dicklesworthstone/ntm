package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func iterationRecoveryWorkflow() *Workflow {
	leaf := Step{ID: "leaf", Prompt: "Do the work", OutputVar: "answer"}
	return &Workflow{Steps: []Step{
		{ID: "legacy", Loop: &LoopConfig{Items: "${vars.items}", Steps: []Step{{ID: "legacy_leaf", Prompt: leaf.Prompt, OutputVar: leaf.OutputVar}}}},
		{ID: "counted", Loop: &LoopConfig{Times: 3, Steps: []Step{{ID: "counted_leaf", Prompt: leaf.Prompt, OutputVar: leaf.OutputVar}}}},
		{ID: "conditional", Loop: &LoopConfig{While: "true", Steps: []Step{{ID: "conditional_leaf", Prompt: leaf.Prompt, OutputVar: leaf.OutputVar}}}},
		{ID: "batch", Foreach: &ForeachConfig{Items: "${vars.items}", MaxRounds: IntOrExpr{Value: 3}, Steps: []Step{
			leaf,
			{ID: "nested", Loop: &LoopConfig{Times: 2, Steps: []Step{leaf}}},
			{ID: "parallel", Parallel: ParallelSpec{Steps: []Step{leaf}}},
			{ID: "branch", Branches: map[string]interface{}{"chosen": []Step{
				{ID: "loop", Loop: &LoopConfig{Times: 2, Steps: []Step{leaf}}},
			}}},
			{ID: "leaf_round3", Command: "printf authored", OutputVar: "authored"},
		}}},
		{ID: "alias", Foreach: &ForeachConfig{Items: "${vars.items}", Body: []Step{leaf}}},
		{ID: "template_loop", OutputVar: "template_answer", Foreach: &ForeachConfig{Items: "${vars.items}", Template: "review.md", MaxRounds: IntOrExpr{Value: 3}}},
		{ID: "anonymous", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{{Prompt: "Review", OutputVar: "anonymous_answer"}}, MaxRounds: IntOrExpr{Value: 3}}},
		{ID: "dynamic", Foreach: &ForeachConfig{Items: "${vars.items}", MaxRounds: IntOrExpr{Expr: "${vars.rounds}"}, Steps: []Step{
			leaf, {ID: "leaf_round2", Command: "printf authored", OutputVar: "authored"},
		}}},
		{ID: "panes", ForeachPane: &ForeachConfig{Steps: []Step{leaf}}},
		{ID: "outer", Parallel: ParallelSpec{Steps: []Step{
			{ID: "loop", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{leaf}, MaxRounds: IntOrExpr{Value: 2}}},
		}}},
	}}
}

func TestRecoveryResolvesIterationRuntimeSteps(t *testing.T) {
	g := NewDependencyGraph(iterationRecoveryWorkflow())
	if errs := g.Validate(); len(errs) != 0 {
		t.Fatalf("invalid recovery fixture: %v", errs)
	}
	for _, tc := range []struct {
		id        string
		canonical string
		outputVar string
	}{
		{"legacy_iter0_legacy_leaf", "legacy_leaf", "answer"},
		{"counted_iter2_counted_leaf", "counted_leaf", "answer"},
		{"conditional_iter17_conditional_leaf", "conditional_leaf", "answer"},
		{"batch_iter7_leaf_round2", "batch_iter7_leaf_round2", "answer"},
		{"batch_iter7_leaf_round3", "batch_iter7_leaf_round3", "answer"},
		{"batch_iter7_leaf_round3_round2", "batch_iter7_leaf_round3_round2", "authored"},
		{"batch_iter7_nested_round2_iter1_leaf", "batch_iter7_nested_round2_iter1_leaf", "answer"},
		{"batch_iter7_parallel_round2_leaf", "batch_iter7_parallel_round2_leaf", "answer"},
		{"batch_iter7_branch_round2_loop_iter1_leaf", "batch_iter7_branch_round2_loop_iter1_leaf", "answer"},
		{"alias_iter0_leaf", "alias_iter0_leaf", "answer"},
		{"template_loop_iter0_template_round2", "template_loop_iter0_template_round2", "template_answer"},
		{"anonymous_iter0_step0_round2", "anonymous_iter0_step0_round2", "anonymous_answer"},
		{"panes_iter1_leaf", "panes_iter1_leaf", "answer"},
		{"outer_loop_iter3_leaf_round2", "outer_loop_iter3_leaf_round2", "answer"},
		{"dynamic_iter0_leaf", "dynamic_iter0_leaf", "answer"},
		{"dynamic_iter0_leaf_round1", "dynamic_iter0_leaf_round1", "answer"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			step, canonical, ok := g.ResolveScopedRuntimeStep(tc.id)
			if !ok || step == nil || canonical != tc.canonical || step.OutputVar != tc.outputVar {
				t.Fatalf("resolve %q: step=%+v canonical=%q ok=%v", tc.id, step, canonical, ok)
			}
		})
	}
}

func TestRecoveryRejectsMalformedIterationRuntimeIdentities(t *testing.T) {
	g := NewDependencyGraph(iterationRecoveryWorkflow())
	for _, id := range []string{
		"batch_iter0", "batch_iter_leaf", "batch_iter-1_leaf", "batch_iter+1_leaf", "batch_iter1_leaf",
		"batch_iter01_leaf", "batch_iter1other_leaf", "batch_iter1.leaf",
		"batch_iter1_leaf_missing", "batch_iter1_leaf_round0", "batch_iter1_leaf_round01",
		"batch_iter1_leaf_round-2", "batch_iter1_leaf_round2other",
		"legacy_iter0_legacy_leaf_round2", "outer_loop_iter1_leaf_missing",
		"batch_iter1_nestedness_iter0_leaf", "missing_iter0_leaf",
		"batch_iter" + strings.Repeat("9", 80) + "_leaf",
		"batch_iter0_leaf_round" + strings.Repeat("9", 80),
		"dynamic_iter0_leaf_round2",
	} {
		if step, canonical, ok := g.ResolveScopedRuntimeStep(id); ok {
			t.Errorf("invalid identity %q mapped to %+v (%s)", id, step, canonical)
		}
	}
}

func TestIterationLegacyResumeCannotBypassDeliveryEvidence(t *testing.T) {
	for _, id := range []string{
		"legacy_iter0_legacy_leaf", "counted_iter1_counted_leaf", "conditional_iter2_conditional_leaf",
		"batch_iter1_leaf_round2", "batch_iter1_nested_round2_iter0_leaf",
		"template_loop_iter0_template_round1", "anonymous_iter0_step0_round1", "outer_loop_iter0_leaf_round1",
	} {
		t.Run(id, func(t *testing.T) {
			prior := &ExecutionState{
				Steps:         map[string]StepResult{id: {StepID: id, Status: StatusRunning}},
				InFlightSteps: map[string]InFlightStepState{id: {StepID: id}},
			}
			data, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			var restored ExecutionState
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			e := &Executor{state: &restored, graph: NewDependencyGraph(iterationRecoveryWorkflow())}
			err = e.applyResumeOptions(iterationRecoveryWorkflow(), defaultResumeOptions())
			if err == nil || !strings.Contains(err.Error(), "no durable delivery evidence") {
				t.Fatalf("unfinished iteration agent must not implicitly resend: %v", err)
			}
			beforeReset, err := json.Marshal(e.state)
			if err != nil || string(beforeReset) != string(data) {
				t.Fatal("failed validation changed recovery evidence")
			}
			err = e.applyResumeOptions(iterationRecoveryWorkflow(), ResumeOptions{Mode: ResumeModeRestartFailed, KeepState: true})
			if err != nil {
				t.Fatalf("explicit restart authorization was ignored: %v", err)
			}
		})
	}
}

func TestIterationRecoveryDoesNotCompleteUnrelatedAuthoredName(t *testing.T) {
	workflow := &Workflow{Steps: []Step{
		{ID: "batch", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{{ID: "work", Command: "printf nested"}}}},
		{ID: "work", DependsOn: []string{"batch"}, Command: "printf top-level"},
	}}
	e := &Executor{graph: NewDependencyGraph(workflow), state: &ExecutionState{
		Steps: map[string]StepResult{"batch_iter0_work": {StepID: "batch_iter0_work", Status: StatusCompleted, Output: "nested output"}},
	}}
	e.applyResumeState()
	if e.graph.IsExecuted("work") || e.graph.IsExecuted("batch") {
		t.Fatal("an iteration result completed an unrelated scheduler node")
	}
	if e.state.Steps["batch_iter0_work"].Output != "nested output" {
		t.Fatal("dynamic iteration output was discarded")
	}
	if err := e.graph.MarkExecuted("batch"); err != nil {
		t.Fatal(err)
	}
	if ready := e.graph.GetReadySteps(); !reflect.DeepEqual(ready, []string{"work"}) {
		t.Fatalf("top-level work was skipped: %v", ready)
	}
}

func TestForcedIterationClearsResolvedOutputAliases(t *testing.T) {
	id := "batch_iter1_nested_round2_iter0_leaf"
	e := &Executor{
		graph: NewDependencyGraph(iterationRecoveryWorkflow()),
		state: &ExecutionState{
			Steps: map[string]StepResult{id: {StepID: id, Status: StatusCompleted}},
			Variables: map[string]interface{}{
				"answer": "stale", "answer_parsed": "stale", "input": "keep",
				"steps." + id + ".output": "stale",
			},
		},
	}
	e.forceResumeIteration("batch", 1)
	if len(e.state.Variables) != 1 || e.state.Variables["input"] != "keep" {
		t.Fatalf("forced iteration retained stale scoped aliases: %+v", e.state.Variables)
	}
}

func TestIterationResolverDoesNotMutateWorkflow(t *testing.T) {
	workflow := iterationRecoveryWorkflow()
	before, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	g := NewDependencyGraph(workflow)
	for i := 0; i < 10; i++ {
		g.ResolveScopedRuntimeStep("batch_iter0_branch_round2_loop_iter0_leaf")
		g.ResolveScopedRuntimeStep("template_loop_iter0_template_round2")
	}
	after, err := json.Marshal(workflow)
	if err != nil || string(before) != string(after) {
		t.Fatal("recovery resolution mutated the authored workflow")
	}
}

func forceReplayWorkflow() *Workflow {
	return &Workflow{Steps: []Step{
		{ID: "prepare", Command: "printf prepare"},
		{ID: "batch", DependsOn: []string{"prepare"}, OutputVar: "aggregate", Foreach: &ForeachConfig{
			Items: "${vars.items}", Steps: []Step{{ID: "work", Command: "printf work", OutputVar: "answer"}},
		}},
		{ID: "consumer", DependsOn: []string{"batch"}, Command: "printf consume"},
		{ID: "report", DependsOn: []string{"consumer"}, Command: "printf report"},
		{ID: "independent", Command: "printf independent", OutputVar: "independent_output"},
	}}
}

func forceReplayState() *ExecutionState {
	return &ExecutionState{
		Session: "session",
		Steps: map[string]StepResult{
			"prepare":          {StepID: "prepare", Status: StatusCompleted, Output: "prepared"},
			"batch":            {StepID: "batch", Status: StatusCompleted, Output: "old aggregate"},
			"batch_iter0_work": {StepID: "batch_iter0_work", Status: StatusCompleted, Output: "zero"},
			"batch_iter1_work": {StepID: "batch_iter1_work", Status: StatusCompleted, Output: "one"},
			"batch_iter2_work": {StepID: "batch_iter2_work", Status: StatusCompleted, Output: "two"},
			"independent":      {StepID: "independent", Status: StatusCompleted, Output: "keep"},
		},
		ForeachState: map[string]ForeachIterationState{"batch": {
			StepID: "batch", Total: 3, CurrentIteration: 3,
			CompletedIterationIDs: []string{"batch_iter0", "batch_iter1", "batch_iter2"},
		}},
		Variables: map[string]interface{}{
			"items": []interface{}{"a", "b", "c"}, "aggregate": "old aggregate",
			"aggregate_parsed": "old parsed aggregate", "steps.batch.output": "old aggregate",
			"steps.batch.data": "old parsed aggregate", "independent_output": "keep",
		},
	}
}

func forceReplayDelivery(id string) AgentDeliveryState {
	started := time.Now().Add(-time.Minute)
	return AgentDeliveryState{
		Version: agentDeliveryVersion, StepID: id, Kind: StepKindPrompt, Session: "session",
		Endpoint: "local", PaneID: "%1", PanePID: 123, AgentType: "claude",
		StepHash: strings.Repeat("a", 64), PromptHash: strings.Repeat("b", 64), PromptEchoHash: strings.Repeat("c", 64),
		Status: agentDeliveryDelivered, StartedAt: started, DeliveredAt: started.Add(time.Second),
	}
}

func TestForceReplayReopensCompletedSchedulerNode(t *testing.T) {
	workflow := forceReplayWorkflow()
	prior := forceReplayState()
	prior.InFlightSteps = map[string]InFlightStepState{"batch": {StepID: "batch"}}
	prior.AgentDeliveries = map[string]AgentDeliveryState{
		"batch_iter0_work": forceReplayDelivery("batch_iter0_work"),
		"batch_iter1_work": forceReplayDelivery("batch_iter1_work"),
	}
	encoded, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	var restored ExecutionState
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	e := &Executor{state: &restored, graph: NewDependencyGraph(workflow)}
	if err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1}); err != nil {
		t.Fatal(err)
	}
	// This is the real graph restoration path used by Resume, not a direct
	// query of the pruning helper. A stale parent result makes it skip batch.
	e.applyResumeState()
	if e.graph.IsExecuted("batch") || e.graph.IsExecuted("consumer") {
		t.Fatal("forced loop or its not-yet-run consumer was marked executed")
	}
	if ready := e.graph.GetReadySteps(); !reflect.DeepEqual(ready, []string{"batch"}) {
		t.Fatalf("ready steps after forced replay = %v, want only batch", ready)
	}
	if !e.graph.IsExecuted("prepare") || !e.graph.IsExecuted("independent") {
		t.Fatal("unrelated or preceding work lost completion")
	}
	for _, key := range []string{"aggregate", "aggregate_parsed", "steps.batch.output", "steps.batch.data"} {
		if _, exists := restored.Variables[key]; exists {
			t.Errorf("stale parent output %q survived graph restoration", key)
		}
	}
	if restored.Steps["batch_iter0_work"].Output != "zero" || restored.Variables["independent_output"] != "keep" {
		t.Fatal("prior iteration or independent output was lost")
	}
	if len(restored.AgentDeliveries) != 1 || restored.AgentDeliveries["batch_iter0_work"].StepID == "" {
		t.Fatal("forced replay did not preserve exactly the earlier dispatch receipt")
	}
	if err := e.graph.MarkExecuted("batch"); err != nil {
		t.Fatal(err)
	}
	if ready := e.graph.GetReadySteps(); !reflect.DeepEqual(ready, []string{"consumer"}) {
		t.Fatalf("consumer did not wait for replay completion: %v", ready)
	}
}

func TestForceReplayRefusesInvalidSelectionsWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(*Workflow, *ExecutionState, *ResumeOptions)
	}{
		{"unknown_step", func(_ *Workflow, _ *ExecutionState, o *ResumeOptions) { o.StepID = "missing" }},
		{"non_iterating_step", func(_ *Workflow, _ *ExecutionState, o *ResumeOptions) { o.StepID = "prepare" }},
		{"past_end", func(_ *Workflow, _ *ExecutionState, o *ResumeOptions) { o.Iteration = 3 }},
		{"missing_cursor", func(_ *Workflow, s *ExecutionState, _ *ResumeOptions) { s.ForeachState = nil }},
		{"empty_cursor", func(_ *Workflow, s *ExecutionState, _ *ResumeOptions) {
			s.ForeachState["batch"] = ForeachIterationState{StepID: "batch"}
		}},
		{"mismatched_cursor", func(_ *Workflow, s *ExecutionState, _ *ResumeOptions) {
			s.ForeachState["batch"] = ForeachIterationState{StepID: "other", Total: 3}
		}},
		{"nested_target", func(w *Workflow, _ *ExecutionState, o *ResumeOptions) {
			w.Steps[1].Foreach.Steps = []Step{{ID: "nested", Foreach: &ForeachConfig{Items: "${vars.items}"}}}
			o.StepID = "batch_iter0_nested"
		}},
		{"times_not_resumable", func(w *Workflow, _ *ExecutionState, _ *ResumeOptions) {
			w.Steps[1].Foreach = nil
			w.Steps[1].Loop = &LoopConfig{Times: 3}
		}},
		{"while_not_resumable", func(w *Workflow, _ *ExecutionState, _ *ResumeOptions) {
			w.Steps[1].Foreach = nil
			w.Steps[1].Loop = &LoopConfig{While: "true"}
		}},
		{"namespace_collision", func(w *Workflow, _ *ExecutionState, _ *ResumeOptions) {
			w.Steps = append(w.Steps, Step{ID: "batch_iter1_unrelated", Command: "printf other"})
		}},
		{"parent_was_agent_leaf", func(_ *Workflow, s *ExecutionState, _ *ResumeOptions) {
			s.AgentDeliveries = map[string]AgentDeliveryState{"batch": forceReplayDelivery("batch")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow, prior := forceReplayWorkflow(), forceReplayState()
			opts := ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1, OnRosterChange: ResumeRosterProceed}
			tc.seed(workflow, prior, &opts)
			before, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			e := &Executor{state: prior, graph: NewDependencyGraph(workflow), config: ExecutorConfig{Session: "new-session"}}
			if tc.name == "parent_was_agent_leaf" {
				// Reach the scope check, rather than being rejected earlier by
				// the independent cross-session receipt fence.
				e.config.Session = prior.Session
			}
			if err := e.applyResumeOptions(workflow, opts); err == nil {
				t.Fatal("invalid replay selection was accepted")
			}
			after, err := json.Marshal(prior)
			if err != nil || string(before) != string(after) {
				t.Fatal("refused replay mutated checkpoint or session")
			}
		})
	}
}

func TestForceReplayRefusesDownstreamRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(*ExecutionState)
	}{
		{"completed_consumer", func(s *ExecutionState) { s.Steps["consumer"] = StepResult{Status: StatusCompleted} }},
		{"failed_consumer", func(s *ExecutionState) { s.Steps["consumer"] = StepResult{Status: StatusFailed} }},
		{"transitive_consumer", func(s *ExecutionState) { s.Steps["report"] = StepResult{Status: StatusCompleted} }},
		{"nested_result", func(s *ExecutionState) { s.Steps["consumer_iter0_leaf"] = StepResult{Status: StatusCompleted} }},
		{"inflight_only", func(s *ExecutionState) {
			s.InFlightSteps = map[string]InFlightStepState{"consumer_leaf": {StepID: "consumer_leaf"}}
		}},
		{"receipt_only", func(s *ExecutionState) {
			s.AgentDeliveries = map[string]AgentDeliveryState{"report_leaf": forceReplayDelivery("report_leaf")}
		}},
		{"nested_foreach_only", func(s *ExecutionState) {
			s.ForeachState["consumer_nested"] = ForeachIterationState{StepID: "consumer_nested", Total: 3}
		}},
		{"parallel_only", func(s *ExecutionState) {
			s.ParallelState = map[string]ParallelGroupState{"consumer_parallel": {Total: 2}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow, prior := forceReplayWorkflow(), forceReplayState()
			tc.seed(prior)
			before, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			// A detached-worker preflight has no executor graph yet.
			e := &Executor{state: prior}
			err = e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1})
			if err == nil || !strings.Contains(err.Error(), "downstream recovery evidence") {
				t.Fatalf("dependent work was silently reused or redispatched: %v", err)
			}
			after, err := json.Marshal(prior)
			if err != nil || string(before) != string(after) {
				t.Fatal("rejected replay mutated recovery evidence")
			}
			if err := e.applyResumeOptions(workflow, ResumeOptions{Reset: true}); err != nil {
				t.Fatalf("explicit reset should remain available: %v", err)
			}
		})
	}
}

func TestForceReplayLegacyCollectAndUnstartedLoop(t *testing.T) {
	workflow, prior := forceReplayWorkflow(), forceReplayState()
	workflow.Steps[1].Foreach = nil
	workflow.Steps[1].Loop = &LoopConfig{Items: "${vars.items}", Collect: "collected", Steps: []Step{{ID: "work", Command: "printf work"}}}
	prior.Variables["collected"] = []interface{}{"a", "b", "c"}
	e := &Executor{state: prior, graph: NewDependencyGraph(workflow)}
	if err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1}); err != nil {
		t.Fatal(err)
	}
	if _, exists := prior.Variables["collected"]; exists {
		t.Fatal("legacy collect aggregate survived replay")
	}
	// An unstarted loop can be explicitly restarted from zero; no source
	// query or iteration-count guess is necessary before real execution.
	e = &Executor{state: &ExecutionState{}, graph: NewDependencyGraph(workflow)}
	if err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 0}); err != nil {
		t.Fatal(err)
	}
}

func TestForceReplayFindsNestedConsumerOwnership(t *testing.T) {
	workflow, prior := forceReplayWorkflow(), forceReplayState()
	workflow.Steps = append(workflow.Steps,
		Step{ID: "side", Parallel: ParallelSpec{Steps: []Step{{ID: "nested_consumer", DependsOn: []string{"batch"}, Command: "printf nested"}}}},
		Step{ID: "side_report", DependsOn: []string{"side"}, Command: "printf report"},
	)
	for _, id := range []string{"side_nested_consumer", "side_report"} {
		prior.Steps[id] = StepResult{Status: StatusCompleted}
		e := &Executor{state: prior}
		err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1})
		if err == nil || !strings.Contains(err.Error(), "downstream recovery evidence") {
			t.Fatalf("nested consumer evidence %q was ignored: %v", id, err)
		}
		delete(prior.Steps, id)
	}
}

func TestDetachedIterationResumePreflightUsesWorkflowIdentity(t *testing.T) {
	for _, id := range []string{"batch_iter1_leaf_round2", "outer_loop_iter1_leaf_round1", "template_loop_iter0_template_round2"} {
		e := &Executor{state: &ExecutionState{
			InFlightSteps: map[string]InFlightStepState{id: {StepID: id, Kind: StepKindPrompt}},
		}}
		err := e.applyResumeOptions(iterationRecoveryWorkflow(), defaultResumeOptions())
		if err == nil || !strings.Contains(err.Error(), "no durable delivery evidence") {
			t.Fatalf("graphless worker preflight bypassed receipt safety for %q: %v", id, err)
		}
		if e.graph != nil {
			t.Fatal("preflight installed a live scheduler graph")
		}
		if err := e.applyResumeOptions(iterationRecoveryWorkflow(), ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
			t.Fatalf("explicit restart was refused: %v", err)
		}
	}
}

func TestLegacyReceiptGuardUsesPersistedDispatchKind(t *testing.T) {
	for _, tc := range []struct{ id, kind string }{
		{"dynamic_iter0_leaf_round2", StepKindPrompt}, // Ambiguous current round dialect.
		{"alias_iter0_leaf_round2", StepKindPrompt},   // Current workflow no longer uses rounds.
		{"removed_agent_step", StepKindTemplate},
	} {
		e := &Executor{state: &ExecutionState{InFlightSteps: map[string]InFlightStepState{
			tc.id: {StepID: tc.id, Kind: tc.kind},
		}}}
		err := e.applyResumeOptions(iterationRecoveryWorkflow(), defaultResumeOptions())
		if err == nil || !strings.Contains(err.Error(), "no durable delivery evidence") {
			t.Fatalf("unresolved %q lost its recorded dispatch kind: %v", tc.id, err)
		}
		if err := e.applyResumeOptions(iterationRecoveryWorkflow(), ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
			t.Fatalf("explicit restart refused: %v", err)
		}
	}
}
