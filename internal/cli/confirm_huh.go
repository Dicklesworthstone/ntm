// Package cli provides NTM command-line interface.
// confirm_huh.go provides styled confirmation dialogs using charmbracelet/huh.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// confirmHuh shows a styled confirmation dialog using huh.
// Falls back to simple text prompt if not running in a TTY.
func confirmHuh(title, description string) bool {
	if !isTTY() {
		return confirmSimple(title)
	}

	var confirmed bool
	form := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed)

	if err := form.WithTheme(theme.HuhTheme()).Run(); err != nil {
		// User pressed Ctrl+C or form errored - treat as "no"
		return false
	}
	return confirmed
}

// confirmHuhDestructive shows a destructive-action confirmation dialog.
// Uses red-tinted styling to emphasize the danger of the action.
// Falls back to simple text prompt if not running in a TTY.
func confirmHuhDestructive(title, description string) bool {
	if !isTTY() {
		return confirmSimple(title)
	}

	var confirmed bool
	form := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative("Yes, I'm sure").
		Negative("Cancel").
		Value(&confirmed)

	if err := form.WithTheme(theme.HuhDestructiveTheme()).Run(); err != nil {
		return false
	}
	return confirmed
}

// confirmSimple prompts for y/n confirmation via stdin.
// Used as fallback when huh forms aren't available.
func confirmSimple(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

// isTTY returns true if stdin/stdout are connected to a terminal.
func isTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}
