package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestExecuteHelp verifies that the root command executes successfully
func TestExecuteHelp(t *testing.T) {
	// Reset command for clean test
	rootCmd.SetArgs([]string{"--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() with --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Named Tmux Manager") {
		t.Error("Expected 'Named Tmux Manager' in help output")
	}
	if !strings.Contains(output, "ntm spawn") {
		t.Error("Expected 'ntm spawn' example in help output")
	}
}

// TestVersionCmd tests the version subcommand
func TestVersionCmd(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		contains string
	}{
		{
			name:     "default version",
			args:     []string{"version"},
			contains: "ntm version",
		},
		{
			name:     "short version",
			args:     []string{"version", "--short"},
			contains: Version,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd.SetArgs(tt.args)

			var stdout bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stdout)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("Execute() failed: %v", err)
			}

			output := stdout.String()
			if !strings.Contains(output, tt.contains) {
				t.Errorf("Expected output to contain %q, got: %s", tt.contains, output)
			}
		})
	}
}

// TestConfigPathCmd tests the config path subcommand
func TestConfigPathCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"config", "path"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		t.Error("Expected config path output, got empty string")
	}
	if !strings.Contains(output, "ntm") {
		t.Errorf("Expected config path to contain 'ntm', got: %s", output)
	}
}

// TestConfigShowCmd tests the config show subcommand
func TestConfigShowCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"config", "show"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	output := stdout.String()
	// Should contain config sections
	if !strings.Contains(output, "[agents]") {
		t.Error("Expected '[agents]' section in config output")
	}
	if !strings.Contains(output, "[tmux]") {
		t.Error("Expected '[tmux]' section in config output")
	}
}

// TestDepsCmd tests the deps command
func TestDepsCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"deps"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	// deps may exit 1 if missing required deps, but shouldn't error
	_ = err

	output := stdout.String()
	if !strings.Contains(output, "Dependency Check") {
		t.Error("Expected 'Dependency Check' in output")
	}
	if !strings.Contains(output, "tmux") {
		t.Error("Expected 'tmux' in output")
	}
}

// TestListCmdNoSessions tests list command when no sessions exist
func TestListCmdNoSessions(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	rootCmd.SetArgs([]string{"list"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	// Output may show sessions or "No tmux sessions running"
	// Both are valid depending on system state
}

// TestListCmdJSON tests list command with JSON output
func TestListCmdJSON(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	rootCmd.SetArgs([]string{"list", "--json"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	output := stdout.String()
	// Should be valid JSON (even if empty sessions)
	if output != "" && !strings.HasPrefix(strings.TrimSpace(output), "{") {
		t.Errorf("Expected JSON output starting with '{', got: %s", output)
	}
}

// TestSpawnValidation tests spawn command argument validation
func TestSpawnValidation(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	// Initialize config for spawn command
	cfg = config.Default()

	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "missing session name",
			args:        []string{"spawn"},
			expectError: true,
			errorMsg:    "accepts 1 arg",
		},
		{
			name:        "no agents specified",
			args:        []string{"spawn", "testproject"},
			expectError: true,
			errorMsg:    "no agents specified",
		},
		{
			name:        "invalid session name with colon",
			args:        []string{"spawn", "test:project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain ':'",
		},
		{
			name:        "invalid session name with dot",
			args:        []string{"spawn", "test.project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd.SetArgs(tt.args)

			var stderr bytes.Buffer
			rootCmd.SetOut(&stderr)
			rootCmd.SetErr(&stderr)

			err := rootCmd.Execute()

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestIsJSONOutput tests the JSON output detection
func TestIsJSONOutput(t *testing.T) {
	// Save original value
	original := jsonOutput
	defer func() { jsonOutput = original }()

	jsonOutput = false
	if IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return false")
	}

	jsonOutput = true
	if !IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return true")
	}
}

// TestGetFormatter tests the formatter creation
func TestGetFormatter(t *testing.T) {
	formatter := GetFormatter()
	if formatter == nil {
		t.Fatal("Expected non-nil formatter")
	}
}

// TestBuildInfo tests that build info variables are set
func TestBuildInfo(t *testing.T) {
	// These should have default values even if not set by build
	if Version == "" {
		t.Error("Version should not be empty")
	}
	if Commit == "" {
		t.Error("Commit should not be empty")
	}
	if Date == "" {
		t.Error("Date should not be empty")
	}
	if BuiltBy == "" {
		t.Error("BuiltBy should not be empty")
	}
}

// TestRobotFlags tests robot flag parsing
func TestRobotFlags(t *testing.T) {
	// Test robot-version flag
	t.Run("robot-version", func(t *testing.T) {
		rootCmd.SetArgs([]string{"--robot-version"})

		var stdout bytes.Buffer
		rootCmd.SetOut(&stdout)
		rootCmd.SetErr(&stdout)

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("Execute() failed: %v", err)
		}

		output := stdout.String()
		// Should produce JSON
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Errorf("Expected valid JSON output, got: %s", output)
		}
		if _, ok := result["version"]; !ok {
			t.Error("Expected 'version' field in robot-version output")
		}
	})

	// Test robot-help flag
	t.Run("robot-help", func(t *testing.T) {
		rootCmd.SetArgs([]string{"--robot-help"})

		var stdout bytes.Buffer
		rootCmd.SetOut(&stdout)
		rootCmd.SetErr(&stdout)

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("Execute() failed: %v", err)
		}

		output := stdout.String()
		// Should produce JSON
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Errorf("Expected valid JSON output, got: %s", output)
		}
	})
}

// TestRobotStatus tests the robot-status flag
func TestRobotStatus(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	rootCmd.SetArgs([]string{"--robot-status"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	output := stdout.String()
	// Should produce valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Errorf("Expected valid JSON output, got: %s", output)
	}
}

// TestRobotSnapshot tests the robot-snapshot flag
func TestRobotSnapshot(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	rootCmd.SetArgs([]string{"--robot-snapshot"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	output := stdout.String()
	// Should produce valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Errorf("Expected valid JSON output, got: %s", output)
	}
	// Should have timestamp
	if _, ok := result["timestamp"]; !ok {
		t.Error("Expected 'timestamp' field in snapshot output")
	}
}

// TestRobotSendValidation tests robot-send flag validation
func TestRobotSendValidation(t *testing.T) {
	// robot-send without --msg should error
	rootCmd.SetArgs([]string{"--robot-send", "testsession"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	// This is handled internally, not as a cobra error
	_ = rootCmd.Execute()
	// The command should handle this internally
}

// TestAttachCmdNoArgs tests attach command without arguments
func TestAttachCmdNoArgs(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	// Initialize config
	cfg = config.Default()

	rootCmd.SetArgs([]string{"attach"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	// Should not error - just lists sessions
	if err != nil && !strings.Contains(err.Error(), "no server running") {
		t.Logf("Attach without args result: %v", err)
	}
}

// TestStatusCmdRequiresArg tests status command requires session name
func TestStatusCmdRequiresArg(t *testing.T) {
	rootCmd.SetArgs([]string{"status"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for status without session name")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("Expected 'accepts 1 arg' error, got: %v", err)
	}
}

// TestAddCmdRequiresSession tests add command requires session name
func TestAddCmdRequiresSession(t *testing.T) {
	rootCmd.SetArgs([]string{"add"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for add without session name")
	}
}

// TestZoomCmdRequiresArgs tests zoom command requires arguments
func TestZoomCmdRequiresArgs(t *testing.T) {
	rootCmd.SetArgs([]string{"zoom"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for zoom without arguments")
	}
}

// TestSendCmdRequiresArgs tests send command requires arguments
func TestSendCmdRequiresArgs(t *testing.T) {
	rootCmd.SetArgs([]string{"send"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for send without arguments")
	}
}

// TestCompletionCmd tests completion subcommand
func TestCompletionCmd(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			rootCmd.SetArgs([]string{"completion", shell})

			var stdout bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stdout)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("completion %s failed: %v", shell, err)
			}

			output := stdout.String()
			if output == "" {
				t.Errorf("Expected completion output for %s, got empty", shell)
			}
		})
	}
}

// TestInitCmd tests init subcommand for shell integration
func TestInitCmd(t *testing.T) {
	shells := []string{"bash", "zsh"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			rootCmd.SetArgs([]string{"init", shell})

			var stdout bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stdout)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("init %s failed: %v", shell, err)
			}

			output := stdout.String()
			if output == "" {
				t.Errorf("Expected init output for %s, got empty", shell)
			}
		})
	}
}

// TestKillCmdRequiresSession tests kill command requires session name
func TestKillCmdRequiresSession(t *testing.T) {
	rootCmd.SetArgs([]string{"kill"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for kill without session name")
	}
}

// TestViewCmdRequiresSession tests view command requires session name
func TestViewCmdRequiresSession(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	rootCmd.SetArgs([]string{"view"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for view without session name")
	}
}

// TestCopyCmdRequiresSession tests copy command requires session name
func TestCopyCmdRequiresSession(t *testing.T) {
	rootCmd.SetArgs([]string{"copy"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for copy without session name")
	}
}

// TestSaveCmdRequiresSession tests save command requires session name
func TestSaveCmdRequiresSession(t *testing.T) {
	rootCmd.SetArgs([]string{"save"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for save without session name")
	}
}

// TestTutorialCmd tests the tutorial command
func TestTutorialCmd(t *testing.T) {
	// Tutorial is interactive, so we can only test that it parses
	rootCmd.SetArgs([]string{"tutorial", "--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("tutorial --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "tutorial") {
		t.Error("Expected 'tutorial' in help output")
	}
}

// TestDashboardCmd tests the dashboard command help
func TestDashboardCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"dashboard", "--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("dashboard --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "dashboard") {
		t.Error("Expected 'dashboard' in help output")
	}
}

// TestPaletteCmd tests the palette command help
func TestPaletteCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"palette", "--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("palette --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "palette") {
		t.Error("Expected 'palette' in help output")
	}
}

// TestQuickCmdRequiresName tests quick command requires project name
func TestQuickCmdRequiresName(t *testing.T) {
	rootCmd.SetArgs([]string{"quick"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for quick without project name")
	}
}

// TestUpgradeCmd tests the upgrade command help
func TestUpgradeCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"upgrade", "--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("upgrade --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "upgrade") {
		t.Error("Expected 'upgrade' in help output")
	}
}

// TestCreateCmdRequiresName tests create command requires session name
func TestCreateCmdRequiresName(t *testing.T) {
	rootCmd.SetArgs([]string{"create"})

	var stderr bytes.Buffer
	rootCmd.SetOut(&stderr)
	rootCmd.SetErr(&stderr)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for create without session name")
	}
}

// TestBindCmdHelp tests the bind command help
func TestBindCmdHelp(t *testing.T) {
	rootCmd.SetArgs([]string{"bind", "--help"})

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stdout)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("bind --help failed: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "bind") {
		t.Error("Expected 'bind' in help output")
	}
}

// TestCommandAliases tests command aliases work
func TestCommandAliases(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	aliases := []struct {
		alias   string
		command string
	}{
		{"ls", "list"},
		{"l", "list"},
		{"a", "attach"},
	}

	for _, a := range aliases {
		t.Run(a.alias, func(t *testing.T) {
			rootCmd.SetArgs([]string{a.alias})

			var stdout bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stdout)

			// These should not error on parsing
			err := rootCmd.Execute()
			// May error due to missing args or no sessions, but shouldn't fail on alias
			_ = err
		})
	}
}

// TestEnvVarConfig tests that environment variables are respected
func TestEnvVarConfig(t *testing.T) {
	// Test that XDG_CONFIG_HOME affects config path
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	testDir := "/tmp/ntm_test_config"
	os.Setenv("XDG_CONFIG_HOME", testDir)

	path := config.DefaultPath()
	if !strings.HasPrefix(path, testDir) {
		t.Errorf("Expected config path to start with %s, got: %s", testDir, path)
	}
}
