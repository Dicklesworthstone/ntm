//go:build linux || darwin

package pipeline

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecuteCommand_CancelKillsProcessGroupChild(t *testing.T) {
	e := newCommandTestExecutor(t)
	pidFile := e.config.ProjectDir + "/child.pid"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	step := &Step{
		ID:      "process-group-cancel",
		Command: "sleep 30 & echo $! > " + strconv.Quote(pidFile) + "; wait",
		Timeout: Duration{Duration: 10 * time.Second},
	}

	resultCh := make(chan StepResult, 1)
	go func() {
		resultCh <- e.executeCommand(ctx, step, &Workflow{Name: "test"})
	}()

	childPID := waitForPIDFile(t, pidFile)
	if !processExists(childPID) {
		t.Fatalf("child process %d was not running before cancellation", childPID)
	}

	cancel()

	select {
	case result := <-resultCh:
		if result.Status != StatusCancelled {
			t.Fatalf("Status = %s, want cancelled; error=%+v", result.Status, result.Error)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("executeCommand did not return within cancellation grace period")
	}

	waitUntil(t, 2*time.Second, func() bool {
		return !processExists(childPID)
	}, "child process still exists after process-group cancellation")
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child PID file %s", path)
	return 0
}

func waitUntil(t *testing.T, timeout time.Duration, ok func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errorsIsPermission(err)
}

func errorsIsPermission(err error) bool {
	return err == syscall.EPERM
}

// TestParseProcStatPGID covers bd-ob92m: the descendant-sweep fallback in
// signalCommandProcessGroup parses /proc/<pid>/stat to discover which
// processes share the leader's pgid. The comm field is wrapped in
// parentheses and can itself contain spaces or parentheses, so we must
// scan back to the LAST `)` before splitting fields.
func TestParseProcStatPGID(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want int
		ok   bool
	}{
		{
			name: "simple",
			stat: "1234 (sleep) S 1 1234 1234 0 -1 4194304 ...",
			want: 1234,
			ok:   true,
		},
		{
			name: "comm with spaces and parens",
			stat: "5678 (my (weird) program) R 1 9999 9999 0 ...",
			want: 9999,
			ok:   true,
		},
		{
			name: "missing close paren",
			stat: "1234 (sleep S 1 1234 1234",
			ok:   false,
		},
		{
			name: "too few fields after comm",
			stat: "1 (init) S",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcStatPGID([]byte(tc.stat))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("pgid = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestKillProcessGroupDescendantsBestEffort verifies the helper does not
// panic on bogus inputs and does not target pid 1 / the leader itself.
// We can't easily simulate a real "kill -pgid failed" sequence in a
// portable test, but the helper must be safe to call when /proc is
// missing (early return) and when leaderPID matches no one.
func TestKillProcessGroupDescendantsBestEffort(t *testing.T) {
	// Should be a silent no-op rather than panicking.
	killProcessGroupDescendants(0)
	killProcessGroupDescendants(-5)
	// Use a pgid that very likely matches no live process group.
	killProcessGroupDescendants(2147483646)
}
