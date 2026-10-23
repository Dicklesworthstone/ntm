package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Produce an actual uncertain delivery through the executor. The verifier
// fails after the one paste, so recovery cannot confuse a synthetic journal
// row with work that never crossed the transport boundary.
func uncertainRecoveryFixture(t *testing.T, shape string) (ExecutorConfig, *Workflow, *ExecutionState, *MockTmuxClient, string, string) {
	t.Helper()
	cfg := DefaultExecutorConfig("operator-recovery")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), GenerateRunID()
	cfg.DefaultTimeout, cfg.GlobalTimeout = time.Second, 5*time.Second
	leaf := Step{ID: "agent", Pane: PaneSpec{Index: 1}, Prompt: "SECRET-TASK ${vars.subject}", Wait: WaitTime, Timeout: Duration{Duration: time.Millisecond}}
	root := leaf
	switch shape {
	case "foreach":
		root = Step{ID: "outer", Foreach: &ForeachConfig{Items: `["outer"]`, Steps: []Step{
			{ID: "inner", Foreach: &ForeachConfig{Items: `["inner"]`, Steps: []Step{leaf}}},
		}}}
	case "branch":
		other := leaf
		other.Prompt = "UNSELECTED-TASK"
		root = Step{ID: "route", Branch: "z", Branches: map[string]interface{}{"a": []Step{other}, "z": []Step{leaf}}}
	}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "operator-recovery", Settings: DefaultWorkflowSettings(), Steps: []Step{
		root, {ID: "after", Command: "printf resumed", DependsOn: []string{root.ID}},
	}}
	frozen, path, err := SnapshotWorkflow(context.Background(), cfg.ProjectDir, workflow)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkflowFile = path
	client := NewMockTmuxClient(tmux.Pane{ID: "%17", PID: 2718, Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Command: "claude", Width: 120})
	t.Cleanup(client.Reset)
	if err := client.SetPaneOutput("%17", "OLD-BOUNDARY\n"); err != nil {
		t.Fatal(err)
	}
	client.SetSubmissionVerifier(func(string, string, string) error { return errors.New("submission observation was interrupted") })
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(client)
	state, runErr := executor.Run(context.Background(), frozen, map[string]interface{}{"subject": "original"}, nil)
	if runErr == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("fixture did not stop at an uncertain delivery: %+v %v", state, runErr)
	}
	state, err = LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil || len(state.AgentDeliveries) != 1 {
		t.Fatalf("fixture did not persist exactly one delivery: %+v %v", state, err)
	}
	var stepID string
	for id, record := range state.AgentDeliveries {
		stepID = id
		if record.Status != agentDeliverySending {
			t.Fatalf("fixture receipt is not uncertain: %+v", record)
		}
		if err := client.SetPaneOutput("%17", record.BeforeOutput+"\nFRESH-RESPONSE\n"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectAgentDeliveryRecovery(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil || len(inspection.Deliveries) != 1 || !inspection.Deliveries[0].CanAccept {
		t.Fatalf("uncertain delivery cannot be inspected: %+v %v", inspection, err)
	}
	encoded, err := json.Marshal(inspection)
	if err != nil || strings.Contains(string(encoded), "SECRET-TASK") || strings.Contains(string(encoded), "OLD-BOUNDARY") || strings.Contains(string(encoded), "FRESH-RESPONSE") {
		t.Fatalf("inspection exposed prompt or transcript: %s %v", encoded, err)
	}
	after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
	if err != nil || string(after) != string(before) {
		t.Fatalf("inspection changed the checkpoint: %v", err)
	}
	return cfg, frozen, state, client, stepID, inspection.Deliveries[0].ReceiptToken
}

func TestAgentDeliveryRecoveryScopedContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, shape string
		drift       bool
	}{{"nested foreach", "foreach", false}, {"selected branch with reused child ID", "branch", false}, {"changed materialized foreach definition", "foreach", true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, workflow, prior, client, stepID, token := uncertainRecoveryFixture(t, tc.shape)
			original := prior.AgentDeliveries[stepID]
			resolver := NewExecutor(cfg)
			resolver.SetTmuxClient(client)
			accepted, err := resolver.acceptAgentDeliveryContext(context.Background(), stepID, token, "Observed the original agent working on the submitted task")
			if err != nil || accepted.Replayed || accepted.Delivery.Status != agentDeliveryDelivered || accepted.Delivery.ReceiptToken != token {
				t.Fatalf("accept scoped delivery: %+v %v", accepted, err)
			}
			acceptedState, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := acceptedState.AgentDeliveries[stepID]
			if receipt.BeforeOutput != original.BeforeOutput || receipt.Output != original.Output || !receipt.StartedAt.Equal(original.StartedAt) || acceptedState.Steps["after"].Status == StatusCompleted {
				t.Fatalf("acceptance changed observation evidence or executed downstream work: %+v", acceptedState)
			}
			pastes, err := client.PasteHistory("%17")
			if err != nil || len(pastes) != 1 || len(client.VerificationHistory()) != 1 {
				t.Fatalf("acceptance actuated transport: %+v %v", pastes, err)
			}
			if tc.drift {
				acceptedState.Variables["subject"] = "changed-after-inspection"
			}
			resumed := NewExecutor(cfg)
			resumed.SetTmuxClient(client)
			final, resumeErr := resumed.Resume(context.Background(), workflow, acceptedState, nil)
			pastes, err = client.PasteHistory("%17")
			if err != nil || len(pastes) != 1 {
				t.Fatalf("resume resent an accepted delivery: %+v %v", pastes, err)
			}
			if tc.drift {
				if resumeErr == nil || final.Steps["after"].Status == StatusCompleted || final.AgentDeliveries[stepID] != receipt {
					t.Fatalf("changed materialized definition escaped canonical resume gate: %+v %v", final, resumeErr)
				}
			} else {
				if resumeErr != nil || final.Status != StatusCompleted || final.Steps["after"].Output != "resumed" || strings.TrimSpace(final.Steps[stepID].Output) != "FRESH-RESPONSE" {
					t.Fatalf("accepted scoped work did not continue with original response: %+v %v", final, resumeErr)
				}
				completed := final.AgentDeliveries[stepID]
				if completed.Status != agentDeliveryCompleted || completed.ResolutionToken != token || completed.ResolutionReason != receipt.ResolutionReason || !completed.ResolvedAt.Equal(receipt.ResolvedAt) {
					t.Fatalf("completion lost immutable acceptance: %+v", completed)
				}
				// Settled replay must not depend on a still-live historical pane.
				client.AddPane("", tmux.Pane{ID: "%17", PID: 9999, Index: 1, Type: tmux.AgentClaude})
				replay, err := resolver.acceptAgentDeliveryContext(context.Background(), stepID, token, receipt.ResolutionReason)
				if err != nil || !replay.Replayed || replay.Delivery.Status != agentDeliveryCompleted {
					t.Fatalf("completed acceptance is not idempotent: %+v %v", replay, err)
				}
			}
			// AddPane resets its fixture's history, so assert the no-repeat
			// invariant against the verifier too; resume never verifies a send.
			if len(client.VerificationHistory()) != 1 {
				t.Fatalf("recovery repeated submission: %+v", client.VerificationHistory())
			}
		})
	}
}

func TestAgentDeliveryRecoveryRejectsChangedAcceptedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AgentDeliveryState)
	}{
		{"pane process", func(r *AgentDeliveryState) { r.PanePID++ }},
		{"step definition", func(r *AgentDeliveryState) { r.StepHash = strings.Repeat("a", 64) }},
		{"submitted prompt", func(r *AgentDeliveryState) { r.PromptHash = strings.Repeat("b", 64) }},
		{"original boundary", func(r *AgentDeliveryState) { r.ResolutionBoundaryHash = strings.Repeat("c", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, workflow, _, client, stepID, token := uncertainRecoveryFixture(t, "plain")
			resolver := NewExecutor(cfg)
			resolver.SetTmuxClient(client)
			if _, err := resolver.acceptAgentDeliveryContext(context.Background(), stepID, token, "Confirmed delivery in original pane"); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			record := prior.AgentDeliveries[stepID]
			tc.mutate(&record)
			prior.AgentDeliveries[stepID] = record
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil {
				t.Fatal(err)
			}
			resume := NewExecutor(cfg)
			resume.SetTmuxClient(client)
			if _, err := resume.Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "original receipt identity") {
				t.Fatalf("accepted identity change was not refused before resume: %v", err)
			}
			after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil || string(after) != string(before) || len(client.VerificationHistory()) != 1 {
				t.Fatalf("refused recovery changed evidence or repeated delivery: %v", err)
			}
		})
	}
}

type recoveryCaptureClient struct {
	*MockTmuxClient
	onCapture func()
}

func (c *recoveryCaptureClient) CapturePaneOutput(target string, lines int) (string, error) {
	if c.onCapture != nil {
		c.onCapture()
	}
	return c.MockTmuxClient.CapturePaneOutput(target, lines)
}

func TestAgentDeliveryRecoveryInterruptedAcceptancePreservesReceipt(t *testing.T) {
	for _, mode := range []string{"cancelled during observation", "persistence refused"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, _, client, stepID, token := uncertainRecoveryFixture(t, "plain")
			before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolver := NewExecutor(cfg)
			capture := &recoveryCaptureClient{MockTmuxClient: client}
			if mode == "cancelled during observation" {
				capture.onCapture = cancel
			} else {
				blocked := filepath.Join(cfg.ProjectDir, "unwritable-state-root")
				if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				capture.onCapture = func() { resolver.config.ProjectDir = blocked }
			}
			resolver.SetTmuxClient(capture)
			if result, err := resolver.acceptAgentDeliveryContext(ctx, stepID, token, "Confirmed original delivery"); err == nil || result != nil {
				t.Fatalf("interrupted decision was reported accepted: %+v %v", result, err)
			}
			after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID))
			if err != nil || string(after) != string(before) || len(client.VerificationHistory()) != 1 {
				t.Fatalf("failed acceptance changed receipt or submitted work: %v", err)
			}
		})
	}
}

func testAgentDelivery(stepID, status string) AgentDeliveryState {
	started := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	record := AgentDeliveryState{
		Version: agentDeliveryVersion, StepID: stepID, Kind: StepKindPrompt,
		Session: "resume-session", Endpoint: "local", PaneID: "%17", PanePID: 2718, AgentType: "claude",
		StepHash: strings.Repeat("a", 64), PromptHash: strings.Repeat("b", 64),
		PromptEchoHash: strings.Repeat("c", 64),
		Status:         status, BeforeOutput: "previous task", StartedAt: started,
	}
	if status == agentDeliveryDelivered || status == agentDeliveryCompleted {
		record.DeliveredAt = started.Add(time.Second)
	}
	if status == agentDeliveryCompleted {
		record.CompletedAt = started.Add(2 * time.Second)
		record.Output = "current result"
	}
	return record
}

func TestAgentDeliveryJournalPersistsAndReturnsCopies(t *testing.T) {
	cfg := DefaultExecutorConfig("resume-session")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID: "agent-delivery-roundtrip", WorkflowID: "delivery-workflow", Session: cfg.Session,
		Status: StatusRunning, Steps: map[string]StepResult{}, Variables: map[string]interface{}{},
	}
	for _, status := range []string{agentDeliverySending, agentDeliveryDelivered, agentDeliveryCompleted} {
		record := testAgentDelivery("review", status)
		if err := executor.saveAgentDelivery(record); err != nil {
			t.Fatalf("save %s: %v", status, err)
		}
		persisted, err := LoadState(cfg.ProjectDir, executor.state.RunID)
		if err != nil {
			t.Fatalf("load %s: %v", status, err)
		}
		if got := persisted.AgentDeliveries["review"]; !reflect.DeepEqual(got, record) {
			t.Fatalf("persisted %s receipt = %#v, want %#v", status, got, record)
		}
		loaded, ok := executor.loadAgentDelivery("review")
		if !ok {
			t.Fatal("saved delivery missing")
		}
		loaded.Status = "mutated"
		loaded.Output = "mutated"
		again, _ := executor.loadAgentDelivery("review")
		if !reflect.DeepEqual(again, record) {
			t.Fatalf("caller mutated journal through load: %#v", again)
		}
	}
	if _, ok := executor.loadAgentDelivery("unknown"); ok {
		t.Fatal("unknown step has a delivery")
	}

	// A failed durable write must propagate to the dispatch caller. Keeping the
	// intent in memory still prevents another attempt from treating it as new.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	executor.config.ProjectDir = blocked
	if err := executor.saveAgentDelivery(testAgentDelivery("next", agentDeliverySending)); !errors.Is(err, ErrCheckpointFailed) {
		t.Fatalf("failed save = %v, want ErrCheckpointFailed", err)
	}
	if _, ok := executor.loadAgentDelivery("next"); !ok {
		t.Fatal("failed persistence erased in-memory dispatch intent")
	}
}

func TestAgentStepHashUsesCompleteStableDefinition(t *testing.T) {
	step := Step{ID: "review", Template: "review", Params: map[string]interface{}{"a": "first", "b": "second"}}
	hash, err := agentStepHash(&step)
	if err != nil {
		t.Fatal(err)
	}
	copy := cloneStep(step)
	copy.Params = map[string]interface{}{"b": "second", "a": "first"}
	got, err := agentStepHash(&copy)
	if err != nil || got != hash {
		t.Fatalf("equivalent definition hash = %q, %v, want %q", got, err, hash)
	}
	copy.Params["a"] = "changed"
	got, err = agentStepHash(&copy)
	if err != nil || got == hash {
		t.Fatalf("changed definition hash = %q, %v, original %q", got, err, hash)
	}
	copy.Params["unsupported"] = make(chan int)
	if _, err := agentStepHash(&copy); err == nil {
		t.Fatal("non-serializable definition was accepted")
	}
	if _, err := agentStepHash(nil); err == nil {
		t.Fatal("nil definition was accepted")
	}
}

func TestResumeRejectsInvalidAgentDeliveriesBeforeReset(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AgentDeliveryState)
	}{
		{"unknown status", func(r *AgentDeliveryState) { r.Status = "maybe-sent" }},
		{"unsupported version", func(r *AgentDeliveryState) { r.Version++ }},
		{"different step", func(r *AgentDeliveryState) { r.StepID = "other" }},
		{"different session", func(r *AgentDeliveryState) { r.Session = "other" }},
		{"missing endpoint", func(r *AgentDeliveryState) { r.Endpoint = "" }},
		{"empty remote endpoint", func(r *AgentDeliveryState) { r.Endpoint = "ssh: " }},
		{"unsupported endpoint", func(r *AgentDeliveryState) { r.Endpoint = "socket:other" }},
		{"command kind", func(r *AgentDeliveryState) { r.Kind = StepKindCommand }},
		{"logical pane", func(r *AgentDeliveryState) { r.PaneID = "resume-session:0.1" }},
		{"missing PID", func(r *AgentDeliveryState) { r.PanePID = 0 }},
		{"missing agent", func(r *AgentDeliveryState) { r.AgentType = "" }},
		{"invalid step hash", func(r *AgentDeliveryState) { r.StepHash = "broken" }},
		{"invalid prompt hash", func(r *AgentDeliveryState) { r.PromptHash = strings.Repeat("z", 64) }},
		{"oversized baseline", func(r *AgentDeliveryState) { r.BeforeOutput = strings.Repeat("x", agentDeliveryMaxCaptureBytes+1) }},
		{"oversized result", func(r *AgentDeliveryState) { r.Output = strings.Repeat("x", agentDeliveryMaxOutputBytes+1) }},
		{"missing start", func(r *AgentDeliveryState) { r.StartedAt = time.Time{} }},
		{"missing delivery", func(r *AgentDeliveryState) { r.DeliveredAt = time.Time{} }},
		{"completion before delivery", func(r *AgentDeliveryState) { r.CompletedAt = r.StartedAt }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, opts := range []ResumeOptions{{Reset: true}, {Mode: ResumeModeRestartFailed}, {Mode: ResumeModeForceIter, StepID: "fanout", Iteration: 0}} {
				executor := NewExecutor(DefaultExecutorConfig("resume-session"))
				record := testAgentDelivery("fanout_iter0_work", agentDeliveryCompleted)
				tc.mutate(&record)
				executor.state = &ExecutionState{
					Session: "resume-session", Steps: map[string]StepResult{record.StepID: {Status: StatusFailed}},
					AgentDeliveries: map[string]AgentDeliveryState{"fanout_iter0_work": record},
				}
				err := executor.applyResumeOptions(nil, opts)
				if err == nil || !strings.Contains(err.Error(), "invalid agent delivery") {
					t.Fatalf("opts %#v: invalid journal accepted: %v", opts, err)
				}
				if tc.name == "unknown status" && !strings.Contains(err.Error(), "unknown delivery status") {
					t.Fatalf("unknown status diagnosis = %v", err)
				}
				if got := executor.state.AgentDeliveries["fanout_iter0_work"]; !reflect.DeepEqual(got, record) {
					t.Fatalf("opts %#v discarded invalid dispatch evidence", opts)
				}
				if len(executor.state.Steps) != 1 || executor.state.ForeachState != nil {
					t.Fatalf("opts %#v mutated state before validation", opts)
				}
			}
		})
	}
}

func TestResumeAgentDeliveryPolicies(t *testing.T) {
	for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
		t.Run(string(mode), func(t *testing.T) {
			executor := NewExecutor(DefaultExecutorConfig("resume-session"))
			executor.state = &ExecutionState{
				Session: "resume-session", Steps: map[string]StepResult{},
				InFlightSteps:   map[string]InFlightStepState{"inflight": {StepID: "inflight", Kind: StepKindPrompt}},
				AgentDeliveries: map[string]AgentDeliveryState{},
			}
			for _, status := range []ExecutionStatus{StatusCompleted, StatusFailed, StatusCancelled, StatusRunning, StatusPending} {
				id := string(status)
				executor.state.Steps[id] = StepResult{StepID: id, Status: status}
				executor.state.AgentDeliveries[id] = testAgentDelivery(id, agentDeliveryDelivered)
			}
			executor.state.AgentDeliveries["inflight"] = testAgentDelivery("inflight", agentDeliverySending)
			executor.state.AgentDeliveries["receipt-only"] = testAgentDelivery("receipt-only", agentDeliverySending)
			executor.state.AgentDeliveries["completed-receipt"] = testAgentDelivery("completed-receipt", agentDeliveryCompleted)
			executor.state.Steps["completed-receipt"] = StepResult{Status: StatusRunning}
			executor.state.InFlightSteps["completed-receipt"] = InFlightStepState{StepID: "completed-receipt", Kind: StepKindPrompt}
			if err := executor.applyResumeOptions(nil, ResumeOptions{Mode: mode}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"completed", "failed", "cancelled", "running", "pending", "inflight", "receipt-only", "completed-receipt"} {
				_, exists := executor.loadAgentDelivery(id)
				want := mode == ResumeModeContinue || id == "completed" || id == "receipt-only" || id == "completed-receipt"
				if exists != want {
					t.Fatalf("receipt %q present = %t, want %t", id, exists, want)
				}
			}
		})
	}
}

func TestResumeAgentDeliveryCannotMoveSessionsWithoutReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		executor := NewExecutor(DefaultExecutorConfig("new-session"))
		record := testAgentDelivery("review", agentDeliveryDelivered)
		executor.state = &ExecutionState{
			Session: "resume-session", Steps: map[string]StepResult{"review": {Status: StatusRunning}},
			AgentDeliveries: map[string]AgentDeliveryState{"review": record},
		}
		err := executor.applyResumeOptions(nil, ResumeOptions{Reset: reset, OnRosterChange: ResumeRosterProceed})
		if reset {
			if err != nil || executor.state.Session != "new-session" || len(executor.state.AgentDeliveries) != 0 {
				t.Fatalf("explicit reset failed to migrate: session=%q receipts=%#v err=%v", executor.state.Session, executor.state.AgentDeliveries, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "explicit reset") {
				t.Fatalf("preserved delivery moved to another session: %v", err)
			}
			if executor.state.Session != "resume-session" || executor.state.AgentDeliveries["review"] != record {
				t.Fatal("refused session migration mutated delivery identity")
			}
		}
	}
}

func TestResumeLegacyExternalLeafRequiresExplicitRestart(t *testing.T) {
	workflow := &Workflow{Name: "legacy", Steps: []Step{
		{ID: "prompt", Prompt: "review"},
		{ID: "template", Template: "review"},
		{ID: "command", Command: "echo allowed"},
		{ID: "group", Parallel: ParallelSpec{Steps: []Step{{ID: "nested", Prompt: "review nested"}}}},
	}}
	for _, stepID := range []string{"prompt", "template", "group_nested", "command"} {
		t.Run(stepID, func(t *testing.T) {
			for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
				executor := NewExecutor(DefaultExecutorConfig("resume-session"))
				executor.graph = NewDependencyGraph(workflow)
				executor.state = &ExecutionState{
					Session: "resume-session", Steps: map[string]StepResult{stepID: {StepID: stepID, Status: StatusRunning}},
				}
				err := executor.applyResumeOptions(workflow, ResumeOptions{Mode: mode})
				wantErr := mode == ResumeModeContinue
				if (err != nil) != wantErr {
					t.Fatalf("mode %s error = %v, want error %t", mode, err, wantErr)
				}
				diagnosis := "no durable delivery evidence"
				if stepID == "command" {
					diagnosis = "unresolved launch"
				}
				if wantErr && !strings.Contains(err.Error(), diagnosis) {
					t.Fatalf("missing recovery diagnosis: %v", err)
				}
			}
		})
	}
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		Session:       "resume-session",
		InFlightSteps: map[string]InFlightStepState{"group_nested": {StepID: "group_nested", Kind: StepKindPrompt}},
	}
	if err := executor.applyResumeOptions(workflow, ResumeOptions{}); err == nil || !strings.Contains(err.Error(), "no durable delivery evidence") {
		t.Fatalf("legacy in-flight leaf without a StepResult was replayable: %v", err)
	}
}

func TestResumeResetClearsDurableWorkStateAndStepVariables(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:       "run-reset",
		WorkflowID:  "resume-reset",
		Status:      StatusRunning,
		CurrentStep: "fanout",
		Steps: map[string]StepResult{
			"top": {StepID: "top", Status: StatusCompleted, Output: "old"},
		},
		Variables: map[string]interface{}{
			"input":                "keep",
			"top_out":              "drop",
			"top_out_parsed":       map[string]interface{}{"drop": true},
			"parallel_out":         "drop",
			"loop_out":             "drop",
			"foreach_out":          "drop",
			"foreach_pane_out":     "drop",
			"steps.top.output":     "drop",
			"steps.top.parsed.foo": "drop",
		},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {StepID: "fanout", CurrentIteration: 2, Total: 4},
		},
		ParallelState: map[string]ParallelGroupState{
			"group": {StepID: "group", Total: 2, InFlightStepIDs: []string{"child"}},
		},
		ScopeStack:      []ScopeFrame{{Kind: StepKindLoop, Name: "item"}},
		InFlightSteps:   map[string]InFlightStepState{"fanout": {StepID: "fanout", Kind: "foreach"}},
		AgentDeliveries: map[string]AgentDeliveryState{"top": testAgentDelivery("top", agentDeliveryCompleted)},
		Errors:          []ExecutionError{{StepID: "top", Message: "old failure"}},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "resume-reset",
		Steps: []Step{{
			ID:        "top",
			OutputVar: "top_out",
			Parallel:  ParallelSpec{Steps: []Step{{ID: "parallel-child", OutputVar: "parallel_out"}}},
			Loop: &LoopConfig{
				Steps: []Step{{ID: "loop-child", OutputVar: "loop_out"}},
			},
			Foreach:     &ForeachConfig{Steps: []Step{{ID: "foreach-child", OutputVar: "foreach_out"}}},
			ForeachPane: &ForeachConfig{Steps: []Step{{ID: "foreach-pane-child", OutputVar: "foreach_pane_out"}}},
		}},
	}

	executor.resetResumeState(workflow)

	if executor.state.CurrentStep != "" {
		t.Fatalf("CurrentStep = %q, want empty", executor.state.CurrentStep)
	}
	if len(executor.state.Steps) != 0 {
		t.Fatalf("Steps = %#v, want empty map", executor.state.Steps)
	}
	if executor.state.AgentDeliveries != nil {
		t.Fatalf("agent deliveries survived reset: %#v", executor.state.AgentDeliveries)
	}
	if executor.state.ForeachState != nil || executor.state.ParallelState != nil || executor.state.ScopeStack != nil || executor.state.InFlightSteps != nil || executor.state.Errors != nil {
		t.Fatalf("resume bookkeeping not cleared: foreach=%#v parallel=%#v scopes=%#v in_flight=%#v errors=%#v",
			executor.state.ForeachState, executor.state.ParallelState, executor.state.ScopeStack, executor.state.InFlightSteps, executor.state.Errors)
	}
	if got := executor.state.Variables["input"]; got != "keep" {
		t.Fatalf("input variable = %#v, want preserved", got)
	}
	for _, key := range []string{
		"top_out", "top_out_parsed", "parallel_out", "loop_out",
		"foreach_out", "foreach_pane_out", "steps.top.output", "steps.top.parsed.foo",
	} {
		if _, ok := executor.state.Variables[key]; ok {
			t.Fatalf("variable %q survived reset: %#v", key, executor.state.Variables)
		}
	}
}

func TestForceResumeIterationPrunesFutureIterationState(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-force",
		WorkflowID: "resume-force",
		Status:     StatusRunning,
		Steps: map[string]StepResult{
			"fanout_iter0":      {StepID: "fanout_iter0", Status: StatusCompleted},
			"fanout_iter1":      {StepID: "fanout_iter1", Status: StatusCompleted},
			"fanout_iter2":      {StepID: "fanout_iter2", Status: StatusCompleted},
			"fanout_iter3_work": {StepID: "fanout_iter3_work", Status: StatusCompleted},
			"other_iter9":       {StepID: "other_iter9", Status: StatusCompleted},
		},
		AgentDeliveries: map[string]AgentDeliveryState{
			"fanout_iter0":      testAgentDelivery("fanout_iter0", agentDeliveryCompleted),
			"fanout_iter2":      testAgentDelivery("fanout_iter2", agentDeliveryCompleted),
			"fanout_iter4_work": testAgentDelivery("fanout_iter4_work", agentDeliverySending),
			"other_iter9":       testAgentDelivery("other_iter9", agentDeliveryDelivered),
		},
		// bd-a3fwf: seed flat substitution keys for both pre-pivot iterations
		// (must survive) and post-pivot iterations (must be scrubbed).
		Variables: map[string]interface{}{
			"steps.fanout_iter0.output":      "keep-prev",
			"steps.fanout_iter1.output":      "keep-prev",
			"steps.fanout_iter2.output":      "ghost",
			"steps.fanout_iter2.data":        map[string]interface{}{"v": 2},
			"steps.fanout_iter3_work.output": "ghost",
			"steps.fanout_iter3_work.data":   "ghost-data",
			"steps.other_iter9.output":       "unrelated-step-keep",
		},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:                "fanout",
				CurrentIteration:      4,
				Total:                 5,
				CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3", "other_iter0", "fanout_iterx"},
			},
		},
	}

	executor.forceResumeIteration("fanout", 2)
	for _, id := range []string{"fanout_iter0", "fanout_iter2", "fanout_iter4_work", "other_iter9"} {
		_, exists := executor.loadAgentDelivery(id)
		want := id == "fanout_iter0" || id == "other_iter9"
		if exists != want {
			t.Fatalf("receipt %q present = %t after force iteration, want %t", id, exists, want)
		}
	}

	state := executor.state.ForeachState["fanout"]
	if state.CurrentIteration != 2 {
		t.Fatalf("CurrentIteration = %d, want 2", state.CurrentIteration)
	}
	wantCompleted := []string{"fanout_iter0", "fanout_iter1"}
	if !reflect.DeepEqual(state.CompletedIterationIDs, wantCompleted) {
		t.Fatalf("CompletedIterationIDs = %#v, want %#v", state.CompletedIterationIDs, wantCompleted)
	}
	for _, removed := range []string{"fanout_iter2", "fanout_iter3_work"} {
		if _, ok := executor.state.Steps[removed]; ok {
			t.Fatalf("future step %q survived force iteration: %#v", removed, executor.state.Steps)
		}
	}
	for _, kept := range []string{"fanout_iter0", "fanout_iter1", "other_iter9"} {
		if _, ok := executor.state.Steps[kept]; !ok {
			t.Fatalf("step %q was removed unexpectedly: %#v", kept, executor.state.Steps)
		}
	}
	// bd-a3fwf: ghost variables for pruned iterations must be scrubbed so a
	// forced rerun does not resolve through stale future-iteration outputs.
	for _, ghost := range []string{
		"steps.fanout_iter2.output",
		"steps.fanout_iter2.data",
		"steps.fanout_iter3_work.output",
		"steps.fanout_iter3_work.data",
	} {
		if _, ok := executor.state.Variables[ghost]; ok {
			t.Fatalf("ghost variable %q survived force iteration: %#v", ghost, executor.state.Variables)
		}
	}
	for _, kept := range []string{
		"steps.fanout_iter0.output",
		"steps.fanout_iter1.output",
		"steps.other_iter9.output",
	} {
		if _, ok := executor.state.Variables[kept]; !ok {
			t.Fatalf("variable %q was scrubbed unexpectedly: %#v", kept, executor.state.Variables)
		}
	}
}

func TestResumeProgressBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-bookkeeping",
		WorkflowID: "resume-bookkeeping",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.markStepInFlight("fanout", "foreach", 3)
	if got := executor.state.InFlightSteps["fanout"]; got.StepID != "fanout" || got.Kind != "foreach" || got.Iteration != 3 {
		t.Fatalf("in-flight state = %#v, want fanout foreach iteration 3", got)
	}
	executor.clearStepInFlight("fanout")
	if executor.state.InFlightSteps != nil {
		t.Fatalf("InFlightSteps = %#v, want nil after clearing last entry", executor.state.InFlightSteps)
	}

	start := executor.beginForeachState("fanout", 4)
	if start != 0 {
		t.Fatalf("initial foreach start = %d, want 0", start)
	}
	executor.markForeachIterationCompleted("fanout", 0, 4)
	executor.markForeachIterationCompleted("fanout", 2, 4)
	// bd-p12ti: completed=[iter0, iter2] has a gap at iteration 1.
	// markForeachIterationCompleted(2) bumps CurrentIteration to 3, but the
	// durable completed set is authoritative — beginForeachState must start
	// at the first gap (1), not jump past it to the cursor.
	start = executor.beginForeachState("fanout", 4)
	if start != 1 {
		t.Fatalf("gap-aware foreach start = %d, want 1 (skip past gap is unsafe)", start)
	}
	if got := executor.state.ForeachState["fanout"].CurrentIteration; got != 1 {
		t.Fatalf("CurrentIteration after gap-aware begin = %d, want 1", got)
	}
	// bd-p12ti regression guard: with no gaps and CurrentIteration ahead,
	// resume legitimately starts at CurrentIteration.
	executor.markForeachIterationCompleted("fanout", 1, 4)
	start = executor.beginForeachState("fanout", 4)
	if start != 3 {
		t.Fatalf("contiguous-complete foreach start = %d, want 3", start)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter2"}, "fanout", 4); got != 1 {
		t.Fatalf("first gap incomplete iteration = %d, want 1", got)
	}
	if got := firstIncompleteIteration([]string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3"}, "fanout", 4); got != 4 {
		t.Fatalf("all-complete first incomplete = %d, want 4", got)
	}

	if iterationSucceeded([]StepResult{{Status: StatusCompleted}, {Status: StatusSkipped}}, false) != true {
		t.Fatal("completed/skipped iteration was not treated as successful")
	}
	if iterationSucceeded([]StepResult{{Status: StatusCompleted}}, true) != false {
		t.Fatal("break-controlled iteration was treated as successful")
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldCompleteForeachIteration(cancelledCtx, nil, false, false) {
		t.Fatal("empty cancelled iteration was treated as completed")
	}
	// bd-vq8bc: late cancellation after every nested step ran
	// (completedAllSteps=true) is still safe to checkpoint.
	if !shouldCompleteForeachIteration(cancelledCtx, []StepResult{{Status: StatusCompleted}}, false, true) {
		t.Fatal("completed iteration was not checkpointed after late cancellation")
	}
	// bd-vq8bc: mid-iteration cancellation after a successful body step
	// must NOT mark the iteration complete; resume needs to rerun the
	// remaining body steps. The previous len(results)>0 heuristic
	// returned true for this case and silently dropped work.
	if shouldCompleteForeachIteration(cancelledCtx, []StepResult{{Status: StatusCompleted}}, false, false) {
		t.Fatal("partial cancelled iteration was treated as complete (bd-vq8bc regression)")
	}
	// Clean (non-cancelled) ctx with an intentional break/continue keeps
	// existing semantics — break is rejected via iterationSucceeded.
	if shouldCompleteForeachIteration(context.Background(), []StepResult{{Status: StatusCompleted}}, true, false) {
		t.Fatal("break-controlled iteration treated as complete")
	}
	for _, status := range []ExecutionStatus{StatusFailed, StatusCancelled, StatusRunning, StatusPending} {
		if iterationSucceeded([]StepResult{{Status: status}}, false) {
			t.Fatalf("iteration status %s was treated as successful", status)
		}
	}
}

func TestResumeParallelAndScopeBookkeepingHelpers(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("resume-session"))
	executor.state = &ExecutionState{
		RunID:      "run-parallel",
		WorkflowID: "resume-parallel",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{},
	}

	executor.beginParallelState("group", 3)
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "a")
	executor.markParallelSubstepStarted("group", "b")
	executor.markParallelSubstepFinished("group", "a", StatusCompleted)
	executor.markParallelSubstepFinished("group", "b", StatusFailed)
	executor.completeParallelState("group")

	group := executor.state.ParallelState["group"]
	if !group.AllSubstepsSettled || group.CompletedAt.IsZero() {
		t.Fatalf("parallel group was not completed: %#v", group)
	}
	if !reflect.DeepEqual(group.CompletedStepIDs, []string{"a"}) {
		t.Fatalf("CompletedStepIDs = %#v, want [a]", group.CompletedStepIDs)
	}
	if !reflect.DeepEqual(group.FailedStepIDs, []string{"b"}) {
		t.Fatalf("FailedStepIDs = %#v, want [b]", group.FailedStepIDs)
	}
	if len(group.InFlightStepIDs) != 0 {
		t.Fatalf("InFlightStepIDs = %#v, want empty", group.InFlightStepIDs)
	}

	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindLoop, Name: "row"})
	executor.pushScopeFrameLocked(ScopeFrame{Kind: StepKindParallel, Name: "group"})
	executor.popScopeFrameLocked()
	if got := executor.state.ScopeStack; len(got) != 1 || got[0].Name != "row" {
		t.Fatalf("ScopeStack after pop = %#v, want only row frame", got)
	}
	executor.popScopeFrameLocked()
	executor.popScopeFrameLocked()
	if len(executor.state.ScopeStack) != 0 {
		t.Fatalf("ScopeStack = %#v, want empty", executor.state.ScopeStack)
	}
}
