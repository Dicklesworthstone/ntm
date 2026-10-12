package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// GitRollbackPlan holds verified bytes and a pinned commit for one rollback.
// Preparing a plan never interrupts agents or changes the repository's index,
// refs or working files. WorkDir still needs the caller's authorization check.
type GitRollbackPlan struct {
	WorkDir  string
	Commit   string
	Patch    string
	Warnings []string
}

// ApplyGitRollbackPatch applies only the bytes verified by PrepareGitRollback.
// The caller must have authorized WorkDir and checked out Commit. Failure is
// fatal: Git may have left conflicts, which must be inspected before retrying.
func ApplyGitRollbackPatch(ctx context.Context, plan *GitRollbackPlan) error {
	if ctx == nil || plan == nil {
		return errors.New("applying a rollback patch requires a context and plan")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.Patch == "" {
		return nil
	}
	out := &gitPatchBuffer{limit: 64 << 10}
	return runGitRecoveryCommandInput(ctx, plan.WorkDir, out, false, strings.NewReader(plan.Patch), nil,
		"apply", "--3way", "--whitespace=nowarn", "-")
}

// PrepareGitRollback rejects unusable/corrupt recovery data before rollback can
// stash current work or move HEAD. The patch is checked against the saved commit
// in a disposable index, so a dirty current tree does not invalidate a good
// checkpoint. Callers must apply the returned bytes, not reopen the artifact.
func PrepareGitRollback(ctx context.Context, storage *Storage, cp *Checkpoint, workDir string) (*GitRollbackPlan, error) {
	if ctx == nil {
		return nil, errors.New("rollback context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cp == nil || storage == nil {
		return nil, errors.New("rollback requires a checkpoint and storage")
	}
	commit := cp.Git.Commit
	if len(commit) < 7 || len(commit) > 64 || strings.Trim(commit, "0123456789abcdefABCDEF") != "" {
		return nil, errors.New("checkpoint has no valid git commit to restore (use --no-git to skip git recovery)")
	}
	root, err := GitWorktreeRoot(ctx, workDir)
	if err != nil {
		return nil, err
	}
	resolved := &gitPatchBuffer{limit: 256}
	if err := runGitRecoveryCommand(ctx, root, resolved, false, "rev-parse", "--verify", commit+"^{commit}"); err != nil {
		return nil, fmt.Errorf("resolving checkpoint commit: %w", err)
	}
	plan := &GitRollbackPlan{WorkDir: root, Commit: strings.TrimSpace(resolved.String())}
	plan.Warnings, err = preflightCheckpointArtifacts(ctx, storage, cp, cp.SessionName)
	if err != nil {
		return nil, err
	}
	if cp.Git.UntrackedCount > cp.Git.UntrackedCaptured {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("checkpoint did not capture %d untracked files", cp.Git.UntrackedCount-cp.Git.UntrackedCaptured))
	}
	if cp.Git.PatchFile == "" {
		if cp.Git.UntrackedCaptured > 0 {
			return nil, errors.New("checkpoint claims captured untracked files but has no git patch")
		}
		return plan, nil
	}
	dir, err := storage.safeCheckpointDir(cp.SessionName, cp.ID)
	if err != nil {
		return nil, err
	}
	if err := validateCheckpointArtifactReferences(cp); err != nil {
		return nil, err
	}
	path, err := resolveExistingCheckpointArtifactPath(dir, cp.Git.PatchFile)
	if err != nil {
		return nil, fmt.Errorf("resolving rollback patch: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening rollback patch: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(artifactContextReader{ctx: ctx, reader: f}, int64(MaxGitOutputBytes)+1))
	closeErr := f.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("reading rollback patch: %w", err)
	}
	if len(data) == 0 || len(data) > MaxGitOutputBytes {
		return nil, errors.New("rollback patch is empty or exceeds the git capture size limit")
	}
	// Preflight hashed the stored artifact. Check these exact bytes again:
	// a replacement between verification and reading must not be applied.
	if cp.ArtifactIntegrity != nil {
		expected, ok := cp.ArtifactIntegrity.Files[cp.Git.PatchFile]
		if !ok {
			return nil, fmt.Errorf("%w: rollback patch checksum missing", ErrArtifactIntegrity)
		}
		if err := verifyArtifactBytes(data, expected); err != nil {
			return nil, fmt.Errorf("%w: rollback patch: %v", ErrArtifactIntegrity, err)
		}
	}
	plan.Patch = string(data)
	if err := checkGitRollbackPatch(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func checkGitRollbackPatch(ctx context.Context, plan *GitRollbackPlan) error {
	dir, err := os.MkdirTemp("", "ntm-rollback-preflight-")
	if err != nil {
		return fmt.Errorf("creating rollback preflight index: %w", err)
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	out := &gitPatchBuffer{limit: 64 << 10}
	if err := runGitRecoveryCommandInput(ctx, plan.WorkDir, out, false, nil, env, "read-tree", plan.Commit); err != nil {
		return fmt.Errorf("preparing rollback validation index: %w", err)
	}
	if err := runGitRecoveryCommandInput(ctx, plan.WorkDir, out, false, strings.NewReader(plan.Patch), env,
		"apply", "--cached", "--check", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("checkpoint patch cannot restore its recorded commit: %w", err)
	}
	return nil
}
