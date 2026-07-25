package pipeline

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

// bd-ljx8s: when only the parent foreach step has on_error: continue (workflow
// default stays at fail), nested body steps without their own on_error must
// inherit the parent's continue policy. Otherwise a per-item failure stops
// later body steps in the same iteration even though the pipeline author
// scoped continue semantics to that exact foreach.
func TestExecuteForeachParentStepContinueKeepsBodyStepsAfterFailure(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-parent-continue",
		Settings:      DefaultWorkflowSettings(),
	}
	// Workflow default stays at fail; only the parent foreach asks for continue.
	step := &Step{
		ID:      "parent_continue_fanout",
		OnError: ErrorActionContinue,
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

func TestSubstituteForeachFieldsResolvesModelsList(t *testing.T) {
	// bd-8bujt: foreach.models entries must receive the same protected-root
	// substitution as items/beads/pairs/debates/filter, otherwise workflow
	// vars and shell sources containing ${...} reach ResolveModels as raw
	// literals and fall apart silently.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-models-substitution",
		Settings:      DefaultWorkflowSettings(),
	}
	e := createForeachTestExecutor(t, workflow)
	e.state.Variables["model_family"] = "claude"
	e.state.Variables["fallback"] = "codex"

	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Models: StringOrList{
				"${vars.model_family}",
				"${vars.fallback}",
				"literal-pinned-model",
			},
			Steps: []Step{{ID: "echo", Command: "true"}},
		},
	}

	e.substituteForeachStepFieldsProtected(step, nil)

	got := step.Foreach.Models
	want := StringOrList{"claude", "codex", "literal-pinned-model"}
	if len(got) != len(want) {
		t.Fatalf("len(Models) = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i, want := range want {
		if got[i] != want {
			t.Errorf("Models[%d] = %q, want %q", i, got[i], want)
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

// bd-s9ptg: foreachProgressInterval bounds total tick count at
// maxForeachProgressTicks (≈20) for any fan-out size. Pre-fix this
// function used a "cap at 5 iterations between events" rule that
// produced ~total/5 ticks — the opposite of the documented intent
// for very large fan-outs. The new contract: small fan-outs (≤20)
// tick on every iteration; larger fan-outs use ceil(total/20) so the
// emitted event count stays bounded.
func TestForeachProgressIntervalBoundsTotalTickCount(t *testing.T) {
	cases := []struct {
		total int
		want  int
	}{
		{total: 0, want: 1},    // degenerate guard
		{total: 1, want: 1},    // single-item -> one tick
		{total: 5, want: 1},    // total <= 20 → tick every iteration
		{total: 20, want: 1},   // boundary: total == maxTicks
		{total: 21, want: 2},   // 21/20 = 1, +1 (round up) = 2
		{total: 41, want: 3},   // 41/20 = 2, +1 = 3
		{total: 47, want: 3},   // 47/20 = 2, +1 = 3
		{total: 50, want: 3},   // 50/20 = 2, +1 = 3
		{total: 100, want: 5},  // 100/20 = 5 (no remainder)
		{total: 200, want: 10}, // 200/20 = 10
		{total: 500, want: 25}, // 500/20 = 25
	}
	for _, tc := range cases {
		if got := foreachProgressInterval(tc.total); got != tc.want {
			t.Errorf("foreachProgressInterval(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

// bd-s9ptg: across a wide range of fan-out sizes, the tick count
// produced by foreachProgressInterval must never exceed
// maxForeachProgressTicks. Pre-fix this assertion would fire for
// every total ≥ 100 (where the cap-at-5 produced total/5 ticks).
func TestForeachProgressIntervalCapsTickCountAcrossSizes(t *testing.T) {
	for _, total := range []int{50, 100, 500, 1000, 5000, 10000, 100000} {
		interval := foreachProgressInterval(total)
		if interval <= 0 {
			t.Errorf("foreachProgressInterval(%d) = %d, want > 0", total, interval)
			continue
		}
		ticks := total / interval
		if total%interval != 0 {
			ticks++
		}
		if ticks > maxForeachProgressTicks {
			t.Errorf("foreachProgressInterval(%d) → interval=%d → %d ticks, exceeds maxForeachProgressTicks=%d",
				total, interval, ticks, maxForeachProgressTicks)
		}
	}
}

// bd-2ubxp.19: per-iteration progress events let operators monitor long
// foreach runs. A 5-iteration foreach should emit exactly 5 starts, 5
// completes (carrying duration_ms + status), and at least one aggregate
// progress event with completed/total/elapsed_ms.
func TestExecuteForeachEmitsPerIterationProgressEvents(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previousLogger)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-progress-events",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c","d","e"]`,
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
	starts, completes, progress, completedFields, statusFields := countForeachIterationEvents(t, &logs, "fanout")
	if starts != 5 {
		t.Errorf("foreach iteration starting events = %d, want 5", starts)
	}
	if completes != 5 {
		t.Errorf("foreach iteration completed events = %d, want 5", completes)
	}
	if progress < 1 {
		t.Errorf("foreach progress events = %d, want >= 1", progress)
	}
	for _, status := range statusFields {
		if status != "completed" {
			t.Errorf("iteration completed event status = %q, want %q", status, "completed")
		}
	}
	if got := completedFields[len(completedFields)-1]; got != 5 {
		t.Errorf("final foreach progress completed = %d, want 5", got)
	}
}

func countForeachIterationEvents(t *testing.T, logs *bytes.Buffer, stepID string) (starts, completes, progress int, progressCompleted []int, completeStatuses []string) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, `"step_id":"`+stepID+`"`) {
			continue
		}
		switch {
		case strings.Contains(line, `"msg":"foreach iteration starting"`):
			starts++
		case strings.Contains(line, `"msg":"foreach iteration completed"`):
			completes++
			if status := extractJSONStringField(line, "status"); status != "" {
				completeStatuses = append(completeStatuses, status)
			}
		case strings.Contains(line, `"msg":"foreach progress"`):
			progress++
			if completed := extractJSONIntField(line, "completed"); completed > 0 {
				progressCompleted = append(progressCompleted, completed)
			}
		}
	}
	return
}

func extractJSONStringField(line, key string) string {
	needle := `"` + key + `":"`
	idx := strings.Index(line, needle)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(needle):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func extractJSONIntField(line, key string) int {
	needle := `"` + key + `":`
	idx := strings.Index(line, needle)
	if idx < 0 {
		return 0
	}
	rest := line[idx+len(needle):]
	end := 0
	for end < len(rest) && (rest[end] == '-' || (rest[end] >= '0' && rest[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// bd-2ubxp.13: foreach_pane treats the panes themselves as the iteration set,
// dispatching the body for every pane in the session unless a filter excludes
// it. Items come from the tmux client's pane list and are surfaced as
// ${item.X} / ${pane.X} maps.
func TestExecuteForeachPaneNoFilterIteratesAllPanes(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-pane-no-filter",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fanout_pane",
		ForeachPane: &ForeachConfig{
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s' '${pane.role}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)
	mock := NewMockTmuxClient(
		tmux.Pane{ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Tags: []string{"role=investigator"}},
		tmux.Pane{ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentCodex, Tags: []string{"role=adjudicator"}},
		tmux.Pane{ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini, Tags: []string{"role=investigator"}},
	)
	e.SetTmuxClient(mock)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	var outputs []string
	for _, iter := range iterations {
		if iter.Skipped {
			t.Fatalf("iteration %d unexpectedly skipped: kind=%q reason=%q", iter.Index, iter.SkipKind, iter.SkipReason)
		}
		if len(iter.Results) != 1 {
			t.Fatalf("iteration %d results = %d, want 1", iter.Index, len(iter.Results))
		}
		outputs = append(outputs, iter.Results[0].Output)
	}
	if got, want := strings.Join(outputs, ","), "investigator,adjudicator,investigator"; got != want {
		t.Fatalf("outputs = %q, want %q", got, want)
	}
}

// bd-2ubxp.13: a foreach_pane filter narrows the iteration set to panes whose
// metadata satisfies the predicate; non-matching panes are recorded as
// SkipKindForeachFilter without dispatching the body.
func TestExecuteForeachPaneFilterByRoleIncludesOnlyMatchingPanes(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-pane-filter",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fanout_pane",
		ForeachPane: &ForeachConfig{
			Filter: `pane.role=="investigator"`,
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s' '${pane.id}'`,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)
	mock := NewMockTmuxClient(
		tmux.Pane{ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Tags: []string{"role=investigator"}},
		tmux.Pane{ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentCodex, Tags: []string{"role=adjudicator"}},
		tmux.Pane{ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini, Tags: []string{"role=investigator"}},
	)
	e.SetTmuxClient(mock)

	result := e.executeForeach(context.Background(), step, workflow)

	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}
	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	var dispatched []string
	skipped := 0
	for _, iter := range iterations {
		if iter.Skipped {
			skipped++
			if iter.SkipKind != SkipKindForeachFilter {
				t.Fatalf("iteration %d skip kind = %q, want %q", iter.Index, iter.SkipKind, SkipKindForeachFilter)
			}
			continue
		}
		if len(iter.Results) != 1 {
			t.Fatalf("iteration %d results = %d, want 1", iter.Index, len(iter.Results))
		}
		dispatched = append(dispatched, iter.Results[0].Output)
	}
	if skipped != 1 {
		t.Fatalf("skipped iterations = %d, want 1", skipped)
	}
	if got, want := strings.Join(dispatched, ","), "%1,%3"; got != want {
		t.Fatalf("dispatched outputs = %q, want %q", got, want)
	}
}

// TestExecuteForeach_ResumeSkipsCompletedIterations covers bd-qeatk:
// when a prior run completed some iterations and persisted their IDs in
// ForeachState.CompletedIterationIDs, the resumed executor must NOT
// re-dispatch those iterations — duplicating side effects (re-running
// commands, re-prompting agents) violates the resume contract that the
// foreach progress bookkeeping was added to enforce.
func TestExecuteForeach_ResumeSkipsCompletedIterations(t *testing.T) {
	tmpDir := t.TempDir()
	counterPath := tmpDir + "/iter-runs.txt"

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-resume",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items: `["a","b","c"]`,
			Steps: []Step{{
				ID:      "echo",
				Command: `printf '%s\n' '${item}' >> ` + counterPath,
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	// Simulate a prior run that completed iter0 — its iteration ID is in
	// CompletedIterationIDs and the body command must not run again.
	e.state.ForeachState = map[string]ForeachIterationState{
		"fanout": {
			StepID:                "fanout",
			Total:                 3,
			CompletedIterationIDs: []string{loopIterationID("fanout", 0)},
			CurrentIteration:      1,
		},
	}

	result := e.executeStepOnce(context.Background(), step, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}

	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 3 {
		t.Fatalf("iterations = %d, want 3", len(iterations))
	}
	if iterations[0].SkipKind != SkipKindResumeAlreadyCompleted {
		t.Fatalf("iter0 SkipKind = %q, want %q (resume must skip prior-completed iter)", iterations[0].SkipKind, SkipKindResumeAlreadyCompleted)
	}
	for i := 1; i < 3; i++ {
		if iterations[i].Skipped {
			t.Fatalf("iter%d unexpectedly skipped (SkipKind=%q); only iter0 should be resume-skipped", i, iterations[i].SkipKind)
		}
	}

	// The body command must have run exactly twice — once each for items b and c.
	body, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("counter recorded %d body runs (%v); want exactly 2 (iter1, iter2 — never iter0)", len(lines), lines)
	}
	for _, ln := range lines {
		if ln == "a" {
			t.Fatalf("counter contains 'a' from iter0 — the prior-completed iteration was re-dispatched (lines=%v)", lines)
		}
	}

	// ForeachState now records all three iterations as completed.
	st := e.state.ForeachState["fanout"]
	if len(st.CompletedIterationIDs) != 3 {
		t.Fatalf("CompletedIterationIDs = %v, want 3 entries after the resumed run", st.CompletedIterationIDs)
	}
}

// A foreach container's output_var is written by storeForeachOutputVars with the
// shape output_var_mode declares. The generic step-result path then overwrote it
// with result.Output — "Foreach completed: 2/2 dispatched, 0 skipped, 0 failed" —
// so output_var_mode was dead and downstream `${vars.x}` consumers read a status
// string instead of the collected data.
func TestStepOwnsForeachOutputVar(t *testing.T) {
	cases := map[string]struct {
		step *Step
		want bool
	}{
		"nil step":      {step: nil, want: false},
		"plain command": {step: &Step{ID: "run", OutputVar: "out"}, want: false},
		"foreach":       {step: &Step{ID: "each", Foreach: &ForeachConfig{}}, want: true},
		"foreach_pane":  {step: &Step{ID: "each", ForeachPane: &ForeachConfig{}}, want: true},
	}
	for name, tc := range cases {
		if got := stepOwnsForeachOutputVar(tc.step); got != tc.want {
			t.Errorf("%s: stepOwnsForeachOutputVar = %t, want %t", name, got, tc.want)
		}
	}
}

// storeForeachOutputVars writes the declared aggregate shape; the value must
// survive as that shape rather than being replaced by a summary string.
func TestForeachAggregateOutputVarKeepsDeclaredShape(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	e := &Executor{state: state}

	parent := &Step{ID: "each", OutputVar: "collected", Foreach: &ForeachConfig{}}
	state.Variables[parent.OutputVar] = []string{"a", "b"}

	// Simulate the generic step-result path running afterwards.
	summary := "Foreach completed: 2/2 dispatched, 0 skipped, 0 failed"
	e.storeForeachNestedResult(parent, StepResult{StepID: parent.ID, Status: StatusCompleted, Output: summary})

	got, ok := state.Variables[parent.OutputVar].([]string)
	if !ok {
		t.Fatalf("output_var type = %T (%v), want []string", state.Variables[parent.OutputVar], state.Variables[parent.OutputVar])
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("output_var = %v, want [a b]", got)
	}
	// The steps.<id>.output key still carries the summary for observability.
	if state.Variables["steps."+parent.ID+".output"] != summary {
		t.Fatalf("steps.%s.output = %v, want the summary line", parent.ID, state.Variables["steps."+parent.ID+".output"])
	}
}
