//go:build !windows

package hooks

import (
	"os"
	"os/exec"
	"syscall"
)

// configureHookProcessGroup puts a hook in its own process group and kills that
// whole group when the context expires. Without this, a timeout only kills the
// `sh -c` wrapper and leaves its children running (bd-gtyr3).
//
// Setpgid and Kill are POSIX-only, so this lives behind a build tag; see
// procgroup_windows.go for the Windows counterpart.
func configureHookProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			// The group already exited between timeout and signal.
			return os.ErrProcessDone
		}
		return err
	}
}
