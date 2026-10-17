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

	"golang.org/x/sys/unix"
)

const commandGroupPollInterval = 50 * time.Millisecond

var errCommandLeaderRunning = errors.New("command leader is still running")

// waitCommandWithProcessGroupCleanup owns the WHOLE isolated command group.
// Do not call Cmd.Wait concurrently: it would reap the leader, allowing its
// numeric PID/PGID to be reused before a delayed group signal. WNOWAIT observes
// exit without reaping, so every group signal is sent while identity is pinned.
// In particular, a shell exiting on SIGTERM does not abandon a descendant
// that ignores SIGTERM, redirects its output, or outlives the shell naturally.
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
	// All group signals must finish before this handoff. Wait owns cmd from
	// here on, even if it cannot finish within the cleanup budget. In that
	// case retain one final reaper (with no delayed group signals), rather
	// than hanging the pipeline or falsely settling its launch journal.
	// Callers may keep reading synchronized captures, but must not reuse cmd.
	reap := func(result commandCleanupResult) commandCleanupResult {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		timer := time.NewTimer(commandPipeDrainTimeout + cmd.WaitDelay)
		defer timer.Stop()
		select {
		case err := <-done:
			result.Err = errors.Join(result.Err, err)
			if cmd.ProcessState == nil {
				result.Settled = false
			}
		case <-timer.C:
			result.Err = errors.Join(result.Err, errors.New("command leader did not finish waiting after cleanup; execution remains unresolved"))
			result.Settled = false
		}
		return result
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		// Never infer group ownership from a process number alone.
		killErr := cmd.Process.Kill()
		return reap(commandCleanupResult{Err: errors.Join(errors.New("command cleanup requires its own process group"), killErr)})
	}
	// Detect unsupported waitid or an externally reaped child before sending
	// any numeric group signal. A live or exited child remains unreaped.
	if err := observeCommandExit(cmd.Process.Pid, syscall.WNOHANG); err != nil && !errors.Is(err, errCommandLeaderRunning) {
		killErr := cmd.Process.Kill()
		return reap(commandCleanupResult{Err: errors.Join(err, killErr)})
	}
	// A blocking waitid goroutine cannot be canceled if SIGKILL is denied
	// or the leader is stuck in uninterruptible I/O. Poll without reaping so
	// cancellation never has to join an unbounded exit observer.
	observationErr := waitCommandLeaderExit(ctx, cmd.Process.Pid)
	if observationErr != nil && !errors.Is(observationErr, context.Canceled) && !errors.Is(observationErr, context.DeadlineExceeded) {
		// Lost waitability is not authority for a numeric group signal.
		killErr := cmd.Process.Kill()
		return reap(commandCleanupResult{Err: errors.Join(observationErr, ctx.Err(), killErr), Cancelled: ctx.Err() != nil})
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
			// Signal delivery is not proof that every child stopped. Take
			// a fresh census while the leader still pins the group ID.
			settle, stop := context.WithTimeout(context.Background(), commandPipeDrainTimeout)
			quietErr := waitCommandGroupQuiescent(settle, cmd.Process.Pid)
			stop()
			return reap(commandCleanupResult{Err: errors.Join(err, quietErr), Settled: err == nil && quietErr == nil})
		}
	}
	if observationErr != nil && ctx.Err() == nil {
		// The leader is pinned, but failed procfs inspection cannot prove
		// quiescence. Terminate the owned group and surface that failure.
		killErr := cancelCommandProcessGroup(cmd)
		return reap(commandCleanupResult{Err: errors.Join(observationErr, killErr)})
	}

	result = commandCleanupResult{Cancelled: true, SignalSent: "SIGTERM"}
	termErr := signalCommandProcessGroup(cmd, syscall.SIGTERM)
	grace, cancel := context.WithTimeout(context.Background(), commandCancelGracePeriod)
	_ = waitCommandGroupQuiescent(grace, cmd.Process.Pid)
	cancel()
	// Even a successful graceful census is not atomic with fork/exit. Match
	// the Unix waiter: close that race with a final signal to the still-pinned
	// group, then observe quiescence again before granting replay permission.
	result.SignalSent = "SIGTERM,SIGKILL"
	killErr := cancelCommandProcessGroup(cmd)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	settle, stop := context.WithTimeout(context.Background(), commandPipeDrainTimeout)
	quietErr := waitCommandGroupQuiescent(settle, cmd.Process.Pid)
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

// observeCommandExit distinguishes a live child from an observed exit while
// preserving waitability. Use x/sys's architecture-specific siginfo layout;
// a successful WNOHANG syscall alone does not prove that the child exited.
func observeCommandExit(pid int, extraFlags int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|extraFlags, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return os.NewSyscallError("waitid", err)
		}
		if info.Signo == 0 {
			return errCommandLeaderRunning
		}
		return nil
	}
}

func waitCommandLeaderExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := observeCommandExit(pid, syscall.WNOHANG); !errors.Is(err, errCommandLeaderRunning) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
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
// A quiet result requires a complete, valid census containing the unreaped
// leader in its expected group. Missing evidence must not authorize replay.
func commandGroupHasLiveMembers(pgid int) (bool, error) {
	if pgid <= 1 {
		return false, fmt.Errorf("inspect command process group: invalid group ID %d", pgid)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("inspect command process group: %w", err)
	}
	leaderSeen, alive := false, false
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return false, fmt.Errorf("inspect process %s: %w", entry.Name(), err)
		}
		group, processState, err := parseCommandGroupStat(data, pid)
		if err != nil {
			return false, err
		}
		if pid == pgid {
			if group != pgid {
				return false, errors.New("inspect command process group: leader changed process group")
			}
			leaderSeen = true
		}
		if group == pgid && processState != "Z" && processState != "X" {
			alive = true
		}
	}
	if !leaderSeen {
		return false, errors.New("inspect command process group: unreaped leader missing from procfs")
	}
	return alive, nil
}

// Validate identity and state as well as group membership. Silently skipping a
// malformed row could hide a live member; accepting a different PID could turn
// a restricted or inconsistent procfs view into false settlement evidence.
func parseCommandGroupStat(data []byte, expectedPID int) (int, string, error) {
	text := string(data)
	open, close := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	invalid := func() (int, string, error) {
		return 0, "", fmt.Errorf("inspect process %d: malformed process-group evidence", expectedPID)
	}
	if open < 1 || close <= open {
		return invalid()
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil || pid != expectedPID || pid <= 0 {
		return invalid()
	}
	group, ok := parseProcStatPGID(data)
	fields := strings.Fields(text[close+1:])
	if !ok || group < 0 || len(fields) < 3 || len(fields[0]) != 1 {
		return invalid()
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return invalid()
	}
	// Unknown single-character states remain live, never proof of quiescence.
	return group, fields[0], nil
}
