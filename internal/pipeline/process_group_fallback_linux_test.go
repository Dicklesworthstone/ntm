package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Use real children and kernel failure injection. The seccomp filters apply
// only inside a subprocess, never to the test runner or a developer's shell.
func TestCommandGroupFallbackKernelFailures(t *testing.T) {
	for _, mode := range []string{"group-denied", "normal-signals-denied", "open-denied", "send-denied", "unowned", "reaped", "wait-cancel", "wait-deadline", "wait-observe", "wait-observe-only", "wait-observe-pipes", "wait-ownership"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCommandGroupFallbackHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "NTM_GROUP_FALLBACK_HELPER="+mode)
			configureCommandProcessGroup(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", mode, err, output)
			}
			if strings.Contains(string(output), "SECCOMP_UNAVAILABLE") {
				t.Skipf("kernel fault injection unavailable: %s", output)
			}
			if strings.HasPrefix(mode, "wait-") {
				t.Logf("%s", output)
			}
		})
	}
}

func TestCommandGroupFallbackHelper(t *testing.T) {
	mode := os.Getenv("NTM_GROUP_FALLBACK_HELPER")
	if mode == "" {
		return
	}
	// A filter attached by prctl applies to this thread. Keep all subsequent
	// signal operations on it. The process exits without unlocking the thread.
	runtime.LockOSThread()
	if strings.HasPrefix(mode, "wait-") {
		fallbackTestBoundedWait(t, mode)
		return
	}
	if mode == "unowned" {
		// The outer test gives this helper its own group. Without the ownership
		// guard this call SIGKILLs the helper, safely demonstrating the defect.
		self, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		defer self.Release()
		if err := cancelCommandProcessGroup(&exec.Cmd{Process: self}); err == nil {
			t.Fatal("unowned process number was accepted for group cancellation")
		}
		return
	}
	if mode == "normal-signals-denied" {
		cmd := exec.Command("sh", "-c", "printf done")
		configureCommandProcessGroup(cmd)
		capture := newCappedWriter(64)
		cmd.Stdout = capture
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer fallbackTestStopProcess(cmd)
		if err := fallbackTestInstallFilter("group-denied"); err != nil {
			fmt.Printf("SECCOMP_UNAVAILABLE: %v\n", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result := waitCommandWithProcessGroupCleanup(ctx, cmd)
		if !result.Settled || result.Cancelled || result.Err != nil || capture.String() != "done" || !capture.outputCaptureComplete() {
			t.Fatalf("normal completion depended on an unsolicited group signal: %+v output=%q", result, capture.String())
		}
		return
	}
	leader := fallbackTestStartProcess(t, 0)
	if mode == "reaped" {
		_ = leader.Process.Kill()
		_ = leader.Wait()
		if err := cancelCommandProcessGroup(leader); err == nil || errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("a reaped leader must be rejected before a numeric group signal: %v", err)
		}
		return
	}
	member := fallbackTestStartProcess(t, leader.Process.Pid)
	other := fallbackTestStartProcess(t, 0)
	defer fallbackTestStopProcess(other)
	defer fallbackTestStopProcess(member)
	defer fallbackTestStopProcess(leader)

	if err := fallbackTestInstallFilter(mode); err != nil {
		fmt.Printf("SECCOMP_UNAVAILABLE: %v\n", err)
		return
	}
	if mode == "group-denied" {
		err := cancelCommandProcessGroup(leader)
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("successful fallback erased the failed group signal: %v", err)
		}
		fallbackTestAwaitKilled(t, leader)
		fallbackTestAwaitKilled(t, member)
	} else {
		err := killCommandGroupMember(member.Process.Pid, leader.Process.Pid)
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("restricted pidfd operation did not report its failure: %v", err)
		}
		fallbackTestAssertAlive(t, member)
	}
	fallbackTestAssertAlive(t, other)
}

func fallbackTestAssertAlive(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	// Signal(0) itself uses pidfd_send_signal on recent Go releases, so it is
	// deliberately unavailable in the send-denied case. Waitid is unaffected
	// by our filter and distinguishes a live child from an unreaped zombie.
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil || info.Signo != 0 {
		t.Fatalf("child %d did not remain alive: signo=%d err=%v", cmd.Process.Pid, info.Signo, err)
	}
}

func fallbackTestStartProcess(t *testing.T, group int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fallbackTestStopProcess(cmd) })
	return cmd
}

func fallbackTestStopProcess(cmd *exec.Cmd) {
	if cmd.ProcessState != nil {
		return
	}
	// Only test-owned, unreaped direct children are signaled numerically.
	// This also works after the helper denies pidfd_send_signal to itself.
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func fallbackTestAwaitKilled(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	// Observe without reaping; a timeout must not start a competing Cmd.Wait.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var info unix.Siginfo
		if err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil {
			t.Fatal(err)
		}
		if info.Signo != 0 {
			err := cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("expected killed child, got %v", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("unexpected child exit: %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child %d survived fallback cleanup", cmd.Process.Pid)
}

func fallbackTestInstallFilter(mode string) error {
	number := uint32(unix.SYS_KILL)
	if mode == "open-denied" {
		number = unix.SYS_PIDFD_OPEN
	} else if mode == "send-denied" {
		number = unix.SYS_PIDFD_SEND_SIGNAL
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // seccomp_data.nr
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: number, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(syscall.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if mode == "group-denied" {
		// Deny kill(-pgid, signal), but permit pinned single-child cleanup.
		// Load the low word of the first syscall argument on either endian.
		var word uint16 = 1
		offset := uint32(16)
		if *(*byte)(unsafe.Pointer(&word)) == 0 {
			offset += 4
		}
		filter = []unix.SockFilter{
			{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
			{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: number, Jf: 3},
			{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset},
			{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x80000000, Jf: 1},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(syscall.EPERM)},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		}
	}
	if strings.HasPrefix(mode, "wait-") {
		// The command remains alive even after every shutdown signal fails.
		// Deny both os.Process's pidfd route and the numeric group route on
		// the cleanup owner's locked thread. The final reaper runs elsewhere
		// and never sends a signal. wait-observe also denies the initial probe;
		// wait-observe-only and wait-observe-pipes permit all signals so an
		// erroneous kill is real.
		numbers := []uint32{unix.SYS_KILL, unix.SYS_PIDFD_SEND_SIGNAL}
		if mode == "wait-observe" {
			numbers = append(numbers, unix.SYS_WAITID)
		} else if mode == "wait-observe-only" || mode == "wait-observe-pipes" {
			numbers = []uint32{unix.SYS_WAITID}
		}
		filter = []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
		for _, nr := range numbers {
			filter = append(filter,
				unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1},
				unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(syscall.EPERM)},
			)
		}
		filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
	runtime.KeepAlive(filter)
	return err
}

// Exercise the production waiter, not a substituted Wait or signal function.
// The 12-second child lifetime is an independent safety net for a regressed
// waiter: even the negative control cannot leave an indefinitely live child.
func fallbackTestBoundedWait(t *testing.T, mode string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "printf ready; exec sleep 12")
	var releasePath string
	if mode == "wait-observe-pipes" {
		// The leader exits naturally; its child writes only after the waiter
		// has returned. Closing its output on failed observation would lose
		// the late bytes or deliver SIGPIPE to otherwise healthy work.
		cmd = exec.Command("/bin/sh", "-c", `(
i=0
while [ ! -f release ] && [ "$i" -lt 1200 ]; do
  i=$((i+1)); sleep 0.01
done
printf after
) &
printf ready
`)
		cmd.Dir = t.TempDir()
		releasePath = filepath.Join(cmd.Dir, "release")
	}
	configureCommandProcessGroup(cmd)
	cmd.WaitDelay = 0 // The waiter must also bound callers without pipe setup.
	capture := newCappedWriter(64)
	cmd.Stdout, cmd.Stderr = capture, capture
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := cmd.Process
	waitStarted := false
	defer func() {
		if releasePath != "" {
			_ = os.WriteFile(releasePath, []byte("finish"), 0600)
		}
		// Leave the seccomp-restricted thread to clean up this exact process
		// handle. Never call Wait twice or read cmd.ProcessState concurrently
		// with a late reaper. Observe reaping and capture completion instead.
		stopped := make(chan struct{})
		go func() {
			_ = process.Kill()
			if !waitStarted {
				_ = cmd.Wait()
			}
			close(stopped)
		}()
		<-stopped
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_, err := os.Stat(fmt.Sprintf("/proc/%d", process.Pid))
			if errors.Is(err, os.ErrNotExist) && capture.outputCaptureComplete() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("final reaper did not release the child and finish its output capture")
	}()
	deadline := time.Now().Add(3 * time.Second)
	for capture.String() != "ready" {
		if time.Now().After(deadline) {
			t.Fatal("command did not publish its initial output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if mode == "wait-ownership" {
		// The OS group is isolated, but the supplied command lacks proof.
		cmd.SysProcAttr = nil
	}
	if err := fallbackTestInstallFilter(mode); err != nil {
		fmt.Printf("SECCOMP_UNAVAILABLE: %v\n", err)
		return
	}
	if mode == "wait-observe-only" || mode == "wait-observe-pipes" {
		// Prove that only observation is restricted. Either of the old
		// cleanup paths could kill this healthy process on the same thread.
		if err := process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("identity-stable signals unexpectedly denied: %v", err)
		}
		if err := syscall.Kill(-process.Pid, 0); err != nil {
			t.Fatalf("owned group signals unexpectedly denied: %v", err)
		}
	}
	ctx := context.Background()
	if mode == "wait-cancel" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	} else if mode == "wait-deadline" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
	}
	start := time.Now()
	waitStarted = true
	result := waitCommandWithProcessGroupCleanup(ctx, cmd)
	elapsed := time.Since(start)
	// Five seconds of grace, one second of settlement, two of final wait,
	// plus scheduling tolerance. The old code waits for the 12-second child.
	if elapsed > commandCancelGracePeriod+5*commandPipeDrainTimeout {
		t.Fatalf("cleanup exceeded its budget: elapsed=%v result=%+v", elapsed, result)
	}
	if result.Settled || !errors.Is(result.Err, syscall.EPERM) ||
		!strings.Contains(result.Err.Error(), "did not finish waiting after cleanup") {
		t.Fatalf("failed shutdown did not retain an unresolved outcome: %+v", result)
	}
	if result.Cancelled != (ctx.Err() != nil) || (ctx.Err() != nil && !errors.Is(result.Err, ctx.Err())) {
		t.Fatalf("lost cancellation or deadline cause: %+v context=%v", result, ctx.Err())
	}
	if mode == "wait-observe-pipes" {
		if capture.String() != "ready" || capture.outputCaptureComplete() {
			t.Fatal("failed observation closed the healthy descendant's inherited output")
		}
		if err := os.WriteFile(releasePath, []byte("finish"), 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for capture.String() != "readyafter" || !capture.outputCaptureComplete() {
			if time.Now().After(deadline) {
				t.Fatalf("healthy descendant lost late output: %q; EOF=%t", capture.String(), capture.outputCaptureComplete())
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Logf("%s returned unresolved in %v and retained the descendant's late output", mode, elapsed)
		return
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := parseCommandGroupStat(data, process.Pid)
	if err != nil || state == "Z" || state == "X" {
		t.Fatalf("%s did not keep the unresolved child alive: state=%q err=%v", mode, state, err)
	}
	if capture.String() != "ready" || capture.outputCaptureComplete() {
		t.Fatal("unresolved return lost partial output or claimed its still-open pipe had reached EOF")
	}
	t.Logf("%s returned unresolved in %v with child still alive and partial output retained", mode, elapsed)
}

func TestCommandGroupFallbackSweepsOnlyItsGroup(t *testing.T) {
	leader := fallbackTestStartProcess(t, 0)
	member := fallbackTestStartProcess(t, leader.Process.Pid)
	other := fallbackTestStartProcess(t, 0)
	if err := killProcessGroupDescendants(leader.Process.Pid); err != nil {
		t.Fatal(err)
	}
	fallbackTestAwaitKilled(t, member)
	for _, cmd := range []*exec.Cmd{leader, other} {
		fallbackTestAssertAlive(t, cmd)
	}
}

func TestCommandGroupFallbackRechecksMembershipAndClosesHandles(t *testing.T) {
	leader := fallbackTestStartProcess(t, 0)
	other := fallbackTestStartProcess(t, 0)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		// Model a stale census entry whose current process is in a different
		// group: obtaining its handle does not authorize the signal.
		if err := killCommandGroupMember(other.Process.Pid, leader.Process.Pid); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) != len(before) {
		t.Fatalf("membership refusal leaked process handles: before=%d after=%d err=%v", len(before), len(after), err)
	}
	fallbackTestAssertAlive(t, other)
}

func TestCommandGroupFallbackPreservesNormalLifecycle(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%t", cancelled), func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "(sleep 0.05; printf end) & printf begin")
			if cancelled {
				cmd = exec.Command("sleep", "60")
			}
			configureCommandProcessGroup(cmd)
			capture := newCappedWriter(64)
			cmd.Stdout = capture
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer fallbackTestStopProcess(cmd)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if cancelled {
				cancel()
			}
			result := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if !result.Settled || result.Cancelled != cancelled {
				t.Fatalf("normal lifecycle lost settlement or cancellation: %+v", result)
			}
			if cancelled {
				if !errors.Is(result.Err, context.Canceled) {
					t.Fatalf("cancellation cause was lost: %+v", result)
				}
			} else if result.Err != nil || capture.String() != "beginend" {
				t.Fatalf("background completion lost output: result=%+v output=%q", result, capture.String())
			}
		})
	}
}

const commandGroupTestMountInfo = "10 1 0:2 / /proc rw,nosuid,nodev,noexec - proc proc rw\n"

func commandGroupTestSelfStatus() string {
	return fmt.Sprintf("Name:\tntm\nNSpid:\t%d\n", os.Getpid())
}

func commandGroupTestStat(pid int, state string, group int, threads string) string {
	fields := make([]string, 19)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1], fields[2], fields[17] = state, "1", strconv.Itoa(group), threads
	return fmt.Sprintf("%d (worker) %s", pid, strings.Join(fields, " "))
}

func TestCommandGroupCensusNarrowsAndValidatesEvidence(t *testing.T) {
	type process struct {
		group    int
		groupErr error
		stat     string
		statErr  error
		read     bool
	}
	leader := process{group: 42, stat: commandGroupTestStat(42, "Z", 42, "1"), read: true}
	for _, tc := range []struct {
		name      string
		processes map[int]process
		dirErr    error
		alive     bool
		causes    []error
		message   string
	}{
		{
			name: "unrelated malformed and unreadable rows are never read",
			processes: map[int]process{
				42: leader,
				43: {group: 99, stat: "not a process record"},
				44: {group: 99, statErr: syscall.EACCES},
			},
		},
		{
			name: "live member survives unrelated malformed row",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) S 1 42", read: true},
				44: {group: 99, stat: "bad"},
			},
			alive: true,
		},
		{
			name: "unknown member state stays live",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) ? 1 42", read: true},
			},
			alive: true,
		},
		{
			name: "zombie and dead members cannot execute",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "1"), read: true},
				44: {group: 42, stat: commandGroupTestStat(44, "X", 42, "1"), read: true},
			},
		},
		{
			name: "zombie thread leader can have executing threads",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "2"), read: true},
			},
			alive: true,
		},
		{
			name: "dead thread leader does not prove other threads stopped",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "X", 42, "2"), read: true},
			},
			alive: true,
		},
		{
			name: "zombie without thread count cannot settle",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) Z 1 42", read: true},
			},
			message: "missing thread-count evidence",
		},
		{
			name: "thread count cut off within its number cannot settle",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: strings.TrimSuffix(commandGroupTestStat(43, "Z", 42, "1"), " 0"), read: true},
			},
			message: "missing thread-count evidence",
		},
		{
			name: "zero thread count cannot settle",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "0"), read: true},
			},
			message: "malformed thread-count evidence",
		},
		{
			name: "negative thread count cannot settle",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "-1"), read: true},
			},
			message: "malformed thread-count evidence",
		},
		{
			name: "malformed thread count cannot settle",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "unknown"), read: true},
			},
			message: "malformed thread-count evidence",
		},
		{
			name: "denied kernel lookup falls back to strict leader and unrelated rows",
			processes: map[int]process{
				42: {groupErr: syscall.EPERM, stat: commandGroupTestStat(42, "Z", 42, "1"), read: true},
				43: {groupErr: syscall.EPERM, stat: "43 (other) S 1 99", read: true},
			},
		},
		{
			name: "denied kernel lookup falls back to live member evidence",
			processes: map[int]process{
				42: leader,
				43: {groupErr: syscall.EPERM, stat: "43 (worker) S 1 42", read: true},
			},
			alive: true,
		},
		{
			name: "unknown membership without valid stat refuses quiet census",
			processes: map[int]process{
				42: leader,
				43: {groupErr: syscall.EPERM, read: true},
			},
			causes:  []error{syscall.EPERM},
			message: "malformed process-group evidence",
		},
		{
			name: "unknown membership is not masked by a live member",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) S 1 42", read: true},
				44: {groupErr: syscall.EPERM, read: true},
			},
			causes: []error{syscall.EPERM},
		},
		{
			name: "unknown membership and unreadable stat retain both failures",
			processes: map[int]process{
				42: leader,
				43: {groupErr: syscall.EPERM, statErr: syscall.EIO, read: true},
			},
			causes:  []error{syscall.EPERM, syscall.EIO},
			message: "inspect process 43",
		},
		{
			name: "unknown membership and missing stat remain uncertain",
			processes: map[int]process{
				42: leader,
				43: {groupErr: syscall.EPERM, statErr: os.ErrNotExist, read: true},
			},
			causes: []error{syscall.EPERM, os.ErrNotExist},
		},
		{
			name: "target stat permission failure remains uncertain",
			processes: map[int]process{
				42: leader,
				43: {group: 42, statErr: syscall.EACCES, read: true},
			},
			causes: []error{syscall.EACCES},
		},
		{
			name: "target malformed stat remains uncertain",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "bad", read: true},
			},
			message: "malformed process-group evidence",
		},
		{
			name: "target mismatched identity remains uncertain",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "99 (worker) Z 1 42", read: true},
			},
			message: "malformed process-group evidence",
		},
		{
			name: "member vanishes before kernel lookup",
			processes: map[int]process{
				42: leader,
				43: {groupErr: syscall.ESRCH},
			},
		},
		{
			name: "member vanishes before stat open",
			processes: map[int]process{
				42: leader,
				43: {group: 42, statErr: os.ErrNotExist, read: true},
			},
		},
		{
			name: "member vanishes during stat read",
			processes: map[int]process{
				42: leader,
				43: {group: 42, statErr: syscall.ESRCH, read: true},
			},
		},
		{
			name: "member moves after kernel lookup",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) S 1 99", read: true},
			},
		},
		{
			name: "member released after kernel lookup",
			processes: map[int]process{
				42: leader,
				43: {group: 42, stat: "43 (worker) R 0 -1 -1 0 -1 4 0", read: true},
			},
		},
		{
			name: "leader kernel group mismatch",
			processes: map[int]process{
				42: {group: 99, stat: "42 (leader) Z 1 42"},
			},
			message: "leader changed process group",
		},
		{
			name: "leader stat group mismatch",
			processes: map[int]process{
				42: {group: 42, stat: commandGroupTestStat(42, "Z", 99, "1"), read: true},
			},
			message: "leader changed process group",
		},
		{
			name: "leader absent from directory",
			processes: map[int]process{
				43: {group: 42, stat: commandGroupTestStat(43, "Z", 42, "1"), read: true},
			},
			message: "unreaped leader missing",
		},
		{
			name: "leader vanishes before kernel lookup",
			processes: map[int]process{
				42: {groupErr: syscall.ESRCH},
			},
			message: "unreaped leader missing",
		},
		{
			name: "leader vanishes before stat read",
			processes: map[int]process{
				42: {group: 42, statErr: os.ErrNotExist, read: true},
			},
			message: "unreaped leader missing",
		},
		{
			name:   "directory enumeration failure",
			dirErr: syscall.EIO,
			causes: []error{syscall.EIO},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := fstest.MapFS{
				"self/stat": {Data: []byte("not a numbered process directory")},
				"uptime":    {Data: []byte("not a process directory")},
			}
			for pid, process := range tc.processes {
				proc[strconv.Itoa(pid)+"/stat"] = &fstest.MapFile{Data: []byte(process.stat)}
			}
			reads := make(map[int]int)
			inspector := commandGroupInspector{
				readDir: func(path string) ([]os.DirEntry, error) {
					if path != "/proc" {
						t.Fatalf("unexpected directory %q", path)
					}
					if tc.dirErr != nil {
						return nil, tc.dirErr
					}
					return proc.ReadDir(".")
				},
				getpgid: func(pid int) (int, error) {
					process, ok := tc.processes[pid]
					if !ok {
						t.Fatalf("looked up a non-process entry %d", pid)
					}
					return process.group, process.groupErr
				},
				readFile: func(path string) ([]byte, error) {
					if path == "/proc/self/mountinfo" {
						return []byte(commandGroupTestMountInfo), nil
					}
					if path == "/proc/self/status" {
						return []byte(commandGroupTestSelfStatus()), nil
					}
					if path == "/proc/stat" {
						return []byte("cpu 1 2 3\nprocesses 123\nprocs_running 2\n"), nil
					}
					path = strings.TrimPrefix(path, "/proc/")
					name, _, _ := strings.Cut(path, "/")
					pid, err := strconv.Atoi(name)
					if err != nil {
						t.Fatalf("unexpected stat path %q", path)
					}
					reads[pid]++
					if err := tc.processes[pid].statErr; err != nil {
						return nil, err
					}
					return proc.ReadFile(path)
				},
			}
			alive, err := inspectCommandGroup(42, inspector)
			if alive != tc.alive {
				t.Errorf("alive=%t, want %t; error=%v", alive, tc.alive, err)
			}
			if len(tc.causes) == 0 && tc.message == "" {
				if err != nil {
					t.Errorf("valid group evidence was rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("incomplete group evidence authorized quiescence")
			}
			for _, cause := range tc.causes {
				if !errors.Is(err, cause) {
					t.Errorf("lost observation cause: got %v, want %v", err, cause)
				}
			}
			if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Errorf("got %v, want diagnostic %q", err, tc.message)
			}
			for pid, process := range tc.processes {
				want := 0
				if process.read {
					want = 1
				}
				if reads[pid] != want {
					t.Errorf("read stat for PID %d %d times, want %d", pid, reads[pid], want)
				}
			}
		})
	}
}

func TestCommandGroupCensusRequiresStableBirthEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		before    string
		after     string
		readErrAt int
		live      bool
		wantReads int
		wantCause error
		wantError bool
	}{
		{name: "unchanged", before: "processes 123\n", after: "processes 123\n", wantReads: 2},
		{name: "fork during census", before: "processes 123\n", after: "processes 124\n", wantReads: 2, wantCause: errCommandGroupBirthRace, wantError: true},
		{name: "counter decreased", before: "processes 123\n", after: "processes 122\n", wantReads: 2, wantCause: errCommandGroupBirthRace, wantError: true},
		{name: "initial read denied", readErrAt: 1, wantReads: 1, wantCause: syscall.EACCES, wantError: true},
		{name: "final read denied", before: "processes 123\n", readErrAt: 2, wantReads: 2, wantCause: syscall.EACCES, wantError: true},
		{name: "initial row missing", before: "procs_running 1\n", wantReads: 1, wantError: true},
		{name: "final row missing", before: "processes 123\n", after: "procs_running 1\n", wantReads: 2, wantError: true},
		{name: "malformed count", before: "processes unknown\n", wantReads: 1, wantError: true},
		{name: "count cut off before record terminator", before: "processes 12", wantReads: 1, wantError: true},
		{name: "negative count", before: "processes -1\n", wantReads: 1, wantError: true},
		{name: "overflowing count", before: "processes 18446744073709551616\n", wantReads: 1, wantError: true},
		{name: "duplicate count", before: "processes 123\nprocesses 123\n", wantReads: 1, wantError: true},
		{name: "extra count field", before: "processes 123 124\n", wantReads: 1, wantError: true},
		{name: "positive liveness needs no ending counter", before: "processes 123\n", after: "malformed", live: true, wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := fstest.MapFS{
				"42/stat": {Data: []byte(commandGroupTestStat(42, "Z", 42, "1"))},
			}
			if tc.live {
				proc["43/stat"] = &fstest.MapFile{Data: []byte(commandGroupTestStat(43, "S", 42, "1"))}
			}
			reads := 0
			inspector := commandGroupInspector{
				readDir: func(path string) ([]os.DirEntry, error) {
					if path != "/proc" {
						t.Fatalf("unexpected directory %q", path)
					}
					return proc.ReadDir(".")
				},
				getpgid: func(int) (int, error) { return 42, nil },
				readFile: func(path string) ([]byte, error) {
					if path == "/proc/self/mountinfo" {
						return []byte(commandGroupTestMountInfo), nil
					}
					if path == "/proc/self/status" {
						return []byte(commandGroupTestSelfStatus()), nil
					}
					if path != "/proc/stat" {
						return proc.ReadFile(strings.TrimPrefix(path, "/proc/"))
					}
					reads++
					if reads == tc.readErrAt {
						return nil, syscall.EACCES
					}
					if reads == 1 {
						return []byte(tc.before), nil
					}
					return []byte(tc.after), nil
				},
			}
			alive, err := inspectCommandGroup(42, inspector)
			if alive != tc.live || (err != nil) != tc.wantError || reads != tc.wantReads {
				t.Fatalf("alive=%t error=%v reads=%d; want alive=%t error=%t reads=%d", alive, err, reads, tc.live, tc.wantError, tc.wantReads)
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Fatalf("lost counter failure cause: %v", err)
			}
		})
	}
}

func TestCommandGroupCensusRequiresCompleteProcfsVisibility(t *testing.T) {
	withOption := func(option string) string {
		return strings.TrimSuffix(commandGroupTestMountInfo, "\n") + "," + option + "\n"
	}
	for _, tc := range []struct {
		name           string
		before         string
		after          string
		readErrAt      int
		wantMountReads int
		wantDirReads   int
		wantError      bool
		wantCause      error
	}{
		{name: "ordinary procfs", before: commandGroupTestMountInfo, wantMountReads: 2, wantDirReads: 1},
		{name: "hidepid zero", before: withOption("hidepid=0"), wantMountReads: 2, wantDirReads: 1},
		{name: "hidepid off", before: withOption("hidepid=off"), wantMountReads: 2, wantDirReads: 1},
		{name: "hidepid one retains directory enumeration", before: withOption("hidepid=1"), wantMountReads: 2, wantDirReads: 1},
		{name: "hidepid noaccess retains directory enumeration", before: withOption("hidepid=noaccess"), wantMountReads: 2, wantDirReads: 1},
		{name: "unrelated hidden mount does not change proc visibility", before: "20 1 0:3 / /other rw - proc proc rw,hidepid=2\n" + commandGroupTestMountInfo, wantMountReads: 2, wantDirReads: 1},
		{name: "invisible numeric", before: withOption("hidepid=2"), wantMountReads: 1, wantError: true},
		{name: "invisible named", before: withOption("hidepid=invisible"), wantMountReads: 1, wantError: true},
		{name: "ptraceable numeric", before: withOption("hidepid=4"), wantMountReads: 1, wantError: true},
		{name: "ptraceable named", before: withOption("hidepid=ptraceable"), wantMountReads: 1, wantError: true},
		{name: "hidden mount-level option", before: strings.Replace(commandGroupTestMountInfo, "rw,nosuid", "rw,hidepid=2,nosuid", 1), wantMountReads: 1, wantError: true},
		{name: "unknown visibility value", before: withOption("hidepid=3"), wantMountReads: 1, wantError: true},
		{name: "unknown visibility option", before: withOption("hidepid-future=0"), wantMountReads: 1, wantError: true},
		{name: "visibility value missing", before: withOption("hidepid"), wantMountReads: 1, wantError: true},
		{name: "duplicate visibility values", before: withOption("hidepid=0,hidepid=1"), wantMountReads: 1, wantError: true},
		{name: "missing proc mount", before: "1 1 0:1 / / rw - ext4 root rw\n", wantMountReads: 1, wantError: true},
		{name: "non-proc filesystem", before: strings.Replace(commandGroupTestMountInfo, "- proc ", "- tmpfs ", 1), wantMountReads: 1, wantError: true},
		{name: "partial proc subtree", before: strings.Replace(commandGroupTestMountInfo, " / /proc ", " /123 /proc ", 1), wantMountReads: 1, wantError: true},
		{name: "overlapping proc mounts", before: commandGroupTestMountInfo + withOption("hidepid=2"), wantMountReads: 1, wantError: true},
		{name: "invalid mount identity", before: strings.Replace(commandGroupTestMountInfo, "10 1", "none 1", 1), wantMountReads: 1, wantError: true},
		{name: "invalid mount device", before: strings.Replace(commandGroupTestMountInfo, "0:2", "invalid", 1), wantMountReads: 1, wantError: true},
		{name: "missing mount separator", before: strings.Replace(commandGroupTestMountInfo, " - ", " ", 1), wantMountReads: 1, wantError: true},
		{name: "truncated selected record", before: strings.TrimSuffix(commandGroupTestMountInfo, "\n"), wantMountReads: 1, wantError: true},
		{name: "truncated mountinfo fields", before: "10 1 0:2\n", wantMountReads: 1, wantError: true},
		{name: "initial mountinfo denied", before: commandGroupTestMountInfo, readErrAt: 1, wantMountReads: 1, wantError: true, wantCause: syscall.EACCES},
		{name: "final mountinfo denied", before: commandGroupTestMountInfo, readErrAt: 2, wantMountReads: 2, wantDirReads: 1, wantError: true, wantCause: syscall.EACCES},
		{name: "restricted then unrestricted refuses initial enumeration", before: withOption("hidepid=2"), after: commandGroupTestMountInfo, wantMountReads: 1, wantError: true},
		{name: "unrestricted then restricted refuses quiet", before: commandGroupTestMountInfo, after: withOption("hidepid=2"), wantMountReads: 2, wantDirReads: 1, wantError: true},
		{name: "complete-visible remount remains safe", before: withOption("hidepid=1"), after: withOption("hidepid=0"), wantMountReads: 2, wantDirReads: 1},
		{name: "mount identity replaced during census", before: commandGroupTestMountInfo, after: strings.Replace(commandGroupTestMountInfo, "10 1", "11 1", 1), wantMountReads: 2, wantDirReads: 1, wantError: true, wantCause: errCommandGroupMountRace},
		{name: "mount device changed during census", before: commandGroupTestMountInfo, after: strings.Replace(commandGroupTestMountInfo, "0:2", "0:3", 1), wantMountReads: 2, wantDirReads: 1, wantError: true, wantCause: errCommandGroupMountRace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := fstest.MapFS{
				"42/stat": {Data: []byte(commandGroupTestStat(42, "Z", 42, "1"))},
			}
			mountReads, dirReads := 0, 0
			inspector := commandGroupInspector{
				readDir: func(path string) ([]os.DirEntry, error) {
					dirReads++
					if path != "/proc" {
						t.Fatalf("unexpected directory %q", path)
					}
					return proc.ReadDir(".")
				},
				getpgid: func(int) (int, error) { return 42, nil },
				readFile: func(path string) ([]byte, error) {
					switch path {
					case "/proc/self/status":
						return []byte(commandGroupTestSelfStatus()), nil
					case "/proc/self/mountinfo":
						mountReads++
						if mountReads == tc.readErrAt {
							return nil, syscall.EACCES
						}
						if mountReads > 1 && tc.after != "" {
							return []byte(tc.after), nil
						}
						return []byte(tc.before), nil
					case "/proc/stat":
						return []byte("processes 123\n"), nil
					default:
						return proc.ReadFile(strings.TrimPrefix(path, "/proc/"))
					}
				},
			}
			alive, err := inspectCommandGroup(42, inspector)
			if alive || (err != nil) != tc.wantError || mountReads != tc.wantMountReads || dirReads != tc.wantDirReads {
				t.Fatalf("alive=%t error=%v mount_reads=%d dir_reads=%d; want error=%t mount_reads=%d dir_reads=%d", alive, err, mountReads, dirReads, tc.wantError, tc.wantMountReads, tc.wantDirReads)
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Fatalf("lost visibility failure cause: %v", err)
			}
		})
	}
}

func TestCommandGroupCensusRequiresMatchingPIDNamespace(t *testing.T) {
	valid := commandGroupTestSelfStatus()
	pid := strconv.Itoa(os.Getpid())
	otherPID := strconv.Itoa(os.Getpid() + 1)
	for _, tc := range []struct {
		name    string
		status  string
		readErr error
		valid   bool
	}{
		{name: "matching namespace", status: valid, valid: true},
		{name: "namespace evidence missing"},
		{name: "matching numeric Pid alone is insufficient", status: "Pid:\t" + pid + "\n"},
		{name: "namespace value missing", status: "NSpid:\n"},
		{name: "zero PID", status: "NSpid:\t0\n"},
		{name: "negative PID", status: "NSpid:\t-1\n"},
		{name: "signed PID", status: "NSpid:\t+" + pid + "\n"},
		{name: "nondecimal PID", status: "NSpid:\t0x2a\n"},
		{name: "unknown PID", status: "NSpid:\tunknown\n"},
		{name: "overflowing PID", status: "NSpid:\t18446744073709551616\n"},
		{name: "mismatched PID", status: "NSpid:\t" + otherPID + "\n"},
		{name: "nested namespace", status: "NSpid:\t" + otherPID + "\t" + pid + "\n"},
		{name: "nested namespace with equal numeric IDs", status: "NSpid:\t" + pid + "\t" + pid + "\n"},
		{name: "extra namespace token", status: "NSpid:\t" + pid + "\tunknown\n"},
		{name: "duplicate namespace row", status: valid + "NSpid:\t" + pid + "\n"},
		{name: "truncated namespace row", status: "NSpid:\t" + pid},
		{name: "status read denied", readErr: syscall.EACCES},
	} {
		observations := []int{1, 2}
		if tc.valid {
			observations = []int{0}
		}
		for _, failAt := range observations {
			t.Run(fmt.Sprintf("%s/observation_%d", tc.name, failAt), func(t *testing.T) {
				// An unrelated ancestor-namespace zombie may have the same
				// numeric PID and group as the locally owned leader. Even this
				// apparently quiet record cannot prove settlement without a
				// matching procfs namespace before and after the census.
				proc := fstest.MapFS{
					"42/stat": {Data: []byte(commandGroupTestStat(42, "Z", 42, "1"))},
				}
				statusReads, dirReads := 0, 0
				inspector := commandGroupInspector{
					readDir: func(path string) ([]os.DirEntry, error) {
						dirReads++
						if path != "/proc" {
							t.Fatalf("unexpected directory %q", path)
						}
						return proc.ReadDir(".")
					},
					getpgid: func(int) (int, error) { return 42, nil },
					readFile: func(path string) ([]byte, error) {
						switch path {
						case "/proc/self/mountinfo":
							return []byte(commandGroupTestMountInfo), nil
						case "/proc/self/status":
							statusReads++
							if statusReads == failAt {
								return []byte(tc.status), tc.readErr
							}
							return []byte(valid), nil
						case "/proc/stat":
							return []byte("processes 123\n"), nil
						default:
							return proc.ReadFile(strings.TrimPrefix(path, "/proc/"))
						}
					},
				}
				alive, err := inspectCommandGroup(42, inspector)
				wantStatusReads, wantDirReads := failAt, failAt-1
				if tc.valid {
					wantStatusReads, wantDirReads = 2, 1
				}
				if alive || (err == nil) != tc.valid || statusReads != wantStatusReads || dirReads != wantDirReads {
					t.Fatalf("alive=%t error=%v status_reads=%d dir_reads=%d; want valid=%t status_reads=%d dir_reads=%d", alive, err, statusReads, dirReads, tc.valid, wantStatusReads, wantDirReads)
				}
				if err != nil && !strings.Contains(err.Error(), "namespace") {
					t.Fatalf("missing namespace diagnostic: %v", err)
				}
				if tc.readErr != nil && !errors.Is(err, tc.readErr) {
					t.Fatalf("lost status read error: %v", err)
				}
			})
		}
	}
}

func TestCommandLeaderObservationRetriesPreserveHealthyWork(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("probe_%d", failAt), func(t *testing.T) {
			dir := t.TempDir()
			cmd := startGroupTestCommand(t, dir, "sleep 0.15; printf complete > effect")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			calls, failures := 0, 0
			ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
				calls++
				if calls >= failAt && failures < 2 {
					failures++
					return syscall.EIO
				}
				return observeCommandExit(pid, syscall.WNOHANG)
			})
			result := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if result.Err != nil || result.Cancelled || !result.Settled || failures != 2 {
				t.Fatalf("transient leader observation abandoned healthy work: %+v failures=%d", result, failures)
			}
			if data, err := os.ReadFile(filepath.Join(dir, "effect")); err != nil || string(data) != "complete" {
				t.Fatalf("healthy command did not complete its effect: %q %v", data, err)
			}
		})
	}
}

func TestCommandLeaderObservationRetriesRetainGroupCancellation(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("probe_%d", failAt), func(t *testing.T) {
			dir := t.TempDir()
			groupTestFile(t, dir, "child.sh", `trap 'printf handled > handled; exit 0' TERM
printf '%s' "$$" > child.pid
i=0
while [ "$i" -lt 12 ]; do i=$((i+1)); sleep 1; done
`)
			cmd := startGroupTestCommand(t, dir, `trap 'exit 0' TERM; /bin/sh child.sh >/dev/null 2>&1 & wait`)
			child, err := os.FindProcess(waitForPIDFile(t, filepath.Join(dir, "child.pid")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			calls, failures := 0, 0
			ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
				calls++
				if calls >= failAt && failures < 2 {
					failures++
					// Cancellation arrives while waitability is unknown. A
					// recovering probe must retain the whole group's lifecycle.
					cancel()
					return syscall.EIO
				}
				return observeCommandExit(pid, syscall.WNOHANG)
			})
			result := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if !result.Cancelled || !result.Settled || !errors.Is(result.Err, context.Canceled) ||
				result.SignalSent != "SIGTERM,SIGKILL" || failures != 2 {
				t.Fatalf("leader observation retry surrendered group cancellation: %+v failures=%d", result, failures)
			}
			if data, err := os.ReadFile(filepath.Join(dir, "handled")); err != nil || string(data) != "handled" {
				t.Fatalf("redirected descendant did not receive graceful cancellation: %q %v", data, err)
			}
		})
	}
}

func TestCommandLeaderObservationFailureCannotAuthorizeGroupCancellation(t *testing.T) {
	dir := t.TempDir()
	groupTestFile(t, dir, "child.sh", `printf '%s' "$$" > child.pid
exec sleep 12
`)
	cmd := startGroupTestCommand(t, dir, `/bin/sh child.sh >/dev/null 2>&1 & wait`)
	childPID := waitForPIDFile(t, filepath.Join(dir, "child.pid"))
	child, err := os.FindProcess(childPID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	calls := 0
	ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
		calls++
		if calls == 1 {
			return observeCommandExit(pid, syscall.WNOHANG)
		}
		cancel()
		return errors.Join(context.Canceled, syscall.EIO)
	})
	started := time.Now()
	result := waitCommandWithProcessGroupCleanup(ctx, cmd)
	if !result.Cancelled || result.Settled || result.SignalSent != "" ||
		!errors.Is(result.Err, context.Canceled) || !errors.Is(result.Err, syscall.EIO) {
		t.Fatalf("failed ownership proof entered numeric group cancellation: %+v", result)
	}
	if elapsed := time.Since(started); elapsed > commandLeaderObservationRetryTimeout+2*commandPipeDrainTimeout {
		t.Fatalf("failed ownership observation exceeded its bound: %v", elapsed)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPID))
	if err != nil {
		t.Fatalf("uncertain cleanup signaled a descendant: %v", err)
	}
	group, state, err := parseCommandGroupStat(data, childPID)
	if err != nil || group != cmd.Process.Pid || state == "Z" || state == "X" {
		t.Fatalf("unproven group identity was used to stop a child: group=%d state=%q err=%v", group, state, err)
	}
}

func TestCommandLeaderLostOwnershipCannotSignalOnCancellation(t *testing.T) {
	for _, cause := range []error{syscall.ECHILD, syscall.ESRCH} {
		for _, failAt := range []int{1, 2} {
			t.Run(fmt.Sprintf("%v/probe_%d", cause, failAt), func(t *testing.T) {
				dir := t.TempDir()
				input, release, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = input.Close(); _ = release.Close() })
				cmd := exec.Command("/bin/sh", "-c", `trap 'printf signalled > signalled; exit 17' TERM
printf ready > ready
read release
printf natural > complete
`)
				cmd.Dir, cmd.Stdin = dir, input
				configureCommandProcessGroup(cmd)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				handedToWait := false
				t.Cleanup(func() {
					_ = release.Close()
					_ = cmd.Process.Kill()
					if !handedToWait {
						_ = cmd.Wait()
					}
				})
				awaitGroupTestFile(t, filepath.Join(dir, "ready"))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				calls := 0
				ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
					calls++
					if calls != failAt {
						// A later successful probe must never recover authority
						// after definite loss: the PID could have been reused.
						return observeCommandExit(pid, syscall.WNOHANG)
					}
					cancel()
					return errors.Join(context.Canceled, cause)
				})
				handedToWait = true
				started := time.Now()
				result := waitCommandWithProcessGroupCleanup(ctx, cmd)
				if !result.Cancelled || result.Settled || result.SignalSent != "" ||
					!errors.Is(result.Err, context.Canceled) || !errors.Is(result.Err, cause) {
					t.Fatalf("lost identity authorized cancellation or settlement: %+v", result)
				}
				if calls != failAt {
					t.Fatalf("retried a definitively lost leader identity: calls=%d want=%d", calls, failAt)
				}
				if elapsed := time.Since(started); elapsed > 2*commandPipeDrainTimeout+time.Second {
					t.Fatalf("lost ownership exceeded its final reaper bound: %v", elapsed)
				}
				// Keep the command blocked until cleanup has returned. This
				// deterministically exposes even a direct Process.Kill fallback,
				// without needing to recycle a real system PID in the test.
				fallbackTestAssertAlive(t, cmd)
				if _, err := os.Stat(filepath.Join(dir, "signalled")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("lost ownership sent SIGTERM to the live command: %v", err)
				}
				if _, err := release.WriteString("finish\n"); err != nil {
					t.Fatal(err)
				}
				if data := awaitGroupTestFile(t, filepath.Join(dir, "complete")); string(data) != "natural" {
					t.Fatalf("unresolved command did not finish naturally: %q", data)
				}
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					if errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("final reaper did not collect the naturally completed command")
			})
		}
	}
}
