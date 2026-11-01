package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Exercise the foreground CLI/robot entry rather than only the OS helper:
// external cancellation must keep exclusive ownership through descendant
// cleanup, then allow another owner without any old command still executing.
func TestControlledPipelineCancellationOwnsCommandDescendants(t *testing.T) {
	root := t.TempDir()
	groupTestFile(t, root, "child.sh", `trap '' TERM
printf ready > child.ready
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "owned-descendants", Steps: []Step{
		{ID: "command", Command: "trap 'exit 0' TERM; /bin/sh child.sh >/dev/null 2>&1 & wait"},
	}}
	cfg := DefaultExecutorConfig("owned-descendants")
	cfg.ProjectDir, cfg.RunID = root, "run-owned-descendants"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type outcome struct {
		state *ExecutionState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
		done <- outcome{state, err}
	}()
	awaitGroupTestFile(t, filepath.Join(root, "child.ready"))
	if err := RequestRunCancellation(ctx, root, cfg.RunID); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(ctx, root, cfg.RunID)
	if owner != nil {
		owner.Close()
		t.Error("run released ownership before TERM-ignoring child settled")
	} else if !errors.Is(err, ErrRunAlreadyOwned) {
		t.Errorf("unexpected ownership error during cleanup: %v", err)
	}
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("cancelled workflow failed to finish cleanup")
	}
	if result.err == nil || result.state == nil || result.state.Status != StatusCancelled {
		t.Fatalf("cancelled foreground run: %+v %v", result.state, result.err)
	}
	owner, err = AcquireRunControl(ctx, root, cfg.RunID)
	if err != nil {
		t.Fatalf("settled run did not release ownership: %v", err)
	}
	defer owner.Close()
	groupTestFile(t, root, "release", "new owner working")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old descendant performed work during new ownership: %v", err)
	}
}

func TestControlledPipelineObservationRetriesReconcileReceipts(t *testing.T) {
	for _, phase := range []string{"live_child", "quiet_group"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			groupTestFile(t, root, "child.sh", `printf '%s' "$$" > child.pid
while [ ! -f release ]; do sleep 0.01; done
printf complete > child.effect
printf child
`)
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "retry-observation", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "produce", Command: "printf 'once\\n' >> launches; /bin/sh child.sh & printf leader-"},
				{ID: "consume", DependsOn: []string{"produce"}, Command: "test -f resume-allowed || exit 7; cat child.effect"},
			}}
			cfg := DefaultExecutorConfig("retry-observation")
			cfg.ProjectDir, cfg.RunID = root, "run-retry-observation"
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			injected := errors.New("injected transient pipeline census failure")
			var failures atomic.Int32
			injectedAll := make(chan struct{})
			quietSeen := false // Only the target command's waiter accesses this.
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				if int64(pgid) != target.Load() {
					return commandGroupHasLiveMembers(pgid)
				}
				if phase == "quiet_group" && !quietSeen {
					alive, err := commandGroupHasLiveMembers(pgid)
					if err != nil || alive {
						return alive, err
					}
					// Withhold the first otherwise-quiet certificate. Once a
					// complete certificate is accepted, no second census is
					// needed to settle naturally completed work.
					quietSeen = true
				}
				if failures.Load() < 3 {
					if failures.Add(1) == 3 {
						close(injectedAll)
					}
					return false, injected
				}
				return commandGroupHasLiveMembers(pgid)
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
				cancel()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Error("fault-injected pipeline did not finish cleanup")
				}
			})
			awaitGroupTestFile(t, filepath.Join(root, "child.pid"))
			if phase == "quiet_group" {
				groupTestFile(t, root, "release", "finish")
			}
			select {
			case <-injectedAll:
			case early := <-done:
				t.Fatalf("pipeline ended on transient observation failure: %+v %v", early.state, early.err)
			case <-time.After(5 * time.Second):
				t.Fatal("pipeline did not retry the observation failures")
			}
			if phase == "live_child" {
				prior, err := LoadState(root, cfg.RunID)
				if err != nil || prior.CommandExecutions["produce"].Status != commandExecutionRunning {
					t.Fatalf("observation failures ended healthy command supervision: %+v %v", prior, err)
				}
				if _, err := os.Stat(filepath.Join(root, "child.effect")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("fixture child completed before its release: %v", err)
				}
				groupTestFile(t, root, "release", "finish")
			}
			var initial outcome
			select {
			case initial = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("recovered observation did not settle the pipeline")
			}
			if initial.err == nil || initial.state == nil || initial.state.Status != StatusFailed ||
				initial.state.Steps["produce"].Status != StatusCompleted || initial.state.Steps["consume"].Error == nil ||
				initial.state.Steps["consume"].Error.Type != "exit" {
				t.Fatalf("pipeline did not reach its intentional downstream failure: %+v %v", initial.state, initial.err)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions["produce"]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() ||
				receipt.Result.Status != StatusCompleted || receipt.Result.Output != "leader-child" || receipt.Result.Error != nil || failures.Load() != 3 {
				t.Fatalf("transient failure stranded or corrupted the completed receipt: %+v failures=%d", receipt, failures.Load())
			}
			groupTestFile(t, root, "resume-allowed", "continue")
			final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps["consume"].Output != "complete" {
				t.Fatalf("ordinary resume did not reconcile the completed producer: %+v %v", final, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("receipt reconciliation repeated producer effects: %q %v", data, err)
			}
			checkpointPath := pipelineStatePath(root, cfg.RunID)
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(checkpointPath, old, old); err != nil {
				t.Fatal(err)
			}
			report, err := CleanupStates(context.Background(), root, 24*time.Hour, false)
			if err != nil || report.Deleted != 1 || report.Retained != 0 {
				t.Fatalf("reconciled receipt permanently blocked safe cleanup: %+v %v", report, err)
			}
			if _, err := os.Stat(checkpointPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("settled checkpoint was not removed: %v", err)
			}
			if _, err := os.Stat(prior.WorkflowFile); err != nil {
				t.Fatalf("cleanup removed the shared workflow snapshot: %v", err)
			}
		})
	}
}

func TestControlledPipelineLeaderObservationRecoveryKeepsOwnershipAndReceipts(t *testing.T) {
	requireCommandPIDFDObservation(t)
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background_%t", background), func(t *testing.T) {
			root := t.TempDir()
			groupTestFile(t, root, "child.sh", `trap 'printf signalled > child.signalled; exit 19' TERM
printf ready > child.ready
while [ ! -f child.release ]; do sleep 0.01; done
printf complete > child.effect
printf child-
`)
			work := Step{ID: "work", Command: "printf 'once\\n' >> launches; /bin/sh child.sh & wait; printf leader"}
			if background {
				work.Wait = WaitNone
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "leader-recovery", Settings: DefaultWorkflowSettings(), Steps: []Step{
				work,
				{ID: "consume", DependsOn: []string{work.ID}, Command: "printf started > consume.started; while [ ! -f consume.release ]; do sleep 0.01; done; test -f resume-allowed || exit 7; printf consumed"},
			}}
			cfg := DefaultExecutorConfig("leader-recovery")
			cfg.ProjectDir, cfg.RunID = root, "run-leader-recovery"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			var recovered atomic.Bool
			outageHeld := make(chan struct{})
			var outageStarted time.Time
			announced := false // Accessed only by the target command's waiter.
			fault := errors.New("injected leader observation outage beyond the old retry and reaper budgets")
			ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
				if int64(pid) != target.Load() || recovered.Load() {
					return observeCommandExit(pid, syscall.WNOHANG)
				}
				if outageStarted.IsZero() {
					outageStarted = time.Now()
				}
				if !announced && time.Since(outageStarted) > commandLeaderObservationRetryTimeout+commandPipeDrainTimeout+2*commandGroupPollInterval {
					announced = true
					close(outageHeld)
				}
				return fault
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				recovered.Store(true)
				_ = os.WriteFile(filepath.Join(root, "child.release"), []byte("finish"), 0600)
				_ = os.WriteFile(filepath.Join(root, "consume.release"), []byte("finish"), 0600)
				cancel()
				select {
				case <-done:
				case <-time.After(commandCancelGracePeriod + commandSettlementRecoveryTimeout + 4*commandPipeDrainTimeout):
					t.Error("leader-observation recovery did not join its command cleanup")
				}
			})
			awaitGroupTestFile(t, filepath.Join(root, "child.ready"))
			select {
			case <-outageHeld:
			case early := <-done:
				t.Fatalf("leader inspection failure abandoned healthy work: %+v %v", early.state, early.err)
			case <-time.After(5 * time.Second):
				t.Fatal("leader inspection stopped retrying while healthy work remained owned")
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			pending := prior.CommandExecutions[work.ID]
			if pending.Status != commandExecutionRunning || !pending.SettledAt.IsZero() || pending.ProcessID != int(target.Load()) {
				t.Fatalf("healthy leader outage prematurely ended its receipt: %+v", pending)
			}
			for _, name := range []string{"child.signalled", "child.effect"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("inspection failure advanced or terminated gated work (%s): %v", name, err)
				}
			}
			if !background {
				if _, err := os.Stat(filepath.Join(root, "consume.started")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("leader uncertainty dispatched foreground dependents: %v", err)
				}
			}
			owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
			if owner != nil {
				owner.Close()
				t.Fatal("healthy leader outage released exclusive run ownership")
			}
			if !errors.Is(err, ErrRunAlreadyOwned) {
				t.Fatalf("unexpected ownership error during leader recovery: %v", err)
			}
			recovered.Store(true)
			groupTestFile(t, root, "child.release", "finish")
			settled := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				prior, err = LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if prior.CommandExecutions[work.ID].Status == commandExecutionSettled {
					settled = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			receipt := prior.CommandExecutions[work.ID]
			if !settled || receipt.SettledAt.IsZero() || receipt.ProcessID != pending.ProcessID ||
				receipt.Result.Status != StatusCompleted || receipt.Result.Error != nil || receipt.Background != background {
				t.Fatalf("recovered leader observation stranded or corrupted its receipt: %+v", receipt)
			}
			if !background && receipt.Result.Output != "child-leader" {
				t.Fatalf("leader recovery lost captured descendant output: %q", receipt.Result.Output)
			}
			if string(awaitGroupTestFile(t, filepath.Join(root, "child.effect"))) != "complete" {
				t.Fatal("healthy child did not finish its work after observation recovered")
			}
			groupTestFile(t, root, "consume.release", "finish")
			var initial outcome
			select {
			case initial = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("settled leader recovery did not release the run")
			}
			if initial.err == nil || initial.state == nil || initial.state.Status != StatusFailed ||
				initial.state.Steps["consume"].Error == nil || initial.state.Steps["consume"].Error.Type != "exit" {
				t.Fatalf("run did not reach the intentional downstream failure: %+v %v", initial.state, initial.err)
			}
			prior, err = LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			groupTestFile(t, root, "resume-allowed", "continue")
			final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps["consume"].Output != "consumed" {
				t.Fatalf("ordinary resume could not use the recovered receipt: %+v %v", final, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("leader recovery replayed healthy command effects: %q %v", data, err)
			}
		})
	}
}

func TestWaitNoneExplicitTimeoutWithUnknownSettlementRefusesReplay(t *testing.T) {
	e := newCommandTestExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	fault := errors.New("injected unknown background timeout settlement")
	ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(int) (bool, error) { return false, fault })
	step := Step{ID: "sidecar", Wait: WaitNone, Timeout: Duration{Duration: time.Second},
		Command: "printf 'once\\n' >> launches; trap 'printf shutdown; exit 0' TERM; while :; do sleep 0.01; done"}
	workflow := Workflow{Name: "uncertain-timeout"}
	result := e.executeCommand(ctx, &step, &workflow)
	e.stateMu.Lock()
	e.state.Steps[step.ID] = result
	e.stateMu.Unlock()
	done := make(chan struct{})
	go func() { e.backgroundCommandWG.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("unknown background timeout did not finish bounded cleanup")
		}
	})
	if result.Status != StatusCompleted {
		t.Fatalf("sidecar did not acknowledge launch: %+v", result)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("explicit timeout failed to finish its observation budgets")
	}
	record, ok := e.loadCommandExecution(step.ID)
	if !ok || record.Status != commandExecutionUnresolved || !record.SettledAt.IsZero() || record.Result.RerunOnResume ||
		record.Result.Status != StatusFailed || record.Result.Error == nil || record.Result.Error.Type != "timeout" ||
		!strings.Contains(record.Result.Output, "shutdown") || !strings.Contains(record.Result.Error.Details, fault.Error()) {
		t.Fatalf("timeout fabricated settlement or lost its output and uncertainty: %+v", record)
	}
	if err := e.backgroundCommandFailure(); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, fault) {
		t.Fatalf("background outcome lost its deadline or failed inspection: %v", err)
	}
	replayed := e.executeCommand(context.Background(), &step, &workflow)
	if replayed.Status != StatusFailed || replayed.Error == nil || replayed.Error.Type != "execution_unknown" {
		t.Fatalf("uncertain background timeout authorized replay: %+v", replayed)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "launches")); err != nil || string(data) != "once\n" {
		t.Fatalf("uncertain timeout duplicated command effects: %q %v", data, err)
	}
}

func TestControlledPipelinePersistentObservationCancellationPreservesUncertainty(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background_%t", background), func(t *testing.T) {
			root := t.TempDir()
			work := Step{ID: "uncertain", Command: "printf 'once\\n' >> launches; printf captured",
				OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}}
			if background {
				work.Wait = WaitNone
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "unknown-observation", Settings: DefaultWorkflowSettings(), Steps: []Step{
				work,
				{ID: "gate", DependsOn: []string{work.ID}, Command: "exec sleep 30"},
			}}
			cfg := DefaultExecutorConfig("unknown-observation")
			cfg.ProjectDir, cfg.RunID = root, "run-unknown-observation"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			injected := errors.New("injected persistent census failure through cancellation")
			var failures atomic.Int32
			quietSeen := false
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				if int64(pgid) != target.Load() {
					return commandGroupHasLiveMembers(pgid)
				}
				if !quietSeen {
					alive, err := commandGroupHasLiveMembers(pgid)
					if err != nil || alive {
						return alive, err
					}
					quietSeen = true
				}
				if failures.Add(1) == 1 {
					// The observation is unknown before any quiet certificate
					// is admitted. Only genuine cancellation ends supervision;
					// the same fault must survive grace and post-kill retries.
					cancel()
				}
				return false, injected
			})
			events := make(chan ProgressEvent, 100)
			started := time.Now()
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, events)
			minimum := commandCancelGracePeriod + commandPipeDrainTimeout + commandSettlementRecoveryTimeout
			if elapsed := time.Since(started); elapsed < minimum || elapsed > minimum+3*time.Second {
				t.Fatalf("persistent observation shortened its grace/recovery budgets or exceeded its cleanup bound: %v (minimum %v)", elapsed, minimum)
			}
			if err == nil || state == nil || state.Status != StatusCancelled || failures.Load() < 2 {
				t.Fatalf("persistent uncertainty became success or skipped retries: %+v %v failures=%d", state, err, failures.Load())
			}
			assertNoCompletionEvent(t, events)
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions[work.ID]
			if receipt.Status != commandExecutionUnresolved || !receipt.SettledAt.IsZero() || receipt.Background != background ||
				receipt.Result.Status != StatusCancelled || receipt.Result.Output != "captured" || receipt.Result.Error == nil ||
				!strings.Contains(receipt.Result.Error.Details, injected.Error()) || receipt.Result.FinishedAt.IsZero() || receipt.Result.RerunOnResume {
				t.Fatalf("ended supervision lost uncertainty or granted replay: %+v", receipt)
			}
			if !background && (prior.Steps[work.ID].Attempts != 1 || prior.Steps[work.ID].Error.Type != "cancelled") {
				t.Fatalf("uncertain foreground execution was retried or disguised as an exit error: %+v", prior.Steps[work.ID])
			}
			entry := commandOperatorRecoveryEntry(t, cfg)
			if entry.Status != commandExecutionUnresolved || entry.CanAccept == background {
				t.Fatalf("recovery inspection misclassified unresolved command: %+v", entry)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(pipelineStatePath(root, cfg.RunID), old, old); err != nil {
				t.Fatal(err)
			}
			report, err := CleanupStates(context.Background(), root, 24*time.Hour, false)
			if err != nil || report.Deleted != 0 || report.Retained != 1 || len(report.Entries) != 1 || report.Entries[0].Reason != "unsettled_command" {
				t.Fatalf("old uncertain termination lost its replay fence during cleanup: %+v %v", report, err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("cleanup altered unresolved command evidence")
			}
			if _, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil); err == nil || !strings.Contains(err.Error(), "already has state") {
				t.Fatalf("cleanup authorized a new launch with an unresolved run ID: %v", err)
			}
			if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
				t.Fatalf("ordinary resume replayed uncertain work: %v", err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected resume rewrote unresolved command evidence")
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("uncertain command was launched more than once: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineCensusOwnershipLossPreservesRecoveryEvidence(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, ownershipErr := range []error{syscall.ECHILD, syscall.ESRCH} {
			t.Run(fmt.Sprintf("background_%t/%v", background, ownershipErr), func(t *testing.T) {
				root := t.TempDir()
				groupTestFile(t, root, "child.sh", `printf '%s' "$$" > child.pid
while [ ! -f release ]; do sleep 0.01; done
printf complete > child.effect
`)
				work := Step{ID: "work", Command: "printf 'once\\n' >> launches; /bin/sh child.sh </dev/null >/dev/null 2>&1 & printf captured",
					OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}}
				if background {
					work.Wait = WaitNone
				}
				workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "lost-census-ownership", Settings: DefaultWorkflowSettings(), Steps: []Step{
					work,
					{ID: "gate", DependsOn: []string{work.ID}, Command: "exec sleep 30"},
				}}
				cfg := DefaultExecutorConfig("lost-census-ownership")
				cfg.ProjectDir, cfg.RunID = root, "run-lost-census-ownership"
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				var target atomic.Int64
				var armed, ownershipLost atomic.Bool
				var lostProbes atomic.Int32
				censusErr := errors.New("injected unrelated procfs read failure before ownership loss")
				ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
				ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
					if int64(pid) == target.Load() && ownershipLost.Load() {
						lostProbes.Add(1)
						return ownershipErr
					}
					return observeCommandExit(pid, syscall.WNOHANG)
				})
				ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
					if int64(pgid) == target.Load() && armed.Load() && ownershipLost.CompareAndSwap(false, true) {
						// The leader has exited and the waiter has begun its group
						// census. Faults before this boundary would exercise only
						// the initial ownership probe, not cancellation admission.
						cancel()
						return false, censusErr
					}
					return commandGroupHasLiveMembers(pgid)
				})
				type outcome struct {
					state *ExecutionState
					err   error
				}
				done := make(chan outcome, 1)
				go func() {
					state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
					done <- outcome{state, err}
					close(done)
				}()
				t.Cleanup(func() {
					_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
					cancel()
					select {
					case <-done:
					case <-time.After(commandCancelGracePeriod + 4*commandPipeDrainTimeout):
						t.Error("pipeline did not finish after injected ownership loss")
					}
				})
				childPID := waitForPIDFile(t, filepath.Join(root, "child.pid"))
				child, err := os.FindProcess(childPID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
				// Pin the fixture's own child handle before allowing cancellation;
				// never use the waiter's saved group number for fixture cleanup.
				armed.Store(true)
				var initial outcome
				select {
				case initial = <-done:
				case <-time.After(commandCancelGracePeriod + 4*commandPipeDrainTimeout):
					t.Fatal("lost ownership did not return an unresolved outcome")
				}
				if !errors.Is(initial.err, context.Canceled) || initial.state == nil || initial.state.Status != StatusCancelled || lostProbes.Load() != 1 {
					t.Fatalf("cancellation failed to recheck or retain lost ownership: %+v %v probes=%d", initial.state, initial.err, lostProbes.Load())
				}
				prior, err := LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				receipt := prior.CommandExecutions[work.ID]
				if receipt.Status != commandExecutionUnresolved || !receipt.SettledAt.IsZero() || receipt.Background != background ||
					receipt.ProcessID != int(target.Load()) || receipt.Result.Status != StatusCancelled || receipt.Result.Output != "captured" ||
					receipt.Result.Error == nil || !strings.Contains(receipt.Result.Error.Details, ownershipErr.Error()) ||
					!strings.Contains(receipt.Result.Error.Details, censusErr.Error()) ||
					receipt.Result.FinishedAt.IsZero() || receipt.Result.RerunOnResume {
					t.Fatalf("ownership loss published settlement or lost cancellation evidence: %+v", receipt)
				}
				if !background && (prior.Steps[work.ID].Attempts != 1 || prior.Steps[work.ID].Error == nil || prior.Steps[work.ID].Error.Type != "cancelled") {
					t.Fatalf("unresolved foreground command was retried or misclassified: %+v", prior.Steps[work.ID])
				}
				entry := commandOperatorRecoveryEntry(t, cfg)
				if entry.Status != commandExecutionUnresolved || entry.CanAccept == background {
					t.Fatalf("operator recovery misclassified ownership uncertainty: %+v", entry)
				}
				before := commandOperatorRecoveryBytes(t, cfg)
				if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
					t.Fatalf("ordinary resume replayed work after losing its group identity: %v", err)
				}
				if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
					t.Fatal("rejected resume rewrote the original unresolved evidence")
				}
				if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
					t.Fatalf("uncertain command was dispatched again: %q %v", data, err)
				}
				// An ownership fault must not grant permission to signal a numeric
				// group. The independently held child still completes on release,
				// even after the pipeline and its refused recovery have returned.
				groupTestFile(t, root, "release", "finish")
				if got := string(awaitGroupTestFile(t, filepath.Join(root, "child.effect"))); got != "complete" {
					t.Fatalf("ownership failure terminated healthy descendant work: %q", got)
				}
				if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
					t.Fatal("late descendant completion rewrote unresolved recovery evidence")
				}
			})
		}
	}
}

func TestControlledPipelineLateSettlementRecoveryKeepsOwnershipAndReceipts(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background_%t", background), func(t *testing.T) {
			root := t.TempDir()
			work := Step{ID: "work", Command: "printf 'once\\n' >> launches; printf captured"}
			if background {
				work.Wait = WaitNone
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "late-settlement", Settings: DefaultWorkflowSettings(), Steps: []Step{
				work,
				{ID: "gate", DependsOn: []string{work.ID}, Command: "test -f resume-allowed || exec sleep 30; printf consumed"},
			}}
			cfg := DefaultExecutorConfig("late-settlement")
			cfg.ProjectDir, cfg.RunID = root, "run-late-settlement"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			fault := errors.New("injected census outage outlasting the first settlement budget")
			recovering := make(chan struct{})
			var release atomic.Bool
			phase := 0 // Only the target command's waiter accesses these fields.
			var firstSettlementCheck time.Time
			announcedRecovery := false
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				if int64(pgid) != target.Load() {
					return commandGroupHasLiveMembers(pgid)
				}
				if phase < 2 {
					alive, err := commandGroupHasLiveMembers(pgid)
					if err != nil || alive {
						return alive, err
					}
					phase++
					if phase == 1 {
						// Cancel during the first real quiet observation so the
						// waiter enters cancellation instead of natural completion.
						// The next real quiet proof finishes the SIGTERM grace.
						cancel()
					}
					return false, nil
				}
				if release.Load() {
					return commandGroupHasLiveMembers(pgid)
				}
				if firstSettlementCheck.IsZero() {
					firstSettlementCheck = time.Now()
				}
				if !announcedRecovery && time.Since(firstSettlementCheck) > commandPipeDrainTimeout+2*commandGroupPollInterval {
					announcedRecovery = true
					close(recovering)
				}
				return false, fault
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				release.Store(true)
				cancel()
				select {
				case <-done:
				case <-time.After(commandCancelGracePeriod + commandSettlementRecoveryTimeout + 4*commandPipeDrainTimeout):
					t.Error("pipeline did not join its late settlement recovery")
				}
			})
			select {
			case <-recovering:
			case early := <-done:
				t.Fatalf("first failed settlement check permanently ended supervision: %+v %v", early.state, early.err)
			case <-time.After(8 * time.Second):
				t.Fatal("command did not enter late settlement recovery")
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			pending := prior.CommandExecutions[work.ID]
			if pending.Status != commandExecutionRunning || !pending.SettledAt.IsZero() || pending.ProcessID != int(target.Load()) {
				t.Fatalf("receipt claimed settlement or lost its identity while recovery remained uncertain: %+v", pending)
			}
			owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
			if owner != nil {
				owner.Close()
				t.Fatal("run released exclusive ownership before late settlement recovery finished")
			}
			if !errors.Is(err, ErrRunAlreadyOwned) {
				t.Fatalf("unexpected ownership failure during late settlement recovery: %v", err)
			}
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(pipelineStatePath(root, cfg.RunID), old, old); err != nil {
				t.Fatal(err)
			}
			report, err := CleanupStates(context.Background(), root, 24*time.Hour, false)
			if err != nil || report.Deleted != 0 || report.Retained != 1 || len(report.Entries) != 1 || report.Entries[0].Reason != "owned" {
				t.Fatalf("cleanup discarded the checkpoint during late settlement recovery: %+v %v", report, err)
			}
			release.Store(true)
			var initial outcome
			select {
			case initial = <-done:
			case <-time.After(commandSettlementRecoveryTimeout + 3*commandPipeDrainTimeout):
				t.Fatal("recovered observation did not settle the receipt before releasing ownership")
			}
			if !errors.Is(initial.err, context.Canceled) || initial.state == nil || initial.state.Status != StatusCancelled {
				t.Fatalf("late settlement lost the original cancellation: %+v %v", initial.state, initial.err)
			}
			prior, err = LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions[work.ID]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() || receipt.Result.Status != StatusCancelled ||
				receipt.Result.Output != "captured" || receipt.ProcessID != pending.ProcessID || receipt.Background != background {
				t.Fatalf("late proof failed to reconcile the exact cancelled receipt and captured output: %+v", receipt)
			}
			owner, err = AcquireRunControl(context.Background(), root, cfg.RunID)
			if err != nil {
				t.Fatalf("settled recovery did not release run ownership: %v", err)
			}
			owner.Close()
			groupTestFile(t, root, "resume-allowed", "continue")
			final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps["gate"].Output != "consumed" {
				t.Fatalf("ordinary resume did not use the reconciled settlement: %+v %v", final, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\nonce\n" {
				t.Fatalf("resuming proved cancellation did not launch exactly one replacement attempt: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineCancellationFaultsSettleReceiptAndResume(t *testing.T) {
	root := t.TempDir()
	groupTestFile(t, root, "child.sh", `trap '' TERM
printf '%s' "$$" > child.pid
while [ ! -f release ]; do sleep 0.05; done
printf orphan > late-effect
`)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "cancel-observation", Settings: DefaultWorkflowSettings(), Steps: []Step{
		{ID: "work", Command: "printf 'attempt\\n' >> launches; if [ -f resume-allowed ]; then printf resumed; exit 0; fi; /bin/sh child.sh >/dev/null 2>&1 & printf leader",
			OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}},
	}}
	cfg := DefaultExecutorConfig("cancel-observation")
	cfg.ProjectDir, cfg.RunID = root, "run-cancel-observation"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	graceFailure := errors.New("injected live-group census failure during cancellation grace")
	finalFailure := errors.New("injected transient post-kill census failure")
	observedLive := make(chan struct{})
	var liveFailures, quietObservations atomic.Int32
	ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
		alive, err := commandGroupHasLiveMembers(pgid)
		if err != nil {
			return alive, err
		}
		if alive {
			if liveFailures.Add(1) == 1 {
				close(observedLive)
			}
			return false, graceFailure
		}
		if quietObservations.Add(1) <= 3 {
			return false, finalFailure
		}
		return false, nil
	})
	type outcome struct {
		state *ExecutionState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
		done <- outcome{state, err}
		close(done)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
		cancel()
		select {
		case <-done:
		case <-time.After(commandCancelGracePeriod + 4*commandPipeDrainTimeout):
			t.Error("cancelled pipeline did not join its fault-injected cleanup")
		}
	})
	childPID := waitForPIDFile(t, filepath.Join(root, "child.pid"))
	select {
	case <-observedLive:
	case early := <-done:
		t.Fatalf("observation failure ended healthy work before cancellation: %+v %v", early.state, early.err)
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline never observed its live TERM-ignoring child")
	}
	cancelledAt := time.Now()
	cancel()
	var initial outcome
	select {
	case initial = <-done:
	case <-time.After(commandCancelGracePeriod + 4*commandPipeDrainTimeout):
		t.Fatal("fault-injected cancellation did not finish within its cleanup bound")
	}
	if elapsed := time.Since(cancelledAt); elapsed < commandCancelGracePeriod {
		t.Fatalf("observation failure shortened the SIGTERM grace period: %v", elapsed)
	}
	if !errors.Is(initial.err, context.Canceled) || initial.state == nil || initial.state.Status != StatusCancelled ||
		liveFailures.Load() < 2 || quietObservations.Load() < 4 {
		t.Fatalf("cancellation did not retry both observation phases: %+v %v live=%d quiet=%d", initial.state, initial.err, liveFailures.Load(), quietObservations.Load())
	}
	prior, err := LoadState(root, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := prior.CommandExecutions["work"]
	if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() || receipt.Result.Status != StatusCancelled ||
		receipt.Result.Output != "leader" || receipt.Result.Error == nil || receipt.Result.Error.Type != "cancelled" ||
		!strings.Contains(receipt.Result.Error.Details, graceFailure.Error()) || !strings.Contains(receipt.Result.Error.Details, "SIGTERM grace period") {
		t.Fatalf("cancelled receipt lost settlement or the grace-period failure: %+v", receipt)
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPID)); err == nil {
		group, state, parseErr := parseCommandGroupStat(data, childPID)
		if parseErr != nil || (group == receipt.ProcessID && state != "Z" && state != "X") {
			t.Fatalf("TERM-ignoring child remained live after settled cancellation: group=%d state=%q err=%v", group, state, parseErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect cancelled fixture child: %v", err)
	}
	groupTestFile(t, root, "release", "old child must be gone")
	groupTestFile(t, root, "resume-allowed", "continue")
	final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
	if err != nil || final.Status != StatusCompleted || final.Steps["work"].Output != "resumed" {
		t.Fatalf("proven cancellation could not resume normally: %+v %v", final, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "attempt\nattempt\n" {
		t.Fatalf("ordinary resume did not launch exactly one new attempt: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled child produced an effect after receipt settlement: %v", err)
	}
}

func TestControlledPipelineReceiptsUnderProcessChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("repeated real process-group lifecycles under bounded process churn")
	}
	for iteration := 0; iteration < 8; iteration++ {
		cancelWork := iteration%2 != 0
		t.Run(fmt.Sprintf("iteration_%d/cancel_%t", iteration, cancelWork), func(t *testing.T) {
			root := t.TempDir()
			groupTestFile(t, root, "child.sh", `trap 'printf handled > handled; exit 0' TERM
printf ready > child.ready
while [ ! -f release ]; do sleep 0.01; done
printf complete > child.effect
`)
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "churn-receipts", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "work", Command: "printf 'once\\n' >> launches; /bin/sh child.sh >/dev/null 2>&1 & printf leader"},
			}}
			cfg := DefaultExecutorConfig("churn-receipts")
			cfg.ProjectDir, cfg.RunID = root, "run-churn-receipts"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			observedLive := make(chan struct{})
			var liveCensuses, churn atomic.Int32
			var firstChurnCount int32
			observedChurn := false
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				alive, err := commandGroupHasLiveMembers(pgid)
				if launches := churn.Load(); err == nil && alive && launches > 0 {
					liveCensuses.Add(1)
					if firstChurnCount == 0 {
						firstChurnCount = launches
					} else if launches > firstChurnCount && !observedChurn {
						observedChurn = true
						close(observedLive)
					}
				}
				return alive, err
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
				cancel()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Error("pipeline did not join its command during churn")
				}
			})
			awaitGroupTestFile(t, filepath.Join(root, "child.ready"))
			// Arm churn only after the owned child is ready, then require
			// unrelated launches to advance between real live-group censuses.
			// A slow fixture setup cannot consume the entire churn budget.
			startPipelineReceiptChurn(t, &churn)
			select {
			case <-observedLive:
			case early := <-done:
				t.Fatalf("process churn ended healthy work: %+v %v", early.state, early.err)
			case <-time.After(5 * time.Second):
				t.Fatal("unrelated process launches did not advance between live-group censuses")
			}
			if cancelWork {
				cancel()
			} else {
				groupTestFile(t, root, "release", "finish")
			}
			var result outcome
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("command lifecycle did not settle during churn")
			}
			wantStatus := StatusCompleted
			if cancelWork {
				wantStatus = StatusCancelled
				if !errors.Is(result.err, context.Canceled) {
					t.Fatalf("cancellation was lost during churn: %v", result.err)
				}
				if got := string(awaitGroupTestFile(t, filepath.Join(root, "handled"))); got != "handled" {
					t.Fatalf("cooperative descendant lost its grace period: %q", got)
				}
				if _, err := os.Stat(filepath.Join(root, "child.effect")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("cancelled descendant completed its forbidden effect: %v", err)
				}
			} else if result.err != nil {
				t.Fatalf("process churn failed healthy work: %v", result.err)
			}
			if result.state == nil || result.state.Status != wantStatus {
				t.Fatalf("unexpected terminal pipeline state during churn: %+v %v", result.state, result.err)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions["work"]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() || receipt.Result.Status != wantStatus || receipt.Result.Output != "leader" {
				t.Fatalf("process churn stranded or corrupted the command receipt: %+v", receipt)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("process churn repeated command effects: %q %v", data, err)
			}
			t.Logf("settled after %d real live-group censuses and %d unrelated process launches", liveCensuses.Load(), churn.Load())
		})
	}
}

func TestControlledPipelineConcurrentChildrenUnderProcessChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("real concurrent descendants with staggered completion under bounded process churn")
	}
	for _, cancelWork := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_%t", cancelWork), func(t *testing.T) {
			root := t.TempDir()
			groupTestFile(t, root, "child.sh", `id="$1"
trap 'printf handled > "child.$id.handled"; exit 0' TERM
printf '%s' "$$" > "child.$id.pid"
while [ ! -f "child.$id.release" ]; do sleep 0.01; done
printf complete > "child.$id.effect"
`)
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "concurrent-churn", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "work", Command: "printf 'once\\n' >> launches; /bin/sh child.sh 1 >/dev/null 2>&1 & /bin/sh child.sh 2 >/dev/null 2>&1 & /bin/sh child.sh 3 >/dev/null 2>&1 & printf leader"},
				{ID: "downstream", DependsOn: []string{"work"}, Command: "printf consumed > downstream; printf consumed"},
			}}
			cfg := DefaultExecutorConfig("concurrent-churn")
			cfg.ProjectDir, cfg.RunID = root, "run-concurrent-churn"
			cleanupBudget := commandCancelGracePeriod + commandSettlementRecoveryTimeout + 4*commandPipeDrainTimeout
			ctx, cancel := context.WithTimeout(context.Background(), 2*cleanupBudget)
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			observedChurn, afterFirst, afterSecond := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var churn, completedChildren, liveCensuses atomic.Int32
			var firstChurnCount int32
			churnSeen, firstSeen, secondSeen := false, false, false // Target waiter only.
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				alive, err := commandGroupHasLiveMembers(pgid)
				if int64(pgid) != target.Load() || err != nil || !alive {
					return alive, err
				}
				liveCensuses.Add(1)
				if launches := churn.Load(); launches > 0 {
					if firstChurnCount == 0 {
						firstChurnCount = launches
					} else if launches > firstChurnCount && !churnSeen {
						churnSeen = true
						close(observedChurn)
					}
				}
				if completedChildren.Load() >= 1 && !firstSeen {
					firstSeen = true
					close(afterFirst)
				}
				if completedChildren.Load() >= 2 && !secondSeen {
					secondSeen = true
					close(afterSecond)
				}
				return alive, nil
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				for id := 1; id <= 3; id++ {
					_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("child.%d.release", id)), []byte("finish"), 0600)
				}
				cancel()
				select {
				case <-done:
				case <-time.After(cleanupBudget):
					t.Error("concurrent descendants did not finish bounded cleanup")
				}
			})
			pids := make([]int, 3)
			for id := 1; id <= 3; id++ {
				pids[id-1] = waitForPIDFile(t, filepath.Join(root, fmt.Sprintf("child.%d.pid", id)))
			}
			startPipelineReceiptChurn(t, &churn)
			awaitLive := func(observed <-chan struct{}, phase string) {
				t.Helper()
				select {
				case <-observed:
				case early := <-done:
					t.Fatalf("pipeline ended while %s still had gated children: %+v %v", phase, early.state, early.err)
				case <-time.After(5 * commandPipeDrainTimeout):
					t.Fatalf("pipeline did not observe live children during %s", phase)
				}
				state, err := LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				receipt := state.CommandExecutions["work"]
				if receipt.Status != commandExecutionRunning || !receipt.SettledAt.IsZero() {
					t.Fatalf("pipeline settled with gated siblings during %s: %+v", phase, receipt)
				}
				if _, err := os.Stat(filepath.Join(root, "downstream")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("downstream ran before all siblings finished during %s: %v", phase, err)
				}
			}
			awaitLive(observedChurn, "unrelated process churn")
			groupTestFile(t, root, "child.1.release", "finish first")
			awaitGroupTestFile(t, filepath.Join(root, "child.1.effect"))
			completedChildren.Store(1)
			awaitLive(afterFirst, "first child completion")
			if cancelWork {
				cancel()
			} else {
				groupTestFile(t, root, "child.2.release", "finish second")
				awaitGroupTestFile(t, filepath.Join(root, "child.2.effect"))
				completedChildren.Store(2)
				awaitLive(afterSecond, "second child completion")
				groupTestFile(t, root, "child.3.release", "finish last")
			}
			var final outcome
			select {
			case final = <-done:
			case <-time.After(cleanupBudget):
				t.Fatal("concurrent command group did not reach bounded settlement")
			}
			wantStatus := StatusCompleted
			if cancelWork {
				wantStatus = StatusCancelled
				if !errors.Is(final.err, context.Canceled) {
					t.Fatalf("concurrent cancellation lost its original cause: %v", final.err)
				}
				for id := 2; id <= 3; id++ {
					if got := string(awaitGroupTestFile(t, filepath.Join(root, fmt.Sprintf("child.%d.handled", id)))); got != "handled" {
						t.Fatalf("child %d lost its SIGTERM grace: %q", id, got)
					}
					groupTestFile(t, root, fmt.Sprintf("child.%d.release", id), "cancelled child must be gone")
					if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("child.%d.effect", id))); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("cancelled child %d performed its gated effect: %v", id, err)
					}
				}
				if _, err := os.Stat(filepath.Join(root, "downstream")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("cancelled group dispatched downstream work: %v", err)
				}
			} else {
				if final.err != nil {
					t.Fatalf("concurrent normal completion failed: %v", final.err)
				}
				for id := 1; id <= 3; id++ {
					if got := string(awaitGroupTestFile(t, filepath.Join(root, fmt.Sprintf("child.%d.effect", id)))); got != "complete" {
						t.Fatalf("healthy child %d did not finish its work: %q", id, got)
					}
				}
				if got := string(awaitGroupTestFile(t, filepath.Join(root, "downstream"))); got != "consumed" {
					t.Fatalf("downstream lost settled command output: %q", got)
				}
			}
			if final.state == nil || final.state.Status != wantStatus {
				t.Fatalf("unexpected concurrent lifecycle result: %+v %v", final.state, final.err)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions["work"]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() || receipt.Result.Status != wantStatus || receipt.Result.Output != "leader" {
				t.Fatalf("concurrent lifecycle stranded or corrupted its receipt: %+v", receipt)
			}
			for _, pid := range pids {
				if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
					group, state, parseErr := parseCommandGroupStat(data, pid)
					if parseErr != nil || (group == receipt.ProcessID && state != "Z" && state != "X") {
						t.Fatalf("child %d remained live after group settlement: group=%d state=%q err=%v", pid, group, state, parseErr)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("inspect settled child %d: %v", pid, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("concurrent process churn duplicated command execution: %q %v", data, err)
			}
			t.Logf("settled three independent children after %d live censuses and %d unrelated launches", liveCensuses.Load(), churn.Load())
		})
	}
}

// At most one short-lived shell and its child run concurrently, and no test
// starts more than 64 churn commands. Stop between commands so cleanup does
// not create another batch of orphaned processes in a container's PID 1.
func startPipelineReceiptChurn(t *testing.T, launches *atomic.Int32) {
	t.Helper()
	stop, started := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go func() {
		var runErr error
		defer func() {
			cancel()
			done <- runErr
			close(done)
		}()
		for iteration := 0; iteration < 64; iteration++ {
			select {
			case <-stop:
				return
			default:
			}
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 0.01 & wait")
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				runErr = err
				return
			}
			if launches.Add(1) == 1 {
				close(started)
			}
			if err := cmd.Wait(); err != nil {
				runErr = err
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bounded process churn failed: %v", err)
			}
		case <-time.After(3 * time.Second):
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("bounded process churn did not exit")
			}
		}
	})
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("could not start bounded process churn: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("bounded process churn did not start")
	}
}

func TestControlledPipelineObservedBackgroundFailureSurvivesNormalShutdown(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			root := t.TempDir()
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "observed-background-exit", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "sidecar", Wait: WaitNone, Command: fmt.Sprintf("printf 'once\\n' >> launches; printf diagnostic; exit %d", exitCode)},
				{ID: "gate", DependsOn: []string{"sidecar"}, Command: "while [ ! -f gate.release ]; do sleep 0.01; done; printf consumed > gate.effect"},
			}}
			cfg := DefaultExecutorConfig("observed-background-exit")
			cfg.ProjectDir, cfg.RunID = root, "run-observed-background-exit"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			observedExit := make(chan struct{})
			held := false // Accessed only by the target command's waiter.
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(probeCtx context.Context, pgid int) (bool, error) {
				alive, err := commandGroupHasLiveMembers(pgid)
				if int64(pgid) != target.Load() || held || err != nil || alive {
					return alive, err
				}
				// The leader has already exited and its group is quiet. Hold
				// publication until finishExecution performs its routine
				// cancellation, making the outcome race deterministic.
				held = true
				close(observedExit)
				<-probeCtx.Done()
				return false, nil
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			events := make(chan ProgressEvent, 100)
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, events)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				_ = os.WriteFile(filepath.Join(root, "gate.release"), []byte("finish"), 0600)
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("observed background exit did not finish cleanup")
				}
			})
			select {
			case <-observedExit:
			case early := <-done:
				t.Fatalf("workflow ended before its observed sidecar exit was held: %+v %v", early.state, early.err)
			case <-ctx.Done():
				t.Fatal("sidecar never reached the held settlement observation")
			}
			pending, err := LoadState(root, cfg.RunID)
			if err != nil || pending.CommandExecutions["sidecar"].Status != commandExecutionRunning {
				t.Fatalf("sidecar published before the cancellation race: %+v %v", pending, err)
			}
			groupTestFile(t, root, "gate.release", "finish")
			var final outcome
			select {
			case final = <-done:
			case <-ctx.Done():
				t.Fatal("ordinary workflow shutdown failed to join its observed sidecar")
			}
			if ctx.Err() != nil || final.state == nil || final.state.Steps["gate"].Status != StatusCompleted {
				t.Fatalf("fixture did not finish by ordinary workflow shutdown: %+v %v", final.state, final.err)
			}
			if exitCode == 0 {
				if final.err != nil || final.state.Status != StatusCompleted {
					t.Fatalf("successful observed exit became a shutdown failure: %+v %v", final.state, final.err)
				}
			} else {
				var exitErr *exec.ExitError
				if final.state.Status != StatusFailed || !errors.As(final.err, &exitErr) || exitErr.ExitCode() != exitCode ||
					errors.Is(final.err, context.Canceled) || errors.Is(final.err, context.DeadlineExceeded) {
					t.Fatalf("normal shutdown erased or misclassified the prior failed exit: %+v %v", final.state, final.err)
				}
				assertNoCompletionEvent(t, events)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions["sidecar"]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() || receipt.Result.Output != "diagnostic" {
				t.Fatalf("observed sidecar exit lost its settled receipt or output: %+v", receipt)
			}
			if exitCode != 0 && (prior.Status != StatusFailed || receipt.Result.Status != StatusFailed || receipt.Result.Error == nil || !receipt.Result.RerunOnResume) {
				t.Fatalf("ordinary cleanup replaced the prior failure in durable state: %+v", receipt)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("shutdown race repeated background effects: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineHandledObservationFailurePreservesRecovery(t *testing.T) {
	requireCommandPIDFDObservation(t)
	for _, selector := range []bool{false, true} {
		t.Run(fmt.Sprintf("selector_%t", selector), func(t *testing.T) {
			root := t.TempDir()
			source := "printf '%s' \"$$\" > uncertain.pid; " +
				"while [ ! -f source.release ]; do sleep 0.01; done; " +
				"printf 'once\\n' >> source-effects; printf 'chosen\\n' > observed-output; cat observed-output"
			work := Step{ID: "uncertain", Command: source, OnError: ErrorActionContinue}
			if selector {
				work.Command, work.Branch = "", "$("+source+")"
				work.Branches = map[string]interface{}{"chosen": []Step{{
					ID: "body", Command: "printf 'once\\n' >> selected-effects; printf selected",
				}}}
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "handled-uncertainty", Settings: DefaultWorkflowSettings(), Steps: []Step{
				work,
				{ID: "independent", Command: "printf ready > independent.ready; while [ ! -f independent.release ]; do sleep 0.01; done; printf 'once\\n' >> independent-effects; printf continued"},
				{ID: "consume", DependsOn: []string{work.ID}, Command: "printf 'once\\n' >> dependent-effects; printf consumed"},
			}}
			workflow.Settings.Limits.MaxParallelSteps = 2
			cfg := DefaultExecutorConfig(workflow.Name)
			cfg.ProjectDir, cfg.RunID = root, "run-handled-uncertainty"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			fault := errors.New("injected loss of leader observation after its actual exit")
			var injected, observedExited atomic.Bool
			observing := make(chan struct{})
			ctx = context.WithValue(ctx, commandLeaderObservationTestHookKey{}, func(pid int) error {
				data, err := os.ReadFile(filepath.Join(root, "uncertain.pid"))
				if err == nil && strings.TrimSpace(string(data)) == fmt.Sprint(pid) && injected.CompareAndSwap(false, true) {
					close(observing)
					// Hold the original child unreaped until it really exits.
					// The fixture's foreground children finish before its shell;
					// this independent observation later supports acceptance.
					if err := observeCommandExit(pid, 0); err != nil {
						return err
					}
					observedExited.Store(true)
					return errors.Join(syscall.ECHILD, fault)
				}
				return observeCommandExit(pid, syscall.WNOHANG)
			})
			type outcome struct {
				state *ExecutionState
				err   error
			}
			events := make(chan ProgressEvent, 100)
			done := make(chan outcome, 1)
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, events)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "source.release"), []byte("finish"), 0600)
				_ = os.WriteFile(filepath.Join(root, "independent.release"), []byte("finish"), 0600)
				cancel()
				select {
				case <-done:
				case <-time.After(commandCancelGracePeriod + commandSettlementRecoveryTimeout + 4*commandPipeDrainTimeout):
					t.Error("handled observation failure did not join its command cleanup")
				}
			})
			awaitGroupTestFile(t, filepath.Join(root, "uncertain.pid"))
			awaitGroupTestFile(t, filepath.Join(root, "independent.ready"))
			select {
			case <-observing:
			case early := <-done:
				t.Fatalf("pipeline ended before the injected observation: %+v %v", early.state, early.err)
			case <-time.After(5 * time.Second):
				t.Fatal("source never reached its deterministic observation boundary")
			}
			groupTestFile(t, root, "source.release", "finish")

			// on_error: continue must let independently running work finish.
			// The terminal settlement gate runs only after that work joins.
			var pending *ExecutionState
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				saved, err := LoadState(root, cfg.RunID)
				if err == nil && saved.Steps[work.ID].Status == StatusFailed {
					pending = saved
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pending == nil || pending.Status != StatusRunning || ctx.Err() != nil || !observedExited.Load() {
				t.Fatalf("handled observation failure stopped independent work or missed the fault: %+v %v", pending, ctx.Err())
			}
			if _, err := os.Stat(filepath.Join(root, "independent-effects")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("gated independent command already ended: %v", err)
			}
			receiptBytes := func(state *ExecutionState) []byte {
				t.Helper()
				var receipt interface{} = state.CommandExecutions[work.ID]
				if selector {
					record := state.CommandEvaluations[work.ID]
					if record.Status != commandExecutionUnresolved || !strings.Contains(record.Error, fault.Error()) {
						t.Fatalf("selector lost unknown settlement evidence: %+v", record)
					}
					receipt = record
				} else {
					record := state.CommandExecutions[work.ID]
					if record.Status != commandExecutionUnresolved || !record.SettledAt.IsZero() ||
						record.Result.RerunOnResume || record.Result.Error == nil || !strings.Contains(record.Result.Error.Details, fault.Error()) {
						t.Fatalf("command lost unknown settlement evidence: %+v", record)
					}
				}
				data, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			unresolved := receiptBytes(pending)
			groupTestFile(t, root, "independent.release", "finish")
			var initial outcome
			select {
			case initial = <-done:
			case <-ctx.Done():
				t.Fatal("handled observation failure did not finish after independent work")
			}
			if initial.err == nil || initial.state == nil || initial.state.Status != StatusFailed ||
				initial.state.Steps["independent"].Status != StatusCompleted || initial.state.Steps["independent"].Output != "continued" {
				t.Fatalf("unknown settlement published success or interrupted independent work: %+v %v", initial.state, initial.err)
			}
			if selector && !errors.Is(initial.err, errCommandOutputUnsettled) {
				t.Fatalf("terminal outcome lost selector uncertainty: %v", initial.err)
			}
			if !selector && !strings.Contains(initial.err.Error(), "unresolved launch") {
				t.Fatalf("terminal outcome lost command uncertainty: %v", initial.err)
			}
			assertNoCompletionEvent(t, events)
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if prior.Status != StatusFailed || !bytes.Equal(unresolved, receiptBytes(prior)) {
				t.Fatal("terminal settlement gate changed the unresolved receipt or failed to persist failure")
			}
			diagnosticSaved := false
			for _, failure := range prior.Errors {
				if failure.Fatal && failure.Type == "execution" && failure.Message == initial.err.Error() {
					diagnosticSaved = true
				}
			}
			if !diagnosticSaved {
				t.Fatalf("terminal uncertainty was not available to later status inspection: %+v", prior.Errors)
			}
			for _, name := range []string{"dependent-effects", "selected-effects"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("uncertain source dispatched dependent work (%s): %v", name, err)
				}
			}
			completedIndependent := prior.Steps["independent"].FinishedAt
			before := commandOperatorRecoveryBytes(t, cfg)
			if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); err == nil {
				t.Fatal("ordinary resume replayed the unknown source")
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("refused ordinary resume changed the unresolved checkpoint")
			}

			output, err := os.ReadFile(filepath.Join(root, "observed-output"))
			if err != nil || string(output) != "chosen\n" {
				t.Fatalf("independently recorded complete source output: %q %v", output, err)
			}
			const reason = "Independently observed the original shell and its foreground children finish and verified complete output"
			if selector {
				inspection, err := InspectCommandEvaluationRecovery(context.Background(), root, cfg.RunID)
				if err != nil || len(inspection.Selectors) != 1 || !inspection.Selectors[0].CanAccept {
					t.Fatalf("handled uncertainty stranded selector recovery: %+v %v", inspection, err)
				}
				entry := inspection.Selectors[0]
				if _, err := AcceptCommandEvaluationContext(context.Background(), root, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, output); err != nil {
					t.Fatal(err)
				}
			} else {
				entry := commandOperatorRecoveryEntry(t, cfg)
				if !entry.CanAccept {
					t.Fatalf("handled uncertainty stranded command recovery: %+v", entry)
				}
				if _, err := AcceptCommandExecutionContext(context.Background(), root, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, string(output)); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer resumeCancel()
			final, err := NewExecutor(cfg).Resume(resumeCtx, workflow, resolved, nil)
			if err != nil || final == nil || final.Status != StatusCompleted || final.Steps["consume"].Output != "consumed" ||
				!final.Steps["independent"].FinishedAt.Equal(completedIndependent) {
				t.Fatalf("verified recovery did not preserve completed independent work: %+v %v", final, err)
			}
			names := []string{"source-effects", "independent-effects", "dependent-effects"}
			if selector {
				names = append(names, "selected-effects")
			}
			for _, name := range names {
				if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != "once\n" {
					t.Fatalf("recovery repeated or lost %s: %q %v", name, data, err)
				}
			}
		})
	}
}
