//go:build integration

package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// =============================================================================
// Capture Real Integration Tests (ntm-j9gq)
//
// These tests capture real output from tmux panes and verify behavior.
// Run with: go test -tags=integration ./internal/tmux/...
// =============================================================================

// createTestSessionForCapture creates a unique test session for capture tests
func createTestSessionForCapture(t *testing.T) string {
	t.Helper()
	name := uniqueSessionName("capture")
	t.Cleanup(func() { cleanupSession(t, name) })

	err := CreateSession(name, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	return name
}

// =============================================================================
// Basic Capture Tests
// =============================================================================

func TestRealCaptureBasic(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Run a command that produces known output
	marker := fmt.Sprintf("CAPTURE_BASIC_%d", time.Now().UnixNano())
	SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(400 * time.Millisecond)

	// Capture output
	output, err := CapturePaneOutput(paneID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, marker) {
		t.Logf("output: %q", output)
		t.Errorf("expected output to contain marker %s", marker)
	}
}

func TestRealCaptureFeedsBackpressureSnapshot(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	if len(panes) == 0 {
		t.Fatal("test session has no panes")
	}

	if _, err := CapturePaneOutput(panes[0].ID, 20); err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	inputs := DefaultClient.CaptureBackpressureInputs()
	snapshot := backpressure.Evaluate(inputs, backpressure.SnapshotOptions{})
	for _, surface := range snapshot.Surfaces {
		if surface.Surface == backpressure.SurfaceTmuxCapture && surface.Pane == panes[0].ID {
			return
		}
	}
	t.Fatalf("backpressure snapshot missing live capture for pane %q: %#v", panes[0].ID, snapshot.Surfaces)
}

func TestRealCaptureDifferentLineCounts(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate some output
	for i := 0; i < 5; i++ {
		SendKeys(paneID, fmt.Sprintf("echo LINE_%d", i), true)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	// Test different line counts
	testCases := []struct {
		lines    int
		minLines int
	}{
		{5, 1},
		{10, 1},
		{20, 1},
		{50, 1},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("lines_%d", tc.lines), func(t *testing.T) {
			output, err := CapturePaneOutput(paneID, tc.lines)
			if err != nil {
				t.Fatalf("CapturePaneOutput(%d) failed: %v", tc.lines, err)
			}

			lines := strings.Split(strings.TrimSpace(output), "\n")
			if len(lines) < tc.minLines {
				t.Errorf("expected at least %d lines, got %d", tc.minLines, len(lines))
			}
		})
	}
}

func TestRealCaptureIncludesPrompt(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Run a command
	marker := "PROMPT_TEST"
	SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(400 * time.Millisecond)

	// Capture should include both the command and output
	output, err := CapturePaneOutput(paneID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain the marker (from the echo output)
	if !strings.Contains(output, marker) {
		t.Errorf("output should contain marker %s", marker)
	}

	// Should contain "echo" (the command)
	if !strings.Contains(output, "echo") {
		t.Logf("output: %q", output)
		t.Log("command 'echo' not visible - may be in scrollback")
	}
}

// =============================================================================
// Scrollback Tests
// =============================================================================

func TestRealCaptureScrollbackSmall(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate known amount of output
	markers := make([]string, 10)
	for i := 0; i < 10; i++ {
		markers[i] = fmt.Sprintf("SCROLL_LINE_%d", i)
		SendKeys(paneID, fmt.Sprintf("echo %s", markers[i]), true)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	// Capture with 100 lines (more than we generated)
	output, err := CapturePaneOutput(paneID, 100)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain at least some of our markers
	foundCount := 0
	for _, marker := range markers {
		if strings.Contains(output, marker) {
			foundCount++
		}
	}

	if foundCount < 5 {
		t.Errorf("expected to find at least 5 markers, found %d", foundCount)
	}
}

func TestRealCaptureScrollbackLarge(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate more output than visible area
	numLines := 30
	markers := make([]string, numLines)
	for i := 0; i < numLines; i++ {
		markers[i] = fmt.Sprintf("BIG_SCROLL_%d", i)
		SendKeys(paneID, fmt.Sprintf("echo %s", markers[i]), true)
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// Capture with 500 lines
	output, err := CapturePaneOutput(paneID, 500)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain recent markers
	foundCount := 0
	for i := numLines - 10; i < numLines; i++ {
		if strings.Contains(output, markers[i]) {
			foundCount++
		}
	}

	if foundCount < 5 {
		t.Errorf("expected to find at least 5 recent markers, found %d", foundCount)
	}
}

func TestRealCaptureEmptyPane(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	// Create a new pane (will have minimal content)
	paneID, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Capture from relatively fresh pane
	output, err := CapturePaneOutput(paneID, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should not error even with minimal content
	// Output might contain just the prompt or be mostly empty
	t.Logf("empty pane capture length: %d", len(output))
}

func TestRealCaptureMoreThanAvailable(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate just a few lines
	for i := 0; i < 3; i++ {
		SendKeys(paneID, fmt.Sprintf("echo short_%d", i), true)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	// Request more lines than available
	output, err := CapturePaneOutput(paneID, 2000)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should return what's available without error
	if len(output) == 0 {
		t.Error("expected some output")
	}

	// Should contain our markers
	if !strings.Contains(output, "short_0") {
		t.Error("expected output to contain our markers")
	}
}

// =============================================================================
// Content Verification Tests
// =============================================================================

func TestRealCaptureKnownCommand(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Run a command with predictable output
	marker := fmt.Sprintf("KNOWN_%d", time.Now().UnixNano())
	SendKeys(paneID, fmt.Sprintf("echo %s && echo DONE", marker), true)
	time.Sleep(500 * time.Millisecond)

	output, err := CapturePaneOutput(paneID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain both the marker and DONE
	if !strings.Contains(output, marker) {
		t.Errorf("output should contain marker %s", marker)
	}
	if !strings.Contains(output, "DONE") {
		t.Errorf("output should contain DONE")
	}
}

func TestRealCaptureUTF8Content(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Echo UTF-8 content
	SendKeys(paneID, "echo 'Hello 世界'", true)
	time.Sleep(400 * time.Millisecond)

	output, err := CapturePaneOutput(paneID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain at least the ASCII part
	if !strings.Contains(output, "Hello") {
		t.Error("output should contain 'Hello'")
	}

	// UTF-8 characters may or may not be captured correctly depending on terminal
	if strings.Contains(output, "世界") {
		t.Log("UTF-8 characters captured successfully")
	} else {
		t.Log("UTF-8 characters may not have been captured correctly")
	}
}

func TestRealCaptureMultilineOutput(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate multiline output
	SendKeys(paneID, "echo -e 'LINE1\\nLINE2\\nLINE3'", true)
	time.Sleep(400 * time.Millisecond)

	output, err := CapturePaneOutput(paneID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain all lines
	lines := []string{"LINE1", "LINE2", "LINE3"}
	for _, line := range lines {
		if !strings.Contains(output, line) {
			t.Errorf("output should contain %s", line)
		}
	}
}

func TestRealCaptureTiming(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Run command that takes a moment
	marker := fmt.Sprintf("TIMED_%d", time.Now().UnixNano())
	SendKeys(paneID, fmt.Sprintf("sleep 0.2 && echo %s", marker), true)

	// Capture immediately (before command completes)
	outputBefore, _ := CapturePaneOutput(paneID, 30)

	// Wait for command to complete
	time.Sleep(500 * time.Millisecond)

	// Capture after completion
	outputAfter, _ := CapturePaneOutput(paneID, 30)

	// After should contain the marker
	if !strings.Contains(outputAfter, marker) {
		t.Errorf("output after completion should contain marker")
	}

	// Before might not contain it (timing dependent)
	t.Logf("before capture contained marker: %v", strings.Contains(outputBefore, marker))
}

// =============================================================================
// Capture with ANSI Escape Sequences
// =============================================================================

func TestRealCaptureWithColors(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Send text with color codes
	marker := "COLOR_TEST"
	SendKeys(paneID, fmt.Sprintf("echo -e '\\033[31m%s\\033[0m'", marker), true)
	time.Sleep(400 * time.Millisecond)

	output, err := CapturePaneOutput(paneID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain the text (escape codes may or may not be stripped)
	if !strings.Contains(output, marker) {
		t.Errorf("output should contain %s", marker)
	}
}

// =============================================================================
// Multi-Pane Capture Tests
// =============================================================================

func TestRealCaptureMultiplePanes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	// Create additional panes
	_, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	panes, _ := GetPanes(session)
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}

	// Send unique content to each pane
	markers := make(map[string]string)
	for i, p := range panes {
		marker := fmt.Sprintf("PANE_%d_%d", i, time.Now().UnixNano())
		markers[p.ID] = marker
		SendKeys(p.ID, fmt.Sprintf("echo %s", marker), true)
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	// Capture each pane and verify isolation
	for _, p := range panes {
		output, err := CapturePaneOutput(p.ID, 30)
		if err != nil {
			t.Fatalf("CapturePaneOutput for pane %s failed: %v", p.ID, err)
		}

		expectedMarker := markers[p.ID]
		if !strings.Contains(output, expectedMarker) {
			t.Errorf("pane %s should contain its marker %s", p.ID, expectedMarker)
		}

		// Verify other pane's marker is NOT in this capture
		for otherID, otherMarker := range markers {
			if otherID != p.ID && strings.Contains(output, otherMarker) {
				t.Errorf("pane %s should NOT contain marker from pane %s", p.ID, otherID)
			}
		}
	}
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestRealCaptureNonExistentPane(t *testing.T) {
	skipIfNoTmux(t)

	// Try to capture from non-existent pane
	_, err := CapturePaneOutput("%999999", 10)
	if err == nil {
		t.Error("CapturePaneOutput should fail for non-existent pane")
	}
}

func TestRealCaptureZeroLines(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Request 0 lines - should still work (implementation may adjust)
	output, err := CapturePaneOutput(paneID, 0)
	if err != nil {
		// Some implementations may reject 0 lines
		t.Logf("0 lines request: %v", err)
		return
	}

	t.Logf("0 lines capture returned %d bytes", len(output))
}

func TestRealCaptureNegativeLines(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForCapture(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Request negative lines - implementation may handle or reject
	output, err := CapturePaneOutput(paneID, -10)
	if err != nil {
		t.Logf("negative lines request handled: %v", err)
		return
	}

	t.Logf("negative lines capture returned %d bytes", len(output))
}
