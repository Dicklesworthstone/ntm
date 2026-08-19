package pipeline

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type coverageRegressionStringer string

func (s coverageRegressionStringer) String() string {
	return "stringer:" + string(s)
}

type coverageRegressionFilterNode struct {
	value bool
	err   error
}

func (n coverageRegressionFilterNode) eval(FilterContext) (bool, error) {
	return n.value, n.err
}

func TestCoverageAggregateForeachErrorsCancelledAndFallbacks(t *testing.T) {
	cancelledOnly := aggregateForeachErrors([]foreachIterationResult{{
		Index: 2,
		Results: []StepResult{{
			StepID: "fanout_iter2_wait",
			Status: StatusCancelled,
			Error:  &StepError{Type: "cancelled", Message: "interrupted"},
		}},
	}}, "fanout", 3)
	if cancelledOnly == nil {
		t.Fatal("aggregateForeachErrors() = nil, want cancelled aggregate")
	}
	if got := len(cancelledOnly.Aggregated); got != 1 {
		t.Fatalf("cancelled aggregate entries = %d, want 1", got)
	}
	if !strings.Contains(cancelledOnly.Aggregated[0].Message, "iter-2: interrupted") {
		t.Fatalf("cancelled aggregate message = %q, want iteration prefix", cancelledOnly.Aggregated[0].Message)
	}
	var summaries []aggregatedErrorSummary
	if err := json.Unmarshal([]byte(cancelledOnly.Details), &summaries); err != nil {
		t.Fatalf("aggregate details are not JSON: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Iteration == nil || *summaries[0].Iteration != 2 || summaries[0].Type != "cancelled" {
		t.Fatalf("aggregate summaries = %#v, want cancelled iteration 2", summaries)
	}

	iterationErrorOnly := aggregateForeachErrors([]foreachIterationResult{{
		Index: 4,
		Error: "iteration body failed before result capture",
	}}, "fanout", 5)
	if iterationErrorOnly == nil || len(iterationErrorOnly.Aggregated) != 1 {
		t.Fatalf("iteration-error aggregate = %#v, want one entry", iterationErrorOnly)
	}
	if got := iterationErrorOnly.Aggregated[0].Type; got != "foreach_iteration" {
		t.Fatalf("iteration-error type = %q, want foreach_iteration", got)
	}
	if !strings.Contains(iterationErrorOnly.Message, "1 of 5 iterations failed") {
		t.Fatalf("iteration-error message = %q, want count", iterationErrorOnly.Message)
	}

	noEntries := aggregateForeachErrors(nil, "empty", 0)
	if noEntries == nil || noEntries.Type != "foreach" || len(noEntries.Aggregated) != 0 {
		t.Fatalf("empty aggregate = %#v, want generic foreach error without entries", noEntries)
	}
	if !strings.Contains(noEntries.Message, `foreach step "empty" failed`) {
		t.Fatalf("empty aggregate message = %q, want step failure", noEntries.Message)
	}
}

func TestForeachIterationAggregatesAllFailedBodySteps(t *testing.T) {
	// bd-5f60g: with on_error=continue, a foreach iteration may record
	// multiple failed body steps. The aggregator must surface all of them
	// in result.Error.Aggregated and result.Error.Details, not just the
	// first one — incident reports lose real failures otherwise.
	iteration := foreachIterationResult{
		Index: 0,
		Results: []StepResult{
			{
				StepID: "iter0_lint",
				Status: StatusFailed,
				Error:  &StepError{Type: "command", Message: "exit 7"},
			},
			{
				StepID: "iter0_format",
				Status: StatusCompleted,
			},
			{
				StepID: "iter0_test",
				Status: StatusFailed,
				Error:  &StepError{Type: "command", Message: "exit 1"},
			},
		},
	}

	got := aggregateForeachErrors([]foreachIterationResult{iteration}, "fanout", 1)
	if got == nil {
		t.Fatal("aggregateForeachErrors() = nil, want aggregate from two failed body steps")
	}
	if len(got.Aggregated) != 1 {
		t.Fatalf("len(Aggregated) = %d, want 1 outer iteration entry", len(got.Aggregated))
	}
	outer := got.Aggregated[0]
	if !strings.Contains(outer.Message, "iter-0:") {
		t.Fatalf("outer message = %q, want iter-0 prefix", outer.Message)
	}
	if !strings.Contains(outer.Message, "+1 more failed steps in iter-0") {
		t.Fatalf("outer message = %q, want '+1 more failed steps' suffix", outer.Message)
	}
	if len(outer.Aggregated) != 2 {
		t.Fatalf("outer.Aggregated len = %d, want 2 nested failures", len(outer.Aggregated))
	}
	wantMessages := []string{"iter-0: exit 7", "iter-0: exit 1"}
	for i, want := range wantMessages {
		if outer.Aggregated[i].Message != want {
			t.Errorf("outer.Aggregated[%d].Message = %q, want %q", i, outer.Aggregated[i].Message, want)
		}
	}
}

func TestCoverageStepResultAggregatedErrorClonesNestedErrors(t *testing.T) {
	original := StepError{
		Type: "command",
		Aggregated: []StepError{{
			Type: "parallel",
			Aggregated: []StepError{{
				Type:    "leaf",
				Message: "original leaf",
			}},
		}},
	}

	cloned := stepResultAggregatedError(StepResult{
		StepID: "nested",
		Status: StatusFailed,
		Error:  &original,
	})
	if cloned.Message != `step "nested" failed` {
		t.Fatalf("cloned message = %q, want fallback step message", cloned.Message)
	}
	cloned.Aggregated[0].Aggregated[0].Message = "mutated clone"
	if got := original.Aggregated[0].Aggregated[0].Message; got != "original leaf" {
		t.Fatalf("original nested message = %q, clone mutation leaked", got)
	}

	fromStatus := stepResultAggregatedError(StepResult{
		StepID: "cancelled",
		Status: StatusCancelled,
	})
	if fromStatus.Type != string(StatusCancelled) || fromStatus.Message != string(StatusCancelled) {
		t.Fatalf("status-derived error = %#v, want cancelled status message", fromStatus)
	}
}

func TestCoverageAggregateParallelErrorsEmptyAndStatusFallback(t *testing.T) {
	if got := aggregateParallelErrors([]StepResult{{StepID: "ok", Status: StatusCompleted}}, 1); got != nil {
		t.Fatalf("aggregateParallelErrors(completed) = %#v, want nil", got)
	}

	err := aggregateParallelErrors([]StepResult{
		{StepID: "ok", Status: StatusCompleted},
		{StepID: "failed_without_error", Status: StatusFailed},
	}, 2)
	if err == nil {
		t.Fatal("aggregateParallelErrors() = nil, want aggregate")
	}
	if len(err.Aggregated) != 1 || err.Aggregated[0].Type != string(StatusFailed) {
		t.Fatalf("parallel aggregate = %#v, want status-derived failed entry", err.Aggregated)
	}
	if !strings.Contains(err.Message, "1 of 2 parallel steps failed") {
		t.Fatalf("parallel aggregate message = %q, want count", err.Message)
	}
	if !strings.Contains(err.Details, "failed_without_error") {
		t.Fatalf("parallel aggregate details = %q, want failed step id", err.Details)
	}
}

func TestCoverageForeachPaneAssignmentAdditionalBranches(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, NTMIndex: 11, Type: tmux.AgentCodex, Variant: "codex", Tags: []string{"domain=api,backend", "model=codex"}},
		{ID: "%2", Index: 2, NTMIndex: 12, Type: tmux.AgentClaude, Variant: "opus", Tags: []string{"domain=docs", "model=claude"}},
		{ID: "%3", Index: 3, NTMIndex: 13, Type: tmux.AgentGemini, Variant: "gemini", Tags: []string{"domain=api", "excluded=true"}},
	}
	strategyPanes := foreachStrategyPanes(panes)
	if got, want := foreachPaneIDs(strategyPanes), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foreachPaneIDs() = %#v, want %#v", got, want)
	}

	paneID, paneIndex, paneVars, err := selectForeachPane("round_robin", strategyPanes, panes, nil, 3)
	if err != nil {
		t.Fatalf("round_robin assignment error = %v", err)
	}
	if paneID != "%2" || paneIndex != 2 || paneVars["model"] != "claude" {
		t.Fatalf("round_robin assignment = %q/%d/%#v, want %%2/index 2/claude", paneID, paneIndex, paneVars)
	}

	paneID, paneIndex, paneVars, err = selectForeachPane("round_robin_by_domain", strategyPanes, panes, map[string]interface{}{"domain": "api"}, 1)
	if err != nil {
		t.Fatalf("round_robin_by_domain assignment error = %v", err)
	}
	if paneID != "%1" || paneIndex != 1 || paneVars["domain"] != "api" {
		t.Fatalf("domain assignment = %q/%d/%#v, want %%1/index 1/api", paneID, paneIndex, paneVars)
	}

	paneID, _, _, err = selectForeachPane("by_model_family", strategyPanes, panes, map[string]string{"model": "claude"}, 0)
	if err != nil {
		t.Fatalf("by_model_family assignment error = %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("by_model_family assignment = %q, want %%2", paneID)
	}

	paneID, _, _, err = selectForeachPane("by_model_family_difference", strategyPanes, panes, map[string]interface{}{"model_family": "codex"}, 0)
	if err != nil {
		t.Fatalf("by_model_family_difference assignment error = %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("by_model_family_difference assignment = %q, want %%2", paneID)
	}

	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]interface{}{"pane_id": "%1"}, panes)
	if paneID != "%1" || paneIndex != 1 || paneVars["pane_id"] != "%1" {
		t.Fatalf("pane_id assignment = %q/%d/%#v, want known pane %%1", paneID, paneIndex, paneVars)
	}
	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]interface{}{"index": 12}, panes)
	if paneID != "%2" || paneIndex != 2 || paneVars["ntm_index"] != 12 {
		t.Fatalf("ntm index assignment = %q/%d/%#v, want pane %%2", paneID, paneIndex, paneVars)
	}
	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]string{"pane": "9"}, panes)
	if paneID != "%9" || paneIndex != 9 || paneVars["index"] != 9 {
		t.Fatalf("fallback pane assignment = %q/%d/%#v, want synthetic %%9", paneID, paneIndex, paneVars)
	}
	if paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]interface{}{"other": "x"}, panes); paneID != "" || paneIndex != 0 || paneVars != nil {
		t.Fatalf("empty pane assignment = %q/%d/%#v, want zero values", paneID, paneIndex, paneVars)
	}

	if _, _, _, err := selectForeachPane("not-a-strategy", strategyPanes, panes, nil, 0); err == nil {
		t.Fatal("unknown pane strategy error = nil, want error")
	}
}

func TestCoverageForeachItemExtractionHelpers(t *testing.T) {
	if got := foreachItemString("direct", "ignored"); got != "direct" {
		t.Fatalf("foreachItemString(string) = %q, want direct", got)
	}
	if got := foreachItemString(coverageRegressionStringer("value"), "ignored"); got != "stringer:value" {
		t.Fatalf("foreachItemString(Stringer) = %q, want stringer:value", got)
	}
	if got := foreachItemString(map[string]interface{}{"missing": "x", "id": 42}, "id"); got != "42" {
		t.Fatalf("foreachItemString(map interface) = %q, want 42", got)
	}
	if got := foreachItemString(map[string]string{"pane_id": "%7"}, "pane_id"); got != "%7" {
		t.Fatalf("foreachItemString(map string) = %q, want %%7", got)
	}
	if got := foreachItemString(99, "id"); got != "" {
		t.Fatalf("foreachItemString(int) = %q, want empty", got)
	}

	item := map[string]interface{}{
		"champions":  []interface{}{"%1", " ", "%2"},
		"fallback":   []string{"%3", "%4"},
		"adjudicate": "%5",
	}
	if got, want := foreachItemStrings(item, "champions", "fallback", "adjudicate"), []string{"%1", "%2", "%3", "%4", "%5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foreachItemStrings() = %#v, want %#v", got, want)
	}
	if got := foreachItemStrings("not-a-map", "champions"); len(got) != 0 {
		t.Fatalf("foreachItemStrings(non-map) = %#v, want empty", got)
	}

	if got := foreachItemInt(map[string]interface{}{"index": "12"}, "index"); got != 12 {
		t.Fatalf("foreachItemInt(map interface) = %d, want 12", got)
	}
	if got := foreachItemInt(map[string]string{"pane": "3"}, "pane"); got != 3 {
		t.Fatalf("foreachItemInt(map string) = %d, want 3", got)
	}
	if got := foreachItemInt(map[string]interface{}{"index": "NaN", "pane": ""}, "index", "pane"); got != 0 {
		t.Fatalf("foreachItemInt(invalid) = %d, want 0", got)
	}
}

func TestCoverageSubstituteForeachInterfaceValues(t *testing.T) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "coverage", Settings: DefaultWorkflowSettings()}
	e := createForeachTestExecutor(t, workflow)
	scope := e.pushForeachVars("item", map[string]interface{}{"name": "alpha"}, 2, 4, map[string]interface{}{"role": "reviewer"})
	defer e.popForeachVars(scope)

	substituted := substituteForeachInterfaceMap(e, map[string]interface{}{
		"prompt":  "${item.name}:${loop.index}:${pane.role}",
		"nested":  map[string]interface{}{"value": "${item.name}"},
		"list":    []interface{}{"${item.name}", 7},
		"strings": []string{"${item.name}", "${loop.count}"},
		"plain":   9,
	}, nil)
	if substituted["prompt"] != "alpha:2:reviewer" {
		t.Fatalf("prompt substitution = %#v, want alpha:2:reviewer", substituted["prompt"])
	}
	if got := substituted["nested"].(map[string]interface{})["value"]; got != "alpha" {
		t.Fatalf("nested substitution = %#v, want alpha", got)
	}
	if got := substituted["list"].([]interface{})[0]; got != "alpha" {
		t.Fatalf("list substitution = %#v, want alpha", got)
	}
	if got := substituted["strings"].([]string)[1]; got != "4" {
		t.Fatalf("string slice substitution = %#v, want 4", got)
	}
	if got := substituted["plain"]; got != 9 {
		t.Fatalf("plain value = %#v, want 9", got)
	}
	if got := substituteForeachInterfaceMap(e, nil, nil); got != nil {
		t.Fatalf("empty foreach map substitution = %#v, want nil", got)
	}

	protected := substituteForeachInterfaceValue(e, []interface{}{"${item.name}", "${pane.role}"}, map[string]struct{}{"item": {}})
	protectedValues := protected.([]interface{})
	if protectedValues[0] != "${item.name}" || protectedValues[1] != "reviewer" {
		t.Fatalf("protected substitution = %#v, want item preserved and pane resolved", protectedValues)
	}
}

func TestCoverageEvaluateForeachFilterAdditionalBranches(t *testing.T) {
	type filterStruct struct {
		UserName string
		Enabled  bool
		Count    int
	}
	ctx := FilterContext{
		Item: &filterStruct{UserName: "Ada", Enabled: true, Count: 2},
		Pane: map[string]string{"role": "reviewer", "ready": "yes"},
	}
	got, err := EvaluateForeachFilter(`user_name == "Ada" && enabled && count == "2" && pane.role != "author"`, ctx)
	if err != nil {
		t.Fatalf("EvaluateForeachFilter(struct) error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter(struct) = false, want true")
	}

	got, err = EvaluateForeachFilter(`false && missing == "x"`, FilterContext{})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter(false short-circuit) error = %v", err)
	}
	if got {
		t.Fatal("EvaluateForeachFilter(false short-circuit) = true, want false")
	}
	got, err = EvaluateForeachFilter(`true || missing == "x"`, FilterContext{})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter(true short-circuit) error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter(true short-circuit) = false, want true")
	}

	for _, expr := range []string{`"unterminated`, `(enabled`, `enabled &&`, `pane.`} {
		if _, err := EvaluateForeachFilter(expr, ctx); err == nil {
			t.Fatalf("EvaluateForeachFilter(%q) error = nil, want parse or binding error", expr)
		}
	}
}

func TestCoverageFilterPrimitiveHelpers(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value interface{}
		want  bool
	}{
		{name: "bool true", value: true, want: true},
		{name: "bool false", value: false, want: false},
		{name: "string true", value: "true", want: true},
		{name: "string false", value: "false", want: false},
		{name: "string zero", value: "0", want: false},
		{name: "string empty", value: "", want: false},
		{name: "int", value: 1, want: true},
		{name: "int zero", value: 0, want: false},
		{name: "int64", value: int64(1), want: true},
		{name: "int64 zero", value: int64(0), want: false},
		{name: "float64", value: 1.5, want: true},
		{name: "float64 zero", value: 0.0, want: false},
		{name: "nil", value: nil, want: false},
		{name: "struct", value: struct{}{}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterTruthy(tt.value); got != tt.want {
				t.Fatalf("filterTruthy(%#v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}

	for _, tt := range []struct {
		value interface{}
		want  float64
		ok    bool
	}{
		{value: 7, want: 7, ok: true},
		{value: int64(8), want: 8, ok: true},
		{value: float64(1.5), want: 1.5, ok: true},
		{value: float32(2.5), want: 2.5, ok: true},
		{value: "3.25", want: 3.25, ok: true},
		{value: "not-a-number", ok: false},
		{value: true, ok: false},
	} {
		got, ok := filterNumber(tt.value)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("filterNumber(%#v) = %v/%v, want %v/%v", tt.value, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCoverageFilterErrorBranches(t *testing.T) {
	if _, err := resolveFilterPart(map[int]string{1: "one"}, "1"); err == nil || !strings.Contains(err.Error(), "map key type") {
		t.Fatalf("resolveFilterPart(non-string map) error = %v, want key-type error", err)
	}
	var nilStruct *struct{ Name string }
	if _, err := resolveFilterPart(nilStruct, "Name"); err == nil || !strings.Contains(err.Error(), "nil value") {
		t.Fatalf("resolveFilterPart(nil pointer) error = %v, want nil value", err)
	}
	if _, err := resolveFilterPath(nil, "name", "name"); err == nil || !strings.Contains(err.Error(), "undefined filter variable") {
		t.Fatalf("resolveFilterPath(nil) error = %v, want undefined variable", err)
	}
	if _, err := resolveFilterBinding("missing", FilterContext{Item: map[string]interface{}{}, Pane: map[string]interface{}{}}); err == nil {
		t.Fatal("resolveFilterBinding(missing) error = nil, want error")
	}

	wantErr := errors.New("left failed")
	if _, err := (filterLogicalNode{op: "&&", left: coverageRegressionFilterNode{err: wantErr}, right: coverageRegressionFilterNode{value: true}}).eval(FilterContext{}); !errors.Is(err, wantErr) {
		t.Fatalf("logical left error = %v, want %v", err, wantErr)
	}
	if _, err := (filterLogicalNode{op: "xor", left: coverageRegressionFilterNode{value: true}, right: coverageRegressionFilterNode{value: true}}).eval(FilterContext{}); err == nil || !strings.Contains(err.Error(), "unknown filter logical operator") {
		t.Fatalf("unknown logical op error = %v, want unknown operator", err)
	}
	if _, err := (filterCompareNode{op: "<=>", left: filterOperand{value: "1"}, right: filterOperand{value: "1"}}).eval(FilterContext{}); err == nil || !strings.Contains(err.Error(), "unknown filter comparison operator") {
		t.Fatalf("unknown compare op error = %v, want unknown operator", err)
	}
}
