package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newRollbackCmd() *cobra.Command {
	var dryRun bool
	var noStash bool
	var noGit bool
	var last bool
	var force bool

	cmd := &cobra.Command{
		Use:   "rollback <session> [checkpoint-id]",
		Short: "Restore session to a checkpoint state",
		Long: `Restore a session to a previous checkpoint state.

By default, current git changes are stashed before rollback.
The checkpoint's git patch is applied to restore the code state.

The checkpoint-id can be:
- A full checkpoint ID (e.g., 20251210-143052)
- A partial ID prefix
- "last" or "~1" for the most recent checkpoint
- "~N" for the Nth most recent (e.g., "~2" for second-to-last)

Examples:
  ntm rollback myproject last              # Roll back to last checkpoint
  ntm rollback myproject 20251210-143052   # Roll back to specific checkpoint
  ntm rollback myproject ~2                # Roll back to second-to-last
  ntm rollback myproject last --dry-run    # Preview without changes
  ntm rollback myproject last --no-stash   # Don't stash current changes
  ntm rollback myproject last --no-git     # Skip git operations`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			// Determine checkpoint reference
			var checkpointRef string
			if last {
				checkpointRef = "last"
			} else if len(args) > 1 {
				checkpointRef = args[1]
			} else {
				return fmt.Errorf("checkpoint reference required (use --last or provide checkpoint ID)")
			}

			storageSession, liveSession, err := resolveRollbackSessions(session, cmd.OutOrStdout(), !noGit)
			if err != nil {
				return err
			}

			workDir := ""
			if !noGit {
				workDir, err = getSessionWorkDir(liveSession)
				if err != nil {
					return fmt.Errorf("getting session working directory: %w", err)
				}
			}

			// Load checkpoint
			storage := checkpoint.NewStorage()
			capturer := checkpoint.NewCapturerWithStorage(storage)
			cp, err := capturer.ParseCheckpointRef(storageSession, checkpointRef)
			if err != nil {
				if checkpoint.IsValidCheckpointID(strings.TrimSpace(checkpointRef)) {
					exists, existsErr := storage.HasCheckpointPath(storageSession, checkpointRef)
					if existsErr != nil {
						return fmt.Errorf("finding checkpoint: %w", existsErr)
					}
					if exists {
						return fmt.Errorf("loading checkpoint: %w", err)
					}
				}
				return fmt.Errorf("finding checkpoint: %w", err)
			}

			if dryRun {
				return showRollbackPreview(cp, workDir, noStash, noGit)
			}

			// Safety check
			if !force && !jsonOutput {
				fmt.Printf("Roll back to checkpoint %s?\n", cp.ID)
				fmt.Printf("  Created: %s (%s)\n", cp.CreatedAt.Format(time.RFC3339), formatAge(cp.CreatedAt))
				if cp.Git.Commit != "" {
					fmt.Printf("  Git: %s @ %s\n", cp.Git.Branch, cp.Git.Commit[:min(8, len(cp.Git.Commit))])
				}
				if cp.Description != "" {
					fmt.Printf("  Description: %s\n", cp.Description)
				}
				fmt.Println()
				title := "Proceed with rollback?"
				desc := "This will restore files to the checkpoint state."
				if !confirmHuhDestructive(title, desc) {
					fmt.Println("Aborted.")
					return nil
				}
			}

			return performRollback(cp, workDir, noStash, noGit)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without applying")
	cmd.Flags().BoolVar(&noStash, "no-stash", false, "don't stash current changes before rollback")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "skip all git operations")
	cmd.Flags().BoolVar(&last, "last", false, "roll back to the most recent checkpoint")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompt")

	return cmd
}

func resolveRollbackSessions(session string, w io.Writer, requireLive bool) (string, string, error) {
	storageSession, err := resolveCheckpointStorageSessionArg(session)
	if err != nil {
		return "", "", err
	}
	if !requireLive {
		return storageSession, "", nil
	}
	liveSession, err := resolveCheckpointLiveSessionArg(session, w)
	if err != nil {
		return "", "", err
	}
	return storageSession, liveSession, nil
}

func showRollbackPreview(cp *checkpoint.Checkpoint, workDir string, noStash, noGit bool) error {
	if jsonOutput {
		preview := map[string]interface{}{
			"dry_run":     true,
			"checkpoint":  cp.ID,
			"session":     cp.SessionName,
			"created_at":  cp.CreatedAt,
			"description": cp.Description,
			"pane_count":  cp.PaneCount,
			"actions":     []string{},
		}

		var actions []string
		if !noGit && cp.Git.Commit != "" {
			if !noStash {
				actions = append(actions, "stash_current_changes")
			}
			if cp.Git.PatchFile != "" {
				actions = append(actions, "apply_git_patch")
			}
			actions = append(actions, fmt.Sprintf("checkout_commit_%s", cp.Git.Commit[:min(8, len(cp.Git.Commit))]))
		}
		preview["actions"] = actions

		return json.NewEncoder(os.Stdout).Encode(preview)
	}

	t := theme.Current()
	fmt.Printf("%sRollback Preview (dry-run)%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

	fmt.Printf("  Checkpoint: %s%s%s\n", colorize(t.Primary), cp.ID, "\033[0m")
	fmt.Printf("  Created: %s (%s)\n", cp.CreatedAt.Format(time.RFC3339), formatAge(cp.CreatedAt))
	if cp.Description != "" {
		fmt.Printf("  Description: %s\n", cp.Description)
	}
	fmt.Println()

	fmt.Printf("  %sPlanned Actions:%s\n", "\033[1m", "\033[0m")

	actionNum := 1
	if !noGit && workDir != "" {
		// Check current git state
		if hasUncommittedChanges(workDir) {
			if !noStash {
				fmt.Printf("    %d. %sStash%s current uncommitted changes\n", actionNum, colorize(t.Warning), "\033[0m")
				actionNum++
			} else {
				fmt.Printf("    %s! Warning: uncommitted changes will be lost%s\n", colorize(t.Error), "\033[0m")
			}
		}

		if cp.Git.Commit != "" {
			fmt.Printf("    %d. Checkout commit %s\n", actionNum, cp.Git.Commit[:min(8, len(cp.Git.Commit))])
			actionNum++

			if cp.Git.PatchFile != "" && cp.Git.IsDirty {
				fmt.Printf("    %d. Apply saved patch (%d staged, %d unstaged changes)\n",
					actionNum, cp.Git.StagedCount, cp.Git.UnstagedCount)
				actionNum++
			}
		}
	}

	if noGit {
		fmt.Printf("    %s(git operations skipped with --no-git)%s\n", "\033[2m", "\033[0m")
	}

	fmt.Println()
	fmt.Printf("  %sNo changes made (dry-run mode)%s\n", colorize(t.Info), "\033[0m")

	return nil
}

func performRollback(cp *checkpoint.Checkpoint, workDir string, noStash, noGit bool) error {
	t := theme.Current()
	var stashName string

	// 1. Interrupt agents to stop them from writing to files during rollback
	if !jsonOutput {
		fmt.Println("Interrupting agents...")
	}
	// Interrupt all agents (no tags)
	if err := runInterrupt(cp.SessionName, nil); err != nil {
		if !jsonOutput {
			fmt.Printf("%s! Warning: failed to interrupt agents: %v%s\n", colorize(t.Warning), err, "\033[0m")
		}
	} else if !jsonOutput {
		fmt.Printf("%s\u2713%s Agents interrupted\n", colorize(t.Success), "\033[0m")
	}

	if !noGit && workDir != "" && cp.Git.Commit != "" {
		// Stash current changes if needed
		if !noStash && hasUncommittedChanges(workDir) {
			stashName = fmt.Sprintf("ntm-rollback-%s", time.Now().Format("20060102-150405"))
			if err := gitStash(workDir, stashName); err != nil {
				return fmt.Errorf("stashing changes: %w", err)
			}
			if !jsonOutput {
				fmt.Printf("%s\u2713%s Stashed current changes: %s\n", colorize(t.Success), "\033[0m", stashName)
			}
		}

		// Checkout the commit
		if err := gitCheckout(workDir, cp.Git.Commit); err != nil {
			// Try to restore stash if checkout fails
			if stashName != "" {
				gitStashPop(workDir)
			}
			return fmt.Errorf("checkout commit: %w", err)
		}
		if !jsonOutput {
			fmt.Printf("%s\u2713%s Checked out: %s\n", colorize(t.Success), "\033[0m", cp.Git.Commit[:min(8, len(cp.Git.Commit))])
		}

		// Apply patch if available
		if cp.Git.PatchFile != "" && cp.Git.IsDirty {
			storage := checkpoint.NewStorage()
			patch, err := storage.LoadGitPatch(cp.SessionName, cp.ID)
			if err == nil && patch != "" {
				if err := gitApplyPatch(workDir, patch); err != nil {
					if !jsonOutput {
						fmt.Printf("%s! Warning: could not apply patch: %v%s\n", colorize(t.Warning), err, "\033[0m")
					}
				} else {
					if !jsonOutput {
						fmt.Printf("%s\u2713%s Applied saved patch\n", colorize(t.Success), "\033[0m")
					}
				}
			}
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"success":     true,
			"checkpoint":  cp.ID,
			"session":     cp.SessionName,
			"stash_name":  stashName,
			"rolled_back": true,
		}
		if cp.Git.Commit != "" {
			result["git_commit"] = cp.Git.Commit
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Println()
	fmt.Printf("%s\u2713 Rollback complete%s\n", colorize(t.Success), "\033[0m")
	if stashName != "" {
		fmt.Printf("  To restore stashed changes: git stash pop\n")
	}

	return nil
}

// getSessionWorkDir gets the working directory from a tmux session.
func getSessionWorkDir(session string) (string, error) {
	session = strings.TrimSpace(session)
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	if !tmux.SessionExists(session) {
		return "", fmt.Errorf("session %q does not exist", session)
	}
	cmd := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", session, "#{pane_current_path}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// hasUncommittedChanges checks if there are uncommitted changes.
func hasUncommittedChanges(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// gitStash creates a named stash.
func gitStash(dir, name string) error {
	cmd := exec.Command("git", "-C", dir, "stash", "push", "-m", name)
	return cmd.Run()
}

// gitStashPop restores the most recent stash.
func gitStashPop(dir string) error {
	cmd := exec.Command("git", "-C", dir, "stash", "pop")
	return cmd.Run()
}

// gitCheckout checks out a specific commit.
func gitCheckout(dir, commit string) error {
	cmd := exec.Command("git", "-C", dir, "checkout", commit)
	return cmd.Run()
}

// gitApplyPatch applies a patch.
func gitApplyPatch(dir, patch string) error {
	cmd := exec.Command("git", "-C", dir, "apply", "--3way", "-")
	cmd.Stdin = strings.NewReader(patch)
	return cmd.Run()
}
