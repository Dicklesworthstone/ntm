//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setSysProcAttr sets platform-specific process attributes for clean shutdown.
// On Windows, CREATE_NEW_PROCESS_GROUP makes the child PID the process-group ID
// so shutdown can target the daemon group with CTRL_BREAK_EVENT.
func setSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// terminateProcess attempts graceful shutdown, falling back to force kill.
// On Windows, CTRL_BREAK_EVENT is delivered to the process group whose ID is the
// daemon PID created with CREATE_NEW_PROCESS_GROUP. If the process is detached,
// has no console, or the control event cannot be delivered, fall back to Kill.
func terminateProcess(p *os.Process) {
	if p == nil {
		return
	}
	if err := generateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid)); err != nil {
		forceKillProcess(p)
	}
}

// forceKillProcess is used after the graceful CTRL_BREAK_EVENT timeout.
func forceKillProcess(p *os.Process) {
	if p == nil {
		return
	}
	_ = killProcess(p)
}

var (
	generateConsoleCtrlEvent = windows.GenerateConsoleCtrlEvent
	killProcess              = func(p *os.Process) error { return p.Kill() }
)
