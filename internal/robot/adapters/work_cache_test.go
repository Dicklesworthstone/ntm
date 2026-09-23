package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

type durableCollectorFixture struct {
	project               string
	input                 *WorkSection
	policy                WorkVerificationPolicy
	collectErr            error
	collections, restores int
	afterRestore          func()
}

func (c *durableCollectorFixture) Collect(ctx context.Context) (*SignalBatch, error) {
	c.collections++
	work, err := collectWorkWithSource(ctx, c.project, c.policy, func(context.Context) (*WorkSection, error) { return c.input, c.collectErr })
	stampWorkSnapshotProject(work, c.project)
	if err == nil {
		work = limitVerifiedWorkPreview(work, 1)
	}
	return &SignalBatch{Work: work}, err
}
func (c *durableCollectorFixture) RestoreWorkSnapshot(ctx context.Context, payload []byte) (*WorkSection, error) {
	c.restores++
	work, err := restoreWorkSnapshot(ctx, c.project, c.policy, 1, payload)
	if c.afterRestore != nil {
		c.afterRestore()
	}
	return work, err
}

func durableStoreFixture(t *testing.T) (*state.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store, path
}
func durableProjectFixture(t *testing.T) string {
	t.Helper()
	return snapshotSourceFixture(t, `{"id":"blocked","status":"open","dependencies":[{"depends_on_id":"missing","type":"blocks"}]}`+"\n"+`{"id":"a","status":"open"}`+"\n"+`{"id":"b","status":"open"}`+"\n")
}
func durableAssertReady(t *testing.T, work *WorkSection, err error, total int, cached bool) {
	t.Helper()
	if err != nil || work == nil || !work.Available || work.Summary == nil || work.Summary.Ready != total || work.Verification == nil || work.Verification.FromCache != cached {
		t.Fatalf("work=%+v err=%v want ready=%d cached=%v", work, err, total, cached)
	}
	if total > 0 && len(work.Ready) != 1 {
		t.Fatalf("display limit lost: %+v", work.Ready)
	}
}

func TestDurableWorkReopensWithoutRecollectingAndKeepsFullCounts(t *testing.T) {
	store, path := durableStoreFixture(t)
	project := durableProjectFixture(t)
	ctx := context.Background()
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("blocked", "a", "b")}
	work, err := collectDurableWork(ctx, store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 2, false)
	original, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || original == nil {
		t.Fatalf("missing durable receipt: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c.collectErr = errors.New("tool must not execute during cache reuse")
	work, err = collectDurableWork(ctx, reopened, project, c, false, time.Now)
	durableAssertReady(t, work, err, 2, true)
	if c.collections != 1 || c.restores != 1 || work.Ready[0].ID != "a" || len(work.Verification.Excluded) != 1 {
		t.Fatalf("cache bypassed filtering or reran tools: %+v %+v", c, work)
	}
	if work.Verification.CacheCollectedAt != original.CollectedAt.Format(time.RFC3339Nano) || work.Verification.CacheExpiresAt != original.StaleAfter.Format(time.RFC3339Nano) {
		t.Fatal("reuse changed original freshness window")
	}
	after, err := reopened.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || after.Revision != original.Revision {
		t.Fatalf("read renewed persistence: %v", err)
	}
}

func TestDurableWorkMismatchRequiresExplicitRefresh(t *testing.T) {
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	ctx := context.Background()
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a", "b")}
	work, err := collectDurableWork(ctx, store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 2, false)
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(`{"id":"a","status":"closed"}`+"\n"+`{"id":"b","status":"open"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	work, err = collectDurableWork(ctx, store, project, c, false, time.Now)
	if !errors.Is(err, worksource.ErrStale) || c.collections != 1 || (work != nil && work.Available) {
		t.Fatalf("mismatch hidden by collection: %+v %v calls=%d", work, err, c.collections)
	}
	work, err = collectDurableWork(ctx, store, project, c, true, time.Now)
	durableAssertReady(t, work, err, 1, false)
	if c.collections != 2 || work.Ready[0].ID != "b" {
		t.Fatal("explicit refresh did not recollect source")
	}
}

func TestDurableWorkCorruptCacheNeverFallsBack(t *testing.T) {
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
	if _, err := collectDurableWork(context.Background(), store, project, c, false, time.Now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE runtime_work_snapshots SET payload = ? WHERE project_dir = ?`, []byte("corrupt"), project); err != nil {
		t.Fatal(err)
	}
	work, err := collectDurableWork(context.Background(), store, project, c, false, time.Now)
	if !errors.Is(err, worksource.ErrStale) || work != nil || c.collections != 1 {
		t.Fatalf("corrupt cache became healthy: %+v %v", work, err)
	}
}

func TestDurableWorkFailureReplacesHealthyReceiptAndRecovers(t *testing.T) {
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	ctx := context.Background()
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
	if _, err := collectDurableWork(ctx, store, project, c, false, time.Now); err != nil {
		t.Fatal(err)
	}
	old, _ := store.GetRuntimeWorkSnapshot(ctx, project)
	c.collectErr = errors.New("tracker temporarily unavailable")
	work, err := collectDurableWork(ctx, store, project, c, true, time.Now)
	if !errors.Is(err, c.collectErr) || work.Available || len(work.Ready) != 0 {
		t.Fatalf("collection failure was hidden: %+v %v", work, err)
	}
	failed, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || failed.Revision == old.Revision {
		t.Fatalf("old healthy receipt survived newer failure: %v", err)
	}
	c.collectErr = nil
	work, err = collectDurableWork(ctx, store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 1, false)
	if c.collections != 3 {
		t.Fatal("unavailable receipt was not explicitly recollected")
	}
}

func TestDurableWorkRechecksPeerReservationsAcrossReads(t *testing.T) {
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	reserved := true
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a", "b")}
	c.policy.readReservations = func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
		byBead := map[string][]string{}
		if reserved {
			byBead["a"] = []string{"peer"}
		}
		return &agentmail.WorkReservationSnapshot{ProjectKey: project, ProjectID: 7, ObservedAt: time.Now(), ByBead: byBead}, nil
	}
	work, err := collectDurableWork(context.Background(), store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 1, false)
	if work.Ready[0].ID != "b" {
		t.Fatal("reserved work advertised")
	}
	reserved = false
	work, err = collectDurableWork(context.Background(), store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 2, true)
	if c.collections != 1 || work.Ready[0].ID != "a" {
		t.Fatal("released candidate lost because only preview was persisted")
	}
}

func TestDurableWorkRejectsExpiryOrReplacementDuringRestore(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "replacement"}[replace], func(t *testing.T) {
			store, _ := durableStoreFixture(t)
			project := durableProjectFixture(t)
			ctx := context.Background()
			c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
			if _, err := collectDurableWork(ctx, store, project, c, false, time.Now); err != nil {
				t.Fatal(err)
			}
			clock := time.Now().UTC()
			c.afterRestore = func() {
				if !replace {
					clock = clock.Add(WorkCacheLifetime)
					return
				}
				newer, err := state.NewRuntimeWorkSnapshot(project, []byte(`{"unavailable":"newer writer"}`), clock.Add(time.Nanosecond), clock.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PublishRuntimeWorkSnapshot(ctx, newer); err != nil {
					t.Fatal(err)
				}
			}
			work, err := readDurableWork(ctx, store, project, c, func() time.Time { return clock })
			if work != nil || !errors.Is(err, ErrWorkSnapshotUnavailable) {
				t.Fatalf("expired/replaced observation escaped: %+v %v", work, err)
			}
		})
	}
}

func TestDurableWorkCancellationDoesNotCollectOrWrite(t *testing.T) {
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if work, err := collectDurableWork(ctx, store, project, c, true, time.Now); work != nil || !errors.Is(err, context.Canceled) || c.collections != 0 {
		t.Fatalf("cancelled request performed work: %+v %v", work, err)
	}
	if record, err := store.GetRuntimeWorkSnapshot(context.Background(), project); err != nil || record != nil {
		t.Fatalf("cancelled request wrote a receipt: %+v %v", record, err)
	}
}
