package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

type workflowManualStatusResponse struct {
	Success      bool   `json:"success"`
	CurrentStage string `json:"current_stage"`
	Paused       bool   `json:"paused"`
	Completed    bool   `json:"completed"`
	ManualGate   *struct {
		Token       string `json:"token"`
		Stage       string `json:"stage"`
		Transitions []struct {
			Index int    `json:"index"`
			Label string `json:"label"`
			To    string `json:"to"`
		} `json:"transitions"`
		Decision *workflowManualDecisionResponse `json:"decision"`
	} `json:"manual_gate"`
}

type workflowManualDecisionResponse struct {
	Success     bool   `json:"success"`
	Token       string `json:"token"`
	Transition  int    `json:"transition"`
	Reason      string `json:"reason"`
	RequestedAt string `json:"requested_at"`
	Replayed    bool   `json:"replayed"`
}

// Run the actual Cobra status/trigger surface while the real workflow runner
// owns its session lock. The fake ports replace pane transport only; stage
// evaluation, delivery checkpoints, manual receipts and transitions are real.
func TestWorkflowManualCommandSelectsExactTransition(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual, opts.MaxTransitions = false, 8
	template := &workflow.WorkflowTemplate{
		Name: "manual-choice", Coordination: workflow.CoordPipeline,
		Agents: []workflow.WorkflowAgent{{Profile: "build", Role: "build"}, {Profile: "review", Role: "review"}},
		Flow: &workflow.FlowConfig{Initial: "build", Stages: []string{"build", "review", "wrong", "done"}, Transitions: []workflow.Transition{
			{From: "build", To: "wrong", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "Proceed"}},
			{From: "build", To: "review", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "Proceed"}},
			{From: "review", To: "done", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "Finish"}},
			{From: "review", To: "wrong", Trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "APPROVED", Role: "review"}},
		}},
	}
	fake := newFakeWorkflowSession()
	run := startWorkflowManualRunner(t, template, opts, fake)
	run.nextPoll(t)
	initial := workflowManualStatus(t, opts)
	if initial.ManualGate == nil || initial.ManualGate.Stage != "build" || len(initial.ManualGate.Transitions) != 2 {
		t.Fatalf("initial manual controls = %+v", initial)
	}
	for i, choice := range initial.ManualGate.Transitions {
		if choice.Index != i || choice.Label != "Proceed" {
			t.Fatalf("duplicate labels lost their exact edge identity: %+v", initial.ManualGate.Transitions)
		}
	}
	if initial.ManualGate.Transitions[1].To != "review" {
		t.Fatalf("second choice destination = %q", initial.ManualGate.Transitions[1].To)
	}
	// Polling output evidence must not invalidate a token a human is reading.
	run.continuePoll(t)
	run.nextPoll(t)
	if current := workflowManualStatus(t, opts); current.ManualGate == nil || current.ManualGate.Token != initial.ManualGate.Token {
		t.Fatalf("ordinary observation changed the manual gate: %+v", current)
	}

	args := workflowManualTriggerArgs(opts, initial.ManualGate.Token, 1, "Choose the review route")
	accepted := workflowManualTrigger(t, args)
	if accepted.Transition != 1 || accepted.Token != initial.ManualGate.Token || accepted.Reason != "Choose the review route" || accepted.RequestedAt == "" || accepted.Replayed {
		t.Fatalf("accepted decision = %+v", accepted)
	}
	repeated := workflowManualTrigger(t, args)
	if !repeated.Replayed || repeated.RequestedAt != accepted.RequestedAt || repeated.Transition != accepted.Transition {
		t.Fatalf("identical decision was not idempotent: first=%+v repeated=%+v", accepted, repeated)
	}
	for _, conflict := range [][]string{
		workflowManualTriggerArgs(opts, initial.ManualGate.Token, 0, accepted.Reason),
		workflowManualTriggerArgs(opts, initial.ManualGate.Token, 1, "A conflicting explanation"),
	} {
		if output, err := executeWorkflowManualCommand(t, conflict); err == nil {
			t.Fatalf("conflicting decision accepted: %s", output)
		}
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatalf("request command dispatched work before the runner consumed it: %v", got)
	}
	run.continuePoll(t)
	run.nextPoll(t)
	review := workflowManualStatus(t, opts)
	if review.CurrentStage != "review" || review.ManualGate == nil || review.ManualGate.Token == initial.ManualGate.Token || len(review.ManualGate.Transitions) != 1 || review.ManualGate.Transitions[0].Index != 2 {
		t.Fatalf("selected edge did not expose only the next manual gate: %+v", review)
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1", "%2"}) {
		t.Fatalf("wrong duplicate-label edge or repeated prompt: %v", got)
	}
	// An old response may be retried after advancement, but that receipt is
	// observational and cannot authorize the next stage's decision.
	if replay := workflowManualTrigger(t, args); !replay.Replayed || replay.RequestedAt != accepted.RequestedAt {
		t.Fatalf("past-stage decision retry changed its receipt: %+v", replay)
	}
	if output, err := executeWorkflowManualCommand(t, workflowManualTriggerArgs(opts, review.ManualGate.Token, 3, "Try the agent verdict edge")); err == nil {
		t.Fatalf("manual command bypassed an agent_says transition: %s", output)
	}
	run.continuePoll(t)
	run.nextPoll(t)
	if current := workflowManualStatus(t, opts); current.CurrentStage != "review" || current.ManualGate == nil || current.ManualGate.Token != review.ManualGate.Token {
		t.Fatalf("a previous gate or rejected nonmanual edge advanced the workflow: %+v", current)
	}
	workflowManualTrigger(t, workflowManualTriggerArgs(opts, review.ManualGate.Token, 2, "Finish this reviewed stage"))
	run.continuePoll(t)
	result, err := run.finish(t)
	if err != nil || !result.Completed || !reflect.DeepEqual(result.Stages, []string{"build", "review", "done"}) || result.Transitions != 2 {
		t.Fatalf("manual run = %+v, %v", result, err)
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1", "%2"}) {
		t.Fatalf("terminal approval sent extra work: %v", got)
	}
	if final := workflowManualStatus(t, opts); !final.Completed || final.ManualGate != nil {
		t.Fatalf("completed workflow still advertises a manual gate: %+v", final)
	}
}

func TestWorkflowManualCommandQueuedWhilePausedResumesWithoutResend(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual = false
	fake := newFakeWorkflowSession()
	first := startWorkflowManualRunner(t, resumeWorkflowTemplate(), opts, fake)
	first.nextPoll(t)
	initial := workflowManualStatus(t, opts)
	if initial.ManualGate == nil || len(initial.ManualGate.Transitions) != 1 || initial.ManualGate.Transitions[0].Index != 0 {
		t.Fatalf("first stage did not advertise edge zero: %+v", initial)
	}
	first.cancel()
	if result, err := first.finish(t); !errors.Is(err, context.Canceled) || result.Completed {
		t.Fatalf("interrupted run = %+v, %v", result, err)
	}
	store := &workflow.StateStore{Dir: opts.StateDir}
	prior := mustLoadWorkflowCheckpoint(t, opts)
	if err := store.Pause(prior, "waiting for operator review", time.Now()); err != nil {
		t.Fatal(err)
	}
	paused := workflowManualStatus(t, opts)
	if !paused.Paused || paused.ManualGate == nil || paused.ManualGate.Token != initial.ManualGate.Token {
		t.Fatalf("pause discarded the completed stage's manual gate: %+v", paused)
	}
	checkpointPath := filepath.Join(opts.StateDir, opts.Session+".json")
	checkpointBefore, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name string
		args []string
	}{
		{"missing exact index", []string{"trigger", "--session", opts.Session, "--project-root", opts.ProjectRoot, "--token", paused.ManualGate.Token, "--reason", "Checked"}},
		{"negative index", workflowManualTriggerArgs(opts, paused.ManualGate.Token, -1, "Checked")},
		{"unknown edge", workflowManualTriggerArgs(opts, paused.ManualGate.Token, 99, "Checked")},
		{"wrong gate", workflowManualTriggerArgs(opts, strings.Repeat("0", 64), 0, "Checked")},
		{"empty reason", workflowManualTriggerArgs(opts, paused.ManualGate.Token, 0, " \t ")},
	}
	for _, tc := range invalid {
		if output, err := executeWorkflowManualCommand(t, tc.args); err == nil {
			t.Fatalf("%s accepted: %s", tc.name, output)
		}
		if snapshot := workflowManualStatus(t, opts); snapshot.ManualGate == nil || snapshot.ManualGate.Decision != nil {
			t.Fatalf("%s recorded a decision: %+v", tc.name, snapshot)
		}
	}
	// Explicit zero is a valid global transition index, distinct from an
	// omitted --transition flag. Queue the approval with no process running.
	args := workflowManualTriggerArgs(opts, paused.ManualGate.Token, 0, "Build inspected while the runner was stopped")
	decision := workflowManualTrigger(t, args)
	if decision.Transition != 0 || decision.Replayed {
		t.Fatalf("first edge zero decision = %+v", decision)
	}
	if repeated := workflowManualTrigger(t, args); !repeated.Replayed || repeated.RequestedAt != decision.RequestedAt {
		t.Fatalf("offline decision retry = %+v, original=%+v", repeated, decision)
	}
	checkpointAfter, err := os.ReadFile(checkpointPath)
	if err != nil || !bytes.Equal(checkpointBefore, checkpointAfter) {
		t.Fatalf("request command rewrote the runner checkpoint: %v", err)
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatalf("offline approval sent a prompt: %v", got)
	}
	queued := workflowManualStatus(t, opts)
	if queued.ManualGate == nil || queued.ManualGate.Decision == nil || queued.ManualGate.Decision.Reason != decision.Reason {
		t.Fatalf("status omitted the queued offline decision: %+v", queued)
	}

	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, fake.ports())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := resumed.Run(ctx)
	if err != nil || !result.Resumed || result.Reason != "max-transitions" || !reflect.DeepEqual(result.Stages, []string{"build", "review"}) {
		t.Fatalf("resume with offline approval = %+v, %v", result, err)
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1", "%2"}) {
		t.Fatalf("resume replayed completed work or omitted the next stage: %v", got)
	}
	advanced := mustLoadWorkflowCheckpoint(t, opts)
	if advanced.Paused || advanced.CurrentStage != "review" || len(advanced.StageHistory) != 1 || advanced.StageHistory[0].ManualDecision == nil || advanced.StageHistory[0].ManualDecision.Token != decision.Token || advanced.StageHistory[0].ManualDecision.Reason != decision.Reason {
		t.Fatalf("resumed checkpoint lost the durable operator decision: %+v", advanced)
	}
	second := workflowManualStatus(t, opts)
	if second.ManualGate == nil || second.ManualGate.Token == decision.Token || second.ManualGate.Decision != nil {
		t.Fatalf("offline approval spread to the next gate: %+v", second)
	}
	workflowManualTrigger(t, workflowManualTriggerArgs(opts, second.ManualGate.Token, 1, "Review inspected separately"))
	opts.MaxTransitions = 8
	final := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, fake.ports())
	result, err = final.Run(ctx)
	if err != nil || !result.Resumed || !result.Completed || len(fake.dispatchedPanes()) != 2 {
		t.Fatalf("final approved resume = %+v, %v, sends=%v", result, err, fake.dispatchedPanes())
	}
}

func TestWorkflowManualDecisionCannotCrossRetryOrRestart(t *testing.T) {
	for _, change := range []string{"retry stage", "restart workflow"} {
		t.Run(change, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.MaxTransitions = false, 8
			fake := newFakeWorkflowSession()
			run := startWorkflowManualRunner(t, resumeWorkflowTemplate(), opts, fake)
			run.nextPoll(t)
			initial := workflowManualStatus(t, opts)
			if initial.ManualGate == nil {
				t.Fatal("initial manual gate missing")
			}
			oldArgs := workflowManualTriggerArgs(opts, initial.ManualGate.Token, 0, "Decision about the original build")
			oldDecision := workflowManualTrigger(t, oldArgs)
			if change == "retry stage" {
				if err := (workflowRunActions{r: run.runner}).RetryStage(t.Context()); err != nil {
					t.Fatalf("retry stage: %v", err)
				}
			} else {
				run.cancel()
				if _, err := run.finish(t); !errors.Is(err, context.Canceled) {
					t.Fatalf("stop before restart: %v", err)
				}
				opts.Restart = true
				run = startWorkflowManualRunner(t, resumeWorkflowTemplate(), opts, fake)
				run.nextPoll(t)
			}
			fresh := workflowManualStatus(t, opts)
			if fresh.CurrentStage != "build" || fresh.ManualGate == nil || fresh.ManualGate.Token == oldDecision.Token || fresh.ManualGate.Decision != nil {
				t.Fatalf("%s retained the original visit's approval: %+v", change, fresh)
			}
			if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1", "%1"}) {
				t.Fatalf("%s did not start a new build delivery: %v", change, got)
			}
			if replay := workflowManualTrigger(t, oldArgs); !replay.Replayed || replay.RequestedAt != oldDecision.RequestedAt {
				t.Fatalf("old receipt did not remain observational: %+v", replay)
			}
			run.continuePoll(t)
			run.nextPoll(t)
			if current := workflowManualStatus(t, opts); current.CurrentStage != "build" || current.ManualGate == nil || current.ManualGate.Token != fresh.ManualGate.Token {
				t.Fatalf("%s consumed a decision from an earlier visit: %+v", change, current)
			}
			workflowManualTrigger(t, workflowManualTriggerArgs(opts, fresh.ManualGate.Token, 0, "The replacement build is now checked"))
			run.continuePoll(t)
			run.nextPoll(t)
			if current := workflowManualStatus(t, opts); current.CurrentStage != "review" || current.ManualGate == nil || current.ManualGate.Decision != nil {
				t.Fatalf("new approval failed to advance exactly one stage: %+v", current)
			}
			if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1", "%1", "%2"}) {
				t.Fatalf("%s dispatch history = %v", change, got)
			}
			run.cancel()
			if _, err := run.finish(t); !errors.Is(err, context.Canceled) {
				t.Fatalf("stop final runner: %v", err)
			}
		})
	}
}

func TestWorkflowManualCorruptDecisionStopsBeforeDispatch(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual, opts.MaxTransitions = false, 8
	fake := newFakeWorkflowSession()
	run := startWorkflowManualRunner(t, resumeWorkflowTemplate(), opts, fake)
	run.nextPoll(t)
	status := workflowManualStatus(t, opts)
	if status.ManualGate == nil {
		t.Fatal("initial manual gate missing")
	}
	args := workflowManualTriggerArgs(opts, status.ManualGate.Token, 0, "Build checked")
	workflowManualTrigger(t, args)
	receiptPath := filepath.Join(opts.StateDir, "manual-decisions", opts.Session, status.ManualGate.Token+".json")
	if _, err := os.ReadFile(receiptPath); err != nil {
		t.Fatalf("accepted manual decision has no durable receipt: %v", err)
	}
	if err := os.WriteFile(receiptPath, []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	run.continuePoll(t)
	result, err := run.finish(t)
	if err == nil || result.Completed || result.Transitions != 0 {
		t.Fatalf("corrupted decision advanced the workflow: %+v, %v", result, err)
	}
	if got := fake.dispatchedPanes(); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatalf("corrupted decision dispatched a destination prompt: %v", got)
	}
	if state := mustLoadWorkflowCheckpoint(t, opts); state.CurrentStage != "build" || len(state.StageHistory) != 0 {
		t.Fatalf("corrupted decision was committed as a transition: %+v", state)
	}
	if output, err := executeWorkflowManualCommand(t, args); err == nil {
		t.Fatalf("retry silently replaced a corrupted decision: %s", output)
	}
	if output, err := executeWorkflowManualCommand(t, []string{"status", "--session", opts.Session, "--project-root", opts.ProjectRoot}); err == nil {
		t.Fatalf("status concealed the unreadable decision: %s", output)
	}
}

func TestWorkflowManualDecisionDoesNotRequireNewScrollback(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run("pane_replaced="+strconv.FormatBool(replaced), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.MaxTransitions = false, 8
			template := resumeWorkflowTemplate()
			template.Flow.Transitions = []workflow.Transition{
				{From: "build", To: "done", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "Build independently reviewed"}},
			}
			fake := newFakeWorkflowSession()
			run := startWorkflowManualRunner(t, template, opts, fake)
			run.nextPoll(t)
			status := workflowManualStatus(t, opts)
			if status.ManualGate == nil {
				t.Fatal("initial manual gate missing")
			}
			// The runner is parked at the poll barrier after delivery. Its
			// next capture would fail, but the operator's decision is based on
			// independent review and still requires the original pane identity.
			captures, validations := 0, 0
			run.runner.ports.capture = func(context.Context, string, int) (string, error) {
				captures++
				return "", errors.New("pane scrollback is unavailable")
			}
			run.runner.ports.validate = func(context.Context) error {
				validations++
				if replaced {
					return errors.New("pane shell PID changed")
				}
				return nil
			}
			workflowManualTrigger(t, workflowManualTriggerArgs(opts, status.ManualGate.Token, 0, "Reviewed the output independently"))
			run.continuePoll(t)
			result, err := run.finish(t)
			if replaced {
				if err == nil || result.Completed || result.Transitions != 0 || !strings.Contains(err.Error(), "identity changed") {
					t.Fatalf("manual approval ignored changed pane identity: %+v, %v", result, err)
				}
			} else if err != nil || !result.Completed || result.Transitions != 1 || !reflect.DeepEqual(result.Stages, []string{"build", "done"}) {
				t.Fatalf("manual approval incorrectly required new scrollback: %+v, %v", result, err)
			}
			if captures != 0 || validations == 0 || !reflect.DeepEqual(fake.dispatchedPanes(), []string{"%1"}) {
				t.Fatalf("approval boundary: captures=%d identity_checks=%d sends=%v", captures, validations, fake.dispatchedPanes())
			}
		})
	}
}

func executeWorkflowManualCommand(t *testing.T, args []string) ([]byte, error) {
	t.Helper()
	previousJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = previousJSON }()
	var output bytes.Buffer
	cmd := newWorkflowsCmd()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return append([]byte(nil), output.Bytes()...), err
}

func workflowManualStatus(t *testing.T, opts workflowRunOptions) workflowManualStatusResponse {
	t.Helper()
	output, err := executeWorkflowManualCommand(t, []string{"status", "--session", opts.Session, "--project-root", opts.ProjectRoot})
	if err != nil {
		t.Fatalf("workflow status: %v: %s", err, output)
	}
	var status workflowManualStatusResponse
	if err := json.Unmarshal(output, &status); err != nil || !status.Success {
		t.Fatalf("workflow status output = %s: %v", output, err)
	}
	return status
}

func workflowManualTriggerArgs(opts workflowRunOptions, token string, transition int, reason string) []string {
	return []string{"trigger", "--session", opts.Session, "--project-root", opts.ProjectRoot,
		"--token", token, "--transition", strconv.Itoa(transition), "--reason", reason}
}

func workflowManualTrigger(t *testing.T, args []string) workflowManualDecisionResponse {
	t.Helper()
	output, err := executeWorkflowManualCommand(t, args)
	if err != nil {
		t.Fatalf("workflow trigger: %v: %s", err, output)
	}
	var decision workflowManualDecisionResponse
	if err := json.Unmarshal(output, &decision); err != nil || !decision.Success {
		t.Fatalf("workflow trigger output = %s: %v", output, err)
	}
	return decision
}

type workflowManualRunOutcome struct {
	result WorkflowRunResult
	err    error
}

type workflowManualRunning struct {
	runner   *workflowRunner
	polls    chan struct{}
	resume   chan struct{}
	done     chan workflowManualRunOutcome
	cancel   context.CancelFunc
	finished bool
	outcome  workflowManualRunOutcome
}

func startWorkflowManualRunner(t *testing.T, template *workflow.WorkflowTemplate, opts workflowRunOptions, fake *fakeWorkflowSession) *workflowManualRunning {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	run := &workflowManualRunning{polls: make(chan struct{}), resume: make(chan struct{}), done: make(chan workflowManualRunOutcome, 1), cancel: cancel}
	ports := fake.ports()
	ports.sleep = func(ctx context.Context, _ time.Duration) {
		select {
		case run.polls <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-run.resume:
		case <-ctx.Done():
		}
	}
	run.runner = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	go func() {
		result, err := run.runner.Run(ctx)
		run.done <- workflowManualRunOutcome{result, err}
	}()
	t.Cleanup(func() {
		cancel()
		if !run.finished {
			select {
			case run.outcome = <-run.done:
				run.finished = true
			case <-time.After(2 * time.Second):
				t.Error("manual workflow runner did not stop after cancellation")
			}
		}
	})
	return run
}

func (r *workflowManualRunning) nextPoll(t *testing.T) {
	t.Helper()
	select {
	case <-r.polls:
	case r.outcome = <-r.done:
		r.finished = true
		t.Fatalf("workflow exited before the next operator decision: %+v, %v", r.outcome.result, r.outcome.err)
	case <-time.After(2 * time.Second):
		t.Fatal("workflow did not reach the next operator decision")
	}
}

func (r *workflowManualRunning) continuePoll(t *testing.T) {
	t.Helper()
	select {
	case r.resume <- struct{}{}:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow did not accept the next observation tick")
	}
}

func (r *workflowManualRunning) finish(t *testing.T) (WorkflowRunResult, error) {
	t.Helper()
	if !r.finished {
		select {
		case r.outcome = <-r.done:
			r.finished = true
		case <-time.After(2 * time.Second):
			t.Fatal("workflow did not finish after the final operator decision")
		}
	}
	return r.outcome.result, r.outcome.err
}
