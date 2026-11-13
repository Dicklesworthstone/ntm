package assign

import (
	"math"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestAllocationRelaxationRetainsHardGatesAndProvenance(t *testing.T) {
	base := AllocationInput{
		BVAvailable: true, MinScore: 0.8,
		Pressure:   AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 1},
		ReadyBeads: []AllocationReadyBead{{ID: "work", Priority: 2, TaskType: TaskFeature}},
		Agents:     []AllocationAgent{{ID: "agent", Session: "proj", Idle: true, AgentType: tmux.AgentCodex}},
	}
	strict := PlanAllocations(base)
	if len(strict.Recommendations) != 0 {
		t.Fatalf("fixture unexpectedly passed strict score threshold: %+v", strict)
	}
	base.Relax = true
	got := PlanAllocations(base)
	if len(got.Recommendations) != 1 {
		t.Fatalf("opt-in score fallback did not place work: %+v", got)
	}
	recommendation := got.Recommendations[0]
	receipt := recommendation.Relaxation
	if receipt == nil || receipt.OriginalThreshold != 0.8 || receipt.RelaxedThreshold != 0.4 ||
		receipt.OriginalScore != recommendation.Score || receipt.RelaxedScore != recommendation.Score ||
		!reflect.DeepEqual(receipt.RelaxedConstraints, []string{"minimum_assignment_score"}) {
		t.Fatalf("missing exact score relaxation provenance: %+v", recommendation)
	}
	for _, tc := range []struct {
		name  string
		alter func(*AllocationInput)
	}{
		{"busy", func(in *AllocationInput) { in.Agents[0].Idle = false }},
		{"context", func(in *AllocationInput) { in.Agents[0].ContextUsage = 0.95 }},
		{"negative context", func(in *AllocationInput) { in.Agents[0].ContextUsage = -0.1 }},
		{"NaN context", func(in *AllocationInput) { in.Agents[0].ContextUsage = math.NaN() }},
		{"infinite context", func(in *AllocationInput) { in.Agents[0].ContextUsage = math.Inf(1) }},
		{"negative infinite context", func(in *AllocationInput) { in.Agents[0].ContextUsage = math.Inf(-1) }},
		{"agent capacity", func(in *AllocationInput) { in.Agents[0].AssignmentLimit, in.Agents[0].ActiveAssignments = 1, 1 }},
		{"session capacity", func(in *AllocationInput) {
			in.Sessions = []AllocationSession{{Name: "proj", AssignmentLimit: 1, ActiveAssignments: 1}}
		}},
		{"critical pressure", func(in *AllocationInput) { in.Pressure.Level, in.Pressure.AgentHeadroom = "critical", 0 }},
		{"no candidates", func(in *AllocationInput) { in.Agents = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Agents = append([]AllocationAgent(nil), base.Agents...)
			tc.alter(&in)
			if got := PlanAllocations(in); len(got.Recommendations) != 0 {
				t.Fatalf("relaxation bypassed %s: %+v", tc.name, got)
			}
		})
	}
	base.MinScore = 0.25
	base.Relax = false
	strict = PlanAllocations(base)
	base.Relax = true
	if got := PlanAllocations(base); len(strict.Recommendations) == 0 || !reflect.DeepEqual(got, strict) {
		t.Fatalf("relaxation changed a nonempty strict plan: got %+v, want %+v", got, strict)
	}
}

func TestAllocationRelaxationPreservesPartiallyFulfilledStrictPlan(t *testing.T) {
	in := AllocationInput{
		BVAvailable: true, MinScore: 0.6,
		Pressure: AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		ReadyBeads: []AllocationReadyBead{
			{ID: "high-score", Priority: 0, TaskType: TaskFeature, GraphScore: 1},
			{ID: "low-score", Priority: 4, TaskType: TaskFeature},
		},
		Agents: []AllocationAgent{
			{ID: "one", Session: "proj", Idle: true, AgentType: tmux.AgentCodex},
			{ID: "two", Session: "proj", Idle: true, AgentType: tmux.AgentCodex},
		},
	}
	strict := PlanAllocations(in)
	if len(strict.Recommendations) != 1 || strict.Recommendations[0].BeadID != "high-score" || len(strict.UnassignedBeads) != 1 {
		t.Fatalf("fixture needs a partially fulfilled strict plan: %+v", strict)
	}
	// Show that the second bead really could be added at the relaxed floor.
	in.MinScore /= 2
	if lowered := PlanAllocations(in); len(lowered.Recommendations) != 2 {
		t.Fatalf("fixture needs additional work below the strict floor: %+v", lowered)
	}
	in.MinScore *= 2
	in.Relax = true
	if got := PlanAllocations(in); !reflect.DeepEqual(got, strict) {
		t.Fatalf("relaxation filled or changed a nonempty strict plan: got %+v, want %+v", got, strict)
	}
}

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
	if allocationTestNotEqual(row.Decision, AllocationDecisionRecommend) {
		t.Fatalf("log row decision = %s, want %s", row.Decision, AllocationDecisionRecommend)
	}
}

func TestPlanAllocationsPreservesCanonicalPaneIdentity(t *testing.T) {
	plan := PlanAllocations(AllocationInput{
		BVAvailable: true,
		Pressure:    AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2},
		ReadyBeads:  []AllocationReadyBead{{ID: "ntm-pane", Title: "Topology", Priority: 1}},
		Agents: []AllocationAgent{{
			ID: "cod-1", Session: "alpha", PaneIndex: 1, PaneTarget: "2.1", PaneID: "%91",
			AgentType: tmux.AgentCodex, Idle: true, ResourceHeadroom: 0.90,
		}},
	})
	if len(plan.Recommendations) != 1 {
		t.Fatalf("recommendations=%d, want 1", len(plan.Recommendations))
	}
	recommendation := plan.Recommendations[0]
	if recommendation.PaneTarget != "2.1" || recommendation.PaneID != "%91" {
		t.Fatalf("recommendation lost canonical pane identity: %+v", recommendation)
	}
}

func allocationTestEqual[T comparable](left, right T) bool {
	return left == right
}

func allocationTestNotEqual[T comparable](left, right T) bool {
	return !allocationTestEqual(left, right)
}
