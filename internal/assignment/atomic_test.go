package assignment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type atomicClaimLedger struct {
	mu    sync.Mutex
	owner map[string]string
	calls int
}

func (f *atomicClaimLedger) Claim(_ context.Context, beadID, actor string) (ClaimReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.owner == nil {
		f.owner = make(map[string]string)
	}
	if owner := f.owner[beadID]; owner != "" && owner != actor {
		return ClaimReceipt{}, ErrClaimConflict
	}
	f.owner[beadID] = actor
	return ClaimReceipt{
		BeadID: beadID, Actor: actor, Status: "in_progress",
		ClaimedAt: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (f *atomicClaimLedger) ReconcileClaim(_ context.Context, beadID, actor string) (ClaimReconciliation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := f.owner[beadID]
	switch {
	case owner == "":
		return ClaimReconciliation{State: ClaimReconciliationAbsent}, nil
	case owner == actor:
		return ClaimReconciliation{State: ClaimReconciliationOwned, Receipt: ClaimReceipt{
			BeadID: beadID, Actor: actor, Status: "in_progress",
			ClaimedAt: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		}}, nil
	default:
		return ClaimReconciliation{State: ClaimReconciliationConflict, Receipt: ClaimReceipt{BeadID: beadID, Actor: owner}}, nil
	}
}

type atomicReservationRecorder struct {
	calls atomic.Int32
}

func (f *atomicReservationRecorder) Reserve(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
	f.calls.Add(1)
	expiresAt := time.Date(2030, 7, 11, 13, 0, 0, 0, time.UTC)
	return LeaseReceipt{
		AgentName:      req.AgentName,
		Target:         req.Target,
		Requested:      append([]string(nil), req.RequestedPaths...),
		Granted:        []string{"internal/assignment/**"},
		ReservationIDs: []int{42},
		ExpiresAt:      &expiresAt,
	}, nil
}

type atomicDispatchRecorder struct {
	calls     atomic.Int32
	failFirst atomic.Bool
}

type atomicEligibilityRecorder struct {
	mu       sync.Mutex
	err      error
	requests []AssignmentEligibilityAuthorizationRequest
}

func (f *atomicEligibilityRecorder) AuthorizeAssignment(_ context.Context, req AssignmentEligibilityAuthorizationRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return f.err
}

func (f *atomicEligibilityRecorder) snapshot() []AssignmentEligibilityAuthorizationRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AssignmentEligibilityAuthorizationRequest(nil), f.requests...)
}

func (f *atomicDispatchRecorder) Dispatch(_ context.Context, _ DispatchRequest) (DispatchReceipt, error) {
	call := f.calls.Add(1)
	if call == 1 && f.failFirst.Load() {
		return DispatchReceipt{Duration: 5 * time.Millisecond}, GuaranteeNoActuation(errors.New("known send failure"))
	}
	return DispatchReceipt{DeliveryID: "delivery-1", Duration: 5 * time.Millisecond}, nil
}

func atomicTestRequest(key, target string) AtomicRequest {
	occupancyKey := target
	if !isPhysicalPaneOccupancyKey(occupancyKey) {
		paneID := 0
		for _, ch := range target {
			paneID = (paneID*31 + int(ch)) % 1_000_000
		}
		occupancyKey = fmt.Sprintf("%%%d", paneID)
	}
	return AtomicRequest{
		BeadID: "ntm-atomic", BeadTitle: "Atomic assignment",
		Target: target, OccupancyKey: occupancyKey, Pane: 1, AgentType: "codex", AgentName: target,
		Actor: "SharedAgent", Prompt: "work the bead", IdempotencyKey: key,
		ReservationTTL: time.Hour,
	}
}

func withOpenWorkStatus(coordinator *AtomicCoordinator) *AtomicCoordinator {
	return coordinator.WithWorkItemStatusPort(WorkItemStatusFunc(func(context.Context, string) (string, error) {
		return "open", nil
	}))
}

func prepareAtomicWorkingReplacement(t *testing.T, store *AssignmentStore, coordinator *AtomicCoordinator, request AtomicRequest) *Assignment {
	t.Helper()
	result, err := coordinator.Execute(t.Context(), request)
	if err != nil || !result.Sent {
		t.Fatalf("seed atomic assignment result=%+v error=%v", result, err)
	}
	if err := store.MarkWorking(request.BeadID); err != nil {
		t.Fatalf("mark seed assignment working: %v", err)
	}
	if _, err := store.BeginClearIfStatus(t.Context(), request.BeadID, time.Now().UTC(), StatusWorking); err != nil {
		t.Fatalf("begin replacement lease release: %v", err)
	}
	if _, err := store.RecordClearLeasesReleased(t.Context(), request.BeadID); err != nil {
		t.Fatalf("record replacement leases released: %v", err)
	}
	prepared := store.Get(request.BeadID)
	if prepared == nil || prepared.Status != StatusWorking || prepared.ClearState != ClearStateLeasesReleased {
		t.Fatalf("prepared replacement assignment = %+v", prepared)
	}
	return prepared
}

func TestAtomicWorkingReplacementPersistsNewGenerationAndReusesClaimActor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-working-replacement")
	var claimMu sync.Mutex
	var claimActors []string
	var guardedClaims []bool
	claimer := ClaimFunc(func(ctx context.Context, beadID, actor string) (ClaimReceipt, error) {
		claimMu.Lock()
		claimActors = append(claimActors, actor)
		guardedClaims = append(guardedClaims, NonTerminalClaimGuardRequired(ctx))
		claimMu.Unlock()
		return ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	})
	dispatcher := &atomicDispatchRecorder{}
	coordinator := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, nil, dispatcher))
	oldRequest := atomicTestRequest("replacement-old-key", "%301")
	oldRequest.AgentName = "OriginalAgent"
	oldRequest.Actor = "OriginalAgent"
	prepared := prepareAtomicWorkingReplacement(t, store, coordinator, oldRequest)
	retryCount := 3
	if err := store.Update(oldRequest.BeadID, AssignmentUpdate{RetryCount: &retryCount}); err != nil {
		t.Fatalf("record old retry count: %v", err)
	}
	oldActor := prepared.ClaimActor

	replacement := atomicTestRequest("replacement-new-key", "%302")
	replacement.AgentName = "ReplacementAgent"
	replacement.Actor = "ReplacementAgent"
	replacement.Pane = 2
	replacement.Prompt = "continue the bead on the replacement pane"
	replacement.BeadTitle = ""
	replacement.ReplaceWorkingAssignment = true
	result, err := coordinator.Execute(t.Context(), replacement)
	if err != nil || !result.Sent || result.Replayed || result.Recovered {
		t.Fatalf("replacement Execute result=%+v error=%v", result, err)
	}
	claimMu.Lock()
	gotActors := append([]string(nil), claimActors...)
	gotGuards := append([]bool(nil), guardedClaims...)
	claimMu.Unlock()
	if !reflect.DeepEqual(gotActors, []string{oldActor, oldActor}) {
		t.Fatalf("claim actors=%v, want old actor %q for both generations", gotActors, oldActor)
	}
	if !reflect.DeepEqual(gotGuards, []bool{true, true}) {
		t.Fatalf("nonterminal claim guards=%v, want both guarded", gotGuards)
	}
	stored := store.Get(replacement.BeadID)
	if stored == nil || stored.Status != StatusAssigned || stored.ClearState != ClearStateNone ||
		stored.IdempotencyKey != replacement.IdempotencyKey || stored.ClaimActor != oldActor || !stored.ClaimRequiresNonTerminal ||
		stored.DispatchTarget != replacement.Target || stored.OccupancyKey != replacement.OccupancyKey || stored.Pane != replacement.Pane ||
		stored.AgentName != replacement.AgentName || stored.PromptSent != replacement.Prompt || stored.PendingPrompt != "" || stored.DispatchState != DispatchSent ||
		stored.RetryCount != retryCount || stored.BeadTitle != oldRequest.BeadTitle {
		t.Fatalf("replacement durable generation = %+v", stored)
	}
}

func TestAtomicWorkingReplacementRejectsInvalidPriorState(t *testing.T) {
	tests := []struct {
		name       string
		beadID     string
		status     AssignmentStatus
		clearState AssignmentClearState
		seed       bool
	}{
		{name: "wrong bead", beadID: "ntm-different-bead", status: StatusWorking, clearState: ClearStateLeasesReleased, seed: true},
		{name: "working without clear barrier", beadID: "ntm-atomic", status: StatusWorking, seed: true},
		{name: "working while release in progress", beadID: "ntm-atomic", status: StatusWorking, clearState: ClearStateReservationReleasing, seed: true},
		{name: "assigned after lease release", beadID: "ntm-atomic", status: StatusAssigned, clearState: ClearStateLeasesReleased, seed: true},
		{name: "failed after lease release", beadID: "ntm-atomic", status: StatusFailed, clearState: ClearStateLeasesReleased, seed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("invalid-working-replacement-" + strings.ReplaceAll(test.name, " ", "-"))
			if test.seed {
				store.Assignments["ntm-atomic"] = &Assignment{
					BeadID: "ntm-atomic", BeadTitle: "Original", Pane: 1, AgentType: "codex", AgentName: "OriginalAgent",
					Status: test.status, AssignedAt: time.Now().UTC(), IdempotencyKey: "old-key", ClaimActor: "durable-old-actor",
					ClaimState: ClaimClaimed, ReservationState: ReservationReleased, DispatchState: DispatchSent,
					DispatchTarget: "%311", OccupancyKey: "%311", ClearState: test.clearState,
				}
				if err := store.Save(); err != nil {
					t.Fatalf("seed invalid prior: %v", err)
				}
			}
			var claimCalls atomic.Int32
			claimer := ClaimFunc(func(context.Context, string, string) (ClaimReceipt, error) {
				claimCalls.Add(1)
				return ClaimReceipt{}, errors.New("claim must not be reached")
			})
			dispatcher := &atomicDispatchRecorder{}
			request := atomicTestRequest("new-key", "%312")
			request.BeadID = test.beadID
			request.ReplaceWorkingAssignment = true
			result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
			if !errors.Is(err, ErrWorkingReplacementNotAllowed) || result.Sent {
				t.Fatalf("replacement result=%+v error=%v, want ErrWorkingReplacementNotAllowed", result, err)
			}
			if claimCalls.Load() != 0 || dispatcher.calls.Load() != 0 {
				t.Fatalf("invalid replacement crossed external boundary: claims=%d dispatches=%d", claimCalls.Load(), dispatcher.calls.Load())
			}
			if test.seed {
				stored := store.Get("ntm-atomic")
				if stored == nil || stored.IdempotencyKey != "old-key" || stored.DispatchTarget != "%311" || stored.ClearState != test.clearState {
					t.Fatalf("invalid replacement mutated durable prior: %+v", stored)
				}
			}
		})
	}
}

func TestRecordAtomicIntentWorkingReplacementRejectsChangedClaimActor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-working-replacement-actor-integrity")
	store.Assignments["ntm-atomic"] = &Assignment{
		BeadID: "ntm-atomic", BeadTitle: "Original", Pane: 1, AgentType: "codex", AgentName: "OriginalAgent",
		Status: StatusWorking, AssignedAt: time.Now().UTC(), IdempotencyKey: "old-key", ClaimActor: "durable-old-actor",
		ClaimState: ClaimClaimed, ReservationState: ReservationReleased, DispatchState: DispatchSent,
		DispatchTarget: "%316", OccupancyKey: "%316", ClearState: ClearStateLeasesReleased,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed working replacement actor: %v", err)
	}
	replacement := atomicTestRequest("new-key", "%317")
	replacement.ReplaceWorkingAssignment = true
	if _, err := store.RecordAtomicIntent(replacement, "different-actor", time.Now().UTC()); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("RecordAtomicIntent error=%v, want ErrClaimConflict", err)
	}
	stored := store.Get(replacement.BeadID)
	if stored == nil || stored.IdempotencyKey != "old-key" || stored.ClaimActor != "durable-old-actor" ||
		stored.DispatchTarget != "%316" || stored.ClearState != ClearStateLeasesReleased {
		t.Fatalf("changed replacement actor mutated durable prior: %+v", stored)
	}
}

func TestAtomicWorkingReplacementRejectsAgentNameAsClaimActorFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-working-replacement-missing-claim-actor")
	prior := &Assignment{
		BeadID: "ntm-atomic", BeadTitle: "Original", Pane: 1, AgentType: "codex", AgentName: "LegacyAgentName",
		Status: StatusWorking, AssignedAt: time.Now().UTC(), IdempotencyKey: "old-key",
		ClaimState: ClaimClaimed, ReservationState: ReservationReleased, DispatchState: DispatchSent,
		DispatchTarget: "%318", OccupancyKey: "%318", ClearState: ClearStateLeasesReleased,
	}
	store.Assignments[prior.BeadID] = prior
	if err := store.Save(); err != nil {
		t.Fatalf("seed working replacement: %v", err)
	}
	replacement := atomicTestRequest("new-key", "%319")
	replacement.ReplaceWorkingAssignment = true
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}

	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), replacement)
	if !errors.Is(err, ErrWorkingReplacementNotAllowed) || !strings.Contains(err.Error(), "no durable claim actor") {
		t.Fatalf("Execute result=%+v error=%v", result, err)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 || !reflect.DeepEqual(store.Get(prior.BeadID), prior) {
		t.Fatalf("claim-actor rejection mutated state: claims=%d dispatches=%d stored=%+v", claimer.calls, dispatcher.calls.Load(), store.Get(prior.BeadID))
	}
}

func TestAtomicWorkingReplacementRecoversReservationAndDispatchFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-working-replacement-recovery")
	claimer := &atomicClaimLedger{}
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(_ context.Context, request DispatchRequest) (DispatchReceipt, error) {
		call := dispatchCalls.Add(1)
		if request.IdempotencyKey == "replacement-recovery-new" && call == 2 {
			return DispatchReceipt{}, GuaranteeNoActuation(errors.New("replacement transport unavailable"))
		}
		return DispatchReceipt{DeliveryID: fmt.Sprintf("delivery-%d", call)}, nil
	})
	seedCoordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)
	oldRequest := atomicTestRequest("replacement-recovery-old", "%321")
	prepared := prepareAtomicWorkingReplacement(t, store, seedCoordinator, oldRequest)
	oldActor := prepared.ClaimActor

	var reservationCalls atomic.Int32
	reserver := ReservationFunc(func(_ context.Context, request ReservationRequest) (LeaseReceipt, error) {
		if reservationCalls.Add(1) == 1 {
			return LeaseReceipt{}, GuaranteeNoReservation(errors.New("reservation service unavailable"))
		}
		expiresAt := time.Now().UTC().Add(time.Hour)
		return LeaseReceipt{
			AgentName: request.AgentName, Target: request.Target,
			Requested: append([]string(nil), request.RequestedPaths...), Granted: append([]string(nil), request.RequestedPaths...),
			ReservationIDs: []int{321}, ExpiresAt: &expiresAt,
		}, nil
	})
	replacement := atomicTestRequest("replacement-recovery-new", "%322")
	replacement.AgentName = "RecoveryAgent"
	replacement.Actor = "DifferentNewActor"
	replacement.Pane = 2
	replacement.Prompt = "replacement recovery prompt"
	replacement.ReplaceWorkingAssignment = true
	replacement.RequireReservation = true
	replacement.RequestedPaths = []string{"internal/assignment/**"}
	coordinator := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, reserver, dispatcher))

	first, err := coordinator.Execute(t.Context(), replacement)
	if err == nil || first.Sent || !IsGuaranteedNoReservation(err) {
		t.Fatalf("reservation failure result=%+v error=%v", first, err)
	}
	stored := store.Get(replacement.BeadID)
	if stored == nil || stored.IdempotencyKey != replacement.IdempotencyKey || stored.ClaimActor != oldActor ||
		stored.DispatchTarget != replacement.Target || stored.OccupancyKey != replacement.OccupancyKey || stored.PendingPrompt != replacement.Prompt ||
		stored.Status != StatusClaimed || stored.ClearState != ClearStateNone || stored.ClaimState != ClaimClaimed ||
		stored.ReservationState != ReservationFailed || stored.DispatchState != DispatchPending {
		t.Fatalf("reservation failure lost replacement metadata: %+v", stored)
	}

	second, err := coordinator.Execute(t.Context(), replacement)
	if err == nil || second.Sent || !IsGuaranteedNoActuation(err) {
		t.Fatalf("dispatch failure result=%+v error=%v", second, err)
	}
	stored = store.Get(replacement.BeadID)
	if stored == nil || stored.IdempotencyKey != replacement.IdempotencyKey || stored.ClaimActor != oldActor ||
		stored.ReservationState != ReservationReserved || !stored.ReservationCompleted || !reflect.DeepEqual(stored.ReservationIDs, []int{321}) ||
		stored.DispatchState != DispatchPending || !strings.Contains(stored.LastDispatchError, "replacement transport unavailable") {
		t.Fatalf("dispatch failure lost recoverable replacement metadata: %+v", stored)
	}

	retry := replacement
	retry.ReplaceWorkingAssignment = false
	retry.Actor = oldActor
	third, err := coordinator.Execute(t.Context(), retry)
	if err != nil || !third.Sent || !third.Recovered {
		t.Fatalf("recovered replacement result=%+v error=%v", third, err)
	}
	stored = store.Get(replacement.BeadID)
	if stored == nil || stored.Status != StatusAssigned || stored.DispatchState != DispatchSent || stored.DispatchReceiptID == "" ||
		stored.IdempotencyKey != replacement.IdempotencyKey || stored.ClaimActor != oldActor || stored.DispatchTarget != replacement.Target {
		t.Fatalf("recovered replacement final ledger: %+v", stored)
	}
	if claimer.calls != 2 || reservationCalls.Load() != 2 || dispatchCalls.Load() != 3 {
		t.Fatalf("side effects claims=%d reservations=%d dispatches=%d, want 2/2/3", claimer.calls, reservationCalls.Load(), dispatchCalls.Load())
	}
}

func seedWorkingAtomicReservation(t *testing.T, session string) (*AssignmentStore, *atomicClaimLedger, AtomicRequest, *Assignment) {
	t.Helper()
	store := NewStore(session)
	claimer := &atomicClaimLedger{}
	request := atomicTestRequest("integrated-release-old", "%325")
	request.AgentName = "OriginalAgent"
	request.Actor = "OriginalAgent"
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/**", "internal/completion/**"}
	expiresAt := time.Now().UTC().Add(time.Hour)
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		return LeaseReceipt{
			AgentName: req.AgentName, Target: req.Target,
			Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...),
			ReservationIDs: []int{3251, 3252}, ExpiresAt: &expiresAt,
		}, nil
	})
	result, err := NewAtomicCoordinator(store, claimer, reserver, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if err != nil || !result.Sent {
		t.Fatalf("seed atomic reservation result=%+v error=%v", result, err)
	}
	if err := store.MarkWorking(request.BeadID); err != nil {
		t.Fatalf("mark seed working: %v", err)
	}
	working := store.Get(request.BeadID)
	if working == nil || working.Status != StatusWorking || working.ClearState != ClearStateNone {
		t.Fatalf("seed working assignment=%+v", working)
	}
	return store, claimer, request, working
}

func exactWorkingReleaseReceipt(current *Assignment) WorkingReplacementReleaseReceipt {
	paths := append([]string(nil), current.ReservedPaths...)
	if len(paths) == 0 {
		paths = append(paths, current.ReservationRequested...)
	}
	return WorkingReplacementReleaseReceipt{
		ReleasedPaths:          paths,
		ReleasedReservationIDs: append([]int(nil), current.ReservationIDs...),
	}
}

func TestAtomicEligibilityAuthorizationPrecedesAllNewIntentMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-eligibility-new-intent")
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	authorization := &atomicEligibilityRecorder{err: fmt.Errorf("%w: operator gate appeared", ErrClaimIneligible)}
	request := atomicTestRequest("eligibility-new-intent", "%351")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/**"}

	result, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher).
		WithAssignmentEligibilityAuthorizationPort(authorization).
		Execute(t.Context(), request)
	if !errors.Is(err, ErrClaimIneligible) || result.Sent || result.Assignment != nil {
		t.Fatalf("new-intent authorization result=%+v error=%v", result, err)
	}
	if stored := store.Get(request.BeadID); stored != nil {
		t.Fatalf("authorization rejection persisted an intent: %+v", stored)
	}
	if claimer.calls != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("authorization rejection side effects claim=%d reserve=%d dispatch=%d", claimer.calls, reserver.calls.Load(), dispatcher.calls.Load())
	}
	requests := authorization.snapshot()
	if len(requests) != 1 || !requests[0].AllowUnassignedOpen || requests[0].AllowOwnedOpen || requests[0].AllowOwnedInProgress {
		t.Fatalf("new-intent authorization requests=%+v", requests)
	}
}

func TestAtomicEligibilityAuthorizationRejectsSameKeyPendingRecoveryWithoutMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-eligibility-pending-recovery")
	request := atomicTestRequest("eligibility-pending-recovery", "%352")
	request.IntentSHA256 = PromptSHA256(request.Prompt)
	request.claimRequiresNonTerminal = true
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("seed pending intent: %v", err)
	}
	before := store.Get(request.BeadID)
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	authorization := &atomicEligibilityRecorder{err: fmt.Errorf("%w: blocker appeared", ErrClaimIneligible)}

	result, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher).
		WithAssignmentEligibilityAuthorizationPort(authorization).
		Execute(t.Context(), request)
	if !errors.Is(err, ErrClaimIneligible) || result.Sent {
		t.Fatalf("pending-recovery authorization result=%+v error=%v", result, err)
	}
	if after := store.Get(request.BeadID); !reflect.DeepEqual(after, before) {
		t.Fatalf("pending-recovery rejection mutated ledger: before=%+v after=%+v", before, after)
	}
	if claimer.calls != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("pending-recovery side effects claim=%d reserve=%d dispatch=%d", claimer.calls, reserver.calls.Load(), dispatcher.calls.Load())
	}
	requests := authorization.snapshot()
	if len(requests) != 1 || !requests[0].AllowUnassignedOpen || requests[0].AllowOwnedOpen || !requests[0].AllowOwnedInProgress {
		t.Fatalf("pending-recovery authorization requests=%+v", requests)
	}
}

func TestAtomicEligibilityAuthorizationPrecedesWorkingReplacementRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, claimer, _, working := seedWorkingAtomicReservation(t, "atomic-eligibility-replacement")
	before := store.Get(working.BeadID)
	claimCallsBefore := claimer.calls
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	authorization := &atomicEligibilityRecorder{err: fmt.Errorf("%w: operator gate appeared", ErrClaimIneligible)}
	var releaseCalls atomic.Int32
	releaser := WorkingReplacementReleaseFunc(func(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error) {
		releaseCalls.Add(1)
		return WorkingReplacementReleaseReceipt{}, nil
	})
	replacement := atomicTestRequest("eligibility-replacement-new", "%353")
	replacement.Pane = 2
	replacement.ReplaceWorkingAssignment = true

	result, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher).
		WithAssignmentEligibilityAuthorizationPort(authorization).
		WithWorkingReplacementAuthorizationPort(WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress", Assignee: working.ClaimActor}, nil
		})).
		WithWorkingReplacementReleasePort(releaser).
		Execute(t.Context(), replacement)
	if !errors.Is(err, ErrClaimIneligible) || result.Sent {
		t.Fatalf("replacement authorization result=%+v error=%v", result, err)
	}
	if after := store.Get(replacement.BeadID); !reflect.DeepEqual(after, before) {
		t.Fatalf("replacement authorization mutated source: before=%+v after=%+v", before, after)
	}
	if claimer.calls != claimCallsBefore || releaseCalls.Load() != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("replacement rejection side effects claim=%d/%d release=%d reserve=%d dispatch=%d", claimer.calls, claimCallsBefore, releaseCalls.Load(), reserver.calls.Load(), dispatcher.calls.Load())
	}
	requests := authorization.snapshot()
	if len(requests) != 1 || requests[0].AllowUnassignedOpen || requests[0].AllowOwnedOpen || !requests[0].AllowOwnedInProgress || requests[0].ClaimActor != working.ClaimActor {
		t.Fatalf("replacement authorization requests=%+v", requests)
	}
}

func TestAtomicSentReceiptReplayBypassesEligibilityAuthorization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-eligibility-sent-replay")
	request := atomicTestRequest("eligibility-sent-replay", "%354")
	seedResult, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if err != nil || !seedResult.Sent {
		t.Fatalf("seed sent receipt result=%+v error=%v", seedResult, err)
	}
	before := store.Get(request.BeadID)
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	authorization := &atomicEligibilityRecorder{err: fmt.Errorf("%w: late policy change", ErrClaimIneligible)}

	result, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher).
		WithAssignmentEligibilityAuthorizationPort(authorization).
		Execute(t.Context(), request)
	if err != nil || !result.Sent || !result.Replayed {
		t.Fatalf("sent replay result=%+v error=%v", result, err)
	}
	if len(authorization.snapshot()) != 0 || claimer.calls != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("sent replay consulted or mutated external state authorization=%d claim=%d reserve=%d dispatch=%d", len(authorization.snapshot()), claimer.calls, reserver.calls.Load(), dispatcher.calls.Load())
	}
	if after := store.Get(request.BeadID); !reflect.DeepEqual(after, before) {
		t.Fatalf("sent replay mutated receipt: before=%+v after=%+v", before, after)
	}
}

func TestAtomicWorkingReplacementReleasesExactPriorGenerationUnderLocks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, claimer, oldRequest, working := seedWorkingAtomicReservation(t, "atomic-integrated-working-release")
	var sequence []string
	var sequenceMu sync.Mutex
	record := func(value string) {
		sequenceMu.Lock()
		sequence = append(sequence, value)
		sequenceMu.Unlock()
	}
	preflight := PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		record("preflight")
		return PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: req.Prompt, DurableTitle: req.BeadTitle}, nil
	})
	authorization := WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
		record("authorization")
		return WorkingReplacementAuthorization{Status: "in_progress", Assignee: working.ClaimActor}, nil
	})
	releaser := WorkingReplacementReleaseFunc(func(_ context.Context, current *Assignment) (WorkingReplacementReleaseReceipt, error) {
		record("release")
		if current.IdempotencyKey != working.IdempotencyKey || current.ClearState != ClearStateReservationReleasing {
			t.Fatalf("release source=%+v, want old generation at durable release barrier", current)
		}
		return exactWorkingReleaseReceipt(current), nil
	})
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(_ context.Context, req DispatchRequest) (DispatchReceipt, error) {
		record("dispatch")
		dispatchCalls.Add(1)
		return DispatchReceipt{DeliveryID: "replacement-exact-receipt"}, nil
	})
	replacement := atomicTestRequest("integrated-release-new", "%326")
	replacement.AgentName = "ReplacementAgent"
	replacement.Pane = 2
	replacement.ReplaceWorkingAssignment = true
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher, preflight).
		WithWorkingReplacementAuthorizationPort(authorization).
		WithWorkingReplacementReleasePort(releaser)
	result, err := coordinator.Execute(t.Context(), replacement)
	if err != nil || !result.Sent {
		t.Fatalf("replacement result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(result.ReleasedPaths, oldRequest.RequestedPaths) || !reflect.DeepEqual(result.ReleasedReservationIDs, []int{3251, 3252}) {
		t.Fatalf("release receipt paths=%v ids=%v", result.ReleasedPaths, result.ReleasedReservationIDs)
	}
	if got := sequence; !reflect.DeepEqual(got, []string{"preflight", "authorization", "release", "dispatch"}) {
		t.Fatalf("replacement boundary order=%v, want preflight/authorization/release/dispatch", got)
	}
	stored := store.Get(replacement.BeadID)
	if stored == nil || stored.IdempotencyKey != replacement.IdempotencyKey || stored.Status != StatusAssigned || stored.ClearState != ClearStateNone ||
		stored.BeadTitle != replacement.BeadTitle || stored.DispatchReceiptID != "replacement-exact-receipt" {
		t.Fatalf("replacement durable generation=%+v", stored)
	}
	if dispatchCalls.Load() != 1 {
		t.Fatalf("replacement dispatch calls=%d, want 1", dispatchCalls.Load())
	}
}

func TestAtomicWorkingReplacementMalformedReleaseReceiptStaysRetryable(t *testing.T) {
	tests := []struct {
		name    string
		receipt WorkingReplacementReleaseReceipt
	}{
		{name: "missing path", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**"}, ReleasedReservationIDs: []int{3251, 3252}}},
		{name: "unexpected path", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/completion/**", "internal/other/**"}, ReleasedReservationIDs: []int{3251, 3252}}},
		{name: "duplicate path", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/assignment/**"}, ReleasedReservationIDs: []int{3251, 3252}}},
		{name: "empty path", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", " "}, ReleasedReservationIDs: []int{3251, 3252}}},
		{name: "missing id", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/completion/**"}, ReleasedReservationIDs: []int{3251}}},
		{name: "unexpected id", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/completion/**"}, ReleasedReservationIDs: []int{3251, 3252, 9999}}},
		{name: "duplicate id", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/completion/**"}, ReleasedReservationIDs: []int{3251, 3251}}},
		{name: "invalid id", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/assignment/**", "internal/completion/**"}, ReleasedReservationIDs: []int{3251, 0}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store, claimer, _, working := seedWorkingAtomicReservation(t, "atomic-malformed-release-"+strings.ReplaceAll(test.name, " ", "-"))
			var releaseCalls atomic.Int32
			releaser := WorkingReplacementReleaseFunc(func(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error) {
				releaseCalls.Add(1)
				return test.receipt, nil
			})
			dispatcher := &atomicDispatchRecorder{}
			replacement := atomicTestRequest("malformed-release-new", "%327")
			replacement.Pane = 2
			replacement.ReplaceWorkingAssignment = true
			coordinator := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, nil, dispatcher)).
				WithWorkingReplacementAuthorizationPort(WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
					return WorkingReplacementAuthorization{Status: "in_progress", Assignee: working.ClaimActor}, nil
				})).
				WithWorkingReplacementReleasePort(releaser)
			result, err := coordinator.Execute(t.Context(), replacement)
			if err == nil || result.Sent {
				t.Fatalf("malformed release result=%+v error=%v", result, err)
			}
			stored := store.Get(replacement.BeadID)
			if stored == nil || stored.IdempotencyKey != working.IdempotencyKey || stored.Status != StatusWorking ||
				stored.ClearState != ClearStateReservationReleasing || strings.TrimSpace(stored.ClearError) == "" {
				t.Fatalf("malformed release advanced or lost retry state: %+v", stored)
			}
			if releaseCalls.Load() != 1 || dispatcher.calls.Load() != 0 || claimer.calls != 1 {
				t.Fatalf("malformed receipt side effects release=%d claim=%d dispatch=%d", releaseCalls.Load(), claimer.calls, dispatcher.calls.Load())
			}
		})
	}
}

func TestAtomicWorkingReplacementEmptyExpectedSurfaceRejectsUnexpectedReceipt(t *testing.T) {
	for _, test := range []struct {
		name    string
		receipt WorkingReplacementReleaseReceipt
	}{
		{name: "unexpected path", receipt: WorkingReplacementReleaseReceipt{ReleasedPaths: []string{"internal/unexpected/**"}}},
		{name: "unexpected id", receipt: WorkingReplacementReleaseReceipt{ReleasedReservationIDs: []int{999}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("atomic-empty-release-surface-" + strings.ReplaceAll(test.name, " ", "-"))
			claimer := &atomicClaimLedger{}
			seedRequest := atomicTestRequest("empty-release-old", "%324")
			if result, err := NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{}).Execute(t.Context(), seedRequest); err != nil || !result.Sent {
				t.Fatalf("seed result=%+v error=%v", result, err)
			}
			if err := store.MarkWorking(seedRequest.BeadID); err != nil {
				t.Fatalf("mark seed working: %v", err)
			}
			working := store.Get(seedRequest.BeadID)
			releaser := WorkingReplacementReleaseFunc(func(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error) {
				return test.receipt, nil
			})
			replacement := atomicTestRequest("empty-release-new", "%323")
			replacement.Pane = 2
			replacement.ReplaceWorkingAssignment = true
			result, err := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{})).
				WithWorkingReplacementAuthorizationPort(WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
					return WorkingReplacementAuthorization{Status: "in_progress", Assignee: working.ClaimActor}, nil
				})).
				WithWorkingReplacementReleasePort(releaser).
				Execute(t.Context(), replacement)
			if err == nil || result.Sent {
				t.Fatalf("unexpected empty-surface receipt result=%+v error=%v", result, err)
			}
			stored := store.Get(seedRequest.BeadID)
			if stored == nil || stored.IdempotencyKey != seedRequest.IdempotencyKey || stored.ClearState != ClearStateReservationReleasing || stored.ClearError == "" {
				t.Fatalf("unexpected receipt advanced empty source: %+v", stored)
			}
		})
	}
}

func TestAtomicWorkingReplacementReleaseFailureRetriesFromDurableBarrier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, claimer, _, working := seedWorkingAtomicReservation(t, "atomic-working-release-retry")
	var releaseCalls atomic.Int32
	releaser := WorkingReplacementReleaseFunc(func(_ context.Context, current *Assignment) (WorkingReplacementReleaseReceipt, error) {
		if releaseCalls.Add(1) == 1 {
			return WorkingReplacementReleaseReceipt{}, errors.New("release response unavailable")
		}
		if current.ClearState != ClearStateReservationReleasing || strings.TrimSpace(current.ClearError) == "" {
			t.Fatalf("retry did not receive durable release failure: %+v", current)
		}
		return exactWorkingReleaseReceipt(working), nil
	})
	dispatcher := &atomicDispatchRecorder{}
	replacement := atomicTestRequest("working-release-retry-new", "%328")
	replacement.Pane = 2
	replacement.ReplaceWorkingAssignment = true
	coordinator := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, nil, dispatcher)).
		WithWorkingReplacementAuthorizationPort(WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress", Assignee: working.ClaimActor}, nil
		})).
		WithWorkingReplacementReleasePort(releaser)
	first, err := coordinator.Execute(t.Context(), replacement)
	if err == nil || first.Sent || first.Assignment == nil || first.Assignment.ClearState != ClearStateReservationReleasing {
		t.Fatalf("first release result=%+v error=%v", first, err)
	}
	second, err := coordinator.Execute(t.Context(), replacement)
	if err != nil || !second.Sent || second.Assignment == nil || second.Assignment.IdempotencyKey != replacement.IdempotencyKey {
		t.Fatalf("release retry result=%+v error=%v", second, err)
	}
	if releaseCalls.Load() != 2 || claimer.calls != 2 || dispatcher.calls.Load() != 1 {
		t.Fatalf("retry side effects releases=%d claims=%d dispatches=%d, want 2/2/1 replacement dispatch", releaseCalls.Load(), claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicWorkingReplacementAuthorizationAndPreflightBlockBeforeActuation(t *testing.T) {
	for _, test := range []struct {
		name          string
		preflight     PromptPreflightPort
		authorization func(string) (WorkingReplacementAuthorization, error)
		wantAuthCalls int32
		clearActor    bool
	}{
		{name: "missing authorization port"},
		{name: "open tracker", authorization: func(actor string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "open", Assignee: actor}, nil
		}, wantAuthCalls: 1},
		{name: "noncanonical tracker status", authorization: func(actor string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "IN_PROGRESS", Assignee: actor}, nil
		}, wantAuthCalls: 1},
		{name: "terminal tracker", authorization: func(actor string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "closed", Assignee: actor}, nil
		}, wantAuthCalls: 1},
		{name: "empty assignee", authorization: func(string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress"}, nil
		}, wantAuthCalls: 1},
		{name: "different assignee", authorization: func(string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress", Assignee: "DifferentActor"}, nil
		}, wantAuthCalls: 1},
		{name: "empty durable actor", authorization: func(string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress", Assignee: "OriginalAgent"}, nil
		}, clearActor: true},
		{name: "tracker error", authorization: func(string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{}, errors.New("tracker unavailable")
		}, wantAuthCalls: 1},
		{name: "blocked preflight", preflight: PromptPreflightFunc(func(context.Context, DispatchRequest) (PromptPreflightResult, error) {
			return PromptPreflightResult{}, errors.New("redaction blocked")
		}), authorization: func(actor string) (WorkingReplacementAuthorization, error) {
			return WorkingReplacementAuthorization{Status: "in_progress", Assignee: actor}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store, claimer, _, working := seedWorkingAtomicReservation(t, "atomic-release-gate-"+strings.ReplaceAll(test.name, " ", "-"))
			if test.clearActor {
				current := store.Get(working.BeadID)
				current.ClaimActor = ""
				store.Assignments[current.BeadID] = current
				if err := store.Save(); err != nil {
					t.Fatalf("clear durable claim actor: %v", err)
				}
			}
			var authCalls, releaseCalls, reserveCalls atomic.Int32
			releaser := WorkingReplacementReleaseFunc(func(context.Context, *Assignment) (WorkingReplacementReleaseReceipt, error) {
				releaseCalls.Add(1)
				return exactWorkingReleaseReceipt(working), nil
			})
			reserver := ReservationFunc(func(context.Context, ReservationRequest) (LeaseReceipt, error) {
				reserveCalls.Add(1)
				return LeaseReceipt{}, errors.New("reservation must not be reached")
			})
			dispatcher := &atomicDispatchRecorder{}
			replacement := atomicTestRequest("release-gate-new", "%329")
			replacement.Pane = 2
			replacement.ReplaceWorkingAssignment = true
			coordinator := NewAtomicCoordinator(store, claimer, reserver, dispatcher, test.preflight).
				WithWorkingReplacementReleasePort(releaser)
			if test.authorization != nil {
				coordinator.WithWorkingReplacementAuthorizationPort(WorkingReplacementAuthorizationFunc(func(context.Context, string) (WorkingReplacementAuthorization, error) {
					authCalls.Add(1)
					return test.authorization(working.ClaimActor)
				}))
			}
			result, err := coordinator.Execute(t.Context(), replacement)
			if err == nil || result.Sent || (test.preflight == nil && !errors.Is(err, ErrWorkingReplacementNotAllowed)) {
				t.Fatalf("gated replacement result=%+v error=%v", result, err)
			}
			if authCalls.Load() != test.wantAuthCalls || releaseCalls.Load() != 0 || reserveCalls.Load() != 0 || dispatcher.calls.Load() != 0 || claimer.calls != 1 {
				t.Fatalf("gated replacement side effects auth=%d release=%d reserve=%d dispatch=%d claim=%d", authCalls.Load(), releaseCalls.Load(), reserveCalls.Load(), dispatcher.calls.Load(), claimer.calls)
			}
			stored := store.Get(replacement.BeadID)
			if stored == nil || stored.IdempotencyKey != working.IdempotencyKey || stored.ClearState != ClearStateNone || stored.Status != StatusWorking {
				t.Fatalf("gated replacement mutated source: %+v", stored)
			}
		})
	}
}

func TestAtomicWorkingReplacementCannotTakeOccupiedCanonicalTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-working-replacement-occupied")
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)
	oldRequest := atomicTestRequest("occupied-replacement-old", "%331")
	prepared := prepareAtomicWorkingReplacement(t, store, coordinator, oldRequest)

	occupant := atomicTestRequest("occupied-target-owner", "%339")
	occupant.BeadID = "ntm-target-owner"
	occupant.Pane = 9
	if result, err := coordinator.Execute(t.Context(), occupant); err != nil || !result.Sent {
		t.Fatalf("seed target occupant result=%+v error=%v", result, err)
	}
	claimCalls := claimer.calls
	dispatchCalls := dispatcher.calls.Load()
	replacement := atomicTestRequest("occupied-replacement-new", "%339")
	replacement.Pane = 9
	replacement.ReplaceWorkingAssignment = true
	result, err := coordinator.Execute(t.Context(), replacement)
	if !errors.Is(err, ErrTargetOccupied) || result.Sent {
		t.Fatalf("occupied replacement result=%+v error=%v, want ErrTargetOccupied", result, err)
	}
	if claimer.calls != claimCalls || dispatcher.calls.Load() != dispatchCalls {
		t.Fatalf("occupied replacement crossed external boundary: claims=%d/%d dispatches=%d/%d", claimer.calls, claimCalls, dispatcher.calls.Load(), dispatchCalls)
	}
	stored := store.Get(oldRequest.BeadID)
	if stored == nil || stored.IdempotencyKey != prepared.IdempotencyKey || stored.DispatchTarget != prepared.DispatchTarget ||
		stored.Status != StatusWorking || stored.ClearState != ClearStateLeasesReleased {
		t.Fatalf("occupied replacement mutated prior: %+v", stored)
	}
}

func TestAtomicWorkingReplacementConcurrentGenerationsHaveOneWinner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "atomic-concurrent-working-replacement"
	store := NewStore(session)
	claimer := &atomicClaimLedger{}
	prepareAtomicWorkingReplacement(t, store, NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{}), atomicTestRequest("concurrent-old", "%341"))

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseDispatch := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseDispatch)
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(_ context.Context, request DispatchRequest) (DispatchReceipt, error) {
		if dispatchCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		return DispatchReceipt{DeliveryID: "replacement-" + request.IdempotencyKey}, nil
	})
	results := make(chan error, 2)
	start := make(chan struct{})
	for index, target := range []string{"%342", "%343"} {
		request := atomicTestRequest(fmt.Sprintf("concurrent-new-%d", index), target)
		request.Pane = index + 2
		request.ReplaceWorkingAssignment = true
		coordinator := withOpenWorkStatus(NewAtomicCoordinator(NewStore(session), claimer, nil, dispatcher))
		go func() {
			<-start
			_, err := coordinator.Execute(context.Background(), request)
			results <- err
		}()
	}
	close(start)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement winner did not start dispatch")
	}
	if dispatchCalls.Load() != 1 {
		t.Fatalf("concurrent replacement dispatches before release=%d, want 1", dispatchCalls.Load())
	}
	releaseDispatch()

	var successes, rejected int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrWorkingReplacementNotAllowed):
			rejected++
		default:
			t.Fatalf("unexpected concurrent replacement error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 || dispatchCalls.Load() != 1 || claimer.calls != 2 {
		t.Fatalf("success=%d rejected=%d claims=%d dispatches=%d, want 1/1/2/1", successes, rejected, claimer.calls, dispatchCalls.Load())
	}
	finalStore := NewStore(session)
	if err := finalStore.LoadStrict(); err != nil {
		t.Fatalf("reload concurrent replacement winner: %v", err)
	}
	stored := finalStore.Get("ntm-atomic")
	if stored == nil || stored.Status != StatusAssigned || stored.DispatchState != DispatchSent || stored.ClearState != ClearStateNone ||
		(stored.IdempotencyKey != "concurrent-new-0" && stored.IdempotencyKey != "concurrent-new-1") {
		t.Fatalf("concurrent replacement winner ledger: %+v", stored)
	}
}

func TestNewAssignmentIdempotencyKeyIsUnique(t *testing.T) {
	t.Parallel()
	first, err := NewAssignmentIdempotencyKey()
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	second, err := NewAssignmentIdempotencyKey()
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if len(first) != 64 || len(second) != 64 || first == second {
		t.Fatalf("keys first=%q second=%q", first, second)
	}
}

func TestAtomicAssignmentConcurrentContendersDispatchOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	start := make(chan struct{})
	results := make(chan error, 2)

	for _, attempt := range []struct {
		session string
		key     string
		target  string
	}{
		{session: "atomic-a", key: "attempt-a", target: "AgentA"},
		{session: "atomic-b", key: "attempt-b", target: "AgentA"},
	} {
		attempt := attempt
		go func() {
			<-start
			coordinator := NewAtomicCoordinator(NewStore(attempt.session), claimer, nil, dispatcher)
			_, err := coordinator.Execute(context.Background(), atomicTestRequest(attempt.key, attempt.target))
			results <- err
		}()
	}
	close(start)

	var successes, conflicts int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrClaimConflict):
			conflicts++
		default:
			t.Fatalf("unexpected contender result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 each", successes, conflicts)
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("dispatch calls=%d, want 1", got)
	}
}

func TestAtomicAssignmentConcurrentSameKeyDispatchesOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	started := make(chan struct{})
	release := make(chan struct{})
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(context.Context, DispatchRequest) (DispatchReceipt, error) {
		if dispatchCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		return DispatchReceipt{DeliveryID: "same-key-delivery"}, nil
	})

	const session = "atomic-concurrent-same-key"
	coordinators := []*AtomicCoordinator{
		NewAtomicCoordinator(NewStore(session), claimer, nil, dispatcher),
		NewAtomicCoordinator(NewStore(session), claimer, nil, dispatcher),
	}
	request := atomicTestRequest("shared-attempt", "AgentA")
	type outcome struct {
		result AtomicResult
		err    error
	}
	outcomes := make(chan outcome, len(coordinators))
	start := make(chan struct{})
	for _, coordinator := range coordinators {
		coordinator := coordinator
		go func() {
			<-start
			result, err := coordinator.Execute(context.Background(), request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first same-key dispatch did not start")
	}
	time.Sleep(100 * time.Millisecond)
	if got := dispatchCalls.Load(); got != 1 {
		t.Fatalf("concurrent same-key dispatch calls while first is active = %d, want 1", got)
	}
	close(release)

	replayed := 0
	for range coordinators {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("same-key Execute: %v", outcome.err)
		}
		if !outcome.result.Sent {
			t.Fatalf("same-key result did not report sent: %+v", outcome.result)
		}
		if outcome.result.Replayed {
			replayed++
		}
	}
	if replayed != 1 || dispatchCalls.Load() != 1 || claimer.calls != 1 {
		t.Fatalf("replayed=%d claim calls=%d dispatch calls=%d, want 1/1/1", replayed, claimer.calls, dispatchCalls.Load())
	}
}

func TestAtomicAssignmentDifferentBeadsRemainConcurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	started := make(chan string, 2)
	release := make(chan struct{})
	dispatcher := DispatchFunc(func(_ context.Context, request DispatchRequest) (DispatchReceipt, error) {
		started <- request.BeadID
		<-release
		return DispatchReceipt{DeliveryID: "delivery-" + request.BeadID}, nil
	})

	const session = "atomic-different-beads"
	results := make(chan error, 2)
	for index, beadID := range []string{"ntm-a", "ntm-b"} {
		request := atomicTestRequest("attempt-"+beadID, "Agent"+beadID)
		request.BeadID = beadID
		request.Pane = index + 1
		coordinator := NewAtomicCoordinator(NewStore(session), claimer, nil, dispatcher)
		go func() {
			_, err := coordinator.Execute(context.Background(), request)
			results <- err
		}()
	}

	seen := make(map[string]struct{}, 2)
	for range 2 {
		select {
		case beadID := <-started:
			seen[beadID] = struct{}{}
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("different-bead dispatches were serialized by the operation lock")
		}
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("different-bead Execute: %v", err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("started beads=%v, want both", seen)
	}
}

func TestAtomicAssignmentDifferentBeadsCannotOccupySameTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	started := make(chan struct{})
	release := make(chan struct{})
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(_ context.Context, request DispatchRequest) (DispatchReceipt, error) {
		if dispatchCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		return DispatchReceipt{DeliveryID: "delivery-" + request.BeadID}, nil
	})

	const session = "atomic-same-target"
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, beadID := range []string{"ntm-target-a", "ntm-target-b"} {
		request := atomicTestRequest("attempt-"+beadID, "%42")
		request.BeadID = beadID
		coordinator := NewAtomicCoordinator(NewStore(session), claimer, nil, dispatcher)
		go func() {
			<-start
			_, err := coordinator.Execute(context.Background(), request)
			results <- err
		}()
	}
	close(start)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("same-target winner did not start dispatch")
	}
	time.Sleep(100 * time.Millisecond)
	if got := dispatchCalls.Load(); got != 1 {
		t.Fatalf("same-target dispatch calls while winner active = %d, want 1", got)
	}
	close(release)

	var successes, occupied int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrTargetOccupied):
			occupied++
		default:
			t.Fatalf("same-target result: %v", err)
		}
	}
	claimer.mu.Lock()
	claimCalls := claimer.calls
	claimer.mu.Unlock()
	if successes != 1 || occupied != 1 || claimCalls != 1 || dispatchCalls.Load() != 1 {
		t.Fatalf("success=%d occupied=%d claims=%d dispatches=%d, want 1/1/1/1", successes, occupied, claimCalls, dispatchCalls.Load())
	}
}

func TestAtomicAssignmentWaitsForExternalCleanupLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "ntm-external-cleanup-lock"
	store := NewStore("atomic-external-cleanup-lock")
	cleanupUnlock, err := store.AcquireExternalCleanupLock(t.Context(), beadID)
	if err != nil {
		t.Fatalf("acquire external cleanup lock: %v", err)
	}
	defer cleanupUnlock()

	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	request := atomicTestRequest("external-cleanup-attempt", "%43")
	request.BeadID = beadID
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(ctx, request)
	if !errors.Is(err, context.DeadlineExceeded) || result.Sent {
		t.Fatalf("Execute while cleanup locked result=%+v error=%v", result, err)
	}
	claimer.mu.Lock()
	claimCalls := claimer.calls
	claimer.mu.Unlock()
	if claimCalls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("cleanup-locked Execute actuated claims=%d dispatches=%d", claimCalls, dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentCanonicalOccupancyRejectsSelectorAliases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	store := NewStore("atomic-alias-occupancy")
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)

	first := atomicTestRequest("alias-first", "AgentA")
	first.BeadID = "ntm-alias-a"
	first.Target = "0.1"
	first.OccupancyKey = "%42"
	if _, err := coordinator.Execute(t.Context(), first); err != nil {
		t.Fatalf("first alias Execute: %v", err)
	}

	second := atomicTestRequest("alias-second", "AgentB")
	second.BeadID = "ntm-alias-b"
	second.Target = "%42"
	second.OccupancyKey = "%42"
	if _, err := coordinator.Execute(t.Context(), second); !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("second alias Execute error=%v, want ErrTargetOccupied", err)
	}
	if claimer.calls != 1 || dispatcher.calls.Load() != 1 {
		t.Fatalf("alias conflict crossed external boundary: claims=%d dispatches=%d", claimer.calls, dispatcher.calls.Load())
	}
	if stored := store.Get(first.BeadID); stored == nil || stored.DispatchTarget != "0.1" || stored.OccupancyKey != "%42" {
		t.Fatalf("stored canonical occupancy = %+v", stored)
	}
}

func TestAtomicAssignmentDispatchOnlyCanonicalIdentityOccupiesTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-dispatch-only-occupancy")
	store.Assignments["ntm-dispatch-only"] = &Assignment{
		BeadID: "ntm-dispatch-only", Pane: 1, Status: StatusWorking, AssignedAt: time.Now().UTC(),
		DispatchTarget: "%42",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed dispatch-only assignment: %v", err)
	}
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	request := atomicTestRequest("dispatch-only-contender", "%42")
	request.BeadID = "ntm-contender"

	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
	if !errors.Is(err, ErrTargetOccupied) || result.Assignment == nil || result.Assignment.BeadID != "ntm-dispatch-only" {
		t.Fatalf("Execute result=%+v error=%v, want dispatch-only target occupant", result, err)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 || store.Get(request.BeadID) != nil {
		t.Fatalf("dispatch-only conflict crossed boundary: claims=%d dispatches=%d contender=%+v", claimer.calls, dispatcher.calls.Load(), store.Get(request.BeadID))
	}
}

func TestAtomicAssignmentRejectsLegacyOccupancyWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name           string
		dispatchTarget string
		occupancyKey   string
	}{
		{name: "missing target identity"},
		{name: "bare pane index", dispatchTarget: "1"},
		{name: "window address without stable occupancy", dispatchTarget: "0.1"},
		{name: "nonphysical occupancy", dispatchTarget: "0.1", occupancyKey: "0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("legacy-occupancy-" + strings.ReplaceAll(test.name, " ", "-"))
			store.Assignments["ntm-legacy"] = &Assignment{
				BeadID: "ntm-legacy", Pane: 1, Status: StatusWorking, AssignedAt: time.Now().UTC(),
				DispatchTarget: test.dispatchTarget, OccupancyKey: test.occupancyKey,
			}
			if err := store.Save(); err != nil {
				t.Fatalf("seed legacy assignment: %v", err)
			}
			claimer := &atomicClaimLedger{}
			dispatcher := &atomicDispatchRecorder{}
			request := atomicTestRequest("legacy-contender", "%42")
			request.BeadID = "ntm-contender"
			request.Pane = 1

			before := store.Get("ntm-legacy")
			result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
			var migrationErr *PaneIdentityMigrationError
			if !errors.Is(err, ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
				t.Fatalf("Execute error=%v, want typed pane identity migration error", err)
			}
			if migrationErr.BeadID != "ntm-legacy" || result.Assignment == nil || result.Assignment.BeadID != "ntm-legacy" {
				t.Fatalf("migration error=%+v result=%+v", migrationErr, result)
			}
			if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
				t.Fatalf("legacy occupancy crossed external boundary: claims=%d dispatches=%d", claimer.calls, dispatcher.calls.Load())
			}
			if after := store.Get("ntm-legacy"); !reflect.DeepEqual(after, before) || store.Get(request.BeadID) != nil {
				t.Fatalf("migration rejection mutated store: before=%+v after=%+v contender=%+v", before, after, store.Get(request.BeadID))
			}
		})
	}
}

func TestCanonicalPaneIdentityRequiresPhysicalPaneID(t *testing.T) {
	for _, test := range []struct {
		name       string
		assignment *Assignment
		want       string
		wantErr    bool
	}{
		{name: "zero pane ID", assignment: &Assignment{OccupancyKey: "%0"}, want: "%0"},
		{name: "occupancy pane ID", assignment: &Assignment{OccupancyKey: "%42", DispatchTarget: "1.2"}, want: "%42"},
		{name: "dispatch pane ID", assignment: &Assignment{DispatchTarget: "%43"}, want: "%43"},
		{name: "zero alias pane ID", assignment: &Assignment{OccupancyKey: "%00"}, wantErr: true},
		{name: "leading zero pane ID", assignment: &Assignment{OccupancyKey: "%01"}, wantErr: true},
		{name: "overflow pane ID", assignment: &Assignment{OccupancyKey: "%999999999999999999999999999999999999"}, wantErr: true},
		{name: "window pane address", assignment: &Assignment{DispatchTarget: "1.2"}, wantErr: true},
		{name: "session window pane address", assignment: &Assignment{DispatchTarget: "proj:1.2"}, wantErr: true},
		{name: "bare local index", assignment: &Assignment{DispatchTarget: "2"}, wantErr: true},
		{name: "invalid occupancy blocks fallback", assignment: &Assignment{OccupancyKey: "1.2", DispatchTarget: "%44"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := CanonicalPaneIdentity(test.assignment)
			if test.wantErr {
				var migrationErr *PaneIdentityMigrationError
				if !errors.Is(err, ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) || got != "" {
					t.Fatalf("CanonicalPaneIdentity()=%q error=%v, want typed migration error", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("CanonicalPaneIdentity()=%q error=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestAtomicAssignmentRejectsNoncanonicalRequestBeforeMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("noncanonical-request")
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	request := atomicTestRequest("noncanonical-request", "0.1")
	request.OccupancyKey = "0.1"

	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
	var migrationErr *PaneIdentityMigrationError
	if !errors.Is(err, ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) || result.Assignment != nil {
		t.Fatalf("Execute result=%+v error=%v, want typed request migration error", result, err)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 || len(store.List()) != 0 {
		t.Fatalf("invalid request mutated state: claims=%d dispatches=%d store=%+v", claimer.calls, dispatcher.calls.Load(), store.List())
	}
}

func TestAtomicAssignmentCanonicalPaneIDsAllowDuplicateLocalIndexes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("canonical-duplicate-index")
	store.Assignments["ntm-window-zero"] = &Assignment{
		BeadID: "ntm-window-zero", Pane: 1, Status: StatusWorking, AssignedAt: time.Now().UTC(),
		DispatchTarget: "%41", OccupancyKey: "%41",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed canonical assignment: %v", err)
	}
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	request := atomicTestRequest("other-window", "%42")
	request.BeadID = "ntm-window-one"
	request.Pane = 1

	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
	if err != nil || !result.Sent {
		t.Fatalf("Execute result=%+v error=%v", result, err)
	}
	if claimer.calls != 1 || dispatcher.calls.Load() != 1 {
		t.Fatalf("canonical duplicate-index actuation: claims=%d dispatches=%d", claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicFreshAssignmentCarriesNonTerminalClaimGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var guarded atomic.Bool
	claimer := ClaimFunc(func(ctx context.Context, beadID, actor string) (ClaimReceipt, error) {
		guarded.Store(NonTerminalClaimGuardRequired(ctx))
		return ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	})
	store := NewStore("fresh-claim-guard")
	request := atomicTestRequest("fresh-guard", "%43")
	result, err := NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if err != nil || !result.Sent || !guarded.Load() {
		t.Fatalf("Execute result=%+v error=%v guarded=%v", result, err, guarded.Load())
	}
	stored := store.Get(request.BeadID)
	if stored == nil || !stored.ClaimRequiresNonTerminal {
		t.Fatalf("fresh claim guard was not durable: %+v", stored)
	}
}

func TestAtomicRecoveredLegacyIntentBackfillsNonTerminalClaimGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("legacy-recovered-claim-guard")
	request := atomicTestRequest("legacy-guard", "%44")
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	if recorded, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("seed legacy intent: %v", err)
	} else if recorded.ClaimRequiresNonTerminal {
		t.Fatalf("legacy fixture unexpectedly has nonterminal guard: %+v", recorded)
	}
	var guarded atomic.Bool
	claimer := ClaimFunc(func(ctx context.Context, beadID, claimActor string) (ClaimReceipt, error) {
		guarded.Store(NonTerminalClaimGuardRequired(ctx))
		return ClaimReceipt{BeadID: beadID, Actor: claimActor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	})
	result, err := NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if err != nil || !result.Sent || !guarded.Load() {
		t.Fatalf("recovered Execute result=%+v error=%v guarded=%v", result, err, guarded.Load())
	}
	if stored := store.Get(request.BeadID); stored == nil || !stored.ClaimRequiresNonTerminal {
		t.Fatalf("legacy claim guard was not backfilled: %+v", stored)
	}
}

func TestAtomicAssignmentRejectsMismatchedClaimReceipts(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*ClaimReceipt)
	}{
		{name: "missing bead", alter: func(receipt *ClaimReceipt) { receipt.BeadID = "" }},
		{name: "wrong bead", alter: func(receipt *ClaimReceipt) { receipt.BeadID = "different-bead" }},
		{name: "missing actor", alter: func(receipt *ClaimReceipt) { receipt.Actor = "" }},
		{name: "wrong actor", alter: func(receipt *ClaimReceipt) { receipt.Actor = "different-actor" }},
		{name: "wrong status", alter: func(receipt *ClaimReceipt) { receipt.Status = "open" }},
		{name: "missing timestamp", alter: func(receipt *ClaimReceipt) { receipt.ClaimedAt = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dispatcher := &atomicDispatchRecorder{}
			store := NewStore("atomic-invalid-claim-" + strings.ReplaceAll(test.name, " ", "-"))
			claimer := ClaimFunc(func(_ context.Context, beadID, actor string) (ClaimReceipt, error) {
				receipt := ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}
				test.alter(&receipt)
				return receipt, nil
			})
			req := atomicTestRequest("invalid-claim-"+test.name, "AgentA")
			_, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), req)
			if !errors.Is(err, ErrClaimOutcomeUnknown) {
				t.Fatalf("Execute error=%v, want ErrClaimOutcomeUnknown", err)
			}
			if dispatcher.calls.Load() != 0 {
				t.Fatalf("invalid claim receipt dispatched %d times", dispatcher.calls.Load())
			}
			if stored := store.Get(req.BeadID); stored == nil || stored.ClaimState != ClaimUnknown || stored.ClaimError == "" {
				t.Fatalf("invalid claim state = %+v", stored)
			}
		})
	}
}

func TestAtomicAssignmentRejectsMalformedReconciledClaimReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := atomicTestRequest("malformed-reconciled-claim", "%78")
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	store := NewStore("atomic-malformed-reconciled-claim")
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaimStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicClaimStarted: %v", err)
	}
	// Override the durable owner timestamp with a zero timestamp by using a
	// dedicated reconciler instead of allowing the coordinator to normalize it.
	malformed := &malformedClaimReconciler{receipt: ClaimReceipt{BeadID: request.BeadID, Actor: actor, Status: "in_progress"}}
	result, err := NewAtomicCoordinator(store, malformed, nil, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if !errors.Is(err, ErrClaimOutcomeUnknown) || result.Sent {
		t.Fatalf("Execute result=%+v error=%v, want unknown malformed reconciliation", result, err)
	}
}

type malformedClaimReconciler struct {
	receipt ClaimReceipt
}

func (m *malformedClaimReconciler) Claim(context.Context, string, string) (ClaimReceipt, error) {
	return ClaimReceipt{}, errors.New("claim must not repeat after an ambiguous barrier")
}

func (m *malformedClaimReconciler) ReconcileClaim(context.Context, string, string) (ClaimReconciliation, error) {
	return ClaimReconciliation{State: ClaimReconciliationOwned, Receipt: m.receipt}, nil
}

func TestAtomicAssignmentSameIdempotencyReplaysWithoutSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(NewStore("atomic-replay"), claimer, reserver, dispatcher)
	req := atomicTestRequest("same-key", "AgentA")

	first, err := coordinator.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	replayRequest := req
	replayRequest.Pane = 9 // Display index drift must not change physical %N identity.
	second, err := coordinator.Execute(t.Context(), replayRequest)
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if !first.Sent || !second.Sent || !second.Replayed {
		t.Fatalf("unexpected results: first=%+v second=%+v", first, second)
	}
	if claimer.calls != 1 || reserver.calls.Load() != 1 || dispatcher.calls.Load() != 1 {
		t.Fatalf("side effects claim=%d reserve=%d dispatch=%d, want 1 each", claimer.calls, reserver.calls.Load(), dispatcher.calls.Load())
	}
	if second.Assignment == nil || second.Assignment.Pane != req.Pane {
		t.Fatalf("replay replaced durable display pane: %+v", second.Assignment)
	}
}

func TestAtomicAssignmentTerminalGenerationRequiresNewKeyAndReusesClaimActor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-terminal-generation")
	claimer := &atomicClaimLedger{}
	var dispatchCalls atomic.Int32
	dispatcher := DispatchFunc(func(context.Context, DispatchRequest) (DispatchReceipt, error) {
		call := dispatchCalls.Add(1)
		return DispatchReceipt{DeliveryID: fmt.Sprintf("delivery-%d", call)}, nil
	})
	trackerStatus := "closed"
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher).
		WithWorkItemStatusPort(WorkItemStatusFunc(func(context.Context, string) (string, error) {
			return trackerStatus, nil
		}))

	firstRequest := atomicTestRequest("generation-one", "%73")
	first, err := coordinator.Execute(t.Context(), firstRequest)
	if err != nil || !first.Sent || first.Replayed || first.Assignment == nil {
		t.Fatalf("first Execute=%+v err=%v", first, err)
	}
	firstActor := first.Assignment.ClaimActor
	if err := store.MarkCompleted(firstRequest.BeadID); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	staleReplay, err := coordinator.Execute(t.Context(), firstRequest)
	if !errors.Is(err, ErrTerminalAssignmentAttempt) || staleReplay.Sent || staleReplay.Replayed {
		t.Fatalf("terminal same-key Execute=%+v err=%v", staleReplay, err)
	}
	if dispatchCalls.Load() != 1 {
		t.Fatalf("terminal same-key retry dispatched %d times, want 1", dispatchCalls.Load())
	}

	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "generation-two"
	closedAttempt, err := coordinator.Execute(t.Context(), secondRequest)
	if !errors.Is(err, ErrTerminalAssignmentAttempt) || closedAttempt.Sent || closedAttempt.Replayed {
		t.Fatalf("closed tracker generation Execute=%+v err=%v", closedAttempt, err)
	}
	if stored := store.Get(firstRequest.BeadID); stored == nil || stored.IdempotencyKey != firstRequest.IdempotencyKey || stored.Status != StatusCompleted {
		t.Fatalf("closed tracker attempt replaced terminal receipt: %+v", stored)
	}
	if dispatchCalls.Load() != 1 || claimer.calls != 1 {
		t.Fatalf("closed tracker attempt actuated claim=%d dispatch=%d", claimer.calls, dispatchCalls.Load())
	}

	trackerStatus = "open"
	second, err := coordinator.Execute(t.Context(), secondRequest)
	if err != nil || !second.Sent || second.Replayed || second.Assignment == nil {
		t.Fatalf("second generation Execute=%+v err=%v", second, err)
	}
	if second.Assignment.IdempotencyKey != secondRequest.IdempotencyKey || second.Assignment.ClaimActor != firstActor {
		t.Fatalf("second generation identity=%+v, want key %q and retained actor %q", second.Assignment, secondRequest.IdempotencyKey, firstActor)
	}
	if second.Dispatch.DeliveryID != "delivery-2" || dispatchCalls.Load() != 2 || claimer.calls != 2 {
		t.Fatalf("second generation dispatch=%+v claim calls=%d dispatch calls=%d", second.Dispatch, claimer.calls, dispatchCalls.Load())
	}
}

func TestAtomicReopenedGenerationWaitsForExactCompletionEventAcknowledgement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-completion-event-reopen")
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := withOpenWorkStatus(NewAtomicCoordinator(store, claimer, nil, dispatcher))
	firstRequest := atomicTestRequest("completion-generation-one", "%74")
	first, err := coordinator.Execute(t.Context(), firstRequest)
	if err != nil || !first.Sent || first.Assignment == nil {
		t.Fatalf("seed generation result=%+v error=%v", first, err)
	}

	barrier, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), first.Assignment, StatusCompleted, "")
	if err != nil || !applied || barrier == nil || barrier.PendingCompletionEventID == "" {
		t.Fatalf("completion barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	if _, err := store.RecordClearLeasesReleased(t.Context(), firstRequest.BeadID); err != nil {
		t.Fatalf("record leases released: %v", err)
	}
	if _, err := store.RecordTerminalClaimReleased(t.Context(), firstRequest.BeadID); err != nil {
		t.Fatalf("record claim released: %v", err)
	}
	if err := store.CompleteTerminalReconciliation(t.Context(), firstRequest.BeadID, StatusCompleted, ""); err != nil {
		t.Fatalf("complete first generation: %v", err)
	}
	eventID := barrier.PendingCompletionEventID
	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "completion-generation-two"

	actor := StableClaimActor(secondRequest.Actor, secondRequest.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(secondRequest, actor, time.Now().UTC()); !errors.Is(err, ErrCompletionEventPending) {
		t.Fatalf("RecordAtomicIntent error=%v, want ErrCompletionEventPending", err)
	}
	blocked, err := coordinator.Execute(t.Context(), secondRequest)
	if !errors.Is(err, ErrCompletionEventPending) || blocked.Sent || blocked.Replayed {
		t.Fatalf("blocked reopened generation result=%+v error=%v", blocked, err)
	}
	if claimer.calls != 1 || dispatcher.calls.Load() != 1 {
		t.Fatalf("pending outbox crossed external boundary: claims=%d dispatches=%d", claimer.calls, dispatcher.calls.Load())
	}
	if current := store.Get(firstRequest.BeadID); current == nil || current.IdempotencyKey != firstRequest.IdempotencyKey || current.PendingCompletionEventID != eventID {
		t.Fatalf("pending outbox generation was replaced: %+v", current)
	}

	const consumerToken = "atomic-reopen-consumer"
	if _, acquired, err := store.ClaimPendingCompletionEvent(t.Context(), firstRequest.BeadID, eventID, consumerToken, time.Minute); err != nil || !acquired {
		t.Fatalf("claim completion event acquired=%v error=%v", acquired, err)
	}
	if acknowledged, err := store.AcknowledgeCompletionEvent(t.Context(), firstRequest.BeadID, eventID, consumerToken); err != nil || !acknowledged {
		t.Fatalf("acknowledge completion event applied=%v error=%v", acknowledged, err)
	}
	second, err := coordinator.Execute(t.Context(), secondRequest)
	if err != nil || !second.Sent || second.Replayed || second.Assignment == nil || second.Assignment.IdempotencyKey != secondRequest.IdempotencyKey {
		t.Fatalf("acknowledged reopened generation result=%+v error=%v", second, err)
	}
	if claimer.calls != 2 || dispatcher.calls.Load() != 2 {
		t.Fatalf("acknowledged generation side effects claims=%d dispatches=%d, want 2 each", claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicTerminalGenerationFailsClosedWithoutReopenProof(t *testing.T) {
	tests := []struct {
		name string
		port WorkItemStatusPort
	}{
		{name: "missing status port"},
		{name: "status read error", port: WorkItemStatusFunc(func(context.Context, string) (string, error) {
			return "", errors.New("tracker unavailable")
		})},
		{name: "terminal tracker status", port: WorkItemStatusFunc(func(context.Context, string) (string, error) {
			return "tombstone", nil
		})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("terminal-proof-" + strings.ReplaceAll(test.name, " ", "-"))
			request := atomicTestRequest("new-generation", "%81")
			store.Assignments[request.BeadID] = &Assignment{
				BeadID: request.BeadID, BeadTitle: request.BeadTitle, Pane: request.Pane,
				AgentType: request.AgentType, AgentName: request.AgentName,
				Status: StatusCompleted, AssignedAt: time.Now().UTC(),
				IdempotencyKey: "old-generation", ClaimActor: "retained-actor",
				DispatchTarget: request.Target, OccupancyKey: request.OccupancyKey,
				DispatchState: DispatchSent, DispatchReceiptID: "old-receipt",
			}
			if err := store.Save(); err != nil {
				t.Fatalf("seed terminal assignment: %v", err)
			}
			claimer := &atomicClaimLedger{}
			dispatcher := &atomicDispatchRecorder{}
			coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)
			if test.port != nil {
				coordinator.WithWorkItemStatusPort(test.port)
			}

			result, err := coordinator.Execute(t.Context(), request)
			if !errors.Is(err, ErrTerminalAssignmentAttempt) || result.Sent || result.Replayed {
				t.Fatalf("Execute=%+v err=%v, want terminal proof failure", result, err)
			}
			stored := store.Get(request.BeadID)
			if stored == nil || stored.IdempotencyKey != "old-generation" || stored.DispatchReceiptID != "old-receipt" || stored.Status != StatusCompleted {
				t.Fatalf("terminal proof failure mutated ledger: %+v", stored)
			}
			if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
				t.Fatalf("terminal proof failure actuated claim=%d dispatch=%d", claimer.calls, dispatcher.calls.Load())
			}
		})
	}
}

func TestAtomicTerminalGenerationCarriesNonTerminalClaimGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("terminal-claim-guard")
	request := atomicTestRequest("guarded-generation", "%82")
	store.Assignments[request.BeadID] = &Assignment{
		BeadID: request.BeadID, BeadTitle: request.BeadTitle, Pane: request.Pane,
		AgentType: request.AgentType, AgentName: request.AgentName,
		Status: StatusCompleted, AssignedAt: time.Now().UTC(),
		IdempotencyKey: "old-generation", ClaimActor: "retained-actor",
		DispatchTarget: request.Target, OccupancyKey: request.OccupancyKey,
		DispatchState: DispatchSent, DispatchReceiptID: "old-receipt",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal assignment: %v", err)
	}
	var guarded atomic.Bool
	claimer := ClaimFunc(func(ctx context.Context, beadID, actor string) (ClaimReceipt, error) {
		guarded.Store(NonTerminalClaimGuardRequired(ctx))
		return ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	})
	result, err := NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{}).
		WithWorkItemStatusPort(WorkItemStatusFunc(func(context.Context, string) (string, error) {
			return "open", nil
		})).Execute(t.Context(), request)
	if err != nil || !result.Sent || !guarded.Load() {
		t.Fatalf("guarded terminal generation result=%+v err=%v guarded=%v", result, err, guarded.Load())
	}
	stored := store.Get(request.BeadID)
	if stored == nil || !stored.ClaimRequiresNonTerminal {
		t.Fatalf("terminal claim guard was not durable: %+v", stored)
	}
}

func TestAtomicTerminalGenerationRefusesUnreleasedLeaseHandles(t *testing.T) {
	for _, test := range []struct {
		name      string
		expiresAt time.Time
	}{
		{name: "future expiry", expiresAt: time.Now().UTC().Add(time.Hour)},
		{name: "stale expiry", expiresAt: time.Now().UTC().Add(-time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("terminal-live-lease-" + strings.ReplaceAll(test.name, " ", "-"))
			request := atomicTestRequest("replacement-generation", "%83")
			store.Assignments[request.BeadID] = &Assignment{
				BeadID: request.BeadID, BeadTitle: request.BeadTitle, Pane: request.Pane,
				AgentType: request.AgentType, AgentName: request.AgentName,
				Status: StatusCompleted, AssignedAt: time.Now().UTC(),
				IdempotencyKey: "old-generation", ClaimActor: "retained-actor",
				DispatchTarget: request.Target, OccupancyKey: request.OccupancyKey,
				DispatchState: DispatchSent, DispatchReceiptID: "old-receipt",
				ReservationState: ReservationReserved, ReservationCompleted: true,
				ReservedPaths: []string{"internal/assignment/**"}, ReservationIDs: []int{83},
				ReservationExpiresAt: &test.expiresAt,
			}
			if err := store.Save(); err != nil {
				t.Fatalf("seed terminal assignment: %v", err)
			}
			claimer := &atomicClaimLedger{}
			dispatcher := &atomicDispatchRecorder{}
			result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).
				WithWorkItemStatusPort(WorkItemStatusFunc(func(context.Context, string) (string, error) {
					return "open", nil
				})).Execute(t.Context(), request)
			if !errors.Is(err, ErrReservationReleaseRequired) || result.Sent {
				t.Fatalf("Execute=%+v err=%v, want release-required refusal", result, err)
			}
			stored := store.Get(request.BeadID)
			if stored == nil || stored.IdempotencyKey != "old-generation" || len(stored.ReservationIDs) != 1 || stored.DispatchReceiptID != "old-receipt" {
				t.Fatalf("terminal lease refusal lost durable handles: %+v", stored)
			}
			if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
				t.Fatalf("release-required replacement actuated claim=%d dispatch=%d", claimer.calls, dispatcher.calls.Load())
			}
		})
	}
}

func TestAtomicTerminalGenerationRefusesAmbiguousReservationWithoutHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("terminal-ambiguous-reservation")
	request := atomicTestRequest("replacement-generation", "%84")
	store.Assignments[request.BeadID] = &Assignment{
		BeadID: request.BeadID, BeadTitle: request.BeadTitle, Pane: request.Pane,
		AgentType: request.AgentType, AgentName: request.AgentName,
		Status: StatusCompleted, AssignedAt: time.Now().UTC(),
		IdempotencyKey: "old-generation", ClaimActor: "retained-actor",
		DispatchTarget: request.Target, OccupancyKey: request.OccupancyKey,
		DispatchState: DispatchSent, DispatchReceiptID: "old-receipt",
		ReservationRequired: true, ReservationState: ReservationUnknown,
		ReservationAttempts: 1, ReservationError: ErrReservationOutcomeUnknown.Error(),
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal assignment: %v", err)
	}
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).
		WithWorkItemStatusPort(WorkItemStatusFunc(func(context.Context, string) (string, error) {
			return "open", nil
		})).Execute(t.Context(), request)
	if !errors.Is(err, ErrReservationReleaseRequired) || result.Sent {
		t.Fatalf("Execute=%+v err=%v, want reconcile-required refusal", result, err)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.IdempotencyKey != "old-generation" || stored.ReservationState != ReservationUnknown ||
		stored.ReservationAttempts != 1 || stored.DispatchReceiptID != "old-receipt" {
		t.Fatalf("terminal ambiguous reservation was overwritten: %+v", stored)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("ambiguous replacement actuated claim=%d dispatch=%d", claimer.calls, dispatcher.calls.Load())
	}
}

func TestDispatchDeliveryIDIsStablePerGeneration(t *testing.T) {
	first := DispatchDeliveryID("%7", "double_enter", "generation-one")
	if first == "" || first != DispatchDeliveryID("%7", "double_enter", "generation-one") {
		t.Fatalf("same generation receipt is not stable: %q", first)
	}
	if second := DispatchDeliveryID("%7", "double_enter", "generation-two"); second == first {
		t.Fatalf("independent generations reused receipt %q", first)
	}
	if !strings.Contains(first, "%7") || !strings.Contains(first, "generation-one") {
		t.Fatalf("receipt %q omitted route or generation identity", first)
	}
}

func TestAtomicAssignmentCompletedReplayBypassesChangedPreflightPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-preflight-replay")
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	preflightCalls := 0
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher, PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		preflightCalls++
		return PromptPreflightResult{DispatchPrompt: "token=[REDACTED]", DurablePrompt: "token=[REDACTED]", DurableTitle: "safe title"}, nil
	}))
	req := atomicTestRequest("preflight-replay-key", "%73")
	req.Prompt = "token=raw-secret"
	first, err := coordinator.Execute(t.Context(), req)
	if err != nil || !first.Sent || first.Replayed {
		t.Fatalf("first Execute=%+v err=%v", first, err)
	}
	coordinator.preflight = PromptPreflightFunc(func(context.Context, DispatchRequest) (PromptPreflightResult, error) {
		preflightCalls++
		return PromptPreflightResult{}, errors.New("new policy blocks prompt")
	})
	second, err := coordinator.Execute(t.Context(), req)
	if err != nil || !second.Sent || !second.Replayed {
		t.Fatalf("replay Execute=%+v err=%v", second, err)
	}
	claimer.mu.Lock()
	claimCalls := claimer.calls
	claimer.mu.Unlock()
	if preflightCalls != 1 || claimCalls != 1 || dispatcher.calls.Load() != 1 {
		t.Fatalf("calls preflight=%d claim=%d dispatch=%d, want 1/1/1", preflightCalls, claimCalls, dispatcher.calls.Load())
	}
	if second.Assignment == nil || second.Assignment.BeadTitle != "safe title" {
		t.Fatalf("replay lost durable sanitized title: %+v", second.Assignment)
	}
}

func TestAtomicAssignmentPreflightSanitizesLedgerBeforeClaim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	var delivered string
	var deliveredTitle string
	dispatcher := DispatchFunc(func(_ context.Context, req DispatchRequest) (DispatchReceipt, error) {
		delivered = req.Prompt
		deliveredTitle = req.BeadTitle
		return DispatchReceipt{DeliveryID: "safe-delivery"}, nil
	})
	preflight := PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		if !strings.Contains(req.Prompt, "raw-secret") {
			t.Fatalf("preflight did not receive raw intent: %q", req.Prompt)
		}
		return PromptPreflightResult{
			DispatchPrompt: "deliver [REDACTED]",
			DurablePrompt:  "persist [REDACTED]",
			DurableTitle:   "title [REDACTED]",
		}, nil
	})
	store := NewStore("atomic-preflight-safe")
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher, preflight)
	req := atomicTestRequest("safe-key", "%91")
	req.Prompt = "deliver raw-secret"
	result, err := coordinator.Execute(t.Context(), req)
	if err != nil || !result.Sent {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	stored := store.Get(req.BeadID)
	if stored == nil || stored.BeadTitle != "title [REDACTED]" || stored.PromptSent != "persist [REDACTED]" || stored.PendingPrompt != "" ||
		strings.Contains(stored.PromptSent, "raw-secret") || strings.Contains(stored.PromptSHA256, "raw-secret") ||
		stored.IntentSHA256 != PromptSHA256(req.Prompt) {
		t.Fatalf("durable assignment leaked or mismatched intent: %+v", stored)
	}
	if delivered != "deliver [REDACTED]" {
		t.Fatalf("dispatch prompt = %q", delivered)
	}
	if deliveredTitle != "title [REDACTED]" {
		t.Fatalf("dispatch title = %q", deliveredTitle)
	}
}

func TestAtomicAssignmentReservationDiscoveryUsesSanitizedTitleAndPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const rawSecret = "sk-proj-sensitive-token"
	const durablePath = "internal/x/[REDACTED].txt"
	var reservationTitle string
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		reservationTitle = req.BeadTitle
		expiresAt := time.Now().UTC().Add(time.Hour)
		return LeaseReceipt{
			AgentName: req.AgentName, Target: req.Target,
			Requested: []string{durablePath}, Granted: []string{durablePath},
			ReservationIDs: []int{911}, ExpiresAt: &expiresAt,
		}, nil
	})
	preflight := PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		if !strings.Contains(req.BeadTitle, rawSecret) {
			t.Fatalf("preflight did not receive raw title: %q", req.BeadTitle)
		}
		return PromptPreflightResult{
			DispatchPrompt: req.Prompt,
			DurablePrompt:  req.Prompt,
			DurableTitle:   "Work in " + durablePath,
		}, nil
	})
	store := NewStore("atomic-sanitized-reservation-discovery")
	request := atomicTestRequest("sanitized-reservation-discovery", "%92")
	request.BeadTitle = "Work in internal/x/" + rawSecret + ".txt"
	request.RequireReservation = true
	request.AllowReservationDiscovery = true
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, reserver, &atomicDispatchRecorder{}, preflight).Execute(t.Context(), request)
	if err != nil || !result.Sent {
		t.Fatalf("sanitized reservation result=%+v error=%v", result, err)
	}
	stored := store.Get(request.BeadID)
	if strings.Contains(reservationTitle, rawSecret) || reservationTitle != "Work in "+durablePath {
		t.Fatalf("reservation discovery title leaked raw secret: %q", reservationTitle)
	}
	if stored == nil || strings.Contains(stored.BeadTitle, rawSecret) || strings.Contains(strings.Join(stored.ReservationRequested, " "), rawSecret) ||
		stored.BeadTitle != "Work in "+durablePath || !reflect.DeepEqual(stored.ReservationRequested, []string{durablePath}) {
		t.Fatalf("durable reservation metadata leaked raw title: %+v", stored)
	}
}

func TestAtomicAssignmentPreflightCanBlockSensitiveExplicitReservationPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sensitivePath = "internal/x/sk-proj-sensitive-token.txt"
	var preflightPaths []string
	preflight := PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		preflightPaths = append([]string(nil), req.RequestedPaths...)
		if slices := strings.Join(req.RequestedPaths, "\n"); strings.Contains(slices, "sk-proj-") {
			return PromptPreflightResult{}, errors.New("reservation path blocked by redaction policy")
		}
		return PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: req.Prompt, DurableTitle: req.BeadTitle}, nil
	})
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	store := NewStore("atomic-sensitive-explicit-reservation-path")
	request := atomicTestRequest("sensitive-explicit-reservation-path", "%94")
	request.RequireReservation = true
	request.RequestedPaths = []string{sensitivePath}
	result, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher, preflight).Execute(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "reservation path blocked") || result.Sent {
		t.Fatalf("sensitive path result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(preflightPaths, []string{sensitivePath}) {
		t.Fatalf("preflight paths=%v, want exact requested path", preflightPaths)
	}
	if claimer.calls != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 || store.Get(request.BeadID) != nil {
		t.Fatalf("sensitive path crossed boundary: claims=%d reservations=%d dispatches=%d stored=%+v",
			claimer.calls, reserver.calls.Load(), dispatcher.calls.Load(), store.Get(request.BeadID))
	}
}

func TestAtomicAssignmentBlockedPreflightHasNoExternalSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	preflight := PromptPreflightFunc(func(context.Context, DispatchRequest) (PromptPreflightResult, error) {
		return PromptPreflightResult{}, errors.New("sensitive prompt blocked")
	})
	store := NewStore("atomic-preflight-blocked")
	coordinator := NewAtomicCoordinator(store, claimer, reserver, dispatcher, preflight)
	req := atomicTestRequest("blocked-key", "%92")
	req.RequestedPaths = []string{"internal/**"}
	req.RequireReservation = true
	_, err := coordinator.Execute(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("blocked Execute error = %v", err)
	}
	claimer.mu.Lock()
	claimCalls := claimer.calls
	claimer.mu.Unlock()
	if claimCalls != 0 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 || store.Get(req.BeadID) != nil {
		t.Fatalf("blocked preflight side effects: claims=%d reservations=%d dispatches=%d stored=%+v",
			claimCalls, reserver.calls.Load(), dispatcher.calls.Load(), store.Get(req.BeadID))
	}
}

func TestAtomicAssignmentEligibilityRejectionStopsReservationAndDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var claimCalls atomic.Int32
	claimer := ClaimFunc(func(context.Context, string, string) (ClaimReceipt, error) {
		claimCalls.Add(1)
		return ClaimReceipt{}, fmt.Errorf("%w: dependency added at claim boundary", ErrClaimIneligible)
	})
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	store := NewStore("atomic-eligibility-rejected")
	coordinator := NewAtomicCoordinator(store, claimer, reserver, dispatcher)
	req := atomicTestRequest("eligibility-rejected", "%95")
	req.RequireReservation = true
	req.RequestedPaths = []string{"internal/assignment/**"}

	result, err := coordinator.Execute(t.Context(), req)
	if !errors.Is(err, ErrClaimIneligible) || result.Sent {
		t.Fatalf("eligibility rejection result=%+v error=%v", result, err)
	}
	stored := store.Get(req.BeadID)
	if claimCalls.Load() != 1 || reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("eligibility rejection side effects: claims=%d reservations=%d dispatches=%d",
			claimCalls.Load(), reserver.calls.Load(), dispatcher.calls.Load())
	}
	if stored == nil || stored.ClaimState != ClaimIneligible || stored.Status != StatusFailed ||
		stored.ClaimAttempts != 1 || stored.ReservationAttempts != 0 || stored.DispatchAttempts != 0 {
		t.Fatalf("eligibility rejection durable row=%+v", stored)
	}

	replay, replayErr := coordinator.Execute(t.Context(), req)
	if !errors.Is(replayErr, ErrClaimIneligible) || replay.Sent || claimCalls.Load() != 1 ||
		reserver.calls.Load() != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("eligibility replay result=%+v error=%v claims=%d reservations=%d dispatches=%d",
			replay, replayErr, claimCalls.Load(), reserver.calls.Load(), dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRejectsSameKeyForDifferentIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(NewStore("atomic-key-reuse"), claimer, nil, dispatcher)
	req := atomicTestRequest("same-key", "AgentA")
	if _, err := coordinator.Execute(t.Context(), req); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	req.Target = "AgentB"
	if _, err := coordinator.Execute(t.Context(), req); err == nil {
		t.Fatal("expected changed assignment intent to be rejected")
	}
	if dispatcher.calls.Load() != 1 {
		t.Fatalf("dispatch calls=%d, want 1", dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRequiredReservationFailsBeforeClaimOrDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(NewStore("atomic-required-reservation"), claimer, nil, dispatcher)
	req := atomicTestRequest("required-key", "AgentA")
	req.RequireReservation = true
	req.RequestedPaths = []string{"internal/assignment/**"}

	result, err := coordinator.Execute(t.Context(), req)
	if !errors.Is(err, ErrReservationRequired) {
		t.Fatalf("Execute error=%v, want ErrReservationRequired", err)
	}
	if result.Assignment != nil || claimer.calls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("side effects result=%+v claim=%d dispatch=%d", result, claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRequestedPathsImplyRequiredReservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(NewStore("atomic-path-reservation"), claimer, nil, dispatcher)
	req := atomicTestRequest("path-key", "AgentA")
	req.RequestedPaths = []string{"internal/assignment/**"}

	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrReservationRequired) {
		t.Fatalf("Execute error=%v, want ErrReservationRequired", err)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("claim=%d dispatch=%d, want zero side effects", claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRequiredReservationRejectsUndefinedScopeBeforeClaim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(NewStore("atomic-missing-scope"), claimer, &atomicReservationRecorder{}, dispatcher)
	req := atomicTestRequest("missing-scope-key", "AgentA")
	req.RequireReservation = true

	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrReservationPathsRequired) {
		t.Fatalf("Execute error=%v, want ErrReservationPathsRequired", err)
	}
	if claimer.calls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("claim=%d dispatch=%d, want zero side effects", claimer.calls, dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRequiredReservationRejectsPartialGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		return LeaseReceipt{AgentName: req.AgentName, Target: req.Target, Requested: append([]string(nil), req.RequestedPaths...), Granted: []string{"internal/a/**"}}, nil
	})
	coordinator := NewAtomicCoordinator(NewStore("atomic-partial-grant"), claimer, reserver, dispatcher)
	req := atomicTestRequest("partial-key", "AgentA")
	req.RequireReservation = true
	req.RequestedPaths = []string{"internal/a/**", "internal/b/**"}

	result, err := coordinator.Execute(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "internal/b/**") {
		t.Fatalf("Execute error=%v, want missing grant", err)
	}
	if result.Assignment == nil || result.Assignment.ReservationCompleted || result.Assignment.ReservationError == "" {
		t.Fatalf("result assignment=%+v", result.Assignment)
	}
	if result.Assignment.ReservationState != ReservationReserved || !errors.Is(err, ErrReservationReleaseRequired) {
		t.Fatalf("partial grant state=%s err=%v, want reserved/release-required", result.Assignment.ReservationState, err)
	}
	if dispatcher.calls.Load() != 0 {
		t.Fatalf("dispatch calls=%d, want zero", dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentKnownZeroGrantFailureIsRetryable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-known-zero-grant")
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	var reserveCalls atomic.Int32
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		if reserveCalls.Add(1) == 1 {
			return LeaseReceipt{AgentName: req.AgentName, Target: req.Target}, GuaranteeNoReservation(errors.New("reservation conflict before actuation"))
		}
		expiresAt := time.Now().UTC().Add(time.Hour)
		return LeaseReceipt{
			AgentName: req.AgentName, Target: req.Target,
			Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...),
			ReservationIDs: []int{84}, ExpiresAt: &expiresAt,
		}, nil
	})
	request := atomicTestRequest("known-zero-grant", "%84")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/**"}
	coordinator := NewAtomicCoordinator(store, claimer, reserver, dispatcher)
	first, err := coordinator.Execute(t.Context(), request)
	if err == nil || errors.Is(err, ErrReservationOutcomeUnknown) || errors.Is(err, ErrReservationReleaseRequired) {
		t.Fatalf("first Execute=%+v err=%v, want known retryable failure", first, err)
	}
	failed := store.Get(request.BeadID)
	if failed == nil || failed.ReservationState != ReservationFailed || failed.ReservationCompleted || len(failed.ReservationIDs) != 0 || failed.ReservationError == "" {
		t.Fatalf("known zero-grant state=%+v", failed)
	}
	second, err := coordinator.Execute(t.Context(), request)
	if err != nil || !second.Sent || reserveCalls.Load() != 2 || dispatcher.calls.Load() != 1 {
		t.Fatalf("retry Execute=%+v err=%v reserve=%d dispatch=%d", second, err, reserveCalls.Load(), dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentPartialGrantErrorRequiresReleaseAndPreservesHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-partial-grant-error")
	var reserveCalls atomic.Int32
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		reserveCalls.Add(1)
		return LeaseReceipt{
			AgentName: req.AgentName, Target: req.Target,
			Requested: append([]string(nil), req.RequestedPaths...), Granted: []string{"internal/a/**"},
			ReservationIDs: []int{851},
		}, errors.New("second path conflicted")
	})
	request := atomicTestRequest("partial-grant-error", "%85")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/a/**", "internal/b/**"}
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, reserver, &atomicDispatchRecorder{})
	first, err := coordinator.Execute(t.Context(), request)
	if !errors.Is(err, ErrReservationReleaseRequired) || first.Sent {
		t.Fatalf("first Execute=%+v err=%v, want release required", first, err)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.ReservationState != ReservationReserved || stored.ReservationCompleted || len(stored.ReservationIDs) != 1 || len(stored.ReservedPaths) != 1 {
		t.Fatalf("partial grant handles not preserved: %+v", stored)
	}
	second, retryErr := coordinator.Execute(t.Context(), request)
	if !errors.Is(retryErr, ErrReservationReleaseRequired) || second.Sent || reserveCalls.Load() != 1 {
		t.Fatalf("retry Execute=%+v err=%v reserve calls=%d", second, retryErr, reserveCalls.Load())
	}
}

func TestAtomicAssignmentRejectsMismatchedReservationReceipts(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*LeaseReceipt)
	}{
		{name: "agent", alter: func(lease *LeaseReceipt) { lease.AgentName = "DifferentAgent" }},
		{name: "target", alter: func(lease *LeaseReceipt) { lease.Target = "%999" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			claimer := &atomicClaimLedger{}
			dispatcher := &atomicDispatchRecorder{}
			reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
				expiresAt := time.Now().UTC().Add(time.Hour)
				lease := LeaseReceipt{
					AgentName: req.AgentName, Target: req.Target,
					Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...),
					ReservationIDs: []int{42}, ExpiresAt: &expiresAt,
				}
				test.alter(&lease)
				return lease, nil
			})
			store := NewStore("atomic-invalid-reservation-" + test.name)
			req := atomicTestRequest("invalid-reservation-"+test.name, "AgentA")
			req.RequireReservation = true
			req.RequestedPaths = []string{"internal/assignment/**"}
			_, err := NewAtomicCoordinator(store, claimer, reserver, dispatcher).Execute(t.Context(), req)
			if err == nil || !strings.Contains(err.Error(), "mismatch") {
				t.Fatalf("Execute error=%v, want receipt mismatch", err)
			}
			if dispatcher.calls.Load() != 0 {
				t.Fatalf("invalid reservation receipt dispatched %d times", dispatcher.calls.Load())
			}
			if stored := store.Get(req.BeadID); stored == nil || stored.ReservationCompleted || stored.ReservationError == "" {
				t.Fatalf("invalid reservation state = %+v", stored)
			}
		})
	}
}

func TestAtomicAssignmentRejectsMalformedRequiredLeaseReceipts(t *testing.T) {
	tests := []struct {
		name      string
		alter     func(*LeaseReceipt)
		wantError string
	}{
		{name: "requested mismatch", alter: func(lease *LeaseReceipt) { lease.Requested = []string{"internal/other/**"} }, wantError: "requested paths"},
		{name: "duplicate requested path", alter: func(lease *LeaseReceipt) { lease.Requested = append(lease.Requested, lease.Requested[0]) }, wantError: "duplicated"},
		{name: "unexpected granted path", alter: func(lease *LeaseReceipt) { lease.Granted = []string{"internal/other/**"} }, wantError: "not granted"},
		{name: "duplicate granted path", alter: func(lease *LeaseReceipt) { lease.Granted = append(lease.Granted, lease.Granted[0]) }, wantError: "duplicated"},
		{name: "missing reservation id", alter: func(lease *LeaseReceipt) { lease.ReservationIDs = nil }, wantError: "no positive durable reservation IDs"},
		{name: "invalid reservation id", alter: func(lease *LeaseReceipt) { lease.ReservationIDs = []int{0} }, wantError: "invalid ID"},
		{name: "duplicate reservation id", alter: func(lease *LeaseReceipt) { lease.ReservationIDs = []int{42, 42} }, wantError: "repeats ID"},
		{name: "missing expiry", alter: func(lease *LeaseReceipt) { lease.ExpiresAt = nil }, wantError: "future expiry"},
		{name: "expired lease", alter: func(lease *LeaseReceipt) { expired := time.Now().UTC().Add(-time.Minute); lease.ExpiresAt = &expired }, wantError: "future expiry"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dispatcher := &atomicDispatchRecorder{}
			reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
				expiresAt := time.Now().UTC().Add(time.Hour)
				lease := LeaseReceipt{
					AgentName: req.AgentName, Target: req.Target,
					Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...),
					ReservationIDs: []int{42}, ExpiresAt: &expiresAt,
				}
				test.alter(&lease)
				return lease, nil
			})
			store := NewStore("atomic-malformed-required-lease-" + strings.ReplaceAll(test.name, " ", "-"))
			request := atomicTestRequest("malformed-required-lease-"+test.name, "%89")
			request.RequireReservation = true
			request.RequestedPaths = []string{"internal/assignment/**"}
			result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, reserver, dispatcher).Execute(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.wantError) || !errors.Is(err, ErrReservationReleaseRequired) || result.Sent {
				t.Fatalf("malformed lease result=%+v error=%v, want release-required containing %q", result, err, test.wantError)
			}
			stored := store.Get(request.BeadID)
			if stored == nil || stored.ReservationCompleted || stored.ReservationError == "" || dispatcher.calls.Load() != 0 {
				t.Fatalf("malformed lease crossed dispatch or lost error: stored=%+v dispatches=%d", stored, dispatcher.calls.Load())
			}
		})
	}
}

func TestAtomicAssignmentRequiredLeaseAllowsMultipleUniqueIDsForOnePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	expiresAt := time.Now().UTC().Add(time.Hour)
	reserver := ReservationFunc(func(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
		return LeaseReceipt{
			AgentName: req.AgentName, Target: req.Target,
			Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...),
			ReservationIDs: []int{91, 92}, ExpiresAt: &expiresAt,
		}, nil
	})
	request := atomicTestRequest("multiple-ids-one-path", "%90")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/**"}
	result, err := NewAtomicCoordinator(NewStore("atomic-multiple-ids-one-path"), &atomicClaimLedger{}, reserver, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if err != nil || !result.Sent || !reflect.DeepEqual(result.Lease.ReservationIDs, []int{91, 92}) {
		t.Fatalf("multiple ID lease result=%+v error=%v", result, err)
	}
}

func TestAtomicAssignmentRevalidatesPersistedRequiredLeaseBeforeDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-persisted-lease-validation")
	dispatcher := &atomicDispatchRecorder{}
	dispatcher.failFirst.Store(true)
	request := atomicTestRequest("persisted-required-lease", "%93")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/**"}
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, &atomicReservationRecorder{}, dispatcher)
	if _, err := coordinator.Execute(t.Context(), request); err == nil || !IsGuaranteedNoActuation(err) {
		t.Fatalf("seed dispatch error=%v, want guaranteed no actuation", err)
	}
	store.mutex.Lock()
	store.Assignments[request.BeadID].ReservationExpiresAt = nil
	store.mutex.Unlock()
	if err := store.Save(); err != nil {
		t.Fatalf("persist malformed lease: %v", err)
	}
	dispatcher.failFirst.Store(false)
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher).Execute(t.Context(), request)
	if !errors.Is(err, ErrReservationReleaseRequired) || result.Sent {
		t.Fatalf("persisted malformed lease result=%+v error=%v", result, err)
	}
	if dispatcher.calls.Load() != 1 {
		t.Fatalf("persisted malformed lease redispatched: %d", dispatcher.calls.Load())
	}
}

func TestAtomicAssignmentRecoveryCanReusePersistedRequiredLeaseWithoutPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	dispatcher.failFirst.Store(true)
	store := NewStore("atomic-required-recovery")
	req := atomicTestRequest("required-recovery-key", "AgentA")
	req.RequireReservation = true
	req.RequestedPaths = []string{"internal/assignment/**"}

	withReservation := NewAtomicCoordinator(store, claimer, reserver, dispatcher)
	if _, err := withReservation.Execute(t.Context(), req); err == nil {
		t.Fatal("first Execute unexpectedly succeeded")
	}
	dispatcher.failFirst.Store(false)
	withoutReservation := NewAtomicCoordinator(store, claimer, nil, dispatcher)
	result, err := withoutReservation.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("recovery Execute: %v", err)
	}
	if !result.Sent || !result.Recovered {
		t.Fatalf("recovery result=%+v", result)
	}
}

func TestAtomicAssignmentLocalOwnershipConflictDoesNotClaimAgain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	store := NewStore("atomic-local-conflict")
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)
	if _, err := coordinator.Execute(t.Context(), atomicTestRequest("first-key", "AgentA")); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := coordinator.Execute(t.Context(), atomicTestRequest("second-key", "AgentB")); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("second Execute error=%v, want claim conflict", err)
	}
	if claimer.calls != 1 {
		t.Fatalf("claim calls=%d, want local conflict before a second external claim", claimer.calls)
	}
}

func TestAtomicAssignmentClaimedButUnsentRecoversWithoutStealing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	reserver := &atomicReservationRecorder{}
	dispatcher := &atomicDispatchRecorder{}
	dispatcher.failFirst.Store(true)
	store := NewStore("atomic-recover")
	coordinator := NewAtomicCoordinator(store, claimer, reserver, dispatcher)
	owned := atomicTestRequest("recover-key", "AgentA")

	if _, err := coordinator.Execute(t.Context(), owned); err == nil {
		t.Fatal("first Execute unexpectedly succeeded")
	}
	pending := store.Get(owned.BeadID)
	if pending == nil || pending.Status != StatusClaimed || pending.DispatchState != DispatchPending {
		t.Fatalf("pending assignment=%+v, want claimed/pending", pending)
	}
	if pending.LastDispatchError == "" || len(pending.ReservationIDs) != 1 {
		t.Fatalf("recovery metadata missing: %+v", pending)
	}
	if pending.PendingPrompt != owned.Prompt || pending.DispatchTarget != owned.Target {
		t.Fatalf("pending dispatch intent missing: %+v", pending)
	}

	contender := NewAtomicCoordinator(NewStore("atomic-contender"), claimer, nil, dispatcher)
	if _, err := contender.Execute(t.Context(), atomicTestRequest("other-key", "AgentB")); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("contender error=%v, want claim conflict", err)
	}

	dispatcher.failFirst.Store(false)
	recovered, err := coordinator.Execute(t.Context(), owned)
	if err != nil {
		t.Fatalf("recovery Execute: %v", err)
	}
	if !recovered.Sent || !recovered.Recovered || recovered.Replayed {
		t.Fatalf("recovered result=%+v", recovered)
	}
	if reserver.calls.Load() != 1 {
		t.Fatalf("reservation calls=%d, want persisted lease reuse", reserver.calls.Load())
	}
	if dispatcher.calls.Load() != 2 {
		t.Fatalf("dispatch calls=%d, want failed send plus recovery", dispatcher.calls.Load())
	}

	reloaded, err := LoadStore("atomic-recover")
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	final := reloaded.Get(owned.BeadID)
	if final == nil || final.DispatchState != DispatchSent || final.DispatchReceiptID != "delivery-1" {
		t.Fatalf("final assignment=%+v, want durable sent receipt", final)
	}
	if final.ClaimActor != StableClaimActor(owned.Actor, owned.IdempotencyKey) {
		t.Fatalf("claim actor=%q", final.ClaimActor)
	}
}

func TestAtomicAssignmentSendingStateRequiresReconciliation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	store := NewStore("atomic-unknown")
	req := atomicTestRequest("unknown-key", "AgentA")
	claim, err := claimer.Claim(t.Context(), req.BeadID, StableClaimActor(req.Actor, req.IdempotencyKey))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.RecordAtomicIntent(req, claim.Actor, time.Now().UTC()); err != nil {
		t.Fatalf("record intent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(req, claim); err != nil {
		t.Fatalf("record claim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(req.BeadID, req.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("record send intent: %v", err)
	}

	coordinator := NewAtomicCoordinator(store, claimer, nil, &atomicDispatchRecorder{})
	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("Execute error=%v, want outcome unknown", err)
	}
}

func TestAtomicAssignmentAmbiguousDispatchErrorStaysSendingAndCannotRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claimer := &atomicClaimLedger{}
	var calls atomic.Int32
	dispatcher := DispatchFunc(func(context.Context, DispatchRequest) (DispatchReceipt, error) {
		calls.Add(1)
		return DispatchReceipt{}, errors.New("connection lost after write")
	})
	store := NewStore("atomic-ambiguous-error")
	coordinator := NewAtomicCoordinator(store, claimer, nil, dispatcher)
	req := atomicTestRequest("ambiguous-key", "AgentA")

	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("first Execute error=%v, want ErrDispatchOutcomeUnknown", err)
	}
	if got := store.Get(req.BeadID); got == nil || got.DispatchState != DispatchSending {
		t.Fatalf("stored assignment=%+v, want sending", got)
	}
	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("retry Execute error=%v, want ErrDispatchOutcomeUnknown", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("dispatch calls=%d, want 1", calls.Load())
	}
}

func TestAtomicAssignmentEmptyDispatchReceiptStaysSendingAndCannotRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calls atomic.Int32
	dispatcher := DispatchFunc(func(context.Context, DispatchRequest) (DispatchReceipt, error) {
		calls.Add(1)
		return DispatchReceipt{Duration: time.Millisecond}, nil
	})
	store := NewStore("atomic-empty-dispatch-receipt")
	request := atomicTestRequest("empty-dispatch-receipt", "%96")
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher)
	first, err := coordinator.Execute(t.Context(), request)
	if !errors.Is(err, ErrDispatchOutcomeUnknown) || first.Sent {
		t.Fatalf("first empty receipt result=%+v error=%v", first, err)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.DispatchState != DispatchSending || stored.DispatchReceiptID != "" || stored.DispatchAttempts != 1 {
		t.Fatalf("empty receipt durable state=%+v", stored)
	}
	second, err := coordinator.Execute(t.Context(), request)
	if !errors.Is(err, ErrDispatchOutcomeUnknown) || second.Sent || calls.Load() != 1 {
		t.Fatalf("empty receipt retry result=%+v error=%v calls=%d", second, err, calls.Load())
	}
}

func TestAtomicAssignmentRefusesMalformedPersistedDispatchReplay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-malformed-dispatch-replay")
	request := atomicTestRequest("malformed-dispatch-replay", "%97")
	dispatcher := &atomicDispatchRecorder{}
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher)
	if result, err := coordinator.Execute(t.Context(), request); err != nil || !result.Sent {
		t.Fatalf("seed dispatch result=%+v error=%v", result, err)
	}
	store.mutex.Lock()
	store.Assignments[request.BeadID].DispatchReceiptID = ""
	store.mutex.Unlock()
	if err := store.Save(); err != nil {
		t.Fatalf("persist malformed dispatch receipt: %v", err)
	}
	result, err := coordinator.Execute(t.Context(), request)
	if !errors.Is(err, ErrDispatchOutcomeUnknown) || result.Sent || result.Replayed || dispatcher.calls.Load() != 1 {
		t.Fatalf("malformed replay result=%+v error=%v dispatches=%d", result, err, dispatcher.calls.Load())
	}
}

func TestRecordAtomicDispatchSentRejectsEmptyReceiptWithoutMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-record-empty-dispatch-receipt")
	request := atomicTestRequest("record-empty-dispatch-receipt", "%98")
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	if err := store.RecordAtomicDispatchSent(request.BeadID, request.IdempotencyKey, request.Prompt, DispatchReceipt{}, time.Now().UTC()); err == nil {
		t.Fatal("RecordAtomicDispatchSent accepted an empty receipt")
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.DispatchState != DispatchSending || stored.Status != StatusClaiming || stored.DispatchReceiptID != "" {
		t.Fatalf("empty receipt mutated dispatch state: %+v", stored)
	}
}

type reconcilingReservationRecorder struct {
	mu             sync.Mutex
	lease          *LeaseReceipt
	reserveCalls   int
	reconcileCalls int
}

func (r *reconcilingReservationRecorder) Reserve(_ context.Context, req ReservationRequest) (LeaseReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserveCalls++
	expiresAt := time.Date(2030, 7, 11, 13, 0, 0, 0, time.UTC)
	lease := LeaseReceipt{
		AgentName: req.AgentName, Target: req.Target, Requested: append([]string(nil), req.RequestedPaths...),
		Granted: append([]string(nil), req.RequestedPaths...), ReservationIDs: []int{91}, ExpiresAt: &expiresAt,
	}
	r.lease = &lease
	return lease, nil
}

func (r *reconcilingReservationRecorder) ReconcileReservation(_ context.Context, _ ReservationRequest, _ LeaseReceipt) (ReservationReconciliation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconcileCalls++
	if r.lease == nil {
		return ReservationReconciliation{State: ReservationReconciliationAbsent}, nil
	}
	return ReservationReconciliation{State: ReservationReconciliationReserved, Lease: *r.lease}, nil
}

func TestAtomicAssignmentRecoversClaimBarrierBeforeAndAfterActuation(t *testing.T) {
	tests := []struct {
		name           string
		seedExternal   bool
		wantClaimCalls int
	}{
		{name: "crash before external claim", wantClaimCalls: 1},
		{name: "crash after external claim", seedExternal: true, wantClaimCalls: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			req := atomicTestRequest("claim-recovery-key", "%201")
			actor := StableClaimActor(req.Actor, req.IdempotencyKey)
			store := NewStore("atomic-claim-barrier-" + strings.ReplaceAll(test.name, " ", "-"))
			if _, err := store.RecordAtomicIntent(req, actor, time.Now().UTC()); err != nil {
				t.Fatalf("record pre-claim intent: %v", err)
			}
			if _, err := store.RecordAtomicClaimStarted(req.BeadID, req.IdempotencyKey, time.Now().UTC()); err != nil {
				t.Fatalf("record claim barrier: %v", err)
			}

			claimer := &atomicClaimLedger{}
			if test.seedExternal {
				claimer.owner = map[string]string{req.BeadID: actor}
			}
			dispatcher := &atomicDispatchRecorder{}
			result, err := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), req)
			if err != nil || !result.Sent || !result.Recovered {
				t.Fatalf("recovered Execute = %+v, %v", result, err)
			}
			if claimer.calls != test.wantClaimCalls || dispatcher.calls.Load() != 1 {
				t.Fatalf("claim calls=%d dispatch calls=%d, want %d/1", claimer.calls, dispatcher.calls.Load(), test.wantClaimCalls)
			}
			stored := store.Get(req.BeadID)
			if stored == nil || stored.ClaimState != ClaimClaimed || stored.Status != StatusAssigned || stored.ClaimedAt == nil {
				t.Fatalf("recovered claim ledger=%+v", stored)
			}
			wantAttempts := 1
			if !test.seedExternal {
				wantAttempts = 2
			}
			if stored.ClaimAttempts != wantAttempts {
				t.Fatalf("claim attempts=%d, want %d", stored.ClaimAttempts, wantAttempts)
			}
		})
	}
}

func TestAtomicAssignmentRecoversReservationBarrierBeforeAndAfterActuation(t *testing.T) {
	tests := []struct {
		name             string
		seedExternal     bool
		wantReserveCalls int
	}{
		{name: "crash before external reservation", wantReserveCalls: 1},
		{name: "crash after external reservation", seedExternal: true, wantReserveCalls: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			req := atomicTestRequest("reservation-recovery-key", "%202")
			req.RequireReservation = true
			req.RequestedPaths = []string{"internal/assignment/**"}
			actor := StableClaimActor(req.Actor, req.IdempotencyKey)
			store := NewStore("atomic-reservation-barrier-" + strings.ReplaceAll(test.name, " ", "-"))
			if _, err := store.RecordAtomicIntent(req, actor, time.Now().UTC()); err != nil {
				t.Fatalf("record intent: %v", err)
			}
			if _, err := store.RecordAtomicClaim(req, normalizeClaimReceipt(ClaimReceipt{Status: "in_progress"}, req.BeadID, actor, time.Now().UTC())); err != nil {
				t.Fatalf("record claim: %v", err)
			}
			if err := store.RecordAtomicReservationStarted(req.BeadID, req.IdempotencyKey, time.Now().UTC()); err != nil {
				t.Fatalf("record reservation barrier: %v", err)
			}

			reserver := &reconcilingReservationRecorder{}
			if test.seedExternal {
				expiresAt := time.Date(2030, 7, 11, 13, 0, 0, 0, time.UTC)
				reserver.lease = &LeaseReceipt{
					AgentName: req.AgentName, Target: req.OccupancyKey, Requested: append([]string(nil), req.RequestedPaths...),
					Granted: append([]string(nil), req.RequestedPaths...), ReservationIDs: []int{91}, ExpiresAt: &expiresAt,
				}
			}
			dispatcher := &atomicDispatchRecorder{}
			result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, reserver, dispatcher).Execute(t.Context(), req)
			if err != nil || !result.Sent || !result.Recovered {
				t.Fatalf("recovered Execute = %+v, %v", result, err)
			}
			if reserver.reconcileCalls != 1 || reserver.reserveCalls != test.wantReserveCalls || dispatcher.calls.Load() != 1 {
				t.Fatalf("reconcile=%d reserve=%d dispatch=%d, want 1/%d/1", reserver.reconcileCalls, reserver.reserveCalls, dispatcher.calls.Load(), test.wantReserveCalls)
			}
			stored := store.Get(req.BeadID)
			if stored == nil || stored.ReservationState != ReservationReserved || !stored.ReservationCompleted || len(stored.ReservationIDs) != 1 {
				t.Fatalf("recovered reservation ledger=%+v", stored)
			}
		})
	}
}

func TestAtomicAssignmentRecoveryUsesPersistedOriginalIntentChecksum(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := atomicTestRequest("recovered-intent-key", "%212")
	request.Prompt = "[REDACTED] durable coordinator prompt"
	request.IntentSHA256 = PromptSHA256("original prompt containing a secret")
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	store := NewStore("atomic-recovered-intent")
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, ClaimReceipt{
		BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}

	var dispatchCalls atomic.Int32
	dispatchedPrompt := ""
	dispatcher := DispatchFunc(func(_ context.Context, req DispatchRequest) (DispatchReceipt, error) {
		dispatchCalls.Add(1)
		dispatchedPrompt = req.Prompt
		return DispatchReceipt{DeliveryID: "delivery-1", Duration: 5 * time.Millisecond}, nil
	})
	preflightPrompt := ""
	preflight := PromptPreflightFunc(func(_ context.Context, req DispatchRequest) (PromptPreflightResult, error) {
		preflightPrompt = req.Prompt
		return PromptPreflightResult{
			DispatchPrompt: "dispatch under changed policy: " + req.Prompt,
			DurablePrompt:  "newly redacted durable prompt: " + req.Prompt,
			DurableTitle:   "newly redacted title",
		}, nil
	})
	recovery := request
	recovery.IntentSHA256 = ""
	recovery.RecoveredIntentSHA256 = request.IntentSHA256
	recovery.Prompt = "attacker supplied replacement prompt"
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher, preflight).Execute(t.Context(), recovery)
	if err != nil || !result.Sent || !result.Recovered || dispatchCalls.Load() != 1 {
		t.Fatalf("recovered Execute result=%+v error=%v dispatches=%d", result, err, dispatchCalls.Load())
	}
	if preflightPrompt != request.Prompt || !strings.Contains(dispatchedPrompt, request.Prompt) || strings.Contains(dispatchedPrompt, recovery.Prompt) {
		t.Fatalf("recovery preflight=%q dispatched=%q, want only persisted prompt %q", preflightPrompt, dispatchedPrompt, request.Prompt)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.IntentSHA256 != request.IntentSHA256 || stored.DispatchState != DispatchSent ||
		stored.PromptSent != request.Prompt || stored.BeadTitle != request.BeadTitle {
		t.Fatalf("recovered intent ledger = %+v", stored)
	}

	wrong := recovery
	wrong.RecoveredIntentSHA256 = PromptSHA256("different original prompt")
	if _, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher, preflight).Execute(t.Context(), wrong); err == nil ||
		!strings.Contains(err.Error(), "does not match the durable assignment") {
		t.Fatalf("wrong recovered checksum error = %v", err)
	}
	if dispatchCalls.Load() != 1 {
		t.Fatalf("wrong recovered checksum dispatched again: %d", dispatchCalls.Load())
	}
}

func TestAtomicAssignmentRejectsRecoveryChecksumWithoutDurableIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := atomicTestRequest("orphan-recovery-key", "%213")
	request.RecoveredIntentSHA256 = PromptSHA256(request.Prompt)
	store := NewStore("atomic-orphan-recovery")
	dispatcher := &atomicDispatchRecorder{}
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher).Execute(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "requires an existing same-key assignment") || result.Sent || dispatcher.calls.Load() != 0 {
		t.Fatalf("orphan recovery result=%+v error=%v dispatches=%d", result, err, dispatcher.calls.Load())
	}
	if got := store.Get(request.BeadID); got != nil {
		t.Fatalf("orphan recovery persisted intent: %+v", got)
	}
}

func TestAtomicAssignmentAmbiguousReservationWithoutInspectorFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	req := atomicTestRequest("reservation-unknown-key", "%203")
	req.RequireReservation = true
	req.RequestedPaths = []string{"internal/assignment/**"}
	actor := StableClaimActor(req.Actor, req.IdempotencyKey)
	store := NewStore("atomic-reservation-unknown")
	if _, err := store.RecordAtomicIntent(req, actor, time.Now().UTC()); err != nil {
		t.Fatalf("record intent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(req, normalizeClaimReceipt(ClaimReceipt{Status: "in_progress"}, req.BeadID, actor, time.Now().UTC())); err != nil {
		t.Fatalf("record claim: %v", err)
	}
	if err := store.RecordAtomicReservationStarted(req.BeadID, req.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("record reservation barrier: %v", err)
	}
	var reserveCalls atomic.Int32
	reserver := ReservationFunc(func(context.Context, ReservationRequest) (LeaseReceipt, error) {
		reserveCalls.Add(1)
		return LeaseReceipt{}, nil
	})
	dispatcher := &atomicDispatchRecorder{}
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, reserver, dispatcher).Execute(t.Context(), req)
	if !errors.Is(err, ErrReservationOutcomeUnknown) {
		t.Fatalf("Execute error=%v, want ErrReservationOutcomeUnknown", err)
	}
	if reserveCalls.Load() != 0 || dispatcher.calls.Load() != 0 || result.Sent {
		t.Fatalf("ambiguous reservation repeated side effects: reserve=%d dispatch=%d result=%+v", reserveCalls.Load(), dispatcher.calls.Load(), result)
	}
}

type unknownReservationWithHandles struct {
	lease LeaseReceipt
}

func (r unknownReservationWithHandles) Reserve(context.Context, ReservationRequest) (LeaseReceipt, error) {
	return LeaseReceipt{}, errors.New("reserve must not repeat while outcome is unknown")
}

func (r unknownReservationWithHandles) ReconcileReservation(context.Context, ReservationRequest, LeaseReceipt) (ReservationReconciliation, error) {
	return ReservationReconciliation{State: ReservationReconciliationUnknown, Lease: r.lease}, nil
}

func TestAtomicAssignmentPersistsHandlesDiscoveredDuringUnknownReconciliation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	request := atomicTestRequest("reservation-unknown-handles-key", "%214")
	request.RequireReservation = true
	request.RequestedPaths = []string{"internal/assignment/a.go", "internal/assignment/b.go"}
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	store := NewStore("atomic-reservation-unknown-handles")
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, ClaimReceipt{BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicReservationStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicReservationStarted: %v", err)
	}
	partial := LeaseReceipt{
		AgentName: request.AgentName, Target: request.Target, Requested: append([]string(nil), request.RequestedPaths...),
		Granted: []string{"internal/assignment/a.go"}, ReservationIDs: []int{2141},
	}
	result, err := NewAtomicCoordinator(store, &atomicClaimLedger{}, unknownReservationWithHandles{lease: partial}, &atomicDispatchRecorder{}).Execute(t.Context(), request)
	if !errors.Is(err, ErrReservationOutcomeUnknown) || result.Sent {
		t.Fatalf("Execute result=%+v error=%v, want unknown reservation failure", result, err)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.ReservationState != ReservationUnknown || !reflect.DeepEqual(stored.ReservationIDs, []int{2141}) ||
		!reflect.DeepEqual(stored.ReservedPaths, []string{"internal/assignment/a.go"}) {
		t.Fatalf("unknown reconciliation lost discovered handles: %+v", stored)
	}
}

func TestAtomicOperationLockWaitHonorsContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storePath := NewStore("atomic-context-lock").path
	firstUnlock, err := acquireAtomicBeadOperationLock(context.Background(), storePath, "ntm-context-lock")
	if err != nil {
		t.Fatalf("acquire first operation lock: %v", err)
	}
	defer firstUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = acquireAtomicBeadOperationLock(ctx, storePath, "ntm-context-lock")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error=%v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context cancellation took %s", elapsed)
	}
}

func TestAtomicOperationLockReapsIdentityFileButPreservesStableLocks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storePath := NewStore("atomic-lock-reap").path
	lockPath := atomicOperationLockPath(storePath, "bead", "agent-factory-uygc")
	guardPath := storePath + ".atomic.lock"

	unlock, err := acquireAtomicBeadOperationLock(context.Background(), storePath, "agent-factory-uygc")
	if err != nil {
		t.Fatalf("acquire atomic operation lock: %v", err)
	}
	lockToken, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("identity lock must exist while held: %v", err)
	}
	if len(lockToken) != 32 {
		t.Fatalf("identity lock token length = %d, want 32", len(lockToken))
	}
	if _, err := os.Stat(guardPath); err != nil {
		t.Fatalf("stable atomic guard must exist: %v", err)
	}
	unlock()

	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("identity lock survived release: %v", err)
	}
	if _, err := os.Stat(guardPath); err != nil {
		t.Fatalf("stable atomic guard was removed: %v", err)
	}

	storeUnlock, err := acquireStoreFileLock(storePath)
	if err != nil {
		t.Fatalf("acquire whole-store lock: %v", err)
	}
	storeUnlock()
	if _, err := os.Stat(storePath + ".lock"); err != nil {
		t.Fatalf("whole-store lock was removed: %v", err)
	}
}

func TestAtomicOperationLockFilesStayBoundedAcrossUniqueIdentities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storePath := NewStore("atomic-lock-bounded").path

	for i := range 200 {
		identity := fmt.Sprintf("never-repeated-%d", i)
		unlock, err := acquireAtomicBeadOperationLock(context.Background(), storePath, identity)
		if err != nil {
			t.Fatalf("acquire identity %q: %v", identity, err)
		}
		unlock()
	}

	matches, err := filepath.Glob(storePath + ".atomic-*.lock")
	if err != nil {
		t.Fatalf("glob identity locks: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("unique identities left %d lock files: %v", len(matches), matches)
	}
	if _, err := os.Stat(storePath + ".atomic.lock"); err != nil {
		t.Fatalf("stable atomic guard must remain for future operations: %v", err)
	}
}

func TestRemoveLockFileWithTokenPreservesReplacement(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "assignments.json.atomic-bead-replaced.lock")
	originalToken := []byte("original-token")
	if err := os.WriteFile(lockPath, originalToken, 0600); err != nil {
		t.Fatalf("write original lock: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove original lock: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("replacement"), 0600); err != nil {
		t.Fatalf("write replacement lock: %v", err)
	}

	removeLockFileWithToken(lockPath, originalToken)

	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("replacement lock was removed: %v", err)
	}
	if string(content) != "replacement" {
		t.Fatalf("replacement lock content = %q", content)
	}
}

func TestAtomicOperationLockReleaseCancelRaceFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storePath := NewStore("atomic-cancel-release-race").path
	lockPath := storePath + ".cancel-release-race.lock"

	// Release the held lock only after the waiter has registered locally. This
	// makes both the token and cancellation ready together, the boundary where
	// select is otherwise allowed to choose a now-invalid acquisition.
	for attempt := 0; attempt < 8; attempt++ {
		firstUnlock, err := acquireAssignmentFileLock(context.Background(), lockPath)
		if err != nil {
			t.Fatalf("attempt %d acquire first operation lock: %v", attempt, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			unlock, lockErr := acquireAssignmentFileLock(ctx, lockPath)
			if unlock != nil {
				unlock()
			}
			result <- lockErr
		}()

		deadline := time.Now().Add(time.Second)
		for {
			assignmentPathLocks.Lock()
			entry := assignmentPathLocks.entries[lockPath]
			refs := 0
			if entry != nil {
				refs = entry.refs
			}
			assignmentPathLocks.Unlock()
			if refs == 2 {
				break
			}
			if time.Now().After(deadline) {
				firstUnlock()
				t.Fatalf("attempt %d waiter did not register for the local lock", attempt)
			}
			time.Sleep(time.Millisecond)
		}

		cancel()
		firstUnlock()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("attempt %d waiter error=%v, want context canceled", attempt, err)
		}
	}
}

// TestTargetOccupiedErrorNamesStatusAndAge pins ntm#302: an occupied-target
// refusal has to say who holds the pane, in what state, and for how long —
// otherwise terminal residue is indistinguishable from live work.
func TestTargetOccupiedErrorNamesStatusAndAge(t *testing.T) {
	assignedAt := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	err := targetOccupiedError("%42", &Assignment{
		BeadID: "ntm-stale", Status: StatusCompleted, AssignedAt: assignedAt,
	}, assignedAt.Add(90*time.Minute))
	if !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("targetOccupiedError() = %v, want ErrTargetOccupied", err)
	}
	for _, want := range []string{"owned by bead ntm-stale", "status=completed", "age=1h30m0s", "assigned_at=2026-09-03T10:00:00Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("targetOccupiedError() = %q, missing %q", err, want)
		}
	}

	// A clock that runs backwards must not produce a negative age.
	skewed := targetOccupiedError("%42", &Assignment{BeadID: "b", Status: StatusWorking, AssignedAt: assignedAt}, assignedAt.Add(-time.Hour))
	if !strings.Contains(skewed.Error(), "age=0s") {
		t.Fatalf("clock skew produced %q, want age=0s", skewed)
	}

	if err := targetOccupiedError("%42", nil, assignedAt); !errors.Is(err, ErrTargetOccupied) || !strings.Contains(err.Error(), "unknown owner") {
		t.Fatalf("nil occupant error = %v", err)
	}
}
