package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const commandGroupPollInterval = 50 * time.Millisecond

// waitCommandWithProcessGroupCleanup owns the WHOLE isolated command group.
// Do not call Cmd.Wait concurrently: it would reap the leader, allowing its
// numeric PID/PGID to be reused before a delayed group signal. WNOWAIT observes
// exit without reaping, so every group signal is sent while identity is pinned.
// In particular, a shell exiting on SIGTERM does not abandon a descendant
// that ignores SIGTERM, redirects its output, or outlives the shell naturally.
func waitCommandWithProcessGroupCleanup(ctx context.Context, cmd *exec.Cmd) commandCleanupResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return commandCleanupResult{Err: errors.New("command cleanup requires a started, unwaited command")}
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		// Never infer group ownership from a process number alone.
		_ = cmd.Process.Kill()
		return commandCleanupResult{Err: errors.Join(errors.New("command cleanup requires its own process group"), cmd.Wait())}
	}
	// Detect unsupported waitid or an externally reaped child before sending
	// any numeric group signal. A successful WNOHANG probe does not reap.
	if err := observeCommandExit(cmd.Process.Pid, syscall.WNOHANG); err != nil {
		_ = cmd.Process.Kill()
		return commandCleanupResult{Err: errors.Join(err, cmd.Wait())}
	}
	exited := make(chan error, 1)
	go func() { exited <- observeCommandExit(cmd.Process.Pid, 0) }()
	var observationErr error
	leaderExited := false
	select {
	case observationErr = <-exited:
		leaderExited = true
		if observationErr != nil {
			_ = cmd.Process.Kill()
			return commandCleanupResult{Err: errors.Join(observationErr, cmd.Wait())}
		}
		if ctx.Err() == nil {
			// The shell may have forked work and exited before that work
			// finishes. Preserve its output and keep cancellation effective
			// until all ordinary group members stop executing.
			observationErr = waitCommandGroupQuiescent(ctx, cmd.Process.Pid)
			if observationErr == nil {
				// Close the enumeration/fork race while the group ID is still
				// pinned. Normal background writers have already finished.
				err := cancelCommandProcessGroup(cmd)
				if errors.Is(err, os.ErrProcessDone) {
					err = nil
				}
				return commandCleanupResult{Err: errors.Join(err, cmd.Wait())}
			}
		}
	case <-ctx.Done():
		// The observation goroutine is joined below, before Cmd.Wait.
	}
	if observationErr != nil && ctx.Err() == nil {
		// The leader is pinned, but failed procfs inspection cannot prove
		// quiescence. Terminate the owned group and surface that failure.
		_ = cancelCommandProcessGroup(cmd)
		return commandCleanupResult{Err: errors.Join(observationErr, cmd.Wait())}
	}

	result := commandCleanupResult{Cancelled: true, SignalSent: "SIGTERM"}
	termErr := signalCommandProcessGroup(cmd, syscall.SIGTERM)
	grace, cancel := context.WithTimeout(context.Background(), commandCancelGracePeriod)
	quietErr := waitCommandGroupQuiescent(grace, cmd.Process.Pid)
	cancel()
	if quietErr != nil {
		result.SignalSent = "SIGTERM,SIGKILL"
		killErr := cancelCommandProcessGroup(cmd)
		settle, stop := context.WithTimeout(context.Background(), commandPipeDrainTimeout)
		quietErr = waitCommandGroupQuiescent(settle, cmd.Process.Pid)
		stop()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			result.Err = errors.Join(result.Err, fmt.Errorf("kill command group: %w", killErr))
		}
	}
	if termErr != nil && !errors.Is(termErr, os.ErrProcessDone) {
		result.Err = errors.Join(result.Err, fmt.Errorf("terminate command group: %w", termErr))
	}
	if quietErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("command group did not settle: %w", quietErr))
	}
	// Join the observation goroutine before reaping. No waiter or deferred
	// numeric-PGID signal is allowed to survive this command's lifetime.
	if !leaderExited {
		observationErr = <-exited
	} else {
		observationErr = nil
	}
	result.Err = errors.Join(result.Err, ctx.Err(), observationErr, cmd.Wait())
	return result
}

// observeCommandExit uses the same 128-byte Linux siginfo buffer and
// WEXITED|WNOWAIT protocol as Go's os.Process implementation. The raw syscall
// is confined to this Linux file; no pointers survive the synchronous call.
func observeCommandExit(pid int, extraFlags int) error {
	var info [16]uint64
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT|uintptr(extraFlags), 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return os.NewSyscallError("waitid", errno)
		}
		return nil
	}
}

func waitCommandGroupQuiescent(ctx context.Context, pgid int) error {
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		alive, err := commandGroupHasLiveMembers(pgid)
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

// Zombies cannot execute or keep output pipes open. Their unreaped IDs may
// remain in procfs (notably under container PID 1), so they are not live work.
// This checks group membership, not parent PID: orphaned grandchildren have
// already been reparented by the time the command leader exits.
func commandGroupHasLiveMembers(pgid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("inspect command process group: %w", err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return false, fmt.Errorf("inspect process %s: %w", entry.Name(), err)
		}
		group, ok := parseProcStatPGID(data)
		if !ok || group != pgid {
			continue
		}
		fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
		if fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
