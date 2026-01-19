package assign

import (
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDefaultCapabilities(t *testing.T) {
	// Verify all agent types have capabilities
	agents := []tmux.AgentType{tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini}
	for _, agent := range agents {
		if _, ok := DefaultCapabilities[agent]; !ok {
			t.Errorf("DefaultCapabilities missing agent %s", agent)
		}
	}
}

func TestCapabilityMatrix_GetScore(t *testing.T) {
	m := NewCapabilityMatrix()

	tests := []struct {
		name     string
		agent    tmux.AgentType
		task     TaskType
		wantMin  float64
		wantMax  float64
	}{
		{"claude refactor", tmux.AgentClaude, TaskRefactor, 0.9, 1.0},
		{"claude analysis", tmux.AgentClaude, TaskAnalysis, 0.85, 0.95},
		{"codex bug", tmux.AgentCodex, TaskBug, 0.85, 0.95},
		{"codex feature", tmux.AgentCodex, TaskFeature, 0.85, 0.95},
		{"gemini docs", tmux.AgentGemini, TaskDocs, 0.85, 0.95},
		{"unknown task defaults to 0.5", tmux.AgentClaude, TaskType("unknown"), 0.45, 0.55},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score := m.GetScore(tc.agent, tc.task)
			if score < tc.wantMin || score > tc.wantMax {
				t.Errorf("GetScore(%s, %s) = %f, want in range [%f, %f]",
					tc.agent, tc.task, score, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestCapabilityMatrix_SetOverride(t *testing.T) {
	m := NewCapabilityMatrix()

	originalScore := m.GetScore(tmux.AgentClaude, TaskBug)
	overrideScore := 0.99

	m.SetOverride(tmux.AgentClaude, TaskBug, overrideScore)

	newScore := m.GetScore(tmux.AgentClaude, TaskBug)
	if newScore != overrideScore {
		t.Errorf("After override, GetScore = %f, want %f", newScore, overrideScore)
	}

	m.ClearOverrides()
	clearedScore := m.GetScore(tmux.AgentClaude, TaskBug)
	if clearedScore != originalScore {
		t.Errorf("After clear, GetScore = %f, want %f", clearedScore, originalScore)
	}
}

func TestCapabilityMatrix_SetLearned(t *testing.T) {
	m := NewCapabilityMatrix()

	// Learned scores take priority over base and overrides
	m.SetOverride(tmux.AgentCodex, TaskFeature, 0.70)
	m.SetLearned(tmux.AgentCodex, TaskFeature, 0.95)

	score := m.GetScore(tmux.AgentCodex, TaskFeature)
	if score != 0.95 {
		t.Errorf("Learned score should take priority, got %f, want 0.95", score)
	}

	m.ClearLearned()
	score = m.GetScore(tmux.AgentCodex, TaskFeature)
	if score != 0.70 {
		t.Errorf("After clearing learned, override should apply, got %f, want 0.70", score)
	}
}

func TestCapabilityMatrix_Clamp(t *testing.T) {
	m := NewCapabilityMatrix()

	// Test score clamping
	m.SetOverride(tmux.AgentClaude, TaskTask, 1.5)
	if score := m.GetScore(tmux.AgentClaude, TaskTask); score != 1.0 {
		t.Errorf("Score should be clamped to 1.0, got %f", score)
	}

	m.SetOverride(tmux.AgentClaude, TaskTask, -0.5)
	if score := m.GetScore(tmux.AgentClaude, TaskTask); score != 0.0 {
		t.Errorf("Score should be clamped to 0.0, got %f", score)
	}
}

func TestGetAgentScoreByString(t *testing.T) {
	tests := []struct {
		agent   string
		task    string
		wantMin float64
		wantMax float64
	}{
		{"claude", "refactor", 0.9, 1.0},
		{"cc", "analysis", 0.85, 0.95},
		{"codex", "bug", 0.85, 0.95},
		{"cod", "feature", 0.85, 0.95},
		{"gemini", "docs", 0.85, 0.95},
		{"gmi", "documentation", 0.85, 0.95},
	}

	for _, tc := range tests {
		t.Run(tc.agent+"/"+tc.task, func(t *testing.T) {
			score := GetAgentScoreByString(tc.agent, tc.task)
			if score < tc.wantMin || score > tc.wantMax {
				t.Errorf("GetAgentScoreByString(%s, %s) = %f, want in range [%f, %f]",
					tc.agent, tc.task, score, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestParseAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  tmux.AgentType
	}{
		{"cc", tmux.AgentClaude},
		{"claude", tmux.AgentClaude},
		{"Claude", tmux.AgentClaude},
		{"cod", tmux.AgentCodex},
		{"codex", tmux.AgentCodex},
		{"Codex", tmux.AgentCodex},
		{"gmi", tmux.AgentGemini},
		{"gemini", tmux.AgentGemini},
		{"Gemini", tmux.AgentGemini},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseAgentType(tc.input)
			if got != tc.want {
				t.Errorf("ParseAgentType(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTaskType(t *testing.T) {
	tests := []struct {
		input string
		want  TaskType
	}{
		{"bug", TaskBug},
		{"fix", TaskBug},
		{"broken", TaskBug},
		{"feature", TaskFeature},
		{"implement", TaskFeature},
		{"test", TaskTesting},
		{"testing", TaskTesting},
		{"docs", TaskDocs},
		{"documentation", TaskDocs},
		{"refactor", TaskRefactor},
		{"analysis", TaskAnalysis},
		{"investigate", TaskAnalysis},
		{"unknown", TaskTask}, // defaults to task
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseTaskType(tc.input)
			if got != tc.want {
				t.Errorf("ParseTaskType(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestCapabilityMatrix_ConcurrentAccess(t *testing.T) {
	m := NewCapabilityMatrix()
	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.GetScore(tmux.AgentClaude, TaskRefactor)
		}()
	}

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.SetOverride(tmux.AgentCodex, TaskBug, float64(n)/100.0)
		}(i)
	}

	// Concurrent learned scores
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.SetLearned(tmux.AgentGemini, TaskDocs, float64(n)/100.0)
		}(i)
	}

	wg.Wait() // Should complete without race conditions
}

func TestGlobalMatrix(t *testing.T) {
	// Verify global matrix is accessible and functional
	gm := GlobalMatrix()
	if gm == nil {
		t.Fatal("GlobalMatrix() returned nil")
	}

	// Should match GetAgentScore results
	score1 := GetAgentScore(tmux.AgentClaude, TaskRefactor)
	score2 := gm.GetScore(tmux.AgentClaude, TaskRefactor)
	if score1 != score2 {
		t.Errorf("GetAgentScore != GlobalMatrix().GetScore: %f vs %f", score1, score2)
	}
}
