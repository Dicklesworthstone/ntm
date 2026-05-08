package pipeline

import (
	"context"
	"strings"
	"testing"
)

// TestForeachMaxRounds_LiteralRunsBodyNTimes covers bd-2ubxp.14 acceptance #1:
// max_rounds: 3 → iteration body sees ${round} = 1, 2, 3 in order. The body
// echoes ${round} so we can confirm both the count and the ordering of round
// bindings; the same iteration runs the body three times with distinct round
// values. Per-round step IDs land under unique keys in state.Steps.
func TestForeachMaxRounds_LiteralRunsBodyNTimes(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-literal"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-literal-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Value: 3},
				Steps: []Step{
					{
						ID:      "echo_round",
						Command: "echo round=${round}/${rounds_remaining}",
					},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	for round := 1; round <= 3; round++ {
		key := buildExpectedRoundStepID(round)
		got, ok := state.Steps[key]
		if !ok {
			t.Fatalf("state.Steps[%q] missing — round %d body did not run", key, round)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("round %d status = %s, want completed; error=%+v", round, got.Status, got.Error)
		}
		want := stringForRound(round, 3)
		if !strings.Contains(got.Output, want) {
			t.Errorf("round %d output = %q, want to contain %q", round, got.Output, want)
		}
	}
}

// TestForeachMaxRounds_LoopControlBreakExitsEarly covers bd-2ubxp.14
// acceptance #2: a loop_control: break inside the body must exit the
// iteration's round loop early. Round 1 runs; round 2's body sets break;
// rounds 3 and 4 must not run.
func TestForeachMaxRounds_LoopControlBreakExitsEarly(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-break"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-break-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Value: 4},
				Steps: []Step{
					{
						ID:      "echo_round",
						Command: "echo round=${round}",
					},
					{
						ID:          "break_after_two",
						Command:     `sh -c 'if [ "${round}" = "2" ]; then exit 0; fi; exit 0'`,
						LoopControl: LoopControlBreak,
						When:        `${round} == "2"`,
					},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	for round := 1; round <= 2; round++ {
		key := buildExpectedRoundStepID(round)
		if _, ok := state.Steps[key]; !ok {
			t.Errorf("state.Steps[%q] missing — round %d should have run", key, round)
		}
	}
	for round := 3; round <= 4; round++ {
		key := buildExpectedRoundStepID(round)
		if got, ok := state.Steps[key]; ok {
			t.Errorf("state.Steps[%q] present with status=%s — round %d ran after break", key, got.Status, round)
		}
	}
}

// TestForeachMaxRounds_ExprResolvesAtIterationEntry covers bd-2ubxp.14
// acceptance #3: max_rounds: ${defaults.foo} resolves at iteration entry
// against the workflow Defaults map. Without dynamic resolution the literal
// expression string would be interpreted as the int 0 and the body would
// never run, or worse, the string would parse-error and fail the iteration.
func TestForeachMaxRounds_ExprResolvesAtIterationEntry(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-expr"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-expr-workflow",
		Settings:      DefaultWorkflowSettings(),
		Defaults: map[string]interface{}{
			"hard_caps": map[string]interface{}{
				"phase_5_max_rounds": 2,
			},
		},
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Expr: "${defaults.hard_caps.phase_5_max_rounds}"},
				Steps: []Step{
					{
						ID:      "echo_round",
						Command: "echo round=${round}",
					},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	for round := 1; round <= 2; round++ {
		key := buildExpectedRoundStepID(round)
		got, ok := state.Steps[key]
		if !ok {
			t.Fatalf("state.Steps[%q] missing — round %d body did not run after expr resolution", key, round)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("round %d status = %s, want completed; error=%+v", round, got.Status, got.Error)
		}
	}
	if _, ok := state.Steps[buildExpectedRoundStepID(3)]; ok {
		t.Errorf("round 3 ran — max_rounds ${defaults.hard_caps.phase_5_max_rounds} resolved to >= 3 instead of 2")
	}
}

// TestForeachMaxRounds_UnsetPreservesSingleRoundBehavior covers the
// back-compat contract: a foreach without max_rounds runs the body exactly
// once per outer iteration (the historical default). The step IDs land
// without any `_round<N>` suffix so existing pipelines and assertions keep
// working.
func TestForeachMaxRounds_UnsetPreservesSingleRoundBehavior(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-unset"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-unset-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items: `["only"]`,
				As:    "item",
				Steps: []Step{
					{
						ID:      "echo_once",
						Command: "echo single",
					},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	got, ok := state.Steps["fanout_iter0_echo_once"]
	if !ok {
		t.Fatalf("state.Steps[fanout_iter0_echo_once] missing — historical single-round path broken")
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", got.Status)
	}
	if _, ok := state.Steps["fanout_iter0_echo_once_round1"]; ok {
		t.Errorf("step ID got `_round1` suffix despite max_rounds being unset — back-compat broken")
	}
}

// TestForeachMaxRounds_NestedForeachKeepsRoundUnique covers bd-2ubxp.21 by
// proving that a nested foreach inside a max_rounds body does NOT collide on
// state.Steps across rounds. The outer body step's ID is suffixed with
// `_round<N>` by rewriteRoundStepIDs; when that body step is itself a
// foreach, materializeForeachSteps prefixes each nested step's ID with the
// rewritten parent ID (`%s_iter%d_%s`), so round 1 and round 2 land at
// distinct keys without any need to recurse into nested config inside
// rewriteRoundStepIDs. This regression locks that contract.
func TestForeachMaxRounds_NestedForeachKeepsRoundUnique(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-nested-foreach"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-nested-foreach-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "outer",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Value: 2},
				Steps: []Step{{
					ID: "inner_fanout",
					Foreach: &ForeachConfig{
						Items: `["x","y"]`,
						As:    "sub",
						Steps: []Step{{
							ID:      "leaf",
							Command: "echo round=${round} sub=${sub}",
						}},
					},
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	// Each (round, sub-iter) cell must land under a distinct state.Steps key.
	for round := 1; round <= 2; round++ {
		for sub := 0; sub <= 1; sub++ {
			key := "outer_iter0_inner_fanout_round" + intToString(round) +
				"_iter" + intToString(sub) + "_leaf"
			got, ok := state.Steps[key]
			if !ok {
				t.Fatalf("state.Steps[%q] missing — round %d sub-iter %d collided into a different key", key, round, sub)
			}
			if got.Status != StatusCompleted {
				t.Fatalf("%s status=%s, want completed; error=%+v", key, got.Status, got.Error)
			}
			want := "round=" + intToString(round)
			if !strings.Contains(got.Output, want) {
				t.Errorf("%s output=%q, want to contain %q", key, got.Output, want)
			}
		}
	}
}

// buildExpectedRoundStepID returns the state.Steps key for round N's
// echo_round body step in the single-iteration max_rounds test fixtures
// (parent=fanout, iter=0, body step=echo_round).
func buildExpectedRoundStepID(round int) string {
	return "fanout_iter0_echo_round_round" + intToString(round)
}

func stringForRound(round, maxRounds int) string {
	return "round=" + intToString(round) + "/" + intToString(maxRounds-round)
}

// intToString avoids strconv import in test file.
func intToString(n int) string {
	switch n {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	case 4:
		return "4"
	}
	// Fallback for unexpected values; tests only use 0..4.
	if n < 0 {
		return "neg"
	}
	return "?"
}
