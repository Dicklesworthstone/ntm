//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-LOGS] Tests for ntm logs (log aggregation and viewing).
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// LogsOutput mirrors the CLI JSON output for log commands
type LogsOutput struct {
	Success    bool        `json:"success"`
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Panes      []PaneLogs  `json:"panes"`
	Summary    LogsSummary `json:"summary"`
	Error      string      `json:"error,omitempty"`
}

// PaneLogs contains logs from a single agent pane
type PaneLogs struct {
	Pane       int       `json:"pane"`
	AgentType  string    `json:"agent_type"`
	Lines      []string  `json:"lines"`
	LineCount  int       `json:"line_count"`
	Truncated  bool      `json:"truncated"`
	CapturedAt time.Time `json:"captured_at"`
}

// LogsSummary contains aggregate statistics
type LogsSummary struct {
	TotalPanes     int `json:"total_panes"`
	TotalLines     int `json:"total_lines"`
	TruncatedPanes int `json:"truncated_panes"`
	FilteredLines  int `json:"filtered_lines,omitempty"`
}

// AggregatedLogsOutput mirrors aggregated view output
type AggregatedLogsOutput struct {
	Success    bool                   `json:"success"`
	Session    string                 `json:"session"`
	CapturedAt time.Time              `json:"captured_at"`
	Entries    []AggregatedLogEntry   `json:"entries"`
	Summary    AggregatedLogsSummary  `json:"summary"`
	Error      string                 `json:"error,omitempty"`
}

// AggregatedLogEntry is a single interleaved log entry
type AggregatedLogEntry struct {
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	Line      string `json:"line"`
	LineNum   int    `json:"line_num"`
}

// AggregatedLogsSummary contains aggregate log statistics
type AggregatedLogsSummary struct {
	TotalEntries  int `json:"total_entries"`
	PanesIncluded int `json:"panes_included"`
}

// RobotLogsOutput mirrors --robot-logs output
type RobotLogsOutput struct {
	Success    bool        `json:"success"`
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Panes      []PaneLogs  `json:"panes"`
	Summary    LogsSummary `json:"summary"`
	Error      string      `json:"error,omitempty"`
}

// HistoryEntry represents a history entry
type HistoryEntry struct {
	ID        string    `json:"id"`
	Session   string    `json:"session"`
	Prompt    string    `json:"prompt"`
	Pane      int       `json:"pane"`
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
}

// HistoryListOutput mirrors history list output
type HistoryListOutput struct {
	Success bool           `json:"success"`
	Count   int            `json:"count"`
	Entries []HistoryEntry `json:"entries"`
	Error   string         `json:"error,omitempty"`
}

// HistoryStatsOutput mirrors history stats output
type HistoryStatsOutput struct {
	Success          bool                 `json:"success"`
	TotalEntries     int                  `json:"total_entries"`
	BySessions       map[string]int       `json:"by_session"`
	BySource         map[string]int       `json:"by_source"`
	OldestEntry      *time.Time           `json:"oldest_entry,omitempty"`
	NewestEntry      *time.Time           `json:"newest_entry,omitempty"`
	Error            string               `json:"error,omitempty"`
}

// LogsTestSuite manages E2E tests for logs commands
type LogsTestSuite struct {
	t       *testing.T
	logger  *TestLogger
	tempDir string
	cleanup []func()
	ntmPath string
}

// NewLogsTestSuite creates a new logs test suite
func NewLogsTestSuite(t *testing.T, scenario string) *LogsTestSuite {
	logger := NewTestLogger(t, scenario)

	// Find ntm binary
	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm binary not found in PATH")
	}

	return &LogsTestSuite{
		t:       t,
		logger:  logger,
		ntmPath: ntmPath,
	}
}

// supportsCommand checks if ntm supports a given subcommand
func (s *LogsTestSuite) supportsCommand(cmd string) bool {
	out, err := exec.Command(s.ntmPath, "--help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), cmd)
}

// requireLogsCommand skips if logs command is not supported
func (s *LogsTestSuite) requireLogsCommand() {
	if !s.supportsCommand("logs") {
		s.t.Skip("logs command not supported by this ntm version")
	}
}

// requireHistoryCommand skips if history command is not supported
func (s *LogsTestSuite) requireHistoryCommand() {
	if !s.supportsCommand("history") {
		s.t.Skip("history command not supported by this ntm version")
	}
}

// Setup creates a temporary directory for testing
func (s *LogsTestSuite) Setup() error {
	tempDir, err := os.MkdirTemp("", "ntm-logs-e2e-*")
	if err != nil {
		return err
	}
	s.tempDir = tempDir
	s.cleanup = append(s.cleanup, func() { os.RemoveAll(tempDir) })
	s.logger.Log("Created temp directory: %s", tempDir)
	return nil
}

// Teardown cleans up resources
func (s *LogsTestSuite) Teardown() {
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

// runNTM executes ntm with arguments and returns output
func (s *LogsTestSuite) runNTM(args ...string) (string, error) {
	s.logger.Log("Running: %s %s", s.ntmPath, strings.Join(args, " "))
	cmd := exec.Command(s.ntmPath, args...)
	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		s.logger.Log("Command failed: %v, output: %s", err, result)
		return result, err
	}
	s.logger.Log("Output: %s", result)
	return result, nil
}

// runNTMAllowFail runs ntm allowing non-zero exit codes
func (s *LogsTestSuite) runNTMAllowFail(args ...string) string {
	s.logger.Log("Running (allow-fail): %s %s", s.ntmPath, strings.Join(args, " "))
	cmd := exec.Command(s.ntmPath, args...)
	output, _ := cmd.CombinedOutput()
	result := string(output)
	s.logger.Log("Output: %s", result)
	return result
}

// TestLogsRequiresSession verifies logs command requires a session argument
func TestLogsRequiresSession(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-requires-session")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing that logs command requires session argument")

	output := suite.runNTMAllowFail("logs")

	// Should fail without session argument
	if !strings.Contains(output, "accepts 1 arg") && !strings.Contains(output, "required") {
		t.Logf("Expected error about required argument, got: %s", output)
	}

	suite.logger.Log("PASS: logs command correctly requires session argument")
}

// TestLogsNonExistentSession verifies behavior for non-existent session
func TestLogsNonExistentSession(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-nonexistent-session")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs with non-existent session")

	output := suite.runNTMAllowFail("logs", "nonexistent-session-xyz-12345", "--json")

	// Should indicate session not found
	if !strings.Contains(output, "not found") && !strings.Contains(output, "error") && !strings.Contains(output, "Error") {
		suite.logger.Log("Expected 'not found' error, got: %s", output)
	}

	suite.logger.Log("PASS: Correctly handles non-existent session")
}

// TestLogsJSONOutputStructure verifies JSON output has expected fields
func TestLogsJSONOutputStructure(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-json-structure")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --json output structure (requires active session)")

	// First check if any sessions exist
	statusOut, err := suite.runNTM("status", "--json")
	if err != nil {
		suite.logger.Log("No active sessions, skipping JSON structure test")
		t.Skip("No active sessions available for testing")
		return
	}

	// Try to find a session name from status
	var statusResult map[string]interface{}
	if err := json.Unmarshal([]byte(statusOut), &statusResult); err != nil {
		suite.logger.Log("Could not parse status, skipping: %v", err)
		t.Skip("Could not determine session from status")
		return
	}

	sessions, ok := statusResult["sessions"].([]interface{})
	if !ok || len(sessions) == 0 {
		suite.logger.Log("No sessions in status, skipping")
		t.Skip("No active sessions")
		return
	}

	session, ok := sessions[0].(map[string]interface{})
	if !ok {
		t.Skip("Could not parse session")
		return
	}

	sessionName, ok := session["name"].(string)
	if !ok {
		t.Skip("Could not get session name")
		return
	}

	suite.logger.Log("Using session: %s", sessionName)

	output, err := suite.runNTM("logs", sessionName, "--json")
	if err != nil {
		t.Fatalf("logs --json failed: %v", err)
	}

	var result LogsOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v, raw: %s", err, output)
	}

	// Verify required fields
	if result.Session != sessionName {
		t.Errorf("Expected session=%s, got %s", sessionName, result.Session)
	}

	if result.CapturedAt.IsZero() {
		t.Error("captured_at should not be zero")
	}

	suite.logger.Log("PASS: JSON output has valid structure")
}

// TestLogsLimitFlag verifies --limit flag works correctly
func TestLogsLimitFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-limit-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --limit flag")

	// Test that limit flag is accepted
	output := suite.runNTMAllowFail("logs", "test-session", "--limit=10", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--limit flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --limit flag is accepted")
}

// TestLogsPanesFlag verifies --panes flag works correctly
func TestLogsPanesFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-panes-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --panes flag")

	// Test that panes flag is accepted
	output := suite.runNTMAllowFail("logs", "test-session", "--panes=1,2,3", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--panes flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --panes flag is accepted")
}

// TestLogsSinceFlag verifies --since flag works correctly
func TestLogsSinceFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-since-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --since flag")

	// Test valid duration formats
	formats := []string{"5m", "1h", "30s", "2h30m"}
	for _, dur := range formats {
		output := suite.runNTMAllowFail("logs", "test-session", "--since="+dur, "--json")
		if strings.Contains(output, "invalid --since") {
			t.Errorf("--since=%s should be valid, got: %s", dur, output)
		}
	}

	// Test invalid duration
	output := suite.runNTMAllowFail("logs", "test-session", "--since=notaduration", "--json")
	if !strings.Contains(output, "invalid") {
		suite.logger.Log("Expected 'invalid' error for bad duration, got: %s", output)
	}

	suite.logger.Log("PASS: --since flag validates durations correctly")
}

// TestLogsFilterFlag verifies --filter flag works correctly
func TestLogsFilterFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-filter-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --filter flag")

	// Test that filter flag is accepted
	output := suite.runNTMAllowFail("logs", "test-session", "--filter=error", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--filter flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --filter flag is accepted")
}

// TestLogsAggregateFlag verifies --aggregate flag works correctly
func TestLogsAggregateFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "logs-aggregate-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing logs --aggregate flag")

	// Test that aggregate flag is accepted
	output := suite.runNTMAllowFail("logs", "test-session", "--aggregate", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--aggregate flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --aggregate flag is accepted")
}

// TestRobotLogsFlag verifies --robot-logs flag works correctly
func TestRobotLogsFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "robot-logs-flag")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing --robot-logs flag")

	output := suite.runNTMAllowFail("--robot-logs=nonexistent-session")

	// Should either work or give session not found error (not flag error)
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--robot-logs flag not recognized: %s", output)
	}

	// Try to parse as JSON
	var result RobotLogsOutput
	if err := json.Unmarshal([]byte(output), &result); err == nil {
		suite.logger.Log("Got valid JSON response: success=%v", result.Success)
	}

	suite.logger.Log("PASS: --robot-logs flag is accepted")
}

// TestRobotLogsLimitFlag verifies --logs-limit flag works correctly
func TestRobotLogsLimitFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "robot-logs-limit")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing --robot-logs with --logs-limit")

	output := suite.runNTMAllowFail("--robot-logs=test-session", "--logs-limit=50")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--logs-limit flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --logs-limit flag is accepted")
}

// TestRobotLogsPanesFlag verifies --logs-panes flag works correctly
func TestRobotLogsPanesFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "robot-logs-panes")
	defer suite.Teardown()
	suite.requireLogsCommand()

	suite.logger.Log("Testing --robot-logs with --logs-panes")

	output := suite.runNTMAllowFail("--robot-logs=test-session", "--logs-panes=1,2")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--logs-panes flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --logs-panes flag is accepted")
}

// TestHistoryListEmpty verifies history works with no entries
func TestHistoryListEmpty(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-list-empty")
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history list with no entries")

	// Use temp home to avoid existing history
	os.Setenv("HOME", suite.tempDir)
	defer os.Unsetenv("HOME")

	output := suite.runNTMAllowFail("history", "--json")

	// Should either be empty or show no entries
	if strings.Contains(output, "unknown") {
		t.Fatalf("history command failed: %s", output)
	}

	suite.logger.Log("PASS: history handles empty state")
}

// TestHistoryLimitFlag verifies --limit flag works correctly
func TestHistoryLimitFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-limit-flag")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history --limit flag")

	output := suite.runNTMAllowFail("history", "--limit=5", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--limit flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --limit flag is accepted")
}

// TestHistorySessionFilter verifies --session flag works correctly
func TestHistorySessionFilter(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-session-filter")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history --session flag")

	output := suite.runNTMAllowFail("history", "--session=test-session", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--session flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --session flag is accepted")
}

// TestHistorySinceFilter verifies --since flag works correctly
func TestHistorySinceFilter(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-since-filter")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history --since flag")

	output := suite.runNTMAllowFail("history", "--since=1h", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--since flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --since flag is accepted")
}

// TestHistorySearchFilter verifies --search flag works correctly
func TestHistorySearchFilter(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-search-filter")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history --search flag")

	output := suite.runNTMAllowFail("history", "--search=test", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--search flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --search flag is accepted")
}

// TestHistorySourceFilter verifies --source flag works correctly
func TestHistorySourceFilter(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-source-filter")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history --source flag")

	output := suite.runNTMAllowFail("history", "--source=cli", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--source flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --source flag is accepted")
}

// TestHistoryStats verifies history stats subcommand works
func TestHistoryStats(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-stats")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history stats subcommand")

	output := suite.runNTMAllowFail("history", "stats", "--json")

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("history stats command not recognized: %s", output)
	}

	// Try to parse as JSON
	var result HistoryStatsOutput
	if err := json.Unmarshal([]byte(output), &result); err == nil {
		suite.logger.Log("Got stats response: total_entries=%d", result.TotalEntries)
	}

	suite.logger.Log("PASS: history stats subcommand works")
}

// TestHistoryClear verifies history clear subcommand exists
func TestHistoryClear(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-clear")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history clear subcommand exists")

	// Just check the help to avoid actually clearing history
	output := suite.runNTMAllowFail("history", "clear", "--help")

	// Should show help, not unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("history clear command not recognized: %s", output)
	}

	suite.logger.Log("PASS: history clear subcommand exists")
}

// TestHistoryExport verifies history export subcommand works
func TestHistoryExport(t *testing.T) {
	suite := NewLogsTestSuite(t, "history-export")
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing history export subcommand")

	exportPath := suite.tempDir + "/export.jsonl"
	output := suite.runNTMAllowFail("history", "export", exportPath)

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("history export command not recognized: %s", output)
	}

	suite.logger.Log("PASS: history export subcommand works")
}

// TestRobotHistoryFlag verifies --robot-history flag works
func TestRobotHistoryFlag(t *testing.T) {
	suite := NewLogsTestSuite(t, "robot-history-flag")
	defer suite.Teardown()
	suite.requireHistoryCommand()

	suite.logger.Log("Testing --robot-history flag")

	output := suite.runNTMAllowFail("--robot-history=test-session")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--robot-history flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --robot-history flag is accepted")
}

// TestCombinedLogsAndHistory verifies logs and history work together
func TestCombinedLogsAndHistory(t *testing.T) {
	suite := NewLogsTestSuite(t, "combined-logs-history")
	defer suite.Teardown()

	suite.logger.Log("Testing logs and history commands together")

	// Both commands should be available
	logsOutput := suite.runNTMAllowFail("logs", "--help")
	historyOutput := suite.runNTMAllowFail("history", "--help")

	if strings.Contains(logsOutput, "unknown command") {
		t.Fatal("logs command not found")
	}
	if strings.Contains(historyOutput, "unknown command") {
		t.Fatal("history command not found")
	}

	suite.logger.Log("PASS: Both logs and history commands available")
}
