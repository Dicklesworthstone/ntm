// Package components provides shared TUI building blocks.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/terminal"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Tab represents a single tab in the tab bar.
type Tab struct {
	ID       string // Unique identifier for the panel
	Label    string // Display label
	Icon     string // Optional icon prefix
	Badge    int    // Notification badge count (0 = hidden)
	HasError bool   // Show error indicator
}

// TabBarOptions configures tab bar rendering.
type TabBarOptions struct {
	Tabs       []Tab  // Available tabs
	ActiveID   string // Currently active tab ID
	Width      int    // Total available width
	Focused    bool   // Whether the tab bar area has focus
	ShowBadges bool   // Whether to show notification badges
}

// RenderTabBar renders a horizontal tab bar with active tab highlighting.
// Adapts from beads_rust's semantic styling: active = accent color, inactive = muted.
func RenderTabBar(opts TabBarOptions) string {
	t := theme.Current()
	if len(opts.Tabs) == 0 || opts.Width <= 0 {
		return ""
	}

	// Style definitions
	activeTab := lipgloss.NewStyle().
		Foreground(t.Base).
		Background(t.Blue).
		Bold(true).
		Padding(0, 1)

	inactiveTab := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Background(t.Surface0).
		Padding(0, 1)

	errorTab := lipgloss.NewStyle().
		Foreground(t.Base).
		Background(t.Red).
		Bold(true).
		Padding(0, 1)

	badgeStyle := lipgloss.NewStyle().
		Foreground(t.Base).
		Background(t.Yellow).
		Bold(true).
		Padding(0, 0)

	separator := lipgloss.NewStyle().
		Foreground(t.Surface1).
		Render("│")

	// Build tabs, tracking per-tab widths and active index for the underline.
	type tabMeta struct {
		width    int
		isActive bool
	}
	var parts []string
	var metas []tabMeta
	for _, tab := range opts.Tabs {
		label := tab.Label
		if tab.Icon != "" {
			label = tab.Icon + " " + label
		}

		// Append badge if present
		badgeSuffix := ""
		if opts.ShowBadges && tab.Badge > 0 {
			count := tab.Badge
			if count > 99 {
				count = 99
			}
			badgeSuffix = " " + badgeStyle.Render(strings.Repeat("•", min(count, 3)))
		}

		var rendered string
		active := tab.ID == opts.ActiveID
		if active {
			rendered = activeTab.Render(label + badgeSuffix)
		} else if tab.HasError {
			rendered = errorTab.Render(label + badgeSuffix)
		} else {
			rendered = inactiveTab.Render(label + badgeSuffix)
		}

		parts = append(parts, rendered)
		metas = append(metas, tabMeta{width: lipgloss.Width(rendered), isActive: active})
	}

	// Join with separators, respecting width
	var result strings.Builder
	totalWidth := 0
	for i, part := range parts {
		partWidth := lipgloss.Width(part)
		sepWidth := 0
		if i > 0 {
			sepWidth = 1
		}

		if totalWidth+partWidth+sepWidth > opts.Width {
			// Show overflow indicator
			overflow := lipgloss.NewStyle().
				Foreground(t.Overlay).
				Render(" +" + strings.Repeat("·", 1))
			result.WriteString(overflow)
			break
		}

		if i > 0 {
			result.WriteString(separator)
			totalWidth += sepWidth
		}
		result.WriteString(part)
		totalWidth += partWidth
	}

	// Pad remaining width with background
	remaining := opts.Width - totalWidth
	if remaining > 0 {
		fill := lipgloss.NewStyle().
			Background(t.Surface0).
			Width(remaining).
			Render("")
		result.WriteString(fill)
	}

	tabRow := result.String()

	// --- Gradient underline bar below the tabs ---
	truecolor := terminal.SupportsTrueColor()
	const underChar = "━" // heavy horizontal line

	// Reconstruct the column layout: for each displayed tab, emit an
	// underline segment whose width matches the rendered tab. Separators
	// between tabs (1-column "│") get a dim underline as well.
	var underline strings.Builder
	ulWidth := 0
	for i, meta := range metas {
		// Same overflow guard as the tab loop above.
		sepWidth := 0
		if i > 0 {
			sepWidth = 1
		}
		if ulWidth+meta.width+sepWidth > opts.Width {
			break
		}

		// Separator column
		if i > 0 {
			underline.WriteString(
				lipgloss.NewStyle().Foreground(t.Surface1).Render(underChar),
			)
			ulWidth += sepWidth
		}

		// Tab underline segment
		seg := strings.Repeat(underChar, meta.width)
		if meta.isActive {
			if truecolor {
				// Gradient: Blue → Lavender → Mauve
				seg = styles.GradientText(seg,
					string(t.Blue), string(t.Lavender), string(t.Mauve))
			} else {
				// Fallback: solid Blue
				seg = lipgloss.NewStyle().Foreground(t.Blue).Render(seg)
			}
		} else {
			seg = lipgloss.NewStyle().Foreground(t.Surface1).Render(seg)
		}
		underline.WriteString(seg)
		ulWidth += meta.width
	}

	// Fill remaining width with dim underline
	if rem := opts.Width - ulWidth; rem > 0 {
		underline.WriteString(
			lipgloss.NewStyle().Foreground(t.Surface1).
				Render(strings.Repeat(underChar, rem)),
		)
	}

	return fmt.Sprintf("%s\n%s", tabRow, underline.String())
}

// PanelIDToTab converts a panel identifier to a Tab with appropriate labeling.
func PanelIDToTab(id string, badge int, hasError bool) Tab {
	labels := map[string]struct{ label, icon string }{
		"panes":     {"Panes", "◫"},
		"detail":    {"Detail", "◳"},
		"beads":     {"Beads", "◉"},
		"alerts":    {"Alerts", "⚠"},
		"conflicts": {"Conflicts", "⚡"},
		"metrics":   {"Metrics", "◆"},
		"history":   {"History", "◷"},
		"sidebar":   {"Sidebar", "▐"},
	}

	info, ok := labels[id]
	if !ok {
		info = struct{ label, icon string }{id, "·"}
	}

	return Tab{
		ID:       id,
		Label:    info.label,
		Icon:     info.icon,
		Badge:    badge,
		HasError: hasError,
	}
}
