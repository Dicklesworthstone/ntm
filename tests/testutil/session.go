package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AgentConfig specifies the number of agents of each type to spawn.
type AgentConfig struct {
	Claude int // Number of Claude Code agents
	Codex  int // Number of Codex agents
	Gemini int // Number of Gemini agents
}

// SessionConfig holds configuration for creating a test session.
type SessionConfig struct {
	Agents    AgentConfig
	WorkDir   string   // Working directory for the session (used as NTM_PROJECTS_BASE)
	ExtraArgs []string // Additional arguments to pass to ntm spawn
}

// CreateTestSession creates a new ntm session for testing.
// It automatically registers cleanup to kill the session when the test completes.
// Returns the session name.
//
// Note: ntm spawn derives the project directory from NTM_PROJECTS_BASE + session name.
// This function sets NTM_PROJECTS_BASE to a temp directory and creates the project
// directory within it, named after the session.
func CreateTestSession(t *testing.T, logger *TestLogger, config SessionConfig) string {
	t.Helper()
	if !isolatedTmuxReady() {
		t.Fatal("tmux-backed test refused: call SetupIsolatedTmuxTestProcess from TestMain")
	}

	// Generate unique session/project name
	name := fmt.Sprintf("ntm_test_%d", time.Now().UnixNano())
	logger.LogSection("Creating Test Session")
	logger.Log("Session name: %s", name)

	// Set up the projects base directory
	// NTM uses NTM_PROJECTS_BASE + session_name as the project directory
	projectsBase := config.WorkDir
	if projectsBase == "" {
		projectsBase = t.TempDir()
	}

	// Create the actual project directory (session name = project name)
	projectDir := filepath.Join(projectsBase, name)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory %s: %v", projectDir, err)
	}
	logger.Log("Project directory: %s", projectDir)

	logger.Log("NTM_PROJECTS_BASE: %s", projectsBase)

	// Build spawn command arguments
	// Use --json flag to avoid interactive prompts and terminal attachment
	args := []string{"spawn", name, "--json"}
	if config.Agents.Claude > 0 {
		args = append(args, fmt.Sprintf("--cc=%d", config.Agents.Claude))
	}
	if config.Agents.Codex > 0 {
		args = append(args, fmt.Sprintf("--cod=%d", config.Agents.Codex))
	}
	if config.Agents.Gemini > 0 {
		args = append(args, fmt.Sprintf("--gmi=%d", config.Agents.Gemini))
	}
	args = append(args, config.ExtraArgs...)

	// Create command with NTM_PROJECTS_BASE properly set
	// We need to filter out any existing NTM_PROJECTS_BASE and add our own
	cmd := exec.Command("ntm", args...)
	env := filterEnv(os.Environ(), "NTM_PROJECTS_BASE")
	env = append(env, "NTM_PROJECTS_BASE="+projectsBase)
	cmd.Env = env

	// Create the session
	out, err := cmd.CombinedOutput()
	logger.Log("EXEC: ntm %s", strings.Join(args, " "))
	logger.Log("OUTPUT: %s", string(out))
	if err != nil {
		logger.Log("EXIT: error: %v", err)
		t.Fatalf("failed to create session %s: %v\nOutput: %s", name, err, string(out))
	}
	logger.Log("EXIT: success")

	// Register cleanup to kill the session
	t.Cleanup(func() {
		logger.LogSection("Teardown")
		logger.Log("Killing session %s", name)
		killSession(logger, name)
	})

	// Wait a moment for session to initialize
	time.Sleep(500 * time.Millisecond)

	logger.Log("Session %s created successfully", name)
	return name
}

// CreateTestSessionSimple creates a session with a simple agent configuration.
// This is a convenience wrapper around CreateTestSession.
func CreateTestSessionSimple(t *testing.T, logger *TestLogger, agents map[string]int) string {
	t.Helper()

	config := SessionConfig{}
	for agentType, count := range agents {
		switch strings.ToLower(agentType) {
		case "claude", "cc":
			config.Agents.Claude = count
		case "codex", "cod":
			config.Agents.Codex = count
		case "gemini", "gem":
			config.Agents.Gemini = count
		}
	}

	return CreateTestSession(t, logger, config)
}

// killSession forcefully kills an ntm session.
func killSession(logger *TestLogger, name string) {
	// Try ntm kill first
	out, err := exec.Command("ntm", "kill", "-f", name).CombinedOutput()
	if err != nil {
		logger.Log("ntm kill failed: %v, output: %s", err, string(out))
		// Fallback to tmux kill-session
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", name).Run()
	} else {
		logger.Log("Session %s killed successfully", name)
	}
}

// KillAllTestSessions kills all ntm test sessions.
// Matches both naming conventions: "ntm_test_*" (underscore) and "ntm-test-*" (hyphen).
// Useful for cleanup in TestMain or after failed tests.
func KillAllTestSessions(logger *TestLogger) {
	if !isolatedTmuxReady() {
		logger.Log("Refusing tmux cleanup without an isolated test server")
		return
	}
	logger.LogSection("Killing All Test Sessions")

	withGlobalTmuxTestLock(func() {
		// List all tmux sessions
		out, err := exec.Command(tmux.BinaryPath(), "list-sessions", "-F", "#{session_name}").Output()
		if err != nil {
			logger.Log("Failed to list tmux sessions: %v", err)
			return
		}

		sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
		killed := 0
		for _, session := range sessions {
			// Match both naming conventions used in tests
			if strings.HasPrefix(session, "ntm_test_") || strings.HasPrefix(session, "ntm-test-") {
				logger.Log("Killing orphan test session: %s", session)
				exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
				killed++
			}
		}

		logger.Log("Killed %d orphan test sessions", killed)
	})
}

// KillAllTestSessionsSilent kills all ntm test sessions without logging.
// Use this in TestMain where a logger may not be available. The cleanup is
// best-effort and intentionally avoids blocking on the global tmux test lock so
// concurrent package startup can continue under multi-agent test runs.
func KillAllTestSessionsSilent() int {
	if !isolatedTmuxReady() {
		return 0
	}
	killed := 0
	if !tryWithGlobalTmuxTestLock(func() {
		out, err := exec.Command(tmux.BinaryPath(), "list-sessions", "-F", "#{session_name}").Output()
		if err != nil {
			return
		}

		sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, session := range sessions {
			if strings.HasPrefix(session, "ntm_test_") || strings.HasPrefix(session, "ntm-test-") {
				exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
				killed++
			}
		}
	}) {
		return 0
	}
	return killed
}

// SessionExists checks if a tmux session exists.
func SessionExists(name string) bool {
	err := exec.Command(tmux.BinaryPath(), "has-session", "-t", name).Run()
	return err == nil
}

// GetSessionPaneCount returns the number of panes in a session.
func GetSessionPaneCount(name string) (int, error) {
	out, err := exec.Command(tmux.BinaryPath(), "list-panes", "-t", name, "-F", "#{pane_id}").Output()
	if err != nil {
		return 0, err
	}
	panes := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(panes) == 1 && panes[0] == "" {
		return 0, nil
	}
	return len(panes), nil
}

// WaitForSession waits for a session to exist, with timeout.
func WaitForSession(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if SessionExists(name) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("session %s did not appear within %s", name, timeout)
}

// CapturePane captures the visible content of a pane.
func CapturePane(name string, paneIndex int) (string, error) {
	target := fmt.Sprintf("%s:%d", name, paneIndex)
	ctx, cancel := context.WithTimeout(context.Background(), tmux.DefaultCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture pane %s: %w", target, err)
	}
	return string(out), nil
}

// filterEnv filters out environment variables with the given prefix.
func filterEnv(env []string, prefix string) []string {
	result := make([]string, 0, len(env))
	prefix = prefix + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}

// CreateNTMConfig creates a temporary NTM config file pointing to the given project directory.
func CreateNTMConfig(t *testing.T, projectDir string, logger *TestLogger) string {
	t.Helper()
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "echo claude"
codex = "echo codex"
gemini = "echo gemini"
`, filepath.Dir(projectDir))

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	logger.Log("Created config file at %s", configPath)
	return configPath
}
