package pipeline

import (
	"context"
	"os"
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

// TestForeachMaxRounds_NegativeLiteralRejectedAtParse covers bd-ltghx
// acceptance #1: a literal `max_rounds: -N` must surface a clear parse
// error pointing at the offending field, not silently degrade to a single
// round at runtime.
func TestForeachMaxRounds_NegativeLiteralRejectedAtParse(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "neg-rounds",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Value: -1},
				Steps: []Step{{
					ID:      "noop",
					Command: "true",
				}},
			},
		}},
	}

	result := Validate(workflow)
	if result.Valid {
		t.Fatalf("ValidateWorkflow accepted negative max_rounds; want parse error")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "foreach.max_rounds") &&
			strings.Contains(e.Message, "negative") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected parse error pointing at foreach.max_rounds for negative value; got %+v", result.Errors)
	}
}

// TestForeachMaxRounds_ExprResolvedAboveCapClampsToDefault covers bd-ltghx
// acceptance #3: an expression that resolves to a value above
// DefaultMaxRounds is clamped at runtime so a misconfigured external
// value cannot drive the body loop unbounded. Literal values are not
// clamped (parser already rejected the dangerous shapes).
func TestForeachMaxRounds_ExprResolvedAboveCapClampsToDefault(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-cap"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-cap-workflow",
		Settings:      DefaultWorkflowSettings(),
		Defaults: map[string]interface{}{
			"hard_caps": map[string]interface{}{
				"crazy_rounds": 999999,
			},
		},
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Expr: "${defaults.hard_caps.crazy_rounds}"},
				Steps: []Step{{
					ID:      "noop",
					Command: "true",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	// One step result per round; if not clamped, we'd see DefaultMaxRounds+1.
	rounds := 0
	for k := range state.Steps {
		if strings.HasPrefix(k, "fanout_iter0_noop_round") {
			rounds++
		}
	}
	if rounds != DefaultMaxRounds {
		t.Fatalf("body ran %d rounds, want %d (clamped to DefaultMaxRounds)", rounds, DefaultMaxRounds)
	}
}

// TestForeachMaxRounds_OperatorOverrideRaisesCap covers bd-iz5hd: operators
// who legitimately need >DefaultMaxRounds expression-driven rounds can raise
// `limits.max_foreach_rounds` in workflow settings, and the resolver clamps
// to the configured value rather than the constant. The expression resolves
// well above the operator's chosen cap so we observe the clamp at the new
// boundary instead of at DefaultMaxRounds.
func TestForeachMaxRounds_OperatorOverrideRaisesCap(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-override"))

	settings := DefaultWorkflowSettings()
	settings.Limits.MaxForeachRounds = 250

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-override-workflow",
		Settings:      settings,
		Defaults: map[string]interface{}{
			"hard_caps": map[string]interface{}{
				"crazy_rounds": 999999,
			},
		},
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Expr: "${defaults.hard_caps.crazy_rounds}"},
				Steps: []Step{{
					ID:      "noop",
					Command: "true",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	rounds := 0
	for k := range state.Steps {
		if strings.HasPrefix(k, "fanout_iter0_noop_round") {
			rounds++
		}
	}
	if rounds != 250 {
		t.Fatalf("body ran %d rounds, want 250 (operator-configured cap, not the default %d)", rounds, DefaultMaxRounds)
	}
}

// TestForeachMaxRounds_LiteralZeroAndExprZeroBothDefaultToOne covers
// bd-wapme: literal `max_rounds: 0` and expression `${vars.zero}` (where
// vars.zero == 0) used to diverge — literal silently became 1 round,
// expression erroed with "value 0 must be > 0". Both now silently default
// to a single round so refactoring `max_rounds: 0` into a dynamic
// expression doesn't flip behaviour. Acceptance from the bead: the two
// forms produce the same observable behaviour.
func TestForeachMaxRounds_LiteralZeroAndExprZeroBothDefaultToOne(t *testing.T) {
	t.Run("literal_zero", func(t *testing.T) {
		executor := NewExecutor(DefaultExecutorConfig("max-rounds-literal-zero"))
		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "max-rounds-literal-zero-workflow",
			Settings:      DefaultWorkflowSettings(),
			Steps: []Step{{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items:     `["only"]`,
					As:        "item",
					MaxRounds: IntOrExpr{Value: 0},
					Steps: []Step{{
						ID:      "noop",
						Command: "true",
					}},
				},
			}},
		}

		state, err := executor.Run(context.Background(), workflow, nil, nil)
		if err != nil {
			t.Fatalf("Run() error = %v, want nil (literal 0 should default to 1 round)", err)
		}
		if _, ok := state.Steps["fanout_iter0_noop"]; !ok {
			t.Fatalf("expected single-round step ID 'fanout_iter0_noop', got keys: %v", stepKeys(state.Steps))
		}
		// Round-suffix should NOT be applied when max_rounds == 1.
		for k := range state.Steps {
			if strings.Contains(k, "_round") {
				t.Errorf("step %q has _round suffix despite max_rounds=0 defaulting to 1", k)
			}
		}
	})

	t.Run("expression_resolves_to_zero", func(t *testing.T) {
		executor := NewExecutor(DefaultExecutorConfig("max-rounds-expr-zero"))
		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "max-rounds-expr-zero-workflow",
			Settings:      DefaultWorkflowSettings(),
			Defaults: map[string]interface{}{
				"hard_caps": map[string]interface{}{
					"zero_rounds": 0,
				},
			},
			Steps: []Step{{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items:     `["only"]`,
					As:        "item",
					MaxRounds: IntOrExpr{Expr: "${defaults.hard_caps.zero_rounds}"},
					Steps: []Step{{
						ID:      "noop",
						Command: "true",
					}},
				},
			}},
		}

		state, err := executor.Run(context.Background(), workflow, nil, nil)
		if err != nil {
			t.Fatalf("Run() error = %v, want nil (expression resolving to 0 should default to 1 round, matching literal 0)", err)
		}
		if _, ok := state.Steps["fanout_iter0_noop"]; !ok {
			t.Fatalf("expected single-round step ID 'fanout_iter0_noop', got keys: %v", stepKeys(state.Steps))
		}
		for k := range state.Steps {
			if strings.Contains(k, "_round") {
				t.Errorf("step %q has _round suffix despite expression resolving to 0 (should default to 1)", k)
			}
		}
	})
}

func stepKeys(steps map[string]StepResult) []string {
	out := make([]string, 0, len(steps))
	for k := range steps {
		out = append(out, k)
	}
	return out
}

// buildExpectedRoundStepID returns the state.Steps key for round N's
// echo_round body step in the single-iteration max_rounds test fixtures
// (parent=fanout, iter=0, body step=echo_round).
func buildExpectedRoundStepID(round int) string {
	return "fanout_iter0_echo_round_round" + intToString(round)
}

// TestForeachMaxRounds_ResumeSkipsAlreadyCompletedRounds covers bd-r2pan:
// when a foreach iteration is interrupted mid-rounds (process killed
// between executeForeachIteration entry and markForeachIterationCompleted),
// the iteration is NOT in CompletedIterationIDs (bd-qeatk only marks fully
// finished iterations) and on resume re-dispatches every round from 1 —
// duplicating the side effects of any rounds that had completed before
// the interruption.
//
// The fix records each fully-completed round on a per-iteration watermark
// (ForeachIterationState.CompletedRounds[iterID]) so the resume loop
// starts at watermark+1 instead of round 1. Test pre-seeds a foreach
// state with CompletedRounds[fanout_iter0]=2 and runs a max_rounds=4
// foreach against a body step that records its round number to a counter
// file. The expected result is that only rounds 3 and 4 execute (the
// counter file gets two writes, not four), and the recorded watermark
// advances to 4 after the run finishes.
func TestForeachMaxRounds_ResumeSkipsAlreadyCompletedRounds(t *testing.T) {
	tmpDir := t.TempDir()
	counterPath := tmpDir + "/rounds.log"

	cfg := DefaultExecutorConfig("max-rounds-resume")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:      "run-r2pan",
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:           "fanout",
				Total:            1,
				CurrentIteration: 0,
				CompletedRounds:  map[string]int{"fanout_iter0": 2},
			},
		},
		ParallelState: map[string]ParallelGroupState{},
		InFlightSteps: map[string]InFlightStepState{},
	}

	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items:     `["only"]`,
			As:        "item",
			MaxRounds: IntOrExpr{Value: 4},
			Steps: []Step{{
				ID:      "record_round",
				Command: "echo round=${round} >> " + counterPath,
			}},
		},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "r2pan",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{*step},
	}

	res := executor.executeForeach(context.Background(), step, workflow)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q; error = %+v", res.Status, StatusCompleted, res.Error)
	}

	// Only rounds 3 and 4 should have executed — rounds 1 and 2 must be
	// skipped on resume because their watermark was already recorded.
	body, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read counter file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("counter file has %d lines (%q), want 2 — rounds 1/2 should have been skipped on resume", len(lines), body)
	}
	for _, want := range []string{"round=3", "round=4"} {
		found := false
		for _, line := range lines {
			if line == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("counter file missing %q; got %q", want, body)
		}
	}

	// Round watermark must advance to 4 after rounds 3+4 complete cleanly.
	st, ok := executor.state.ForeachState["fanout"]
	if !ok {
		t.Fatal("ForeachState[fanout] missing after run")
	}
	if got := st.CompletedRounds["fanout_iter0"]; got != 4 {
		t.Errorf("CompletedRounds[fanout_iter0] = %d, want 4 (watermark must advance after each clean round)", got)
	}
}

// TestForeachMaxRounds_FreshRunRecordsRoundWatermark covers the other
// side of bd-r2pan: a fresh foreach run with max_rounds > 1 must record
// the round watermark per iteration so a subsequent resume can detect
// progress. Without this, every resume re-runs all rounds from 1.
func TestForeachMaxRounds_FreshRunRecordsRoundWatermark(t *testing.T) {
	cfg := DefaultExecutorConfig("max-rounds-fresh-watermark")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:         "run-r2pan-fresh",
		WorkflowID:    "wf",
		Variables:     map[string]interface{}{},
		Steps:         map[string]StepResult{},
		ForeachState:  map[string]ForeachIterationState{},
		ParallelState: map[string]ParallelGroupState{},
		InFlightSteps: map[string]InFlightStepState{},
	}

	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items:     `["a","b"]`,
			As:        "item",
			MaxRounds: IntOrExpr{Value: 3},
			Steps:     []Step{{ID: "noop", Command: "true"}},
		},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "r2pan-fresh",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{*step},
	}

	_ = executor.executeForeach(context.Background(), step, workflow)

	st, ok := executor.state.ForeachState["fanout"]
	if !ok {
		t.Fatal("ForeachState[fanout] missing after fresh run")
	}
	for _, iterID := range []string{"fanout_iter0", "fanout_iter1"} {
		if got := st.CompletedRounds[iterID]; got != 3 {
			t.Errorf("CompletedRounds[%s] = %d, want 3 (every round must be recorded on a clean run)", iterID, got)
		}
	}
}

// TestForeachMaxRounds_NestedLoopWhenResolvesRoundOverlay covers bd-ypo73:
// a loop nested inside a foreach max_rounds body whose body step carries
// `when: ${round} == N` must see the round overlay carried on ctx by
// withRoundOverrides. The loop dispatcher routes body steps through
// executor.executeStep, whose When evaluation previously called the
// non-ctx evaluateCondition variant — so `${round}` was unresolvable
// and the body step failed with "round not set".
//
// Test shape: max_rounds=3, inner `loop.times: 1` so the body executes
// exactly once per round, body step gated on `when: ${round} == 2`. With
// the bd-ypo73 fix, no recorded step result surfaces a "round not set"
// error; the gated body completes for round 2 and skips for rounds 1/3.
func TestForeachMaxRounds_NestedLoopWhenResolvesRoundOverlay(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("max-rounds-nested-when"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "max-rounds-nested-when-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items:     `["only"]`,
				As:        "item",
				MaxRounds: IntOrExpr{Value: 3},
				Steps: []Step{{
					ID: "nested_loop",
					Loop: &LoopConfig{
						Times:         1,
						MaxIterations: IntOrExpr{Value: 1},
						Steps: []Step{{
							ID:      "gated",
							When:    "${round} == 2",
							Command: "echo gated",
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

	// Walk every recorded step result and assert no substitution error
	// escaped from a non-ctx evaluateCondition call. Without the bd-ypo73
	// fix, the loop body step's when on `${round}` would have surfaced
	// "round not set" as a step error because executeStep called the
	// non-ctx evaluateCondition variant for top-level when checks.
	for id, sr := range state.Steps {
		if sr.Error != nil && strings.Contains(sr.Error.Message, "round not set") {
			t.Fatalf("state.Steps[%q] surfaced %q — bd-ypo73 fix did not thread ctx through executeStep.When", id, sr.Error.Message)
		}
	}
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
