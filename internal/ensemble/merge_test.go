package ensemble

import (
	"testing"
)

func TestDefaultMergeConfig(t *testing.T) {
	cfg := DefaultMergeConfig()

	if cfg.MaxFindings != 20 {
		t.Errorf("MaxFindings = %d, want 20", cfg.MaxFindings)
	}
	if cfg.MaxRisks != 10 {
		t.Errorf("MaxRisks = %d, want 10", cfg.MaxRisks)
	}
	if cfg.MaxRecommendations != 10 {
		t.Errorf("MaxRecommendations = %d, want 10", cfg.MaxRecommendations)
	}
	if cfg.MinConfidence != 0.3 {
		t.Errorf("MinConfidence = %f, want 0.3", cfg.MinConfidence)
	}
	if cfg.DeduplicationThreshold != 0.7 {
		t.Errorf("DeduplicationThreshold = %f, want 0.7", cfg.DeduplicationThreshold)
	}
}

func TestMergeOutputs_EmptyInputs(t *testing.T) {
	cfg := DefaultMergeConfig()
	result := MergeOutputs(nil, cfg)

	if result == nil {
		t.Fatal("MergeOutputs returned nil for empty inputs")
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(result.Findings))
	}
	if result.Stats.InputCount != 0 {
		t.Errorf("InputCount = %d, want 0", result.Stats.InputCount)
	}
}

func TestMergeOutputs_SingleOutput(t *testing.T) {
	cfg := DefaultMergeConfig()

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Thesis:     "Test thesis",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Finding 1", Impact: ImpactHigh, Confidence: 0.9},
				{Finding: "Finding 2", Impact: ImpactMedium, Confidence: 0.7},
			},
			Risks: []Risk{
				{Risk: "Risk 1", Impact: ImpactHigh, Likelihood: 0.8},
			},
			Recommendations: []Recommendation{
				{Recommendation: "Rec 1", Priority: ImpactHigh},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.SourceModes) != 1 {
		t.Errorf("SourceModes = %d, want 1", len(result.SourceModes))
	}
	if result.SourceModes[0] != "mode-a" {
		t.Errorf("SourceModes[0] = %s, want mode-a", result.SourceModes[0])
	}
	if len(result.Findings) != 2 {
		t.Errorf("Findings = %d, want 2", len(result.Findings))
	}
	if len(result.Risks) != 1 {
		t.Errorf("Risks = %d, want 1", len(result.Risks))
	}
	if len(result.Recommendations) != 1 {
		t.Errorf("Recommendations = %d, want 1", len(result.Recommendations))
	}
}

func TestMergeOutputs_MultipleOutputs_NoDuplicates(t *testing.T) {
	cfg := DefaultMergeConfig()

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Finding A", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "mode-b",
			Confidence: 0.7,
			TopFindings: []Finding{
				{Finding: "Finding B", Impact: ImpactMedium, Confidence: 0.8},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Findings) != 2 {
		t.Errorf("Findings = %d, want 2", len(result.Findings))
	}
	if result.Stats.TotalFindings != 2 {
		t.Errorf("TotalFindings = %d, want 2", result.Stats.TotalFindings)
	}
	if result.Stats.DedupedFindings != 2 {
		t.Errorf("DedupedFindings = %d, want 2", result.Stats.DedupedFindings)
	}
}

func TestMergeOutputs_Deduplication(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.DeduplicationThreshold = 0.5 // Lower threshold for easier matching

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "The authentication module has a security vulnerability", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "mode-b",
			Confidence: 0.7,
			TopFindings: []Finding{
				{Finding: "The authentication module has a security vulnerability", Impact: ImpactHigh, Confidence: 0.8},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	// Should deduplicate identical findings
	if len(result.Findings) != 1 {
		t.Errorf("Findings = %d, want 1 (should be deduplicated)", len(result.Findings))
	}
	if result.Stats.TotalFindings != 2 {
		t.Errorf("TotalFindings = %d, want 2", result.Stats.TotalFindings)
	}
	// The deduplicated finding should list both source modes
	if len(result.Findings) > 0 && len(result.Findings[0].SourceModes) != 2 {
		t.Errorf("SourceModes for deduplicated finding = %d, want 2", len(result.Findings[0].SourceModes))
	}
}

func TestMergeOutputs_MinConfidenceFilter(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.MinConfidence = 0.5

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "High confidence finding", Impact: ImpactHigh, Confidence: 0.9},
				{Finding: "Low confidence finding", Impact: ImpactHigh, Confidence: 0.2},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	// Low confidence finding should be filtered out
	if len(result.Findings) != 1 {
		t.Errorf("Findings = %d, want 1 (low confidence filtered)", len(result.Findings))
	}
}

func TestMergeOutputs_MaxFindingsLimit(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.MaxFindings = 3

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Finding 1", Impact: ImpactHigh, Confidence: 0.9},
				{Finding: "Finding 2", Impact: ImpactMedium, Confidence: 0.8},
				{Finding: "Finding 3", Impact: ImpactLow, Confidence: 0.7},
				{Finding: "Finding 4", Impact: ImpactLow, Confidence: 0.6},
				{Finding: "Finding 5", Impact: ImpactLow, Confidence: 0.5},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Findings) != 3 {
		t.Errorf("Findings = %d, want 3 (limited by MaxFindings)", len(result.Findings))
	}
}

func TestMergeOutputs_SortByScore(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.PreferHighImpact = true
	cfg.WeightByConfidence = true

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Low impact finding", Impact: ImpactLow, Confidence: 0.9},
				{Finding: "Critical finding", Impact: ImpactCritical, Confidence: 0.9},
				{Finding: "Medium impact finding", Impact: ImpactMedium, Confidence: 0.9},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Findings) < 3 {
		t.Fatalf("Expected at least 3 findings, got %d", len(result.Findings))
	}

	// Critical should be first (highest score)
	if result.Findings[0].Finding.Finding != "Critical finding" {
		t.Errorf("First finding should be critical, got: %s", result.Findings[0].Finding.Finding)
	}
}

func TestMergeOutputs_Questions(t *testing.T) {
	cfg := DefaultMergeConfig()

	outputs := []ModeOutput{
		{
			ModeID:           "mode-a",
			Confidence:       0.8,
			TopFindings:      []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
			QuestionsForUser: []Question{{Question: "Question from A"}},
		},
		{
			ModeID:           "mode-b",
			Confidence:       0.7,
			TopFindings:      []Finding{{Finding: "f2", Impact: ImpactMedium, Confidence: 0.5}},
			QuestionsForUser: []Question{{Question: "Question from B"}},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Questions) != 2 {
		t.Errorf("Questions = %d, want 2", len(result.Questions))
	}
}

func TestMergeOutputs_RisksScoring(t *testing.T) {
	cfg := DefaultMergeConfig()

	outputs := []ModeOutput{
		{
			ModeID:      "mode-a",
			Confidence:  0.8,
			TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
			Risks: []Risk{
				{Risk: "Low risk", Impact: ImpactLow, Likelihood: 0.3},
				{Risk: "High risk", Impact: ImpactHigh, Likelihood: 0.9},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Risks) < 2 {
		t.Fatalf("Expected at least 2 risks, got %d", len(result.Risks))
	}

	// High risk should be first (higher score due to impact × likelihood)
	if result.Risks[0].Risk.Risk != "High risk" {
		t.Errorf("First risk should be high risk, got: %s", result.Risks[0].Risk.Risk)
	}
}

func TestConsolidateTheses(t *testing.T) {
	tests := []struct {
		name    string
		outputs []ModeOutput
		want    string
	}{
		{
			name:    "empty outputs",
			outputs: nil,
			want:    "",
		},
		{
			name: "single output",
			outputs: []ModeOutput{
				{Thesis: "Only thesis", Confidence: 0.8},
			},
			want: "Only thesis",
		},
		{
			name: "multiple outputs - pick highest confidence",
			outputs: []ModeOutput{
				{Thesis: "Low confidence thesis", Confidence: 0.5},
				{Thesis: "High confidence thesis", Confidence: 0.9},
				{Thesis: "Medium confidence thesis", Confidence: 0.7},
			},
			want: "High confidence thesis",
		},
		{
			name: "empty theses skipped",
			outputs: []ModeOutput{
				{Thesis: "", Confidence: 0.95},
				{Thesis: "Valid thesis", Confidence: 0.7},
			},
			want: "Valid thesis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConsolidateTheses(tt.outputs)
			if got != tt.want {
				t.Errorf("ConsolidateTheses() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAverageConfidence(t *testing.T) {
	tests := []struct {
		name    string
		outputs []ModeOutput
		want    Confidence
	}{
		{
			name:    "empty outputs",
			outputs: nil,
			want:    0,
		},
		{
			name: "single output",
			outputs: []ModeOutput{
				{Confidence: 0.8},
			},
			want: 0.8,
		},
		{
			name: "multiple outputs",
			outputs: []ModeOutput{
				{Confidence: 0.6},
				{Confidence: 0.8},
				{Confidence: 1.0},
			},
			want: 0.8, // (0.6 + 0.8 + 1.0) / 3 = 0.8
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AverageConfidence(tt.outputs)
			// Use approximate comparison for floating point
			diff := float64(got) - float64(tt.want)
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.001 {
				t.Errorf("AverageConfidence() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestImpactWeight(t *testing.T) {
	tests := []struct {
		impact ImpactLevel
		want   float64
	}{
		{ImpactCritical, 1.0},
		{ImpactHigh, 0.8},
		{ImpactMedium, 0.5},
		{ImpactLow, 0.3},
		{ImpactLevel("unknown"), 0.4},
	}

	for _, tt := range tests {
		t.Run(string(tt.impact), func(t *testing.T) {
			got := impactWeight(tt.impact)
			if got != tt.want {
				t.Errorf("impactWeight(%v) = %v, want %v", tt.impact, got, tt.want)
			}
		})
	}
}

func TestMergeOutputs_RecommendationsScoring(t *testing.T) {
	cfg := DefaultMergeConfig()

	outputs := []ModeOutput{
		{
			ModeID:      "mode-a",
			Confidence:  0.8,
			TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
			Recommendations: []Recommendation{
				{Recommendation: "Low priority", Priority: ImpactLow},
				{Recommendation: "High priority", Priority: ImpactHigh},
				{Recommendation: "Critical priority", Priority: ImpactCritical},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Recommendations) < 3 {
		t.Fatalf("Expected at least 3 recommendations, got %d", len(result.Recommendations))
	}

	// Critical should be first
	if result.Recommendations[0].Recommendation.Recommendation != "Critical priority" {
		t.Errorf("First recommendation should be critical priority, got: %s",
			result.Recommendations[0].Recommendation.Recommendation)
	}
}

func TestMergeStats(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.DeduplicationThreshold = 0.9 // High threshold to only merge identical text

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "This exact finding appears in both modes", Impact: ImpactHigh, Confidence: 0.9},
				{Finding: "Alpha mode discovered a completely separate issue with authentication", Impact: ImpactMedium, Confidence: 0.8},
			},
			Risks: []Risk{
				{Risk: "Duplicate risk", Impact: ImpactHigh, Likelihood: 0.8},
			},
			Recommendations: []Recommendation{
				{Recommendation: "Unique rec A", Priority: ImpactHigh},
			},
		},
		{
			ModeID:     "mode-b",
			Confidence: 0.7,
			TopFindings: []Finding{
				{Finding: "This exact finding appears in both modes", Impact: ImpactHigh, Confidence: 0.85},
				{Finding: "Beta mode found something entirely different about performance", Impact: ImpactLow, Confidence: 0.7},
			},
			Risks: []Risk{
				{Risk: "Duplicate risk", Impact: ImpactHigh, Likelihood: 0.7},
			},
			Recommendations: []Recommendation{
				{Recommendation: "Unique rec B", Priority: ImpactMedium},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if result.Stats.InputCount != 2 {
		t.Errorf("InputCount = %d, want 2", result.Stats.InputCount)
	}
	if result.Stats.TotalFindings != 4 {
		t.Errorf("TotalFindings = %d, want 4", result.Stats.TotalFindings)
	}
	// Deduplication merges the identical finding, leaving 3
	if result.Stats.DedupedFindings != 3 {
		t.Errorf("DedupedFindings = %d, want 3", result.Stats.DedupedFindings)
	}
	if result.Stats.TotalRisks != 2 {
		t.Errorf("TotalRisks = %d, want 2", result.Stats.TotalRisks)
	}
	if result.Stats.TotalRecommendations != 2 {
		t.Errorf("TotalRecommendations = %d, want 2", result.Stats.TotalRecommendations)
	}
	if result.Stats.MergeTime <= 0 {
		t.Error("MergeTime should be positive")
	}
}

func TestMergedFinding_SourceModes(t *testing.T) {
	cfg := DefaultMergeConfig()
	cfg.DeduplicationThreshold = 0.5

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Shared finding across modes", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "mode-b",
			Confidence: 0.7,
			TopFindings: []Finding{
				{Finding: "Shared finding across modes", Impact: ImpactHigh, Confidence: 0.85},
			},
		},
		{
			ModeID:     "mode-c",
			Confidence: 0.75,
			TopFindings: []Finding{
				{Finding: "Shared finding across modes", Impact: ImpactHigh, Confidence: 0.8},
			},
		},
	}

	result := MergeOutputs(outputs, cfg)

	if len(result.Findings) != 1 {
		t.Fatalf("Expected 1 merged finding, got %d", len(result.Findings))
	}

	// All three modes should be in source modes
	sourceModes := result.Findings[0].SourceModes
	if len(sourceModes) != 3 {
		t.Errorf("SourceModes = %d, want 3", len(sourceModes))
	}

	// Check that score was boosted for agreement (1.1x per merge)
	// Base score for mode-a: 0.9 * 0.8 * 0.8 = 0.576
	// After 2 merges: 0.576 * 1.1 * 1.1 ≈ 0.697
	// So we expect > 0.6 to confirm boosting occurred
	if result.Findings[0].MergeScore <= 0.6 {
		t.Errorf("MergeScore should be boosted for multi-mode agreement, got %f", result.Findings[0].MergeScore)
	}
}

// =============================================================================
// MechanicalMerger Tests
// =============================================================================
