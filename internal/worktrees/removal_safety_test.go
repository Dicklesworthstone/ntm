package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func removalGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func removalFixture(t *testing.T) (*WorktreeManager, *WorktreeInfo) {
	t.Helper()
	repo := t.TempDir()
	removalGit(t, repo, "init", "-b", "main")
	removalGit(t, repo, "config", "user.name", "Removal Test")
	removalGit(t, repo, "config", "user.email", "removal@example.invalid")
	removalGit(t, repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	manager := NewManager(repo, "removal-test")
	info, err := manager.CreateForAgent(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	return manager, info
}

func removalMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, "valuable-work"), []byte("must survive refusal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRemovalPreserved(t *testing.T, manager *WorktreeManager, info *WorktreeInfo) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(info.Path, "valuable-work"))
	if err != nil || string(data) != "must survive refusal\n" {
		t.Fatalf("removal refusal lost the checkout: data=%q err=%v", data, err)
	}
	removalGit(t, manager.projectPath, "show-ref", "--verify", "refs/heads/"+info.BranchName)
}

// Git's single --force deliberately does not override a worktree lock. NTM
// must not reinterpret that refusal as permission for recursive deletion.
func TestRemovalPreservesLockedWorktree(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	removalGit(t, manager.projectPath, "worktree", "lock", "--reason", "unfinished agent work", info.Path)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Error("locked worktree removal reported success")
	}
	assertRemovalPreserved(t, manager, info)
	if listed := removalGit(t, manager.projectPath, "worktree", "list", "--porcelain"); !strings.Contains(listed, "locked unfinished agent work") {
		t.Fatalf("Git's locked registration was pruned: %s", listed)
	}
}

func TestCleanupPreservesLockedWorktree(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	removalGit(t, manager.projectPath, "worktree", "lock", info.Path)
	if err := manager.Cleanup(context.Background()); err == nil {
		t.Error("cleanup reported success despite a locked worktree")
	}
	assertRemovalPreserved(t, manager, info)
}

func removalWrapper(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fault-injection wrapper requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nreal_git=" + quote(realGit) + "\n" + body + "\nexec \"$real_git\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRemovalFailureAfterGitMutationRetainsBranch(t *testing.T) {
	manager, info := removalFixture(t)
	removalWrapper(t, `if [ "$1 $2" = "worktree remove" ]; then
  "$real_git" "$@" || exit $?
  echo 'wrapper failed after removing checkout' >&2
  exit 73
fi`)
	err := manager.RemoveWorktree(context.Background(), info.AgentName)
	if err == nil {
		t.Fatal("failed wrapper was acknowledged as successful removal")
	}
	if _, err := os.Lstat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("fixture did not reach the post-removal failure: %v", err)
	}
	removalGit(t, manager.projectPath, "show-ref", "--verify", "refs/heads/"+info.BranchName)
}

func TestRemovalMissingCheckoutDoesNotAuthorizeBranchDeletion(t *testing.T) {
	manager, info := removalFixture(t)
	// Git removes only the checkout, leaving its branch to represent work
	// whose ownership can no longer be established by the expected path.
	removalGit(t, manager.projectPath, "worktree", "remove", info.Path)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Fatal("missing checkout silently authorized branch deletion")
	}
	removalGit(t, manager.projectPath, "show-ref", "--verify", "refs/heads/"+info.BranchName)
}

func TestRemovalChecksSuccessfulGitPostcondition(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	log := filepath.Join(t.TempDir(), "branch-delete")
	t.Setenv("NTM_REMOVAL_BRANCH_LOG", log)
	removalWrapper(t, `if [ "$1 $2" = "worktree remove" ]; then exit 0; fi
if [ "$1 $2" = "branch -D" ]; then
  printf attempted > "$NTM_REMOVAL_BRANCH_LOG"
fi`)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Fatal("successful wrapper hid the surviving checkout")
	}
	if _, err := os.Lstat(log); !os.IsNotExist(err) {
		t.Fatalf("branch deletion was attempted before verifying checkout removal: %v", err)
	}
	assertRemovalPreserved(t, manager, info)
}

func TestRemovalSuccessfulRetryIsNoOp(t *testing.T) {
	manager, info := removalFixture(t)
	for i := 0; i < 2; i++ {
		if err := manager.RemoveWorktree(context.Background(), info.AgentName); err != nil {
			t.Fatalf("removal attempt %d: %v", i+1, err)
		}
	}
	if _, err := os.Lstat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("removed checkout remains: %v", err)
	}
}
