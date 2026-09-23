package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func resumeWorkflowTemplate() *workflow.WorkflowTemplate {
	return &workflow.WorkflowTemplate{
		Name: "resume-flow", Coordination: workflow.CoordPipeline,
		Agents:  []workflow.WorkflowAgent{{Profile: "build", Role: "build"}, {Profile: "review", Role: "review"}},
		Prompts: []workflow.SetupPrompt{{Key: "feature", Question: "What should be built?", Required: true}},
		Flow: &workflow.FlowConfig{
			Initial: "build", Stages: []string{"build", "review", "done"},
			Transitions: []workflow.Transition{
				{From: "build", To: "review", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "built"}},
				{From: "review", To: "done", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "reviewed"}},
			},
		},
	}
}

func resumeWorkflowAgents() []workflow.CoordinatorAgent {
	return []workflow.CoordinatorAgent{{ID: "%1", Role: "build"}, {ID: "%2", Role: "review"}}
}

func resumeWorkflowOptions(t *testing.T) workflowRunOptions {
	t.Helper()
	root := t.TempDir()
	return workflowRunOptions{
		Session: "resume-session", ProjectRoot: root,
		StateDir: filepath.Join(root, ".ntm", "workflows", "state"),
		Vars:     map[string]string{"feature": "durable continuation"},
		PanePIDs: map[string]int{"%1": 1001, "%2": 1002},
		Interval: time.Millisecond, MaxTransitions: 1, FireManual: true,
	}
}

func resumeWorkflowPorts(sent *[]string) workflowRunPorts {
	return workflowRunPorts{
		dispatch: func(_ context.Context, _, pane, prompt string) error {
			*sent = append(*sent, pane+":"+prompt)
			return nil
		},
		capture: func(context.Context, string, int) (string, error) { return "", nil },
	}
}

func mustResumeWorkflowRunner(t *testing.T, template *workflow.WorkflowTemplate, agents []workflow.CoordinatorAgent, opts workflowRunOptions, ports workflowRunPorts) *workflowRunner {
	t.Helper()
	runner, err := newWorkflowRunner(template, agents, opts, ports)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func mustLoadWorkflowCheckpoint(t *testing.T, opts workflowRunOptions) *workflow.WorkflowState {
	t.Helper()
	state, err := (&workflow.StateStore{Dir: opts.StateDir}).Load(opts.Session)
	if err != nil || state == nil {
		t.Fatalf("load checkpoint = (%+v, %v)", state, err)
	}
	return state
}

func TestWorkflowResumeContinuesSavedStageWithoutRepeatingPrompts(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	var sent []string
	first := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	result, err := first.Run(context.Background())
	if err != nil || result.Reason != "max-transitions" || len(sent) != 2 {
		t.Fatalf("initial run = %+v, %v, sends=%d", result, err, len(sent))
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if prior.CurrentStage != "review" || prior.Completed || prior.Turn != 2 || len(prior.StageHistory) != 1 || prior.Dispatches[0].Status != "delivered" {
		t.Fatalf("checkpoint = %+v", prior)
	}
	// Exercise the persisted pause path as well as an ordinary budget stop.
	if err := (&workflow.StateStore{Dir: opts.StateDir}).Pause(prior, "operator pause", time.Now()); err != nil {
		t.Fatal(err)
	}
	opts.Resume, opts.Vars = true, nil // Required variables come from the checkpoint.
	resumed := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	result, err = resumed.Run(context.Background())
	if err != nil || !result.Success || !result.Resumed || !result.Completed || !reflect.DeepEqual(result.Stages, []string{"review", "done"}) {
		t.Fatalf("resume = %+v, %v", result, err)
	}
	if len(sent) != 2 || resumed.opts.Vars["feature"] != "durable continuation" {
		t.Fatalf("resume resent work or lost variables: sends=%v vars=%v", sent, resumed.opts.Vars)
	}
	state := mustLoadWorkflowCheckpoint(t, opts)
	if state.Paused || !state.Completed || len(state.StageHistory) != 2 || state.Turn != 2 {
		t.Fatalf("completed checkpoint = %+v", state)
	}
	// A repeated resume after completion is observational, not a new run.
	replay := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	if result, err := replay.Run(context.Background()); err != nil || !result.Completed || len(sent) != 2 {
		t.Fatalf("completed replay = %+v, %v, sends=%d", result, err, len(sent))
	}
}

func TestWorkflowResumePartiallyDispatchedParallelStage(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	template := resumeWorkflowTemplate()
	template.Flow = nil
	template.Coordination = workflow.CoordParallel
	var sent []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := resumeWorkflowPorts(&sent)
	baseDispatch := ports.dispatch
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		journal := mustLoadWorkflowCheckpoint(t, opts)
		if len(journal.Dispatches) != 2 || journal.Dispatches[0].Status != "sending" || journal.Dispatches[1].Status != "pending" {
			t.Fatalf("no write-ahead plan before first prompt: %+v", journal.Dispatches)
		}
		if err := baseDispatch(ctx, session, pane, prompt); err != nil {
			return err
		}
		cancel() // Delivery succeeded; the next pane has not been attempted.
		return nil
	}
	first := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	if _, err := first.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error = %v", err)
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if prior.Dispatches[0].Status != "delivered" || prior.Dispatches[1].Status != "pending" {
		t.Fatalf("partial journal = %+v", prior.Dispatches)
	}
	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	if result, err := resumed.Run(context.Background()); err != nil || !result.Completed {
		t.Fatalf("resume = %+v, %v", result, err)
	}
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "%1:") || !strings.HasPrefix(sent[1], "%2:") {
		t.Fatalf("fan-out replayed or omitted a pane: %v", sent)
	}
}

func TestWorkflowResumeRefusesUnknownDeliveryAndRequiresExplicitRestart(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	var sent []string
	ports := resumeWorkflowPorts(&sent)
	baseDispatch := ports.dispatch
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		_ = baseDispatch(ctx, session, pane, prompt)
		return errors.New("connection lost after typing")
	}
	first := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, ports)
	if _, err := first.Run(context.Background()); err == nil {
		t.Fatal("lost response reported success")
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if prior.Dispatches[0].Status != "sending" {
		t.Fatalf("uncertain send was made retryable: %+v", prior.Dispatches)
	}
	path := filepath.Join(opts.StateDir, opts.Session+".json")
	before, _ := os.ReadFile(path)
	for _, resume := range []bool{false, true} {
		opts.Resume = resume
		runner := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
		result, err := runner.Run(context.Background())
		if err == nil || result.Success || len(sent) != 1 {
			t.Fatalf("resume=%v replayed uncertain work: %+v %v sends=%d", resume, result, err, len(sent))
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("rejected run mutated its checkpoint")
		}
	}
	opts.Resume, opts.Restart = false, true
	runner := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	if result, err := runner.Run(context.Background()); err != nil || !result.Success || result.Resumed || len(sent) != 3 {
		t.Fatalf("explicit restart = %+v %v sends=%d", result, err, len(sent))
	}
}

func TestWorkflowResumeRejectsMismatchedOrInvalidCheckpointWithoutMutation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*workflow.WorkflowState)
	}{
		{"legacy snapshot", func(s *workflow.WorkflowState) { s.ResumeVersion = 0 }},
		{"future version", func(s *workflow.WorkflowState) { s.ResumeVersion = 99 }},
		{"changed definition", func(s *workflow.WorkflowState) { s.TemplateHash = "different" }},
		{"changed workflow", func(s *workflow.WorkflowState) { s.WorkflowName = "another" }},
		{"changed project", func(s *workflow.WorkflowState) { s.ProjectRoot = "/another/project" }},
		{"changed role", func(s *workflow.WorkflowState) { s.Agents["%1"] = "review" }},
		{"changed lifetime", func(s *workflow.WorkflowState) { s.PanePIDs["%2"]++ }},
		{"unknown stage", func(s *workflow.WorkflowState) { s.CurrentStage = "not-a-stage" }},
		{"future time", func(s *workflow.WorkflowState) { s.StageStartedAt = time.Now().Add(time.Hour) }},
		{"invalid routing", func(s *workflow.WorkflowState) { s.NextByRole["review"] = -1 }},
		{"unknown delivery status", func(s *workflow.WorkflowState) { s.Dispatches[0].Status = "accepted-maybe" }},
		{"duplicate pane", func(s *workflow.WorkflowState) { s.Dispatches = append(s.Dispatches, s.Dispatches[0]) }},
		{"evidence version", func(s *workflow.WorkflowState) { s.Evidence.Version++ }},
		{"evidence stage", func(s *workflow.WorkflowState) { s.Evidence.Stage = "build" }},
		{"evidence round", func(s *workflow.WorkflowState) { s.Evidence.Round = s.Turn + 1 }},
		{"stale evidence round", func(s *workflow.WorkflowState) { s.Evidence.Round = s.Turn - 1 }},
		{"evidence pane lifetime", func(s *workflow.WorkflowState) { p := s.Evidence.Panes["%2"]; p.PID++; s.Evidence.Panes["%2"] = p }},
		{"evidence transition", func(s *workflow.WorkflowState) { s.Evidence.Matches = map[int][]string{99: {"%2"}} }},
		{"evidence wrong trigger", func(s *workflow.WorkflowState) { s.Evidence.Matches = map[int][]string{1: {"%2"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			var sent []string
			runner := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			if _, err := runner.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			state.Paused = true
			tc.change(state)
			if err := (&workflow.StateStore{Dir: opts.StateDir}).Save(state); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			before, _ := os.ReadFile(path)
			opts.Resume, opts.Vars = true, nil
			resumed := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			if result, err := resumed.Run(context.Background()); err == nil || result.Success || len(sent) != 2 {
				t.Fatalf("invalid checkpoint resumed: %+v %v sends=%d", result, err, len(sent))
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("resume rejection cleared or overwrote saved state")
			}
		})
	}
}

func TestWorkflowResumeRejectsVariableChangesAndMissingCheckpoint(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	var sent []string
	opts.Resume = true
	if _, err := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent)).Run(context.Background()); err == nil || len(sent) != 0 {
		t.Fatal("missing checkpoint resumed as a fresh run")
	}
	opts.Resume = false
	if _, err := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts.Resume = true
	opts.Vars = map[string]string{"feature": "different task"}
	if _, err := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent)).Run(context.Background()); err == nil || len(sent) != 2 {
		t.Fatal("resume changed the task or resent prompts")
	}
	opts.Restart = true
	if _, err := newWorkflowRunner(resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent)); err == nil {
		t.Fatal("accepted resume and restart together")
	}
}

func TestWorkflowCheckpointWriteFailureStopsFurtherDispatch(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	var sent []string
	ports := resumeWorkflowPorts(&sent)
	baseDispatch := ports.dispatch
	blocked := filepath.Join(opts.ProjectRoot, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var runner *workflowRunner
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		_ = baseDispatch(ctx, session, pane, prompt)
		// Make receipt persistence fail after a real successful delivery.
		runner.store.Dir = blocked
		return nil
	}
	runner = mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, ports)
	result, err := runner.Run(context.Background())
	if err == nil || result.Success || len(sent) != 1 {
		t.Fatalf("continued after checkpoint failure: %+v %v sends=%d", result, err, len(sent))
	}
	if state := mustLoadWorkflowCheckpoint(t, opts); state.Dispatches[0].Status != "sending" {
		t.Fatalf("lost receipt became a false success: %+v", state.Dispatches)
	}
}

func TestWorkflowRunLeaseExcludesASecondRunner(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var firstSent, secondSent []string
	ports := resumeWorkflowPorts(&firstSent)
	baseDispatch := ports.dispatch
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		if pane == "%1" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return baseDispatch(ctx, session, pane, prompt)
	}
	first := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, ports)
	done := make(chan error, 1)
	go func() { _, err := first.Run(context.Background()); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first runner never entered dispatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	second := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&secondSent))
	result, err := second.Run(ctx)
	if err == nil || result.Reason != "checkpoint-locked" || len(secondSent) != 0 {
		t.Fatalf("second runner escaped lease: %+v %v sends=%d", result, err, len(secondSent))
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowReconciledDeliveryResumesWithoutRestartingPriorStages(t *testing.T) {
	for _, outcome := range []string{"delivered", "not-sent"} {
		t.Run(outcome, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			var sent []string
			first := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			if _, err := first.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			store := &workflow.StateStore{Dir: opts.StateDir}
			state := mustLoadWorkflowCheckpoint(t, opts)
			state.Dispatches[0].Status = "sending" // The receipt was lost.
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			token, err := state.CheckpointToken()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := store.ReconcileDelivery(context.Background(), opts.Session, "%2", token, outcome)
			if err != nil || resolved.Dispatches[0].Resolution != outcome || resolved.Dispatches[0].ResolvedAt == nil {
				t.Fatalf("resolve = %+v, %v", resolved, err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			before, _ := os.ReadFile(path)
			if _, err := store.ReconcileDelivery(context.Background(), opts.Session, "%2", token, outcome); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("idempotent resolution rewrote checkpoint")
			}
			opts.Resume, opts.Vars = true, nil
			resumed := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			result, err := resumed.Run(context.Background())
			wantSends := 2
			if outcome == "not-sent" {
				wantSends++
			}
			if err != nil || !result.Completed || len(sent) != wantSends || !reflect.DeepEqual(result.Stages, []string{"review", "done"}) {
				t.Fatalf("resume after %s: %+v %v sends=%d", outcome, result, err, len(sent))
			}
			for _, prompt := range sent[2:] {
				if !strings.HasPrefix(prompt, "%2:") {
					t.Fatal("recovery replayed a prior stage")
				}
			}
		})
	}
}

func TestWorkflowResumeRetainsFreshReviewVotesWithoutRepeatingPrompts(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.MaxTransitions = 5
	opts.PanePIDs["%3"] = 1003
	template := reviewGateTemplate("all")
	template.Flow.Initial = "review"
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}}
	fake := newFakeWorkflowSession()
	fake.onDispatch = func(pane, _ string) {
		if pane == "%2" {
			fake.say(pane, "SHIP-VERDICT\nretained progress anchor")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := fake.ports()
	polls := 0
	ports.sleep = func(context.Context, time.Duration) {
		polls++
		if polls == 2 {
			// Ordinary scrollback rollover retains an overlap but no verdict.
			fake.mu.Lock()
			fake.outputs["%2"] = "retained progress anchor\n" + strings.Repeat("work in progress ", 5000)
			fake.mu.Unlock()
		}
		if polls == 3 {
			cancel()
		}
	}
	first := mustResumeWorkflowRunner(t, template, agents, opts, ports)
	if result, err := first.Run(ctx); !errors.Is(err, context.Canceled) || result.Transitions != 0 {
		t.Fatalf("initial partial review = %+v, %v", result, err)
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if prior.Evidence == nil || len(prior.Evidence.Matches[1]) != 1 || strings.Contains(prior.Evidence.Panes["%2"].Capture, "SHIP-VERDICT") || strings.Contains(prior.Evidence.Panes["%2"].Fresh, "SHIP-VERDICT") {
		t.Fatalf("durable approval did not survive its text scrolling away: %+v", prior.Evidence)
	}
	if err := (&workflow.StateStore{Dir: opts.StateDir}).Pause(prior, "operator pause", time.Now()); err != nil {
		t.Fatal(err)
	}
	fake.say("%3", "SHIP-VERDICT") // The second reviewer finishes while paused.
	opts.Resume, opts.Vars = true, nil
	resumePorts := fake.ports()
	resumePorts.sleep = func(context.Context, time.Duration) {}
	resumed := mustResumeWorkflowRunner(t, template, agents, opts, resumePorts)
	result, err := resumed.Run(context.Background())
	if err != nil || !result.Completed || !result.Resumed || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("resume lost fresh votes or repeated prompts: %+v, %v, sends=%v", result, err, fake.dispatchedPanes())
	}
}

func TestWorkflowResumeRefusesMissingVerdictBoundaryWithoutChangingCheckpoint(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	template := reviewGateTemplate("all")
	template.Flow.Initial = "review"
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}}
	opts.PanePIDs["%3"] = 1003
	fake := newFakeWorkflowSession()
	ctx, cancel := context.WithCancel(context.Background())
	ports := fake.ports()
	ports.sleep = func(context.Context, time.Duration) { cancel() }
	first := mustResumeWorkflowRunner(t, template, agents, opts, ports)
	if _, err := first.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	prior.Evidence = nil
	store := &workflow.StateStore{Dir: opts.StateDir}
	if err := store.Save(prior); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.StateDir, opts.Session+".json")
	before, _ := os.ReadFile(path)
	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, template, agents, opts, fake.ports())
	result, err := resumed.Run(context.Background())
	after, _ := os.ReadFile(path)
	if err == nil || result.Reason != "resume-rejected" || !strings.Contains(err.Error(), "output boundary") || string(before) != string(after) || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("missing boundary was trusted or changed: %+v, %v, sends=%v", result, err, fake.dispatchedPanes())
	}
}

func TestWorkflowResumePendingRecipientExcludesItsEarlierTaskVerdict(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.MaxTransitions = 5
	opts.PanePIDs["%3"] = 1003
	template := reviewGateTemplate("all")
	template.Flow.Initial = "review"
	agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "author"}, {ID: "%2", Role: "reviewer"}, {ID: "%3", Role: "reviewer"}}
	fake := newFakeWorkflowSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onDispatch = func(pane, _ string) {
		if pane == "%2" {
			fake.say(pane, "SHIP-VERDICT")
			cancel() // The first prompt arrived; the second has not been sent.
		}
	}
	first := mustResumeWorkflowRunner(t, template, agents, opts, fake.ports())
	if _, err := first.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fake.say("%3", "SHIP-VERDICT") // Completion of an earlier, unrelated task.
	resumeCtx, resumeCancel := context.WithCancel(context.Background())
	defer resumeCancel()
	ports := fake.ports()
	polls := 0
	ports.sleep = func(context.Context, time.Duration) {
		polls++
		if polls == 3 {
			resumeCancel()
		}
	}
	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, template, agents, opts, ports)
	result, err := resumed.Run(resumeCtx)
	if !errors.Is(err, context.Canceled) || result.Completed || result.Transitions != 0 || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("pending recipient's old task satisfied new review: %+v, %v sends=%v", result, err, fake.dispatchedPanes())
	}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if len(prior.Evidence.Matches[1]) != 1 || prior.Evidence.Matches[1][0] != "%2" {
		t.Fatalf("wrong reviewer receipt retained: %+v", prior.Evidence.Matches)
	}
	fake.say("%3", "SHIP-VERDICT") // The prompted review now actually finishes.
	ports = fake.ports()
	ports.sleep = func(context.Context, time.Duration) {}
	finished := mustResumeWorkflowRunner(t, template, agents, opts, ports)
	result, err = finished.Run(context.Background())
	if err != nil || !result.Completed || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("confirmed recipients were repeated or fresh review lost: %+v, %v sends=%v", result, err, fake.dispatchedPanes())
	}
}
