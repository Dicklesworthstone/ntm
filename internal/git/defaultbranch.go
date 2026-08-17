package git

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// DefaultBranch determines the default branch of the git repository at
// repoPath. It never silently guesses "main"; the resolution chain is:
//
//  1. `git symbolic-ref refs/remotes/origin/HEAD` — the remote's advertised
//     default branch (e.g. refs/remotes/origin/master → "master").
//  2. `git config init.defaultBranch` — honored only when that branch
//     actually exists locally (global config may name a branch this repo
//     never had).
//  3. Probe for a local `main`, then `master` branch.
//  4. Fall back to the branch HEAD currently points at, when there is one.
//
// If the entire chain fails (e.g. detached HEAD in a repo with neither main
// nor master and no origin/HEAD), DefaultBranch returns a loud error rather
// than defaulting to "main".
func DefaultBranch(ctx context.Context, repoPath string) (string, error) {
	if err := validateWorktreeContext(ctx); err != nil {
		return "", err
	}

	// 1. Remote default branch advertised by origin.
	if ref, err := defaultBranchGitOutput(ctx, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		if branch := strings.TrimPrefix(ref, "refs/remotes/origin/"); branch != "" && branch != ref {
			slog.Debug("default branch resolved from origin/HEAD", "repo", repoPath, "branch", branch)
			return branch, nil
		}
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	// 2. init.defaultBranch, but only if that branch exists in this repo.
	if branch, err := defaultBranchGitOutput(ctx, repoPath, "config", "--get", "init.defaultBranch"); err == nil && branch != "" {
		if localBranchExists(ctx, repoPath, branch) {
			slog.Debug("default branch resolved from init.defaultBranch", "repo", repoPath, "branch", branch)
			return branch, nil
		}
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	// 3. Probe conventional default branches.
	for _, candidate := range []string{"main", "master"} {
		if localBranchExists(ctx, repoPath, candidate) {
			slog.Debug("default branch resolved by probe", "repo", repoPath, "branch", candidate)
			return candidate, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}

	// 4. Fall back to the branch HEAD points at, when one exists.
	if branch, err := defaultBranchGitOutput(ctx, repoPath, "symbolic-ref", "--short", "HEAD"); err == nil && branch != "" {
		slog.Warn("default branch fell back to current HEAD branch",
			"repo", repoPath, "branch", branch)
		return branch, nil
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	return "", fmt.Errorf(
		"cannot determine default branch for %s: no origin/HEAD, no usable init.defaultBranch, no main or master branch, and HEAD is not on a branch",
		repoPath,
	)
}

// defaultBranchGitOutput runs a git command in repoPath and returns its
// trimmed stdout.
func defaultBranchGitOutput(ctx context.Context, repoPath string, args ...string) (string, error) {
	commandCtx, cancel, err := newWorktreeGitCommandContext(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.WaitDelay = worktreeGitCommandWaitDelay
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if cmdErr := worktreeCommandError(commandCtx, err); cmdErr != nil {
		return "", cmdErr
	}
	return strings.TrimSpace(string(output)), nil
}

// localBranchExists reports whether refs/heads/<branch> exists in repoPath.
func localBranchExists(ctx context.Context, repoPath, branch string) bool {
	if branch == "" {
		return false
	}
	_, err := defaultBranchGitOutput(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
