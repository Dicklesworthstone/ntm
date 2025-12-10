package cli

import (
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func newPaletteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "palette [session]",
		Short: "Open the interactive command palette",
		Long: `Open an interactive TUI to select and send pre-configured prompts to agents.

The palette shows all commands defined in your config file, organized by category.
Filter by typing, select with Enter, then choose the target agents.

If no session is specified and you're inside tmux, uses the current session.

Examples:
  ntm palette myproject  # Open palette for specific session
  ntm palette            # Use current tmux session`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runPalette(session)
		},
	}
}

func runPalette(session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Auto-detect session if not specified
	if session == "" {
		session = tmux.GetCurrentSession()
		if session == "" {
			// Not in tmux, show enhanced session selector
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}

			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found - create one with 'ntm spawn'")
			}

			if len(sessions) == 1 {
				session = sessions[0].Name
			} else {
				// Use session selector
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
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Check that we have commands
	if len(cfg.Palette) == 0 {
		return fmt.Errorf("no palette commands configured - run 'ntm config init' first")
	}

	// Create and run the TUI palette
	model := palette.New(session, cfg.Palette)
	p := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("running palette: %w", err)
	}

	// Check result
	m := finalModel.(palette.Model)
	sent, err := m.Result()
	if err != nil {
		return err
	}

	if !sent {
		// User cancelled
		return nil
	}

	return nil
}
