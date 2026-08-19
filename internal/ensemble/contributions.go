package ensemble

import (
	"encoding/json"
	"sort"
	"time"
)

// ContributionScore tracks a single mode's contribution to the ensemble.
type ContributionScore struct {
	// ModeID identifies the mode.
	ModeID string `json:"mode_id" yaml:"mode_id"`

	// ModeName is the human-readable name.
	ModeName string `json:"mode_name,omitempty" yaml:"mode_name,omitempty"`

	// FindingsCount is how many findings from this mode survived deduplication.
	FindingsCount int `json:"findings_count" yaml:"findings_count"`

	// OriginalFindings is the total findings before deduplication.
	OriginalFindings int `json:"original_findings" yaml:"original_findings"`

	// UniqueInsights is findings not seen in other modes.
	UniqueInsights int `json:"unique_insights" yaml:"unique_insights"`

	// CitationCount is how many times this mode was cited in synthesis.
	CitationCount int `json:"citation_count" yaml:"citation_count"`

	// RisksCount is risks contributed by this mode.
	RisksCount int `json:"risks_count" yaml:"risks_count"`

	// RecommendationsCount is recommendations contributed.
	RecommendationsCount int `json:"recommendations_count" yaml:"recommendations_count"`

	// Score is the overall contribution score (0-100).
	Score float64 `json:"score" yaml:"score"`

	// Rank is the position among all modes (1 = highest contributor).
	Rank int `json:"rank" yaml:"rank"`

	// HighlightFindings lists the most impactful unique findings.
	HighlightFindings []string `json:"highlight_findings,omitempty" yaml:"highlight_findings,omitempty"`
}

// ContributionReport summarizes mode contributions across an ensemble run.
type ContributionReport struct {
	// GeneratedAt is when this report was created.
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`

	// Scores lists per-mode contribution scores, ordered by rank.
	Scores []ContributionScore `json:"scores" yaml:"scores"`

	// TotalFindings is the total findings across all modes before dedup.
	TotalFindings int `json:"total_findings" yaml:"total_findings"`

	// DedupedFindings is findings after deduplication.
	DedupedFindings int `json:"deduped_findings" yaml:"deduped_findings"`

	// OverlapRate measures how much modes agree (0 = no overlap, 1 = complete).
	OverlapRate float64 `json:"overlap_rate" yaml:"overlap_rate"`

	// DiversityScore measures how unique each mode's contributions are.
	DiversityScore float64 `json:"diversity_score" yaml:"diversity_score"`
}

// ContributionTracker accumulates contribution data during synthesis.
type ContributionTracker struct {
	// modeScores tracks per-mode statistics.
	modeScores map[string]*ContributionScore

	// Config controls scoring weights.
	Config ContributionConfig
}

// ContributionConfig controls how contribution scores are calculated.
type ContributionConfig struct {
	// FindingsWeight is the weight for surviving findings (default: 0.4).
	FindingsWeight float64 `json:"findings_weight" yaml:"findings_weight"`

	// UniqueWeight is the weight for unique insights (default: 0.3).
	UniqueWeight float64 `json:"unique_weight" yaml:"unique_weight"`

	// CitationWeight is the weight for synthesis citations (default: 0.2).
	CitationWeight float64 `json:"citation_weight" yaml:"citation_weight"`

	// RisksWeight is the weight for risks contributed (default: 0.05).
	RisksWeight float64 `json:"risks_weight" yaml:"risks_weight"`

	// RecommendationsWeight is the weight for recommendations (default: 0.05).
	RecommendationsWeight float64 `json:"recommendations_weight" yaml:"recommendations_weight"`

	// MaxHighlights limits highlight findings per mode.
	MaxHighlights int `json:"max_highlights" yaml:"max_highlights"`
}

// DefaultContributionConfig returns default scoring weights.
func DefaultContributionConfig() ContributionConfig {
	return ContributionConfig{
		FindingsWeight:        0.40,
		UniqueWeight:          0.30,
		CitationWeight:        0.20,
		RisksWeight:           0.05,
		RecommendationsWeight: 0.05,
		MaxHighlights:         3,
	}
}

// NewContributionTracker creates a tracker with default config.
func NewContributionTracker() *ContributionTracker {
	return &ContributionTracker{
		modeScores: make(map[string]*ContributionScore),
		Config:     DefaultContributionConfig(),
	}
}

// RecordOriginalFinding records a finding before deduplication.
func (t *ContributionTracker) RecordOriginalFinding(modeID string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.OriginalFindings++
}

// RecordSurvivingFinding records a finding that survived deduplication.
func (t *ContributionTracker) RecordSurvivingFinding(modeID, findingText string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.FindingsCount++
}

// RecordUniqueFinding records a finding unique to this mode.
func (t *ContributionTracker) RecordUniqueFinding(modeID, findingText string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.UniqueInsights++
	if t.Config.MaxHighlights > 0 && len(score.HighlightFindings) < t.Config.MaxHighlights {
		highlight := findingText
		if len(highlight) > 80 {
			r := []rune(highlight)
			if len(r) > 80 {
				highlight = string(r[:77]) + "..."
			}
		}
		score.HighlightFindings = append(score.HighlightFindings, highlight)
	}
}

// RecordCitation records a mode being cited in synthesis output.
func (t *ContributionTracker) RecordCitation(modeID string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.CitationCount++
}

// RecordRisk records a risk contributed by a mode.
func (t *ContributionTracker) RecordRisk(modeID string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.RisksCount++
}

// RecordRecommendation records a recommendation contributed by a mode.
func (t *ContributionTracker) RecordRecommendation(modeID string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.RecommendationsCount++
}

// SetModeName sets the human-readable name for a mode.
func (t *ContributionTracker) SetModeName(modeID, modeName string) {
	if t == nil {
		return
	}
	score := t.getOrCreate(modeID)
	score.ModeName = modeName
}

func (t *ContributionTracker) getOrCreate(modeID string) *ContributionScore {
	if t.modeScores[modeID] == nil {
		t.modeScores[modeID] = &ContributionScore{ModeID: modeID}
	}
	return t.modeScores[modeID]
}

// GenerateReport computes final scores and creates the report.
func (t *ContributionTracker) GenerateReport() *ContributionReport {
	if t == nil {
		return nil
	}

	report := &ContributionReport{
		GeneratedAt: time.Now(),
		Scores:      make([]ContributionScore, 0, len(t.modeScores)),
	}

	// Compute totals for normalization
	var totalFindings, totalUnique, totalCitations, totalRisks, totalRecs int
	for _, score := range t.modeScores {
		totalFindings += score.FindingsCount
		totalUnique += score.UniqueInsights
		totalCitations += score.CitationCount
		totalRisks += score.RisksCount
		totalRecs += score.RecommendationsCount
		report.TotalFindings += score.OriginalFindings
	}
	report.DedupedFindings = totalFindings

	// Calculate overlap rate
	if report.TotalFindings > 0 {
		report.OverlapRate = 1.0 - float64(report.DedupedFindings)/float64(report.TotalFindings)
		if report.OverlapRate < 0 {
			report.OverlapRate = 0
		}
	}

	// Compute normalized scores
	cfg := t.Config
	for _, score := range t.modeScores {
		normalized := 0.0

		// Findings component
		if totalFindings > 0 {
			normalized += cfg.FindingsWeight * (float64(score.FindingsCount) / float64(totalFindings))
		}

		// Unique insights component
		if totalUnique > 0 {
			normalized += cfg.UniqueWeight * (float64(score.UniqueInsights) / float64(totalUnique))
		}

		// Citation component
		if totalCitations > 0 {
			normalized += cfg.CitationWeight * (float64(score.CitationCount) / float64(totalCitations))
		}

		// Risks component
		if totalRisks > 0 {
			normalized += cfg.RisksWeight * (float64(score.RisksCount) / float64(totalRisks))
		}

		// Recommendations component
		if totalRecs > 0 {
			normalized += cfg.RecommendationsWeight * (float64(score.RecommendationsCount) / float64(totalRecs))
		}

		// Scale to 0-100
		score.Score = normalized * 100

		cpy := *score
		report.Scores = append(report.Scores, cpy)
	}

	// Sort by score descending and assign ranks
	sort.Slice(report.Scores, func(i, j int) bool {
		return report.Scores[i].Score > report.Scores[j].Score
	})
	for i := range report.Scores {
		report.Scores[i].Rank = i + 1
	}

	// Compute diversity score (based on how evenly distributed unique insights are)
	if len(report.Scores) > 1 && totalUnique > 0 {
		// Use coefficient of variation: std/mean
		mean := float64(totalUnique) / float64(len(report.Scores))
		var variance float64
		for _, score := range report.Scores {
			diff := float64(score.UniqueInsights) - mean
			variance += diff * diff
		}
		variance /= float64(len(report.Scores))
		if mean > 0 {
			cv := (variance / (mean * mean)) // squared coefficient of variation
			// High CV = low diversity, low CV = high diversity
			report.DiversityScore = 1.0 / (1.0 + cv)
		}
	}

	return report
}

// JSON returns the report as indented JSON.
func (r *ContributionReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// TrackContributionsFromMerge populates contribution data from merge results.
func TrackContributionsFromMerge(tracker *ContributionTracker, merged *MergedOutput) {
	if tracker == nil || merged == nil {
		return
	}

	// Track surviving findings
	for _, mf := range merged.Findings {
		for _, mode := range mf.SourceModes {
			tracker.RecordSurvivingFinding(mode, mf.Finding.Finding)
		}

		// If only one mode contributed, it's a unique insight
		if len(mf.SourceModes) == 1 {
			tracker.RecordUniqueFinding(mf.SourceModes[0], mf.Finding.Finding)
		}
	}

	// Track risks
	for _, mr := range merged.Risks {
		for _, mode := range mr.SourceModes {
			tracker.RecordRisk(mode)
		}
	}

	// Track recommendations
	for _, mr := range merged.Recommendations {
		for _, mode := range mr.SourceModes {
			tracker.RecordRecommendation(mode)
		}
	}
}

// TrackOriginalFindings records the original finding counts before merge.
func TrackOriginalFindings(tracker *ContributionTracker, outputs []ModeOutput) {
	if tracker == nil {
		return
	}
	for _, o := range outputs {
		for range o.TopFindings {
			tracker.RecordOriginalFinding(o.ModeID)
		}
	}
}
