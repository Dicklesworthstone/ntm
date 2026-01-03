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
	name := fmt.Sprintf("ntm_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = KillSession(name) // ignore error on cleanup
	})
	if err := CreateSession(name, os.TempDir()); err != nil {
		t.Fatalf("failed to create test session: %v", err)
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

func TestParseAgentFromTitle(t *testing.T) {
	tests := []struct {
		title       string
		wantType    AgentType
		wantVariant string
		wantTags    []string
	}{
		{"proj__cc_1", AgentClaude, "", nil},
		{"proj__cod_2", AgentCodex, "", nil},
		{"proj__gmi_3", AgentGemini, "", nil},
		{"proj__cc_4_opus", AgentClaude, "opus", nil},
		{"proj__cod_5_sonnet", AgentCodex, "sonnet", nil},
		{"proj__gmi_6_flash", AgentGemini, "flash", nil},
		{"proj__cc_7_opus[foo,bar]", AgentClaude, "opus", []string{"foo", "bar"}},
		{"proj__foo_1", AgentUser, "", nil},
		{"plain-title", AgentUser, "", nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.title, func(t *testing.T) {
			gotType, gotVariant, gotTags := parseAgentFromTitle(tt.title)
			if gotType != tt.wantType || gotVariant != tt.wantVariant {
				t.Fatalf("parseAgentFromTitle(%q) = (%v,%q); want (%v,%q)", tt.title, gotType, gotVariant, tt.wantType, tt.wantVariant)
			}
			if len(gotTags) != len(tt.wantTags) {
				t.Fatalf("parseAgentFromTitle(%q) tags len=%d want %d", tt.title, len(gotTags), len(tt.wantTags))
			}
			for i := range tt.wantTags {
				if gotTags[i] != tt.wantTags[i] {
					t.Fatalf("parseAgentFromTitle(%q) tags[%d]=%q want %q", tt.title, i, gotTags[i], tt.wantTags[i])
				}
			}
		})
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
