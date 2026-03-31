// Package components provides shared TUI building blocks.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// KeyHint represents a single keybinding hint (e.g., "↑/↓" → "navigate").
type KeyHint struct {
	Key  string // The key(s) to press, e.g., "↑/↓", "Enter", "q"
	Desc string // Brief description, e.g., "navigate", "select", "quit"
}

type DashboardHelpVerbosity string

const (
	DashboardHelpVerbosityMinimal DashboardHelpVerbosity = "minimal"
	DashboardHelpVerbosityFull    DashboardHelpVerbosity = "full"
)

type DashboardHelpOptions struct {
	Verbosity DashboardHelpVerbosity
	Debug     bool
	PopupMode bool // Dashboard is running in a tmux popup overlay
}

func DashboardHelpOptionsFrom(verbosity string, debug bool) DashboardHelpOptions {
	v := strings.ToLower(strings.TrimSpace(verbosity))
	opts := DashboardHelpOptions{
		Verbosity: DashboardHelpVerbosityFull,
		Debug:     debug,
	}
	if v == string(DashboardHelpVerbosityMinimal) {
		opts.Verbosity = DashboardHelpVerbosityMinimal
	}
	// Debug mode is an opt-in superset of full help.
	if opts.Debug {
		opts.Verbosity = DashboardHelpVerbosityFull
	}
	return opts
}

// HelpBarOptions configures HelpBar rendering.
type HelpBarOptions struct {
	Hints     []KeyHint // Key hints to display
	Width     int       // Available width (0 = unlimited)
	Separator string    // Separator between hints (default: "  ")
}

// RenderKeyHint renders a single key hint with consistent styling.
// Returns styled string: "[key] desc"
func RenderKeyHint(hint KeyHint) string {
	t := theme.Current()

	keyStyle := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Bold(true).
		Padding(0, 1)

	descStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)

	return keyStyle.Render(hint.Key) + " " + descStyle.Render(hint.Desc)
}

// RenderKeyHintCompact renders a minimal key hint without background.
// Returns styled string: "key desc" (for narrow widths).
func RenderKeyHintCompact(hint KeyHint) string {
	t := theme.Current()

	keyStyle := lipgloss.NewStyle().
		Foreground(t.Text).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)

	return keyStyle.Render(hint.Key) + " " + descStyle.Render(hint.Desc)
}

// RenderHelpBar renders a horizontal bar of key hints, respecting width constraints.
// Hints are progressively hidden from right-to-left if they don't fit.
// Note: This complements the existing HelpBar struct type in preview.go with a
// function-based API that supports width-aware truncation.
func RenderHelpBar(opts HelpBarOptions) string {
	if len(opts.Hints) == 0 {
		return ""
	}

	sep := opts.Separator
	if sep == "" {
		sep = "  "
	}

	// Determine rendering style based on width tier
	tier := layout.TierForWidth(opts.Width)
	compact := tier == layout.TierNarrow

	// Build all rendered hints
	var rendered []string
	for _, h := range opts.Hints {
		if compact {
			rendered = append(rendered, RenderKeyHintCompact(h))
		} else {
			rendered = append(rendered, RenderKeyHint(h))
		}
	}

	// If no width constraint, return all hints
	if opts.Width <= 0 {
		return strings.Join(rendered, sep)
	}

	// Progressive truncation: remove hints from right until it fits
	sepWidth := lipgloss.Width(sep)
	for len(rendered) > 0 {
		total := 0
		for i, r := range rendered {
			total += lipgloss.Width(r)
			if i > 0 {
				total += sepWidth
			}
		}
		if total <= opts.Width {
			break
		}
		// Remove rightmost hint
		rendered = rendered[:len(rendered)-1]
	}

	return strings.Join(rendered, sep)
}

// HelpOverlayOptions configures the help overlay appearance.
type HelpOverlayOptions struct {
	Title    string        // Overlay title (default: "Keyboard Shortcuts")
	Sections []HelpSection // Grouped key hints
	Width    int           // Overlay width (0 = auto-size)
	MaxWidth int           // Maximum width cap
}

// HelpSection groups related key hints under a heading.
type HelpSection struct {
	Title string    // Section heading, e.g., "Navigation", "Actions"
	Hints []KeyHint // Key hints in this section
}

// HelpOverlay renders a modal-style help overlay with grouped keybindings.
// Designed to be shown when user presses '?' or F1.
func HelpOverlay(opts HelpOverlayOptions) string {
	t := theme.Current()
	ic := icons.Current()

	// Calculate content to determine width
	var lines []string

	// Title
	title := opts.Title
	if title == "" {
		title = "Keyboard Shortcuts"
	}

	titleIcon := strings.TrimSpace(ic.Help)
	if titleIcon == "" {
		titleIcon = "?"
	}

	titleStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	lines = append(lines, titleStyle.Render(titleIcon+"  "+title))
	lines = append(lines, "")

	// Styles for sections
	sectionStyle := lipgloss.NewStyle().
		Foreground(t.Mauve).
		Bold(true)

	keyStyle := lipgloss.NewStyle().
		Foreground(t.Text).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(t.Subtext)

	// Find max key width for alignment
	maxKeyWidth := 0
	for _, section := range opts.Sections {
		for _, hint := range section.Hints {
			w := lipgloss.Width(hint.Key)
			if w > maxKeyWidth {
				maxKeyWidth = w
			}
		}
	}

	// Render sections
	for i, section := range opts.Sections {
		if section.Title != "" {
			lines = append(lines, sectionStyle.Render(section.Title))
		}

		for _, hint := range section.Hints {
			// Right-align key, left-align description
			keyPadded := lipgloss.NewStyle().
				Width(maxKeyWidth).
				Align(lipgloss.Right).
				Render(hint.Key)

			line := "  " + keyStyle.Render(keyPadded) + "  " + descStyle.Render(hint.Desc)
			lines = append(lines, line)
		}

		// Add spacing between sections (except last)
		if i < len(opts.Sections)-1 {
			lines = append(lines, "")
		}
	}

	// Add footer hint
	lines = append(lines, "")
	footerStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Italic(true)
	lines = append(lines, footerStyle.Render("Press ? or Esc to close"))

	content := strings.Join(lines, "\n")

	// Calculate box width
	width := opts.Width
	if width <= 0 {
		// Auto-size based on content
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > width {
				width = w
			}
		}
		width += 4 // Padding
	}

	if opts.MaxWidth > 0 && width > opts.MaxWidth {
		width = opts.MaxWidth
	}

	// Render in a styled box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Blue).
		Padding(1, 2).
		Width(width)

	return boxStyle.Render(content)
}

// CommonNavigationHints returns standard navigation key hints.
func CommonNavigationHints() []KeyHint {
	return []KeyHint{
		{Key: "↑/↓", Desc: "navigate"},
		{Key: "j/k", Desc: "navigate"},
	}
}

// CommonSelectionHints returns standard selection key hints.
func CommonSelectionHints() []KeyHint {
	return []KeyHint{
		{Key: "Enter", Desc: "select"},
		{Key: "1-9", Desc: "quick select"},
	}
}

// CommonQuitHints returns standard quit/back key hints.
func CommonQuitHints() []KeyHint {
	return []KeyHint{
		{Key: "Esc", Desc: "back"},
		{Key: "q", Desc: "quit"},
	}
}

// DefaultPaletteHints returns the standard palette key hints.
func DefaultPaletteHints() []KeyHint {
	return []KeyHint{
		{Key: "↑/↓", Desc: "navigate"},
		{Key: "1-9", Desc: "quick select"},
		{Key: "Enter", Desc: "select"},
		{Key: "Esc", Desc: "back"},
	}
}

// DefaultDashboardHints returns the standard dashboard key hints.
// Order matters: most important hints first (quit, help) so they remain visible at narrow widths.
func DefaultDashboardHints() []KeyHint {
	return DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull})
}

// DashboardHelpBarHints returns key hints for the dashboard footer help bar.
// Order matters: RenderHelpBar truncates from right-to-left, so the most important hints
// must come first.
func DashboardHelpBarHints(opts DashboardHelpOptions) []KeyHint {
	// In popup mode, lead with "Esc close" instead of "q quit"
	quitHint := KeyHint{Key: "q", Desc: "quit"}
	if opts.PopupMode {
		quitHint = KeyHint{Key: "Esc", Desc: "close"}
	}

	switch opts.Verbosity {
	case DashboardHelpVerbosityMinimal:
		return []KeyHint{
			quitHint,
			{Key: "?", Desc: "help"},
			{Key: "↑↓", Desc: "navigate"},
			{Key: "1-9", Desc: "select"},
		}
	default:
		hints := []KeyHint{
			quitHint,
			{Key: "?", Desc: "help"},
			{Key: "↑↓", Desc: "navigate"},
			{Key: "Tab", Desc: "panels"},
			{Key: "1-9", Desc: "select"},
			{Key: "z", Desc: "zoom"},
			{Key: "r", Desc: "refresh"},
		}
		if opts.Debug {
			hints = append(hints,
				KeyHint{Key: "d", Desc: "diag"},
				KeyHint{Key: "u", Desc: "scan"},
				KeyHint{Key: "ctrl+k", Desc: "checkpoint"},
			)
		}
		return hints
	}
}

// PaletteHelpSections returns help sections for the palette overlay.
func PaletteHelpSections() []HelpSection {
	return []HelpSection{
		{
			Title: "Navigation",
			Hints: []KeyHint{
				{Key: "↑ / k", Desc: "Move up"},
				{Key: "↓ / j", Desc: "Move down"},
				{Key: "1-9", Desc: "Quick select item"},
			},
		},
		{
			Title: "Actions",
			Hints: []KeyHint{
				{Key: "Enter", Desc: "Select command"},
				{Key: "1-4", Desc: "Select target (in target phase)"},
				{Key: "e", Desc: "Edit prompt before sending (in target phase)"},
				{Key: "Ctrl+S", Desc: "Save edit and return to target selection"},
				{Key: "Ctrl+P", Desc: "Pin / unpin command"},
				{Key: "Ctrl+F", Desc: "Favorite / unfavorite command"},
				{Key: "Type", Desc: "Filter commands"},
			},
		},
		{
			Title: "General",
			Hints: []KeyHint{
				{Key: "Esc", Desc: "Go back / Cancel"},
				{Key: "q", Desc: "Quit palette"},
				{Key: "Ctrl+C", Desc: "Force quit"},
			},
		},
	}
}

// DashboardHelpSections returns help sections for the dashboard overlay.
func DashboardHelpSections(opts DashboardHelpOptions) []HelpSection {
	if opts.Verbosity == DashboardHelpVerbosityMinimal {
		return []HelpSection{
			{
				Title: "Navigation",
				Hints: []KeyHint{
					{Key: "↑ / k", Desc: "Move up"},
					{Key: "↓ / j", Desc: "Move down"},
					{Key: "1-9", Desc: "Quick select pane"},
				},
			},
			{
				Title: "General",
				Hints: []KeyHint{
					{Key: "?", Desc: "Toggle help"},
					{Key: "q / Esc", Desc: "Quit dashboard"},
					{Key: "Ctrl+C", Desc: "Force quit"},
				},
			},
		}
	}

	sections := []HelpSection{
		{
			Title: "Navigation",
			Hints: []KeyHint{
				{Key: "↑ / k", Desc: "Move up"},
				{Key: "↓ / j", Desc: "Move down"},
				{Key: "← / h", Desc: "Previous panel"},
				{Key: "→ / l", Desc: "Next panel"},
				{Key: "Tab", Desc: "Cycle panels"},
				{Key: "1-9", Desc: "Quick select pane"},
			},
		},
		{
			Title: "Actions",
			Hints: []KeyHint{
				{Key: "z / Enter", Desc: "Zoom to pane"},
				{Key: "s", Desc: "Send prompt"},
				{Key: "p", Desc: "Pause refresh"},
			},
		},
		{
			Title: "Data",
			Hints: []KeyHint{
				{Key: "c", Desc: "Refresh context"},
				{Key: "m", Desc: "Refresh mail"},
				{Key: "i", Desc: "Inbox details"},
				{Key: "Ctrl+S", Desc: "CASS search"},
			},
		},
		{
			Title: "View Controls",
			Hints: []KeyHint{
				{Key: "r", Desc: "Refresh data"},
				{Key: "?", Desc: "Toggle help"},
			},
		},
		{
			Title: "General",
			Hints: []KeyHint{
				{Key: "q / Esc", Desc: "Quit dashboard"},
				{Key: "Ctrl+C", Desc: "Force quit"},
			},
		},
	}

	if opts.Debug {
		sections = append(sections, HelpSection{
			Title: "Debug",
			Hints: []KeyHint{
				{Key: "d", Desc: "Toggle diagnostics"},
				{Key: "u", Desc: "Toggle UBS scan"},
				{Key: "Ctrl+K", Desc: "Create checkpoint"},
			},
		})
	}

	return sections
}
