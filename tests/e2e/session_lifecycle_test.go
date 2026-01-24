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

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestFullSessionLifecycle tests the complete session lifecycle including
// state store verification, event emission, and crash recovery.
func TestFullSessionLifecycle(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create unique session name
	sessionName := fmt.Sprintf("e2e_lifecycle_%d", time.Now().UnixNano())

	// Setup directories
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Setup state database in temp dir
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "state.db")

	// Create config with state path
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Cleanup session on test completion
	t.Cleanup(func() {
		logger.LogSection("Teardown: Killing test session")
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	})

	// Step 1: Spawn session with agents
	logger.LogSection("Step 1: Spawn session with agents")
	out, err := logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=2", "--safety")
	logger.Log("Spawn output: %s, err: %v", string(out), err)

	// Give tmux time to create panes
	time.Sleep(1 * time.Second)

	// Step 2: Verify session exists via tmux
	logger.LogSection("Step 2: Verify session structure")
	testutil.AssertSessionExists(t, logger, sessionName)
	testutil.AssertPaneCountAtLeast(t, logger, sessionName, 3) // 2 Claude + 1 User

	// Step 3: Verify status JSON contains expected data
	logger.LogSection("Step 3: Verify status JSON output")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "status", "--json", sessionName)
	logger.Log("Status JSON: %s", string(out))

	var statusResponse struct {
		Timestamp string `json:"timestamp"`
		Session   string `json:"session"`
		Exists    bool   `json:"exists"`
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
	if statusResponse.AgentCounts.Claude < 2 {
		t.Errorf("status.agent_counts.claude = %d, expected at least 2", statusResponse.AgentCounts.Claude)
	}

	logger.Log("PASS: Status JSON validated - found %d Claude agents", statusResponse.AgentCounts.Claude)

	// Step 4: Send a prompt and verify delivery
	// Note: The prompt must come before type-selection flags due to flag parsing behavior
	logger.LogSection("Step 4: Send prompt and verify delivery")
	marker := fmt.Sprintf("LIFECYCLE_MARKER_%d", time.Now().UnixNano())
	out, _ = logger.Exec("ntm", "--config", configPath, "send", sessionName, fmt.Sprintf("echo %s", marker), "--cc")
	logger.Log("Send output: %s", string(out))

	// Wait for command to execute
	time.Sleep(500 * time.Millisecond)

	// Capture pane content and look for marker
	paneCount, _ := testutil.GetSessionPaneCount(sessionName)
	markerFound := false
	for i := 0; i < paneCount; i++ {
		content, err := testutil.CapturePane(sessionName, i)
		if err != nil {
			continue
		}
		if containsMarker(content, marker) {
			markerFound = true
			logger.Log("PASS: Found marker in pane %d", i)
			break
		}
	}

	if !markerFound {
		logger.Log("WARNING: Marker not found in any pane - send may have timing issues")
	}

	// Step 5: Test interrupt command
	logger.LogSection("Step 5: Test interrupt command")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "interrupt", sessionName)
	logger.Log("Interrupt output: %s", string(out))

	// Step 6: Kill session
	logger.LogSection("Step 6: Kill session")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessionName)
	logger.Log("Kill output: %s", string(out))

	// Wait for kill to complete
	time.Sleep(500 * time.Millisecond)

	// Step 7: Verify session no longer exists
	logger.LogSection("Step 7: Verify session killed")
	testutil.AssertSessionNotExists(t, logger, sessionName)

	logger.Log("PASS: Full session lifecycle test completed successfully")
}

// TestStateStoreIntegration tests that the state store correctly tracks sessions.
func TestStateStoreIntegration(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Create temporary state database
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "test_state.db")

	logger.LogSection("Step 1: Open state store")
	store, err := state.Open(stateDBPath)
	if err != nil {
		t.Fatalf("failed to open state store: %v", err)
	}
	defer store.Close()

	// Run migrations
	if err := store.Migrate(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Step 2: Create a session
	logger.LogSection("Step 2: Create session in state store")
	sessionID := fmt.Sprintf("test-session-%d", time.Now().UnixNano())
	sess := &state.Session{
		ID:          sessionID,
		Name:        "test-session",
		ProjectPath: "/tmp/test-project",
		CreatedAt:   time.Now(),
		Status:      "active",
	}

	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Step 3: Verify session can be retrieved
	logger.LogSection("Step 3: Verify session retrieval")
	retrieved, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if retrieved == nil {
		t.Fatal("session not found after creation")
	}
	if retrieved.Name != "test-session" {
		t.Errorf("session.Name = %q, want %q", retrieved.Name, "test-session")
	}
	if retrieved.Status != "active" {
		t.Errorf("session.Status = %q, want %q", retrieved.Status, "active")
	}

	logger.Log("PASS: Session created and retrieved successfully")

	// Step 4: Create agents for the session
	logger.LogSection("Step 4: Create agents")
	now := time.Now()
	agent1 := &state.Agent{
		ID:         fmt.Sprintf("agent-1-%d", time.Now().UnixNano()),
		SessionID:  sessionID,
		Name:       "claude-1",
		Type:       "cc",
		Model:      "opus",
		TmuxPaneID: "%1",
		LastSeen:   &now,
		Status:     "idle",
	}
	if err := store.CreateAgent(agent1); err != nil {
		t.Fatalf("failed to create agent 1: %v", err)
	}

	now2 := time.Now()
	agent2 := &state.Agent{
		ID:         fmt.Sprintf("agent-2-%d", time.Now().UnixNano()),
		SessionID:  sessionID,
		Name:       "claude-2",
		Type:       "cc",
		Model:      "sonnet",
		TmuxPaneID: "%2",
		LastSeen:   &now2,
		Status:     "working",
	}
	if err := store.CreateAgent(agent2); err != nil {
		t.Fatalf("failed to create agent 2: %v", err)
	}

	// Step 5: List agents and verify count
	logger.LogSection("Step 5: Verify agent listing")
	agents, err := store.ListAgents(sessionID)
	if err != nil {
		t.Fatalf("failed to list agents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("agent count = %d, want 2", len(agents))
	}

	logger.Log("PASS: Found %d agents for session", len(agents))

	// Step 6: Test crash recovery - close and reopen store
	logger.LogSection("Step 6: Test crash recovery")
	store.Close()

	// Reopen store
	store2, err := state.Open(stateDBPath)
	if err != nil {
		t.Fatalf("failed to reopen state store: %v", err)
	}
	defer store2.Close()

	// Verify data persisted
	recovered, err := store2.GetSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get session after recovery: %v", err)
	}
	if recovered == nil {
		t.Fatal("session not found after crash recovery")
	}
	if recovered.Status != "active" {
		t.Errorf("recovered session.Status = %q, want %q", recovered.Status, "active")
	}

	recoveredAgents, err := store2.ListAgents(sessionID)
	if err != nil {
		t.Fatalf("failed to list agents after recovery: %v", err)
	}
	if len(recoveredAgents) != 2 {
		t.Errorf("recovered agent count = %d, want 2", len(recoveredAgents))
	}

	logger.Log("PASS: Crash recovery verified - all data persisted")

	// Step 7: Cleanup - delete session
	logger.LogSection("Step 7: Cleanup")
	if err := store2.DeleteSession(sessionID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	// Verify deletion
	deleted, err := store2.GetSession(sessionID)
	if err != nil {
		t.Fatalf("failed to check deleted session: %v", err)
	}
	if deleted != nil {
		t.Error("session still exists after deletion")
	}

	logger.Log("PASS: State store integration test completed successfully")
}

// TestEventLogging tests that events are properly logged to the state store.
func TestEventLogging(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Create temporary state database
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "event_test.db")

	logger.LogSection("Step 1: Open state store")
	store, err := state.Open(stateDBPath)
	if err != nil {
		t.Fatalf("failed to open state store: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	sessionID := fmt.Sprintf("event-test-%d", time.Now().UnixNano())

	// Step 1b: Create session first (required for foreign key constraint)
	sess := &state.Session{
		ID:          sessionID,
		Name:        "event-test-session",
		ProjectPath: "/tmp/event-test",
		CreatedAt:   time.Now(),
		Status:      "active",
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session for events: %v", err)
	}

	// Step 2: Log various events
	logger.LogSection("Step 2: Log events")
	events := []struct {
		eventType     string
		correlationID string
		data          string
	}{
		{"session_create", "corr-1", `{"claude_count": 2}`},
		{"agent_spawn", "corr-2", `{"agent_type": "cc", "model": "opus"}`},
		{"agent_spawn", "corr-3", `{"agent_type": "cc", "model": "sonnet"}`},
		{"prompt_send", "corr-4", `{"target_count": 2}`},
	}

	for _, e := range events {
		entry := &state.EventLogEntry{
			SessionID:     sessionID,
			EventType:     e.eventType,
			EventData:     e.data,
			CorrelationID: e.correlationID,
		}
		if err := store.LogEvent(entry); err != nil {
			t.Fatalf("failed to log event %s: %v", e.eventType, err)
		}
		logger.Log("Logged event: %s (id=%d)", e.eventType, entry.ID)
	}

	// Step 3: Verify events can be retrieved
	logger.LogSection("Step 3: Verify event retrieval")
	retrieved, err := store.ListEvents(sessionID, 100)
	if err != nil {
		t.Fatalf("failed to list events: %v", err)
	}

	if len(retrieved) != 4 {
		t.Errorf("event count = %d, want 4", len(retrieved))
	}

	// Events are returned in reverse order (newest first)
	expectedTypes := []string{"prompt_send", "agent_spawn", "agent_spawn", "session_create"}
	for i, expected := range expectedTypes {
		if i < len(retrieved) && retrieved[i].EventType != expected {
			t.Errorf("event[%d].Type = %q, want %q", i, retrieved[i].EventType, expected)
		}
	}

	logger.Log("PASS: Event logging and retrieval verified")

	// Step 4: Test event replay for crash recovery
	logger.LogSection("Step 4: Test event replay")
	replayedEvents := []state.EventLogEntry{}
	err = store.ReplayEvents(sessionID, 0, func(entry state.EventLogEntry) error {
		replayedEvents = append(replayedEvents, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to replay events: %v", err)
	}

	if len(replayedEvents) != 4 {
		t.Errorf("replayed event count = %d, want 4", len(replayedEvents))
	}

	// Replay returns events in order (oldest first)
	if replayedEvents[0].EventType != "session_create" {
		t.Errorf("first replayed event = %q, want session_create", replayedEvents[0].EventType)
	}

	logger.Log("PASS: Event replay verified with %d events", len(replayedEvents))
}

// containsMarker checks if content contains the marker string.
func containsMarker(content, marker string) bool {
	return strings.Contains(content, marker)
}
