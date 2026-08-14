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
	if err := store.CompleteSendOperation("op-1", `{"success":true}`, time.Now()); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	got, err := store.GetSendOperation("op-1")
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
	if err := store.CompleteSendOperation("op-1", `{"success":false}`, time.Now()); err != nil {
		t.Fatalf("re-complete error = %v", err)
	}
	got2, _ := store.GetSendOperation("op-1")
	if got2.OutcomeJSON != `{"success":true}` {
		t.Errorf("re-complete overwrote outcome: %q", got2.OutcomeJSON)
	}
}

func TestGetSendOperationUnknownID(t *testing.T) {
	store := testStore(t)
	got, err := store.GetSendOperation("missing")
	if err != nil {
		t.Fatalf("get unknown error = %v", err)
	}
	if got != nil {
		t.Errorf("get unknown = %+v, want nil", got)
	}
}

func TestClaimSendOperationRequiresID(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.ClaimSendOperation(&SendOperation{}); err == nil {
		t.Fatal("claim without ID succeeded; want error")
	}
}
