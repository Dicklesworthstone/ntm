package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupGitRepo creates a temporary git repo with an initial commit.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
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

func assertStringEqual(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func assertStringNotEqual(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Compare(got, unwanted) == 0 {
		t.Fatalf("got %q, want a distinct value", got)
	}
}

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

func TestCanonicalSessionKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  string
		want2 string
	}{
		{"empty falls back", "", "session", ""},
		{"preserves identity-bearing suffix", "alpha-team-claude-12", "alpha-team-claude-12", ""},
		{"preserves repeated safe separators", "alpha--team-claude-1", "alpha--team-claude-1", ""},
		{"normalizes leading dot for git-ref safety", ".alpha--team-claude-1", "alpha--team-claude-1", ""},
		{"normalizes trailing dot for git-ref safety", "alpha--team-claude-1.", "alpha--team-claude-1", ""},
		{"normalizes disallowed characters", "alpha team/claude:12", "alpha-team-claude-12", ""},
		{"collapses repeated separators", "alpha---team***claude", "alpha-team-claude", ""},
		{"distinct pane IDs remain distinct", "alpha-team-claude-1", "alpha-team-claude-1", "alpha-team-claude-2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalSessionKey(tc.in)
			assertStringEqual(t, got, tc.want)
			if tc.want2 != "" {
				got2 := canonicalSessionKey(tc.want2)
				assertStringNotEqual(t, got, got2)
			}
		})
	}
}

func TestCanonicalAgentKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  string
		want2 string
	}{
		{"empty falls back", "", "agent", ""},
		{"preserves normal agent type", "claude", "claude", ""},
		{"preserves repeated safe separators", "alpha--team", "alpha--team", ""},
		{"normalizes leading/trailing dot for git-ref safety", ".alpha--team.", "alpha--team", ""},
		{"normalizes path separators", "../evil/type", "evil-type", ""},
		{"collapses punctuation", "***codex///agent***", "codex-agent", ""},
		{"distinct safe IDs remain distinct", "alpha--team", "alpha--team", "alpha-team"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalAgentKey(tc.in)
			assertStringEqual(t, got, tc.want)
			if tc.want2 != "" {
				got2 := canonicalAgentKey(tc.want2)
				assertStringNotEqual(t, got, got2)
			}
		})
	}
}

func TestWorktreeManager_ProvisionWorktreeSanitizesAgentName(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	info, err := wm.ProvisionWorktree(context.Background(), "../evil/type", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	assertStringEqual(t, filepath.Base(info.Path), "agent-evil-type-sess-one")
	assertStringEqual(t, info.Branch, "agent/evil-type/sess-one")
	assertStringEqual(t, info.Agent, "evil-type")
}

func TestWorktreeManager_ProvisionWorktreeDistinctSafeAgentKeysDoNotAlias(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	first, err := wm.ProvisionWorktree(context.Background(), "alpha--team", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree first: %v", err)
	}
	second, err := wm.ProvisionWorktree(context.Background(), "alpha-team", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree second: %v", err)
	}

	assertStringEqual(t, filepath.Base(first.Path), "agent-alpha--team-sess-one")
	assertStringEqual(t, filepath.Base(second.Path), "agent-alpha-team-sess-one")
	assertStringNotEqual(t, first.Path, second.Path)
	assertStringNotEqual(t, first.Branch, second.Branch)
	assertStringNotEqual(t, first.Agent, second.Agent)
}

func TestWorktreeManager_ProvisionAndList_PreservesSanitizedHyphenatedAgent(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	info, err := wm.ProvisionWorktree(context.Background(), "../evil/type", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	assertStringEqual(t, info.Agent, "evil-type")

	worktrees, err := wm.ListWorktrees(context.Background())
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	assertStringEqual(t, worktrees[0].Agent, "evil-type")
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

func TestNewWorktreeManager(t *testing.T) {
	t.Parallel()

	t.Run("valid git repo", func(t *testing.T) {
		t.Parallel()
		tmp := setupGitRepo(t)

		wm, err := NewWorktreeManager(tmp)
		if err != nil {
			t.Fatalf("NewWorktreeManager: %v", err)
		}
		if wm.projectDir != tmp {
			t.Errorf("projectDir = %q, want %q", wm.projectDir, tmp)
		}
		if wm.baseRepo != tmp {
			t.Errorf("baseRepo = %q, want %q", wm.baseRepo, tmp)
		}
	})

	t.Run("non-git directory", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()

		_, err := NewWorktreeManager(tmp)
		if err == nil {
			t.Fatal("expected error for non-git directory")
		}
		if !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestWorktreeManager_parseWorktreeList_EmptyInput(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}

	worktrees, err := wm.parseWorktreeList("")
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees for empty input, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_WhitespaceOnly(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}

	worktrees, err := wm.parseWorktreeList("   \n\n  \n  ")
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees for whitespace input, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_NoAgentWorktrees(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}
	output := "worktree /tmp/myproject\nHEAD abc123\nbranch refs/heads/main\n\nworktree /tmp/feature\nHEAD def456\nbranch refs/heads/feature\n"

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 agent worktrees, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_IgnoresAgentTextInParentPath(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}
	output := "worktree /tmp/my-agent-repo/normal\nHEAD abc123\nbranch refs/heads/main\n"

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected parent path containing agent- to be ignored, got %d worktrees", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_ExcludesPrimaryCheckoutOnAgentBranch(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wm := &WorktreeManager{baseRepo: repoPath}
	output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/agent/evil-type/sess-one\n", repoPath)

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected primary checkout path to be excluded, got %d worktrees", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_IncludesAgentBranchWithoutAgentBasename(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	worktreePath := filepath.Join(tmp, "normal-name")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wm := &WorktreeManager{}
	output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/agent/evil-type/sess-one\n", worktreePath)

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected agent branch worktree, got %d worktrees", len(worktrees))
	}
	assertStringEqual(t, worktrees[0].Agent, "evil-type")
}

func TestWorktreeManager_parseWorktreeList_MultipleAgents(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path1 := filepath.Join(tmp, "agent-cc-sess1")
	path2 := filepath.Join(tmp, "agent-cod-sess2")
	path3 := filepath.Join(tmp, "agent-gmi-sess3")
	for _, p := range []string{path1, path2, path3} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	output := fmt.Sprintf(
		"worktree %s\nHEAD aaa111\nbranch refs/heads/agent/cc/sess1\n\n"+
			"worktree %s\nHEAD bbb222\nbranch refs/heads/agent/cod/sess2\n\n"+
			"worktree %s\nHEAD ccc333\nbranch refs/heads/agent/gmi/sess3\n",
		path1, path2, path3,
	)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(worktrees))
	}

	agents := map[string]bool{}
	for _, wt := range worktrees {
		agents[wt.Agent] = true
	}
	for _, expected := range []string{"cc", "cod", "gmi"} {
		if !agents[expected] {
			t.Errorf("expected agent %q in results", expected)
		}
	}
}

func TestWorktreeManager_parseWorktreeList_DetachedHead(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	agentPath := filepath.Join(tmp, "agent-cc-detached")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Detached HEAD has no branch line, instead "HEAD abc123" followed by "detached"
	output := fmt.Sprintf("worktree %s\nHEAD abc123\ndetached\n", agentPath)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	if worktrees[0].Branch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %q", worktrees[0].Branch)
	}
}

func TestWorktreeManager_parseWorktreeList_AgentNameExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		basename  string
		branchRef string
		wantAgent string
	}{
		{"simple fallback", "agent-cc-12345678", "refs/heads/main", "cc"},
		{"codex fallback", "agent-cod-abcdefgh", "refs/heads/main", "cod"},
		{"gemini fallback", "agent-gmi-sessid12", "refs/heads/main", "gmi"},
		{"no dash after agent fallback", "agent-onlyone", "refs/heads/main", "onlyone"},
		{"hyphenated canonical key from branch", "agent-evil-type-sess-one", "refs/heads/agent/evil-type/sess-one", "evil-type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmp := t.TempDir()
			agentPath := filepath.Join(tmp, tt.basename)
			if err := os.MkdirAll(agentPath, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch %s\n", agentPath, tt.branchRef)

			wm := &WorktreeManager{}
			worktrees, err := wm.parseWorktreeList(output)
			if err != nil {
				t.Fatalf("parseWorktreeList: %v", err)
			}
			if len(worktrees) != 1 {
				t.Fatalf("expected 1 worktree, got %d", len(worktrees))
			}
			if worktrees[0].Agent != tt.wantAgent {
				t.Errorf("Agent = %q, want %q", worktrees[0].Agent, tt.wantAgent)
			}
		})
	}
}

func TestWorktreeManager_parseWorktreeList_NonExistentPath(t *testing.T) {
	t.Parallel()

	// Path that contains "agent-" but doesn't exist on disk
	output := "worktree /nonexistent/agent-cc-test1234\nHEAD abc123\nbranch refs/heads/agent/cc/test1234\n"

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	// Should still parse the worktree (os.Stat failure means LastUsed defaults to now)
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	// LastUsed should be approximately now since stat failed
	if time.Since(worktrees[0].LastUsed) > 5*time.Second {
		t.Errorf("LastUsed should be ~now for non-existent path, got %v", worktrees[0].LastUsed)
	}
}

// Integration tests using real git repos

func TestWorktreeManager_ProvisionAndList(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	info, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	if info.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", info.Agent, "cc")
	}
	if !strings.HasPrefix(info.Branch, "agent/cc/") {
		t.Errorf("Branch = %q, want prefix %q", info.Branch, "agent/cc/")
	}
	if info.Commit == "" {
		t.Error("expected non-empty commit hash")
	}
	if info.Path == "" {
		t.Error("expected non-empty path")
	}

	// Verify the path was actually created
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree path does not exist: %v", err)
	}

	// List worktrees
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	if worktrees[0].Agent != "cc" {
		t.Errorf("listed Agent = %q, want %q", worktrees[0].Agent, "cc")
	}
}

func TestWorktreeManager_ProvisionExisting(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// First provision
	info1, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Second provision for same agent/session should return existing
	info2, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree (existing): %v", err)
	}

	if info1.Branch != info2.Branch {
		t.Errorf("expected same branch, got %q vs %q", info1.Branch, info2.Branch)
	}
}

func TestWorktreeManager_RemoveWorktree(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision
	info, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Verify exists
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("worktree path should exist: %v", err)
	}

	// Remove
	if err := wm.RemoveWorktree(ctx, "cc", "session1234"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Verify no agent worktrees remain
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees after remove, got %d", len(worktrees))
	}
}

func TestWorktreeManager_RemoveWorktree_NonExistent(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	// Removing a non-existent worktree should not error
	err = wm.RemoveWorktree(context.Background(), "cc", "nonexist1234")
	if err != nil {
		t.Fatalf("RemoveWorktree (non-existent) should not error: %v", err)
	}
}

func TestWorktreeManager_MultipleProvisions(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision multiple worktrees
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree cc: %v", err)
	}
	_, err = wm.ProvisionWorktree(ctx, "cod", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree cod: %v", err)
	}

	// List should return both
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(worktrees))
	}

	agents := map[string]bool{}
	for _, wt := range worktrees {
		agents[wt.Agent] = true
	}
	if !agents["cc"] || !agents["cod"] {
		t.Errorf("expected agents cc and cod, got %v", agents)
	}
}

func TestWorktreeManager_CleanupStaleWorktrees(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Cleanup with 0 duration should remove it (everything is stale)
	if err := wm.CleanupStaleWorktrees(ctx, 0); err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}

	// Verify cleaned up
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees after cleanup, got %d", len(worktrees))
	}
}

func TestWorktreeManager_CleanupStaleWorktrees_RecentKept(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Cleanup with long maxAge should keep recent worktrees
	if err := wm.CleanupStaleWorktrees(ctx, 24*time.Hour); err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Errorf("expected 1 worktree (recent, kept), got %d", len(worktrees))
	}
}
