// Package dashboard provides the main NTM dashboard TUI.
package dashboard

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// paneItem implements list.Item for pane list entries.
// It wraps the underlying PaneTableRow data for bubbles/list integration.
type paneItem struct {
	pane tmux.Pane
	row  PaneTableRow
}

// Title returns the pane title for list display.
func (p paneItem) Title() string {
	return p.row.Title
}

// Description returns the pane description (status and model).
func (p paneItem) Description() string {
	desc := p.row.Status
	if p.row.ModelVariant != "" {
		desc += " | " + p.row.ModelVariant
	}
	return desc
}

// FilterValue returns the string used for fuzzy filtering.
// Includes title and agent type for comprehensive search.
func (p paneItem) FilterValue() string {
	return p.row.Title + " " + p.row.Type + " " + p.row.ModelVariant
}

// paneDelegate implements list.ItemDelegate for pane rendering.
// It preserves the existing visual design from RenderPaneRow.
type paneDelegate struct {
	theme    theme.Theme
	dims     LayoutDimensions
	tick     int
	selected int // Currently selected index for highlight
}

// newPaneDelegate creates a delegate with the given theme and layout.
func newPaneDelegate(t theme.Theme, dims LayoutDimensions) paneDelegate {
	return paneDelegate{
		theme: t,
		dims:  dims,
	}
}

// Height returns the row height based on layout mode.
// Wide+ modes show a second line with badges and details.
func (d paneDelegate) Height() int {
	if d.dims.Mode >= LayoutWide {
		return 2 // Two lines in wide mode
	}
	return 1 // Single line in narrow/split modes
}

// Spacing returns the space between items.
func (d paneDelegate) Spacing() int {
	return 0
}

// Update handles item-specific messages.
func (d paneDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd {
	return nil
}

// Render renders a single pane item.
// This delegates to RenderPaneRow to preserve existing visual design.
func (d paneDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(paneItem)
	if !ok {
		return
	}

	// Mark selection status
	row := item.row
	row.IsSelected = index == m.Index()
	row.Tick = d.tick

	// Use existing RenderPaneRow for visual consistency
	rendered := RenderPaneRow(row, d.dims, d.theme)
	fmt.Fprint(w, rendered)
}

// SetTick updates the animation tick for spinners and pulses.
func (d *paneDelegate) SetTick(tick int) {
	d.tick = tick
}

// SetDims updates the layout dimensions.
func (d *paneDelegate) SetDims(dims LayoutDimensions) {
	d.dims = dims
}

// toPaneItems converts panes and their statuses to list items.
func toPaneItems(panes []tmux.Pane, statuses map[int]PaneStatus, beads []bv.BeadPreview, t theme.Theme) []list.Item {
	items := make([]list.Item, len(panes))
	for i, pane := range panes {
		ps := statuses[pane.Index]
		row := BuildPaneTableRow(pane, ps, beads, nil)
		row.BorderColor = AgentBorderColor(string(pane.Type), t)
		items[i] = paneItem{pane: pane, row: row}
	}
	return items
}

// findPaneIndexByID finds the list index of a pane by its ID.
// Returns -1 if not found.
func findPaneIndexByID(items []list.Item, paneID string) int {
	for i, item := range items {
		if pi, ok := item.(paneItem); ok {
			if pi.pane.ID == paneID {
				return i
			}
		}
	}
	return -1
}
