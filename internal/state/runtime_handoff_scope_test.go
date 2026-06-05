package state

// ntm#135 regression coverage: prior to migration 015, runtime_handoff
// was singleton-keyed (CHECK id=1). Two distinct (session_name,
// working_dir) pairs could not coexist — the reporter's repro hit a
// "CHECK constraint failed: id = 1" on the second insert. This file
// proves that:
//
//   - inserting two distinct (session, workdir) rows succeeds;
//   - GetRuntimeHandoffByScope returns the right row for each;
//   - GetRuntimeHandoff (no args, legacy shape) returns the
//     most-recently-updated row;
//   - upserting the same (session, workdir) twice does NOT add a
//     second row (uniqueness via the new unique index).

import (
	"testing"
	"time"
)

// openMigratedStore opens an in-memory store and runs all migrations.
// Test helper local to this file so we don't depend on test layout in
// state_test.go (which uses inline t.TempDir + Open patterns).
func openMigratedStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func TestRuntimeHandoff_MultipleSessionWorkdirPairs(t *testing.T) {
	store := openMigratedStore(t)

	now := time.Now()
	fresh := now.Add(5 * time.Minute)

	if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
		SessionName: "session-a",
		WorkingDir:  "/path/to/repoA",
		Status:      "ok",
		CollectedAt: now,
		StaleAfter:  fresh,
	}); err != nil {
		t.Fatalf("upsert (session-a, repoA): %v", err)
	}
	if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
		SessionName: "session-b",
		WorkingDir:  "/path/to/repoB",
		Status:      "ok",
		CollectedAt: now,
		StaleAfter:  fresh,
	}); err != nil {
		t.Fatalf("upsert (session-b, repoB): %v", err)
	}

	a, err := store.GetRuntimeHandoffByScope("session-a", "/path/to/repoA")
	if err != nil {
		t.Fatalf("get (session-a, repoA): %v", err)
	}
	if a == nil || a.SessionName != "session-a" || a.WorkingDir != "/path/to/repoA" {
		t.Fatalf("got %+v; want session-a/repoA", a)
	}

	b, err := store.GetRuntimeHandoffByScope("session-b", "/path/to/repoB")
	if err != nil {
		t.Fatalf("get (session-b, repoB): %v", err)
	}
	if b == nil || b.SessionName != "session-b" || b.WorkingDir != "/path/to/repoB" {
		t.Fatalf("got %+v; want session-b/repoB", b)
	}
}

func TestRuntimeHandoff_UpsertSameScopeIsIdempotent(t *testing.T) {
	store := openMigratedStore(t)

	now := time.Now()
	fresh := now.Add(5 * time.Minute)

	for i, status := range []string{"v1", "v2", "v3"} {
		if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
			SessionName: "session-x",
			WorkingDir:  "/path/to/repo",
			Status:      status,
			CollectedAt: now.Add(time.Duration(i) * time.Second),
			StaleAfter:  fresh,
		}); err != nil {
			t.Fatalf("upsert iter %d: %v", i, err)
		}
	}

	row, err := store.GetRuntimeHandoffByScope("session-x", "/path/to/repo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil {
		t.Fatal("got nil; want row")
	}
	if row.Status != "v3" {
		t.Errorf("status=%q; want v3 (last upsert)", row.Status)
	}
}

func TestRuntimeHandoff_LegacyGetReturnsLatest(t *testing.T) {
	store := openMigratedStore(t)

	now := time.Now()
	fresh := now.Add(5 * time.Minute)
	older := now.Add(-1 * time.Hour)

	if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
		SessionName: "session-old",
		WorkingDir:  "/old",
		Status:      "old",
		UpdatedAt:   &older,
		CollectedAt: older,
		StaleAfter:  fresh,
	}); err != nil {
		t.Fatalf("upsert older: %v", err)
	}
	if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
		SessionName: "session-new",
		WorkingDir:  "/new",
		Status:      "newest",
		UpdatedAt:   &now,
		CollectedAt: now,
		StaleAfter:  fresh,
	}); err != nil {
		t.Fatalf("upsert newer: %v", err)
	}

	row, err := store.GetRuntimeHandoff()
	if err != nil {
		t.Fatalf("legacy get: %v", err)
	}
	if row == nil {
		t.Fatal("got nil; want most-recent row")
	}
	if row.Status != "newest" {
		t.Errorf("status=%q; want newest (ORDER BY updated_at DESC)", row.Status)
	}
}
