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

func renewalEvidence(t *testing.T, result *ReservationTransferResult) (bool, []agentmail.FileReservation) {
	t.Helper()
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Verified bool                        `json:"renewal_verified"`
		Rows     []agentmail.FileReservation `json:"renewed_reservations"`
	}
	if err := json.Unmarshal(wire, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence.Verified, evidence.Rows
}

func TestTransferRenewalRejectsAcknowledgedIdentityDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(agentmail.FileReservation) agentmail.FileReservation
	}{
		{"replacement ID", func(r agentmail.FileReservation) agentmail.FileReservation { r.ID += 100; return r }},
		{"foreign project", func(r agentmail.FileReservation) agentmail.FileReservation { r.ProjectID++; return r }},
		{"foreign owner", func(r agentmail.FileReservation) agentmail.FileReservation { r.AgentName = "peer"; return r }},
		{"changed path", func(r agentmail.FileReservation) agentmail.FileReservation { r.PathPattern = "other.go"; return r }},
		{"changed creation", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.CreatedTS.Time = r.CreatedTS.Add(time.Second)
			return r
		}},
		{"changed mode", func(r agentmail.FileReservation) agentmail.FileReservation { r.Exclusive = !r.Exclusive; return r }},
		{"changed reason", func(r agentmail.FileReservation) agentmail.FileReservation { r.Reason = "another task"; return r }},
		{"expired", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ExpiresTS.Time = time.Now().Add(-time.Hour)
			return r
		}},
		{"released", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ReleasedTS = &agentmail.FlexTime{Time: time.Now()}
			return r
		}},
		{"zero release marker", func(r agentmail.FileReservation) agentmail.FileReservation {
			r.ReleasedTS = &agentmail.FlexTime{}
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			opts.ToAgent = opts.FromAgent
			c.renew = func(_ context.Context, o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
				if len(o.Paths) != 0 || !reflect.DeepEqual(o.ReservationIDs, []int{1001, 1002}) {
					t.Fatalf("wrong renewal scope: %+v", o)
				}
				row := tc.change(c.live[1002])
				delete(c.live, 1002)
				c.live[row.ID] = row
				return &agentmail.RenewReservationsResult{Renewed: 2}, nil
			}
			result, err := TransferReservations(context.Background(), c, opts)
			verified, rows := renewalEvidence(t, result)
			if !errors.Is(err, ErrTransferPostcondition) || result.Success || !result.OutcomeUnknown || result.RolledBack || result.Stage != "verify_renewal" || verified || len(rows) != 0 || len(result.GrantedIDs) != 0 {
				t.Fatalf("renewal count laundered changed leases: %+v error=%v", result, err)
			}
			if c.reads != 2 || !reflect.DeepEqual(c.calls, []string{"renew:old"}) {
				t.Fatalf("verification retried or broadened mutation: reads=%d calls=%v", c.reads, c.calls)
			}
		})
	}
}

func TestTransferRenewalRejectsCountWithoutRequestedLifetime(t *testing.T) {
	c, opts := transferEvidenceFixture()
	opts.ToAgent = opts.FromAgent
	opts.TTLSeconds = 900
	for id, row := range c.live {
		row.ExpiresTS.Time = time.Now().Add(time.Minute)
		c.live[id] = row
	}
	c.renew = func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		return &agentmail.RenewReservationsResult{Renewed: 2}, nil
	}
	result, err := TransferReservations(context.Background(), c, opts)
	if !errors.Is(err, ErrTransferPostcondition) || result.Success || !result.OutcomeUnknown || result.Stage != "verify_renewal" {
		t.Fatalf("no-op renewal overstated remaining lifetime: %+v %v", result, err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("failed verification triggered another mutation: %v", c.calls)
	}
}

func TestTransferRenewalPublishesObservedLeases(t *testing.T) {
	for _, extend := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing lifetime already sufficient", true: "renewed lifetime"}[extend], func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			opts.ToAgent = opts.FromAgent
			opts.TTLSeconds = 600
			c.renew = func(_ context.Context, o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
				if extend {
					for _, id := range o.ReservationIDs {
						row := c.live[id]
						row.ExpiresTS.Time = time.Now().UTC().Truncate(time.Second).Add(time.Duration(o.ExtendSeconds) * time.Second)
						c.live[id] = row
					}
				}
				return &agentmail.RenewReservationsResult{Renewed: 2}, nil
			}
			before, _ := json.Marshal(opts)
			result, err := TransferReservations(context.Background(), c, opts)
			verified, rows := renewalEvidence(t, result)
			if err != nil || !result.Success || !verified || result.OutcomeUnknown || result.RolledBack || len(rows) != 2 || c.reads != 2 {
				t.Fatalf("verified refresh failed: %+v %v", result, err)
			}
			for i, row := range rows {
				if !reflect.DeepEqual(row, c.live[1001+i]) {
					t.Fatalf("returned row is not the observed lease: %+v want %+v", row, c.live[1001+i])
				}
			}
			after, _ := json.Marshal(opts)
			if string(before) != string(after) {
				t.Fatal("renewal rewrote captured source evidence")
			}
			if !reflect.DeepEqual(result.GrantedIDs, []int{1001, 1002}) || len(result.ReleasedIDs) != 0 {
				t.Fatalf("renewal invented new or released IDs: %+v", result)
			}
			// The returned evidence must remain independent of the registry.
			oldWire, _ := json.Marshal(result)
			r := c.live[1001]
			r.AgentName = "changed"
			r.ExpiresTS.Time = time.Now()
			c.live[1001] = r
			newWire, _ := json.Marshal(result)
			if string(oldWire) != string(newWire) {
				t.Fatal("live mutation changed returned renewal evidence")
			}
		})
	}
}

func TestTransferRenewalReadFailureIsTerminal(t *testing.T) {
	readFailure := errors.New("readback unavailable")
	for _, mode := range []string{"error", "nil listing", "empty listing", "duplicate ID", "missing ID", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			opts.ToAgent = opts.FromAgent
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := []agentmail.FileReservation{c.live[1001], c.live[1002]}
			c.list = func(ctx context.Context) ([]agentmail.FileReservation, error) {
				if c.reads == 1 {
					return original, nil
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultTransferCleanupTime {
					t.Error("post-renewal read is unbounded")
				}
				switch mode {
				case "error":
					return original, readFailure
				case "nil listing":
					return nil, nil
				case "empty listing":
					return []agentmail.FileReservation{}, nil
				case "duplicate ID":
					return append(append([]agentmail.FileReservation{}, original...), original[0]), nil
				case "missing ID":
					return append(append([]agentmail.FileReservation{}, original...), agentmail.FileReservation{ProjectID: 73}), nil
				default:
					cancel()
					return original, nil
				}
			}
			c.renew = func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
				return &agentmail.RenewReservationsResult{Renewed: 2}, nil
			}
			result, err := TransferReservations(ctx, c, opts)
			verified, rows := renewalEvidence(t, result)
			if !errors.Is(err, ErrTransferPostcondition) || result.Success || !result.OutcomeUnknown || verified || len(rows) != 0 || len(c.calls) != 1 {
				t.Fatalf("unavailable renewal evidence accepted: %+v %v", result, err)
			}
			if mode == "error" && !errors.Is(err, readFailure) || mode == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("underlying read failure discarded: %v", err)
			}
		})
	}
}

func TestTransferRenewalUncertainAcknowledgementSkipsReadback(t *testing.T) {
	for _, mode := range []string{"error", "nil", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			c, opts := transferEvidenceFixture()
			opts.ToAgent = opts.FromAgent
			c.renew = func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
				switch mode {
				case "error":
					return &agentmail.RenewReservationsResult{Renewed: 2}, errors.New("acknowledgement lost")
				case "nil":
					return nil, nil
				default:
					return &agentmail.RenewReservationsResult{Renewed: 1}, nil
				}
			}
			result, err := TransferReservations(context.Background(), c, opts)
			verified, rows := renewalEvidence(t, result)
			if err == nil || result.Success || !result.OutcomeUnknown || verified || len(rows) != 0 || c.reads != 1 || len(c.calls) != 1 {
				t.Fatalf("uncertain mutation authorized further work: %+v %v reads=%d calls=%v", result, err, c.reads, c.calls)
			}
		})
	}
}

func TestTransferRenewalDurationOverflowIsNonmutating(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if int64(maxInt) <= int64((1<<63-1)/time.Second) {
		t.Skip("int range cannot overflow time.Duration seconds")
	}
	c, opts := transferEvidenceFixture()
	opts.ToAgent = opts.FromAgent
	opts.TTLSeconds = maxInt
	result, err := TransferReservations(context.Background(), c, opts)
	if err == nil || result.Success || result.OutcomeUnknown || c.reads != 0 || len(c.calls) != 0 {
		t.Fatalf("overflowed TTL reached external I/O: %+v %v", result, err)
	}
}

func TestTransferRenewalExpiryBoundaryAndReadOnlyFailure(t *testing.T) {
	minimum := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		c, opts := transferEvidenceFixture()
		for id, row := range c.live {
			row.ExpiresTS.Time = minimum.Add(offset)
			c.live[id] = row
		}
		rows, err := verifyTransferRenewal(context.Background(), c, opts.ProjectKey, opts.Reservations, minimum)
		if offset < 0 && (!errors.Is(err, ErrTransferPostcondition) || len(rows) != 0) || offset >= 0 && (err != nil || len(rows) != 2) {
			t.Fatalf("expiry bound offset=%v returned %v %v", offset, rows, err)
		}
		if len(c.calls) != 0 || c.reads != 1 {
			t.Fatalf("postcondition check mutated registry: %v", c.calls)
		}
	}
}
