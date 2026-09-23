package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestPersistedWorkSnapshotCannotRepublishCacheRead(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n")
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
	work, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	// A cached observation must never acquire a new lifetime just because an
	// aggregator publishes the result of a status read as a fresh collection.
	if _, _, _, err := marshalWorkSnapshotObservation(work); !errors.Is(err, ErrWorkSnapshotReadOnly) {
		t.Fatal("cached work can be republished as a fresh observation")
	}
	work.Verification.FromCache = false
	if _, _, _, err := marshalWorkSnapshotObservation(work); !errors.Is(err, ErrWorkSnapshotReadOnly) {
		t.Fatal("mutable display flag erased the cache's read-only provenance")
	}

	// A failed read must not publish a newer unavailable marker either: it is
	// still an observation of an old cache, not an attempted tracker collection.
	failed, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, []byte("broken"))
	if err == nil {
		t.Fatal("expected cached corruption")
	}
	stampWorkSnapshotProject(failed, project, time.Now())
	if _, _, _, err := marshalWorkSnapshotObservation(failed); !errors.Is(err, ErrWorkSnapshotReadOnly) {
		t.Fatal("failed cache read can overwrite a newer tracker observation")
	}
}

func TestPersistedWorkSnapshotFreezesCandidateEvidence(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n"+`{"id":"b","status":"open"}`+"\n")
	input := snapshotCandidateFixture("a")
	score := 0.7
	input.Ready[0].Score = &score
	input.Ready[0].Labels = []string{"original"}
	work, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		return input, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both tasks are eligible in JSONL, but b was NOT in the actual direct
	// candidate read. Mutable output slices must not rewrite that evidence.
	input.Ready[0].ID = "b"
	input.Ready[0].Title = "later mutation"
	input.Ready[0].Labels[0] = "changed"
	score = 0.2
	_, payload, _, err := marshalWorkSnapshotObservation(work)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ready) != 1 || got.Ready[0].ID != "a" || got.Ready[0].Title != "Task a" || got.Ready[0].Labels[0] != "original" || *got.Ready[0].Score != 0.7 {
		t.Fatalf("mutable output changed the persisted candidate evidence: %+v", got.Ready)
	}
}

func TestPersistedWorkSnapshotRetainsCollectionTimeAndDetachedPayload(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n")
	before := time.Now()
	var directRead time.Time
	work, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		directRead = time.Now()
		return snapshotCandidateFixture("a"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key, first, collectedAt, err := marshalWorkSnapshotObservation(work)
	if err != nil || collectedAt.Before(before) || collectedAt.After(directRead) {
		t.Fatalf("publication time is not the beginning of collection: %v, %v", collectedAt, err)
	}
	original := string(first)
	first[0] = '!'
	// The enclosing aggregator or publisher finishing later cannot re-stamp
	// a real candidate read and make an old callback win the SQL ordering gate.
	stampWorkSnapshotProject(work, project, directRead.Add(time.Hour))
	_, second, again, err := marshalWorkSnapshotObservation(work)
	if err != nil || !again.Equal(collectedAt) || string(second) != original {
		t.Fatalf("retry altered the collection identity or shared mutable payload: %v", err)
	}
	var envelope workSnapshotEnvelope
	if err := json.Unmarshal(second, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ProjectDir != key || !envelope.CollectedAt.Equal(collectedAt) {
		t.Fatal("opaque payload lost publication identity")
	}

	failed := NewWorkSection()
	failed.Reason = "read failed"
	failed.Verification = &WorkVerification{}
	stampWorkSnapshotProject(failed, project, collectedAt)
	stampWorkSnapshotProject(failed, project, directRead.Add(time.Hour))
	_, marker, failedAt, err := marshalWorkSnapshotObservation(failed)
	if err != nil || !failedAt.Equal(collectedAt) {
		t.Fatalf("failure marker's age was renewed: %v %v", failedAt, err)
	}
	if err := json.Unmarshal(marker, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Unavailable == "" || !envelope.CollectedAt.Equal(collectedAt) {
		t.Fatal("failed collection did not publish a dated unavailable marker")
	}
}

func TestPersistedWorkSnapshotReopenCannotResurrectSupersededCollection(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n"+`{"id":"b","status":"open"}`+"\n")
	work, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		return snapshotCandidateFixture("a", "b"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	work = limitVerifiedWorkPreview(work, 1)
	project, payload, observedAt, err := marshalWorkSnapshotObservation(work)
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.NewRuntimeWorkSnapshot(project, payload, observedAt, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshots.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Closing twice is harmless, and ensures an early assertion does not leak
	// the handle before the explicit close/reopen boundary below.
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Transaction(func(tx *state.Tx) error { return tx.UpsertRuntimeWorkSnapshot(ctx, first) }); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || stored == nil {
		t.Fatalf("read persisted snapshot: %v", err)
	}
	if !stored.CollectedAt.Equal(observedAt) || stored.Revision != first.Revision {
		t.Fatal("restart changed the persisted collection identity")
	}
	restored, err := restoreWorkSnapshot(ctx, project, WorkVerificationPolicy{}, 1, stored.Payload)
	if err != nil || len(restored.Ready) != 1 || restored.Summary.Ready != 2 {
		t.Fatalf("restart lost the complete candidate count: %+v %v", restored, err)
	}

	newFailure := NewWorkSection()
	newFailure.Verification = &WorkVerification{}
	newFailure.Reason = "newer tracker read failed"
	stampWorkSnapshotProject(newFailure, project, observedAt.Add(time.Second))
	_, marker, failureAt, err := marshalWorkSnapshotObservation(newFailure)
	if err != nil {
		t.Fatal(err)
	}
	failure, err := state.NewRuntimeWorkSnapshot(project, marker, failureAt, failureAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Transaction(func(tx *state.Tx) error { return tx.UpsertRuntimeWorkSnapshot(ctx, failure) }); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := marshalWorkSnapshotObservation(restored); !errors.Is(err, ErrWorkSnapshotReadOnly) {
		t.Fatalf("cache read can manufacture a replacement generation: %v", err)
	}
	if err := reopened.Transaction(func(tx *state.Tx) error { return tx.UpsertRuntimeWorkSnapshot(ctx, first) }); !errors.Is(err, state.ErrWorkSnapshotSuperseded) {
		t.Fatalf("late original collection overwrote the failure marker: %v", err)
	}
	current, err := reopened.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || current.Revision != failure.Revision {
		t.Fatalf("newer failure was lost after attempted resurrection: %v", err)
	}
}
