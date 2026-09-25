package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// The registry records real fixture effects independently of acknowledgements.
// Injected replies are never repaired or inferred from the requested owner.
type evidenceTransferClient struct {
	live         map[int]agentmail.FileReservation
	calls        []string
	reads        int
	reserve      func(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	release      func(context.Context, string, []int) (*agentmail.ReleaseReservationsResult, error)
	renew        func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
	list         func(context.Context) ([]agentmail.FileReservation, error)
	afterRelease func()
}

func transferEvidenceLease(id int, path, owner string, exclusive bool) agentmail.FileReservation {
	return agentmail.FileReservation{
		ID: id, ProjectID: 73, AgentName: owner, PathPattern: path, Exclusive: exclusive,
		Reason:    "handoff transfer from old",
		CreatedTS: agentmail.FlexTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)},
		ExpiresTS: agentmail.FlexTime{Time: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func transferEvidenceFixture() (*evidenceTransferClient, TransferReservationsOptions) {
	c := &evidenceTransferClient{live: make(map[int]agentmail.FileReservation)}
	o := TransferReservationsOptions{
		ProjectKey: "project", FromAgent: "old", ToAgent: "new", GracePeriod: time.Nanosecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for i, path := range []string{"a.go", "b.go"} {
		r := transferEvidenceLease(1001+i, path, "old", i == 0)
		c.live[r.ID] = r
		o.Reservations = append(o.Reservations, ReservationSnapshot{
			ID: r.ID, ProjectID: r.ProjectID, AgentName: r.AgentName, PathPattern: path,
			Exclusive: r.Exclusive, Reason: r.Reason, CreatedAt: r.CreatedTS.Time, ExpiresAt: r.ExpiresTS.Time,
		})
	}
	return c, o
}

func (c *evidenceTransferClient) ListReservations(ctx context.Context, project, owner string, all bool) ([]agentmail.FileReservation, error) {
	c.reads++
	if project != "project" || owner != "" || !all {
		return nil, errors.New("listing must cover the entire project")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.list != nil {
		return c.list(ctx)
	}
	out := make([]agentmail.FileReservation, 0, len(c.live))
	for _, r := range c.live {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (c *evidenceTransferClient) ReservePaths(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	c.calls = append(c.calls, "reserve:"+o.AgentName)
	if c.reserve != nil {
		return c.reserve(ctx, o)
	}
	return nil, errors.New("unexpected acquisition")
}

func (c *evidenceTransferClient) ReleaseReservations(ctx context.Context, _, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	c.calls = append(c.calls, "release:"+owner)
	if len(paths) != 0 || len(ids) == 0 {
		return nil, errors.New("release must use IDs only")
	}
	if c.release != nil {
		return c.release(ctx, owner, ids)
	}
	count := 0
	for _, id := range ids {
		if r, ok := c.live[id]; ok && r.AgentName == owner {
			delete(c.live, id)
			count++
		}
	}
	if c.afterRelease != nil {
		c.afterRelease()
	}
	return &agentmail.ReleaseReservationsResult{Released: count}, nil
}

func (c *evidenceTransferClient) RenewReservations(ctx context.Context, o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	c.calls = append(c.calls, "renew:"+o.AgentName)
	if c.renew != nil {
		return c.renew(ctx, o)
	}
	return nil, errors.New("unexpected renewal")
}

func decodeRollbackEvidence(t *testing.T, result *ReservationTransferResult) ([]agentmail.FileReservation, []agentmail.ReservationConflict) {
	t.Helper()
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Grants    []agentmail.FileReservation     `json:"rollback_grants"`
		Conflicts []agentmail.ReservationConflict `json:"rollback_conflicts"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Grants, decoded.Conflicts
}

func TestTransferRollbackRetainsCompleteLeaseEvidence(t *testing.T) {
	c, opts := transferEvidenceFixture()
	original := errors.New("destination unavailable")
	want := []agentmail.FileReservation{
		transferEvidenceLease(301, "a.go", "old", true),
		transferEvidenceLease(302, "b.go", "old", false),
	}
	c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			return nil, original
		}
		i := 0
		if !o.Exclusive {
			i = 1
		}
		return &agentmail.ReservationResult{Granted: want[i : i+1]}, nil
	}
	result, err := TransferReservations(context.Background(), c, opts)
	grants, conflicts := decodeRollbackEvidence(t, result)
	if !errors.Is(err, original) || result.Success || !result.RolledBack || result.RollbackError != "" || !result.OutcomeUnknown {
		t.Fatalf("restoration lost its original failure: %+v %v", result, err)
	}
	if !reflect.DeepEqual(grants, want) || len(conflicts) != 0 {
		t.Fatalf("restored lease metadata discarded: grants=%+v conflicts=%+v", grants, conflicts)
	}
	if !reflect.DeepEqual(result.RequestedIDs, []int{1001, 1002}) || len(result.GrantedIDs) != 0 {
		t.Fatalf("source replacement evidence overwrote source or destination identity: %+v", result)
	}
	if !reflect.DeepEqual(c.calls, []string{"release:old", "reserve:new", "reserve:old", "reserve:old"}) {
		t.Fatalf("restoration changed mutation sequencing: %v", c.calls)
	}
}

func TestTransferRollbackRetainsPartialAndUnverifiedReceipts(t *testing.T) {
	original := errors.New("destination failed")
	for _, mode := range []string{"partial conflict", "unverified", "later shared failure", "transport failure"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			partial := []agentmail.FileReservation{transferEvidenceLease(301, "a.go", "old", true)}
			conflicts := []agentmail.ReservationConflict{{Path: "b.go", Holders: []string{"peer"}}}
			cause := errors.New("source transport failure")
			if mode == "partial conflict" {
				cause = agentmail.ErrReservationConflict
			}
			if mode == "unverified" || mode == "later shared failure" {
				cause = agentmail.ErrReservationUnverified
			}
			c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				if o.AgentName == "new" {
					return nil, original
				}
				if mode == "later shared failure" && o.Exclusive {
					return &agentmail.ReservationResult{Granted: partial}, nil
				}
				if mode == "later shared failure" {
					return &agentmail.ReservationResult{Conflicts: conflicts}, cause
				}
				return &agentmail.ReservationResult{Granted: partial, Conflicts: conflicts}, cause
			}
			result, err := TransferReservations(context.Background(), c, opts)
			gotGrants, gotConflicts := decodeRollbackEvidence(t, result)
			if !errors.Is(err, original) || !errors.Is(err, cause) || result.RolledBack || result.Success || !result.OutcomeUnknown || result.RollbackError == "" {
				t.Fatalf("restoration failure disappeared: %+v %v", result, err)
			}
			if !reflect.DeepEqual(gotGrants, partial) || !reflect.DeepEqual(gotConflicts, conflicts) {
				t.Fatalf("partial restoration evidence lost: %+v %+v", gotGrants, gotConflicts)
			}
			wantCalls := 3
			if mode == "later shared failure" {
				wantCalls++
			}
			if len(c.calls) != wantCalls {
				t.Fatalf("rollback failure authorized retry or cleanup: %v", c.calls)
			}
		})
	}
}

func TestTransferRollbackRetainsMalformedGrantIdentity(t *testing.T) {
	for _, mode := range []string{"missing ID", "wrong path", "duplicate ID"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			opts.Reservations[1].Exclusive = true
			row := c.live[1002]
			row.Exclusive = true
			c.live[1002] = row
			grants := []agentmail.FileReservation{transferEvidenceLease(301, "a.go", "old", true), transferEvidenceLease(302, "b.go", "old", true)}
			switch mode {
			case "missing ID":
				grants[0].ID = 0
			case "wrong path":
				grants[0].PathPattern = "foreign.go"
			case "duplicate ID":
				grants[1].ID = grants[0].ID
			}
			c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				if o.AgentName == "new" {
					return nil, errors.New("destination failed")
				}
				return &agentmail.ReservationResult{Granted: grants}, nil
			}
			result, err := TransferReservations(context.Background(), c, opts)
			got, _ := decodeRollbackEvidence(t, result)
			if !errors.Is(err, ErrTransferGrantEvidence) || result.RolledBack || !result.OutcomeUnknown || !reflect.DeepEqual(got, grants) {
				t.Fatalf("malformed evidence was discarded, repaired or used: result=%+v grants=%+v error=%v", result, got, err)
			}
		})
	}
}

func TestTransferRollbackEvidenceDetachedAcrossAcquisitionGroups(t *testing.T) {
	c, opts := transferEvidenceFixture()
	released := agentmail.FlexTime{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	first := transferEvidenceLease(301, "a.go", "old", true)
	first.ReleasedTS = &released
	wantReleased := released
	c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			return nil, errors.New("destination failed")
		}
		if o.Exclusive {
			return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{first}}, nil
		}
		// This deliberately lax client reuses nested reply storage. The later
		// failure must preserve the earlier evidence, not its mutated contents.
		released.Time = released.Add(time.Hour)
		return nil, agentmail.ErrReservationUnverified
	}
	result, err := TransferReservations(context.Background(), c, opts)
	got, _ := decodeRollbackEvidence(t, result)
	if !errors.Is(err, agentmail.ErrReservationUnverified) || len(got) != 1 || got[0].ReleasedTS == nil || !got[0].ReleasedTS.Equal(wantReleased.Time) {
		t.Fatalf("later reply rewrote earlier evidence: %+v %v", got, err)
	}
}

func TestTransferRollbackEvidenceDetachedFromClient(t *testing.T) {
	c, opts := transferEvidenceFixture()
	released := agentmail.FlexTime{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	grant := transferEvidenceLease(301, "a.go", "foreign", true)
	grant.ReleasedTS = &released
	reply := &agentmail.ReservationResult{Granted: []agentmail.FileReservation{grant}, Conflicts: []agentmail.ReservationConflict{{Path: "a.go", Holders: []string{"original peer"}}}}
	c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			return nil, errors.New("destination failed")
		}
		return reply, agentmail.ErrReservationUnverified
	}
	result, err := TransferReservations(context.Background(), c, opts)
	if !errors.Is(err, agentmail.ErrReservationUnverified) {
		t.Fatal(err)
	}
	before, _ := json.Marshal(result)
	reply.Granted[0].ID = 999
	released.Time = released.Add(time.Hour)
	reply.Conflicts[0].Holders[0] = "changed peer"
	after, _ := json.Marshal(result)
	if string(before) != string(after) {
		t.Fatalf("client mutation changed published evidence:\nbefore=%s\nafter=%s", before, after)
	}
	got, conflicts := decodeRollbackEvidence(t, result)
	if len(got) != 1 || got[0].AgentName != "foreign" || len(conflicts) != 1 || conflicts[0].Holders[0] != "original peer" {
		t.Fatalf("evidence missing or inferred: %+v %+v", got, conflicts)
	}
}

func TestTransferCancellationRollbackRetainsNewLeaseIDs(t *testing.T) {
	c, opts := transferEvidenceFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.afterRelease = cancel
	c.reserve = func(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if ctx.Err() != nil || o.AgentName != "old" {
			return nil, fmt.Errorf("wrong compensation context/owner: %v %s", ctx.Err(), o.AgentName)
		}
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("unbounded compensation")
		}
		id := 301
		if !o.Exclusive {
			id = 302
		}
		return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{transferEvidenceLease(id, o.Paths[0], o.AgentName, o.Exclusive)}}, nil
	}
	result, err := TransferReservations(ctx, c, opts)
	grants, _ := decodeRollbackEvidence(t, result)
	if !errors.Is(err, context.Canceled) || !result.RolledBack || result.Attempts != 0 || len(grants) != 2 || grants[0].ID != 301 || grants[1].ID != 302 {
		t.Fatalf("cancelled transfer lost restored leases: %+v %+v %v", result, grants, err)
	}
}

func TestTransferWithoutRollbackHasNoRollbackEvidence(t *testing.T) {
	c, opts := transferEvidenceFixture()
	c.release = func(context.Context, string, []int) (*agentmail.ReleaseReservationsResult, error) {
		return nil, errors.New("source release failed")
	}
	result, err := TransferReservations(context.Background(), c, opts)
	grants, conflicts := decodeRollbackEvidence(t, result)
	if err == nil || result.RolledBack || len(grants) != 0 || len(conflicts) != 0 {
		t.Fatalf("invented compensation receipt: %+v %v", result, err)
	}
}
