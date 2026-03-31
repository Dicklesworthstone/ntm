package panels

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// rateLimitConfig returns the configuration for the rate limit panel
func rateLimitConfig() PanelConfig {
	return PanelConfig{
		ID:              "ratelimit",
		Title:           "OAuth/Rate Status",
		Priority:        PriorityHigh,
		RefreshInterval: 5 * time.Second,
		MinWidth:        30,
		MinHeight:       6,
		Collapsible:     true,
	}
}

// RateLimitPanel displays OAuth and rate limit status per agent
type RateLimitPanel struct {
	PanelBase
	agents []robot.AgentOAuthHealth
	err    error
}

// NewRateLimitPanel creates a new rate limit status panel
func NewRateLimitPanel() *RateLimitPanel {
	return &RateLimitPanel{
		PanelBase: NewPanelBase(rateLimitConfig()),
	}
}

// SetData updates the panel with new OAuth/rate limit data
func (m *RateLimitPanel) SetData(agents []robot.AgentOAuthHealth, err error) {
	m.agents = agents
	m.err = err
}

// HasError returns true if there's an active error
func (m *RateLimitPanel) HasError() bool {
	return m.err != nil
}

// Init implements tea.Model
func (m *RateLimitPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *RateLimitPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

// View implements tea.Model
func (m *RateLimitPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Pink
		bgColor = t.Surface0
	}

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
			Render("!")
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

	// Show error if present
	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(t.Red).
			Italic(true).
			Padding(0, 1)
		errMsg := layout.TruncateWidthDefault(m.err.Error(), w-6)
		content.WriteString(errorStyle.Render("! "+errMsg) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	if len(m.agents) == 0 {
		content.WriteString("\n")
		noAgentsStyle := lipgloss.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Padding(0, 1)
		content.WriteString(noAgentsStyle.Render("No agents to display") + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	// Calculate available lines for agent rows
	availableLines := h - 2 // header + spacing
	if availableLines < 1 {
		availableLines = 1
	}

	// Render each agent row
	for i, agent := range m.agents {
		if i >= availableLines {
			break
		}

		line := m.formatAgentRow(agent, w, t)
		content.WriteString(line + "\n")
	}

	// Show overflow indicator if needed
	if len(m.agents) > availableLines {
		moreStyle := lipgloss.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Padding(0, 1)
		content.WriteString(moreStyle.Render(fmt.Sprintf("... +%d more", len(m.agents)-availableLines)) + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h))
}

// formatAgentRow formats a single agent's status line
func (m *RateLimitPanel) formatAgentRow(agent robot.AgentOAuthHealth, width int, t theme.Theme) string {
	if width <= 0 {
		return ""
	}

	// Format: "cc-1: OAuth=✓ Rate=OK  Last=2m ago"
	var sb strings.Builder

	// Agent identifier
	agentID := fmt.Sprintf("%s-%d", m.shortAgentType(agent.AgentType), agent.Pane)
	sb.WriteString(fmt.Sprintf("  %s: ", agentID))

	// OAuth status with color
	oauthStyle := m.getOAuthStyle(agent.OAuthStatus, t)
	oauthIcon := m.getOAuthIcon(agent.OAuthStatus)
	sb.WriteString(oauthStyle.Render(fmt.Sprintf("OAuth=%s", oauthIcon)))
	sb.WriteString(" ")

	// Rate limit status with color
	rateStyle := m.getRateLimitStyle(agent.RateLimitStatus, t)
	rateIcon := m.getRateLimitIcon(agent.RateLimitStatus)
	sb.WriteString(rateStyle.Render(fmt.Sprintf("Rate=%s", rateIcon)))
	sb.WriteString(" ")

	// Last activity
	lastActivity := m.formatLastActivity(agent.LastActivitySec)
	activityStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	sb.WriteString(activityStyle.Render(fmt.Sprintf("Last=%s", lastActivity)))

	// Cooldown remaining if applicable
	if agent.CooldownRemaining > 0 {
		cooldownStyle := lipgloss.NewStyle().Foreground(t.Yellow)
		sb.WriteString(cooldownStyle.Render(fmt.Sprintf(" (%ds)", agent.CooldownRemaining)))
	}

	// Rate limit count if any
	if agent.RateLimitCount > 0 && agent.RateLimitStatus != robot.RateLimitOK {
		countStyle := lipgloss.NewStyle().Foreground(t.Yellow)
		sb.WriteString(countStyle.Render(fmt.Sprintf(" (%d limits)", agent.RateLimitCount)))
	}

	return layout.TruncateWidthDefault(sb.String(), max(1, width-2))
}

// shortAgentType returns a short agent type identifier
func (m *RateLimitPanel) shortAgentType(agentType string) string {
	switch robot.ResolveAgentType(agentType) {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	default:
		agentType = robot.ResolveAgentType(agentType)
		return agentType[:min(3, len(agentType))]
	}
}

// getOAuthStyle returns the style for OAuth status
func (m *RateLimitPanel) getOAuthStyle(status robot.OAuthStatus, t theme.Theme) lipgloss.Style {
	switch status {
	case robot.OAuthValid:
		return lipgloss.NewStyle().Foreground(t.Green)
	case robot.OAuthExpired:
		return lipgloss.NewStyle().Foreground(t.Yellow)
	case robot.OAuthError:
		return lipgloss.NewStyle().Foreground(t.Red)
	default:
		return lipgloss.NewStyle().Foreground(t.Subtext)
	}
}

// getOAuthIcon returns the icon for OAuth status
func (m *RateLimitPanel) getOAuthIcon(status robot.OAuthStatus) string {
	switch status {
	case robot.OAuthValid:
		return "ok"
	case robot.OAuthExpired:
		return "exp"
	case robot.OAuthError:
		return "err"
	default:
		return "?"
	}
}

// getRateLimitStyle returns the style for rate limit status
func (m *RateLimitPanel) getRateLimitStyle(status robot.RateLimitStatus, t theme.Theme) lipgloss.Style {
	switch status {
	case robot.RateLimitOK:
		return lipgloss.NewStyle().Foreground(t.Green)
	case robot.RateLimitWarning:
		return lipgloss.NewStyle().Foreground(t.Yellow)
	case robot.RateLimitLimited:
		return lipgloss.NewStyle().Foreground(t.Red)
	default:
		return lipgloss.NewStyle().Foreground(t.Subtext)
	}
}

// getRateLimitIcon returns the icon for rate limit status
func (m *RateLimitPanel) getRateLimitIcon(status robot.RateLimitStatus) string {
	switch status {
	case robot.RateLimitOK:
		return "OK"
	case robot.RateLimitWarning:
		return "WARN"
	case robot.RateLimitLimited:
		return "LIM"
	default:
		return "?"
	}
}

// formatLastActivity formats the last activity time
func (m *RateLimitPanel) formatLastActivity(sec int) string {
	if sec < 0 {
		return "?"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh", sec/3600)
}

// min() removed — use Go 1.25 builtin min()
