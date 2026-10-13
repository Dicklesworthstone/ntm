//go:build unix

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func branchLeafMailWorkflowStep() Step {
	return Step{ID: "route", Branch: "chosen", Branches: map[string]interface{}{
		"chosen": []Step{
			{ID: "notify", MailSend: validMailSendStep(), OutputVar: "sent", OutputParse: OutputParse{Type: "json"}},
			{ID: "gate", Command: "test -f release && printf '%s' '${vars.sent_parsed.thread_id}'"},
		},
	}}
}

func TestBranchLeafRetryAdoptsMailAndRestoresParsedOutput(t *testing.T) {
	server, requests := newAgentMailRuntimeFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	step := branchLeafMailWorkflowStep()
	e, workflow, cfg := decisionTestExecutor(t, step)
	e.state.Variables["sent"] = "outer"
	e.state.Variables["sent_parsed"] = map[string]interface{}{"outer": true}

	first := e.executeBranch(context.Background(), &step, workflow)
	if first.Status != StatusFailed || len(requests()) != 1 {
		t.Fatalf("first attempt: %+v requests=%+v", first, requests())
	}
	original := e.state.Steps["route_notify"]
	if original.Status != StatusCompleted || original.ParsedData == nil {
		t.Fatalf("missing completed mail evidence: %+v", original)
	}
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	second := e.executeBranch(context.Background(), &step, workflow)
	if second.Status != StatusCompleted || e.state.Steps["route_gate"].Output != "bd-b5l8d" {
		t.Fatalf("retry lost the cached parsed output: %+v gate=%+v", second, e.state.Steps["route_gate"])
	}
	if len(requests()) != 1 {
		t.Fatalf("retry repeated a completed message: %+v", requests())
	}
	if !reflect.DeepEqual(e.state.Steps["route_notify"], original) {
		t.Fatalf("adoption replaced original result metadata: %+v", e.state.Steps["route_notify"])
	}
	if e.state.Variables["sent"] != "outer" || !reflect.DeepEqual(e.state.Variables["sent_parsed"], map[string]interface{}{"outer": true}) {
		t.Fatalf("retry leaked branch-local output: %+v", e.state.Variables)
	}
}

func TestBranchLeafControlledResumeDoesNotRepeatCompletedMessage(t *testing.T) {
	server, requests := newAgentMailRuntimeFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	_, workflow, cfg := decisionTestExecutor(t, branchLeafMailWorkflowStep())
	first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || first == nil || first.Status != StatusFailed || len(requests()) != 1 {
		t.Fatalf("first controlled attempt: %+v %v requests=%+v", first, err, requests())
	}
	original := first.Steps["route_notify"]
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("resume definition: %v %+v", err, valid)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("controlled resume: %+v %v", resumed, err)
	}
	if len(requests()) != 1 || resumed.Steps["route_gate"].Output != "bd-b5l8d" {
		t.Fatalf("resume repeated mail or lost its output: requests=%+v gate=%+v", requests(), resumed.Steps["route_gate"])
	}
	got := resumed.Steps["route_notify"]
	if got.Output != original.Output || !got.StartedAt.Equal(original.StartedAt) || !got.FinishedAt.Equal(original.FinishedAt) || got.Attempts != original.Attempts {
		t.Fatalf("resume replaced the original completion: before=%+v after=%+v", original, got)
	}
}

func TestBranchLeafRetryRetainsWhenDecision(t *testing.T) {
	for _, initiallyEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "skipped", true: "completed"}[initiallyEnabled], func(t *testing.T) {
			t.Setenv("NTM_BRANCH_LEAF_ENABLED", map[bool]string{false: "false", true: "true"}[initiallyEnabled])
			step := Step{ID: "route", Branch: "chosen", Branches: map[string]interface{}{
				"chosen": []Step{
					{ID: "conditional", When: "${env.NTM_BRANCH_LEAF_ENABLED}", Command: "printf x >> condition-effects; printf original", OutputVar: "local"},
					{ID: "gate", Command: "test -f release"},
				},
			}}
			e, workflow, cfg := decisionTestExecutor(t, step)
			first := e.executeBranch(context.Background(), &step, workflow)
			if first.Status != StatusFailed {
				t.Fatalf("first attempt should stop at the gate: %+v", first)
			}
			original := e.state.Steps["route_conditional"]
			wantStatus := StatusSkipped
			if initiallyEnabled {
				wantStatus = StatusCompleted
			}
			if original.Status != wantStatus {
				t.Fatalf("initial condition: %+v", original)
			}
			t.Setenv("NTM_BRANCH_LEAF_ENABLED", map[bool]string{false: "true", true: "false"}[initiallyEnabled])
			decisionTestWrite(t, cfg.ProjectDir, "release", "go")
			second := e.executeBranch(context.Background(), &step, workflow)
			if second.Status != StatusCompleted || !reflect.DeepEqual(e.state.Steps["route_conditional"], original) {
				t.Fatalf("retry reevaluated a finished child's condition: %+v child=%+v", second, e.state.Steps["route_conditional"])
			}
			data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "condition-effects"))
			if initiallyEnabled {
				if err != nil || string(data) != "x" {
					t.Fatalf("completed effect changed: %q %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("previously skipped work was dispatched: %q %v", data, err)
			}
		})
	}
}

func TestBranchLeafUnfinishedResultsStillUseLifecycle(t *testing.T) {
	for _, status := range []ExecutionStatus{StatusPending, StatusRunning, StatusFailed, StatusCancelled, StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			step := Step{ID: "route", Branch: "chosen", Branches: map[string]interface{}{
				"chosen": []Step{{ID: "work", Command: "printf ran > dispatched; printf recovered"}},
			}}
			e, workflow, cfg := decisionTestExecutor(t, step)
			if _, _, err := e.prepareBranchDecision(context.Background(), &step); err != nil {
				t.Fatal(err)
			}
			e.state.Steps["route_work"] = StepResult{StepID: "route_work", Status: status, RerunOnResume: status == StatusCompleted}
			result := e.executeBranch(context.Background(), &step, workflow)
			if result.Status != StatusCompleted || e.state.Steps["route_work"].Output != "recovered" {
				t.Fatalf("unfinished or explicitly rerunnable work was adopted: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "dispatched"))
			if err != nil || string(data) != "ran" {
				t.Fatalf("lifecycle did not execute: %q %v", data, err)
			}
		})
	}
}

func TestBranchLeafRejectsMismatchedSavedIdentity(t *testing.T) {
	step := Step{ID: "route", Branch: "chosen", Branches: map[string]interface{}{
		"chosen": []Step{{ID: "work", Command: "printf unsafe > dispatched"}},
	}}
	e, workflow, cfg := decisionTestExecutor(t, step)
	if _, _, err := e.prepareBranchDecision(context.Background(), &step); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.state.Steps["route_work"] = StepResult{StepID: "somebody_else", Status: StatusCompleted, StartedAt: now, FinishedAt: now}
	result := e.executeBranch(context.Background(), &step, workflow)
	if result.Status != StatusFailed || result.Error == nil || !strings.Contains(result.Error.Message, "mismatched saved result identity") {
		t.Fatalf("accepted ambiguous ownership: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "dispatched")); !os.IsNotExist(err) {
		t.Fatalf("ambiguous result caused dispatch: %v", err)
	}
}
