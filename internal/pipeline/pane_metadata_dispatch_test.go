package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestExecuteTemplate_PaneMetadataAutomaticallyBound is the bd-2xka8
// regression: a normal workflow step with Pane: {Index: 2} and
// ${pane.role}/${pane.model}/${pane.domain} in its params must resolve
// against the configured pane WITHOUT the test having to manually call
// pushPaneMetadataVars first. Pre-fix, executeTemplate substituted
// variables before any pane metadata was bound, so production workflows
// silently dispatched templates with unresolved ${pane.X} references.
func TestExecuteTemplate_PaneMetadataAutomaticallyBound(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "dispatch.md")
	if err := os.WriteFile(templatePath, []byte("role=<ROLE> model=<MODEL> domain=<DOMAIN>\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	mock := NewMockTmuxClient(tmux.Pane{
		ID:      "%2",
		Index:   2,
		Type:    tmux.AgentCodex,
		Variant: "gpt-5",
		Tags:    []string{"role=investigator", "domain=H-005"},
	})

	cfg := DefaultExecutorConfig("pane-dispatch-session")
	cfg.ProjectDir = dir
	cfg.DefaultTimeout = time.Second
	e := NewExecutor(cfg)
	e.SetTmuxClient(mock)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Session:    cfg.Session,
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	step := &Step{
		ID:       "tpl-pane",
		Template: "dispatch.md",
		Params: map[string]interface{}{
			"ROLE":   "${pane.role}",
			"MODEL":  "${pane.model}",
			"DOMAIN": "${pane.domain}",
		},
		Pane: PaneSpec{Index: 2},
		Wait: WaitNone,
	}

	result := e.executeTemplate(context.Background(), step, &Workflow{Name: "workflow"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want completed; error = %+v", result.Status, result.Error)
	}

	history, err := mock.PasteHistory("%2")
	if err != nil {
		t.Fatalf("PasteHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory len = %d, want 1", len(history))
	}
	want := "role=investigator model=gpt-5 domain=H-005"
	if !strings.Contains(history[0].Content, want) {
		t.Fatalf("rendered template = %q, want substring %q", history[0].Content, want)
	}

	// After dispatch, the pane vars must have been popped — caller's
	// Variables map should be untouched.
	if _, leaked := e.state.Variables[paneVariableKey]; leaked {
		t.Fatalf("paneVariableKey leaked into Variables after executeTemplate; got %#v",
			e.state.Variables[paneVariableKey])
	}
}

// TestExecuteCommand_PaneMetadataAutomaticallyBound covers the command
// dispatch path: ${pane.role} inside step.Command must resolve to the
// configured pane's role without manual setup.
func TestExecuteCommand_PaneMetadataAutomaticallyBound(t *testing.T) {
	mock := NewMockTmuxClient(tmux.Pane{
		ID:      "%3",
		Index:   3,
		Type:    tmux.AgentClaude,
		Variant: "opus-4-7",
		Tags:    []string{"role=reviewer", "domain=H-007"},
	})

	cfg := DefaultExecutorConfig("pane-cmd-session")
	cfg.DefaultTimeout = 2 * time.Second
	e := NewExecutor(cfg)
	e.SetTmuxClient(mock)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	step := &Step{
		ID:        "echo_role",
		Command:   `printf '%s' '${pane.role}'`,
		OutputVar: "role",
		Pane:      PaneSpec{Index: 3},
	}

	result := e.executeCommand(context.Background(), step, &Workflow{Name: "workflow"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want completed; error = %+v", result.Status, result.Error)
	}
	if result.Output != "reviewer" {
		t.Fatalf("Output = %q, want %q", result.Output, "reviewer")
	}
	if _, leaked := e.state.Variables[paneVariableKey]; leaked {
		t.Fatalf("paneVariableKey leaked into Variables after executeCommand; got %#v",
			e.state.Variables[paneVariableKey])
	}
}

// TestBindStepPaneMetadata_NoOverrideWhenAlreadyBound verifies that when
// an outer foreach iteration has already pushed pane vars (paneVariableKey
// already populated), bindStepPaneMetadata is a no-op so foreach-scoped
// per-iteration values aren't replaced by a roster-only lookup.
func TestBindStepPaneMetadata_NoOverrideWhenAlreadyBound(t *testing.T) {
	mock := NewMockTmuxClient(tmux.Pane{
		ID:      "%2",
		Index:   2,
		Type:    tmux.AgentCodex,
		Variant: "gpt-5",
		Tags:    []string{"role=lookup-role"},
	})

	cfg := DefaultExecutorConfig("pane-noop-session")
	e := NewExecutor(cfg)
	e.SetTmuxClient(mock)

	preBound := map[string]interface{}{
		"role":  "foreach-bound-role",
		"model": "foreach-bound-model",
		"index": 2,
	}
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables: map[string]interface{}{
			paneVariableKey: preBound,
		},
		Steps: map[string]StepResult{},
	}

	step := &Step{ID: "noop", Pane: PaneSpec{Index: 2}}
	release := e.bindStepPaneMetadata(step)
	got, ok := e.state.Variables[paneVariableKey].(map[string]interface{})
	if !ok {
		t.Fatalf("paneVariableKey type = %T, want map[string]interface{}", e.state.Variables[paneVariableKey])
	}
	if got["role"] != "foreach-bound-role" {
		t.Fatalf("role overridden by lookup: got %v, want foreach-bound-role", got["role"])
	}
	release()
	// Release must be safe; vars stay as the foreach-bound values.
	got, _ = e.state.Variables[paneVariableKey].(map[string]interface{})
	if got["role"] != "foreach-bound-role" {
		t.Fatalf("release() altered vars: got %v, want foreach-bound-role", got["role"])
	}
}

// TestBindStepPaneMetadata_NoOpWhenStepHasNoPane verifies the early-out
// path: when step.Pane is empty, bindStepPaneMetadata writes nothing to
// state.Variables and returns a release function that's safe to call.
func TestBindStepPaneMetadata_NoOpWhenStepHasNoPane(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-empty-session")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	step := &Step{ID: "no-pane"} // No Pane.Index, no Pane.Expr
	release := e.bindStepPaneMetadata(step)
	if _, exists := e.state.Variables[paneVariableKey]; exists {
		t.Fatalf("paneVariableKey set on stepless dispatch: got %#v", e.state.Variables[paneVariableKey])
	}
	release() // must not panic
}
