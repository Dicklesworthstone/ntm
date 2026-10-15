package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fenceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fenceFixture(t *testing.T) *MergePreview {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	target, source := filepath.Join(root, "target"), filepath.Join(root, "agent")
	fenceGit(t, root, "init", "-b", "main", target)
	fenceGit(t, target, "config", "user.name", "NTM Test")
	fenceGit(t, target, "config", "user.email", "ntm@example.invalid")
	fenceGit(t, target, "commit", "--allow-empty", "-m", "base")
	branch := "ntm/fenced/agent"
	fenceGit(t, target, "worktree", "add", "-b", branch, source)
	if err := os.WriteFile(filepath.Join(source, "delivered.txt"), []byte("agent work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fenceGit(t, source, "add", "delivered.txt")
	fenceGit(t, source, "commit", "-m", "agent work")
	plan := &MergePreview{AgentName: "agent", SessionID: "fenced", TargetPath: target, SourcePath: source, TargetBranch: "main", SourceBranch: branch, CanMerge: true}
	plan.SourceCommit = fenceGit(t, source, "rev-parse", "HEAD")
	plan.TargetCommit = fenceGit(t, target, "rev-parse", "HEAD")
	plan.MergeTree = fenceGit(t, target, "merge-tree", "--write-tree", plan.TargetCommit, plan.SourceCommit)
	return plan
}

func awaitFenceMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subprocess never signaled %s", path)
}

// Self-execution exercises actual OS ownership in an independent process.
func TestMergeFenceChild(t *testing.T) {
	mode := os.Getenv("NTM_MERGE_FENCE_CHILD")
	if mode == "" {
		return
	}
	path := os.Getenv("NTM_MERGE_FENCE_PLAN")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plan MergePreview
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if mode == "apply" {
		if err := applyMergePreview(context.Background(), &plan); err != nil {
			t.Fatal(err)
		}
		return
	}
	fence, err := acquireMergeFence(context.Background(), plan.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.close()
	if mode == "prepare" {
		if _, err := fence.prepareMerge(&plan); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path+".ready", []byte("locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func startFenceChild(t *testing.T, plan *MergePreview, mode string) (*exec.Cmd, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMergeFenceChild$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "NTM_MERGE_FENCE_CHILD="+mode, "NTM_MERGE_FENCE_PLAN="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, path
}

func TestMergeFenceSharedAcrossLinkedWorktreesAndReleasedOnExit(t *testing.T) {
	plan := fenceFixture(t)
	child, path := startFenceChild(t, plan, "hold")
	awaitFenceMarker(t, path+".ready")
	for _, dir := range []string{plan.TargetPath, plan.SourcePath} {
		fence, err := acquireMergeFence(context.Background(), dir)
		if fence != nil || !errors.Is(err, ErrMergeBusy) {
			if fence != nil {
				fence.close()
			}
			t.Fatalf("linked repository did not share ownership: %v", err)
		}
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	fence, err := acquireMergeFence(context.Background(), plan.SourcePath)
	if err != nil {
		t.Fatalf("dead owner retained kernel lock: %v", err)
	}
	fence.close()
	fence.close()
	if _, err := os.Stat(filepath.Join(plan.TargetPath, ".git", "ntm-merge.lock")); err != nil {
		t.Fatalf("release must retain the rendezvous inode: %v", err)
	}
}

func TestMergeFenceApplicationOwnsChecksThroughHookAndPostconditions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Git hook")
	}
	plan := fenceFixture(t)
	control := t.TempDir()
	started, release := filepath.Join(control, "started"), filepath.Join(control, "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	hook := fmt.Sprintf("#!/bin/sh\nprintf started > %s\nwhile ! test -f %s; do sleep 0.02; done\n", quote(started), quote(release))
	if err := os.WriteFile(filepath.Join(plan.TargetPath, ".git", "hooks", "pre-merge-commit"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	child, _ := startFenceChild(t, plan, "apply")
	awaitFenceMarker(t, started)
	if err := applyMergePreview(context.Background(), plan); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("concurrent application entered the owned repository: %v", err)
	}
	if got := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD"); got != plan.TargetCommit {
		t.Fatalf("competitor advanced HEAD during the held hook: %s", got)
	}
	if err := os.WriteFile(release, []byte("finish"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("owned merge failed: %v", err)
	}
	merged := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD")
	if err := applyMergePreview(context.Background(), plan); !errors.Is(err, ErrMergeSnapshotChanged) {
		t.Fatalf("old preview was not revalidated after ownership changed: %v", err)
	}
	if got := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD"); got != merged {
		t.Fatal("stale second application changed the merge")
	}
}

func TestMergeFenceCancellationAndIndependentRepositories(t *testing.T) {
	plan := fenceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireMergeFence(ctx, plan.TargetPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.TargetPath, ".git", "ntm-merge.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled acquisition created ownership state: %v", err)
	}
	if _, err := acquireMergeFence(nil, plan.TargetPath); err == nil {
		t.Fatal("nil context accepted")
	}
	first, err := acquireMergeFence(context.Background(), plan.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	other := fenceFixture(t)
	second, err := acquireMergeFence(context.Background(), other.TargetPath)
	if err != nil {
		t.Fatalf("unrelated repositories share a global lock: %v", err)
	}
	second.close()
}

func TestMergeFenceRefusesSubstitutedControlFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	plan := fenceFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(plan.TargetPath, ".git", "ntm-merge.lock")); err != nil {
		t.Fatal(err)
	}
	if err := applyMergePreview(context.Background(), plan); err == nil {
		t.Fatal("application followed a substituted lock file")
	}
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != "untouched" {
		t.Fatalf("external data changed: %q %v", raw, err)
	}
	if got := fenceGit(t, plan.TargetPath, "rev-parse", "HEAD"); got != plan.TargetCommit {
		t.Fatal("refused ownership still merged")
	}
}
