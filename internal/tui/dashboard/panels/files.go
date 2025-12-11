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
	changes    []tracker.RecordedFileChange
	cursor     int
	offset     int
	timeWindow TimeWindow
	theme      theme.Theme
	err        error
}

// NewFilesPanel creates a new files panel
func NewFilesPanel() *FilesPanel {
	return &FilesPanel{
		PanelBase:  NewPanelBase(filesConfig()),
		timeWindow: Window15m, // Default to 15 minute window
		theme:      theme.Current(),
	}
}

// Init implements tea.Model
func (m *FilesPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *FilesPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.cursor < len(m.changes)-1 {
				m.cursor++
				if m.cursor >= m.offset+m.contentHeight() {
					m.offset = m.cursor - m.contentHeight() + 1
				}
			}
		case "tab":
			// Cycle time window
			m.timeWindow = (m.timeWindow + 1) % 4
		}
	}
	return m, nil
}

// SetData updates the file changes
func (m *FilesPanel) SetData(changes []tracker.RecordedFileChange, err error) {
	m.changes = m.filterByTimeWindow(changes)
	m.err = err
	// Keep cursor within bounds
	if m.cursor >= len(m.changes) {
		m.cursor = len(m.changes) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
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

// Keybindings returns files panel specific shortcuts
func (m *FilesPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "window")),
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
	return m.Height() - 5 // borders + header + stats row
}

// View renders the panel
func (m *FilesPanel) View() string {
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
		errorStyle := lipgloss.NewStyle().
			Foreground(t.Red).
			Italic(true).
			Padding(0, 1)
		errMsg := layout.TruncateRunes(m.err.Error(), w-6, "...")
		content.WriteString(errorStyle.Render("! "+errMsg) + "\n")
	}

	// Stats row
	stats := m.buildStats()
	statsStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1)
	content.WriteString(statsStyle.Render(stats) + "\n")

	if len(m.changes) == 0 {
		content.WriteString("\n" + lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Padding(0, 1).Render("No file changes"))
		return boxStyle.Render(content.String())
	}

	// File list
	visibleHeight := m.contentHeight()
	end := m.offset + visibleHeight
	if end > len(m.changes) {
		end = len(m.changes)
	}

	for i := m.offset; i < end; i++ {
		change := m.changes[i]
		selected := i == m.cursor

		var lineStyle lipgloss.Style
		if selected {
			lineStyle = lipgloss.NewStyle().Background(t.Surface0).Bold(true)
		} else {
			lineStyle = lipgloss.NewStyle()
		}

		// Change type prefix with color
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

		// File path (basename for display, truncated)
		filename := filepath.Base(change.Change.Path)
		maxFilename := w - 20
		if maxFilename < 10 {
			maxFilename = 10
		}
		filename = layout.TruncateRunes(filename, maxFilename, "...")
		filenameStyled := lipgloss.NewStyle().Foreground(t.Text).Render(filename)

		// Agent attribution
		agentStr := ""
		if len(change.Agents) > 0 {
			agent := change.Agents[0]
			if len(change.Agents) > 1 {
				agent = fmt.Sprintf("%s+%d", agent, len(change.Agents)-1)
			}
			agentStr = lipgloss.NewStyle().Foreground(t.Blue).Render("@" + agent)
		}

		// Time ago
		timeAgo := m.formatTimeAgo(change.Timestamp)
		timeStyled := lipgloss.NewStyle().Foreground(t.Overlay).Render(timeAgo)

		line := fmt.Sprintf(" %s %s %s %s", prefixStyled, filenameStyled, agentStr, timeStyled)
		content.WriteString(lineStyle.Render(line) + "\n")
	}

	return boxStyle.Render(content.String())
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
