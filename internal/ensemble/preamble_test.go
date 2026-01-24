package ensemble

import (
	"strings"
	"testing"
	"time"
)

func TestNewPreambleEngine(t *testing.T) {
	engine := NewPreambleEngine()
	if engine == nil {
		t.Fatal("NewPreambleEngine() returned nil")
	}
	if engine.baseTemplate == nil {
		t.Error("baseTemplate is nil")
	}
	if engine.schemaContract == "" {
		t.Error("schemaContract is empty")
	}
}

func TestPreambleEngine_Render_Basic(t *testing.T) {
	engine := NewPreambleEngine()

	data := &PreambleData{
		Problem:  "How can we improve test coverage?",
		TokenCap: 5000,
		Mode: &ReasoningMode{
			ID:          "deductive",
			Code:        "A1",
			Name:        "Deductive Logic",
			Category:    CategoryFormal,
			Tier:        TierCore,
			Description: "Apply formal logical rules to derive conclusions from premises.",
			Outputs:     "Proof or counterexample",
			BestFor:     []string{"Verification", "Debugging"},
			FailureModes: []string{
				"Insufficient evidence for premises",
				"Hidden assumptions",
			},
		},
	}

	result, err := engine.Render(data)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Check for required sections
	checks := []string{
		"REASONING ENSEMBLE PARTICIPANT",
		"How can we improve test coverage",
		"DO NOT exceed 5000 tokens",
		"YOUR REASONING MODE",
		"Deductive Logic (A1)",
		"REQUIRED OUTPUT FORMAT",
		"mode_id:",
		"thesis:",
		"top_findings:",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("Render() missing expected content: %q", check)
		}
	}
}

func TestPreambleEngine_Render_WithContextPack(t *testing.T) {
	engine := NewPreambleEngine()

	data := &PreambleData{
		Problem:  "Why are builds slow?",
		TokenCap: 3000,
		Mode: &ReasoningMode{
			ID:       "systems-thinking",
			Code:     "K1",
			Name:     "Systems Thinking",
			Category: CategoryDomain,
			Tier:     TierCore,
		},
		ContextPack: &ContextPack{
			Hash:        "abc123",
			GeneratedAt: time.Now(),
			ProjectBrief: &ProjectBrief{
				Name:        "my-project",
				Description: "A test project",
				Languages:   []string{"Go", "TypeScript"},
				OpenIssues:  42,
				Structure: &ProjectStructure{
					TotalFiles: 150,
					TotalLines: 25000,
				},
			},
			UserContext: &UserContext{
				ProblemStatement: "Build times are too long",
				FocusAreas:       []string{"CI/CD", "Dependencies"},
			},
		},
	}

	result, err := engine.Render(data)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Check context is included
	contextChecks := []string{
		"my-project",
		"Go, TypeScript",
		"Open Issues**: 42",
		"Build times are too long",
	}

	for _, check := range contextChecks {
		if !strings.Contains(result, check) {
			t.Errorf("Render() missing context: %q", check)
		}
	}
}

func TestPreambleEngine_Render_AdvancedTierWarning(t *testing.T) {
	engine := NewPreambleEngine()

	data := &PreambleData{
		Problem:  "Test problem",
		TokenCap: 2000,
		Mode: &ReasoningMode{
			ID:       "formal-verification",
			Code:     "A3",
			Name:     "Formal Verification",
			Category: CategoryFormal,
			Tier:     TierAdvanced,
		},
	}

	result, err := engine.Render(data)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	if !strings.Contains(result, "advanced-tier mode") {
		t.Error("Render() missing advanced tier warning")
	}
	if !strings.Contains(result, "expertise") {
		t.Error("Render() missing expertise note")
	}
}

func TestPreambleEngine_Render_ExperimentalTierWarning(t *testing.T) {
	engine := NewPreambleEngine()

	data := &PreambleData{
		Problem:  "Test problem",
		TokenCap: 2000,
		Mode: &ReasoningMode{
			ID:       "quantum-logic",
			Code:     "A99",
			Name:     "Quantum Logic",
			Category: CategoryFormal,
			Tier:     TierExperimental,
		},
	}

	result, err := engine.Render(data)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	if !strings.Contains(result, "experimental-tier mode") {
		t.Error("Render() missing experimental tier warning")
	}
	if !strings.Contains(result, "inconsistent") {
		t.Error("Render() missing experimental note")
	}
}

func TestPreambleEngine_Render_NilData(t *testing.T) {
	engine := NewPreambleEngine()

	_, err := engine.Render(nil)
	if err == nil {
		t.Error("Render(nil) should error")
	}
}

func TestPreambleEngine_Render_NilMode(t *testing.T) {
	engine := NewPreambleEngine()

	data := &PreambleData{
		Problem:  "Test",
		TokenCap: 1000,
		Mode:     nil,
	}

	_, err := engine.Render(data)
	if err == nil {
		t.Error("Render() with nil mode should error")
	}
}

func TestGetSchemaContract(t *testing.T) {
	schema := GetSchemaContract()

	if schema == "" {
		t.Error("GetSchemaContract() returned empty string")
	}

	// Check for required schema elements
	required := []string{
		"REQUIRED OUTPUT FORMAT",
		"mode_id:",
		"thesis:",
		"confidence:",
		"top_findings:",
		"risks:",
		"affected_areas:",
		"recommendations:",
		"related_findings:",
		"questions_for_user:",
		"suggested_answers:",
		"failure_modes_to_watch:",
		"indicators:",
		"SCHEMA VERSION",
		SchemaVersion,
	}

	for _, r := range required {
		if !strings.Contains(schema, r) {
			t.Errorf("GetSchemaContract() missing: %q", r)
		}
	}
}

func TestPreambleEngine_Render_AllModes(t *testing.T) {
	engine := NewPreambleEngine()

	for _, mode := range EmbeddedModes {
		mode := mode
		data := &PreambleData{
			Problem:  "Test problem",
			TokenCap: 2000,
			Mode:     &mode,
		}

		result, err := engine.Render(data)
		if err != nil {
			t.Errorf("Render() failed for mode %s (%s, %s): %v", mode.ID, mode.Code, mode.Tier, err)
			continue
		}

		if !strings.Contains(result, mode.Name) {
			t.Errorf("Render() missing mode name for %s (%s)", mode.ID, mode.Code)
		}
	}
}

func TestPreambleEngine_Render_FallbackIncludesMetadata(t *testing.T) {
	engine := NewPreambleEngine()

	var mode *ReasoningMode
	for i := range EmbeddedModes {
		if EmbeddedModes[i].ID == "statistical" {
			mode = &EmbeddedModes[i]
			break
		}
	}
	if mode == nil {
		t.Fatal("statistical mode not found in EmbeddedModes")
	}

	section := engine.renderModeSection(mode)

	checks := []string{
		mode.Name,
		mode.Code,
		mode.Category.String(),
		"Inference about populations from samples",
		"Effect estimates",
		"A/B tests",
		"P-value worship",
		"long-run procedure properties",
		"advanced-tier mode",
	}

	for _, check := range checks {
		if !strings.Contains(section, check) {
			t.Errorf("fallback section missing %q", check)
		}
	}
}

func TestLoadBaseTemplate(t *testing.T) {
	tmpl := LoadBaseTemplate()

	if tmpl == nil {
		t.Fatal("LoadBaseTemplate() returned nil")
	}
	if tmpl.BaseInstructions == "" {
		t.Error("BaseInstructions is empty")
	}
	if tmpl.SchemaContract == "" {
		t.Error("SchemaContract is empty")
	}
}

func TestFormatContextPack_Nil(t *testing.T) {
	result := formatContextPack(nil)
	if result != "(No context available)" {
		t.Errorf("formatContextPack(nil) = %q, want no context message", result)
	}
}

func TestFormatContextPack_Empty(t *testing.T) {
	result := formatContextPack(&ContextPack{})
	if result != "(Minimal context available)" {
		t.Errorf("formatContextPack(empty) = %q, want minimal context message", result)
	}
}

func TestFormatContextPack_Full(t *testing.T) {
	cp := &ContextPack{
		ProjectBrief: &ProjectBrief{
			Name:        "test-project",
			Description: "A test",
			Languages:   []string{"Go"},
			Frameworks:  []string{"Gin"},
			OpenIssues:  5,
			Structure: &ProjectStructure{
				TotalFiles: 100,
				TotalLines: 10000,
			},
		},
		UserContext: &UserContext{
			ProblemStatement: "Testing focus",
			FocusAreas:       []string{"API"},
			Constraints:      []string{"No breaking changes"},
		},
	}

	result := formatContextPack(cp)

	checks := []string{
		"test-project",
		"Go",
		"Gin",
		"5",
		"100",
		"10000",
		"Testing focus",
		"API",
		"No breaking changes",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("formatContextPack() missing: %q", check)
		}
	}
}

func TestPreambleEngine_Render_ModeMetadata(t *testing.T) {
	engine := NewPreambleEngine()

	mode := &ReasoningMode{
		ID:             "bayesian",
		Code:           "C1",
		Name:           "Bayesian Reasoning",
		Category:       CategoryUncertainty,
		Tier:           TierCore,
		Description:    "Update beliefs based on evidence using probability.",
		Outputs:        "Posterior probabilities and confidence intervals",
		BestFor:        []string{"Uncertain evidence", "Decision making"},
		FailureModes:   []string{"Prior bias", "Ignoring base rates"},
		Differentiator: "Explicitly tracks and updates uncertainty.",
	}

	data := &PreambleData{
		Problem:  "Should we refactor this module?",
		TokenCap: 4000,
		Mode:     mode,
	}

	result, err := engine.Render(data)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Verify mode metadata is included
	metadataChecks := []string{
		"Bayesian Reasoning (C1)",
		"Uncertainty",
		"Update beliefs based on evidence",
		"Posterior probabilities",
		"Uncertain evidence",
		"Decision making",
		"Prior bias",
		"Ignoring base rates",
		"Explicitly tracks and updates uncertainty",
	}

	for _, check := range metadataChecks {
		if !strings.Contains(result, check) {
			t.Errorf("Render() missing mode metadata: %q", check)
		}
	}
}
