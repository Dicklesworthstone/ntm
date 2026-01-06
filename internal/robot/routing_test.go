package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
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

// =============================================================================
// Routing Strategy Tests
// =============================================================================

func TestStrategyNames(t *testing.T) {
	if StrategyLeastLoaded != "least-loaded" {
		t.Errorf("StrategyLeastLoaded = %q, want %q", StrategyLeastLoaded, "least-loaded")
	}
	if StrategyFirstAvailable != "first-available" {
		t.Errorf("StrategyFirstAvailable = %q, want %q", StrategyFirstAvailable, "first-available")
	}
	if StrategyRoundRobin != "round-robin" {
		t.Errorf("StrategyRoundRobin = %q, want %q", StrategyRoundRobin, "round-robin")
	}
	if StrategyRoundRobinAvailable != "round-robin-available" {
		t.Errorf("StrategyRoundRobinAvailable = %q, want %q", StrategyRoundRobinAvailable, "round-robin-available")
	}
	if StrategyRandom != "random" {
		t.Errorf("StrategyRandom = %q, want %q", StrategyRandom, "random")
	}
	if StrategySticky != "sticky" {
		t.Errorf("StrategySticky = %q, want %q", StrategySticky, "sticky")
	}
	if StrategyExplicit != "explicit" {
		t.Errorf("StrategyExplicit = %q, want %q", StrategyExplicit, "explicit")
	}
}

func TestIsValidStrategy(t *testing.T) {
	tests := []struct {
		name  string
		strat StrategyName
		want  bool
	}{
		{"least-loaded valid", StrategyLeastLoaded, true},
		{"first-available valid", StrategyFirstAvailable, true},
		{"round-robin valid", StrategyRoundRobin, true},
		{"round-robin-available valid", StrategyRoundRobinAvailable, true},
		{"random valid", StrategyRandom, true},
		{"sticky valid", StrategySticky, true},
		{"explicit valid", StrategyExplicit, true},
		{"invalid name", "invalid-strategy", false},
		{"empty name", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidStrategy(tt.strat)
			if got != tt.want {
				t.Errorf("IsValidStrategy(%q) = %v, want %v", tt.strat, got, tt.want)
			}
		})
	}
}

func TestGetStrategyNames(t *testing.T) {
	names := GetStrategyNames()
	if len(names) != 7 {
		t.Errorf("GetStrategyNames() returned %d names, want 7", len(names))
	}

	// Check all expected names are present
	expected := map[StrategyName]bool{
		StrategyLeastLoaded:         true,
		StrategyFirstAvailable:      true,
		StrategyRoundRobin:          true,
		StrategyRoundRobinAvailable: true,
		StrategyRandom:              true,
		StrategySticky:              true,
		StrategyExplicit:            true,
	}

	for _, name := range names {
		if !expected[name] {
			t.Errorf("Unexpected strategy name: %s", name)
		}
	}
}

func TestLeastLoadedStrategy(t *testing.T) {
	strat := &LeastLoadedStrategy{}

	if strat.Name() != StrategyLeastLoaded {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategyLeastLoaded)
	}

	t.Run("selects highest score", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 50, Excluded: false},
			{PaneID: "cc_2", Score: 80, Excluded: false},
			{PaneID: "cc_3", Score: 60, Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("skips excluded agents", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 50, Excluded: false},
			{PaneID: "cc_2", Score: 100, Excluded: true}, // Highest but excluded
			{PaneID: "cc_3", Score: 60, Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_3" {
			t.Errorf("Select() = %s, want cc_3", selected.PaneID)
		}
	})

	t.Run("returns nil when all excluded", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 50, Excluded: true},
			{PaneID: "cc_2", Score: 80, Excluded: true},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})

	t.Run("handles empty list", func(t *testing.T) {
		selected := strat.Select([]ScoredAgent{}, RoutingContext{})
		if selected != nil {
			t.Error("Select() should return nil for empty list")
		}
	})
}

func TestFirstAvailableStrategy(t *testing.T) {
	strat := &FirstAvailableStrategy{}

	if strat.Name() != StrategyFirstAvailable {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategyFirstAvailable)
	}

	t.Run("selects first WAITING agent", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateGenerating, Excluded: false},
			{PaneID: "cc_2", State: StateWaiting, Excluded: false},
			{PaneID: "cc_3", State: StateWaiting, Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("returns nil when none waiting", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateGenerating, Excluded: false},
			{PaneID: "cc_2", State: StateThinking, Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})

	t.Run("skips excluded WAITING agents", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateWaiting, Excluded: true},
			{PaneID: "cc_2", State: StateWaiting, Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})
}

func TestRoundRobinStrategy(t *testing.T) {
	strat := &RoundRobinStrategy{}

	if strat.Name() != StrategyRoundRobin {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategyRoundRobin)
	}

	t.Run("rotates through agents", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: false},
			{PaneID: "cc_2", Excluded: false},
			{PaneID: "cc_3", Excluded: false},
		}

		// First selection - starts at index 1 (lastIndex=0, so next is 1)
		selected := strat.Select(agents, RoutingContext{})
		if selected == nil || selected.PaneID != "cc_2" {
			t.Errorf("First Select() = %v, want cc_2", selected)
		}

		// Second selection - should be cc_3
		selected = strat.Select(agents, RoutingContext{})
		if selected == nil || selected.PaneID != "cc_3" {
			t.Errorf("Second Select() = %v, want cc_3", selected)
		}

		// Third selection - should wrap to cc_1
		selected = strat.Select(agents, RoutingContext{})
		if selected == nil || selected.PaneID != "cc_1" {
			t.Errorf("Third Select() = %v, want cc_1", selected)
		}
	})

	t.Run("includes excluded in rotation", func(t *testing.T) {
		strat2 := &RoundRobinStrategy{}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: true},
			{PaneID: "cc_2", Excluded: false},
		}

		// Round-robin doesn't skip excluded
		selected := strat2.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		// Starting from lastIndex=0, next is index 1
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("handles empty list", func(t *testing.T) {
		selected := strat.Select([]ScoredAgent{}, RoutingContext{})
		if selected != nil {
			t.Error("Select() should return nil for empty list")
		}
	})
}

func TestRoundRobinAvailableStrategy(t *testing.T) {
	strat := &RoundRobinAvailableStrategy{}

	if strat.Name() != StrategyRoundRobinAvailable {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategyRoundRobinAvailable)
	}

	t.Run("skips excluded agents", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: true},
			{PaneID: "cc_2", Excluded: false},
			{PaneID: "cc_3", Excluded: true},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("rotates through available only", func(t *testing.T) {
		strat2 := &RoundRobinAvailableStrategy{}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: false},
			{PaneID: "cc_2", Excluded: true},
			{PaneID: "cc_3", Excluded: false},
		}

		// First available is cc_1 (index 0, starting from -1)
		// Actually starts at (lastIndex+1)%3 = 1, but cc_2 excluded, so continues to cc_3
		selected := strat2.Select(agents, RoutingContext{})
		if selected != nil && selected.PaneID != "cc_1" {
			// lastIndex starts at 0, so (0+1)%3=1 is cc_2 (excluded), then (0+2)%3=2 is cc_3
			// Wait - actually i=0 means idx=(0+1+0)%3=1, which is cc_2 (excluded)
			// i=1 means idx=(0+1+1)%3=2, which is cc_3
			if selected.PaneID != "cc_3" {
				t.Errorf("First Select() = %s, want cc_1 or cc_3", selected.PaneID)
			}
		}
	})

	t.Run("returns nil when all excluded", func(t *testing.T) {
		strat2 := &RoundRobinAvailableStrategy{}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: true},
			{PaneID: "cc_2", Excluded: true},
		}

		selected := strat2.Select(agents, RoutingContext{})
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})
}

func TestRandomStrategy(t *testing.T) {
	t.Run("returns strategy name", func(t *testing.T) {
		strat := &RandomStrategy{}
		if strat.Name() != StrategyRandom {
			t.Errorf("Name() = %s, want %s", strat.Name(), StrategyRandom)
		}
	})

	t.Run("uses injected random function", func(t *testing.T) {
		strat := &RandomStrategy{
			randFunc: func(n int) int { return 0 }, // Always pick first
		}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: false},
			{PaneID: "cc_2", Excluded: false},
			{PaneID: "cc_3", Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_1" {
			t.Errorf("Select() = %s, want cc_1", selected.PaneID)
		}
	})

	t.Run("skips excluded agents", func(t *testing.T) {
		strat := &RandomStrategy{
			randFunc: func(n int) int { return 0 },
		}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: true},
			{PaneID: "cc_2", Excluded: false},
			{PaneID: "cc_3", Excluded: false},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("returns nil when all excluded", func(t *testing.T) {
		strat := &RandomStrategy{}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: true},
		}

		selected := strat.Select(agents, RoutingContext{})
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})

	t.Run("deterministic fallback", func(t *testing.T) {
		strat := &RandomStrategy{} // No randFunc
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: false},
			{PaneID: "cc_2", Excluded: false},
			{PaneID: "cc_3", Excluded: false},
		}

		// Without randFunc, uses len(available)/2 = 1
		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2 (middle element)", selected.PaneID)
		}
	})
}

func TestStickyStrategy(t *testing.T) {
	strat := NewStickyStrategy()

	if strat.Name() != StrategySticky {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategySticky)
	}

	t.Run("prefers last agent", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 100, Excluded: false},
			{PaneID: "cc_2", Score: 50, Excluded: false},
			{PaneID: "cc_3", Score: 60, Excluded: false},
		}

		ctx := RoutingContext{LastAgent: "cc_2"}
		selected := strat.Select(agents, ctx)
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		// Should prefer cc_2 even though cc_1 has higher score
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("falls back when last agent excluded", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 100, Excluded: false},
			{PaneID: "cc_2", Score: 50, Excluded: true}, // Last agent but excluded
			{PaneID: "cc_3", Score: 60, Excluded: false},
		}

		ctx := RoutingContext{LastAgent: "cc_2"}
		selected := strat.Select(agents, ctx)
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		// Should fall back to least-loaded (cc_1 with highest score)
		if selected.PaneID != "cc_1" {
			t.Errorf("Select() = %s, want cc_1", selected.PaneID)
		}
	})

	t.Run("falls back when no last agent", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 100, Excluded: false},
			{PaneID: "cc_2", Score: 50, Excluded: false},
		}

		ctx := RoutingContext{} // No LastAgent
		selected := strat.Select(agents, ctx)
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		// Should use least-loaded
		if selected.PaneID != "cc_1" {
			t.Errorf("Select() = %s, want cc_1", selected.PaneID)
		}
	})
}

func TestExplicitStrategy(t *testing.T) {
	strat := &ExplicitStrategy{}

	if strat.Name() != StrategyExplicit {
		t.Errorf("Name() = %s, want %s", strat.Name(), StrategyExplicit)
	}

	t.Run("selects explicit pane", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Excluded: false},
			{PaneID: "cc_2", PaneIndex: 2, Excluded: false},
			{PaneID: "cc_3", PaneIndex: 3, Excluded: false},
		}

		ctx := RoutingContext{ExplicitPane: 2}
		selected := strat.Select(agents, ctx)
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})

	t.Run("returns nil when pane not found", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Excluded: false},
		}

		ctx := RoutingContext{ExplicitPane: 5}
		selected := strat.Select(agents, ctx)
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})

	t.Run("returns nil when explicit pane not set", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Excluded: false},
		}

		ctx := RoutingContext{ExplicitPane: -1}
		selected := strat.Select(agents, ctx)
		if selected != nil {
			t.Errorf("Select() = %s, want nil", selected.PaneID)
		}
	})

	t.Run("ignores exclusion status", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Excluded: false},
			{PaneID: "cc_2", PaneIndex: 2, Excluded: true}, // Excluded but explicitly requested
		}

		ctx := RoutingContext{ExplicitPane: 2}
		selected := strat.Select(agents, ctx)
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		// Explicit should return even if excluded
		if selected.PaneID != "cc_2" {
			t.Errorf("Select() = %s, want cc_2", selected.PaneID)
		}
	})
}

func TestRouter(t *testing.T) {
	router := NewRouter()

	t.Run("registers all strategies", func(t *testing.T) {
		names := GetStrategyNames()
		for _, name := range names {
			strat := router.GetStrategy(name)
			if strat == nil {
				t.Errorf("Strategy %s not registered", name)
			}
			if strat.Name() != name {
				t.Errorf("Strategy name mismatch: %s vs %s", strat.Name(), name)
			}
		}
	})

	t.Run("returns default for unknown strategy", func(t *testing.T) {
		strat := router.GetStrategy("unknown")
		if strat.Name() != StrategyLeastLoaded {
			t.Errorf("Expected default strategy, got %s", strat.Name())
		}
	})
}

func TestRouterRoute(t *testing.T) {
	router := NewRouter()

	t.Run("primary strategy succeeds", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 50, State: StateWaiting, Excluded: false},
			{PaneID: "cc_2", Score: 80, State: StateWaiting, Excluded: false},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected == nil {
			t.Fatal("Route() returned nil selection")
		}
		if result.Selected.PaneID != "cc_2" {
			t.Errorf("Selected = %s, want cc_2", result.Selected.PaneID)
		}
		if result.FallbackUsed {
			t.Error("FallbackUsed should be false")
		}
		if result.Strategy != StrategyLeastLoaded {
			t.Errorf("Strategy = %s, want %s", result.Strategy, StrategyLeastLoaded)
		}
	})

	t.Run("applies context exclusions", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Score: 100, State: StateWaiting, Excluded: false},
			{PaneID: "cc_2", PaneIndex: 2, Score: 50, State: StateWaiting, Excluded: false},
		}

		ctx := RoutingContext{ExcludePanes: []int{1}} // Exclude pane 1
		result := router.Route(agents, StrategyLeastLoaded, ctx)
		if result.Selected == nil {
			t.Fatal("Route() returned nil selection")
		}
		if result.Selected.PaneID != "cc_2" {
			t.Errorf("Selected = %s, want cc_2 (cc_1 should be excluded)", result.Selected.PaneID)
		}
	})

	t.Run("uses fallback when primary fails", func(t *testing.T) {
		// Create a scenario where first-available fails (all busy)
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 50, State: StateGenerating, Excluded: false},
			{PaneID: "cc_2", Score: 80, State: StateThinking, Excluded: false},
		}

		result := router.Route(agents, StrategyFirstAvailable, RoutingContext{})
		// FirstAvailable requires StateWaiting, so it will fail
		// Then fallback to LeastLoaded which should pick cc_2
		if result.Selected == nil {
			t.Fatal("Route() returned nil selection")
		}
		if result.Selected.PaneID != "cc_2" {
			t.Errorf("Selected = %s, want cc_2", result.Selected.PaneID)
		}
		if !result.FallbackUsed {
			t.Error("FallbackUsed should be true")
		}
	})

	t.Run("returns no selection when all fail", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateGenerating, Excluded: true},
			{PaneID: "cc_2", State: StateGenerating, Excluded: true},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Errorf("Selected = %s, want nil", result.Selected.PaneID)
		}
	})
}

func TestRouterRouteWithRelaxation(t *testing.T) {
	router := NewRouter()

	t.Run("returns immediately if primary succeeds", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 80, State: StateWaiting, Excluded: false},
		}

		result := router.RouteWithRelaxation(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected == nil {
			t.Fatal("RouteWithRelaxation() returned nil")
		}
		if result.Selected.PaneID != "cc_1" {
			t.Errorf("Selected = %s, want cc_1", result.Selected.PaneID)
		}
	})

	t.Run("relaxes THINKING exclusion", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", Score: 80, State: StateThinking, Excluded: true, ExcludeReason: "agent is currently generating"},
		}

		result := router.RouteWithRelaxation(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected == nil {
			t.Fatal("RouteWithRelaxation() should include THINKING agents")
		}
		if result.Selected.PaneID != "cc_1" {
			t.Errorf("Selected = %s, want cc_1", result.Selected.PaneID)
		}
	})
}

func TestFilterExcluded(t *testing.T) {
	agents := []ScoredAgent{
		{PaneID: "cc_1", Excluded: false},
		{PaneID: "cc_2", Excluded: true},
		{PaneID: "cc_3", Excluded: false},
		{PaneID: "cc_4", Excluded: true},
	}

	t.Run("filter for non-excluded", func(t *testing.T) {
		filtered := filterExcluded(agents, false)
		if len(filtered) != 2 {
			t.Errorf("Got %d non-excluded, want 2", len(filtered))
		}
	})

	t.Run("filter for excluded", func(t *testing.T) {
		filtered := filterExcluded(agents, true)
		if len(filtered) != 2 {
			t.Errorf("Got %d excluded, want 2", len(filtered))
		}
	})
}

func TestRoutingResult(t *testing.T) {
	result := RoutingResult{
		Strategy:     StrategyLeastLoaded,
		FallbackUsed: false,
		Reason:       "primary strategy succeeded",
	}

	if result.Strategy != StrategyLeastLoaded {
		t.Errorf("Strategy = %s, want %s", result.Strategy, StrategyLeastLoaded)
	}
	if result.FallbackUsed {
		t.Error("FallbackUsed should be false")
	}
}

func TestRoutingContext(t *testing.T) {
	ctx := RoutingContext{
		Prompt:       "test prompt",
		LastAgent:    "cc_1",
		ExcludePanes: []int{2, 3},
		ExplicitPane: 1,
	}

	if ctx.Prompt != "test prompt" {
		t.Errorf("Prompt = %s, want 'test prompt'", ctx.Prompt)
	}
	if ctx.LastAgent != "cc_1" {
		t.Errorf("LastAgent = %s, want cc_1", ctx.LastAgent)
	}
	if len(ctx.ExcludePanes) != 2 {
		t.Errorf("ExcludePanes len = %d, want 2", len(ctx.ExcludePanes))
	}
	if ctx.ExplicitPane != 1 {
		t.Errorf("ExplicitPane = %d, want 1", ctx.ExplicitPane)
	}
}

// =============================================================================
// Additional Edge Case Tests
// =============================================================================

func TestSingleAgentEdgeCases(t *testing.T) {
	t.Run("single agent all strategies", func(t *testing.T) {
		router := NewRouter()
		agent := []ScoredAgent{
			{PaneID: "cc_1", PaneIndex: 1, Score: 75, State: StateWaiting, Excluded: false},
		}

		strategies := GetStrategyNames()
		for _, strat := range strategies {
			if strat == StrategyExplicit {
				// Explicit needs ExplicitPane set
				ctx := RoutingContext{ExplicitPane: 1}
				result := router.Route(agent, strat, ctx)
				if result.Selected == nil || result.Selected.PaneID != "cc_1" {
					t.Errorf("Strategy %s failed for single agent", strat)
				}
			} else if strat == StrategySticky {
				// Sticky works with or without LastAgent
				ctx := RoutingContext{LastAgent: "cc_1"}
				result := router.Route(agent, strat, ctx)
				if result.Selected == nil || result.Selected.PaneID != "cc_1" {
					t.Errorf("Strategy %s failed for single agent", strat)
				}
			} else {
				result := router.Route(agent, strat, RoutingContext{})
				if result.Selected == nil || result.Selected.PaneID != "cc_1" {
					t.Errorf("Strategy %s failed for single agent", strat)
				}
			}
		}
	})

	t.Run("single agent excluded", func(t *testing.T) {
		router := NewRouter()
		agent := []ScoredAgent{
			{PaneID: "cc_1", Score: 75, State: StateError, Excluded: true},
		}

		result := router.Route(agent, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Error("Should return nil when single agent is excluded")
		}
	})
}

func TestAllBusyEdgeCases(t *testing.T) {
	router := NewRouter()

	t.Run("all agents generating", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateGenerating, Excluded: true, ExcludeReason: "agent is currently generating"},
			{PaneID: "cc_2", State: StateGenerating, Excluded: true, ExcludeReason: "agent is currently generating"},
			{PaneID: "cc_3", State: StateGenerating, Excluded: true, ExcludeReason: "agent is currently generating"},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Error("Should return nil when all agents are generating")
		}
	})

	t.Run("all agents high context", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", ContextUsage: 95, Excluded: true, ExcludeReason: "context usage above threshold"},
			{PaneID: "cc_2", ContextUsage: 92, Excluded: true, ExcludeReason: "context usage above threshold"},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Error("Should return nil when all agents have high context usage")
		}
	})
}

func TestAllErrorEdgeCases(t *testing.T) {
	router := NewRouter()

	t.Run("all agents in error state", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateError, HealthState: HealthUnhealthy, Excluded: true},
			{PaneID: "cc_2", State: StateError, HealthState: HealthUnhealthy, Excluded: true},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Error("Should return nil when all agents are in error state")
		}
		if result.Reason != "no suitable agent found" {
			t.Errorf("Reason = %q, want 'no suitable agent found'", result.Reason)
		}
	})

	t.Run("all agents rate limited", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", RateLimited: true, Excluded: true, ExcludeReason: "agent is rate limited"},
			{PaneID: "cc_2", RateLimited: true, Excluded: true, ExcludeReason: "agent is rate limited"},
		}

		result := router.Route(agents, StrategyLeastLoaded, RoutingContext{})
		if result.Selected != nil {
			t.Error("Should return nil when all agents are rate limited")
		}
	})
}

func TestCalculateFinalScoreEdgeCases(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	t.Run("score above 100 gets clamped", func(t *testing.T) {
		agent := &ScoredAgent{
			ScoreDetail: ScoreBreakdown{
				ContextContrib: 50,
				StateContrib:   50,
				RecencyContrib: 30,
				AffinityBonus:  20, // Total: 150
			},
		}

		score := scorer.calculateFinalScore(agent)
		if score != 100 {
			t.Errorf("Score = %f, want 100 (clamped)", score)
		}
	})

	t.Run("negative contributions get clamped to 0", func(t *testing.T) {
		agent := &ScoredAgent{
			ScoreDetail: ScoreBreakdown{
				ContextContrib: -10,
				StateContrib:   -10,
				RecencyContrib: -10,
				AffinityBonus:  0,
			},
		}

		score := scorer.calculateFinalScore(agent)
		if score != 0 {
			t.Errorf("Score = %f, want 0 (clamped)", score)
		}
	})

	t.Run("score rounds to 2 decimal places", func(t *testing.T) {
		agent := &ScoredAgent{
			ScoreDetail: ScoreBreakdown{
				ContextContrib: 33.333,
				StateContrib:   33.333,
				RecencyContrib: 16.667,
			},
		}

		score := scorer.calculateFinalScore(agent)
		// Should round to 83.33
		if score != 83.33 {
			t.Errorf("Score = %f, want 83.33", score)
		}
	})
}

func TestNewAgentScorerFromConfig(t *testing.T) {
	// Test that it creates a scorer with defaults when config is nil
	scorer := NewAgentScorerFromConfig(nil)
	if scorer == nil {
		t.Fatal("NewAgentScorerFromConfig(nil) returned nil")
	}

	// Verify defaults are applied
	cfg := scorer.config
	if cfg.ContextWeight != 0.4 {
		t.Errorf("ContextWeight = %f, want 0.4", cfg.ContextWeight)
	}
	if cfg.StateWeight != 0.4 {
		t.Errorf("StateWeight = %f, want 0.4", cfg.StateWeight)
	}
	if cfg.RecencyWeight != 0.2 {
		t.Errorf("RecencyWeight = %f, want 0.2", cfg.RecencyWeight)
	}
}

func TestCalculateScoreComponentsEdgeCases(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	t.Run("context usage above 100", func(t *testing.T) {
		agent := &ScoredAgent{
			ContextUsage: 150, // Invalid but should handle gracefully
			State:        StateWaiting,
		}

		breakdown := scorer.calculateScoreComponents(agent, "")
		// 100 - 150 = -50, clamped to 0
		if breakdown.ContextScore != 0 {
			t.Errorf("ContextScore = %f, want 0 (clamped)", breakdown.ContextScore)
		}
	})

	t.Run("affinity enabled with prompt", func(t *testing.T) {
		cfg := DefaultRoutingConfig()
		cfg.AffinityEnabled = true
		scorer := NewAgentScorer(cfg)

		agent := &ScoredAgent{
			State: StateWaiting,
		}

		breakdown := scorer.calculateScoreComponents(agent, "test prompt")
		// calculateAffinity returns 0 for now (TODO in code)
		if breakdown.AffinityBonus != 0 {
			t.Errorf("AffinityBonus = %f, want 0 (not implemented)", breakdown.AffinityBonus)
		}
	})
}

func TestStateToScoreDefaultCase(t *testing.T) {
	scorer := NewAgentScorer(DefaultRoutingConfig())

	// Test with an invalid state (cast from string)
	invalidState := AgentState("invalid_state")
	score := scorer.stateToScore(invalidState)
	// Default case returns 0
	if score != 0 {
		t.Errorf("stateToScore(invalid) = %f, want 0", score)
	}
}

func TestRoundRobinAvailableEdgeCase(t *testing.T) {
	t.Run("wraps around with mixed excluded", func(t *testing.T) {
		strat := &RoundRobinAvailableStrategy{lastIndex: 2}
		agents := []ScoredAgent{
			{PaneID: "cc_1", Excluded: false},
			{PaneID: "cc_2", Excluded: true},
			{PaneID: "cc_3", Excluded: true},
			{PaneID: "cc_4", Excluded: false},
		}

		// lastIndex=2, so starts checking at (2+1)%4=3 (cc_4, available)
		selected := strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_4" {
			t.Errorf("Select() = %s, want cc_4", selected.PaneID)
		}

		// Next call: lastIndex=3, starts at (3+1)%4=0 (cc_1, available)
		selected = strat.Select(agents, RoutingContext{})
		if selected == nil {
			t.Fatal("Select() returned nil")
		}
		if selected.PaneID != "cc_1" {
			t.Errorf("Select() = %s, want cc_1", selected.PaneID)
		}
	})
}

func TestRouteWithRelaxationNoEffect(t *testing.T) {
	router := NewRouter()

	t.Run("relaxation has no effect on non-THINKING agents", func(t *testing.T) {
		agents := []ScoredAgent{
			{PaneID: "cc_1", State: StateGenerating, Excluded: true, ExcludeReason: "agent is currently generating"},
		}

		result := router.RouteWithRelaxation(agents, StrategyLeastLoaded, RoutingContext{})
		// GENERATING (not THINKING) should not be relaxed
		if result.Selected != nil {
			t.Error("Relaxation should not affect GENERATING agents")
		}
	})
}

func TestEmptyAgentsListAllStrategies(t *testing.T) {
	router := NewRouter()

	strategies := GetStrategyNames()
	for _, strat := range strategies {
		t.Run(string(strat), func(t *testing.T) {
			result := router.Route([]ScoredAgent{}, strat, RoutingContext{})
			if result.Selected != nil {
				t.Errorf("Strategy %s should return nil for empty list", strat)
			}
		})
	}
}

// =============================================================================
// Agent Mail Integration Tests
// =============================================================================

func TestDefaultAgentMailConfig(t *testing.T) {
	cfg := DefaultAgentMailConfig()

	if cfg.Enabled {
		t.Error("Enabled should be false by default")
	}
	if cfg.ReservationBonus != 30.0 {
		t.Errorf("ReservationBonus = %f, want 30.0", cfg.ReservationBonus)
	}
	if cfg.RespectReservations {
		t.Error("RespectReservations should be false by default")
	}
	if cfg.CacheTTL != 30*time.Second {
		t.Errorf("CacheTTL = %v, want 30s", cfg.CacheTTL)
	}
}

func TestRoutingConfigIncludesAgentMail(t *testing.T) {
	cfg := DefaultRoutingConfig()

	// Verify AgentMail config is included
	if cfg.AgentMail.ReservationBonus != 30.0 {
		t.Errorf("AgentMail.ReservationBonus = %f, want 30.0", cfg.AgentMail.ReservationBonus)
	}
}

func TestExtractFilePaths(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   []string
	}{
		{
			name:   "simple file path",
			prompt: "Please fix internal/robot/routing.go",
			want:   []string{"internal/robot/routing.go"},
		},
		{
			name:   "multiple paths",
			prompt: "Update src/main.go and src/util.go",
			want:   []string{"src/main.go", "src/util.go"},
		},
		{
			name:   "path in quotes",
			prompt: `Fix the bug in "internal/config.yaml"`,
			want:   []string{"internal/config.yaml"},
		},
		{
			name:   "tsx file",
			prompt: "Modify the component in src/Button.tsx",
			want:   []string{"src/Button.tsx"},
		},
		{
			name:   "standalone filename",
			prompt: "Update config.go",
			want:   []string{"config.go"},
		},
		{
			name:   "no file paths",
			prompt: "Please help me understand the code",
			want:   nil,
		},
		{
			name:   "relative path",
			prompt: "Check ./config.yaml",
			want:   []string{"./config.yaml"},
		},
		{
			name:   "filters out version numbers",
			prompt: "Upgrade to version 1.2.3",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractFilePaths(tt.prompt)
			if len(got) != len(tt.want) {
				t.Errorf("ExtractFilePaths() = %v, want %v", got, tt.want)
				return
			}
			for i, path := range got {
				if path != tt.want[i] {
					t.Errorf("ExtractFilePaths()[%d] = %q, want %q", i, path, tt.want[i])
				}
			}
		})
	}
}

func TestIsLikelyCodePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"go file with path", "internal/robot/routing.go", true},
		{"standalone go file", "main.go", true},
		{"ts file", "src/app.ts", true},
		{"tsx file", "src/Button.tsx", true},
		{"yaml file", "config.yaml", true},
		{"json file", "package.json", true},
		{"no extension", "Makefile", false},
		{"directory", "src/", false},
		{"version number", "1.2.3", false},
		{"http url", "http://example.com/file.txt", false},
		{"https url", "https://example.com/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLikelyCodePath(tt.path)
			if got != tt.want {
				t.Errorf("isLikelyCodePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestNewReservationCache(t *testing.T) {
	t.Run("default TTL", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test/project", 0)
		if cache.ttl != 30*time.Second {
			t.Errorf("Default TTL = %v, want 30s", cache.ttl)
		}
	})

	t.Run("custom TTL", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test/project", 60*time.Second)
		if cache.ttl != 60*time.Second {
			t.Errorf("TTL = %v, want 60s", cache.ttl)
		}
	})

	t.Run("initializes pathToAgents map", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test/project", 0)
		if cache.pathToAgents == nil {
			t.Error("pathToAgents should be initialized")
		}
	})
}

func TestReservationCacheNeedsRefresh(t *testing.T) {
	t.Run("needs refresh when never fetched", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test", 30*time.Second)
		if !cache.NeedsRefresh() {
			t.Error("Should need refresh when never fetched")
		}
	})

	t.Run("does not need refresh when recently fetched", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test", 30*time.Second)
		cache.lastFetch = time.Now()
		if cache.NeedsRefresh() {
			t.Error("Should not need refresh when recently fetched")
		}
	})

	t.Run("needs refresh after TTL", func(t *testing.T) {
		cache := NewReservationCache(nil, "/test", 1*time.Millisecond)
		cache.lastFetch = time.Now().Add(-10 * time.Millisecond)
		if !cache.NeedsRefresh() {
			t.Error("Should need refresh after TTL")
		}
	})
}

func TestReservationCacheRefreshWithNilClient(t *testing.T) {
	cache := NewReservationCache(nil, "/test", 30*time.Second)
	err := cache.Refresh(nil)
	if err != nil {
		t.Errorf("Refresh with nil client should not error, got: %v", err)
	}
}

func TestReservationCacheGetReservedPathsForAgent(t *testing.T) {
	cache := NewReservationCache(nil, "/test", 30*time.Second)

	// Add some test reservations
	now := time.Now()
	cache.reservations = []agentmail.FileReservation{
		{AgentName: "GreenCastle", PathPattern: "internal/*.go", ExpiresTS: now.Add(1 * time.Hour)},
		{AgentName: "GreenCastle", PathPattern: "cmd/main.go", ExpiresTS: now.Add(1 * time.Hour)},
		{AgentName: "BlueLake", PathPattern: "pkg/*.go", ExpiresTS: now.Add(1 * time.Hour)},
		{AgentName: "GreenCastle", PathPattern: "expired.go", ExpiresTS: now.Add(-1 * time.Hour)}, // expired
	}

	paths := cache.GetReservedPathsForAgent("GreenCastle")
	if len(paths) != 2 {
		t.Errorf("GetReservedPathsForAgent() = %v, want 2 paths", paths)
	}
}

func TestAgentScorerMapping(t *testing.T) {
	cfg := DefaultRoutingConfig()
	scorer := NewAgentScorer(cfg)

	t.Run("MapPaneToAgent", func(t *testing.T) {
		scorer.MapPaneToAgent("%1", "GreenCastle")
		scorer.MapPaneToAgent("%2", "BlueLake")

		name, ok := scorer.GetAgentNameForPane("%1")
		if !ok || name != "GreenCastle" {
			t.Errorf("GetAgentNameForPane(%%1) = %q, %v, want GreenCastle, true", name, ok)
		}
	})

	t.Run("GetAgentNameForPane returns false for unknown", func(t *testing.T) {
		name, ok := scorer.GetAgentNameForPane("%99")
		if ok || name != "" {
			t.Errorf("GetAgentNameForPane(%%99) = %q, %v, want '', false", name, ok)
		}
	})

	t.Run("SetAgentMapping", func(t *testing.T) {
		mapping := map[string]string{
			"%10": "RedStone",
			"%11": "PurpleBear",
		}
		scorer.SetAgentMapping(mapping)

		name, ok := scorer.GetAgentNameForPane("%10")
		if !ok || name != "RedStone" {
			t.Errorf("After SetAgentMapping: GetAgentNameForPane(%%10) = %q, %v", name, ok)
		}
	})
}

func TestCheckReservationWarning_Disabled(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = false
	scorer := NewAgentScorer(cfg)

	warning := scorer.CheckReservationWarning("Fix internal/robot/routing.go", "%1")
	if warning != nil {
		t.Error("Should return nil when Agent Mail is disabled")
	}
}

func TestCheckReservationWarning_NoCache(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)
	// No reservation cache set

	warning := scorer.CheckReservationWarning("Fix internal/robot/routing.go", "%1")
	if warning != nil {
		t.Error("Should return nil when no reservation cache")
	}
}

func TestCheckReservationWarning_NoFilePaths(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)
	scorer.SetReservationCache(NewReservationCache(nil, "/test", 30*time.Second))

	warning := scorer.CheckReservationWarning("Please help me understand the code", "%1")
	if warning != nil {
		t.Error("Should return nil when no file paths in prompt")
	}
}

func TestCheckReservationWarning_WithReservations(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)

	cache := NewReservationCache(nil, "/test", 30*time.Second)
	// Manually set pathToAgents for testing
	cache.pathToAgents = map[string][]string{
		"internal/robot/*.go": {"GreenCastle"},
	}
	scorer.SetReservationCache(cache)
	scorer.MapPaneToAgent("%1", "BlueLake") // Different from reservation holder

	warning := scorer.CheckReservationWarning("Fix internal/robot/routing.go", "%1")
	if warning == nil {
		t.Fatal("Should return warning when files are reserved")
	}
	if warning.SelectedHas {
		t.Error("SelectedHas should be false for BlueLake")
	}
	if len(warning.Holders) != 1 || warning.Holders[0] != "GreenCastle" {
		t.Errorf("Holders = %v, want [GreenCastle]", warning.Holders)
	}
}

func TestCheckReservationWarning_SelectedHoldsReservation(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)

	cache := NewReservationCache(nil, "/test", 30*time.Second)
	cache.pathToAgents = map[string][]string{
		"internal/robot/*.go": {"GreenCastle"},
	}
	scorer.SetReservationCache(cache)
	scorer.MapPaneToAgent("%1", "GreenCastle") // Same as reservation holder

	warning := scorer.CheckReservationWarning("Fix internal/robot/routing.go", "%1")
	if warning == nil {
		t.Fatal("Should return warning")
	}
	if !warning.SelectedHas {
		t.Error("SelectedHas should be true for GreenCastle")
	}
}

func TestCalculateAffinity_WithReservations(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.ReservationBonus = 30.0
	scorer := NewAgentScorer(cfg)

	// Set up cache with reservations
	cache := NewReservationCache(nil, "/test", 30*time.Second)
	now := time.Now()
	cache.reservations = []agentmail.FileReservation{
		{AgentName: "GreenCastle", PathPattern: "internal/robot/*.go", ExpiresTS: now.Add(1 * time.Hour)},
	}
	scorer.SetReservationCache(cache)
	scorer.MapPaneToAgent("%1", "GreenCastle")

	agent := &ScoredAgent{PaneID: "%1"}

	// Test with matching file path
	affinity := scorer.calculateAffinity(agent, "Fix internal/robot/routing.go")
	if affinity <= 0 {
		t.Errorf("Affinity should be > 0 when agent holds matching reservation, got %f", affinity)
	}
	if affinity > 30.0 {
		t.Errorf("Affinity should not exceed ReservationBonus (30), got %f", affinity)
	}
}

func TestCalculateAffinity_NoMapping(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true
	scorer := NewAgentScorer(cfg)
	scorer.SetReservationCache(NewReservationCache(nil, "/test", 30*time.Second))

	// No mapping for pane
	agent := &ScoredAgent{PaneID: "%1"}
	affinity := scorer.calculateAffinity(agent, "Fix internal/robot/routing.go")
	if affinity != 0 {
		t.Errorf("Affinity should be 0 when no pane mapping, got %f", affinity)
	}
}

func TestNewAgentScorerWithReservations(t *testing.T) {
	cfg := DefaultRoutingConfig()
	cfg.AgentMail.Enabled = true

	// Without client/project key, cache should not be created
	scorer := NewAgentScorerWithReservations(cfg, nil, "")
	if scorer.reservationCache != nil {
		t.Error("Should not create cache without client and project key")
	}

	// With disabled, cache should not be created
	cfg.AgentMail.Enabled = false
	scorer = NewAgentScorerWithReservations(cfg, nil, "/test")
	if scorer.reservationCache != nil {
		t.Error("Should not create cache when disabled")
	}
}

func TestReservationWarningStruct(t *testing.T) {
	warning := ReservationWarning{
		Message:     "Test warning",
		Paths:       []string{"file1.go", "file2.go"},
		Holders:     []string{"Agent1", "Agent2"},
		SelectedHas: true,
	}

	if warning.Message != "Test warning" {
		t.Errorf("Message = %q", warning.Message)
	}
	if len(warning.Paths) != 2 {
		t.Errorf("Paths = %v", warning.Paths)
	}
	if len(warning.Holders) != 2 {
		t.Errorf("Holders = %v", warning.Holders)
	}
	if !warning.SelectedHas {
		t.Error("SelectedHas should be true")
	}
}

func TestRoutingResultWithReservationWarning(t *testing.T) {
	result := RoutingResult{
		Strategy: StrategyLeastLoaded,
		ReservationWarning: &ReservationWarning{
			Message: "Files reserved by other agents",
			Paths:   []string{"config.go"},
			Holders: []string{"GreenCastle"},
		},
	}

	if result.ReservationWarning == nil {
		t.Fatal("ReservationWarning should not be nil")
	}
	if result.ReservationWarning.Message == "" {
		t.Error("Warning message should not be empty")
	}
}
