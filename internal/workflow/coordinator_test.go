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
	for _, tc := range []struct {
		name     string
		mode     string
		quorum   int
		required int
	}{
		{name: "default", required: 1},
		{name: "any", mode: "any", required: 1},
		{name: "all", mode: "all", required: 3},
		{name: "quorum", mode: "quorum", quorum: 2, required: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := &WorkflowTemplate{
				Name: "review-gate", Coordination: CoordReviewGate,
				Agents: []WorkflowAgent{{Profile: "cod", Role: "author"}, {Profile: "cc", Role: "reviewer", Count: 3}},
				Flow: &FlowConfig{
					Initial: "review", RequireApproval: true, ApprovalMode: tc.mode, Quorum: tc.quorum,
					Transitions: []Transition{{From: "review", To: "complete", Trigger: Trigger{Type: TriggerAgentSays, Role: "reviewer", Pattern: "APPROVED"}}},
				},
			}
			coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "a", Role: "author"}, {ID: "r1", Role: "reviewer"}, {ID: "r2", Role: "reviewer"}, {ID: "r3", Role: "reviewer"}}, nil)
			if err != nil {
				t.Fatalf("NewCoordinator(): %v", err)
			}
			review := coordinator.(*ReviewGateCoordinator)
			if err := review.Start(&TriggerContext{Context: context.Background()}); err != nil {
				t.Fatalf("Start(): %v", err)
			}
			t.Cleanup(func() { _ = review.Stop() })
			for _, agentIDs := range [][]string{{"a"}, {"not-a-reviewer"}, {""}, {" "}, {"r1", "r2", "r3", "a"}} {
				if approved, err := review.CheckApprovals(0, agentIDs); err == nil || approved {
					t.Fatalf("CheckApprovals(0, %q) = (%v, %v), want invalid approver rejection", agentIDs, approved, err)
				}
			}
			for _, agentIDs := range [][]string{nil, {"r1"}, {"r2"}, {"r1", "r1"}, {"r1", "r2"}, {"r1", "r2", "r3"}, nil} {
				count := len(agentIDs)
				if count == 2 && agentIDs[0] == agentIDs[1] {
					count = 1
				}
				wantApproved := count >= tc.required
				if approved, err := review.CheckApprovals(0, agentIDs); err != nil || approved != wantApproved {
					t.Fatalf("CheckApprovals(0, %q) = (%v, %v), want (%v, nil)", agentIDs, approved, err, wantApproved)
				}
			}
		})
	}
}

func TestReviewGateKeepsTransitionEvidenceSeparate(t *testing.T) {
	template := &WorkflowTemplate{
		Name: "separate-review-gates", Coordination: CoordReviewGate,
		Agents: []WorkflowAgent{{Profile: "cod", Role: "author"}, {Profile: "cc", Role: "reviewer", Count: 2}},
		Flow: &FlowConfig{
			Initial: "prepare", RequireApproval: true, ApprovalMode: "all",
			Transitions: []Transition{
				{From: "prepare", To: "review", Trigger: Trigger{Type: TriggerManual, Label: "submit"}},
				{From: "review", To: "revise", Trigger: Trigger{Type: TriggerAgentSays, Role: "reviewer", Pattern: "CHANGES REQUESTED"}},
				{From: "review", To: "complete", Trigger: Trigger{Type: TriggerAgentSays, Role: "reviewer", Pattern: "APPROVED"}},
				{From: "review", To: "revise", Trigger: Trigger{Type: TriggerAgentSays, Role: "author", Pattern: "WITHDRAW"}},
				{From: "revise", To: "review", Trigger: Trigger{Type: TriggerManual, Label: "resubmit"}},
			},
		},
	}
	coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "a", Role: "author"}, {ID: "r1", Role: "reviewer"}, {ID: "r2", Role: "reviewer"}}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	review := coordinator.(*ReviewGateCoordinator)
	if approved, err := review.CheckApprovals(1, []string{"r1", "r2"}); err == nil || approved {
		t.Fatalf("CheckApprovals before Start = (%v, %v), want rejection", approved, err)
	}
	ctx := &TriggerContext{Context: context.Background()}
	if err := review.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = review.Stop() })
	for _, transitionIndex := range []int{-1, 0, 1, len(template.Flow.Transitions)} {
		if approved, err := review.CheckApprovals(transitionIndex, []string{"r1", "r2"}); err == nil || approved {
			t.Fatalf("CheckApprovals(%d) before review = (%v, %v), want invalid transition rejection", transitionIndex, approved, err)
		}
	}
	if err := review.Transition("submit"); err != nil {
		t.Fatalf("Transition(submit): %v", err)
	}
	for _, tc := range []struct {
		index    int
		agentIDs []string
		approved bool
	}{
		{index: 1, agentIDs: []string{"r1"}},
		{index: 2, agentIDs: []string{"r2"}},
		{index: 1, agentIDs: []string{"r1", "r2"}, approved: true},
		{index: 2, agentIDs: []string{"r2"}},
		{index: 3, agentIDs: []string{"a"}, approved: true},
		{index: 2, agentIDs: []string{"r2"}},
		{index: 2, agentIDs: []string{"r1", "r2"}, approved: true},
	} {
		if approved, err := review.CheckApprovals(tc.index, tc.agentIDs); err != nil || approved != tc.approved {
			t.Fatalf("CheckApprovals(%d, %q) = (%v, %v), want (%v, nil)", tc.index, tc.agentIDs, approved, err, tc.approved)
		}
	}
	if approved, err := review.CheckApprovals(3, []string{"r1"}); err == nil || approved {
		t.Fatalf("CheckApprovals for wrong transition role = (%v, %v), want rejection", approved, err)
	}
	if transitioned, err := review.Evaluate(&TriggerContext{Context: context.Background(), Outputs: []AgentOutput{{Role: "reviewer", Text: "CHANGES REQUESTED"}}}); err != nil || !transitioned {
		t.Fatalf("Evaluate(changes requested) = (%v, %v)", transitioned, err)
	}
	if approved, err := review.CheckApprovals(1, []string{"r1", "r2"}); err == nil || approved {
		t.Fatalf("CheckApprovals after leaving review = (%v, %v), want stale transition rejection", approved, err)
	}
	if err := review.Transition("resubmit"); err != nil {
		t.Fatalf("Transition(resubmit): %v", err)
	}
	for _, agentIDs := range [][]string{nil, {"r1"}, {"r2"}} {
		if approved, err := review.CheckApprovals(2, agentIDs); err != nil || approved {
			t.Fatalf("CheckApprovals after re-entering review with %q = (%v, %v), want fresh approval set", agentIDs, approved, err)
		}
	}
	if err := review.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	if approved, err := review.CheckApprovals(2, []string{"r1", "r2"}); err == nil || approved {
		t.Fatalf("CheckApprovals after Stop = (%v, %v), want rejection", approved, err)
	}
}

func TestReviewGateCountsDistinctEligibleAgents(t *testing.T) {
	for _, tc := range []struct {
		name            string
		role            string
		mode            string
		quorum          int
		requireApproval bool
		agents          []CoordinatorAgent
		approvals       []string
		wantApproved    bool
		wantErr         bool
	}{
		{name: "duplicate inventory", role: "reviewer", mode: "all", requireApproval: true, agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}, {ID: "r1", Role: "reviewer"}, {ID: "r2", Role: "reviewer"}}, approvals: []string{"r1", "r2"}, wantApproved: true},
		{name: "empty role counts every distinct agent", mode: "all", requireApproval: true, agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}, {ID: "a", Role: "author"}}, approvals: []string{"r1", "a"}, wantApproved: true},
		{name: "empty role requires every role", mode: "all", requireApproval: true, agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}, {ID: "a", Role: "author"}}, approvals: []string{"r1", "r1"}},
		{name: "empty role rejects unknown agent", mode: "any", requireApproval: true, agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}}, approvals: []string{"missing"}, wantErr: true},
		{name: "quorum exceeds distinct agents", role: "reviewer", mode: "quorum", quorum: 2, requireApproval: true, agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}, {ID: "r1", Role: "reviewer"}}, approvals: []string{"r1", "r1"}, wantErr: true},
		{name: "no eligible agents", role: "reviewer", mode: "all", requireApproval: true, agents: []CoordinatorAgent{{ID: "a", Role: "author"}}, wantErr: true},
		{name: "blank runtime identity", role: "reviewer", mode: "any", requireApproval: true, agents: []CoordinatorAgent{{Role: "reviewer"}}, wantErr: true},
		{name: "approval disabled", role: "reviewer", mode: "any", agents: []CoordinatorAgent{{ID: "r1", Role: "reviewer"}}, approvals: []string{"r1"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := &WorkflowTemplate{
				Name: "review-eligibility", Coordination: CoordReviewGate,
				Agents: []WorkflowAgent{{Profile: "cc", Role: "reviewer"}, {Profile: "cod", Role: "author"}},
				Flow: &FlowConfig{
					Initial: "review", RequireApproval: tc.requireApproval, ApprovalMode: tc.mode, Quorum: tc.quorum,
					Transitions: []Transition{{From: "review", To: "complete", Trigger: Trigger{Type: TriggerAgentSays, Role: tc.role, Pattern: "APPROVED"}}},
				},
			}
			coordinator, err := NewCoordinator(template, tc.agents, nil)
			if err != nil {
				t.Fatalf("NewCoordinator(): %v", err)
			}
			review := coordinator.(*ReviewGateCoordinator)
			if err := review.Start(&TriggerContext{Context: context.Background()}); err != nil {
				t.Fatalf("Start(): %v", err)
			}
			t.Cleanup(func() { _ = review.Stop() })
			approved, err := review.CheckApprovals(0, tc.approvals)
			if (err != nil) != tc.wantErr || approved != tc.wantApproved {
				t.Fatalf("CheckApprovals() = (%v, %v), want approved=%v error=%v", approved, err, tc.wantApproved, tc.wantErr)
			}
		})
	}
}

func TestCoordinatorUsesSpecificTransitionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		evidence  map[int]bool
		outputs   []AgentOutput
		wantStage string
	}{
		{name: "legacy raw output", outputs: []AgentOutput{{Role: "reviewer", Text: "APPROVED"}}, wantStage: "complete"},
		{name: "empty receipts block raw output", evidence: map[int]bool{}, outputs: []AgentOutput{{Role: "reviewer", Text: "APPROVED"}}, wantStage: "review"},
		{name: "unmet receipt blocks raw output", evidence: map[int]bool{2: false}, outputs: []AgentOutput{{Role: "reviewer", Text: "APPROVED"}}, wantStage: "review"},
		{name: "receipt needs no output replay", evidence: map[int]bool{2: true}, wantStage: "complete"},
		{name: "receipt selects specific branch", evidence: map[int]bool{2: true}, outputs: []AgentOutput{{Role: "reviewer", Text: "CHANGES REQUESTED"}}, wantStage: "complete"},
		{name: "global index cannot address another stage", evidence: map[int]bool{0: true}, wantStage: "review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := &WorkflowTemplate{
				Name: "transition-evidence", Coordination: CoordReviewGate,
				Agents: []WorkflowAgent{{Profile: "cc", Role: "reviewer"}},
				Flow: &FlowConfig{
					Initial: "review", RequireApproval: true, ApprovalMode: "any",
					Transitions: []Transition{
						{From: "prepare", To: "review", Trigger: Trigger{Type: TriggerManual, Label: "submit"}},
						{From: "review", To: "revise", Trigger: Trigger{Type: TriggerAgentSays, Role: "reviewer", Pattern: "CHANGES REQUESTED"}},
						{From: "review", To: "complete", Trigger: Trigger{Type: TriggerAgentSays, Role: "reviewer", Pattern: "APPROVED"}},
					},
				},
			}
			coordinator, err := NewCoordinator(template, []CoordinatorAgent{{ID: "r1", Role: "reviewer"}}, nil)
			if err != nil {
				t.Fatalf("NewCoordinator(): %v", err)
			}
			review := coordinator.(*ReviewGateCoordinator)
			if err := review.Start(&TriggerContext{Context: context.Background()}); err != nil {
				t.Fatalf("Start(): %v", err)
			}
			t.Cleanup(func() { _ = review.Stop() })
			transitioned, err := review.Evaluate(&TriggerContext{Context: context.Background(), Outputs: tc.outputs, TransitionEvidence: tc.evidence})
			if err != nil || transitioned != (tc.wantStage != "review") || review.CurrentStage() != tc.wantStage {
				t.Fatalf("Evaluate() = (%v, %v), stage %q, want %q", transitioned, err, review.CurrentStage(), tc.wantStage)
			}
		})
	}
}

func TestCoordinatorTransitionEvidenceDoesNotOverrideOtherTriggers(t *testing.T) {
	template := &WorkflowTemplate{
		Name: "mixed-evidence", Coordination: CoordPingPong,
		Agents: []WorkflowAgent{{Profile: "cc", Role: "reviewer"}},
		Flow: &FlowConfig{Initial: "review", Transitions: []Transition{
			{From: "review", To: "complete", Trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 1}},
		}},
	}
	coordinator, err := NewCoordinator(template, nil, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	runtime := coordinator.(*PingPongCoordinator)
	now := time.Now()
	ctx := &TriggerContext{Context: context.Background(), Now: func() time.Time { return now }, TransitionEvidence: map[int]bool{0: true}}
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = runtime.Stop() })
	if transitioned, err := runtime.Evaluate(ctx); err != nil || transitioned {
		t.Fatalf("Evaluate() before timer = (%v, %v), receipt must not fire time trigger", transitioned, err)
	}
	now = now.Add(time.Minute)
	ctx.TransitionEvidence = map[int]bool{}
	if transitioned, err := runtime.Evaluate(ctx); err != nil || !transitioned {
		t.Fatalf("Evaluate() after timer = (%v, %v), empty receipts must not block time trigger", transitioned, err)
	}
}

func TestCoordinatorRetainsIndependentTransitionEvidenceSnapshot(t *testing.T) {
	template := &WorkflowTemplate{
		Name: "evidence-snapshot", Coordination: CoordPingPong,
		Agents: []WorkflowAgent{{Profile: "cc", Role: "reviewer"}},
		Flow: &FlowConfig{Initial: "review", Transitions: []Transition{
			{From: "review", To: "revise", Trigger: Trigger{Type: TriggerAgentSays, Pattern: "CHANGES REQUESTED"}},
			{From: "review", To: "complete", Trigger: Trigger{Type: TriggerAgentSays, Pattern: "APPROVED"}},
		}},
	}
	coordinator, err := NewCoordinator(template, nil, nil)
	if err != nil {
		t.Fatalf("NewCoordinator(): %v", err)
	}
	runtime := coordinator.(*PingPongCoordinator)
	ctx := &TriggerContext{Context: context.Background(), TransitionEvidence: map[int]bool{1: true}}
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = runtime.Stop() })
	ctx.TransitionEvidence[0] = true
	ctx.TransitionEvidence[1] = false
	if transitioned, err := runtime.Evaluate(runtime.triggerCtx); err != nil || !transitioned || runtime.CurrentStage() != "complete" {
		t.Fatalf("Evaluate(saved context) = (%v, %v), stage %q: caller changed saved receipts", transitioned, err, runtime.CurrentStage())
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
