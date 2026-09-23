package state

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func workSnapshotTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store, path
}
func snapshotForTest(t *testing.T, project, payload string, at time.Time) *RuntimeWorkSnapshot {
	t.Helper()
	s, err := NewRuntimeWorkSnapshot(project, []byte(payload), at, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func publishSnapshotForTest(store *Store, snapshot *RuntimeWorkSnapshot) error {
	return store.Transaction(func(tx *Tx) error { return tx.UpsertRuntimeWorkSnapshot(context.Background(), snapshot) })
}

func TestRuntimeWorkSnapshotSurvivesReopenAndSeparatesProjects(t *testing.T) {
	store, path := workSnapshotTestStore(t)
	now := time.Now().UTC()
	first := snapshotForTest(t, t.TempDir(), `{"ready":[]}`, now)
	second := snapshotForTest(t, t.TempDir(), `{"unavailable":"source changed"}`, now)
	for _, s := range []*RuntimeWorkSnapshot{first, second} {
		if err := publishSnapshotForTest(store, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, want := range []*RuntimeWorkSnapshot{first, second} {
		got, err := reopened.GetRuntimeWorkSnapshot(context.Background(), want.ProjectDir)
		if err != nil || got == nil || got.Revision != want.Revision || string(got.Payload) != string(want.Payload) || !got.CollectedAt.Equal(want.CollectedAt) || !got.StaleAfter.Equal(want.StaleAfter) {
			t.Fatalf("reopen: got=%+v want=%+v err=%v", got, want, err)
		}
	}
	if got, err := reopened.GetRuntimeWorkSnapshot(context.Background(), t.TempDir()); err != nil || got != nil {
		t.Fatalf("missing project must not borrow another snapshot: %v %v", got, err)
	}
}

func TestRuntimeWorkSnapshotPublicationIsAtomic(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	ctx := context.Background()
	project := t.TempDir()
	now := time.Now().UTC()
	old := snapshotForTest(t, project, `{"ready":["old"]}`, now)
	if err := publishSnapshotForTest(store, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TABLE work_snapshot_atomic_probe (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("later normalized projection write failed")
	newer := snapshotForTest(t, project, `{"ready":["new"]}`, now.Add(time.Second))
	err := store.Transaction(func(tx *Tx) error {
		if err := tx.UpsertRuntimeWorkSnapshot(ctx, newer); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO work_snapshot_atomic_probe VALUES ('new')`); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	got, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || got.Revision != old.Revision {
		t.Fatalf("partial receipt committed: %v %v", got, err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM work_snapshot_atomic_probe`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows escaped rollback: %d %v", count, err)
	}
}

func TestRuntimeWorkSnapshotRejectsOldAndConflictingPublication(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	ctx := context.Background()
	project := t.TempDir()
	now := time.Now().UTC()
	newer := snapshotForTest(t, project, `{"unavailable":"latest read failed"}`, now)
	if err := publishSnapshotForTest(store, newer); err != nil {
		t.Fatal(err)
	}
	if err := publishSnapshotForTest(store, newer); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	for _, at := range []time.Time{now.Add(-time.Second), now} {
		old := snapshotForTest(t, project, `{"ready":["stale"]}`, at)
		if err := publishSnapshotForTest(store, old); !errors.Is(err, ErrWorkSnapshotSuperseded) {
			t.Fatalf("late/ambiguous collector accepted: %v", err)
		}
	}
	got, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || got.Revision != newer.Revision {
		t.Fatalf("new failure was replaced by stale success: %v %v", got, err)
	}
	if current, err := store.RuntimeWorkSnapshotIsCurrent(ctx, project, newer.Revision); err != nil || !current {
		t.Fatalf("current receipt lost: %t %v", current, err)
	}
	next := snapshotForTest(t, project, `{"ready":[]}`, now.Add(time.Second))
	if err := publishSnapshotForTest(store, next); err != nil {
		t.Fatal(err)
	}
	if current, err := store.RuntimeWorkSnapshotIsCurrent(ctx, project, newer.Revision); err != nil || current {
		t.Fatalf("old reader still current: %t %v", current, err)
	}
}

func TestRuntimeWorkSnapshotConcurrentConnectionsKeepNewest(t *testing.T) {
	store, path := workSnapshotTestStore(t)
	project := t.TempDir()
	now := time.Now().UTC()
	const n = 12
	others := make([]*Store, n)
	for i := range others {
		var err error
		others[i], err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer others[i].Close()
	}
	snapshots := make([]*RuntimeWorkSnapshot, n)
	for i := range snapshots {
		snapshots[i] = snapshotForTest(t, project, string(rune('a'+i)), now.Add(time.Duration(i)*time.Millisecond))
	}
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range others {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := publishSnapshotForTest(others[i], snapshots[i])
			if err != nil && !errors.Is(err, ErrWorkSnapshotSuperseded) {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	got, err := store.GetRuntimeWorkSnapshot(context.Background(), project)
	if err != nil || got == nil || got.Revision != snapshots[n-1].Revision {
		t.Fatalf("concurrent publication lost newest result: %v %v", got, err)
	}
}

func TestRuntimeWorkSnapshotDetectsTampering(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	ctx := context.Background()
	project := t.TempDir()
	snapshot := snapshotForTest(t, project, `{"ready":[]}`, time.Now().UTC())
	if err := publishSnapshotForTest(store, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE runtime_work_snapshots SET payload = ? WHERE project_dir = ?`, []byte(`{"ready":["injected"]}`), project); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetRuntimeWorkSnapshot(ctx, project); err == nil || got != nil {
		t.Fatalf("corruption became readiness: %v %v", got, err)
	}
}

func TestRuntimeWorkSnapshotValidationAndCancellation(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	project := t.TempDir()
	now := time.Now().UTC()
	data := []byte(`{"ready":[]}`)
	snapshot, err := NewRuntimeWorkSnapshot(project, data, now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data[0] = '!'
	if snapshot.Payload[0] != '{' {
		t.Fatal("caller can mutate prepared payload")
	}
	if snapshot.IsFreshAt(now.Add(-time.Nanosecond)) || !snapshot.IsFreshAt(now) || snapshot.IsFreshAt(snapshot.StaleAfter) {
		t.Fatal("invalid timestamp boundary")
	}
	for _, p := range []string{"", "relative"} {
		if _, err := NewRuntimeWorkSnapshot(p, []byte("x"), now, now.Add(time.Minute)); err == nil {
			t.Fatal("relative scope accepted")
		}
	}
	if _, err := NewRuntimeWorkSnapshot(project, []byte("x"), now, now); err == nil {
		t.Fatal("zero TTL accepted")
	}
	if _, err := NewRuntimeWorkSnapshot(project, []byte("x"), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 2, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("overflow timestamp accepted")
	}
	if _, err := NewRuntimeWorkSnapshot(project, nil, now, now.Add(time.Minute)); err == nil {
		t.Fatal("empty payload accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Transaction(func(tx *Tx) error { return tx.UpsertRuntimeWorkSnapshot(ctx, snapshot) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publish: %v", err)
	}
	if got, err := store.GetRuntimeWorkSnapshot(context.Background(), project); err != nil || got != nil {
		t.Fatalf("cancelled publication wrote: %v %v", got, err)
	}
	snapshot.Payload[0] = '!'
	if err := publishSnapshotForTest(store, snapshot); err == nil {
		t.Fatal("mutated prepared snapshot accepted")
	}
}

func TestRuntimeWorkSnapshotPrunesOnlyExpiredProjects(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	now := time.Now().UTC()
	old := snapshotForTest(t, t.TempDir(), "old", now.Add(-10*time.Minute))
	// Seed directly so publication GC has a real expired record to remove.
	if _, err := store.DB().Exec(`INSERT INTO runtime_work_snapshots VALUES (?, ?, ?, ?, ?)`, old.ProjectDir, old.Revision, old.CollectedAt.UnixNano(), old.StaleAfter.UnixNano(), old.Payload); err != nil {
		t.Fatal(err)
	}
	recent := snapshotForTest(t, t.TempDir(), "recent", now.Add(-2*time.Minute))
	if err := publishSnapshotForTest(store, recent); err != nil {
		t.Fatal(err)
	}
	current := snapshotForTest(t, t.TempDir(), "current", now)
	if err := publishSnapshotForTest(store, current); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := store.GetRuntimeWorkSnapshot(ctx, old.ProjectDir); err != nil || got != nil {
		t.Fatalf("expired project retained: %v %v", got, err)
	}
	for _, s := range []*RuntimeWorkSnapshot{recent, current} {
		if got, err := store.GetRuntimeWorkSnapshot(ctx, s.ProjectDir); err != nil || got == nil {
			t.Fatalf("active/grace project removed: %v %v", got, err)
		}
	}
}

func TestRuntimeWorkSnapshotReadBudgetIncludesStoreContention(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	project := t.TempDir()
	for _, generationRead := range []bool{false, true} {
		store.mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			if generationRead {
				_, err := store.RuntimeWorkSnapshotIsCurrent(ctx, project, "revision")
				done <- err
			} else {
				_, err := store.GetRuntimeWorkSnapshot(ctx, project)
				done <- err
			}
		}()
		select {
		case err := <-done:
			store.mu.Unlock()
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("writer contention misclassified: %v", err)
			}
		case <-time.After(time.Second):
			store.mu.Unlock()
			cancel()
			<-done
			t.Fatal("request outlived its budget while waiting for a local writer")
		}
	}
}
