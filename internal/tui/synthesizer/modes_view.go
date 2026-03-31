package synthesizer

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// CloseMsg requests closing the modes view overlay.
type CloseMsg struct{}

// RefreshMsg requests reloading ensemble mode data.
type RefreshMsg struct{}

// ZoomMsg requests zooming to a tmux pane index.
type ZoomMsg struct {
	PaneIndex int
}

type KeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Refresh key.Binding
	Zoom    key.Binding
	Close   key.Binding
}

var defaultKeys = KeyMap{
	Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "prev")),
	Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next")),
	Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Zoom:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "zoom")),
	Close:   key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc/q", "close")),
}

// ModeVisualization is a full-screen Bubble Tea component that displays ensemble reasoning modes.
type ModeVisualization struct {
	Session   string
	Strategy  string
	Width     int
	Height    int
	Focused   bool
	Keys      KeyMap
	cursor    int
	session   *ensemble.EnsembleSession
	catalog   *ensemble.ModeCatalog
	paneIndex map[string]int // assignment.PaneName -> tmux pane index
	err       error
}

func NewModeVisualization() ModeVisualization {
	return ModeVisualization{
		Focused: true,
		Keys:    defaultKeys,
	}
}

func (m *ModeVisualization) SetSize(width, height int) {
	m.Width = width
	m.Height = height
}

// SetData updates the visualization with new ensemble session data.
func (m *ModeVisualization) SetData(sessionName string, sess *ensemble.EnsembleSession, catalog *ensemble.ModeCatalog, panes []tmux.Pane, err error) {
	m.Session = sessionName
	m.session = sess
	m.catalog = catalog
	m.err = err

	m.paneIndex = make(map[string]int, len(panes))
	for _, p := range panes {
		if strings.TrimSpace(p.Title) == "" {
			continue
		}
		m.paneIndex[p.Title] = p.Index
	}

	if sess != nil {
		m.Strategy = sess.SynthesisStrategy.String()
	} else {
		m.Strategy = ""
	}

	m.cursor = clampInt(m.cursor, 0, maxInt(0, assignmentCount(sess)-1))
}

// Init implements tea.Model.
func (m ModeVisualization) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m ModeVisualization) Update(msg tea.Msg) (ModeVisualization, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.Keys.Close):
			return m, func() tea.Msg { return CloseMsg{} }
		case key.Matches(msg, m.Keys.Refresh):
			return m, func() tea.Msg { return RefreshMsg{} }
		case key.Matches(msg, m.Keys.Up):
			m.cursor--
			if m.cursor < 0 {
				m.cursor = 0
			}
			return m, nil
		case key.Matches(msg, m.Keys.Down):
			m.cursor++
			maxCursor := maxInt(0, assignmentCount(m.session)-1)
			if m.cursor > maxCursor {
				m.cursor = maxCursor
			}
			return m, nil
		case key.Matches(msg, m.Keys.Zoom):
			if idx, ok := m.selectedPaneIndex(); ok {
				return m, func() tea.Msg { return ZoomMsg{PaneIndex: idx} }
			}
			return m, nil
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m ModeVisualization) View() string {
	t := theme.Current()
	w := m.Width
	h := m.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	contentWidth := maxInt(w, 20)
	contentHeight := maxInt(h, 8)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Text)
	subStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	dimStyle := lipgloss.NewStyle().Foreground(t.Overlay)

	var b strings.Builder

	ensembleName := m.Session
	if strings.TrimSpace(ensembleName) == "" && m.session != nil {
		ensembleName = m.session.SessionName
	}
	if strings.TrimSpace(ensembleName) == "" {
		ensembleName = "—"
	}

	header := fmt.Sprintf("Ensemble: %s", ensembleName)
	b.WriteString(subStyle.Render(layout.TruncateWidthDefault(header, contentWidth)) + "\n")
	b.WriteString(titleStyle.Render("Reasoning Modes") + "\n")
	b.WriteString(dimStyle.Render(strings.Repeat("━", maxInt(0, minInt(contentWidth, 36)))) + "\n\n")

	if m.err != nil {
		b.WriteString(components.ErrorState(m.err.Error(), "Press r to retry, esc to close", contentWidth))
		return fitToHeight(b.String(), contentHeight)
	}

	if m.session == nil || len(m.session.Assignments) == 0 {
		desc := "No ensemble assignments found."
		if m.session == nil {
			desc = "No ensemble session found for this tmux session."
		}
		b.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No modes to display",
			Description: desc,
			Width:       contentWidth,
			Centered:    true,
		}))
		return fitToHeight(b.String(), contentHeight)
	}

	assignments := m.session.Assignments
	barWidth := clampInt(contentWidth/6, 10, 24)

	for i, a := range assignments {
		line := m.renderAssignmentLine(i, a, contentWidth, barWidth)
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	done, total := summarizeCompletion(assignments)
	strategy := strings.TrimSpace(m.Strategy)
	if strategy == "" {
		strategy = "—"
	}
	footer1 := fmt.Sprintf("Progress: %d/%d complete", done, total)
	footer2 := fmt.Sprintf("Strategy: %s", strategy)
	footerHint := "enter=zoom  r=refresh  esc/q=close"

	b.WriteString(subStyle.Render(layout.TruncateWidthDefault(footer1, contentWidth)) + "\n")
	b.WriteString(subStyle.Render(layout.TruncateWidthDefault(footer2, contentWidth)) + "\n")
	b.WriteString(dimStyle.Render(layout.TruncateWidthDefault(footerHint, contentWidth)))

	return fitToHeight(strings.TrimRight(b.String(), "\n"), contentHeight)
}

func (m ModeVisualization) renderAssignmentLine(i int, a ensemble.ModeAssignment, width, barWidth int) string {
	t := theme.Current()

	cursorPrefix := " "
	if i == m.cursor {
		cursorPrefix = "›"
	}
	idxLabel := fmt.Sprintf("[%d]", i)

	modeLabel := a.ModeID
	modeTier := ensemble.ModeTier("")
	if m.catalog != nil {
		if mode := m.catalog.GetMode(a.ModeID); mode != nil {
			modeLabel = strings.TrimSpace(mode.Code)
			if modeLabel == "" {
				modeLabel = strings.ToUpper(mode.ID)
			}
			name := strings.TrimSpace(mode.ShortDesc)
			if name == "" {
				name = strings.TrimSpace(mode.Name)
			}
			if name != "" {
				modeLabel = modeLabel + " " + name
			}
			modeTier = mode.Tier
		}
	}

	tierChip := ensemble.TierChip(modeTier)

	agentLabel := modeAssignmentAgentLabel(a.AgentType)
	agentStyle := lipgloss.NewStyle().Foreground(modeAssignmentAgentColor(a.AgentType, t))
	if agentLabel != "unk" {
		agentStyle = agentStyle.Bold(true)
	}

	statusIcon := assignmentStatusIcon(a.Status)
	progress := assignmentProgress(a.Status)
	bar := renderProgressBar(progress, barWidth)

	// Layout budget: prefix + idx + space + label + spaces + tier + spaces + agent + space + icon + space + bar
	fixed := lipgloss.Width(cursorPrefix) + 1 + lipgloss.Width(idxLabel) + 1
	fixed += lipgloss.Width(tierChip) + 2
	fixed += 6 + 2 // agent field padded to 6, plus spacing
	fixed += lipgloss.Width(statusIcon) + 1 + barWidth

	labelWidth := width - fixed
	if labelWidth < 10 {
		labelWidth = 10
	}

	label := layout.TruncateWidthDefault(modeLabel, labelWidth)

	lineStyle := lipgloss.NewStyle().Foreground(t.Text)
	if i == m.cursor {
		lineStyle = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	}

	return lineStyle.Render(cursorPrefix) + " " +
		lineStyle.Render(idxLabel) + " " +
		layout.TruncateWidthDefault(label, labelWidth) + "  " +
		tierChip + "  " +
		agentStyle.Render(fmt.Sprintf("%-6s", agentLabel)) + " " +
		lipgloss.NewStyle().Foreground(t.Subtext).Render(statusIcon) + " " +
		bar
}

func modeAssignmentAgentLabel(agentType string) string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "cc"
	case agent.AgentTypeCodex:
		return "cod"
	case agent.AgentTypeGemini:
		return "gmi"
	case agent.AgentTypeCursor:
		return "cur"
	case agent.AgentTypeWindsurf:
		return "ws"
	case agent.AgentTypeAider:
		return "aid"
	case agent.AgentTypeOllama:
		return "oll"
	case agent.AgentTypeUser:
		return "usr"
	default:
		trimmed := strings.TrimSpace(strings.ToLower(agentType))
		if trimmed == "" {
			return "unk"
		}
		return trimmed[:min(3, len(trimmed))]
	}
}

func modeAssignmentAgentColor(agentType string, t theme.Theme) lipgloss.Color {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return t.Claude
	case agent.AgentTypeCodex:
		return t.Codex
	case agent.AgentTypeGemini:
		return t.Gemini
	case agent.AgentTypeCursor:
		return t.Cursor
	case agent.AgentTypeWindsurf:
		return t.Windsurf
	case agent.AgentTypeAider:
		return t.Aider
	case agent.AgentTypeOllama:
		return t.Ollama
	case agent.AgentTypeUser:
		return t.User
	default:
		return t.Subtext
	}
}

func (m ModeVisualization) selectedPaneIndex() (int, bool) {
	if m.session == nil || m.cursor < 0 || m.cursor >= len(m.session.Assignments) {
		return 0, false
	}
	name := strings.TrimSpace(m.session.Assignments[m.cursor].PaneName)
	if name == "" {
		return 0, false
	}
	idx, ok := m.paneIndex[name]
	return idx, ok
}

func summarizeCompletion(assignments []ensemble.ModeAssignment) (done, total int) {
	total = len(assignments)
	for _, a := range assignments {
		if a.Status == ensemble.AssignmentDone {
			done++
		}
	}
	return done, total
}

func assignmentCount(sess *ensemble.EnsembleSession) int {
	if sess == nil {
		return 0
	}
	return len(sess.Assignments)
}

func assignmentProgress(status ensemble.AssignmentStatus) float64 {
	switch status {
	case ensemble.AssignmentPending:
		return 0.05
	case ensemble.AssignmentInjecting:
		return 0.25
	case ensemble.AssignmentActive:
		return 0.6
	case ensemble.AssignmentDone, ensemble.AssignmentError:
		return 1.0
	default:
		return 0
	}
}

func assignmentStatusIcon(status ensemble.AssignmentStatus) string {
	switch status {
	case ensemble.AssignmentActive:
		return "●"
	case ensemble.AssignmentInjecting:
		return "◐"
	case ensemble.AssignmentPending:
		return "○"
	case ensemble.AssignmentDone:
		return "✓"
	case ensemble.AssignmentError:
		return "✗"
	default:
		return "•"
	}
}

func renderProgressBar(percent float64, width int) string {
	bar := components.NewProgressBar(width)
	bar.ShowPercent = false
	bar.ShowLabel = false
	bar.Animated = false
	bar.SetPercent(percent)
	return bar.View()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fitToHeight(content string, targetHeight int) string {
	if targetHeight <= 0 {
		return content
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > targetHeight {
		lines = lines[:targetHeight]
	}
	for len(lines) < targetHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
