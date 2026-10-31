//go:build linux || darwin

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		if receipt, ok := e.loadCommandExecution(step.ID); !ok || receipt.Status != commandExecutionSettled {
			t.Fatalf("cancellation left an unresolved receipt: %+v", receipt)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("executeCommand did not return within cancellation grace period")
	}

	waitUntil(t, 2*time.Second, func() bool {
		return !processExists(childPID)
	}, "child process still exists after process-group cancellation")
}

func TestCommandBackgroundChildSettlesBeforeReceiptRecovery(t *testing.T) {
	e := newCommandTestExecutor(t)
	dir := e.config.ProjectDir
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "produce", Command: `printf 'once\n' >> launches; (printf ready > child.ready; while [ ! -f release ]; do sleep 0.01; done; printf complete > child-effect) >/dev/null 2>&1 & printf leader`},
			{ID: "consume", DependsOn: []string{"produce"}, Command: "cat child-effect"},
		},
	}
	e.state.Status, e.state.Session = StatusRunning, e.config.Session
	e.state.InFlightSteps = map[string]InFlightStepState{
		"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: time.Now()},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan StepResult, 1)
	go func() {
		done <- e.executeCommand(ctx, &workflow.Steps[0], workflow)
		close(done)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600)
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("background command did not finish cleanup")
		}
	})
	waitUntil(t, 2*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "child.ready"))
		return err == nil
	}, "background child never became ready")
	select {
	case result := <-done:
		t.Fatalf("leader exit surrendered its live background child: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	prior, err := LoadState(dir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, ok := prior.CommandExecutions["produce"]; !ok || receipt.Status != commandExecutionRunning {
		t.Fatalf("live background work lost its running receipt: %+v", receipt)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Status != StatusCompleted || result.Output != "leader" {
			t.Fatalf("natural background completion failed: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("finished background child did not settle")
	}
	prior, err = LoadState(dir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, ok := prior.CommandExecutions["produce"]; !ok || receipt.Status != commandExecutionSettled {
		t.Fatalf("finished background work left an unresolved receipt: %+v", receipt)
	}
	if _, exists := prior.Steps["produce"]; exists {
		t.Fatal("fixture already has the scheduler checkpoint")
	}
	final, err := NewExecutor(e.config).Resume(ctx, workflow, prior, nil)
	if err != nil || final == nil || final.Status != StatusCompleted || final.Steps["consume"].Output != "complete" {
		t.Fatalf("ordinary receipt recovery failed: %+v %v", final, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "launches")); err != nil || string(data) != "once\n" {
		t.Fatalf("receipt recovery replayed the producer: %q %v", data, err)
	}
}

func TestCommandBackgroundChildSettlesBeforeExitRetry(t *testing.T) {
	e := newCommandTestExecutor(t)
	workflow := &Workflow{
		Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "background-exit",
			Command:    `printf 'attempt\n' >> attempts; (sleep 0.08; printf 'child\n' >> children) >/dev/null 2>&1 & exit 7`,
			OnError:    ErrorActionRetry,
			RetryCount: 1,
			RetryDelay: Duration{Duration: time.Millisecond},
		}},
	}
	e.graph = NewDependencyGraph(workflow)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := e.executeStep(ctx, &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "exit" || result.Attempts != 2 {
		t.Fatalf("settled nonzero exit did not retry: %+v", result)
	}
	if receipt, ok := e.loadCommandExecution(workflow.Steps[0].ID); !ok || receipt.Status != commandExecutionSettled {
		t.Fatalf("completed retry left an unresolved receipt: %+v", receipt)
	}
	for _, file := range []struct {
		name string
		want string
	}{
		{"attempts", "attempt\nattempt\n"},
		{"children", "child\nchild\n"},
	} {
		if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, file.name)); err != nil || string(data) != file.want {
			t.Fatalf("retry did not wait for each background child (%s): %q %v", file.name, data, err)
		}
	}
}

func TestCommandBackgroundChildCancellationSettlesAndResumes(t *testing.T) {
	e := newCommandTestExecutor(t)
	dir := e.config.ProjectDir
	child := `trap '' TERM
printf '%s\n' "$$" > child.pid
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`
	if err := os.WriteFile(filepath.Join(dir, "child.sh"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "background-cancel",
			Command: `printf 'attempt\n' >> attempts; if [ -f allow ]; then printf resumed; exit 0; fi; /bin/sh child.sh >/dev/null 2>&1 &`,
		}},
	}
	type outcome struct {
		state *ExecutionState
		err   error
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan outcome, 1)
	go func() {
		state, err := NewExecutor(e.config).Run(ctx, workflow, nil, nil)
		done <- outcome{state, err}
		close(done)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600)
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("cancelled background workflow did not finish cleanup")
		}
	})
	waitForPIDFile(t, filepath.Join(dir, "child.pid"))
	select {
	case result := <-done:
		t.Fatalf("workflow abandoned its live background child: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	var initial outcome
	select {
	case initial = <-done:
		if initial.err == nil || initial.state == nil || initial.state.Status != StatusCancelled {
			t.Fatalf("background workflow did not cancel: %+v %v", initial.state, initial.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancellation did not finish within its cleanup bound")
	}
	prior, err := LoadState(dir, initial.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, ok := prior.CommandExecutions["background-cancel"]; !ok || receipt.Status != commandExecutionSettled {
		t.Fatalf("killed background child left an unresolved receipt: %+v", receipt)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled child performed work after cleanup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "allow"), []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	resumeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	final, err := NewExecutor(e.config).Resume(resumeCtx, workflow, prior, nil)
	if err != nil || final == nil || final.Status != StatusCompleted || final.Steps["background-cancel"].Output != "resumed" {
		t.Fatalf("ordinary resume after cancellation failed: %+v %v", final, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "attempts")); err != nil || string(data) != "attempt\nattempt\n" {
		t.Fatalf("resume did not execute exactly one new attempt: %q %v", data, err)
	}
}

func TestCommandGroupInspectionFailurePreservesUnresolvedLaunch(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux uses procfs rather than the portable ps inspection boundary")
	}
	for _, tc := range []struct {
		name   string
		script string
	}{
		{"ps_failure", "#!/bin/sh\nexit 1\n"},
		{"missing_leader", "#!/bin/sh\nprintf '1 1 S\\n'\n"},
		{"malformed_output", "#!/bin/sh\nprintf 'malformed row\\n'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			fakeBin := t.TempDir()
			if err := os.WriteFile(filepath.Join(fakeBin, "ps"), []byte(tc.script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
				Steps: []Step{{
					ID:         "uncertain",
					Command:    "printf once >> effects; sleep 30",
					OnError:    ErrorActionRetry,
					RetryCount: 2,
					RetryDelay: Duration{Duration: time.Millisecond},
				}},
			}
			const timeout = 250 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			started := time.Now()
			initial, err := NewExecutor(e.config).Run(ctx, workflow, nil, nil)
			if err == nil || ctx.Err() != context.DeadlineExceeded || initial == nil || initial.Status != StatusCancelled || initial.Steps["uncertain"].Attempts != 1 {
				t.Fatalf("inspection failure ended work before its genuine timeout or retried it: %+v %v", initial, err)
			}
			if elapsed := time.Since(started); elapsed < timeout+commandCancelGracePeriod {
				t.Fatalf("failed inspection shortened the command deadline or SIGTERM grace: %v", elapsed)
			}
			prior, err := LoadState(e.config.ProjectDir, initial.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt, ok := prior.CommandExecutions["uncertain"]; !ok || receipt.Status != commandExecutionUnresolved ||
				!receipt.SettledAt.IsZero() || receipt.Result.Error == nil || receipt.Result.FinishedAt.IsZero() ||
				!strings.Contains(receipt.Result.Error.Details, "SIGTERM grace period") {
				t.Fatalf("failed inspection lost its unresolved cleanup evidence: %+v", receipt)
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err != nil || string(data) != "once" {
				t.Fatalf("uncertain command effects were lost or repeated: %q %v", data, err)
			}
			statePath := pipelineStatePath(e.config.ProjectDir, initial.RunID)
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
				t.Fatalf("ordinary resume accepted an unresolved command: %v", err)
			}
			after, err := os.ReadFile(statePath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("rejected resume changed the saved evidence: %v", err)
			}
		})
	}
}

func TestCommandBackgroundChildCooperativeCancellationSettles(t *testing.T) {
	e := newCommandTestExecutor(t)
	dir := e.config.ProjectDir
	child := "trap 'printf handled > handled; exit 0' TERM\n" +
		"printf '%s\\n' \"$$\" > child.pid\n" +
		"while [ ! -f release ]; do sleep 0.01; done\n"
	if err := os.WriteFile(filepath.Join(dir, "child.sh"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	step := &Step{
		ID:      "cooperative-cancel",
		Command: "trap 'exit 0' TERM; /bin/sh child.sh </dev/null >/dev/null 2>&1 & wait",
		Timeout: Duration{Duration: 10 * time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan StepResult, 1)
	go func() {
		done <- e.executeCommand(ctx, step, &Workflow{Name: e.state.WorkflowID})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600)
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("cooperative command did not finish cleanup")
		}
	})
	waitForPIDFile(t, filepath.Join(dir, "child.pid"))
	cancel()
	select {
	case result := <-done:
		if result.Status != StatusCancelled {
			t.Fatalf("cooperative cancellation returned %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cooperative child needed forced cleanup after the grace period")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "handled")); err != nil || string(data) != "handled" {
		t.Fatalf("cancellation interrupted the child's TERM handler: %q %v", data, err)
	}
	if receipt, ok := e.loadCommandExecution(step.ID); !ok || receipt.Status != commandExecutionSettled {
		t.Fatalf("cooperative cleanup left an unresolved receipt: %+v", receipt)
	}
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

// A group whose only member is its exited, still-unreaped leader has nothing
// left to signal. Darwin's killpg skips zombies and answers EPERM rather than
// ESRCH for such a group, which used to fail the final signal of every quiet
// command, so no command step on macOS could settle or complete.
func TestSignalCommandProcessGroupZombieOnlyGroupIsDone(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	quiet, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := waitCommandGroupQuiescent(quiet, cmd.Process.Pid); err != nil {
		t.Fatalf("leader did not exit: %v", err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := signalCommandProcessGroup(cmd, sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("signal %v to a group of only its unreaped leader failed: %v", sig, err)
		}
	}
	cleanup := waitCommandWithProcessGroupCleanup(context.Background(), cmd)
	if !cleanup.Settled || cleanup.Err != nil {
		t.Fatalf("quiet exited command did not settle: %+v", cleanup)
	}
}

// EPERM from a group signal can also mean a live member this process may not
// signal, so it is discounted only when the census finds no live member.
func TestGroupSignalFoundOnlyZombiesRejectsLiveGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cancelCommandProcessGroup(cmd)
		_ = cmd.Wait()
	})
	if groupSignalFoundOnlyZombies(cmd.Process.Pid) {
		t.Fatal("a group with a live member was treated as having nothing left to signal")
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

func TestCommandGroupObservationPolicy(t *testing.T) {
	fault := errors.New("injected process-group observation failure")
	t.Run("unknown then live then quiet", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		calls := 0
		err := waitCommandGroupObservation(ctx, 42, func() (bool, error) {
			calls++
			switch calls {
			case 1, 2:
				return false, fault
			case 3:
				return true, nil
			default:
				return false, nil
			}
		})
		if err != nil || calls != 4 {
			t.Fatalf("observation did not recover: calls=%d err=%v", calls, err)
		}
	})
	for _, tc := range []struct {
		name          string
		secondAlive   bool
		secondFailure error
		wantFailure   bool
	}{
		{"cancel during unknown observation", false, fault, true},
		{"cancel after recovered live observation", true, nil, false},
		{"cancel during quiet observation", false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			err := waitCommandGroupObservation(ctx, 42, func() (bool, error) {
				calls++
				if calls == 1 {
					return false, fault
				}
				cancel()
				return tc.secondAlive, tc.secondFailure
			})
			if calls != 2 || !errors.Is(err, context.Canceled) || errors.Is(err, fault) != tc.wantFailure {
				t.Fatalf("cancellation lost or retained obsolete evidence: calls=%d err=%v", calls, err)
			}
		})
	}
	t.Run("independent cleanup preserves observer", func(t *testing.T) {
		calls := 0
		ctx := context.WithValue(context.Background(), commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
			if pgid != 42 {
				t.Fatalf("observer received group %d", pgid)
			}
			calls++
			if calls == 1 {
				return false, fault
			}
			return false, nil
		})
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(cancelled), time.Second)
		defer stop()
		if err := waitCommandGroupObservation(cleanup, 42, func() (bool, error) {
			t.Fatal("cleanup lost its per-command observer")
			return false, nil
		}); err != nil || calls != 2 {
			t.Fatalf("cleanup inherited cancellation or lost observer: calls=%d err=%v", calls, err)
		}
	})
	t.Run("persistent unknown uses full deadline", func(t *testing.T) {
		const budget = 150 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		started := time.Now()
		err := waitCommandGroupObservation(ctx, 42, func() (bool, error) { return false, fault })
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, fault) || time.Since(started) < budget {
			t.Fatalf("unknown observation shortened its budget or lost the failure: %v", err)
		}
	})
}

func TestParseCommandGroupPSOutputRequiresCompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		alive  bool
		err    string
	}{
		{"live leader", "42 42 S\n", true, ""},
		{"live descendant", "42 42 Z\n43 42 R\n", true, ""},
		{"zombie group", "42 42 Z\n43 42 Z+\n", false, ""},
		{"unrelated live work", "41 41 S\n42 42 Z\n", false, ""},
		{"whitespace", "\n\t42\t42\tZ\n\n", false, ""},
		{"unknown state remains live", "42 42 ?\n", true, ""},
		{"missing leader", "43 42 Z\n", false, "leader missing"},
		{"moved leader", "42 41 Z\n", false, "leader changed process group"},
		{"empty census", "", false, "leader missing"},
		{"truncated row", "42 42\n", false, "malformed ps row"},
		{"unrelated malformed row", "42 42 Z\nunknown row\n", false, "malformed ps row"},
		{"extra column", "42 42 Z extra\n", false, "malformed ps row"},
		{"invalid pid", "bad 42 Z\n", false, "invalid ps process identity"},
		{"invalid group", "42 bad Z\n", false, "invalid ps process identity"},
		{"negative pid", "-1 42 Z\n", false, "invalid ps process identity"},
		{"negative group", "42 -1 Z\n", false, "invalid ps process identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive, err := parseCommandGroupPSOutput([]byte(tc.output), 42)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) || alive {
					t.Fatalf("incomplete census was accepted: alive=%t err=%v", alive, err)
				}
			} else if err != nil || alive != tc.alive {
				t.Fatalf("census alive=%t err=%v, want alive=%t", alive, err, tc.alive)
			}
		})
	}
	for _, pgid := range []int{-1, 0, 1} {
		if _, err := parseCommandGroupPSOutput([]byte("1 1 Z\n"), pgid); err == nil {
			t.Fatalf("accepted unsafe group %d", pgid)
		}
	}
}

func TestCommandGroupPSObservationRecoversFailures(t *testing.T) {
	realPS, err := exec.LookPath("ps")
	if err != nil {
		t.Skipf("system ps unavailable: %v", err)
	}
	for _, fault := range []string{"exit_failure", "missing_leader", "malformed_row"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			probes, recovered := filepath.Join(dir, "probes"), filepath.Join(dir, "recovered")
			t.Setenv("NTM_TEST_COMMAND_PS_PROBES", probes)
			t.Setenv("NTM_TEST_COMMAND_PS_RECOVERED", recovered)
			t.Setenv("NTM_TEST_COMMAND_PS_FAULT", fault)
			t.Setenv("NTM_TEST_COMMAND_PS_REAL", realPS)
			const script = `#!/bin/sh
printf x >> "$NTM_TEST_COMMAND_PS_PROBES"
if [ ! -f "$NTM_TEST_COMMAND_PS_RECOVERED" ]; then
  case "$NTM_TEST_COMMAND_PS_FAULT" in
    exit_failure) exit 42 ;;
    missing_leader) printf '1 1 S\n' ;;
    malformed_row) printf 'uninterpretable row\n' ;;
  esac
  exit 0
fi
exec "$NTM_TEST_COMMAND_PS_REAL" "$@"
`
			if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd := exec.Command("/bin/sh", "-c", "while [ ! -f release ]; do sleep 0.01; done; printf complete")
			cmd.Dir = dir
			var output bytes.Buffer
			cmd.Stdout = &output
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			done, live := make(chan error, 1), make(chan struct{})
			go func() {
				liveReported := false
				done <- waitCommandGroupObservation(ctx, cmd.Process.Pid, func() (bool, error) {
					alive, err := observeCommandGroupWithPS(ctx, cmd.Process.Pid)
					if err == nil && alive && !liveReported {
						liveReported = true
						close(live)
					}
					return alive, err
				})
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				_ = os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600)
				select {
				case <-done:
				case <-time.After(3 * commandPipeDrainTimeout):
					t.Error("ps observer did not stop after cancellation")
				}
				if cmd.ProcessState == nil {
					_ = cancelCommandProcessGroup(cmd)
					_ = cmd.Wait()
				}
			})
			waitUntil(t, 4*time.Second, func() bool {
				data, _ := os.ReadFile(probes)
				return len(data) >= 2
			}, "portable observer did not retry the real ps failures")
			select {
			case err := <-done:
				t.Fatalf("failed ps census abandoned healthy work: %v", err)
			default:
			}
			if !processExists(cmd.Process.Pid) {
				t.Fatal("observation failure killed the healthy leader")
			}
			if err := os.WriteFile(recovered, []byte("observe"), 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-live:
			case err := <-done:
				t.Fatalf("recovered ps did not observe the live leader: %v", err)
			case <-ctx.Done():
				t.Fatal("real ps observation did not recover")
			}
			if err := os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("real ps could not settle the unreaped group: %v", err)
			}
			if err := cmd.Wait(); err != nil || output.String() != "complete" {
				t.Fatalf("healthy work lost its output: %q %v", output.String(), err)
			}
		})
	}
}

func TestCommandGroupPSProbeHonorsDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	alive, err := observeCommandGroupWithPS(context.Background(), 42)
	if alive || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked ps was accepted or lost its deadline: alive=%t err=%v", alive, err)
	}
	if elapsed := time.Since(started); elapsed < commandPipeDrainTimeout || elapsed > 3*commandPipeDrainTimeout {
		t.Fatalf("blocked ps escaped its observation budget: %v", elapsed)
	}
}
