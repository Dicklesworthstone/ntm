package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// detectIdleAgents
// =============================================================================

func TestDetectIdleAgents_AllIdle(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
		{Index: 1, Title: "__cc_1", Active: true},
		{Index: 2, Title: "__cod_1", Active: true},
	}

	idle := detectIdleAgents(store, panes, "", 0)

	if len(idle) != 2 {
		t.Fatalf("expected 2 idle agents, got %d", len(idle))
	}
	if idle[0].Pane != 1 {
		t.Errorf("first idle pane should be 1, got %d", idle[0].Pane)
	}
	if idle[1].Pane != 2 {
		t.Errorf("second idle pane should be 2, got %d", idle[1].Pane)
	}
}

func TestDetectIdleAgents_BusyAgentExcluded(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	_, _ = store.Assign("bd-123", "Fix bug", 1, "claude", "", "")

	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
		{Index: 1, Title: "__cc_1", Active: true},
		{Index: 2, Title: "__cod_1", Active: true},
	}

	idle := detectIdleAgents(store, panes, "", 0)

	if len(idle) != 1 {
		t.Fatalf("expected 1 idle agent, got %d", len(idle))
	}
	if idle[0].Pane != 2 {
		t.Errorf("idle pane should be 2, got %d", idle[0].Pane)
	}
}

func TestDetectIdleAgents_FilterByType(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
		{Index: 1, Title: "__cc_1", Active: true},
		{Index: 2, Title: "__cod_1", Active: true},
		{Index: 3, Title: "__cc_2", Active: true},
	}

	idle := detectIdleAgents(store, panes, "cc", 0)

	if len(idle) != 2 {
		t.Fatalf("expected 2 idle cc agents, got %d", len(idle))
	}
	for _, a := range idle {
		if !strings.Contains(strings.ToLower(a.AgentType), "claude") && !strings.HasPrefix(a.AgentType, "cc") {
			t.Errorf("agent type %q should match cc filter", a.AgentType)
		}
	}
}

func TestDetectIdleAgents_NoPanes(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	idle := detectIdleAgents(store, nil, "", 0)

	if len(idle) != 0 {
		t.Errorf("expected 0 idle agents, got %d", len(idle))
	}
}

func TestDetectIdleAgents_SkipsUserPane(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
	}

	idle := detectIdleAgents(store, panes, "", 0)

	if len(idle) != 0 {
		t.Errorf("expected 0 idle agents (user pane skipped), got %d", len(idle))
	}
}

func TestDetectIdleAgents_IdleThreshold(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")

	// Create a completed assignment with recent completion time
	a, _ := store.Assign("bd-100", "Task A", 1, "claude", "", "")
	_ = store.UpdateStatus(a.BeadID, assignment.StatusWorking)
	_ = store.UpdateStatus(a.BeadID, assignment.StatusCompleted)

	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
		{Index: 1, Title: "__cc_1", Active: true},
	}

	// With a very high threshold, the recently-completed agent won't be idle
	idle := detectIdleAgents(store, panes, "", 1*time.Hour)
	if len(idle) != 0 {
		t.Errorf("expected 0 idle agents with 1h threshold, got %d", len(idle))
	}

	// With zero threshold, agent should be idle
	idle = detectIdleAgents(store, panes, "", 0)
	if len(idle) != 1 {
		t.Errorf("expected 1 idle agent with 0 threshold, got %d", len(idle))
	}
}

func TestDetectIdleAgents_LastTaskInfo(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")

	a, _ := store.Assign("bd-200", "Implement auth", 1, "claude", "", "")
	_ = store.UpdateStatus(a.BeadID, assignment.StatusWorking)
	_ = store.UpdateStatus(a.BeadID, assignment.StatusCompleted)

	panes := []tmux.Pane{
		{Index: 0, Title: "user", Active: true},
		{Index: 1, Title: "__cc_1", Active: true},
	}

	idle := detectIdleAgents(store, panes, "", 0)
	if len(idle) != 1 {
		t.Fatalf("expected 1 idle agent, got %d", len(idle))
	}
	if idle[0].LastTask != "Implement auth" {
		t.Errorf("last task should be %q, got %q", "Implement auth", idle[0].LastTask)
	}
}

// =============================================================================
// assignSuggestionsToAgents
// =============================================================================

func TestAssignSuggestionsToAgents_RoundRobin(t *testing.T) {
	t.Parallel()

	suggestions := []ReviewSuggestion{
		{Prompt: "review A", Source: "agent_work"},
		{Prompt: "review B", Source: "git_commit"},
		{Prompt: "review C", Source: "agent_work"},
	}

	agents := []IdleAgent{
		{Pane: 1, AgentType: "claude"},
		{Pane: 2, AgentType: "codex"},
	}

	result := assignSuggestionsToAgents(suggestions, agents)

	if len(result) != 3 {
		t.Fatalf("expected 3 suggestions, got %d", len(result))
	}
	// Round-robin: 0->agent[0], 1->agent[1], 2->agent[0]
	if result[0].Pane != 1 {
		t.Errorf("suggestion 0 should go to pane 1, got %d", result[0].Pane)
	}
	if result[1].Pane != 2 {
		t.Errorf("suggestion 1 should go to pane 2, got %d", result[1].Pane)
	}
	if result[2].Pane != 1 {
		t.Errorf("suggestion 2 should go to pane 1, got %d", result[2].Pane)
	}
}

func TestAssignSuggestionsToAgents_EmptySuggestions(t *testing.T) {
	t.Parallel()

	agents := []IdleAgent{{Pane: 1, AgentType: "claude"}}
	result := assignSuggestionsToAgents(nil, agents)

	if result != nil {
		t.Errorf("expected nil for empty suggestions, got %v", result)
	}
}

func TestAssignSuggestionsToAgents_EmptyAgents(t *testing.T) {
	t.Parallel()

	suggestions := []ReviewSuggestion{{Prompt: "review A"}}
	result := assignSuggestionsToAgents(suggestions, nil)

	if result != nil {
		t.Errorf("expected nil for empty agents, got %v", result)
	}
}

func TestAssignSuggestionsToAgents_SingleAgent(t *testing.T) {
	t.Parallel()

	suggestions := []ReviewSuggestion{
		{Prompt: "A"},
		{Prompt: "B"},
		{Prompt: "C"},
	}
	agents := []IdleAgent{
		{Pane: 5, AgentType: "claude", AgentName: "BlueLake"},
	}

	result := assignSuggestionsToAgents(suggestions, agents)

	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	for i, s := range result {
		if s.Pane != 5 {
			t.Errorf("suggestion %d: expected pane 5, got %d", i, s.Pane)
		}
		if s.Agent != "BlueLake" {
			t.Errorf("suggestion %d: expected agent BlueLake, got %q", i, s.Agent)
		}
	}
}

// =============================================================================
// agentLabel
// =============================================================================

func TestAgentLabel_WithName(t *testing.T) {
	t.Parallel()

	a := IdleAgent{Pane: 1, AgentType: "claude", AgentName: "BlueLake"}
	if got := agentLabel(a); got != "BlueLake" {
		t.Errorf("agentLabel() = %q, want %q", got, "BlueLake")
	}
}

func TestAgentLabel_WithoutName(t *testing.T) {
	t.Parallel()

	a := IdleAgent{Pane: 3, AgentType: "codex"}
	if got := agentLabel(a); got != "codex_3" {
		t.Errorf("agentLabel() = %q, want %q", got, "codex_3")
	}
}

// =============================================================================
// matchesReviewQueueFilter (delegates to matchesRebalanceFilter)
// =============================================================================

func TestMatchesReviewQueueFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		filter    string
		want      bool
	}{
		{"claude", "cc", true},
		{"cc", "cc", true},
		{"codex", "cc", false},
		{"codex", "cod", true},
		{"gemini", "gmi", true},
		{"claude", "", true},
		{"codex", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.agentType+"_"+tt.filter, func(t *testing.T) {
			t.Parallel()
			got := matchesReviewQueueFilter(tt.agentType, tt.filter)
			if got != tt.want {
				t.Errorf("matchesReviewQueueFilter(%q, %q) = %v, want %v",
					tt.agentType, tt.filter, got, tt.want)
			}
		})
	}
}

// =============================================================================
// minInt
// =============================================================================

func TestMinInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b, want int
	}{
		{3, 5, 3},
		{5, 3, 3},
		{0, 0, 0},
		{-1, 1, -1},
		{8, 8, 8},
	}

	for _, tt := range tests {
		if got := minInt(tt.a, tt.b); got != tt.want {
			t.Errorf("minInt(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// =============================================================================
// generateReviewSuggestions
// =============================================================================

func TestGenerateReviewSuggestions_NoIdleAgents(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	result := generateReviewSuggestions(store, nil, 5)

	if result != nil {
		t.Errorf("expected nil for no idle agents, got %v", result)
	}
}

func TestGenerateReviewSuggestions_CompletedWork(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")

	// Create a completed assignment as review source
	a, _ := store.Assign("bd-300", "Refactor auth", 2, "codex", "", "")
	_ = store.UpdateStatus(a.BeadID, assignment.StatusWorking)
	_ = store.UpdateStatus(a.BeadID, assignment.StatusCompleted)

	idleAgents := []IdleAgent{
		{Pane: 1, AgentType: "claude"},
	}

	// commitLimit=0 means no git commits - only agent work
	result := generateReviewSuggestions(store, idleAgents, 0)

	if len(result) == 0 {
		t.Fatal("expected at least 1 suggestion from completed work")
	}

	found := false
	for _, s := range result {
		if s.Source == "agent_work" && strings.Contains(s.SourceRef, "bd-300") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected suggestion from completed bead bd-300")
	}
}

func TestGenerateReviewSuggestions_AgentWorkPromptFormat(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")

	a, _ := store.Assign("bd-400", "Add login page", 2, "codex", "", "")
	_ = store.UpdateStatus(a.BeadID, assignment.StatusWorking)
	_ = store.UpdateStatus(a.BeadID, assignment.StatusCompleted)

	idleAgents := []IdleAgent{
		{Pane: 1, AgentType: "claude"},
	}

	result := generateReviewSuggestions(store, idleAgents, 0)

	if len(result) < 1 {
		t.Fatal("expected at least 1 suggestion")
	}

	agentWorkSuggestion := result[0]
	if !strings.Contains(agentWorkSuggestion.Prompt, "bd-400") {
		t.Errorf("prompt should contain bead ID, got %q", agentWorkSuggestion.Prompt)
	}
	if !strings.Contains(agentWorkSuggestion.Prompt, "Add login page") {
		t.Errorf("prompt should contain bead title, got %q", agentWorkSuggestion.Prompt)
	}
}

// =============================================================================
// ReviewQueueResponse struct
// =============================================================================

func TestReviewQueueResponse_EmptyState(t *testing.T) {
	t.Parallel()

	resp := ReviewQueueResponse{
		Session:     "test",
		IdleAgents:  []IdleAgent{},
		Suggestions: []ReviewSuggestion{},
	}

	if resp.Session != "test" {
		t.Errorf("session = %q, want %q", resp.Session, "test")
	}
	if len(resp.IdleAgents) != 0 {
		t.Errorf("expected 0 idle agents, got %d", len(resp.IdleAgents))
	}
	if len(resp.Suggestions) != 0 {
		t.Errorf("expected 0 suggestions, got %d", len(resp.Suggestions))
	}
}

// =============================================================================
// printReviewQueueReport - output formatting
// =============================================================================

func TestPrintReviewQueueReport_NoIdle(t *testing.T) {
	t.Parallel()

	resp := ReviewQueueResponse{
		Session:     "test",
		IdleAgents:  []IdleAgent{},
		Suggestions: []ReviewSuggestion{},
	}

	// Just ensure it doesn't panic
	printReviewQueueReport(resp)
}

func TestPrintReviewQueueReport_WithSuggestions(t *testing.T) {
	t.Parallel()

	resp := ReviewQueueResponse{
		Session: "test",
		IdleAgents: []IdleAgent{
			{Pane: 1, AgentType: "claude", IdleDuration: "5m", LastTask: "Fix auth"},
		},
		Suggestions: []ReviewSuggestion{
			{Agent: "claude_1", Pane: 1, AgentType: "claude", Prompt: "Review auth.go", Source: "agent_work"},
		},
	}

	// Just ensure it doesn't panic
	printReviewQueueReport(resp)
}

func TestPrintReviewQueueReport_IdleButNoSuggestions(t *testing.T) {
	t.Parallel()

	resp := ReviewQueueResponse{
		Session: "test",
		IdleAgents: []IdleAgent{
			{Pane: 1, AgentType: "claude"},
		},
		Suggestions: []ReviewSuggestion{},
	}

	// Just ensure it doesn't panic
	printReviewQueueReport(resp)
}
