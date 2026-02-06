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

// MedalColors returns the current theme's medal palette.
func MedalColors() MedalPalette {
	t := theme.Current()
	return MedalPalette{
		Gold:   t.Yellow,
		Silver: t.Surface2,
		Bronze: t.Maroon,
	}
}

// MiniBarPalette controls the colors and glyphs for MiniBar rendering.
type MiniBarPalette struct {
	Low        lipgloss.Color // value < 0.60
	Mid        lipgloss.Color // 0.40–0.59 (legacy mid-low band)
	MidHigh    lipgloss.Color // 0.60–0.79 (optional; falls back to Mid)
	High       lipgloss.Color // >= 0.80
	Empty      lipgloss.Color
	FilledChar string
	EmptyChar  string
}

// DefaultMiniBarPalette returns a sensible palette derived from the current theme.
func DefaultMiniBarPalette() MiniBarPalette {
	t := theme.Current()
	return MiniBarPalette{
		Low:        t.Green,
		Mid:        t.Blue,
		MidHigh:    t.Yellow,
		High:       t.Red,
		Empty:      t.Surface1,
		FilledChar: "█",
		EmptyChar:  "░",
	}
}

// DefaultBadgeOptions returns sensible defaults for badge rendering
func DefaultBadgeOptions() BadgeOptions {
	return BadgeOptions{
		Style:    BadgeStyleDefault,
		Bold:     true,
		ShowIcon: true,
	}
}

// AgentBadge renders a badge for an agent type using theme colors.
// agentType can be: "claude", "cc", "codex", "cod", "gemini", "gmi", "user"
func AgentBadge(agentType string, opts ...BadgeOptions) string {
	t := theme.Current()
	ic := icons.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	var bgColor lipgloss.Color
	var icon string
	var label string

	switch strings.ToLower(agentType) {
	case "claude", "cc":
		bgColor = t.Claude
		icon = ic.Claude
		label = "claude"
	case "codex", "cod":
		bgColor = t.Codex
		icon = ic.Codex
		label = "codex"
	case "gemini", "gmi":
		bgColor = t.Gemini
		icon = ic.Gemini
		label = "gemini"
	case "user":
		bgColor = t.User
		icon = ic.User
		label = "user"
	default:
		bgColor = t.Overlay
		icon = "?"
		label = agentType
	}

	text := label
	if opt.ShowIcon {
		text = icon + " " + label
	}

	return renderBadge(text, bgColor, t.Base, opt)
}

// AgentBadgeWithCount renders an agent badge with a count (e.g., "󰗣 3")
func AgentBadgeWithCount(agentType string, count int, opts ...BadgeOptions) string {
	t := theme.Current()
	ic := icons.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	var bgColor lipgloss.Color
	var icon string

	switch strings.ToLower(agentType) {
	case "claude", "cc":
		bgColor = t.Claude
		icon = ic.Claude
	case "codex", "cod":
		bgColor = t.Codex
		icon = ic.Codex
	case "gemini", "gmi":
		bgColor = t.Gemini
		icon = ic.Gemini
	case "user":
		bgColor = t.User
		icon = ic.User
	default:
		bgColor = t.Overlay
		icon = "?"
	}

	text := fmt.Sprintf("%s %d", icon, count)
	return renderBadge(text, bgColor, t.Base, opt)
}

// StatusBadge renders a status indicator badge using theme colors.
// status can be: "success", "ok", "running", "active", "idle", "warning",
// "error", "failed", "pending", "disabled"
func StatusBadge(status string, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	var bgColor lipgloss.Color
	var icon string
	var label string

	switch strings.ToLower(status) {
	case "success", "ok", "done", "complete", "completed":
		bgColor = t.Success
		icon = "✓"
		label = "success"
	case "running", "active", "working":
		bgColor = t.Green
		icon = "●"
		label = "running"
	case "idle", "waiting":
		bgColor = t.Yellow
		icon = "○"
		label = "idle"
	case "warning", "warn", "attention":
		bgColor = t.Warning
		icon = "⚠"
		label = "warning"
	case "error", "failed", "failure":
		bgColor = t.Error
		icon = "✗"
		label = "error"
	case "pending", "in_progress":
		bgColor = t.Blue
		icon = "◐"
		label = "pending"
	case "disabled", "unavailable":
		bgColor = t.Overlay
		icon = "◌"
		label = "disabled"
	case "blocked":
		bgColor = t.Red
		icon = "⊘"
		label = "blocked"
	default:
		bgColor = t.Surface1
		icon = "•"
		label = status
	}

	text := label
	if opt.ShowIcon {
		text = icon + " " + label
	}

	return renderBadge(text, bgColor, t.Base, opt)
}

// StatusBadgeIcon renders just a status icon as a small badge
func StatusBadgeIcon(status string) string {
	t := theme.Current()

	var bgColor lipgloss.Color
	var icon string

	switch strings.ToLower(status) {
	case "success", "ok", "done":
		bgColor = t.Success
		icon = "✓"
	case "running", "active":
		bgColor = t.Green
		icon = "●"
	case "idle", "waiting":
		bgColor = t.Yellow
		icon = "○"
	case "warning", "warn":
		bgColor = t.Warning
		icon = "⚠"
	case "error", "failed":
		bgColor = t.Error
		icon = "✗"
	case "pending":
		bgColor = t.Blue
		icon = "◐"
	case "blocked":
		bgColor = t.Red
		icon = "⊘"
	default:
		bgColor = t.Overlay
		icon = "•"
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(t.Base).
		Bold(true).
		Width(3).
		Align(lipgloss.Center).
		Render(icon)
}

// RankBadge renders a compact numeric rank badge (1-based) with medal-like colors.
// 1 → gold, 2 → silver, 3 → bronze, others → neutral surface.
// Fixed width to avoid layout jitter in dense tables.
func RankBadge(rank int, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}
	if rank <= 0 {
		return ""
	}

	medals := MedalColors()
	bg := t.Surface1
	switch rank {
	case 1:
		bg = medals.Gold
	case 2:
		bg = medals.Silver
	case 3:
		bg = medals.Bronze
	}

	label := fmt.Sprintf("#%d", rank)
	content := label
	if opt.ShowIcon {
		content = "★ " + label
	}

	return lipgloss.NewStyle().
		Background(bg).
		Foreground(t.Base).
		Bold(opt.Bold).
		Width(5).
		Align(lipgloss.Center).
		Render(content)
}

// PriorityBadge renders a priority indicator badge (P0-P4)
func PriorityBadge(priority int, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		// Priority badges don't show icons by default (just "P0", "P1", etc.)
		opt.ShowIcon = false
	}

	var bgColor lipgloss.Color
	label := fmt.Sprintf("P%d", priority)

	switch priority {
	case 0:
		bgColor = t.Red // Critical
	case 1:
		bgColor = t.Peach // High
	case 2:
		bgColor = t.Yellow // Medium
	case 3:
		bgColor = t.Blue // Low
	case 4:
		bgColor = t.Overlay // Backlog
	default:
		bgColor = t.Surface1
	}

	return renderBadge(label, bgColor, t.Base, opt)
}

// CountBadge renders a simple numeric count badge
func CountBadge(count int, bgColor, fgColor lipgloss.Color) string {
	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%d", count))
}

// TextBadge renders a simple text badge with custom colors
func TextBadge(text string, bgColor, fgColor lipgloss.Color, opts ...BadgeOptions) string {
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}
	return renderBadge(text, bgColor, fgColor, opt)
}

// HealthBadge renders a health status badge (for bv drift status)
func HealthBadge(status string, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	var bgColor lipgloss.Color
	var icon string
	var label string

	switch strings.ToLower(status) {
	case "ok", "healthy":
		bgColor = t.Green
		icon = "✓"
		label = "healthy"
	case "warning", "drift":
		bgColor = t.Yellow
		icon = "⚠"
		label = "drift"
	case "critical":
		bgColor = t.Red
		icon = "✗"
		label = "critical"
	case "no_baseline":
		bgColor = t.Surface1
		icon = "?"
		label = "no baseline"
	case "unavailable":
		bgColor = t.Overlay
		icon = "—"
		label = "n/a"
	default:
		bgColor = t.Surface1
		icon = "?"
		label = status
	}

	text := label
	if opt.ShowIcon {
		text = icon + " " + label
	}

	return renderBadge(text, bgColor, t.Base, opt)
}

// IssueTypeBadge renders a badge for issue types (epic, feature, task, bug, chore)
func IssueTypeBadge(issueType string, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	var bgColor lipgloss.Color
	var icon string

	switch strings.ToLower(issueType) {
	case "epic":
		bgColor = t.Mauve
		icon = "◆"
	case "feature":
		bgColor = t.Blue
		icon = "★"
	case "task":
		bgColor = t.Green
		icon = "●"
	case "bug":
		bgColor = t.Red
		icon = "◉"
	case "chore":
		bgColor = t.Overlay
		icon = "○"
	default:
		bgColor = t.Surface1
		icon = "•"
	}

	text := issueType
	if opt.ShowIcon {
		text = icon + " " + issueType
	}

	return renderBadge(text, bgColor, t.Base, opt)
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

// AlertSeverityBadge renders a badge for alert severity levels.
// severity: critical|high|medium|low|info (case-insensitive)
func AlertSeverityBadge(severity string, opts ...BadgeOptions) string {
	t := theme.Current()
	opt := DefaultBadgeOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	lower := strings.ToLower(severity)

	var (
		bgColor lipgloss.Color
		icon    string
		label   string
	)

	switch lower {
	case "critical", "crit", "sev0", "p0":
		bgColor = t.Error
		icon = "‼"
		label = "critical"
	case "high", "sev1", "p1":
		bgColor = t.Warning
		icon = "⚠"
		label = "high"
	case "medium", "med", "sev2", "p2":
		bgColor = t.Yellow
		icon = "▲"
		label = "medium"
	case "low", "sev3", "p3":
		bgColor = t.Blue
		icon = "▼"
		label = "low"
	default:
		bgColor = t.Surface1
		icon = "ℹ"
		label = "info"
	}

	text := label
	if opt.ShowIcon && icon != "" {
		text = icon + " " + label
	}

	return renderBadge(text, bgColor, t.Base, opt)
}

// MiniBar renders a compact, fixed-width bar (typically 4–8 chars) for inline metrics.
// Value is clamped to [0,1]. Palette can override default colors and glyphs.
func MiniBar(value float64, width int, palettes ...MiniBarPalette) string {
	if width < 1 {
		return ""
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}

	palette := DefaultMiniBarPalette()
	if len(palettes) > 0 {
		palette = palettes[0]
	}

	filled := int(value * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	var barColor lipgloss.Color
	switch {
	case value >= 0.80:
		barColor = palette.High
	case value >= 0.60:
		if palette.MidHigh != "" {
			barColor = palette.MidHigh
		} else {
			barColor = palette.Mid
		}
	case value >= 0.40:
		barColor = palette.Mid
	default:
		barColor = palette.Low
	}

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(palette.Empty)

	return filledStyle.Render(strings.Repeat(palette.FilledChar, filled)) +
		emptyStyle.Render(strings.Repeat(palette.EmptyChar, empty))
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

// BadgeBar renders badges separated by a consistent spacer
func BadgeBar(badges ...string) string {
	return strings.Join(badges, "  ")
}
