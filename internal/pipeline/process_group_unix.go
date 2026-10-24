//go:build !windows

package pipeline

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	commandCancelGracePeriod = 5 * time.Second
	commandPipeDrainTimeout  = time.Second
)

type commandCleanupResult struct {
	Err        error
	Cancelled  bool
	SignalSent string
	Settled    bool // The owned group is quiet and its captured output reached EOF.
}

// verifyCommandOutputSettlement runs after the process-group proof and the
// Cmd.Wait join. An inherited output pipe can outlive the group (setsid), and
// Wait's ExitError takes precedence over a concurrent ErrWaitDelay. Such a run
// must keep its launch journal unresolved, not authorize an automatic retry.
// This does not claim to contain descendants that detach AND close all pipes.
func verifyCommandOutputSettlement(cmd *exec.Cmd, result *commandCleanupResult) {
	if !result.Settled {
		return
	}
	if errors.Is(result.Err, exec.ErrWaitDelay) {
		result.Settled = false
	}
	for _, stream := range []struct {
		name   string
		writer interface{}
	}{{"stdout", cmd.Stdout}, {"stderr", cmd.Stderr}} {
		capture, ok := stream.writer.(*cappedWriter)
		if ok && !capture.outputCaptureComplete() {
			result.Settled = false
			result.Err = errors.Join(result.Err, fmt.Errorf("%s output capture did not reach EOF; inspect detached command work before replay", stream.name))
		}
	}
}

func configureCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A descendant that deliberately leaves the group can still inherit
	// stdout/stderr. It must not hold cmd.Wait (and run ownership) forever.
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}
}

// cancelCommandProcessGroup force-stops an owned group. The caller must still
// hold its leader identity; it must not be used after a competing Cmd.Wait.
func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	return signalCommandProcessGroup(cmd, syscall.SIGKILL)
}

func signalCommandProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if cmd.Process.Pid <= 1 || cmd.ProcessState != nil || cmd.SysProcAttr == nil ||
		!cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		return errors.New("signal command process group: an owned, unreaped group leader is required")
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if errors.Is(err, syscall.EPERM) && groupSignalFoundOnlyZombies(cmd.Process.Pid) {
		return os.ErrProcessDone
	}
	// A failed group signal is not repaired by killing only its leader.
	// Keep the original failure so the caller cannot publish settlement on
	// the strength of a partial fallback. Linux can additionally stop group
	// members through identity-stable handles, never stale numeric PIDs.
	groupErr := fmt.Errorf("signal command process group: %w", err)
	if sig == syscall.SIGKILL {
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(groupErr, killErr, killProcessGroupDescendants(cmd.Process.Pid))
	}
	fallbackErr := cmd.Process.Signal(sig)
	if errors.Is(fallbackErr, os.ErrProcessDone) {
		fallbackErr = nil
	}
	return errors.Join(groupErr, fallbackErr)
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
