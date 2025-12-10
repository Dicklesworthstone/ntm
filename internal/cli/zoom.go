package cli

import (
	"fmt"
	"strconv"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newZoomCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "zoom [session-name] [pane-index]",
		Aliases: []string{"z"},
		Short:   "Zoom a specific pane in a session",
		Long: `Zoom a specific pane in a tmux session and attach/switch to it.

If no session is specified:
- If inside tmux, operates on the current session
- Otherwise, shows a session selector

If no pane index is specified, shows a pane selector.

Examples:
  ntm zoom myproject 0      # Zoom pane 0
  ntm zoom myproject        # Select pane to zoom
  ntm zoom                  # Select session and pane`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			var paneIdx int = -1

			if len(args) >= 1 {
				session = args[0]
			}
			if len(args) >= 2 {
				idx, err := strconv.Atoi(args[1])
				if err != nil {
					return fmt.Errorf("invalid pane index: %s", args[1])
				}
				paneIdx = idx
			}

			return runZoom(session, paneIdx)
		},
	}

	return cmd
}

func runZoom(session string, paneIdx int) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	interactive := isInteractive()
	t := theme.Current()

	// Determine target session
	if session == "" {
		if !interactive {
			return fmt.Errorf("non-interactive environment: session name is required for zoom")
		}
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
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
				return nil // User cancelled
			}
			session = selected
		}
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// If no pane specified, let user select
	if paneIdx < 0 {
		if !interactive {
			return fmt.Errorf("non-interactive environment: pane index is required for zoom")
		}
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return err
		}
		if len(panes) == 0 {
			return fmt.Errorf("no panes found in session '%s'", session)
		}

		// Show pane selector
		selected, err := runPaneSelector(session, panes)
		if err != nil {
			return err
		}
		if selected < 0 {
			return nil // User cancelled
		}
		paneIdx = selected
	}

	// Zoom the pane
	if err := tmux.ZoomPane(session, paneIdx); err != nil {
		return fmt.Errorf("failed to zoom pane: %w", err)
	}

	fmt.Printf("%s✓%s Zoomed pane %d in '%s'\n",
		colorize(t.Success), colorize(t.Text), paneIdx, session)

	// Attach or switch to session
	return tmux.AttachOrSwitch(session)
}

// runPaneSelector shows a simple pane selector and returns the selected pane index
func runPaneSelector(session string, panes []tmux.Pane) (int, error) {
	t := theme.Current()

	if len(panes) == 0 {
		return -1, fmt.Errorf("no panes available")
	}

	// For now, use a simple numbered list
	fmt.Printf("\n%sSelect pane to zoom:%s\n\n", "\033[1m", "\033[0m")

	for i, p := range panes {
		typeIcon := ""
		typeColor := ""
		switch p.Type {
		case tmux.AgentClaude:
			typeIcon = "󰗣"
			typeColor = fmt.Sprintf("\033[38;2;%s", colorToRGB(t.Claude))
		case tmux.AgentCodex:
			typeIcon = ""
			typeColor = fmt.Sprintf("\033[38;2;%s", colorToRGB(t.Codex))
		case tmux.AgentGemini:
			typeIcon = "󰊤"
			typeColor = fmt.Sprintf("\033[38;2;%s", colorToRGB(t.Gemini))
		default:
			typeIcon = ""
			typeColor = fmt.Sprintf("\033[38;2;%s", colorToRGB(t.Green))
		}

		num := i + 1
		if num <= 9 {
			fmt.Printf("  %s%d%s %s%s %s%s (%s)\n",
				"\033[38;5;245m", num, "\033[0m",
				typeColor, typeIcon, "\033[0m",
				p.Title, p.Command)
		}
	}

	fmt.Print("\nEnter number (or q to cancel): ")
	var input string
	fmt.Scanln(&input)

	if input == "q" || input == "" {
		return -1, nil
	}

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(panes) {
		return -1, fmt.Errorf("invalid selection")
	}

	return panes[idx-1].Index, nil
}
