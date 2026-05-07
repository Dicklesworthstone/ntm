package pipeline

import (
	"strings"
	"testing"
)

// bd-ctxcf: parallel sub-steps that share output_var must not declare
// conflicting non-empty output_var_mode values. parallelGroupOutputVarMode
// silently picks the first non-empty mode in declaration order, so
// reordering the steps would silently change storage shape (map vs string
// vs list). Validation rejects the conflict so the workflow author has to
// pick one explicitly.
func TestValidate_ConflictingParallelOutputVarModesRejected(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "conflict",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "p", Parallel: ParallelSpec{Steps: []Step{
				{ID: "left", Prompt: "L", OutputVar: "shared", OutputVarMode: OutputVarModeCollect},
				{ID: "right", Prompt: "R", OutputVar: "shared", OutputVarMode: OutputVarModeLast},
			}}},
		},
	}

	res := Validate(wf)
	if res.Valid {
		t.Fatalf("Validate() = valid, want conflicting-mode error")
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "conflicting output_var_mode") &&
			strings.Contains(e.Message, "shared") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Validate() errors = %+v, want conflicting-mode error mentioning 'shared'", res.Errors)
	}
}

// bd-ctxcf: a single non-empty mode (others empty) must NOT trigger the
// conflict check — the empty-mode sub-steps inherit the explicit one.
func TestValidate_SingleNonEmptyParallelOutputVarModeAllowed(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "ok-mode",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "p", Parallel: ParallelSpec{Steps: []Step{
				{ID: "left", Prompt: "L", OutputVar: "shared", OutputVarMode: OutputVarModeCollect},
				{ID: "right", Prompt: "R", OutputVar: "shared"},
			}}},
		},
	}

	res := Validate(wf)
	if !res.Valid {
		t.Fatalf("Validate() errors = %+v, want valid (only one explicit mode)", res.Errors)
	}
}

// bd-ctxcf: identical non-empty modes across the group must also pass —
// no conflict, just the existing duplicate-output_var warning.
func TestValidate_IdenticalParallelOutputVarModesAllowed(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "same-mode",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "p", Parallel: ParallelSpec{Steps: []Step{
				{ID: "left", Prompt: "L", OutputVar: "shared", OutputVarMode: OutputVarModeCollect},
				{ID: "right", Prompt: "R", OutputVar: "shared", OutputVarMode: OutputVarModeCollect},
			}}},
		},
	}

	res := Validate(wf)
	if !res.Valid {
		t.Fatalf("Validate() errors = %+v, want valid (matching modes)", res.Errors)
	}
}

// bd-ctxcf: the helper returns nil when there is at most one distinct
// non-empty mode, and a sorted list of distinct modes when there is more
// than one.
func TestConflictingParallelOutputVarModes_HelperBehavior(t *testing.T) {
	steps := []Step{
		{ID: "a", OutputVarMode: OutputVarModeCollect},
		{ID: "b", OutputVarMode: OutputVarModeLast},
		{ID: "c"},
		{ID: "d", OutputVarMode: OutputVarModeAggregate},
	}
	got := conflictingParallelOutputVarModes(steps, []int{0, 1, 2, 3})
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 distinct modes", got)
	}
	want := []string{"aggregate", "collect", "last"}
	for i, m := range want {
		if got[i] != m {
			t.Fatalf("got[%d] = %q, want %q (sorted)", i, got[i], m)
		}
	}

	if conflictingParallelOutputVarModes(steps, []int{0, 2}) != nil {
		t.Fatalf("single explicit mode should return nil")
	}
	if conflictingParallelOutputVarModes(steps, []int{2}) != nil {
		t.Fatalf("only-empty mode should return nil")
	}
}
