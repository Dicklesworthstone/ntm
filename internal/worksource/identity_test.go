package worksource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trackerFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"a\",\"status\":\"open\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for checkout identity tests")
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=NTM Test", "GIT_COMMITTER_NAME=NTM Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func captureFixture(t *testing.T, dir string) Identity {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := Capture(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCaptureTracksContentNotMtime(t *testing.T) {
	dir, path := trackerFixture(t)
	before := captureFixture(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"id\":\"b\",\"status\":\"open\"}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after := captureFixture(t, dir)
	want := sha256.Sum256(content)
	if after.JSONLSHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %s", after.JSONLSHA256)
	}
	if !before.Bound() || !errors.Is(before.Verify(after), ErrChanged) {
		t.Fatal("same-size, same-mtime tracker replacement did not invalidate identity")
	}
	if err := after.Verify(captureFixture(t, dir)); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureBindsHeadAndAllowsDirtyWork(t *testing.T) {
	dir, path := trackerFixture(t)
	gitFixture(t, dir, "init", "--quiet")
	gitFixture(t, dir, "add", ".beads/issues.jsonl")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "initial")
	before := captureFixture(t, dir)
	if before.HeadSHA != gitFixture(t, dir, "rev-parse", "HEAD") {
		t.Fatal("wrong HEAD")
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"dirty-local-task\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := captureFixture(t, dir)
	if dirty.HeadSHA != before.HeadSHA || dirty.JSONLSHA256 == before.JSONLSHA256 {
		t.Fatal("local edits must get their own digest without changing HEAD")
	}
	// Dirty code unrelated to the tracker is not a validity condition.
	if err := os.WriteFile(filepath.Join(dir, "code.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dirty.Verify(captureFixture(t, dir)); err != nil {
		t.Fatal("unrelated dirty work invalidated tracker identity:", err)
	}
	gitFixture(t, dir, "add", "code.go")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "code only")
	after := captureFixture(t, dir)
	if after.JSONLSHA256 != dirty.JSONLSHA256 || !errors.Is(dirty.Verify(after), ErrChanged) {
		t.Fatal("HEAD-only change failed to invalidate identity")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "dirty-local-task") {
		t.Fatal("capture mutated tracker", err)
	}
}

func TestCaptureUnbornAndLinkedWorktree(t *testing.T) {
	dir, _ := trackerFixture(t)
	gitFixture(t, dir, "init", "--quiet")
	unborn := captureFixture(t, dir)
	if !strings.HasPrefix(unborn.HeadSHA, "unborn:refs/heads/") {
		t.Fatalf("unborn identity = %+v", unborn)
	}
	gitFixture(t, dir, "add", ".beads/issues.jsonl")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	gitFixture(t, dir, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
	id := captureFixture(t, linked)
	if !id.Bound() || id.HeadSHA != gitFixture(t, dir, "rev-parse", "HEAD") || id.ProjectDir == captureFixture(t, dir).ProjectDir {
		t.Fatalf("linked worktree identity = %+v", id)
	}
}

func TestCaptureDoesNotFollowAmbientGitRouting(t *testing.T) {
	dir, _ := trackerFixture(t)
	gitFixture(t, dir, "init", "--quiet")
	gitFixture(t, dir, "add", ".beads/issues.jsonl")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "initial")
	want := captureFixture(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "wrong.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_COMMON_DIR", t.TempDir())
	if got := captureFixture(t, dir); got != want {
		t.Fatalf("ambient repository changed identity: %+v", got)
	}
}

func TestCaptureMissingAppearingAndRemovedTracker(t *testing.T) {
	dir := t.TempDir()
	missing := captureFixture(t, dir)
	if missing.Bound() {
		t.Fatal("DB-only workspace claimed JSONL provenance")
	}
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	present := captureFixture(t, dir)
	if !present.Bound() || !errors.Is(missing.Verify(present), ErrChanged) {
		t.Fatal("new export did not invalidate identity")
	}
	// Rename rather than remove, preserving the fixture's evidence.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(present.Verify(captureFixture(t, dir)), ErrChanged) {
		t.Fatal("missing export reused a source-bound projection")
	}
}

func TestCaptureCanonicalPathsAndDanglingSymlinks(t *testing.T) {
	dir, _ := trackerFixture(t)
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks: %v", err)
	}
	if captureFixture(t, dir) != captureFixture(t, link) {
		t.Fatal("same source through symlink changed identity")
	}
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "absent"), filepath.Join(other, ".beads", "issues.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), other); err == nil {
		t.Fatal("dangling tracker silently became a DB-only workspace")
	}
}

func TestCaptureRejectsInvalidSourcesAndCancellation(t *testing.T) {
	if _, err := Capture(nil, t.TempDir()); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".beads", "issues.jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Capture(context.Background(), dir); err == nil {
			t.Fatal("directory accepted")
		}
	})
	t.Run("oversize", func(t *testing.T) {
		dir, path := trackerFixture(t)
		if err := os.Truncate(path, maxTrackerBytes+1); err != nil {
			t.Fatal(err)
		}
		if _, err := Capture(context.Background(), dir); err == nil {
			t.Fatal("oversize export accepted")
		}
	})
	t.Run("broken git", func(t *testing.T) {
		dir, _ := trackerFixture(t)
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Capture(context.Background(), dir); err == nil {
			t.Fatal("broken checkout discarded HEAD provenance")
		}
	})
}
