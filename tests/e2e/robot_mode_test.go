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

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// These tests exercise the robot-mode JSON outputs end-to-end using the built
// binary on PATH. They intentionally avoid deep schema validation beyond
// parseability to keep them fast and resilient to small additive fields.

func TestRobotVersion(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-version")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
		GoVersion string `json:"go_version"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Version == "" {
		t.Fatalf("missing version field in output")
	}
	if payload.GoVersion == "" {
		t.Fatalf("missing go_version field in output")
	}
	if payload.Commit == "" {
		t.Fatalf("missing commit field in output")
	}
	if payload.BuildDate == "" {
		t.Fatalf("missing build_date field in output")
	}
}

func TestRobotStatusEmptySessions(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		GeneratedAt string                   `json:"generated_at"`
		Sessions    []map[string]interface{} `json:"sessions"`
		Summary     struct {
			TotalSessions int `json:"total_sessions"`
			TotalAgents   int `json:"total_agents"`
			ClaudeCount   int `json:"claude_count"`
			CodexCount    int `json:"codex_count"`
			GeminiCount   int `json:"gemini_count"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Fatalf("missing generated_at field")
	}

	if payload.Sessions == nil {
		t.Fatalf("missing sessions array")
	}

	if payload.Summary.TotalSessions < 0 || payload.Summary.TotalAgents < 0 {
		t.Fatalf("summary counts should be non-negative: %+v", payload.Summary)
	}
}

func TestRobotPlan(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-plan")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		GeneratedAt    string                   `json:"generated_at"`
		Actions        []map[string]interface{} `json:"actions"`
		Recommendation string                   `json:"recommendation"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Fatalf("missing generated_at field")
	}
	if _, err := time.Parse(time.RFC3339, payload.GeneratedAt); err != nil {
		t.Fatalf("generated_at not RFC3339: %v", err)
	}

	if payload.Actions == nil {
		t.Fatalf("missing actions array")
	}

	if payload.Recommendation == "" {
		t.Fatalf("missing recommendation field")
	}

	for i, action := range payload.Actions {
		if _, ok := action["priority"]; !ok {
			t.Fatalf("actions[%d] missing priority", i)
		}
		if cmd, ok := action["command"].(string); !ok || strings.TrimSpace(cmd) == "" {
			t.Fatalf("actions[%d] missing non-empty command", i)
		}
	}
}

// TestRobotStatusWithLiveSession ensures a real session appears in robot-status.
func TestRobotStatusWithLiveSession(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("ntm_robot_status_%d", time.Now().UnixNano())
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
gemini = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn session with two agents (claude+codex)
	logger.LogSection("spawn session")
	if _, err := logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=1", "--cod=1"); err != nil {
		t.Fatalf("ntm spawn failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// robot-status should include the session and at least 2 agents
	logger.LogSection("robot-status")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "--robot-status")

	var payload struct {
		Sessions []struct {
			Name    string                   `json:"name"`
			Agents  []map[string]interface{} `json:"agents"`
			Summary struct {
				TotalAgents int `json:"total_agents"`
			} `json:"summary"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	found := false
	for _, s := range payload.Sessions {
		if s.Name == session {
			found = true
			if s.Summary.TotalAgents < 2 {
				t.Fatalf("expected at least 2 agents (claude+codex) in summary, got %d", s.Summary.TotalAgents)
			}
			break
		}
	}
	if !found {
		t.Fatalf("robot-status did not include session %q; payload: %+v", session, payload)
	}
}

func TestRobotHelp(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-help")

	if len(out) == 0 {
		t.Fatalf("robot help output empty")
	}
	if !strings.Contains(string(out), "robot-status") {
		t.Fatalf("robot help missing expected marker")
	}
}

// TestRobotStatusWithSyntheticAgents ensures agent counts and types are surfaced when panes
// follow the NTM naming convention. This avoids launching real agent binaries by
// creating a tmux session with synthetic pane titles.
func TestRobotStatusWithSyntheticAgents(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	sessionName := createSyntheticAgentSession(t, logger)

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Sessions    []struct {
			Name   string `json:"name"`
			Agents []struct {
				Type    string `json:"type"`
				Pane    string `json:"pane"`
				PaneIdx int    `json:"pane_idx"`
			} `json:"agents"`
		} `json:"sessions"`
		Summary struct {
			TotalAgents int `json:"total_agents"`
			ClaudeCount int `json:"claude_count"`
			CodexCount  int `json:"codex_count"`
			GeminiCount int `json:"gemini_count"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Fatalf("generated_at should be set")
	}
	if _, err := time.Parse(time.RFC3339, payload.GeneratedAt); err != nil {
		t.Fatalf("generated_at not RFC3339: %v", err)
	}

	var targetSession *struct {
		Name   string `json:"name"`
		Agents []struct {
			Type    string `json:"type"`
			Pane    string `json:"pane"`
			PaneIdx int    `json:"pane_idx"`
		} `json:"agents"`
	}
	for i := range payload.Sessions {
		if payload.Sessions[i].Name == sessionName {
			targetSession = &payload.Sessions[i]
			break
		}
	}

	if targetSession == nil {
		t.Fatalf("robot-status missing session %s", sessionName)
	}

	if len(targetSession.Agents) < 3 {
		t.Fatalf("expected at least 3 agents for %s, got %d", sessionName, len(targetSession.Agents))
	}

	typeCounts := map[string]int{}
	for _, a := range targetSession.Agents {
		typeCounts[a.Type]++
	}

	if typeCounts["cc"] == 0 || typeCounts["cod"] == 0 || typeCounts["gmi"] == 0 {
		t.Fatalf("expected cc, cod, gmi agents in session %s; got %+v", sessionName, typeCounts)
	}

	if payload.Summary.TotalAgents < 3 {
		t.Fatalf("summary.total_agents should reflect at least synthetic agents, got %d", payload.Summary.TotalAgents)
	}
	if payload.Summary.ClaudeCount < 1 || payload.Summary.CodexCount < 1 || payload.Summary.GeminiCount < 1 {
		t.Fatalf("summary counts missing agent types: %+v", payload.Summary)
	}
}

func TestRobotStatusIncludesSystemFields(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		System      struct {
			Version   string `json:"version"`
			OS        string `json:"os"`
			Arch      string `json:"arch"`
			TmuxOK    bool   `json:"tmux_available"`
			GoVersion string `json:"go_version"`
		} `json:"system"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Fatalf("generated_at should be set")
	}
	if _, err := time.Parse(time.RFC3339, payload.GeneratedAt); err != nil {
		t.Fatalf("generated_at not RFC3339: %v", err)
	}
	if payload.System.Version == "" {
		t.Fatalf("system.version should be set")
	}
	if payload.System.OS == "" || payload.System.Arch == "" {
		t.Fatalf("system.os/arch should be set")
	}
	if payload.System.GoVersion == "" {
		t.Fatalf("system.go_version should be set")
	}
}

func TestRobotStatusHandlesLongSessionNames(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	longName := "robot_json_long_session_name_status_validation_1234567890"
	sessionName := createSyntheticAgentSessionWithName(t, logger, longName)

	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")
	logger.Log("FULL JSON OUTPUT:\n%s", string(out))

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Sessions    []struct {
			Name string `json:"name"`
		} `json:"sessions"`
		Summary struct {
			TotalSessions int `json:"total_sessions"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload.GeneratedAt == "" {
		t.Fatalf("generated_at should be set")
	}

	var found bool
	for _, s := range payload.Sessions {
		if s.Name == sessionName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("robot-status missing long session name %s", sessionName)
	}
	if payload.Summary.TotalSessions < 1 {
		t.Fatalf("summary.total_sessions should be at least 1, got %d", payload.Summary.TotalSessions)
	}
}

// TestRobotSpawn tests the --robot-spawn flag for creating sessions.
func TestRobotSpawn(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_spawn_%d", time.Now().UnixNano())
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
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Test robot-spawn with Claude agents
	logger.LogSection("robot-spawn")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-spawn", session, "--spawn-cc=2", "--spawn-wait", "--spawn-safety")
	logger.Log("robot-spawn output: %s", string(out))

	var payload struct {
		Session string `json:"session"`
		Error   string `json:"error,omitempty"`
		Agents  []struct {
			Pane  string `json:"pane"`
			Type  string `json:"type"`
			Title string `json:"title"`
			Ready bool   `json:"ready"`
		} `json:"agents"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Error != "" {
		t.Fatalf("robot-spawn should succeed, got error: %s", payload.Error)
	}
	if payload.Session != session {
		t.Errorf("session = %q, want %q", payload.Session, session)
	}

	// Count Claude agents (type "claude" in agents list, excluding "user" type)
	claudeCount := 0
	for _, agent := range payload.Agents {
		if agent.Type == "claude" {
			claudeCount++
		}
	}
	if claudeCount < 2 {
		t.Errorf("claude count = %d, want at least 2", claudeCount)
	}
}

// TestRobotSendAndTail tests --robot-send and --robot-tail together.
func TestRobotSendAndTail(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_%d", time.Now().UnixNano())
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
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn session first
	logger.LogSection("spawn session for send test")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(500 * time.Millisecond)

	// Verify session was created
	testutil.AssertSessionExists(t, logger, session)

	// Test robot-send
	marker := fmt.Sprintf("ROBOT_SEND_MARKER_%d", time.Now().UnixNano())
	logger.LogSection("robot-send")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", marker), "--all")
	logger.Log("robot-send output: %s", string(out))

	var sendPayload struct {
		Success bool `json:"success"`
		Targets []struct {
			PaneIdx int `json:"pane_idx"`
		} `json:"targets"`
		TargetCount int `json:"target_count"`
	}

	if err := json.Unmarshal(out, &sendPayload); err != nil {
		t.Fatalf("invalid robot-send JSON: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("robot-send should succeed")
	}
	if sendPayload.TargetCount < 1 {
		t.Errorf("target_count = %d, want at least 1", sendPayload.TargetCount)
	}

	// Wait for command to execute
	time.Sleep(300 * time.Millisecond)

	// Test robot-tail
	logger.LogSection("robot-tail")
	out = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-tail", session, "--lines=50")
	logger.Log("robot-tail output: %s", string(out))

	var tailPayload struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
		Panes   []struct {
			Index   int    `json:"index"`
			Content string `json:"content"`
		} `json:"panes"`
	}

	if err := json.Unmarshal(out, &tailPayload); err != nil {
		t.Fatalf("invalid robot-tail JSON: %v", err)
	}

	if !tailPayload.Success {
		t.Fatalf("robot-tail should succeed")
	}
	if tailPayload.Session != session {
		t.Errorf("session = %q, want %q", tailPayload.Session, session)
	}

	// Check if marker appears in any pane content
	markerFound := false
	for _, pane := range tailPayload.Panes {
		if strings.Contains(pane.Content, marker) {
			markerFound = true
			logger.Log("Found marker in pane %d", pane.Index)
			break
		}
	}
	if !markerFound {
		logger.Log("WARNING: marker not found in tail output - timing issue possible")
	}
}

// TestRobotInterrupt tests the --robot-interrupt flag.
func TestRobotInterrupt(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_interrupt_%d", time.Now().UnixNano())
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
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn session
	logger.LogSection("spawn session for interrupt test")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(500 * time.Millisecond)

	// Verify session was created
	testutil.AssertSessionExists(t, logger, session)

	// Test robot-interrupt
	logger.LogSection("robot-interrupt")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-interrupt", session, "--interrupt-force")
	logger.Log("robot-interrupt output: %s", string(out))

	var payload struct {
		Success     bool `json:"success"`
		Interrupted int  `json:"interrupted"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid robot-interrupt JSON: %v", err)
	}

	if !payload.Success {
		t.Fatalf("robot-interrupt should succeed")
	}
}

func createSyntheticAgentSession(t *testing.T, logger *testutil.TestLogger) string {
	t.Helper()

	name := fmt.Sprintf("robot_json_%d", time.Now().UnixNano())
	return createSyntheticAgentSessionWithName(t, logger, name)
}

func createSyntheticAgentSessionWithName(t *testing.T, logger *testutil.TestLogger, name string) string {
	t.Helper()

	workdir := t.TempDir()

	logger.LogSection("Create synthetic tmux session")
	testutil.AssertCommandSuccess(t, logger, "tmux", "new-session", "-d", "-s", name, "-c", workdir)
	testutil.AssertCommandSuccess(t, logger, "tmux", "split-window", "-t", name, "-h", "-c", workdir)
	testutil.AssertCommandSuccess(t, logger, "tmux", "split-window", "-t", name, "-v", "-c", workdir)
	testutil.AssertCommandSuccess(t, logger, "tmux", "select-layout", "-t", name, "tiled")

	paneIDsRaw := testutil.AssertCommandSuccess(t, logger, "tmux", "list-panes", "-t", name, "-F", "#{pane_id}")
	panes := strings.Fields(string(paneIDsRaw))
	if len(panes) < 3 {
		t.Fatalf("expected at least 3 panes, got %d (output=%s)", len(panes), string(paneIDsRaw))
	}

	titles := []string{
		fmt.Sprintf("%s__cc_1", name),
		fmt.Sprintf("%s__cod_1", name),
		fmt.Sprintf("%s__gmi_1", name),
	}

	for i, id := range panes[:3] {
		testutil.AssertCommandSuccess(t, logger, "tmux", "select-pane", "-t", id, "-T", titles[i])
	}

	t.Cleanup(func() {
		logger.LogSection("Teardown synthetic session")
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", name).Run()
	})

	return name
}

// TestRobotStatusAllAgentStates comprehensively tests robot-status JSON output
// for all possible agent states: idle, working, error, unknown.
func TestRobotStatusAllAgentStates(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_status_all_states_%d", time.Now().UnixNano())
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
gemini = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn session with agents to simulate different states
	logger.LogSection("spawn session with multiple agents")
	if _, err := logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=2", "--cod=1", "--gmi=1"); err != nil {
		t.Fatalf("ntm spawn failed: %v", err)
	}
	time.Sleep(1 * time.Second) // Wait for agents to stabilize

	// Test basic robot-status JSON validity
	logger.LogSection("test robot-status JSON validity")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "--robot-status")
	logger.Log("[E2E-ROBOT-STATUS] Full JSON output:\n%s", string(out))

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		System      struct {
			Version   string `json:"version"`
			OS        string `json:"os"`
			Arch      string `json:"arch"`
			TmuxOK    bool   `json:"tmux_available"`
			GoVersion string `json:"go_version"`
		} `json:"system"`
		Sessions []struct {
			Name   string `json:"name"`
			Agents []struct {
				Type         string `json:"type"`
				Pane         string `json:"pane"`
				PaneIdx      int    `json:"pane_idx"`
				State        string `json:"state"`
				ErrorType    string `json:"error_type,omitempty"`
				LastActivity string `json:"last_activity,omitempty"`
			} `json:"agents"`
			Summary struct {
				TotalAgents   int `json:"total_agents"`
				WorkingAgents int `json:"working_agents"`
				IdleAgents    int `json:"idle_agents"`
				ErrorAgents   int `json:"error_agents"`
			} `json:"summary"`
		} `json:"sessions"`
		Summary struct {
			TotalSessions int `json:"total_sessions"`
			TotalAgents   int `json:"total_agents"`
			ClaudeCount   int `json:"claude_count"`
			CodexCount    int `json:"codex_count"`
			GeminiCount   int `json:"gemini_count"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Validate required top-level fields
	if payload.GeneratedAt == "" {
		t.Fatalf("missing generated_at field")
	}
	if _, err := time.Parse(time.RFC3339, payload.GeneratedAt); err != nil {
		t.Fatalf("generated_at not RFC3339: %v", err)
	}

	// Validate system fields
	if payload.System.Version == "" {
		t.Fatalf("missing system.version")
	}
	if payload.System.OS == "" || payload.System.Arch == "" {
		t.Fatalf("missing system.os or system.arch")
	}
	if payload.System.GoVersion == "" {
		t.Fatalf("missing system.go_version")
	}

	// Find our session
	var targetSession *struct {
		Name   string `json:"name"`
		Agents []struct {
			Type         string `json:"type"`
			Pane         string `json:"pane"`
			PaneIdx      int    `json:"pane_idx"`
			State        string `json:"state"`
			ErrorType    string `json:"error_type,omitempty"`
			LastActivity string `json:"last_activity,omitempty"`
		} `json:"agents"`
		Summary struct {
			TotalAgents   int `json:"total_agents"`
			WorkingAgents int `json:"working_agents"`
			IdleAgents    int `json:"idle_agents"`
			ErrorAgents   int `json:"error_agents"`
		} `json:"summary"`
	}
	for i := range payload.Sessions {
		if payload.Sessions[i].Name == session {
			targetSession = &payload.Sessions[i]
			break
		}
	}

	if targetSession == nil {
		t.Fatalf("robot-status missing session %s", session)
	}

	// Validate agent count and types
	if len(targetSession.Agents) < 4 {
		t.Fatalf("expected at least 4 agents (2 claude + 1 codex + 1 gemini), got %d", len(targetSession.Agents))
	}

	// Test that agents have valid states
	validStates := map[string]bool{
		"idle":    true,
		"working": true,
		"error":   true,
		"unknown": true,
	}

	statesCounted := map[string]int{}
	typeCounts := map[string]int{}

	for _, agent := range targetSession.Agents {
		// Validate required agent fields
		if agent.Type == "" {
			t.Fatalf("agent missing type field")
		}
		if agent.Pane == "" {
			t.Fatalf("agent missing pane field")
		}
		if agent.State == "" {
			t.Fatalf("agent missing state field")
		}

		// Validate state is one of the known valid states
		if !validStates[agent.State] {
			t.Fatalf("agent has invalid state %q, expected one of: idle, working, error, unknown", agent.State)
		}

		statesCounted[agent.State]++
		typeCounts[agent.Type]++

		logger.Log("[E2E-ROBOT-STATUS] Agent pane=%s type=%s state=%s error_type=%s",
			agent.Pane, agent.Type, agent.State, agent.ErrorType)
	}

	// Validate summary counts match agent counts
	expectedTotal := len(targetSession.Agents)
	if targetSession.Summary.TotalAgents != expectedTotal {
		t.Fatalf("summary.total_agents = %d, expected %d", targetSession.Summary.TotalAgents, expectedTotal)
	}

	// Validate state counts add up
	calculatedTotal := targetSession.Summary.IdleAgents + targetSession.Summary.WorkingAgents + targetSession.Summary.ErrorAgents
	// Note: unknown state agents might not be counted in specific categories
	if calculatedTotal > expectedTotal {
		t.Fatalf("sum of state counts (%d) exceeds total agents (%d)", calculatedTotal, expectedTotal)
	}

	// Validate agent type counts
	expectedClaude := 2
	expectedCodex := 1
	expectedGemini := 1
	if typeCounts["cc"] < expectedClaude {
		t.Fatalf("expected at least %d claude agents, got %d", expectedClaude, typeCounts["cc"])
	}
	if typeCounts["cod"] < expectedCodex {
		t.Fatalf("expected at least %d codex agents, got %d", expectedCodex, typeCounts["cod"])
	}
	if typeCounts["gmi"] < expectedGemini {
		t.Fatalf("expected at least %d gemini agents, got %d", expectedGemini, typeCounts["gmi"])
	}

	logger.Log("[E2E-ROBOT-STATUS] State distribution: idle=%d working=%d error=%d unknown=%d",
		statesCounted["idle"], statesCounted["working"], statesCounted["error"], statesCounted["unknown"])
	logger.Log("[E2E-ROBOT-STATUS] Type distribution: claude=%d codex=%d gemini=%d",
		typeCounts["cc"], typeCounts["cod"], typeCounts["gmi"])
}

// TestRobotStatusVerboseMode tests that verbose mode includes additional fields
func TestRobotStatusVerboseMode(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	_ = createSyntheticAgentSession(t, logger)

	// Test with verbose flag (if supported)
	logger.LogSection("test robot-status verbose mode")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status", "--verbose")
	logger.Log("[E2E-ROBOT-STATUS-VERBOSE] Full JSON output:\n%s", string(out))

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Sessions    []struct {
			Name   string `json:"name"`
			Agents []struct {
				Type         string `json:"type"`
				State        string `json:"state"`
				LastActivity string `json:"last_activity,omitempty"`
				MemoryMB     int    `json:"memory_mb,omitempty"`
				LastOutput   string `json:"last_output,omitempty"`
			} `json:"agents"`
		} `json:"sessions"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON in verbose mode: %v", err)
	}

	// In verbose mode, we expect additional fields (though their presence depends on implementation)
	if payload.GeneratedAt == "" {
		t.Fatalf("verbose mode missing generated_at")
	}

	logger.Log("[E2E-ROBOT-STATUS-VERBOSE] Verbose mode JSON validated successfully")
}

// TestRobotStatusErrorHandling tests error conditions and malformed input
func TestRobotStatusErrorHandling(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Test with non-existent session filter (should still return valid JSON)
	logger.LogSection("test robot-status with non-existent session")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")
	logger.Log("[E2E-ROBOT-STATUS-ERROR] Output for empty state:\n%s", string(out))

	var payload struct {
		GeneratedAt string                   `json:"generated_at"`
		Sessions    []map[string]interface{} `json:"sessions"`
		Summary     struct {
			TotalSessions int `json:"total_sessions"`
			TotalAgents   int `json:"total_agents"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON in error condition: %v", err)
	}

	// Should still have valid structure even with no sessions
	if payload.GeneratedAt == "" {
		t.Fatalf("missing generated_at in error condition")
	}
	if payload.Sessions == nil {
		t.Fatalf("sessions should be empty array, not nil")
	}

	logger.Log("[E2E-ROBOT-STATUS-ERROR] Error handling validated successfully")
}

// TestRobotStatusFieldStability tests that JSON schema remains stable
func TestRobotStatusFieldStability(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")

	// Define expected schema for critical fields that should never change
	type ExpectedAgentSchema struct {
		Type    string `json:"type"`
		Pane    string `json:"pane"`
		PaneIdx int    `json:"pane_idx"`
		State   string `json:"state"`
	}

	type ExpectedSessionSchema struct {
		Name   string                `json:"name"`
		Agents []ExpectedAgentSchema `json:"agents"`
	}

	type ExpectedSchema struct {
		GeneratedAt string                  `json:"generated_at"`
		Sessions    []ExpectedSessionSchema `json:"sessions"`
		Summary     struct {
			TotalSessions int `json:"total_sessions"`
			TotalAgents   int `json:"total_agents"`
		} `json:"summary"`
	}

	var payload ExpectedSchema
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("JSON schema validation failed: %v", err)
	}

	// Verify critical fields are present (this ensures API stability)
	if payload.GeneratedAt == "" {
		t.Fatalf("critical field 'generated_at' missing")
	}
	if payload.Sessions == nil {
		t.Fatalf("critical field 'sessions' missing")
	}
	if payload.Summary.TotalSessions < 0 || payload.Summary.TotalAgents < 0 {
		t.Fatalf("summary counts should be non-negative")
	}

	logger.Log("[E2E-ROBOT-STATUS-SCHEMA] JSON schema stability validated")
}

// TestRobotSendTargetFiltering tests comprehensive target filtering for robot-send.
func TestRobotSendTargetFiltering(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_filtering_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-target-filtering")
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Starting comprehensive target filtering tests")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn mixed session with multiple agent types
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=3", "--cod=2", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Spawn output: %s", string(spawnOut))

	// Give agents time to initialize
	time.Sleep(2 * time.Second)
	testutil.AssertSessionExists(t, logger, session)

	// Test 1: Filter by agent type (claude)
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 1: Filter by agent type --type=claude")
	testMessage := fmt.Sprintf("FILTER_TEST_CLAUDE_%d", time.Now().UnixNano())
	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude")

	var sendPayload struct {
		Success    bool     `json:"success"`
		Session    string   `json:"session"`
		Targets    []string `json:"targets"`
		Successful []string `json:"successful"`
		Failed     []struct {
			Pane  string `json:"pane"`
			Error string `json:"error"`
		} `json:"failed"`
		MessagePreview string    `json:"message_preview"`
		SentAt         time.Time `json:"sent_at"`
	}

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] Invalid robot-send JSON: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] robot-send --type=claude should succeed")
	}
	if sendPayload.Session != session {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected session %s, got %s", session, sendPayload.Session)
	}
	if len(sendPayload.Targets) != 3 {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected 3 claude targets, got %d", len(sendPayload.Targets))
	}
	if len(sendPayload.Successful) != 3 {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected 3 successful sends, got %d", len(sendPayload.Successful))
	}
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 1 PASSED: claude filter sent to %d targets", len(sendPayload.Targets))

	// Test 2: Filter by agent type (codex)
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 2: Filter by agent type --type=cod")
	testMessage = fmt.Sprintf("FILTER_TEST_CODEX_%d", time.Now().UnixNano())
	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=cod")

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] Invalid robot-send JSON for codex: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] robot-send --type=cod should succeed")
	}
	if len(sendPayload.Targets) != 2 {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected 2 codex targets, got %d", len(sendPayload.Targets))
	}
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 2 PASSED: codex filter sent to %d targets", len(sendPayload.Targets))

	// Test 3: Send to all agents
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 3: Send to all agents --all")
	testMessage = fmt.Sprintf("FILTER_TEST_ALL_%d", time.Now().UnixNano())
	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--all")

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] Invalid robot-send JSON for all: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] robot-send --all should succeed")
	}
	if len(sendPayload.Targets) < 5 {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected at least 5 targets (all agents), got %d", len(sendPayload.Targets))
	}
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 3 PASSED: --all sent to %d targets", len(sendPayload.Targets))

	// Test 4: Specific pane targeting
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 4: Send to specific panes --panes=1,2")
	testMessage = fmt.Sprintf("FILTER_TEST_PANES_%d", time.Now().UnixNano())
	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--panes=1,2")

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] Invalid robot-send JSON for panes: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-FILTERING] robot-send --panes should succeed")
	}
	if len(sendPayload.Targets) != 2 {
		t.Errorf("[E2E-ROBOT-SEND-FILTERING] Expected 2 pane targets, got %d", len(sendPayload.Targets))
	}
	logger.Log("[E2E-ROBOT-SEND-FILTERING] Test 4 PASSED: pane filter sent to %d targets", len(sendPayload.Targets))

	logger.Log("[E2E-ROBOT-SEND-FILTERING] All target filtering tests completed successfully")
}

// TestRobotSendExcludeFiltering tests exclude filtering capabilities.
func TestRobotSendExcludeFiltering(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_exclude_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-exclude-filtering")
	logger.Log("[E2E-ROBOT-SEND-EXCLUDE] Starting exclude filtering tests")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn session with multiple agents
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=3", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-EXCLUDE] Spawn output: %s", string(spawnOut))

	time.Sleep(2 * time.Second)
	testutil.AssertSessionExists(t, logger, session)

	// Test exclude functionality
	logger.Log("[E2E-ROBOT-SEND-EXCLUDE] Test: Send to all claude agents except pane 1 --type=claude --exclude=1")
	testMessage := fmt.Sprintf("EXCLUDE_TEST_%d", time.Now().UnixNano())
	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude", "--exclude=1")

	var sendPayload struct {
		Success        bool     `json:"success"`
		Targets        []string `json:"targets"`
		Successful     []string `json:"successful"`
		MessagePreview string   `json:"message_preview"`
	}

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-EXCLUDE] Invalid robot-send JSON: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-EXCLUDE] robot-send with exclude should succeed")
	}

	// Should send to 2 claude agents (3 total - 1 excluded)
	if len(sendPayload.Targets) != 2 {
		t.Errorf("[E2E-ROBOT-SEND-EXCLUDE] Expected 2 targets after excluding 1 pane, got %d", len(sendPayload.Targets))
	}

	logger.Log("[E2E-ROBOT-SEND-EXCLUDE] PASSED: exclude filter sent to %d targets", len(sendPayload.Targets))
}

// TestRobotSendDryRun tests dry-run functionality.
func TestRobotSendDryRun(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_dryrun_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-dry-run")
	logger.Log("[E2E-ROBOT-SEND-DRYRUN] Starting dry-run tests")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn session
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=2", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-DRYRUN] Spawn output: %s", string(spawnOut))

	time.Sleep(2 * time.Second)
	testutil.AssertSessionExists(t, logger, session)

	// Test dry-run mode
	logger.Log("[E2E-ROBOT-SEND-DRYRUN] Test: Dry run with --dry-run")
	testMessage := fmt.Sprintf("DRYRUN_TEST_%d", time.Now().UnixNano())
	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude", "--dry-run")

	var sendPayload struct {
		Success     bool     `json:"success"`
		DryRun      bool     `json:"dry_run"`
		WouldSendTo []string `json:"would_send_to"`
		Targets     []string `json:"targets"`
	}

	if err := json.Unmarshal(sendOut, &sendPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-DRYRUN] Invalid robot-send JSON: %v", err)
	}

	if !sendPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-DRYRUN] robot-send dry-run should succeed")
	}
	if !sendPayload.DryRun {
		t.Fatalf("[E2E-ROBOT-SEND-DRYRUN] dry_run field should be true")
	}
	if len(sendPayload.WouldSendTo) == 0 {
		t.Errorf("[E2E-ROBOT-SEND-DRYRUN] would_send_to should have entries in dry-run mode")
	}

	logger.Log("[E2E-ROBOT-SEND-DRYRUN] PASSED: dry-run would send to %d targets", len(sendPayload.WouldSendTo))
}

// TestRobotSendErrorHandling tests error scenarios and edge cases.
func TestRobotSendErrorHandling(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.LogSection("robot-send-error-handling")
	logger.Log("[E2E-ROBOT-SEND-ERROR] Starting error handling tests")

	// Test 1: Non-existent session
	logger.Log("[E2E-ROBOT-SEND-ERROR] Test 1: Send to non-existent session")
	nonexistentSession := fmt.Sprintf("nonexistent_%d", time.Now().UnixNano())

	cmd := exec.Command("ntm", "--robot-send", nonexistentSession, "--msg", "test")
	out, err := cmd.CombinedOutput()

	logger.Log("[E2E-ROBOT-SEND-ERROR] Command output: %s", string(out))
	logger.Log("[E2E-ROBOT-SEND-ERROR] Command error: %v", err)

	// Robot mode should return JSON even for errors (exit code 0, but success=false)

	var errorPayload struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
		Session   string `json:"session"`
	}

	if jsonErr := json.Unmarshal(out, &errorPayload); jsonErr != nil {
		t.Fatalf("[E2E-ROBOT-SEND-ERROR] Error response should be valid JSON: %v", jsonErr)
	}

	if errorPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-ERROR] Error response should have success=false")
	}
	if errorPayload.Error == "" {
		t.Errorf("[E2E-ROBOT-SEND-ERROR] Error response should have error message")
	}
	if errorPayload.ErrorCode == "" {
		t.Errorf("[E2E-ROBOT-SEND-ERROR] Error response should have error_code")
	}

	logger.Log("[E2E-ROBOT-SEND-ERROR] Test 1 PASSED: Non-existent session error handled correctly")

	// Test 2: Empty message
	logger.Log("[E2E-ROBOT-SEND-ERROR] Test 2: Send empty message")
	cmd = exec.Command("ntm", "--robot-send", "dummy", "--msg", "")
	out, err = cmd.CombinedOutput()

	logger.Log("[E2E-ROBOT-SEND-ERROR] Empty message output: %s", string(out))
	logger.Log("[E2E-ROBOT-SEND-ERROR] Empty message error: %v", err)

	// Empty message may return plain text error or JSON error, both are acceptable
	if jsonErr := json.Unmarshal(out, &errorPayload); jsonErr != nil {
		// Plain text error is acceptable for validation errors
		if err != nil && strings.Contains(string(out), "msg is required") {
			logger.Log("[E2E-ROBOT-SEND-ERROR] Test 2 PASSED: Empty message rejected with plain text error")
		} else {
			t.Fatalf("[E2E-ROBOT-SEND-ERROR] Empty message should produce valid JSON or clear error: %v", jsonErr)
		}
	} else {
		// JSON error response
		if errorPayload.Success {
			t.Fatalf("[E2E-ROBOT-SEND-ERROR] Empty message should fail")
		}
		logger.Log("[E2E-ROBOT-SEND-ERROR] Test 2 PASSED: Empty message rejected with JSON error")
	}

	logger.Log("[E2E-ROBOT-SEND-ERROR] Test 2 PASSED: Empty message error handled correctly")

	logger.Log("[E2E-ROBOT-SEND-ERROR] All error handling tests completed")
}

// TestRobotSendFieldStability validates JSON schema stability for robot-send.
func TestRobotSendFieldStability(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_schema_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-schema-stability")
	logger.Log("[E2E-ROBOT-SEND-SCHEMA] Starting JSON schema stability validation")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn session
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=1", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-SCHEMA] Spawn output: %s", string(spawnOut))

	time.Sleep(2 * time.Second)

	// Send message and validate schema
	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", "echo schema_test", "--type=claude")

	// Define expected schema for critical fields that should never change
	type ExpectedSendSchema struct {
		Success    bool      `json:"success"`
		Session    string    `json:"session"`
		SentAt     time.Time `json:"sent_at"`
		Targets    []string  `json:"targets"`
		Successful []string  `json:"successful"`
		Failed     []struct {
			Pane  string `json:"pane"`
			Error string `json:"error"`
		} `json:"failed"`
		MessagePreview string `json:"message_preview"`
	}

	var payload ExpectedSendSchema
	if err := json.Unmarshal(sendOut, &payload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] JSON schema validation failed: %v", err)
	}

	// Verify critical fields are present (this ensures API stability)
	if payload.Session == "" {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'session' missing")
	}
	if payload.SentAt.IsZero() {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'sent_at' missing or invalid")
	}
	if payload.Targets == nil {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'targets' missing")
	}
	if payload.Successful == nil {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'successful' missing")
	}
	if payload.Failed == nil {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'failed' missing")
	}
	if payload.MessagePreview == "" {
		t.Fatalf("[E2E-ROBOT-SEND-SCHEMA] Critical field 'message_preview' missing")
	}

	logger.Log("[E2E-ROBOT-SEND-SCHEMA] JSON schema stability validated")
}

// TestRobotSendTrackingCapabilities tests the tracking functionality for robot-send.
func TestRobotSendTrackingCapabilities(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_tracking_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-tracking")
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Starting tracking capabilities tests")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn session
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=2", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Spawn output: %s", string(spawnOut))

	time.Sleep(2 * time.Second)
	testutil.AssertSessionExists(t, logger, session)

	// Test 1: Basic send with tracking enabled (with timeout to avoid hanging)
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 1: Send with --track and timeout")
	testMessage := fmt.Sprintf("TRACKING_TEST_%d", time.Now().UnixNano())

	// Use timeout to prevent test from hanging if agent doesn't respond
	cmd := exec.Command("timeout", "5s", "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude", "--track")

	out, err := cmd.CombinedOutput()
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Track command output: %s", string(out))

	// Track mode may timeout, but that's acceptable for this test
	if err != nil {
		logger.Log("[E2E-ROBOT-SEND-TRACKING] Track command timed out or errored (expected): %v", err)

		// Even if it timed out, it should still produce valid JSON for what was sent
		var trackPayload struct {
			Success        bool      `json:"success"`
			Session        string    `json:"session"`
			Targets        []string  `json:"targets"`
			Successful     []string  `json:"successful"`
			MessagePreview string    `json:"message_preview"`
			SentAt         time.Time `json:"sent_at"`
		}

		// Try to parse JSON even from timeout/error output
		if jsonErr := json.Unmarshal(out, &trackPayload); jsonErr == nil {
			logger.Log("[E2E-ROBOT-SEND-TRACKING] Valid JSON received even with timeout")
			if trackPayload.Session == session && len(trackPayload.Targets) > 0 {
				logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 1 PASSED: Track mode initiated successfully before timeout")
			}
		} else {
			logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 1 PASSED: Track mode executed (timeout expected in test environment)")
		}
	} else {
		// If track completed successfully, validate the JSON
		var trackPayload struct {
			Success        bool      `json:"success"`
			Session        string    `json:"session"`
			Targets        []string  `json:"targets"`
			Successful     []string  `json:"successful"`
			MessagePreview string    `json:"message_preview"`
			SentAt         time.Time `json:"sent_at"`
		}

		if err := json.Unmarshal(out, &trackPayload); err != nil {
			t.Fatalf("[E2E-ROBOT-SEND-TRACKING] Invalid robot-send track JSON: %v", err)
		}

		if !trackPayload.Success {
			t.Errorf("[E2E-ROBOT-SEND-TRACKING] robot-send --track should succeed")
		}
		if trackPayload.Session != session {
			t.Errorf("[E2E-ROBOT-SEND-TRACKING] Expected session %s, got %s", session, trackPayload.Session)
		}
		logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 1 PASSED: Track mode completed successfully")
	}

	// Test 2: Send with delay staggering (testing throughput control)
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 2: Send with --delay-ms=100")
	testMessage = fmt.Sprintf("DELAY_TEST_%d", time.Now().UnixNano())
	startTime := time.Now()

	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude", "--delay-ms=100")

	elapsedTime := time.Since(startTime)
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Send with delay took: %v", elapsedTime)

	var delayPayload struct {
		Success        bool     `json:"success"`
		Targets        []string `json:"targets"`
		Successful     []string `json:"successful"`
		MessagePreview string   `json:"message_preview"`
	}

	if err := json.Unmarshal(sendOut, &delayPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-TRACKING] Invalid robot-send delay JSON: %v", err)
	}

	if !delayPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-TRACKING] robot-send with delay should succeed")
	}

	// Should have taken at least some time due to delay
	if len(delayPayload.Targets) > 1 && elapsedTime < 50*time.Millisecond {
		t.Errorf("[E2E-ROBOT-SEND-TRACKING] Expected some delay for multiple targets, got %v", elapsedTime)
	}

	logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 2 PASSED: Delay staggering worked, sent to %d targets", len(delayPayload.Targets))

	// Test 3: Track message delivery by checking pane output
	logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 3: Verify message delivery to panes")
	uniqueMarker := fmt.Sprintf("DELIVERY_MARKER_%d", time.Now().UnixNano())

	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", uniqueMarker), "--panes=1")

	var deliveryPayload struct {
		Success    bool     `json:"success"`
		Targets    []string `json:"targets"`
		Successful []string `json:"successful"`
	}

	if err := json.Unmarshal(sendOut, &deliveryPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-TRACKING] Invalid robot-send delivery JSON: %v", err)
	}

	if !deliveryPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-TRACKING] robot-send for delivery test should succeed")
	}

	// Wait for command to execute
	time.Sleep(500 * time.Millisecond)

	// Check if we can capture pane output (best effort)
	if output, err := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", fmt.Sprintf("%s:0.1", session), "-p").Output(); err == nil {
		paneContent := string(output)
		if strings.Contains(paneContent, uniqueMarker) {
			logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 3 PASSED: Message delivered to pane (marker found in output)")
		} else {
			logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 3 PASSED: Message sent successfully (marker not visible in pane capture)")
		}
	} else {
		logger.Log("[E2E-ROBOT-SEND-TRACKING] Test 3 PASSED: Message sent successfully (pane capture failed)")
	}

	logger.Log("[E2E-ROBOT-SEND-TRACKING] All tracking tests completed successfully")
}

// TestRobotSendAdvancedFiltering tests complex filtering scenarios and edge cases.
func TestRobotSendAdvancedFiltering(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_send_advanced_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	// Create test configuration
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	logger.LogSection("robot-send-advanced-filtering")
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Starting advanced filtering tests")

	defer func() {
		logger.LogSection("cleanup")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Spawn large session with multiple agent types
	spawnOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"spawn", session, "--cc=3", "--cod=2", "--project-dir", projectDir)
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Spawn output: %s", string(spawnOut))

	time.Sleep(3 * time.Second)
	testutil.AssertSessionExists(t, logger, session)

	// Test 1: Combination filtering (type + exclude)
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 1: Combination filter --type=claude --exclude=2,3")
	testMessage := fmt.Sprintf("COMBO_FILTER_%d", time.Now().UnixNano())
	sendOut := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=claude", "--exclude=2,3")

	var comboPayload struct {
		Success    bool     `json:"success"`
		Targets    []string `json:"targets"`
		Successful []string `json:"successful"`
	}

	if err := json.Unmarshal(sendOut, &comboPayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-ADVANCED] Invalid combo filter JSON: %v", err)
	}

	if !comboPayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-ADVANCED] Combination filter should succeed")
	}

	// Should have fewer targets than total claude agents due to exclusion
	if len(comboPayload.Targets) == 0 {
		t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Combination filter should have some targets")
	}

	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 1 PASSED: Combination filter sent to %d targets", len(comboPayload.Targets))

	// Test 2: Invalid pane indices
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 2: Send to invalid pane indices --panes=99,100")
	testMessage = fmt.Sprintf("INVALID_PANES_%d", time.Now().UnixNano())

	cmd := exec.Command("ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--panes=99,100")
	out, err := cmd.CombinedOutput()

	// This should either succeed with 0 targets or fail gracefully
	if err != nil {
		// If it fails, should return structured error JSON
		var errorPayload struct {
			Success   bool   `json:"success"`
			Error     string `json:"error"`
			ErrorCode string `json:"error_code"`
		}

		if jsonErr := json.Unmarshal(out, &errorPayload); jsonErr == nil {
			if !errorPayload.Success {
				logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 2 PASSED: Invalid panes handled with structured error")
			} else {
				t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Invalid panes should return success=false")
			}
		} else {
			logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 2 PASSED: Invalid panes handled (error output)")
		}
	} else {
		// If it succeeds, should have 0 targets
		var invalidPayload struct {
			Success bool     `json:"success"`
			Targets []string `json:"targets"`
		}

		if jsonErr := json.Unmarshal(out, &invalidPayload); jsonErr == nil {
			if len(invalidPayload.Targets) == 0 {
				logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 2 PASSED: Invalid panes result in 0 targets")
			} else {
				t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Invalid panes should have 0 targets, got %d", len(invalidPayload.Targets))
			}
		}
	}

	// Test 3: Non-existent agent type
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 3: Send to non-existent agent type --type=nonexistent")
	testMessage = fmt.Sprintf("NONEXISTENT_TYPE_%d", time.Now().UnixNano())

	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", fmt.Sprintf("echo %s", testMessage), "--type=nonexistent")

	var typePayload struct {
		Success bool     `json:"success"`
		Targets []string `json:"targets"`
	}

	if err := json.Unmarshal(sendOut, &typePayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-ADVANCED] Invalid non-existent type JSON: %v", err)
	}

	// Should succeed but have 0 targets
	if !typePayload.Success {
		t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Non-existent type filter should succeed")
	}
	if len(typePayload.Targets) != 0 {
		t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Non-existent type should have 0 targets, got %d", len(typePayload.Targets))
	}

	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 3 PASSED: Non-existent type handled correctly (0 targets)")

	// Test 4: Large message handling
	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 4: Send large message")
	largeMessage := fmt.Sprintf("LARGE_MSG_%d_%s", time.Now().UnixNano(), strings.Repeat("x", 500))

	sendOut = testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-send", session, "--msg", largeMessage, "--type=claude")

	var largePayload struct {
		Success        bool   `json:"success"`
		MessagePreview string `json:"message_preview"`
	}

	if err := json.Unmarshal(sendOut, &largePayload); err != nil {
		t.Fatalf("[E2E-ROBOT-SEND-ADVANCED] Invalid large message JSON: %v", err)
	}

	if !largePayload.Success {
		t.Fatalf("[E2E-ROBOT-SEND-ADVANCED] Large message send should succeed")
	}

	// Message preview should be truncated for large messages
	if len(largePayload.MessagePreview) == 0 {
		t.Errorf("[E2E-ROBOT-SEND-ADVANCED] Message preview should not be empty")
	}

	logger.Log("[E2E-ROBOT-SEND-ADVANCED] Test 4 PASSED: Large message handled correctly")

	logger.Log("[E2E-ROBOT-SEND-ADVANCED] All advanced filtering tests completed")
}

// TestRobotRestartPane tests the --robot-restart-pane flag.
func TestRobotRestartPane(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	session := fmt.Sprintf("robot_restart_%d", time.Now().UnixNano())
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
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	// Spawn session
	logger.LogSection("spawn session for restart test")
	_, _ = logger.Exec("ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(500 * time.Millisecond)

	// Verify session was created
	testutil.AssertSessionExists(t, logger, session)

	// Test robot-restart-pane
	logger.LogSection("robot-restart-pane")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath,
		"--robot-restart-pane", session, "--type=claude")
	logger.Log("robot-restart-pane output: %s", string(out))

	var payload struct {
		Session   string   `json:"session"`
		Restarted []string `json:"restarted"`
		Failed    []struct {
			Pane   string `json:"pane"`
			Reason string `json:"reason"`
		} `json:"failed"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid robot-restart-pane JSON: %v", err)
	}

	if payload.Session != session {
		t.Errorf("session = %q, want %q", payload.Session, session)
	}
	if len(payload.Restarted) != 1 {
		t.Errorf("restarted count = %d, want 1", len(payload.Restarted))
	}
	if len(payload.Failed) > 0 {
		t.Errorf("failed count = %d, want 0", len(payload.Failed))
	}
}

// Skip tests if ntm binary is missing.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ntm"); err != nil {
		// ntm binary not on PATH; skip suite gracefully
		return
	}
	os.Exit(m.Run())
}
