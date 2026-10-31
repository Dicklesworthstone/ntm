//go:build linux

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
	if !result.Cancelled || !errors.Is(result.Err, context.Canceled) || result.SignalSent != "SIGTERM,SIGKILL" || !result.Settled {
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

// The shell's exit status does not establish that inherited output descriptors
// have closed. In particular, Cmd.Wait reports ExitError instead of ErrWaitDelay
// when BOTH the command exits nonzero and its pipe-drain deadline expires.
// Exercise the same capped captures used by executeCommand, then allow the
// escaped child to produce a real side effect only AFTER cleanup returns.
func TestCommandCleanupRefusesSettlementWithEscapedOutput(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is required for the escaped-descriptor fixture")
	}
	for _, stream := range []string{"merged", "stderr"} {
		for _, code := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit_%d", stream, code), func(t *testing.T) {
				dir := t.TempDir()
				childScript := `printf '%s' "$$" > escaped.pid
while [ ! -f release ]; do sleep 0.01; done
printf late-effect > effect
`
				if stream == "stderr" {
					childScript = "exec 1>/dev/null\n" + childScript
				}
				groupTestFile(t, dir, "escaped.sh", childScript)
				cmd := exec.Command("/bin/sh", "-c", `"$1" /bin/sh escaped.sh &
while [ ! -s escaped.pid ]; do sleep 0.01; done
printf done
exit "$2"
`, "fixture", setsid, strconv.Itoa(code))
				cmd.Dir = dir
				stdout := newCappedWriter(2)
				cmd.Stdout, cmd.Stderr = stdout, stdout
				if stream == "stderr" {
					cmd.Stderr = newCappedWriter(2)
				}
				configureCommandProcessGroup(cmd)
				cmd.WaitDelay = 80 * time.Millisecond
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					groupTestFile(t, dir, "release", "finish")
					if cmd.ProcessState == nil {
						_ = cancelCommandProcessGroup(cmd)
						_ = cmd.Wait()
					}
				}()
				pid, err := strconv.Atoi(string(awaitGroupTestFile(t, filepath.Join(dir, "escaped.pid"))))
				if err != nil {
					t.Fatal(err)
				}
				escaped, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				defer escaped.Kill()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				outcome := waitCommandWithProcessGroupCleanup(ctx, cmd)

				groupTestFile(t, dir, "release", "finish")
				if got := string(awaitGroupTestFile(t, filepath.Join(dir, "effect"))); got != "late-effect" {
					t.Fatalf("escaped child did not perform the post-cleanup effect: %q", got)
				}
				if outcome.Settled {
					t.Errorf("certified replay-safe settlement while escaped child could still act: %+v", outcome)
				}
				if outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "output capture did not reach EOF") {
					t.Errorf("missing capture-uncertainty error: %+v", outcome)
				}
				if outcome.Cancelled || stdout.String() != "do" || stdout.Total() != 4 || !stdout.Truncated() {
					t.Errorf("changed capture/cancellation contract: %+v output=%q total=%d", outcome, stdout.String(), stdout.Total())
				}
				if code != 0 {
					var exit *exec.ExitError
					if !errors.As(outcome.Err, &exit) || exit.ExitCode() != code {
						t.Errorf("lost exit code %d: %v", code, outcome.Err)
					}
				}
			})
		}
	}
}

// WaitNone only confirms the launch synchronously. Its final cleanup must
// still prevent a false workflow success when an escaped child holds a pipe
// open and can perform work after the executor returns.
func TestWaitNoneUnsettledCleanupReachesWorkflowOutcome(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is required for the escaped-descriptor fixture")
	}
	quotedSetsid := "'" + strings.ReplaceAll(setsid, "'", "'\\''") + "'"
	for _, mode := range []string{"run", "resume", "primary_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultExecutorConfig("wait-none-unsettled")
			cfg.ProjectDir, cfg.RunID = dir, "wait-none-unsettled-"+mode
			e := NewExecutor(cfg)
			groupTestFile(t, dir, "escaped.sh", `printf '%s' "$$" > "$1.pid"
while [ ! -f release ]; do sleep 0.01; done
printf late-effect > "$1.effect"
`)
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "wait-none-unsettled", Settings: DefaultWorkflowSettings(),
			}
			// Two workers exercise aggregation without allowing completion
			// order to replace an earlier sidecar failure.
			for _, id := range []string{"zeta", "alpha"} {
				workflow.Steps = append(workflow.Steps, Step{
					ID: id, Wait: WaitNone,
					Command: fmt.Sprintf(`printf 'once\n' >> %s.launches
if [ -f repair ]; then exit 0; fi
%s /bin/sh escaped.sh %s &
while [ ! -s %s.pid ]; do sleep 0.01; done
printf started
`, id, quotedSetsid, id, id),
				})
			}
			finish := `while [ ! -s alpha.pid ] || [ ! -s zeta.pid ]; do sleep 0.01; done
printf ready > pipeline.ready
`
			if mode == "cancelled" {
				finish += "sleep 30"
			} else if mode == "primary_failure" {
				finish += "exit 7"
			} else {
				finish += "printf finished"
			}
			workflow.Steps = append(workflow.Steps, Step{ID: "finish", Command: finish, DependsOn: []string{"alpha", "zeta"}})

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			events := make(chan ProgressEvent, 100)
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			var escaped []*os.Process
			t.Cleanup(func() {
				cancel()
				_ = os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600)
				for _, child := range escaped {
					_ = child.Kill()
					_ = child.Release()
				}
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Error("workflow did not join background cleanup")
				}
			})
			go func() {
				var state *ExecutionState
				var runErr error
				if mode == "resume" {
					prior := &ExecutionState{
						RunID: cfg.RunID, WorkflowID: workflow.Name, Session: cfg.Session,
						Status: StatusFailed, StartedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute),
					}
					state, runErr = e.Resume(ctx, workflow, prior, events)
				} else {
					state, runErr = e.Run(ctx, workflow, nil, events)
				}
				done <- outcome{state, runErr}
				close(done)
			}()
			awaitGroupTestFile(t, filepath.Join(dir, "pipeline.ready"))
			for _, id := range []string{"alpha", "zeta"} {
				pid, parseErr := strconv.Atoi(string(awaitGroupTestFile(t, filepath.Join(dir, id+".pid"))))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				child, findErr := os.FindProcess(pid)
				if findErr != nil {
					t.Fatal(findErr)
				}
				escaped = append(escaped, child)
			}
			if mode == "cancelled" {
				cancel()
			}
			var result outcome
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("unsettled background cleanup did not return within its bound")
			}
			wantStatus := StatusFailed
			if mode == "cancelled" {
				wantStatus = StatusCancelled
				if !errors.Is(result.err, context.Canceled) {
					t.Fatalf("cleanup failure masked caller cancellation: %v", result.err)
				}
			}
			if result.state == nil || result.state.Status != wantStatus || result.err == nil {
				t.Fatalf("unsettled background command reported the wrong outcome: %+v %v", result.state, result.err)
			}
			message := result.err.Error()
			alpha, zeta := strings.Index(message, `background command "alpha"`), strings.Index(message, `background command "zeta"`)
			if alpha < 0 || zeta < alpha || !strings.Contains(message, "output capture did not reach EOF") || !strings.Contains(message, "unresolved launch") {
				t.Fatalf("background cleanup causes were lost or reordered: %v", result.err)
			}
			if mode == "primary_failure" && !strings.Contains(message, "exit_code=7") {
				t.Fatalf("cleanup failure masked the main step failure: %v", result.err)
			}
			assertNoCompletionEvent(t, events)
			saved, loadErr := LoadState(dir, cfg.RunID)
			if loadErr != nil || saved.Status != wantStatus {
				t.Fatalf("terminal outcome was not persisted: %+v %v", saved, loadErr)
			}
			for _, id := range []string{"alpha", "zeta"} {
				receipt, ok := saved.CommandExecutions[id]
				if !ok || receipt.Status != commandExecutionRunning || !receipt.Background || !receipt.SettledAt.IsZero() {
					t.Fatalf("unsettled %s receipt was replaced with replay permission: %+v", id, receipt)
				}
			}
			persistedCause := false
			for _, diagnostic := range saved.Errors {
				if diagnostic.Fatal && strings.Contains(diagnostic.Message, "background command") && strings.Contains(diagnostic.Message, "unresolved launch") {
					persistedCause = true
				}
			}
			if !persistedCause {
				t.Fatalf("public status lost the background cleanup cause: %+v", saved.Errors)
			}
			if _, resumeErr := NewExecutor(cfg).Resume(context.Background(), workflow, saved, nil); resumeErr == nil || !strings.Contains(resumeErr.Error(), "unresolved launch") {
				t.Fatalf("ordinary resume replayed uncertain background work: %v", resumeErr)
			}
			for _, id := range []string{"alpha", "zeta"} {
				if got := string(awaitGroupTestFile(t, filepath.Join(dir, id+".launches"))); got != "once\n" {
					t.Fatalf("rejected resume repeated %s: %q", id, got)
				}
			}
			// The original children really can act after the failed workflow
			// returns; no error or receipt may claim that they were stopped.
			groupTestFile(t, dir, "release", "finish")
			for _, id := range []string{"alpha", "zeta"} {
				if got := string(awaitGroupTestFile(t, filepath.Join(dir, id+".effect"))); got != "late-effect" {
					t.Fatalf("escaped %s child did not perform its post-return effect: %q", id, got)
				}
			}
			if mode == "run" {
				e.config.RunID += "-reused"
				clean := &Workflow{SchemaVersion: SchemaVersion, Name: "clean", Steps: []Step{{ID: "work", Command: "printf recovered"}}}
				next, nextErr := e.Run(context.Background(), clean, nil, nil)
				if nextErr != nil || next.Status != StatusCompleted {
					t.Fatalf("new Run inherited the prior background failure: %+v %v", next, nextErr)
				}
			} else if mode == "resume" {
				groupTestFile(t, dir, "repair", "do not start another escaped child")
				e.config.ResumeOptions = ResumeOptions{Mode: ResumeModeRestartFailed}
				next, nextErr := e.Resume(context.Background(), workflow, saved, nil)
				if nextErr != nil || next.Status != StatusCompleted {
					t.Fatalf("explicit repaired Resume inherited the prior background failure: %+v %v", next, nextErr)
				}
			}
		})
	}
}

func TestCommandCleanupSettlesCompleteCappedOutput(t *testing.T) {
	for _, code := range []int{0, 7} {
		for _, merged := range []bool{false, true} {
			for _, limit := range []int64{0, 3} {
				t.Run(fmt.Sprintf("exit_%d/merged_%t/cap_%d", code, merged, limit), func(t *testing.T) {
					stdout, stderr := newCappedWriter(limit), newCappedWriter(limit)
					cmd := exec.Command("/bin/sh", "-c", `printf stdout; printf stderr >&2; exit "$1"`, "fixture", strconv.Itoa(code))
					cmd.Stdout, cmd.Stderr = stdout, stderr
					if merged {
						stderr = stdout
						cmd.Stderr = stderr
					}
					configureCommandProcessGroup(cmd)
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					outcome := waitCommandWithProcessGroupCleanup(ctx, cmd)
					if !outcome.Settled || outcome.Cancelled {
						t.Fatalf("complete captures must settle, even after truncation or exit failure: %+v", outcome)
					}
					if !stdout.outputCaptureComplete() || !stderr.outputCaptureComplete() {
						t.Fatal("Cmd.Wait did not leave complete EOF evidence")
					}
					wantTotal := int64(6)
					if merged {
						wantTotal = 12
					}
					if stdout.Total() != wantTotal || stderr.Truncated() != (limit > 0) {
						t.Fatalf("capture counts changed: stdout=%d stderr truncated=%t", stdout.Total(), stderr.Truncated())
					}
					if code == 0 && outcome.Err != nil {
						t.Fatal(outcome.Err)
					}
					if code != 0 {
						var exit *exec.ExitError
						if !errors.As(outcome.Err, &exit) || exit.ExitCode() != code {
							t.Fatalf("lost command exit: %v", outcome.Err)
						}
					}
				})
			}
		}
	}
}

func TestCappedCaptureRetainsIncompleteReadEvidence(t *testing.T) {
	writer := newCappedWriter(3)
	reader, pipe := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		_, err := writer.ReadFrom(reader)
		finished <- err
	}()
	if _, err := pipe.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if writer.outputCaptureComplete() {
		t.Fatal("reaching the cap must not settle a still-open stream")
	}
	if err := pipe.CloseWithError(io.ErrUnexpectedEOF); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost read failure: %v", err)
	}
	if _, err := writer.ReadFrom(strings.NewReader("later")); err != nil {
		t.Fatal(err)
	}
	if writer.outputCaptureComplete() || writer.String() != "par" || writer.Total() != 12 {
		t.Fatalf("later EOF erased failure or changed accounting: complete=%t output=%q total=%d", writer.outputCaptureComplete(), writer.String(), writer.Total())
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandGroupInspectionRejectsMissingLeader(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	configureCommandProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if alive, err := commandGroupHasLiveMembers(cmd.Process.Pid); err == nil {
		t.Fatalf("missing pinned leader was accepted as group evidence: alive=%t", alive)
	}
	for _, pgid := range []int{-1, 0} {
		if _, err := commandGroupHasLiveMembers(pgid); err == nil {
			t.Errorf("invalid group ID %d accepted as settlement evidence", pgid)
		}
	}
}

func TestCommandGroupInspectionRejectsForeignGroupLeader(t *testing.T) {
	// This child inherits the test's group; its PID is not a group we own.
	// Killing the child by its own handle is safe. Never signal its group.
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if alive, err := commandGroupHasLiveMembers(cmd.Process.Pid); err == nil {
		t.Fatalf("foreign process-group membership accepted: alive=%t", alive)
	}
}

func TestCommandGroupInspectionRetainsZombieLeaderEvidence(t *testing.T) {
	cmd := startGroupTestCommand(t, t.TempDir(), "exit 0")
	// Observe without reaping, exactly as the production waiter does.
	if err := observeCommandExit(cmd.Process.Pid, 0); err != nil {
		t.Fatal(err)
	}
	if alive, err := commandGroupHasLiveMembers(cmd.Process.Pid); err != nil || alive {
		t.Fatalf("unreaped zombie leader should establish a quiet owned group: alive=%t err=%v", alive, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := commandGroupHasLiveMembers(cmd.Process.Pid); err == nil {
		t.Fatal("reaped leader must no longer establish group ownership")
	}
}

func TestParseCommandGroupStatRequiresCompleteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		group int
		state string
		valid bool
	}{
		{"running", "42 (worker) R 1 42 0 0 0", 42, "R", true},
		{"zombie", "42 (worker) Z 1 42 0", 42, "Z", true},
		{"dead", "42 (worker) X 1 42 0", 42, "X", true},
		{"dead past signal teardown", "42 (sleep) X 0 -1 -1 0 -1 4227084", -1, "X", true},
		{"group -1 while still running", "42 (worker) R 0 -1 -1 0", 0, "", false},
		{"group -1 while sleeping", "42 (worker) S 0 -1 -1 0", 0, "", false},
		{"group below -1 even when dead", "42 (worker) X 0 -2 -1 0", 0, "", false},
		{"comm parentheses and newline", "42 (name )\n(inner)) S 1 42 0", 42, "S", true},
		{"unknown remains nonterminal", "42 (worker) ? 1 42 0", 42, "?", true},
		{"foreign group", "42 (worker) R 1 80 0", 80, "R", true},
		{"empty", "", 0, "", false},
		{"wrong pid", "43 (worker) Z 1 42 0", 0, "", false},
		{"missing pid", "(worker) Z 1 42", 0, "", false},
		{"missing comm", "42 Z 1 42", 0, "", false},
		{"missing state", "42 (worker) 1 42", 0, "", false},
		{"truncated group", "42 (worker) Z 1", 0, "", false},
		{"malformed state", "42 (worker) ZZ 1 42", 0, "", false},
		{"malformed parent", "42 (worker) Z none 42", 0, "", false},
		{"negative parent", "42 (worker) Z -1 42", 0, "", false},
		{"malformed group", "42 (worker) Z 1 none", 0, "", false},
		{"negative group", "42 (worker) Z 1 -1", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group, state, err := parseCommandGroupStat([]byte(tc.input), 42)
			if (err == nil) != tc.valid || group != tc.group || state != tc.state {
				t.Fatalf("got group=%d state=%q err=%v, want group=%d state=%q valid=%t", group, state, err, tc.group, tc.state, tc.valid)
			}
		})
	}
}

func TestCommandCleanupSettlesEmptyCapturedStreams(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code))
			stdout, stderr := newCappedWriter(3), newCappedWriter(3)
			cmd.Stdout, cmd.Stderr = stdout, stderr
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if !result.Settled || result.Cancelled || !stdout.outputCaptureComplete() || !stderr.outputCaptureComplete() {
				t.Fatalf("empty captures did not settle: %+v", result)
			}
		})
	}
}
