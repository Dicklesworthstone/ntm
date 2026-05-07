package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ---------------------------------------------------------------------------
// resolveBranch + lookupBranch tests (bd-w6nth.1)
// ---------------------------------------------------------------------------

func newBranchTestExecutor() *Executor {
	cfg := DefaultExecutorConfig("test-session")
	cfg.RunID = "run-branch-test"
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "run-branch-test",
		WorkflowID: "test",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	return e
}

func TestResolveBranch_Literal(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "branch-lit",
		Branch: "fresh-pass",
	}

	key, err := e.resolveBranch(context.Background(), step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "fresh-pass" {
		t.Errorf("got %q, want %q", key, "fresh-pass")
	}
}

func TestResolveBranch_ShellCommand(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "branch-shell",
		Branch: "$(echo audit-only)",
	}

	key, err := e.resolveBranch(context.Background(), step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "audit-only" {
		t.Errorf("got %q, want %q", key, "audit-only")
	}
}

func TestResolveBranch_ShellTrimWhitespace(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "branch-ws",
		Branch: `$(printf "  spaced  \n")`,
	}

	key, err := e.resolveBranch(context.Background(), step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "spaced" {
		t.Errorf("got %q, want %q", key, "spaced")
	}
}

func TestResolveBranch_ShellFailure(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "branch-fail",
		Branch: "$(exit 1)",
	}

	_, err := e.resolveBranch(context.Background(), step)
	if err == nil {
		t.Fatal("expected error for failing shell command")
	}
}

func TestResolveBranch_VariableSubstitution(t *testing.T) {
	e := newBranchTestExecutor()
	e.state.Variables["mode"] = "fast"
	e.defaults = map[string]interface{}{"prefix": "run"}

	step := &Step{
		ID:     "branch-vars",
		Branch: "${vars.mode}",
	}

	key, err := e.resolveBranch(context.Background(), step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "fast" {
		t.Errorf("got %q, want %q", key, "fast")
	}
}

func TestLookupBranch_MatchFound(t *testing.T) {
	branches := map[string]interface{}{
		"fresh-pass": map[string]interface{}{"command": "echo fresh"},
		"audit-only": map[string]interface{}{"command": "echo audit"},
	}

	val, err := lookupBranch(branches, "fresh-pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val == nil {
		t.Fatal("expected non-nil value")
	}
}

func TestLookupBranch_NoMatch_Error(t *testing.T) {
	branches := map[string]interface{}{
		"a": map[string]interface{}{"command": "echo a"},
		"b": map[string]interface{}{"command": "echo b"},
	}

	_, err := lookupBranch(branches, "c")
	if err == nil {
		t.Fatal("expected error for unmatched branch key")
	}
	if !strings.Contains(err.Error(), "branch produced no matching key: c") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLookupBranch_DefaultFallback(t *testing.T) {
	branches := map[string]interface{}{
		"a":       map[string]interface{}{"command": "echo a"},
		"default": map[string]interface{}{"command": "echo fallback"},
	}

	val, err := lookupBranch(branches, "unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val == nil {
		t.Fatal("expected non-nil value")
	}
}

// ---------------------------------------------------------------------------
// parseBranchSteps tests (bd-w6nth.2)
// ---------------------------------------------------------------------------

func TestParseBranchSteps_SingleStep(t *testing.T) {
	val := map[string]interface{}{
		"id":      "step-a",
		"command": "echo hello",
	}
	steps, err := parseBranchSteps(val, "parent", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	if steps[0].Command != "echo hello" {
		t.Errorf("command=%q, want %q", steps[0].Command, "echo hello")
	}
}

func TestParseBranchSteps_ListOfSteps(t *testing.T) {
	val := []interface{}{
		map[string]interface{}{"id": "s1", "command": "echo one"},
		map[string]interface{}{"id": "s2", "command": "echo two"},
		map[string]interface{}{"id": "s3", "command": "echo three"},
	}
	steps, err := parseBranchSteps(val, "parent", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}
	if steps[2].Command != "echo three" {
		t.Errorf("step[2].command=%q, want %q", steps[2].Command, "echo three")
	}
}

func TestParseBranchSteps_AutoGeneratesID(t *testing.T) {
	val := map[string]interface{}{
		"command": "echo auto-id",
	}
	steps, err := parseBranchSteps(val, "dispatch", "fresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if steps[0].ID != "dispatch.fresh.0" {
		t.Errorf("ID=%q, want %q", steps[0].ID, "dispatch.fresh.0")
	}
}

// ---------------------------------------------------------------------------
// executeBranch integration tests (bd-w6nth.1 + bd-w6nth.2)
// ---------------------------------------------------------------------------

func TestExecuteBranch_SingleCommandStep(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-cmd",
		Branch: "fresh-pass",
		Branches: map[string]interface{}{
			"fresh-pass": map[string]interface{}{
				"id":      "do-fresh",
				"command": "echo fresh-output",
			},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed; error=%v", result.Status, result.Error)
	}
	if !strings.Contains(result.Output, "fresh-output") {
		t.Errorf("output=%q, want to contain %q", result.Output, "fresh-output")
	}
}

func TestExecuteBranch_ShellDispatch(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-shell-disp",
		Branch: "$(echo audit-only)",
		Branches: map[string]interface{}{
			"fresh-pass": map[string]interface{}{"command": "echo fresh"},
			"audit-only": map[string]interface{}{"command": "echo audit-result"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed; error=%v", result.Status, result.Error)
	}
	if !strings.Contains(result.Output, "audit-result") {
		t.Errorf("output=%q, want to contain %q", result.Output, "audit-result")
	}
}

func TestExecuteBranch_MultipleSteps(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-multi",
		Branch: "investigate",
		Branches: map[string]interface{}{
			"investigate": []interface{}{
				map[string]interface{}{"id": "inv-1", "command": "echo step-one"},
				map[string]interface{}{"id": "inv-2", "command": "echo step-two"},
				map[string]interface{}{"id": "inv-3", "command": "echo step-three"},
			},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed; error=%v", result.Status, result.Error)
	}
	if !strings.Contains(result.Output, "step-one") || !strings.Contains(result.Output, "step-three") {
		t.Errorf("output should contain all step outputs, got: %q", result.Output)
	}
}

func TestExecuteBranch_NoMatch_Error(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-nomatch",
		Branch: "unknown-key",
		Branches: map[string]interface{}{
			"a": map[string]interface{}{"command": "echo a"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", result.Status)
	}
	if result.Error == nil || !strings.Contains(result.Error.Message, "branch produced no matching key") {
		t.Errorf("expected 'no matching key' error, got: %v", result.Error)
	}
}

func TestExecuteBranch_DefaultFallback(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-default",
		Branch: "$(echo something-unexpected)",
		Branches: map[string]interface{}{
			"expected": map[string]interface{}{"command": "echo expected"},
			"default":  map[string]interface{}{"command": "echo fallback-ran"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed; error=%v", result.Status, result.Error)
	}
	if !strings.Contains(result.Output, "fallback-ran") {
		t.Errorf("expected fallback output, got: %q", result.Output)
	}
}

func TestExecuteBranch_DryRun(t *testing.T) {
	e := newBranchTestExecutor()
	e.config.DryRun = true
	step := &Step{
		ID:     "br-dry",
		Branch: "$(echo hello)",
		Branches: map[string]interface{}{
			"hello": map[string]interface{}{"command": "echo should-not-run"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed", result.Status)
	}
	if !strings.Contains(result.Output, "DRY RUN") {
		t.Errorf("expected DRY RUN in output, got: %q", result.Output)
	}
}

func TestExecuteBranch_DryRun_RendersDispatchLineWithDescription(t *testing.T) {
	// Branch dry-run must use the shared dryRunOutput() helper so the
	// dispatch line "▶ [step.id] description" appears, matching the
	// prompt/command/template/bead-query dry-run paths (bd-zc034). Without
	// this, branch steps lack the operator-facing dispatch line.
	e := newBranchTestExecutor()
	e.config.DryRun = true
	step := &Step{
		ID:          "br-described",
		Description: "Pick the right path",
		Branch:      "$(echo hello)",
		Branches: map[string]interface{}{
			"hello": map[string]interface{}{"command": "echo should-not-run"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed", result.Status)
	}
	wantDispatch := "▶ [br-described] Pick the right path"
	if !strings.Contains(result.Output, wantDispatch) {
		t.Errorf("expected dispatch line %q in output, got: %q", wantDispatch, result.Output)
	}
	if !strings.Contains(result.Output, "DRY RUN") {
		t.Errorf("expected DRY RUN in output, got: %q", result.Output)
	}
}

func TestExecuteBranch_ShellFailure(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-shellfail",
		Branch: "$(exit 42)",
		Branches: map[string]interface{}{
			"x": map[string]interface{}{"command": "echo x"},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", result.Status)
	}
	if result.Error == nil || !strings.Contains(result.Error.Message, "branch predicate failed") {
		t.Errorf("expected predicate-failed error, got: %v", result.Error)
	}
}

func TestExecuteBranch_BodyStepFails(t *testing.T) {
	e := newBranchTestExecutor()
	step := &Step{
		ID:     "br-fail-body",
		Branch: "go",
		Branches: map[string]interface{}{
			"go": []interface{}{
				map[string]interface{}{"id": "ok-step", "command": "echo ok"},
				map[string]interface{}{"id": "fail-step", "command": "exit 1"},
				map[string]interface{}{"id": "skip-step", "command": "echo should-not-run"},
			},
		},
	}

	result := e.executeBranch(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", result.Status)
	}
}

func TestExecuteBranch_VariableScopeCleanup(t *testing.T) {
	e := newBranchTestExecutor()
	e.state.Variables["keep_me"] = "preserved"

	step := &Step{
		ID:     "br-scope",
		Branch: "go",
		Branches: map[string]interface{}{
			"go": map[string]interface{}{
				"id":      "scope-step",
				"command": "echo scoped",
			},
		},
	}

	e.executeBranch(context.Background(), step, &Workflow{Name: "test"})

	if e.state.Variables["keep_me"] != "preserved" {
		t.Errorf("pre-existing variable lost after branch execution")
	}
}

// ---------------------------------------------------------------------------
// on_failure recovery dispatch tests (bd-w6nth.4)
// ---------------------------------------------------------------------------

func TestOnFailureRecoveryDispatchesTemplateToPane(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(tmpDir+"/recover.md", []byte("Recover <CAUSE>"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("recovery-session")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "recovery-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "fail",
			Command: "exit 7",
			OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
				"pane":     1,
				"template": "recover.md",
				"params": map[string]interface{}{
					"CAUSE": "${vars.reason}",
				},
			}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, map[string]interface{}{"reason": "broken"}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want original step failure")
	}

	result := state.Steps["fail"]
	if result.Status != StatusFailed {
		t.Fatalf("original step status = %s, want failed", result.Status)
	}
	recovery := state.Steps["fail.on_failure"]
	if recovery.Status != StatusCompleted {
		t.Fatalf("recovery status = %s, want completed; error=%+v", recovery.Status, recovery.Error)
	}

	history, err := mock.PasteHistory("%1")
	if err != nil {
		t.Fatalf("PasteHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory length = %d, want 1", len(history))
	}
	if history[0].Content != "Recover broken" {
		t.Fatalf("recovery paste = %q, want %q", history[0].Content, "Recover broken")
	}
}

func TestOnFailureRecoveryFailureIsRecorded(t *testing.T) {
	tmpDir := t.TempDir()
	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("recovery-session")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "recovery-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "fail",
			Command: "exit 9",
			OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
				"pane":     1,
				"template": "missing.md",
			}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want original step failure")
	}

	recovery := state.Steps["fail.on_failure"]
	if recovery.Status != StatusFailed {
		t.Fatalf("recovery status = %s, want failed", recovery.Status)
	}
	if len(state.Errors) == 0 {
		t.Fatal("state.Errors empty, want recovery failure record")
	}
	if state.Errors[len(state.Errors)-1].Type != "on_failure" {
		t.Fatalf("last error type = %q, want on_failure", state.Errors[len(state.Errors)-1].Type)
	}
	if result := state.Steps["fail"]; result.Error == nil || !strings.Contains(result.Error.Details, "on_failure recovery failed") {
		t.Fatalf("original error details = %+v, want recovery failure details", result.Error)
	}
}

func TestOnFailureRecoveryCanSuppressOriginalFailure(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(tmpDir+"/recover.md", []byte("Recover now"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("recovery-session")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "recovery-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "fail",
			Command: "exit 11",
			OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
				"pane":             1,
				"template":         "recover.md",
				"suppress_failure": true,
			}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after suppress_failure recovery", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %s, want completed", state.Status)
	}
	if result := state.Steps["fail"]; result.Status != StatusCompleted || result.Error != nil {
		t.Fatalf("original step result = %+v, want completed without error", result)
	}
}

func TestOnFailureActionSetsRuntimeVariableAndSkipsOriginalFailure(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("runtime-failure-session"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "runtime-failure-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:        "register_mail",
				Command:   "exit 4",
				OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
			},
			{
				ID:        "use_fallback",
				Command:   "echo fallback",
				DependsOn: []string{"register_mail"},
				When:      `${runtime.register_mail_failure_action} == "fallback_to_ntm_inbox"`,
			},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after handled on_failure action", err)
	}

	register := state.Steps["register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("register_mail status = %s, want skipped", register.Status)
	}
	if register.Error != nil {
		t.Fatalf("register_mail error = %+v, want nil after handled on_failure action", register.Error)
	}
	if !strings.Contains(register.SkipReason, "${runtime.register_mail_failure_action}") {
		t.Fatalf("register_mail SkipReason = %q, want runtime variable reference", register.SkipReason)
	}
	// bd-2ytru: on_failure recovery must set a structured SkipKind so the
	// step is distinguishable from unclassified skips in robot output and
	// persisted state.
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
	if result := state.Steps["use_fallback"]; result.Status != StatusCompleted {
		t.Fatalf("use_fallback status = %s, want completed; error=%+v", result.Status, result.Error)
	}
}

func TestOnFailureActionFiresInsideForeachBody(t *testing.T) {
	// A failed foreach body step with on_failure: <action> must run the
	// on_failure action just like a top-level step does. Without this,
	// moving a working step into a foreach body silently disabled the
	// recovery contract: the step returned StatusFailed without setting
	// runtime.<id>_failure_action, downstream guarded steps could not
	// route around it, and per-item fallback handling broke for
	// brennerbot / incident workflows.
	executor := NewExecutor(DefaultExecutorConfig("foreach-failure-session"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-onfailure-action-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items: `["alpha"]`,
					As:    "item",
					Steps: []Step{
						{
							ID:        "register_mail",
							Command:   "exit 4",
							OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
						},
					},
				},
			},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after handled on_failure action inside foreach", err)
	}

	register := state.Steps["fanout_iter0_register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("foreach body register_mail status = %s, want skipped (action ran)", register.Status)
	}
	if register.SkipKind != SkipKindOnFailureAction {
		t.Errorf("foreach body register_mail SkipKind = %q, want %q",
			register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.fanout_iter0_register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
}

func TestOnFailureActionNotSetOnSuccessSkipsRuntimeGuardedStep(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("runtime-failure-session"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "runtime-failure-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:        "register_mail",
				Command:   "echo ok",
				OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
			},
			{
				ID:        "use_fallback",
				Command:   "echo fallback",
				DependsOn: []string{"register_mail"},
				When:      `${runtime.register_mail_failure_action} == "fallback_to_ntm_inbox"`,
			},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if _, ok := state.Variables["runtime.register_mail_failure_action"]; ok {
		t.Fatalf("runtime failure action set after successful step: %v", state.Variables["runtime.register_mail_failure_action"])
	}
	if result := state.Steps["use_fallback"]; result.Status != StatusSkipped {
		t.Fatalf("use_fallback status = %s, want skipped", result.Status)
	}
}

// TestRunPostPipelineStepsExecuteAfterMainSuccess covers bd-w6nth.5: when
// the main pipeline graph completes successfully, post_pipeline_steps
// must run and their results land in state.Steps.
func TestRunPostPipelineStepsExecuteAfterMainSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("post-pipeline-success")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "post-pipeline-success-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "main", Command: "echo main"},
		},
		PostPipelineSteps: []Step{
			{ID: "notify", Command: "echo notified"},
			{ID: "cleanup", Command: "echo cleaned"},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, StatusCompleted)
	}
	for _, id := range []string{"notify", "cleanup"} {
		got, ok := state.Steps[id]
		if !ok {
			t.Errorf("state.Steps[%q] missing — post_pipeline_step did not run", id)
			continue
		}
		if got.Status != StatusCompleted {
			t.Errorf("post_pipeline_step %q status = %q, want %q (error=%+v)", id, got.Status, StatusCompleted, got.Error)
		}
	}
}

// TestRunPostPipelineStepsRunAfterMainFailure covers bd-w6nth.5: post-
// pipeline steps must run even when the main graph fails. The pipeline
// status remains Failed; post-step results are still persisted.
func TestRunPostPipelineStepsRunAfterMainFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("post-pipeline-fail")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "post-pipeline-fail-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "main", Command: "exit 7"},
		},
		PostPipelineSteps: []Step{
			{ID: "cleanup", Command: "echo cleaned"},
		},
	}

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	if state.Status != StatusFailed {
		t.Fatalf("state.Status = %q, want %q after main failure", state.Status, StatusFailed)
	}
	cleanup, ok := state.Steps["cleanup"]
	if !ok {
		t.Fatal("post_pipeline_step 'cleanup' missing — must run even after main failure")
	}
	if cleanup.Status != StatusCompleted {
		t.Errorf("cleanup status = %q, want %q (error=%+v)", cleanup.Status, StatusCompleted, cleanup.Error)
	}
}

// TestOnFailureActionFiresInsideBranchBody covers bd-afwly: a failed step
// inside a branch body with on_failure: fallback_to_ntm_inbox must set
// the runtime variable and convert to StatusSkipped, matching the
// top-level executeStep tail. Without the fix the body step kept its
// StatusFailed and downstream when: guards never saw the runtime var.
func TestOnFailureActionFiresInsideBranchBody(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("branch-failure-session"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "branch-onfailure-action-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:     "router",
				Branch: "primary",
				Branches: map[string]interface{}{
					"primary": map[string]interface{}{
						"id":         "register_mail",
						"command":    "exit 4",
						"on_failure": "fallback_to_ntm_inbox",
					},
				},
			},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after handled on_failure action", err)
	}

	register := state.Steps["register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("register_mail status = %s, want skipped (on_failure handled inside branch body)", register.Status)
	}
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
}

// TestOnFailureActionFiresInsideParallelChild covers bd-afwly: same
// contract for a parallel substep. Previously executeParallelStep
// returned the failed StepResult directly and skipped the on_failure
// tail entirely. Mock tmux gives us a real pane so dispatch reaches
// the retry loop and the prompt-resolution failure exercises the new
// tail.
func TestOnFailureActionFiresInsideParallelChild(t *testing.T) {
	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})

	executor := NewExecutor(DefaultExecutorConfig("parallel-failure-session"))
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-onfailure-action-workflow",
		Settings: WorkflowSettings{
			OnError: ErrorActionContinue,
		},
		Steps: []Step{
			{
				ID: "fanout",
				Parallel: ParallelSpec{Steps: []Step{
					{
						ID:         "register_mail",
						Pane:       PaneSpec{Index: 1},
						PromptFile: "/this/path/does/not/exist.txt",
						OnFailure:  OnFailureSpec{Action: "fallback_to_ntm_inbox"},
					},
				}},
			},
		},
	}

	state, _ := executor.Run(context.Background(), workflow, nil, nil)

	register := state.Steps["register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("register_mail status = %s, want skipped (on_failure handled inside parallel group); error=%+v", register.Status, register.Error)
	}
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
}
