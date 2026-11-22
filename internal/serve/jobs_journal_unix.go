//go:build !windows

package serve

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// Lock files are permanent rendezvous points. The kernel releases ownership on
// crash; unlinking a lock file would allow two servers to own different inodes.
func lockJobJournal(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("job journal already owned or unavailable: %w", err)
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			// Explicitly unlock before Close so a concurrent fork()+execve() in
			// another goroutine cannot keep the flock held on the duplicated open
			// file description until the child reaches execve().
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			closeErr = file.Close()
		})
		return closeErr
	}, nil
}
