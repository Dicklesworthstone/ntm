package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestConfigGenerateAgentCommand(t *testing.T) {
	// Test no template
	cmd, err := GenerateAgentCommand("simple command", AgentTemplateVars{})
	if err != nil {
		t.Fatalf("GenerateAgentCommand failed: %v", err)
	}
	if cmd != "simple command" {
		t.Errorf("Expected 'simple command', got %q", cmd)
	}

	// Test with template
	tmpl := "echo {{.Model}}"
	vars := AgentTemplateVars{Model: "gpt-4"}
	cmd, err = GenerateAgentCommand(tmpl, vars)
	if err != nil {
		t.Fatalf("GenerateAgentCommand failed: %v", err)
	}
	if cmd != "echo gpt-4" {
		t.Errorf("Expected 'echo gpt-4', got %q", cmd)
	}
}

func TestIsPersonaName(t *testing.T) {
	cfg := &Config{}
	// Currently always returns false
	if cfg.IsPersonaName("architect") {
		t.Error("IsPersonaName should return false (not implemented)")
	}
}

func TestDetectPalettePath(t *testing.T) {
	// Test explicit path
	cfg := &Config{PaletteFile: "/custom/path.md"}
	if path := DetectPalettePath(cfg); path != "/custom/path.md" {
		t.Errorf("Expected /custom/path.md, got %s", path)
	}

	// Test nil config
	if path := DetectPalettePath(nil); path != "" {
		t.Errorf("Expected empty path for nil config, got %s", path)
	}
}

func TestScannerDefaultsGetTimeout(t *testing.T) {
	d := ScannerDefaults{Timeout: "60s"}
	if d.GetTimeout() != 60*time.Second {
		t.Errorf("Expected 60s, got %v", d.GetTimeout())
	}

	d = ScannerDefaults{Timeout: "invalid"}
	if d.GetTimeout() != 120*time.Second {
		t.Errorf("Expected default 120s for invalid, got %v", d.GetTimeout())
	}

	d = ScannerDefaults{Timeout: ""}
	if d.GetTimeout() != 120*time.Second {
		t.Errorf("Expected default 120s for empty, got %v", d.GetTimeout())
	}
}

func TestScannerToolsIsToolEnabled(t *testing.T) {
	// Default (empty) -> all enabled
	tools := ScannerTools{}
	if !tools.IsToolEnabled("semgrep") {
		t.Error("Empty config should enable all tools")
	}

	// Enabled list
	tools = ScannerTools{Enabled: []string{"semgrep"}}
	if !tools.IsToolEnabled("semgrep") {
		t.Error("Explicitly enabled tool should be enabled")
	}
	if tools.IsToolEnabled("gosec") {
		t.Error("Tool not in enabled list should be disabled")
	}

	// Disabled list
	tools = ScannerTools{Disabled: []string{"bandit"}}
	if tools.IsToolEnabled("bandit") {
		t.Error("Disabled tool should be disabled")
	}
	if !tools.IsToolEnabled("semgrep") {
		t.Error("Non-disabled tool should be enabled")
	}
}

func TestThresholdConfigShouldBlock(t *testing.T) {
	t.Run("block critical", func(t *testing.T) {
		tc := ThresholdConfig{BlockCritical: true}
		if !tc.ShouldBlock(1, 0) {
			t.Error("Should block on critical")
		}
		if tc.ShouldBlock(0, 5) {
			t.Error("Should not block on errors when BlockErrors=0")
		}
	})

	t.Run("block errors", func(t *testing.T) {
		tc := ThresholdConfig{BlockErrors: 5}
		if !tc.ShouldBlock(0, 5) {
			t.Error("Should block on 5 errors")
		}
		if tc.ShouldBlock(0, 4) {
			t.Error("Should not block on 4 errors")
		}
	})
}

func TestThresholdConfigShouldFail(t *testing.T) {
	t.Run("fail critical", func(t *testing.T) {
		tc := ThresholdConfig{FailCritical: true}
		if !tc.ShouldFail(1, 0) {
			t.Error("Should fail on critical")
		}
	})

	t.Run("fail errors", func(t *testing.T) {
		tc := ThresholdConfig{FailErrors: 0} // Any error fails
		if !tc.ShouldFail(0, 1) {
			t.Error("Should fail on 1 error")
		}

		tc = ThresholdConfig{FailErrors: -1} // Disabled
		if tc.ShouldFail(0, 100) {
			t.Error("Should not fail when disabled")
		}
	})
}

func TestLoadProjectScannerConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Test no config
	cfg, err := LoadProjectScannerConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadProjectScannerConfig failed: %v", err)
	}
	// Should return defaults
	if cfg.Defaults.Timeout != "120s" {
		t.Errorf("Expected default timeout 120s, got %s", cfg.Defaults.Timeout)
	}

	// Test .ntm.yaml
	yamlContent := `
scanner:
  defaults:
    timeout: 30s
`
	os.WriteFile(filepath.Join(tmpDir, ".ntm.yaml"), []byte(yamlContent), 0644)

	cfg, err = LoadProjectScannerConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadProjectScannerConfig failed: %v", err)
	}
	if cfg.Defaults.Timeout != "30s" {
		t.Errorf("Expected timeout 30s from yaml, got %s", cfg.Defaults.Timeout)
	}
}

func TestInitProjectConfigForce(t *testing.T) {
	tmpDir := t.TempDir()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("InitProjectConfig failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".ntm", "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config to exist at %s: %v", configPath, err)
	}

	palettePath := filepath.Join(tmpDir, ".ntm", "palette.md")
	if err := os.WriteFile(palettePath, []byte("custom palette\n"), 0644); err != nil {
		t.Fatalf("writing palette: %v", err)
	}

	if err := os.WriteFile(configPath, []byte("custom config\n"), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("expected InitProjectConfig to succeed without force when config exists: %v", err)
	}

	if err := InitProjectConfig(true); err != nil {
		t.Fatalf("InitProjectConfig(force) failed: %v", err)
	}

	configContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if strings.TrimSpace(string(configContent)) == "custom config" {
		t.Fatalf("expected config.toml to be overwritten when force=true")
	}

	// Ensure non-force run preserved the custom config before force overwrite.
	if err := os.WriteFile(configPath, []byte("custom config\n"), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("expected InitProjectConfig to succeed without force when config exists: %v", err)
	}
	configContent, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if strings.TrimSpace(string(configContent)) != "custom config" {
		t.Fatalf("expected config.toml to be preserved when force=false")
	}

	paletteContent, err := os.ReadFile(palettePath)
	if err != nil {
		t.Fatalf("reading palette: %v", err)
	}
	if strings.TrimSpace(string(paletteContent)) != "custom palette" {
		t.Fatalf("expected palette.md to be preserved when force=true")
	}
}

func TestInitProjectConfigScaffolding(t *testing.T) {
	tmpDir := t.TempDir()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("InitProjectConfig failed: %v", err)
	}

	t.Run("creates .ntm directory", func(t *testing.T) {
		ntmDir := filepath.Join(tmpDir, ".ntm")
		info, err := os.Stat(ntmDir)
		if err != nil {
			t.Fatalf("expected .ntm directory: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("expected .ntm to be a directory")
		}
	})

	t.Run("creates templates subdirectory", func(t *testing.T) {
		templatesDir := filepath.Join(tmpDir, ".ntm", "templates")
		info, err := os.Stat(templatesDir)
		if err != nil {
			t.Fatalf("expected templates directory: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("expected templates to be a directory")
		}
	})

	t.Run("creates pipelines subdirectory", func(t *testing.T) {
		pipelinesDir := filepath.Join(tmpDir, ".ntm", "pipelines")
		info, err := os.Stat(pipelinesDir)
		if err != nil {
			t.Fatalf("expected pipelines directory: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("expected pipelines to be a directory")
		}
	})

	t.Run("creates valid TOML config", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, ".ntm", "config.toml")
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("reading config: %v", err)
		}

		// Verify it's parseable as TOML
		var parsed map[string]interface{}
		if _, err := toml.Decode(string(content), &parsed); err != nil {
			t.Fatalf("config.toml is not valid TOML: %v", err)
		}
	})

	t.Run("creates palette.md with expected content", func(t *testing.T) {
		palettePath := filepath.Join(tmpDir, ".ntm", "palette.md")
		content, err := os.ReadFile(palettePath)
		if err != nil {
			t.Fatalf("reading palette: %v", err)
		}

		// Verify key sections exist
		contentStr := string(content)
		if !strings.Contains(contentStr, "# Project Commands") {
			t.Error("palette.md missing header")
		}
		if !strings.Contains(contentStr, "### build |") {
			t.Error("palette.md missing build command")
		}
		if !strings.Contains(contentStr, "### test |") {
			t.Error("palette.md missing test command")
		}
	})

	t.Run("creates personas.toml scaffold", func(t *testing.T) {
		personaPath := filepath.Join(tmpDir, ".ntm", "personas.toml")
		content, err := os.ReadFile(personaPath)
		if err != nil {
			t.Fatalf("reading personas.toml: %v", err)
		}
		if !strings.Contains(string(content), "Project personas for NTM") {
			t.Error("personas.toml missing header")
		}
	})

	t.Run("config contains expected sections", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, ".ntm", "config.toml")
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("reading config: %v", err)
		}

		contentStr := string(content)
		expectedSections := []string{"[project]", "[integrations]", "[defaults]", "[palette]", "[palette_state]", "[templates]"}
		for _, section := range expectedSections {
			if !strings.Contains(contentStr, section) {
				t.Errorf("config missing section: %s", section)
			}
		}
	})
}

func TestInitProjectConfigPreservesExistingPalette(t *testing.T) {
	tmpDir := t.TempDir()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Create .ntm directory and custom palette before init
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("creating .ntm: %v", err)
	}

	customPalette := "# My Custom Commands\n\n### deploy | Deploy App\nkubectl apply -f .\n"
	palettePath := filepath.Join(ntmDir, "palette.md")
	if err := os.WriteFile(palettePath, []byte(customPalette), 0644); err != nil {
		t.Fatalf("writing custom palette: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	// Init should NOT overwrite existing palette
	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("InitProjectConfig failed: %v", err)
	}

	content, err := os.ReadFile(palettePath)
	if err != nil {
		t.Fatalf("reading palette: %v", err)
	}

	if string(content) != customPalette {
		t.Errorf("expected custom palette to be preserved\ngot: %s\nwant: %s", string(content), customPalette)
	}
}

func TestInitProjectConfigDirectoryPermissions(t *testing.T) {
	tmpDir := t.TempDir()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	if err := InitProjectConfig(false); err != nil {
		t.Fatalf("InitProjectConfig failed: %v", err)
	}

	// Check directory permissions (should be 0755)
	ntmDir := filepath.Join(tmpDir, ".ntm")
	info, err := os.Stat(ntmDir)
	if err != nil {
		t.Fatalf("stat .ntm: %v", err)
	}
	// On Unix, check mode; on Windows, this check may behave differently
	mode := info.Mode().Perm()
	// Directory should be at least readable and executable by owner
	if mode&0500 != 0500 {
		t.Errorf("expected .ntm directory to be readable+executable, got %o", mode)
	}

	templatesDir := filepath.Join(ntmDir, "templates")
	info, err = os.Stat(templatesDir)
	if err != nil {
		t.Fatalf("stat templates: %v", err)
	}
	mode = info.Mode().Perm()
	if mode&0500 != 0500 {
		t.Errorf("expected templates directory to be readable+executable, got %o", mode)
	}
}

func TestFindProjectConfig(t *testing.T) {
	// Create temp directory hierarchy: /tmp/root/sub1/sub2
	tmpDir := t.TempDir()
	rootDir := filepath.Join(tmpDir, "root")
	sub1Dir := filepath.Join(rootDir, "sub1")
	sub2Dir := filepath.Join(sub1Dir, "sub2")

	if err := os.MkdirAll(sub2Dir, 0755); err != nil {
		t.Fatalf("creating directory hierarchy: %v", err)
	}

	// Create .ntm/config.toml at root level
	ntmDir := filepath.Join(rootDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("creating .ntm directory: %v", err)
	}

	configContent := `[defaults]
agents = { cc = 3, cod = 2 }

[agents]
claude = "claude --project test"
`
	configPath := filepath.Join(ntmDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	t.Run("finds config from same directory", func(t *testing.T) {
		foundDir, cfg, err := FindProjectConfig(rootDir)
		if err != nil {
			t.Fatalf("FindProjectConfig failed: %v", err)
		}
		if foundDir != rootDir {
			t.Errorf("expected foundDir=%s, got=%s", rootDir, foundDir)
		}
		if cfg == nil {
			t.Fatal("expected config to be non-nil")
		}
		if cfg.Agents.Claude != "claude --project test" {
			t.Errorf("expected claude command to be set, got=%s", cfg.Agents.Claude)
		}
	})

	t.Run("finds config from nested directory", func(t *testing.T) {
		foundDir, cfg, err := FindProjectConfig(sub2Dir)
		if err != nil {
			t.Fatalf("FindProjectConfig failed: %v", err)
		}
		if foundDir != rootDir {
			t.Errorf("expected foundDir=%s, got=%s", rootDir, foundDir)
		}
		if cfg == nil {
			t.Fatal("expected config to be non-nil")
		}
		if cfg.Defaults.Agents["cc"] != 3 {
			t.Errorf("expected cc=3, got=%d", cfg.Defaults.Agents["cc"])
		}
	})

	t.Run("returns nil when no config exists", func(t *testing.T) {
		emptyDir := t.TempDir()
		foundDir, cfg, err := FindProjectConfig(emptyDir)
		if err != nil {
			t.Fatalf("FindProjectConfig failed: %v", err)
		}
		if foundDir != "" {
			t.Errorf("expected empty foundDir, got=%s", foundDir)
		}
		if cfg != nil {
			t.Error("expected config to be nil")
		}
	})
}

func TestLoadMerged(t *testing.T) {
	// Create temp dirs for global and project configs
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Create global config
	globalConfigPath := filepath.Join(globalDir, "config.toml")
	globalContent := `[agents]
claude = "claude --global"
codex = "codex --global"

[layout]
default_layout = "global-layout"
`
	if err := os.WriteFile(globalConfigPath, []byte(globalContent), 0644); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	// Create project config
	ntmDir := filepath.Join(projectDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("creating .ntm directory: %v", err)
	}

	projectContent := `[defaults]
agents = { cc = 4, cod = 1 }

[agents]
claude = "claude --project-override"
`
	projectConfigPath := filepath.Join(ntmDir, "config.toml")
	if err := os.WriteFile(projectConfigPath, []byte(projectContent), 0644); err != nil {
		t.Fatalf("writing project config: %v", err)
	}

	t.Run("merges global and project config", func(t *testing.T) {
		cfg, err := LoadMerged(projectDir, globalConfigPath)
		if err != nil {
			t.Fatalf("LoadMerged failed: %v", err)
		}

		// SECURITY: Project should NOT override agent commands (RCE prevention)
		// Agent commands are only loaded from global/user config, never from
		// project repos to prevent malicious repositories from executing
		// arbitrary commands.
		if cfg.Agents.Claude != "claude --global" {
			t.Errorf("expected global claude (agent override disabled for security), got=%s", cfg.Agents.Claude)
		}

		// Global codex should be preserved
		if cfg.Agents.Codex != "codex --global" {
			t.Errorf("expected global codex, got=%s", cfg.Agents.Codex)
		}

		// Project defaults (agent counts) SHOULD still be set - this is safe
		if cfg.ProjectDefaults["cc"] != 4 {
			t.Errorf("expected cc=4, got=%d", cfg.ProjectDefaults["cc"])
		}
	})

	t.Run("uses defaults when global config missing", func(t *testing.T) {
		cfg, err := LoadMerged(projectDir, filepath.Join(globalDir, "nonexistent.toml"))
		if err != nil {
			t.Fatalf("LoadMerged failed: %v", err)
		}
		// SECURITY: Project should NOT override agent commands (RCE prevention)
		// When global config is missing, default agent commands are used,
		// NOT project-specified commands. This prevents malicious repositories
		// from specifying arbitrary agent commands.
		defaultCfg := Default()
		if cfg.Agents.Claude != defaultCfg.Agents.Claude {
			t.Errorf("expected default claude command, got=%s", cfg.Agents.Claude)
		}
		// But project defaults (agent counts) should still be merged
		if cfg.ProjectDefaults["cc"] != 4 {
			t.Errorf("expected project defaults cc=4, got=%d", cfg.ProjectDefaults["cc"])
		}
	})

	t.Run("returns error for invalid project config", func(t *testing.T) {
		badProjectDir := t.TempDir()
		badNtmDir := filepath.Join(badProjectDir, ".ntm")
		os.MkdirAll(badNtmDir, 0755)
		os.WriteFile(filepath.Join(badNtmDir, "config.toml"), []byte("invalid { toml"), 0644)

		_, err := LoadMerged(badProjectDir, globalConfigPath)
		if err == nil {
			t.Fatal("expected error for invalid project config")
		}
		if !strings.Contains(err.Error(), "project config") {
			t.Errorf("expected error to mention project config, got=%v", err)
		}
	})

	t.Run("returns error for invalid global config", func(t *testing.T) {
		badGlobalPath := filepath.Join(globalDir, "bad-config.toml")
		if err := os.WriteFile(badGlobalPath, []byte("invalid { toml"), 0644); err != nil {
			t.Fatalf("writing bad global config: %v", err)
		}

		_, err := LoadMerged(projectDir, badGlobalPath)
		if err == nil {
			t.Fatal("expected error for invalid global config")
		}
		if !strings.Contains(err.Error(), "global config") {
			t.Errorf("expected error to mention global config, got=%v", err)
		}
	})
}

func TestMergeConfig(t *testing.T) {
	t.Run("project does NOT override global agents for security", func(t *testing.T) {
		// SECURITY: Agent command overrides from project configs are disabled
		// to prevent RCE from malicious repositories. Project configs can
		// specify agent COUNTS (defaults.agents) but NOT agent COMMANDS.
		global := &Config{
			Agents: AgentConfig{
				Claude: "claude-global",
				Codex:  "codex-global",
				Gemini: "gemini-global",
			},
		}
		project := &ProjectConfig{
			Agents: AgentConfig{
				Claude: "claude-project", // This SHOULD be ignored for security
			},
		}

		result := MergeConfig(global, project, "/project")
		// Agent commands should NOT be overridden (security feature)
		if result.Agents.Claude != "claude-global" {
			t.Errorf("expected claude-global (agent override disabled), got=%s", result.Agents.Claude)
		}
		if result.Agents.Codex != "codex-global" {
			t.Errorf("expected codex-global to be preserved, got=%s", result.Agents.Codex)
		}
		if result.Agents.Gemini != "gemini-global" {
			t.Errorf("expected gemini-global to be preserved, got=%s", result.Agents.Gemini)
		}
	})

	t.Run("project defaults override global defaults", func(t *testing.T) {
		global := &Config{
			ProjectDefaults: map[string]int{"cc": 1, "cod": 1},
		}
		project := &ProjectConfig{
			Defaults: ProjectDefaults{
				Agents: map[string]int{"cc": 5},
			},
		}

		result := MergeConfig(global, project, "/project")
		if result.ProjectDefaults["cc"] != 5 {
			t.Errorf("expected cc=5, got=%d", result.ProjectDefaults["cc"])
		}
	})

	t.Run("ignores unsafe palette paths", func(t *testing.T) {
		global := &Config{}
		project := &ProjectConfig{
			Palette: ProjectPalette{
				File: "../../../etc/passwd",
			},
		}

		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		os.Stdout = w
		t.Cleanup(func() {
			os.Stdout = oldStdout
		})

		// Should not panic or error, just ignore, and must not corrupt stdout.
		result := MergeConfig(global, project, "/project")
		if err := w.Close(); err != nil {
			t.Fatalf("closing writer: %v", err)
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatalf("reading stdout: %v", err)
		}
		if len(result.Palette) != 0 {
			t.Errorf("expected empty palette for unsafe path, got=%d commands", len(result.Palette))
		}
		if buf.Len() != 0 {
			t.Errorf("expected no stdout output for unsafe palette path, got %q", buf.String())
		}
	})

	t.Run("merges palette state with project taking precedence", func(t *testing.T) {
		global := &Config{
			PaletteState: PaletteState{
				Pinned:    []string{"global-pin1", "shared-pin"},
				Favorites: []string{"global-fav"},
			},
		}
		project := &ProjectConfig{
			PaletteState: PaletteState{
				Pinned:    []string{"project-pin", "shared-pin"},
				Favorites: []string{"project-fav"},
			},
		}

		result := MergeConfig(global, project, "/project")

		// Project pins should come first, then unique global pins
		if len(result.PaletteState.Pinned) != 3 {
			t.Errorf("expected 3 pinned items, got=%d", len(result.PaletteState.Pinned))
		}
		if result.PaletteState.Pinned[0] != "project-pin" {
			t.Errorf("expected project-pin first, got=%s", result.PaletteState.Pinned[0])
		}

		// Favorites should follow same precedence
		if len(result.PaletteState.Favorites) != 2 {
			t.Errorf("expected 2 favorites, got=%d", len(result.PaletteState.Favorites))
		}
		if result.PaletteState.Favorites[0] != "project-fav" {
			t.Errorf("expected project-fav first, got=%s", result.PaletteState.Favorites[0])
		}
	})
}

func TestMergeConfig_ProjectAlerts(t *testing.T) {
	t.Run("explicit false overrides global enabled without zeroing thresholds", func(t *testing.T) {
		global := Default()
		enabled := false
		project := &ProjectConfig{
			Alerts: &ProjectAlerts{
				Enabled: &enabled,
			},
		}

		result := MergeConfig(global, project, "/project")
		if result.Alerts.Enabled {
			t.Fatal("expected project alerts.enabled=false to override global config")
		}
		if result.Alerts.AgentStuckMinutes != DefaultAlertsConfig().AgentStuckMinutes {
			t.Fatalf("AgentStuckMinutes = %d, want default %d", result.Alerts.AgentStuckMinutes, DefaultAlertsConfig().AgentStuckMinutes)
		}
		if result.Alerts.MailBacklogThreshold != DefaultAlertsConfig().MailBacklogThreshold {
			t.Fatalf("MailBacklogThreshold = %d, want default %d", result.Alerts.MailBacklogThreshold, DefaultAlertsConfig().MailBacklogThreshold)
		}
	})

	t.Run("partial project alert overrides preserve other global values", func(t *testing.T) {
		global := Default()
		global.Alerts.Enabled = true
		global.Alerts.AgentStuckMinutes = 7
		global.Alerts.DiskLowThresholdGB = 9.5
		global.Alerts.ContextWarningThreshold = 68.0

		beadStaleHours := 72
		project := &ProjectConfig{
			Alerts: &ProjectAlerts{
				BeadStaleHours: &beadStaleHours,
			},
		}

		result := MergeConfig(global, project, "/project")
		if !result.Alerts.Enabled {
			t.Fatal("expected unspecified alerts.enabled to preserve global true")
		}
		if result.Alerts.AgentStuckMinutes != 7 {
			t.Fatalf("AgentStuckMinutes = %d, want 7", result.Alerts.AgentStuckMinutes)
		}
		if result.Alerts.DiskLowThresholdGB != 9.5 {
			t.Fatalf("DiskLowThresholdGB = %v, want 9.5", result.Alerts.DiskLowThresholdGB)
		}
		if result.Alerts.ContextWarningThreshold != 68.0 {
			t.Fatalf("ContextWarningThreshold = %v, want 68.0", result.Alerts.ContextWarningThreshold)
		}
		if result.Alerts.BeadStaleHours != 72 {
			t.Fatalf("BeadStaleHours = %d, want 72", result.Alerts.BeadStaleHours)
		}
	})

	t.Run("project can override context warning threshold independently", func(t *testing.T) {
		global := Default()
		threshold := 91.5
		project := &ProjectConfig{
			Alerts: &ProjectAlerts{
				ContextWarningThreshold: &threshold,
			},
		}

		result := MergeConfig(global, project, "/project")
		if result.Alerts.ContextWarningThreshold != 91.5 {
			t.Fatalf("ContextWarningThreshold = %v, want 91.5", result.Alerts.ContextWarningThreshold)
		}
		if result.Alerts.AgentStuckMinutes != DefaultAlertsConfig().AgentStuckMinutes {
			t.Fatalf("AgentStuckMinutes = %d, want default %d", result.Alerts.AgentStuckMinutes, DefaultAlertsConfig().AgentStuckMinutes)
		}
	})
}

func TestMergeStringListPreferFirst(t *testing.T) {
	tests := []struct {
		name      string
		primary   []string
		secondary []string
		expected  []string
	}{
		{
			name:      "empty both",
			primary:   nil,
			secondary: nil,
			expected:  nil,
		},
		{
			name:      "primary only",
			primary:   []string{"a", "b"},
			secondary: nil,
			expected:  []string{"a", "b"},
		},
		{
			name:      "secondary only",
			primary:   nil,
			secondary: []string{"x", "y"},
			expected:  []string{"x", "y"},
		},
		{
			name:      "primary takes precedence on duplicates",
			primary:   []string{"a", "b"},
			secondary: []string{"b", "c"},
			expected:  []string{"a", "b", "c"},
		},
		{
			name:      "trims whitespace",
			primary:   []string{" a ", "  b"},
			secondary: []string{"c  "},
			expected:  []string{"a", "b", "c"},
		},
		{
			name:      "filters empty strings",
			primary:   []string{"a", "", "b"},
			secondary: []string{"", "c"},
			expected:  []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeStringListPreferFirst(tt.primary, tt.secondary)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got=%v", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("expected len=%d, got=%d", len(tt.expected), len(result))
				return
			}
			for i := range tt.expected {
				if result[i] != tt.expected[i] {
					t.Errorf("at index %d: expected=%s, got=%s", i, tt.expected[i], result[i])
				}
			}
		})
	}
}

func TestNormalizeSafetyProfile(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty defaults", in: "", want: SafetyProfileStandard},
		{name: "whitespace defaults", in: "   ", want: SafetyProfileStandard},
		{name: "standard canonical", in: SafetyProfileStandard, want: SafetyProfileStandard},
		{name: "safe canonical", in: SafetyProfileSafe, want: SafetyProfileSafe},
		{name: "paranoid canonical", in: SafetyProfileParanoid, want: SafetyProfileParanoid},
		{name: "case insensitive", in: "SAFE", want: SafetyProfileSafe},
		{name: "trims whitespace", in: "  paranoid  ", want: SafetyProfileParanoid},
		{name: "invalid falls back", in: "nope", want: SafetyProfileStandard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSafetyProfile(tt.in); got != tt.want {
				t.Fatalf("normalizeSafetyProfile(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplySafetyProfileDefaults_Mappings(t *testing.T) {
	tests := []struct {
		name             string
		profile          string
		wantProfile      string
		preflightEnabled bool
		preflightStrict  bool
		redactionMode    string
		privacyEnabled   bool
		dcgAllowOverride bool
	}{
		{
			name:             "standard",
			profile:          SafetyProfileStandard,
			wantProfile:      SafetyProfileStandard,
			preflightEnabled: true,
			preflightStrict:  false,
			redactionMode:    "warn",
			privacyEnabled:   false,
			dcgAllowOverride: true,
		},
		{
			name:             "safe",
			profile:          SafetyProfileSafe,
			wantProfile:      SafetyProfileSafe,
			preflightEnabled: true,
			preflightStrict:  false,
			redactionMode:    "redact",
			privacyEnabled:   false,
			dcgAllowOverride: false,
		},
		{
			name:             "paranoid",
			profile:          SafetyProfileParanoid,
			wantProfile:      SafetyProfileParanoid,
			preflightEnabled: true,
			preflightStrict:  true,
			redactionMode:    "block",
			privacyEnabled:   true,
			dcgAllowOverride: false,
		},
		{
			name:             "invalid falls back to standard",
			profile:          "NOPE",
			wantProfile:      SafetyProfileStandard,
			preflightEnabled: true,
			preflightStrict:  false,
			redactionMode:    "warn",
			privacyEnabled:   false,
			dcgAllowOverride: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Safety:    SafetyConfig{Profile: tt.profile},
				Redaction: RedactionConfig{Mode: "off"},
				Preflight: PreflightConfig{Enabled: false, Strict: false},
				Privacy:   PrivacyConfig{Enabled: false},
				Integrations: IntegrationsConfig{
					DCG: DCGConfig{AllowOverride: false},
				},
			}

			applySafetyProfileDefaults(cfg)

			if cfg.Safety.Profile != tt.wantProfile {
				t.Fatalf("Safety.Profile=%q, want %q", cfg.Safety.Profile, tt.wantProfile)
			}
			if cfg.Preflight.Enabled != tt.preflightEnabled || cfg.Preflight.Strict != tt.preflightStrict {
				t.Fatalf("Preflight={enabled:%v strict:%v}, want {enabled:%v strict:%v}",
					cfg.Preflight.Enabled, cfg.Preflight.Strict, tt.preflightEnabled, tt.preflightStrict)
			}
			if cfg.Redaction.Mode != tt.redactionMode {
				t.Fatalf("Redaction.Mode=%q, want %q", cfg.Redaction.Mode, tt.redactionMode)
			}
			if cfg.Privacy.Enabled != tt.privacyEnabled {
				t.Fatalf("Privacy.Enabled=%v, want %v", cfg.Privacy.Enabled, tt.privacyEnabled)
			}
			if cfg.Integrations.DCG.AllowOverride != tt.dcgAllowOverride {
				t.Fatalf("Integrations.DCG.AllowOverride=%v, want %v", cfg.Integrations.DCG.AllowOverride, tt.dcgAllowOverride)
			}
		})
	}
}

func TestLoadSafetyProfile_CanonicalizesProfileString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[safety]
profile = "SAFE"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Safety.Profile != SafetyProfileSafe {
		t.Fatalf("Safety.Profile=%q, want %q", cfg.Safety.Profile, SafetyProfileSafe)
	}
}

func TestLoadSafetyProfile_ProfileDefaults_AllProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	tests := []struct {
		name                string
		profile             string
		wantRedaction       string
		wantPrivacy         bool
		wantPreflightOn     bool
		wantPreflightStrict bool
		wantAllowOverride   bool
	}{
		{
			name:                "standard defaults",
			profile:             "standard",
			wantRedaction:       "warn",
			wantPrivacy:         false,
			wantPreflightOn:     true,
			wantPreflightStrict: false,
			wantAllowOverride:   true,
		},
		{
			name:                "safe defaults",
			profile:             "safe",
			wantRedaction:       "redact",
			wantPrivacy:         false,
			wantPreflightOn:     true,
			wantPreflightStrict: false,
			wantAllowOverride:   false,
		},
		{
			name:                "paranoid defaults",
			profile:             "paranoid",
			wantRedaction:       "block",
			wantPrivacy:         true,
			wantPreflightOn:     true,
			wantPreflightStrict: true,
			wantAllowOverride:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`
[safety]
profile = %q
`, tt.profile)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}

			if cfg.Safety.Profile != tt.profile {
				t.Fatalf("Safety.Profile=%q, want %q", cfg.Safety.Profile, tt.profile)
			}
			if cfg.Redaction.Mode != tt.wantRedaction {
				t.Fatalf("Redaction.Mode=%q, want %q", cfg.Redaction.Mode, tt.wantRedaction)
			}
			if cfg.Privacy.Enabled != tt.wantPrivacy {
				t.Fatalf("Privacy.Enabled=%v, want %v", cfg.Privacy.Enabled, tt.wantPrivacy)
			}
			if cfg.Preflight.Enabled != tt.wantPreflightOn || cfg.Preflight.Strict != tt.wantPreflightStrict {
				t.Fatalf("Preflight={enabled:%v strict:%v}, want {enabled:%v strict:%v}",
					cfg.Preflight.Enabled, cfg.Preflight.Strict, tt.wantPreflightOn, tt.wantPreflightStrict)
			}
			if cfg.Integrations.DCG.AllowOverride != tt.wantAllowOverride {
				t.Fatalf("Integrations.DCG.AllowOverride=%v, want %v", cfg.Integrations.DCG.AllowOverride, tt.wantAllowOverride)
			}
		})
	}
}

func TestLoadSafetyProfile_ExplicitKnobOverridesWin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `
[safety]
profile = "paranoid"

[redaction]
mode = "warn"

[privacy]
enabled = false

[preflight]
strict = false

[integrations.dcg]
allow_override = true
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Safety.Profile != SafetyProfileParanoid {
		t.Fatalf("Safety.Profile=%q, want %q", cfg.Safety.Profile, SafetyProfileParanoid)
	}
	if cfg.Redaction.Mode != "warn" {
		t.Fatalf("Redaction.Mode=%q, want %q", cfg.Redaction.Mode, "warn")
	}
	if cfg.Privacy.Enabled {
		t.Fatalf("Privacy.Enabled=%v, want false", cfg.Privacy.Enabled)
	}
	if cfg.Preflight.Strict {
		t.Fatalf("Preflight.Strict=%v, want false", cfg.Preflight.Strict)
	}
	if !cfg.Integrations.DCG.AllowOverride {
		t.Fatalf("Integrations.DCG.AllowOverride=%v, want true", cfg.Integrations.DCG.AllowOverride)
	}
}
