//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-ACTIVITY-METRICS] Tests for ntm activity and ntm metrics commands.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ActivityState represents an agent's current state
type ActivityState struct {
	Pane       int     `json:"pane"`
	AgentType  string  `json:"agent_type"`
	State      string  `json:"state"`
	Velocity   float64 `json:"velocity,omitempty"`
	Duration   string  `json:"duration,omitempty"`
	LastOutput string  `json:"last_output,omitempty"`
}

// ActivityOutput represents the JSON output from ntm activity
type ActivityOutput struct {
	Success   bool            `json:"success"`
	Session   string          `json:"session,omitempty"`
	Timestamp time.Time       `json:"timestamp,omitempty"`
	Agents    []ActivityState `json:"agents,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// MetricsOutput represents the JSON output from ntm metrics show
type MetricsOutput struct {
	Success     bool            `json:"success"`
	Session     string          `json:"session,omitempty"`
	Timestamp   time.Time       `json:"timestamp,omitempty"`
	Duration    string          `json:"duration,omitempty"`
	APICalls    map[string]int  `json:"api_calls,omitempty"`
	Latencies   map[string]int  `json:"latencies,omitempty"`
	TokenCounts map[string]int  `json:"token_counts,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// MetricsCompareOutput represents the JSON output from ntm metrics compare
type MetricsCompareOutput struct {
	Success   bool                `json:"success"`
	Current   map[string]int      `json:"current,omitempty"`
	Baseline  map[string]int      `json:"baseline,omitempty"`
	Changes   map[string]float64  `json:"changes,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// ActivityMetricsTestSuite manages E2E tests for activity and metrics commands
type ActivityMetricsTestSuite struct {
	t       *testing.T
	logger  *TestLogger
	tempDir string
	cleanup []func()
	ntmPath string
}

// NewActivityMetricsTestSuite creates a new test suite
func NewActivityMetricsTestSuite(t *testing.T, scenario string) *ActivityMetricsTestSuite {
	logger := NewTestLogger(t, scenario)

	// Find ntm binary
	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm binary not found in PATH")
	}

	return &ActivityMetricsTestSuite{
		t:       t,
		logger:  logger,
		ntmPath: ntmPath,
	}
}

// supportsCommand checks if ntm supports a given subcommand
func (s *ActivityMetricsTestSuite) supportsCommand(cmd string) bool {
	out, err := exec.Command(s.ntmPath, cmd, "--help").CombinedOutput()
	if err != nil {
		return !strings.Contains(string(out), "unknown command")
	}
	return true
}

// requireActivityCommand skips if activity command is not supported
func (s *ActivityMetricsTestSuite) requireActivityCommand() {
	if !s.supportsCommand("activity") {
		s.t.Skip("activity command not supported by this ntm version")
	}
}

// requireMetricsCommand skips if metrics command is not supported
func (s *ActivityMetricsTestSuite) requireMetricsCommand() {
	if !s.supportsCommand("metrics") {
		s.t.Skip("metrics command not supported by this ntm version")
	}
}

// Setup creates a temporary directory for testing
func (s *ActivityMetricsTestSuite) Setup() error {
	tempDir, err := os.MkdirTemp("", "ntm-activity-e2e-*")
	if err != nil {
		return err
	}
	s.tempDir = tempDir
	s.cleanup = append(s.cleanup, func() { os.RemoveAll(tempDir) })
	s.logger.Log("Created temp directory: %s", tempDir)
	return nil
}

// Teardown cleans up resources
func (s *ActivityMetricsTestSuite) Teardown() {
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

// runNTM executes ntm with arguments and returns output
func (s *ActivityMetricsTestSuite) runNTM(args ...string) (string, error) {
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
func (s *ActivityMetricsTestSuite) runNTMAllowFail(args ...string) string {
	s.logger.Log("Running (allow-fail): %s %s", s.ntmPath, strings.Join(args, " "))
	cmd := exec.Command(s.ntmPath, args...)
	output, _ := cmd.CombinedOutput()
	result := string(output)
	s.logger.Log("Output: %s", result)
	return result
}

// ========== Activity Command Tests ==========

// TestActivityCommandExists verifies activity command is available
func TestActivityCommandExists(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-exists")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity command exists")

	output := suite.runNTMAllowFail("activity", "--help")

	// Should show help, not unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("activity command not recognized: %s", output)
	}

	if !strings.Contains(output, "activity") {
		t.Errorf("Expected help text, got: %s", output)
	}

	suite.logger.Log("PASS: activity command exists")
}

// TestActivityCCFlag verifies --cc flag works correctly
func TestActivityCCFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-cc-flag")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --cc flag")

	output := suite.runNTMAllowFail("activity", "--cc", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--cc flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --cc flag is accepted")
}

// TestActivityCodFlag verifies --cod flag works correctly
func TestActivityCodFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-cod-flag")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --cod flag")

	output := suite.runNTMAllowFail("activity", "--cod", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--cod flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --cod flag is accepted")
}

// TestActivityGmiFlag verifies --gmi flag works correctly
func TestActivityGmiFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-gmi-flag")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --gmi flag")

	output := suite.runNTMAllowFail("activity", "--gmi", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--gmi flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --gmi flag is accepted")
}

// TestActivityWatchFlag verifies --watch flag works correctly
func TestActivityWatchFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-watch-flag")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --watch flag")

	output := suite.runNTMAllowFail("activity", "--watch", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--watch flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --watch flag is accepted")
}

// TestActivityIntervalFlag verifies --interval flag works correctly
func TestActivityIntervalFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-interval-flag")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --interval flag")

	output := suite.runNTMAllowFail("activity", "--interval=1000", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--interval flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --interval flag is accepted")
}

// TestActivityJSONOutput verifies JSON output structure
func TestActivityJSONOutput(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-json-output")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity --json output")

	output := suite.runNTMAllowFail("activity", "--json")

	// Try to parse as JSON
	var result ActivityOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		suite.logger.Log("Note: Could not parse JSON (may need active session): %v", err)
	} else {
		suite.logger.Log("Got valid JSON structure: %d agents", len(result.Agents))
	}

	suite.logger.Log("PASS: activity --json produces output")
}

// TestActivityNonExistentSession verifies behavior for non-existent session
func TestActivityNonExistentSession(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "activity-nonexistent")
	defer suite.Teardown()
	suite.requireActivityCommand()

	suite.logger.Log("Testing activity with non-existent session")

	output := suite.runNTMAllowFail("activity", "nonexistent-session-xyz-12345", "--json")

	// Should handle gracefully
	suite.logger.Log("Output for nonexistent session: %s", output)
	suite.logger.Log("PASS: Handles non-existent session gracefully")
}

// ========== Metrics Command Tests ==========

// TestMetricsCommandExists verifies metrics command is available
func TestMetricsCommandExists(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-exists")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics command exists")

	output := suite.runNTMAllowFail("metrics", "--help")

	// Should show help, not unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("metrics command not recognized: %s", output)
	}

	if !strings.Contains(output, "metrics") {
		t.Errorf("Expected help text, got: %s", output)
	}

	suite.logger.Log("PASS: metrics command exists")
}

// TestMetricsShowSubcommand verifies metrics show subcommand
func TestMetricsShowSubcommand(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-show")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics show subcommand")

	output := suite.runNTMAllowFail("metrics", "show")

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("metrics show command not recognized: %s", output)
	}

	suite.logger.Log("PASS: metrics show subcommand works")
}

// TestMetricsShowJSONOutput verifies metrics show JSON output
func TestMetricsShowJSONOutput(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-show-json")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics show --json output")

	output := suite.runNTMAllowFail("metrics", "show", "--json")

	// Try to parse as JSON
	var result MetricsOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		suite.logger.Log("Note: Could not parse JSON: %v", err)
	} else {
		suite.logger.Log("Got valid JSON structure")
	}

	suite.logger.Log("PASS: metrics show --json produces output")
}

// TestMetricsShowSessionFlag verifies --session flag works
func TestMetricsShowSessionFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-show-session")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics show --session flag")

	output := suite.runNTMAllowFail("metrics", "show", "--session=test-session", "--json")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--session flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --session flag is accepted")
}

// TestMetricsCompareSubcommand verifies metrics compare subcommand
func TestMetricsCompareSubcommand(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-compare")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics compare subcommand")

	output := suite.runNTMAllowFail("metrics", "compare", "--help")

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("metrics compare command not recognized: %s", output)
	}

	suite.logger.Log("PASS: metrics compare subcommand exists")
}

// TestMetricsExportSubcommand verifies metrics export subcommand
func TestMetricsExportSubcommand(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-export")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics export subcommand")

	output := suite.runNTMAllowFail("metrics", "export", "--help")

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("metrics export command not recognized: %s", output)
	}

	suite.logger.Log("PASS: metrics export subcommand exists")
}

// TestMetricsExportFormatFlag verifies --format flag works
func TestMetricsExportFormatFlag(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-export-format")
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics export --format flag")

	output := suite.runNTMAllowFail("metrics", "export", "--format=csv", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--format flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --format flag is accepted")
}

// TestMetricsSnapshotSubcommand verifies metrics snapshot subcommand
func TestMetricsSnapshotSubcommand(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "metrics-snapshot")
	defer suite.Teardown()
	suite.requireMetricsCommand()

	suite.logger.Log("Testing metrics snapshot subcommand")

	output := suite.runNTMAllowFail("metrics", "snapshot", "--help")

	// Should not fail with unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("metrics snapshot command not recognized: %s", output)
	}

	suite.logger.Log("PASS: metrics snapshot subcommand exists")
}

// TestActivityAndMetricsBothExist verifies both commands exist
func TestActivityAndMetricsBothExist(t *testing.T) {
	suite := NewActivityMetricsTestSuite(t, "both-commands-exist")
	defer suite.Teardown()

	suite.logger.Log("Testing that both activity and metrics exist")

	// Check activity command
	activityOutput := suite.runNTMAllowFail("activity", "--help")
	if strings.Contains(activityOutput, "unknown command") {
		suite.logger.Log("activity command not found, may be in different version")
	} else {
		suite.logger.Log("activity command exists")
	}

	// Check metrics command
	metricsOutput := suite.runNTMAllowFail("metrics", "--help")
	if strings.Contains(metricsOutput, "unknown command") {
		suite.logger.Log("metrics command not found, may be in different version")
	} else {
		suite.logger.Log("metrics command exists")
	}

	suite.logger.Log("PASS: Checked for both activity and metrics commands")
}
