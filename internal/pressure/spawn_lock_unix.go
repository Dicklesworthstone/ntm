//go:build linux || darwin

package pressure

import (
	"errors"
	"os"
	"syscall"
)

func openSpawnAdmissionFile(path string) (*os.File, error) {
	// No symlink traversal, truncation, blocking on a FIFO, or inheritance by
	// agent processes. Each call opens independently so threads also contend.
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
			err = errors.New("spawn admission fence must be an owned, non-shared regular file with one link")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func trySpawnAdmissionLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func unlockSpawnAdmissionLock(file *os.File) {
	if file != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}
}
