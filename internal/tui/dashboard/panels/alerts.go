package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// alertsConfig returns the configuration for the alerts panel
func alertsConfig() PanelConfig {
	return PanelConfig{
		ID:              "alerts",
		Title:           "Active Alerts",
		Priority:        PriorityCritical, // Alerts are highest priority
		RefreshInterval: 3 * time.Second,  // Fast refresh for alerts
		MinWidth:        25,
		MinHeight:       6,
		Collapsible:     false, // Don't hide alerts
	}
}

const newAlertPulseDuration = 3 * time.Second

type AlertsPanel struct {
	PanelBase
	alerts   []alerts.Alert
	err      error
	viewport viewport.Model

	firstSeen map[string]time.Time
	now       func() time.Time
}

func NewAlertsPanel() *AlertsPanel {
	vp := viewport.New(25, 6)
	return &AlertsPanel{
		PanelBase: NewPanelBase(alertsConfig()),
		viewport:  vp,
		firstSeen: make(map[string]time.Time),
		now:       time.Now,
	}
}

func (m *AlertsPanel) SetData(alertList []alerts.Alert, err error) {
	m.alerts = alertList
	m.err = err

	nowFn := m.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	if m.firstSeen == nil {
		m.firstSeen = make(map[string]time.Time, len(alertList))
	}

	keep := make(map[string]struct{}, len(alertList))
	for _, a := range alertList {
		key := m.alertKey(a)
		keep[key] = struct{}{}
		if _, ok := m.firstSeen[key]; !ok {
			m.firstSeen[key] = now
		}
	}
	for key := range m.firstSeen {
		if _, ok := keep[key]; !ok {
			delete(m.firstSeen, key)
		}
	}
}

// HasError returns true if there's an active error
func (m *AlertsPanel) HasError() bool {
	return m.err != nil
}

func (m *AlertsPanel) Init() tea.Cmd {
	return nil
}

func (m *AlertsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.IsFocused() {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// Keybindings returns alerts panel specific shortcuts
func (m *AlertsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dismiss")),
			Description: "Dismiss selected alert",
			Action:      "dismiss",
		},
		{
			Key:         key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "ack all")),
			Description: "Acknowledge all alerts",
			Action:      "ack_all",
		},
	}
}

func (m *AlertsPanel) alertKey(a alerts.Alert) string {
	if a.ID != "" {
		return a.ID
	}
	// Fallback for older/edge alert sources that don't set ID.
	return fmt.Sprintf("%s|%s|%s|%s|%s", a.Type, a.Severity, a.Session, a.Pane, a.Message)
}

func (m *AlertsPanel) shouldPulse(key string, now time.Time) bool {
	if m.firstSeen == nil {
		return false
	}
	seenAt, ok := m.firstSeen[key]
	if !ok {
		return false
	}
	age := now.Sub(seenAt)
	return age >= 0 && age < newAlertPulseDuration
}

func (m *AlertsPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	nowFn := m.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	// Keep tick bounded to avoid int overflow on 32-bit platforms.
	tick := int((now.UnixMilli() / 100) % 10_000)

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
		errorStyle := lipgloss.NewStyle().
			Foreground(t.Red).
			Italic(true).
			Padding(0, 1)
		errMsg := layout.TruncateWidthDefault(m.err.Error(), w-6)
		content.WriteString(errorStyle.Render("⚠ "+errMsg) + "\n\n")
	}

	if len(m.alerts) == 0 && m.err == nil {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconSuccess,
			Title:       "All clear",
			Description: "No alerts to display",
			Width:       w,
			Centered:    true,
		}))
		// Ensure stable height to prevent layout jitter
		return boxStyle.Render(FitToHeight(content.String(), h))
	} else if len(m.alerts) == 0 {
		// Ensure stable height to prevent layout jitter
		return boxStyle.Render(FitToHeight(content.String(), h))
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

	// Render ALL alerts into viewport body (let viewport handle scrolling)
	var body strings.Builder

	renderList := func(list []alerts.Alert, color lipgloss.Color, icon string) {
		for _, a := range list {
			msg := layout.TruncateWidthDefault(a.Message, w-6)
			line := fmt.Sprintf("  %s %s", icon, msg)
			style := lipgloss.NewStyle().Foreground(color)
			if m.shouldPulse(m.alertKey(a), now) {
				style = style.Foreground(styles.Pulse(string(color), tick)).Bold(true)
			}
			body.WriteString(style.Render(line) + "\n")
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

	// Update viewport dimensions and content
	vpHeight := h - 5 // header + stats + newlines + scroll indicator
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.Width = w
	m.viewport.Height = vpHeight
	m.viewport.SetContent(body.String())

	content.WriteString(m.viewport.View())

	// Show scroll indicator if content overflows
	totalLines := lipgloss.Height(body.String())
	if totalLines > vpHeight {
		scrollPct := int(m.viewport.ScrollPercent() * 100)
		scrollHint := lipgloss.NewStyle().Foreground(t.Overlay).Render(fmt.Sprintf(" ↕ %d%%", scrollPct))
		content.WriteString("\n" + scrollHint)
	}

	// Ensure stable height to prevent layout jitter
	return boxStyle.Render(FitToHeight(content.String(), h))
}
