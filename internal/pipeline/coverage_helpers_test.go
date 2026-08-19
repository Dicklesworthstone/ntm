package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type coverageHelperStringer string

func (s coverageHelperStringer) String() string {
	return string(s)
}

type coverageFilterItem struct {
	Role    string
	Score   int
	Enabled bool
}

func TestCoverageHelpersForeachPaneAssignmentHelpers(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, NTMIndex: 11, Type: tmux.AgentCodex, Variant: "codex", Tags: []string{"model=codex", "domain=api,db"}},
		{ID: "%2", Index: 2, NTMIndex: 12, Type: tmux.AgentClaude, Variant: "opus", Tags: []string{"model=opus", "domain=docs"}},
		{ID: "%3", Index: 3, NTMIndex: 13, Type: tmux.AgentGemini, Variant: "gemini", Tags: []string{"excluded=true", "model=gemini"}},
	}
	strategyPanes := foreachStrategyPanes(panes)

	if ids := strings.Join(foreachPaneIDs(strategyPanes), ","); ids != "%1,%2" {
		t.Fatalf("available pane IDs = %q, want %%1,%%2", ids)
	}

	paneID, paneIndex, paneVars, err := selectForeachPane("round_robin_by_domain", strategyPanes, panes, map[string]interface{}{"domain": "docs"}, 0)
	if err != nil {
		t.Fatalf("round_robin_by_domain error: %v", err)
	}
	if paneID != "%2" || paneIndex != 2 || paneVars["domain"] != "docs" {
		t.Fatalf("domain pane = (%q,%d,%v), want %%2/2/docs", paneID, paneIndex, paneVars)
	}

	paneID, _, _, err = selectForeachPane("rotate_adjudicator", strategyPanes, panes, map[string]interface{}{
		"champions": []interface{}{"%1"},
	}, 0)
	if err != nil {
		t.Fatalf("rotate_adjudicator error: %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("adjudicator pane = %q, want %%2", paneID)
	}

	paneID, _, _, err = selectForeachPane("by_model_family", strategyPanes, panes, map[string]interface{}{"model_family": "codex"}, 0)
	if err != nil {
		t.Fatalf("by_model_family error: %v", err)
	}
	if paneID != "%1" {
		t.Fatalf("model pane = %q, want %%1", paneID)
	}

	paneID, _, _, err = selectForeachPane("by_model_family_difference", strategyPanes, panes, map[string]interface{}{"model": "codex"}, 0)
	if err != nil {
		t.Fatalf("by_model_family_difference error: %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("different-model pane = %q, want %%2", paneID)
	}

	if _, _, _, err := selectForeachPane("does_not_exist", strategyPanes, panes, nil, 0); err == nil {
		t.Fatal("unknown strategy error = nil")
	}

	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]interface{}{"ntm_index": "12"}, panes)
	if paneID != "%2" || paneIndex != 2 || paneVars["ntm_index"] != 12 {
		t.Fatalf("ntm-index assignment = (%q,%d,%v), want %%2/2/ntm_index=12", paneID, paneIndex, paneVars)
	}

	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]string{"pane": "44"}, nil)
	if paneID != "%44" || paneIndex != 44 || paneVars["index"] != 44 {
		t.Fatalf("fallback pane assignment = (%q,%d,%v), want %%44/44", paneID, paneIndex, paneVars)
	}

	paneID, paneIndex, paneVars = paneAssignmentFromItem(map[string]interface{}{"pane_id": "%missing"}, panes)
	if paneID != "%missing" || paneIndex != 0 || paneVars["pane_id"] != "%missing" {
		t.Fatalf("missing pane assignment = (%q,%d,%v), want fallback vars", paneID, paneIndex, paneVars)
	}
}

func TestCoverageHelpersForeachItemHelpers(t *testing.T) {
	if got := foreachItemString("direct", "id"); got != "direct" {
		t.Fatalf("string item = %q, want direct", got)
	}
	if got := foreachItemString(coverageHelperStringer("from-stringer"), "id"); got != "from-stringer" {
		t.Fatalf("stringer item = %q, want from-stringer", got)
	}
	if got := foreachItemString(map[string]interface{}{"title": "fallback"}, "id", "title"); got != "fallback" {
		t.Fatalf("map interface string = %q, want fallback", got)
	}
	if got := foreachItemString(map[string]string{"id": "H-1"}, "id"); got != "H-1" {
		t.Fatalf("map string = %q, want H-1", got)
	}
	if got := foreachItemString(123, "id"); got != "" {
		t.Fatalf("unsupported string item = %q, want empty", got)
	}

	item := map[string]interface{}{
		"champions": []string{"%1", "%2"},
		"also":      []interface{}{" %3 ", "", "%4"},
		"single":    "%5",
	}
	if got := strings.Join(foreachItemStrings(item, "champions", "also", "single"), ","); got != "%1,%2,%3,%4,%5" {
		t.Fatalf("item strings = %q, want all non-empty values", got)
	}
	if got := foreachItemInt(map[string]interface{}{"bad": "x", "index": "7"}, "bad", "index"); got != 7 {
		t.Fatalf("item int = %d, want 7", got)
	}
	if got := foreachItemInt(map[string]string{"pane": "9"}, "pane"); got != 9 {
		t.Fatalf("string map item int = %d, want 9", got)
	}
	if got := foreachItemInt(map[string]string{"pane": "nope"}, "pane"); got != 0 {
		t.Fatalf("invalid item int = %d, want 0", got)
	}
}

// bd-rab6y: foreachItemString must skip blank values so fallback alias keys
// take effect. Strategies like by_model_family pass alias chains such as
// (model_family, model, family, type) — when the canonical key is present
// but empty, routing previously returned "" instead of the populated alias.
func TestForeachItemString_SkipsBlankAliasValues(t *testing.T) {
	cases := []struct {
		name string
		item interface{}
		keys []string
		want string
	}{
		{
			name: "interface map: empty canonical falls through to alias",
			item: map[string]interface{}{"model_family": "", "model": "codex"},
			keys: []string{"model_family", "model", "family", "type"},
			want: "codex",
		},
		{
			name: "interface map: whitespace-only treated as blank",
			item: map[string]interface{}{"model_family": "   ", "model": "claude"},
			keys: []string{"model_family", "model"},
			want: "claude",
		},
		{
			name: "interface map: domain alias chain",
			item: map[string]interface{}{"domain": "", "id": "task-42"},
			keys: []string{"domain", "id"},
			want: "task-42",
		},
		{
			name: "string map: empty canonical falls through to alias",
			item: map[string]string{"model_family": "", "model": "gemini"},
			keys: []string{"model_family", "model"},
			want: "gemini",
		},
		{
			name: "all blank yields empty",
			item: map[string]interface{}{"model_family": "", "model": ""},
			keys: []string{"model_family", "model"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foreachItemString(tc.item, tc.keys...); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoverageHelpersForeachBodyAndSubstitutionHelpers(t *testing.T) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "coverage", Settings: DefaultWorkflowSettings()}
	executor := createForeachTestExecutor(t, workflow)
	executor.state.Variables["name"] = "Ada"
	executor.state.Variables["alias"] = map[string]interface{}{"id": "A-1"}

	body, err := foreachBodySteps(&Step{ID: "body"}, &ForeachConfig{Body: []Step{{ID: "from_body"}}})
	if err != nil {
		t.Fatalf("body alias error: %v", err)
	}
	if len(body) != 1 || body[0].ID != "from_body" {
		t.Fatalf("body steps = %#v, want copied body", body)
	}

	body, err = foreachBodySteps(&Step{ID: "templated", Agent: "codex", OutputVar: "out"}, &ForeachConfig{
		Template: "MO.md",
		Params:   map[string]interface{}{"NAME": "${vars.name}"},
	})
	if err != nil {
		t.Fatalf("template body error: %v", err)
	}
	if body[0].ID != "template" || body[0].Template != "MO.md" || body[0].OutputVar != "out" {
		t.Fatalf("template body = %#v, want synthesized template step", body[0])
	}
	if _, err := foreachBodySteps(&Step{ID: "empty"}, &ForeachConfig{}); err == nil {
		t.Fatal("empty foreach body error = nil")
	}

	got := substituteForeachInterfaceMap(executor, map[string]interface{}{
		"alias": "${alias.id}",
		"kept":  "${item.id}",
	}, map[string]struct{}{"item": {}})
	if got["alias"] != "A-1" || got["kept"] != "${item.id}" {
		t.Fatalf("substituteForeachInterfaceMap = %#v, want alias resolved and item protected", got)
	}
}

func TestCoverageHelpersFilterHelperBranches(t *testing.T) {
	ctx := FilterContext{
		Item: coverageFilterItem{Role: "lead", Score: 3, Enabled: true},
		Pane: map[string]interface{}{"model": "opus", "healthy": "true"},
	}
	ok, err := EvaluateForeachFilter(`enabled && role == lead && (score == 3 || pane.model != "opus")`, ctx)
	if err != nil {
		t.Fatalf("filter eval error: %v", err)
	}
	if !ok {
		t.Fatal("filter eval = false, want true")
	}

	if _, err := (filterLogicalNode{op: "??", left: filterTruthyNode{operand: filterOperand{value: "true"}}}).eval(ctx); err == nil {
		t.Fatal("unknown logical op error = nil")
	}
	if _, err := (filterCompareNode{op: "??", left: filterOperand{value: "1"}, right: filterOperand{value: "1"}}).eval(ctx); err == nil {
		t.Fatal("unknown compare op error = nil")
	}

	if _, err := resolveFilterPart(map[int]string{1: "one"}, "1"); err == nil {
		t.Fatal("non-string map key error = nil")
	}
	if _, err := resolveFilterPart((*coverageFilterItem)(nil), "role"); err == nil {
		t.Fatal("nil pointer path error = nil")
	}
	if _, err := resolveFilterPart(coverageFilterItem{}, "missing"); err == nil {
		t.Fatal("missing struct field error = nil")
	}
	if got := normalizeFilterField("Model_Family"); got != "modelfamily" {
		t.Fatalf("normalized field = %q, want modelfamily", got)
	}

	for _, value := range []interface{}{true, "yes", 1, int64(1), float64(1), struct{}{}} {
		if !filterTruthy(value) {
			t.Fatalf("filterTruthy(%#v) = false, want true", value)
		}
	}
	for _, value := range []interface{}{false, "", "false", "0", 0, int64(0), float64(0), nil} {
		if filterTruthy(value) {
			t.Fatalf("filterTruthy(%#v) = true, want false", value)
		}
	}
	for _, value := range []interface{}{int64(2), float32(2), "2.5"} {
		if _, ok := filterNumber(value); !ok {
			t.Fatalf("filterNumber(%#v) not recognized", value)
		}
	}
	if _, ok := filterNumber(errors.New("no")); ok {
		t.Fatal("filterNumber(error) recognized unexpectedly")
	}
}

func TestCoverageHelpersErrorAggregationHelpers(t *testing.T) {
	cancelled := foreachIterationResult{
		Index: 4,
		Results: []StepResult{{
			StepID: "cancelled",
			Status: StatusCancelled,
			Error:  &StepError{Type: "cancelled", Message: "context cancelled", Timestamp: time.Now()},
		}},
	}
	entry, ok := foreachIterationAnyError(cancelled)
	if !ok {
		t.Fatal("foreachIterationAnyError did not report cancelled result")
	}
	if !strings.Contains(entry.Message, "iter-4") {
		t.Fatalf("cancelled entry message = %q, want iteration prefix", entry.Message)
	}

	plain := stepResultAggregatedError(StepResult{StepID: "plain", Status: StatusFailed})
	if plain.Type != string(StatusFailed) || !strings.Contains(plain.Message, "failed") {
		t.Fatalf("plain aggregated error = %#v, want failed fallback", plain)
	}

	nested := StepError{Type: "outer", Message: "outer", Aggregated: []StepError{{Type: "inner", Message: "inner"}}}
	cloned := cloneStepErrorValue(nested)
	cloned.Aggregated[0].Message = "changed"
	if nested.Aggregated[0].Message != "inner" {
		t.Fatalf("clone mutated original nested error: %#v", nested.Aggregated)
	}

	if got := aggregateParallelErrors(nil, 0); got != nil {
		t.Fatalf("empty parallel aggregate = %#v, want nil", got)
	}
	if got := aggregateForeachErrors(nil, "fanout", 0); got == nil || got.Type != "foreach" {
		t.Fatalf("empty foreach aggregate = %#v, want fallback foreach error", got)
	}
}

func TestCoverageHelpersForeachErrorFormattingHelpers(t *testing.T) {
	failed := finishForeachFailure(StepResult{StepID: "fanout"}, "source", "missing source")
	if failed.Status != StatusFailed || failed.Error == nil || failed.Error.Type != "source" {
		t.Fatalf("finishForeachFailure = %#v, want source failure", failed)
	}

	if got := resultErrorMessage(StepResult{Error: &StepError{Message: "explicit"}}); got != "explicit" {
		t.Fatalf("resultErrorMessage explicit = %q", got)
	}
	if got := resultErrorMessage(StepResult{SkipReason: "skip"}); got != "skip" {
		t.Fatalf("resultErrorMessage skip = %q", got)
	}
	if got := resultErrorMessage(StepResult{Status: StatusCancelled}); got != string(StatusCancelled) {
		t.Fatalf("resultErrorMessage status = %q", got)
	}
	if got := resultErrorMessage(StepResult{}); got != "unknown error" {
		t.Fatalf("resultErrorMessage empty = %q", got)
	}
}

func TestCoverageHelpersSelectForeachPaneNoAvailablePanes(t *testing.T) {
	_, _, _, err := selectForeachPane("round_robin", []paneStrategyPane{{ID: "%1", Excluded: true}}, nil, nil, -10)
	if !errors.Is(err, errNoPaneForStrategy) {
		t.Fatalf("round_robin unavailable error = %v, want errNoPaneForStrategy", err)
	}

	_, err = byModelFamily(nil, "")
	if !errors.Is(err, errNoModelFamilyPane) {
		t.Fatalf("empty model family error = %v, want errNoModelFamilyPane", err)
	}

	_, err = rotateAdjudicator([]string{"%1"}, []string{"%1"}, nil)
	if !errors.Is(err, errNoAdjudicatorPane) {
		t.Fatalf("all champions error = %v, want errNoAdjudicatorPane", err)
	}

	paneID, warn, err := byModelFamilyDifference([]paneStrategyPane{{ID: "%1", ModelFamily: "codex"}}, "codex")
	if err != nil || !warn || paneID != "%1" {
		t.Fatalf("model-family fallback = (%q,%v,%v), want %%1/warn/nil", paneID, warn, err)
	}

	if got := fmt.Sprint(foreachStrategyPanes(nil)); got != "[]" {
		t.Fatalf("empty strategy panes = %s, want []", got)
	}
}
