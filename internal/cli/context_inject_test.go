package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDefaultContextFiles(t *testing.T) {
	files := defaultContextFiles()
	if len(files) != 3 {
		t.Fatalf("expected 3 default files, got %d", len(files))
	}
	if files[0] != "AGENTS.md" {
		t.Errorf("expected AGENTS.md first, got %s", files[0])
	}
	if files[1] != "README.md" {
		t.Errorf("expected README.md second, got %s", files[1])
	}
	if files[2] != ".claude/project_context.md" {
		t.Errorf("expected .claude/project_context.md third, got %s", files[2])
	}
}

func TestFormatContextInjectContent_BasicFiles(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Agent Rules\nRule 1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Project README\nDescription"), 0644); err != nil {
		t.Fatal(err)
	}

	content, injected, truncated, err := formatContextInjectContent(dir, defaultContextFiles(), 0)
	if err != nil {
		t.Fatal(err)
	}

	if truncated {
		t.Error("expected no truncation")
	}

	if len(injected) != 2 {
		t.Fatalf("expected 2 injected files, got %d: %v", len(injected), injected)
	}

	if injected[0] != "AGENTS.md" {
		t.Errorf("expected AGENTS.md, got %s", injected[0])
	}
	if injected[1] != "README.md" {
		t.Errorf("expected README.md, got %s", injected[1])
	}

	if !strings.Contains(content, "### AGENTS.md") {
		t.Error("content should contain AGENTS.md header")
	}
	if !strings.Contains(content, "# Agent Rules") {
		t.Error("content should contain AGENTS.md body")
	}
	if !strings.Contains(content, "### README.md") {
		t.Error("content should contain README.md header")
	}
	if !strings.Contains(content, "# Project README") {
		t.Error("content should contain README.md body")
	}
}

func TestFormatContextInjectContent_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	// No files created - all should be skipped

	content, injected, truncated, err := formatContextInjectContent(dir, defaultContextFiles(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("expected no truncation")
	}
	if len(injected) != 0 {
		t.Errorf("expected 0 injected files, got %d", len(injected))
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestFormatContextInjectContent_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("  \n  "), 0644); err != nil {
		t.Fatal(err)
	}

	_, injected, _, err := formatContextInjectContent(dir, defaultContextFiles(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(injected) != 0 {
		t.Errorf("expected 0 injected files for empty content, got %d", len(injected))
	}
}

func TestFormatContextInjectContent_MaxBytesTruncation(t *testing.T) {
	dir := t.TempDir()

	// Create a large file
	largeContent := strings.Repeat("A", 1000)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(largeContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Small content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Limit to 200 bytes total
	content, injected, truncated, err := formatContextInjectContent(dir, defaultContextFiles(), 200)
	if err != nil {
		t.Fatal(err)
	}

	if !truncated {
		t.Error("expected truncation")
	}

	// Should have at least AGENTS.md (truncated)
	if len(injected) == 0 {
		t.Fatal("expected at least one injected file")
	}

	if !strings.Contains(content, "...(truncated)") {
		t.Error("truncated content should contain truncation marker")
	}

	if len(content) > 250 { // Some slack for headers
		t.Errorf("content too large: %d bytes", len(content))
	}
}

func TestFormatContextInjectContent_CustomFiles(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte("Custom content"), 0644); err != nil {
		t.Fatal(err)
	}

	content, injected, _, err := formatContextInjectContent(dir, []string{"custom.md"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(injected) != 1 {
		t.Fatalf("expected 1 injected file, got %d", len(injected))
	}
	if injected[0] != "custom.md" {
		t.Errorf("expected custom.md, got %s", injected[0])
	}
	if !strings.Contains(content, "### custom.md") {
		t.Error("content should contain custom.md header")
	}
	if !strings.Contains(content, "Custom content") {
		t.Error("content should contain custom.md body")
	}
}

func TestFormatContextInjectContent_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	absPath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(absPath, []byte("README"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := formatContextInjectContent(dir, []string{absPath}, 0)
	if err == nil {
		t.Fatal("expected error for absolute inject file path, got nil")
	}
	if !strings.Contains(err.Error(), "project-relative") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatContextInjectContent_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := formatContextInjectContent(projectDir, []string{"../secret.txt"}, 0)
	if err == nil {
		t.Fatal("expected path traversal error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes project directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatContextInjectContent_NegativeMaxBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := formatContextInjectContent(dir, []string{"README.md"}, -1)
	if err == nil {
		t.Fatal("expected error for negative max bytes, got nil")
	}
	if !strings.Contains(err.Error(), "maxBytes must be >= 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectContextInjectTargetPanes(t *testing.T) {
	panes := []tmux.Pane{{Index: 0}, {Index: 1}, {Index: 2}}

	t.Run("default_excludes_user_pane", func(t *testing.T) {
		targets, err := selectContextInjectTargetPanes(panes, -1, false, "demo")
		if err != nil {
			t.Fatalf("selectContextInjectTargetPanes returned error: %v", err)
		}
		if len(targets) != 2 || targets[0].Index != 1 || targets[1].Index != 2 {
			t.Fatalf("unexpected targets: %+v", targets)
		}
	})

	t.Run("all_includes_user_pane", func(t *testing.T) {
		targets, err := selectContextInjectTargetPanes(panes, -1, true, "demo")
		if err != nil {
			t.Fatalf("selectContextInjectTargetPanes returned error: %v", err)
		}
		if len(targets) != 3 {
			t.Fatalf("expected 3 targets, got %d", len(targets))
		}
	})

	t.Run("specific_pane_found", func(t *testing.T) {
		targets, err := selectContextInjectTargetPanes(panes, 2, false, "demo")
		if err != nil {
			t.Fatalf("selectContextInjectTargetPanes returned error: %v", err)
		}
		if len(targets) != 1 || targets[0].Index != 2 {
			t.Fatalf("unexpected targets: %+v", targets)
		}
	})

	t.Run("specific_pane_missing_errors", func(t *testing.T) {
		_, err := selectContextInjectTargetPanes(panes, 99, false, "demo")
		if err == nil {
			t.Fatal("expected missing pane error, got nil")
		}
		if !strings.Contains(err.Error(), "pane 99 not found in session demo") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// GH#251 phase 2: mixed batches containing Grok Build panes now inject like
// claude/codex batches — every alias canonicalizes to a deliverable target.
func TestInjectContextIntoPanesAcceptsMixedGrokBatch(t *testing.T) {
	for _, grokAlias := range []tmux.AgentType{tmux.AgentGrok, " Grok-Build ", "XAI_GROK_BUILD"} {
		t.Run(string(grokAlias), func(t *testing.T) {
			panes := []tmux.Pane{
				{ID: "%1", Index: 1, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, Type: grokAlias},
				{ID: "%3", Index: 3, Type: tmux.AgentCodex},
			}
			calls := 0
			injected, err := injectContextIntoPanes("demo", panes, "context", false, func(string, string, bool) error {
				calls++
				return nil
			})

			if err != nil {
				t.Fatalf("error = %v, want nil (grok delivery supported in phase 2)", err)
			}
			if calls != 3 {
				t.Fatalf("sender calls = %d, want 3", calls)
			}
			if len(injected) != 3 {
				t.Fatalf("injected panes = %v, want 3 entries", injected)
			}
		})
	}
}

func TestRobotContextInjectGrokFailureUsesUnavailableExitCode(t *testing.T) {
	code, hint := classifyRobotContextInjectError(agent.ErrAutomatedPromptDeliveryNotImplemented)
	if code != robot.ErrCodeNotImplemented {
		t.Fatalf("error code = %q, want %q", code, robot.ErrCodeNotImplemented)
	}
	if hint != agent.GrokPromptDeliveryCapabilityHint {
		t.Fatalf("hint = %q, want %q", hint, agent.GrokPromptDeliveryCapabilityHint)
	}
	response := robot.NewErrorResponse(agent.ErrAutomatedPromptDeliveryNotImplemented, code, hint)
	if exitCode := robot.ExitCodeForResponse(response); exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
}

func TestNewContextInjectCmdRejectsInvalidSessionName(t *testing.T) {
	cmd := newContextInjectCmd()
	cmd.SetArgs([]string{"../escape"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetRepoRev_DirectoryGitDir(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	refDir := filepath.Join(gitDir, "refs", "heads")
	if err := os.MkdirAll(refDir, 0755); err != nil {
		t.Fatal(err)
	}

	const rev = "1234567890abcdef1234567890abcdef12345678"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "main"), []byte(rev+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := getRepoRev(dir); got != rev {
		t.Fatalf("getRepoRev() = %q, want %q", got, rev)
	}
}

func TestGetRepoRev_WorktreeGitDirFile(t *testing.T) {
	root := t.TempDir()
	worktreeDir := filepath.Join(root, "worktree")
	actualGitDir := filepath.Join(root, "gitdir")
	refDir := filepath.Join(actualGitDir, "refs", "heads")
	if err := os.MkdirAll(refDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	const rev = "abcdef1234567890abcdef1234567890abcdef12"
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: ../gitdir\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actualGitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "main"), []byte(rev+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := getRepoRev(worktreeDir); got != rev {
		t.Fatalf("getRepoRev() = %q, want %q", got, rev)
	}
}

func TestGetRepoRev_PackedRefsFallback(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	const rev = "fedcba0987654321fedcba0987654321fedcba09"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	packedRefs := "# pack-refs with: peeled fully-peeled sorted\n" + rev + " refs/heads/main\n"
	if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(packedRefs), 0644); err != nil {
		t.Fatal(err)
	}

	if got := getRepoRev(dir); got != rev {
		t.Fatalf("getRepoRev() = %q, want %q", got, rev)
	}
}

func TestFormatContextInjectContent_NestedFiles(t *testing.T) {
	dir := t.TempDir()

	// Create nested directory structure
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "project_context.md"), []byte("Claude context"), 0644); err != nil {
		t.Fatal(err)
	}

	content, injected, _, err := formatContextInjectContent(dir, defaultContextFiles(), 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(injected) != 1 {
		t.Fatalf("expected 1 injected file, got %d", len(injected))
	}
	if injected[0] != ".claude/project_context.md" {
		t.Errorf("expected .claude/project_context.md, got %s", injected[0])
	}
	if !strings.Contains(content, "Claude context") {
		t.Error("content should contain Claude context body")
	}
}

func TestFormatContextInjectContent_Separator(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Agents"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Readme"), 0644); err != nil {
		t.Fatal(err)
	}

	content, _, _, err := formatContextInjectContent(dir, defaultContextFiles(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Files should be separated by ---
	if !strings.Contains(content, "---") {
		t.Error("multi-file content should contain separator")
	}
}

func TestFormatContextInjectContent_MaxBytesZeroSkip(t *testing.T) {
	dir := t.TempDir()

	// Create files where the first already fills the budget
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(strings.Repeat("X", 500)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Readme content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Budget so tight that second file won't fit after first
	_, injected, truncated, err := formatContextInjectContent(dir, defaultContextFiles(), 50)
	if err != nil {
		t.Fatal(err)
	}

	// First file should be truncated, second should be skipped
	if !truncated {
		t.Error("expected truncation")
	}

	if len(injected) > 1 {
		t.Errorf("expected at most 1 file, got %d", len(injected))
	}
}

func TestContextInjectResult_Fields(t *testing.T) {
	r := ContextInjectResult{
		Success:       true,
		Session:       "test",
		InjectedFiles: []string{"AGENTS.md"},
		TotalBytes:    42,
		Truncated:     false,
		PanesInjected: []int{1, 2},
	}

	if !r.Success {
		t.Error("expected success")
	}
	if r.Session != "test" {
		t.Error("wrong session")
	}
	if len(r.InjectedFiles) != 1 {
		t.Error("wrong file count")
	}
	if r.TotalBytes != 42 {
		t.Error("wrong byte count")
	}
	if r.Truncated {
		t.Error("should not be truncated")
	}
	if len(r.PanesInjected) != 2 {
		t.Error("wrong pane count")
	}
}
