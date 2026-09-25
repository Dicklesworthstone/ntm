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
	sourceBShared bool
	calls         []string
	grants        map[int]string
	reserve       func(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	release       func(context.Context, string, []string) (*agentmail.ReleaseReservationsResult, error)
	renew         func(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

func (c *receiptTransferClient) ListReservations(ctx context.Context, _, _ string, _ bool) ([]agentmail.FileReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// These fixed source rows enable the existing destination-receipt tests;
	// they are independent of the malformed mutation replies under test.
	created := agentmail.FlexTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	expires := agentmail.FlexTime{Time: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
	rows := []agentmail.FileReservation{
		{ID: 1001, ProjectID: 73, AgentName: "old", PathPattern: "a.go", Exclusive: true, CreatedTS: created, ExpiresTS: expires},
		{ID: 1002, ProjectID: 73, AgentName: "old", PathPattern: "b.go", Exclusive: !c.sourceBShared, CreatedTS: created, ExpiresTS: expires},
	}
	if c.grants == nil {
		c.grants = make(map[int]string)
	}
	for _, row := range rows {
		c.grants[row.ID] = row.PathPattern
	}
	return rows, nil
}

func (c *receiptTransferClient) ReservePaths(ctx context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	c.calls = append(c.calls, "reserve:"+o.AgentName+":"+strings.Join(o.Paths, ","))
	result := receiptGrants(o.Paths...)
	var err error
	if c.reserve != nil {
		result, err = c.reserve(ctx, o)
	}
	if result != nil {
		if c.grants == nil {
			c.grants = make(map[int]string)
		}
		for _, grant := range result.Granted {
			c.grants[grant.ID] = grant.PathPattern
		}
	}
	return result, err
}
func (c *receiptTransferClient) ReleaseReservations(ctx context.Context, _, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	if len(paths) != 0 || len(ids) == 0 {
		return nil, errors.New("source release and destination cleanup must use exact IDs only")
	}
	if len(ids) != 0 {
		if len(paths) != 0 {
			return nil, errors.New("release mixed paths and IDs")
		}
		// Emulate server ID selection for the existing sequencing assertions;
		// never repair the mutation receipts themselves.
		paths = make([]string, 0, len(ids))
		for _, id := range ids {
			path, ok := c.grants[id]
			if !ok {
				return nil, fmt.Errorf("unknown grant ID %d", id)
			}
			paths = append(paths, path)
		}
	}
	c.calls = append(c.calls, "release:"+owner+":"+strings.Join(paths, ","))
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
	return &agentmail.RenewReservationsResult{Renewed: len(o.ReservationIDs)}, nil
}
func receiptGrants(paths ...string) *agentmail.ReservationResult {
	out := &agentmail.ReservationResult{}
	for i, p := range paths {
		id := 1000 + i
		switch p {
		case "a.go":
			id = 101
		case "b.go":
			id = 102
		}
		out.Granted = append(out.Granted, agentmail.FileReservation{ID: id, PathPattern: p})
	}
	return out
}
func receiptTransferOptions() TransferReservationsOptions {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	return TransferReservationsOptions{
		ProjectKey: "project", FromAgent: "old", ToAgent: "new", GracePeriod: time.Nanosecond,
		Reservations: []ReservationSnapshot{
			{ID: 1001, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "a.go", Exclusive: true},
			{ID: 1002, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "b.go", Exclusive: true},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	client.sourceBShared = true
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

// A grant can name an expected path and still be unverified: Agent Mail keeps
// the original receipt when decoding or independent ownership readback fails.
func TestTransferUnverifiedOwnershipNeverAuthorizesCompensation(t *testing.T) {
	cause := errors.New("active grant belongs to a different agent")
	for _, tc := range []struct {
		name                               string
		conflict, noReceipt, sharedFailure bool
	}{
		{name: "readback failure"},
		{name: "mixed conflict", conflict: true},
		{name: "no receipt", noReceipt: true},
		{name: "later shared group", sharedFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &receiptTransferClient{reserve: func(_ context.Context, o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				if o.AgentName != "new" {
					t.Fatal("unverified receipt authorized source reacquisition")
				}
				if tc.sharedFailure && o.Exclusive {
					return receiptGrants(o.Paths...), nil
				}
				err := errors.Join(agentmail.ErrReservationUnverified, cause)
				if tc.conflict {
					err = errors.Join(err, agentmail.ErrReservationConflict)
				}
				if tc.noReceipt {
					return nil, err
				}
				return receiptGrants(o.Paths...), err
			}}
			opts := receiptTransferOptions()
			if tc.sharedFailure {
				opts.Reservations[1].Exclusive = false
				client.sourceBShared = true
			}
			result, err := TransferReservations(context.Background(), client, opts)
			if !errors.Is(err, cause) || !errors.Is(err, agentmail.ErrReservationUnverified) || !errors.Is(err, ErrTransferGrantEvidence) || result.Success || result.RolledBack || !result.OutcomeUnknown {
				t.Fatalf("ownership uncertainty was lost: result=%+v error=%v", result, err)
			}
			if result.Attempts != 1 || result.Stage != "reserve" || result.CleanupError != "" || result.RollbackError != "" {
				t.Fatalf("unverified ownership reached compensation: %+v", result)
			}
			wantCalls := 2
			if tc.sharedFailure {
				wantCalls = 3
			}
			if len(client.calls) != wantCalls {
				t.Fatalf("unexpected post-verification mutation: %v", client.calls)
			}
			if !tc.noReceipt && !reflect.DeepEqual(result.GrantedPaths, []string{"a.go", "b.go"}) {
				t.Fatalf("partial evidence discarded: %+v", result)
			}
		})
	}
}

func TestTransferCleanupPreservesReplacementLease(t *testing.T) {
	// The destination's original lease disappears before cleanup. Another
	// process using the same agent name acquires the same path with a new ID.
	live := map[int]string{202: "a.go"}
	client := &fakeTransferClient{}
	client.reserveFn = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if o.AgentName != "new" {
			t.Fatal("source compensation ran after uncertain cleanup")
		}
		return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{
			{ID: 201, PathPattern: "a.go"},
		}}, agentmail.ErrReservationConflict
	}
	client.releaseFn = func(_, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		if owner == "old" {
			return &agentmail.ReleaseReservationsResult{Released: len(paths) + len(ids)}, nil
		}
		count := 0
		// Emulate union-selector semantics, so the old path-based code really
		// deletes the replacement. The test's oracle is the surviving lease.
		for id, path := range live {
			selected := false
			for _, requested := range paths {
				selected = selected || requested == path
			}
			for _, requested := range ids {
				selected = selected || requested == id
			}
			if selected {
				delete(live, id)
				count++
			}
		}
		return &agentmail.ReleaseReservationsResult{Released: count}, nil
	}
	opts := receiptTransferOptions()
	opts.Reservations = opts.Reservations[:1]
	client.prepareSource(&opts)
	result, err := TransferReservations(context.Background(), client, opts)
	if live[202] != "a.go" {
		t.Fatal("cleanup released a replacement lease on the same path")
	}
	if !errors.Is(err, agentmail.ErrReservationConflict) || result.Success || result.RolledBack || !result.OutcomeUnknown || result.CleanupError == "" || result.Attempts != 1 {
		t.Fatalf("uncertain cleanup authorized further work: result=%+v error=%v", result, err)
	}
	if len(client.releaseCalls) != 2 || len(client.reserveCalls) != 1 {
		t.Fatalf("unexpected mutations: release=%v reserve=%v", client.releaseCalls, client.reserveCalls)
	}
	cleanup := client.releaseCalls[1]
	if len(cleanup.paths) != 0 || !reflect.DeepEqual(cleanup.ids, []int{201}) || !reflect.DeepEqual(transferResultIDs(t, result), []int{201}) {
		t.Fatalf("lost exact cleanup identity: call=%+v result=%+v", cleanup, result)
	}
}

func TestTransferCleanupUsesPriorAttemptIDsOnly(t *testing.T) {
	client := &fakeTransferClient{}
	attempt := 0
	client.reserveFn = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		attempt++
		if attempt == 1 {
			return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{
				{ID: 201, PathPattern: "a.go"},
			}}, agentmail.ErrReservationConflict
		}
		return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{
			{ID: 301, PathPattern: "a.go"}, {ID: 302, PathPattern: "b.go"},
		}}, nil
	}
	client.releaseFn = func(_, owner string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		if owner == "new" {
			if len(paths) != 0 || !reflect.DeepEqual(ids, []int{201}) {
				t.Fatalf("cleanup broadened its lease scope: paths=%v IDs=%v", paths, ids)
			}
			ids[0] = -1 // A port must not be able to corrupt returned evidence.
			return &agentmail.ReleaseReservationsResult{Released: 1}, nil
		}
		return &agentmail.ReleaseReservationsResult{Released: len(paths) + len(ids)}, nil
	}
	opts := receiptTransferOptions()
	client.prepareSource(&opts)
	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil || !result.Success || result.Attempts != 2 || !reflect.DeepEqual(transferResultIDs(t, result), []int{301, 302}) {
		t.Fatalf("retry lost its new lease identities: result=%+v error=%v", result, err)
	}
	wire, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(wire), `"granted_ids":[301,302]`) {
		t.Fatalf("lease identity missing from recovery output: %s %v", wire, err)
	}
}

func TestTransferRejectsUnusableGrantIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ids      []int
		shared   bool
		conflict bool
	}{
		{name: "missing", ids: []int{0, 102}},
		{name: "negative", ids: []int{-1, 102}},
		{name: "duplicate within group", ids: []int{101, 101}},
		{name: "duplicate across groups", ids: []int{101, 101}, shared: true},
		{name: "missing partial-conflict identity", ids: []int{0, 102}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeTransferClient{}
			client.reserveFn = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				result := &agentmail.ReservationResult{}
				for _, path := range o.Paths {
					i := 0
					if path == "b.go" {
						i = 1
					}
					result.Granted = append(result.Granted, agentmail.FileReservation{ID: tc.ids[i], PathPattern: path})
				}
				if tc.conflict {
					return result, agentmail.ErrReservationConflict
				}
				return result, nil
			}
			opts := receiptTransferOptions()
			if tc.shared {
				opts.Reservations[1].Exclusive = false
			}
			client.prepareSource(&opts)
			result, err := TransferReservations(context.Background(), client, opts)
			if !errors.Is(err, ErrTransferGrantEvidence) || result.Success || result.RolledBack || !result.OutcomeUnknown || len(client.releaseCalls) != 1 {
				t.Fatalf("bad identity authorized cleanup or retry: result=%+v error=%v calls=%+v", result, err, client.releaseCalls)
			}
			if !reflect.DeepEqual(transferResultIDs(t, result), tc.ids) || !reflect.DeepEqual(result.GrantedPaths, []string{"a.go", "b.go"}) {
				t.Fatalf("discarded unverified receipt evidence: %+v", result)
			}
		})
	}
}

func transferResultIDs(t *testing.T, result *ReservationTransferResult) []int {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		IDs []int `json:"granted_ids"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.IDs
}
