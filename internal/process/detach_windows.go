//go:build windows

package process

import "os/exec"

// DetachControllingTerminal is a no-op on Windows, which has no POSIX
// controlling-terminal job-control signals (SIGTTIN/SIGTTOU).
func DetachControllingTerminal(_ *exec.Cmd) {}
