package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestNormalizeAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"cc", "claude"},
		{"CC", "claude"},
		{"claude", "claude"},
		{"Claude", "claude"},
		{"claude-code", "claude"},
		{"cod", "codex"},
		{"codex", "codex"},
		{"openai-codex", "codex"},
		{"gmi", "gemini"},
		{"gemini", "gemini"},
		{"google-gemini", "gemini"},
		{"unknown", "unknown"},
		{"aider", "aider"},
		{"  cc  ", "claude"}, // whitespace handling
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentType(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeAgentType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestRespawnRequiresSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Test respawning a non-existent session should fail
	err := runRespawn("nonexistent-session-12345", true, "", "", false, false)
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestRespawnDryRun(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.Agents.Claude = "sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}

	err = spawnSessionLogic(opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test dry-run mode (should not error and not actually restart)
	err = runRespawn(sessionName, true, "", "", false, true)
	if err != nil {
		t.Errorf("dry-run respawn failed: %v", err)
	}
}

func TestRespawnWithPaneFilter(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn-filter")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.Agents.Claude = "sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-filter-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session with 2 agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
		{Type: AgentTypeClaude, Index: 2},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  2,
		UserPane: true,
	}

	err = spawnSessionLogic(opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test respawning specific pane with force flag
	err = runRespawn(sessionName, true, "1", "", false, false)
	if err != nil {
		t.Errorf("respawn with pane filter failed: %v", err)
	}
}
