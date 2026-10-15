package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
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
			dir, session, err := resolveWorktreeScope(cmd.Context(), "")
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)
			worktreeList, err := manager.ListWorktrees(cmd.Context())
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
	var dryRun bool
	var sessionName string
	var expectedSource, expectedTarget string

	cmd := &cobra.Command{
		Use:   "merge <agent-name>",
		Short: "Safely integrate committed agent work into the default branch",
		Long: `Merge committed agent work into the repository's resolved default branch.

Both checkouts must be clean and the target must already have the default
branch checked out. Conflicts, pending Git operations, and ignored-file
overwrite risks are refused before mutation. No branch switching, stashing,
automatic conflict resolution, or force override is performed.

Use --dry-run to evaluate the real three-way merge without changing either
checkout or index. The preview reports source/target commit IDs, conflicting
paths, and blockers. In JSON mode inspect can_merge; a successful preview is
an assessment, not confirmation that work was merged. Git 2.38+ is required.

For an apply bound to reviewed commits, supply --expected-source and
--expected-target using the complete IDs from the preview. Both are required.
Without them, apply assesses the current tips. Successful integration creates
a two-parent merge; already-merged work is a no-op. If Git or a hook fails after
mutation starts, inspect git status before retrying; NTM never resets the work.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("expected-source") || cmd.Flags().Changed("expected-target") {
				if dryRun {
					return fmt.Errorf("expected commit IDs apply to a merge, not --dry-run")
				}
				if expectedSource == "" || expectedTarget == "" {
					return fmt.Errorf("--expected-source and --expected-target must both contain complete preview commit IDs")
				}
			}
			agentName := args[0]
			dir, session, err := resolveWorktreeScope(cmd.Context(), sessionName)
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)
			if dryRun {
				plan, err := manager.PreviewMerge(cmd.Context(), agentName)
				if err != nil {
					return fmt.Errorf("failed to preview merge: %w", err)
				}
				if IsJSONOutput() {
					return output.PrintJSON(plan)
				}
				cmd.Printf("Merge preview: %s -> %s\nSource: %s\nTarget: %s\nCan merge: %t\nAlready merged: %t\n",
					plan.SourceBranch, plan.TargetBranch, plan.SourceCommit, plan.TargetCommit, plan.CanMerge, plan.AlreadyMerged)
				for _, blocker := range plan.Blockers {
					cmd.Printf("  Blocker: %s\n", blocker)
				}
				for _, path := range plan.Conflicts {
					cmd.Printf("  Conflict: %q\n", path)
				}
				for _, path := range plan.OverwriteRisks {
					cmd.Printf("  Ignored file at risk: %q\n", path)
				}
				return nil
			}

			// Check if worktree exists
			info, err := manager.GetWorktreeForAgent(cmd.Context(), agentName)
			if err != nil {
				return fmt.Errorf("failed to get worktree info: %w", err)
			}

			if !info.Created {
				return fmt.Errorf("no worktree found for agent: %s", agentName)
			}

			if info.Error != "" {
				return fmt.Errorf("worktree has errors: %s", info.Error)
			}

			// Perform the merge
			if err := manager.MergeBack(cmd.Context(), agentName, worktrees.MergeOptions{
				ExpectedSource: expectedSource, ExpectedTarget: expectedTarget,
			}); err != nil {
				return fmt.Errorf("failed to merge worktree: %w", err)
			}

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"success": true, "session": session, "agent_name": info.AgentName, "source_branch": info.BranchName,
				})
			}
			cmd.Printf("Agent %s's committed work from branch %s is integrated\n", info.AgentName, info.BranchName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview the merge without changing checkout files, indexes, or branch refs")
	cmd.Flags().StringVar(&sessionName, "session", "", "Session owning the agent worktree (defaults to current session)")
	cmd.Flags().StringVar(&expectedSource, "expected-source", "", "Require the complete source commit ID from a reviewed merge preview")
	cmd.Flags().StringVar(&expectedTarget, "expected-target", "", "Require the complete target commit ID from the same merge preview")
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
			dir, session, err := resolveWorktreeScope(cmd.Context(), sessionName)
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)

			// List worktrees first to show what will be removed
			worktreeList, err := manager.ListWorktrees(cmd.Context())
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
			if err := manager.Cleanup(cmd.Context()); err != nil {
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
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <agent-name>",
		Short: "Remove a specific agent's worktree",
		Long: `Remove the worktree and branch for a specific agent.

This will remove the worktree directory and delete the associated branch.
Use with caution as this will permanently delete any uncommitted changes
in the worktree.

Prompts for confirmation, reporting the uncommitted changes and unmerged
commits that would be destroyed. Pass --force to skip the prompt.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]
			dir, session, err := resolveWorktreeScope(cmd.Context(), "")
			if err != nil {
				return err
			}

			manager := worktrees.NewManager(dir, session)

			// Check if worktree exists
			info, err := manager.GetWorktreeForAgent(cmd.Context(), agentName)
			if err != nil {
				return fmt.Errorf("failed to get worktree info: %w", err)
			}

			if !info.Created {
				return fmt.Errorf("no worktree found for agent: %s", agentName)
			}

			// Removal runs `git worktree remove --force` and `git branch -D`, so
			// nothing downstream will refuse on dirty or unmerged work. The sibling
			// `worktrees clean` has always confirmed before destroying many
			// worktrees; this destroys one just as permanently.
			if !force {
				// Never prompt into a JSON stream: a half-written question would
				// corrupt the caller's output, and answering for them is worse.
				if IsJSONOutput() {
					return fmt.Errorf("refusing to remove worktree for agent %s without confirmation in JSON mode: re-run with --force", agentName)
				}
				risk, riskErr := manager.AssessRemovalRisk(cmd.Context(), agentName)
				title := fmt.Sprintf("Remove worktree for agent '%s'?", agentName)
				if !confirmHuhDestructive(title, describeWorktreeRemoval(info, risk, riskErr)) {
					fmt.Println("Removal cancelled.")
					return nil
				}
			}

			// Remove the worktree
			if err := manager.RemoveWorktree(cmd.Context(), agentName); err != nil {
				return fmt.Errorf("failed to remove worktree: %w", err)
			}

			fmt.Printf("Successfully removed worktree for agent: %s\n", agentName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

// describeWorktreeRemoval renders the confirmation body for `worktrees remove`.
//
// A failed assessment is reported as unknown rather than as "nothing at risk":
// treating an inspection error as zero work is how a prompt talks an operator
// into deleting something it never managed to look at.
func describeWorktreeRemoval(info *worktrees.WorktreeInfo, risk worktrees.RemovalRisk, riskErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Path:   %s\n", info.Path)
	fmt.Fprintf(&b, "Branch: %s\n\n", info.BranchName)

	switch {
	case riskErr != nil:
		fmt.Fprintf(&b, "Could not determine what would be lost: %v\n", riskErr)
		b.WriteString("The worktree is deleted and the branch force-deleted regardless.")
	case !risk.HasWork():
		b.WriteString("No uncommitted changes and no unmerged commits detected.")
	default:
		b.WriteString("This permanently destroys:\n")
		if risk.UncommittedFiles > 0 {
			fmt.Fprintf(&b, "  - %s with uncommitted changes\n", countNoun(risk.UncommittedFiles, "file", "files"))
		}
		if risk.UnmergedCommits > 0 {
			fmt.Fprintf(&b, "  - %s not merged into the default branch\n", countNoun(risk.UnmergedCommits, "commit", "commits"))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// countNoun renders "1 file" / "3 files".
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func resolveWorktreeScope(ctx context.Context, session string) (string, string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
		if err != nil {
			return "", "", err
		}
		session = resolved
	}
	if session == "" {
		projectDir := GetProjectRoot()
		if projectDir == "" {
			return "", "", fmt.Errorf("getting project root failed")
		}
		return projectDir, defaultProjectScopedSession(projectDir), nil
	}

	projectDir, err := resolveExplicitProjectDirForSessionContext(ctx, session)
	if err != nil {
		return "", "", err
	}
	return projectDir, session, nil
}
