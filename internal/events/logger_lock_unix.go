//go:build !windows

package events

import (
	"errors"
	"os"
	"syscall"
)

func tryLockEventLogFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func unlockEventLogFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
