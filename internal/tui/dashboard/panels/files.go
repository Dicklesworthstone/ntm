package panels

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// filesConfig returns the configuration for the files panel
func filesConfig() PanelConfig {
	return PanelConfig{
		ID:              "files",
		Title:           "File Changes",
		Priority:        PriorityNormal,
		RefreshInterval: 5 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

// TimeWindow represents a time filter for file changes
type TimeWindow int

const (
	WindowAll TimeWindow = iota
	Window1h
	Window15m
	Window5m
)

func (w TimeWindow) String() string {
	switch w {
	case Window5m:
		return "5m"
	case Window15m:
		return "15m"
	case Window1h:
		return "1h"
	default:
		return "all"
	}
}

func (w TimeWindow) Duration() time.Duration {
	switch w {
	case Window5m:
		return 5 * time.Minute
	case Window15m:
		return 15 * time.Minute
	case Window1h:
		return time.Hour
	default:
		return 0 // All time
	}
}

// FilesPanel displays recent file changes with agent attribution
type FilesPanel struct {
	PanelBase
	allChanges   []tracker.RecordedFileChange
	changes      []tracker.RecordedFileChange
	cursor       int
	offset       int
	timeWindow   TimeWindow
	theme        theme.Theme
	err          error
	scroll       *components.ScrollablePanel
	lastBodyHash string
}

// NewFilesPanel creates a new files panel
func NewFilesPanel() *FilesPanel {
	return &FilesPanel{
		PanelBase:  NewPanelBase(filesConfig()),
		timeWindow: Window15m, // Default to 15 minute window
		theme:      theme.Current(),
		scroll:     components.NewScrollablePanel(30, 8),
	}
}

// Init implements tea.Model
func (m *FilesPanel) Init() tea.Cmd {
	return nil
}

// OpenFileMsg is sent when the user wants to open the selected file in an editor.
type OpenFileMsg struct {
	Change tracker.RecordedFileChange
}

// Update implements tea.Model
func (m *FilesPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if len(m.changes) > 0 {
				m.cursor = len(m.changes) - 1
				if m.scroll != nil {
					m.scroll.GotoBottom()
				}
			}
			m.syncOffsetFromScroll()
			m.syncScrollBody()
			return m, nil
		case msg.Type == tea.KeyEnter:
			change, ok := m.selectedChange()
			if !ok {
				return m, nil
			}
			return m, func() tea.Msg { return OpenFileMsg{Change: change} }
		case msg.Type == tea.KeyTab || msg.String() == "t":
			m.timeWindow = (m.timeWindow + 1) % 4
			m.refreshVisibleChanges()
			m.syncScrollBody()
			m.ensureCursorVisible()
			m.syncOffsetFromScroll()
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

// SetData updates the file changes
func (m *FilesPanel) SetData(changes []tracker.RecordedFileChange, err error) {
	m.allChanges = append([]tracker.RecordedFileChange(nil), changes...)
	m.refreshVisibleChanges()
	m.err = err
	m.syncScrollBody()
	m.ensureCursorVisible()
	m.syncOffsetFromScroll()
}

func (m *FilesPanel) refreshVisibleChanges() {
	selectedKey, hasSelection := m.selectedChangeKey()
	m.changes = m.filterByTimeWindow(m.allChanges)
	if hasSelection && m.selectChangeByKey(selectedKey) {
		return
	}
	m.clampCursor()
}

// filterByTimeWindow filters changes based on the current time window
func (m *FilesPanel) filterByTimeWindow(changes []tracker.RecordedFileChange) []tracker.RecordedFileChange {
	if m.timeWindow == WindowAll {
		return changes
	}

	cutoff := time.Now().Add(-m.timeWindow.Duration())
	var filtered []tracker.RecordedFileChange
	for _, c := range changes {
		if c.Timestamp.After(cutoff) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// HasError returns true if there's an active error
func (m *FilesPanel) HasError() bool {
	return m.err != nil
}

// HandlesOwnHeight returns true because the files body is viewport-managed.
func (m *FilesPanel) HandlesOwnHeight() bool {
	return true
}

// Keybindings returns files panel specific shortcuts
func (m *FilesPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "window")),
			Description: "Cycle time window",
			Action:      "cycle_window",
		},
		{
			Key:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			Description: "Open file in editor",
			Action:      "open",
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

func (m *FilesPanel) contentHeight() int {
	height := m.Height() - 7 // borders + header + stats + footer
	if height < 3 {
		height = 3
	}
	return height
}

func (m *FilesPanel) contentWidth() int {
	width := m.Width() - 4
	if width < 1 {
		width = 1
	}
	return width
}

func (m *FilesPanel) syncScrollBody() {
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

func (m *FilesPanel) renderBody() string {
	if len(m.changes) == 0 {
		return ""
	}

	t := m.theme
	innerWidth := m.contentWidth()
	var body strings.Builder
	for i, change := range m.changes {
		selected := i == m.cursor

		var lineStyle lipgloss.Style
		if selected {
			lineStyle = lipgloss.NewStyle().Background(t.Surface0).Bold(true)
		} else {
			lineStyle = lipgloss.NewStyle()
		}

		var prefix string
		var prefixColor lipgloss.Color
		switch change.Change.Type {
		case tracker.FileAdded:
			prefix = "+"
			prefixColor = t.Green
		case tracker.FileModified:
			prefix = "~"
			prefixColor = t.Yellow
		case tracker.FileDeleted:
			prefix = "-"
			prefixColor = t.Red
		default:
			prefix = "?"
			prefixColor = t.Overlay
		}
		prefixStyled := lipgloss.NewStyle().Foreground(prefixColor).Bold(true).Render(prefix)

		filename := filepath.Base(change.Change.Path)
		agentLabel := ""
		if len(change.Agents) > 0 {
			agent := change.Agents[0]
			if len(change.Agents) > 1 {
				agent = fmt.Sprintf("%s+%d", agent, len(change.Agents)-1)
			}
			agentLabel = "@" + agent
		}
		timeAgo := m.formatTimeAgo(change.Timestamp)

		maxFilename := innerWidth - len(agentLabel) - len(timeAgo) - 8
		if maxFilename < 10 {
			maxFilename = 10
		}
		filename = layout.TruncateWidthDefault(filename, maxFilename)

		filenameStyled := lipgloss.NewStyle().Foreground(t.Text).Render(filename)
		agentStyled := lipgloss.NewStyle().Foreground(t.Blue).Render(agentLabel)
		timeStyled := lipgloss.NewStyle().Foreground(t.Overlay).Render(timeAgo)

		line := fmt.Sprintf(" %s %s %s %s", prefixStyled, filenameStyled, agentStyled, timeStyled)
		body.WriteString(lineStyle.Render(line))
		body.WriteByte('\n')
	}

	return strings.TrimRight(body.String(), "\n")
}

func (m *FilesPanel) selectedChange() (tracker.RecordedFileChange, bool) {
	if len(m.changes) == 0 || m.cursor < 0 || m.cursor >= len(m.changes) {
		return tracker.RecordedFileChange{}, false
	}
	return m.changes[m.cursor], true
}

func (m *FilesPanel) selectedChangeKey() (string, bool) {
	change, ok := m.selectedChange()
	if !ok {
		return "", false
	}
	return fileChangeKey(change), true
}

func (m *FilesPanel) selectChangeByKey(target string) bool {
	if target == "" {
		return false
	}
	for i, change := range m.changes {
		if fileChangeKey(change) == target {
			m.cursor = i
			return true
		}
	}
	return false
}

func fileChangeKey(change tracker.RecordedFileChange) string {
	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s",
		change.Timestamp.UTC().Format(time.RFC3339Nano),
		change.Session,
		change.Change.Path,
		change.Change.Type,
		strings.Join(change.Agents, "\x1f"),
	)
}

func (m *FilesPanel) clampCursor() {
	if len(m.changes) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor >= len(m.changes) {
		m.cursor = len(m.changes) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	maxOffset := len(m.changes) - 1
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *FilesPanel) moveCursor(delta int) {
	if len(m.changes) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	m.cursor += delta
	m.clampCursor()
}

func (m *FilesPanel) pageCursor(direction int) {
	step := m.contentHeight() - 1
	if step < 1 {
		step = 1
	}
	m.moveCursor(direction * step)
}

func (m *FilesPanel) syncOffsetFromScroll() {
	if m.scroll == nil || len(m.changes) == 0 {
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

func (m *FilesPanel) clampCursorToVisible() {
	if m.scroll == nil || len(m.changes) == 0 {
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

func (m *FilesPanel) ensureCursorVisible() {
	if m.scroll == nil || len(m.changes) == 0 {
		return
	}
	for i := 0; i < len(m.changes)+1; i++ {
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

// View renders the panel
func (m *FilesPanel) View() string {
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

	// Build header with error badge and time window if needed
	title := m.Config().Title
	windowBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Subtext).
		Padding(0, 1).
		Render(m.timeWindow.String())
	title = title + " " + windowBadge

	if m.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("! Error")
		title = title + " " + errorBadge
	}

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(w - 4).
		Align(lipgloss.Center)

	content.WriteString(headerStyle.Render(title) + "\n")

	// Show error message if present
	if m.err != nil {
		content.WriteString(components.ErrorState(m.err.Error(), "Press r to retry", w-4) + "\n")
	}

	// Stats row
	stats := m.buildStats()
	statsStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1)
	content.WriteString(statsStyle.Render(stats) + "\n")

	if len(m.changes) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconEmpty,
			Title:       "No recent changes",
			Description: "File changes will appear here",
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

// buildStats returns a summary string of file changes
func (m *FilesPanel) buildStats() string {
	var added, modified, deleted int
	for _, c := range m.changes {
		switch c.Change.Type {
		case tracker.FileAdded:
			added++
		case tracker.FileModified:
			modified++
		case tracker.FileDeleted:
			deleted++
		}
	}
	return fmt.Sprintf("+%d ~%d -%d", added, modified, deleted)
}

// formatTimeAgo returns a human-readable time difference
func (m *FilesPanel) formatTimeAgo(t time.Time) string {
	d := time.Since(t)
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
