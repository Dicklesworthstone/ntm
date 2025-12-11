package panels

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type AlertsPanel struct {
	width   int
	height  int
	focused bool
	alerts  []alerts.Alert
}

func NewAlertsPanel() *AlertsPanel {
	return &AlertsPanel{}
}

func (m *AlertsPanel) SetData(alerts []alerts.Alert) {
	m.alerts = alerts
}

func (m *AlertsPanel) Init() tea.Cmd {
	return nil
}

func (m *AlertsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m *AlertsPanel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *AlertsPanel) Focus() {
	m.focused = true
}

func (m *AlertsPanel) Blur() {
	m.focused = false
}

func (m *AlertsPanel) View() string {
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
		Render("Active Alerts")
	var content strings.Builder
	content.WriteString(header + "\n")

	if len(m.alerts) == 0 {
		content.WriteString("\n  " + lipgloss.NewStyle().Foreground(t.Green).Render("✓ System Healthy") + "\n")
		content.WriteString("  " + lipgloss.NewStyle().Foreground(t.Subtext).Render("No active alerts") + "\n")
		return content.String()
	}

	// Group by severity
	var critical, warning, info []alerts.Alert
	for _, a := range m.alerts {
		switch a.Severity {
		case alerts.SeverityCritical:
			critical = append(critical, a)
		case alerts.SeverityWarning:
			warning = append(warning, a)
		default:
			info = append(info, a)
		}
	}

	// Stats row
	stats := fmt.Sprintf("Crit: %d  Warn: %d  Info: %d", len(critical), len(warning), len(info))
	statsStyled := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Calculate display limit based on height
	// Header + Stats + 2 newlines = ~4 lines
	// Each item = 1 line
	availableLines := m.height - 4
	if availableLines < 0 {
		availableLines = 0
	}

	// Render alerts (Critical > Warning > Info)
	count := 0
	
	renderList := func(list []alerts.Alert, color lipgloss.Color, icon string) {
		for _, a := range list {
			if count >= availableLines {
				return
			}
			msg := layout.TruncateRunes(a.Message, m.width-6, "…")
			line := fmt.Sprintf("  %s %s", icon, msg)
			content.WriteString(lipgloss.NewStyle().Foreground(color).Render(line) + "\n")
			count++
		}
	}

	if len(critical) > 0 {
		renderList(critical, t.Red, "✗")
	}
	if len(warning) > 0 {
		renderList(warning, t.Yellow, "⚠")
	}
	if len(info) > 0 {
		renderList(info, t.Blue, "ℹ")
	}

	return content.String()
}