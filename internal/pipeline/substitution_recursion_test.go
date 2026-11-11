package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubstituteRecursiveReferences(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "${vars.y}",
			"y": "world",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("hello ${vars.x}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "hello world" {
		t.Fatalf("Substitute() = %q, want hello world", got)
	}
}

func TestSubstituteRecursionDepthExceeded(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "${vars.x}",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("loop ${vars.x}")
	if err == nil {
		t.Fatalf("Substitute() error = nil, got %q", got)
	}
	if !strings.Contains(err.Error(), "substitution recursion depth exceeded") {
		t.Fatalf("Substitute() error = %v, want recursion depth exceeded", err)
	}
	if got != "loop ${vars.x}" {
		t.Fatalf("Substitute() = %q, want unresolved self-reference", got)
	}
}

func TestSubstituteEscapedVariableReference(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "secret",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute(`echo \${vars.x}`)
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "echo ${vars.x}" {
		t.Fatalf("Substitute() = %q, want literal variable reference", got)
	}
}

func TestSubstituteEscapedDollar(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "ok",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute(`price \$5 ${vars.x}`)
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "price $5 ok" {
		t.Fatalf("Substitute() = %q, want escaped dollar restored", got)
	}
}

func TestSubstituteEscapedReferenceFromVariableValue(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": `\${vars.y}`,
			"y": "must-not-expand",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("${vars.x}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "${vars.y}" {
		t.Fatalf("Substitute() = %q, want escaped reference from value preserved", got)
	}
}

// bd-447se: a step output containing a literal ${env.SECRET_TOKEN} (or any
// other variable reference) MUST NOT be re-substituted on the next pass.
// Recursive expansion is a feature for trusted vars/defaults, but step
// output is external/untrusted data and must be terminal so it cannot
// disclose process environment secrets or alter control flow.
func TestSubstituteStepOutputDoesNotInjectEnvVariable(t *testing.T) {
	t.Setenv("FAKE_SECRET_FOR_BD_447SE", "leaked-value")
	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"A": {Output: "Result: ${env.FAKE_SECRET_FOR_BD_447SE}"},
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Used: ${steps.A.output}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if strings.Contains(got, "leaked-value") {
		t.Fatalf("Substitute() leaked env value through step output: %q", got)
	}
	if got != "Used: Result: ${env.FAKE_SECRET_FOR_BD_447SE}" {
		t.Fatalf("Substitute() = %q, want literal env reference preserved", got)
	}
}

// bd-447se: env values themselves are also untrusted (the operator's
// environment may contain attacker-controlled values via SSH agent
// forwarding etc.) and any ${...} in their text must stay literal.
func TestSubstituteEnvValueDoesNotRecurse(t *testing.T) {
	t.Setenv("BD_447SE_ENV_INJECTION", "${vars.secret}")
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"secret": "should-stay-hidden",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Use: ${env.BD_447SE_ENV_INJECTION}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if strings.Contains(got, "should-stay-hidden") {
		t.Fatalf("env value recursed into vars: %q", got)
	}
	if got != "Use: ${vars.secret}" {
		t.Fatalf("Substitute() = %q, want literal vars reference preserved", got)
	}
}

// bd-447se sanity check: trusted vars/defaults still recurse so the
// existing recursion contract is preserved (regression on
// TestSubstituteRecursiveReferences).
func TestSubstituteVarsStillRecurse(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "${vars.y}",
			"y": "deep",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("${vars.x}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "deep" {
		t.Fatalf("Substitute() = %q, want recursive vars to still resolve", got)
	}
}

// bd-yowuf: SetMaxDepth must override the package default so a workflow
// that configures `max_substitution_recursion: 32` actually gets 32 passes
// instead of being capped at DefaultMaxSubstitutionDepth (8).
func TestSubstituteHonorsConfiguredMaxDepth(t *testing.T) {
	// 12 levels deep — exceeds the default of 8 but under a 32 limit.
	vars := map[string]interface{}{
		"l00": "leaf",
	}
	for i := 1; i <= 11; i++ {
		vars[chainKey(i)] = "${vars." + chainKey(i-1) + "}"
	}
	state := &ExecutionState{Variables: vars}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetMaxDepth(32)

	got, err := sub.Substitute("${vars.l11}")
	if err != nil {
		t.Fatalf("Substitute() error = %v with MaxDepth=32", err)
	}
	if got != "leaf" {
		t.Fatalf("Substitute() = %q, want %q", got, "leaf")
	}
}

// bd-yowuf: a non-positive limit must fall back to DefaultMaxSubstitutionDepth
// (the user-configurable knob is opt-in; zero means "use the default").
func TestSubstituteFallsBackToDefaultWhenMaxDepthUnset(t *testing.T) {
	vars := map[string]interface{}{
		"l00": "leaf",
	}
	for i := 1; i <= 11; i++ {
		vars[chainKey(i)] = "${vars." + chainKey(i-1) + "}"
	}
	state := &ExecutionState{Variables: vars}
	sub := NewSubstitutor(state, "sess", "wf")

	_, err := sub.Substitute("${vars.l11}")
	if err == nil {
		t.Fatalf("Substitute() error = nil, want recursion depth exceeded")
	}
	want := "substitution recursion depth exceeded after 8 passes"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Substitute() error = %v, want substring %q", err, want)
	}
}

// bd-yowuf: the depth-exceeded error message should reference the effective
// limit, not the hardcoded constant. A workflow that lowered the limit to 4
// must see "after 4 passes" so debug output matches the configured knob.
func TestSubstituteErrorMessageReflectsConfiguredLimit(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{"x": "${vars.x}"},
	}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetMaxDepth(4)

	_, err := sub.Substitute("${vars.x}")
	if err == nil {
		t.Fatalf("Substitute() error = nil, want recursion error")
	}
	want := "substitution recursion depth exceeded after 4 passes"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Substitute() error = %v, want substring %q", err, want)
	}
}

// Runtime loop aliases are a data boundary too. A bead/step value can flow
// through foreach as ${item.*} / ${loop.item.*}; preserving the terminal
// fence here prevents that indirection from re-enabling recursive expansion.
func TestSubstituteLoopItemDoesNotInjectEnvVariable(t *testing.T) {
	t.Setenv("BD_447SE_LOOP_SECRET", "must-not-leak")
	item := map[string]interface{}{"title": "${env.BD_447SE_LOOP_SECRET}"}
	state := &ExecutionState{Variables: map[string]interface{}{
		"loop.item": item,
		"loop.task": item,
	}}
	sub := NewSubstitutor(state, "sess", "wf")

	for _, template := range []string{"${item.title}", "${loop.item.title}", "${loop.task.title}"} {
		got, err := sub.Substitute(template)
		if err != nil {
			t.Fatalf("Substitute(%q) error = %v", template, err)
		}
		if got != "${env.BD_447SE_LOOP_SECRET}" {
			t.Fatalf("Substitute(%q) = %q, want literal env reference", template, got)
		}
		if strings.Contains(got, "must-not-leak") {
			t.Fatalf("Substitute(%q) leaked env value: %q", template, got)
		}
	}
}

func TestSubstituteLoopItemKeepsAlreadyResolvedScalarValues(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{
		"loop.item":  "ready",
		"loop.index": 2,
	}}
	sub := NewSubstitutor(state, "sess", "wf")
	got, err := sub.Substitute("item=${loop.item} index=${loop.index}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "item=ready index=2" {
		t.Fatalf("Substitute() = %q, want ordinary loop values preserved", got)
	}
}

func TestSubstituteOutputVarAliasPreservesTerminalProvenance(t *testing.T) {
	t.Setenv("BD_447SE_ALIAS_SECRET", "must-not-leak")
	state := &ExecutionState{Variables: map[string]interface{}{}}
	storeOutputVar(state, "answer", "literal ${env.BD_447SE_ALIAS_SECRET}", map[string]interface{}{"text": "${env.BD_447SE_ALIAS_SECRET}"})

	if got := state.Variables["answer"]; got != "literal ${env.BD_447SE_ALIAS_SECRET}" {
		t.Fatalf("raw output_var value mutated: %q", got)
	}
	for _, template := range []string{"${vars.answer}", "${vars.answer_parsed.text}"} {
		sub := NewSubstitutor(state, "sess", "wf")
		got, err := sub.Substitute(template)
		if err != nil { t.Fatalf("Substitute(%q): %v", template, err) }
		if strings.Contains(got, "must-not-leak") { t.Fatalf("output_var alias leaked env value: %q", got) }
	}

	raw, err := json.Marshal(state)
	if err != nil { t.Fatal(err) }
	var restored ExecutionState
	if err := json.Unmarshal(raw, &restored); err != nil { t.Fatal(err) }
	got, err := NewSubstitutor(&restored, "sess", "wf").Substitute("${vars.answer}")
	if err != nil { t.Fatal(err) }
	if got != "literal ${env.BD_447SE_ALIAS_SECRET}" {
		t.Fatalf("restored output_var provenance lost: %q", got)
	}
}

func chainKey(i int) string {
	if i < 10 {
		return "l0" + string(rune('0'+i))
	}
	return "l1" + string(rune('0'+i-10))
}
