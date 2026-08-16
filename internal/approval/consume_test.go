package approval

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// hermeticConfig keeps the external slb CLI out of unit tests: these tests
// exercise the durable engine only (bd-2y2on).
func hermeticConfig() Config {
	return Config{DefaultExpiry: time.Hour, EnableSLB: false}
}

func TestConsumeApprovedRecord(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	ctx := context.Background()

	appr, err := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #1",
		RequestedBy: "alice",
		RequiresSLB: true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if err := engine.Approve(ctx, appr.ID, "bob"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	if err := engine.Consume(ctx, appr.ID, "alice"); err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	got, err := engine.Check(ctx, appr.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if got.Status != state.ApprovalConsumed {
		t.Errorf("status = %s, want consumed", got.Status)
	}
	// Approver identity survives consumption.
	if got.ApprovedBy != "bob" {
		t.Errorf("approved_by = %q, want bob", got.ApprovedBy)
	}

	// Consumption is terminal: a second Consume fails.
	if err := engine.Consume(ctx, appr.ID, "alice"); err == nil {
		t.Error("second Consume should fail; one approval authorizes one execution")
	}
}

// TestConsumeGuardedAcrossProcesses pins the cross-process TOCTOU guard:
// gated commands run as independent `ntm` processes, each with its own
// engine (whose mutex therefore serializes nothing between them) and its own
// SQLite connection to the shared state.db. The approved->consumed transition
// must be decided by the conditional UPDATE in the store, so of two
// consumers that both read status=approved, exactly one wins.
func TestConsumeGuardedAcrossProcesses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	openStore := func() *state.Store {
		store, err := state.Open(dbPath)
		if err != nil {
			t.Fatalf("open shared store: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		if err := store.Migrate(); err != nil {
			t.Fatalf("migrate shared store: %v", err)
		}
		return store
	}
	// Two engines over two separate connections = two processes in miniature.
	engineA := New(openStore(), nil, nil, hermeticConfig())
	engineB := New(openStore(), nil, nil, hermeticConfig())
	ctx := context.Background()

	appr, err := engineA.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #6",
		RequestedBy: "alice",
		RequiresSLB: true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if err := engineA.Approve(ctx, appr.ID, "bob"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	if err := engineA.Consume(ctx, appr.ID, "alice"); err != nil {
		t.Fatalf("first Consume failed: %v", err)
	}
	// The second consumer's connection still sees whatever it sees; the SQL
	// guard, not any in-process state, must reject the double spend.
	if err := engineB.Consume(ctx, appr.ID, "alice"); err == nil {
		t.Fatal("second engine consumed the same approval; one approval must authorize exactly one execution across processes")
	}

	got, err := engineB.Check(ctx, appr.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if got.Status != state.ApprovalConsumed {
		t.Fatalf("status = %s, want consumed", got.Status)
	}
}

// TestDecisionGuardedAcrossProcesses pins the pending->decided SQL guard
// (UpdateApprovalFrom): two deciders run as separate `ntm approve` processes,
// so both may read status=pending; the first decision to land must be
// terminal. A stale second write — simulated here at the store layer, exactly
// the write a racing process would issue after its pre-write read — must
// match zero rows instead of flipping a landed denial to approved.
func TestDecisionGuardedAcrossProcesses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	openStore := func() *state.Store {
		store, err := state.Open(dbPath)
		if err != nil {
			t.Fatalf("open shared store: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		if err := store.Migrate(); err != nil {
			t.Fatalf("migrate shared store: %v", err)
		}
		return store
	}
	storeA := openStore()
	storeB := openStore()
	engineA := New(storeA, nil, nil, hermeticConfig())
	ctx := context.Background()

	appr, err := engineA.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #9",
		RequestedBy: "alice",
		RequiresSLB: true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Process B reads the record while it is still pending (the racing
	// approver's pre-write read).
	stale, err := storeB.GetApproval(appr.ID)
	if err != nil || stale == nil {
		t.Fatalf("stale read failed: %v (record=%v)", err, stale)
	}

	// Process A's denial lands first.
	if err := engineA.Deny(ctx, appr.ID, "bob", "too risky"); err != nil {
		t.Fatalf("Deny failed: %v", err)
	}

	// Process B's stale approve write must lose: zero rows matched.
	now := time.Now().UTC()
	stale.Status = state.ApprovalApproved
	stale.ApprovedBy = "mallory"
	stale.ApprovedAt = &now
	ok, err := storeB.UpdateApprovalFrom(stale, state.ApprovalPending)
	if err != nil {
		t.Fatalf("guarded update errored: %v", err)
	}
	if ok {
		t.Fatal("stale approve overwrote a landed denial; pending->decided must be guarded in SQL")
	}

	got, err := engineA.Check(ctx, appr.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if got.Status != state.ApprovalDenied || got.ApprovedBy != "bob" || got.DeniedReason != "too risky" {
		t.Fatalf("denial not terminal: %+v", got)
	}
}

// TestConsumeRejectsExpiredApproved: an approved record past its expires_at
// no longer authorizes anything — a grant is only good for the record's
// validity window.
func TestConsumeRejectsExpiredApproved(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	ctx := context.Background()

	appr, err := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #7",
		RequestedBy: "alice",
		RequiresSLB: true,
		ExpiresIn:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if err := engine.Approve(ctx, appr.ID, "bob"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	if err := engine.Consume(ctx, appr.ID, "alice"); err == nil {
		t.Fatal("Consume of an expired approved record should fail")
	}
	got, err := engine.Check(ctx, appr.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if got.Status == state.ApprovalConsumed {
		t.Fatal("expired approved record must not transition to consumed")
	}
}

func TestConsumeRejectsNonApprovedStatuses(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	ctx := context.Background()

	// Pending record cannot be consumed.
	pending, err := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #2",
		RequestedBy: "alice",
		RequiresSLB: true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if err := engine.Consume(ctx, pending.ID, "alice"); err == nil {
		t.Error("Consume of pending record should fail")
	}

	// Denied record cannot be consumed.
	denied, err := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "reservation #3",
		RequestedBy: "alice",
		RequiresSLB: true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if err := engine.Deny(ctx, denied.ID, "bob", "nope"); err != nil {
		t.Fatalf("Deny failed: %v", err)
	}
	if err := engine.Consume(ctx, denied.ID, "alice"); err == nil {
		t.Error("Consume of denied record should fail")
	}

	// Missing record.
	if err := engine.Consume(ctx, "appr-does-not-exist", "alice"); err == nil {
		t.Error("Consume of missing record should fail")
	}
}

func TestLatestForCorrelation(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	ctx := context.Background()

	const key = "force_release:proj:abcdef0123456789"

	// Nothing yet.
	got, err := engine.LatestForCorrelation(ctx, key)
	if err != nil {
		t.Fatalf("LatestForCorrelation failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for unknown key, got %+v", got)
	}

	first, err := engine.Request(ctx, RequestParams{
		Action:        "force_release",
		Resource:      "reservation #4",
		RequestedBy:   "alice",
		CorrelationID: key,
		RequiresSLB:   true,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	got, err = engine.LatestForCorrelation(ctx, key)
	if err != nil {
		t.Fatalf("LatestForCorrelation failed: %v", err)
	}
	if got == nil || got.ID != first.ID {
		t.Fatalf("expected record %s, got %+v", first.ID, got)
	}

	// A second record for the same key wins as latest.
	if err := engine.Deny(ctx, first.ID, "bob", "stale"); err != nil {
		t.Fatalf("Deny failed: %v", err)
	}
	// created_at has second granularity in the ID timestamp but the DB stores
	// full precision; still, guarantee ordering across rows.
	time.Sleep(1100 * time.Millisecond)
	second, err := engine.Request(ctx, RequestParams{
		Action:        "force_release",
		Resource:      "reservation #4",
		RequestedBy:   "alice",
		CorrelationID: key,
		RequiresSLB:   true,
	})
	if err != nil {
		t.Fatalf("second Request failed: %v", err)
	}
	got, err = engine.LatestForCorrelation(ctx, key)
	if err != nil {
		t.Fatalf("LatestForCorrelation failed: %v", err)
	}
	if got == nil || got.ID != second.ID {
		t.Fatalf("expected latest record %s, got %+v", second.ID, got)
	}

	// A different key does not match.
	got, err = engine.LatestForCorrelation(ctx, key+"-other")
	if err != nil {
		t.Fatalf("LatestForCorrelation failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for different key, got %+v", got)
	}
}

func TestLatestForCorrelationExpiresStalePending(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	ctx := context.Background()

	const key = "force_release:proj:feedfeedfeedfeed"
	appr, err := engine.Request(ctx, RequestParams{
		Action:        "force_release",
		Resource:      "reservation #5",
		RequestedBy:   "alice",
		CorrelationID: key,
		RequiresSLB:   true,
		ExpiresIn:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	got, err := engine.LatestForCorrelation(ctx, key)
	if err != nil {
		t.Fatalf("LatestForCorrelation failed: %v", err)
	}
	if got == nil || got.ID != appr.ID {
		t.Fatalf("expected record %s, got %+v", appr.ID, got)
	}
	if got.Status != state.ApprovalExpired {
		t.Errorf("stale pending record should be lazily expired, got %s", got.Status)
	}
}
