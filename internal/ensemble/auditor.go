package ensemble

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// DisagreementAuditor surfaces conflicts across mode outputs.
type DisagreementAuditor struct {
	Outputs         []ModeOutput
	SynthesisResult *SynthesisResult
}

// SynthesisResult represents the combined output from a synthesis stage.
// This will be extended by the synthesis subsystem as it matures.
type SynthesisResult struct {
	Summary          string               `json:"summary" yaml:"summary"`
	Findings         []Finding            `json:"findings,omitempty" yaml:"findings,omitempty"`
	Risks            []Risk               `json:"risks,omitempty" yaml:"risks,omitempty"`
	Recommendations  []Recommendation     `json:"recommendations,omitempty" yaml:"recommendations,omitempty"`
	QuestionsForUser []Question           `json:"questions_for_user,omitempty" yaml:"questions_for_user,omitempty"`
	Confidence       Confidence           `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	RawOutput        string               `json:"raw_output,omitempty" yaml:"raw_output,omitempty"`
	GeneratedAt      time.Time            `json:"generated_at,omitempty" yaml:"generated_at,omitempty"`
	Explanation      *ExplanationLayer    `json:"explanation,omitempty" yaml:"explanation,omitempty"`
	Contributions    *ContributionReport  `json:"contributions,omitempty" yaml:"contributions,omitempty"`
}

// AuditReport captures disagreement analysis across modes.
type AuditReport struct {
	Conflicts             []DetailedConflict  `json:"conflicts"`
	EvidenceNeeded        []EvidenceRequest   `json:"evidence_needed,omitempty"`
	ModeDisagreements     map[string][]string `json:"mode_disagreements,omitempty"`
	ResolutionSuggestions []string            `json:"resolution_suggestions,omitempty"`
}

// DetailedConflict represents a specific disagreement between modes.
type DetailedConflict struct {
	Topic          string             `json:"topic"`
	Positions      []ConflictPosition `json:"positions"`
	ResolutionPath string             `json:"resolution_path,omitempty"`
	EvidenceNeeded string             `json:"evidence_needed,omitempty"`
	Severity       ConflictSeverity   `json:"severity"`
}

// ConflictPosition captures a mode's stance on a conflict.
type ConflictPosition struct {
	ModeID     string  `json:"mode_id"`
	ModeName   string  `json:"mode_name,omitempty"`
	Position   string  `json:"position"`
	Evidence   string  `json:"evidence,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// EvidenceRequest records supporting evidence required to resolve a conflict.
type EvidenceRequest struct {
	Topic       string   `json:"topic"`
	RequestedBy []string `json:"requested_by,omitempty"`
	Rationale   string   `json:"rationale,omitempty"`
}

// ConflictSeverity signals how impactful a disagreement is.
type ConflictSeverity string

const (
	ConflictLow    ConflictSeverity = "low"
	ConflictMedium ConflictSeverity = "medium"
	ConflictHigh   ConflictSeverity = "high"
)

// NewDisagreementAuditor creates an auditor for mode outputs.
func NewDisagreementAuditor(outputs []ModeOutput, synthesis *SynthesisResult) *DisagreementAuditor {
	return &DisagreementAuditor{
		Outputs:         outputs,
		SynthesisResult: synthesis,
	}
}

// Audit runs conflict analysis and returns a report.
func (a *DisagreementAuditor) Audit() (*AuditReport, error) {
	if a == nil {
		return nil, errors.New("auditor is nil")
	}
	if len(a.Outputs) == 0 {
		return nil, errors.New("no outputs provided")
	}

	conflicts := a.IdentifyConflicts()
	report := &AuditReport{
		Conflicts:             conflicts,
		EvidenceNeeded:        buildEvidenceRequests(conflicts),
		ModeDisagreements:     buildModeDisagreements(conflicts),
		ResolutionSuggestions: suggestResolutions(conflicts),
	}

	return report, nil
}

// IdentifyConflicts returns a list of detected disagreements across modes.
func (a *DisagreementAuditor) IdentifyConflicts() []DetailedConflict {
	if a == nil || len(a.Outputs) < 2 {
		return nil
	}

	var conflicts []DetailedConflict

	if conflict, ok := buildConflict("Thesis divergence", a.Outputs, func(o ModeOutput) string {
		return o.Thesis
	}); ok {
		conflicts = append(conflicts, conflict)
	}

	if conflict, ok := buildConflict("Key findings diverge", a.Outputs, func(o ModeOutput) string {
		return summarizeFindings(o.TopFindings, 3)
	}); ok {
		conflicts = append(conflicts, conflict)
	}

	if conflict, ok := buildConflict("Risk assessments diverge", a.Outputs, func(o ModeOutput) string {
		return summarizeRisks(o.Risks, 3)
	}); ok {
		conflicts = append(conflicts, conflict)
	}

	if conflict, ok := buildConflict("Recommendations diverge", a.Outputs, func(o ModeOutput) string {
		return summarizeRecommendations(o.Recommendations, 3)
	}); ok {
		conflicts = append(conflicts, conflict)
	}

	return conflicts
}

// SuggestResolutions returns high-level resolution paths for conflicts.
func (a *DisagreementAuditor) SuggestResolutions() []string {
	if a == nil {
		return nil
	}
	return suggestResolutions(a.IdentifyConflicts())
}

// GeneratePrompt builds the prompt for an auditor agent.
func (a *DisagreementAuditor) GeneratePrompt() string {
	return fmt.Sprintf(auditorPromptTemplate,
		formatOutputs(a.Outputs),
		formatSynthesis(a.SynthesisResult),
		auditSchemaJSON(),
	)
}

const auditorPromptTemplate = `You are the DISAGREEMENT AUDITOR for a reasoning ensemble.

Your role: Identify where mode agents DISAGREE and what evidence would resolve conflicts.

## Mode Outputs
%s

## Synthesizer's Merged Report
%s

## Your Task
1. Identify all points of disagreement
2. For each conflict, state what evidence would resolve it
3. Note which modes are most/least in alignment
4. Do NOT try to resolve conflicts - just surface them clearly

## Output Format
%s
`

func buildConflict(topic string, outputs []ModeOutput, positionFn func(ModeOutput) string) (DetailedConflict, bool) {
	positions := buildPositions(outputs, positionFn)
	if len(positions) < 2 {
		return DetailedConflict{}, false
	}
	if !positionsDiverge(positions) {
		return DetailedConflict{}, false
	}

	conflict := DetailedConflict{
		Topic:          topic,
		Positions:      positions,
		ResolutionPath: defaultResolutionPath(topic),
		EvidenceNeeded: defaultEvidenceNeeded(topic),
		Severity:       conflictSeverity(positions),
	}

	return conflict, true
}

func buildPositions(outputs []ModeOutput, positionFn func(ModeOutput) string) []ConflictPosition {
	positions := make([]ConflictPosition, 0, len(outputs))
	for _, output := range outputs {
		position := strings.TrimSpace(positionFn(output))
		if position == "" {
			continue
		}
		positions = append(positions, ConflictPosition{
			ModeID:     output.ModeID,
			Position:   position,
			Evidence:   firstEvidencePointer(output),
			Confidence: float64(output.Confidence),
		})
	}
	return positions
}

func positionsDiverge(positions []ConflictPosition) bool {
	if len(positions) < 2 {
		return false
	}

	normalized := make([]string, 0, len(positions))
	for _, position := range positions {
		normalized = append(normalized, normalizeText(position.Position))
	}

	unique := make(map[string]struct{}, len(normalized))
	for _, value := range normalized {
		if value == "" {
			continue
		}
		unique[value] = struct{}{}
	}
	if len(unique) <= 1 {
		return false
	}

	tokens := make([]map[string]struct{}, 0, len(normalized))
	for _, value := range normalized {
		tokens = append(tokens, tokenize(value))
	}

	minSimilarity := 1.0
	for i := 0; i < len(tokens); i++ {
		for j := i + 1; j < len(tokens); j++ {
			similarity := jaccardSimilarity(tokens[i], tokens[j])
			if similarity < minSimilarity {
				minSimilarity = similarity
			}
		}
	}

	return minSimilarity < 0.35
}

func conflictSeverity(positions []ConflictPosition) ConflictSeverity {
	if len(positions) >= 4 {
		return ConflictHigh
	}

	var totalConfidence float64
	for _, position := range positions {
		totalConfidence += position.Confidence
	}
	avg := 0.0
	if len(positions) > 0 {
		avg = totalConfidence / float64(len(positions))
	}

	switch {
	case len(positions) >= 3 || avg >= 0.75:
		return ConflictHigh
	case avg >= 0.45:
		return ConflictMedium
	default:
		return ConflictLow
	}
}

func summarizeFindings(findings []Finding, limit int) string {
	if len(findings) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(findings)
	}
	parts := make([]string, 0, limit)
	for i, finding := range findings {
		if i >= limit {
			break
		}
		if finding.Finding != "" {
			parts = append(parts, finding.Finding)
		}
	}
	return strings.Join(parts, " | ")
}

func summarizeRisks(risks []Risk, limit int) string {
	if len(risks) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(risks)
	}
	parts := make([]string, 0, limit)
	for i, risk := range risks {
		if i >= limit {
			break
		}
		if risk.Risk != "" {
			parts = append(parts, risk.Risk)
		}
	}
	return strings.Join(parts, " | ")
}

func summarizeRecommendations(recommendations []Recommendation, limit int) string {
	if len(recommendations) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(recommendations)
	}
	parts := make([]string, 0, limit)
	for i, rec := range recommendations {
		if i >= limit {
			break
		}
		if rec.Recommendation != "" {
			parts = append(parts, rec.Recommendation)
		}
	}
	return strings.Join(parts, " | ")
}

func firstEvidencePointer(output ModeOutput) string {
	for _, finding := range output.TopFindings {
		if strings.TrimSpace(finding.EvidencePointer) != "" {
			return finding.EvidencePointer
		}
	}
	return ""
}

func defaultResolutionPath(topic string) string {
	lowered := strings.ToLower(topic)
	switch {
	case strings.Contains(lowered, "risk"):
		return "Validate risk assumptions with targeted tests or measurements."
	case strings.Contains(lowered, "recommendation"):
		return "Compare tradeoffs against constraints and stakeholder priorities."
	default:
		return "Gather objective evidence and reconcile the conflicting positions."
	}
}

func defaultEvidenceNeeded(topic string) string {
	lowered := strings.ToLower(topic)
	switch {
	case strings.Contains(lowered, "thesis"):
		return "Provide supporting evidence for each thesis (logs, traces, or code references)."
	case strings.Contains(lowered, "risk"):
		return "Evidence showing likelihood and impact of each risk assessment."
	default:
		return "Concrete evidence to support each conflicting claim."
	}
}

func buildEvidenceRequests(conflicts []DetailedConflict) []EvidenceRequest {
	requests := make([]EvidenceRequest, 0, len(conflicts))
	for _, conflict := range conflicts {
		missing := make([]string, 0)
		for _, position := range conflict.Positions {
			if strings.TrimSpace(position.Evidence) == "" {
				missing = append(missing, position.ModeID)
			}
		}
		if conflict.EvidenceNeeded == "" && len(missing) == 0 {
			continue
		}
		requests = append(requests, EvidenceRequest{
			Topic:       conflict.Topic,
			RequestedBy: uniqueStrings(missing),
			Rationale:   conflict.EvidenceNeeded,
		})
	}

	return requests
}

func buildModeDisagreements(conflicts []DetailedConflict) map[string][]string {
	modeMap := make(map[string]map[string]struct{})
	for _, conflict := range conflicts {
		modeIDs := uniqueStrings(extractModeIDs(conflict.Positions))
		for _, mode := range modeIDs {
			if _, ok := modeMap[mode]; !ok {
				modeMap[mode] = make(map[string]struct{})
			}
			for _, other := range modeIDs {
				if other == mode {
					continue
				}
				modeMap[mode][other] = struct{}{}
			}
		}
	}

	result := make(map[string][]string, len(modeMap))
	for mode, others := range modeMap {
		list := make([]string, 0, len(others))
		for other := range others {
			list = append(list, other)
		}
		sort.Strings(list)
		result[mode] = list
	}

	return result
}

func suggestResolutions(conflicts []DetailedConflict) []string {
	if len(conflicts) == 0 {
		return nil
	}

	suggestions := []string{
		"Collect concrete evidence supporting each conflicting claim.",
		"Run targeted experiments or tests to validate disputed points.",
		"Check authoritative documentation or source-of-truth data.",
	}

	highSeverity := false
	for _, conflict := range conflicts {
		if conflict.Severity == ConflictHigh {
			highSeverity = true
			break
		}
	}
	if highSeverity {
		suggestions = append(suggestions, "Prioritize resolving high-severity disagreements before synthesis.")
	}

	return suggestions
}

func auditSchemaJSON() string {
	sample := AuditReport{
		Conflicts: []DetailedConflict{
			{
				Topic: "Conflicting assessment of root cause",
				Positions: []ConflictPosition{
					{
						ModeID:     "deductive",
						ModeName:   "Deductive",
						Position:   "Root cause is a missing nil check in handler X.",
						Evidence:   "internal/handler.go:42",
						Confidence: 0.72,
					},
					{
						ModeID:     "counterfactual",
						ModeName:   "Counterfactual",
						Position:   "Root cause is an upstream config mismatch.",
						Evidence:   "config/settings.yaml:12",
						Confidence: 0.61,
					},
				},
				ResolutionPath: "Gather logs and reproduce the failure to isolate the root cause.",
				EvidenceNeeded: "Stack trace or failing test evidence that confirms the trigger.",
				Severity:       ConflictMedium,
			},
		},
		EvidenceNeeded: []EvidenceRequest{
			{
				Topic:       "Root cause evidence",
				RequestedBy: []string{"deductive", "counterfactual"},
				Rationale:   "Provide evidence to resolve the disagreement.",
			},
		},
		ModeDisagreements: map[string][]string{
			"deductive":      []string{"counterfactual"},
			"counterfactual": []string{"deductive"},
		},
		ResolutionSuggestions: []string{
			"Collect runtime logs and failing stack traces.",
			"Review recent changes that could explain the divergence.",
		},
	}

	data, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

func formatOutputs(outputs []ModeOutput) string {
	if len(outputs) == 0 {
		return "[]"
	}
	data, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return string(data)
}

func formatSynthesis(synthesis *SynthesisResult) string {
	if synthesis == nil {
		return "{}"
	}
	data, err := json.MarshalIndent(synthesis, "", "  ")
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return string(data)
}

func extractModeIDs(positions []ConflictPosition) []string {
	modes := make([]string, 0, len(positions))
	for _, position := range positions {
		if position.ModeID != "" {
			modes = append(modes, position.ModeID)
		}
	}
	return modes
}

func uniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeText(input string) string {
	if input == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(input))
	lastSpace := false
	for _, r := range strings.ToLower(input) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func tokenize(input string) map[string]struct{} {
	parts := strings.Fields(input)
	tokens := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		tokens[part] = struct{}{}
	}
	return tokens
}

func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	intersection := 0
	for token := range a {
		if _, ok := b[token]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
