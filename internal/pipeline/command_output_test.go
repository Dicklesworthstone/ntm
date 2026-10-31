//go:build !windows

package pipeline

import (
	"bytes"
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
				beforeStart: func(commandOutputRequest) ([]byte, bool, error) {
					beforeCalls++
					if phase == "before_start" {
						return nil, false, injected
					}
					if phase == "cancel_after_intent" {
						cancel()
					}
					return nil, false, nil
				},
				afterWait: func(observed *exec.Cmd, settled bool, _ []byte, err error) error {
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
					afterWait: func(_ *exec.Cmd, proof bool, _ []byte, err error) error {
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

func TestCommandOutputReusesDurableSuccessBytesAfterReload(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty_%t", empty), func(t *testing.T) {
			e := newCommandTestExecutor(t)
			script := `printf x >> selections; printf '\377\000\300tail'`
			want := []byte{0xff, 0, 0xc0, 't', 'a', 'i', 'l'}
			if empty {
				script, want = `printf x >> selections`, nil
			}
			step := &Step{ID: "source", Branch: "$(" + script + ")"}
			command := func() *exec.Cmd {
				cmd := exec.Command("/bin/sh", "-c", script)
				cmd.Dir = e.config.ProjectDir
				cmd.Env = []string{"SOURCE_VALUE=original", "DUP=discarded", "DUP=effective"}
				return cmd
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := runCommandOutput(e.commandEvaluationContext(ctx, step), command(), 64, 64)
			if err != nil || !bytes.Equal(out, want) {
				t.Fatalf("initial complete output: %x %v", out, err)
			}
			prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			record := prior.CommandEvaluations[step.ID]
			if record.Status != commandExecutionSettled || !bytes.Equal(record.Output, want) || validateCommandEvaluation(step.ID, record) != nil {
				t.Fatalf("saved success lost exact bytes or proof: %+v output=%x", record, record.Output)
			}
			if len(want) != 0 {
				snapshot := e.snapshotState()
				snapshot.CommandEvaluations[step.ID].Output[0] = 'z'
				if !bytes.Equal(e.state.CommandEvaluations[step.ID].Output, want) {
					t.Fatal("snapshot output aliases the live evaluation receipt")
				}
			}
			fresh := NewExecutor(e.config)
			fresh.state = prior
			cmd := command()
			// Equivalent effective environments have the same identity despite
			// order changes or a duplicate variable that exec would discard.
			cmd.Env = []string{"DUP=effective", "SOURCE_VALUE=original"}
			out, err = runCommandOutput(fresh.commandEvaluationContext(ctx, step), cmd, 64, 64)
			if err != nil || !bytes.Equal(out, want) || cmd.Process != nil {
				t.Fatalf("reload reran or altered successful output: %x %v process=%v", out, err, cmd.Process)
			}
			if len(out) != 0 {
				out[0] = 'z'
				if !bytes.Equal(fresh.state.CommandEvaluations[step.ID].Output, want) {
					t.Fatal("returned output aliases its durable receipt")
				}
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "selections")); err != nil || string(data) != "x" {
				t.Fatalf("successful evaluation effects were repeated: %q %v", data, err)
			}
		})
	}
}

func TestCommandOutputInvocationFreezePreservesEffectiveEnvironment(t *testing.T) {
	t.Setenv("PWD", "/inherited/logical/directory")
	cmd := exec.Command("/bin/sh", "-c", "true")
	before := cmd.Environ()
	request := commandOutputRequest{Cmd: cmd, StdoutLimit: 64, StderrLimit: 64}
	digest, err := commandEvaluationInvocationHash(request)
	if err != nil || !filepath.IsAbs(cmd.Dir) || strings.Join(cmd.Env, "\x00") != strings.Join(before, "\x00") {
		t.Fatalf("freezing an implicit cwd changed the effective launch environment: dir=%q env=%q err=%v", cmd.Dir, cmd.Env, err)
	}
	if again, err := commandEvaluationInvocationHash(request); err != nil || again != digest {
		t.Fatalf("freezing a command twice changed its identity: %q %q %v", digest, again, err)
	}
}

func TestCommandOutputRejectsChangedDurableInvocation(t *testing.T) {
	for _, change := range []string{"expanded command", "executable", "directory", "environment", "definition", "stdout limit", "stderr limit", "invalid UTF-8 argument"} {
		t.Run(change, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			script := "printf x >> selections; printf original"
			step := &Step{ID: "source", Branch: "${vars.expanded_selector}"}
			command := func() *exec.Cmd {
				cmd := exec.Command("/bin/sh", "-c", script, "source", string([]byte{0xff}))
				cmd.Dir, cmd.Env = e.config.ProjectDir, []string{"SOURCE_VALUE=original"}
				return cmd
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := runCommandOutput(e.commandEvaluationContext(ctx, step), command(), 64, 64); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, e.state.RunID))
			if err != nil {
				t.Fatal(err)
			}
			fresh := NewExecutor(e.config)
			fresh.state = prior
			cmd := command()
			stdoutLimit, stderrLimit := int64(64), int64(64)
			switch change {
			case "expanded command":
				cmd.Args[2] += "; printf changed"
			case "executable":
				cmd.Path = "/bin/false"
			case "directory":
				cmd.Dir = t.TempDir()
			case "environment":
				cmd.Env = []string{"SOURCE_VALUE=changed"}
			case "definition":
				step.Branch = "${vars.other_selector}"
			case "stdout limit":
				stdoutLimit++
			case "stderr limit":
				stderrLimit++
			case "invalid UTF-8 argument":
				cmd.Args[len(cmd.Args)-1] = string([]byte{0xfe})
			}
			out, err := runCommandOutput(fresh.commandEvaluationContext(ctx, step), cmd, stdoutLimit, stderrLimit)
			if out != nil || err == nil || !strings.Contains(err.Error(), "expanded invocation changed") || cmd.Process != nil {
				t.Fatalf("changed invocation reused output or launched work: %q %v process=%v", out, err, cmd.Process)
			}
			after, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, e.state.RunID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected invocation changed its saved evidence: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "selections")); err != nil || string(data) != "x" {
				t.Fatalf("identity rejection repeated source effects: %q %v", data, err)
			}
		})
	}
}

func TestCommandOutputLostSuccessCheckpointKeepsIntentUnresolved(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := &Step{ID: "source", Branch: "$(printf x >> selections; printf complete)"}
	ctx := e.commandEvaluationContext(context.Background(), step)
	lifecycle := ctx.Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
	injected := errors.New("owner stopped before success checkpoint")
	lifecycle.afterWait = func(_ *exec.Cmd, settled bool, output []byte, err error) error {
		if !settled || err != nil || string(output) != "complete" {
			t.Fatalf("fixture did not reach the completed-command crash boundary: settled=%t output=%q err=%v", settled, output, err)
		}
		return injected
	}
	cmd := exec.Command("/bin/sh", "-c", "printf x >> selections; printf complete")
	cmd.Dir = e.config.ProjectDir
	if out, err := runCommandOutput(ctx, cmd, 64, 64); out != nil || !errors.Is(err, injected) {
		t.Fatalf("lost checkpoint published authoritative output: %q %v", out, err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if prior.CommandEvaluations[step.ID].Status != commandExecutionStarting {
		t.Fatalf("lost success checkpoint erased launch intent: %+v", prior.CommandEvaluations)
	}
	fresh := NewExecutor(e.config)
	fresh.state = prior
	if err := fresh.applyResumeOptions(&Workflow{Steps: []Step{*step}}, ResumeOptions{}); !errors.Is(err, errCommandOutputUnsettled) {
		t.Fatalf("missing completion proof authorized replay: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "selections")); err != nil || string(data) != "x" {
		t.Fatalf("refused resume repeated effects: %q %v", data, err)
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
