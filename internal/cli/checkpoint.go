package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func resolveCheckpointLiveSessionArg(session string, w io.Writer) (string, error) {
	res, err := ResolveSessionWithOptions(session, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		return "", err
	}
	if res.Session == "" {
		return "", fmt.Errorf("session is required")
	}
	res.ExplainIfInferred(os.Stderr)
	return res.Session, nil
}

func resolveCheckpointStorageSessionArg(session string) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	allowPrefix := !IsJSONOutput()
	storage := checkpoint.NewStorage()
	if storedSessions, err := listCheckpointSessions(storage); err == nil {
		if resolved, _, err := resolveExplicitSessionName(session, checkpointSessionCandidates(storedSessions), allowPrefix); err == nil {
			return resolved, nil
		} else {
			var re *sessionPkg.ResolveExplicitSessionNameError
			if errors.As(err, &re) &&
				re.Kind != sessionPkg.ResolveExplicitSessionNameErrorNoSessions &&
				re.Kind != sessionPkg.ResolveExplicitSessionNameErrorNotFound {
				return "", err
			}
		}
	}
	if tmux.IsInstalled() {
		if sessionList, err := tmux.ListSessions(); err == nil {
			if resolved, _, err := resolveExplicitSessionName(session, sessionList, allowPrefix); err == nil {
				return resolved, nil
			} else {
				var re *sessionPkg.ResolveExplicitSessionNameError
				if !errors.As(err, &re) ||
					(re.Kind != sessionPkg.ResolveExplicitSessionNameErrorNoSessions &&
						re.Kind != sessionPkg.ResolveExplicitSessionNameErrorNotFound) {
					return "", err
				}
			}
		}
	}
	return session, nil
}

func checkpointSessionCandidates(names []string) []tmux.Session {
	sessions := make([]tmux.Session, 0, len(names))
	for _, name := range names {
		sessions = append(sessions, tmux.Session{Name: name})
	}
	return sessions
}

func newCheckpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Manage session checkpoints",
		Long: `Create, list, and manage session checkpoints.

Checkpoints capture the complete state of a tmux session including:
- Pane layout and configuration
- Agent types and commands
- Scrollback buffer content
- Git repository state (branch, commit, uncommitted changes)

Examples:
  ntm checkpoint save myproject           # Create a checkpoint
  ntm checkpoint save myproject -m "Pre-refactor snapshot"
  ntm checkpoint list                     # List all checkpoints
  ntm checkpoint list myproject           # List checkpoints for session
  ntm checkpoint show myproject <id>      # Show checkpoint details
  ntm checkpoint restore myproject        # Restore the latest checkpoint
  ntm checkpoint delete myproject <id>    # Delete a checkpoint`,
	}

	cmd.AddCommand(newCheckpointSaveCmd())
	cmd.AddCommand(newCheckpointListCmd())
	cmd.AddCommand(newCheckpointShowCmd())
	cmd.AddCommand(newCheckpointRestoreCmd())
	cmd.AddCommand(newCheckpointDeleteCmd())
	cmd.AddCommand(newCheckpointVerifyCmd())
	cmd.AddCommand(newCheckpointExportCmd())
	cmd.AddCommand(newCheckpointImportCmd())

	return cmd
}

func newCheckpointSaveCmd() *cobra.Command {
	var description string
	var scrollbackLines int
	var noGit bool

	cmd := &cobra.Command{
		Use:   "save <session>",
		Short: "Create a checkpoint of a session",
		Long: `Create a checkpoint capturing the current state of a session.

The checkpoint includes:
- All pane configurations (titles, agent types, commands)
- Pane scrollback buffers (configurable depth)
- Git repository state (branch, commit, dirty status)
- Diff patch of uncommitted changes (optional)

Examples:
  ntm checkpoint save myproject
  ntm checkpoint save myproject -m "Before major refactor"
  ntm checkpoint save myproject --scrollback=500
  ntm checkpoint save myproject --no-git`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := resolveCheckpointLiveSessionArg(args[0], cmd.OutOrStdout())
			if err != nil {
				return err
			}

			// Verify session exists
			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q does not exist", session)
			}

			// Build options
			opts := []checkpoint.CheckpointOption{
				checkpoint.WithScrollbackLines(scrollbackLines),
				checkpoint.WithGitCapture(!noGit),
			}
			if description != "" {
				opts = append(opts, checkpoint.WithDescription(description))
			}

			capturer := checkpoint.NewCapturer()
			cp, err := capturer.Create(session, "", opts...)
			if err != nil {
				return fmt.Errorf("creating checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"id":                cp.ID,
					"session":           session,
					"created_at":        cp.CreatedAt,
					"description":       cp.Description,
					"pane_count":        cp.PaneCount,
					"has_git":           cp.Git.Commit != "",
					"assignments_count": len(cp.Assignments),
					"assignments":       cp.Assignments,
					"bv_summary":        cp.BVSummary,
				})
			}

			t := theme.Current()
			fmt.Printf("%s\u2713%s Checkpoint created: %s\n", colorize(t.Success), "\033[0m", cp.ID)
			fmt.Printf("  Session: %s\n", session)
			fmt.Printf("  Panes: %d\n", cp.PaneCount)
			if cp.Git.Commit != "" {
				commitPreview := cp.Git.Commit
				if len(commitPreview) > 8 {
					commitPreview = commitPreview[:8]
				}
				fmt.Printf("  Git: %s @ %s\n", cp.Git.Branch, commitPreview)
				if cp.Git.IsDirty {
					fmt.Printf("  Uncommitted: %d staged, %d unstaged\n",
						cp.Git.StagedCount, cp.Git.UnstagedCount)
				}
			}
			if cp.Description != "" {
				fmt.Printf("  Description: %s\n", cp.Description)
			}
			if summary := summarizeAssignmentCounts(cp.Assignments); summary.total > 0 {
				fmt.Printf("  Assignments: %d total (%d working, %d assigned, %d failed)\n",
					summary.total, summary.working, summary.assigned, summary.failed)
			}
			if cp.BVSummary != nil {
				fmt.Printf("  Beads: %d ready, %d blocked, %d in progress\n",
					cp.BVSummary.ActionableCount, cp.BVSummary.BlockedCount, cp.BVSummary.InProgressCount)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&description, "message", "m", "", "checkpoint description")
	cmd.Flags().IntVar(&scrollbackLines, "scrollback", 1000, "lines of scrollback to capture per pane")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "skip capturing git state")

	return cmd
}

func newCheckpointListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [session]",
		Short: "List checkpoints",
		Long: `List all checkpoints, optionally filtered by session.

Examples:
  ntm checkpoint list              # List all checkpoints
  ntm checkpoint list myproject    # List checkpoints for session`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage := checkpoint.NewStorage()

			if len(args) == 1 {
				// List checkpoints for specific session
				session, err := resolveCheckpointStorageSessionArg(args[0])
				if err != nil {
					return err
				}
				return listSessionCheckpoints(storage, session)
			}

			// List all sessions with checkpoints
			sessions, err := listCheckpointSessions(storage)
			if err != nil {
				return fmt.Errorf("listing sessions: %w", err)
			}

			if len(sessions) == 0 {
				if jsonOutput {
					return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
						"sessions": []interface{}{},
						"count":    0,
					})
				}
				fmt.Println("No checkpoints found.")
				return nil
			}

			if jsonOutput {
				type sessionInfo struct {
					Session     string                   `json:"session"`
					Checkpoints []*checkpoint.Checkpoint `json:"checkpoints"`
				}
				var result []sessionInfo
				for _, sess := range sessions {
					cps, _ := storage.List(sess)
					result = append(result, sessionInfo{
						Session:     sess,
						Checkpoints: cps,
					})
				}
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"sessions": result,
					"count":    len(sessions),
				})
			}

			t := theme.Current()
			fmt.Printf("%sCheckpoints%s\n", "\033[1m", "\033[0m")
			fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

			for _, sess := range sessions {
				cps, err := storage.List(sess)
				if err != nil || len(cps) == 0 {
					continue
				}

				fmt.Printf("  %s%s%s (%d checkpoint(s))\n", colorize(t.Primary), sess, "\033[0m", len(cps))
				for _, cp := range cps {
					age := formatAge(cp.CreatedAt)
					gitMark := ""
					if cp.Git.Commit != "" {
						gitMark = " [git]"
					}
					desc := ""
					if cp.Description != "" {
						desc = fmt.Sprintf(" - %s", truncateStr(cp.Description, 30))
					}
					fmt.Printf("    %s (%s)%s%s\n", cp.ID, age, gitMark, desc)
				}
				fmt.Println()
			}

			return nil
		},
	}

	return cmd
}

// listCheckpointSessions lists all session names that have checkpoints.
func listCheckpointSessions(storage *checkpoint.Storage) ([]string, error) {
	entries, err := os.ReadDir(storage.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []string
	for _, entry := range entries {
		checkpoints, err := storage.List(entry.Name())
		if err != nil || len(checkpoints) == 0 {
			continue
		}
		sessions = append(sessions, entry.Name())
	}
	sort.Strings(sessions)
	return sessions, nil
}

func listSessionCheckpoints(storage *checkpoint.Storage, session string) error {
	cps, err := storage.List(session)
	if err != nil {
		return fmt.Errorf("listing checkpoints: %w", err)
	}

	if len(cps) == 0 {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"session":     session,
				"checkpoints": []interface{}{},
				"count":       0,
			})
		}
		fmt.Printf("No checkpoints for session %q.\n", session)
		return nil
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"session":     session,
			"checkpoints": cps,
			"count":       len(cps),
		})
	}

	t := theme.Current()
	fmt.Printf("%sCheckpoints for %s%s\n", "\033[1m", session, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

	for _, cp := range cps {
		age := formatAge(cp.CreatedAt)
		gitMark := ""
		if cp.Git.Commit != "" {
			gitMark = fmt.Sprintf(" %s[git]%s", colorize(t.Info), "\033[0m")
		}
		desc := ""
		if cp.Description != "" {
			desc = fmt.Sprintf("\n    %s%s%s", "\033[2m", cp.Description, "\033[0m")
		}
		fmt.Printf("  %s%s%s  %s  %d pane(s)%s%s\n",
			colorize(t.Primary), cp.ID, "\033[0m",
			age, cp.PaneCount, gitMark, desc)
	}

	return nil
}

func newCheckpointShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <session> <id>",
		Short: "Show checkpoint details",
		Long: `Show detailed information about a checkpoint.

Examples:
  ntm checkpoint show myproject 20251210-143052`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := resolveCheckpointStorageSessionArg(args[0])
			if err != nil {
				return err
			}
			id := args[1]

			storage := checkpoint.NewStorage()
			cp, err := storage.Load(session, id)
			if err != nil {
				return fmt.Errorf("loading checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(cp)
			}

			t := theme.Current()
			fmt.Printf("%sCheckpoint: %s%s\n", "\033[1m", cp.ID, "\033[0m")
			fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

			fmt.Printf("  Session: %s\n", cp.SessionName)
			fmt.Printf("  Created: %s (%s)\n", cp.CreatedAt.Format(time.RFC3339), formatAge(cp.CreatedAt))
			fmt.Printf("  Working Dir: %s\n", cp.WorkingDir)
			if cp.Description != "" {
				fmt.Printf("  Description: %s\n", cp.Description)
			}
			fmt.Println()

			fmt.Printf("  %sPanes (%d):%s\n", "\033[1m", len(cp.Session.Panes), "\033[0m")
			for _, pane := range cp.Session.Panes {
				agentType := pane.AgentType
				if agentType == "" {
					agentType = "user"
				}
				scrollbackInfo := ""
				if pane.ScrollbackLines > 0 {
					scrollbackInfo = fmt.Sprintf(" [%d lines]", pane.ScrollbackLines)
				}
				fmt.Printf("    %d: %s (%s)%s\n", pane.Index, pane.Title, agentType, scrollbackInfo)
			}

			if cp.Git.Commit != "" {
				fmt.Println()
				fmt.Printf("  %sGit State:%s\n", "\033[1m", "\033[0m")
				fmt.Printf("    Branch: %s\n", cp.Git.Branch)
				fmt.Printf("    Commit: %s\n", cp.Git.Commit)
				if cp.Git.IsDirty {
					fmt.Printf("    Status: %sdirty%s (%d staged, %d unstaged, %d untracked)\n",
						colorize(t.Warning), "\033[0m",
						cp.Git.StagedCount, cp.Git.UnstagedCount, cp.Git.UntrackedCount)
					if cp.Git.PatchFile != "" {
						fmt.Printf("    Patch: captured\n")
					}
				} else {
					fmt.Printf("    Status: %sclean%s\n", colorize(t.Success), "\033[0m")
				}
			}

			if summary := summarizeAssignmentCounts(cp.Assignments); summary.total > 0 {
				fmt.Println()
				fmt.Printf("  %sAssignments:%s\n", "\033[1m", "\033[0m")
				fmt.Printf("    Total: %d (working=%d, assigned=%d, failed=%d)\n",
					summary.total, summary.working, summary.assigned, summary.failed)
			}

			if cp.BVSummary != nil {
				fmt.Println()
				fmt.Printf("  %sBV Summary:%s\n", "\033[1m", "\033[0m")
				fmt.Printf("    Ready: %d\n", cp.BVSummary.ActionableCount)
				fmt.Printf("    Blocked: %d\n", cp.BVSummary.BlockedCount)
				fmt.Printf("    In Progress: %d\n", cp.BVSummary.InProgressCount)
			}

			return nil
		},
	}

	return cmd
}

func newCheckpointDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <session> <id>",
		Short: "Delete a checkpoint",
		Long: `Delete a checkpoint from storage.

Examples:
  ntm checkpoint delete myproject 20251210-143052
  ntm checkpoint delete myproject 20251210-143052 --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := resolveCheckpointStorageSessionArg(args[0])
			if err != nil {
				return err
			}
			id := args[1]

			storage := checkpoint.NewStorage()

			// Verify checkpoint exists
			if _, err := storage.Load(session, id); err != nil {
				return fmt.Errorf("checkpoint not found: %w", err)
			}

			if !force && !jsonOutput {
				title := fmt.Sprintf("Delete checkpoint %s?", id)
				desc := fmt.Sprintf("This checkpoint for session '%s' will be permanently removed.", session)
				if !confirmHuhDestructive(title, desc) {
					fmt.Println("Aborted.")
					return nil
				}
			}

			if err := storage.Delete(session, id); err != nil {
				return fmt.Errorf("deleting checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"deleted": true,
					"session": session,
					"id":      id,
				})
			}

			t := theme.Current()
			fmt.Printf("%s\u2713%s Deleted checkpoint: %s\n", colorize(t.Success), "\033[0m", id)

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")

	return cmd
}

func newCheckpointRestoreCmd() *cobra.Command {
	var (
		force           bool
		attach          bool
		skipGitCheck    bool
		injectContext   bool
		dryRun          bool
		customDirectory string
		scrollbackLines int
	)

	cmd := &cobra.Command{
		Use:   "restore <session> [checkpoint-id]",
		Short: "Restore a session from a checkpoint",
		Long: `Restore a tmux session from a checkpoint.

If checkpoint-id is omitted, the most recent checkpoint is restored.

The checkpoint-id can be:
- A full checkpoint ID (e.g. 20251210-143052)
- A partial ID prefix or checkpoint name
- "last", "latest", "~1", or "~N" for historical selection

Examples:
  ntm checkpoint restore myproject
  ntm checkpoint restore myproject 20251210-143052
  ntm checkpoint restore myproject ~2 --dry-run
  ntm checkpoint restore myproject --inject-context
  ntm checkpoint restore myproject last --force`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOutput && attach {
				return fmt.Errorf("--attach cannot be used with --json")
			}
			if dryRun && attach {
				return fmt.Errorf("--attach cannot be used with --dry-run")
			}
			if scrollbackLines < 0 {
				return fmt.Errorf("--scrollback must be >= 0")
			}

			sessionName, err := resolveCheckpointStorageSessionArg(args[0])
			if err != nil {
				return err
			}
			checkpointRef := "last"
			if len(args) == 2 {
				checkpointRef = args[1]
			}

			capturer := checkpoint.NewCapturer()
			cp, err := capturer.ParseCheckpointRef(sessionName, checkpointRef)
			if err != nil {
				if jsonOutput {
					_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
						"success":        false,
						"session":        sessionName,
						"checkpoint_ref": checkpointRef,
						"error":          err.Error(),
					})
					return fmt.Errorf("finding checkpoint: %w", err)
				}
				return fmt.Errorf("finding checkpoint: %w", err)
			}

			if !dryRun {
				if err := tmux.EnsureInstalled(); err != nil {
					return err
				}
			}

			opts := checkpoint.RestoreOptions{
				Force:           force,
				SkipGitCheck:    skipGitCheck,
				InjectContext:   injectContext,
				DryRun:          dryRun,
				CustomDirectory: customDirectory,
				ScrollbackLines: scrollbackLines,
			}

			restorer := checkpoint.NewRestorer()
			result, err := restorer.RestoreFromCheckpoint(cp, opts)
			if err != nil {
				if jsonOutput {
					_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
						"success":        false,
						"session":        sessionName,
						"checkpoint_ref": checkpointRef,
						"checkpoint_id":  cp.ID,
						"error":          err.Error(),
					})
					return fmt.Errorf("restoring checkpoint: %w", err)
				}
				return fmt.Errorf("restoring checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"success":           true,
					"session":           result.SessionName,
					"checkpoint_id":     cp.ID,
					"checkpoint_ref":    checkpointRef,
					"created_at":        cp.CreatedAt,
					"description":       cp.Description,
					"panes_restored":    result.PanesRestored,
					"context_injected":  result.ContextInjected,
					"dry_run":           result.DryRun,
					"warnings":          result.Warnings,
					"assignments_count": len(result.Assignments),
					"assignments":       result.Assignments,
					"bv_summary":        result.BVSummary,
				})
			}

			t := theme.Current()
			if dryRun {
				fmt.Printf("%sRestore Preview (dry-run)%s\n", "\033[1m", "\033[0m")
				fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")
				fmt.Printf("  Session: %s\n", result.SessionName)
				fmt.Printf("  Checkpoint: %s\n", cp.ID)
				fmt.Printf("  Created: %s (%s)\n", cp.CreatedAt.Format(time.RFC3339), formatAge(cp.CreatedAt))
				if cp.Description != "" {
					fmt.Printf("  Description: %s\n", cp.Description)
				}
				fmt.Printf("  Panes to restore: %d\n", result.PanesRestored)
				if injectContext {
					fmt.Printf("  Context Injection: enabled")
					if scrollbackLines > 0 {
						fmt.Printf(" (%d lines)\n", scrollbackLines)
					} else {
						fmt.Printf(" (all captured lines)\n")
					}
				} else {
					fmt.Printf("  Context Injection: disabled\n")
				}
				if summary := summarizeAssignmentCounts(result.Assignments); summary.total > 0 {
					fmt.Printf("  Assignments: %d total (%d working, %d assigned, %d failed)\n",
						summary.total, summary.working, summary.assigned, summary.failed)
				}
				if result.BVSummary != nil {
					fmt.Printf("  Beads: %d ready, %d blocked, %d in progress\n",
						result.BVSummary.ActionableCount, result.BVSummary.BlockedCount, result.BVSummary.InProgressCount)
				}
				if len(result.Warnings) > 0 {
					fmt.Printf("\n  %sWarnings:%s\n", colorize(t.Warning), "\033[0m")
					for _, warning := range result.Warnings {
						fmt.Printf("    • %s\n", warning)
					}
				}
				fmt.Println()
				fmt.Printf("  %sNo changes made (dry-run mode)%s\n", colorize(t.Info), "\033[0m")
				return nil
			}

			fmt.Printf("%s✓%s Restored checkpoint: %s\n", colorize(t.Success), "\033[0m", cp.ID)
			fmt.Printf("  Session: %s\n", result.SessionName)
			fmt.Printf("  Created: %s (%s)\n", cp.CreatedAt.Format(time.RFC3339), formatAge(cp.CreatedAt))
			if cp.Description != "" {
				fmt.Printf("  Description: %s\n", cp.Description)
			}
			fmt.Printf("  Panes Restored: %d\n", result.PanesRestored)
			if result.ContextInjected {
				fmt.Printf("  Context Injection: enabled\n")
			}
			if summary := summarizeAssignmentCounts(result.Assignments); summary.total > 0 {
				fmt.Printf("  Assignments: %d total (%d working, %d assigned, %d failed)\n",
					summary.total, summary.working, summary.assigned, summary.failed)
			}
			if result.BVSummary != nil {
				fmt.Printf("  Beads: %d ready, %d blocked, %d in progress\n",
					result.BVSummary.ActionableCount, result.BVSummary.BlockedCount, result.BVSummary.InProgressCount)
			}
			if len(result.Warnings) > 0 {
				fmt.Printf("\n  %sWarnings:%s\n", colorize(t.Warning), "\033[0m")
				for _, warning := range result.Warnings {
					fmt.Printf("    • %s\n", warning)
				}
			}

			if attach {
				return tmux.AttachOrSwitch(result.SessionName)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing tmux session")
	cmd.Flags().BoolVarP(&attach, "attach", "a", false, "attach after restore")
	cmd.Flags().BoolVar(&skipGitCheck, "skip-git-check", false, "skip git branch and commit mismatch warnings")
	cmd.Flags().BoolVar(&injectContext, "inject-context", false, "inject captured scrollback into restored panes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the restore without making changes")
	cmd.Flags().StringVar(&customDirectory, "directory", "", "override the checkpoint working directory")
	cmd.Flags().IntVar(&scrollbackLines, "scrollback", 0, "lines of captured scrollback to inject (0 = all captured)")

	return cmd
}

func newCheckpointVerifyCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "verify <session> [id]",
		Short: "Verify checkpoint integrity",
		Long: `Verify the integrity of one or all checkpoints.

Performs the following checks:
- Schema validation (version, required fields)
- File existence (metadata.json, session.json, scrollback files)
- Consistency checks (pane count, valid indices)

Examples:
  ntm checkpoint verify myproject 20251210-143052  # Verify single checkpoint
  ntm checkpoint verify myproject --all            # Verify all checkpoints for session`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := resolveCheckpointStorageSessionArg(args[0])
			if err != nil {
				return err
			}
			storage := checkpoint.NewStorage()

			if all {
				return verifyAllCheckpoints(storage, session)
			}

			if len(args) < 2 {
				return fmt.Errorf("checkpoint ID required (or use --all)")
			}

			id := args[1]
			return verifySingleCheckpoint(storage, session, id)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "verify all checkpoints for session")

	return cmd
}

func verifySingleCheckpoint(storage *checkpoint.Storage, session, id string) error {
	exists, err := storage.HasCheckpointPath(session, id)
	if err != nil {
		return fmt.Errorf("loading checkpoint: %w", err)
	}
	if !exists {
		return fmt.Errorf("loading checkpoint: checkpoint not found: %s", id)
	}

	result := checkpoint.VerifyStoredCheckpoint(storage, session, id)

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"session": session,
			"id":      id,
			"valid":   result.Valid,
			"checks":  result,
		})
	}

	t := theme.Current()
	fmt.Printf("%sVerifying: %s%s\n", "\033[1m", id, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

	// Schema
	if result.SchemaValid {
		fmt.Printf("  %s\u2713%s Schema valid\n", colorize(t.Success), "\033[0m")
	} else {
		fmt.Printf("  %s\u2717%s Schema invalid\n", colorize(t.Error), "\033[0m")
	}

	// Files
	if result.FilesPresent {
		fmt.Printf("  %s\u2713%s All files present\n", colorize(t.Success), "\033[0m")
	} else {
		fmt.Printf("  %s\u2717%s Missing files\n", colorize(t.Error), "\033[0m")
	}

	// Consistency
	if result.ConsistencyValid {
		fmt.Printf("  %s\u2713%s Consistency checks passed\n", colorize(t.Success), "\033[0m")
	} else {
		fmt.Printf("  %s\u2717%s Consistency issues\n", colorize(t.Error), "\033[0m")
	}

	// Errors
	if len(result.Errors) > 0 {
		fmt.Printf("\n  %sErrors:%s\n", colorize(t.Error), "\033[0m")
		for _, e := range result.Errors {
			fmt.Printf("    • %s\n", e)
		}
	}

	// Warnings
	if len(result.Warnings) > 0 {
		fmt.Printf("\n  %sWarnings:%s\n", colorize(t.Warning), "\033[0m")
		for _, w := range result.Warnings {
			fmt.Printf("    • %s\n", w)
		}
	}

	fmt.Println()
	if result.Valid {
		fmt.Printf("%s\u2713 Checkpoint verified successfully%s\n", colorize(t.Success), "\033[0m")
	} else {
		fmt.Printf("%s\u2717 Checkpoint verification failed%s\n", colorize(t.Error), "\033[0m")
		return fmt.Errorf("verification failed with %d error(s)", len(result.Errors))
	}

	return nil
}

func verifyAllCheckpoints(storage *checkpoint.Storage, session string) error {
	results, err := checkpoint.VerifyAll(storage, session)
	if err != nil {
		return fmt.Errorf("verifying checkpoints: %w", err)
	}

	if len(results) == 0 {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"session":     session,
				"checkpoints": []interface{}{},
				"valid_count": 0,
				"total_count": 0,
			})
		}
		fmt.Printf("No checkpoints found for session %q.\n", session)
		return nil
	}

	validCount := 0
	for _, r := range results {
		if r.Valid {
			validCount++
		}
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"session":     session,
			"checkpoints": results,
			"valid_count": validCount,
			"total_count": len(results),
		})
	}

	t := theme.Current()
	fmt.Printf("%sVerifying checkpoints for %s%s\n", "\033[1m", session, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")

	for id, result := range results {
		status := colorize(t.Success) + "\u2713" + "\033[0m"
		if !result.Valid {
			status = colorize(t.Error) + "\u2717" + "\033[0m"
		}
		errorInfo := ""
		if len(result.Errors) > 0 {
			errorInfo = fmt.Sprintf(" (%d error(s))", len(result.Errors))
		}
		fmt.Printf("  %s %s%s\n", status, id, errorInfo)
	}

	fmt.Println()
	fmt.Printf("Verified: %d/%d valid\n", validCount, len(results))

	if validCount < len(results) {
		return fmt.Errorf("%d checkpoint(s) failed verification", len(results)-validCount)
	}

	return nil
}

func newCheckpointExportCmd() *cobra.Command {
	var (
		output        string
		format        string
		redactSecrets bool
		noScrollback  bool
		noGitPatch    bool
	)

	cmd := &cobra.Command{
		Use:   "export <session> <id>",
		Short: "Export a checkpoint to a shareable archive",
		Long: `Export a checkpoint to a tar.gz or zip archive for sharing.

The exported archive contains all checkpoint data:
- Metadata (session name, git state, pane configuration)
- Scrollback buffers
- Git patches (uncommitted changes)
- MANIFEST.json with SHA256 checksums

Use --redact-secrets to remove sensitive data (API keys, tokens) from
scrollback files before sharing.

Examples:
  ntm checkpoint export myproject 20251210-143052
  ntm checkpoint export myproject 20251210-143052 --output=backup.tar.gz
  ntm checkpoint export myproject 20251210-143052 --format=zip
  ntm checkpoint export myproject 20251210-143052 --redact-secrets`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := resolveCheckpointStorageSessionArg(args[0])
			if err != nil {
				return err
			}
			id := args[1]

			storage := checkpoint.NewStorage()

			// Verify checkpoint exists
			if _, err := storage.Load(session, id); err != nil {
				return fmt.Errorf("checkpoint not found: %w", err)
			}

			// Determine output path
			outputPath := output
			if outputPath == "" {
				ext := ".tar.gz"
				if format == "zip" {
					ext = ".zip"
				}
				outputPath = fmt.Sprintf("%s_%s%s", session, id, ext)
			}

			// Build options
			opts := checkpoint.DefaultExportOptions()
			if format == "zip" {
				opts.Format = checkpoint.FormatZip
			}
			opts.RedactSecrets = redactSecrets
			opts.IncludeScrollback = !noScrollback
			opts.IncludeGitPatch = !noGitPatch

			manifest, err := storage.Export(session, id, outputPath, opts)
			if err != nil {
				return fmt.Errorf("exporting checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"output_path":     outputPath,
					"session":         manifest.SessionName,
					"checkpoint_id":   manifest.CheckpointID,
					"checkpoint_name": manifest.CheckpointName,
					"file_count":      len(manifest.Files),
					"exported_at":     manifest.ExportedAt,
				})
			}

			t := theme.Current()
			fmt.Printf("%s✓%s Exported checkpoint: %s\n", colorize(t.Success), "\033[0m", outputPath)
			fmt.Printf("  Session: %s\n", manifest.SessionName)
			fmt.Printf("  Checkpoint: %s\n", manifest.CheckpointID)
			fmt.Printf("  Files: %d\n", len(manifest.Files))

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: <session>_<id>.tar.gz)")
	cmd.Flags().StringVar(&format, "format", "tar.gz", "archive format: tar.gz or zip")
	cmd.Flags().BoolVar(&redactSecrets, "redact-secrets", false, "remove sensitive data before export")
	cmd.Flags().BoolVar(&noScrollback, "no-scrollback", false, "exclude scrollback buffers")
	cmd.Flags().BoolVar(&noGitPatch, "no-git-patch", false, "exclude git patch file")

	return cmd
}

func newCheckpointImportCmd() *cobra.Command {
	var (
		targetSession  string
		targetDir      string
		skipVerify     bool
		allowOverwrite bool
	)

	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a checkpoint from an archive",
		Long: `Import a checkpoint from a tar.gz or zip archive.

The archive must contain a valid NTM checkpoint structure with
metadata.json and session data.

Use --session to import into a different session name.
Use --target-dir to override the working directory path.

Examples:
  ntm checkpoint import backup.tar.gz
  ntm checkpoint import backup.zip --session=restored-session
  ntm checkpoint import backup.tar.gz --target-dir=/new/path/to/project
  ntm checkpoint import backup.tar.gz --skip-verify`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archivePath := args[0]

			// Verify archive exists
			if _, err := os.Stat(archivePath); err != nil {
				return fmt.Errorf("archive not found: %w", err)
			}

			storage := checkpoint.NewStorage()

			opts := checkpoint.ImportOptions{
				TargetSession:   targetSession,
				TargetDir:       targetDir,
				VerifyChecksums: !skipVerify,
				AllowOverwrite:  allowOverwrite,
			}

			cp, err := storage.Import(archivePath, opts)
			if err != nil {
				return fmt.Errorf("importing checkpoint: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"session":       cp.SessionName,
					"checkpoint_id": cp.ID,
					"name":          cp.Name,
					"working_dir":   cp.WorkingDir,
					"pane_count":    cp.PaneCount,
				})
			}

			t := theme.Current()
			fmt.Printf("%s✓%s Imported checkpoint\n", colorize(t.Success), "\033[0m")
			fmt.Printf("  Session: %s\n", cp.SessionName)
			fmt.Printf("  ID: %s\n", cp.ID)
			if cp.Name != "" {
				fmt.Printf("  Name: %s\n", cp.Name)
			}
			fmt.Printf("  Panes: %d\n", cp.PaneCount)
			if cp.WorkingDir != "" && cp.WorkingDir != "${WORKING_DIR}" {
				fmt.Printf("  Working Dir: %s\n", cp.WorkingDir)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&targetSession, "session", "", "override session name")
	cmd.Flags().StringVar(&targetDir, "target-dir", "", "override working directory path")
	cmd.Flags().BoolVar(&skipVerify, "skip-verify", false, "skip checksum verification")
	cmd.Flags().BoolVar(&allowOverwrite, "overwrite", false, "overwrite existing checkpoint")

	return cmd
}

func summarizeAssignmentCounts(assignments []checkpoint.AssignmentSnapshot) assignmentSummary {
	var summary assignmentSummary
	summary.total = len(assignments)
	for _, a := range assignments {
		switch a.Status {
		case "working":
			summary.working++
		case "assigned":
			summary.assigned++
		case "failed":
			summary.failed++
		}
	}
	return summary
}

type assignmentSummary struct {
	total    int
	working  int
	assigned int
	failed   int
}

// formatAge returns a human-readable age string.
func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		return t.Format("Jan 2")
	}
}

// truncateStr shortens a string to max length, respecting UTF-8 boundaries.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	// Find the last rune boundary that allows for "..." suffix within maxLen bytes.
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts are <= targetLen, but string is > maxLen bytes.
	return s[:prevI] + "..."
}
