// Package theme provides Catppuccin theming for NTM's TUI.
// huh.go provides a huh forms theme adapter that matches NTM's Catppuccin variants.
package theme

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// HuhTheme returns a huh.Theme configured to match the current NTM theme.
// This ensures huh forms (confirmations, inputs, selects) use consistent
// Catppuccin styling with the rest of the TUI.
func HuhTheme() *huh.Theme {
	return HuhThemeFrom(Current())
}

// HuhThemeFrom creates a huh.Theme from a specific NTM theme.
// Use this when you need a theme for a specific variant rather than Current().
func HuhThemeFrom(t Theme) *huh.Theme {
	// Start with huh's built-in Catppuccin as base (it uses Mocha)
	baseTheme := *huh.ThemeCatppuccin()
	base := &baseTheme

	// Override with our theme's colors for consistency
	base.Focused.Title = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Text))).
		Bold(true)

	base.Focused.Description = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Subtext)))

	base.Focused.ErrorIndicator = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Error)))

	base.Focused.ErrorMessage = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Error)))

	// Select styles
	base.Focused.SelectSelector = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary))).
		SetString("> ")

	base.Focused.Option = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Text)))

	base.Focused.SelectedOption = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary)))

	base.Focused.UnselectedOption = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	// Multi-select styles
	base.Focused.MultiSelectSelector = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary))).
		SetString("> ")

	base.Focused.SelectedPrefix = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Success))).
		SetString("[x] ")

	base.Focused.UnselectedPrefix = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay))).
		SetString("[ ] ")

	// Button styles for Confirm
	base.Focused.FocusedButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Base))).
		Background(lipgloss.Color(string(t.Primary))).
		Padding(0, 2).
		Bold(true)

	base.Focused.BlurredButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Subtext))).
		Background(lipgloss.Color(string(t.Surface0))).
		Padding(0, 2)

	// Text input styles
	base.Focused.TextInput.Cursor = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary)))

	base.Focused.TextInput.Placeholder = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	base.Focused.TextInput.Prompt = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary)))

	// Card/Note styles
	base.Focused.Card = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(string(t.Surface2))).
		Padding(1, 2)

	base.Focused.NoteTitle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Primary))).
		Bold(true)

	// Next indicator
	base.Focused.Next = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Subtext)))

	base.Focused.NextIndicator = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	base.Focused.PrevIndicator = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	// Blurred variants - dim versions
	base.Blurred.Title = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Subtext)))

	base.Blurred.Description = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	base.Blurred.SelectSelector = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay))).
		SetString("  ")

	base.Blurred.Option = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay)))

	base.Blurred.FocusedButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay))).
		Background(lipgloss.Color(string(t.Surface0))).
		Padding(0, 2)

	base.Blurred.BlurredButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Overlay))).
		Background(lipgloss.Color(string(t.Surface0))).
		Padding(0, 2)

	// Form-level styles
	base.Form = huh.FormStyles{
		// No extra padding - let the form control layout
	}

	// Group styles
	base.Group = huh.GroupStyles{}

	// Field separator
	base.FieldSeparator = lipgloss.NewStyle().
		SetString("\n")

	return base
}

// HuhDestructiveTheme returns a huh theme styled for destructive actions.
// Red-tinted confirm buttons emphasize the danger of the action.
func HuhDestructiveTheme() *huh.Theme {
	return HuhDestructiveThemeFrom(Current())
}

// HuhDestructiveThemeFrom creates a destructive-action theme from a specific NTM theme.
func HuhDestructiveThemeFrom(t Theme) *huh.Theme {
	base := HuhThemeFrom(t)

	// Override button colors for destructive actions
	base.Focused.FocusedButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Base))).
		Background(lipgloss.Color(string(t.Error))).
		Padding(0, 2).
		Bold(true)

	base.Focused.BlurredButton = lipgloss.NewStyle().
		Foreground(lipgloss.Color(string(t.Text))).
		Background(lipgloss.Color(string(t.Surface1))).
		Padding(0, 2)

	return base
}
