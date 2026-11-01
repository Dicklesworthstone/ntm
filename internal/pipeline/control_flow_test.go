package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestOnSuccessStepsRunOnParentSuccess covers bd-w6nth.7: a successful
// parent step must trigger every step in its OnSuccess chain. Failures
// inside the chain are logged but do not flip the parent's status.
func TestOnSuccessStepsRunOnParentSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("on-success-success")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-success-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "main",
				Command: "echo main",
				OnSuccess: []Step{
					{ID: "notify", Command: "echo notified"},
					{ID: "log", Command: "echo logged"},
				},
			},
		},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if main := state.Steps["main"]; main.Status != StatusCompleted {
		t.Fatalf("main status = %q, want %q", main.Status, StatusCompleted)
	}
	for _, id := range []string{"main_on_success_notify", "main_on_success_log"} {
		got, ok := state.Steps[id]
		if !ok {
			t.Errorf("state.Steps[%q] missing — on_success step did not run", id)
			continue
		}
		if got.Status != StatusCompleted {
			t.Errorf("on_success step %q status = %q, want %q (error=%+v)", id, got.Status, StatusCompleted, got.Error)
		}
	}
}

// TestOnSuccessStepsSkipOnParentFailure covers bd-w6nth.7: when the
// parent step fails, OnSuccess steps must NOT run.
func TestOnSuccessStepsSkipOnParentFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("on-success-fail")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-fail-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "main",
				Command: "exit 7",
				OnSuccess: []Step{
					{ID: "should_not_run", Command: "echo wrong"},
				},
			},
		},
	}

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	if _, ok := state.Steps["should_not_run"]; ok {
		t.Fatalf("on_success step ran despite parent failure: %#v", state.Steps["should_not_run"])
	}
}

// TestOnSuccessChildFailureDoesNotFlipParentStatus covers bd-w6nth.7:
// if 1 of N OnSuccess steps fails, the others still run and the parent
// remains StatusCompleted.
func TestOnSuccessChildFailureDoesNotFlipParentStatus(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("on-success-mixed")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-mixed-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "main",
				Command: "echo main",
				OnSuccess: []Step{
					{ID: "ok_first", Command: "echo first"},
					{ID: "broken", Command: "exit 9"},
					{ID: "ok_third", Command: "echo third"},
				},
			},
		},
	}

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	if main := state.Steps["main"]; main.Status != StatusCompleted {
		t.Fatalf("main status = %q, want completed despite OnSuccess child failure", main.Status)
	}
	if state.Steps["main_on_success_ok_first"].Status != StatusCompleted {
		t.Errorf("ok_first did not run before the broken sibling")
	}
	if state.Steps["main_on_success_broken"].Status != StatusFailed {
		t.Errorf("broken status = %q, want failed", state.Steps["main_on_success_broken"].Status)
	}
	if state.Steps["main_on_success_ok_third"].Status != StatusCompleted {
		t.Errorf("ok_third did not run after the broken sibling")
	}
}

func TestOnSuccessExplicitIDsInsideForeachAreNamespaced(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("on-success-foreach"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-foreach-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "outer",
			Foreach: &ForeachConfig{
				Items: `["alpha","beta"]`,
				As:    "item",
				Steps: []Step{{
					ID:      "body",
					Command: "echo body-${item}",
					OnSuccess: []Step{{
						ID:      "notify",
						Command: "echo notified-${item}",
					}},
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if _, ok := state.Steps["notify"]; ok {
		t.Fatalf("state.Steps[notify] present — explicit on_success ID was not namespaced")
	}

	expected := map[string]string{
		"outer_iter0_body_on_success_notify": "notified-alpha",
		"outer_iter1_body_on_success_notify": "notified-beta",
	}
	for id, wantOutput := range expected {
		got, ok := state.Steps[id]
		if !ok {
			t.Fatalf("state.Steps[%q] missing — on_success result collided or did not run", id)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("%s status = %q, want completed; error=%+v", id, got.Status, got.Error)
		}
		if !strings.Contains(got.Output, wantOutput) {
			t.Fatalf("%s output = %q, want to contain %q", id, got.Output, wantOutput)
		}
	}
}

// TestOnSuccessFiresForTopLevelParallel locks the composite-step contract:
// when the parallel group reaches StatusCompleted, every OnSuccess child must
// run and land in state.Steps under the canonical
// <parent>_on_success_<child> key.
func TestOnSuccessFiresForTopLevelParallel(t *testing.T) {
	cfg := DefaultExecutorConfig("on-success-parallel")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-parallel-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fan",
			Parallel: ParallelSpec{
				Steps: []Step{
					{ID: "left", Pane: PaneSpec{Index: 1}, Prompt: "do left"},
					{ID: "right", Pane: PaneSpec{Index: 1}, Prompt: "do right"},
				},
			},
			OnSuccess: []Step{{ID: "notify", Command: "echo notified"}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["fan"]; got.Status != StatusCompleted {
		t.Fatalf("parent fan status = %q, want %q", got.Status, StatusCompleted)
	}
	got, ok := state.Steps["fan_on_success_notify"]
	if !ok {
		t.Fatalf("state.Steps[fan_on_success_notify] missing — top-level Parallel skipped its OnSuccess chain (bd-h8lc4 regression)")
	}
	if got.Status != StatusCompleted {
		t.Fatalf("on_success step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
}

// TestOnSuccessSkipsForFailedTopLevelParallel covers the negative leg: when
// the parallel group is not StatusCompleted, the OnSuccess chain must NOT run.
// This matches the existing OnSuccess contract for command steps.
func TestOnSuccessSkipsForFailedTopLevelParallel(t *testing.T) {
	cfg := DefaultExecutorConfig("on-success-parallel-fail")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-parallel-fail-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "fan",
			OnError: ErrorActionFailFast,
			Parallel: ParallelSpec{
				Steps: []Step{
					{ID: "ok", Pane: PaneSpec{Index: 1}, Prompt: "ok"},
					{ID: "boom", Pane: PaneSpec{Index: 1}, PromptFile: "/this/path/does/not/exist.txt"},
				},
			},
			OnSuccess: []Step{{ID: "should_not_run", Command: "echo wrong"}},
		}},
	}

	state, _ := executor.Run(context.Background(), workflow, nil, nil)
	if _, ok := state.Steps["fan_on_success_should_not_run"]; ok {
		t.Fatalf("on_success step ran despite parallel group failure: %#v", state.Steps["fan_on_success_should_not_run"])
	}
}

func TestOnFailureActionFiresForFailedTopLevelParallel(t *testing.T) {
	cfg := DefaultExecutorConfig("on-failure-parallel-parent")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-failure-parallel-parent-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:        "fan",
			OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
			Parallel: ParallelSpec{
				Steps: []Step{{
					ID:         "boom",
					Pane:       PaneSpec{Index: 1},
					PromptFile: "/this/path/does/not/exist.txt",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after handled top-level parallel on_failure action", err)
	}

	fan := state.Steps["fan"]
	if fan.Status != StatusSkipped {
		t.Fatalf("fan status = %s, want skipped; error=%+v", fan.Status, fan.Error)
	}
	if fan.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("fan SkipKind = %q, want %q", fan.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.fan_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
	if child := state.Steps["fan_boom"]; child.Status != StatusFailed {
		t.Fatalf("fan_boom status = %s, want failed before parent recovery", child.Status)
	}
}

func TestOnFailureActionFiresForFailedTopLevelLoop(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("on-failure-loop-parent"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-failure-loop-parent-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:        "watch",
			OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
			Loop: &LoopConfig{
				Times:         1,
				MaxIterations: IntOrExpr{Value: 1},
				Steps: []Step{{
					ID:      "tick",
					Command: "exit 7",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after handled top-level loop on_failure action", err)
	}

	watch := state.Steps["watch"]
	if watch.Status != StatusSkipped {
		t.Fatalf("watch status = %s, want skipped; error=%+v", watch.Status, watch.Error)
	}
	if watch.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("watch SkipKind = %q, want %q", watch.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.watch_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
	if child := state.Steps["watch_iter0_tick"]; child.Status != StatusFailed {
		t.Fatalf("watch_iter0_tick status = %s, want failed before parent recovery", child.Status)
	}
}

func TestRetryFiresForFailedTopLevelParallel(t *testing.T) {
	cfg := DefaultExecutorConfig("retry-parallel-parent")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "retry-parallel-parent-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "fan",
			OnError:    ErrorActionRetry,
			RetryCount: 2,
			RetryDelay: Duration{Duration: time.Nanosecond},
			Parallel: ParallelSpec{
				Steps: []Step{{
					ID:         "boom",
					Pane:       PaneSpec{Index: 1},
					PromptFile: "/this/path/does/not/exist.txt",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want exhausted retry failure")
	}
	fan := state.Steps["fan"]
	if fan.Status != StatusFailed {
		t.Fatalf("fan status = %s, want failed after retries", fan.Status)
	}
	if fan.Attempts != 3 {
		t.Fatalf("fan attempts = %d, want 3", fan.Attempts)
	}
}

func TestRetryFiresForFailedTopLevelLoop(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("retry-loop-parent"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "retry-loop-parent-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "watch",
			OnError:    ErrorActionRetry,
			RetryCount: 2,
			RetryDelay: Duration{Duration: time.Nanosecond},
			Loop: &LoopConfig{
				Times:         1,
				MaxIterations: IntOrExpr{Value: 1},
				Steps: []Step{{
					ID:      "tick",
					Command: "exit 7",
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want exhausted retry failure")
	}
	watch := state.Steps["watch"]
	if watch.Status != StatusFailed {
		t.Fatalf("watch status = %s, want failed after retries", watch.Status)
	}
	if watch.Attempts != 3 {
		t.Fatalf("watch attempts = %d, want 3", watch.Attempts)
	}
}

func TestRunFailsAfterRetryExhaustion(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("retry-exhaustion"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "retry-exhaustion-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "retry_exit",
			Command:    "exit 7",
			OnError:    ErrorActionRetry,
			RetryCount: 1,
			RetryDelay: Duration{Duration: time.Nanosecond},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want exhausted retry failure")
	}
	if state.Status != StatusFailed {
		t.Fatalf("workflow status = %s, want failed", state.Status)
	}
	step := state.Steps["retry_exit"]
	if step.Status != StatusFailed {
		t.Fatalf("retry_exit status = %s, want failed", step.Status)
	}
	if step.Attempts != 2 {
		t.Fatalf("retry_exit attempts = %d, want 2", step.Attempts)
	}
	if !strings.Contains(err.Error(), "retry_exit") {
		t.Fatalf("Run() error = %v, want step id", err)
	}
}

func TestFailedTopLevelParallelPreservesStructuredResultData(t *testing.T) {
	cfg := DefaultExecutorConfig("failed-parallel-data")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "failed-parallel-data-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fan",
			Parallel: ParallelSpec{
				Steps: []Step{
					{ID: "ok", Pane: PaneSpec{Index: 1}, Prompt: "ok"},
					{ID: "boom", Pane: PaneSpec{Index: 1}, PromptFile: "/this/path/does/not/exist.txt"},
				},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want failed parallel group")
	}

	fan := state.Steps["fan"]
	if fan.Status != StatusFailed {
		t.Fatalf("fan status = %s, want failed", fan.Status)
	}

	groupOutputs, ok := fan.ParsedData.(map[string]interface{})
	if !ok {
		t.Fatalf("fan ParsedData type = %T, want grouped parallel outputs", fan.ParsedData)
	}

	okEntry, ok := groupOutputs["fan_ok"].(map[string]interface{})
	if !ok {
		t.Fatalf("fan_ok entry missing from ParsedData: %#v", groupOutputs)
	}
	if okEntry["status"] != string(StatusCompleted) {
		t.Fatalf("fan_ok status = %v, want %s", okEntry["status"], StatusCompleted)
	}

	boomEntry, ok := groupOutputs["fan_boom"].(map[string]interface{})
	if !ok {
		t.Fatalf("fan_boom entry missing from ParsedData: %#v", groupOutputs)
	}
	if boomEntry["status"] != string(StatusFailed) {
		t.Fatalf("fan_boom status = %v, want %s", boomEntry["status"], StatusFailed)
	}
}

// TestOnSuccessFiresForBranchBodyChild covers bd-2g48y: a branch body
// step reaches its dispatcher via executeBranch -> executeStepOnce, which
// bypasses executeStep's common success tail. Without the bd-2g48y seam,
// an on_success chain attached to a branch body step
// was schema-accepted and silently skipped. Mirrors the bd-0fkcn fix for
// the foreach body case.
func TestOnSuccessFiresForBranchBodyChild(t *testing.T) {
	executor := newBranchTestExecutor()
	step := &Step{
		ID:     "router",
		Branch: "primary",
		Branches: map[string]interface{}{
			"primary": map[string]interface{}{
				"id":      "register_mail",
				"command": "echo registered",
				"on_success": []interface{}{
					map[string]interface{}{"id": "notify", "command": "echo notified"},
				},
			},
		},
	}
	workflow := &Workflow{Name: "branch-on-success-child", Steps: []Step{*step}}
	executor.graph = NewDependencyGraph(workflow)

	result := executor.executeBranch(context.Background(), step, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("branch status = %s, want completed; error=%v", result.Status, result.Error)
	}

	branchChildID := "router_register_mail"
	if got, ok := executor.state.Steps[branchChildID]; !ok || got.Status != StatusCompleted {
		t.Fatalf("state.Steps[%q] = %+v, want completed branch child", branchChildID, got)
	}

	onSuccessID := branchChildID + "_on_success_notify"
	got, ok := executor.state.Steps[onSuccessID]
	if !ok {
		t.Fatalf("state.Steps[%q] missing: branch body step skipped its on_success chain (bd-2g48y regression)", onSuccessID)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("on_success child status = %q, want completed (error=%+v)", got.Status, got.Error)
	}
}

// TestOnSuccessFiresForParallelSubstep covers bd-2g48y for the parallel
// case: parallel substeps run through executeParallelStep (an inlined
// retry+exec loop), not through the executeStep retry loop, so the
// common success tail never fires for an on_success chain attached to a
// parallel substep. DryRun lets the substep dispatch short-circuit before
// pane selection, the same fixture used by
// TestOnSuccessFiresForTopLevelParallel.
func TestOnSuccessFiresForParallelSubstep(t *testing.T) {
	cfg := DefaultExecutorConfig("parallel-substep-on-success")
	cfg.DryRun = true
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-substep-on-success-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fan",
			Parallel: ParallelSpec{Steps: []Step{{
				ID:     "child_a",
				Prompt: "do work",
				OnSuccess: []Step{
					{ID: "notify", Command: "echo notified"},
				},
			}}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["fan_child_a"]; got.Status != StatusCompleted {
		t.Fatalf("parallel substep status = %q, want %q", got.Status, StatusCompleted)
	}
	got, ok := state.Steps["fan_child_a_on_success_notify"]
	if !ok {
		t.Fatalf("state.Steps[fan_child_a_on_success_notify] missing: parallel substep skipped its on_success chain (bd-2g48y regression)")
	}
	if got.Status != StatusCompleted {
		t.Fatalf("on_success child status = %q, want completed (error=%+v)", got.Status, got.Error)
	}
}

// TestOnSuccessFiresForTopLevelLoop covers the Loop dispatch path. Composite
// steps must stay inside executeStep's common retry/success/failure tail so
// `loop: ... on_success: ...` behaves like ordinary command steps.
func TestOnSuccessFiresForTopLevelLoop(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("on-success-loop"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-loop-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "watch",
			Loop: &LoopConfig{
				MaxIterations: IntOrExpr{Value: 1},
				Steps: []Step{
					{ID: "tick", Command: "echo tick"},
				},
			},
			OnSuccess: []Step{{ID: "notify", Command: "echo notified"}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["watch"]; got.Status != StatusCompleted {
		t.Fatalf("parent watch status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
	got, ok := state.Steps["watch_on_success_notify"]
	if !ok {
		t.Fatalf("state.Steps[watch_on_success_notify] missing — top-level Loop skipped its OnSuccess chain (bd-h8lc4 regression)")
	}
	if got.Status != StatusCompleted {
		t.Fatalf("on_success step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
}

func TestOnSuccessFiresForTopLevelBranch(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("on-success-branch"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-branch-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:     "route",
			Branch: "go",
			Branches: map[string]interface{}{
				"go": map[string]interface{}{
					"id":      "chosen",
					"command": "echo chosen",
				},
			},
			OnSuccess: []Step{{ID: "notify", Command: "echo notified"}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["route"]; got.Status != StatusCompleted {
		t.Fatalf("parent route status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
	if got := state.Steps["route_on_success_notify"]; got.Status != StatusCompleted {
		t.Fatalf("on_success step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
}

func TestOnSuccessFiresForTopLevelForeach(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("on-success-foreach-parent"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-foreach-parent-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "fanout",
			Foreach: &ForeachConfig{
				Items: `["one"]`,
				Steps: []Step{{
					ID:      "body",
					Command: "echo body",
				}},
			},
			OnSuccess: []Step{{ID: "notify", Command: "echo notified"}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["fanout"]; got.Status != StatusCompleted {
		t.Fatalf("parent fanout status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
	if got := state.Steps["fanout_on_success_notify"]; got.Status != StatusCompleted {
		t.Fatalf("on_success step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
}

func TestOnSuccessFiresForTopLevelBeadQuery(t *testing.T) {
	cfg := DefaultExecutorConfig("on-success-bead-query")
	cfg.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		return []byte(`{"issues":[]}`), nil
	}
	executor := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "on-success-bead-query-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Status: "open",
			},
			OnSuccess: []Step{{ID: "notify", Command: "echo notified"}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := state.Steps["collect"]; got.Status != StatusCompleted {
		t.Fatalf("parent collect status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
	if got := state.Steps["collect_on_success_notify"]; got.Status != StatusCompleted {
		t.Fatalf("on_success step status = %q, want %q (error=%+v)", got.Status, StatusCompleted, got.Error)
	}
}

func TestBranchChildrenInsideForeachAreNamespaced(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("branch-foreach"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "branch-foreach-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "outer",
			Foreach: &ForeachConfig{
				Items: `["alpha","beta"]`,
				As:    "item",
				Steps: []Step{{
					ID:     "route",
					Branch: "go",
					Branches: map[string]interface{}{
						"go": map[string]interface{}{
							"id":      "chosen",
							"command": "echo branch-child",
						},
					},
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if _, ok := state.Steps["chosen"]; ok {
		t.Fatalf("state.Steps[chosen] present — branch child ID was not namespaced")
	}

	for _, id := range []string{"outer_iter0_route_chosen", "outer_iter1_route_chosen"} {
		got, ok := state.Steps[id]
		if !ok {
			t.Fatalf("state.Steps[%q] missing — branch child result collided or did not run", id)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("%s status = %q, want completed; error=%+v", id, got.Status, got.Error)
		}
	}
}

func TestParallelChildrenInsideForeachAreNamespaced(t *testing.T) {
	cfg := DefaultExecutorConfig("parallel-foreach")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "parallel-foreach-workflow",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "outer",
			Foreach: &ForeachConfig{
				Items: `["alpha","beta"]`,
				As:    "item",
				Steps: []Step{{
					ID: "fanout",
					Parallel: ParallelSpec{Steps: []Step{{
						ID:     "worker",
						Pane:   PaneSpec{Index: 1},
						Prompt: "work ${item}",
					}}},
				}},
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if _, ok := state.Steps["worker"]; ok {
		t.Fatalf("state.Steps[worker] present — parallel child ID was not namespaced")
	}

	expected := map[string]string{
		"outer_iter0_fanout_worker": "work alpha",
		"outer_iter1_fanout_worker": "work beta",
	}
	for id, wantOutput := range expected {
		got, ok := state.Steps[id]
		if !ok {
			t.Fatalf("state.Steps[%q] missing — parallel child result collided or did not run", id)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("%s status = %q, want completed; error=%+v", id, got.Status, got.Error)
		}
		if !strings.Contains(got.Output, wantOutput) {
			t.Fatalf("%s output = %q, want to contain %q", id, got.Output, wantOutput)
		}
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

func resumeFinalizationSavedState(t *testing.T, projectDir string, workflow *Workflow, completed map[string]StepResult) *ExecutionState {
	t.Helper()
	prior := &ExecutionState{
		RunID: "resume-finalization-test", WorkflowID: workflow.Name,
		Session: "resume-finalization", Status: StatusRunning,
		StartedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now(),
		Steps: completed, Variables: map[string]interface{}{"workspace": projectDir},
	}
	if err := SaveState(projectDir, prior); err != nil {
		t.Fatalf("save prior state: %v", err)
	}
	loaded, err := LoadState(projectDir, prior.RunID)
	if err != nil {
		t.Fatalf("load prior state: %v", err)
	}
	return loaded
}

func resumeFinalizationCompletedStep(stepID, output string) StepResult {
	return StepResult{
		StepID: stepID, Status: StatusCompleted, Output: output,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now().Add(-30 * time.Second),
	}
}

func resumeFinalizationConfig(projectDir string) ExecutorConfig {
	cfg := DefaultExecutorConfig("resume-finalization")
	cfg.ProjectDir = projectDir
	cfg.DefaultTimeout, cfg.GlobalTimeout = 5*time.Second, 15*time.Second
	return cfg
}

func assertResumeFinalizationOutputs(t *testing.T, state *ExecutionState, found, missing []string) {
	t.Helper()
	if state.OutputValidation == nil {
		t.Error("resumed workflow has no declared-output report")
		return
	}
	if !reflect.DeepEqual(state.OutputValidation.Found, found) || !reflect.DeepEqual(state.OutputValidation.Missing, missing) {
		t.Errorf("declared-output report = %+v, want found %v, missing %v", state.OutputValidation, found, missing)
	}
}

func TestResumeFinalizationRunsHooksAndValidatesOutputs(t *testing.T) {
	for _, mainFails := range []bool{false, true} {
		name := "main-success"
		if mainFails {
			name = "main-failure"
		}
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			mainCommand := "printf finished"
			wantStatus := StatusCompleted
			if mainFails {
				mainCommand, wantStatus = "exit 7", StatusFailed
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "resume-tail", Settings: DefaultWorkflowSettings(),
				Steps: []Step{
					{ID: "prepared", Command: "printf duplicate > duplicate-main.txt"},
					{ID: "finish", Command: mainCommand, DependsOn: []string{"prepared"}},
				},
				PostPipelineSteps: []Step{
					{ID: "failed-notification", Command: "exit 9"},
					{ID: "handback", Command: "printf handback > handback.txt; printf handback", OutputVar: "handback"},
					{ID: "consume-handback", Command: "printf '%s' '${vars.handback}' > consumed.txt"},
				},
				Outputs: []OutputDecl{
					{Name: "handback", Path: "${vars.workspace}/handback.txt"},
					{Name: "missing", Path: "${vars.workspace}/missing.txt"},
				},
			}
			prior := resumeFinalizationSavedState(t, projectDir, workflow, map[string]StepResult{
				"prepared": resumeFinalizationCompletedStep("prepared", "retained"),
			})
			state, err := NewExecutor(resumeFinalizationConfig(projectDir)).Resume(context.Background(), workflow, prior, nil)
			if (err != nil) != mainFails {
				t.Fatalf("Resume() error = %v, main failure = %v", err, mainFails)
			}
			if state.Status != wantStatus {
				t.Errorf("status = %q, want %q; post failures and missing outputs must remain advisory", state.Status, wantStatus)
			}
			for _, artifact := range []string{"handback.txt", "consumed.txt"} {
				data, err := os.ReadFile(filepath.Join(projectDir, artifact))
				if err != nil || string(data) != "handback" {
					t.Errorf("post-pipeline artifact %q = %q, %v; want handback", artifact, data, err)
				}
			}
			if _, err := os.Stat(filepath.Join(projectDir, "duplicate-main.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("completed main step ran again: %v", err)
			}
			found, missing := []string{filepath.Join(projectDir, "handback.txt")}, []string{filepath.Join(projectDir, "missing.txt")}
			assertResumeFinalizationOutputs(t, state, found, missing)
			persisted, err := LoadState(projectDir, state.RunID)
			if err != nil {
				t.Fatalf("load final state: %v", err)
			}
			assertResumeFinalizationOutputs(t, persisted, found, missing)
			for _, id := range []string{"handback", "consume-handback"} {
				if result := persisted.Steps[id]; result.Status != StatusCompleted {
					t.Errorf("persisted hook %q = %+v, want completed", id, result)
				}
			}
			postFailure := false
			for _, executionError := range persisted.Errors {
				if executionError.StepID == "failed-notification" && executionError.Type == "post_pipeline" && !executionError.Fatal {
					postFailure = true
				}
			}
			if !postFailure {
				t.Errorf("missing advisory post-pipeline failure: %+v", persisted.Errors)
			}
		})
	}
}

func TestResumeFinalizationValidatesOutputsWithoutHooks(t *testing.T) {
	projectDir := t.TempDir()
	outputPath := filepath.Join(projectDir, "result.txt")
	if err := os.WriteFile(outputPath, []byte("already written"), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "resume-outputs", Settings: DefaultWorkflowSettings(),
		Steps:   []Step{{ID: "main", Command: "exit 8"}},
		Outputs: []OutputDecl{{Name: "result", Path: outputPath}, {Name: "missing", Path: filepath.Join(projectDir, "missing.txt")}},
	}
	prior := resumeFinalizationSavedState(t, projectDir, workflow, map[string]StepResult{
		"main": resumeFinalizationCompletedStep("main", "done"),
	})
	state, err := NewExecutor(resumeFinalizationConfig(projectDir)).Resume(context.Background(), workflow, prior, nil)
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("Resume() = %v, %v; want completed", state, err)
	}
	assertResumeFinalizationOutputs(t, state, []string{outputPath}, []string{filepath.Join(projectDir, "missing.txt")})
	persisted, err := LoadState(projectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertResumeFinalizationOutputs(t, persisted, []string{outputPath}, []string{filepath.Join(projectDir, "missing.txt")})
}

func TestResumeFinalizationKeepsCompletedHooks(t *testing.T) {
	for _, completedContainer := range []bool{false, true} {
		name := "simple-main"
		if completedContainer {
			name = "completed-container-with-trimmed-child-state"
		}
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			effectsPath := filepath.Join(projectDir, "handoff-count.txt")
			if err := os.WriteFile(effectsPath, []byte("once\n"), 0600); err != nil {
				t.Fatal(err)
			}
			main := Step{ID: "main", Command: "exit 8"}
			if completedContainer {
				main = Step{ID: "main", Parallel: ParallelSpec{Steps: []Step{{ID: "trimmed", Command: "exit 8"}}}}
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "resume-partial-tail", Settings: DefaultWorkflowSettings(),
				Steps: []Step{main},
				PostPipelineSteps: []Step{
					{Command: "printf 'once\\n' >> handoff-count.txt; printf handoff.txt", OutputVar: "handback_file"},
					{ID: "finish-handback", Command: "printf '%s' '${steps.post_pipeline_1.output}' > '${vars.handback_file}'"},
				},
				Outputs: []OutputDecl{{Name: "handback", Path: "${vars.workspace}/${vars.handback_file}"}},
			}
			firstHook := resumeFinalizationCompletedStep("post_pipeline_1", "handoff.txt")
			prior := resumeFinalizationSavedState(t, projectDir, workflow, map[string]StepResult{
				"main": resumeFinalizationCompletedStep("main", "done"), "post_pipeline_1": firstHook,
			})
			state, err := NewExecutor(resumeFinalizationConfig(projectDir)).Resume(context.Background(), workflow, prior, nil)
			if err != nil || state.Status != StatusCompleted {
				t.Fatalf("Resume() = %v, %v; want completed", state, err)
			}
			if data, err := os.ReadFile(effectsPath); err != nil || string(data) != "once\n" {
				t.Errorf("already-completed post hook repeated side effect: %q, %v", data, err)
			}
			if got := state.Steps[firstHook.StepID]; got.Status != StatusCompleted || !got.FinishedAt.Equal(firstHook.FinishedAt) {
				t.Errorf("completed post hook receipt changed: %+v; want retained %+v", got, firstHook)
			}
			if data, err := os.ReadFile(filepath.Join(projectDir, "handoff.txt")); err != nil || string(data) != "handoff.txt" {
				t.Errorf("pending post hook did not use retained outputs: %q, %v", data, err)
			}
			assertResumeFinalizationOutputs(t, state, []string{filepath.Join(projectDir, "handoff.txt")}, nil)
		})
	}
}

func TestResumeFinalizationCancellationRunsOnlyOnCancel(t *testing.T) {
	projectDir := t.TempDir()
	settings := DefaultWorkflowSettings()
	settings.OnCancel = []Step{{ID: "cancel-cleanup", Command: "printf cleaned > cancel-cleanup.txt"}}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "resume-cancel-tail", Settings: settings,
		Steps:             []Step{{ID: "main", Command: "printf unexpected > main.txt"}},
		PostPipelineSteps: []Step{{ID: "handback", Command: "printf unexpected > handback.txt"}},
		Outputs:           []OutputDecl{{Name: "missing", Path: filepath.Join(projectDir, "missing.txt")}},
	}
	prior := resumeFinalizationSavedState(t, projectDir, workflow, map[string]StepResult{})
	// A cancelled attempt must not expose an output report from an earlier run.
	prior.OutputValidation = &OutputValidationResult{Found: []string{"stale"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := NewExecutor(resumeFinalizationConfig(projectDir)).Resume(ctx, workflow, prior, nil)
	if !errors.Is(err, context.Canceled) || state.Status != StatusCancelled {
		t.Fatalf("Resume() status = %q, error = %v; want cancelled", state.Status, err)
	}
	if data, err := os.ReadFile(filepath.Join(projectDir, "cancel-cleanup.txt")); err != nil || string(data) != "cleaned" {
		t.Errorf("on_cancel cleanup did not run: %q, %v", data, err)
	}
	for _, artifact := range []string{"main.txt", "handback.txt"} {
		if _, err := os.Stat(filepath.Join(projectDir, artifact)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cancelled run dispatched %q: %v", artifact, err)
		}
	}
	if state.OutputValidation != nil {
		t.Errorf("cancelled run validated outputs: %+v", state.OutputValidation)
	}
	persisted, err := LoadState(projectDir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusCancelled || persisted.Steps["cancel-cleanup"].Status != StatusCompleted || persisted.OutputValidation != nil {
		t.Errorf("cancelled final state = %+v", persisted)
	}
}

func TestResumeFinalizationStartsFreshTailWhenMainReopens(t *testing.T) {
	for _, kind := range []string{"direct", "on-success", "parallel", "foreach-rounds"} {
		t.Run(kind, func(t *testing.T) {
			cfg := resumeFinalizationConfig(t.TempDir())
			release := Step{ID: "release", Command: "printf 'released\\n' >> cleanups"}
			post, releasesPerAttempt := release, 1
			switch kind {
			case "on-success":
				post = Step{ID: "handoff", Command: "printf prepared", OnSuccess: []Step{release}}
			case "parallel":
				post = Step{ID: "handoff", Parallel: ParallelSpec{Steps: []Step{release}}}
			case "foreach-rounds":
				post = Step{ID: "handoff", Foreach: &ForeachConfig{
					Items: "${vars.resources}", MaxRounds: IntOrExpr{Value: 2}, Steps: []Step{release},
				}}
				releasesPerAttempt = 2
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "post-cleanup-each-attempt", Settings: DefaultWorkflowSettings(),
				Steps:             []Step{{ID: "work", Command: "printf 'acquired\\n' >> attempts; test -f allow"}},
				PostPipelineSteps: []Step{post},
			}
			first, err := NewExecutor(cfg).Run(context.Background(), workflow, map[string]interface{}{"resources": []interface{}{"resource"}}, nil)
			if err == nil || first == nil || first.Status != StatusFailed {
				t.Fatalf("initial attempt should fail: state=%+v err=%v", first, err)
			}
			prior, err := LoadState(cfg.ProjectDir, first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "allow"), []byte("allow"), 0600); err != nil {
				t.Fatal(err)
			}
			second, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err != nil || second == nil || second.Status != StatusCompleted {
				t.Fatalf("resumed attempt should complete: state=%+v err=%v", second, err)
			}
			attempts, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "attempts"))
			if err != nil || string(attempts) != "acquired\nacquired\n" {
				t.Errorf("main work did not reopen: %q, %v", attempts, err)
			}
			cleanups, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "cleanups"))
			if err != nil || string(cleanups) != strings.Repeat("released\n", 2*releasesPerAttempt) {
				t.Errorf("resumed main work must receive fresh cleanup: %q, %v", cleanups, err)
			}
		})
	}
}

func TestResumeFinalizationAdoptsCompletedSuccessChildBeforeRootCheckpoint(t *testing.T) {
	cfg := resumeFinalizationConfig(t.TempDir())
	calls := 0
	cfg.BeadQueryRunBr = func(_ context.Context, _ []string) ([]byte, error) {
		calls++
		return []byte(`{"issues":[]}`), nil
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "partial-root-tail", Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "main", Command: "printf main > main-effect"}},
		PostPipelineSteps: []Step{{ID: "tail", Command: "printf 'tail\\n' >> tail-effects", OnSuccess: []Step{{
			ID: "child", BeadQuery: &BeadQueryStep{}, OutputVar: "retained_child",
		}}}},
	}
	executor := NewExecutor(cfg)
	executor.state = resumeFinalizationSavedState(t, cfg.ProjectDir, workflow, map[string]StepResult{})
	executor.graph = NewDependencyGraph(workflow)
	mainResult := executor.executeStep(context.Background(), &workflow.Steps[0], workflow)
	if mainResult.Status != StatusCompleted {
		t.Fatalf("main fixture failed: %+v", mainResult)
	}
	executor.state.Steps[mainResult.StepID] = mainResult
	tailResult := executor.executeStep(finalizationContext(context.Background()), &workflow.PostPipelineSteps[0], workflow)
	if tailResult.Status != StatusCompleted || calls != 1 {
		t.Fatalf("tail fixture did not complete once: %+v calls=%d", tailResult, calls)
	}
	// A concurrent checkpoint can land after the child finishes but before
	// the post runner publishes the root result. The command receipt is durable.
	if err := executor.persistState(); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(cfg.ProjectDir, executor.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := prior.Steps["tail"]; exists {
		t.Fatal("fixture incorrectly published the tail root")
	}
	childID := "tail_on_success_child"
	child := prior.Steps[childID]
	// Completed step outputs must also survive a trimmed/legacy Variables map.
	prior.Variables = map[string]interface{}{}
	final, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
	if err != nil || final.Status != StatusCompleted {
		t.Fatalf("partial tail did not resume: state=%+v err=%v", final, err)
	}
	if calls != 1 {
		t.Fatalf("completed non-command success child was dispatched again: calls=%d", calls)
	}
	if got := final.Steps[childID]; !got.FinishedAt.Equal(child.FinishedAt) || final.Variables["retained_child"] != child.Output {
		t.Errorf("completed success child or named output was lost: %+v, %v", got, final.Variables)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "tail-effects")); err != nil || string(data) != "tail\n" {
		t.Errorf("tail command was re-executed: %q, %v", data, err)
	}
}

func TestResumeFinalizationRejectsNewDynamicPhaseCollisions(t *testing.T) {
	for _, fromPost := range []bool{false, true} {
		name := "main-materializes-id"
		if fromPost {
			name = "earlier-post-materializes-id"
		}
		t.Run(name, func(t *testing.T) {
			cfg := resumeFinalizationConfig(t.TempDir())
			producer := Step{ID: "batch", Foreach: &ForeachConfig{
				Items: "${vars.items}", Steps: []Step{{ID: "publish", Command: "printf first > first-effects; printf first"}},
			}}
			consumer := Step{ID: "batch_iter0", Parallel: ParallelSpec{Steps: []Step{{
				ID: "publish", Command: "printf unexpected > post-effects",
			}}}}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "dynamic-phase-collision", Settings: DefaultWorkflowSettings(),
				Steps: []Step{producer}, PostPipelineSteps: []Step{consumer},
			}
			if fromPost {
				workflow.Steps = []Step{{ID: "main", Command: "printf main"}}
				workflow.PostPipelineSteps = []Step{producer, consumer}
			}
			prior := resumeFinalizationSavedState(t, cfg.ProjectDir, workflow, map[string]StepResult{})
			prior.Variables["items"] = []interface{}{"one"}
			state, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err == nil || state.Status != StatusFailed || !strings.Contains(err.Error(), "shared by workflow phases") {
				t.Fatalf("new runtime ID hid a different post hook: status=%s err=%v", state.Status, err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "first-effects")); err != nil || string(data) != "first" {
				t.Fatalf("fixture never materialized its dynamic runtime ID: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "post-effects")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("ambiguous post hook dispatched: %v", err)
			}
			persisted, err := LoadState(cfg.ProjectDir, state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != StatusFailed || persisted.Steps["batch_iter0_publish"].Output != "first" {
				t.Errorf("phase rejection lost the already-executed result: %+v", persisted)
			}
		})
	}
}

func TestResumeFinalizationClearsReopenedNamedOutputs(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "failed-post-output"
		if reset {
			name = "reset-success-child-output"
		}
		t.Run(name, func(t *testing.T) {
			cfg := resumeFinalizationConfig(t.TempDir())
			cfg.ResumeOptions.Reset = reset
			producer := Step{ID: "producer", Command: "printf '%s' '${vars.previous | empty}' > observed; printf new", OutputVar: "previous"}
			if !reset {
				producer.Command = "if [ ! -f allow-producer ]; then printf old; exit 7; fi; " + producer.Command
			}
			post, priorID := producer, producer.ID
			if reset {
				post = Step{ID: "tail", Command: "printf parent", OnSuccess: []Step{producer}}
				priorID = "tail_on_success_producer"
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "reopened-post-output", Settings: DefaultWorkflowSettings(),
				Steps: []Step{{ID: "main", Command: "printf main"}}, PostPipelineSteps: []Step{post},
			}
			completed := map[string]StepResult{"main": resumeFinalizationCompletedStep("main", "done")}
			if reset {
				completed[priorID] = resumeFinalizationCompletedStep(priorID, "old")
			}
			prior := resumeFinalizationSavedState(t, cfg.ProjectDir, workflow, completed)
			if !reset {
				// Ordinary resume may reopen a proven-settled failure. Generate
				// that evidence through the command waiter; a bare failed legacy
				// result cannot establish that its process group stopped.
				executor := NewExecutor(cfg)
				executor.state, executor.graph = prior, NewDependencyGraph(workflow)
				previous := executor.executeStep(finalizationContext(context.Background()), &workflow.PostPipelineSteps[0], workflow)
				receipt := prior.CommandExecutions[priorID]
				if previous.Status != StatusFailed || previous.Output != "old" || receipt.Status != commandExecutionSettled || receipt.SettledAt.IsZero() {
					t.Fatalf("post fixture did not produce a settled failure: result=%+v receipt=%+v", previous, receipt)
				}
				prior.Steps[priorID] = previous
				if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "allow-producer"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			prior.Variables["previous"] = "stale"
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(cfg.ProjectDir, prior.RunID)
			if err != nil {
				t.Fatal(err)
			}
			state, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil)
			if err != nil || state.Status != StatusCompleted {
				t.Fatalf("Resume() = %v, %v; want completed", state, err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "observed")); err != nil || string(data) != "empty" {
				t.Errorf("reopened hook consumed stale named output: %q, %v", data, err)
			}
			if got := state.Variables["previous"]; got != "new" {
				t.Errorf("new hook output = %v, want new", got)
			}
		})
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

	register := state.Steps["router_register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("register_mail status = %s, want skipped (on_failure handled inside branch body)", register.Status)
	}
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.router_register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
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

	register := state.Steps["fanout_register_mail"]
	if register.Status != StatusSkipped {
		t.Fatalf("register_mail status = %s, want skipped (on_failure handled inside parallel group); error=%+v", register.Status, register.Error)
	}
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.fanout_register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}
}

// ---------------------------------------------------------------------------
// bd-w6nth.6 brennerbot-flavored end-to-end integration tests
// ---------------------------------------------------------------------------

// TestBranchPhaseDispatchHonorsAllFiveResumeModes covers the brennerbot-resume.yaml
// phase_dispatch contract: a single branches: map keyed by mode_to_resume must
// route to exactly one body per resume mode. Without per-mode coverage a typo
// in any branch entry silently falls through to "default" and resumes the
// wrong phase; this test pins each of the five modes to its own command so a
// regression in lookupBranch / parseBranchSteps shows up as a wrong-mode
// dispatch rather than a generic failure.
func TestBranchPhaseDispatchHonorsAllFiveResumeModes(t *testing.T) {
	modes := []string{"phase_2", "phase_3", "phase_4", "phase_5", "phase_6"}

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			executor := NewExecutor(DefaultExecutorConfig("resume-phase-dispatch"))

			workflow := &Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "brennerbot-resume",
				Settings:      DefaultWorkflowSettings(),
				Steps: []Step{{
					ID:     "phase_dispatch",
					Branch: "${vars.mode_to_resume}",
					Branches: map[string]interface{}{
						"phase_2": map[string]interface{}{"id": "do_phase_2", "command": "echo dispatched-phase_2"},
						"phase_3": map[string]interface{}{"id": "do_phase_3", "command": "echo dispatched-phase_3"},
						"phase_4": map[string]interface{}{"id": "do_phase_4", "command": "echo dispatched-phase_4"},
						"phase_5": map[string]interface{}{"id": "do_phase_5", "command": "echo dispatched-phase_5"},
						"phase_6": map[string]interface{}{"id": "do_phase_6", "command": "echo dispatched-phase_6"},
					},
				}},
			}

			state, err := executor.Run(context.Background(), workflow, map[string]interface{}{
				"mode_to_resume": mode,
			}, nil)
			if err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}

			dispatch := state.Steps["phase_dispatch"]
			if dispatch.Status != StatusCompleted {
				t.Fatalf("phase_dispatch status = %s, want completed; error=%+v", dispatch.Status, dispatch.Error)
			}

			wantToken := "dispatched-" + mode
			if !strings.Contains(dispatch.Output, wantToken) {
				t.Fatalf("phase_dispatch output for mode=%q = %q, want to contain %q", mode, dispatch.Output, wantToken)
			}
			for _, other := range modes {
				if other == mode {
					continue
				}
				if strings.Contains(dispatch.Output, "dispatched-"+other) {
					t.Fatalf("phase_dispatch output for mode=%q leaked branch %q dispatch: %q",
						mode, other, dispatch.Output)
				}
			}
		})
	}
}

// TestOnFailureFallbackToNtmInboxRoutesDownstreamCoordinations covers
// brennerbot's register_mail soft-failure contract end-to-end: when register_mail
// fails with on_failure: fallback_to_ntm_inbox, every downstream coordination
// guarded by ${runtime.register_mail_failure_action} == "fallback_to_ntm_inbox"
// must run, and any branch guarded by the inverse predicate must skip. Asserts
// the chain end-to-end (not just the runtime variable) so a future regression
// in when:-evaluation or skip propagation is caught.
func TestOnFailureFallbackToNtmInboxRoutesDownstreamCoordinations(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("register-mail-fallback"))

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-register-mail-fallback",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:        "register_mail",
				Command:   "exit 4",
				OnFailure: OnFailureSpec{Action: "fallback_to_ntm_inbox"},
			},
			{
				ID:        "coord_inbox_phase_3",
				Command:   "echo phase_3-via-inbox",
				DependsOn: []string{"register_mail"},
				When:      `${runtime.register_mail_failure_action} == "fallback_to_ntm_inbox"`,
			},
			{
				ID:        "coord_inbox_phase_4",
				Command:   "echo phase_4-via-inbox",
				DependsOn: []string{"register_mail"},
				When:      `${runtime.register_mail_failure_action} == "fallback_to_ntm_inbox"`,
			},
			{
				ID:        "coord_mcp_only",
				Command:   "echo phase_3-via-mcp",
				DependsOn: []string{"register_mail"},
				When:      `${runtime.register_mail_failure_action} != "fallback_to_ntm_inbox"`,
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
	if register.SkipKind != SkipKindOnFailureAction {
		t.Fatalf("register_mail SkipKind = %q, want %q", register.SkipKind, SkipKindOnFailureAction)
	}
	if got := state.Variables["runtime.register_mail_failure_action"]; got != "fallback_to_ntm_inbox" {
		t.Fatalf("runtime failure action = %v, want fallback_to_ntm_inbox", got)
	}

	for _, id := range []string{"coord_inbox_phase_3", "coord_inbox_phase_4"} {
		got, ok := state.Steps[id]
		if !ok {
			t.Fatalf("inbox-mode coordination %q missing from state.Steps", id)
		}
		if got.Status != StatusCompleted {
			t.Fatalf("inbox-mode coordination %q status = %s, want completed; error=%+v", id, got.Status, got.Error)
		}
	}

	mcp, ok := state.Steps["coord_mcp_only"]
	if !ok {
		t.Fatal("coord_mcp_only missing from state.Steps")
	}
	if mcp.Status != StatusSkipped {
		t.Fatalf("coord_mcp_only status = %s, want skipped (when:-guard inverse), output=%q", mcp.Status, mcp.Output)
	}
}

// TestOnFailurePaneTemplateRecoveryDispatchesMO03cToPaneOne covers
// brennerbot's phase_3_third_alt_check soft-failure contract: when the check
// step fails, the on_failure: {pane: 1, template: MO-03c-third-alternative.md}
// recovery must render the MO and paste it onto pane 1. Asserts the rendered
// content reaches the pane (not just that the recovery step finished) so a
// regression in pane selection or template substitution is caught.
func TestOnFailurePaneTemplateRecoveryDispatchesMO03cToPaneOne(t *testing.T) {
	tmpDir := t.TempDir()
	const moBody = "MO-03c third-alternative for hypothesis <HYPOTHESIS_ID>: " +
		"investigate the <THIRD_ALT_REASON> alternative.\n"
	if err := os.WriteFile(tmpDir+"/MO-03c-third-alternative.md", []byte(moBody), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := NewMockTmuxClient(tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("brennerbot-recovery")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase-3",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:      "phase_3_third_alt_check",
			Command: "exit 1",
			OnFailure: OnFailureSpec{Fallback: map[string]interface{}{
				"pane":     1,
				"template": "MO-03c-third-alternative.md",
				"params": map[string]interface{}{
					"HYPOTHESIS_ID":    "${vars.hypothesis_id}",
					"THIRD_ALT_REASON": "${vars.third_alt_reason}",
				},
			}},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, map[string]interface{}{
		"hypothesis_id":    "H-7",
		"third_alt_reason": "memory-pressure",
	}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want original step failure")
	}

	if main := state.Steps["phase_3_third_alt_check"]; main.Status != StatusFailed {
		t.Fatalf("phase_3_third_alt_check status = %s, want failed", main.Status)
	}
	recovery := state.Steps["phase_3_third_alt_check.on_failure"]
	if recovery.Status != StatusCompleted {
		t.Fatalf("recovery status = %s, want completed; error=%+v", recovery.Status, recovery.Error)
	}

	history, err := mock.PasteHistory("%1")
	if err != nil {
		t.Fatalf("PasteHistory(%%1) error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory length = %d, want 1 (MO-03c dispatch)", len(history))
	}
	wantContent := "MO-03c third-alternative for hypothesis H-7: " +
		"investigate the memory-pressure alternative."
	if !strings.Contains(history[0].Content, wantContent) {
		t.Fatalf("recovery paste = %q, want substring %q", history[0].Content, wantContent)
	}
}

// TestPostPipelineStepsDispatchesMO09HandbackTemplate covers
// brennerbot-squad.yaml's MO-09 handback contract: when the main pipeline
// completes, the post_pipeline_steps must dispatch the MO-09 handback template
// to the originating pane. Asserts the MO content reaches the pane (not just
// that the post-step finished) so a regression in runPostPipelineSteps' use of
// the regular executeStep machinery is caught.
func TestPostPipelineStepsDispatchesMO09HandbackTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	const moBody = "MO-09 handback to <ORIGINATING_PANE>: methodology run " +
		"<RUN_ID> complete.\n"
	if err := os.WriteFile(tmpDir+"/MO-09-handback.md", []byte(moBody), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := NewMockTmuxClient(tmux.Pane{ID: "%9", Index: 9, Type: tmux.AgentCodex})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("brennerbot-squad-handback")
	cfg.ProjectDir = tmpDir
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-squad",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "phase_8_freeze", Command: "echo phase-8-frozen"},
		},
		PostPipelineSteps: []Step{{
			ID:       "mo_09_handback",
			Template: "MO-09-handback.md",
			Pane:     PaneSpec{Index: 9},
			Wait:     WaitNone,
			Params: map[string]interface{}{
				"ORIGINATING_PANE": "9",
				"RUN_ID":           "squad-run-2026-05-07",
			},
		}},
	}

	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %s, want completed", state.Status)
	}

	handback, ok := state.Steps["mo_09_handback"]
	if !ok {
		t.Fatal("post_pipeline_step 'mo_09_handback' missing from state.Steps")
	}
	if handback.Status != StatusCompleted {
		t.Fatalf("handback status = %s, want completed; error=%+v", handback.Status, handback.Error)
	}

	history, err := mock.PasteHistory("%9")
	if err != nil {
		t.Fatalf("PasteHistory(%%9) error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory length = %d, want 1 (MO-09 dispatch)", len(history))
	}
	wantContent := "MO-09 handback to 9: methodology run squad-run-2026-05-07 complete."
	if !strings.Contains(history[0].Content, wantContent) {
		t.Fatalf("handback paste = %q, want substring %q", history[0].Content, wantContent)
	}
}

func TestOnFailureActionScopesCommandSettlement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope      string
		status     string
		background bool
		scheduler  ExecutionStatus
		rerun      bool
		malformed  bool
		handled    bool
	}{
		{name: "direct_running", scope: "direct", status: commandExecutionRunning},
		{name: "direct_background_ack", scope: "direct", status: commandExecutionRunning, background: true, scheduler: StatusCompleted},
		{name: "owned_foreground_running", scope: "parallel", status: commandExecutionRunning},
		{name: "owned_foreground_unresolved", scope: "parallel", status: commandExecutionUnresolved, scheduler: StatusFailed},
		{name: "owned_foreach_unresolved", scope: "foreach", status: commandExecutionUnresolved, scheduler: StatusFailed},
		{name: "background_started_without_ack", scope: "parallel", status: commandExecutionRunning, background: true, handled: true},
		{name: "background_launch_ack", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusCompleted, handled: true},
		{name: "background_starting_is_not_live_proof", scope: "parallel", status: commandExecutionStarting, background: true},
		{name: "background_returned_failed", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusFailed},
		{name: "background_returned_cancelled", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusCancelled},
		{name: "background_returned_skipped", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusSkipped},
		{name: "background_ack_requires_rerun", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusCompleted, rerun: true},
		{name: "background_returned_unresolved", scope: "parallel", status: commandExecutionUnresolved, background: true, scheduler: StatusCompleted},
		{name: "settled_native_failure", scope: "parallel", status: commandExecutionSettled, scheduler: StatusFailed, handled: true},
		{name: "invalid_settlement", scope: "parallel", status: commandExecutionSettled, malformed: true},
		{name: "invalid_background_running", scope: "parallel", status: commandExecutionRunning, background: true, scheduler: StatusCompleted, malformed: true},
		{name: "unrelated_similar_prefix", scope: "unrelated", status: commandExecutionUnresolved, scheduler: StatusFailed, handled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newBranchTestExecutor()
			child := Step{ID: "child", Command: "exit 7"}
			if tc.background {
				child.Wait = WaitNone
			}
			parent := Step{ID: "group", Parallel: ParallelSpec{Steps: []Step{child}},
				OnFailure: OnFailureSpec{Action: "use_verified_fallback"}}
			id := "group_child"
			switch tc.scope {
			case "direct":
				parent.Command, parent.Wait, parent.Parallel = child.Command, child.Wait, ParallelSpec{}
				id = parent.ID
			case "foreach":
				parent.Parallel = ParallelSpec{}
				parent.Foreach = &ForeachConfig{Items: "[\"one\"]", Steps: []Step{child}}
				id = "group_iter0_child"
			case "unrelated":
				id = "group_child_shadow"
			}
			definition := child
			if tc.scope == "direct" {
				definition = parent
			}
			definition.ID = id
			digest, err := commandDefinitionHash(&definition)
			if err != nil {
				t.Fatal(err)
			}
			started, finished := time.Now().Add(-time.Second), time.Now()
			record := CommandExecutionState{
				Version: commandExecutionVersion, StepID: id, StepHash: digest, StartedAt: started,
				Status: tc.status, Background: tc.background, ProcessID: 123,
			}
			if tc.status == commandExecutionStarting {
				record.ProcessID = 0
			}
			if tc.status == commandExecutionUnresolved || tc.status == commandExecutionSettled {
				record.Result = StepResult{
					StepID: id, AgentType: "command", Status: StatusFailed,
					StartedAt: started, FinishedAt: finished,
					Error: &StepError{Type: "command", Message: "recorded source failure"},
				}
				if tc.status == commandExecutionSettled {
					record.SettledAt = finished
				}
			}
			if tc.malformed {
				if tc.status == commandExecutionSettled {
					record.SettledAt = time.Time{}
				} else {
					record.ProcessID = 0
				}
			} else if err := validateCommandExecution(id, record); err != nil {
				t.Fatalf("invalid fixture receipt: %v", err)
			}
			executor.state.CommandExecutions = map[string]CommandExecutionState{id: record}
			if tc.scheduler != "" {
				executor.state.Steps[id] = StepResult{StepID: id, Status: tc.scheduler, RerunOnResume: tc.rerun}
			}
			failed := StepResult{StepID: parent.ID, Status: StatusFailed,
				Error: &StepError{Type: "command", Message: "original failure", Details: "retain the original cause"}}
			got := executor.executeOnFailureAction(&parent, failed)
			if tc.handled {
				if got.Status != StatusSkipped || executor.state.Variables["runtime.group_failure_action"] != "use_verified_fallback" {
					t.Fatalf("known failure policy was disabled by another outcome: %+v", got)
				}
			} else {
				if !reflect.DeepEqual(got, failed) {
					t.Fatalf("unknown source was handled or lost its cause: %+v", got)
				}
				if _, exists := executor.state.Variables["runtime.group_failure_action"]; exists {
					t.Fatal("unknown source published a failure action")
				}
			}
			if !reflect.DeepEqual(record, executor.state.CommandExecutions[id]) {
				t.Fatal("failure policy rewrote the receipt it inspected")
			}
		})
	}
}
