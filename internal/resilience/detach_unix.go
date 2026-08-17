//go:build unix

package resilience

import (
	"os/exec"
	"syscall"
)

// setDetachedProcess configures the command to run in a new session,
// detached from the terminal so it survives when the parent exits.
func setDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
