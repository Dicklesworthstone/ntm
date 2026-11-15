package hooks

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// =============================================================================
// PrintPreCommitResult — 0% → 100%
// =============================================================================

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	return string(out)
}

func TestPrintPreCommitResult_Passed(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  []string{"main.go", "util.go"},
		Duration:     150 * time.Millisecond,
		UBSAvailable: true,
		ScanResult: &scanner.ScanResult{
			Totals: scanner.ScanTotals{Critical: 0, Warning: 0, Info: 1},
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if len(output) == 0 {
		t.Error("expected non-empty output")
	}
	// Should contain staged files count
	if !contains(output, "Staged files: 2") {
		t.Errorf("missing staged files count in output: %s", output)
	}
	// Should contain pass indicator
	if !contains(output, "passed") {
		t.Errorf("missing pass indicator: %s", output)
	}
}

func TestPrintPreCommitResult_Failed(t *testing.T) {
	result := &PreCommitResult{
		Passed:       false,
		StagedFiles:  []string{"bad.go"},
		Duration:     200 * time.Millisecond,
		UBSAvailable: true,
		BlockReason:  "critical findings",
		ScanResult: &scanner.ScanResult{
			Totals: scanner.ScanTotals{Critical: 2, Warning: 1, Info: 0},
			Findings: []scanner.Finding{
				{File: "bad.go", Line: 10, Severity: scanner.SeverityCritical, Message: "SQL injection"},
				{File: "bad.go", Line: 20, Severity: scanner.SeverityWarning, Message: "unused var"},
			},
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "failed") {
		t.Errorf("missing fail indicator: %s", output)
	}
	if !contains(output, "critical findings") {
		t.Errorf("missing block reason: %s", output)
	}
}

func TestPrintPreCommitResult_UBSNotAvailable(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  []string{"main.go"},
		Duration:     50 * time.Millisecond,
		UBSAvailable: false,
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "UBS not installed") {
		t.Errorf("missing UBS warning: %s", output)
	}
}

func TestPrintPreCommitResult_NoStagedFiles(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  nil,
		Duration:     10 * time.Millisecond,
		UBSAvailable: true,
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "No staged files") {
		t.Errorf("missing 'no staged files' message: %s", output)
	}
}

func TestPrintPreCommitResult_ManyFindings(t *testing.T) {
	findings := make([]scanner.Finding, 8)
	for i := range findings {
		findings[i] = scanner.Finding{
			File: "file.go", Line: i + 1,
			Severity: scanner.SeverityWarning, Message: "issue",
		}
	}

	result := &PreCommitResult{
		Passed:       false,
		StagedFiles:  []string{"file.go"},
		Duration:     100 * time.Millisecond,
		UBSAvailable: true,
		BlockReason:  "too many warnings",
		ScanResult: &scanner.ScanResult{
			Totals:   scanner.ScanTotals{Warning: 8},
			Findings: findings,
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	// Should truncate to 5 and show "... and 3 more"
	if !contains(output, "and 3 more") {
		t.Errorf("missing truncation message: %s", output)
	}
}

// =============================================================================
// ExitCode — 0% → 100%
// =============================================================================

func TestExitCode_Passed(t *testing.T) {
	t.Parallel()
	r := &PreCommitResult{Passed: true}
	if code := r.ExitCode(); code != 0 {
		t.Errorf("ExitCode() = %d, want 0", code)
	}
}

func TestExitCode_Failed(t *testing.T) {
	t.Parallel()
	r := &PreCommitResult{Passed: false}
	if code := r.ExitCode(); code != 1 {
		t.Errorf("ExitCode() = %d, want 1", code)
	}
}

// =============================================================================
// getStagedFiles — 0% → 100%
// =============================================================================

func setupHooksGitRepo(t *testing.T) string {
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

func TestGetStagedFiles_NoStaged(t *testing.T) {
	t.Parallel()
	repoDir := setupHooksGitRepo(t)

	files, err := getStagedFiles(repoDir)
	if err != nil {
		t.Fatalf("getStagedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("got %v, want empty for no staged files", files)
	}
}

func TestGetStagedFiles_WithStaged(t *testing.T) {
	t.Parallel()
	repoDir := setupHooksGitRepo(t)

	// Create a file and stage it
	filePath := repoDir + "/hello.go"
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", "hello.go")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	files, err := getStagedFiles(repoDir)
	if err != nil {
		t.Fatalf("getStagedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "hello.go" {
		t.Errorf("got %v, want [hello.go]", files)
	}
}

func TestGetStagedFiles_InvalidRepo(t *testing.T) {
	t.Parallel()
	_, err := getStagedFiles("/nonexistent-hooks-test-dir")
	if err == nil {
		t.Error("expected error for invalid repo")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHookCheckResultExitCodeAndPrint(t *testing.T) {
	var nilRes *HookCheckResult
	if nilRes.ExitCode() != 1 {
		t.Fatalf("nil HookCheckResult.ExitCode() = %d, want 1", nilRes.ExitCode())
	}

	passRes := &HookCheckResult{
		HookType: string(HookPrePush),
		Passed:   true,
		Warnings: []string{"beads dirty"},
		Duration: 15 * time.Millisecond,
	}
	if passRes.ExitCode() != 0 {
		t.Fatalf("passRes.ExitCode() = %d, want 0", passRes.ExitCode())
	}
	passOut := captureStdout(t, func() {
		PrintHookResult(passRes)
	})
	if !strings.Contains(passOut, "beads dirty") || !strings.Contains(passOut, "passed") {
		t.Fatalf("unexpected pass output: %q", passOut)
	}

	failRes := &HookCheckResult{
		HookType:    string(HookCommitMsg),
		Passed:      false,
		BlockReason: "commit message is empty",
		Duration:    5 * time.Millisecond,
	}
	if failRes.ExitCode() != 1 {
		t.Fatalf("failRes.ExitCode() = %d, want 1", failRes.ExitCode())
	}
	failOut := captureStdout(t, func() {
		PrintHookResult(failRes)
	})
	if !strings.Contains(failOut, "failed") || !strings.Contains(failOut, "commit message is empty") {
		t.Fatalf("unexpected fail output: %q", failOut)
	}
}

func TestRunPrePush_CleanAndDirtyBeadsAndCycles(t *testing.T) {
	repo := initTempGitRepoWithCommit(t)

	// 1. Clean repo without .beads passes cleanly.
	res, err := RunPrePush(context.Background(), repo, false, 10*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(clean): %v", err)
	}
	if !res.Passed || len(res.Warnings) != 0 {
		t.Fatalf("expected clean pre-push pass, got %+v", res)
	}

	// 2. Dirty .beads + passing br dep cycles -> warning, Passed=true.
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeBR := filepath.Join(binDir, "br")
	if err := os.WriteFile(fakeBR, []byte(`#!/bin/sh
if [ "$1 $2" = "dep cycles" ] && [ -n "$FAKE_CYCLE" ]; then
  echo "cycle: bd-1 -> bd-2 -> bd-1" >&2
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err = RunPrePush(context.Background(), repo, true, 10*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(dirty beads): %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected Passed=true on dirty .beads without cycles, got %+v", res)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], ".beads") {
		t.Fatalf("expected dirty .beads warning, got %+v", res.Warnings)
	}

	// 3. Failing br dep cycles -> Passed=false with BlockReason.
	t.Setenv("FAKE_CYCLE", "1")
	res, err = RunPrePush(context.Background(), repo, true, 10*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(cycles): %v", err)
	}
	if res.Passed || !strings.Contains(res.BlockReason, "bd-1 -> bd-2 -> bd-1") {
		t.Fatalf("expected cycle failure, got %+v", res)
	}
}

func TestRunPrePush_MainOnlyEnforcement(t *testing.T) {
	repo := initTempGitRepoWithCommit(t)
	runGit(t, repo, "branch", "-M", "main")
	t.Setenv("NTM_MAIN_ONLY", "1")

	// On main -> passes.
	res, err := RunPrePush(context.Background(), repo, false, 5*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(main): %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected push allowed on main, got %+v", res)
	}

	// Detached HEAD -> passes (branch == "HEAD").
	headSHA := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", headSHA)
	res, err = RunPrePush(context.Background(), repo, false, 5*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(detached HEAD): %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected push allowed on detached HEAD, got %+v", res)
	}

	// Feature branch -> blocked.
	runGit(t, repo, "checkout", "-b", "feature/wip")
	res, err = RunPrePush(context.Background(), repo, false, 5*time.Second)
	if err != nil {
		t.Fatalf("RunPrePush(feature): %v", err)
	}
	if res.Passed || !strings.Contains(res.BlockReason, "NTM_MAIN_ONLY=1") {
		t.Fatalf("expected feature branch push blocked, got %+v", res)
	}
}

func TestRunCommitMsg_ValidEmptyConflictAndFallback(t *testing.T) {
	repo := initTempGitRepoWithCommit(t)

	// 1. Fallback to <git-dir>/COMMIT_EDITMSG when msgFile == "".
	editMsgPath := filepath.Join(repo, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(editMsgPath, []byte("feat(hooks): wire commit-msg check\n\n# git comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunCommitMsg(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("RunCommitMsg(fallback): %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected valid fallback commit message to pass, got %+v", res)
	}

	// 2. Comment-only + whitespace fails as empty.
	msgPath := filepath.Join(t.TempDir(), "MSG")
	if err := os.WriteFile(msgPath, []byte("# Please enter the commit message\n   \n# Another comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = RunCommitMsg(context.Background(), repo, msgPath)
	if err != nil {
		t.Fatalf("RunCommitMsg(empty): %v", err)
	}
	if res.Passed || !strings.Contains(res.BlockReason, "empty") {
		t.Fatalf("expected empty message failure, got %+v", res)
	}

	// 3. Merge-conflict markers fail.
	for _, marker := range []string{
		"feat: merge fix\n\n<<<<<<< HEAD\nours\n",
		"feat: merge fix\n\n=======\ntheirs\n",
		"feat: merge fix\n\n>>>>>>> branch-a\n",
	} {
		if err := os.WriteFile(msgPath, []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err = RunCommitMsg(context.Background(), repo, msgPath)
		if err != nil {
			t.Fatalf("RunCommitMsg(conflict): %v", err)
		}
		if res.Passed || !strings.Contains(res.BlockReason, "merge-conflict") {
			t.Fatalf("expected conflict marker failure for %q, got %+v", marker, res)
		}
	}
}

func TestRunCommitMsg_SubjectLengthAndConventionalCommits(t *testing.T) {
	repo := initTempGitRepoWithCommit(t)
	msgPath := filepath.Join(t.TempDir(), "MSG")

	longSubject := "feat(hooks): " + strings.Repeat("a", 120)
	if err := os.WriteFile(msgPath, []byte(longSubject+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without NTM_CONVENTIONAL_COMMITS=1 -> warns on >120 chars, Passed=true.
	t.Setenv("NTM_CONVENTIONAL_COMMITS", "0")
	res, err := RunCommitMsg(context.Background(), repo, msgPath)
	if err != nil {
		t.Fatalf("RunCommitMsg(long warn): %v", err)
	}
	if !res.Passed || len(res.Warnings) != 1 {
		t.Fatalf("expected warning without block on >120 subject, got %+v", res)
	}

	// With NTM_CONVENTIONAL_COMMITS=1 -> >120 chars fails.
	t.Setenv("NTM_CONVENTIONAL_COMMITS", "1")
	res, err = RunCommitMsg(context.Background(), repo, msgPath)
	if err != nil {
		t.Fatalf("RunCommitMsg(long strict): %v", err)
	}
	if res.Passed || !strings.Contains(res.BlockReason, "exceeds 120") {
		t.Fatalf("expected strict failure on >120 subject, got %+v", res)
	}

	// Non-conventional prefix fails under NTM_CONVENTIONAL_COMMITS=1.
	if err := os.WriteFile(msgPath, []byte("Update README without prefix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = RunCommitMsg(context.Background(), repo, msgPath)
	if err != nil {
		t.Fatalf("RunCommitMsg(non-conventional): %v", err)
	}
	if res.Passed || !strings.Contains(res.BlockReason, "Conventional Commits") {
		t.Fatalf("expected conventional commit failure, got %+v", res)
	}

	// Valid conventional commit passes under NTM_CONVENTIONAL_COMMITS=1.
	if err := os.WriteFile(msgPath, []byte("feat(hooks,attention): implement all 5 git hooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = RunCommitMsg(context.Background(), repo, msgPath)
	if err != nil {
		t.Fatalf("RunCommitMsg(valid conventional): %v", err)
	}
	if !res.Passed || len(res.Warnings) != 0 {
		t.Fatalf("expected valid conventional commit to pass, got %+v", res)
	}
}

func TestRunPostCommitAndPostCheckout(t *testing.T) {
	repo := initTempGitRepoWithCommit(t)

	// Clean repo -> no warnings, Passed=true for both.
	pc, err := RunPostCommit(context.Background(), repo)
	if err != nil || !pc.Passed || len(pc.Warnings) != 0 {
		t.Fatalf("RunPostCommit(clean) = %+v, err=%v", pc, err)
	}
	co, err := RunPostCheckout(context.Background(), repo)
	if err != nil || !co.Passed || len(co.Warnings) != 0 {
		t.Fatalf("RunPostCheckout(clean) = %+v, err=%v", co, err)
	}

	// Dirty .beads -> Passed=true with expected warnings for both.
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"bd-1\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pc, err = RunPostCommit(context.Background(), repo)
	if err != nil {
		t.Fatalf("RunPostCommit(dirty): %v", err)
	}
	if !pc.Passed || len(pc.Warnings) != 1 || pc.Warnings[0] != "uncommitted .beads changes remain after commit; run 'br sync --flush-only' if beads changed" {
		t.Fatalf("unexpected RunPostCommit(dirty): %+v", pc)
	}

	co, err = RunPostCheckout(context.Background(), repo)
	if err != nil {
		t.Fatalf("RunPostCheckout(dirty): %v", err)
	}
	if !co.Passed || len(co.Warnings) != 1 || co.Warnings[0] != ".beads has uncommitted changes. Run: br sync --flush-only and commit .beads/" {
		t.Fatalf("unexpected RunPostCheckout(dirty): %+v", co)
	}
}
