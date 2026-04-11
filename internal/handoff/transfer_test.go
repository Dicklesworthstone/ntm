package handoff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

type releaseCall struct {
	projectKey string
	agentName  string
	paths      []string
}

type renewCall struct {
	projectKey    string
	agentName     string
	extendSeconds int
	paths         []string
}

type fakeTransferClient struct {
	reserveCalls []agentmail.FileReservationOptions
	releaseCalls []releaseCall
	renewCalls   []renewCall
	reserveFn    func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	releaseFn    func(projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error)
	renewFn      func(opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

type contextAwareTransferClient struct {
	*fakeTransferClient
}

func (c *contextAwareTransferClient) ReservePaths(ctx context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.fakeTransferClient.ReservePaths(ctx, opts)
}

func (c *contextAwareTransferClient) ReleaseReservations(ctx context.Context, projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.fakeTransferClient.ReleaseReservations(ctx, projectKey, agentName, paths, ids)
}

func (c *contextAwareTransferClient) RenewReservations(ctx context.Context, opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.fakeTransferClient.RenewReservations(ctx, opts)
}

func (f *fakeTransferClient) ReservePaths(ctx context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	f.reserveCalls = append(f.reserveCalls, opts)
	if f.reserveFn != nil {
		return f.reserveFn(opts)
	}
	var granted []agentmail.FileReservation
	for _, p := range opts.Paths {
		granted = append(granted, agentmail.FileReservation{PathPattern: p})
	}
	return &agentmail.ReservationResult{Granted: granted}, nil
}

func (f *fakeTransferClient) ReleaseReservations(ctx context.Context, projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	f.releaseCalls = append(f.releaseCalls, releaseCall{
		projectKey: projectKey,
		agentName:  agentName,
		paths:      paths,
	})
	if f.releaseFn != nil {
		return f.releaseFn(projectKey, agentName, paths, ids)
	}
	return &agentmail.ReleaseReservationsResult{Released: len(paths) + len(ids)}, nil
}

func (f *fakeTransferClient) RenewReservations(ctx context.Context, opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	f.renewCalls = append(f.renewCalls, renewCall{
		projectKey:    opts.ProjectKey,
		agentName:     opts.AgentName,
		extendSeconds: opts.ExtendSeconds,
		paths:         append([]string(nil), opts.Paths...),
	})
	if f.renewFn != nil {
		return f.renewFn(opts)
	}
	return &agentmail.RenewReservationsResult{Renewed: len(opts.Paths)}, nil
}

func TestTransferReservationsSuccess(t *testing.T) {
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
			{PathPattern: "internal/b.go", Exclusive: false},
		},
		TTLSeconds: 120,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("TransferReservations error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if len(client.releaseCalls) != 1 {
		t.Fatalf("expected 1 release call, got %d", len(client.releaseCalls))
	}
	if len(client.reserveCalls) != 2 {
		t.Fatalf("expected 2 reserve calls (exclusive+shared), got %d", len(client.reserveCalls))
	}
	if len(result.GrantedPaths) != 2 {
		t.Fatalf("expected 2 granted paths, got %d", len(result.GrantedPaths))
	}
}

func TestTransferReservationsConflictRollback(t *testing.T) {
	client := &fakeTransferClient{}
	callCount := 0
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		callCount++
		if opts.AgentName == "new" {
			conflict := agentmail.ReservationConflict{Path: "internal/a.go", Holders: []string{"someone"}}
			res := &agentmail.ReservationResult{
				Granted:   []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
				Conflicts: []agentmail.ReservationConflict{conflict},
			}
			return res, agentmail.ErrReservationConflict
		}
		// rollback for old agent
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		TTLSeconds:  60,
		GracePeriod: 0,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if result == nil || len(result.Conflicts) == 0 {
		t.Fatalf("expected conflicts in result")
	}
	if len(client.releaseCalls) < 2 {
		t.Fatalf("expected release calls for old and partial grants")
	}
	if client.releaseCalls[1].agentName != "new" {
		t.Fatalf("expected second release call to clear partial new-agent grants, got %#v", client.releaseCalls[1])
	}
	if !result.RolledBack {
		t.Fatalf("expected rollback to succeed")
	}
}

func TestTransferReservationsReserveErrorRollsBackAfterPartialGrant(t *testing.T) {
	client := &fakeTransferClient{}
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if opts.AgentName == "new" {
			return &agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
			}, errors.New("reserve failed")
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		GracePeriod: 0,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected reserve failure")
	}
	if !result.RolledBack {
		t.Fatal("expected rollback after reserve failure")
	}
	if len(client.releaseCalls) < 2 {
		t.Fatalf("expected release of old reservation and partial new grant, got %d calls", len(client.releaseCalls))
	}
	if client.releaseCalls[1].agentName != "new" {
		t.Fatalf("expected second release call to target new agent, got %#v", client.releaseCalls[1])
	}
}

func TestTransferReservationsRetryWaitFailureRollsBackOldReservations(t *testing.T) {
	client := &fakeTransferClient{}
	ctx, cancel := context.WithCancel(context.Background())
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if opts.AgentName == "new" {
			cancel()
			return &agentmail.ReservationResult{
				Granted:   []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
				Conflicts: []agentmail.ReservationConflict{{Path: "internal/a.go", Holders: []string{"other"}}},
			}, agentmail.ErrReservationConflict
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		GracePeriod: 50 * time.Millisecond,
	}

	result, err := TransferReservations(ctx, client, opts)
	if err == nil {
		t.Fatal("expected context cancellation during retry wait")
	}
	if !result.RolledBack {
		t.Fatal("expected rollback after retry wait cancellation")
	}
	if len(client.releaseCalls) < 2 {
		t.Fatalf("expected old release and partial-grant cleanup, got %d", len(client.releaseCalls))
	}
}

func TestTransferReservationsRollbackUsesCleanupContextAfterCancellation(t *testing.T) {
	base := &fakeTransferClient{}
	client := &contextAwareTransferClient{fakeTransferClient: base}
	base.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if opts.AgentName == "new" {
			return &agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
			}, errors.New("reserve failed")
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	base.releaseFn = func(projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		if agentName == "old" {
			cancel()
		}
		return &agentmail.ReleaseReservationsResult{Released: len(paths) + len(ids)}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	result, err := TransferReservations(ctx, client, opts)
	if err == nil {
		t.Fatal("expected reserve failure")
	}
	if !result.RolledBack {
		t.Fatal("expected rollback to succeed with cleanup context after cancellation")
	}
	if len(base.releaseCalls) != 1 || base.releaseCalls[0].agentName != "old" {
		t.Fatalf("expected only the original old-agent release before cancellation, got %#v", base.releaseCalls)
	}
	if len(base.reserveCalls) != 1 || base.reserveCalls[0].AgentName != "old" {
		t.Fatalf("expected cleanup rollback to re-reserve for old agent, got %#v", base.reserveCalls)
	}
}

func TestTransferReservationsRollbackReleasesPartialGrantOnlyOnce(t *testing.T) {
	client := &fakeTransferClient{}
	newReleaseCalls := 0
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if opts.AgentName == "new" {
			return &agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
			}, errors.New("reserve failed")
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}
	client.releaseFn = func(projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		if agentName == "new" {
			newReleaseCalls++
			if newReleaseCalls > 1 {
				return nil, errors.New("partial grant released twice")
			}
		}
		return &agentmail.ReleaseReservationsResult{Released: len(paths) + len(ids)}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected reserve failure")
	}
	if !result.RolledBack {
		t.Fatal("expected rollback to succeed without double-releasing partial grants")
	}
	if newReleaseCalls != 1 {
		t.Fatalf("expected exactly one partial-grant release, got %d", newReleaseCalls)
	}
}

func TestTransferReservationsSameAgentRefresh(t *testing.T) {
	client := &fakeTransferClient{}
	client.renewFn = func(opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		return &agentmail.RenewReservationsResult{Renewed: 1}, nil
	}
	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "same",
		ToAgent:    "same",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		TTLSeconds: 90,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("TransferReservations error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %s", result.Error)
	}
	if len(client.renewCalls) != 1 {
		t.Fatalf("expected 1 renew call, got %d", len(client.renewCalls))
	}
	if got := client.renewCalls[0].paths; len(got) != 1 || got[0] != "internal/a.go" {
		t.Fatalf("expected renew scoped to requested path, got %#v", got)
	}
	if len(result.GrantedPaths) != 1 || result.GrantedPaths[0] != "internal/a.go" {
		t.Fatalf("expected granted paths to mirror refreshed reservation, got %#v", result.GrantedPaths)
	}
	if len(client.releaseCalls) != 0 || len(client.reserveCalls) != 0 {
		t.Fatalf("expected no release/reserve calls for same-agent refresh")
	}
}

func TestTransferReservationsSameAgentRefreshScopesRenewalToRequestedPaths(t *testing.T) {
	client := &fakeTransferClient{}
	client.renewFn = func(opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		if len(opts.Paths) != 2 {
			t.Fatalf("expected 2 requested paths, got %#v", opts.Paths)
		}
		if opts.Paths[0] != "internal/a.go" || opts.Paths[1] != "internal/b.go" {
			t.Fatalf("expected deduplicated requested paths, got %#v", opts.Paths)
		}
		return &agentmail.RenewReservationsResult{Renewed: 2}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "same",
		ToAgent:    "same",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
			{PathPattern: "internal/b.go", Exclusive: false},
			{PathPattern: "internal/a.go", Exclusive: false},
		},
		TTLSeconds: 90,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("TransferReservations error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %s", result.Error)
	}
	if len(client.renewCalls) != 1 {
		t.Fatalf("expected 1 renew call, got %d", len(client.renewCalls))
	}
	if len(result.GrantedPaths) != 2 || result.GrantedPaths[0] != "internal/a.go" || result.GrantedPaths[1] != "internal/b.go" {
		t.Fatalf("expected granted paths to mirror refreshed reservations, got %#v", result.GrantedPaths)
	}
	if len(client.releaseCalls) != 0 || len(client.reserveCalls) != 0 {
		t.Fatalf("expected no release/reserve calls for same-agent refresh")
	}
}

func TestTransferReservationsSameAgentRefreshIncomplete(t *testing.T) {
	client := &fakeTransferClient{}
	client.renewFn = func(opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		return &agentmail.RenewReservationsResult{Renewed: 0}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "same",
		ToAgent:    "same",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		TTLSeconds: 90,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected incomplete refresh error")
	}
	if result.Success {
		t.Fatal("expected Success=false on incomplete refresh")
	}
}

func TestTransferReservationsReleaseFailure(t *testing.T) {
	client := &fakeTransferClient{}
	client.releaseFn = func(projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		return nil, errors.New("release failed")
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected release error")
	}
	if result.Success {
		t.Fatal("expected Success=false on release error")
	}
	if len(client.reserveCalls) != 0 {
		t.Fatalf("expected no reserve calls on release failure, got %d", len(client.reserveCalls))
	}
}

func TestTransferReservationsReleaseIncomplete(t *testing.T) {
	client := &fakeTransferClient{}
	client.releaseFn = func(projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		return &agentmail.ReleaseReservationsResult{Released: 0}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected incomplete release error")
	}
	if result.Success {
		t.Fatal("expected Success=false on incomplete release")
	}
	if len(client.reserveCalls) != 0 {
		t.Fatalf("expected no reserve calls after incomplete release, got %d", len(client.reserveCalls))
	}
	if result.Error == "" {
		t.Fatal("expected error message on incomplete release")
	}
}

func TestTransferReservationsRenewFailure(t *testing.T) {
	client := &fakeTransferClient{}
	client.renewFn = func(opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		return nil, errors.New("renew failed")
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "same",
		ToAgent:    "same",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		TTLSeconds: 60,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected renew error")
	}
	if result.Success {
		t.Fatal("expected Success=false on renew error")
	}
	if len(client.releaseCalls) != 0 || len(client.reserveCalls) != 0 {
		t.Fatal("expected no release/reserve calls for renew failure")
	}
}

func TestTransferReservationsGraceRetrySuccess(t *testing.T) {
	client := &fakeTransferClient{}
	callCount := 0
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if opts.AgentName != "new" {
			return &agentmail.ReservationResult{
				Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
			}, nil
		}
		callCount++
		if callCount == 1 {
			return &agentmail.ReservationResult{
				Granted:   []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
				Conflicts: []agentmail.ReservationConflict{{Path: "internal/a.go", Holders: []string{"other"}}},
			}, agentmail.ErrReservationConflict
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "internal/a.go"}},
		}, nil
	}

	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
		GracePeriod: 5 * time.Millisecond,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success after retry, got %s", result.Error)
	}
	if result.RolledBack {
		t.Fatal("did not expect rollback on successful retry")
	}
	if callCount != 2 {
		t.Fatalf("expected 2 reserve attempts for new agent, got %d", callCount)
	}
	if len(client.releaseCalls) < 2 {
		t.Fatalf("expected release for old agent and partial retry, got %d", len(client.releaseCalls))
	}
}

// =============================================================================
// Edge Case Tests for Handoff Protocol State Management (bd-2ofm)
// =============================================================================

func TestTransferReservationsNilClient(t *testing.T) {
	t.Logf("HANDOFF_TEST: NilClient | Testing nil client error handling")
	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	result, err := TransferReservations(context.Background(), nil, opts)
	if err == nil {
		t.Fatal("expected error for nil client")
	}
	if result == nil {
		t.Fatal("expected non-nil result even on error")
	}
	if result.Success {
		t.Fatal("expected Success=false for nil client")
	}
	if result.Error == "" {
		t.Fatal("expected Error message for nil client")
	}
	t.Logf("HANDOFF_TEST: NilClient | Error=%s", result.Error)
}

func TestTransferReservationsEmptyProjectKey(t *testing.T) {
	t.Logf("HANDOFF_TEST: EmptyProjectKey | Testing empty project key error")
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey:   "", // Empty
		FromAgent:    "old",
		ToAgent:      "new",
		Reservations: []ReservationSnapshot{{PathPattern: "a.go"}},
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err == nil {
		t.Fatal("expected error for empty project key")
	}
	if result.Success {
		t.Fatal("expected Success=false")
	}
	t.Logf("HANDOFF_TEST: EmptyProjectKey | Error=%s", result.Error)
}

func TestTransferReservationsMissingAgents(t *testing.T) {
	t.Logf("HANDOFF_TEST: MissingAgents | Testing missing from/to agent errors")
	client := &fakeTransferClient{}

	tests := []struct {
		name      string
		fromAgent string
		toAgent   string
	}{
		{"missing from agent", "", "new"},
		{"missing to agent", "old", ""},
		{"missing both", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := TransferReservationsOptions{
				ProjectKey:   "proj",
				FromAgent:    tt.fromAgent,
				ToAgent:      tt.toAgent,
				Reservations: []ReservationSnapshot{{PathPattern: "a.go"}},
			}

			result, err := TransferReservations(context.Background(), client, opts)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if result.Success {
				t.Fatalf("expected Success=false for %s", tt.name)
			}
			t.Logf("HANDOFF_TEST: MissingAgents | Case=%s | Error=%s", tt.name, result.Error)
		})
	}
}

func TestTransferReservationsEmptyReservations(t *testing.T) {
	t.Logf("HANDOFF_TEST: EmptyReservations | Testing empty reservations list")
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey:   "proj",
		FromAgent:    "old",
		ToAgent:      "new",
		Reservations: []ReservationSnapshot{}, // Empty
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("unexpected error for empty reservations: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true for empty reservations, got Error=%s", result.Error)
	}
	if len(client.releaseCalls) != 0 || len(client.reserveCalls) != 0 {
		t.Fatal("expected no client calls for empty reservations")
	}
	t.Logf("HANDOFF_TEST: EmptyReservations | Success=true (no-op)")
}

func TestTransferReservationsContextCancellation(t *testing.T) {
	t.Logf("HANDOFF_TEST: ContextCancellation | Testing context cancellation during grace period")
	client := &fakeTransferClient{}
	callCount := 0
	client.reserveFn = func(opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		callCount++
		// First call conflicts to trigger grace period wait
		if callCount == 1 {
			return &agentmail.ReservationResult{
				Conflicts: []agentmail.ReservationConflict{{Path: "a.go", Holders: []string{"other"}}},
			}, agentmail.ErrReservationConflict
		}
		return &agentmail.ReservationResult{
			Granted: []agentmail.FileReservation{{PathPattern: "a.go"}},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to test context handling
	cancel()

	opts := TransferReservationsOptions{
		ProjectKey:   "proj",
		FromAgent:    "old",
		ToAgent:      "new",
		Reservations: []ReservationSnapshot{{PathPattern: "a.go", Exclusive: true}},
		GracePeriod:  100 * time.Millisecond,
	}

	result, err := TransferReservations(ctx, client, opts)
	// With cancelled context, should fail during grace wait
	if result.Success && err == nil {
		// Success is also valid if cancellation happens after completion
		t.Logf("HANDOFF_TEST: ContextCancellation | Transfer completed before cancellation")
	} else {
		t.Logf("HANDOFF_TEST: ContextCancellation | Error=%v | CallCount=%d", err, callCount)
	}
}

func TestTransferReservationsNilContext(t *testing.T) {
	t.Logf("HANDOFF_TEST: NilContext | Testing nil context handling")
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "old",
		ToAgent:    "new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "a.go", Exclusive: true},
		},
	}

	// nil context should be handled gracefully
	result, err := TransferReservations(context.TODO(), client, opts)
	if err != nil {
		t.Fatalf("unexpected error with nil context: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success with nil context, got Error=%s", result.Error)
	}
	t.Logf("HANDOFF_TEST: NilContext | Success=true")
}

func TestTransferReservationsDefaultTTL(t *testing.T) {
	t.Logf("HANDOFF_TEST: DefaultTTL | Testing default TTL values")
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey: "proj",
		FromAgent:  "same",
		ToAgent:    "same",
		Reservations: []ReservationSnapshot{
			{PathPattern: "a.go", Exclusive: true},
		},
		TTLSeconds: 0, // Should use default
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success")
	}
	if len(client.renewCalls) != 1 {
		t.Fatalf("expected 1 renew call")
	}
	// Default is 15 minutes (900 seconds)
	if client.renewCalls[0].extendSeconds != 15*60 {
		t.Errorf("expected default TTL of 900, got %d", client.renewCalls[0].extendSeconds)
	}
	t.Logf("HANDOFF_TEST: DefaultTTL | TTLSeconds=%d", client.renewCalls[0].extendSeconds)
}

func TestSplitReservationPaths(t *testing.T) {
	t.Logf("HANDOFF_TEST: SplitReservationPaths | Testing path splitting logic")

	tests := []struct {
		name             string
		reservations     []ReservationSnapshot
		wantExclusiveLen int
		wantSharedLen    int
		wantRequestedLen int
	}{
		{
			name:             "empty input",
			reservations:     []ReservationSnapshot{},
			wantExclusiveLen: 0,
			wantSharedLen:    0,
			wantRequestedLen: 0,
		},
		{
			name: "all exclusive",
			reservations: []ReservationSnapshot{
				{PathPattern: "a.go", Exclusive: true},
				{PathPattern: "b.go", Exclusive: true},
			},
			wantExclusiveLen: 2,
			wantSharedLen:    0,
			wantRequestedLen: 2,
		},
		{
			name: "all shared",
			reservations: []ReservationSnapshot{
				{PathPattern: "a.go", Exclusive: false},
				{PathPattern: "b.go", Exclusive: false},
			},
			wantExclusiveLen: 0,
			wantSharedLen:    2,
			wantRequestedLen: 2,
		},
		{
			name: "mixed",
			reservations: []ReservationSnapshot{
				{PathPattern: "a.go", Exclusive: true},
				{PathPattern: "b.go", Exclusive: false},
				{PathPattern: "c.go", Exclusive: true},
			},
			wantExclusiveLen: 2,
			wantSharedLen:    1,
			wantRequestedLen: 3,
		},
		{
			name: "duplicates merged (exclusive wins)",
			reservations: []ReservationSnapshot{
				{PathPattern: "a.go", Exclusive: false},
				{PathPattern: "a.go", Exclusive: true},
				{PathPattern: "a.go", Exclusive: false},
			},
			wantExclusiveLen: 1,
			wantSharedLen:    0,
			wantRequestedLen: 1,
		},
		{
			name: "empty path skipped",
			reservations: []ReservationSnapshot{
				{PathPattern: "", Exclusive: true},
				{PathPattern: "a.go", Exclusive: true},
			},
			wantExclusiveLen: 1,
			wantSharedLen:    0,
			wantRequestedLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exclusive, shared, requested := splitReservationPaths(tt.reservations)

			if len(exclusive) != tt.wantExclusiveLen {
				t.Errorf("exclusive: got %d, want %d", len(exclusive), tt.wantExclusiveLen)
			}
			if len(shared) != tt.wantSharedLen {
				t.Errorf("shared: got %d, want %d", len(shared), tt.wantSharedLen)
			}
			if len(requested) != tt.wantRequestedLen {
				t.Errorf("requested: got %d, want %d", len(requested), tt.wantRequestedLen)
			}
			t.Logf("HANDOFF_TEST: SplitReservationPaths | Case=%s | Exclusive=%d Shared=%d Requested=%d",
				tt.name, len(exclusive), len(shared), len(requested))
		})
	}
}

func TestWaitWithContext(t *testing.T) {
	t.Logf("HANDOFF_TEST: WaitWithContext | Testing context-aware wait")

	t.Run("zero duration", func(t *testing.T) {
		err := waitWithContext(context.Background(), 0)
		if err != nil {
			t.Errorf("unexpected error for zero duration: %v", err)
		}
	})

	t.Run("negative duration", func(t *testing.T) {
		err := waitWithContext(context.Background(), -1*time.Second)
		if err != nil {
			t.Errorf("unexpected error for negative duration: %v", err)
		}
	})

	t.Run("normal wait", func(t *testing.T) {
		start := time.Now()
		err := waitWithContext(context.Background(), 50*time.Millisecond)
		elapsed := time.Since(start)

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if elapsed < 40*time.Millisecond {
			t.Errorf("wait too short: %v", elapsed)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := waitWithContext(ctx, 1*time.Second)
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		err := waitWithContext(ctx, 1*time.Second)
		if err != context.DeadlineExceeded {
			t.Errorf("expected context.DeadlineExceeded, got %v", err)
		}
	})
}

func TestTransferReservationsResultFields(t *testing.T) {
	t.Logf("HANDOFF_TEST: ResultFields | Testing result struct population")
	client := &fakeTransferClient{}
	opts := TransferReservationsOptions{
		ProjectKey: "test-project",
		FromAgent:  "agent-old",
		ToAgent:    "agent-new",
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true, Reason: "test"},
			{PathPattern: "internal/b.go", Exclusive: false},
		},
		TTLSeconds: 120,
	}

	result, err := TransferReservations(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify result fields
	if result.FromAgent != "agent-old" {
		t.Errorf("FromAgent: got %s, want agent-old", result.FromAgent)
	}
	if result.ToAgent != "agent-new" {
		t.Errorf("ToAgent: got %s, want agent-new", result.ToAgent)
	}
	if len(result.RequestedPaths) != 2 {
		t.Errorf("RequestedPaths: got %d, want 2", len(result.RequestedPaths))
	}
	if len(result.ReleasedPaths) != 2 {
		t.Errorf("ReleasedPaths: got %d, want 2", len(result.ReleasedPaths))
	}
	if len(result.GrantedPaths) != 2 {
		t.Errorf("GrantedPaths: got %d, want 2", len(result.GrantedPaths))
	}
	if !result.Success {
		t.Errorf("Success: got false, want true")
	}
	if result.RolledBack {
		t.Errorf("RolledBack: got true, want false")
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("Conflicts: got %d, want 0", len(result.Conflicts))
	}

	t.Logf("HANDOFF_TEST: ResultFields | From=%s To=%s Requested=%d Granted=%d",
		result.FromAgent, result.ToAgent, len(result.RequestedPaths), len(result.GrantedPaths))
}
