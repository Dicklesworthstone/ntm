package assignment

import (
	"strings"
	"testing"
	"time"
)

func newStrandedSendingStore(t *testing.T, session string) (*AssignmentStore, AtomicRequest) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	request := AtomicRequest{
		BeadID: "ntm-stranded", BeadTitle: "Stranded", Target: "%993", OccupancyKey: "%993", Pane: 6,
		AgentType: "cod", AgentName: "BlueLake", Actor: "BlueLake", Prompt: "durable prompt",
		IdempotencyKey: "stranded-generation",
	}
	store := NewStore(session)
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, ClaimReceipt{
		BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	return store, request
}

func TestResolveStrandedDispatchIfCurrentRequiresEvidence(t *testing.T) {
	store, request := newStrandedSendingStore(t, "stranded-evidence")
	observed := store.Get(request.BeadID)

	if _, _, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), observed, "   "); err == nil ||
		!strings.Contains(err.Error(), "evidence") {
		t.Fatalf("blank evidence error = %v", err)
	}
	if _, _, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), nil, "reason"); err == nil {
		t.Fatal("nil generation must be rejected")
	}
	if stored := store.Get(request.BeadID); stored.DispatchState != DispatchSending {
		t.Fatalf("barrier changed on a rejected call: %+v", stored)
	}
}

func TestResolveStrandedDispatchIfCurrentIsExactGenerationAndIdempotent(t *testing.T) {
	store, request := newStrandedSendingStore(t, "stranded-generation")
	observed := store.Get(request.BeadID)

	stale := cloneAssignment(observed)
	stale.IdempotencyKey = "some-other-generation"
	if _, applied, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), stale, "reason"); err != nil || applied {
		t.Fatalf("stale generation applied=%v err=%v", applied, err)
	}
	if stored := store.Get(request.BeadID); stored.DispatchState != DispatchSending {
		t.Fatalf("barrier changed for a stale generation: %+v", stored)
	}

	resolved, applied, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), observed, "target %993 is gone")
	if err != nil || !applied {
		t.Fatalf("resolve applied=%v err=%v", applied, err)
	}
	if resolved.DispatchState != DispatchPending || resolved.LastDispatchError != "target %993 is gone" {
		t.Fatalf("resolved row = %+v", resolved)
	}
	if resolved.StrandedDispatchReason != "target %993 is gone" || resolved.StrandedDispatchResolvedAt == nil {
		t.Fatalf("audit record = %q at %v", resolved.StrandedDispatchReason, resolved.StrandedDispatchResolvedAt)
	}

	// A second call sees a row that is no longer on the barrier and must not
	// re-decide anything.
	if _, applied, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), observed, "another reason"); err != nil || applied {
		t.Fatalf("repeat resolve applied=%v err=%v", applied, err)
	}
	if stored := store.Get(request.BeadID); stored.StrandedDispatchReason != "target %993 is gone" {
		t.Fatalf("audit record was overwritten: %+v", stored)
	}
}

func TestAdoptStrandedDispatchReceiptIfCurrentCompletesTheDeliveryForward(t *testing.T) {
	store, request := newStrandedSendingStore(t, "stranded-adopt")
	observed := store.Get(request.BeadID)

	if _, _, err := store.AdoptStrandedDispatchReceiptIfCurrent(t.Context(), observed, DispatchReceipt{}, "reason"); err == nil {
		t.Fatal("a receipt with no delivery id must be rejected")
	}
	if stored := store.Get(request.BeadID); stored.DispatchState != DispatchSending {
		t.Fatalf("barrier changed on a rejected adopt: %+v", stored)
	}

	adopted, applied, err := store.AdoptStrandedDispatchReceiptIfCurrent(t.Context(), observed,
		DispatchReceipt{DeliveryID: "agent-mail-message-7", Duration: time.Second}, "found in BlueLake's inbox")
	if err != nil || !applied {
		t.Fatalf("adopt applied=%v err=%v", applied, err)
	}
	if adopted.DispatchState != DispatchSent || adopted.DispatchReceiptID != "agent-mail-message-7" ||
		adopted.Status != StatusAssigned || adopted.PendingPrompt != "" || adopted.PromptSent != observed.PendingPrompt {
		t.Fatalf("adopted row = %+v", adopted)
	}
	if adopted.StrandedDispatchResolvedAt == nil || adopted.StrandedDispatchReason != "found in BlueLake's inbox" {
		t.Fatalf("audit record = %q at %v", adopted.StrandedDispatchReason, adopted.StrandedDispatchResolvedAt)
	}

	// An adopted row is a normal delivered assignment: the ordinary replay path
	// must report it as already sent rather than dispatch it again.
	reloaded, err := LoadStoreStrict("stranded-adopt")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	stored := reloaded.Get(request.BeadID)
	if stored.DispatchState != DispatchSent || stored.DispatchReceiptID != "agent-mail-message-7" {
		t.Fatalf("durable adopted row = %+v", stored)
	}
}

func TestStrandedDispatchResolutionIgnoresRowsOffTheBarrier(t *testing.T) {
	store, request := newStrandedSendingStore(t, "stranded-off-barrier")
	observed := store.Get(request.BeadID)
	if _, applied, err := store.ResolveStrandedDispatchIfCurrent(t.Context(), observed, "target gone"); err != nil || !applied {
		t.Fatalf("first resolve applied=%v err=%v", applied, err)
	}

	// The row is now pending, not sending. Neither entry point may touch it.
	pending := store.Get(request.BeadID)
	if _, applied, err := store.AdoptStrandedDispatchReceiptIfCurrent(t.Context(), pending,
		DispatchReceipt{DeliveryID: "agent-mail-message-9"}, "late probe"); err != nil || applied {
		t.Fatalf("adopt on a pending row applied=%v err=%v", applied, err)
	}
	if stored := store.Get(request.BeadID); stored.DispatchState != DispatchPending || stored.DispatchReceiptID != "" {
		t.Fatalf("pending row was mutated: %+v", stored)
	}
}
