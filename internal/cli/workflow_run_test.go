package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
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

// recordTransition runs on the main loop while the TimeoutMonitor goroutine's
// Pause action writes the same WorkflowState; both must synchronize on r.mu.
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
			runner.recordTransition("green")
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
