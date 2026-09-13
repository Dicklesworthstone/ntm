package state

import (
	"path/filepath"
	"testing"
	"time"
)

func openFileChangeTestStore(t *testing.T) *Store {
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

func TestFileChanges_RoundTrip(t *testing.T) {
	store := openFileChangeTestStore(t)
	now := time.Now().UTC()

	rows := []FileChangeRow{
		{ProjectDir: "/proj", Session: "sess", Agent: "cc-1", Path: "a.go", ChangeType: "modified", CreatedAt: now.Add(-time.Minute)},
		{ProjectDir: "/proj", Session: "sess", Agent: "cod-2", Path: "a.go", ChangeType: "modified", CreatedAt: now},
	}
	if err := store.AppendFileChanges(rows); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.FileChangesSince("/proj", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d rows, want 2", len(got))
	}
	// Oldest first, so a reader can scan forward in time.
	if got[0].Agent != "cc-1" || got[1].Agent != "cod-2" {
		t.Errorf("rows out of order: %q then %q", got[0].Agent, got[1].Agent)
	}
	if got[0].Path != "a.go" || got[0].ChangeType != "modified" || got[0].Session != "sess" {
		t.Errorf("row round-tripped wrong: %+v", got[0])
	}
}

// state.db is shared by every ntm process on the machine, so a query must never
// return another repository's rows.
func TestFileChangesAreProjectScoped(t *testing.T) {
	store := openFileChangeTestStore(t)
	now := time.Now().UTC()

	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "/proj-a", Path: "a.go", ChangeType: "modified", CreatedAt: now},
		{ProjectDir: "/proj-b", Path: "b.go", ChangeType: "modified", CreatedAt: now},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.FileChangesSince("/proj-a", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("project scoping leaked: %+v", got)
	}

	// An unknown project is empty, not everything.
	if got, err := store.FileChangesSince("", now.Add(-time.Hour)); err != nil || len(got) != 0 {
		t.Errorf("empty project dir returned %d rows (err=%v), want none", len(got), err)
	}
}

func TestFileChangesSinceIsExclusive(t *testing.T) {
	store := openFileChangeTestStore(t)
	now := time.Now().UTC()

	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "/proj", Path: "old.go", ChangeType: "modified", CreatedAt: now.Add(-2 * time.Hour)},
		{ProjectDir: "/proj", Path: "new.go", ChangeType: "modified", CreatedAt: now},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.FileChangesSince("/proj", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Path != "new.go" {
		t.Errorf("window filtering wrong: %+v", got)
	}
}

// The ledger prunes on write so it cannot grow without bound, and so the fix
// does not introduce a maintenance surface someone has to remember to wire.
func TestAppendFileChangesPrunesBeyondRetention(t *testing.T) {
	store := openFileChangeTestStore(t)
	now := time.Now().UTC()

	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "/proj", Path: "ancient.go", ChangeType: "modified", CreatedAt: now.Add(-FileChangeRetention - time.Hour)},
	}); err != nil {
		t.Fatalf("append ancient: %v", err)
	}
	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "/proj", Path: "fresh.go", ChangeType: "modified", CreatedAt: now},
	}); err != nil {
		t.Fatalf("append fresh: %v", err)
	}

	got, err := store.FileChangesSince("/proj", now.Add(-10*FileChangeRetention))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Path != "fresh.go" {
		t.Errorf("retention did not prune: %+v", got)
	}
}

// A row with no project, path or type could never be queried back; it must be
// skipped rather than stored as unusable noise.
func TestAppendFileChangesSkipsUnusableRows(t *testing.T) {
	store := openFileChangeTestStore(t)
	now := time.Now().UTC()

	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "", Path: "a.go", ChangeType: "modified", CreatedAt: now},
		{ProjectDir: "/proj", Path: "", ChangeType: "modified", CreatedAt: now},
		{ProjectDir: "/proj", Path: "b.go", ChangeType: "", CreatedAt: now},
		{ProjectDir: "/proj", Path: "good.go", ChangeType: "modified", CreatedAt: now},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.FileChangesSince("/proj", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Path != "good.go" {
		t.Errorf("unusable rows were stored: %+v", got)
	}
}

// A missing timestamp must still be queryable rather than landing at the zero
// time where every window filter would exclude it.
func TestAppendFileChangesDefaultsTimestamp(t *testing.T) {
	store := openFileChangeTestStore(t)

	if err := store.AppendFileChanges([]FileChangeRow{
		{ProjectDir: "/proj", Path: "a.go", ChangeType: "modified"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.FileChangesSince("/proj", time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d rows, want 1", len(got))
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("missing timestamp was stored as the zero time")
	}
}

func TestAppendFileChangesEmptyIsNoop(t *testing.T) {
	store := openFileChangeTestStore(t)
	if err := store.AppendFileChanges(nil); err != nil {
		t.Errorf("appending nothing should not error: %v", err)
	}
}
