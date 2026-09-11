//go:build unix

package pipeline

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
// until the lock is granted or ctx is done. flock locks belong to the open
// file description, so every call contends independently even within one
// process — and the kernel releases the lock if this process dies, which is
// what makes a crashed pipeline unable to wedge a pane.
func openLockedFile(ctx context.Context, lockPath string) (*lockedFile, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
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
