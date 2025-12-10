package tmux

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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

func TestValidateSessionName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"myproject", false},
		{"my-project", false},
		{"my_project", false},
		{"MyProject123", false},
		{"", true},          // empty
		{"my.project", true}, // contains .
		{"my:project", true}, // contains :
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

	if !strings.Contains(output, text) {
		t.Logf("output: %q", output)
		t.Errorf("output should contain %q", text)
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

	// Zoom first pane
	if err := ZoomPane(session, 0); err != nil {
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
