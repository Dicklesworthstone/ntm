//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// These tests verify the complete system with real tmux sessions and agents.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	e2eNTMBinOnce sync.Once
	e2eNTMBinPath string
	e2eNTMBinErr  error
)

func TestMain(m *testing.M) {
	bin, err := ensureE2ENTMBin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "E2E: failed to resolve/build ntm: %v\n", err)
		os.Exit(1)
	}

	dir := filepath.Dir(bin)
	pathKey := "PATH"
	oldPath := os.Getenv(pathKey)
	newPath := dir + string(os.PathListSeparator) + oldPath
	if err := os.Setenv(pathKey, newPath); err != nil {
		fmt.Fprintf(os.Stderr, "E2E: failed to update PATH: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func ensureE2ENTMBin() (string, error) {
	e2eNTMBinOnce.Do(func() {
		// If explicitly provided, prefer it (useful for debugging).
		if override := os.Getenv("E2E_NTM_BIN"); override != "" {
			path := override
			if !strings.ContainsRune(path, filepath.Separator) {
				resolved, err := exec.LookPath(path)
				if err != nil {
					e2eNTMBinErr = fmt.Errorf("E2E_NTM_BIN=%q not found on PATH: %w", override, err)
					return
				}
				path = resolved
			}
			e2eNTMBinPath = path
			return
		}

		wd, err := os.Getwd()
		if err != nil {
			e2eNTMBinErr = fmt.Errorf("getwd: %w", err)
			return
		}
		repoRoot, err := findRepoRoot(wd)
		if err != nil {
			e2eNTMBinErr = err
			return
		}

		outDir, err := os.MkdirTemp("", "ntm-e2e-bin-*")
		if err != nil {
			e2eNTMBinErr = fmt.Errorf("mkdtemp: %w", err)
			return
		}

		binName := "ntm"
		if runtime.GOOS == "windows" {
			binName += ".exe"
		}
		outPath := filepath.Join(outDir, binName)

		cmd := exec.Command("go", "build", "-o", outPath, "./cmd/ntm")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			e2eNTMBinErr = fmt.Errorf("go build ./cmd/ntm: %w output=%s", err, string(out))
			return
		}
		e2eNTMBinPath = outPath
	})

	return e2eNTMBinPath, e2eNTMBinErr
}

func findRepoRoot(startDir string) (string, error) {
	dir := startDir
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find repo root with go.mod from %q", startDir)
}

// TestLogger provides structured logging for E2E tests with persistent log files.
type TestLogger struct {
	t         *testing.T
	logFile   *os.File
	startTime time.Time
	scenario  string
	logPath   string
}

// NewTestLogger creates a new test logger with a dedicated log file.
func NewTestLogger(t *testing.T, scenario string) *TestLogger {
	logDir := os.Getenv("E2E_LOG_DIR")
	if logDir == "" {
		logDir = "/tmp/ntm-e2e-logs"
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("[E2E-SETUP] Failed to create log directory: %v", err)
	}

	timestamp := time.Now().Format("20060102-150405")
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", scenario, timestamp))

	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("[E2E-SETUP] Failed to create log file: %v", err)
	}

	logger := &TestLogger{
		t:         t,
		logFile:   f,
		startTime: time.Now(),
		scenario:  scenario,
		logPath:   logPath,
	}

	logger.Log("[E2E-START] === E2E Test Started: %s ===", scenario)
	logger.Log("[E2E-START] Timestamp: %s", logger.startTime.Format(time.RFC3339))
	logger.Log("[E2E-START] Log file: %s", logPath)

	return logger
}

// Log writes a timestamped message to both the test log and log file.
func (l *TestLogger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	elapsed := time.Since(l.startTime).Round(time.Millisecond)
	line := fmt.Sprintf("[%s] %s\n", elapsed, msg)

	l.t.Log(msg)
	if l.logFile != nil {
		l.logFile.WriteString(line)
	}
}

// LogJSON writes a JSON-formatted value to the log with a label.
func (l *TestLogger) LogJSON(label string, v interface{}) {
	data, _ := json.MarshalIndent(v, "", "  ")
	l.Log("[E2E-JSON] %s:\n%s", label, string(data))
}

// Close finalizes the log file with a summary.
func (l *TestLogger) Close() {
	duration := time.Since(l.startTime)
	l.Log("[E2E-END] === Test completed in %s ===", duration.Round(time.Millisecond))
	if l.logFile != nil {
		l.logFile.Close()
	}
}

// LogPath returns the path to the log file.
func (l *TestLogger) LogPath() string {
	return l.logPath
}

// TestSuite manages E2E test sessions with setup and cleanup.
type TestSuite struct {
	t       *testing.T
	logger  *TestLogger
	session string
	panes   map[int]string // pane index -> agent type
	cleanup []func()
}

// NewTestSuite creates a new test suite for the given scenario.
func NewTestSuite(t *testing.T, scenario string) *TestSuite {
	logger := NewTestLogger(t, scenario)

	s := &TestSuite{
		t:       t,
		logger:  logger,
		session: fmt.Sprintf("e2e_%s_%d", scenario, time.Now().Unix()),
		panes:   make(map[int]string),
	}

	s.logger.Log("[E2E-SUITE] Creating test session: %s", s.session)
	return s
}

// Setup creates the tmux session for testing.
func (s *TestSuite) Setup() error {
	s.logger.Log("[E2E-SETUP] Setting up test environment")

	// Check if tmux is available
	if !tmux.DefaultClient.IsInstalled() {
		return fmt.Errorf("tmux not found")
	}

	// Check if ntm is available
	if ntmPath, err := exec.LookPath("ntm"); err != nil {
		return fmt.Errorf("ntm not found: %w", err)
	} else {
		s.logger.Log("[E2E-SETUP] Using ntm binary: %s", ntmPath)
	}

	// Create tmux session with specified dimensions
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", s.session, "-x", "200", "-y", "50")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create session: %w, output: %s", err, string(output))
	}

	s.cleanup = append(s.cleanup, func() {
		s.logger.Log("[E2E-CLEANUP] Killing session %s", s.session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", s.session).Run()
	})

	s.logger.Log("[E2E-SETUP] Session created successfully")
	return nil
}

// SpawnAgent launches an agent in the specified pane.
func (s *TestSuite) SpawnAgent(pane int, agentType string) error {
	s.logger.Log("[E2E-SPAWN] Spawning %s agent in pane %d", agentType, pane)

	// Create new pane if needed
	if pane > 0 {
		cmd := exec.Command(tmux.BinaryPath(), "split-window", "-t", s.session, "-h")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("create pane: %w", err)
		}
	}

	// Map agent type to command alias
	alias := agentType
	switch agentType {
	case "claude", "cc":
		alias = "cc"
	case "codex", "cod":
		alias = "cod"
	case "gemini", "gmi":
		alias = "gmi"
	default:
		return fmt.Errorf("unknown agent type: %s", agentType)
	}

	// Launch agent
	target := fmt.Sprintf("%s:%d", s.session, pane)
	sendCmd := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, alias, "Enter")
	if err := sendCmd.Run(); err != nil {
		return fmt.Errorf("launch agent: %w", err)
	}

	s.panes[pane] = agentType
	s.logger.Log("[E2E-SPAWN] Agent %s launched in pane %d", agentType, pane)

	// Wait for agent to initialize
	time.Sleep(6 * time.Second)

	return nil
}

// SendPrompt sends a prompt to a specific pane.
func (s *TestSuite) SendPrompt(pane int, prompt string) error {
	s.logger.Log("[E2E-SEND] Sending prompt to pane %d: %s", pane, truncateString(prompt, 50))

	// Use ntm --robot-send for consistent behavior
	cmd := exec.Command("ntm",
		fmt.Sprintf("--robot-send=%s", s.session),
		fmt.Sprintf("--panes=%d", pane),
		fmt.Sprintf("--msg=%s", prompt))

	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Log("[E2E-SEND] Send failed: %v, output: %s", err, string(output))
		return err
	}

	s.logger.Log("[E2E-SEND] Prompt sent successfully")
	return nil
}

// CaptureOutput captures recent output from a pane.
func (s *TestSuite) CaptureOutput(pane int, lines int) (string, error) {
	s.logger.Log("[E2E-CAPTURE] Capturing %d lines from pane %d", lines, pane)

	cmd := exec.Command("ntm",
		fmt.Sprintf("--robot-tail=%s", s.session),
		fmt.Sprintf("--panes=%d", pane),
		fmt.Sprintf("--lines=%d", lines))

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	s.logger.Log("[E2E-CAPTURE] Captured %d bytes", len(output))
	return string(output), nil
}

// IsWorkingResult represents the parsed response from --robot-is-working.
type IsWorkingResult struct {
	Success bool                      `json:"success"`
	Session string                    `json:"session"`
	Panes   map[string]PaneWorkStatus `json:"panes"`
	Summary IsWorkingSummary          `json:"summary"`
	Error   string                    `json:"error,omitempty"`
}

// PaneWorkStatus represents the work state for a single pane.
type PaneWorkStatus struct {
	AgentType            string   `json:"agent_type"`
	IsWorking            bool     `json:"is_working"`
	IsIdle               bool     `json:"is_idle"`
	IsRateLimited        bool     `json:"is_rate_limited"`
	IsContextLow         bool     `json:"is_context_low"`
	ContextRemaining     *float64 `json:"context_remaining,omitempty"`
	Confidence           float64  `json:"confidence"`
	Recommendation       string   `json:"recommendation"`
	RecommendationReason string   `json:"recommendation_reason"`
}

// IsWorkingSummary provides aggregate statistics.
type IsWorkingSummary struct {
	TotalPanes       int              `json:"total_panes"`
	WorkingCount     int              `json:"working_count"`
	IdleCount        int              `json:"idle_count"`
	RateLimitedCount int              `json:"rate_limited_count"`
	ContextLowCount  int              `json:"context_low_count"`
	ErrorCount       int              `json:"error_count"`
	ByRecommendation map[string][]int `json:"by_recommendation"`
}

// CallIsWorking invokes --robot-is-working and parses the result.
func (s *TestSuite) CallIsWorking(panes []int) (*IsWorkingResult, error) {
	s.logger.Log("[E2E-IS-WORKING] Calling --robot-is-working for panes %v", panes)

	args := []string{fmt.Sprintf("--robot-is-working=%s", s.session)}
	if len(panes) > 0 {
		paneStrs := make([]string, len(panes))
		for i, p := range panes {
			paneStrs[i] = strconv.Itoa(p)
		}
		args = append(args, fmt.Sprintf("--panes=%s", strings.Join(paneStrs, ",")))
	}

	cmd := exec.Command("ntm", args...)
	output, err := cmd.Output()
	if err != nil {
		s.logger.Log("[E2E-IS-WORKING] Command failed: %v", err)
		// Still try to parse the output for error info
	}

	var result IsWorkingResult
	if err := json.Unmarshal(output, &result); err != nil {
		s.logger.Log("[E2E-IS-WORKING] Parse failed: %v, output: %s", err, string(output))
		return nil, fmt.Errorf("parse failed: %w, output: %s", err, string(output))
	}

	s.logger.LogJSON("[E2E-IS-WORKING] Result", result)
	return &result, nil
}

// SmartRestartResult represents the parsed response from --robot-smart-restart.
type SmartRestartResult struct {
	Success bool                     `json:"success"`
	Session string                   `json:"session"`
	DryRun  bool                     `json:"dry_run"`
	Force   bool                     `json:"force"`
	Actions map[string]RestartAction `json:"actions"`
	Summary RestartSummary           `json:"summary"`
	Error   string                   `json:"error,omitempty"`
}

// RestartAction documents the action taken for a single pane.
type RestartAction struct {
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RestartSummary aggregates results across all panes.
type RestartSummary struct {
	Restarted     int              `json:"restarted"`
	Skipped       int              `json:"skipped"`
	Waiting       int              `json:"waiting"`
	Failed        int              `json:"failed"`
	WouldRestart  int              `json:"would_restart,omitempty"`
	PanesByAction map[string][]int `json:"panes_by_action"`
}

// CallSmartRestart invokes --robot-smart-restart and parses the result.
func (s *TestSuite) CallSmartRestart(panes []int, force bool, dryRun bool) (*SmartRestartResult, error) {
	s.logger.Log("[E2E-SMART-RESTART] Calling --robot-smart-restart for panes %v (force=%v, dry-run=%v)",
		panes, force, dryRun)

	args := []string{fmt.Sprintf("--robot-smart-restart=%s", s.session)}
	if len(panes) > 0 {
		paneStrs := make([]string, len(panes))
		for i, p := range panes {
			paneStrs[i] = strconv.Itoa(p)
		}
		args = append(args, fmt.Sprintf("--panes=%s", strings.Join(paneStrs, ",")))
	}
	if force {
		args = append(args, "--force")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}

	cmd := exec.Command("ntm", args...)
	output, err := cmd.Output()
	if err != nil {
		s.logger.Log("[E2E-SMART-RESTART] Command failed: %v", err)
	}

	var result SmartRestartResult
	if err := json.Unmarshal(output, &result); err != nil {
		s.logger.Log("[E2E-SMART-RESTART] Parse failed: %v, output: %s", err, string(output))
		return nil, err
	}

	s.logger.LogJSON("[E2E-SMART-RESTART] Result", result)
	return &result, nil
}

// AgentHealthResult represents the parsed response from --robot-agent-health.
type AgentHealthResult struct {
	Success       bool                        `json:"success"`
	Session       string                      `json:"session"`
	CautAvailable bool                        `json:"caut_available"`
	Panes         map[string]PaneHealthStatus `json:"panes"`
	FleetHealth   FleetHealthSummary          `json:"fleet_health"`
	Error         string                      `json:"error,omitempty"`
}

// PaneHealthStatus contains health info for a single pane.
type PaneHealthStatus struct {
	AgentType            string   `json:"agent_type"`
	HealthScore          int      `json:"health_score"`
	HealthGrade          string   `json:"health_grade"`
	Issues               []string `json:"issues"`
	Recommendation       string   `json:"recommendation"`
	RecommendationReason string   `json:"recommendation_reason"`
}

// FleetHealthSummary contains overall health statistics.
type FleetHealthSummary struct {
	TotalPanes     int     `json:"total_panes"`
	HealthyCount   int     `json:"healthy_count"`
	WarningCount   int     `json:"warning_count"`
	CriticalCount  int     `json:"critical_count"`
	AvgHealthScore float64 `json:"avg_health_score"`
	OverallGrade   string  `json:"overall_grade"`
}

// CallAgentHealth invokes --robot-agent-health and parses the result.
func (s *TestSuite) CallAgentHealth(panes []int) (*AgentHealthResult, error) {
	s.logger.Log("[E2E-AGENT-HEALTH] Calling --robot-agent-health for panes %v", panes)

	args := []string{fmt.Sprintf("--robot-agent-health=%s", s.session)}
	if len(panes) > 0 {
		paneStrs := make([]string, len(panes))
		for i, p := range panes {
			paneStrs[i] = strconv.Itoa(p)
		}
		args = append(args, fmt.Sprintf("--panes=%s", strings.Join(paneStrs, ",")))
	}

	cmd := exec.Command("ntm", args...)
	output, err := cmd.Output()
	if err != nil {
		s.logger.Log("[E2E-AGENT-HEALTH] Command failed: %v", err)
	}

	var result AgentHealthResult
	if err := json.Unmarshal(output, &result); err != nil {
		s.logger.Log("[E2E-AGENT-HEALTH] Parse failed: %v, output: %s", err, string(output))
		return nil, err
	}

	s.logger.LogJSON("[E2E-AGENT-HEALTH] Result", result)
	return &result, nil
}

// WaitForState polls until the state check function returns true or timeout.
func (s *TestSuite) WaitForState(pane int, check func(*PaneWorkStatus) bool, timeout time.Duration) bool {
	s.logger.Log("[E2E-WAIT] Waiting for state condition on pane %d (timeout: %v)", pane, timeout)

	deadline := time.Now().Add(timeout)
	attempts := 0

	for time.Now().Before(deadline) {
		attempts++
		result, err := s.CallIsWorking([]int{pane})
		if err == nil && result.Success {
			if status, ok := result.Panes[strconv.Itoa(pane)]; ok {
				if check(&status) {
					s.logger.Log("[E2E-WAIT] State condition met after %d attempts", attempts)
					return true
				}
			}
		}
		time.Sleep(2 * time.Second)
	}

	s.logger.Log("[E2E-WAIT] State condition NOT met after %d attempts", attempts)
	return false
}

// Teardown cleans up all resources created by the test suite.
func (s *TestSuite) Teardown() {
	s.logger.Log("[E2E-TEARDOWN] Running cleanup (%d items)", len(s.cleanup))

	// Run cleanup in reverse order
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}

	s.logger.Close()
}

// Session returns the tmux session name.
func (s *TestSuite) Session() string {
	return s.session
}

// Logger returns the test logger.
func (s *TestSuite) Logger() *TestLogger {
	return s.logger
}

// truncateString truncates a string to n characters with ellipsis.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
