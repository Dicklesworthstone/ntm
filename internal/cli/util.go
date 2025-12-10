package cli

import (
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// parseEditorCommand splits the editor string into command and arguments.
// It handles simple spaces.
func parseEditorCommand(editor string) (string, []string) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// IsInteractive returns true when the writer is a terminal.
// The pane/session selectors rely on user input; in tests or piped execution they should not run.
func IsInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
