package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Deliberately does not repair sparse or malformed responses. These tests drive
// the public transfer entry point with the exact replies under examination.
type receiptTransferClient struct {
	calls   []string
	reserve func(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	release func(context.Context, string, []string) (*agentmail.ReleaseReservationsResult, error)
	renew   func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

func (c *receiptTransferClient) ReservePaths(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	c.calls = append(c.calls, "reserve:"+o.AgentName+":"+strings.Join(o.Paths, ","))
	if c.reserve != nil {
		return c.reserve(ctx, o)
	}
	return receiptGrants(o.Paths...), nil
}
func (c *receiptTransferClient) ReleaseReservations(ctx context.Context, _, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	c.calls = append(c.calls, "release:"+owner+":"+strings.Join(paths, ","))
	if len(ids) != 0 {
		return nil, errors.New("unexpected ID release in path-based transfer")
	}
	if c.release != nil {
		return c.release(ctx, owner, paths)
	}
	return &agentmail.ReleaseReservationsResult{Released: len(paths)}, nil
}
func (c *receiptTransferClient) RenewReservations(ctx context.Context, o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	c.calls = append(c.calls, "renew:"+o.AgentName)
	if c.renew != nil {
		return c.renew(ctx, o)
	}
	return &agentmail.RenewReservationsResult{Renewed: len(o.Paths)}, nil
}
func receiptGrants(paths ...string) *agentmail.ReservationResult {
	out := &agentmail.ReservationResult{}
	for _, p := range paths {
		out.Granted = append(out.Granted, agentmail.FileReservation{PathPattern: p})
	}
	return out
}
func receiptTransferOptions() TransferReservationsOptions {
	return TransferReservationsOptions{
		ProjectKey: "project", FromAgent: "old", ToAgent: "new", GracePeriod: time.Nanosecond,
		Reservations: []ReservationSnapshot{{PathPattern: "a.go", Exclusive: true}, {PathPattern: "b.go", Exclusive: true}},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestTransferRejectsUnprovenGrantCoverage(t *testing.T) {
	cases := []struct {
		name  string
		reply *agentmail.ReservationResult
	}{
		{"nil", nil},
		{"empty", receiptGrants()},
		{"partial", receiptGrants("a.go")},
		{"duplicate", receiptGrants("a.go", "a.go")},
		{"unexpected", receiptGrants("a.go", "foreign.go")},
		{"blank", receiptGrants("a.go", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &receiptTransferClient{reserve: func(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				return tc.reply, nil
			}}
			result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
			if !errors.Is(err, ErrTransferGrantEvidence) || result.Success || result.RolledBack || !result.OutcomeUnknown {
				t.Fatalf("unproven receipt accepted or silently compensated: result=%+v error=%v", result, err)
			}
			if !reflect.DeepEqual(client.calls, []string{"release:old:a.go,b.go", "reserve:new:a.go,b.go"}) {
				t.Fatalf("untrusted reply authorized further mutations: %v", client.calls)
			}
			if !reflect.DeepEqual(result.ReleasedPaths, []string{"a.go", "b.go"}) || result.Attempts != 1 || result.Stage != "reserve" {
				t.Fatalf("lost confirmed source-release evidence: %+v", result)
			}
			if tc.reply != nil && len(result.GrantedPaths) != len(tc.reply.Granted) {
				t.Fatalf("discarded partial reply evidence: %+v", result)
			}
		})
	}
}

func TestTransferPreservesConflictWithoutDetails(t *testing.T) {
	client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			return &agentmail.ReservationResult{}, fmt.Errorf("occupied: %w", agentmail.ErrReservationConflict)
		}
		return receiptGrants(o.Paths...), nil
	}}
	opts := receiptTransferOptions()
	opts.Reservations[1].Exclusive = false
	result, err := TransferReservations(context.Background(), client, opts)
	if !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || !result.RolledBack || result.Attempts != 2 {
		t.Fatalf("conflict disappeared without a conflict array: result=%+v error=%v", result, err)
	}
	for _, call := range client.calls {
		if call == "reserve:new:b.go" {
			t.Fatalf("shared acquisition ran after exclusive failure: %v", client.calls)
		}
	}
}

func TestTransferRefusesRetryAfterCleanupFailure(t *testing.T) {
	cleanupFailure := errors.New("release acknowledgement lost")
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			client := &receiptTransferClient{
				reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
					if o.AgentName != "new" {
						t.Fatal("rollback ran despite unconfirmed destination cleanup")
					}
					return receiptGrants("a.go"), agentmail.ErrReservationConflict
				},
				release: func(_ context.Context, owner string, paths []string) (*agentmail.ReleaseReservationsResult, error) {
					if owner == "new" {
						if partial {
							return &agentmail.ReleaseReservationsResult{Released: 0}, nil
						}
						return nil, cleanupFailure
					}
					return &agentmail.ReleaseReservationsResult{Released: len(paths)}, nil
				},
			}
			result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
			if !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || result.RolledBack || !result.OutcomeUnknown || result.CleanupError == "" || result.Stage != "cleanup" {
				t.Fatalf("cleanup failure was suppressed: result=%+v error=%v", result, err)
			}
			if !partial && !errors.Is(err, cleanupFailure) {
				t.Fatalf("cleanup cause lost: %v", err)
			}
			if result.Attempts != 1 || !reflect.DeepEqual(result.GrantedPaths, []string{"a.go"}) || len(client.calls) != 3 {
				t.Fatalf("reacquired or discarded pending grants: result=%+v calls=%v", result, client.calls)
			}
		})
	}
}

func TestTransferRequiresCompleteRollbackCoverage(t *testing.T) {
	originalFailure := errors.New("destination acquisition failed")
	rollbackFailure := errors.New("source acquisition failed")
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				if o.AgentName == "new" {
					return receiptGrants("a.go"), originalFailure
				}
				if incomplete {
					return receiptGrants("a.go"), nil
				}
				return nil, rollbackFailure
			}}
			result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
			if !errors.Is(err, originalFailure) || result.Success || result.RolledBack || result.RollbackError == "" || result.Stage != "rollback" || !result.OutcomeUnknown {
				t.Fatalf("rollback failure was suppressed: result=%+v error=%v", result, err)
			}
			if incomplete && !errors.Is(err, ErrTransferGrantEvidence) || !incomplete && !errors.Is(err, rollbackFailure) {
				t.Fatalf("lost rollback cause: %v", err)
			}
			wire, marshalErr := json.Marshal(result)
			if marshalErr != nil || !strings.Contains(string(wire), `"rollback_error"`) || !strings.Contains(string(wire), `"outcome_unknown":true`) {
				t.Fatalf("recovery fields absent from JSON: %s, %v", wire, marshalErr)
			}
		})
	}
}

func TestTransferMixedConflictErrorDoesNotRetry(t *testing.T) {
	readbackFailure := errors.New("ownership readback unavailable")
	newCalls := 0
	client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			newCalls++
			return receiptGrants(), errors.Join(agentmail.ErrReservationConflict, readbackFailure)
		}
		return receiptGrants(o.Paths...), nil
	}}
	result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
	if !errors.Is(err, readbackFailure) || !errors.Is(err, agentmail.ErrReservationConflict) || newCalls != 1 || result.Success || !result.OutcomeUnknown {
		t.Fatalf("mixed failure allowed propagation retry: result=%+v error=%v calls=%v", result, err, client.calls)
	}
}

func TestTransferConflictReceiptCannotLookSuccessful(t *testing.T) {
	newCalls := 0
	client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			newCalls++
			if newCalls == 1 {
				res := receiptGrants("a.go")
				res.Conflicts = []agentmail.ReservationConflict{{Path: "b.go", Holders: []string{"peer"}}}
				return res, nil // A lax port omitted the Go error but not the conflict.
			}
		}
		return receiptGrants(o.Paths...), nil
	}}
	result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
	if err != nil || !result.Success || result.Attempts != 2 || result.RolledBack || result.OutcomeUnknown || result.Stage != "complete" || len(result.Conflicts) != 0 {
		t.Fatalf("verified second attempt did not succeed cleanly: result=%+v error=%v", result, err)
	}
	want := []string{"release:old:a.go,b.go", "reserve:new:a.go,b.go", "release:new:a.go", "reserve:new:a.go,b.go"}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("retry ordering = %v, want %v", client.calls, want)
	}
}

func TestTransferCancellationBeforeReleaseIsNonmutating(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &receiptTransferClient{}
	result, err := TransferReservations(ctx, client, receiptTransferOptions())
	if !errors.Is(err, context.Canceled) || result.Success || len(client.calls) != 0 || result.OutcomeUnknown {
		t.Fatalf("cancelled caller reached a mutating port: result=%+v error=%v calls=%v", result, err, client.calls)
	}
}

func TestTransferCancellationAfterReleaseCompensatesWithoutDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &receiptTransferClient{
		release: func(_ context.Context, _ string, paths []string) (*agentmail.ReleaseReservationsResult, error) {
			cancel()
			return &agentmail.ReleaseReservationsResult{Released: len(paths)}, nil
		},
		reserve: func(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
			if ctx.Err() != nil || o.AgentName != "old" {
				t.Fatalf("compensation lost its context or sent to destination: %+v, %v", o, ctx.Err())
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("cleanup is not bounded")
			}
			return receiptGrants(o.Paths...), nil
		},
	}
	result, err := TransferReservations(ctx, client, receiptTransferOptions())
	if !errors.Is(err, context.Canceled) || result.Success || !result.RolledBack || result.Attempts != 0 || len(client.calls) != 2 {
		t.Fatalf("post-release cancellation lost recovery: result=%+v error=%v calls=%v", result, err, client.calls)
	}
}

func TestTransferRetryCancellationPreservesBothCauses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName == "new" {
			cancel()
			return receiptGrants("a.go"), agentmail.ErrReservationConflict
		}
		return receiptGrants(o.Paths...), nil
	}}
	result, err := TransferReservations(ctx, client, receiptTransferOptions())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, agentmail.ErrReservationConflict) || !result.RolledBack || result.Success || result.Attempts != 1 {
		t.Fatalf("cancelled conflict lost cause or compensation: result=%+v error=%v", result, err)
	}
}

func TestTransferDoesNotMutateRequestedPaths(t *testing.T) {
	client := &receiptTransferClient{
		release: func(_ context.Context, _ string, paths []string) (*agentmail.ReleaseReservationsResult, error) {
			paths[0] = "changed by port"
			return &agentmail.ReleaseReservationsResult{Released: len(paths)}, nil
		},
		reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
			res := receiptGrants(o.Paths...)
			o.Paths[0] = "changed by port"
			return res, nil
		},
	}
	opts := receiptTransferOptions()
	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil || !result.Success || !reflect.DeepEqual(result.RequestedPaths, []string{"a.go", "b.go"}) || opts.Reservations[0].PathPattern != "a.go" {
		t.Fatalf("port mutated transfer scope: %+v, %v", result, err)
	}
}
