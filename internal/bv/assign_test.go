package bv

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestTriageToAssignIntegration tests the conversion from triage data to assignments.
func TestTriageToAssignIntegration(t *testing.T) {
	// Create mock triage recommendations
	triageRecs := []TriageRecommendation{
		{
			ID:       "bd-001",
			Title:    "Refactor authentication module",
			Type:     "task",
			Status:   "open",
			Priority: 1,
			Score:    0.85,
			Action:   "Start work",
			Reasons:  []string{"High impact", "Unblocks 3 items"},
		},
		{
			ID:       "bd-002",
			Title:    "Fix login bug",
			Type:     "bug",
			Status:   "open",
			Priority: 0,
			Score:    0.90,
			Action:   "Start work",
			Reasons:  []string{"Critical priority"},
		},
		{
			ID:       "bd-003",
			Title:    "Update documentation",
			Type:     "task",
			Status:   "open",
			Priority: 2,
			Score:    0.70,
			Action:   "Start work",
			Reasons:  []string{"Good first task"},
		},
	}

	// Convert to assign.Bead format
	beads := triageRecommendationsToBeads(triageRecs)

	if len(beads) != 3 {
		t.Fatalf("Expected 3 beads, got %d", len(beads))
	}

	// Verify priority conversion
	if beads[0].Priority != 1 {
		t.Errorf("First bead priority = %d, want 1", beads[0].Priority)
	}

	// Create mock agents
	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.2},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.3},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true, ContextUsage: 0.1},
	}

	// Run assignment
	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyQuality)

	if len(assignments) == 0 {
		t.Fatal("Expected at least 1 assignment")
	}

	t.Logf("BV_TEST: TriageToAssign | Tasks=%d | Agents=%d | Assignments=%d",
		len(beads), len(agents), len(assignments))

	for _, a := range assignments {
		t.Logf("  Assignment: %s -> Agent %s (%s) [score=%.2f]",
			a.Bead.ID, a.Agent.ID, a.Agent.AgentType, a.Score)
	}
}

// triageRecommendationsToBeads converts triage recommendations to assignment beads.
func triageRecommendationsToBeads(recs []TriageRecommendation) []assign.Bead {
	beads := make([]assign.Bead, 0, len(recs))
	for _, rec := range recs {
		beads = append(beads, assign.Bead{
			ID:          rec.ID,
			Title:       rec.Title,
			Priority:    rec.Priority,
			TaskType:    inferTaskTypeFromTriage(rec),
			UnblocksIDs: rec.UnblocksIDs,
		})
	}
	return beads
}

// inferTaskTypeFromTriage determines task type from triage recommendation.
func inferTaskTypeFromTriage(rec TriageRecommendation) assign.TaskType {
	// Check explicit type first
	switch rec.Type {
	case "bug":
		return assign.TaskBug
	case "feature":
		return assign.TaskFeature
	case "epic":
		return assign.TaskEpic
	case "chore":
		return assign.TaskChore
	}

	// Fall back to title analysis
	return assign.ParseTaskType(rec.Title)
}

// TestPriorityBasedAssignment verifies that high-priority items are assigned first.
func TestPriorityBasedAssignment(t *testing.T) {
	// Create beads with different priorities
	beads := []assign.Bead{
		{ID: "low", Title: "Low priority task", Priority: 3, TaskType: assign.TaskTask},
		{ID: "p0", Title: "Critical bug fix", Priority: 0, TaskType: assign.TaskBug},
		{ID: "mid", Title: "Medium priority", Priority: 2, TaskType: assign.TaskTask},
	}

	// Only one agent available
	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.1},
	}

	matcher := assign.NewMatcher()

	// Quality strategy should pick highest priority
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyQuality)

	if len(assignments) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(assignments))
	}

	// Should assign the P0 critical bug
	if assignments[0].Bead.ID != "p0" {
		t.Errorf("Expected P0 bead, got %s", assignments[0].Bead.ID)
	}

	t.Logf("BV_TEST: PriorityBased | Assigned=%s | Priority=%d",
		assignments[0].Bead.ID, assignments[0].Bead.Priority)
}

// TestLoadBalancingAcrossAgents verifies even distribution with balanced strategy.
func TestLoadBalancingAcrossAgents(t *testing.T) {
	// Create 6 beads
	beads := make([]assign.Bead, 6)
	for i := 0; i < 6; i++ {
		beads[i] = assign.Bead{
			ID:       string(rune('a' + i)),
			Title:    "Task",
			Priority: 2,
			TaskType: assign.TaskTask,
		}
	}

	// Create 3 agents with different prior assignments
	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, Assignments: 0},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, Assignments: 0},
		{ID: "3", AgentType: tmux.AgentGemini, Idle: true, Assignments: 0},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyBalanced)

	// Count per agent
	counts := make(map[string]int)
	for _, a := range assignments {
		counts[a.Agent.ID]++
	}

	t.Logf("BV_TEST: LoadBalancing | Total=%d | Distribution=%v", len(assignments), counts)

	// Each agent should get 2 tasks
	for id, count := range counts {
		if count != 2 {
			t.Errorf("Agent %s got %d tasks, expected 2", id, count)
		}
	}
}

// TestAgentCapabilityMatching verifies agents are matched to appropriate tasks.
func TestAgentCapabilityMatching(t *testing.T) {
	beads := []assign.Bead{
		{ID: "refactor", Title: "Refactor module", TaskType: assign.TaskRefactor},
		{ID: "docs", Title: "Update documentation", TaskType: assign.TaskDocs},
		{ID: "bug", Title: "Fix critical bug", TaskType: assign.TaskBug},
	}

	agents := []assign.Agent{
		{ID: "claude", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "codex", AgentType: tmux.AgentCodex, Idle: true},
		{ID: "gemini", AgentType: tmux.AgentGemini, Idle: true},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyQuality)

	// Build assignment map
	assignMap := make(map[string]string)
	for _, a := range assignments {
		assignMap[a.Bead.ID] = string(a.Agent.AgentType)
	}

	t.Logf("BV_TEST: CapabilityMatching | Assignments=%v", assignMap)

	// Verify expected matches based on capability matrix
	// Claude excels at refactoring (0.95)
	if assignMap["refactor"] != "cc" {
		t.Errorf("Expected Claude for refactor, got %s", assignMap["refactor"])
	}

	// Gemini excels at docs (0.90)
	if assignMap["docs"] != "gmi" {
		t.Errorf("Expected Gemini for docs, got %s", assignMap["docs"])
	}

	// Codex excels at bugs (0.90)
	if assignMap["bug"] != "cod" {
		t.Errorf("Expected Codex for bug, got %s", assignMap["bug"])
	}
}

// TestDependencyStrategyBlockerPriority verifies blocker items get priority.
func TestDependencyStrategyBlockerPriority(t *testing.T) {
	beads := []assign.Bead{
		{ID: "regular", Title: "Regular task", Priority: 2, TaskType: assign.TaskTask},
		{ID: "blocker", Title: "Blocker task", Priority: 2, TaskType: assign.TaskTask,
			UnblocksIDs: []string{"a", "b", "c"}},
	}

	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyDependency)

	if len(assignments) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(assignments))
	}

	// Blocker should be assigned (unblocks 3 items)
	if assignments[0].Bead.ID != "blocker" {
		t.Errorf("Expected blocker task, got %s", assignments[0].Bead.ID)
	}

	t.Logf("BV_TEST: DependencyStrategy | Assigned=%s | Unblocks=%d",
		assignments[0].Bead.ID, len(assignments[0].Bead.UnblocksIDs))
}

// TestContextUsageFiltering verifies agents with high context usage are filtered.
func TestContextUsageFiltering(t *testing.T) {
	beads := []assign.Bead{
		{ID: "task", Title: "Some task", Priority: 2, TaskType: assign.TaskTask},
	}

	// All agents have high context usage
	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: 0.95},
		{ID: "2", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.98},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyBalanced)

	// No assignments should be made (all agents over 90% context)
	if len(assignments) != 0 {
		t.Errorf("Expected 0 assignments for high-context agents, got %d", len(assignments))
	}

	t.Logf("BV_TEST: ContextFiltering | Agents=%d | Filtered=all", len(agents))
}

// TestIdleAgentFiltering verifies non-idle agents are filtered.
func TestIdleAgentFiltering(t *testing.T) {
	beads := []assign.Bead{
		{ID: "task1", Title: "Task 1", Priority: 2, TaskType: assign.TaskTask},
		{ID: "task2", Title: "Task 2", Priority: 2, TaskType: assign.TaskTask},
	}

	agents := []assign.Agent{
		{ID: "busy", AgentType: tmux.AgentClaude, Idle: false, CurrentTask: "other-task"},
		{ID: "idle", AgentType: tmux.AgentCodex, Idle: true},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategySpeed)

	// Only idle agent should get assignment
	if len(assignments) != 1 {
		t.Fatalf("Expected 1 assignment, got %d", len(assignments))
	}

	if assignments[0].Agent.ID != "idle" {
		t.Errorf("Expected idle agent, got %s", assignments[0].Agent.ID)
	}

	t.Logf("BV_TEST: IdleFiltering | TotalAgents=%d | IdleAgents=1 | Assignments=%d",
		len(agents), len(assignments))
}

// TestMixedStrategies compares different strategy outputs.
func TestMixedStrategies(t *testing.T) {
	beads := []assign.Bead{
		{ID: "refactor", Title: "Refactor code", Priority: 2, TaskType: assign.TaskRefactor},
		{ID: "bug", Title: "Fix bug", Priority: 1, TaskType: assign.TaskBug},
	}

	agents := []assign.Agent{
		{ID: "claude", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "codex", AgentType: tmux.AgentCodex, Idle: true},
	}

	strategies := []assign.Strategy{
		assign.StrategyBalanced,
		assign.StrategySpeed,
		assign.StrategyQuality,
		assign.StrategyDependency,
	}

	matcher := assign.NewMatcher()

	for _, strategy := range strategies {
		assignments := matcher.AssignTasks(beads, agents, strategy)

		t.Logf("BV_TEST: Strategy=%s | Assignments=%d", strategy, len(assignments))
		for _, a := range assignments {
			t.Logf("  %s -> %s (score=%.2f, confidence=%.2f)",
				a.Bead.ID, a.Agent.AgentType, a.Score, a.Confidence)
		}
	}
}

// TestEmptyInputs verifies graceful handling of empty inputs.
func TestEmptyInputs(t *testing.T) {
	matcher := assign.NewMatcher()

	tests := []struct {
		name   string
		beads  []assign.Bead
		agents []assign.Agent
	}{
		{"no beads", nil, []assign.Agent{{ID: "1", Idle: true}}},
		{"no agents", []assign.Bead{{ID: "1"}}, nil},
		{"both empty", nil, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assignments := matcher.AssignTasks(tc.beads, tc.agents, assign.StrategyBalanced)
			if assignments != nil {
				t.Errorf("Expected nil for %s, got %v", tc.name, assignments)
			}
		})
	}
}

// TestReasoningGeneration verifies assignment reasons are generated.
func TestReasoningGeneration(t *testing.T) {
	beads := []assign.Bead{
		{ID: "critical", Title: "Critical bug", Priority: 0, TaskType: assign.TaskBug},
	}

	agents := []assign.Agent{
		{ID: "1", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: 0.5},
	}

	matcher := assign.NewMatcher()
	assignments := matcher.AssignTasks(beads, agents, assign.StrategyQuality)

	if len(assignments) != 1 {
		t.Fatal("Expected 1 assignment")
	}

	reason := assignments[0].Reason
	if reason == "" {
		t.Error("Expected non-empty reason")
	}

	// Should mention critical priority
	t.Logf("BV_TEST: ReasonGeneration | Reason=%q", reason)
}

// TestGetReadyPreviewIntegration tests converting ready beads to assignment format.
func TestGetReadyPreviewIntegration(t *testing.T) {
	// Create mock BeadPreview data (simulating bv output)
	previews := []BeadPreview{
		{ID: "bd-001", Title: "Fix authentication bug", Priority: "P0"},
		{ID: "bd-002", Title: "Add unit tests", Priority: "P1"},
		{ID: "bd-003", Title: "Update README", Priority: "P2"},
	}

	// Convert to assignment beads
	beads := beadPreviewsToBeads(previews)

	if len(beads) != 3 {
		t.Fatalf("Expected 3 beads, got %d", len(beads))
	}

	// Verify priority conversion
	expectedPriorities := []int{0, 1, 2}
	for i, b := range beads {
		if b.Priority != expectedPriorities[i] {
			t.Errorf("Bead %d priority = %d, want %d", i, b.Priority, expectedPriorities[i])
		}
	}

	t.Logf("BV_TEST: BeadPreviewConversion | Count=%d", len(beads))
}

// beadPreviewsToBeads converts BeadPreview to assign.Bead.
func beadPreviewsToBeads(previews []BeadPreview) []assign.Bead {
	beads := make([]assign.Bead, 0, len(previews))
	for _, p := range previews {
		beads = append(beads, assign.Bead{
			ID:       p.ID,
			Title:    p.Title,
			Priority: parsePriority(p.Priority),
			TaskType: assign.ParseTaskType(p.Title),
		})
	}
	return beads
}

// parsePriority converts "P0"-"P4" to int.
func parsePriority(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 {
			return int(n)
		}
	}
	return 2 // Default to P2
}
