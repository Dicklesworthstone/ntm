package ensemble

import (
	"strings"
	"testing"
)

func TestRedundancyAnalysis_SuggestReplacements(t *testing.T) {
	analysis := &RedundancyAnalysis{
		PairwiseScores: []PairSimilarity{
			{ModeA: "mode-a", ModeB: "mode-b", Similarity: 0.9},
		},
	}

	catalog, err := NewModeCatalog([]ReasoningMode{
		{ID: "mode-a", Name: "A", Category: CategoryFormal, Tier: TierCore, ShortDesc: "A"},
		{ID: "mode-b", Name: "B", Category: CategoryFormal, Tier: TierCore, ShortDesc: "B"},
		{ID: "alt-1", Name: "Alt", Category: CategoryUncertainty, Tier: TierCore, ShortDesc: "Alt"},
	}, "test")
	if err != nil {
		t.Fatalf("NewModeCatalog error: %v", err)
	}

	suggestions := analysis.SuggestReplacements(catalog)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}

	joined := strings.Join(suggestions, "\n")
	if !strings.Contains(joined, "mode-b") || !strings.Contains(joined, "alt-1") {
		t.Fatalf("unexpected suggestions: %v", suggestions)
	}
}

func TestGetHighRedundancyPairs(t *testing.T) {
	analysis := &RedundancyAnalysis{
		OverallScore: 0.4,
		PairwiseScores: []PairSimilarity{
			{ModeA: "a", ModeB: "b", Similarity: 0.8},
			{ModeA: "a", ModeB: "c", Similarity: 0.3},
			{ModeA: "b", ModeB: "c", Similarity: 0.6},
		},
	}

	// Threshold 0.5
	high := analysis.GetHighRedundancyPairs(0.5)
	if len(high) != 2 {
		t.Errorf("expected 2 pairs above 0.5, got %d", len(high))
	}

	// Threshold 0.7
	high = analysis.GetHighRedundancyPairs(0.7)
	if len(high) != 1 {
		t.Errorf("expected 1 pair above 0.7, got %d", len(high))
	}

	// Threshold 0.9
	high = analysis.GetHighRedundancyPairs(0.9)
	if len(high) != 0 {
		t.Errorf("expected 0 pairs above 0.9, got %d", len(high))
	}
}

func TestGetHighRedundancyPairs_NilAnalysis(t *testing.T) {
	var analysis *RedundancyAnalysis
	high := analysis.GetHighRedundancyPairs(0.5)
	if high != nil {
		t.Error("expected nil for nil analysis")
	}
}

func TestRedundancyAnalysis_Render(t *testing.T) {
	analysis := &RedundancyAnalysis{
		OverallScore: 0.34,
		PairwiseScores: []PairSimilarity{
			{ModeA: "F1", ModeB: "E2", Similarity: 0.23, SharedFindings: 1, UniqueToA: 2, UniqueToB: 3},
			{ModeA: "F1", ModeB: "K1", Similarity: 0.67, SharedFindings: 5, UniqueToA: 1, UniqueToB: 1},
		},
		Recommendations: []string{"Consider replacing K1 with different mode"},
	}

	output := analysis.Render()

	// Check key elements are present
	if !simContains(output, "Overall Score: 0.34") {
		t.Error("expected overall score in output")
	}
	if !simContains(output, "acceptable") {
		t.Error("expected interpretation in output")
	}
	if !simContains(output, "F1 ↔ E2") {
		t.Error("expected first pair in output")
	}
	if !simContains(output, "F1 ↔ K1") {
		t.Error("expected second pair in output")
	}
	if !simContains(output, "moderate") {
		t.Error("expected moderate classification for 0.67 similarity")
	}
	if !simContains(output, "Recommendation:") {
		t.Error("expected recommendation in output")
	}
}

func TestRedundancyAnalysis_Render_Nil(t *testing.T) {
	var analysis *RedundancyAnalysis
	output := analysis.Render()
	if output != "No redundancy data available" {
		t.Errorf("unexpected output for nil analysis: %s", output)
	}
}

func TestInterpretScore(t *testing.T) {
	tests := []struct {
		score    float64
		contains string
	}{
		{0.8, "high redundancy"},
		{0.6, "moderate"},
		{0.35, "acceptable"},
		{0.1, "low redundancy"},
	}

	for _, tc := range tests {
		result := interpretScore(tc.score)
		if !simContains(result, tc.contains) {
			t.Errorf("score %.2f: expected to contain %q, got %q", tc.score, tc.contains, result)
		}
	}
}

func TestClassifySimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		score float64
		want  string
	}{
		{0.9, "HIGH"},
		{0.7, "HIGH"},
		{0.5, "moderate"},
		{0.4, "moderate"},
		{0.3, "low"},
		{0.0, "low"},
	}

	for _, tc := range tests {
		got := classifySimilarity(tc.score)
		if got != tc.want {
			t.Errorf("classifySimilarity(%.1f) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

func TestDiversityNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		score float64
		want  string
	}{
		{0.8, "overlapping insights"},
		{0.5, "overlapping insights"},
		{0.49, "good diversity"},
		{0.0, "good diversity"},
	}

	for _, tc := range tests {
		got := diversityNote(tc.score)
		if got != tc.want {
			t.Errorf("diversityNote(%.2f) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

// =============================================================================
// jaccardSimilarity (token-based)
// =============================================================================

func TestJaccardSimilarity(t *testing.T) {
	t.Parallel()

	set := func(elems ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(elems))
		for _, e := range elems {
			m[e] = struct{}{}
		}
		return m
	}

	t.Run("both empty", func(t *testing.T) {
		t.Parallel()
		got := jaccardSimilarity(nil, nil)
		if got != 1.0 {
			t.Errorf("both empty = %f, want 1.0 (identical empty sets)", got)
		}
	})

	t.Run("one empty", func(t *testing.T) {
		t.Parallel()
		got := jaccardSimilarity(set("a"), nil)
		if got != 0.0 {
			t.Errorf("one empty = %f, want 0.0", got)
		}
	})

	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		got := jaccardSimilarity(set("a", "b"), set("a", "b"))
		if got != 1.0 {
			t.Errorf("identical = %f, want 1.0", got)
		}
	})

	t.Run("disjoint", func(t *testing.T) {
		t.Parallel()
		got := jaccardSimilarity(set("a"), set("b"))
		if got != 0.0 {
			t.Errorf("disjoint = %f, want 0.0", got)
		}
	})

	t.Run("partial overlap", func(t *testing.T) {
		t.Parallel()
		got := jaccardSimilarity(set("a", "b"), set("b", "c"))
		want := 1.0 / 3.0
		if got < want-0.001 || got > want+0.001 {
			t.Errorf("partial = %f, want %f", got, want)
		}
	})
}

// =============================================================================
// tokenize (ensemble)
// =============================================================================

func TestTokenizeEnsemble(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"single word", "hello", 1},
		{"multiple words", "hello world foo", 3},
		{"with whitespace", "  hello   world  ", 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tokenize(tc.text)
			if len(got) != tc.want {
				t.Errorf("tokenize(%q) returned %d tokens, want %d", tc.text, len(got), tc.want)
			}
		})
	}
}

// =============================================================================
// normalizeText
// =============================================================================

func TestNormalizeText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"lowercase", "HELLO WORLD", "hello world"},
		{"trims whitespace", "  hello  ", "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeText(tc.input)
			if got != tc.want {
				t.Errorf("normalizeText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Helper function
func simContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && simFindSubstring(s, substr)))
}

func simFindSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
