package assign

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scoring"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDefaultEffectivenessConfig(t *testing.T) {
	cfg := DefaultEffectivenessConfig()

	if !cfg.Enabled {
		t.Error("expected Enabled=true by default")
	}
	if cfg.Mode != ModeBalanced {
		t.Errorf("expected Mode=balanced, got %s", cfg.Mode)
	}
	if cfg.WindowDays != 14 {
		t.Errorf("expected WindowDays=14, got %d", cfg.WindowDays)
	}
	if cfg.MinSamples != 3 {
		t.Errorf("expected MinSamples=3, got %d", cfg.MinSamples)
	}
}

func TestEffectivenessWeight(t *testing.T) {
	tests := []struct {
		name    string
		mode    AssignmentMode
		enabled bool
		want    float64
	}{
		{"exploitation", ModeExploitation, true, 0.6},
		{"learning", ModeLearning, true, 0.2},
		{"balanced", ModeBalanced, true, 0.4},
		{"disabled", ModeBalanced, false, 0.0},
		{"unknown mode", "unknown", true, 0.4}, // defaults to balanced weight
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultEffectivenessConfig()
			cfg.Mode = tt.mode
			cfg.Enabled = tt.enabled

			got := cfg.EffectivenessWeight()
			if got != tt.want {
				t.Errorf("EffectivenessWeight() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewEffectivenessIntegrator(t *testing.T) {
	// Test with nil config (should use defaults)
	ei := NewEffectivenessIntegrator(nil)
	if ei.config.Mode != ModeBalanced {
		t.Errorf("expected default mode=balanced, got %s", ei.config.Mode)
	}

	// Test with custom config
	custom := &EffectivenessConfig{
		Enabled: true,
		Mode:    ModeExploitation,
	}
	ei = NewEffectivenessIntegrator(custom)
	if ei.config.Mode != ModeExploitation {
		t.Errorf("expected custom mode=exploitation, got %s", ei.config.Mode)
	}
}

func TestIntegratorSetGetMode(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Default should be balanced
	if ei.GetMode() != ModeBalanced {
		t.Errorf("expected default mode=balanced, got %s", ei.GetMode())
	}

	// Set to exploitation
	ei.SetMode(ModeExploitation)
	if ei.GetMode() != ModeExploitation {
		t.Errorf("expected mode=exploitation after set, got %s", ei.GetMode())
	}

	// Set to learning
	ei.SetMode(ModeLearning)
	if ei.GetMode() != ModeLearning {
		t.Errorf("expected mode=learning after set, got %s", ei.GetMode())
	}
}

func TestGetEffectivenessBonusDisabled(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Enabled = false
	ei := NewEffectivenessIntegrator(cfg)

	bonus, reason := ei.GetEffectivenessBonus("claude", "bug")

	if bonus != 0 {
		t.Errorf("expected bonus=0 when disabled, got %f", bonus)
	}
	if reason != "effectiveness scoring disabled" {
		t.Errorf("unexpected reason: %s", reason)
	}
}

func TestGetEffectivenessBonusNoData(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Enabled = true
	ei := NewEffectivenessIntegrator(cfg)

	// Query for a non-existent agent-task pair (no historical data)
	bonus, reason := ei.GetEffectivenessBonus("nonexistent", "invalid_task")

	if bonus != 0 {
		t.Errorf("expected bonus=0 with no data, got %f", bonus)
	}
	if reason != "insufficient historical data" {
		t.Errorf("unexpected reason: %s", reason)
	}
}

func TestRankAgentsForTask(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Get ranking for bug fixes
	ranking, err := ei.RankAgentsForTask("bug")
	if err != nil {
		t.Fatalf("RankAgentsForTask failed: %v", err)
	}

	if ranking.TaskType != "bug" {
		t.Errorf("expected task_type=bug, got %s", ranking.TaskType)
	}

	// Should have 3 agent types
	if len(ranking.Rankings) != 3 {
		t.Errorf("expected 3 rankings, got %d", len(ranking.Rankings))
	}

	// Rankings should be ordered (rank 1, 2, 3)
	for i, r := range ranking.Rankings {
		expectedRank := i + 1
		if r.Rank != expectedRank {
			t.Errorf("ranking[%d].Rank = %d, want %d", i, r.Rank, expectedRank)
		}
	}

	// Scores should be descending
	for i := 0; i < len(ranking.Rankings)-1; i++ {
		if ranking.Rankings[i].Score < ranking.Rankings[i+1].Score {
			t.Errorf("rankings not sorted descending: [%d]=%f < [%d]=%f",
				i, ranking.Rankings[i].Score, i+1, ranking.Rankings[i+1].Score)
		}
	}
}

func TestAgentTaskEffectivenessStruct(t *testing.T) {
	score := scoring.AgentTaskEffectiveness{
		AgentType:    "claude",
		TaskType:     "bug",
		Score:        0.85,
		SampleCount:  10,
		Confidence:   0.9,
		HasData:      true,
		DecayApplied: true,
	}

	if score.AgentType != "claude" {
		t.Errorf("expected AgentType=claude, got %s", score.AgentType)
	}
	if score.Score != 0.85 {
		t.Errorf("expected Score=0.85, got %f", score.Score)
	}
	if !score.HasData {
		t.Error("expected HasData=true")
	}
}

func TestCapabilityMatrixWithEffectiveness(t *testing.T) {
	// Create a fresh matrix
	m := NewCapabilityMatrix()

	// Get base score for claude-bug
	baseScore := m.GetScore(tmux.AgentClaude, TaskBug)
	if baseScore != 0.80 {
		t.Errorf("expected base score=0.80 for claude-bug, got %f", baseScore)
	}

	// Set a learned score
	m.SetLearned(tmux.AgentClaude, TaskBug, 0.95)

	// GetScore should now return learned score (higher priority)
	score := m.GetScore(tmux.AgentClaude, TaskBug)
	if score != 0.95 {
		t.Errorf("expected learned score=0.95, got %f", score)
	}

	// Clear learned
	m.ClearLearned()

	// Should be back to base
	score = m.GetScore(tmux.AgentClaude, TaskBug)
	if score != 0.80 {
		t.Errorf("expected base score=0.80 after clear, got %f", score)
	}
}

func TestAssignmentModeConstants(t *testing.T) {
	// Verify mode constant values
	if ModeExploitation != "exploitation" {
		t.Error("ModeExploitation constant mismatch")
	}
	if ModeLearning != "learning" {
		t.Error("ModeLearning constant mismatch")
	}
	if ModeBalanced != "balanced" {
		t.Error("ModeBalanced constant mismatch")
	}
}

func TestEffectivenessIntegratorConcurrency(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Simulate concurrent access
	done := make(chan bool, 10)

	for i := 0; i < 5; i++ {
		go func() {
			ei.SetMode(ModeExploitation)
			_ = ei.GetMode()
			done <- true
		}()
	}

	for i := 0; i < 5; i++ {
		go func() {
			_, _ = ei.GetEffectivenessBonus("claude", "bug")
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestDefaultIntegratorSingleton(t *testing.T) {
	// DefaultIntegrator should return same instance
	i1 := DefaultIntegrator()
	i2 := DefaultIntegrator()

	if i1 != i2 {
		t.Error("DefaultIntegrator should return same instance")
	}
}

func TestGlobalFunctions(t *testing.T) {
	// Test global convenience functions
	mode := GetAssignmentMode()
	if mode == "" {
		t.Error("GetAssignmentMode returned empty mode")
	}

	// Set mode
	SetAssignmentMode(ModeLearning)
	if GetAssignmentMode() != ModeLearning {
		t.Error("SetAssignmentMode/GetAssignmentMode mismatch")
	}

	// Reset
	SetAssignmentMode(ModeBalanced)

	// GetEffectivenessBonus should work
	bonus, reason := GetEffectivenessBonus("claude", "bug")
	_ = bonus
	if reason == "" {
		t.Error("GetEffectivenessBonus returned empty reason")
	}
}

// populateCache sets the integrator's cache with the given data and marks
// it as current (or stale if staleTime is non-zero).
func populateCache(ei *EffectivenessIntegrator, data map[string]map[string]*scoring.AgentTaskEffectiveness, at time.Time) {
	ei.mu.Lock()
	defer ei.mu.Unlock()
	ei.cache = data
	ei.cacheAt = at
}

func TestGetEffectivenessScoreCacheHit(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	want := &scoring.AgentTaskEffectiveness{
		AgentType:   "cc",
		TaskType:    "bug",
		Score:       0.82,
		SampleCount: 7,
		Confidence:  0.9,
		HasData:     true,
	}
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": want},
	}, time.Now()) // fresh cache

	got, err := ei.GetEffectivenessScore("cc", "bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("cache hit should return same pointer; got %p, want %p", got, want)
	}
	if got.Score != 0.82 {
		t.Errorf("Score = %v, want 0.82", got.Score)
	}
}

func TestGetEffectivenessScoreCacheMissAgent(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Cache has data for "cc" but not "cod"
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": {HasData: true, Score: 0.8}},
	}, time.Now())

	// "cod" not in cache → falls through to tracker
	got, err := ei.GetEffectivenessScore("cod", "bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Tracker returns no data for a non-existent pair
	if got.HasData {
		t.Error("expected HasData=false from tracker fallback")
	}
}

func TestGetEffectivenessScoreCacheMissTask(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Cache has data for cc→bug but not cc→feature
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": {HasData: true, Score: 0.8}},
	}, time.Now())

	got, err := ei.GetEffectivenessScore("cc", "feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.HasData {
		t.Error("expected HasData=false for missing task type in cache")
	}
}

func TestGetEffectivenessScoreStaleCache(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)

	// Populate cache but mark it as 10 minutes old (stale, >5 min)
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": {HasData: true, Score: 0.99, SampleCount: 50}},
	}, time.Now().Add(-10*time.Minute))

	// Stale cache → should fall through to tracker
	got, err := ei.GetEffectivenessScore("cc", "bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Tracker returns fresh (no data) rather than stale cached 0.99
	if got.Score == 0.99 {
		t.Error("stale cache should not be used; expected fresh tracker result")
	}
}

func TestGetEffectivenessScoreNilCache(t *testing.T) {
	ei := NewEffectivenessIntegrator(nil)
	// cache starts as empty map, set to nil explicitly
	ei.mu.Lock()
	ei.cache = nil
	ei.mu.Unlock()

	got, err := ei.GetEffectivenessScore("cc", "bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// nil cache → falls through to tracker
	if got == nil {
		t.Fatal("expected non-nil result from tracker fallback")
	}
}

func TestGetEffectivenessBonusWithData(t *testing.T) {
	tests := []struct {
		name      string
		mode      AssignmentMode
		score     float64
		wantSign  int // -1 negative, 0 zero, 1 positive
		wantInMsg string
	}{
		{"high score exploitation", ModeExploitation, 0.85, 1, "effectiveness 0.85"},
		{"low score exploitation", ModeExploitation, 0.20, -1, "effectiveness 0.20"},
		{"baseline score", ModeBalanced, 0.50, 0, "effectiveness 0.50"},
		{"high score learning", ModeLearning, 0.90, 1, "mode=learning"},
		{"high score balanced", ModeBalanced, 0.75, 1, "mode=balanced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultEffectivenessConfig()
			cfg.Mode = tt.mode
			ei := NewEffectivenessIntegrator(cfg)

			populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
				"cc": {"bug": {
					HasData:     true,
					Score:       tt.score,
					SampleCount: 10,
					Confidence:  0.95,
				}},
			}, time.Now())

			bonus, reason := ei.GetEffectivenessBonus("cc", "bug")

			switch tt.wantSign {
			case 1:
				if bonus <= 0 {
					t.Errorf("expected positive bonus for score=%.2f mode=%s, got %f", tt.score, tt.mode, bonus)
				}
			case -1:
				if bonus >= 0 {
					t.Errorf("expected negative bonus for score=%.2f mode=%s, got %f", tt.score, tt.mode, bonus)
				}
			case 0:
				if bonus != 0 {
					t.Errorf("expected zero bonus for baseline score, got %f", bonus)
				}
			}

			if len(tt.wantInMsg) > 0 {
				if !containsStr(reason, tt.wantInMsg) {
					t.Errorf("reason %q should contain %q", reason, tt.wantInMsg)
				}
			}
		})
	}
}

func TestGetEffectivenessBonusMagnitude(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Mode = ModeExploitation // weight = 0.6

	ei := NewEffectivenessIntegrator(cfg)
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": {
			HasData:     true,
			Score:       0.80,
			SampleCount: 10,
			Confidence:  0.95,
		}},
	}, time.Now())

	bonus, _ := ei.GetEffectivenessBonus("cc", "bug")

	// bonus = (0.80 - 0.50) * 0.6 = 0.18
	expected := 0.18
	if diff := bonus - expected; diff > 0.001 || diff < -0.001 {
		t.Errorf("bonus = %f, want ~%f (within 0.001)", bonus, expected)
	}
}

func TestGetEffectivenessBonusNoDataInCache(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	ei := NewEffectivenessIntegrator(cfg)

	// Cache entry exists but HasData=false
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"bug": {
			HasData:     false,
			Score:       0,
			SampleCount: 0,
		}},
	}, time.Now())

	bonus, reason := ei.GetEffectivenessBonus("cc", "bug")
	if bonus != 0 {
		t.Errorf("expected bonus=0 for HasData=false, got %f", bonus)
	}
	if reason != "insufficient historical data" {
		t.Errorf("unexpected reason: %s", reason)
	}
}

func TestRankAgentsForTaskWithEffectivenessData(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Mode = ModeExploitation // weight = 0.6
	ei := NewEffectivenessIntegrator(cfg)

	// Populate cache with effectiveness data that should reorder the rankings.
	// Base scores (from CapabilityMatrix for "bug"):
	//   cc=0.80, cod=0.90, gmi=0.75
	// Default order by base: cod, cc, gmi.
	// Use effectiveness to push gmi to top and cc below gmi.
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc":  {"bug": {HasData: true, Score: 0.20, SampleCount: 10, Confidence: 1.0}},
		"cod": {"bug": {HasData: true, Score: 0.30, SampleCount: 15, Confidence: 1.0}},
		"gmi": {"bug": {HasData: true, Score: 0.99, SampleCount: 8, Confidence: 1.0}},
	}, time.Now())

	ranking, err := ei.RankAgentsForTask("bug")
	if err != nil {
		t.Fatalf("RankAgentsForTask error: %v", err)
	}

	if !ranking.HasData {
		t.Error("expected HasData=true when effectiveness data exists")
	}

	if len(ranking.Rankings) != 3 {
		t.Fatalf("expected 3 rankings, got %d", len(ranking.Rankings))
	}

	// With exploitation weight=0.6 and confidence=1.0:
	// gmi: 0.75*(1-0.6) + 0.99*0.6 = 0.30 + 0.594 = 0.894
	// cc:  0.80*(1-0.6) + 0.20*0.6 = 0.32 + 0.12  = 0.44
	// cod: 0.90*(1-0.6) + 0.30*0.6 = 0.36 + 0.18  = 0.54
	// Expected order: gmi, cod, cc
	if ranking.Rankings[0].AgentType != "gmi" {
		t.Errorf("expected gmi at rank 1, got %s (score=%.3f)",
			ranking.Rankings[0].AgentType, ranking.Rankings[0].Score)
	}
	if ranking.Rankings[1].AgentType != "cod" {
		t.Errorf("expected cod at rank 2, got %s (score=%.3f)",
			ranking.Rankings[1].AgentType, ranking.Rankings[1].Score)
	}
	if ranking.Rankings[2].AgentType != "cc" {
		t.Errorf("expected cc at rank 3, got %s (score=%.3f)",
			ranking.Rankings[2].AgentType, ranking.Rankings[2].Score)
	}

	// Verify rank numbers
	for i, r := range ranking.Rankings {
		if r.Rank != i+1 {
			t.Errorf("ranking[%d].Rank = %d, want %d", i, r.Rank, i+1)
		}
	}

	// Verify explanation includes effectiveness info
	for _, r := range ranking.Rankings {
		if !containsStr(r.Explanation, "eff=") {
			t.Errorf("ranking for %s should have eff= in explanation: %s", r.AgentType, r.Explanation)
		}
	}
}

func TestRankAgentsForTaskPartialData(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Mode = ModeBalanced
	ei := NewEffectivenessIntegrator(cfg)

	// Only cc has effectiveness data
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"feature": {HasData: true, Score: 0.95, SampleCount: 12, Confidence: 0.8}},
	}, time.Now())

	ranking, err := ei.RankAgentsForTask("feature")
	if err != nil {
		t.Fatalf("RankAgentsForTask error: %v", err)
	}

	if !ranking.HasData {
		t.Error("expected HasData=true with at least one agent having data")
	}

	// cc should get boosted, others use base scores
	var ccRank, codRank, gmiRank AgentEffectivenessRank
	for _, r := range ranking.Rankings {
		switch r.AgentType {
		case "cc":
			ccRank = r
		case "cod":
			codRank = r
		case "gmi":
			gmiRank = r
		}
	}

	if ccRank.SampleCount != 12 {
		t.Errorf("cc SampleCount = %d, want 12", ccRank.SampleCount)
	}
	if codRank.SampleCount != 0 {
		t.Errorf("cod SampleCount = %d, want 0 (no data)", codRank.SampleCount)
	}
	if gmiRank.SampleCount != 0 {
		t.Errorf("gmi SampleCount = %d, want 0 (no data)", gmiRank.SampleCount)
	}

	// cod/gmi should have "no effectiveness data" explanation
	if !containsStr(codRank.Explanation, "no effectiveness data") {
		t.Errorf("cod explanation should mention no data: %s", codRank.Explanation)
	}
}

func TestRankAgentsForTaskConfidenceScaling(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Mode = ModeExploitation // weight = 0.6
	ei := NewEffectivenessIntegrator(cfg)

	// Low confidence should dampen the effectiveness adjustment
	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cod": {"bug": {HasData: true, Score: 0.99, SampleCount: 2, Confidence: 0.1}},
	}, time.Now())

	ranking, err := ei.RankAgentsForTask("bug")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var codRank AgentEffectivenessRank
	for _, r := range ranking.Rankings {
		if r.AgentType == "cod" {
			codRank = r
			break
		}
	}

	// weight = 0.6 * 0.1 = 0.06 (very small adjustment)
	// score = 0.90*(1-0.06) + 0.99*0.06 = 0.846 + 0.0594 = 0.9054
	// Base for cod-bug = 0.90, so score should be close to base
	if codRank.Score < 0.89 || codRank.Score > 0.92 {
		t.Errorf("low confidence should barely shift score from base 0.90; got %.4f", codRank.Score)
	}
}

func TestRankAgentsEffectivenessRankFields(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	ei := NewEffectivenessIntegrator(cfg)

	populateCache(ei, map[string]map[string]*scoring.AgentTaskEffectiveness{
		"cc": {"task": {HasData: true, Score: 0.72, SampleCount: 5, Confidence: 0.85}},
	}, time.Now())

	ranking, err := ei.RankAgentsForTask("task")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var ccRank AgentEffectivenessRank
	for _, r := range ranking.Rankings {
		if r.AgentType == "cc" {
			ccRank = r
			break
		}
	}

	if ccRank.EffScore != 0.72 {
		t.Errorf("EffScore = %f, want 0.72", ccRank.EffScore)
	}
	if ccRank.Confidence != 0.85 {
		t.Errorf("Confidence = %f, want 0.85", ccRank.Confidence)
	}
	if ccRank.SampleCount != 5 {
		t.Errorf("SampleCount = %d, want 5", ccRank.SampleCount)
	}

	// BaseScore should be the matrix base score for cc-task
	expectedBase := NewCapabilityMatrix().GetScore(tmux.AgentClaude, TaskTask)
	if ccRank.BaseScore != expectedBase {
		t.Errorf("BaseScore = %f, want %f", ccRank.BaseScore, expectedBase)
	}
}

func TestEffectivenessRankingStruct(t *testing.T) {
	ranking := EffectivenessRanking{
		TaskType: "bug",
		Mode:     ModeBalanced,
		HasData:  true,
		Rankings: []AgentEffectivenessRank{
			{AgentType: "claude", Score: 0.9, Rank: 1},
		},
	}

	if ranking.TaskType != "bug" {
		t.Errorf("TaskType = %s, want bug", ranking.TaskType)
	}
	if ranking.Mode != ModeBalanced {
		t.Errorf("Mode = %s, want balanced", ranking.Mode)
	}
	if !ranking.HasData {
		t.Error("expected HasData=true")
	}
	if len(ranking.Rankings) != 1 {
		t.Errorf("expected 1 ranking, got %d", len(ranking.Rankings))
	}
}

func TestRefreshCapabilitiesDisabled(t *testing.T) {
	cfg := DefaultEffectivenessConfig()
	cfg.Enabled = false
	ei := NewEffectivenessIntegrator(cfg)

	err := ei.RefreshCapabilities()
	if err != nil {
		t.Errorf("RefreshCapabilities with disabled config should return nil, got: %v", err)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
