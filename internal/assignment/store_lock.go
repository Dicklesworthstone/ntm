package assignment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
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

// acquireAtomicOperationLock takes the per-identity operation lock. Identities
// (bead IDs, occupancy keys) are unbounded over the life of a session, so these
// lock files are transient: the releaser unlinks its own file, and the store
// directory only ever holds files for identities that are currently locked.
func acquireAtomicOperationLock(ctx context.Context, storePath, namespace, identity string) (func(), error) {
	return acquireTransientAssignmentFileLock(ctx, atomicOperationLockPath(storePath, namespace, identity))
}

func atomicOperationLockPath(storePath, namespace, identity string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + identity))
	return storePath + ".atomic-" + namespace + "-" + hex.EncodeToString(digest[:16]) + ".lock"
}

// acquireAssignmentFileLock takes a cross-process exclusive lock on a lock file
// that stays on disk after release (the whole-store lock).
func acquireAssignmentFileLock(ctx context.Context, lockPath string) (func(), error) {
	return acquireAssignmentFileLockWithPolicy(ctx, lockPath, false)
}

// acquireTransientAssignmentFileLock takes a cross-process exclusive lock on a
// lock file that the releaser removes again, so per-identity lock paths do not
// accumulate. Removal is safe against contenders because a waiter only trusts a
// lock it acquired on the file that lockPath still names (see lockCurrentFile),
// and a releaser only removes the file it locked (see lockedFile.release).
func acquireTransientAssignmentFileLock(ctx context.Context, lockPath string) (func(), error) {
	return acquireAssignmentFileLockWithPolicy(ctx, lockPath, true)
}

func acquireAssignmentFileLockWithPolicy(ctx context.Context, lockPath string, transient bool) (func(), error) {
	localUnlock, err := lockAssignmentPathLocally(ctx, lockPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		localUnlock()
		return nil, err
	}

	locked, err := lockCurrentFile(ctx, lockPath, transient)
	if err != nil {
		localUnlock()
		return nil, err
	}

	return func() {
		locked.release(transient)
		localUnlock()
	}, nil
}

// lockCurrentFile takes the OS-level exclusive lock on lockPath. For transient
// locks it additionally verifies that the locked file is still the file the
// path names: a waiter that polled on a file the previous holder unlinked on
// release wins that lock trivially, but the lock is on an orphaned inode that
// no later contender can see. Such a waiter closes the orphan and reopens the
// path, which now yields either a fresh file or the one a newer contender
// created. The retry loop terminates because every retry follows a release.
func lockCurrentFile(ctx context.Context, lockPath string, transient bool) (*lockedFile, error) {
	for {
		locked, err := openLockedFile(ctx, lockPath)
		if err != nil {
			return nil, err
		}
		if !transient {
			return locked, nil
		}
		current, err := lockFileIsCurrent(locked.file, lockPath)
		if err != nil {
			locked.unlockAndClose()
			return nil, err
		}
		if current {
			return locked, nil
		}
		locked.unlockAndClose()
	}
}

// lockFileIsCurrent reports whether lockPath still names the open file behind
// lockFile. A missing path means the file was unlinked; a different identity
// means it was unlinked and recreated. Inode-number reuse cannot fool the check
// because the caller still holds the old file open, which keeps its inode alive.
func lockFileIsCurrent(lockFile *os.File, lockPath string) (bool, error) {
	held, err := lockFile.Stat()
	if err != nil {
		return false, err
	}
	current, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return os.SameFile(held, current), nil
}

// removeLockFileIfSame unlinks lockPath only while it still names the file
// described by held, so a releaser never removes a replacement created after
// its own file disappeared from the path (for example by an external cleanup).
func removeLockFileIfSame(held os.FileInfo, lockPath string) {
	if held == nil {
		return
	}
	current, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(held, current) {
		return
	}
	_ = os.Remove(lockPath)
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
