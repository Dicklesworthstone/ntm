package pipeline

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// killProcessGroupDescendants is best-effort damage containment after a
// failed group kill, not proof of settlement. The caller retains the unreaped
// group leader throughout. Never fall back to kill(pid): between a procfs
// read and that signal, the PID could have been recycled for unrelated work.
func killProcessGroupDescendants(leaderPID int) error {
	return killProcessGroupDescendantsWithOps(leaderPID, systemCommandGroupFallbackOps())
}

// Keep failure injection local to one fallback. Membership and identity
// handles must use the same PID namespace: procfs numbers are only candidates,
// since an inherited /proc mount can describe a different namespace.
type commandGroupFallbackOps struct {
	readDir     func(string) ([]os.DirEntry, error)
	getpgid     func(int) (int, error)
	openPidfd   func(int, int) (int, error)
	signalPidfd func(int, unix.Signal, *unix.Siginfo, int) error
	closePidfd  func(int) error
}

func systemCommandGroupFallbackOps() commandGroupFallbackOps {
	return commandGroupFallbackOps{
		readDir:     os.ReadDir,
		getpgid:     syscall.Getpgid,
		openPidfd:   unix.PidfdOpen,
		signalPidfd: unix.PidfdSendSignal,
		closePidfd:  unix.Close,
	}
}

func killProcessGroupDescendantsWithOps(leaderPID int, ops commandGroupFallbackOps) error {
	if leaderPID <= 1 {
		return fmt.Errorf("inspect command descendants: invalid leader %d", leaderPID)
	}
	entries, err := ops.readDir("/proc")
	if err != nil {
		return fmt.Errorf("inspect command descendants: %w", err)
	}
	var failures error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() || pid <= 1 || pid == leaderPID {
			continue
		}
		// This first query only narrows the candidates. A procfs row may
		// describe an unrelated ancestor-namespace process with the same
		// number, so only kernel membership can authorize a local pidfd.
		group, err := ops.getpgid(pid)
		if err != nil {
			if !errors.Is(err, syscall.ESRCH) {
				failures = errors.Join(failures, fmt.Errorf("inspect process %d group: %w", pid, err))
			}
			continue
		}
		if group != leaderPID {
			continue
		}
		if err := killCommandGroupMember(pid, leaderPID, ops); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

// Open the identity handle BEFORE checking membership. If the process exits
// and its number is recycled during inspection, pidfd_send_signal still
// addresses the original process (or returns ESRCH), never the replacement.
// An unsupported or restricted pidfd syscall is an explicit cleanup failure;
// it must not opt back into the unsafe numeric signaling path.
func killCommandGroupMember(pid, leaderPID int, ops commandGroupFallbackOps) error {
	if pid <= 1 || leaderPID <= 1 || pid == leaderPID {
		return fmt.Errorf("kill command descendant: invalid member %d of group %d", pid, leaderPID)
	}
	fd, err := ops.openPidfd(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("open process handle %d: %w", pid, err)
	}
	defer ops.closePidfd(fd)
	group, err := ops.getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("recheck process %d group: %w", pid, err)
	}
	if group != leaderPID {
		return nil
	}
	if err := ops.signalPidfd(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process handle %d: %w", pid, err)
	}
	return nil
}
