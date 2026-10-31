package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCommandEvaluationDurableIntentAndReturnedUncertainty(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := &Step{ID: "choose", Branch: "$(printf selected)", Branches: map[string]interface{}{"selected": Step{ID: "body", Command: "true"}}}
	lifecycle := e.commandEvaluationContext(context.Background(), step).Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
	cmd := exec.Command("/bin/sh", "-c", "printf selected")
	request := commandOutputRequest{Cmd: cmd, StdoutLimit: 64, StderrLimit: 64}
	if _, reused, err := lifecycle.beforeStart(request); err != nil || reused {
		t.Fatal(err)
	}
	// This is the crash boundary: no process has been launched and no result
	// or decision exists, but ordinary resume must not infer non-execution.
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := prior.CommandEvaluations[step.ID]
	if !ok || record.Status != commandExecutionStarting || validateCommandEvaluation(step.ID, record) != nil {
		t.Fatalf("prelaunch intent was not durable: %+v", prior.CommandEvaluations)
	}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "evaluation", Steps: []Step{*step}}
	recovered := NewExecutor(e.config)
	recovered.state = prior
	if err := recovered.applyResumeOptions(workflow, ResumeOptions{}); !errors.Is(err, errCommandOutputUnsettled) {
		t.Fatalf("resume admitted a command interrupted after intent: %v", err)
	}
	if _, _, err := lifecycle.beforeStart(request); !errors.Is(err, errCommandOutputUnsettled) {
		t.Fatalf("duplicate evaluation overwrote an outstanding intent: %v", err)
	}
	// A returned cleanup failure is terminal evidence, independently of a
	// scheduler result that a failure handler may replace with Completed.
	cmd.Process = &os.Process{Pid: 31415}
	if err := lifecycle.afterWait(cmd, false, nil, errors.New("injected output drain failure")); err != nil {
		t.Fatal(err)
	}
	prior, err = LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	record = prior.CommandEvaluations[step.ID]
	if record.Status != commandExecutionUnresolved || record.ProcessID != 31415 || record.FinishedAt.IsZero() || !strings.Contains(record.Error, "injected output drain failure") || validateCommandEvaluation(step.ID, record) != nil {
		t.Fatalf("returned uncertainty was stranded as a running evaluation: %+v", record)
	}
	prior.Steps[step.ID] = StepResult{StepID: step.ID, Status: StatusCompleted}
	recovered.state = prior
	if err := recovered.applyResumeOptions(workflow, ResumeOptions{}); !errors.Is(err, errCommandOutputUnsettled) {
		t.Fatalf("completed recovery handler erased evaluation uncertainty: %v", err)
	}
	snapshot := e.snapshotState()
	delete(snapshot.CommandEvaluations, step.ID)
	if !e.hasUnresolvedCommandEvaluation(step) {
		t.Fatal("snapshot mutation erased live launch evidence")
	}
}

func TestCommandEvaluationProvenFailureClearsIntent(t *testing.T) {
	for _, waitErr := range []error{context.Canceled, errors.New("settled command exited unsuccessfully")} {
		e := newCommandTestExecutor(t)
		step := &Step{ID: "source", Foreach: &ForeachConfig{Items: "$(printf item)"}}
		lifecycle := e.commandEvaluationContext(context.Background(), step).Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
		cmd := exec.Command("/bin/sh", "-c", "printf item")
		if _, reused, err := lifecycle.beforeStart(commandOutputRequest{Cmd: cmd, StdoutLimit: 64, StderrLimit: 64}); err != nil || reused {
			t.Fatal(err)
		}
		if err := lifecycle.afterWait(cmd, true, nil, waitErr); err != nil {
			t.Fatal(err)
		}
		prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
		if err != nil || len(prior.CommandEvaluations) != 0 {
			t.Fatalf("verified settlement retained a replay fence: %+v %v", prior, err)
		}
	}
}

func TestCommandEvaluationResumeScopePreservesUnprovenWork(t *testing.T) {
	for _, scenario := range []string{"continue", "completed recovery", "restart top-level", "completed container", "completed iteration", "force wrong suffix", "force selected suffix", "reset"} {
		t.Run(scenario, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			selector := Step{ID: "choose", Branch: "$(printf yes)", Branches: map[string]interface{}{"yes": Step{ID: "body", Command: "true"}}}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "evaluation-scope", Steps: []Step{selector}}
			id := selector.ID
			opts := ResumeOptions{}
			allowed := false
			switch scenario {
			case "completed recovery", "restart top-level":
				e.state.Steps[id] = StepResult{StepID: id, Status: StatusCompleted}
				if scenario == "restart top-level" {
					opts.Mode, allowed = ResumeModeRestartFailed, true
				}
			case "completed container", "completed iteration", "force wrong suffix", "force selected suffix":
				workflow.Steps = []Step{{ID: "outer", Foreach: &ForeachConfig{Items: `["one","two"]`, Steps: []Step{selector}}}}
				id = "outer_iter1_choose"
				e.state.ForeachState = map[string]ForeachIterationState{"outer": {StepID: "outer", Total: 2}}
				opts.Mode = ResumeModeRestartFailed
				if scenario == "completed container" {
					e.state.Steps["outer"] = StepResult{StepID: "outer", Status: StatusCompleted}
				}
				if scenario == "completed iteration" {
					progress := e.state.ForeachState["outer"]
					progress.CompletedIterationIDs = []string{"outer_iter1"}
					e.state.ForeachState["outer"] = progress
				}
				if strings.HasPrefix(scenario, "force ") {
					opts.Mode, opts.StepID, opts.Iteration = ResumeModeForceIter, "outer", 1
					if scenario == "force wrong suffix" {
						id = "outer_iter0_choose"
					} else {
						allowed = true
					}
				}
			case "reset":
				opts.Reset, allowed = true, true
			}
			selector.ID = id
			digest, err := commandDefinitionHash(&selector)
			if err != nil {
				t.Fatal(err)
			}
			e.state.CommandEvaluations = map[string]CommandEvaluationState{id: {Version: 1, StepID: id, StepHash: digest, Status: commandExecutionStarting, StartedAt: time.Now()}}
			before, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			err = e.applyResumeOptions(workflow, opts)
			if allowed {
				if err != nil || len(e.state.CommandEvaluations) != 0 {
					t.Fatalf("explicit replay scope did not reopen evaluation: %+v %v", e.state.CommandEvaluations, err)
				}
				if scenario == "restart top-level" && e.state.Steps[id].Status != StatusPending {
					t.Fatal("explicit restart retained a completed selector")
				}
				return
			}
			after, marshalErr := json.Marshal(e.state)
			if err == nil || marshalErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("unsafe replay was admitted or changed its evidence: err=%v marshal=%v", err, marshalErr)
			}
		})
	}
}

func TestCommandEvaluationMalformedReceiptCannotBeReset(t *testing.T) {
	for _, damage := range []string{"version", "journal version", "identity", "hash", "status", "started", "pid", "finished", "error"} {
		t.Run(damage, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			step := Step{ID: "choose", Branch: "$(printf yes)"}
			digest, err := commandDefinitionHash(&step)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			record := CommandEvaluationState{Version: 1, StepID: step.ID, StepHash: digest, Status: commandExecutionUnresolved, ProcessID: 2718, StartedAt: now, FinishedAt: now, Error: "unproven cleanup"}
			switch damage {
			case "version":
				record.Version++
			case "journal version":
				e.state.CommandEvaluationVersion = 999
			case "identity":
				record.StepID = "unrelated"
			case "hash":
				record.StepHash = "invalid"
			case "status":
				record.Status = "settled"
			case "started":
				record.StartedAt = time.Time{}
			case "pid":
				record.ProcessID = -1
			case "finished":
				record.FinishedAt = now.Add(-time.Second)
			case "error":
				record.Error = ""
			}
			e.state.CommandEvaluations = map[string]CommandEvaluationState{step.ID: record}
			before, _ := json.Marshal(e.state)
			if err := e.applyResumeOptions(&Workflow{Steps: []Step{step}}, ResumeOptions{Reset: true}); err == nil {
				t.Fatal("reset discarded malformed launch evidence")
			}
			after, _ := json.Marshal(e.state)
			if !bytes.Equal(before, after) {
				t.Fatal("refused reset changed launch evidence")
			}
		})
	}
}

func TestCommandEvaluationSettledReceiptCorruptionCannotAuthorizeReuseOrReset(t *testing.T) {
	for _, damage := range []string{"output bytes", "output hash", "invocation hash", "stdout bound", "stderr bound", "pid", "timestamp", "error", "old version", "future version"} {
		t.Run(damage, func(t *testing.T) {
			e := newCommandTestExecutor(t)
			step := Step{ID: "source", Branch: "$(printf selected)"}
			cmd := exec.Command("/bin/sh", "-c", "printf selected")
			lifecycle := e.commandEvaluationContext(context.Background(), &step).Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
			if _, _, err := lifecycle.beforeStart(commandOutputRequest{Cmd: cmd, StdoutLimit: 64, StderrLimit: 64}); err != nil {
				t.Fatal(err)
			}
			cmd.Process = &os.Process{Pid: 31415}
			if err := lifecycle.afterWait(cmd, true, []byte("selected"), nil); err != nil {
				t.Fatal(err)
			}
			record := cloneCommandEvaluation(e.state.CommandEvaluations[step.ID])
			switch damage {
			case "output bytes":
				record.Output[0] = 'X'
			case "output hash":
				record.OutputHash = strings.Repeat("0", 64)
			case "invocation hash":
				record.InvocationHash = "missing"
			case "stdout bound":
				record.StdoutLimit = 1
			case "stderr bound":
				record.StderrLimit = 0
			case "pid":
				record.ProcessID = 0
			case "timestamp":
				record.FinishedAt = record.StartedAt.Add(-time.Second)
			case "error":
				record.Error = "incomplete output"
			case "old version":
				record.Version = 1
			case "future version":
				record.Version = commandEvaluationVersion + 1
			}
			e.state.CommandEvaluations[step.ID] = record
			before, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			for _, opts := range []ResumeOptions{{}, {Reset: true}} {
				if err := e.applyResumeOptions(&Workflow{Steps: []Step{step}}, opts); err == nil {
					t.Fatalf("corrupt successful output authorized resume/reset: %+v", opts)
				}
				after, err := json.Marshal(e.state)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("refusal discarded corrupt output evidence: %v", err)
				}
			}
			if !e.hasUnresolvedCommandEvaluation(&step) {
				t.Fatal("invalid success bypassed the automatic retry fence")
			}
		})
	}
}

func TestLegacyCommandEvaluationResumeRequiresLaunchEvidence(t *testing.T) {
	cases := []struct {
		name     string
		step     Step
		external bool
	}{
		{"shell branch", Step{Branch: "$(printf selected)"}, true},
		{"expanded branch", Step{Branch: "${vars.selector}"}, true},
		{"literal branch", Step{Branch: "selected"}, false},
		{"bead query", Step{BeadQuery: &BeadQueryStep{}}, true},
		{"bead source", Step{Foreach: &ForeachConfig{Beads: "status:open"}}, true},
		{"pairs source", Step{Foreach: &ForeachConfig{Pairs: "$(printf pairs)"}}, true},
		{"debates source", Step{Foreach: &ForeachConfig{Debates: "$(printf debates)"}}, true},
		{"model source", Step{Foreach: &ForeachConfig{Models: StringOrList{"$(printf cc)"}}}, true},
		{"static models", Step{Foreach: &ForeachConfig{Models: StringOrList{"cc", "cod"}}}, false},
		{"static items", Step{Foreach: &ForeachConfig{Items: `["item"]`}}, false},
		{"literal shell-shaped item", Step{Foreach: &ForeachConfig{Items: "$(printf literal)"}}, false},
	}
	for _, tc := range cases {
		for _, version := range []int{0, 1, commandEvaluationVersion} {
			t.Run(tc.name+"/version"+strconv.Itoa(version), func(t *testing.T) {
				e := newCommandTestExecutor(t)
				step := tc.step
				step.ID = "evaluate"
				workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "legacy-source", Steps: []Step{step}}
				e.state.CommandEvaluationVersion = version
				e.state.InFlightSteps = map[string]InFlightStepState{step.ID: {StepID: step.ID, Kind: stepKind(&step)}}
				e.state.Steps[step.ID] = StepResult{StepID: step.ID, Status: StatusRunning}
				// Existing iteration progress describes earlier source evaluations;
				// it does not prove that a later legacy evaluation has stopped.
				if step.Foreach != nil {
					e.state.ForeachState = map[string]ForeachIterationState{step.ID: {StepID: step.ID, Total: 2, CompletedIterationIDs: []string{step.ID + "_iter0"}}}
				}
				before, _ := json.Marshal(e.state)
				err := e.applyResumeOptions(workflow, ResumeOptions{})
				if version < commandEvaluationVersion && tc.external {
					after, _ := json.Marshal(e.state)
					if !errors.Is(err, errCommandOutputUnsettled) || !bytes.Equal(before, after) {
						t.Fatalf("legacy external work was admitted or rewritten: %v", err)
					}
					if err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
						t.Fatalf("explicit top-level replay was refused: %v", err)
					}
				} else if err != nil {
					t.Fatalf("safe evaluation was incorrectly fenced: %v", err)
				}
				if e.state.CommandEvaluationVersion != commandEvaluationVersion {
					t.Fatal("accepted resume did not adopt the evaluation journal")
				}
			})
		}
	}
}

func TestCommandEvaluationCheckpointFailurePreventsLaunch(t *testing.T) {
	e := newCommandTestExecutor(t)
	// Block checkpoint-directory creation while leaving the working directory
	// usable by the shell. A failed intent must never dispatch its command.
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, ".ntm"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	step := &Step{ID: "choose", Branch: "$(printf dispatched > marker; printf selected)"}
	key, err := e.resolveBranch(context.Background(), step)
	if key != "" || !errors.Is(err, ErrCheckpointFailed) {
		t.Fatalf("failed durable intent returned a selector: %q %v", key, err)
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selector launched without a durable intent: %v", err)
	}
}

func TestLegacyFailedCommandEvaluationCannotAuthorizeReplay(t *testing.T) {
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled} {
		for _, step := range []Step{{ID: "choose", Branch: "$(printf selected)"}, {ID: "source", Foreach: &ForeachConfig{Beads: "$(printf item)"}}} {
			t.Run(string(status)+"/"+step.ID, func(t *testing.T) {
				e := newCommandTestExecutor(t)
				e.state.Steps[step.ID] = StepResult{StepID: step.ID, Status: status, FinishedAt: time.Now(), Error: &StepError{Type: "branch", Message: "output observation failed"}}
				workflow := &Workflow{Steps: []Step{step}}
				if err := e.applyResumeOptions(workflow, ResumeOptions{}); !errors.Is(err, errCommandOutputUnsettled) {
					t.Fatalf("legacy terminal failure authorized selector replay: %v", err)
				}
				if err := e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeRestartFailed}); err != nil {
					t.Fatalf("explicit restart did not authorize replay: %v", err)
				}
			})
		}
	}
}

func TestResumeContinueRestartsFirstIncompleteLoopIteration(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "iterations.log")
	markerPath := filepath.Join(tmpDir, "failed-once")

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-loop",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "fanout",
				Loop: &LoopConfig{
					Items: "${vars.items}",
					As:    "row",
					Steps: []Step{
						{
							ID: "work",
							Command: "printf '%s\n' '${loop.index}' >> " + strconv.Quote(logPath) +
								"; if [ '${loop.index}' = '1' ] && [ ! -f " + strconv.Quote(markerPath) + " ]; then touch " + strconv.Quote(markerPath) + "; exit 7; fi",
						},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("resume-loop-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	first := NewExecutor(cfg)
	prior, err := first.Run(context.Background(), workflow, map[string]interface{}{
		"items": []interface{}{"a", "b", "c"},
	}, nil)
	if err == nil {
		t.Fatal("first Run() error = nil, want first pass to fail at iteration 1")
	}
	if prior.Status != StatusFailed {
		t.Fatalf("prior.Status = %s, want failed", prior.Status)
	}
	if got := prior.ForeachState["fanout"].CurrentIteration; got != 1 {
		t.Fatalf("prior current iteration = %d, want 1", got)
	}

	second := NewExecutor(cfg)
	final, err := second.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("final.Status = %s, want completed", final.Status)
	}
	if got := final.ForeachState["fanout"].CurrentIteration; got != 3 {
		t.Fatalf("final current iteration = %d, want 3", got)
	}
	if got := final.ForeachState["fanout"].CompletedIterationIDs; !reflect.DeepEqual(got, []string{"fanout_iter0", "fanout_iter1", "fanout_iter2"}) {
		t.Fatalf("completed iterations = %#v", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read iteration log: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "0\n1\n1\n2"; got != want {
		t.Fatalf("iteration log = %q, want %q", got, want)
	}
}

func TestResumeRestartFailedRerunsFailedStepOnly(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "resume.log")
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-failed",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "done", Command: "printf done >> " + strconv.Quote(logPath)},
			{ID: "flaky", Command: "printf flaky >> " + strconv.Quote(logPath), DependsOn: []string{"done"}},
		},
	}
	prior := &ExecutionState{
		RunID:      "restart-failed",
		WorkflowID: workflow.Name,
		Session:    "resume-session",
		Status:     StatusFailed,
		StartedAt:  time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 7, 10, 1, 0, 0, time.UTC),
		Steps: map[string]StepResult{
			"done":  {StepID: "done", Status: StatusCompleted, Output: "prior"},
			"flaky": {StepID: "flaky", Status: StatusFailed, Error: &StepError{Type: "command", Message: "exit 7", Timestamp: time.Date(2026, 5, 7, 10, 1, 0, 0, time.UTC)}},
		},
		Variables: map[string]interface{}{},
	}

	cfg := DefaultExecutorConfig("resume-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeRestartFailed,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("final.Status = %s, want completed", final.Status)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := string(data); got != "flaky" {
		t.Fatalf("log = %q, want only failed step to rerun", got)
	}
}

func TestResumeRestartFailedPreservesStateWhenKeepStateOmitted(t *testing.T) {
	// bd-uyjdn: callers that pass a partial ResumeOptions (e.g. only Mode)
	// without explicitly setting KeepState=true should still preserve
	// completed step state. Previously normalizeResumeOptions left KeepState
	// at the Go zero-value (false) for any non-zero options struct, which
	// caused applyResumeOptions to call resetResumeState and silently rerun
	// completed dependencies.
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "resume.log")
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-failed-partial-opts",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "done", Command: "printf done >> " + strconv.Quote(logPath)},
			{ID: "flaky", Command: "printf flaky >> " + strconv.Quote(logPath), DependsOn: []string{"done"}},
		},
	}
	prior := &ExecutionState{
		RunID:      "restart-failed-partial",
		WorkflowID: workflow.Name,
		Session:    "resume-session",
		Status:     StatusFailed,
		StartedAt:  time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 7, 10, 1, 0, 0, time.UTC),
		Steps: map[string]StepResult{
			"done":  {StepID: "done", Status: StatusCompleted, Output: "prior"},
			"flaky": {StepID: "flaky", Status: StatusFailed, Error: &StepError{Type: "command", Message: "exit 7", Timestamp: time.Date(2026, 5, 7, 10, 1, 0, 0, time.UTC)}},
		},
		Variables: map[string]interface{}{},
	}

	cfg := DefaultExecutorConfig("resume-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode: ResumeModeRestartFailed,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("final.Status = %s, want completed", final.Status)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := string(data); got != "flaky" {
		t.Fatalf("log = %q, want only failed step to rerun (completed dependency 'done' must not re-execute)", got)
	}
}

func TestResumeResetOptInClearsCompletedSteps(t *testing.T) {
	// bd-uyjdn: explicit Reset=true reproduces the legacy KeepState=false
	// behavior — prior step state is cleared and the workflow runs from the
	// beginning.
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "reset.log")
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-reset-optin",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "first", Command: "printf first >> " + strconv.Quote(logPath)},
		},
	}
	prior := &ExecutionState{
		RunID:      "reset-optin",
		WorkflowID: workflow.Name,
		Session:    "reset-session",
		Status:     StatusFailed,
		StartedAt:  time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 7, 10, 1, 0, 0, time.UTC),
		Steps: map[string]StepResult{
			"first": {StepID: "first", Status: StatusCompleted, Output: "prior"},
		},
		Variables: map[string]interface{}{},
	}

	cfg := DefaultExecutorConfig("reset-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:  ResumeModeContinue,
		Reset: true,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("final.Status = %s, want completed", final.Status)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := string(data); got != "first" {
		t.Fatalf("log = %q, want completed step to rerun under Reset=true", got)
	}
}

func TestNormalizeResumeOptionsDefaultsKeepStateOnNonZero(t *testing.T) {
	// bd-uyjdn: every non-zero ResumeOptions normalizes to KeepState=true
	// unless Reset=true is set explicitly.
	tests := []struct {
		name   string
		opts   ResumeOptions
		want   bool
		reset  bool
		errMsg string
	}{
		{name: "mode only", opts: ResumeOptions{Mode: ResumeModeRestartFailed}, want: true},
		{name: "max age only", opts: ResumeOptions{MaxResumeAge: time.Hour}, want: true},
		{name: "explicit reset", opts: ResumeOptions{Mode: ResumeModeContinue, Reset: true}, want: false, reset: true},
		{name: "legacy KeepState=true survives", opts: ResumeOptions{Mode: ResumeModeContinue, KeepState: true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeResumeOptions(tt.opts)
			if err != nil {
				t.Fatalf("normalizeResumeOptions() error: %v", err)
			}
			if got.KeepState != tt.want {
				t.Fatalf("KeepState = %v, want %v", got.KeepState, tt.want)
			}
			if got.Reset != tt.reset {
				t.Fatalf("Reset = %v, want %v", got.Reset, tt.reset)
			}
		})
	}
}

func TestResumeRejectsMismatchedWorkflowID(t *testing.T) {
	// bd-0wzkc: prior state captured for a different workflow must not
	// resume against the current workflow. Otherwise applyResumeState
	// marks any matching step IDs as executed in the new graph and reuses
	// their outputs in incompatible logic.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "current-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	prior := &ExecutionState{
		RunID:      "from-other-workflow",
		WorkflowID: "other-workflow",
		Session:    "session",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{"step": {StepID: "step", Status: StatusCompleted, Output: "stale"}},
		Variables:  map[string]interface{}{},
	}
	executor := NewExecutor(DefaultExecutorConfig("session"))
	_, err := executor.Resume(context.Background(), workflow, prior, nil)
	if err == nil {
		t.Fatal("Resume() error = nil, want workflow-mismatch rejection")
	}
	if !strings.Contains(err.Error(), "other-workflow") || !strings.Contains(err.Error(), "current-workflow") {
		t.Fatalf("Resume() error = %q, want both workflow names named", err.Error())
	}
}

func TestResumeAcceptsMatchingWorkflowID(t *testing.T) {
	// bd-0wzkc: matching workflow IDs continue to resume normally so the
	// happy path is not regressed.
	tmpDir := t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "match-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	prior := &ExecutionState{
		RunID:      "match-run",
		WorkflowID: "match-workflow",
		Session:    "match-session",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{"step": {StepID: "step", Status: StatusCompleted, Output: "ok"}},
		Variables:  map[string]interface{}{},
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	cfg := DefaultExecutorConfig("match-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("final.Status = %s, want completed", final.Status)
	}
}

func TestResumeAcceptsLegacyEmptyWorkflowID(t *testing.T) {
	// bd-0wzkc: a legacy state file with no recorded WorkflowID continues to
	// resume; the validator should only reject explicit non-empty mismatches.
	tmpDir := t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "legacy-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	prior := &ExecutionState{
		RunID:     "legacy-run",
		Session:   "legacy-session",
		Status:    StatusRunning,
		Steps:     map[string]StepResult{},
		Variables: map[string]interface{}{},
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	cfg := DefaultExecutorConfig("legacy-session")
	cfg.ProjectDir = tmpDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error: %v", err)
	}
	if final.WorkflowID != "legacy-workflow" {
		t.Fatalf("final.WorkflowID = %q, want it backfilled from workflow.Name", final.WorkflowID)
	}
}

func TestResumeRosterChangeAbort(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-roster",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	prior := &ExecutionState{
		RunID:      "roster-change",
		WorkflowID: workflow.Name,
		Session:    "old-session",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}
	executor := NewExecutor(DefaultExecutorConfig("new-session"))
	final, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("ResumeWithOptions() error = nil, want roster-change abort")
	}
	if final == nil || final.Status != StatusFailed {
		t.Fatalf("final status = %#v, want failed state", final)
	}
	if !strings.Contains(err.Error(), "old-session") || !strings.Contains(err.Error(), "new-session") {
		t.Fatalf("error = %q, want both sessions named", err.Error())
	}
}

func TestResumeRejectsStaleState(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-stale",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	prior := &ExecutionState{
		RunID:            "stale",
		WorkflowID:       workflow.Name,
		Session:          "session",
		Status:           StatusRunning,
		UpdatedAt:        time.Now().Add(-48 * time.Hour),
		LastCheckpointAt: time.Now().Add(-48 * time.Hour),
		Steps:            map[string]StepResult{},
		Variables:        map[string]interface{}{},
	}
	executor := NewExecutor(DefaultExecutorConfig("session"))
	_, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		MaxResumeAge:   time.Hour,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("ResumeWithOptions() error = nil, want stale-state error")
	}
	if !strings.Contains(err.Error(), "older than MaxResumeAge") {
		t.Fatalf("error = %q, want stale-state message", err.Error())
	}
}

func TestResumeRejectingStaleStateDoesNotRefreshCheckpoint(t *testing.T) {
	// bd-0n73e: a resume that fails the MaxResumeAge guard must not
	// rewrite LastCheckpointAt/UpdatedAt on disk; otherwise a second
	// attempt would silently pass the same age check and resume work
	// that should remain blocked.
	tmpDir := t.TempDir()
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "stale-resume-no-refresh",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	stale := time.Now().Add(-48 * time.Hour)
	prior := &ExecutionState{
		RunID:            "stale-no-refresh",
		WorkflowID:       workflow.Name,
		Session:          "session",
		Status:           StatusRunning,
		StartedAt:        stale.Add(-time.Hour),
		UpdatedAt:        stale,
		LastCheckpointAt: stale,
		Steps:            map[string]StepResult{},
		Variables:        map[string]interface{}{},
	}
	if err := SaveState(tmpDir, prior); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	cfg := DefaultExecutorConfig("session")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	_, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		MaxResumeAge:   time.Hour,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("ResumeWithOptions() error = nil, want stale-state rejection")
	}

	// Reload the on-disk file. The checkpoint must still be ~stale, not now.
	reloaded, err := LoadState(tmpDir, prior.RunID)
	if err != nil {
		t.Fatalf("LoadState after rejected resume: %v", err)
	}
	if delta := time.Since(reloaded.LastCheckpointAt); delta < 24*time.Hour {
		t.Fatalf("LastCheckpointAt advanced after rejected stale resume (delta=%s); a follow-up resume would bypass MaxResumeAge", delta)
	}

	// A second resume against the same state must still be rejected.
	_, err = executor.ResumeWithOptions(context.Background(), workflow, reloaded, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		MaxResumeAge:   time.Hour,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("second ResumeWithOptions() error = nil, want stale-state rejection on retry")
	}
	if !strings.Contains(err.Error(), "older than MaxResumeAge") {
		t.Fatalf("second attempt error = %q, want stale-state message", err.Error())
	}
}

func TestResumeRejectsLegacyStateWithOnlyUpdatedAt(t *testing.T) {
	// bd-05l02: a legacy state with LastCheckpointAt zero, UpdatedAt older
	// than MaxResumeAge, and no child step / foreach / parallel / in-flight
	// timestamps must still fail the stale-age guard. Before the fix
	// resumeCheckpointTime returned the zero time and the guard was a no-op.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "legacy-only-updated",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	stale := time.Now().Add(-72 * time.Hour)
	prior := &ExecutionState{
		RunID:      "legacy-only-updated",
		WorkflowID: workflow.Name,
		Session:    "session",
		Status:     StatusRunning,
		UpdatedAt:  stale,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor := NewExecutor(DefaultExecutorConfig("session"))
	_, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		MaxResumeAge:   2 * time.Hour,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("ResumeWithOptions() error = nil, want stale-state rejection from UpdatedAt fallback")
	}
	if !strings.Contains(err.Error(), "older than MaxResumeAge") {
		t.Fatalf("error = %q, want stale-state message", err.Error())
	}
}

func TestResumeRejectsLegacyStateWithoutCheckpoint(t *testing.T) {
	// bd-5uqtd: a true legacy state file lacks both LastCheckpointAt AND
	// UpdatedAt. The previous version of this test left UpdatedAt set to
	// `stale`, which let the pre-fix `checkpoint = state.UpdatedAt`
	// fallback satisfy the guard, so the test passed against both buggy
	// and fixed code. Zeroing UpdatedAt and StartedAt forces resumeCheckpointTime
	// to walk step timestamps to discover the age — the path bd-omfqk's
	// fix actually introduced.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-legacy-stale",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "step", Command: "true"}},
	}
	stale := time.Now().Add(-72 * time.Hour)
	prior := &ExecutionState{
		RunID:      "legacy-stale",
		WorkflowID: workflow.Name,
		Session:    "session",
		Status:     StatusRunning,
		// StartedAt, UpdatedAt, LastCheckpointAt are all left zero so
		// the only age signal is on the step result. resumeCheckpointTime
		// must walk into Steps[].FinishedAt to discover the stale state.
		Steps: map[string]StepResult{
			"step": {
				StepID:     "step",
				Status:     StatusCompleted,
				StartedAt:  stale.Add(-30 * time.Minute),
				FinishedAt: stale,
			},
		},
		Variables: map[string]interface{}{},
	}

	executor := NewExecutor(DefaultExecutorConfig("session"))
	_, err := executor.ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode:           ResumeModeContinue,
		KeepState:      true,
		MaxResumeAge:   2 * time.Hour,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err == nil {
		t.Fatal("ResumeWithOptions() error = nil, want stale-state error for legacy checkpoint-less state")
	}
	if !strings.Contains(err.Error(), "older than MaxResumeAge") {
		t.Fatalf("error = %q, want stale-state message", err.Error())
	}
}

func TestExecutionStateResumeMetadataJSONRoundTrip(t *testing.T) {
	stamp := time.Date(2026, 5, 7, 12, 30, 0, 0, time.UTC)
	original := ExecutionState{
		RunID:            "roundtrip",
		WorkflowID:       "workflow",
		Session:          "session",
		Status:           StatusRunning,
		StartedAt:        stamp.Add(-time.Minute),
		UpdatedAt:        stamp,
		LastCheckpointAt: stamp,
		Steps: map[string]StepResult{
			"step": {StepID: "step", Status: StatusCompleted, Output: "ok", StartedAt: stamp.Add(-time.Second), FinishedAt: stamp},
		},
		Variables: map[string]interface{}{"input": "value"},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:                "fanout",
				CurrentIteration:      2,
				Total:                 4,
				CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1"},
				StartedAt:             stamp.Add(-time.Minute),
				UpdatedAt:             stamp,
			},
		},
		ParallelState: map[string]ParallelGroupState{
			"group": {
				StepID:           "group",
				Total:            3,
				CompletedStepIDs: []string{"a"},
				FailedStepIDs:    []string{"b"},
				InFlightStepIDs:  []string{"c"},
				StartedAt:        stamp.Add(-time.Minute),
				UpdatedAt:        stamp,
			},
		},
		ScopeStack: []ScopeFrame{
			{Kind: StepKindLoop, Name: "row", Variables: map[string]interface{}{"loop.item": "a", "loop.first": true}},
		},
		InFlightSteps: map[string]InFlightStepState{
			"group.c": {StepID: "group.c", Kind: "parallel_step", StartedAt: stamp, Iteration: 2, Output: "partial"},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	var decoded ExecutionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", decoded, original)
	}
}

// TestApplyResumeStateLogsOrphanDrops covers bd-98sd7: when MarkExecuted
// rejects a step ID (most often because it is a synthetic loop-iteration
// ID that is not part of the current workflow's dependency graph), the
// step record is dropped from state.Steps. Previously this happened
// silently; now applyResumeState emits a slog.Warn so the audit trail
// loss is observable.
func TestApplyResumeStateLogsOrphanDrops(t *testing.T) {
	var buf bytes.Buffer
	restore := capturePipelineLogs(t, &buf)
	defer restore()

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "applyResumeState-orphan",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{{ID: "real_step", Command: "true"}},
	}
	executor := NewExecutor(DefaultExecutorConfig("session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "run-orphan",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"real_step":      {StepID: "real_step", Status: StatusCompleted, Output: "ok"},
			"ghost_iter_2_x": {StepID: "ghost_iter_2_x", Status: StatusCompleted, Output: "stale"},
		},
		Variables: map[string]interface{}{
			"steps.ghost_iter_2_x.output": "stale",
		},
	}

	executor.applyResumeState()

	if _, ok := executor.state.Steps["real_step"]; !ok {
		t.Errorf("real_step should still be in state.Steps after applyResumeState")
	}
	if _, ok := executor.state.Steps["ghost_iter_2_x"]; ok {
		t.Errorf("ghost_iter_2_x should have been dropped (no graph entry)")
	}

	events := parseJSONLEvents(t, &buf)
	var found map[string]any
	for _, evt := range events {
		if msg, _ := evt["msg"].(string); strings.Contains(msg, "resume dropped persisted step result") {
			found = evt
			break
		}
	}
	if found == nil {
		t.Fatalf("expected slog.Warn for dropped step; saw events = %#v", events)
	}
	if got, _ := found["step_id"].(string); got != "ghost_iter_2_x" {
		t.Errorf("step_id = %q, want %q", got, "ghost_iter_2_x")
	}
	if got, _ := found["run_id"].(string); got != "run-orphan" {
		t.Errorf("run_id = %q, want run-orphan", got)
	}
	if got, _ := found["workflow"].(string); got != workflow.Name {
		t.Errorf("workflow = %q, want %q", got, workflow.Name)
	}
	if level, _ := found["level"].(string); level != "WARN" {
		t.Errorf("level = %q, want WARN", level)
	}
}

func TestResumeParallelScopedChildDoesNotReExecuteCompletedSubstep(t *testing.T) {
	cfg := DefaultExecutorConfig("parallel-resume-scoped")
	cfg.DryRun = true
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-resume-scoped-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "parallel_group",
			Parallel: ParallelSpec{Steps: []Step{
				{ID: "step1", Prompt: "already done"},
				{ID: "step2", Prompt: "fresh work"},
			}},
		}},
	}

	priorFinishedAt := time.Now().Add(-1 * time.Hour)
	prior := &ExecutionState{
		RunID:      "run-parallel-resume-scoped",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		StartedAt:  priorFinishedAt.Add(-1 * time.Minute),
		Steps: map[string]StepResult{
			"parallel_group_step1": {
				StepID:     "parallel_group_step1",
				Status:     StatusCompleted,
				StartedAt:  priorFinishedAt.Add(-10 * time.Millisecond),
				FinishedAt: priorFinishedAt,
				Output:     "PRIOR-SCOPED-STEP1",
			},
		},
		Variables: make(map[string]interface{}),
	}

	state, err := executor.Resume(context.Background(), workflow, prior, nil)
	if err != nil {
		t.Fatalf("Resume() error = %v, want nil", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %s, want completed", state.Status)
	}

	step1, ok := state.Steps["parallel_group_step1"]
	if !ok {
		t.Fatalf("state.Steps[parallel_group_step1] missing after resume")
	}
	if step1.Output != "PRIOR-SCOPED-STEP1" {
		t.Fatalf("parallel_group_step1 output = %q, want prior output preserved", step1.Output)
	}
	if !step1.FinishedAt.Equal(priorFinishedAt) {
		t.Fatalf("parallel_group_step1 FinishedAt = %v, want prior timestamp %v", step1.FinishedAt, priorFinishedAt)
	}
	if step2, ok := state.Steps["parallel_group_step2"]; !ok || step2.Status != StatusCompleted {
		t.Fatalf("parallel_group_step2 = %+v, want freshly completed result", step2)
	}
}

func TestApplyResumeStateRetainsScopedBranchChildResult(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "branch-resume-scoped-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:     "router",
			Branch: "primary",
			Branches: map[string]interface{}{
				"primary": map[string]interface{}{
					"id":         "register_mail",
					"command":    "echo already-routed",
					"output_var": "branch_out",
				},
			},
		}},
	}
	executor := NewExecutor(DefaultExecutorConfig("branch-resume-scoped"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "run-branch-resume-scoped",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"router_register_mail": {
				StepID: "router_register_mail",
				Status: StatusCompleted,
				Output: "prior branch child",
			},
		},
		Variables: make(map[string]interface{}),
	}

	executor.applyResumeState()

	if _, ok := executor.state.Steps["router_register_mail"]; !ok {
		t.Fatalf("scoped branch child result was dropped as an orphan")
	}
	if got := executor.state.Variables["steps.router_register_mail.output"]; got != "prior branch child" {
		t.Fatalf("steps.router_register_mail.output = %v, want prior branch child", got)
	}
	if got := executor.state.Variables["branch_out"]; got != "prior branch child" {
		t.Fatalf("branch_out = %v, want prior branch child", got)
	}
}

// TestApplyResumeStateRebuildsRetainedStepOutputs covers bd-bllgq: when
// applyResumeState marks a completed step as already-executed in the
// dependency graph, it must also rebuild steps.<id>.output / data plus
// the output_var aliases. Otherwise a partially-written or legacy state
// file with Steps populated but Variables empty leaves downstream
// substitution unable to see the producer's output.
func TestApplyResumeStateRebuildsRetainedStepOutputs(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "applyResumeState-rebuild",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "producer", Command: "echo done", OutputVar: "producer_out"},
			{ID: "consumer", Command: "echo ${steps.producer.output}", DependsOn: []string{"producer"}},
		},
	}
	executor := NewExecutor(DefaultExecutorConfig("session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "run-rebuild",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"producer": {
				StepID:     "producer",
				Status:     StatusCompleted,
				Output:     "retained-output",
				ParsedData: map[string]interface{}{"k": "v"},
			},
		},
		Variables: map[string]interface{}{},
	}

	executor.applyResumeState()

	executor.varMu.RLock()
	defer executor.varMu.RUnlock()

	got, ok := executor.state.Variables["steps.producer.output"]
	if !ok {
		t.Errorf("steps.producer.output not rebuilt on resume; vars=%#v", executor.state.Variables)
	} else if got != "retained-output" {
		t.Errorf("steps.producer.output = %v, want retained-output", got)
	}
	if data, ok := executor.state.Variables["steps.producer.data"]; !ok {
		t.Errorf("steps.producer.data not rebuilt on resume")
	} else if m, _ := data.(map[string]interface{}); m["k"] != "v" {
		t.Errorf("steps.producer.data = %v, want {k:v}", data)
	}
	if got, _ := executor.state.Variables["producer_out"].(string); got != "retained-output" {
		t.Errorf("producer_out = %v, want retained-output", got)
	}
	if got, _ := executor.state.Variables["producer_out_parsed"].(map[string]interface{}); got["k"] != "v" {
		t.Errorf("producer_out_parsed = %v, want {k:v}", got)
	}
}
