package bv

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadyCandidatesRequireCompleteExplicitResponse(t *testing.T) {
	for _, input := range []string{
		"", "null", `{}`, `{"issues":null}`, `{"other":[]}`,
		`[] {}`, `[null]`, `[{"id":"a"}]`, `[{"id":"a","priority":null}]`,
		`[{"id":" ","priority":1}]`, `[{"id":"a","priority":-1}]`, `[{"id":"a","priority":5}]`,
		`[{"id":"a","priority":1},{"id":" a ","priority":2}]`,
		`{"issues":[],"has_more":true}`, `{"issues":[],"truncated":true}`,
		`{"issues":[],"next_cursor":"next"}`, `{"issues":[],"next_cursor":1}`,
		`{"issues":[],"total":1}`, `{"issues":[],"total":-1}`,
		`{"issues":[],"success":false}`, `{"issues":[],"error":"tracker unavailable"}`,
	} {
		t.Run(input, func(t *testing.T) {
			rows, err := decodeReadyCandidates(input, 10)
			if !errors.Is(err, ErrReadyCandidatesIncomplete) || rows != nil {
				t.Fatalf("unverified response became a ready set: rows=%v err=%v", rows, err)
			}
		})
	}
}

func TestReadyCandidatesAcceptCheckedEmptyAndPreserveTrackerOrder(t *testing.T) {
	for _, input := range []string{`[]`, `{"issues":[]}`, `{"issues":[],"total":0,"next_cursor":null}`, `{"issues":[],"next_cursor":""}`} {
		rows, err := decodeReadyCandidates(input, 10)
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("checked-empty %s: rows=%v err=%v", input, rows, err)
		}
	}
	rows, err := decodeReadyCandidates(`{"issues":[{"id":" z ","title":"First","priority":2},{"id":"a","title":"Second","priority":1}],"total":2}`, 2)
	want := []BeadPreview{{ID: "z", Title: "First", Priority: "P2"}, {ID: "a", Title: "Second", Priority: "P1"}}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("tracker order or data changed: rows=%v err=%v", rows, err)
	}
}

func TestReadyCandidatesSentinelRejectsOverflowWithoutDroppingTail(t *testing.T) {
	input := `[{"id":"a","priority":1},{"id":"b","priority":2},{"id":"eligible-tail","priority":3}]`
	if rows, err := decodeReadyCandidates(input, 2); rows != nil || !errors.Is(err, ErrReadyCandidatesIncomplete) {
		t.Fatalf("overflow silently truncated: rows=%v err=%v", rows, err)
	}
	rows, err := decodeReadyCandidates(input, 3)
	if err != nil || len(rows) != 3 || rows[2].ID != "eligible-tail" {
		t.Fatalf("exact limit rejected or lost tail: rows=%v err=%v", rows, err)
	}
}

func TestReadyCandidatesRunnerRequestsSentinelAndExactProject(t *testing.T) {
	calls := 0
	rows, err := readReadyCandidates(context.Background(), "/intended/project", func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++
		if dir != "/intended/project" || !reflect.DeepEqual(args, []string{"ready", "--json", "--limit", "100001"}) {
			t.Fatalf("project or completeness request changed: %s %v", dir, args)
		}
		return `[{"id":"ready","priority":0}]`, nil
	})
	if calls != 1 || err != nil || len(rows) != 1 || rows[0].ID != "ready" {
		t.Fatalf("ready read failed: calls=%d rows=%v err=%v", calls, rows, err)
	}
}

func TestReadyCandidatesCancellationAndCommandFailureNeverBecomeEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := func(context.Context, string, ...string) (string, error) {
		t.Fatal("already-cancelled read invoked the tracker")
		return "[]", nil
	}
	if rows, err := readReadyCandidates(ctx, "/project", runner); rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read became empty: %v %v", rows, err)
	}
	if _, err := readReadyCandidates(nil, "/project", runner); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := readReadyCandidates(context.Background(), " ", runner); err == nil {
		t.Fatal("implicit project accepted")
	}
	failure := errors.New("tracker unavailable")
	if rows, err := readReadyCandidates(context.Background(), "/project", func(context.Context, string, ...string) (string, error) {
		return "[]", failure
	}); rows != nil || !errors.Is(err, failure) {
		t.Fatalf("failed command became empty: %v %v", rows, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if rows, err := readReadyCandidates(ctx, "/project", func(context.Context, string, ...string) (string, error) {
		cancel()
		return "[]", nil
	}); rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation became empty: %v %v", rows, err)
	}
}

func TestReadyCandidatesErrorsDoNotEchoTaskText(t *testing.T) {
	_, err := decodeReadyCandidates(`[{"id":"private task text","title":"secret value","priority":99}]`, 10)
	if err == nil || strings.Contains(err.Error(), "private task text") || strings.Contains(err.Error(), "secret value") {
		t.Fatalf("invalid-record error exposed task text: %v", err)
	}
}
