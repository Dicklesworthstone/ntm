//go:build !windows

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCommandOutputPreservesCompleteStdoutAndExitDiagnostics(t *testing.T) {
	for _, script := range []string{"printf 12345; printf abc >&2", "printf 12345; printf abc >&2; exit 7"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.Command("/bin/sh", "-c", script)
		out, err := runCommandOutput(ctx, cmd, 5, 3)
		cancel()
		if strings.HasSuffix(script, "exit 7") {
			var exit *exec.ExitError
			if out != nil || !errors.As(err, &exit) || exit.ExitCode() != 7 || string(exit.Stderr) != "abc" || errors.Is(err, errCommandOutputUnsettled) {
				t.Fatalf("lost failure diagnostics or exposed partial stdout: %q %v", out, err)
			}
		} else if err != nil || string(out) != "12345" {
			t.Fatalf("exact output limits rejected: %q %v", out, err)
		}
	}
}

func TestCommandOutputRejectsOverflowInsteadOfReturningPrefix(t *testing.T) {
	for name, script := range map[string]string{
		"stdout": "printf 123456",
		"stderr": "printf useful; printf abcdef >&2",
		"both":   "printf 123456; printf abcdef >&2",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := runCommandOutput(ctx, exec.Command("/bin/sh", "-c", script), 5, 5)
			if out != nil || !errors.Is(err, errCommandOutputLimit) {
				t.Fatalf("accepted truncated %s: %q %v", name, out, err)
			}
			if ctx.Err() != nil {
				t.Fatal("output limit cancelled the caller's context")
			}
		})
	}
}

func TestCommandOutputOverflowStopsUnboundedProducer(t *testing.T) {
	for _, redirect := range []string{"", ">&2"} {
		t.Run(redirect, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.Command("/bin/sh", "-c", "while :; do printf 012345678901234567890123456789 "+redirect+"; done")
			start := time.Now()
			out, err := runCommandOutput(ctx, cmd, 1024, 1024)
			if out != nil || !errors.Is(err, errCommandOutputLimit) || time.Since(start) > 2*time.Second {
				t.Fatalf("unbounded producer was not stopped by output limit: %q %v elapsed=%v", out, err, time.Since(start))
			}
			if cmd.ProcessState == nil {
				t.Fatal("output overflow returned before reaping producer")
			}
		})
	}
}

func TestCommandOutputPreCancelledStartsNoWork(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "printf dispatched > marker")
	cmd.Dir = root
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runCommandOutput(ctx, cmd, 100, 100)
	if out != nil || !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("pre-cancelled capture started work: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled command dispatched: %v", err)
	}
}

func TestCommandOutputDeadlineReturnsNoPartialWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	out, err := runCommandOutput(ctx, exec.Command("/bin/sh", "-c", "printf partial; sleep 30"), 100, 100)
	if out != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out selector exposed partial output: %q %v", out, err)
	}
}

func TestCommandOutputRejectsInvalidSetup(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		if _, err := runCommandOutput(context.Background(), cmd, limit, 100); err == nil || cmd.Process != nil {
			t.Fatal("unbounded capture started a command")
		}
	}
	if _, err := runCommandOutput(context.Background(), nil, 100, 100); err == nil {
		t.Fatal("nil command accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil || cmd.Process != nil {
		t.Fatal("competing context watcher accepted")
	}
	cmd = exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil {
		t.Fatal("reused command accepted")
	}
	cmd = exec.Command(filepath.Join(t.TempDir(), "missing"))
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestCancelOnTruncationWriterRetainsBoundedBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := newCappedWriter(4)
	writer := &cancelOnTruncationWriter{capture: capture, cancel: cancel}
	if n, err := writer.Write([]byte("1234")); n != 4 || err != nil || ctx.Err() != nil {
		t.Fatal("exact cap treated as overflow")
	}
	if n, err := writer.Write([]byte(strings.Repeat("x", 10000))); n != 10000 || err != nil {
		t.Fatal("overflow writer stopped pipe drainage")
	}
	if capture.Len() != 4 || capture.Total() != 10004 || capture.String() != "1234" || ctx.Err() == nil {
		t.Fatalf("unbounded bytes or missing cancellation: kept=%d total=%d context=%v", capture.Len(), capture.Total(), ctx.Err())
	}
}

func TestCancelOnTruncationWriterTracksStreamFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &cancelOnTruncationWriter{capture: newCappedWriter(4), cancel: cancel}
	reader, pipe := io.Pipe()
	defer reader.Close()
	defer pipe.Close()
	done := make(chan error, 1)
	go func() {
		_, err := writer.ReadFrom(reader)
		done <- err
	}()
	if _, err := pipe.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ReaderFrom bypassed cancellation on truncation")
	}
	if writer.outputCaptureComplete() {
		t.Fatal("truncation was mistaken for EOF while the source pipe remained open")
	}
	if err := pipe.CloseWithError(io.ErrUnexpectedEOF); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("wrapper lost the output read failure: %v", err)
	}
	if _, err := writer.ReadFrom(strings.NewReader("later")); err != nil {
		t.Fatal(err)
	}
	if writer.outputCaptureComplete() || writer.capture.String() != "part" || writer.capture.Total() != 12 {
		t.Fatalf("later EOF erased incomplete capture evidence: complete=%t output=%q total=%d",
			writer.outputCaptureComplete(), writer.capture.String(), writer.capture.Total())
	}
}

func TestCommandOutputLifecycleGatesDispatchAndPublication(t *testing.T) {
	for _, phase := range []string{"before_start", "cancel_after_intent", "start_failure", "after_wait", "cancel_after_wait"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.Command("/bin/sh", "-c", "printf ran > marker; printf good")
			cmd.Dir = root
			if phase == "start_failure" {
				cmd = exec.Command(filepath.Join(root, "missing"))
			}
			injected := errors.New("injected selector checkpoint failure")
			beforeCalls, afterCalls := 0, 0
			var observedErr error
			observedSettled := false
			ctx = context.WithValue(ctx, commandOutputLifecycleKey{}, &commandOutputLifecycle{
				beforeStart: func() error {
					beforeCalls++
					if phase == "before_start" {
						return injected
					}
					if phase == "cancel_after_intent" {
						cancel()
					}
					return nil
				},
				afterWait: func(observed *exec.Cmd, settled bool, err error) error {
					afterCalls++
					observedSettled, observedErr = settled, err
					if observed != cmd {
						t.Error("completion callback changed command identity")
					}
					if phase == "after_wait" {
						return injected
					}
					if phase == "cancel_after_wait" {
						cancel()
					}
					return nil
				},
			})
			out, err := runCommandOutput(ctx, cmd, 64, 64)
			if out != nil || err == nil || beforeCalls != 1 {
				t.Fatalf("failed lifecycle admitted command output: %q %v before=%d", out, err, beforeCalls)
			}
			if phase == "before_start" {
				if afterCalls != 0 || !errors.Is(err, injected) {
					t.Fatalf("failed launch intent continued into completion: after=%d err=%v", afterCalls, err)
				}
			} else if afterCalls != 1 || !observedSettled {
				t.Fatalf("lost no-process or completed-command proof: after=%d settled=%t error=%v", afterCalls, observedSettled, observedErr)
			}
			if phase == "after_wait" || phase == "cancel_after_wait" {
				wantErr := injected
				if phase == "cancel_after_wait" {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || observedErr != nil || cmd.ProcessState == nil {
					t.Fatalf("settlement persistence failure replaced the real process outcome: %v observed=%v", err, observedErr)
				}
				if data, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(data) != "ran" {
					t.Fatalf("fixture command never performed its effect: %q %v", data, err)
				}
			} else {
				if cmd.Process != nil {
					t.Fatal("failed prelaunch boundary started a process")
				}
				if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed prelaunch boundary performed work: %v", err)
				}
				if phase == "cancel_after_intent" && (!errors.Is(err, context.Canceled) || !errors.Is(observedErr, context.Canceled)) {
					t.Fatalf("cancellation during checkpoint was lost: %v observed=%v", err, observedErr)
				}
				if phase == "start_failure" && observedErr == nil {
					t.Fatal("start failure was not delivered with its no-process proof")
				}
			}
		})
	}
}

func TestCommandOutputRejectsEscapedInheritedPipes(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, exitCode := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit_%d", stream, exitCode), func(t *testing.T) {
				root := t.TempDir()
				script, release := escapedCommandOutputFixture(t, root, stream, exitCode)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				observed, settled := false, true
				var observedErr error
				ctx = context.WithValue(ctx, commandOutputLifecycleKey{}, &commandOutputLifecycle{
					afterWait: func(_ *exec.Cmd, proof bool, err error) error {
						observed, settled, observedErr = true, proof, err
						return nil
					},
				})
				cmd := exec.Command("/bin/sh", "-c", script)
				cmd.Dir, cmd.WaitDelay = root, 80*time.Millisecond
				out, err := runCommandOutput(ctx, cmd, 64, 64)
				if out != nil || !errors.Is(err, errCommandOutputUnsettled) || ctx.Err() != nil ||
					!strings.Contains(err.Error(), stream+" output capture did not reach EOF") {
					t.Fatalf("escaped %s was admitted as authoritative output: %q %v", stream, out, err)
				}
				if !observed || settled || !errors.Is(observedErr, errCommandOutputUnsettled) {
					t.Fatalf("completion callback was given unsafe settlement authority: observed=%t settled=%t err=%v", observed, settled, observedErr)
				}
				if exitCode != 0 {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != exitCode || string(exit.Stderr) != "diagnostic" {
						t.Fatalf("uncertainty erased the original exit diagnostics: %v", err)
					}
				}
				release()
				if data, err := os.ReadFile(filepath.Join(root, "late-effect")); err != nil || string(data) != "late" {
					t.Fatalf("fixture failed to demonstrate work surviving capture: %q %v", data, err)
				}
			})
		}
	}
}

// The detached child retains exactly one output stream and can produce a
// side effect only after capture has returned. The release callback also owns
// fixture cleanup, so assertion failures never leave a perpetual file waiter.
func escapedCommandOutputFixture(t *testing.T, root, stream string, exitCode int) (string, func()) {
	t.Helper()
	return escapedCommandOutputFixtureWithOutput(t, root, stream, exitCode, "good")
}

func escapedCommandOutputFixtureWithOutput(t *testing.T, root, stream string, exitCode int, output string) (string, func()) {
	t.Helper()
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is required for escaped output fixtures")
	}
	child := `case "$1" in
stdout) exec 2>/dev/null ;;
stderr) exec 1>/dev/null ;;
esac
printf '%s' "$$" > escaped.pid
attempts=0
while [ ! -f release ] && [ "$attempts" -lt 1000 ]; do
  sleep 0.01
  attempts=$((attempts + 1))
done
if [ -f release ]; then printf late > late-effect; fi
printf done > escaped.done
`
	if err := os.WriteFile(filepath.Join(root, "escaped.sh"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		var process *os.Process
		if data, err := os.ReadFile(filepath.Join(root, "escaped.pid")); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				process, _ = os.FindProcess(pid)
			}
		}
		if process != nil {
			defer process.Release()
		}
		if err := os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600); err != nil {
			t.Errorf("release escaped fixture: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for process != nil && time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(root, "escaped.done")); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if process != nil {
			_ = process.Kill()
			t.Error("escaped fixture did not finish after release")
		}
	}
	t.Cleanup(release)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := fmt.Sprintf(`printf 'launch\n' >> selector.launches
%s /bin/sh escaped.sh %s &
while [ ! -s escaped.pid ]; do sleep 0.01; done
printf '%%s' %s
printf diagnostic >&2
exit %d`, quote(setsid), quote(stream), quote(output), exitCode)
	return script, release
}
