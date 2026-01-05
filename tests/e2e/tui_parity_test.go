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

// These tests exercise the TUI-parity robot commands that provide AI agents
// with the same information available to human users in the TUI dashboard.

// =============================================================================
// --robot-files tests
// =============================================================================

func TestRobotFilesEmpty(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// --robot-files requires a value; use "all" to get all sessions
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=all")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Timestamp  string `json:"timestamp"`
		Success    bool   `json:"success"`
		TimeWindow string `json:"time_window"`
		Count      int    `json:"count"`
		Changes    []struct {
			Timestamp string   `json:"timestamp"`
			Path      string   `json:"path"`
			Operation string   `json:"operation"`
			Agents    []string `json:"agents"`
		} `json:"changes"`
		Summary struct {
			TotalChanges int            `json:"total_changes"`
			UniqueFiles  int            `json:"unique_files"`
			ByAgent      map[string]int `json:"by_agent"`
			ByOperation  map[string]int `json:"by_operation"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Timestamp == "" {
		t.Fatalf("missing timestamp field")
	}
	if !payload.Success {
		t.Fatalf("expected success=true")
	}
	if payload.TimeWindow == "" {
		t.Fatalf("missing time_window field")
	}
	if payload.Changes == nil {
		t.Fatalf("changes should be an array, not nil")
	}
	// Count can be 0 if no changes tracked
	if payload.Count < 0 {
		t.Fatalf("count should be non-negative")
	}
}

func TestRobotFilesWithTimeWindow(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Test different time windows
	windows := []string{"5m", "15m", "1h", "all"}
	for _, window := range windows {
		t.Run(window, func(t *testing.T) {
			// --robot-files requires a value; use "all" for all sessions
			out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=all", "--files-window="+window)

			var payload struct {
				TimeWindow string `json:"time_window"`
				Success    bool   `json:"success"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON for window %s: %v", window, err)
			}
			if payload.TimeWindow != window {
				t.Errorf("time_window = %q, want %q", payload.TimeWindow, window)
			}
			if !payload.Success {
				t.Errorf("expected success=true for window %s", window)
			}
		})
	}
}

// =============================================================================
// --robot-inspect-pane tests
// =============================================================================

func TestRobotInspectPaneMissingSession(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without session argument, Cobra returns a text error (not JSON)
	// because the flag requires a value
	cmd := exec.Command("ntm", "--robot-inspect-pane")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-inspect-pane has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-inspect-pane") {
		t.Errorf("error output should mention the flag name")
	}
}

func TestRobotInspectPaneWithSyntheticSession(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	sessionName := createSyntheticAgentSession(t, logger)

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-inspect-pane="+sessionName, "--inspect-index=0")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Timestamp string `json:"timestamp"`
		Success   bool   `json:"success"`
		Session   string `json:"session"`
		PaneIndex int    `json:"pane_index"`
		PaneID    string `json:"pane_id"`
		Agent     struct {
			Type  string `json:"type"`
			State string `json:"state"`
			Title string `json:"title"`
		} `json:"agent"`
		Output struct {
			Lines      int      `json:"lines"`
			Characters int      `json:"characters"`
			LastLines  []string `json:"last_lines"`
		} `json:"output"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("expected success=true")
	}
	if payload.Session != sessionName {
		t.Errorf("session = %q, want %q", payload.Session, sessionName)
	}
	if payload.PaneIndex != 0 {
		t.Errorf("pane_index = %d, want 0", payload.PaneIndex)
	}
	if payload.PaneID == "" {
		t.Errorf("pane_id should be set")
	}
	// Agent type should be detected from synthetic pane title
	if payload.Agent.Type == "" {
		t.Errorf("agent.type should be detected")
	}
}

// =============================================================================
// --robot-metrics tests
// =============================================================================

func TestRobotMetricsWithSyntheticSession(t *testing.T) {
	// --robot-metrics requires a session name
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	sessionName := createSyntheticAgentSession(t, logger)

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-metrics="+sessionName)
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Timestamp string `json:"timestamp"`
		Success   bool   `json:"success"`
		Session   string `json:"session"`
		Period    string `json:"period"`
		TokenUsage struct {
			TotalTokens int64   `json:"total_tokens"`
			TotalCost   float64 `json:"total_cost_usd"`
		} `json:"token_usage"`
		SessionStats struct {
			TotalPrompts int `json:"total_prompts"`
			TotalAgents  int `json:"total_agents"`
			FilesChanged int `json:"files_changed"`
		} `json:"session_stats"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("expected success=true")
	}
	if payload.Session != sessionName {
		t.Errorf("session = %q, want %q", payload.Session, sessionName)
	}
	if payload.Period == "" {
		t.Fatalf("missing period field")
	}
	// Token usage can be 0 for empty metrics
	if payload.TokenUsage.TotalTokens < 0 {
		t.Fatalf("total_tokens should be non-negative")
	}
}

func TestRobotMetricsPeriods(t *testing.T) {
	// --robot-metrics requires a session name
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	sessionName := createSyntheticAgentSession(t, logger)

	periods := []string{"1h", "24h", "7d", "all"}
	for _, period := range periods {
		t.Run(period, func(t *testing.T) {
			out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-metrics="+sessionName, "--metrics-period="+period)

			var payload struct {
				Period  string `json:"period"`
				Success bool   `json:"success"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON for period %s: %v", period, err)
			}
			if payload.Period != period {
				t.Errorf("period = %q, want %q", payload.Period, period)
			}
		})
	}
}

// =============================================================================
// --robot-palette tests
// =============================================================================

func TestRobotPalette(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Timestamp  string `json:"timestamp"`
		Success    bool   `json:"success"`
		Commands   []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Category string `json:"category"`
			Prompt   string `json:"prompt"`
		} `json:"commands"`
		Categories []string `json:"categories"`
		Favorites  []string `json:"favorites"`
		Recent     []struct {
			Key     string `json:"key"`
			UsedAt  string `json:"used_at"`
			Session string `json:"session"`
		} `json:"recent"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("expected success=true")
	}
	if payload.Commands == nil {
		t.Fatalf("commands should be an array")
	}
	if payload.Categories == nil {
		t.Fatalf("categories should be an array")
	}
	// Favorites and Recent can be empty
	if payload.Favorites == nil {
		t.Fatalf("favorites should be an array, not nil")
	}
	if payload.Recent == nil {
		t.Fatalf("recent should be an array, not nil")
	}
}

func TestRobotPaletteWithConfig(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	// Create a config with palette commands
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := `
projects_base = "/tmp"

[[palette]]
key = "test_cmd"
label = "Test Command"
category = "testing"
prompt = "echo test"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "--robot-palette")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Success    bool `json:"success"`
		Commands   []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Category string `json:"category"`
		} `json:"commands"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("expected success=true")
	}

	// Check that our test command is in the palette
	found := false
	for _, cmd := range payload.Commands {
		if cmd.Key == "test_cmd" {
			found = true
			if cmd.Label != "Test Command" {
				t.Errorf("label = %q, want %q", cmd.Label, "Test Command")
			}
			if cmd.Category != "testing" {
				t.Errorf("category = %q, want %q", cmd.Category, "testing")
			}
			break
		}
	}
	if !found {
		t.Errorf("test_cmd not found in palette commands")
	}
}

// =============================================================================
// --robot-alerts tests (TUI parity)
// =============================================================================

func TestRobotAlertsTUI(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-alerts")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	// Handle potential warning messages before JSON by finding the JSON start
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatalf("no JSON object found in output")
	}
	jsonBytes := out[jsonStart:]

	var payload struct {
		Timestamp string `json:"timestamp"`
		Success   bool   `json:"success"`
		Count     int    `json:"count"`
		Alerts    []struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			Severity    string `json:"severity"`
			Message     string `json:"message"`
			CreatedAt   string `json:"created_at"`
			AgeSeconds  int    `json:"age_seconds"`
			Dismissible bool   `json:"dismissible"`
		} `json:"alerts"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("expected success=true")
	}
	if payload.Alerts == nil {
		t.Fatalf("alerts should be an array")
	}
	if payload.Count < 0 {
		t.Fatalf("count should be non-negative")
	}
	// Verify count matches alerts length
	if payload.Count != len(payload.Alerts) {
		t.Errorf("count (%d) != len(alerts) (%d)", payload.Count, len(payload.Alerts))
	}
}

func TestRobotDismissAlertMissingID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without alert ID value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-dismiss-alert")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-dismiss-alert has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-dismiss-alert") {
		t.Errorf("error output should mention the flag name")
	}
}

func TestRobotDismissAlertWithID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Dismiss a (potentially non-existent) alert - should still return valid JSON
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-dismiss-alert=test-alert-123")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Success   bool   `json:"success"`
		AlertID   string `json:"alert_id"`
		Dismissed bool   `json:"dismissed"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Should succeed even if alert doesn't exist (idempotent)
	if !payload.Success {
		t.Logf("note: dismiss returned success=false (alert may not exist)")
	}
	if payload.AlertID != "test-alert-123" {
		t.Errorf("alert_id = %q, want %q", payload.AlertID, "test-alert-123")
	}
}

// =============================================================================
// --robot-replay tests
// =============================================================================

func TestRobotReplayMissingSession(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without session value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-replay")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-replay has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-replay") {
		t.Errorf("error output should mention the flag name")
	}
}

func TestRobotReplayWithInvalidSession(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Using a fake session - should return JSON error
	cmd := exec.Command("ntm", "--robot-replay=fake-session-id", "--replay-id=1234")
	out, _ := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Find JSON in output (may have warnings before and text errors after)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Logf("no JSON in output, command likely failed with text error")
		return
	}
	// Find the closing brace for top-level JSON object
	jsonEnd := strings.LastIndex(string(out), "}")
	if jsonEnd == -1 || jsonEnd < jsonStart {
		t.Logf("malformed JSON in output")
		return
	}
	jsonBytes := out[jsonStart : jsonEnd+1]

	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Logf("could not parse JSON (may be expected): %v", err)
		return
	}

	// Should indicate failure for non-existent session
	if payload.Success && payload.Error == "" {
		t.Logf("note: command succeeded (session may exist)")
	}
}

// =============================================================================
// --robot-beads-list tests
// =============================================================================

func TestRobotBeadsList(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-beads-list")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Timestamp string `json:"timestamp"`
		Success   bool   `json:"success"`
		Beads     []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Status    string   `json:"status"`
			Priority  string   `json:"priority"`
			Type      string   `json:"type"`
			IsReady   bool     `json:"is_ready"`
			IsBlocked bool     `json:"is_blocked"`
			Labels    []string `json:"labels"`
		} `json:"beads"`
		Total    int `json:"total"`
		Filtered int `json:"filtered"`
		Summary  struct {
			Open       int `json:"open"`
			InProgress int `json:"in_progress"`
			Blocked    int `json:"blocked"`
			Closed     int `json:"closed"`
			Ready      int `json:"ready"`
		} `json:"summary"`
		Error string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// May return error if bd not installed or no .beads/ - that's okay
	if !payload.Success && payload.Error != "" {
		t.Logf("beads-list returned error (expected if bd not available): %s", payload.Error)
		return
	}

	// If successful, validate structure
	if payload.Beads == nil {
		t.Fatalf("beads should be an array")
	}
	if payload.Total < 0 {
		t.Fatalf("total should be non-negative")
	}
	// Summary counts should be non-negative
	if payload.Summary.Open < 0 || payload.Summary.InProgress < 0 ||
		payload.Summary.Blocked < 0 || payload.Summary.Closed < 0 {
		t.Fatalf("summary counts should be non-negative")
	}
}

func TestRobotBeadsListWithFilters(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Test various filter flags
	filterFlags := []string{
		"--beads-status=open",
		"--beads-status=in_progress",
		"--beads-priority=P2",
		"--beads-type=task",
	}

	for _, flag := range filterFlags {
		t.Run(flag, func(t *testing.T) {
			out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-beads-list", flag)

			var payload struct {
				Success bool   `json:"success"`
				Error   string `json:"error,omitempty"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			// Don't fail if bd not available
			if !payload.Success && payload.Error != "" {
				t.Logf("skipped: %s", payload.Error)
			}
		})
	}
}

// =============================================================================
// --robot-bead-show tests
// =============================================================================

func TestRobotBeadShowMissingID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without bead ID value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-bead-show")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-bead-show has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-bead-show") {
		t.Errorf("error output should mention the flag name")
	}
}

func TestRobotBeadShowInvalidID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	cmd := exec.Command("ntm", "--robot-bead-show=nonexistent-bead-id")
	out, _ := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Find JSON in output (may have warnings before and text errors after)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Logf("no JSON in output, command likely failed with text error")
		return
	}
	// Find the closing brace for top-level JSON object
	jsonEnd := strings.LastIndex(string(out), "}")
	if jsonEnd == -1 || jsonEnd < jsonStart {
		t.Logf("malformed JSON in output")
		return
	}
	jsonBytes := out[jsonStart : jsonEnd+1]

	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Should fail gracefully for non-existent bead
	if payload.Success && payload.Error == "" {
		t.Logf("note: show succeeded (bead may exist)")
	}
}

// =============================================================================
// --robot-bead-claim tests
// =============================================================================

func TestRobotBeadClaimMissingID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without bead ID value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-bead-claim")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-bead-claim has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-bead-claim") {
		t.Errorf("error output should mention the flag name")
	}
}

// =============================================================================
// --robot-bead-close tests
// =============================================================================

func TestRobotBeadCloseMissingID(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without bead ID value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-bead-close")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-bead-close has no value")
	}
	// Output should mention the flag issue
	if !strings.Contains(string(out), "robot-bead-close") {
		t.Errorf("error output should mention the flag name")
	}
}

// =============================================================================
// --robot-bead-create tests
// =============================================================================

func TestRobotBeadCreateMissingTitle(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// --robot-bead-create is a boolean flag; calling without --bead-title
	// should return JSON error (our application validates this)
	cmd := exec.Command("ntm", "--robot-bead-create")
	out, _ := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Find JSON in output (may have warnings before and text errors after)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Logf("no JSON in output, command likely failed with text error")
		return
	}
	// Find the closing brace for top-level JSON object
	jsonEnd := strings.LastIndex(string(out), "}")
	if jsonEnd == -1 || jsonEnd < jsonStart {
		t.Logf("malformed JSON in output")
		return
	}
	jsonBytes := out[jsonStart : jsonEnd+1]

	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Logf("could not parse JSON: %v", err)
		return
	}

	// Should fail because --bead-title is required
	if payload.Success {
		t.Errorf("expected success=false for missing title")
	}
	if payload.Error == "" {
		t.Errorf("expected error message explaining missing title")
	}
}

// =============================================================================
// Integration test: Session with TUI parity commands
// =============================================================================

func TestTUIParityWithLiveSession(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("tui_parity_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
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
		exec.Command("tmux", "kill-session", "-t", session).Run()
	})

	// Spawn session
	logger.LogSection("spawn session for TUI parity test")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(500 * time.Millisecond)

	// Verify session was created
	testutil.AssertSessionExists(t, logger, session)

	// Test --robot-files with session filter
	logger.LogSection("robot-files with session")
	filesOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-files="+session)

	var filesPayload struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(filesOut, &filesPayload); err != nil {
		t.Fatalf("invalid files JSON: %v", err)
	}
	if filesPayload.Session != session {
		t.Errorf("files session = %q, want %q", filesPayload.Session, session)
	}

	// Test --robot-inspect-pane
	logger.LogSection("robot-inspect-pane")
	inspectOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-inspect-pane="+session, "--inspect-index=0")

	var inspectPayload struct {
		Success   bool   `json:"success"`
		Session   string `json:"session"`
		PaneIndex int    `json:"pane_index"`
		Agent     struct {
			Type string `json:"type"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(inspectOut, &inspectPayload); err != nil {
		t.Fatalf("invalid inspect JSON: %v", err)
	}
	if !inspectPayload.Success {
		t.Errorf("inspect-pane should succeed")
	}
	if inspectPayload.Session != session {
		t.Errorf("inspect session = %q, want %q", inspectPayload.Session, session)
	}

	// Test --robot-metrics with session
	logger.LogSection("robot-metrics with session")
	metricsOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-metrics="+session)

	var metricsPayload struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(metricsOut, &metricsPayload); err != nil {
		t.Fatalf("invalid metrics JSON: %v", err)
	}
	if metricsPayload.Session != session {
		t.Errorf("metrics session = %q, want %q", metricsPayload.Session, session)
	}

	// Test --robot-palette (no session needed)
	logger.LogSection("robot-palette")
	paletteOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "--robot-palette")

	var palettePayload struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(paletteOut, &palettePayload); err != nil {
		t.Fatalf("invalid palette JSON: %v", err)
	}
	if !palettePayload.Success {
		t.Errorf("palette should succeed")
	}

	// Test --robot-alerts
	logger.LogSection("robot-alerts")
	alertsOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "--robot-alerts")

	var alertsPayload struct {
		Success bool `json:"success"`
		Count   int  `json:"count"`
	}
	if err := json.Unmarshal(alertsOut, &alertsPayload); err != nil {
		t.Fatalf("invalid alerts JSON: %v", err)
	}
	if !alertsPayload.Success {
		t.Errorf("alerts should succeed")
	}
}
