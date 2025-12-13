package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func cassConfig() PanelConfig {
	return PanelConfig{
		ID:              "cass",
		Title:           "CASS Context",
		Priority:        PriorityNormal,
		RefreshInterval: 15 * time.Minute,
		MinWidth:        35,
		MinHeight:       8,
		Collapsible:     true,
	}
}

// CASSPanel displays recent CASS search hits for the current session.
type CASSPanel struct {
	PanelBase
	hits   []cass.SearchHit
	cursor int
	offset int
	theme  theme.Theme
	err    error
}

func NewCASSPanel() *CASSPanel {
	return &CASSPanel{
		PanelBase: NewPanelBase(cassConfig()),
		theme:     theme.Current(),
	}
}

func (m *CASSPanel) Init() tea.Cmd {
	return nil
}

func (m *CASSPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		case "down", "j":
			if m.cursor < len(m.hits)-1 {
				m.cursor++
				if m.cursor >= m.offset+m.contentHeight() {
					m.offset = m.cursor - m.contentHeight() + 1
				}
			}
		}
	}

	return m, nil
}

func (m *CASSPanel) SetData(hits []cass.SearchHit, err error) {
	m.err = err

	m.hits = append([]cass.SearchHit(nil), hits...)
	sort.SliceStable(m.hits, func(i, j int) bool {
		return m.hits[i].Score > m.hits[j].Score
	})

	if m.cursor >= len(m.hits) {
		m.cursor = len(m.hits) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
}

func (m *CASSPanel) HasError() bool {
	return m.err != nil
}

func (m *CASSPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
			Description: "Manual CASS search",
			Action:      "search",
		},
		{
			Key:         key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "down")),
			Description: "Move cursor down",
			Action:      "down",
		},
		{
			Key:         key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "up")),
			Description: "Move cursor up",
			Action:      "up",
		},
	}
}

func (m *CASSPanel) contentHeight() int {
	return m.Height() - 4 // borders + header
}

func (m *CASSPanel) View() string {
	t := m.theme
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	if m.IsFocused() {
		borderColor = t.Primary
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

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

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(w - 4).
		Align(lipgloss.Center)

	content.WriteString(headerStyle.Render(title) + "\n")

	if m.err != nil {
		errMsg := layout.TruncateRunes(m.err.Error(), w-6, "…")
		content.WriteString(components.ErrorState(errMsg, "Press r to refresh", w-4) + "\n")
	}

	if len(m.hits) == 0 {
		content.WriteString("\n" + components.EmptyState("No matches", w-4))
		return boxStyle.Render(content.String())
	}

	visibleHeight := m.contentHeight()
	end := m.offset + visibleHeight
	if end > len(m.hits) {
		end = len(m.hits)
	}

	for i := m.offset; i < end; i++ {
		hit := m.hits[i]
		selected := i == m.cursor

		var lineStyle lipgloss.Style
		if selected {
			lineStyle = lipgloss.NewStyle().Background(t.Surface0).Bold(true)
		} else {
			lineStyle = lipgloss.NewStyle()
		}

		score := lipgloss.NewStyle().
			Foreground(t.Blue).
			Width(5).
			Align(lipgloss.Right).
			Render(fmt.Sprintf("%.2f", hit.Score))

		age := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Width(5).
			Render(formatAge(hit.CreatedAtTime()))

		titleWidth := w - 4 - 5 - 1 - 5 - 1 // box padding + score + spaces + age + spaces
		if titleWidth < 8 {
			titleWidth = 8
		}
		name := layout.TruncateRunes(hit.Title, titleWidth, "…")

		line := fmt.Sprintf("%s %s %s", score, age, name)
		content.WriteString(lineStyle.Render(line) + "\n")
	}

	return boxStyle.Render(content.String())
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
