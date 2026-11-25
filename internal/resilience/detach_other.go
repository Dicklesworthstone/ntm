//go:build !unix

package resilience

import (
	"errors"
	"os"
	"os/exec"
)

func monitorPlatformSupported() error {
	return errors.New("detached session monitoring requires Unix process and locking support")
}

func tryMonitorLock(string) (*os.File, error) { return nil, monitorPlatformSupported() }

func tryControlLock(string, bool) (*os.File, error) { return nil, monitorPlatformSupported() }

func closeControlLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

// setDetachedProcess is a no-op on non-Unix platforms.
// Process detachment requires platform-specific implementation on Windows.
func setDetachedProcess(cmd *exec.Cmd) {
	// On Windows, we would need to use CREATE_NEW_PROCESS_GROUP and
	// DETACHED_PROCESS flags, but the resilience monitor is primarily
	// designed for Unix environments where tmux runs.
}
