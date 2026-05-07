//go:build !windows

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const commandCancelGracePeriod = 5 * time.Second

type commandCleanupResult struct {
	Err        error
	Cancelled  bool
	SignalSent string
}

func configureCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// cancelCommandProcessGroup is the cmd.Cancel handler used for shell
// subprocesses that opt into process-group isolation. Sending SIGKILL to
// -pid kills the whole group; if that fails (e.g. the process already
// exited), fall back to a single-process Kill.
func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func waitCommandWithProcessGroupCleanup(ctx context.Context, cmd *exec.Cmd) commandCleanupResult {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return commandCleanupResult{Err: err}
	case <-ctx.Done():
		result := commandCleanupResult{
			Cancelled:  true,
			SignalSent: "SIGTERM",
		}
		_ = signalCommandProcessGroup(cmd, syscall.SIGTERM)

		select {
		case err := <-done:
			result.Err = err
			if result.Err == nil {
				result.Err = ctx.Err()
			}
			return result
		case <-time.After(commandCancelGracePeriod):
			result.SignalSent = "SIGTERM,SIGKILL"
			_ = signalCommandProcessGroup(cmd, syscall.SIGKILL)
			err := <-done
			result.Err = err
			if result.Err == nil {
				result.Err = ctx.Err()
			}
			return result
		}
	}
}

func signalCommandProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	// bd-ob92m: when the group-targeted kill fails for any reason other
	// than ESRCH (the group is already empty), the single-process
	// fallback would have left every descendant reparented to init and
	// running. For SIGKILL we must scrub descendants explicitly: walk
	// /proc looking for processes whose pgid matches the leader's pgid
	// and SIGKILL each one. Best-effort; ignored on systems without
	// /proc.
	if sig == syscall.SIGKILL {
		killErr := cmd.Process.Kill()
		killProcessGroupDescendants(cmd.Process.Pid)
		return killErr
	}
	return cmd.Process.Signal(sig)
}

// killProcessGroupDescendants is a best-effort sweep of /proc that sends
// SIGKILL to any process whose pgid matches the supplied leader pid.
// Used as a fallback when the kernel-side `kill(-pgid, SIGKILL)` fails
// for non-ESRCH reasons (EPERM, sandbox surprises, etc.) so the
// "process-group cleanup on cancel" contract is honored even on
// degraded paths (bd-ob92m). Returns silently on systems where /proc
// is not available.
func killProcessGroupDescendants(leaderPID int) {
	if leaderPID <= 0 {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == leaderPID || pid <= 1 {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		pgid, ok := parseProcStatPGID(stat)
		if !ok || pgid != leaderPID {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// parseProcStatPGID extracts the process group id (field 5) from
// /proc/<pid>/stat, taking care to skip past the comm field which can
// contain spaces and parentheses.
func parseProcStatPGID(stat []byte) (int, bool) {
	idx := -1
	for i := len(stat) - 1; i >= 0; i-- {
		if stat[i] == ')' {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(stat) {
		return 0, false
	}
	rest := strings.TrimSpace(string(stat[idx+1:]))
	fields := strings.Fields(rest)
	// fields[0]=state, fields[1]=ppid, fields[2]=pgrp.
	if len(fields) < 3 {
		return 0, false
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, false
	}
	return pgid, true
}
