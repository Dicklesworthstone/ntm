package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestIsGitRepository(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	if IsGitRepository(tmp) {
		t.Fatal("expected temp dir to not be a git repo")
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = tmp
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed, skipping test: %v", err)
	}

	if !IsGitRepository(tmp) {
		t.Fatal("expected directory to be detected as git repo after init")
	}
}

func TestWorktreeManager_worktreeExists(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	wm := &WorktreeManager{baseRepo: tmp}

	worktreePath := filepath.Join(tmp, ".git", "worktrees", "agent-cc-123")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree path: %v", err)
	}

	exists, err := wm.worktreeExists("agent-cc-123")
	if err != nil {
		t.Fatalf("worktreeExists error: %v", err)
	}
	if !exists {
		t.Fatal("expected worktree to exist")
	}

	exists, err = wm.worktreeExists("missing")
	if err != nil {
		t.Fatalf("worktreeExists error: %v", err)
	}
	if exists {
		t.Fatal("expected missing worktree to return false")
	}
}

func TestWorktreeManager_parseWorktreeList(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	agentPath := filepath.Join(tmp, "agent-cc-123")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatalf("mkdir agent path: %v", err)
	}
	otherPath := filepath.Join(tmp, "normal")
	if err := os.MkdirAll(otherPath, 0o755); err != nil {
		t.Fatalf("mkdir other path: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(agentPath, modTime, modTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	output := fmt.Sprintf(
		"worktree %s\nHEAD abcdef\nbranch refs/heads/agent/cc/abc123\n\nworktree %s\nHEAD 111111\nbranch refs/heads/main\n",
		agentPath,
		otherPath,
	)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList error: %v", err)
	}

	if len(worktrees) != 1 {
		t.Fatalf("expected 1 agent worktree, got %d", len(worktrees))
	}

	wt := worktrees[0]
	if wt.Path != agentPath {
		t.Errorf("Path = %q, want %q", wt.Path, agentPath)
	}
	if wt.Branch != "agent/cc/abc123" {
		t.Errorf("Branch = %q, want %q", wt.Branch, "agent/cc/abc123")
	}
	if wt.Commit != "abcdef" {
		t.Errorf("Commit = %q, want %q", wt.Commit, "abcdef")
	}
	if wt.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", wt.Agent, "cc")
	}
	if diff := wt.LastUsed.Sub(modTime); diff > time.Second || diff < -time.Second {
		t.Errorf("LastUsed = %v, want ~%v (diff %v)", wt.LastUsed, modTime, diff)
	}
}
