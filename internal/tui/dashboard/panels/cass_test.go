package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
)

func TestNewCASSPanel(t *testing.T) {
	panel := NewCASSPanel()
	if panel == nil {
		t.Fatal("NewCASSPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "cass" {
		t.Errorf("expected ID 'cass', got %q", cfg.ID)
	}
	if cfg.Title != "CASS Context" {
		t.Errorf("expected title 'CASS Context', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", cfg.Priority)
	}
}

func TestCASSPanelSetSize(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(80, 24)
	if panel.Width() != 80 {
		t.Errorf("expected width 80, got %d", panel.Width())
	}
	if panel.Height() != 24 {
		t.Errorf("expected height 24, got %d", panel.Height())
	}
}

func TestCASSPanelFocusBlur(t *testing.T) {
	panel := NewCASSPanel()
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

func TestCASSPanelSetData(t *testing.T) {
	panel := NewCASSPanel()
	now := time.Now()

	hits := []cass.SearchHit{
		{Title: "Low score", Score: 0.10, CreatedAt: ptrFlexTime(now.Add(-2 * time.Hour))},
		{Title: "High score", Score: 0.90, CreatedAt: ptrFlexTime(now.Add(-10 * time.Minute))},
	}

	panel.SetData(hits, nil)
	if panel.HasError() {
		t.Error("panel should not have error when nil passed")
	}
	if len(panel.hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(panel.hits))
	}
	if panel.hits[0].Title != "High score" {
		t.Errorf("expected hits to be sorted by score desc, got %q first", panel.hits[0].Title)
	}
}

func TestCASSPanelSetDataWithError(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetData(nil, errors.New("cass not installed"))
	if !panel.HasError() {
		t.Error("panel should have error")
	}
}

func TestCASSPanelKeybindings(t *testing.T) {
	panel := NewCASSPanel()
	bindings := panel.Keybindings()
	if len(bindings) == 0 {
		t.Fatal("expected keybindings, got none")
	}

	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	for _, action := range []string{"search", "down", "up"} {
		if !actions[action] {
			t.Errorf("expected keybinding action %q not found", action)
		}
	}
}

func TestCASSPanelViewEmptyWidth(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(0, 10)
	if view := panel.View(); view != "" {
		t.Error("expected empty view for zero width")
	}
}

func TestCASSPanelViewNoHits(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(60, 15)
	panel.SetData(nil, nil)

	view := panel.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
	if !strings.Contains(view, "No context found") {
		t.Error("expected 'No context found' in view")
	}
}

func TestCASSPanelViewShowsErrorState(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(80, 15)
	panel.SetData(nil, errors.New("cass not installed"))

	view := panel.View()
	if !strings.Contains(view, "Error") {
		t.Error("expected view to include error badge")
	}
	if !strings.Contains(view, "cass not installed") {
		t.Error("expected view to include error message")
	}
	if !strings.Contains(view, "Press r") {
		t.Error("expected view to include refresh hint")
	}
}

func TestCASSPanelViewWithHits(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(80, 15)

	now := time.Now()
	hits := []cass.SearchHit{
		{Title: "Session: auth refactor", Score: 0.90, CreatedAt: ptrFlexTime(now.Add(-2 * time.Hour))},
		{Title: "Session: ui polish", Score: 0.50, CreatedAt: ptrFlexTime(now.Add(-25 * time.Hour))},
	}
	panel.SetData(hits, nil)

	view := panel.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
	if !strings.Contains(view, "auth refactor") {
		t.Error("expected hit title in view")
	}
	if !strings.Contains(view, "0.90") {
		t.Error("expected score formatted in view")
	}
	if !strings.Contains(view, "2h") {
		t.Error("expected age formatted in view")
	}
}

func ptrFlexTime(t time.Time) *cass.FlexTime { return &cass.FlexTime{Time: t} }
