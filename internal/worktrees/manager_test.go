package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	t.Parallel()
	manager := NewManager("/tmp/test", "test-session")
	if manager.projectPath != "/tmp/test" {
		t.Errorf("Expected project path /tmp/test, got %s", manager.projectPath)
	}
	if manager.session != "test-session" {
		t.Errorf("Expected session test-session, got %s", manager.session)
	}
}

func TestWorktreeInfo(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	// Test GetWorktreeForAgent with non-existent worktree
	info, err := manager.GetWorktreeForAgent(t.Context(), "test-agent")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if info.Created {
		t.Error("Expected Created to be false for non-existent worktree")
	}
	if info.Error == "" {
		t.Error("Expected error message for non-existent worktree")
	}

	expectedPath := manager.worktreePath("test-agent")
	if info.Path != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, info.Path)
	}

	expectedBranch := "ntm/test-session/test-agent"
	if info.BranchName != expectedBranch {
		t.Errorf("Expected branch %s, got %s", expectedBranch, info.BranchName)
	}
}

func TestCreateForAgent_ExistingWorktreeSkipsGit(t *testing.T) {
	t.Parallel()
	projectDir := setupWorktreeGitRepo(t)
	manager := NewManager(projectDir, "test-session")

	created, err := manager.CreateForAgent(t.Context(), "agent-1")
	if err != nil {
		t.Fatalf("failed to create initial worktree: %v", err)
	}
	if !created.Created {
		t.Fatal("expected initial worktree creation to report Created=true")
	}

	info, err := manager.CreateForAgent(t.Context(), "agent-1")
	if err != nil {
		t.Fatalf("unexpected error for existing valid worktree: %v", err)
	}
	if info.Created {
		t.Error("expected Created=false when worktree already exists")
	}
	if info.Error != "" {
		t.Errorf("expected empty error for existing worktree, got %q", info.Error)
	}

	expectedPath := manager.worktreePath("agent-1")
	if info.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, info.Path)
	}

	expectedBranch := "ntm/test-session/agent-1"
	if info.BranchName != expectedBranch {
		t.Errorf("expected branch %s, got %s", expectedBranch, info.BranchName)
	}
}

func TestCreateForAgent_ExistingInvalidDirectoryReturnsError(t *testing.T) {
	t.Parallel()

	projectDir := setupWorktreeGitRepo(t)
	manager := NewManager(projectDir, "test-session")

	worktreePath := manager.worktreePath("agent-1")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("failed to create stale worktree dir: %v", err)
	}

	info, err := manager.CreateForAgent(t.Context(), "agent-1")
	if err == nil {
		t.Fatal("expected error for stale pre-existing directory")
	}
	if info == nil || info.Error != "invalid or stale worktree" {
		t.Fatalf("expected stale worktree error, got info=%+v err=%v", info, err)
	}
}

func TestPreflightForAgentRejectsUnsafeAndConflictingTargets(t *testing.T) {
	t.Run("unsafe agent name", func(t *testing.T) {
		manager := NewManager(setupWorktreeGitRepo(t), "test-session")
		err := manager.PreflightForAgent(t.Context(), "../escaped")
		if err == nil || !strings.Contains(err.Error(), "path separators") {
			t.Fatalf("PreflightForAgent() error = %v", err)
		}
	})

	t.Run("stale target directory", func(t *testing.T) {
		manager := NewManager(setupWorktreeGitRepo(t), "test-session")
		if err := os.MkdirAll(manager.worktreePath("agent-1"), 0o755); err != nil {
			t.Fatalf("create stale worktree path: %v", err)
		}
		err := manager.PreflightForAgent(t.Context(), "agent-1")
		if err == nil || !strings.Contains(err.Error(), "not a valid git worktree") {
			t.Fatalf("PreflightForAgent() error = %v", err)
		}
	})

	t.Run("target path is a file", func(t *testing.T) {
		manager := NewManager(setupWorktreeGitRepo(t), "test-session")
		path := manager.worktreePath("agent-1")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create worktree parent: %v", err)
		}
		if err := os.WriteFile(path, []byte("collision\n"), 0o600); err != nil {
			t.Fatalf("create worktree path file: %v", err)
		}
		err := manager.PreflightForAgent(t.Context(), "agent-1")
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("PreflightForAgent() error = %v", err)
		}
	})

	t.Run("branch exists without target path", func(t *testing.T) {
		repo := setupWorktreeGitRepo(t)
		manager := NewManager(repo, "test-session")
		cmd := exec.Command("git", "branch", "ntm/test-session/agent-1")
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create conflicting branch: %v: %s", err, output)
		}
		err := manager.PreflightForAgent(t.Context(), "agent-1")
		if err == nil || !strings.Contains(err.Error(), "already exists without its expected worktree path") {
			t.Fatalf("PreflightForAgent() error = %v", err)
		}
	})

	t.Run("existing worktree on different branch", func(t *testing.T) {
		repo := setupWorktreeGitRepo(t)
		manager := NewManager(repo, "test-session")
		path := manager.worktreePath("agent-1")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create worktree parent: %v", err)
		}
		cmd := exec.Command("git", "worktree", "add", "-b", "different-branch", path)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create conflicting worktree: %v: %s", err, output)
		}
		err := manager.PreflightForAgent(t.Context(), "agent-1")
		if err == nil || !strings.Contains(err.Error(), "would collide") {
			t.Fatalf("PreflightForAgent() error = %v", err)
		}
		info, err := manager.CreateForAgent(t.Context(), "agent-1")
		if err == nil || info == nil || !strings.Contains(info.Error, "would collide") {
			t.Fatalf("CreateForAgent() info=%+v error=%v, want branch collision", info, err)
		}
		info, err = manager.GetWorktreeForAgent(t.Context(), "agent-1")
		if err != nil || info == nil || !strings.Contains(info.Error, "expected") {
			t.Fatalf("GetWorktreeForAgent() info=%+v error=%v, want wrong-branch rejection", info, err)
		}
	})

	t.Run("existing worktree with detached head", func(t *testing.T) {
		repo := setupWorktreeGitRepo(t)
		manager := NewManager(repo, "test-session")
		info, err := manager.CreateForAgent(t.Context(), "agent-1")
		if err != nil {
			t.Fatalf("CreateForAgent() initial error = %v", err)
		}
		cmd := exec.CommandContext(t.Context(), "git", "checkout", "--detach", "--quiet")
		cmd.Dir = info.Path
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("detach worktree HEAD: %v: %s", err, output)
		}
		if err := manager.PreflightForAgent(t.Context(), "agent-1"); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
			t.Fatalf("PreflightForAgent() detached error = %v", err)
		}
		info, err = manager.CreateForAgent(t.Context(), "agent-1")
		if err == nil || info == nil || !strings.Contains(info.Error, "detached HEAD") {
			t.Fatalf("CreateForAgent() info=%+v error=%v, want detached rejection", info, err)
		}
		info, err = manager.GetWorktreeForAgent(t.Context(), "agent-1")
		if err != nil || info == nil || !strings.Contains(info.Error, "detached HEAD") {
			t.Fatalf("GetWorktreeForAgent() info=%+v error=%v, want detached rejection", info, err)
		}
	})
}

func TestWorktreeManagerContextCancellation(t *testing.T) {
	t.Run("nil contexts are rejected", func(t *testing.T) {
		manager := NewManager(t.TempDir(), "test-session")
		if err := manager.PreflightForAgent(nil, "agent-1"); err == nil {
			t.Fatal("PreflightForAgent(nil) succeeded")
		}
		if _, err := manager.CreateForAgent(nil, "agent-1"); err == nil {
			t.Fatal("CreateForAgent(nil) succeeded")
		}
		if _, err := manager.GetWorktreeForAgent(nil, "agent-1"); err == nil {
			t.Fatal("GetWorktreeForAgent(nil) succeeded")
		}
		if _, err := manager.ListWorktrees(nil); err == nil {
			t.Fatal("ListWorktrees(nil) succeeded")
		}
		if err := manager.Cleanup(nil); err == nil {
			t.Fatal("Cleanup(nil) succeeded")
		}
		if err := manager.MergeBack(nil, "agent-1"); err == nil {
			t.Fatal("MergeBack(nil) succeeded")
		}
		if err := manager.RemoveWorktree(nil, "agent-1"); err == nil {
			t.Fatal("RemoveWorktree(nil) succeeded")
		}
	})

	t.Run("merge preserves cancellation during git", func(t *testing.T) {
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		err := NewManager(t.TempDir(), "test-session").MergeBack(ctx, "agent-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("MergeBack() error = %v, want context deadline", err)
		}
	})

	t.Run("remove cancellation does not fall back to filesystem deletion", func(t *testing.T) {
		projectDir := t.TempDir()
		manager := NewManager(projectDir, "test-session")
		worktreePath := manager.worktreePath("agent-1")
		if err := os.MkdirAll(worktreePath, 0o755); err != nil {
			t.Fatalf("create worktree fixture: %v", err)
		}
		marker := filepath.Join(worktreePath, "must-remain")
		if err := os.WriteFile(marker, []byte("retained\n"), 0o600); err != nil {
			t.Fatalf("write retained marker: %v", err)
		}
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		err := manager.RemoveWorktree(ctx, "agent-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RemoveWorktree() error = %v, want context deadline", err)
		}
		if _, statErr := os.Stat(marker); statErr != nil {
			t.Fatalf("canceled removal deleted retained marker: %v", statErr)
		}
	})

	t.Run("listing rejects an already canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := NewManager(t.TempDir(), "test-session").ListWorktrees(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListWorktrees() error = %v, want context.Canceled", err)
		}
	})

	t.Run("preflight preserves cancellation during git", func(t *testing.T) {
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := NewManager(t.TempDir(), "test-session").PreflightForAgent(ctx, "agent-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("PreflightForAgent() error = %v, want context deadline", err)
		}
	})

	t.Run("creation preserves cancellation during provisioning", func(t *testing.T) {
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		manager := NewManager(t.TempDir(), "test-session")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		info, err := manager.CreateForAgent(ctx, "agent-1")
		if !errors.Is(err, context.DeadlineExceeded) || info == nil || info.Created {
			t.Fatalf("CreateForAgent() info=%+v error=%v, want canceled uncreated worktree", info, err)
		}
		if _, statErr := os.Stat(manager.worktreePath("agent-1")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("canceled creation left worktree path: %v", statErr)
		}
	})

	t.Run("creation reports checkout retained before cancellation", func(t *testing.T) {
		repo := setupWorktreeGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		fakeBin := t.TempDir()
		started := filepath.Join(t.TempDir(), "worktree-added")
		wrapper := `#!/bin/sh
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$NTM_TEST_REAL_GIT" "$@" || exit $?
  : > "$NTM_TEST_WORKTREE_STARTED"
  exec sleep 10
fi
exec "$NTM_TEST_REAL_GIT" "$@"
`
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write post-provision blocking git: %v", err)
		}
		t.Setenv("NTM_TEST_REAL_GIT", realGit)
		t.Setenv("NTM_TEST_WORKTREE_STARTED", started)
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		manager := NewManager(repo, "test-session")
		ctx, cancel := context.WithCancel(context.Background())
		type creationResult struct {
			info *WorktreeInfo
			err  error
		}
		resultCh := make(chan creationResult, 1)
		go func() {
			info, createErr := manager.CreateForAgent(ctx, "agent-1")
			resultCh <- creationResult{info: info, err: createErr}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("wrapper did not complete real worktree add before timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		var result creationResult
		select {
		case result = <-resultCh:
		case <-time.After(5 * time.Second):
			t.Fatal("CreateForAgent did not return after cancellation")
		}
		if !errors.Is(result.err, context.Canceled) || result.info == nil || !result.info.Created {
			t.Fatalf("CreateForAgent() info=%+v error=%v, want canceled retained worktree", result.info, result.err)
		}
		validated, err := manager.GetWorktreeForAgent(t.Context(), "agent-1")
		if err != nil || validated == nil || !validated.Created || validated.Error != "" {
			t.Fatalf("retained worktree validation info=%+v error=%v", validated, err)
		}
	})

	t.Run("lookup preserves cancellation during git", func(t *testing.T) {
		repo := setupWorktreeGitRepo(t)
		manager := NewManager(repo, "test-session")
		if _, err := manager.CreateForAgent(t.Context(), "agent-1"); err != nil {
			t.Fatalf("CreateForAgent() error = %v", err)
		}
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
			t.Fatalf("write blocking git: %v", err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := manager.GetWorktreeForAgent(ctx, "agent-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetWorktreeForAgent() error = %v, want context deadline", err)
		}
	})
}

func TestPreflightForAgentAcceptsMissingAndMatchingTargets(t *testing.T) {
	repo := setupWorktreeGitRepo(t)
	manager := NewManager(repo, "test-session")
	if err := manager.PreflightForAgent(t.Context(), "agent-1"); err != nil {
		t.Fatalf("PreflightForAgent() missing target error = %v", err)
	}
	if _, err := manager.CreateForAgent(t.Context(), "agent-1"); err != nil {
		t.Fatalf("CreateForAgent() error = %v", err)
	}
	if err := manager.PreflightForAgent(t.Context(), "agent-1"); err != nil {
		t.Fatalf("PreflightForAgent() matching target error = %v", err)
	}
}

func TestCreateForAgent_MkdirAllFailure(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	// Create a file where the worktrees directory should be to force MkdirAll failure.
	worktreesPath := manager.sessionRoot()
	if err := os.MkdirAll(filepath.Dir(worktreesPath), 0755); err != nil {
		t.Fatalf("failed to create .ntm dir: %v", err)
	}
	if err := os.WriteFile(worktreesPath, []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("failed to create worktrees file: %v", err)
	}

	info, err := manager.CreateForAgent(t.Context(), "agent-2")
	if err == nil {
		t.Fatal("expected error when worktrees path is a file, got nil")
	}
	if info.Error == "" {
		t.Fatal("expected error message on worktree creation failure")
	}
}

func TestCreateForAgent_RejectsUnsafeAgentName(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")
	escapedPath := filepath.Join(projectDir, ".ntm", "escaped")

	info, err := manager.CreateForAgent(t.Context(), "../escaped")
	if err == nil {
		t.Fatal("expected error for unsafe agent name")
	}
	if info == nil || !strings.Contains(info.Error, "path separators") {
		t.Fatalf("expected path separator error, got info=%+v err=%v", info, err)
	}
	if _, statErr := os.Stat(escapedPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected escaped path to remain absent, stat err: %v", statErr)
	}
}

func TestListWorktrees_MissingDirReturnsEmpty(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktrees, err := manager.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected empty worktree list, got %d", len(worktrees))
	}
}

func TestListWorktrees_EmptyDirReturnsEmpty(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreesDir := manager.sessionRoot()
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("failed to create worktrees dir: %v", err)
	}

	worktrees, err := manager.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected empty worktree list, got %d", len(worktrees))
	}
}

func TestListWorktreesSurfacesGitValidationFailure(t *testing.T) {
	fakeBin := t.TempDir()
	script := "#!/bin/sh\necho 'git unavailable' >&2\nexit 127\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := NewManager(t.TempDir(), "test-session")
	worktreePath := manager.worktreePath("agent-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create worktree directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte("gitdir: fake\n"), 0o600); err != nil {
		t.Fatalf("write worktree git file: %v", err)
	}

	worktrees, err := manager.ListWorktrees(t.Context())
	if err == nil || !strings.Contains(err.Error(), "inspect worktree registration") {
		t.Fatalf("ListWorktrees() error = %v, want git validation failure", err)
	}
	if worktrees != nil {
		t.Fatalf("ListWorktrees() worktrees = %+v, want nil on validation failure", worktrees)
	}
}

func TestCleanup_RemovesEmptyWorktreesDir(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreesDir := manager.worktreesRoot()
	if err := os.MkdirAll(manager.sessionRoot(), 0755); err != nil {
		t.Fatalf("failed to create worktrees dir: %v", err)
	}

	if err := manager.Cleanup(t.Context()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(worktreesDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktrees dir to be removed, stat err: %v", err)
	}
}

// setupWorktreeGitRepo creates a temp git repo with an initial commit for worktree tests.
func setupWorktreeGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

func TestListWorktrees_WithDirectoriesAndFiles(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreesDir := manager.sessionRoot()

	// Create agent directories
	for _, name := range []string{"cc-1", "cod-2", "gmi-3"} {
		if err := os.MkdirAll(filepath.Join(worktreesDir, name), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// Create a file (should be skipped)
	if err := os.WriteFile(filepath.Join(worktreesDir, "not-a-dir.txt"), []byte("skip"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	worktrees, err := manager.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}

	// Should have 3 entries (files skipped)
	if len(worktrees) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(worktrees))
	}

	names := map[string]bool{}
	for _, wt := range worktrees {
		names[wt.AgentName] = true
		// Verify branch name format
		expectedBranch := "ntm/test-session/" + wt.AgentName
		if wt.BranchName != expectedBranch {
			t.Errorf("BranchName = %q, want %q", wt.BranchName, expectedBranch)
		}
		if wt.SessionID != "test-session" {
			t.Errorf("SessionID = %q, want test-session", wt.SessionID)
		}
		// isValidWorktree should fail (no .git file), so Error should be set
		if wt.Error == "" {
			t.Errorf("expected error for invalid worktree %s", wt.AgentName)
		}
	}

	for _, expected := range []string{"cc-1", "cod-2", "gmi-3"} {
		if !names[expected] {
			t.Errorf("expected agent %q in results", expected)
		}
	}
}

func TestGetWorktreeForAgent_ExistingWorktree(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess-123")

	// Create the worktree directory
	worktreePath := manager.worktreePath("agent-cc")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info, err := manager.GetWorktreeForAgent(t.Context(), "agent-cc")
	if err != nil {
		t.Fatalf("GetWorktreeForAgent: %v", err)
	}

	if !info.Created {
		t.Error("expected Created=true for existing worktree dir")
	}
	if info.AgentName != "agent-cc" {
		t.Errorf("AgentName = %q, want agent-cc", info.AgentName)
	}
	if info.BranchName != "ntm/sess-123/agent-cc" {
		t.Errorf("BranchName = %q, want ntm/sess-123/agent-cc", info.BranchName)
	}
	if info.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want sess-123", info.SessionID)
	}
	if info.Path != worktreePath {
		t.Errorf("Path = %q, want %q", info.Path, worktreePath)
	}
	// isValidWorktree should report invalid (no .git file)
	if info.Error == "" {
		t.Error("expected error for invalid worktree (no .git file)")
	}
}

func TestGetWorktreeForAgent_MultipleSessions(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()

	m1 := NewManager(projectDir, "session-alpha")
	m2 := NewManager(projectDir, "session-beta")

	info1, _ := m1.GetWorktreeForAgent(t.Context(), "agent-1")
	info2, _ := m2.GetWorktreeForAgent(t.Context(), "agent-1")

	if info1.BranchName == info2.BranchName {
		t.Error("expected different branch names for different sessions")
	}
	if info1.BranchName != "ntm/session-alpha/agent-1" {
		t.Errorf("session-alpha branch = %q", info1.BranchName)
	}
	if info2.BranchName != "ntm/session-beta/agent-1" {
		t.Errorf("session-beta branch = %q", info2.BranchName)
	}
	if info1.Path == info2.Path {
		t.Errorf("expected different paths for different sessions, got %q", info1.Path)
	}
	if info1.Path != m1.worktreePath("agent-1") {
		t.Errorf("session-alpha path = %q", info1.Path)
	}
	if info2.Path != m2.worktreePath("agent-1") {
		t.Errorf("session-beta path = %q", info2.Path)
	}
}

func TestListWorktrees_IsolatedBySession(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	alpha := NewManager(projectDir, "session-alpha")
	beta := NewManager(projectDir, "session-beta")

	if err := os.MkdirAll(alpha.worktreePath("agent-1"), 0755); err != nil {
		t.Fatalf("mkdir alpha worktree: %v", err)
	}
	if err := os.MkdirAll(beta.worktreePath("agent-1"), 0755); err != nil {
		t.Fatalf("mkdir beta worktree: %v", err)
	}

	alphaWorktrees, err := alpha.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("alpha ListWorktrees: %v", err)
	}
	if len(alphaWorktrees) != 1 {
		t.Fatalf("expected 1 alpha worktree, got %d", len(alphaWorktrees))
	}
	if alphaWorktrees[0].Path != alpha.worktreePath("agent-1") {
		t.Fatalf("alpha path = %q, want %q", alphaWorktrees[0].Path, alpha.worktreePath("agent-1"))
	}

	betaWorktrees, err := beta.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("beta ListWorktrees: %v", err)
	}
	if len(betaWorktrees) != 1 {
		t.Fatalf("expected 1 beta worktree, got %d", len(betaWorktrees))
	}
	if betaWorktrees[0].Path != beta.worktreePath("agent-1") {
		t.Fatalf("beta path = %q, want %q", betaWorktrees[0].Path, beta.worktreePath("agent-1"))
	}
}

func TestCleanup_DoesNotRemoveOtherSessionWorktrees(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	alpha := NewManager(projectDir, "session-alpha")
	beta := NewManager(projectDir, "session-beta")

	if err := os.MkdirAll(alpha.worktreePath("agent-1"), 0755); err != nil {
		t.Fatalf("mkdir alpha worktree: %v", err)
	}
	if err := os.MkdirAll(beta.worktreePath("agent-1"), 0755); err != nil {
		t.Fatalf("mkdir beta worktree: %v", err)
	}

	if err := alpha.Cleanup(t.Context()); err != nil && !strings.Contains(err.Error(), "cleanup errors") {
		t.Fatalf("alpha Cleanup: %v", err)
	}
	if _, err := os.Stat(beta.worktreePath("agent-1")); err != nil {
		t.Fatalf("beta worktree should remain after alpha cleanup: %v", err)
	}

	betaWorktrees, err := beta.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("beta ListWorktrees after alpha cleanup: %v", err)
	}
	if len(betaWorktrees) != 1 {
		t.Fatalf("expected beta worktree to survive alpha cleanup, got %d entries", len(betaWorktrees))
	}
}

func TestCreateForAgent_BranchNameFormat(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "my-session")

	info, err := manager.buildWorktreeInfo("my-agent")
	if err != nil {
		t.Fatalf("buildWorktreeInfo: %v", err)
	}

	if info.BranchName != "ntm/my-session/my-agent" {
		t.Errorf("BranchName = %q, want ntm/my-session/my-agent", info.BranchName)
	}
}

func TestCreateForAgent_PathFormat(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess")

	info, err := manager.buildWorktreeInfo("agent-cc")
	if err != nil {
		t.Fatalf("buildWorktreeInfo: %v", err)
	}

	expectedPath := manager.worktreePath("agent-cc")
	if info.Path != expectedPath {
		t.Errorf("Path = %q, want %q", info.Path, expectedPath)
	}
}

// Integration test with real git repo
func TestCreateForAgent_RealGitRepo(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	info, err := manager.CreateForAgent(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}

	if !info.Created {
		t.Error("expected Created=true for new worktree")
	}
	if info.Error != "" {
		t.Errorf("unexpected error: %s", info.Error)
	}

	// Verify the worktree directory exists
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree path does not exist: %v", err)
	}

	// Verify branch name
	if info.BranchName != "ntm/test-sess/cc-1" {
		t.Errorf("BranchName = %q, want ntm/test-sess/cc-1", info.BranchName)
	}
}

func TestListWorktrees_RealGitRepo(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Create two worktrees
	_, err := manager.CreateForAgent(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("CreateForAgent cc-1: %v", err)
	}
	_, err = manager.CreateForAgent(t.Context(), "cod-2")
	if err != nil {
		t.Fatalf("CreateForAgent cod-2: %v", err)
	}

	worktrees, err := manager.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}

	if len(worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(worktrees))
	}

	agents := map[string]bool{}
	for _, wt := range worktrees {
		agents[wt.AgentName] = true
	}
	if !agents["cc-1"] || !agents["cod-2"] {
		t.Errorf("expected agents cc-1 and cod-2, got %v", agents)
	}
}

func TestRemoveWorktree_RealGitRepo(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Create
	info, err := manager.CreateForAgent(t.Context(), "rm-agent")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("worktree should exist: %v", err)
	}

	// Remove
	if err := manager.RemoveWorktree(t.Context(), "rm-agent"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Verify removed
	worktrees, err := manager.ListWorktrees(t.Context())
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	for _, wt := range worktrees {
		if wt.AgentName == "rm-agent" {
			t.Error("expected rm-agent to be removed from listing")
		}
	}
}

func TestCleanup_RealGitRepo(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Create multiple worktrees
	for _, name := range []string{"a1", "a2", "a3"} {
		if _, err := manager.CreateForAgent(t.Context(), name); err != nil {
			t.Fatalf("CreateForAgent %s: %v", name, err)
		}
	}

	worktrees, _ := manager.ListWorktrees(t.Context())
	if len(worktrees) != 3 {
		t.Fatalf("expected 3 worktrees before cleanup, got %d", len(worktrees))
	}

	// Cleanup
	if err := manager.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Verify all removed
	worktrees, _ = manager.ListWorktrees(t.Context())
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees after cleanup, got %d", len(worktrees))
	}
}

func TestCleanup_ErrorAggregation(t *testing.T) {
	t.Parallel()

	// Test that Cleanup aggregates errors (use a non-git directory)
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess")

	// Create worktree directories manually (no real git)
	worktreesDir := manager.sessionRoot()
	for _, name := range []string{"agent-1", "agent-2"} {
		if err := os.MkdirAll(filepath.Join(worktreesDir, name), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	err := manager.Cleanup(t.Context())
	// Cleanup may fail since these aren't real git worktrees,
	// but it should aggregate errors, not panic
	if err != nil && !strings.Contains(err.Error(), "cleanup errors") {
		t.Errorf("expected aggregated cleanup errors, got: %v", err)
	}
}

func TestMergeBack_NonGitRepo(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess")

	err := manager.MergeBack(t.Context(), "agent-1")
	if err == nil {
		t.Fatal("expected error for MergeBack in non-git directory")
	}
}

func TestRemoveWorktree_NonExistent(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Remove non-existent worktree should not error fatally
	err := manager.RemoveWorktree(t.Context(), "nonexistent")
	if err != nil {
		t.Fatalf("RemoveWorktree (non-existent) should not error: %v", err)
	}
}

func TestRemoveWorktreeReportsFailedBranchDeletion(t *testing.T) {
	fakeBin := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
  "worktree remove") exit 0 ;;
  "branch -D") echo "branch deletion refused" >&2; exit 1 ;;
  "show-ref --verify") exit 0 ;;
  *) echo "unexpected git invocation: $*" >&2; exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := NewManager(t.TempDir(), "test-sess").RemoveWorktree(t.Context(), "agent-1")
	if err == nil {
		t.Fatal("RemoveWorktree() succeeded after branch deletion failed")
	}
	if !strings.Contains(err.Error(), "delete worktree branch ntm/test-sess/agent-1") {
		t.Fatalf("RemoveWorktree() error = %v, want branch deletion failure", err)
	}
}

func TestRemoveWorktree_RejectsUnsafeAgentNameWithoutDeletingSiblingPath(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")
	siblingPath := filepath.Join(projectDir, ".ntm", "keep")
	if err := os.MkdirAll(siblingPath, 0755); err != nil {
		t.Fatalf("mkdir sibling path: %v", err)
	}

	err := manager.RemoveWorktree(t.Context(), "../keep")
	if err == nil {
		t.Fatal("expected error for unsafe agent name")
	}
	if _, statErr := os.Stat(siblingPath); statErr != nil {
		t.Fatalf("expected sibling path to remain after rejection, got %v", statErr)
	}
}

func TestRemoveWorktreeRefusesFallbackForUnregisteredDirectory(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir() // Intentionally not a Git repository.
	manager := NewManager(projectDir, "test-session")
	worktreePath := manager.worktreePath("agent-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create unregistered worktree path: %v", err)
	}
	marker := filepath.Join(worktreePath, "must-remain")
	if err := os.WriteFile(marker, []byte("not a worktree\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	err := manager.RemoveWorktree(t.Context(), "agent-1")
	if err == nil || !strings.Contains(err.Error(), "refusing filesystem cleanup for unregistered worktree") {
		t.Fatalf("RemoveWorktree() error = %v, want unregistered-worktree rejection", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("unregistered directory marker was deleted: %v", statErr)
	}
}

func TestIsValidWorktree_NoGitFile(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess")

	// Create a directory without .git file
	worktreePath := filepath.Join(projectDir, "fake-worktree")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// isValidWorktree is private, test through GetWorktreeForAgent
	wtDir := manager.worktreePath("test-agent")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info, _ := manager.GetWorktreeForAgent(t.Context(), "test-agent")
	if info.Error == "" {
		t.Error("expected error for invalid worktree without .git file")
	}
}

func TestIsValidWorktree_WithGitFile(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Create a real worktree
	info, err := manager.CreateForAgent(t.Context(), "valid-agent")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}

	// GetWorktreeForAgent should report it as valid
	info2, err := manager.GetWorktreeForAgent(t.Context(), "valid-agent")
	if err != nil {
		t.Fatalf("GetWorktreeForAgent: %v", err)
	}
	if info2.Error != "" {
		t.Errorf("expected no error for valid worktree, got %q", info2.Error)
	}
	if !info2.Created {
		t.Error("expected Created=true for existing valid worktree")
	}
	_ = info // suppress unused
}

func TestGetWorktreeForAgent_DoesNotTreatPrefixMatchedWorktreeAsValid(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	if _, err := manager.CreateForAgent(t.Context(), "agent-10"); err != nil {
		t.Fatalf("CreateForAgent agent-10: %v", err)
	}

	fakePath := manager.worktreePath("agent-1")
	if err := os.MkdirAll(fakePath, 0755); err != nil {
		t.Fatalf("mkdir fake worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakePath, ".git"), []byte("gitdir: fake\n"), 0644); err != nil {
		t.Fatalf("write fake .git: %v", err)
	}

	info, err := manager.GetWorktreeForAgent(t.Context(), "agent-1")
	if err != nil {
		t.Fatalf("GetWorktreeForAgent: %v", err)
	}
	if info.Error == "" {
		t.Fatalf("expected prefix-matched fake worktree to be reported invalid, got %+v", info)
	}
}

func TestSessionHasWorktrees(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()

	if SessionHasWorktrees(projectDir, "sess") {
		t.Fatal("no worktree root yet: want false")
	}
	if SessionHasWorktrees("", "sess") || SessionHasWorktrees(projectDir, "") {
		t.Fatal("empty inputs must report false")
	}

	root := filepath.Join(projectDir, ".ntm", "worktrees", "sess")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !SessionHasWorktrees(projectDir, "sess") {
		t.Fatal("session worktree root exists: want true")
	}
	t.Logf("decision: the surviving worktree root is the previous-isolation-mode record used for transition detection (persists across ntm kill)")
}

// runWorktreeE2EGit runs a git command in dir for the merge round-trip test.
func runWorktreeE2EGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

// TestMergeBack_MasterDefaultRepo proves the worktree merge round-trip works
// in a repo whose default branch is 'master' (bd-ws1-truth-safety-l5ddi.5):
// before DefaultBranch(), MergeBack hardcoded `git checkout main` and was
// unusable on master-default repos.
func TestMergeBack_MasterDefaultRepo(t *testing.T) {
	// Keep host-level init.defaultBranch out of the resolution chain.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	tmp := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "master"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}

	manager := NewManager(tmp, "merge-sess")
	info, err := manager.CreateForAgent(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}

	// Commit agent work in the isolated worktree.
	if err := os.WriteFile(filepath.Join(info.Path, "agent.txt"), []byte("agent work\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	runWorktreeE2EGit(t, info.Path, "add", "agent.txt")
	runWorktreeE2EGit(t, info.Path, "commit", "-m", "agent work")

	if err := manager.MergeBack(t.Context(), "cc-1"); err != nil {
		t.Fatalf("MergeBack: %v", err)
	}

	// The primary checkout must be on master with the agent's work merged.
	branch := strings.TrimSpace(string(runWorktreeE2EGit(t, tmp, "rev-parse", "--abbrev-ref", "HEAD")))
	if branch != "master" {
		t.Fatalf("primary checkout on %q after merge, want master", branch)
	}
	if _, err := os.Stat(filepath.Join(tmp, "agent.txt")); err != nil {
		t.Fatalf("agent work not merged into master: %v", err)
	}
}
