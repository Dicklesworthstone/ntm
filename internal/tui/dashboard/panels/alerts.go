package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
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
	alerts       []alerts.Alert
	err          error
	scroll       *components.ScrollablePanel
	lastBodyHash string // Track content changes to preserve scroll position

	firstSeen map[string]time.Time
	now       func() time.Time
}

func NewAlertsPanel() *AlertsPanel {
	return &AlertsPanel{
		PanelBase: NewPanelBase(alertsConfig()),
		scroll:    components.NewScrollablePanel(25, 6),
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
	if m.IsFocused() && m.scroll != nil {
		var cmd tea.Cmd
		m.scroll, cmd = m.scroll.Update(msg)
		return m, cmd
	}
	return m, nil
}

// Keybindings returns alerts panel specific shortcuts
func (m *AlertsPanel) Keybindings() []Keybinding {
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

// HandlesOwnHeight returns true because the alerts body is viewport-managed.
func (m *AlertsPanel) HandlesOwnHeight() bool {
	return true
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
