package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestCLISpawnSendAndStatus verifies that ntm CLI commands drive tmux correctly:
// - spawn creates a tmux session
// - we can add synthetic agent panes
// - send targets those agent panes
// - status reports the expected pane count.
func TestCLISpawnSendAndStatus(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Use a temp config that stubs agent binaries to /bin/true to avoid external dependencies.
	projectsBase := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := fmt.Sprintf(`projects_base = "%s"

[agents]
claude = "/bin/true"
codex = "/bin/true"
gemini = "/bin/true"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContents), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	// Make config available to subsequent ntm commands.
	t.Setenv("NTM_PROJECTS_BASE", projectsBase)

	// Create a session with a single stubbed claude agent.
	session := testutil.CreateTestSession(t, logger, testutil.SessionConfig{
		Agents: testutil.AgentConfig{
			Claude: 1,
		},
		WorkDir:   projectsBase,
		ExtraArgs: []string{"--config", configPath},
	})
	testutil.AssertSessionExists(t, logger, session)

	// Discover panes and locate the spawned cc pane.
	panes, err := tmux.GetPanesWithActivity(session)
	if err != nil {
		t.Fatalf("failed to list panes: %v", err)
	}
	var ccPaneID string
	for _, p := range panes {
		if strings.HasPrefix(p.Pane.Title, session+"__cc_") {
			ccPaneID = p.Pane.ID
			break
		}
	}
	if ccPaneID == "" {
		t.Fatalf("cc pane not found in session %s", session)
	}

	// status should see user + cc = 2 panes.
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "status", "--json", "--config", configPath, session)
	var status struct {
		Panes []struct {
			Type string `json:"type"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}
	if len(status.Panes) != 2 {
		t.Fatalf("expected 2 panes (user + cc), got %d", len(status.Panes))
	}

	// Send a command to cc panes and verify it lands.
	// Use --no-cass-check to skip CASS duplicate detection (may not be available in all test environments).
	// Use --no-hooks to skip hook loading (may not be configured in test environments).
	// Note: Use "--cc=" (with empty value) to ensure the prompt is parsed as positional argument,
	// not as the value for --cc. With IsBoolFlag=true, "--cc value" is ambiguous.
	const marker = "INTEGRATION_CC_OK"
	testutil.AssertCommandSuccess(t, logger, "ntm", "send", "--config", configPath, "--no-cass-check", "--no-hooks", session, "--cc=", "echo "+marker)

	testutil.AssertEventually(t, logger, 5*time.Second, 150*time.Millisecond, "cc pane receives send payload", func() bool {
		out, err := tmux.CapturePaneOutput(ccPaneID, 200)
		if err != nil {
			return false
		}
		return strings.Contains(out, marker)
	})
}

// TestCLIAddCommand verifies that ntm add correctly adds new panes to an existing session.
func TestCLIAddCommand(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create config with stubbed agent binaries
	projectsBase := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := fmt.Sprintf(`projects_base = "%s"

[agents]
claude = "/bin/true"
codex = "/bin/true"
gemini = "/bin/true"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContents), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	t.Setenv("NTM_PROJECTS_BASE", projectsBase)

	// Create session with 1 Claude agent (user + 1 cc = 2 panes)
	session := testutil.CreateTestSession(t, logger, testutil.SessionConfig{
		Agents: testutil.AgentConfig{
			Claude: 1,
		},
		WorkDir:   projectsBase,
		ExtraArgs: []string{"--config", configPath},
	})
	testutil.AssertSessionExists(t, logger, session)

	// Verify initial pane count (user + cc = 2)
	initialCount, err := testutil.GetSessionPaneCount(session)
	if err != nil {
		t.Fatalf("failed to get initial pane count: %v", err)
	}
	if initialCount != 2 {
		t.Fatalf("expected 2 initial panes (user + cc), got %d", initialCount)
	}
	logger.Log("Initial pane count: %d", initialCount)

	// Add another Claude agent
	logger.LogSection("Adding Claude Agent")
	testutil.AssertCommandSuccess(t, logger, "ntm", "add", "--config", configPath, session, "--cc=1")

	// Wait for pane to be added
	time.Sleep(500 * time.Millisecond)

	// Verify pane count increased to 3 (user + 2 cc)
	testutil.AssertPaneCount(t, logger, session, 3)

	// Add a Codex agent
	logger.LogSection("Adding Codex Agent")
	testutil.AssertCommandSuccess(t, logger, "ntm", "add", "--config", configPath, session, "--cod=1")

	// Wait for pane to be added
	time.Sleep(500 * time.Millisecond)

	// Verify pane count is now 4 (user + 2 cc + 1 cod)
	testutil.AssertPaneCount(t, logger, session, 4)
}

// TestCLIKillCommand verifies that ntm kill correctly destroys a session.
func TestCLIKillCommand(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create config with stubbed agent binaries
	projectsBase := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := fmt.Sprintf(`projects_base = "%s"

[agents]
claude = "/bin/true"
codex = "/bin/true"
gemini = "/bin/true"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContents), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	t.Setenv("NTM_PROJECTS_BASE", projectsBase)

	// Create a unique session name for this test to avoid cleanup conflicts
	sessionName := fmt.Sprintf("ntm_kill_test_%d", time.Now().UnixNano())
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Spawn the session directly (not using CreateTestSession to avoid auto-cleanup)
	logger.LogSection("Creating Session for Kill Test")
	testutil.AssertCommandSuccess(t, logger, "ntm", "spawn", sessionName, "--json", "--cc=1", "--config", configPath)

	// Wait for session to initialize
	time.Sleep(500 * time.Millisecond)

	// Verify session exists
	testutil.AssertSessionExists(t, logger, sessionName)

	// Kill the session
	logger.LogSection("Killing Session")
	testutil.AssertCommandSuccess(t, logger, "ntm", "kill", "-f", sessionName)

	// Wait for session to be destroyed
	time.Sleep(300 * time.Millisecond)

	// Verify session no longer exists
	testutil.AssertSessionNotExists(t, logger, sessionName)
}
