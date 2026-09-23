package agentmail

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestReservationGlobOverlapAndConcreteMembership(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"src/*/main.go", "src/service/*.go", true},
		{"src/[α-γ]*.go", "src/β?.go", true},
		{"src/*/main.go", "src/service/*.rs", false},
		{"src/*.go", "src/deep/file.go", false},
		{"src/**/test.go", "src/deep/mytest.go", true}, // A literal reservation also covers its subtree.
	} {
		if got := reservationPatternsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("overlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if matchesReservationPattern("src/deep/mytest.go", "src/**/test.go") {
		t.Fatal("a concrete basename suffix was mistaken for the reserved filename")
	}
}

func TestStagedReservationGlobPolicy(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, pattern, path string
		want                bool
	}{
		{"question wildcard", "src/?/main.go", "src/a/main.go", true},
		{"Unicode class", "src/[α-γ].go", "src/β.go", true},
		{"literal brackets", "src/file1.go", "src/file[1].go", false},
		{"escaped brackets", `src/file\[1].go`, "src/file[1].go", true},
		{"literal star", "src/a.go", "src/*.go", false},
		{"escaped star", `src/\*.go`, "src/*.go", true},
		{"recursive exact basename", "src/**/test.go", "src/deep/test.go", true},
		{"recursive wrong basename", "src/**/test.go", "src/deep/mytest.go", false},
		{"qualified star", "src/*.go", "src/nested/file.go", false},
		{"basename glob", "main*.go", "src/nested/main_test.go", true},
		{"concrete path not subtree", "src/file.go/child", "src/file.go", false},
		{"reserved directory", "src", "src/file.go", true},
		{"leading space preserved", " src/*.go", " src/file.go", true},
		{"trailing space preserved", "*.go ", "src/file.go ", true},
		{"whitespace does not alias", "src/file.go", " src/file.go ", false},
		{"malformed is uncertain", "src/[", "unrelated/file.go", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reservations := []FileReservation{{ID: 42, AgentName: "OtherAgent", PathPattern: tc.pattern,
				Exclusive: true, ExpiresTS: FlexTime{Time: now.Add(time.Hour)}}}
			before := append([]FileReservation(nil), reservations...)
			paths := []string{tc.path}
			got := stagedReservationConflicts(reservations, "Self", paths, now)
			if (len(got) > 0) != tc.want {
				t.Fatalf("staged conflicts = %+v, want conflict %v", got, tc.want)
			}
			if len(got) > 0 && (got[0].Path != tc.path || got[0].PathPattern != tc.pattern || got[0].ReservationID != 42) {
				t.Fatalf("changed source identity in conflict: %+v", got)
			}
			if !reflect.DeepEqual(reservations, before) || paths[0] != tc.path {
				t.Fatal("mutated caller's reservation or staged filename")
			}
		})
	}
}

func TestStagedReservationOwnershipAndUnknownExpiry(t *testing.T) {
	now := time.Now().UTC()
	released := FlexTime{Time: now.Add(-time.Minute)}
	rows := []FileReservation{
		{ID: 5, AgentName: "Other", PathPattern: "src/*.go", Exclusive: true}, // unknown expiry
		{ID: 4, AgentName: "Other", PathPattern: "src/*.go", Exclusive: true, ReleasedTS: &released},
		{ID: 3, AgentName: "Other", PathPattern: "src/*.go", Exclusive: true, ExpiresTS: FlexTime{Time: now.Add(-time.Second)}},
		{ID: 2, AgentName: "Self", PathPattern: "src/*.go", Exclusive: true},
		{ID: 1, AgentName: "Other", PathPattern: "src/*.go", Exclusive: false},
		{ID: 6, AgentName: "Other", PathPattern: "src/*.go", Exclusive: true, ExpiresTS: FlexTime{Time: now.Add(time.Hour)}},
	}
	got := stagedReservationConflicts(rows, "Self", []string{"src/b.go", "src/a.go"}, now)
	wantIDs := []int{5, 6, 5, 6}
	if len(got) != len(wantIDs) {
		t.Fatalf("conflicts = %+v; only unknown/future exclusive non-self leases should block", got)
	}
	for i, id := range wantIDs {
		wantPath := "src/a.go"
		if i >= 2 {
			wantPath = "src/b.go"
		}
		if got[i].ReservationID != id || got[i].Path != wantPath {
			t.Fatalf("wrong identity/order at %d: %+v", i, got)
		}
	}
}

// Exercise the real public APIs and legacy ListReservations transport, which
// may omit expiry fields. No send, reserve, renew or release tool is installed
// in this fixture, so any attempted mutation fails the test.
func TestReservationGlobsThroughAgentMailClient(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	rows := []FileReservation{
		{ID: 1, AgentName: "Alpha", PathPattern: "src/*/main.go", Exclusive: true, ExpiresTS: FlexTime{Time: now.Add(time.Hour)}},
		{ID: 2, AgentName: "Beta", PathPattern: "src/service/*.go", Exclusive: true},
		{ID: 3, AgentName: "Gamma", PathPattern: "docs/*.md", Exclusive: true, ExpiresTS: FlexTime{Time: now.Add(time.Hour)}},
	}
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(map[string]interface{}) (interface{}, *JSONRPCError){
		"list_file_reservations": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			if args["project_key"] != "/project" || args["all_agents"] != true {
				t.Errorf("wrong reservation scope: %+v", args)
			}
			return rows, nil
		},
	}))
	defer server.Close()
	client := NewClient(WithBaseURL(server.URL))
	conflicts, err := client.CheckConflicts(context.Background(), "/project", []string{"src/service/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || !reflect.DeepEqual(conflicts[0].Holders, []string{"Alpha", "Beta"}) {
		t.Fatalf("mutual glob intersection missing from client result: %+v", conflicts)
	}
	staged, err := client.CheckStagedReservations(context.Background(), "/project", "Alpha", []string{"src/service/main.go", "docs/file[1].go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0].ReservationID != 2 {
		t.Fatalf("public staged guard lost unknown-expiry holder or self scope: %+v", staged)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := client.CheckStagedReservations(ctx, "/project", "", []string{"src/service/main.go"}); err == nil || len(result) != 0 {
		t.Fatalf("cancelled listing became a healthy guard result: %+v, %v", result, err)
	} else if !errors.Is(err, context.Canceled) {
		// Existing transport wrappers may classify cancellation differently;
		// never require a successful empty result to accommodate that.
		t.Logf("transport retains cancellation as an error: %v", err)
	}
}
