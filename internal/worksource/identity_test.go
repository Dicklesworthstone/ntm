package worksource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestBindBeadsCommandPreservesExplicitProjectAndLocalMetadata(t *testing.T) {
	project, _ := trackerFixture(t)
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(project, ".beads", "metadata.json")
	metadata := []byte(`{"database":"project-custom.db","jsonl_export":"project-custom.jsonl"}`)
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	t.Chdir(foreign)
	t.Setenv("BEADS_DB", filepath.Join(foreign, "ambient.db"))
	cmd := exec.Command("br", "info", "--json")
	cmd.Env = []string{"PATH=retained-path", "UNRELATED=retained-value", "SYSTEMROOT=retained-systemroot"}
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BD_DATABASE", "BEADS_JSONL", "BEADS_CACHE_DIR"} {
		cmd.Env = append(cmd.Env, key+"=foreign", strings.ToLower(key)+"=foreign-lowercase")
	}
	if err := BindBeadsCommand(cmd, project); err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != canonical {
		t.Fatalf("command directory=%q, want selected project %q", cmd.Dir, canonical)
	}
	want := map[string]string{
		"PATH": "retained-path", "UNRELATED": "retained-value", "SYSTEMROOT": "retained-systemroot",
		"BEADS_DIR": filepath.Join(canonical, ".beads"),
	}
	for _, entry := range cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || want[key] != value {
			t.Fatalf("unexpected scoped environment entry %q", entry)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("scoped environment lost entries: %v", want)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "info" || cmd.Args[2] != "--json" {
		t.Fatalf("binding changed command arguments: %v", cmd.Args)
	}
	gotMetadata, err := os.ReadFile(metadataPath)
	if err != nil || string(gotMetadata) != string(metadata) {
		t.Fatalf("local database routing changed: %s, %v", gotMetadata, err)
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != foreign || os.Getenv("BEADS_DB") != filepath.Join(foreign, "ambient.db") {
		t.Fatalf("binding changed process state: cwd=%q err=%v BEADS_DB=%q", cwd, err, os.Getenv("BEADS_DB"))
	}
}

func TestResolveBeadsProjectDirRefusesParentFallback(t *testing.T) {
	parent, _ := trackerFixture(t)
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, ".beads"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	for _, project := range []string{"", "  ", child, filepath.Join(parent, "missing"), invalid} {
		if resolved, err := ResolveBeadsProjectDir(project); err == nil {
			t.Errorf("project %q resolved to %q without a local tracker", project, resolved)
		}
		cmd := exec.Command("br", "info", "--json")
		if err := BindBeadsCommand(cmd, project); err == nil {
			t.Errorf("command accepted invalid project %q", project)
		}
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveBeadsProjectDir("."); err != nil || got != canonical {
		t.Fatalf("explicit relative project=%q err=%v, want %q", got, err, canonical)
	}
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
	if !before.Bound() || !errors.Is(before.Verify(after), ErrChanged) || !errors.Is(before.Verify(after), ErrSourceChanged) {
		t.Fatal("same-size, same-mtime tracker replacement did not invalidate identity")
	}
	// The mismatch is its own remediation receipt: both digests, named.
	var receipt *StaleError
	if !errors.As(before.Verify(after), &receipt) || receipt.Expected == nil || receipt.Observed == nil ||
		*receipt.Expected != before || *receipt.Observed != after || receipt.Expected.JSONLSHA256 == receipt.Observed.JSONLSHA256 {
		t.Fatalf("mismatch receipt = %+v, want expected %+v and observed %+v", receipt, before, after)
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
	if after.JSONLSHA256 != dirty.JSONLSHA256 || !errors.Is(dirty.Verify(after), ErrChanged) || !errors.Is(dirty.Verify(after), ErrSourceChanged) {
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
	if !present.Bound() || !errors.Is(missing.Verify(present), ErrChanged) || errors.Is(missing.Verify(present), ErrSourceChanged) {
		t.Fatal("new export did not invalidate identity")
	}
	// Rename rather than remove, preserving the fixture's evidence.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := present.Verify(captureFixture(t, dir)); !errors.Is(err, ErrChanged) || errors.Is(err, ErrSourceChanged) {
		t.Fatal("missing export reused a source-bound projection")
	}
}

// A fake local Git probe places each mutation at a known point in Capture:
// after inspecting the export but before opening it, or between HEAD reads.
// No timing races or sleeps are needed to distinguish movement from failure.
func TestCaptureClassifiesObservedSourceChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Git probe uses a POSIX shell")
	}
	for _, mode := range []string{"replace", "head", "missing", "invalid-head"} {
		t.Run(mode, func(t *testing.T) {
			dir, tracker := trackerFixture(t)
			if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := `#!/bin/sh
set -eu
if [ ! -e "$NTM_CAPTURE_PROBE" ]; then
  : > "$NTM_CAPTURE_PROBE"
  case "$NTM_CAPTURE_MODE" in
    replace)
      mv "$NTM_CAPTURE_TRACKER" "$NTM_CAPTURE_TRACKER.saved"
      printf '{"id":"new","status":"open"}\n' > "$NTM_CAPTURE_TRACKER"
      ;;
    missing) mv "$NTM_CAPTURE_TRACKER" "$NTM_CAPTURE_TRACKER.saved" ;;
    invalid-head) printf 'invalid-head\n'; exit 0 ;;
  esac
elif [ "$NTM_CAPTURE_MODE" = head ]; then
  printf 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n'
  exit 0
fi
printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NTM_CAPTURE_MODE", mode)
			t.Setenv("NTM_CAPTURE_TRACKER", tracker)
			t.Setenv("NTM_CAPTURE_PROBE", filepath.Join(t.TempDir(), "probe"))
			identity, err := Capture(context.Background(), dir)
			if err == nil || identity.Bound() {
				t.Fatalf("Capture returned unverified identity %+v with error %v", identity, err)
			}
			wantTransient := mode == "replace" || mode == "head"
			if errors.Is(err, ErrSourceChanged) != wantTransient {
				t.Fatalf("Capture error %v, transient=%v; want %v", err, errors.Is(err, ErrSourceChanged), wantTransient)
			}
			if wantTransient && !errors.Is(err, ErrChanged) {
				t.Fatalf("transient error lost the STALE_WORK_COORDINATION classification: %v", err)
			}
		})
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
