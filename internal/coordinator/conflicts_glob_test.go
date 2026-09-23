package coordinator

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestRunCycleDetectsIntersectingReservationGlobs(t *testing.T) {
	now := time.Now().UTC()
	payload, err := json.Marshal([]map[string]any{
		{"id": 1, "agent": "AgentAlpha", "path_pattern": "src/*/main.go", "exclusive": true,
			"created_ts": now.Add(-time.Hour).Format(time.RFC3339), "expires_ts": now.Add(time.Hour).Format(time.RFC3339)},
		{"id": 2, "agent": "AgentBeta", "path_pattern": "src/service/*.go", "exclusive": true,
			"created_ts": now.Add(-time.Minute).Format(time.RFC3339), "expires_ts": now.Add(time.Hour).Format(time.RFC3339)},
		{"id": 3, "agent": "AgentGamma", "path_pattern": "docs/*.md", "exclusive": true,
			"created_ts": now.Add(-time.Minute).Format(time.RFC3339), "expires_ts": now.Add(time.Hour).Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, recorder := newConflictWireMailServer(t, string(payload))
	cfg := DefaultCoordinatorConfig()
	cfg.ConflictNotify = true
	cfg.ConflictNegotiate = false
	c := newConflictWireCoordinator(t, client, cfg)
	if _, err := c.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := findConflictOutcomeEvent(t, drainConflictEvents(c), EventConflictDetected)
	if got := detailString(t, event, "pattern"); got != "src/*/main.go <-> src/service/*.go" {
		t.Fatalf("wrong overlapping patterns: %q", got)
	}
	if got := event.Details["holders"]; !reflect.DeepEqual(got, []string{"AgentAlpha", "AgentBeta"}) {
		t.Fatalf("wrong conflict holders: %v", got)
	}
	if got := recorder.callsFor("send_message"); len(got) != 1 {
		t.Fatalf("want one notification for the intersecting globs, got %v", got)
	}
	for _, call := range recorder.snapshot() {
		if call.Tool == "release_file_reservations" || call.Tool == "force_release_file_reservation" {
			t.Fatalf("read-only overlap evidence authorized a release: %+v", call)
		}
	}
}

func TestReservationGlobConflictsRetainOwnershipAndLifetimeRules(t *testing.T) {
	now := time.Now()
	base := []agentmail.FileReservation{
		{AgentName: "A", PathPattern: "src/*/main.go", Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: now.Add(time.Hour)}},
		{AgentName: "B", PathPattern: "src/service/*.go", Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: now.Add(time.Hour)}},
	}
	for _, tc := range []struct {
		name   string
		mutate func([]agentmail.FileReservation)
		want   int
	}{
		{"intersecting", func([]agentmail.FileReservation) {}, 1},
		{"same owner", func(r []agentmail.FileReservation) { r[1].AgentName = "A" }, 0},
		{"both shared", func(r []agentmail.FileReservation) { r[0].Exclusive = false; r[1].Exclusive = false }, 0},
		{"one exclusive", func(r []agentmail.FileReservation) { r[1].Exclusive = false }, 1},
		{"expired", func(r []agentmail.FileReservation) { r[1].ExpiresTS.Time = now.Add(-time.Second) }, 0},
		{"unknown expiry", func(r []agentmail.FileReservation) { r[1].ExpiresTS.Time = time.Time{} }, 1},
		{"disjoint globs", func(r []agentmail.FileReservation) { r[1].PathPattern = "src/service/*.rs" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := append([]agentmail.FileReservation(nil), base...)
			tc.mutate(r)
			before := append([]agentmail.FileReservation(nil), r...)
			if got := detectReservationConflictsAt(r, now); len(got) != tc.want {
				t.Fatalf("conflicts = %+v, want %d", got, tc.want)
			}
			if !reflect.DeepEqual(r, before) {
				t.Fatal("conflict analysis mutated the source reservations")
			}
		})
	}
}
