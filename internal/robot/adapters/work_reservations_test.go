package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

const workReservationFixtureJSONL = `{"id":"reserved","status":"open"}
{"id":"closing","status":"closed","labels":["mutex:database"]}
{"id":"mutex-task","status":"open","labels":["mutex:database"]}
{"id":"independent-a","status":"open"}
{"id":"independent-b","status":"open"}
`

func reservationEvidenceProject(t *testing.T) (string, *WorkSection) {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(workReservationFixtureJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	return project, &WorkSection{
		Available: true,
		Ready:     []WorkItem{{ID: "reserved"}, {ID: "mutex-task"}, {ID: "independent-a"}, {ID: "independent-b"}},
		Summary:   &WorkSummary{Ready: 4},
		Triage:    &WorkTriage{ReadyCount: 4, TopRecommendation: &WorkRecommendation{ID: "reserved"}},
	}
}

func observedWorkReservations(project string) *agentmail.WorkReservationSnapshot {
	return &agentmail.WorkReservationSnapshot{
		ProjectKey: project, ProjectID: 17, ObservedAt: time.Now().UTC(), Active: 2,
		ByBead: map[string][]string{"reserved": {"Peer"}, "closing": {"OtherPeer"}},
	}
}

func TestWorkReservationsFilterPeersAndClosingMutexBeforePreview(t *testing.T) {
	project, work := reservationEvidenceProject(t)
	calls := 0
	policy := WorkVerificationPolicy{readReservations: func(_ context.Context, key string) (*agentmail.WorkReservationSnapshot, error) {
		calls++
		if key != project {
			t.Fatalf("reader project = %s", key)
		}
		return observedWorkReservations(project), nil
	}}
	out, err := collectWorkWithSource(context.Background(), project, policy, func(context.Context) (*WorkSection, error) { return work, nil })
	if err != nil {
		t.Fatal(err)
	}
	out = limitVerifiedWorkPreview(out, 1)
	if calls != 1 || len(out.Ready) != 1 || out.Ready[0].ID != "independent-a" || out.Summary.Ready != 2 || out.Triage.ReadyCount != 2 || out.Triage.TopRecommendation != nil {
		t.Fatalf("reservation or preview bypass: %+v", out)
	}
	if out.Verification.VerifiedReady == nil || *out.Verification.VerifiedReady != 2 || !out.Verification.PreviewTruncated {
		t.Fatalf("lost full-set counts: %+v", out.Verification)
	}
	want := []worksource.Exclusion{{ID: "mutex-task", Reasons: []string{"mutex_held"}}, {ID: "reserved", Reasons: []string{"reserved"}}}
	if !reflect.DeepEqual(out.Verification.Excluded, want) {
		t.Fatalf("exclusions = %+v", out.Verification.Excluded)
	}
	if receipt := out.Verification.Reservations; receipt == nil || receipt.State != "observed" || receipt.ProjectID != 17 || receipt.Active != 2 || receipt.MappedBeads != 2 || receipt.ObservedAt == "" {
		t.Fatalf("missing independent ownership receipt: %+v", receipt)
	}
	if len(work.Ready) != 4 || work.Summary.Ready != 4 || work.Triage.TopRecommendation == nil || work.Verification != nil {
		t.Fatal("verification mutated collector-owned data")
	}
}

func TestWorkReservationsFailureDoesNotClaimEmptyObservation(t *testing.T) {
	project, work := reservationEvidenceProject(t)
	for _, name := range []string{"failure", "nil result", "foreign project", "missing map"} {
		t.Run(name, func(t *testing.T) {
			policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
				if name == "failure" {
					return nil, errors.New("reservation service unavailable")
				}
				if name == "nil result" {
					return nil, nil
				}
				snapshot := observedWorkReservations(project)
				if name == "foreign project" {
					snapshot.ProjectKey = filepath.Join(project, "other")
				}
				if name == "missing map" {
					snapshot.ByBead = nil
				}
				return snapshot, nil
			}}
			out, err := collectWorkWithSource(context.Background(), project, policy, func(context.Context) (*WorkSection, error) { return work, nil })
			if err != nil || !out.Available || len(out.Ready) != 4 || out.Verification.Reservations.State != "unavailable" || out.Verification.Reservations.Reason == "" {
				t.Fatalf("optional ownership failure looked healthy or erased backlog: %+v, %v", out, err)
			}
		})
	}
}

func TestWorkReservationsReadIsInsideSourceValidationWindow(t *testing.T) {
	project, work := reservationEvidenceProject(t)
	policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
		path := filepath.Join(project, ".beads", "issues.jsonl")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.WriteString("{\"id\":\"added\",\"status\":\"open\"}\n")
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("fixture write: %v, %v", err, closeErr)
		}
		return observedWorkReservations(project), nil
	}}
	out, err := collectWorkWithSource(context.Background(), project, policy, func(context.Context) (*WorkSection, error) { return work, nil })
	if !errors.Is(err, worksource.ErrStale) || out.Available || len(out.Ready) != 0 || out.Summary.Ready != 0 {
		t.Fatalf("mixed source accepted: %+v, %v", out, err)
	}
}

func TestWorkReservationsParentCancellationStopsCollection(t *testing.T) {
	project, work := reservationEvidenceProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
		cancel()
		return observedWorkReservations(project), nil
	}}
	out, err := collectWorkWithSource(ctx, project, policy, func(context.Context) (*WorkSection, error) { return work, nil })
	if !errors.Is(err, context.Canceled) || len(out.Ready) != 0 || out.Available {
		t.Fatalf("cancellation swallowed: %+v, %v", out, err)
	}
}

func TestWorkReservationsNestedTimeoutIsExplicitAndBounded(t *testing.T) {
	reader := func(ctx context.Context, _ string) (*agentmail.WorkReservationSnapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	rows, receipt, err := collectWorkReservationEvidence(context.Background(), "/project", reader, 5*time.Millisecond)
	if err != nil || rows != nil || receipt.State != "unavailable" || receipt.Reason == "" {
		t.Fatalf("timeout receipt: %+v, %v", receipt, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("optional ownership read was not bounded")
	}
}

func TestWorkReservationsObservedEmptyAndNotCheckedAreDifferent(t *testing.T) {
	project := t.TempDir()
	rows, absent, err := collectWorkReservationEvidence(context.Background(), project, nil, time.Second)
	if err != nil || rows != nil || absent.State != "not_checked" {
		t.Fatalf("absent reader: %+v %v", absent, err)
	}
	rows, empty, err := collectWorkReservationEvidence(context.Background(), project, func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
		return &agentmail.WorkReservationSnapshot{ProjectKey: project, ProjectID: 17, ObservedAt: time.Now(), ByBead: map[string][]string{}}, nil
	}, time.Second)
	if err != nil || rows == nil || len(rows) != 0 || empty.State != "observed" || empty.Active != 0 {
		t.Fatalf("empty read: %+v, %v", empty, err)
	}
}

func TestWorkReservationsDatabaseOnlyDoesNotClaimVerification(t *testing.T) {
	project := t.TempDir()
	policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
		t.Fatal("DB-only source unexpectedly read reservations")
		return nil, nil
	}}
	work := &WorkSection{Available: true, Ready: []WorkItem{{ID: "tool-only"}}, Summary: &WorkSummary{Ready: 1}}
	out, err := collectWorkWithSource(context.Background(), project, policy, func(context.Context) (*WorkSection, error) { return work, nil })
	if err != nil || out.Verification.CountScope != "tool_reported_unverified" || out.Verification.Reservations.State != "not_checked" {
		t.Fatalf("DB-only receipt: %+v, %v", out, err)
	}
}
