package ensemble

import (
	"fmt"
	"strings"
)

// RedundancyAnalysis measures output similarity across modes.
// Higher overall score indicates more redundant (wasteful) mode selection.
type RedundancyAnalysis struct {
	// OverallScore ranges from 0-1, where higher means more redundant.
	OverallScore float64 `json:"overall_score" yaml:"overall_score"`

	// PairwiseScores holds similarity data for each mode pair.
	PairwiseScores []PairSimilarity `json:"pairwise_scores" yaml:"pairwise_scores"`

	// Recommendations are suggestions for reducing redundancy.
	Recommendations []string `json:"recommendations,omitempty" yaml:"recommendations,omitempty"`
}

// PairSimilarity measures similarity between two specific modes.
type PairSimilarity struct {
	// ModeA is the first mode ID.
	ModeA string `json:"mode_a" yaml:"mode_a"`

	// ModeB is the second mode ID.
	ModeB string `json:"mode_b" yaml:"mode_b"`

	// Similarity ranges from 0-1, where 1 means identical findings.
	Similarity float64 `json:"similarity" yaml:"similarity"`

	// SharedFindings is the count of findings appearing in both modes.
	SharedFindings int `json:"shared_findings" yaml:"shared_findings"`

	// UniqueToA is findings only in ModeA.
	UniqueToA int `json:"unique_to_a" yaml:"unique_to_a"`

	// UniqueToB is findings only in ModeB.
	UniqueToB int `json:"unique_to_b" yaml:"unique_to_b"`
}

// GetHighRedundancyPairs returns pairs above the given similarity threshold.
func (r *RedundancyAnalysis) GetHighRedundancyPairs(threshold float64) []PairSimilarity {
	if r == nil {
		return nil
	}

	var result []PairSimilarity
	for _, pair := range r.PairwiseScores {
		if pair.Similarity >= threshold {
			result = append(result, pair)
		}
	}
	return result
}

// SuggestReplacements suggests alternative modes for redundant pairs.
func (r *RedundancyAnalysis) SuggestReplacements(catalog *ModeCatalog) []string {
	if r == nil || catalog == nil {
		return nil
	}

	highRedundancy := r.GetHighRedundancyPairs(0.5)
	if len(highRedundancy) == 0 {
		return []string{"No high-redundancy pairs found - mode selection is diverse"}
	}

	var suggestions []string
	suggestedModes := make(map[string]bool)

	for _, pair := range highRedundancy {
		// Get the categories of the redundant modes
		modeA := catalog.GetMode(pair.ModeA)
		modeB := catalog.GetMode(pair.ModeB)

		if modeA == nil || modeB == nil {
			continue
		}

		// If both modes are in the same category, suggest a mode from a different category
		if modeA.Category == modeB.Category {
			// Find an alternative from a different category
			for _, cat := range AllCategories() {
				if cat == modeA.Category {
					continue
				}
				alternatives := catalog.ListByCategory(cat)
				for _, alt := range alternatives {
					if alt.Tier == TierCore && !suggestedModes[alt.ID] {
						suggestion := fmt.Sprintf("Consider replacing %s with %s (%s) for more diverse analysis",
							pair.ModeB, alt.ID, alt.Category)
						suggestions = append(suggestions, suggestion)
						suggestedModes[alt.ID] = true
						break
					}
				}
				if len(suggestions) > 0 {
					break
				}
			}
		}
	}

	if len(suggestions) == 0 {
		suggestions = append(suggestions, "Redundant modes span multiple categories - consider domain-specific alternatives")
	}

	return suggestions
}

// Render produces a human-readable redundancy report.
func (r *RedundancyAnalysis) Render() string {
	if r == nil {
		return "No redundancy data available"
	}

	var b strings.Builder

	// Header
	b.WriteString("Redundancy Analysis:\n")

	// Overall score with interpretation
	interpretation := interpretScore(r.OverallScore)
	fmt.Fprintf(&b, "Overall Score: %.2f (%s)\n\n", r.OverallScore, interpretation)

	// Pairwise similarities
	if len(r.PairwiseScores) > 0 {
		b.WriteString("Pairwise Similarity:\n")
		for _, pair := range r.PairwiseScores {
			level := classifySimilarity(pair.Similarity)
			fmt.Fprintf(&b, "%s ↔ %s: %.2f (%s - %s)\n",
				pair.ModeA, pair.ModeB, pair.Similarity, level, diversityNote(pair.Similarity))
		}
		b.WriteString("\n")
	}

	// Recommendations
	if len(r.Recommendations) > 0 {
		for _, rec := range r.Recommendations {
			fmt.Fprintf(&b, "Recommendation: %s\n", rec)
		}
	}

	return b.String()
}

// interpretScore returns a human-readable interpretation of the overall score.
func interpretScore(score float64) string {
	switch {
	case score >= 0.7:
		return "high redundancy - significant overlap"
	case score >= 0.5:
		return "moderate redundancy"
	case score >= 0.3:
		return "acceptable"
	default:
		return "low redundancy - good diversity"
	}
}

// classifySimilarity returns a classification label for a similarity score.
func classifySimilarity(score float64) string {
	switch {
	case score >= 0.7:
		return "HIGH"
	case score >= 0.4:
		return "moderate"
	default:
		return "low"
	}
}

// diversityNote returns a diversity note based on similarity.
func diversityNote(score float64) string {
	if score >= 0.5 {
		return "overlapping insights"
	}
	return "good diversity"
}
