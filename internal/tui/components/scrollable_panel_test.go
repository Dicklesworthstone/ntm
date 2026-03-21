package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewScrollablePanel(t *testing.T) {
	sp := NewScrollablePanel(80, 24)
	if sp == nil {
		t.Fatal("NewScrollablePanel returned nil")
	}
	if sp.Width() != 80 {
		t.Errorf("Width() = %d, want 80", sp.Width())
	}
	if sp.Height() != 24 {
		t.Errorf("Height() = %d, want 24", sp.Height())
	}
	t.Logf("Created panel: %dx%d", sp.Width(), sp.Height())
}

func TestScrollablePanelSetContent(t *testing.T) {
	sp := NewScrollablePanel(40, 10)
	content := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	sp.SetContent(content)

	view := sp.View()
	if view == "" {
		t.Error("View() returned empty after SetContent")
	}
	t.Logf("Content lines: %d, View lines: %d", strings.Count(content, "\n")+1, strings.Count(view, "\n")+1)
}

func TestScrollablePanelSetSize(t *testing.T) {
	sp := NewScrollablePanel(80, 24)
	sp.SetSize(120, 40)

	if sp.Width() != 120 {
		t.Errorf("Width() = %d after SetSize, want 120", sp.Width())
	}
	if sp.Height() != 40 {
		t.Errorf("Height() = %d after SetSize, want 40", sp.Height())
	}
}

func TestScrollablePanelScrollPosition(t *testing.T) {
	sp := NewScrollablePanel(40, 5)

	// Create content that needs scrolling (20 lines in 5-line viewport)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))

	// Should start at top
	if !sp.AtTop() {
		t.Error("AtTop() should be true initially")
	}
	if sp.AtBottom() {
		t.Error("AtBottom() should be false initially")
	}
	if sp.ScrollPercent() != 0 {
		t.Errorf("ScrollPercent() = %f, want 0", sp.ScrollPercent())
	}

	// Scroll to bottom
	sp.GotoBottom()
	if !sp.AtBottom() {
		t.Error("AtBottom() should be true after GotoBottom")
	}
	if sp.AtTop() {
		t.Error("AtTop() should be false after GotoBottom")
	}

	// Scroll back to top
	sp.GotoTop()
	if !sp.AtTop() {
		t.Error("AtTop() should be true after GotoTop")
	}

	t.Logf("Total lines: %d, Visible: %d", sp.TotalLines(), sp.VisibleLines())
}

func TestScrollablePanelScrollState(t *testing.T) {
	sp := NewScrollablePanel(40, 5)

	// Small content that doesn't need scrolling
	sp.SetContent("Line 1\nLine 2\nLine 3")
	state := sp.ScrollState()
	if !state.AllVisible() {
		t.Error("AllVisible() should be true for small content")
	}

	// Large content that needs scrolling
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))
	state = sp.ScrollState()

	if state.AllVisible() {
		t.Error("AllVisible() should be false for large content")
	}
	if !state.HasMoreBelow() {
		t.Error("HasMoreBelow() should be true at top of large content")
	}
	if state.HasMoreAbove() {
		t.Error("HasMoreAbove() should be false at top")
	}

	t.Logf("ScrollState: first=%d, last=%d, total=%d",
		state.FirstVisible, state.LastVisible, state.TotalItems)
}

func TestScrollablePanelScrollIndicator(t *testing.T) {
	sp := NewScrollablePanel(40, 5)

	// Content that doesn't need scrolling
	sp.SetContent("Line 1\nLine 2")
	indicator := sp.ScrollIndicator()
	if indicator != "" {
		t.Errorf("ScrollIndicator() = %q for small content, want empty", indicator)
	}

	// Large content at top
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))
	indicator = sp.ScrollIndicator()
	if indicator != "▼" {
		t.Errorf("ScrollIndicator() = %q at top, want '▼'", indicator)
	}

	// Scroll to middle
	sp.LineDown(5)
	indicator = sp.ScrollIndicator()
	if indicator != "▲▼" {
		t.Errorf("ScrollIndicator() = %q in middle, want '▲▼'", indicator)
	}

	// Scroll to bottom
	sp.GotoBottom()
	indicator = sp.ScrollIndicator()
	if indicator != "▲" {
		t.Errorf("ScrollIndicator() = %q at bottom, want '▲'", indicator)
	}

	t.Logf("Indicators tested: top=▼, middle=▲▼, bottom=▲")
}

func TestScrollablePanelNeedsScroll(t *testing.T) {
	sp := NewScrollablePanel(40, 10)

	// Small content
	sp.SetContent("Line 1\nLine 2\nLine 3")
	if sp.NeedsScroll() {
		t.Error("NeedsScroll() should be false for small content")
	}

	// Large content
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))
	if !sp.NeedsScroll() {
		t.Error("NeedsScroll() should be true for large content")
	}
}

func TestScrollablePanelUpdate(t *testing.T) {
	sp := NewScrollablePanel(40, 10)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))

	// Simulate a key message (though viewport handles its own keys)
	_, cmd := sp.Update(tea.KeyMsg{Type: tea.KeyDown})
	// cmd may or may not be nil depending on viewport state
	_ = cmd

	// Panel should still be valid
	if sp.Width() != 40 {
		t.Errorf("Width() changed after Update: %d", sp.Width())
	}
}

func TestScrollablePanelLineScrolling(t *testing.T) {
	sp := NewScrollablePanel(40, 5)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	sp.SetContent(strings.Join(lines, "\n"))

	initialOffset := sp.YOffset()
	sp.LineDown(3)
	if sp.YOffset() != initialOffset+3 {
		t.Errorf("YOffset after LineDown(3) = %d, want %d", sp.YOffset(), initialOffset+3)
	}

	sp.LineUp(1)
	if sp.YOffset() != initialOffset+2 {
		t.Errorf("YOffset after LineUp(1) = %d, want %d", sp.YOffset(), initialOffset+2)
	}

	t.Logf("Line scrolling: down 3, up 1, final offset=%d", sp.YOffset())
}

func BenchmarkScrollablePanelView(b *testing.B) {
	sp := NewScrollablePanel(80, 24)
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("x", 80)
	}
	sp.SetContent(strings.Join(lines, "\n"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sp.View()
	}
}

func BenchmarkScrollablePanelScrollState(b *testing.B) {
	sp := NewScrollablePanel(80, 24)
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("x", 80)
	}
	sp.SetContent(strings.Join(lines, "\n"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sp.ScrollState()
	}
}
