package assign

import (
	"testing"

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
