package ensemble

import (
	"encoding/json"
	"fmt"
	"time"
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

	// Agent-based strategies - for now, fall back to mechanical
	// Full agent synthesis would inject a synthesizer agent prompt
	// and wait for its response. This is tracked separately.
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
		ModeID       string         `json:"mode_id"`
		Thesis       string         `json:"thesis"`
		TopFindings  []Finding      `json:"top_findings"`
		Risks        []Risk         `json:"risks,omitempty"`
		Confidence   Confidence     `json:"confidence"`
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
