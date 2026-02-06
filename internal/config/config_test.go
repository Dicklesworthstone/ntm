package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get user home dir")
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"/abs/path", "/abs/path"},
		{"rel/path", "rel/path"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ExpandHome(tt.input)
			if got != tt.expected {
				t.Errorf("ExpandHome(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetProjectDirWithJustTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg := &Config{
		ProjectsBase: "~",
	}

	dir := cfg.GetProjectDir("myproject")
	expected := filepath.Join(home, "myproject")

	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
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
	// When the config file doesn't exist, Load should return defaults (not an error).
	// This is the correct behavior - missing config files are silently ignored.
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Errorf("Expected no error for non-existent config (should return defaults): %v", err)
	}
	if cfg == nil {
		t.Error("Expected non-nil config with defaults")
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
pane_init_delay_ms = 1500

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
	if cfg.Tmux.PaneInitDelayMs != 1500 {
		t.Errorf("Expected pane_init_delay_ms 1500, got %d", cfg.Tmux.PaneInitDelayMs)
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
	// When the config file doesn't exist, Load should return defaults (not an error).
	cfg, err := Load("/definitely/does/not/exist/config.toml")
	if err != nil {
		t.Errorf("Expected no error for missing config file (should return defaults): %v", err)
	}
	if cfg == nil {
		t.Error("Expected non-nil config with defaults")
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
	if cfg.Tmux.PaneInitDelayMs != 1000 {
		t.Errorf("Expected pane_init_delay_ms 1000, got %d", cfg.Tmux.PaneInitDelayMs)
	}
}

func TestCustomTmuxSettings(t *testing.T) {
	content := `
[tmux]
default_panes = 20
palette_key = "F12"
pane_init_delay_ms = 2500
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
	if cfg.Tmux.PaneInitDelayMs != 2500 {
		t.Errorf("Expected pane_init_delay_ms 2500, got %d", cfg.Tmux.PaneInitDelayMs)
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
	if cfg.Tmux.PaneInitDelayMs != 1000 {
		t.Errorf("Missing pane_init_delay_ms should be 1000, got %d", cfg.Tmux.PaneInitDelayMs)
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
		{"claude", "opus", "claude-opus-4-5-20251101"},
		{"codex", "gpt4", "gpt-4"},
		{"gemini", "flash", "gemini-3-flash"},
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
	for _, section := range []string{"[agents]", "[tmux]", "[agent_mail]", "[integrations.dcg]", "[models]", "[ensemble]", "[[palette]]"} {
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

func TestCASSEnabledFalseRespected(t *testing.T) {
	// This tests that when a user sets enabled = false but nothing else,
	// we don't override their enabled = false with the default true.

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[cass]
enabled = false
`), 0644); err != nil {
		t.Fatalf("Failed to create config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// User's enabled = false should be respected
	if cfg.CASS.Enabled {
		t.Error("User's enabled = false was overwritten with default true")
	}

	// But other defaults should still be applied
	if cfg.CASS.Timeout != 30 {
		t.Errorf("Expected default timeout 30, got %d", cfg.CASS.Timeout)
	}
	if cfg.CASS.Context.MaxSessions != 3 {
		t.Errorf("Expected default MaxSessions 3, got %d", cfg.CASS.Context.MaxSessions)
	}
}

func TestCASSEnvOverrides(t *testing.T) {
	// Save original values
	origEnabled := os.Getenv("NTM_CASS_ENABLED")
	origTimeout := os.Getenv("NTM_CASS_TIMEOUT")
	origBinary := os.Getenv("NTM_CASS_BINARY")

	// Clear env vars before test
	os.Unsetenv("NTM_CASS_ENABLED")
	os.Unsetenv("NTM_CASS_TIMEOUT")
	os.Unsetenv("NTM_CASS_BINARY")

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

	// Test that negative timeout values are rejected
	os.Setenv("NTM_CASS_TIMEOUT", "-5")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Timeout != 30 {
		t.Errorf("Negative timeout should be rejected; expected 30 (from config), got %d", cfg.CASS.Timeout)
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
	// Switch to temp dir to avoid picking up project's command_palette.md
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	// Also override XDG_CONFIG_HOME to avoid picking up user's palette
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

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

func TestAccountsDefaults(t *testing.T) {
	cfg := Default()

	if cfg.Accounts.StateFile == "" {
		t.Error("Accounts.StateFile should have a default")
	}
	if cfg.Accounts.ResetBufferMinutes == 0 {
		t.Error("Accounts.ResetBufferMinutes should have a default")
	}
	if !cfg.Accounts.AutoRotate {
		t.Error("Accounts.AutoRotate should default to true")
	}
}

func TestRotationDefaults(t *testing.T) {
	cfg := Default()

	// Rotation should be disabled by default (opt-in)
	if cfg.Rotation.Enabled {
		t.Error("Rotation.Enabled should default to false")
	}
	if !cfg.Rotation.PreferRestart {
		t.Error("Rotation.PreferRestart should default to true")
	}
	if cfg.Rotation.ContinuationPrompt == "" {
		t.Error("Rotation.ContinuationPrompt should have a default")
	}
	if cfg.Rotation.Thresholds.WarningPercent == 0 {
		t.Error("Rotation.Thresholds.WarningPercent should have a default")
	}
	if cfg.Rotation.Thresholds.CriticalPercent == 0 {
		t.Error("Rotation.Thresholds.CriticalPercent should have a default")
	}
	if !cfg.Rotation.Dashboard.ShowQuotaBars {
		t.Error("Rotation.Dashboard.ShowQuotaBars should default to true")
	}
}

func TestAccountsFromTOML(t *testing.T) {
	configContent := `
[accounts]
state_file = "/custom/state.json"
auto_rotate = false
reset_buffer_minutes = 30

[[accounts.claude]]
email = "test@example.com"
alias = "main"
priority = 1

[[accounts.claude]]
email = "backup@example.com"
alias = "backup"
priority = 2
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Accounts.StateFile != "/custom/state.json" {
		t.Errorf("Expected custom state file, got %s", cfg.Accounts.StateFile)
	}
	if cfg.Accounts.AutoRotate {
		t.Error("Expected auto_rotate = false")
	}
	if cfg.Accounts.ResetBufferMinutes != 30 {
		t.Errorf("Expected reset_buffer_minutes = 30, got %d", cfg.Accounts.ResetBufferMinutes)
	}
	if len(cfg.Accounts.Claude) != 2 {
		t.Fatalf("Expected 2 Claude accounts, got %d", len(cfg.Accounts.Claude))
	}
	if cfg.Accounts.Claude[0].Email != "test@example.com" {
		t.Errorf("Expected first account email test@example.com, got %s", cfg.Accounts.Claude[0].Email)
	}
	if cfg.Accounts.Claude[1].Alias != "backup" {
		t.Errorf("Expected second account alias backup, got %s", cfg.Accounts.Claude[1].Alias)
	}
}

func TestRotationFromTOML(t *testing.T) {
	configContent := `
[rotation]
enabled = true
prefer_restart = false
auto_open_browser = true
continuation_prompt = "Custom prompt: {{.Context}}"

[rotation.thresholds]
warning_percent = 70
critical_percent = 90
restart_if_tokens_above = 50000
restart_if_session_hours = 4

[rotation.dashboard]
show_quota_bars = false
show_account_status = true
show_reset_timers = false
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if !cfg.Rotation.Enabled {
		t.Error("Expected rotation.enabled = true")
	}
	if cfg.Rotation.PreferRestart {
		t.Error("Expected rotation.prefer_restart = false")
	}
	if !cfg.Rotation.AutoOpenBrowser {
		t.Error("Expected rotation.auto_open_browser = true")
	}
	if cfg.Rotation.ContinuationPrompt != "Custom prompt: {{.Context}}" {
		t.Errorf("Wrong continuation_prompt: %s", cfg.Rotation.ContinuationPrompt)
	}
	if cfg.Rotation.Thresholds.WarningPercent != 70 {
		t.Errorf("Expected warning_percent = 70, got %d", cfg.Rotation.Thresholds.WarningPercent)
	}
	if cfg.Rotation.Thresholds.CriticalPercent != 90 {
		t.Errorf("Expected critical_percent = 90, got %d", cfg.Rotation.Thresholds.CriticalPercent)
	}
	if cfg.Rotation.Dashboard.ShowQuotaBars {
		t.Error("Expected show_quota_bars = false")
	}
	if !cfg.Rotation.Dashboard.ShowAccountStatus {
		t.Error("Expected show_account_status = true")
	}
}

func TestAccountsEnvOverrides(t *testing.T) {
	configContent := `
[accounts]
auto_rotate = true
`
	configPath := createTempConfig(t, configContent)

	// Set env override to disable auto_rotate
	os.Setenv("NTM_ACCOUNTS_AUTO_ROTATE", "false")
	defer os.Unsetenv("NTM_ACCOUNTS_AUTO_ROTATE")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Accounts.AutoRotate {
		t.Error("Expected auto_rotate to be overridden to false by env var")
	}
}

func TestRotationEnvOverrides(t *testing.T) {
	configContent := `
[rotation]
enabled = false
`
	configPath := createTempConfig(t, configContent)

	// Set env override to enable rotation
	os.Setenv("NTM_ROTATION_ENABLED", "true")
	defer os.Unsetenv("NTM_ROTATION_ENABLED")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if !cfg.Rotation.Enabled {
		t.Error("Expected rotation.enabled to be overridden to true by env var")
	}
}

func TestWatchProjectConfig(t *testing.T) {
	// Setup dirs
	tmpDir := t.TempDir()
	cwd := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(origWd)

	// Set NTM_CONFIG to point to our temp global config
	globalPath := filepath.Join(tmpDir, "config.toml")
	os.Setenv("NTM_CONFIG", globalPath)
	defer os.Unsetenv("NTM_CONFIG")

	// Global config
	os.WriteFile(globalPath, []byte(`
[agents]
claude = "global-claude"
`), 0644)

	// Project config - NOTE: agent commands in project config are ignored
	// for security (RCE prevention). Test with defaults.agents instead.
	os.Mkdir(".ntm", 0755)
	projPath := filepath.Join(cwd, ".ntm", "config.toml")
	os.WriteFile(projPath, []byte(`
[defaults]
agents = { cc = 2 }
`), 0644)

	// Setup watcher
	updated := make(chan *Config, 1)
	closeWatcher, err := Watch(func(cfg *Config) {
		select {
		case updated <- cfg:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	defer closeWatcher()

	// Modify project config - change the defaults.agents which IS merged
	time.Sleep(600 * time.Millisecond) // Wait for debounce/start
	os.WriteFile(projPath, []byte(`
[defaults]
agents = { cc = 5 }
`), 0644)

	// Wait for update
	select {
	case cfg := <-updated:
		// Agent commands should still be from global config (security feature)
		if cfg.Agents.Claude != "global-claude" {
			t.Errorf("Expected 'global-claude' (agent override disabled), got %q", cfg.Agents.Claude)
		}
		// Project defaults SHOULD be updated
		if cfg.ProjectDefaults["cc"] != 5 {
			t.Errorf("Expected project defaults cc=5, got %d", cfg.ProjectDefaults["cc"])
		}
	case <-time.After(3 * time.Second):
		t.Error("Timed out waiting for config update")
	}
}

func TestContextRotationDefaults(t *testing.T) {
	cfg := Default()

	// Defaults should be sensible
	if !cfg.ContextRotation.Enabled {
		t.Error("ContextRotation should be enabled by default")
	}
	if cfg.ContextRotation.WarningThreshold != 0.80 {
		t.Errorf("Expected warning_threshold 0.80, got %f", cfg.ContextRotation.WarningThreshold)
	}
	if cfg.ContextRotation.RotateThreshold != 0.95 {
		t.Errorf("Expected rotate_threshold 0.95, got %f", cfg.ContextRotation.RotateThreshold)
	}
	if cfg.ContextRotation.SummaryMaxTokens != 2000 {
		t.Errorf("Expected summary_max_tokens 2000, got %d", cfg.ContextRotation.SummaryMaxTokens)
	}
	if cfg.ContextRotation.MinSessionAgeSec != 300 {
		t.Errorf("Expected min_session_age_sec 300, got %d", cfg.ContextRotation.MinSessionAgeSec)
	}
	if !cfg.ContextRotation.TryCompactFirst {
		t.Error("TryCompactFirst should be true by default")
	}
	if cfg.ContextRotation.RequireConfirm {
		t.Error("RequireConfirm should be false by default")
	}
}

func TestContextRotationFromTOML(t *testing.T) {
	configContent := `
[context_rotation]
enabled = false
warning_threshold = 0.70
rotate_threshold = 0.90
summary_max_tokens = 3000
min_session_age_sec = 600
try_compact_first = false
require_confirm = true
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.ContextRotation.Enabled {
		t.Error("Expected enabled = false")
	}
	if cfg.ContextRotation.WarningThreshold != 0.70 {
		t.Errorf("Expected warning_threshold 0.70, got %f", cfg.ContextRotation.WarningThreshold)
	}
	if cfg.ContextRotation.RotateThreshold != 0.90 {
		t.Errorf("Expected rotate_threshold 0.90, got %f", cfg.ContextRotation.RotateThreshold)
	}
	if cfg.ContextRotation.SummaryMaxTokens != 3000 {
		t.Errorf("Expected summary_max_tokens 3000, got %d", cfg.ContextRotation.SummaryMaxTokens)
	}
	if cfg.ContextRotation.MinSessionAgeSec != 600 {
		t.Errorf("Expected min_session_age_sec 600, got %d", cfg.ContextRotation.MinSessionAgeSec)
	}
	if cfg.ContextRotation.TryCompactFirst {
		t.Error("Expected try_compact_first = false")
	}
	if !cfg.ContextRotation.RequireConfirm {
		t.Error("Expected require_confirm = true")
	}
}

func TestValidateContextRotationConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ContextRotationConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: ContextRotationConfig{
				Enabled:          true,
				WarningThreshold: 0.80,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 2000,
				MinSessionAgeSec: 300,
			},
			wantErr: false,
		},
		{
			name: "warning_threshold too low",
			cfg: ContextRotationConfig{
				WarningThreshold: -0.1,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "warning_threshold too high",
			cfg: ContextRotationConfig{
				WarningThreshold: 1.5,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "rotate_threshold too low",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  -0.1,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "rotate_threshold too high",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  1.5,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "warning >= rotate threshold",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.95,
				RotateThreshold:  0.80,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "warning == rotate threshold",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  0.80,
				SummaryMaxTokens: 2000,
			},
			wantErr: true,
		},
		{
			name: "summary_max_tokens too low",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 100,
			},
			wantErr: true,
		},
		{
			name: "summary_max_tokens too high",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 20000,
			},
			wantErr: true,
		},
		{
			name: "min_session_age negative",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 2000,
				MinSessionAgeSec: -1,
			},
			wantErr: true,
		},
		{
			name: "min_session_age zero is valid",
			cfg: ContextRotationConfig{
				WarningThreshold: 0.80,
				RotateThreshold:  0.95,
				SummaryMaxTokens: 2000,
				MinSessionAgeSec: 0,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateContextRotationConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateContextRotationConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestContextRotationPrintOutput(t *testing.T) {
	cfg := Default()
	var buf bytes.Buffer
	err := Print(cfg, &buf)
	if err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	output := buf.String()

	// Check for context_rotation section
	if !strings.Contains(output, "[context_rotation]") {
		t.Error("Expected output to contain [context_rotation]")
	}
	if !strings.Contains(output, "warning_threshold") {
		t.Error("Expected output to contain warning_threshold")
	}
	if !strings.Contains(output, "rotate_threshold") {
		t.Error("Expected output to contain rotate_threshold")
	}
}

func TestValidateHealthConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     HealthConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: HealthConfig{
				Enabled:            true,
				CheckInterval:      10,
				StallThreshold:     300,
				AutoRestart:        false,
				MaxRestarts:        3,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: false,
		},
		{
			name: "check_interval zero",
			cfg: HealthConfig{
				CheckInterval:      0,
				StallThreshold:     300,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: true,
		},
		{
			name: "check_interval negative",
			cfg: HealthConfig{
				CheckInterval:      -1,
				StallThreshold:     300,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: true,
		},
		{
			name: "stall_threshold less than check_interval",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     5,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: true,
		},
		{
			name: "stall_threshold equals check_interval",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     10,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: false,
		},
		{
			name: "max_restarts negative",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     300,
				MaxRestarts:        -1,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: true,
		},
		{
			name: "max_restarts zero is valid",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     300,
				MaxRestarts:        0,
				RestartBackoffBase: 30,
				RestartBackoffMax:  300,
			},
			wantErr: false,
		},
		{
			name: "restart_backoff_base zero",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     300,
				RestartBackoffBase: 0,
				RestartBackoffMax:  300,
			},
			wantErr: true,
		},
		{
			name: "restart_backoff_max less than base",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     300,
				RestartBackoffBase: 60,
				RestartBackoffMax:  30,
			},
			wantErr: true,
		},
		{
			name: "restart_backoff_max equals base",
			cfg: HealthConfig{
				CheckInterval:      10,
				StallThreshold:     300,
				RestartBackoffBase: 30,
				RestartBackoffMax:  30,
			},
			wantErr: false,
		},
		{
			name: "minimal valid config",
			cfg: HealthConfig{
				CheckInterval:      1,
				StallThreshold:     1,
				RestartBackoffBase: 1,
				RestartBackoffMax:  1,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHealthConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateHealthConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHealthConfigPrintOutput(t *testing.T) {
	cfg := Default()
	var buf bytes.Buffer
	err := Print(cfg, &buf)
	if err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	output := buf.String()

	// Check for health section
	if !strings.Contains(output, "[health]") {
		t.Error("Expected output to contain [health]")
	}
	if !strings.Contains(output, "check_interval") {
		t.Error("Expected output to contain check_interval")
	}
	if !strings.Contains(output, "stall_threshold") {
		t.Error("Expected output to contain stall_threshold")
	}
	if !strings.Contains(output, "restart_backoff_base") {
		t.Error("Expected output to contain restart_backoff_base")
	}
}

func TestHealthConfigGetValue(t *testing.T) {
	cfg := Default()

	tests := []struct {
		path string
		want interface{}
	}{
		{"health.enabled", true},
		{"health.check_interval", 10},
		{"health.stall_threshold", 300},
		{"health.auto_restart", false},
		{"health.max_restarts", 3},
		{"health.restart_backoff_base", 30},
		{"health.restart_backoff_max", 300},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Fatalf("GetValue(%q) error = %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("GetValue(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDCGConfigGetValue(t *testing.T) {
	cfg := Default()

	tests := []struct {
		path string
		want interface{}
	}{
		{"integrations.dcg.enabled", false},
		{"integrations.dcg.binary_path", ""},
		{"integrations.dcg.audit_log", ""},
		{"integrations.dcg.allow_override", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Fatalf("GetValue(%q) error = %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("GetValue(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCASSContextDefaults(t *testing.T) {
	cfg := Default()

	// New fields should have defaults
	if cfg.CASS.Context.MinRelevance != 0.5 {
		t.Errorf("Expected MinRelevance 0.5, got %f", cfg.CASS.Context.MinRelevance)
	}
	if cfg.CASS.Context.SkipIfContextAbove != 80 {
		t.Errorf("Expected SkipIfContextAbove 80, got %f", cfg.CASS.Context.SkipIfContextAbove)
	}
	if !cfg.CASS.Context.PreferSameProject {
		t.Error("PreferSameProject should be true by default")
	}
	if cfg.CASS.Context.MaxTokens != 2000 {
		t.Errorf("Expected MaxTokens 2000, got %d", cfg.CASS.Context.MaxTokens)
	}
}

func TestCASSContextFromTOML(t *testing.T) {
	configContent := `
[cass]
enabled = true

[cass.context]
enabled = false
max_sessions = 5
lookback_days = 14
max_tokens = 1000
min_relevance = 0.7
skip_if_context_above = 60
prefer_same_project = false
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.CASS.Context.Enabled {
		t.Error("Expected context.enabled = false")
	}
	if cfg.CASS.Context.MaxSessions != 5 {
		t.Errorf("Expected MaxSessions 5, got %d", cfg.CASS.Context.MaxSessions)
	}
	if cfg.CASS.Context.LookbackDays != 14 {
		t.Errorf("Expected LookbackDays 14, got %d", cfg.CASS.Context.LookbackDays)
	}
	if cfg.CASS.Context.MaxTokens != 1000 {
		t.Errorf("Expected MaxTokens 1000, got %d", cfg.CASS.Context.MaxTokens)
	}
	if cfg.CASS.Context.MinRelevance != 0.7 {
		t.Errorf("Expected MinRelevance 0.7, got %f", cfg.CASS.Context.MinRelevance)
	}
	if cfg.CASS.Context.SkipIfContextAbove != 60 {
		t.Errorf("Expected SkipIfContextAbove 60, got %f", cfg.CASS.Context.SkipIfContextAbove)
	}
	if cfg.CASS.Context.PreferSameProject {
		t.Error("Expected PreferSameProject = false")
	}
}

func TestCASSContextEnvOverrides(t *testing.T) {
	// Save original values
	origContextEnabled := os.Getenv("NTM_CASS_CONTEXT_ENABLED")
	origMinRel := os.Getenv("NTM_CASS_MIN_RELEVANCE")
	origSkipAbove := os.Getenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE")
	origPreferSame := os.Getenv("NTM_CASS_PREFER_SAME_PROJECT")
	defer func() {
		os.Setenv("NTM_CASS_CONTEXT_ENABLED", origContextEnabled)
		os.Setenv("NTM_CASS_MIN_RELEVANCE", origMinRel)
		os.Setenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE", origSkipAbove)
		os.Setenv("NTM_CASS_PREFER_SAME_PROJECT", origPreferSame)
	}()

	// Clear env vars
	os.Unsetenv("NTM_CASS_CONTEXT_ENABLED")
	os.Unsetenv("NTM_CASS_MIN_RELEVANCE")
	os.Unsetenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE")
	os.Unsetenv("NTM_CASS_PREFER_SAME_PROJECT")

	// Create config with defaults
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[cass.context]
enabled = true
min_relevance = 0.5
skip_if_context_above = 80
prefer_same_project = true
`), 0644); err != nil {
		t.Fatalf("Failed to create config file: %v", err)
	}

	// Test NTM_CASS_CONTEXT_ENABLED=false
	os.Setenv("NTM_CASS_CONTEXT_ENABLED", "false")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.Enabled {
		t.Error("Context should be disabled via NTM_CASS_CONTEXT_ENABLED=false")
	}

	// Test NTM_CASS_MIN_RELEVANCE
	os.Setenv("NTM_CASS_CONTEXT_ENABLED", "true")
	os.Setenv("NTM_CASS_MIN_RELEVANCE", "0.8")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.MinRelevance != 0.8 {
		t.Errorf("Expected MinRelevance 0.8 from env, got %f", cfg.CASS.Context.MinRelevance)
	}

	// Test NTM_CASS_SKIP_IF_CONTEXT_ABOVE
	os.Setenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE", "50")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.SkipIfContextAbove != 50 {
		t.Errorf("Expected SkipIfContextAbove 50 from env, got %f", cfg.CASS.Context.SkipIfContextAbove)
	}

	// Test NTM_CASS_PREFER_SAME_PROJECT
	os.Setenv("NTM_CASS_PREFER_SAME_PROJECT", "false")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.PreferSameProject {
		t.Error("Expected PreferSameProject false from env")
	}

	// Test invalid MinRelevance values are rejected (outside 0-1)
	os.Setenv("NTM_CASS_MIN_RELEVANCE", "1.5")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.MinRelevance != 0.5 { // Should keep config value, not env
		t.Errorf("Invalid MinRelevance should be rejected, got %f", cfg.CASS.Context.MinRelevance)
	}

	// Test invalid SkipIfContextAbove values are rejected (outside 0-100)
	os.Setenv("NTM_CASS_MIN_RELEVANCE", "0.5") // Reset to valid
	os.Setenv("NTM_CASS_SKIP_IF_CONTEXT_ABOVE", "150")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CASS.Context.SkipIfContextAbove != 80 { // Should keep config value, not env
		t.Errorf("Invalid SkipIfContextAbove should be rejected, got %f", cfg.CASS.Context.SkipIfContextAbove)
	}
}

func TestCASSContextValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errPath string
	}{
		{
			name: "valid context config",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MaxSessions:        3,
						LookbackDays:       30,
						MaxTokens:          2000,
						MinRelevance:       0.5,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: false,
		},
		{
			name: "min_relevance too low",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       -0.1,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.min_relevance",
		},
		{
			name: "min_relevance too high",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       1.5,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.min_relevance",
		},
		{
			name: "skip_if_context_above too low",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       0.5,
						SkipIfContextAbove: -10,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.skip_if_context_above",
		},
		{
			name: "skip_if_context_above too high",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       0.5,
						SkipIfContextAbove: 150,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.skip_if_context_above",
		},
		{
			name: "max_sessions negative",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MaxSessions:        -1,
						MinRelevance:       0.5,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.max_sessions",
		},
		{
			name: "max_tokens negative",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MaxTokens:          -100,
						MinRelevance:       0.5,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.max_tokens",
		},
		{
			name: "lookback_days negative",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						LookbackDays:       -7,
						MinRelevance:       0.5,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: true,
			errPath: "cass.context.lookback_days",
		},
		{
			name: "boundary values valid - min_relevance 0",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       0,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: false,
		},
		{
			name: "boundary values valid - min_relevance 1",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       1,
						SkipIfContextAbove: 80,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: false,
		},
		{
			name: "boundary values valid - skip_if_context_above 0",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       0.5,
						SkipIfContextAbove: 0,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: false,
		},
		{
			name: "boundary values valid - skip_if_context_above 100",
			cfg: Config{
				CASS: CASSConfig{
					Timeout: 30,
					Context: CASSContextConfig{
						MinRelevance:       0.5,
						SkipIfContextAbove: 100,
					},
				},
				Tmux:            TmuxConfig{DefaultPanes: 1},
				ContextRotation: DefaultContextRotationConfig(),
				Health:          DefaultHealthConfig(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(&tt.cfg)
			hasErr := len(errs) > 0
			if hasErr != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", errs, tt.wantErr)
			}
			if tt.wantErr && tt.errPath != "" {
				found := false
				for _, err := range errs {
					if strings.Contains(err.Error(), tt.errPath) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected error containing %q, got %v", tt.errPath, errs)
				}
			}
		})
	}
}

func TestDCGConfigValidation(t *testing.T) {
	cfg := Config{
		Integrations: IntegrationsConfig{
			DCG: DCGConfig{
				BinaryPath: "/no/such/dcg",
			},
		},
		Tmux:            TmuxConfig{DefaultPanes: 1},
		ContextRotation: DefaultContextRotationConfig(),
		Health:          DefaultHealthConfig(),
	}

	errs := Validate(&cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for invalid dcg binary_path")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "integrations.dcg") && strings.Contains(err.Error(), "binary_path") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning integrations.dcg binary_path, got %v", errs)
	}
}

func TestCASSContextGetValue(t *testing.T) {
	cfg := Default()

	tests := []struct {
		path string
		want interface{}
	}{
		{"cass.context.enabled", true},
		{"cass.context.max_sessions", 3},
		{"cass.context.lookback_days", 30},
		{"cass.context.max_tokens", 2000},
		{"cass.context.min_relevance", 0.5},
		{"cass.context.skip_if_context_above", float64(80)},
		{"cass.context.prefer_same_project", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Fatalf("GetValue(%q) error = %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("GetValue(%q) = %v (%T), want %v (%T)", tt.path, got, got, tt.want, tt.want)
			}
		})
	}
}

func TestCASSContextPrintOutput(t *testing.T) {
	cfg := Default()
	var buf bytes.Buffer
	err := Print(cfg, &buf)
	if err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	output := buf.String()

	// Check for cass.context section
	if !strings.Contains(output, "[cass.context]") {
		t.Error("Expected output to contain [cass.context]")
	}
	if !strings.Contains(output, "min_relevance") {
		t.Error("Expected output to contain min_relevance")
	}
	if !strings.Contains(output, "skip_if_context_above") {
		t.Error("Expected output to contain skip_if_context_above")
	}
	if !strings.Contains(output, "prefer_same_project") {
		t.Error("Expected output to contain prefer_same_project")
	}
}

func TestSessionRecoveryDefaults(t *testing.T) {
	cfg := Default()

	if !cfg.SessionRecovery.Enabled {
		t.Error("SessionRecovery should be enabled by default")
	}
	if !cfg.SessionRecovery.IncludeCMMemories {
		t.Error("SessionRecovery.IncludeCMMemories should be true by default")
	}
	if !cfg.SessionRecovery.IncludeAgentMail {
		t.Error("SessionRecovery.IncludeAgentMail should be true by default")
	}
	if !cfg.SessionRecovery.IncludeBeadsContext {
		t.Error("SessionRecovery.IncludeBeadsContext should be true by default")
	}
	if cfg.SessionRecovery.MaxRecoveryTokens != 2000 {
		t.Errorf("Expected MaxRecoveryTokens 2000, got %d", cfg.SessionRecovery.MaxRecoveryTokens)
	}
	if !cfg.SessionRecovery.AutoInjectOnSpawn {
		t.Error("SessionRecovery.AutoInjectOnSpawn should be true by default")
	}
	if cfg.SessionRecovery.StaleThresholdHours != 24 {
		t.Errorf("Expected StaleThresholdHours 24, got %d", cfg.SessionRecovery.StaleThresholdHours)
	}
}

func TestSessionRecoveryFromTOML(t *testing.T) {
	configContent := `
[recovery]
enabled = false
include_cm_memories = false
include_agent_mail = false
include_beads_context = false
max_recovery_tokens = 5000
auto_inject_on_spawn = false
stale_threshold_hours = 48
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.SessionRecovery.Enabled {
		t.Error("Expected enabled = false")
	}
	if cfg.SessionRecovery.IncludeCMMemories {
		t.Error("Expected include_cm_memories = false")
	}
	if cfg.SessionRecovery.IncludeAgentMail {
		t.Error("Expected include_agent_mail = false")
	}
	if cfg.SessionRecovery.IncludeBeadsContext {
		t.Error("Expected include_beads_context = false")
	}
	if cfg.SessionRecovery.MaxRecoveryTokens != 5000 {
		t.Errorf("Expected MaxRecoveryTokens 5000, got %d", cfg.SessionRecovery.MaxRecoveryTokens)
	}
	if cfg.SessionRecovery.AutoInjectOnSpawn {
		t.Error("Expected auto_inject_on_spawn = false")
	}
	if cfg.SessionRecovery.StaleThresholdHours != 48 {
		t.Errorf("Expected StaleThresholdHours 48, got %d", cfg.SessionRecovery.StaleThresholdHours)
	}
}

func TestSessionRecoveryEnvOverrides(t *testing.T) {
	// Save original values
	origEnabled := os.Getenv("NTM_RECOVERY_ENABLED")
	origIncludeCM := os.Getenv("NTM_RECOVERY_INCLUDE_CM")
	origIncludeAgentMail := os.Getenv("NTM_RECOVERY_INCLUDE_AGENT_MAIL")
	origIncludeBeads := os.Getenv("NTM_RECOVERY_INCLUDE_BEADS")
	origMaxTokens := os.Getenv("NTM_RECOVERY_MAX_TOKENS")
	origAutoInject := os.Getenv("NTM_RECOVERY_AUTO_INJECT")
	origStaleHours := os.Getenv("NTM_RECOVERY_STALE_HOURS")

	// Clear env vars before test
	os.Unsetenv("NTM_RECOVERY_ENABLED")
	os.Unsetenv("NTM_RECOVERY_INCLUDE_CM")
	os.Unsetenv("NTM_RECOVERY_INCLUDE_AGENT_MAIL")
	os.Unsetenv("NTM_RECOVERY_INCLUDE_BEADS")
	os.Unsetenv("NTM_RECOVERY_MAX_TOKENS")
	os.Unsetenv("NTM_RECOVERY_AUTO_INJECT")
	os.Unsetenv("NTM_RECOVERY_STALE_HOURS")

	defer func() {
		os.Setenv("NTM_RECOVERY_ENABLED", origEnabled)
		os.Setenv("NTM_RECOVERY_INCLUDE_CM", origIncludeCM)
		os.Setenv("NTM_RECOVERY_INCLUDE_AGENT_MAIL", origIncludeAgentMail)
		os.Setenv("NTM_RECOVERY_INCLUDE_BEADS", origIncludeBeads)
		os.Setenv("NTM_RECOVERY_MAX_TOKENS", origMaxTokens)
		os.Setenv("NTM_RECOVERY_AUTO_INJECT", origAutoInject)
		os.Setenv("NTM_RECOVERY_STALE_HOURS", origStaleHours)
	}()

	// Create a config file with defaults (enabled = true)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[recovery]
enabled = true
include_cm_memories = true
include_agent_mail = true
include_beads_context = true
max_recovery_tokens = 2000
auto_inject_on_spawn = true
stale_threshold_hours = 24
`), 0644); err != nil {
		t.Fatalf("Failed to create config file: %v", err)
	}

	// Test NTM_RECOVERY_ENABLED=false
	os.Setenv("NTM_RECOVERY_ENABLED", "false")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.Enabled {
		t.Error("SessionRecovery should be disabled via NTM_RECOVERY_ENABLED=false")
	}

	// Test NTM_RECOVERY_ENABLED=0 (also means false)
	os.Setenv("NTM_RECOVERY_ENABLED", "0")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.Enabled {
		t.Error("SessionRecovery should be disabled via NTM_RECOVERY_ENABLED=0")
	}

	// Test NTM_RECOVERY_INCLUDE_CM=false
	os.Setenv("NTM_RECOVERY_ENABLED", "true")
	os.Setenv("NTM_RECOVERY_INCLUDE_CM", "false")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.IncludeCMMemories {
		t.Error("IncludeCMMemories should be false via NTM_RECOVERY_INCLUDE_CM=false")
	}

	// Test NTM_RECOVERY_INCLUDE_AGENT_MAIL=false
	os.Setenv("NTM_RECOVERY_INCLUDE_AGENT_MAIL", "false")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.IncludeAgentMail {
		t.Error("IncludeAgentMail should be false via NTM_RECOVERY_INCLUDE_AGENT_MAIL=false")
	}

	// Test NTM_RECOVERY_INCLUDE_BEADS=false
	os.Setenv("NTM_RECOVERY_INCLUDE_BEADS", "false")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.IncludeBeadsContext {
		t.Error("IncludeBeadsContext should be false via NTM_RECOVERY_INCLUDE_BEADS=false")
	}

	// Test NTM_RECOVERY_MAX_TOKENS
	os.Setenv("NTM_RECOVERY_MAX_TOKENS", "5000")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.MaxRecoveryTokens != 5000 {
		t.Errorf("Expected MaxRecoveryTokens 5000 from env, got %d", cfg.SessionRecovery.MaxRecoveryTokens)
	}

	// Test invalid/negative MaxTokens is rejected
	os.Setenv("NTM_RECOVERY_MAX_TOKENS", "-100")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.MaxRecoveryTokens != 2000 { // Should keep config value, not env
		t.Errorf("Negative MaxTokens should be rejected, got %d", cfg.SessionRecovery.MaxRecoveryTokens)
	}

	// Test NTM_RECOVERY_AUTO_INJECT=false
	os.Setenv("NTM_RECOVERY_MAX_TOKENS", "2000") // Reset to valid
	os.Setenv("NTM_RECOVERY_AUTO_INJECT", "false")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.AutoInjectOnSpawn {
		t.Error("AutoInjectOnSpawn should be false via NTM_RECOVERY_AUTO_INJECT=false")
	}

	// Test NTM_RECOVERY_AUTO_INJECT=1 (means true)
	os.Setenv("NTM_RECOVERY_AUTO_INJECT", "1")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.SessionRecovery.AutoInjectOnSpawn {
		t.Error("AutoInjectOnSpawn should be true via NTM_RECOVERY_AUTO_INJECT=1")
	}

	// Test NTM_RECOVERY_STALE_HOURS
	os.Setenv("NTM_RECOVERY_STALE_HOURS", "48")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.StaleThresholdHours != 48 {
		t.Errorf("Expected StaleThresholdHours 48 from env, got %d", cfg.SessionRecovery.StaleThresholdHours)
	}

	// Test invalid/negative StaleHours is rejected
	os.Setenv("NTM_RECOVERY_STALE_HOURS", "-10")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SessionRecovery.StaleThresholdHours != 24 { // Should keep config value, not env
		t.Errorf("Negative StaleHours should be rejected, got %d", cfg.SessionRecovery.StaleThresholdHours)
	}
}

func TestDefaultAssignConfig(t *testing.T) {
	cfg := DefaultAssignConfig()

	if cfg.Strategy != "balanced" {
		t.Errorf("Expected default strategy 'balanced', got %q", cfg.Strategy)
	}
}

func TestIsValidStrategy(t *testing.T) {
	tests := []struct {
		strategy string
		valid    bool
	}{
		{"balanced", true},
		{"speed", true},
		{"quality", true},
		{"dependency", true},
		{"round-robin", true},
		{"invalid", false},
		{"", false},
		{"BALANCED", false}, // Case-sensitive
		{"bv", false},       // Non-existent strategy mentioned in spec
	}

	for _, tt := range tests {
		t.Run(tt.strategy, func(t *testing.T) {
			got := IsValidStrategy(tt.strategy)
			if got != tt.valid {
				t.Errorf("IsValidStrategy(%q) = %v, want %v", tt.strategy, got, tt.valid)
			}
		})
	}
}

func TestValidAssignStrategies(t *testing.T) {
	// Verify all expected strategies are present
	expected := []string{"balanced", "speed", "quality", "dependency", "round-robin"}
	if len(ValidAssignStrategies) != len(expected) {
		t.Errorf("Expected %d strategies, got %d", len(expected), len(ValidAssignStrategies))
	}

	for _, s := range expected {
		found := false
		for _, v := range ValidAssignStrategies {
			if v == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected strategy %q not found in ValidAssignStrategies", s)
		}
	}
}

func TestAssignConfigFromTOML(t *testing.T) {
	configContent := `
[assign]
strategy = "quality"
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Assign.Strategy != "quality" {
		t.Errorf("Expected strategy 'quality', got %q", cfg.Assign.Strategy)
	}
}

func TestAssignConfigDefaultInFullConfig(t *testing.T) {
	cfg := Default()

	if cfg.Assign.Strategy != "balanced" {
		t.Errorf("Expected default strategy 'balanced' in full config, got %q", cfg.Assign.Strategy)
	}
}

func TestDefaultCAAMConfig(t *testing.T) {
	cfg := DefaultCAAMConfig()

	if !cfg.Enabled {
		t.Error("Expected CAAM to be enabled by default")
	}
	if cfg.BinaryPath != "" {
		t.Errorf("Expected empty binary path (PATH lookup), got %q", cfg.BinaryPath)
	}
	if !cfg.AutoRotate {
		t.Error("Expected AutoRotate to be enabled by default")
	}
	if len(cfg.Providers) != 3 {
		t.Errorf("Expected 3 default providers, got %d", len(cfg.Providers))
	}
	if cfg.AccountCooldown != 300 {
		t.Errorf("Expected 300s account cooldown, got %d", cfg.AccountCooldown)
	}
	if cfg.AlertThreshold != 80 {
		t.Errorf("Expected 80%% alert threshold, got %d", cfg.AlertThreshold)
	}
}

func TestDefaultIntegrationsConfig(t *testing.T) {
	cfg := DefaultIntegrationsConfig()

	// Verify CAAM config is properly nested
	if !cfg.CAAM.Enabled {
		t.Error("Expected CAAM integration to be enabled by default")
	}
	if !cfg.CAAM.AutoRotate {
		t.Error("Expected CAAM AutoRotate to be enabled by default")
	}

	// Verify XF config defaults are present
	if !cfg.XF.Enabled {
		t.Error("Expected XF integration to be enabled by default")
	}
	if cfg.XF.BinPath != "xf" {
		t.Errorf("Expected XF bin_path 'xf', got %q", cfg.XF.BinPath)
	}
	if cfg.XF.ArchivePath != "~/.xf/archive" {
		t.Errorf("Expected XF archive_path '~/.xf/archive', got %q", cfg.XF.ArchivePath)
	}
	if cfg.XF.DefaultMode != "hybrid" {
		t.Errorf("Expected XF default_mode 'hybrid', got %q", cfg.XF.DefaultMode)
	}
}

func TestIntegrationsConfigInFullConfig(t *testing.T) {
	cfg := Default()

	// Verify integrations config is present and properly initialized
	if !cfg.Integrations.CAAM.Enabled {
		t.Error("Expected CAAM to be enabled in full config default")
	}
	if cfg.Integrations.CAAM.AccountCooldown != 300 {
		t.Errorf("Expected 300s cooldown in full config, got %d", cfg.Integrations.CAAM.AccountCooldown)
	}
}

func TestCAAMConfigFromTOML(t *testing.T) {
	configContent := `
	[integrations.caam]
	enabled = false
binary_path = "/usr/local/bin/caam"
auto_rotate = false
providers = ["claude"]
account_cooldown = 600
alert_threshold = 90
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Integrations.CAAM.Enabled {
		t.Error("Expected CAAM to be disabled")
	}
	if cfg.Integrations.CAAM.BinaryPath != "/usr/local/bin/caam" {
		t.Errorf("Expected binary path '/usr/local/bin/caam', got %q", cfg.Integrations.CAAM.BinaryPath)
	}
	if cfg.Integrations.CAAM.AutoRotate {
		t.Error("Expected AutoRotate to be disabled")
	}
	if len(cfg.Integrations.CAAM.Providers) != 1 || cfg.Integrations.CAAM.Providers[0] != "claude" {
		t.Errorf("Expected single 'claude' provider, got %v", cfg.Integrations.CAAM.Providers)
	}
	if cfg.Integrations.CAAM.AccountCooldown != 600 {
		t.Errorf("Expected 600s cooldown, got %d", cfg.Integrations.CAAM.AccountCooldown)
	}
	if cfg.Integrations.CAAM.AlertThreshold != 90 {
		t.Errorf("Expected 90%% threshold, got %d", cfg.Integrations.CAAM.AlertThreshold)
	}
}

func TestXFConfigFromTOML(t *testing.T) {
	configContent := `
	[integrations.xf]
	enabled = false
	bin_path = "/usr/local/bin/xf"
	archive_path = "~/.xf/custom-archive"
	default_mode = "semantic"
	`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Integrations.XF.Enabled {
		t.Error("Expected XF to be disabled")
	}
	if cfg.Integrations.XF.BinPath != "/usr/local/bin/xf" {
		t.Errorf("Expected bin_path '/usr/local/bin/xf', got %q", cfg.Integrations.XF.BinPath)
	}
	if cfg.Integrations.XF.ArchivePath != "~/.xf/custom-archive" {
		t.Errorf("Expected archive_path '~/.xf/custom-archive', got %q", cfg.Integrations.XF.ArchivePath)
	}
	if cfg.Integrations.XF.DefaultMode != "semantic" {
		t.Errorf("Expected default_mode 'semantic', got %q", cfg.Integrations.XF.DefaultMode)
	}
}

func TestDefaultProcessTriageConfig(t *testing.T) {
	cfg := DefaultProcessTriageConfig()

	if !cfg.Enabled {
		t.Error("Expected ProcessTriage to be enabled by default")
	}
	if cfg.BinaryPath != "" {
		t.Errorf("Expected empty binary path (PATH lookup), got %q", cfg.BinaryPath)
	}
	if cfg.CheckInterval != 30 {
		t.Errorf("Expected 30s check interval, got %d", cfg.CheckInterval)
	}
	if cfg.IdleThreshold != 300 {
		t.Errorf("Expected 300s idle threshold, got %d", cfg.IdleThreshold)
	}
	if cfg.StuckThreshold != 600 {
		t.Errorf("Expected 600s stuck threshold, got %d", cfg.StuckThreshold)
	}
	if cfg.OnStuck != "alert" {
		t.Errorf("Expected 'alert' on_stuck action, got %q", cfg.OnStuck)
	}
	if !cfg.UseRanoData {
		t.Error("Expected UseRanoData to be enabled by default")
	}
}

func TestValidateXFConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     XFConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid default config",
			cfg:     DefaultXFConfig(),
			wantErr: false,
		},
		{
			name: "disabled skips validation",
			cfg: XFConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "missing bin_path",
			cfg: XFConfig{
				Enabled:     true,
				BinPath:     "",
				ArchivePath: "~/.xf/archive",
				DefaultMode: "hybrid",
			},
			wantErr: true,
			errMsg:  "bin_path",
		},
		{
			name: "missing archive_path",
			cfg: XFConfig{
				Enabled:     true,
				BinPath:     "xf",
				ArchivePath: "",
				DefaultMode: "hybrid",
			},
			wantErr: true,
			errMsg:  "archive_path",
		},
		{
			name: "invalid default_mode",
			cfg: XFConfig{
				Enabled:     true,
				BinPath:     "xf",
				ArchivePath: "~/.xf/archive",
				DefaultMode: "wat",
			},
			wantErr: true,
			errMsg:  "default_mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateXFConfig(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %v", tt.errMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

func TestProcessTriageInIntegrationsConfig(t *testing.T) {
	cfg := DefaultIntegrationsConfig()

	// Verify ProcessTriage config is properly nested
	if !cfg.ProcessTriage.Enabled {
		t.Error("Expected ProcessTriage integration to be enabled by default")
	}
	if cfg.ProcessTriage.CheckInterval != 30 {
		t.Errorf("Expected 30s check interval, got %d", cfg.ProcessTriage.CheckInterval)
	}
	if cfg.ProcessTriage.OnStuck != "alert" {
		t.Errorf("Expected 'alert' on_stuck, got %q", cfg.ProcessTriage.OnStuck)
	}
}

func TestProcessTriageInFullConfig(t *testing.T) {
	cfg := Default()

	// Verify ProcessTriage config is present and properly initialized
	if !cfg.Integrations.ProcessTriage.Enabled {
		t.Error("Expected ProcessTriage to be enabled in full config default")
	}
	if cfg.Integrations.ProcessTriage.StuckThreshold != 600 {
		t.Errorf("Expected 600s stuck threshold in full config, got %d", cfg.Integrations.ProcessTriage.StuckThreshold)
	}
}

func TestProcessTriageConfigFromTOML(t *testing.T) {
	configContent := `
[integrations.process_triage]
enabled = false
binary_path = "/usr/local/bin/pt"
check_interval = 60
idle_threshold = 600
stuck_threshold = 1200
on_stuck = "kill"
use_rano_data = false
`
	configPath := createTempConfig(t, configContent)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Integrations.ProcessTriage.Enabled {
		t.Error("Expected ProcessTriage to be disabled")
	}
	if cfg.Integrations.ProcessTriage.BinaryPath != "/usr/local/bin/pt" {
		t.Errorf("Expected binary path '/usr/local/bin/pt', got %q", cfg.Integrations.ProcessTriage.BinaryPath)
	}
	if cfg.Integrations.ProcessTriage.CheckInterval != 60 {
		t.Errorf("Expected 60s check interval, got %d", cfg.Integrations.ProcessTriage.CheckInterval)
	}
	if cfg.Integrations.ProcessTriage.IdleThreshold != 600 {
		t.Errorf("Expected 600s idle threshold, got %d", cfg.Integrations.ProcessTriage.IdleThreshold)
	}
	if cfg.Integrations.ProcessTriage.StuckThreshold != 1200 {
		t.Errorf("Expected 1200s stuck threshold, got %d", cfg.Integrations.ProcessTriage.StuckThreshold)
	}
	if cfg.Integrations.ProcessTriage.OnStuck != "kill" {
		t.Errorf("Expected 'kill' on_stuck, got %q", cfg.Integrations.ProcessTriage.OnStuck)
	}
	if cfg.Integrations.ProcessTriage.UseRanoData {
		t.Error("Expected UseRanoData to be disabled")
	}
}

func TestValidateProcessTriageConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ProcessTriageConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid default config",
			cfg:     DefaultProcessTriageConfig(),
			wantErr: false,
		},
		{
			name: "valid custom config",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				BinaryPath:     "",
				CheckInterval:  60,
				IdleThreshold:  300,
				StuckThreshold: 600,
				OnStuck:        "kill",
				UseRanoData:    false,
			},
			wantErr: false,
		},
		{
			name: "check_interval too low",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  3,
				IdleThreshold:  300,
				StuckThreshold: 600,
				OnStuck:        "alert",
			},
			wantErr: true,
			errMsg:  "check_interval must be at least 5 seconds",
		},
		{
			name: "idle_threshold too low",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  30,
				IdleThreshold:  20,
				StuckThreshold: 600,
				OnStuck:        "alert",
			},
			wantErr: true,
			errMsg:  "idle_threshold must be at least 30 seconds",
		},
		{
			name: "stuck_threshold less than idle_threshold",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  30,
				IdleThreshold:  600,
				StuckThreshold: 300,
				OnStuck:        "alert",
			},
			wantErr: true,
			errMsg:  "stuck_threshold (300) must be >= idle_threshold (600)",
		},
		{
			name: "invalid on_stuck action",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  30,
				IdleThreshold:  300,
				StuckThreshold: 600,
				OnStuck:        "invalid",
			},
			wantErr: true,
			errMsg:  "on_stuck must be 'alert', 'kill', or 'ignore'",
		},
		{
			name: "on_stuck ignore is valid",
			cfg: ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  30,
				IdleThreshold:  300,
				StuckThreshold: 600,
				OnStuck:        "ignore",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProcessTriageConfig(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateProcessTriageConfigNil(t *testing.T) {
	err := ValidateProcessTriageConfig(nil)
	if err != nil {
		t.Errorf("Expected nil error for nil config, got %v", err)
	}
}

// TestDefaultRobotOutputConfig verifies the default robot output configuration
func TestDefaultRobotOutputConfig(t *testing.T) {
	cfg := DefaultRobotOutputConfig()

	if cfg.Format != "json" {
		t.Errorf("Expected default format 'json', got %q", cfg.Format)
	}
	if cfg.Pretty {
		t.Error("Expected default pretty to be false")
	}
	if !cfg.Timestamps {
		t.Error("Expected default timestamps to be true")
	}
	if cfg.Compress {
		t.Error("Expected default compress to be false")
	}
}

// TestValidateRobotOutputConfig tests validation of robot output configuration
func TestValidateRobotOutputConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RobotOutputConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid json format",
			cfg: RobotOutputConfig{
				Format:     "json",
				Pretty:     false,
				Timestamps: true,
				Compress:   false,
			},
			wantErr: false,
		},
		{
			name: "valid toon format",
			cfg: RobotOutputConfig{
				Format:     "toon",
				Pretty:     true,
				Timestamps: true,
				Compress:   false,
			},
			wantErr: false,
		},
		{
			name: "invalid format",
			cfg: RobotOutputConfig{
				Format: "xml",
			},
			wantErr: true,
			errMsg:  "invalid robot output format",
		},
		{
			name: "empty format defaults to json (valid)",
			cfg: RobotOutputConfig{
				Format: "",
			},
			wantErr: false,
		},
		{
			name:    "default config is valid",
			cfg:     DefaultRobotOutputConfig(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRobotOutputConfig(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestRobotOutputConfigFromTOML tests parsing robot.output from TOML
func TestRobotOutputConfigFromTOML(t *testing.T) {
	content := `
[robot]
verbosity = "debug"

[robot.output]
format = "toon"
pretty = true
timestamps = false
compress = true
`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Robot.Verbosity != "debug" {
		t.Errorf("Expected verbosity 'debug', got %q", cfg.Robot.Verbosity)
	}
	if cfg.Robot.Output.Format != "toon" {
		t.Errorf("Expected format 'toon', got %q", cfg.Robot.Output.Format)
	}
	if !cfg.Robot.Output.Pretty {
		t.Error("Expected pretty to be true")
	}
	if cfg.Robot.Output.Timestamps {
		t.Error("Expected timestamps to be false")
	}
	if !cfg.Robot.Output.Compress {
		t.Error("Expected compress to be true")
	}
}

// TestRobotOutputConfigDefaults tests that defaults are applied for missing robot.output
func TestRobotOutputConfigDefaults(t *testing.T) {
	content := `
[robot]
verbosity = "terse"
`
	path := createTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Robot verbosity should be overridden
	if cfg.Robot.Verbosity != "terse" {
		t.Errorf("Expected verbosity 'terse', got %q", cfg.Robot.Verbosity)
	}

	// Robot.Output should use defaults since not specified
	defaults := DefaultRobotOutputConfig()
	if cfg.Robot.Output.Format != defaults.Format {
		t.Errorf("Expected default format %q, got %q", defaults.Format, cfg.Robot.Output.Format)
	}
	if cfg.Robot.Output.Pretty != defaults.Pretty {
		t.Errorf("Expected default pretty %v, got %v", defaults.Pretty, cfg.Robot.Output.Pretty)
	}
	if cfg.Robot.Output.Timestamps != defaults.Timestamps {
		t.Errorf("Expected default timestamps %v, got %v", defaults.Timestamps, cfg.Robot.Output.Timestamps)
	}
}

// TestValidateRejectsInvalidRobotOutputFormat tests that Validate() catches invalid robot.output.format
func TestValidateRejectsInvalidRobotOutputFormat(t *testing.T) {
	cfg := Default()
	cfg.Robot.Output.Format = "invalid"

	errs := Validate(cfg)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "robot.output") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected Validate to return error for invalid robot.output.format")
	}
}

func TestValidateRejectsInvalidRedactionMode(t *testing.T) {
	cfg := Default()
	cfg.Redaction.Mode = "invalid"

	errs := Validate(cfg)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "redaction") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected Validate to return error for invalid redaction mode")
	}
}

func TestUpsertTOMLTable(t *testing.T) {
	t.Parallel()

	t.Run("insert new table", func(t *testing.T) {
		t.Parallel()
		contents := "key = \"value\"\n"
		got := upsertTOMLTable(contents, "new_section", "[new_section]\nfoo = \"bar\"\n")
		if !strings.Contains(got, "[new_section]") {
			t.Error("expected [new_section] in output")
		}
		if !strings.Contains(got, "foo = \"bar\"") {
			t.Error("expected foo = bar in output")
		}
		if !strings.Contains(got, "key = \"value\"") {
			t.Error("existing content should be preserved")
		}
	})

	t.Run("replace existing table", func(t *testing.T) {
		t.Parallel()
		contents := "[existing]\nold = \"data\"\n\n[other]\nkeep = \"this\"\n"
		got := upsertTOMLTable(contents, "existing", "[existing]\nnew = \"data\"\n")
		if strings.Contains(got, "old = \"data\"") {
			t.Error("old table content should be removed")
		}
		if !strings.Contains(got, "new = \"data\"") {
			t.Error("new table content should be present")
		}
		if !strings.Contains(got, "[other]") {
			t.Error("[other] table should be preserved")
		}
	})

	t.Run("empty contents", func(t *testing.T) {
		t.Parallel()
		got := upsertTOMLTable("", "section", "[section]\nval = 1\n")
		if !strings.Contains(got, "[section]") {
			t.Error("expected [section] in output")
		}
	})

	t.Run("ensures trailing newline", func(t *testing.T) {
		t.Parallel()
		got := upsertTOMLTable("", "s", "[s]\nk = 1")
		if !strings.HasSuffix(got, "\n") {
			t.Error("output should end with newline")
		}
	})
}

func TestUpsertTOMLKey(t *testing.T) {
	t.Parallel()

	t.Run("update existing key", func(t *testing.T) {
		t.Parallel()
		contents := "name = \"old\"\nother = \"keep\"\n"
		got := upsertTOMLKey(contents, "name", "new")
		if !strings.Contains(got, `name = "new"`) {
			t.Errorf("expected updated key, got %q", got)
		}
		if !strings.Contains(got, `other = "keep"`) {
			t.Error("other keys should be preserved")
		}
	})

	t.Run("insert new key", func(t *testing.T) {
		t.Parallel()
		contents := "existing = \"value\"\n"
		got := upsertTOMLKey(contents, "newkey", "newval")
		if !strings.Contains(got, `newkey = "newval"`) {
			t.Errorf("expected new key, got %q", got)
		}
		if !strings.Contains(got, `existing = "value"`) {
			t.Error("existing content should be preserved")
		}
	})

	t.Run("insert after comments", func(t *testing.T) {
		t.Parallel()
		contents := "# comment\n# another\nexisting = \"val\"\n"
		got := upsertTOMLKey(contents, "newkey", "newval")
		if !strings.Contains(got, `newkey = "newval"`) {
			t.Errorf("expected new key, got %q", got)
		}
	})

	t.Run("ensures trailing newline", func(t *testing.T) {
		t.Parallel()
		got := upsertTOMLKey("k = \"v\"", "k", "v2")
		if !strings.HasSuffix(got, "\n") {
			t.Error("output should end with newline")
		}
	})
}

func TestRenderTOMLStringArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"empty", nil, "[]"},
		{"single", []string{"a"}, `[ "a" ]`},
		{"multiple", []string{"a", "b", "c"}, `[ "a", "b", "c" ]`},
		{"deduplicates", []string{"a", "b", "a"}, `[ "a", "b" ]`},
		{"trims whitespace", []string{" a ", " b "}, `[ "a", "b" ]`},
		{"filters empty", []string{"a", "", "b"}, `[ "a", "b" ]`},
		{"all empty", []string{"", " "}, "[]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderTOMLStringArray(tc.values)
			if got != tc.want {
				t.Errorf("renderTOMLStringArray(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

func TestRenderPaletteStateTOML(t *testing.T) {
	t.Parallel()

	t.Run("empty state", func(t *testing.T) {
		t.Parallel()
		got := renderPaletteStateTOML(PaletteState{})
		if !strings.Contains(got, "[palette_state]") {
			t.Error("expected [palette_state] header")
		}
		if !strings.Contains(got, "pinned = []") {
			t.Error("expected empty pinned array")
		}
		if !strings.Contains(got, "favorites = []") {
			t.Error("expected empty favorites array")
		}
	})

	t.Run("with values", func(t *testing.T) {
		t.Parallel()
		state := PaletteState{
			Pinned:    []string{"cmd1", "cmd2"},
			Favorites: []string{"fav1"},
		}
		got := renderPaletteStateTOML(state)
		if !strings.Contains(got, `"cmd1"`) {
			t.Error("expected cmd1 in output")
		}
		if !strings.Contains(got, `"fav1"`) {
			t.Error("expected fav1 in output")
		}
	})
}

// ============================================================================
// RedactionConfig Tests
// ============================================================================

func TestDefaultRedactionConfig(t *testing.T) {
	cfg := DefaultRedactionConfig()

	if cfg.Mode != "warn" {
		t.Errorf("Default redaction mode should be 'warn', got %q", cfg.Mode)
	}

	if len(cfg.Allowlist) != 0 {
		t.Errorf("Default allowlist should be empty, got %d items", len(cfg.Allowlist))
	}

	if len(cfg.ExtraPatterns) != 0 {
		t.Errorf("Default extra patterns should be empty, got %d items", len(cfg.ExtraPatterns))
	}

	if len(cfg.DisabledCategories) != 0 {
		t.Errorf("Default disabled categories should be empty, got %d items", len(cfg.DisabledCategories))
	}
}

func TestValidateRedactionConfig(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{"empty mode is valid", "", false},
		{"off mode", "off", false},
		{"warn mode", "warn", false},
		{"redact mode", "redact", false},
		{"block mode", "block", false},
		{"invalid mode", "invalid", true},
		{"uppercase mode is invalid", "WARN", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RedactionConfig{Mode: tt.mode}
			err := ValidateRedactionConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRedactionConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRedactionConfig_ToRedactionLibConfig(t *testing.T) {
	t.Run("basic conversion", func(t *testing.T) {
		cfg := &RedactionConfig{
			Mode:      "redact",
			Allowlist: []string{"test-.*", "example"},
		}

		libCfg := cfg.ToRedactionLibConfig()

		if string(libCfg.Mode) != "redact" {
			t.Errorf("Mode should be 'redact', got %q", libCfg.Mode)
		}

		if len(libCfg.Allowlist) != 2 {
			t.Errorf("Allowlist should have 2 items, got %d", len(libCfg.Allowlist))
		}
	})

	t.Run("with extra patterns", func(t *testing.T) {
		cfg := &RedactionConfig{
			Mode: "warn",
			ExtraPatterns: map[string][]string{
				"CUSTOM_TOKEN": {"custom-[a-z]+"},
			},
		}

		libCfg := cfg.ToRedactionLibConfig()

		if len(libCfg.ExtraPatterns) != 1 {
			t.Errorf("ExtraPatterns should have 1 category, got %d", len(libCfg.ExtraPatterns))
		}
	})

	t.Run("with disabled categories", func(t *testing.T) {
		cfg := &RedactionConfig{
			Mode:               "redact",
			DisabledCategories: []string{"JWT", "PASSWORD"},
		}

		libCfg := cfg.ToRedactionLibConfig()

		if len(libCfg.DisabledCategories) != 2 {
			t.Errorf("DisabledCategories should have 2 items, got %d", len(libCfg.DisabledCategories))
		}
	})

	t.Run("mode conversion", func(t *testing.T) {
		modes := map[string]string{
			"":       "warn", // default
			"off":    "off",
			"warn":   "warn",
			"redact": "redact",
			"block":  "block",
		}

		for input, expected := range modes {
			cfg := &RedactionConfig{Mode: input}
			libCfg := cfg.ToRedactionLibConfig()
			if string(libCfg.Mode) != expected {
				t.Errorf("Mode %q should convert to %q, got %q", input, expected, libCfg.Mode)
			}
		}
	})
}

func TestRedactionConfigInDefault(t *testing.T) {
	cfg := Default()

	if cfg.Redaction.Mode != "warn" {
		t.Errorf("Default config should have redaction mode 'warn', got %q", cfg.Redaction.Mode)
	}
}

func TestDefaultPrivacyConfig(t *testing.T) {
	cfg := DefaultPrivacyConfig()

	// Privacy mode should be disabled by default (opt-in)
	if cfg.Enabled {
		t.Error("Privacy mode should be disabled by default")
	}

	// When privacy mode is enabled, these should all be true by default
	if !cfg.DisablePromptHistory {
		t.Error("DisablePromptHistory should be true when privacy mode is enabled")
	}

	if !cfg.DisableEventLogs {
		t.Error("DisableEventLogs should be true when privacy mode is enabled")
	}

	if !cfg.DisableCheckpoints {
		t.Error("DisableCheckpoints should be true when privacy mode is enabled")
	}

	if !cfg.DisableScrollbackCapture {
		t.Error("DisableScrollbackCapture should be true when privacy mode is enabled")
	}

	if !cfg.RequireExplicitPersist {
		t.Error("RequireExplicitPersist should be true when privacy mode is enabled")
	}
}

func TestPrivacyConfigInDefault(t *testing.T) {
	cfg := Default()

	// Privacy config should be present with default values
	if cfg.Privacy.Enabled {
		t.Error("Default config should have privacy mode disabled")
	}
}

func TestValidatePrivacyConfig(t *testing.T) {
	// Validate should always succeed for PrivacyConfig (only boolean flags)
	tests := []PrivacyConfig{
		{},
		{Enabled: true},
		{Enabled: true, DisablePromptHistory: false}, // override defaults
		{Enabled: false, RequireExplicitPersist: true},
	}

	for i, cfg := range tests {
		if err := ValidatePrivacyConfig(&cfg); err != nil {
			t.Errorf("Test %d: ValidatePrivacyConfig should not error, got: %v", i, err)
		}
	}
}

func TestSafetyProfileDefaultsInDefault(t *testing.T) {
	cfg := Default()

	if cfg.Safety.Profile != SafetyProfileStandard {
		t.Errorf("Default safety profile = %q, want %q", cfg.Safety.Profile, SafetyProfileStandard)
	}
	if !cfg.Preflight.Enabled {
		t.Error("Default Preflight.Enabled should be true")
	}
	if cfg.Preflight.Strict {
		t.Error("Default Preflight.Strict should be false")
	}
	if cfg.Redaction.Mode != "warn" {
		t.Errorf("Default redaction mode = %q, want %q", cfg.Redaction.Mode, "warn")
	}
	if cfg.Privacy.Enabled {
		t.Error("Default privacy should be disabled")
	}
}

func TestLoadSafetyProfileAppliesDefaultsAndAllowsOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	t.Run("profile safe applies defaults", func(t *testing.T) {
		content := `
[safety]
profile = "safe"
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Safety.Profile != SafetyProfileSafe {
			t.Errorf("Safety.Profile = %q, want %q", cfg.Safety.Profile, SafetyProfileSafe)
		}
		if cfg.Redaction.Mode != "redact" {
			t.Errorf("Redaction.Mode = %q, want %q", cfg.Redaction.Mode, "redact")
		}
		if cfg.Privacy.Enabled {
			t.Error("Privacy.Enabled should be false for safe profile")
		}
		if cfg.Integrations.DCG.AllowOverride {
			t.Error("Integrations.DCG.AllowOverride should be false for safe profile")
		}
		if !cfg.Preflight.Enabled {
			t.Error("Preflight.Enabled should be true for safe profile")
		}
	})

	t.Run("explicit knob overrides profile defaults", func(t *testing.T) {
		content := `
[safety]
profile = "safe"

[redaction]
mode = "warn"
`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Safety.Profile != SafetyProfileSafe {
			t.Errorf("Safety.Profile = %q, want %q", cfg.Safety.Profile, SafetyProfileSafe)
		}
		if cfg.Redaction.Mode != "warn" {
			t.Errorf("Redaction.Mode = %q, want %q", cfg.Redaction.Mode, "warn")
		}
	})
}

func TestValidateSafetyConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SafetyConfig
		wantErr bool
	}{
		{name: "empty ok", cfg: SafetyConfig{}, wantErr: false},
		{name: "standard ok", cfg: SafetyConfig{Profile: "standard"}, wantErr: false},
		{name: "safe ok", cfg: SafetyConfig{Profile: "safe"}, wantErr: false},
		{name: "paranoid ok", cfg: SafetyConfig{Profile: "paranoid"}, wantErr: false},
		{name: "invalid", cfg: SafetyConfig{Profile: "nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSafetyConfig(&tt.cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// =============================================================================
// Rotation: GetAccountsForProvider / SuggestNextAccount
// =============================================================================

func TestRotationConfig_GetAccountsForProvider(t *testing.T) {
	t.Parallel()

	cfg := &RotationConfig{
		Accounts: []RotationAccount{
			{Provider: "claude", Email: "a@example.com"},
			{Provider: "codex", Email: "b@example.com"},
			{Provider: "claude", Email: "c@example.com"},
			{Provider: "gemini", Email: "d@example.com"},
		},
	}

	tests := []struct {
		provider string
		wantLen  int
	}{
		{"claude", 2},
		{"codex", 1},
		{"gemini", 1},
		{"unknown", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()
			accounts := cfg.GetAccountsForProvider(tt.provider)
			if len(accounts) != tt.wantLen {
				t.Errorf("GetAccountsForProvider(%q) returned %d accounts, want %d", tt.provider, len(accounts), tt.wantLen)
			}
		})
	}
}

func TestRotationConfig_GetAccountsForProvider_Empty(t *testing.T) {
	t.Parallel()
	cfg := &RotationConfig{}
	accounts := cfg.GetAccountsForProvider("claude")
	if len(accounts) != 0 {
		t.Errorf("Expected 0 accounts for empty config, got %d", len(accounts))
	}
}

func TestRotationConfig_SuggestNextAccount(t *testing.T) {
	t.Parallel()

	cfg := &RotationConfig{
		Accounts: []RotationAccount{
			{Provider: "claude", Email: "a@example.com"},
			{Provider: "claude", Email: "b@example.com"},
			{Provider: "codex", Email: "c@example.com"},
		},
	}

	tests := []struct {
		name         string
		provider     string
		currentEmail string
		wantEmail    string
		wantNil      bool
	}{
		{"suggests next claude", "claude", "a@example.com", "b@example.com", false},
		{"suggests first claude when current is second", "claude", "b@example.com", "a@example.com", false},
		{"nil when no other accounts", "codex", "c@example.com", "", true},
		{"nil for unknown provider", "unknown", "a@example.com", "", true},
		{"nil for empty provider", "", "a@example.com", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cfg.SuggestNextAccount(tt.provider, tt.currentEmail)
			if tt.wantNil {
				if got != nil {
					t.Errorf("SuggestNextAccount() = %v, want nil", got)
				}
			} else {
				if got == nil {
					t.Fatal("SuggestNextAccount() = nil, want non-nil")
				}
				if got.Email != tt.wantEmail {
					t.Errorf("SuggestNextAccount().Email = %q, want %q", got.Email, tt.wantEmail)
				}
			}
		})
	}
}

// =============================================================================
// Validation functions
// =============================================================================

func TestValidateFileReservationConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     FileReservationConfig
		wantErr bool
	}{
		{
			name:    "valid defaults",
			cfg:     DefaultFileReservationConfig(),
			wantErr: false,
		},
		{
			name:    "auto release disabled (0)",
			cfg:     FileReservationConfig{AutoReleaseIdleMin: 0, DefaultTTLMin: 5, PollIntervalSec: 5, CaptureLinesForDetect: 20},
			wantErr: false,
		},
		{
			name:    "auto release too low (not 0)",
			cfg:     FileReservationConfig{AutoReleaseIdleMin: -1, DefaultTTLMin: 5, PollIntervalSec: 5, CaptureLinesForDetect: 20},
			wantErr: true,
		},
		{
			name:    "TTL too low",
			cfg:     FileReservationConfig{AutoReleaseIdleMin: 0, DefaultTTLMin: 0, PollIntervalSec: 5, CaptureLinesForDetect: 20},
			wantErr: true,
		},
		{
			name:    "poll interval too low",
			cfg:     FileReservationConfig{AutoReleaseIdleMin: 0, DefaultTTLMin: 5, PollIntervalSec: 0, CaptureLinesForDetect: 20},
			wantErr: true,
		},
		{
			name:    "capture lines too low",
			cfg:     FileReservationConfig{AutoReleaseIdleMin: 0, DefaultTTLMin: 5, PollIntervalSec: 5, CaptureLinesForDetect: 5},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateFileReservationConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFileReservationConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMemoryConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     MemoryConfig
		wantErr bool
	}{
		{
			name:    "valid defaults",
			cfg:     DefaultMemoryConfig(),
			wantErr: false,
		},
		{
			name:    "max_rules zero is valid",
			cfg:     MemoryConfig{MaxRules: 0, QueryTimeoutSeconds: 5},
			wantErr: false,
		},
		{
			name:    "max_rules negative",
			cfg:     MemoryConfig{MaxRules: -1, QueryTimeoutSeconds: 5},
			wantErr: true,
		},
		{
			name:    "timeout too low",
			cfg:     MemoryConfig{MaxRules: 10, QueryTimeoutSeconds: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMemoryConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMemoryConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateActivityIndicatorConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ActivityIndicatorConfig
		wantErr bool
	}{
		{
			name:    "valid defaults",
			cfg:     DefaultActivityIndicatorConfig(),
			wantErr: false,
		},
		{
			name:    "active_seconds zero",
			cfg:     ActivityIndicatorConfig{ActiveSeconds: 0, StalledSeconds: 120},
			wantErr: true,
		},
		{
			name:    "stalled not greater than active",
			cfg:     ActivityIndicatorConfig{ActiveSeconds: 30, StalledSeconds: 30},
			wantErr: true,
		},
		{
			name:    "stalled less than active",
			cfg:     ActivityIndicatorConfig{ActiveSeconds: 30, StalledSeconds: 10},
			wantErr: true,
		},
		{
			name:    "minimal valid",
			cfg:     ActivityIndicatorConfig{ActiveSeconds: 1, StalledSeconds: 2},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateActivityIndicatorConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateActivityIndicatorConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRanoConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *RanoConfig
		wantErr bool
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: false,
		},
		{
			name:    "unconfigured zero-value",
			cfg:     &RanoConfig{},
			wantErr: false,
		},
		{
			name:    "valid with providers",
			cfg:     &RanoConfig{Enabled: true, PollIntervalMs: 1000, Providers: []string{"anthropic"}, HistoryDays: 7},
			wantErr: false,
		},
		{
			name:    "poll interval too low",
			cfg:     &RanoConfig{Enabled: true, PollIntervalMs: 50, Providers: []string{"anthropic"}},
			wantErr: true,
		},
		{
			name:    "negative history days",
			cfg:     &RanoConfig{Enabled: true, PollIntervalMs: 1000, Providers: []string{"anthropic"}, HistoryDays: -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRanoConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRanoConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// =============================================================================
// validateSynthesisStrategy
// =============================================================================

func TestValidateSynthesisStrategy(t *testing.T) {
	t.Parallel()

	t.Run("valid strategies", func(t *testing.T) {
		t.Parallel()
		valid := []string{"consensus", "creative", "analytical", "deliberative", "prioritized", "dialectical", "meta-reasoning", "voting", "argumentation"}
		for _, s := range valid {
			if err := validateSynthesisStrategy(s); err != nil {
				t.Errorf("validateSynthesisStrategy(%q) = %v, want nil", s, err)
			}
		}
	})

	t.Run("deprecated strategies", func(t *testing.T) {
		t.Parallel()
		deprecated := map[string]string{
			"debate":     "dialectical",
			"weighted":   "prioritized",
			"sequential": "manual",
			"best-of":    "prioritized",
		}
		for old, replacement := range deprecated {
			err := validateSynthesisStrategy(old)
			if err == nil {
				t.Errorf("validateSynthesisStrategy(%q) = nil, want error", old)
				continue
			}
			if !strings.Contains(err.Error(), replacement) {
				t.Errorf("validateSynthesisStrategy(%q) error should mention %q, got: %v", old, replacement, err)
			}
		}
	})

	t.Run("unknown strategy", func(t *testing.T) {
		t.Parallel()
		err := validateSynthesisStrategy("nonexistent")
		if err == nil {
			t.Error("validateSynthesisStrategy(\"nonexistent\") = nil, want error")
		}
		if !strings.Contains(err.Error(), "unknown") {
			t.Errorf("error should mention 'unknown', got: %v", err)
		}
	})
}

// =============================================================================
// dirWritable
// =============================================================================

func TestDirWritable(t *testing.T) {
	t.Parallel()

	t.Run("nil info", func(t *testing.T) {
		t.Parallel()
		if dirWritable(nil) {
			t.Error("dirWritable(nil) = true, want false")
		}
	})

	t.Run("writable directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat failed: %v", err)
		}
		if !dirWritable(info) {
			t.Error("dirWritable should return true for writable temp dir")
		}
	})
}

// =============================================================================
// ValidateEnsembleConfig
// =============================================================================

func TestValidateEnsembleConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *EnsembleConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: false,
		},
		{
			name:    "empty config",
			cfg:     &EnsembleConfig{},
			wantErr: false,
		},
		{
			name:    "valid assignment round-robin",
			cfg:     &EnsembleConfig{Assignment: "round-robin"},
			wantErr: false,
		},
		{
			name:    "valid assignment affinity",
			cfg:     &EnsembleConfig{Assignment: "affinity"},
			wantErr: false,
		},
		{
			name:    "invalid assignment",
			cfg:     &EnsembleConfig{Assignment: "invalid-assignment"},
			wantErr: true,
			errMsg:  "assignment",
		},
		{
			name:    "valid mode tier core",
			cfg:     &EnsembleConfig{ModeTierDefault: "core"},
			wantErr: false,
		},
		{
			name:    "valid mode tier advanced",
			cfg:     &EnsembleConfig{ModeTierDefault: "advanced"},
			wantErr: false,
		},
		{
			name:    "invalid mode tier",
			cfg:     &EnsembleConfig{ModeTierDefault: "invalid-tier"},
			wantErr: true,
			errMsg:  "mode_tier_default",
		},
		{
			name: "invalid synthesis min_confidence negative",
			cfg: &EnsembleConfig{
				Synthesis: EnsembleSynthesisConfig{MinConfidence: -0.5},
			},
			wantErr: true,
			errMsg:  "min_confidence",
		},
		{
			name: "invalid synthesis min_confidence too high",
			cfg: &EnsembleConfig{
				Synthesis: EnsembleSynthesisConfig{MinConfidence: 1.5},
			},
			wantErr: true,
			errMsg:  "min_confidence",
		},
		{
			name: "invalid synthesis max_findings negative",
			cfg: &EnsembleConfig{
				Synthesis: EnsembleSynthesisConfig{MaxFindings: -1},
			},
			wantErr: true,
			errMsg:  "max_findings",
		},
		{
			name: "invalid budget per_agent negative",
			cfg: &EnsembleConfig{
				Budget: EnsembleBudgetConfig{PerAgent: -100},
			},
			wantErr: true,
			errMsg:  "budget",
		},
		{
			name: "invalid budget per_agent > total",
			cfg: &EnsembleConfig{
				Budget: EnsembleBudgetConfig{PerAgent: 1000, Total: 500},
			},
			wantErr: true,
			errMsg:  "per_agent",
		},
		{
			name: "invalid cache ttl negative",
			cfg: &EnsembleConfig{
				Cache: EnsembleCacheConfig{TTLMinutes: -1},
			},
			wantErr: true,
			errMsg:  "ttl_minutes",
		},
		{
			name: "invalid cache max_entries negative",
			cfg: &EnsembleConfig{
				Cache: EnsembleCacheConfig{MaxEntries: -1},
			},
			wantErr: true,
			errMsg:  "max_entries",
		},
		// Additional test cases for remaining branches
		{
			name:    "valid assignment category",
			cfg:     &EnsembleConfig{Assignment: "category"},
			wantErr: false,
		},
		{
			name:    "valid assignment explicit",
			cfg:     &EnsembleConfig{Assignment: "explicit"},
			wantErr: false,
		},
		{
			name:    "valid mode tier experimental",
			cfg:     &EnsembleConfig{ModeTierDefault: "experimental"},
			wantErr: false,
		},
		{
			name: "invalid synthesis strategy",
			cfg: &EnsembleConfig{
				Synthesis: EnsembleSynthesisConfig{Strategy: "invalid-strategy"},
			},
			wantErr: true,
			errMsg:  "synthesis.strategy",
		},
		{
			name: "invalid budget total negative",
			cfg: &EnsembleConfig{
				Budget: EnsembleBudgetConfig{Total: -500},
			},
			wantErr: true,
			errMsg:  "budget",
		},
		{
			name: "invalid budget synthesis negative",
			cfg: &EnsembleConfig{
				Budget: EnsembleBudgetConfig{Synthesis: -100},
			},
			wantErr: true,
			errMsg:  "budget",
		},
		{
			name: "invalid budget context_pack negative",
			cfg: &EnsembleConfig{
				Budget: EnsembleBudgetConfig{ContextPack: -50},
			},
			wantErr: true,
			errMsg:  "budget",
		},
		{
			name: "invalid early_stop min_agents negative",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{MinAgents: -1},
			},
			wantErr: true,
			errMsg:  "min_agents",
		},
		{
			name: "invalid early_stop window_size negative",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{WindowSize: -1},
			},
			wantErr: true,
			errMsg:  "window_size",
		},
		{
			name: "invalid early_stop findings_threshold negative",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{FindingsThreshold: -0.5},
			},
			wantErr: true,
			errMsg:  "findings_threshold",
		},
		{
			name: "invalid early_stop findings_threshold too high",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{FindingsThreshold: 1.5},
			},
			wantErr: true,
			errMsg:  "findings_threshold",
		},
		{
			name: "invalid early_stop similarity_threshold negative",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{SimilarityThreshold: -0.5},
			},
			wantErr: true,
			errMsg:  "similarity_threshold",
		},
		{
			name: "invalid early_stop similarity_threshold too high",
			cfg: &EnsembleConfig{
				EarlyStop: EnsembleEarlyStopConfig{SimilarityThreshold: 1.5},
			},
			wantErr: true,
			errMsg:  "similarity_threshold",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEnsembleConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidateEnsembleConfig() = nil, want error containing %q", tc.errMsg)
				} else if !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("error = %q, should contain %q", err.Error(), tc.errMsg)
				}
			} else if err != nil {
				t.Errorf("ValidateEnsembleConfig() = %v, want nil", err)
			}
		})
	}
}
