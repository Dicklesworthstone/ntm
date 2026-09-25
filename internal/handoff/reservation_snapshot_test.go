package handoff

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestReservationTransferCapturePreservesServerLeaseIdentity(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("fixture", -5*60*60))
	expires := created.Add(time.Hour)
	rows := []agentmail.FileReservation{
		{ID: 91, ProjectID: 73, AgentName: "BlueLake", PathPattern: "internal/**", Exclusive: true,
			Reason: "implementation", CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
		{ID: 92, ProjectID: 73, AgentName: "BlueLake", PathPattern: "docs/**", Exclusive: false,
			Reason: "reference", CreatedTS: agentmail.FlexTime{Time: created}, ExpiresTS: agentmail.FlexTime{Time: expires}},
	}
	transfer := buildReservationTransfer(GenerateHandoffOptions{
		AgentName: "BlueLake", TransferTTLSeconds: 900, TransferGraceSeconds: 2,
	}, "/project", rows)
	if transfer == nil || transfer.FromAgent != "BlueLake" || transfer.ProjectKey != "/project" ||
		transfer.TTLSeconds != 900 || transfer.GracePeriodSeconds != 2 || len(transfer.Reservations) != 2 {
		t.Fatalf("capture metadata lost: %+v", transfer)
	}
	for i, row := range rows {
		snapshot := transfer.Reservations[i]
		if snapshot.ID != row.ID || snapshot.ProjectID != row.ProjectID || snapshot.AgentName != row.AgentName ||
			snapshot.PathPattern != row.PathPattern || snapshot.Exclusive != row.Exclusive || snapshot.Reason != row.Reason ||
			!snapshot.CreatedAt.Equal(created) || !snapshot.ExpiresAt.Equal(expires) {
			t.Errorf("capture %d lost lease identity: %+v", i, snapshot)
		}
	}

	// The snapshot must stay independent of subsequent changes to the live
	// listing. Its lease identity is evidence from capture time, not a view.
	rows[0].ID = 191
	rows[0].AgentName = "Replacement"
	rows[0].CreatedTS.Time = created.Add(time.Minute)
	if transfer.Reservations[0].ID != 91 || transfer.Reservations[0].AgentName != "BlueLake" || !transfer.Reservations[0].CreatedAt.Equal(created) {
		t.Fatal("live listing changes rewrote captured ownership")
	}

	encoded, err := json.Marshal(transfer)
	if err != nil {
		t.Fatal(err)
	}
	var restored ReservationTransfer
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	for i, original := range transfer.Reservations {
		got := restored.Reservations[i]
		if got.ID != original.ID || got.ProjectID != original.ProjectID || got.AgentName != original.AgentName ||
			got.PathPattern != original.PathPattern || got.Exclusive != original.Exclusive || got.Reason != original.Reason ||
			!got.CreatedAt.Equal(original.CreatedAt) || !got.ExpiresAt.Equal(original.ExpiresAt) {
			t.Errorf("serialized lease %d did not round-trip: %+v", i, got)
		}
	}
}

func TestReservationTransferCaptureDoesNotInventOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  agentmail.FileReservation
	}{
		{"legacy missing identity", agentmail.FileReservation{PathPattern: "a.go"}},
		{"foreign owner remains visible", agentmail.FileReservation{ID: 9, ProjectID: 73, AgentName: "Other", PathPattern: "a.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transfer := buildReservationTransfer(GenerateHandoffOptions{AgentName: "Requested"}, "/project", []agentmail.FileReservation{tc.row})
			if transfer == nil || len(transfer.Reservations) != 1 {
				t.Fatalf("capture lost diagnostic evidence: %+v", transfer)
			}
			got := transfer.Reservations[0]
			if got.ID != tc.row.ID || got.ProjectID != tc.row.ProjectID || got.AgentName != tc.row.AgentName || !got.CreatedAt.Equal(tc.row.CreatedTS.Time) {
				t.Fatalf("request identity was substituted for server evidence: %+v", got)
			}
		})
	}
}

func TestReservationSnapshotIdentityHasStableYAMLNames(t *testing.T) {
	typ := reflect.TypeOf(ReservationSnapshot{})
	for field, tag := range map[string]string{
		"ID": "id,omitempty", "ProjectID": "project_id,omitempty", "AgentName": "agent_name,omitempty", "CreatedAt": "created_at,omitempty",
	} {
		got, ok := typ.FieldByName(field)
		if !ok || got.Tag.Get("yaml") != tag {
			t.Errorf("%s yaml tag = %q, want %q", field, got.Tag.Get("yaml"), tag)
		}
	}
}
