package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteCommandRetryPolicyRetriesExitCodeFailure(t *testing.T) {
	executor := newCommandTestExecutor(t)
	attemptsPath := filepath.Join(executor.config.ProjectDir, "retry-attempts.txt")
	workflow := &Workflow{
		Name:     "command-retry",
		Settings: DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "retry_exit",
			Command:    `printf 'attempt\n' >> retry-attempts.txt; exit 7`,
			OnError:    ErrorActionRetry,
			RetryCount: 3,
			RetryDelay: Duration{Duration: time.Millisecond},
		}},
	}
	executor.graph = NewDependencyGraph(workflow)

	result := executor.executeStep(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, StatusFailed)
	}
	if result.Attempts != 4 {
		t.Fatalf("attempts = %d, want initial attempt plus 3 retries", result.Attempts)
	}
	if result.Error == nil || result.Error.Type != "exit" {
		t.Fatalf("error = %#v, want exit error", result.Error)
	}
	if !strings.Contains(result.Error.Details, "exit_code=7") {
		t.Fatalf("error details = %q, want exit_code=7", result.Error.Details)
	}

	content, err := os.ReadFile(attemptsPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", attemptsPath, err)
	}
	if got := strings.Count(string(content), "attempt\n"); got != 4 {
		t.Fatalf("recorded attempts = %d, want 4; content = %q", got, content)
	}
}

func TestExecuteCommandContextCancellationReturnsCancelled(t *testing.T) {
	executor := newCommandTestExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	step := &Step{
		ID:      "cancel_command",
		Command: "printf started > started; sleep 60",
		Timeout: Duration{Duration: 30 * time.Second},
	}

	// Cancel only once the command is really running. A fixed delay raced the
	// durable launch journal on loaded hosts: a cancel that lands before
	// dispatch takes the separate skip-before-start path, which never starts
	// the command and so carries no cancelled step error.
	started := filepath.Join(executor.config.ProjectDir, "started")
	go func() {
		defer cancel()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(started); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	result := executor.executeCommand(ctx, step, &Workflow{Name: "command-cancel"})
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("command never started before cancellation: %v", err)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q; error = %#v", result.Status, StatusCancelled, result.Error)
	}
	if result.Error == nil || result.Error.Type != "cancelled" {
		t.Fatalf("error = %#v, want cancelled error", result.Error)
	}
}

func TestCommandReceiptRecoversResultBeforeSchedulerCheckpoint(t *testing.T) {
	e := newCommandTestExecutor(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "produce", Command: `printf 'once\n' >> effects; printf '{"answer":42}'`, OutputVar: "produced", OutputParse: OutputParse{Type: "json"}},
			{ID: "consume", DependsOn: []string{"produce"}, Command: `printf '%s' '${steps.produce.data.answer}' > dependent`},
		},
	}
	e.state.Status, e.state.Session = StatusRunning, e.config.Session
	e.state.InFlightSteps = map[string]InFlightStepState{"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: time.Now()}}
	result := e.executeCommand(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("command did not complete: %+v", result)
	}
	// The command has returned but its caller has not recorded the StepResult.
	// Load precisely the checkpoint written by the command's settlement path.
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := prior.Steps["produce"]; exists {
		t.Fatal("fixture already has the scheduler checkpoint")
	}
	final, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
	if err != nil || final.Status != StatusCompleted {
		t.Fatalf("receipt recovery failed: %+v %v", final, err)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
		t.Fatalf("receipt recovery repeated the command: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "dependent")); err != nil || string(data) != "42" {
		t.Fatalf("receipt recovery lost parsed output used by a dependent: %q %v", data, err)
	}
	if final.Variables["produced"] != `{"answer":42}` {
		t.Fatalf("receipt recovery lost the named output: %#v", final.Variables)
	}
}

func TestCommandLaunchRequiresDurableIntent(t *testing.T) {
	e := newCommandTestExecutor(t)
	blockCheckpointPath(t, e)
	step := Step{ID: "must-not-start", Command: "printf started > effects"}
	result := e.executeCommand(context.Background(), &step, &Workflow{Name: e.state.WorkflowID})
	if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "checkpoint" || !errors.Is(e.checkpointFailure(), ErrCheckpointFailed) {
		t.Fatalf("command ignored an unwritable launch journal: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "effects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran without a durable intent: %v", err)
	}
}

func TestCommandResumeRejectsUnknownEvidenceBeforeExplicitReset(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := Step{ID: "command", Command: "printf once >> effects"}
	workflow := &Workflow{Name: e.state.WorkflowID, Steps: []Step{step}}
	if result := e.executeCommand(context.Background(), &step, workflow); result.Status != StatusCompleted {
		t.Fatalf("command failed: %+v", result)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	record := prior.CommandExecutions[step.ID]
	record.Status = "future-unknown"
	prior.CommandExecutions[step.ID] = record
	if err := SaveState(e.config.ProjectDir, prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewExecutor(e.config).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Reset: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("explicit reset erased unrecognized evidence: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, prior.RunID))
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected reset changed the saved command evidence")
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err != nil || string(data) != "once" {
		t.Fatalf("rejected reset repeated work: %q %v", data, err)
	}
}

func TestCommandUnresolvedReceiptRequiresFailureEvidence(t *testing.T) {
	started := time.Now().Add(-time.Second)
	record := CommandExecutionState{
		Version: commandExecutionVersion, StepID: "work", StepHash: strings.Repeat("a", 64),
		Status: commandExecutionUnresolved, ProcessID: 123, StartedAt: started,
		Result: StepResult{
			StepID: "work", AgentType: "command", Status: StatusFailed,
			StartedAt: started, FinishedAt: time.Now(), Output: "partial output",
			Error: &StepError{Type: "execution_unknown", Message: "group observation failed", Details: "injected census failure"},
		},
	}
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled} {
		candidate := cloneCommandExecution(record)
		candidate.Result.Status = status
		if err := validateCommandExecution(candidate.StepID, candidate); err != nil {
			t.Fatalf("valid %s cleanup evidence rejected: %v", status, err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CommandExecutionState)
	}{
		{"success", func(r *CommandExecutionState) { r.Result.Status = StatusCompleted }},
		{"still running", func(r *CommandExecutionState) { r.Result.Status = StatusRunning }},
		{"settlement claim", func(r *CommandExecutionState) { r.SettledAt = time.Now() }},
		{"missing process", func(r *CommandExecutionState) { r.ProcessID = 0 }},
		{"missing failure", func(r *CommandExecutionState) { r.Result.Error = nil }},
		{"empty failure", func(r *CommandExecutionState) { r.Result.Error.Message = "" }},
		{"missing finish", func(r *CommandExecutionState) { r.Result.FinishedAt = time.Time{} }},
		{"reversed lifetime", func(r *CommandExecutionState) { r.Result.FinishedAt = started.Add(-time.Second) }},
		{"different launch", func(r *CommandExecutionState) { r.Result.StartedAt = started.Add(time.Millisecond) }},
		{"different step", func(r *CommandExecutionState) { r.Result.StepID = "other" }},
		{"different executor", func(r *CommandExecutionState) { r.Result.AgentType = "agent" }},
		{"replay permission", func(r *CommandExecutionState) { r.Result.RerunOnResume = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneCommandExecution(record)
			tc.mutate(&candidate)
			if err := validateCommandExecution(candidate.StepID, candidate); err == nil {
				t.Fatalf("accepted inconsistent unresolved cleanup evidence: %+v", candidate)
			}
		})
	}
}

func TestCommandUnresolvedReceiptBlocksReplayAndSupportsOperatorRecovery(t *testing.T) {
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			cfg, workflow, prior, output := commandOperatorRecoveryFixture(t, 0, "direct")
			running := commandOperatorRecoveryEntry(t, cfg)
			record := prior.CommandExecutions[running.StepID]
			// The fixture really ran the original command once and joined it.
			// Model an ended supervisor that could not prove its cleanup; its
			// partial result must never become a substitute for that proof.
			record.Status = commandExecutionUnresolved
			record.Result = StepResult{
				StepID: record.StepID, AgentType: "command", Status: status,
				StartedAt: record.StartedAt, FinishedAt: time.Now(), Output: output[:5],
				Error: &StepError{Type: "execution_unknown", Message: "command group could not be observed", Details: "injected final census failure"},
			}
			prior.Status = status
			prior.CommandExecutions[record.StepID] = record
			prior.Steps[record.StepID] = record.Result
			delete(prior.InFlightSteps, record.StepID)
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			saved, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			durable := saved.CommandExecutions[record.StepID]
			if durable.Status != commandExecutionUnresolved || !durable.SettledAt.IsZero() ||
				durable.Result.Status != status || durable.Result.Output != output[:5] ||
				durable.Result.Error == nil || durable.Result.Error.Details != "injected final census failure" {
				t.Fatalf("lost unresolved command evidence across checkpoint round trip: %+v", durable)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, saved, nil); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
				t.Fatalf("ordinary resume accepted an unresolved terminal receipt: %v", err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected resume rewrote unresolved cleanup evidence")
			}
			retry := NewExecutor(cfg)
			retry.state, retry.graph = saved, NewDependencyGraph(workflow)
			result := retry.executeStep(context.Background(), &workflow.Steps[0], workflow)
			if result.Attempts != 1 || result.Status != StatusFailed || result.Error == nil || result.Error.Type != "execution_unknown" {
				t.Fatalf("automatic retry accepted an unresolved terminal receipt: %+v", result)
			}
			entry := commandOperatorRecoveryEntry(t, cfg)
			if entry.Status != commandExecutionUnresolved || !entry.CanAccept || entry.ReceiptToken != running.ReceiptToken {
				t.Fatalf("ended supervision changed the command identity or hid recovery: %+v", entry)
			}
			accepted, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID,
				entry.StepID, entry.ReceiptToken, "Verified the original group ended and recovered its complete output", 0, output)
			if err != nil || accepted.Replayed || accepted.Command.Status != commandExecutionSettled {
				t.Fatalf("explicit recovery of unresolved supervision failed: %+v %v", accepted, err)
			}
			resolved, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "resume-allowed"), []byte("allow"), 0600); err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(cfg).Resume(context.Background(), workflow, resolved, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps[entry.StepID].Output != strings.TrimSpace(output) {
				t.Fatalf("resume did not use the explicitly accepted complete outcome: %+v %v", final, err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("receipt reconciliation replayed original work: %q %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "dependent")); err != nil || string(data) != "42" {
				t.Fatalf("dependent consumed partial unresolved output: %q %v", data, err)
			}
		})
	}
}

func TestCommandResumeProtectsLiveWaitNoneAfterSchedulerCompletion(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := Step{ID: "sidecar", Command: "sleep 60", Wait: WaitNone}
	workflow := &Workflow{Name: e.state.WorkflowID, Steps: []Step{step}}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		e.backgroundCommandWG.Wait()
	}()
	result := e.executeCommand(ctx, &step, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("WaitNone did not dispatch: %+v", result)
	}
	e.state.Steps[step.ID] = result
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.InFlightSteps) != 0 || prior.Steps[step.ID].Status != StatusCompleted {
		t.Fatal("fixture has not reached synchronous WaitNone completion")
	}
	checker := NewExecutor(e.config)
	checker.state = prior
	if err := checker.applyResumeOptions(workflow, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "unresolved launch") {
		t.Fatalf("completed scheduler bookkeeping hid a live command: %v", err)
	}
	cancel()
	e.backgroundCommandWG.Wait()
	if err := checker.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
		t.Fatalf("explicit replay was rejected after stopping the old work: %v", err)
	}
	if _, exists := checker.state.CommandExecutions[step.ID]; exists || !shouldRerunStep(checker.state.Steps[step.ID]) {
		t.Fatal("explicit replay retained the live receipt or skipped the completed WaitNone leaf")
	}
}

func TestCommandReplayCannotEraseWorkBehindCompletedContainer(t *testing.T) {
	for _, kind := range []string{"parallel", "foreach_rounds"} {
		t.Run(kind, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			body := Step{ID: "sidecar", Command: "printf 'once\\n' >> effects; sleep 60", Wait: WaitNone}
			// A parallel group's return cancels its child contexts. Keep it
			// alive until the sidecar has actually produced its external effect.
			ready := Step{ID: "ready", Command: `expected=1; if [ -f allow ]; then expected=2; fi; while [ ! -f effects ] || [ "$(wc -l < effects)" -lt "$expected" ]; do sleep 0.01; done`}
			group := Step{ID: "group", Parallel: ParallelSpec{Steps: []Step{body, ready}}}
			perRun := 1
			if kind == "foreach_rounds" {
				perRun = 2
				group = Step{ID: "group", Foreach: &ForeachConfig{Items: "${vars.items}", MaxRounds: IntOrExpr{Value: 2}, Steps: []Step{body}}}
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{
				group,
				{ID: "gate", DependsOn: []string{"group"}, Command: fmt.Sprintf("while [ ! -f effects ] || [ \"$(wc -l < effects)\" -lt %d ]; do sleep 0.01; done; if [ ! -f allow ]; then exit 7; fi; while [ \"$(wc -l < effects)\" -lt %d ]; do sleep 0.01; done", perRun, 2*perRun)},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			initial, err := NewExecutor(e.config).Run(ctx, workflow, map[string]interface{}{"items": []interface{}{"item"}}, nil)
			if err == nil || initial.Status != StatusFailed || initial.Steps["group"].Status != StatusCompleted {
				t.Fatalf("fixture did not complete the container before failing: %+v %v", initial, err)
			}
			prior, err := LoadState(e.config.ProjectDir, initial.RunID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, initial.RunID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewExecutor(e.config).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Mode: ResumeModeRestartFailed}, nil); err == nil || !strings.Contains(err.Error(), "cannot replay command") {
				t.Fatalf("leaf-only replay silently skipped its completed container: %v", err)
			}
			after, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, initial.RunID))
			if err != nil || string(before) != string(after) {
				t.Fatal("rejected leaf replay erased command receipts or container progress")
			}
			if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "allow"), []byte("allow"), 0600); err != nil {
				t.Fatal(err)
			}
			prior, err = LoadState(e.config.ProjectDir, initial.RunID)
			if err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(e.config).ResumeWithOptions(ctx, workflow, prior, ResumeOptions{Reset: true}, nil)
			if err != nil || final.Status != StatusCompleted {
				t.Fatalf("explicit whole-run replay failed: %+v %v", final, err)
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err != nil || strings.Count(string(data), "once\n") != 2*perRun {
				t.Fatalf("explicit replay did not repeat the nested work exactly once: %q %v", data, err)
			}
		})
	}
}

func TestCommandCleanupRunsForEveryResumedAttempt(t *testing.T) {
	for _, kind := range []string{"direct", "parallel", "foreach_rounds", "success_foreach_rounds"} {
		t.Run(kind, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			cleanup := Step{ID: "release", Command: `printf 'released\n' >> cleanups`}
			perAttempt := 1
			switch kind {
			case "parallel":
				cleanup = Step{ID: "cleanup_group", Parallel: ParallelSpec{Steps: []Step{cleanup}}}
			case "foreach_rounds", "success_foreach_rounds":
				perAttempt = 2
				cleanup = Step{ID: "cleanup_group", Foreach: &ForeachConfig{Items: "${vars.resources}", MaxRounds: IntOrExpr{Value: 2}, Steps: []Step{cleanup}}}
				if kind == "success_foreach_rounds" {
					cleanup = Step{ID: "cleanup_start", Command: "true", OnSuccess: []Step{cleanup}}
				}
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
				Steps: []Step{{ID: "work", Command: `printf 'acquired\n' >> attempts; sleep 60`}},
			}
			workflow.Settings.OnCancel = []Step{cleanup}
			workflow.Settings.OnCancelTimeout = Duration{Duration: 2 * time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			initial, err := NewExecutor(e.config).Run(ctx, workflow, map[string]interface{}{"resources": []interface{}{"resource"}}, nil)
			cancel()
			if err == nil || initial == nil || initial.Status != StatusCancelled {
				t.Fatalf("initial attempt did not cancel: %+v %v", initial, err)
			}
			prior, err := LoadState(e.config.ProjectDir, initial.RunID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), 750*time.Millisecond)
			resumed, err := NewExecutor(e.config).Resume(ctx, workflow, prior, nil)
			cancel()
			if err == nil || resumed == nil || resumed.Status != StatusCancelled {
				t.Fatalf("resumed attempt did not cancel: %+v %v", resumed, err)
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "attempts")); err != nil || strings.Count(string(data), "acquired\n") != 2 {
				t.Fatalf("work did not reacquire its resource on resume: %q %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "cleanups")); err != nil || strings.Count(string(data), "released\n") != 2*perAttempt {
				t.Fatalf("each cancelled attempt must run its cleanup: %q %v", data, err)
			}
		})
	}
}

func TestCommandResumePreservesUnresolvedCleanup(t *testing.T) {
	e := newCommandTestExecutor(t)
	cleanup := Step{ID: "release", Command: "sleep 60", Wait: WaitNone}
	workflow := &Workflow{Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{{ID: "work", Command: "printf unsafe > effects"}}}
	workflow.Settings.OnCancel = []Step{cleanup}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		e.backgroundCommandWG.Wait()
	}()
	result := e.executeCommand(ctx, &cleanup, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("cleanup did not launch: %+v", result)
	}
	e.state.Steps[cleanup.ID] = result
	if err := e.persistState(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, e.state.RunID))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
		prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewExecutor(e.config).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Mode: mode}, nil); err == nil {
			t.Fatalf("%s forgot an unresolved cleanup process", mode)
		}
		after, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, e.state.RunID))
		if err != nil || string(before) != string(after) {
			t.Fatalf("%s changed unresolved cleanup evidence", mode)
		}
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "effects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume dispatched while old cleanup was unresolved: %v", err)
	}
}

func TestCommandCleanupResetPreservesMainNamespaceCollision(t *testing.T) {
	e := newCommandTestExecutor(t)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{
		{ID: "cleanup_group_release", Command: `printf 'once\n' >> main_effects`},
		{ID: "gate", DependsOn: []string{"cleanup_group_release"}, Command: "test -f allow"},
	}}
	workflow.Settings.OnCancel = []Step{{ID: "cleanup_group", Parallel: ParallelSpec{Steps: []Step{
		{ID: "release", Command: "printf released > cleanup_effects"},
	}}}}
	if validation := Validate(workflow); !validation.Valid {
		t.Fatalf("fixture must use an accepted workflow namespace: %+v", validation.Errors)
	}
	initial, err := NewExecutor(e.config).Run(context.Background(), workflow, nil, nil)
	if err == nil || initial.Status != StatusFailed {
		t.Fatalf("fixture did not fail its gate: %+v %v", initial, err)
	}
	prior, err := LoadState(e.config.ProjectDir, initial.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "allow"), []byte("allow"), 0600); err != nil {
		t.Fatal(err)
	}
	resumed, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
	if err != nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume failed: %+v %v", resumed, err)
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "main_effects")); err != nil || string(data) != "once\n" {
		t.Fatalf("cleanup namespace erased and repeated completed main work: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "cleanup_effects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup unexpectedly ran without cancellation: %v", err)
	}
}

// Capture the real running journal while a command waits after performing its
// external effect. Let the group finish, then restore that captured checkpoint
// to model losing the final settlement write, without fabricating launch data or
// leaving an orphan process. Direct execution also permits testing an actual
// nonzero result without the original scheduler consuming its retry budget.
func commandOperatorRecoveryFixture(t *testing.T, exitCode int, layout string) (ExecutorConfig, *Workflow, *ExecutionState, string) {
	t.Helper()
	e := newCommandTestExecutor(t)
	e.config.RunID = e.state.RunID
	output := " \n{\"answer\":42}\n"
	produce := Step{ID: "produce", Command: fmt.Sprintf("printf 'once\\n' >> effects; while [ ! -f release ]; do sleep 0.01; done; printf ' \\n{\"answer\":42}\\n'; exit %d", exitCode),
		OutputVar: "produced", OutputParse: OutputParse{Type: "json"}, OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings()}
	workflow.Settings.Limits.MaxCommandStdoutBytes = 64
	dependency := "produce"
	switch layout {
	case "foreach":
		dependency = "group"
		workflow.Steps = []Step{{ID: dependency, Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{produce}}}}
	case "parallel":
		dependency = "group"
		workflow.Steps = []Step{{ID: dependency, Parallel: ParallelSpec{Steps: []Step{produce}}}}
	case "branch":
		dependency = "route"
		workflow.Steps = []Step{{ID: dependency, Branch: "$(printf b)", Branches: map[string]interface{}{
			"a": []Step{{ID: "produce", Prompt: "Must not send this unselected alternative", Pane: PaneSpec{Index: 1}}},
			"b": []Step{produce},
		}}}
	case "post":
		workflow.Steps = []Step{{ID: "main", Command: "true"}}
		workflow.PostPipelineSteps = []Step{produce}
	default:
		workflow.Steps = []Step{produce}
	}
	consumer := Step{ID: "consume", DependsOn: []string{dependency}, Command: "test -f resume-allowed && printf done > dependent"}
	if layout == "direct" {
		consumer.Command = "test -f resume-allowed && printf '%s' '${steps.produce.data.answer}' > dependent"
	}
	if layout != "post" {
		workflow.Steps = append(workflow.Steps, consumer)
	}
	frozen, path, err := SnapshotWorkflow(context.Background(), e.config.ProjectDir, workflow)
	if err != nil {
		t.Fatal(err)
	}
	e.config.WorkflowFile = path
	e.state.WorkflowFile, e.state.Session, e.state.Status, e.state.StartedAt = path, e.config.Session, StatusRunning, time.Now()
	e.state.InFlightSteps = map[string]InFlightStepState{"produce": {StepID: "produce", Kind: StepKindCommand, StartedAt: time.Now()}}
	e.graph, e.limits = NewDependencyGraph(frozen), frozen.Settings.Limits.EffectiveLimits()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		if layout == "direct" {
			result := e.executeCommand(ctx, &frozen.Steps[0], frozen)
			if (exitCode == 0 && result.Status != StatusCompleted) || (exitCode != 0 && result.Status != StatusFailed) {
				done <- fmt.Errorf("unexpected original command outcome: %+v", result)
				return
			}
			done <- nil
			return
		}
		_, err := e.Run(ctx, frozen, map[string]interface{}{"items": []interface{}{"only"}}, nil)
		done <- err
	}()
	var captured *ExecutionState
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		saved, err := LoadState(e.config.ProjectDir, e.config.RunID)
		if err == nil {
			for _, record := range saved.CommandExecutions {
				if record.Status == commandExecutionRunning {
					if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err == nil && string(data) == "once\n" {
						captured = saved
					}
				}
			}
		}
		if captured != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if captured == nil {
		t.Fatal("original command never published its running receipt and effect")
	}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if layout == "direct" && err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original command group did not finish")
	}
	if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
		t.Fatalf("fixture must perform its effect exactly once: %q %v", data, err)
	}
	if err := SaveState(e.config.ProjectDir, captured); err != nil {
		t.Fatal(err)
	}
	return e.config, frozen, captured, output
}

func commandOperatorRecoveryEntry(t *testing.T, cfg ExecutorConfig) CommandExecutionRecoveryEntry {
	t.Helper()
	inspection, err := InspectCommandExecutionRecovery(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil || len(inspection.Commands) != 1 {
		t.Fatalf("inspect command recovery: %+v %v", inspection, err)
	}
	return inspection.Commands[0]
}

func commandOperatorRecoveryBytes(t *testing.T, cfg ExecutorConfig) []byte {
	t.Helper()
	data, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCommandOperatorRecoveryPublishesOnlyOnResume(t *testing.T) {
	for _, layout := range []string{"direct", "foreach", "parallel", "branch"} {
		t.Run(layout, func(t *testing.T) {
			cfg, workflow, prior, output := commandOperatorRecoveryFixture(t, 0, layout)
			before := commandOperatorRecoveryBytes(t, cfg)
			entry := commandOperatorRecoveryEntry(t, cfg)
			if !entry.CanAccept || entry.Status != commandExecutionRunning || entry.ProcessID <= 0 || len(entry.ReceiptToken) != 64 {
				t.Fatalf("original command not recoverable: %+v", entry)
			}
			encoded, err := json.Marshal(entry)
			if err != nil || bytes.Contains(encoded, []byte("answer")) || bytes.Contains(encoded, []byte("printf")) || bytes.Contains(encoded, []byte("step_hash")) {
				t.Fatalf("inspection exposed command payload: %s %v", encoded, err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("read-only inspection changed checkpoint")
			}
			const reason = "Verified the original command group ended and its complete result"
			accepted, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, output)
			if err != nil || accepted.Replayed || accepted.Command.ExitCode == nil || *accepted.Command.ExitCode != 0 || accepted.Command.OutputBytes != len(output) {
				t.Fatalf("accept command result: %+v %v", accepted, err)
			}
			saved, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil || saved.Status != prior.Status || saved.Steps[entry.StepID].Status == StatusCompleted || saved.Variables["produced"] != nil {
				t.Fatalf("acceptance published provisional scheduler output: %+v %v", saved, err)
			}
			record := saved.CommandExecutions[entry.StepID]
			if record.Result.Output != output || record.Result.ParsedData != nil || record.ResolutionOutputHash == "" {
				t.Fatalf("acceptance lost exact raw evidence: %+v", record)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "dependent")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("acceptance executed dependent work: %v", err)
			}
			acceptedBytes := commandOperatorRecoveryBytes(t, cfg)
			replayed, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, output)
			if err != nil || !replayed.Replayed || !bytes.Equal(acceptedBytes, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatalf("exact acceptance did not replay unchanged: %+v %v", replayed, err)
			}
			if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "resume-allowed"), []byte("allow"), 0600); err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(cfg).Resume(context.Background(), workflow, saved, nil)
			if err != nil || final.Status != StatusCompleted {
				t.Fatalf("normal resume did not consume operator outcome: %+v %v", final, err)
			}
			if got := final.Steps[entry.StepID]; got.Status != StatusCompleted || got.Output != strings.TrimSpace(output) || got.ParsedData == nil {
				t.Fatalf("normal parser/result path was bypassed: %+v", got)
			}
			want := "done"
			if layout == "direct" {
				want = "42"
				if final.Variables["produced"] != strings.TrimSpace(output) {
					t.Fatalf("named output missing: %#v", final.Variables)
				}
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "dependent")); err != nil || string(data) != want {
				t.Fatalf("dependent did not receive recovered output: %q %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("resume repeated original command side effects: %q %v", data, err)
			}
			completed := commandOperatorRecoveryBytes(t, cfg)
			replayed, err = AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, output)
			if err != nil || !replayed.Replayed || !replayed.Command.ResolvedAt.Equal(accepted.Command.ResolvedAt) || !bytes.Equal(completed, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatalf("completed decision lost immutable replay: %+v %v", replayed, err)
			}
		})
	}
}

func TestCommandOperatorRecoveryAcceptedFailureDoesNotRetry(t *testing.T) {
	cfg, workflow, _, output := commandOperatorRecoveryFixture(t, 7, "direct")
	entry := commandOperatorRecoveryEntry(t, cfg)
	if _, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, "Verified the group ended with exit 7", 7, output); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
	if err == nil || final.Status != StatusFailed {
		t.Fatalf("accepted exit failure became success: %+v %v", final, err)
	}
	result := final.Steps[entry.StepID]
	if result.Attempts != 1 || result.Error == nil || result.Error.Type != "exit" ||
		result.Error.Message != `command step "produce" failed: exit_code=7` ||
		result.Error.Details != "kind=command step_id=produce reason=exit_code=7 hint=inspect stdout/stderr and command arguments details=exit_code=7" ||
		result.Output != strings.TrimSpace(output) || result.ParsedData != nil {
		t.Fatalf("accepted failure did not retain normal command semantics: %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
		t.Fatalf("automatic retry repeated the accepted command: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "dependent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("accepted failure ran its dependent: %v", err)
	}
}

func TestCommandOperatorRecoveryRefusalsPreserveCheckpoint(t *testing.T) {
	for _, scenario := range []string{"stale receipt", "live owner", "background", "terminal step", "completed container", "completed iteration", "post-pipeline", "missing workflow", "output limit", "changed reason", "changed output", "changed exit", "canceled context"} {
		t.Run(scenario, func(t *testing.T) {
			layout := "direct"
			if scenario == "completed container" || scenario == "completed iteration" {
				layout = "foreach"
			}
			if scenario == "post-pipeline" {
				layout = "post"
			}
			cfg, _, prior, output := commandOperatorRecoveryFixture(t, 0, layout)
			entry := commandOperatorRecoveryEntry(t, cfg)
			reason, exitCode, token := "Verified the group ended", 0, entry.ReceiptToken
			ctx := context.Background()
			switch scenario {
			case "stale receipt":
				if token[0] == '0' {
					token = "1" + token[1:]
				} else {
					token = "0" + token[1:]
				}
			case "live owner":
				owner, err := AcquireRunControl(ctx, cfg.ProjectDir, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(owner.Close)
				if err := os.WriteFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID), []byte("not a checkpoint"), 0600); err != nil {
					t.Fatal(err)
				}
			case "background":
				record := prior.CommandExecutions[entry.StepID]
				record.Background = true
				prior.CommandExecutions[entry.StepID] = record
				if err := SaveState(cfg.ProjectDir, prior); err != nil {
					t.Fatal(err)
				}
				entry = commandOperatorRecoveryEntry(t, cfg)
				token = entry.ReceiptToken
			case "terminal step", "completed container":
				id := entry.StepID
				if scenario == "completed container" {
					id = "group"
				}
				prior.Steps[id] = StepResult{StepID: id, Status: StatusCompleted, FinishedAt: time.Now()}
				if err := SaveState(cfg.ProjectDir, prior); err != nil {
					t.Fatal(err)
				}
				entry = commandOperatorRecoveryEntry(t, cfg)
			case "completed iteration":
				progress := prior.ForeachState["group"]
				progress.CompletedIterationIDs = []string{"group_iter0"}
				prior.ForeachState["group"] = progress
				if err := SaveState(cfg.ProjectDir, prior); err != nil {
					t.Fatal(err)
				}
				entry = commandOperatorRecoveryEntry(t, cfg)
			case "missing workflow":
				if err := os.Rename(prior.WorkflowFile, prior.WorkflowFile+".unavailable"); err != nil {
					t.Fatal(err)
				}
				entry = commandOperatorRecoveryEntry(t, cfg)
			case "output limit":
				output = strings.Repeat("x", 65)
			case "changed reason", "changed output", "changed exit":
				if _, err := AcceptCommandExecutionContext(ctx, cfg.ProjectDir, cfg.RunID, entry.StepID, token, reason, exitCode, output); err != nil {
					t.Fatal(err)
				}
				if scenario == "changed reason" {
					reason += " differently"
				}
				if scenario == "changed output" {
					output = strings.TrimSpace(output)
				}
				if scenario == "changed exit" {
					exitCode = 7
				}
			case "canceled context":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			switch scenario {
			case "background", "terminal step", "completed container", "completed iteration", "post-pipeline", "missing workflow":
				if entry.CanAccept || entry.RefusalReason == "" {
					t.Fatalf("unsupported receipt advertised acceptance: %+v", entry)
				}
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			_, err := AcceptCommandExecutionContext(ctx, cfg.ProjectDir, cfg.RunID, entry.StepID, token, reason, exitCode, output)
			if err == nil {
				t.Fatal("unsafe command acceptance succeeded")
			}
			if scenario == "live owner" && !errors.Is(err, ErrRunAlreadyOwned) {
				t.Fatalf("ownership did not precede checkpoint loading: %v", err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("refused command recovery changed the checkpoint")
			}
		})
	}
}

func TestCommandOperatorRecoveryRetainsSessionIdentity(t *testing.T) {
	cfg, workflow, _, output := commandOperatorRecoveryFixture(t, 0, "direct")
	entry := commandOperatorRecoveryEntry(t, cfg)
	if _, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, "Verified the complete result", 0, output); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before := commandOperatorRecoveryBytes(t, cfg)
	cfg.Session = "replacement-session"
	_, err = NewExecutor(cfg).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{OnRosterChange: ResumeRosterProceed}, nil)
	if err == nil || !strings.Contains(err.Error(), "explicit reset") {
		t.Fatalf("resume rebound immutable command receipt to another session: %v", err)
	}
	if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
		t.Fatal("refused roster change rewrote original decision")
	}
}

func TestCommandOperatorRecoveryRejectsCorruptResolutionBeforeReset(t *testing.T) {
	for _, corrupt := range []string{"output", "parsed data", "token", "exit code", "timestamp"} {
		t.Run(corrupt, func(t *testing.T) {
			cfg, workflow, _, output := commandOperatorRecoveryFixture(t, 0, "direct")
			entry := commandOperatorRecoveryEntry(t, cfg)
			if _, err := AcceptCommandExecutionContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, "Verified the complete result", 0, output); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			record := prior.CommandExecutions[entry.StepID]
			switch corrupt {
			case "output":
				record.Result.Output += "changed"
			case "parsed data":
				record.Result.ParsedData = map[string]interface{}{"unverified": true}
			case "token":
				record.ResolutionToken = strings.Repeat("0", 64)
			case "exit code":
				record.ResolutionExitCode = 7
			case "timestamp":
				record.ResolvedAt = record.ResolvedAt.Add(time.Second)
			}
			prior.CommandExecutions[entry.StepID] = record
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			if _, err := InspectCommandExecutionRecovery(context.Background(), cfg.ProjectDir, cfg.RunID); err == nil {
				t.Fatal("inspection accepted corrupt operator evidence")
			}
			if _, err := NewExecutor(cfg).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{Reset: true}, nil); err == nil {
				t.Fatal("explicit reset erased invalid operator evidence")
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected corrupt state was rewritten")
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("corrupt recovery repeated command: %q %v", data, err)
			}
		})
	}
}
