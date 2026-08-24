package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// validKeyRegex validates tmux key bindings. The first character must be a
// letter, digit, or ^ (Ctrl); subsequent characters may also include - (used
// by key names like C-a and M-Left). A leading - is rejected because tmux
// would read it as a flag (e.g. "--help") and refuse the binding.
var validKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9\^][a-zA-Z0-9\-\^]*$`)

// validateKey reports whether key is a tmux key name that can be written to a
// bind-key line. The error names the rejected value so a mistyped invocation
// is self-explanatory.
func validateKey(key string) error {
	if !validKeyRegex.MatchString(key) {
		return fmt.Errorf("invalid key %q: must start with a letter, digit, or ^ (Ctrl); may contain letters, digits, -, and ^", key)
	}
	return nil
}

func newBindCmd() *cobra.Command {
	var (
		key      string
		unbind   bool
		showOnly bool
		overlay  bool
	)

	cmd := &cobra.Command{
		Use:   "bind",
		Short: "Set up tmux keybinding for command palette or overlay",
		Long: `Configure a tmux keybinding to open the NTM command palette or dashboard overlay.

By default, binds F6 to open a floating popup with the command palette.
With --overlay, binds F12 to toggle the dashboard overlay above agent panes.
Bindings are added to both the current tmux server and ~/.tmux.conf.

Examples:
  ntm bind                  # Bind F6 for palette (default)
  ntm bind --overlay        # Bind F12 for dashboard overlay
  ntm bind --key=F5         # Bind F5 for palette
  ntm bind --overlay -k F11 # Bind F11 for overlay
  ntm bind --show           # Show current binding
  ntm bind --unbind         # Remove the binding`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if overlay && !cmd.Flags().Changed("key") {
				// Default overlay key is F12 (not F6 which is the palette default)
				key = "F12"
			}

			// Validate the key before touching tmux or the config file. A
			// leading dash would be read by tmux as a flag and refuse to parse.
			if err := validateKey(key); err != nil {
				return err
			}

			if showOnly {
				return showBinding(key)
			}
			if unbind {
				return removeBinding(key)
			}
			if overlay {
				return setupOverlayBinding(key)
			}
			return setupBinding(key)
		},
	}

	cmd.Flags().StringVarP(&key, "key", "k", "F6", "Key to bind (e.g., F5, F6, F7, F12)")
	cmd.Flags().BoolVar(&unbind, "unbind", false, "Remove the binding")
	cmd.Flags().BoolVar(&showOnly, "show", false, "Show current binding")
	cmd.Flags().BoolVar(&overlay, "overlay", false, "Bind the dashboard overlay toggle instead of palette")

	return cmd
}

func setupBinding(key string) error {
	t := theme.Current()

	// The binding command. The palette is a compact dialog (content-sized list +
	// short target/agent pickers), so it uses a smaller popup than the full
	// dashboard overlay to avoid blocking most of the screen behind it (#204).
	bindCmd := fmt.Sprintf(`bind-key -n %s display-popup -E -w 80%% -h 70%% "ntm palette"`, key)

	// Apply to current tmux server (if running)
	inTmux := os.Getenv("TMUX") != ""
	if inTmux {
		cmd := exec.Command(tmux.BinaryPath(), "bind-key", "-n", key, "display-popup", "-E", "-w", "80%", "-h", "70%", "ntm palette")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not bind %q in current tmux session: %w", key, err)
		}
		fmt.Printf("%s✓%s Bound %s in current tmux server\n", colorize(t.Success), colorize(t.Text), key)
	} else {
		fmt.Printf("%s→%s Not in tmux, will only update config file\n", colorize(t.Info), colorize(t.Text))
	}

	// Update tmux.conf
	tmuxConf := filepath.Join(os.Getenv("HOME"), ".tmux.conf")

	// Read existing config
	existing := ""
	if data, err := os.ReadFile(tmuxConf); err == nil {
		existing = string(data)
	}

	// Check if binding already exists
	lines := strings.Split(existing, "\n")
	found := false
	var newLines []string

	for _, line := range lines {
		if isBindingLine(line, key) {
			newLines = append(newLines, bindCmd)
			found = true
		} else {
			newLines = append(newLines, line)
		}
	}

	if found {
		if err := os.WriteFile(tmuxConf, []byte(strings.Join(newLines, "\n")), 0600); err != nil {
			return fmt.Errorf("failed to update tmux.conf: %w", err)
		}
		fmt.Printf("%s✓%s Updated existing %s binding in %s\n", colorize(t.Success), colorize(t.Text), key, tmuxConf)
	} else {
		// Append new binding
		f, err := os.OpenFile(tmuxConf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("failed to open tmux.conf: %w", err)
		}
		defer func() { _ = f.Close() }()

		// Add comment and binding
		addition := fmt.Sprintf("\n# NTM Command Palette (added by 'ntm bind')\n%s\n", bindCmd)
		if _, err := f.WriteString(addition); err != nil {
			return fmt.Errorf("failed to write tmux.conf: %w", err)
		}
		fmt.Printf("%s✓%s Added %s binding to %s\n", colorize(t.Success), colorize(t.Text), key, tmuxConf)
	}

	// Print usage hint
	fmt.Println()
	fmt.Printf("  Press %s%s%s in tmux to open the command palette.\n",
		colorize(t.Primary), key, colorize(t.Text))

	if !inTmux {
		fmt.Printf("\n  %sNote:%s Run %stmux source ~/.tmux.conf%s to reload config.\n",
			colorize(t.Info), colorize(t.Text),
			colorize(t.Primary), colorize(t.Text))
	}

	return nil
}

func setupOverlayBinding(key string) error {
	return setupOverlayBindingWithWriter(key, os.Stdout)
}

func setupOverlayBindingQuiet(key string) error {
	return setupOverlayBindingWithWriter(key, nil)
}

func setupOverlayBindingWithWriter(key string, out io.Writer) error {
	t := theme.Current()

	// The binding: launch ntm dashboard in popup mode for the current session.
	// #{session_name} is expanded by tmux at trigger time.
	bindCmd := overlayBindingCommand(key)

	// Apply to current tmux server
	inTmux := os.Getenv("TMUX") != ""
	if inTmux {
		cmd := exec.Command(tmux.BinaryPath(), overlayBindingArgs(key)...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not bind %q for dashboard overlay in current tmux session: %w", key, err)
		}
		maybeFprintf(out, "%s✓%s Bound %s for dashboard overlay in current tmux server\n", colorize(t.Success), colorize(t.Text), key)
	} else {
		maybeFprintf(out, "%s→%s Not in tmux, will only update config file\n", colorize(t.Info), colorize(t.Text))
	}

	// Update tmux.conf
	tmuxConf := filepath.Join(os.Getenv("HOME"), ".tmux.conf")

	existing := ""
	if data, err := os.ReadFile(tmuxConf); err == nil {
		existing = string(data)
	}

	lines := strings.Split(existing, "\n")
	found := false
	var newLines []string

	for _, line := range lines {
		if isBindingLine(line, key) {
			newLines = append(newLines, bindCmd)
			found = true
		} else {
			newLines = append(newLines, line)
		}
	}

	if found {
		if err := os.WriteFile(tmuxConf, []byte(strings.Join(newLines, "\n")), 0600); err != nil {
			return fmt.Errorf("failed to update tmux.conf: %w", err)
		}
		maybeFprintf(out, "%s✓%s Updated existing %s binding in %s\n", colorize(t.Success), colorize(t.Text), key, tmuxConf)
	} else {
		f, err := os.OpenFile(tmuxConf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("failed to open tmux.conf: %w", err)
		}
		defer func() { _ = f.Close() }()

		addition := fmt.Sprintf("\n# NTM Dashboard Overlay (added by 'ntm bind --overlay')\n%s\n", bindCmd)
		if _, err := f.WriteString(addition); err != nil {
			return fmt.Errorf("failed to write tmux.conf: %w", err)
		}
		maybeFprintf(out, "%s✓%s Added %s overlay binding to %s\n", colorize(t.Success), colorize(t.Text), key, tmuxConf)
	}

	maybeFprintf(out, "\n")
	maybeFprintf(out, "  Press %s%s%s in tmux to toggle the dashboard overlay.\n",
		colorize(t.Primary), key, colorize(t.Text))
	maybeFprintf(out, "  Press %sEscape%s inside the overlay to dismiss it.\n",
		colorize(t.Primary), colorize(t.Text))
	maybeFprintf(out, "  Press %sz/Enter%s on a pane to zoom into it.\n",
		colorize(t.Primary), colorize(t.Text))

	if !inTmux {
		maybeFprintf(out, "\n  %sNote:%s Run %stmux source ~/.tmux.conf%s to reload config.\n",
			colorize(t.Info), colorize(t.Text),
			colorize(t.Primary), colorize(t.Text))
	}

	return nil
}

func overlayBindingCommand(key string) string {
	return fmt.Sprintf(`bind-key -n %s display-popup -E -w 95%% -h 95%% "NTM_POPUP=1 ntm dashboard --popup --inferred #{session_name}"`, key)
}

func overlayBindingArgs(key string) []string {
	return []string{
		"bind-key", "-n", key,
		"display-popup", "-E", "-w", "95%", "-h", "95%",
		// --inferred keeps the lenient current-session resolution: #{session_name}
		// is always the session the key was pressed in, so the overlay is a
		// current-session view and must not fail closed for unregistered sessions.
		"NTM_POPUP=1 ntm dashboard --popup --inferred #{session_name}",
	}
}

func maybeFprintf(out io.Writer, format string, args ...interface{}) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, format, args...)
}

func removeBinding(key string) error {
	t := theme.Current()

	// Remove from current tmux server
	if os.Getenv("TMUX") != "" {
		cmd := exec.Command(tmux.BinaryPath(), "unbind-key", "-n", key)
		if err := cmd.Run(); err != nil {
			fmt.Printf("%s⚠%s Could not unbind in current session: %v\n", colorize(t.Warning), colorize(t.Text), err)
		} else {
			fmt.Printf("%s✓%s Unbound %s in current tmux server\n", colorize(t.Success), colorize(t.Text), key)
		}
	}

	// Remove from tmux.conf
	tmuxConf := filepath.Join(os.Getenv("HOME"), ".tmux.conf")

	data, err := os.ReadFile(tmuxConf)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("%s→%s No tmux.conf found\n", colorize(t.Info), colorize(t.Text))
			return nil
		}
		return err
	}

	// Remove the binding lines (and their preceding NTM comment, if any).
	// Uses look-ahead so we only skip a comment when the NEXT line is the
	// target binding — avoids orphaning comments for other NTM bindings.
	lines := strings.Split(string(data), "\n")
	var newLines []string
	found := false

	isNTMComment := func(s string) bool {
		return strings.Contains(s, "NTM Command Palette") || strings.Contains(s, "NTM Dashboard Overlay")
	}

	for i, line := range lines {
		// Skip NTM comment only if the next line is a binding for our key
		if isNTMComment(line) && i+1 < len(lines) && isBindingLine(lines[i+1], key) {
			continue
		}
		// Skip the binding itself
		if isBindingLine(line, key) {
			found = true
			continue
		}
		newLines = append(newLines, line)
	}

	if !found {
		fmt.Printf("%s→%s No %s binding found in tmux.conf\n", colorize(t.Info), colorize(t.Text), key)
		return nil
	}

	if err := os.WriteFile(tmuxConf, []byte(strings.Join(newLines, "\n")), 0600); err != nil {
		return fmt.Errorf("failed to update tmux.conf: %w", err)
	}

	fmt.Printf("%s✓%s Removed %s binding from %s\n", colorize(t.Success), colorize(t.Text), key, tmuxConf)
	return nil
}

func showBinding(key string) error {
	t := theme.Current()

	// Check tmux.conf
	tmuxConf := filepath.Join(os.Getenv("HOME"), ".tmux.conf")
	data, err := os.ReadFile(tmuxConf)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("%s→%s No tmux.conf found\n", colorize(t.Info), colorize(t.Text))
			return nil
		}
		return err
	}

	lines := strings.Split(string(data), "\n")

	found := false
	for _, line := range lines {
		if isBindingLine(line, key) {
			fmt.Printf("%s✓%s Found binding:\n", colorize(t.Success), colorize(t.Text))
			fmt.Printf("  %s%s%s\n", colorize(t.Primary), line, colorize(t.Text))
			found = true
		}
	}

	if !found {
		fmt.Printf("%s→%s No %s binding found in tmux.conf\n", colorize(t.Info), colorize(t.Text), key)
		fmt.Printf("\n  Run %sntm bind%s to set it up.\n", colorize(t.Primary), colorize(t.Text))
	}

	return nil
}

// isOverlayKeyBound checks if the given key already has an NTM overlay binding
// in tmux.conf in its CURRENT form. Returns false when the binding is absent OR
// when it is a stale pre-#201 binding (missing --inferred); in the stale case
// the auto-setup path then replaces it in place (via isBindingLine), migrating
// it to the lenient current-session resolution.
func isOverlayKeyBound(key string) bool {
	tmuxConf := filepath.Join(os.Getenv("HOME"), ".tmux.conf")
	data, err := os.ReadFile(tmuxConf)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if isOverlayBindingLine(line, key) {
			return true
		}
	}
	return false
}

// isOverlayBindingLine reports whether line is an ntm overlay binding for key in
// its CURRENT form. It requires the "--inferred" marker so a stale pre-#201
// overlay binding (which lacks it) is reported as NOT current — that makes
// isOverlayKeyBound return false and triggers setupOverlayBinding to replace the
// old line in place (found via the broader isBindingLine), so existing F12
// bindings are migrated to the fixed lenient resolution instead of silently
// failing closed for unregistered sessions.
func isOverlayBindingLine(line, key string) bool {
	return isBindingLine(line, key) &&
		strings.Contains(line, "display-popup") &&
		strings.Contains(line, "dashboard --popup") &&
		strings.Contains(line, "--inferred")
}

// isBindingLine checks if a line is a tmux binding for the given key.
// Matches "bind-key -n KEY ..." or "bind -n KEY ..."
func isBindingLine(line, key string) bool {
	fields := strings.Fields(line)
	// Check for "bind-key" or "bind"
	if len(fields) < 3 {
		return false
	}

	cmd := fields[0]
	if cmd != "bind-key" && cmd != "bind" {
		return false
	}

	// Check for -n flag (root table)
	if fields[1] != "-n" {
		return false
	}

	// Check key
	return fields[2] == key
}
