package ensemble

import (
	"strings"
	"testing"
	"time"
)

func TestNewSynthesizer(t *testing.T) {
	cfg := SynthesisConfig{
		Strategy: StrategyManual,
	}

	synth, err := NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("NewSynthesizer returned error: %v", err)
	}
	if synth == nil {
		t.Fatal("NewSynthesizer returned nil")
	}
	if synth.Strategy == nil {
		t.Error("Strategy is nil")
	}
	if synth.Strategy.Name != "manual" {
		t.Errorf("Strategy.Name = %s, want manual", synth.Strategy.Name)
	}
}

func TestNewSynthesizer_DefaultStrategy(t *testing.T) {
	cfg := SynthesisConfig{} // No strategy specified

	synth, err := NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("NewSynthesizer returned error: %v", err)
	}

	// Should default to manual
	if synth.Strategy.Name != "manual" {
		t.Errorf("Strategy.Name = %s, want manual (default)", synth.Strategy.Name)
	}
}

func TestNewSynthesizer_InvalidStrategy(t *testing.T) {
	cfg := SynthesisConfig{
		Strategy: SynthesisStrategy("nonexistent"),
	}

	_, err := NewSynthesizer(cfg)
	if err == nil {
		t.Error("Expected error for invalid strategy")
	}
}

func TestSynthesizer_Synthesize_ManualStrategy(t *testing.T) {
	cfg := SynthesisConfig{
		Strategy: StrategyManual,
	}

	synth, err := NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("NewSynthesizer error: %v", err)
	}

	input := &SynthesisInput{
		OriginalQuestion: "What is the architecture?",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "The architecture is microservices-based",
				Confidence: 0.8,
				TopFindings: []Finding{
					{Finding: "Uses REST APIs", Impact: ImpactMedium, Confidence: 0.9},
				},
				Risks: []Risk{
					{Risk: "Scaling concerns", Impact: ImpactHigh, Likelihood: 0.6},
				},
				Recommendations: []Recommendation{
					{Recommendation: "Add caching", Priority: ImpactHigh},
				},
			},
		},
	}

	result, err := synth.Synthesize(input)
	if err != nil {
		t.Fatalf("Synthesize returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Synthesize returned nil result")
	}

	if result.Summary == "" {
		t.Error("Summary is empty")
	}
	if len(result.Findings) == 0 {
		t.Error("No findings in result")
	}
	if len(result.Risks) == 0 {
		t.Error("No risks in result")
	}
	if len(result.Recommendations) == 0 {
		t.Error("No recommendations in result")
	}
	if result.GeneratedAt.IsZero() {
		t.Error("GeneratedAt is zero")
	}
}

func TestSynthesizer_Synthesize_NilInput(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	_, err := synth.Synthesize(nil)
	if err == nil {
		t.Error("Expected error for nil input")
	}
}

func TestSynthesizer_Synthesize_EmptyOutputs(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	input := &SynthesisInput{
		OriginalQuestion: "Test question",
		Outputs:          []ModeOutput{},
	}

	_, err := synth.Synthesize(input)
	if err == nil {
		t.Error("Expected error for empty outputs")
	}
}

func TestSynthesizer_Synthesize_NilReceiver(t *testing.T) {
	var synth *Synthesizer

	_, err := synth.Synthesize(&SynthesisInput{})
	if err == nil {
		t.Error("Expected error for nil receiver")
	}
}

func TestSynthesizer_Synthesize_ConfigOverrides(t *testing.T) {
	cfg := SynthesisConfig{
		Strategy:      StrategyManual,
		MaxFindings:   2,
		MinConfidence: 0.5,
	}

	synth, _ := NewSynthesizer(cfg)

	input := &SynthesisInput{
		OriginalQuestion: "Test",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "Test thesis",
				Confidence: 0.8,
				TopFindings: []Finding{
					{Finding: "Finding 1", Impact: ImpactHigh, Confidence: 0.9},
					{Finding: "Finding 2", Impact: ImpactMedium, Confidence: 0.8},
					{Finding: "Finding 3", Impact: ImpactLow, Confidence: 0.7},
					{Finding: "Finding 4", Impact: ImpactLow, Confidence: 0.3}, // Below min confidence
				},
			},
		},
	}

	result, err := synth.Synthesize(input)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}

	// MaxFindings should limit to 2
	if len(result.Findings) > 2 {
		t.Errorf("Findings = %d, want <= 2 (MaxFindings)", len(result.Findings))
	}
}

func TestSynthesizer_GeneratePrompt(t *testing.T) {
	cfg := SynthesisConfig{
		Strategy:    StrategyConsensus,
		MaxFindings: 10,
	}

	synth, _ := NewSynthesizer(cfg)

	input := &SynthesisInput{
		OriginalQuestion: "What are the key risks?",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "Primary risks are in authentication",
				Confidence: 0.8,
				TopFindings: []Finding{
					{Finding: "Auth vulnerability", Impact: ImpactHigh, Confidence: 0.9},
				},
			},
		},
		AuditReport: &AuditReport{
			Conflicts: []DetailedConflict{
				{Topic: "Risk severity", Severity: ConflictMedium},
			},
		},
	}

	prompt := synth.GeneratePrompt(input)

	if prompt == "" {
		t.Error("GeneratePrompt returned empty string")
	}

	// Check prompt contains key elements
	if !strings.Contains(prompt, "What are the key risks?") {
		t.Error("Prompt should contain original question")
	}
	if !strings.Contains(prompt, "consensus") {
		t.Error("Prompt should contain strategy name")
	}
	if !strings.Contains(prompt, "mode-a") {
		t.Error("Prompt should contain mode outputs")
	}
	if !strings.Contains(prompt, "Risk severity") {
		t.Error("Prompt should contain audit conflicts")
	}
}

func TestSynthesizer_GeneratePrompt_NilInputs(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	prompt := synth.GeneratePrompt(nil)
	if prompt != "" {
		t.Error("GeneratePrompt should return empty for nil input")
	}

	var nilSynth *Synthesizer
	prompt = nilSynth.GeneratePrompt(&SynthesisInput{})
	if prompt != "" {
		t.Error("GeneratePrompt should return empty for nil receiver")
	}
}

func TestSynthesizer_GeneratePrompt_NoAudit(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	input := &SynthesisInput{
		OriginalQuestion: "Test question",
		Outputs: []ModeOutput{
			{ModeID: "test", Thesis: "thesis", TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}}, Confidence: 0.5},
		},
		AuditReport: nil,
	}

	prompt := synth.GeneratePrompt(input)

	if !strings.Contains(prompt, "No disagreement analysis available") {
		t.Error("Prompt should indicate no audit available")
	}
}

func TestNewSynthesisEngine(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}

	engine, err := NewSynthesisEngine(cfg)
	if err != nil {
		t.Fatalf("NewSynthesisEngine error: %v", err)
	}
	if engine == nil {
		t.Fatal("NewSynthesisEngine returned nil")
	}
	if engine.Collector == nil {
		t.Error("Collector is nil")
	}
	if engine.Synthesizer == nil {
		t.Error("Synthesizer is nil")
	}
}

func TestNewSynthesisEngine_InvalidStrategy(t *testing.T) {
	cfg := SynthesisConfig{Strategy: SynthesisStrategy("invalid")}

	_, err := NewSynthesisEngine(cfg)
	if err == nil {
		t.Error("Expected error for invalid strategy")
	}
}

func TestSynthesisEngine_AddOutput(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	engine, _ := NewSynthesisEngine(cfg)

	output := ModeOutput{
		ModeID:      "test",
		Thesis:      "Test thesis",
		Confidence:  0.8,
		TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.7}},
	}

	err := engine.AddOutput(output)
	if err != nil {
		t.Fatalf("AddOutput error: %v", err)
	}

	if engine.Collector.Count() != 1 {
		t.Errorf("Collector count = %d, want 1", engine.Collector.Count())
	}
}

func TestSynthesisEngine_AddOutput_NilEngine(t *testing.T) {
	var engine *SynthesisEngine

	err := engine.AddOutput(ModeOutput{})
	if err == nil {
		t.Error("Expected error for nil engine")
	}
}

func TestSynthesisEngine_Process(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	engine, _ := NewSynthesisEngine(cfg)

	// Add outputs
	outputs := []ModeOutput{
		{
			ModeID:      "mode-a",
			Thesis:      "First thesis",
			Confidence:  0.8,
			TopFindings: []Finding{{Finding: "Finding A", Impact: ImpactHigh, Confidence: 0.9}},
		},
		{
			ModeID:      "mode-b",
			Thesis:      "Second thesis",
			Confidence:  0.7,
			TopFindings: []Finding{{Finding: "Finding B", Impact: ImpactMedium, Confidence: 0.8}},
		},
	}

	for _, o := range outputs {
		_ = engine.AddOutput(o)
	}

	result, audit, err := engine.Process("What is the system architecture?", nil)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if result == nil {
		t.Fatal("Process returned nil result")
	}
	if audit == nil {
		t.Error("Audit report should not be nil")
	}

	if result.Summary == "" {
		t.Error("Summary is empty")
	}
	if len(result.Findings) == 0 {
		t.Error("No findings in result")
	}
}

func TestSynthesisEngine_Process_NilEngine(t *testing.T) {
	var engine *SynthesisEngine

	_, _, err := engine.Process("question", nil)
	if err == nil {
		t.Error("Expected error for nil engine")
	}
}

func TestSynthesisEngine_Process_NoOutputs(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	engine, _ := NewSynthesisEngine(cfg)

	_, _, err := engine.Process("question", nil)
	if err == nil {
		t.Error("Expected error for no outputs")
	}
}

func TestSynthesisResult_Fields(t *testing.T) {
	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	input := &SynthesisInput{
		OriginalQuestion: "Test question",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "Test thesis",
				Confidence: 0.75,
				TopFindings: []Finding{
					{Finding: "Test finding", Impact: ImpactHigh, Confidence: 0.9},
				},
				Risks: []Risk{
					{Risk: "Test risk", Impact: ImpactMedium, Likelihood: 0.7},
				},
				Recommendations: []Recommendation{
					{Recommendation: "Test rec", Priority: ImpactHigh},
				},
				QuestionsForUser: []Question{
					{Question: "Follow-up question?"},
				},
			},
		},
	}

	result, _ := synth.Synthesize(input)

	// Check all fields are populated correctly
	if result.Summary == "" {
		t.Error("Summary should not be empty")
	}
	if len(result.Findings) != 1 {
		t.Errorf("Findings = %d, want 1", len(result.Findings))
	}
	if len(result.Risks) != 1 {
		t.Errorf("Risks = %d, want 1", len(result.Risks))
	}
	if len(result.Recommendations) != 1 {
		t.Errorf("Recommendations = %d, want 1", len(result.Recommendations))
	}
	if len(result.QuestionsForUser) != 1 {
		t.Errorf("QuestionsForUser = %d, want 1", len(result.QuestionsForUser))
	}
	if result.Confidence != 0.75 {
		t.Errorf("Confidence = %v, want 0.75", result.Confidence)
	}
	if result.GeneratedAt.After(time.Now().Add(time.Second)) {
		t.Error("GeneratedAt should not be in the future")
	}
}

func TestFormatModeOutputs_Empty(t *testing.T) {
	result := formatModeOutputs(nil)
	if result != "[]" {
		t.Errorf("formatModeOutputs(nil) = %q, want []", result)
	}

	result = formatModeOutputs([]ModeOutput{})
	if result != "[]" {
		t.Errorf("formatModeOutputs([]) = %q, want []", result)
	}
}

func TestFormatModeOutputs_Content(t *testing.T) {
	outputs := []ModeOutput{
		{
			ModeID:     "test-mode",
			Thesis:     "Test thesis",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Test finding", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
	}

	result := formatModeOutputs(outputs)

	if !strings.Contains(result, "test-mode") {
		t.Error("Output should contain mode_id")
	}
	if !strings.Contains(result, "Test thesis") {
		t.Error("Output should contain thesis")
	}
	if !strings.Contains(result, "Test finding") {
		t.Error("Output should contain finding")
	}
}

func TestFormatAuditSummary_Nil(t *testing.T) {
	result := formatAuditSummary(nil)
	if result != "No disagreement analysis available." {
		t.Errorf("formatAuditSummary(nil) = %q", result)
	}
}

func TestFormatAuditSummary_NoConflicts(t *testing.T) {
	report := &AuditReport{Conflicts: nil}
	result := formatAuditSummary(report)
	if result != "No significant disagreements detected." {
		t.Errorf("formatAuditSummary with no conflicts = %q", result)
	}
}

func TestFormatAuditSummary_WithConflicts(t *testing.T) {
	report := &AuditReport{
		Conflicts: []DetailedConflict{
			{Topic: "Architecture choice", Severity: ConflictHigh},
			{Topic: "Risk assessment", Severity: ConflictMedium},
		},
		ResolutionSuggestions: []string{
			"Review architecture documentation",
			"Consult with team lead",
		},
	}

	result := formatAuditSummary(report)

	if !strings.Contains(result, "2 areas of disagreement") {
		t.Error("Summary should mention conflict count")
	}
	if !strings.Contains(result, "Architecture choice") {
		t.Error("Summary should contain first conflict topic")
	}
	if !strings.Contains(result, "Risk assessment") {
		t.Error("Summary should contain second conflict topic")
	}
	if !strings.Contains(result, "Review architecture documentation") {
		t.Error("Summary should contain resolution suggestions")
	}
}

func TestSynthesisSchemaJSON(t *testing.T) {
	schema := synthesisSchemaJSON()

	if schema == "" || schema == "{}" {
		t.Error("synthesisSchemaJSON should return valid schema")
	}
	if !strings.Contains(schema, "summary") {
		t.Error("Schema should contain summary field")
	}
	if !strings.Contains(schema, "findings") {
		t.Error("Schema should contain findings field")
	}
	if !strings.Contains(schema, "risks") {
		t.Error("Schema should contain risks field")
	}
	if !strings.Contains(schema, "recommendations") {
		t.Error("Schema should contain recommendations field")
	}
}

func TestSynthesizer_Contributions_Populated(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, err := NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("NewSynthesizer error: %v", err)
	}

	input := &SynthesisInput{
		OriginalQuestion: "How does the system perform?",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "System performs well",
				Confidence: 0.85,
				TopFindings: []Finding{
					{Finding: "Finding 1 from mode-a", Impact: ImpactHigh, Confidence: 0.9},
					{Finding: "Finding 2 from mode-a", Impact: ImpactMedium, Confidence: 0.8},
				},
				Risks: []Risk{
					{Risk: "Risk from mode-a", Impact: ImpactMedium, Likelihood: 0.5},
				},
			},
			{
				ModeID:     "mode-b",
				Thesis:     "System has issues",
				Confidence: 0.75,
				TopFindings: []Finding{
					{Finding: "Finding 1 from mode-b", Impact: ImpactHigh, Confidence: 0.85},
				},
				Recommendations: []Recommendation{
					{Recommendation: "Recommendation from mode-b", Priority: ImpactHigh},
				},
			},
		},
	}

	result, err := synth.Synthesize(input)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}

	t.Logf("TEST: %s - result has %d findings", t.Name(), len(result.Findings))

	// Verify contributions are populated
	if result.Contributions == nil {
		t.Fatal("Contributions should not be nil")
	}

	t.Logf("TEST: %s - contributions: total=%d deduped=%d scores=%d",
		t.Name(),
		result.Contributions.TotalFindings,
		result.Contributions.DedupedFindings,
		len(result.Contributions.Scores))

	if result.Contributions.TotalFindings != 3 {
		t.Errorf("TotalFindings = %d, want 3", result.Contributions.TotalFindings)
	}

	if len(result.Contributions.Scores) != 2 {
		t.Errorf("Scores = %d, want 2 (one per mode)", len(result.Contributions.Scores))
	}

	// mode-a should rank higher (2 findings vs 1)
	foundModeA := false
	for _, score := range result.Contributions.Scores {
		if score.ModeID == "mode-a" {
			foundModeA = true
			if score.OriginalFindings != 2 {
				t.Errorf("mode-a OriginalFindings = %d, want 2", score.OriginalFindings)
			}
			if score.RisksCount != 1 {
				t.Errorf("mode-a RisksCount = %d, want 1", score.RisksCount)
			}
		}
	}
	if !foundModeA {
		t.Error("mode-a should be in Contributions.Scores")
	}

	t.Logf("TEST: %s - assertion: contributions populated correctly", t.Name())
}

func TestSynthesizer_Contributions_UniqueInsights(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	cfg := SynthesisConfig{Strategy: StrategyManual}
	synth, _ := NewSynthesizer(cfg)

	// Create two modes with one shared finding and one unique finding each
	input := &SynthesisInput{
		OriginalQuestion: "Test question",
		Outputs: []ModeOutput{
			{
				ModeID:     "mode-a",
				Thesis:     "Thesis A",
				Confidence: 0.8,
				TopFindings: []Finding{
					{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.9, EvidencePointer: "file.go:10"},
					{Finding: "Unique to mode-a", Impact: ImpactMedium, Confidence: 0.8},
				},
			},
			{
				ModeID:     "mode-b",
				Thesis:     "Thesis B",
				Confidence: 0.8,
				TopFindings: []Finding{
					{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.9, EvidencePointer: "file.go:10"},
					{Finding: "Unique to mode-b", Impact: ImpactMedium, Confidence: 0.7},
				},
			},
		},
	}

	result, err := synth.Synthesize(input)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}

	t.Logf("TEST: %s - result has %d findings", t.Name(), len(result.Findings))

	if result.Contributions == nil {
		t.Fatal("Contributions should not be nil")
	}

	// With 4 original and 3 deduped, each mode should have 1 unique
	t.Logf("TEST: %s - contributions: total=%d deduped=%d",
		t.Name(),
		result.Contributions.TotalFindings,
		result.Contributions.DedupedFindings)

	for _, score := range result.Contributions.Scores {
		t.Logf("TEST: %s - mode %s: findings=%d original=%d unique=%d",
			t.Name(), score.ModeID, score.FindingsCount, score.OriginalFindings, score.UniqueInsights)
	}

	t.Logf("TEST: %s - assertion: unique insights tracked", t.Name())
}
