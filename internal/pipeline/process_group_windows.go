//go:build windows

package pipeline

import (
	"context"
	"os/exec"
	"time"
)

const commandCancelGracePeriod = 5 * time.Second

type commandCleanupResult struct {
	Err                 error
	Cancelled           bool
	SignalSent          string
	Settled             bool
	ExitBeforeSignalErr error
}

// configureCommandProcessGroup is a no-op on Windows. Unix's Setpgid +
// kill(-pid, ...) idiom does not have a direct Windows equivalent, and
// os/exec's Process.Kill terminates the process tree on Windows when a
// command was started normally — sufficient for the shell subprocess
// cancellation contract used by branch predicates and iteration sources.
func configureCommandProcessGroup(cmd *exec.Cmd) {}

// cancelCommandProcessGroup terminates the command via the OS-portable
// Process.Kill so cmd.Cancel handlers compile and behave reasonably on
// Windows release builds.
func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// waitCommandWithProcessGroupCleanup mirrors the Unix variant but uses
// Process.Kill instead of SIGTERM/SIGKILL. The grace period is preserved
// so callers see consistent timing behavior across platforms.
func waitCommandWithProcessGroupCleanup(ctx context.Context, cmd *exec.Cmd) commandCleanupResult {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return commandCleanupResult{Err: err, Settled: true}
	case <-ctx.Done():
		result := commandCleanupResult{
			Cancelled:  true,
			SignalSent: "Kill",
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case err := <-done:
			result.Err = err
			result.Settled = true
			if result.Err == nil {
				result.Err = ctx.Err()
			}
			return result
		case <-time.After(commandCancelGracePeriod):
			result.Err = ctx.Err()
			return result
		}
	}
}
