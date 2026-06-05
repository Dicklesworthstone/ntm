package pipeline

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// TestForeachOutputVarMode_AggregateDefault verifies that a foreach over N
// items with output_var: result and no explicit mode lands on
// []string{item0, item1, ...} in iteration order — the bd-dg38m regression.
// Pre-fix, the per-iteration storeForeachNestedResult writes overwrote
// each other and only the last writer survived.
func TestForeachOutputVarMode_AggregateDefault(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-aggregate-default",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:        "fanout",
		OutputVar: "result",
		Foreach: &ForeachConfig{
			Items: `["alpha","beta","gamma"]`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}

	value, ok := e.state.Variables["result"]
	if !ok {
		t.Fatalf("vars[result] not set after foreach")
	}
	slice, ok := value.([]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want []string", value)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(slice, want) {
		t.Fatalf("vars[result] = %#v, want %#v", slice, want)
	}
}

// TestForeachOutputVarMode_AggregateParallelKeepsAllIterations is the
// concrete bd-dg38m bug: parallel foreach over N items previously raced
// through e.state.Variables[step.OutputVar] and only one winner remained.
// After the fix, every completed iteration contributes one slice entry
// and the entries are in iteration order regardless of completion order.
func TestForeachOutputVarMode_AggregateParallelKeepsAllIterations(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-aggregate-parallel",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:        "fanout",
		OutputVar: "result",
		Foreach: &ForeachConfig{
			Items:    `["a","b","c","d","e"]`,
			Parallel: true,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}

	slice, ok := e.state.Variables["result"].([]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want []string", e.state.Variables["result"])
	}
	want := []string{"a", "b", "c", "d", "e"}
	if !reflect.DeepEqual(slice, want) {
		t.Fatalf("vars[result] = %#v, want %#v (iteration order, not completion order)", slice, want)
	}
}

// TestForeachOutputVarMode_CollectKeyedByItemIdentity covers the bead's
// bead/pair/debate iteration shape — items have stable "id" fields, so
// collect-mode should produce a map[string]string keyed by those ids.
func TestForeachOutputVarMode_CollectKeyedByItemIdentity(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-collect",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:            "fanout",
		OutputVar:     "result",
		OutputVarMode: OutputVarModeCollect,
		Foreach: &ForeachConfig{
			Items: `[{"id":"first","payload":"one"},{"id":"second","payload":"two"}]`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item.payload}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}

	asMap, ok := e.state.Variables["result"].(map[string]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want map[string]string", e.state.Variables["result"])
	}
	if asMap["first"] != "one" || asMap["second"] != "two" {
		t.Fatalf("vars[result] = %#v, want {first:one,second:two}", asMap)
	}
}

// TestForeachOutputVarMode_CollectFallsBackToIterIndex verifies the
// foreachIterationKey fallback: when item is opaque (no id/key/name field
// and not a string) the key becomes "iter_<index>" so map entries stay
// distinct.
func TestForeachOutputVarMode_CollectFallsBackToIterIndex(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-collect-fallback",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:            "fanout",
		OutputVar:     "result",
		OutputVarMode: OutputVarModeCollect,
		Foreach: &ForeachConfig{
			Items: `[{"label":"x"},{"label":"y"}]`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item.label}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}
	asMap, ok := e.state.Variables["result"].(map[string]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want map[string]string", e.state.Variables["result"])
	}
	keys := make([]string, 0, len(asMap))
	for k := range asMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"iter_0", "iter_1"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("collect keys = %v, want %v", keys, want)
	}
}

// TestForeachOutputVarMode_LastKeepsScalarSemantics verifies that explicit
// last-mode preserves the historic last-writer-wins shape (scalar string
// in vars[output_var]) rather than wrapping in a slice. Sequential
// foreach lands on the final iteration deterministically.
func TestForeachOutputVarMode_LastKeepsScalarSemantics(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-last",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:            "fanout",
		OutputVar:     "result",
		OutputVarMode: OutputVarModeLast,
		Foreach: &ForeachConfig{
			Items: `["alpha","beta","gamma"]`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}
	value, ok := e.state.Variables["result"].(string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want string", e.state.Variables["result"])
	}
	if value != "gamma" {
		t.Fatalf("vars[result] = %q, want gamma (sequential last)", value)
	}
}

// TestForeachOutputVarMode_ConfigLevelMode covers the
// ForeachConfig.OutputVarMode entry of the cascade —
// effectiveForeachOutputVarMode prefers it over the implicit aggregate
// default when Step.OutputVarMode is unset.
func TestForeachOutputVarMode_ConfigLevelMode(t *testing.T) {
	parent := &Step{OutputVarMode: ""}
	cfg := &ForeachConfig{OutputVarMode: OutputVarModeCollect}
	if got := effectiveForeachOutputVarMode(parent, cfg); got != OutputVarModeCollect {
		t.Fatalf("effectiveForeachOutputVarMode = %q, want %q", got, OutputVarModeCollect)
	}

	parent = &Step{OutputVarMode: OutputVarModeLast}
	if got := effectiveForeachOutputVarMode(parent, cfg); got != OutputVarModeLast {
		t.Fatalf("step mode should win over config: got %q, want %q", got, OutputVarModeLast)
	}

	parent = &Step{OutputVarMode: ""}
	cfg = &ForeachConfig{OutputVarMode: ""}
	if got := effectiveForeachOutputVarMode(parent, cfg); got != OutputVarModeAggregate {
		t.Fatalf("default mode = %q, want %q", got, OutputVarModeAggregate)
	}
}

// TestForeachOutputVarMode_SkippedIterationsExcluded verifies that
// filter-excluded iterations contribute no entry to the aggregate — the
// "no result" semantics noted in storeForeachOutputVars.
func TestForeachOutputVarMode_SkippedIterationsExcluded(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-skip-excluded",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:        "fanout",
		OutputVar: "result",
		Foreach: &ForeachConfig{
			Items:  `[{"id":"keep1","keep":"yes"},{"id":"drop","keep":"no"},{"id":"keep2","keep":"yes"}]`,
			Filter: `item.keep == "yes"`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item.id}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}
	slice, ok := e.state.Variables["result"].([]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want []string", e.state.Variables["result"])
	}
	want := []string{"keep1", "keep2"}
	if !reflect.DeepEqual(slice, want) {
		t.Fatalf("vars[result] = %#v, want %#v", slice, want)
	}
}

func TestForeachOutputVarMode_ResumeCompletedIterationsContributePriorOutput(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-resume-output-var",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID:        "fanout",
		OutputVar: "result",
		Foreach: &ForeachConfig{
			Items: `["alpha","beta","gamma"]`,
			Steps: []Step{{
				ID:        "echo",
				Command:   `printf '%s' '${item}'`,
				OutputVar: "result",
			}},
		},
	}
	workflow.Steps = []Step{*step}
	e := createForeachTestExecutor(t, workflow)
	e.state.ForeachState = map[string]ForeachIterationState{
		"fanout": {
			StepID:                "fanout",
			Total:                 3,
			CompletedIterationIDs: []string{loopIterationID("fanout", 0)},
			CurrentIteration:      1,
		},
	}
	e.state.Steps["fanout_iter0_echo"] = StepResult{
		StepID: "fanout_iter0_echo",
		Status: StatusCompleted,
		Output: "alpha",
	}

	got := e.executeForeach(context.Background(), step, workflow)
	if got.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", got.Status, got.Error)
	}

	value, ok := e.state.Variables["result"]
	if !ok {
		t.Fatalf("vars[result] not set after foreach resume")
	}
	slice, ok := value.([]string)
	if !ok {
		t.Fatalf("vars[result] type = %T, want []string", value)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(slice, want) {
		t.Fatalf("vars[result] = %#v, want %#v", slice, want)
	}

	iterations, ok := got.ParsedData.([]foreachIterationResult)
	if !ok || len(iterations) != 3 {
		t.Fatalf("iterations = %#v, want 3 foreach iteration results", got.ParsedData)
	}
	if iterations[0].SkipKind != SkipKindResumeAlreadyCompleted {
		t.Fatalf("iter0 SkipKind = %q, want %q", iterations[0].SkipKind, SkipKindResumeAlreadyCompleted)
	}
}
