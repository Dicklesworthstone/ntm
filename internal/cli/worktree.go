package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func isWorktreesJSONOutput(outputFormat string) (bool, error) {
	if IsJSONOutput() {
		return true, nil
	}
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "", "table", "text":
		return false, nil
	case "json":
		return true, nil
	default:
		return false, fmt.Errorf("invalid output format %q: must be table or json", outputFormat)
	}
}

func newWorktreesCreateCmd() *cobra.Command {
	var (
		sessionName  string
		outputFormat string
	)

	cmd := &cobra.Command{
		Use:     "create <agent-name> [agent-name...]",
		Aliases: []string{"provision"},
		Short:   "Create Git worktrees for one or more agents",
		Long: `Create isolated Git worktrees for one or more agents in a session.

Each agent worktree is created under .ntm/worktrees/<session>/<agent> on
branch ntm/<session>/<agent>.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, err := isWorktreesJSONOutput(outputFormat)
			if err != nil {
				return err
			}
			dir, session, err := resolveWorktreeScopeWithJSON(cmd.Context(), sessionName, jsonMode)
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)
			created := make([]*worktrees.WorktreeInfo, 0, len(args))
			for _, agentName := range args {
				info, createErr := manager.CreateForAgent(cmd.Context(), agentName)
				if createErr != nil {
					return fmt.Errorf("failed to create worktree for agent %s: %w", agentName, createErr)
				}
				created = append(created, info)
			}

			if jsonMode {
				return output.PrintJSON(map[string]interface{}{
					"success":   true,
					"session":   session,
					"worktrees": created,
					"total":     len(created),
				})
			}

			for _, info := range created {
				action := "Reused existing worktree"
				if info.Created {
					action = "Created worktree"
				}
				cmd.Printf("%s for agent %s:\n", action, info.AgentName)
				cmd.Printf("  Path:   %s\n", info.Path)
				cmd.Printf("  Branch: %s\n", info.BranchName)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionName, "session", "", "Session owning the agent worktrees (defaults to current session)")
	cmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table or json")
	return cmd
}

func newWorktreesPruneCmd() *cobra.Command {
	var (
		sessionName  string
		dryRun       bool
		outputFormat string
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune stale Git worktree administrative files",
		Long: `Remove stale Git worktree administrative records whose working directories
no longer exist on disk. Use --dry-run to preview prunable entries.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonMode, err := isWorktreesJSONOutput(outputFormat)
			if err != nil {
				return err
			}
			dir, session, err := resolveWorktreeScopeWithJSON(cmd.Context(), sessionName, jsonMode)
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)
			report, err := manager.Prune(cmd.Context(), dryRun)
			if err != nil {
				return err
			}

			if jsonMode {
				return output.PrintJSON(map[string]interface{}{
					"success": true,
					"session": session,
					"dry_run": dryRun,
					"output":  report,
				})
			}

			if report != "" {
				cmd.Println(report)
			} else if dryRun {
				cmd.Println("No stale worktree metadata to prune.")
			} else {
				cmd.Println("Pruned stale worktree metadata.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionName, "session", "", "Resolve the repository from this session (defaults to current directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview stale worktree entries without pruning")
	cmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table or json")
	return cmd
}
