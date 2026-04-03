package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestDiscoverConfigs(t *testing.T) {
	// Test that discoverConfigs returns at least the main config
	locations := discoverConfigs(false)

	if len(locations) == 0 {
		t.Fatal("discoverConfigs should return at least the main config")
	}

	// First location should be the main config
	if locations[0].Type != "main" {
		t.Errorf("first location should be main config, got %s", locations[0].Type)
	}
}

func TestDiscoverConfigsAll(t *testing.T) {
	// Test that --all flag includes more locations
	normalLocations := discoverConfigs(false)
	allLocations := discoverConfigs(true)

	if len(allLocations) < len(normalLocations) {
		t.Error("--all should return at least as many locations as normal mode")
	}
}

func TestDiscoverConfigsUsesSelectedConfigPath(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	cfgFile = customPath

	locations := discoverConfigs(false)
	if len(locations) == 0 {
		t.Fatal("discoverConfigs should return at least the main config")
	}
	if locations[0].Type != "main" {
		t.Fatalf("first location type = %q, want main", locations[0].Type)
	}
	if locations[0].Path != customPath {
		t.Fatalf("main config path = %q, want %q", locations[0].Path, customPath)
	}
}

func TestValidateConfigFile_NonExistent(t *testing.T) {
	loc := ConfigLocation{
		Path:   "/nonexistent/path/config.toml",
		Type:   "main",
		Exists: false,
	}

	result := validateConfigFile(loc, false)

	if !result.Valid {
		t.Error("non-existent file should be valid (just informational)")
	}
	if len(result.Info) == 0 {
		t.Error("non-existent file should have info message")
	}
}

func TestValidateRecipesFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-validate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("valid recipes file", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "recipes.toml")
		content := `
[[recipes]]
name = "test-recipe"
description = "A test recipe"
[[recipes.agents]]
type = "claude"
count = 2
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) > 0 {
			t.Errorf("valid recipes file should pass: errors=%v", result.Errors)
		}
		if len(result.Warnings) > 0 {
			t.Errorf("valid recipes file should not warn: warnings=%v", result.Warnings)
		}
	})

	t.Run("invalid TOML syntax", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "bad-recipes.toml")
		content := `[test-recipe
description = "missing closing bracket"
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) == 0 {
			t.Error("invalid TOML syntax should produce errors")
		}
	})

	t.Run("legacy wrong schema errors", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "legacy-recipes.toml")
		content := `[test-recipe]
description = "A test recipe"
steps = ["step1", "step2"]
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) == 0 {
			t.Fatal("legacy schema should produce errors")
		}
	})

	t.Run("missing description warns", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "incomplete-recipes.toml")
		content := `
[[recipes]]
name = "incomplete-recipe"
[[recipes.agents]]
type = "cc"
count = 1
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) != 0 {
			t.Fatalf("missing description should not error: %v", result.Errors)
		}
		if len(result.Warnings) == 0 {
			t.Error("missing description should produce warnings")
		}
	})

	t.Run("unsupported agent type errors", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "unsupported-recipes.toml")
		content := `
[[recipes]]
name = "bad-recipe"
description = "bad"
[[recipes.agents]]
type = "user"
count = 1
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) == 0 {
			t.Fatal("unsupported agent type should produce errors")
		}
	})

	t.Run("unknown recipe field errors", func(t *testing.T) {
		recipePath := filepath.Join(tmpDir, "unknown-field-recipes.toml")
		content := `
legacy = true

[[recipes]]
name = "strict-recipe"
description = "strict"
[[recipes.agents]]
type = "cc"
count = 1
`
		if err := os.WriteFile(recipePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validateRecipesFile(recipePath, result)

		if len(result.Errors) == 0 {
			t.Fatal("unknown recipe field should produce errors")
		}
		if !strings.Contains(result.Errors[0].Message, "unknown field(s): legacy") {
			t.Fatalf("unexpected error: %v", result.Errors[0].Message)
		}
	})
}

func TestValidatePersonasFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-validate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("valid personas file", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "personas.toml")
		content := `
[[personas]]
name = "developer"
agent_type = "claude"
system_prompt = "You are a developer."
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) > 0 {
			t.Errorf("valid personas file should pass: errors=%v", result.Errors)
		}
		if len(result.Warnings) > 0 {
			t.Errorf("valid personas file should not warn: warnings=%v", result.Warnings)
		}
	})

	t.Run("legacy wrong schema errors", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "legacy-personas.toml")
		content := `[developer]
system_prompt = "You are a developer."
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) == 0 {
			t.Fatal("legacy persona schema should produce errors")
		}
	})

	t.Run("missing system_prompt warns", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "incomplete-personas.toml")
		content := `
[[personas]]
name = "incomplete-persona"
agent_type = "claude"
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) != 0 {
			t.Fatalf("missing system_prompt should not error: %v", result.Errors)
		}
		if len(result.Warnings) == 0 {
			t.Error("missing system_prompt should produce warnings")
		}
	})

	t.Run("missing system_prompt allowed when extending", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "extending-personas.toml")
		content := `
[[personas]]
name = "base"
agent_type = "claude"
system_prompt = "Base prompt"

[[personas]]
name = "child"
extends = "base"
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) != 0 {
			t.Fatalf("extending persona should not error: %v", result.Errors)
		}
		for _, warning := range result.Warnings {
			if warning.Field == "child" {
				t.Fatalf("extending persona should not warn about missing system_prompt: %+v", warning)
			}
		}
	})

	t.Run("project persona can extend user persona", func(t *testing.T) {
		xdgDir := filepath.Join(tmpDir, "xdg")
		userDir := filepath.Join(xdgDir, "ntm")
		projectDir := filepath.Join(tmpDir, "project")
		if err := os.MkdirAll(userDir, 0755); err != nil {
			t.Fatalf("failed to create user persona dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
			t.Fatalf("failed to create project persona dir: %v", err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		t.Setenv("NTM_CONFIG", filepath.Join(tmpDir, "custom-root", "config.toml"))

		userContent := `
[[personas]]
name = "base"
agent_type = "claude"
system_prompt = "Base prompt"
`
		if err := os.WriteFile(filepath.Join(userDir, "personas.toml"), []byte(userContent), 0644); err != nil {
			t.Fatalf("failed to write user personas: %v", err)
		}

		projectPath := filepath.Join(projectDir, ".ntm", "personas.toml")
		projectContent := `
[[personas]]
name = "child"
extends = "base"
`
		if err := os.WriteFile(projectPath, []byte(projectContent), 0644); err != nil {
			t.Fatalf("failed to write project personas: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(projectPath, result)

		if len(result.Errors) != 0 {
			t.Fatalf("project persona extending user persona should not error: %v", result.Errors)
		}
	})

	t.Run("project persona reports invalid active user personas file", func(t *testing.T) {
		projectDir := filepath.Join(tmpDir, "invalid-user-project")
		customRoot := filepath.Join(tmpDir, "custom-root-invalid")
		if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
			t.Fatalf("failed to create project persona dir: %v", err)
		}
		if err := os.MkdirAll(customRoot, 0755); err != nil {
			t.Fatalf("failed to create custom config dir: %v", err)
		}
		t.Setenv("NTM_CONFIG", filepath.Join(customRoot, "config.toml"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "unused-xdg"))

		userContent := `
[[personas]]
name = "broken"
agent_type = "claude"
legacy = true
`
		if err := os.WriteFile(filepath.Join(customRoot, "personas.toml"), []byte(userContent), 0644); err != nil {
			t.Fatalf("failed to write invalid user personas: %v", err)
		}

		projectPath := filepath.Join(projectDir, ".ntm", "personas.toml")
		projectContent := `
[[personas]]
name = "child"
extends = "base"
`
		if err := os.WriteFile(projectPath, []byte(projectContent), 0644); err != nil {
			t.Fatalf("failed to write project personas: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(projectPath, result)

		if len(result.Errors) == 0 {
			t.Fatal("invalid active user personas file should produce errors")
		}
		if !strings.Contains(result.Errors[0].Message, "loading user personas") {
			t.Fatalf("unexpected error: %v", result.Errors[0].Message)
		}
		if !strings.Contains(result.Errors[0].Message, "legacy") {
			t.Fatalf("unexpected error: %v", result.Errors[0].Message)
		}
	})

	t.Run("persona set missing member errors", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "broken-persona-set.toml")
		content := `
[[personas]]
name = "dev"
agent_type = "claude"
system_prompt = "Dev"

[[persona_sets]]
name = "team"
personas = ["dev", "missing"]
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) == 0 {
			t.Fatal("broken persona set should produce errors")
		}
	})

	t.Run("duplicate persona set errors", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, "duplicate-persona-sets.toml")
		content := `
[[personas]]
name = "dev"
agent_type = "claude"
system_prompt = "Dev"

[[persona_sets]]
name = "team"
personas = ["dev"]

[[persona_sets]]
name = "TEAM"
personas = ["dev"]
`
		if err := os.WriteFile(personaPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePersonasFile(personaPath, result)

		if len(result.Errors) == 0 {
			t.Fatal("duplicate persona set should produce errors")
		}
	})
}

func TestValidatePolicyFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-validate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("valid policy file", func(t *testing.T) {
		policyPath := filepath.Join(tmpDir, "policy.yaml")
		content := `version: 1
blocked:
  - pattern: "rm -rf /"
    reason: "dangerous"
`
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}}
		validatePolicyFile(policyPath, result)

		if !result.Valid || len(result.Errors) > 0 || len(result.Warnings) > 0 {
			t.Errorf("valid policy file should pass cleanly: errors=%v warnings=%v", result.Errors, result.Warnings)
		}
	})

	t.Run("invalid YAML syntax", func(t *testing.T) {
		policyPath := filepath.Join(tmpDir, "bad-policy.yaml")
		content := `version: 1
rules:
  - name: test
    action: [invalid: yaml: syntax
`
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}}
		validatePolicyFile(policyPath, result)

		if len(result.Errors) == 0 {
			t.Error("invalid YAML syntax should produce errors")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		policyPath := filepath.Join(tmpDir, "incomplete-policy.yaml")
		content := `version: 1
blocked:
  - pattern: "rm -rf /"
legacy: true
`
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePolicyFile(policyPath, result)

		if len(result.Errors) == 0 {
			t.Error("unknown policy field should produce errors")
		}
	})

	t.Run("no version and no rules warnings", func(t *testing.T) {
		policyPath := filepath.Join(tmpDir, "warning-policy.yaml")
		content := `# empty policy
`
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
		validatePolicyFile(policyPath, result)

		if len(result.Errors) != 0 {
			t.Fatalf("empty policy should warn, not error: %v", result.Errors)
		}
		if len(result.Warnings) < 2 {
			t.Fatalf("expected version and no-rules warnings, got %v", result.Warnings)
		}
	})
}

func TestValidationResult_Valid(t *testing.T) {
	t.Run("valid when no errors", func(t *testing.T) {
		result := ValidationResult{
			Path:     "/test/path",
			Type:     "main",
			Errors:   []ValidationIssue{},
			Warnings: []ValidationIssue{{Message: "some warning"}},
		}
		// Note: Valid is set by validateConfigFile, not auto-computed
		result.Valid = len(result.Errors) == 0

		if !result.Valid {
			t.Error("result should be valid when there are no errors (warnings OK)")
		}
	})

	t.Run("invalid when errors exist", func(t *testing.T) {
		result := ValidationResult{
			Path:   "/test/path",
			Type:   "main",
			Errors: []ValidationIssue{{Message: "some error"}},
		}
		result.Valid = len(result.Errors) == 0

		if result.Valid {
			t.Error("result should be invalid when there are errors")
		}
	})
}

func TestFileAndDirExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-validate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test file
	testFile := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Test fileExists (from safety.go - checks path existence, not file vs dir)
	if !fileExists(testFile) {
		t.Error("fileExists should return true for existing file")
	}
	if fileExists(filepath.Join(tmpDir, "nonexistent.txt")) {
		t.Error("fileExists should return false for non-existent file")
	}
	// Note: fileExists in safety.go returns true for directories too (just checks existence)
	if !fileExists(tmpDir) {
		t.Error("fileExists should return true for existing paths (including directories)")
	}

	// Test dirExists (checks specifically for directories)
	if !dirExists(tmpDir) {
		t.Error("dirExists should return true for existing directory")
	}
	if dirExists(testFile) {
		t.Error("dirExists should return false for files")
	}
	if dirExists(filepath.Join(tmpDir, "nonexistent")) {
		t.Error("dirExists should return false for non-existent path")
	}
}

func TestValidateProjectConfig_AllowsRootPaletteFallback(t *testing.T) {
	tmpDir := t.TempDir()
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "palette.md"), []byte("# palette"), 0644); err != nil {
		t.Fatalf("write root palette: %v", err)
	}
	configPath := filepath.Join(ntmDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[palette]\nfile = \"palette.md\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
	validateProjectConfig(configPath, result, false)

	for _, warning := range result.Warnings {
		if warning.Field == "palette.file" {
			t.Fatalf("unexpected palette.file warning: %+v", warning)
		}
	}
}

func TestValidateProjectConfig_DoesNotCreateUnsafeTemplatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	configPath := filepath.Join(ntmDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[templates]\ndir = \"../outside\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
	validateProjectConfig(configPath, result, true)

	outsidePath := filepath.Join(tmpDir, "outside")
	if _, err := os.Stat(outsidePath); err == nil {
		t.Fatalf("validateProjectConfig created unsafe directory: %s", outsidePath)
	}

	foundWarning := false
	for _, warning := range result.Warnings {
		if warning.Field == "templates.dir" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatal("expected templates.dir warning for unsafe path")
	}
}

func TestValidateMainConfigReferences_WarnsWhenPaletteFileIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.PaletteFile = tmpDir

	result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
	validateMainConfigReferences(cfg, result, false)

	foundWarning := false
	for _, warning := range result.Warnings {
		if warning.Field == "palette_file" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatal("expected palette_file warning when configured path is a directory")
	}
}

func TestValidateMainConfigReferences_WarnsWhenPromptFileIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Send.BasePromptFile = tmpDir

	result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
	validateMainConfigReferences(cfg, result, false)

	foundWarning := false
	for _, warning := range result.Warnings {
		if warning.Field == "send.base_prompt_file" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatal("expected send.base_prompt_file warning when configured path is a directory")
	}
}

func TestValidateMainConfigReferences_WarnsWhenConfiguredBinaryPathIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Integrations.RCH.BinaryPath = tmpDir

	result := &ValidationResult{Valid: true, Errors: []ValidationIssue{}, Warnings: []ValidationIssue{}}
	validateMainConfigReferences(cfg, result, false)

	foundWarning := false
	for _, warning := range result.Warnings {
		if warning.Field == "integrations.rch.binary_path" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatal("expected integrations.rch.binary_path warning when configured path is a directory")
	}
}
