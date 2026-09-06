//go:build windows

package assignment

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockedFile is an open lock file holding an exclusive LockFileEx byte lock.
type lockedFile struct {
	path       string
	file       *os.File
	overlapped *windows.Overlapped
}

// openLockedFile opens lockPath and takes an exclusive LockFileEx lock on its
// first byte, polling until the lock is granted or ctx is done. Byte-range
// locks belong to the handle, so every call contends independently even within
// one process.
func openLockedFile(ctx context.Context, lockPath string) (*lockedFile, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
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
			return &lockedFile{path: lockPath, file: lockFile, overlapped: overlapped}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, l.overlapped)
	_ = l.file.Close()
}

// release drops the lock. A transient lock file is removed AFTER the handle is
// closed, the opposite order from Unix: os.OpenFile does not grant
// FILE_SHARE_DELETE, so Windows refuses to delete the file while this handle or
// any waiter's handle is still open. That refusal is what makes removal safe
// here: a delete can only succeed when nobody holds the file, so no waiter can
// be left locking a deleted file, and the file simply stays behind until the
// last contender releases it. The identity captured before closing keeps a
// replacement created in the meantime from being removed by this releaser.
func (l *lockedFile) release(removePath bool) {
	var held os.FileInfo
	if removePath {
		if info, err := l.file.Stat(); err == nil {
			held = info
		}
	}
	l.unlockAndClose()
	if removePath {
		removeLockFileIfSame(held, l.path)
	}
}

func syncAssignmentDirectory(string) error {
	// Windows FlushFileBuffers does not support directory handles opened by
	// os.Open. The temp files themselves are flushed before atomic replacement.
	return nil
}
