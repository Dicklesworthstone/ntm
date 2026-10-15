package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestRemovalRefusesChangedCheckoutOwnership(t *testing.T) {
	for _, state := range []string{"different branch", "detached", "foreign gitdir", "symlink ancestor", "symlink gitfile"} {
		t.Run(state, func(t *testing.T) {
			manager, info := removalFixture(t)
			removalMarker(t, info.Path)
			switch state {
			case "different branch":
				removalGit(t, info.Path, "checkout", "-b", "different-owner")
			case "detached":
				removalGit(t, info.Path, "checkout", "--detach")
			case "foreign gitdir":
				other, _ := removalFixture(t)
				if err := os.WriteFile(filepath.Join(info.Path, ".git"), []byte("gitdir: "+filepath.Join(other.projectPath, ".git")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink ancestor", "symlink gitfile":
				path := filepath.Join(manager.projectPath, ".ntm")
				if state == "symlink gitfile" {
					path = filepath.Join(info.Path, ".git")
				}
				retained := path + "-retained"
				if err := os.Rename(path, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(retained, path); err != nil {
					t.Skipf("symlink fixture unavailable: %v", err)
				}
			}
			if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
				t.Fatal("changed ownership authorized worktree removal")
			}
			assertRemovalPreserved(t, manager, info)
		})
	}
}

func TestRemovalHonorsMergeRecoveryBarrier(t *testing.T) {
	for _, state := range []string{"prepared", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			manager, info := removalFixture(t)
			removalMarker(t, info.Path)
			now := time.Now().UTC()
			record := MergeRecovery{
				Version: 1, AttemptID: strings.Repeat("a", 32), State: "prepared", StartedAt: now, UpdatedAt: now,
				Plan: MergePreview{SourcePath: info.Path, TargetPath: manager.projectPath, SourceBranch: info.BranchName, TargetBranch: "main",
					SourceCommit: removalGit(t, info.Path, "rev-parse", "HEAD"), TargetCommit: removalGit(t, manager.projectPath, "rev-parse", "HEAD"),
					MergeTree: removalGit(t, manager.projectPath, "rev-parse", "HEAD^{tree}")},
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if state == "corrupt" {
				raw = []byte("{broken journal")
			}
			journal := filepath.Join(manager.projectPath, ".git", "ntm-merge.json")
			if err := os.WriteFile(journal, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, run := range []func() error{
				func() error { return manager.RemoveWorktree(context.Background(), info.AgentName) },
				func() error { return manager.RemoveWorktree(context.Background(), "already-absent") },
				func() error { return manager.Cleanup(context.Background()) },
			} {
				if err := run(); !errors.Is(err, ErrMergeRecoveryRequired) {
					t.Fatalf("cleanup bypassed the durable merge barrier: %v", err)
				}
				assertRemovalPreserved(t, manager, info)
			}
			got, err := os.ReadFile(journal)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("cleanup changed recovery evidence: %q %v", got, err)
			}
		})
	}
}

func TestRemovalHonorsLiveMergeOwner(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	// Acquire through the linked checkout, as another session's merge can.
	owner, err := acquireMergeFence(context.Background(), info.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("removal ignored a live owner: %v", err)
	}
	if err := manager.Cleanup(context.Background()); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("batch cleanup ignored a live owner: %v", err)
	}
	assertRemovalPreserved(t, manager, info)
}

func TestRemovalPreservesAdvancedBranch(t *testing.T) {
	manager, info := removalFixture(t)
	tip := removalGit(t, manager.projectPath, "rev-parse", info.BranchName)
	tree := removalGit(t, manager.projectPath, "rev-parse", tip+"^{tree}")
	advanced := removalGit(t, manager.projectPath, "commit-tree", tree, "-p", tip, "-m", "concurrent agent work")
	t.Setenv("NTM_REMOVAL_REF", "refs/heads/"+info.BranchName)
	t.Setenv("NTM_REMOVAL_ADVANCED", advanced)
	removalWrapper(t, `if [ "$1 $2" = "worktree remove" ]; then
  "$real_git" "$@" || exit $?
  exec "$real_git" update-ref "$NTM_REMOVAL_REF" "$NTM_REMOVAL_ADVANCED"
fi`)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Fatal("removal deleted work that arrived after checkout inspection")
	}
	if got := removalGit(t, manager.projectPath, "rev-parse", info.BranchName); got != advanced {
		t.Fatalf("new branch tip was lost: got %s want %s", got, advanced)
	}
}

func TestRemovalVerifiesBranchDeletion(t *testing.T) {
	manager, info := removalFixture(t)
	removalWrapper(t, `if [ "$1 $2" = "branch -D" ]; then exit 0; fi`)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Fatal("false branch-deletion success was acknowledged")
	}
	removalGit(t, manager.projectPath, "show-ref", "--verify", "refs/heads/"+info.BranchName)
}

func TestRemovalRejectsMalformedRegistration(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	removalWrapper(t, `if [ "$1 $2" = "worktree list" ]; then printf 'invalid listing'; exit 0; fi`)
	if err := manager.RemoveWorktree(context.Background(), info.AgentName); err == nil {
		t.Fatal("invalid registration listing authorized deletion")
	}
	assertRemovalPreserved(t, manager, info)
}

func TestRemovalCancelledBeforeMutation(t *testing.T) {
	manager, info := removalFixture(t)
	removalMarker(t, info.Path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.RemoveWorktree(ctx, info.AgentName); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if err := manager.Cleanup(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup lost cancellation: %v", err)
	}
	assertRemovalPreserved(t, manager, info)
}

func TestRemovalRelativeScopeAndSuccessfulBatch(t *testing.T) {
	manager, info := removalFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, manager.projectPath)
	if err != nil {
		t.Fatal(err)
	}
	manager = NewManager(relative, manager.session)
	if err := manager.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.projectPath != relative {
		t.Fatal("cleanup mutated a reusable manager's scope")
	}
	if _, err := os.Lstat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("valid batch cleanup did not remove its checkout: %v", err)
	}
}

func TestRemovalFenceSubprocess(t *testing.T) {
	if os.Getenv("NTM_REMOVAL_CHILD") != "1" {
		return
	}
	manager := NewManager(os.Getenv("NTM_REMOVAL_PROJECT"), "removal-test")
	if err := manager.RemoveWorktree(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalFenceSpansGitAndBranchCleanup(t *testing.T) {
	for _, stage := range []string{"worktree remove", "branch -D"} {
		t.Run(stage, func(t *testing.T) {
			manager, info := removalFixture(t)
			if _, err := manager.CreateForAgent(context.Background(), "other"); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "inside-git")
			release := filepath.Join(t.TempDir(), "release-git")
			t.Setenv("NTM_REMOVAL_STAGE", stage)
			t.Setenv("NTM_REMOVAL_MARKER", marker)
			t.Setenv("NTM_REMOVAL_RELEASE", release)
			removalWrapper(t, `if [ "$1 $2" = "$NTM_REMOVAL_STAGE" ]; then
  printf started > "$NTM_REMOVAL_MARKER"
  while [ ! -f "$NTM_REMOVAL_RELEASE" ]; do sleep 0.02; done
fi`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRemovalFenceSubprocess$")
			cmd.Env = append(os.Environ(), "NTM_REMOVAL_CHILD=1", "NTM_REMOVAL_PROJECT="+manager.projectPath)
			var logs bytes.Buffer
			cmd.Stdout, cmd.Stderr = &logs, &logs
			if err := cmd.Start(); err != nil {
				cancel()
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			joined := false
			t.Cleanup(func() {
				// Release the fixture wrapper even if an assertion fails.
				_ = os.WriteFile(release, []byte("release"), 0o600)
				cancel()
				if !joined {
					<-done
				}
			})
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child never reached the real mutation boundary")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := manager.RemoveWorktree(context.Background(), info.AgentName); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("competing process entered removal at %s: %v", stage, err)
			}
			if err := manager.Cleanup(context.Background()); !errors.Is(err, ErrMergeBusy) {
				t.Fatalf("batch ignored the child owner at %s: %v", stage, err)
			}
			if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				joined = true
				t.Fatalf("child removal failed: %v\n%s", err, logs.String())
			}
			joined = true
			owner, err := acquireMergeFence(context.Background(), manager.projectPath)
			if err != nil {
				t.Fatalf("finished removal retained its OS lock: %v", err)
			}
			owner.close()
		})
	}
}
