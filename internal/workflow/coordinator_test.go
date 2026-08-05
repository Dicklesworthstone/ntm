package workflow

import (
	"context"
	"testing"
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
	if err := coordinator.Start(context.Background()); err != nil {
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
	if err := review.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = review.Stop() })
	if approved, err := review.Approve("r1"); err != nil || approved {
		t.Fatalf("first approval = (%v, %v)", approved, err)
	}
	if approved, err := review.Approve("r2"); err != nil || !approved {
		t.Fatalf("second approval = (%v, %v)", approved, err)
	}
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
