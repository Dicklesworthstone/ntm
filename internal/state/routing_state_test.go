package state

import (
	"path/filepath"
	"testing"
	"time"
)

func routingStateStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestRoutingState_RoundTripAndUpsert(t *testing.T) {
	store := routingStateStore(t)

	// No history yet: nil, no error.
	rs, err := store.GetRoutingState("proj", "")
	if err != nil || rs != nil {
		t.Fatalf("GetRoutingState(empty) = %+v, %v; want nil, nil", rs, err)
	}

	if err := store.SaveRoutingState(&RoutingState{SessionName: "proj", LastAgent: "%2", RotationCursor: 1}); err != nil {
		t.Fatalf("save: %v", err)
	}
	rs, err = store.GetRoutingState("proj", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rs == nil || rs.LastAgent != "%2" || rs.RotationCursor != 1 || rs.UpdatedAt.IsZero() {
		t.Fatalf("round-trip mismatch: %+v", rs)
	}

	// Upsert advances in place.
	if err := store.SaveRoutingState(&RoutingState{SessionName: "proj", LastAgent: "%3", RotationCursor: 2}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rs, err = store.GetRoutingState("proj", "")
	if err != nil || rs == nil || rs.LastAgent != "%3" || rs.RotationCursor != 2 {
		t.Fatalf("upsert mismatch: %+v (err %v)", rs, err)
	}

	// Sessions are isolated.
	other, err := store.GetRoutingState("otherproj", "")
	if err != nil || other != nil {
		t.Fatalf("cross-session leak: %+v, %v", other, err)
	}

	if err := store.SaveRoutingState(nil); err == nil {
		t.Fatal("SaveRoutingState(nil) must error")
	}
	if err := store.SaveRoutingState(&RoutingState{}); err == nil {
		t.Fatal("SaveRoutingState without session must error")
	}
}

// TestRoutingState_FilterKeysAreIsolated pins the bd-88um4 fix: state for the
// same session under different filter sets must not share one row (a rotation
// cursor is an index into a FILTERED list).
func TestRoutingState_FilterKeysAreIsolated(t *testing.T) {
	store := routingStateStore(t)

	saves := []*RoutingState{
		{SessionName: "proj", FilterKey: "", LastAgent: "%1", RotationCursor: 0},
		{SessionName: "proj", FilterKey: "type=claude;exclude=", LastAgent: "%2", RotationCursor: 1},
		{SessionName: "proj", FilterKey: "type=codex;exclude=", LastAgent: "%3", RotationCursor: 0},
	}
	for _, rs := range saves {
		if err := store.SaveRoutingState(rs); err != nil {
			t.Fatalf("save %q: %v", rs.FilterKey, err)
		}
	}
	for _, want := range saves {
		got, err := store.GetRoutingState("proj", want.FilterKey)
		if err != nil || got == nil {
			t.Fatalf("get %q: %+v, %v", want.FilterKey, got, err)
		}
		if got.LastAgent != want.LastAgent || got.RotationCursor != want.RotationCursor {
			t.Fatalf("filter key %q: got %+v, want last=%s cursor=%d",
				want.FilterKey, got, want.LastAgent, want.RotationCursor)
		}
	}
}

// TestRoutingState_DeleteRemovesAllFilterKeys pins session-death cleanup: a
// recreated session must not inherit any filter set's stale state (bd-88um4).
func TestRoutingState_DeleteRemovesAllFilterKeys(t *testing.T) {
	store := routingStateStore(t)

	for _, fk := range []string{"", "type=claude;exclude=", "type=;exclude=1,2"} {
		if err := store.SaveRoutingState(&RoutingState{SessionName: "doomed", FilterKey: fk, LastAgent: "%1"}); err != nil {
			t.Fatalf("save %q: %v", fk, err)
		}
	}
	if err := store.SaveRoutingState(&RoutingState{SessionName: "survivor", LastAgent: "%9", RotationCursor: 3}); err != nil {
		t.Fatalf("save survivor: %v", err)
	}

	if err := store.DeleteRoutingState("doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, fk := range []string{"", "type=claude;exclude=", "type=;exclude=1,2"} {
		if rs, err := store.GetRoutingState("doomed", fk); err != nil || rs != nil {
			t.Fatalf("doomed %q still present: %+v, %v", fk, rs, err)
		}
	}
	if rs, err := store.GetRoutingState("survivor", ""); err != nil || rs == nil || rs.LastAgent != "%9" {
		t.Fatalf("survivor damaged: %+v, %v", rs, err)
	}

	if err := store.DeleteRoutingState(""); err == nil {
		t.Fatal("DeleteRoutingState(\"\") must error")
	}
}

// TestRoutingState_PurgeOlderThan pins the TTL purge (bd-88um4): stale rows
// are removed, fresh rows survive.
func TestRoutingState_PurgeOlderThan(t *testing.T) {
	store := routingStateStore(t)

	stale := &RoutingState{
		SessionName: "stale", LastAgent: "%1", RotationCursor: 0,
		UpdatedAt: time.Now().UTC().Add(-30 * 24 * time.Hour),
	}
	fresh := &RoutingState{SessionName: "fresh", LastAgent: "%2", RotationCursor: 1}
	if err := store.SaveRoutingState(stale); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if err := store.SaveRoutingState(fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	purged, err := store.PurgeRoutingStateOlderThan(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged %d rows, want 1", purged)
	}
	if rs, err := store.GetRoutingState("stale", ""); err != nil || rs != nil {
		t.Fatalf("stale row survived purge: %+v, %v", rs, err)
	}
	if rs, err := store.GetRoutingState("fresh", ""); err != nil || rs == nil {
		t.Fatalf("fresh row purged: %+v, %v", rs, err)
	}

	if _, err := store.PurgeRoutingStateOlderThan(0); err == nil {
		t.Fatal("PurgeRoutingStateOlderThan(0) must error")
	}
}

// TestRoutingState_ReadWithoutMigrateTreatsMissingSchemaAsEmpty pins the
// advisory read path (bd-88um4): a store opened WITHOUT Migrate must read
// routing state as "no history" instead of erroring on the missing table.
func TestRoutingState_ReadWithoutMigrateTreatsMissingSchemaAsEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unmigrated.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	if rs, err := store.GetRoutingState("proj", ""); err != nil || rs != nil {
		t.Fatalf("unmigrated GetRoutingState = %+v, %v; want nil, nil", rs, err)
	}
	if err := store.DeleteRoutingState("proj"); err != nil {
		t.Fatalf("unmigrated DeleteRoutingState: %v", err)
	}
	if n, err := store.PurgeRoutingStateOlderThan(time.Hour); err != nil || n != 0 {
		t.Fatalf("unmigrated purge = %d, %v; want 0, nil", n, err)
	}
}

// TestRoutingState_AdvisoryReadSucceedsUnderHeldWriteLock is the
// no-write-lock proof for the advisory route path (bd-88um4): with another
// connection holding the DB's exclusive write reservation (BEGIN IMMEDIATE),
// a second store opened WITHOUT Migrate — exactly what the advisory 'ntm
// robot route' path does — must still read routing state, and a Migrate on a
// fully-migrated DB must return without needing the write lock at all.
func TestRoutingState_AdvisoryReadSucceedsUnderHeldWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer writer.Close()
	if err := writer.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := writer.SaveRoutingState(&RoutingState{SessionName: "proj", LastAgent: "%2", RotationCursor: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Hold the exclusive write reservation on a dedicated connection.
	conn, err := writer.DB().Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	defer func() { _, _ = conn.ExecContext(t.Context(), "ROLLBACK") }()

	// The advisory path: open WITHOUT Migrate, read state. Must not block on
	// or fail against the held write lock.
	reader, err := Open(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()

	start := time.Now()
	rs, err := reader.GetRoutingState("proj", "")
	if err != nil || rs == nil || rs.LastAgent != "%2" {
		t.Fatalf("read under held write lock = %+v, %v; want last_agent %%2", rs, err)
	}
	t.Logf("advisory read under held BEGIN IMMEDIATE succeeded in %v", time.Since(start))

	// Bonus hardening: Migrate on an up-to-date schema no longer needs the
	// write lock either (the pending pre-check short-circuits).
	if err := reader.Migrate(); err != nil {
		t.Fatalf("no-op Migrate under held write lock: %v", err)
	}
}
