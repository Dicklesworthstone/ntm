package worksource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func batchSource(t *testing.T, records ...string) *Snapshot {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".beads", "issues.jsonl"), []byte(strings.Join(records, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := Read(context.Background(), root, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestEligibilityBatchReservesAllMutexesInRankOrder(t *testing.T) {
	source := batchSource(t,
		`{"id":"first","status":"open","labels":[" Mutex: DB ","mutex:api","mutex:db"]}`,
		`{"id":"db-peer","status":"open","labels":["mutex:db"]}`,
		`{"id":"api-peer","status":"open","labels":["mutex:api"]}`,
		`{"id":"independent","status":"open","labels":["mutex:docs"]}`,
		`{"id":"unlabelled","status":"open"}`,
	)
	got := source.Filter([]string{" first ", "db-peer", "api-peer", "first", "independent", "unlabelled"}, EligibilityPolicy{})
	if want := []string{"first", "independent", "unlabelled"}; !reflect.DeepEqual(got.EligibleIDs, want) {
		t.Fatalf("mutually exclusive work escaped one batch: got %v, want %v", got.EligibleIDs, want)
	}
	if len(got.Excluded) != 2 {
		t.Fatalf("missing conflict exclusions: %+v", got)
	}
	for _, exclusion := range got.Excluded {
		if !reflect.DeepEqual(exclusion.Reasons, []string{"mutex_conflict"}) {
			t.Fatalf("batch conflict described as existing ownership: %+v", exclusion)
		}
	}
	// Selection follows the input rank, not map order or lexical ID order.
	reversed := source.Filter([]string{"api-peer", "db-peer", "first", "independent"}, EligibilityPolicy{})
	if want := []string{"api-peer", "db-peer", "independent"}; !reflect.DeepEqual(reversed.EligibleIDs, want) {
		t.Fatalf("rank ignored: %+v", reversed)
	}
}

func TestEligibilityBatchRejectedWorkDoesNotReservePartialMutexes(t *testing.T) {
	source := batchSource(t,
		`{"id":"first","status":"open","labels":["mutex:a"]}`,
		`{"id":"overlap","status":"open","labels":["mutex:b","mutex:a"]}`,
		`{"id":"second","status":"open","labels":["mutex:b"]}`,
		`{"id":"gated","status":"open","labels":["human-only","mutex:c"]}`,
		`{"id":"third","status":"open","labels":["mutex:c"]}`,
		`{"id":"blocked","status":"open","labels":["mutex:d"],"dependencies":[{"depends_on_id":"missing","type":"blocks"}]}`,
		`{"id":"fourth","status":"open","labels":["mutex:d"]}`,
	)
	got := source.Filter([]string{"first", "overlap", "second", "gated", "third", "blocked", "fourth"}, EligibilityPolicy{GatedLabels: []string{"human-only"}})
	if want := []string{"first", "second", "third", "fourth"}; !reflect.DeepEqual(got.EligibleIDs, want) {
		t.Fatalf("rejected candidate reserved resources: %+v", got)
	}
}

func TestEligibilityBatchExistingOwnersStillTakePrecedence(t *testing.T) {
	source := batchSource(t,
		`{"id":"running","status":"in_progress","labels":["mutex:a"]}`,
		`{"id":"blocked","status":"open","labels":["mutex:a","mutex:b"]}`,
		`{"id":"selected","status":"open","labels":["mutex:b"]}`,
		`{"id":"external","status":"open","labels":["mutex:c"]}`,
	)
	policy := EligibilityPolicy{HeldMutexes: map[string]string{" C ": "private-owner"}}
	before, _ := json.Marshal(policy)
	got := source.Filter([]string{"blocked", "selected", "external"}, policy)
	if !reflect.DeepEqual(got.EligibleIDs, []string{"selected"}) {
		t.Fatalf("existing ownership lost or blocked work held extra resource: %+v", got)
	}
	for _, exclusion := range got.Excluded {
		if !reflect.DeepEqual(exclusion.Reasons, []string{"mutex_held"}) {
			t.Fatalf("existing holder misreported: %+v", exclusion)
		}
	}
	after, _ := json.Marshal(policy)
	if string(before) != string(after) {
		t.Fatal("selection modified caller ownership evidence")
	}
}

func TestEligibilityBatchSelectionIsRepeatableAndReadOnly(t *testing.T) {
	source := batchSource(t,
		`{"id":"a","status":"open","labels":["mutex:x"]}`,
		`{"id":"b","status":"open","labels":["mutex:x"]}`,
		`{"id":"c","status":"open","labels":["mutex:y"]}`,
	)
	before, err := os.ReadFile(source.Identity.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"a", "b", "c"}
	want := source.Filter(ids, EligibilityPolicy{})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := source.Filter(ids, EligibilityPolicy{}); !reflect.DeepEqual(got, want) {
				t.Errorf("concurrent selection changed: %+v", got)
			}
		}()
	}
	wg.Wait()
	if got := source.Filter([]string{"b", "a"}, EligibilityPolicy{}); !reflect.DeepEqual(got.EligibleIDs, []string{"b"}) {
		t.Fatalf("an earlier recommendation leaked ownership into a new call: %+v", got)
	}
	after, err := os.ReadFile(source.Identity.JSONLPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("read-only selection modified tracker: %v", err)
	}
	if err := Validate(context.Background(), source.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestEligibilityBatchLeavesIndependentWorkAndEmptyMutexesAlone(t *testing.T) {
	source := batchSource(t,
		`{"id":"a","status":"open","labels":["mutex:","mutex:  "]}`,
		`{"id":"b","status":"open","labels":["mutex:x"]}`,
		`{"id":"c","status":"open","labels":["mutex:y"]}`,
		`{"id":"d","status":"open","labels":["not-mutex:x"]}`,
	)
	ids := []string{"a", "b", "c", "d"}
	got := source.Filter(ids, EligibilityPolicy{})
	if !reflect.DeepEqual(got.EligibleIDs, ids) || len(got.Excluded) != 0 || got.ReasonCode != "" {
		t.Fatalf("independent work was unnecessarily serialized: %+v", got)
	}
}
