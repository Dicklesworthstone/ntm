package state

import (
	"testing"
	"time"
)

// Tests for the durable idempotent send-operation records (#245).

func TestClaimSendOperationLifecycle(t *testing.T) {
	store := testStore(t)

	op := &SendOperation{
		OperationID:   "op-1",
		SessionName:   "proj",
		BindingHash:   "bind-a",
		PayloadSHA256: "sha-a",
		PayloadBytes:  42,
	}

	stored, claimed, err := store.ClaimSendOperation(op)
	if err != nil {
		t.Fatalf("first claim error = %v", err)
	}
	if !claimed {
		t.Fatal("first claim not claimed; want claimed=true")
	}
	if stored.Status != SendOperationInProgress {
		t.Errorf("claimed status = %q, want in_progress", stored.Status)
	}
	if stored.BindingHash != "bind-a" || stored.PayloadSHA256 != "sha-a" || stored.PayloadBytes != 42 {
		t.Errorf("claimed record lost binding fields: %+v", stored)
	}

	// A second claim with the same ID observes the existing row (race-safe
	// duplicate detection) and does not overwrite its binding.
	dup := &SendOperation{
		OperationID:   "op-1",
		SessionName:   "proj",
		BindingHash:   "bind-DIFFERENT",
		PayloadSHA256: "sha-DIFFERENT",
		PayloadBytes:  7,
	}
	existing, claimedAgain, err := store.ClaimSendOperation(dup)
	if err != nil {
		t.Fatalf("duplicate claim error = %v", err)
	}
	if claimedAgain {
		t.Fatal("duplicate claim reported claimed=true; want false")
	}
	if existing.BindingHash != "bind-a" {
		t.Errorf("duplicate claim overwrote binding: %+v", existing)
	}

	// Completion records the outcome durably.
	if err := store.CompleteSendOperation("op-1", "proj", `{"success":true}`, time.Now()); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	got, err := store.GetSendOperation("op-1", "proj")
	if err != nil {
		t.Fatalf("get error = %v", err)
	}
	if got.Status != SendOperationCompleted || got.OutcomeJSON != `{"success":true}` {
		t.Errorf("completed record = %+v, want completed with stored outcome", got)
	}
	if got.CompletedAt == nil {
		t.Error("completed record missing completed_at")
	}

	// Completing again is a no-op (status guard), preserving the original outcome.
	if err := store.CompleteSendOperation("op-1", "proj", `{"success":false}`, time.Now()); err != nil {
		t.Fatalf("re-complete error = %v", err)
	}
	got2, _ := store.GetSendOperation("op-1", "proj")
	if got2.OutcomeJSON != `{"success":true}` {
		t.Errorf("re-complete overwrote outcome: %q", got2.OutcomeJSON)
	}
}

func TestGetSendOperationUnknownID(t *testing.T) {
	store := testStore(t)
	got, err := store.GetSendOperation("missing", "proj")
	if err != nil {
		t.Fatalf("get unknown error = %v", err)
	}
	if got != nil {
		t.Errorf("get unknown = %+v, want nil", got)
	}
}

func TestClaimSendOperationRequiresID(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.ClaimSendOperation(&SendOperation{SessionName: "proj"}); err == nil {
		t.Fatal("claim without ID succeeded; want error")
	}
	if _, _, err := store.ClaimSendOperation(&SendOperation{OperationID: "op"}); err == nil {
		t.Fatal("claim without session succeeded; want error")
	}
}

// Sessions form independent operation-ID namespaces: the same ID claimed in
// two sessions creates two rows rather than colliding.
func TestClaimSendOperationSessionScoped(t *testing.T) {
	store := testStore(t)

	_, claimedA, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "step-1", SessionName: "projA", BindingHash: "bind-a",
	})
	if err != nil || !claimedA {
		t.Fatalf("session A claim = (claimed=%v, err=%v), want fresh claim", claimedA, err)
	}
	_, claimedB, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "step-1", SessionName: "projB", BindingHash: "bind-b",
	})
	if err != nil || !claimedB {
		t.Fatalf("session B claim = (claimed=%v, err=%v), want independent fresh claim", claimedB, err)
	}

	ops, err := store.GetSendOperationsByID("step-1")
	if err != nil {
		t.Fatalf("get by ID error = %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("rows for step-1 = %d, want 2", len(ops))
	}
}

func TestReleaseSendOperation(t *testing.T) {
	store := testStore(t)
	op := &SendOperation{OperationID: "op-r", SessionName: "proj", BindingHash: "b"}
	if _, _, err := store.ClaimSendOperation(op); err != nil {
		t.Fatalf("claim error = %v", err)
	}

	// Release frees the ID for a fresh claim.
	if err := store.ReleaseSendOperation("op-r", "proj"); err != nil {
		t.Fatalf("release error = %v", err)
	}
	_, claimed, err := store.ClaimSendOperation(op)
	if err != nil || !claimed {
		t.Fatalf("re-claim after release = (claimed=%v, err=%v), want fresh claim", claimed, err)
	}

	// A completed row is never released.
	if err := store.CompleteSendOperation("op-r", "proj", `{"success":true}`, time.Now()); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	if err := store.ReleaseSendOperation("op-r", "proj"); err != nil {
		t.Fatalf("release completed error = %v", err)
	}
	got, _ := store.GetSendOperation("op-r", "proj")
	if got == nil || got.Status != SendOperationCompleted {
		t.Fatalf("completed row after release attempt = %+v, want intact", got)
	}
}

func TestTakeOverStaleSendOperation(t *testing.T) {
	store := testStore(t)
	stale := time.Now().UTC().Add(-time.Hour)
	if _, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-t", SessionName: "proj", BindingHash: "b", CreatedAt: stale,
	}); err != nil {
		t.Fatalf("claim error = %v", err)
	}

	// A fresh claim (created now) must not be usurped.
	won, err := store.TakeOverStaleSendOperation("op-t", "proj", "b", time.Now().UTC().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("takeover error = %v", err)
	}
	if won {
		t.Fatal("takeover won against a claim inside the staleness window")
	}

	// A stale claim with matching binding is taken over.
	won, err = store.TakeOverStaleSendOperation("op-t", "proj", "b", time.Now().UTC().Add(-time.Minute))
	if err != nil || !won {
		t.Fatalf("stale takeover = (won=%v, err=%v), want success", won, err)
	}

	// Mismatched binding never takes over.
	won, err = store.TakeOverStaleSendOperation("op-t", "proj", "OTHER", time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("mismatched takeover error = %v", err)
	}
	if won {
		t.Fatal("takeover won with a mismatched binding hash")
	}
}

func TestGCCompletedSendOperations(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-old", SessionName: "proj", BindingHash: "b",
	}); err != nil {
		t.Fatalf("claim error = %v", err)
	}
	if err := store.CompleteSendOperation("op-old", "proj", `{}`, time.Now().UTC().Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	// An in_progress row is never GC'd regardless of age.
	if _, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-live", SessionName: "proj", BindingHash: "b",
		CreatedAt: time.Now().UTC().Add(-60 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("claim error = %v", err)
	}

	pruned, err := store.GCCompletedSendOperations(0)
	if err != nil {
		t.Fatalf("gc error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if got, _ := store.GetSendOperation("op-old", "proj"); got != nil {
		t.Error("old completed row survived GC")
	}
	if got, _ := store.GetSendOperation("op-live", "proj"); got == nil {
		t.Error("in_progress row was GC'd")
	}
}
