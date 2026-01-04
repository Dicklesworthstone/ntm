package output

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ConfirmStyle defines the type of confirmation prompt
type ConfirmStyle int

const (
	// StyleDefault is a neutral confirmation
	StyleDefault ConfirmStyle = iota
	// StyleDestructive is for potentially dangerous operations
	StyleDestructive
	// StyleInfo is for informational confirmations
	StyleInfo
)

// ConfirmOptions configures the confirm prompt behavior
type ConfirmOptions struct {
	// Style changes the visual appearance based on action type
	Style ConfirmStyle
	// Default sets whether Y or N is the default (true = Y, false = N)
	Default bool
	// HideHint hides the [y/N] hint
	HideHint bool
}

// Confirm prompts the user for confirmation with styled output.
// Returns true if the user confirmed, false otherwise.
func Confirm(prompt string) bool {
	return ConfirmWithOptions(prompt, ConfirmOptions{})
}

// ConfirmWithOptions prompts with custom options.
func ConfirmWithOptions(prompt string, opts ConfirmOptions) bool {
	return ConfirmWriter(os.Stdout, os.Stdin, prompt, opts)
}

// ConfirmWriter prompts using the given writer and reader.
func ConfirmWriter(w io.Writer, r io.Reader, prompt string, opts ConfirmOptions) bool {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}

	t := theme.Current()

	// Build the styled prompt
	var icon string
	var iconStyle lipgloss.Style
	var promptStyle lipgloss.Style

	switch opts.Style {
	case StyleDestructive:
		icon = "⚠"
		iconStyle = lipgloss.NewStyle().Foreground(t.Warning).Bold(true)
		promptStyle = lipgloss.NewStyle().Foreground(t.Warning)
	case StyleInfo:
		icon = "?"
		iconStyle = lipgloss.NewStyle().Foreground(t.Info).Bold(true)
		promptStyle = lipgloss.NewStyle().Foreground(t.Text)
	default:
		icon = "?"
		iconStyle = lipgloss.NewStyle().Foreground(t.Lavender).Bold(true)
		promptStyle = lipgloss.NewStyle().Foreground(t.Text)
	}

	// Build hint based on default
	var hint string
	if !opts.HideHint {
		if opts.Default {
			hint = "[Y/n]"
		} else {
			hint = "[y/N]"
		}
	}

	// Render the prompt
	if useColor {
		hintStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		fmt.Fprintf(w, "%s %s %s ",
			iconStyle.Render(icon),
			promptStyle.Render(prompt),
			hintStyle.Render(hint),
		)
	} else {
		fmt.Fprintf(w, "%s %s %s ", icon, prompt, hint)
	}

	// Read answer
	reader := bufio.NewReader(r)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	// Handle empty answer based on default
	if answer == "" {
		return opts.Default
	}

	return answer == "y" || answer == "yes"
}

// ConfirmDestructive is a convenience function for destructive operations.
// Uses warning styling and defaults to N.
func ConfirmDestructive(prompt string) bool {
	return ConfirmWithOptions(prompt, ConfirmOptions{
		Style:   StyleDestructive,
		Default: false,
	})
}

// MustConfirm prompts for confirmation and calls os.Exit(1) if declined.
// Use for operations that cannot proceed without confirmation.
func MustConfirm(prompt string) {
	if !Confirm(prompt) {
		fmt.Fprintln(os.Stderr, "Operation cancelled.")
		os.Exit(1)
	}
}

// MustConfirmDestructive prompts with destructive styling and exits if declined.
func MustConfirmDestructive(prompt string) {
	if !ConfirmDestructive(prompt) {
		fmt.Fprintln(os.Stderr, "Operation cancelled.")
		os.Exit(1)
	}
}
