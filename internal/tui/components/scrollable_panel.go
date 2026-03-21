// Package components provides shared TUI building blocks.
package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// ScrollablePanel wraps bubbles/viewport for scrollable content panels.
// It provides a consistent interface for panels that need to display
// content larger than their allocated height.
type ScrollablePanel struct {
	vp      viewport.Model
	content string
	ready   bool
}

// NewScrollablePanel creates a new scrollable panel with the given dimensions.
func NewScrollablePanel(width, height int) *ScrollablePanel {
	vp := viewport.New(width, height)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3
	return &ScrollablePanel{
		vp:    vp,
		ready: true,
	}
}

// SetContent sets the panel's content string.
func (sp *ScrollablePanel) SetContent(content string) {
	sp.content = content
	sp.vp.SetContent(content)
}

// SetSize updates the viewport dimensions.
func (sp *ScrollablePanel) SetSize(width, height int) {
	sp.vp.Width = width
	sp.vp.Height = height
	// Re-set content to recalculate scroll bounds
	if sp.content != "" {
		sp.vp.SetContent(sp.content)
	}
}

// Width returns the viewport width.
func (sp *ScrollablePanel) Width() int {
	return sp.vp.Width
}

// Height returns the viewport height.
func (sp *ScrollablePanel) Height() int {
	return sp.vp.Height
}

// Update handles tea messages for scrolling.
func (sp *ScrollablePanel) Update(msg tea.Msg) (*ScrollablePanel, tea.Cmd) {
	var cmd tea.Cmd
	sp.vp, cmd = sp.vp.Update(msg)
	return sp, cmd
}

// View renders the viewport content.
func (sp *ScrollablePanel) View() string {
	return sp.vp.View()
}

// ScrollPercent returns the current scroll position as a percentage (0.0-1.0).
func (sp *ScrollablePanel) ScrollPercent() float64 {
	return sp.vp.ScrollPercent()
}

// AtTop returns true if scrolled to the top.
func (sp *ScrollablePanel) AtTop() bool {
	return sp.vp.AtTop()
}

// AtBottom returns true if scrolled to the bottom.
func (sp *ScrollablePanel) AtBottom() bool {
	return sp.vp.AtBottom()
}

// GotoTop scrolls to the top.
func (sp *ScrollablePanel) GotoTop() {
	sp.vp.GotoTop()
}

// GotoBottom scrolls to the bottom.
func (sp *ScrollablePanel) GotoBottom() {
	sp.vp.GotoBottom()
}

// LineDown scrolls down one line.
func (sp *ScrollablePanel) LineDown(n int) {
	sp.vp.LineDown(n)
}

// LineUp scrolls up one line.
func (sp *ScrollablePanel) LineUp(n int) {
	sp.vp.LineUp(n)
}

// HalfPageDown scrolls down half a page.
func (sp *ScrollablePanel) HalfPageDown() {
	sp.vp.HalfViewDown()
}

// HalfPageUp scrolls up half a page.
func (sp *ScrollablePanel) HalfPageUp() {
	sp.vp.HalfViewUp()
}

// TotalLines returns the total number of content lines.
func (sp *ScrollablePanel) TotalLines() int {
	return sp.vp.TotalLineCount()
}

// VisibleLines returns the number of currently visible lines.
func (sp *ScrollablePanel) VisibleLines() int {
	return sp.vp.VisibleLineCount()
}

// YOffset returns the current Y offset (scroll position in lines).
func (sp *ScrollablePanel) YOffset() int {
	return sp.vp.YOffset
}

// ScrollState returns the current scroll state for use with scroll indicators.
func (sp *ScrollablePanel) ScrollState() ScrollState {
	total := sp.TotalLines()
	visible := sp.VisibleLines()
	offset := sp.YOffset()

	// Calculate first and last visible line indices
	firstVisible := offset
	lastVisible := offset + visible - 1
	if lastVisible >= total {
		lastVisible = total - 1
	}
	if lastVisible < 0 {
		lastVisible = 0
	}

	return ScrollState{
		FirstVisible: firstVisible,
		LastVisible:  lastVisible,
		TotalItems:   total,
	}
}

// ScrollIndicator returns the scroll indicator string based on current position.
func (sp *ScrollablePanel) ScrollIndicator() string {
	return sp.ScrollState().Indicator()
}

// NeedsScroll returns true if the content is larger than the viewport.
func (sp *ScrollablePanel) NeedsScroll() bool {
	return sp.TotalLines() > sp.Height()
}

// ContentHeight returns the height needed to display all content without scrolling.
func (sp *ScrollablePanel) ContentHeight() int {
	return strings.Count(sp.content, "\n") + 1
}
