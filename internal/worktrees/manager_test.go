package worktrees

import (
	"os"
	"path/filepath"
	"testing"
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
	info, err := manager.GetWorktreeForAgent("test-agent")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if info.Created {
		t.Error("Expected Created to be false for non-existent worktree")
	}
	if info.Error == "" {
		t.Error("Expected error message for non-existent worktree")
	}

	expectedPath := filepath.Join(projectDir, ".ntm", "worktrees", "test-agent")
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
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreePath := filepath.Join(projectDir, ".ntm", "worktrees", "agent-1")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("failed to create worktree dir: %v", err)
	}

	info, err := manager.CreateForAgent("agent-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Created {
		t.Error("expected Created=false when worktree already exists")
	}
	if info.Error != "" {
		t.Errorf("expected empty error for existing worktree, got %q", info.Error)
	}

	expectedPath := filepath.Join(projectDir, ".ntm", "worktrees", "agent-1")
	if info.Path != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, info.Path)
	}

	expectedBranch := "ntm/test-session/agent-1"
	if info.BranchName != expectedBranch {
		t.Errorf("expected branch %s, got %s", expectedBranch, info.BranchName)
	}
}

func TestCreateForAgent_MkdirAllFailure(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	// Create a file where the worktrees directory should be to force MkdirAll failure.
	worktreesPath := filepath.Join(projectDir, ".ntm", "worktrees")
	if err := os.MkdirAll(filepath.Dir(worktreesPath), 0755); err != nil {
		t.Fatalf("failed to create .ntm dir: %v", err)
	}
	if err := os.WriteFile(worktreesPath, []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("failed to create worktrees file: %v", err)
	}

	info, err := manager.CreateForAgent("agent-2")
	if err == nil {
		t.Fatal("expected error when worktrees path is a file, got nil")
	}
	if info.Error == "" {
		t.Fatal("expected error message on worktree creation failure")
	}
}

func TestListWorktrees_MissingDirReturnsEmpty(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktrees, err := manager.ListWorktrees()
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

	worktreesDir := filepath.Join(projectDir, ".ntm", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("failed to create worktrees dir: %v", err)
	}

	worktrees, err := manager.ListWorktrees()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected empty worktree list, got %d", len(worktrees))
	}
}

func TestCleanup_RemovesEmptyWorktreesDir(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreesDir := filepath.Join(projectDir, ".ntm", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("failed to create worktrees dir: %v", err)
	}

	if err := manager.Cleanup(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(worktreesDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktrees dir to be removed, stat err: %v", err)
	}
}
