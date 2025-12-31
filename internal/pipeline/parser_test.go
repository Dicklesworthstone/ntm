package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFile_YAML(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	_, err := ParseFile("/nonexistent/path/workflow.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseString_YAML(t *testing.T) {
	t.Parallel()

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

func TestParseString_TOML(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:       "s1",
				Prompt:   "test",
				Parallel: []Step{{ID: "p1", Prompt: "parallel"}},
			},
		},
	}

	result := Validate(w)
	if result.Valid {
		t.Error("expected validation to fail for both prompt and parallel")
	}
}

func TestValidate_MultipleAgentSelectionMethods(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "claude",
				Pane:   1,
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
	t.Parallel()

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
	t.Parallel()

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

func TestValidate_RetryWithZeroCount(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

func TestValidate_ValidWorkflow(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "parallel-workflow",
		Steps: []Step{
			{
				ID: "parallel_work",
				Parallel: []Step{
					{ID: "p1", Agent: "claude", Prompt: "Task 1"},
					{ID: "p2", Agent: "codex", Prompt: "Task 2"},
				},
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

func TestValidate_VariableReferences(t *testing.T) {
	t.Parallel()

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

func TestLoadAndValidate(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"claude", "claude"},
		{"cc", "claude"},
		{"claude-code", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"openai", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"google", "gemini"},
		{"unknown", "unknown"},
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

func TestParseError_Error(t *testing.T) {
	t.Parallel()

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
