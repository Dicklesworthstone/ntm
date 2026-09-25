package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent"
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

This command uses tmux's respawn-pane -k to kill each selected pane's
process and restore a fresh shell, then relaunches the pane's agent CLI
(agent CLIs are started by keystroke after spawn, so a bare respawn
would otherwise leave an empty shell) and waits for it to become ready.

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
			return runRespawn(cmd.Context(), args[0], force, panes, agentType, all, dryRun)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringVarP(&panes, "panes", "p", "", "comma-separated pane indices to restart (e.g., 1,2,3)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "", "filter by agent type (cc, claude, cod, codex, gmi, gemini, agy, antigravity, grok, grok-build, omp, oh-my-pi)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include all panes (including user pane)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview which panes would be restarted")

	return cmd
}

func runRespawn(ctx context.Context, session string, force bool, panesFlag string, agentType string, all bool, dryRun bool) error {
	if ctx == nil {
		return fmt.Errorf("respawn context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSessionWithOptionsContext(ctx, session, nil, SessionResolveOptions{})
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		return fmt.Errorf("check session %q: %w", session, err)
	}
	if !exists {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Parse pane filter
	var paneFilter []string
	if panesFlag != "" {
		paneFilter = strings.Split(panesFlag, ",")
		for i := range paneFilter {
			paneFilter[i] = strings.TrimSpace(paneFilter[i])
			// Reject malformed selectors before the confirmation prompt; the
			// restart engine refuses them anyway.
			if _, err := tmux.ParsePaneSelector(paneFilter[i]); err != nil {
				return fmt.Errorf("--panes: %w", err)
			}
		}
	}

	// Get panes to determine targets
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Build filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range paneFilter {
		paneFilterMap[p] = true
	}
	targetPanes := selectRespawnTargets(panes, paneFilterMap, agentType, all)

	if len(targetPanes) == 0 {
		fmt.Println("No panes matched the filter criteria.")
		return nil
	}
	if err := validateRespawnTargets(targetPanes); err != nil {
		return err
	}

	// Dry-run mode
	if dryRun {
		fmt.Printf("Would restart %d pane(s) in session '%s':\n", len(targetPanes), session)
		for _, pane := range targetPanes {
			agentType := respawnPaneAgentType(pane)
			fmt.Printf("  - Pane %d: %s (%s)\n", pane.Index, pane.ID, agentType)
		}
		return nil
	}

	// Confirmation
	if !force {
		title := fmt.Sprintf("Restart %d pane(s)?", len(targetPanes))
		desc := fmt.Sprintf("Agents in session '%s' will be killed and relaunched.", session)
		if !confirmHuh(title, desc) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Restart targets via the shared robot engine, which relaunches agent
	// CLIs after the respawn and ready-gates them (#187) — respawn-pane -k
	// alone only restores the pane's default command (the login shell).
	fmt.Printf("Restarting %d pane(s) (relaunching agent CLIs)...\n", len(targetPanes))
	out, err := robot.GetRestartPaneContext(ctx, robot.RestartPaneOptions{
		Session: session,
		Panes:   paneFilter,
		Type:    agentType,
		All:     all,
		Config:  cfg,
	})
	if err != nil {
		return err
	}
	if out == nil {
		return respawnResultError(nil)
	}
	resultErr := respawnResultError(out)

	// Report results
	if len(out.Restarted) > 0 {
		fmt.Printf("Restarted panes: %s\n", strings.Join(out.Restarted, ", "))
		for _, paneKey := range out.Restarted {
			if status, ok := respawnRelaunchDisplayStatus(out, paneKey); ok {
				fmt.Printf("  - Pane %s: %s\n", paneKey, status)
			}
		}
	}
	if len(out.Failed) > 0 {
		fmt.Printf("Failed to restart:\n")
		for _, f := range out.Failed {
			fmt.Printf("  - %s: %s\n", f.Pane, f.Reason)
		}
		if resultErr == nil {
			resultErr = fmt.Errorf("%d pane(s) failed to restart cleanly", len(out.Failed))
		}
	}

	return resultErr
}

func respawnRelaunchDisplayStatus(out *robot.RestartPaneOutput, paneKey string) (string, bool) {
	if out == nil {
		return "", false
	}
	if status, ok := out.AgentRelaunchStatus[paneKey]; ok {
		switch status {
		case robot.RestartAgentRelaunchReady:
			return "agent relaunched and ready", true
		case robot.RestartAgentRelaunchNotReady:
			if out.ProcessAlive[paneKey] {
				return "agent process is alive but not ready", true
			}
			return "agent did not relaunch", true
		case robot.RestartAgentRelaunchUnknown:
			if out.ProcessAlive[paneKey] {
				return "agent relaunch status UNKNOWN (live child observed; inspect pane before retrying)", true
			}
			return "agent relaunch status UNKNOWN (inspect pane before retrying)", true
		case robot.RestartAgentRelaunchFailed:
			return "agent relaunch FAILED", true
		default:
			return fmt.Sprintf("agent relaunch status %q (inspect pane)", status), true
		}
	}
	if relaunched, ok := out.AgentRelaunched[paneKey]; ok {
		if relaunched {
			return "agent relaunched", true
		}
		return "agent relaunch FAILED (pane left at a shell)", true
	}
	return "", false
}

func validateRespawnTargets(panes []tmux.Pane) error {
	for _, pane := range panes {
		agentType := respawnPaneAgentType(pane)
		if err := agent.AgentType(agentType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("pane %d (%s): %w", pane.Index, agentType, err)
		}
	}
	return nil
}

func respawnResultError(out *robot.RestartPaneOutput) error {
	if out == nil {
		return fmt.Errorf("respawn failed: empty restart response")
	}
	if out.Success {
		return nil
	}
	if out.ErrorCode != "" {
		return fmt.Errorf("respawn unavailable (%s): %s", out.ErrorCode, out.Error)
	}
	return fmt.Errorf("respawn failed: %s", out.Error)
}

func selectRespawnTargets(panes []tmux.Pane, paneFilterMap map[string]bool, agentType string, all bool) []tmux.Pane {
	hasPaneFilter := len(paneFilterMap) > 0
	targetType := normalizeAgentType(agentType)

	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		currentType := respawnPaneAgentType(pane)
		if targetType != "" && targetType != currentType {
			continue
		}

		// By default only restart agent panes. Explicit pane filters and --all opt out.
		if !all && !hasPaneFilter && targetType == "" {
			if pane.Index == 0 && currentType == "unknown" {
				continue
			}
			if currentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	return targetPanes
}

func respawnPaneAgentType(pane tmux.Pane) string {
	if resolved := normalizeAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return normalizeAgentType(robot.DetectAgentType(pane.Title))
}

// normalizeAgentType normalizes agent type aliases to canonical form.
func normalizeAgentType(t string) string {
	return robot.ResolveAgentType(t)
}
