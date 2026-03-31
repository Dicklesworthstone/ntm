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
	summary      bv.BeadsSummary
	ready        []bv.BeadPreview
	err          error
	scroll       *components.ScrollablePanel
	lastBodyHash string // Track content changes to avoid resetting scroll position
}

func NewBeadsPanel() *BeadsPanel {
	return &BeadsPanel{
		PanelBase: NewPanelBase(beadsConfig()),
		scroll:    components.NewScrollablePanel(30, 10),
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
	if m.IsFocused() && m.scroll != nil {
		var cmd tea.Cmd
		m.scroll, cmd = m.scroll.Update(msg)
		return m, cmd
	}
	return m, nil
}

// Keybindings returns beads panel specific shortcuts
func (m *BeadsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "up")),
			Description: "Scroll up",
			Action:      "page_up",
		},
		{
			Key:         key.NewBinding(key.WithKeys("pgdn"), key.WithHelp("pgdn", "down")),
			Description: "Scroll down",
			Action:      "page_down",
		},
		{
			Key:         key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
			Description: "Jump to top",
			Action:      "top",
		},
		{
			Key:         key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
			Description: "Jump to bottom",
			Action:      "bottom",
		},
	}
}

// HandlesOwnHeight returns true because the beads body is viewport-managed.
func (m *BeadsPanel) HandlesOwnHeight() bool {
	return true
}

func (m *BeadsPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Pink
		bgColor = t.Surface0 // Subtle tint for focused panel
	}

	// Create box style for background tint
	boxStyle := lipgloss.NewStyle().
		Background(bgColor).
		Width(w).
		Height(h)

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
		errMsg := layout.TruncateWidthDefault(m.err.Error(), w-6)
		content.WriteString(components.ErrorState(errMsg, "Press r to refresh", w) + "\n\n")
	}

	if !m.summary.Available && m.err == nil {
		if strings.TrimSpace(m.summary.Reason) == "" {
			content.WriteString(components.LoadingState("Fetching beads pipeline…", w) + "\n")
		} else {
			// Check if this is a "no beads" case vs an actual error
			reason := m.summary.Reason
			isNotInitialized := strings.Contains(reason, "no .beads") ||
				strings.Contains(reason, "bv not installed") ||
				strings.Contains(reason, "bd not installed") ||
				strings.Contains(reason, "br not installed")
			if isNotInitialized {
				// Show subtle "not initialized" message instead of error
				content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
					Icon:        components.IconExternal,
					Title:       "Beads not initialized",
					Description: "Run 'br init' in your project",
					Action:      "to enable issue tracking",
					Width:       w,
					Centered:    true,
				}) + "\n")
			} else {
				// Actual error - show with refresh hint
				truncReason := layout.TruncateWidthDefault(reason, w-6)
				content.WriteString(components.ErrorState(truncReason, "Press r to refresh", w) + "\n")
			}
		}
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	// Stats row
	stats := fmt.Sprintf("Ready: %d  In Progress: %d  Blocked: %d  Closed: %d",
		m.summary.Ready, m.summary.InProgress, m.summary.Blocked, m.summary.Closed)
	statsStyled := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Build scrollable body content (render ALL items, let viewport handle scrolling)
	var body strings.Builder

	// In Progress Section
	if len(m.summary.InProgressList) > 0 {
		body.WriteString(lipgloss.NewStyle().Foreground(t.Blue).Bold(true).Padding(0, 1).Render("In Progress") + "\n")

		for _, b := range m.summary.InProgressList {
			assignee := ""
			if b.Assignee != "" {
				assignee = fmt.Sprintf(" (@%s)", b.Assignee)
			}

			titleWidth := w - 10 - lipgloss.Width(assignee)
			if titleWidth < 10 {
				titleWidth = 10
			}

			title := layout.TruncateWidthDefault(b.Title, titleWidth)
			line := fmt.Sprintf("  %s %s%s", b.ID, title, assignee)
			body.WriteString(lipgloss.NewStyle().Foreground(t.Text).Render(line) + "\n")
		}
		body.WriteString("\n")
	}

	// Ready Section
	if len(m.ready) > 0 {
		body.WriteString(lipgloss.NewStyle().Foreground(t.Green).Bold(true).Padding(0, 1).Render("Ready / Backlog") + "\n")

		for _, b := range m.ready {
			prio := b.Priority
			prioStyle := lipgloss.NewStyle().Foreground(t.Overlay)
			switch prio {
			case "P0":
				prioStyle = prioStyle.Foreground(t.Red).Bold(true)
			case "P1":
				prioStyle = prioStyle.Foreground(t.Yellow)
			}

			titleWidth := w - 14
			if titleWidth < 10 {
				titleWidth = 10
			}

			title := layout.TruncateWidthDefault(b.Title, titleWidth)
			line := fmt.Sprintf("  %s %s %s", prioStyle.Render(fmt.Sprintf("% -3s", prio)), b.ID, title)
			body.WriteString(lipgloss.NewStyle().Foreground(t.Text).Render(line) + "\n")
		}
	} else if m.summary.Available {
		body.WriteString("  No ready items\n")
	} else {
		body.WriteString(lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Padding(0, 1).Render("  (Pipeline unavailable)") + "\n")
	}

	// Update viewport dimensions; only reset content when it actually changes
	// to preserve the user's scroll position across refreshes.
	usedHeight := lipgloss.Height(header) + lipgloss.Height(statsStyled) + 3 // +3 for newlines + scroll indicator
	vpHeight := h - usedHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	if m.scroll == nil {
		m.scroll = components.NewScrollablePanel(w, vpHeight)
	}
	m.scroll.SetSize(w, vpHeight)
	bodyStr := body.String()
	if bodyStr != m.lastBodyHash {
		m.scroll.SetContent(bodyStr)
		m.lastBodyHash = bodyStr
	}

	content.WriteString(m.scroll.View())

	// Show scroll indicator if content overflows
	if m.scroll.NeedsScroll() {
		if footer := components.ScrollFooter(m.scroll.ScrollState(), w); footer != "" {
			content.WriteString("\n" + footer)
		}
	}

	// Ensure stable height to prevent layout jitter
	return boxStyle.Render(FitToHeight(content.String(), h))
}
