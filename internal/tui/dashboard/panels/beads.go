package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// beadsConfig returns the configuration for the beads panel
func beadsConfig() PanelConfig {
	return PanelConfig{
		ID:              "beads",
		Title:           "Beads Pipeline",
		Priority:        PriorityHigh, // Important for workflow
		RefreshInterval: 15 * time.Second,
		MinWidth:        30,
		MinHeight:       10,
		Collapsible:     true,
	}
}

type BeadsPanel struct {
	PanelBase
	summary bv.BeadsSummary
	ready   []bv.BeadPreview
	err     error
}

func NewBeadsPanel() *BeadsPanel {
	return &BeadsPanel{
		PanelBase: NewPanelBase(beadsConfig()),
	}
}

func (m *BeadsPanel) SetData(summary bv.BeadsSummary, ready []bv.BeadPreview, err error) {
	m.summary = summary
	m.ready = ready
	m.err = err
}

// HasError returns true if there's an active error
func (m *BeadsPanel) HasError() bool {
	return m.err != nil
}

// Error returns the current error message
func (m *BeadsPanel) Error() string {
	if m.err != nil {
		return m.err.Error()
	}
	return ""
}

func (m *BeadsPanel) Init() tea.Cmd {
	return nil
}

func (m *BeadsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

// Keybindings returns beads panel specific shortcuts
func (m *BeadsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "claim")),
			Description: "Claim selected bead",
			Action:      "claim",
		},
		{
			Key:         key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open")),
			Description: "Open bead details",
			Action:      "open",
		},
		{
			Key:         key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
			Description: "Create new bead",
			Action:      "new",
		},
	}
}

func (m *BeadsPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	if m.IsFocused() {
		borderColor = t.Pink
	}

	// Build header with error badge if needed
	title := m.Config().Title
	if m.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("⚠ Error")
		title = title + " " + errorBadge
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(borderColor).
		Width(w).
		Padding(0, 1).
		Render(title)

	var content strings.Builder
	content.WriteString(header + "\n")

	// Show error message if present
	if m.err != nil {
		errMsg := layout.TruncateRunes(m.err.Error(), w-6, "…")
		content.WriteString(components.ErrorState(errMsg, "Press r to refresh", w) + "\n\n")
	}

	if !m.summary.Available && m.err == nil {
		if strings.TrimSpace(m.summary.Reason) == "" {
			content.WriteString(components.LoadingState("Fetching beads pipeline…", w) + "\n")
		} else {
			reason := layout.TruncateRunes(m.summary.Reason, w-6, "…")
			content.WriteString(components.ErrorState(reason, "Press r to refresh", w) + "\n")
		}
		return content.String()
	}

	// Stats row
	stats := fmt.Sprintf("Ready: %d  In Progress: %d  Blocked: %d  Closed: %d",
		m.summary.Ready, m.summary.InProgress, m.summary.Blocked, m.summary.Closed)
	statsStyled := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Calculate remaining height
	usedHeight := lipgloss.Height(header) + lipgloss.Height(statsStyled) + 2 // +2 for newlines
	remainingHeight := h - usedHeight
	if remainingHeight < 0 {
		remainingHeight = 0
	}

	// Split remaining height between In Progress and Ready
	halfHeight := remainingHeight / 2
	if halfHeight < 3 {
		halfHeight = 3 // Minimum
	}

	// In Progress Section
	if len(m.summary.InProgressList) > 0 {
		content.WriteString(lipgloss.NewStyle().Foreground(t.Blue).Bold(true).Padding(0, 1).Render("In Progress") + "\n")

		for i, b := range m.summary.InProgressList {
			if i >= halfHeight-1 {
				break
			}
			assignee := ""
			if b.Assignee != "" {
				assignee = fmt.Sprintf(" (@%s)", b.Assignee)
			}

			titleWidth := w - 10 - len(assignee)
			if titleWidth < 10 {
				titleWidth = 10
			}

			title := layout.TruncateRunes(b.Title, titleWidth, "…")
			line := fmt.Sprintf("  %s %s%s", b.ID, title, assignee)
			content.WriteString(lipgloss.NewStyle().Foreground(t.Text).Render(line) + "\n")
		}
		content.WriteString("\n")
	}

	// Ready Section
	if len(m.ready) > 0 {
		content.WriteString(lipgloss.NewStyle().Foreground(t.Green).Bold(true).Padding(0, 1).Render("Ready / Backlog") + "\n")

		for i, b := range m.ready {
			if i >= halfHeight-1 {
				break
			}

			prio := b.Priority
			prioStyle := lipgloss.NewStyle().Foreground(t.Overlay)
			if prio == "P0" {
				prioStyle = prioStyle.Foreground(t.Red).Bold(true)
			} else if prio == "P1" {
				prioStyle = prioStyle.Foreground(t.Yellow)
			}

			titleWidth := w - 14
			if titleWidth < 10 {
				titleWidth = 10
			}

			title := layout.TruncateRunes(b.Title, titleWidth, "…")
			line := fmt.Sprintf("  %s %s %s", prioStyle.Render(fmt.Sprintf("% -3s", prio)), b.ID, title)
			content.WriteString(lipgloss.NewStyle().Foreground(t.Text).Render(line) + "\n")
		}
	} else if m.summary.Available {
		content.WriteString("  No ready items\n")
	} else {
		content.WriteString(lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Padding(0, 1).Render("  (Pipeline unavailable)") + "\n")
	}

	return content.String()
}
