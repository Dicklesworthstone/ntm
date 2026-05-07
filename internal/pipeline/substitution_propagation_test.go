package pipeline

import (
	"context"
	"strings"
	"testing"
)

// bd-bhcz7: a command step that references an unset env var must fail with a
// clear substitution error instead of running the literal placeholder text
// through /bin/sh -c (which yields shell-specific bad-substitution errors and
// leaks the unresolved reference into output).
func TestExecuteCommand_FailsOnMissingEnvSubstitution(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := &Step{ID: "missing-env", Command: "echo ${env.NTM_BD_BHCZ7_DEFINITELY_UNSET}"}

	result := e.executeCommand(context.Background(), step, &Workflow{Name: "test"})

	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want %q (output=%q error=%+v)", result.Status, StatusFailed, result.Output, result.Error)
	}
	if result.Error == nil {
		t.Fatalf("error = nil, want substitution error")
	}
	if result.Error.Type != "substitution" {
		t.Fatalf("error.Type = %q, want %q", result.Error.Type, "substitution")
	}
	if !strings.Contains(result.Error.Message, "NTM_BD_BHCZ7_DEFINITELY_UNSET") {
		t.Fatalf("error.Message = %q, want substring referencing the missing env var", result.Error.Message)
	}
}

// bd-bhcz7: args entries with unresolved env references must fail the step
// before the command shell runs, so secrets-style placeholders never leak as
// literal text into the child process environment.
func TestExecuteCommand_FailsOnMissingEnvSubstitutionInArgs(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := &Step{
		ID:      "missing-env-arg",
		Command: "true",
		Args: map[string]interface{}{
			"TOKEN": "${env.NTM_BD_BHCZ7_ARG_DEFINITELY_UNSET}",
		},
	}

	result := e.executeCommand(context.Background(), step, &Workflow{Name: "test"})

	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, StatusFailed)
	}
	if result.Error == nil {
		t.Fatalf("error = nil, want substitution error")
	}
	if result.Error.Type != "substitution" {
		t.Fatalf("error.Type = %q, want %q", result.Error.Type, "substitution")
	}
	if !strings.Contains(result.Error.Message, "args") {
		t.Fatalf("error.Message = %q, want indication that args triggered the failure", result.Error.Message)
	}
}

// bd-bhcz7: the strict variant must still succeed for fully resolved
// references — regression check that we didn't break the happy path.
func TestExecuteCommand_ResolvedEnvSucceeds(t *testing.T) {
	t.Setenv("NTM_BD_BHCZ7_DEFINED", "ok")
	e := newCommandTestExecutor(t)
	step := &Step{ID: "ok-env", Command: "echo ${env.NTM_BD_BHCZ7_DEFINED}"}

	result := e.executeCommand(context.Background(), step, &Workflow{Name: "test"})

	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q (output=%q error=%+v)", result.Status, StatusCompleted, result.Output, result.Error)
	}
	if !strings.Contains(result.Output, "ok") {
		t.Fatalf("output = %q, want substring %q", result.Output, "ok")
	}
}

// bd-bhcz7: substituteVariablesStrict must surface SubstitutionError for the
// recursion-depth case as well, not just missing env vars.
func TestSubstituteVariablesStrict_PropagatesRecursionError(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.state.Variables["self"] = "${vars.self}"

	_, err := e.substituteVariablesStrict("${vars.self}")
	if err == nil {
		t.Fatalf("substituteVariablesStrict returned nil error, want recursion-depth error")
	}
	if !strings.Contains(err.Error(), "recursion depth exceeded") {
		t.Fatalf("err = %v, want substring 'recursion depth exceeded'", err)
	}
}
