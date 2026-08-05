//go:build !windows

package robot

import "syscall"

// signalTerm sends SIGTERM (graceful termination) to the process `pid`.
//
// Errors are intentionally ignored: the process may exit between the caller's
// liveness check and this signal, which is exactly the outcome the lifecycle
// kill path wants. Mirrors termProcess in internal/cli/reap_unix.go.
func signalTerm(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

// signalKill sends SIGKILL (forceful termination) to the process `pid`.
func signalKill(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
