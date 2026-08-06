package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoordinatorTransitionsAndRoutesTasks(t *testing.T) {
	template := &WorkflowTemplate{
		Name: "review-flow", Coordination: CoordPingPong,
		Agents:  []WorkflowAgent{{Profile: "cod", Role: "author"}, {Profile: "cc", Role: "reviewer", Count: 2}},
		Routing: map[string]string{"docs/*": "reviewer"},
		Flow:    &FlowConfig{Initial: "author", Transitions: []Transition{{From: "author", To: "reviewer", Trigger: Trigger{Type: TriggerManual, Label: "review"}}}},
	}
	coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "a", Role: "author"}, {ID: "r1", Role: "reviewer"}, {ID: "r2", Role: "reviewer"}}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	if err := coordinator.Start(&TriggerContext{Context: context.Background()}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Stop() })
	if got := coordinator.CurrentStage(); got != "author" {
		t.Fatalf("CurrentStage() = %q", got)
	}
	agent, err := coordinator.GetAgentForTask(Task{Path: "docs/readme.md"})
	if err != nil || agent.ID != "r1" {
		t.Fatalf("routed agent = (%+v, %v)", agent, err)
	}
	if err := coordinator.Transition("review"); err != nil {
		t.Fatalf("Transition(): %v", err)
	}
	if got := coordinator.CurrentStage(); got != "reviewer" {
		t.Fatalf("CurrentStage after transition = %q", got)
	}
}

func TestReviewGateApprovalModes(t *testing.T) {
	template := &WorkflowTemplate{
		Name: "review-gate", Coordination: CoordReviewGate,
		Agents: []WorkflowAgent{{Profile: "cod", Role: "author"}, {Profile: "cc", Role: "reviewer", Count: 2}},
		Flow:   &FlowConfig{Initial: "review", RequireApproval: true, ApprovalMode: "all", Transitions: []Transition{{From: "review", To: "complete", Trigger: Trigger{Type: TriggerManual}}}},
	}
	coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "a", Role: "author"}, {ID: "r1", Role: "reviewer"}, {ID: "r2", Role: "reviewer"}}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	review, ok := coordinator.(*ReviewGateCoordinator)
	if !ok {
		t.Fatalf("coordinator type = %T", coordinator)
	}
	if err := review.Start(&TriggerContext{Context: context.Background()}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = review.Stop() })
	for _, agentID := range []string{"a", "not-a-reviewer"} {
		if approved, err := review.Approve(agentID); err == nil || approved {
			t.Fatalf("Approve(%q) = (%v, %v), want non-reviewer rejection", agentID, approved, err)
		}
	}
	if approved, err := review.Approve("r1"); err != nil || approved {
		t.Fatalf("first approval = (%v, %v)", approved, err)
	}
	if approved, err := review.Approve("r2"); err != nil || !approved {
		t.Fatalf("second approval = (%v, %v)", approved, err)
	}
}

func TestCoordinatorStartsFileTransitionWithProjectRoot(t *testing.T) {
	projectRoot := t.TempDir()
	template := &WorkflowTemplate{
		Name:         "file-transition",
		Coordination: CoordPipeline,
		Agents:       []WorkflowAgent{{Profile: "cod", Role: "watch"}},
		Flow: &FlowConfig{
			Initial: "prepare",
			Stages:  []string{"prepare", "watch", "complete"},
			Transitions: []Transition{
				{From: "prepare", To: "watch", Trigger: Trigger{Type: TriggerManual, Label: "begin-watching"}},
				{From: "watch", To: "complete", Trigger: Trigger{Type: TriggerFileCreated, Pattern: "*.done"}},
			},
		},
	}
	coordinator, err := NewCoordinator(template, nil, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	ctx := &TriggerContext{Context: context.Background(), ProjectRoot: projectRoot}
	if err := coordinator.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Stop() })
	if err := coordinator.Transition("begin-watching"); err != nil {
		t.Fatalf("Transition(): %v", err)
	}
	if got := coordinator.CurrentStage(); got != "watch" {
		t.Fatalf("CurrentStage() after manual transition = %q, want watch", got)
	}

	if err := os.WriteFile(filepath.Join(projectRoot, "ready.done"), []byte("ready\n"), 0o644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		transitioned, err := coordinator.(*PipelineCoordinator).Evaluate(ctx)
		if err != nil {
			t.Fatalf("Evaluate(): %v", err)
		}
		if transitioned {
			if got := coordinator.CurrentStage(); got != "complete" {
				t.Fatalf("CurrentStage() = %q, want complete", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("file transition did not fire")
}

func TestCoordinatorRejectsInvalidLifecycle(t *testing.T) {
	template := &WorkflowTemplate{Name: "manual", Coordination: CoordPingPong, Agents: []WorkflowAgent{{Profile: "cod", Role: "one"}}, Flow: &FlowConfig{Initial: "one", Transitions: []Transition{{From: "one", To: "two", Trigger: Trigger{Type: TriggerManual}}}}}
	coordinator, err := NewCoordinator(template, nil, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	if err := coordinator.Transition("manual"); err == nil {
		t.Fatal("Transition before Start succeeded")
	}
	if _, err := coordinator.GetAgentForTask(Task{}); err == nil {
		t.Fatal("GetAgentForTask without agents succeeded")
	}
}

func TestCoordinatorRestoresSourceStageWhenDestinationTriggerFailsToStart(t *testing.T) {
	const transitionLabel = "advance"
	destinationErr := errors.New("destination trigger unavailable")
	template := &WorkflowTemplate{
		Name:         "rollback-transition",
		Coordination: CoordPingPong,
		Agents:       []WorkflowAgent{{Profile: "cod", Role: "source"}},
		Flow: &FlowConfig{
			Initial: "source",
			Transitions: []Transition{
				{From: "source", To: "destination", Trigger: Trigger{Type: TriggerManual, Label: transitionLabel}},
				{From: "destination", To: "done", Trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 1}},
			},
		},
	}
	registry := NewTriggerRegistry()
	registry.Register(TriggerTimeElapsed, func(Trigger) (RuntimeTrigger, error) {
		return nil, destinationErr
	})
	coordinator, err := NewCoordinator(template, nil, registry)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	if err := coordinator.Start(&TriggerContext{Context: context.Background()}); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Stop() })

	err = coordinator.Transition(transitionLabel)
	if !errors.Is(err, destinationErr) {
		t.Fatalf("Transition() error = %v, want destination error", err)
	}
	if got := coordinator.CurrentStage(); got != "source" {
		t.Fatalf("CurrentStage() after failed transition = %q, want source", got)
	}

	// A second attempt must find the source transition again instead of the
	// pre-fix state where the coordinator stayed started with no triggers.
	err = coordinator.Transition(transitionLabel)
	if !errors.Is(err, destinationErr) {
		t.Fatalf("second Transition() error = %v, want destination error after source-stage restore", err)
	}
}

func TestParallelCoordinatorStartsWithoutFlow(t *testing.T) {
	template := &WorkflowTemplate{
		Name:         "parallel-without-flow",
		Coordination: CoordParallel,
		Agents: []WorkflowAgent{
			{Profile: "cod", Role: "research"},
			{Profile: "cc", Role: "review"},
		},
	}
	coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "a", Role: "research"}, {ID: "b", Role: "review"}}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	parallel, ok := coordinator.(*ParallelCoordinator)
	if !ok {
		t.Fatalf("coordinator type = %T, want *ParallelCoordinator", coordinator)
	}
	if err := parallel.Start(&TriggerContext{Context: context.Background()}); err != nil {
		t.Fatalf("Start() flowless parallel coordinator: %v", err)
	}
	t.Cleanup(func() { _ = parallel.Stop() })

	if got := parallel.Agents(); len(got) != 2 {
		t.Fatalf("Agents() returned %d agents, want 2", len(got))
	}
	if transitioned, err := parallel.Evaluate(&TriggerContext{}); err != nil || transitioned {
		t.Fatalf("Evaluate() = (%v, %v), want (false, nil) for flowless parallel workflow", transitioned, err)
	}
}
