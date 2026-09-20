//go:build windows

package serve

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

func lockJobJournal(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
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
