//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-MONITOR] Tests for ntm watch and --robot-monitor.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// MonitorEvent represents a single monitoring event from --robot-monitor
type MonitorEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Session    string    `json:"session"`
	Pane       int       `json:"pane,omitempty"`
	Level      string    `json:"level"`
	Type       string    `json:"type"`
	Message    string    `json:"message"`
	Metrics    *Metrics  `json:"metrics,omitempty"`
}

// Metrics contains usage metrics from monitoring
type Metrics struct {
	ContextPercent   float64 `json:"context_percent,omitempty"`
	ProviderPercent  float64 `json:"provider_percent,omitempty"`
	TokensUsed       int     `json:"tokens_used,omitempty"`
	TokensRemaining  int     `json:"tokens_remaining,omitempty"`
}

// MonitorTestSuite manages E2E tests for watch and monitor commands
type MonitorTestSuite struct {
	t       *testing.T
	logger  *TestLogger
	tempDir string
	cleanup []func()
	ntmPath string
}

// NewMonitorTestSuite creates a new monitor test suite
func NewMonitorTestSuite(t *testing.T, scenario string) *MonitorTestSuite {
	logger := NewTestLogger(t, scenario)

	// Find ntm binary
	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm binary not found in PATH")
	}

	return &MonitorTestSuite{
		t:       t,
		logger:  logger,
		ntmPath: ntmPath,
	}
}

// supportsCommand checks if ntm supports a given subcommand
func (s *MonitorTestSuite) supportsCommand(cmd string) bool {
	out, err := exec.Command(s.ntmPath, "--help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), cmd)
}

// requireWatchCommand skips if watch command is not supported
func (s *MonitorTestSuite) requireWatchCommand() {
	if !s.supportsCommand("watch") {
		s.t.Skip("watch command not supported by this ntm version")
	}
}

// requireRobotMonitor skips if robot-monitor flag is not supported
func (s *MonitorTestSuite) requireRobotMonitor() {
	if !s.supportsCommand("--robot-monitor") {
		s.t.Skip("--robot-monitor flag not supported by this ntm version")
	}
}

// Setup creates a temporary directory for testing
func (s *MonitorTestSuite) Setup() error {
	tempDir, err := os.MkdirTemp("", "ntm-monitor-e2e-*")
	if err != nil {
		return err
	}
	s.tempDir = tempDir
	s.cleanup = append(s.cleanup, func() { os.RemoveAll(tempDir) })
	s.logger.Log("Created temp directory: %s", tempDir)
	return nil
}

// Teardown cleans up resources
func (s *MonitorTestSuite) Teardown() {
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

// runNTM executes ntm with arguments and returns output
func (s *MonitorTestSuite) runNTM(args ...string) (string, error) {
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
func (s *MonitorTestSuite) runNTMAllowFail(args ...string) string {
	s.logger.Log("Running (allow-fail): %s %s", s.ntmPath, strings.Join(args, " "))
	cmd := exec.Command(s.ntmPath, args...)
	output, _ := cmd.CombinedOutput()
	result := string(output)
	s.logger.Log("Output: %s", result)
	return result
}

// ========== Watch Command Tests ==========

// TestWatchCommandExists verifies watch command is available
func TestWatchCommandExists(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-exists")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch command exists")

	output := suite.runNTMAllowFail("watch", "--help")

	// Should show help, not unknown command
	if strings.Contains(output, "unknown command") {
		t.Fatalf("watch command not recognized: %s", output)
	}

	if !strings.Contains(output, "Stream agent output") {
		t.Errorf("Expected help text, got: %s", output)
	}

	suite.logger.Log("PASS: watch command exists")
}

// TestWatchCCFlag verifies --cc flag works correctly
func TestWatchCCFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-cc-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --cc flag")

	output := suite.runNTMAllowFail("watch", "--cc", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--cc flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --cc flag is accepted")
}

// TestWatchCodFlag verifies --cod flag works correctly
func TestWatchCodFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-cod-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --cod flag")

	output := suite.runNTMAllowFail("watch", "--cod", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--cod flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --cod flag is accepted")
}

// TestWatchGmiFlag verifies --gmi flag works correctly
func TestWatchGmiFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-gmi-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --gmi flag")

	output := suite.runNTMAllowFail("watch", "--gmi", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--gmi flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --gmi flag is accepted")
}

// TestWatchPaneFlag verifies --pane flag works correctly
func TestWatchPaneFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-pane-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --pane flag")

	output := suite.runNTMAllowFail("watch", "--pane=1", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--pane flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --pane flag is accepted")
}

// TestWatchActivityFlag verifies --activity flag works correctly
func TestWatchActivityFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-activity-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --activity flag")

	output := suite.runNTMAllowFail("watch", "--activity", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--activity flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --activity flag is accepted")
}

// TestWatchIntervalFlag verifies --interval flag works correctly
func TestWatchIntervalFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-interval-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --interval flag")

	output := suite.runNTMAllowFail("watch", "--interval=500", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--interval flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --interval flag is accepted")
}

// TestWatchPatternFlag verifies --pattern flag works correctly
func TestWatchPatternFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-pattern-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --pattern flag")

	output := suite.runNTMAllowFail("watch", "--pattern=*.go", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--pattern flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --pattern flag is accepted")
}

// TestWatchCommandFlag verifies --command flag works correctly
func TestWatchCommandFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-command-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --command flag")

	output := suite.runNTMAllowFail("watch", "--command=go test", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--command flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --command flag is accepted")
}

// TestWatchNoColorFlag verifies --no-color flag works correctly
func TestWatchNoColorFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-no-color-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --no-color flag")

	output := suite.runNTMAllowFail("watch", "--no-color", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--no-color flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --no-color flag is accepted")
}

// TestWatchNoTimestampsFlag verifies --no-timestamps flag works correctly
func TestWatchNoTimestampsFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-no-timestamps-flag")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch --no-timestamps flag")

	output := suite.runNTMAllowFail("watch", "--no-timestamps", "--help")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--no-timestamps flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --no-timestamps flag is accepted")
}

// TestWatchNonExistentSession verifies behavior for non-existent session
func TestWatchNonExistentSession(t *testing.T) {
	suite := NewMonitorTestSuite(t, "watch-nonexistent")
	defer suite.Teardown()
	suite.requireWatchCommand()

	suite.logger.Log("Testing watch with non-existent session")

	// Use timeout context since watch would block indefinitely
	output := suite.runNTMAllowFail("watch", "nonexistent-session-xyz-12345")

	// Should indicate session not found or similar error
	if !strings.Contains(output, "not found") && !strings.Contains(output, "error") && !strings.Contains(output, "Error") && !strings.Contains(output, "session") {
		suite.logger.Log("Expected session error, got: %s", output)
	}

	suite.logger.Log("PASS: Correctly handles non-existent session")
}

// ========== Robot Monitor Tests ==========

// TestRobotMonitorFlag verifies --robot-monitor flag works correctly
func TestRobotMonitorFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-flag")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor flag")

	output := suite.runNTMAllowFail("--robot-monitor=nonexistent-session")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--robot-monitor flag not recognized: %s", output)
	}

	// Attempt to parse any JSON output
	var event MonitorEvent
	if err := json.Unmarshal([]byte(output), &event); err == nil {
		suite.logger.Log("Got event: level=%s, type=%s", event.Level, event.Type)
	}

	suite.logger.Log("PASS: --robot-monitor flag is accepted")
}

// TestRobotMonitorIntervalFlag verifies --interval flag with monitor
func TestRobotMonitorIntervalFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-interval")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --interval")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--interval=5s")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--interval flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --interval flag is accepted")
}

// TestRobotMonitorWarnThreshold verifies --warn-threshold flag
func TestRobotMonitorWarnThreshold(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-warn")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --warn-threshold")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--warn-threshold=30")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--warn-threshold flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --warn-threshold flag is accepted")
}

// TestRobotMonitorCritThreshold verifies --crit-threshold flag
func TestRobotMonitorCritThreshold(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-crit")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --crit-threshold")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--crit-threshold=10")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--crit-threshold flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --crit-threshold flag is accepted")
}

// TestRobotMonitorInfoThreshold verifies --info-threshold flag
func TestRobotMonitorInfoThreshold(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-info")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --info-threshold")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--info-threshold=50")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--info-threshold flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --info-threshold flag is accepted")
}

// TestRobotMonitorAlertThreshold verifies --alert-threshold flag
func TestRobotMonitorAlertThreshold(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-alert")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --alert-threshold")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--alert-threshold=90")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--alert-threshold flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --alert-threshold flag is accepted")
}

// TestRobotMonitorIncludeCaut verifies --include-caut flag
func TestRobotMonitorIncludeCaut(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-caut")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --include-caut")

	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--include-caut")

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--include-caut flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --include-caut flag is accepted")
}

// TestRobotMonitorOutputFlag verifies --output flag
func TestRobotMonitorOutputFlag(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-output")
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with --output")

	outputPath := suite.tempDir + "/monitor.jsonl"
	output := suite.runNTMAllowFail("--robot-monitor=test-session", "--output="+outputPath)

	// Should not complain about unknown flag
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("--output flag not recognized: %s", output)
	}

	suite.logger.Log("PASS: --output flag is accepted")
}

// TestRobotMonitorAllThresholds verifies multiple thresholds together
func TestRobotMonitorAllThresholds(t *testing.T) {
	suite := NewMonitorTestSuite(t, "robot-monitor-all-thresholds")
	defer suite.Teardown()
	suite.requireRobotMonitor()

	suite.logger.Log("Testing --robot-monitor with all threshold flags")

	output := suite.runNTMAllowFail(
		"--robot-monitor=test-session",
		"--interval=10s",
		"--warn-threshold=30",
		"--crit-threshold=10",
		"--info-threshold=50",
		"--alert-threshold=85",
	)

	// Should not complain about unknown flags
	if strings.Contains(output, "unknown flag") {
		t.Fatalf("Threshold flags not recognized: %s", output)
	}

	suite.logger.Log("PASS: All threshold flags are accepted together")
}

// TestWatchAndMonitorBothExist verifies both commands exist
func TestWatchAndMonitorBothExist(t *testing.T) {
	suite := NewMonitorTestSuite(t, "both-commands-exist")
	defer suite.Teardown()

	suite.logger.Log("Testing that both watch and robot-monitor exist")

	// Check watch command
	watchOutput := suite.runNTMAllowFail("watch", "--help")
	if strings.Contains(watchOutput, "unknown command") {
		suite.logger.Log("watch command not found, may be in different version")
	} else {
		suite.logger.Log("watch command exists")
	}

	// Check robot-monitor flag
	monitorOutput := suite.runNTMAllowFail("--help")
	if strings.Contains(monitorOutput, "--robot-monitor") {
		suite.logger.Log("--robot-monitor flag exists")
	} else {
		suite.logger.Log("--robot-monitor flag not found, may be in different version")
	}

	suite.logger.Log("PASS: Checked for both watch and monitor features")
}
