//go:build windows

package pipeline

import (
	"os/exec"
	"syscall"
)

func detachBackgroundProcess(cmd *exec.Cmd) error {
	const detachedProcess = 0x00000008
	const newProcessGroup = 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup}
	return nil
}
