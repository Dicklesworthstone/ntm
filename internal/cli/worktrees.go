package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func newWorktreesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktrees",
		Short: "Manage Git worktrees for agent isolation",
		Long: `Manage Git worktrees for agent isolation.

Worktrees allow multiple agents to work in parallel on different branches
without interfering with each other. Each agent gets its own working
directory with its own branch.`,
	}

	cmd.AddCommand(
		newWorktreesListCmd(),
		newWorktreesMergeCmd(),
		newWorktreesCleanCmd(),
		newWorktreesRemoveCmd(),
	)

	return cmd
}

func newWorktreesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all worktrees for the current session",
		Long:  `List all Git worktrees created for agents in the current session.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}

			session := tmux.GetCurrentSession()
			if session == "" {
				session = filepath.Base(dir)
			}

			manager := worktrees.NewManager(dir, session)
			worktreeList, err := manager.ListWorktrees()
			if err != nil {
				return fmt.Errorf("failed to list worktrees: %w", err)
			}

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"session":   session,
					"worktrees": worktreeList,
					"total":     len(worktreeList),
				})
			}

			if len(worktreeList) == 0 {
				fmt.Printf("No worktrees found for session: %s\n", session)
				return nil
			}

			fmt.Printf("Worktrees for session: %s\n\n", session)
			for _, wt := range worktreeList {
				status := "✓ Active"
				if wt.Error != "" {
					status = "✗ " + wt.Error
				}

				fmt.Printf("Agent: %s\n", wt.AgentName)
				fmt.Printf("  Path:   %s\n", wt.Path)
				fmt.Printf("  Branch: %s\n", wt.BranchName)
				fmt.Printf("  Status: %s\n", status)
				fmt.Println()
			}

			return nil
		},
	}
}

func newWorktreesMergeCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "merge <agent-name>",
		Short: "Merge an agent's worktree back to main branch",
		Long: `Merge changes from an agent's worktree branch back to the main branch.

This will switch to the main branch and merge the agent's branch using
a non-fast-forward merge to preserve the merge history.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]

			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}

			session := tmux.GetCurrentSession()
			if session == "" {
				session = filepath.Base(dir)
			}

			manager := worktrees.NewManager(dir, session)

			// Check if worktree exists
			info, err := manager.GetWorktreeForAgent(agentName)
			if err != nil {
				return fmt.Errorf("failed to get worktree info: %w", err)
			}

			if !info.Created {
				return fmt.Errorf("no worktree found for agent: %s", agentName)
			}

			if info.Error != "" && !force {
				return fmt.Errorf("worktree has errors: %s (use --force to proceed)", info.Error)
			}

			// Perform the merge
			if err := manager.MergeBack(agentName); err != nil {
				return fmt.Errorf("failed to merge worktree: %w", err)
			}

			fmt.Printf("Successfully merged agent %s's work from branch %s\n", agentName, info.BranchName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force merge even if worktree has errors")
	return cmd
}

func newWorktreesCleanCmd() *cobra.Command {
	var (
		sessionName string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Clean up worktrees for a session",
		Long: `Remove all worktrees and branches for the specified session.

By default, cleans up the current session. Use --session to specify
a different session.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}

			session := sessionName
			if session == "" {
				session = tmux.GetCurrentSession()
				if session == "" {
					session = filepath.Base(dir)
				}
			}

			manager := worktrees.NewManager(dir, session)

			// List worktrees first to show what will be removed
			worktreeList, err := manager.ListWorktrees()
			if err != nil {
				return fmt.Errorf("failed to list worktrees: %w", err)
			}

			if len(worktreeList) == 0 {
				fmt.Printf("No worktrees found for session: %s\n", session)
				return nil
			}

			if !force {
				fmt.Printf("The following worktrees will be removed for session %s:\n", session)
				for _, wt := range worktreeList {
					fmt.Printf("  - %s (%s)\n", wt.AgentName, wt.Path)
				}
				fmt.Print("\nAre you sure? (y/N): ")

				var response string
				fmt.Scanln(&response)
				if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
					fmt.Println("Cleanup cancelled.")
					return nil
				}
			}

			// Perform cleanup
			if err := manager.Cleanup(); err != nil {
				return fmt.Errorf("failed to cleanup worktrees: %w", err)
			}

			fmt.Printf("Successfully cleaned up %d worktrees for session: %s\n", len(worktreeList), session)
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionName, "session", "", "Session name to clean up (defaults to current session)")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

func newWorktreesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <agent-name>",
		Short: "Remove a specific agent's worktree",
		Long: `Remove the worktree and branch for a specific agent.

This will remove the worktree directory and delete the associated branch.
Use with caution as this will permanently delete any uncommitted changes
in the worktree.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]

			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}

			session := tmux.GetCurrentSession()
			if session == "" {
				session = filepath.Base(dir)
			}

			manager := worktrees.NewManager(dir, session)

			// Check if worktree exists
			info, err := manager.GetWorktreeForAgent(agentName)
			if err != nil {
				return fmt.Errorf("failed to get worktree info: %w", err)
			}

			if !info.Created {
				return fmt.Errorf("no worktree found for agent: %s", agentName)
			}

			// Remove the worktree
			if err := manager.RemoveWorktree(agentName); err != nil {
				return fmt.Errorf("failed to remove worktree: %w", err)
			}

			fmt.Printf("Successfully removed worktree for agent: %s\n", agentName)
			return nil
		},
	}
}