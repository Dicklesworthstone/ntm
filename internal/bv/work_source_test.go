package bv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func sourceGuardFixture(t *testing.T) (string, string) {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, ".beads", "issues.jsonl")
	data := `{"id":"blocked","status":"open","issue_type":"task","dependencies":[{"depends_on_id":"busy","type":"blocks"}]}` + "\n" + `{"id":"busy","status":"in_progress","issue_type":"task"}` + "\n" + `{"id":"ready","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return project, path
}

func TestActionableSourceFiltersBeforeLimitAndDoesNotDispatchStaleWork(t *testing.T) {
	project, path := sourceGuardFixture(t)
	calls := 0
	collect := func(ctx context.Context, dir string, n int) ([]TriageRecommendation, error) {
		calls++
		if n != 0 {
			t.Fatalf("source filter received a prematurely truncated plan: %d", n)
		}
		return []TriageRecommendation{{ID: "blocked"}, {ID: "ready"}, {ID: "ready"}}, nil
	}
	got, err := actionableWithWorkSource(context.Background(), project, 1, collect)
	if err != nil || len(got) != 1 || got[0].ID != "ready" || calls != 1 {
		t.Fatalf("filtered plan: %+v %v calls=%d", got, err, calls)
	}
	dispatched := 0
	mutatingCollect := func(context.Context, string, int) ([]TriageRecommendation, error) {
		if err := os.WriteFile(path, []byte(`{"id":"ready","status":"closed"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return []TriageRecommendation{{ID: "ready"}}, nil
	}
	got, err = actionableWithWorkSource(context.Background(), project, 1, mutatingCollect)
	if err == nil {
		dispatched += len(got)
	}
	if !errors.Is(err, worksource.ErrStale) || len(got) != 0 || dispatched != 0 {
		t.Fatalf("mixed-source plan reached dispatch: %+v %v", got, err)
	}
}

func TestActionableSourceFailsBeforeToolReadsWhenExistingSourceIsInvalid(t *testing.T) {
	project, path := sourceGuardFixture(t)
	if err := os.WriteFile(path, []byte("invalid JSONL"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	collect := func(context.Context, string, int) ([]TriageRecommendation, error) { calls++; return nil, nil }
	if _, err := actionableWithWorkSource(context.Background(), project, 1, collect); !errors.Is(err, worksource.ErrStale) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unverified source triggered %d tool reads", calls)
	}
}

func TestActionableSourcePreservesDBOnlyOperation(t *testing.T) {
	calls := 0
	got, err := actionableWithWorkSource(context.Background(), t.TempDir(), 3, func(ctx context.Context, dir string, n int) ([]TriageRecommendation, error) {
		calls++
		if n != 3 {
			t.Fatalf("DB-only limit = %d", n)
		}
		return []TriageRecommendation{{ID: "database-only"}}, nil
	})
	if err != nil || calls != 1 || len(got) != 1 || got[0].ID != "database-only" {
		t.Fatalf("DB-only planning changed: %+v %v", got, err)
	}
}

func TestActionableSourceRetainsExclusionsAndNormalEmptyQueue(t *testing.T) {
	project, _ := sourceGuardFixture(t)
	got, err := actionableWithWorkSource(context.Background(), project, 0, func(context.Context, string, int) ([]TriageRecommendation, error) {
		return []TriageRecommendation{{ID: "blocked"}}, nil
	})
	var receipt *WorkEligibilityError
	if !errors.Is(err, ErrNoClaimableWork) || !errors.As(err, &receipt) || len(got) != 0 || len(receipt.Exclusions) != 1 || receipt.Exclusions[0].ID != "blocked" {
		t.Fatalf("missing exclusions: %+v %v", got, err)
	}
	got, err = actionableWithWorkSource(context.Background(), project, 0, func(context.Context, string, int) ([]TriageRecommendation, error) {
		return []TriageRecommendation{}, nil
	})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("normal queue exhaustion changed: %+v %v", got, err)
	}
}

func TestActionableSourcePublicPlannerExcludesBlockedJoin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project, path := sourceGuardFixture(t)
	bin := t.TempDir()
	bvScript := `#!/bin/sh
case "$*" in
  *--robot-plan*) printf '%s\n' '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"blocked","title":"Blocked join","status":"open","priority":1},{"id":"ready","title":"Ready task","status":"open","priority":2}]}],"summary":{"total_actionable":2}}}' ;;
  *--robot-triage*) printf '%s\n' '{"triage":{"recommendations":[{"id":"blocked","title":"Blocked join","status":"open","priority":1},{"id":"ready","title":"Ready task","status":"open","priority":2}]}}' ;;
  *) exit 1 ;;
esac
`
	brScript := `#!/bin/sh
case "$*" in
  *ready*|*list*) printf '%s\n' '[{"id":"blocked","status":"open","issue_type":"task","labels":[]},{"id":"ready","status":"open","issue_type":"task","labels":[]}]' ;;
  *) printf '%s\n' '{}' ;;
esac
`
	for name, script := range map[string]string{"bv": bvScript, "br": brScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	InvalidateTriageCache()
	t.Cleanup(InvalidateTriageCache)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetActionableRecommendationsContext(context.Background(), project, 1)
	if err != nil || len(got) != 1 || got[0].ID != "ready" {
		t.Fatalf("public planner returned the stale blocked join: %+v %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("read-only planning changed the tracker", err)
	}
}
