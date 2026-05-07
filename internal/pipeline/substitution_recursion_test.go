package pipeline

import (
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
