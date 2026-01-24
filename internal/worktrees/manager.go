// Package worktrees provides Git worktree isolation for multi-agent sessions.
package worktrees

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// CreateForAgent creates a new worktree for the specified agent
func (m *WorktreeManager) CreateForAgent(agentName string) (*WorktreeInfo, error) {
	info := &WorktreeInfo{
		AgentName:  agentName,
		SessionID:  m.session,
		BranchName: fmt.Sprintf("ntm/%s/%s", m.session, agentName),
	}

	// Construct worktree path
	worktreePath := filepath.Join(m.projectPath, ".ntm", "worktrees", agentName)
	info.Path = worktreePath

	// Ensure .ntm/worktrees directory exists
	worktreeDir := filepath.Dir(worktreePath)
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		info.Error = fmt.Sprintf("failed to create worktree directory: %v", err)
		return info, err
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		info.Created = false
		return info, nil
	}

	// Create the worktree with new branch
	cmd := exec.Command("git", "worktree", "add", "-b", info.BranchName, worktreePath)
	cmd.Dir = m.projectPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		info.Error = fmt.Sprintf("git worktree add failed: %v: %s", err, string(output))
		return info, fmt.Errorf("failed to create worktree: %w", err)
	}

	info.Created = true
	return info, nil
}

// ListWorktrees returns information about all worktrees for the current session
func (m *WorktreeManager) ListWorktrees() ([]*WorktreeInfo, error) {
	worktreesDir := filepath.Join(m.projectPath, ".ntm", "worktrees")

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
		branchName := fmt.Sprintf("ntm/%s/%s", m.session, agentName)

		info := &WorktreeInfo{
			AgentName:  agentName,
			Path:       worktreePath,
			BranchName: branchName,
			SessionID:  m.session,
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
	branchName := fmt.Sprintf("ntm/%s/%s", m.session, agentName)

	// Switch to main branch in main worktree
	cmd := exec.Command("git", "checkout", "main")
	cmd.Dir = m.projectPath
	if err := cmd.Run(); err != nil {
		// Try master if main doesn't exist
		cmd = exec.Command("git", "checkout", "master")
		cmd.Dir = m.projectPath
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout main/master branch: %w", err)
		}
	}

	// Merge the agent's branch
	cmd = exec.Command("git", "merge", branchName, "--no-ff", "-m",
		fmt.Sprintf("Merge agent %s work from session %s", agentName, m.session))
	cmd.Dir = m.projectPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to merge branch %s: %v: %s", branchName, err, string(output))
	}

	return nil
}

// RemoveWorktree removes a specific agent's worktree
func (m *WorktreeManager) RemoveWorktree(agentName string) error {
	worktreePath := filepath.Join(m.projectPath, ".ntm", "worktrees", agentName)
	branchName := fmt.Sprintf("ntm/%s/%s", m.session, agentName)

	// Remove the worktree
	cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
	cmd.Dir = m.projectPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		// If removal failed, try to prune and remove manually
		cmd = exec.Command("git", "worktree", "prune")
		cmd.Dir = m.projectPath
		_ = cmd.Run() // Ignore errors for prune

		// Try to remove directory manually
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("failed to remove worktree %s: %v: %s", agentName, err, string(output))
		}
	}

	// Delete the branch
	cmd = exec.Command("git", "branch", "-D", branchName)
	cmd.Dir = m.projectPath
	_ = cmd.Run() // Ignore errors as branch might not exist

	return nil
}

// Cleanup removes all worktrees for the current session
func (m *WorktreeManager) Cleanup() error {
	worktrees, err := m.ListWorktrees()
	if err != nil {
		return fmt.Errorf("failed to list worktrees for cleanup: %w", err)
	}

	var errors []string
	for _, wt := range worktrees {
		if err := m.RemoveWorktree(wt.AgentName); err != nil {
			errors = append(errors, fmt.Sprintf("failed to remove worktree %s: %v", wt.AgentName, err))
		}
	}

	// Remove the worktrees directory if empty
	worktreesDir := filepath.Join(m.projectPath, ".ntm", "worktrees")
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
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = m.projectPath
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.Contains(string(output), worktreePath)
}

// GetWorktreeForAgent returns worktree information for a specific agent
func (m *WorktreeManager) GetWorktreeForAgent(agentName string) (*WorktreeInfo, error) {
	worktreePath := filepath.Join(m.projectPath, ".ntm", "worktrees", agentName)
	branchName := fmt.Sprintf("ntm/%s/%s", m.session, agentName)

	info := &WorktreeInfo{
		AgentName:  agentName,
		Path:       worktreePath,
		BranchName: branchName,
		SessionID:  m.session,
	}

	// Check if worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		info.Created = false
		info.Error = "worktree does not exist"
		return info, nil
	}

	info.Created = true
	if !m.isValidWorktree(worktreePath) {
		info.Error = "invalid or stale worktree"
	}

	return info, nil
}
