// Package git provides git worktree isolation functionality for multi-agent coordination.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorktreeManager handles git worktree creation and management for agent isolation
type WorktreeManager struct {
	projectDir string
	baseRepo   string
}

// NewWorktreeManager creates a new worktree manager for a project
func NewWorktreeManager(projectDir string) (*WorktreeManager, error) {
	// Verify this is a git repository
	if !IsGitRepository(projectDir) {
		return nil, fmt.Errorf("directory is not a git repository: %s", projectDir)
	}

	return &WorktreeManager{
		projectDir: projectDir,
		baseRepo:   projectDir,
	}, nil
}

// WorktreeInfo represents information about a git worktree
type WorktreeInfo struct {
	Path       string    `json:"path"`
	Branch     string    `json:"branch"`
	Commit     string    `json:"commit"`
	Agent      string    `json:"agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsed   time.Time `json:"last_used"`
}

// ProvisionWorktree creates an isolated worktree for an agent
func (wm *WorktreeManager) ProvisionWorktree(ctx context.Context, agentName, sessionID string) (*WorktreeInfo, error) {
	// Generate a unique worktree name
	worktreeName := fmt.Sprintf("agent-%s-%s", agentName, sessionID[:8])
	workingDir := filepath.Join(wm.baseRepo, "..", worktreeName)

	// Check if worktree already exists
	if exists, err := wm.worktreeExists(worktreeName); err != nil {
		return nil, fmt.Errorf("failed to check worktree existence: %w", err)
	} else if exists {
		// Return existing worktree info
		return wm.getWorktreeInfo(worktreeName)
	}

	// Create a new branch for this agent
	branchName := fmt.Sprintf("agent/%s/%s", agentName, sessionID[:8])

	// Get current branch and commit for base
	currentBranch, err := wm.getCurrentBranch()
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}

	// Create the worktree with a new branch
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, workingDir, currentBranch)
	cmd.Dir = wm.baseRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w\nOutput: %s", err, string(output))
	}

	// Get commit hash
	commit, err := wm.getCommitHash(workingDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit hash: %w", err)
	}

	worktreeInfo := &WorktreeInfo{
		Path:      workingDir,
		Branch:    branchName,
		Commit:    commit,
		Agent:     agentName,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}

	return worktreeInfo, nil
}

// ListWorktrees returns all worktrees associated with agents
func (wm *WorktreeManager) ListWorktrees(ctx context.Context) ([]*WorktreeInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = wm.baseRepo
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return wm.parseWorktreeList(string(output))
}

// RemoveWorktree removes a worktree and its associated branch
func (wm *WorktreeManager) RemoveWorktree(ctx context.Context, agentName, sessionID string) error {
	worktreeName := fmt.Sprintf("agent-%s-%s", agentName, sessionID[:8])
	workingDir := filepath.Join(wm.baseRepo, "..", worktreeName)
	branchName := fmt.Sprintf("agent/%s/%s", agentName, sessionID[:8])

	// Remove the worktree
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", workingDir)
	cmd.Dir = wm.baseRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		// If worktree doesn't exist, that's OK
		if !strings.Contains(string(output), "not a working tree") {
			return fmt.Errorf("failed to remove worktree: %w\nOutput: %s", err, string(output))
		}
	}

	// Remove the branch
	cmd = exec.CommandContext(ctx, "git", "branch", "-D", branchName)
	cmd.Dir = wm.baseRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		// If branch doesn't exist, that's OK
		if !strings.Contains(string(output), "not found") {
			return fmt.Errorf("failed to remove branch: %w\nOutput: %s", err, string(output))
		}
	}

	return nil
}

// CleanupStaleWorktrees removes worktrees that haven't been used recently
func (wm *WorktreeManager) CleanupStaleWorktrees(ctx context.Context, maxAge time.Duration) error {
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)

	for _, wt := range worktrees {
		if wt.LastUsed.Before(cutoff) && strings.HasPrefix(wt.Branch, "agent/") {
			// Extract agent and session info from branch name
			parts := strings.Split(wt.Branch, "/")
			if len(parts) >= 3 {
				agentName := parts[1]
				sessionID := parts[2] + "00000000" // Pad to ensure minimum length
				if err := wm.RemoveWorktree(ctx, agentName, sessionID); err != nil {
					// Log error but continue cleanup
					fmt.Printf("Warning: failed to remove stale worktree for %s: %v\n", wt.Path, err)
				}
			}
		}
	}

	return nil
}

// SyncWorktree ensures a worktree is up-to-date with its base branch
func (wm *WorktreeManager) SyncWorktree(ctx context.Context, worktreePath string) error {
	// Fetch latest changes
	cmd := exec.CommandContext(ctx, "git", "fetch", "origin")
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch: %w\nOutput: %s", err, string(output))
	}

	// Get the base branch (what this agent branch was created from)
	// For now, assume 'main' - this could be enhanced to track the actual base
	cmd = exec.CommandContext(ctx, "git", "merge", "origin/main")
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to merge base branch: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Helper methods

// IsGitRepository checks if a directory is a git repository
func IsGitRepository(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	err := cmd.Run()
	return err == nil
}

// worktreeExists checks if a worktree with the given name exists
func (wm *WorktreeManager) worktreeExists(name string) (bool, error) {
	worktreePath := filepath.Join(wm.baseRepo, ".git", "worktrees", name)
	_, err := os.Stat(worktreePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// getCurrentBranch returns the current branch name
func (wm *WorktreeManager) getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = wm.baseRepo
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getCommitHash returns the current commit hash for a worktree
func (wm *WorktreeManager) getCommitHash(worktreePath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getWorktreeInfo retrieves information about an existing worktree
func (wm *WorktreeManager) getWorktreeInfo(name string) (*WorktreeInfo, error) {
	workingDir := filepath.Join(wm.baseRepo, "..", name)

	// Get branch name
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workingDir
	branchOutput, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))

	// Get commit hash
	commit, err := wm.getCommitHash(workingDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// Extract agent name from worktree name
	agentName := "unknown"
	if strings.HasPrefix(name, "agent-") {
		parts := strings.Split(name, "-")
		if len(parts) >= 2 {
			agentName = parts[1]
		}
	}

	// Get last modified time of worktree directory as proxy for last used
	stat, err := os.Stat(workingDir)
	lastUsed := time.Now()
	if err == nil {
		lastUsed = stat.ModTime()
	}

	return &WorktreeInfo{
		Path:      workingDir,
		Branch:    branch,
		Commit:    commit,
		Agent:     agentName,
		CreatedAt: time.Now(), // We can't easily determine creation time
		LastUsed:  lastUsed,
	}, nil
}

// parseWorktreeList parses the output of 'git worktree list --porcelain'
func (wm *WorktreeManager) parseWorktreeList(output string) ([]*WorktreeInfo, error) {
	var worktrees []*WorktreeInfo

	// Split into worktree blocks (separated by blank lines)
	blocks := regexp.MustCompile(`\n\s*\n`).Split(strings.TrimSpace(output), -1)

	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}

		var path, branch, commit string
		lines := strings.Split(block, "\n")

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "worktree ") {
				path = strings.TrimPrefix(line, "worktree ")
			} else if strings.HasPrefix(line, "branch ") {
				branch = strings.TrimPrefix(line, "branch ")
				branch = strings.TrimPrefix(branch, "refs/heads/")
			} else if strings.HasPrefix(line, "HEAD ") {
				commit = strings.TrimPrefix(line, "HEAD ")
			}
		}

		// Only include agent worktrees
		if path != "" && strings.Contains(path, "agent-") {
			// Extract agent name from path
			agentName := "unknown"
			basename := filepath.Base(path)
			if strings.HasPrefix(basename, "agent-") {
				parts := strings.Split(basename, "-")
				if len(parts) >= 2 {
					agentName = parts[1]
				}
			}

			// Get last modified time
			lastUsed := time.Now()
			if stat, err := os.Stat(path); err == nil {
				lastUsed = stat.ModTime()
			}

			worktrees = append(worktrees, &WorktreeInfo{
				Path:      path,
				Branch:    branch,
				Commit:    commit,
				Agent:     agentName,
				CreatedAt: time.Now(), // Can't determine actual creation time
				LastUsed:  lastUsed,
			})
		}
	}

	return worktrees, nil
}