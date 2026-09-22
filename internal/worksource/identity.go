// Package worksource binds work projections to the local tracker and checkout.
// It never fetches, checks out, imports, or otherwise repairs either source.
package worksource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrChanged means a projection cannot be attributed to one source revision.
var ErrChanged = errors.New("STALE_WORK_COORDINATION")

// Identity identifies the bytes and checkout used to produce a projection.
// Dirty local edits are valid inputs: their actual digest, not their committed
// version, is recorded. A zero identity denotes a workspace without a JSONL
// export; consumers must not claim JSONL provenance for such a workspace.
type Identity struct {
	ProjectDir  string `json:"project_dir,omitempty"`
	JSONLPath   string `json:"jsonl_path,omitempty"`
	JSONLSHA256 string `json:"jsonl_sha256,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
}

// Bound reports whether a readable tracker export was included in this identity.
func (i Identity) Bound() bool { return i.JSONLSHA256 != "" }

// Verify rejects a stale saved identity, including a tracker appearing or
// disappearing between observations. It deliberately excludes filesystem mtimes
// from equality: timestamps alone do not identify content.
func (i Identity) Verify(current Identity) error {
	if i != current {
		return fmt.Errorf("%w: tracker or checkout changed; refresh work before dispatch", ErrChanged)
	}
	return nil
}

const maxTrackerBytes int64 = 64 << 20
const gitProbeTimeout = 2 * time.Second

// Capture reads a bounded local tracker digest and the local HEAD without
// mutating the checkout. A missing JSONL export preserves DB-only operation,
// but unreadable or unstable existing exports fail closed. ProjectDir should
// already be resolved to the authoritative Beads workspace by the caller.
func Capture(ctx context.Context, projectDir string) (Identity, error) {
	if ctx == nil {
		return Identity{}, errors.New("work source context is required")
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve work source: %w", err)
	}
	path := filepath.Join(root, ".beads", "issues.jsonl")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Identity{}, nil
	}
	if err != nil {
		return Identity{}, fmt.Errorf("inspect work source: %w", err)
	}
	// Resolve an existing symlink explicitly: a dangling export is NOT the
	// same as a workspace that never had an export.
	if info.Mode()&os.ModeSymlink != 0 {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return Identity{}, fmt.Errorf("resolve work tracker: %w", err)
		}
		info, err = os.Stat(path)
		if err != nil {
			return Identity{}, fmt.Errorf("inspect work tracker: %w", err)
		}
	}
	if !info.Mode().IsRegular() || info.Size() > maxTrackerBytes {
		return Identity{}, errors.New("work tracker must be a regular file no larger than 64 MiB")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve work tracker: %w", err)
	}
	head, err := readHead(ctx, root)
	if err != nil {
		return Identity{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Identity{}, fmt.Errorf("open work tracker: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return Identity{}, fmt.Errorf("stat work tracker: %w", err)
	}
	if !before.Mode().IsRegular() || !os.SameFile(info, before) {
		return Identity{}, fmt.Errorf("%w: tracker replaced during source capture", ErrChanged)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, maxTrackerBytes+1))
	if err != nil {
		return Identity{}, fmt.Errorf("hash work tracker: %w", err)
	}
	if n > maxTrackerBytes {
		return Identity{}, errors.New("work tracker exceeds 64 MiB")
	}
	after, err := file.Stat()
	if err != nil {
		return Identity{}, fmt.Errorf("stat work tracker after read: %w", err)
	}
	current, err := os.Stat(filepath.Join(root, ".beads", "issues.jsonl"))
	if err != nil || !os.SameFile(before, current) || n != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return Identity{}, fmt.Errorf("%w: tracker changed during source capture", ErrChanged)
	}
	finalHead, err := readHead(ctx, root)
	if err != nil {
		return Identity{}, err
	}
	if head != finalHead {
		return Identity{}, fmt.Errorf("%w: HEAD changed during source capture", ErrChanged)
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	return Identity{ProjectDir: root, JSONLPath: path, JSONLSHA256: hex.EncodeToString(hash.Sum(nil)), HeadSHA: head}, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func readHead(ctx context.Context, dir string) (string, error) {
	// Do not require Git for non-Git Beads workspaces. A present but broken
	// .git marker, on the other hand, must not silently erase provenance.
	found := false
	for parent := dir; ; parent = filepath.Dir(parent) {
		_, err := os.Lstat(filepath.Join(parent, ".git"))
		if err == nil {
			found = true
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect checkout identity: %w", err)
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if !found {
		return "", nil
	}
	head, err := gitOutput(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err == nil {
		if len(head) != 40 && len(head) != 64 {
			return "", errors.New("invalid checkout HEAD identity")
		}
		if _, err := hex.DecodeString(head); err != nil {
			return "", errors.New("invalid checkout HEAD identity")
		}
		return head, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// An unborn branch is a legitimate local-first workspace, not a broken
	// committed checkout. Verify the symbolic ref really does not exist.
	ref, refErr := gitOutput(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if refErr == nil && strings.HasPrefix(ref, "refs/heads/") {
		_, existsErr := gitOutput(ctx, dir, "show-ref", "--verify", "--quiet", ref)
		var exit *exec.ExitError
		if errors.As(existsErr, &exit) && exit.ExitCode() == 1 {
			return "unborn:" + ref, nil
		}
	}
	return "", fmt.Errorf("read checkout HEAD: %w", err)
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	probe, cancel := context.WithTimeout(ctx, gitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probe, "git", append([]string{"-C", dir}, args...)...)
	// A caller inside another worktree may export Git routing variables. The
	// explicit project, never that ambient repository, owns this identity.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_OPTIONAL_LOCKS":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if probe.Err() != nil {
		return "", probe.Err()
	}
	return strings.TrimSpace(string(out)), err
}
