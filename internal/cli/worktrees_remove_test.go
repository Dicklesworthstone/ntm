package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func testWorktreeInfo() *worktrees.WorktreeInfo {
	return &worktrees.WorktreeInfo{
		AgentName:  "cc-1",
		Path:       "/proj/.ntm/worktrees/sess/cc-1",
		BranchName: "agent/cc-1/sess",
		Created:    true,
	}
}

func TestDescribeWorktreeRemovalAlwaysNamesPathAndBranch(t *testing.T) {
	got := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{}, nil)
	for _, want := range []string{"/proj/.ntm/worktrees/sess/cc-1", "agent/cc-1/sess"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
}

func TestDescribeWorktreeRemovalCleanSaysNothingAtRisk(t *testing.T) {
	got := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{}, nil)
	if !strings.Contains(got, "No uncommitted changes and no unmerged commits") {
		t.Errorf("clean worktree should say so, got:\n%s", got)
	}
	if strings.Contains(got, "permanently destroys") {
		t.Errorf("clean worktree must not claim destruction, got:\n%s", got)
	}
}

func TestDescribeWorktreeRemovalReportsCounts(t *testing.T) {
	got := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{
		UncommittedFiles: 1,
		UnmergedCommits:  3,
	}, nil)

	for _, want := range []string{"permanently destroys", "1 file with uncommitted changes", "3 commits not merged"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
}

func TestDescribeWorktreeRemovalSingularAndPlural(t *testing.T) {
	one := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{UncommittedFiles: 1, UnmergedCommits: 1}, nil)
	if !strings.Contains(one, "1 file ") || !strings.Contains(one, "1 commit ") {
		t.Errorf("singular forms wrong:\n%s", one)
	}
	many := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{UncommittedFiles: 2, UnmergedCommits: 2}, nil)
	if !strings.Contains(many, "2 files ") || !strings.Contains(many, "2 commits ") {
		t.Errorf("plural forms wrong:\n%s", many)
	}
}

// The dangerous failure mode: an assessment error renders as a zero RemovalRisk,
// so the prompt would reassure the operator that nothing is at risk right before
// force-deleting a branch it never managed to inspect.
func TestDescribeWorktreeRemovalErrorIsNotReportedAsSafe(t *testing.T) {
	got := describeWorktreeRemoval(testWorktreeInfo(), worktrees.RemovalRisk{}, errors.New("git exploded"))

	if strings.Contains(got, "No uncommitted changes") {
		t.Errorf("an assessment failure must not read as 'nothing at risk':\n%s", got)
	}
	if !strings.Contains(got, "Could not determine") || !strings.Contains(got, "git exploded") {
		t.Errorf("description should surface the assessment error:\n%s", got)
	}
	if !strings.Contains(got, "force-deleted") {
		t.Errorf("description should still warn that removal proceeds regardless:\n%s", got)
	}
}

func TestWorktreesRemoveHasForceFlag(t *testing.T) {
	cmd := newWorktreesRemoveCmd()
	flag := cmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("worktrees remove must expose --force to skip the confirmation prompt")
	}
	if flag.DefValue != "false" {
		t.Errorf("--force default = %q, want \"false\": confirmation must be opt-out, not opt-in", flag.DefValue)
	}
}
