//go:build unix

package session

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var localMu sync.Mutex

// acquireLock acquires both process-level (flock) and thread-level (mutex) locks.
// Returns an unlock function to release both.
func acquireLock() (func(), error) {
	localMu.Lock()

	// Ensure directory exists for lock file
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

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		localMu.Unlock()
		return nil, err
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		localMu.Unlock()
	}, nil
}
