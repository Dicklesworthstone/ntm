package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newCopyCmd() *cobra.Command {
	var (
		lines   int
		allFlag bool
		ccFlag  bool
		codFlag bool
		gmiFlag bool
	)

	cmd := &cobra.Command{
		Use:     "copy [session-name]",
		Aliases: []string{"cp", "yank"},
		Short:   "Copy pane output to clipboard",
		Long: `Copy the output from one or more panes to the system clipboard.

By default, captures the last 1000 lines from each pane.
Use filters to target specific agent types.

Examples:
  ntm copy myproject            # Copy from current/selected pane
  ntm copy myproject --all      # Copy from all panes
  ntm copy myproject --cc       # Copy from Claude panes only
  ntm copy myproject -l 500     # Copy last 500 lines`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			filter := AgentFilter{
				All:    allFlag,
				Claude: ccFlag,
				Codex:  codFlag,
				Gemini: gmiFlag,
			}

			return runCopy(cmd.OutOrStdout(), session, lines, filter)
		},
	}

	cmd.Flags().IntVarP(&lines, "lines", "l", 1000, "Number of lines to capture")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Copy from all panes")
	cmd.Flags().BoolVar(&ccFlag, "cc", false, "Copy from Claude panes")
	cmd.Flags().BoolVar(&codFlag, "cod", false, "Copy from Codex panes")
	cmd.Flags().BoolVar(&gmiFlag, "gmi", false, "Copy from Gemini panes")

	return cmd
}

// AgentFilter specifies which agent types to target
type AgentFilter struct {
	All    bool
	Claude bool
	Codex  bool
	Gemini bool
}

func (f AgentFilter) IsEmpty() bool {
	return !f.All && !f.Claude && !f.Codex && !f.Gemini
}

func (f AgentFilter) Matches(agentType tmux.AgentType) bool {
	if f.All {
		return true
	}
	switch agentType {
	case tmux.AgentClaude:
		return f.Claude
	case tmux.AgentCodex:
		return f.Codex
	case tmux.AgentGemini:
		return f.Gemini
	default:
		return false
	}
}

func runCopy(w io.Writer, session string, lines int, filter AgentFilter) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	t := theme.Current()

	// Determine target session
	if session == "" {
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
			if !IsInteractive(w) {
				return fmt.Errorf("non-interactive environment: session name is required")
			}
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found")
			}

			selected, err := palette.RunSessionSelector(sessions)
			if err != nil {
				return err
			}
			if selected == "" {
				return nil
			}
			session = selected
		}
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	// Filter panes
	var targetPanes []tmux.Pane
	if filter.IsEmpty() {
		// No filter: copy from active pane or first pane
		for _, p := range panes {
			if p.Active {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 && len(panes) > 0 {
			targetPanes = []tmux.Pane{panes[0]}
		}
	} else {
		for _, p := range panes {
			if filter.Matches(p.Type) {
				targetPanes = append(targetPanes, p)
			}
		}
	}

	if len(targetPanes) == 0 {
		return fmt.Errorf("no matching panes found")
	}

	// Capture output from all target panes
	var outputs []string
	for _, p := range targetPanes {
		output, err := tmux.CapturePaneOutput(p.ID, lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to capture pane %d: %v\n", p.Index, err)
			continue
		}

		// Add header for each pane
		header := fmt.Sprintf("═══ %s (pane %d) ═══", p.Title, p.Index)
		outputs = append(outputs, header, output, "")
	}

	combined := strings.Join(outputs, "\n")

	// Copy to clipboard
	if err := copyToClipboard(combined); err != nil {
		return fmt.Errorf("failed to copy to clipboard: %w", err)
	}

	lineCount := strings.Count(combined, "\n")
	fmt.Printf("%s✓%s Copied %d lines from %d pane(s) to clipboard\n",
		colorize(t.Success), colorize(t.Text), lineCount, len(targetPanes))

	return nil
}

// copyToClipboard copies text to the system clipboard
func copyToClipboard(text string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		// Try xclip first, then xsel
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else if _, err := exec.LookPath("wl-copy"); err == nil {
			// Wayland support
			cmd = exec.Command("wl-copy")
		} else {
			return fmt.Errorf("no clipboard utility found (install xclip, xsel, or wl-copy)")
		}
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}

	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
