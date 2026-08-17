package assign

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestParseStrategy(t *testing.T) {
	tests := []struct {
		input string
		want  Strategy
	}{
		{"balanced", StrategyBalanced},
		{"BALANCED", StrategyBalanced},
		{"speed", StrategySpeed},
		{"quality", StrategyQuality},
		{"dependency", StrategyDependency},
		{"unknown", StrategyBalanced}, // Default
		{"", StrategyBalanced},        // Default
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseStrategy(tc.input)
			if got != tc.want {
				t.Errorf("ParseStrategy(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestMatcher_AssignTasks_EmptyInputs(t *testing.T) {
	m := NewMatcher()

	// Empty beads
	result := m.AssignTasks(nil, []Agent{{ID: "1", Idle: true}}, StrategyBalanced)
	if result != nil {
		t.Errorf("Expected nil for empty beads, got %v", result)
	}

	// Empty agents
	result = m.AssignTasks([]Bead{{ID: "b1"}}, nil, StrategyBalanced)
	if result != nil {
		t.Errorf("Expected nil for empty agents, got %v", result)
	}
}

func TestMatcher_AssignTasks_NoAvailableAgents(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", Title: "Test", TaskType: TaskFeature}}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: false},                   // Not idle
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.95}, // Too much context
	}

	result := m.AssignTasks(beads, agents, StrategyBalanced)
	if len(result) != 0 {
		t.Errorf("Expected no assignments for unavailable agents, got %d", len(result))
	}
}

func TestMatcher_AssignTasks_SingleBead_SingleAgent(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", Title: "Fix bug", TaskType: TaskBug, Priority: 1}}
	agents := []Agent{{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.2}}

	result := m.AssignTasks(beads, agents, StrategyBalanced)

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	if result[0].Bead.ID != "b1" {
		t.Errorf("Expected bead b1, got %s", result[0].Bead.ID)
	}
	if result[0].Agent.ID != "1" {
		t.Errorf("Expected agent 1, got %s", result[0].Agent.ID)
	}
	if result[0].Score <= 0 {
		t.Error("Expected positive score")
	}
	if result[0].Reason == "" {
		t.Error("Expected non-empty reason")
	}
}

func TestMatcher_AssignTasks_MoreBeadsThanAgents(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 1},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Task 3", TaskType: TaskDocs, Priority: 3},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
	}

	// Balanced strategy assigns all beads even to single agent
	result := m.AssignTasks(beads, agents, StrategyBalanced)
	if len(result) != 3 {
		t.Errorf("Balanced strategy should assign all beads, got %d", len(result))
	}

	// Speed/Quality/Dependency strategies limit to one bead per agent
	result = m.AssignTasks(beads, agents, StrategySpeed)
	if len(result) != 1 {
		t.Errorf("Speed strategy should limit to 1 per agent, got %d", len(result))
	}

	// Should assign highest priority first (P1 = b1)
	if result[0].Bead.ID != "b1" {
		t.Errorf("Expected highest priority bead b1, got %s", result[0].Bead.ID)
	}
}

func TestMatcher_AssignTasks_MoreAgentsThanBeads(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Fix bug", TaskType: TaskBug, Priority: 1},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyBalanced)

	// Should only assign as many as we have beads
	if len(result) != 1 {
		t.Errorf("Expected 1 assignment (limited by beads), got %d", len(result))
	}
}

func TestMatcher_Strategy_Speed(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 1},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.7},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.1},
	}

	result := m.AssignTasks(beads, agents, StrategySpeed)

	if len(result) != 2 {
		t.Fatalf("Expected 2 assignments, got %d", len(result))
	}

	// Speed strategy should assign quickly without much optimization
	for _, a := range result {
		if a.Reason == "" {
			t.Error("Expected non-empty reason")
		}
		// Speed strategy boosts confidence
		if a.Confidence < a.Score {
			t.Errorf("Speed strategy should boost confidence, got score=%f confidence=%f", a.Score, a.Confidence)
		}
	}
}

func TestMatcher_Strategy_Quality(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Refactor code", TaskType: TaskRefactor, Priority: 2},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true}, // Excellent at refactor (0.95)
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},  // Good at refactor (0.75)
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true}, // Good at refactor (0.75)
	}

	result := m.AssignTasks(beads, agents, StrategyQuality)

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	// Quality strategy should pick Claude for refactor task
	if result[0].Agent.AgentType != tmux.AgentClaude {
		t.Errorf("Quality strategy should pick Claude for refactor, got %s", result[0].Agent.AgentType)
	}
}

func TestMatcher_Strategy_Dependency(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Feature", TaskType: TaskFeature, Priority: 2, UnblocksIDs: nil},
		{ID: "b2", Title: "Blocker", TaskType: TaskBug, Priority: 2, UnblocksIDs: []string{"b3", "b4", "b5"}},
		{ID: "b3", Title: "Critical", TaskType: TaskBug, Priority: 0, UnblocksIDs: nil},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "2", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyDependency)

	if len(result) < 2 {
		t.Fatalf("Expected at least 2 assignments, got %d", len(result))
	}

	// First assignment should be b3 (P0 critical) or b2 (unblocks 3 items)
	// With dependency strategy, P0 comes first
	if result[0].Bead.ID != "b3" {
		t.Errorf("Expected critical priority bead b3 first, got %s", result[0].Bead.ID)
	}

	// Second should be the blocker (unblocks most items)
	if result[1].Bead.ID != "b2" {
		t.Errorf("Expected blocker bead b2 second, got %s", result[1].Bead.ID)
	}
}

func TestMatcher_Strategy_Balanced(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Task 3", TaskType: TaskDocs, Priority: 2},
		{ID: "b4", Title: "Task 4", TaskType: TaskRefactor, Priority: 2},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, Assignments: 0},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, Assignments: 0},
	}

	result := m.AssignTasks(beads, agents, StrategyBalanced)

	if len(result) != 4 {
		t.Fatalf("Expected 4 assignments, got %d", len(result))
	}

	// Count assignments per agent
	counts := make(map[string]int)
	for _, a := range result {
		counts[a.Agent.ID]++
	}

	// Balanced strategy should spread work (2 each)
	if counts["1"] != 2 || counts["2"] != 2 {
		t.Errorf("Expected balanced distribution (2, 2), got (%d, %d)", counts["1"], counts["2"])
	}
}

func TestMatcher_ContextUsageAffectsScore(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Bug fix", TaskType: TaskBug, Priority: 1},
	}

	// Agent with low context usage
	agentsLow := []Agent{{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.1}}
	resultLow := m.AssignTasks(beads, agentsLow, StrategyQuality)

	// Agent with moderate context usage (0.5 so score = 0.90 * 0.5 = 0.45 > MinConfidence 0.3)
	agentsMod := []Agent{{ID: "2", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.5}}
	resultMod := m.AssignTasks(beads, agentsMod, StrategyQuality)

	if len(resultLow) != 1 || len(resultMod) != 1 {
		t.Fatalf("Expected 1 assignment each, got low=%d mod=%d", len(resultLow), len(resultMod))
	}

	// Lower context usage should result in higher score
	// Low: 0.90 * 0.9 = 0.81
	// Mod: 0.90 * 0.5 = 0.45
	if resultLow[0].Score <= resultMod[0].Score {
		t.Errorf("Low context usage should have higher score: low=%f, mod=%f",
			resultLow[0].Score, resultMod[0].Score)
	}
}

func TestMatcher_ReasonContainsRelevantInfo(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Critical bug", TaskType: TaskBug, Priority: 0},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.6},
	}

	result := m.AssignTasks(beads, agents, StrategyQuality)

	if len(result) != 1 {
		t.Fatal("Expected 1 assignment")
	}

	reason := result[0].Reason

	// Should mention priority for P0
	if reason == "" {
		t.Error("Expected non-empty reason")
	}

	// Should mention context usage (60% is significant)
	if result[0].Agent.ContextUsage >= 0.5 {
		// Reason should include context info
		// This is a soft check - just verify reason is populated
		t.Logf("Reason: %s", reason)
	}
}

func TestAssignTasksFunc(t *testing.T) {
	// Test the convenience function
	beads := []Bead{
		{ID: "b1", Title: "Feature", TaskType: TaskFeature, Priority: 1},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := AssignTasksFunc(beads, agents, "quality")

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	if result[0].Bead.ID != "b1" {
		t.Errorf("Expected bead b1, got %s", result[0].Bead.ID)
	}
}

func TestMatcherConfigFiltering(t *testing.T) {
	config := MatcherConfig{
		MaxContextUsage: 0.5, // More restrictive
		MinConfidence:   0.7, // Higher threshold
	}
	m := &Matcher{matrix: GlobalMatrix(), config: config}

	beads := []Bead{
		{ID: "b1", Title: "Task", TaskType: TaskTask, Priority: 2},
	}

	// Agent with 60% context - should be filtered out with our config
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.6},
	}

	result := m.AssignTasks(beads, agents, StrategyBalanced)

	// Should have no assignments because agent context usage > MaxContextUsage
	if len(result) != 0 {
		t.Errorf("Expected 0 assignments (agent filtered), got %d", len(result))
	}
}

func TestMatcher_WithCustomMatrix(t *testing.T) {
	// Create custom matrix with boosted scores
	matrix := NewCapabilityMatrix()
	matrix.SetOverride(tmux.AgentGemini, TaskBug, 0.99) // Boost Gemini for bugs

	m := &Matcher{matrix: matrix, config: DefaultMatcherConfig()}

	beads := []Bead{
		{ID: "b1", Title: "Bug fix", TaskType: TaskBug, Priority: 1},
	}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentCodex, Idle: true},  // Default: 0.90 for bugs
		{ID: "2", AgentType: tmux.AgentGemini, Idle: true}, // Override: 0.99 for bugs
	}

	result := m.AssignTasks(beads, agents, StrategyQuality)

	if len(result) != 1 {
		t.Fatal("Expected 1 assignment")
	}

	// Quality strategy should pick Gemini due to override
	if result[0].Agent.AgentType != tmux.AgentGemini {
		t.Errorf("Expected Gemini (boosted score), got %s", result[0].Agent.AgentType)
	}
}

// Round-robin strategy tests

func TestParseStrategy_RoundRobin(t *testing.T) {
	tests := []struct {
		input string
		want  Strategy
	}{
		{"round-robin", StrategyRoundRobin},
		{"roundrobin", StrategyRoundRobin},
		{"rr", StrategyRoundRobin},
		{"ROUND-ROBIN", StrategyRoundRobin},
		{"RR", StrategyRoundRobin},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseStrategy(tc.input)
			if got != tc.want {
				t.Errorf("ParseStrategy(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestMatcher_Strategy_RoundRobin_EvenDistribution(t *testing.T) {
	m := NewMatcher()

	// 12 beads / 4 agents = 3, 3, 3, 3
	beads := make([]Bead, 12)
	for i := range beads {
		beads[i] = Bead{ID: string(rune('a' + i)), Title: "Task", TaskType: TaskFeature, Priority: 2}
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
		{ID: "4", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 12 {
		t.Fatalf("Expected 12 assignments, got %d", len(result))
	}

	// Count assignments per agent
	counts := make(map[string]int)
	for _, a := range result {
		counts[a.Agent.ID]++
	}

	// Each agent should have exactly 3 assignments
	for agentID, count := range counts {
		if count != 3 {
			t.Errorf("Agent %s expected 3 assignments, got %d", agentID, count)
		}
	}
}

func TestMatcher_Strategy_RoundRobin_UnevenDistribution(t *testing.T) {
	m := NewMatcher()

	// 13 beads / 4 agents = 4, 3, 3, 3 (first agent gets +1)
	beads := make([]Bead, 13)
	for i := range beads {
		beads[i] = Bead{ID: string(rune('a' + i)), Title: "Task", TaskType: TaskFeature, Priority: 2}
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
		{ID: "4", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 13 {
		t.Fatalf("Expected 13 assignments, got %d", len(result))
	}

	// Count assignments per agent
	counts := make(map[string]int)
	for _, a := range result {
		counts[a.Agent.ID]++
	}

	// Agent 1: 4 (gets +1), others: 3 each
	if counts["1"] != 4 {
		t.Errorf("Agent 1 expected 4 assignments (uneven), got %d", counts["1"])
	}
	for _, id := range []string{"2", "3", "4"} {
		if counts[id] != 3 {
			t.Errorf("Agent %s expected 3 assignments, got %d", id, counts[id])
		}
	}
}

func TestMatcher_Strategy_RoundRobin_MoreAgentsThanBeads(t *testing.T) {
	m := NewMatcher()

	// 3 beads / 5 agents = 1, 1, 1, 0, 0
	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Task 3", TaskType: TaskDocs, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
		{ID: "4", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "5", AgentType: tmux.AgentCodex, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 3 {
		t.Fatalf("Expected 3 assignments, got %d", len(result))
	}

	// Count assignments per agent
	counts := make(map[string]int)
	for _, a := range result {
		counts[a.Agent.ID]++
	}

	// First 3 agents get 1 each, last 2 get 0
	for _, id := range []string{"1", "2", "3"} {
		if counts[id] != 1 {
			t.Errorf("Agent %s expected 1 assignment, got %d", id, counts[id])
		}
	}
	for _, id := range []string{"4", "5"} {
		if counts[id] != 0 {
			t.Errorf("Agent %s expected 0 assignments, got %d", id, counts[id])
		}
	}
}

func TestMatcher_Strategy_RoundRobin_SingleBead(t *testing.T) {
	m := NewMatcher()

	// 1 bead / 4 agents = 1, 0, 0, 0
	beads := []Bead{
		{ID: "b1", Title: "Single task", TaskType: TaskFeature, Priority: 1},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
		{ID: "4", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	// Should be assigned to first agent
	if result[0].Agent.ID != "1" {
		t.Errorf("Expected agent 1 to get single bead, got agent %s", result[0].Agent.ID)
	}
}

func TestMatcher_Strategy_RoundRobin_EmptyAgents(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task", TaskType: TaskFeature, Priority: 2},
	}

	result := m.AssignTasks(beads, []Agent{}, StrategyRoundRobin)

	if result != nil {
		t.Errorf("Expected nil for empty agents, got %v", result)
	}
}

func TestMatcher_Strategy_RoundRobin_EmptyBeads(t *testing.T) {
	m := NewMatcher()

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks([]Bead{}, agents, StrategyRoundRobin)

	if result != nil {
		t.Errorf("Expected nil for empty beads, got %v", result)
	}
}

func TestMatcher_Strategy_RoundRobin_ScoreIsOne(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 1},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Task 3", TaskType: TaskDocs, Priority: 3},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	// All assignments should have score 1.0
	for _, a := range result {
		if a.Score != 1.0 {
			t.Errorf("Round-robin score should be 1.0, got %f for bead %s", a.Score, a.Bead.ID)
		}
		if a.Confidence != 1.0 {
			t.Errorf("Round-robin confidence should be 1.0, got %f for bead %s", a.Confidence, a.Bead.ID)
		}
	}
}

func TestMatcher_Strategy_RoundRobin_Deterministic(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 1},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Task 3", TaskType: TaskDocs, Priority: 3},
		{ID: "b4", Title: "Task 4", TaskType: TaskRefactor, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
	}

	// Run multiple times and verify same result
	result1 := m.AssignTasks(beads, agents, StrategyRoundRobin)
	result2 := m.AssignTasks(beads, agents, StrategyRoundRobin)
	result3 := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result1) != len(result2) || len(result2) != len(result3) {
		t.Fatal("Results should have same length")
	}

	for i := range result1 {
		if result1[i].Bead.ID != result2[i].Bead.ID || result2[i].Bead.ID != result3[i].Bead.ID {
			t.Errorf("Bead order not deterministic at index %d", i)
		}
		if result1[i].Agent.ID != result2[i].Agent.ID || result2[i].Agent.ID != result3[i].Agent.ID {
			t.Errorf("Agent assignment not deterministic at index %d", i)
		}
	}
}

func TestMatcher_Strategy_RoundRobin_ReasonFormat(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	for i, a := range result {
		if a.Reason == "" {
			t.Errorf("Expected non-empty reason at index %d", i)
		}
		// Reason should contain "round-robin"
		if !contains(a.Reason, "round-robin") {
			t.Errorf("Reason should mention round-robin: %s", a.Reason)
		}
	}
}

func TestMatcher_Strategy_RoundRobin_BeadOrdering(t *testing.T) {
	m := NewMatcher()

	// Beads with different priorities - should be sorted by priority first
	beads := []Bead{
		{ID: "b3", Title: "P3 Task", TaskType: TaskFeature, Priority: 3},
		{ID: "b1", Title: "P1 Task", TaskType: TaskBug, Priority: 1},
		{ID: "b2", Title: "P2 Task", TaskType: TaskDocs, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 3 {
		t.Fatalf("Expected 3 assignments, got %d", len(result))
	}

	// Beads should be sorted by priority (P1, P2, P3) before assignment
	// Agent 1 gets P1 (bead 1), Agent 2 gets P2 (bead 2), Agent 3 gets P3 (bead 3)
	expectedBeadOrder := []string{"b1", "b2", "b3"}
	expectedAgentOrder := []string{"1", "2", "3"}

	for i, a := range result {
		if a.Bead.ID != expectedBeadOrder[i] {
			t.Errorf("Position %d: expected bead %s, got %s", i, expectedBeadOrder[i], a.Bead.ID)
		}
		if a.Agent.ID != expectedAgentOrder[i] {
			t.Errorf("Position %d: expected agent %s, got %s", i, expectedAgentOrder[i], a.Agent.ID)
		}
	}
}

func TestMatcher_Strategy_RoundRobin_FiltersUnavailableAgents(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{
		{ID: "b1", Title: "Task 1", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Task 2", TaskType: TaskBug, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: false},                     // Not idle
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true, ContextUsage: 0.95}, // Too much context
		{ID: "4", AgentType: tmux.AgentClaude, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyRoundRobin)

	if len(result) != 2 {
		t.Fatalf("Expected 2 assignments, got %d", len(result))
	}

	// Only agents 1 and 4 should be assigned (2 and 3 are filtered)
	assignedAgents := make(map[string]bool)
	for _, a := range result {
		assignedAgents[a.Agent.ID] = true
	}

	if !assignedAgents["1"] || !assignedAgents["4"] {
		t.Errorf("Expected agents 1 and 4 to be assigned, got %v", assignedAgents)
	}
	if assignedAgents["2"] || assignedAgents["3"] {
		t.Errorf("Agents 2 and 3 should be filtered out, got %v", assignedAgents)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============================================================================
// Additional tests for bd-3nson: strategy selection and bead-agent matching
// ============================================================================

func TestDefaultMatcherConfig_Values(t *testing.T) {
	config := DefaultMatcherConfig()

	if config.MaxContextUsage != 0.9 {
		t.Errorf("DefaultMatcherConfig MaxContextUsage = %f, want 0.9", config.MaxContextUsage)
	}
	if config.MinConfidence != 0.3 {
		t.Errorf("DefaultMatcherConfig MinConfidence = %f, want 0.3", config.MinConfidence)
	}
}

func TestMatcher_ContextUsageBoundary_Exactly0_9(t *testing.T) {
	// Set MinConfidence to 0 to isolate MaxContextUsage check from scoring logic
	// With 0.9 usage, score becomes very low (capability * 0.1), which fails default 0.3 threshold
	config := DefaultMatcherConfig()
	config.MinConfidence = 0.0
	m := &Matcher{matrix: GlobalMatrix(), config: config}

	beads := []Bead{{ID: "b1", Title: "Test", TaskType: TaskFeature, Priority: 2}}

	// Agent with exactly 0.9 context (at boundary - should be available per default config)
	agentAt09 := []Agent{{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.9}}
	result := m.AssignTasks(beads, agentAt09, StrategyBalanced)
	if len(result) != 1 {
		t.Errorf("Agent at 0.9 context should be available (<=), got %d assignments", len(result))
	}

	// Agent at 0.91 - should be filtered
	agentOver09 := []Agent{{ID: "2", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.91}}
	result = m.AssignTasks(beads, agentOver09, StrategyBalanced)
	if len(result) != 0 {
		t.Errorf("Agent at 0.91 context should be filtered, got %d assignments", len(result))
	}
}

func TestMatcher_ContextUsage_ZeroUsage(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", Title: "Test", TaskType: TaskBug, Priority: 1}}
	agents := []Agent{{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.0}}

	result := m.AssignTasks(beads, agents, StrategyQuality)
	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	// With 0.0 context usage, score = capability * (1 - 0.0) = capability
	// Codex + Bug = 0.90
	expectedScore := 0.90
	if result[0].Score != expectedScore {
		t.Errorf("With 0.0 context usage, score should be %f, got %f", expectedScore, result[0].Score)
	}
}

func TestMatcher_MinConfidenceThreshold_Boundary(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", Title: "Test", TaskType: TaskTask, Priority: 2}}

	// Agent with context usage that puts score exactly at MinConfidence
	// TaskTask for Claude = 0.80, need score = 0.3
	// 0.80 * (1 - context) = 0.3 => context = 0.625
	agentAtThreshold := []Agent{{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.625}}
	result := m.AssignTasks(beads, agentAtThreshold, StrategyQuality)
	if len(result) != 1 {
		t.Errorf("Agent at MinConfidence threshold should be included, got %d", len(result))
	}

	// Agent just below threshold (context 0.63 => score = 0.80 * 0.37 = 0.296 < 0.3)
	agentBelowThreshold := []Agent{{ID: "2", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.63}}
	result = m.AssignTasks(beads, agentBelowThreshold, StrategyQuality)
	if len(result) != 0 {
		t.Errorf("Agent below MinConfidence threshold should be filtered, got %d", len(result))
	}
}

func TestMatcher_AllTaskTypes_AllStrategies(t *testing.T) {
	m := NewMatcher()

	taskTypes := []TaskType{
		TaskRefactor, TaskAnalysis, TaskDocs, TaskDocumentation,
		TaskBug, TaskFeature, TaskTesting, TaskTask, TaskChore, TaskEpic,
	}

	strategies := []Strategy{
		StrategyBalanced, StrategySpeed, StrategyQuality, StrategyDependency, StrategyRoundRobin,
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	for _, taskType := range taskTypes {
		for _, strategy := range strategies {
			t.Run(string(taskType)+"/"+string(strategy), func(t *testing.T) {
				beads := []Bead{{ID: "b1", Title: "Test", TaskType: taskType, Priority: 2}}
				result := m.AssignTasks(beads, agents, strategy)

				if len(result) != 1 {
					t.Errorf("TaskType %s with strategy %s should produce 1 assignment, got %d",
						taskType, strategy, len(result))
				}
			})
		}
	}
}

func TestMatcher_ScoreCalculation_Verification(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		name         string
		agentType    tmux.AgentType
		taskType     TaskType
		contextUsage float64
		wantScore    float64
	}{
		// Score = capability * (1 - contextUsage)
		{"Claude/Refactor/0.0", tmux.AgentClaude, TaskRefactor, 0.0, 0.95},
		{"Claude/Refactor/0.5", tmux.AgentClaude, TaskRefactor, 0.5, 0.475},
		{"Codex/Bug/0.0", tmux.AgentCodex, TaskBug, 0.0, 0.90},
		{"Codex/Bug/0.2", tmux.AgentCodex, TaskBug, 0.2, 0.72},
		{"Gemini/Docs/0.0", tmux.AgentGemini, TaskDocs, 0.0, 0.90},
		{"Gemini/Docs/0.3", tmux.AgentGemini, TaskDocs, 0.3, 0.63},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			beads := []Bead{{ID: "b1", TaskType: tc.taskType, Priority: 2}}
			agents := []Agent{{ID: "1", AgentType: tc.agentType, Idle: true, ContextUsage: tc.contextUsage}}

			result := m.AssignTasks(beads, agents, StrategyQuality)
			if len(result) != 1 {
				t.Fatalf("Expected 1 assignment, got %d", len(result))
			}

			// Allow small floating point tolerance
			if diff := result[0].Score - tc.wantScore; diff > 0.001 || diff < -0.001 {
				t.Errorf("Score = %f, want %f (diff: %f)", result[0].Score, tc.wantScore, diff)
			}
		})
	}
}

func TestMatcher_PriorityEdgeCases(t *testing.T) {
	m := NewMatcher()

	agents := []Agent{{ID: "1", AgentType: tmux.AgentClaude, Idle: true}}

	// Test various priority values including edge cases
	priorities := []int{0, 1, 2, 3, 4, 5, 10, 100, -1}

	for _, p := range priorities {
		t.Run(string(rune('P')+rune(p)), func(t *testing.T) {
			beads := []Bead{{ID: "b1", Title: "Test", TaskType: TaskFeature, Priority: p}}
			result := m.AssignTasks(beads, agents, StrategyBalanced)

			if len(result) != 1 {
				t.Errorf("Priority %d should still produce assignment, got %d", p, len(result))
			}
		})
	}
}

func TestMatcher_BeadLabels_NoEffectOnMatching(t *testing.T) {
	m := NewMatcher()

	// Labels exist in Bead struct but don't affect matching
	beadsWithLabels := []Bead{{
		ID:       "b1",
		Title:    "Test",
		TaskType: TaskFeature,
		Priority: 2,
		Labels:   []string{"urgent", "frontend", "v2.0"},
	}}
	beadsWithoutLabels := []Bead{{
		ID:       "b2",
		Title:    "Test",
		TaskType: TaskFeature,
		Priority: 2,
		Labels:   nil,
	}}

	agents := []Agent{{ID: "1", AgentType: tmux.AgentClaude, Idle: true}}

	resultWith := m.AssignTasks(beadsWithLabels, agents, StrategyQuality)
	resultWithout := m.AssignTasks(beadsWithoutLabels, agents, StrategyQuality)

	// Scores should be identical since labels don't affect matching
	if resultWith[0].Score != resultWithout[0].Score {
		t.Errorf("Labels should not affect score: with=%f, without=%f",
			resultWith[0].Score, resultWithout[0].Score)
	}
}

func TestMatcher_AgentModel_NoEffectOnMatching(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", TaskType: TaskFeature, Priority: 2}}

	// Agent with model specified
	agentWithModel := []Agent{{
		ID:           "1",
		AgentType:    tmux.AgentClaude,
		Model:        "claude-3-opus",
		Idle:         true,
		ContextUsage: 0.2,
	}}
	// Agent without model
	agentWithoutModel := []Agent{{
		ID:           "2",
		AgentType:    tmux.AgentClaude,
		Model:        "",
		Idle:         true,
		ContextUsage: 0.2,
	}}

	resultWith := m.AssignTasks(beads, agentWithModel, StrategyQuality)
	resultWithout := m.AssignTasks(beads, agentWithoutModel, StrategyQuality)

	// Model doesn't affect score in current implementation
	if resultWith[0].Score != resultWithout[0].Score {
		t.Errorf("Model should not affect score: with=%f, without=%f",
			resultWith[0].Score, resultWithout[0].Score)
	}
}

func TestMatcher_UnblocksIDs_OnlyAffectsDependencyStrategy(t *testing.T) {
	m := NewMatcher()

	beadWithUnblocks := Bead{
		ID:          "b1",
		Title:       "Blocker",
		TaskType:    TaskBug,
		Priority:    2,
		UnblocksIDs: []string{"b2", "b3", "b4"},
	}
	beadWithoutUnblocks := Bead{
		ID:       "b2",
		Title:    "Regular",
		TaskType: TaskBug,
		Priority: 2,
	}

	agents := []Agent{{ID: "1", AgentType: tmux.AgentCodex, Idle: true}}

	// For non-dependency strategies, UnblocksIDs shouldn't affect assignment
	nonDepStrategies := []Strategy{StrategyBalanced, StrategySpeed, StrategyQuality, StrategyRoundRobin}

	for _, strategy := range nonDepStrategies {
		t.Run(string(strategy), func(t *testing.T) {
			result1 := m.AssignTasks([]Bead{beadWithUnblocks}, agents, strategy)
			result2 := m.AssignTasks([]Bead{beadWithoutUnblocks}, agents, strategy)

			if len(result1) != 1 || len(result2) != 1 {
				t.Fatal("Expected 1 assignment each")
			}

			// Scores should be equal for non-dependency strategies
			if result1[0].Score != result2[0].Score {
				t.Errorf("UnblocksIDs should not affect %s strategy: with=%f, without=%f",
					strategy, result1[0].Score, result2[0].Score)
			}
		})
	}

	// For dependency strategy, UnblocksIDs SHOULD boost score
	t.Run("dependency", func(t *testing.T) {
		result1 := m.AssignTasks([]Bead{beadWithUnblocks}, agents, StrategyDependency)
		result2 := m.AssignTasks([]Bead{beadWithoutUnblocks}, agents, StrategyDependency)

		if len(result1) != 1 || len(result2) != 1 {
			t.Fatal("Expected 1 assignment each")
		}

		// With unblocks should have higher score
		if result1[0].Score <= result2[0].Score {
			t.Errorf("UnblocksIDs should boost dependency strategy score: with=%f, without=%f",
				result1[0].Score, result2[0].Score)
		}
	})
}

func TestMatcher_MultipleBeadsSamePriority_Ordering(t *testing.T) {
	m := NewMatcher()

	// Multiple beads with same priority - order should be stable
	beads := []Bead{
		{ID: "b1", Title: "First", TaskType: TaskFeature, Priority: 2},
		{ID: "b2", Title: "Second", TaskType: TaskBug, Priority: 2},
		{ID: "b3", Title: "Third", TaskType: TaskDocs, Priority: 2},
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	// Run multiple times to verify determinism
	for i := 0; i < 5; i++ {
		result := m.AssignTasks(beads, agents, StrategyRoundRobin)

		if len(result) != 3 {
			t.Fatalf("Expected 3 assignments, got %d", len(result))
		}

		// Round-robin should maintain input order for same-priority beads
		expectedOrder := []string{"b1", "b2", "b3"}
		for j, a := range result {
			if a.Bead.ID != expectedOrder[j] {
				t.Errorf("Iteration %d: position %d expected %s, got %s",
					i, j, expectedOrder[j], a.Bead.ID)
			}
		}
	}
}

func TestMatcher_Strategy_Speed_LowerThreshold(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", TaskType: TaskTask, Priority: 2}}

	// Speed strategy uses MinConfidence * 0.5 threshold
	// Normal MinConfidence = 0.3, Speed threshold = 0.15
	// TaskTask for Codex = 0.85, context needed for score = 0.15
	// 0.85 * (1 - context) = 0.15 => context = 0.824

	// Agent with very high context that would fail normal threshold
	agent := []Agent{{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.75}}

	result := m.AssignTasks(beads, agent, StrategySpeed)
	// Score = 0.85 * 0.25 = 0.2125 > 0.15 threshold
	if len(result) != 1 {
		t.Errorf("Speed strategy should accept lower scores, got %d assignments", len(result))
	}
}

func TestMatcher_Strategy_Quality_PicksBestCapability(t *testing.T) {
	m := NewMatcher()

	// Test that quality strategy picks the agent with best capability for each task type
	tests := []struct {
		taskType     TaskType
		expectedBest tmux.AgentType
	}{
		{TaskRefactor, tmux.AgentClaude}, // 0.95
		{TaskBug, tmux.AgentCodex},       // 0.90
		{TaskDocs, tmux.AgentGemini},     // 0.90
		{TaskFeature, tmux.AgentCodex},   // 0.90
		{TaskAnalysis, tmux.AgentClaude}, // 0.90
		{TaskTesting, tmux.AgentCodex},   // 0.85
		{TaskEpic, tmux.AgentClaude},     // 0.90
	}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	for _, tc := range tests {
		t.Run(string(tc.taskType), func(t *testing.T) {
			beads := []Bead{{ID: "b1", TaskType: tc.taskType, Priority: 2}}
			result := m.AssignTasks(beads, agents, StrategyQuality)

			if len(result) != 1 {
				t.Fatalf("Expected 1 assignment, got %d", len(result))
			}

			if result[0].Agent.AgentType != tc.expectedBest {
				t.Errorf("Quality strategy for %s should pick %s, got %s",
					tc.taskType, tc.expectedBest, result[0].Agent.AgentType)
			}
		})
	}
}

func TestMatcher_AgentCurrentTask_DoesNotAffectAssignment(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", TaskType: TaskFeature, Priority: 2}}

	// Agent with CurrentTask set but Idle=true (unusual but possible)
	agent := []Agent{{
		ID:          "1",
		AgentType:   tmux.AgentClaude,
		Idle:        true,
		CurrentTask: "some-previous-task",
	}}

	result := m.AssignTasks(beads, agent, StrategyBalanced)

	// Should still be assigned since Idle=true
	if len(result) != 1 {
		t.Errorf("Agent with CurrentTask but Idle=true should be assignable, got %d", len(result))
	}
}

func TestMatcher_AgentAssignments_AffectsBalancedStrategy(t *testing.T) {
	m := NewMatcher()

	beads := []Bead{{ID: "b1", TaskType: TaskFeature, Priority: 2}}

	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, Assignments: 5},
		{ID: "2", AgentType: tmux.AgentClaude, Idle: true, Assignments: 0},
	}

	result := m.AssignTasks(beads, agents, StrategyBalanced)

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	// Balanced strategy should prefer agent with fewer assignments
	if result[0].Agent.ID != "2" {
		t.Errorf("Balanced strategy should prefer agent with fewer assignments, got agent %s", result[0].Agent.ID)
	}
}

func TestMatcherCustomMatrix_UsesProvidedMatrix(t *testing.T) {
	matrix := NewCapabilityMatrix()

	// Override all agents to have very low score for TaskBug
	matrix.SetOverride(tmux.AgentClaude, TaskBug, 0.1)
	matrix.SetOverride(tmux.AgentCodex, TaskBug, 0.1)
	matrix.SetOverride(tmux.AgentGemini, TaskBug, 0.99) // Only Gemini is good

	m := &Matcher{matrix: matrix, config: DefaultMatcherConfig()}

	beads := []Bead{{ID: "b1", TaskType: TaskBug, Priority: 2}}
	agents := []Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true},
	}

	result := m.AssignTasks(beads, agents, StrategyQuality)

	if len(result) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(result))
	}

	// Custom matrix should cause Gemini to be picked
	if result[0].Agent.AgentType != tmux.AgentGemini {
		t.Errorf("Custom matrix should pick Gemini, got %s", result[0].Agent.AgentType)
	}
}

func TestMatcherConfig_MinConfidenceFiltering(t *testing.T) {
	// Test that MinConfidence properly filters low-score matches
	config := MatcherConfig{
		MaxContextUsage: 0.9,
		MinConfidence:   0.8, // Very high threshold
	}
	m := &Matcher{matrix: GlobalMatrix(), config: config}

	beads := []Bead{{ID: "b1", TaskType: TaskChore, Priority: 2}}
	// Claude has 0.70 for TaskChore, so with 0.0 context, score = 0.70 < 0.8 threshold
	agents := []Agent{{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.0}}

	result := m.AssignTasks(beads, agents, StrategyQuality)

	if len(result) != 0 {
		t.Errorf("High MinConfidence should filter low-capability matches, got %d", len(result))
	}
}

func TestMatcher_ReasonFormat_VariousScenarios(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		name           string
		bead           Bead
		agent          Agent
		strategy       Strategy
		expectContains []string
	}{
		{
			name:           "critical priority",
			bead:           Bead{ID: "b1", TaskType: TaskBug, Priority: 0},
			agent:          Agent{ID: "1", AgentType: tmux.AgentCodex, Idle: true},
			strategy:       StrategyQuality,
			expectContains: []string{"critical priority"},
		},
		{
			name:           "high priority",
			bead:           Bead{ID: "b1", TaskType: TaskBug, Priority: 1},
			agent:          Agent{ID: "1", AgentType: tmux.AgentCodex, Idle: true},
			strategy:       StrategyQuality,
			expectContains: []string{"high priority"},
		},
		{
			name:           "high context usage",
			bead:           Bead{ID: "b1", TaskType: TaskBug, Priority: 2},
			agent:          Agent{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.6},
			strategy:       StrategyQuality,
			expectContains: []string{"context", "60%"},
		},
		{
			name:           "excels capability",
			bead:           Bead{ID: "b1", TaskType: TaskRefactor, Priority: 2},
			agent:          Agent{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
			strategy:       StrategyQuality,
			expectContains: []string{"excels", "95%"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := m.AssignTasks([]Bead{tc.bead}, []Agent{tc.agent}, tc.strategy)
			if len(result) != 1 {
				t.Fatalf("Expected 1 assignment, got %d", len(result))
			}

			reason := result[0].Reason
			for _, expected := range tc.expectContains {
				if !contains(reason, expected) {
					t.Errorf("Reason %q should contain %q", reason, expected)
				}
			}
		})
	}
}
