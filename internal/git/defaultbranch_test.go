package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGitConfig keeps host-level git configuration (e.g. a global
// init.defaultBranch) from leaking into DefaultBranch resolution. Tests using
// it must not call t.Parallel (t.Setenv forbids it).
func isolateGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// setupRepoWithInitialBranch creates a real temp git repo whose initial
// branch is the given name, with one commit.
func setupRepoWithInitialBranch(t *testing.T, branch string) string {
	t.Helper()
	tmp := t.TempDir()
	cmds := [][]string{
		{"init", "-b", branch},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		if out, err := runGitTestCommand(t, tmp, args...); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

func TestDefaultBranch_OriginHEADWins(t *testing.T) {
	isolateGitConfig(t)

	remote := setupRepoWithInitialBranch(t, "master")
	parent := t.TempDir()
	if out, err := runGitTestCommand(t, parent, "clone", remote, "local"); err != nil {
		t.Skipf("git clone failed: %v\n%s", err, out)
	}
	clone := filepath.Join(parent, "local")
	// Create a local 'main' so the probe would find it; origin/HEAD (master)
	// must still win, proving chain order.
	if out, err := runGitTestCommand(t, clone, "branch", "main"); err != nil {
		t.Fatalf("git branch main failed: %v\n%s", err, out)
	}

	branch, err := DefaultBranch(t.Context(), clone)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "master")
}

func TestDefaultBranch_InitDefaultBranchConfig(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "trunk")
	if out, err := runGitTestCommand(t, repo, "config", "init.defaultBranch", "trunk"); err != nil {
		t.Fatalf("git config failed: %v\n%s", err, out)
	}
	// A local 'main' exists too; init.defaultBranch must beat the probe.
	if out, err := runGitTestCommand(t, repo, "branch", "main"); err != nil {
		t.Fatalf("git branch main failed: %v\n%s", err, out)
	}

	branch, err := DefaultBranch(t.Context(), repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "trunk")
}

func TestDefaultBranch_ConfigNamingMissingBranchIsSkipped(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "master")
	// init.defaultBranch names a branch this repo does not have; the chain
	// must fall through to the probe instead of returning a phantom branch.
	if out, err := runGitTestCommand(t, repo, "config", "init.defaultBranch", "phantom"); err != nil {
		t.Fatalf("git config failed: %v\n%s", err, out)
	}

	branch, err := DefaultBranch(t.Context(), repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "master")
}

func TestDefaultBranch_ProbeMain(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "main")
	branch, err := DefaultBranch(t.Context(), repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "main")
}

func TestDefaultBranch_ProbeMaster(t *testing.T) {
	isolateGitConfig(t)

	repo := setupRepoWithInitialBranch(t, "master")
	branch, err := DefaultBranch(t.Context(), repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "master")
}

func TestDefaultBranch_FallsBackToCurrentHEAD(t *testing.T) {
	isolateGitConfig(t)

	// A repo whose only branch is 'develop': no origin, no config, no
	// main/master. Must return the current HEAD branch, never 'main'.
	repo := setupRepoWithInitialBranch(t, "develop")
	branch, err := DefaultBranch(t.Context(), repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	assertStringEqual(t, branch, "develop")
}

func TestDefaultBranch_UnresolvableIsLoudError(t *testing.T) {
	isolateGitConfig(t)

	// Detached HEAD in a develop-only repo: the entire chain fails and the
	// result must be a loud error — never a silent 'main'.
	repo := setupRepoWithInitialBranch(t, "develop")
	if out, err := runGitTestCommand(t, repo, "checkout", "--detach"); err != nil {
		t.Skipf("git checkout --detach failed: %v\n%s", err, out)
	}

	branch, err := DefaultBranch(t.Context(), repo)
	if err == nil {
		t.Fatalf("DefaultBranch = %q, want loud error", branch)
	}
	if branch != "" {
		t.Fatalf("DefaultBranch returned %q alongside error, want empty", branch)
	}
	if !strings.Contains(err.Error(), "cannot determine default branch") {
		t.Fatalf("error %q lacks loud default-branch message", err)
	}
}

func TestSyncWorktree_MergesMasterDefaultBase(t *testing.T) {
	isolateGitConfig(t)

	remote := setupRepoWithInitialBranch(t, "master")
	parent := t.TempDir()
	if out, err := runGitTestCommand(t, parent, "clone", remote, "local"); err != nil {
		t.Skipf("git clone failed: %v\n%s", err, out)
	}
	clone := filepath.Join(parent, "local")

	// Advance the remote's master after the clone so sync has work to do.
	if err := os.WriteFile(filepath.Join(remote, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatalf("write upstream file: %v", err)
	}
	for _, args := range [][]string{
		{"add", "upstream.txt"},
		{"commit", "-m", "upstream change"},
	} {
		if out, err := runGitTestCommand(t, remote, args...); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	ctx := sequentialWorktreeIntegrationContext(t)
	wm, err := NewWorktreeManager(ctx, clone)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}
	// Before the fix this merged the hardcoded origin/main, which does not
	// exist here, so a master-default repo could never sync.
	if err := wm.SyncWorktree(ctx, clone); err != nil {
		t.Fatalf("SyncWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "upstream.txt")); err != nil {
		t.Fatalf("upstream change not merged into clone: %v", err)
	}
}

func TestDefaultBranch_RejectsNilContext(t *testing.T) {
	if _, err := DefaultBranch(nil, t.TempDir()); err == nil { //nolint:staticcheck // intentional nil-context contract check
		t.Fatal("DefaultBranch(nil ctx) succeeded, want error")
	}
}
