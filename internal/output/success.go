package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Suggestion represents a "what next" command suggestion
type Suggestion struct {
	Command     string // The command to run (e.g., "ntm attach myproject")
	Description string // Brief description (e.g., "Connect to session")
}

// SuccessFooter prints a "What's next?" section with suggested commands.
// It's designed for post-success output to guide users to their next action.
// Returns without printing if stdout is not a terminal (piped output).
func SuccessFooter(suggestions ...Suggestion) {
	PrintSuccessFooter(os.Stdout, suggestions...)
}

// PrintSuccessFooter prints a "What's next?" footer to the given writer.
// Skips output if w is not a terminal or is piped.
func PrintSuccessFooter(w io.Writer, suggestions ...Suggestion) {
	if len(suggestions) == 0 {
		return
	}

	// Check if output is a terminal (skip for pipes/redirects)
	if f, ok := w.(*os.File); ok {
		if !term.IsTerminal(int(f.Fd())) {
			return
		}
	}

	// Detect colors
	useColor := isStdoutTerminal() && os.Getenv("NO_COLOR") == ""

	fmt.Fprintln(w)

	if useColor {
		t := theme.Current()
		headerStyle := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
		cmdStyle := lipgloss.NewStyle().Foreground(t.Info)
		descStyle := lipgloss.NewStyle().Foreground(t.Overlay)

		fmt.Fprintln(w, headerStyle.Render("What's next?"))
		for _, s := range suggestions {
			fmt.Fprintf(w, "  %s  %s\n",
				cmdStyle.Render(s.Command),
				descStyle.Render("# "+s.Description),
			)
		}
	} else {
		fmt.Fprintln(w, "What's next?")
		for _, s := range suggestions {
			fmt.Fprintf(w, "  %s  # %s\n", s.Command, s.Description)
		}
	}
	fmt.Fprintln(w)
}

// isStdoutTerminal checks if stdout is a terminal
func isStdoutTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Pre-built suggestion sets for common commands

// SpawnSuggestions returns suggestions for after spawning a session
func SpawnSuggestions(session string) []Suggestion {
	return []Suggestion{
		{Command: fmt.Sprintf("ntm attach %s", session), Description: "Connect to session"},
		{Command: fmt.Sprintf("ntm dashboard %s", session), Description: "Live status overview"},
		{Command: "ntm palette", Description: "Quick command launcher"},
	}
}

// QuickSuggestions returns suggestions for after running quick setup
func QuickSuggestions(projectDir, sessionName string) []Suggestion {
	return []Suggestion{
		{Command: fmt.Sprintf("cd %s", projectDir), Description: "Enter project directory"},
		{Command: fmt.Sprintf("ntm spawn %s --cc=2", sessionName), Description: "Start agents"},
	}
}

// AddSuggestions returns suggestions for after adding agents
func AddSuggestions(session string, addedCount int) []Suggestion {
	return []Suggestion{
		{Command: fmt.Sprintf("ntm status %s", session), Description: "Check session status"},
		{Command: fmt.Sprintf("ntm send %s", session), Description: "Send prompt to agents"},
		{Command: fmt.Sprintf("ntm dashboard %s", session), Description: "Live status overview"},
	}
}

// SendSuggestions returns suggestions for after sending a prompt
func SendSuggestions(session string) []Suggestion {
	return []Suggestion{
		{Command: fmt.Sprintf("ntm dashboard %s", session), Description: "Monitor progress"},
		{Command: fmt.Sprintf("ntm attach %s", session), Description: "Connect to session"},
	}
}

// KillSuggestions returns suggestions for after killing a session
func KillSuggestions() []Suggestion {
	return []Suggestion{
		{Command: "ntm list", Description: "View remaining sessions"},
		{Command: "ntm spawn <name>", Description: "Create a new session"},
	}
}

// SuccessCheck prints a success message with a checkmark
func SuccessCheck(msg string) {
	PrintSuccessCheck(os.Stdout, msg)
}

// PrintSuccessCheck prints a success message with a checkmark to the given writer
func PrintSuccessCheck(w io.Writer, msg string) {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}

	if useColor {
		t := theme.Current()
		checkStyle := lipgloss.NewStyle().Foreground(t.Success)
		fmt.Fprintf(w, "%s %s\n", checkStyle.Render("✓"), msg)
	} else {
		fmt.Fprintf(w, "✓ %s\n", msg)
	}
}

// SuccessResponse is the JSON schema for success output with suggestions
type SuccessFooterResponse struct {
	Success     bool       `json:"success"`
	Message     string     `json:"message,omitempty"`
	Suggestions []SuggJSON `json:"suggestions,omitempty"`
}

// SuggJSON is the JSON representation of a suggestion
type SuggJSON struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// NewSuccessWithSuggestions creates a JSON response with suggestions
func NewSuccessWithSuggestions(msg string, suggestions []Suggestion) SuccessFooterResponse {
	resp := SuccessFooterResponse{
		Success: true,
		Message: msg,
	}
	for _, s := range suggestions {
		resp.Suggestions = append(resp.Suggestions, SuggJSON(s))
	}
	return resp
}

// FormatSuggestions formats suggestions as a simple string (for logs/non-interactive)
func FormatSuggestions(suggestions []Suggestion) string {
	if len(suggestions) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "What's next?")
	for _, s := range suggestions {
		lines = append(lines, fmt.Sprintf("  %s  # %s", s.Command, s.Description))
	}
	return strings.Join(lines, "\n")
}
