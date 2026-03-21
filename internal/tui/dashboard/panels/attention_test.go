package panels

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestNewAttentionPanel(t *testing.T) {
	panel := NewAttentionPanel()
	if panel == nil {
		t.Fatal("NewAttentionPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "attention" {
		t.Fatalf("Config().ID = %q, want %q", cfg.ID, "attention")
	}
	if cfg.Title != "Attention" {
		t.Fatalf("Config().Title = %q, want %q", cfg.Title, "Attention")
	}
}

func TestAttentionPanelViewFeedInactive(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, false)

	view := panel.View()
	if !strings.Contains(view, "Feed not active") {
		t.Fatalf("expected inactive attention panel title, got %q", view)
	}
	if !strings.Contains(view, "Attention Feed not active") {
		t.Fatalf("expected inactive attention panel description, got %q", view)
	}
}

func TestAttentionPanelViewAllClear(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, true)

	view := panel.View()
	if !strings.Contains(view, "All clear") {
		t.Fatalf("expected all-clear title, got %q", view)
	}
	if !strings.Contains(view, "No attention items") {
		t.Fatalf("expected empty-state description, got %q", view)
	}
}

func TestAttentionPanelSelectedItemAndCursorClamp(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
	}, true)
	panel.cursor = 5
	panel.SetData(panel.items, true)

	item := panel.SelectedItem()
	if item == nil || item.Summary != "first" {
		t.Fatalf("SelectedItem() = %+v, want first item", item)
	}
}

func TestAttentionPanelSetDataSortsActionabilityAndRecency(t *testing.T) {
	panel := NewAttentionPanel()
	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	panel.SetData([]AttentionItem{
		{Summary: "interesting-old", Actionability: robot.ActionabilityInteresting, Timestamp: base},
		{Summary: "action-new", Actionability: robot.ActionabilityActionRequired, Timestamp: base.Add(2 * time.Minute)},
		{Summary: "interesting-new", Actionability: robot.ActionabilityInteresting, Timestamp: base.Add(3 * time.Minute)},
	}, true)

	if got := panel.items[0].Summary; got != "action-new" {
		t.Fatalf("expected action_required item first, got %q", got)
	}
	if got := panel.items[1].Summary; got != "interesting-new" {
		t.Fatalf("expected newer interesting item second, got %q", got)
	}
}

func TestAttentionPanelNavigationMovesSelection(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now()},
		{Summary: "second", Actionability: robot.ActionabilityInteresting, Timestamp: time.Now().Add(-2 * time.Minute)},
	}, true)
	panel.Focus()

	updated, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatalf("expected nil command from navigation update, got %v", cmd)
	}

	got := updated.(*AttentionPanel).SelectedItem()
	if got == nil || got.Summary != "second" {
		t.Fatalf("selected item after down key = %+v, want second", got)
	}
}

func TestAttentionPanelKeybindingsIncludeZoomToSource(t *testing.T) {
	panel := NewAttentionPanel()
	bindings := panel.Keybindings()

	found := false
	for _, binding := range bindings {
		if binding.Action == "zoom_to_source" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected zoom_to_source keybinding")
	}
}

func TestAttentionPanelSelectCursor(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "older", Cursor: 10, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "newer", Cursor: 25, Timestamp: time.Now()},
	}, true)

	if !panel.SelectCursor(25) {
		t.Fatal("expected SelectCursor to find cursor 25")
	}
	if got := panel.SelectedItem(); got == nil || got.Cursor != 25 {
		t.Fatalf("selected item = %+v, want cursor 25", got)
	}
}

func TestAttentionPanelSelectNearestCursor(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "older", Cursor: 40, Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "newer", Cursor: 44, Timestamp: time.Now()},
	}, true)

	if !panel.SelectNearestCursor(42) {
		t.Fatal("expected SelectNearestCursor to pick a fallback item")
	}
	if got := panel.SelectedItem(); got == nil || got.Cursor != 44 {
		t.Fatalf("selected item = %+v, want nearest newer cursor 44", got)
	}
}
