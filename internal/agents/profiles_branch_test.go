package agents

import (
	"testing"
)

// =============================================================================
// copy — nil receiver
// =============================================================================

func TestCopy_NilReceiver(t *testing.T) {
	t.Parallel()

	var p *AgentProfile
	got := p.copy()
	if got != nil {
		t.Errorf("copy() on nil receiver = %v, want nil", got)
	}
}

// =============================================================================
// GetProfile — nil return for unknown agent
// =============================================================================

func TestGetProfile_UnknownAgent(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	got := pm.GetProfile(AgentType("unknown"))
	if got != nil {
		t.Errorf("GetProfile(unknown) = %v, want nil", got)
	}
}

// =============================================================================
// ScoreAssignment — low success rate penalty
// =============================================================================

func TestScoreAssignment_LowSuccessRate(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	// Set Claude's success rate below 0.7
	pm.mu.Lock()
	pm.profiles[AgentTypeClaude].Performance.SuccessRate = 0.6
	pm.mu.Unlock()

	task := TaskInfo{
		Title: "Simple task",
		Type:  "task",
	}

	result := pm.ScoreAssignment(AgentTypeClaude, task)
	if result.PerformanceBonus != -0.1 {
		t.Errorf("PerformanceBonus = %f, want -0.1", result.PerformanceBonus)
	}
}

// =============================================================================
// ScoreAssignment — high success rate bonus
// =============================================================================

func TestScoreAssignment_HighSuccessRate(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	// Set Claude's success rate above 0.9
	pm.mu.Lock()
	pm.profiles[AgentTypeClaude].Performance.SuccessRate = 0.95
	pm.mu.Unlock()

	task := TaskInfo{
		Title: "Simple task",
		Type:  "task",
	}

	result := pm.ScoreAssignment(AgentTypeClaude, task)
	if result.PerformanceBonus != 0.1 {
		t.Errorf("PerformanceBonus = %f, want 0.1", result.PerformanceBonus)
	}
}

// =============================================================================
// taskMatchesSpecialization — default case (unknown spec)
// =============================================================================

func TestTaskMatchesSpecialization_UnknownSpec(t *testing.T) {
	t.Parallel()

	task := TaskInfo{Title: "Some task", Type: "task"}
	got := taskMatchesSpecialization(task, Specialization("unknown-spec"))
	if got {
		t.Error("taskMatchesSpecialization with unknown spec should return false")
	}
}

// =============================================================================
// calculateFileScore — boost cap and penalty cap
// =============================================================================

func TestCalculateFileScore_BoostCap(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	profile := pm.profiles[AgentTypeClaude]

	// 6 preferred Go files → boost = 1 + 6*0.1 = 1.6 → capped at 1.5
	files := []string{
		"internal/a/a.go",
		"internal/b/b.go",
		"internal/c/c.go",
		"internal/d/d.go",
		"internal/e/e.go",
		"internal/f/f.go",
	}

	score := pm.calculateFileScore(profile, files)
	if score > 1.5 {
		t.Errorf("calculateFileScore should cap boost at 1.5, got %f", score)
	}
}

func TestCalculateFileScore_PenaltyCap(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	profile := pm.profiles[AgentTypeClaude]

	// Claude avoids *.md and docs/** — use many md files to trigger penalty floor
	files := []string{
		"a.md",
		"b.md",
		"c.md",
		"d.md",
		"e.md",
		"f.md",
		"g.md",
	}

	score := pm.calculateFileScore(profile, files)
	if score < 0.5 {
		t.Errorf("calculateFileScore should floor penalty at 0.5, got %f", score)
	}
}

// =============================================================================
// calculateLabelScore — score > 2.0 cap
// =============================================================================

func TestCalculateLabelScore_Cap(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()
	profile := pm.profiles[AgentTypeClaude]

	// Claude prefers: epic, feature, critical, P0, P1
	// Each label match gives 1.15x → 5 matches = 1.15^5 ≈ 2.01
	// Need 6+ to be clearly over 2.0
	labels := []string{"epic", "feature", "critical", "P0", "P1", "critical", "P0"}

	score := pm.calculateLabelScore(profile, labels)
	if score > 2.0 {
		t.Errorf("calculateLabelScore should cap at 2.0, got %f", score)
	}
}

// =============================================================================
// matchGlobPattern — additional branch coverage
// =============================================================================

func TestMatchGlobPattern_DoubleStarFooNoWildcard(t *testing.T) {
	t.Parallel()

	// Pattern like **/foo (no wildcard in suffix) — match path containing /foo
	tests := []struct {
		path string
		want bool
	}{
		{"internal/foo", true},        // ends with suffix
		{"a/b/foo", true},             // ends with suffix
		{"internal/bar", false},       // doesn't match
		{"internal/foo/bar.go", true}, // contains /foo
	}

	for _, tt := range tests {
		got := matchGlobPattern(tt.path, "**/foo")
		if got != tt.want {
			t.Errorf("matchGlobPattern(%q, %q) = %v, want %v", tt.path, "**/foo", got, tt.want)
		}
	}
}

func TestMatchGlobPattern_SuffixDoubleStarExactMatch(t *testing.T) {
	t.Parallel()

	// Pattern docs/** should match "docs" exactly (path == prefix)
	got := matchGlobPattern("docs", "docs/**")
	if !got {
		t.Error("matchGlobPattern(\"docs\", \"docs/**\") should match exactly")
	}
}

func TestMatchGlobPattern_ExactFileBaseName(t *testing.T) {
	t.Parallel()

	// Exact match via filepath.Base — pattern "main.go" matches path "cmd/ntm/main.go"
	got := matchGlobPattern("cmd/ntm/main.go", "main.go")
	if !got {
		t.Error("matchGlobPattern should match filepath.Base for exact pattern")
	}

	// And exact path match
	got2 := matchGlobPattern("main.go", "main.go")
	if !got2 {
		t.Error("matchGlobPattern should match exact path")
	}
}

func TestMatchGlobPattern_InfixDoubleStarNoWildcard(t *testing.T) {
	t.Parallel()

	// Pattern like prefix/**/suffix (no wildcard in suffix)
	got := matchGlobPattern("internal/a/b/config.toml", "internal/**/config.toml")
	if !got {
		t.Error("matchGlobPattern should match infix /** with exact suffix")
	}

	got2 := matchGlobPattern("internal/a/b/other.toml", "internal/**/config.toml")
	if got2 {
		t.Error("matchGlobPattern should not match wrong suffix")
	}
}

func TestMatchGlobPattern_InfixDoubleStarPrefixExact(t *testing.T) {
	t.Parallel()

	// Path equals prefix exactly (no "/")
	got := matchGlobPattern("internal", "internal/**/*.go")
	if got {
		t.Error("matchGlobPattern should not match prefix-only path for infix /** pattern")
	}
}

// =============================================================================
// RecommendAgent — fallback to Claude when no agent can handle
// =============================================================================

func TestRecommendAgent_FallbackToClaude(t *testing.T) {
	t.Parallel()

	pm := NewProfileMatcher()

	// Task exceeding all context budgets
	task := TaskInfo{
		Title:           "Massive task",
		Type:            "epic",
		EstimatedTokens: 999999,
	}

	agent, result := pm.RecommendAgent(task)
	// Should default to Claude even though it can't handle
	if agent != AgentTypeClaude {
		t.Errorf("RecommendAgent fallback = %v, want %v", agent, AgentTypeClaude)
	}
	// Claude can't handle either, so CanHandle should be false
	if result.CanHandle {
		t.Error("fallback result should not CanHandle when task exceeds all budgets")
	}
}

// =============================================================================
// RecordCompletion — nil profile (unknown agent)
// =============================================================================

// =============================================================================
// ParseAgentType — default case
// =============================================================================
