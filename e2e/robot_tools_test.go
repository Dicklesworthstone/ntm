//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-TOOLS] Tests for `ntm --robot-tools` inventory + health reporting.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type robotToolsOutput struct {
	Success      bool                   `json:"success"`
	Timestamp    string                 `json:"timestamp,omitempty"`
	Tools        []robotToolInfo        `json:"tools"`
	HealthReport robotToolsHealthReport `json:"health_report"`
	Error        string                 `json:"error,omitempty"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	Hint         string                 `json:"hint,omitempty"`
}

type robotToolInfo struct {
	Name         string          `json:"name"`
	Installed    bool            `json:"installed"`
	Version      string          `json:"version,omitempty"`
	Path         string          `json:"path,omitempty"`
	Capabilities []string        `json:"capabilities"`
	Health       robotToolHealth `json:"health"`
	Required     bool            `json:"required,omitempty"`
}

type robotToolHealth struct {
	Healthy     bool   `json:"healthy"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	LastChecked string `json:"last_checked"`
}

type robotToolsHealthReport struct {
	Total     int             `json:"total"`
	Healthy   int             `json:"healthy"`
	Unhealthy int             `json:"unhealthy"`
	Missing   int             `json:"missing"`
	Tools     map[string]bool `json:"tools"`
}

type robotSnapshotOutput struct {
	Success bool            `json:"success"`
	Tools   []robotToolInfo `json:"tools"`
}

func runNTMStdout(args []string, env []string) (stdout []byte, stderr []byte, err error) {
	cmd := exec.Command("ntm", args...)
	if env != nil {
		cmd.Env = env
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	return out, stderrBuf.Bytes(), err
}

func mustParseRobotToolsJSON(t *testing.T, stdout []byte) robotToolsOutput {
	t.Helper()
	var out robotToolsOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("[E2E-TOOLS] failed to parse JSON: %v\nraw=%s", err, string(stdout))
	}
	return out
}

func findTool(tools []robotToolInfo, name string) *robotToolInfo {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func mustParseRFC3339(t *testing.T, ts string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
	if err != nil {
		t.Fatalf("[E2E-TOOLS] invalid timestamp %q: %v", ts, err)
	}
	return parsed
}

func TestRobotTools_BasicInventoryAndHealth(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "robot-tools-basic")
	defer logger.Close()

	stdout, stderr, err := runNTMStdout([]string{"--robot-tools"}, nil)
	logger.Log("[E2E-TOOLS] stderr=%s", string(stderr))
	if err != nil {
		t.Fatalf("[E2E-TOOLS] ntm --robot-tools failed: %v", err)
	}

	out := mustParseRobotToolsJSON(t, stdout)
	logger.LogJSON("[E2E-TOOLS] tools", out)

	if !out.Success {
		t.Fatalf("[E2E-TOOLS] expected success=true, got success=false error=%q code=%q hint=%q", out.Error, out.ErrorCode, out.Hint)
	}
	if len(out.Tools) == 0 {
		t.Fatalf("[E2E-TOOLS] expected non-empty tools list")
	}

	// Assert stable ordering (robot output promises deterministic sort by name).
	for i := 1; i < len(out.Tools); i++ {
		if out.Tools[i-1].Name > out.Tools[i].Name {
			t.Fatalf("[E2E-TOOLS] tools not sorted by name at %d: %q > %q", i, out.Tools[i-1].Name, out.Tools[i].Name)
		}
	}

	// bv is required for triage; this environment should have it installed (we use bv in this repo).
	bv := findTool(out.Tools, "bv")
	if bv == nil {
		t.Fatalf("[E2E-TOOLS] expected tool \"bv\" to be present")
	}
	if !bv.Required {
		t.Fatalf("[E2E-TOOLS] expected bv.required=true")
	}
	if !bv.Installed {
		t.Fatalf("[E2E-TOOLS] expected bv.installed=true (got false). PATH=%q", os.Getenv("PATH"))
	}
	if strings.TrimSpace(bv.Version) == "" || strings.TrimSpace(bv.Version) == "0.0.0" {
		t.Fatalf("[E2E-TOOLS] expected bv.version to be populated, got %q", bv.Version)
	}
	if !bv.Health.Healthy {
		t.Fatalf("[E2E-TOOLS] expected bv.health.healthy=true, got false (msg=%q err=%q)", bv.Health.Message, bv.Health.Error)
	}
	_ = mustParseRFC3339(t, bv.Health.LastChecked)

	// If JFP is installed, it should appear with capabilities and a version string.
	if jfp := findTool(out.Tools, "jfp"); jfp != nil && jfp.Installed {
		if strings.TrimSpace(jfp.Version) == "" || strings.TrimSpace(jfp.Version) == "0.0.0" {
			t.Fatalf("[E2E-TOOLS] expected jfp.version to be populated, got %q", jfp.Version)
		}
		if len(jfp.Capabilities) == 0 {
			t.Fatalf("[E2E-TOOLS] expected jfp.capabilities to be non-empty when installed")
		}
		_ = mustParseRFC3339(t, jfp.Health.LastChecked)
	}
}

func TestRobotTools_ReportsMissingToolsWhenPATHIsRestricted(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "robot-tools-missing")
	defer logger.Close()

	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm not found in PATH")
	}

	// Restrict PATH to the directory that contains ntm to ensure most tools are missing.
	ntmDir := filepath.Dir(ntmPath)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+ntmDir)

	stdout, stderr, err := runNTMStdout([]string{"--robot-tools"}, env)
	logger.Log("[E2E-TOOLS] stderr=%s", string(stderr))
	if err != nil {
		t.Fatalf("[E2E-TOOLS] ntm --robot-tools failed under restricted PATH: %v", err)
	}

	out := mustParseRobotToolsJSON(t, stdout)
	logger.LogJSON("[E2E-TOOLS] tools", out)

	if !out.Success {
		t.Fatalf("[E2E-TOOLS] expected success=true under restricted PATH, got success=false error=%q code=%q hint=%q", out.Error, out.ErrorCode, out.Hint)
	}

	bv := findTool(out.Tools, "bv")
	if bv == nil {
		t.Fatalf("[E2E-TOOLS] expected tool \"bv\" to be present under restricted PATH")
	}
	if bv.Installed {
		t.Fatalf("[E2E-TOOLS] expected bv.installed=false under restricted PATH, got true (PATH=%q)", ntmDir)
	}
	if bv.Health.Healthy {
		t.Fatalf("[E2E-TOOLS] expected bv.health.healthy=false when missing")
	}
	if bv.Health.Message == "" {
		t.Fatalf("[E2E-TOOLS] expected missing tool to include a message")
	}
	_ = mustParseRFC3339(t, bv.Health.LastChecked)
}

func TestRobotSnapshot_IncludesToolsSummary(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "robot-snapshot-tools")
	defer logger.Close()

	// Keep output smaller; we only care that tools are present.
	stdout, stderr, err := runNTMStdout([]string{"--robot-snapshot", "--bead-limit=1", "--robot-limit=1"}, nil)
	logger.Log("[E2E-TOOLS] stderr=%s", string(stderr))
	if err != nil {
		t.Fatalf("[E2E-TOOLS] ntm --robot-snapshot failed: %v", err)
	}

	var snap robotSnapshotOutput
	if err := json.Unmarshal(stdout, &snap); err != nil {
		t.Fatalf("[E2E-TOOLS] failed to parse snapshot JSON: %v\nraw=%s", err, string(stdout))
	}
	if !snap.Success {
		t.Fatalf("[E2E-TOOLS] expected snapshot success=true")
	}
	if len(snap.Tools) == 0 {
		t.Fatalf("[E2E-TOOLS] expected snapshot.tools to be non-empty")
	}
	if findTool(snap.Tools, "bv") == nil {
		t.Fatalf("[E2E-TOOLS] expected snapshot.tools to include \"bv\"")
	}
}

func TestRobotTools_ConsistentWithDoctorTools(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "robot-tools-doctor-consistency")
	defer logger.Close()

	toolsStdout, toolsStderr, err := runNTMStdout([]string{"--robot-tools"}, nil)
	logger.Log("[E2E-TOOLS] robot-tools stderr=%s", string(toolsStderr))
	if err != nil {
		t.Fatalf("[E2E-TOOLS] ntm --robot-tools failed: %v", err)
	}
	toolsOut := mustParseRobotToolsJSON(t, toolsStdout)
	if !toolsOut.Success {
		t.Fatalf("[E2E-TOOLS] expected robot-tools success=true")
	}

	doctorStdout, doctorStderr, _ := runNTMStdout([]string{"doctor", "--json"}, nil)
	logger.Log("[E2E-TOOLS] doctor stderr=%s", string(doctorStderr))

	var doctor DoctorReport
	if err := json.Unmarshal(doctorStdout, &doctor); err != nil {
		t.Fatalf("[E2E-TOOLS] failed to parse doctor JSON: %v\nraw=%s", err, string(doctorStdout))
	}

	// Cross-check at least the required bv tool when present in both outputs.
	bvRobot := findTool(toolsOut.Tools, "bv")
	if bvRobot == nil {
		t.Fatalf("[E2E-TOOLS] expected robot-tools to include bv")
	}

	var bvDoctor *ToolCheck
	for i := range doctor.Tools {
		if doctor.Tools[i].Name == "bv" {
			bvDoctor = &doctor.Tools[i]
			break
		}
	}
	if bvDoctor == nil {
		t.Fatalf("[E2E-TOOLS] expected doctor.tools to include bv")
	}

	if bvDoctor.Installed != bvRobot.Installed {
		t.Fatalf("[E2E-TOOLS] mismatch for bv installed: doctor=%t robot-tools=%t", bvDoctor.Installed, bvRobot.Installed)
	}

	logger.Log("[E2E-TOOLS] PASS: bv installed consistent (installed=%t)", bvRobot.Installed)
}
