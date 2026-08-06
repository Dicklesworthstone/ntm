//go:build windows

package session

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

var localMu sync.Mutex

// acquireLock acquires both process-level (LockFileEx) and thread-level
// (mutex) locks, matching the Unix implementation. Session saves perform a
// check-then-write sequence, so an in-process mutex alone lets two CLI
// processes overwrite the same saved session despite Overwrite being false.
func acquireLock() (func(), error) {
	localMu.Lock()

	// Ensure directory exists for the lock file.
	dir := StorageDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		localMu.Unlock()
		return nil, err
	}

	lockPath := filepath.Join(dir, "session.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		localMu.Unlock()
		return nil, err
	}

	overlapped := windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		localMu.Unlock()
		return nil, err
	}

	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
		_ = f.Close()
		localMu.Unlock()
	}, nil
}
