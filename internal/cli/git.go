package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func newGitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Git coordination commands",
		Long: `Git-related commands for coordinating version control across agents.

These commands combine git state with Agent Mail file reservations to
provide a unified view of coordination state.

Examples:
  ntm git status myproject     # Show branches, locks, and pending changes
  ntm git status               # Status for current directory
  ntm git sync myproject       # Pull and push changes`,
	}

	cmd.AddCommand(newGitStatusCmd())
	cmd.AddCommand(newGitSyncCmd())

	return cmd
}

func newGitSyncCmd() *cobra.Command {
	var (
		pullOnly bool
		pushOnly bool
		force    bool
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "sync [session]",
		Short: "Synchronize git repository (pull and push)",
		Long: `Pull remote changes and push local commits.

By default, performs: git fetch, git pull --rebase, git push.
If conflicts occur, shows details and prompts for action.

Examples:
  ntm git sync myproject        # Full sync (pull + push)
  ntm git sync                  # Sync current directory
  ntm git sync --pull-only      # Only fetch and pull
  ntm git sync --push-only      # Only push local commits
  ntm git sync --dry-run        # Show what would be done`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runGitSync(cmd.Context(), session, pullOnly, pushOnly, force, dryRun)
		},
	}

	cmd.Flags().BoolVar(&pullOnly, "pull-only", false, "Only pull (no push)")
	cmd.Flags().BoolVar(&pushOnly, "push-only", false, "Only push (no pull)")
	cmd.Flags().BoolVar(&force, "force", false, "Force push (use with caution)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without doing it")

	return cmd
}

// GitSyncResult represents the result of a git sync operation.
type GitSyncResult struct {
	Success     bool        `json:"success"`
	Session     string      `json:"session,omitempty"`
	WorkingDir  string      `json:"working_dir"`
	PullResult  *PullResult `json:"pull_result,omitempty"`
	PushResult  *PushResult `json:"push_result,omitempty"`
	HasConflict bool        `json:"has_conflict"`
	Error       string      `json:"error,omitempty"`
}

// PullResult contains the result of a git pull operation.
type PullResult struct {
	Success         bool     `json:"success"`
	FastForward     bool     `json:"fast_forward"`
	Behind          int      `json:"behind"`
	Merged          int      `json:"merged"`
	Files           []string `json:"files,omitempty"`
	Conflicts       []string `json:"conflicts,omitempty"`
	Error           string   `json:"error,omitempty"`
	AlreadyUpToDate bool     `json:"already_up_to_date"`
}

// PushResult contains the result of a git push operation.
type PushResult struct {
	Success       bool   `json:"success"`
	Ahead         int    `json:"ahead"`
	Pushed        int    `json:"pushed"`
	Remote        string `json:"remote"`
	Branch        string `json:"branch"`
	Error         string `json:"error,omitempty"`
	NothingToPush bool   `json:"nothing_to_push"`
}

func runGitSync(ctx context.Context, session string, pullOnly, pushOnly, force, dryRun bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session, workDir, err := resolveGitProjectDir(ctx, session)
	if err != nil {
		return err
	}

	result := GitSyncResult{
		Success:    true,
		Session:    session,
		WorkingDir: workDir,
	}

	// Check if git repo
	cmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result.Success = false
		result.Error = "not a git repository"
		return outputGitSyncResult(result)
	}

	// Get current status first
	gitInfo, _ := getGitInfoContext(ctx, workDir)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Check for uncommitted changes that could cause conflicts
	if gitInfo != nil && gitInfo.Dirty && !pushOnly {
		if !dryRun && !IsJSONOutput() {
			fmt.Println("⚠ Warning: You have uncommitted changes")
			fmt.Println("  Consider committing or stashing before sync")
			fmt.Println()
		}
	}

	// Pull phase
	if !pushOnly {
		result.PullResult = runGitPull(ctx, workDir, dryRun)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !result.PullResult.Success {
			result.Success = false
			result.HasConflict = len(result.PullResult.Conflicts) > 0
			result.Error = "pull failed: " + result.PullResult.Error
		}
	}

	// Push phase (only if pull succeeded or we're doing push-only)
	if !pullOnly && (result.PullResult == nil || result.PullResult.Success) {
		result.PushResult = runGitPush(ctx, workDir, force, dryRun)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !result.PushResult.Success {
			result.Success = false
			result.Error = "push failed: " + result.PushResult.Error
		}
	}

	return outputGitSyncResult(result)
}

func runGitPull(ctx context.Context, workDir string, dryRun bool) *PullResult {
	if ctx == nil {
		ctx = context.Background()
	}
	result := &PullResult{Success: true}

	if dryRun {
		// Fetch to see what would be pulled
		cmd := exec.CommandContext(ctx, "git", "-C", workDir, "fetch", "--dry-run")
		if err := cmd.Run(); err != nil {
			result.Success = false
			result.Error = gitCommandFailure("fetch --dry-run failed", err)
			return result
		}

		// Check how far behind we are
		cmd = exec.CommandContext(ctx, "git", "-C", workDir, "rev-list", "--count", "HEAD..@{u}")
		out, err := cmd.Output()
		if err != nil {
			result.Success = false
			result.Error = gitCommandFailure("determine pull distance failed", err)
			return result
		}
		result.Behind, err = parseGitCount(out)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			return result
		}
		if result.Behind == 0 {
			result.AlreadyUpToDate = true
		}
		return result
	}

	// Fetch first
	cmd := exec.CommandContext(ctx, "git", "-C", workDir, "fetch")
	if err := cmd.Run(); err != nil {
		result.Success = false
		result.Error = gitCommandFailure("fetch failed", err)
		return result
	}

	// Check if there are changes to pull
	cmd = exec.CommandContext(ctx, "git", "-C", workDir, "rev-list", "--count", "HEAD..@{u}")
	out, err := cmd.Output()
	if err != nil {
		result.Success = false
		result.Error = gitCommandFailure("determine pull distance failed", err)
		return result
	}
	result.Behind, err = parseGitCount(out)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if result.Behind == 0 {
		result.AlreadyUpToDate = true
		return result
	}

	// Pull with rebase
	cmd = exec.CommandContext(ctx, "git", "-C", workDir, "pull", "--rebase")
	pullOut, err := cmd.CombinedOutput()
	pullStr := string(pullOut)

	if err != nil {
		// Check for conflicts
		if strings.Contains(pullStr, "CONFLICT") || strings.Contains(pullStr, "conflict") {
			result.Success = false
			result.Conflicts = parseConflicts(pullStr)
			result.Error = "merge conflicts detected"
		} else {
			result.Success = false
			if trimmed := strings.TrimSpace(pullStr); trimmed != "" {
				result.Error = trimmed
			} else {
				result.Error = gitCommandFailure("pull --rebase failed", err)
			}
		}
	} else {
		result.Merged = result.Behind
		result.FastForward = strings.Contains(pullStr, "Fast-forward")
	}

	return result
}

func runGitPush(ctx context.Context, workDir string, force, dryRun bool) *PushResult {
	if ctx == nil {
		ctx = context.Background()
	}
	result := &PushResult{Success: true}

	// Get remote and branch
	cmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	out, err := cmd.Output()
	if err != nil {
		result.Success = false
		result.Error = gitCommandFailure("resolve push upstream failed", err)
		return result
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		result.Success = false
		result.Error = "resolve push upstream failed: unexpected upstream reference"
		return result
	}
	result.Remote = parts[0]
	result.Branch = parts[1]

	// Check how far ahead we are
	cmd = exec.CommandContext(ctx, "git", "-C", workDir, "rev-list", "--count", "@{u}..HEAD")
	out, err = cmd.Output()
	if err != nil {
		result.Success = false
		result.Error = gitCommandFailure("determine push distance failed", err)
		return result
	}
	result.Ahead, err = parseGitCount(out)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if result.Ahead == 0 {
		result.NothingToPush = true
		return result
	}

	if dryRun {
		result.Pushed = result.Ahead
		return result
	}

	// Push
	args := []string{"-C", workDir, "push"}
	if force {
		args = append(args, "--force-with-lease")
	}

	cmd = exec.CommandContext(ctx, "git", args...)
	pushOut, err := cmd.CombinedOutput()

	if err != nil {
		result.Success = false
		if trimmed := strings.TrimSpace(string(pushOut)); trimmed != "" {
			result.Error = trimmed
		} else {
			result.Error = gitCommandFailure("push failed", err)
		}
	} else {
		result.Pushed = result.Ahead
	}

	return result
}

func parseConflicts(output string) []string {
	var conflicts []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "CONFLICT") {
			conflicts = append(conflicts, line)
		}
	}
	return conflicts
}

func parseGitCount(out []byte) (int, error) {
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid git commit count %q", strings.TrimSpace(string(out)))
	}
	return count, nil
}

func gitCommandFailure(action string, err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
			return action + ": " + detail
		}
	}
	return fmt.Sprintf("%s: %v", action, err)
}

func outputGitSyncResult(result GitSyncResult) error {
	if IsJSONOutput() {
		if !result.Success {
			return emitJSONFailureEnvelopeWithCause(result, gitSyncResultError(result))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if err := printGitSyncResult(result); err != nil {
		return err
	}
	if !result.Success {
		return gitSyncResultError(result)
	}
	return nil
}

func gitSyncResultError(result GitSyncResult) error {
	message := strings.TrimSpace(result.Error)
	if message == "" {
		message = "git sync failed"
	}
	return errors.New(message)
}

func printGitSyncResult(result GitSyncResult) error {
	fmt.Printf("Git Sync: %s\n", result.WorkingDir)
	fmt.Println(strings.Repeat("─", 60))

	// Pull result
	if result.PullResult != nil {
		if result.PullResult.AlreadyUpToDate {
			fmt.Println("↓ Pull: Already up to date")
		} else if result.PullResult.Success {
			fmt.Printf("↓ Pull: %d commit(s) merged", result.PullResult.Merged)
			if result.PullResult.FastForward {
				fmt.Print(" (fast-forward)")
			}
			fmt.Println()
		} else {
			fmt.Printf("↓ Pull: FAILED - %s\n", result.PullResult.Error)
			if len(result.PullResult.Conflicts) > 0 {
				fmt.Println("\n⚠ Conflicts detected:")
				for _, c := range result.PullResult.Conflicts {
					fmt.Printf("  %s\n", c)
				}
				fmt.Println("\nResolve conflicts and run:")
				fmt.Println("  git add <files>")
				fmt.Println("  git rebase --continue")
			}
		}
	}

	// Push result
	if result.PushResult != nil {
		fmt.Println()
		if result.PushResult.NothingToPush {
			fmt.Println("↑ Push: Nothing to push")
		} else if result.PushResult.Success {
			fmt.Printf("↑ Push: %d commit(s) pushed", result.PushResult.Pushed)
			if result.PushResult.Remote != "" {
				fmt.Printf(" to %s/%s", result.PushResult.Remote, result.PushResult.Branch)
			}
			fmt.Println()
		} else {
			fmt.Printf("↑ Push: FAILED - %s\n", result.PushResult.Error)
		}
	}

	// Overall status
	fmt.Println()
	if result.Success {
		fmt.Println("✓ Sync complete")
	} else {
		fmt.Println("✗ Sync failed")
		if result.Error != "" {
			fmt.Printf("  Error: %s\n", result.Error)
		}
	}

	return nil
}

func newGitStatusCmd() *cobra.Command {
	var allAgents bool

	cmd := &cobra.Command{
		Use:   "status [session]",
		Short: "Show coordination state: branches, locks, pending changes",
		Long: `Display combined git and Agent Mail status for coordination.

Shows:
  - Current git branch and status
  - File reservations (locks) held by agents
  - Uncommitted changes summary
  - Conflicts if any patterns overlap

Examples:
  ntm git status myproject           # Status for session
  ntm git status                     # Status for current directory
  ntm git status --all-agents        # Include all agents' reservations`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runGitStatus(cmd.Context(), session, allAgents)
		},
	}

	cmd.Flags().BoolVar(&allAgents, "all-agents", false, "Show reservations from all agents")

	return cmd
}

// GitStatusResult represents the combined git and Agent Mail status.
type GitStatusResult struct {
	Success      bool                        `json:"success"`
	Session      string                      `json:"session,omitempty"`
	WorkingDir   string                      `json:"working_dir"`
	Git          *GitInfo                    `json:"git,omitempty"`
	Reservations []agentmail.InboxMessage    `json:"reservations,omitempty"`
	Locks        []agentmail.FileReservation `json:"locks,omitempty"`
	AgentMail    *AgentMailStatus            `json:"agent_mail,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

// GitInfo contains git repository information.
type GitInfo struct {
	Branch          string   `json:"branch"`
	Commit          string   `json:"commit"`
	CommitShort     string   `json:"commit_short"`
	Dirty           bool     `json:"dirty"`
	Ahead           int      `json:"ahead"`
	Behind          int      `json:"behind"`
	StagedFiles     []string `json:"staged_files,omitempty"`
	ModifiedFiles   []string `json:"modified_files,omitempty"`
	UntrackedFiles  []string `json:"untracked_files,omitempty"`
	ConflictedFiles []string `json:"conflicted_files,omitempty"`
}

// AgentMailStatus contains Agent Mail coordination info.
type AgentMailStatus struct {
	Available       bool   `json:"available"`
	RegisteredAgent string `json:"registered_agent,omitempty"`
	LockCount       int    `json:"lock_count"`
	ConflictCount   int    `json:"conflict_count"`
}

func runGitStatus(ctx context.Context, session string, allAgents bool) error {
	session, workDir, err := resolveGitProjectDir(ctx, session)
	if err != nil {
		return err
	}
	agentMailProjectKey := resolveGitAgentMailProjectKey(session, workDir)

	result := GitStatusResult{
		Success:    true,
		Session:    session,
		WorkingDir: workDir,
	}

	// Get git status
	gitInfo, gitErr := getGitInfo(workDir)
	if gitErr != nil {
		result.Git = nil
	} else {
		result.Git = gitInfo
	}

	// Get Agent Mail status
	result.AgentMail = &AgentMailStatus{}
	client := newAgentMailClient(agentMailProjectKey)

	if client.IsAvailable() {
		result.AgentMail.Available = true

		// Try to get session agent info
		if session != "" {
			sessionAgent, err := agentmail.LoadBestSessionAgent(session, agentMailProjectKey)
			if err == nil && sessionAgent != nil {
				result.AgentMail.RegisteredAgent = sessionAgent.AgentName

				// Fetch file reservations for this agent
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				locks, err := fetchAgentLocks(ctx, client, agentMailProjectKey, sessionAgent.AgentName, allAgents)
				cancel()
				if err == nil {
					result.Locks = locks
					result.AgentMail.LockCount = len(locks)
				}
			}
		}
	}

	// Output
	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	return printGitStatus(result)
}

func resolveGitAgentMailProjectKey(session, workDir string) string {
	if strings.TrimSpace(session) == "" {
		return workDir
	}
	return refineAgentMailProjectKey(session, workDir)
}

func resolveGitProjectDir(ctx context.Context, session string) (string, string, error) {
	session, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
	if err != nil {
		return "", "", err
	}

	workDir := ""
	if strings.TrimSpace(session) == "" {
		workDir = resolveProjectDirForSession(session, true)
	} else {
		workDir, err = resolveExplicitProjectDirForSessionContext(ctx, session)
		if err != nil {
			return "", "", err
		}
	}
	workDir = resolveGitAgentMailProjectKey(session, workDir)
	if workDir == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}
	return session, workDir, nil
}

func getGitInfo(dir string) (*GitInfo, error) {
	return getGitInfoContext(context.Background(), dir)
}

func getGitInfoContext(ctx context.Context, dir string) (*GitInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Check if it's a git repo
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("not a git repository")
	}

	info := &GitInfo{}

	// Get current branch
	cmd = exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err == nil {
		info.Branch = strings.TrimSpace(string(out))
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Get commit hash
	cmd = exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	out, err = cmd.Output()
	if err == nil {
		info.Commit = strings.TrimSpace(string(out))
		if len(info.Commit) >= 7 {
			info.CommitShort = info.Commit[:7]
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Get status (porcelain v2 for machine parsing)
	cmd = exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain=v2", "--branch")
	out, err = cmd.Output()
	if err == nil {
		parseGitStatus(info, string(out))
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Check if dirty
	cmd = exec.CommandContext(ctx, "git", "-C", dir, "diff", "--quiet", "HEAD")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		info.Dirty = true
	}

	return info, nil
}

func parseGitStatus(info *GitInfo, output string) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "# branch.ab ") {
			// Parse ahead/behind: # branch.ab +1 -2
			parts := strings.Fields(line)
			for _, p := range parts {
				if strings.HasPrefix(p, "+") {
					fmt.Sscanf(p, "+%d", &info.Ahead)
				} else if strings.HasPrefix(p, "-") {
					fmt.Sscanf(p, "-%d", &info.Behind)
				}
			}
		} else if strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") {
			// Tracked file changes
			parts := strings.Fields(line)
			if len(parts) >= 9 {
				xy := parts[1] // XY status
				file := parts[8]
				if xy[0] != '.' {
					info.StagedFiles = append(info.StagedFiles, file)
				}
				if xy[1] != '.' {
					info.ModifiedFiles = append(info.ModifiedFiles, file)
				}
			}
		} else if strings.HasPrefix(line, "u ") {
			// Unmerged (conflict)
			parts := strings.Fields(line)
			if len(parts) >= 11 {
				info.ConflictedFiles = append(info.ConflictedFiles, parts[10])
			}
		} else if strings.HasPrefix(line, "? ") {
			// Untracked
			file := strings.TrimPrefix(line, "? ")
			info.UntrackedFiles = append(info.UntrackedFiles, file)
		}
	}
}

func fetchAgentLocks(ctx context.Context, client *agentmail.Client, projectKey, agentName string, allAgents bool) ([]agentmail.FileReservation, error) {
	return client.ListReservations(ctx, projectKey, agentName, allAgents)
}

func printGitStatus(result GitStatusResult) error {
	fmt.Printf("Git Status: %s\n", result.WorkingDir)
	fmt.Println(strings.Repeat("─", 60))

	if result.Git != nil {
		// Branch and commit
		branchIcon := "󰘬"
		if result.Git.Dirty {
			branchIcon = "󰘭"
		}
		fmt.Printf("%s Branch: %s", branchIcon, result.Git.Branch)
		if result.Git.CommitShort != "" {
			fmt.Printf(" (%s)", result.Git.CommitShort)
		}
		fmt.Println()

		// Ahead/behind
		if result.Git.Ahead > 0 || result.Git.Behind > 0 {
			fmt.Printf("  ↑%d ↓%d ", result.Git.Ahead, result.Git.Behind)
			if result.Git.Ahead > 0 {
				fmt.Print("(push needed) ")
			}
			if result.Git.Behind > 0 {
				fmt.Print("(pull needed) ")
			}
			fmt.Println()
		}

		// File changes
		if len(result.Git.StagedFiles) > 0 {
			fmt.Printf("\n Staged (%d):\n", len(result.Git.StagedFiles))
			for _, f := range result.Git.StagedFiles {
				fmt.Printf("  + %s\n", f)
			}
		}

		if len(result.Git.ModifiedFiles) > 0 {
			fmt.Printf("\n Modified (%d):\n", len(result.Git.ModifiedFiles))
			for _, f := range result.Git.ModifiedFiles {
				fmt.Printf("  M %s\n", f)
			}
		}

		if len(result.Git.UntrackedFiles) > 0 {
			fmt.Printf("\n Untracked (%d):\n", len(result.Git.UntrackedFiles))
			for _, f := range result.Git.UntrackedFiles {
				fmt.Printf("  ? %s\n", f)
			}
		}

		if len(result.Git.ConflictedFiles) > 0 {
			fmt.Printf("\n⚠ Conflicts (%d):\n", len(result.Git.ConflictedFiles))
			for _, f := range result.Git.ConflictedFiles {
				fmt.Printf("  ! %s\n", f)
			}
		}

		if !result.Git.Dirty && len(result.Git.StagedFiles) == 0 && len(result.Git.UntrackedFiles) == 0 {
			fmt.Println("  Working tree clean")
		}
	} else {
		fmt.Println("  Not a git repository")
	}

	// Agent Mail status
	fmt.Println()
	fmt.Println("Agent Mail:")
	fmt.Println(strings.Repeat("─", 60))

	if result.AgentMail != nil && result.AgentMail.Available {
		fmt.Println("  Status: Available")
		if result.AgentMail.RegisteredAgent != "" {
			fmt.Printf("  Agent: %s\n", result.AgentMail.RegisteredAgent)
		}
		if result.AgentMail.LockCount > 0 {
			fmt.Printf("  Active locks: %d\n", result.AgentMail.LockCount)
		} else {
			fmt.Println("  No active file reservations")
		}
	} else {
		fmt.Println("  Status: Unavailable")
	}

	return nil
}
