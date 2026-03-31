package panels

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

func TestCASSPanelHandlesOwnHeight(t *testing.T) {
	panel := NewCASSPanel()
	if !panel.HandlesOwnHeight() {
		t.Error("expected CASS panel to manage its own height")
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

func TestCASSPanelSetDataWithErrorClearsStaleHits(t *testing.T) {
	panel := NewCASSPanel()
	now := time.Now()

	panel.SetData([]cass.SearchHit{
		{Title: "Session: stale hit", Score: 0.90, CreatedAt: ptrFlexTime(now)},
	}, nil)
	if len(panel.hits) != 1 {
		t.Fatalf("expected initial hit, got %d", len(panel.hits))
	}

	panel.SetData([]cass.SearchHit{
		{Title: "Session: stale hit", Score: 0.90, CreatedAt: ptrFlexTime(now)},
	}, errors.New("cass unavailable"))

	if !panel.HasError() {
		t.Fatal("expected panel error")
	}
	if len(panel.hits) != 0 {
		t.Fatalf("expected hits to clear on error, got %d", len(panel.hits))
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
		if b.Action == "search" {
			if keys := b.Key.Keys(); len(keys) != 1 || keys[0] != "ctrl+s" {
				t.Fatalf("search binding keys = %v, want [ctrl+s]", keys)
			}
		}
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

func TestCASSPanelViewportNavigationKeepsSelectionVisible(t *testing.T) {
	panel := NewCASSPanel()
	panel.SetSize(52, 10)
	panel.Focus()
	now := time.Now()

	var hits []cass.SearchHit
	for i := 0; i < 12; i++ {
		hits = append(hits, cass.SearchHit{
			Title:     fmt.Sprintf("Session: result %02d", i),
			Score:     float64(100-i) / 100,
			CreatedAt: ptrFlexTime(now.Add(time.Duration(-i) * time.Hour)),
		})
	}
	panel.SetData(hits, nil)
	panel.View()

	for i := 0; i < 8; i++ {
		panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	view := panel.View()
	if panel.cursor != 8 {
		t.Fatalf("expected cursor 8, got %d", panel.cursor)
	}
	if panel.offset == 0 {
		t.Fatal("expected viewport-backed navigation to advance offset")
	}
	if !panel.scroll.NeedsScroll() {
		t.Fatal("expected scrollable viewport for overflowing CASS hits")
	}
	if !strings.Contains(view, "result 08") {
		t.Fatalf("expected selected hit to remain visible, view=%q", view)
	}
}

func TestCASSPanelSetDataPreservesSelectedHitAcrossResort(t *testing.T) {
	panel := NewCASSPanel()
	now := time.Now()
	lineOne := 12
	lineTwo := 27

	panel.SetData([]cass.SearchHit{
		{
			SessionID:  "sess-a",
			SourcePath: "/tmp/a.jsonl",
			LineNumber: &lineOne,
			Title:      "Alpha",
			Score:      0.90,
			MatchType:  "title",
			Agent:      "codex",
			CreatedAt:  ptrFlexTime(now.Add(-2 * time.Hour)),
		},
		{
			SessionID:  "sess-b",
			SourcePath: "/tmp/b.jsonl",
			LineNumber: &lineTwo,
			Title:      "Bravo",
			Score:      0.40,
			MatchType:  "title",
			Agent:      "codex",
			CreatedAt:  ptrFlexTime(now.Add(-1 * time.Hour)),
		},
	}, nil)
	panel.cursor = 1

	panel.SetData([]cass.SearchHit{
		{
			SessionID:  "sess-a",
			SourcePath: "/tmp/a.jsonl",
			LineNumber: &lineOne,
			Title:      "Alpha",
			Score:      0.20,
			MatchType:  "title",
			Agent:      "codex",
			CreatedAt:  ptrFlexTime(now.Add(-2 * time.Hour)),
		},
		{
			SessionID:  "sess-b",
			SourcePath: "/tmp/b.jsonl",
			LineNumber: &lineTwo,
			Title:      "Bravo",
			Score:      0.95,
			MatchType:  "title",
			Agent:      "codex",
			CreatedAt:  ptrFlexTime(now.Add(-1 * time.Hour)),
		},
	}, nil)

	selected, ok := panel.selectedHit()
	if !ok {
		t.Fatal("expected selected hit after refresh")
	}
	if selected.SessionID != "sess-b" {
		t.Fatalf("expected selection to stay on sess-b across resort, got %q", selected.SessionID)
	}
	if panel.cursor != 0 {
		t.Fatalf("expected cursor to move with the selected hit after resort, got %d", panel.cursor)
	}
}

func ptrFlexTime(t time.Time) *cass.FlexTime { return &cass.FlexTime{Time: t} }
