//go:build !windows

package webhook

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var errOutboxBusy = errors.New("webhook outbox is busy; retry admission")

// Keep the lock bank's inodes stable. Closing the descriptor releases ownership
// after an attempt or process crash; never unlink a shared lock file.
func openOutboxLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		err = errors.New("webhook outbox lock must be a private regular file")
	}
	if err == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			return nil, errOutboxBusy
		}
		return nil, fmt.Errorf("lock webhook outbox: %w", err)
	}
	return file, nil
}
