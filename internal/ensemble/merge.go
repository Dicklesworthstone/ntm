package ensemble

import (
	"fmt"
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

// =============================================================================
// Mechanical Merger - Algorithmic pre-processing before synthesis
// =============================================================================

// FindingGroup groups findings by their evidence pointer.
type FindingGroup struct {
	// EvidencePointer is the shared evidence location (e.g., "file.go:42").
	EvidencePointer string `json:"evidence_pointer"`

	// Findings are all findings referencing this evidence.
	Findings []Finding `json:"findings"`

	// Modes lists which reasoning modes contributed findings to this group.
	Modes []string `json:"modes"`
}

// RiskGroup groups risks by severity level.
type RiskGroup struct {
	// Severity is the impact level for this group.
	Severity ImpactLevel `json:"severity"`

	// Risks are all risks at this severity level.
	Risks []Risk `json:"risks"`

	// Modes lists which reasoning modes contributed risks to this group.
	Modes []string `json:"modes"`
}

// RecommendationGroup groups recommendations by action type.
type RecommendationGroup struct {
	// ActionType categorizes the recommendation (e.g., "refactor", "add-test", "document").
	ActionType string `json:"action_type"`

	// Recommendations are all recommendations in this action category.
	Recommendations []Recommendation `json:"recommendations"`

	// Modes lists which reasoning modes contributed recommendations to this group.
	Modes []string `json:"modes"`
}

// PotentialConflict identifies contradictory positions between modes.
type PotentialConflict struct {
	// Topic is what the conflict is about.
	Topic string `json:"topic"`

	// ModeA is the first mode in the conflict.
	ModeA string `json:"mode_a"`

	// ModeB is the second mode in the conflict.
	ModeB string `json:"mode_b"`

	// PositionA is mode A's position on the topic.
	PositionA string `json:"position_a"`

	// PositionB is mode B's position on the topic.
	PositionB string `json:"position_b"`

	// ConflictType categorizes the conflict (thesis, severity, recommendation).
	ConflictType string `json:"conflict_type"`

	// Severity indicates how significant the conflict is (0-1).
	Severity float64 `json:"severity"`
}

// MergeResult is the output of mechanical merging.
type MergeResult struct {
	// GroupedFindings are findings organized by evidence pointer.
	GroupedFindings []FindingGroup `json:"grouped_findings"`

	// GroupedRisks are risks organized by severity level.
	GroupedRisks []RiskGroup `json:"grouped_risks"`

	// GroupedRecommendations are recommendations organized by action type.
	GroupedRecommendations []RecommendationGroup `json:"grouped_recommendations"`

	// IdentifiedConflicts are potential contradictions between modes.
	IdentifiedConflicts []PotentialConflict `json:"identified_conflicts"`

	// Statistics summarizes the merge operation.
	Statistics MergeResultStats `json:"statistics"`
}

// MergeResultStats provides statistics for mechanical merging.
type MergeResultStats struct {
	// TotalFindings is the count of all findings across all modes.
	TotalFindings int `json:"total_findings"`

	// UniqueFindings is the count after deduplication.
	UniqueFindings int `json:"unique_findings"`

	// DuplicateRate is the proportion of duplicates (0-1).
	DuplicateRate float64 `json:"duplicate_rate"`

	// ConflictCount is the number of identified conflicts.
	ConflictCount int `json:"conflict_count"`

	// TotalRisks is the count of all risks across all modes.
	TotalRisks int `json:"total_risks"`

	// TotalRecommendations is the count of all recommendations across all modes.
	TotalRecommendations int `json:"total_recommendations"`

	// EvidenceGroups is the number of distinct evidence pointers.
	EvidenceGroups int `json:"evidence_groups"`
}

// MechanicalMerger performs algorithmic pre-processing of mode outputs.
type MechanicalMerger struct {
	// Outputs are the mode outputs to merge.
	Outputs []ModeOutput
}

// NewMechanicalMerger creates a new mechanical merger from mode outputs.
func NewMechanicalMerger(outputs []ModeOutput) *MechanicalMerger {
	return &MechanicalMerger{Outputs: outputs}
}

// Merge performs the mechanical merge and returns grouped results.
func (m *MechanicalMerger) Merge() (*MergeResult, error) {
	if len(m.Outputs) == 0 {
		return &MergeResult{}, nil
	}

	result := &MergeResult{
		GroupedFindings:        m.groupFindingsByEvidence(),
		GroupedRisks:           m.groupRisksBySeverity(),
		GroupedRecommendations: m.groupRecommendationsByAction(),
		IdentifiedConflicts:    m.detectConflicts(),
	}

	// Compute statistics
	totalFindings := 0
	for _, o := range m.Outputs {
		totalFindings += len(o.TopFindings)
	}

	uniqueFindings := 0
	for _, g := range result.GroupedFindings {
		uniqueFindings += len(g.Findings)
	}

	duplicateRate := 0.0
	if totalFindings > 0 {
		duplicateRate = 1.0 - float64(uniqueFindings)/float64(totalFindings)
		if duplicateRate < 0 {
			duplicateRate = 0
		}
	}

	totalRisks := 0
	for _, o := range m.Outputs {
		totalRisks += len(o.Risks)
	}

	totalRecs := 0
	for _, o := range m.Outputs {
		totalRecs += len(o.Recommendations)
	}

	result.Statistics = MergeResultStats{
		TotalFindings:        totalFindings,
		UniqueFindings:       uniqueFindings,
		DuplicateRate:        duplicateRate,
		ConflictCount:        len(result.IdentifiedConflicts),
		TotalRisks:           totalRisks,
		TotalRecommendations: totalRecs,
		EvidenceGroups:       len(result.GroupedFindings),
	}

	return result, nil
}

// groupFindingsByEvidence groups findings by their evidence pointer.
// Findings with similar text (Jaccard > 0.8) at the same evidence location are deduplicated.
func (m *MechanicalMerger) groupFindingsByEvidence() []FindingGroup {
	// Map from evidence pointer to findings and contributing modes
	type groupEntry struct {
		findings []Finding
		modes    map[string]struct{}
	}
	groups := make(map[string]*groupEntry)

	for _, output := range m.Outputs {
		for _, f := range output.TopFindings {
			// Use evidence pointer as key, or "unspecified" for findings without one
			key := f.EvidencePointer
			if key == "" {
				key = "_unspecified_"
			}

			if groups[key] == nil {
				groups[key] = &groupEntry{
					findings: make([]Finding, 0),
					modes:    make(map[string]struct{}),
				}
			}

			// Check for duplicates within this group using Jaccard similarity
			isDuplicate := false
			normalizedNew := normalizeText(f.Finding)
			tokensNew := tokenize(normalizedNew)

			for _, existing := range groups[key].findings {
				normalizedExisting := normalizeText(existing.Finding)
				tokensExisting := tokenize(normalizedExisting)
				similarity := jaccardSimilarity(tokensNew, tokensExisting)
				if similarity > 0.8 {
					isDuplicate = true
					break
				}
			}

			if !isDuplicate {
				groups[key].findings = append(groups[key].findings, f)
			}
			groups[key].modes[output.ModeID] = struct{}{}
		}
	}

	// Convert to slice of FindingGroup, sorted by evidence pointer
	result := make([]FindingGroup, 0, len(groups))
	for evidence, entry := range groups {
		displayEvidence := evidence
		if evidence == "_unspecified_" {
			displayEvidence = ""
		}
		modes := make([]string, 0, len(entry.modes))
		for mode := range entry.modes {
			modes = append(modes, mode)
		}
		sort.Strings(modes)

		result = append(result, FindingGroup{
			EvidencePointer: displayEvidence,
			Findings:        entry.findings,
			Modes:           modes,
		})
	}

	// Sort groups: specified evidence first (alphabetically), then unspecified
	sort.Slice(result, func(i, j int) bool {
		if result[i].EvidencePointer == "" && result[j].EvidencePointer != "" {
			return false
		}
		if result[i].EvidencePointer != "" && result[j].EvidencePointer == "" {
			return true
		}
		return result[i].EvidencePointer < result[j].EvidencePointer
	})

	return result
}

// groupRisksBySeverity groups risks by their impact level.
func (m *MechanicalMerger) groupRisksBySeverity() []RiskGroup {
	type groupEntry struct {
		risks []Risk
		modes map[string]struct{}
	}
	groups := make(map[ImpactLevel]*groupEntry)

	// Initialize groups for all severity levels
	for _, level := range []ImpactLevel{ImpactCritical, ImpactHigh, ImpactMedium, ImpactLow} {
		groups[level] = &groupEntry{
			risks: make([]Risk, 0),
			modes: make(map[string]struct{}),
		}
	}

	for _, output := range m.Outputs {
		for _, r := range output.Risks {
			level := r.Impact
			if !level.IsValid() {
				level = ImpactMedium // Default for invalid levels
			}

			// Check for duplicates using Jaccard similarity
			isDuplicate := false
			normalizedNew := normalizeText(r.Risk)
			tokensNew := tokenize(normalizedNew)

			for _, existing := range groups[level].risks {
				normalizedExisting := normalizeText(existing.Risk)
				tokensExisting := tokenize(normalizedExisting)
				similarity := jaccardSimilarity(tokensNew, tokensExisting)
				if similarity > 0.8 {
					isDuplicate = true
					break
				}
			}

			if !isDuplicate {
				groups[level].risks = append(groups[level].risks, r)
			}
			groups[level].modes[output.ModeID] = struct{}{}
		}
	}

	// Convert to slice, only including non-empty groups
	result := make([]RiskGroup, 0)
	for _, level := range []ImpactLevel{ImpactCritical, ImpactHigh, ImpactMedium, ImpactLow} {
		entry := groups[level]
		if len(entry.risks) == 0 {
			continue
		}
		modes := make([]string, 0, len(entry.modes))
		for mode := range entry.modes {
			modes = append(modes, mode)
		}
		sort.Strings(modes)

		result = append(result, RiskGroup{
			Severity: level,
			Risks:    entry.risks,
			Modes:    modes,
		})
	}

	return result
}

// groupRecommendationsByAction groups recommendations by inferred action type.
func (m *MechanicalMerger) groupRecommendationsByAction() []RecommendationGroup {
	type groupEntry struct {
		recs  []Recommendation
		modes map[string]struct{}
	}
	groups := make(map[string]*groupEntry)

	for _, output := range m.Outputs {
		for _, r := range output.Recommendations {
			// Infer action type from recommendation text
			actionType := inferActionType(r.Recommendation)

			if groups[actionType] == nil {
				groups[actionType] = &groupEntry{
					recs:  make([]Recommendation, 0),
					modes: make(map[string]struct{}),
				}
			}

			// Check for duplicates using Jaccard similarity
			isDuplicate := false
			normalizedNew := normalizeText(r.Recommendation)
			tokensNew := tokenize(normalizedNew)

			for _, existing := range groups[actionType].recs {
				normalizedExisting := normalizeText(existing.Recommendation)
				tokensExisting := tokenize(normalizedExisting)
				similarity := jaccardSimilarity(tokensNew, tokensExisting)
				if similarity > 0.8 {
					isDuplicate = true
					break
				}
			}

			if !isDuplicate {
				groups[actionType].recs = append(groups[actionType].recs, r)
			}
			groups[actionType].modes[output.ModeID] = struct{}{}
		}
	}

	// Convert to slice, sorted by action type
	result := make([]RecommendationGroup, 0, len(groups))
	for actionType, entry := range groups {
		modes := make([]string, 0, len(entry.modes))
		for mode := range entry.modes {
			modes = append(modes, mode)
		}
		sort.Strings(modes)

		result = append(result, RecommendationGroup{
			ActionType:      actionType,
			Recommendations: entry.recs,
			Modes:           modes,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ActionType < result[j].ActionType
	})

	return result
}

// inferActionType categorizes a recommendation by its action.
func inferActionType(text string) string {
	lower := strings.ToLower(text)

	// Order matters - more specific patterns first
	patterns := []struct {
		keywords   []string
		actionType string
	}{
		{[]string{"add test", "write test", "test coverage", "unit test", "integration test"}, "add-test"},
		{[]string{"refactor", "restructure", "reorganize", "simplify", "clean up"}, "refactor"},
		{[]string{"document", "add comment", "add documentation", "update readme"}, "document"},
		{[]string{"fix", "resolve", "correct", "repair", "patch"}, "fix"},
		{[]string{"add", "implement", "create", "introduce", "include"}, "add-feature"},
		{[]string{"remove", "delete", "eliminate", "drop"}, "remove"},
		{[]string{"update", "upgrade", "migrate", "bump"}, "update"},
		{[]string{"monitor", "log", "track", "observe"}, "monitor"},
		{[]string{"secure", "encrypt", "validate", "sanitize"}, "security"},
		{[]string{"optimize", "improve performance", "speed up", "cache"}, "optimize"},
	}

	for _, p := range patterns {
		for _, kw := range p.keywords {
			if strings.Contains(lower, kw) {
				return p.actionType
			}
		}
	}

	return "other"
}

// detectConflicts identifies potential contradictions between mode outputs.
func (m *MechanicalMerger) detectConflicts() []PotentialConflict {
	var conflicts []PotentialConflict

	// Compare thesis positions between pairs of modes
	conflicts = append(conflicts, m.detectThesisConflicts()...)

	// Compare risk severity assessments
	conflicts = append(conflicts, m.detectSeverityConflicts()...)

	// Compare recommendation directions
	conflicts = append(conflicts, m.detectRecommendationConflicts()...)

	// Sort conflicts by severity descending
	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Severity > conflicts[j].Severity
	})

	return conflicts
}

// detectThesisConflicts finds contradictory thesis statements.
func (m *MechanicalMerger) detectThesisConflicts() []PotentialConflict {
	var conflicts []PotentialConflict

	// Look for opposing sentiment indicators
	negativeIndicators := []string{
		"should not", "shouldn't", "must not", "cannot", "will not",
		"avoid", "prevent", "stop", "reject", "against", "oppose",
		"not recommended", "inadvisable", "risky", "dangerous",
	}
	positiveIndicators := []string{
		"should", "must", "recommend", "suggest", "advise",
		"beneficial", "important", "essential", "necessary",
		"approve", "support", "embrace", "adopt",
	}

	for i := 0; i < len(m.Outputs); i++ {
		for j := i + 1; j < len(m.Outputs); j++ {
			thesisA := strings.ToLower(m.Outputs[i].Thesis)
			thesisB := strings.ToLower(m.Outputs[j].Thesis)

			// Check if they're discussing similar topics (high Jaccard similarity)
			tokensA := tokenize(normalizeText(thesisA))
			tokensB := tokenize(normalizeText(thesisB))
			topicSimilarity := jaccardSimilarity(tokensA, tokensB)

			if topicSimilarity < 0.3 {
				continue // Not discussing similar topics
			}

			// Check for opposing sentiments
			hasNegA := containsAny(thesisA, negativeIndicators)
			hasPosA := containsAny(thesisA, positiveIndicators)
			hasNegB := containsAny(thesisB, negativeIndicators)
			hasPosB := containsAny(thesisB, positiveIndicators)

			// Conflict if one is positive and other is negative on similar topic
			if (hasNegA && hasPosB) || (hasPosA && hasNegB) {
				conflicts = append(conflicts, PotentialConflict{
					Topic:        extractTopic(thesisA, thesisB),
					ModeA:        m.Outputs[i].ModeID,
					ModeB:        m.Outputs[j].ModeID,
					PositionA:    m.Outputs[i].Thesis,
					PositionB:    m.Outputs[j].Thesis,
					ConflictType: "thesis",
					Severity:     0.5 + topicSimilarity*0.5, // Higher similarity = higher severity
				})
			}
		}
	}

	return conflicts
}

// detectSeverityConflicts finds disagreements about risk severity.
func (m *MechanicalMerger) detectSeverityConflicts() []PotentialConflict {
	var conflicts []PotentialConflict

	// Build a map of risk descriptions to (mode, severity) pairs
	type riskEntry struct {
		modeID   string
		severity ImpactLevel
		riskText string
	}

	allRisks := make([]riskEntry, 0)
	for _, output := range m.Outputs {
		for _, r := range output.Risks {
			allRisks = append(allRisks, riskEntry{
				modeID:   output.ModeID,
				severity: r.Impact,
				riskText: r.Risk,
			})
		}
	}

	// Compare pairs of risks
	for i := 0; i < len(allRisks); i++ {
		for j := i + 1; j < len(allRisks); j++ {
			if allRisks[i].modeID == allRisks[j].modeID {
				continue // Same mode
			}

			// Check if discussing similar risk
			tokensA := tokenize(normalizeText(allRisks[i].riskText))
			tokensB := tokenize(normalizeText(allRisks[j].riskText))
			similarity := jaccardSimilarity(tokensA, tokensB)

			if similarity < 0.5 {
				continue // Not similar enough to be same risk
			}

			// Check for severity disagreement
			sevA := severityRank(allRisks[i].severity)
			sevB := severityRank(allRisks[j].severity)
			sevDiff := abs(sevA - sevB)

			if sevDiff >= 2 { // At least 2 levels apart (e.g., critical vs medium)
				conflicts = append(conflicts, PotentialConflict{
					Topic:        extractTopic(allRisks[i].riskText, allRisks[j].riskText),
					ModeA:        allRisks[i].modeID,
					ModeB:        allRisks[j].modeID,
					PositionA:    allRisks[i].severity.String(),
					PositionB:    allRisks[j].severity.String(),
					ConflictType: "severity",
					Severity:     float64(sevDiff) / 3.0, // Normalize to 0-1
				})
			}
		}
	}

	return conflicts
}

// detectRecommendationConflicts finds contradictory recommendations.
func (m *MechanicalMerger) detectRecommendationConflicts() []PotentialConflict {
	var conflicts []PotentialConflict

	// Build list of all recommendations with source mode
	type recEntry struct {
		modeID  string
		recText string
	}

	allRecs := make([]recEntry, 0)
	for _, output := range m.Outputs {
		for _, r := range output.Recommendations {
			allRecs = append(allRecs, recEntry{
				modeID:  output.ModeID,
				recText: r.Recommendation,
			})
		}
	}

	// Opposing action patterns
	opposingPairs := []struct {
		pattern1 string
		pattern2 string
	}{
		{"add", "remove"},
		{"enable", "disable"},
		{"increase", "decrease"},
		{"should", "should not"},
		{"use", "avoid"},
		{"implement", "remove"},
		{"keep", "delete"},
	}

	// Compare pairs of recommendations
	for i := 0; i < len(allRecs); i++ {
		for j := i + 1; j < len(allRecs); j++ {
			if allRecs[i].modeID == allRecs[j].modeID {
				continue // Same mode
			}

			textA := strings.ToLower(allRecs[i].recText)
			textB := strings.ToLower(allRecs[j].recText)

			// Check if discussing similar subject
			tokensA := tokenize(normalizeText(textA))
			tokensB := tokenize(normalizeText(textB))
			similarity := jaccardSimilarity(tokensA, tokensB)

			if similarity < 0.3 {
				continue // Not similar enough
			}

			// Check for opposing actions
			for _, pair := range opposingPairs {
				hasA1 := strings.Contains(textA, pair.pattern1)
				hasA2 := strings.Contains(textA, pair.pattern2)
				hasB1 := strings.Contains(textB, pair.pattern1)
				hasB2 := strings.Contains(textB, pair.pattern2)

				if (hasA1 && hasB2) || (hasA2 && hasB1) {
					conflicts = append(conflicts, PotentialConflict{
						Topic:        extractTopic(allRecs[i].recText, allRecs[j].recText),
						ModeA:        allRecs[i].modeID,
						ModeB:        allRecs[j].modeID,
						PositionA:    allRecs[i].recText,
						PositionB:    allRecs[j].recText,
						ConflictType: "recommendation",
						Severity:     0.4 + similarity*0.6,
					})
					break
				}
			}
		}
	}

	return conflicts
}

// evidenceProximity calculates similarity between two evidence pointers.
// Same file = 0.8, nearby lines (within 10) = 0.5-0.8, different files = 0.
func evidenceProximity(a, b string) float64 {
	if a == "" || b == "" {
		return 0.0
	}
	if a == b {
		return 1.0
	}

	// Parse file:line format
	fileA, lineA := parseEvidencePointer(a)
	fileB, lineB := parseEvidencePointer(b)

	if fileA != fileB {
		return 0.0 // Different files
	}

	// Same file, check line proximity
	if lineA < 0 || lineB < 0 {
		return 0.8 // Same file but no line numbers
	}

	lineDiff := abs(lineA - lineB)
	if lineDiff == 0 {
		return 1.0
	}
	if lineDiff <= 5 {
		return 0.9
	}
	if lineDiff <= 10 {
		return 0.7
	}
	if lineDiff <= 20 {
		return 0.5
	}
	return 0.3 // Same file but far apart
}

// parseEvidencePointer extracts file and line from "file:line" format.
func parseEvidencePointer(ptr string) (file string, line int) {
	parts := strings.Split(ptr, ":")
	if len(parts) == 0 {
		return "", -1
	}
	file = parts[0]
	if len(parts) > 1 {
		var l int
		if _, err := fmt.Sscanf(parts[1], "%d", &l); err == nil {
			line = l
		} else {
			line = -1
		}
	} else {
		line = -1
	}
	return
}

// Helper functions

func containsAny(text string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func extractTopic(textA, textB string) string {
	// Find common significant words
	tokensA := tokenize(normalizeText(textA))
	tokensB := tokenize(normalizeText(textB))

	common := make([]string, 0)
	for token := range tokensA {
		if _, ok := tokensB[token]; ok {
			// Skip very common words
			if len(token) > 3 && !isStopWord(token) {
				common = append(common, token)
			}
		}
	}

	if len(common) == 0 {
		return "general"
	}

	sort.Strings(common)
	if len(common) > 3 {
		common = common[:3]
	}
	return strings.Join(common, " ")
}

func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"the": true, "this": true, "that": true, "with": true,
		"from": true, "have": true, "been": true, "will": true,
		"should": true, "would": true, "could": true, "being": true,
		"there": true, "their": true, "when": true, "where": true,
	}
	return stopWords[word]
}

func severityRank(level ImpactLevel) int {
	switch level {
	case ImpactCritical:
		return 4
	case ImpactHigh:
		return 3
	case ImpactMedium:
		return 2
	case ImpactLow:
		return 1
	default:
		return 2
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
