// Package hooks provides git hook integration for NTM.
// It enables installation and management of git hooks that run quality checks
// like UBS scans before commits.
package hooks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Common errors returned by the hooks package.
var (
	ErrNotGitRepo       = errors.New("not a git repository")
	ErrHookExists       = errors.New("hook already exists (use --force to overwrite)")
	ErrHookNotInstalled = errors.New("hook not installed")
	ErrNTMNotFound      = errors.New("ntm binary not found in PATH")
)

// HookType represents the type of git hook.
type HookType string

const (
	HookPreCommit  HookType = "pre-commit"
	HookPrePush    HookType = "pre-push"
	HookCommitMsg  HookType = "commit-msg"
	HookPostCommit HookType = "post-commit"
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

	return &Manager{
		repoRoot: root,
		hooksDir: filepath.Join(root, ".git", "hooks"),
	}, nil
}

// Install installs the specified hook type.
func (m *Manager) Install(hookType HookType, force bool) error {
	hookPath := filepath.Join(m.hooksDir, string(hookType))
	backupPath := hookPath + ".backup"

	// Check if hook already exists
	if _, err := os.Stat(hookPath); err == nil {
		content, _ := os.ReadFile(hookPath)
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
	if err := os.WriteFile(hookPath, []byte(script), 0755); err != nil {
		return fmt.Errorf("writing hook: %w", err)
	}

	return nil
}

// Uninstall removes the specified hook type.
func (m *Manager) Uninstall(hookType HookType, restore bool) error {
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
	hooks := []HookType{HookPreCommit, HookPrePush, HookCommitMsg, HookPostCommit}
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
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", ErrNotGitRepo
	}
	return strings.TrimSpace(string(output)), nil
}

// isNTMHook checks if a hook script is managed by NTM.
func isNTMHook(content string) bool {
	return strings.Contains(content, "NTM_MANAGED_HOOK")
}

// generateHookScript generates the hook script content.
func generateHookScript(hookType HookType, repoRoot string) (string, error) {
	// Ensure ntm is available
	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		return "", ErrNTMNotFound
	}

	switch hookType {
	case HookPreCommit:
		return generatePreCommitScript(ntmPath, repoRoot), nil
	default:
		return "", fmt.Errorf("hook type %s not yet implemented", hookType)
	}
}

// generatePreCommitScript generates the pre-commit hook script.
func generatePreCommitScript(ntmPath, repoRoot string) string {
	return fmt.Sprintf(`#!/bin/bash
# NTM_MANAGED_HOOK - Do not edit manually
# Installed by: ntm hooks install pre-commit
# Repository: %s

set -e

# Run UBS scan on staged files
"%s" hooks run pre-commit "$@"
UBS_EXIT=$?

# Chain to backup hook if it exists
BACKUP_HOOK="$(dirname "$0")/pre-commit.backup"
if [ -x "$BACKUP_HOOK" ]; then
    "$BACKUP_HOOK" "$@"
    BACKUP_EXIT=$?
    # If either failed, fail the hook
    if [ $UBS_EXIT -ne 0 ] || [ $BACKUP_EXIT -ne 0 ]; then
        exit 1
    fi
elif [ $UBS_EXIT -ne 0 ]; then
    exit $UBS_EXIT
fi

exit 0
`, repoRoot, ntmPath)
}
