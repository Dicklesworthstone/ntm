package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/git"
)

// worktreeCmd represents the worktree command group
var worktreeCmd = &cobra.Command{
	Use:   "worktree",
	Short: "Manage git worktrees for agent isolation",
	Long: `Git worktree isolation allows multiple agents to work on the same repository
simultaneously without conflicts by using separate git worktrees.

Each agent gets their own isolated working directory and branch, allowing for:
- Parallel development without file conflicts
- Independent git state per agent
- Safe experimentation and rollback
- Coordinated multi-agent workflows`,
}

// worktreeListCmd lists all agent worktrees
var worktreeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all agent worktrees",
	Long:  "List all git worktrees created for agent isolation",
	RunE:  runWorktreeList,
}

// worktreeProvisionCmd creates a new worktree for an agent
var worktreeProvisionCmd = &cobra.Command{
	Use:   "provision <agent-name> <session-id>",
	Short: "Create a new worktree for an agent",
	Long:  "Create an isolated git worktree and branch for an agent to work in",
	Args:  cobra.ExactArgs(2),
	RunE:  runWorktreeProvision,
}

// worktreeRemoveCmd removes an agent's worktree
var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <agent-name> <session-id>",
	Short: "Remove an agent's worktree",
	Long:  "Remove an agent's worktree and associated branch",
	Args:  cobra.ExactArgs(2),
	RunE:  runWorktreeRemove,
}

// worktreeCleanupCmd removes stale worktrees
var worktreeCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up stale worktrees",
	Long:  "Remove worktrees that haven't been used recently",
	RunE:  runWorktreeCleanup,
}

// worktreeSyncCmd synchronizes a worktree with its base branch
var worktreeSyncCmd = &cobra.Command{
	Use:   "sync <worktree-path>",
	Short: "Synchronize a worktree with its base branch",
	Long:  "Fetch and merge the latest changes from the base branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorktreeSync,
}

// worktreeAutoProvisionCmd automatically provisions worktrees for all agents in a session
var worktreeAutoProvisionCmd = &cobra.Command{
	Use:   "auto-provision <session-name>",
	Short: "Automatically provision worktrees for all agents in a session",
	Long: `Automatically detects all agent panes in a session and provisions
isolated git worktrees for each agent. Each agent will get:
- A unique git worktree (working directory)
- A dedicated branch for isolated development
- Automatic directory change to their worktree

This allows multiple agents to work on the same repository simultaneously
without file conflicts or git state interference.`,
	Args: cobra.ExactArgs(1),
	RunE: runWorktreeAutoProvision,
}

// worktreeStatusCmd shows the status of worktrees for a session
var worktreeStatusCmd = &cobra.Command{
	Use:   "status [session-name]",
	Short: "Show worktree status for a session",
	Long:  "Show which agents have worktrees and their current status",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorktreeStatus,
}

// worktreeCleanSessionCmd cleans up worktrees for a specific session
var worktreeCleanSessionCmd = &cobra.Command{
	Use:   "clean-session <session-name>",
	Short: "Clean up worktrees for a specific session",
	Long:  "Remove all worktrees and branches associated with a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorktreeCleanSession,
}

var (
	// Flags for worktree commands
	worktreeMaxAge = 7 * 24 * time.Hour // Default: 7 days
	outputFormat   string               // json or table
	dryRun         bool                 // Preview changes without applying
)

func init() {
	// Add subcommands
	worktreeCmd.AddCommand(worktreeListCmd)
	worktreeCmd.AddCommand(worktreeProvisionCmd)
	worktreeCmd.AddCommand(worktreeRemoveCmd)
	worktreeCmd.AddCommand(worktreeCleanupCmd)
	worktreeCmd.AddCommand(worktreeSyncCmd)
	worktreeCmd.AddCommand(worktreeAutoProvisionCmd)
	worktreeCmd.AddCommand(worktreeStatusCmd)
	worktreeCmd.AddCommand(worktreeCleanSessionCmd)

	// Add flags
	worktreeListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table|json)")

	worktreeCleanupCmd.Flags().DurationVar(&worktreeMaxAge, "max-age", worktreeMaxAge, "Maximum age of worktrees to keep")
	worktreeCleanupCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be cleaned up without doing it")

	// Add to root command
	rootCmd.AddCommand(worktreeCmd)
}

// runWorktreeList handles the 'ntm worktree list' command
func runWorktreeList(cmd *cobra.Command, args []string) error {
	projectDir, err := getProjectDir()
	if err != nil {
		return fmt.Errorf("failed to determine project directory: %w", err)
	}

	wm, err := git.NewWorktreeManager(projectDir)
	if err != nil {
		return fmt.Errorf("failed to initialize worktree manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Sort by agent name, then by last used time
	sort.Slice(worktrees, func(i, j int) bool {
		if worktrees[i].Agent != worktrees[j].Agent {
			return worktrees[i].Agent < worktrees[j].Agent
		}
		return worktrees[i].LastUsed.After(worktrees[j].LastUsed)
	})

	switch outputFormat {
	case "json":
		return printWorktreesJSON(worktrees)
	default:
		return printWorktreesTable(worktrees)
	}
}

// runWorktreeProvision handles the 'ntm worktree provision' command
func runWorktreeProvision(cmd *cobra.Command, args []string) error {
	agentName := args[0]
	sessionID := args[1]

	projectDir, err := getProjectDir()
	if err != nil {
		return fmt.Errorf("failed to determine project directory: %w", err)
	}

	wm, err := git.NewWorktreeManager(projectDir)
	if err != nil {
		return fmt.Errorf("failed to initialize worktree manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("Provisioning worktree for agent '%s' with session '%s'...\n", agentName, sessionID)

	worktree, err := wm.ProvisionWorktree(ctx, agentName, sessionID)
	if err != nil {
		return fmt.Errorf("failed to provision worktree: %w", err)
	}

	fmt.Printf("✓ Worktree created successfully!\n")
	fmt.Printf("  Path:   %s\n", worktree.Path)
	fmt.Printf("  Branch: %s\n", worktree.Branch)
	fmt.Printf("  Commit: %s\n", worktree.Commit[:8])

	return nil
}

// runWorktreeRemove handles the 'ntm worktree remove' command
func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	agentName := args[0]
	sessionID := args[1]

	projectDir, err := getProjectDir()
	if err != nil {
		return fmt.Errorf("failed to determine project directory: %w", err)
	}

	wm, err := git.NewWorktreeManager(projectDir)
	if err != nil {
		return fmt.Errorf("failed to initialize worktree manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("Removing worktree for agent '%s' with session '%s'...\n", agentName, sessionID)

	if err := wm.RemoveWorktree(ctx, agentName, sessionID); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	fmt.Printf("✓ Worktree removed successfully!\n")

	return nil
}

// runWorktreeCleanup handles the 'ntm worktree cleanup' command
func runWorktreeCleanup(cmd *cobra.Command, args []string) error {
	projectDir, err := getProjectDir()
	if err != nil {
		return fmt.Errorf("failed to determine project directory: %w", err)
	}

	wm, err := git.NewWorktreeManager(projectDir)
	if err != nil {
		return fmt.Errorf("failed to initialize worktree manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if dryRun {
		// Show what would be cleaned up
		worktrees, err := wm.ListWorktrees(ctx)
		if err != nil {
			return fmt.Errorf("failed to list worktrees: %w", err)
		}

		cutoff := time.Now().Add(-worktreeMaxAge)
		var staleWorktrees []*git.WorktreeInfo

		for _, wt := range worktrees {
			if wt.LastUsed.Before(cutoff) && strings.HasPrefix(wt.Branch, "agent/") {
				staleWorktrees = append(staleWorktrees, wt)
			}
		}

		if len(staleWorktrees) == 0 {
			fmt.Println("No stale worktrees found.")
			return nil
		}

		fmt.Printf("Would clean up %d stale worktrees older than %v:\n", len(staleWorktrees), worktreeMaxAge)
		for _, wt := range staleWorktrees {
			age := time.Since(wt.LastUsed)
			fmt.Printf("  %s (agent: %s, age: %v)\n", wt.Path, wt.Agent, age.Truncate(time.Hour))
		}
		fmt.Println("\nRun without --dry-run to actually remove these worktrees.")
		return nil
	}

	fmt.Printf("Cleaning up worktrees older than %v...\n", worktreeMaxAge)

	if err := wm.CleanupStaleWorktrees(ctx, worktreeMaxAge); err != nil {
		return fmt.Errorf("failed to cleanup stale worktrees: %w", err)
	}

	fmt.Printf("✓ Cleanup completed!\n")

	return nil
}

// runWorktreeSync handles the 'ntm worktree sync' command
func runWorktreeSync(cmd *cobra.Command, args []string) error {
	worktreePath := args[0]

	// Verify the path exists and is a git repository
	if !git.IsGitRepository(worktreePath) {
		return fmt.Errorf("path is not a git repository: %s", worktreePath)
	}

	// Get the parent project directory to create a manager
	// This is a bit hacky - ideally we'd track the parent repo differently
	parentDir := filepath.Dir(worktreePath)
	if !git.IsGitRepository(parentDir) {
		// Try one level up (common pattern: /project/../agent-name/)
		parentDir = filepath.Dir(parentDir)
		if !git.IsGitRepository(parentDir) {
			return fmt.Errorf("could not find parent git repository for worktree: %s", worktreePath)
		}
	}

	wm, err := git.NewWorktreeManager(parentDir)
	if err != nil {
		return fmt.Errorf("failed to initialize worktree manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("Synchronizing worktree %s...\n", worktreePath)

	if err := wm.SyncWorktree(ctx, worktreePath); err != nil {
		return fmt.Errorf("failed to sync worktree: %w", err)
	}

	fmt.Printf("✓ Worktree synchronized successfully!\n")

	return nil
}

// Helper functions

// getProjectDir determines the current project directory
func getProjectDir() (string, error) {
	// Try current working directory first
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	if git.IsGitRepository(cwd) {
		return cwd, nil
	}

	// Could add more sophisticated project detection here
	// For now, just require the command to be run from within a git repo
	return "", fmt.Errorf("not in a git repository (current dir: %s)", cwd)
}

// printWorktreesTable prints worktrees in a formatted table
func printWorktreesTable(worktrees []*git.WorktreeInfo) error {
	if len(worktrees) == 0 {
		fmt.Println("No agent worktrees found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "AGENT\tBRANCH\tPATH\tCOMMIT\tLAST USED\n")

	for _, wt := range worktrees {
		lastUsed := wt.LastUsed.Format("2006-01-02 15:04")
		commitShort := wt.Commit
		if len(commitShort) > 8 {
			commitShort = commitShort[:8]
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			wt.Agent,
			wt.Branch,
			wt.Path,
			commitShort,
			lastUsed,
		)
	}

	return nil
}

// printWorktreesJSON prints worktrees in JSON format
func printWorktreesJSON(worktrees []*git.WorktreeInfo) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(worktrees)
}

// runWorktreeAutoProvision handles the 'ntm worktree auto-provision' command
func runWorktreeAutoProvision(cmd *cobra.Command, args []string) error {
	sessionName := args[0]

	cfg, err := config.Load("config.toml")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	service := git.NewWorktreeService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Printf("Auto-provisioning worktrees for session '%s'...\n", sessionName)

	response, err := service.AutoProvisionSession(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("auto-provisioning failed: %w", err)
	}

	// Print results
	if len(response.Provisions) > 0 {
		fmt.Printf("\n✓ Successfully provisioned %d worktrees:\n", len(response.Provisions))
		for _, provision := range response.Provisions {
			fmt.Printf("  %s (%s): %s → %s\n",
				provision.AgentType,
				provision.PaneID,
				provision.Branch,
				provision.WorktreePath,
			)
		}
	}

	if len(response.Skipped) > 0 {
		fmt.Printf("\n⚠ Skipped %d agents:\n", len(response.Skipped))
		for _, skipped := range response.Skipped {
			fmt.Printf("  %s (%s): %s\n",
				skipped.AgentType,
				skipped.PaneID,
				skipped.Reason,
			)
		}
	}

	if len(response.Errors) > 0 {
		fmt.Printf("\n✗ Failed to provision %d worktrees:\n", len(response.Errors))
		for _, provErr := range response.Errors {
			fmt.Printf("  %s (%s): %s\n",
				provErr.AgentType,
				provErr.PaneID,
				provErr.Error,
			)
		}
	}

	fmt.Printf("\nProcessing completed in %s\n", response.ProcessingTime)

	return nil
}

// runWorktreeStatus handles the 'ntm worktree status' command
func runWorktreeStatus(cmd *cobra.Command, args []string) error {
	var sessionName string
	if len(args) > 0 {
		sessionName = args[0]
	} else {
		// Try to detect session from tmux
		if session, err := getCurrentTmuxSession(); err == nil {
			sessionName = session
		} else {
			return fmt.Errorf("session name required (not in tmux session)")
		}
	}

	cfg, err := config.Load("config.toml")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	service := git.NewWorktreeService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	worktrees, err := service.GetSessionWorktreeStatus(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	if len(worktrees) == 0 {
		fmt.Printf("No worktrees found for session '%s'\n", sessionName)
		return nil
	}

	fmt.Printf("Worktree Status for Session: %s\n\n", sessionName)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "AGENT\tBRANCH\tPATH\tCOMMIT\tLAST USED\n")

	for agentType, wt := range worktrees {
		lastUsed := wt.LastUsed.Format("2006-01-02 15:04")
		commitShort := wt.Commit
		if len(commitShort) > 8 {
			commitShort = commitShort[:8]
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			agentType,
			wt.Branch,
			wt.Path,
			commitShort,
			lastUsed,
		)
	}

	return nil
}

// runWorktreeCleanSession handles the 'ntm worktree clean-session' command
func runWorktreeCleanSession(cmd *cobra.Command, args []string) error {
	sessionName := args[0]

	cfg, err := config.Load("config.toml")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	service := git.NewWorktreeService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("Cleaning up worktrees for session '%s'...\n", sessionName)

	if err := service.CleanupSessionWorktrees(ctx, sessionName); err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	fmt.Printf("✓ Session worktrees cleaned up successfully!\n")

	return nil
}

// getCurrentTmuxSession returns the current tmux session name if we're in one
func getCurrentTmuxSession() (string, error) {
	tmuxSession := os.Getenv("TMUX")
	if tmuxSession == "" {
		return "", fmt.Errorf("not in a tmux session")
	}

	// Extract session name from tmux environment
	// This is a simplified approach - could be enhanced
	cmd := exec.Command("tmux", "display-message", "-p", "#S")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get tmux session: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}