package robot

import (
	"testing"
	"time"
)

func TestDefaultRoutingConfig(t *testing.T) {
	cfg := DefaultRoutingConfig()

	// Check weights sum to 1.0
	totalWeight := cfg.ContextWeight + cfg.StateWeight + cfg.RecencyWeight
	if totalWeight != 1.0 {
		t.Errorf("Weights should sum to 1.0, got %f", totalWeight)
	}

	// Check default values
	if cfg.ContextWeight != 0.4 {
		t.Errorf("ContextWeight = %f, want 0.4", cfg.ContextWeight)
	}
	if cfg.StateWeight != 0.4 {
		t.Errorf("StateWeight = %f, want 0.4", cfg.StateWeight)
	}
	if cfg.RecencyWeight != 0.2 {
		t.Errorf("RecencyWeight = %f, want 0.2", cfg.RecencyWeight)
	}
	if cfg.AffinityEnabled {
		t.Error("AffinityEnabled should be false by default")
	}
	if cfg.ExcludeContextAbove != 85.0 {
		t.Errorf("ExcludeContextAbove = %f, want 85.0", cfg.ExcludeContextAbove)
	}
}

func TestStateToScore(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	tests := []struct {
		name  string
		state AgentState
		want  float64
	}{
		{"waiting", StateWaiting, 100},
		{"thinking", StateThinking, 50},
		{"generating", StateGenerating, 0},
		{"stalled", StateStalled, -50},
		{"error", StateError, -100},
		{"unknown", StateUnknown, 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scorer.stateToScore(tt.state)
			if got != tt.want {
				t.Errorf("stateToScore(%s) = %f, want %f", tt.state, got, tt.want)
			}
		})
	}
}

func TestRecencyToScore(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	tests := []struct {
		name       string
		age        time.Duration
		wantApprox float64
	}{
		{"zero time", 0, 50},
		{"30 seconds", 30 * time.Second, 20},
		{"3 minutes", 3 * time.Minute, 50},
		{"10 minutes", 10 * time.Minute, 80},
		{"1 hour", time.Hour, 70},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lastActivity time.Time
			if tt.age != 0 {
				lastActivity = time.Now().Add(-tt.age)
			}
			got := scorer.recencyToScore(lastActivity)
			if got != tt.wantApprox {
				t.Errorf("recencyToScore(%v ago) = %f, want %f", tt.age, got, tt.wantApprox)
			}
		})
	}
}

func TestCheckExclusion(t *testing.T) {
	cfg := DefaultRoutingConfig()
	scorer := NewAgentScorer(cfg)

	tests := []struct {
		name       string
		agent      ScoredAgent
		wantExcl   bool
		wantReason string
	}{
		{
			name:       "error state",
			agent:      ScoredAgent{State: StateError},
			wantExcl:   true,
			wantReason: "agent in ERROR state",
		},
		{
			name:       "rate limited",
			agent:      ScoredAgent{State: StateWaiting, RateLimited: true},
			wantExcl:   true,
			wantReason: "agent is rate limited",
		},
		{
			name:       "unhealthy",
			agent:      ScoredAgent{State: StateWaiting, HealthState: HealthUnhealthy},
			wantExcl:   true,
			wantReason: "agent is unhealthy",
		},
		{
			name:       "high context",
			agent:      ScoredAgent{State: StateWaiting, ContextUsage: 90},
			wantExcl:   true,
			wantReason: "context usage above threshold",
		},
		{
			name:       "generating",
			agent:      ScoredAgent{State: StateGenerating},
			wantExcl:   true,
			wantReason: "agent is currently generating",
		},
		{
			name:     "healthy waiting",
			agent:    ScoredAgent{State: StateWaiting, ContextUsage: 50},
			wantExcl: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotExcl, gotReason := scorer.checkExclusion(&tt.agent)
			if gotExcl != tt.wantExcl {
				t.Errorf("checkExclusion() excluded = %v, want %v", gotExcl, tt.wantExcl)
			}
			if tt.wantExcl && gotReason != tt.wantReason {
				t.Errorf("checkExclusion() reason = %q, want %q", gotReason, tt.wantReason)
			}
		})
	}
}

func TestCalculateFinalScore(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	agent := &ScoredAgent{
		ScoreDetail: ScoreBreakdown{
			ContextScore:   80,
			StateScore:     100, // (100+100)/2 = 100 normalized
			RecencyScore:   50,
			ContextContrib: 80 * 0.4,  // 32
			StateContrib:   100 * 0.4, // 40
			RecencyContrib: 50 * 0.2,  // 10
		},
	}

	score := scorer.calculateFinalScore(agent)
	// Expected: 32 + 40 + 10 = 82
	expected := 82.0
	if score != expected {
		t.Errorf("calculateFinalScore() = %f, want %f", score, expected)
	}
}

func TestDeriveHealthState(t *testing.T) {
	tests := []struct {
		state AgentState
		want  HealthState
	}{
		{StateWaiting, HealthHealthy},
		{StateThinking, HealthHealthy},
		{StateGenerating, HealthHealthy},
		{StateStalled, HealthDegraded},
		{StateError, HealthUnhealthy},
		{StateUnknown, HealthHealthy},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			got := deriveHealthState(tt.state)
			if got != tt.want {
				t.Errorf("deriveHealthState(%s) = %s, want %s", tt.state, got, tt.want)
			}
		})
	}
}

func TestGetBestAgent(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	agents := []ScoredAgent{
		{PaneID: "cc_1", Score: 50, Excluded: false},
		{PaneID: "cc_2", Score: 80, Excluded: false},
		{PaneID: "cc_3", Score: 100, Excluded: true}, // Excluded, should not be selected
		{PaneID: "cc_4", Score: 60, Excluded: false},
	}

	best := scorer.GetBestAgent(agents)
	if best == nil {
		t.Fatal("GetBestAgent() returned nil")
	}
	if best.PaneID != "cc_2" {
		t.Errorf("GetBestAgent() = %s, want cc_2", best.PaneID)
	}
}

func TestGetBestAgent_AllExcluded(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	agents := []ScoredAgent{
		{PaneID: "cc_1", Score: 50, Excluded: true},
		{PaneID: "cc_2", Score: 80, Excluded: true},
	}

	best := scorer.GetBestAgent(agents)
	if best != nil {
		t.Errorf("GetBestAgent() should return nil when all excluded, got %s", best.PaneID)
	}
}

func TestGetAvailableAgents(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	agents := []ScoredAgent{
		{PaneID: "cc_1", Score: 50, Excluded: false},
		{PaneID: "cc_2", Score: 80, Excluded: false},
		{PaneID: "cc_3", Score: 100, Excluded: true},
		{PaneID: "cc_4", Score: 60, Excluded: false},
	}

	available := scorer.GetAvailableAgents(agents)
	if len(available) != 3 {
		t.Errorf("GetAvailableAgents() returned %d agents, want 3", len(available))
	}

	// Check sorted by score descending
	if available[0].PaneID != "cc_2" {
		t.Errorf("First available should be cc_2, got %s", available[0].PaneID)
	}
	if available[1].PaneID != "cc_4" {
		t.Errorf("Second available should be cc_4, got %s", available[1].PaneID)
	}
	if available[2].PaneID != "cc_1" {
		t.Errorf("Third available should be cc_1, got %s", available[2].PaneID)
	}
}

func TestFilterByType(t *testing.T) {
	agents := []ScoredAgent{
		{PaneID: "cc_1", AgentType: "cc"},
		{PaneID: "cod_1", AgentType: "cod"},
		{PaneID: "cc_2", AgentType: "cc"},
		{PaneID: "gmi_1", AgentType: "gmi"},
	}

	// Filter for claude
	filtered := FilterByType(agents, "cc")
	if len(filtered) != 2 {
		t.Errorf("FilterByType(cc) returned %d agents, want 2", len(filtered))
	}

	// Case insensitive
	filtered = FilterByType(agents, "CC")
	if len(filtered) != 2 {
		t.Errorf("FilterByType(CC) should be case insensitive")
	}

	// Empty filter returns all
	filtered = FilterByType(agents, "")
	if len(filtered) != 4 {
		t.Errorf("FilterByType('') should return all agents")
	}
}

func TestFilterByPanes(t *testing.T) {
	agents := []ScoredAgent{
		{PaneID: "cc_1", PaneIndex: 1},
		{PaneID: "cc_2", PaneIndex: 2},
		{PaneID: "cc_3", PaneIndex: 3},
		{PaneID: "cc_4", PaneIndex: 4},
	}

	// Filter for panes 2 and 3
	filtered := FilterByPanes(agents, []int{2, 3})
	if len(filtered) != 2 {
		t.Errorf("FilterByPanes([2,3]) returned %d agents, want 2", len(filtered))
	}

	// Empty filter returns all
	filtered = FilterByPanes(agents, []int{})
	if len(filtered) != 4 {
		t.Errorf("FilterByPanes([]) should return all agents")
	}
}

func TestExcludePanes(t *testing.T) {
	agents := []ScoredAgent{
		{PaneID: "cc_1", PaneIndex: 1},
		{PaneID: "cc_2", PaneIndex: 2},
		{PaneID: "cc_3", PaneIndex: 3},
		{PaneID: "cc_4", PaneIndex: 4},
	}

	// Exclude panes 2 and 3
	filtered := ExcludePanes(agents, []int{2, 3})
	if len(filtered) != 2 {
		t.Errorf("ExcludePanes([2,3]) returned %d agents, want 2", len(filtered))
	}

	// Check the right panes remain
	for _, a := range filtered {
		if a.PaneIndex == 2 || a.PaneIndex == 3 {
			t.Errorf("ExcludePanes should have excluded pane %d", a.PaneIndex)
		}
	}

	// Empty exclusion returns all
	filtered = ExcludePanes(agents, []int{})
	if len(filtered) != 4 {
		t.Errorf("ExcludePanes([]) should return all agents")
	}
}

func TestHealthStateConstants(t *testing.T) {
	// Verify health state string values
	if HealthHealthy != "healthy" {
		t.Errorf("HealthHealthy = %q, want %q", HealthHealthy, "healthy")
	}
	if HealthDegraded != "degraded" {
		t.Errorf("HealthDegraded = %q, want %q", HealthDegraded, "degraded")
	}
	if HealthUnhealthy != "unhealthy" {
		t.Errorf("HealthUnhealthy = %q, want %q", HealthUnhealthy, "unhealthy")
	}
	if HealthRateLimited != "rate_limited" {
		t.Errorf("HealthRateLimited = %q, want %q", HealthRateLimited, "rate_limited")
	}
}

func TestCalculateScoreComponents(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	agent := &ScoredAgent{
		ContextUsage: 30, // 30% used -> 70 context score
		State:        StateWaiting,
		LastActivity: time.Now().Add(-10 * time.Minute), // 10 min ago -> 80 recency
	}

	breakdown := scorer.calculateScoreComponents(agent, "")

	// Context score: 100 - 30 = 70
	if breakdown.ContextScore != 70 {
		t.Errorf("ContextScore = %f, want 70", breakdown.ContextScore)
	}

	// State score: WAITING = 100 raw, normalized = (100+100)/2 = 100
	if breakdown.StateScore != 100 {
		t.Errorf("StateScore = %f, want 100", breakdown.StateScore)
	}

	// Recency score: 10 min ago -> 80
	if breakdown.RecencyScore != 80 {
		t.Errorf("RecencyScore = %f, want 80", breakdown.RecencyScore)
	}

	// Verify contributions use weights
	if breakdown.ContextContrib != 70*0.4 {
		t.Errorf("ContextContrib = %f, want %f", breakdown.ContextContrib, 70*0.4)
	}
	if breakdown.StateContrib != 100*0.4 {
		t.Errorf("StateContrib = %f, want %f", breakdown.StateContrib, 100*0.4)
	}
	if breakdown.RecencyContrib != 80*0.2 {
		t.Errorf("RecencyContrib = %f, want %f", breakdown.RecencyContrib, 80*0.2)
	}
}

func TestExcludeIfGeneratingConfig(t *testing.T) {
	// Test with ExcludeIfGenerating = false
	cfg := DefaultRoutingConfig()
	cfg.ExcludeIfGenerating = false
	scorer := NewAgentScorer(cfg)

	agent := &ScoredAgent{State: StateGenerating}
	excluded, _ := scorer.checkExclusion(agent)
	if excluded {
		t.Error("Agent should not be excluded when ExcludeIfGenerating = false")
	}

	// Test with ExcludeIfGenerating = true (default)
	cfg.ExcludeIfGenerating = true
	scorer = NewAgentScorer(cfg)
	excluded, _ = scorer.checkExclusion(agent)
	if !excluded {
		t.Error("Agent should be excluded when ExcludeIfGenerating = true")
	}
}
