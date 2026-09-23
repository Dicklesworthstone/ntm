package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublishRuntimeWorkSnapshotHonorsWaitingWriterCancellation(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	snapshot := snapshotForTest(t, t.TempDir(), "payload", time.Now())
	store.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- store.PublishRuntimeWorkSnapshot(ctx, snapshot) }()
	cancel()
	select {
	case err := <-done:
		store.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(time.Second):
		store.mu.Unlock()
		<-done
		t.Fatal("cancelled publisher waited for unrelated writer")
	}
	if got, err := store.GetRuntimeWorkSnapshot(context.Background(), snapshot.ProjectDir); err != nil || got != nil {
		t.Fatalf("cancelled publisher wrote: %+v %v", got, err)
	}
}

func TestPublishRuntimeWorkSnapshotRollsBackRejectedMutation(t *testing.T) {
	store, _ := workSnapshotTestStore(t)
	project := t.TempDir()
	now := time.Now().UTC()
	old := snapshotForTest(t, project, "old", now)
	ctx := context.Background()
	if err := store.PublishRuntimeWorkSnapshot(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_work_snapshot BEFORE UPDATE ON runtime_work_snapshots BEGIN SELECT RAISE(ABORT, 'publication rejected'); END`); err != nil {
		t.Fatal(err)
	}
	newer := snapshotForTest(t, project, "new", now.Add(time.Nanosecond))
	if err := store.PublishRuntimeWorkSnapshot(ctx, newer); err == nil {
		t.Fatal("rejected write succeeded")
	}
	got, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil || got.Revision != old.Revision {
		t.Fatalf("failure lost prior receipt: %+v %v", got, err)
	}
}
