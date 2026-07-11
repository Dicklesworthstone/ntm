package testutil

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AssertSessionExists verifies that a tmux session exists.
func AssertSessionExists(t *testing.T, logger *TestLogger, name string) {
	t.Helper()
	logger.Log("VERIFY: Session %s exists", name)

	cmd := exec.Command(tmux.BinaryPath(), "has-session", "-t", name)
	if err := bindIsolatedTmuxCommand(tmux.BinaryPath(), cmd); err != nil {
		t.Fatal(err)
	}
	err := cmd.Run()
	if err != nil {
		logger.Log("FAIL: Session %s does not exist", name)
		t.Errorf("session %s should exist, but it does not", name)
	} else {
		logger.Log("PASS: Session %s exists", name)
	}
}

// AssertSessionNotExists verifies that a tmux session does not exist.
func AssertSessionNotExists(t *testing.T, logger *TestLogger, name string) {
	t.Helper()
	logger.Log("VERIFY: Session %s does not exist", name)

	cmd := exec.Command(tmux.BinaryPath(), "has-session", "-t", name)
	if err := bindIsolatedTmuxCommand(tmux.BinaryPath(), cmd); err != nil {
		t.Fatal(err)
	}
	err := cmd.Run()
	if err == nil {
		logger.Log("FAIL: Session %s exists but should not", name)
		t.Errorf("session %s should not exist, but it does", name)
	} else {
		logger.Log("PASS: Session %s does not exist", name)
	}
}

// AssertPaneCount verifies the number of panes in a session.
func AssertPaneCount(t *testing.T, logger *TestLogger, name string, expected int) {
	t.Helper()
	logger.Log("VERIFY: Session %s has %d panes", name, expected)

	count, err := GetSessionPaneCount(name)
	if err != nil {
		logger.Log("FAIL: Could not get pane count: %v", err)
		t.Errorf("failed to get pane count for session %s: %v", name, err)
		return
	}

	if count != expected {
		logger.Log("FAIL: Session %s has %d panes, expected %d", name, count, expected)
		t.Errorf("session %s has %d panes, expected %d", name, count, expected)
	} else {
		logger.Log("PASS: Session %s has %d panes", name, count)
	}
}

// AssertPaneCountAtLeast verifies the session has at least N panes.
func AssertPaneCountAtLeast(t *testing.T, logger *TestLogger, name string, minimum int) {
	t.Helper()
	logger.Log("VERIFY: Session %s has at least %d panes", name, minimum)

	count, err := GetSessionPaneCount(name)
	if err != nil {
		logger.Log("FAIL: Could not get pane count: %v", err)
		t.Errorf("failed to get pane count for session %s: %v", name, err)
		return
	}

	if count < minimum {
		logger.Log("FAIL: Session %s has %d panes, expected at least %d", name, count, minimum)
		t.Errorf("session %s has %d panes, expected at least %d", name, count, minimum)
	} else {
		logger.Log("PASS: Session %s has %d panes (>= %d)", name, count, minimum)
	}
}

// AssertPaneContains verifies that a pane's content contains a substring.
func AssertPaneContains(t *testing.T, logger *TestLogger, name string, paneIndex int, substring string) {
	t.Helper()
	logger.Log("VERIFY: Pane %s:%d contains %q", name, paneIndex, substring)

	content, err := CapturePane(name, paneIndex)
	if err != nil {
		logger.Log("FAIL: Could not capture pane: %v", err)
		t.Errorf("failed to capture pane %s:%d: %v", name, paneIndex, err)
		return
	}

	if !strings.Contains(content, substring) {
		logger.Log("FAIL: Pane content does not contain %q", substring)
		logger.Log("Pane content:\n%s", content)
		t.Errorf("pane %s:%d does not contain %q", name, paneIndex, substring)
	} else {
		logger.Log("PASS: Pane contains %q", substring)
	}
}

// AssertCommandSuccess runs a command and verifies it succeeds (exit 0).
func AssertCommandSuccess(t *testing.T, logger *TestLogger, cmd string, args ...string) []byte {
	t.Helper()
	logger.Log("VERIFY: Command succeeds: %s %s", cmd, strings.Join(args, " "))

	out, err := logger.Exec(cmd, args...)
	if err != nil {
		logger.Log("FAIL: Command failed: %v", err)
		t.Errorf("command %s %s failed: %v\nOutput: %s", cmd, strings.Join(args, " "), err, string(out))
		return out
	}

	logger.Log("PASS: Command succeeded")
	return out
}

// AssertCommandFails runs a command and verifies it fails (non-zero exit).
func AssertCommandFails(t *testing.T, logger *TestLogger, cmd string, args ...string) []byte {
	t.Helper()
	logger.Log("VERIFY: Command fails: %s %s", cmd, strings.Join(args, " "))

	out, err := logger.Exec(cmd, args...)
	if err == nil {
		logger.Log("FAIL: Command succeeded but should have failed")
		t.Errorf("command %s %s should have failed but succeeded\nOutput: %s", cmd, strings.Join(args, " "), string(out))
		return out
	}

	logger.Log("PASS: Command failed as expected: %v", err)
	return out
}

// AssertJSONOutput verifies that command output is valid JSON.
func AssertJSONOutput(t *testing.T, logger *TestLogger, output []byte) {
	t.Helper()
	logger.Log("VERIFY: Output is valid JSON")

	var v interface{}
	if err := json.Unmarshal(output, &v); err != nil {
		logger.Log("FAIL: Invalid JSON: %v", err)
		logger.Log("Output: %s", string(output))
		t.Errorf("output is not valid JSON: %v\nOutput: %s", err, string(output))
	} else {
		logger.Log("PASS: Output is valid JSON")
	}
}

// AssertJSONField verifies a field in JSON output has the expected value.
func AssertJSONField(t *testing.T, logger *TestLogger, output []byte, field string, expected interface{}) {
	t.Helper()
	logger.Log("VERIFY: JSON field %q equals %v", field, expected)

	var data map[string]interface{}
	if err := json.Unmarshal(output, &data); err != nil {
		logger.Log("FAIL: Invalid JSON: %v", err)
		t.Errorf("failed to parse JSON: %v", err)
		return
	}

	actual, ok := data[field]
	if !ok {
		logger.Log("FAIL: Field %q not found in JSON", field)
		t.Errorf("field %q not found in JSON output", field)
		return
	}

	// Compare as strings for simplicity
	if actual != expected {
		logger.Log("FAIL: Field %q = %v, expected %v", field, actual, expected)
		t.Errorf("field %q = %v, expected %v", field, actual, expected)
	} else {
		logger.Log("PASS: Field %q = %v", field, actual)
	}
}

// AssertEventually retries an assertion until it passes or timeout.
// Returns true if the assertion passed, false if it timed out.
func AssertEventually(t *testing.T, logger *TestLogger, timeout time.Duration, interval time.Duration, description string, assertion func() bool) bool {
	t.Helper()
	logger.Log("VERIFY (eventually, timeout=%s): %s", timeout, description)

	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		if assertion() {
			logger.Log("PASS: %s (attempt %d)", description, attempt)
			return true
		}
		time.Sleep(interval)
	}

	logger.Log("FAIL: %s (timed out after %d attempts)", description, attempt)
	t.Errorf("%s: timed out after %s (%d attempts)", description, timeout, attempt)
	return false
}

// AssertNTMStatus verifies ntm status output for a session.
func AssertNTMStatus(t *testing.T, logger *TestLogger, sessionName string, expectedPanes int) {
	t.Helper()
	logger.LogSection("Verifying NTM Status")

	out := AssertCommandSuccess(t, logger, "ntm", "status", "--json", sessionName)
	AssertJSONOutput(t, logger, out)

	var status struct {
		Session string `json:"session"`
		Panes   []struct {
			Index int    `json:"index"`
			Type  string `json:"type"`
		} `json:"panes"`
	}

	if err := json.Unmarshal(out, &status); err != nil {
		logger.Log("FAIL: Could not parse status JSON: %v", err)
		t.Errorf("failed to parse status JSON: %v", err)
		return
	}

	if status.Session != sessionName {
		logger.Log("FAIL: Session name mismatch: %q vs %q", status.Session, sessionName)
		t.Errorf("session name mismatch: got %q, expected %q", status.Session, sessionName)
	}

	if len(status.Panes) != expectedPanes {
		logger.Log("FAIL: Pane count mismatch: %d vs %d", len(status.Panes), expectedPanes)
		t.Errorf("pane count mismatch: got %d, expected %d", len(status.Panes), expectedPanes)
	} else {
		logger.Log("PASS: Status verified for session %s with %d panes", sessionName, expectedPanes)
	}
}
