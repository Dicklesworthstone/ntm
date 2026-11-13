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
