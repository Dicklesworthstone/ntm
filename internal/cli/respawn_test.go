package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		{"claude_code", "claude"},
		{"claude-code", "claude"},
		{"cod", "codex"},
		{"codex", "codex"},
		{"openai-codex", "codex"},
		{"codex-cli", "codex"},
		{"codex_cli", "codex"},
		{"gmi", "gemini"},
		{"gemini", "gemini"},
		{"google-gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"gemini_cli", "gemini"},
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

func TestRespawnPaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom status title",
		Type:    tmux.AgentCodex,
		Command: "codex --unsafe-mode",
	}

	if got := respawnPaneAgentType(pane); got != "codex" {
		t.Fatalf("respawnPaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestSelectRespawnTargetsUsesParsedPaneTypeForFilters(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "build monitor", Type: tmux.AgentClaude, Command: "claude"},
		{ID: "%2", Index: 2, Title: "notes", Type: tmux.AgentCodex, Command: "codex"},
	}

	targets := selectRespawnTargets(panes, nil, "codex", false)
	if len(targets) != 1 {
		t.Fatalf("selectRespawnTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%2" {
		t.Fatalf("selectRespawnTargets() picked %s, want %%2", targets[0].ID)
	}
}

func TestSelectRespawnTargetsSkipsUserPaneByDefault(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "agent output", Type: tmux.AgentClaude, Command: "claude"},
	}

	targets := selectRespawnTargets(panes, nil, "", false)
	if len(targets) != 1 {
		t.Fatalf("selectRespawnTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%1" {
		t.Fatalf("selectRespawnTargets() picked %s, want %%1", targets[0].ID)
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
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
