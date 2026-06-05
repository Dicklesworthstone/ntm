// Package worktrees provides Git worktree isolation for multi-agent sessions.
package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout is the maximum duration for any git command in the worktrees package.
const gitTimeout = 30 * time.Second

// gitCombinedOutput runs a git command with the standard timeout and returns combined stdout/stderr.
func gitCombinedOutput(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// gitRun runs a git command with the standard timeout.
func gitRun(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// gitOutput runs a git command with the standard timeout and returns stdout.
func gitOutput(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// WorktreeManager manages Git worktrees for agent isolation
type WorktreeManager struct {
	projectPath string
	session     string
}

// WorktreeInfo contains information about an agent's worktree
type WorktreeInfo struct {
	AgentName  string `json:"agent_name"`
	Path       string `json:"path"`
	BranchName string `json:"branch_name"`
	SessionID  string `json:"session_id"`
	Created    bool   `json:"created"`
	Error      string `json:"error,omitempty"`
}

// NewManager creates a new WorktreeManager
func NewManager(projectPath, session string) *WorktreeManager {
	return &WorktreeManager{
		projectPath: projectPath,
		session:     session,
	}
}

func (m *WorktreeManager) worktreesRoot() string {
	return filepath.Join(m.projectPath, ".ntm", "worktrees")
}

func (m *WorktreeManager) sessionRoot() string {
	return filepath.Join(m.worktreesRoot(), m.session)
}

func (m *WorktreeManager) worktreePath(agentName string) string {
	return filepath.Join(m.sessionRoot(), agentName)
}

func validateWorktreeComponent(kind, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s cannot be empty", kind)
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%s cannot be an absolute path: %q", kind, value)
	}
	if trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("%s cannot be %q", kind, trimmed)
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("%s cannot contain path separators: %q", kind, value)
	}
	return trimmed, nil
}

func (m *WorktreeManager) buildWorktreeInfo(agentName string) (*WorktreeInfo, error) {
	sessionName, err := validateWorktreeComponent("session", m.session)
	if err != nil {
		return nil, err
	}
	agentName, err = validateWorktreeComponent("agent name", agentName)
	if err != nil {
		return nil, err
	}

	return &WorktreeInfo{
		AgentName:  agentName,
		Path:       filepath.Join(m.worktreesRoot(), sessionName, agentName),
		BranchName: fmt.Sprintf("ntm/%s/%s", sessionName, agentName),
		SessionID:  sessionName,
	}, nil
}

// CreateForAgent creates a new worktree for the specified agent
func (m *WorktreeManager) CreateForAgent(agentName string) (*WorktreeInfo, error) {
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return &WorktreeInfo{
			AgentName: strings.TrimSpace(agentName),
			SessionID: strings.TrimSpace(m.session),
			Error:     err.Error(),
		}, err
	}
	worktreePath := info.Path

	// Ensure the session-specific worktree directory exists.
	worktreeDir := filepath.Dir(worktreePath)
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		info.Error = fmt.Sprintf("failed to create worktree directory: %v", err)
		return info, err
	}

	// Check if worktree already exists
	if stat, err := os.Stat(worktreePath); err == nil {
		if !stat.IsDir() {
			info.Error = "worktree path exists but is not a directory"
			return info, fmt.Errorf("worktree path exists but is not a directory: %s", worktreePath)
		}
		if !m.isValidWorktree(worktreePath) {
			info.Error = "invalid or stale worktree"
			return info, fmt.Errorf("worktree path exists but is not a valid git worktree: %s", worktreePath)
		}
		// Defend against the silent-contamination scenario from #145: the
		// worktree path already exists, but a *different* prior spawn
		// created it under a different branch. Returning Created=false
		// here would silently hand the second spawn the first spawn's
		// checkout — that's exactly the cross-contamination the issue
		// reports. Compare the checked-out branch against what we'd have
		// created (`ntm/<session>/<agent>`); on mismatch, refuse loudly
		// so the caller has to either reuse the same agent name or pick
		// a non-colliding path. See ntm#145.
		if currentBranch, branchErr := worktreeCurrentBranch(worktreePath); branchErr == nil &&
			currentBranch != "" && currentBranch != info.BranchName {
			info.Error = fmt.Sprintf(
				"worktree path %s already exists on branch %q; would collide with the new branch %q (likely cross-contamination — see ntm#145; pass --worktree-name to disambiguate)",
				worktreePath, currentBranch, info.BranchName,
			)
			return info, fmt.Errorf("%s", info.Error)
		}
		info.Created = false
		return info, nil
	} else if !os.IsNotExist(err) {
		info.Error = fmt.Sprintf("failed to stat worktree path: %v", err)
		return info, err
	}

	// Create the worktree with new branch
	output, err := gitCombinedOutput(m.projectPath, "worktree", "add", "-b", info.BranchName, worktreePath)
	if err != nil {
		info.Error = fmt.Sprintf("git worktree add failed: %v: %s", err, string(output))
		return info, fmt.Errorf("failed to create worktree: %w", err)
	}

	info.Created = true
	return info, nil
}

// ListWorktrees returns information about all worktrees for the current session
func (m *WorktreeManager) ListWorktrees() ([]*WorktreeInfo, error) {
	sessionName, err := validateWorktreeComponent("session", m.session)
	if err != nil {
		return nil, err
	}
	worktreesDir := filepath.Join(m.worktreesRoot(), sessionName)

	// Check if worktrees directory exists
	if _, err := os.Stat(worktreesDir); os.IsNotExist(err) {
		return []*WorktreeInfo{}, nil
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read worktrees directory: %w", err)
	}

	var worktrees []*WorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		agentName := entry.Name()
		worktreePath := filepath.Join(worktreesDir, agentName)
		branchName := fmt.Sprintf("ntm/%s/%s", sessionName, agentName)

		info := &WorktreeInfo{
			AgentName:  agentName,
			Path:       worktreePath,
			BranchName: branchName,
			SessionID:  sessionName,
			Created:    true,
		}

		// Check if the worktree is still valid
		if !m.isValidWorktree(worktreePath) {
			info.Error = "invalid or stale worktree"
		}

		worktrees = append(worktrees, info)
	}

	return worktrees, nil
}

// MergeBack merges an agent's worktree changes back to the main branch
func (m *WorktreeManager) MergeBack(agentName string) error {
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return err
	}
	branchName := info.BranchName

	// Switch to the canonical main branch in the primary worktree.
	if err := gitRun(m.projectPath, "checkout", "main"); err != nil {
		return fmt.Errorf("failed to checkout main branch: %w", err)
	}

	// Merge the agent's branch
	output, err := gitCombinedOutput(
		m.projectPath,
		"merge",
		branchName,
		"--no-ff",
		"-m",
		fmt.Sprintf("Merge agent %s work from session %s", agentName, m.session),
	)
	if err != nil {
		return fmt.Errorf("failed to merge branch %s: %v: %s", branchName, err, string(output))
	}

	return nil
}

// RemoveWorktree removes a specific agent's worktree
func (m *WorktreeManager) RemoveWorktree(agentName string) error {
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return err
	}
	worktreePath := info.Path
	branchName := info.BranchName

	// Remove the worktree
	output, err := gitCombinedOutput(m.projectPath, "worktree", "remove", worktreePath, "--force")
	if err != nil {
		// If removal failed, try to prune and remove manually
		_ = gitRun(m.projectPath, "worktree", "prune") // Ignore errors for prune

		// Try to remove directory manually
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("failed to remove worktree %s: %v: %s", agentName, err, string(output))
		}
	}

	// Delete the branch
	_ = gitRun(m.projectPath, "branch", "-D", branchName) // Ignore errors as branch might not exist

	return nil
}

// Cleanup removes all worktrees for the current session
func (m *WorktreeManager) Cleanup() error {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return fmt.Errorf("failed to list worktrees for cleanup: %w", err)
	}
	sessionName, err := validateWorktreeComponent("session", m.session)
	if err != nil {
		return err
	}

	var errors []string
	for _, wt := range worktrees {
		if err := m.RemoveWorktree(wt.AgentName); err != nil {
			errors = append(errors, fmt.Sprintf("failed to remove worktree %s: %v", wt.AgentName, err))
		}
	}

	// Remove the session directory if empty, then the shared root if it is empty too.
	sessionDir := filepath.Join(m.worktreesRoot(), sessionName)
	if entries, err := os.ReadDir(sessionDir); err == nil && len(entries) == 0 {
		_ = os.Remove(sessionDir) // Ignore error for optional cleanup
	}
	worktreesDir := m.worktreesRoot()
	if entries, err := os.ReadDir(worktreesDir); err == nil && len(entries) == 0 {
		_ = os.Remove(worktreesDir) // Ignore error for optional cleanup
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// isValidWorktree checks if a worktree path is still valid
func (m *WorktreeManager) isValidWorktree(worktreePath string) bool {
	// Check if .git file exists (worktrees have a .git file pointing to the main repo)
	gitPath := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		return false
	}

	// Check if it's recognized by git worktree list
	output, err := gitOutput(m.projectPath, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}

	return worktreeListed(output, worktreePath)
}

// worktreeCurrentBranch reads the checked-out branch name for the worktree
// at the given path. Returns "" with no error for detached HEAD or when
// the path isn't a git worktree. Used by CreateForAgent to detect
// cross-contamination on path collision (see ntm#145).
func worktreeCurrentBranch(worktreePath string) (string, error) {
	output, err := gitOutput(worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// `symbolic-ref --quiet` exits non-zero (without writing) on
		// detached HEAD; treat as "no branch" rather than an error.
		if len(strings.TrimSpace(string(output))) == 0 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func worktreeListed(output []byte, worktreePath string) bool {
	target := filepath.Clean(worktreePath)
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		listed := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if listed == "" {
			continue
		}
		if filepath.Clean(listed) == target {
			return true
		}
	}
	return false
}

// GetWorktreeForAgent returns worktree information for a specific agent
func (m *WorktreeManager) GetWorktreeForAgent(agentName string) (*WorktreeInfo, error) {
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return &WorktreeInfo{
			AgentName: strings.TrimSpace(agentName),
			SessionID: strings.TrimSpace(m.session),
			Error:     err.Error(),
		}, err
	}
	worktreePath := info.Path

	// Check if worktree exists
	stat, err := os.Stat(worktreePath)
	if os.IsNotExist(err) {
		info.Created = false
		info.Error = "worktree does not exist"
		return info, nil
	}
	if err != nil {
		info.Error = fmt.Sprintf("failed to stat worktree path: %v", err)
		return info, err
	}

	info.Created = true
	if !stat.IsDir() {
		info.Error = "worktree path exists but is not a directory"
		return info, nil
	}
	if !m.isValidWorktree(worktreePath) {
		info.Error = "invalid or stale worktree"
	}

	return info, nil
}
