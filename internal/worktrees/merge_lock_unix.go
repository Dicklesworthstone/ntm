//go:build !windows

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openMergeFenceFile(path string) (*os.File, error) {
	// Do not follow a substituted symlink or block opening a FIFO. Never
	// truncate: the stable inode is shared by all cooperating invocations.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Nlink != 1 {
			err = errors.New("merge ownership file must be private, regular and unshared")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func tryMergeFenceLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return fmt.Errorf("%w; retry after the active merge finishes", ErrMergeBusy)
	}
	if err != nil {
		return fmt.Errorf("acquire merge ownership: %w", err)
	}
	return nil
}
