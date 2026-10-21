package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// fakeWorkflowSession scripts pane dispatch/capture for the runner loop.
type fakeWorkflowSession struct {
	mu         sync.Mutex
	dispatches []string          // "<pane>:<first line>"
	outputs    map[string]string // pane -> transcript the capture port returns
	onDispatch func(pane, prompt string)
}

func newFakeWorkflowSession() *fakeWorkflowSession {
	return &fakeWorkflowSession{outputs: make(map[string]string)}
}

func (f *fakeWorkflowSession) ports() workflowRunPorts {
	return workflowRunPorts{
		dispatch: func(_ context.Context, _, paneID, prompt string) error {
			f.mu.Lock()
			first, _, _ := strings.Cut(prompt, "\n")
			f.dispatches = append(f.dispatches, paneID+":"+first)
			hook := f.onDispatch
			f.mu.Unlock()
			if hook != nil {
				hook(paneID, prompt)
			}
			return nil
		},
		capture: func(_ context.Context, paneID string, _ int) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.outputs[paneID], nil
		},
	}
}

func (f *fakeWorkflowSession) say(pane, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs[pane] += "\n" + text
}

func (f *fakeWorkflowSession) dispatchedPanes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	panes := make([]string, len(f.dispatches))
	for i, d := range f.dispatches {
		panes[i], _, _ = strings.Cut(d, ":")
	}
	return panes
}

func pingPongTemplate() *workflow.WorkflowTemplate {
	return &workflow.WorkflowTemplate{
		Name:         "pp-test",
		Agents:       []workflow.WorkflowAgent{{Profile: "a", Role: "red"}, {Profile: "b", Role: "green"}},
		Coordination: workflow.CoordPingPong,
		Flow: &workflow.FlowConfig{
			Initial: "red",
			Transitions: []workflow.Transition{
				{From: "red", To: "green", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "RED-HANDOFF", Role: "red"}},
				{From: "green", To: "red", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "GREEN-HANDOFF", Role: "green"}},
			},
		},
	}
}

// The ping-pong loop must alternate dispatches red → green → red, driven by
// agent_says observations, and stop at the transition budget.
func TestWorkflowRunnerPingPongAlternates(t *testing.T) {
	fake := newFakeWorkflowSession()
	fake.onDispatch = func(pane, _ string) {
		// Each fake agent "finishes its turn" as soon as it is prompted.
		switch pane {
		case "%1":
			fake.say("%1", "RED-HANDOFF")
		case "%2":
			fake.say("%2", "GREEN-HANDOFF")
		}
	}
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
	runner, err := newWorkflowRunner(pingPongTemplate(), agents,
		workflowRunOptions{Session: "s", MaxTransitions: 2, Interval: 2 * time.Millisecond}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (result=%+v)", err, result)
	}
	if !result.Success || result.Reason != "max-transitions" || result.Transitions != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	wantStages := []string{"red", "green", "red"}
	if strings.Join(result.Stages, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("stages = %v, want %v", result.Stages, wantStages)
	}
	wantPanes := []string{"%1", "%2", "%1"}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != strings.Join(wantPanes, ",") {
		t.Fatalf("dispatch order = %v, want %v", got, wantPanes)
	}
}

// A flowless parallel workflow dispatches every role once and completes.
func TestWorkflowRunnerParallelDispatchesAll(t *testing.T) {
	tmpl := &workflow.WorkflowTemplate{
		Name: "par-test",
		Agents: []workflow.WorkflowAgent{
			{Profile: "x", Role: "approach-a"}, {Profile: "x", Role: "approach-b"}, {Profile: "x", Role: "approach-c"},
		},
		Coordination: workflow.CoordParallel,
	}
	fake := newFakeWorkflowSession()
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "approach-a"}, {ID: "%2", Role: "approach-b"}, {ID: "%3", Role: "approach-c"}}
	runner, err := newWorkflowRunner(tmpl, agents, workflowRunOptions{Session: "s"}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Completed || result.Reason != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1,%2,%3" {
		t.Fatalf("dispatch order = %v, want all three panes", got)
	}
}

func reviewGateTemplate(mode string) *workflow.WorkflowTemplate {
	return &workflow.WorkflowTemplate{
		Name: "rg-test",
		Agents: []workflow.WorkflowAgent{
			{Profile: "impl", Role: "author"},
			{Profile: "rev", Role: "reviewer", Count: 2},
		},
		Coordination: workflow.CoordReviewGate,
		Flow: &workflow.FlowConfig{
			Initial:         "implement",
			RequireApproval: true,
			ApprovalMode:    mode,
			Transitions: []workflow.Transition{
				{From: "implement", To: "review", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "submit"}},
				{From: "review", To: "complete", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "SHIP-VERDICT", Role: "reviewer"}},
			},
		},
	}
}

// approval_mode=all: one reviewer's verdict must NOT advance the gate; the
// transition fires only after every reviewer has approved (Approve wiring).
func TestWorkflowRunnerReviewGateRequiresAllApprovals(t *testing.T) {
	fake := newFakeWorkflowSession()
	var polls int
	var pollMu sync.Mutex
	fake.onDispatch = func(pane, _ string) {
		if pane == "%2" { // first reviewer approves immediately when engaged
			fake.say("%2", "SHIP-VERDICT")
		}
	}
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}}
	ports := fake.ports()
	baseCapture := ports.capture
	ports.capture = func(ctx context.Context, paneID string, lines int) (string, error) {
		pollMu.Lock()
		polls++
		// Second reviewer approves only well after the first (poll 12+ is
		// several evaluation rounds later at a 2ms interval).
		if polls > 12 {
			pollMu.Unlock()
			fake.say("%3", "SHIP-VERDICT")
		} else {
			pollMu.Unlock()
		}
		return baseCapture(ctx, paneID, lines)
	}
	runner, err := newWorkflowRunner(reviewGateTemplate("all"), agents,
		workflowRunOptions{Session: "s", MaxTransitions: 5, Interval: 2 * time.Millisecond, FireManual: true}, ports)
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (result=%+v)", err, result)
	}
	if !result.Completed || result.Reason != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	wantStages := "implement,review,complete"
	if strings.Join(result.Stages, ",") != wantStages {
		t.Fatalf("stages = %v, want %s", result.Stages, wantStages)
	}
	// implement engages the author (fallback rule), review engages both reviewers.
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1,%2,%3" {
		t.Fatalf("dispatch order = %v, want [%%1 %%2 %%3]", got)
	}
}

// require_approval must gate approval transitions whose trigger names a role
// other than the conventional "reviewer" and whose target stage is not
// terminal: one approver's verdict must not advance the gate in mode=all.
func TestWorkflowRunnerReviewGateNonReviewerRoleNonTerminalTarget(t *testing.T) {
	tmpl := &workflow.WorkflowTemplate{
		Name: "rg-qa",
		Agents: []workflow.WorkflowAgent{
			{Profile: "impl", Role: "author"},
			{Profile: "qa", Role: "qa", Count: 2},
		},
		Coordination: workflow.CoordReviewGate,
		Flow: &workflow.FlowConfig{
			Initial:         "implement",
			RequireApproval: true,
			ApprovalMode:    "all",
			Transitions: []workflow.Transition{
				{From: "implement", To: "verify", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "submit"}},
				// Non-terminal target ("land" has an outgoing transition) and
				// a non-"reviewer" approver role.
				{From: "verify", To: "land", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "QA-SHIP", Role: "qa"}},
				{From: "land", To: "complete", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "land"}},
			},
		},
	}
	fake := newFakeWorkflowSession()
	fake.onDispatch = func(pane, _ string) {
		if pane == "%2" { // first qa approves as soon as it is engaged
			fake.say("%2", "QA-SHIP")
		}
	}
	var polls int
	var pollMu sync.Mutex
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "qa"}, {ID: "%3", Role: "qa"}}
	ports := fake.ports()
	baseCapture := ports.capture
	ports.capture = func(ctx context.Context, paneID string, lines int) (string, error) {
		pollMu.Lock()
		polls++
		late := polls > 12
		pollMu.Unlock()
		if late {
			fake.say("%3", "QA-SHIP") // second qa approves several rounds later
		}
		return baseCapture(ctx, paneID, lines)
	}
	runner, err := newWorkflowRunner(tmpl, agents,
		workflowRunOptions{Session: "s", MaxTransitions: 5, Interval: 2 * time.Millisecond, FireManual: true}, ports)
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (result=%+v)", err, result)
	}
	if !result.Completed || result.Reason != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	wantStages := "implement,verify,land,complete"
	if strings.Join(result.Stages, ",") != wantStages {
		t.Fatalf("stages = %v, want %s", result.Stages, wantStages)
	}
}

// recordTransitionLocked runs on the main loop (under dispatchMu, via
// advanceObservation) while the TimeoutMonitor goroutine's Pause action
// writes the same WorkflowState; both must synchronize on r.mu. Pause does
// not take dispatchMu, so holding it here does not serialize the two writers.
// Run with -race: this test exists to catch the unlocked r.state access.
func TestWorkflowRunnerPauseAndRecordTransitionAreRaceFree(t *testing.T) {
	fake := newFakeWorkflowSession()
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
	runner, err := newWorkflowRunner(pingPongTemplate(), agents,
		workflowRunOptions{Session: "s", StateDir: t.TempDir()}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	runner.mu.Lock()
	runner.state = &workflow.WorkflowState{
		WorkflowName: "pp-test", SessionName: "s", CurrentStage: "red",
		StageStartedAt: time.Now(), Agents: map[string]string{"%1": "red", "%2": "green"},
	}
	runner.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			runner.dispatchMu.Lock()
			runner.recordTransitionLocked("green")
			runner.dispatchMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			_ = workflowRunActions{r: runner}.Pause(context.Background(), "stage timeout")
		}
	}()
	wg.Wait()
	if reason, stopErr := runner.stopped(); reason != "paused" || stopErr == nil {
		t.Fatalf("stopped() = (%q, %v), want paused reason with error", reason, stopErr)
	}
}

// A stage timeout with on_timeout=pause must stop the run loop with the
// "paused" reason (the monitor fires on its own goroutine mid-run).
func TestWorkflowRunnerStageTimeoutPausesRun(t *testing.T) {
	fake := newFakeWorkflowSession() // triggers never fire; stage stalls
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
	runner, err := newWorkflowRunner(pingPongTemplate(), agents,
		workflowRunOptions{Session: "s", Interval: time.Millisecond, StateDir: t.TempDir()}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	runner.errorHandler = workflow.NewErrorHandler(workflow.ErrorHandlingConfig{
		OnTimeout: workflow.ErrorActionPause,
	}, workflowRunActions{r: runner})
	runner.timeoutMonitor = workflow.NewTimeoutMonitor(5*time.Millisecond, runner.errorHandler, runner.coordinator.CurrentStage)
	defer runner.timeoutMonitor.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "paused" {
		t.Fatalf("want paused result, got err=%v result=%+v", err, result)
	}
	// The pause must be persisted so a rerun fails closed without --resume.
	store := &workflow.StateStore{Dir: runner.opts.StateDir}
	state, loadErr := store.Load("s")
	if loadErr != nil || state == nil || !state.Paused {
		t.Fatalf("persisted state = %+v (err=%v), want Paused", state, loadErr)
	}
}

// Operator cancellation (parent context canceled) must not be labeled a
// timeout.
func TestWorkflowRunnerCancelIsNotTimeout(t *testing.T) {
	fake := newFakeWorkflowSession()
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
	runner, err := newWorkflowRunner(pingPongTemplate(), agents,
		workflowRunOptions{Session: "s", Interval: 2 * time.Millisecond}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "canceled" {
		t.Fatalf("want canceled failure, got err=%v result=%+v", err, result)
	}
}

// A run whose triggers never fire must classify the deadline as a timeout.
func TestWorkflowRunnerTimesOut(t *testing.T) {
	fake := newFakeWorkflowSession()
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
	runner, err := newWorkflowRunner(pingPongTemplate(), agents,
		workflowRunOptions{Session: "s", Interval: 2 * time.Millisecond}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "timeout" {
		t.Fatalf("want timeout failure, got err=%v result=%+v", err, result)
	}
}

func TestWorkflowRunnerStageRoleResolution(t *testing.T) {
	fake := newFakeWorkflowSession()
	tmpl := reviewGateTemplate("any")
	tmpl.Routing = map[string]string{"triage": "reviewer"}
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}}
	runner, err := newWorkflowRunner(tmpl, agents, workflowRunOptions{Session: "s"}, fake.ports())
	if err != nil {
		t.Fatalf("newWorkflowRunner: %v", err)
	}
	cases := []struct{ stage, want string }{
		{"author", "author"},    // rule 1: exact role match
		{"triage", "reviewer"},  // rule 2: routing table
		{"review", "reviewer"},  // rule 3: outgoing trigger role
		{"implement", "author"}, // rule 4: first declared role
		{"complete", "author"},  // terminal stages also fall through to rule 4
	}
	for _, tc := range cases {
		if got := runner.stageRole(tc.stage); got != tc.want {
			t.Errorf("stageRole(%q) = %q, want %q", tc.stage, got, tc.want)
		}
	}
}

func TestResolveWorkflowForRunBuiltinAndNotFound(t *testing.T) {
	tmpl, err := resolveWorkflowForRun("red-green")
	if err != nil || tmpl.Name != "red-green" {
		t.Fatalf("builtin resolve failed: tmpl=%v err=%v", tmpl, err)
	}
	_, err = resolveWorkflowForRun("definitely-not-a-workflow")
	if err == nil {
		t.Fatal("want not-found error")
	}
	for _, name := range workflow.BuiltinNames() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("not-found error must list builtin %q; got: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "red-green") == false {
		t.Fatalf("not-found error missing builtins: %v", err)
	}
}

func TestResolveWorkflowForRunFromPath(t *testing.T) {
	dir := t.TempDir()
	single := filepath.Join(dir, "single.toml")
	if err := os.WriteFile(single, []byte(`
name = "custom-flow"
description = "single-format user workflow"
coordination = "parallel"

[[agents]]
profile = "cc"
role = "worker"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := resolveWorkflowForRun(single)
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if tmpl.Name != "custom-flow" || !strings.HasPrefix(tmpl.Source, "file:") {
		t.Fatalf("unexpected template: %+v", tmpl)
	}

	array := filepath.Join(dir, "array.toml")
	if err := os.WriteFile(array, []byte(`
[[workflows]]
name = "array-flow"
coordination = "parallel"

[[workflows.agents]]
profile = "cc"
role = "worker"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err = resolveWorkflowForRun(array)
	if err != nil || tmpl.Name != "array-flow" {
		t.Fatalf("array-format resolve: tmpl=%v err=%v", tmpl, err)
	}

	if _, err := resolveWorkflowForRun(filepath.Join(dir, "missing.toml")); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing path must produce the not-found error, got %v", err)
	}
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte(`name = "no-agents"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkflowForRun(bad); err == nil {
		t.Fatal("invalid workflow file must fail validation")
	}
}

// A quoted tilde path reaches resolveWorkflowForRun unexpanded by the shell;
// it must be expanded against the home directory rather than failing with a
// misleading not-found error.
func TestResolveWorkflowForRunExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "tilde-flow.toml"), []byte(`
name = "tilde-flow"
coordination = "parallel"

[[agents]]
profile = "cc"
role = "worker"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := resolveWorkflowForRun("~/tilde-flow.toml")
	if err != nil {
		t.Fatalf("resolve tilde path: %v", err)
	}
	if tmpl.Name != "tilde-flow" || !strings.Contains(tmpl.Source, home) {
		t.Fatalf("unexpected template: %+v", tmpl)
	}
	// ~user form stays unsupported but must say why instead of "not found".
	if _, err := resolveWorkflowForRun("~otheruser/flow.toml"); err == nil ||
		!strings.Contains(err.Error(), "~") || strings.Contains(err.Error(), "not found") {
		t.Fatalf("~user path must produce a tilde-specific error, got %v", err)
	}
}

func TestResolveWorkflowVars(t *testing.T) {
	tmpl := &workflow.WorkflowTemplate{
		Name: "v", Coordination: workflow.CoordParallel,
		Agents: []workflow.WorkflowAgent{{Profile: "cc", Role: "worker"}},
		Prompts: []workflow.SetupPrompt{
			{Key: "feature", Question: "What feature?", Required: true},
			{Key: "pattern", Question: "Pattern?", Default: "*_test.go"},
		},
	}
	if _, err := resolveWorkflowVars(tmpl, nil); err == nil || !strings.Contains(err.Error(), "feature") {
		t.Fatalf("missing required var must error with the key, got %v", err)
	}
	vars, err := resolveWorkflowVars(tmpl, []string{"feature=demo mode"})
	if err != nil {
		t.Fatalf("resolveWorkflowVars: %v", err)
	}
	if vars["feature"] != "demo mode" || vars["pattern"] != "*_test.go" {
		t.Fatalf("vars = %v", vars)
	}
	if _, err := resolveWorkflowVars(tmpl, []string{"nonsense"}); err == nil {
		t.Fatal("malformed --var must error")
	}
}

func TestAssignWorkflowPanes(t *testing.T) {
	tmpl := pingPongTemplate()
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Type: tmux.AgentUser},
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentClaude},
	}
	agents, err := assignWorkflowPanes(tmpl, panes)
	if err != nil {
		t.Fatalf("assignWorkflowPanes: %v", err)
	}
	if len(agents) != 2 || agents[0].ID != "%1" || agents[0].Role != "red" || agents[1].ID != "%2" || agents[1].Role != "green" {
		t.Fatalf("assignment = %+v", agents)
	}
	if _, err := assignWorkflowPanes(tmpl, panes[:2]); err == nil || !strings.Contains(err.Error(), "needs 2 agent pane(s)") {
		t.Fatalf("insufficient panes must error, got %v", err)
	}
}

func TestWorkflowRunnerIgnoresVerdictsBeforeStageAndOnReentry(t *testing.T) {
	for _, reenter := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial history", true: "second round"}[reenter], func(t *testing.T) {
			fake := newFakeWorkflowSession()
			fake.say("%1", "RED-HANDOFF")
			fake.say("%2", "GREEN-HANDOFF")
			redTurns := 0
			if reenter {
				fake.onDispatch = func(pane, _ string) {
					if pane == "%1" {
						redTurns++
						if redTurns == 1 {
							fake.say(pane, "RED-HANDOFF")
						}
					} else {
						fake.say(pane, "GREEN-HANDOFF")
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ports := fake.ports()
			polls := 0
			ports.sleep = func(context.Context, time.Duration) {
				polls++
				if polls == 5 {
					cancel()
				}
			}
			runner, err := newWorkflowRunner(pingPongTemplate(), []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}},
				workflowRunOptions{Session: "s", MaxTransitions: 6}, ports)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(ctx)
			wantTransitions, wantSends := 0, 1
			if reenter {
				wantTransitions, wantSends = 2, 3
			}
			if !errors.Is(err, context.Canceled) || result.Transitions != wantTransitions || len(fake.dispatchedPanes()) != wantSends {
				t.Fatalf("stale verdict advanced workflow: result=%+v err=%v sends=%v", result, err, fake.dispatchedPanes())
			}
		})
	}
}

func TestWorkflowRunnerKeepsDifferentReviewVerdictsSeparate(t *testing.T) {
	template := reviewGateTemplate("all")
	template.Flow.Initial = "review"
	template.Flow.Transitions = []workflow.Transition{
		{From: "review", To: "revise", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "REVISE-VERDICT", Role: "reviewer"}},
		{From: "review", To: "complete", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "SHIP-VERDICT", Role: "reviewer"}},
	}
	fake := newFakeWorkflowSession()
	fake.onDispatch = func(pane, _ string) {
		if pane == "%2" {
			fake.say(pane, "REVISE-VERDICT")
		} else if pane == "%3" {
			fake.say(pane, "SHIP-VERDICT")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := fake.ports()
	polls := 0
	ports.sleep = func(context.Context, time.Duration) {
		polls++
		if polls == 3 {
			cancel()
		}
	}
	runner, err := newWorkflowRunner(template, []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}},
		workflowRunOptions{Session: "s", StateDir: t.TempDir()}, ports)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) || result.Completed || result.Transitions != 0 {
		t.Fatalf("mixed verdicts satisfied a quorum: %+v %v", result, err)
	}
	state, err := runner.store.Load("s")
	if err != nil || state.Evidence == nil || len(state.Evidence.Matches[0]) != 1 || len(state.Evidence.Matches[1]) != 1 {
		t.Fatalf("independent verdict receipts = %+v, %v", state, err)
	}
}

func TestWorkflowRunnerRefusesItsOwnVerdictPrompt(t *testing.T) {
	fake := newFakeWorkflowSession()
	runner, err := newWorkflowRunner(pingPongTemplate(), []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}},
		workflowRunOptions{Session: "s", Vars: map[string]string{"instructions": "Reply RED-HANDOFF after finishing"}}, fake.ports())
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prompt itself matches") || result.Success || len(fake.dispatchedPanes()) != 0 {
		t.Fatalf("ambiguous prompt was delivered: %+v %v sends=%v", result, err, fake.dispatchedPanes())
	}
}

func TestWorkflowRunnerPreservesReceiptsWhenOutputEvidenceFails(t *testing.T) {
	for _, lostBoundary := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture failure", true: "screen replacement"}[lostBoundary], func(t *testing.T) {
			fake := newFakeWorkflowSession()
			fake.say("%1", "original unique screen")
			ports := fake.ports()
			capture := ports.capture
			ports.capture = func(ctx context.Context, pane string, lines int) (string, error) {
				if pane == "%1" && len(fake.dispatchedPanes()) > 0 {
					if lostBoundary {
						return "unrelated screen contains RED-HANDOFF", nil
					}
					return "", errors.New("recorded tmux capture failure")
				}
				return capture(ctx, pane, lines)
			}
			ports.sleep = func(context.Context, time.Duration) {}
			runner, err := newWorkflowRunner(pingPongTemplate(), []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}},
				workflowRunOptions{Session: "s", StateDir: t.TempDir()}, ports)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(context.Background())
			if err == nil || result.Reason != "observation-failed" || result.Transitions != 0 || len(fake.dispatchedPanes()) != 1 {
				t.Fatalf("untrusted capture advanced: %+v %v", result, err)
			}
			state, loadErr := runner.store.Load("s")
			if loadErr != nil || !state.Paused || state.Dispatches[0].Status != "delivered" || state.Evidence == nil || len(state.Evidence.Matches) != 0 {
				t.Fatalf("failure lost original evidence or receipt: %+v, %v", state, loadErr)
			}
		})
	}
}

func TestWorkflowObservationCannotApproveRetriedRound(t *testing.T) {
	fake := newFakeWorkflowSession()
	fake.onDispatch = func(pane, _ string) {
		if len(fake.dispatchedPanes()) == 1 {
			fake.say(pane, "RED-HANDOFF")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := fake.ports()
	ports.sleep = func(context.Context, time.Duration) { cancel() }
	runner, err := newWorkflowRunner(pingPongTemplate(), []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}},
		workflowRunOptions{Session: "s"}, ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Restart only the in-memory watcher at the persisted stage to inject the
	// race deterministically; no prompt is repeated by observation itself.
	if err := runner.coordinator.Start(&workflow.TriggerContext{Context: context.Background()}); err != nil {
		t.Fatal(err)
	}
	defer runner.coordinator.Stop()
	observed, err := runner.observe(context.Background())
	if err != nil || !observed.context.TransitionEvidence[0] {
		t.Fatalf("first round evidence = %+v, %v", observed, err)
	}
	if err := (workflowRunActions{r: runner}).RetryStage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fired, _, stale, err := runner.advanceObservation(context.Background(), observed); err != nil || fired || !stale {
		t.Fatalf("old round observation applied after retry: fired=%v stale=%v err=%v", fired, stale, err)
	}
	if runner.coordinator.CurrentStage() != "red" || len(runner.state.Evidence.Matches) != 0 || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("retry reused old verdict: %+v sends=%v", runner.state, fake.dispatchedPanes())
	}
}

func TestWorkflowRunnerDoesNotManufactureAnchorsByTrimmingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		prefix  string
	}{
		{name: "multiline partial line", pattern: `(?m)^APPROVED`, prefix: "NOT "},
		{name: "string partial line", pattern: `\AAPPROVED`, prefix: "NOT "},
		{name: "string complete line", pattern: `\AAPPROVED`, prefix: "preamble\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := pingPongTemplate()
			template.Flow.Transitions[0].Trigger.Pattern = tc.pattern
			fake := newFakeWorkflowSession()
			fake.onDispatch = func(pane, _ string) {
				// The exact response cannot match, but a 64 KiB suffix would
				// begin with APPROVED and invent a new regexp string/line start.
				fake.mu.Lock()
				fake.outputs[pane] = tc.prefix + "APPROVED" + strings.Repeat("x", workflowEvidenceMaxBytes-len("APPROVED"))
				fake.mu.Unlock()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ports := fake.ports()
			polls := 0
			ports.sleep = func(context.Context, time.Duration) {
				polls++
				if polls == 3 {
					cancel()
				}
			}
			opts := workflowRunOptions{
				Session: "s", StateDir: t.TempDir(), PanePIDs: map[string]int{"%1": 1001, "%2": 1002},
			}
			agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}
			runner, err := newWorkflowRunner(template, agents, opts, ports)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), "response evidence exceeds") || result.Reason != "observation-failed" || result.Transitions != 0 {
				t.Fatalf("unbounded response acquired a false anchor: %+v, %v", result, err)
			}
			state, err := runner.store.Load(opts.Session)
			if err != nil || !state.Paused || len(state.Evidence.Matches) != 0 || len(state.Evidence.Panes["%1"].Fresh) > workflowEvidenceMaxBytes || len(state.Dispatches) != 1 || state.Dispatches[0].Status != "delivered" {
				t.Fatalf("evidence-limit pause lost the bounded checkpoint or receipt: %+v, %v", state, err)
			}
			// The saved boundary is unchanged on failure: resume cannot turn
			// the same oversized response into a fresh suffix or resend a prompt.
			opts.Resume = true
			resumed, err := newWorkflowRunner(template, agents, opts, ports)
			if err != nil {
				t.Fatal(err)
			}
			polls = 0
			result, err = resumed.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), "response evidence exceeds") || result.Transitions != 0 || len(fake.dispatchedPanes()) != 1 {
				t.Fatalf("resume trusted truncated evidence or repeated delivery: %+v, %v, sends=%v", result, err, fake.dispatchedPanes())
			}
		})
	}
}

// fakeWorkflowAgents scripts agent health and restarts for supervision
// tests. It mirrors production identity: a restart replaces the pane's live
// shell PID, and the validate port fails unless the runner rebinds to it.
type fakeWorkflowAgents struct {
	mu        sync.Mutex
	live      map[string]int                // pane -> live shell PID
	faults    map[string]workflowAgentFault // current fault per pane
	restarts  []string
	checks    int
	nextPID   int
	onCheck   func(check int) // runs before each health observation
	onRestart func(pane string)
}

func newFakeWorkflowAgents(live map[string]int) *fakeWorkflowAgents {
	return &fakeWorkflowAgents{live: live, faults: make(map[string]workflowAgentFault), nextPID: 5000}
}

func crashFault(reason string) *workflowAgentFault {
	return &workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: reason, Confirmed: true}
}

func (f *fakeWorkflowAgents) set(pane string, fault *workflowAgentFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fault == nil {
		delete(f.faults, pane)
		return
	}
	f.faults[pane] = *fault
}

func (f *fakeWorkflowAgents) restarted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restarts...)
}

func (f *fakeWorkflowAgents) healthChecks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

// wire installs the fault, restart and identity ports. runner is read
// lazily: identity validation compares the runner's pane binding with the
// live PIDs, exactly like validateWorkflowPaneLifetimes.
func (f *fakeWorkflowAgents) wire(ports *workflowRunPorts, runner **workflowRunner) {
	ports.faults = func(_ context.Context, expected map[string]int) (map[string]workflowAgentFault, error) {
		f.mu.Lock()
		f.checks++
		check, hook := f.checks, f.onCheck
		f.mu.Unlock()
		if hook != nil {
			hook(check)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		out := make(map[string]workflowAgentFault)
		for pane, pid := range expected {
			if fault, ok := f.faults[pane]; ok && pid == f.live[pane] {
				out[pane] = fault
			}
		}
		return out, nil
	}
	ports.restart = func(_ context.Context, _, pane string) (int, error) {
		f.mu.Lock()
		f.restarts = append(f.restarts, pane)
		f.nextPID++
		pid := f.nextPID
		f.live[pane] = pid
		delete(f.faults, pane) // The relaunched agent starts healthy.
		hook := f.onRestart
		f.mu.Unlock()
		if hook != nil {
			hook(pane)
		}
		return pid, nil
	}
	ports.validate = func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		for pane, pid := range (*runner).opts.PanePIDs {
			if f.live[pane] != pid {
				return fmt.Errorf("pane %s changed process lifetime", pane)
			}
		}
		return nil
	}
}

func pingPongPIDs() map[string]int { return map[string]int{"%1": 1001, "%2": 1002} }

// A crashed agent pane runs on_agent_crash=restart_agent: the agent is
// relaunched, the run rebinds to the replacement process and its fresh
// screen, the stage prompt is re-sent, and the relaunched agent's verdict
// advances the workflow. The fault is reported and checkpointed.
func TestWorkflowRunnerRestartsCrashedAgentAndResendsStage(t *testing.T) {
	template := pingPongTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: workflow.ErrorActionRestartAgent}
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(pingPongPIDs())
	redTurns := 0
	fake.onDispatch = func(pane, _ string) {
		switch pane {
		case "%1":
			redTurns++
			if redTurns == 1 {
				// The agent dies mid-turn; only its shell is left.
				fake.say("%1", "$ ")
				agents.set("%1", crashFault("agent process exited; the pane is back at its shell"))
				return
			}
			fake.say("%1", "RED-HANDOFF")
		case "%2":
			fake.say("%2", "GREEN-HANDOFF")
		}
	}
	agents.onRestart = func(pane string) {
		// respawn-pane -k replaces the screen: the old boundary is gone.
		fake.mu.Lock()
		fake.outputs[pane] = "fresh agent ready"
		fake.mu.Unlock()
	}
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	opts := workflowRunOptions{
		Session: "s", StateDir: t.TempDir(), PanePIDs: pingPongPIDs(),
		MaxAgentRestarts: 3, MaxTransitions: 1, Interval: time.Millisecond,
	}
	var err error
	runner, err = newWorkflowRunner(template, []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}, opts, ports)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx)
	if err != nil || !result.Success || result.Reason != "max-transitions" || result.Transitions != 1 {
		t.Fatalf("restarted agent did not complete its stage: result=%+v err=%v", result, err)
	}
	if got := agents.restarted(); strings.Join(got, ",") != "%1" {
		t.Fatalf("restarts = %v, want [%%1]", got)
	}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1,%1,%2" {
		t.Fatalf("dispatches = %v, want the red prompt re-sent once before green", got)
	}
	wantFault := WorkflowRunFault{Pane: "%1", Role: "red", Stage: "red", Type: "agent_crash", Reason: "agent process exited; the pane is back at its shell", Action: "restart_agent"}
	if result.Restarts != 1 || len(result.Faults) != 1 || result.Faults[0] != wantFault {
		t.Fatalf("fault report = restarts %d faults %+v, want 1 restart and %+v", result.Restarts, result.Faults, wantFault)
	}
	state, err := runner.store.Load("s")
	if err != nil || state == nil {
		t.Fatalf("load checkpoint: %+v %v", state, err)
	}
	if state.PanePIDs["%1"] != 5001 || state.PanePIDs["%2"] != 1002 {
		t.Fatalf("checkpoint pane binding = %v, want %%1 rebound to the replacement pid 5001", state.PanePIDs)
	}
	if len(state.Errors) != 1 || state.Errors[0].Type != workflow.ErrorAgentCrash || state.Errors[0].AgentID != "%1" || state.Errors[0].Stage != "red" {
		t.Fatalf("checkpoint errors = %+v, want the red pane's crash", state.Errors)
	}
}

// An errored agent runs on_agent_error. Text-classified errors are
// debounced: a one-poll blip on another pane is not raised, a persistent
// error is raised once, and on_agent_error=pause persists the pause.
func TestWorkflowRunnerAgentErrorRunsOnAgentErrorPolicy(t *testing.T) {
	template := pingPongTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{OnAgentError: workflow.ErrorActionPause}
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(pingPongPIDs())
	rateLimited := &workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is rate limited"}
	agents.onCheck = func(check int) {
		switch check {
		case 1:
			agents.set("%2", rateLimited) // A single-poll blip.
		case 2:
			agents.set("%2", nil)
		case 3:
			agents.set("%1", rateLimited) // Persists: raised on its second poll.
		}
	}
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	opts := workflowRunOptions{
		Session: "s", StateDir: t.TempDir(), PanePIDs: pingPongPIDs(),
		AgentFaultPolls: 2, Interval: time.Millisecond,
	}
	var err error
	runner, err = newWorkflowRunner(template, []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}, opts, ports)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "paused" || !strings.Contains(err.Error(), "pane %1 (red): agent is rate limited") {
		t.Fatalf("agent error did not pause the run: result=%+v err=%v", result, err)
	}
	if checks := agents.healthChecks(); checks != 4 {
		t.Fatalf("health checks = %d, want the error raised on the 4th poll (2-poll debounce)", checks)
	}
	if len(result.Faults) != 1 || result.Faults[0].Pane != "%1" || result.Faults[0].Type != "agent_error" || result.Faults[0].Action != "pause" {
		t.Fatalf("faults = %+v, want only the persistent red error with action pause", result.Faults)
	}
	if got := agents.restarted(); len(got) != 0 {
		t.Fatalf("pause policy restarted agents: %v", got)
	}
	state, err := runner.store.Load("s")
	if err != nil || state == nil || !state.Paused || !strings.Contains(state.PauseReason, "agent is rate limited") {
		t.Fatalf("persisted pause = %+v, %v", state, err)
	}
	if len(state.Errors) != 1 || state.Errors[0].Type != workflow.ErrorAgentError || state.Errors[0].AgentID != "%1" {
		t.Fatalf("checkpoint errors = %+v, want the red pane's error", state.Errors)
	}
}

// A crash loop cannot restart forever: restart_agent runs at most
// MaxAgentRestarts times per pane, and the next crash aborts the run.
func TestWorkflowRunnerCrashLoopStopsAfterRestartBudget(t *testing.T) {
	template := pingPongTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: workflow.ErrorActionRestartAgent}
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(pingPongPIDs())
	fake.onDispatch = func(pane, _ string) {
		if pane == "%1" { // Every relaunch dies on its first prompt.
			agents.set("%1", crashFault("agent process exited"))
		}
	}
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	opts := workflowRunOptions{
		Session: "s", StateDir: t.TempDir(), PanePIDs: pingPongPIDs(),
		MaxAgentRestarts: 2, Interval: time.Millisecond,
	}
	var err error
	runner, err = newWorkflowRunner(template, []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}}, opts, ports)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "aborted" || !strings.Contains(err.Error(), "exhausted its restart budget (2 restart(s))") {
		t.Fatalf("crash loop was not stopped by the restart budget: result=%+v err=%v", result, err)
	}
	if got := agents.restarted(); strings.Join(got, ",") != "%1,%1" {
		t.Fatalf("restarts = %v, want exactly two", got)
	}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1,%1,%1" {
		t.Fatalf("dispatches = %v, want the initial prompt plus one re-send per restart", got)
	}
	if result.Restarts != 2 || len(result.Faults) != 3 {
		t.Fatalf("fault report = restarts %d faults %+v, want 2 restarts and 3 crashes", result.Restarts, result.Faults)
	}
}

// Without an error_handling policy a fault is still surfaced (notify), and
// it is raised once per episode rather than on every poll: a pane that
// recovers and crashes again is a new episode.
func TestWorkflowRunnerAgentFaultWithoutPolicyNotifiesOncePerEpisode(t *testing.T) {
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(pingPongPIDs())
	agents.onCheck = func(check int) {
		switch check {
		case 1, 5:
			agents.set("%2", crashFault("pane process exited (pane is dead)"))
		case 4:
			agents.set("%2", nil)
		}
	}
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	var noticesMu sync.Mutex
	var notices []string
	ports.notify = func(format string, args ...any) {
		noticesMu.Lock()
		defer noticesMu.Unlock()
		notices = append(notices, fmt.Sprintf(format, args...))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polls := 0
	ports.sleep = func(context.Context, time.Duration) {
		polls++
		if polls == 7 {
			cancel()
		}
	}
	var err error
	runner, err = newWorkflowRunner(pingPongTemplate(), []workflow.CoordinatorAgent{{ID: "%1", Role: "red"}, {ID: "%2", Role: "green"}},
		workflowRunOptions{Session: "s", PanePIDs: pingPongPIDs()}, ports)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) || result.Reason != "canceled" {
		t.Fatalf("notify policy changed the run outcome: result=%+v err=%v", result, err)
	}
	noticesMu.Lock()
	raised := 0
	for _, notice := range notices {
		if strings.Contains(notice, "workflow error: workflow agent_crash at red: pane %2 (green)") {
			raised++
		}
	}
	noticesMu.Unlock()
	if raised != 2 || len(result.Faults) != 2 || result.Faults[0].Action != "notify" {
		t.Fatalf("notifications = %d, faults = %+v; want one per crash episode with action notify", raised, result.Faults)
	}
	if got := agents.restarted(); len(got) != 0 {
		t.Fatalf("notify policy restarted agents: %v", got)
	}
}

// A flowless parallel template has no poll loop: its agents are checked once
// before the single dispatch, so a crashed agent is relaunched first.
func TestWorkflowRunnerParallelRestartsCrashedAgentBeforeDispatch(t *testing.T) {
	tmpl := &workflow.WorkflowTemplate{
		Name: "par-restart",
		Agents: []workflow.WorkflowAgent{
			{Profile: "x", Role: "approach-a"}, {Profile: "x", Role: "approach-b"}, {Profile: "x", Role: "approach-c"},
		},
		Coordination:  workflow.CoordParallel,
		ErrorHandling: &workflow.ErrorConfig{OnAgentCrash: workflow.ErrorActionRestartAgent},
	}
	pids := map[string]int{"%1": 1001, "%2": 1002, "%3": 1003}
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(map[string]int{"%1": 1001, "%2": 1002, "%3": 1003})
	agents.set("%2", crashFault("agent process exited; the pane is back at its shell"))
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	var err error
	runner, err = newWorkflowRunner(tmpl, []workflow.CoordinatorAgent{{ID: "%1", Role: "approach-a"}, {ID: "%2", Role: "approach-b"}, {ID: "%3", Role: "approach-c"}},
		workflowRunOptions{Session: "s", StateDir: t.TempDir(), PanePIDs: pids, MaxAgentRestarts: 1}, ports)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err != nil || !result.Completed || result.Restarts != 1 {
		t.Fatalf("result=%+v err=%v, want a completed run after one restart", result, err)
	}
	if got := agents.restarted(); strings.Join(got, ",") != "%2" {
		t.Fatalf("restarts = %v, want [%%2]", got)
	}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1,%2,%3" {
		t.Fatalf("dispatches = %v, want every pane prompted once after the restart", got)
	}
	state, err := runner.store.Load("s")
	if err != nil || state == nil || state.PanePIDs["%2"] != 5001 || state.Dispatches[1].Status != "delivered" {
		t.Fatalf("checkpoint = %+v, %v; want %%2 rebound and delivered", state, err)
	}
}

// The built-in specialist-team template's error policy is honored as
// written: on_agent_crash=restart_agent relaunches a crashed builder (idle in
// the design stage, so no prompt is re-sent to it) and on_agent_error=pause
// pauses the run when the QA agent errors.
func TestWorkflowRunnerBuiltinSpecialistTeamErrorPolicy(t *testing.T) {
	template, err := resolveWorkflowForRun("specialist-team")
	if err != nil {
		t.Fatal(err)
	}
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Type: tmux.AgentUser},
		{ID: "%1", Index: 1, Type: tmux.AgentClaude}, {ID: "%2", Index: 2, Type: tmux.AgentClaude},
		{ID: "%3", Index: 3, Type: tmux.AgentClaude}, {ID: "%4", Index: 4, Type: tmux.AgentClaude},
	}
	assigned, err := assignWorkflowPanes(template, panes)
	if err != nil {
		t.Fatal(err)
	}
	pids := map[string]int{"%1": 1001, "%2": 1002, "%3": 1003, "%4": 1004}
	fake := newFakeWorkflowSession()
	agents := newFakeWorkflowAgents(map[string]int{"%1": 1001, "%2": 1002, "%3": 1003, "%4": 1004})
	agents.onCheck = func(check int) {
		switch check {
		case 1:
			agents.set("%3", crashFault("agent process exited; the pane is back at its shell"))
		case 3:
			agents.set("%4", &workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is rate limited"})
		}
	}
	var runner *workflowRunner
	ports := fake.ports()
	agents.wire(&ports, &runner)
	opts := workflowRunOptions{
		Session: "s", StateDir: t.TempDir(), PanePIDs: pids, Vars: map[string]string{"project": "billing"},
		MaxAgentRestarts: 3, AgentFaultPolls: 1, Interval: time.Millisecond,
	}
	runner, err = newWorkflowRunner(template, assigned, opts, ports)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx)
	if err == nil || result.Reason != "paused" {
		t.Fatalf("qa error did not pause specialist-team: result=%+v err=%v", result, err)
	}
	if got := agents.restarted(); strings.Join(got, ",") != "%3" {
		t.Fatalf("restarts = %v, want the crashed builder %%3", got)
	}
	if got := fake.dispatchedPanes(); strings.Join(got, ",") != "%1" {
		t.Fatalf("dispatches = %v, want only the design prompt (the idle builder gets none)", got)
	}
	if len(result.Faults) != 2 ||
		result.Faults[0].Pane != "%3" || result.Faults[0].Role != "build" || result.Faults[0].Action != "restart_agent" ||
		result.Faults[1].Pane != "%4" || result.Faults[1].Role != "qa" || result.Faults[1].Action != "pause" {
		t.Fatalf("faults = %+v, want builder crash (restart_agent) then qa error (pause)", result.Faults)
	}
}

func TestClassifyWorkflowAgentHealth(t *testing.T) {
	alive := func(int) bool { return true }
	exited := func(int) bool { return false }
	fresh := status.FreshnessFresh
	for _, tc := range []struct {
		name    string
		row     health.AgentHealth
		local   bool
		alive   func(int) bool
		want    workflowAgentFault
		faulted bool
	}{
		{name: "working agent", row: health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, ObservedState: status.StateWorking}, local: true, alive: alive},
		{
			name: "agent exited to its shell", row: health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, ObservedState: status.StateIdle}, local: true, alive: exited,
			want: workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "agent process exited; the pane is back at its shell", Confirmed: true}, faulted: true,
		},
		{
			name: "remote pids are identifiers, not processes", local: false, alive: exited,
			row: health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, ProcessStatus: health.ProcessExited, Status: health.StatusError},
		},
		{
			name: "rate limited", local: true, alive: alive,
			row:  health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, RateLimited: true, Issues: []health.Issue{{Type: "rate_limit", Message: "Rate limit detected"}}},
			want: workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is rate limited"}, faulted: true,
		},
		{
			name: "blocked on an interactive gate", local: true, alive: alive,
			row:  health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, Issues: []health.Issue{{Type: "interactive_gate", Message: "Blocked on interactive gate screen"}}},
			want: workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "Blocked on interactive gate screen"}, faulted: true,
		},
		{
			name: "authentication failure", local: true, alive: alive,
			row:  health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, Issues: []health.Issue{{Type: "auth_error", Message: "Authentication error"}}},
			want: workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "Authentication error"}, faulted: true,
		},
		{
			name: "status detector error state", local: true, alive: alive,
			row:  health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, ObservedState: status.StateError, Issues: []health.Issue{{Type: "network_error", Message: "Network error"}}},
			want: workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is in an error state: Network error"}, faulted: true,
		},
		{
			name: "crash text under a live agent is not a crash", local: true, alive: alive,
			row: health.AgentHealth{ShellPID: 10, ObservationFreshness: fresh, ObservedState: status.StateIdle, Issues: []health.Issue{{Type: "crash", Message: "Agent crashed"}}},
		},
		{
			name: "stale observation is not trusted", local: true, alive: alive,
			row: health.AgentHealth{ShellPID: 10, ObservationFreshness: status.FreshnessStale, RateLimited: true, ObservedState: status.StateError},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, faulted := classifyWorkflowAgentHealth(tc.row, tc.local, tc.alive)
			if faulted != tc.faulted || got != tc.want {
				t.Fatalf("classify = (%+v, %v), want (%+v, %v)", got, faulted, tc.want, tc.faulted)
			}
		})
	}
}

func TestWorkflowRestartOutcomeAcceptsOnlyVerifiedRelaunch(t *testing.T) {
	ready := func() *robot.RestartPaneOutput {
		return &robot.RestartPaneOutput{
			RobotResponse:       robot.NewRobotResponse(true),
			Restarted:           []string{"2"},
			PaneShellPIDs:       map[string]robot.RestartPanePIDs{"2": {Before: 100, After: 200}},
			AgentRelaunchStatus: map[string]robot.RestartAgentRelaunchStatus{"2": robot.RestartAgentRelaunchReady},
		}
	}
	if pid, err := workflowRestartOutcome(ready()); err != nil || pid != 200 {
		t.Fatalf("ready relaunch = (%d, %v), want replacement pid 200", pid, err)
	}
	notReady := ready()
	notReady.Success = false
	notReady.AgentRelaunchStatus["2"] = robot.RestartAgentRelaunchNotReady
	notReady.Failed = []robot.RestartError{{Pane: "2", Reason: "agent not ready within 15s after relaunch"}}
	soft := ready()
	soft.PaneShellPIDs["2"] = robot.RestartPanePIDs{Before: 100, After: 100}
	for name, tc := range map[string]struct {
		out  *robot.RestartPaneOutput
		want string
	}{
		"agent not ready":   {out: notReady, want: "2: agent not ready"},
		"soft restart":      {out: soft, want: "no pane was respawned with a ready agent"},
		"no target matched": {out: &robot.RestartPaneOutput{RobotResponse: robot.NewRobotResponse(true)}, want: "no pane was respawned"},
		"no result":         {out: nil, want: "no result"},
	} {
		if pid, err := workflowRestartOutcome(tc.out); err == nil || pid != 0 || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: outcome = (%d, %v), want error containing %q", name, pid, err, tc.want)
		}
	}
}

// The production fault port classifies real tmux panes: a live agent child
// is healthy, the agent exiting back to its shell is a confirmed crash, and
// a pane that left the session is a confirmed crash.
func TestWorkflowAgentFaultsDetectsRealPaneCrash(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	if tmux.DefaultClient.Remote != "" {
		t.Skip("process liveness needs a local tmux server")
	}
	tmpDir := t.TempDir()
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "sleep 300"
	sessionName := fmt.Sprintf("ntm-wf-faults-%d", time.Now().UnixNano())
	if err := os.MkdirAll(filepath.Join(tmpDir, sessionName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := spawnSessionLogicContext(t.Context(), SpawnOptions{
		Session: sessionName, Agents: []FlatAgent{{Type: AgentTypeClaude, Index: 1}}, CCCount: 1, UserPane: true,
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = tmux.KillSession(sessionName) }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	panes, err := tmux.GetPanesContext(ctx, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	var agentPane tmux.Pane
	for _, pane := range panes {
		if pane.Type == tmux.AgentClaude {
			agentPane = pane
		}
	}
	if agentPane.ID == "" || agentPane.PID <= 0 {
		t.Fatalf("no claude pane in %+v", panes)
	}
	expected := map[string]int{agentPane.ID: agentPane.PID}
	waitFault := func(want workflowAgentFault) {
		t.Helper()
		var last map[string]workflowAgentFault
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			faults, err := workflowAgentFaults(ctx, sessionName, expected)
			if err != nil {
				t.Fatal(err)
			}
			last = faults
			if faults[agentPane.ID] == want {
				return
			}
		}
		t.Fatalf("faults = %+v, want %s: %+v", last, agentPane.ID, want)
	}
	for deadline := time.Now().Add(10 * time.Second); !process.HasChildAlive(agentPane.PID); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("agent process never started under the pane shell")
		}
	}
	faults, err := workflowAgentFaults(ctx, sessionName, expected)
	if err != nil || faults[agentPane.ID].Kind == workflow.ErrorAgentCrash {
		t.Fatalf("running agent classified as crashed: %+v, %v", faults, err)
	}
	// The agent CLI exits: Ctrl-C returns the pane to its shell.
	if _, err := tmux.DefaultClient.RunContext(ctx, "send-keys", "-t", tmux.ExactTarget(agentPane.ID), "C-c"); err != nil {
		t.Fatal(err)
	}
	waitFault(workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "agent process exited; the pane is back at its shell", Confirmed: true})
	// The pane leaves the session.
	if _, err := tmux.DefaultClient.RunContext(ctx, "kill-pane", "-t", tmux.ExactTarget(agentPane.ID)); err != nil {
		t.Fatal(err)
	}
	waitFault(workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "pane no longer exists", Confirmed: true})
}

// TestHealthTreatsAgentRootPaneAsRunning is the real-tmux ntm#359 regression
// guard. A pane can run its agent as the pane's own process instead of under
// an interactive shell (a boot script's `tmux new-session 'claude --resume'`,
// respawn-pane with a command). Then #{pane_pid} is the agent itself, with no
// children between tool calls, and the health check behind `ntm health`, the
// resilience monitor and workflow fault detection used to report it as an
// exited agent while it was working.
func TestHealthTreatsAgentRootPaneAsRunning(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	if tmux.DefaultClient.Remote != "" {
		t.Skip("process liveness needs a local tmux server")
	}
	tmpDir := t.TempDir()
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "sleep 300"
	sessionName := fmt.Sprintf("ntm-health-root-%d", time.Now().UnixNano())
	if err := os.MkdirAll(filepath.Join(tmpDir, sessionName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := spawnSessionLogicContext(t.Context(), SpawnOptions{
		Session: sessionName, Agents: []FlatAgent{{Type: AgentTypeClaude, Index: 1}}, CCCount: 1, UserPane: true,
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = tmux.KillSession(sessionName) }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	findAgentPane := func() tmux.Pane {
		t.Helper()
		panes, err := tmux.GetPanesContext(ctx, sessionName)
		if err != nil {
			t.Fatal(err)
		}
		for _, pane := range panes {
			if pane.Type == tmux.AgentClaude {
				return pane
			}
		}
		t.Fatalf("no claude pane in %+v", panes)
		return tmux.Pane{}
	}
	shellPane := findAgentPane()

	// Replace the pane's shell with the agent itself. respawn-pane keeps the
	// pane's options, so the pane stays typed as claude.
	if _, err := tmux.DefaultClient.RunContext(ctx, "respawn-pane", "-k", "-t", tmux.ExactTarget(shellPane.ID), "exec sleep 300"); err != nil {
		t.Fatal(err)
	}
	var agentPane tmux.Pane
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		agentPane = findAgentPane()
		if agentPane.PID > 0 && agentPane.PID != shellPane.PID && filepath.Base(strings.TrimSpace(agentPane.Command)) == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never became its own agent process: before=%+v after=%+v", shellPane, agentPane)
		}
	}
	if process.HasChildAlive(agentPane.PID) {
		t.Fatalf("pane root %d has a child; the fixture must model an agent with no tool call running", agentPane.PID)
	}

	report, err := health.CheckSession(ctx, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	var row *health.AgentHealth
	for i := range report.Agents {
		if report.Agents[i].PaneID == agentPane.ID {
			row = &report.Agents[i]
		}
	}
	if row == nil {
		t.Fatalf("pane %s missing from health report %+v", agentPane.ID, report.Agents)
	}
	if row.ProcessStatus != health.ProcessRunning || row.Status == health.StatusError {
		t.Fatalf("agent-root pane health = process %q status %q issues %+v, want a running agent", row.ProcessStatus, row.Status, row.Issues)
	}
	faults, err := workflowAgentFaults(ctx, sessionName, map[string]int{agentPane.ID: agentPane.PID})
	if err != nil {
		t.Fatal(err)
	}
	if fault, ok := faults[agentPane.ID]; ok {
		t.Fatalf("working agent-root pane classified as faulted: %+v", fault)
	}
}
