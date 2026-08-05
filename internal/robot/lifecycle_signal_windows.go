//go:build windows

package robot

import "os"

// signalTerm terminates the process `pid`.
//
// Windows has no SIGTERM delivery for an arbitrary unrelated process, so this
// falls back to a hard termination via the process handle (TerminateProcess).
// Agent lifecycle management is fundamentally a Unix/tmux concern in ntm; on
// Windows this keeps the build compiling and still cleans up the process, just
// without the graceful-then-forceful escalation. Errors are ignored (the
// process may already have exited), matching the Unix behavior and the
// termProcess/killProcess pattern in internal/cli/reap_windows.go.
func signalTerm(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// signalKill forcefully terminates the process `pid`.
func signalKill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
