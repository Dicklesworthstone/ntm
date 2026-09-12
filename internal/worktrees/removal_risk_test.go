package worktrees

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RemoveWorktree force-deletes both the worktree and the branch, so the only
// thing standing between an operator and destroyed work is an accurate
// assessment beforehand. These tests exercise it against a real git repo.

func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

func newRiskFixture(t *testing.T) (*WorktreeManager, *WorktreeInfo) {
	t.Helper()
	repo := setupWorktreeGitRepo(t)
	m := NewManager(repo, "risk-session")
	info, err := m.CreateForAgent(t.Context(), "cc-1")
	if err != nil {
		t.Skipf("CreateForAgent unavailable in this environment: %v", err)
	}
	return m, info
}

func TestAssessRemovalRiskCleanWorktree(t *testing.T) {
	m, _ := newRiskFixture(t)

	risk, err := m.AssessRemovalRisk(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("AssessRemovalRisk: %v", err)
	}
	if risk.HasWork() {
		t.Errorf("clean worktree reported work at risk: %+v", risk)
	}
}

func TestAssessRemovalRiskCountsUncommittedFiles(t *testing.T) {
	m, info := newRiskFixture(t)

	for _, name := range []string{"untracked.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(info.Path, name), []byte("work"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	risk, err := m.AssessRemovalRisk(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("AssessRemovalRisk: %v", err)
	}
	if risk.UncommittedFiles != 2 {
		t.Errorf("UncommittedFiles = %d, want 2 (%+v)", risk.UncommittedFiles, risk)
	}
	if !risk.HasWork() {
		t.Error("HasWork() = false with uncommitted files present")
	}
}

// Ignored files are not work the operator would mourn, and counting them would
// make the prompt cry wolf on any repo with build output.
func TestAssessRemovalRiskIgnoresIgnoredFiles(t *testing.T) {
	m, info := newRiskFixture(t)

	if err := os.WriteFile(filepath.Join(info.Path, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	gitInDir(t, info.Path, "add", ".gitignore")
	gitInDir(t, info.Path, "-c", "user.email=test@test.com", "-c", "user.name=Test",
		"commit", "-m", "ignore build")
	if err := os.MkdirAll(filepath.Join(info.Path, "build"), 0o755); err != nil {
		t.Fatalf("mkdir build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "build", "artifact.o"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	risk, err := m.AssessRemovalRisk(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("AssessRemovalRisk: %v", err)
	}
	if risk.UncommittedFiles != 0 {
		t.Errorf("UncommittedFiles = %d, want 0 (ignored files must not count): %+v", risk.UncommittedFiles, risk)
	}
}

// `git status --porcelain` without -uall collapses an untracked directory into a
// single entry, which would announce a worktree holding many new files as "1
// file with uncommitted changes" right before destroying all of them.
func TestAssessRemovalRiskCountsFilesInsideUntrackedDirectories(t *testing.T) {
	m, info := newRiskFixture(t)

	nested := filepath.Join(info.Path, "newpkg", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte("package deep"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	risk, err := m.AssessRemovalRisk(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("AssessRemovalRisk: %v", err)
	}
	if risk.UncommittedFiles != 3 {
		t.Errorf("UncommittedFiles = %d, want 3 (each file inside an untracked directory must count): %+v",
			risk.UncommittedFiles, risk)
	}
}

func TestAssessRemovalRiskCountsUnmergedCommits(t *testing.T) {
	m, info := newRiskFixture(t)

	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(info.Path, name), []byte("work"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		gitInDir(t, info.Path, "add", name)
		gitInDir(t, info.Path, "-c", "user.email=test@test.com", "-c", "user.name=Test",
			"commit", "-m", "add "+name)
	}

	risk, err := m.AssessRemovalRisk(t.Context(), "cc-1")
	if err != nil {
		t.Fatalf("AssessRemovalRisk: %v", err)
	}
	if risk.UnmergedCommits != 2 {
		t.Errorf("UnmergedCommits = %d, want 2 (%+v)", risk.UnmergedCommits, risk)
	}
	if risk.UncommittedFiles != 0 {
		t.Errorf("UncommittedFiles = %d, want 0 after committing (%+v)", risk.UncommittedFiles, risk)
	}
	if !risk.HasWork() {
		t.Error("HasWork() = false with unmerged commits present")
	}
}

func TestAssessRemovalRiskNilContext(t *testing.T) {
	m := NewManager(t.TempDir(), "risk-session")
	//nolint:staticcheck // deliberately passing a nil context to assert the guard
	if _, err := m.AssessRemovalRisk(nil, "cc-1"); err == nil {
		t.Error("expected an error for a nil context, got nil")
	}
}

// A missing worktree must surface an error, never a zero RemovalRisk that a
// caller would render as "nothing at risk".
func TestAssessRemovalRiskMissingWorktreeErrors(t *testing.T) {
	repo := setupWorktreeGitRepo(t)
	m := NewManager(repo, "risk-session")

	risk, err := m.AssessRemovalRisk(t.Context(), "never-created")
	if err == nil {
		t.Errorf("expected an error for a worktree that does not exist, got risk=%+v", risk)
	}
}
