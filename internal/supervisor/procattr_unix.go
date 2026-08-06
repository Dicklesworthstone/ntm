//go:build unix

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr sets platform-specific process attributes for clean shutdown.
// On Unix systems, this sets Setpgid to create a new process group.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess requests graceful shutdown of the daemon and every process
// it started. setSysProcAttr creates a dedicated process group, so signaling
// the group is necessary to avoid leaving daemon children behind.
func terminateProcess(p *os.Process) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGTERM); err != nil {
		_ = p.Signal(syscall.SIGTERM)
	}
}

// forceKillProcess terminates the daemon process group after the graceful
// shutdown deadline has elapsed.
func forceKillProcess(p *os.Process) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}
