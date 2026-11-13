package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ASCII art logos for NTM
var (
	// Large banner logo
	LogoLarge = []string{
		"███╗   ██╗████████╗███╗   ███╗",
		"████╗  ██║╚══██╔══╝████╗ ████║",
		"██╔██╗ ██║   ██║   ██╔████╔██║",
		"██║╚██╗██║   ██║   ██║╚██╔╝██║",
		"██║ ╚████║   ██║   ██║ ╚═╝ ██║",
		"╚═╝  ╚═══╝   ╚═╝   ╚═╝     ╚═╝",
	}

	// Medium banner logo
	LogoMedium = []string{
		"╔╗╔╔╦╗╔╦╗",
		"║║║ ║ ║║║",
		"╝╚╝ ╩ ╩ ╩",
	}

	// Small inline logo
	LogoSmall = "⟦NTM⟧"

	// Icon variants
	LogoIcon      = "󰆍" // Terminal icon (Nerd Font)
	LogoIconPlain = "▣" // Plain Unicode fallback
)

func gradientPrimary() []string {
	stops := theme.Current().Gradient(5)
	colors := make([]string, len(stops))
	for i, c := range stops {
		colors[i] = string(c)
	}
	return colors
}

// RenderBanner renders the large logo with gradient
func RenderBanner(animated bool, tick int) string {
	var lines []string

	for _, line := range LogoLarge {
		if animated {
			lines = append(lines, styles.Shimmer(line, tick, gradientPrimary()...))
		} else {
			lines = append(lines, styles.GradientText(line, gradientPrimary()...))
		}
	}

	return strings.Join(lines, "\n")
}

// RenderBannerMedium renders the medium logo with gradient
func RenderBannerMedium(animated bool, tick int) string {
	var lines []string

	for _, line := range LogoMedium {
		if animated {
			lines = append(lines, styles.Shimmer(line, tick, gradientPrimary()...))
		} else {
			lines = append(lines, styles.GradientText(line, gradientPrimary()...))
		}
	}

	return strings.Join(lines, "\n")
}

// RenderSubtitle renders a styled subtitle
func RenderSubtitle(text string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Subtext).
		Italic(true).
		Render(text)
}

// RenderAgentBadge renders a colored badge for an agent type
func RenderAgentBadge(agentType string) string {
	t := theme.Current()
	ic := icons.Current()

	bgColor, icon := renderAgentBadgeStyle(agentType, t, ic)
	fgColor := string(t.Crust)
	label := strings.ToUpper(renderAgentBadgeLabel(agentType))

	return lipgloss.NewStyle().
		Background(lipgloss.Color(bgColor)).
		Foreground(lipgloss.Color(fgColor)).
		Bold(true).
		Padding(0, 1).
		Render(strings.TrimSpace(icon + " " + label))
}

func renderAgentBadgeStyle(agentType string, t theme.Theme, ic icons.IconSet) (string, string) {
	sem := t.Semantic()
	color := sem.AgentColor(agentType)
	if !agent.AgentType(agentType).IsValid() {
		color = t.Green
	}

	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return string(color), ic.Claude
	case agent.AgentTypeCodex:
		return string(color), ic.Codex
	case agent.AgentTypeGemini:
		return string(color), ic.Gemini
	case agent.AgentTypeGrok:
		return string(color), ic.Robot
	case agent.AgentTypeOmp:
		return string(color), ic.Robot
	case agent.AgentTypeAntigravity:
		return string(color), ic.Gemini
	case agent.AgentTypeCursor:
		return string(color), ic.Cursor
	case agent.AgentTypeWindsurf:
		return string(color), ic.Windsurf
	case agent.AgentTypeAider:
		return string(color), ic.Aider
	case agent.AgentTypeOpencode:
		return string(color), ic.Robot
	case agent.AgentTypeOllama:
		return string(color), ic.Ollama
	case agent.AgentTypeUser:
		return string(color), ic.User
	default:
		return string(t.Green), ""
	}
}

func renderAgentBadgeLabel(agentType string) string {
	canonical := agent.AgentType(agentType).Canonical()
	if canonical.IsValid() || canonical == agent.AgentTypeUnknown {
		if label := strings.TrimSpace(canonical.ProfileName()); label != "" {
			return label
		}
	}

	label := strings.TrimSpace(agentType)
	if label == "" {
		return "unknown"
	}
	return label
}
