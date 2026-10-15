//go:build windows

package worktrees

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openMergeFenceFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Open the reparse point itself, not its target. The shared regular-file
	// check refuses it before locking. No sharing of delete/rename access.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func tryMergeFenceLock(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return fmt.Errorf("%w; retry after the active merge finishes", ErrMergeBusy)
	}
	if err != nil {
		return fmt.Errorf("acquire merge ownership: %w", err)
	}
	return nil
}
