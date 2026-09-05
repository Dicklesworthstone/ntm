//go:build windows

package assignment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func acquireAssignmentFileLock(ctx context.Context, lockPath string) (func(), error) {
	localUnlock, err := lockAssignmentPathLocally(ctx, lockPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		localUnlock()
		return nil, err
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		localUnlock()
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	for {
		err = windows.LockFileEx(
			windows.Handle(lockFile.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = lockFile.Close()
			localUnlock()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = lockFile.Close()
			localUnlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	return func() {
		_ = windows.UnlockFileEx(windows.Handle(lockFile.Fd()), 0, 1, 0, overlapped)
		_ = lockFile.Close()
		localUnlock()
	}, nil
}

// acquireReapableAssignmentFileLock is the Windows counterpart of the Unix
// guarded lifecycle. LockFileEx contenders close a busy identity descriptor
// before releasing the stable guard, and removal happens after UnlockFileEx
// and Close because Windows does not permit deleting this open handle.
func acquireReapableAssignmentFileLock(ctx context.Context, lockPath, guardPath string) (func(), error) {
	localUnlock, err := lockAssignmentPathLocally(ctx, lockPath)
	if err != nil {
		return nil, err
	}

	var lockFile *os.File
	var overlapped *windows.Overlapped
	var lockToken []byte
	for {
		guardUnlock, guardErr := acquireAssignmentFileLock(ctx, guardPath)
		if guardErr != nil {
			localUnlock()
			return nil, guardErr
		}
		lockFile, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			guardUnlock()
			localUnlock()
			return nil, err
		}
		overlapped = new(windows.Overlapped)
		err = windows.LockFileEx(
			windows.Handle(lockFile.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			lockToken, err = markLockFile(lockFile)
			if err != nil {
				_ = windows.UnlockFileEx(windows.Handle(lockFile.Fd()), 0, 1, 0, overlapped)
				_ = lockFile.Close()
				guardUnlock()
				localUnlock()
				return nil, err
			}
			guardUnlock()
			break
		}
		_ = lockFile.Close()
		guardUnlock()
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			localUnlock()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			localUnlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	return func() {
		guardUnlock, guardErr := acquireAssignmentFileLock(context.Background(), guardPath)
		_ = windows.UnlockFileEx(windows.Handle(lockFile.Fd()), 0, 1, 0, overlapped)
		_ = lockFile.Close()
		if guardErr == nil {
			removeLockFileWithToken(lockPath, lockToken)
			guardUnlock()
		}
		localUnlock()
	}, nil
}

func syncAssignmentDirectory(string) error {
	// Windows FlushFileBuffers does not support directory handles opened by
	// os.Open. The temp files themselves are flushed before atomic replacement.
	return nil
}
