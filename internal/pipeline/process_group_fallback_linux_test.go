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
	for _, mode := range []string{"group-denied", "settlement-denied", "open-denied", "send-denied", "unowned", "reaped", "wait-cancel", "wait-deadline", "wait-observe", "wait-observe-only", "wait-observe-pipes", "wait-ownership"} {
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
	if mode == "settlement-denied" {
		cmd := exec.Command("sh", "-c", "printf done")
		configureCommandProcessGroup(cmd)
		cmd.Stdout = newCappedWriter(64)
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
		if result.Settled || result.Cancelled || !errors.Is(result.Err, syscall.EPERM) {
			t.Fatalf("failed group cleanup authorized a settled launch receipt: %+v", result)
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

func TestCommandGroupCensusNarrowsAndValidatesEvidence(t *testing.T) {
	type process struct {
		group    int
		groupErr error
		stat     string
		statErr  error
		read     bool
	}
	leader := process{group: 42, stat: "42 (leader) Z 1 42", read: true}
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
				43: {group: 42, stat: "43 (worker) Z 1 42", read: true},
				44: {group: 42, stat: "44 (worker) X 1 42", read: true},
			},
		},
		{
			name: "denied kernel lookup falls back to strict leader and unrelated rows",
			processes: map[int]process{
				42: {groupErr: syscall.EPERM, stat: "42 (leader) Z 1 42", read: true},
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
				42: {group: 42, stat: "42 (leader) Z 1 99", read: true},
			},
			message: "leader changed process group",
		},
		{
			name: "leader absent from directory",
			processes: map[int]process{
				43: {group: 42, stat: "43 (worker) Z 1 42", read: true},
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
