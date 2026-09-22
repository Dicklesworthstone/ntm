//go:build unix

package resilience

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func monitorPlatformSupported() error { return nil }

// Every caller opens its own descriptor. The lock is held until close and
// released by the kernel on process exit; its path is never unlinked.
func tryMonitorLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, ErrSessionMonitorOwned
		}
		return nil, fmt.Errorf("lock session monitor: %w", err)
	}
	return file, nil
}

// setDetachedProcess configures the command to run in a new session,
// detached from the terminal so it survives when the parent exits.
func setDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
