//go:build windows

package tmux

import (
	"os/exec"
	"testing"
)

func setTestProcessGroup(_ *exec.Cmd) {}

func killTestProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func acquireGlobalTmuxTestLock(t *testing.T) {
	t.Helper()
}
