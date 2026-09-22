package bv

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Exercise the public planner, including source capture, external plan/triage
// collection, label reconciliation and the caller's limit. Tool scores alone
// deliberately recommend a conflicting pair.
func TestActionableSourcePublicPlannerBuildsMutexCompatibleBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project, path := sourceGuardFixture(t)
	data := `{"id":"first","status":"open","labels":["mutex:db"]}
{"id":"conflicting","status":"open","labels":["mutex:db"]}
{"id":"independent","status":"open","labels":["mutex:docs"]}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	bvScript := `#!/bin/sh
case "$*" in
  *--robot-plan*) printf '%s\n' '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"first","title":"First","status":"open","priority":1},{"id":"conflicting","title":"Conflicting","status":"open","priority":2},{"id":"independent","title":"Independent","status":"open","priority":3}]}],"summary":{"total_actionable":3}}}' ;;
  *--robot-triage*) printf '%s\n' '{"triage":{"recommendations":[{"id":"first","title":"First","status":"open","priority":1},{"id":"conflicting","title":"Conflicting","status":"open","priority":2},{"id":"independent","title":"Independent","status":"open","priority":3}]}}' ;;
  *) exit 1 ;;
esac
`
	brScript := `#!/bin/sh
case "$*" in
  *ready*|*list*) printf '%s\n' '[{"id":"first","status":"open","issue_type":"task","labels":["mutex:db"]},{"id":"conflicting","status":"open","issue_type":"task","labels":["mutex:db"]},{"id":"independent","status":"open","issue_type":"task","labels":["mutex:docs"]}]' ;;
  *) printf '%s\n' '{}' ;;
esac
`
	for name, script := range map[string]string{"bv": bvScript, "br": brScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	InvalidateTriageCache()
	t.Cleanup(InvalidateTriageCache)
	for _, limit := range []int{1, 2, 0} {
		got, err := GetActionableRecommendationsContext(context.Background(), project, limit)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(got))
		for _, candidate := range got {
			ids = append(ids, candidate.ID)
		}
		want := []string{"first", "independent"}
		if limit == 1 {
			want = want[:1]
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("limit=%d: got %v, want compatible ranked batch %v", limit, ids, want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != data {
		t.Fatalf("planning changed tracker: %v", err)
	}
}
