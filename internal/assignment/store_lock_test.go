package assignment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func atomicLockFiles(t *testing.T, storePath string) []string {
	t.Helper()
	matches, err := filepath.Glob(storePath + ".atomic-*.lock")
	if err != nil {
		t.Fatalf("glob atomic lock files: %v", err)
	}
	return matches
}

// TestAtomicOperationLockFilesDoNotAccumulate pins the inode leak: every
// distinct bead ID, occupancy key, and cleanup identity used to leave its own
// assignments.json.atomic-*.lock behind forever.
func TestAtomicOperationLockFilesDoNotAccumulate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("atomic-lock-bounded")
	storePath := store.path
	ctx := context.Background()

	for i := range 150 {
		identity := fmt.Sprintf("ntm-unique-%d", i)
		unlock, err := acquireAtomicBeadOperationLock(ctx, storePath, identity)
		if err != nil {
			t.Fatalf("bead lock %s: %v", identity, err)
		}
		if got := atomicLockFiles(t, storePath); len(got) != 1 {
			t.Fatalf("while held, %d lock files exist: %v", len(got), got)
		}
		unlock()

		unlock, err = acquireAtomicTargetOperationLock(ctx, storePath, "%"+fmt.Sprint(i))
		if err != nil {
			t.Fatalf("target lock %d: %v", i, err)
		}
		unlock()

		unlock, err = store.AcquireExternalCleanupLock(ctx, identity)
		if err != nil {
			t.Fatalf("cleanup lock %s: %v", identity, err)
		}
		unlock()
	}

	if got := atomicLockFiles(t, storePath); len(got) != 0 {
		t.Fatalf("%d transient lock files survived release: %v", len(got), got)
	}
}

// TestStoreFileLockRemainsPersistent guards the whole-store lock against the
// transient policy: assignments.json.lock is a stable rendezvous point and must
// keep existing after release.
func TestStoreFileLockRemainsPersistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storePath := NewStore("atomic-lock-persistent").path

	unlock, err := acquireStoreFileLock(storePath)
	if err != nil {
		t.Fatalf("acquire store lock: %v", err)
	}
	unlock()
	if _, err := os.Stat(storePath + ".lock"); err != nil {
		t.Fatalf("whole-store lock file must persist after release: %v", err)
	}
}

// TestTransientLockReleaseKeepsReplacementFile covers the stale-releaser case:
// if the held lock file vanished from its path and something else now lives
// there, release must not remove the replacement.
func TestTransientLockReleaseKeepsReplacementFile(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "assignments.json.atomic-bead-replaced.lock")

	unlock, err := acquireTransientAssignmentFileLock(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("acquire transient lock: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("simulate external removal: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("replacement"), 0600); err != nil {
		t.Fatalf("simulate replacement: %v", err)
	}

	unlock()

	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("replacement lock file was removed by a stale releaser: %v", err)
	}
	if string(content) != "replacement" {
		t.Fatalf("replacement content = %q, want %q", content, "replacement")
	}
}

// TestTransientLockWaiterAbandonsUnlinkedFile drives the exact race the
// identity check exists for: a waiter polling on the holder's file must not
// treat a flock granted on the unlinked inode as ownership of the path.
func TestTransientLockWaiterAbandonsUnlinkedFile(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "assignments.json.atomic-bead-orphan.lock")
	ctx := context.Background()

	holder, err := lockCurrentFile(ctx, lockPath, true)
	if err != nil {
		t.Fatalf("holder lock: %v", err)
	}

	type outcome struct {
		locked *lockedFile
		err    error
	}
	waiterDone := make(chan outcome, 1)
	go func() {
		locked, err := lockCurrentFile(ctx, lockPath, true)
		waiterDone <- outcome{locked: locked, err: err}
	}()

	// Give the waiter time to open the holder's file and start polling on it.
	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-waiterDone:
		t.Fatalf("waiter acquired while holder held the lock: %+v", got)
	default:
	}

	holder.release(true)
	// The waiter may already have recreated the path, so both present and
	// absent are acceptable here; any other stat error is not.
	if _, err := os.Stat(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after release: %v", err)
	}

	var got outcome
	select {
	case got = <-waiterDone:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never acquired after release")
	}
	if got.err != nil {
		t.Fatalf("waiter error: %v", got.err)
	}
	current, err := lockFileIsCurrent(got.locked.file, lockPath)
	if err != nil {
		t.Fatalf("identity check: %v", err)
	}
	if !current {
		t.Fatal("waiter holds a lock on a file the path no longer names")
	}
	got.locked.release(true)
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last releaser left the lock file behind: %v", err)
	}
}

// TestTransientLockMutualExclusionAcrossIndependentHandles runs contenders
// that bypass the in-process path lock, so each goroutine holds its own open
// file description and contends at the OS level exactly like a separate
// process. Mutual exclusion must hold across releases that unlink the file.
func TestTransientLockMutualExclusionAcrossIndependentHandles(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "assignments.json.atomic-bead-race.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders = 8
	const rounds = 40
	var inCritical atomic.Int32
	var entries atomic.Int32
	var wg sync.WaitGroup
	failures := make(chan error, contenders)
	for c := range contenders {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				locked, err := lockCurrentFile(ctx, lockPath, true)
				if err != nil {
					failures <- fmt.Errorf("contender %d round %d: %v", id, round, err)
					return
				}
				if !inCritical.CompareAndSwap(0, 1) {
					locked.release(true)
					failures <- fmt.Errorf("contender %d round %d entered the critical section concurrently", id, round)
					return
				}
				entries.Add(1)
				time.Sleep(200 * time.Microsecond)
				inCritical.Store(0)
				locked.release(true)
			}
		}(c)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if got := entries.Load(); got != contenders*rounds {
		t.Fatalf("critical section entered %d times, want %d", got, contenders*rounds)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file survived the last release: %v", err)
	}
}

// TestTransientLockHonorsContextWhileContending keeps the cancellation
// behavior of the transient path identical to the persistent one.
func TestTransientLockHonorsContextWhileContending(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "assignments.json.atomic-bead-ctx.lock")
	holder, err := acquireTransientAssignmentFileLock(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer holder()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := lockCurrentFile(ctx, lockPath, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contender error = %v, want deadline exceeded", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("a canceled waiter must not remove the holder's lock file: %v", err)
	}
}
