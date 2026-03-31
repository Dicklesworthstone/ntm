package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/context"
)

func TestNewRotationConfirmPanel(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()
	if panel == nil {
		t.Fatal("NewRotationConfirmPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "rotation_confirm" {
		t.Errorf("expected ID 'rotation_confirm', got %q", cfg.ID)
	}
	if cfg.Priority != PriorityCritical {
		t.Errorf("expected PriorityCritical, got %v", cfg.Priority)
	}
}

func TestRotationConfirmPanel_SetData(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()

	pending := []*context.PendingRotation{
		{
			AgentID:        "myproject__cc_1",
			SessionName:    "myproject",
			ContextPercent: 92.5,
			TimeoutAt:      time.Now().Add(30 * time.Second),
			DefaultAction:  context.ConfirmRotate,
		},
		{
			AgentID:        "myproject__cc_2",
			SessionName:    "myproject",
			ContextPercent: 88.0,
			TimeoutAt:      time.Now().Add(60 * time.Second),
			DefaultAction:  context.ConfirmCompact,
		},
	}

	panel.SetData(pending, nil)

	if !panel.HasPending() {
		t.Error("expected HasPending to be true")
	}
	if panel.HasError() {
		t.Error("expected HasError to be false")
	}
}

func TestRotationConfirmPanel_EmptyState(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()
	panel.SetSize(80, 20)
	panel.SetData(nil, nil)

	if panel.HasPending() {
		t.Error("expected HasPending to be false")
	}

	view := panel.View()
	if !strings.Contains(view, "No pending rotations") {
		t.Error("expected empty state message in view")
	}
}

func TestRotationConfirmPanel_Selection(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()

	pending := []*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: time.Now().Add(time.Minute)},
		{AgentID: "agent2", TimeoutAt: time.Now().Add(time.Minute)},
		{AgentID: "agent3", TimeoutAt: time.Now().Add(time.Minute)},
	}
	panel.SetData(pending, nil)

	// Initial selection is 0
	selected := panel.SelectedPending()
	if selected == nil || selected.AgentID != "agent1" {
		t.Error("expected first agent selected initially")
	}

	// Selection should be bounded
	panel.data.Selected = 10
	panel.SetData(pending, nil) // Should adjust selection
	if panel.data.Selected != 2 {
		t.Errorf("expected selection clamped to 2, got %d", panel.data.Selected)
	}
}

func TestRotationConfirmPanel_ErrorClearsPendingSelection(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()
	pending := []*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: time.Now().Add(time.Minute)},
	}
	panel.SetData(pending, nil)

	panel.SetData(pending, errors.New("refresh failed"))

	if !panel.HasError() {
		t.Fatal("expected panel error to be set")
	}
	if panel.HasPending() {
		t.Fatal("expected pending rotations to be cleared on error")
	}
	if pending := panel.SelectedPending(); pending != nil {
		t.Fatalf("expected no selected pending rotation on error, got %v", pending.AgentID)
	}
}

func TestRotationConfirmPanelSetDataPreservesSelectedAgentAcrossReorder(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panel := NewRotationConfirmPanel()
	panel.SetData([]*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: now.Add(3 * time.Minute)},
		{AgentID: "agent2", TimeoutAt: now.Add(2 * time.Minute)},
		{AgentID: "agent3", TimeoutAt: now.Add(1 * time.Minute)},
	}, nil)
	panel.data.Selected = 1

	panel.SetData([]*context.PendingRotation{
		{AgentID: "agent3", TimeoutAt: now.Add(1 * time.Minute)},
		{AgentID: "agent1", TimeoutAt: now.Add(3 * time.Minute)},
		{AgentID: "agent2", TimeoutAt: now.Add(2 * time.Minute)},
	}, nil)

	selected := panel.SelectedPending()
	if selected == nil {
		t.Fatal("expected selected pending rotation after reorder")
	}
	if selected.AgentID != "agent2" {
		t.Fatalf("selected agent = %q, want agent2", selected.AgentID)
	}
	if panel.data.Selected != 2 {
		t.Fatalf("selected index = %d, want 2", panel.data.Selected)
	}
}

func TestRotationConfirmPanelSetDataPreservesSelectedAgentWhenEarlierItemRemoved(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panel := NewRotationConfirmPanel()
	panel.SetData([]*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: now.Add(3 * time.Minute)},
		{AgentID: "agent2", TimeoutAt: now.Add(2 * time.Minute)},
		{AgentID: "agent3", TimeoutAt: now.Add(1 * time.Minute)},
	}, nil)
	panel.data.Selected = 1

	panel.SetData([]*context.PendingRotation{
		{AgentID: "agent2", TimeoutAt: now.Add(2 * time.Minute)},
		{AgentID: "agent3", TimeoutAt: now.Add(1 * time.Minute)},
	}, nil)

	selected := panel.SelectedPending()
	if selected == nil {
		t.Fatal("expected selected pending rotation after removal")
	}
	if selected.AgentID != "agent2" {
		t.Fatalf("selected agent = %q, want agent2", selected.AgentID)
	}
	if panel.data.Selected != 0 {
		t.Fatalf("selected index = %d, want 0", panel.data.Selected)
	}
}

func TestRotationConfirmPanel_UrgentTimeout(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panel := NewRotationConfirmPanel()
	panel.now = func() time.Time { return now }

	// No urgent items
	pending := []*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: now.Add(60 * time.Second)},
	}
	panel.SetData(pending, nil)
	if panel.hasUrgentTimeout() {
		t.Error("expected no urgent timeout with 60s remaining")
	}

	// Add urgent item
	pending = append(pending, &context.PendingRotation{
		AgentID:   "agent2",
		TimeoutAt: now.Add(15 * time.Second),
	})
	panel.SetData(pending, nil)
	if !panel.hasUrgentTimeout() {
		t.Error("expected urgent timeout with 15s remaining")
	}
}

func TestRotationConfirmPanel_Keybindings(t *testing.T) {
	t.Parallel()

	panel := NewRotationConfirmPanel()
	bindings := panel.Keybindings()

	if len(bindings) < 4 {
		t.Errorf("expected at least 4 keybindings, got %d", len(bindings))
	}

	// Check for expected actions
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	expectedActions := []string{"confirm_rotate", "confirm_compact", "confirm_ignore", "confirm_postpone"}
	for _, action := range expectedActions {
		if !actions[action] {
			t.Errorf("expected keybinding for action %q", action)
		}
	}
}

func TestRotationConfirmPanel_ViewRendering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panel := NewRotationConfirmPanel()
	panel.now = func() time.Time { return now }
	panel.SetSize(80, 20)

	pending := []*context.PendingRotation{
		{
			AgentID:        "myproject__cc_1",
			SessionName:    "myproject",
			ContextPercent: 92.5,
			TimeoutAt:      now.Add(45 * time.Second),
			DefaultAction:  context.ConfirmRotate,
		},
	}
	panel.SetData(pending, nil)

	view := panel.View()

	// Check that agent ID is rendered
	if !strings.Contains(view, "myproject__cc_1") {
		t.Error("expected agent ID in view")
	}

	// Check that context percent is shown
	if !strings.Contains(view, "92%") && !strings.Contains(view, "93%") {
		t.Error("expected context percentage in view")
	}

	// Check that default action is shown
	if !strings.Contains(view, "rotate") {
		t.Error("expected default action in view")
	}
}

func TestRotationConfirmPanel_Focus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panel := NewRotationConfirmPanel()
	panel.now = func() time.Time { return now }
	panel.SetSize(80, 30) // Increase height to ensure hints fit

	pending := []*context.PendingRotation{
		{AgentID: "agent1", TimeoutAt: now.Add(time.Minute)},
	}
	panel.SetData(pending, nil)

	// Unfocused - no keyboard hints
	panel.Blur()
	_ = panel.View() // Render unfocused first

	panel.Focus()

	// Focused - should show keyboard hints
	viewFocused := panel.View()

	if !strings.Contains(viewFocused, "[r]otate") {
		t.Errorf("expected keyboard hints when focused, got: %s", viewFocused)
	}
}

func TestFormatTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		seconds  int
		expected string
	}{
		{0, "expired"},
		{-5, "expired"},
		{30, "30s"},
		{59, "59s"},
		{60, "1m"},
		{90, "1m30s"},
		{120, "2m"},
		{125, "2m5s"},
	}

	for _, tc := range tests {
		result := formatTimeout(tc.seconds)
		if !strings.Contains(result, tc.expected) {
			t.Errorf("formatTimeout(%d) = %q, expected to contain %q", tc.seconds, result, tc.expected)
		}
	}
}

func TestTruncateAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID  string
		maxWidth int
		expected string
	}{
		{"short", 10, "short"},
		{"myproject__cc_1", 15, "myproject__cc_1"},
		{"myproject__cc_1", 10, "myproje..."},
		{"agent", 3, "..."},
		{"agent", 2, "..."},
	}

	for _, tc := range tests {
		result := truncateAgent(tc.agentID, tc.maxWidth)
		if result != tc.expected {
			t.Errorf("truncateAgent(%q, %d) = %q, expected %q", tc.agentID, tc.maxWidth, result, tc.expected)
		}
	}
}
