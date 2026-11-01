package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These entry guards call Err outside stateMu. Cancel only after the chosen
// admission boundary is visible, so the regression cannot accidentally stop
// in the already-safe admission check or rely on goroutine scheduling.
type stepEntryCancellationContext struct {
	context.Context
	cancel          context.CancelFunc
	executor        *Executor
	stepID          string
	afterInFlight   bool
	afterSettlement bool
	stepStarts      <-chan ProgressEvent
	startAfter      int
	seenStarts      atomic.Int32
	triggered       atomic.Bool
	wasInFlight     atomic.Bool
}

func (c *stepEntryCancellationContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	c.executor.stateMu.RLock()
	state := c.executor.state
	admitted, inFlight, settled := false, false, false
	if state != nil {
		if state.StepAdmissions != nil {
			admitted = state.StepAdmissions.Work[c.stepID]
		}
		_, inFlight = state.InFlightSteps[c.stepID]
		settled = state.CommandExecutions[c.stepID].Status == commandExecutionSettled
	}
	c.executor.stateMu.RUnlock()
	ready := admitted && (!c.afterInFlight || inFlight) && (!c.afterSettlement || settled)
	if c.startAfter > 0 {
		for {
			select {
			case event := <-c.stepStarts:
				if event.Type == "step_start" && event.StepID == c.stepID {
					c.seenStarts.Add(1)
				}
			default:
				ready = ready && int(c.seenStarts.Load()) >= c.startAfter
				goto observed
			}
		}
	}
observed:
	if ready && c.triggered.CompareAndSwap(false, true) {
		c.wasInFlight.Store(inFlight)
		c.cancel()
	}
	return c.Context.Err()
}

func TestStepEntryCancellationPersistsNoDispatchProofAndResumes(t *testing.T) {
	for _, boundary := range []struct {
		name                   string
		afterInFlight, foreach bool
		startAfter             int
	}{
		{name: "after admission"},
		{name: "after in-flight checkpoint", afterInFlight: true},
		{name: "before kind dispatch", afterInFlight: true, startAfter: 1},
		{name: "foreach before kind dispatch", afterInFlight: true, foreach: true},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			cfg := DefaultExecutorConfig("step-entry-cancel")
			cfg.ProjectDir, cfg.RunID = t.TempDir(), GenerateRunID()
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "step-entry-cancel", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "work", Command: "printf 'once\\n' >> effects; printf resumed"},
			}}
			if boundary.foreach {
				workflow.Steps = []Step{{ID: "batch", Foreach: &ForeachConfig{Items: `["item"]`, Steps: workflow.Steps}}}
			}
			frozen, path, err := SnapshotWorkflow(context.Background(), cfg.ProjectDir, workflow)
			if err != nil {
				t.Fatal(err)
			}
			cfg.WorkflowFile = path
			step := frozen.Steps[0]
			if boundary.foreach {
				step = cloneStep(frozen.Steps[0].Foreach.Steps[0])
				step.ID = "batch_iter0_work"
			}
			executor := NewExecutor(cfg)
			executor.graph = NewDependencyGraph(frozen)
			progress := make(chan ProgressEvent, 8)
			executor.progress = progress
			now := time.Now()
			executor.state = &ExecutionState{
				RunID: cfg.RunID, WorkflowID: frozen.Name, WorkflowFile: path, Session: cfg.Session,
				Status: StatusRunning, StartedAt: now, UpdatedAt: now,
				Steps: map[string]StepResult{}, Variables: map[string]interface{}{}, CommandEvaluationVersion: commandEvaluationVersion,
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &stepEntryCancellationContext{Context: base, cancel: cancel, executor: executor, stepID: step.ID,
				afterInFlight: boundary.afterInFlight, stepStarts: progress, startAfter: boundary.startAfter}
			var result StepResult
			if boundary.foreach {
				result = executor.executeForeachNestedStep(ctx, &step, frozen)
			} else {
				result = executor.executeStep(ctx, &step, frozen)
			}
			if !ctx.triggered.Load() || ctx.wasInFlight.Load() != ctx.afterInFlight || int(ctx.seenStarts.Load()) != boundary.startAfter {
				t.Fatalf("cancellation missed the selected entry guard: triggered=%t in-flight=%t", ctx.triggered.Load(), ctx.wasInFlight.Load())
			}
			if result.Status != StatusCancelled || result.SkipKind != SkipKindCancelledBeforeDispatch || result.Attempts != 0 || result.AgentType != "" {
				t.Fatalf("pre-dispatch cancellation lost its affirmative proof: %+v", result)
			}
			executor.stateMu.Lock()
			executor.state.Status = StatusCancelled
			executor.state.Steps[step.ID] = result
			executor.stateMu.Unlock()
			if err := executor.persistState(); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil || len(prior.CommandExecutions) != 0 || len(prior.InFlightSteps) != 0 || !resumeCancelledBeforeDispatch(prior, step.ID) {
				t.Fatalf("saved cancellation cannot safely resume: %+v %v", prior, err)
			}
			if effects, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); !os.IsNotExist(err) {
				t.Fatalf("cancelled entry dispatched a command: %q %v", effects, err)
			}
			final, err := NewExecutor(cfg).Resume(context.Background(), frozen, prior, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps[step.ID].Output != "resumed" || final.CommandExecutions[step.ID].Status != commandExecutionSettled {
				t.Fatalf("ordinary resume stranded a proven-undispatched command: %+v %v", final, err)
			}
			if effects, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(effects) != "once\n" {
				t.Fatalf("resume did not execute exactly one original command: %q %v", effects, err)
			}
		})
	}
}

func TestStepEntryCancellationPreservesEarlierAttemptEvidence(t *testing.T) {
	for _, adapter := range []string{"top-level", "foreach"} {
		t.Run(adapter, func(t *testing.T) {
			executor := newCommandTestExecutor(t)
			executor.state.Session = executor.config.Session
			step := Step{ID: "work", Command: "printf 'once\\n' >> effects; exit 7", OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond}}
			workflow := &Workflow{Name: executor.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
			executor.graph = NewDependencyGraph(workflow)
			progress := make(chan ProgressEvent, 8)
			executor.progress = progress
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &stepEntryCancellationContext{Context: base, cancel: cancel, executor: executor, stepID: step.ID, afterInFlight: true}
			var result StepResult
			if adapter == "foreach" {
				ctx.afterSettlement = true
				result = executor.executeForeachNestedStep(ctx, &step, workflow)
			} else {
				ctx.stepStarts, ctx.startAfter = progress, 2
				result = executor.executeStep(ctx, &step, workflow)
			}
			if !ctx.triggered.Load() || result.Status != StatusCancelled || result.SkipKind != SkipKindCancelledBeforeDispatch || result.Attempts != 1 {
				t.Fatalf("cancelled retry erased its earlier actual attempt: %+v", result)
			}
			original, exists := executor.loadCommandExecution(step.ID)
			if !exists || original.Status != commandExecutionSettled || original.ProcessID <= 0 || original.Result.Status != StatusFailed {
				t.Fatalf("cancelled retry lost its original native failure: %+v", original)
			}
			if effects, err := os.ReadFile(filepath.Join(executor.config.ProjectDir, "effects")); err != nil || string(effects) != "once\n" {
				t.Fatalf("cancelled retry executed another command: %q %v", effects, err)
			}
			executor.state.Steps[step.ID] = result
			// A later loss of native evidence cannot make the new cancellation
			// marker authorize replay of the first attempt's real effect.
			delete(executor.state.CommandExecutions, step.ID)
			if err := executor.applyResumeOptions(workflow, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
				t.Fatalf("earlier attempt became replayable without its receipt: %v", err)
			}
		})
	}
}

func TestNativeCancellationBeforeStartPersistsSettlementAndResumes(t *testing.T) {
	executor := newCommandTestExecutor(t)
	executor.state.Session = executor.config.Session
	executor.state.CommandEvaluationVersion = commandEvaluationVersion
	executor.config.DefaultTimeout = -time.Nanosecond
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: executor.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{
		{ID: "work", Command: "printf 'once\\n' >> effects; printf resumed"},
	}}
	executor.graph = NewDependencyGraph(workflow)
	// The parent stays live: only executeCommand's own timeout expires, after
	// scheduler admission and its kind-dispatch guard have already passed.
	result := executor.executeStep(context.Background(), &workflow.Steps[0], workflow)
	original, exists := executor.loadCommandExecution("work")
	if result.Status != StatusCancelled || !exists || original.Status != commandExecutionSettled || original.ProcessID != 0 || original.Result.Status != StatusCancelled {
		t.Fatalf("native pre-start cancellation lost its exact no-process receipt: %+v %+v", result, original)
	}
	if err := validateCommandExecution("work", original); err != nil {
		t.Fatalf("native no-process evidence is invalid: %v", err)
	}
	if effects, err := os.ReadFile(filepath.Join(executor.config.ProjectDir, "effects")); !os.IsNotExist(err) {
		t.Fatalf("expired native context started its command: %q %v", effects, err)
	}
	executor.state.Status = StatusCancelled
	executor.state.Steps["work"] = result
	if err := executor.persistState(); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(executor.config.ProjectDir, executor.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := executor.config
	cfg.DefaultTimeout = time.Second
	final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
	if err != nil || final.Status != StatusCompleted || final.Steps["work"].Output != "resumed" {
		t.Fatalf("ordinary resume stranded a proven-unstarted command: %+v %v", final, err)
	}
	if effects, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(effects) != "once\n" {
		t.Fatalf("resume did not launch exactly one command: %q %v", effects, err)
	}
}

func TestExecuteCommand_GlobalTimeoutCancelsWithPartialOutput(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	step := &Step{
		ID:      "global-timeout",
		Command: "printf started; sleep 10",
		Timeout: Duration{Duration: 30 * time.Second},
	}

	start := time.Now()
	result := e.executeCommand(ctx, step, &Workflow{Name: "test"})
	elapsed := time.Since(start)

	if result.Status != StatusCancelled {
		t.Fatalf("Status = %q, want %q; error=%+v", result.Status, StatusCancelled, result.Error)
	}
	if result.Error == nil || result.Error.Type != "timeout" {
		t.Fatalf("Error.Type = %v, want timeout", result.Error)
	}
	if result.Output != "started" {
		t.Fatalf("Output = %q, want partial output %q", result.Output, "started")
	}
	if elapsed > 6*time.Second {
		t.Fatalf("executeCommand took %s, want cancellation within grace period", elapsed)
	}
}

func TestExecutor_Run_GlobalTimeoutRunsOnCancelSteps(t *testing.T) {
	tmpDir := t.TempDir()
	cleanupPath := filepath.Join(tmpDir, "cleanup.txt")

	cfg := DefaultExecutorConfig("test-cancel")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "cancel-cleanup",
		Settings: WorkflowSettings{
			Timeout: Duration{Duration: 150 * time.Millisecond},
			OnError: ErrorActionFail,
			OnCancel: []Step{
				{
					ID:      "cleanup",
					Command: "printf cleanup > " + strconv.Quote(cleanupPath),
				},
			},
		},
		Steps: []Step{
			{
				ID:      "slow",
				Command: "sleep 10",
			},
		},
	}

	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want timeout cancellation error")
	}
	if state.Status != StatusCancelled {
		t.Fatalf("state.Status = %q, want %q", state.Status, StatusCancelled)
	}
	if state.CancelledAt == nil || state.CancelledAt.IsZero() {
		t.Fatal("state.CancelledAt is nil or zero")
	}
	if state.FinishedAt.IsZero() {
		t.Fatal("state.FinishedAt is zero")
	}

	foundTimeout := false
	for _, stateErr := range state.Errors {
		if stateErr.Type == "timeout" {
			foundTimeout = true
			break
		}
	}
	if !foundTimeout {
		t.Fatalf("state.Errors = %+v, want a workflow timeout error", state.Errors)
	}
	cleanup := state.Steps["cleanup"]
	if cleanup.Status != StatusCompleted {
		t.Fatalf("cleanup.Status = %q, want %q; error=%+v", cleanup.Status, StatusCompleted, cleanup.Error)
	}

	data, err := os.ReadFile(cleanupPath)
	if err != nil {
		t.Fatalf("cleanup file was not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "cleanup" {
		t.Fatalf("cleanup file = %q, want cleanup", string(data))
	}
}

func TestExecutor_Run_CancellationRecordsActiveStep(t *testing.T) {
	tmpDir := t.TempDir()
	startedPath := filepath.Join(tmpDir, "started.txt")

	cfg := DefaultExecutorConfig("test-active-cancel")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "active-cancel",
		Settings: WorkflowSettings{
			Timeout: Duration{Duration: 30 * time.Second},
			OnError: ErrorActionFail,
		},
		Steps: []Step{
			{
				ID:      "slow",
				Command: "printf started > " + strconv.Quote(startedPath) + "; sleep 10",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runOutcome struct {
		state *ExecutionState
		err   error
	}
	runDone := make(chan runOutcome, 1)
	go func() {
		state, err := e.Run(ctx, workflow, nil, nil)
		runDone <- runOutcome{state: state, err: err}
	}()

	waitForRun := func() runOutcome {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case outcome := <-runDone:
			return outcome
		case <-timer.C:
			t.Fatal("Run() did not return within 10s of cancellation")
			return runOutcome{}
		}
	}

	markerTimer := time.NewTimer(10 * time.Second)
	defer markerTimer.Stop()
	markerPoll := time.NewTicker(10 * time.Millisecond)
	defer markerPoll.Stop()
	var lastMarkerErr error

waitForStart:
	for {
		select {
		case <-markerPoll.C:
			data, err := os.ReadFile(startedPath)
			lastMarkerErr = err
			if err == nil && strings.TrimSpace(string(data)) == "started" {
				break waitForStart
			}
		case <-markerTimer.C:
			cancel()
			outcome := waitForRun()
			t.Fatalf("slow command did not publish its start marker: last error=%v state=%+v run error=%v", lastMarkerErr, outcome.state, outcome.err)
		}
	}

	cancel()
	outcome := waitForRun()
	if outcome.err == nil {
		t.Fatal("Run() error = nil, want cancellation error")
	}
	if outcome.state == nil || outcome.state.Status != StatusCancelled {
		t.Fatalf("state = %+v, want cancelled workflow", outcome.state)
	}
	slow := outcome.state.Steps["slow"]
	if slow.Status != StatusCancelled {
		t.Fatalf("slow.Status = %q, want %q; error=%+v", slow.Status, StatusCancelled, slow.Error)
	}
}

// TestExecuteCommand_WaitNoneCancellation_MarksRerunOnResume covers
// bd-yrnue: when a fire-and-forget (WaitNone) command's background process
// is killed by cancellation cleanup AFTER executeCommand has returned
// StatusCompleted, the persisted step result must be flagged for rerun on
// resume so the sidecar gets relaunched instead of skipped.
func TestExecuteCommand_WaitNoneCancellation_MarksRerunOnResume(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	step := &Step{
		ID:      "sidecar",
		Command: "sleep 30",
		Wait:    WaitNone,
	}

	result := e.executeCommand(ctx, step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q (WaitNone returns Completed synchronously)", result.Status, StatusCompleted)
	}
	if result.RerunOnResume {
		t.Fatalf("RerunOnResume = true on synchronous return; want false (no cancellation yet)")
	}

	// Simulate executeWorkflow recording the result before cancellation fires.
	e.stateMu.Lock()
	e.state.Steps[step.ID] = result
	e.stateMu.Unlock()

	cancel()

	// Cancellation cleanup owns the state mutation and its atomic disk write.
	// Join the worker before inspecting either surface so TempDir cleanup cannot
	// race a late .ntm/pipelines writer after this test returns.
	e.backgroundCommandWG.Wait()

	e.stateMu.RLock()
	got := e.state.Steps[step.ID]
	e.stateMu.RUnlock()
	if !got.RerunOnResume {
		t.Fatalf("RerunOnResume = false after cancellation; want true so resume relaunches sidecar")
	}
	if !shouldRerunStep(got) {
		t.Fatalf("shouldRerunStep(%+v) = false; want true so applyResumeState reruns the sidecar", got)
	}

	persisted, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatalf("LoadState() after WaitNone cleanup: %v", err)
	}
	if persistedStep := persisted.Steps[step.ID]; !persistedStep.RerunOnResume {
		t.Fatalf("persisted RerunOnResume = false; want true after cleanup worker exits")
	}
}

func TestControlledPipelineWaitNoneNaturalFailurePersistsAndResumes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command and signal fixtures")
	}
	for _, tc := range []struct {
		name, terminate string
		exitCode        int
		failed          bool
		resume          bool
		shutdown        bool
	}{
		{name: "exit", terminate: "exit 7", exitCode: 7, failed: true},
		{name: "signal", terminate: "kill -TERM $$", exitCode: -1, failed: true},
		{name: "resume", terminate: "exit 7", exitCode: 7, failed: true, resume: true},
		{name: "success", terminate: "exit 0"},
		{name: "workflow_shutdown", shutdown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := DefaultExecutorConfig("wait-none-outcome")
			cfg.ProjectDir, cfg.RunID = root, "run-wait-none-outcome"
			command := "printf 'once\\n' >> launches; " +
				"if [ -f repair ]; then printf repaired > repaired; printf recovered; exit 0; fi; " +
				"printf ready > sidecar-ready; while [ ! -f sidecar-release ]; do sleep 0.01; done; printf original; " + tc.terminate
			if tc.shutdown {
				command = "printf 'once\\n' >> launches; trap 'printf handled > handled; printf shutdown; exit 0' TERM; " +
					"printf ready > sidecar-ready; while :; do sleep 0.01; done"
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "wait-none-outcome", Settings: DefaultWorkflowSettings(), Steps: []Step{
				{ID: "sidecar", Wait: WaitNone, Command: command, OnError: ErrorActionRetry, RetryCount: 3, RetryDelay: Duration{Duration: time.Millisecond}},
				{ID: "gate", DependsOn: []string{"sidecar"}, Command: "printf 'once\\n' >> consumers; printf ready > gate-ready; while [ ! -f gate-release ]; do sleep 0.01; done; printf consumed"},
			}}
			if tc.resume {
				// A separately failed final step keeps the resumed attempt open
				// until the test observes the repaired sidecar's own settlement.
				// The successful gate remains consumed across both attempts.
				workflow.Steps = append(workflow.Steps, Step{ID: "finish", DependsOn: []string{"gate"},
					Command: "if [ ! -f repair ]; then exit 19; fi; printf ready > resume-ready; while [ ! -f finish-release ]; do sleep 0.01; done; printf finished"})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			type outcome struct {
				state *ExecutionState
				err   error
			}
			events := make(chan ProgressEvent, 100)
			done := make(chan outcome, 1)
			attempts := []chan outcome{done}
			cancels := []context.CancelFunc{cancel}
			go func() {
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, events)
				done <- outcome{state, err}
				close(done)
			}()
			t.Cleanup(func() {
				for _, stop := range cancels {
					stop()
				}
				for _, name := range []string{"sidecar-release", "gate-release", "finish-release"} {
					_ = os.WriteFile(filepath.Join(root, name), []byte("finish"), 0600)
				}
				for _, attempt := range attempts {
					select {
					case <-attempt:
					case <-time.After(15 * time.Second):
						t.Error("background outcome fixture did not join command cleanup")
					}
				}
			})
			write := func(name string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte("finish"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			awaitCommandCancellationFile(t, filepath.Join(root, "sidecar-ready"), "ready")
			awaitCommandCancellationFile(t, filepath.Join(root, "gate-ready"), "ready")
			if !tc.shutdown {
				write("sidecar-release")
				deadline := time.Now().Add(5 * time.Second)
				for {
					prior, err := LoadState(root, cfg.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if prior.CommandExecutions["sidecar"].Status == commandExecutionSettled {
						if prior.Status != StatusRunning || ctx.Err() != nil {
							t.Fatalf("background outcome cancelled independently admitted work: state=%s context=%v", prior.Status, ctx.Err())
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("background process did not settle before workflow shutdown: %+v", prior.CommandExecutions["sidecar"])
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			select {
			case early := <-done:
				t.Fatalf("background outcome abandoned the foreground gate: %+v %v", early.state, early.err)
			default:
			}
			write("gate-release")
			var initial outcome
			select {
			case initial = <-done:
			case <-ctx.Done():
				t.Fatal("workflow did not finish after its gate was released")
			}
			if initial.state == nil || initial.state.Steps["gate"].Status != StatusCompleted || initial.state.Steps["gate"].Output != "consumed" {
				t.Fatalf("background outcome replaced its consumed launch acknowledgement: %+v %v", initial.state, initial.err)
			}
			var exitMessage string
			if tc.failed {
				var exitErr *exec.ExitError
				if initial.state.Status != StatusFailed || !errors.As(initial.err, &exitErr) || exitErr.ExitCode() != tc.exitCode ||
					errors.Is(initial.err, context.Canceled) || errors.Is(initial.err, context.DeadlineExceeded) ||
					!strings.Contains(initial.err.Error(), "background command \"sidecar\" failed") {
					t.Fatalf("background failure was hidden or lost its original process error: %+v %v", initial.state, initial.err)
				}
				exitMessage = exitErr.Error()
				assertNoCompletionEvent(t, events)
			} else if initial.err != nil || initial.state.Status != StatusCompleted {
				t.Fatalf("ordinary background %s became a terminal failure: %+v %v", tc.name, initial.state, initial.err)
			}
			saved, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := saved.CommandExecutions["sidecar"]
			if receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() {
				t.Fatalf("background result lacks durable settlement evidence: %+v", receipt)
			}
			if tc.failed {
				if saved.Status != StatusFailed || receipt.Result.Status != StatusFailed || !receipt.Result.RerunOnResume ||
					receipt.Result.Error == nil || !strings.Contains(receipt.Result.Error.Details, exitMessage) ||
					!strings.Contains(receipt.Result.Output, "original") || len(saved.Errors) == 0 ||
					!strings.Contains(saved.Errors[len(saved.Errors)-1].Message, "background command \"sidecar\" failed") {
					t.Fatalf("background failure was not durable or rerunnable after settlement: %+v %+v", receipt, saved.Errors)
				}
			} else if tc.shutdown {
				if receipt.Result.Status != StatusCancelled || !receipt.Result.RerunOnResume || !strings.Contains(receipt.Result.Output, "shutdown") {
					t.Fatalf("expected workflow shutdown lost its graceful cancellation outcome: %+v", receipt)
				}
				awaitCommandCancellationFile(t, filepath.Join(root, "handled"), "handled")
			} else if receipt.Result.Status != StatusCompleted || receipt.Result.Error != nil || receipt.Result.RerunOnResume {
				t.Fatalf("natural successful completion was changed: %+v", receipt)
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
				t.Fatalf("background outcome invoked an asynchronous retry: %q %v", data, err)
			}
			if !tc.resume {
				return
			}
			originalGate := saved.Steps["gate"]
			write("repair")
			resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 20*time.Second)
			cancels = append(cancels, resumeCancel)
			resumed := make(chan outcome, 1)
			attempts = append(attempts, resumed)
			go func() {
				state, err := NewExecutor(cfg).Resume(resumeCtx, workflow, saved, nil)
				resumed <- outcome{state, err}
				close(resumed)
			}()
			awaitCommandCancellationFile(t, filepath.Join(root, "repaired"), "repaired")
			awaitCommandCancellationFile(t, filepath.Join(root, "resume-ready"), "ready")
			deadline := time.Now().Add(5 * time.Second)
			for {
				current, err := LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				repaired := current.CommandExecutions["sidecar"]
				if repaired.Status == commandExecutionSettled && repaired.StartedAt.After(receipt.StartedAt) && repaired.Result.Status == StatusCompleted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ordinary resume did not settle the repaired background execution: %+v", repaired)
				}
				time.Sleep(10 * time.Millisecond)
			}
			write("finish-release")
			select {
			case final := <-resumed:
				if final.err != nil || final.state == nil || final.state.Status != StatusCompleted ||
					!final.state.Steps["gate"].FinishedAt.Equal(originalGate.FinishedAt) || final.state.Steps["finish"].Output != "finished" {
					t.Fatalf("ordinary resume lost repaired sidecar or retained dependent state: %+v %v", final.state, final.err)
				}
			case <-resumeCtx.Done():
				t.Fatal("resumed workflow did not finish after repairing its sidecar")
			}
			if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\nonce\n" {
				t.Fatalf("ordinary resume did not relaunch the failed sidecar exactly once: %q %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "consumers")); err != nil || string(data) != "once\n" {
				t.Fatalf("ordinary resume replayed the completed dependent: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineWaitNoneExplicitTimeoutPersistsFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command and SIGTERM fixture")
	}
	root := t.TempDir()
	cfg := DefaultExecutorConfig("wait-none-timeout")
	cfg.ProjectDir, cfg.RunID = root, "run-wait-none-timeout"
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "wait-none-timeout", Settings: DefaultWorkflowSettings(), Steps: []Step{
		{ID: "sidecar", Wait: WaitNone, Timeout: Duration{Duration: time.Second},
			Command: "printf 'once\\n' >> launches; trap 'printf handled > handled; printf shutdown; exit 0' TERM; printf ready > ready; while [ ! -f sidecar-release ]; do sleep 0.01; done; printf late > late-effect"},
		{ID: "gate", DependsOn: []string{"sidecar"}, Command: "while [ ! -f gate-release ]; do sleep 0.01; done; printf consumed"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		_ = os.WriteFile(filepath.Join(root, "sidecar-release"), []byte("finish"), 0600)
		_ = os.WriteFile(filepath.Join(root, "gate-release"), []byte("finish"), 0600)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("background timeout fixture did not finish cleanup")
		}
	})
	awaitCommandCancellationFile(t, filepath.Join(root, "ready"), "ready")
	awaitCommandCancellationFile(t, filepath.Join(root, "handled"), "handled")
	deadline := time.Now().Add(10 * time.Second)
	var prior *ExecutionState
	for {
		var err error
		prior, err = LoadState(root, cfg.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if prior.CommandExecutions["sidecar"].Status == commandExecutionSettled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("explicit background timeout never settled its receipt: %+v", prior.CommandExecutions["sidecar"])
		}
		time.Sleep(10 * time.Millisecond)
	}
	receipt := prior.CommandExecutions["sidecar"]
	if receipt.Result.Status != StatusFailed || receipt.Result.Error == nil || receipt.Result.Error.Type != "timeout" ||
		!receipt.Result.RerunOnResume || !strings.Contains(receipt.Result.Output, "shutdown") ||
		!strings.Contains(receipt.Result.Error.Details, context.DeadlineExceeded.Error()) {
		t.Fatalf("background deadline lost its durable failure, graceful output, or settled retry permission: %+v", receipt)
	}
	if prior.Status != StatusRunning || ctx.Err() != nil {
		t.Fatalf("background deadline replaced the workflow's independent lifetime: state=%s context=%v", prior.Status, ctx.Err())
	}
	select {
	case early := <-done:
		t.Fatalf("fire-and-forget timeout abandoned already-admitted gate work: %+v %v", early.state, early.err)
	default:
	}
	if err := os.WriteFile(filepath.Join(root, "sidecar-release"), []byte("old work must be gone"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gate-release"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	var final outcome
	select {
	case final = <-done:
	case <-ctx.Done():
		t.Fatal("background timeout fixture did not finish after the foreground gate")
	}
	if !errors.Is(final.err, context.DeadlineExceeded) || final.state == nil || final.state.Status != StatusFailed ||
		final.state.Steps["gate"].Status != StatusCompleted || final.state.Steps["gate"].Output != "consumed" {
		t.Fatalf("settled background deadline was silently published as success: %+v %v", final.state, final.err)
	}
	assertNoCompletionEvent(t, events)
	saved, err := LoadState(root, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != StatusFailed || !saved.Steps["sidecar"].RerunOnResume || len(saved.Errors) == 0 ||
		!strings.Contains(saved.Errors[len(saved.Errors)-1].Message, "timed out") {
		t.Fatalf("final background timeout was not durable: %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(root, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired sidecar performed work after its settled timeout: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "launches")); err != nil || string(data) != "once\n" {
		t.Fatalf("background timeout retried a fire-and-forget command: %q %v", data, err)
	}
}

func TestExecuteCommandWaitNoneWithoutExplicitTimeoutKeepsRunLifetime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	e := newCommandTestExecutor(t)
	e.config.DefaultTimeout = 250 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		e.backgroundCommandWG.Wait()
	})
	step := Step{ID: "sidecar", Wait: WaitNone,
		Command: "printf ready > ready; while [ ! -f release ]; do sleep 0.01; done; printf survived > effect"}
	result := e.executeCommand(ctx, &step, &Workflow{Name: "sidecar-run-lifetime"})
	if result.Status != StatusCompleted {
		t.Fatalf("sidecar did not acknowledge its launch: %+v", result)
	}
	e.stateMu.Lock()
	e.state.Steps[step.ID] = result
	e.stateMu.Unlock()
	awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "ready"), "ready")
	time.Sleep(2 * e.config.DefaultTimeout)
	if record, ok := e.loadCommandExecution(step.ID); !ok || record.Status != commandExecutionRunning {
		t.Fatalf("foreground default timeout shortened an unbounded sidecar's lifetime: %+v", record)
	}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "release"), []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "effect"), "survived")
	e.backgroundCommandWG.Wait()
	record, ok := e.loadCommandExecution(step.ID)
	if !ok || record.Status != commandExecutionSettled || record.Result.Status != StatusCompleted || record.Result.RerunOnResume || e.backgroundCommandFailure() != nil {
		t.Fatalf("unbounded sidecar lost its natural successful completion: %+v", record)
	}
}

func TestExecuteCommandWaitNoneExplicitTimeoutPreservesCancellationCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command and SIGTERM fixture")
	}
	for _, scenario := range []string{"natural", "parent_cancel", "deadline_then_parent_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				_ = os.WriteFile(filepath.Join(e.config.ProjectDir, "release"), []byte("finish"), 0600)
				_ = os.WriteFile(filepath.Join(e.config.ProjectDir, "grace-release"), []byte("finish"), 0600)
				e.backgroundCommandWG.Wait()
			})
			timeout := 30 * time.Second
			if scenario == "deadline_then_parent_cancel" {
				timeout = time.Second
			}
			step := Step{ID: "sidecar", Wait: WaitNone, Timeout: Duration{Duration: timeout},
				Command: "trap 'printf handled > handled; while [ ! -f grace-release ]; do sleep 0.01; done; printf stopped; exit 0' TERM; printf ready > ready; while [ ! -f release ]; do sleep 0.01; done; printf completed"}
			result := e.executeCommand(ctx, &step, &Workflow{Name: "sidecar-cause"})
			if result.Status != StatusCompleted {
				t.Fatalf("explicit-timeout sidecar did not acknowledge launch: %+v", result)
			}
			e.stateMu.Lock()
			e.state.Steps[step.ID] = result
			e.stateMu.Unlock()
			awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "ready"), "ready")
			if scenario == "natural" {
				if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "release"), []byte("finish"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if scenario == "parent_cancel" {
					cancel()
				}
				awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "handled"), "handled")
				// In the deadline scenario, cancel the parent only after TERM
				// proves that the step's own deadline already fired. This must
				// not rewrite its final classification to workflow cancellation.
				cancel()
				if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "grace-release"), []byte("finish"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.backgroundCommandWG.Wait()
			record, ok := e.loadCommandExecution(step.ID)
			if !ok || record.Status != commandExecutionSettled {
				t.Fatalf("sidecar did not settle after %s: %+v", scenario, record)
			}
			wantStatus, wantError := StatusCompleted, ""
			if scenario == "parent_cancel" {
				wantStatus, wantError = StatusCancelled, "cancelled"
			} else if scenario == "deadline_then_parent_cancel" {
				wantStatus, wantError = StatusFailed, "timeout"
			}
			if record.Result.Status != wantStatus || record.Result.RerunOnResume != (scenario != "natural") ||
				(wantError == "" && record.Result.Error != nil) || (wantError != "" && (record.Result.Error == nil || record.Result.Error.Type != wantError)) {
				t.Fatalf("sidecar lost its original %s outcome: %+v", scenario, record)
			}
			backgroundErr := e.backgroundCommandFailure()
			if scenario == "deadline_then_parent_cancel" {
				if !errors.Is(backgroundErr, context.DeadlineExceeded) || !strings.Contains(record.Result.Output, "stopped") {
					t.Fatalf("step deadline lost its terminal cause or graceful output: receipt=%+v error=%v", record, backgroundErr)
				}
			} else if backgroundErr != nil {
				t.Fatalf("ordinary %s became a background timeout failure: %v", scenario, backgroundErr)
			}
		})
	}
}

func awaitCommandCancellationFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command did not publish %q in %s", want, path)
}

func TestExecutor_Run_WaitNoneCleanupSettlesBeforeReturn(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("wait-none-lifecycle")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "wait-none-lifecycle",
		Steps: []Step{
			{
				ID:      "sidecar",
				Command: "sleep 30",
				Wait:    WaitNone,
			},
		},
	}

	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := state.Steps["sidecar"]; !got.RerunOnResume {
		t.Fatalf("returned sidecar result = %+v; want cancellation cleanup settled before Run returns", got)
	}

	persisted, err := LoadState(tmpDir, state.RunID)
	if err != nil {
		t.Fatalf("LoadState() after Run: %v", err)
	}
	if got := persisted.Steps["sidecar"]; !got.RerunOnResume {
		t.Fatalf("persisted sidecar result = %+v; want cancellation cleanup settled before Run returns", got)
	}
}

func TestExecutor_Resume_WaitNoneCleanupSettlesBeforeReturn(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("wait-none-resume-lifecycle")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "wait-none-resume-lifecycle",
		Steps: []Step{
			{
				ID:      "sidecar",
				Command: "sleep 30",
				Wait:    WaitNone,
			},
		},
	}
	prior := &ExecutionState{
		RunID:         "wait-none-resume-run",
		WorkflowID:    workflow.Name,
		Session:       cfg.Session,
		Status:        StatusFailed,
		StartedAt:     time.Now().Add(-time.Minute),
		UpdatedAt:     time.Now().Add(-time.Minute),
		Steps:         make(map[string]StepResult),
		Variables:     make(map[string]interface{}),
		ForeachState:  make(map[string]ForeachIterationState),
		ParallelState: make(map[string]ParallelGroupState),
		InFlightSteps: make(map[string]InFlightStepState),
	}

	state, err := e.Resume(context.Background(), workflow, prior, nil)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got := state.Steps["sidecar"]; !got.RerunOnResume {
		t.Fatalf("returned sidecar result = %+v; want cancellation cleanup settled before Resume returns", got)
	}

	persisted, err := LoadState(tmpDir, state.RunID)
	if err != nil {
		t.Fatalf("LoadState() after Resume: %v", err)
	}
	if got := persisted.Steps["sidecar"]; !got.RerunOnResume {
		t.Fatalf("persisted sidecar result = %+v; want cancellation cleanup settled before Resume returns", got)
	}
}

// TestShouldRerunStep_RerunOnResumeFlagWins covers the resume contract for
// fire-and-forget commands flagged after cancellation cleanup: the explicit
// RerunOnResume flag overrides the stale persisted Completed status.
func TestShouldRerunStep_RerunOnResumeFlagWins(t *testing.T) {
	completed := StepResult{Status: StatusCompleted}
	if shouldRerunStep(completed) {
		t.Fatalf("shouldRerunStep(plain completed) = true; want false")
	}
	flagged := StepResult{Status: StatusCompleted, RerunOnResume: true}
	if !shouldRerunStep(flagged) {
		t.Fatalf("shouldRerunStep(completed+RerunOnResume) = false; want true")
	}
}

// TestExecutor_RunOnCancelSteps_HungCleanupRespectsTimeout covers
// bd-new9w: a misbehaving cleanup step (sleep N seconds) must not block
// the executor forever. The OnCancelTimeout caps each cleanup step's
// wall-clock budget; the test sets it well below the cleanup sleep so
// the cleanup phase returns within seconds rather than minutes.
func TestExecutor_RunOnCancelSteps_HungCleanupRespectsTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("hung-cleanup")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "hung-cleanup",
		Settings: WorkflowSettings{
			Timeout:         Duration{Duration: 150 * time.Millisecond},
			OnError:         ErrorActionFail,
			OnCancelTimeout: Duration{Duration: 500 * time.Millisecond},
			OnCancel: []Step{
				{
					ID:      "hung",
					Command: "sleep 30",
				},
			},
		},
		Steps: []Step{
			{ID: "slow", Command: "sleep 10"},
		},
	}

	start := time.Now()
	state, _ := e.Run(context.Background(), workflow, nil, nil)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("Run() blocked for %s — OnCancelTimeout did not bound the hung cleanup", elapsed)
	}
	if state == nil || state.Status != StatusCancelled {
		t.Fatalf("state.Status = %v, want cancelled", state)
	}
	hung := state.Steps["hung"]
	if hung.Status == StatusCompleted {
		t.Fatalf("hung cleanup completed despite 30s sleep vs 500ms cap; status=%q error=%+v", hung.Status, hung.Error)
	}
}

func TestExecutorCancellationHooksFollowMainBackgroundSupervision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command and SIGTERM fixtures")
	}
	for _, unresolved := range []bool{false, true} {
		name := "settled_sidecar"
		if unresolved {
			name = "unresolved_supervision"
		}
		t.Run(name, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			e.cancelFn = cancel
			t.Cleanup(func() {
				cancel()
				e.backgroundCommandWG.Wait()
			})
			main := Step{
				ID: "sidecar", Wait: WaitNone, Stdin: "fixture\n",
				Command: "trap 'printf stopped > main-stopped; exit 0' TERM; printf ready > main-ready; while :; do sleep 0.02; done",
			}
			receiptStatus := "settled"
			if unresolved {
				receiptStatus = "unresolved"
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "test-workflow", Settings: DefaultWorkflowSettings(),
				Steps: []Step{main},
			}
			workflow.Settings.OnCancelTimeout = Duration{Duration: 2 * time.Second}
			workflow.Settings.OnCancel = []Step{
				{
					ID: "release_resources", Stdin: "fixture\n",
					Command: "test -f main-stopped && grep -q '\"status\": \"" + receiptStatus +
						"\"' .ntm/pipelines/run-cmd-test.json && printf released > resources-released",
				},
				// Cancellation cleanup may itself acknowledge background work.
				// The final join must still settle workers created after the
				// main-sidecar barrier, before finishExecution returns.
				{ID: "cleanup_sidecar", Wait: WaitNone, Stdin: "fixture\n", Command: "sleep 30"},
			}
			e.graph = NewDependencyGraph(workflow)
			unknown := errors.New("injected command settlement remains unknown")
			workerResult := make(chan error, 1)
			if unresolved {
				// Model a waiter that completed supervision without acquiring
				// settlement proof. No PID is observed or signalled by this
				// fixture, and joining it must not authorize command replay.
				hash, err := commandDefinitionHash(&main)
				if err != nil {
					t.Fatal(err)
				}
				record := CommandExecutionState{
					Version: commandExecutionVersion, StepID: main.ID, StepHash: hash,
					Status: commandExecutionRunning, Background: true,
					ProcessID: os.Getpid(), StartedAt: time.Now(),
				}
				if err := e.saveCommandExecution(record); err != nil {
					t.Fatal(err)
				}
				e.stateMu.Lock()
				e.state.Steps[main.ID] = StepResult{StepID: main.ID, AgentType: "command", Status: StatusCompleted}
				e.stateMu.Unlock()
				e.backgroundCommandWG.Add(1)
				go func() {
					defer e.backgroundCommandWG.Done()
					<-ctx.Done()
					saved, err := LoadState(e.config.ProjectDir, e.state.RunID)
					if err == nil && (saved.Status != StatusCancelled || saved.CancelledAt == nil || saved.CancelledAt.IsZero()) {
						err = errors.New("cancellation was not checkpointed before joining sidecars")
					}
					record.Status = commandExecutionUnresolved
					record.Result = StepResult{
						StepID: main.ID, AgentType: "command", Status: StatusCancelled,
						StartedAt: record.StartedAt, FinishedAt: time.Now(),
						Error: &StepError{Type: "execution_unknown", Message: unknown.Error()},
					}
					e.stateMu.Lock()
					e.backgroundCommandErrors = map[string]error{main.ID: unknown}
					e.stateMu.Unlock()
					err = errors.Join(err, e.saveCommandExecution(record))
					err = errors.Join(err, os.WriteFile(filepath.Join(e.config.ProjectDir, "main-stopped"), []byte("supervision-ended"), 0600))
					workerResult <- err
				}()
			} else {
				result := e.executeCommand(ctx, &main, workflow)
				if result.Status != StatusCompleted {
					t.Fatalf("sidecar launch failed: %+v", result)
				}
				e.stateMu.Lock()
				e.state.Steps[main.ID] = result
				e.stateMu.Unlock()
				awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "main-ready"), "ready")
			}
			// A recorded protection failure can reach finalization before
			// cancellation becomes visible through its supplied context. The
			// finalizer must request cancellation before waiting. In the old
			// order, OnCancel necessarily ran before this sidecar could stop.
			protection := errors.New("injected reservation protection failure")
			e.stateMu.Lock()
			e.reservationFailure = protection
			e.stateMu.Unlock()
			if ctx.Err() != nil {
				t.Fatalf("fixture must enter finalization with a live context: %v", ctx.Err())
			}
			state, err := e.finishExecution(ctx, workflow, nil)
			if ctx.Err() != context.Canceled {
				t.Fatalf("finalizer waited for the caller deadline instead of requesting cancellation: %v", ctx.Err())
			}
			if !errors.Is(err, protection) || state.Status != StatusFailed || state.CancelledAt == nil {
				t.Fatalf("lost cancellation/protection outcome: state=%+v err=%v", state, err)
			}
			if cleanup := state.Steps["release_resources"]; cleanup.Status != StatusCompleted {
				t.Fatalf("authored cleanup preceded sidecar shutdown or its receipt: %+v", cleanup)
			}
			awaitCommandCancellationFile(t, filepath.Join(e.config.ProjectDir, "resources-released"), "released")
			saved, loadErr := LoadState(e.config.ProjectDir, state.RunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			cleanup := saved.CommandExecutions["cleanup_sidecar"]
			if cleanup.Status != commandExecutionSettled || cleanup.SettledAt.IsZero() {
				t.Fatalf("cleanup-launched sidecar escaped the final join: %+v", cleanup)
			}
			mainReceipt := saved.CommandExecutions[main.ID]
			if unresolved {
				if workerErr := <-workerResult; workerErr != nil {
					t.Fatal(workerErr)
				}
				if !errors.Is(err, unknown) || mainReceipt.Status != commandExecutionUnresolved ||
					!mainReceipt.SettledAt.IsZero() || mainReceipt.Result.RerunOnResume {
					t.Fatalf("joining supervision fabricated settlement: receipt=%+v err=%v", mainReceipt, err)
				}
				retry := e.executeCommand(context.Background(), &main, workflow)
				if retry.Status != StatusFailed || retry.Error == nil || retry.Error.Type != "execution_unknown" {
					t.Fatalf("authored cleanup bypassed the unresolved replay fence: %+v", retry)
				}
			} else if mainReceipt.Status != commandExecutionSettled || mainReceipt.Result.Status != StatusCancelled {
				t.Fatalf("main sidecar cancellation lost its settled receipt: %+v", mainReceipt)
			}
		})
	}
}
