package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// setupSessionGitRepo creates a temp git repo with an initial commit and
// a configured remote. Skips if git is not available.
func setupSessionGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "branch", "-m", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
		{"git", "remote", "add", "origin", "https://github.com/test/repo.git"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

// =============================================================================
// getGitInfo — 0% → 100%
// =============================================================================

func TestGetGitInfo_ValidRepo(t *testing.T) {
	t.Parallel()
	repoDir := setupSessionGitRepo(t)

	branch, remote, commit := getGitInfo(repoDir)

	// Should detect branch
	if branch == "" {
		t.Error("expected non-empty branch")
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}

	// Should detect remote
	if remote != "https://github.com/test/repo.git" {
		t.Errorf("remote = %q, want test repo URL", remote)
	}

	// Should detect commit hash
	if commit == "" {
		t.Error("expected non-empty commit hash")
	}
	if len(commit) < 7 {
		t.Errorf("commit = %q, expected at least 7 chars", commit)
	}
}

func TestGetGitInfo_EmptyDir(t *testing.T) {
	t.Parallel()

	branch, remote, commit := getGitInfo("")
	if branch != "" || remote != "" || commit != "" {
		t.Errorf("expected all empty for empty dir, got branch=%q remote=%q commit=%q", branch, remote, commit)
	}
}

func TestGetGitInfo_NonGitDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	branch, remote, commit := getGitInfo(tmp)
	// Should return empty strings gracefully (no panic)
	if branch != "" {
		t.Errorf("branch = %q, want empty for non-git dir", branch)
	}
	if commit != "" {
		t.Errorf("commit = %q, want empty for non-git dir", commit)
	}
	_ = remote // remote may also be empty
}

func TestGetGitInfo_NonExistentDir(t *testing.T) {
	t.Parallel()

	branch, remote, commit := getGitInfo("/tmp/nonexistent-session-test-dir-99999")
	if branch != "" || remote != "" || commit != "" {
		t.Error("expected all empty for nonexistent dir")
	}
}

func TestGetGitInfoWithTimeout_UsesIndependentCommandBudgets(t *testing.T) {
	const perCommandBudget = 150 * time.Millisecond

	tmpBin := t.TempDir()
	logFile := filepath.Join(tmpBin, "git-invocations.log")
	gitPath := filepath.Join(tmpBin, "git")
	script := `#!/bin/sh
sleep 0.07
printf '%s\n' "$3 $4 $5" >> "$NTM_GITINFO_LOG"
case "$3 $4 $5" in
  "rev-parse --abbrev-ref HEAD")
    printf 'main\n'
    ;;
  "remote get-url origin")
    printf 'https://example.com/repo.git\n'
    ;;
  "rev-parse --short HEAD")
    printf 'abcdef0\n'
    ;;
esac
`
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	t.Setenv("PATH", tmpBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_GITINFO_LOG", logFile)

	start := time.Now()
	branch, remote, commit := getGitInfoWithTimeout(t.TempDir(), perCommandBudget)
	if elapsed := time.Since(start); elapsed < perCommandBudget {
		t.Fatalf("total runtime = %v, want > %v to prove commands did not share one timeout budget", elapsed, perCommandBudget)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	if remote != "https://example.com/repo.git" {
		t.Fatalf("remote = %q, want fake remote", remote)
	}
	if commit != "abcdef0" {
		t.Fatalf("commit = %q, want abcdef0", commit)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	if got := string(data); got != "rev-parse --abbrev-ref HEAD\nremote get-url origin\nrev-parse --short HEAD\n" {
		t.Fatalf("unexpected fake git invocations:\n%s", got)
	}
}

// =============================================================================
// getCurrentGitBranch — 0% → 100%
// =============================================================================

func TestGetCurrentGitBranch_ValidRepo(t *testing.T) {
	t.Parallel()
	repoDir := setupSessionGitRepo(t)

	branch := getCurrentGitBranch(repoDir)
	if branch == "" {
		t.Error("expected non-empty branch")
	}
}

func TestGetCurrentGitBranch_NonGitDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	branch := getCurrentGitBranch(tmp)
	if branch != "" {
		t.Errorf("branch = %q, want empty for non-git dir", branch)
	}
}

func TestGetCurrentGitBranch_NonExistentDir(t *testing.T) {
	t.Parallel()

	branch := getCurrentGitBranch("/tmp/nonexistent-session-test-dir-88888")
	if branch != "" {
		t.Errorf("branch = %q, want empty", branch)
	}
}

// =============================================================================
// detectWorkDir — 0% partial (only non-tmux fallback paths)
// Without tmux, we can't test the tmux path, but we can test the fallbacks.
// =============================================================================

func TestDetectWorkDir_NoPanes(t *testing.T) {
	// Without tmux running, the fallback should be os.Getwd()
	result := detectWorkDir("nonexistent-session", nil)
	cwd, _ := os.Getwd()
	if result != cwd {
		home, _ := os.UserHomeDir()
		// Could be cwd or home dir depending on environment
		if result != home && result != "" {
			t.Errorf("detectWorkDir = %q, expected cwd (%q) or home (%q)", result, cwd, home)
		}
	}
}

// =============================================================================
// shouldCreateDir — edge cases (80% → 100%)
// =============================================================================

func TestShouldCreateDir_ShallowPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	// One level deep should not be created
	if shouldCreateDir(home + "/project") {
		t.Error("should not create dir one level from home")
	}

	// Two levels deep should be ok
	if !shouldCreateDir(home + "/Dev/project") {
		t.Error("should create dir two levels from home")
	}
}

func TestShouldCreateDir_OutsideHome(t *testing.T) {
	t.Parallel()

	// Path outside home should not be created
	if shouldCreateDir("/tmp/some/project") {
		t.Error("should not create dir outside home")
	}
}

func TestShouldCreateDir_ExactHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	if shouldCreateDir(home) {
		t.Error("should not create home dir itself")
	}
}
