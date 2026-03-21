// Package components provides shared TUI building blocks.
package components

import (
	"strings"
	"time"

	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
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
	ID        string        // Unique identifier for dedup
	Message   string        // Display text
	Level     ToastLevel    // Severity level
	CreatedAt time.Time     // When the toast was created
	Duration  time.Duration // How long to display (0 = default 4s)

	// Spring animation fields (internal)
	offsetX    float64          // Current horizontal offset (pixels from final position)
	offsetXVel float64          // Current velocity for spring physics
	spring     harmonica.Spring // Spring physics engine
	dismissed  bool             // Whether toast is animating out
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

	// Initialize spring animation (slide in from right)
	if styles.ReducedMotionEnabled() {
		toast.offsetX = 0.0
		toast.offsetXVel = 0.0
	} else {
		// Create spring: 60 FPS, frequency 6.0 Hz, damping 0.4 (slightly underdamped for bounce)
		toast.spring = harmonica.NewSpring(harmonica.FPS(60), 6.0, 0.4)
		toast.offsetX = 40.0 // Start 40 chars to the right (offscreen)
		toast.offsetXVel = 0.0
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

// Tick prunes expired toasts and updates spring animations. Call on each dashboard tick.
func (tm *ToastManager) Tick() {
	now := time.Now()
	reducedMotion := styles.ReducedMotionEnabled()

	// Update spring animations for all toasts
	if !reducedMotion {
		for i := range tm.toasts {
			t := &tm.toasts[i]
			// Calculate target position: 0 for active, 60 for dismissed (slide out right)
			target := 0.0
			if t.dismissed {
				target = 60.0
			}
			t.offsetX, t.offsetXVel = t.spring.Update(t.offsetX, t.offsetXVel, target)
		}
	}

	// Prune expired toasts (keep if not expired or still animating out)
	active := tm.toasts[:0]
	for _, t := range tm.toasts {
		dur := t.Duration
		if dur == 0 {
			dur = DefaultToastDuration
		}
		expired := now.Sub(t.CreatedAt) >= dur

		if expired && !t.dismissed {
			// Start dismiss animation
			t.dismissed = true
		}

		// Reduced motion suppresses slide animations, so dismissed toasts should
		// disappear on the next tick instead of waiting for offsetX to advance.
		keep := !t.dismissed && !expired
		if t.dismissed && !reducedMotion && t.offsetX < 55.0 {
			keep = true
		}
		if keep {
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

// IsAnimating returns true if any toast is currently animating (slide-in or slide-out).
// Use this to request faster tick rates during animation.
func (tm *ToastManager) IsAnimating() bool {
	if styles.ReducedMotionEnabled() {
		return false
	}
	for _, t := range tm.toasts {
		// Animating in: offset > 0.5 and not dismissed
		if !t.dismissed && t.offsetX > 0.5 {
			return true
		}
		// Animating out: dismissed and offset < 55
		if t.dismissed && t.offsetX < 55.0 {
			return true
		}
	}
	return false
}

// Dismiss marks a toast for removal, triggering the slide-out animation.
func (tm *ToastManager) Dismiss(id string) bool {
	for i := range tm.toasts {
		if tm.toasts[i].ID == id && !tm.toasts[i].dismissed {
			tm.toasts[i].dismissed = true
			return true
		}
	}
	return false
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

		// Truncate message to fit by visual width (not rune count),
		// so wide characters (CJK, emojis) are handled correctly.
		msg := toast.Message
		msgMaxWidth := toastWidth - 6 // icon + padding + border
		if msgMaxWidth < 10 {
			msgMaxWidth = 10
		}
		if lipgloss.Width(msg) > msgMaxWidth {
			runes := []rune(msg)
			truncated := make([]rune, 0, len(runes))
			visWidth := 0
			for _, r := range runes {
				rw := lipgloss.Width(string(r))
				if visWidth+rw+1 > msgMaxWidth { // +1 for "…"
					break
				}
				truncated = append(truncated, r)
				visWidth += rw
			}
			msg = string(truncated) + "…"
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

		// Apply horizontal offset from spring animation
		if toast.offsetX > 0.5 {
			// Pad with spaces to create the offset effect
			offset := int(toast.offsetX)
			if offset > 0 {
				toastBox = strings.Repeat(" ", offset) + toastBox
			}
		}

		rendered = append(rendered, toastBox)
	}

	return strings.Join(rendered, "\n")
}
