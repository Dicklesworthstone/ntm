// Package hooks provides git hook integration for NTM.
// It enables installation and management of git hooks that run quality checks
// like UBS scans before commits.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Common errors returned by the hooks package.
var (
	ErrNotGitRepo       = errors.New("not a git repository")
	ErrHookExists       = errors.New("hook already exists (use --force to overwrite)")
	ErrHookNotInstalled = errors.New("hook not installed")
	ErrNTMNotFound      = errors.New("ntm binary not found in PATH")
	// ErrHooksDirOutsideRepo means `git rev-parse --git-path hooks` resolved
	// to a directory outside the repository — a core.hooksPath redirect (for
	// example a global `git config --global core.hooksPath`) is in effect.
	// NTM-managed hooks are repo-scoped (they bake in REPO_ROOT), so writing
	// or deleting them through a shared/global hooks directory would execute
	// wrong-repo tooling machine-wide (#225).
	ErrHooksDirOutsideRepo = errors.New("git resolves the hooks directory outside this repository (core.hooksPath redirect)")
)

// HookType represents the type of git hook.
type HookType string

const (
	HookPreCommit    HookType = "pre-commit"
	HookPrePush      HookType = "pre-push"
	HookCommitMsg    HookType = "commit-msg"
	HookPostCommit   HookType = "post-commit"
	HookPostCheckout HookType = "post-checkout"
)

// HookInfo contains information about an installed hook.
type HookInfo struct {
	Type      HookType `json:"type"`
	Path      string   `json:"path"`
	Installed bool     `json:"installed"`
	IsNTM     bool     `json:"is_ntm"`     // true if this is an NTM-managed hook
	HasBackup bool     `json:"has_backup"` // true if a backup exists
}

// Manager handles git hook installation and management.
type Manager struct {
	repoRoot string
	hooksDir string
	// externalHooksErr is non-nil when the resolved hooks directory lies
	// outside the repository (core.hooksPath redirect, #225). Read-only
	// operations still work; mutating operations return this error.
	externalHooksErr error
}

// NewManager creates a new hook manager for the given repository.
// If repoPath is empty, it uses the current working directory.
func NewManager(repoPath string) (*Manager, error) {
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Find git root
	root, err := findGitRoot(repoPath)
	if err != nil {
		return nil, err
	}
	hooksDir, err := findGitPath(root, "hooks")
	if err != nil {
		return nil, err
	}

	m := &Manager{
		repoRoot: root,
		hooksDir: hooksDir,
	}

	// `git rev-parse --git-path hooks` honors core.hooksPath from ANY config
	// scope. A global redirect silently retargets a repo-scoped hook install
	// at a shared hooks directory that every repository on the machine
	// executes (#225). Detect that here; read-only operations (status/list)
	// still work, but Install/Uninstall refuse with the redirect named.
	commonDir, commonErr := findGitCommonDir(root)
	if commonErr == nil && !pathWithin(hooksDir, root) && !pathWithin(hooksDir, commonDir) {
		m.externalHooksErr = fmt.Errorf(
			"%w: git resolves hooks to %s, outside repository %s.\n"+
				"A core.hooksPath redirect is in effect (check `git config --show-origin --get core.hooksPath`).\n"+
				"NTM refuses to write repo-scoped hooks into a shared hooks directory.\n"+
				"Fix: pin this repo's hooks dir with `git -C %s config core.hooksPath .git/hooks`,\n"+
				"or remove the global setting with `git config --global --unset core.hooksPath`",
			ErrHooksDirOutsideRepo, hooksDir, root, root,
		)
	}

	return m, nil
}

// Install installs the specified hook type.
func (m *Manager) Install(hookType HookType, force bool) error {
	if m.externalHooksErr != nil {
		return m.externalHooksErr
	}
	hookPath := filepath.Join(m.hooksDir, string(hookType))
	backupPath := hookPath + ".backup"

	// Check if hook already exists
	if _, err := os.Stat(hookPath); err == nil {
		content, readErr := os.ReadFile(hookPath)
		if readErr != nil {
			return fmt.Errorf("reading existing hook %s: %w", hookPath, readErr)
		}
		if isNTMHook(string(content)) {
			// Already an NTM hook, just overwrite
			force = true
		} else if !force {
			return ErrHookExists
		} else {
			// Backup existing hook
			if err := os.Rename(hookPath, backupPath); err != nil {
				return fmt.Errorf("backing up existing hook: %w", err)
			}
		}
	}

	// Generate hook script
	script, err := generateHookScript(hookType, m.repoRoot)
	if err != nil {
		return fmt.Errorf("generating hook script: %w", err)
	}

	// Write hook file
	if err := writeHookFile(hookPath, script); err != nil {
		return fmt.Errorf("writing hook: %w", err)
	}

	return nil
}

// Uninstall removes the specified hook type.
func (m *Manager) Uninstall(hookType HookType, restore bool) error {
	if m.externalHooksErr != nil {
		return m.externalHooksErr
	}
	hookPath := filepath.Join(m.hooksDir, string(hookType))
	backupPath := hookPath + ".backup"

	// Check if hook exists
	content, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrHookNotInstalled
		}
		return fmt.Errorf("reading hook: %w", err)
	}

	// Verify it's an NTM hook
	if !isNTMHook(string(content)) {
		return fmt.Errorf("hook exists but is not managed by ntm")
	}

	// Remove the hook
	if err := os.Remove(hookPath); err != nil {
		return fmt.Errorf("removing hook: %w", err)
	}

	// Restore backup if requested and exists
	if restore {
		if _, err := os.Stat(backupPath); err == nil {
			if err := os.Rename(backupPath, hookPath); err != nil {
				return fmt.Errorf("restoring backup: %w", err)
			}
		}
	}

	return nil
}

// Status returns information about installed hooks.
func (m *Manager) Status(hookType HookType) (*HookInfo, error) {
	hookPath := filepath.Join(m.hooksDir, string(hookType))
	backupPath := hookPath + ".backup"

	info := &HookInfo{
		Type: hookType,
		Path: hookPath,
	}

	content, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return info, nil
		}
		return nil, fmt.Errorf("reading hook: %w", err)
	}

	info.Installed = true
	info.IsNTM = isNTMHook(string(content))

	// Check for backup
	if _, err := os.Stat(backupPath); err == nil {
		info.HasBackup = true
	}

	return info, nil
}

// ListAll returns status of all supported hook types.
func (m *Manager) ListAll() ([]*HookInfo, error) {
	hooks := []HookType{HookPreCommit, HookPrePush, HookCommitMsg, HookPostCommit, HookPostCheckout}
	infos := make([]*HookInfo, 0, len(hooks))

	for _, h := range hooks {
		info, err := m.Status(h)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}

	return infos, nil
}

// RepoRoot returns the repository root path.
func (m *Manager) RepoRoot() string {
	return m.repoRoot
}

// HooksDir returns the hooks directory path.
func (m *Manager) HooksDir() string {
	return m.hooksDir
}

// findGitRoot finds the root of the git repository.
func findGitRoot(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", ErrNotGitRepo
	}
	return strings.TrimSpace(string(output)), nil
}

func findGitPath(repoRoot, gitPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--git-path", gitPath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving git path %q: %w", gitPath, err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", fmt.Errorf("resolving git path %q: empty path", gitPath)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(repoRoot, path), nil
}

// findGitCommonDir returns the repository's common .git directory (absolute).
// For linked worktrees this is the main repository's git dir, so hooks that
// legitimately live there are not misreported as external.
func findGitCommonDir(repoRoot string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving git common dir: %w", err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", errors.New("resolving git common dir: empty path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	return filepath.Clean(path), nil
}

// pathWithin reports whether child is parent or lives underneath parent.
// Symlinks are resolved when possible so equivalent physical paths (e.g.
// /var vs /private/var on macOS) compare equal.
func pathWithin(child, parent string) bool {
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	childResolved := resolve(child)
	parentResolved := resolve(parent)
	rel, err := filepath.Rel(parentResolved, childResolved)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func writeHookFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ntm-hook-*")
	if err != nil {
		return fmt.Errorf("creating temporary hook: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temporary hook: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary hook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary hook: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("installing hook: %w", err)
	}
	cleanup = false
	return nil
}

// isNTMHook checks if a hook script is managed by NTM.
func isNTMHook(content string) bool {
	return strings.Contains(content, "NTM_MANAGED_HOOK")
}

// generateHookScript generates the hook script content.
func generateHookScript(hookType HookType, repoRoot string) (string, error) {
	switch hookType {
	case HookPreCommit, HookPrePush, HookCommitMsg, HookPostCommit:
		// Ensure ntm is available for hooks that invoke `ntm hooks run` / `ntm guards check`.
		ntmPath, err := exec.LookPath("ntm")
		if err != nil {
			return "", ErrNTMNotFound
		}
		switch hookType {
		case HookPreCommit:
			return generatePreCommitScript(ntmPath, repoRoot), nil
		case HookPrePush:
			return generatePrePushScript(ntmPath, repoRoot), nil
		case HookCommitMsg:
			return generateCommitMsgScript(ntmPath, repoRoot), nil
		case HookPostCommit:
			return generatePostCommitScript(ntmPath, repoRoot), nil
		}
	case HookPostCheckout:
		return generatePostCheckoutScript(repoRoot), nil
	}
	return "", fmt.Errorf("unsupported hook type %q (supported: pre-commit, pre-push, commit-msg, post-commit, post-checkout)", hookType)
}

func sanitizeCommentValue(s string) string {
	safe := strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(safe, "\r", " ")
}

// generatePreCommitScript generates the pre-commit hook script.
func generatePreCommitScript(ntmPath, repoRoot string) string {
	safeRepoRoot := sanitizeCommentValue(repoRoot)
	repoRootQuoted := quoteShell(repoRoot)

	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install pre-commit
# Repository: %s

set -e

# Repo root for hooks that need project context
REPO_ROOT=%s

# Sync beads metadata if available
if command -v br &> /dev/null; then
    if [ -d "$REPO_ROOT/.beads" ]; then
        (cd "$REPO_ROOT" && br sync --flush-only)
    fi
else
    echo "[ntm] br not installed - skipping beads sync" >&2
fi

# Refuse commits touching files another agent holds an exclusive Agent Mail
# reservation on ('ntm guards check'; when Agent Mail is unreachable it warns,
# records a degraded run for 'ntm doctor' and allows the commit, unless
# NTM_GUARD_STRICT=1).
GUARD_EXIT=0
%[3]s guards check --staged || GUARD_EXIT=$?

# Run UBS scan on staged files
UBS_EXIT=0
%[3]s hooks run pre-commit "$@" || UBS_EXIT=$?

# Chain to backup hook if it exists
BACKUP_EXIT=0
BACKUP_HOOK="$(dirname "$0")/pre-commit.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@" || BACKUP_EXIT=$?
fi

if [ $GUARD_EXIT -ne 0 ] || [ $UBS_EXIT -ne 0 ] || [ $BACKUP_EXIT -ne 0 ]; then
    exit 1
fi

exit 0
`, safeRepoRoot, repoRootQuoted, quoteShell(ntmPath))
}

// generatePrePushScript generates the pre-push hook script.
func generatePrePushScript(ntmPath, repoRoot string) string {
	safeRepoRoot := sanitizeCommentValue(repoRoot)
	repoRootQuoted := quoteShell(repoRoot)

	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install pre-push
# Repository: %s

set -e

# Repo root for hooks that need project context
REPO_ROOT=%s

# Check active Agent Mail reservations (respects NTM_GUARD_STRICT)
GUARD_EXIT=0
%[3]s guards check --staged || GUARD_EXIT=$?

# Run NTM pre-push checks
HOOK_EXIT=0
%[3]s hooks run pre-push "$@" || HOOK_EXIT=$?

# Chain to backup hook if it exists
BACKUP_EXIT=0
BACKUP_HOOK="$(dirname "$0")/pre-push.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@" || BACKUP_EXIT=$?
fi

if [ $GUARD_EXIT -ne 0 ] || [ $HOOK_EXIT -ne 0 ] || [ $BACKUP_EXIT -ne 0 ]; then
    exit 1
fi

exit 0
`, safeRepoRoot, repoRootQuoted, quoteShell(ntmPath))
}

// generateCommitMsgScript generates the commit-msg hook script.
func generateCommitMsgScript(ntmPath, repoRoot string) string {
	safeRepoRoot := sanitizeCommentValue(repoRoot)
	repoRootQuoted := quoteShell(repoRoot)

	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install commit-msg
# Repository: %s

set -e

# Repo root for hooks that need project context
REPO_ROOT=%s

# Run NTM commit-msg validation
HOOK_EXIT=0
%[3]s hooks run commit-msg "$@" || HOOK_EXIT=$?

# Chain to backup hook if it exists
BACKUP_EXIT=0
BACKUP_HOOK="$(dirname "$0")/commit-msg.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@" || BACKUP_EXIT=$?
fi

if [ $HOOK_EXIT -ne 0 ] || [ $BACKUP_EXIT -ne 0 ]; then
    exit 1
fi

exit 0
`, safeRepoRoot, repoRootQuoted, quoteShell(ntmPath))
}

// generatePostCommitScript generates the post-commit hook script.
func generatePostCommitScript(ntmPath, repoRoot string) string {
	safeRepoRoot := sanitizeCommentValue(repoRoot)
	repoRootQuoted := quoteShell(repoRoot)

	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install post-commit
# Repository: %s

set -e

# Repo root for hooks that need project context
REPO_ROOT=%s

# Run NTM post-commit checks (non-fatal)
%[3]s hooks run post-commit "$@" || true

# Chain to backup hook if it exists
BACKUP_HOOK="$(dirname "$0")/post-commit.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@" || true
fi

exit 0
`, safeRepoRoot, repoRootQuoted, quoteShell(ntmPath))
}

// PreCommitRunsReservationGuard reports whether a pre-commit hook runs ntm's
// file-reservation check: the standalone guard `ntm guards install` writes,
// or ntm's managed pre-commit hook, which includes the check.
func PreCommitRunsReservationGuard(content string) bool {
	return strings.Contains(content, "ntm-precommit-guard") ||
		(isNTMHook(content) && strings.Contains(content, "guards check --staged"))
}

// IsManagedHook reports whether a hook script is ntm's managed hook
// (`ntm init` / `ntm hooks install`), which `Install` regenerates in place.
func IsManagedHook(content string) bool {
	return isNTMHook(content)
}

// quoteShell quotes a string for safe use in a shell script.
func quoteShell(s string) string {
	// If the string is empty, return an empty quoted string
	if s == "" {
		return "''"
	}
	// Use single quotes, and replace any single quote with '\''
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func generatePostCheckoutScript(repoRoot string) string {
	safeRepoRoot := sanitizeCommentValue(repoRoot)
	repoRootQuoted := quoteShell(repoRoot)

	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install post-checkout
# Repository: %s

set -e

# Repo root for hooks that need project context
REPO_ROOT=%s

# Warn on uncommitted beads changes
if [ -d "$REPO_ROOT/.beads" ]; then
    if git -C "$REPO_ROOT" status --porcelain .beads | grep -q .; then
        echo "[ntm] Warning: .beads has uncommitted changes. Run: br sync --flush-only and commit .beads/"
    fi
fi

# Chain to backup hook if it exists
BACKUP_HOOK="$(dirname "$0")/post-checkout.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@" || true
fi

exit 0
`, safeRepoRoot, repoRootQuoted)
}
