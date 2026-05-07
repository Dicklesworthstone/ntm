package pipeline

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func createForeachTestExecutor(t *testing.T, workflow *Workflow) *Executor {
	t.Helper()
	cfg := DefaultExecutorConfig("test")
	cfg.DefaultTimeout = 2 * time.Second
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	e.defaults = workflow.Defaults
	e.limits = workflow.Settings.Limits.EffectiveLimits()
	return e
}

func TestExecuteStepOnceForeachSequentialDispatchesOrderedResults(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-sequential",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c"]`,
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s' '${item}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeStepOnce(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	for i, want := range []string{"a", "b", "c"} {
		if len(iterations[i].Results) != 1 {
			t.Fatalf("iteration %d results = %d, want 1", i, len(iterations[i].Results))
		}
		if got := iterations[i].Results[0].Output; got != want {
			t.Fatalf("iteration %d output = %q, want %q", i, got, want)
		}
		stepID := "fanout_iter" + string(rune('0'+i)) + "_echo"
		if stored := e.state.Steps[stepID]; stored.Output != want {
			t.Fatalf("stored result %s output = %q, want %q", stepID, stored.Output, want)
		}
	}
}

func TestExecuteForeachParallelMaxConcurrentCompletesAllIterations(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-parallel",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "parallel_fanout",
		Foreach: &ForeachConfig{
			Items:         `["a","b","c"]`,
			Parallel:      true,
			MaxConcurrent: 2,
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s' '${item}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	seen := map[string]bool{}
	for _, iteration := range iterations {
		if len(iteration.Results) != 1 {
			t.Fatalf("iteration %d results = %d, want 1", iteration.Index, len(iteration.Results))
		}
		seen[iteration.Results[0].Output] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Fatalf("parallel foreach missing output %q in %#v", want, seen)
		}
	}
}

func TestExecuteForeachContinueKeepsOtherIterationsAfterFailure(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-continue",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:      "continue_fanout",
		OnError: ErrorActionContinue,
		Foreach: &ForeachConfig{
			Items: `["one","bad","two"]`,
			Steps: []Step{{
				ID:      "maybe",
				Command: `case '${item}' in bad) echo failed; exit 7;; *) printf '%s' '${item}';; esac`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	if iterations[0].Results[0].Status != StatusCompleted || iterations[0].Results[0].Output != "one" {
		t.Fatalf("iteration 0 result = %#v, want completed one", iterations[0].Results[0])
	}
	if iterations[1].Results[0].Status != StatusFailed {
		t.Fatalf("iteration 1 status = %s, want failed", iterations[1].Results[0].Status)
	}
	if iterations[2].Results[0].Status != StatusCompleted || iterations[2].Results[0].Output != "two" {
		t.Fatalf("iteration 2 result = %#v, want completed two", iterations[2].Results[0])
	}
	if !strings.Contains(result.Output, "1 failed") {
		t.Fatalf("foreach output = %q, want failure count", result.Output)
	}
}

func TestExecuteForeachWorkflowContinueKeepsBodyStepsAfterFailure(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-workflow-continue",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue
	step := &Step{
		ID: "workflow_continue_fanout",
		Foreach: &ForeachConfig{
			Items: `["one"]`,
			Steps: []Step{
				{
					ID:      "fail",
					Command: `echo failed; exit 7`,
				},
				{
					ID:      "after",
					Command: `printf 'after-%s' '${item}'`,
				},
			},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 1 {
		t.Fatalf("iterations = %d, want 1", len(iterations))
	}
	if got := len(iterations[0].Results); got != 2 {
		t.Fatalf("iteration results = %d, want failed body step plus follow-up", got)
	}
	if iterations[0].Results[0].Status != StatusFailed {
		t.Fatalf("first body status = %s, want failed", iterations[0].Results[0].Status)
	}
	if iterations[0].Results[1].Status != StatusCompleted || iterations[0].Results[1].Output != "after-one" {
		t.Fatalf("second body result = %#v, want completed after-one", iterations[0].Results[1])
	}
}

func TestExecuteForeachFilterExcludesIterationsBeforeDispatch(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-filter",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "filtered_fanout",
		Foreach: &ForeachConfig{
			Items:  `[{"id":"a","role":"keep"},{"id":"b","role":"drop"},{"id":"c","role":"keep"}]`,
			Filter: `role==keep`,
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s' '${item.id}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	var outputs []string
	var skipped int
	for _, iteration := range iterations {
		if iteration.Skipped {
			skipped++
			if iteration.SkipKind != SkipKindForeachFilter {
				t.Fatalf("skip kind = %q, want %q", iteration.SkipKind, SkipKindForeachFilter)
			}
			continue
		}
		if len(iteration.Results) != 1 {
			t.Fatalf("iteration %d results = %d, want 1", iteration.Index, len(iteration.Results))
		}
		outputs = append(outputs, iteration.Results[0].Output)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if strings.Join(outputs, ",") != "a,c" {
		t.Fatalf("dispatched outputs = %v, want [a c]", outputs)
	}
	if got := len(e.state.Steps); got != 2 {
		t.Fatalf("stored dispatched steps = %d, want 2", got)
	}
}

func TestExecuteForeachNestedAliasesExposeOuterAndInnerItems(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-nested-aliases",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "outer_fanout",
		Foreach: &ForeachConfig{
			Items: `[{"id":"H1","evidence_ids":[{"id":"E1"},{"id":"E2"}]},{"id":"H2","evidence_ids":[{"id":"E3"}]}]`,
			As:    "outer",
			Steps: []Step{{
				ID: "inner_fanout",
				Foreach: &ForeachConfig{
					Items: `${outer.evidence_ids}`,
					As:    "inner",
					Steps: []Step{{
						ID:      "echo",
						Command: `printf '%s/%s' '${outer.id}' '${inner.id}'`,
					}},
				},
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	outputs := foreachLeafOutputs(result)
	if got, want := strings.Join(outputs, ","), "H1/E1,H1/E2,H2/E3"; got != want {
		t.Fatalf("nested outputs = %q, want %q", got, want)
	}
}

func TestExecuteForeachNestedDefaultItemShadowsOuterItem(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-nested-shadow",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "outer_fanout",
		Foreach: &ForeachConfig{
			Items: `[{"id":"outer","children":[{"id":"inner"}]}]`,
			Steps: []Step{{
				ID: "inner_fanout",
				Foreach: &ForeachConfig{
					Items: `${item.children}`,
					Steps: []Step{{
						ID:      "echo",
						Command: `printf '%s' '${item.id}'`,
					}},
				},
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	outputs := foreachLeafOutputs(result)
	if got, want := strings.Join(outputs, ","), "inner"; got != want {
		t.Fatalf("nested default item output = %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "shadows outer item") {
		t.Fatalf("debug log = %q, want shadowing warning", logs.String())
	}
}

func TestExecuteForeachNestedAliasesSupportThreeLevels(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-nested-three-levels",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "outer_fanout",
		Foreach: &ForeachConfig{
			Items: `[{"id":"O","middles":[{"id":"M","inners":[{"id":"I"}]}]}]`,
			As:    "outer",
			Steps: []Step{{
				ID: "middle_fanout",
				Foreach: &ForeachConfig{
					Items: `${outer.middles}`,
					As:    "middle",
					Steps: []Step{{
						ID: "inner_fanout",
						Foreach: &ForeachConfig{
							Items: `${middle.inners}`,
							As:    "inner",
							Steps: []Step{{
								ID:      "echo",
								Command: `printf '%s/%s/%s' '${outer.id}' '${middle.id}' '${inner.id}'`,
							}},
						},
					}},
				},
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	outputs := foreachLeafOutputs(result)
	if got, want := strings.Join(outputs, ","), "O/M/I"; got != want {
		t.Fatalf("three-level outputs = %q, want %q", got, want)
	}
}

func TestExecuteForeachParallelOuterSequentialInnerKeepsAliasesIsolated(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-parallel-nested-isolation",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "outer_fanout",
		Foreach: &ForeachConfig{
			Items:         `[{"id":"O1","children":[{"id":"A"}]},{"id":"O2","children":[{"id":"B"}]}]`,
			As:            "outer",
			Parallel:      true,
			MaxConcurrent: 2,
			Steps: []Step{{
				ID: "inner_fanout",
				Foreach: &ForeachConfig{
					Items: `${outer.children}`,
					As:    "inner",
					Steps: []Step{{
						ID:      "echo",
						Command: `printf '%s/%s' '${outer.id}' '${inner.id}'`,
					}},
				},
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	outputs := foreachLeafOutputs(result)
	if got, want := strings.Join(outputs, ","), "O1/A,O2/B"; got != want {
		t.Fatalf("parallel nested outputs = %q, want %q", got, want)
	}
}

func TestExecuteForeachLoopControlBreakSkipsRemainingIterations(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-break",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "break_fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c","d","e"]`,
			Steps: []Step{
				{
					ID:      "echo",
					Command: `printf '%s' '${item}'`,
				},
				{
					ID:          "break_on_c",
					LoopControl: LoopControlBreak,
					When:        `${item} == "c"`,
				},
			},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 5 {
		t.Fatalf("iterations = %d, want 5", len(iterations))
	}
	if got, want := strings.Join(foreachLeafOutputs(result), ","), "a,b,c"; got != want {
		t.Fatalf("outputs before break = %q, want %q", got, want)
	}
	for _, iteration := range iterations[3:] {
		if !iteration.Skipped || iteration.SkipKind != SkipKindForeachBreak {
			t.Fatalf("remaining iteration = %#v, want loop-break skip", iteration)
		}
	}
}

func TestExecuteForeachSkippedBodyStepSkipsIteration(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-skipped-body",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "skip_fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c"]`,
			Steps: []Step{{
				ID:      "maybe",
				When:    `${item} == "run"`,
				Command: `printf '%s' '${item}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	for _, iteration := range iterations {
		if !iteration.Skipped || iteration.SkipKind != SkipKindWhenCondition {
			t.Fatalf("iteration = %#v, want when-condition skip", iteration)
		}
	}
	if got := foreachLeafOutputs(result); len(got) != 0 {
		t.Fatalf("outputs = %v, want none", got)
	}
	if !strings.Contains(result.Output, "3 skipped") {
		t.Fatalf("foreach output = %q, want skipped count", result.Output)
	}
}

func TestExecuteForeachMixedSkippedAndCompletedIterations(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-mixed-skip",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "mixed_fanout",
		Foreach: &ForeachConfig{
			Items: `["run","skip-one","skip-two"]`,
			Steps: []Step{{
				ID:      "maybe",
				When:    `${item} == "run"`,
				Command: `printf '%s' '${item}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	if iterations[0].Skipped {
		t.Fatalf("iteration 0 skipped unexpectedly: %#v", iterations[0])
	}
	for _, iteration := range iterations[1:] {
		if !iteration.Skipped || iteration.SkipKind != SkipKindWhenCondition {
			t.Fatalf("iteration = %#v, want when-condition skip", iteration)
		}
	}
	if got, want := strings.Join(foreachLeafOutputs(result), ","), "run"; got != want {
		t.Fatalf("outputs = %q, want %q", got, want)
	}
}

func TestExecuteForeachFailFastFailureIsNotCountedAsSkipped(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-fail-fast-not-skipped",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fail_fast_fanout",
		Foreach: &ForeachConfig{
			Items: `["ok","bad","after"]`,
			Steps: []Step{{
				ID:      "maybe",
				Command: `case '${item}' in bad) exit 9;; *) printf '%s' '${item}';; esac`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusFailed {
		t.Fatalf("foreach status = %s, want failed", result.Status)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 2 {
		t.Fatalf("iterations = %d, want fail-fast halt after 2", len(iterations))
	}
	if iterations[1].Skipped {
		t.Fatalf("failed iteration marked skipped: %#v", iterations[1])
	}
	if iterations[1].Results[0].Status != StatusFailed {
		t.Fatalf("iteration 1 status = %s, want failed", iterations[1].Results[0].Status)
	}
}

func TestExecuteForeachLoopControlContinueSkipsRestOfIteration(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-continue",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "continue_fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c"]`,
			Steps: []Step{
				{
					ID:      "before",
					Command: `printf 'before-%s' '${item}'`,
				},
				{
					ID:          "continue_on_b",
					LoopControl: LoopControlContinue,
					When:        `${item} == "b"`,
				},
				{
					ID:      "after",
					Command: `printf 'after-%s' '${item}'`,
				},
			},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	if got, want := strings.Join(foreachLeafOutputs(result), ","), "before-a,after-a,before-b,before-c,after-c"; got != want {
		t.Fatalf("outputs with continue = %q, want %q", got, want)
	}
	iterations := foreachIterationsFromResult(t, result)
	if iterations[1].Control != LoopControlContinue {
		t.Fatalf("iteration 1 control = %q, want continue", iterations[1].Control)
	}
	if len(iterations[1].Results) != 1 || iterations[1].Results[0].Output != "before-b" {
		t.Fatalf("iteration 1 results = %#v, want only before-b", iterations[1].Results)
	}
}

func TestExecuteForeachParallelLoopControlBreakCancelsInFlight(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-parallel-break",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "parallel_break_fanout",
		Foreach: &ForeachConfig{
			Items:         `["break","slow","later"]`,
			Parallel:      true,
			MaxConcurrent: 3,
			Steps: []Step{
				{
					ID:          "break_now",
					LoopControl: LoopControlBreak,
					When:        `${item} == "break"`,
				},
				{
					ID:      "slow",
					Command: `sleep 5; printf '%s' '${item}'`,
					Timeout: Duration{Duration: 10 * time.Second},
				},
			},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if iterations[0].Control != LoopControlBreak {
		t.Fatalf("break iteration control = %q, want break", iterations[0].Control)
	}
	var breakSkipped int
	for _, iteration := range iterations {
		if iteration.SkipKind == SkipKindForeachBreak {
			breakSkipped++
		}
	}
	if breakSkipped == 0 {
		t.Fatalf("iterations = %#v, want at least one loop-break skipped/cancelled iteration", iterations)
	}
}

func foreachIterationsFromResult(t *testing.T, result StepResult) []foreachIterationResult {
	t.Helper()
	iterations, ok := result.ParsedData.([]foreachIterationResult)
	if !ok {
		t.Fatalf("ParsedData type = %T, want []foreachIterationResult", result.ParsedData)
	}
	return iterations
}

func foreachLeafOutputs(result StepResult) []string {
	var outputs []string
	appendForeachLeafOutputs(&outputs, result)
	return outputs
}

func appendForeachLeafOutputs(outputs *[]string, result StepResult) {
	iterations, ok := result.ParsedData.([]foreachIterationResult)
	if !ok {
		if result.Output != "" {
			*outputs = append(*outputs, result.Output)
		}
		return
	}
	for _, iteration := range iterations {
		for _, nested := range iteration.Results {
			appendForeachLeafOutputs(outputs, nested)
		}
	}
}

func TestForeachMaxConcurrentBoundedByGlobalCap(t *testing.T) {
	// bd-pwxh1: per-step foreach.max_concurrent must be capped by the
	// global settings.limits.max_concurrent_foreach. Otherwise a workflow
	// could set a low global safety limit and bypass it on a single step.
	limits := LimitsConfig{MaxConcurrentForeach: 4}.EffectiveLimits()

	cases := []struct {
		name   string
		config *ForeachConfig
		want   int
	}{
		{name: "no per-step override uses global", config: &ForeachConfig{}, want: 4},
		{name: "per-step under global is honored", config: &ForeachConfig{MaxConcurrent: 2}, want: 2},
		{name: "per-step equal to global is honored", config: &ForeachConfig{MaxConcurrent: 4}, want: 4},
		{name: "per-step above global clamps to global", config: &ForeachConfig{MaxConcurrent: 10000}, want: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foreachMaxConcurrent(tc.config, limits); got != tc.want {
				t.Fatalf("foreachMaxConcurrent() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestForeachMaxConcurrentZeroGlobalAllowsPerStep(t *testing.T) {
	// When the global cap is zero/disabled, a per-step value still applies
	// without being clamped to zero.
	limits := LimitsConfig{}
	limits.MaxConcurrentForeach = 0
	if got := foreachMaxConcurrent(&ForeachConfig{MaxConcurrent: 8}, limits); got != 8 {
		t.Fatalf("foreachMaxConcurrent(unbounded global, per-step=8) = %d, want 8", got)
	}
	// And a missing per-step value falls back to a safe minimum of 1.
	if got := foreachMaxConcurrent(&ForeachConfig{}, limits); got != 1 {
		t.Fatalf("foreachMaxConcurrent(unbounded global, no per-step) = %d, want 1", got)
	}
}
