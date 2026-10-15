package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	ErrMergeUnsafe            = errors.New("worktree merge refused")
	ErrMergeOutcomeUnverified = errors.New("worktree merge outcome requires inspection")
)

// MergeOptions binds an apply to a previously inspected pair of full commit
// IDs. Empty expectations request a fresh assessment; partial pairs are errors.
type MergeOptions struct {
	ExpectedSource string
	ExpectedTarget string
}

func mergeOptions(expectations []MergeOptions) (MergeOptions, error) {
	if len(expectations) > 1 {
		return MergeOptions{}, errors.New("merge accepts at most one expectation pair")
	}
	var opts MergeOptions
	if len(expectations) == 1 {
		opts = expectations[0]
	}
	if opts.ExpectedSource == "" && opts.ExpectedTarget == "" {
		return opts, nil
	}
	if !isMergeObjectID(opts.ExpectedSource) || !isMergeObjectID(opts.ExpectedTarget) {
		return MergeOptions{}, errors.New("expected source and target must both be full commit IDs from a merge preview")
	}
	return opts, nil
}

// applyMergePreview is reached only after a fresh preview and expectation
// validation. Recheck ownership and checkout state at the mutation boundary;
// pass the frozen source commit, never the moving agent branch, to Git.
func applyMergePreview(ctx context.Context, plan *MergePreview) error {
	if err := verifyMergeRepository(ctx, plan.TargetPath, plan.SourcePath); err != nil {
		return err
	}
	for _, checkout := range []struct{ path, branch, role string }{
		{plan.SourcePath, plan.SourceBranch, "source"},
		{plan.TargetPath, plan.TargetBranch, "target"},
	} {
		blockers, err := mergeCheckoutBlockers(ctx, checkout.path, checkout.branch, checkout.role)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return fmt.Errorf("%w: %s", ErrMergeUnsafe, strings.Join(blockers, ", "))
		}
	}
	risks, err := mergeIgnoredOverwriteRisks(ctx, plan)
	if err != nil {
		return err
	}
	if len(risks) > 0 {
		return fmt.Errorf("%w: target_ignored_path_collision; inspect worktrees merge --dry-run", ErrMergeUnsafe)
	}
	if err := verifyMergeSnapshot(ctx, plan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Pin the semantics used by merge-tree. Inherited branch.mergeOptions can
	// otherwise turn this into a squash, ours merge, or successful --no-commit.
	// Do not bypass hooks; a rejected hook retains normal Git recovery state.
	cmd := exec.CommandContext(ctx, "git", "-c", "branch."+plan.TargetBranch+".mergeOptions=", "merge",
		"--strategy=ort", "--no-ff", "--no-squash", "--commit", "--no-edit", "--no-autostash", "--no-overwrite-ignore",
		"-m", fmt.Sprintf("Merge agent %s work from session %s", plan.AgentName, plan.SessionID), plan.SourceCommit)
	cmd.Dir = plan.TargetPath
	cmd.WaitDelay = time.Second
	output, runErr := cmd.CombinedOutput()
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrMergeOutcomeUnverified, err,
			errors.New("merge stopped; inspect HEAD, git status and MERGE_HEAD before retrying; no rollback was attempted"))
	}
	if runErr != nil {
		return fmt.Errorf("%w: %w: %s; inspect git status before retrying; no rollback was attempted",
			ErrMergeOutcomeUnverified, runErr, strings.TrimSpace(string(output)))
	}

	// Exit zero is not sufficient evidence: hooks or another Git writer can
	// move HEAD or rewrite the index. Only the exact two-parent, planned tree
	// on the target branch is acknowledged as success. Never auto-reset it.
	branch, err := worktreeCurrentBranch(ctx, plan.TargetPath)
	if err != nil || branch != plan.TargetBranch {
		return errors.Join(ErrMergeOutcomeUnverified, err, errors.New("target branch changed during merge; inspect the checkout"))
	}
	out, err := gitOutput(ctx, plan.TargetPath, "show", "-s", "--format=%T%x00%P", "HEAD")
	if err != nil {
		return errors.Join(ErrMergeOutcomeUnverified, err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "\x00")
	if len(parts) != 2 || parts[0] != plan.MergeTree || parts[1] != plan.TargetCommit+" "+plan.SourceCommit {
		return fmt.Errorf("%w: HEAD does not match the preview's tree and parents; inspect before retrying", ErrMergeOutcomeUnverified)
	}
	blockers, err := mergeCheckoutBlockers(ctx, plan.TargetPath, plan.TargetBranch, "target")
	if err != nil || len(blockers) > 0 {
		return errors.Join(ErrMergeOutcomeUnverified, err,
			fmt.Errorf("target checkout needs inspection after merge: %s", strings.Join(blockers, ", ")))
	}
	return nil
}
