package panels

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func TestNewConflictsPanel(t *testing.T) {
	panel := NewConflictsPanel()

	if panel == nil {
		t.Fatal("NewConflictsPanel() returned nil")
	}
	if panel.Config().ID != "conflicts" {
		t.Errorf("Config().ID = %v, want 'conflicts'", panel.Config().ID)
	}
	if panel.HasConflicts() {
		t.Error("New panel should not have conflicts")
	}
}

func TestConflictsPanel_AddConflict(t *testing.T) {
	panel := NewConflictsPanel()

	conflict := watcher.FileConflict{
		Path:           "/test/file.go",
		RequestorAgent: "TestAgent",
		Holders:        []string{"HolderAgent"},
		DetectedAt:     time.Now(),
	}

	panel.AddConflict(conflict)

	if !panel.HasConflicts() {
		t.Error("Panel should have conflicts after adding one")
	}
	if panel.ConflictCount() != 1 {
		t.Errorf("ConflictCount() = %v, want 1", panel.ConflictCount())
	}

	// Adding the same conflict should update, not duplicate
	conflict.Holders = []string{"NewHolder"}
	panel.AddConflict(conflict)

	if panel.ConflictCount() != 1 {
		t.Errorf("ConflictCount() after duplicate = %v, want 1", panel.ConflictCount())
	}

	// Verify it was updated
	conflicts := panel.GetConflicts()
	if len(conflicts[0].Holders) != 1 || conflicts[0].Holders[0] != "NewHolder" {
		t.Errorf("Conflict was not updated: %v", conflicts[0].Holders)
	}
}

func TestConflictsPanel_AddMultipleConflicts(t *testing.T) {
	panel := NewConflictsPanel()

	conflict1 := watcher.FileConflict{
		Path:           "/test/file1.go",
		RequestorAgent: "Agent1",
		Holders:        []string{"HolderA"},
		DetectedAt:     time.Now(),
	}
	conflict2 := watcher.FileConflict{
		Path:           "/test/file2.go",
		RequestorAgent: "Agent2",
		Holders:        []string{"HolderB"},
		DetectedAt:     time.Now(),
	}

	panel.AddConflict(conflict1)
	panel.AddConflict(conflict2)

	if panel.ConflictCount() != 2 {
		t.Errorf("ConflictCount() = %v, want 2", panel.ConflictCount())
	}
}

func TestConflictsPanel_RemoveConflict(t *testing.T) {
	panel := NewConflictsPanel()

	conflict := watcher.FileConflict{
		Path:           "/test/file.go",
		RequestorAgent: "TestAgent",
		Holders:        []string{"HolderAgent"},
		DetectedAt:     time.Now(),
	}

	panel.AddConflict(conflict)
	panel.RemoveConflict(conflict.Path, conflict.RequestorAgent)

	if panel.HasConflicts() {
		t.Error("Panel should not have conflicts after removing the only one")
	}
}

func TestConflictsPanel_SelectedConflict(t *testing.T) {
	panel := NewConflictsPanel()

	// No conflicts
	if panel.SelectedConflict() != nil {
		t.Error("SelectedConflict() should be nil when no conflicts")
	}

	conflict := watcher.FileConflict{
		Path:           "/test/file.go",
		RequestorAgent: "TestAgent",
		Holders:        []string{"HolderAgent"},
		DetectedAt:     time.Now(),
	}

	panel.AddConflict(conflict)

	selected := panel.SelectedConflict()
	if selected == nil {
		t.Fatal("SelectedConflict() should not be nil after adding a conflict")
	}
	if selected.Path != conflict.Path {
		t.Errorf("SelectedConflict().Path = %v, want %v", selected.Path, conflict.Path)
	}
}

func TestConflictsPanel_SetConflicts(t *testing.T) {
	panel := NewConflictsPanel()

	conflicts := []watcher.FileConflict{
		{
			Path:           "/test/file1.go",
			RequestorAgent: "Agent1",
			Holders:        []string{"HolderA"},
		},
		{
			Path:           "/test/file2.go",
			RequestorAgent: "Agent2",
			Holders:        []string{"HolderB"},
		},
	}

	panel.SetConflicts(conflicts)

	if panel.ConflictCount() != 2 {
		t.Errorf("ConflictCount() = %v, want 2", panel.ConflictCount())
	}
}

func TestConflictsPanel_Update_Navigation(t *testing.T) {
	panel := NewConflictsPanel()
	panel.Focus()
	panel.SetSize(80, 24)

	// Add two conflicts
	panel.AddConflict(watcher.FileConflict{Path: "/file1.go", RequestorAgent: "A1", Holders: []string{"H"}})
	panel.AddConflict(watcher.FileConflict{Path: "/file2.go", RequestorAgent: "A2", Holders: []string{"H"}})

	// Navigate down
	newModel, _ := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updatedPanel := newModel.(*ConflictsPanel)
	if updatedPanel.selectedIndex != 1 {
		t.Errorf("After down key, selectedIndex = %v, want 1", updatedPanel.selectedIndex)
	}

	// Navigate up
	newModel, _ = updatedPanel.Update(tea.KeyMsg{Type: tea.KeyUp})
	updatedPanel = newModel.(*ConflictsPanel)
	if updatedPanel.selectedIndex != 0 {
		t.Errorf("After up key, selectedIndex = %v, want 0", updatedPanel.selectedIndex)
	}

	// Navigate action selection right
	newModel, _ = updatedPanel.Update(tea.KeyMsg{Type: tea.KeyRight})
	updatedPanel = newModel.(*ConflictsPanel)
	if updatedPanel.selectedAction != 1 {
		t.Errorf("After right key, selectedAction = %v, want 1", updatedPanel.selectedAction)
	}

	// Navigate action selection left
	newModel, _ = updatedPanel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updatedPanel = newModel.(*ConflictsPanel)
	if updatedPanel.selectedAction != 0 {
		t.Errorf("After left key, selectedAction = %v, want 0", updatedPanel.selectedAction)
	}
}

func TestConflictsPanel_Update_NotFocused(t *testing.T) {
	panel := NewConflictsPanel()
	// Not focused
	panel.AddConflict(watcher.FileConflict{Path: "/file.go", RequestorAgent: "A", Holders: []string{"H"}})

	newModel, _ := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updatedPanel := newModel.(*ConflictsPanel)

	// Should not change when not focused
	if updatedPanel.selectedIndex != 0 {
		t.Errorf("When not focused, selectedIndex should not change, got %v", updatedPanel.selectedIndex)
	}
}

func TestConflictsPanel_View_Empty(t *testing.T) {
	panel := NewConflictsPanel()
	panel.SetSize(80, 24)

	view := panel.View()

	if !strings.Contains(view, "No file conflicts") {
		t.Error("Empty panel view should contain 'No file conflicts'")
	}
}

func TestConflictsPanel_View_WithConflict(t *testing.T) {
	panel := NewConflictsPanel()
	panel.SetSize(80, 24)

	// Use a fixed time for testing
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	panel.now = func() time.Time { return now }

	reserved := now.Add(-5 * time.Minute)
	expires := now.Add(25 * time.Minute)

	conflict := watcher.FileConflict{
		Path:           "/test/file.go",
		RequestorAgent: "TestAgent",
		Holders:        []string{"HolderAgent"},
		ReservedSince:  &reserved,
		ExpiresAt:      &expires,
		DetectedAt:     now,
	}

	panel.AddConflict(conflict)
	view := panel.View()

	// Check that key elements are present
	if !strings.Contains(view, "1 Conflict") {
		t.Error("View should contain conflict count")
	}
	if !strings.Contains(view, "/test/file.go") {
		t.Error("View should contain file path")
	}
	if !strings.Contains(view, "TestAgent") {
		t.Error("View should contain requestor agent")
	}
	if !strings.Contains(view, "HolderAgent") {
		t.Error("View should contain holder agent")
	}
	if !strings.Contains(view, "[1] Wait") {
		t.Error("View should contain Wait action")
	}
	if !strings.Contains(view, "[2] Request") {
		t.Error("View should contain Request action")
	}
	if !strings.Contains(view, "[3] Force") {
		t.Error("View should contain Force action")
	}
}

func TestConflictsPanel_Keybindings(t *testing.T) {
	panel := NewConflictsPanel()

	bindings := panel.Keybindings()

	if len(bindings) != 4 {
		t.Errorf("Keybindings() returned %d bindings, want 4", len(bindings))
	}

	// Check expected actions
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	expectedActions := []string{"wait", "request", "force", "dismiss"}
	for _, action := range expectedActions {
		if !actions[action] {
			t.Errorf("Missing keybinding for action: %s", action)
		}
	}
}

func TestConflictsPanel_ActionHandler(t *testing.T) {
	panel := NewConflictsPanel()
	panel.Focus()
	panel.SetSize(80, 24)

	var handlerCalled bool
	var receivedAction watcher.ConflictAction

	panel.SetActionHandler(func(conflict watcher.FileConflict, action watcher.ConflictAction) error {
		handlerCalled = true
		receivedAction = action
		return nil
	})

	conflict := watcher.FileConflict{Path: "/file.go", RequestorAgent: "A", Holders: []string{"H"}}
	panel.AddConflict(conflict)

	// Press "1" for Wait action
	_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})

	if cmd == nil {
		t.Fatal("Expected a command from pressing action key")
	}

	// Execute the command to trigger the handler
	msg := cmd()
	if _, ok := msg.(ConflictActionResultMsg); !ok {
		t.Errorf("Expected ConflictActionResultMsg, got %T", msg)
	}

	if !handlerCalled {
		t.Error("Action handler was not called")
	}
	if receivedAction != watcher.ConflictActionWait {
		t.Errorf("Received action = %v, want Wait", receivedAction)
	}
}

func TestFormatConflictDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{1 * time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatConflictDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("formatConflictDuration(%v) = %v, want %v", tt.duration, result, tt.expected)
			}
		})
	}
}
