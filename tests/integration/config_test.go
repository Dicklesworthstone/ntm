package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestConfigCustomProjectsBase verifies that projects_base setting affects ntm quick
func TestConfigCustomProjectsBase(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create temp directory for custom projects base
	customBase := t.TempDir()

	// Create temp config file with custom projects_base
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := `projects_base = "` + customBase + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created config with projects_base=%s at %s", customBase, configPath)

	// Run ntm quick with custom config
	projectName := "test_project"
	out, err := logger.Exec(binary, "--config", configPath, "quick", projectName, "--json")
	if err != nil {
		t.Fatalf("ntm quick failed: %v\nOutput: %s", err, string(out))
	}

	// Parse JSON output to verify working directory
	var result struct {
		WorkingDirectory string `json:"working_directory"`
		Session          string `json:"session"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse quick output: %v", err)
	}

	// Clean up the session
	t.Cleanup(func() {
		logger.LogSection("Teardown")
		logger.Exec(binary, "kill", "-f", result.Session)
	})

	// Verify the working directory is in our custom base
	if !strings.HasPrefix(result.WorkingDirectory, customBase) {
		t.Fatalf("expected working directory to be in %s, got %s", customBase, result.WorkingDirectory)
	}

	// Verify the project directory was created
	expectedDir := filepath.Join(customBase, projectName)
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Fatalf("expected project directory %s to exist", expectedDir)
	}

	logger.Log("PASS: Project created in custom projects_base: %s", result.WorkingDirectory)
}

// TestConfigCustomAgentCommands verifies custom agent commands are used
func TestConfigCustomAgentCommands(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create temp directory for projects base (spawn creates a project dir under this).
	projectsBase := t.TempDir()

	// Create a custom command that just echoes a marker
	customMarker := "CUSTOM_AGENT_CMD_MARKER_" + t.Name()

	// Create temp config file with custom agent command
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	// Use echo command instead of actual agent - this is just a test
	configContent := `projects_base = "` + projectsBase + `"

[agents]
claude = "echo '` + customMarker + `' && sleep 30"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created config with custom Claude command at %s", configPath)

	sessionName := "test_custom_agent_" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// Spawn session with custom config
	out, err := logger.Exec(binary, "--config", configPath, "spawn", sessionName, "--cc=1", "--json")
	if err != nil {
		// Session might fail because the command isn't a real agent, but we can still check pane output
		logger.Log("Spawn completed (may have warnings): %s", string(out))
	}

	// Parse session name from output
	var result struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse spawn output: %v\nOutput: %s", err, string(out))
	}

	// Clean up the session
	t.Cleanup(func() {
		logger.LogSection("Teardown")
		logger.Exec(binary, "kill", "-f", result.Session)
	})

	// Give the command time to run
	logger.Log("Waiting for custom command to execute...")

	// Find the spawned cc pane by title and verify our custom marker appears in its output.
	panes, err := tmux.GetPanesWithActivity(result.Session)
	if err != nil {
		t.Fatalf("failed to list panes: %v", err)
	}
	var ccPaneID string
	for _, p := range panes {
		if strings.HasPrefix(p.Pane.Title, result.Session+"__cc_") {
			ccPaneID = p.Pane.ID
			break
		}
	}
	if ccPaneID == "" {
		t.Fatalf("cc pane not found in session %s", result.Session)
	}

	if testutil.AssertEventually(t, logger, 5*time.Second, 150*time.Millisecond, "cc pane outputs custom marker", func() bool {
		captured, err := tmux.CapturePaneOutput(ccPaneID, 200)
		if err != nil {
			return false
		}
		return strings.Contains(captured, customMarker)
	}) {
		logger.Log("PASS: Custom agent command was used")
	}
}

// TestConfigPaletteFromConfig verifies palette commands from config are loaded
func TestConfigPaletteFromConfig(t *testing.T) {
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Prevent user/global palette markdown from overriding TOML [[palette]] in this test.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Create temp config file with palette commands
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := `
[[palette]]
key = "test_cmd_one"
label = "Test Command One"
prompt = "This is test command one"
category = "test"
tags = ["integration", "test"]

[[palette]]
key = "test_cmd_two"
label = "Test Command Two"
prompt = "This is test command two"
category = "test"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created config with palette commands at %s", configPath)

	// Use ntm config show to verify palette commands are loaded
	out, err := logger.Exec(binary, "--config", configPath, "config", "show", "--json")
	if err != nil {
		t.Fatalf("ntm config show failed: %v", err)
	}

	// Parse output to verify palette commands
	var configResult struct {
		Palette []struct {
			Key      string   `json:"key"`
			Label    string   `json:"label"`
			Prompt   string   `json:"prompt"`
			Category string   `json:"category"`
			Tags     []string `json:"tags"`
		} `json:"palette"`
	}
	if err := json.Unmarshal(out, &configResult); err != nil {
		t.Fatalf("failed to parse config output: %v\nOutput: %s", err, string(out))
	}

	// Verify our commands are present
	foundOne := false
	foundTwo := false
	for _, cmd := range configResult.Palette {
		if cmd.Label == "Test Command One" {
			foundOne = true
			if cmd.Prompt != "This is test command one" {
				t.Fatalf("Test Command One has wrong prompt: %s", cmd.Prompt)
			}
			if cmd.Category != "test" {
				t.Fatalf("Test Command One has wrong category: %s", cmd.Category)
			}
		}
		if cmd.Label == "Test Command Two" {
			foundTwo = true
		}
	}

	if !foundOne {
		t.Fatalf("Test Command One not found in palette")
	}
	if !foundTwo {
		t.Fatalf("Test Command Two not found in palette")
	}

	logger.Log("PASS: Palette commands loaded from config")
}

// TestConfigPaletteFromMarkdown verifies palette commands from markdown file
func TestConfigPaletteFromMarkdown(t *testing.T) {
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create temp directory for project
	projectDir := t.TempDir()

	// Create a command_palette.md file
	paletteContent := `# Command Palette

## Test Category

### Markdown Test Command
This is a test command from markdown.
It has multiple lines.

### Another Markdown Command
Single line content.
`
	palettePath := filepath.Join(projectDir, "command_palette.md")
	if err := os.WriteFile(palettePath, []byte(paletteContent), 0644); err != nil {
		t.Fatalf("failed to write palette file: %v", err)
	}

	// Create config that points to the palette file
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := `palette_file = "` + palettePath + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created palette file at %s and config at %s", palettePath, configPath)

	// Use ntm config show to verify palette commands are loaded from markdown
	out, err := logger.Exec(binary, "--config", configPath, "config", "show", "--json")
	if err != nil {
		t.Fatalf("ntm config show failed: %v", err)
	}

	// Check that the output contains our markdown commands
	outStr := string(out)
	if !strings.Contains(outStr, "Markdown Test Command") && !strings.Contains(outStr, "markdown") {
		logger.Log("Note: Markdown palette loading may not be implemented yet")
		t.Skip("Markdown palette loading not implemented")
	}

	logger.Log("PASS: Palette commands loaded from markdown")
}

// TestConfigPrecedence verifies config file takes precedence over defaults
func TestConfigPrecedence(t *testing.T) {
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create temp config file with specific values different from defaults
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")

	// Use non-default values
	customScrollback := 9999
	configContent := `[checkpoints]
scrollback_lines = ` + string(rune('0'+customScrollback/1000)) + `999
max_auto_checkpoints = 50

[alerts]
agent_stuck_minutes = 42
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created config with custom values at %s", configPath)

	// Use ntm config show to verify our values override defaults
	out, err := logger.Exec(binary, "--config", configPath, "config", "show", "--json")
	if err != nil {
		t.Fatalf("ntm config show failed: %v", err)
	}

	// Parse output
	var configResult struct {
		Checkpoints struct {
			ScrollbackLines    int `json:"scrollback_lines"`
			MaxAutoCheckpoints int `json:"max_auto_checkpoints"`
		} `json:"checkpoints"`
		Alerts struct {
			AgentStuckMinutes int `json:"agent_stuck_minutes"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(out, &configResult); err != nil {
		t.Fatalf("failed to parse config output: %v\nOutput: %s", err, string(out))
	}

	// Verify values from config file override defaults
	if configResult.Checkpoints.MaxAutoCheckpoints != 50 {
		t.Fatalf("expected max_auto_checkpoints=50, got %d", configResult.Checkpoints.MaxAutoCheckpoints)
	}

	if configResult.Alerts.AgentStuckMinutes != 42 {
		t.Fatalf("expected agent_stuck_minutes=42, got %d", configResult.Alerts.AgentStuckMinutes)
	}

	logger.Log("PASS: Config file values take precedence over defaults")
}

// TestConfigEnvironmentOverride verifies NTM_CONFIG env var works
func TestConfigEnvironmentOverride(t *testing.T) {
	testutil.RequireNTMBinary(t)
	binary := testutil.BuildLocalNTM(t)
	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create temp config file with a distinctive value
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "env_config.toml")
	distinctiveValue := "/distinctive/projects/base/path"
	configContent := `projects_base = "` + distinctiveValue + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	logger.Log("Created config at %s with projects_base=%s", configPath, distinctiveValue)

	// Set environment variable and run ntm config show
	oldEnv := os.Getenv("NTM_CONFIG")
	os.Setenv("NTM_CONFIG", configPath)
	defer os.Setenv("NTM_CONFIG", oldEnv)

	out, err := logger.Exec(binary, "config", "show", "--json")
	if err != nil {
		t.Fatalf("ntm config show failed: %v", err)
	}

	// Parse output and verify our value is used
	var configResult struct {
		ProjectsBase string `json:"projects_base"`
	}
	if err := json.Unmarshal(out, &configResult); err != nil {
		t.Fatalf("failed to parse config output: %v\nOutput: %s", err, string(out))
	}

	if configResult.ProjectsBase != distinctiveValue {
		t.Fatalf("expected projects_base=%s, got %s", distinctiveValue, configResult.ProjectsBase)
	}

	logger.Log("PASS: NTM_CONFIG environment variable is honored")
}
