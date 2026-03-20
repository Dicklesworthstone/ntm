// Package components provides shared TUI building blocks.
package components

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ToastLevel defines the severity/color of a toast notification.
type ToastLevel int

const (
	ToastInfo    ToastLevel = iota // Blue info toast
	ToastSuccess                   // Green success toast
	ToastWarning                   // Yellow warning toast
	ToastError                     // Red error toast
)

// Toast represents a single ephemeral notification.
type Toast struct {
	ID        string     // Unique identifier for dedup
	Message   string     // Display text
	Level     ToastLevel // Severity level
	CreatedAt time.Time  // When the toast was created
	Duration  time.Duration // How long to display (0 = default 4s)
}

// DefaultToastDuration is the default display time for toasts.
const DefaultToastDuration = 4 * time.Second

// MaxToasts is the maximum number of toasts displayed simultaneously.
const MaxToasts = 4

// ToastManager tracks active toasts with automatic expiry.
type ToastManager struct {
	toasts []Toast
	seen   map[string]time.Time // Dedup: ID -> last seen time
}

// NewToastManager creates a new toast manager.
func NewToastManager() *ToastManager {
	return &ToastManager{
		seen: make(map[string]time.Time),
	}
}

// Push adds a toast notification. Deduplicates by ID within the toast duration.
func (tm *ToastManager) Push(toast Toast) {
	if toast.Duration == 0 {
		toast.Duration = DefaultToastDuration
	}
	if toast.CreatedAt.IsZero() {
		toast.CreatedAt = time.Now()
	}

	// Dedup check
	if toast.ID != "" {
		if lastSeen, ok := tm.seen[toast.ID]; ok {
			if time.Since(lastSeen) < toast.Duration {
				return // Skip duplicate
			}
		}
		tm.seen[toast.ID] = toast.CreatedAt
	}

	tm.toasts = append(tm.toasts, toast)

	// Trim to max
	if len(tm.toasts) > MaxToasts {
		tm.toasts = tm.toasts[len(tm.toasts)-MaxToasts:]
	}
}

// Tick prunes expired toasts. Call on each dashboard tick.
func (tm *ToastManager) Tick() {
	now := time.Now()
	active := tm.toasts[:0]
	for _, t := range tm.toasts {
		dur := t.Duration
		if dur == 0 {
			dur = DefaultToastDuration
		}
		if now.Sub(t.CreatedAt) < dur {
			active = append(active, t)
		}
	}
	tm.toasts = active

	// Clean old dedup entries
	for id, seen := range tm.seen {
		if now.Sub(seen) > 30*time.Second {
			delete(tm.seen, id)
		}
	}
}

// Count returns the number of active toasts.
func (tm *ToastManager) Count() int {
	return len(tm.toasts)
}

// RenderToasts renders all active toasts as a vertical stack for overlay.
// Designed to be positioned in the bottom-right corner of the dashboard.
func (tm *ToastManager) RenderToasts(maxWidth int) string {
	if len(tm.toasts) == 0 || maxWidth <= 0 {
		return ""
	}

	t := theme.Current()
	toastWidth := maxWidth
	if toastWidth > 60 {
		toastWidth = 60
	}
	if toastWidth < 20 {
		toastWidth = 20
	}

	var rendered []string
	for _, toast := range tm.toasts {
		var bgColor, fgColor, iconColor lipgloss.Color
		var icon string

		switch toast.Level {
		case ToastSuccess:
			bgColor = t.Surface0
			fgColor = t.Green
			iconColor = t.Green
			icon = "✓"
		case ToastWarning:
			bgColor = t.Surface0
			fgColor = t.Yellow
			iconColor = t.Yellow
			icon = "⚠"
		case ToastError:
			bgColor = t.Surface0
			fgColor = t.Red
			iconColor = t.Red
			icon = "✗"
		default: // ToastInfo
			bgColor = t.Surface0
			fgColor = t.Blue
			iconColor = t.Blue
			icon = "ℹ"
		}

		// Calculate remaining time for fade effect
		elapsed := time.Since(toast.CreatedAt)
		dur := toast.Duration
		if dur == 0 {
			dur = DefaultToastDuration
		}
		remaining := dur - elapsed

		// Dim the toast as it approaches expiry (last 25%)
		if remaining < dur/4 {
			fgColor = t.Overlay
			iconColor = t.Overlay
		}

		iconStyled := lipgloss.NewStyle().
			Foreground(iconColor).
			Bold(true).
			Render(icon)

		// Truncate message to fit
		msg := toast.Message
		msgMaxWidth := toastWidth - 6 // icon + padding + border
		if msgMaxWidth < 10 {
			msgMaxWidth = 10
		}
		if lipgloss.Width(msg) > msgMaxWidth {
			runes := []rune(msg)
			if len(runes) > msgMaxWidth-1 {
				msg = string(runes[:msgMaxWidth-1]) + "…"
			}
		}

		msgStyled := lipgloss.NewStyle().
			Foreground(fgColor).
			Render(msg)

		content := iconStyled + " " + msgStyled

		toastBox := lipgloss.NewStyle().
			Background(bgColor).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(fgColor).
			Padding(0, 1).
			Width(toastWidth).
			Render(content)

		_ = bgColor // used in style above
		rendered = append(rendered, toastBox)
	}

	return strings.Join(rendered, "\n")
}
