package assign

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPlanAllocationsEmptyReadyState(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "alpha", AgentType: tmux.AgentCodex, Idle: true},
		},
	})

	if allocationTestNotEqual(plan.Decision, AllocationDecisionNoReadyWork) {
		t.Fatalf("decision = %s, want %s", plan.Decision, AllocationDecisionNoReadyWork)
	}
	if len(plan.Recommendations) != 0 {
		t.Fatalf("recommendations = %d, want 0", len(plan.Recommendations))
	}
	if len(plan.Logs) != 1 || !hasReason(plan.Logs[0].ReasonCodes, AllocationReasonNoReadyBeads) {
		t.Fatalf("logs missing no-ready reason: %+v", plan.Logs)
	}
}

func TestPlanAllocationsPriorityConflict(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable:        true,
		MaxRecommendations: 1,
		Pressure:           AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		ReadyBeads: []AllocationReadyBead{
			{ID: "bd-urgent", Title: "Urgent fix", TaskType: TaskBug, Priority: 0, GraphScore: 0.60},
			{ID: "bd-graph", Title: "High graph task", TaskType: TaskBug, Priority: 3, GraphScore: 0.70},
		},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "alpha", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
		},
	})

	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if allocationTestNotEqual(got.BeadID, "bd-urgent") {
		t.Fatalf("recommended bead = %s, want bd-urgent", got.BeadID)
	}
	if !hasReason(got.ReasonCodes, AllocationReasonPriorityConflict) {
		t.Fatalf("recommendation missing priority reason: %+v", got.ReasonCodes)
	}
}

func TestPlanAllocationsResourcePressurePrefersHeadroom(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "high", Limiting: []string{"proc_count"}, AgentHeadroom: 1},
		ReadyBeads: []AllocationReadyBead{
			{ID: "bd-heavy", Title: "Heavy feature", TaskType: TaskFeature, Priority: 2, GraphScore: 0.75, ResourceCost: 0.85},
		},
		Agents: []AllocationAgent{
			{ID: "cod-low", Session: "packed", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.20},
			{ID: "cod-high", Session: "roomy", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.95},
		},
	})

	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if allocationTestNotEqual(got.AgentID, "cod-high") {
		t.Fatalf("recommended agent = %s, want cod-high", got.AgentID)
	}
	if !hasReason(got.ReasonCodes, AllocationReasonResourcePressure) {
		t.Fatalf("recommendation missing resource pressure reason: %+v", got.ReasonCodes)
	}
	if got.ScoreComponents.ResourceFit <= plan.Recommendations[0].Alternatives[0].ScoreComponents.ResourceFit {
		t.Fatalf("selected resource fit = %.3f, alternative = %.3f", got.ScoreComponents.ResourceFit, plan.Recommendations[0].Alternatives[0].ScoreComponents.ResourceFit)
	}
}

func TestPlanAllocationsAgentTypeMismatchPenalty(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		ReadyBeads: []AllocationReadyBead{
			{
				ID:                  "bd-docs",
				Title:               "Codex-only docs task",
				TaskType:            TaskDocs,
				Priority:            2,
				GraphScore:          0.70,
				PreferredAgentTypes: []tmux.AgentType{tmux.AgentCodex},
			},
		},
		Agents: []AllocationAgent{
			{ID: "gmi-1", Session: "docs", AgentType: tmux.AgentGemini, Idle: true, ResourceHeadroom: 0.90},
			{ID: "cod-1", Session: "impl", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
		},
	})

	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if allocationTestNotEqual(got.AgentType, tmux.AgentCodex) {
		t.Fatalf("recommended agent type = %s, want %s", got.AgentType, tmux.AgentCodex)
	}
	if len(got.Alternatives) == 0 || !hasReason(got.Alternatives[0].ReasonCodes, AllocationReasonAgentTypeMismatch) {
		t.Fatalf("expected mismatch reason on alternative: %+v", got.Alternatives)
	}
}

func TestPlanAllocationsCanonicalizesPreferredAgentTypeAliases(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 1},
		ReadyBeads: []AllocationReadyBead{
			{
				ID:                  "bd-codex",
				Title:               "Codex alias task",
				TaskType:            TaskBug,
				Priority:            1,
				GraphScore:          0.80,
				PreferredAgentTypes: []tmux.AgentType{tmux.AgentType("openai-codex")},
			},
		},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "impl", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
		},
	})

	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if allocationTestNotEqual(got.AgentType, tmux.AgentCodex) {
		t.Fatalf("recommended agent type = %s, want %s", got.AgentType, tmux.AgentCodex)
	}
	if hasReason(got.ReasonCodes, AllocationReasonAgentTypeMismatch) {
		t.Fatalf("canonical alias was treated as a mismatch: %+v", got.ReasonCodes)
	}
}

func TestPlanAllocationsStarvationAvoidance(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		Fairness: AllocationFairness{
			AgentRecentAssignments: map[string]int{"cod-busy": 4},
			StarvedAgents:          []string{"cod-starved"},
		},
		ReadyBeads: []AllocationReadyBead{
			{ID: "bd-next", Title: "Next feature", TaskType: TaskFeature, Priority: 2, GraphScore: 0.70},
		},
		Agents: []AllocationAgent{
			{ID: "cod-busy", Session: "alpha", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
			{ID: "cod-starved", Session: "beta", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
		},
	})

	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if allocationTestNotEqual(got.AgentID, "cod-starved") {
		t.Fatalf("recommended agent = %s, want cod-starved", got.AgentID)
	}
	if !hasReason(got.ReasonCodes, AllocationReasonStarvationRisk) {
		t.Fatalf("recommendation missing starvation reason: %+v", got.ReasonCodes)
	}
}

func TestPlanAllocationsMissingBVAndPressureDegradeCleanly(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		ReadyBeads: []AllocationReadyBead{
			{ID: "bd-missing", Title: "Needs fallback data", TaskType: TaskTask, Priority: 2},
		},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "fallback", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.80},
		},
	})

	if allocationTestNotEqual(plan.Decision, AllocationDecisionRecommend) {
		t.Fatalf("decision = %s, want %s", plan.Decision, AllocationDecisionRecommend)
	}
	if len(plan.Warnings) != 2 {
		t.Fatalf("warnings = %v, want bv and pressure warnings", plan.Warnings)
	}
	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(plan.Recommendations))
	}
	got := plan.Recommendations[0]
	if !hasReason(got.ReasonCodes, AllocationReasonBVMissing) {
		t.Fatalf("recommendation missing bv fallback reason: %+v", got.ReasonCodes)
	}
	if !hasReason(got.ReasonCodes, AllocationReasonPressureMissing) {
		t.Fatalf("recommendation missing pressure fallback reason: %+v", got.ReasonCodes)
	}
}

func TestPlanAllocationsLogsRequiredFields(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		ReadyBeads: []AllocationReadyBead{
			{ID: "bd-log", Title: "Log row", TaskType: TaskBug, Priority: 1, GraphScore: 0.80},
		},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "alpha", AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90},
		},
	})

	if len(plan.Logs) == 0 {
		t.Fatal("expected at least one log row")
	}
	row := plan.Logs[0]
	if allocationTestNotEqual(row.BeadID, "bd-log") ||
		allocationTestNotEqual(row.Session, "alpha") ||
		allocationTestNotEqual(row.AgentType, tmux.AgentCodex) {
		t.Fatalf("log row missing required identity fields: %+v", row)
	}
	if row.ScoreComponents.Total == 0 {
		t.Fatalf("log row missing score components: %+v", row.ScoreComponents)
	}
	if allocationTestEqual(row.Decision, "") || len(row.ReasonCodes) == 0 {
		t.Fatalf("log row missing decision or reasons: %+v", row)
	}
}

func allocationTestEqual[T comparable](left, right T) bool {
	return left == right
}

func allocationTestNotEqual[T comparable](left, right T) bool {
	return !allocationTestEqual(left, right)
}
