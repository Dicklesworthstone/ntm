//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// DetachControllingTerminal starts cmd in a new session so neither interactive
// shells nor procps `ps` probes can steal or receive SIGTTIN/SIGTTOU from the
// caller's controlling terminal.
func DetachControllingTerminal(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return
	}
	cmd.SysProcAttr.Setsid = true
}
