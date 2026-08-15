package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// newGateFixture returns an approval engine over a real temp state.db (no
// mocks, bd-2y2on) plus the store for direct inspection.
func newGateFixture(t *testing.T) (*approval.Engine, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// EnableSLB=false keeps the external slb CLI out of hermetic unit tests;
	// it only affects notification fan-out, not gating.
	return approval.New(store, nil, nil, approval.Config{DefaultExpiry: time.Hour, EnableSLB: false}), store
}

func gatePolicy(forceRelease string) *policy.Policy {
	return &policy.Policy{Automation: policy.AutomationConfig{ForceRelease: forceRelease}}
}

func TestForceReleaseOperationKeyStability(t *testing.T) {
	k1 := forceReleaseOperationKey("projA", "sess-1", 42)
	k2 := forceReleaseOperationKey("projA", "sess-1", 42)
	if k1 != k2 {
		t.Fatalf("operation key not stable: %q vs %q", k1, k2)
	}
	if !strings.HasPrefix(k1, "force_release:projA:") {
		t.Fatalf("unexpected key shape: %q", k1)
	}

	// Any change to the operation scope changes the key.
	for name, other := range map[string]string{
		"different project":     forceReleaseOperationKey("projB", "sess-1", 42),
		"different session":     forceReleaseOperationKey("projA", "sess-2", 42),
		"different reservation": forceReleaseOperationKey("projA", "sess-1", 43),
	} {
		if other == k1 {
			t.Errorf("%s should produce a different key (got %q for both)", name, k1)
		}
	}
}

func TestForceReleaseGateNever(t *testing.T) {
	engine, store := newGateFixture(t)
	dec, err := evaluateForceReleaseGate(context.Background(), gatePolicy("never"), engine,
		forceReleaseOperationKey("proj", "sess", 1), "alice", "reservation #1", "")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec.Allowed {
		t.Fatal("never policy must refuse")
	}
	if dec.ApprovalStatus != "policy_never" {
		t.Errorf("status = %q, want policy_never", dec.ApprovalStatus)
	}
	if !strings.Contains(dec.Message, "automation.force_release=never") {
		t.Errorf("message should name the policy setting: %q", dec.Message)
	}
	if !strings.Contains(dec.Message, "policy") {
		t.Errorf("message should name the policy file: %q", dec.Message)
	}

	// never must not create approval records.
	pending, err := store.ListPendingApprovals()
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("never policy created approval records: %+v", pending)
	}
}

func TestForceReleaseGateAuto(t *testing.T) {
	engine, store := newGateFixture(t)
	dec, err := evaluateForceReleaseGate(context.Background(), gatePolicy("auto"), engine,
		forceReleaseOperationKey("proj", "sess", 1), "alice", "reservation #1", "")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if !dec.Allowed {
		t.Fatal("auto policy must allow")
	}
	if dec.ApprovalStatus != "auto" {
		t.Errorf("status = %q, want auto", dec.ApprovalStatus)
	}
	pending, _ := store.ListPendingApprovals()
	if len(pending) != 0 {
		t.Fatalf("auto policy created approval records: %+v", pending)
	}
}

// TestForceReleaseGateApprovalLifecycle walks one operation key through the
// full approval x record-state table:
//
//	no record  -> blocked, fresh pending record created
//	pending    -> blocked, same record (no duplicate)
//	approved   -> allowed exactly once, record consumed at gate-pass
//	consumed   -> blocked again with a NEW pending record (one approval =
//	              one execution)
func TestForceReleaseGateApprovalLifecycle(t *testing.T) {
	engine, _ := newGateFixture(t)
	ctx := context.Background()
	pol := gatePolicy("approval")
	key := forceReleaseOperationKey("proj", "sess", 7)

	// Empty policy value defaults to approval too.
	if got := gatePolicy("").ForceReleasePolicy(); got != "approval" {
		t.Fatalf("default force-release policy = %q, want approval", got)
	}

	// 1. No record: blocked, record created.
	dec, err := evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #7", "holder crashed")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec.Allowed || !dec.Created || dec.ApprovalID == "" || dec.ApprovalStatus != "pending" {
		t.Fatalf("expected blocked+created+pending, got %+v", dec)
	}
	if !strings.Contains(dec.Message, "ntm approve "+dec.ApprovalID) {
		t.Errorf("message should tell a second operator how to approve: %q", dec.Message)
	}
	firstID := dec.ApprovalID

	rec, err := engine.Check(ctx, firstID)
	if err != nil {
		t.Fatalf("check created record: %v", err)
	}
	if !rec.RequiresSLB {
		t.Error("force-release approvals must carry RequiresSLB=true (two-person rule)")
	}
	if rec.RequestedBy != "alice" || rec.CorrelationID != key {
		t.Errorf("record identity wrong: %+v", rec)
	}

	// 2. Pending record: still blocked, re-run finds ITS OWN record.
	dec, err = evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #7", "holder crashed")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec.Allowed || dec.Created || dec.ApprovalID != firstID || dec.ApprovalStatus != "pending" {
		t.Fatalf("expected blocked on same pending record %s, got %+v", firstID, dec)
	}

	// 3. Approved by a second identity: allowed once, record consumed.
	if err := engine.Approve(ctx, firstID, "bob"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	dec, err = evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #7", "holder crashed")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if !dec.Allowed || dec.ApprovalID != firstID || dec.ApprovalStatus != string(state.ApprovalConsumed) {
		t.Fatalf("expected allowed+consumed for %s, got %+v", firstID, dec)
	}
	rec, err = engine.Check(ctx, firstID)
	if err != nil {
		t.Fatalf("check consumed record: %v", err)
	}
	if rec.Status != state.ApprovalConsumed {
		t.Fatalf("record not consumed after gate pass: %s", rec.Status)
	}

	// 4. Consumed record: one approval authorized exactly one execution —
	// the next attempt is blocked with a FRESH pending record.
	dec, err = evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #7", "holder crashed")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec.Allowed || !dec.Created || dec.ApprovalStatus != "pending" {
		t.Fatalf("expected blocked with fresh request after consumption, got %+v", dec)
	}
	if dec.ApprovalID == firstID {
		t.Fatal("consumed approval was reused; expected a new approval record")
	}
}

// TestForceReleaseGateApprovedExpired: an approval, like a denial, stands
// only for its record's validity window. A grant that sat unused past
// expires_at is inert: the gate must NOT consume it or allow execution — it
// blocks and files a fresh request (fail closed).
func TestForceReleaseGateApprovedExpired(t *testing.T) {
	engine, _ := newGateFixture(t)
	ctx := context.Background()
	pol := gatePolicy("approval")
	key := forceReleaseOperationKey("proj", "sess", 11)

	stale, err := engine.Request(ctx, approval.RequestParams{
		Action:        "force_release",
		Resource:      "reservation #11",
		RequestedBy:   "alice",
		CorrelationID: key,
		RequiresSLB:   true,
		ExpiresIn:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if err := engine.Approve(ctx, stale.ID, "bob"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	dec, err := evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #11", "")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec.Allowed {
		t.Fatal("expired approval must not authorize execution")
	}
	if !dec.Created || dec.ApprovalID == stale.ID || dec.ApprovalStatus != "pending" {
		t.Fatalf("expected fresh pending request after stale approval, got %+v", dec)
	}
	// The stale grant was not spent by the refused attempt.
	rec, err := engine.Check(ctx, stale.ID)
	if err != nil {
		t.Fatalf("check stale record: %v", err)
	}
	if rec.Status == state.ApprovalConsumed {
		t.Fatalf("stale approval was consumed: %+v", rec)
	}
}

func TestForceReleaseGateDenied(t *testing.T) {
	engine, _ := newGateFixture(t)
	ctx := context.Background()
	pol := gatePolicy("approval")
	key := forceReleaseOperationKey("proj", "sess", 9)

	dec, err := evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #9", "")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if err := engine.Deny(ctx, dec.ApprovalID, "bob", "holder still active"); err != nil {
		t.Fatalf("deny: %v", err)
	}

	// Denied within its validity window: stays blocked, reports the denial.
	dec2, err := evaluateForceReleaseGate(ctx, pol, engine, key, "alice", "reservation #9", "")
	if err != nil {
		t.Fatalf("gate error: %v", err)
	}
	if dec2.Allowed || dec2.Created || dec2.ApprovalID != dec.ApprovalID {
		t.Fatalf("expected blocked on denied record %s, got %+v", dec.ApprovalID, dec2)
	}
	if dec2.ApprovalStatus != string(state.ApprovalDenied) {
		t.Errorf("status = %q, want denied", dec2.ApprovalStatus)
	}
	if !strings.Contains(dec2.Message, "denied") || !strings.Contains(dec2.Message, "holder still active") {
		t.Errorf("denial message should carry status and reason: %q", dec2.Message)
	}
}
