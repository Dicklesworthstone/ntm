package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Exercise the two distinct substitution boundaries: item values are frozen
// during materialization, while rounds and later step outputs become available
// only when the command consumes stdin. Already-sealed upstream contents must
// remain literal data through both boundaries.
func TestForeachNativeFieldsCommandInputUsesItemRoundAndLiteralData(t *testing.T) {
	workflow := &Workflow{Name: "native-input", Settings: DefaultWorkflowSettings()}
	e := createForeachTestExecutor(t, workflow)
	e.state.Variables["secret"] = "must-not-expand"
	parent := &Step{ID: "fanout"}
	config := &ForeachConfig{}
	body := []Step{{
		ID: "consume", Command: "cat", Stdin: "${item}|${round}|${steps.later.output}",
		OnSuccess: []Step{{ID: "audit", Command: "cat", Stdin: "hook:${item}:${round}"}},
	}}
	before, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	items := []string{"literal ${vars.secret}", "second item"}
	plans := make([][]Step, len(items))
	for i, item := range items {
		plans[i], err = e.materializeForeachSteps(parent, config, body, foreachIterationPlan{Index: i, Item: sealTerminalValue(item)}, len(items))
		if err != nil {
			t.Fatal(err)
		}
	}
	StoreStepOutput(e.state, "later", "downstream", nil)
	for i, item := range items {
		ctx := withRoundOverrides(context.Background(), buildRoundOverrides(i+1, 3))
		got, err := e.substituteVariablesStrictCtx(ctx, plans[i][0].Stdin)
		if err != nil {
			t.Fatalf("iteration %d command input: %v", i, err)
		}
		if want := fmt.Sprintf("%s|%d|downstream", item, i+1); got != want {
			t.Errorf("iteration %d input = %q, want %q", i, got, want)
		}
		got, err = e.substituteVariablesStrictCtx(ctx, plans[i][0].OnSuccess[0].Stdin)
		if err != nil {
			t.Fatalf("iteration %d success-hook input: %v", i, err)
		}
		if want := fmt.Sprintf("hook:%s:%d", item, i+1); got != want {
			t.Errorf("iteration %d hook input = %q, want %q", i, got, want)
		}
	}
	after, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("materializing command inputs changed the reusable workflow definition")
	}
}

func TestForeachNativeFieldsNestedLoopKeepsInnerBindings(t *testing.T) {
	workflow := &Workflow{Name: "nested-native-input", Settings: DefaultWorkflowSettings()}
	e := createForeachTestExecutor(t, workflow)
	parent := &Step{ID: "groups"}
	config := &ForeachConfig{As: "group"}
	body := []Step{{ID: "members", Loop: &LoopConfig{
		Items: "${group.members}", As: "item", Collect: "members_${group.name}",
		MaxIterations: IntOrExpr{Expr: "${group.limit}"},
		Steps:         []Step{{ID: "consume", Command: "cat", Stdin: "${group.name}/${item}/${loop.index}"}},
	}}}
	for i, name := range []string{"alpha", "beta"} {
		steps, err := e.materializeForeachSteps(parent, config, body, foreachIterationPlan{Index: i, Item: map[string]interface{}{
			"name": name, "members": []string{"one", "two"}, "limit": 2,
		}}, 2)
		if err != nil {
			t.Fatal(err)
		}
		loop := steps[0].Loop
		if loop.Items != `["one","two"]` || loop.Collect != "members_"+name || loop.MaxIterations.Expr != "2" {
			t.Fatalf("outer item did not configure its nested loop: %#v", loop)
		}
		if want := name + "/${item}/${loop.index}"; loop.Steps[0].Stdin != want {
			t.Fatalf("inner bindings were captured before the nested loop ran: got %q, want %q", loop.Steps[0].Stdin, want)
		}
		scope := CaptureVariableScope(e.state.Variables, loopScopeKeys("item")...)
		SetLoopVars(e.state, "item", "inner", 1, 2)
		got, err := e.substituteVariablesStrictCtx(context.Background(), loop.Steps[0].Stdin)
		if err != nil || got != name+"/inner/1" {
			t.Fatalf("inner execution input = %q, error = %v", got, err)
		}
		scope.Restore(e.state.Variables)
	}
	if body[0].Loop.Items != "${group.members}" || !strings.Contains(body[0].Loop.Steps[0].Stdin, "${group.name}") {
		t.Fatal("materialization changed the nested loop's source definition")
	}
}
