package assignment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

type assignmentPathLock struct {
	token chan struct{}
	refs  int
}

var assignmentPathLocks = struct {
	sync.Mutex
	entries map[string]*assignmentPathLock
}{entries: make(map[string]*assignmentPathLock)}

func acquireStoreFileLock(storePath string) (func(), error) {
	return acquireAssignmentFileLock(context.Background(), storePath+".lock")
}

func acquireAtomicBeadOperationLock(ctx context.Context, storePath, beadID string) (func(), error) {
	return acquireAtomicOperationLock(ctx, storePath, "bead", beadID)
}

func acquireAtomicTargetOperationLock(ctx context.Context, storePath, target string) (func(), error) {
	return acquireAtomicOperationLock(ctx, storePath, "target", strings.TrimSpace(target))
}

// AcquireExternalCleanupLock serializes the non-idempotent external release
// boundary for one bead across processes. Callers must reload and recheck the
// durable assignment after acquiring the lock and before any external effect.
// This lock is always acquired outside the bead and target operation locks.
func (s *AssignmentStore) AcquireExternalCleanupLock(ctx context.Context, beadID string) (func(), error) {
	if s == nil {
		return nil, errors.New("assignment store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	return acquireAtomicOperationLock(ctx, s.path, "external-cleanup", beadID)
}

func acquireAtomicOperationLock(ctx context.Context, storePath, namespace, identity string) (func(), error) {
	digest := sha256.Sum256([]byte(namespace + "\x00" + identity))
	lockPath := storePath + ".atomic-" + namespace + "-" + hex.EncodeToString(digest[:16]) + ".lock"
	return acquireAssignmentFileLock(ctx, lockPath)
}

func lockAssignmentPathLocally(ctx context.Context, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	assignmentPathLocks.Lock()
	entry := assignmentPathLocks.entries[path]
	if entry == nil {
		entry = &assignmentPathLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		assignmentPathLocks.entries[path] = entry
	}
	entry.refs++
	assignmentPathLocks.Unlock()
	releaseRef := func() {
		assignmentPathLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(assignmentPathLocks.entries, path)
		}
		assignmentPathLocks.Unlock()
	}

	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-entry.token:
	}
	// When a lock release races with cancellation, both select cases may be
	// ready and Go may choose the token. Do not let an already-canceled caller
	// cross the durable/external assignment boundary merely because it won that
	// race; return the token and fail closed instead.
	if err := ctx.Err(); err != nil {
		entry.token <- struct{}{}
		releaseRef()
		return nil, err
	}
	return func() {
		entry.token <- struct{}{}
		releaseRef()
	}, nil
}
