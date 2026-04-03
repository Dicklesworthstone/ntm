package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonaValidation(t *testing.T) {
	tests := []struct {
		name    string
		persona Persona
		wantErr bool
	}{
		{
			name: "valid persona",
			persona: Persona{
				Name:        "test",
				AgentType:   "claude",
				Model:       "sonnet",
				Description: "Test persona",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			persona: Persona{
				AgentType: "claude",
			},
			wantErr: true,
		},
		{
			name: "missing agent_type",
			persona: Persona{
				Name: "test",
			},
			wantErr: true,
		},
		{
			name: "missing agent_type allowed when extending",
			persona: Persona{
				Name:    "child",
				Extends: "base",
			},
			wantErr: false,
		},
		{
			name: "invalid agent_type",
			persona: Persona{
				Name:      "test",
				AgentType: "invalid",
			},
			wantErr: true,
		},
		{
			name: "valid claude short name",
			persona: Persona{
				Name:      "test",
				AgentType: "cc",
			},
			wantErr: false,
		},
		{
			name: "valid claude alias with spacing",
			persona: Persona{
				Name:      "test",
				AgentType: " claude_code ",
			},
			wantErr: false,
		},
		{
			name: "valid codex",
			persona: Persona{
				Name:      "test",
				AgentType: "codex",
			},
			wantErr: false,
		},
		{
			name: "valid codex alias",
			persona: Persona{
				Name:      "test",
				AgentType: "openai-codex",
			},
			wantErr: false,
		},
		{
			name: "valid gemini short name",
			persona: Persona{
				Name:      "test",
				AgentType: "gmi",
			},
			wantErr: false,
		},
		{
			name: "valid gemini alias",
			persona: Persona{
				Name:      "test",
				AgentType: "google_gemini",
			},
			wantErr: false,
		},
		{
			name: "valid windsurf alias",
			persona: Persona{
				Name:      "test",
				AgentType: "ws",
			},
			wantErr: false,
		},
		{
			name: "invalid temperature - too high",
			persona: Persona{
				Name:        "test",
				AgentType:   "claude",
				Temperature: ptrFloat64(2.5),
			},
			wantErr: true,
		},
		{
			name: "invalid temperature - negative",
			persona: Persona{
				Name:        "test",
				AgentType:   "claude",
				Temperature: ptrFloat64(-0.1),
			},
			wantErr: true,
		},
		{
			name: "valid temperature",
			persona: Persona{
				Name:        "test",
				AgentType:   "claude",
				Temperature: ptrFloat64(0.7),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.persona.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAgentTypeFlag(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "cc"},
		{"Claude", "cc"},
		{"cc", "cc"},
		{"codex", "cod"},
		{"Codex", "cod"},
		{"cod", "cod"},
		{" openai-codex ", "cod"},
		{"gemini", "gmi"},
		{"Gemini", "gmi"},
		{"gmi", "gmi"},
		{" google_gemini ", "gmi"},
		{"claude_code", "cc"},
		{"ws", "windsurf"},
		{"unknown", "cc"}, // defaults to cc
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			p := &Persona{AgentType: tt.agentType}
			if got := p.AgentTypeFlag(); got != tt.want {
				t.Errorf("AgentTypeFlag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	p1 := &Persona{Name: "architect", AgentType: "claude"}
	p2 := &Persona{Name: "implementer", AgentType: "claude"}
	p3 := &Persona{Name: "Architect", AgentType: "codex"} // Override with different case

	r.Add(p1)
	r.Add(p2)
	r.Add(p3) // Should override p1

	// Test Get with case insensitivity
	got, ok := r.Get("architect")
	if !ok {
		t.Error("expected to find architect")
	}
	if got.AgentType != "codex" {
		t.Errorf("expected architect to be codex (overwritten), got %s", got.AgentType)
	}

	// Test List
	list := r.List()
	if len(list) != 2 {
		t.Errorf("expected 2 personas, got %d", len(list))
	}

	// Test Get not found
	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent persona")
	}
}

func TestBuiltinPersonas(t *testing.T) {
	personas := BuiltinPersonas()

	if len(personas) < 5 {
		t.Errorf("expected at least 5 builtin personas, got %d", len(personas))
	}

	// Verify all builtin personas are valid
	for _, p := range personas {
		if err := p.Validate(); err != nil {
			t.Errorf("builtin persona %q is invalid: %v", p.Name, err)
		}
	}

	// Check expected personas exist
	names := make(map[string]bool)
	for _, p := range personas {
		names[p.Name] = true
	}

	expected := []string{"architect", "implementer", "reviewer", "tester", "documenter"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected builtin persona %q not found", name)
		}
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "custom"
description = "Custom test persona"
agent_type = "claude"
model = "opus"
system_prompt = "You are a custom agent."
temperature = 0.5
tags = ["custom", "test"]

[[personas]]
name = "another"
description = "Another persona"
agent_type = "codex"
model = "gpt-4"
`

	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadFromFile(personasFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if len(cfg.Personas) != 2 {
		t.Errorf("expected 2 personas, got %d", len(cfg.Personas))
	}

	// Check first persona
	p := cfg.Personas[0]
	if p.Name != "custom" {
		t.Errorf("expected name 'custom', got %q", p.Name)
	}
	if p.Model != "opus" {
		t.Errorf("expected model 'opus', got %q", p.Model)
	}
	if p.Temperature == nil || *p.Temperature != 0.5 {
		t.Error("expected temperature 0.5")
	}
	if len(p.Tags) != 2 || p.Tags[0] != "custom" {
		t.Errorf("unexpected tags: %v", p.Tags)
	}
}

func TestLoadRegistry(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Create project personas file
	projectPersonas := `
[[personas]]
name = "architect"
description = "Project-specific architect"
agent_type = "codex"
model = "gpt-4"
`
	projectPath := filepath.Join(projectDir, ".ntm", "personas.toml")
	if err := os.WriteFile(projectPath, []byte(projectPersonas), 0644); err != nil {
		t.Fatalf("failed to write project personas: %v", err)
	}

	registry, err := LoadRegistry(projectDir)
	if err != nil {
		t.Fatalf("LoadRegistry failed: %v", err)
	}

	// Should have builtins + project override
	architect, ok := registry.Get("architect")
	if !ok {
		t.Error("expected to find architect")
	}
	// Project should override builtin
	if architect.AgentType != "codex" {
		t.Errorf("expected project architect with codex, got %s", architect.AgentType)
	}

	// Builtin-only personas should still exist
	if _, ok := registry.Get("implementer"); !ok {
		t.Error("expected to find builtin implementer")
	}
}

func ptrFloat64(v float64) *float64 {
	return &v
}

func TestPersonaInheritance(t *testing.T) {
	r := NewRegistry()

	// Add parent persona
	parent := &Persona{
		Name:         "base-claude",
		AgentType:    "claude",
		Model:        "sonnet",
		Description:  "Base Claude persona",
		SystemPrompt: "You are a helpful assistant.",
		Tags:         []string{"base", "claude"},
	}
	r.Add(parent)

	// Add child that extends parent
	child := &Persona{
		Name:               "senior-claude",
		Extends:            "base-claude",
		Model:              "opus", // Override model
		SystemPromptAppend: "You have 15+ years of experience.",
		Tags:               []string{"senior"},
	}
	r.Add(child)

	// Resolve inheritance
	if err := r.ResolveInheritance(); err != nil {
		t.Fatalf("ResolveInheritance failed: %v", err)
	}

	// Check resolved child
	resolved, ok := r.Get("senior-claude")
	if !ok {
		t.Fatal("expected to find senior-claude")
	}

	// Should have overridden model
	if resolved.Model != "opus" {
		t.Errorf("expected model 'opus', got %q", resolved.Model)
	}

	// Should have inherited agent type
	if resolved.AgentType != "claude" {
		t.Errorf("expected agent_type 'claude', got %q", resolved.AgentType)
	}

	// Should have inherited description
	if resolved.Description != "Base Claude persona" {
		t.Errorf("expected inherited description, got %q", resolved.Description)
	}

	// Should have merged system prompt
	if !strings.Contains(resolved.SystemPrompt, "helpful assistant") {
		t.Error("expected inherited system prompt")
	}
	if !strings.Contains(resolved.SystemPrompt, "15+ years") {
		t.Error("expected appended system prompt")
	}

	// Should have merged tags (parent: base, claude + child: senior = 3 unique)
	if len(resolved.Tags) != 3 {
		t.Errorf("expected 3 merged tags (base, claude, senior), got %d: %v", len(resolved.Tags), resolved.Tags)
	}
	// Verify specific tags are present
	tagMap := make(map[string]bool)
	for _, tag := range resolved.Tags {
		tagMap[tag] = true
	}
	for _, expected := range []string{"base", "claude", "senior"} {
		if !tagMap[expected] {
			t.Errorf("expected tag %q in merged tags, got %v", expected, resolved.Tags)
		}
	}
}

func TestPersonaInheritanceCycle(t *testing.T) {
	r := NewRegistry()

	// Create a cycle: A extends B, B extends A
	r.Add(&Persona{Name: "cycle-a", Extends: "cycle-b", AgentType: "claude"})
	r.Add(&Persona{Name: "cycle-b", Extends: "cycle-a", AgentType: "claude"})

	err := r.ResolveInheritance()
	if err == nil {
		t.Fatal("expected error for circular inheritance")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("expected circular error, got: %v", err)
	}
}

func TestPersonaSets(t *testing.T) {
	r := NewRegistry()

	// Add a persona set
	set := &PersonaSet{
		Name:        "test-team",
		Description: "Test team set",
		Personas:    []string{"implementer", "tester"},
	}
	r.AddSet(set)

	// Retrieve it
	got, ok := r.GetSet("test-team")
	if !ok {
		t.Fatal("expected to find test-team set")
	}
	if len(got.Personas) != 2 {
		t.Errorf("expected 2 personas in set, got %d", len(got.Personas))
	}

	// Test case insensitivity
	got2, ok := r.GetSet("TEST-TEAM")
	if !ok {
		t.Fatal("expected case-insensitive lookup to work")
	}
	if got2.Name != got.Name {
		t.Error("expected same set from case-insensitive lookup")
	}

	// List sets
	sets := r.ListSets()
	if len(sets) != 1 {
		t.Errorf("expected 1 set, got %d", len(sets))
	}
}

func TestBuiltinPersonaSets(t *testing.T) {
	sets := BuiltinPersonaSets()

	if len(sets) < 3 {
		t.Errorf("expected at least 3 builtin sets, got %d", len(sets))
	}

	// Check expected sets exist
	names := make(map[string]bool)
	for _, s := range sets {
		names[s.Name] = true
	}

	expected := []string{"backend-team", "review-team", "full-stack"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected builtin set %q not found", name)
		}
	}
}

func TestFocusPatterns(t *testing.T) {
	personas := BuiltinPersonas()

	// Check that builtin personas have focus patterns
	for _, p := range personas {
		if len(p.FocusPatterns) == 0 {
			t.Errorf("persona %q should have focus patterns", p.Name)
		}
	}

	// Check architect has expected patterns
	for _, p := range personas {
		if p.Name == "architect" {
			found := false
			for _, pattern := range p.FocusPatterns {
				if pattern == "docs/**" {
					found = true
					break
				}
			}
			if !found {
				t.Error("expected architect to have docs/** focus pattern")
			}
		}
	}
}

func TestTemplateContext(t *testing.T) {
	// Create temp directory with a go.mod
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := LoadTemplateContext(tmpDir)

	// Should detect Go language
	if ctx.Language != "Go" {
		t.Errorf("expected language 'Go', got %q", ctx.Language)
	}

	// Should have project name from directory
	if ctx.ProjectName == "" {
		t.Error("expected project name to be set")
	}
}

func TestExpandPromptVarsWithContext(t *testing.T) {
	p := &Persona{
		Name:        "test",
		Description: "Test persona",
		AgentType:   "claude",
		Model:       "sonnet",
	}

	ctx := &TemplateContext{
		ProjectName:     "MyProject",
		Language:        "Go",
		CodebaseSummary: "A test project",
		CustomVars: map[string]string{
			"custom_key": "custom_value",
		},
	}

	content := `Hello {{.Name}}, you work on {{project_name}} written in {{language}}.
Summary: {{codebase_summary}}
Custom: {{custom_key}}`

	expanded := ExpandPromptVarsWithContext(content, p, ctx)

	if !strings.Contains(expanded, "Hello test") {
		t.Error("expected persona name expansion")
	}
	if !strings.Contains(expanded, "MyProject") {
		t.Error("expected project_name expansion")
	}
	if !strings.Contains(expanded, "Go") {
		t.Error("expected language expansion")
	}
	if !strings.Contains(expanded, "A test project") {
		t.Error("expected codebase_summary expansion")
	}
	if !strings.Contains(expanded, "custom_value") {
		t.Error("expected custom_key expansion")
	}
}

func TestExpandPromptVarsWithNils(t *testing.T) {
	// Test with nil persona and nil context
	content := "Hello {{.Name}}"
	result := ExpandPromptVarsWithContext(content, nil, nil)
	if result != content {
		t.Errorf("expected unchanged content with nil inputs, got %q", result)
	}

	// Test with nil context only
	p := &Persona{Name: "test", AgentType: "claude"}
	result = ExpandPromptVarsWithContext("Hello {{.Name}}", p, nil)
	if result != "Hello test" {
		t.Errorf("expected 'Hello test', got %q", result)
	}
}

func TestPrepareSystemPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with nil persona
	path, err := PrepareSystemPrompt(nil, tmpDir)
	if err != nil {
		t.Errorf("unexpected error for nil persona: %v", err)
	}
	if path != "" {
		t.Error("expected empty path for nil persona")
	}

	// Test with empty system prompt
	p := &Persona{Name: "test", AgentType: "claude", SystemPrompt: ""}
	path, err = PrepareSystemPrompt(p, tmpDir)
	if err != nil {
		t.Errorf("unexpected error for empty prompt: %v", err)
	}
	if path != "" {
		t.Error("expected empty path for empty system prompt")
	}

	// Test with valid system prompt
	p = &Persona{
		Name:         "architect",
		AgentType:    "claude",
		SystemPrompt: "You are an architect for {{project_name}}.",
	}
	path, err = PrepareSystemPrompt(p, tmpDir)
	if err != nil {
		t.Fatalf("PrepareSystemPrompt failed: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}

	// Verify file was created
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("prompt file was not created")
	}

	// Verify content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read prompt file: %v", err)
	}
	if !strings.Contains(string(data), "You are an architect") {
		t.Error("expected system prompt content in file")
	}
}

func TestPrepareSystemPromptWithContextFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a context file
	contextDir := filepath.Join(tmpDir, "docs")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		t.Fatal(err)
	}
	contextFile := filepath.Join(contextDir, "README.md")
	if err := os.WriteFile(contextFile, []byte("# Project README"), 0644); err != nil {
		t.Fatal(err)
	}

	p := &Persona{
		Name:         "test",
		AgentType:    "claude",
		SystemPrompt: "You are a test agent.",
		ContextFiles: []string{"docs/*.md"},
	}

	path, err := PrepareSystemPrompt(p, tmpDir)
	if err != nil {
		t.Fatalf("PrepareSystemPrompt failed: %v", err)
	}

	// Verify content includes context files
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read prompt file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Project README") {
		t.Error("expected context file content in prompt")
	}
	if !strings.Contains(content, "You are a test agent") {
		t.Error("expected system prompt in output")
	}
}

func TestPrepareContextFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with nil persona
	result, err := PrepareContextFiles(nil, tmpDir)
	if err != nil {
		t.Errorf("unexpected error for nil persona: %v", err)
	}
	if result != "" {
		t.Error("expected empty result for nil persona")
	}

	// Test with empty context files
	p := &Persona{Name: "test", AgentType: "claude"}
	result, err = PrepareContextFiles(p, tmpDir)
	if err != nil {
		t.Errorf("unexpected error for empty context files: %v", err)
	}
	if result != "" {
		t.Error("expected empty result for empty context files")
	}

	// Test with no matching files
	p = &Persona{
		Name:         "test",
		AgentType:    "claude",
		ContextFiles: []string{"nonexistent/*.xyz"},
	}
	result, err = PrepareContextFiles(p, tmpDir)
	if err != nil {
		t.Errorf("unexpected error for non-matching glob: %v", err)
	}
	if result != "" {
		t.Error("expected empty result for non-matching glob")
	}

	// Create test files
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("Content 1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("Content 2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Test with matching files
	p = &Persona{
		Name:         "test",
		AgentType:    "claude",
		ContextFiles: []string{"*.txt"},
	}
	result, err = PrepareContextFiles(p, tmpDir)
	if err != nil {
		t.Fatalf("PrepareContextFiles failed: %v", err)
	}
	if !strings.Contains(result, "Content 1") || !strings.Contains(result, "Content 2") {
		t.Error("expected both file contents in result")
	}
	if !strings.Contains(result, "# Context Files") {
		t.Error("expected Context Files header")
	}
}

func TestCleanupPromptFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Test cleanup when directory doesn't exist
	err := CleanupPromptFiles(tmpDir)
	if err != nil {
		t.Errorf("unexpected error for non-existent prompts dir: %v", err)
	}

	// Create prompts directory and files
	promptsDir := filepath.Join(tmpDir, ".ntm", "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(promptsDir, "test.md")
	if err := os.WriteFile(promptFile, []byte("test prompt"), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify file exists
	if _, err := os.Stat(promptFile); os.IsNotExist(err) {
		t.Fatal("prompt file should exist before cleanup")
	}

	// Cleanup
	err = CleanupPromptFiles(tmpDir)
	if err != nil {
		t.Fatalf("CleanupPromptFiles failed: %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(promptsDir); !os.IsNotExist(err) {
		t.Error("prompts directory should be removed after cleanup")
	}
}

func TestGetGitRepoName(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with no git directory
	result := getGitRepoName(tmpDir)
	if result != "" {
		t.Errorf("expected empty result for non-git dir, got %q", result)
	}

	// Create git directory with config
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test with HTTPS URL
	gitConfig := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://github.com/user/myrepo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(gitConfig), 0644); err != nil {
		t.Fatal(err)
	}

	result = getGitRepoName(tmpDir)
	if result != "myrepo" {
		t.Errorf("expected 'myrepo', got %q", result)
	}

	// Test with SSH URL
	gitConfigSSH := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = git@github.com:user/sshrepo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(gitConfigSSH), 0644); err != nil {
		t.Fatal(err)
	}

	result = getGitRepoName(tmpDir)
	if result != "sshrepo" {
		t.Errorf("expected 'sshrepo', got %q", result)
	}

	// Test with no origin remote
	gitConfigNoOrigin := `[core]
	repositoryformatversion = 0
[remote "upstream"]
	url = https://github.com/other/repo.git
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(gitConfigNoOrigin), 0644); err != nil {
		t.Fatal(err)
	}

	result = getGitRepoName(tmpDir)
	if result != "" {
		t.Errorf("expected empty for no origin, got %q", result)
	}
}

func TestLoadCustomVars(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .ntm directory
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test with no config file
	ctx := DefaultTemplateContext()
	loadCustomVars(tmpDir, ctx)
	if len(ctx.CustomVars) != 0 {
		t.Error("expected empty custom vars when no config")
	}

	// Create config with template_vars section
	configContent := `[general]
some_setting = true

[template_vars]
project_name = "CustomProject"
language = "Rust"
codebase_summary = "A custom project"
custom_var = "custom_value"
quoted_var = "with quotes"

[other_section]
ignored = "yes"
`
	if err := os.WriteFile(filepath.Join(ntmDir, "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx = DefaultTemplateContext()
	loadCustomVars(tmpDir, ctx)

	if ctx.ProjectName != "CustomProject" {
		t.Errorf("expected project_name 'CustomProject', got %q", ctx.ProjectName)
	}
	if ctx.Language != "Rust" {
		t.Errorf("expected language 'Rust', got %q", ctx.Language)
	}
	if ctx.CodebaseSummary != "A custom project" {
		t.Errorf("expected codebase_summary 'A custom project', got %q", ctx.CodebaseSummary)
	}
	if ctx.CustomVars["custom_var"] != "custom_value" {
		t.Errorf("expected custom_var 'custom_value', got %q", ctx.CustomVars["custom_var"])
	}
	if ctx.CustomVars["quoted_var"] != "with quotes" {
		t.Errorf("expected quoted_var 'with quotes', got %q", ctx.CustomVars["quoted_var"])
	}
}

func TestDetectPrimaryLanguage(t *testing.T) {
	tests := []struct {
		file     string
		expected string
	}{
		{"go.mod", "Go"},
		{"Cargo.toml", "Rust"},
		{"package.json", "JavaScript/TypeScript"},
		{"requirements.txt", "Python"},
		{"pyproject.toml", "Python"},
		{"Gemfile", "Ruby"},
		{"pom.xml", "Java"},
		{"build.gradle", "Java/Kotlin"},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, tt.file), []byte(""), 0644); err != nil {
				t.Fatal(err)
			}

			result := detectPrimaryLanguage(tmpDir)
			if result != tt.expected {
				t.Errorf("expected %q for %s, got %q", tt.expected, tt.file, result)
			}
		})
	}

	// Test with no language files
	tmpDir := t.TempDir()
	result := detectPrimaryLanguage(tmpDir)
	if result != "" {
		t.Errorf("expected empty for no language files, got %q", result)
	}
}

func TestDefaultUserPath(t *testing.T) {
	// Save original env vars
	origNTMConfig := os.Getenv("NTM_CONFIG")
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		os.Setenv("NTM_CONFIG", origNTMConfig)
		os.Setenv("XDG_CONFIG_HOME", origXDG)
	}()

	// Test with NTM_CONFIG set
	os.Setenv("NTM_CONFIG", "/custom/path/config.toml")
	os.Setenv("XDG_CONFIG_HOME", "")
	path := DefaultUserPath()
	if path != "/custom/path/personas.toml" {
		t.Errorf("expected '/custom/path/personas.toml', got %q", path)
	}

	// Test with XDG_CONFIG_HOME set
	os.Setenv("NTM_CONFIG", "")
	os.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	path = DefaultUserPath()
	if path != "/xdg/config/ntm/personas.toml" {
		t.Errorf("expected '/xdg/config/ntm/personas.toml', got %q", path)
	}

	// Test with neither set (falls back to ~/.config/ntm)
	os.Setenv("NTM_CONFIG", "")
	os.Setenv("XDG_CONFIG_HOME", "")
	path = DefaultUserPath()
	if !strings.HasSuffix(path, ".config/ntm/personas.toml") {
		t.Errorf("expected path ending with '.config/ntm/personas.toml', got %q", path)
	}
}

func TestMergePersonasEdgeCases(t *testing.T) {
	// Test merging with all fields populated in parent
	parent := &Persona{
		Name:          "parent",
		Description:   "Parent description",
		AgentType:     "claude",
		Model:         "opus",
		SystemPrompt:  "Parent prompt",
		Temperature:   ptrFloat64(0.7),
		ContextFiles:  []string{"parent/*.md"},
		Tags:          []string{"parent-tag"},
		FocusPatterns: []string{"parent/**"},
	}

	// Child with minimal fields
	child := &Persona{
		Name:    "child",
		Extends: "parent",
	}

	// Note: mergePersonas is not exported, so we test through ResolveInheritance
	r := NewRegistry()
	r.Add(parent)
	r.Add(child)

	if err := r.ResolveInheritance(); err != nil {
		t.Fatalf("ResolveInheritance failed: %v", err)
	}

	resolved, ok := r.Get("child")
	if !ok {
		t.Fatal("expected to find child")
	}

	// Check inherited fields
	if resolved.Description != "Parent description" {
		t.Errorf("expected inherited description, got %q", resolved.Description)
	}
	if resolved.AgentType != "claude" {
		t.Errorf("expected inherited agent_type, got %q", resolved.AgentType)
	}
	if resolved.Model != "opus" {
		t.Errorf("expected inherited model, got %q", resolved.Model)
	}
	if resolved.SystemPrompt != "Parent prompt" {
		t.Errorf("expected inherited system_prompt, got %q", resolved.SystemPrompt)
	}
	if resolved.Temperature == nil || *resolved.Temperature != 0.7 {
		t.Error("expected inherited temperature 0.7")
	}
	if len(resolved.ContextFiles) != 1 || resolved.ContextFiles[0] != "parent/*.md" {
		t.Errorf("expected inherited context_files, got %v", resolved.ContextFiles)
	}
	if len(resolved.FocusPatterns) != 1 || resolved.FocusPatterns[0] != "parent/**" {
		t.Errorf("expected inherited focus_patterns, got %v", resolved.FocusPatterns)
	}
}

func TestLoadFromFileInvalidToml(t *testing.T) {
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "bad.toml")

	// Write invalid TOML
	if err := os.WriteFile(badFile, []byte("this is not valid toml [[["), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(badFile)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadFromFileInvalidPersona(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	// Write valid TOML but invalid persona (missing required fields)
	content := `
[[personas]]
name = "incomplete"
# Missing agent_type
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(personasFile)
	if err == nil {
		t.Error("expected error for invalid persona")
	}
}

func TestLoadFromFileUnknownField(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "dev"
agent_type = "claude"
legacy = true
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(personasFile)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadFromFileDuplicatePersonaName(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "dup"
agent_type = "claude"

[[personas]]
name = "Dup"
agent_type = "codex"
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(personasFile)
	if err == nil {
		t.Fatal("expected error for duplicate persona name")
	}
}

func TestLoadFromFileDuplicatePersonaSetName(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "dev"
agent_type = "claude"

[[persona_sets]]
name = "team"
personas = ["dev"]

[[persona_sets]]
name = "TEAM"
personas = ["dev"]
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(personasFile)
	if err == nil {
		t.Fatal("expected error for duplicate persona set name")
	}
}

func TestLoadFromFileInvalidPersonaSet(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "dev"
agent_type = "claude"

[[persona_sets]]
name = ""
personas = []
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(personasFile)
	if err == nil {
		t.Fatal("expected error for invalid persona set")
	}
}

func TestLoadFromFileAllowsExtendingPersonaWithoutAgentType(t *testing.T) {
	tmpDir := t.TempDir()
	personasFile := filepath.Join(tmpDir, "personas.toml")

	content := `
[[personas]]
name = "base"
agent_type = "claude"
system_prompt = "Base"

[[personas]]
name = "child"
extends = "base"
system_prompt_append = "Extra"
`
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFile(personasFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if len(cfg.Personas) != 2 {
		t.Fatalf("expected 2 personas, got %d", len(cfg.Personas))
	}
	if got := cfg.Personas[1].AgentType; got != "" {
		t.Fatalf("child AgentType = %q, want empty before inheritance resolution", got)
	}
}

func TestLoadRegistryRejectsUnknownPersonaSetReference(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
		t.Fatal(err)
	}

	content := `
[[personas]]
name = "dev"
agent_type = "claude"
system_prompt = "Dev"

[[persona_sets]]
name = "broken"
personas = ["dev", "missing"]
`
	personasFile := filepath.Join(projectDir, ".ntm", "personas.toml")
	if err := os.WriteFile(personasFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRegistry(projectDir)
	if err == nil {
		t.Fatal("expected error for unknown persona set member")
	}
	if !strings.Contains(err.Error(), "references unknown persona") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRegistry_UserPersonaParseError(t *testing.T) {
	tmpDir := t.TempDir()
	xdgDir := filepath.Join(tmpDir, "xdg")
	userDir := filepath.Join(xdgDir, "ntm")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdgDir)

	content := `
[[personas]]
name = "broken"
agent_type = "claude"
legacy = true
`
	if err := os.WriteFile(filepath.Join(userDir, "personas.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRegistry(filepath.Join(tmpDir, "project"))
	if err == nil {
		t.Fatal("expected error for invalid user personas file")
	}
	if !strings.Contains(err.Error(), "loading user personas") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRegistry_ProjectPersonaParseError(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
		t.Fatal(err)
	}

	content := `
[[personas]]
name = "broken"
agent_type = "claude"
legacy = true
`
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "personas.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRegistry(projectDir)
	if err == nil {
		t.Fatal("expected error for invalid project personas file")
	}
	if !strings.Contains(err.Error(), "loading project personas") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRegistry_ProjectPersonaExtendsUserPersonaWithoutAgentType(t *testing.T) {
	tmpDir := t.TempDir()
	xdgDir := filepath.Join(tmpDir, "xdg")
	userDir := filepath.Join(xdgDir, "ntm")
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("NTM_CONFIG", filepath.Join(tmpDir, "custom-root", "config.toml"))

	userContent := `
[[personas]]
name = "base"
agent_type = "claude"
model = "sonnet"
system_prompt = "Base prompt"
`
	if err := os.WriteFile(filepath.Join(userDir, "personas.toml"), []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	projectContent := `
[[personas]]
name = "child"
extends = "base"
system_prompt_append = "Extra detail"
`
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "personas.toml"), []byte(projectContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry, err := LoadRegistry(projectDir)
	if err != nil {
		t.Fatalf("LoadRegistry failed: %v", err)
	}
	child, ok := registry.Get("child")
	if !ok {
		t.Fatal("expected child persona in registry")
	}
	if child.AgentType != "claude" {
		t.Fatalf("child AgentType = %q, want %q", child.AgentType, "claude")
	}
	if child.Model != "sonnet" {
		t.Fatalf("child Model = %q, want %q", child.Model, "sonnet")
	}
	if !strings.Contains(child.SystemPrompt, "Extra detail") {
		t.Fatalf("child SystemPrompt = %q, want appended content", child.SystemPrompt)
	}
}
