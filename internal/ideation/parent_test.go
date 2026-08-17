package ideation

// parent_test.go pins the three-branch parent resolution contract for
// queue-dry ideation (reality-bridge W1 lane B1): explicit flag > exactly one
// open epic in the TARGET project's beads DB > no parent, never guessing
// among multiple candidates.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const parentEpicListKey = "br list --status open --type epic --limit 0 --json --no-auto-flush --no-auto-import"

func newParentTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "beads.db"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write beads.db: %v", err)
	}
	return dir
}

func TestResolveRoadmapParentExplicitFlagWins(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir:     newParentTestWorkspace(t),
		ExplicitParent: "bd-flag1",
		Runner: fakeRunner{err: map[string]error{
			parentEpicListKey: errors.New("runner must not be called when --parent is explicit"),
		}},
	})
	if got.ParentID != "bd-flag1" || got.Source != ParentSourceFlag {
		t.Fatalf("resolution=%+v, want explicit flag to win", got)
	}
	if got.Warning != "" {
		t.Fatalf("warning=%q, want none for explicit flag", got.Warning)
	}
}

func TestResolveRoadmapParentSingleOpenEpicDetected(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir: newParentTestWorkspace(t),
		Runner: fakeRunner{outputs: map[string][]byte{
			parentEpicListKey: []byte(`[{"id":"bd-epic1","title":"Only epic","status":"open","issue_type":"epic"}]`),
		}},
	})
	if got.ParentID != "bd-epic1" || got.Source != ParentSourceDetectedEpic {
		t.Fatalf("resolution=%+v, want the single open epic detected", got)
	}
	if got.Warning != "" {
		t.Fatalf("warning=%q, want none for unambiguous detection", got.Warning)
	}
}

func TestResolveRoadmapParentZeroEpicsMeansNoParent(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir: newParentTestWorkspace(t),
		Runner:     fakeRunner{outputs: map[string][]byte{parentEpicListKey: []byte(`[]`)}},
	})
	if got.ParentID != "" || got.Source != ParentSourceNone {
		t.Fatalf("resolution=%+v, want no parent when zero open epics exist", got)
	}
}

func TestResolveRoadmapParentMultipleEpicsIsAmbiguousNeverGuesses(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir: newParentTestWorkspace(t),
		Runner: fakeRunner{outputs: map[string][]byte{
			parentEpicListKey: []byte(`[{"id":"bd-epic2","status":"open"},{"id":"bd-epic1","status":"open"}]`),
		}},
	})
	if got.ParentID != "" || got.Source != ParentSourceAmbiguous {
		t.Fatalf("resolution=%+v, want ambiguous with no parent chosen", got)
	}
	if len(got.Candidates) != 2 || got.Candidates[0] != "bd-epic1" || got.Candidates[1] != "bd-epic2" {
		t.Fatalf("candidates=%v, want both epics named deterministically", got.Candidates)
	}
	for _, want := range []string{"bd-epic1", "bd-epic2", "--parent"} {
		if !strings.Contains(got.Warning, want) {
			t.Fatalf("warning=%q, want it to name %q", got.Warning, want)
		}
	}
}

func TestResolveRoadmapParentMissingBeadsDBWarnsWithoutRunningBR(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir: t.TempDir(),
		Runner: fakeRunner{err: map[string]error{
			parentEpicListKey: errors.New("runner must not be called without a beads database"),
		}},
	})
	if got.ParentID != "" || got.Source != ParentSourceNone {
		t.Fatalf("resolution=%+v, want none for missing beads DB", got)
	}
	if !strings.Contains(got.Warning, "beads database") || !strings.Contains(got.Warning, "--parent") {
		t.Fatalf("warning=%q, want missing-database explanation with --parent hint", got.Warning)
	}
}

func TestResolveRoadmapParentListFailureWarnsInsteadOfGuessing(t *testing.T) {
	got := ResolveRoadmapParent(context.Background(), ParentResolutionOptions{
		ProjectDir: newParentTestWorkspace(t),
		Runner:     fakeRunner{err: map[string]error{parentEpicListKey: errors.New("br exploded")}},
	})
	if got.ParentID != "" || got.Source != ParentSourceNone {
		t.Fatalf("resolution=%+v, want none when the epic listing fails", got)
	}
	if !strings.Contains(got.Warning, "br exploded") {
		t.Fatalf("warning=%q, want the underlying error surfaced", got.Warning)
	}
}
