package ensemble

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExplanationLayer provides reasoning transparency for synthesis conclusions.
type ExplanationLayer struct {
	// Conclusions explains each synthesis conclusion.
	Conclusions []ConclusionExplanation `json:"conclusions,omitempty" yaml:"conclusions,omitempty"`

	// StrategyRationale explains the synthesis strategy used.
	StrategyRationale string `json:"strategy_rationale,omitempty" yaml:"strategy_rationale,omitempty"`

	// ConflictsResolved summarizes conflicts resolved during synthesis.
	ConflictsResolved []ConflictResolution `json:"conflicts_resolved,omitempty" yaml:"conflicts_resolved,omitempty"`

	// ModeWeights shows how modes were weighted in synthesis.
	ModeWeights map[string]float64 `json:"mode_weights,omitempty" yaml:"mode_weights,omitempty"`

	// GeneratedAt is when this explanation was generated.
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
}

// ConclusionExplanation provides detailed reasoning for a single conclusion.
type ConclusionExplanation struct {
	// ConclusionID uniquely identifies this conclusion (can be a provenance ID).
	ConclusionID string `json:"conclusion_id" yaml:"conclusion_id"`

	// Type is the kind of conclusion (finding, risk, recommendation).
	Type ConclusionType `json:"type" yaml:"type"`

	// Text is the conclusion text.
	Text string `json:"text" yaml:"text"`

	// SourceFindings lists provenance IDs that contributed to this conclusion.
	SourceFindings []string `json:"source_findings,omitempty" yaml:"source_findings,omitempty"`

	// SourceModes lists the modes that contributed.
	SourceModes []string `json:"source_modes" yaml:"source_modes"`

	// ConfidenceBasis explains why this confidence level was assigned.
	ConfidenceBasis string `json:"confidence_basis,omitempty" yaml:"confidence_basis,omitempty"`

	// Confidence is the assigned confidence level.
	Confidence Confidence `json:"confidence" yaml:"confidence"`

	// SupportingEvidence lists evidence supporting this conclusion.
	SupportingEvidence []string `json:"supporting_evidence,omitempty" yaml:"supporting_evidence,omitempty"`

	// CounterEvidence lists evidence that challenged this conclusion.
	CounterEvidence []string `json:"counter_evidence,omitempty" yaml:"counter_evidence,omitempty"`

	// Reasoning explains the logic behind this conclusion.
	Reasoning string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
}

// ConclusionType categorizes the type of conclusion.
type ConclusionType string

const (
	ConclusionFinding        ConclusionType = "finding"
	ConclusionRisk           ConclusionType = "risk"
	ConclusionRecommendation ConclusionType = "recommendation"
)

// ConflictResolution documents how a conflict was resolved.
type ConflictResolution struct {
	// ConflictID identifies the conflict.
	ConflictID string `json:"conflict_id" yaml:"conflict_id"`

	// Topic describes what the conflict was about.
	Topic string `json:"topic" yaml:"topic"`

	// Positions shows the competing positions.
	Positions []PositionSummary `json:"positions" yaml:"positions"`

	// Resolution describes how the conflict was resolved.
	Resolution string `json:"resolution" yaml:"resolution"`

	// Method is the resolution method used.
	Method ResolutionMethod `json:"method" yaml:"method"`

	// ConfidenceImpact is how resolution affected confidence.
	ConfidenceImpact float64 `json:"confidence_impact,omitempty" yaml:"confidence_impact,omitempty"`
}

// PositionSummary summarizes a mode's position in a conflict.
type PositionSummary struct {
	ModeID   string  `json:"mode_id" yaml:"mode_id"`
	Position string  `json:"position" yaml:"position"`
	Strength float64 `json:"strength" yaml:"strength"`
}

// ResolutionMethod describes how a conflict was resolved.
type ResolutionMethod string

const (
	ResolutionConsensus   ResolutionMethod = "consensus"
	ResolutionMajority    ResolutionMethod = "majority"
	ResolutionWeighted    ResolutionMethod = "weighted"
	ResolutionAdversarial ResolutionMethod = "adversarial"
	ResolutionDeferred    ResolutionMethod = "deferred"
	ResolutionManual      ResolutionMethod = "manual"
)

// ExplanationTracker accumulates explanation data during synthesis.
type ExplanationTracker struct {
	// conclusions tracks per-conclusion explanations.
	conclusions map[string]*ConclusionExplanation

	// conflicts tracks conflict resolutions.
	conflicts []ConflictResolution

	// strategyRationale stores the strategy explanation.
	strategyRationale string

	// modeWeights stores mode weighting.
	modeWeights map[string]float64

	// provenance links to provenance tracker if available.
	provenance *ProvenanceTracker
}

// NewExplanationTracker creates a tracker for building explanation layers.
func NewExplanationTracker(provenance *ProvenanceTracker) *ExplanationTracker {
	return &ExplanationTracker{
		conclusions: make(map[string]*ConclusionExplanation),
		modeWeights: make(map[string]float64),
		provenance:  provenance,
	}
}

// RecordConclusion records a conclusion with its sources.
func (t *ExplanationTracker) RecordConclusion(
	conclusionID string,
	conclusionType ConclusionType,
	text string,
	sourceModes []string,
	confidence Confidence,
) {
	if t == nil {
		return
	}

	t.conclusions[conclusionID] = &ConclusionExplanation{
		ConclusionID: conclusionID,
		Type:         conclusionType,
		Text:         text,
		SourceModes:  sourceModes,
		Confidence:   confidence,
	}
}

// AddSourceFinding adds a source finding reference to a conclusion.
func (t *ExplanationTracker) AddSourceFinding(conclusionID, findingID string) {
	if t == nil {
		return
	}
	if c, ok := t.conclusions[conclusionID]; ok {
		c.SourceFindings = append(c.SourceFindings, findingID)
	}
}

// SetConfidenceBasis sets the confidence rationale for a conclusion.
func (t *ExplanationTracker) SetConfidenceBasis(conclusionID, basis string) {
	if t == nil {
		return
	}
	if c, ok := t.conclusions[conclusionID]; ok {
		c.ConfidenceBasis = basis
	}
}

// AddSupportingEvidence adds supporting evidence to a conclusion.
func (t *ExplanationTracker) AddSupportingEvidence(conclusionID, evidence string) {
	if t == nil {
		return
	}
	if c, ok := t.conclusions[conclusionID]; ok {
		c.SupportingEvidence = append(c.SupportingEvidence, evidence)
	}
}

// AddCounterEvidence adds counter evidence to a conclusion.
func (t *ExplanationTracker) AddCounterEvidence(conclusionID, evidence string) {
	if t == nil {
		return
	}
	if c, ok := t.conclusions[conclusionID]; ok {
		c.CounterEvidence = append(c.CounterEvidence, evidence)
	}
}

// SetReasoning sets the reasoning for a conclusion.
func (t *ExplanationTracker) SetReasoning(conclusionID, reasoning string) {
	if t == nil {
		return
	}
	if c, ok := t.conclusions[conclusionID]; ok {
		c.Reasoning = reasoning
	}
}

// RecordConflictResolution records how a conflict was resolved.
func (t *ExplanationTracker) RecordConflictResolution(
	topic string,
	positions []PositionSummary,
	resolution string,
	method ResolutionMethod,
) {
	if t == nil {
		return
	}

	conflictID := GenerateConflictID(topic, positions)
	t.conflicts = append(t.conflicts, ConflictResolution{
		ConflictID: conflictID,
		Topic:      topic,
		Positions:  positions,
		Resolution: resolution,
		Method:     method,
	})
}

// SetStrategyRationale sets the synthesis strategy explanation.
func (t *ExplanationTracker) SetStrategyRationale(rationale string) {
	if t == nil {
		return
	}
	t.strategyRationale = rationale
}

// SetModeWeight records a mode's weight in synthesis.
func (t *ExplanationTracker) SetModeWeight(modeID string, weight float64) {
	if t == nil {
		return
	}
	t.modeWeights[modeID] = weight
}

// GenerateLayer produces the final explanation layer.
func (t *ExplanationTracker) GenerateLayer() *ExplanationLayer {
	if t == nil {
		return nil
	}

	layer := &ExplanationLayer{
		Conclusions:       make([]ConclusionExplanation, 0, len(t.conclusions)),
		StrategyRationale: t.strategyRationale,
		ConflictsResolved: t.conflicts,
		ModeWeights:       t.modeWeights,
		GeneratedAt:       time.Now().UTC(),
	}

	for _, c := range t.conclusions {
		layer.Conclusions = append(layer.Conclusions, *c)
	}

	return layer
}

// GenerateConflictID creates a deterministic ID for a conflict.
func GenerateConflictID(topic string, positions []PositionSummary) string {
	var modeIDs []string
	for _, p := range positions {
		modeIDs = append(modeIDs, p.ModeID)
	}
	return fmt.Sprintf("conflict-%s-%s", sanitizeID(topic), strings.Join(modeIDs, "-"))
}

func sanitizeID(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	if len(s) > 20 {
		s = s[:20]
	}
	return s
}

// FormatExplanation produces a human-readable explanation.
func FormatExplanation(layer *ExplanationLayer) string {
	if layer == nil {
		return "No explanation available"
	}

	var b strings.Builder

	fmt.Fprintf(&b, "Synthesis Explanation\n")
	fmt.Fprintf(&b, "=====================\n\n")

	if layer.StrategyRationale != "" {
		fmt.Fprintf(&b, "Strategy Rationale:\n")
		fmt.Fprintf(&b, "  %s\n\n", layer.StrategyRationale)
	}

	if len(layer.ModeWeights) > 0 {
		fmt.Fprintf(&b, "Mode Weights:\n")
		for mode, weight := range layer.ModeWeights {
			fmt.Fprintf(&b, "  %-20s %.2f\n", mode, weight)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(layer.Conclusions) > 0 {
		fmt.Fprintf(&b, "Conclusion Explanations:\n")
		for i, c := range layer.Conclusions {
			fmt.Fprintf(&b, "\n  %d. [%s] %s\n", i+1, c.Type, truncateText(c.Text, 60))
			fmt.Fprintf(&b, "     ID: %s\n", c.ConclusionID)
			fmt.Fprintf(&b, "     Sources: %s\n", strings.Join(c.SourceModes, ", "))
			fmt.Fprintf(&b, "     Confidence: %s\n", c.Confidence.String())
			if c.ConfidenceBasis != "" {
				fmt.Fprintf(&b, "     Basis: %s\n", c.ConfidenceBasis)
			}
			if c.Reasoning != "" {
				fmt.Fprintf(&b, "     Reasoning: %s\n", c.Reasoning)
			}
			if len(c.SupportingEvidence) > 0 {
				fmt.Fprintf(&b, "     Supporting: %d items\n", len(c.SupportingEvidence))
			}
			if len(c.CounterEvidence) > 0 {
				fmt.Fprintf(&b, "     Counter: %d items\n", len(c.CounterEvidence))
			}
		}
	}

	if len(layer.ConflictsResolved) > 0 {
		fmt.Fprintf(&b, "\nConflicts Resolved:\n")
		for i, cr := range layer.ConflictsResolved {
			fmt.Fprintf(&b, "\n  %d. %s\n", i+1, cr.Topic)
			fmt.Fprintf(&b, "     Method: %s\n", cr.Method)
			fmt.Fprintf(&b, "     Resolution: %s\n", cr.Resolution)
			fmt.Fprintf(&b, "     Positions:\n")
			for _, p := range cr.Positions {
				fmt.Fprintf(&b, "       - %s: %s (strength: %.2f)\n", p.ModeID, truncateText(p.Position, 40), p.Strength)
			}
		}
	}

	return b.String()
}

// JSON returns the explanation layer as indented JSON.
func (layer *ExplanationLayer) JSON() ([]byte, error) {
	if layer == nil {
		return nil, fmt.Errorf("nil explanation layer")
	}
	return json.MarshalIndent(layer, "", "  ")
}

// BuildExplanationFromMerge creates explanations from merge results.
func BuildExplanationFromMerge(
	tracker *ExplanationTracker,
	merged *MergedOutput,
	strategy *StrategyConfig,
) {
	if tracker == nil || merged == nil {
		return
	}

	// Set strategy rationale
	if strategy != nil {
		rationale := fmt.Sprintf("Using %s strategy: %s. Best for: %s",
			strategy.Name,
			strategy.Description,
			strings.Join(strategy.BestFor, ", "))
		tracker.SetStrategyRationale(rationale)
	}

	// Record findings as conclusions
	for _, mf := range merged.Findings {
		conclusionID := mf.ProvenanceID
		if conclusionID == "" {
			conclusionID = GenerateFindingID(primarySourceMode(mf.SourceModes), mf.Finding.Finding)
		}

		tracker.RecordConclusion(
			conclusionID,
			ConclusionFinding,
			mf.Finding.Finding,
			mf.SourceModes,
			mf.Finding.Confidence,
		)

		// Set confidence basis based on sources
		if len(mf.SourceModes) > 1 {
			tracker.SetConfidenceBasis(conclusionID,
				fmt.Sprintf("Confirmed by %d modes: %s", len(mf.SourceModes), strings.Join(mf.SourceModes, ", ")))
		} else if len(mf.SourceModes) == 1 {
			tracker.SetConfidenceBasis(conclusionID,
				fmt.Sprintf("Single source: %s", mf.SourceModes[0]))
		} else {
			tracker.SetConfidenceBasis(conclusionID, "No source mode metadata provided")
		}

		// Add provenance references
		for _, mode := range mf.SourceModes {
			srcID := GenerateFindingID(mode, mf.Finding.Finding)
			tracker.AddSourceFinding(conclusionID, srcID)
		}
	}

	// Record risks as conclusions
	for _, mr := range merged.Risks {
		conclusionID := fmt.Sprintf("risk-%s", sanitizeID(mr.Risk.Risk))
		tracker.RecordConclusion(
			conclusionID,
			ConclusionRisk,
			mr.Risk.Risk,
			mr.SourceModes,
			Confidence(mr.Risk.Likelihood),
		)

		if len(mr.SourceModes) > 1 {
			tracker.SetConfidenceBasis(conclusionID,
				fmt.Sprintf("Identified by %d modes", len(mr.SourceModes)))
		}
	}

	// Record recommendations as conclusions
	for _, mr := range merged.Recommendations {
		conclusionID := fmt.Sprintf("rec-%s", sanitizeID(mr.Recommendation.Recommendation))
		tracker.RecordConclusion(
			conclusionID,
			ConclusionRecommendation,
			mr.Recommendation.Recommendation,
			mr.SourceModes,
			0.0, // Recommendations don't have confidence
		)
	}
}

func primarySourceMode(sourceModes []string) string {
	if len(sourceModes) == 0 {
		return "unknown"
	}
	return sourceModes[0]
}

// BuildExplanationFromConflicts records conflict resolutions from audit report.
func BuildExplanationFromConflicts(
	tracker *ExplanationTracker,
	audit *AuditReport,
) {
	if tracker == nil || audit == nil {
		return
	}

	for _, conflict := range audit.Conflicts {
		positions := make([]PositionSummary, 0, len(conflict.Positions))
		for _, p := range conflict.Positions {
			positions = append(positions, PositionSummary{
				ModeID:   p.ModeID,
				Position: p.Position,
				Strength: p.Confidence,
			})
		}

		method := ResolutionManual
		if conflict.ResolutionPath != "" {
			method = inferResolutionMethod(conflict.ResolutionPath)
		}

		tracker.RecordConflictResolution(
			conflict.Topic,
			positions,
			conflict.ResolutionPath,
			method,
		)
	}
}

func inferResolutionMethod(path string) ResolutionMethod {
	path = strings.ToLower(path)
	switch {
	case strings.Contains(path, "consensus"):
		return ResolutionConsensus
	case strings.Contains(path, "majority"):
		return ResolutionMajority
	case strings.Contains(path, "weight"):
		return ResolutionWeighted
	case strings.Contains(path, "defer"):
		return ResolutionDeferred
	default:
		return ResolutionManual
	}
}
