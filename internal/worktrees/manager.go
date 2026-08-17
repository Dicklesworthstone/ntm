// Package worktrees provides Git worktree isolation for multi-agent sessions.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/git"
)

// gitTimeout is the maximum duration for any git command in the worktrees package.
const gitTimeout = 30 * time.Second

// gitCombinedOutput runs a git command with a timeout derived from the caller.
func gitCombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("git operation requires a context")
	}
	commandCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if commandErr := commandCtx.Err(); commandErr != nil {
		return output, errors.Join(err, commandErr)
	}
	return output, err
}

// gitRun runs a Git command with a timeout derived from the caller.
func gitRun(ctx context.Context, dir string, args ...string) error {
	if ctx == nil {
		return errors.New("git operation requires a context")
	}
	commandCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = dir
	err := cmd.Run()
	if commandErr := commandCtx.Err(); commandErr != nil {
		return errors.Join(err, commandErr)
	}
	return err
}

// gitOutput runs a git command with a timeout derived from the caller.
func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("git operation requires a context")
	}
	commandCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if commandErr := commandCtx.Err(); commandErr != nil {
		return output, errors.Join(err, commandErr)
	}
	return output, err
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

// SessionHasWorktrees reports whether worktree isolation was previously
// provisioned for the session: its worktree root directory exists on disk.
// The root persists across `ntm kill`, so it doubles as the record of the
// session's last isolation mode when a respawn flips --worktrees (ntm-83dz).
func SessionHasWorktrees(projectPath, session string) bool {
	if projectPath == "" || session == "" {
		return false
	}
	info, err := os.Stat(NewManager(projectPath, session).sessionRoot())
	return err == nil && info.IsDir()
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

// PreflightForAgent validates every deterministic failure that can be checked
// before provisioning a worktree. CreateForAgent rechecks path state and Git
// enforces branch conditions at the mutation boundary, so races fail closed.
func (m *WorktreeManager) PreflightForAgent(ctx context.Context, agentName string) error {
	if ctx == nil {
		return errors.New("worktree preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worktree preflight canceled: %w", err)
	}
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return err
	}

	if output, err := gitCombinedOutput(ctx, m.projectPath, "check-ref-format", "refs/heads/"+info.BranchName); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("invalid worktree branch %q: %s: %w", info.BranchName, detail, err)
	}

	stat, statErr := os.Stat(info.Path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
	}
	switch {
	case statErr == nil:
		if !stat.IsDir() {
			return fmt.Errorf("worktree path exists but is not a directory: %s", info.Path)
		}
		valid, validErr := m.isValidWorktree(ctx, info.Path)
		if validErr != nil {
			return fmt.Errorf("inspect existing worktree registration at %s: %w", info.Path, validErr)
		}
		if !valid {
			return fmt.Errorf("worktree path exists but is not a valid git worktree: %s", info.Path)
		}
		currentBranch, branchErr := worktreeCurrentBranch(ctx, info.Path)
		if branchErr != nil {
			return fmt.Errorf("inspect existing worktree branch at %s: %w", info.Path, branchErr)
		}
		if currentBranch == "" {
			return fmt.Errorf("worktree path %s has detached HEAD; expected branch %q", info.Path, info.BranchName)
		}
		if currentBranch != info.BranchName {
			return fmt.Errorf(
				"worktree path %s already exists on branch %q; would collide with the new branch %q (likely cross-contamination; pass --worktree-name to disambiguate)",
				info.Path, currentBranch, info.BranchName,
			)
		}
		return nil
	case !errors.Is(statErr, os.ErrNotExist):
		return fmt.Errorf("stat worktree path %s: %w", info.Path, statErr)
	}

	ref := "refs/heads/" + info.BranchName
	output, branchErr := gitCombinedOutput(ctx, m.projectPath, "show-ref", "--verify", "--quiet", ref)
	if branchErr == nil {
		return fmt.Errorf("worktree branch %q already exists without its expected worktree path %s", info.BranchName, info.Path)
	}
	var exitErr *exec.ExitError
	if !errors.As(branchErr, &exitErr) || exitErr.ExitCode() != 1 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = branchErr.Error()
		}
		return fmt.Errorf("inspect worktree branch %q: %s: %w", info.BranchName, detail, branchErr)
	}

	return nil
}

// CreateForAgent creates a new worktree for the specified agent
func (m *WorktreeManager) CreateForAgent(ctx context.Context, agentName string) (*WorktreeInfo, error) {
	if ctx == nil {
		err := errors.New("worktree creation requires a context")
		return &WorktreeInfo{AgentName: strings.TrimSpace(agentName), SessionID: strings.TrimSpace(m.session), Error: err.Error()}, err
	}
	if err := ctx.Err(); err != nil {
		wrapped := fmt.Errorf("worktree creation canceled: %w", err)
		return &WorktreeInfo{AgentName: strings.TrimSpace(agentName), SessionID: strings.TrimSpace(m.session), Error: wrapped.Error()}, wrapped
	}
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
	if err := ctx.Err(); err != nil {
		info.Error = fmt.Sprintf("worktree creation canceled: %v", err)
		return info, fmt.Errorf("worktree creation canceled: %w", err)
	}

	// Check if worktree already exists
	if stat, err := os.Stat(worktreePath); err == nil {
		if !stat.IsDir() {
			info.Error = "worktree path exists but is not a directory"
			return info, fmt.Errorf("worktree path exists but is not a directory: %s", worktreePath)
		}
		valid, validErr := m.isValidWorktree(ctx, worktreePath)
		if validErr != nil {
			info.Error = fmt.Sprintf("failed to inspect existing worktree registration: %v", validErr)
			return info, fmt.Errorf("inspect existing worktree registration at %s: %w", worktreePath, validErr)
		}
		if !valid {
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
		currentBranch, branchErr := worktreeCurrentBranch(ctx, worktreePath)
		if branchErr != nil {
			info.Error = fmt.Sprintf("failed to inspect existing worktree branch: %v", branchErr)
			return info, fmt.Errorf("inspect existing worktree branch at %s: %w", worktreePath, branchErr)
		}
		if currentBranch == "" {
			info.Error = fmt.Sprintf("worktree path %s has detached HEAD; expected branch %q", worktreePath, info.BranchName)
			return info, errors.New(info.Error)
		}
		if currentBranch != info.BranchName {
			info.Error = fmt.Sprintf(
				"worktree path %s already exists on branch %q; would collide with the new branch %q (likely cross-contamination — see ntm#145; pass --worktree-name to disambiguate)",
				worktreePath, currentBranch, info.BranchName,
			)
			return info, errors.New(info.Error)
		}
		info.Created = false
		return info, nil
	} else if !os.IsNotExist(err) {
		info.Error = fmt.Sprintf("failed to stat worktree path: %v", err)
		return info, err
	}

	// Create the worktree with new branch
	if err := ctx.Err(); err != nil {
		info.Error = fmt.Sprintf("worktree creation canceled: %v", err)
		return info, fmt.Errorf("worktree creation canceled: %w", err)
	}
	output, err := gitCombinedOutput(ctx, m.projectPath, "worktree", "add", "-b", info.BranchName, worktreePath)
	if err != nil {
		// A wrapper can run the real `git worktree add` successfully and then
		// block until cancellation. Preserve the retained checkout in the
		// failure result instead of reporting an unmutated filesystem.
		info.Created = worktreePathProvisioned(worktreePath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			info.Error = fmt.Sprintf("worktree creation canceled: %v", ctxErr)
			return info, fmt.Errorf("worktree creation canceled: %w", ctxErr)
		}
		info.Error = fmt.Sprintf("git worktree add failed: %v: %s", err, string(output))
		// Surface git's own diagnostics: a bare "exit status 128" is
		// undebuggable for the user (#222).
		if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
			return info, fmt.Errorf("failed to create worktree: %w: %s", err, trimmed)
		}
		return info, fmt.Errorf("failed to create worktree: %w", err)
	}

	info.Created = true
	currentBranch, branchErr := worktreeCurrentBranch(ctx, worktreePath)
	if branchErr != nil {
		info.Error = fmt.Sprintf("failed to inspect created worktree branch: %v", branchErr)
		return info, fmt.Errorf("inspect created worktree branch at %s: %w", worktreePath, branchErr)
	}
	if currentBranch == "" {
		info.Error = fmt.Sprintf("created worktree path %s has detached HEAD; expected branch %q", worktreePath, info.BranchName)
		return info, errors.New(info.Error)
	}
	if currentBranch != info.BranchName {
		info.Error = fmt.Sprintf("created worktree path %s is on branch %q; expected %q", worktreePath, currentBranch, info.BranchName)
		return info, errors.New(info.Error)
	}
	return info, nil
}

func worktreePathProvisioned(worktreePath string) bool {
	info, err := os.Stat(worktreePath)
	if err != nil || !info.IsDir() {
		return false
	}
	gitInfo, err := os.Stat(filepath.Join(worktreePath, ".git"))
	return err == nil && !gitInfo.IsDir()
}

// ListWorktrees returns information about all worktrees for the current session.
func (m *WorktreeManager) ListWorktrees(ctx context.Context) ([]*WorktreeInfo, error) {
	if ctx == nil {
		return nil, errors.New("list worktrees requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		valid, validationErr := m.isValidWorktree(ctx, worktreePath)
		if validationErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("inspect worktree registration at %s: %w", worktreePath, validationErr)
		}
		if !valid {
			info.Error = "invalid or stale worktree"
		}

		worktrees = append(worktrees, info)
	}

	return worktrees, nil
}

// MergeBack merges an agent's worktree changes back to the main branch.
func (m *WorktreeManager) MergeBack(ctx context.Context, agentName string) error {
	if ctx == nil {
		return errors.New("merge worktree requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return err
	}
	branchName := info.BranchName

	// Switch to the repository's actual default branch in the primary
	// worktree — never a hardcoded 'main'.
	defaultBranch, err := git.DefaultBranch(ctx, m.projectPath)
	if err != nil {
		return fmt.Errorf("failed to determine default branch: %w", err)
	}
	if err := gitRun(ctx, m.projectPath, "checkout", defaultBranch); err != nil {
		return fmt.Errorf("failed to checkout default branch %s: %w", defaultBranch, err)
	}

	// Merge the agent's branch
	output, err := gitCombinedOutput(
		ctx,
		m.projectPath,
		"merge",
		branchName,
		"--no-ff",
		"-m",
		fmt.Sprintf("Merge agent %s work from session %s", agentName, m.session),
	)
	if err != nil {
		return fmt.Errorf("failed to merge branch %s: %w: %s", branchName, err, string(output))
	}

	return nil
}

// RemoveWorktree removes a specific agent's worktree.
func (m *WorktreeManager) RemoveWorktree(ctx context.Context, agentName string) error {
	if ctx == nil {
		return errors.New("remove worktree requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := m.buildWorktreeInfo(agentName)
	if err != nil {
		return err
	}
	worktreePath := info.Path
	branchName := info.BranchName

	// Remove the worktree
	output, err := gitCombinedOutput(ctx, m.projectPath, "worktree", "remove", worktreePath, "--force")
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("remove worktree %s canceled: %w", agentName, ctx.Err())
		}
		if _, statErr := os.Lstat(worktreePath); statErr == nil {
			// Do not treat every failed git invocation as proof that this path is a
			// stale worktree. For example, an invalid repository or a missing git
			// binary also lands here. Removing an unregistered directory in those
			// cases would recursively delete data that merely happened to use the
			// expected agent path.
			valid, validErr := m.isValidWorktree(ctx, worktreePath)
			if validErr != nil {
				return fmt.Errorf("inspect worktree %s after git removal failed: %w", agentName, validErr)
			}
			if !valid {
				return fmt.Errorf("refusing filesystem cleanup for unregistered worktree %s after git removal failed: %w", agentName, err)
			}

			// If removal failed, try to prune and remove manually.
			_ = gitRun(ctx, m.projectPath, "worktree", "prune") // Ignore non-cancellation errors for prune
			if ctx.Err() != nil {
				return fmt.Errorf("remove worktree %s canceled before filesystem cleanup: %w", agentName, ctx.Err())
			}

			if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
				return fmt.Errorf("failed to remove worktree %s: %v: %s", agentName, err, string(output))
			}
			// The first prune runs before the manual removal, so it cannot remove
			// this worktree's registration. Prune again after successful fallback
			// cleanup to avoid leaving a stale worktree entry behind.
			if pruneErr := gitRun(ctx, m.projectPath, "worktree", "prune"); pruneErr != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("remove worktree %s canceled after filesystem cleanup: %w", agentName, ctx.Err())
				}
				return fmt.Errorf("prune worktree registration after removing %s: %w", agentName, pruneErr)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect worktree path %s after git removal failed: %w", worktreePath, statErr)
		}
	}

	// Delete the branch. A missing branch is harmless because removal is
	// intentionally idempotent, but any other deletion failure leaves a branch
	// that will block a future worktree creation for this agent.
	if branchErr := gitRun(ctx, m.projectPath, "branch", "-D", branchName); branchErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("remove worktree %s canceled before branch cleanup: %w", agentName, ctx.Err())
		}

		refErr := gitRun(ctx, m.projectPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
		if refErr == nil {
			return fmt.Errorf("delete worktree branch %s after removing %s: %w", branchName, agentName, branchErr)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("remove worktree %s canceled while checking branch cleanup: %w", agentName, ctx.Err())
		}
		var exitErr *exec.ExitError
		if !errors.As(refErr, &exitErr) || exitErr.ExitCode() != 1 {
			return fmt.Errorf("verify worktree branch %s after failed deletion: %w", branchName, refErr)
		}
	}

	return nil
}

// Cleanup removes all worktrees for the current session.
func (m *WorktreeManager) Cleanup(ctx context.Context) error {
	worktrees, err := m.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("failed to list worktrees for cleanup: %w", err)
	}
	sessionName, err := validateWorktreeComponent("session", m.session)
	if err != nil {
		return err
	}

	var errors []string
	for _, wt := range worktrees {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("cleanup canceled before removing worktree %s: %w", wt.AgentName, err)
		}
		if err := m.RemoveWorktree(ctx, wt.AgentName); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("cleanup canceled while removing worktree %s: %w", wt.AgentName, ctx.Err())
			}
			errors = append(errors, fmt.Sprintf("failed to remove worktree %s: %v", wt.AgentName, err))
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cleanup canceled before directory cleanup: %w", err)
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
func (m *WorktreeManager) isValidWorktree(ctx context.Context, worktreePath string) (bool, error) {
	if ctx == nil {
		return false, errors.New("worktree validation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Check if .git file exists (worktrees have a .git file pointing to the main repo)
	gitPath := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect worktree git file %s: %w", gitPath, err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// Check if it's recognized by git worktree list
	output, err := gitOutput(ctx, m.projectPath, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}

	return worktreeListed(output, worktreePath), nil
}

// worktreeCurrentBranch reads the checked-out branch name for the worktree
// at the given path. Returns "" with no error only for detached HEAD.
func worktreeCurrentBranch(ctx context.Context, worktreePath string) (string, error) {
	output, err := gitOutput(ctx, worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(strings.TrimSpace(string(output))) == 0 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func worktreeListed(output []byte, worktreePath string) bool {
	// macOS resolves /var/folders/... to /private/var/folders/... when
	// git emits worktree paths; the caller usually passes the
	// pre-resolved form. Normalise both sides via EvalSymlinks so the
	// comparison survives the substitution.
	target := canonicalisePath(worktreePath)
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		listed := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if listed == "" {
			continue
		}
		if canonicalisePath(listed) == target {
			return true
		}
	}
	return false
}

// canonicalisePath returns filepath.Clean(p) with all existing symlinks
// resolved. If the path or an ancestor doesn't exist, it canonicalises
// the deepest ancestor that does and re-attaches the missing tail —
// this keeps comparisons stable across the macOS /var → /private/var
// substitution even for not-yet-created worktree paths.
func canonicalisePath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	dir := clean
	missing := ""
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if missing == "" {
				return filepath.Clean(resolved)
			}
			return filepath.Clean(filepath.Join(resolved, missing))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return clean
		}
		base := filepath.Base(dir)
		if missing == "" {
			missing = base
		} else {
			missing = filepath.Join(base, missing)
		}
		dir = parent
	}
}

// GetWorktreeForAgent returns worktree information for a specific agent
func (m *WorktreeManager) GetWorktreeForAgent(ctx context.Context, agentName string) (*WorktreeInfo, error) {
	if ctx == nil {
		err := errors.New("worktree lookup requires a context")
		return &WorktreeInfo{AgentName: strings.TrimSpace(agentName), SessionID: strings.TrimSpace(m.session), Error: err.Error()}, err
	}
	if err := ctx.Err(); err != nil {
		wrapped := fmt.Errorf("worktree lookup canceled: %w", err)
		return &WorktreeInfo{AgentName: strings.TrimSpace(agentName), SessionID: strings.TrimSpace(m.session), Error: wrapped.Error()}, wrapped
	}
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
	if ctxErr := ctx.Err(); ctxErr != nil {
		info.Error = fmt.Sprintf("worktree lookup canceled: %v", ctxErr)
		return info, fmt.Errorf("worktree lookup canceled: %w", ctxErr)
	}
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
	valid, validErr := m.isValidWorktree(ctx, worktreePath)
	if validErr != nil {
		info.Error = fmt.Sprintf("failed to inspect worktree registration: %v", validErr)
		return info, fmt.Errorf("inspect worktree registration at %s: %w", worktreePath, validErr)
	}
	if !valid {
		info.Error = "invalid or stale worktree"
		return info, nil
	}
	currentBranch, branchErr := worktreeCurrentBranch(ctx, worktreePath)
	if branchErr != nil {
		info.Error = fmt.Sprintf("failed to inspect worktree branch: %v", branchErr)
		return info, fmt.Errorf("inspect worktree branch at %s: %w", worktreePath, branchErr)
	}
	if currentBranch == "" {
		info.Error = fmt.Sprintf("worktree has detached HEAD; expected branch %q", info.BranchName)
		return info, nil
	}
	if currentBranch != info.BranchName {
		info.Error = fmt.Sprintf("worktree is on branch %q; expected %q", currentBranch, info.BranchName)
	}

	return info, nil
}
