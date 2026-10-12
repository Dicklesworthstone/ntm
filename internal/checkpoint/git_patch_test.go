package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const maxGitRecoveryPatchBytes = 10 << 20

func gitSnapshotTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func gitSnapshotTestWrite(t *testing.T, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

func gitSnapshotTestRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for recovery patch round trips")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := filepath.Join(t.TempDir(), "worktree with spaces ")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	gitSnapshotTestCommand(t, root, "init", "-q", "-b", "main")
	gitSnapshotTestCommand(t, root, "config", "user.name", "NTM Snapshot Test")
	gitSnapshotTestCommand(t, root, "config", "user.email", "snapshot@example.invalid")
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("base\n"), 0644)
	gitSnapshotTestWrite(t, root, "asset.bin", []byte{0, 1, 2, 255}, 0644)
	gitSnapshotTestWrite(t, root, "sub/inside.txt", []byte("inside\n"), 0644)
	gitSnapshotTestWrite(t, root, ".gitignore", []byte(".env\nignored/\n"), 0644)
	gitSnapshotTestCommand(t, root, "add", "--all")
	gitSnapshotTestCommand(t, root, "commit", "-qm", "base")
	return root, gitSnapshotTestCommand(t, root, "rev-parse", "HEAD")
}

func gitSnapshotTestApply(t *testing.T, source, patch string) string {
	t.Helper()
	recovery := filepath.Join(t.TempDir(), "recovery")
	gitSnapshotTestCommand(t, source, "clone", "--quiet", "--local", source, recovery)
	for _, args := range [][]string{{"apply", "--check", "-"}, {"apply", "--3way", "-"}} {
		cmd := exec.Command("git", append([]string{"-C", recovery}, args...)...)
		cmd.Stdin = strings.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("recovery git %v: %v\n%s\npatch:\n%s", args, err, out, patch)
		}
	}
	return recovery
}

func TestCaptureWorkingTreePatchBinaryAndUntrackedRoundTrip(t *testing.T) {
	root, commit := gitSnapshotTestRepo(t)
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("staged\n"), 0644)
	gitSnapshotTestCommand(t, root, "add", "tracked.txt")
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("working\n"), 0644)
	binary := []byte{0, 255, 7, 9, 1, 0, 128}
	gitSnapshotTestWrite(t, root, "asset.bin", binary, 0644)
	files := map[string][]byte{
		"new.go":             []byte("package recovered\n"),
		"new dir/nested.bin": {0, 111, 255, 0},
		"empty":              {},
		"-leading-option":    []byte("literal pathname\n"),
		"line\nbreak.txt":    []byte("newline pathname\n"),
		"nonutf8-\xff":       []byte("non-UTF8 pathname\n"),
	}
	for name, data := range files {
		gitSnapshotTestWrite(t, root, name, data, 0644)
	}
	gitSnapshotTestWrite(t, root, ".env", []byte("DO_NOT_CAPTURE=secret\n"), 0600)
	gitSnapshotTestWrite(t, root, "ignored/secret", []byte("ignored-secret\n"), 0600)
	gitSnapshotTestWrite(t, root, "new-executable", []byte("#!/bin/sh\nexit 0\n"), 0755)
	if err := os.Symlink("/outside/checkpoint-not-readable", filepath.Join(root, "new-link")); err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git/index"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := captureWorkingTreePatch(context.Background(), filepath.Join(root, "sub"), commit,
		gitPatchCaptureOptions{IncludeUntracked: true, MaxBytes: maxGitRecoveryPatchBytes})
	if err != nil {
		t.Fatal(err)
	}
	if result.UntrackedCaptured != len(files)+2 || !strings.Contains(result.Patch, "GIT binary patch") {
		t.Fatalf("incomplete recovery snapshot: files=%d patch=%s", result.UntrackedCaptured, result.Patch)
	}
	if strings.Contains(result.Patch, "DO_NOT_CAPTURE") || strings.Contains(result.Patch, "ignored-secret") {
		t.Fatal("ignored file content leaked into recovery patch")
	}
	indexAfter, err := os.ReadFile(filepath.Join(root, ".git/index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("capture modified the user's real staging index: %v", err)
	}
	if got := gitSnapshotTestCommand(t, root, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("capture moved HEAD: %s", got)
	}
	recovery := gitSnapshotTestApply(t, root, result.Patch)
	files["tracked.txt"] = []byte("working\n")
	files["asset.bin"] = binary
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(recovery, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("recovered %q = %q, %v; want %q", name, got, err, want)
		}
	}
	if link, err := os.Readlink(filepath.Join(recovery, "new-link")); err != nil || link != "/outside/checkpoint-not-readable" {
		t.Fatalf("recovered symlink = %q, %v", link, err)
	}
	info, err := os.Stat(filepath.Join(recovery, "new-executable"))
	if err != nil || info.Mode()&0111 == 0 {
		t.Fatalf("executable mode was not recovered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recovery, ".env")); !os.IsNotExist(err) {
		t.Fatalf("ignored file was restored: %v", err)
	}
}

func TestCaptureWorkingTreePatchUntrackedIsOptIn(t *testing.T) {
	root, commit := gitSnapshotTestRepo(t)
	gitSnapshotTestWrite(t, root, "new.txt", []byte("not opted in\n"), 0644)
	gitSnapshotTestWrite(t, root, "asset.bin", []byte{0, 255, 254}, 0644)
	result, err := captureWorkingTreePatch(context.Background(), root, commit,
		gitPatchCaptureOptions{MaxBytes: maxGitRecoveryPatchBytes})
	if err != nil {
		t.Fatal(err)
	}
	if result.UntrackedCaptured != 0 || strings.Contains(result.Patch, "new.txt") || !strings.Contains(result.Patch, "GIT binary patch") {
		t.Fatalf("default capture must save binary tracked changes only: %+v", result)
	}
	gitSnapshotTestApply(t, root, result.Patch)
}

func TestCaptureWorkingTreePatchOverridesPresentationConfiguration(t *testing.T) {
	root, commit := gitSnapshotTestRepo(t)
	gitSnapshotTestWrite(t, root, ".gitattributes", []byte("*.bin diff=preview\n"), 0644)
	gitSnapshotTestCommand(t, root, "add", ".gitattributes")
	gitSnapshotTestCommand(t, root, "commit", "-qm", "attributes")
	commit = gitSnapshotTestCommand(t, root, "rev-parse", "HEAD")
	for key, value := range map[string]string{
		"color.ui": "always", "diff.noprefix": "true", "diff.relative": "true",
		"diff.external": "ntm-nonexistent-external-diff", "diff.preview.textconv": "ntm-nonexistent-textconv",
	} {
		gitSnapshotTestCommand(t, root, "config", key, value)
	}
	gitSnapshotTestWrite(t, root, "asset.bin", []byte{0, 222, 255}, 0644)
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte("outside active subdir\n"), 0644)
	gitSnapshotTestWrite(t, root, "sub/inside.txt", []byte("inside changed\n"), 0644)
	result, err := captureWorkingTreePatch(context.Background(), filepath.Join(root, "sub"), commit,
		gitPatchCaptureOptions{MaxBytes: maxGitRecoveryPatchBytes})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(result.Patch, '\x1b') || !strings.Contains(result.Patch, "a/tracked.txt b/tracked.txt") {
		t.Fatalf("presentation settings contaminated patch:\n%s", result.Patch)
	}
	recovery := gitSnapshotTestApply(t, root, result.Patch)
	if got, err := os.ReadFile(filepath.Join(recovery, "asset.bin")); err != nil || !bytes.Equal(got, []byte{0, 222, 255}) {
		t.Fatalf("binary with textconv attributes was not recovered: %v", err)
	}
}

func TestCaptureWorkingTreePatchLimitsAndCancellation(t *testing.T) {
	root, commit := gitSnapshotTestRepo(t)
	gitSnapshotTestWrite(t, root, "tracked.txt", []byte(strings.Repeat("different content\n", 200)), 0644)
	result, err := captureWorkingTreePatch(context.Background(), root, commit,
		gitPatchCaptureOptions{IncludeUntracked: true, MaxBytes: 64})
	if !errors.Is(err, errGitRecoveryPatchTooLarge) || result.Patch != "" || result.UntrackedCaptured != 0 {
		t.Fatalf("size failure returned partial recovery data: %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = captureWorkingTreePatch(ctx, root, commit, gitPatchCaptureOptions{MaxBytes: 64})
	if !errors.Is(err, context.Canceled) || result.Patch != "" {
		t.Fatalf("cancellation = %+v, %v", result, err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err = captureWorkingTreePatch(ctx, root, commit, gitPatchCaptureOptions{MaxBytes: 64}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline = %v", err)
	}
	for _, bad := range []string{"HEAD", "--help", strings.Repeat("z", 40)} {
		if _, err := captureWorkingTreePatch(context.Background(), root, bad, gitPatchCaptureOptions{MaxBytes: 64}); err == nil {
			t.Fatalf("accepted unpinned or invalid commit %q", bad)
		}
	}
}

func TestCaptureWorkingTreePatchRejectsNestedRepository(t *testing.T) {
	root, commit := gitSnapshotTestRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	gitSnapshotTestCommand(t, nested, "init", "-q", "-b", "main")
	gitSnapshotTestWrite(t, nested, "work.txt", []byte("nested work must not vanish\n"), 0644)
	result, err := captureWorkingTreePatch(context.Background(), root, commit,
		gitPatchCaptureOptions{IncludeUntracked: true, MaxBytes: maxGitRecoveryPatchBytes})
	if err == nil || result.Patch != "" || result.UntrackedCaptured != 0 {
		t.Fatalf("nested repository was silently omitted: %+v, %v", result, err)
	}
}

func TestInspectUntrackedGitPathRejectsEscapesAndParentSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	gitSnapshotTestWrite(t, outside, "private", []byte("must not read\n"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "../private", "/absolute", "alias/private", "x/../alias"} {
		if _, err := inspectUntrackedGitPath(root, name); err == nil {
			t.Errorf("accepted unsafe path %q", name)
		}
	}
	if _, err := inspectUntrackedGitPath(root, "alias"); err != nil {
		t.Fatalf("a leaf symlink's text is safe to capture: %v", err)
	}
}

func TestGitPatchBufferCannotBypassLimitWithReaderFrom(t *testing.T) {
	out := &gitPatchBuffer{limit: 3}
	// Hide the source's WriterTo so io.Copy would prefer the destination's
	// ReaderFrom, which embedding bytes.Buffer would accidentally promote.
	source := struct{ io.Reader }{strings.NewReader("oversized")}
	if _, err := io.Copy(out, source); !errors.Is(err, errGitRecoveryPatchTooLarge) || out.Len() > 3 {
		t.Fatalf("copy bypassed size bound: len=%d err=%v", out.Len(), err)
	}
}
