//go:build !unix

package resilience

import "os/exec"

// setDetachedProcess is a no-op on non-Unix platforms.
// Process detachment requires platform-specific implementation on Windows.
func setDetachedProcess(cmd *exec.Cmd) {
	// On Windows, we would need to use CREATE_NEW_PROCESS_GROUP and
	// DETACHED_PROCESS flags, but the resilience monitor is primarily
	// designed for Unix environments where tmux runs.
}
