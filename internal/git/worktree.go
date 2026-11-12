// Package git provides repository root, default-branch, and Git command helpers.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

var (
	worktreeGitCommandTimeout   = 5 * time.Second
	worktreeGitCommandWaitDelay = 500 * time.Millisecond
)

func validateWorktreeContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("worktree operation requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worktree operation canceled: %w", err)
	}
	return nil
}

func newWorktreeGitCommandContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return nil, nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, worktreeGitCommandTimeout)
	return commandCtx, cancel, nil
}

func worktreeCommandError(ctx context.Context, commandErr error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return commandErr
}

// IsGitRepository checks if a directory is a git repository. It returns false
// for an empty directory instead of accidentally inspecting the process CWD.
func IsGitRepository(ctx context.Context, dir string) (bool, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return false, err
	}
	if dir == "" {
		return false, nil
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect repository directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return false, nil
	}
	if err := validateWorktreeContext(ctx); err != nil {
		return false, err
	}
	commandCtx, cancel, err := newWorktreeGitCommandContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", "rev-parse", "--git-dir")
	cmd.WaitDelay = worktreeGitCommandWaitDelay
	cmd.Dir = dir
	commandErr := cmd.Run()
	if err := worktreeCommandError(commandCtx, commandErr); err != nil {
		if commandCtx.Err() != nil {
			return false, err
		}
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
