package cli

import (
	"context"
	"encoding/json"
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

func fileResumeWorkflowTemplate(kind workflow.TriggerType) *workflow.WorkflowTemplate {
	template := resumeWorkflowTemplate()
	template.Flow.Stages = []string{"build", "done"}
	template.Flow.Transitions = []workflow.Transition{{
		From: "build", To: "done", Trigger: workflow.Trigger{Type: kind, Pattern: "artifacts/**/*.ready"},
	}}
	return template
}

// Collect through the production observation/checkpoint path without asking
// the coordinator to advance yet, so tests can stop at the durable boundary.
func (r *workflowRunner) checkpointFileTriggers() error {
	_, err := r.observe(context.Background())
	return err
}

func stopFileWorkflowAfterDispatch(t *testing.T, template *workflow.WorkflowTemplate, opts workflowRunOptions, sent *[]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ports := resumeWorkflowPorts(sent)
	dispatch := ports.dispatch
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		checkpoint := mustLoadWorkflowCheckpoint(t, opts)
		if checkpoint.Files == nil || checkpoint.Files.Round != checkpoint.Turn || checkpoint.Files.Triggers == nil {
			t.Fatal("file-trigger prompt dispatched without its durable baseline")
		}
		if err := dispatch(ctx, session, pane, prompt); err != nil {
			return err
		}
		cancel()
		return nil
	}
	runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	result, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) || result.Reason != "canceled" || len(*sent) != 1 {
		t.Fatalf("stop after delivery = %+v, %v, sends=%d", result, err, len(*sent))
	}
}

func TestWorkflowResumeObservesFilesChangedWhileStopped(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kind             workflow.TriggerType
		preexisting      bool
		preserveMetadata bool
	}{
		{name: "created in a new nested directory", kind: workflow.TriggerFileCreated},
		{name: "modified existing file", kind: workflow.TriggerFileModified, preexisting: true},
		{name: "same size and restored timestamp", kind: workflow.TriggerFileModified, preexisting: true, preserveMetadata: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.MaxTransitions = false, 8
			template := fileResumeWorkflowTemplate(tc.kind)
			path := filepath.Join(opts.ProjectRoot, "artifacts", "nested", "result.ready")
			var previous os.FileInfo
			if tc.preexisting {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				previous, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			var sent []string
			stopFileWorkflowAfterDispatch(t, template, opts, &sent)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.preserveMetadata {
				if err := os.Chtimes(path, previous.ModTime(), previous.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			opts.Resume, opts.Vars = true, nil
			runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			result, err := runner.Run(ctx)
			if err != nil || !result.Completed || result.Transitions != 1 || len(sent) != 1 {
				t.Fatalf("offline file completion = %+v, %v, sends=%d", result, err, len(sent))
			}
			checkpoint := mustLoadWorkflowCheckpoint(t, opts)
			if !checkpoint.Completed || len(checkpoint.StageHistory) != 1 || checkpoint.Files != nil {
				t.Fatalf("file completion did not commit exactly one destination: %+v", checkpoint)
			}
		})
	}
}

func TestWorkflowResumeUnchangedFilesDoNotCompleteStage(t *testing.T) {
	for _, kind := range []workflow.TriggerType{workflow.TriggerFileCreated, workflow.TriggerFileModified} {
		t.Run(string(kind), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual = false
			template := fileResumeWorkflowTemplate(kind)
			if err := os.MkdirAll(filepath.Join(opts.ProjectRoot, "artifacts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(opts.ProjectRoot, "artifacts", "old.ready"), []byte("old task"), 0o600); err != nil {
				t.Fatal(err)
			}
			var sent []string
			stopFileWorkflowAfterDispatch(t, template, opts, &sent)
			opts.Resume, opts.Vars = true, nil
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			polls := 0
			ports.sleep = func(context.Context, time.Duration) {
				polls++
				if polls == 3 {
					cancel()
				}
			}
			runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			result, err := runner.Run(ctx)
			if !errors.Is(err, context.Canceled) || result.Transitions != 0 || result.Completed || len(sent) != 1 {
				t.Fatalf("preexisting file approved new work: %+v, %v, sends=%d", result, err, len(sent))
			}
		})
	}
}

func TestWorkflowResumeRetainsObservedFileAfterRemoval(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual = false
	template := fileResumeWorkflowTemplate(workflow.TriggerFileCreated)
	var sent []string
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ports := resumeWorkflowPorts(&sent)
	var runner *workflowRunner
	ports.sleep = func(context.Context, time.Duration) {
		path := filepath.Join(opts.ProjectRoot, "artifacts", "result.ready")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("finished"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runner.checkpointFileTriggers(); err != nil {
			t.Fatal(err)
		}
		if state := mustLoadWorkflowCheckpoint(t, opts); !state.Files.Triggers[0].Fired {
			t.Fatal("file completion was not durable before removal")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	runner = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	if result, err := runner.Run(ctx); !errors.Is(err, context.Canceled) || result.Transitions != 0 {
		t.Fatalf("stopped run = %+v, %v", result, err)
	}
	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	resumeCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if result, err := resumed.Run(resumeCtx); err != nil || !result.Completed || len(sent) != 1 {
		t.Fatalf("lost durable file completion: %+v, %v, sends=%d", result, err, len(sent))
	}
}

func TestWorkflowRetryStartsNewFilesystemBoundary(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual = false
	template := fileResumeWorkflowTemplate(workflow.TriggerFileCreated)
	var sent []string
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ports := resumeWorkflowPorts(&sent)
	var runner *workflowRunner
	polls := 0
	ports.sleep = func(context.Context, time.Duration) {
		polls++
		if polls != 1 {
			cancel()
			return
		}
		path := filepath.Join(opts.ProjectRoot, "artifacts", "old.ready")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("previous attempt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runner.checkpointFileTriggers(); err != nil {
			t.Fatal(err)
		}
		if err := (workflowRunActions{r: runner}).RetryStage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runner = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	result, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) || result.Completed || result.Transitions != 0 || len(sent) != 2 {
		t.Fatalf("retry inherited old file completion: %+v, %v, sends=%d", result, err, len(sent))
	}
	checkpoint := mustLoadWorkflowCheckpoint(t, opts)
	if checkpoint.Files == nil || checkpoint.Files.Round != 2 || checkpoint.Files.Triggers[0].Fired {
		t.Fatalf("retry retained old file evidence: %+v", checkpoint.Files)
	}
}

func TestWorkflowResumeRejectsMissingOrStaleFilesystemEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*workflow.WorkflowState)
	}{
		{"missing", func(s *workflow.WorkflowState) { s.Files = nil }},
		{"old round", func(s *workflow.WorkflowState) { s.Files.Round++ }},
		{"old visit", func(s *workflow.WorkflowState) { s.Files.StartedAt = s.Files.StartedAt.Add(-time.Second) }},
		{"wrong stage", func(s *workflow.WorkflowState) { s.Files.Stage = "review" }},
		{"missing transition", func(s *workflow.WorkflowState) { s.Files.Triggers = map[int]workflow.FileTriggerState{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual = false
			template := fileResumeWorkflowTemplate(workflow.TriggerFileCreated)
			var sent []string
			stopFileWorkflowAfterDispatch(t, template, opts, &sent)
			state := mustLoadWorkflowCheckpoint(t, opts)
			tc.change(state)
			if err := (&workflow.StateStore{Dir: opts.StateDir}).Save(state); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			opts.Resume, opts.Vars = true, nil
			runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			result, err := runner.Run(t.Context())
			if err == nil || result.Success || result.Reason != "resume-rejected" || len(sent) != 1 {
				t.Fatalf("invalid file evidence resumed: %+v, %v, sends=%d", result, err, len(sent))
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("rejected resume changed checkpoint: %v", err)
			}
		})
	}
}

func TestWorkflowFileTriggersIgnoreCheckpointWrites(t *testing.T) {
	for _, location := range []string{"default", "custom", "symlink alias"} {
		t.Run(location, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual = false
			if location != "default" {
				opts.StateDir = filepath.Join(opts.ProjectRoot, "runtime-checkpoints")
			}
			if location == "symlink alias" {
				if err := os.MkdirAll(opts.StateDir, 0o755); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(t.TempDir(), "checkpoint-alias")
				if err := os.Symlink(opts.StateDir, alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				opts.StateDir = alias
			}
			template := fileResumeWorkflowTemplate(workflow.TriggerFileCreated)
			template.Flow.Transitions[0].Trigger.Pattern = "**/*.json"
			var sent []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			polls := 0
			ports.sleep = func(context.Context, time.Duration) {
				polls++
				if polls == 3 {
					cancel()
				}
			}
			runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			result, err := runner.Run(ctx)
			if !errors.Is(err, context.Canceled) || result.Transitions != 0 || result.Completed || len(sent) != 1 {
				t.Fatalf("checkpoint writes completed file work: %+v, %v, sends=%d", result, err, len(sent))
			}
			if err := os.WriteFile(filepath.Join(opts.ProjectRoot, "answer.json"), []byte(`{"done":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			opts.Resume, opts.Vars = true, nil
			resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			resumeCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			if result, err := resumed.Run(resumeCtx); err != nil || !result.Completed || len(sent) != 1 {
				t.Fatalf("checkpoint exclusion suppressed real file work: %+v, %v, sends=%d", result, err, len(sent))
			}
		})
	}
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

func TestWorkflowTimeoutRetriesRearmAndRemainBoundedAcrossResume(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual, opts.Interval = false, 5*time.Minute
	template := resumeWorkflowTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: workflow.ErrorActionRetry, MaxRetriesPerStage: 2}
	started := time.Now().UTC()
	now := started
	var sent []string
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ports := resumeWorkflowPorts(&sent)
	ports.now = func() time.Time { return now }
	baseDispatch := ports.dispatch
	ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		state := mustLoadWorkflowCheckpoint(t, opts)
		if state.Recovery.Retries["build"] != len(sent) || state.Timeout == nil || state.Timeout.Handling || state.Timeout.Handled ||
			state.Timeout.Round != state.Turn || state.Timeout.Round != len(sent)+1 || !state.Timeout.StartedAt.Equal(started) ||
			!state.Timeout.Deadline.Equal(now.Add(time.Minute)) {
			t.Fatalf("prompt preceded its fresh deadline or recovery debit: sent=%d state=%+v timeout=%+v", len(sent), state, state.Timeout)
		}
		if err := baseDispatch(ctx, session, pane, prompt); err != nil {
			return err
		}
		if len(sent) == 2 {
			cancel() // Interrupt after the first retry's successful delivery.
		}
		return nil
	}
	firstSleeps := 0
	ports.sleep = func(ctx context.Context, d time.Duration) {
		if ctx.Err() != nil {
			return
		}
		firstSleeps++
		if firstSleeps > 2 {
			t.Fatal("the initial timeout did not retry the stalled stage")
		}
		if d != time.Minute {
			t.Fatalf("poll ignored stage deadline: %s", d)
		}
		now = now.Add(d)
	}
	first := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	result, err := first.Run(ctx)
	if !errors.Is(err, context.Canceled) || result.Reason != "canceled" || len(sent) != 2 {
		t.Fatalf("initial timed retry = %+v, %v, sends=%d", result, err, len(sent))
	}
	state := mustLoadWorkflowCheckpoint(t, opts)
	if state.Timeout.Handled || state.Timeout.Handling || len(state.Errors) != 1 || state.Recovery.Retries["build"] != 1 {
		t.Fatalf("old timeout acknowledged the replacement round: %+v", state)
	}

	// Downtime consumes the existing round's deadline, but not another retry.
	now = now.Add(30 * time.Second)
	opts.Resume, opts.Vars = true, nil
	resumePorts := resumeWorkflowPorts(&sent)
	resumePorts.now = func() time.Time { return now }
	resumeDispatch := resumePorts.dispatch
	resumePorts.dispatch = func(ctx context.Context, session, pane, prompt string) error {
		state := mustLoadWorkflowCheckpoint(t, opts)
		if len(sent) != 2 || state.Recovery.Retries["build"] != 2 || state.Timeout.Round != 3 || state.Timeout.Handled ||
			!state.Timeout.Deadline.Equal(now.Add(time.Minute)) {
			t.Fatalf("resumed retry lost its bounded round: sends=%d state=%+v timeout=%+v", len(sent), state, state.Timeout)
		}
		return resumeDispatch(ctx, session, pane, prompt)
	}
	var sleeps []time.Duration
	resumePorts.sleep = func(_ context.Context, d time.Duration) {
		sleeps = append(sleeps, d)
		if len(sleeps) > 2 {
			t.Fatal("timeout retries did not exhaust their budget")
		}
		now = now.Add(d)
	}
	resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts)
	result, err = resumed.Run(t.Context())
	if err == nil || result.Reason != "aborted" || !result.Resumed || len(sent) != 3 ||
		!reflect.DeepEqual(sleeps, []time.Duration{30 * time.Second, time.Minute}) {
		t.Fatalf("bounded resumed retries = %+v, %v, sends=%d sleeps=%v", result, err, len(sent), sleeps)
	}
	state = mustLoadWorkflowCheckpoint(t, opts)
	if state.Recovery.Retries["build"] != 2 || len(state.Errors) != 3 || !state.Timeout.Handled || state.Timeout.StopReason != "aborted" {
		t.Fatalf("exhaustion did not preserve its durable receipt: %+v", state)
	}
	if result, err := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts).Run(t.Context()); err == nil || result.Reason != "resume-rejected" || len(sent) != 3 {
		t.Fatalf("resume revived an exhausted timeout policy: %+v, %v, sends=%d", result, err, len(sent))
	}
}

func TestWorkflowResumeRetainsTimeoutDeadlineAndAcknowledgesOnce(t *testing.T) {
	for _, action := range []workflow.ErrorAction{workflow.ErrorActionPause, workflow.ErrorActionNotify, ""} {
		t.Run("policy="+string(action), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.Interval = false, 5*time.Minute
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: action}
			started := time.Now().UTC()
			now := started
			var sent []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			ports.now = func() time.Time { return now }
			ports.sleep = func(context.Context, time.Duration) { now = now.Add(20 * time.Second); cancel() }
			first := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			if result, err := first.Run(ctx); !errors.Is(err, context.Canceled) || result.Reason != "canceled" || len(sent) != 1 {
				t.Fatalf("interrupted stage = %+v, %v, sends=%d", result, err, len(sent))
			}
			deadline := mustLoadWorkflowCheckpoint(t, opts).Timeout.Deadline
			if !deadline.Equal(started.Add(time.Minute)) {
				t.Fatalf("initial deadline = %s", deadline)
			}
			now = started.Add(45 * time.Second)
			opts.Resume, opts.Vars = true, nil
			resumeCtx, stop := context.WithCancel(t.Context())
			defer stop()
			resumePorts := resumeWorkflowPorts(&sent)
			resumePorts.now = func() time.Time { return now }
			sleeps := 0
			resumePorts.sleep = func(_ context.Context, d time.Duration) {
				sleeps++
				if sleeps == 1 {
					if d != 15*time.Second {
						t.Fatalf("resume granted a fresh timeout: sleep=%s", d)
					}
					now = now.Add(d)
				} else {
					stop() // Notify continues; pause stops before another poll.
				}
			}
			resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts)
			result, err := resumed.Run(resumeCtx)
			wantReason := "canceled"
			if action == workflow.ErrorActionPause {
				wantReason = "paused"
			}
			if err == nil || result.Reason != wantReason || len(sent) != 1 {
				t.Fatalf("remaining deadline = %+v, %v, sends=%d", result, err, len(sent))
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			if !state.Timeout.Deadline.Equal(deadline) || !state.Timeout.Handled || state.Timeout.Handling || len(state.Errors) != 1 {
				t.Fatalf("timeout receipt was lost: %+v", state)
			}
			// Resuming the handled round must neither pause/notify again nor
			// replace its elapsed deadline with another one.
			lastCtx, lastCancel := context.WithCancel(t.Context())
			defer lastCancel()
			lastPorts := resumeWorkflowPorts(&sent)
			lastPorts.now = func() time.Time { return now }
			lastPorts.sleep = func(_ context.Context, d time.Duration) {
				if d != opts.Interval {
					t.Fatalf("handled receipt still shortened polling: %s", d)
				}
				lastCancel()
			}
			result, err = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, lastPorts).Run(lastCtx)
			state = mustLoadWorkflowCheckpoint(t, opts)
			if !errors.Is(err, context.Canceled) || result.Reason != "canceled" || len(sent) != 1 || len(state.Errors) != 1 || !state.Timeout.Deadline.Equal(deadline) {
				t.Fatalf("handled timeout replayed: %+v, %v, state=%+v", result, err, state)
			}
		})
	}
}

func TestWorkflowExpiredResumeHandlesTimeoutBeforePendingDeliveries(t *testing.T) {
	for _, action := range []workflow.ErrorAction{workflow.ErrorActionPause, workflow.ErrorActionSkipStage, workflow.ErrorActionRetry} {
		t.Run(string(action), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual = false
			template := resumeWorkflowTemplate()
			template.Agents = []workflow.WorkflowAgent{{Profile: "build", Role: "build", Count: 2}}
			template.Flow.Stages = []string{"build", "done"}
			template.Flow.ParallelWithinStage = true
			template.Flow.Transitions = []workflow.Transition{{From: "build", To: "done", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "done"}}}
			template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: action, MaxRetriesPerStage: 2}
			agents := []workflow.CoordinatorAgent{{ID: "%1", Role: "build"}, {ID: "%2", Role: "build"}}
			now := time.Now().UTC()
			var sent []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			ports.now = func() time.Time { return now }
			dispatch := ports.dispatch
			ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
				if err := dispatch(ctx, session, pane, prompt); err != nil {
					return err
				}
				cancel() // The second recipient remains durably pending.
				return nil
			}
			first := mustResumeWorkflowRunner(t, template, agents, opts, ports)
			if result, err := first.Run(ctx); !errors.Is(err, context.Canceled) || len(sent) != 1 {
				t.Fatalf("partial delivery = %+v, %v, sends=%d", result, err, len(sent))
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			if len(state.Dispatches) != 2 || state.Dispatches[0].Status != "delivered" || state.Dispatches[1].Status != "pending" {
				t.Fatalf("fixture lost partial delivery: %+v", state.Dispatches)
			}
			now = now.Add(2 * time.Minute)
			opts.Resume, opts.Vars = true, nil
			resumePorts := resumeWorkflowPorts(&sent)
			resumePorts.now = func() time.Time { return now }
			resumePorts.dispatch = func(context.Context, string, string, string) error {
				t.Fatal("expired resume sent its pending prompt before applying timeout policy")
				return nil
			}
			result, err := mustResumeWorkflowRunner(t, template, agents, opts, resumePorts).Run(t.Context())
			state = mustLoadWorkflowCheckpoint(t, opts)
			switch action {
			case workflow.ErrorActionPause:
				if err == nil || result.Reason != "paused" || !state.Paused || !state.Timeout.Handled {
					t.Fatalf("expired pending stage did not pause: %+v, %v, state=%+v", result, err, state)
				}
			case workflow.ErrorActionSkipStage:
				if err != nil || !result.Completed || !state.Completed || state.CurrentStage != "done" || len(state.StageHistory) != 1 || state.Timeout != nil {
					t.Fatalf("expired pending stage did not durably skip: %+v, %v, state=%+v", result, err, state)
				}
			case workflow.ErrorActionRetry:
				if err == nil || result.Reason != "error-handler-failed" || !state.Timeout.Handling || state.Timeout.Handled || state.Recovery.Retries["build"] != 1 {
					t.Fatalf("unsafe partial retry was not stopped: %+v, %v, state=%+v", result, err, state)
				}
				if result, err := mustResumeWorkflowRunner(t, template, agents, opts, resumePorts).Run(t.Context()); err == nil || result.Reason != "resume-rejected" {
					t.Fatalf("unknown timeout action replayed: %+v, %v", result, err)
				}
			}
		})
	}
}

func TestWorkflowTimeoutSkipCommitsBeforeDestinationDelivery(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual, opts.Interval = false, time.Minute
	template := resumeWorkflowTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: workflow.ErrorActionSkipStage}
	now := time.Now().UTC()
	var sent []string
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ports := resumeWorkflowPorts(&sent)
	ports.now = func() time.Time { return now }
	ports.sleep = func(_ context.Context, d time.Duration) { now = now.Add(d) }
	ports.notify = func(format string, _ ...any) {
		if !strings.Contains(format, "skipped stage") {
			return
		}
		state := mustLoadWorkflowCheckpoint(t, opts)
		if state.CurrentStage != "review" || len(state.StageHistory) != 1 || state.Timeout != nil || state.Dispatches != nil {
			t.Fatalf("skip announced before destination committed: %+v", state)
		}
		cancel() // Simulate stopping after the skip commit, before next dispatch.
	}
	first := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
	if result, err := first.Run(ctx); !errors.Is(err, context.Canceled) || len(sent) != 1 {
		t.Fatalf("interrupted skip = %+v, %v, sends=%d", result, err, len(sent))
	}
	opts.Resume, opts.Vars, opts.FireManual, opts.Interval = true, nil, true, time.Millisecond
	resumePorts := resumeWorkflowPorts(&sent)
	resumePorts.now = func() time.Time { return now }
	resumePorts.sleep = func(_ context.Context, d time.Duration) { now = now.Add(d) }
	resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts)
	result, err := resumed.Run(t.Context())
	state := mustLoadWorkflowCheckpoint(t, opts)
	if err != nil || !result.Completed || len(sent) != 2 || !strings.HasPrefix(sent[1], "%2:") || len(state.StageHistory) != 2 || state.Timeout != nil {
		t.Fatalf("resume repeated a skipped stage: %+v, %v, sends=%v state=%+v", result, err, sent, state)
	}
}

func TestWorkflowTimeoutSkipSelfTransitionStartsFreshVisit(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.FireManual, opts.Interval, opts.MaxTransitions = false, time.Minute, 2
	template := resumeWorkflowTemplate()
	template.Flow.Transitions = []workflow.Transition{{From: "build", To: "build", Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "again"}}}
	template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: workflow.ErrorActionSkipStage}
	started := time.Now().UTC()
	now := started
	var sent []string
	ports := resumeWorkflowPorts(&sent)
	ports.now = func() time.Time { return now }
	ports.sleep = func(_ context.Context, d time.Duration) {
		if now.Sub(started) >= 2*time.Minute {
			t.Fatal("self-transition was not counted or did not establish a fresh round")
		}
		now = now.Add(d)
	}
	result, err := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports).Run(t.Context())
	state := mustLoadWorkflowCheckpoint(t, opts)
	if err != nil || result.Reason != "max-transitions" || result.Transitions != 2 || len(sent) != 3 || len(state.StageHistory) != 2 ||
		state.Timeout == nil || state.Timeout.Round != 3 || state.Timeout.Handled || !state.StageStartedAt.Equal(started.Add(2*time.Minute)) ||
		!state.Timeout.Deadline.Equal(started.Add(3*time.Minute)) {
		t.Fatalf("self-transition reused expired evidence: %+v, %v, sends=%d state=%+v", result, err, len(sent), state)
	}
}

func TestWorkflowTimeoutCheckpointFailureStopsBeforeFurtherAction(t *testing.T) {
	for _, failAcknowledgment := range []bool{false, true} {
		name := "handling intent"
		if failAcknowledgment {
			name = "action acknowledgment"
		}
		t.Run(name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.Interval = false, time.Minute
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: workflow.ErrorActionNotify}
			now := time.Now().UTC()
			var sent []string
			blocked := filepath.Join(opts.ProjectRoot, "not-a-directory")
			if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			var runner *workflowRunner
			ports := resumeWorkflowPorts(&sent)
			ports.now = func() time.Time { return now }
			ports.sleep = func(_ context.Context, d time.Duration) {
				now = now.Add(d)
				if !failAcknowledgment {
					runner.store.Dir = blocked
				}
			}
			notifications := 0
			ports.notify = func(_ string, args ...any) {
				for _, arg := range args {
					if fault, ok := arg.(*workflow.WorkflowError); ok && fault.Type == workflow.ErrorTimeout {
						notifications++
						state := mustLoadWorkflowCheckpoint(t, opts)
						if !state.Timeout.Handling || state.Timeout.Handled || len(state.Errors) != 1 {
							t.Fatalf("timeout acted before its durable intent: %+v", state)
						}
						if failAcknowledgment {
							runner.store.Dir = blocked
						}
					}
				}
			}
			runner = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			result, err := runner.Run(t.Context())
			if err == nil || result.Reason != "checkpoint-failed" || result.Success || len(sent) != 1 {
				t.Fatalf("timeout checkpoint failure did not stop: %+v, %v, sends=%d", result, err, len(sent))
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			wantNotifications := 0
			if failAcknowledgment {
				wantNotifications = 1
			}
			if notifications != wantNotifications || len(state.Errors) != wantNotifications || state.Timeout.Handled || state.Timeout.Handling != failAcknowledgment {
				t.Fatalf("failed timeout write changed action evidence: notifications=%d state=%+v", notifications, state)
			}
			if failAcknowledgment {
				opts.Resume, opts.Vars = true, nil
				resumePorts := resumeWorkflowPorts(&sent)
				resumePorts.now = func() time.Time { return now }
				result, err := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts).Run(t.Context())
				if err == nil || result.Reason != "resume-rejected" || len(sent) != 1 {
					t.Fatalf("unacknowledged timeout action replayed: %+v, %v", result, err)
				}
			}
		})
	}
}

func TestWorkflowResumeRejectsMissingOrStaleTimeoutReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*workflow.StageTimeout) *workflow.StageTimeout
	}{
		{"missing deadline", func(*workflow.StageTimeout) *workflow.StageTimeout { return nil }},
		{"unknown version", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Version++; return r }},
		{"different stage", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Stage = "review"; return r }},
		{"previous visit", func(r *workflow.StageTimeout) *workflow.StageTimeout {
			r.StartedAt = r.StartedAt.Add(-time.Second)
			return r
		}},
		{"different round", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Round++; return r }},
		{"missing timestamp", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Deadline = time.Time{}; return r }},
		{"deadline before visit", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Deadline = r.StartedAt; return r }},
		{"deadline extended", func(r *workflow.StageTimeout) *workflow.StageTimeout {
			r.Deadline = r.Deadline.Add(time.Minute)
			return r
		}},
		{"changed action", func(r *workflow.StageTimeout) *workflow.StageTimeout {
			r.Action = workflow.ErrorActionSkipStage
			return r
		}},
		{"unknown action outcome", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.Handling = true; return r }},
		{"conflicting action receipt", func(r *workflow.StageTimeout) *workflow.StageTimeout {
			r.Handling, r.Handled = true, true
			return r
		}},
		{"unhandled stop reason", func(r *workflow.StageTimeout) *workflow.StageTimeout { r.StopReason = "paused"; return r }},
		{"invalid handled reason", func(r *workflow.StageTimeout) *workflow.StageTimeout {
			r.Handled, r.StopReason = true, "unknown"
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual = false
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{StageTimeoutMinutes: 1, OnTimeout: workflow.ErrorActionPause}
			now := time.Now().UTC()
			var sent []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			ports.now = func() time.Time { return now }
			ports.sleep = func(context.Context, time.Duration) { cancel() }
			if result, err := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports).Run(ctx); !errors.Is(err, context.Canceled) || len(sent) != 1 {
				t.Fatalf("seed stage = %+v, %v, sends=%d", result, err, len(sent))
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			state.Timeout = tc.change(state.Timeout)
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			opts.Resume, opts.Vars = true, nil
			resumePorts := resumeWorkflowPorts(&sent)
			resumePorts.now = func() time.Time { return now }
			resumeCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			result, err := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts).Run(resumeCtx)
			if err == nil || result.Success || result.Reason != "resume-rejected" || len(sent) != 1 {
				t.Fatalf("unsafe timeout receipt resumed: %+v, %v, sends=%d", result, err, len(sent))
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatalf("rejected timeout receipt was mutated: %v", err)
			}
		})
	}
}

func TestWorkflowResumePreservesConsumedRecoveryBudget(t *testing.T) {
	for _, action := range []workflow.ErrorAction{workflow.ErrorActionRestartAgent, workflow.ErrorActionRetry} {
		t.Run(string(action), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.MaxAgentRestarts = false, 1
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: action, MaxRetriesPerStage: 1}
			agents := newFakeWorkflowAgents(pingPongPIDs())
			var sent []string
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ports := resumeWorkflowPorts(&sent)
			baseDispatch := ports.dispatch
			ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
				if len(sent) == 1 {
					// The recovery attempt must already be durable when the
					// retried prompt reaches the external transport.
					state := mustLoadWorkflowCheckpoint(t, opts)
					if state.Recovery == nil {
						t.Fatal("recovery prompt dispatched without durable budget evidence")
					}
					if action == workflow.ErrorActionRetry && state.Recovery.Retries["build"] != 1 {
						t.Fatalf("retry dispatched before its budget was consumed: %+v", state.Recovery)
					}
					if action == workflow.ErrorActionRestartAgent && state.Recovery.Restarts[pane] != 1 {
						t.Fatalf("restart prompt dispatched before its budget was consumed: %+v", state.Recovery)
					}
				}
				if err := baseDispatch(ctx, session, pane, prompt); err != nil {
					return err
				}
				if len(sent) == 1 {
					agents.set(pane, crashFault("agent exited during its first turn"))
				}
				return nil
			}
			ports.sleep = func(context.Context, time.Duration) {
				if len(sent) >= 2 {
					cancel() // Stop after the recovery action and receipt complete.
				}
			}
			var first *workflowRunner
			agents.wire(&ports, &first)
			baseRestart := ports.restart
			ports.restart = func(ctx context.Context, session, pane string) (int, error) {
				state := mustLoadWorkflowCheckpoint(t, opts)
				if state.Recovery == nil || state.Recovery.Restarts[pane] != 1 {
					t.Fatalf("agent restarted before reserving its durable budget: %+v", state.Recovery)
				}
				return baseRestart(ctx, session, pane)
			}
			first = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			result, err := first.Run(ctx)
			if !errors.Is(err, context.Canceled) || result.Reason != "canceled" || len(sent) != 2 {
				t.Fatalf("initial recovery = %+v, %v, sends=%d", result, err, len(sent))
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			if state.Recovery == nil || len(state.Dispatches) != 1 || state.Dispatches[0].Status != "delivered" {
				t.Fatalf("successful recovery did not preserve its checkpoint: %+v", state)
			}
			wantRestarts := 0
			if action == workflow.ErrorActionRestartAgent {
				wantRestarts = 1
				if state.PanePIDs["%1"] == opts.PanePIDs["%1"] {
					t.Fatal("recovery did not bind the replacement agent process")
				}
			}
			if got := len(agents.restarted()); got != wantRestarts {
				t.Fatalf("initial restarts = %d, want %d", got, wantRestarts)
			}

			// Reconstruct the runner, including its handler and the live pane
			// identity. Repeated resumes must not grant another recovery attempt.
			opts.Resume, opts.Vars, opts.PanePIDs = true, nil, state.PanePIDs
			for attempt := 1; attempt <= 2; attempt++ {
				agents.set("%1", crashFault("replacement agent exited"))
				resumePorts := resumeWorkflowPorts(&sent)
				resumePorts.dispatch = func(_ context.Context, _, pane, prompt string) error {
					sent = append(sent, pane+":"+prompt)
					return errors.New("resumed runner repeated recovered work")
				}
				var resumed *workflowRunner
				agents.wire(&resumePorts, &resumed)
				resumed = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumePorts)
				resumeCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
				result, err := resumed.Run(resumeCtx)
				stop()
				if err == nil || result.Reason != "aborted" || !result.Resumed || result.Restarts != 0 {
					t.Fatalf("resume %d reset recovery budget: %+v, %v", attempt, result, err)
				}
				if len(sent) != 2 || len(agents.restarted()) != wantRestarts {
					t.Fatalf("resume %d repeated a recovery side effect: sends=%d restarts=%v", attempt, len(sent), agents.restarted())
				}
				resumedState := mustLoadWorkflowCheckpoint(t, opts)
				if !reflect.DeepEqual(resumedState.Recovery, state.Recovery) {
					t.Fatalf("resume %d changed exhausted budgets: got %+v, want %+v", attempt, resumedState.Recovery, state.Recovery)
				}
			}
		})
	}
}

func TestWorkflowRecoveryBudgetWriteFailureRefusesSideEffects(t *testing.T) {
	for _, action := range []workflow.ErrorAction{workflow.ErrorActionRestartAgent, workflow.ErrorActionRetry} {
		t.Run(string(action), func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.FireManual, opts.MaxAgentRestarts = false, 1
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: action, MaxRetriesPerStage: 1}
			agents := newFakeWorkflowAgents(pingPongPIDs())
			var sent []string
			ports := resumeWorkflowPorts(&sent)
			baseDispatch := ports.dispatch
			ports.dispatch = func(ctx context.Context, session, pane, prompt string) error {
				if err := baseDispatch(ctx, session, pane, prompt); err != nil {
					return err
				}
				agents.set(pane, crashFault("agent exited"))
				return nil
			}
			blocked := filepath.Join(opts.ProjectRoot, "not-a-directory")
			if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			var runner *workflowRunner
			agents.wire(&ports, &runner)
			injected := false
			ports.notify = func(_ string, _ ...any) {
				if injected || runner.state == nil || len(runner.state.Errors) == 0 {
					return
				}
				// Agent supervision has saved the fault before notifying. Fail
				// only the ensuing recovery reservation, not initial dispatch or
				// fault detection, so this exercises the write-before-action gate.
				state := mustLoadWorkflowCheckpoint(t, opts)
				if len(state.Errors) != 1 || state.Recovery == nil {
					t.Fatalf("fault was not checkpointed before recovery: %+v", state)
				}
				injected = true
				runner.store.Dir = blocked
			}
			runner = mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, ports)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err := runner.Run(ctx)
			if !injected || err == nil || result.Success || result.Reason != "checkpoint-failed" {
				t.Fatalf("failed recovery reservation was not surfaced: %+v, %v, injected=%v", result, err, injected)
			}
			if len(sent) != 1 || len(agents.restarted()) != 0 {
				t.Fatalf("failed reservation performed an external action: sends=%d restarts=%v", len(sent), agents.restarted())
			}
			if len(runner.state.Dispatches) != 1 || runner.state.Dispatches[0].Status != "delivered" || runner.state.Evidence == nil {
				t.Fatalf("failed reservation entered the retry action and cleared delivery evidence: %+v", runner.state)
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			if state.Recovery.Retries["build"] != 0 || state.Recovery.Restarts["%1"] != 0 || len(state.Dispatches) != 1 || state.Dispatches[0].Status != "delivered" {
				t.Fatalf("failed reservation altered the durable recovery state: %+v", state)
			}
		})
	}
}

func TestWorkflowResumeRejectsMissingOrInvalidRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*workflow.WorkflowState)
	}{
		{"legacy missing budgets", func(s *workflow.WorkflowState) { s.Recovery = nil }},
		{"missing retry ledger", func(s *workflow.WorkflowState) { s.Recovery.Retries = nil }},
		{"missing restart ledger", func(s *workflow.WorkflowState) { s.Recovery.Restarts = nil }},
		{"unknown retry stage", func(s *workflow.WorkflowState) { s.Recovery.Retries = map[string]int{"missing-stage": 1} }},
		{"unknown restarted pane", func(s *workflow.WorkflowState) { s.Recovery.Restarts = map[string]int{"%99": 1} }},
		{"negative retry count", func(s *workflow.WorkflowState) { s.Recovery.Retries = map[string]int{"build": -1} }},
		{"negative restart count", func(s *workflow.WorkflowState) { s.Recovery.Restarts = map[string]int{"%1": -1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.MaxAgentRestarts = 1
			template := resumeWorkflowTemplate()
			template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: workflow.ErrorActionRestartAgent, MaxRetriesPerStage: 1}
			var sent []string
			runner := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			if result, err := runner.Run(t.Context()); err != nil || result.Reason != "max-transitions" {
				t.Fatalf("initial run = %+v, %v", result, err)
			}
			state := mustLoadWorkflowCheckpoint(t, opts)
			if state.Recovery == nil {
				t.Fatal("new checkpoint lacks recovery evidence")
			}
			state.Paused = true
			tc.change(state)
			// Inject old or damaged input rather than asking the normal save
			// path to construct an invalid execution checkpoint.
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			opts.Resume, opts.Vars = true, nil
			resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			result, err := resumed.Run(t.Context())
			if err == nil || result.Success || result.Reason != "resume-rejected" || len(sent) != 2 {
				t.Fatalf("unsafe recovery evidence resumed: %+v, %v, sends=%d", result, err, len(sent))
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatalf("recovery rejection changed the saved checkpoint: %v", err)
			}
		})
	}
}

func TestWorkflowResumeCompletedCheckpointDoesNotRequireRecoveryEvidence(t *testing.T) {
	opts := resumeWorkflowOptions(t)
	opts.MaxTransitions, opts.MaxAgentRestarts = 3, 1
	template := resumeWorkflowTemplate()
	template.ErrorHandling = &workflow.ErrorConfig{OnAgentCrash: workflow.ErrorActionRestartAgent}
	var sent []string
	first := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	if result, err := first.Run(t.Context()); err != nil || !result.Completed || len(sent) != 2 {
		t.Fatalf("completed run = %+v, %v, sends=%d", result, err, len(sent))
	}
	state := mustLoadWorkflowCheckpoint(t, opts)
	state.Recovery = nil // A completed checkpoint from before recovery journaling.
	if err := (&workflow.StateStore{Dir: opts.StateDir}).Save(state); err != nil {
		t.Fatal(err)
	}
	opts.Resume, opts.Vars = true, nil
	resumed := mustResumeWorkflowRunner(t, template, resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
	result, err := resumed.Run(t.Context())
	if err != nil || !result.Success || !result.Completed || !result.Resumed || len(sent) != 2 {
		t.Fatalf("completed legacy checkpoint did not remain observational: %+v, %v, sends=%d", result, err, len(sent))
	}
}

func TestWorkflowResumeBindsTmuxEndpointBeforeObservation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		original        string
		resume          string
		missingIdentity bool
		completed       bool
		allowed         bool
	}{
		{name: "same local endpoint", original: "local", resume: "local", allowed: true},
		{name: "same SSH endpoint", original: "ssh:build-host", resume: "ssh:build-host", allowed: true},
		{name: "another SSH host", original: "ssh:build-host", resume: "ssh:other-host"},
		{name: "local to SSH", original: "local", resume: "ssh:build-host"},
		{name: "SSH to local", original: "ssh:build-host", resume: "local"},
		{name: "legacy local checkpoint", original: "local", resume: "local", missingIdentity: true},
		{name: "legacy SSH checkpoint", original: "ssh:build-host", resume: "ssh:build-host", missingIdentity: true},
		{name: "completed checkpoint on another host", original: "ssh:build-host", resume: "ssh:other-host", completed: true},
		{name: "completed legacy checkpoint", original: "local", resume: "local", missingIdentity: true, completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := resumeWorkflowOptions(t)
			opts.TmuxEndpoint = tc.original
			if tc.completed {
				opts.MaxTransitions = 3
			}
			var sent []string
			first := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, resumeWorkflowPorts(&sent))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			result, err := first.Run(ctx)
			if err != nil || !result.Success || result.Completed != tc.completed || len(sent) != 2 {
				t.Fatalf("initial run = %+v, %v, sends=%v", result, err, sent)
			}
			prior := mustLoadWorkflowCheckpoint(t, opts)
			if prior.TmuxEndpoint != tc.original {
				t.Fatalf("checkpoint endpoint = %q, want %q", prior.TmuxEndpoint, tc.original)
			}
			if !tc.completed {
				if err := (&workflow.StateStore{Dir: opts.StateDir}).Pause(prior, "operator pause", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			if tc.missingIdentity {
				// Reproduce an older checkpoint without allowing the current
				// writer to infer which host originally received its prompts.
				prior.TmuxEndpoint = ""
				data, err := json.MarshalIndent(prior, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			opts.Resume, opts.Vars, opts.TmuxEndpoint = true, nil, tc.resume
			ports := resumeWorkflowPorts(&sent)
			observations := 0
			ports.endpoint = func() string { return tc.resume }
			ports.capture = func(context.Context, string, int) (string, error) {
				observations++
				return "", nil
			}
			ports.faults = func(context.Context, map[string]int) (map[string]workflowAgentFault, error) {
				observations++
				return nil, nil
			}
			resumed := mustResumeWorkflowRunner(t, resumeWorkflowTemplate(), resumeWorkflowAgents(), opts, ports)
			result, err = resumed.Run(ctx)
			if tc.allowed {
				if err != nil || !result.Success || !result.Completed || !result.Resumed || len(sent) != 2 {
					t.Fatalf("same endpoint lost progress or repeated prompts: %+v, %v, sends=%v", result, err, sent)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "endpoint") || result.Success || result.Reason != "resume-rejected" || observations != 0 || len(sent) != 2 {
				t.Fatalf("foreign or unbound checkpoint was observed or acted on: %+v, %v, observations=%d sends=%v", result, err, observations, sent)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("endpoint refusal cleared the pause or changed the checkpoint")
			}
		})
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
			// Inject corrupt on-disk input directly: the normal save path
			// correctly refuses these inconsistent execution identities.
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, opts.Session+".json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
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
