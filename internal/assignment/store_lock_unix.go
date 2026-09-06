//go:build unix

package assignment

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// lockedFile is an open lock file holding an exclusive flock.
type lockedFile struct {
	path string
	file *os.File
}

// openLockedFile opens lockPath and takes an exclusive flock on it, polling
// until the lock is granted or ctx is done. flock locks belong to the open file
// description, so every call contends independently even within one process.
func openLockedFile(ctx context.Context, lockPath string) (*lockedFile, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &lockedFile{path: lockPath, file: lockFile}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lockFile.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = lockFile.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *lockedFile) unlockAndClose() {
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
}

// release drops the lock. A transient lock file is unlinked BEFORE the flock is
// released: waiters that already hold this inode open will still be granted the
// flock afterwards, but they then find the path gone or pointing elsewhere and
// retry (lockCurrentFile). Unlinking after the unlock would instead let a waiter
// win the lock on this inode and pass its identity check, only for this
// releaser to unlink that live lock file behind its back.
func (l *lockedFile) release(removePath bool) {
	if removePath {
		held, err := l.file.Stat()
		if err == nil {
			removeLockFileIfSame(held, l.path)
		}
	}
	l.unlockAndClose()
}

func syncAssignmentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
