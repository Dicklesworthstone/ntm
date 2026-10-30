package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

	if len(content) > 200 {
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
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Type: tmux.AgentUser},
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentCodex},
	}

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
		if !strings.Contains(err.Error(), "session demo") || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("default_uses_agent_identity_including_zero", func(t *testing.T) {
		targets, err := selectContextInjectTargetPanes([]tmux.Pane{
			{ID: "%0", Index: 0, Type: "claude-code"},
			{ID: "%1", Index: 1, Type: tmux.AgentUser},
			{ID: "%2", Index: 2, Type: "unknown"},
		}, -1, false, "demo")
		if err != nil || len(targets) != 1 || targets[0].ID != "%0" {
			t.Fatalf("agent-at-zero default selected wrong targets: %+v, %v", targets, err)
		}
	})

	t.Run("default_without_agents_reports_actionable_error", func(t *testing.T) {
		targets, err := selectContextInjectTargetPanes([]tmux.Pane{
			{ID: "%1", Index: 1, Type: tmux.AgentUser},
			{ID: "%2", Index: 2, Type: "unknown"},
		}, -1, false, "demo")
		if err == nil || len(targets) != 0 || !strings.Contains(err.Error(), "no known agents") || !strings.Contains(err.Error(), "select a pane explicitly") {
			t.Fatalf("no-agent default did not explain how to select targets: %+v, %v", targets, err)
		}
	})

	t.Run("robot_integer_refuses_duplicate_index_across_windows", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
			{ID: "%2", WindowIndex: 1, Index: 0, Type: tmux.AgentClaude},
			{ID: "%3", WindowIndex: 1, Index: 1, Type: tmux.AgentCodex},
		}
		if targets, err := selectContextInjectTargetPanes(panes, 1, false, "demo"); err == nil || len(targets) != 0 {
			t.Fatalf("ambiguous robot target selected the first pane: %+v, %v", targets, err)
		}
	})

	t.Run("robot_integer_preserves_pane_index_in_multi_window_session", func(t *testing.T) {
		panes := []tmux.Pane{
			{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
			{ID: "%2", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex},
		}
		targets, err := selectContextInjectTargetPanes(panes, 1, false, "demo")
		if err != nil || len(targets) != 1 || targets[0].ID != "%1" {
			t.Fatalf("robot pane index was reinterpreted as a window: %+v, %v", targets, err)
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

func TestFormatContextInjectContent_ExactTextBudgets(t *testing.T) {
	dir := t.TempDir()
	files := []string{"first.md", "second.md", "last.md"}
	contents := []string{"first file", strings.Repeat("é🙂界", 12), "last file"}
	for i, file := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(contents[i]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	full, _, truncated, err := formatContextInjectContent(dir, files, 0)
	if err != nil || truncated {
		t.Fatalf("unlimited text preparation: truncated=%t err=%v", truncated, err)
	}
	for budget := 1; budget <= len(full)+1; budget++ {
		content, _, truncated, err := formatContextInjectContent(dir, files, budget)
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if len(content) > budget || !utf8.ValidString(content) {
			t.Fatalf("budget %d produced %d bytes or invalid UTF-8: %q", budget, len(content), content)
		}
		if truncated != (budget < len(full)) {
			t.Fatalf("budget %d: truncated=%t, full size=%d", budget, truncated, len(full))
		}
		if budget >= len(full) && content != full {
			t.Fatalf("budget %d changed a fully fitting message", budget)
		}
	}
}

func TestReadContextInjectTextBoundsReads(t *testing.T) {
	input := strings.Repeat("bounded source", 4096)
	reader := strings.NewReader(input)
	content, truncated, err := readContextInjectText(reader, 32)
	if err != nil || !truncated || content != input[:32] {
		t.Fatalf("bounded read: content=%q truncated=%t err=%v", content, truncated, err)
	}
	if consumed := len(input) - reader.Len(); consumed > 33 {
		t.Fatalf("32-byte budget read %d bytes from the source", consumed)
	}
	// A very large user budget must not overflow the read limit.
	content, truncated, err = readContextInjectText(strings.NewReader("whole file"), int(^uint(0)>>1))
	if err != nil || truncated || content != "whole file" {
		t.Fatalf("maximum integer budget: content=%q truncated=%t err=%v", content, truncated, err)
	}
}

func TestReadContextInjectTextValidity(t *testing.T) {
	for _, tc := range []struct {
		name, input, want, wantError string
		limit                       int
		truncated                   bool
	}{
		{name: "unlimited", input: "  café🙂\n", want: "café🙂", limit: -1},
		{name: "exact", input: "界", want: "界", limit: 3},
		{name: "split rune", input: "界tail", limit: 2, truncated: true},
		{name: "zero bytes", input: "text", limit: 0, truncated: true},
		{name: "invalid interior", input: "a\xffrest", limit: 3, wantError: "invalid UTF-8"},
		{name: "incomplete at EOF", input: "a\xc2", limit: 2, wantError: "invalid UTF-8"},
		{name: "binary", input: "text\x00tail", limit: -1, wantError: "NUL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, truncated, err := readContextInjectText(strings.NewReader(tc.input), tc.limit)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %s, got content=%q err=%v", tc.wantError, content, err)
				}
				return
			}
			if err != nil || content != tc.want || truncated != tc.truncated || !utf8.ValidString(content) {
				t.Fatalf("content=%q truncated=%t err=%v; want %q truncated=%t", content, truncated, err, tc.want, tc.truncated)
			}
		})
	}
}

func TestFormatContextInjectContent_AllowsConfinedSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("project context"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(dir, "alias.md")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	content, injected, truncated, err := formatContextInjectContent(dir, []string{"alias.md"}, 0)
	if err != nil || truncated || content != "### alias.md\n\nproject context" || len(injected) != 1 || injected[0] != "alias.md" {
		t.Fatalf("confined symlink rejected or changed: content=%q files=%v truncated=%t err=%v", content, injected, truncated, err)
	}
}

func TestFormatContextInjectContent_RejectsFIFO(t *testing.T) {
	// Run the read in a child test process so removing the regular-file guard
	// fails by a bounded deadline instead of hanging the whole test suite.
	if fifo := os.Getenv("NTM_CONTEXT_INJECT_FIFO_FIXTURE"); fifo != "" {
		_, _, _, err := formatContextInjectContent(filepath.Dir(fifo), []string{filepath.Base(fifo)}, 64)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO was not rejected before opening: %v", err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("FIFO fixture requires a Unix filesystem")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable")
	}
	fifo := filepath.Join(t.TempDir(), "context.pipe")
	if output, err := exec.Command(mkfifo, fifo).CombinedOutput(); err != nil {
		t.Fatalf("create FIFO fixture: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFormatContextInjectContent_RejectsFIFO$", "-test.timeout=4s")
	cmd.Env = append(os.Environ(), "NTM_CONTEXT_INJECT_FIFO_FIXTURE="+fifo)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO rejection must finish without a writer: %v: %s", err, output)
	}
}

func TestContextInjectCobraRejectsUnsafeRawFiles(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, scenario := range []string{"file symlink", "directory symlink", "default symlink", "directory", "invalid UTF-8", "NUL"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "private.md"), []byte("OUTSIDE_PROJECT_CONTENT"), 0600); err != nil {
				t.Fatal(err)
			}
			selected := "README.md"
			args := []string{"demo", "--files", selected}
			switch scenario {
			case "file symlink", "directory symlink", "default symlink":
				target, link := filepath.Join(outside, "private.md"), filepath.Join(dir, selected)
				if scenario == "directory symlink" {
					target, link = outside, filepath.Join(dir, "linked")
					args = []string{"demo", "--files", "linked/private.md"}
				} else if scenario == "default symlink" {
					if err := os.Mkdir(filepath.Join(dir, ".claude"), 0700); err != nil {
						t.Fatal(err)
					}
					link = filepath.Join(dir, ".claude", "project_context.md")
					args = []string{"demo"}
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink fixture unavailable: %v", err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(dir, selected), 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid UTF-8", "NUL":
				data := []byte("text\xfftail")
				if scenario == "NUL" {
					data = []byte("text\x00tail")
				}
				if err := os.WriteFile(filepath.Join(dir, selected), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			deps := contextInjectDeps{
				resolve: func(context.Context, string) (string, error) { return "demo", nil },
				project: func(context.Context, string) (string, error) { return dir, nil },
				panes: func(string) ([]tmux.Pane, error) {
					return []tmux.Pane{{ID: "%7", Index: 1, Type: tmux.AgentClaude}}, nil
				},
				send: func(context.Context, tmux.Pane, string) error {
					calls++
					return nil
				},
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(t.Context())
			var result ContextInjectResult
			if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
				t.Fatalf("invalid command response: %v: %s", decodeErr, out.String())
			}
			if err == nil || result.Success || result.Error == "" || calls != 0 || len(result.PanesInjected) != 0 {
				t.Fatalf("unsafe preparation reached delivery: calls=%d result=%+v err=%v", calls, result, err)
			}
			if strings.Contains(out.String(), "OUTSIDE_PROJECT_CONTENT") {
				t.Fatal("outside-project content leaked into the command response")
			}
		})
	}
}

func TestContextInjectCobraRawFilesHonorsByteBudget(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"a.md": "first", "b.md": strings.Repeat("é🙂", 30)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	var delivered string
	deps := contextInjectDeps{
		resolve: func(context.Context, string) (string, error) { return "demo", nil },
		project: func(context.Context, string) (string, error) { return dir, nil },
		panes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%7", Index: 1, Type: tmux.AgentClaude}}, nil
		},
		send: func(_ context.Context, _ tmux.Pane, content string) error {
			delivered = content
			return nil
		},
	}
	cmd := newContextInjectCmdWithDeps(&deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"demo", "--files", "a.md,b.md", "--max-bytes", "64"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var result ContextInjectResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid command response: %v: %s", err, out.String())
	}
	if delivered == "" || len(delivered) > 64 || !utf8.ValidString(delivered) || !strings.Contains(delivered, "...(truncated)") {
		t.Fatalf("command delivered an invalid or oversized payload (%d bytes): %q", len(delivered), delivered)
	}
	if !result.Success || !result.Truncated || result.TotalBytes != len(delivered) || len(result.InjectedFiles) != 2 || len(result.PanesInjected) != 1 {
		t.Fatalf("command receipt disagrees with delivery: %+v", result)
	}
}
