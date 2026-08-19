package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestResolvePaneExpr_DefaultsRef verifies that PaneSpec.Expr referencing
// ${defaults.X} is substituted and parsed as the 1-based pane index, the
// bd-6lkqr.4 happy path. After resolution, Pane.Index is populated and
// Pane.Expr is cleared so downstream pane lookup uses the integer form.
func TestResolvePaneExpr_DefaultsRef(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-defaults")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	e.defaults = map[string]interface{}{
		"triage_pane": "1",
	}

	step := &Step{ID: "tpl", Pane: PaneSpec{Expr: "${defaults.triage_pane}"}}
	if err := e.resolvePaneExprCtx(context.Background(), step); err != nil {
		t.Fatalf("resolvePaneExprCtx() error = %v", err)
	}
	if step.Pane.Index != 1 {
		t.Fatalf("Pane.Index = %d, want 1", step.Pane.Index)
	}
	if step.Pane.Expr != "" {
		t.Fatalf("Pane.Expr = %q, want \"\" after resolution", step.Pane.Expr)
	}
}

// TestResolvePaneExpr_VarsRef verifies a ${vars.X} reference resolves
// the same way as defaults — both forms must work because workflow
// authors may stash pane indices in either map.
func TestResolvePaneExpr_VarsRef(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-vars")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables: map[string]interface{}{
			"primary_pane": 3,
		},
		Steps: map[string]StepResult{},
	}

	step := &Step{ID: "cmd", Pane: PaneSpec{Expr: "${vars.primary_pane}"}}
	if err := e.resolvePaneExprCtx(context.Background(), step); err != nil {
		t.Fatalf("resolvePaneExprCtx() error = %v", err)
	}
	if step.Pane.Index != 3 {
		t.Fatalf("Pane.Index = %d, want 3", step.Pane.Index)
	}
}

// TestResolvePaneExpr_NonIntRejected covers the bead's "non-int string"
// acceptance: a ${session_id} reference (or any other string-typed value)
// that doesn't parse to an integer must surface a clear error instead of
// silently dispatching to pane index 0 / invalid pane.
func TestResolvePaneExpr_NonIntRejected(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-non-int")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables: map[string]interface{}{
			"session_id": "alpha-session",
		},
		Steps: map[string]StepResult{},
	}

	step := &Step{ID: "tpl", Pane: PaneSpec{Expr: "${vars.session_id}"}}
	err := e.resolvePaneExprCtx(context.Background(), step)
	if err == nil {
		t.Fatalf("resolvePaneExprCtx() error = nil, want non-int error")
	}
	if !strings.Contains(err.Error(), "not an integer pane index") {
		t.Fatalf("error = %v, want substring %q", err, "not an integer pane index")
	}
}

// TestResolvePaneExpr_NonPositiveRejected guards against a 0 or negative
// resolution — pane indices are 1-based throughout the codebase, so 0 is
// a sentinel meaning "no explicit pane" and would fall through to the
// agent-scoring path. Surfacing it as an error is friendlier than the
// silent fallthrough.
func TestResolvePaneExpr_NonPositiveRejected(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-zero")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables: map[string]interface{}{
			"bad_pane": 0,
		},
		Steps: map[string]StepResult{},
	}

	step := &Step{ID: "cmd", Pane: PaneSpec{Expr: "${vars.bad_pane}"}}
	err := e.resolvePaneExprCtx(context.Background(), step)
	if err == nil {
		t.Fatalf("resolvePaneExprCtx() error = nil, want positive-int error")
	}
	if !strings.Contains(err.Error(), "1-based") {
		t.Fatalf("error = %v, want substring %q", err, "1-based")
	}
}

// TestResolvePaneExpr_NoOpWhenIndexAlreadySet ensures the regression
// where literal pane: 3 (no Expr) keeps working — bd-6lkqr.4 acceptance
// requires no behavior change for the static-index path.
func TestResolvePaneExpr_NoOpWhenIndexAlreadySet(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-noop")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	step := &Step{ID: "tpl", Pane: PaneSpec{Index: 3}}
	if err := e.resolvePaneExprCtx(context.Background(), step); err != nil {
		t.Fatalf("resolvePaneExprCtx() error = %v on static-index step", err)
	}
	if step.Pane.Index != 3 {
		t.Fatalf("Pane.Index = %d, want 3 (unchanged)", step.Pane.Index)
	}
}

// TestResolvePaneExpr_NoOpWhenExprEmpty guards the early-return path —
// resolvePaneExprCtx must not error on steps that simply don't use a pane
// expression.
func TestResolvePaneExpr_NoOpWhenExprEmpty(t *testing.T) {
	cfg := DefaultExecutorConfig("pane-expr-empty")
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	step := &Step{ID: "no-pane"}
	if err := e.resolvePaneExprCtx(context.Background(), step); err != nil {
		t.Fatalf("resolvePaneExprCtx() error = %v on stepless dispatch", err)
	}
	if step.Pane.Index != 0 || step.Pane.Expr != "" {
		t.Fatalf("Pane = %+v, want zero-value", step.Pane)
	}
}

// TestSelectPane_ResolvesPaneExprBeforeLookup is the integration smoke:
// a Step with Pane.Expr referencing ${defaults.triage_pane} must result
// in selectPane returning the configured pane's ID rather than the
// pre-fix "not yet resolved" error.
func TestSelectPane_ResolvesPaneExprBeforeLookup(t *testing.T) {
	mock := NewMockTmuxClient(tmux.Pane{
		ID:    "%2",
		Index: 2,
		Type:  tmux.AgentClaude,
	})

	cfg := DefaultExecutorConfig("pane-expr-select")
	e := NewExecutor(cfg)
	e.SetTmuxClient(mock)
	e.state = &ExecutionState{
		RunID:      "test-run",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	e.defaults = map[string]interface{}{
		"triage_pane": 2,
	}

	step := &Step{ID: "tpl", Pane: PaneSpec{Expr: "${defaults.triage_pane}"}}
	paneID, agentType, err := e.selectPane(context.Background(), step)
	if err != nil {
		t.Fatalf("selectPane() error = %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("paneID = %q, want %%2", paneID)
	}
	if agentType != string(tmux.AgentClaude) {
		t.Fatalf("agentType = %q, want %q", agentType, tmux.AgentClaude)
	}
	// Resolution mutated the step; subsequent calls see Index, not Expr.
	if step.Pane.Index != 2 || step.Pane.Expr != "" {
		t.Fatalf("step.Pane after selectPane = %+v, want {Index:2, Expr:\"\"}", step.Pane)
	}
}
