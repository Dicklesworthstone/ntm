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
	t := theme.Current()
	return []string{string(t.Blue), string(t.Lavender), string(t.Mauve)}
}

func gradientSecondary() []string {
	t := theme.Current()
	return []string{string(t.Mauve), string(t.Pink), string(t.Red)}
}

func gradientSuccess() []string {
	t := theme.Current()
	return []string{string(t.Teal), string(t.Green), string(t.Yellow)}
}

func gradientRainbow() []string {
	t := theme.Current()
	return []string{
		string(t.Red),
		string(t.Peach),
		string(t.Yellow),
		string(t.Green),
		string(t.Sky),
		string(t.Blue),
		string(t.Mauve),
	}
}

func gradientAgent(agent string) []string {
	t := theme.Current()
	switch agent {
	case "claude":
		return []string{string(t.Mauve), string(t.Lavender), string(t.Blue)}
	case "codex":
		return []string{string(t.Blue), string(t.Sapphire), string(t.Sky)}
	case "gemini":
		return []string{string(t.Yellow), string(t.Peach), string(t.Red)}
	default:
		return gradientPrimary()
	}
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

// RenderInlineLogo renders a small inline logo
func RenderInlineLogo() string {
	return styles.GradientText(LogoSmall, gradientPrimary()...)
}

// RenderSubtitle renders a styled subtitle
func RenderSubtitle(text string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Subtext).
		Italic(true).
		Render(text)
}

// RenderVersion renders a styled version string
func RenderVersion(version string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Overlay).
		Render("v" + version)
}

// RenderHeaderBar renders a full header bar with title
func RenderHeaderBar(title string, width int) string {
	// Gradient divider
	divider := styles.GradientDivider(width, gradientPrimary()...)

	// Centered title
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Current().Text)

	centeredTitle := styles.CenterText(titleStyle.Render(title), width)

	return divider + "\n" + centeredTitle + "\n" + divider
}

// RenderSection renders a section header
func RenderSection(title string, width int) string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Current().Primary)

	// Gradient line after title
	titleLen := lipgloss.Width(title) + 2
	remaining := width - titleLen
	if remaining < 0 {
		remaining = 0
	}

	line := styles.GradientText(strings.Repeat("─", remaining), gradientPrimary()...)

	return titleStyle.Render(title) + " " + line
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
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return string(t.Claude), ic.Claude
	case agent.AgentTypeCodex:
		return string(t.Codex), ic.Codex
	case agent.AgentTypeGemini:
		return string(t.Gemini), ic.Gemini
	case agent.AgentTypeCursor:
		return string(t.Cursor), ic.Cursor
	case agent.AgentTypeWindsurf:
		return string(t.Windsurf), ic.Windsurf
	case agent.AgentTypeAider:
		return string(t.Aider), ic.Aider
	case agent.AgentTypeOllama:
		return string(t.Ollama), ic.Ollama
	case agent.AgentTypeUser:
		return string(t.User), ic.User
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

// RenderStatusBadge renders a status badge
func RenderStatusBadge(status string) string {
	var bgColor string
	var icon string

	switch status {
	case "running", "active":
		bgColor = string(theme.Semantic().StatusSuccess)
		icon = "●"
	case "idle":
		bgColor = string(theme.Semantic().StatusIdle)
		icon = "○"
	case "error", "failed":
		bgColor = string(theme.Semantic().StatusError)
		icon = "✗"
	case "success", "done":
		bgColor = string(theme.Semantic().StatusSuccess)
		icon = "✓"
	default:
		bgColor = string(theme.Semantic().FgSecondary)
		icon = "•"
	}

	return lipgloss.NewStyle().
		Background(lipgloss.Color(bgColor)).
		Foreground(theme.Semantic().FgInverse).
		Padding(0, 1).
		Render(icon + " " + status)
}

// RenderKeyMap renders a keyboard shortcuts help section
func RenderKeyMap(keys map[string]string, width int) string {
	var lines []string

	keyStyle := lipgloss.NewStyle().
		Background(theme.Current().Surface1).
		Foreground(theme.Current().Text).
		Bold(true).
		Padding(0, 1)

	descStyle := lipgloss.NewStyle().
		Foreground(theme.Current().Subtext)

	for key, desc := range keys {
		lines = append(lines, keyStyle.Render(key)+" "+descStyle.Render(desc))
	}

	// Join with separator
	return strings.Join(lines, "  ")
}

// RenderFooter renders a styled footer
func RenderFooter(text string, width int) string {
	t := theme.Current()
	divider := styles.GradientDivider(width, string(t.Surface1), string(t.Surface0))

	footerStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Italic(true)

	return divider + "\n" + styles.CenterText(footerStyle.Render(text), width)
}

// RenderHint renders a dimmed hint text
func RenderHint(text string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Overlay).
		Italic(true).
		Render(text)
}

// RenderHighlight renders highlighted text
func RenderHighlight(text string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Rosewater).
		Bold(true).
		Render(text)
}

// RenderCommand renders a command with styling
func RenderCommand(cmd string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Primary).
		Bold(true).
		Render(cmd)
}

// RenderArg renders an argument with styling
func RenderArg(arg string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Green).
		Render("<" + arg + ">")
}

// RenderFlag renders a flag with styling
func RenderFlag(flag string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Yellow).
		Render(flag)
}

// RenderExample renders an example with styling
func RenderExample(example string) string {
	return lipgloss.NewStyle().
		Foreground(theme.Current().Peach).
		Italic(true).
		Render(example)
}
