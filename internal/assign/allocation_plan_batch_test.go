package assign

import (
	"reflect"
	"testing"
)

func TestPlanAllocationsConsumesSessionCapacity(t *testing.T) {
	in := AllocationInput{
		ReadyBeads: []AllocationReadyBead{{ID: "one", Priority: 1}, {ID: "two", Priority: 1}, {ID: "three", Priority: 1}},
		Agents: []AllocationAgent{
			{ID: "a", Session: "packed", Idle: true},
			{ID: "b", Session: "packed", Idle: true},
			{ID: "c", Session: "roomy", Idle: true},
		},
		Sessions: []AllocationSession{{Name: "packed", ActiveAssignments: 1, AssignmentLimit: 2}},
	}
	plan := PlanAllocations(in)
	if len(plan.Recommendations) != 2 || plan.Summary.Recommended != 2 || len(plan.UnassignedBeads) != 1 {
		t.Fatalf("expected two assignments and one unassigned bead, got %+v", plan)
	}
	counts := make(map[string]int)
	for _, rec := range plan.Recommendations {
		counts[rec.Session]++
	}
	if counts["packed"] != 1 || counts["roomy"] != 1 {
		t.Fatalf("batch exceeded remaining session capacity instead of spilling over: %v", counts)
	}
	if in.Sessions[0].ActiveAssignments != 1 {
		t.Fatal("planning changed the caller's session snapshot")
	}
	if again := PlanAllocations(in); !reflect.DeepEqual(plan, again) {
		t.Fatal("previewing the same snapshot changed the plan")
	}
}

func TestPlanAllocationsSessionScopedWorkerIdentity(t *testing.T) {
	in := AllocationInput{
		ReadyBeads: []AllocationReadyBead{{ID: "one", Priority: 1}, {ID: "two", Priority: 1}},
		Agents: []AllocationAgent{
			{ID: "cod-1", Session: "beta", Idle: true},
			{ID: "cod-1", Session: "alpha", Idle: true},
		},
	}
	plan := PlanAllocations(in)
	if len(plan.Recommendations) != 2 {
		t.Fatalf("same display identity in independent sessions lost capacity: %+v", plan)
	}
	if plan.Recommendations[0].Session == plan.Recommendations[1].Session {
		t.Fatalf("worker was reused in one session: %+v", plan.Recommendations)
	}
	in.Agents[0], in.Agents[1] = in.Agents[1], in.Agents[0]
	again := PlanAllocations(in)
	if !reflect.DeepEqual(plan.Recommendations, again.Recommendations) {
		t.Fatalf("agent discovery order changed assignment decisions: %+v versus %+v", plan.Recommendations, again.Recommendations)
	}
}

func TestPlanAllocationsBatchRetainsSafetyGates(t *testing.T) {
	in := AllocationInput{
		ReadyBeads: []AllocationReadyBead{{ID: "one", Priority: 0}},
		Agents: []AllocationAgent{
			{ID: "busy", Session: "alpha", Idle: false},
			{ID: "context-full", Session: "alpha", Idle: true, ContextUsage: 0.96},
			{ID: "assigned", Session: "alpha", Idle: true, ActiveAssignments: 1, AssignmentLimit: 1},
			{ID: "full-session", Session: "full", Idle: true},
		},
		Sessions: []AllocationSession{{Name: "full", ActiveAssignments: 2, AssignmentLimit: 2}},
	}
	if plan := PlanAllocations(in); len(plan.Recommendations) != 0 || plan.Decision != AllocationDecisionNoCapacity {
		t.Fatalf("batch bypassed eligibility checks: %+v", plan)
	}
	in.Agents = []AllocationAgent{{ID: "idle", Session: "alpha", Idle: true}}
	in.Pressure = AllocationPressure{Available: true, Level: "critical", AgentHeadroom: 0}
	if plan := PlanAllocations(in); len(plan.Recommendations) != 0 || plan.Decision != AllocationDecisionDefer {
		t.Fatalf("batch bypassed critical-pressure deferral: %+v", plan)
	}
}

func TestPlanAllocationsOptimizesWholeBatch(t *testing.T) {
	matrix := NewCapabilityMatrix()
	matrix.base["specialist"] = map[TaskType]float64{TaskFeature: 0.99, TaskBug: 0.95}
	matrix.base["generalist"] = map[TaskType]float64{TaskFeature: 0.94, TaskBug: 0.25}
	in := AllocationInput{
		Matrix: matrix,
		ReadyBeads: []AllocationReadyBead{
			{ID: "flexible", TaskType: TaskFeature, Priority: 2},
			{ID: "constrained", TaskType: TaskBug, Priority: 2},
		},
		Agents: []AllocationAgent{
			{ID: "specialist", Session: "s", AgentType: "specialist", Idle: true},
			{ID: "generalist", Session: "s", AgentType: "generalist", Idle: true},
		},
	}
	for _, minimum := range []float64{0, 0.50} {
		// The higher threshold removes the poor fallback entirely. The same
		// rerouting must then preserve cardinality as well as overall fit.
		in.MinScore = minimum
		plan := PlanAllocations(in)
		if len(plan.Recommendations) != 2 || len(plan.UnassignedBeads) != 0 {
			t.Fatalf("minimum %.2f: feasible work was stranded: %+v", minimum, plan)
		}
		got := make(map[string]string)
		for _, rec := range plan.Recommendations {
			got[rec.BeadID] = rec.AgentID
			if minimum > 0 && rec.Score < minimum {
				t.Fatalf("optimizer relaxed the minimum score: %+v", rec)
			}
		}
		if got["flexible"] != "generalist" || got["constrained"] != "specialist" {
			t.Fatalf("minimum %.2f: selected locally greedy rather than best batch: %v", minimum, got)
		}
		logged := 0
		for _, row := range plan.Logs {
			if row.Decision == AllocationDecisionRecommend {
				logged++
				if got[row.BeadID] != row.AgentID {
					t.Fatalf("audit row describes a superseded choice: %+v", row)
				}
			}
		}
		if logged != 2 || plan.Summary.Recommended != 2 {
			t.Fatalf("final batch and audit disagree: %+v", plan)
		}
	}
	in.MaxRecommendations = 1
	plan := PlanAllocations(in)
	if len(plan.Recommendations) != 1 || plan.Recommendations[0].BeadID != "flexible" || plan.Recommendations[0].AgentID != "specialist" {
		t.Fatalf("one-slot plan did not optimize for its actual limit: %+v", plan)
	}
}
