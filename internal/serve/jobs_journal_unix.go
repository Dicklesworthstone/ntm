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
		once.Do(func() { closeErr = file.Close() })
		return closeErr
	}, nil
}
