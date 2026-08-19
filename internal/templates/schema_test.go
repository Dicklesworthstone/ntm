package templates

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	// Set test environment variables
	os.Setenv("TEST_VAR", "test_value")
	os.Setenv("EMPTY_VAR", "")
	defer func() {
		os.Unsetenv("TEST_VAR")
		os.Unsetenv("EMPTY_VAR")
	}()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple variable",
			input:    "${TEST_VAR}",
			expected: "test_value",
		},
		{
			name:     "variable with default, var set",
			input:    "${TEST_VAR:-default}",
			expected: "test_value",
		},
		{
			name:     "variable with default, var unset",
			input:    "${UNSET_VAR:-default_value}",
			expected: "default_value",
		},
		{
			name:     "variable with default, var empty",
			input:    "${EMPTY_VAR:-fallback}",
			expected: "fallback",
		},
		{
			name:     "mixed content",
			input:    "prefix ${TEST_VAR} suffix",
			expected: "prefix test_value suffix",
		},
		{
			name:     "multiple variables",
			input:    "${TEST_VAR} and ${UNSET_VAR:-other}",
			expected: "test_value and other",
		},
		{
			name:     "no variables",
			input:    "just plain text",
			expected: "just plain text",
		},
		{
			name:     "unset without default",
			input:    "${UNSET_VAR}",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expandEnvVars(tc.input)
			if got != tc.expected {
				t.Errorf("expandEnvVars(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSessionTemplate_MergeFrom(t *testing.T) {
	parent := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "parent",
			Description: "Parent template",
			Tags:        []string{"base", "common"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 2, Model: "opus"},
			},
			Options: SessionOptionsSpec{
				AutoRestart: true,
			},
		},
	}

	child := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "child",
			Description: "Child template",
			Extends:     "parent",
			Tags:        []string{"specialized"},
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Codex: &AgentTypeSpec{Count: 1, Model: "gpt4"},
			},
			Beads: BeadsSpec{
				AutoAssign: true,
				Recipe:     "default",
			},
		},
	}

	child.MergeFrom(parent)

	// Child values should be preserved when set
	if child.Metadata.Name != "child" {
		t.Errorf("expected child name to be preserved, got %q", child.Metadata.Name)
	}

	// Child should have its own agents, not inherit parent's
	if child.Spec.Agents.Codex == nil || child.Spec.Agents.Codex.Count != 1 {
		t.Errorf("expected child to have its own Codex agent")
	}

	// Tags are NOT merged - child keeps its own if set
	if len(child.Metadata.Tags) != 1 || child.Metadata.Tags[0] != "specialized" {
		t.Errorf("expected child to keep its own tags, got %v", child.Metadata.Tags)
	}

	// Options should inherit from parent when not set in child
	if !child.Spec.Options.AutoRestart {
		t.Error("expected AutoRestart to be inherited from parent")
	}

	// Child-specific field should be preserved
	if !child.Spec.Beads.AutoAssign || child.Spec.Beads.Recipe != "default" {
		t.Error("expected child's Beads config to be preserved")
	}
}

func TestSessionTemplateLoader_LoadBuiltin(t *testing.T) {
	// Register a test builtin template
	testBuiltin := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "test-builtin",
			Description: "Test builtin template",
		},
		Spec: SessionTemplateSpec{},
	}
	RegisterBuiltinSessionTemplate(testBuiltin)
	defer func() {
		delete(builtinSessionTemplates, "test-builtin")
	}()

	loader := NewSessionTemplateLoader()
	tmpl, err := loader.Load("test-builtin")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if tmpl.Metadata.Name != "test-builtin" {
		t.Errorf("expected name %q, got %q", "test-builtin", tmpl.Metadata.Name)
	}
}

func TestSessionTemplateLoader_LoadFromProject(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: project-template
  description: A project template
spec:
  agents:
    claude:
      count: 1
      model: opus
`
	if err := os.WriteFile(filepath.Join(projectDir, "project-template.yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	tmpl, err := loader.Load("project-template")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if tmpl.Metadata.Name != "project-template" {
		t.Errorf("expected name %q, got %q", "project-template", tmpl.Metadata.Name)
	}
}

func TestSessionTemplateLoader_LoadWithInheritance(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create parent template
	parentContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: base-template
  description: Base template
spec:
  agents:
    claude:
      count: 2
      model: opus
  options:
    autoRestart: true
`
	if err := os.WriteFile(filepath.Join(projectDir, "base-template.yaml"), []byte(parentContent), 0644); err != nil {
		t.Fatalf("WriteFile(parent): %v", err)
	}

	// Create child template that extends parent
	childContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: child-template
  description: Child template
  extends: base-template
spec:
  agents:
    codex:
      count: 1
`
	if err := os.WriteFile(filepath.Join(projectDir, "child-template.yaml"), []byte(childContent), 0644); err != nil {
		t.Fatalf("WriteFile(child): %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	tmpl, err := loader.Load("child-template")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Verify inheritance worked
	if tmpl.Metadata.Name != "child-template" {
		t.Errorf("expected name %q, got %q", "child-template", tmpl.Metadata.Name)
	}
	if !tmpl.Spec.Options.AutoRestart {
		t.Error("expected AutoRestart to be inherited from parent")
	}
}

func TestSessionTemplateLoader_CircularInheritance(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create template A that extends B
	templateA := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: template-a
  extends: template-b
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(projectDir, "template-a.yaml"), []byte(templateA), 0644); err != nil {
		t.Fatalf("WriteFile(A): %v", err)
	}

	// Create template B that extends A (circular!)
	templateB := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: template-b
  extends: template-a
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(projectDir, "template-b.yaml"), []byte(templateB), 0644); err != nil {
		t.Fatalf("WriteFile(B): %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	_, err := loader.Load("template-a")
	if err == nil {
		t.Fatal("expected error for circular inheritance, got nil")
	}
	if !errors.Is(err, ErrCircularInherit) {
		t.Errorf("expected ErrCircularInherit, got %v", err)
	}
}

func TestSessionTemplateLoader_List(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create a project template
	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: list-test-template
  description: Test template for listing
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(projectDir, "list-test-template.yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	templates, err := loader.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	// Should have at least the project template
	found := false
	for _, tmpl := range templates {
		if tmpl.Metadata.Name == "list-test-template" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find list-test-template in list")
	}
}

func TestSessionTemplateLoader_Load_ProjectErrorWinsOverBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	testBuiltin := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name:        "broken-shadow",
			Description: "builtin fallback",
		},
		Spec: SessionTemplateSpec{},
	}
	RegisterBuiltinSessionTemplate(testBuiltin)
	defer func() {
		delete(builtinSessionTemplates, "broken-shadow")
	}()

	content := `apiVersion: [
`
	if err := os.WriteFile(filepath.Join(projectDir, "broken-shadow.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	_, err := loader.Load("broken-shadow")
	if err == nil {
		t.Fatal("expected invalid project template to fail")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("got not-found style error instead of project parse error: %v", err)
	}
}

func TestSessionTemplateLoader_List_ErrorsOnInvalidProjectTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := `apiVersion: [
`
	if err := os.WriteFile(filepath.Join(projectDir, "broken.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	_, err := loader.List()
	if err == nil {
		t.Fatal("expected invalid project session template to fail")
	}
}

func TestSessionTemplateLoader_NotFound(t *testing.T) {
	loader := NewSessionTemplateLoader()
	_, err := loader.Load("nonexistent-template-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent template, got nil")
	}
}

func TestSessionTemplate_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    *SessionTemplate
		wantErr bool
	}{
		{
			name: "valid template",
			tmpl: &SessionTemplate{
				APIVersion: "v1",
				Kind:       "SessionTemplate",
				Metadata: SessionTemplateMetadata{
					Name: "valid-template",
				},
				Spec: SessionTemplateSpec{
					Agents: AgentsSpec{
						Claude: &AgentTypeSpec{Count: 1},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			tmpl: &SessionTemplate{
				APIVersion: "v1",
				Kind:       "SessionTemplate",
				Metadata:   SessionTemplateMetadata{},
				Spec:       SessionTemplateSpec{},
			},
			wantErr: true,
		},
		{
			name: "missing apiVersion",
			tmpl: &SessionTemplate{
				Kind: "SessionTemplate",
				Metadata: SessionTemplateMetadata{
					Name: "no-version",
				},
				Spec: SessionTemplateSpec{},
			},
			wantErr: true,
		},
		{
			name: "missing kind",
			tmpl: &SessionTemplate{
				APIVersion: "v1",
				Metadata: SessionTemplateMetadata{
					Name: "no-kind",
				},
				Spec: SessionTemplateSpec{},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tmpl.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestGetBuiltinSessionTemplate(t *testing.T) {
	// Register a test template
	testTmpl := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name: "get-builtin-test",
		},
	}
	RegisterBuiltinSessionTemplate(testTmpl)
	defer func() {
		delete(builtinSessionTemplates, "get-builtin-test")
	}()

	// Should find registered template
	got := GetBuiltinSessionTemplate("get-builtin-test")
	if got == nil {
		t.Fatal("expected to find registered template")
	}
	if got.Metadata.Name != "get-builtin-test" {
		t.Errorf("expected name %q, got %q", "get-builtin-test", got.Metadata.Name)
	}

	// Should return nil for nonexistent
	got = GetBuiltinSessionTemplate("nonexistent")
	if got != nil {
		t.Error("expected nil for nonexistent template")
	}
}

func TestParseSessionTemplate_EnvExpansion(t *testing.T) {
	t.Setenv("NTM_TEMPLATE_NAME", "env-template")
	t.Setenv("NTM_TEMPLATE_DESC", "env description")

	content := []byte(`apiVersion: v1
kind: SessionTemplate
metadata:
  name: ${NTM_TEMPLATE_NAME}
  description: ${NTM_TEMPLATE_DESC:-default}
spec:
  agents:
    claude:
      count: 1
`)

	tmpl, err := ParseSessionTemplate(content)
	if err != nil {
		t.Fatalf("ParseSessionTemplate failed: %v", err)
	}

	if tmpl.Metadata.Name != "env-template" {
		t.Errorf("expected name to be expanded, got %q", tmpl.Metadata.Name)
	}
	if tmpl.Metadata.Description != "env description" {
		t.Errorf("expected description to be expanded, got %q", tmpl.Metadata.Description)
	}
}

func TestSessionTemplateLoader_LoadFromUserDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	userDir := filepath.Join(tmpDir, "ntm", "templates")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	templateContent := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: user-template
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(userDir, "user-template.yaml"), []byte(templateContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	tmpl, err := loader.Load("user-template")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if tmpl.Metadata.Source != "user" {
		t.Errorf("expected source 'user', got %q", tmpl.Metadata.Source)
	}
}

func TestAgentsSpecValidate_Errors(t *testing.T) {
	tests := []struct {
		name string
		spec AgentsSpec
		err  error
	}{
		{name: "no agents", spec: AgentsSpec{}, err: ErrNoAgents},
		{name: "negative count", spec: AgentsSpec{Claude: &AgentTypeSpec{Count: -1}}, err: ErrInvalidAgentCount},
		{
			name: "conflicting count and variants",
			spec: AgentsSpec{Claude: &AgentTypeSpec{Count: 1, Variants: []AgentVariantSpec{{Count: 1, Model: "opus"}}}},
			err:  ErrConflictingAgents,
		},
		{
			name: "variant count invalid",
			spec: AgentsSpec{Claude: &AgentTypeSpec{Variants: []AgentVariantSpec{{Count: 0, Model: "opus"}}}},
			err:  ErrInvalidAgentCount,
		},
		{
			name: "persona negative count",
			spec: AgentsSpec{Personas: []PersonaSpec{{Name: "architect", Count: -2}}},
			err:  ErrInvalidAgentCount,
		},
		{
			name: "total mismatch",
			spec: AgentsSpec{Claude: &AgentTypeSpec{Count: 1}, Total: intPtr(2)},
			err:  ErrTotalMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected error %v, got %v", tc.err, err)
			}
		})
	}
}

func TestPromptsSpecValidate_InvalidDelay(t *testing.T) {
	spec := PromptsSpec{Delay: "not-a-duration"}
	if err := spec.Validate(); err == nil || !errors.Is(err, ErrInvalidDuration) {
		t.Fatalf("expected ErrInvalidDuration, got %v", err)
	}
}

func TestFileReservationsSpecValidate_Errors(t *testing.T) {
	spec := FileReservationsSpec{TTL: "bad"}
	if err := spec.Validate(); err == nil || !errors.Is(err, ErrInvalidDuration) {
		t.Fatalf("expected ErrInvalidDuration, got %v", err)
	}

	spec = FileReservationsSpec{Patterns: []string{""}}
	if err := spec.Validate(); err == nil || !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern, got %v", err)
	}
}

func TestSessionOptionsSpecValidate_InvalidDurations(t *testing.T) {
	spec := SessionOptionsSpec{
		Stagger:    &StaggerSpec{Interval: "bad"},
		Checkpoint: &CheckpointSpec{Interval: "nope"},
	}
	if err := spec.Validate(); err == nil || !errors.Is(err, ErrInvalidDuration) {
		t.Fatalf("expected ErrInvalidDuration, got %v", err)
	}
}

func TestEnvironmentSpecValidate_Errors(t *testing.T) {
	spec := EnvironmentSpec{
		PreSpawn: []HookSpec{{Command: ""}},
	}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("expected missing command error, got %v", err)
	}

	spec = EnvironmentSpec{
		PostSpawn: []HookSpec{{Command: "echo ok", Timeout: "nope"}},
	}
	if err := spec.Validate(); err == nil || !errors.Is(err, ErrInvalidDuration) {
		t.Fatalf("expected ErrInvalidDuration, got %v", err)
	}
}

func TestSessionTemplateValidate_InvalidName(t *testing.T) {
	tmpl := &SessionTemplate{
		APIVersion: "v1",
		Kind:       "SessionTemplate",
		Metadata: SessionTemplateMetadata{
			Name: "1invalid",
		},
		Spec: SessionTemplateSpec{
			Agents: AgentsSpec{
				Claude: &AgentTypeSpec{Count: 1},
			},
		},
	}

	err := tmpl.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), ErrInvalidName.Error()) {
		t.Fatalf("expected ErrInvalidName in error, got %v", err)
	}
}

func intPtr(v int) *int {
	return &v
}

func TestParseSessionTemplate_UnknownField(t *testing.T) {
	content := []byte(`apiVersion: v1
kind: SessionTemplate
legacy: true
metadata:
  name: strict-template
spec:
  agents:
    claude:
      count: 1
`)

	_, err := ParseSessionTemplate(content)
	if err == nil {
		t.Fatal("expected error for unknown session-template field")
	}
	if !strings.Contains(err.Error(), "field legacy not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestSessionTemplateLoader_Load_UnknownFieldFails(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := `apiVersion: v1
kind: SessionTemplate
legacy: true
metadata:
  name: broken-strict-template
spec:
  agents:
    claude:
      count: 1
`
	if err := os.WriteFile(filepath.Join(projectDir, "broken-strict-template.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := newSessionTemplateLoaderWithProjectForTest(tmpDir)
	_, err := loader.Load("broken-strict-template")
	if err == nil {
		t.Fatal("expected load error for unknown session-template field")
	}
	if !strings.Contains(err.Error(), "field legacy not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

// newSessionTemplateLoaderWithProjectForTest builds a SessionTemplateLoader
// rooted at a test project dir.
func newSessionTemplateLoaderWithProjectForTest(projectPath string) *SessionTemplateLoader {
	return &SessionTemplateLoader{
		projectDir: filepath.Join(projectPath, ".ntm", "templates"),
		userDir:    getDefaultSessionTemplateDir(),
	}
}
