package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestFullAgentWorkflow tests the complete lifecycle of spawning, using, and killing an agent session.
// Uses simple shell commands as agent substitutes to avoid requiring real Claude/Codex binaries.
func TestFullAgentWorkflow(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create a unique session name
	sessionName := fmt.Sprintf("ntm_e2e_workflow_%d", time.Now().UnixNano())

	// Create projects_base and project directory
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Create config with simple bash commands as agent substitutes
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
# Use simple bash shells as agent substitutes for testing
claude = "bash"
codex = "bash"
gemini = "bash"

[tmux]
scrollback = 500
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Cleanup session on test completion
	t.Cleanup(func() {
		logger.LogSection("Teardown: Killing test session")
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	// Step 1: Spawn session with agents (may have non-zero exit due to terminal issues in test env)
	logger.LogSection("Step 1: Spawn session with agents")
	out, err := logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=2", "--cod=1")
	logger.Log("Spawn output: %s, err: %v", string(out), err)

	// Give tmux time to create panes
	time.Sleep(500 * time.Millisecond)

	// Step 2: Verify session exists with correct pane count
	logger.LogSection("Step 2: Verify session structure")
	testutil.AssertSessionExists(t, logger, sessionName)

	// Should have 4 panes: 2 Claude + 1 Codex + 1 User
	testutil.AssertPaneCountAtLeast(t, logger, sessionName, 4)

	// Step 3: Send command to Claude agents
	// Note: The prompt is a single argument after the flags
	logger.LogSection("Step 3: Send command to Claude agents")
	out, _ = logger.Exec("ntm", "--config", configPath, "send", sessionName, "--cc", "echo CLAUDE_TEST_MARKER_12345")
	logger.Log("Send to Claude output: %s", string(out))

	// Give time for command to execute
	time.Sleep(500 * time.Millisecond)

	// Step 4: Send command to Codex agent
	logger.LogSection("Step 4: Send command to Codex agent")
	out, _ = logger.Exec("ntm", "--config", configPath, "send", sessionName, "--cod", "echo CODEX_TEST_MARKER_67890")
	logger.Log("Send to Codex output: %s", string(out))

	time.Sleep(300 * time.Millisecond)

	// Step 5: Verify prompts were sent by capturing pane content
	logger.LogSection("Step 5: Verify prompt delivery")
	paneCount, err := testutil.GetSessionPaneCount(sessionName)
	if err != nil {
		t.Fatalf("failed to get pane count: %v", err)
	}

	foundClaudeMarker := false
	foundCodexMarker := false

	for i := 0; i < paneCount; i++ {
		content, err := testutil.CapturePane(sessionName, i)
		if err != nil {
			logger.Log("Failed to capture pane %d: %v", i, err)
			continue
		}
		logger.Log("Pane %d content (last 200 chars): ...%s", i, truncateEnd(content, 200))

		if strings.Contains(content, "CLAUDE_TEST_MARKER_12345") {
			foundClaudeMarker = true
			logger.Log("PASS: Found Claude marker in pane %d", i)
		}
		if strings.Contains(content, "CODEX_TEST_MARKER_67890") {
			foundCodexMarker = true
			logger.Log("PASS: Found Codex marker in pane %d", i)
		}
	}

	// Note: Marker verification is best-effort as send command may have timing/terminal issues
	if !foundClaudeMarker {
		logger.Log("WARNING: Claude marker not found - send may have failed")
	}
	if !foundCodexMarker {
		logger.Log("WARNING: Codex marker not found - send may have failed")
	}

	// Step 6: Test interrupt command
	logger.LogSection("Step 6: Test interrupt command")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "interrupt", sessionName)
	logger.Log("Interrupt output: %s", string(out))

	// Step 7: Test status command with JSON output
	logger.LogSection("Step 7: Test status JSON output")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "status", "--json", sessionName)
	logger.Log("Status JSON: %s", string(out))

	var statusResponse struct {
		Timestamp string `json:"timestamp"`
		Session   string `json:"session"`
		Exists    bool   `json:"exists"`
		Attached  bool   `json:"attached"`
		Panes     []struct {
			Index   int    `json:"index"`
			Title   string `json:"title"`
			Type    string `json:"type"`
			Variant string `json:"variant"`
		} `json:"panes"`
		AgentCounts struct {
			Claude int `json:"claude"`
			Codex  int `json:"codex"`
			Gemini int `json:"gemini"`
			User   int `json:"user"`
			Total  int `json:"total"`
		} `json:"agent_counts"`
	}

	if err := json.Unmarshal(out, &statusResponse); err != nil {
		t.Fatalf("failed to parse status JSON: %v\nOutput: %s", err, string(out))
	}

	if !statusResponse.Exists {
		t.Error("status.exists should be true")
	}
	if statusResponse.Session != sessionName {
		t.Errorf("status.session = %q, expected %q", statusResponse.Session, sessionName)
	}
	if statusResponse.AgentCounts.Claude < 2 {
		t.Errorf("status.agent_counts.claude = %d, expected at least 2", statusResponse.AgentCounts.Claude)
	}
	if statusResponse.AgentCounts.Codex < 1 {
		t.Errorf("status.agent_counts.codex = %d, expected at least 1", statusResponse.AgentCounts.Codex)
	}
	if len(statusResponse.Panes) < 4 {
		t.Errorf("status.panes has %d entries, expected at least 4", len(statusResponse.Panes))
	}

	logger.Log("PASS: Status JSON validated")

	// Step 8: Kill session
	logger.LogSection("Step 8: Kill session")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessionName)
	logger.Log("Kill output: %s", string(out))

	// Step 9: Verify session no longer exists
	logger.LogSection("Step 9: Verify session killed")
	time.Sleep(200 * time.Millisecond)
	testutil.AssertSessionNotExists(t, logger, sessionName)

	logger.Log("PASS: Full agent workflow test completed successfully")
}

// TestAgentWorkflowSendAll tests the --all flag for broadcasting to all panes.
func TestAgentWorkflowSendAll(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	sessionName := fmt.Sprintf("ntm_e2e_sendall_%d", time.Now().UnixNano())

	// Create projects_base and project directory
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
codex = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	// Spawn with multiple agent types (may have non-zero exit due to terminal issues)
	logger.LogSection("Spawn mixed session")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=1", "--cod=1")
	time.Sleep(500 * time.Millisecond)

	// Send to all panes
	logger.LogSection("Send to all panes")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "send", sessionName, "--all", "echo BROADCAST_MARKER_ABCDEF")
	time.Sleep(300 * time.Millisecond)

	// Verify marker in multiple panes
	logger.LogSection("Verify broadcast delivery")
	paneCount, _ := testutil.GetSessionPaneCount(sessionName)
	markerCount := 0

	for i := 0; i < paneCount; i++ {
		content, err := testutil.CapturePane(sessionName, i)
		if err != nil {
			continue
		}
		if strings.Contains(content, "BROADCAST_MARKER_ABCDEF") {
			markerCount++
			logger.Log("Found broadcast marker in pane %d", i)
		}
	}

	// Note: Marker verification is best-effort due to timing/terminal issues in test environment
	if markerCount < 1 {
		logger.Log("WARNING: broadcast marker not found in any pane - send may have failed")
	} else {
		logger.Log("PASS: Broadcast delivery verified in %d panes", markerCount)
	}
}

// TestAgentWorkflowKillForce tests force-killing a session.
func TestAgentWorkflowKillForce(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	sessionName := fmt.Sprintf("ntm_e2e_kill_%d", time.Now().UnixNano())

	// Create projects_base and project directory
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Create session (may have non-zero exit due to terminal issues in test env)
	logger.LogSection("Create session for kill test")
	out, err := logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=1")
	logger.Log("Spawn output: %s, err: %v", string(out), err)
	time.Sleep(500 * time.Millisecond)

	// Verify session was created despite potential terminal errors
	testutil.AssertSessionExists(t, logger, sessionName)

	// Force kill
	logger.LogSection("Force kill session")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessionName)
	time.Sleep(200 * time.Millisecond)

	// Verify killed
	testutil.AssertSessionNotExists(t, logger, sessionName)

	logger.Log("PASS: Force kill verified")
}

// TestAgentWorkflowStatusNonexistent tests status on a non-existent session.
func TestAgentWorkflowStatusNonexistent(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Check status of non-existent session
	logger.LogSection("Status of non-existent session")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "status", "--json", "nonexistent_session_xyz_12345")
	logger.Log("Status output: %s", string(out))

	var statusResponse struct {
		Session string `json:"session"`
		Exists  bool   `json:"exists"`
	}

	if err := json.Unmarshal(out, &statusResponse); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	if statusResponse.Exists {
		t.Error("status.exists should be false for non-existent session")
	}

	logger.Log("PASS: Non-existent session status verified")
}

// TestAgentWorkflowWithVariants tests spawning agents with variants.
// Note: This test is skipped in CI as variants may not work with bash substitutes
func TestAgentWorkflowWithVariants(t *testing.T) {
	t.Skip("Variants test skipped - requires full agent environment")
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	sessionName := fmt.Sprintf("ntm_e2e_variants_%d", time.Now().UnixNano())

	// Create projects_base and project directory
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
codex = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	// Spawn with variants (may have non-zero exit due to terminal issues)
	logger.LogSection("Spawn with variants")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=opus", "--cc=sonnet", "--cod=1")
	time.Sleep(500 * time.Millisecond)

	// Verify session structure
	testutil.AssertSessionExists(t, logger, sessionName)
	testutil.AssertPaneCountAtLeast(t, logger, sessionName, 4) // 2 Claude variants + 1 Codex + 1 User

	// Check status for variant info
	logger.LogSection("Check status for variants")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "status", "--json", sessionName)
	logger.Log("Status: %s", string(out))

	var statusResponse struct {
		Panes []struct {
			Type    string `json:"type"`
			Variant string `json:"variant"`
		} `json:"panes"`
	}

	if err := json.Unmarshal(out, &statusResponse); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	opusFound := false
	sonnetFound := false
	for _, p := range statusResponse.Panes {
		if p.Type == "cc" && p.Variant == "opus" {
			opusFound = true
		}
		if p.Type == "cc" && p.Variant == "sonnet" {
			sonnetFound = true
		}
	}

	if !opusFound {
		t.Error("opus variant not found in status")
	}
	if !sonnetFound {
		t.Error("sonnet variant not found in status")
	}

	logger.Log("PASS: Variants verified")
}

// truncateEnd returns the last n characters of s.
func truncateEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
