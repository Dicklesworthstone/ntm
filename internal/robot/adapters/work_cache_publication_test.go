package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// Deliberately returns an existing object to exercise the public publisher's
// boundary, rather than testing only the serializer's private helpers.
type replayingWorkCollector struct {
	work *WorkSection
	err  error
}

func (c replayingWorkCollector) Collect(context.Context) (*SignalBatch, error) {
	return &SignalBatch{Work: c.work}, c.err
}
func (c replayingWorkCollector) RestoreWorkSnapshot(context.Context, []byte) (*WorkSection, error) {
	return nil, errors.New("unexpected restore")
}

func TestDurableWorkRejectsRepublishedCacheSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a", "b")}
	if _, err := collectDurableWork(ctx, store, project, c, false, time.Now); err != nil {
		t.Fatal(err)
	}
	cached, err := readDurableWork(ctx, store, project, c, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	failed, failureErr := restoreWorkSnapshot(ctx, project, WorkVerificationPolicy{}, 1, []byte("broken"))
	if failureErr == nil {
		t.Fatal("invalid cache unexpectedly restored")
	}
	c.collectErr = errors.New("new tracker observation failed")
	if _, err := collectDurableWork(ctx, store, project, c, true, time.Now); !errors.Is(err, c.collectErr) {
		t.Fatalf("fresh failure: %v", err)
	}
	latest, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || latest == nil {
		t.Fatalf("missing fresh failure marker: %v", err)
	}
	for _, work := range []*WorkSection{cached, failed, rejectWorkSource(cached, errors.New("later rejection"))} {
		work.Verification.FromCache = false // Display fields cannot grant publication authority.
		out, err := collectDurableWork(ctx, store, project, replayingWorkCollector{work, failureErr}, true, time.Now)
		if !errors.Is(err, ErrWorkSnapshotReadOnly) || out == nil || out.Available {
			t.Fatalf("cached evidence was republished: %+v %v", out, err)
		}
		current, err := store.GetRuntimeWorkSnapshot(ctx, project)
		if err != nil || current == nil || current.Revision != latest.Revision {
			t.Fatalf("replayed result replaced newer evidence: %+v %v", current, err)
		}
	}
}

func TestDurableWorkOriginalResultCannotAcquireNewLifetime(t *testing.T) {
	ctx := context.Background()
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
	batch, err := c.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, originalTime, err := marshalWorkSnapshotObservation(batch.Work)
	if err != nil {
		t.Fatal(err)
	}
	passThrough := replayingWorkCollector{work: batch.Work}
	if _, err := collectDurableWork(ctx, store, project, passThrough, true, time.Now); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || stored == nil || !stored.CollectedAt.Equal(originalTime) || !stored.StaleAfter.Equal(originalTime.Add(WorkCacheLifetime)) {
		t.Fatalf("publication renewed its source observation: %+v %v", stored, err)
	}
	if _, err := collectDurableWork(ctx, store, project, passThrough, true, time.Now); err != nil {
		t.Fatalf("identical original publication must remain retryable: %v", err)
	}
	after, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || after == nil || after.Revision != stored.Revision {
		t.Fatalf("retry created a new generation: %+v %v", after, err)
	}
	c.collectErr = errors.New("newer failure")
	if _, err := collectDurableWork(ctx, store, project, c, true, time.Now); !errors.Is(err, c.collectErr) {
		t.Fatal(err)
	}
	if _, err := collectDurableWork(ctx, store, project, passThrough, true, time.Now); !errors.Is(err, state.ErrWorkSnapshotSuperseded) {
		t.Fatalf("old live result resurrected over newer failure: %v", err)
	}
}

func TestDurableWorkRejectsReboundObservationTimes(t *testing.T) {
	for _, extendExpiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "retimestamp", true: "extended_expiry"}[extendExpiry], func(t *testing.T) {
			ctx := context.Background()
			store, _ := durableStoreFixture(t)
			project := durableProjectFixture(t)
			c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a")}
			if _, err := collectDurableWork(ctx, store, project, c, false, time.Now); err != nil {
				t.Fatal(err)
			}
			original, err := store.GetRuntimeWorkSnapshot(ctx, project)
			if err != nil || original == nil {
				t.Fatalf("missing receipt: %v", err)
			}
			collected, expires := original.CollectedAt.Add(time.Nanosecond), original.StaleAfter
			if extendExpiry {
				collected, expires = original.CollectedAt, original.StaleAfter.Add(time.Hour)
			}
			rebound, err := state.NewRuntimeWorkSnapshot(project, original.Payload, collected, expires)
			if err != nil {
				t.Fatal(err)
			}
			// Recompute the outer checksum as a buggy publisher would. An
			// internally consistent SQLite row is still not a new observation.
			if _, err := store.DB().Exec(`UPDATE runtime_work_snapshots SET revision = ?, collected_at_ns = ?, stale_after_ns = ? WHERE project_dir = ?`, rebound.Revision, rebound.CollectedAt.UnixNano(), rebound.StaleAfter.UnixNano(), project); err != nil {
				t.Fatal(err)
			}
			work, err := collectDurableWork(ctx, store, project, c, false, time.Now)
			if !errors.Is(err, worksource.ErrStale) || work != nil || c.collections != 1 {
				t.Fatalf("rebound lifetime accepted or hidden by recollection: %+v %v", work, err)
			}
		})
	}
}

func TestDurableWorkUpgradesLegacyPayloadThroughFreshCollection(t *testing.T) {
	ctx := context.Background()
	store, _ := durableStoreFixture(t)
	project := durableProjectFixture(t)
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
	var legacy map[string]any
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["version"] = 1
	delete(legacy, "collected_at")
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	record, err := state.NewRuntimeWorkSnapshot(project, payload, started, started.Add(WorkCacheLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishRuntimeWorkSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	c := &durableCollectorFixture{project: project, input: snapshotCandidateFixture("a", "b")}
	work, err := collectDurableWork(ctx, store, project, c, false, time.Now)
	durableAssertReady(t, work, err, 2, false)
	if c.restores != 1 || c.collections != 1 {
		t.Fatalf("legacy evidence was reused or not upgraded: %+v", c)
	}
	upgraded, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || upgraded == nil {
		t.Fatalf("missing upgraded receipt: %v", err)
	}
	var envelope workSnapshotEnvelope
	if err := json.Unmarshal(upgraded.Payload, &envelope); err != nil || envelope.Version != workSnapshotVersion || !envelope.CollectedAt.Equal(upgraded.CollectedAt) {
		t.Fatalf("upgrade did not publish bound original time: %+v %v", envelope, err)
	}
}
