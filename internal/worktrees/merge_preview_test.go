package worktrees

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every merge assessment below uses real Git in disposable repositories, not
// fabricated merge-tree output. The parser-only cases cover invalid wire data.
func mergePreviewGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mergePreviewWrite(t *testing.T, dir, path, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newMergePreviewFixture(t *testing.T) (*WorktreeManager, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "absent-config"))
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_AUTHOR_NAME", "NTM Merge Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "ntm-test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "NTM Merge Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "ntm-test@example.invalid")
	mergePreviewGit(t, root, "init", "-b", "main")
	mergePreviewGit(t, root, "config", "commit.gpgsign", "false")
	mergePreviewWrite(t, root, ".gitignore", ".ntm/\n")
	mergePreviewWrite(t, root, "shared.txt", "base\n")
	mergePreviewGit(t, root, "add", ".gitignore", "shared.txt")
	mergePreviewGit(t, root, "commit", "-m", "base")
	source := filepath.Join(root, ".ntm", "worktrees", "merge-test", "agent")
	mergePreviewGit(t, root, "worktree", "add", "-b", "ntm/merge-test/agent", source)
	return NewManager(root, "merge-test"), root, source
}

func mergePreviewCommit(t *testing.T, dir, path, text string) {
	t.Helper()
	mergePreviewWrite(t, dir, path, text)
	mergePreviewGit(t, dir, "add", "--", path)
	mergePreviewGit(t, dir, "commit", "-m", "change "+path)
}

type mergeCheckoutSnapshot struct {
	head, branch, status string
	index                []byte
}

func snapshotMergeCheckout(t *testing.T, dir string) mergeCheckoutSnapshot {
	t.Helper()
	indexPath := mergePreviewGit(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	return mergeCheckoutSnapshot{
		head:   mergePreviewGit(t, dir, "rev-parse", "HEAD"),
		branch: mergePreviewGit(t, dir, "symbolic-ref", "HEAD"),
		status: mergePreviewGit(t, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z"),
		index:  index,
	}
}

func TestMergePreviewCleanPreservesBothCheckouts(t *testing.T) {
	manager, root, source := newMergePreviewFixture(t)
	mergePreviewCommit(t, source, "agent.txt", "agent work\n")
	mergePreviewCommit(t, root, "primary.txt", "primary work\n")
	beforeTarget, beforeSource := snapshotMergeCheckout(t, root), snapshotMergeCheckout(t, source)
	plan, err := manager.PreviewMerge(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanMerge || plan.HasConflicts || plan.AlreadyMerged || plan.SourceCommits != 1 || plan.SourceCommit != beforeSource.head || plan.TargetCommit != beforeTarget.head {
		t.Fatalf("incorrect merge evidence: %+v", plan)
	}
	if len(plan.Conflicts) != 0 || len(plan.Blockers) != 0 || plan.MergeTree == "" {
		t.Fatalf("missing tree or false blockers: %+v", plan)
	}
	if files := mergePreviewGit(t, root, "ls-tree", "--name-only", plan.MergeTree); !strings.Contains(files, "agent.txt") || !strings.Contains(files, "primary.txt") {
		t.Fatalf("preview tree lost one side: %s", files)
	}
	if !reflect.DeepEqual(snapshotMergeCheckout(t, root), beforeTarget) || !reflect.DeepEqual(snapshotMergeCheckout(t, source), beforeSource) {
		t.Fatal("preview changed a checkout, index, or ref")
	}
}

func TestMergePreviewReportsRealTextAndBinaryConflicts(t *testing.T) {
	for _, tc := range []struct{ name, path, ours, theirs string }{
		{"text", "shared.txt", "primary edit\n", "agent edit\n"},
		{"binary", "binary.dat", "\x00primary\xff", "\x00agent\xfe"},
		{"quoted filename", "tab\tand\nnewline.txt", "primary\n", "agent\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			mergePreviewCommit(t, source, tc.path, tc.theirs)
			mergePreviewCommit(t, root, tc.path, tc.ours)
			beforeTarget, beforeSource := snapshotMergeCheckout(t, root), snapshotMergeCheckout(t, source)
			plan, err := manager.PreviewMerge(context.Background(), "agent")
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanMerge || !plan.HasConflicts || !reflect.DeepEqual(plan.Conflicts, []string{tc.path}) || !reflect.DeepEqual(plan.Blockers, []string{"merge_conflicts"}) {
				t.Fatalf("conflict was missed or its path corrupted: %+v", plan)
			}
			if !reflect.DeepEqual(snapshotMergeCheckout(t, root), beforeTarget) || !reflect.DeepEqual(snapshotMergeCheckout(t, source), beforeSource) {
				t.Fatal("conflict preview left a merge in progress or changed checkout data")
			}
			content, err := os.ReadFile(filepath.Join(root, tc.path))
			if err != nil || !bytes.Equal(content, []byte(tc.ours)) {
				t.Fatal("conflict markers or agent content reached the primary checkout")
			}
		})
	}
}

func TestMergePreviewRequiresCleanMatchingCheckouts(t *testing.T) {
	for _, tc := range []struct{ name, blocker string }{
		{"source dirty", "source_has_uncommitted_changes"},
		{"target dirty", "target_has_uncommitted_changes"},
		{"target branch", "target_branch_not_checked_out"},
		{"target merge in progress", "target_operation_in_progress:MERGE_HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			mergePreviewCommit(t, source, "agent.txt", "agent work\n")
			switch tc.name {
			case "source dirty":
				mergePreviewWrite(t, source, "draft.txt", "unsaved work")
			case "target dirty":
				mergePreviewWrite(t, root, "draft.txt", "human work")
			case "target branch":
				mergePreviewGit(t, root, "checkout", "-b", "human-work")
			case "target merge in progress":
				mergePreviewWrite(t, root, filepath.Join(".git", "MERGE_HEAD"), mergePreviewGit(t, source, "rev-parse", "HEAD")+"\n")
			}
			before := snapshotMergeCheckout(t, root)
			plan, err := manager.PreviewMerge(context.Background(), "agent")
			if err != nil {
				t.Fatal(err)
			}
			if plan.CanMerge || !strings.Contains(strings.Join(plan.Blockers, "|"), tc.blocker) {
				t.Fatalf("unsafe apply was approved: %+v", plan)
			}
			if !reflect.DeepEqual(snapshotMergeCheckout(t, root), before) {
				t.Fatal("preview changed human checkout state")
			}
		})
	}
}

func TestMergePreviewResolvesActualDefaultAndAlreadyMerged(t *testing.T) {
	manager, root, _ := newMergePreviewFixture(t)
	mergePreviewGit(t, root, "branch", "-m", "trunk")
	mergePreviewGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	plan, err := manager.PreviewMerge(context.Background(), "agent")
	if err != nil || plan == nil || plan.TargetBranch != "trunk" || !plan.AlreadyMerged || !plan.CanMerge {
		t.Fatalf("wrong default branch/no-op result: %+v %v", plan, err)
	}
}

func TestMergePreviewRefusesMissingDetachedOrForeignSource(t *testing.T) {
	for _, kind := range []string{"missing", "detached", "different branch", "foreign repository"} {
		t.Run(kind, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			agent := "agent"
			switch kind {
			case "missing":
				agent = "absent"
			case "detached":
				mergePreviewGit(t, source, "checkout", "--detach")
			case "different branch":
				mergePreviewGit(t, source, "checkout", "-b", "other-agent")
			case "foreign repository":
				foreign := t.TempDir()
				mergePreviewGit(t, foreign, "init", "-b", "ntm/merge-test/agent")
				mergePreviewCommit(t, foreign, "foreign.txt", "other project")
				mergePreviewWrite(t, source, ".git", "gitdir: "+filepath.Join(foreign, ".git")+"\n")
			}
			before := snapshotMergeCheckout(t, root)
			if plan, err := manager.PreviewMerge(context.Background(), agent); err == nil || plan != nil {
				t.Fatalf("invalid source accepted: %+v %v", plan, err)
			}
			if !reflect.DeepEqual(snapshotMergeCheckout(t, root), before) {
				t.Fatal("refused source changed target")
			}
		})
	}
}

func TestMergePreviewCancellationAndSnapshotChange(t *testing.T) {
	manager, root, source := newMergePreviewFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if plan, err := manager.PreviewMerge(ctx, "agent"); plan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preview: %+v %v", plan, err)
	}
	if plan, err := manager.PreviewMerge(nil, "agent"); plan != nil || err == nil {
		t.Fatal("nil context accepted")
	}
	plan, err := manager.PreviewMerge(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	mergePreviewCommit(t, source, "later.txt", "new agent generation")
	if err := verifyMergeSnapshot(context.Background(), plan); !errors.Is(err, ErrMergeSnapshotChanged) {
		t.Fatalf("stale source accepted: %v", err)
	}
	plan, err = manager.PreviewMerge(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	mergePreviewCommit(t, root, "later-primary.txt", "new target generation")
	if err := verifyMergeSnapshot(context.Background(), plan); !errors.Is(err, ErrMergeSnapshotChanged) {
		t.Fatalf("stale target accepted: %v", err)
	}
}

func TestMergePreviewParserRejectsUntrustedOutput(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, raw := range []string{"", oid, oid + "\n", "not-an-oid\x00", oid + "\x00\x00", oid + "\x00conflict\x00\x00unexpected\x00"} {
		if _, _, err := parseMergeTreePreview([]byte(raw)); err == nil {
			t.Fatalf("malformed preview accepted: %q", raw)
		}
	}
	for _, oid := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		tree, paths, err := parseMergeTreePreview([]byte(oid + "\x00"))
		if err != nil || tree != oid || paths == nil || len(paths) != 0 {
			t.Fatalf("valid empty path list rejected: %q %v %v", tree, paths, err)
		}
	}
}
