//go:build !windows && !linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Keep the leader unreaped until its entire isolated group stops executing.
// A leader-first Cmd.Wait allows PID/PGID reuse and can abandon background
// children. Unlike kill(-pgid, 0), ps distinguishes zombies from live work on
// Darwin and the other Unix platforms without Linux's waitid/procfs protocol.
func waitCommandWithProcessGroupCleanup(ctx context.Context, cmd *exec.Cmd) (result commandCleanupResult) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return commandCleanupResult{Err: errors.New("command cleanup requires a started, unwaited command")}
	}
	defer func() { verifyCommandOutputSettlement(cmd, &result) }()
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}
	// No group signals may follow this join: Cmd.Wait releases the pinned
	// leader identity. Bound even a failed kill; an unkillable process must
	// leave an unresolved journal rather than hang pipeline cancellation.
	reap := func(result commandCleanupResult) commandCleanupResult {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		timeout := commandPipeDrainTimeout
		if cmd.WaitDelay > 0 {
			timeout += cmd.WaitDelay
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case err := <-done:
			result.Err = errors.Join(result.Err, err)
			if cmd.ProcessState == nil {
				result.Settled = false
			}
		case <-timer.C:
			result.Err = errors.Join(result.Err, errors.New("command leader did not finish waiting after cleanup"))
			result.Settled = false
		}
		return result
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		_ = cmd.Process.Kill()
		return reap(commandCleanupResult{Err: errors.New("command cleanup requires its own process group")})
	}

	quietErr := waitCommandGroupQuiescent(ctx, cmd.Process.Pid)
	if quietErr == nil && ctx.Err() == nil {
		// Close the snapshot/fork race while the group ID remains pinned,
		// then verify the group again before publishing settlement.
		killErr := cancelCommandProcessGroup(cmd)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		settle, stop := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
		quietErr = waitCommandGroupQuiescent(settle, cmd.Process.Pid)
		stop()
		return reap(commandCleanupResult{Err: errors.Join(killErr, quietErr), Settled: killErr == nil && quietErr == nil})
	}
	result = commandCleanupResult{Cancelled: true, SignalSent: "SIGTERM"}
	termErr := signalCommandProcessGroup(cmd, syscall.SIGTERM)
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandCancelGracePeriod)
	graceErr := waitCommandGroupQuiescent(grace, cmd.Process.Pid)
	cancel()
	// A failed census must neither shorten the grace allowance nor disappear
	// from the final receipt. A plain deadline means live work used its grace;
	// a wrapped observation error also retains why quiescence was uncertain.
	if graceErr != nil && graceErr != context.DeadlineExceeded {
		result.Err = fmt.Errorf("observe command group during SIGTERM grace period: %w", graceErr)
	}
	// A child can fork after the snapshot starts and exit before its row is
	// read. Keep the leader pinned through a final kill and a fresh quiet
	// observation even when the graceful shutdown appeared complete.
	result.SignalSent = "SIGTERM,SIGKILL"
	killErr := cancelCommandProcessGroup(cmd)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	settle, stop := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
	quietErr = waitCommandGroupQuiescent(settle, cmd.Process.Pid)
	stop()
	if killErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("kill command group: %w", killErr))
	}
	if termErr != nil && !errors.Is(termErr, os.ErrProcessDone) {
		result.Err = errors.Join(result.Err, fmt.Errorf("terminate command group: %w", termErr))
	}
	if quietErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("command group did not settle: %w", quietErr))
	}
	result.Err = errors.Join(result.Err, ctx.Err())
	result.Settled = killErr == nil && quietErr == nil
	return reap(result)
}

func waitCommandGroupQuiescent(ctx context.Context, pgid int) error {
	return waitCommandGroupObservation(ctx, pgid, func() (bool, error) {
		return commandGroupHasLiveMembers(ctx, pgid)
	})
}

// groupSignalFoundOnlyZombies reports whether an EPERM from kill(-pgid) only
// means that the pinned group had no member left to signal. Darwin's killpg
// skips zombies and, with the POSIX semantics syscall.Kill uses, answers
// EPERM rather than ESRCH while the unreaped leader still pins the group;
// read as a failure, that fails the final signal of every quiet group. EPERM
// also covers a live member this process may not signal, so it is discounted
// only when a complete census still shows the pinned leader and no live
// member; a failed census keeps the error. Callers still take their own
// census before settling.
func groupSignalFoundOnlyZombies(pgid int) bool {
	alive, err := commandGroupHasLiveMembers(context.Background(), pgid)
	return err == nil && !alive
}

func commandGroupHasLiveMembers(ctx context.Context, pgid int) (bool, error) {
	return observeCommandGroupWithPS(ctx, pgid)
}
