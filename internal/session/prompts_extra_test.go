package session

import (
	"os"
	"testing"
)

// =============================================================================
// savePromptHistory — 58.8% → higher
// =============================================================================

func TestSavePromptHistory_NilHistory(t *testing.T) {
	err := savePromptHistory(nil)
	if err == nil {
		t.Error("expected error for nil history")
	}
}

// =============================================================================
// GetLatestPrompts — 75% → 100% (test limit=0 returns all)
// =============================================================================

// =============================================================================
// ClearPromptHistory — 71.4% → higher (test clearing non-existent session)
// =============================================================================

// =============================================================================
// ListSessionDirs — edge case: no sessions dir
// =============================================================================

// =============================================================================
// promptsFilePath — exercise the happy path
// =============================================================================

func TestPromptsFilePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-path")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	path, err := promptsFilePath("my-session")
	if err != nil {
		t.Fatalf("promptsFilePath: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
}
