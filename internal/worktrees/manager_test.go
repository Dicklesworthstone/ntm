package worktrees

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	projectDir := t.TempDir()
	manager := NewManager(projectDir, "test-session")

	worktreePath := manager.worktreePath("agent-1")
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

	expectedPath := manager.worktreePath("agent-1")
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
	worktreesPath := manager.sessionRoot()
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

	worktreesDir := manager.sessionRoot()
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

	worktreesDir := manager.worktreesRoot()
	if err := os.MkdirAll(manager.sessionRoot(), 0755); err != nil {
		t.Fatalf("failed to create worktrees dir: %v", err)
	}

	if err := manager.Cleanup(); err != nil {
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

	worktrees, err := manager.ListWorktrees()
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

	info, err := manager.GetWorktreeForAgent("agent-cc")
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

	info1, _ := m1.GetWorktreeForAgent("agent-1")
	info2, _ := m2.GetWorktreeForAgent("agent-1")

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

	alphaWorktrees, err := alpha.ListWorktrees()
	if err != nil {
		t.Fatalf("alpha ListWorktrees: %v", err)
	}
	if len(alphaWorktrees) != 1 {
		t.Fatalf("expected 1 alpha worktree, got %d", len(alphaWorktrees))
	}
	if alphaWorktrees[0].Path != alpha.worktreePath("agent-1") {
		t.Fatalf("alpha path = %q, want %q", alphaWorktrees[0].Path, alpha.worktreePath("agent-1"))
	}

	betaWorktrees, err := beta.ListWorktrees()
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

	if err := alpha.Cleanup(); err != nil && !strings.Contains(err.Error(), "cleanup errors") {
		t.Fatalf("alpha Cleanup: %v", err)
	}
	if _, err := os.Stat(beta.worktreePath("agent-1")); err != nil {
		t.Fatalf("beta worktree should remain after alpha cleanup: %v", err)
	}

	betaWorktrees, err := beta.ListWorktrees()
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

	// Pre-create the directory so it returns early without calling git
	worktreePath := manager.worktreePath("my-agent")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info, err := manager.CreateForAgent("my-agent")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}

	if info.BranchName != "ntm/my-session/my-agent" {
		t.Errorf("BranchName = %q, want ntm/my-session/my-agent", info.BranchName)
	}
}

func TestCreateForAgent_PathFormat(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	manager := NewManager(projectDir, "sess")

	// Pre-create to avoid git call
	worktreePath := manager.worktreePath("agent-cc")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info, err := manager.CreateForAgent("agent-cc")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
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

	info, err := manager.CreateForAgent("cc-1")
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
	_, err := manager.CreateForAgent("cc-1")
	if err != nil {
		t.Fatalf("CreateForAgent cc-1: %v", err)
	}
	_, err = manager.CreateForAgent("cod-2")
	if err != nil {
		t.Fatalf("CreateForAgent cod-2: %v", err)
	}

	worktrees, err := manager.ListWorktrees()
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
	info, err := manager.CreateForAgent("rm-agent")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("worktree should exist: %v", err)
	}

	// Remove
	if err := manager.RemoveWorktree("rm-agent"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Verify removed
	worktrees, err := manager.ListWorktrees()
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
		if _, err := manager.CreateForAgent(name); err != nil {
			t.Fatalf("CreateForAgent %s: %v", name, err)
		}
	}

	worktrees, _ := manager.ListWorktrees()
	if len(worktrees) != 3 {
		t.Fatalf("expected 3 worktrees before cleanup, got %d", len(worktrees))
	}

	// Cleanup
	if err := manager.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Verify all removed
	worktrees, _ = manager.ListWorktrees()
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

	err := manager.Cleanup()
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

	err := manager.MergeBack("agent-1")
	if err == nil {
		t.Fatal("expected error for MergeBack in non-git directory")
	}
}

func TestRemoveWorktree_NonExistent(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Remove non-existent worktree should not error fatally
	err := manager.RemoveWorktree("nonexistent")
	if err != nil {
		t.Fatalf("RemoveWorktree (non-existent) should not error: %v", err)
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

	info, _ := manager.GetWorktreeForAgent("test-agent")
	if info.Error == "" {
		t.Error("expected error for invalid worktree without .git file")
	}
}

func TestIsValidWorktree_WithGitFile(t *testing.T) {
	t.Parallel()

	tmp := setupWorktreeGitRepo(t)
	manager := NewManager(tmp, "test-sess")

	// Create a real worktree
	info, err := manager.CreateForAgent("valid-agent")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
	}

	// GetWorktreeForAgent should report it as valid
	info2, err := manager.GetWorktreeForAgent("valid-agent")
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
