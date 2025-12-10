package cli

import (
	"fmt"
	"io"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "view [session-name]",
		Aliases: []string{"v", "tile"},
		Short:   "View all panes in a session (unzoom, tile, attach)",
		Long: `View all panes in a tmux session by:
1. Unzooming any zoomed panes
2. Applying tiled layout to all windows
3. Attaching/switching to the session

If no session is specified:
- If inside tmux, operates on the current session
- Otherwise, shows a session selector

Examples:
  ntm view myproject
  ntm view                 # Select session or use current
  ntm tile myproject       # Alias`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runView(cmd.OutOrStdout(), session)
		},
	}
}

func runView(w io.Writer, session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	interactive := IsInteractive(w)
	t := theme.Current()

	// Determine target session
	if session == "" {
		if !interactive {
			return fmt.Errorf("non-interactive environment: session name is required for view")
		}
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
			// Show session selector
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

	// Apply tiled layout (includes unzoom)
	if err := tmux.ApplyTiledLayout(session); err != nil {
		return fmt.Errorf("failed to apply layout: %w", err)
	}

	fmt.Printf("%s✓%s Tiled layout applied to '%s'\n",
		colorize(t.Success), colorize(t.Text), session)

	// Attach or switch to session
	return tmux.AttachOrSwitch(session)
}
