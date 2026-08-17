package state

// guard_events_test.go — round-trip tests for the guard degraded-event ledger
// (bd-ws1-truth-safety-l5ddi.1).

import (
	"path/filepath"
	"testing"
	"time"
)

func openGuardTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestGuardDegradedEvents_RoundTrip(t *testing.T) {
	store := openGuardTestStore(t)

	stats, err := store.GuardDegradedEventStats(time.Time{})
	if err != nil {
		t.Fatalf("stats on empty ledger: %v", err)
	}
	if stats.Count != 0 {
		t.Fatalf("empty ledger count = %d, want 0", stats.Count)
	}

	base := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ev := &GuardDegradedEvent{
			RepoPath:   "/repo",
			ProjectKey: "/repo",
			Reason:     "agent-mail-unreachable",
			Detail:     "dial tcp: connection refused",
			CreatedAt:  base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.RecordGuardDegradedEvent(ev); err != nil {
			t.Fatalf("record event %d: %v", i, err)
		}
		if ev.ID == 0 {
			t.Errorf("event %d did not get an ID", i)
		}
	}

	stats, err = store.GuardDegradedEventStats(time.Time{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Count != 3 {
		t.Errorf("count = %d, want 3", stats.Count)
	}
	if !stats.FirstAt.Equal(base) {
		t.Errorf("FirstAt = %v, want %v", stats.FirstAt, base)
	}
	if !stats.LastAt.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("LastAt = %v, want %v", stats.LastAt, base.Add(2*time.Minute))
	}

	// since filter
	stats, err = store.GuardDegradedEventStats(base.Add(90 * time.Second))
	if err != nil {
		t.Fatalf("stats since: %v", err)
	}
	if stats.Count != 1 {
		t.Errorf("since-filtered count = %d, want 1", stats.Count)
	}

	events, err := store.ListGuardDegradedEvents(2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("list len = %d, want 2", len(events))
	}
	if !events[0].CreatedAt.After(events[1].CreatedAt) {
		t.Errorf("list not newest-first: %v then %v", events[0].CreatedAt, events[1].CreatedAt)
	}
	if events[0].Reason != "agent-mail-unreachable" || events[0].RepoPath != "/repo" {
		t.Errorf("row fields not preserved: %+v", events[0])
	}
}

func TestRecordGuardDegradedEvent_RequiresReason(t *testing.T) {
	store := openGuardTestStore(t)
	if err := store.RecordGuardDegradedEvent(&GuardDegradedEvent{RepoPath: "/repo"}); err == nil {
		t.Fatal("expected error for missing reason")
	}
	if err := store.RecordGuardDegradedEvent(nil); err == nil {
		t.Fatal("expected error for nil event")
	}
}
