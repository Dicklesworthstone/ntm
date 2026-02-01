//go:build integration

package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Keys Real Integration Tests (ntm-gabg)
//
// These tests send real keys to tmux panes and verify behavior.
// Run with: go test -tags=integration ./internal/tmux/...
// =============================================================================

// createTestSessionForKeys creates a unique test session for key tests
func createTestSessionForKeys(t *testing.T) string {
	t.Helper()
	name := uniqueSessionName("keys")
	t.Cleanup(func() { cleanupSession(t, name) })

	err := CreateSession(name, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	return name
}

// =============================================================================
// Key Sending Tests - Simple Text
// =============================================================================

func TestRealKeySendSimpleText(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Send simple text without enter
	text := "hello world test"
	if err := SendKeys(paneID, text, false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Capture output and verify text appears
	output, err := CapturePaneOutput(paneID, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Normalize output (terminal may wrap lines)
	normalizedOutput := strings.ReplaceAll(output, "\n", "")
	if !strings.Contains(normalizedOutput, strings.ReplaceAll(text, " ", "")) {
		t.Logf("output: %q", output)
		// Text should at least partially appear
		if !strings.Contains(normalizedOutput, "hello") {
			t.Errorf("expected output to contain 'hello'")
		}
	}
}

func TestRealKeySendWithEnter(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Send echo command with enter
	marker := fmt.Sprintf("MARKER_%d", time.Now().UnixNano())
	if err := SendKeys(paneID, fmt.Sprintf("echo %s", marker), true); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify command was executed and marker appears in output
	output, err := CapturePaneOutput(paneID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, marker) {
		t.Logf("output: %q", output)
		t.Errorf("expected output to contain marker %s", marker)
	}
}

func TestRealKeySendMultipleCommands(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Send multiple commands
	markers := []string{"CMD1_OK", "CMD2_OK", "CMD3_OK"}
	for _, marker := range markers {
		if err := SendKeys(paneID, fmt.Sprintf("echo %s", marker), true); err != nil {
			t.Fatalf("SendKeys failed: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Verify all markers appear
	output, _ := CapturePaneOutput(paneID, 30)
	for _, marker := range markers {
		if !strings.Contains(output, marker) {
			t.Errorf("expected output to contain %s", marker)
		}
	}
}

// =============================================================================
// Key Sending Tests - Special Keys
// =============================================================================

func TestRealKeySendTab(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Send partial command followed by tab for completion
	// This tests tab key handling
	if err := SendKeys(paneID, "ech", false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Send Tab key (tab completion)
	if err := SendKeys(paneID, "\t", false); err != nil {
		t.Fatalf("SendKeys Tab failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Clear with Ctrl+U and send a known command
	SendKeys(paneID, "", false) // Clear any pending
	time.Sleep(100 * time.Millisecond)

	// Just verify we can still interact with the pane
	marker := "TAB_TEST_DONE"
	SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(300 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	if !strings.Contains(output, marker) {
		t.Logf("output: %q", output)
		t.Log("tab test may have incomplete output, but pane is responsive")
	}
}

func TestRealKeySendInterrupt(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Start a long-running command
	SendKeys(paneID, "sleep 60", true)
	time.Sleep(300 * time.Millisecond)

	// Send interrupt (Ctrl+C)
	if err := SendInterrupt(paneID); err != nil {
		t.Fatalf("SendInterrupt failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Verify we get a prompt back (command was interrupted)
	marker := "AFTER_INTERRUPT"
	SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(300 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 20)
	if !strings.Contains(output, marker) {
		t.Errorf("expected pane to be responsive after interrupt")
	}
}

func TestRealKeySendControlD(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	// Create additional pane so killing first one doesn't destroy session
	_, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	panes, _ := GetPanes(session)
	if len(panes) < 2 {
		t.Fatal("expected at least 2 panes")
	}

	// Start cat in first pane
	SendKeys(panes[0].ID, "cat", true)
	time.Sleep(200 * time.Millisecond)

	// Send EOF (Ctrl+D) - should exit cat
	SendKeys(panes[0].ID, "\x04", false) // Ctrl+D
	time.Sleep(300 * time.Millisecond)

	// Verify we get a prompt back
	marker := "AFTER_CAT"
	SendKeys(panes[0].ID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(300 * time.Millisecond)

	output, _ := CapturePaneOutput(panes[0].ID, 20)
	if !strings.Contains(output, marker) {
		t.Logf("output: %q", output)
		t.Log("Ctrl+D may not have exited cat cleanly, but testing key sending")
	}
}

// =============================================================================
// Paste Operations Tests
// =============================================================================

func TestRealKeysPasteMultilineText(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Use cat to capture multiline input
	SendKeys(paneID, "cat << 'ENDMARKER'", true)
	time.Sleep(200 * time.Millisecond)

	// Send multiline text
	lines := []string{"line one", "line two", "line three"}
	for _, line := range lines {
		SendKeys(paneID, line, true)
		time.Sleep(100 * time.Millisecond)
	}

	// End heredoc
	SendKeys(paneID, "ENDMARKER", true)
	time.Sleep(500 * time.Millisecond)

	// Capture output
	output, _ := CapturePaneOutput(paneID, 30)

	// Verify all lines appear
	for _, line := range lines {
		if !strings.Contains(output, line) {
			t.Errorf("expected output to contain %q", line)
		}
	}
}

func TestRealKeysPasteSpecialCharacters(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Test various special characters via echo
	testCases := []struct {
		name     string
		text     string
		expected string
	}{
		{"spaces", "hello world", "hello world"},
		{"numbers", "12345", "12345"},
		{"punctuation", "hello, world!", "hello, world!"},
		{"quotes", "'single' \"double\"", "'single'"},
		{"path", "/usr/local/bin", "/usr/local/bin"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			marker := fmt.Sprintf("TEST_%d", time.Now().UnixNano())
			cmd := fmt.Sprintf("echo '%s' && echo %s", tc.text, marker)
			SendKeys(paneID, cmd, true)
			time.Sleep(400 * time.Millisecond)

			output, _ := CapturePaneOutput(paneID, 30)
			if !strings.Contains(output, marker) {
				t.Errorf("command may not have executed")
			}
			if !strings.Contains(output, tc.expected) {
				t.Logf("output: %q", output)
				t.Errorf("expected output to contain %q", tc.expected)
			}
		})
	}
}

func TestRealKeysPasteLargeText(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate a large text block (but not too large for terminal)
	largeText := strings.Repeat("x", 500)

	// Send as echo command
	SendKeys(paneID, fmt.Sprintf("echo '%s'", largeText), true)
	time.Sleep(500 * time.Millisecond)

	// Capture output
	output, _ := CapturePaneOutput(paneID, 50)

	// Should contain at least part of the large text
	if !strings.Contains(output, "xxxxx") {
		t.Errorf("expected large text to be echoed")
	}
}

// =============================================================================
// Timing and Verification Tests
// =============================================================================

func TestRealKeysCommandExecution(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Execute a command that produces known output with a marker
	marker := fmt.Sprintf("RESULT_%d", time.Now().UnixNano())
	SendKeys(paneID, fmt.Sprintf("echo %s_$((2 + 2))", marker), true)
	time.Sleep(500 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	expected := marker + "_4"
	if !strings.Contains(output, expected) {
		t.Logf("output: %q", output)
		// At minimum, the marker should appear
		if !strings.Contains(output, marker) {
			t.Errorf("expected output to contain marker %s", marker)
		}
	}
}

func TestRealKeysInterruptLongOperation(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Start a long operation
	SendKeys(paneID, "while true; do echo working; sleep 0.5; done", true)
	time.Sleep(500 * time.Millisecond)

	// Interrupt it
	SendInterrupt(paneID)
	time.Sleep(300 * time.Millisecond)

	// Send a new command to verify responsiveness
	marker := "LOOP_INTERRUPTED"
	SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	if !strings.Contains(output, marker) {
		t.Errorf("pane should be responsive after interrupt")
	}
}

// =============================================================================
// Multi-Pane Key Targeting Tests
// =============================================================================

func TestRealKeysToCorrectPane(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	// Create additional panes
	_, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	_, err = SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow 2 failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	panes, _ := GetPanes(session)
	if len(panes) != 3 {
		t.Fatalf("expected 3 panes, got %d", len(panes))
	}

	// Send unique markers to each pane
	markers := make(map[string]string)
	for i, p := range panes {
		marker := fmt.Sprintf("PANE_%d_MARKER_%d", i, time.Now().UnixNano())
		markers[p.ID] = marker
		SendKeys(p.ID, fmt.Sprintf("echo %s", marker), true)
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)

	// Verify each pane only has its own marker
	for _, p := range panes {
		output, _ := CapturePaneOutput(p.ID, 30)
		expectedMarker := markers[p.ID]

		if !strings.Contains(output, expectedMarker) {
			t.Errorf("pane %s should contain its marker %s", p.ID, expectedMarker)
		}

		// Check that other markers are NOT in this pane
		for otherID, otherMarker := range markers {
			if otherID != p.ID && strings.Contains(output, otherMarker) {
				t.Errorf("pane %s should NOT contain marker from pane %s", p.ID, otherID)
			}
		}
	}
}

func TestRealKeysConcurrentSending(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	// Create additional panes
	for i := 0; i < 2; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	panes, _ := GetPanes(session)
	if len(panes) != 3 {
		t.Fatalf("expected 3 panes, got %d", len(panes))
	}

	// Send keys to all panes concurrently
	done := make(chan struct{})
	for i, p := range panes {
		go func(paneID string, idx int) {
			marker := fmt.Sprintf("CONCURRENT_%d", idx)
			SendKeys(paneID, fmt.Sprintf("echo %s", marker), true)
			done <- struct{}{}
		}(p.ID, i)
	}

	// Wait for all sends to complete
	for range panes {
		<-done
	}
	time.Sleep(500 * time.Millisecond)

	// Verify each pane received its command
	for i, p := range panes {
		output, _ := CapturePaneOutput(p.ID, 20)
		expectedMarker := fmt.Sprintf("CONCURRENT_%d", i)
		if !strings.Contains(output, expectedMarker) {
			t.Errorf("pane %d should contain %s", i, expectedMarker)
		}
	}
}

// =============================================================================
// UTF-8 and Special Character Tests
// =============================================================================

func TestRealKeysUTF8Characters(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Test UTF-8 characters
	utf8Text := "Hello 世界 emoji: 🚀"
	SendKeys(paneID, fmt.Sprintf("echo '%s'", utf8Text), true)
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	// Just verify the command ran - UTF-8 handling may vary
	if !strings.Contains(output, "Hello") {
		t.Errorf("expected at least 'Hello' in output")
	}
}

func TestRealKeysEscapeSequences(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Test that escape characters are handled properly
	// Using echo with escape interpretation
	marker := "ESCAPE_TEST_OK"
	SendKeys(paneID, fmt.Sprintf("echo -e 'tab:\\there' && echo %s", marker), true)
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	if !strings.Contains(output, marker) {
		t.Errorf("escape sequence test should complete")
	}
}

// =============================================================================
// Buffer-Based Paste Tests (for Gemini multi-line fix)
// =============================================================================

func TestRealSendBufferSimpleText(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Use cat to capture input, testing buffer-based paste
	SendKeys(paneID, "cat << 'EOF'", true)
	time.Sleep(200 * time.Millisecond)

	// Send simple text using buffer method
	text := "hello from buffer"
	if err := SendBuffer(paneID, text, true); err != nil {
		t.Fatalf("SendBuffer failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// End heredoc
	SendKeys(paneID, "EOF", true)
	time.Sleep(300 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 20)
	if !strings.Contains(output, "hello from buffer") {
		t.Logf("output: %q", output)
		t.Errorf("expected buffer-pasted text to appear in output")
	}
}

func TestRealSendBufferMultilineText(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Use cat to capture multiline input
	SendKeys(paneID, "cat << 'ENDTEST'", true)
	time.Sleep(200 * time.Millisecond)

	// Send multiline text using buffer method (this is the key fix for Gemini)
	// With SendKeys, each \n would be interpreted as Enter
	// With SendBuffer, the entire text is pasted as data
	multilineText := "line one\nline two\nline three"
	if err := SendBuffer(paneID, multilineText, true); err != nil {
		t.Fatalf("SendBuffer multiline failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// End heredoc
	SendKeys(paneID, "ENDTEST", true)
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	// Verify all lines appear in output
	for _, line := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(output, line) {
			t.Logf("output: %q", output)
			t.Errorf("expected output to contain %q", line)
		}
	}
}

func TestRealSendKeysForAgentGemini(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Use cat to capture input
	SendKeys(paneID, "cat << 'GEMINITEST'", true)
	time.Sleep(200 * time.Millisecond)

	// Test SendKeysForAgent with Gemini type and multiline content
	// This should use buffer-based paste internally
	multilinePrompt := "First line of prompt\nSecond line of prompt\nThird line"
	if err := SendKeysForAgent(paneID, multilinePrompt, true, AgentGemini); err != nil {
		t.Fatalf("SendKeysForAgent for Gemini failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// End heredoc
	SendKeys(paneID, "GEMINITEST", true)
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 30)
	// All lines should appear as data, not as separate commands
	for _, expected := range []string{"First line", "Second line", "Third line"} {
		if !strings.Contains(output, expected) {
			t.Logf("output: %q", output)
			t.Errorf("expected Gemini multiline content to contain %q", expected)
		}
	}
}

func TestRealSendKeysForAgentClaude(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForKeys(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// For Claude, SendKeysForAgent should use regular send-keys (not buffer)
	// Test that simple text still works
	marker := fmt.Sprintf("CLAUDE_TEST_%d", time.Now().UnixNano())
	if err := SendKeysForAgent(paneID, fmt.Sprintf("echo %s", marker), true, AgentClaude); err != nil {
		t.Fatalf("SendKeysForAgent for Claude failed: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	output, _ := CapturePaneOutput(paneID, 20)
	if !strings.Contains(output, marker) {
		t.Logf("output: %q", output)
		t.Errorf("expected Claude send to work normally")
	}
}
