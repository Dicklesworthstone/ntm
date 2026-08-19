package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

type StateKind int

const (
	StateEmpty StateKind = iota
	StateLoading
	StateError
	StateRetrying // Retry in progress with attempt tracking
)

// EmptyStateIcon represents contextual icons for empty states.
type EmptyStateIcon string

const (
	// IconWaiting indicates data not yet available (◎)
	IconWaiting EmptyStateIcon = "waiting"
	// IconEmpty indicates checked but nothing found (○)
	IconEmpty EmptyStateIcon = "empty"
	// IconExternal indicates needs external action (◇)
	IconExternal EmptyStateIcon = "external"
	// IconSuccess indicates empty is good state (✓)
	IconSuccess EmptyStateIcon = "success"
	// IconUnknown indicates couldn't determine (?)
	IconUnknown EmptyStateIcon = "unknown"
)

// EmptyStateOptions configures enhanced empty state rendering.
type EmptyStateOptions struct {
	Icon        EmptyStateIcon // Contextual icon type
	Title       string         // Primary message (required)
	Description string         // Explanatory text (optional)
	Action      string         // Suggested action (optional)
	Width       int            // Available width
	Centered    bool           // Center in container (default: true)
}

// resolveEmptyIcon returns the appropriate icon string for an EmptyStateIcon.
func resolveEmptyIcon(icon EmptyStateIcon) string {
	ic := icons.Current()
	switch icon {
	case IconWaiting:
		return ic.Target // ◎
	case IconEmpty:
		return ic.Circle // ○
	case IconExternal:
		return ic.Session // ◆ (diamond-like)
	case IconSuccess:
		return ic.Check // ✓
	case IconUnknown:
		return ic.Question // ?
	default:
		return ic.Info // fallback
	}
}

// RenderEmptyState renders an enhanced multi-line empty state.
// Format:
//
//	       ◎
//	  No metrics yet
//
//	Data will appear when
//	 agents start working
func RenderEmptyState(opts EmptyStateOptions) string {
	t := theme.Current()

	// Resolve icon
	icon := resolveEmptyIcon(opts.Icon)

	// Styles
	iconStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	titleStyle := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	actionStyle := lipgloss.NewStyle().Foreground(t.Blue).Italic(true)

	// Special styling for success state
	if opts.Icon == IconSuccess {
		iconStyle = iconStyle.Foreground(t.Green)
		titleStyle = titleStyle.Foreground(t.Green)
	}

	var lines []string

	// Icon line
	lines = append(lines, iconStyle.Render(icon))

	// Title line
	title := opts.Title
	if title == "" {
		title = "Nothing to show"
	}
	lines = append(lines, titleStyle.Render(title))

	// Description line (if provided)
	if opts.Description != "" {
		lines = append(lines, "") // blank line before description
		// Word wrap description if needed
		desc := opts.Description
		if opts.Width > 0 && lipgloss.Width(desc) > opts.Width-4 {
			desc = layout.TruncateWidthDefault(desc, opts.Width-4)
		}
		lines = append(lines, descStyle.Render(desc))
	}

	// Action line (if provided)
	if opts.Action != "" {
		action := opts.Action
		if opts.Width > 0 && lipgloss.Width(action) > opts.Width-4 {
			action = layout.TruncateWidthDefault(action, opts.Width-4)
		}
		lines = append(lines, actionStyle.Render(action))
	}

	content := strings.Join(lines, "\n")

	// Center if requested (default behavior)
	centered := opts.Centered
	if !centered && opts.Width > 0 {
		// Left-aligned with some padding
		return lipgloss.NewStyle().PaddingLeft(2).Render(content)
	}

	if opts.Width > 0 {
		return lipgloss.NewStyle().
			Width(opts.Width).
			Align(lipgloss.Center).
			Render(content)
	}

	return content
}

// ErrorStateOptions configures enhanced error state rendering.
type ErrorStateOptions struct {
	Title       string // Primary error message (required)
	Description string // Detail text (optional)
	Width       int    // Available width
}

// RenderErrorState renders a styled multi-line error state.
func RenderErrorState(opts ErrorStateOptions) string {
	t := theme.Current()
	ic := icons.Current()

	icon := strings.TrimSpace(ic.Warning)
	if icon == "" {
		icon = "!"
	}

	iconStyle := lipgloss.NewStyle().Foreground(t.Red)
	titleStyle := lipgloss.NewStyle().Foreground(t.Red).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)

	var lines []string

	lines = append(lines, iconStyle.Render(icon))

	title := opts.Title
	if title == "" {
		title = "Something went wrong"
	}
	lines = append(lines, titleStyle.Render(title))

	if opts.Description != "" {
		lines = append(lines, "")
		desc := opts.Description
		if opts.Width > 0 && lipgloss.Width(desc) > opts.Width-4 {
			desc = layout.TruncateWidthDefault(desc, opts.Width-4)
		}
		lines = append(lines, descStyle.Render(desc))
	}

	content := strings.Join(lines, "\n")

	if opts.Width > 0 {
		return lipgloss.NewStyle().
			Width(opts.Width).
			Align(lipgloss.Center).
			Render(content)
	}

	return content
}

type StateOptions struct {
	Kind        StateKind
	Icon        string
	Message     string
	Hint        string
	Width       int
	Align       lipgloss.Position
	Attempt     int // Current retry attempt (for StateRetrying)
	MaxAttempts int // Max attempts (0 = unlimited)
}

func RenderState(opts StateOptions) string {
	t := theme.Current()
	ic := icons.Current()

	align := opts.Align
	indent := "  "
	if align == lipgloss.Center {
		indent = ""
	}

	icon := strings.TrimSpace(opts.Icon)
	lineStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)

	message := strings.TrimSpace(opts.Message)
	hint := strings.TrimSpace(opts.Hint)

	switch opts.Kind {
	case StateLoading:
		lineStyle = lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
		if message == "" {
			message = "Loading…"
		}
		if icon == "" {
			icon = strings.TrimSpace(ic.Gear)
			if icon == "" {
				icon = "…"
			}
		}
	case StateError:
		lineStyle = lipgloss.NewStyle().Foreground(t.Red).Italic(true)
		hintStyle = lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		if message == "" {
			message = "Something went wrong"
		}
		if icon == "" {
			icon = strings.TrimSpace(ic.Warning)
			if icon == "" {
				icon = "!"
			}
		}
	case StateRetrying:
		lineStyle = lipgloss.NewStyle().Foreground(t.Yellow).Italic(true)
		hintStyle = lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		if message == "" {
			message = "Retrying…"
		}
		if icon == "" {
			icon = strings.TrimSpace(ic.Gear)
			if icon == "" {
				icon = "↻"
			}
		}
		// Build attempt info as hint if not provided
		if hint == "" && opts.Attempt > 0 {
			if opts.MaxAttempts > 0 {
				hint = fmt.Sprintf("Attempt %d of %d", opts.Attempt, opts.MaxAttempts)
			} else {
				hint = fmt.Sprintf("Attempt %d", opts.Attempt)
			}
		}
	default:
		if message == "" {
			message = "Nothing to show"
		}
		if icon == "" {
			icon = strings.TrimSpace(ic.Info)
			if icon == "" {
				icon = "i"
			}
		}
	}

	width := opts.Width
	if width < 0 {
		width = 0
	}

	prefix := indent + icon
	if icon != "" {
		prefix += " "
	}

	available := width
	if available > 0 {
		available -= lipgloss.Width(prefix)
		if available < 0 {
			available = 0
		}
	}

	if available > 0 {
		message = layout.TruncateWidthDefault(message, available)
	}

	lines := []string{lineStyle.Render(prefix + message)}

	if hint != "" {
		hintPrefix := indent
		hAvailable := width
		if hAvailable > 0 {
			hAvailable -= lipgloss.Width(hintPrefix)
			if hAvailable < 0 {
				hAvailable = 0
			}
		}
		if hAvailable > 0 {
			hint = layout.TruncateWidthDefault(hint, hAvailable)
		}
		lines = append(lines, hintStyle.Render(hintPrefix+hint))
	}

	rendered := strings.Join(lines, "\n")
	if width > 0 && (align == lipgloss.Center || align == lipgloss.Right) {
		return lipgloss.NewStyle().Width(width).Align(align).Render(rendered)
	}

	return rendered
}

func EmptyState(message string, width int) string {
	return RenderState(StateOptions{Kind: StateEmpty, Message: message, Width: width})
}

func LoadingState(message string, width int) string {
	return RenderState(StateOptions{Kind: StateLoading, Message: message, Width: width})
}

func ErrorState(message, hint string, width int) string {
	return RenderState(StateOptions{Kind: StateError, Message: message, Hint: hint, Width: width})
}
