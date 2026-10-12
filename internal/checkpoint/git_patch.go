package checkpoint

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const maxUntrackedListingBytes = 4 << 20

var errGitRecoveryPatchTooLarge = errors.New("git recovery output exceeds size limit")

type gitPatchCaptureOptions struct {
	IncludeUntracked bool
	MaxBytes         int
}

type gitPatchCaptureResult struct {
	Patch             string
	UntrackedCaptured int
}

// captureWorkingTreePatch records the combined tracked worktree delta against
// a pinned commit, plus non-ignored untracked files only when requested. It
// never stages files, writes Git objects, changes refs, or changes the worktree.
// Git produces the patch syntax, including binary bodies, modes, symlinks and
// quoted filenames; NTM does not manufacture or rewrite diff headers.
func captureWorkingTreePatch(ctx context.Context, dir, commit string, opts gitPatchCaptureOptions) (gitPatchCaptureResult, error) {
	if ctx == nil {
		return gitPatchCaptureResult{}, errors.New("git capture context is required")
	}
	if err := ctx.Err(); err != nil {
		return gitPatchCaptureResult{}, err
	}
	if len(commit) != 40 && len(commit) != 64 {
		return gitPatchCaptureResult{}, errors.New("git capture requires a full commit object ID")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return gitPatchCaptureResult{}, fmt.Errorf("invalid git capture commit: %w", err)
	}
	if opts.MaxBytes <= 0 {
		return gitPatchCaptureResult{}, errors.New("git patch size limit must be positive")
	}
	root, err := GitWorktreeRoot(ctx, dir)
	if err != nil {
		return gitPatchCaptureResult{}, err
	}

	patch := &gitPatchBuffer{limit: opts.MaxBytes}
	args := append(gitRecoveryDiffArgs(), commit, "--")
	if err := runGitRecoveryCommand(ctx, root, patch, false, args...); err != nil {
		return gitPatchCaptureResult{}, fmt.Errorf("capturing tracked changes: %w", err)
	}
	if !opts.IncludeUntracked {
		return gitPatchCaptureResult{Patch: patch.String()}, nil
	}

	paths, err := listGitUntrackedFiles(ctx, root)
	if err != nil {
		return gitPatchCaptureResult{}, err
	}
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return gitPatchCaptureResult{}, err
		}
		info, err := inspectUntrackedGitPath(root, name)
		if err != nil {
			return gitPatchCaptureResult{}, err
		}
		start := patch.Len()
		args := append(gitRecoveryDiffArgs(), "--no-index", "--", os.DevNull, name)
		if err := runGitRecoveryCommand(ctx, root, patch, true, args...); err != nil {
			return gitPatchCaptureResult{}, fmt.Errorf("capturing untracked file %q: %w", name, err)
		}
		// A missing input can also make no-index exit 1. An accepted exit
		// status alone must never be counted as a successfully saved file.
		if patch.Len() == start {
			return gitPatchCaptureResult{}, fmt.Errorf("untracked file %q produced no recovery patch", name)
		}
		after, err := inspectUntrackedGitPath(root, name)
		if err != nil {
			return gitPatchCaptureResult{}, err
		}
		if !os.SameFile(info, after) || info.Size() != after.Size() ||
			info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) {
			return gitPatchCaptureResult{}, fmt.Errorf("untracked file %q changed during capture; retry the checkpoint", name)
		}
	}
	// Reject an obviously inconsistent selection when files appeared,
	// disappeared or became tracked during capture. This is not a filesystem
	// transaction and does not claim to freeze concurrent edits.
	after, err := listGitUntrackedFiles(ctx, root)
	if err != nil {
		return gitPatchCaptureResult{}, err
	}
	if !slices.Equal(paths, after) {
		return gitPatchCaptureResult{}, errors.New("untracked file selection changed during capture; retry the checkpoint")
	}
	return gitPatchCaptureResult{Patch: patch.String(), UntrackedCaptured: len(paths)}, nil
}

// Presentation settings must not replace recovery bytes with an external diff,
// textconv preview, colorized output or directory-relative fragment.
func gitRecoveryDiffArgs() []string {
	return []string{"diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-relative", "--no-renames", "--ignore-submodules=none",
		"--src-prefix=a/", "--dst-prefix=b/"}
}

// GitWorktreeRoot resolves the repository root used by recovery patches.
// Remote callers must enforce their project-directory boundary on the result
// before performing mutations; an allowed subdirectory can have a parent root.
func GitWorktreeRoot(ctx context.Context, dir string) (string, error) {
	if ctx == nil {
		return "", errors.New("git context is required")
	}
	out := &gitPatchBuffer{limit: 64 << 10}
	if err := runGitRecoveryCommand(ctx, dir, out, false, "rev-parse", "--show-toplevel"); err != nil {
		return "", fmt.Errorf("resolving git worktree root: %w", err)
	}
	// Do not TrimSpace: spaces can legitimately begin or end a path.
	root := strings.TrimSuffix(out.String(), "\n")
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("git did not report an absolute worktree root")
	}
	return root, nil
}

func listGitUntrackedFiles(ctx context.Context, root string) ([]string, error) {
	out := &gitPatchBuffer{limit: maxUntrackedListingBytes}
	if err := runGitRecoveryCommand(ctx, root, out, false,
		"ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--"); err != nil {
		return nil, fmt.Errorf("enumerating non-ignored untracked files: %w", err)
	}
	if out.Len() == 0 {
		return nil, nil
	}
	if out.Bytes()[out.Len()-1] != 0 {
		return nil, errors.New("git returned an incomplete untracked file listing")
	}
	paths := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
	slices.Sort(paths)
	return paths, nil
}

func inspectUntrackedGitPath(root, name string) (os.FileInfo, error) {
	local := filepath.FromSlash(name)
	if name == "" || !filepath.IsLocal(local) || local == "." ||
		filepath.ToSlash(filepath.Clean(local)) != name {
		return nil, fmt.Errorf("invalid untracked path %q (nested repositories are not captured)", name)
	}
	// A parent symlink must not redirect a capture outside the tree. A leaf
	// symlink is saved as link text, not as the contents of its target.
	for parent := filepath.Dir(local); parent != "."; parent = filepath.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, parent))
		if err != nil {
			return nil, fmt.Errorf("inspecting untracked path %q: %w", name, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("untracked path %q has a non-directory or symlink parent", name)
		}
	}
	info, err := os.Lstat(filepath.Join(root, local))
	if err != nil {
		return nil, fmt.Errorf("inspecting untracked file %q: %w", name, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("untracked path %q is not a regular file or symlink (nested repositories and special files are not captured)", name)
	}
	return info, nil
}

// noIndexDifference accepts exit 1 only for a successful no-index difference.
// The caller also requires a nonempty patch. Cancellation, limit errors and
// diagnostics take precedence over that normal difference exit status.
func runGitRecoveryCommand(ctx context.Context, dir string, stdout *gitPatchBuffer, noIndexDifference bool, args ...string) error {
	return runGitRecoveryCommandInput(ctx, dir, stdout, noIndexDifference, nil, nil, args...)
}

func runGitRecoveryCommandInput(ctx context.Context, dir string, stdout *gitPatchBuffer, noIndexDifference bool, stdin io.Reader, env []string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout.onLimit = cancel
	defer func() { stdout.onLimit = nil }()
	gitArgs := append([]string{"--no-pager", "-c", "core.quotePath=true", "-C", dir}, args...)
	cmd := exec.CommandContext(commandCtx, "git", gitArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	cmd.WaitDelay = time.Second
	cmd.Stdout = stdout
	stderr := &gitPatchBuffer{limit: 64 << 10, onLimit: cancel}
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return fmt.Errorf("%w (stdout budget %d bytes)", errGitRecoveryPatchTooLarge, stdout.limit)
	}
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if noIndexDifference && errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
		return nil
	}
	return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
}

// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy bypass
// Write, silently defeating the aggregate output bound.
type gitPatchBuffer struct {
	output   bytes.Buffer
	limit    int
	exceeded bool
	onLimit  func()
}

var _ io.Writer = (*gitPatchBuffer)(nil)

func (b *gitPatchBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.output.Len() {
		b.exceeded = true
		if b.onLimit != nil {
			b.onLimit()
		}
		return 0, errGitRecoveryPatchTooLarge
	}
	return b.output.Write(p)
}

func (b *gitPatchBuffer) Len() int       { return b.output.Len() }
func (b *gitPatchBuffer) String() string { return b.output.String() }
func (b *gitPatchBuffer) Bytes() []byte  { return b.output.Bytes() }
