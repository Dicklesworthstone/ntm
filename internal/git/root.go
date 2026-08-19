package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FindProjectRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
func FindProjectRoot(startDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// CommonDir returns the absolute, symlink-resolved git common directory for
// the repository containing dir. A base checkout and every linked worktree
// created from it share one common directory (the base's .git), so the result
// serves as a physical repository identity: two checkouts belong to the same
// repository exactly when their common directories match. Submodules keep a
// distinct common directory (.git/modules/<name>) and therefore a distinct
// identity. Returns an error when dir is not inside a usable git checkout.
func CommonDir(ctx context.Context, dir string) (string, error) {
	if ctx == nil {
		return "", errors.New("git common-dir context is required")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := commonDirGitOutput(commandCtx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if ctxErr := commandCtx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		// Older git (<2.31) lacks --path-format; retry with the relative form.
		output, err = commonDirGitOutput(commandCtx, dir, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
	}
	commonDir := strings.TrimSpace(output)
	if commonDir == "" {
		return "", fmt.Errorf("git common directory for %s resolved empty", dir)
	}
	if !filepath.IsAbs(commonDir) {
		// The relative form is relative to the command's working directory.
		commonDir = filepath.Join(dir, commonDir)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(commonDir); resolveErr == nil {
		commonDir = resolved
	}
	return filepath.Clean(commonDir), nil
}

// commonDirGitOutput runs a git command in dir and returns its trimmed stdout.
func commonDirGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
