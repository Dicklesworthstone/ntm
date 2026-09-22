package agentmail

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func workReservationTestRow(id int, owner, reason string, expires time.Time) FileReservation {
	return FileReservation{ID: id, ProjectID: 17, AgentName: owner, PathPattern: "src/**", Reason: reason, ExpiresTS: FlexTime{Time: expires}}
}

func TestWorkReservationSnapshotIncludesPeersAndExactBeadReasons(t *testing.T) {
	now := time.Now().UTC()
	rows := []FileReservation{
		workReservationTestRow(1, "Zebra", "bead assignment: task-1", now.Add(time.Hour)),
		workReservationTestRow(2, "Alpha", "bead assignment: task-1", now.Add(time.Hour)),
		workReservationTestRow(3, "Zebra", "bead assignment: task-1", now.Add(time.Hour)),
		workReservationTestRow(4, "Peer", "bead assignment: task-10", now.Add(time.Hour)),
		workReservationTestRow(5, "Peer", "bead assignment: expired", now),
		workReservationTestRow(6, "Peer", "bead assignment: released", now.Add(time.Hour)),
		workReservationTestRow(7, "Peer", "planning task-1", now.Add(time.Hour)),
		workReservationTestRow(8, "Peer", "bead assignment: task-1 extra", now.Add(time.Hour)),
		workReservationTestRow(9, "Peer", "bead assignment: ", now.Add(time.Hour)),
	}
	rows[5].ReleasedTS = &FlexTime{Time: now.Add(-time.Minute)}
	before := append([]FileReservation(nil), rows...)
	projectReads := 0
	got, err := readWorkReservations(context.Background(), "/project", func(_ context.Context, key string) (*Project, error) {
		projectReads++
		if key != "/project" {
			t.Fatalf("project = %q", key)
		}
		return &Project{ID: 17, HumanKey: key}, nil
	}, func(_ context.Context, key string) ([]FileReservation, error) {
		if key != "/project" {
			t.Fatalf("list project = %q", key)
		}
		return rows, nil
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"task-1": {"Alpha", "Zebra"}, "task-10": {"Peer"}}
	if !reflect.DeepEqual(got.ByBead, want) || got.Active != 7 || got.Unmapped != 3 || got.ProjectID != 17 || projectReads != 2 || !got.ObservedAt.Equal(now) {
		t.Fatalf("incorrect project reservation observation: %+v (project reads %d)", got, projectReads)
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("read modified server rows")
	}
}

func TestWorkReservationSnapshotRejectsUnverifiableOwnership(t *testing.T) {
	now := time.Now().UTC()
	for _, name := range []string{"project replaced", "project moved", "foreign row", "duplicate", "missing owner", "missing path", "missing expiry", "bad ID", "missing project", "oversized"} {
		t.Run(name, func(t *testing.T) {
			rows := []FileReservation{workReservationTestRow(1, "Peer", "bead assignment: ready", now.Add(time.Hour))}
			reads := 0
			readProject := func(context.Context, string) (*Project, error) {
				reads++
				p := &Project{ID: 17, HumanKey: "/project"}
				if name == "project replaced" && reads == 2 {
					p.ID = 18
				}
				if name == "project moved" && reads == 2 {
					p.HumanKey = "/other"
				}
				if name == "missing project" {
					p.ID = 0
				}
				return p, nil
			}
			switch name {
			case "foreign row":
				rows[0].ProjectID = 18
			case "duplicate":
				rows = append(rows, rows[0])
			case "missing owner":
				rows[0].AgentName = " "
			case "missing path":
				rows[0].PathPattern = ""
			case "missing expiry":
				rows[0].ExpiresTS = FlexTime{}
			case "bad ID":
				rows[0].ID = 0
			case "oversized":
				rows = make([]FileReservation, maxReservationReadbackRows+1)
			}
			got, err := readWorkReservations(context.Background(), "/project", readProject, func(context.Context, string) ([]FileReservation, error) { return rows, nil }, func() time.Time { return now })
			if err == nil || got != nil {
				t.Fatalf("accepted unverifiable ownership: %+v, %v", got, err)
			}
		})
	}
}

func TestWorkReservationSnapshotErrorsAndCancellationAreNotEmpty(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, failureAt := range []string{"initial project", "listing", "final project", "cancel before", "cancel during", "clock"} {
		t.Run(failureAt, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failureAt == "cancel before" {
				cancel()
			}
			reads := 0
			got, err := readWorkReservations(ctx, "/project", func(context.Context, string) (*Project, error) {
				reads++
				if failureAt == "initial project" || (failureAt == "final project" && reads == 2) {
					return nil, sentinel
				}
				return &Project{ID: 17, HumanKey: "/project"}, nil
			}, func(context.Context, string) ([]FileReservation, error) {
				if failureAt == "listing" {
					return nil, sentinel
				}
				if failureAt == "cancel during" {
					cancel()
				}
				return []FileReservation{}, nil
			}, func() time.Time {
				if failureAt == "clock" {
					return time.Time{}
				}
				return time.Now()
			})
			if got != nil || err == nil {
				t.Fatalf("failure presented as empty success: %+v, %v", got, err)
			}
			if failureAt == "initial project" || failureAt == "listing" || failureAt == "final project" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("lost error identity: %v", err)
				}
			}
			if failureAt == "cancel before" || failureAt == "cancel during" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			}
			if failureAt == "cancel before" && reads != 0 {
				t.Fatal("cancelled read contacted server")
			}
		})
	}
}

func TestWorkReservationSnapshotExplicitEmptyRemainsObserved(t *testing.T) {
	got, err := readWorkReservations(context.Background(), "/project", func(context.Context, string) (*Project, error) {
		return &Project{ID: 17, HumanKey: "/project"}, nil
	}, func(context.Context, string) ([]FileReservation, error) { return []FileReservation{}, nil }, time.Now)
	if err != nil || got == nil || got.ByBead == nil || len(got.ByBead) != 0 || got.Active != 0 {
		t.Fatalf("empty observation: %+v, %v", got, err)
	}
}

func TestWorkReservationSnapshotRejectsMissingContextAndClient(t *testing.T) {
	if _, err := (*Client)(nil).ReadWorkReservations(context.Background(), "/project"); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := readWorkReservations(nil, "/project", nil, nil, time.Now); err == nil {
		t.Fatal("nil context accepted")
	}
}
