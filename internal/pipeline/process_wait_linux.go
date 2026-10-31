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

var (
	errCommandLeaderRunning  = errors.New("command leader is still running")
	errCommandGroupBirthRace = errors.New("process creation raced command group observation")
	errCommandGroupMountRace = errors.New("procfs mount changed during command group observation")
)

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
	if err := observeCommandExitWithRetry(ctx, cmd.Process.Pid); err != nil && err != errCommandLeaderRunning {
		var killErr error
		if ctx.Err() != nil {
			// Process.Kill can fall back to a numeric PID when pidfds are
			// unavailable. Definite ownership loss forbids that signal too.
			if !errors.Is(err, syscall.ECHILD) && !errors.Is(err, syscall.ESRCH) {
				killErr = cmd.Process.Kill()
			}
		} else {
			// Leave inherited pipes open for healthy descendants, even if
			// their leader exits after we hand it to the final reaper. The
			// outer reap timer still bounds this unresolved return.
			cmd.WaitDelay = 0
		}
		return reap(commandCleanupResult{Err: errors.Join(err, ctx.Err(), killErr), Cancelled: ctx.Err() != nil})
	}
	// A blocking waitid goroutine cannot be canceled if SIGKILL is denied
	// or the leader is stuck in uninterruptible I/O. Poll without reaping so
	// cancellation never has to join an unbounded exit observer.
	observationErr := waitCommandLeaderExit(ctx, cmd.Process.Pid)
	if observationErr != nil && observationErr != context.Canceled && observationErr != context.DeadlineExceeded {
		// Lost waitability is not authority for a numeric group signal, or
		// a request to kill a healthy leader. Even actual cancellation must
		// not use Process.Kill after definite ownership loss: without pidfd
		// support that method signals the potentially recycled numeric PID.
		var killErr error
		if ctx.Err() != nil {
			if !errors.Is(observationErr, syscall.ECHILD) && !errors.Is(observationErr, syscall.ESRCH) {
				killErr = cmd.Process.Kill()
			}
		} else {
			cmd.WaitDelay = 0
		}
		return reap(commandCleanupResult{Err: errors.Join(observationErr, ctx.Err(), killErr), Cancelled: ctx.Err() != nil})
	}
	if ctx.Err() == nil {
		// The shell may have forked work and exited before that work
		// finishes. Preserve its output and keep cancellation effective
		// until all ordinary group members stop executing.
		observationErr = waitCommandGroupQuiescent(ctx, cmd.Process.Pid)
		if observationErr == nil && ctx.Err() == nil {
			// Quiet evidence includes an unchanged kernel birth counter and
			// accounts for surviving threads. Never use an unsolicited kill
			// to repair a snapshot that might have missed a new descendant.
			// No executing member remains to fork after this certificate.
			// Hand off the pinned leader and verify captured-stream EOF.
			return reap(commandCleanupResult{Settled: true})
		}
	}
	result = commandCleanupResult{Cancelled: true, SignalSent: "SIGTERM"}
	termErr := signalCommandProcessGroup(cmd, syscall.SIGTERM)
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandCancelGracePeriod)
	graceErr := waitCommandGroupQuiescent(grace, cmd.Process.Pid)
	cancel()
	// The deadline alone means live work used its normal grace allowance.
	// Failed observation is different: retain its cause, but never shorten
	// the grace period or mistake it for evidence that the children exited.
	if graceErr != nil && graceErr != context.DeadlineExceeded {
		result.Err = fmt.Errorf("observe command group during SIGTERM grace period: %w", graceErr)
	}
	// Even a successful graceful census is not atomic with fork/exit. Match
	// the Unix waiter: close that race with a final signal to the still-pinned
	// group, then observe quiescence again before granting replay permission.
	result.SignalSent = "SIGTERM,SIGKILL"
	killErr := cancelCommandProcessGroup(cmd)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	settle, stop := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
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

const commandLeaderObservationRetryTimeout = 250 * time.Millisecond

type commandLeaderObservationTestHookKey struct{}

// Retain the leader while retrying a failed non-reaping probe. Even when the
// caller cancels during these bounded retries, recovering waitability permits
// ordinary owned-group cleanup. Reaping on the first fault would permanently
// surrender that authority and leave subsequent cancellation unable to stop
// descendants. ECHILD/ESRCH are different: ownership was lost, and probing the
// same numeric PID again could observe a different process after reuse.
func observeCommandExitWithRetry(ctx context.Context, pid int) error {
	observe := func(pid int) error { return observeCommandExit(pid, syscall.WNOHANG) }
	if hook, ok := ctx.Value(commandLeaderObservationTestHookKey{}).(func(int) error); ok && hook != nil {
		observe = hook
	}
	observationErr := observe(pid)
	if observationErr == nil || observationErr == errCommandLeaderRunning {
		return observationErr
	}
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(commandLeaderObservationRetryTimeout)
	defer deadline.Stop()
	for {
		if errors.Is(observationErr, syscall.ECHILD) || errors.Is(observationErr, syscall.ESRCH) {
			return fmt.Errorf("command leader %d is no longer waitable: %w", pid, observationErr)
		}
		select {
		case <-deadline.C:
			// Always wrap observation failures. Only waitCommandLeaderExit's
			// direct context error may enter group cancellation without a new
			// successful probe; an error wrapping cancellation is no proof.
			return fmt.Errorf("observe command leader %d after bounded retries: %w", pid, observationErr)
		case <-ticker.C:
			observationErr = observe(pid)
			if observationErr == nil || observationErr == errCommandLeaderRunning {
				return observationErr
			}
		}
	}
}

func waitCommandLeaderExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := observeCommandExitWithRetry(ctx, pid); err != errCommandLeaderRunning {
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
	return waitCommandGroupObservation(ctx, pgid, func() (bool, error) {
		return commandGroupHasLiveMembers(pgid)
	})
}

// groupSignalFoundOnlyZombies is always false on Linux: kill(-pgid) signals
// zombie members successfully, so EPERM there always names a live member this
// process may not signal.
func groupSignalFoundOnlyZombies(int) bool {
	return false
}

// Zombies cannot execute or keep output pipes open. Their unreaped IDs may
// remain in procfs (notably under container PID 1), so they are not live work.
// This checks group membership, not parent PID: orphaned grandchildren have
// already been reparented by the time the command leader exits.
// A quiet result requires a complete, valid census containing the unreaped
// leader in its expected group. Missing evidence must not authorize replay.
func commandGroupHasLiveMembers(pgid int) (bool, error) {
	return inspectCommandGroup(pgid, commandGroupInspector{
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		getpgid:  syscall.Getpgid,
	})
}

// Keep the observation operations local to each census. Besides making the
// failure paths testable without changing process-wide state, this prevents a
// test or a concurrent cleanup from replacing another command's observer.
type commandGroupInspector struct {
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)
	getpgid  func(int) (int, error)
}

func inspectCommandGroup(pgid int, inspector commandGroupInspector) (bool, error) {
	if pgid <= 1 {
		return false, fmt.Errorf("inspect command process group: invalid group ID %d", pgid)
	}
	mountBefore, err := readCommandProcfsVisibility(inspector.readFile)
	if err != nil {
		return false, err
	}
	birthsBefore, err := readCommandProcessBirths(inspector.readFile)
	if err != nil {
		return false, err
	}
	entries, err := inspector.readDir("/proc")
	if err != nil {
		return false, fmt.Errorf("inspect command process group: %w", err)
	}
	leaderSeen, alive := false, false
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		// Kernel membership narrows the census BEFORE procfs interpretation.
		// A malformed or unreadable row from another group cannot affect this
		// command. Still enumerate every PID: orphaned grandchildren no longer
		// appear in the leader's children list. A later birth-counter check
		// rejects quiet snapshots in which a descendant could have forked
		// after enumeration and exited before its own row was inspected.
		group, groupErr := inspector.getpgid(pid)
		if errors.Is(groupErr, syscall.ESRCH) {
			continue
		}
		if groupErr == nil {
			if group < 0 {
				return false, fmt.Errorf("inspect process %d: invalid kernel process group %d", pid, group)
			}
			if pid == pgid && group != pgid {
				return false, errors.New("inspect command process group: leader changed process group")
			}
			if group != pgid {
				continue
			}
		} else {
			// An LSM or syscall filter may deny getpgid while allowing procfs.
			// A fully validated stat row still supplies the original census
			// proof. Without that independent evidence, retain both failures.
			groupErr = fmt.Errorf("inspect process %d group: %w", pid, groupErr)
		}
		data, err := inspector.readFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if groupErr == nil && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)) {
				continue
			}
			return false, errors.Join(groupErr, fmt.Errorf("inspect process %s: %w", entry.Name(), err))
		}
		group, processState, err := parseCommandGroupStat(data, pid)
		if err != nil {
			return false, errors.Join(groupErr, err)
		}
		if pid == pgid {
			if group != pgid {
				return false, errors.New("inspect command process group: leader changed process group")
			}
			leaderSeen = true
		}
		if group == pgid {
			if processState != "Z" && processState != "X" {
				alive = true
			} else {
				// A zombie thread-group leader can still have executing
				// threads. /proc enumerates the leader, not each of its TIDs.
				threads, err := commandGroupStatThreads(data, pid)
				if err != nil {
					return false, errors.Join(groupErr, err)
				}
				alive = alive || threads > 1
			}
		}
	}
	if !leaderSeen {
		return false, errors.New("inspect command process group: unreaped leader missing from procfs")
	}
	if alive {
		return true, nil
	}
	mountAfter, err := readCommandProcfsVisibility(inspector.readFile)
	if err != nil {
		return false, err
	}
	if mountBefore != mountAfter {
		return false, errCommandGroupMountRace
	}
	birthsAfter, err := readCommandProcessBirths(inspector.readFile)
	if err != nil {
		return false, err
	}
	if birthsBefore != birthsAfter {
		return false, errCommandGroupBirthRace
	}
	return false, nil
}

// A stable birth counter cannot repair hidden process directories. A setuid
// or non-dumpable descendant can remain in the owned group while hidepid=2/4
// omits it from readdir. hidepid=1 only restricts access to rows: enumeration
// remains complete, and an unreadable owned row already fails closed below.
func readCommandProcfsVisibility(readFile func(string) ([]byte, error)) (string, error) {
	data, err := readFile("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("inspect command procfs visibility: %w", err)
	}
	invalid := func(reason string) (string, error) {
		return "", fmt.Errorf("inspect command procfs visibility: %s", reason)
	}
	identity := ""
	for _, line := range strings.SplitAfter(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 5 {
			return invalid("malformed mountinfo record")
		}
		if fields[4] != "/proc" {
			continue
		}
		if !strings.HasSuffix(line, "\n") {
			return invalid("truncated /proc mountinfo record")
		}
		if identity != "" {
			return invalid("ambiguous overlapping /proc mounts")
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || len(fields) != separator+4 || fields[3] != "/" || fields[separator+1] != "proc" {
			return invalid("/proc is not a complete procfs mount")
		}
		mountID, idErr := strconv.ParseUint(fields[0], 10, 64)
		parentID, parentErr := strconv.ParseUint(fields[1], 10, 64)
		major, minor, deviceOK := strings.Cut(fields[2], ":")
		_, majorErr := strconv.ParseUint(major, 10, 64)
		_, minorErr := strconv.ParseUint(minor, 10, 64)
		if idErr != nil || mountID == 0 || parentErr != nil || parentID == 0 || !deviceOK || majorErr != nil || minorErr != nil {
			return invalid("invalid /proc mount identity")
		}
		hidepidSeen := false
		for _, options := range []string{fields[5], fields[separator+3]} {
			for _, option := range strings.Split(options, ",") {
				name, value, assigned := strings.Cut(option, "=")
				if !strings.HasPrefix(name, "hidepid") {
					continue
				}
				if hidepidSeen || !assigned || name != "hidepid" {
					return invalid("ambiguous hidepid mount option")
				}
				hidepidSeen = true
				switch value {
				case "0", "off", "1", "noaccess":
				default:
					return invalid("hidepid mount option does not guarantee visible process directories")
				}
			}
		}
		// Preserve mount identity separately from complete-visible options:
		// remounting off/noaccess does not change directory completeness.
		identity = strings.Join(fields[:5], "\x00") + "\x00" + strings.Join(fields[separator+1:separator+3], "\x00")
	}
	if identity == "" {
		return invalid("/proc mount is missing from mountinfo")
	}
	// NSpid lists IDs from this procfs namespace through the task's own
	// namespace. A single matching ID proves that procfs rows and getpgid
	// refer to the same processes; equal numbers in nested namespaces do not.
	// /proc/self resolves the thread-group leader, including on worker threads.
	data, err = readFile("/proc/self/status")
	if err != nil {
		return "", fmt.Errorf("inspect command procfs PID namespace: %w", err)
	}
	namespaceSeen := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "NSpid:" {
			continue
		}
		if namespaceSeen || len(fields) != 2 || !strings.HasSuffix(line, "\n") {
			return invalid("PID namespace evidence must contain one complete NSpid value")
		}
		namespaceSeen = true
		pid, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || pid == 0 {
			return invalid("malformed PID namespace evidence")
		}
		if pid != uint64(os.Getpid()) {
			return invalid("procfs PID namespace does not match command observer")
		}
	}
	if !namespaceSeen {
		return invalid("PID namespace evidence missing from /proc/self/status")
	}
	return identity, nil
}

// Linux increments total_forks under tasklist_lock before waking a new task,
// including CLONE_THREAD tasks, and exposes it as /proc/stat's processes row.
// An unchanged counter across enumeration and inspection rules out a live
// descendant hidden by a fork/exit handoff. Global churn can invalidate this
// certificate; the caller retries unknown evidence without signalling work.
func readCommandProcessBirths(readFile func(string) ([]byte, error)) (uint64, error) {
	data, err := readFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("inspect command process birth counter: %w", err)
	}
	var births uint64
	found := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "processes" {
			continue
		}
		if found || len(fields) != 2 || !strings.HasSuffix(line, "\n") {
			return 0, errors.New("inspect command process birth counter: malformed processes row")
		}
		births, err = strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("inspect command process birth counter: %w", err)
		}
		found = true
	}
	if !found {
		return 0, errors.New("inspect command process birth counter: processes row missing")
	}
	return births, nil
}

func commandGroupStatThreads(data []byte, pid int) (uint64, error) {
	// parseCommandGroupStat has already validated comm and process identity.
	text := string(data)
	fields := strings.Fields(text[strings.LastIndexByte(text, ')')+1:])
	// num_threads is field 20, or index 17 after comm. Require the following
	// field too so a record truncated within the thread count cannot say 1.
	if len(fields) < 19 {
		return 0, fmt.Errorf("inspect process %d: missing thread-count evidence", pid)
	}
	threads, err := strconv.ParseUint(fields[17], 10, 64)
	if err != nil || threads == 0 {
		return 0, fmt.Errorf("inspect process %d: malformed thread-count evidence", pid)
	}
	return threads, nil
}

// procTaskExiting is the kernel's PF_EXITING.
const procTaskExiting = 0x4

// procStatRowIsReleasedTask reports whether fields, the stat fields after comm,
// are what the kernel prints for a task whose signal state is gone: parent 0,
// group -1 and session -1, the defaults it starts from when it cannot take the
// signal lock. Any process exiting on the host can show this row mid-census.
// The state letter is read before that lock and can be stale, so it proves
// nothing. Flags are printed last, and PF_EXITING there means the task is
// leaving and cannot return to user code. It has left every group, so it is
// no group's live member.
func procStatRowIsReleasedTask(fields []string) bool {
	// Requiring a field after flags refuses a row cut off inside that number.
	if len(fields) < 8 || fields[1] != "0" || fields[2] != "-1" || fields[3] != "-1" {
		return false
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	return err == nil && flags&procTaskExiting != 0
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
	if !ok || group < -1 || len(fields) < 3 || len(fields[0]) != 1 {
		return invalid()
	}
	if group == -1 && !procStatRowIsReleasedTask(fields) {
		return invalid()
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return invalid()
	}
	// Unknown single-character states remain live, never proof of quiescence.
	return group, fields[0], nil
}
