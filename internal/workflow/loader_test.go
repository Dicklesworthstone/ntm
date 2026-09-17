// Package workflow provides workflow template definitions and coordination for multi-agent patterns.
package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLoader(t *testing.T) {
	loader := NewLoader()
	if loader == nil {
		t.Fatal("NewLoader returned nil")
	}
	if loader.UserConfigDir == "" {
		t.Error("UserConfigDir should not be empty")
	}
	if loader.ProjectDir == "" {
		t.Error("ProjectDir should not be empty")
	}
}

func TestBuiltinWorkflows(t *testing.T) {
	workflows, err := builtinWorkflows()
	if err != nil {
		t.Fatalf("builtinWorkflows failed: %v", err)
	}
	if len(workflows) == 0 {
		t.Error("expected at least one builtin workflow")
	}

	// Verify expected builtins exist
	expectedNames := []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"}
	nameMap := make(map[string]bool)
	for _, w := range workflows {
		nameMap[w.Name] = true
		if w.Source != "builtin" {
			t.Errorf("workflow %q should have source 'builtin', got %q", w.Name, w.Source)
		}
	}

	for _, name := range expectedNames {
		if !nameMap[name] {
			t.Errorf("expected builtin workflow %q not found", name)
		}
	}
}

func TestLoader_LoadAll(t *testing.T) {
	loader := NewLoader()
	workflows, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(workflows) < 4 {
		t.Errorf("expected at least 4 workflows (builtins), got %d", len(workflows))
	}

	// Verify builtins are first in order
	builtinOrder := []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"}
	for i, name := range builtinOrder {
		if i >= len(workflows) {
			break
		}
		if workflows[i].Name != name {
			t.Errorf("expected workflow[%d] to be %q, got %q", i, name, workflows[i].Name)
		}
	}
}

func TestLoader_Get(t *testing.T) {
	loader := NewLoader()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"red-green", false},
		{"review-pipeline", false},
		{"specialist-team", false},
		{"parallel-explore", false},
		{"RED-GREEN", false}, // case-insensitive
		{"nonexistent", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := loader.Get(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.name)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if w == nil {
					t.Error("expected workflow, got nil")
				}
			}
		})
	}
}

func TestLoader_Get_Validates(t *testing.T) {
	loader := NewLoader()
	w, err := loader.Get("red-green")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Verify the workflow is valid
	if err := w.Validate(); err != nil {
		t.Errorf("builtin workflow should be valid: %v", err)
	}
}

func TestLoader_UserOverride(t *testing.T) {
	// Create a temporary user config directory
	tmpDir := t.TempDir()
	workflowsDir := filepath.Join(tmpDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	// Write a custom workflow that overrides red-green
	customWorkflow := `name = "red-green"
description = "Custom override"
coordination = "parallel"

[[agents]]
profile = "custom"
role = "worker"
`
	if err := os.WriteFile(filepath.Join(workflowsDir, "custom.toml"), []byte(customWorkflow), 0644); err != nil {
		t.Fatalf("failed to write custom workflow: %v", err)
	}

	loader := &Loader{
		UserConfigDir: tmpDir,
		ProjectDir:    t.TempDir(), // Empty project dir
	}

	workflows, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	// Find red-green
	var found *WorkflowTemplate
	for i := range workflows {
		if workflows[i].Name == "red-green" {
			found = &workflows[i]
			break
		}
	}

	if found == nil {
		t.Fatal("red-green workflow not found")
	}

	if found.Source != "user" {
		t.Errorf("expected source 'user', got %q", found.Source)
	}
	if found.Description != "Custom override" {
		t.Errorf("expected custom description, got %q", found.Description)
	}
}

func TestLoader_ProjectOverride(t *testing.T) {
	// Create temporary directories
	userDir := t.TempDir()
	projectDir := t.TempDir()
	ntmDir := filepath.Join(projectDir, ".ntm", "workflows")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("failed to create .ntm/workflows dir: %v", err)
	}

	// Write a project-level workflow
	projectWorkflow := `name = "my-project-workflow"
description = "Project-specific workflow"
coordination = "parallel"

[[agents]]
profile = "custom"
role = "worker"
`
	if err := os.WriteFile(filepath.Join(ntmDir, "custom.toml"), []byte(projectWorkflow), 0644); err != nil {
		t.Fatalf("failed to write project workflow: %v", err)
	}

	loader := &Loader{
		UserConfigDir: userDir,
		ProjectDir:    projectDir,
	}

	w, err := loader.Get("my-project-workflow")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if w.Source != "project" {
		t.Errorf("expected source 'project', got %q", w.Source)
	}
}

func TestBuiltinNames(t *testing.T) {
	names := BuiltinNames()
	expected := []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"}
	if len(names) != len(expected) {
		t.Errorf("expected %d builtin names, got %d", len(expected), len(names))
	}
	for i, name := range expected {
		if names[i] != name {
			t.Errorf("expected names[%d] = %q, got %q", i, name, names[i])
		}
	}
}

func TestSourceDescription(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"builtin", "Built-in"},
		{"user", "User (~/.config/ntm/workflows/)"},
		{"project", "Project (.ntm/workflows/)"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got := SourceDescription(tt.source)
			if got != tt.want {
				t.Errorf("SourceDescription(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestParseWorkflowFromContent(t *testing.T) {
	// Test single workflow format
	single := `name = "test"
description = "Test workflow"
coordination = "parallel"

[[agents]]
profile = "worker"
role = "builder"
`
	ws, err := parseWorkflowsFromContent(single)
	if err != nil {
		t.Fatalf("parseWorkflowsFromContent (single) failed: %v", err)
	}
	if len(ws) == 0 {
		t.Fatalf("expected at least 1 workflow, got 0")
	}
	w := ws[0]
	if w.Name != "test" {
		t.Errorf("expected name 'test', got %q", w.Name)
	}

	// Test array format
	array := `[[workflows]]
name = "array-test"
description = "Array format test"
coordination = "parallel"

[[workflows.agents]]
profile = "worker"
role = "builder"
`
	ws, err = parseWorkflowsFromContent(array)
	if err != nil {
		t.Fatalf("parseWorkflowsFromContent (array) failed: %v", err)
	}
	if len(ws) == 0 {
		t.Fatalf("expected at least 1 workflow, got 0")
	}
	w = ws[0]
	if w.Name != "array-test" {
		t.Errorf("expected name 'array-test', got %q", w.Name)
	}
}

func TestParseWorkflowFromContent_RejectsInvalidWorkflow(t *testing.T) {
	t.Parallel()

	invalid := `description = "missing required fields"
coordination = "parallel"
`

	if _, err := parseWorkflowsFromContent(invalid); err == nil {
		t.Fatal("expected invalid workflow content to fail validation")
	}
}

func TestParseWorkflowFromContent_RejectsUnknownFields(t *testing.T) {
	t.Parallel()

	invalid := `legacy = true

[[workflows]]
name = "array-test"
description = "Array format test"
coordination = "parallel"

[[workflows.agents]]
profile = "worker"
role = "builder"
`

	_, err := parseWorkflowsFromContent(invalid)
	if err == nil {
		t.Fatal("expected unknown field content to fail")
	}
	if !strings.Contains(err.Error(), "unknown field(s): legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadFromDir_ErrorsOnInvalidWorkflowFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := `name = "valid-workflow"
description = "Valid workflow"
coordination = "parallel"

[[agents]]
profile = "worker"
role = "builder"
`
	invalid := `description = "missing required fields"
coordination = "parallel"
`
	if err := os.WriteFile(filepath.Join(dir, "valid.toml"), []byte(valid), 0644); err != nil {
		t.Fatalf("write valid workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "invalid.toml"), []byte(invalid), 0644); err != nil {
		t.Fatalf("write invalid workflow: %v", err)
	}

	_, err := loadFromDir(dir, "project")
	if err == nil {
		t.Fatal("expected invalid workflow file to fail")
	}
}

func TestLoader_LoadAll_ErrorsOnInvalidUserWorkflow(t *testing.T) {
	t.Parallel()

	userDir := t.TempDir()
	workflowsDir := filepath.Join(userDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		t.Fatalf("failed to create workflows dir: %v", err)
	}

	invalid := `description = "missing required fields"
coordination = "parallel"
`
	if err := os.WriteFile(filepath.Join(workflowsDir, "broken.toml"), []byte(invalid), 0644); err != nil {
		t.Fatalf("failed to write invalid workflow: %v", err)
	}

	loader := &Loader{UserConfigDir: userDir, ProjectDir: t.TempDir()}
	_, err := loader.LoadAll()
	if err == nil {
		t.Fatal("expected invalid user workflow to fail")
	}
}

func TestLoader_LoadAll_ErrorsOnInvalidProjectWorkflow(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ntmDir := filepath.Join(projectDir, ".ntm", "workflows")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("failed to create .ntm/workflows dir: %v", err)
	}

	invalid := `description = "missing required fields"
coordination = "parallel"
`
	if err := os.WriteFile(filepath.Join(ntmDir, "broken.toml"), []byte(invalid), 0644); err != nil {
		t.Fatalf("failed to write invalid workflow: %v", err)
	}

	loader := &Loader{UserConfigDir: t.TempDir(), ProjectDir: projectDir}
	_, err := loader.LoadAll()
	if err == nil {
		t.Fatal("expected invalid project workflow to fail")
	}
}

func TestProfileToAgentType(t *testing.T) {
	tests := []struct {
		profile string
		want    string
	}{
		// Claude variants
		{"claude", "cc"},
		{"cc", "cc"},
		{"claude-code", "cc"},
		{"claude_code", "cc"},
		{"CLAUDE", "cc"},
		{"CC", "cc"},
		// Codex variants
		{"codex", "cod"},
		{"cod", "cod"},
		{"codex-cli", "cod"},
		{"openai-codex", "cod"},
		{"CODEX", "cod"},
		// Gemini variants
		{"gemini", "gmi"},
		{"gmi", "gmi"},
		{"gemini-cli", "gmi"},
		{"google-gemini", "gmi"},
		{"GEMINI", "gmi"},
		// Cursor variants
		{"cursor", "cursor"},
		{"CURSOR", "cursor"},
		// Windsurf variants
		{"windsurf", "windsurf"},
		{"ws", "windsurf"},
		{"WINDSURF", "windsurf"},
		// Aider variants
		{"aider", "aider"},
		{"AIDER", "aider"},
		// Oh My Pi variants
		{"omp", "omp"},
		{"oh-my-pi", "omp"},
		// Unknown profiles default to Claude
		{"tester", "cc"},
		{"implementer", "cc"},
		{"explorer", "cc"},
		{"unknown", "cc"},
		{"", "cc"},
		{"  codex-cli  ", "cod"},
	}

	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			got := ProfileToAgentType(tt.profile)
			if got != tt.want {
				t.Errorf("ProfileToAgentType(%q) = %q, want %q", tt.profile, got, tt.want)
			}
		})
	}
}

func TestWorkflowTemplate_AgentCounts(t *testing.T) {
	tests := []struct {
		name   string
		agents []WorkflowAgent
		want   map[string]int
	}{
		{
			name: "single agent defaults to 1",
			agents: []WorkflowAgent{
				{Profile: "tester", Role: "red"},
			},
			want: map[string]int{"cc": 1},
		},
		{
			name: "explicit count",
			agents: []WorkflowAgent{
				{Profile: "explorer", Role: "a", Count: 3},
			},
			want: map[string]int{"cc": 3},
		},
		{
			name: "multiple agent types",
			agents: []WorkflowAgent{
				{Profile: "claude", Role: "main", Count: 2},
				{Profile: "codex", Role: "backup", Count: 1},
			},
			want: map[string]int{"cc": 2, "cod": 1},
		},
		{
			name: "same type aggregates",
			agents: []WorkflowAgent{
				{Profile: "tester", Role: "red"},
				{Profile: "implementer", Role: "green"},
				{Profile: "reviewer", Role: "blue"},
			},
			want: map[string]int{"cc": 3},
		},
		{
			name: "mixed types with counts",
			agents: []WorkflowAgent{
				{Profile: "cc", Role: "main", Count: 2},
				{Profile: "cod", Role: "helper", Count: 1},
				{Profile: "gmi", Role: "explorer", Count: 3},
			},
			want: map[string]int{"cc": 2, "cod": 1, "gmi": 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl := &WorkflowTemplate{
				Agents: tt.agents,
			}
			got := tmpl.AgentCounts()
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("AgentCounts()[%q] = %d, want %d", k, got[k], v)
				}
			}
			for k, v := range got {
				if tt.want[k] != v {
					t.Errorf("AgentCounts() has extra key %q = %d", k, v)
				}
			}
		})
	}
}

func TestBuiltinTemplates_AgentCounts(t *testing.T) {
	loader := NewLoader()

	// Test that builtin templates return valid agent counts
	builtins := []string{"red-green", "parallel-explore", "review-pipeline", "specialist-team"}
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			tmpl, err := loader.Get(name)
			if err != nil {
				t.Fatalf("Get(%q) failed: %v", name, err)
			}

			counts := tmpl.AgentCounts()
			if len(counts) == 0 {
				t.Errorf("AgentCounts() returned empty for %q", name)
			}

			// Should have at least one agent
			total := 0
			for _, c := range counts {
				total += c
			}
			if total == 0 {
				t.Errorf("AgentCounts() total is 0 for %q", name)
			}
		})
	}
}
