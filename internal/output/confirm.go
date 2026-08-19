package output

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
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

// ConfirmWithOptions prompts with custom options.
func ConfirmWithOptions(prompt string, opts ConfirmOptions) bool {
	return ConfirmWriter(os.Stdout, os.Stdin, prompt, opts)
}

// ConfirmWriter prompts using the given writer and reader.
func ConfirmWriter(w io.Writer, r io.Reader, prompt string, opts ConfirmOptions) bool {
	if ok, handled := confirmWithHuh(w, r, prompt, opts); handled {
		return ok
	}

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

func confirmWithHuh(w io.Writer, r io.Reader, prompt string, opts ConfirmOptions) (confirmed bool, handled bool) {
	stdout, ok := w.(*os.File)
	if !ok || stdout != os.Stdout || !term.IsTerminal(int(stdout.Fd())) {
		return false, false
	}

	stdin, ok := r.(*os.File)
	if !ok || stdin != os.Stdin || !term.IsTerminal(int(stdin.Fd())) {
		return false, false
	}

	confirmed = opts.Default
	confirm := huh.NewConfirm().
		Title(prompt).
		Affirmative(confirmAffirmativeLabel(opts)).
		Negative(confirmNegativeLabel(opts)).
		Value(&confirmed)

	form := huh.NewForm(huh.NewGroup(confirm)).WithTheme(confirmHuhTheme(opts))
	if err := form.Run(); err != nil {
		return false, false
	}
	return confirmed, true
}

func confirmAffirmativeLabel(opts ConfirmOptions) string {
	switch opts.Style {
	case StyleDestructive:
		return "Yes, continue"
	case StyleInfo:
		return "Continue"
	default:
		return "Yes"
	}
}

func confirmNegativeLabel(opts ConfirmOptions) string {
	switch opts.Style {
	case StyleDestructive:
		return "Cancel"
	case StyleInfo:
		return "Abort"
	default:
		return "No"
	}
}

func confirmHuhTheme(opts ConfirmOptions) *huh.Theme {
	switch opts.Style {
	case StyleDestructive:
		return theme.HuhDestructiveTheme()
	default:
		return theme.HuhTheme()
	}
}
