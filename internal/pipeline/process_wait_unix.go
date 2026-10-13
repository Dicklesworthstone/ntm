//go:build !windows && !linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const commandGroupPollInterval = 50 * time.Millisecond

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
	if quietErr == nil {
		// Close the snapshot/fork race while the group ID remains pinned,
		// then verify the group again before publishing settlement.
		killErr := cancelCommandProcessGroup(cmd)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		settle, stop := context.WithTimeout(context.Background(), commandPipeDrainTimeout)
		quietErr = waitCommandGroupQuiescent(settle, cmd.Process.Pid)
		stop()
		return reap(commandCleanupResult{Err: errors.Join(killErr, quietErr), Settled: killErr == nil && quietErr == nil})
	}
	if ctx.Err() == nil {
		// Failed process inspection is not evidence of settlement. Kill the
		// still-owned group, but retain the unresolved launch for inspection.
		killErr := cancelCommandProcessGroup(cmd)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return reap(commandCleanupResult{Err: errors.Join(quietErr, killErr)})
	}

	result = commandCleanupResult{Cancelled: true, SignalSent: "SIGTERM"}
	termErr := signalCommandProcessGroup(cmd, syscall.SIGTERM)
	grace, cancel := context.WithTimeout(context.Background(), commandCancelGracePeriod)
	_ = waitCommandGroupQuiescent(grace, cmd.Process.Pid)
	cancel()
	// A child can fork after the snapshot starts and exit before its row is
	// read. Keep the leader pinned through a final kill and a fresh quiet
	// observation even when the graceful shutdown appeared complete.
	result.SignalSent = "SIGTERM,SIGKILL"
	killErr := cancelCommandProcessGroup(cmd)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	settle, stop := context.WithTimeout(context.Background(), commandPipeDrainTimeout)
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
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		alive, err := commandGroupHasLiveMembers(ctx, pgid)
		if err != nil || !alive {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// The leader must appear in every complete snapshot until we call Cmd.Wait.
// Missing or malformed output fails closed, including restricted ps output;
// a permission error from kill(0) is never accepted as proof of settlement.
func commandGroupHasLiveMembers(ctx context.Context, pgid int) (bool, error) {
	probe, cancel := context.WithTimeout(ctx, commandPipeDrainTimeout)
	defer cancel()
	// BSD and AIX ps name this column "state"; Solaris and illumos use "s".
	stateColumn := "state="
	if runtime.GOOS == "solaris" || runtime.GOOS == "illumos" {
		stateColumn = "s="
	}
	cmd := exec.CommandContext(probe, "ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", stateColumn)
	cmd.WaitDelay = commandPipeDrainTimeout
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("inspect command process group: %w", err)
	}
	leaderSeen, alive := false, false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return false, fmt.Errorf("inspect command process group: malformed ps row %q", line)
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil || pid < 0 || group < 0 {
			return false, fmt.Errorf("inspect command process group: invalid ps process identity %q", line)
		}
		if pid == pgid {
			if group != pgid {
				return false, errors.New("inspect command process group: leader changed process group")
			}
			leaderSeen = true
		}
		if group == pgid && fields[2][0] != 'Z' {
			alive = true
		}
	}
	if !leaderSeen {
		return false, errors.New("inspect command process group: unreaped leader missing from ps output")
	}
	return alive, nil
}
