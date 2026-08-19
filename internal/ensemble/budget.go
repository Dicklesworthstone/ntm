package ensemble

import (
	"encoding/json"
	"strings"

	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

// EstimateOutputTokens provides a thin wrapper around internal/tokens for
// ensemble outputs. Raw agent output is usually Markdown-like text.
func EstimateOutputTokens(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	return tokenpkg.EstimateTokensWithLanguageHint(raw, tokenpkg.ContentMarkdown)
}

// EstimateModeOutputTokens estimates the token count for a ModeOutput.
// If RawOutput is present, it is used directly; otherwise we fall back
// to a JSON-encoded representation (or a structured text fallback).
func EstimateModeOutputTokens(output *ModeOutput) int {
	if output == nil {
		return 0
	}
	if strings.TrimSpace(output.RawOutput) != "" {
		return EstimateOutputTokens(output.RawOutput)
	}

	blob, err := json.Marshal(output)
	if err != nil {
		return tokenpkg.EstimateTokensWithLanguageHint(fallbackModeOutputText(output), tokenpkg.ContentMarkdown)
	}
	return tokenpkg.EstimateTokensWithLanguageHint(string(blob), tokenpkg.ContentJSON)
}

// EstimateModeOutputsTokens sums token estimates across multiple ModeOutput values.
func EstimateModeOutputsTokens(outputs []ModeOutput) int {
	total := 0
	for i := range outputs {
		total += EstimateModeOutputTokens(&outputs[i])
	}
	return total
}

func fallbackModeOutputText(output *ModeOutput) string {
	if output == nil {
		return ""
	}

	var b strings.Builder
	if output.ModeID != "" {
		b.WriteString("Mode: ")
		b.WriteString(output.ModeID)
		b.WriteString("\n")
	}
	if output.Thesis != "" {
		b.WriteString("Thesis: ")
		b.WriteString(output.Thesis)
		b.WriteString("\n")
	}
	if len(output.TopFindings) > 0 {
		b.WriteString("Findings:\n")
		for _, f := range output.TopFindings {
			b.WriteString("- ")
			b.WriteString(f.Finding)
			if f.Reasoning != "" {
				b.WriteString(" (")
				b.WriteString(f.Reasoning)
				b.WriteString(")")
			}
			b.WriteString("\n")
		}
	}
	if len(output.Risks) > 0 {
		b.WriteString("Risks:\n")
		for _, r := range output.Risks {
			b.WriteString("- ")
			b.WriteString(r.Risk)
			if r.Mitigation != "" {
				b.WriteString(" | mitigation: ")
				b.WriteString(r.Mitigation)
			}
			b.WriteString("\n")
		}
	}
	if len(output.Recommendations) > 0 {
		b.WriteString("Recommendations:\n")
		for _, rec := range output.Recommendations {
			b.WriteString("- ")
			b.WriteString(rec.Recommendation)
			b.WriteString("\n")
		}
	}
	if len(output.QuestionsForUser) > 0 {
		b.WriteString("Questions:\n")
		for _, q := range output.QuestionsForUser {
			b.WriteString("- ")
			b.WriteString(q.Question)
			b.WriteString("\n")
		}
	}
	if len(output.FailureModesToWatch) > 0 {
		b.WriteString("Failure modes:\n")
		for _, f := range output.FailureModesToWatch {
			b.WriteString("- ")
			b.WriteString(f.Mode)
			b.WriteString(": ")
			b.WriteString(f.Description)
			b.WriteString("\n")
		}
	}
	return b.String()
}
