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
)

type overlayResponse struct {
	Success   bool   `json:"success"`
	Session   string `json:"session"`
	Cursor    int64  `json:"cursor"`
	NoWait    bool   `json:"no_wait"`
	Launched  bool   `json:"launched"`
	Dismissed bool   `json:"dismissed"`
	PID       int    `json:"pid"`
	Error     string `json:"error"`
	ErrorCode string `json:"error_code"`
	Hint      string `json:"hint"`
	Timestamp string `json:"timestamp"`
}

func newOverlayHarness(t *testing.T, scenario string) *ScenarioHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping overlay-feed e2e in short mode")
	}

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     scenario,
		ArtifactRoot: t.TempDir(),
		Retain:       RetainAlways,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	return h
}

func decodeOverlayResponse(t *testing.T, data []byte) overlayResponse {
	t.Helper()

	var resp overlayResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		raw := strings.TrimSpace(string(data))
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start >= 0 && end > start {
			if retryErr := json.Unmarshal([]byte(raw[start:end+1]), &resp); retryErr == nil {
				return resp
			}
		}
		t.Fatalf("parse overlay response: %v\nraw: %s", err, string(data))
	}
	return resp
}

func overlayNTMBin(t *testing.T) string {
	t.Helper()

	if override := strings.TrimSpace(os.Getenv("E2E_NTM_BIN")); override != "" {
		if strings.ContainsRune(override, filepath.Separator) {
			return override
		}
		path, err := exec.LookPath(override)
		if err != nil {
			t.Skipf("E2E_NTM_BIN=%q not found on PATH: %v", override, err)
		}
		return path
	}

	path, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm binary not found in PATH; set E2E_NTM_BIN to run overlay-feed e2e")
	}
	return path
}

func runOverlayWithEnv(t *testing.T, h *ScenarioHarness, env []string, args ...string) overlayResponse {
	t.Helper()

	bin := overlayNTMBin(t)

	result, err := h.RunCommand(CommandSpec{
		Name:    "robot-overlay",
		Path:    bin,
		Args:    args,
		Env:     env,
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunCommand() error = %v\nstdout=%s\nstderr=%s", err, string(result.Stdout), string(result.Stderr))
	}
	if result.ExitCode != 0 {
		t.Fatalf("overlay exit code = %d, want 0\nstdout=%s\nstderr=%s", result.ExitCode, string(result.Stdout), string(result.Stderr))
	}
	return decodeOverlayResponse(t, result.Stdout)
}

func runOverlayInPane(t *testing.T, h *ScenarioHarness, label string, args ...string) overlayResponse {
	t.Helper()

	bin := overlayNTMBin(t)

	base := sanitizeName(label)
	if base == "" {
		base = "overlay"
	}
	stdoutPath := filepath.Join(h.Root(), base+"-stdout.json")
	donePath := filepath.Join(h.Root(), base+"-done")

	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, tmux.ShellQuote(bin))
	for _, arg := range args {
		quoted = append(quoted, tmux.ShellQuote(arg))
	}
	commandLine := strings.Join(quoted, " ")
	shellLine := fmt.Sprintf("%s > %s 2>&1; printf done > %s", commandLine, tmux.ShellQuote(stdoutPath), tmux.ShellQuote(donePath))

	target := h.SessionName()
	result, err := h.RunCommand(CommandSpec{
		Name:    "tmux-send-keys-" + base,
		Path:    tmux.BinaryPath(),
		Args:    []string{"send-keys", "-t", target, shellLine, "Enter"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("send-keys failed: %v\nstdout=%s\nstderr=%s", err, string(result.Stdout), string(result.Stderr))
	}

	var data []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(donePath); err == nil {
			data, err = os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatalf("read overlay output: %v", err)
			}
			if len(strings.TrimSpace(string(data))) == 0 {
				t.Fatalf("overlay output file %s was empty", stdoutPath)
			}
			return decodeOverlayResponse(t, data)
		}
		time.Sleep(50 * time.Millisecond)
	}

	paneResult, paneErr := h.RunCommand(CommandSpec{
		Name:         "tmux-capture-pane-" + base,
		Path:         tmux.BinaryPath(),
		Args:         []string{"capture-pane", "-t", target, "-p", "-S", "-50"},
		Timeout:      5 * time.Second,
		AllowFailure: true,
	})
	t.Fatalf("overlay command in pane did not finish within timeout\nstdout_path=%s\ndone_path=%s\ncapture_err=%v\npane=%s", stdoutPath, donePath, paneErr, strings.TrimSpace(string(paneResult.Stdout)))
	return overlayResponse{}
}

func requireTmux(t *testing.T) {
	t.Helper()
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}
}

func TestOverlayFeedRejectsNegativeCursorOutsideTmux(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_negative_cursor_outside_tmux")
	defer h.Close()

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--overlay-session", "proj", "--overlay-cursor", "-1")
	if resp.Success {
		t.Fatalf("expected failure response, got success: %+v", resp)
	}
	if resp.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("error_code = %q, want INVALID_FLAG", resp.ErrorCode)
	}
	if resp.Session != "proj" {
		t.Fatalf("session = %q, want %q", resp.Session, "proj")
	}
	if !strings.Contains(resp.Hint, "non-negative event cursor") {
		t.Fatalf("hint = %q, want non-negative cursor guidance", resp.Hint)
	}
}

func TestOverlayFeedDefaultsToCurrentSessionInsideTmux(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_current_session_default")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	resp := runOverlayInPane(t, h, "current-session-default", "--robot-overlay")
	if resp.Success {
		t.Fatalf("expected failure response in detached session, got success: %+v", resp)
	}
	if resp.Session != h.SessionName() {
		t.Fatalf("session = %q, want %q", resp.Session, h.SessionName())
	}
	if resp.ErrorCode != "INTERNAL_ERROR" {
		t.Fatalf("error_code = %q, want INTERNAL_ERROR", resp.ErrorCode)
	}
}

func TestOverlayFeedReportsMissingTargetSessionInsideTmux(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_session_not_found")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	const missingSession = "overlay-missing-session-e2e"
	resp := runOverlayInPane(t, h, "session-not-found", "--robot-overlay", "--overlay-session", missingSession)
	if resp.Success {
		t.Fatalf("expected failure response, got success: %+v", resp)
	}
	if resp.ErrorCode != "SESSION_NOT_FOUND" {
		t.Fatalf("error_code = %q, want SESSION_NOT_FOUND", resp.ErrorCode)
	}
	if resp.Session != missingSession {
		t.Fatalf("session = %q, want %q", resp.Session, missingSession)
	}
}

func TestOverlayFeedNoWaitReportsImmediatePopupFailureInsideTmux(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_no_wait_failure")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	resp := runOverlayInPane(
		t,
		h,
		"overlay-no-wait-failure",
		"--robot-overlay",
		"--overlay-session", h.SessionName(),
		"--overlay-cursor", "73",
		"--overlay-no-wait",
	)
	if resp.Success {
		t.Fatalf("expected failure response, got success: %+v", resp)
	}
	if resp.ErrorCode != "INTERNAL_ERROR" {
		t.Fatalf("error_code = %q, want INTERNAL_ERROR", resp.ErrorCode)
	}
	if resp.Cursor != 73 {
		t.Fatalf("cursor = %d, want 73", resp.Cursor)
	}
	if !resp.NoWait {
		t.Fatalf("expected no_wait=true, got %+v", resp)
	}
	if resp.Launched {
		t.Fatalf("expected launched=false on immediate popup failure, got %+v", resp)
	}
}
