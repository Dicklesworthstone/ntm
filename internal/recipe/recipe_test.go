package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAgentSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		spec    AgentSpec
		wantErr bool
	}{
		{"valid", AgentSpec{Type: "cc", Count: 1}, false},
		{"valid_cursor", AgentSpec{Type: "cursor", Count: 1}, false},
		{"valid_claude_alias", AgentSpec{Type: "claude", Count: 1}, false},
		{"valid_codex_alias", AgentSpec{Type: "openai-codex", Count: 1}, false},
		{"valid_gemini_alias", AgentSpec{Type: "google-gemini", Count: 1}, false},
		{"valid_grok", AgentSpec{Type: "grok", Count: 1}, false},
		{"valid_grok_alias", AgentSpec{Type: "xai_grok_build", Count: 1}, false},
		{"valid_windsurf_alias", AgentSpec{Type: "ws", Count: 1}, false},
		{"missing_type", AgentSpec{Count: 1}, true},
		{"zero_count", AgentSpec{Type: "cc", Count: 0}, true},
		{"negative_count", AgentSpec{Type: "cc", Count: -1}, true},
		{"high_count", AgentSpec{Type: "cc", Count: 21}, true},
		{"max_count", AgentSpec{Type: "cc", Count: 20}, false},
		{"with_model", AgentSpec{Type: "cc", Count: 1, Model: "opus"}, false},
		{"with_reasoning_effort", AgentSpec{Type: "grok", Count: 1, Model: "grok-4", ReasoningEffort: "high"}, false},
		{"with_persona", AgentSpec{Type: "cc", Count: 1, Persona: "code-reviewer"}, false},
		{"user_not_supported", AgentSpec{Type: "user", Count: 1}, true},
		{"unknown_type", AgentSpec{Type: "custom-plugin", Count: 1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAgentSpec(tt.spec); (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgentSpec() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRecipeHelpers(t *testing.T) {
	t.Parallel()
	r := Recipe{
		Agents: []AgentSpec{
			{Type: "cc", Count: 2},
			{Type: "openai-codex", Count: 1},
			{Type: "claude", Count: 1},
		},
	}

	if total := r.TotalAgents(); total != 4 {
		t.Errorf("TotalAgents() = %d, want 4", total)
	}

	counts := r.AgentCounts()
	if counts["cc"] != 3 {
		t.Errorf("AgentCounts[cc] = %d, want 3", counts["cc"])
	}
	if counts["cod"] != 1 {
		t.Errorf("AgentCounts[cod] = %d, want 1", counts["cod"])
	}
}

func TestBuiltinRecipes(t *testing.T) {
	t.Parallel()
	recipes := builtinRecipes()
	if len(recipes) == 0 {
		t.Fatal("expected at least one builtin recipe")
	}

	// Check all builtin recipes are valid
	for _, r := range recipes {
		if r.Name == "" {
			t.Error("builtin recipe has empty name")
		}
		if r.Source != "builtin" {
			t.Errorf("builtin recipe %q has source %q, expected 'builtin'", r.Name, r.Source)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("builtin recipe %q is invalid: %v", r.Name, err)
		}
		for _, spec := range r.Agents {
			if spec.Type == "gmi" || spec.Type == "gemini" {
				t.Errorf("builtin recipe %q still allocates panes to legacy %q; expected supported harnesses (cc, cod, agy)", r.Name, spec.Type)
			}
		}
	}
}

func TestBuiltinNames(t *testing.T) {
	t.Parallel()
	names := BuiltinNames()
	if len(names) == 0 {
		t.Fatal("expected at least one builtin recipe name")
	}

	// Verify all expected builtins exist
	expectedNames := []string{"quick-claude", "full-stack", "minimal", "codex-heavy", "balanced", "review-team"}
	for _, expected := range expectedNames {
		found := false
		for _, name := range names {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected builtin recipe %q not found", expected)
		}
	}
}

func TestNewLoader(t *testing.T) {
	t.Parallel()
	loader := NewLoader()
	if loader == nil {
		t.Fatal("NewLoader returned nil")
	}
	if loader.UserConfigDir == "" {
		t.Error("loader.UserConfigDir is empty")
	}
	// ProjectDir might be empty in some test environments
}

func TestDefaultUserConfigDir(t *testing.T) {
	t.Parallel()
	dir := defaultUserConfigDir()
	if dir == "" {
		t.Error("defaultUserConfigDir returned empty string")
	}
}

func TestDefaultUserConfigDir_WithXDG(t *testing.T) {
	// Save original value
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	// Set custom XDG
	os.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	dir := defaultUserConfigDir()
	expected := "/custom/xdg/ntm"
	if dir != expected {
		t.Errorf("defaultUserConfigDir() = %q, want %q", dir, expected)
	}
}

func TestLoaderLoadAll(t *testing.T) {
	t.Parallel()
	loader := NewLoader()
	recipes, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	if len(recipes) == 0 {
		t.Error("LoadAll() returned no recipes")
	}

	// Should at least have builtins
	builtins := builtinRecipes()
	if len(recipes) < len(builtins) {
		t.Errorf("LoadAll() returned %d recipes, expected at least %d (builtins)", len(recipes), len(builtins))
	}
}

func TestLoaderGet(t *testing.T) {
	t.Parallel()
	loader := NewLoader()

	// Test getting a builtin recipe
	recipe, err := loader.Get("quick-claude")
	if err != nil {
		t.Fatalf("Get('quick-claude') error: %v", err)
	}
	if recipe == nil {
		t.Fatal("Get('quick-claude') returned nil")
	}
	if recipe.Name != "quick-claude" {
		t.Errorf("expected name 'quick-claude', got %q", recipe.Name)
	}

	// Test case-insensitive matching
	recipe, err = loader.Get("QUICK-CLAUDE")
	if err != nil {
		t.Fatalf("Get('QUICK-CLAUDE') error: %v", err)
	}
	if recipe == nil {
		t.Fatal("Get('QUICK-CLAUDE') returned nil (case-insensitive)")
	}
}

func TestLoaderGet_NotFound(t *testing.T) {
	t.Parallel()
	loader := NewLoader()

	_, err := loader.Get("nonexistent-recipe-xyz")
	if err == nil {
		t.Error("expected error for non-existent recipe")
	}
}

func TestLoadFromFile_ValidFile(t *testing.T) {
	t.Parallel()
	// Create a temp directory with a valid recipes.toml
	tmpDir := t.TempDir()
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	content := `
[[recipes]]
name = "test-recipe"
description = "A test recipe"
[[recipes.agents]]
type = "cc"
count = 2
`
	if err := os.WriteFile(recipesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	recipes, err := loadFromFile(recipesPath, "test")
	if err != nil {
		t.Fatalf("loadFromFile() error: %v", err)
	}
	if len(recipes) != 1 {
		t.Errorf("expected 1 recipe, got %d", len(recipes))
	}
	if recipes[0].Name != "test-recipe" {
		t.Errorf("expected name 'test-recipe', got %q", recipes[0].Name)
	}
	if recipes[0].Source != "test" {
		t.Errorf("expected source 'test', got %q", recipes[0].Source)
	}
}

func TestLoadFromFile_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	content := `
legacy = true

[[recipes]]
name = "test-recipe"
description = "A test recipe"
[[recipes.agents]]
type = "cc"
count = 1
`
	if err := os.WriteFile(recipesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write recipes.toml: %v", err)
	}

	_, err := loadFromFile(recipesPath, "project")
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if !strings.Contains(err.Error(), "unknown field(s): legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadFromFile_NormalizesBuiltInAliases(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	content := `
[[recipes]]
name = "alias-recipe"
description = "A test recipe"
[[recipes.agents]]
type = "claude"
count = 1
[[recipes.agents]]
type = "openai-codex"
count = 2
[[recipes.agents]]
type = "ws"
count = 1
[[recipes.agents]]
type = " xai_grok_build "
count = 1
model = " grok-4 "
reasoning_effort = " high "
`
	if err := os.WriteFile(recipesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	recipes, err := loadFromFile(recipesPath, "test")
	if err != nil {
		t.Fatalf("loadFromFile() error: %v", err)
	}
	if got := recipes[0].Agents[0].Type; got != "cc" {
		t.Fatalf("recipes[0].Agents[0].Type = %q, want %q", got, "cc")
	}
	if got := recipes[0].Agents[1].Type; got != "cod" {
		t.Fatalf("recipes[0].Agents[1].Type = %q, want %q", got, "cod")
	}
	if got := recipes[0].Agents[2].Type; got != "windsurf" {
		t.Fatalf("recipes[0].Agents[2].Type = %q, want %q", got, "windsurf")
	}
	if got := recipes[0].Agents[3]; got.Type != "grok" || got.Model != "grok-4" || got.ReasoningEffort != "high" {
		t.Fatalf("recipes[0].Agents[3] = %+v, want normalized Grok model and effort", got)
	}
}

func TestLoadFromFile_NonExistent(t *testing.T) {
	t.Parallel()
	_, err := loadFromFile("/nonexistent/path/recipes.toml", "test")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestLoadFromFile_InvalidTOML(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	content := `this is not valid TOML {{{{`
	if err := os.WriteFile(recipesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := loadFromFile(recipesPath, "test")
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadFromFile_InvalidRecipeType(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	content := `
[[recipes]]
name = "bad-recipe"
description = "A bad recipe"
[[recipes.agents]]
type = "user"
count = 1
`
	if err := os.WriteFile(recipesPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := loadFromFile(recipesPath, "test")
	if err == nil {
		t.Fatal("expected error for unsupported recipe agent type")
	}
}

func TestRecipeValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		recipe  Recipe
		wantErr bool
	}{
		{
			name: "valid_recipe",
			recipe: Recipe{
				Name: "test",
				Agents: []AgentSpec{
					{Type: "cc", Count: 1},
				},
			},
			wantErr: false,
		},
		{
			name: "missing_name",
			recipe: Recipe{
				Agents: []AgentSpec{
					{Type: "cc", Count: 1},
				},
			},
			wantErr: true,
		},
		{
			name: "no_agents",
			recipe: Recipe{
				Name:   "test",
				Agents: []AgentSpec{},
			},
			wantErr: true,
		},
		{
			name: "invalid_agent",
			recipe: Recipe{
				Name: "test",
				Agents: []AgentSpec{
					{Type: "", Count: 1}, // Missing type
				},
			},
			wantErr: true,
		},
		{
			name: "too_many_agents",
			recipe: Recipe{
				Name: "test",
				Agents: []AgentSpec{
					{Type: "cc", Count: 20},
					{Type: "cod", Count: 20},
					{Type: "gmi", Count: 20}, // Total: 60 > 50
				},
			},
			wantErr: true,
		},
		{
			name: "max_agents",
			recipe: Recipe{
				Name: "test",
				Agents: []AgentSpec{
					{Type: "cc", Count: 20},
					{Type: "cod", Count: 20},
					{Type: "gmi", Count: 10}, // Total: 50 (exactly max)
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.recipe.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Recipe.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoaderLoadAll_WithProjectRecipes(t *testing.T) {
	// Create a temp project directory with recipes
	tmpDir := t.TempDir()
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("failed to create .ntm dir: %v", err)
	}

	content := `
[[recipes]]
name = "project-custom"
description = "Project-specific recipe"
[[recipes.agents]]
type = "cc"
count = 3
`
	if err := os.WriteFile(filepath.Join(ntmDir, "recipes.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write recipes.toml: %v", err)
	}

	loader := &Loader{
		UserConfigDir: t.TempDir(), // Empty user config
		ProjectDir:    tmpDir,
	}

	recipes, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	// Should find the project recipe
	found := false
	for _, r := range recipes {
		if r.Name == "project-custom" {
			found = true
			if r.Source != "project" {
				t.Errorf("expected source 'project', got %q", r.Source)
			}
			break
		}
	}
	if !found {
		t.Error("project recipe 'project-custom' not found in LoadAll results")
	}
}

func TestLoaderLoadAll_ErrorsOnInvalidProjectRecipes(t *testing.T) {
	tmpDir := t.TempDir()
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("failed to create .ntm dir: %v", err)
	}

	content := `
[[recipes]]
name = "broken-project-recipe"
description = "Project-specific recipe"
[[recipes.agents]]
type = "user"
count = 1
`
	if err := os.WriteFile(filepath.Join(ntmDir, "recipes.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write recipes.toml: %v", err)
	}

	loader := &Loader{
		UserConfigDir: t.TempDir(),
		ProjectDir:    tmpDir,
	}

	_, err := loader.LoadAll()
	if err == nil {
		t.Fatal("expected invalid project recipes to fail")
	}
	if !strings.Contains(err.Error(), "unsupported recipe agent type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderLoadAll_ErrorsOnInvalidUserRecipes(t *testing.T) {
	userDir := t.TempDir()
	content := `
[[recipes]]
name = "broken-user-recipe"
description = "User recipe"
[[recipes.agents]]
type = "nope"
count = 1
`
	if err := os.WriteFile(filepath.Join(userDir, "recipes.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write recipes.toml: %v", err)
	}

	loader := &Loader{
		UserConfigDir: userDir,
		ProjectDir:    t.TempDir(),
	}

	_, err := loader.LoadAll()
	if err == nil {
		t.Fatal("expected invalid user recipes to fail")
	}
	if !strings.Contains(err.Error(), "unsupported recipe agent type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTotalAgents_EmptyRecipe(t *testing.T) {
	t.Parallel()
	r := Recipe{Agents: []AgentSpec{}}
	if total := r.TotalAgents(); total != 0 {
		t.Errorf("TotalAgents() = %d, want 0", total)
	}
}

func TestAgentCounts_EmptyRecipe(t *testing.T) {
	t.Parallel()
	r := Recipe{Agents: []AgentSpec{}}
	counts := r.AgentCounts()
	if len(counts) != 0 {
		t.Errorf("AgentCounts() len = %d, want 0", len(counts))
	}
}

func TestAgentCounts_MultipleOfSameType(t *testing.T) {
	t.Parallel()
	r := Recipe{
		Agents: []AgentSpec{
			{Type: "cc", Count: 2},
			{Type: "cc", Count: 3}, // Same type, should sum
		},
	}
	counts := r.AgentCounts()
	if counts["cc"] != 5 {
		t.Errorf("AgentCounts[cc] = %d, want 5", counts["cc"])
	}
}
