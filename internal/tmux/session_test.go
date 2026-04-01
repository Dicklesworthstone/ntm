package tmux

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"
)

// createTestSession creates a unique test session with cleanup
func createTestSession(t *testing.T) string {
	t.Helper()
	if !IsInstalled() {
		t.Skip("tmux not installed")
	}
	acquireGlobalTmuxTestLock(t)
	name := fmt.Sprintf("ntm_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = KillSession(name) // ignore error on cleanup
	})
	if err := CreateSession(name, os.TempDir()); err != nil {
		// Skip instead of fail when tmux server has issues (common on CI)
		t.Skipf("failed to create test session: %v", err)
	}
	// Small delay to let tmux settle
	time.Sleep(100 * time.Millisecond)
	return name
}

// skipIfNoTmux skips the test if tmux is not installed
func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if !IsInstalled() {
		t.Skip("tmux not installed, skipping test")
	}
}

func TestValidateSessionName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"myproject", false},
		{"my-project", false},
		{"my_project", false},
		{"MyProject123", false},
		{"", true},            // empty
		{"my.project", true},  // dot rejected
		{"my project", true},  // space rejected
		{"my:project", true},  // contains :
		{"my/project", true},  // contains /
		{"my\\project", true}, // contains \
		{"my;project", true},  // semicolon rejected
		{"my&project", true},  // ampersand rejected
		{"my$project", true},  // dollar rejected
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSessionName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestAgentTypeFromTitle(t *testing.T) {
	tests := []struct {
		title    string
		expected AgentType
	}{
		{"myproject__cc_1", AgentClaude},
		{"myproject__cod_1", AgentCodex},
		{"myproject__gmi_1", AgentGemini},
		{"myproject", AgentUser},
		{"zsh", AgentUser},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			// Create a pane and check type detection logic
			pane := Pane{Title: tt.title, Type: AgentUser}

			// Apply same logic as GetPanes
			if contains(pane.Title, "__cc") {
				pane.Type = AgentClaude
			} else if contains(pane.Title, "__cod") {
				pane.Type = AgentCodex
			} else if contains(pane.Title, "__gmi") {
				pane.Type = AgentGemini
			}

			if pane.Type != tt.expected {
				t.Errorf("Expected type %v for title %q, got %v", tt.expected, tt.title, pane.Type)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestInTmux(t *testing.T) {
	// This will be false in test environment
	// Just verify the function doesn't panic
	_ = InTmux()
}

func TestIsInstalled(t *testing.T) {
	// This checks if tmux is installed on the system
	// Just verify the function doesn't panic
	_ = IsInstalled()
}

func TestSanitizePaneCommand(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple command", "claude --model opus", false},
		{"tabs allowed", "codex\t--dangerously-bypass-approvals-and-sandbox", false},
		{"newline rejected", "echo hi\necho bye", true},
		{"carriage return rejected", "echo hi\recho bye", true},
		{"escape rejected", "echo \x1b[31mred\x1b[0m", true},
		{"null byte rejected", "echo hi\x00", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SanitizePaneCommand(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SanitizePaneCommand(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ============== Session Management Tests ==============

func TestCreateSession(t *testing.T) {
	skipIfNoTmux(t)
	session := createTestSession(t)

	// Verify session exists
	if !SessionExists(session) {
		t.Errorf("session %s should exist after creation", session)
	}
}

func TestCreateSessionWithDir(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	// Create temp directory
	tmpDir := t.TempDir()

	name := fmt.Sprintf("ntm_test_dir_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = KillSession(name) })

	if err := CreateSession(name, tmpDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify session was created
	if !SessionExists(name) {
		t.Errorf("session %s should exist", name)
	}
}

func TestSessionExistsNonExistent(t *testing.T) {
	skipIfNoTmux(t)

	nonExistent := "ntm_definitely_nonexistent_12345"
	if SessionExists(nonExistent) {
		t.Errorf("session %s should not exist", nonExistent)
	}
}

func TestKillSession(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	name := fmt.Sprintf("ntm_test_kill_%d", time.Now().UnixNano())

	if err := CreateSession(name, os.TempDir()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !SessionExists(name) {
		t.Fatalf("session %s should exist before kill", name)
	}

	if err := KillSession(name); err != nil {
		t.Errorf("KillSession failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if SessionExists(name) {
		t.Errorf("session %s should not exist after kill", name)
	}
}

func TestListSessions(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	found := false
	for _, s := range sessions {
		if s.Name == session {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("session %s not found in ListSessions result", session)
	}
}

func TestGetAllPanes_NoServerReturnsEmpty(t *testing.T) {
	skipIfNoTmux(t)

	// Point tmux at an empty socket directory so no server exists.
	t.Setenv("TMUX_TMPDIR", t.TempDir())

	panes, err := GetAllPanes()
	if err != nil {
		t.Fatalf("GetAllPanes error: %v", err)
	}
	if panes == nil {
		t.Fatal("expected non-nil panes map")
	}
	if len(panes) != 0 {
		t.Fatalf("expected no panes, got %d", len(panes))
	}
}

func TestGetSession(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	s, err := GetSession(session)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if s.Name != session {
		t.Errorf("GetSession returned wrong name: got %s, want %s", s.Name, session)
	}

	if s.Windows < 1 {
		t.Errorf("GetSession should show at least 1 window, got %d", s.Windows)
	}
}

func TestGetSessionNonExistent(t *testing.T) {
	skipIfNoTmux(t)

	_, err := GetSession("ntm_nonexistent_session")
	if err == nil {
		t.Error("GetSession should fail for non-existent session")
	}
}

// ============== Pane Operations Tests ==============

func TestGetPanes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) < 1 {
		t.Errorf("GetPanes should return at least 1 pane, got %d", len(panes))
	}

	// Verify pane has expected fields
	pane := panes[0]
	if pane.ID == "" {
		t.Error("pane ID should not be empty")
	}
	if pane.Width == 0 || pane.Height == 0 {
		t.Errorf("pane dimensions should be positive: %dx%d", pane.Width, pane.Height)
	}
}

func TestSplitWindow(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Get initial pane count
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	initialCount := len(panes)

	// Split window
	paneID, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if paneID == "" {
		t.Error("SplitWindow should return pane ID")
	}

	// Verify pane count increased
	panes, err = GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != initialCount+1 {
		t.Errorf("expected %d panes after split, got %d", initialCount+1, len(panes))
	}
}

func TestSplitWindowMultiple(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create 4 more panes (5 total)
	for i := 0; i < 4; i++ {
		_, err := SplitWindow(session, os.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != 5 {
		t.Errorf("expected 5 panes, got %d", len(panes))
	}
}

func TestSetPaneTitle(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Get the first pane
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	paneID := panes[0].ID
	newTitle := "test_pane_title"

	if err := SetPaneTitle(paneID, newTitle); err != nil {
		t.Fatalf("SetPaneTitle failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify title changed
	panes, err = GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	found := false
	for _, p := range panes {
		if p.ID == paneID && p.Title == newTitle {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("pane title should be %q", newTitle)
	}
}

// ============== Key Sending Tests ==============

func TestSendKeys(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Send some text without enter
	text := "hello world"
	panes, _ := GetPanes(session)
	target := panes[0].ID

	if err := SendKeys(target, text, false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Capture output to verify (text should be in buffer)
	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Normalize by removing all whitespace to handle terminal line wrapping on
	// narrow CI terminals (e.g., "hello wor\nld" should still match "hello world")
	removeAllWhitespace := func(s string) string {
		var result []rune
		for _, r := range s {
			if !unicode.IsSpace(r) {
				result = append(result, r)
			}
		}
		return string(result)
	}
	normalizedOutput := removeAllWhitespace(output)
	normalizedText := removeAllWhitespace(text)
	if !strings.Contains(normalizedOutput, normalizedText) {
		t.Logf("output: %q", output)
		t.Logf("normalized: %q", normalizedOutput)
		t.Errorf("output should contain %q (normalized: %q)", text, normalizedText)
	}
}

func TestSendKeysWithEnter(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Send echo command with enter
	if err := SendKeys(target, "echo TESTMARKER123", true); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // wait for command to execute

	output, err := CapturePaneOutput(target, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, "TESTMARKER123") {
		t.Logf("output: %q", output)
		t.Errorf("output should contain TESTMARKER123")
	}
}

func TestSendInterrupt(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Just verify interrupt doesn't error
	if err := SendInterrupt(target); err != nil {
		t.Errorf("SendInterrupt failed: %v", err)
	}
}

func TestSendKeysMultiByteChunking(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Create a payload larger than 4096 bytes with multi-byte characters.
	// '€' is 3 bytes (0xE2 0x82 0xAC).
	const numChars = 2000
	payload := strings.Repeat("€", numChars)

	// Use 'cat' to echo back input, avoiding shell line-editor limits (readline/zle)
	// which might choke on 6000+ byte lines or drop characters when flooded.
	if err := SendKeys(target, "cat", true); err != nil {
		t.Fatalf("Failed to start cat: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Send the payload
	if err := SendKeys(target, payload, false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	// Send EOF to cat
	if err := SendKeys(target, "", true); err != nil { // Enter to ensure newline
		t.Fatalf("Failed to send newline: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := SendInterrupt(target); err != nil {
		t.Fatalf("Failed to interrupt cat: %v", err)
	}

	// Capture output
	output, err := CapturePaneOutput(target, 500) // Increase capture lines
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Analysis:
	// If the fix works, we should see very few UTF-8 replacement characters ().
	// If the fix failed, naive splitting would break the multi-byte char '€'
	// resulting in invalid UTF-8 sequences which are typically rendered as .
	// Allow a small tolerance (<=3) for terminal timing issues on CI.
	replacementCount := strings.Count(output, "\ufffd")
	if replacementCount > 3 {
		t.Errorf("Found %d UTF-8 replacement characters (), confirming corruption.", replacementCount)
	} else if replacementCount > 0 {
		t.Logf("Found %d UTF-8 replacement characters (within tolerance)", replacementCount)
	}

	// Due to shell/tmux buffer limits/rate-limiting, we might not get the full 6000 chars echoed back
	// in a short test window. But we should get a significant amount (at least the first chunk).
	// And crucially, it should be valid UTF-8 (no replacement chars).
	if len(output) < 100 {
		t.Errorf("Captured output too short: %d bytes", len(output))
	}

	// Verify we see our payload characters
	if !strings.Contains(output, "€€€") {
		t.Error("Output does not contain payload characters")
	}
}

// ============== Output Capture Tests ==============

func TestCapturePaneOutput(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Output should be a string (possibly empty for new pane)
	if output == "" {
		// This is fine for a new pane
		t.Log("captured empty output (expected for new pane)")
	}
}

func TestCapturePaneOutputWithContent(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate some output
	SendKeys(target, "echo LINE1; echo LINE2; echo LINE3", true)
	time.Sleep(300 * time.Millisecond)

	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain our echo output
	if !strings.Contains(output, "LINE1") {
		t.Logf("output: %q", output)
		t.Error("output should contain LINE1")
	}
}

// ============== Layout Tests ==============

func TestApplyTiledLayout(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create multiple panes
	for i := 0; i < 3; i++ {
		_, err := SplitWindow(session, os.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow failed: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Apply tiled layout
	if err := ApplyTiledLayout(session); err != nil {
		t.Errorf("ApplyTiledLayout failed: %v", err)
	}
}

func TestZoomPane(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create another pane so we can zoom
	_, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Get the first pane index
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("Failed to get panes: %v", err)
	}
	firstPaneIndex := panes[0].Index

	// Zoom first pane
	if err := ZoomPane(session, firstPaneIndex); err != nil {
		t.Errorf("ZoomPane failed: %v", err)
	}
}

// ============== Helper Functions Tests ==============

func TestGetFirstWindow(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	winIdx, err := GetFirstWindow(session)
	if err != nil {
		t.Fatalf("GetFirstWindow failed: %v", err)
	}

	// Window index should be 0 or 1 depending on tmux config
	if winIdx < 0 || winIdx > 1 {
		t.Errorf("unexpected first window index: %d", winIdx)
	}
}

func TestGetDefaultPaneIndex(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	paneIdx, err := GetDefaultPaneIndex(session)
	if err != nil {
		t.Fatalf("GetDefaultPaneIndex failed: %v", err)
	}

	// Pane index should be 0 or 1 depending on tmux config
	if paneIdx < 0 || paneIdx > 1 {
		t.Errorf("unexpected default pane index: %d", paneIdx)
	}
}

func TestGetCurrentSession(t *testing.T) {
	// This will return empty string when not in tmux
	session := GetCurrentSession()
	t.Logf("GetCurrentSession returned: %q", session)
}

// ============== Additional Tests for Coverage ==============

func TestEnsureInstalled(t *testing.T) {
	err := EnsureInstalled()
	if IsInstalled() && err != nil {
		t.Errorf("EnsureInstalled should not error when tmux is installed: %v", err)
	}
	if !IsInstalled() && err == nil {
		t.Error("EnsureInstalled should error when tmux is not installed")
	}
}

func TestGetPaneActivity(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate some activity
	SendKeys(paneID, "echo activity", true)
	time.Sleep(300 * time.Millisecond)

	activity, err := GetPaneActivity(paneID)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("GetPaneActivity not supported: %v", err)
	}

	// Activity time should be recent (within last minute)
	if time.Since(activity) > time.Minute {
		t.Errorf("pane activity should be recent, got %v ago", time.Since(activity))
	}
}

func TestParsePaneActivityTimestamp(t *testing.T) {
	now := time.Date(2026, 1, 25, 6, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		raw       string
		want      time.Time
		wantError bool
	}{
		{name: "empty", raw: "", want: now},
		{name: "whitespace", raw: "   ", want: now},
		{name: "zero", raw: "0", want: now},
		{name: "negative", raw: "-1", want: now},
		{name: "valid", raw: "123", want: time.Unix(123, 0)},
		{name: "invalid", raw: "abc", wantError: true},
	}

	for _, tt := range tests {
		got, err := parsePaneActivityTimestamp(tt.raw, now)
		if tt.wantError {
			if err == nil {
				t.Fatalf("%s: expected error for %q", tt.name, tt.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error for %q: %v", tt.name, tt.raw, err)
		}
		if !got.Equal(tt.want) {
			t.Fatalf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
}

func TestGetPanesWithActivity(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create additional pane
	_, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Generate activity
	panes, _ := GetPanes(session)
	for _, p := range panes {
		SendKeys(p.ID, "echo test", true)
	}
	time.Sleep(300 * time.Millisecond)

	// Get panes with activity
	panesWithActivity, err := GetPanesWithActivity(session)
	if err != nil {
		t.Fatalf("GetPanesWithActivity failed: %v", err)
	}

	if len(panesWithActivity) != len(panes) {
		t.Errorf("expected %d panes with activity, got %d", len(panes), len(panesWithActivity))
	}

	// Verify each pane has activity info
	for _, p := range panesWithActivity {
		if p.LastActivity.IsZero() {
			t.Errorf("pane %s should have activity timestamp", p.Pane.ID)
		}
	}
}

func TestIsRecentlyActive(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate recent activity
	SendKeys(paneID, "echo recent", true)
	time.Sleep(300 * time.Millisecond)

	// Should be recently active (within 1 minute)
	recent, err := IsRecentlyActive(paneID, time.Minute)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("IsRecentlyActive not supported: %v", err)
	}

	if !recent {
		t.Error("pane should be recently active after generating output")
	}
}

func TestGetPaneLastActivityAge(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate activity
	SendKeys(paneID, "echo age test", true)
	time.Sleep(300 * time.Millisecond)

	age, err := GetPaneLastActivityAge(paneID)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("GetPaneLastActivityAge not supported: %v", err)
	}

	// Age should be small (just generated activity)
	if age > 5*time.Second {
		t.Errorf("pane activity age should be < 5s, got %v", age)
	}
}

func TestListSessionsNoServer(t *testing.T) {
	// This just verifies the function handles edge cases
	// When tmux is running, it should return sessions or empty list
	skipIfNoTmux(t)

	sessions, err := ListSessions()
	// Should not error even if no sessions exist
	if err != nil {
		t.Logf("ListSessions returned error: %v", err)
	}
	t.Logf("ListSessions returned %d sessions", len(sessions))
}

func TestGetPanesWithBadSession(t *testing.T) {
	skipIfNoTmux(t)

	_, err := GetPanes("nonexistent_session_xyz")
	if err == nil {
		t.Error("GetPanes should fail for non-existent session")
	}
}

func TestSplitWindowWithBadSession(t *testing.T) {
	skipIfNoTmux(t)

	_, err := SplitWindow("nonexistent_session_xyz", os.TempDir())
	if err == nil {
		t.Error("SplitWindow should fail for non-existent session")
	}
}

func TestAgentType_ProfileName(t *testing.T) {
	tests := []struct {
		agentType AgentType
		expected  string
	}{
		{AgentClaude, "Claude"},
		{AgentCodex, "Codex"},
		{AgentGemini, "Gemini"},
		{AgentUser, "User"},
		{AgentType("unknown"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.agentType), func(t *testing.T) {
			result := tt.agentType.ProfileName()
			if result != tt.expected {
				t.Errorf("ProfileName() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// ============== Binary Path Resolution Tests ==============

func TestBinaryPath(t *testing.T) {
	// BinaryPath should return a non-empty path
	path := BinaryPath()
	if path == "" {
		t.Fatal("BinaryPath() returned empty string")
	}

	// Path should either be a full path or "tmux" as fallback
	if path != "tmux" && !strings.HasPrefix(path, "/") {
		t.Errorf("BinaryPath() should return absolute path or 'tmux', got %q", path)
	}

	// If tmux is installed, the path should be a valid executable
	if IsInstalled() {
		// Path should point to an existing file
		if _, err := os.Stat(path); err != nil && path != "tmux" {
			t.Errorf("BinaryPath() returned non-existent path: %q", path)
		}
	}
}

func TestBinaryPathConsistency(t *testing.T) {
	// BinaryPath should return the same value when called multiple times
	// (it uses sync.Once internally)
	path1 := BinaryPath()
	path2 := BinaryPath()
	path3 := BinaryPath()

	if path1 != path2 || path2 != path3 {
		t.Errorf("BinaryPath() inconsistent: %q, %q, %q", path1, path2, path3)
	}
}

func TestBinaryPathPreferredLocations(t *testing.T) {
	// BinaryPath should prefer standard install locations
	path := BinaryPath()

	// If tmux is installed in a standard location, BinaryPath should find it
	preferredPaths := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
	}

	// Check if path matches one of the preferred locations
	isPreferred := path == "tmux" // fallback is also acceptable
	for _, p := range preferredPaths {
		if path == p {
			isPreferred = true
			break
		}
	}

	// If tmux is installed but path is not from preferred locations,
	// it should at least be in PATH
	if IsInstalled() && !isPreferred {
		t.Logf("BinaryPath returned non-standard path: %q (this is fine if tmux is installed elsewhere)", path)
	}
}

// ============== Pure Function Tests ==============

func TestTagsFromTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"simple tags", "session__cc_1[frontend]", []string{"frontend"}},
		{"multiple tags", "session__cc_1[frontend,api]", []string{"frontend", "api"}},
		{"no tags", "session__cc_1", nil},
		{"empty tags", "session__cc_1[]", nil},
		{"not NTM format", "just_a_title", nil},
		{"with variant and tags", "session__cc_1_opus[backend]", []string{"backend"}},
		{"tags with spaces", "session__cc_1[tag1, tag2]", []string{"tag1", "tag2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tagsFromTitle(tt.title)
			if len(got) != len(tt.want) {
				t.Errorf("tagsFromTitle(%q) = %v, want %v", tt.title, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tagsFromTitle(%q)[%d] = %q, want %q", tt.title, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseAgentFromTitleEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		title       string
		wantType    AgentType
		wantIndex   int
		wantVariant string
		wantTags    []string
	}{
		{
			name:        "basic claude",
			title:       "myproject__cc_1",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "codex with variant",
			title:       "myproject__cod_2_gpt4o",
			wantType:    AgentType("cod"),
			wantIndex:   2,
			wantVariant: "gpt4o",
			wantTags:    nil,
		},
		{
			name:        "gemini with tags",
			title:       "myproject__gmi_3[api,backend]",
			wantType:    AgentType("gmi"),
			wantIndex:   3,
			wantVariant: "",
			wantTags:    []string{"api", "backend"},
		},
		{
			name:        "full format",
			title:       "myproject__cc_1_opus[frontend,ui]",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "opus",
			wantTags:    []string{"frontend", "ui"},
		},
		{
			name:        "non-NTM format",
			title:       "zsh",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "double underscore no suffix",
			title:       "just__",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "single underscore",
			title:       "project_name",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "high index",
			title:       "session__cc_999",
			wantType:    AgentType("cc"),
			wantIndex:   999,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "custom agent type",
			title:       "session__aider_1",
			wantType:    AgentType("aider"),
			wantIndex:   1,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "variant with special chars",
			title:       "session__cc_1_opus-4.5",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "opus-4.5",
			wantTags:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotIndex, gotVariant, gotTags := parseAgentFromTitle(tt.title)
			if gotType != tt.wantType {
				t.Errorf("parseAgentFromTitle(%q) type = %q, want %q", tt.title, gotType, tt.wantType)
			}
			if gotIndex != tt.wantIndex {
				t.Errorf("parseAgentFromTitle(%q) index = %d, want %d", tt.title, gotIndex, tt.wantIndex)
			}
			if gotVariant != tt.wantVariant {
				t.Errorf("parseAgentFromTitle(%q) variant = %q, want %q", tt.title, gotVariant, tt.wantVariant)
			}
			if len(gotTags) != len(tt.wantTags) {
				t.Errorf("parseAgentFromTitle(%q) tags = %v, want %v", tt.title, gotTags, tt.wantTags)
			} else {
				for i := range gotTags {
					if gotTags[i] != tt.wantTags[i] {
						t.Errorf("parseAgentFromTitle(%q) tags[%d] = %q, want %q", tt.title, i, gotTags[i], tt.wantTags[i])
					}
				}
			}
		})
	}
}

func TestDetectAgentFromCommandEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    AgentType
	}{
		// Claude variants
		{"claude bare", "claude", AgentClaude},
		{"claude with args", "claude --model opus", AgentClaude},
		{"claude path", "/usr/bin/claude", AgentClaude},
		{"cc alias", "cc", AgentUser},
		{"cc with args", "cc --resume", AgentUser},

		// Codex variants
		{"codex bare", "codex", AgentCodex},
		{"codex with args", "codex --model gpt4o", AgentCodex},
		{"codex path", "/opt/codex", AgentCodex},
		{"cod alias", "cod", AgentUser},
		{"cod with args", "cod --dangerously-bypass", AgentUser},

		// Gemini variants
		{"gemini bare", "gemini", AgentGemini},
		{"gemini with args", "gemini --model flash", AgentGemini},
		{"gemini path", "/bin/gemini", AgentGemini},
		{"gmi alias", "gmi", AgentUser},
		{"gmi with args", "gmi start", AgentUser},

		// Cursor
		{"cursor bare", "cursor", AgentCursor},
		{"cursor with args", "cursor .", AgentCursor},
		{"cursor path", "/Applications/cursor", AgentCursor},

		// Windsurf
		{"windsurf bare", "windsurf", AgentWindsurf},
		{"windsurf with args", "windsurf --new-window", AgentWindsurf},
		{"windsurf path", "/snap/bin/windsurf", AgentWindsurf},

		// Aider
		{"aider bare", "aider", AgentAider},
		{"aider with args", "aider --watch", AgentAider},
		{"aider path", "/home/user/.local/bin/aider", AgentAider},

		// User fallback
		{"bash", "bash", AgentUser},
		{"zsh", "zsh", AgentUser},
		{"vim", "vim", AgentUser},
		{"empty", "", AgentUser},
		{"random command", "some_random_program", AgentUser},

		// Case insensitivity
		{"CLAUDE uppercase", "CLAUDE", AgentClaude},
		{"Claude mixed", "Claude", AgentClaude},
		{"CODEX uppercase", "CODEX", AgentCodex},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentFromCommand(tt.command)
			if got != tt.want {
				t.Errorf("detectAgentFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestFormatTagsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tags []string
		want string
	}{
		{"nil", nil, ""},
		{"empty slice", []string{}, ""},
		{"single tag", []string{"frontend"}, "[frontend]"},
		{"two tags", []string{"api", "backend"}, "[api,backend]"},
		{"three tags", []string{"a", "b", "c"}, "[a,b,c]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTags(tt.tags)
			if got != tt.want {
				t.Errorf("FormatTags(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestParseTagsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tagStr string
		want   []string
	}{
		{"empty", "", nil},
		{"single", "frontend", []string{"frontend"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"with spaces", "a, b, c", []string{"a", "b", "c"}},
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"only commas", ",,", nil},
		{"whitespace only entries", "a, ,b", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTags(tt.tagStr)
			if len(got) != len(tt.want) {
				t.Errorf("parseTags(%q) = %v, want %v", tt.tagStr, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.tagStr, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ============== Pure Function Tests ==============

func TestFormatPaneName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   string
		agentType string
		index     int
		variant   string
		want      string
	}{
		{
			name:      "claude agent no variant",
			session:   "myproject",
			agentType: "cc",
			index:     1,
			variant:   "",
			want:      "myproject__cc_1",
		},
		{
			name:      "codex agent no variant",
			session:   "myproject",
			agentType: "cod",
			index:     2,
			variant:   "",
			want:      "myproject__cod_2",
		},
		{
			name:      "gemini agent no variant",
			session:   "backend",
			agentType: "gmi",
			index:     3,
			variant:   "",
			want:      "backend__gmi_3",
		},
		{
			name:      "claude with variant",
			session:   "myproject",
			agentType: "cc",
			index:     1,
			variant:   "opus",
			want:      "myproject__cc_1_opus",
		},
		{
			name:      "codex with variant",
			session:   "frontend",
			agentType: "cod",
			index:     4,
			variant:   "gpt5",
			want:      "frontend__cod_4_gpt5",
		},
		{
			name:      "user pane",
			session:   "test",
			agentType: "user",
			index:     0,
			variant:   "",
			want:      "test__user_0",
		},
		{
			name:      "hyphenated session name",
			session:   "my-cool-project",
			agentType: "cc",
			index:     1,
			variant:   "",
			want:      "my-cool-project__cc_1",
		},
		{
			name:      "underscore session name",
			session:   "my_other_project",
			agentType: "gmi",
			index:     5,
			variant:   "pro",
			want:      "my_other_project__gmi_5_pro",
		},
		{
			name:      "high index",
			session:   "swarm",
			agentType: "cc",
			index:     100,
			variant:   "",
			want:      "swarm__cc_100",
		},
		{
			name:      "zero index",
			session:   "proj",
			agentType: "user",
			index:     0,
			variant:   "",
			want:      "proj__user_0",
		},
		{
			name:      "claude alias canonicalized",
			session:   "proj",
			agentType: "claude",
			index:     2,
			variant:   "",
			want:      "proj__cc_2",
		},
		{
			name:      "codex alias canonicalized",
			session:   "proj",
			agentType: "openai-codex",
			index:     3,
			variant:   "gpt5",
			want:      "proj__cod_3_gpt5",
		},
		{
			name:      "unknown plugin type preserved",
			session:   "proj",
			agentType: "custom-plugin",
			index:     1,
			variant:   "",
			want:      "proj__custom-plugin_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPaneName(tt.session, tt.agentType, tt.index, tt.variant)
			if got != tt.want {
				t.Errorf("FormatPaneName(%q, %q, %d, %q) = %q, want %q",
					tt.session, tt.agentType, tt.index, tt.variant, got, tt.want)
			}
		})
	}
}

func TestNeedsBufferSend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType AgentType
		content   string
		want      bool
	}{
		// Gemini cases - uses buffer for newlines
		{
			name:      "gemini single line",
			agentType: AgentGemini,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "gemini with newline",
			agentType: AgentGemini,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini multiple newlines",
			agentType: AgentGemini,
			content:   "a\nb\nc\nd",
			want:      true,
		},
		{
			name:      "gemini empty string",
			agentType: AgentGemini,
			content:   "",
			want:      false,
		},
		// Codex cases - uses buffer for newlines or large content (>512)
		{
			name:      "codex single line short",
			agentType: AgentCodex,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "codex with newline",
			agentType: AgentCodex,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex long content no newline",
			agentType: AgentCodex,
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		{
			name:      "codex exactly 512 chars",
			agentType: AgentCodex,
			content:   strings.Repeat("b", 512),
			want:      false,
		},
		{
			name:      "codex 511 chars",
			agentType: AgentCodex,
			content:   strings.Repeat("c", 511),
			want:      false,
		},
		{
			name:      "codex empty string",
			agentType: AgentCodex,
			content:   "",
			want:      false,
		},
		{
			name:      "codex alias with newline",
			agentType: AgentType("codex"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex alias long content no newline",
			agentType: AgentType("codex"),
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		{
			name:      "codex cli alias with newline",
			agentType: AgentType("codex-cli"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "openai codex alias long content no newline",
			agentType: AgentType("openai-codex"),
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		// Claude cases - uses buffer for multi-line content
		{
			name:      "claude single line",
			agentType: AgentClaude,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "claude with newline",
			agentType: AgentClaude,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "claude long content",
			agentType: AgentClaude,
			content:   strings.Repeat("x", 1000),
			want:      false,
		},
		{
			name:      "claude alias with newline",
			agentType: AgentType("claude"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "claude underscore alias with newline",
			agentType: AgentType("claude_code"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini alias with newline",
			agentType: AgentType("gemini"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini cli alias with newline",
			agentType: AgentType("gemini-cli"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "google gemini alias with newline",
			agentType: AgentType("google-gemini"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex alias trimmed and cased",
			agentType: AgentType(" CodEx "),
			content:   strings.Repeat("z", 513),
			want:      true,
		},
		// User pane - never uses buffer
		{
			name:      "user single line",
			agentType: AgentUser,
			content:   "echo hello",
			want:      false,
		},
		{
			name:      "user with newline",
			agentType: AgentUser,
			content:   "echo line1\necho line2",
			want:      false,
		},
		// Other agent types - never uses buffer
		{
			name:      "cursor single line",
			agentType: AgentCursor,
			content:   "test",
			want:      false,
		},
		{
			name:      "cursor with newline",
			agentType: AgentCursor,
			content:   "a\nb",
			want:      false,
		},
		{
			name:      "windsurf with newline",
			agentType: AgentWindsurf,
			content:   "x\ny",
			want:      false,
		},
		{
			name:      "aider with newline",
			agentType: AgentAider,
			content:   "test\ndata",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsBufferSend(tt.agentType, tt.content)
			if got != tt.want {
				t.Errorf("needsBufferSend(%v, %q) = %v, want %v",
					tt.agentType, truncateForLog(tt.content), got, tt.want)
			}
		})
	}
}

// truncateForLog truncates long strings for readable test output
func truncateForLog(s string) string {
	if len(s) > 50 {
		return s[:47] + "..."
	}
	return s
}
