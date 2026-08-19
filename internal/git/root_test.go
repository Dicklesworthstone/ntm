package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindProjectRoot(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "ntm-git-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skip("git init failed, skipping test:", err)
	}

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test from root
	root, err := FindProjectRoot(tmpDir)
	if err != nil {
		t.Errorf("FindProjectRoot(root) error: %v", err)
	}
	// On Mac/Linux, /tmp might be a symlink to /private/tmp, so resolve symlinks
	realTmp, _ := filepath.EvalSymlinks(tmpDir)
	realRoot, _ := filepath.EvalSymlinks(root)
	if realRoot != realTmp {
		t.Errorf("expected root %s, got %s", realTmp, realRoot)
	}

	// Test from subdir
	root, err = FindProjectRoot(subDir)
	if err != nil {
		t.Errorf("FindProjectRoot(subdir) error: %v", err)
	}
	realRoot, _ = filepath.EvalSymlinks(root)
	if realRoot != realTmp {
		t.Errorf("expected root %s, got %s", realTmp, realRoot)
	}
}

// commonDirTestRepo creates a real temp repo with one commit and returns its
// path. Skips when git is unavailable.
func commonDirTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	return repo
}

// TestCommonDirSharedAcrossLinkedWorktrees — the base checkout and a linked
// worktree must resolve to the same physical identity, while an unrelated
// repository resolves elsewhere and a plain directory errors.
func TestCommonDirSharedAcrossLinkedWorktrees(t *testing.T) {
	base := commonDirTestRepo(t)
	worktree := filepath.Join(base, ".ntm", "worktrees", "sess", "cod_1")
	cmd := exec.Command("git", "worktree", "add", "-b", "cod_1", worktree)
	cmd.Dir = base
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree add failed: %v\n%s", err, out)
	}

	baseCommon, err := CommonDir(t.Context(), base)
	if err != nil {
		t.Fatalf("CommonDir(base): %v", err)
	}
	realBase, _ := filepath.EvalSymlinks(base)
	if baseCommon != filepath.Join(realBase, ".git") {
		t.Fatalf("CommonDir(base) = %q, want %q", baseCommon, filepath.Join(realBase, ".git"))
	}

	worktreeCommon, err := CommonDir(t.Context(), worktree)
	if err != nil {
		t.Fatalf("CommonDir(worktree): %v", err)
	}
	if worktreeCommon != baseCommon {
		t.Fatalf("linked worktree common dir %q != base common dir %q", worktreeCommon, baseCommon)
	}

	// From a subdirectory of the base checkout the identity is unchanged.
	subDir := filepath.Join(base, "nested")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	subCommon, err := CommonDir(t.Context(), subDir)
	if err != nil {
		t.Fatalf("CommonDir(subdir): %v", err)
	}
	if subCommon != baseCommon {
		t.Fatalf("subdir common dir %q != base common dir %q", subCommon, baseCommon)
	}

	other := commonDirTestRepo(t)
	otherCommon, err := CommonDir(t.Context(), other)
	if err != nil {
		t.Fatalf("CommonDir(other): %v", err)
	}
	if otherCommon == baseCommon {
		t.Fatalf("distinct repositories share common dir %q", otherCommon)
	}
}

// TestCommonDirRejectsNonRepo — a directory outside any repository must error
// rather than fabricate an identity.
func TestCommonDirRejectsNonRepo(t *testing.T) {
	if _, err := CommonDir(t.Context(), t.TempDir()); err == nil {
		t.Fatal("CommonDir(non-repo) succeeded, want error")
	}
}
