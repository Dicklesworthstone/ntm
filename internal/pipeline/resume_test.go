package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
