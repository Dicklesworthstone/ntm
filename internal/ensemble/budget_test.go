package ensemble

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestFallbackModeOutputText(t *testing.T) {
	output := &ModeOutput{
		ModeID: "deductive",
		Thesis: "thesis",
		TopFindings: []Finding{
			{Finding: "finding one", Reasoning: "because"},
		},
		Risks: []Risk{
			{Risk: "risk one", Impact: ImpactLow, Likelihood: 0.2},
		},
		Recommendations: []Recommendation{
			{Recommendation: "do thing", Priority: ImpactMedium},
		},
		QuestionsForUser: []Question{
			{Question: "clarify", Context: "need context"},
		},
	}

	text := fallbackModeOutputText(output)
	if text == "" {
		t.Fatal("expected fallback output text")
	}
	if !strings.Contains(text, "Mode: deductive") {
		t.Fatalf("expected mode id in fallback text, got %q", text)
	}
	if !strings.Contains(text, "Thesis: thesis") {
		t.Fatalf("expected thesis in fallback text, got %q", text)
	}
	if !strings.Contains(text, "finding one") {
		t.Fatalf("expected finding in fallback text, got %q", text)
	}
	if !strings.Contains(text, "risk one") {
		t.Fatalf("expected risk in fallback text, got %q", text)
	}
	if !strings.Contains(text, "do thing") {
		t.Fatalf("expected recommendation in fallback text, got %q", text)
	}
	if !strings.Contains(text, "clarify") {
		t.Fatalf("expected question in fallback text, got %q", text)
	}
}

func TestApplyTOMLConfig_SeedsAllSixSynthesisAndBudgetKeys(t *testing.T) {
	src := config.Default().Ensemble
	src.Synthesis.Strategy = "dialectical"
	src.Synthesis.MinConfidence = 0.72
	src.Synthesis.MaxFindings = 4
	src.Synthesis.IncludeRawOutputs = true
	src.Synthesis.ConflictResolution = "prefer_high_confidence"
	src.Budget.PerAgent = 2500
	src.Budget.Total = 18000
	src.Budget.Synthesis = 1200
	src.Budget.ContextPack = 650

	baseSynth := DefaultSynthesisConfig()
	baseSynth.Strategy = ""
	dst := &EnsembleConfig{
		Synthesis: baseSynth,
		Budget:    DefaultBudgetConfig(),
		Cache:     DefaultCacheConfig(),
	}
	ApplyTOMLConfig(dst, src)

	if dst.Synthesis.Strategy != StrategyDialectical {
		t.Fatalf("Strategy = %q, want %q", dst.Synthesis.Strategy, StrategyDialectical)
	}
	if dst.Synthesis.MinConfidence != 0.72 {
		t.Fatalf("MinConfidence = %v, want 0.72", dst.Synthesis.MinConfidence)
	}
	if dst.Synthesis.MaxFindings != 4 {
		t.Fatalf("MaxFindings = %d, want 4", dst.Synthesis.MaxFindings)
	}
	if !dst.Synthesis.IncludeRawOutputs {
		t.Fatalf("IncludeRawOutputs = false, want true")
	}
	if dst.Synthesis.ConflictResolution != "prefer_high_confidence" {
		t.Fatalf("ConflictResolution = %q, want prefer_high_confidence", dst.Synthesis.ConflictResolution)
	}
	if dst.Budget.SynthesisReserveTokens != 1200 || dst.Synthesis.MaxSynthesisTokens != 1200 {
		t.Fatalf("SynthesisReserveTokens=%d MaxSynthesisTokens=%d, want 1200", dst.Budget.SynthesisReserveTokens, dst.Synthesis.MaxSynthesisTokens)
	}
	if dst.Budget.ContextReserveTokens != 650 {
		t.Fatalf("ContextReserveTokens = %d, want 650", dst.Budget.ContextReserveTokens)
	}

	// Verify CapContextPackTokens enforces ContextReserveTokens.
	pack := &ContextPack{
		GeneratedAt: time.Now().UTC(),
		ProjectBrief: &ProjectBrief{
			Name:        "demo",
			Description: strings.Repeat("architecture summary detail ", 80),
			RecentActivity: []CommitSummary{
				{Hash: "1111111", Summary: strings.Repeat("commit summary one ", 20)},
				{Hash: "2222222", Summary: strings.Repeat("commit summary two ", 20)},
			},
		},
		UserContext: &UserContext{
			ProblemStatement: strings.Repeat("problem framing detail ", 50),
		},
	}
	capped := CapContextPackTokens(pack, 80)
	if capped.TokenEstimate <= 0 || capped.TokenEstimate > 80 {
		t.Fatalf("CapContextPackTokens TokenEstimate = %d, want in (0, 80]", capped.TokenEstimate)
	}

	// Verify Synthesizer honors MinConfidence, MaxFindings, IncludeRawOutputs, ConflictResolution, and MaxSynthesisTokens.
	synthCfg := dst.Synthesis
	synthCfg.MinConfidence = 0.75
	synthCfg.MaxFindings = 1
	synthCfg.IncludeRawOutputs = true
	synthCfg.ConflictResolution = "prefer_high_confidence"
	synthCfg.MaxSynthesisTokens = 90

	synth, err := NewSynthesizer(synthCfg)
	if err != nil {
		t.Fatalf("NewSynthesizer() error = %v", err)
	}

	now := time.Now().UTC()
	input := &SynthesisInput{
		OriginalQuestion: "Should we enable strict validation?",
		Config:           synthCfg,
		Outputs: []ModeOutput{
			{
				ModeID: "deductive",
				Thesis: "We must strictly enable synchronous validation for all writes.",
				TopFindings: []Finding{
					{Finding: "Top high confidence finding", Impact: ImpactHigh, Confidence: 0.95, Reasoning: strings.Repeat("proof ", 12)},
					{Finding: "Filtered low confidence finding", Impact: ImpactLow, Confidence: 0.40},
				},
				Confidence:  0.95,
				RawOutput:   "RAW_OUT_1",
				GeneratedAt: now,
			},
			{
				ModeID: "adversarial-review",
				Thesis: "We must never enable synchronous validation; reject blocking writes.",
				TopFindings: []Finding{
					{Finding: "Asynchronous queue batching avoids write lock contention", Impact: ImpactHigh, Confidence: 0.80, Reasoning: strings.Repeat("critique ", 12)},
				},
				Confidence:  0.60,
				RawOutput:   "RAW_OUT_2",
				GeneratedAt: now,
			},
		},
	}
	auditReport, err := NewDisagreementAuditor(input.Outputs, nil).Audit()
	if err != nil {
		t.Fatalf("Audit() error = %v", err)
	}
	input.AuditReport = auditReport

	res, err := synth.Synthesize(input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Finding, "Top high confidence finding") {
		t.Fatalf("Findings = %+v, want [Top high confidence finding]", res.Findings)
	}
	if len(res.RawOutputs) != 2 || res.RawOutputs["deductive"] != "RAW_OUT_1" {
		t.Fatalf("RawOutputs = %+v, want 2 entries", res.RawOutputs)
	}
	if res.TokensUsed > 90 {
		t.Fatalf("TokensUsed = %d, want <= 90", res.TokensUsed)
	}
	if input.AuditReport == nil || len(input.AuditReport.Conflicts) == 0 {
		t.Fatalf("AuditReport.Conflicts = %+v, want at least 1 conflict", input.AuditReport)
	}
	for _, c := range input.AuditReport.Conflicts {
		if !strings.Contains(c.ResolutionPath, "Resolved by highest confidence") {
			t.Fatalf("Conflict ResolutionPath = %q, want highest-confidence resolution note", c.ResolutionPath)
		}
	}
}
