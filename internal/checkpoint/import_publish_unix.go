//go:build linux || darwin

package checkpoint

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Lock the session directory itself, not a disposable lock file. Each import
// gets a distinct descriptor, so competing goroutines and processes contend;
// process exit releases the lock without leaving a stale ownership marker.
func openCheckpointImportDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrCheckpointImportBusy, path)
		}
		return nil, fmt.Errorf("lock checkpoint import directory: %w", err)
	}
	return file, nil
}

func closeCheckpointImportDirectory(dir *os.File) error {
	if dir == nil {
		return nil
	}
	// Explicitly unlock before closing: another goroutine calling exec.Command
	// may have forked while this fd was open, leaving a duplicate open file
	// description alive until execve closes O_CLOEXEC fds. Closing alone would
	// leave the flock held until that child calls execve.
	unlockErr := unix.Flock(int(dir.Fd()), unix.LOCK_UN)
	return errors.Join(unlockErr, dir.Close())
}

func syncCheckpointImportDirectory(dir *os.File) error { return dir.Sync() }
