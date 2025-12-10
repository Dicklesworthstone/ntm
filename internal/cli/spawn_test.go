package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSpawnSessionLogic(t *testing.T) {
	// Skip if tmux is not installed (Epic says "Tests requiring tmux must be skipped in CI without tmux")
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() { 
		cfg = oldCfg 
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries
	cfg.Agents.Claude = "echo 'Claude started'; sleep 10"
	cfg.Agents.Codex = "echo 'Codex started'; sleep 10"
	cfg.Agents.Gemini = "echo 'Gemini started'; sleep 10"

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-3-5-sonnet-20241022"},
	}

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Execute spawn
	// spawnSessionLogic(session, agents, ccCount, codCount, gmiCount, userPane, autoRestart, recipeName, personaMap)
	err = spawnSessionLogic(sessionName, agents, 1, 0, 0, true, false, "", nil)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Validate session exists
	if !tmux.SessionExists(sessionName) {
		t.Errorf("session %s was not created", sessionName)
	}

	// Validate panes
	// Expected: 1 user pane + 1 claude pane = 2 panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	if len(panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(panes))
	}

	// Validate user pane and agent pane
	foundClaude := false
	for _, p := range panes {
		if p.Type == tmux.AgentClaude {
			foundClaude = true
			// Check title format: session__type_index_variant
			expectedTitle := fmt.Sprintf("%s__cc_1_claude-3-5-sonnet-20241022", sessionName)
			if p.Title != expectedTitle {
				t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
			}
		}
	}

	if !foundClaude {
		t.Error("did not find Claude agent pane")
	}

	// Verify project directory creation
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Errorf("project directory %s was not created", projectDir)
	}
}
