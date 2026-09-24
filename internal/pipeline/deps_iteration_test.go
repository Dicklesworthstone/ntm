package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func iterationRecoveryWorkflow() *Workflow {
	leaf := Step{ID: "leaf", Prompt: "Do the work", OutputVar: "answer"}
	return &Workflow{Steps: []Step{
		{ID: "legacy", Loop: &LoopConfig{Items: "${vars.items}", Steps: []Step{leaf}}},
		{ID: "counted", Loop: &LoopConfig{Times: 3, Steps: []Step{leaf}}},
		{ID: "conditional", Loop: &LoopConfig{While: "true", Steps: []Step{leaf}}},
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
	for _, tc := range []struct {
		id        string
		canonical string
		outputVar string
	}{
		{"legacy_iter0_leaf", "leaf", "answer"},
		{"counted_iter2_leaf", "leaf", "answer"},
		{"conditional_iter17_leaf", "leaf", "answer"},
		{"batch_iter7_leaf_round2", "leaf", "answer"},
		{"batch_iter7_leaf_round3", "leaf", "answer"},
		{"batch_iter7_leaf_round3_round2", "leaf_round3", "authored"},
		{"batch_iter7_nested_round2_iter1_leaf", "leaf", "answer"},
		{"batch_iter7_parallel_round2_leaf", "leaf", "answer"},
		{"batch_iter7_branch_round2_loop_iter1_leaf", "leaf", "answer"},
		{"alias_iter0_leaf", "leaf", "answer"},
		{"template_loop_iter0_template_round2", "template", "template_answer"},
		{"anonymous_iter0_step0_round2", "", "anonymous_answer"},
		{"panes_iter1_leaf", "leaf", "answer"},
		{"outer_loop_iter3_leaf_round2", "leaf", "answer"},
		{"dynamic_iter0_leaf", "leaf", "answer"},
		{"dynamic_iter0_leaf_round1", "leaf", "answer"},
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
		"legacy_iter0_leaf_round2", "outer_loop_iter1_leaf_missing",
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
		"legacy_iter0_leaf", "counted_iter1_leaf", "conditional_iter2_leaf",
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
