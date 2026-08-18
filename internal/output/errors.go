package output

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// CLIError represents a structured CLI error with remediation hints.
type CLIError struct {
	Message string // What failed
	Cause   string // Why it failed (optional)
	Hint    string // Fastest command/action to fix it (optional)
	Code    string // Error code for programmatic handling (optional)
}

// Error implements the error interface.
func (e *CLIError) Error() string {
	return e.Message
}

// NewCLIError creates a new CLI error with just a message.
func NewCLIError(msg string) *CLIError {
	return &CLIError{Message: msg}
}

// WithCause adds a cause to the error.
func (e *CLIError) WithCause(cause string) *CLIError {
	e.Cause = cause
	return e
}

// WithHint adds a remediation hint to the error.
func (e *CLIError) WithHint(hint string) *CLIError {
	e.Hint = hint
	return e
}

// WithCode adds an error code to the error.
func (e *CLIError) WithCode(code string) *CLIError {
	e.Code = code
	return e
}

// isStderrTerminal checks if stderr is a terminal (for color output).
func isStderrTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// FormatCLIError formats a CLIError for terminal output with colors.
// Returns plain text if stderr is not a terminal or NO_COLOR is set.
func FormatCLIError(e *CLIError) string {
	useColor := isStderrTerminal() && os.Getenv("NO_COLOR") == ""

	var sb strings.Builder

	if useColor {
		t := theme.Current()
		errorStyle := lipgloss.NewStyle().Foreground(t.Error).Bold(true)
		causeStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		hintStyle := lipgloss.NewStyle().Foreground(t.Info)
		codeStyle := lipgloss.NewStyle().Foreground(t.Overlay)

		// Error message (red, bold)
		sb.WriteString(errorStyle.Render("Error: "))
		sb.WriteString(e.Message)

		// Error code if present
		if e.Code != "" {
			sb.WriteString(" ")
			sb.WriteString(codeStyle.Render("[" + e.Code + "]"))
		}
		sb.WriteString("\n")

		// Cause if present
		if e.Cause != "" {
			sb.WriteString(causeStyle.Render("  Cause: "))
			sb.WriteString(e.Cause)
			sb.WriteString("\n")
		}

		// Hint if present
		if e.Hint != "" {
			sb.WriteString(hintStyle.Render("  Hint: "))
			sb.WriteString(e.Hint)
			sb.WriteString("\n")
		}
	} else {
		// Plain text output (no colors)
		sb.WriteString("Error: ")
		sb.WriteString(e.Message)
		if e.Code != "" {
			sb.WriteString(" [")
			sb.WriteString(e.Code)
			sb.WriteString("]")
		}
		sb.WriteString("\n")

		if e.Cause != "" {
			sb.WriteString("  Cause: ")
			sb.WriteString(e.Cause)
			sb.WriteString("\n")
		}

		if e.Hint != "" {
			sb.WriteString("  Hint: ")
			sb.WriteString(e.Hint)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// PrintCLIError prints a CLIError to stderr with formatting.
func PrintCLIError(e *CLIError) {
	fmt.Fprint(os.Stderr, FormatCLIError(e))
}

// Common error hints for frequent scenarios
var (
	// Session errors
	HintSessionNotFound  = "Run 'ntm list' to see available sessions, or 'ntm spawn <name>' to create one"
	HintSessionExists    = "Use a different name, or 'ntm kill <name>' to remove the existing session"
	HintNoSessions       = "Run 'ntm spawn <name>' to create a new session"
	HintSessionInference = "Specify a session name explicitly, e.g., 'ntm status <session>'"

	// Tmux errors
	HintTmuxNotInstalled = "Install tmux: brew install tmux (macOS) or apt install tmux (Linux)"
	HintTmuxNotRunning   = "Start tmux server with 'tmux new-session -d' or run ntm spawn"
	HintNotInTmux        = "Run this command from within a tmux session, or specify --session"

	// Config errors
	HintConfigNotFound = "Run 'ntm config init' to create a default configuration"
	HintConfigInvalid  = "Check config syntax with 'ntm config show' or edit ~/.config/ntm/config.toml"

	// Agent errors
	HintAgentNotInstalled = "Install the agent CLI tool first (claude, codex-cli, or gemini-cli)"
	HintAgentRateLimit    = "Wait a few minutes or switch to a different API key/account"

	// Pane errors
	HintPaneNotFound  = "Run 'ntm status <session>' to see available panes"
	HintNoPanes       = "Add agents with 'ntm add <session> --cc=1' or '--cod=1'"
	HintPaneIndexHelp = "Pane indices start at 0; use 'ntm status' to see valid indices"

	// Permission errors
	HintPermissionDenied = "Check file permissions or run with appropriate privileges"
)

// SessionNotFoundError creates a session not found error with hint
func SessionNotFoundError(session string) *CLIError {
	return NewCLIError(fmt.Sprintf("session '%s' not found", session)).
		WithCode("SESSION_NOT_FOUND").
		WithHint(HintSessionNotFound)
}
