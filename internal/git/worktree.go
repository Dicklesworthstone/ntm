// Package git provides git worktree isolation functionality for multi-agent coordination.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	worktreeGitCommandTimeout   = 5 * time.Second
	worktreeGitCommandWaitDelay = 500 * time.Millisecond
)

func isAlreadySafeWorktreeKey(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	// Git ref components cannot contain ".." or end in ".lock".
	// If either appears, force canonicalization instead of preserving as-is.
	if strings.Contains(value, "..") || strings.HasSuffix(value, ".lock") {
		return false
	}
	// Even if characters are otherwise safe, leading/trailing dots are
	// invalid in git ref components. Keep those on the canonicalization path.
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			// allowed as-is
		default:
			return false
		}
	}
	return true
}

func normalizeRefComponentPatterns(value string) string {
	for strings.Contains(value, "..") {
		value = strings.ReplaceAll(value, "..", "-")
	}
	if strings.HasSuffix(value, ".lock") {
		value = strings.TrimSuffix(value, ".lock") + "-lock"
	}
	return value
}

func canonicalWorktreeKey(value, fallback string) string {
	if value == "" {
		return fallback
	}

	var b strings.Builder
	b.Grow(len(value))
	lastDash := false

	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			if r == '-' {
				if lastDash {
					continue
				}
				lastDash = true
			} else {
				lastDash = false
			}
			b.WriteRune(r)
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	key := strings.Trim(b.String(), "-.")
	key = normalizeRefComponentPatterns(key)
	key = strings.Trim(key, "-.")
	if key == "" {
		return fallback
	}
	return key
}

// canonicalSessionKey converts a session identity into a git-safe key
// without truncating uniqueness-bearing suffixes like agent type/number.
// The previous 8-char truncation caused alias collisions across distinct
// sessions/panes (bd-l542u).
func canonicalSessionKey(sessionID string) string {
	// Session IDs are typically already restricted to [A-Za-z0-9_-].
	// Preserve that safe form exactly so distinct safe IDs don't alias
	// through dash-collapsing canonicalization (bd-iz9ss).
	if isAlreadySafeWorktreeKey(sessionID) {
		return sessionID
	}
	// Keep the no-aliasing behavior for otherwise-safe IDs that only violate
	// git-ref component rules at the edges (for example ".foo."), by trimming
	// edge dots without re-running dash-collapse logic.
	trimmedDots := strings.Trim(sessionID, ".")
	if trimmedDots != sessionID && isAlreadySafeWorktreeKey(trimmedDots) {
		return trimmedDots
	}
	return canonicalWorktreeKey(sessionID, "session")
}

func canonicalAgentKey(agentName string) string {
	// Mirror canonicalSessionKey behavior so already-safe identities keep
	// their exact uniqueness-bearing form (for example repeated dashes).
	// Collapsing those separators aliases distinct agents like
	// "alpha--team" and "alpha-team" into one worktree key (bd-awrt5).
	if isAlreadySafeWorktreeKey(agentName) {
		return agentName
	}
	trimmedDots := strings.Trim(agentName, ".")
	if trimmedDots != agentName && isAlreadySafeWorktreeKey(trimmedDots) {
		return trimmedDots
	}
	return canonicalWorktreeKey(agentName, "agent")
}

func worktreeNameForKeys(agentKey, sessionKey string) string {
	// Length-prefix both components so pairs like ("a-b", "c") and
	// ("a", "b-c") cannot resolve to the same worktree basename.
	return fmt.Sprintf("agent-%d-%s-session-%d-%s", len(agentKey), agentKey, len(sessionKey), sessionKey)
}

func parseWorktreeNameAgentKey(name string) string {
	if agentKey, ok := parseLengthPrefixedWorktreeName(name); ok {
		return agentKey
	}
	return parseLegacyAgentKeyFromWorktreeName(name)
}

func parseLengthPrefixedWorktreeName(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "agent-")
	if !ok {
		return "", false
	}
	lenText, rest, ok := strings.Cut(rest, "-")
	if !ok {
		return "", false
	}
	agentLen, err := strconv.Atoi(lenText)
	if err != nil || agentLen <= 0 || len(rest) < agentLen {
		return "", false
	}
	agentKey := rest[:agentLen]
	rest = rest[agentLen:]
	rest, ok = strings.CutPrefix(rest, "-session-")
	if !ok {
		return "", false
	}
	lenText, rest, ok = strings.Cut(rest, "-")
	if !ok {
		return "", false
	}
	sessionLen, err := strconv.Atoi(lenText)
	if err != nil || sessionLen <= 0 || len(rest) != sessionLen {
		return "", false
	}
	return agentKey, true
}

func parseBranchAgentKey(branch string) string {
	if !strings.HasPrefix(branch, "agent/") {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(branch, "agent/"), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return ""
	}
	return parts[0]
}

func parseLegacyAgentKeyFromWorktreeName(name string) string {
	if !strings.HasPrefix(name, "agent-") {
		return ""
	}
	parts := strings.Split(name, "-")
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return ""
	}
	return parts[1]
}

func pathsReferToSameLocation(pathA, pathB string) bool {
	if len(pathA) == 0 || len(pathB) == 0 {
		return false
	}
	if filepath.Clean(pathA) == filepath.Clean(pathB) {
		return true
	}

	statA, errA := os.Stat(pathA)
	statB, errB := os.Stat(pathB)
	if errA == nil && errB == nil {
		return os.SameFile(statA, statB)
	}
	return false
}

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
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Commit    string    `json:"commit"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
}

// ProvisionWorktree creates an isolated worktree for an agent
func (wm *WorktreeManager) ProvisionWorktree(ctx context.Context, agentName, sessionID string) (*WorktreeInfo, error) {
	agentKey := canonicalAgentKey(agentName)
	sessionKey := canonicalSessionKey(sessionID)

	// Generate a unique worktree name
	worktreeName := worktreeNameForKeys(agentKey, sessionKey)
	workingDir := filepath.Join(wm.baseRepo, "..", worktreeName)

	// Check if worktree already exists
	if exists, err := wm.worktreeExists(worktreeName); err != nil {
		return nil, fmt.Errorf("failed to check worktree existence: %w", err)
	} else if exists {
		// Return existing worktree info
		return wm.getWorktreeInfo(worktreeName)
	}

	// Create a new branch for this agent
	branchName := fmt.Sprintf("agent/%s/%s", agentKey, sessionKey)

	// Get current branch and commit for base
	currentBranch, err := wm.getCurrentBranch()
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}

	// Create the worktree with a new branch
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, workingDir, currentBranch)
	cmd.WaitDelay = 2 * time.Second
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
		Agent:     agentKey,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}

	return worktreeInfo, nil
}

// ListWorktrees returns all worktrees associated with agents
func (wm *WorktreeManager) ListWorktrees(ctx context.Context) ([]*WorktreeInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = wm.baseRepo
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return wm.parseWorktreeList(string(output))
}

// RemoveWorktree removes a worktree and its associated branch
func (wm *WorktreeManager) RemoveWorktree(ctx context.Context, agentName, sessionID string) error {
	agentKey := canonicalAgentKey(agentName)
	sessionKey := canonicalSessionKey(sessionID)
	worktreeName := worktreeNameForKeys(agentKey, sessionKey)
	workingDir := filepath.Join(wm.baseRepo, "..", worktreeName)
	branchName := fmt.Sprintf("agent/%s/%s", agentKey, sessionKey)

	return wm.removeWorktreePathAndBranch(ctx, workingDir, branchName)
}

func (wm *WorktreeManager) removeWorktreePathAndBranch(ctx context.Context, workingDir, branchName string) error {
	// Remove the worktree
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", workingDir)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = wm.baseRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		// If worktree doesn't exist, that's OK
		if !strings.Contains(string(output), "not a working tree") {
			return fmt.Errorf("failed to remove worktree: %w\nOutput: %s", err, string(output))
		}
	}

	// Remove the branch
	if branchName != "" {
		cmd = exec.CommandContext(ctx, "git", "branch", "-D", branchName)
		cmd.WaitDelay = 2 * time.Second
		cmd.Dir = wm.baseRepo
		if output, err := cmd.CombinedOutput(); err != nil {
			// If branch doesn't exist, that's OK
			if !strings.Contains(string(output), "not found") {
				return fmt.Errorf("failed to remove branch: %w\nOutput: %s", err, string(output))
			}
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
				if err := wm.removeWorktreePathAndBranch(ctx, wt.Path, wt.Branch); err != nil {
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
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch: %w\nOutput: %s", err, string(output))
	}

	// Get the base branch (what this agent branch was created from)
	// For now, assume 'main' - this could be enhanced to track the actual base
	cmd = exec.CommandContext(ctx, "git", "merge", "origin/main")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to merge base branch: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Helper methods

// IsGitRepository checks if a directory is a git repository.
// Returns false for empty dir to prevent false positives from CWD.
func IsGitRepository(dir string) bool {
	if dir == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
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
	ctx, cancel := context.WithTimeout(context.Background(), worktreeGitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.WaitDelay = worktreeGitCommandWaitDelay
	cmd.Dir = wm.baseRepo
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getCommitHash returns the current commit hash for a worktree
func (wm *WorktreeManager) getCommitHash(worktreePath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), worktreeGitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.WaitDelay = worktreeGitCommandWaitDelay
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
	ctx, cancel := context.WithTimeout(context.Background(), worktreeGitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.WaitDelay = worktreeGitCommandWaitDelay
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

	// Parse agent key from the branch first. Detached or renamed
	// worktrees fall back to the generated basename, with legacy basename
	// parsing kept for older worktrees.
	agentName := parseBranchAgentKey(branch)
	if agentName == "" {
		agentName = parseWorktreeNameAgentKey(name)
	}
	if agentName == "" {
		agentName = "unknown"
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

		// Only include agent worktrees. A parent directory may contain the
		// string "agent-", so match the branch or the worktree basename
		// instead of the full path.
		if len(path) > 0 {
			// git worktree list includes the primary checkout. Even if that
			// checkout is currently on an agent/* branch, it is not an agent
			// worktree entry and must be excluded from agent listings.
			if pathsReferToSameLocation(path, wm.baseRepo) {
				continue
			}
			basename := filepath.Base(path)
			agentName := parseBranchAgentKey(branch)
			if agentName == "" && !strings.HasPrefix(basename, "agent-") {
				continue
			}
			if agentName == "" {
				agentName = parseWorktreeNameAgentKey(basename)
			}
			if agentName == "" {
				agentName = "unknown"
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
