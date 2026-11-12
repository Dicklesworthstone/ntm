package ensemble

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

// SynthesisConfigFromTOML applies non-zero TOML [ensemble.synthesis] values onto base.
func SynthesisConfigFromTOML(base SynthesisConfig, ensCfg config.EnsembleSynthesisConfig) SynthesisConfig {
	if s := strings.TrimSpace(ensCfg.Strategy); s != "" {
		base.Strategy = SynthesisStrategy(s)
	}
	if ensCfg.MinConfidence > 0 {
		base.MinConfidence = Confidence(ensCfg.MinConfidence)
	}
	if ensCfg.MaxFindings > 0 {
		base.MaxFindings = ensCfg.MaxFindings
	}
	base.IncludeRawOutputs = ensCfg.IncludeRawOutputs
	if cr := strings.TrimSpace(ensCfg.ConflictResolution); cr != "" {
		base.ConflictResolution = cr
	}
	return base
}

// BudgetConfigFromTOML applies non-zero TOML [ensemble.budget] values onto base.
func BudgetConfigFromTOML(base BudgetConfig, ensCfg config.EnsembleBudgetConfig) BudgetConfig {
	if ensCfg.Total > 0 {
		base.MaxTotalTokens = ensCfg.Total
	}
	if ensCfg.PerAgent > 0 {
		base.MaxTokensPerMode = ensCfg.PerAgent
	}
	if ensCfg.Synthesis > 0 {
		base.SynthesisReserveTokens = ensCfg.Synthesis
	}
	if ensCfg.ContextPack > 0 {
		base.ContextReserveTokens = ensCfg.ContextPack
	}
	return base
}

// ApplyTOMLConfig seeds an EnsembleConfig from parsed [ensemble.*] TOML configuration.
func ApplyTOMLConfig(target *EnsembleConfig, ensCfg config.EnsembleConfig) {
	if target == nil {
		return
	}

	if target.Synthesis.Strategy == "" && strings.TrimSpace(ensCfg.Synthesis.Strategy) != "" {
		target.Synthesis.Strategy = SynthesisStrategy(strings.TrimSpace(ensCfg.Synthesis.Strategy))
	}
	if ensCfg.Synthesis.MinConfidence > 0 {
		target.Synthesis.MinConfidence = Confidence(ensCfg.Synthesis.MinConfidence)
	}
	if ensCfg.Synthesis.MaxFindings > 0 {
		target.Synthesis.MaxFindings = ensCfg.Synthesis.MaxFindings
	}
	if ensCfg.Synthesis.IncludeRawOutputs {
		target.Synthesis.IncludeRawOutputs = true
	}
	if cr := strings.TrimSpace(ensCfg.Synthesis.ConflictResolution); cr != "" {
		target.Synthesis.ConflictResolution = cr
	}

	if ensCfg.Budget.Synthesis > 0 {
		target.Budget.SynthesisReserveTokens = ensCfg.Budget.Synthesis
		target.Synthesis.MaxSynthesisTokens = ensCfg.Budget.Synthesis
	}
	if ensCfg.Budget.ContextPack > 0 {
		target.Budget.ContextReserveTokens = ensCfg.Budget.ContextPack
	}

	target.Cache.Enabled = ensCfg.Cache.Enabled
	if ensCfg.Cache.TTLMinutes > 0 {
		target.Cache.TTL = time.Duration(ensCfg.Cache.TTLMinutes) * time.Minute
	}
	if strings.TrimSpace(ensCfg.Cache.CacheDir) != "" {
		target.Cache.CacheDir = config.ExpandHome(strings.TrimSpace(ensCfg.Cache.CacheDir))
	}
	if ensCfg.Cache.MaxEntries > 0 {
		target.Cache.MaxEntries = ensCfg.Cache.MaxEntries
	}
	target.Cache.ShareAcrossModes = ensCfg.Cache.ShareAcrossModes
	if ensCfg.Cache.CacheDir != "" || ensCfg.Cache.TTLMinutes > 0 || ensCfg.Cache.MaxEntries > 0 || !ensCfg.Cache.Enabled {
		target.CacheOverride = true
	}

	target.EarlyStop = EarlyStopConfigFromAppConfig(ensCfg.EarlyStop)
}

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
