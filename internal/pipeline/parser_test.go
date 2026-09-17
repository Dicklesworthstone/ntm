package pipeline

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFile_YAML(t *testing.T) {

	content := `
schema_version: "2.0"
name: test-workflow
description: A test workflow
steps:
  - id: step1
    agent: claude
    prompt: Do something
`
	// Create temp file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	if w.Name != "test-workflow" {
		t.Errorf("expected name 'test-workflow', got %q", w.Name)
	}
	if w.SchemaVersion != "2.0" {
		t.Errorf("expected schema_version '2.0', got %q", w.SchemaVersion)
	}
	if len(w.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(w.Steps))
	}
	if w.Steps[0].ID != "step1" {
		t.Errorf("expected step id 'step1', got %q", w.Steps[0].ID)
	}
}

func TestParseFile_TOML(t *testing.T) {

	content := `
schema_version = "2.0"
name = "test-workflow"

[[steps]]
id = "step1"
agent = "claude"
prompt = "Do something"
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	if w.Name != "test-workflow" {
		t.Errorf("expected name 'test-workflow', got %q", w.Name)
	}
	if len(w.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(w.Steps))
	}
}

func TestParseFile_UnsupportedExtension(t *testing.T) {

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(path)
	if err == nil {
		t.Error("expected error for unsupported extension")
	}
}

func TestParseFile_InvalidYAML(t *testing.T) {

	content := `
schema_version: "2.0"
name: test
steps:
  - id: step1
  invalid yaml here
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseFile_FileNotFound(t *testing.T) {

	_, err := ParseFile("/nonexistent/path/workflow.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseString_YAML(t *testing.T) {

	content := `
schema_version: "2.0"
name: inline-test
steps:
  - id: s1
    agent: codex
    prompt: test
`
	w, err := ParseString(content, "yaml")
	if err != nil {
		t.Fatalf("ParseString failed: %v", err)
	}

	if w.Name != "inline-test" {
		t.Errorf("expected name 'inline-test', got %q", w.Name)
	}
}

// bd-oqv4c: YAML with a foreach body step that uses loop_control as its
// only kind (plus a when guard) must parse and validate. Earlier validation
// rejected it with "step must have prompt, prompt_file, command, ...".
func TestParseString_YAML_LoopControlOnlyForeachBody(t *testing.T) {
	content := `
schema_version: "2.0"
name: loop-control-yaml
steps:
  - id: outer
    foreach:
      items: '["a","b","c"]'
      as: row
      steps:
        - id: break_on_c
          loop_control: break
          when: '${item} == "c"'
        - id: real_work
          command: 'printf %s "${item}"'
`
	w, err := ParseString(content, "yaml")
	if err != nil {
		t.Fatalf("ParseString failed: %v", err)
	}
	result := Validate(w)
	if !result.Valid {
		t.Fatalf("Validate() rejected loop_control-only step: %+v", result.Errors)
	}
	// Sanity-check the parsed loop_control kind made it through round-trip.
	body := w.Steps[0].Foreach.Steps
	if len(body) != 2 {
		t.Fatalf("expected 2 body steps, got %d", len(body))
	}
	if body[0].LoopControl != LoopControlBreak {
		t.Fatalf("body[0].LoopControl = %q, want break", body[0].LoopControl)
	}
}

func TestParseString_TOML(t *testing.T) {

	content := `
schema_version = "2.0"
name = "inline-test"

[[steps]]
id = "s1"
agent = "codex"
prompt = "test"
`
	w, err := ParseString(content, "toml")
	if err != nil {
		t.Fatalf("ParseString failed: %v", err)
	}

	if w.Name != "inline-test" {
		t.Errorf("expected name 'inline-test', got %q", w.Name)
	}
}

func TestValidate_MissingSchemaVersion(t *testing.T) {

	w := &Workflow{
		Name: "test",
		Steps: []Step{
			{ID: "s1", Prompt: "test"},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for missing schema_version")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "schema_version" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error for schema_version field")
	}
}

func TestValidate_MissingName(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Steps: []Step{
			{ID: "s1", Prompt: "test"},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for missing name")
	}
}

func TestValidate_NoSteps(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps:         []Step{},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for no steps")
	}
}

func TestValidate_DuplicateStepIDs(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1", Prompt: "test1"},
			{ID: "s1", Prompt: "test2"},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for duplicate step IDs")
	}
}

func TestValidate_InvalidStepID(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "step with spaces", Prompt: "test"},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for invalid step ID")
	}
}

func TestValidate_MissingPromptAndParallel(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1"}, // No prompt or parallel
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for missing prompt/parallel")
	}
}

func TestValidate_BothPromptAndParallel(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:       "s1",
				Prompt:   "test",
				Parallel: ParallelSpec{Steps: []Step{{ID: "p1", Prompt: "parallel"}}},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for both prompt and parallel")
	}
}

func TestValidate_StepKindMutualExclusivityAndRequiredWork(t *testing.T) {
	tests := []struct {
		name          string
		step          Step
		wantValid     bool
		wantErrSubstr string
	}{
		{
			name:          "prompt and command conflict",
			step:          Step{ID: "s1", Prompt: "ask an agent", Command: "echo no"},
			wantErrSubstr: "both prompt and command",
		},
		{
			name:          "prompt and template conflict",
			step:          Step{ID: "s1", Prompt: "ask an agent", Template: "MO-test.md"},
			wantErrSubstr: "both prompt and template",
		},
		{
			name:          "command and template conflict",
			step:          Step{ID: "s1", Command: "echo no", Template: "MO-test.md"},
			wantErrSubstr: "both command and template",
		},
		{
			name: "prompt and parallel steps conflict",
			step: Step{
				ID:     "s1",
				Prompt: "ask an agent",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "parallel_inner", Prompt: "parallel work"},
				}},
			},
			wantErrSubstr: "both prompt and parallel",
		},
		{
			name:      "prompt only is valid",
			step:      Step{ID: "s1", Prompt: "ask an agent"},
			wantValid: true,
		},
		{
			name:      "command only is valid",
			step:      Step{ID: "s1", Command: "echo ok"},
			wantValid: true,
		},
		{
			name:      "template only is valid",
			step:      Step{ID: "s1", Template: "MO-test.md"},
			wantValid: true,
		},
		{
			name: "foreach only is valid",
			step: Step{
				ID: "s1",
				Foreach: &ForeachConfig{
					Items: "[\"a\"]",
					Steps: []Step{{ID: "foreach_inner", Prompt: "per item"}},
				},
			},
			wantValid: true,
		},
		{
			name: "branch with branches map is valid",
			step: Step{
				ID:     "s1",
				Branch: "echo yes",
				Branches: map[string]interface{}{
					"yes": map[string]interface{}{"command": "echo selected"},
				},
			},
			wantValid: true,
		},
		{
			// bd-ytp3e: parallel foreach launches every iteration
			// concurrently, so loop_control: break inside the body cannot
			// reliably stop later iterations from completing with side
			// effects. Lint must reject the combination.
			name: "parallel foreach with loop_control break is invalid",
			step: Step{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items:    `["a","b"]`,
					Parallel: true,
					Steps: []Step{
						{ID: "break_now", LoopControl: LoopControlBreak, When: `${item} == "a"`},
						{ID: "work", Command: "echo ${item}"},
					},
				},
			},
			wantErrSubstr: "loop_control: break is not supported inside a parallel foreach",
		},
		{
			// bd-ytp3e: sequential foreach is fine — break can short-circuit
			// the per-iteration loop deterministically.
			name: "sequential foreach with loop_control break is valid",
			step: Step{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items: `["a","b"]`,
					Steps: []Step{
						{ID: "break_now", LoopControl: LoopControlBreak, When: `${item} == "a"`},
						{ID: "work", Command: "echo ${item}"},
					},
				},
			},
			wantValid: true,
		},
		{
			// bd-nz63w: a branches map alone has no predicate so the runtime
			// cannot pick a branch; lint must reject it instead of silently
			// passing and failing later with a missing-prompt error.
			name: "branches without branch predicate is invalid",
			step: Step{
				ID: "s1",
				Branches: map[string]interface{}{
					"default": map[string]interface{}{"command": "echo fallback"},
				},
			},
			wantErrSubstr: "step has branches but no branch predicate",
		},
		{
			// bd-nz63w: a branch predicate with no branches map has nothing
			// to select and would error at runtime; lint should catch it.
			name:          "branch predicate without branches map is invalid",
			step:          Step{ID: "s1", Branch: "echo yes"},
			wantErrSubstr: "step has branch predicate but no branches map",
		},
		{
			name:          "empty step has no work",
			step:          Step{ID: "s1"},
			wantErrSubstr: "must have prompt, prompt_file, command, template, parallel, loop, foreach, branch, bead_query, or loop_control",
		},
		{
			// bd-oqv4c: loop_control-only steps are valid; the runtime
			// supports `{loop_control: break/continue, when: ...}` as
			// pure control guards inside foreach/loop bodies.
			name:      "loop_control break only is valid",
			step:      Step{ID: "s1", LoopControl: LoopControlBreak},
			wantValid: true,
		},
		{
			name:      "loop_control continue only is valid",
			step:      Step{ID: "s1", LoopControl: LoopControlContinue, When: `${item} == "skip"`},
			wantValid: true,
		},
		{
			name: "bead_query only is valid",
			step: Step{
				ID:        "s1",
				BeadQuery: &BeadQueryStep{Label: StringOrList{"hypothesis"}},
			},
			wantValid: true,
		},
		{
			name: "loop with until only is valid",
			step: Step{
				ID: "s1",
				Loop: &LoopConfig{
					Until: "test -f done",
					Steps: []Step{{ID: "loop_inner", Prompt: "loop body"}},
				},
			},
			wantValid: true,
		},
		{
			name: "loop without items while until or times fails",
			step: Step{
				ID: "s1",
				Loop: &LoopConfig{
					Steps: []Step{{ID: "loop_inner", Prompt: "loop body"}},
				},
			},
			wantErrSubstr: "loop must specify items, while, until, or times",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, logs := validateWithCapturedSlog(t, workflowWithSingleStep(tt.step))
			if logs != "" {
				t.Fatalf("Validate emitted slog output for parser-only validation: %s", logs)
			}
			if result.Valid != tt.wantValid {
				t.Fatalf("Validate().Valid = %v, want %v; errors: %+v", result.Valid, tt.wantValid, result.Errors)
			}
			if tt.wantErrSubstr == "" {
				return
			}
			if !validationErrorContains(result, tt.wantErrSubstr) {
				t.Fatalf("Validate() errors = %+v, want message containing %q", result.Errors, tt.wantErrSubstr)
			}
		})
	}
}

func workflowWithSingleStep(step Step) *Workflow {
	return &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "test",
		Steps:         []Step{step},
	}
}

func validateWithCapturedSlog(t *testing.T, w *Workflow) (ValidationResult, string) {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	result := Validate(w)
	return result, buf.String()
}

func validationErrorContains(result ValidationResult, substr string) bool {
	for _, err := range result.Errors {
		if strings.Contains(err.Message, substr) {
			return true
		}
	}
	return false
}

func TestValidate_MultipleAgentSelectionMethods(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "claude",
				Pane:   PaneSpec{Index: 1},
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for multiple agent selection methods")
	}
}

func TestValidate_InvalidRoute(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Route:  "invalid-strategy",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for invalid route")
	}
}

func TestValidate_InvalidErrorAction(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:      "s1",
				Prompt:  "test",
				OnError: "invalid",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for invalid on_error")
	}
}

func TestValidate_AllowsFailFastErrorAction(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:      "s1",
				Prompt:  "test",
				OnError: ErrorActionFailFast,
			},
		},
	}

	result := Validate(w)
	if !result.Valid {
		t.Fatalf("expected validation to pass for fail_fast, got errors: %+v", result.Errors)
	}
}

func TestValidate_InvalidWorkflowSettingsErrorAction(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Settings: WorkflowSettings{
			OnError: ErrorAction("invalid"),
		},
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Fatal("expected validation to fail for invalid settings.on_error")
	}
	if len(result.Errors) == 0 || result.Errors[0].Field != "settings.on_error" {
		t.Fatalf("expected settings.on_error validation error, got %+v", result.Errors)
	}
}

func TestValidate_RetryWithZeroCount(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:         "s1",
				Prompt:     "test",
				OnError:    ErrorActionRetry,
				RetryCount: 0,
			},
		},
	}

	result := Validate(w)
	// Should produce warning, not error
	if !result.Valid {
		t.Error("expected validation to pass (with warning)")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning for retry with zero count")
	}
}

func TestValidate_CircularDependency(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1", Prompt: "test", DependsOn: []string{"s2"}},
			{ID: "s2", Prompt: "test", DependsOn: []string{"s1"}},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for circular dependency")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "depends_on" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error for depends_on field")
	}
}

func TestValidate_CycleWithExternalDependency(t *testing.T) {

	// This tests the bug where a node depending on a cycle member
	// was incorrectly reported as part of a cycle
	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "a", Prompt: "test", DependsOn: []string{"b"}}, // Part of cycle
			{ID: "b", Prompt: "test", DependsOn: []string{"a"}}, // Part of cycle
			{ID: "c", Prompt: "test", DependsOn: []string{"a"}}, // Depends on cycle, but NOT part of cycle
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for circular dependency")
	}

	// Should have exactly 1 cycle error (a -> b -> a), not 2
	cycleErrors := 0
	for _, e := range result.Errors {
		if e.Field == "depends_on" {
			cycleErrors++
		}
	}
	if cycleErrors != 1 {
		t.Errorf("expected exactly 1 cycle error, got %d", cycleErrors)
	}
}

func TestValidate_CycleInLoopSubsteps(t *testing.T) {

	// This tests that cycles within loop sub-steps are detected
	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "loop_step",
				Loop: &LoopConfig{
					Items: "items",
					As:    "item",
					Steps: []Step{
						{ID: "inner_a", Prompt: "test", DependsOn: []string{"inner_b"}}, // Part of cycle
						{ID: "inner_b", Prompt: "test", DependsOn: []string{"inner_a"}}, // Part of cycle
					},
				},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for circular dependency in loop sub-steps")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "depends_on" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error for depends_on field in loop sub-steps")
	}
}

func TestValidate_ValidWorkflow(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "valid-workflow",
		Description:   "A valid workflow",
		Steps: []Step{
			{
				ID:     "design",
				Agent:  "claude",
				Prompt: "Design the API",
			},
			{
				ID:        "implement",
				Agent:     "codex",
				Prompt:    "Implement the API",
				DependsOn: []string{"design"},
			},
		},
	}

	result := Validate(w)
	if !result.Valid {
		t.Errorf("expected validation to pass, got errors: %v", result.Errors)
	}
}

func TestValidate_ParallelSteps(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "parallel-workflow",
		Steps: []Step{
			{
				ID: "parallel_work",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "p1", Agent: "claude", Prompt: "Task 1"},
					{ID: "p2", Agent: "codex", Prompt: "Task 2"},
				}},
			},
			{
				ID:        "combine",
				Agent:     "claude",
				Prompt:    "Combine results",
				DependsOn: []string{"parallel_work"},
			},
		},
	}

	result := Validate(w)
	if !result.Valid {
		t.Errorf("expected validation to pass, got errors: %v", result.Errors)
	}
}

func TestValidate_UnknownAgentType(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1", Agent: "unknown-agent", Prompt: "test"},
		},
	}

	result := Validate(w)
	// Should produce warning, not error
	if !result.Valid {
		t.Error("expected validation to pass (with warning)")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown agent type")
	}
}

func TestValidate_InvalidWaitCondition(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1", Prompt: "test", Wait: "invalid-wait"},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for invalid wait condition")
	}
}

func TestValidate_LoopWithMissingItems(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "s1",
				Loop: &LoopConfig{
					As:    "item",
					Steps: []Step{{ID: "inner", Prompt: "test"}},
				},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for loop without items")
	}
}

func TestValidate_LoopNegativeMaxIterations(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "s1",
				Loop: &LoopConfig{
					Items:         "${vars.list}",
					As:            "item",
					MaxIterations: IntOrExpr{Value: -1},
					Steps:         []Step{{ID: "inner", Prompt: "test"}},
				},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for negative max_iterations")
	}
}

func TestValidate_VariableReferences(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "Process ${vars.name} with ${unknown.ref}",
			},
		},
	}

	result := Validate(w)
	// Should produce warning for unknown reference type
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown variable reference type")
	}
}

func TestValidate_VariableReferencesInLoopSubsteps(t *testing.T) {

	// This tests that variable references in loop sub-steps are validated
	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "loop_step",
				Loop: &LoopConfig{
					Items: "items",
					As:    "item",
					Steps: []Step{
						{ID: "inner", Prompt: "Process ${unknown.ref}"},
					},
				},
			},
		},
	}

	result := Validate(w)
	// Should produce warning for unknown reference type in loop sub-step
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown variable reference in loop sub-step")
	}

	// Check that the field path includes loop.steps
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Field, "loop.steps") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning field to contain 'loop.steps'")
	}
}

func TestValidate_VariableReferencesInWhenCondition(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "test",
				When:   "${unknown.ref} == true",
			},
		},
	}

	result := Validate(w)
	// Should produce warning for unknown reference type in when condition
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown variable reference in when condition")
	}

	// Check that the field path includes .when
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Field, ".when") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning field to contain '.when'")
	}
}

func TestValidate_VariableReferencesInParallelSubsteps(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "par_step",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "inner", Prompt: "Process ${unknown.ref}"},
				}},
			},
		},
	}

	result := Validate(w)
	// Should produce warning for unknown reference type in parallel sub-step
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown variable reference in parallel sub-step")
	}

	// Check that the field path includes .parallel
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Field, ".parallel") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning field to contain '.parallel'")
	}
}

func TestLoadAndValidate(t *testing.T) {

	content := `
schema_version: "2.0"
name: test-workflow
steps:
  - id: s1
    agent: claude
    prompt: test
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w, result, err := LoadAndValidate(path)
	if err != nil {
		t.Fatalf("LoadAndValidate failed: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected valid workflow, got errors: %v", result.Errors)
	}
	if w.Name != "test-workflow" {
		t.Errorf("expected name 'test-workflow', got %q", w.Name)
	}
}

func TestIsValidID(t *testing.T) {

	tests := []struct {
		id    string
		valid bool
	}{
		{"valid_id", true},
		{"valid-id", true},
		{"ValidID123", true},
		{"step1", true},
		{"s1", true},
		{"", false},
		{"with spaces", false},
		{"with.dots", false},
		{"with/slashes", false},
		{"with@symbol", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := isValidID(tt.id)
			if got != tt.valid {
				t.Errorf("isValidID(%q) = %v, want %v", tt.id, got, tt.valid)
			}
		})
	}
}

func TestNormalizeAgentType(t *testing.T) {

	tests := []struct {
		input    string
		expected string
	}{
		// Lowercase (canonical)
		{"claude", "claude"},
		{"cc", "claude"},
		{"claude-code", "claude"},
		{" claude_code ", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"openai", "codex"},
		{"openai-codex", "codex"},
		{" codex-cli ", "codex"},
		{"OH_MY_PI", "omp"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"google", "gemini"},
		{"google-gemini", "gemini"},
		{" gemini_cli ", "gemini"},
		{"cursor", "cursor"},
		{"ws", "windsurf"},
		{" Windsurf ", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
		{"unknown", "unknown"},
		// Case-insensitive handling
		{"Claude", "claude"},
		{"CLAUDE", "claude"},
		{"CC", "claude"},
		{"Codex", "codex"},
		{"CODEX", "codex"},
		{"Gemini", "gemini"},
		{"GEMINI", "gemini"},
		{"Unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeAgentType(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeAgentType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsValidAgentType(t *testing.T) {

	tests := []struct {
		input    string
		expected bool
	}{
		// Valid types (lowercase)
		{"claude", true},
		{"cc", true},
		{"claude-code", true},
		{" claude_code ", true},
		{"codex", true},
		{"cod", true},
		{"openai", true},
		{"openai-codex", true},
		{" codex-cli ", true},
		{"gemini", true},
		{"gmi", true},
		{"google", true},
		{"google-gemini", true},
		{" gemini_cli ", true},
		{"cursor", true},
		{"ws", true},
		{"windsurf", true},
		{"aider", true},
		{"ollama", true},
		{"omp", true},
		{"oh-my-pi", true},
		// Case-insensitive handling
		{"Claude", true},
		{"CLAUDE", true},
		{"CC", true},
		{"Codex", true},
		{"Gemini", true},
		// Invalid types
		{"unknown", false},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsValidAgentType(tt.input)
			if got != tt.expected {
				t.Errorf("IsValidAgentType(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseError_Error(t *testing.T) {

	tests := []struct {
		err      ParseError
		expected string
	}{
		{
			ParseError{Message: "simple error"},
			"simple error",
		},
		{
			ParseError{File: "test.yaml", Message: "file error"},
			"test.yaml: file error",
		},
		{
			ParseError{File: "test.yaml", Line: 10, Message: "line error"},
			"test.yaml:line 10: line error",
		},
		{
			ParseError{File: "test.yaml", Line: 10, Column: 7, Message: "column error"},
			"test.yaml:line 10, column 7: column error",
		},
		{
			ParseError{Field: "steps[0].id", Message: "field error"},
			"steps[0].id: field error",
		},
		{
			ParseError{File: "test.yaml", Line: 5, Field: "name", Message: "full error"},
			"test.yaml:line 5:name: full error",
		},
	}

	for _, tt := range tests {
		got := tt.err.Error()
		if got != tt.expected {
			t.Errorf("ParseError.Error() = %q, want %q", got, tt.expected)
		}
	}
}

func TestIsValidPath(t *testing.T) {

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "valid simple path",
			path: "workflow.yaml",
			want: true,
		},
		{
			name: "valid path with directory",
			path: "workflows/myworkflow.yaml",
			want: true,
		},
		{
			name: "valid absolute path",
			path: "/home/user/workflows/myworkflow.yaml",
			want: true,
		},
		{
			name: "valid path with spaces",
			path: "my workflow.yaml",
			want: true,
		},
		{
			name: "empty path",
			path: "",
			want: false,
		},
		{
			name: "path with null byte",
			path: "workflow\x00.yaml",
			want: false,
		},
		{
			name: "path with null byte at start",
			path: "\x00workflow.yaml",
			want: false,
		},
		{
			name: "path with null byte at end",
			path: "workflow.yaml\x00",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidPath(tt.path)
			if got != tt.want {
				t.Errorf("isValidPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsValidRoute(t *testing.T) {

	tests := []struct {
		route RoutingStrategy
		want  bool
	}{
		{RouteLeastLoaded, true},
		{RouteFirstAvailable, true},
		{RouteRoundRobin, true},
		{RoutingStrategy("unknown"), false},
		{RoutingStrategy(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.route), func(t *testing.T) {
			got := isValidRoute(tt.route)
			if got != tt.want {
				t.Errorf("isValidRoute(%q) = %v, want %v", tt.route, got, tt.want)
			}
		})
	}
}

func TestIsValidErrorAction(t *testing.T) {

	tests := []struct {
		action ErrorAction
		want   bool
	}{
		{ErrorActionFail, true},
		{ErrorActionFailFast, true},
		{ErrorActionContinue, true},
		{ErrorActionRetry, true},
		{ErrorAction("unknown"), false},
		{ErrorAction(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			got := isValidErrorAction(tt.action)
			if got != tt.want {
				t.Errorf("isValidErrorAction(%q) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

func TestIsValidWaitCondition(t *testing.T) {

	tests := []struct {
		cond WaitCondition
		want bool
	}{
		{WaitCompletion, true},
		{WaitIdle, true},
		{WaitTime, true},
		{WaitNone, true},
		{WaitCondition("unknown"), false},
		{WaitCondition(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.cond), func(t *testing.T) {
			got := isValidWaitCondition(tt.cond)
			if got != tt.want {
				t.Errorf("isValidWaitCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestParseString_UnsupportedFormat(t *testing.T) {

	_, err := ParseString("{}", "json")
	if err == nil {
		t.Error("expected error for unsupported format")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if !strings.Contains(pe.Message, "unsupported format") {
		t.Errorf("expected 'unsupported format' in message, got %q", pe.Message)
	}
	if pe.Hint != "Use 'yaml' or 'toml'" {
		t.Errorf("expected hint about yaml/toml, got %q", pe.Hint)
	}
}

func TestParseString_InvalidYAML(t *testing.T) {

	content := `
name: test
steps:
  - id: step1
  invalid yaml here: [
`
	_, err := ParseString(content, "yaml")
	if err == nil {
		t.Error("expected error for invalid YAML")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if !strings.Contains(pe.Message, "YAML parse error") {
		t.Errorf("expected 'YAML parse error' in message, got %q", pe.Message)
	}
	if pe.Line == 0 {
		t.Errorf("expected line number in YAML parse error, got %+v", pe)
	}
	if pe.Hint == "" || !strings.Contains(pe.Hint, "docs/WORKFLOW_SCHEMA.md") {
		t.Errorf("expected schema-doc hint, got %q", pe.Hint)
	}
}

func TestParseString_InvalidTOML(t *testing.T) {

	content := `
name = "test"
invalid toml [here
`
	_, err := ParseString(content, "toml")
	if err == nil {
		t.Error("expected error for invalid TOML")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if !strings.Contains(pe.Message, "TOML parse error") {
		t.Errorf("expected 'TOML parse error' in message, got %q", pe.Message)
	}
	if pe.Line == 0 || pe.Column == 0 {
		t.Errorf("expected line and column in TOML parse error, got %+v", pe)
	}
	if pe.Hint == "" {
		t.Errorf("expected TOML parse hint, got empty")
	}
}

func TestParseString_YMLFormat(t *testing.T) {

	content := `
schema_version: "2.0"
name: yml-test
steps:
  - id: s1
    prompt: test
`
	w, err := ParseString(content, "yml")
	if err != nil {
		t.Fatalf("ParseString failed: %v", err)
	}

	if w.Name != "yml-test" {
		t.Errorf("expected name 'yml-test', got %q", w.Name)
	}
}

func TestParseFile_InvalidTOML(t *testing.T) {

	content := `
name = "test"
invalid toml [here
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestValidate_StepWithPromptAndParallel(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "do something",
				Parallel: ParallelSpec{Steps: []Step{
					{ID: "p1", Agent: "cc", Prompt: "parallel task"},
				}},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for prompt + parallel")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "both prompt and parallel") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error about prompt + parallel conflict")
	}
}

func TestValidate_StepWithUnknownAgent(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "unknown-agent-type",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	// Unknown agent should be a warning, not error
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "unknown agent type") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning about unknown agent type")
	}
}

func TestValidate_StepWithExtendedSupportedAgentDoesNotWarn(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "ws",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	for _, warning := range result.Warnings {
		if strings.Contains(warning.Message, "unknown agent type") {
			t.Fatalf("unexpected unknown agent warning for supported alias: %+v", warning)
		}
	}
}

func TestValidate_StepWithInvalidRoute(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Route:  "invalid-route",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for invalid route")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "invalid routing strategy") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error about invalid routing strategy")
	}
}

func TestValidate_StepWithMultipleAgentMethods(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "claude",
				Pane:   PaneSpec{Index: 1},
				Route:  "least-loaded",
				Prompt: "test",
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for multiple agent methods")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "can only use one of") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about multiple agent selection methods, got %v", result.Errors)
	}
}

func TestValidate_IncompleteVarsReference(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "The value is ${vars}",
			},
		},
	}

	result := Validate(w)
	// Should have warning about incomplete reference
	found := false
	for _, warn := range result.Warnings {
		if strings.Contains(warn.Message, "incomplete variable reference") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about incomplete variable reference, got %v", result.Warnings)
	}
}

func TestValidate_IncompleteStepsReference(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "Using ${steps.prev}",
			},
		},
	}

	result := Validate(w)
	// Should have warning about incomplete step reference
	found := false
	for _, warn := range result.Warnings {
		if strings.Contains(warn.Message, "incomplete step reference") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about incomplete step reference, got %v", result.Warnings)
	}
}

func TestValidate_UnknownReferenceType(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "Using ${unknown.ref}",
			},
		},
	}

	result := Validate(w)
	// Should have warning about unknown reference type
	found := false
	for _, warn := range result.Warnings {
		if strings.Contains(warn.Message, "unknown reference type") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about unknown reference type, got %v", result.Warnings)
	}
}

func TestValidate_StepWithEmptyID(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "", Prompt: "test prompt"}, // Empty ID should fail
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for empty step id")
	}

	found := false
	for _, err := range result.Errors {
		if strings.Contains(err.Message, "step id is required") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about empty step id, got %v", result.Errors)
	}
}

func TestValidate_StepWithInvalidPromptFile(t *testing.T) {

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{ID: "s1", PromptFile: "invalid\x00path"}, // Invalid path with null byte
		},
	}

	result := Validate(w)
	// This should produce a warning about invalid path
	found := false
	for _, warn := range result.Warnings {
		if strings.Contains(warn.Message, "may be invalid") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about invalid prompt_file path, got %v", result.Warnings)
	}
}

func TestParseFile_YAMLUnknownField(t *testing.T) {

	content := `
schema_version: "2.0"
name: test-workflow
legacy: true
steps:
  - id: step1
    agent: claude
    prompt: Do something
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected error for unknown YAML field")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if !strings.Contains(pe.Message, "field legacy not found") {
		t.Fatalf("expected unknown-field message, got %q", pe.Message)
	}
	if pe.Field != "legacy" {
		t.Fatalf("expected field legacy, got %q", pe.Field)
	}
	if pe.Line == 0 {
		t.Fatalf("expected line number for unknown YAML field, got %+v", pe)
	}
	if pe.Hint == "" || !strings.Contains(pe.Hint, "docs/WORKFLOW_SCHEMA.md") {
		t.Fatalf("expected schema-doc hint, got %q", pe.Hint)
	}
}

func TestParseFile_TOMLUnknownField(t *testing.T) {

	content := `
schema_version = "2.0"
name = "test-workflow"
legacy = true

[[steps]]
id = "step1"
agent = "claude"
prompt = "Do something"
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workflow.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected error for unknown TOML field")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.Field != "legacy" {
		t.Fatalf("expected field legacy, got %q", pe.Field)
	}
	if !strings.Contains(pe.Message, "unknown TOML field") {
		t.Fatalf("expected unknown TOML field message, got %q", pe.Message)
	}
	if pe.Hint == "" || !strings.Contains(pe.Hint, "docs/WORKFLOW_SCHEMA.md") {
		t.Fatalf("expected schema-doc hint, got %q", pe.Hint)
	}
}

func TestParseString_YAMLUnknownField(t *testing.T) {

	content := `
schema_version: "2.0"
name: test-workflow
legacy: true
steps:
  - id: step1
    agent: claude
    prompt: Do something
`

	_, err := ParseString(content, "yaml")
	if err == nil {
		t.Fatal("expected error for unknown YAML field")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if !strings.Contains(pe.Message, "field legacy not found") {
		t.Fatalf("expected unknown-field message, got %q", pe.Message)
	}
	if pe.Field != "legacy" {
		t.Fatalf("expected field legacy, got %q", pe.Field)
	}
	if pe.Line == 0 {
		t.Fatalf("expected line number for unknown YAML field, got %+v", pe)
	}
	if pe.Hint == "" || !strings.Contains(pe.Hint, "docs/WORKFLOW_SCHEMA.md") {
		t.Fatalf("expected schema-doc hint, got %q", pe.Hint)
	}
}

func TestParseString_TOMLUnknownField(t *testing.T) {

	content := `
schema_version = "2.0"
name = "test-workflow"
legacy = true

[[steps]]
id = "step1"
agent = "claude"
prompt = "Do something"
`

	_, err := ParseString(content, "toml")
	if err == nil {
		t.Fatal("expected error for unknown TOML field")
	}

	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.Field != "legacy" {
		t.Fatalf("expected field legacy, got %q", pe.Field)
	}
	if !strings.Contains(pe.Message, "unknown TOML field") {
		t.Fatalf("expected unknown TOML field message, got %q", pe.Message)
	}
	if pe.Hint == "" || !strings.Contains(pe.Hint, "docs/WORKFLOW_SCHEMA.md") {
		t.Fatalf("expected schema-doc hint, got %q", pe.Hint)
	}
}

func TestParseString_YAMLCommentOnly(t *testing.T) {

	w, err := ParseString("# comment only\n", "yaml")
	if err != nil {
		t.Fatalf("ParseString failed for comment-only YAML: %v", err)
	}
	if w == nil {
		t.Fatal("expected workflow value")
	}
	if w.Name != "" || w.SchemaVersion != "" || len(w.Steps) != 0 {
		t.Fatalf("expected zero-value workflow, got %#v", w)
	}
}
