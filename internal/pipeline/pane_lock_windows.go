//go:build windows

package pipeline

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockedFile is an open lock file holding an exclusive LockFileEx byte lock.
//
// It deliberately carries no path: unlike internal/assignment's sibling type,
// pane lock files are never unlinked (they are stable per-pane rendezvous
// points), so there is nothing a stored path would be used for.
type lockedFile struct {
	file       *os.File
	overlapped *windows.Overlapped
}

// openLockedFile opens lockPath and takes an exclusive LockFileEx lock on its
// first byte, polling until the lock is granted or ctx is done. Byte-range
// locks belong to the handle, so every call contends independently even within
// one process, and Windows releases them when the owning process exits — the
// same crash-safety flock gives us on Unix.
func openLockedFile(ctx context.Context, lockPath string) (*lockedFile, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
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
			return &lockedFile{file: lockFile, overlapped: overlapped}, nil
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
