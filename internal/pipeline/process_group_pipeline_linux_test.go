package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	for _, phase := range []string{"live_child", "final_census"} {
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
				if (phase == "live_child" || quietSeen) && failures.Load() < 3 {
					if failures.Add(1) == 3 {
						close(injectedAll)
					}
					return false, injected
				}
				alive, err := commandGroupHasLiveMembers(pgid)
				if err == nil && !alive {
					quietSeen = true
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
					t.Error("fault-injected pipeline did not finish cleanup")
				}
			})
			awaitGroupTestFile(t, filepath.Join(root, "child.pid"))
			if phase == "final_census" {
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
		})
	}
}

func TestControlledPipelinePersistentFinalObservationPreservesUncertainty(t *testing.T) {
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
				{ID: "gate", DependsOn: []string{work.ID}, Command: "while [ ! -f final.observation ]; do sleep 0.01; done; printf ready"},
			}}
			cfg := DefaultExecutorConfig("unknown-observation")
			cfg.ProjectDir, cfg.RunID = root, "run-unknown-observation"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var target atomic.Int64
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(pid int) { target.CompareAndSwap(0, int64(pid)) })
			injected := errors.New("injected persistent final census failure")
			var failures atomic.Int32
			quietSeen := false
			ctx = context.WithValue(ctx, commandGroupObservationTestHookKey{}, func(pgid int) (bool, error) {
				if int64(pgid) != target.Load() {
					return commandGroupHasLiveMembers(pgid)
				}
				if quietSeen {
					if failures.Add(1) == 1 {
						// WaitNone must reach its final census before the gate
						// completes and finalization cancels background lifetimes.
						if err := os.WriteFile(filepath.Join(root, "final.observation"), []byte("observed"), 0600); err != nil {
							return false, errors.Join(injected, err)
						}
					}
					return false, injected
				}
				alive, err := commandGroupHasLiveMembers(pgid)
				if err == nil && !alive {
					quietSeen = true
				}
				return alive, err
			})
			events := make(chan ProgressEvent, 100)
			started := time.Now()
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, events)
			if elapsed := time.Since(started); elapsed > 6*time.Second {
				t.Fatalf("persistent final observation exceeded its cleanup bound: %v", elapsed)
			}
			if err == nil || state == nil || state.Status != StatusFailed || failures.Load() < 2 {
				t.Fatalf("persistent uncertainty became success or skipped retries: %+v %v failures=%d", state, err, failures.Load())
			}
			assertNoCompletionEvent(t, events)
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.CommandExecutions[work.ID]
			if receipt.Status != commandExecutionUnresolved || !receipt.SettledAt.IsZero() || receipt.Background != background ||
				receipt.Result.Status != StatusFailed || receipt.Result.Output != "captured" || receipt.Result.Error == nil ||
				!strings.Contains(receipt.Result.Error.Details, injected.Error()) || receipt.Result.FinishedAt.IsZero() || receipt.Result.RerunOnResume {
				t.Fatalf("ended supervision lost uncertainty or granted replay: %+v", receipt)
			}
			if !background && (prior.Steps[work.ID].Attempts != 1 || prior.Steps[work.ID].Error.Type != "execution_unknown") {
				t.Fatalf("uncertain foreground execution was retried or disguised as an exit error: %+v", prior.Steps[work.ID])
			}
			entry := commandOperatorRecoveryEntry(t, cfg)
			if entry.Status != commandExecutionUnresolved || entry.CanAccept == background {
				t.Fatalf("recovery inspection misclassified unresolved command: %+v", entry)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
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
