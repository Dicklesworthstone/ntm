//go:build unix

package assignment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
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
	for {
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		localUnlock()
	}, nil
}

// acquireReapableAssignmentFileLock holds a stable guard while opening or
// removing an identity-scoped lock file. A contender that finds the identity
// lock busy closes that descriptor before releasing the guard and retrying.
// Therefore no conforming waiter can retain the inode that the holder removes
// on release and later run concurrently with a new lock file at the same path.
func acquireReapableAssignmentFileLock(ctx context.Context, lockPath, guardPath string) (func(), error) {
	localUnlock, err := lockAssignmentPathLocally(ctx, lockPath)
	if err != nil {
		return nil, err
	}

	var lockFile *os.File
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
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			lockToken, err = markLockFile(lockFile)
			if err != nil {
				_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
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
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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
		// A release cannot inherit the caller's canceled context: it must finish
		// the lock lifecycle even when the operation itself was canceled.
		guardUnlock, guardErr := acquireAssignmentFileLock(context.Background(), guardPath)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		if guardErr == nil {
			removeLockFileWithToken(lockPath, lockToken)
			guardUnlock()
		}
		localUnlock()
	}, nil
}

func syncAssignmentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
