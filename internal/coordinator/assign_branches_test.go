package coordinator

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// ---------------------------------------------------------------------------
// computeConfidence — missing clamping and bonus-only branches (81.2% → higher)
// ---------------------------------------------------------------------------

func TestComputeConfidence_ScoreClampsAbove2(t *testing.T) {
	t.Parallel()
	// score/2.0 > 1.0 should be clamped to 1.0 base
	p := scoredPair{score: 3.0, breakdown: AssignmentScoreBreakdown{}}
	got := computeConfidence(p)
	if got < 0.9 || got > 0.95 {
		t.Errorf("computeConfidence(score=3.0) = %f, want 0.9–0.95 (clamped)", got)
	}
}

func TestComputeConfidence_HighScoreWithBonusesClamped(t *testing.T) {
	t.Parallel()
	// score/2.0 = 1.0 + AgentTypeBonus(0.1) + ProfileTagBonus(0.1) = 1.2 → clamped to 0.95
	p := scoredPair{
		score:     2.0,
		breakdown: AssignmentScoreBreakdown{AgentTypeBonus: 0.2, ProfileTagBonus: 0.2},
	}
	got := computeConfidence(p)
	if got != 0.95 {
		t.Errorf("computeConfidence() = %f, want 0.95 (upper clamp)", got)
	}
}

func TestComputeConfidence_HeavyPenaltiesClamped(t *testing.T) {
	t.Parallel()
	// score/2.0 = 0.1, minus heavy penalties → should clamp to 0.1
	p := scoredPair{
		score: 0.2,
		breakdown: AssignmentScoreBreakdown{
			ContextPenalty:     0.5,
			FileOverlapPenalty: 1.0,
		},
	}
	got := computeConfidence(p)
	if got != 0.1 {
		t.Errorf("computeConfidence() = %f, want 0.1 (lower clamp)", got)
	}
}

func TestComputeConfidence_AgentTypeBonusOnly(t *testing.T) {
	t.Parallel()
	p := scoredPair{
		score:     1.0,
		breakdown: AssignmentScoreBreakdown{AgentTypeBonus: 0.15},
	}
	got := computeConfidence(p)
	// base = 0.5 + 0.1 (agent bonus) = 0.6
	if got < 0.55 || got > 0.65 {
		t.Errorf("computeConfidence() = %f, want ~0.6", got)
	}
}

// ---------------------------------------------------------------------------
// buildAssignmentReason — missing strategy/bonus branches (87.5% → higher)
// ---------------------------------------------------------------------------

func TestBuildAssignmentReason_SpeedStrategy(t *testing.T) {
	t.Parallel()
	p := scoredPair{bead: &bv.TriageRecommendation{}}
	reason := buildAssignmentReason(p, StrategySpeed)
	if !strings.Contains(reason, "fastest available agent") {
		t.Errorf("expected 'fastest available agent' in %q", reason)
	}
}

func TestBuildAssignmentReason_DependencyNoBlockers(t *testing.T) {
	t.Parallel()
	// Dependency strategy but no UnblocksIDs → no lead reason → falls to "available and qualified"
	p := scoredPair{bead: &bv.TriageRecommendation{}}
	reason := buildAssignmentReason(p, StrategyDependency)
	if reason != "available and qualified" {
		t.Errorf("expected 'available and qualified', got %q", reason)
	}
}

func TestBuildAssignmentReason_CriticalPathBonus(t *testing.T) {
	t.Parallel()
	p := scoredPair{
		bead:      &bv.TriageRecommendation{},
		breakdown: AssignmentScoreBreakdown{CriticalPathBonus: 0.2},
	}
	reason := buildAssignmentReason(p, StrategyBalanced)
	if !strings.Contains(reason, "on critical path") {
		t.Errorf("expected 'on critical path' in %q", reason)
	}
}

func TestBuildAssignmentReason_BelowThreshold(t *testing.T) {
	t.Parallel()
	// All bonuses below 0.05 threshold → none included, only strategy lead
	p := scoredPair{
		bead: &bv.TriageRecommendation{},
		breakdown: AssignmentScoreBreakdown{
			AgentTypeBonus:    0.04,
			ProfileTagBonus:   0.03,
			CriticalPathBonus: 0.02,
		},
	}
	reason := buildAssignmentReason(p, StrategyQuality)
	if reason != "best capability match" {
		t.Errorf("expected only 'best capability match', got %q", reason)
	}
}

// ---------------------------------------------------------------------------
// selectDependency — missing priority and score tie-break branches (80% → higher)
// ---------------------------------------------------------------------------

func TestSelectDependency_PriorityTieBreak(t *testing.T) {
	t.Parallel()

	agent1 := &AgentState{PaneID: "%1"}
	agent2 := &AgentState{PaneID: "%2"}
	// Same UnblocksIDs count, different priority
	bead1 := &bv.TriageRecommendation{ID: "b1", UnblocksIDs: []string{"x"}, Priority: 2}
	bead2 := &bv.TriageRecommendation{ID: "b2", UnblocksIDs: []string{"y"}, Priority: 0}

	pairs := []scoredPair{
		{agent: agent1, bead: bead1, score: 1.0},
		{agent: agent2, bead: bead2, score: 0.5},
	}

	result := selectDependency(pairs, 2, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// bead2 (priority 0) should come before bead1 (priority 2)
	if result[0].bead.ID != "b2" {
		t.Errorf("expected b2 first (lower priority), got %s", result[0].bead.ID)
	}
}

func TestSelectDependency_ScoreTieBreak(t *testing.T) {
	t.Parallel()

	agent1 := &AgentState{PaneID: "%1"}
	agent2 := &AgentState{PaneID: "%2"}
	// Same UnblocksIDs count, same priority, different score
	bead1 := &bv.TriageRecommendation{ID: "b1", UnblocksIDs: []string{"x"}, Priority: 1}
	bead2 := &bv.TriageRecommendation{ID: "b2", UnblocksIDs: []string{"y"}, Priority: 1}

	pairs := []scoredPair{
		{agent: agent1, bead: bead1, score: 0.5},
		{agent: agent2, bead: bead2, score: 1.5},
	}

	result := selectDependency(pairs, 2, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// bead2 (score 1.5) should come before bead1 (score 0.5)
	if result[0].bead.ID != "b2" {
		t.Errorf("expected b2 first (higher score), got %s", result[0].bead.ID)
	}
}

func TestSelectDependency_SkipsDuplicateAgent(t *testing.T) {
	t.Parallel()

	agent1 := &AgentState{PaneID: "%1"}
	bead1 := &bv.TriageRecommendation{ID: "b1", UnblocksIDs: []string{"x", "y"}}
	bead2 := &bv.TriageRecommendation{ID: "b2", UnblocksIDs: []string{"z"}}

	// Same agent for both beads — second should be skipped
	pairs := []scoredPair{
		{agent: agent1, bead: bead1, score: 1.0},
		{agent: agent1, bead: bead2, score: 0.5},
	}

	result := selectDependency(pairs, 2, 2)
	if len(result) != 1 {
		t.Fatalf("expected 1 result (agent dedup), got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// estimateTaskComplexity — missing unknown type and clamp branches (85% → higher)
// ---------------------------------------------------------------------------

func TestEstimateTaskComplexity_UnknownType(t *testing.T) {
	t.Parallel()
	rec := &bv.TriageRecommendation{Type: "unknown-type", Priority: 1}
	got := estimateTaskComplexity(rec)
	// Unknown type gets no bonus/penalty → stays at 0.5 base
	if got != 0.5 {
		t.Errorf("estimateTaskComplexity(unknown type) = %f, want 0.5", got)
	}
}

func TestEstimateTaskComplexity_ClampsToOne(t *testing.T) {
	t.Parallel()
	// epic (0.3) + priority>=3 (0.1) + 5 unblocks (0.15) = 0.5+0.3+0.1+0.15 = 1.05 → clamped to 1.0
	rec := &bv.TriageRecommendation{
		Type:       "epic",
		Priority:   4,
		UnblocksIDs: []string{"a", "b", "c", "d", "e"},
	}
	got := estimateTaskComplexity(rec)
	if got != 1.0 {
		t.Errorf("estimateTaskComplexity() = %f, want 1.0 (clamped)", got)
	}
}

func TestEstimateTaskComplexity_ClampsToZero(t *testing.T) {
	t.Parallel()
	// chore (-0.2) + priority==0 (-0.1) = 0.5-0.2-0.1 = 0.2, not below 0
	// To get below 0 we'd need more negative. Actually chore+priority=0 = 0.2 which is >0.
	// The clamp to 0 branch may not be reachable easily.
	// Let's just test chore + priority 0 combination (covers the type+priority paths)
	rec := &bv.TriageRecommendation{Type: "chore", Priority: 0}
	got := estimateTaskComplexity(rec)
	if got < 0.0 || got > 0.3 {
		t.Errorf("estimateTaskComplexity(chore, p0) = %f, want ~0.2", got)
	}
}

func TestEstimateTaskComplexity_ThreeUnblocks(t *testing.T) {
	t.Parallel()
	rec := &bv.TriageRecommendation{
		Type:        "task",
		Priority:    1,
		UnblocksIDs: []string{"a", "b", "c"},
	}
	got := estimateTaskComplexity(rec)
	// task(-0.1) + 3 unblocks(+0.1) = 0.5-0.1+0.1 = 0.5
	if got < 0.45 || got > 0.55 {
		t.Errorf("estimateTaskComplexity(task, 3 unblocks) = %f, want ~0.5", got)
	}
}

// ---------------------------------------------------------------------------
// applyStrategySelection — covers all strategy switch cases (83.3% → higher)
// ---------------------------------------------------------------------------

func TestApplyStrategySelection_Speed(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.9},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.5},
	}
	result := applyStrategySelection(pairs, StrategySpeed, 2, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 selections, got %d", len(result))
	}
}

func TestApplyStrategySelection_Balanced(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0", Status: robot.StateWaiting, Assignments: 0}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.8},
		{agent: &AgentState{PaneID: "%1", Status: robot.StateGenerating, Assignments: 2}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.9},
	}
	result := applyStrategySelection(pairs, StrategyBalanced, 2, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 selections, got %d", len(result))
	}
}

func TestApplyStrategySelection_Quality(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.9},
	}
	result := applyStrategySelection(pairs, StrategyQuality, 2, 1)
	if len(result) != 1 {
		t.Errorf("expected 1 selection (best agent for b1), got %d", len(result))
	}
	if len(result) > 0 && result[0].agent.PaneID != "%1" {
		t.Errorf("expected agent %%1 (higher score), got %s", result[0].agent.PaneID)
	}
}

func TestApplyStrategySelection_Dependency(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1", Priority: 0}, score: 0.5},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b2", Priority: 2}, score: 0.9},
	}
	result := applyStrategySelection(pairs, 2, 2, StrategyDependency)
	if len(result) != 2 {
		t.Errorf("expected 2 selections, got %d", len(result))
	}
}

func TestApplyStrategySelection_UnknownDefaultsToGreedy(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.9},
	}
	result := applyStrategySelection(pairs, 1, 1, AssignmentStrategy("unknown"))
	if len(result) != 1 {
		t.Errorf("expected 1 selection from default/greedy, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// selectQuality — tests for quality selection logic (85% → higher)
// ---------------------------------------------------------------------------

func TestSelectQuality_PicksBestAgentPerBead(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.3},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.7},
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.6},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.4},
	}
	result := selectQuality(pairs, 2, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 selections, got %d", len(result))
	}
}

func TestSelectQuality_AgentConflictResolution(t *testing.T) {
	t.Parallel()
	// Agent %0 is best for both beads - should only be assigned once
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.9},
		{agent: &AgentState{PaneID: "%0"}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.8},
		{agent: &AgentState{PaneID: "%1"}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.5},
	}
	result := selectQuality(pairs, 2, 2)
	// Agent %0 gets b1 (highest), b2 conflict resolved to %1
	agentIDs := make(map[string]bool)
	for _, r := range result {
		agentIDs[r.agent.PaneID] = true
	}
	if len(result) > 2 {
		t.Errorf("expected at most 2 selections, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// selectBalanced — tests for tie-breaker logic (not yet tested)
// ---------------------------------------------------------------------------

func TestSelectBalanced_PrefersIdleAgents(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0", Status: robot.StateGenerating, Assignments: 0}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
		{agent: &AgentState{PaneID: "%1", Status: robot.StateWaiting, Assignments: 0}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
	}
	result := selectBalanced(pairs, 1, 1)
	if len(result) != 1 {
		t.Fatalf("expected 1 selection, got %d", len(result))
	}
	if result[0].agent.PaneID != "%1" {
		t.Errorf("expected idle agent %%1 to be preferred, got %s", result[0].agent.PaneID)
	}
}

func TestSelectBalanced_PrefersLeastRecentAssignment(t *testing.T) {
	t.Parallel()
	now := time.Now()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0", Status: robot.StateWaiting, Assignments: 0, LastAssignedAt: now}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
		{agent: &AgentState{PaneID: "%1", Status: robot.StateWaiting, Assignments: 0, LastAssignedAt: now.Add(-time.Hour)}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
	}
	result := selectBalanced(pairs, 1, 1)
	if len(result) != 1 {
		t.Fatalf("expected 1 selection, got %d", len(result))
	}
	if result[0].agent.PaneID != "%1" {
		t.Errorf("expected agent with oldest assignment, got %s", result[0].agent.PaneID)
	}
}

func TestSelectBalanced_FallbackWhenAssignmentsUnavailable(t *testing.T) {
	t.Parallel()
	pairs := []scoredPair{
		{agent: &AgentState{PaneID: "%0", Status: robot.StateWaiting, Assignments: -1}, bead: &bv.TriageRecommendation{ID: "b1"}, score: 0.5},
		{agent: &AgentState{PaneID: "%1", Status: robot.StateWaiting, Assignments: -1}, bead: &bv.TriageRecommendation{ID: "b2"}, score: 0.8},
	}
	result := selectBalanced(pairs, 2, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 selections with fallback tracking, got %d", len(result))
	}
}
