package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestStatusRealSession tests status command output with a real tmux session
func TestStatusRealSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-status")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = false // Test text output

	// Use simple command
	cfg.Agents.Claude = "cat" // Runs until killed or input closed

	sessionName := fmt.Sprintf("ntm-test-status-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-test"},
	}

	// Create project dir
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn session
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

	// Wait for session to settle
	time.Sleep(500 * time.Millisecond)

	// Run status and capture output
	var buf bytes.Buffer
	err = runStatus(&buf, sessionName, statusOptions{})
	if err != nil {
		t.Fatalf("runStatus failed: %v", err)
	}

	output := stripANSI(buf.String())

	// Verify output contains key info
	// Note: Full pane titles are truncated in the table display, so we verify the Claude
	// pane exists via the agent type indicator (C) and the Agents summary
	checks := []string{
		sessionName,
		"Panes",
		"Directory:",
		"Claude",
		"1 instance(s)",
		"C ", // Claude pane type indicator in the pane list
	}

	for _, check := range checks {
		if !regexp.MustCompile(regexp.QuoteMeta(check)).MatchString(output) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}
}
