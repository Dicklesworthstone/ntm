package worktrees

import (
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	manager := NewManager("/tmp/test", "test-session")
	if manager.projectPath != "/tmp/test" {
		t.Errorf("Expected project path /tmp/test, got %s", manager.projectPath)
	}
	if manager.session != "test-session" {
		t.Errorf("Expected session test-session, got %s", manager.session)
	}
}

func TestWorktreeInfo(t *testing.T) {
	manager := NewManager("/tmp/test", "test-session")

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

	expectedPath := filepath.Join("/tmp/test", ".ntm", "worktrees", "test-agent")
	if info.Path != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, info.Path)
	}

	expectedBranch := "ntm/test-session/test-agent"
	if info.BranchName != expectedBranch {
		t.Errorf("Expected branch %s, got %s", expectedBranch, info.BranchName)
	}
}