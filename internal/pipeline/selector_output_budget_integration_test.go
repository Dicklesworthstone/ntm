//go:build !windows

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func selectorBudgetExecutor(t *testing.T, limit int64) *Executor {
	t.Helper()
	e := newCommandTestExecutor(t)
	e.config.RunID = e.state.RunID
	e.state.Session = e.config.Session
	e.state.StartedAt, e.state.Status = time.Now(), StatusRunning
	e.limits = (LimitsConfig{MaxSelectorOutputBytesTotal: limit}).EffectiveLimits()
	return e
}

func selectorBudgetCommand(e *Executor, script string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = e.config.ProjectDir
	return cmd
}

func selectorBudgetEvaluate(ctx context.Context, e *Executor, id, script string, cap int64) ([]byte, *exec.Cmd, error) {
	step := &Step{ID: id, Branch: "$(" + script + ")"}
	cmd := selectorBudgetCommand(e, script)
	output, err := runCommandOutput(e.commandEvaluationContext(ctx, step), cmd, cap, 64)
	return output, cmd, err
}

func selectorBudgetNoEffect(t *testing.T, e *Executor, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unadmitted selector produced %s: %v", name, err)
	}
}

func TestSelectorOutputBudgetAdmissionPreservesExactBoundaryAndReceipts(t *testing.T) {
	e := selectorBudgetExecutor(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, _, err := selectorBudgetEvaluate(ctx, e, "first", "printf x >> first-effects; printf 1234", 4); err != nil || string(output) != "1234" {
		t.Fatalf("exact aggregate boundary was rejected: %q %v", output, err)
	}
	before := selectorRecoveryCheckpoint(t, e.config)
	output, cmd, err := selectorBudgetEvaluate(ctx, e, "second", "printf forbidden > second-effects; printf z", 1)
	if output != nil || !errors.Is(err, ErrSelectorOutputBudget) || cmd.Process != nil || ctx.Err() != nil {
		t.Fatalf("permanent budget refusal launched or waited for capacity: output=%q process=%v err=%v context=%v", output, cmd.Process, err, ctx.Err())
	}
	selectorBudgetNoEffect(t, e, "second-effects")
	if !bytes.Equal(before, selectorRecoveryCheckpoint(t, e.config)) {
		t.Fatal("budget refusal rewrote existing recovery evidence")
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil || len(prior.CommandEvaluations) != 1 || prior.CommandEvaluations["first"].StdoutLimit != 4 || string(prior.CommandEvaluations["first"].Output) != "1234" {
		t.Fatalf("budget refusal evicted output, shrank its cap, or invented a launch intent: %+v %v", prior, err)
	}
}

type selectorBudgetOutcome struct {
	output []byte
	err    error
}

func selectorBudgetAwait(t *testing.T, done <-chan selectorBudgetOutcome) selectorBudgetOutcome {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("selector budget waiter did not finish")
		return selectorBudgetOutcome{}
	}
}

func TestSelectorOutputBudgetConcurrentAdmissionWaitsAndCancels(t *testing.T) {
	for _, mode := range []string{"release", "cancel_waiter", "deadline_waiter"} {
		t.Run(mode, func(t *testing.T) {
			e := selectorBudgetExecutor(t, 8)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			release := filepath.Join(e.config.ProjectDir, "first-release")
			firstStep := &Step{ID: "first", Branch: "$(blocking source)"}
			first := selectorBudgetCommand(e, "printf ready > first-ready; while [ ! -f first-release ]; do sleep 0.01; done")
			firstDone := make(chan selectorBudgetOutcome, 1)
			go func() {
				output, err := runCommandOutput(e.commandEvaluationContext(ctx, firstStep), first, 8, 64)
				firstDone <- selectorBudgetOutcome{output, err}
				close(firstDone)
			}()
			t.Cleanup(func() {
				_ = os.WriteFile(release, []byte("finish"), 0600)
				cancel()
				selectorBudgetAwait(t, firstDone)
			})
			awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "first-ready"), "ready")
			var secondCtx context.Context
			var cancelSecond context.CancelFunc
			if mode == "deadline_waiter" {
				secondCtx, cancelSecond = context.WithTimeout(ctx, time.Second)
			} else {
				secondCtx, cancelSecond = context.WithCancel(ctx)
			}
			defer cancelSecond()
			secondStep := &Step{ID: "second", Branch: "$(next source)"}
			secondCtx = e.commandEvaluationContext(secondCtx, secondStep)
			second := selectorBudgetCommand(e, "printf once >> second-effects; printf second")
			secondDone := make(chan selectorBudgetOutcome, 1)
			go func() {
				output, err := runCommandOutput(secondCtx, second, 8, 64)
				secondDone <- selectorBudgetOutcome{output, err}
				close(secondDone)
			}()
			t.Cleanup(func() { cancelSecond(); selectorBudgetAwait(t, secondDone) })
			// This notification is allocated only when admission actually
			// registers a capacity wait. Observe it under the state lock so a
			// delayed goroutine cannot make an unentered call look blocked.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				e.stateMu.RLock()
				waiting := e.selectorOutputWake != nil
				e.stateMu.RUnlock()
				if waiting {
					break
				}
				select {
				case early := <-secondDone:
					t.Fatalf("temporary reservation was treated as permanent or allowed oversubscription: %+v", early)
				case <-ctx.Done():
					t.Fatal("second selector never registered a capacity wait")
				case <-ticker.C:
				}
			}
			selectorBudgetNoEffect(t, e, "second-effects")
			saved, err := LoadState(e.config.ProjectDir, e.config.RunID)
			if err != nil || len(saved.CommandEvaluations) != 1 || saved.CommandEvaluations["first"].Status != commandExecutionStarting {
				t.Fatalf("waiting selector created a premature durable launch intent: %+v %v", saved, err)
			}
			if mode != "release" {
				wantErr := context.DeadlineExceeded
				if mode == "cancel_waiter" {
					cancelSecond()
					wantErr = context.Canceled
				}
				result := selectorBudgetAwait(t, secondDone)
				if result.output != nil || !errors.Is(result.err, wantErr) || second.Process != nil {
					t.Fatalf("cancelled capacity wait launched or lost cancellation: %+v process=%v", result, second.Process)
				}
				selectorBudgetNoEffect(t, e, "second-effects")
			}
			if err := os.WriteFile(release, []byte("finish"), 0600); err != nil {
				t.Fatal(err)
			}
			if result := selectorBudgetAwait(t, firstDone); result.err != nil || len(result.output) != 0 {
				t.Fatalf("first selector did not release unused reservation: %+v", result)
			}
			if mode == "release" {
				result := selectorBudgetAwait(t, secondDone)
				if result.err != nil || string(result.output) != "second" || second.Process == nil {
					t.Fatalf("settlement did not wake queued selector: %+v process=%v", result, second.Process)
				}
				if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "second-effects")); err != nil || string(data) != "once" {
					t.Fatalf("queued selector did not launch exactly once: %q %v", data, err)
				}
			} else {
				if output, _, err := selectorBudgetEvaluate(ctx, e, "third", "printf 12345678", 8); err != nil || string(output) != "12345678" {
					t.Fatalf("cancelled waiter leaked aggregate capacity: %q %v", output, err)
				}
			}
		})
	}
}

func TestSelectorOutputBudgetKnownFailuresReleaseReservations(t *testing.T) {
	for _, mode := range []string{"start_failure", "settled_exit_failure"} {
		t.Run(mode, func(t *testing.T) {
			e := selectorBudgetExecutor(t, 8)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			step := &Step{ID: "failed", Branch: "$(failing source)"}
			cmd := selectorBudgetCommand(e, "printf once > failed-effects; printf partial; exit 7")
			if mode == "start_failure" {
				cmd = exec.Command(filepath.Join(e.config.ProjectDir, "missing-executable"))
				cmd.Dir = e.config.ProjectDir
			}
			output, err := runCommandOutput(e.commandEvaluationContext(ctx, step), cmd, 8, 64)
			if output != nil || err == nil || errors.Is(err, ErrSelectorOutputBudget) || errors.Is(err, errCommandOutputUnsettled) {
				t.Fatalf("fixture did not reach proven failure: output=%q err=%v", output, err)
			}
			if mode == "settled_exit_failure" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 || cmd.ProcessState == nil {
					t.Fatalf("nonzero selector was not reaped with its exit status: %v", err)
				}
			}
			prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
			if err != nil || len(prior.CommandEvaluations) != 0 {
				t.Fatalf("proven failure stranded a charged intent: %+v %v", prior, err)
			}
			if output, _, err := selectorBudgetEvaluate(ctx, e, "next", "printf 12345678", 8); err != nil || string(output) != "12345678" {
				t.Fatalf("failed selector did not release capacity: %q %v", output, err)
			}
		})
	}
}

func TestSelectorOutputBudgetUncertainReceiptsRetainFullChargeAfterReload(t *testing.T) {
	for _, status := range []string{commandExecutionStarting, commandExecutionUnresolved} {
		t.Run(status, func(t *testing.T) {
			e := selectorBudgetExecutor(t, 8)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			step := &Step{ID: "uncertain", Branch: "$(printf one byte)"}
			evaluationCtx := e.commandEvaluationContext(ctx, step)
			lifecycle := evaluationCtx.Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
			afterWait := lifecycle.afterWait
			lost := errors.New("injected loss of selector completion evidence")
			lifecycle.afterWait = func(cmd *exec.Cmd, settled bool, output []byte, err error) error {
				if !settled || err != nil || string(output) != "x" {
					return errors.New("selector did not reach the intended fault boundary")
				}
				if status == commandExecutionUnresolved {
					return errors.Join(lost, afterWait(cmd, false, nil, lost))
				}
				return lost // Model a crash before the completion checkpoint.
			}
			cmd := selectorBudgetCommand(e, "printf once > uncertain-effects; printf x")
			if output, err := runCommandOutput(evaluationCtx, cmd, 8, 64); output != nil || !errors.Is(err, lost) || cmd.ProcessState == nil {
				t.Fatalf("fixture did not preserve lost completion: %q %v", output, err)
			}
			prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
			if err != nil || prior.CommandEvaluations[step.ID].Status != status || prior.CommandEvaluations[step.ID].StdoutLimit != 8 {
				t.Fatalf("lost completion receipt is invalid: %+v %v", prior, err)
			}
			fresh := NewExecutor(e.config)
			fresh.state, fresh.limits = prior, e.limits
			before := selectorRecoveryCheckpoint(t, e.config)
			output, next, err := selectorBudgetEvaluate(ctx, fresh, "next", "printf forbidden > next-effects; printf y", 1)
			if output != nil || !errors.Is(err, ErrSelectorOutputBudget) || next.Process != nil || ctx.Err() != nil {
				t.Fatalf("persistent uncertainty released capacity or waited for a nonexistent owner: %q %v process=%v", output, err, next.Process)
			}
			selectorBudgetNoEffect(t, e, "next-effects")
			if !bytes.Equal(before, selectorRecoveryCheckpoint(t, e.config)) {
				t.Fatal("capacity refusal changed uncertain execution evidence")
			}
		})
	}
}

func TestSelectorOutputBudgetCachedBytesRemainUsableAboveLowerLimit(t *testing.T) {
	e := selectorBudgetExecutor(t, 8)
	want := []byte{0xff, 0, 'a', '\n'}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "source-output"), want, 0600); err != nil {
		t.Fatal(err)
	}
	const script = "printf once >> source-effects; cat source-output"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, _, err := selectorBudgetEvaluate(ctx, e, "source", script, 8); err != nil || !bytes.Equal(output, want) {
		t.Fatalf("initial exact-byte output failed: %x %v", output, err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewExecutor(e.config)
	fresh.state, fresh.limits = prior, e.limits
	fresh.limits.MaxSelectorOutputBytesTotal = 1
	before := selectorRecoveryCheckpoint(t, e.config)
	output, cmd, err := selectorBudgetEvaluate(ctx, fresh, "source", script, 8)
	if err != nil || !bytes.Equal(output, want) || cmd.Process != nil {
		t.Fatalf("lower budget discarded or reexecuted cached source: %x %v process=%v", output, err, cmd.Process)
	}
	output[0] = 0
	if !bytes.Equal(fresh.state.CommandEvaluations["source"].Output, want) || fresh.state.CommandEvaluations["source"].StdoutLimit != 8 || !bytes.Equal(before, selectorRecoveryCheckpoint(t, e.config)) {
		t.Fatal("cached reuse aliased bytes, shrank the original cap, or rewrote its checkpoint")
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "source-effects")); err != nil || string(data) != "once" {
		t.Fatalf("cached selector repeated effects: %q %v", data, err)
	}
	if _, next, err := selectorBudgetEvaluate(ctx, fresh, "new", "printf forbidden > new-effects", 1); !errors.Is(err, ErrSelectorOutputBudget) || next.Process != nil {
		t.Fatalf("over-budget cached state allowed new work: %v process=%v", err, next.Process)
	}
	selectorBudgetNoEffect(t, e, "new-effects")
}

func TestSelectorOutputBudgetAcceptanceCanReduceOverBudgetUncertainty(t *testing.T) {
	e := selectorBudgetExecutor(t, 8)
	const script = "printf once >> source-effects; printf good"
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{{
		ID: "select", Branch: "$(" + script + ")", Branches: map[string]interface{}{"good": Step{ID: "body", Command: "true"}},
	}}}
	workflow.Settings.Limits = LimitsConfig{MaxSelectorOutputBytesTotal: 1, MaxCommandStdoutBytes: 8, MaxCommandStderrBytes: 64}
	frozen, path, err := SnapshotWorkflow(context.Background(), e.config.ProjectDir, workflow)
	if err != nil {
		t.Fatal(err)
	}
	e.config.WorkflowFile, e.state.WorkflowFile = path, path
	e.graph = NewDependencyGraph(frozen)
	// A previous explicitly enlarged attempt can leave an unresolved receipt
	// exceeding the frozen limit. Recording its final outcome must still work.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	evaluationCtx := e.commandEvaluationContext(ctx, &frozen.Steps[0])
	lifecycle := evaluationCtx.Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
	afterWait := lifecycle.afterWait
	lost := errors.New("injected final observation uncertainty")
	lifecycle.afterWait = func(cmd *exec.Cmd, settled bool, output []byte, err error) error {
		if !settled || err != nil || string(output) != "good" {
			return errors.New("selector did not reach the intended uncertainty boundary")
		}
		return errors.Join(lost, afterWait(cmd, false, nil, lost))
	}
	cmd := selectorBudgetCommand(e, script)
	if output, err := runCommandOutput(evaluationCtx, cmd, 8, 64); output != nil || !errors.Is(err, lost) {
		t.Fatalf("fixture did not save uncertainty: %q %v", output, err)
	}
	entry := selectorRecoveryEntry(t, e.config)
	const reason = "Verified that the original selector ended and captured its complete stdout"
	accepted, err := AcceptCommandEvaluationContext(ctx, e.config.ProjectDir, e.config.RunID, entry.StepID, entry.ReceiptToken, reason, 0, []byte("good"))
	if err != nil || accepted.Replayed || accepted.Selector.Status != commandEvaluationAccepted {
		t.Fatalf("over-budget uncertainty could not be safely reconciled: %+v %v", accepted, err)
	}
	before := selectorRecoveryCheckpoint(t, e.config)
	replayed, err := AcceptCommandEvaluationContext(ctx, e.config.ProjectDir, e.config.RunID, entry.StepID, entry.ReceiptToken, reason, 0, []byte("good"))
	if err != nil || !replayed.Replayed || !bytes.Equal(before, selectorRecoveryCheckpoint(t, e.config)) {
		t.Fatalf("idempotent acceptance was blocked or rewritten by the frozen budget: %+v %v", replayed, err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewExecutor(e.config)
	fresh.state, fresh.limits = prior, frozen.Settings.Limits.EffectiveLimits()
	cached := selectorBudgetCommand(fresh, script)
	output, err := runCommandOutput(fresh.commandEvaluationContext(ctx, &frozen.Steps[0]), cached, 8, 64)
	if err != nil || string(output) != "good" || cached.Process != nil {
		t.Fatalf("accepted over-budget output was not reusable: %q %v process=%v", output, err, cached.Process)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "source-effects")); err != nil || string(data) != "once" {
		t.Fatalf("acceptance or cache reuse repeated source effects: %q %v", data, err)
	}
}

func TestSelectorOutputBudgetCheckpointFailureDoesNotStrandWaiters(t *testing.T) {
	e := selectorBudgetExecutor(t, 8)
	// Prevent the first intent from becoming durable, before any process may
	// start. The sticky checkpoint error must not leave a live capacity wait.
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, ".ntm"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if output, cmd, err := selectorBudgetEvaluate(ctx, e, "first", "printf forbidden > first-effects", 8); output != nil || !errors.Is(err, ErrCheckpointFailed) || cmd.Process != nil {
		t.Fatalf("checkpoint failure launched a selector: %q %v process=%v", output, err, cmd.Process)
	}
	output, cmd, err := selectorBudgetEvaluate(ctx, e, "second", "printf forbidden > second-effects", 8)
	if output != nil || err == nil || errors.Is(err, context.DeadlineExceeded) || cmd.Process != nil || ctx.Err() != nil {
		t.Fatalf("failed admission left an unwakeable active reservation: %q %v process=%v context=%v", output, err, cmd.Process, ctx.Err())
	}
	selectorBudgetNoEffect(t, e, "first-effects")
	selectorBudgetNoEffect(t, e, "second-effects")
}

func TestSelectorOutputBudgetExplicitRestartPrunesOnlyAuthorizedReceipts(t *testing.T) {
	e := selectorBudgetExecutor(t, 8)
	const keptScript = "printf once >> kept-effects; printf kept"
	const retryScript = "printf once >> retry-effects; printf redo"
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{
		{ID: "kept", Branch: "$(" + keptScript + ")"},
		{ID: "retry", Branch: "$(" + retryScript + ")"},
	}}
	e.graph = NewDependencyGraph(workflow)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := selectorBudgetEvaluate(ctx, e, "kept", keptScript, 4); err != nil {
		t.Fatal(err)
	}
	e.state.Steps["kept"] = StepResult{StepID: "kept", Status: StatusCompleted, StartedAt: time.Now(), FinishedAt: time.Now()}
	evaluationCtx := e.commandEvaluationContext(ctx, &workflow.Steps[1])
	lifecycle := evaluationCtx.Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
	afterWait := lifecycle.afterWait
	lost := errors.New("injected unresolved selector")
	lifecycle.afterWait = func(cmd *exec.Cmd, settled bool, output []byte, err error) error {
		return errors.Join(lost, afterWait(cmd, false, nil, lost))
	}
	cmd := selectorBudgetCommand(e, retryScript)
	if output, err := runCommandOutput(evaluationCtx, cmd, 4, 64); output != nil || !errors.Is(err, lost) {
		t.Fatalf("fixture did not retain an unresolved selector: %q %v", output, err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewExecutor(e.config)
	fresh.state, fresh.graph, fresh.limits = prior, NewDependencyGraph(workflow), e.limits
	keptBefore, err := json.Marshal(prior.CommandEvaluations["kept"])
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.applyResumeOptions(workflow, ResumeOptions{}); !errors.Is(err, errCommandOutputUnsettled) || len(fresh.state.CommandEvaluations) != 2 {
		t.Fatalf("ordinary resume evicted uncertainty to recover capacity: %v", err)
	}
	if err := fresh.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
		t.Fatalf("explicit replay scope was rejected: %v", err)
	}
	keptAfter, err := json.Marshal(fresh.state.CommandEvaluations["kept"])
	if err != nil || !bytes.Equal(keptBefore, keptAfter) || len(fresh.state.CommandEvaluations) != 1 {
		t.Fatalf("explicit restart removed an unrelated completed receipt: %v", err)
	}
	if output, cmd, err := selectorBudgetEvaluate(ctx, fresh, "kept", keptScript, 4); err != nil || string(output) != "kept" || cmd.Process != nil {
		t.Fatalf("kept receipt lost cached reuse after scoped pruning: %q %v", output, err)
	}
	if output, _, err := selectorBudgetEvaluate(ctx, fresh, "new", "printf next", 4); err != nil || string(output) != "next" {
		t.Fatalf("authorized pruning did not release only its reservation: %q %v", output, err)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "kept-effects")); err != nil || string(data) != "once" {
		t.Fatalf("explicit restart repeated unrelated source effects: %q %v", data, err)
	}
}
