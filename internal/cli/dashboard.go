package cli

import (
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard"
	"github.com/spf13/cobra"
)

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "dashboard [session-name]",
		Aliases: []string{"dash", "d"},
		Short:   "Open interactive session dashboard",
		Long: `Open a stunning interactive dashboard for a tmux session.

The dashboard shows:
- Visual grid of all panes with agent types
- Agent counts (Claude, Codex, Gemini)
- Quick actions for zooming and sending commands

If no session is specified:
- Inside tmux: uses the current session
- Outside tmux: shows a session selector

Examples:
  ntm dashboard myproject
  ntm dash                  # Auto-detect session`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runDashboard(session)
		},
	}
}

func runDashboard(session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Determine target session
	if session == "" {
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
			// Show session selector
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found. Create one with: ntm spawn <name>")
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

	return dashboard.Run(session)
}
