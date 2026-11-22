//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const testSetsidHelperEnv = "NTM_TEST_SETSID_HELPER_MODE"

func init() {
	testMainHelperHook = func() (int, bool) {
		mode := os.Getenv(testSetsidHelperEnv)
		if mode == "" {
			return 0, false
		}
		return runSetsidEscapeChildHelper(mode), true
	}
}

func runSetsidEscapeChildHelper(mode string) int {
	if _, err := syscall.Setsid(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "setsid failed: %v\n", err)
		return 2
	}
	if mode == "ignore_term" {
		signal.Ignore(syscall.SIGTERM)
	}
	pidFile := os.Getenv("NTM_TEST_SETSID_PID_FILE")
	if pidFile != "" {
		tmp := pidFile + ".tmp"
		_ = os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600)
		_ = os.Rename(tmp, pidFile)
	}
	time.Sleep(60 * time.Second)
	return 0
}

func requireWritableCgroupV2OrSkip(t *testing.T) string {
	t.Helper()
	t.Setenv(CgroupRootEnv, "")
	parent, err := resolveWritableCgroupV2Parent()
	if err != nil {
		t.Skipf("writable cgroup v2 delegation unavailable on host: %v", err)
	}
	return parent
}

func waitForPIDGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if raw, rErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); rErr == nil {
			if _, stateChar, pErr := parseProcStatStartTimeTicks(raw, pid); pErr == nil && (stateChar == "Z" || stateChar == "X") {
				return true
			}
		} else if errors.Is(rErr, os.ErrNotExist) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestContainmentDoubleForkingDaemon(t *testing.T) {
	tiers := []string{ContainmentTierCgroupV2, ContainmentTierSubreaperPIDFD}
	for _, tier := range tiers {
		t.Run(tier, func(t *testing.T) {
			if tier == ContainmentTierCgroupV2 {
				requireWritableCgroupV2OrSkip(t)
			}
			t.Setenv(ContainmentTierEnv, tier)

			dir := t.TempDir()
			outFile := filepath.Join(dir, "daemon-done.txt")

			statOpensBefore := commandGroupProcStatOpens.Load()

			cmd := exec.Command("/bin/sh", "-c", `(sleep 0.25; printf done > "$OUT") &`)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "OUT="+outFile)
			configureCommandProcessGroup(cmd)

			meta, err := startCommandWithContainment(context.Background(), cmd, commandStartOptions{})
			if err != nil {
				t.Fatalf("startCommandWithContainment(%s) failed: %v", tier, err)
			}
			if meta.Tier != tier {
				t.Fatalf("tier = %q, want %q", meta.Tier, tier)
			}
			if tier == ContainmentTierCgroupV2 && meta.CgroupPath == "" {
				t.Fatal("expected non-empty cgroup leaf path for cgroup_v2 tier")
			}

			cleanup := waitCommandWithProcessGroupCleanup(context.Background(), cmd)
			statOpensDelta := commandGroupProcStatOpens.Load() - statOpensBefore

			if !cleanup.Settled || cleanup.Err != nil {
				t.Fatalf("cleanup(%s) = %+v, want settled with nil error", tier, cleanup)
			}
			got, err := os.ReadFile(outFile)
			if err != nil || string(got) != "done" {
				t.Fatalf("double-forked grandchild did not finish before settlement: got=%q err=%v", string(got), err)
			}
			if statOpensDelta != 0 {
				t.Fatalf("/proc/<pid>/stat opens during %s settlement = %d, want 0", tier, statOpensDelta)
			}
			if tier == ContainmentTierCgroupV2 {
				if _, err := os.Stat(meta.CgroupPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("expected cgroup leaf %q to be removed after settlement, got err=%v", meta.CgroupPath, err)
				}
			}
		})
	}
}

func TestContainmentCgroupRootOverrideAndCascadeFallback(t *testing.T) {
	parent := requireWritableCgroupV2OrSkip(t)

	// 1. Verify NTM_CGROUP_ROOT override to an explicit delegated slice directory.
	customSlice := filepath.Join(parent, fmt.Sprintf("ntm-test-slice-%d", os.Getpid()))
	if err := os.Mkdir(customSlice, 0o755); err != nil {
		t.Fatalf("mkdir custom cgroup slice: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(customSlice) })

	t.Setenv(ContainmentTierEnv, "")
	t.Setenv(CgroupRootEnv, customSlice)

	cmd := exec.Command("/bin/sh", "-c", "printf ok")
	configureCommandProcessGroup(cmd)
	meta, err := startCommandWithContainment(context.Background(), cmd, commandStartOptions{})
	if err != nil {
		t.Fatalf("start under NTM_CGROUP_ROOT failed: %v", err)
	}
	if meta.Tier != ContainmentTierCgroupV2 || !strings.HasPrefix(meta.CgroupPath, customSlice+"/") {
		t.Fatalf("unexpected metadata under NTM_CGROUP_ROOT: %+v", meta)
	}
	cleanup := waitCommandWithProcessGroupCleanup(context.Background(), cmd)
	if !cleanup.Settled || cleanup.Err != nil {
		t.Fatalf("cleanup under NTM_CGROUP_ROOT failed: %+v", cleanup)
	}

	// 2. Verify automatic Tier 1 -> Tier 2 cascade when cgroup v2 is unavailable.
	t.Setenv(CgroupRootEnv, "disabled")
	t.Setenv(StepReaperTestAllowEnv, "1")
	cmd2 := exec.Command("/bin/sh", "-c", "printf cascaded")
	configureCommandProcessGroup(cmd2)
	meta2, err := startCommandWithContainment(context.Background(), cmd2, commandStartOptions{})
	if err != nil {
		t.Fatalf("cascade start failed: %v", err)
	}
	if meta2.Tier != ContainmentTierSubreaperPIDFD {
		t.Fatalf("cascade tier = %q, want %q", meta2.Tier, ContainmentTierSubreaperPIDFD)
	}
	cleanup2 := waitCommandWithProcessGroupCleanup(context.Background(), cmd2)
	if !cleanup2.Settled || cleanup2.Err != nil {
		t.Fatalf("cascade cleanup failed: %+v", cleanup2)
	}
}

func TestContainmentSetsidEscapeCancellation(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	quotedExe := shellSingleQuote(selfExe)

	for _, tier := range []string{ContainmentTierCgroupV2, ContainmentTierSubreaperPIDFD} {
		for _, mode := range []string{"default_term", "ignore_term"} {
			t.Run(tier+"_"+mode, func(t *testing.T) {
				if tier == ContainmentTierCgroupV2 {
					requireWritableCgroupV2OrSkip(t)
				}
				t.Setenv(ContainmentTierEnv, tier)

				dir := t.TempDir()
				pidFile := filepath.Join(dir, "setsid-child.pid")

				statOpensBefore := commandGroupProcStatOpens.Load()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				script := fmt.Sprintf(
					`NTM_TEST_SETSID_HELPER_MODE=%s NTM_TEST_SETSID_PID_FILE=%s %s </dev/null >/dev/null 2>&1 &
while [ ! -s %s ]; do sleep 0.01; done
sleep 30`,
					shellSingleQuote(mode),
					shellSingleQuote(pidFile),
					quotedExe,
					shellSingleQuote(pidFile),
				)

				cmd := exec.Command("/bin/sh", "-c", script)
				cmd.Dir = dir
				configureCommandProcessGroup(cmd)

				meta, err := startCommandWithContainment(ctx, cmd, commandStartOptions{})
				if err != nil {
					t.Fatalf("startCommandWithContainment failed: %v", err)
				}
				if meta.Tier != tier {
					t.Fatalf("tier = %q, want %q", meta.Tier, tier)
				}

				// Wait until the setsid() grandchild is running in its own session.
				var escapedPID int
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if raw, rErr := os.ReadFile(pidFile); rErr == nil && len(strings.TrimSpace(string(raw))) > 0 {
						escapedPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
						if err == nil && escapedPID > 1 {
							break
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				if escapedPID <= 1 {
					cancel()
					_ = waitCommandWithProcessGroupCleanup(ctx, cmd)
					t.Fatal("setsid grandchild never wrote its PID")
				}
				t.Cleanup(func() {
					_ = syscall.Kill(escapedPID, syscall.SIGKILL)
				})

				// Confirm the grandchild actually escaped the leader's PGID via setsid().
				if pgid, gErr := syscall.Getpgid(escapedPID); gErr == nil && pgid == cmd.Process.Pid {
					t.Fatalf("expected setsid grandchild PGID (%d) != leader PID (%d)", pgid, cmd.Process.Pid)
				}

				cancel()
				cleanup := waitCommandWithProcessGroupCleanup(ctx, cmd)
				statOpensDelta := commandGroupProcStatOpens.Load() - statOpensBefore

				if !cleanup.Cancelled || !cleanup.Settled {
					t.Fatalf("cancelled containment cleanup(%s/%s) = %+v, want Cancelled && Settled", tier, mode, cleanup)
				}
				if !waitForPIDGone(escapedPID, 2*time.Second) {
					t.Fatalf("setsid() grandchild PID %d leaked as an orphan on tier %s (%s)", escapedPID, tier, mode)
				}
				if statOpensDelta != 0 {
					t.Fatalf("/proc/<pid>/stat opens on %s cancellation = %d, want 0", tier, statOpensDelta)
				}
				if tier == ContainmentTierCgroupV2 {
					if _, sErr := os.Stat(meta.CgroupPath); !errors.Is(sErr, os.ErrNotExist) {
						t.Fatalf("expected cgroup leaf %q removed after cancelled settlement, got %v", meta.CgroupPath, sErr)
					}
				}
			})
		}
	}
}

func TestSubreaperWrapperFailureDoesNotSettle(t *testing.T) {
	for _, failure := range []string{"killed_wrapper", "stopped_wrapper_cancellation"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)
			root := t.TempDir()
			cfg := DefaultExecutorConfig("subreaper-failure")
			cfg.ProjectDir, cfg.RunID = root, "run-subreaper-failure"
			workflow := &Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "subreaper-failure",
				Settings:      DefaultWorkflowSettings(),
				Steps: []Step{
					{
						ID: "produce",
						// Close the capture streams so EOF cannot accidentally
						// conceal a false settlement after the wrapper dies.
						Command: `exec >/dev/null 2>&1
printf 'once\n' >> launches
if [ -f payload.pid ]; then exit 0; fi
printf '%s\n' "$$" > payload.pid
sleep 30 &
printf '%s\n' "$!" > descendant.pid
wait`,
						OnError: ErrorActionRetry, RetryCount: 2,
						RetryDelay: Duration{Duration: time.Millisecond},
					},
					{ID: "consume", DependsOn: []string{"produce"}, Command: "printf unsafe > downstream"},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			started := make(chan int, 1)
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) {
				select {
				case started <- pid:
				default:
				}
			})
			var state *ExecutionState
			var runErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				state, runErr = NewExecutor(cfg).Run(ctx, workflow, nil, nil)
			}()
			var wrapper *os.Process
			var payloads []*os.Process
			t.Cleanup(func() {
				cancel()
				if wrapper != nil {
					_ = wrapper.Signal(syscall.SIGCONT)
				}
				for _, process := range payloads {
					_ = process.Kill()
					_ = process.Release()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					if wrapper != nil {
						_ = wrapper.Kill()
					}
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("wrapper failure left executor supervision running")
					}
				}
				if wrapper != nil {
					_ = wrapper.Release()
				}
			})
			select {
			case pid := <-started:
				var err error
				wrapper, err = os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
			case <-done:
				t.Fatalf("executor ended before command launch: %+v %v", state, runErr)
			case <-ctx.Done():
				t.Fatal("command did not start")
			}
			for _, name := range []string{"payload.pid", "descendant.pid"} {
				pid := waitForPIDFile(t, filepath.Join(root, name))
				process, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				payloads = append(payloads, process)
			}
			if failure == "killed_wrapper" {
				if err := wrapper.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := wrapper.Signal(syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
				waitUntil(t, time.Second, func() bool {
					_, state, err := readProcessStartTimeTicks(wrapper.Pid)
					return err == nil && (state == "T" || state == "t")
				}, "wrapper did not stop before cancellation")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("wrapper failure did not end supervision within its cleanup bound")
			}
			if runErr == nil || state == nil || state.Status == StatusCompleted {
				t.Fatalf("wrapper death became successful execution: %+v %v", state, runErr)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions["produce"]
			if receipt.Status != commandExecutionUnresolved || !receipt.SettledAt.IsZero() || receipt.Result.RerunOnResume ||
				receipt.Result.Error == nil || !strings.Contains(receipt.Result.Error.Details, "without verified descendant settlement") {
				t.Fatalf("wrapper death granted settlement or lost its recovery evidence: %+v", receipt)
			}
			for _, process := range payloads {
				_, state, err := readProcessStartTimeTicks(process.Pid)
				if err != nil || state == "Z" || state == "X" {
					t.Fatalf("test requires surviving payload %d, state=%q error=%v", process.Pid, state, err)
				}
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
				t.Fatalf("resume allowed work whose wrapper died without settlement: %v", err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected resume rewrote unresolved command evidence")
			}
			if launches, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(launches) != "once\n" {
				t.Fatalf("wrapper failure retried an unsettled command: launches=%q error=%v", launches, err)
			}
			if _, err := os.Stat(filepath.Join(root, "downstream")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("wrapper failure released downstream work: %v", err)
			}
		})
	}
}

func TestSubreaperSettlementPreservesTerminalOutcomes(t *testing.T) {
	t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)
	for _, tc := range []struct {
		name     string
		script   string
		exitCode int
		signal   syscall.Signal
		missing  bool
	}{
		{name: "success", script: "printf ok"},
		{name: "failed_command", script: "exit 7", exitCode: 7},
		{name: "child_sigterm", script: "kill -TERM $$", signal: syscall.SIGTERM},
		{name: "child_sigkill", script: "kill -KILL $$", signal: syscall.SIGKILL},
		{name: "child_sigusr1", script: "trap - USR1; kill -USR1 $$", signal: syscall.SIGUSR1},
		{name: "child_sigpipe", script: "trap - PIPE; kill -PIPE $$", signal: syscall.SIGPIPE},
		{name: "child_sigquit", script: "ulimit -c 0; trap - QUIT; kill -QUIT $$", signal: syscall.SIGQUIT},
		{name: "child_sigabrt", script: "ulimit -c 0; trap - ABRT; kill -ABRT $$", signal: syscall.SIGABRT},
		{name: "missing_executable", missing: true, exitCode: 127},
		{name: "proof_descriptor_not_inherited", script: "test ! -e /proc/$$/fd/4 && printf private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			if tc.missing {
				cmd = exec.Command(filepath.Join(t.TempDir(), "missing-executable"))
			}
			cmd.Dir = t.TempDir()
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			null, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer null.Close()
			// Exercise the private proof fd after an existing ExtraFiles fd.
			cmd.ExtraFiles = []*os.File{null}
			opts := commandStartOptions{ExitCodeFile: filepath.Join(t.TempDir(), "exit.json")}
			if _, err := startCommandWithContainment(ctx, cmd, opts); err != nil {
				t.Fatal(err)
			}
			if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != null {
				t.Fatal("containment changed the caller's extra file configuration")
			}
			cleanup := waitCommandWithProcessGroupCleanup(ctx, cmd)
			if !cleanup.Settled || cmd.ProcessState == nil {
				t.Fatalf("known child outcome was left unresolved: %+v output=%q", cleanup, output.String())
			}
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatal("missing native child wait status")
			}
			if tc.signal != 0 {
				if !status.Signaled() || status.Signal() != tc.signal {
					t.Fatalf("child signal changed: status=%v want=%v", status, tc.signal)
				}
			} else if !status.Exited() || status.ExitStatus() != tc.exitCode {
				t.Fatalf("child exit changed: status=%v want=%d output=%q", status, tc.exitCode, output.String())
			}
			if tc.exitCode == 0 && tc.signal == 0 {
				if cleanup.Err != nil {
					t.Fatalf("successful child returned cleanup error: %v", cleanup.Err)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(cleanup.Err, &exitErr) {
					t.Fatalf("failed child lost its exit error: %v", cleanup.Err)
				}
			}
		})
	}
}

func TestSubreaperSettlementRejectsMismatchedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exitCode   int
		signal     syscall.Signal
		wrapperEnd string
	}{
		{name: "success_before_wrapper_kill", wrapperEnd: "kill -KILL $$"},
		{name: "changed_exit_code", exitCode: 7, wrapperEnd: "exit 0"},
		{name: "changed_signal", exitCode: 128 + int(syscall.SIGTERM), signal: syscall.SIGTERM, wrapperEnd: "kill -KILL $$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Model a wrapper that has finished its payload and published a
			// valid proof, then dies with a different status before returning
			// that payload's outcome. The proof alone cannot authorize retry.
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			proof := fmt.Sprintf("%s %d %d\n", stepReaperSettlementMarker, tc.exitCode, tc.signal)
			cmd := exec.Command("/bin/sh", "-c", "printf %s "+shellSingleQuote(proof)+" >&3; "+tc.wrapperEnd)
			cmd.ExtraFiles = []*os.File{write}
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = write.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cleanup := waitCommandWithSubreaperPIDFD(ctx, cmd, &linuxContainmentHandle{
				tier: ContainmentTierSubreaperPIDFD, pidfd: -1, settlement: read,
			})
			if cleanup.Settled || cleanup.Err == nil || !strings.Contains(cleanup.Err.Error(), "terminal status does not match completed payload outcome") {
				t.Fatalf("mismatched wrapper outcome was admitted as settlement: %+v", cleanup)
			}
		})
	}
}

func TestSubreaperChildOwnershipCannotOutliveReaping(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if !ownsUnreapedSubreaperChild(pid) {
		t.Fatal("native wait did not recognize its running child")
	}
	if ownsUnreapedSubreaperChild(os.Getpid()) {
		t.Fatal("native wait accepted an unrelated process as its child")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	if ownsUnreapedSubreaperChild(pid) {
		t.Fatal("native wait retained signaling ownership after reaping")
	}
}

func TestSubreaperStatusFallbackOnlySelectsOwnedChildren(t *testing.T) {
	root := t.TempDir()
	for name, status := range map[string]string{
		"101": "Name:\tchild\nPPid:\t42\n",
		"102": "Name:\tunrelated\nPPid:\t420\n",
		"103": "Name:\tunknown\nPPid:\tunavailable\n",
		"104": "Name:\tmissing-parent\n",
		"42":  "Name:\tself\nPPid:\t42\n",
	} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A disappearing process cannot be admitted as an owned child either.
	if err := os.Mkdir(filepath.Join(root, "105"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := readSubreaperChildrenFromProcStatus(root, 42)
	if len(got) != 1 || got[0] != 101 {
		t.Fatalf("status fallback selected unproven child identities: %v", got)
	}
}

func TestSubreaperCancellationAfterDirectChildReaped(t *testing.T) {
	t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"default_term", "ignore_term"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "adopted.pid")
			script := fmt.Sprintf(`printf '%%s\n' "$$" > direct.pid
NTM_TEST_SETSID_HELPER_MODE=%s NTM_TEST_SETSID_PID_FILE=%s %s </dev/null >/dev/null 2>&1 &
while [ ! -s %s ]; do sleep 0.01; done`,
				shellSingleQuote(mode), shellSingleQuote(pidFile), shellSingleQuote(selfExe), shellSingleQuote(pidFile))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Dir = dir
			if _, err := startCommandWithContainment(ctx, cmd, commandStartOptions{}); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				cancel()
				if !waited {
					_ = waitCommandWithProcessGroupCleanup(ctx, cmd)
				}
			})
			directPID := waitForPIDFile(t, filepath.Join(dir, "direct.pid"))
			adoptedPID := waitForPIDFile(t, pidFile)
			adopted, err := os.FindProcess(adoptedPID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = adopted.Kill()
				_ = adopted.Release()
			})
			waitUntil(t, 2*time.Second, func() bool {
				_, err := os.Stat(fmt.Sprintf("/proc/%d", directPID))
				return errors.Is(err, os.ErrNotExist)
			}, "subreaper did not reap its direct child before cancellation")
			cancel()
			cleanup := waitCommandWithProcessGroupCleanup(ctx, cmd)
			waited = true
			if !cleanup.Cancelled || !cleanup.Settled {
				t.Fatalf("adopted descendant did not settle after direct child reap: %+v", cleanup)
			}
			if !waitForPIDGone(adoptedPID, time.Second) {
				t.Fatal("adopted setsid descendant survived cancellation")
			}
		})
	}
}

func TestCrashOnlyReceiptReconciliationRequiresContainmentProof(t *testing.T) {
	// A shell's EXIT trap proves only its own exit. Its background child can
	// still be running after the orchestrator dies and the leader is reaped.
	// Neither a successful exit nor a failed exit may release downstream work
	// or authorize retrying the original command in that state.
	for _, tier := range []struct {
		name string
		tier string
	}{
		{"proc_pgid", ContainmentTierProcPGID},
		{"legacy_without_tier", ""},
		{"cgroup_without_leaf", ContainmentTierCgroupV2},
	} {
		for _, exitCode := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit_%d", tier.name, exitCode), func(t *testing.T) {
				t.Setenv(ContainmentTierEnv, ContainmentTierProcPGID)
				root := t.TempDir()
				cfg := DefaultExecutorConfig("unproven-shell-exit")
				cfg.ProjectDir, cfg.RunID = root, "run-unproven-shell-exit"
				workflow := &Workflow{
					SchemaVersion: SchemaVersion,
					Name:          "unproven-shell-exit",
					Settings:      DefaultWorkflowSettings(),
					Steps: []Step{
						{
							ID: "produce",
							Command: fmt.Sprintf(`printf 'once\n' >> launches
if [ ! -f release-leader ]; then
  sleep 30 >/dev/null 2>&1 &
  printf '%%s\n' "$!" > descendant.pid
  while [ ! -f release-leader ]; do sleep 0.01; done
fi
exit %d`, exitCode),
						},
						{ID: "consume", DependsOn: []string{"produce"}, Command: "printf unsafe > downstream"},
					},
				}
				frozen, path, err := SnapshotWorkflow(context.Background(), root, workflow)
				if err != nil {
					t.Fatal(err)
				}
				cfg.WorkflowFile = path
				stepHash, err := commandDefinitionHash(&frozen.Steps[0])
				if err != nil {
					t.Fatal(err)
				}
				startedAt := time.Now().UTC()
				exitFile := stepExitReceiptPath(root, cfg.RunID, "produce", startedAt)
				if err := os.MkdirAll(filepath.Dir(exitFile), 0o700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("/bin/sh", "-c", frozen.Steps[0].Command)
				cmd.Dir = root
				meta, err := startCommandWithContainment(context.Background(), cmd, commandStartOptions{
					RecordLaunchMetadata: true, ExitCodeFile: exitFile, StepID: "produce", StepHash: stepHash,
				})
				if err != nil {
					t.Fatal(err)
				}
				// Simulate losing the supervisor: no owned-group waiter survives.
				// Hold a separate child handle solely for deterministic test cleanup.
				_, _ = takeCommandContainmentHandle(cmd)
				leaderWaited := false
				t.Cleanup(func() {
					if !leaderWaited {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				childPID := waitForPIDFile(t, filepath.Join(root, "descendant.pid"))
				child, err := os.FindProcess(childPID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = child.Kill()
					_ = child.Release()
					if !waitForPIDGone(childPID, 2*time.Second) {
						t.Errorf("test descendant %d did not exit", childPID)
					}
				})
				if meta.Tier != ContainmentTierProcPGID || meta.LeaderStartTimeTicks == 0 {
					t.Fatalf("fixture lacks running fallback leader evidence: %+v", meta)
				}
				prior := &ExecutionState{
					RunID: cfg.RunID, WorkflowID: frozen.Name, WorkflowFile: path,
					Session: cfg.Session, Status: StatusRunning, StartedAt: startedAt, UpdatedAt: startedAt,
					Steps: map[string]StepResult{},
					InFlightSteps: map[string]InFlightStepState{
						"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: startedAt},
					},
					CommandExecutions: map[string]CommandExecutionState{
						"produce": {
							Version: commandExecutionVersion, StepID: "produce", StepHash: stepHash,
							Status: commandExecutionRunning, ProcessID: cmd.Process.Pid,
							LeaderPID: meta.LeaderPID, LeaderStartTimeTicks: meta.LeaderStartTimeTicks,
							ContainmentTier: tier.tier, ExitCodeFile: exitFile, StartedAt: startedAt,
						},
					},
				}
				if err := SaveState(root, prior); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "release-leader"), []byte("exit"), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				leaderWaited = true
				if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != exitCode {
					t.Fatalf("leader did not exit as requested: %v", cmd.ProcessState)
				}
				receipt, err := readStepExitReceipt(exitFile)
				if err != nil || receipt.ExitCode != exitCode {
					t.Fatalf("shell did not publish its natural exit receipt: %+v %v", receipt, err)
				}
				before := commandOperatorRecoveryBytes(t, cfg)
				loaded, err := LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := NewExecutor(cfg).Resume(context.Background(), frozen, loaded, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
					t.Fatalf("resume accepted shell exit without descendant settlement: %v", err)
				}
				if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
					t.Fatal("rejected resume changed the original running receipt")
				}
				if err := child.Signal(syscall.Signal(0)); err != nil {
					t.Fatalf("resume harmed the still-running descendant: %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
					t.Fatalf("resume repeated the original side effect: %q %v", data, err)
				}
				if _, err := os.Stat(filepath.Join(root, "downstream")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("resume released dependent work before descendant settlement: %v", err)
				}
				if exitCode == 0 {
					// The ordinary explicit recovery surface remains available after
					// an operator has independently verified the descendant ended.
					if err := child.Kill(); err != nil || !waitForPIDGone(childPID, 2*time.Second) {
						t.Fatalf("could not finish descendant for explicit recovery: %v", err)
					}
					entry := commandOperatorRecoveryEntry(t, cfg)
					if _, err := AcceptCommandExecutionContext(context.Background(), root, cfg.RunID, entry.StepID,
						entry.ReceiptToken, "Verified the original background descendant ended", 0, ""); err != nil {
						t.Fatal(err)
					}
					resolved, err := LoadState(root, cfg.RunID)
					if err != nil {
						t.Fatal(err)
					}
					// This is new downstream work, independent of the old
					// fallback receipt. Use containment that does not require
					// globally complete procfs visibility in restricted hosts.
					t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)
					resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer resumeCancel()
					final, err := NewExecutor(cfg).Resume(resumeCtx, frozen, resolved, nil)
					if err != nil || final == nil || final.Status != StatusCompleted {
						t.Fatalf("resume after explicit recovery failed: %+v %v", final, err)
					}
					if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
						t.Fatalf("explicit recovery repeated the original side effect: %q %v", data, err)
					}
					if data, err := os.ReadFile(filepath.Join(root, "downstream")); err != nil || string(data) != "unsafe" {
						t.Fatalf("explicit recovery did not release dependent work: %q %v", data, err)
					}
				}
			})
		}
	}
}

func TestCrashOnlyReceiptReconciliationOnResume(t *testing.T) {
	t.Run("reconciles_completed_step_after_orchestrator_crash", func(t *testing.T) {
		t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)

		root := t.TempDir()
		cfg := DefaultExecutorConfig("crash-reconcile")
		cfg.ProjectDir = root
		cfg.RunID = "run-crash-reconcile"

		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "crash-reconcile",
			Settings:      DefaultWorkflowSettings(),
			Steps: []Step{
				{
					ID:          "produce",
					Command:     `printf 'once\n' >> launches; sleep 0.20; printf '{"answer":42}'`,
					OutputVar:   "produced_json",
					OutputParse: OutputParse{Type: "json"},
				},
				{
					ID:        "consume",
					DependsOn: []string{"produce"},
					Command:   `printf '%s' '${steps.produce.data.answer}' > consumed.txt`,
				},
			},
		}

		// Launch step `produce` and snapshot the durable `running` checkpoint
		// while the child is still executing (simulating parent SIGKILL before
		// executeCommand can record settlement!).
		e := NewExecutor(cfg)
		e.state = &ExecutionState{
			RunID:      cfg.RunID,
			WorkflowID: workflow.Name,
			Session:    cfg.Session,
			Status:     StatusRunning,
			StartedAt:  time.Now(),
			Steps:      map[string]StepResult{},
			Variables:  map[string]interface{}{},
			InFlightSteps: map[string]InFlightStepState{
				"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: time.Now()},
			},
		}

		var crashedSnapshot *ExecutionState
		startedCh := make(chan struct{})
		go func() {
			defer close(startedCh)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				loaded, err := LoadState(root, cfg.RunID)
				if err == nil {
					if rec, ok := loaded.CommandExecutions["produce"]; ok && rec.Status == commandExecutionRunning && rec.LeaderPID > 0 && rec.LeaderStartTimeTicks > 0 {
						crashedSnapshot = loaded
						return
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		liveRes := e.executeCommand(context.Background(), &workflow.Steps[0], workflow)
		<-startedCh
		if liveRes.Status != StatusCompleted {
			t.Fatalf("live step failed: %+v", liveRes)
		}
		if crashedSnapshot == nil {
			t.Fatal("did not capture mid-flight running checkpoint before step finished")
		}

		rec := crashedSnapshot.CommandExecutions["produce"]
		if rec.LeaderPID <= 0 || rec.LeaderStartTimeTicks == 0 || rec.ExitCodeFile == "" {
			t.Fatalf("running checkpoint missing crash-only metadata: %+v", rec)
		}
		// Overwrite on-disk state with the interrupted mid-flight snapshot so
		// Resume sees the exact post-SIGKILL checkpoint (`Status: "running"`).
		if err := SaveState(root, crashedSnapshot); err != nil {
			t.Fatalf("restore crashed checkpoint: %v", err)
		}
		reloadedPrior, err := LoadState(root, cfg.RunID)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}

		resumedState, err := NewExecutor(cfg).Resume(context.Background(), workflow, reloadedPrior, nil)
		if err != nil || resumedState.Status != StatusCompleted {
			t.Fatalf("Resume failed to auto-reconcile completed step: state=%+v err=%v", resumedState, err)
		}

		// Step `produce` MUST NOT have been executed a second time.
		launches, err := os.ReadFile(filepath.Join(root, "launches"))
		if err != nil || string(launches) != "once\n" {
			t.Fatalf("produce step was re-executed instead of reconciled: launches=%q err=%v", string(launches), err)
		}
		consumed, err := os.ReadFile(filepath.Join(root, "consumed.txt"))
		if err != nil || string(consumed) != "42" {
			t.Fatalf("downstream step did not receive parsed output from reconciled step: got=%q err=%v", string(consumed), err)
		}
		if resumedState.Variables["produced_json"] != `{"answer":42}` {
			t.Fatalf("missing OutputVar from reconciled receipt: %#v", resumedState.Variables)
		}
		settledRec := resumedState.CommandExecutions["produce"]
		if settledRec.Status != commandExecutionSettled || !settledRec.Reconciled {
			t.Fatalf("expected settled & Reconciled=true receipt, got %+v", settledRec)
		}
	})

	t.Run("reconciles_cgroup_v2_step_after_orchestrator_crash", func(t *testing.T) {
		requireWritableCgroupV2OrSkip(t)
		t.Setenv(ContainmentTierEnv, ContainmentTierCgroupV2)

		root := t.TempDir()
		cfg := DefaultExecutorConfig("crash-reconcile-cg2")
		cfg.ProjectDir = root
		cfg.RunID = "run-crash-reconcile-cg2"

		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "crash-reconcile-cg2",
			Settings:      DefaultWorkflowSettings(),
			Steps: []Step{
				{
					ID:      "produce",
					Command: `printf 'once\n' >> launches; sleep 0.20; printf 'artifact-ready' > artifact.txt`,
				},
				{
					ID:        "consume",
					DependsOn: []string{"produce"},
					Command:   `cat artifact.txt > verified.txt`,
				},
			},
		}

		e := NewExecutor(cfg)
		e.state = &ExecutionState{
			RunID:      cfg.RunID,
			WorkflowID: workflow.Name,
			Session:    cfg.Session,
			Status:     StatusRunning,
			StartedAt:  time.Now(),
			Steps:      map[string]StepResult{},
			Variables:  map[string]interface{}{},
			InFlightSteps: map[string]InFlightStepState{
				"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: time.Now()},
			},
		}

		var crashedSnapshot *ExecutionState
		startedCh := make(chan struct{})
		go func() {
			defer close(startedCh)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				loaded, err := LoadState(root, cfg.RunID)
				if err == nil {
					if rec, ok := loaded.CommandExecutions["produce"]; ok && rec.Status == commandExecutionRunning && rec.CgroupPath != "" {
						crashedSnapshot = loaded
						return
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		liveRes := e.executeCommand(context.Background(), &workflow.Steps[0], workflow)
		<-startedCh
		if liveRes.Status != StatusCompleted {
			t.Fatalf("live cgroup_v2 step failed: %+v", liveRes)
		}
		if crashedSnapshot == nil {
			t.Fatal("did not capture mid-flight cgroup_v2 running checkpoint")
		}
		if err := SaveState(root, crashedSnapshot); err != nil {
			t.Fatal(err)
		}
		reloadedPrior, err := LoadState(root, cfg.RunID)
		if err != nil {
			t.Fatal(err)
		}

		resumedState, err := NewExecutor(cfg).Resume(context.Background(), workflow, reloadedPrior, nil)
		if err != nil || resumedState.Status != StatusCompleted {
			t.Fatalf("Resume failed to reconcile cgroup_v2 step: state=%+v err=%v", resumedState, err)
		}
		launches, err := os.ReadFile(filepath.Join(root, "launches"))
		if err != nil || string(launches) != "once\n" {
			t.Fatalf("cgroup_v2 produce step re-executed on Resume: %q %v", string(launches), err)
		}
		verified, err := os.ReadFile(filepath.Join(root, "verified.txt"))
		if err != nil || string(verified) != "artifact-ready" {
			t.Fatalf("downstream step did not observe artifact: %q %v", string(verified), err)
		}
	})

	t.Run("rejects_pid_reuse_start_time_ticks_mismatch", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cfg := DefaultExecutorConfig("pid-reuse")
		cfg.ProjectDir = root
		cfg.RunID = "run-pid-reuse"

		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "pid-reuse",
			Settings:      DefaultWorkflowSettings(),
			Steps:         []Step{{ID: "work", Command: "printf replayed > replayed.txt"}},
		}
		stepHash, err := commandDefinitionHash(&workflow.Steps[0])
		if err != nil {
			t.Fatal(err)
		}

		// Spawn an unrelated live process occupying `reusedPID`.
		unrelated := exec.Command("/bin/sleep", "30")
		if err := unrelated.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = unrelated.Process.Kill()
			_ = unrelated.Wait()
		})
		actualTicks, _, err := readProcessStartTimeTicks(unrelated.Process.Pid)
		if err != nil {
			t.Fatalf("readProcessStartTimeTicks: %v", err)
		}

		startedAt := time.Now().Add(-2 * time.Second).UTC()
		exitFile := stepExitReceiptPath(root, cfg.RunID, "work", startedAt)
		// Write a forged/stale exit receipt with mismatched start_time_ticks.
		if err := writeStepExitReceiptAtomic(exitFile, StepExitReceipt{
			Version:              stepExitReceiptVersion,
			StepID:               "work",
			StepHash:             stepHash,
			LeaderPID:            unrelated.Process.Pid,
			LeaderStartTimeTicks: actualTicks, // differs from recordedLeaderTicks below!
			ExitCode:             0,
			FinishedAt:           time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}

		prior := &ExecutionState{
			RunID:      cfg.RunID,
			WorkflowID: workflow.Name,
			Session:    cfg.Session,
			Status:     StatusRunning,
			StartedAt:  startedAt,
			UpdatedAt:  startedAt,
			Steps:      map[string]StepResult{},
			CommandExecutions: map[string]CommandExecutionState{
				"work": {
					Version:              commandExecutionVersion,
					StepID:               "work",
					StepHash:             stepHash,
					Status:               commandExecutionRunning,
					ProcessID:            unrelated.Process.Pid,
					LeaderPID:            unrelated.Process.Pid,
					LeaderStartTimeTicks: actualTicks + 99999, // PID reuse mismatch!
					ContainmentTier:      ContainmentTierSubreaperPIDFD,
					ExitCodeFile:         exitFile,
					StartedAt:            startedAt,
				},
			},
		}

		_, resumeErr := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
		if resumeErr == nil || !strings.Contains(resumeErr.Error(), "unresolved launch") {
			t.Fatalf("expected Resume to reject mismatched start_time_ticks receipt, got err=%v", resumeErr)
		}
		// Ensure the unrelated live process sharing the numeric PID was NOT killed.
		if err := syscall.Kill(unrelated.Process.Pid, 0); err != nil {
			t.Fatalf("unrelated process with reused PID was harmed during Resume: %v", err)
		}
	})

	t.Run("refuses_replay_while_leader_still_alive_or_cgroup_populated", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cfg := DefaultExecutorConfig("still-alive")
		cfg.ProjectDir = root
		cfg.RunID = "run-still-alive"

		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "still-alive",
			Settings:      DefaultWorkflowSettings(),
			Steps:         []Step{{ID: "work", Command: "printf unsafe > unsafe.txt"}},
		}
		stepHash, err := commandDefinitionHash(&workflow.Steps[0])
		if err != nil {
			t.Fatal(err)
		}

		liveChild := exec.Command("/bin/sleep", "30")
		if err := liveChild.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = liveChild.Process.Kill()
			_ = liveChild.Wait()
		})
		liveTicks, _, err := readProcessStartTimeTicks(liveChild.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now().Add(-time.Second).UTC()
		prior := &ExecutionState{
			RunID:      cfg.RunID,
			WorkflowID: workflow.Name,
			Session:    cfg.Session,
			Status:     StatusRunning,
			StartedAt:  startedAt,
			UpdatedAt:  startedAt,
			Steps:      map[string]StepResult{},
			CommandExecutions: map[string]CommandExecutionState{
				"work": {
					Version:              commandExecutionVersion,
					StepID:               "work",
					StepHash:             stepHash,
					Status:               commandExecutionRunning,
					ProcessID:            liveChild.Process.Pid,
					LeaderPID:            liveChild.Process.Pid,
					LeaderStartTimeTicks: liveTicks,
					ContainmentTier:      ContainmentTierCgroupV2,
					StartedAt:            startedAt,
				},
			},
		}

		_, resumeErr := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
		if resumeErr == nil || !strings.Contains(resumeErr.Error(), "still running") {
			t.Fatalf("expected still-running error when leader matches (pid, start_time_ticks), got %v", resumeErr)
		}
		if err := syscall.Kill(liveChild.Process.Pid, 0); err != nil {
			t.Fatalf("healthy still-running leader was killed by Resume: %v", err)
		}

		// Kill the leader, but present a cgroup.events reporting `populated 1`.
		_ = liveChild.Process.Kill()
		_ = liveChild.Wait()

		fakeCgDir := filepath.Join(t.TempDir(), "step-cg")
		if err := os.MkdirAll(fakeCgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeCgDir, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		exitFile := stepExitReceiptPath(root, cfg.RunID, "work", startedAt)
		if err := writeStepExitReceiptAtomic(exitFile, StepExitReceipt{
			Version:   stepExitReceiptVersion,
			StepID:    "work",
			StepHash:  stepHash,
			LeaderPID: liveChild.Process.Pid,
			ExitCode:  0,
		}); err != nil {
			t.Fatal(err)
		}

		priorCg := *prior
		priorCg.CommandExecutions = map[string]CommandExecutionState{
			"work": {
				Version:              commandExecutionVersion,
				StepID:               "work",
				StepHash:             stepHash,
				Status:               commandExecutionRunning,
				ProcessID:            liveChild.Process.Pid,
				LeaderPID:            liveChild.Process.Pid,
				LeaderStartTimeTicks: liveTicks,
				ContainmentTier:      ContainmentTierCgroupV2,
				CgroupPath:           fakeCgDir,
				ExitCodeFile:         exitFile,
				StartedAt:            startedAt,
			},
		}

		_, cgErr := NewExecutor(cfg).Resume(context.Background(), workflow, &priorCg, nil)
		if cgErr == nil || !strings.Contains(cgErr.Error(), "populated 1") {
			t.Fatalf("expected populated-cgroup refusal, got %v", cgErr)
		}

		// Once cgroup.events transitions to `populated 0`, Resume reconciles cleanly!
		if err := os.WriteFile(filepath.Join(fakeCgDir, "cgroup.events"), []byte("populated 0\nfrozen 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		resumed, okErr := NewExecutor(cfg).Resume(context.Background(), workflow, &priorCg, nil)
		if okErr != nil || resumed.Status != StatusCompleted {
			t.Fatalf("expected Resume to reconcile once populated 0, got state=%+v err=%v", resumed, okErr)
		}
		if _, err := os.Stat(filepath.Join(root, "unsafe.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reconciled step should not have re-executed: %v", err)
		}
	})
}

func TestContainmentStructuredDiagnostics(t *testing.T) {
	var logBuf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	t.Setenv(ContainmentTierEnv, ContainmentTierSubreaperPIDFD)
	cmd := exec.Command("/bin/sh", "-c", "(:)& exit 0")
	configureCommandProcessGroup(cmd)
	if _, err := startCommandWithContainment(context.Background(), cmd, commandStartOptions{}); err != nil {
		t.Fatalf("startCommandWithContainment: %v", err)
	}
	cleanup := waitCommandWithProcessGroupCleanup(context.Background(), cmd)
	if !cleanup.Settled || cleanup.Err != nil {
		t.Fatalf("cleanup failed: %+v", cleanup)
	}

	logged := logBuf.String()
	for _, field := range []string{`"containment_tier":"subreaper_pidfd"`, `"settlement_latency_us"`, `"reaped_descendants"`} {
		if !strings.Contains(logged, field) {
			t.Fatalf("expected structured slog.Debug diagnostic %s in %s", field, logged)
		}
	}
}

func TestCgroupObservationRetriesAndPreservesFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failure error
		recover bool
		live    bool
	}{
		{name: "transient read failure", failure: syscall.EACCES, recover: true},
		{name: "observer cancellation is not caller cancellation", failure: context.Canceled, recover: true},
		{name: "healthy watch replaces prior failure", failure: syscall.EIO, recover: true, live: true},
		{name: "permanent uncertainty", failure: syscall.EMFILE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			leaf := t.TempDir()
			eventsPath := filepath.Join(leaf, "cgroup.events")
			if err := os.WriteFile(eventsPath, []byte("populated 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			watching := make(chan struct{})
			calls := 0
			ctx = context.WithValue(ctx, commandCgroupObservationTestHookKey{}, func(observationCtx context.Context, observedLeaf string) error {
				calls++
				if observedLeaf != leaf {
					t.Errorf("observer changed cgroup identity: %q", observedLeaf)
				}
				if calls <= 2 || !tc.recover {
					return tc.failure
				}
				if calls == 3 {
					close(watching)
				}
				// Exercise the production inotify watcher after recovery,
				// including its initial read when the update races registration.
				return observeCgroupPopulatedZero(observationCtx, observedLeaf)
			})
			done := make(chan error, 1)
			go func() { done <- waitCgroupPopulatedZero(ctx, leaf) }()
			if tc.recover {
				select {
				case <-watching:
				case err := <-done:
					t.Fatalf("observer abandoned the live cgroup: %v", err)
				}
				if !tc.live {
					// Change only the value byte. Avoid a truncate/write gap
					// that would introduce an unrelated malformed-file race.
					file, err := os.OpenFile(eventsPath, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, writeErr := file.WriteAt([]byte("0"), 10)
					closeErr := file.Close()
					if err := errors.Join(writeErr, closeErr); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := <-done
			if calls < 3 {
				t.Fatalf("cgroup inspection was not retried: calls=%d err=%v", calls, err)
			}
			switch {
			case tc.recover && !tc.live:
				if err != nil {
					t.Fatalf("recovered cgroup failed to settle: %v", err)
				}
			case tc.live:
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tc.failure) {
					t.Fatalf("healthy event watch retained a recovered inspection error: %v", err)
				}
			default:
				if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, tc.failure) {
					t.Fatalf("uncertain cgroup lost its deadline or observation failure: %v", err)
				}
			}
		})
	}
}

func TestCgroupObservationFailureKeepsHealthyCommandAndOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `trap 'printf interrupted; exit 1' TERM
printf ready > ready
while [ ! -f release ]; do sleep 0.01; done
printf completed
printf effect > effect
`)
	capture := newCappedWriter(128)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, capture, capture
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cancelCommandProcessGroup(cmd)
			_ = cmd.Wait()
		}
	})
	awaitGroupTestFile(t, filepath.Join(dir, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := 0
	ctx = context.WithValue(ctx, commandCgroupObservationTestHookKey{}, func(observationCtx context.Context, _ string) error {
		calls++
		if calls <= 4 {
			if err := observeCommandExit(cmd.Process.Pid, syscall.WNOHANG); err != errCommandLeaderRunning {
				t.Errorf("observation failure stopped healthy work: %v", err)
			}
			return syscall.EMFILE
		}
		if err := os.WriteFile(filepath.Join(dir, "release"), []byte("finish"), 0o600); err != nil {
			return err
		}
		return waitCommandLeaderExit(observationCtx, cmd.Process)
	})
	// The command, output pipes, and signals are real. Only the cgroup
	// event source is injected so the lifecycle test needs no delegation.
	result := waitCommandWithCgroupV2(ctx, cmd, &linuxContainmentHandle{
		tier: ContainmentTierCgroupV2, leafDir: dir, pidfd: -1,
	})
	if !result.Settled || result.Cancelled || result.SignalSent != "" || result.Err != nil || calls < 5 ||
		capture.String() != "completed" || !capture.outputCaptureComplete() {
		t.Fatalf("observation failure disrupted healthy work: result=%+v calls=%d output=%q", result, calls, capture.String())
	}
	if data, err := os.ReadFile(filepath.Join(dir, "effect")); err != nil || string(data) != "effect" {
		t.Fatalf("healthy command did not finish its side effect: %q %v", data, err)
	}
}

func TestCgroupObservationFailurePreservesGraceAndSettlementOwnership(t *testing.T) {
	t.Parallel()
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprintf("settlement_recovers_%t", recover), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cmd := startGroupTestCommand(t, dir, `trap '' TERM
printf ready > ready
exec sleep 30
`)
			awaitGroupTestFile(t, filepath.Join(dir, "ready"))
			fault := errors.New("injected cgroup event observation failure")
			var firstSettlementContext context.Context
			recoveryObserved := false
			ctx, cancel := context.WithCancel(context.Background())
			ctx = context.WithValue(ctx, commandCgroupObservationTestHookKey{}, func(observationCtx context.Context, _ string) error {
				if err := observeCommandExit(cmd.Process.Pid, syscall.WNOHANG); err != nil {
					if err != errCommandLeaderRunning {
						return err
					}
					return fault
				}
				if firstSettlementContext == nil {
					firstSettlementContext = observationCtx
				}
				if observationCtx != firstSettlementContext {
					recoveryObserved = true
					if recover {
						return nil
					}
				}
				return fault
			})
			cancel()
			started := time.Now()
			result := waitCommandWithCgroupV2(ctx, cmd, &linuxContainmentHandle{
				tier: ContainmentTierCgroupV2, leafDir: dir, pidfd: -1,
			})
			elapsed := time.Since(started)
			if elapsed < commandCancelGracePeriod+commandPipeDrainTimeout ||
				elapsed > commandCancelGracePeriod+commandPipeDrainTimeout+commandSettlementRecoveryTimeout+3*time.Second {
				t.Fatalf("failed observation changed the full grace/recovery budget: elapsed=%v result=%+v", elapsed, result)
			}
			if !recoveryObserved || !result.Cancelled || result.Settled != recover || result.SignalSent != "SIGTERM,SIGKILL" ||
				!errors.Is(result.Err, context.Canceled) || !errors.Is(result.Err, fault) ||
				!strings.Contains(result.Err.Error(), "SIGTERM grace period") || cmd.ProcessState == nil {
				t.Fatalf("cgroup cleanup lost uncertainty, grace, or settlement ownership: recovery=%t result=%+v", recoveryObserved, result)
			}
			if !recover && !strings.Contains(result.Err.Error(), "cgroup settlement recovery exhausted") {
				t.Fatalf("permanent uncertainty was not retained for operator recovery: %+v", result)
			}
		})
	}
}

func TestCgroupSignalChecksMembershipAfterOpeningHandle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		members     []int
		openErr     error
		readErr     error
		signalErr   error
		wantErr     error
		wantSignal  bool
		wantSuccess bool
	}{
		{name: "owned member", members: []int{41, 42}, wantSignal: true, wantSuccess: true},
		{name: "PID recycled outside cgroup", members: []int{41, 43}},
		{name: "process already exited", openErr: syscall.ESRCH},
		{name: "pidfd unsupported", openErr: syscall.ENOSYS, wantErr: syscall.ENOSYS},
		{name: "pidfd denied", openErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "membership unknown", readErr: syscall.EACCES, wantErr: syscall.EACCES},
		{name: "member exits after identity check", members: []int{42}, signalErr: syscall.ESRCH, wantSignal: true},
		{name: "signal denied", members: []int{42}, signalErr: syscall.EPERM, wantErr: syscall.EPERM, wantSignal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opened, inspected, signaled, closed := false, false, false, false
			ops := cgroupProcessSignalOps{
				openPidfd: func(pid, flags int) (int, error) {
					if pid != 42 || flags != 0 || inspected {
						t.Fatalf("wrong identity acquisition order: pid=%d flags=%d inspected=%t", pid, flags, inspected)
					}
					opened = true
					return 71, tc.openErr
				},
				readMembers: func(leaf string) ([]int, error) {
					if !opened || leaf != "/owned-cgroup" || signaled {
						t.Fatalf("membership was not inspected with a bound handle: opened=%t leaf=%q signaled=%t", opened, leaf, signaled)
					}
					inspected = true
					return tc.members, tc.readErr
				},
				signalPidfd: func(fd int, sig unix.Signal, _ *unix.Siginfo, flags int) error {
					if !inspected || fd != 71 || sig != unix.SIGTERM || flags != 0 {
						t.Fatalf("signal was not bound to the verified handle: inspected=%t fd=%d sig=%d flags=%d", inspected, fd, sig, flags)
					}
					signaled = true
					return tc.signalErr
				},
				closePidfd: func(fd int) error {
					if fd != 71 || closed {
						t.Fatalf("wrong handle release: fd=%d closed=%t", fd, closed)
					}
					closed = true
					return nil
				},
			}
			success, err := signalCgroupProcessWithOps("/owned-cgroup", 42, unix.SIGTERM, ops)
			if !errors.Is(err, tc.wantErr) || success != tc.wantSuccess || signaled != tc.wantSignal || closed != (tc.openErr == nil) {
				t.Fatalf("unsafe cgroup signal outcome: success=%t signal=%t closed=%t err=%v", success, signaled, closed, err)
			}
		})
	}
}

func TestCgroupSignalLeavesStaleCandidateAlive(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// This test owns its unreaped direct child even on a host that
		// restricts pidfd syscalls, so cleanup can use the pinned PID.
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	probeFD, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM) {
		t.Skipf("real pidfd delivery unavailable on host: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	probeErr := unix.PidfdSendSignal(probeFD, 0, nil, 0)
	_ = unix.Close(probeFD)
	if errors.Is(probeErr, syscall.ENOSYS) || errors.Is(probeErr, syscall.EPERM) {
		t.Skipf("real pidfd delivery unavailable on host: %v", probeErr)
	}
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	leaf := t.TempDir()
	procs := filepath.Join(leaf, "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := cgroupProcessSignalOps{
		readMembers: readCgroupProcs,
		openPidfd: func(pid, flags int) (int, error) {
			fd, err := unix.PidfdOpen(pid, flags)
			if err == nil {
				// Model stale enumeration: the process bound by the handle
				// no longer belongs to the owned cgroup at inspection.
				if writeErr := os.WriteFile(procs, nil, 0o600); writeErr != nil {
					_ = unix.Close(fd)
					return -1, writeErr
				}
			}
			return fd, err
		},
		signalPidfd: unix.PidfdSendSignal,
		closePidfd:  unix.Close,
	}
	if signaled, err := signalCgroupProcessWithOps(leaf, cmd.Process.Pid, unix.SIGKILL, ops); err != nil || signaled {
		t.Fatalf("stale cgroup candidate was signaled: signaled=%t err=%v", signaled, err)
	}
	if err := observeCommandExit(cmd.Process.Pid, syscall.WNOHANG); err != errCommandLeaderRunning {
		t.Fatalf("unrelated live process did not survive stale cgroup membership: %v", err)
	}
	// Also exercise real delivery to a freshly verified member.
	if err := os.WriteFile(procs, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if signaled, err := signalCgroupProcess(leaf, cmd.Process.Pid, unix.SIGTERM); err != nil || !signaled {
		t.Fatalf("verified cgroup member was not signaled: signaled=%t err=%v", signaled, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitCommandLeaderExit(ctx, cmd.Process); err != nil {
		t.Fatalf("verified member did not receive its termination signal: %v", err)
	}
}
