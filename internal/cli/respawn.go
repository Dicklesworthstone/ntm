package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newRespawnCmd() *cobra.Command {
	var force bool
	var panes string
	var agentType string
	var all bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:     "respawn <session>",
		Aliases: []string{"restart"},
		Short:   "Kill and restart worker agents in a session",
		Long: `Kill and restart worker agents in a tmux session.

This command uses tmux's respawn-pane -k to cycle the process in each
selected pane, effectively killing the current agent and starting a
fresh instance with the same command.

By default, only agent panes are restarted (not the user pane at index 0).
Use --all to include all panes, or --panes to target specific indices.

Examples:
  ntm respawn myproject              # Restart all agent panes (prompts for confirmation)
  ntm respawn myproject --force      # No confirmation
  ntm respawn myproject --panes=1,2  # Restart only panes 1 and 2
  ntm respawn myproject --type=cc    # Restart only Claude agents
  ntm respawn myproject --all        # Include user pane (index 0)
  ntm respawn myproject --dry-run    # Preview which panes would be restarted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRespawn(args[0], force, panes, agentType, all, dryRun)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringVarP(&panes, "panes", "p", "", "comma-separated pane indices to restart (e.g., 1,2,3)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "", "filter by agent type (cc, claude, cod, codex, gmi, gemini)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include all panes (including user pane)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview which panes would be restarted")

	return cmd
}

func runRespawn(session string, force bool, panesFlag string, agentType string, all bool, dryRun bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, nil)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Parse pane filter
	var paneFilter []string
	if panesFlag != "" {
		paneFilter = strings.Split(panesFlag, ",")
		for i := range paneFilter {
			paneFilter[i] = strings.TrimSpace(paneFilter[i])
		}
	}

	// Get panes to determine targets
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Build filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range paneFilter {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Determine which panes to restart
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Filter by type if specified
		if agentType != "" {
			detectedType := robot.DetectAgentType(pane.Title)
			targetType := normalizeAgentType(agentType)
			currentType := normalizeAgentType(detectedType)
			if targetType != currentType {
				continue
			}
		}

		// Skip user panes by default unless --all or specific pane filter
		if !all && !hasPaneFilter && agentType == "" {
			detectedType := robot.DetectAgentType(pane.Title)
			if pane.Index == 0 && detectedType == "unknown" {
				continue
			}
			if detectedType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	if len(targetPanes) == 0 {
		fmt.Println("No panes matched the filter criteria.")
		return nil
	}

	// Dry-run mode
	if dryRun {
		fmt.Printf("Would restart %d pane(s) in session '%s':\n", len(targetPanes), session)
		for _, pane := range targetPanes {
			agentType := robot.DetectAgentType(pane.Title)
			fmt.Printf("  - Pane %d: %s (%s)\n", pane.Index, pane.ID, agentType)
		}
		return nil
	}

	// Confirmation
	if !force {
		title := fmt.Sprintf("Restart %d pane(s)?", len(targetPanes))
		desc := fmt.Sprintf("Agents in session '%s' will be restarted with fresh shells.", session)
		if !confirmHuh(title, desc) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Restart targets
	var restarted []string
	var failed []string
	for _, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)
		err := tmux.RespawnPane(pane.ID, true)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", paneKey, err))
		} else {
			restarted = append(restarted, paneKey)
		}
	}

	// Report results
	if len(restarted) > 0 {
		fmt.Printf("Restarted panes: %s\n", strings.Join(restarted, ", "))
	}
	if len(failed) > 0 {
		fmt.Printf("Failed to restart:\n")
		for _, f := range failed {
			fmt.Printf("  - %s\n", f)
		}
		return fmt.Errorf("%d pane(s) failed to restart", len(failed))
	}

	return nil
}

// normalizeAgentType normalizes agent type aliases to canonical form.
func normalizeAgentType(t string) string {
	return robot.ResolveAgentType(t)
}
