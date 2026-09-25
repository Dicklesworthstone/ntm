package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Produce a partial destination grant accompanied by a conflict, followed by
// complete coverage if the caller retries. The independent live map (not the
// acknowledgement) is the oracle for cleanup and replacement-lease tests.
func cleanupReadbackFixture() (*evidenceTransferClient, TransferReservationsOptions) {
	c, opts := transferEvidenceFixture()
	opts.Reservations[1].Exclusive = true
	row := c.live[1002]
	row.Exclusive = true
	c.live[1002] = row
	attempts := 0
	c.reserve = func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		attempts++
		if attempts == 1 {
			r := transferEvidenceLease(201, "a.go", o.AgentName, o.Exclusive)
			c.live[r.ID] = r
			return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{r}}, agentmail.ErrReservationConflict
		}
		result := &agentmail.ReservationResult{}
		for i, path := range o.Paths {
			r := transferEvidenceLease(301+i, path, o.AgentName, o.Exclusive)
			c.live[r.ID] = r
			result.Granted = append(result.Granted, r)
		}
		return result, nil
	}
	return c, opts
}

func cleanedTransferIDs(t *testing.T, result *ReservationTransferResult) []int {
	t.Helper()
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		IDs []int `json:"cleaned_ids"`
	}
	if err := json.Unmarshal(wire, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence.IDs
}

func TestTransferCleanupReadbackRejectsSurvivingAcknowledgedLease(t *testing.T) {
	for _, state := range []string{"active", "changed owner", "expired", "released marker"} {
		t.Run(state, func(t *testing.T) {
			c, opts := cleanupReadbackFixture()
			c.release = func(_ context.Context, owner string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
				if owner == "old" {
					for _, id := range ids {
						delete(c.live, id)
					}
				} else {
					r := c.live[201]
					switch state {
					case "changed owner":
						r.AgentName = "peer"
					case "expired":
						r.ExpiresTS.Time = time.Now().Add(-time.Hour)
					case "released marker":
						r.ReleasedTS = &agentmail.FlexTime{Time: time.Now()}
					}
					c.live[201] = r
				}
				return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
			}
			result, err := TransferReservations(context.Background(), c, opts)
			if !errors.Is(err, ErrTransferPostcondition) || !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || result.RolledBack || !result.OutcomeUnknown || result.CleanupError == "" || result.Stage != "cleanup" {
				t.Fatalf("cleanup count authorized further work despite a surviving ID: %+v %v", result, err)
			}
			if result.Attempts != 1 || !reflect.DeepEqual(result.GrantedIDs, []int{201}) || len(cleanedTransferIDs(t, result)) != 0 ||
				!reflect.DeepEqual(c.calls, []string{"release:old", "reserve:new", "release:new"}) || c.reads != 2 {
				t.Fatalf("failed cleanup lost evidence or retried: %+v calls=%v reads=%d", result, c.calls, c.reads)
			}
		})
	}
}

func TestTransferCleanupReadbackAllowsVerifiedRetryAndPreservesReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit empty listing", true: "replacement ID remains"}[replacement], func(t *testing.T) {
			c, opts := cleanupReadbackFixture()
			c.release = func(_ context.Context, owner string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
				if owner == "new" && !reflect.DeepEqual(ids, []int{201}) {
					t.Fatalf("cleanup escaped receipt IDs: %v", ids)
				}
				for _, id := range ids {
					delete(c.live, id)
				}
				if owner == "new" {
					if replacement {
						c.live[202] = transferEvidenceLease(202, "a.go", "new", true)
					}
					ids[0] = 202 // Verification must not consume a port-mutated selector.
				}
				return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
			}
			result, err := TransferReservations(context.Background(), c, opts)
			if err != nil || !result.Success || result.Attempts != 2 || result.OutcomeUnknown || result.RolledBack || c.reads != 2 ||
				!reflect.DeepEqual(result.GrantedIDs, []int{301, 302}) || !reflect.DeepEqual(cleanedTransferIDs(t, result), []int{201}) {
				t.Fatalf("verified cleanup did not permit exactly one retry: %+v %v reads=%d", result, err, c.reads)
			}
			if replacement && !reflect.DeepEqual(c.live[202], transferEvidenceLease(202, "a.go", "new", true)) {
				t.Fatal("cleanup touched or adopted a replacement same-path lease")
			}
			if !reflect.DeepEqual(c.calls, []string{"release:old", "reserve:new", "release:new", "reserve:new"}) {
				t.Fatalf("unexpected cleanup/retry sequence: %v", c.calls)
			}
		})
	}
}

func TestTransferCleanupReadbackFailureStopsRetryAndRollback(t *testing.T) {
	readFailure := errors.New("active resource unavailable")
	for _, mode := range []string{"read error", "nil listing", "duplicate ID", "missing ID", "foreign project", "cancelled read"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := cleanupReadbackFixture()
			original := []agentmail.FileReservation{c.live[1001], c.live[1002]}
			c.list = func(ctx context.Context) ([]agentmail.FileReservation, error) {
				if c.reads == 1 {
					return original, nil
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultTransferCleanupTime {
					t.Error("cleanup readback has no bounded deadline")
				}
				row := transferEvidenceLease(888, "independent.go", "peer", true)
				switch mode {
				case "read error":
					return []agentmail.FileReservation{}, readFailure
				case "nil listing":
					return nil, nil
				case "duplicate ID":
					return []agentmail.FileReservation{row, row}, nil
				case "missing ID":
					row.ID = 0
				case "foreign project":
					row.ProjectID = 74
				case "cancelled read":
					return nil, context.Canceled
				}
				return []agentmail.FileReservation{row}, nil
			}
			result, err := TransferReservations(context.Background(), c, opts)
			if !errors.Is(err, ErrTransferPostcondition) || !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || result.RolledBack || !result.OutcomeUnknown ||
				result.Attempts != 1 || result.CleanupError == "" || len(c.calls) != 3 || len(cleanedTransferIDs(t, result)) != 0 {
				t.Fatalf("unavailable cleanup evidence authorized mutation: %+v %v calls=%v", result, err, c.calls)
			}
			if mode == "read error" && !errors.Is(err, readFailure) || mode == "cancelled read" && !errors.Is(err, context.Canceled) {
				t.Fatalf("readback cause was discarded: %v", err)
			}
		})
	}
}

func TestTransferCleanupReadbackKeepsEarlierVerifiedIDsOnLaterFailure(t *testing.T) {
	c, opts := cleanupReadbackFixture()
	acquire := c.reserve
	c.reserve = func(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		res, err := acquire(ctx, o)
		if err == nil {
			return res, agentmail.ErrReservationConflict
		}
		return res, err
	}
	c.release = func(_ context.Context, owner string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		// Only the first attempt's cleanup is actually effective.
		if owner == "old" || reflect.DeepEqual(ids, []int{201}) {
			for _, id := range ids {
				delete(c.live, id)
			}
		}
		return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
	}
	result, err := TransferReservations(context.Background(), c, opts)
	if !errors.Is(err, ErrTransferPostcondition) || !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || result.RolledBack || result.Attempts != 2 ||
		!reflect.DeepEqual(cleanedTransferIDs(t, result), []int{201}) || !reflect.DeepEqual(result.GrantedIDs, []int{301, 302}) || c.reads != 3 || len(c.calls) != 5 {
		t.Fatalf("later cleanup failure lost earlier confirmation or pending IDs: %+v %v calls=%v", result, err, c.calls)
	}
}

func TestTransferCleanupUncertainAcknowledgementDoesNotReadBack(t *testing.T) {
	for _, mode := range []string{"error", "nil", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := cleanupReadbackFixture()
			c.release = func(_ context.Context, owner string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
				if owner == "old" {
					for _, id := range ids {
						delete(c.live, id)
					}
					return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
				}
				switch mode {
				case "error":
					return &agentmail.ReleaseReservationsResult{Released: len(ids)}, errors.New("lost acknowledgement")
				case "nil":
					return nil, nil
				default:
					return &agentmail.ReleaseReservationsResult{}, nil
				}
			}
			result, err := TransferReservations(context.Background(), c, opts)
			if err == nil || result.Success || result.RolledBack || !result.OutcomeUnknown || result.CleanupError == "" || c.reads != 1 || len(c.calls) != 3 {
				t.Fatalf("uncertain cleanup changed old stopping contract: %+v %v reads=%d", result, err, c.reads)
			}
		})
	}
}

func TestTransferCleanupReadbackRetainsBoundedCompensationAfterCancellation(t *testing.T) {
	c, opts := cleanupReadbackFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.reserve = func(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			r := transferEvidenceLease(201, "a.go", "new", true)
			c.live[201] = r
			cancel()
			return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{r}}, agentmail.ErrReservationConflict
		}
		if ctx.Err() != nil {
			t.Errorf("compensation used cancelled caller: %v", ctx.Err())
		}
		result := &agentmail.ReservationResult{}
		for i, path := range o.Paths {
			r := transferEvidenceLease(301+i, path, "old", o.Exclusive)
			c.live[r.ID] = r
			result.Granted = append(result.Granted, r)
		}
		return result, nil
	}
	result, err := TransferReservations(ctx, c, opts)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || !result.RolledBack ||
		!reflect.DeepEqual(cleanedTransferIDs(t, result), []int{201}) || c.reads != 2 || len(c.calls) != 4 {
		t.Fatalf("caller cancellation lost verified cleanup or restoration: %+v %v reads=%d calls=%v", result, err, c.reads, c.calls)
	}
}
