package bv

import (
	"strings"
	"testing"
)

func TestRenderTriageMarkdown_Nil(t *testing.T) {
	result := RenderTriageMarkdown(nil, DefaultMarkdownOptions())
	if !strings.Contains(result, "No triage data") {
		t.Errorf("expected no triage message, got: %s", result)
	}
}

func TestRenderTriageMarkdown_Compact(t *testing.T) {
	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				OpenCount:       10,
				ActionableCount: 5,
				BlockedCount:    3,
				InProgressCount: 2,
				TopPicks: []TriageTopPick{
					{ID: "ntm-001", Title: "First task", Score: 0.95, Reasons: []string{"High impact"}},
					{ID: "ntm-002", Title: "Second task", Score: 0.85},
				},
			},
			QuickWins: []TriageRecommendation{
				{ID: "ntm-003", Title: "Quick fix"},
			},
		},
	}

	opts := CompactMarkdownOptions()
	result := RenderTriageMarkdown(triage, opts)

	// Check compact format
	if !strings.Contains(result, "5 ready") {
		t.Errorf("expected ready count, got: %s", result)
	}
	if !strings.Contains(result, "ntm-001") {
		t.Errorf("expected top pick ID, got: %s", result)
	}
	if !strings.Contains(result, "Quick wins:") {
		t.Errorf("expected quick wins section, got: %s", result)
	}
}

func TestRenderTriageMarkdown_Full(t *testing.T) {
	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				OpenCount:       20,
				ActionableCount: 10,
				BlockedCount:    5,
				InProgressCount: 3,
			},
			Recommendations: []TriageRecommendation{
				{
					ID:       "ntm-100",
					Title:    "Important feature",
					Type:     "feature",
					Priority: 1,
					Score:    0.9,
					Action:   "Start implementation",
					Reasons:  []string{"Unblocks 3 items"},
				},
			},
			ProjectHealth: &ProjectHealth{
				GraphMetrics: &GraphMetrics{
					TotalNodes: 100,
					TotalEdges: 150,
					Density:    0.03,
					CycleCount: 2,
				},
			},
		},
	}

	opts := DefaultMarkdownOptions()
	result := RenderTriageMarkdown(triage, opts)

	// Check full format
	if !strings.Contains(result, "## Beads Triage") {
		t.Errorf("expected triage header, got: %s", result)
	}
	if !strings.Contains(result, "| Actionable | 10 |") {
		t.Errorf("expected actionable count in table, got: %s", result)
	}
	if !strings.Contains(result, "ntm-100") {
		t.Errorf("expected recommendation ID, got: %s", result)
	}
	if !strings.Contains(result, "Cycles: 2") {
		t.Errorf("expected cycle count warning, got: %s", result)
	}
}

func TestRenderTriageMarkdown_WithScores(t *testing.T) {
	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				ActionableCount: 5,
			},
			Recommendations: []TriageRecommendation{
				{
					ID:       "ntm-200",
					Title:    "Test item",
					Type:     "task",
					Priority: 2,
					Score:    0.75,
					Breakdown: &ScoreBreakdown{
						Pagerank:      0.3,
						BlockerRatio:  0.2,
						PriorityBoost: 0.5,
					},
				},
			},
		},
	}

	opts := DefaultMarkdownOptions()
	opts.IncludeScores = true
	result := RenderTriageMarkdown(triage, opts)

	if !strings.Contains(result, "Score: 0.75") {
		t.Errorf("expected score in output, got: %s", result)
	}
	if !strings.Contains(result, "pagerank: 0.30") {
		t.Errorf("expected pagerank breakdown, got: %s", result)
	}
}

func TestCompactMarkdownPreservesFullText(t *testing.T) {
	// Verify compact mode does NOT truncate text - that was a broken design
	longTitle := "This is a very long title that should not be truncated at all because truncation destroys information"
	longReason := "This is a detailed reason explaining why this task is important and should be worked on next"

	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				ActionableCount: 1,
				TopPicks: []TriageTopPick{
					{ID: "ntm-test", Title: longTitle, Score: 0.9, Reasons: []string{longReason}},
				},
			},
		},
	}

	opts := CompactMarkdownOptions()
	result := RenderTriageMarkdown(triage, opts)

	// Full title must be present - NO truncation
	if !strings.Contains(result, longTitle) {
		t.Errorf("compact mode truncated title! got: %s", result)
	}
	// Full reason must be present - NO truncation
	if !strings.Contains(result, longReason) {
		t.Errorf("compact mode truncated reason! got: %s", result)
	}
	// No ellipsis from truncation
	if strings.Contains(result, "...") {
		t.Errorf("compact mode should not truncate with ellipsis, got: %s", result)
	}
}

func TestCompactMarkdownOptions(t *testing.T) {
	opts := CompactMarkdownOptions()
	if !opts.Compact {
		t.Error("expected Compact=true")
	}
	if opts.MaxRecommendations != 3 {
		t.Errorf("expected MaxRecommendations=3, got %d", opts.MaxRecommendations)
	}
}

func TestAgentContextBudget(t *testing.T) {
	// Verify Claude has largest budget
	if AgentContextBudget[AgentClaude] <= AgentContextBudget[AgentCodex] {
		t.Error("Claude should have larger context than Codex")
	}
	if AgentContextBudget[AgentCodex] <= AgentContextBudget[AgentGemini] {
		t.Error("Codex should have larger context than Gemini")
	}
}

func TestDefaultMarkdownOptions(t *testing.T) {
	t.Parallel()

	opts := DefaultMarkdownOptions()

	if opts.Compact {
		t.Error("expected Compact to be false by default")
	}
	if opts.MaxRecommendations != 5 {
		t.Errorf("MaxRecommendations = %d, want 5", opts.MaxRecommendations)
	}
	if opts.MaxQuickWins != 3 {
		t.Errorf("MaxQuickWins = %d, want 3", opts.MaxQuickWins)
	}
	if opts.IncludeScores {
		t.Error("expected IncludeScores to be false by default")
	}
}
