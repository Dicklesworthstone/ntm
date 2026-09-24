//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package supervisor

import (
	"errors"
	"os"
	"syscall"
)

func lockDaemonOwnership(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("daemon ownership fence is not a regular file")
	}
	if err == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrDaemonOwned, err)
		}
		return nil, err
	}
	return f, nil
}

func daemonProcessMayLive(pid int) (bool, error) {
	if pid <= 0 {
		return true, errors.New("invalid daemon PID")
	}
	// A dead leader may leave descendants still using the same data store.
	// Never signal a recovered PID: PID reuse is a conservative refusal.
	for _, id := range []int{pid, -pid} {
		err := syscall.Kill(id, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			return true, nil
		}
		if !errors.Is(err, syscall.ESRCH) {
			return true, err
		}
	}
	return false, nil
}

func syncDaemonOwnershipDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
