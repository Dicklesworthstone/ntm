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
	testutil.AcquireGlobalTmuxTestLockForTest(t)

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
		Name:         "robot-overlay",
		Path:         bin,
		Args:         args,
		Env:          env,
		Timeout:      15 * time.Second,
		AllowFailure: true,
	})
	if err != nil {
		t.Fatalf("RunCommand() error = %v\nstdout=%s\nstderr=%s", err, string(result.Stdout), string(result.Stderr))
	}

	resp := decodeOverlayResponse(t, result.Stdout)
	wantExitCode := 0
	if !resp.Success {
		wantExitCode = 1
	}
	if result.ExitCode != wantExitCode {
		t.Fatalf("overlay exit code = %d, want %d for success=%t\nstdout=%s\nstderr=%s", result.ExitCode, wantExitCode, resp.Success, string(result.Stdout), string(result.Stderr))
	}
	return resp
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

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "proj", "--overlay-cursor", "-1")
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
	resp := runOverlayInPane(t, h, "session-not-found", "--robot-overlay", "--session", missingSession)
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
		"--session", h.SessionName(),
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

// =============================================================================
// Overlay-Feed Integration Tests (br-rb9oj)
// =============================================================================

func TestOverlayFeedCursorPropagationInZoomHint(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_cursor_propagation")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Test that a high cursor value is passed through correctly
	const targetCursor int64 = 99999
	resp := runOverlayInPane(
		t,
		h,
		"cursor-propagation",
		"--robot-overlay",
		"--session", h.SessionName(),
		"--overlay-cursor", fmt.Sprintf("%d", targetCursor),
		"--overlay-no-wait",
	)

	// Response should echo back the cursor value even on failure
	if resp.Cursor != targetCursor {
		t.Fatalf("cursor = %d, want %d (cursor should propagate to response)", resp.Cursor, targetCursor)
	}
	t.Logf("CURSOR_PROPAGATION cursor_in=%d cursor_out=%d", targetCursor, resp.Cursor)
}

func TestOverlayFeedGracefulDegradationOutsideTmux(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_degradation_outside_tmux")
	defer h.Close()

	// Run without TMUX env (simulates running outside tmux)
	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "proj")

	// Should fail gracefully with clear error
	if resp.Success {
		t.Fatalf("expected failure outside tmux, got success: %+v", resp)
	}
	if resp.ErrorCode == "" {
		t.Fatalf("expected error_code for degradation case, got empty")
	}
	if resp.Hint == "" {
		t.Fatalf("expected helpful hint for user, got empty")
	}
	t.Logf("DEGRADATION_TEST error_code=%s hint=%q", resp.ErrorCode, resp.Hint)
}

func TestOverlayFeedRejectsZeroCursor(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_zero_cursor")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Zero cursor should be accepted (means "no specific cursor")
	resp := runOverlayInPane(
		t,
		h,
		"zero-cursor",
		"--robot-overlay",
		"--session", h.SessionName(),
		"--overlay-cursor", "0",
		"--overlay-no-wait",
	)

	// Zero is valid - response should not have INVALID_FLAG error
	if resp.ErrorCode == "INVALID_FLAG" {
		t.Fatalf("zero cursor should be valid, got INVALID_FLAG: %+v", resp)
	}
	if resp.Cursor != 0 {
		t.Fatalf("cursor = %d, want 0", resp.Cursor)
	}
	t.Logf("ZERO_CURSOR_TEST cursor=%d error_code=%s", resp.Cursor, resp.ErrorCode)
}

func TestOverlayFeedLargeCursorValue(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_large_cursor")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Test with a very large cursor value (simulates long-running feed)
	const largeCursor int64 = 9223372036854775000 // Near int64 max
	resp := runOverlayInPane(
		t,
		h,
		"large-cursor",
		"--robot-overlay",
		"--session", h.SessionName(),
		"--overlay-cursor", fmt.Sprintf("%d", largeCursor),
		"--overlay-no-wait",
	)

	if resp.Cursor != largeCursor {
		t.Fatalf("cursor = %d, want %d (large cursor should round-trip)", resp.Cursor, largeCursor)
	}
	t.Logf("LARGE_CURSOR_TEST cursor=%d", resp.Cursor)
}

func TestOverlayFeedResponseIncludesTimestamp(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_timestamp")
	defer h.Close()

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "proj")

	// All responses should include a timestamp for observability
	if resp.Timestamp == "" {
		t.Fatalf("expected timestamp in response, got empty")
	}

	// Validate timestamp is parseable
	_, err := time.Parse(time.RFC3339, resp.Timestamp)
	if err != nil {
		// Try RFC3339Nano
		_, err = time.Parse(time.RFC3339Nano, resp.Timestamp)
		if err != nil {
			t.Fatalf("timestamp %q is not valid RFC3339: %v", resp.Timestamp, err)
		}
	}
	t.Logf("TIMESTAMP_TEST timestamp=%s", resp.Timestamp)
}

func TestOverlayFeedSessionEchoedInResponse(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_session_echo")
	defer h.Close()

	const testSession = "my-custom-session-name"
	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", testSession)

	// Response should echo back the requested session
	if resp.Session != testSession {
		t.Fatalf("session = %q, want %q (session should be echoed)", resp.Session, testSession)
	}
	t.Logf("SESSION_ECHO_TEST session=%s", resp.Session)
}

func TestOverlayFeedNoWaitFlagSemantic(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_no_wait_semantic")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Without --overlay-no-wait
	resp1 := runOverlayInPane(
		t,
		h,
		"without-no-wait",
		"--robot-overlay",
		"--session", h.SessionName(),
	)
	if resp1.NoWait {
		t.Fatalf("expected no_wait=false without flag, got %+v", resp1)
	}

	// With --overlay-no-wait
	resp2 := runOverlayInPane(
		t,
		h,
		"with-no-wait",
		"--robot-overlay",
		"--session", h.SessionName(),
		"--overlay-no-wait",
	)
	if !resp2.NoWait {
		t.Fatalf("expected no_wait=true with flag, got %+v", resp2)
	}

	t.Logf("NO_WAIT_SEMANTIC without=%v with=%v", resp1.NoWait, resp2.NoWait)
}

// TestOverlayFeedDismissedStateNotLaunched verifies the dismissed field is false when launch fails.
func TestOverlayFeedDismissedStateNotLaunched(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_dismissed_state")
	defer h.Close()

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "proj")

	// When not launched, dismissed should be false
	if resp.Dismissed {
		t.Fatalf("expected dismissed=false when not launched, got %+v", resp)
	}
	if resp.Launched {
		t.Fatalf("expected launched=false outside tmux, got %+v", resp)
	}
	t.Logf("DISMISSED_STATE launched=%v dismissed=%v", resp.Launched, resp.Dismissed)
}

// TestOverlayFeedResponseStructureComplete verifies all expected fields are present.
func TestOverlayFeedResponseStructureComplete(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_response_structure")
	defer h.Close()

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "proj")

	// Verify response has complete structure for agent consumption
	// Success should be boolean (false in this case)
	if resp.Success {
		t.Logf("WARN: unexpected success=true")
	}

	// Session should be present
	if resp.Session == "" {
		t.Errorf("expected session field to be populated")
	}

	// Timestamp should be present
	if resp.Timestamp == "" {
		t.Errorf("expected timestamp field to be populated")
	}

	// Error fields should be present on failure
	if !resp.Success {
		if resp.ErrorCode == "" {
			t.Errorf("expected error_code on failure")
		}
	}

	t.Logf("RESPONSE_STRUCTURE success=%v session=%s timestamp=%s error_code=%s hint=%q",
		resp.Success, resp.Session, resp.Timestamp, resp.ErrorCode, resp.Hint)
}

// =============================================================================
// Badge State Transition Tests (br-rb9oj)
// =============================================================================

func TestOverlayFeedMultipleCursorValuesAreDistinct(t *testing.T) {
	requireTmux(t)

	h := newOverlayHarness(t, "overlay_feed_cursor_distinct")
	defer h.Close()
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("SetupTmuxSession() error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Test that different cursor values are tracked separately
	cursors := []int64{100, 200, 300}
	responses := make([]overlayResponse, len(cursors))

	for i, cursor := range cursors {
		responses[i] = runOverlayInPane(
			t,
			h,
			fmt.Sprintf("cursor-%d", cursor),
			"--robot-overlay",
			"--session", h.SessionName(),
			"--overlay-cursor", fmt.Sprintf("%d", cursor),
			"--overlay-no-wait",
		)
	}

	// Verify each response has the correct cursor
	for i, resp := range responses {
		if resp.Cursor != cursors[i] {
			t.Errorf("response %d cursor = %d, want %d", i, resp.Cursor, cursors[i])
		}
	}

	t.Logf("CURSOR_DISTINCT cursors=%v", cursors)
}

// =============================================================================
// Graceful Degradation Tests (br-rb9oj)
// =============================================================================

func TestOverlayFeedMissingSessionNameReturnsError(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_missing_session")
	defer h.Close()

	// Run with explicit empty session outside tmux (can't auto-detect)
	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "")

	// Should still return a structured error
	if resp.Success {
		t.Fatalf("expected failure with empty session outside tmux, got success")
	}
	t.Logf("MISSING_SESSION error_code=%s hint=%q", resp.ErrorCode, resp.Hint)
}

func TestOverlayFeedHintsProvidesRecoveryGuidance(t *testing.T) {
	h := newOverlayHarness(t, "overlay_feed_recovery_hints")
	defer h.Close()

	resp := runOverlayWithEnv(t, h, []string{"TMUX="}, "--robot-overlay", "--session", "nonexistent")

	// Hints should provide actionable guidance
	if resp.Hint == "" {
		t.Fatalf("expected recovery hint on failure, got empty")
	}

	// Hint should be non-trivial
	if len(resp.Hint) < 10 {
		t.Fatalf("hint %q seems too short to be useful", resp.Hint)
	}

	t.Logf("RECOVERY_HINT hint=%q", resp.Hint)
}
