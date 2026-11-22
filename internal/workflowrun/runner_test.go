package workflowrun

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func testSingleStageTemplate() *workflow.WorkflowTemplate {
	return &workflow.WorkflowTemplate{
		Name:         "unit-single-stage",
		Source:       "builtin",
		Coordination: workflow.CoordPingPong,
		Agents: []workflow.WorkflowAgent{
			{Profile: "claude", Role: "builder", Count: 1},
		},
		Flow: &workflow.FlowConfig{
			Initial: "builder",
			Stages:  []string{"builder", "done"},
			Transitions: []workflow.Transition{
				{
					From:    "builder",
					To:      "done",
					Trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "finish"},
				},
			},
		},
	}
}

func TestNewRunnerValidationAndDefaults(t *testing.T) {
	t.Parallel()
	tmpl := testSingleStageTemplate()
	agents := []workflow.CoordinatorAgent{
		{ID: "%1", Role: "builder"},
	}
	ports := Ports{
		Dispatch: func(context.Context, string, string, string) error { return nil },
		Capture:  func(context.Context, string, int) (string, error) { return "", nil },
	}

	if _, err := NewRunner(tmpl, agents, Options{
		Session:     "demo",
		ProjectRoot: t.TempDir(),
		Resume:      true,
		Restart:     true,
		StateDir:    t.TempDir(),
	}, ports); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected resume/restart conflict error, got %v", err)
	}

	if _, err := NewRunner(tmpl, agents, Options{
		Session:     "demo",
		ProjectRoot: t.TempDir(),
		Resume:      true,
	}, ports); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("expected missing state directory error, got %v", err)
	}

	if _, err := NewRunner(tmpl, agents, Options{
		Session:     "demo",
		ProjectRoot: t.TempDir(),
	}, Ports{}); err == nil || !strings.Contains(err.Error(), "dispatch and capture ports") {
		t.Fatalf("expected missing ports error, got %v", err)
	}

	runner, err := NewRunner(tmpl, agents, Options{
		Session:     "demo",
		ProjectRoot: t.TempDir(),
		StateDir:    t.TempDir(),
	}, ports)
	if err != nil {
		t.Fatalf("NewRunner valid config failed: %v", err)
	}
	if runner.Opts.TmuxEndpoint != "local" {
		t.Fatalf("default TmuxEndpoint = %q, want local", runner.Opts.TmuxEndpoint)
	}
	if runner.StageRole("builder") != "builder" {
		t.Fatalf("StageRole(builder) = %q, want builder", runner.StageRole("builder"))
	}
	if StageLabel("builder") == "" {
		t.Fatal("StageLabel(builder) returned empty string")
	}
	if hint := TriggerHint(workflow.Transition{Trigger: workflow.Trigger{Type: workflow.TriggerManual}}); !strings.Contains(hint, "manual") {
		t.Fatalf("TriggerHint(manual) = %q, want mention of manual", hint)
	}
}

func TestRunnerExecutesManualStageToCompletion(t *testing.T) {
	t.Parallel()
	tmpl := testSingleStageTemplate()
	agents := []workflow.CoordinatorAgent{
		{ID: "%1", Role: "builder"},
	}

	var dispatched []string
	runner, err := NewRunner(tmpl, agents, Options{
		Session:        "demo",
		ProjectRoot:    t.TempDir(),
		StateDir:       t.TempDir(),
		Interval:       5 * time.Millisecond,
		MaxTransitions: 4,
		FireManual:     true,
		PanePIDs:       map[string]int{"%1": 12345},
		PaneTypes:      map[string]string{"%1": "claude"},
	}, Ports{
		Dispatch: func(_ context.Context, session, paneID, prompt string) error {
			dispatched = append(dispatched, session+":"+paneID+":"+prompt)
			return nil
		},
		Capture: func(context.Context, string, int) (string, error) {
			return "ready", nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Runner.Run failed: %v (result=%+v)", err, res)
	}
	if !res.Success || !res.Completed {
		t.Fatalf("expected completed success result, got %+v", res)
	}
	if res.Transitions < 1 {
		t.Fatalf("expected at least 1 transition, got %d", res.Transitions)
	}
	if len(dispatched) == 0 {
		t.Fatal("expected initial stage prompt dispatch")
	}
}
