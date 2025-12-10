package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.ProjectsBase == "" {
		t.Error("ProjectsBase should not be empty")
	}

	if cfg.Agents.Claude == "" {
		t.Error("Claude agent command should not be empty")
	}

	if cfg.Agents.Codex == "" {
		t.Error("Codex agent command should not be empty")
	}

	if cfg.Agents.Gemini == "" {
		t.Error("Gemini agent command should not be empty")
	}

	if len(cfg.Palette) == 0 {
		t.Error("Default palette should have commands")
	}
}

func TestGetProjectDir(t *testing.T) {
	cfg := &Config{
		ProjectsBase: "/test/projects",
	}

	dir := cfg.GetProjectDir("myproject")
	expected := "/test/projects/myproject"

	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
	}
}

func TestGetProjectDirWithTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg := &Config{
		ProjectsBase: "~/projects",
	}

	dir := cfg.GetProjectDir("myproject")
	expected := filepath.Join(home, "projects", "myproject")

	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
	}
}

func TestLoadNonExistent(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("Expected error for non-existent config")
	}
}

func TestDefaultPaletteCategories(t *testing.T) {
	cmds := defaultPaletteCommands()

	categories := make(map[string]bool)
	for _, cmd := range cmds {
		if cmd.Category != "" {
			categories[cmd.Category] = true
		}
	}

	expectedCategories := []string{"Quick Actions", "Code Quality", "Coordination", "Investigation"}
	for _, cat := range expectedCategories {
		if !categories[cat] {
			t.Errorf("Expected category %s in default palette", cat)
		}
	}
}

// createTempConfig creates a temporary TOML config file for testing
func createTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "ntm-config-*.toml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		t.Fatalf("Failed to write temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestLoadFromFile(t *testing.T) {
	content := `
projects_base = "/custom/projects"

[agents]
claude = "custom-claude-cmd"
codex = "custom-codex-cmd"
gemini = "custom-gemini-cmd"

[tmux]
default_panes = 5
palette_key = "F5"

[agent_mail]
enabled = true
url = "http://localhost:9999/mcp/"
auto_register = false
program_name = "test-ntm"
`
	path := createTempConfig(t, content)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.ProjectsBase != "/custom/projects" {
		t.Errorf("Expected projects_base /custom/projects, got %s", cfg.ProjectsBase)
	}
	if cfg.Agents.Claude != "custom-claude-cmd" {
		t.Errorf("Expected claude 'custom-claude-cmd', got %s", cfg.Agents.Claude)
	}
	if cfg.Agents.Codex != "custom-codex-cmd" {
		t.Errorf("Expected codex 'custom-codex-cmd', got %s", cfg.Agents.Codex)
	}
	if cfg.Agents.Gemini != "custom-gemini-cmd" {
		t.Errorf("Expected gemini 'custom-gemini-cmd', got %s", cfg.Agents.Gemini)
	}
	if cfg.Tmux.DefaultPanes != 5 {
		t.Errorf("Expected default_panes 5, got %d", cfg.Tmux.DefaultPanes)
	}
	if cfg.Tmux.PaletteKey != "F5" {
		t.Errorf("Expected palette_key F5, got %s", cfg.Tmux.PaletteKey)
	}
	if cfg.AgentMail.URL != "http://localhost:9999/mcp/" {
		t.Errorf("Expected URL http://localhost:9999/mcp/, got %s", cfg.AgentMail.URL)
	}
	if cfg.AgentMail.AutoRegister != false {
		t.Error("Expected auto_register false")
	}
}

func TestLoadFromFileInvalid(t *testing.T) {
	content := `this is not valid TOML {{{`
	path := createTempConfig(t, content)
	_, err := Load(path)
	if err == nil {
		t.Error("Expected error for invalid TOML")
	}
}

func TestLoadFromFileMissing(t *testing.T) {
	_, err := Load("/definitely/does/not/exist/config.toml")
	if err == nil {
		t.Error("Expected error for missing config file")
	}
}

func TestDefaultAgentCommands(t *testing.T) {
	cfg := Default()
	if !strings.Contains(cfg.Agents.Claude, "claude") {
		t.Errorf("Claude command should contain 'claude': %s", cfg.Agents.Claude)
	}
	if !strings.Contains(cfg.Agents.Codex, "codex") {
		t.Errorf("Codex command should contain 'codex': %s", cfg.Agents.Codex)
	}
	if !strings.Contains(cfg.Agents.Gemini, "gemini") {
		t.Errorf("Gemini command should contain 'gemini': %s", cfg.Agents.Gemini)
	}
}

func TestCustomAgentCommands(t *testing.T) {
	content := `
[agents]
claude = "my-custom-claude --flag"
codex = "my-custom-codex --other-flag"
gemini = "my-custom-gemini"
`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Agents.Claude != "my-custom-claude --flag" {
		t.Errorf("Expected custom claude, got %s", cfg.Agents.Claude)
	}
	if cfg.Agents.Codex != "my-custom-codex --other-flag" {
		t.Errorf("Expected custom codex, got %s", cfg.Agents.Codex)
	}
	if cfg.Agents.Gemini != "my-custom-gemini" {
		t.Errorf("Expected custom gemini, got %s", cfg.Agents.Gemini)
	}
}

func TestDefaultTmuxSettings(t *testing.T) {
	cfg := Default()
	if cfg.Tmux.DefaultPanes != 10 {
		t.Errorf("Expected default_panes 10, got %d", cfg.Tmux.DefaultPanes)
	}
	if cfg.Tmux.PaletteKey != "F6" {
		t.Errorf("Expected palette_key F6, got %s", cfg.Tmux.PaletteKey)
	}
}

func TestCustomTmuxSettings(t *testing.T) {
	content := `
[tmux]
default_panes = 20
palette_key = "F12"
`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Tmux.DefaultPanes != 20 {
		t.Errorf("Expected default_panes 20, got %d", cfg.Tmux.DefaultPanes)
	}
	if cfg.Tmux.PaletteKey != "F12" {
		t.Errorf("Expected palette_key F12, got %s", cfg.Tmux.PaletteKey)
	}
}

func TestLoadDefaultsForMissingFields(t *testing.T) {
	content := `projects_base = "/my/projects"`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.ProjectsBase != "/my/projects" {
		t.Errorf("Expected projects_base /my/projects, got %s", cfg.ProjectsBase)
	}
	if cfg.Agents.Claude == "" {
		t.Error("Missing claude should have default")
	}
	if cfg.Tmux.DefaultPanes != 10 {
		t.Errorf("Missing default_panes should be 10, got %d", cfg.Tmux.DefaultPanes)
	}
	if cfg.Tmux.PaletteKey != "F6" {
		t.Errorf("Missing palette_key should be F6, got %s", cfg.Tmux.PaletteKey)
	}
}

func TestDefaultPath(t *testing.T) {
	path := DefaultPath()
	if !strings.Contains(path, "config.toml") {
		t.Errorf("DefaultPath should contain config.toml: %s", path)
	}
	if !strings.Contains(path, "ntm") {
		t.Errorf("DefaultPath should contain ntm: %s", path)
	}
}

func TestDefaultPathWithXDG(t *testing.T) {
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)
	os.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	path := DefaultPath()
	if path != "/custom/xdg/ntm/config.toml" {
		t.Errorf("Expected /custom/xdg/ntm/config.toml, got %s", path)
	}
}

func TestDefaultProjectsBase(t *testing.T) {
	base := DefaultProjectsBase()
	if base == "" {
		t.Error("DefaultProjectsBase should not be empty")
	}
}

func TestAgentMailDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.AgentMail.Enabled {
		t.Error("AgentMail should be enabled by default")
	}
	if cfg.AgentMail.URL != DefaultAgentMailURL {
		t.Errorf("Expected URL %s, got %s", DefaultAgentMailURL, cfg.AgentMail.URL)
	}
	if !cfg.AgentMail.AutoRegister {
		t.Error("AutoRegister should be true by default")
	}
	if cfg.AgentMail.ProgramName != "ntm" {
		t.Errorf("Expected program_name 'ntm', got %s", cfg.AgentMail.ProgramName)
	}
}

func TestAgentMailEnvOverrides(t *testing.T) {
	origURL := os.Getenv("AGENT_MAIL_URL")
	origToken := os.Getenv("AGENT_MAIL_TOKEN")
	origEnabled := os.Getenv("AGENT_MAIL_ENABLED")
	defer func() {
		os.Setenv("AGENT_MAIL_URL", origURL)
		os.Setenv("AGENT_MAIL_TOKEN", origToken)
		os.Setenv("AGENT_MAIL_ENABLED", origEnabled)
	}()

	os.Setenv("AGENT_MAIL_URL", "http://custom:8080/mcp/")
	os.Setenv("AGENT_MAIL_TOKEN", "secret-token")
	os.Setenv("AGENT_MAIL_ENABLED", "false")

	content := `
[agent_mail]
enabled = true
url = "http://original:1234/mcp/"
`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.AgentMail.URL != "http://custom:8080/mcp/" {
		t.Errorf("Expected URL from env, got %s", cfg.AgentMail.URL)
	}
	if cfg.AgentMail.Token != "secret-token" {
		t.Errorf("Expected token from env, got %s", cfg.AgentMail.Token)
	}
	if cfg.AgentMail.Enabled != false {
		t.Error("Expected enabled=false from env")
	}
}

func TestModelsConfig(t *testing.T) {
	cfg := Default()
	if cfg.Models.DefaultClaude == "" {
		t.Error("DefaultClaude should not be empty")
	}
	if len(cfg.Models.Claude) == 0 {
		t.Error("Claude aliases should not be empty")
	}
}

func TestGetModelName(t *testing.T) {
	models := DefaultModels()
	tests := []struct {
		agentType, alias, expected string
	}{
		{"claude", "", models.DefaultClaude},
		{"cc", "", models.DefaultClaude},
		{"codex", "", models.DefaultCodex},
		{"gemini", "", models.DefaultGemini},
		{"claude", "opus", "claude-opus-4-20250514"},
		{"codex", "gpt4", "gpt-4"},
		{"gemini", "flash", "gemini-2.0-flash"},
		{"claude", "custom-model", "custom-model"},
		{"unknown", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.agentType+"/"+tt.alias, func(t *testing.T) {
			result := models.GetModelName(tt.agentType, tt.alias)
			if result != tt.expected {
				t.Errorf("GetModelName(%s, %s) = %s, want %s", tt.agentType, tt.alias, result, tt.expected)
			}
		})
	}
}

func TestLoadPaletteFromMarkdown(t *testing.T) {
	content := `# Comment
## Quick Actions
### fix | Fix the Bug
Fix the bug.

### test | Run Tests
Run tests.

## Code Quality
### refactor | Refactor
Clean up.
`
	f, err := os.CreateTemp("", "palette-*.md")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	f.WriteString(content)
	f.Close()
	defer os.Remove(f.Name())

	cmds, err := LoadPaletteFromMarkdown(f.Name())
	if err != nil {
		t.Fatalf("Failed to load palette: %v", err)
	}
	if len(cmds) != 3 {
		t.Errorf("Expected 3 commands, got %d", len(cmds))
	}
	if cmds[0].Key != "fix" {
		t.Errorf("Expected key 'fix', got %s", cmds[0].Key)
	}
	if cmds[0].Category != "Quick Actions" {
		t.Errorf("Expected category 'Quick Actions', got %s", cmds[0].Category)
	}
}

func TestLoadPaletteFromMarkdownInvalidFormat(t *testing.T) {
	content := `## Category
### invalid-no-pipe
No pipe separator
`
	f, err := os.CreateTemp("", "palette-invalid-*.md")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	f.WriteString(content)
	f.Close()
	defer os.Remove(f.Name())

	cmds, _ := LoadPaletteFromMarkdown(f.Name())
	if len(cmds) != 0 {
		t.Errorf("Expected 0 commands (invalid skipped), got %d", len(cmds))
	}
}

func TestPrint(t *testing.T) {
	cfg := Default()
	var buf bytes.Buffer
	err := Print(cfg, &buf)
	if err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	output := buf.String()
	for _, section := range []string{"[agents]", "[tmux]", "[agent_mail]", "[models]", "[[palette]]"} {
		if !strings.Contains(output, section) {
			t.Errorf("Expected output to contain %s", section)
		}
	}
}

func TestCASSDefaults(t *testing.T) {
	cfg := Default()

	if !cfg.CASS.Enabled {
		t.Error("CASS should be enabled by default")
	}
	if !cfg.CASS.ShowInstallHints {
		t.Error("CASS ShowInstallHints should be true by default")
	}
	if cfg.CASS.Timeout != 30 {
		t.Errorf("Expected CASS timeout 30, got %d", cfg.CASS.Timeout)
	}

	// Context defaults
	if !cfg.CASS.Context.Enabled {
		t.Error("CASS Context should be enabled by default")
	}
	if cfg.CASS.Context.MaxSessions != 3 {
		t.Errorf("Expected MaxSessions 3, got %d", cfg.CASS.Context.MaxSessions)
	}
	if cfg.CASS.Context.LookbackDays != 30 {
		t.Errorf("Expected LookbackDays 30, got %d", cfg.CASS.Context.LookbackDays)
	}

	// Duplicates defaults
	if !cfg.CASS.Duplicates.Enabled {
		t.Error("CASS Duplicates should be enabled by default")
	}
	if cfg.CASS.Duplicates.SimilarityThreshold != 0.7 {
		t.Errorf("Expected SimilarityThreshold 0.7, got %f", cfg.CASS.Duplicates.SimilarityThreshold)
	}

	// Search defaults
	if cfg.CASS.Search.DefaultLimit != 10 {
		t.Errorf("Expected DefaultLimit 10, got %d", cfg.CASS.Search.DefaultLimit)
	}
	if cfg.CASS.Search.DefaultFields != "summary" {
		t.Errorf("Expected DefaultFields 'summary', got %s", cfg.CASS.Search.DefaultFields)
	}

	// TUI defaults
	if !cfg.CASS.TUI.ShowActivitySparkline {
		t.Error("CASS TUI ShowActivitySparkline should be true by default")
	}
	if !cfg.CASS.TUI.ShowStatusIndicator {
		t.Error("CASS TUI ShowStatusIndicator should be true by default")
	}
}

func TestCASSEnvOverrides(t *testing.T) {
	// Save original values
	origEnabled := os.Getenv("NTM_CASS_ENABLED")
	origTimeout := os.Getenv("NTM_CASS_TIMEOUT")
	origBinary := os.Getenv("NTM_CASS_BINARY")

	// Create a minimal config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[cass]
enabled = true
timeout = 30
`), 0644); err != nil {
		t.Fatalf("Failed to create config file: %v", err)
	}

	// Test NTM_CASS_ENABLED=false
	os.Setenv("NTM_CASS_ENABLED", "false")
	defer os.Setenv("NTM_CASS_ENABLED", origEnabled)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Enabled {
		t.Error("CASS should be disabled via NTM_CASS_ENABLED=false")
	}

	// Test NTM_CASS_TIMEOUT
	os.Setenv("NTM_CASS_ENABLED", "true")
	os.Setenv("NTM_CASS_TIMEOUT", "60")
	defer os.Setenv("NTM_CASS_TIMEOUT", origTimeout)

	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Timeout != 60 {
		t.Errorf("Expected CASS timeout 60 from env, got %d", cfg.CASS.Timeout)
	}

	// Test NTM_CASS_BINARY
	os.Setenv("NTM_CASS_BINARY", "/custom/path/to/cass")
	defer os.Setenv("NTM_CASS_BINARY", origBinary)

	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.BinaryPath != "/custom/path/to/cass" {
		t.Errorf("Expected CASS binary path from env, got %s", cfg.CASS.BinaryPath)
	}
}

func TestCreateDefaultAlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	configDir := filepath.Join(tmpDir, "ntm")
	os.MkdirAll(configDir, 0755)
	configPath := filepath.Join(configDir, "config.toml")
	os.WriteFile(configPath, []byte("# existing"), 0644)

	_, err := CreateDefault()
	if err == nil {
		t.Error("Expected error when config already exists")
	}
}

func TestCreateDefaultSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	path, err := CreateDefault()
	if err != nil {
		t.Fatalf("CreateDefault failed: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("Config file not created at %s", path)
	}
	_, err = Load(path)
	if err != nil {
		t.Errorf("Created config is not valid: %v", err)
	}
}

func TestFindPaletteMarkdownCwd(t *testing.T) {
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	palettePath := filepath.Join(tmpDir, "command_palette.md")
	os.WriteFile(palettePath, []byte("## Test\n### key | Label\nPrompt"), 0644)
	os.Chdir(tmpDir)

	found := findPaletteMarkdown()
	if found == "" {
		t.Error("Expected to find command_palette.md in cwd")
	}
}

func TestLoadWithExplicitPaletteFile(t *testing.T) {
	paletteContent := `## Custom
### custom_key | Custom Command
Custom prompt.
`
	paletteFile, _ := os.CreateTemp("", "custom-palette-*.md")
	paletteFile.WriteString(paletteContent)
	paletteFile.Close()
	defer os.Remove(paletteFile.Name())

	configContent := fmt.Sprintf(`palette_file = %q`, paletteFile.Name())
	configPath := createTempConfig(t, configContent)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if len(cfg.Palette) != 1 || cfg.Palette[0].Key != "custom_key" {
		t.Errorf("Expected palette from explicit file, got %d commands", len(cfg.Palette))
	}
}

func TestLoadWithTildePaletteFile(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get user home dir")
	}

	palettePath := filepath.Join(home, ".ntm-test-palette.md")
	os.WriteFile(palettePath, []byte("## Test\n### tilde_test | Tilde Test\nPrompt."), 0644)
	defer os.Remove(palettePath)

	configContent := `palette_file = "~/.ntm-test-palette.md"`
	configPath := createTempConfig(t, configContent)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if len(cfg.Palette) != 1 || cfg.Palette[0].Key != "tilde_test" {
		t.Errorf("Expected palette from tilde path, got %d commands", len(cfg.Palette))
	}
}

func TestLoadPaletteFromTOML(t *testing.T) {
	configContent := `
[[palette]]
key = "toml_cmd"
label = "TOML Command"
category = "TOML Category"
prompt = "TOML prompt"
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if len(cfg.Palette) != 1 || cfg.Palette[0].Key != "toml_cmd" {
		t.Errorf("Expected palette from TOML, got %d commands", len(cfg.Palette))
	}
}
