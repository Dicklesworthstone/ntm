//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func groupTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func awaitGroupTestFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process did not publish %s", path)
	return nil
}

func startGroupTestCommand(t *testing.T, dir, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only the child group allocated by this test is signaled. Normal
		// successful cleanup has already reaped the direct process.
		if cmd.ProcessState == nil {
			_ = cancelCommandProcessGroup(cmd)
			_ = cmd.Wait()
		}
	})
	return cmd
}

func TestCommandCancellationSettlesRedirectedDescendant(t *testing.T) {
	dir := t.TempDir()
	groupTestFile(t, dir, "child.sh", `trap '' TERM
printf '%s' "$$" > child.pid
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`)
	cmd := startGroupTestCommand(t, dir, `trap 'exit 0' TERM
/bin/sh child.sh </dev/null >/dev/null 2>&1 &
wait
`)
	childPID, err := strconv.Atoi(string(awaitGroupTestFile(t, filepath.Join(dir, "child.pid"))))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(childPID)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Kill()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := waitCommandWithProcessGroupCleanup(ctx, cmd)
	if !outcome.Cancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("cancellation outcome: %+v", outcome)
	}
	groupTestFile(t, dir, "release", "continue")
	// The child can produce its side effect only AFTER cleanup returned.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "late-effect")); err == nil {
			t.Fatal("cancelled command descendant performed work after cleanup returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandWaitRetainsLeaderUntilBackgroundWorkFinishes(t *testing.T) {
	dir := t.TempDir()
	groupTestFile(t, dir, "child.sh", `printf ready > child.ready
while [ ! -f release ]; do sleep 0.01; done
printf complete
`)
	cmd := exec.Command("/bin/sh", "-c", `/bin/sh child.sh &`)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan commandCleanupResult, 1)
	go func() { done <- waitCommandWithProcessGroupCleanup(ctx, cmd) }()
	defer func() { groupTestFile(t, dir, "release", "finish") }()
	awaitGroupTestFile(t, filepath.Join(dir, "child.ready"))
	// The leader must remain waitable while its child is still doing work.
	deadline := time.Now().Add(time.Second)
	pinned := false
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		if err == nil && strings.Contains(string(data), ") Z ") {
			pinned = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	groupTestFile(t, dir, "release", "finish")
	result := <-done
	if !pinned {
		t.Error("leader was reaped while a descendant was still running")
	}
	if result.Err != nil || result.Cancelled || output.String() != "complete" {
		t.Fatalf("natural descendant completion: %+v output=%q", result, output.String())
	}
}

func TestCommandCancellationAfterLeaderExitStillOwnsDescendants(t *testing.T) {
	dir := t.TempDir()
	groupTestFile(t, dir, "child.sh", `printf ready > child.ready
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`)
	cmd := startGroupTestCommand(t, dir, `/bin/sh child.sh >/dev/null 2>&1 &`)
	awaitGroupTestFile(t, filepath.Join(dir, "child.ready"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan commandCleanupResult, 1)
	go func() { done <- waitCommandWithProcessGroupCleanup(ctx, cmd) }()
	// No cancellation yet: redirected descendants still belong to this run.
	select {
	case result := <-done:
		groupTestFile(t, dir, "release", "finish")
		t.Fatalf("command surrendered ownership of its live descendant: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	result := <-done
	if !result.Cancelled || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("cancel after leader exit: %+v", result)
	}
	groupTestFile(t, dir, "release", "finish")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled descendant performed work: %v", err)
	}
}

func TestCommandCleanupPreservesExitStatusAndOutput(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("printf before; (sleep 0.05; printf after) & wait; exit %d", code))
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if result.Cancelled || output.String() != "beforeafter" {
				t.Fatalf("changed normal output/status: %+v %q", result, output.String())
			}
			if code == 0 && result.Err != nil {
				t.Fatal(result.Err)
			}
			if code != 0 {
				var exit *exec.ExitError
				if !errors.As(result.Err, &exit) || exit.ExitCode() != code {
					t.Fatalf("lost exit code %d: %v", code, result.Err)
				}
			}
		})
	}
}

func TestCommandCancellationAllowsCooperativeDescendantCleanup(t *testing.T) {
	dir := t.TempDir()
	groupTestFile(t, dir, "child.sh", `trap 'printf handled > handled; exit 0' TERM
printf ready > child.ready
while :; do sleep 1; done
`)
	cmd := startGroupTestCommand(t, dir, `trap 'exit 0' TERM; /bin/sh child.sh >/dev/null 2>&1 & wait`)
	awaitGroupTestFile(t, filepath.Join(dir, "child.ready"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := waitCommandWithProcessGroupCleanup(ctx, cmd)
	if !result.Cancelled || !errors.Is(result.Err, context.Canceled) || result.SignalSent != "SIGTERM" {
		t.Fatalf("cooperative cancellation: %+v", result)
	}
	if string(awaitGroupTestFile(t, filepath.Join(dir, "handled"))) != "handled" {
		t.Fatal("descendant was killed before its graceful cleanup")
	}
}

func TestCommandCleanupDoesNotSignalOtherGroups(t *testing.T) {
	dir := t.TempDir()
	other := startGroupTestCommand(t, dir, "sleep 30")
	cmd := startGroupTestCommand(t, dir, "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := waitCommandWithProcessGroupCleanup(ctx, cmd)
	if !result.Cancelled {
		t.Fatalf("expected cancellation: %+v", result)
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("cancelled a process from another group: %v", err)
	}
	_ = cancelCommandProcessGroup(other)
	_ = other.Wait()
}

func TestCommandCleanupBoundsEscapedInheritedOutput(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is required for the escaped-descriptor fixture")
	}
	dir := t.TempDir()
	groupTestFile(t, dir, "escaped.sh", `printf '%s' "$$" > escaped.pid
while [ ! -f release ]; do sleep 0.01; done
`)
	cmd := exec.Command("/bin/sh", "-c", `"$1" /bin/sh escaped.sh & while [ ! -s escaped.pid ]; do sleep 0.01; done; printf done`, "fixture", setsid)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	configureCommandProcessGroup(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(awaitGroupTestFile(t, filepath.Join(dir, "escaped.pid"))))
	if err != nil {
		t.Fatal(err)
	}
	escaped, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer escaped.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	result := waitCommandWithProcessGroupCleanup(ctx, cmd)
	if !errors.Is(result.Err, exec.ErrWaitDelay) || result.Cancelled || time.Since(start) > time.Second {
		t.Fatalf("escaped descriptor was not bounded: %+v elapsed=%v", result, time.Since(start))
	}
	if output.String() != "done" {
		t.Fatalf("lost leader output: %q", output.String())
	}
}

func TestCommandCleanupRejectsUnstartedAndReapedCommands(t *testing.T) {
	for _, cmd := range []*exec.Cmd{nil, exec.Command("/bin/sh", "-c", "exit 0")} {
		if result := waitCommandWithProcessGroupCleanup(context.Background(), cmd); result.Err == nil {
			t.Fatal("invalid command was accepted")
		}
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	configureCommandProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if result := waitCommandWithProcessGroupCleanup(context.Background(), cmd); result.Err == nil {
		t.Fatal("already reaped command accepted for numeric group signaling")
	}
}
