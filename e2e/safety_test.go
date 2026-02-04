//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-SAFETY] Tests for ntm safety status/check (destructive command protection).
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// SafetyStatusResponse mirrors the JSON output from `ntm safety status --json`.
type SafetyStatusResponse struct {
	GeneratedAt   time.Time `json:"generated_at"`
	Installed     bool      `json:"installed"`
	PolicyPath    string    `json:"policy_path,omitempty"`
	BlockedCount  int       `json:"blocked_rules"`
	ApprovalCount int       `json:"approval_rules"`
	AllowedCount  int       `json:"allowed_rules"`
	WrapperPath   string    `json:"wrapper_path,omitempty"`
	HookInstalled bool      `json:"hook_installed"`
}

// SafetyCheckResponse mirrors the JSON output from `ntm safety check --json`.
type SafetyCheckResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Command     string               `json:"command"`
	Action      string               `json:"action"`
	Pattern     string               `json:"pattern,omitempty"`
	Reason      string               `json:"reason,omitempty"`
	Policy      *SafetyPolicyVerdict `json:"policy,omitempty"`
	DCG         *SafetyDCGVerdict    `json:"dcg,omitempty"`
}

type SafetyPolicyVerdict struct {
	Action  string `json:"action"`
	Pattern string `json:"pattern,omitempty"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"`
}

type SafetyDCGVerdict struct {
	Available bool   `json:"available"`
	Checked   bool   `json:"checked"`
	Blocked   bool   `json:"blocked"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SafetyTestSuite manages E2E tests for safety commands.
type SafetyTestSuite struct {
	t       *testing.T
	logger  *TestLogger
	tempDir string
}

// NewSafetyTestSuite creates a new safety test suite.
func NewSafetyTestSuite(t *testing.T, scenario string) *SafetyTestSuite {
	SkipIfNoNTM(t)

	tempDir := t.TempDir()
	logger := NewTestLogger(t, "safety-"+scenario)

	suite := &SafetyTestSuite{
		t:       t,
		logger:  logger,
		tempDir: tempDir,
	}

	t.Cleanup(func() {
		logger.Close()
	})

	return suite
}

func (s *SafetyTestSuite) runSafetyStatus() (*SafetyStatusResponse, string, string, error) {
	args := []string{"safety", "status", "--json"}
	s.logger.Log("[E2E-SAFETY] Running: ntm %s", strings.Join(args, " "))

	cmd := exec.Command("ntm", args...)
	cmd.Env = append(os.Environ(), "HOME="+s.tempDir)
	cmd.Dir = s.tempDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	s.logger.Log("[E2E-SAFETY] stdout: %s", stdoutStr)
	if stderrStr != "" {
		s.logger.Log("[E2E-SAFETY] stderr: %s", stderrStr)
	}
	if err != nil {
		s.logger.Log("[E2E-SAFETY] error: %v", err)
	}

	var resp SafetyStatusResponse
	if jsonErr := json.Unmarshal([]byte(stdoutStr), &resp); jsonErr != nil {
		s.logger.Log("[E2E-SAFETY] JSON parse error: %v", jsonErr)
		return nil, stdoutStr, stderrStr, jsonErr
	}

	s.logger.LogJSON("[E2E-SAFETY] Status response", resp)
	return &resp, stdoutStr, stderrStr, err
}

func (s *SafetyTestSuite) runSafetyCheck(command string) (*SafetyCheckResponse, string, string, error) {
	args := []string{"safety", "check", command, "--json"}
	s.logger.Log("[E2E-SAFETY] Running: ntm %s", strings.Join(args, " "))

	cmd := exec.Command("ntm", args...)
	cmd.Env = append(os.Environ(), "HOME="+s.tempDir)
	cmd.Dir = s.tempDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	s.logger.Log("[E2E-SAFETY] stdout: %s", stdoutStr)
	if stderrStr != "" {
		s.logger.Log("[E2E-SAFETY] stderr: %s", stderrStr)
	}
	if err != nil {
		s.logger.Log("[E2E-SAFETY] error: %v", err)
	}

	var resp SafetyCheckResponse
	if jsonErr := json.Unmarshal([]byte(stdoutStr), &resp); jsonErr != nil {
		s.logger.Log("[E2E-SAFETY] JSON parse error: %v", jsonErr)
		return nil, stdoutStr, stderrStr, jsonErr
	}

	s.logger.LogJSON("[E2E-SAFETY] Check response", resp)
	return &resp, stdoutStr, stderrStr, err
}

func TestSafetyStatus_JSON(t *testing.T) {
	suite := NewSafetyTestSuite(t, "status")

	resp, _, _, err := suite.runSafetyStatus()
	if resp == nil {
		t.Fatalf("[E2E-SAFETY] Failed to parse status response: %v", err)
	}

	if resp.GeneratedAt.IsZero() {
		t.Errorf("[E2E-SAFETY] Expected generated_at to be set")
	}
	if resp.BlockedCount <= 0 {
		t.Errorf("[E2E-SAFETY] Expected blocked_rules > 0, got %d", resp.BlockedCount)
	}
	if resp.AllowedCount <= 0 {
		t.Errorf("[E2E-SAFETY] Expected allowed_rules > 0, got %d", resp.AllowedCount)
	}
	if resp.ApprovalCount <= 0 {
		t.Errorf("[E2E-SAFETY] Expected approval_rules > 0, got %d", resp.ApprovalCount)
	}
	if resp.WrapperPath == "" {
		t.Errorf("[E2E-SAFETY] Expected wrapper_path to be set")
	}

	suite.logger.Log("[E2E-SAFETY] safety_status_test_completed")
}

func TestSafetyCheck_Blocked(t *testing.T) {
	suite := NewSafetyTestSuite(t, "check-blocked")

	command := "git reset --hard HEAD~1"
	resp, _, _, err := suite.runSafetyCheck(command)
	if resp == nil {
		t.Fatalf("[E2E-SAFETY] Failed to parse check response: %v", err)
	}

	if resp.Action != "block" {
		t.Errorf("[E2E-SAFETY] Expected action=block, got %q", resp.Action)
	}
	if resp.Policy == nil || resp.Policy.Action != "block" {
		t.Errorf("[E2E-SAFETY] Expected policy action=block, got %+v", resp.Policy)
	}
	if resp.Pattern == "" {
		t.Errorf("[E2E-SAFETY] Expected non-empty pattern for blocked command")
	}
	if err == nil {
		t.Errorf("[E2E-SAFETY] Expected non-nil error (exit code 1) for blocked command")
	}

	suite.logger.Log("[E2E-SAFETY] safety_check_blocked_completed")
}

func TestSafetyCheck_Allowed(t *testing.T) {
	suite := NewSafetyTestSuite(t, "check-allowed")

	command := "git reset --soft HEAD~1"
	resp, _, _, err := suite.runSafetyCheck(command)
	if resp == nil {
		t.Fatalf("[E2E-SAFETY] Failed to parse check response: %v", err)
	}
	if err != nil {
		t.Fatalf("[E2E-SAFETY] Expected exit code 0 for allowed command, got error: %v", err)
	}

	if resp.Action != "allow" {
		t.Errorf("[E2E-SAFETY] Expected action=allow, got %q", resp.Action)
	}
	if resp.Policy == nil || resp.Policy.Action != "allow" {
		t.Errorf("[E2E-SAFETY] Expected policy action=allow, got %+v", resp.Policy)
	}
	if resp.Pattern == "" {
		t.Errorf("[E2E-SAFETY] Expected non-empty pattern for allowed command")
	}

	suite.logger.Log("[E2E-SAFETY] safety_check_allowed_completed")
}
