//go:build windows

package hooks

import "os/exec"

// configureHookProcessGroup is intentionally a no-op on Windows. There is no
// POSIX process group to signal, and syscall.Setpgid / syscall.Kill do not
// exist there — referencing them unconditionally broke the Windows build
// entirely, which is why this split exists.
//
// Cancellation still works: exec.CommandContext's default Cancel kills the
// child when the context expires, and the caller's WaitDelay bounds how long
// Wait blocks afterward. Only the "kill the whole descendant tree" guarantee is
// weaker here than on POSIX.
func configureHookProcessGroup(cmd *exec.Cmd) {}
