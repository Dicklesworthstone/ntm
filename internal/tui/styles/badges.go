// Package styles provides badge rendering functions for consistent UI elements.
package styles

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/lipgloss"
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

// BadgeOptions configures badge rendering
type BadgeOptions struct {
	Style    BadgeStyle
	Bold     bool
	ShowIcon bool
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
		Padding(0, 1).
		Render(icon)
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

	return style.Render(text)
}

// BadgeGroup renders multiple badges in a horizontal group
func BadgeGroup(badges ...string) string {
	return strings.Join(badges, " ")
}

// BadgeBar renders badges separated by a consistent spacer
func BadgeBar(badges ...string) string {
	return strings.Join(badges, "  ")
}
