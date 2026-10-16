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
	if leaderPID <= 1 {
		return fmt.Errorf("inspect command descendants: invalid leader %d", leaderPID)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("inspect command descendants: %w", err)
	}
	var failures error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() || pid <= 1 || pid == leaderPID {
			continue
		}
		// This first read only narrows the census. It is never authority to
		// signal: membership is read again after obtaining the pidfd below.
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if !commandProcessDisappeared(err) {
				failures = errors.Join(failures, fmt.Errorf("inspect process %d: %w", pid, err))
			}
			continue
		}
		group, ok := parseProcStatPGID(data)
		if !ok {
			failures = errors.Join(failures, fmt.Errorf("inspect process %d: malformed group evidence", pid))
			continue
		}
		if group != leaderPID {
			continue
		}
		if err := killCommandGroupMember(pid, leaderPID); err != nil {
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
func killCommandGroupMember(pid, leaderPID int) error {
	if pid <= 1 || leaderPID <= 1 || pid == leaderPID {
		return fmt.Errorf("kill command descendant: invalid member %d of group %d", pid, leaderPID)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("open process handle %d: %w", pid, err)
	}
	defer unix.Close(fd)
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if commandProcessDisappeared(err) {
			return nil
		}
		return fmt.Errorf("recheck process %d: %w", pid, err)
	}
	group, ok := parseProcStatPGID(data)
	if !ok {
		return fmt.Errorf("recheck process %d: malformed group evidence", pid)
	}
	if group != leaderPID {
		return nil
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process handle %d: %w", pid, err)
	}
	return nil
}

func commandProcessDisappeared(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}
