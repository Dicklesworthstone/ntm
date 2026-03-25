package panels

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

func TestNewBeadsPanel(t *testing.T) {
	panel := NewBeadsPanel()
	if panel == nil {
		t.Fatal("NewBeadsPanel returned nil")
	}
}

func TestBeadsPanelConfig(t *testing.T) {
	panel := NewBeadsPanel()
	cfg := panel.Config()

	if cfg.ID != "beads" {
		t.Errorf("expected ID 'beads', got %q", cfg.ID)
	}
	if cfg.Title != "Beads Pipeline" {
		t.Errorf("expected Title 'Beads Pipeline', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityHigh {
		t.Errorf("expected PriorityHigh, got %v", cfg.Priority)
	}
	if cfg.RefreshInterval != 15*time.Second {
		t.Errorf("expected 15s refresh, got %v", cfg.RefreshInterval)
	}
	if !cfg.Collapsible {
		t.Error("expected Collapsible to be true")
	}
}

func TestBeadsPanelSetSize(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(90, 25)

	if panel.Width() != 90 {
		t.Errorf("expected width 90, got %d", panel.Width())
	}
	if panel.Height() != 25 {
		t.Errorf("expected height 25, got %d", panel.Height())
	}
}

func TestBeadsPanelFocusBlur(t *testing.T) {
	panel := NewBeadsPanel()

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("expected IsFocused to be true after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("expected IsFocused to be false after Blur()")
	}
}

func TestBeadsPanelHandlesOwnHeight(t *testing.T) {
	panel := NewBeadsPanel()
	if !panel.HandlesOwnHeight() {
		t.Fatal("expected beads panel to manage its own height")
	}
}

func TestBeadsPanelSetData(t *testing.T) {
	panel := NewBeadsPanel()

	summary := bv.BeadsSummary{
		Available:  true,
		Total:      20,
		Open:       10,
		InProgress: 5,
		Blocked:    2,
		Ready:      8,
		Closed:     3,
	}

	ready := []bv.BeadPreview{
		{ID: "bead-1", Title: "First task", Priority: "P0"},
		{ID: "bead-2", Title: "Second task", Priority: "P1"},
	}

	panel.SetData(summary, ready, nil)

	if !panel.summary.Available {
		t.Error("expected summary.Available to be true")
	}
	if panel.summary.Total != 20 {
		t.Errorf("expected Total 20, got %d", panel.summary.Total)
	}
	if len(panel.ready) != 2 {
		t.Errorf("expected 2 ready beads, got %d", len(panel.ready))
	}
}

func TestBeadsPanelKeybindings(t *testing.T) {
	panel := NewBeadsPanel()
	bindings := panel.Keybindings()

	if len(bindings) == 0 {
		t.Error("expected non-empty keybindings")
	}

	// Check for expected actions
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	if !actions["claim"] {
		t.Error("expected 'claim' action in keybindings")
	}
	if !actions["open"] {
		t.Error("expected 'open' action in keybindings")
	}
	if !actions["new"] {
		t.Error("expected 'new' action in keybindings")
	}
}

func TestBeadsPanelInit(t *testing.T) {
	panel := NewBeadsPanel()
	cmd := panel.Init()
	if cmd != nil {
		t.Error("expected Init() to return nil")
	}
}

func TestBeadsPanelUpdate(t *testing.T) {
	panel := NewBeadsPanel()

	newModel, cmd := panel.Update(nil)

	if newModel != panel {
		t.Error("expected Update to return same model")
	}
	if cmd != nil {
		t.Error("expected Update to return nil cmd")
	}
}

func TestBeadsPanelUpdateScrollsFocusedViewport(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(90, 12)

	ready := make([]bv.BeadPreview, 0, 30)
	for i := 0; i < 30; i++ {
		ready = append(ready, bv.BeadPreview{
			ID:       fmt.Sprintf("br-%03d", i),
			Title:    fmt.Sprintf("Ready item %d", i),
			Priority: "P1",
		})
	}
	panel.SetData(bv.BeadsSummary{
		Available:  true,
		Ready:      len(ready),
		InProgress: 4,
		Blocked:    2,
		Closed:     8,
	}, ready, nil)
	panel.View()

	if panel.scroll == nil {
		t.Fatal("expected scrollable panel to be initialized")
	}
	if !panel.scroll.NeedsScroll() {
		t.Fatal("expected beads body to overflow and require scrolling")
	}

	panel.Focus()
	panel.Update(tea.KeyMsg{Type: tea.KeyPgDown})

	if panel.scroll.ScrollPercent() == 0 {
		t.Fatal("expected pgdown to move the beads viewport")
	}
}

func TestBeadsPanelViewZeroWidth(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(0, 20)

	view := panel.View()
	if view != "" {
		t.Errorf("expected empty view for zero width, got: %s", view)
	}
}

func TestBeadsPanelViewContainsTitle(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(80, 20)

	view := panel.View()

	if !strings.Contains(view, "Beads Pipeline") {
		t.Error("expected view to contain title")
	}
}

func TestBeadsPanelViewShowsStats(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(100, 25)

	summary := bv.BeadsSummary{
		Available:  true,
		Ready:      5,
		InProgress: 3,
		Blocked:    2,
		Closed:     10,
	}
	panel.SetData(summary, nil, nil)

	view := panel.View()

	if !strings.Contains(view, "Ready: 5") {
		t.Error("expected view to contain 'Ready: 5'")
	}
	if !strings.Contains(view, "In Progress: 3") {
		t.Error("expected view to contain 'In Progress: 3'")
	}
	if !strings.Contains(view, "Blocked: 2") {
		t.Error("expected view to contain 'Blocked: 2'")
	}
	if !strings.Contains(view, "Closed: 10") {
		t.Error("expected view to contain 'Closed: 10'")
	}
}

func TestBeadsPanelViewShowsInProgress(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(100, 30)

	summary := bv.BeadsSummary{
		Available: true,
		InProgressList: []bv.BeadInProgress{
			{ID: "bead-123", Title: "Working on feature", Assignee: "RedLake"},
		},
	}
	panel.SetData(summary, nil, nil)

	view := panel.View()

	if !strings.Contains(view, "In Progress") {
		t.Error("expected view to contain 'In Progress' section")
	}
	if !strings.Contains(view, "bead-123") {
		t.Error("expected view to contain bead ID")
	}
}

func TestBeadsPanelViewShowsReadyBeads(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(100, 30)

	summary := bv.BeadsSummary{Available: true}
	ready := []bv.BeadPreview{
		{ID: "bead-456", Title: "Ready to work", Priority: "P0"},
		{ID: "bead-789", Title: "Another task", Priority: "P1"},
	}
	panel.SetData(summary, ready, nil)

	view := panel.View()

	if !strings.Contains(view, "Ready / Backlog") {
		t.Error("expected view to contain 'Ready / Backlog' section")
	}
	if !strings.Contains(view, "bead-456") {
		t.Error("expected view to contain first bead ID")
	}
	if !strings.Contains(view, "P0") {
		t.Error("expected view to contain P0 priority")
	}
}

func TestBeadsPanelViewNoReadyItems(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(80, 20)

	summary := bv.BeadsSummary{Available: true}
	panel.SetData(summary, []bv.BeadPreview{}, nil)

	view := panel.View()

	if !strings.Contains(view, "No ready items") {
		t.Error("expected view to contain 'No ready items'")
	}
}

func TestBeadsPanelViewUnavailable(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(80, 20)

	summary := bv.BeadsSummary{Available: false}
	panel.SetData(summary, nil, nil)

	view := panel.View()

	if !strings.Contains(view, "Fetching beads pipeline") {
		t.Error("expected view to contain loading state message")
	}
}

func TestBeadsPanelViewUnavailableWithReason(t *testing.T) {
	panel := NewBeadsPanel()
	panel.SetSize(80, 20)

	// Test "not initialized" case - should show subtle empty state, not error
	summary := bv.BeadsSummary{Available: false, Reason: "bv not installed"}
	panel.SetData(summary, nil, nil)

	view := panel.View()

	// "bv not installed" should show as empty/not initialized state
	if !strings.Contains(view, "Beads not initialized") {
		t.Error("expected view to contain 'Beads not initialized' for bv not installed case")
	}

	summaryBrMissing := bv.BeadsSummary{Available: false, Reason: "br not installed"}
	panel.SetData(summaryBrMissing, nil, nil)

	viewBrMissing := panel.View()

	if !strings.Contains(viewBrMissing, "Beads not initialized") {
		t.Error("expected view to contain 'Beads not initialized' for br not installed case")
	}

	// Test actual error case - should show error with refresh hint
	summary2 := bv.BeadsSummary{Available: false, Reason: "br stats failed: connection refused"}
	panel.SetData(summary2, nil, nil)

	view2 := panel.View()

	if !strings.Contains(view2, "br stats failed") {
		t.Error("expected view to contain error reason for actual failures")
	}
	if !strings.Contains(view2, "Press r") {
		t.Error("expected view to include refresh hint for actual errors")
	}
}

func TestBeadPreviewStruct(t *testing.T) {
	preview := bv.BeadPreview{
		ID:       "test-bead",
		Title:    "Test Title",
		Priority: "P1",
	}

	if preview.ID != "test-bead" {
		t.Errorf("expected ID 'test-bead', got %q", preview.ID)
	}
	if preview.Title != "Test Title" {
		t.Errorf("expected Title 'Test Title', got %q", preview.Title)
	}
	if preview.Priority != "P1" {
		t.Errorf("expected Priority 'P1', got %q", preview.Priority)
	}
}

func TestBeadInProgressStruct(t *testing.T) {
	inProgress := bv.BeadInProgress{
		ID:       "test-bead",
		Title:    "Test Title",
		Assignee: "TestAgent",
	}

	if inProgress.ID != "test-bead" {
		t.Errorf("expected ID 'test-bead', got %q", inProgress.ID)
	}
	if inProgress.Title != "Test Title" {
		t.Errorf("expected Title 'Test Title', got %q", inProgress.Title)
	}
	if inProgress.Assignee != "TestAgent" {
		t.Errorf("expected Assignee 'TestAgent', got %q", inProgress.Assignee)
	}
}
