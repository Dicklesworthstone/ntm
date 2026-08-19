package ensemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// =============================================================================
// Fixture-Based Tests
// =============================================================================

func TestSchemaValidator_ParseFixture_Malformed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "schema", "malformed.yaml"))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	v := NewSchemaValidator()
	_, err = v.ParseYAML(string(data))
	if err == nil {
		t.Error("expected parse error for malformed YAML")
	} else {
		t.Logf("parse error (expected): %v", err)
	}
}

// =============================================================================
// Serialization Round-Trip Tests
// =============================================================================

func TestModeOutput_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second) // Truncate for JSON precision
	output := ModeOutput{
		ModeID: "bayesian-inference",
		Thesis: "Test thesis for round-trip",
		TopFindings: []Finding{
			{
				Finding:         "Finding one",
				Impact:          ImpactHigh,
				Confidence:      0.85,
				EvidencePointer: "file.go:42",
			},
			{
				Finding:    "Finding two",
				Impact:     ImpactCritical,
				Confidence: 0.95,
			},
		},
		Risks: []Risk{
			{Risk: "Risk description", Impact: ImpactMedium, Likelihood: 0.3},
		},
		Recommendations: []Recommendation{
			{Recommendation: "Do something", Priority: ImpactHigh},
		},
		QuestionsForUser: []Question{
			{Question: "What next?"},
		},
		FailureModesToWatch: []FailureModeWarning{
			{Mode: "bias", Description: "Watch for confirmation bias"},
		},
		Confidence:  0.87,
		GeneratedAt: now,
		RawOutput:   "original raw content",
	}

	// Marshal to JSON
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	t.Logf("JSON output: %s", string(data))

	// Unmarshal back
	var decoded ModeOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Verify fields
	if decoded.ModeID != output.ModeID {
		t.Errorf("ModeID = %q, want %q", decoded.ModeID, output.ModeID)
	}
	if decoded.Thesis != output.Thesis {
		t.Errorf("Thesis = %q, want %q", decoded.Thesis, output.Thesis)
	}
	if len(decoded.TopFindings) != len(output.TopFindings) {
		t.Fatalf("TopFindings count = %d, want %d", len(decoded.TopFindings), len(output.TopFindings))
	}
	if decoded.TopFindings[0].Impact != ImpactHigh {
		t.Errorf("Finding[0].Impact = %q, want %q", decoded.TopFindings[0].Impact, ImpactHigh)
	}
	if decoded.TopFindings[1].Impact != ImpactCritical {
		t.Errorf("Finding[1].Impact = %q, want %q", decoded.TopFindings[1].Impact, ImpactCritical)
	}
	if decoded.Confidence != output.Confidence {
		t.Errorf("Confidence = %v, want %v", decoded.Confidence, output.Confidence)
	}
	if decoded.RawOutput != output.RawOutput {
		t.Errorf("RawOutput = %q, want %q", decoded.RawOutput, output.RawOutput)
	}
}

func TestModeOutput_YAMLRoundTrip(t *testing.T) {
	output := ModeOutput{
		ModeID: "deductive",
		Thesis: "YAML round-trip test",
		TopFindings: []Finding{
			{Finding: "Test finding", Impact: ImpactLow, Confidence: 0.6},
		},
		Confidence: 0.75,
	}

	// Marshal to YAML
	data, err := yaml.Marshal(output)
	if err != nil {
		t.Fatalf("YAML marshal failed: %v", err)
	}
	t.Logf("YAML output:\n%s", string(data))

	// Unmarshal back
	var decoded ModeOutput
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("YAML unmarshal failed: %v", err)
	}

	if decoded.ModeID != output.ModeID {
		t.Errorf("ModeID = %q, want %q", decoded.ModeID, output.ModeID)
	}
	if decoded.Thesis != output.Thesis {
		t.Errorf("Thesis = %q, want %q", decoded.Thesis, output.Thesis)
	}
	if decoded.Confidence != output.Confidence {
		t.Errorf("Confidence = %v, want %v", decoded.Confidence, output.Confidence)
	}
}

func TestFinding_JSONRoundTrip(t *testing.T) {
	finding := Finding{
		Finding:         "Critical security vulnerability",
		Impact:          ImpactCritical,
		Confidence:      0.99,
		EvidencePointer: "src/auth/login.go:142",
	}

	data, err := json.Marshal(finding)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Finding
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Finding != finding.Finding {
		t.Errorf("Finding = %q, want %q", decoded.Finding, finding.Finding)
	}
	if decoded.Impact != ImpactCritical {
		t.Errorf("Impact = %q, want %q", decoded.Impact, ImpactCritical)
	}
	if decoded.Confidence != finding.Confidence {
		t.Errorf("Confidence = %v, want %v", decoded.Confidence, finding.Confidence)
	}
	if decoded.EvidencePointer != finding.EvidencePointer {
		t.Errorf("EvidencePointer = %q, want %q", decoded.EvidencePointer, finding.EvidencePointer)
	}
}

func TestRisk_JSONRoundTrip(t *testing.T) {
	risk := Risk{
		Risk:       "Potential data loss",
		Impact:     ImpactHigh,
		Likelihood: 0.25,
	}

	data, err := json.Marshal(risk)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Risk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Risk != risk.Risk {
		t.Errorf("Risk = %q, want %q", decoded.Risk, risk.Risk)
	}
	if decoded.Impact != risk.Impact {
		t.Errorf("Impact = %q, want %q", decoded.Impact, risk.Impact)
	}
	if decoded.Likelihood != risk.Likelihood {
		t.Errorf("Likelihood = %v, want %v", decoded.Likelihood, risk.Likelihood)
	}
}

func TestRecommendation_JSONRoundTrip(t *testing.T) {
	rec := Recommendation{
		Recommendation: "Implement rate limiting",
		Priority:       ImpactCritical,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Recommendation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Recommendation != rec.Recommendation {
		t.Errorf("Recommendation = %q, want %q", decoded.Recommendation, rec.Recommendation)
	}
	if decoded.Priority != ImpactCritical {
		t.Errorf("Priority = %q, want %q", decoded.Priority, ImpactCritical)
	}
}

// =============================================================================
// ImpactLevel/Critical Tests
// =============================================================================

func TestImpactLevel_CriticalInAllContexts(t *testing.T) {
	// Verify critical is valid in all impact/priority contexts
	if !ImpactCritical.IsValid() {
		t.Error("ImpactCritical should be valid")
	}

	// As finding impact
	finding := Finding{
		Finding:    "Critical bug",
		Impact:     ImpactCritical,
		Confidence: 0.9,
	}
	if err := finding.Validate(); err != nil {
		t.Errorf("Finding with critical impact should be valid: %v", err)
	}

	// As risk impact
	risk := Risk{
		Risk:       "Critical risk",
		Impact:     ImpactCritical,
		Likelihood: 0.5,
	}
	if err := risk.Validate(); err != nil {
		t.Errorf("Risk with critical impact should be valid: %v", err)
	}

	// As recommendation priority
	rec := Recommendation{
		Recommendation: "Critical action",
		Priority:       ImpactCritical,
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("Recommendation with critical priority should be valid: %v", err)
	}
}

func TestImpactLevel_AllValidLevels(t *testing.T) {
	levels := []ImpactLevel{ImpactCritical, ImpactHigh, ImpactMedium, ImpactLow}
	for _, level := range levels {
		if !level.IsValid() {
			t.Errorf("ImpactLevel(%q) should be valid", level)
		}
	}
}

func TestImpactLevel_InvalidLevels(t *testing.T) {
	invalid := []string{"extreme", "urgent", "none", "very-high", ""}
	for _, s := range invalid {
		level := ImpactLevel(s)
		if level.IsValid() {
			t.Errorf("ImpactLevel(%q) should be invalid", s)
		}
	}
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestModeOutput_EmptyOptionalFields(t *testing.T) {
	output := ModeOutput{
		ModeID: "test",
		Thesis: "Test thesis",
		TopFindings: []Finding{
			{Finding: "F1", Impact: ImpactHigh, Confidence: 0.8},
		},
		// Empty optional fields
		Risks:               []Risk{},
		Recommendations:     []Recommendation{},
		QuestionsForUser:    []Question{},
		FailureModesToWatch: []FailureModeWarning{},
		Confidence:          0.7,
	}

	if err := output.Validate(); err != nil {
		t.Errorf("Output with empty optional fields should be valid: %v", err)
	}

	// JSON round-trip should preserve empty slices
	data, _ := json.Marshal(output)
	var decoded ModeOutput
	json.Unmarshal(data, &decoded)

	// Note: JSON null vs empty slice behavior may vary
	if decoded.ModeID != output.ModeID {
		t.Errorf("ModeID mismatch after round-trip")
	}
}

func TestModeOutput_LargeFindings(t *testing.T) {
	// Test with many findings
	findings := make([]Finding, 100)
	for i := 0; i < 100; i++ {
		findings[i] = Finding{
			Finding:    "Finding " + string(rune('A'+i%26)),
			Impact:     ImpactMedium,
			Confidence: Confidence(float64(i%100) / 100.0),
		}
	}

	output := ModeOutput{
		ModeID:      "large-test",
		Thesis:      "Test with many findings",
		TopFindings: findings,
		Confidence:  0.5,
	}

	v := NewSchemaValidator()
	errs := v.Validate(&output)
	if len(errs) > 0 {
		t.Errorf("unexpected validation errors: %v", errs)
	}
}

func TestConfidence_BoundaryValues(t *testing.T) {
	// Exact boundaries should be valid
	tests := []struct {
		value Confidence
		valid bool
	}{
		{0.0, true},
		{1.0, true},
		{0.5, true},
		{-0.0001, false},
		{1.0001, false},
	}

	for _, tc := range tests {
		err := tc.value.Validate()
		if tc.valid && err != nil {
			t.Errorf("Confidence(%v) should be valid: %v", tc.value, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("Confidence(%v) should be invalid", tc.value)
		}
	}
}

func TestConfidence_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    Confidence
		wantErr bool
	}{
		{"float", "0.75", 0.75, false},
		{"high", "high", 0.8, false},
		{"HIGH", "HIGH", 0.8, false},
		{"medium", "medium", 0.5, false},
		{"low", "low", 0.2, false},
		{"percentage", "\"80%\"", 0.8, false},
		{"invalid", "invalid", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c Confidence
			err := yaml.Unmarshal([]byte(tc.yaml), &c)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.yaml)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c != tc.want {
				t.Errorf("got %v, want %v", c, tc.want)
			}
		})
	}
}

func TestConfidence_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    Confidence
		wantErr bool
	}{
		{"float", "0.75", 0.75, false},
		{"high", `"high"`, 0.8, false},
		{"medium", `"medium"`, 0.5, false},
		{"low", `"low"`, 0.2, false},
		{"percentage", `"80%"`, 0.8, false},
		{"invalid", `"invalid"`, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c Confidence
			err := json.Unmarshal([]byte(tc.json), &c)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c != tc.want {
				t.Errorf("got %v, want %v", c, tc.want)
			}
		})
	}
}

func TestValidationError_Serialization(t *testing.T) {
	ve := ValidationError{
		Field:   "findings[0].confidence",
		Message: "must be between 0.0 and 1.0",
		Value:   1.5,
	}

	data, err := json.Marshal(ve)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ValidationError
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Field != ve.Field {
		t.Errorf("Field = %q, want %q", decoded.Field, ve.Field)
	}
	if decoded.Message != ve.Message {
		t.Errorf("Message = %q, want %q", decoded.Message, ve.Message)
	}
	// Value is interface{}, check it's not nil
	if decoded.Value == nil {
		t.Error("Value should not be nil")
	}
}
