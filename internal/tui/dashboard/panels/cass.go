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
	hits         []cass.SearchHit
	cursor       int
	offset       int
	theme        theme.Theme
	err          error
	scroll       *components.ScrollablePanel
	lastBodyHash string
}

func NewCASSPanel() *CASSPanel {
	return &CASSPanel{
		PanelBase: NewPanelBase(cassConfig()),
		theme:     theme.Current(),
		scroll:    components.NewScrollablePanel(35, 8),
	}
}

func (m *CASSPanel) Init() tea.Cmd {
	return nil
}

func (m *CASSPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() {
		return m, nil
	}

	m.syncScrollBody()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case msg.Type == tea.KeyUp || msg.String() == "k":
			m.moveCursor(-1)
			m.ensureCursorVisible()
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyDown || msg.String() == "j":
			m.moveCursor(1)
			m.ensureCursorVisible()
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyPgUp:
			m.pageCursor(-1)
			m.ensureCursorVisible()
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyPgDown:
			m.pageCursor(1)
			m.ensureCursorVisible()
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyHome:
			m.cursor = 0
			if m.scroll != nil {
				m.scroll.GotoTop()
			}
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyEnd:
			if len(m.hits) > 0 {
				m.cursor = len(m.hits) - 1
				if m.scroll != nil {
					m.scroll.GotoBottom()
				}
			}
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		}
	}

	if m.scroll != nil {
		var cmd tea.Cmd
		m.scroll, cmd = m.scroll.Update(msg)
		m.syncOffsetFromScroll()
		m.clampCursorToVisible()
		m.syncScrollBody()
		return m, cmd
	}

	return m, nil
}

func (m *CASSPanel) SetData(hits []cass.SearchHit, err error) {
	selectedKey := ""
	if selected, ok := m.selectedHit(); ok {
		selectedKey = cassHitKey(selected)
	}

	m.err = err
	if err != nil {
		hits = nil
	}

	m.hits = append([]cass.SearchHit(nil), hits...)
	sort.SliceStable(m.hits, func(i, j int) bool {
		return m.hits[i].Score > m.hits[j].Score
	})

	if selectedKey != "" && m.selectHitByKey(selectedKey) {
		if m.offset > m.cursor {
			m.offset = m.cursor
		}
	} else {
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
	m.syncScrollBody()
	m.ensureCursorVisible()
	m.syncOffsetFromScroll()
}

func (m *CASSPanel) selectedHit() (cass.SearchHit, bool) {
	if len(m.hits) == 0 || m.cursor < 0 || m.cursor >= len(m.hits) {
		return cass.SearchHit{}, false
	}
	return m.hits[m.cursor], true
}

func (m *CASSPanel) selectHitByKey(target string) bool {
	for i, hit := range m.hits {
		if cassHitKey(hit) == target {
			m.cursor = i
			return true
		}
	}
	return false
}

func cassHitKey(hit cass.SearchHit) string {
	line := 0
	if hit.LineNumber != nil {
		line = *hit.LineNumber
	}
	return fmt.Sprintf("%s|%s|%d|%s|%s|%s|%s",
		hit.SessionID,
		hit.SourcePath,
		line,
		hit.Title,
		hit.MatchType,
		hit.Agent,
		hit.CreatedAtTime().UTC().Format(time.RFC3339Nano),
	)
}

func (m *CASSPanel) HasError() bool {
	return m.err != nil
}

// HandlesOwnHeight returns true because the CASS body is viewport-managed.
func (m *CASSPanel) HandlesOwnHeight() bool {
	return true
}

func (m *CASSPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "search")),
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
	height := m.Height() - 6 // borders + header + footer
	if height < 3 {
		height = 3
	}
	return height
}

func (m *CASSPanel) contentWidth() int {
	width := m.Width() - 4
	if width < 1 {
		width = 1
	}
	return width
}

func (m *CASSPanel) syncScrollBody() {
	if m.Width() <= 0 || m.Height() <= 0 {
		return
	}
	if m.scroll == nil {
		m.scroll = components.NewScrollablePanel(m.contentWidth(), m.contentHeight())
	}
	m.scroll.SetSize(m.contentWidth(), m.contentHeight())
	bodyStr := m.renderBody()
	if bodyStr != m.lastBodyHash {
		m.scroll.SetContent(bodyStr)
		m.lastBodyHash = bodyStr
	}
}

func (m *CASSPanel) renderBody() string {
	if len(m.hits) == 0 {
		return ""
	}

	t := m.theme
	innerWidth := m.contentWidth()
	var body strings.Builder
	for i, hit := range m.hits {
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

		titleWidth := innerWidth - 13
		if titleWidth < 8 {
			titleWidth = 8
		}
		name := layout.TruncateWidthDefault(hit.Title, titleWidth)
		line := fmt.Sprintf("%s %s %s", score, age, name)
		body.WriteString(lineStyle.Render(line))
		body.WriteByte('\n')
	}
	return strings.TrimRight(body.String(), "\n")
}

func (m *CASSPanel) moveCursor(delta int) {
	if len(m.hits) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	m.cursor += delta
	if m.cursor >= len(m.hits) {
		m.cursor = len(m.hits) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *CASSPanel) pageCursor(direction int) {
	step := m.contentHeight() - 1
	if step < 1 {
		step = 1
	}
	m.moveCursor(direction * step)
}

func (m *CASSPanel) syncOffsetFromScroll() {
	if m.scroll == nil || len(m.hits) == 0 {
		m.offset = 0
		return
	}
	state := m.scroll.ScrollState()
	if state.TotalItems == 0 {
		m.offset = 0
		return
	}
	m.offset = state.FirstVisible
}

func (m *CASSPanel) clampCursorToVisible() {
	if m.scroll == nil || len(m.hits) == 0 {
		return
	}
	state := m.scroll.ScrollState()
	if state.TotalItems == 0 {
		return
	}
	lastVisible := state.LastVisible
	if lastVisible < state.FirstVisible {
		lastVisible = state.FirstVisible
	}
	if m.cursor < state.FirstVisible {
		m.cursor = state.FirstVisible
	}
	if m.cursor > lastVisible {
		m.cursor = lastVisible
	}
}

func (m *CASSPanel) ensureCursorVisible() {
	if m.scroll == nil || len(m.hits) == 0 {
		return
	}
	for i := 0; i < len(m.hits)+1; i++ {
		state := m.scroll.ScrollState()
		if state.TotalItems == 0 || state.AllVisible() {
			break
		}
		lastVisible := state.LastVisible
		if lastVisible < state.FirstVisible {
			lastVisible = state.FirstVisible
		}
		switch {
		case m.cursor < state.FirstVisible:
			m.scroll.Update(tea.KeyMsg{Type: tea.KeyUp})
		case m.cursor > lastVisible:
			m.scroll.Update(tea.KeyMsg{Type: tea.KeyDown})
		default:
			m.offset = state.FirstVisible
			return
		}
	}
	m.syncOffsetFromScroll()
}

func (m *CASSPanel) View() string {
	t := m.theme
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0 // Subtle tint for focused panel
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
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
		errMsg := layout.TruncateWidthDefault(m.err.Error(), w-6)
		content.WriteString(components.ErrorState(errMsg, "Press r to refresh", w-4) + "\n")
	}

	if len(m.hits) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No context found",
			Description: "Relevant history will appear here",
			Width:       w - 4,
			Centered:    true,
		}))
		// Ensure stable height to prevent layout jitter
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	m.syncScrollBody()
	m.ensureCursorVisible()
	m.syncOffsetFromScroll()

	content.WriteString(m.scroll.View())

	if m.scroll != nil && m.scroll.NeedsScroll() {
		if footer := components.ScrollFooter(m.scroll.ScrollState(), m.contentWidth()); footer != "" {
			content.WriteString("\n" + footer)
		}
	}

	// Ensure stable height to prevent layout jitter
	return boxStyle.Render(FitToHeight(content.String(), h-4))
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
