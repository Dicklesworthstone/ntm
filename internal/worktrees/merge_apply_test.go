package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMergeBackAppliesPinnedTreeAndParents(t *testing.T) {
	manager, root, source := newMergePreviewFixture(t)
	mergePreviewCommit(t, source, "agent.txt", "agent work\n")
	mergePreviewCommit(t, root, "primary.txt", "primary work\n")
	// These inherited settings must not silently turn a successful merge into
	// an uncommitted index or a squash that drops the agent's history.
	mergePreviewGit(t, root, "config", "branch.main.mergeOptions", "--squash --no-commit -s ours")
	plan, err := manager.PreviewMerge(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	beforeSource := snapshotMergeCheckout(t, source)
	if err := manager.MergeBack(context.Background(), "agent", MergeOptions{ExpectedSource: plan.SourceCommit, ExpectedTarget: plan.TargetCommit}); err != nil {
		t.Fatal(err)
	}
	parents := mergePreviewGit(t, root, "show", "-s", "--format=%P", "HEAD")
	if parents != plan.TargetCommit+" "+plan.SourceCommit || mergePreviewGit(t, root, "rev-parse", "HEAD^{tree}") != plan.MergeTree {
		t.Fatalf("applied merge differs from preview: parents=%s plan=%+v", parents, plan)
	}
	if !reflect.DeepEqual(snapshotMergeCheckout(t, source), beforeSource) {
		t.Fatal("integration modified the agent worktree")
	}
	for _, file := range []string{"agent.txt", "primary.txt"} {
		if _, err := os.Stat(filepath.Join(root, file)); err != nil {
			t.Fatal(err)
		}
	}
	firstMerge := mergePreviewGit(t, root, "rev-parse", "HEAD")
	if err := manager.MergeBack(context.Background(), "agent"); err != nil {
		t.Fatalf("already-merged retry: %v", err)
	}
	if mergePreviewGit(t, root, "rev-parse", "HEAD") != firstMerge {
		t.Fatal("retry created another merge commit")
	}
}

func TestMergeBackRefusesUnsafeCheckoutsWithoutSwitching(t *testing.T) {
	for _, kind := range []string{"source draft", "target draft", "target branch", "pending merge"} {
		t.Run(kind, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			mergePreviewCommit(t, source, "agent.txt", "agent work\n")
			switch kind {
			case "source draft":
				mergePreviewWrite(t, source, "draft.txt", "agent draft")
			case "target draft":
				mergePreviewWrite(t, root, "draft.txt", "operator draft")
			case "target branch":
				mergePreviewGit(t, root, "checkout", "-b", "operator-work")
			case "pending merge":
				mergePreviewWrite(t, root, filepath.Join(".git", "MERGE_HEAD"), mergePreviewGit(t, source, "rev-parse", "HEAD")+"\n")
			}
			beforeTarget, beforeSource := snapshotMergeCheckout(t, root), snapshotMergeCheckout(t, source)
			if err := manager.MergeBack(context.Background(), "agent"); !errors.Is(err, ErrMergeUnsafe) {
				t.Fatalf("unsafe merge did not fail closed: %v", err)
			}
			if !reflect.DeepEqual(snapshotMergeCheckout(t, root), beforeTarget) || !reflect.DeepEqual(snapshotMergeCheckout(t, source), beforeSource) {
				t.Fatal("refused merge changed checkout files, branch, or index")
			}
		})
	}
}

func TestMergeBackRejectsStaleExpectations(t *testing.T) {
	for _, which := range []string{"source", "target"} {
		t.Run(which, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			mergePreviewCommit(t, source, "agent.txt", "agent work\n")
			plan, err := manager.PreviewMerge(context.Background(), "agent")
			if err != nil {
				t.Fatal(err)
			}
			dir := source
			if which == "target" {
				dir = root
			}
			mergePreviewCommit(t, dir, "later.txt", "unreviewed generation")
			before := snapshotMergeCheckout(t, root)
			err = manager.MergeBack(context.Background(), "agent", MergeOptions{ExpectedSource: plan.SourceCommit, ExpectedTarget: plan.TargetCommit})
			if !errors.Is(err, ErrMergeSnapshotChanged) || !reflect.DeepEqual(snapshotMergeCheckout(t, root), before) {
				t.Fatalf("stale %s preview was applied: %v", which, err)
			}
		})
	}
}

func TestMergeBackPreservesIgnoredLocalFiles(t *testing.T) {
	for _, tc := range []struct{ name, incoming, local, exclude string }{
		{"file", "local.data", "local.data", "local.data\n"},
		{"directory replaced by file", "cache", "cache/private.data", "cache\n"},
		{"file replaced by directory", "cache/incoming.txt", "cache", "cache\n"},
		{"case-insensitive Git policy", "LOCAL.DATA", "local.data", "local.data\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, root, source := newMergePreviewFixture(t)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(source, tc.incoming)), 0o700); err != nil {
				t.Fatal(err)
			}
			mergePreviewCommit(t, source, tc.incoming, "incoming agent file")
			mergePreviewWrite(t, root, filepath.Join(".git", "info", "exclude"), tc.exclude)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, tc.local)), 0o700); err != nil {
				t.Fatal(err)
			}
			mergePreviewWrite(t, root, tc.local, "irreplaceable ignored local data")
			if tc.name == "case-insensitive Git policy" {
				mergePreviewGit(t, root, "config", "core.ignorecase", "true")
			}
			beforeTarget, beforeSource := snapshotMergeCheckout(t, root), snapshotMergeCheckout(t, source)
			plan, err := manager.PreviewMerge(context.Background(), "agent")
			if err != nil || plan == nil || plan.CanMerge || !reflect.DeepEqual(plan.OverwriteRisks, []string{tc.local}) {
				t.Fatalf("preview missed ignored-file overwrite: %+v %v", plan, err)
			}
			if err := manager.MergeBack(context.Background(), "agent"); !errors.Is(err, ErrMergeUnsafe) {
				t.Fatalf("merge did not refuse ignored data loss before mutation: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(root, tc.local))
			if err != nil || string(data) != "irreplaceable ignored local data" || !reflect.DeepEqual(snapshotMergeCheckout(t, root), beforeTarget) || !reflect.DeepEqual(snapshotMergeCheckout(t, source), beforeSource) {
				t.Fatal("ignored data or checkout state was changed by a refused merge")
			}
		})
	}
}

func TestMergeBackIgnoredCollisionRecheckAndUnrelatedCache(t *testing.T) {
	manager, root, source := newMergePreviewFixture(t)
	mergePreviewCommit(t, source, "incoming.data", "agent file")
	mergePreviewWrite(t, root, filepath.Join(".git", "info", "exclude"), "*.data\n")
	mergePreviewWrite(t, root, "unrelated.data", "keep this cache")
	plan, err := manager.PreviewMerge(context.Background(), "agent")
	if err != nil || !plan.CanMerge {
		t.Fatalf("unrelated ignored file blocked integration: %+v %v", plan, err)
	}
	// The write collision must be rechecked after preview, not trusted from
	// its earlier assessment. This directly exercises the mutation boundary.
	mergePreviewWrite(t, root, "incoming.data", "new ignored operator file")
	before := snapshotMergeCheckout(t, root)
	if err := applyMergePreview(context.Background(), plan); !errors.Is(err, ErrMergeUnsafe) {
		t.Fatalf("apply trusted a stale ignored-file assessment: %v", err)
	}
	if !reflect.DeepEqual(snapshotMergeCheckout(t, root), before) {
		t.Fatal("collision recheck modified the checkout")
	}
}

func TestMergeBackHookRefusalRetainsRecoveryState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git hook fixture")
	}
	manager, root, source := newMergePreviewFixture(t)
	mergePreviewCommit(t, source, "agent.txt", "agent work\n")
	before := mergePreviewGit(t, root, "rev-parse", "HEAD")
	hook := filepath.Join(root, ".git", "hooks", "pre-merge-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := manager.MergeBack(context.Background(), "agent")
	if !errors.Is(err, ErrMergeOutcomeUnverified) || !strings.Contains(err.Error(), "no rollback") {
		t.Fatalf("hook refusal lost recovery guidance: %v", err)
	}
	if mergePreviewGit(t, root, "rev-parse", "HEAD") != before {
		t.Fatal("a rejected hook was bypassed")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("recovery state was silently aborted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err != nil {
		t.Fatalf("prepared merge contents were silently reset: %v", err)
	}
}

func TestMergeBackDoesNotAcknowledgeHookChangedTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git hook fixture")
	}
	manager, root, source := newMergePreviewFixture(t)
	mergePreviewCommit(t, source, "agent.txt", "agent work\n")
	before := mergePreviewGit(t, root, "rev-parse", "HEAD")
	// A hook can use Git plumbing to rewrite the just-created commit even
	// while MERGE_HEAD is still present. Preserve both parents but change the
	// tree so the postcondition must detect more than successful ancestry.
	hook := filepath.Join(root, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hook, []byte(`#!/bin/sh
set -e
printf 'hook changed the merge' > hook.txt
git add -- hook.txt
tree=$(git write-tree)
old=$(git rev-parse HEAD)
parents=$(git show -s --format=%P HEAD)
set -- $parents
new=$(printf 'hook rewrite\n' | git commit-tree "$tree" -p "$1" -p "$2")
git update-ref HEAD "$new" "$old"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	err := manager.MergeBack(context.Background(), "agent")
	if !errors.Is(err, ErrMergeOutcomeUnverified) || !strings.Contains(err.Error(), "tree and parents") {
		t.Fatalf("unplanned tree was acknowledged as the reviewed merge: %v", err)
	}
	if mergePreviewGit(t, root, "rev-parse", "HEAD") == before {
		t.Fatal("unexpected completed commit was automatically rolled back")
	}
	data, err := os.ReadFile(filepath.Join(root, "hook.txt"))
	if err != nil || string(data) != "hook changed the merge" {
		t.Fatal("postcondition failure erased hook output instead of retaining evidence")
	}
}

func TestMergeExpectationsRequireCompleteIdentities(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, input := range [][]MergeOptions{
		{{ExpectedSource: sha}}, {{ExpectedTarget: sha}},
		{{ExpectedSource: "main", ExpectedTarget: sha}},
		{{ExpectedSource: sha, ExpectedTarget: "--help"}},
		{{}, {}},
	} {
		if _, err := mergeOptions(input); err == nil {
			t.Fatalf("invalid expectations accepted: %+v", input)
		}
	}
}
