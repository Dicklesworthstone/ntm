package ensemble

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// OutputFormat specifies the synthesis output format.
type OutputFormat string

const (
	FormatMarkdown OutputFormat = "markdown"
	FormatJSON     OutputFormat = "json"
	FormatYAML     OutputFormat = "yaml"
)

// SynthesisFormatter formats synthesis results for output.
type SynthesisFormatter struct {
	Format               OutputFormat
	IncludeRaw           bool
	IncludeAudit         bool
	IncludeExplanation   bool
	IncludeContributions bool
	Verbose              bool
}

// NewSynthesisFormatter creates a formatter with the given format.
func NewSynthesisFormatter(format OutputFormat) *SynthesisFormatter {
	return &SynthesisFormatter{
		Format:       format,
		IncludeRaw:   false,
		IncludeAudit: true,
		Verbose:      false,
	}
}

// FormatResult formats a synthesis result.
func (f *SynthesisFormatter) FormatResult(w io.Writer, result *SynthesisResult, audit *AuditReport) error {
	if f == nil {
		return fmt.Errorf("formatter is nil")
	}
	if w == nil {
		return fmt.Errorf("writer is nil")
	}

	switch f.Format {
	case FormatJSON:
		return f.formatJSON(w, result, audit)
	case FormatYAML:
		return f.formatYAML(w, result, audit)
	case FormatMarkdown:
		return f.formatMarkdown(w, result, audit)
	default:
		return f.formatMarkdown(w, result, audit)
	}
}

// formatJSON outputs the result as JSON.
func (f *SynthesisFormatter) formatJSON(w io.Writer, result *SynthesisResult, audit *AuditReport) error {
	output := struct {
		Synthesis *SynthesisResult `json:"synthesis"`
		Audit     *AuditReport     `json:"audit,omitempty"`
	}{
		Synthesis: result,
	}

	if f.IncludeAudit && audit != nil {
		output.Audit = audit
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// formatYAML outputs the result as YAML.
func (f *SynthesisFormatter) formatYAML(w io.Writer, result *SynthesisResult, audit *AuditReport) error {
	output := struct {
		Synthesis *SynthesisResult `yaml:"synthesis"`
		Audit     *AuditReport     `yaml:"audit,omitempty"`
	}{
		Synthesis: result,
	}

	if f.IncludeAudit && audit != nil {
		output.Audit = audit
	}

	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	return encoder.Encode(output)
}

// formatMarkdown outputs the result as formatted Markdown.
func (f *SynthesisFormatter) formatMarkdown(w io.Writer, result *SynthesisResult, audit *AuditReport) error {
	if result == nil {
		return fmt.Errorf("result is nil")
	}

	var b strings.Builder

	// Header
	b.WriteString("# Ensemble Synthesis Report\n\n")
	b.WriteString(fmt.Sprintf("*Generated: %s*\n\n", result.GeneratedAt.Format(time.RFC3339)))
	if result.SynthesizedBy != "" {
		b.WriteString(fmt.Sprintf("*Synthesis path: %s*\n\n", result.SynthesizedBy))
	}
	if result.FallbackReason != "" {
		b.WriteString(fmt.Sprintf("> **Note:** %s\n\n", result.FallbackReason))
	}

	// Executive Summary
	b.WriteString("## Executive Summary\n\n")
	if result.Summary != "" {
		b.WriteString(result.Summary)
		b.WriteString("\n\n")
	}
	b.WriteString(fmt.Sprintf("**Overall Confidence:** %.0f%%\n\n", float64(result.Confidence)*100))

	// Key Findings
	if len(result.Findings) > 0 {
		b.WriteString("## Key Findings\n\n")
		for i, finding := range result.Findings {
			b.WriteString(fmt.Sprintf("### %d. %s\n\n", i+1, truncate(finding.Finding, 80)))
			b.WriteString(fmt.Sprintf("- **Impact:** %s\n", finding.Impact))
			b.WriteString(fmt.Sprintf("- **Confidence:** %.0f%%\n", float64(finding.Confidence)*100))
			if finding.EvidencePointer != "" {
				b.WriteString(fmt.Sprintf("- **Evidence:** `%s`\n", finding.EvidencePointer))
			}
			if f.Verbose && finding.Reasoning != "" {
				b.WriteString(fmt.Sprintf("- **Reasoning:** %s\n", finding.Reasoning))
			}
			b.WriteString("\n")
		}
	}

	// Risks
	if len(result.Risks) > 0 {
		b.WriteString("## Identified Risks\n\n")
		b.WriteString("| Risk | Impact | Likelihood | Mitigation |\n")
		b.WriteString("|------|--------|------------|------------|\n")
		for _, risk := range result.Risks {
			mitigation := truncate(risk.Mitigation, 50)
			if mitigation == "" {
				mitigation = "-"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %.0f%% | %s |\n",
				truncate(risk.Risk, 40),
				risk.Impact,
				float64(risk.Likelihood)*100,
				mitigation,
			))
		}
		b.WriteString("\n")
	}

	// Recommendations
	if len(result.Recommendations) > 0 {
		b.WriteString("## Recommendations\n\n")
		for i, rec := range result.Recommendations {
			priorityEmoji := priorityEmoji(rec.Priority)
			b.WriteString(fmt.Sprintf("%d. %s **[%s]** %s\n", i+1, priorityEmoji, rec.Priority, rec.Recommendation))
			if f.Verbose && rec.Rationale != "" {
				b.WriteString(fmt.Sprintf("   *Rationale: %s*\n", rec.Rationale))
			}
		}
		b.WriteString("\n")
	}

	// Questions for User
	if len(result.QuestionsForUser) > 0 {
		b.WriteString("## Questions for User\n\n")
		for i, q := range result.QuestionsForUser {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, q.Question))
			if q.Context != "" {
				b.WriteString(fmt.Sprintf("   *Context: %s*\n", q.Context))
			}
		}
		b.WriteString("\n")
	}

	// Disagreement Analysis
	if f.IncludeAudit && audit != nil && len(audit.Conflicts) > 0 {
		b.WriteString("## Mode Disagreements\n\n")
		b.WriteString(fmt.Sprintf("*%d areas of disagreement identified*\n\n", len(audit.Conflicts)))

		for _, conflict := range audit.Conflicts {
			b.WriteString(fmt.Sprintf("### %s (%s)\n\n", conflict.Topic, conflict.Severity))
			for _, pos := range conflict.Positions {
				b.WriteString(fmt.Sprintf("- **%s** (%.0f%% confidence): %s\n",
					pos.ModeID,
					pos.Confidence*100,
					truncate(pos.Position, 100),
				))
			}
			if conflict.ResolutionPath != "" {
				b.WriteString(fmt.Sprintf("\n*Resolution path: %s*\n", conflict.ResolutionPath))
			}
			b.WriteString("\n")
		}

		if len(audit.ResolutionSuggestions) > 0 {
			b.WriteString("### Resolution Suggestions\n\n")
			for _, s := range audit.ResolutionSuggestions {
				b.WriteString(fmt.Sprintf("- %s\n", s))
			}
			b.WriteString("\n")
		}
	}

	// Explanation Layer
	if f.IncludeExplanation && result.Explanation != nil {
		b.WriteString("## Synthesis Explanation\n\n")

		if result.Explanation.StrategyRationale != "" {
			b.WriteString("### Strategy Rationale\n\n")
			b.WriteString(result.Explanation.StrategyRationale)
			b.WriteString("\n\n")
		}

		if len(result.Explanation.ModeWeights) > 0 {
			b.WriteString("### Mode Weights\n\n")
			b.WriteString("| Mode | Weight |\n")
			b.WriteString("|------|--------|\n")
			for mode, weight := range result.Explanation.ModeWeights {
				b.WriteString(fmt.Sprintf("| %s | %.2f |\n", mode, weight))
			}
			b.WriteString("\n")
		}

		if len(result.Explanation.Conclusions) > 0 {
			b.WriteString("### Conclusion Reasoning\n\n")
			for i, c := range result.Explanation.Conclusions {
				b.WriteString(fmt.Sprintf("#### %d. [%s] %s\n\n", i+1, c.Type, truncate(c.Text, 60)))
				b.WriteString(fmt.Sprintf("- **Sources:** %s\n", strings.Join(c.SourceModes, ", ")))
				b.WriteString(fmt.Sprintf("- **Confidence:** %.0f%%\n", float64(c.Confidence)*100))
				if c.ConfidenceBasis != "" {
					b.WriteString(fmt.Sprintf("- **Basis:** %s\n", c.ConfidenceBasis))
				}
				if c.Reasoning != "" {
					b.WriteString(fmt.Sprintf("- **Reasoning:** %s\n", c.Reasoning))
				}
				if len(c.SourceFindings) > 0 {
					b.WriteString(fmt.Sprintf("- **Source findings:** %s\n", strings.Join(c.SourceFindings, ", ")))
				}
				b.WriteString("\n")
			}
		}

		if len(result.Explanation.ConflictsResolved) > 0 {
			b.WriteString("### Conflict Resolutions\n\n")
			for i, cr := range result.Explanation.ConflictsResolved {
				b.WriteString(fmt.Sprintf("#### %d. %s\n\n", i+1, cr.Topic))
				b.WriteString(fmt.Sprintf("- **Method:** %s\n", cr.Method))
				b.WriteString(fmt.Sprintf("- **Resolution:** %s\n", cr.Resolution))
				b.WriteString("- **Positions:**\n")
				for _, p := range cr.Positions {
					b.WriteString(fmt.Sprintf("  - %s: %s (strength: %.2f)\n", p.ModeID, truncate(p.Position, 50), p.Strength))
				}
				b.WriteString("\n")
			}
		}
	}

	// Mode Contributions
	if f.IncludeContributions && result.Contributions != nil && len(result.Contributions.Scores) > 0 {
		b.WriteString("## Mode Contributions\n\n")

		b.WriteString(fmt.Sprintf("*Total findings: %d (deduped: %d), Overlap: %.0f%%, Diversity: %.2f*\n\n",
			result.Contributions.TotalFindings,
			result.Contributions.DedupedFindings,
			result.Contributions.OverlapRate*100,
			result.Contributions.DiversityScore,
		))

		b.WriteString("| Rank | Mode | Score | Findings | Unique | Citations | Risks | Recs |\n")
		b.WriteString("|------|------|-------|----------|--------|-----------|-------|------|\n")

		for _, score := range result.Contributions.Scores {
			name := score.ModeName
			if name == "" {
				name = score.ModeID
			}
			b.WriteString(fmt.Sprintf("| #%d | %s | %.1f | %d/%d | %d | %d | %d | %d |\n",
				score.Rank,
				truncate(name, 20),
				score.Score,
				score.FindingsCount,
				score.OriginalFindings,
				score.UniqueInsights,
				score.CitationCount,
				score.RisksCount,
				score.RecommendationsCount,
			))
		}
		b.WriteString("\n")

		// Show highlights for top contributors
		if f.Verbose {
			for _, score := range result.Contributions.Scores {
				if len(score.HighlightFindings) > 0 {
					name := score.ModeName
					if name == "" {
						name = score.ModeID
					}
					b.WriteString(fmt.Sprintf("**%s highlights:**\n", name))
					for _, h := range score.HighlightFindings {
						b.WriteString(fmt.Sprintf("- %s\n", h))
					}
					b.WriteString("\n")
				}
			}
		}
	}

	// Footer
	b.WriteString("---\n\n")
	b.WriteString("*Report generated by NTM Ensemble Synthesis*\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// Helper functions

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func priorityEmoji(p ImpactLevel) string {
	switch p {
	case ImpactCritical:
		return "🔴"
	case ImpactHigh:
		return "🟠"
	case ImpactMedium:
		return "🟡"
	case ImpactLow:
		return "🟢"
	default:
		return "⚪"
	}
}
