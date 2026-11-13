package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestScrollablePanelSetContent(t *testing.T) {
	sp := NewScrollablePanel(40, 10)
	sp.SetContent("line1\nline2\nline3")
	view := sp.View()
	if !strings.Contains(view, "line1") {
		t.Error("content not visible in view")
	}
}

func TestScrollablePanelOverflow(t *testing.T) {
	sp := NewScrollablePanel(40, 3)
	sp.SetContent(strings.Repeat("line\n", 20))
	if sp.AtBottom() {
		t.Error("should not be at bottom with overflow content")
	}
	if !sp.NeedsScrolling() {
		t.Error("should need scrolling with overflow content")
	}
}

func TestScrollablePanelPgDown(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	sp.SetContent(strings.Repeat("line\n", 50))

	// Simulate PgDown key press
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if sp.ScrollPercent() == 0 {
		t.Error("PgDown should scroll content")
	}
}

func TestScrollablePanelResize(t *testing.T) {
	sp := NewScrollablePanel(40, 10)
	sp.SetContent(strings.Repeat("line\n", 20))
	sp.SetSize(80, 20)

	if sp.Width() != 80 {
		t.Errorf("Width() = %d, want 80", sp.Width())
	}
	if sp.Height() != 20 {
		t.Errorf("Height() = %d, want 20", sp.Height())
	}

	view := sp.View()
	if view == "" {
		t.Error("should render after resize")
	}
}

func TestScrollablePanelBoundaries(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	sp.SetContent(strings.Repeat("line\n", 50))

	if !sp.AtTop() {
		t.Error("should start at top")
	}

	sp.GotoBottom()
	if !sp.AtBottom() {
		t.Error("should be at bottom after GotoBottom")
	}

	sp.GotoTop()
	if !sp.AtTop() {
		t.Error("should be at top after GotoTop")
	}
}

func TestScrollablePanelFitsNoScroll(t *testing.T) {
	sp := NewScrollablePanel(40, 20)
	sp.SetContent("short\ncontent")

	if sp.NeedsScrolling() {
		t.Error("should not need scrolling for short content")
	}
	if sp.ScrollIndicator() != "" {
		t.Error("should not show scroll indicator for short content")
	}
}

func TestScrollIndicatorIntegration(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	sp.SetContent(strings.Repeat("line\n", 50))

	ind := sp.ScrollIndicator()
	if ind == "" {
		t.Error("should show scroll indicator for overflow content")
	}
}

func TestScrollablePanelPageNavigationKeys(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	sp.SetContent(strings.Repeat("line\n", 60))

	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if sp.ScrollPercent() == 0 {
		t.Fatal("expected pgdown to scroll forward")
	}

	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !sp.AtBottom() {
		t.Fatal("expected end to jump to the bottom")
	}

	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyHome})
	if !sp.AtTop() {
		t.Fatal("expected home to jump back to the top")
	}
}

func TestScrollablePanelScrollIndicatorTracksOffset(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	sp.SetContent(strings.Join(lines, "\n"))

	if got := sp.ScrollIndicator(); got != "▼" {
		t.Fatalf("ScrollIndicator() at top = %q, want %q", got, "▼")
	}

	sp.LineDown(4)
	if got := sp.ScrollIndicator(); got != "▲▼" {
		t.Fatalf("ScrollIndicator() in middle = %q, want %q", got, "▲▼")
	}
	if got := sp.YOffset(); got != 4 {
		t.Fatalf("YOffset() after LineDown = %d, want 4", got)
	}

	sp.GotoBottom()
	if got := sp.ScrollIndicator(); got != "▲" {
		t.Fatalf("ScrollIndicator() at bottom = %q, want %q", got, "▲")
	}
}

func TestScrollablePanelResizePreservesScrollOffset(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	sp.SetContent(strings.Join(lines, "\n"))
	sp.LineDown(6)

	if got := sp.YOffset(); got != 6 {
		t.Fatalf("YOffset() before resize = %d, want 6", got)
	}

	sp.SetSize(60, 7)

	if got := sp.YOffset(); got != 6 {
		t.Fatalf("YOffset() after resize = %d, want 6", got)
	}
	if got := sp.VisibleLines(); got != 7 {
		t.Fatalf("VisibleLines() after resize = %d, want 7", got)
	}
}

func TestScrollablePanelContentChange(t *testing.T) {
	sp := NewScrollablePanel(40, 10)

	// Set initial content
	sp.SetContent("initial")
	if sp.TotalLines() != 1 {
		t.Errorf("TotalLines() = %d, want 1", sp.TotalLines())
	}

	// Change content
	sp.SetContent("line1\nline2\nline3")
	if sp.TotalLines() != 3 {
		t.Errorf("TotalLines() = %d, want 3", sp.TotalLines())
	}
}

func TestScrollablePanelEmptyContent(t *testing.T) {
	sp := NewScrollablePanel(40, 10)
	sp.SetContent("")

	if sp.TotalLines() != 0 {
		t.Errorf("TotalLines() = %d, want 0 for empty content", sp.TotalLines())
	}
	if sp.NeedsScrolling() {
		t.Error("should not need scrolling for empty content")
	}
}

func TestScrollablePanelRenderWithIndicatorsIncludesPercentBadge(t *testing.T) {
	sp := NewScrollablePanel(24, 4)
	sp.SetContent(strings.Repeat("line\n", 20))

	rendered := sp.RenderWithIndicators(24)
	if !strings.Contains(rendered, "%") {
		t.Fatalf("expected RenderWithIndicators to include percent badge, got %q", rendered)
	}
	if !strings.Contains(rendered, "▼") {
		t.Fatalf("expected RenderWithIndicators to include scroll direction, got %q", rendered)
	}
}

func TestScrollablePanelClampsInvalidDimensions(t *testing.T) {
	sp := NewScrollablePanel(0, -2)
	if sp.Width() != 1 {
		t.Fatalf("Width() = %d, want 1", sp.Width())
	}
	if sp.Height() != 1 {
		t.Fatalf("Height() = %d, want 1", sp.Height())
	}

	sp.SetContent(strings.Repeat("line\n", 5))
	sp.SetSize(-4, 0)
	if sp.Width() != 1 {
		t.Fatalf("Width() after SetSize = %d, want 1", sp.Width())
	}
	if sp.Height() != 1 {
		t.Fatalf("Height() after SetSize = %d, want 1", sp.Height())
	}

	rendered := sp.RenderWithIndicators(0)
	if rendered == "" {
		t.Fatal("expected RenderWithIndicators to render with clamped width")
	}
}

func TestScrollablePanelVimAndHalfPageKeybindings(t *testing.T) {
	sp := NewScrollablePanel(30, 6)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	sp.SetContent(strings.Join(lines, "\n"))

	if !sp.AtTop() {
		t.Fatal("expected initial AtTop()")
	}
	if got := sp.RenderWithIndicators(30); !strings.Contains(got, "0%") {
		t.Fatalf("expected 0%% at top, got %q", got)
	}

	// j and down scroll down 1 line each
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if sp.YOffset() != 1 {
		t.Fatalf("YOffset after 'j' = %d, want 1", sp.YOffset())
	}
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyDown})
	if sp.YOffset() != 2 {
		t.Fatalf("YOffset after Down = %d, want 2", sp.YOffset())
	}

	// k and up scroll up 1 line each
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if sp.YOffset() != 1 {
		t.Fatalf("YOffset after 'k' = %d, want 1", sp.YOffset())
	}
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !sp.AtTop() {
		t.Fatalf("expected AtTop() after Up, YOffset=%d", sp.YOffset())
	}

	// ctrl+d scrolls half page down, ctrl+u scrolls half page up
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if sp.YOffset() != 3 {
		t.Fatalf("YOffset after Ctrl+D = %d, want 3", sp.YOffset())
	}
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if !sp.AtTop() {
		t.Fatalf("expected AtTop() after Ctrl+U, YOffset=%d", sp.YOffset())
	}

	// G jumps to bottom, g jumps to top
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if !sp.AtBottom() {
		t.Fatal("expected AtBottom() after 'G'")
	}
	if got := sp.RenderWithIndicators(30); !strings.Contains(got, "100%") {
		t.Fatalf("expected 100%% at bottom, got %q", got)
	}
	sp, _ = sp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if !sp.AtTop() {
		t.Fatal("expected AtTop() after 'g'")
	}
}
