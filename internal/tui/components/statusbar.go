// Package components provides shared TUI building blocks.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// StatusBarOptions configures status bar rendering.
type StatusBarOptions struct {
	Width            int
	Session          string
	ClaudeCount      int
	CodexCount       int
	GeminiCount      int
	GrokCount        int
	OmpCount         int
	AntigravityCount int
	UserCount        int
	FocusedPanel     string
	LayoutTier       string // "Narrow", "Split", "Wide", "Ultra", "Mega"
	Paused           bool
	CurrentVelocity  float64
	VelocityHistory  []float64
}

// RenderStatusBar renders a three-section status bar: left | center | right.
// Uses the full terminal width with a Surface0 background and Surface1 top border.
func RenderStatusBar(opts StatusBarOptions) string {
	t := theme.Current()
	if opts.Width <= 0 {
		return ""
	}

	// Top border — subtle line using Surface1
	border := lipgloss.NewStyle().
		Foreground(t.Surface1).
		Width(opts.Width).
		Render(strings.Repeat("─", opts.Width))

	// Base style for all sections
	base := lipgloss.NewStyle().
		Background(t.Surface0)

	// ── Left section: session name + agent count badges ──────────
	left := renderStatusLeft(t, base, opts)

	// ── Right section: layout tier + tick indicator ──────────────
	right := renderStatusRight(t, base, opts)

	// Calculate widths for three-column layout
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	centerW := opts.Width - leftW - rightW
	if centerW < 0 {
		centerW = 0
	}

	// ── Center section: focused panel name + aggregate trend ─────
	center := renderStatusCenter(t, base, opts, centerW)

	// Pad center to fill remaining space, keeping it visually centered
	centeredSection := base.Width(centerW).Align(lipgloss.Center).Render(
		lipgloss.PlaceHorizontal(centerW, lipgloss.Center, center,
			lipgloss.WithWhitespaceBackground(t.Surface0)),
	)

	bar := lipgloss.JoinHorizontal(lipgloss.Top, left, centeredSection, right)
	if lipgloss.Width(bar) > opts.Width {
		bar = styles.Truncate(bar, opts.Width)
	}

	return border + "\n" + bar
}

// renderStatusLeft builds the left section: session name + agent badges.
func renderStatusLeft(t theme.Theme, base lipgloss.Style, opts StatusBarOptions) string {
	var parts []string

	// Session name
	session := opts.Session
	if session == "" {
		session = "—"
	}
	sessionStyle := base.
		Foreground(t.Text).
		Bold(true).
		Padding(0, 1)
	parts = append(parts, sessionStyle.Render(session))

	// Agent count badges — only show when count > 0
	type agentBadge struct {
		label string
		color lipgloss.Color
		count int
	}
	badges := []agentBadge{
		{"CC", t.Claude, opts.ClaudeCount},
		{"COD", t.Codex, opts.CodexCount},
		{"GMI", t.Gemini, opts.GeminiCount},
		{"GRK", t.Pink, opts.GrokCount},
		{"OMP", t.Sky, opts.OmpCount},
		{"AGY", t.Lavender, opts.AntigravityCount},
	}

	for _, ab := range badges {
		if ab.count <= 0 {
			continue
		}
		badge := lipgloss.NewStyle().
			Background(ab.color).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s:%d", ab.label, ab.count))
		parts = append(parts, badge)
	}

	// User count badge (distinct color)
	if opts.UserCount > 0 {
		userBadge := lipgloss.NewStyle().
			Background(t.User).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("USR:%d", opts.UserCount))
		parts = append(parts, userBadge)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// renderStatusCenter builds the center section: focused panel name + velocity trend.
func renderStatusCenter(t theme.Theme, base lipgloss.Style, opts StatusBarOptions, width int) string {
	if width <= 0 {
		return ""
	}

	focusedPanel := ""
	if opts.FocusedPanel != "" {
		focusedPanel = base.
			Foreground(t.Primary).
			Bold(true).
			Render(opts.FocusedPanel)
	}

	if len(opts.VelocityHistory) == 0 {
		if focusedPanel == "" {
			return ""
		}
		return styles.Truncate(focusedPanel, width)
	}

	currentVelocity := opts.CurrentVelocity
	if currentVelocity <= 0 {
		currentVelocity = opts.VelocityHistory[len(opts.VelocityHistory)-1]
	}

	if focusedPanel == "" {
		return SparklineWithLabel("tpm", opts.VelocityHistory, width, fmt.Sprintf("%.0f", currentVelocity))
	}

	if width < 24 {
		return styles.Truncate(focusedPanel, width)
	}

	separator := base.Foreground(t.Overlay).Render(" · ")
	remainingWidth := width - lipgloss.Width(focusedPanel) - lipgloss.Width(separator)
	if remainingWidth < 12 {
		return styles.Truncate(focusedPanel, width)
	}

	trend := SparklineWithLabel("tpm", opts.VelocityHistory, remainingWidth, fmt.Sprintf("%.0f", currentVelocity))
	return lipgloss.JoinHorizontal(lipgloss.Top, focusedPanel, separator, trend)
}

// renderStatusRight builds the right section: layout tier + tick/paused indicator.
func renderStatusRight(t theme.Theme, base lipgloss.Style, opts StatusBarOptions) string {
	var parts []string

	// Layout tier badge
	tier := opts.LayoutTier
	if tier == "" {
		tier = "—"
	}
	tierColor := layoutTierColor(t, tier)
	tierBadge := lipgloss.NewStyle().
		Background(tierColor).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(tier)
	parts = append(parts, tierBadge)

	// Tick / paused indicator
	var indicator string
	if opts.Paused {
		indicator = lipgloss.NewStyle().
			Background(t.Warning).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("⏸")
	} else {
		indicator = base.
			Foreground(t.Success).
			Padding(0, 1).
			Render("●")
	}
	parts = append(parts, indicator)

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// layoutTierColor returns a color based on the layout tier.
func layoutTierColor(t theme.Theme, tier string) lipgloss.Color {
	switch tier {
	case "Narrow":
		return t.Overlay
	case "Split":
		return t.Sapphire
	case "Wide":
		return t.Blue
	case "Ultra":
		return t.Mauve
	case "Mega":
		return t.Flamingo
	default:
		return t.Surface2
	}
}
