package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

func TestNewFilesPanel(t *testing.T) {
	panel := NewFilesPanel()

	if panel == nil {
		t.Fatal("NewFilesPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "files" {
		t.Errorf("expected ID 'files', got %q", cfg.ID)
	}
	if cfg.Title != "File Changes" {
		t.Errorf("expected title 'File Changes', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", cfg.Priority)
	}
}

func TestFilesPanelSetSize(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(80, 24)

	if panel.Width() != 80 {
		t.Errorf("expected width 80, got %d", panel.Width())
	}
	if panel.Height() != 24 {
		t.Errorf("expected height 24, got %d", panel.Height())
	}
}

func TestFilesPanelFocusBlur(t *testing.T) {
	panel := NewFilesPanel()

	if panel.IsFocused() {
		t.Error("panel should not be focused initially")
	}

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("panel should be focused after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("panel should not be focused after Blur()")
	}
}

func TestFilesPanelSetData(t *testing.T) {
	panel := NewFilesPanel()
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now.Add(-5 * time.Minute),
			Session:   "test-session",
			Agents:    []string{"Agent1"},
			Change: tracker.FileChange{
				Path: "/path/to/file1.go",
				Type: tracker.FileAdded,
			},
		},
		{
			Timestamp: now.Add(-10 * time.Minute),
			Session:   "test-session",
			Agents:    []string{"Agent2"},
			Change: tracker.FileChange{
				Path: "/path/to/file2.go",
				Type: tracker.FileModified,
			},
		},
	}

	panel.SetData(changes, nil)

	if panel.HasError() {
		t.Error("panel should not have error when nil passed")
	}
}

func TestFilesPanelSetDataWithError(t *testing.T) {
	panel := NewFilesPanel()

	err := errors.New("test error: watch not running")
	panel.SetData(nil, err)

	if !panel.HasError() {
		t.Error("panel should have error")
	}
}

func TestFilesPanelKeybindings(t *testing.T) {
	panel := NewFilesPanel()
	bindings := panel.Keybindings()

	if len(bindings) == 0 {
		t.Error("expected keybindings, got none")
	}

	// Check for expected bindings
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	expected := []string{"cycle_window", "open", "down", "up"}
	for _, action := range expected {
		if !actions[action] {
			t.Errorf("expected keybinding action %q not found", action)
		}
	}
}

func TestFilesPanelViewEmptyWidth(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(0, 10)

	view := panel.View()
	if view != "" {
		t.Error("expected empty view for zero width")
	}
}

func TestFilesPanelViewNoChanges(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(60, 15)
	panel.SetData(nil, nil)

	view := panel.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	if !strings.Contains(view, "No file changes") {
		t.Error("expected 'No file changes' in view")
	}
}

func TestFilesPanelViewWithChanges(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(80, 20)
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now.Add(-1 * time.Minute),
			Session:   "test",
			Agents:    []string{"BlueAgent"},
			Change: tracker.FileChange{
				Path: "/src/main.go",
				Type: tracker.FileAdded,
			},
		},
		{
			Timestamp: now.Add(-5 * time.Minute),
			Session:   "test",
			Agents:    []string{"RedAgent"},
			Change: tracker.FileChange{
				Path: "/src/util.go",
				Type: tracker.FileModified,
			},
		},
		{
			Timestamp: now.Add(-10 * time.Minute),
			Session:   "test",
			Agents:    []string{"GreenAgent"},
			Change: tracker.FileChange{
				Path: "/src/old.go",
				Type: tracker.FileDeleted,
			},
		},
	}

	panel.SetData(changes, nil)
	view := panel.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain file names
	if !strings.Contains(view, "main.go") {
		t.Error("expected 'main.go' in view")
	}

	// Should contain agent attribution
	if !strings.Contains(view, "@BlueAgent") {
		t.Error("expected '@BlueAgent' in view")
	}

	// Should contain stats
	if !strings.Contains(view, "+1") {
		t.Error("expected '+1' in stats")
	}
}

func TestFilesPanelViewWithError(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(60, 15)

	err := errors.New("test error: watch not running")
	panel.SetData(nil, err)

	view := panel.View()
	if !strings.Contains(view, "Error") {
		t.Error("expected 'Error' badge in view")
	}
}

func TestFilesPanelUpdateNavigation(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(80, 20)
	panel.Focus()
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{Timestamp: now, Session: "s", Agents: []string{"A"}, Change: tracker.FileChange{Path: "/a", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Agents: []string{"B"}, Change: tracker.FileChange{Path: "/b", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Agents: []string{"C"}, Change: tracker.FileChange{Path: "/c", Type: tracker.FileAdded}},
	}
	panel.SetData(changes, nil)

	// Initial cursor should be 0
	if panel.cursor != 0 {
		t.Errorf("expected cursor 0, got %d", panel.cursor)
	}

	// Navigate down
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if panel.cursor != 1 {
		t.Errorf("expected cursor 1 after down, got %d", panel.cursor)
	}

	// Navigate down again
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if panel.cursor != 2 {
		t.Errorf("expected cursor 2 after second down, got %d", panel.cursor)
	}

	// Navigate up
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if panel.cursor != 1 {
		t.Errorf("expected cursor 1 after up, got %d", panel.cursor)
	}
}

func TestFilesPanelTimeWindowCycle(t *testing.T) {
	panel := NewFilesPanel()
	panel.Focus()

	// Default is 15m
	if panel.timeWindow != Window15m {
		t.Errorf("expected default Window15m, got %v", panel.timeWindow)
	}

	// Cycle to next
	panel.Update(tea.KeyMsg{Type: tea.KeyTab})
	if panel.timeWindow != Window5m {
		t.Errorf("expected Window5m after tab, got %v", panel.timeWindow)
	}

	// Cycle again
	panel.Update(tea.KeyMsg{Type: tea.KeyTab})
	if panel.timeWindow != WindowAll {
		t.Errorf("expected WindowAll after second tab, got %v", panel.timeWindow)
	}
}

func TestTimeWindowString(t *testing.T) {
	tests := []struct {
		window   TimeWindow
		expected string
	}{
		{WindowAll, "all"},
		{Window1h, "1h"},
		{Window15m, "15m"},
		{Window5m, "5m"},
	}

	for _, tt := range tests {
		if got := tt.window.String(); got != tt.expected {
			t.Errorf("TimeWindow(%d).String() = %q, want %q", tt.window, got, tt.expected)
		}
	}
}

func TestTimeWindowDuration(t *testing.T) {
	tests := []struct {
		window   TimeWindow
		expected time.Duration
	}{
		{WindowAll, 0},
		{Window1h, time.Hour},
		{Window15m, 15 * time.Minute},
		{Window5m, 5 * time.Minute},
	}

	for _, tt := range tests {
		if got := tt.window.Duration(); got != tt.expected {
			t.Errorf("TimeWindow(%d).Duration() = %v, want %v", tt.window, got, tt.expected)
		}
	}
}

func TestFilesPanelFilterByTimeWindow(t *testing.T) {
	panel := NewFilesPanel()
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{Timestamp: now.Add(-1 * time.Minute), Session: "s", Change: tracker.FileChange{Path: "/a", Type: tracker.FileAdded}},
		{Timestamp: now.Add(-10 * time.Minute), Session: "s", Change: tracker.FileChange{Path: "/b", Type: tracker.FileAdded}},
		{Timestamp: now.Add(-30 * time.Minute), Session: "s", Change: tracker.FileChange{Path: "/c", Type: tracker.FileAdded}},
		{Timestamp: now.Add(-2 * time.Hour), Session: "s", Change: tracker.FileChange{Path: "/d", Type: tracker.FileAdded}},
	}

	// Test 5m window
	panel.timeWindow = Window5m
	panel.SetData(changes, nil)
	if len(panel.changes) != 1 {
		t.Errorf("Window5m: expected 1 change, got %d", len(panel.changes))
	}

	// Test 15m window
	panel.timeWindow = Window15m
	panel.SetData(changes, nil)
	if len(panel.changes) != 2 {
		t.Errorf("Window15m: expected 2 changes, got %d", len(panel.changes))
	}

	// Test 1h window
	panel.timeWindow = Window1h
	panel.SetData(changes, nil)
	if len(panel.changes) != 3 {
		t.Errorf("Window1h: expected 3 changes, got %d", len(panel.changes))
	}

	// Test all window
	panel.timeWindow = WindowAll
	panel.SetData(changes, nil)
	if len(panel.changes) != 4 {
		t.Errorf("WindowAll: expected 4 changes, got %d", len(panel.changes))
	}
}

func TestFilesPanelBuildStats(t *testing.T) {
	panel := NewFilesPanel()
	panel.timeWindow = WindowAll
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/a", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/b", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/c", Type: tracker.FileModified}},
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/d", Type: tracker.FileDeleted}},
	}

	panel.SetData(changes, nil)
	stats := panel.buildStats()

	if stats != "+2 ~1 -1" {
		t.Errorf("expected '+2 ~1 -1', got %q", stats)
	}
}

func TestFilesPanelFormatTimeAgo(t *testing.T) {
	panel := NewFilesPanel()
	now := time.Now()

	tests := []struct {
		timestamp time.Time
		expected  string
	}{
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-90 * time.Minute), "1h"},
		{now.Add(-25 * time.Hour), "1d"},
	}

	for _, tt := range tests {
		got := panel.formatTimeAgo(tt.timestamp)
		if got != tt.expected {
			t.Errorf("formatTimeAgo(%v) = %q, want %q", tt.timestamp, got, tt.expected)
		}
	}
}

func TestFilesPanelCursorBounds(t *testing.T) {
	panel := NewFilesPanel()
	panel.timeWindow = WindowAll
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/a", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/b", Type: tracker.FileAdded}},
	}

	// Set cursor beyond bounds then set data
	panel.cursor = 10
	panel.SetData(changes, nil)

	if panel.cursor != 1 {
		t.Errorf("expected cursor to be clamped to 1, got %d", panel.cursor)
	}

	// Test with empty data
	panel.cursor = 5
	panel.SetData(nil, nil)

	if panel.cursor != 0 {
		t.Errorf("expected cursor to be 0 for empty data, got %d", panel.cursor)
	}
}

func TestFilesPanelMultipleAgents(t *testing.T) {
	panel := NewFilesPanel()
	panel.SetSize(80, 20)
	panel.timeWindow = WindowAll
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now,
			Session:   "s",
			Agents:    []string{"Agent1", "Agent2", "Agent3"},
			Change:    tracker.FileChange{Path: "/multi.go", Type: tracker.FileModified},
		},
	}

	panel.SetData(changes, nil)
	view := panel.View()

	// Should show first agent with count
	if !strings.Contains(view, "@Agent1+2") {
		t.Error("expected '@Agent1+2' for multiple agents")
	}
}

func TestFilesPanelNotFocusedIgnoresKeys(t *testing.T) {
	panel := NewFilesPanel()
	panel.timeWindow = WindowAll
	now := time.Now()

	changes := []tracker.RecordedFileChange{
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/a", Type: tracker.FileAdded}},
		{Timestamp: now, Session: "s", Change: tracker.FileChange{Path: "/b", Type: tracker.FileAdded}},
	}
	panel.SetData(changes, nil)

	// Don't focus the panel
	panel.Blur()

	// Try to navigate
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	// Cursor should not change
	if panel.cursor != 0 {
		t.Errorf("expected cursor 0 when not focused, got %d", panel.cursor)
	}
}
