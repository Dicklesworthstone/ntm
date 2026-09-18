//go:build unix

package pipeline

import (
	"os/exec"
	"syscall"
)

func detachBackgroundProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
