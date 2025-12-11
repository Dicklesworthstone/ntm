package panels

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type BeadsPanel struct {
	width   int
	height  int
	focused bool
	summary bv.BeadsSummary
	ready   []bv.BeadPreview
}

func NewBeadsPanel() *BeadsPanel {
	return &BeadsPanel{}
}

func (m *BeadsPanel) SetData(summary bv.BeadsSummary, ready []bv.BeadPreview) {
	m.summary = summary
	m.ready = ready
}

func (m *BeadsPanel) Init() tea.Cmd {
	return nil
}

func (m *BeadsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m *BeadsPanel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *BeadsPanel) Focus() {
	m.focused = true
}

func (m *BeadsPanel) Blur() {
	m.focused = false
}

func (m *BeadsPanel) View() string {
	t := theme.Current()

	if m.width <= 0 {
		return ""
	}

	borderColor := t.Surface1
	if m.focused {
		borderColor = t.Pink
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(borderColor).
		Width(m.width).
		Padding(0, 1).
		Render("Beads Pipeline")

	var content strings.Builder
	content.WriteString(header + "\n")

	// Stats row
	stats := fmt.Sprintf("Ready: %d  In Progress: %d  Blocked: %d  Closed: %d",
		m.summary.Ready, m.summary.InProgress, m.summary.Blocked, m.summary.Closed)
	statsStyled := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Calculate remaining height
	usedHeight := lipgloss.Height(header) + lipgloss.Height(statsStyled) + 2 // +2 for newlines
	remainingHeight := m.height - usedHeight
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
			
			titleWidth := m.width - 10 - len(assignee)
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
			
			titleWidth := m.width - 14
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