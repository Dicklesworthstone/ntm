package templates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetAgentCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tmpl SessionTemplate
		want int
	}{
		{
			name: "all nil",
			tmpl: SessionTemplate{},
			want: 0,
		},
		{
			name: "claude only",
			tmpl: SessionTemplate{
				Spec: SessionTemplateSpec{
					Agents: AgentsSpec{
						Claude: &AgentTypeSpec{Count: 3},
					},
				},
			},
			want: 3,
		},
		{
			name: "all three",
			tmpl: SessionTemplate{
				Spec: SessionTemplateSpec{
					Agents: AgentsSpec{
						Claude: &AgentTypeSpec{Count: 2},
						Codex:  &AgentTypeSpec{Count: 2},
						Gemini: &AgentTypeSpec{Count: 1},
					},
				},
			},
			want: 5,
		},
		{
			name: "codex and gemini",
			tmpl: SessionTemplate{
				Spec: SessionTemplateSpec{
					Agents: AgentsSpec{
						Codex:  &AgentTypeSpec{Count: 3},
						Gemini: &AgentTypeSpec{Count: 2},
					},
				},
			},
			want: 5,
		},
		{
			name: "variants and default personas",
			tmpl: SessionTemplate{
				Spec: SessionTemplateSpec{
					Agents: AgentsSpec{
						Claude: &AgentTypeSpec{Variants: []AgentVariantSpec{
							{Count: 1, Model: "opus"},
							{Count: 2, Model: "sonnet"},
						}},
						Personas: []PersonaSpec{
							{Name: "reviewer"},
							{Name: "tester", Count: 2},
						},
					},
				},
			},
			want: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.tmpl.GetAgentCount()
			if got != tt.want {
				t.Errorf("GetAgentCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTotalCount_WithVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec AgentTypeSpec
		want int
	}{
		{
			name: "count only",
			spec: AgentTypeSpec{Count: 3},
			want: 3,
		},
		{
			name: "variants override count",
			spec: AgentTypeSpec{
				Count: 0,
				Variants: []AgentVariantSpec{
					{Count: 2, Model: "opus"},
					{Count: 1, Model: "sonnet"},
				},
			},
			want: 3,
		},
		{
			name: "single variant",
			spec: AgentTypeSpec{
				Variants: []AgentVariantSpec{
					{Count: 5, Model: "opus"},
				},
			},
			want: 5,
		},
		{
			name: "empty variants uses count",
			spec: AgentTypeSpec{
				Count:    4,
				Variants: []AgentVariantSpec{},
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.spec.TotalCount()
			if got != tt.want {
				t.Errorf("TotalCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTemplateNotFoundError(t *testing.T) {
	t.Parallel()

	err := &TemplateNotFoundError{Name: "mytemplate"}
	got := err.Error()
	want := "template not found: mytemplate"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestLoader_List(t *testing.T) {
	t.Parallel()

	userDir := t.TempDir()

	// Create a valid template file
	content := `---
name: test-tmpl
description: A test template
---
Hello {{project_dir}}`
	os.WriteFile(filepath.Join(userDir, "test-tmpl.md"), []byte(content), 0644)

	// Also create a non-md file that should be ignored
	os.WriteFile(filepath.Join(userDir, "readme.txt"), []byte("not a template"), 0644)

	loader := &Loader{userDir: userDir}
	templates, err := loader.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Should have user templates + builtins
	if len(templates) == 0 {
		t.Fatal("expected at least one template")
	}

	// Check our user template is present
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "test-tmpl" {
			found = true
			if tmpl.Source != SourceUser {
				t.Errorf("source = %q, want %q", tmpl.Source, SourceUser)
			}
			break
		}
	}
	if !found {
		t.Error("user template 'test-tmpl' not found in List results")
	}
}

func TestLoader_List_ProjectOverridesUser(t *testing.T) {
	t.Parallel()

	userDir := t.TempDir()
	projectDir := t.TempDir()

	// Same-named template in both dirs
	userContent := `---
name: shared
description: User version
---
user content`
	projectContent := `---
name: shared
description: Project version
---
project content`

	os.WriteFile(filepath.Join(userDir, "shared.md"), []byte(userContent), 0644)
	os.WriteFile(filepath.Join(projectDir, "shared.md"), []byte(projectContent), 0644)

	loader := &Loader{userDir: userDir, projectDir: projectDir}
	templates, err := loader.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// The project version should win
	for _, tmpl := range templates {
		if tmpl.Name == "shared" {
			if tmpl.Source != SourceProject {
				t.Errorf("expected project source for 'shared', got %q", tmpl.Source)
			}
			return
		}
	}
	t.Error("template 'shared' not found")
}

func TestLoader_listFromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Valid template
	os.WriteFile(filepath.Join(dir, "valid.md"), []byte(`---
name: valid
---
content`), 0644)

	// Invalid template (bad frontmatter)
	os.WriteFile(filepath.Join(dir, "invalid.md"), []byte("not valid yaml frontmatter without ---"), 0644)

	// Non-md file (should be skipped)
	os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("ignored"), 0644)

	// Subdirectory (should be skipped)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)

	loader := &Loader{}
	templates, err := loader.listFromDir(dir, SourceUser)
	if err != nil {
		t.Fatalf("listFromDir: %v", err)
	}

	// Should have at least the valid template (invalid may or may not parse)
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "valid" {
			found = true
			if tmpl.Source != SourceUser {
				t.Errorf("source = %q, want %q", tmpl.Source, SourceUser)
			}
		}
	}
	if !found {
		t.Error("valid template not found in listFromDir results")
	}
}

func TestLoader_List_ErrorsOnInvalidProjectTemplate(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".ntm", "templates")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := `---
name: [
---
body
`
	if err := os.WriteFile(filepath.Join(projectDir, "broken.md"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoaderWithProject(tmpDir)
	_, err := loader.List()
	if err == nil {
		t.Fatal("expected invalid project template to fail")
	}
}

func TestLoader_listFromDir_NonExistent(t *testing.T) {
	t.Parallel()

	loader := &Loader{}
	_, err := loader.listFromDir("/no/such/directory", SourceUser)
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}
