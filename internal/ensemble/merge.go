package ensemble

import (
	"sort"
	"strings"
	"time"
)

// MergeConfig controls how outputs are merged mechanically.
type MergeConfig struct {
	// MaxFindings limits findings in the merged result.
	MaxFindings int

	// MaxRisks limits risks in the merged result.
	MaxRisks int

	// MaxRecommendations limits recommendations in the merged result.
	MaxRecommendations int

	// MinConfidence filters items below this threshold.
	MinConfidence Confidence

	// DeduplicationThreshold controls similarity threshold for deduplication.
	// Items with similarity above this are considered duplicates.
	DeduplicationThreshold float64

	// WeightByConfidence boosts items by source confidence.
	WeightByConfidence bool

	// PreferHighImpact sorts by impact before confidence.
	PreferHighImpact bool
}

// DefaultMergeConfig returns sensible merge defaults.
func DefaultMergeConfig() MergeConfig {
	return MergeConfig{
		MaxFindings:            20,
		MaxRisks:               10,
		MaxRecommendations:     10,
		MinConfidence:          0.3,
		DeduplicationThreshold: 0.7,
		WeightByConfidence:     true,
		PreferHighImpact:       true,
	}
}

// MergedOutput is the result of mechanically merging mode outputs.
type MergedOutput struct {
	// Findings are deduplicated and ranked findings.
	Findings []MergedFinding `json:"findings"`

	// Risks are deduplicated and ranked risks.
	Risks []MergedRisk `json:"risks"`

	// Recommendations are deduplicated and ranked recommendations.
	Recommendations []MergedRecommendation `json:"recommendations"`

	// Questions are aggregated questions for the user.
	Questions []Question `json:"questions,omitempty"`

	// SourceModes lists the modes that contributed.
	SourceModes []string `json:"source_modes"`

	// Stats provides merge statistics.
	Stats MergeStats `json:"stats"`
}

// MergedFinding wraps a finding with provenance.
type MergedFinding struct {
	Finding     Finding  `json:"finding"`
	SourceModes []string `json:"source_modes"`
	MergeScore  float64  `json:"merge_score"`
	// ProvenanceID links the merged finding back to its primary provenance chain.
	ProvenanceID string `json:"provenance_id,omitempty"`
}

// MergedRisk wraps a risk with provenance.
type MergedRisk struct {
	Risk        Risk     `json:"risk"`
	SourceModes []string `json:"source_modes"`
	MergeScore  float64  `json:"merge_score"`
}

// MergedRecommendation wraps a recommendation with provenance.
type MergedRecommendation struct {
	Recommendation Recommendation `json:"recommendation"`
	SourceModes    []string       `json:"source_modes"`
	MergeScore     float64        `json:"merge_score"`
}

// MergeStats captures merge operation statistics.
type MergeStats struct {
	InputCount             int           `json:"input_count"`
	TotalFindings          int           `json:"total_findings"`
	DedupedFindings        int           `json:"deduped_findings"`
	TotalRisks             int           `json:"total_risks"`
	DedupedRisks           int           `json:"deduped_risks"`
	TotalRecommendations   int           `json:"total_recommendations"`
	DedupedRecommendations int           `json:"deduped_recommendations"`
	MergeTime              time.Duration `json:"merge_time"`
}

// MergeOutputs performs mechanical merging of multiple mode outputs.
func MergeOutputs(outputs []ModeOutput, cfg MergeConfig) *MergedOutput {
	return MergeOutputsWithProvenance(outputs, cfg, nil)
}

// MergeOutputsWithProvenance performs mechanical merging and records provenance if provided.
func MergeOutputsWithProvenance(outputs []ModeOutput, cfg MergeConfig, tracker *ProvenanceTracker) *MergedOutput {
	start := time.Now()

	result := &MergedOutput{
		Findings:        make([]MergedFinding, 0),
		Risks:           make([]MergedRisk, 0),
		Recommendations: make([]MergedRecommendation, 0),
		Questions:       make([]Question, 0),
		SourceModes:     make([]string, 0, len(outputs)),
	}

	// Track source modes
	for _, o := range outputs {
		result.SourceModes = append(result.SourceModes, o.ModeID)
	}

	// Merge findings
	result.Findings, result.Stats.TotalFindings, result.Stats.DedupedFindings = mergeFindings(outputs, cfg, tracker)

	// Merge risks
	result.Risks, result.Stats.TotalRisks, result.Stats.DedupedRisks = mergeRisks(outputs, cfg)

	// Merge recommendations
	result.Recommendations, result.Stats.TotalRecommendations, result.Stats.DedupedRecommendations = mergeRecommendations(outputs, cfg)

	// Aggregate questions (no dedup, just combine)
	for _, o := range outputs {
		result.Questions = append(result.Questions, o.QuestionsForUser...)
	}

	result.Stats.InputCount = len(outputs)
	result.Stats.MergeTime = time.Since(start)

	return result
}

// mergeFindings deduplicates and ranks findings from multiple outputs.
func mergeFindings(outputs []ModeOutput, cfg MergeConfig, tracker *ProvenanceTracker) ([]MergedFinding, int, int) {
	type findingEntry struct {
		finding       Finding
		sourceModes   []string
		score         float64
		provenanceIDs []string
	}

	// Collect all findings
	all := make([]findingEntry, 0)
	for _, o := range outputs {
		modeConf := float64(o.Confidence)
		for _, f := range o.TopFindings {
			provID := ""
			if tracker != nil {
				provID = tracker.RecordDiscovery(o.ModeID, f)
			}
			if float64(f.Confidence) < float64(cfg.MinConfidence) {
				if tracker != nil && provID != "" {
					_ = tracker.RecordFilter(provID, "below min confidence")
				}
				continue
			}
			score := float64(f.Confidence)
			if cfg.WeightByConfidence {
				score *= modeConf
			}
			if cfg.PreferHighImpact {
				score *= impactWeight(f.Impact)
			}
			var provenanceIDs []string
			if provID != "" {
				provenanceIDs = []string{provID}
			}
			all = append(all, findingEntry{
				finding:       f,
				sourceModes:   []string{o.ModeID},
				score:         score,
				provenanceIDs: provenanceIDs,
			})
		}
	}

	totalCount := len(all)

	// Deduplicate by similarity
	merged := deduplicateEntries(all, cfg.DeduplicationThreshold,
		func(e findingEntry) string { return e.finding.Finding },
		func(a, b findingEntry, similarity float64) findingEntry {
			// Merge: combine source modes, take higher score.
			// Use explicit allocation to avoid slice aliasing from append.
			combined := make([]string, 0, len(a.sourceModes)+len(b.sourceModes))
			combined = append(combined, a.sourceModes...)
			combined = append(combined, b.sourceModes...)
			score := a.score
			if b.score > score {
				score = b.score
			}
			combinedIDs := make([]string, 0, len(a.provenanceIDs)+len(b.provenanceIDs))
			combinedIDs = append(combinedIDs, a.provenanceIDs...)
			combinedIDs = append(combinedIDs, b.provenanceIDs...)
			if tracker != nil {
				primaryID := ""
				if len(a.provenanceIDs) > 0 {
					primaryID = a.provenanceIDs[0]
				}
				if primaryID != "" && len(b.provenanceIDs) > 0 {
					_ = tracker.RecordMerge(primaryID, b.provenanceIDs, similarity)
				}
			}
			return findingEntry{
				finding:       a.finding,
				sourceModes:   uniqueStrings(combined),
				score:         score * 1.1, // Boost for agreement
				provenanceIDs: combinedIDs,
			}
		},
	)

	// Sort by score descending
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	// Limit
	if cfg.MaxFindings > 0 && len(merged) > cfg.MaxFindings {
		if tracker != nil {
			for _, dropped := range merged[cfg.MaxFindings:] {
				for _, id := range dropped.provenanceIDs {
					if id != "" {
						_ = tracker.RecordFilter(id, "trimmed by max findings limit")
					}
				}
			}
		}
		merged = merged[:cfg.MaxFindings]
	}

	// Convert to result type
	result := make([]MergedFinding, len(merged))
	for i, e := range merged {
		primaryID := ""
		if len(e.provenanceIDs) > 0 {
			primaryID = e.provenanceIDs[0]
		}
		result[i] = MergedFinding{
			Finding:      e.finding,
			SourceModes:  e.sourceModes,
			MergeScore:   e.score,
			ProvenanceID: primaryID,
		}
	}

	return result, totalCount, len(result)
}

// mergeRisks deduplicates and ranks risks from multiple outputs.
func mergeRisks(outputs []ModeOutput, cfg MergeConfig) ([]MergedRisk, int, int) {
	type riskEntry struct {
		risk        Risk
		sourceModes []string
		score       float64
	}

	all := make([]riskEntry, 0)
	for _, o := range outputs {
		modeConf := float64(o.Confidence)
		for _, r := range o.Risks {
			score := impactWeight(r.Impact) * float64(r.Likelihood)
			if cfg.WeightByConfidence {
				score *= modeConf
			}
			all = append(all, riskEntry{
				risk:        r,
				sourceModes: []string{o.ModeID},
				score:       score,
			})
		}
	}

	totalCount := len(all)

	merged := deduplicateEntries(all, cfg.DeduplicationThreshold,
		func(e riskEntry) string { return e.risk.Risk },
		func(a, b riskEntry, _ float64) riskEntry {
			combined := make([]string, 0, len(a.sourceModes)+len(b.sourceModes))
			combined = append(combined, a.sourceModes...)
			combined = append(combined, b.sourceModes...)
			score := a.score
			if b.score > score {
				score = b.score
			}
			return riskEntry{
				risk:        a.risk,
				sourceModes: uniqueStrings(combined),
				score:       score * 1.1,
			}
		},
	)

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	if cfg.MaxRisks > 0 && len(merged) > cfg.MaxRisks {
		merged = merged[:cfg.MaxRisks]
	}

	result := make([]MergedRisk, len(merged))
	for i, e := range merged {
		result[i] = MergedRisk{
			Risk:        e.risk,
			SourceModes: e.sourceModes,
			MergeScore:  e.score,
		}
	}

	return result, totalCount, len(result)
}

// mergeRecommendations deduplicates and ranks recommendations from multiple outputs.
func mergeRecommendations(outputs []ModeOutput, cfg MergeConfig) ([]MergedRecommendation, int, int) {
	type recEntry struct {
		rec         Recommendation
		sourceModes []string
		score       float64
	}

	all := make([]recEntry, 0)
	for _, o := range outputs {
		modeConf := float64(o.Confidence)
		for _, r := range o.Recommendations {
			score := impactWeight(r.Priority)
			if cfg.WeightByConfidence {
				score *= modeConf
			}
			all = append(all, recEntry{
				rec:         r,
				sourceModes: []string{o.ModeID},
				score:       score,
			})
		}
	}

	totalCount := len(all)

	merged := deduplicateEntries(all, cfg.DeduplicationThreshold,
		func(e recEntry) string { return e.rec.Recommendation },
		func(a, b recEntry, _ float64) recEntry {
			combined := make([]string, 0, len(a.sourceModes)+len(b.sourceModes))
			combined = append(combined, a.sourceModes...)
			combined = append(combined, b.sourceModes...)
			score := a.score
			if b.score > score {
				score = b.score
			}
			return recEntry{
				rec:         a.rec,
				sourceModes: uniqueStrings(combined),
				score:       score * 1.1,
			}
		},
	)

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	if cfg.MaxRecommendations > 0 && len(merged) > cfg.MaxRecommendations {
		merged = merged[:cfg.MaxRecommendations]
	}

	result := make([]MergedRecommendation, len(merged))
	for i, e := range merged {
		result[i] = MergedRecommendation{
			Recommendation: e.rec,
			SourceModes:    e.sourceModes,
			MergeScore:     e.score,
		}
	}

	return result, totalCount, len(result)
}

// deduplicateEntries groups similar entries using text similarity.
func deduplicateEntries[T any](
	entries []T,
	threshold float64,
	textFn func(T) string,
	mergeFn func(a, b T, similarity float64) T,
) []T {
	if len(entries) == 0 {
		return nil
	}

	// Track which entries have been merged
	merged := make([]bool, len(entries))
	result := make([]T, 0, len(entries))

	for i := 0; i < len(entries); i++ {
		if merged[i] {
			continue
		}

		current := entries[i]
		currentText := normalizeText(textFn(current))
		currentTokens := tokenize(currentText)

		// Find similar entries
		for j := i + 1; j < len(entries); j++ {
			if merged[j] {
				continue
			}

			otherText := normalizeText(textFn(entries[j]))
			otherTokens := tokenize(otherText)

			similarity := jaccardSimilarity(currentTokens, otherTokens)
			if similarity >= threshold {
				current = mergeFn(current, entries[j], similarity)
				merged[j] = true
			}
		}

		result = append(result, current)
		merged[i] = true
	}

	return result
}

// Weight functions for scoring

func impactWeight(impact ImpactLevel) float64 {
	switch impact {
	case ImpactCritical:
		return 1.0
	case ImpactHigh:
		return 0.8
	case ImpactMedium:
		return 0.5
	case ImpactLow:
		return 0.3
	default:
		return 0.4
	}
}

// ConsolidateTheses selects or creates a representative thesis.
func ConsolidateTheses(outputs []ModeOutput) string {
	if len(outputs) == 0 {
		return ""
	}

	// Simple approach: find the thesis with highest confidence
	var best string
	var bestConf Confidence
	for _, o := range outputs {
		thesis := strings.TrimSpace(o.Thesis)
		if thesis == "" {
			continue
		}
		if o.Confidence > bestConf {
			best = thesis
			bestConf = o.Confidence
		}
	}

	return best
}

// AverageConfidence computes weighted average confidence across outputs.
func AverageConfidence(outputs []ModeOutput) Confidence {
	if len(outputs) == 0 {
		return 0
	}

	var sum float64
	for _, o := range outputs {
		sum += float64(o.Confidence)
	}
	return Confidence(sum / float64(len(outputs)))
}
