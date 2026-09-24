package coordinator

import (
	"math"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/persona"
)

// Exercise the production planner that AssignWork calls, not just a synthetic
// matrix: the first agent has both a file specialization and an implementation
// persona, so taking its single highest score wastes the latter specialization.
func TestScoreAndSelectAssignmentsOptimizesWholeBatch(t *testing.T) {
	agents := []*AgentState{
		{PaneID: "%1", PaneIndex: 1, AgentType: "test-provider", AgentMailName: "FirstAgent", Profile: &persona.Persona{
			Tags: []string{"implementation"}, FocusPatterns: []string{"src/*.go"},
		}},
		{PaneID: "%2", PaneIndex: 2, AgentType: "test-provider", AgentMailName: "SecondAgent"},
	}
	recommendations := []bv.TriageRecommendation{
		{ID: "ntm-test", Title: "Test src/x.go", Type: "task", Status: "open", Score: 1.0},
		{ID: "ntm-build", Title: "Implement delivery", Type: "task", Status: "open", Score: 0.1},
	}
	selected := ScoreAndSelectAssignments(agents, recommendations, ScoreConfig{UseAgentProfiles: true}, nil)
	if len(selected) != 2 {
		t.Fatalf("selected %d assignments, want 2: %+v", len(selected), selected)
	}
	got := make(map[string]string)
	total := 0.0
	for _, selection := range selected {
		got[selection.Agent.PaneID] = selection.Recommendation.ID
		total += selection.TotalScore
		work := selection.Assignment
		if work == nil || work.AgentPaneID != selection.Agent.PaneID ||
			work.AgentMailName != selection.Agent.AgentMailName ||
			work.BeadID != selection.Recommendation.ID || work.Score != selection.TotalScore {
			t.Fatalf("lost or mixed dispatch metadata: %+v", selection)
		}
	}
	if want := map[string]string{"%1": "ntm-build", "%2": "ntm-test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pairings = %v, want %v", got, want)
	}
	if math.Abs(total-1.25) > 1e-12 {
		t.Fatalf("batch score = %v, want 1.25 (greedy produces 1.20)", total)
	}
	if selected[0].TotalScore < selected[1].TotalScore {
		t.Fatal("dispatch order must remain highest-score first")
	}
}

func TestScoreAndSelectAssignmentsMatchingRetainsSemanticGates(t *testing.T) {
	agents := []*AgentState{nil, {PaneID: " "}, {PaneID: "%1"}, {PaneID: "%2"}}
	recommendations := []bv.TriageRecommendation{
		{ID: "ntm-open", Title: "Implement delivery", Type: "task", Status: "open", Score: 1},
		{ID: "ntm-closed", Type: "task", Status: "closed", Score: 100},
		{ID: "ntm-blocked", Type: "task", Status: "open", BlockedBy: []string{"dependency"}, Score: 100},
		{ID: "ntm-epic", Type: "epic", Status: "open", Score: 100},
		{ID: "ntm-inf", Type: "task", Status: "open", Score: math.Inf(1)},
		{ID: "ntm-nan", Type: "task", Status: "open", Score: math.NaN()},
		{ID: "", Type: "task", Status: "open", Score: 100},
	}
	for _, label := range bv.OperatorGatedLabels() {
		recommendations = append(recommendations, bv.TriageRecommendation{
			ID: "ntm-operator-" + label, Type: "task", Status: "open", Labels: []string{label}, Score: 100,
		})
	}
	selected := ScoreAndSelectAssignments(agents, recommendations, ScoreConfig{}, nil)
	if len(selected) != 1 || selected[0].Recommendation.ID != "ntm-open" {
		t.Fatalf("matching admitted gated or malformed work: %+v", selected)
	}
}
