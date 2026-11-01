//go:build unix

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func stepAdmissionIntegrationConfig(t *testing.T) ExecutorConfig {
	t.Helper()
	cfg := DefaultExecutorConfig("step-admission")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-step-admission"
	cfg.DefaultTimeout, cfg.GlobalTimeout = 5*time.Second, 15*time.Second
	return cfg
}

func stepAdmissionResume(t *testing.T, cfg ExecutorConfig, opts ResumeOptions) (*ExecutionState, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := AcquireRunControl(ctx, cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("load frozen recovery workflow: %+v %v", validation, err)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	return NewExecutor(cfg).ResumeWithOptions(owner.Context(), frozen, prior, opts, nil)
}

func requireStepAdmissionSets(t *testing.T, state *ExecutionState, work, finalization []string) {
	t.Helper()
	if state == nil || state.StepAdmissions == nil || state.StepAdmissions.Version != 1 {
		t.Fatalf("missing durable runtime admission ledger: %+v", state)
	}
	for _, phase := range []struct {
		name string
		got  map[string]bool
		want []string
	}{{"work", state.StepAdmissions.Work, work}, {"finalization", state.StepAdmissions.Finalization, finalization}} {
		if len(phase.got) != len(phase.want) {
			t.Fatalf("%s admission set = %v, want exactly %v", phase.name, phase.got, phase.want)
		}
		for _, id := range phase.want {
			if !phase.got[id] {
				t.Fatalf("%s admission set omitted %q: %v", phase.name, id, phase.got)
			}
		}
	}
	// Iteration bookkeeping can have its own in-flight frame, but stored
	// executable StepResults must never grow for rejected runtime identities.
	for id := range state.Steps {
		if !state.StepAdmissions.Work[id] && !state.StepAdmissions.Finalization[id] {
			t.Fatalf("unadmitted executable step %q gained a stored result", id)
		}
	}
}

func requireStepAdmissionEffect(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s effects = %q, want %q: %v", name, data, want, err)
	}
}

func requireNoStepAdmissionEffect(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unadmitted work produced %s: %v", name, err)
	}
}

func TestStepAdmissionExhaustionStopsContinueAndRetryBeforeChildResults(t *testing.T) {
	for _, action := range []ErrorAction{ErrorActionContinue, ErrorActionRetry} {
		t.Run(string(action), func(t *testing.T) {
			cfg := stepAdmissionIntegrationConfig(t)
			items := make([]string, 128)
			for i := range items {
				items[i] = fmt.Sprint(i)
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "bounded-continue-retry",
				Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
					MaxStepCountTotal: 2, MaxMaterializedStepNodes: 512,
				}},
				Steps: []Step{
					{ID: "expand", OnError: action, RetryCount: 4, RetryDelay: Duration{Duration: time.Millisecond},
						Foreach: &ForeachConfig{Items: "[" + strings.Join(items, ",") + "]", Parallel: true, MaxConcurrent: 2,
							Steps: []Step{{ID: "work", Command: "printf forbidden >> child-effects"}}}},
					{ID: "after", DependsOn: []string{"expand"}, Command: "printf forbidden > after-effects"},
				},
			}
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
				t.Fatalf("%s swallowed the hard admission failure: %+v %v", action, state, err)
			}
			requireStepAdmissionSets(t, state, []string{"expand", "after"}, nil)
			if len(state.CommandExecutions) != 0 || len(state.CommandEvaluations) != 0 || len(state.Steps) != 1 {
				t.Fatalf("rejected expansion created child receipts/results or continued: steps=%v commands=%v selectors=%v", state.Steps, state.CommandExecutions, state.CommandEvaluations)
			}
			if result := state.Steps["expand"]; result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" {
				t.Fatalf("admission refusal became a successful skip or lost its diagnostic: %+v", result)
			}
			requireNoStepAdmissionEffect(t, cfg.ProjectDir, "child-effects")
			requireNoStepAdmissionEffect(t, cfg.ProjectDir, "after-effects")
			prior, loadErr := LoadState(cfg.ProjectDir, cfg.RunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			requireStepAdmissionSets(t, prior, []string{"expand", "after"}, nil)
		})
	}
}

func TestStepAdmissionSettledRetryReusesRuntimeIdentity(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-settled-retry",
		Settings: WorkflowSettings{Limits: LimitsConfig{MaxStepCountTotal: 1, MaxMaterializedStepNodes: 8}},
		Steps: []Step{{ID: "work", OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond},
			Command: "printf 'attempt\\n' >> attempts; if [ ! -f retry-ready ]; then printf ready > retry-ready; exit 7; fi; printf done > completed",
		}},
	}
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("settled retry consumed a second runtime admission: %+v %v", state, err)
	}
	requireStepAdmissionSets(t, state, []string{"work"}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "attempts", "attempt\nattempt\n")
	requireStepAdmissionEffect(t, cfg.ProjectDir, "completed", "done")
	if result := state.Steps["work"]; result.Attempts != 2 || result.Status != StatusCompleted {
		t.Fatalf("bounded retry changed ordinary attempt semantics: %+v", result)
	}
	if receipt := state.CommandExecutions["work"]; receipt.Status != commandExecutionSettled || receipt.Result.Status != StatusCompleted {
		t.Fatalf("retry did not retain the final settled command outcome: %+v", receipt)
	}
}

func TestStepAdmissionParallelContinueKeepsUnfinishedContainerResumable(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-parallel-continue",
		Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
			MaxStepCountTotal: 3, MaxMaterializedStepNodes: 32, SubstepParallelMax: 1,
		}},
		Steps: []Step{{ID: "batch", OnError: ErrorActionContinue, Parallel: ParallelSpec{Steps: []Step{
			{ID: "prefix", Command: "printf once >> prefix-effects"},
			{ID: "expand", DependsOn: []string{"prefix"}, Foreach: &ForeachConfig{Items: `["only"]`,
				Steps: []Step{{ID: "work", Command: "printf remaining >> child-effects"}}}},
		}}}},
	}
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
		t.Fatalf("parallel continue swallowed nested admission refusal: %+v %v", state, err)
	}
	for _, id := range []string{"batch", "batch_expand"} {
		result := state.Steps[id]
		if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" || result.SkipKind == SkipKindAdmissionDenied {
			t.Fatalf("partially executed container %q lost resumable admission failure: %+v", id, result)
		}
	}
	if state.Steps["batch_prefix"].Status != StatusCompleted {
		t.Fatalf("nested refusal changed completed prefix: %+v", state.Steps["batch_prefix"])
	}
	requireStepAdmissionSets(t, state, []string{"batch", "batch_prefix", "batch_expand"}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "prefix-effects", "once")
	requireNoStepAdmissionEffect(t, cfg.ProjectDir, "child-effects")
	const missing = "batch_expand_iter0_work"
	if _, exists := state.Steps[missing]; exists {
		t.Fatal("refused child gained a stored result")
	}
	if _, exists := state.CommandExecutions[missing]; exists {
		t.Fatal("refused child gained a launch receipt")
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	prefixReceipt := prior.CommandExecutions["batch_prefix"]
	if prefixReceipt.Status != commandExecutionSettled || prefixReceipt.Result.Status != StatusCompleted {
		t.Fatalf("prefix did not settle before refusal: %+v", prefixReceipt)
	}
	state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 4})
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("raised capacity did not reopen unfinished parallel work: %+v %v", state, err)
	}
	requireStepAdmissionSets(t, state, []string{"batch", "batch_prefix", "batch_expand", missing}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "prefix-effects", "once")
	requireStepAdmissionEffect(t, cfg.ProjectDir, "child-effects", "remaining")
	if !reflect.DeepEqual(prefixReceipt, state.CommandExecutions["batch_prefix"]) {
		t.Fatal("continuation replayed or replaced the already completed parallel prefix")
	}
}

func TestStepAdmissionForeachContinueKeepsUnfinishedRoundsResumable(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-rounds-continue",
		Defaults: map[string]interface{}{"rounds": 2},
		Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
			MaxStepCountTotal: 2, MaxMaterializedStepNodes: 32,
		}},
		Steps: []Step{{ID: "expand", OnError: ErrorActionContinue,
			Foreach: &ForeachConfig{Items: `["only"]`, MaxRounds: IntOrExpr{Expr: "${defaults.rounds}"},
				Steps: []Step{{ID: "work", Command: "printf '%s\\n' '${round}' >> round-effects"}}},
		}},
	}
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
		t.Fatalf("foreach continue swallowed round admission refusal: %+v %v", state, err)
	}
	if result := state.Steps["expand"]; result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" || result.SkipKind == SkipKindAdmissionDenied {
		t.Fatalf("partially executed rounds were treated as completed or never dispatched: %+v", result)
	}
	const first = "expand_iter0_work_round1"
	const second = "expand_iter0_work_round2"
	requireStepAdmissionSets(t, state, []string{"expand", first}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "round-effects", "1\n")
	progress := state.ForeachState["expand"]
	if progress.CompletedRounds["expand_iter0"] != 1 || len(progress.CompletedIterationIDs) != 0 {
		t.Fatalf("refused round advanced completed iteration evidence: %+v", progress)
	}
	if _, exists := state.Steps[second]; exists {
		t.Fatal("refused round gained a stored result")
	}
	if _, exists := state.CommandExecutions[second]; exists {
		t.Fatal("refused round gained a launch receipt")
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	firstReceipt := prior.CommandExecutions[first]
	if firstReceipt.Status != commandExecutionSettled || firstReceipt.Result.Status != StatusCompleted {
		t.Fatalf("first round did not settle before refusal: %+v", firstReceipt)
	}
	state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 3})
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("raised capacity did not finish the unadmitted round: %+v %v", state, err)
	}
	requireStepAdmissionSets(t, state, []string{"expand", first, second}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "round-effects", "1\n2\n")
	if !reflect.DeepEqual(firstReceipt, state.CommandExecutions[first]) {
		t.Fatal("continuation replayed or replaced the already completed round")
	}
}

func TestStepAdmissionLoopContinueKeepsUnfinishedIterationResumable(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-loop-continue",
		Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
			MaxStepCountTotal: 3, MaxMaterializedStepNodes: 32,
		}},
		Steps: []Step{{ID: "batch", OnError: ErrorActionContinue, Loop: &LoopConfig{Items: `["only"]`, Steps: []Step{
			{ID: "prefix", Command: "printf once >> prefix-effects"},
			{ID: "expand", Foreach: &ForeachConfig{Items: `["nested"]`,
				Steps: []Step{{ID: "work", Command: "printf remaining >> child-effects"}}}},
		}}}},
	}
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
		t.Fatalf("loop continue swallowed nested admission refusal: %+v %v", state, err)
	}
	for _, id := range []string{"batch", "batch_iter0_expand"} {
		result := state.Steps[id]
		if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" || result.SkipKind == SkipKindAdmissionDenied {
			t.Fatalf("partially executed loop container %q lost resumable admission failure: %+v", id, result)
		}
	}
	const prefix = "batch_iter0_prefix"
	const missing = "batch_iter0_expand_iter0_work"
	if state.Steps[prefix].Status != StatusCompleted {
		t.Fatalf("nested refusal changed completed prefix: %+v", state.Steps[prefix])
	}
	if progress := state.ForeachState["batch"]; len(progress.CompletedIterationIDs) != 0 {
		t.Fatalf("unfinished loop iteration gained completion evidence: %+v", progress)
	}
	requireStepAdmissionSets(t, state, []string{"batch", prefix, "batch_iter0_expand"}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "prefix-effects", "once")
	requireNoStepAdmissionEffect(t, cfg.ProjectDir, "child-effects")
	if _, exists := state.Steps[missing]; exists {
		t.Fatal("refused nested child gained a stored result")
	}
	if _, exists := state.CommandExecutions[missing]; exists {
		t.Fatal("refused nested child gained a launch receipt")
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	prefixReceipt := prior.CommandExecutions[prefix]
	if prefixReceipt.Status != commandExecutionSettled || prefixReceipt.Result.Status != StatusCompleted {
		t.Fatalf("loop prefix did not settle before refusal: %+v", prefixReceipt)
	}
	state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 4})
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("raised capacity did not reopen unfinished loop work: %+v %v", state, err)
	}
	requireStepAdmissionSets(t, state, []string{"batch", prefix, "batch_iter0_expand", missing}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "prefix-effects", "once")
	requireStepAdmissionEffect(t, cfg.ProjectDir, "child-effects", "remaining")
	if !reflect.DeepEqual(prefixReceipt, state.CommandExecutions[prefix]) {
		t.Fatal("continuation replayed or replaced the already completed loop prefix")
	}
}

func TestStepAdmissionParallelContinuePreservesDeniedRecoveryFailure(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "recover.md"), []byte("Recover the original command failure."), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-fallback-continue",
		Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
			MaxStepCountTotal: 2, MaxMaterializedStepNodes: 16,
		}},
		Steps: []Step{{ID: "batch", OnError: ErrorActionContinue, Parallel: ParallelSpec{Steps: []Step{
			{ID: "failure", Command: "printf once >> failed-effects; printf original; exit 7",
				OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
					"pane": 1, "template": "recover.md",
				}}},
		}}}},
	}
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
		t.Fatalf("continue hid denied failure recovery: %+v %v", state, err)
	}
	if result := state.Steps["batch"]; result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" || result.SkipKind == SkipKindAdmissionDenied {
		t.Fatalf("entered parent hid the denied fallback: %+v", result)
	}
	const original = "batch_failure"
	const fallback = original + ".on_failure"
	requireStepAdmissionSets(t, state, []string{"batch", original}, nil)
	requireStepAdmissionEffect(t, cfg.ProjectDir, "failed-effects", "once")
	if _, exists := state.Steps[fallback]; exists {
		t.Fatal("denied fallback gained a stored result")
	}
	if len(state.AgentDeliveries) != 0 {
		t.Fatalf("denied fallback gained a delivery receipt: %+v", state.AgentDeliveries)
	}
	if history, err := mock.PasteHistory("%1"); err != nil || len(history) != 0 {
		t.Fatalf("denied fallback dispatched a prompt: %v %v", history, err)
	}
	receipt := state.CommandExecutions[original]
	if receipt.Status != commandExecutionSettled || receipt.Result.Status != StatusFailed || receipt.Result.Output != "original" || receipt.Result.Error == nil || receipt.Result.Error.Type != "exit" {
		t.Fatalf("denied fallback changed the settled native failure: %+v", receipt)
	}
	if len(receipt.Result.Error.Aggregated) != 0 || strings.Contains(receipt.Result.Error.Details, "on_failure") || strings.Contains(receipt.Result.Error.Details, "max_step_count_total") {
		t.Fatalf("fallback diagnostics mutated the original native receipt: %+v", receipt.Result.Error)
	}
	result := state.Steps[original]
	if result.Status != StatusFailed || result.Error == nil || result.Error.Type != receipt.Result.Error.Type || result.Error.Message != receipt.Result.Error.Message || result.Output != receipt.Result.Output || result.SkipKind == SkipKindAdmissionDenied {
		t.Fatalf("fallback refusal erased the original command failure: %+v", result)
	}
}

func TestStepAdmissionPreCancelledReservationResumeImportsLegacyCleanupHistory(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	request := FileReservationPathsStep{ProjectKey: cfg.ProjectDir, AgentName: "BlueLake", Paths: StringOrList{"protected.go"}}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "pre-cancelled-legacy-admission",
		Settings: WorkflowSettings{
			Limits: LimitsConfig{MaxStepCountTotal: 3, MaxMaterializedStepNodes: 32},
			OnCancel: []Step{
				{ID: "cleanup", Loop: &LoopConfig{Items: `[0,1]`, Steps: []Step{
					{ID: "cleanup_leaf", Command: "printf '%s\\n' '${loop.index}' >> cleanup-effects"},
				}}},
				{ID: "release", Command: "printf released > release-effects"},
			},
		},
		Steps: []Step{{ID: "reserve", FileReservationPaths: &request}},
	}
	frozen, path, err := SnapshotWorkflow(context.Background(), cfg.ProjectDir, workflow)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkflowFile = path
	started, finished := time.Now().Add(-time.Minute), time.Now().Add(-30*time.Second)
	reservationHash, err := agentStepHash(&frozen.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	reservation := ReservationStepState{
		Version: reservationStepVersion, StepID: "reserve", StepHash: reservationHash,
		Kind: StepKindReservationAcquire, Status: reservationRejected, Request: request,
		StartedAt: started, FinishedAt: finished,
		Result: StepResult{StepID: "reserve", Status: StatusFailed, StartedAt: started, FinishedAt: finished},
	}
	if err := validateReservationStep("reserve", reservation); err != nil {
		t.Fatal(err)
	}
	const oldID = "cleanup_iter0_cleanup_leaf"
	const newID = "cleanup_iter1_cleanup_leaf"
	oldStep := frozen.Settings.OnCancel[0].Loop.Steps[0]
	oldStep.ID = oldID
	oldHash, err := commandDefinitionHash(&oldStep)
	if err != nil {
		t.Fatal(err)
	}
	command := CommandExecutionState{
		Version: commandExecutionVersion, StepID: oldID, StepHash: oldHash, Status: commandExecutionSettled,
		StartedAt: started, SettledAt: finished,
		Result: StepResult{StepID: oldID, AgentType: "command", Status: StatusCompleted, StartedAt: started, FinishedAt: finished},
	}
	if err := validateCommandExecution(oldID, command); err != nil {
		t.Fatal(err)
	}
	prior := &ExecutionState{
		RunID: cfg.RunID, WorkflowID: frozen.Name, WorkflowFile: path, Session: cfg.Session,
		Status: StatusCancelled, StartedAt: started, FinishedAt: finished, UpdatedAt: finished,
		CommandEvaluationVersion: commandEvaluationVersion,
		Steps: map[string]StepResult{
			"reserve": reservation.Result,
			"cleanup": {StepID: "cleanup", Status: StatusCancelled, StartedAt: started, FinishedAt: finished},
			oldID:     command.Result,
			"release": {StepID: "release", Status: StatusCancelled, StartedAt: started, FinishedAt: finished, SkipKind: SkipKindCancelledBeforeDispatch},
		},
		ForeachState: map[string]ForeachIterationState{"cleanup": {
			StepID: "cleanup", CurrentIteration: 1, Total: 2,
			CompletedIterationIDs: []string{"cleanup_iter0"}, StartedAt: started, UpdatedAt: finished,
		}},
		CommandExecutions: map[string]CommandExecutionState{oldID: command},
		ReservationSteps:  map[string]ReservationStepState{"reserve": reservation},
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "cleanup-effects"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(cfg.ProjectDir, prior); err != nil {
		t.Fatal(err)
	}
	prior, err = LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if prior.StepAdmissions != nil {
		t.Fatal("legacy fixture unexpectedly contains an admission ledger")
	}
	oldCommand := prior.CommandExecutions[oldID]
	oldReservation := cloneReservationStep(prior.ReservationSteps["reserve"])
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithCancel(owner.Context())
	cancel()
	state, err := NewExecutor(cfg).Resume(ctx, frozen, prior, nil)
	if !errors.Is(err, context.Canceled) || state == nil || state.Status != StatusCancelled {
		t.Fatalf("cancelled reservation preflight changed cancellation outcome: %+v %v", state, err)
	}
	requireStepAdmissionSets(t, state, []string{"reserve"}, []string{"cleanup", oldID, "release"})
	if result := state.Steps["cleanup"]; result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" {
		t.Fatalf("legacy cleanup history did not consume the finalization allowance: %+v", result)
	}
	if state.Steps["release"].Status != StatusCompleted || state.CommandExecutions["release"].Status != commandExecutionSettled {
		t.Fatal("fresh dynamic cleanup consumed an already reserved release root")
	}
	requireStepAdmissionEffect(t, cfg.ProjectDir, "cleanup-effects", "0\n")
	requireStepAdmissionEffect(t, cfg.ProjectDir, "release-effects", "released")
	if _, exists := state.Steps[newID]; exists {
		t.Fatal("unadmitted legacy continuation gained a stored child result")
	}
	if _, exists := state.CommandExecutions[newID]; exists {
		t.Fatal("unadmitted legacy continuation gained a launch receipt")
	}
	if !reflect.DeepEqual(oldCommand, state.CommandExecutions[oldID]) || !reflect.DeepEqual(oldReservation, state.ReservationSteps["reserve"]) {
		t.Fatal("cancelled capacity import altered original process or reservation evidence")
	}
}

func TestStepAdmissionLoopAndRoundResumeChargesOnlyNewRuntimeIDs(t *testing.T) {
	for _, kind := range []string{"loop_items", "expression_rounds"} {
		t.Run(kind, func(t *testing.T) {
			cfg := stepAdmissionIntegrationConfig(t)
			parent := Step{ID: "expand"}
			var children []string
			firstEffects, completeEffects := "0\n1\n", "0\n1\n2\n"
			if kind == "loop_items" {
				parent.Loop = &LoopConfig{Items: `[0,1,2]`, Steps: []Step{{ID: "work", Command: "printf '%s\\n' '${loop.index}' >> effects"}}}
				for i := 0; i < 3; i++ {
					children = append(children, fmt.Sprintf("expand_iter%d_work", i))
				}
			} else {
				parent.Foreach = &ForeachConfig{Items: `["only"]`, MaxRounds: IntOrExpr{Expr: "${defaults.rounds}"},
					Steps: []Step{{ID: "work", Command: "printf '%s\\n' '${round}' >> effects"}}}
				for round := 1; round <= 3; round++ {
					children = append(children, fmt.Sprintf("expand_iter0_work_round%d", round))
				}
				firstEffects, completeEffects = "1\n2\n", "1\n2\n3\n"
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bounded-resume-" + kind,
				Defaults: map[string]interface{}{"rounds": 3},
				Settings: WorkflowSettings{Limits: LimitsConfig{MaxStepCountTotal: 3, MaxMaterializedStepNodes: 64}},
				Steps:    []Step{parent},
			}
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
				t.Fatalf("third distinct runtime ID was not refused: %+v %v", state, err)
			}
			admitted := append([]string{"expand"}, children[:2]...)
			requireStepAdmissionSets(t, state, admitted, nil)
			requireStepAdmissionEffect(t, cfg.ProjectDir, "effects", firstEffects)
			if _, exists := state.Steps[children[2]]; exists {
				t.Fatal("unadmitted third child was stored as an executable result")
			}
			if _, exists := state.CommandExecutions[children[2]]; exists {
				t.Fatal("unadmitted third child obtained a launch receipt")
			}
			prior, loadErr := LoadState(cfg.ProjectDir, cfg.RunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			retained := []CommandExecutionState{prior.CommandExecutions[children[0]], prior.CommandExecutions[children[1]]}
			frozen, err := os.ReadFile(state.WorkflowFile)
			if err != nil {
				t.Fatal(err)
			}
			// The same limit must retain completed work while still refusing
			// the next ID. Re-entering a parent/round is not a fresh charge.
			state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 3})
			if !errors.Is(err, ErrStepCountLimit) || state == nil || state.Status != StatusFailed {
				t.Fatalf("unchanged budget lost its refusal on resume: %+v %v", state, err)
			}
			requireStepAdmissionSets(t, state, admitted, nil)
			requireStepAdmissionEffect(t, cfg.ProjectDir, "effects", firstEffects)
			state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 4})
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("one additional admission failed to finish only the remaining work: %+v %v", state, err)
			}
			requireStepAdmissionSets(t, state, append([]string{"expand"}, children...), nil)
			requireStepAdmissionEffect(t, cfg.ProjectDir, "effects", completeEffects)
			for i, record := range retained {
				if !reflect.DeepEqual(record, state.CommandExecutions[children[i]]) {
					t.Fatalf("resume replaced a previously completed command receipt %q", children[i])
				}
			}
			if after, err := os.ReadFile(state.WorkflowFile); err != nil || !bytes.Equal(after, frozen) {
				t.Fatalf("attempt admission override changed the frozen workflow: %v", err)
			}
			// Completed recovery is also idempotent when the new attempt's
			// limit is below its already-admitted history.
			state, err = stepAdmissionResume(t, cfg, ResumeOptions{MaxStepCountTotal: 1})
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("existing admission history was charged again: %+v %v", state, err)
			}
			requireStepAdmissionSets(t, state, append([]string{"expand"}, children...), nil)
			requireStepAdmissionEffect(t, cfg.ProjectDir, "effects", completeEffects)
		})
	}
}

func TestStepAdmissionOverridesPreserveUncertainExecutionFences(t *testing.T) {
	for _, kind := range []string{"unfinished_command", "unresolved_selector"} {
		t.Run(kind, func(t *testing.T) {
			var cfg ExecutorConfig
			if kind == "unfinished_command" {
				cfg, _, _, _ = commandOperatorRecoveryFixture(t, 0, "direct")
			} else {
				cfg, _, _, _ = selectorOperatorRecoveryFixture(t, "branch", []byte("good"), 0, true)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			_, err := stepAdmissionResume(t, cfg, ResumeOptions{
				MaxStepCountTotal: 2 * DefaultMaxStepCountTotal, MaxMaterializedStepNodes: 2 * DefaultMaxMaterializedStepNodes,
			})
			if kind == "unfinished_command" {
				if err == nil || !strings.Contains(err.Error(), "unresolved launch") {
					t.Fatalf("higher capacity authorized replay of an unfinished command: %v", err)
				}
				requireStepAdmissionEffect(t, cfg.ProjectDir, "effects", "once\n")
				requireNoStepAdmissionEffect(t, cfg.ProjectDir, "dependent")
			} else {
				if !errors.Is(err, errCommandOutputUnsettled) {
					t.Fatalf("higher capacity authorized replay of an unresolved selector: %v", err)
				}
				requireStepAdmissionEffect(t, cfg.ProjectDir, "source-effects", "once\n")
				requireNoStepAdmissionEffect(t, cfg.ProjectDir, "admitted")
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected capacity override rewrote the original recovery evidence")
			}
		})
	}
}

func TestStepMaterializationNestedRefusalReleasesCapacityForFinalization(t *testing.T) {
	cfg := stepAdmissionIntegrationConfig(t)
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "bounded-nested-plans",
		Settings: WorkflowSettings{OnError: ErrorActionContinue, Limits: LimitsConfig{
			MaxStepCountTotal: 16, MaxMaterializedStepNodes: 7,
		}},
		Steps: []Step{{ID: "outer", Foreach: &ForeachConfig{Items: `[0,1]`, Steps: []Step{{
			ID: "inner", Foreach: &ForeachConfig{Items: `[0,1]`, Steps: []Step{{ID: "work", Command: "printf forbidden > child-effects"}}},
		}}}}},
		PostPipelineSteps: []Step{{ID: "finalize", Foreach: &ForeachConfig{Items: `[0,1,2,3,4,5]`,
			Steps: []Step{{ID: "release", Command: "printf '%s\\n' '${item}' >> finalized"}},
		}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if !errors.Is(err, ErrMaterializedStepLimit) || ctx.Err() != nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("nested planner waited for its own retained capacity or hid refusal: %+v %v context=%v", state, err, ctx.Err())
	}
	requireNoStepAdmissionEffect(t, cfg.ProjectDir, "child-effects")
	// Outer plans retain five nodes; entering the nested plan would need
	// eight. This finalizer needs all seven nodes, so any leaked lease from
	// the failed main expansion would also prevent the release work.
	requireStepAdmissionEffect(t, cfg.ProjectDir, "finalized", "0\n1\n2\n3\n4\n5\n")
	if result := state.Steps["finalize"]; result.Status != StatusCompleted {
		t.Fatalf("failed planning retained capacity or poisoned finalization: %+v", result)
	}
	for id := range state.CommandExecutions {
		if !strings.HasPrefix(id, "finalize_iter") || state.CommandExecutions[id].Status != commandExecutionSettled {
			t.Fatalf("failed nested planning launched main work or stranded a finalizer: %s %+v", id, state.CommandExecutions[id])
		}
	}
}

func TestStepAdmissionFinalizationExhaustionPreservesLaterCleanupRoot(t *testing.T) {
	for _, failure := range []string{"cancellation", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			cfg := stepAdmissionIntegrationConfig(t)
			work := "trap 'exit 0' TERM; printf ready > ready; while :; do sleep 0.01; done"
			if failure == "checkpoint" {
				work = awaitOwnLaunchJournal(cfg.RunID) + "; mv .ntm/pipelines/run-step-admission.json .ntm/pipelines/original-checkpoint.saved && mkdir .ntm/pipelines/run-step-admission.json && printf done > ready"
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "bounded-cleanup-" + failure,
				Settings: WorkflowSettings{Limits: LimitsConfig{MaxStepCountTotal: 3, MaxMaterializedStepNodes: 32},
					OnCancel: []Step{
						{ID: "expand_cleanup", Loop: &LoopConfig{Items: `[0,1,2]`, Steps: []Step{{ID: "cleanup_leaf", Command: "printf '%s\\n' '${loop.index}' >> expanded-cleanup"}}}},
						{ID: "release", Command: "printf released > release-effects"},
					},
				},
				Steps: []Step{{ID: "work", Command: work}, {ID: "after", DependsOn: []string{"work"}, Command: "printf forbidden > after-effects"}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, commandStartedTestHookKey{}, func(int) {
				// Check the reservation before either failure occurs; admitting
				// release only when cleanup reaches it would be too late once
				// the earlier loop has consumed the finalization allowance.
				before, loadErr := LoadState(cfg.ProjectDir, cfg.RunID)
				if loadErr != nil || before == nil || before.StepAdmissions == nil || len(before.StepAdmissions.Finalization) != 2 ||
					!before.StepAdmissions.Finalization["expand_cleanup"] || !before.StepAdmissions.Finalization["release"] {
					t.Errorf("cleanup roots were not reserved before main dispatch: %+v %v", before, loadErr)
				}
				if failure == "cancellation" {
					defer cancel()
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "ready")); err == nil && string(data) == "ready" {
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					t.Error("owned command never reached its cancellation barrier")
				}
			})
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			if failure == "checkpoint" {
				assertCheckpointFailure(t, state, err)
				requireStepAdmissionEffect(t, cfg.ProjectDir, "ready", "done")
			} else if !errors.Is(err, context.Canceled) || state == nil || state.Status != StatusCancelled {
				t.Fatalf("genuine cancellation lost its outcome: %+v %v", state, err)
			}
			requireStepAdmissionSets(t, state, []string{"work", "after"}, []string{"expand_cleanup", "release", "expand_cleanup_iter0_cleanup_leaf"})
			requireStepAdmissionEffect(t, cfg.ProjectDir, "expanded-cleanup", "0\n")
			for _, id := range []string{"expand_cleanup_iter1_cleanup_leaf", "expand_cleanup_iter2_cleanup_leaf"} {
				if _, exists := state.Steps[id]; exists {
					t.Fatalf("unadmitted cleanup child %s was recorded", id)
				}
			}
			requireNoStepAdmissionEffect(t, cfg.ProjectDir, "after-effects")
			requireStepAdmissionEffect(t, cfg.ProjectDir, "release-effects", "released")
			if result := state.Steps["expand_cleanup"]; result.Status != StatusFailed || result.Error == nil || result.Error.Type != "step_limit" {
				t.Fatalf("first cleanup root did not report its bounded refusal: %+v", result)
			}
			if result := state.Steps["release"]; result.Status != StatusCompleted {
				t.Fatalf("dynamic cleanup exhaustion prevented the pre-reserved release root: %+v", result)
			}
			if failure == "cancellation" {
				for _, id := range []string{"work", "release"} {
					if receipt := state.CommandExecutions[id]; receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() {
						t.Fatalf("cancellation left %s unsettled: %+v", id, receipt)
					}
				}
			}
		})
	}
}
