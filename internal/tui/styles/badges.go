// Package styles provides badge rendering functions for consistent UI elements.
package styles

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// BadgeStyle defines the visual style of a badge
type BadgeStyle int

const (
	// BadgeStyleDefault is a standard badge with padding
	BadgeStyleDefault BadgeStyle = iota
	// BadgeStyleCompact is a minimal badge without padding
	BadgeStyleCompact
	// BadgeStylePill is a rounded pill-style badge
	BadgeStylePill
)

// Badge width constants for consistent alignment
const (
	// ModelBadgeWidth is the fixed width for model badges (e.g., "opus", "sonnet")
	ModelBadgeWidth = 8
	// PriorityBadgeWidth is the fixed width for priority badges (e.g., "P0", "P1")
	PriorityBadgeWidth = 3
	// StatusBadgeWidth is the fixed width for status badges (e.g., "◐ working")
	StatusBadgeWidth = 10
)

// BadgeOptions configures badge rendering
type BadgeOptions struct {
	Style      BadgeStyle
	Bold       bool
	ShowIcon   bool
	FixedWidth int // If > 0, badge is truncated/padded to this width
}

// MedalPalette defines standard medal colors for rank badges.
type MedalPalette struct {
	Gold   lipgloss.Color
	Silver lipgloss.Color
	Bronze lipgloss.Color
}

// DefaultBadgeOptions returns sensible defaults for badge rendering
func DefaultBadgeOptions() BadgeOptions {
	return BadgeOptions{
		Style:    BadgeStyleDefault,
		Bold:     true,
		ShowIcon: true,
	}
}

// TextBadge renders a simple text badge with custom colors
func TextBadge(text string, bgColor, fgColor lipgloss.Color, opts ...BadgeOptions) string {
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}
	return renderBadge(text, bgColor, fgColor, opt)
}

// ModelBadge renders a badge for a model/variant (Claude/OpenAI/Gemini).
// Examples:
//
//	"claude-3-opus"   -> "opus" (Claude color)
//	"gpt-4o-mini"     -> "4o"   (OpenAI color)
//	"gemini-1.5-pro"  -> "g1.5" (Gemini color)
func ModelBadge(model string, opts ...BadgeOptions) string {
	t := theme.Current()
	ic := icons.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	lower := strings.ToLower(model)

	var (
		bgColor lipgloss.Color
		icon    string
		label   string
	)

	switch {
	case strings.Contains(lower, "claude"):
		bgColor = t.Claude
		icon = ic.Claude
		switch {
		case strings.Contains(lower, "opus"):
			label = "opus"
		case strings.Contains(lower, "sonnet"):
			label = "sonnet"
		case strings.Contains(lower, "haiku"):
			label = "haiku"
		default:
			label = "claude"
		}
	case strings.Contains(lower, "gpt"):
		bgColor = t.Codex
		icon = ic.Codex
		switch {
		case strings.Contains(lower, "4.1"):
			label = "4.1"
		case strings.Contains(lower, "4o"):
			label = "4o"
		case strings.Contains(lower, "4"):
			label = "4"
		case strings.Contains(lower, "3.5"):
			label = "3.5"
		default:
			label = "gpt"
		}
	case strings.Contains(lower, "gemini"):
		bgColor = t.Gemini
		icon = ic.Gemini
		switch {
		case strings.Contains(lower, "1.5"):
			label = "g1.5"
		case strings.Contains(lower, "1.0"):
			label = "g1.0"
		default:
			label = "gemini"
		}
	default:
		bgColor = t.Overlay
		icon = "⋯"
		label = model
	}

	text := label
	if opt.ShowIcon && icon != "" {
		text = icon + " " + label
	}

	return renderBadge(text, bgColor, t.Base, opt)
}

// TokenVelocityBadge renders a badge for token velocity (tokens per minute).
// Example output: "⚡ 2400 tpm"
func TokenVelocityBadge(tokensPerMinute float64, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	value := tokensPerMinute
	if value < 0 {
		value = 0
	}

	var bgColor lipgloss.Color
	switch {
	case value >= 8000:
		bgColor = t.Red
	case value >= 4000:
		bgColor = t.Yellow
	default:
		bgColor = t.Green
	}

	label := fmt.Sprintf("%.0f tpm", value)
	if opt.ShowIcon {
		label = "⚡ " + label
	}

	return renderBadge(label, bgColor, t.Base, opt)
}

// TokensPerSecondBadge renders a badge for an instantaneous token rate estimate.
// Example output: "⚡ 42.1 tok/s"
func TokensPerSecondBadge(tokensPerSecond float64, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	value := tokensPerSecond
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		value = 0
	}

	var bgColor lipgloss.Color
	switch {
	case value >= 80:
		bgColor = t.Red
	case value >= 40:
		bgColor = t.Yellow
	default:
		bgColor = t.Green
	}

	label := fmt.Sprintf("%.1f tok/s", value)
	if opt.ShowIcon {
		label = "⚡ " + label
	}

	return renderBadge(label, bgColor, t.Base, opt)
}

// MemoryUsageBadge renders a badge for memory usage (VRAM/CPU).
// Example output: "🧠 12.0 GB"
func MemoryUsageBadge(bytes int64, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	value := bytes
	if value < 0 {
		value = 0
	}

	var bgColor lipgloss.Color
	switch {
	case value >= 16*1024*1024*1024:
		bgColor = t.Red
	case value >= 8*1024*1024*1024:
		bgColor = t.Yellow
	default:
		bgColor = t.Green
	}

	label := util.FormatBytes(value)
	if opt.ShowIcon {
		label = "🧠 " + label
	}

	return renderBadge(label, bgColor, t.Base, opt)
}

// renderBadge is the internal badge rendering function
func renderBadge(text string, bgColor, fgColor lipgloss.Color, opt BadgeOptions) string {
	style := lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor)

	if opt.Bold {
		style = style.Bold(true)
	}

	switch opt.Style {
	case BadgeStyleCompact:
		// No padding
	case BadgeStylePill:
		style = style.Padding(0, 2)
	default:
		style = style.Padding(0, 1)
	}

	// Handle fixed width: truncate if needed and set width for padding
	if opt.FixedWidth > 0 {
		text = truncateBadgeText(text, opt.FixedWidth)
		style = style.Width(opt.FixedWidth)
	}

	return style.Render(text)
}

// truncateBadgeText truncates text to maxWidth terminal columns, adding ellipsis if needed.
// Uses lipgloss.Width() to properly handle ANSI codes and double-width runes.
func truncateBadgeText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}

	ellipsisWidth := lipgloss.Width("…")
	targetWidth := maxWidth - ellipsisWidth
	if targetWidth <= 0 {
		return "…"
	}

	return truncateToWidth(s, targetWidth) + "…"
}

func truncateToWidth(s string, maxWidth int) string {
	runes := []rune(s)
	for len(runes) > 0 {
		candidate := string(runes)
		if lipgloss.Width(candidate) <= maxWidth {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return ""
}

// BadgeGroup renders multiple badges in a horizontal group
func BadgeGroup(badges ...string) string {
	return strings.Join(badges, " ")
}
