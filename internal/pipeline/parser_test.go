package pipeline

import (
	"os"
	"path/filepath"
	"strings"
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

func TestValidate_CycleInLoopSubsteps(t *testing.T) {
	t.Parallel()

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

func TestValidate_LoopNegativeMaxIterations(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "s1",
				Loop: &LoopConfig{
					Items:         "${vars.list}",
					As:            "item",
					MaxIterations: -1,
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

func TestValidate_VariableReferencesInLoopSubsteps(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID: "par_step",
				Parallel: []Step{
					{ID: "inner", Prompt: "Process ${unknown.ref}"},
				},
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
		// Lowercase (canonical)
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
	t.Parallel()

	tests := []struct {
		input    string
		expected bool
	}{
		// Valid types (lowercase)
		{"claude", true},
		{"cc", true},
		{"claude-code", true},
		{"codex", true},
		{"cod", true},
		{"openai", true},
		{"gemini", true},
		{"gmi", true},
		{"google", true},
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

func TestIsValidPath(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := isValidPath(tt.path)
			if got != tt.want {
				t.Errorf("isValidPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsValidRoute(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := isValidRoute(tt.route)
			if got != tt.want {
				t.Errorf("isValidRoute(%q) = %v, want %v", tt.route, got, tt.want)
			}
		})
	}
}

func TestIsValidErrorAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action ErrorAction
		want   bool
	}{
		{ErrorActionFail, true},
		{ErrorActionContinue, true},
		{ErrorActionRetry, true},
		{ErrorAction("unknown"), false},
		{ErrorAction(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			t.Parallel()
			got := isValidErrorAction(tt.action)
			if got != tt.want {
				t.Errorf("isValidErrorAction(%q) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

func TestIsValidWaitCondition(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := isValidWaitCondition(tt.cond)
			if got != tt.want {
				t.Errorf("isValidWaitCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestParseString_UnsupportedFormat(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
}

func TestParseString_InvalidTOML(t *testing.T) {
	t.Parallel()

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
}

func TestParseString_YMLFormat(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Prompt: "do something",
				Parallel: []Step{
					{ID: "p1", Agent: "cc", Prompt: "parallel task"},
				},
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
	t.Parallel()

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

func TestValidate_StepWithInvalidRoute(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	w := &Workflow{
		SchemaVersion: "2.0",
		Name:          "test",
		Steps: []Step{
			{
				ID:     "s1",
				Agent:  "claude",
				Pane:   1,
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
