package ensemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewOutputCollector(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	if collector == nil {
		t.Fatal("NewOutputCollector returned nil")
	}
	if collector.Outputs == nil {
		t.Error("Outputs slice is nil")
	}
	if collector.ValidationErrors == nil {
		t.Error("ValidationErrors map is nil")
	}
}

func TestOutputCollector_Add_ValidOutput(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	output := ModeOutput{
		ModeID: "test-mode",
		Thesis: "This is a test thesis",
		TopFindings: []Finding{
			{
				Finding:    "Test finding",
				Impact:     ImpactMedium,
				Confidence: 0.8,
			},
		},
		Confidence: 0.75,
	}

	err := collector.Add(output)
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if collector.Count() != 1 {
		t.Errorf("Count = %d, want 1", collector.Count())
	}
	if collector.ErrorCount() != 0 {
		t.Errorf("ErrorCount = %d, want 0", collector.ErrorCount())
	}
}

func TestOutputCollector_Add_InvalidOutput_MissingFields(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	// Missing ModeID
	output := ModeOutput{
		Thesis:      "This is a test thesis",
		TopFindings: []Finding{{Finding: "Test", Impact: ImpactMedium, Confidence: 0.8}},
		Confidence:  0.75,
	}

	err := collector.Add(output)
	// With RequireAll = false (default), no error but tracked in ValidationErrors
	if err != nil {
		t.Fatalf("Add returned error with RequireAll=false: %v", err)
	}

	if collector.Count() != 0 {
		t.Errorf("Valid output count = %d, want 0", collector.Count())
	}
	if collector.ErrorCount() != 1 {
		t.Errorf("Error count = %d, want 1", collector.ErrorCount())
	}
}

func TestOutputCollector_Add_RequireAll(t *testing.T) {
	cfg := OutputCollectorConfig{
		RequireAll: true,
		MinOutputs: 1,
	}
	collector := NewOutputCollector(cfg)

	// Missing required fields
	output := ModeOutput{
		Thesis: "No mode ID",
	}

	err := collector.Add(output)
	if err == nil {
		t.Error("Expected error with RequireAll=true and invalid output")
	}
}

func TestOutputCollector_AddRaw_ValidJSON(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	rawJSON := `{
		"mode_id": "test-mode",
		"thesis": "Test thesis from raw JSON",
		"top_findings": [{"finding": "Raw finding", "impact": "medium", "confidence": 0.9}],
		"confidence": 0.85
	}`

	err := collector.AddRaw("test-mode", rawJSON)
	if err != nil {
		t.Fatalf("AddRaw returned error: %v", err)
	}

	if collector.Count() != 1 {
		t.Errorf("Count = %d, want 1", collector.Count())
	}
}

func TestOutputCollector_AddRaw_InvalidJSON(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	rawJSON := `{invalid json}`

	err := collector.AddRaw("test-mode", rawJSON)
	// Should not error, but track validation error
	if err != nil {
		t.Fatalf("AddRaw returned error: %v", err)
	}

	if collector.Count() != 0 {
		t.Errorf("Count = %d, want 0", collector.Count())
	}
	if collector.ErrorCount() != 1 {
		t.Errorf("ErrorCount = %d, want 1", collector.ErrorCount())
	}
}

func TestOutputCollector_CollectFromSavedOutputs(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	output := ModeOutput{
		ModeID: "saved-mode",
		Thesis: "Saved output thesis",
		TopFindings: []Finding{
			{
				Finding:    "Saved finding",
				Impact:     ImpactMedium,
				Confidence: 0.8,
			},
		},
		Confidence: 0.9,
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "saved-mode.json")
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write saved output: %v", err)
	}

	session := &EnsembleSession{
		SessionName: "offline-ensemble",
		Assignments: []ModeAssignment{
			{ModeID: "saved-mode", Status: AssignmentDone, OutputPath: outputPath},
		},
	}

	if err := collector.CollectFromSavedOutputs(session); err != nil {
		t.Fatalf("CollectFromSavedOutputs returned error: %v", err)
	}
	if collector.Count() != 1 {
		t.Fatalf("collector.Count() = %d, want 1", collector.Count())
	}
	if collector.Outputs[0].ModeID != "saved-mode" {
		t.Fatalf("mode_id = %q, want %q", collector.Outputs[0].ModeID, "saved-mode")
	}
}

func TestOutputCollector_CollectFromSavedOutputs_MissingOutputPath(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	session := &EnsembleSession{
		SessionName: "offline-ensemble",
		Assignments: []ModeAssignment{
			{ModeID: "missing-mode", Status: AssignmentDone},
		},
	}

	if err := collector.CollectFromSavedOutputs(session); err != nil {
		t.Fatalf("CollectFromSavedOutputs returned error: %v", err)
	}
	if collector.Count() != 0 {
		t.Fatalf("collector.Count() = %d, want 0", collector.Count())
	}
	if collector.ErrorCount() != 1 {
		t.Fatalf("collector.ErrorCount() = %d, want 1", collector.ErrorCount())
	}
}

func TestCollectModeOutputs_MergesCapturedAndSavedOutputs(t *testing.T) {
	captured := []CapturedOutput{
		{
			ModeID: "captured-mode",
			Parsed: &ModeOutput{
				ModeID: "captured-mode",
				Thesis: "Captured thesis",
				TopFindings: []Finding{{
					Finding:    "Captured finding",
					Impact:     ImpactMedium,
					Confidence: 0.8,
				}},
				Confidence:  0.8,
				GeneratedAt: time.Now().UTC(),
			},
		},
	}

	savedOutput := strings.TrimSpace(`
mode_id: saved-mode
thesis: Saved thesis
top_findings:
  - finding: Saved finding
    impact: medium
    confidence: 0.9
confidence: 0.9
`)
	savedPath := filepath.Join(t.TempDir(), "saved-mode.yaml")
	if err := os.WriteFile(savedPath, []byte(savedOutput), 0o644); err != nil {
		t.Fatalf("write saved output: %v", err)
	}

	session := &EnsembleSession{
		SessionName: "merge-session",
		Assignments: []ModeAssignment{
			{ModeID: "captured-mode", Status: AssignmentDone},
			{ModeID: "saved-mode", Status: AssignmentDone, OutputPath: savedPath},
		},
	}

	collector, err := CollectModeOutputs(session, captured)
	if err != nil {
		t.Fatalf("CollectModeOutputs returned error: %v", err)
	}
	if collector.Count() != 2 {
		t.Fatalf("collector.Count() = %d, want 2", collector.Count())
	}
	if collector.Outputs[0].ModeID != "captured-mode" || collector.Outputs[1].ModeID != "saved-mode" {
		t.Fatalf("mode order = [%s %s], want [captured-mode saved-mode]", collector.Outputs[0].ModeID, collector.Outputs[1].ModeID)
	}
}

func TestCollectModeOutputs_PrefersCapturedOutputOverSavedOutput(t *testing.T) {
	savedOutput := strings.TrimSpace(`
mode_id: duplicate-mode
thesis: Saved thesis
top_findings:
  - finding: Saved finding
    impact: medium
    confidence: 0.6
confidence: 0.6
`)
	savedPath := filepath.Join(t.TempDir(), "duplicate-mode.yaml")
	if err := os.WriteFile(savedPath, []byte(savedOutput), 0o644); err != nil {
		t.Fatalf("write saved output: %v", err)
	}

	session := &EnsembleSession{
		SessionName: "prefer-live-session",
		Assignments: []ModeAssignment{
			{ModeID: "duplicate-mode", Status: AssignmentDone, OutputPath: savedPath},
		},
	}
	captured := []CapturedOutput{
		{
			ModeID: "duplicate-mode",
			Parsed: &ModeOutput{
				ModeID: "duplicate-mode",
				Thesis: "Captured thesis",
				TopFindings: []Finding{{
					Finding:    "Captured finding",
					Impact:     ImpactHigh,
					Confidence: 0.95,
				}},
				Confidence:  0.95,
				GeneratedAt: time.Now().UTC(),
			},
		},
	}

	collector, err := CollectModeOutputs(session, captured)
	if err != nil {
		t.Fatalf("CollectModeOutputs returned error: %v", err)
	}
	if collector.Count() != 1 {
		t.Fatalf("collector.Count() = %d, want 1", collector.Count())
	}
	if collector.Outputs[0].Thesis != "Captured thesis" {
		t.Fatalf("thesis = %q, want %q", collector.Outputs[0].Thesis, "Captured thesis")
	}
}

func TestOutputCollector_AddRaw_EmptyJSON(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	err := collector.AddRaw("test-mode", "")
	if err != nil {
		t.Fatalf("AddRaw returned error: %v", err)
	}

	if collector.Count() != 0 {
		t.Errorf("Count = %d, want 0", collector.Count())
	}
	if collector.ErrorCount() != 1 {
		t.Errorf("ErrorCount = %d, want 1", collector.ErrorCount())
	}
}

func TestOutputCollector_Collect(t *testing.T) {
	cfg := OutputCollectorConfig{
		MinOutputs: 2,
	}
	collector := NewOutputCollector(cfg)

	// Add two valid outputs
	for i := 0; i < 2; i++ {
		output := ModeOutput{
			ModeID:      "mode-" + string(rune('a'+i)),
			Thesis:      "Test thesis",
			TopFindings: []Finding{{Finding: "Finding", Impact: ImpactMedium, Confidence: 0.8}},
			Confidence:  0.75,
		}
		_ = collector.Add(output)
	}

	result, err := collector.Collect()
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Collect returned nil result")
	}

	if len(result.ValidOutputs) != 2 {
		t.Errorf("ValidOutputs count = %d, want 2", len(result.ValidOutputs))
	}
	if result.Stats.ValidCount != 2 {
		t.Errorf("Stats.ValidCount = %d, want 2", result.Stats.ValidCount)
	}
}

func TestOutputCollector_Collect_InsufficientOutputs(t *testing.T) {
	cfg := OutputCollectorConfig{
		MinOutputs: 3,
	}
	collector := NewOutputCollector(cfg)

	// Add only one valid output
	output := ModeOutput{
		ModeID:      "test-mode",
		Thesis:      "Test thesis",
		TopFindings: []Finding{{Finding: "Finding", Impact: ImpactMedium, Confidence: 0.8}},
		Confidence:  0.75,
	}
	_ = collector.Add(output)

	result, err := collector.Collect()
	if err == nil {
		t.Error("Expected error for insufficient outputs")
	}
	if result == nil {
		t.Fatal("Result should not be nil even with error")
	}
	if result.Stats.ValidCount != 1 {
		t.Errorf("Stats.ValidCount = %d, want 1", result.Stats.ValidCount)
	}
}

func TestOutputCollector_BuildSynthesisInput(t *testing.T) {
	cfg := OutputCollectorConfig{MinOutputs: 1}
	collector := NewOutputCollector(cfg)

	output := ModeOutput{
		ModeID:      "test-mode",
		Thesis:      "Test thesis",
		TopFindings: []Finding{{Finding: "Finding", Impact: ImpactMedium, Confidence: 0.8}},
		Confidence:  0.75,
	}
	_ = collector.Add(output)

	question := "What is the meaning of life?"
	pack := &ContextPack{Hash: "abc123", TokenEstimate: 1000}
	synthCfg := SynthesisConfig{Strategy: StrategyManual}

	input, err := collector.BuildSynthesisInput(question, pack, synthCfg)
	if err != nil {
		t.Fatalf("BuildSynthesisInput returned error: %v", err)
	}

	if input.OriginalQuestion != question {
		t.Errorf("OriginalQuestion = %q, want %q", input.OriginalQuestion, question)
	}
	if input.ContextPack != pack {
		t.Error("ContextPack not set correctly")
	}
	if len(input.Outputs) != 1 {
		t.Errorf("Outputs count = %d, want 1", len(input.Outputs))
	}
	// AuditReport should be populated
	if input.AuditReport == nil {
		t.Error("AuditReport should not be nil")
	}
}

func TestOutputCollector_BuildSynthesisInput_NoOutputs(t *testing.T) {
	cfg := OutputCollectorConfig{MinOutputs: 0} // Allow 0 for this test
	collector := NewOutputCollector(cfg)

	_, err := collector.BuildSynthesisInput("question", nil, SynthesisConfig{})
	if err == nil {
		t.Error("Expected error for no outputs")
	}
}

func TestOutputCollector_Validate(t *testing.T) {
	tests := []struct {
		name       string
		output     ModeOutput
		wantErrors int
	}{
		{
			name: "valid output",
			output: ModeOutput{
				ModeID:      "test",
				Thesis:      "thesis",
				TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
				Confidence:  0.5,
			},
			wantErrors: 0,
		},
		{
			name: "missing mode_id",
			output: ModeOutput{
				Thesis:      "thesis",
				TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
				Confidence:  0.5,
			},
			wantErrors: 1,
		},
		{
			name: "missing thesis",
			output: ModeOutput{
				ModeID:      "test",
				TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
				Confidence:  0.5,
			},
			wantErrors: 1,
		},
		{
			name: "no findings",
			output: ModeOutput{
				ModeID:      "test",
				Thesis:      "thesis",
				TopFindings: []Finding{},
				Confidence:  0.5,
			},
			wantErrors: 1,
		},
		{
			name: "invalid confidence",
			output: ModeOutput{
				ModeID:      "test",
				Thesis:      "thesis",
				TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
				Confidence:  1.5, // Out of range
			},
			wantErrors: 1,
		},
		{
			name: "invalid finding confidence",
			output: ModeOutput{
				ModeID:      "test",
				Thesis:      "thesis",
				TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: -0.5}},
				Confidence:  0.5,
			},
			wantErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OutputCollectorConfig{RequireAll: true}
			collector := NewOutputCollector(cfg)

			err := collector.Add(tt.output)

			if tt.wantErrors > 0 {
				if err == nil {
					t.Error("Expected error for invalid output")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestCollectorResult_Stats(t *testing.T) {
	cfg := DefaultOutputCollectorConfig()
	collector := NewOutputCollector(cfg)

	// Add valid output
	valid := ModeOutput{
		ModeID:      "valid",
		Thesis:      "thesis",
		TopFindings: []Finding{{Finding: "f", Impact: ImpactMedium, Confidence: 0.5}},
		Confidence:  0.5,
	}
	_ = collector.Add(valid)

	// Add invalid output
	invalid := ModeOutput{ModeID: "invalid"} // Missing required fields
	_ = collector.Add(invalid)

	result, _ := collector.Collect()

	if result.Stats.TotalReceived != 2 {
		t.Errorf("TotalReceived = %d, want 2", result.Stats.TotalReceived)
	}
	if result.Stats.ValidCount != 1 {
		t.Errorf("ValidCount = %d, want 1", result.Stats.ValidCount)
	}
	if result.Stats.InvalidCount != 1 {
		t.Errorf("InvalidCount = %d, want 1", result.Stats.InvalidCount)
	}
	if len(result.InvalidOutputs) != 1 {
		t.Errorf("InvalidOutputs count = %d, want 1", len(result.InvalidOutputs))
	}
}
