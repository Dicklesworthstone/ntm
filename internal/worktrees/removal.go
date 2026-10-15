package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RemoveWorktree removes only the verified checkout owned by this agent. The
// merge fence covers inspection, Git removal, and branch cleanup; a prepared
// or unreadable merge receipt prevents destruction of recovery evidence.
// Callers still own confirmation of dirty or unmerged work before requesting
// this destructive operation. Independent Git writers must be stopped first.
func (m *WorktreeManager) RemoveWorktree(ctx context.Context, agentName string) error {
	if ctx == nil {
		return errors.New("remove worktree requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := m.removalScope()
	if err != nil {
		return err
	}
	if _, err := m.buildWorktreeInfo(agentName); err != nil {
		return err
	}
	fence, err := acquireMergeFence(ctx, m.projectPath)
	if err != nil {
		return fmt.Errorf("acquire worktree removal ownership: %w", err)
	}
	defer fence.close()
	return m.removeWorktree(ctx, agentName, fence)
}

func (m *WorktreeManager) removalScope() (*WorktreeManager, error) {
	if m == nil || strings.TrimSpace(m.projectPath) == "" {
		return nil, errors.New("worktree removal requires a project directory")
	}
	project, err := filepath.Abs(m.projectPath)
	if err != nil {
		return nil, err
	}
	return NewManager(project, m.session), nil
}

// inspectRemoval refuses changed checkout ownership rather than allowing a
// stale agent name to authorize deletion of another branch or repository.
// A missing checkout is a no-op only when both ref and registration are gone.
func (m *WorktreeManager) inspectRemoval(ctx context.Context, info *WorktreeInfo, fence *mergeFence) (bool, string, error) {
	if err := verifyMergeFenceFile(fence.file, fence.file.Name()); err != nil {
		return false, "", err
	}
	common, err := mergeCommonDirectory(ctx, m.projectPath)
	if err != nil || common != fence.common {
		return false, "", errors.Join(err, errors.New("worktree removal repository changed after acquiring ownership"))
	}
	if err := m.verifyWorktreeDirectories(info); err != nil {
		return false, "", err
	}
	_, statErr := os.Lstat(info.Path)
	if errors.Is(statErr, os.ErrNotExist) {
		registered, err := m.removalRegistered(ctx, info.Path)
		if err != nil {
			return false, "", err
		}
		refErr := gitRun(ctx, m.projectPath, "show-ref", "--verify", "--quiet", "refs/heads/"+info.BranchName)
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		var exitErr *exec.ExitError
		if !registered && errors.As(refErr, &exitErr) && exitErr.ExitCode() == 1 {
			return false, "", nil
		}
		return false, "", fmt.Errorf("worktree %s is missing; retained branch or registration requires inspection: %w", info.AgentName,
			errors.Join(refErr, errors.New("missing checkout does not authorize branch cleanup")))
	}
	if statErr != nil {
		return false, "", statErr
	}
	tip, err := m.inspectWorktreeIdentity(ctx, info, fence.common)
	return true, tip, err
}

// inspectWorktreeIdentity is the shared existing-checkout check for preflight,
// provisioning and removal. A registered path and matching branch name do not
// prove ownership if its .git file has been redirected to another repository.
func (m *WorktreeManager) inspectWorktreeIdentity(ctx context.Context, info *WorktreeInfo, expectedCommon string) (string, error) {
	gitFile, err := os.Lstat(filepath.Join(info.Path, ".git"))
	if err != nil || !gitFile.Mode().IsRegular() {
		return "", errors.Join(err, errors.New("refusing filesystem cleanup for unregistered worktree: expected a regular .git file"))
	}
	registered, err := m.removalRegistered(ctx, info.Path)
	if err != nil {
		return "", err
	}
	if !registered {
		return "", errors.New("refusing filesystem cleanup for unregistered worktree")
	}
	common, err := mergeCommonDirectory(ctx, info.Path)
	if err != nil || common != expectedCommon {
		return "", errors.Join(err, errors.New("worktree checkout belongs to a different repository"))
	}
	branch, err := worktreeCurrentBranch(ctx, info.Path)
	if err != nil {
		return "", err
	}
	if branch != info.BranchName {
		return "", fmt.Errorf("worktree is on branch %q; expected %q", branch, info.BranchName)
	}
	return mergeCommitAt(ctx, m.projectPath, "refs/heads/"+info.BranchName)
}

// verifyWorktreeDirectories is shared by provisioning and removal. An alias
// to the project itself (e.g. macOS /var) is allowed; substitutions below that
// boundary must not redirect either a checkout creation or a deletion.
func (m *WorktreeManager) verifyWorktreeDirectories(info *WorktreeInfo) error {
	for _, path := range []string{filepath.Join(m.projectPath, ".ntm"), m.worktreesRoot(), filepath.Dir(info.Path), info.Path} {
		entry, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing worktree operation through non-directory or symlink %s", path)
		}
	}
	return nil
}

// Use Git's NUL-delimited format: quoted or newline-bearing paths must not
// disappear from registration checks. Empty/malformed output is not absence.
func (m *WorktreeManager) removalRegistered(ctx context.Context, path string) (bool, error) {
	out, err := gitOutput(ctx, m.projectPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	if len(out) == 0 || !strings.HasSuffix(string(out), "\x00\x00") {
		return false, errors.New("invalid worktree registration listing")
	}
	target := canonicalisePath(path)
	found := false
	for _, record := range strings.Split(strings.TrimSuffix(string(out), "\x00\x00"), "\x00\x00") {
		first, _, _ := strings.Cut(record, "\x00")
		listed, ok := strings.CutPrefix(first, "worktree ")
		if !ok || !filepath.IsAbs(listed) {
			return false, errors.New("invalid worktree registration path")
		}
		if canonicalisePath(listed) == target {
			if found {
				return false, errors.New("duplicate worktree registration path")
			}
			found = true
		}
	}
	return found, nil
}
