package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// These tests exercise the robot-mode JSON outputs end-to-end using the built
// binary on PATH. They intentionally avoid deep schema validation beyond
// parseability to keep them fast and resilient to small additive fields.

func TestRobotVersion(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	out, err := logger.Exec("ntm", "--robot-version")
	if err != nil {
		t.Fatalf("ntm --robot-version failed: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := payload["version"]; !ok {
		t.Fatalf("missing version field in output")
	}
}

func TestRobotStatusEmptySessions(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)

	// Ensure tmux not required; if tmux missing, status should still JSON-parse
	out, err := logger.Exec("ntm", "--robot-status")
	if err != nil {
		t.Fatalf("ntm --robot-status failed: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// sessions should exist as array (possibly empty)
	if _, ok := payload["sessions"]; !ok {
		t.Fatalf("missing sessions array")
	}
}

func TestRobotPlan(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	out, err := logger.Exec("ntm", "--robot-plan")
	if err != nil {
		t.Fatalf("ntm --robot-plan failed: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := payload["actions"]; !ok {
		t.Fatalf("missing actions array")
	}
}

func TestRobotHelp(t *testing.T) {
	logger := testutil.NewTestLoggerStdout(t)
	out, err := logger.Exec("ntm", "--robot-help")
	if err != nil {
		t.Fatalf("ntm --robot-help failed: %v", err)
	}

	// Ensure output is not empty and contains a known marker
	if len(out) == 0 {
		t.Fatalf("robot help output empty")
	}
	if !bytesContains(out, []byte("robot-status")) {
		t.Fatalf("robot help missing expected marker")
	}
}

// bytesContains: minimal copy of bytes.Contains to avoid extra imports
func bytesContains(b, subslice []byte) bool {
	for i := 0; i+len(subslice) <= len(b); i++ {
		match := true
		for j := 0; j < len(subslice); j++ {
			if b[i+j] != subslice[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Skip tests if ntm binary is missing.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ntm"); err != nil {
		// ntm binary not on PATH; skip suite gracefully
		return
	}
	os.Exit(m.Run())
}
