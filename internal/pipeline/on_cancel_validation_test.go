package pipeline

import (
	"strings"
	"testing"
)

// bd-tpz1a: a cleanup step under settings.on_cancel that shares its ID with a
// normal step would silently overwrite the cancelled step's persisted result
// inside runOnCancelSteps. Validate() must surface this as a duplicate-ID
// error before the workflow ever runs.
func TestValidate_OnCancelDuplicateIDWithNormalStep(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "dup-cancel",
		Settings: WorkflowSettings{
			OnCancel: []Step{
				{ID: "slow", Command: "echo cleanup"},
			},
		},
		Steps: []Step{
			{ID: "slow", Command: "sleep 30"},
		},
	}

	res := Validate(wf)
	if res.Valid {
		t.Fatalf("Validate() = valid, want duplicate-ID error")
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "slow") || strings.Contains(e.Field, "settings.on_cancel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Validate() errors did not flag the on_cancel/normal step duplicate: %+v", res.Errors)
	}
}

// bd-tpz1a: a cleanup step with no kind (no command/template/prompt/etc.)
// previously slipped past Validate and only failed during cancellation
// cleanup at runtime. The same step-kind rule must apply.
func TestValidate_OnCancelMalformedStepRequiresKind(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bad-cancel",
		Settings: WorkflowSettings{
			OnCancel: []Step{
				{ID: "cleanup"}, // no command/template/prompt
			},
		},
		Steps: []Step{
			{ID: "main", Command: "true"},
		},
	}

	res := Validate(wf)
	if res.Valid {
		t.Fatalf("Validate() = valid, want missing-kind error for on_cancel step")
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Field, "settings.on_cancel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Validate() errors did not flag the malformed on_cancel step: %+v", res.Errors)
	}
}

// bd-tpz1a: empty cleanup step IDs auto-assign as on_cancel_N at runtime, so
// validation must use the same synthetic identity. A normal step that uses
// "on_cancel_1" would otherwise collide silently.
func TestValidate_OnCancelSyntheticIDCollidesWithNormalStep(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "synth-collide",
		Settings: WorkflowSettings{
			OnCancel: []Step{
				{Command: "echo cleanup"}, // no ID — gets on_cancel_1
			},
		},
		Steps: []Step{
			{ID: "on_cancel_1", Command: "true"},
		},
	}

	res := Validate(wf)
	if res.Valid {
		t.Fatalf("Validate() = valid, want collision with synthetic on_cancel_1 ID")
	}
}

// bd-tpz1a: the happy path must keep working — a well-formed on_cancel step
// with a unique ID and a kind passes validation.
func TestValidate_OnCancelWellFormedPasses(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "ok-cancel",
		Settings: WorkflowSettings{
			OnCancel: []Step{
				{ID: "cleanup", Command: "echo bye"},
			},
		},
		Steps: []Step{
			{ID: "main", Command: "true"},
		},
	}

	res := Validate(wf)
	if !res.Valid {
		t.Fatalf("Validate() errors = %+v, want valid", res.Errors)
	}
}
