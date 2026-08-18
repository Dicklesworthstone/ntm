package ensemble

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Synthesizer orchestrates the synthesis of mode outputs.
type Synthesizer struct {
	// Config controls synthesis behavior.
	Config SynthesisConfig

	// Strategy is the resolved strategy configuration.
	Strategy *StrategyConfig

	// MergeConfig controls mechanical merging.
	MergeConfig MergeConfig
}

// SynthesisChunkType identifies the type of streamed synthesis output.
type SynthesisChunkType string

const (
	ChunkStatus         SynthesisChunkType = "status"
	ChunkFinding        SynthesisChunkType = "finding"
	ChunkRisk           SynthesisChunkType = "risk"
	ChunkRecommendation SynthesisChunkType = "recommendation"
	ChunkQuestion       SynthesisChunkType = "question"
	ChunkExplanation    SynthesisChunkType = "explanation"
	ChunkComplete       SynthesisChunkType = "complete"
)

// SynthesisChunk is a single streamed synthesis event.
type SynthesisChunk struct {
	Type      SynthesisChunkType `json:"type"`
	Content   string             `json:"content"`
	ModeID    string             `json:"mode_id,omitempty"`
	Index     int                `json:"index"`
	Timestamp time.Time          `json:"timestamp"`
}

// NewSynthesizer creates a synthesizer with the given config.
func NewSynthesizer(cfg SynthesisConfig) (*Synthesizer, error) {
	strategyName := string(cfg.Strategy)
	if strategyName == "" {
		strategyName = string(StrategyManual)
	}

	strategy, err := GetStrategy(strategyName)
	if err != nil {
		return nil, fmt.Errorf("invalid strategy: %w", err)
	}

	return &Synthesizer{
		Config:      cfg,
		Strategy:    strategy,
		MergeConfig: DefaultMergeConfig(),
	}, nil
}

// StreamSynthesize emits synthesis output in ordered chunks.
func (s *Synthesizer) StreamSynthesize(ctx context.Context, input *SynthesisInput) (<-chan SynthesisChunk, <-chan error) {
	chunks := make(chan SynthesisChunk, 4)
	errs := make(chan error, 1)

	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		defer close(chunks)
		defer close(errs)

		// Check for pre-canceled context before doing any work.
		if ctx.Err() != nil {
			errs <- ctx.Err()
			return
		}

		if s == nil {
			errs <- fmt.Errorf("synthesizer is nil")
			return
		}
		if input == nil {
			errs <- fmt.Errorf("input is nil")
			return
		}
		if len(input.Outputs) == 0 {
			errs <- fmt.Errorf("no outputs to synthesize")
			return
		}

		index := 0
		emit := func(chunk SynthesisChunk) bool {
			index++
			chunk.Index = index
			if chunk.Timestamp.IsZero() {
				chunk.Timestamp = time.Now().UTC()
			}
			select {
			case <-ctx.Done():
				return false
			case chunks <- chunk:
				return true
			}
		}

		if !emit(SynthesisChunk{Type: ChunkStatus, Content: "synthesis started"}) {
			errs <- ctx.Err()
			return
		}

		result, err := s.Synthesize(input)
		if err != nil {
			if ctx.Err() != nil {
				errs <- ctx.Err()
				return
			}
			errs <- err
			return
		}

		if !emit(SynthesisChunk{Type: ChunkStatus, Content: "synthesis merged"}) {
			errs <- ctx.Err()
			return
		}

		for _, finding := range result.Findings {
			if !emit(SynthesisChunk{Type: ChunkFinding, Content: finding.Finding}) {
				errs <- ctx.Err()
				return
			}
		}
		for _, risk := range result.Risks {
			if !emit(SynthesisChunk{Type: ChunkRisk, Content: risk.Risk}) {
				errs <- ctx.Err()
				return
			}
		}
		for _, rec := range result.Recommendations {
			if !emit(SynthesisChunk{Type: ChunkRecommendation, Content: rec.Recommendation}) {
				errs <- ctx.Err()
				return
			}
		}
		for _, question := range result.QuestionsForUser {
			if !emit(SynthesisChunk{Type: ChunkQuestion, Content: question.Question}) {
				errs <- ctx.Err()
				return
			}
		}
		if result.Explanation != nil {
			if !emit(SynthesisChunk{Type: ChunkExplanation, Content: FormatExplanation(result.Explanation)}) {
				errs <- ctx.Err()
				return
			}
		}

		if !emit(SynthesisChunk{Type: ChunkComplete, Content: result.Summary}) {
			errs <- ctx.Err()
			return
		}
	}()

	return chunks, errs
}

// Synthesize combines mode outputs into a synthesis result.
// For agent-based strategies, this returns the prompt for the synthesizer agent.
// For manual strategies, this performs mechanical merging directly.
func (s *Synthesizer) Synthesize(input *SynthesisInput) (*SynthesisResult, error) {
	if s == nil {
		return nil, fmt.Errorf("synthesizer is nil")
	}
	if input == nil {
		return nil, fmt.Errorf("input is nil")
	}
	if len(input.Outputs) == 0 {
		return nil, fmt.Errorf("no outputs to synthesize")
	}

	// Apply config overrides
	if s.Config.MaxFindings > 0 {
		s.MergeConfig.MaxFindings = s.Config.MaxFindings
	}
	if s.Config.MinConfidence > 0 {
		s.MergeConfig.MinConfidence = s.Config.MinConfidence
	}

	// Manual strategies do mechanical merge only
	if !s.Strategy.RequiresAgent {
		return s.mechanicalSynthesize(input)
	}

	// Agent-based strategies - for now, fall back to mechanical // placebo-waiver: bd-lzu08
	// Full agent synthesis would inject a synthesizer agent prompt
	// and wait for its response. Tracked in bd-lzu08.
	return s.mechanicalSynthesize(input)
}

// mechanicalSynthesize performs deterministic merging without an AI agent.
func (s *Synthesizer) mechanicalSynthesize(input *SynthesisInput) (*SynthesisResult, error) {
	// Initialize contribution tracking
	contribTracker := NewContributionTracker()

	// Record original findings before deduplication
	TrackOriginalFindings(contribTracker, input.Outputs)

	merged := MergeOutputsWithProvenance(input.Outputs, s.MergeConfig, input.Provenance)

	// Track contributions from merged output
	TrackContributionsFromMerge(contribTracker, merged)

	// Convert merged findings to plain findings
	findings := make([]Finding, 0, len(merged.Findings))
	for _, mf := range merged.Findings {
		findings = append(findings, mf.Finding)
	}

	// Convert merged risks to plain risks
	risks := make([]Risk, 0, len(merged.Risks))
	for _, mr := range merged.Risks {
		risks = append(risks, mr.Risk)
	}

	// Convert merged recommendations to plain recommendations
	recommendations := make([]Recommendation, 0, len(merged.Recommendations))
	for _, mr := range merged.Recommendations {
		recommendations = append(recommendations, mr.Recommendation)
	}

	result := &SynthesisResult{
		Summary:          ConsolidateTheses(input.Outputs),
		Findings:         findings,
		Risks:            risks,
		Recommendations:  recommendations,
		QuestionsForUser: merged.Questions,
		Confidence:       AverageConfidence(input.Outputs),
		GeneratedAt:      time.Now().UTC(),
	}

	if input.Provenance != nil {
		for i, mf := range merged.Findings {
			if mf.ProvenanceID != "" {
				_ = input.Provenance.RecordSynthesisCitation(mf.ProvenanceID, fmt.Sprintf("findings[%d]", i))
				// Track citations for contribution scoring
				for _, mode := range mf.SourceModes {
					contribTracker.RecordCitation(mode)
				}
			}
		}
	}

	// Generate explanation layer if requested
	if s.Config.IncludeExplanation {
		explTracker := NewExplanationTracker(input.Provenance)
		BuildExplanationFromMerge(explTracker, merged, s.Strategy)
		if input.AuditReport != nil {
			BuildExplanationFromConflicts(explTracker, input.AuditReport)
		}
		result.Explanation = explTracker.GenerateLayer()
	}

	// Generate and attach contribution report
	result.Contributions = contribTracker.GenerateReport()

	return result, nil
}

// GeneratePrompt builds the prompt for a synthesizer agent.
// This is used when Strategy.RequiresAgent is true.
func (s *Synthesizer) GeneratePrompt(input *SynthesisInput) string {
	if s == nil || input == nil {
		return ""
	}

	templateKey := s.Strategy.TemplateKey
	if templateKey == "" {
		templateKey = "synthesis_default"
	}

	return fmt.Sprintf(synthesizerPromptTemplate,
		input.OriginalQuestion,
		s.Strategy.Name,
		s.Strategy.Description,
		formatModeOutputs(input.Outputs),
		formatAuditSummary(input.AuditReport),
		s.Config.MaxFindings,
		float64(s.Config.MinConfidence),
		synthesisSchemaJSON(),
	)
}

const synthesizerPromptTemplate = `You are the SYNTHESIZER for a reasoning ensemble.

Your role: Combine outputs from multiple reasoning modes into a cohesive, high-quality synthesis.

## Original Question
%s

## Synthesis Strategy: %s
%s

## Mode Outputs
%s

## Disagreement Analysis
%s

## Constraints
- Maximum findings to include: %d
- Minimum confidence threshold: %.2f

## Your Task
1. Read all mode outputs carefully
2. Identify key agreements and disagreements
3. Synthesize a unified analysis that:
   - Highlights the strongest findings (supported by multiple modes)
   - Notes significant disagreements and how to resolve them
   - Ranks risks and recommendations by importance
   - Maintains appropriate confidence levels
4. Generate output in the required schema format

## Output Format
%s
`

// formatModeOutputs converts outputs to JSON for the prompt.
func formatModeOutputs(outputs []ModeOutput) string {
	if len(outputs) == 0 {
		return "[]"
	}

	// Include only essential fields to reduce prompt size
	type compactOutput struct {
		ModeID      string     `json:"mode_id"`
		Thesis      string     `json:"thesis"`
		TopFindings []Finding  `json:"top_findings"`
		Risks       []Risk     `json:"risks,omitempty"`
		Confidence  Confidence `json:"confidence"`
	}

	compact := make([]compactOutput, 0, len(outputs))
	for _, o := range outputs {
		compact = append(compact, compactOutput{
			ModeID:      o.ModeID,
			Thesis:      o.Thesis,
			TopFindings: o.TopFindings,
			Risks:       o.Risks,
			Confidence:  o.Confidence,
		})
	}

	data, err := json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return string(data)
}

// formatAuditSummary converts an audit report to a summary for the prompt.
func formatAuditSummary(report *AuditReport) string {
	if report == nil {
		return "No disagreement analysis available."
	}

	if len(report.Conflicts) == 0 {
		return "No significant disagreements detected."
	}

	summary := fmt.Sprintf("Detected %d areas of disagreement:\n", len(report.Conflicts))
	for i, c := range report.Conflicts {
		summary += fmt.Sprintf("%d. %s (severity: %s)\n", i+1, c.Topic, c.Severity)
	}

	if len(report.ResolutionSuggestions) > 0 {
		summary += "\nSuggested resolutions:\n"
		for _, s := range report.ResolutionSuggestions {
			summary += fmt.Sprintf("- %s\n", s)
		}
	}

	return summary
}

// synthesisSchemaJSON returns the expected output schema for synthesizer agents.
func synthesisSchemaJSON() string {
	sample := SynthesisResult{
		Summary: "A unified thesis synthesizing key insights from all reasoning modes.",
		Findings: []Finding{
			{
				Finding:         "Key finding supported by multiple modes",
				Impact:          ImpactHigh,
				Confidence:      0.85,
				EvidencePointer: "file.go:42",
				Reasoning:       "Supported by modes: deductive, systems-thinking",
			},
		},
		Risks: []Risk{
			{
				Risk:       "Primary risk identified across modes",
				Impact:     ImpactHigh,
				Likelihood: 0.8,
				Mitigation: "Suggested mitigation approach",
			},
		},
		Recommendations: []Recommendation{
			{
				Recommendation: "Top recommendation based on synthesis",
				Priority:       ImpactHigh,
				Rationale:      "Why this is the top priority",
			},
		},
		Confidence:  0.8,
		GeneratedAt: time.Now().UTC(),
	}

	data, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// SynthesisEngine wraps the full synthesis pipeline.
type SynthesisEngine struct {
	Collector   *OutputCollector
	Synthesizer *Synthesizer
	Auditor     *DisagreementAuditor
}

// NewSynthesisEngine creates a complete synthesis pipeline.
func NewSynthesisEngine(cfg SynthesisConfig) (*SynthesisEngine, error) {
	synth, err := NewSynthesizer(cfg)
	if err != nil {
		return nil, err
	}

	return &SynthesisEngine{
		Collector:   NewOutputCollector(DefaultOutputCollectorConfig()),
		Synthesizer: synth,
	}, nil
}

// Process runs the full synthesis pipeline on collected outputs.
func (e *SynthesisEngine) Process(question string, pack *ContextPack) (*SynthesisResult, *AuditReport, error) {
	if e == nil {
		return nil, nil, fmt.Errorf("engine is nil")
	}

	// Build synthesis input
	input, err := e.Collector.BuildSynthesisInput(question, pack, e.Synthesizer.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("build synthesis input: %w", err)
	}

	// Run synthesis
	result, err := e.Synthesizer.Synthesize(input)
	if err != nil {
		return nil, input.AuditReport, fmt.Errorf("synthesis failed: %w", err)
	}

	return result, input.AuditReport, nil
}

// AddOutput adds an output to the engine's collector.
func (e *SynthesisEngine) AddOutput(output ModeOutput) error {
	if e == nil || e.Collector == nil {
		return fmt.Errorf("engine not initialized")
	}
	return e.Collector.Add(output)
}

// ParseSynthesisOutput parses raw agent output into a SynthesisResult.
// Supports both YAML and JSON formats. Extracts the output from code blocks
// if present (```yaml or ```json).
func ParseSynthesisOutput(raw string) (*SynthesisResult, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty synthesis output")
	}

	// Try to extract content from code blocks
	content := extractSynthesisContent(raw)

	// Try JSON first (more strict)
	var result SynthesisResult
	if err := json.Unmarshal([]byte(content), &result); err == nil {
		result.RawOutput = raw
		result.GeneratedAt = time.Now().UTC()
		return &result, nil
	}

	// Try YAML
	if err := yaml.Unmarshal([]byte(content), &result); err == nil {
		result.RawOutput = raw
		result.GeneratedAt = time.Now().UTC()
		return &result, nil
	}

	return nil, fmt.Errorf("failed to parse synthesis output as JSON or YAML")
}

// extractSynthesisContent extracts structured content from agent output.
// Handles code blocks (```yaml, ```json) and bare structured content.
func extractSynthesisContent(raw string) string {
	// Look for YAML or JSON code blocks
	for _, lang := range []string{"yaml", "json"} {
		startMarker := "```" + lang
		endMarker := "```"

		startIdx := strings.Index(strings.ToLower(raw), startMarker)
		if startIdx == -1 {
			continue
		}

		contentStart := startIdx + len(startMarker)
		// Skip any newline after the marker
		if contentStart < len(raw) && raw[contentStart] == '\n' {
			contentStart++
		}

		remaining := raw[contentStart:]
		endIdx := strings.Index(remaining, endMarker)
		if endIdx == -1 {
			// No closing marker, take the rest
			return remaining
		}

		return remaining[:endIdx]
	}

	// No code blocks found, try to find structured content
	// Look for "summary:" which should be the first field
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "summary:") || strings.HasPrefix(trimmed, "\"summary\"") {
			return strings.Join(lines[i:], "\n")
		}
	}

	// Return as-is
	return raw
}

// ValidateSynthesisResult validates a parsed SynthesisResult.
// Returns validation errors but does not fail on them.
func ValidateSynthesisResult(result *SynthesisResult) []ValidationError {
	var errs []ValidationError

	if result.Summary == "" {
		errs = append(errs, ValidationError{
			Field:   "summary",
			Message: "required field is missing",
		})
	}

	// Validate confidence range
	if result.Confidence < 0 || result.Confidence > 1 {
		errs = append(errs, ValidationError{
			Field:   "confidence",
			Message: "must be between 0.0 and 1.0",
			Value:   float64(result.Confidence),
		})
	}

	// Validate findings
	for i, f := range result.Findings {
		prefix := fmt.Sprintf("findings[%d]", i)

		if f.Finding == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".finding",
				Message: "required field is missing",
			})
		}

		if !f.Impact.IsValid() && f.Impact != "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".impact",
				Message: "must be one of: high, medium, low",
				Value:   string(f.Impact),
			})
		}

		if f.Confidence < 0 || f.Confidence > 1 {
			errs = append(errs, ValidationError{
				Field:   prefix + ".confidence",
				Message: "must be between 0.0 and 1.0",
				Value:   float64(f.Confidence),
			})
		}
	}

	// Validate risks
	for i, r := range result.Risks {
		prefix := fmt.Sprintf("risks[%d]", i)

		if r.Risk == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".risk",
				Message: "required field is missing",
			})
		}
	}

	// Validate recommendations
	for i, r := range result.Recommendations {
		prefix := fmt.Sprintf("recommendations[%d]", i)

		if r.Recommendation == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".recommendation",
				Message: "required field is missing",
			})
		}
	}

	return errs
}

// ParseAndValidateSynthesisOutput combines parsing and validation.
func ParseAndValidateSynthesisOutput(raw string) (*SynthesisResult, []ValidationError, error) {
	result, err := ParseSynthesisOutput(raw)
	if err != nil {
		return nil, nil, err
	}
	errs := ValidateSynthesisResult(result)
	return result, errs, nil
}
