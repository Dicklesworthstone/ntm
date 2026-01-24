package ensemble

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

func TestModeCategory_IsValid(t *testing.T) {
	tests := []struct {
		cat   ModeCategory
		valid bool
	}{
		{CategoryFormal, true},
		{CategoryAmpliative, true},
		{CategoryUncertainty, true},
		{CategoryVagueness, true},
		{CategoryChange, true},
		{CategoryCausal, true},
		{CategoryPractical, true},
		{CategoryStrategic, true},
		{CategoryDialectical, true},
		{CategoryModal, true},
		{CategoryDomain, true},
		{CategoryMeta, true},
		{ModeCategory("invalid"), false},
		{ModeCategory(""), false},
	}

	for _, tt := range tests {
		if got := tt.cat.IsValid(); got != tt.valid {
			t.Errorf("ModeCategory(%q).IsValid() = %v, want %v", tt.cat, got, tt.valid)
		}
	}
}

func TestModeCategory_String(t *testing.T) {
	tests := []struct {
		cat  ModeCategory
		want string
	}{
		{CategoryFormal, "Formal"},
		{CategoryAmpliative, "Ampliative"},
		{CategoryUncertainty, "Uncertainty"},
		{CategoryVagueness, "Vagueness"},
		{CategoryChange, "Change"},
		{CategoryCausal, "Causal"},
		{CategoryPractical, "Practical"},
		{CategoryStrategic, "Strategic"},
		{CategoryDialectical, "Dialectical"},
		{CategoryModal, "Modal"},
		{CategoryDomain, "Domain"},
		{CategoryMeta, "Meta"},
		{ModeCategory("custom"), "custom"},
	}

	for _, tt := range tests {
		if got := tt.cat.String(); got != tt.want {
			t.Errorf("ModeCategory(%q).String() = %q, want %q", tt.cat, got, tt.want)
		}
	}
}

func TestValidateModeID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"deductive", false},
		{"bayesian-inference", false},
		{"foo-bar-baz", false},
		{"a", false},
		{"a1", false},
		{"", true},                       // empty
		{"Deductive", true},              // uppercase
		{"123abc", true},                 // starts with number
		{"-invalid", true},               // starts with hyphen
		{"has spaces", true},             // contains spaces
		{"has_underscore", true},         // contains underscore
		{string(make([]byte, 65)), true}, // too long
	}

	for _, tt := range tests {
		err := ValidateModeID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateModeID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
		}
	}
}

func TestReasoningMode_Validate(t *testing.T) {
	validMode := ReasoningMode{
		ID:        "deductive",
		Name:      "Deductive Logic",
		Category:  CategoryFormal,
		ShortDesc: "Derive conclusions from premises",
	}

	if err := validMode.Validate(); err != nil {
		t.Errorf("valid mode should pass validation: %v", err)
	}

	// Test missing ID
	noID := validMode
	noID.ID = ""
	if err := noID.Validate(); err == nil {
		t.Error("mode without ID should fail validation")
	}

	// Test invalid ID
	invalidID := validMode
	invalidID.ID = "INVALID"
	if err := invalidID.Validate(); err == nil {
		t.Error("mode with invalid ID should fail validation")
	}

	// Test missing name
	noName := validMode
	noName.Name = ""
	if err := noName.Validate(); err == nil {
		t.Error("mode without name should fail validation")
	}

	// Test invalid category
	invalidCat := validMode
	invalidCat.Category = "invalid"
	if err := invalidCat.Validate(); err == nil {
		t.Error("mode with invalid category should fail validation")
	}

	// Test long short_desc
	longDesc := validMode
	longDesc.ShortDesc = string(make([]byte, 100))
	if err := longDesc.Validate(); err == nil {
		t.Error("mode with short_desc > 80 chars should fail validation")
	}
}

func TestAssignmentStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   AssignmentStatus
		terminal bool
	}{
		{AssignmentPending, false},
		{AssignmentInjecting, false},
		{AssignmentActive, false},
		{AssignmentDone, true},
		{AssignmentError, true},
	}

	for _, tt := range tests {
		if got := tt.status.IsTerminal(); got != tt.terminal {
			t.Errorf("AssignmentStatus(%q).IsTerminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestEnsembleStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   EnsembleStatus
		terminal bool
	}{
		{EnsembleSpawning, false},
		{EnsembleInjecting, false},
		{EnsembleActive, false},
		{EnsembleSynthesizing, false},
		{EnsembleComplete, true},
		{EnsembleError, true},
	}

	for _, tt := range tests {
		if got := tt.status.IsTerminal(); got != tt.terminal {
			t.Errorf("EnsembleStatus(%q).IsTerminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestSynthesisStrategy_IsValid(t *testing.T) {
	tests := []struct {
		strategy SynthesisStrategy
		valid    bool
	}{
		{StrategyManual, true},
		{StrategyAdversarial, true},
		{StrategyConsensus, true},
		{StrategyCreative, true},
		{StrategyAnalytical, true},
		{StrategyDeliberative, true},
		{StrategyPrioritized, true},
		{StrategyDialectical, true},
		{StrategyMetaReasoning, true},
		{StrategyVoting, true},
		{StrategyArgumentation, true},
		{SynthesisStrategy("invalid"), false},
		{SynthesisStrategy(""), false},
		{SynthesisStrategy("debate"), false},
		{SynthesisStrategy("weighted"), false},
		{SynthesisStrategy("sequential"), false},
		{SynthesisStrategy("best-of"), false},
	}

	for _, tt := range tests {
		if got := tt.strategy.IsValid(); got != tt.valid {
			t.Errorf("SynthesisStrategy(%q).IsValid() = %v, want %v", tt.strategy, got, tt.valid)
		}
	}
}

func TestEnsemblePreset_Validate(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Tier: TierCore},
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test", Tier: TierCore, Code: "C1"},
		{ID: "game-theory", Name: "Game Theory", Category: CategoryStrategic, ShortDesc: "Test", Tier: TierAdvanced, Code: "H1"},
	}
	catalog, err := NewModeCatalog(modes, "1.0")
	if err != nil {
		t.Fatalf("failed to create catalog: %v", err)
	}

	validPreset := EnsemblePreset{
		Name:        "test-preset",
		Description: "A test preset",
		Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("bayesian")},
		Synthesis:   SynthesisConfig{Strategy: StrategyConsensus},
	}

	if err := validPreset.Validate(catalog); err != nil {
		t.Errorf("valid preset should pass validation: %v", err)
	}

	// Test preset with code reference
	codePreset := EnsemblePreset{
		Name:        "code-preset",
		Description: "Uses code ref",
		Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromCode("C1")},
		Synthesis:   SynthesisConfig{Strategy: StrategyDialectical},
	}
	if err := codePreset.Validate(catalog); err != nil {
		t.Errorf("preset with code ref should pass validation: %v", err)
	}

	// Test missing name
	noName := validPreset
	noName.Name = ""
	if err := noName.Validate(catalog); err == nil {
		t.Error("preset without name should fail validation")
	}

	// Test no modes
	noModes := validPreset
	noModes.Modes = nil
	if err := noModes.Validate(catalog); err == nil {
		t.Error("preset without modes should fail validation")
	}

	// Test invalid strategy
	invalidStrategy := validPreset
	invalidStrategy.Synthesis = SynthesisConfig{Strategy: "invalid"}
	if err := invalidStrategy.Validate(catalog); err == nil {
		t.Error("preset with invalid strategy should fail validation")
	}

	// Test missing mode
	missingMode := validPreset
	missingMode.Modes = []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("nonexistent")}
	if err := missingMode.Validate(catalog); err == nil {
		t.Error("preset referencing nonexistent mode should fail validation")
	}

	// Test advanced mode blocked by default
	advancedPreset := EnsemblePreset{
		Name:        "advanced-blocked",
		Description: "Should fail",
		Modes:       []ModeRef{ModeRefFromID("game-theory")},
	}
	if err := advancedPreset.Validate(catalog); err == nil {
		t.Error("preset with advanced mode and AllowAdvanced=false should fail")
	}

	// Test advanced mode allowed with flag
	advancedAllowed := advancedPreset
	advancedAllowed.Name = "advanced-allowed"
	advancedAllowed.AllowAdvanced = true
	if err := advancedAllowed.Validate(catalog); err != nil {
		t.Errorf("preset with AllowAdvanced=true should pass: %v", err)
	}
}

func TestModeCatalog(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive Logic", Category: CategoryFormal, ShortDesc: "Derive conclusions", BestFor: []string{"proofs"}},
		{ID: "bayesian", Name: "Bayesian Inference", Category: CategoryUncertainty, ShortDesc: "Probabilistic reasoning", BestFor: []string{"prediction"}},
		{ID: "causal-inference", Name: "Causal Inference", Category: CategoryCausal, ShortDesc: "Find causes", BestFor: []string{"debugging"}},
	}

	cat, err := NewModeCatalog(modes, "1.0.0")
	if err != nil {
		t.Fatalf("NewModeCatalog failed: %v", err)
	}

	// Test Count
	if got := cat.Count(); got != 3 {
		t.Errorf("Count() = %d, want 3", got)
	}

	// Test Version
	if got := cat.Version(); got != "1.0.0" {
		t.Errorf("Version() = %q, want %q", got, "1.0.0")
	}

	// Test GetMode
	mode := cat.GetMode("deductive")
	if mode == nil {
		t.Error("GetMode(deductive) returned nil")
	} else if mode.Name != "Deductive Logic" {
		t.Errorf("GetMode(deductive).Name = %q, want %q", mode.Name, "Deductive Logic")
	}

	// Test GetMode nonexistent
	if cat.GetMode("nonexistent") != nil {
		t.Error("GetMode(nonexistent) should return nil")
	}

	// Test ListModes
	all := cat.ListModes()
	if len(all) != 3 {
		t.Errorf("ListModes() returned %d modes, want 3", len(all))
	}

	// Test ListByCategory
	formal := cat.ListByCategory(CategoryFormal)
	if len(formal) != 1 {
		t.Errorf("ListByCategory(Formal) returned %d modes, want 1", len(formal))
	}

	// Test SearchModes
	found := cat.SearchModes("logic")
	if len(found) != 1 {
		t.Errorf("SearchModes(logic) returned %d modes, want 1", len(found))
	}

	// Test search in BestFor
	foundBestFor := cat.SearchModes("proofs")
	if len(foundBestFor) != 1 {
		t.Errorf("SearchModes(proofs) returned %d modes, want 1", len(foundBestFor))
	}
}

func TestModeCatalog_DuplicateID(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive Logic", Category: CategoryFormal, ShortDesc: "Test"},
		{ID: "deductive", Name: "Duplicate", Category: CategoryFormal, ShortDesc: "Test"},
	}

	_, err := NewModeCatalog(modes, "1.0.0")
	if err == nil {
		t.Error("NewModeCatalog should fail with duplicate IDs")
	}
}

func TestModeCatalog_InvalidMode(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "INVALID", Name: "Invalid", Category: CategoryFormal, ShortDesc: "Test"},
	}

	_, err := NewModeCatalog(modes, "1.0.0")
	if err == nil {
		t.Error("NewModeCatalog should fail with invalid mode")
	}
}

func TestModeAssignment_Fields(t *testing.T) {
	now := time.Now()
	completed := now.Add(time.Hour)

	assignment := ModeAssignment{
		ModeID:      "deductive",
		PaneName:    "myproject__cc_1",
		AgentType:   "cc",
		Status:      AssignmentActive,
		OutputPath:  "/tmp/output.txt",
		AssignedAt:  now,
		CompletedAt: &completed,
		Error:       "",
	}

	if assignment.ModeID != "deductive" {
		t.Errorf("ModeID = %q, want %q", assignment.ModeID, "deductive")
	}
	if assignment.Status != AssignmentActive {
		t.Errorf("Status = %q, want %q", assignment.Status, AssignmentActive)
	}
}

func TestEnsembleSession_Fields(t *testing.T) {
	now := time.Now()

	session := EnsembleSession{
		SessionName:       "myproject",
		Question:          "What is the best approach?",
		PresetUsed:        "architecture-review",
		Assignments:       []ModeAssignment{},
		Status:            EnsembleActive,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         now,
	}

	if session.Status != EnsembleActive {
		t.Errorf("Status = %q, want %q", session.Status, EnsembleActive)
	}
	if session.SynthesisStrategy != StrategyConsensus {
		t.Errorf("SynthesisStrategy = %q, want %q", session.SynthesisStrategy, StrategyConsensus)
	}
}

func TestAllCategories(t *testing.T) {
	cats := AllCategories()
	if len(cats) != 12 {
		t.Errorf("AllCategories() returned %d categories, want 12", len(cats))
	}

	// All should be valid
	for _, cat := range cats {
		if !cat.IsValid() {
			t.Errorf("AllCategories() returned invalid category %q", cat)
		}
	}
}

// =============================================================================
// Output Schema Tests
// =============================================================================

func TestImpactLevel_IsValid(t *testing.T) {
	tests := []struct {
		level ImpactLevel
		valid bool
	}{
		{ImpactHigh, true},
		{ImpactMedium, true},
		{ImpactLow, true},
		{ImpactLevel("invalid"), false},
		{ImpactLevel(""), false},
	}

	for _, tt := range tests {
		if got := tt.level.IsValid(); got != tt.valid {
			t.Errorf("ImpactLevel(%q).IsValid() = %v, want %v", tt.level, got, tt.valid)
		}
	}
}

func TestConfidence_Validate(t *testing.T) {
	tests := []struct {
		conf    Confidence
		wantErr bool
	}{
		{0.0, false},
		{0.5, false},
		{1.0, false},
		{0.75, false},
		{-0.1, true},
		{1.1, true},
		{2.0, true},
	}

	for _, tt := range tests {
		err := tt.conf.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("Confidence(%v).Validate() error = %v, wantErr %v", tt.conf, err, tt.wantErr)
		}
	}
}

func TestConfidence_String(t *testing.T) {
	tests := []struct {
		conf Confidence
		want string
	}{
		{0.0, "0%"},
		{0.5, "50%"},
		{1.0, "100%"},
		{0.75, "75%"},
	}

	for _, tt := range tests {
		if got := tt.conf.String(); got != tt.want {
			t.Errorf("Confidence(%v).String() = %q, want %q", tt.conf, got, tt.want)
		}
	}
}

func TestFinding_Validate(t *testing.T) {
	validFinding := Finding{
		Finding:    "Test finding",
		Impact:     ImpactHigh,
		Confidence: 0.8,
	}

	if err := validFinding.Validate(); err != nil {
		t.Errorf("valid finding should pass validation: %v", err)
	}

	// Test missing finding text
	noText := validFinding
	noText.Finding = ""
	if err := noText.Validate(); err == nil {
		t.Error("finding without text should fail validation")
	}

	// Test invalid impact
	invalidImpact := validFinding
	invalidImpact.Impact = "invalid"
	if err := invalidImpact.Validate(); err == nil {
		t.Error("finding with invalid impact should fail validation")
	}

	// Test invalid confidence
	invalidConf := validFinding
	invalidConf.Confidence = 1.5
	if err := invalidConf.Validate(); err == nil {
		t.Error("finding with invalid confidence should fail validation")
	}
}

func TestRisk_Validate(t *testing.T) {
	validRisk := Risk{
		Risk:       "Test risk",
		Impact:     ImpactMedium,
		Likelihood: 0.3,
	}

	if err := validRisk.Validate(); err != nil {
		t.Errorf("valid risk should pass validation: %v", err)
	}

	// Test missing risk text
	noText := validRisk
	noText.Risk = ""
	if err := noText.Validate(); err == nil {
		t.Error("risk without text should fail validation")
	}

	// Test invalid impact
	invalidImpact := validRisk
	invalidImpact.Impact = "invalid"
	if err := invalidImpact.Validate(); err == nil {
		t.Error("risk with invalid impact should fail validation")
	}

	// Test invalid likelihood
	invalidLikelihood := validRisk
	invalidLikelihood.Likelihood = -0.5
	if err := invalidLikelihood.Validate(); err == nil {
		t.Error("risk with invalid likelihood should fail validation")
	}
}

func TestRecommendation_Validate(t *testing.T) {
	validRec := Recommendation{
		Recommendation: "Test recommendation",
		Priority:       ImpactHigh,
	}

	if err := validRec.Validate(); err != nil {
		t.Errorf("valid recommendation should pass validation: %v", err)
	}

	// Test missing text
	noText := validRec
	noText.Recommendation = ""
	if err := noText.Validate(); err == nil {
		t.Error("recommendation without text should fail validation")
	}

	// Test invalid priority
	invalidPriority := validRec
	invalidPriority.Priority = "invalid"
	if err := invalidPriority.Validate(); err == nil {
		t.Error("recommendation with invalid priority should fail validation")
	}
}

func TestQuestion_Validate(t *testing.T) {
	validQuestion := Question{
		Question: "What is the requirement?",
	}

	if err := validQuestion.Validate(); err != nil {
		t.Errorf("valid question should pass validation: %v", err)
	}

	// Test missing question text
	noText := validQuestion
	noText.Question = ""
	if err := noText.Validate(); err == nil {
		t.Error("question without text should fail validation")
	}
}

func TestFailureModeWarning_Validate(t *testing.T) {
	validWarning := FailureModeWarning{
		Mode:        "confirmation-bias",
		Description: "Seeking evidence that confirms existing beliefs",
	}

	if err := validWarning.Validate(); err != nil {
		t.Errorf("valid failure mode warning should pass validation: %v", err)
	}

	// Test missing mode
	noMode := validWarning
	noMode.Mode = ""
	if err := noMode.Validate(); err == nil {
		t.Error("warning without mode should fail validation")
	}

	// Test missing description
	noDesc := validWarning
	noDesc.Description = ""
	if err := noDesc.Validate(); err == nil {
		t.Error("warning without description should fail validation")
	}
}

func TestModeOutput_Validate(t *testing.T) {
	validOutput := ModeOutput{
		ModeID: "deductive",
		Thesis: "The system has a critical bug in the auth module",
		TopFindings: []Finding{
			{
				Finding:    "Missing input validation",
				Impact:     ImpactHigh,
				Confidence: 0.9,
			},
		},
		Confidence:  0.85,
		GeneratedAt: time.Now(),
	}

	if err := validOutput.Validate(); err != nil {
		t.Errorf("valid mode output should pass validation: %v", err)
	}

	// Test missing mode_id
	noModeID := validOutput
	noModeID.ModeID = ""
	if err := noModeID.Validate(); err == nil {
		t.Error("output without mode_id should fail validation")
	}

	// Test missing thesis
	noThesis := validOutput
	noThesis.Thesis = ""
	if err := noThesis.Validate(); err == nil {
		t.Error("output without thesis should fail validation")
	}

	// Test no findings
	noFindings := validOutput
	noFindings.TopFindings = nil
	if err := noFindings.Validate(); err == nil {
		t.Error("output without findings should fail validation")
	}

	// Test invalid confidence
	invalidConf := validOutput
	invalidConf.Confidence = 1.5
	if err := invalidConf.Validate(); err == nil {
		t.Error("output with invalid confidence should fail validation")
	}

	// Test with invalid finding
	invalidFinding := validOutput
	invalidFinding.TopFindings = []Finding{{Finding: "", Impact: ImpactHigh, Confidence: 0.5}}
	if err := invalidFinding.Validate(); err == nil {
		t.Error("output with invalid finding should fail validation")
	}
}

func TestModeOutput_ValidateNestedTypes(t *testing.T) {
	validOutput := ModeOutput{
		ModeID:     "test",
		Thesis:     "Test thesis",
		Confidence: 0.5,
		TopFindings: []Finding{
			{Finding: "Valid finding", Impact: ImpactHigh, Confidence: 0.8},
		},
		Risks: []Risk{
			{Risk: "Valid risk", Impact: ImpactMedium, Likelihood: 0.3},
		},
		Recommendations: []Recommendation{
			{Recommendation: "Valid rec", Priority: ImpactLow},
		},
		QuestionsForUser: []Question{
			{Question: "Valid question?"},
		},
		FailureModesToWatch: []FailureModeWarning{
			{Mode: "bias", Description: "Confirmation bias"},
		},
		GeneratedAt: time.Now(),
	}

	if err := validOutput.Validate(); err != nil {
		t.Errorf("output with all valid nested types should pass: %v", err)
	}

	// Test invalid risk
	invalidRisk := validOutput
	invalidRisk.Risks = []Risk{{Risk: "", Impact: ImpactHigh, Likelihood: 0.5}}
	if err := invalidRisk.Validate(); err == nil {
		t.Error("output with invalid risk should fail validation")
	}

	// Test invalid recommendation
	invalidRec := validOutput
	invalidRec.Recommendations = []Recommendation{{Recommendation: "", Priority: ImpactHigh}}
	if err := invalidRec.Validate(); err == nil {
		t.Error("output with invalid recommendation should fail validation")
	}

	// Test invalid question
	invalidQ := validOutput
	invalidQ.QuestionsForUser = []Question{{Question: ""}}
	if err := invalidQ.Validate(); err == nil {
		t.Error("output with invalid question should fail validation")
	}

	// Test invalid failure mode warning
	invalidFM := validOutput
	invalidFM.FailureModesToWatch = []FailureModeWarning{{Mode: "", Description: ""}}
	if err := invalidFM.Validate(); err == nil {
		t.Error("output with invalid failure mode should fail validation")
	}
}

func TestDefaultBudgetConfig(t *testing.T) {
	cfg := DefaultBudgetConfig()

	if cfg.MaxTokensPerMode <= 0 {
		t.Error("MaxTokensPerMode should be positive")
	}
	if cfg.MaxTotalTokens <= 0 {
		t.Error("MaxTotalTokens should be positive")
	}
	if cfg.TimeoutPerMode <= 0 {
		t.Error("TimeoutPerMode should be positive")
	}
	if cfg.TotalTimeout <= 0 {
		t.Error("TotalTimeout should be positive")
	}
}

func TestEstimateModeOutputTokens_UsesRawOutput(t *testing.T) {
	output := ModeOutput{
		ModeID:      "test-mode",
		Thesis:      "short thesis",
		TopFindings: []Finding{{Finding: "finding", Impact: ImpactLow, Confidence: 0.5}},
		Confidence:  0.5,
		RawOutput:   "raw output goes here",
	}

	got := EstimateModeOutputTokens(&output)
	want := tokenpkg.EstimateTokensWithLanguageHint(output.RawOutput, tokenpkg.ContentMarkdown)
	if got != want {
		t.Errorf("EstimateModeOutputTokens() = %d, want %d", got, want)
	}
}

func TestEstimateModeOutputTokens_Fallback(t *testing.T) {
	output := ModeOutput{
		ModeID:      "test-mode",
		Thesis:      "short thesis",
		TopFindings: []Finding{{Finding: "finding", Impact: ImpactLow, Confidence: 0.5}},
		Confidence:  0.5,
	}

	got := EstimateModeOutputTokens(&output)
	if got <= 0 {
		t.Errorf("EstimateModeOutputTokens() = %d, want > 0", got)
	}
}

func TestEstimateModeOutputsTokens_Sums(t *testing.T) {
	outputs := []ModeOutput{
		{
			ModeID:      "mode-a",
			Thesis:      "a",
			TopFindings: []Finding{{Finding: "a", Impact: ImpactLow, Confidence: 0.5}},
			Confidence:  0.5,
			RawOutput:   "alpha output",
		},
		{
			ModeID:      "mode-b",
			Thesis:      "b",
			TopFindings: []Finding{{Finding: "b", Impact: ImpactLow, Confidence: 0.5}},
			Confidence:  0.5,
			RawOutput:   "beta output",
		},
	}

	got := EstimateModeOutputsTokens(outputs)
	want := EstimateModeOutputTokens(&outputs[0]) + EstimateModeOutputTokens(&outputs[1])
	if got != want {
		t.Errorf("EstimateModeOutputsTokens() = %d, want %d", got, want)
	}
}

func TestDefaultSynthesisConfig(t *testing.T) {
	cfg := DefaultSynthesisConfig()

	if !cfg.Strategy.IsValid() {
		t.Error("Strategy should be valid")
	}
	if cfg.MinConfidence < 0 || cfg.MinConfidence > 1 {
		t.Errorf("MinConfidence should be between 0 and 1, got %v", cfg.MinConfidence)
	}
	if cfg.MaxFindings <= 0 {
		t.Error("MaxFindings should be positive")
	}
}

func TestEnsemble_Validate(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test"},
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0.0")

	validEnsemble := Ensemble{
		Name:        "test-ensemble",
		DisplayName: "Test Ensemble",
		Description: "A test ensemble",
		ModeIDs:     []string{"deductive", "bayesian"},
		Synthesis:   DefaultSynthesisConfig(),
		Budget:      DefaultBudgetConfig(),
	}

	if err := validEnsemble.Validate(catalog); err != nil {
		t.Errorf("valid ensemble should pass validation: %v", err)
	}

	// Test missing name
	noName := validEnsemble
	noName.Name = ""
	if err := noName.Validate(catalog); err == nil {
		t.Error("ensemble without name should fail validation")
	}

	// Test invalid name format
	invalidName := validEnsemble
	invalidName.Name = "INVALID"
	if err := invalidName.Validate(catalog); err == nil {
		t.Error("ensemble with invalid name format should fail validation")
	}

	// Test missing display name
	noDisplayName := validEnsemble
	noDisplayName.DisplayName = ""
	if err := noDisplayName.Validate(catalog); err == nil {
		t.Error("ensemble without display_name should fail validation")
	}

	// Test no modes
	noModes := validEnsemble
	noModes.ModeIDs = nil
	if err := noModes.Validate(catalog); err == nil {
		t.Error("ensemble without modes should fail validation")
	}

	// Test nonexistent mode
	badMode := validEnsemble
	badMode.ModeIDs = []string{"deductive", "nonexistent"}
	if err := badMode.Validate(catalog); err == nil {
		t.Error("ensemble with nonexistent mode should fail validation")
	}
}

func TestModeTier_IsValid(t *testing.T) {
	tests := []struct {
		tier  ModeTier
		valid bool
	}{
		{TierCore, true},
		{TierAdvanced, true},
		{TierExperimental, true},
		{ModeTier(""), false},
		{ModeTier("unknown"), false},
		{ModeTier("Core"), false}, // case-sensitive
	}

	for _, tt := range tests {
		if got := tt.tier.IsValid(); got != tt.valid {
			t.Errorf("ModeTier(%q).IsValid() = %v, want %v", tt.tier, got, tt.valid)
		}
	}
}

func TestModeTier_String(t *testing.T) {
	if got := TierCore.String(); got != "core" {
		t.Errorf("TierCore.String() = %q, want %q", got, "core")
	}
	if got := TierAdvanced.String(); got != "advanced" {
		t.Errorf("TierAdvanced.String() = %q, want %q", got, "advanced")
	}
}

func TestModeCategory_CategoryLetter(t *testing.T) {
	tests := []struct {
		cat    ModeCategory
		letter string
	}{
		{CategoryFormal, "A"},
		{CategoryAmpliative, "B"},
		{CategoryUncertainty, "C"},
		{CategoryVagueness, "D"},
		{CategoryChange, "E"},
		{CategoryCausal, "F"},
		{CategoryPractical, "G"},
		{CategoryStrategic, "H"},
		{CategoryDialectical, "I"},
		{CategoryModal, "J"},
		{CategoryDomain, "K"},
		{CategoryMeta, "L"},
		{ModeCategory("invalid"), ""},
	}

	for _, tt := range tests {
		if got := tt.cat.CategoryLetter(); got != tt.letter {
			t.Errorf("ModeCategory(%q).CategoryLetter() = %q, want %q", tt.cat, got, tt.letter)
		}
	}
}

func TestCategoryFromLetter(t *testing.T) {
	tests := []struct {
		letter string
		cat    ModeCategory
		ok     bool
	}{
		{"A", CategoryFormal, true},
		{"B", CategoryAmpliative, true},
		{"C", CategoryUncertainty, true},
		{"D", CategoryVagueness, true},
		{"E", CategoryChange, true},
		{"F", CategoryCausal, true},
		{"G", CategoryPractical, true},
		{"H", CategoryStrategic, true},
		{"I", CategoryDialectical, true},
		{"J", CategoryModal, true},
		{"K", CategoryDomain, true},
		{"L", CategoryMeta, true},
		{"M", "", false},
		{"a", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		cat, ok := CategoryFromLetter(tt.letter)
		if ok != tt.ok {
			t.Errorf("CategoryFromLetter(%q) ok = %v, want %v", tt.letter, ok, tt.ok)
		}
		if cat != tt.cat {
			t.Errorf("CategoryFromLetter(%q) = %q, want %q", tt.letter, cat, tt.cat)
		}
	}
}

func TestValidateModeCode(t *testing.T) {
	tests := []struct {
		code    string
		cat     ModeCategory
		wantErr bool
	}{
		{"A1", CategoryFormal, false},
		{"B3", CategoryAmpliative, false},
		{"L10", CategoryMeta, false},
		{"C99", CategoryUncertainty, false},
		// Invalid format
		{"", CategoryFormal, true},    // empty
		{"a1", CategoryFormal, true},  // lowercase letter
		{"M1", CategoryFormal, true},  // letter out of range
		{"A", CategoryFormal, true},   // no number
		{"AB1", CategoryFormal, true}, // two letters
		{"1A", CategoryFormal, true},  // starts with digit
		// Category mismatch
		{"B1", CategoryFormal, true},     // B != A (Formal)
		{"A1", CategoryAmpliative, true}, // A != B (Ampliative)
		{"K5", CategoryMeta, true},       // K != L (Meta)
	}

	for _, tt := range tests {
		err := ValidateModeCode(tt.code, tt.cat)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateModeCode(%q, %q) error = %v, wantErr %v", tt.code, tt.cat, err, tt.wantErr)
		}
	}
}

func TestReasoningMode_Validate_CodeAndTier(t *testing.T) {
	base := ReasoningMode{
		ID:        "deductive",
		Name:      "Deductive Logic",
		Category:  CategoryFormal,
		ShortDesc: "Derive conclusions from premises",
	}

	// Valid with code and tier
	withCodeTier := base
	withCodeTier.Code = "A1"
	withCodeTier.Tier = TierCore
	if err := withCodeTier.Validate(); err != nil {
		t.Errorf("mode with valid code+tier should pass: %v", err)
	}

	// Valid without code and tier (optional fields)
	if err := base.Validate(); err != nil {
		t.Errorf("mode without code/tier should pass: %v", err)
	}

	// Invalid code format
	badCodeFmt := base
	badCodeFmt.Code = "ZZ9"
	if err := badCodeFmt.Validate(); err == nil {
		t.Error("mode with invalid code format should fail")
	}

	// Code-category mismatch
	mismatch := base
	mismatch.Code = "B1" // B is Ampliative, but category is Formal
	if err := mismatch.Validate(); err == nil {
		t.Error("mode with code-category mismatch should fail")
	}

	// Invalid tier
	badTier := base
	badTier.Tier = ModeTier("unknown")
	if err := badTier.Validate(); err == nil {
		t.Error("mode with invalid tier should fail")
	}
}

func TestModeCatalog_DuplicateCode(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "mode-a", Name: "Mode A", Category: CategoryFormal, ShortDesc: "Test A", Code: "A1", Tier: TierCore},
		{ID: "mode-b", Name: "Mode B", Category: CategoryFormal, ShortDesc: "Test B", Code: "A1", Tier: TierAdvanced},
	}

	_, err := NewModeCatalog(modes, "1.0")
	if err == nil {
		t.Fatal("catalog with duplicate code should fail")
	}
	if got := err.Error(); !strings.Contains(got, "duplicate mode code") {
		t.Errorf("error should mention duplicate code, got: %v", err)
	}
}

func TestModeCatalog_GetModeByCode(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Code: "A1", Tier: TierCore},
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1", Tier: TierAdvanced},
		{ID: "no-code", Name: "No Code", Category: CategoryMeta, ShortDesc: "Test"},
	}

	catalog, err := NewModeCatalog(modes, "1.0")
	if err != nil {
		t.Fatalf("failed to create catalog: %v", err)
	}

	// Lookup by code
	m := catalog.GetModeByCode("A1")
	if m == nil {
		t.Fatal("GetModeByCode(A1) returned nil")
	}
	if m.ID != "deductive" {
		t.Errorf("GetModeByCode(A1).ID = %q, want %q", m.ID, "deductive")
	}

	m = catalog.GetModeByCode("C1")
	if m == nil {
		t.Fatal("GetModeByCode(C1) returned nil")
	}
	if m.ID != "bayesian" {
		t.Errorf("GetModeByCode(C1).ID = %q, want %q", m.ID, "bayesian")
	}

	// Nonexistent code
	if catalog.GetModeByCode("Z9") != nil {
		t.Error("GetModeByCode(Z9) should return nil")
	}

	// Empty code mode shouldn't be in byCode
	if catalog.GetModeByCode("") != nil {
		t.Error("GetModeByCode('') should return nil")
	}
}

func TestModeCatalog_ListByTier(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "core1", Name: "Core 1", Category: CategoryFormal, ShortDesc: "Test", Code: "A1", Tier: TierCore},
		{ID: "core2", Name: "Core 2", Category: CategoryAmpliative, ShortDesc: "Test", Code: "B1", Tier: TierCore},
		{ID: "adv1", Name: "Advanced 1", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1", Tier: TierAdvanced},
		{ID: "exp1", Name: "Exp 1", Category: CategoryMeta, ShortDesc: "Test", Code: "L1", Tier: TierExperimental},
		{ID: "no-tier", Name: "No Tier", Category: CategoryCausal, ShortDesc: "Test"},
	}

	catalog, err := NewModeCatalog(modes, "1.0")
	if err != nil {
		t.Fatalf("failed to create catalog: %v", err)
	}

	core := catalog.ListByTier(TierCore)
	if len(core) != 2 {
		t.Errorf("ListByTier(core) count = %d, want 2", len(core))
	}

	adv := catalog.ListByTier(TierAdvanced)
	if len(adv) != 1 {
		t.Errorf("ListByTier(advanced) count = %d, want 1", len(adv))
	}

	exp := catalog.ListByTier(TierExperimental)
	if len(exp) != 1 {
		t.Errorf("ListByTier(experimental) count = %d, want 1", len(exp))
	}

	// No modes with this tier
	empty := catalog.ListByTier(ModeTier("nonexistent"))
	if len(empty) != 0 {
		t.Errorf("ListByTier(nonexistent) count = %d, want 0", len(empty))
	}
}

func TestModeCatalog_ListDefault(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "core1", Name: "Core 1", Category: CategoryFormal, ShortDesc: "Test", Code: "A1", Tier: TierCore},
		{ID: "core2", Name: "Core 2", Category: CategoryAmpliative, ShortDesc: "Test", Code: "B1", Tier: TierCore},
		{ID: "adv1", Name: "Advanced", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1", Tier: TierAdvanced},
	}

	catalog, err := NewModeCatalog(modes, "1.0")
	if err != nil {
		t.Fatalf("failed to create catalog: %v", err)
	}

	defaults := catalog.ListDefault()
	if len(defaults) != 2 {
		t.Errorf("ListDefault() count = %d, want 2", len(defaults))
	}
	for _, m := range defaults {
		if m.Tier != TierCore {
			t.Errorf("ListDefault() returned mode with tier %q, want %q", m.Tier, TierCore)
		}
	}
}

// =============================================================================
// ModeRef Tests
// =============================================================================

func TestModeRef_Resolve_ByID(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	ref := ModeRefFromID("deductive")
	id, err := ref.Resolve(catalog)
	if err != nil {
		t.Fatalf("Resolve by ID failed: %v", err)
	}
	if id != "deductive" {
		t.Errorf("Resolve() = %q, want %q", id, "deductive")
	}
}

func TestModeRef_Resolve_ByCode(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	ref := ModeRefFromCode("C1")
	id, err := ref.Resolve(catalog)
	if err != nil {
		t.Fatalf("Resolve by code failed: %v", err)
	}
	if id != "bayesian" {
		t.Errorf("Resolve() = %q, want %q", id, "bayesian")
	}
}

func TestModeRef_Resolve_BothSet(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Code: "A1"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	ref := ModeRef{ID: "deductive", Code: "A1"}
	_, err := ref.Resolve(catalog)
	if err == nil {
		t.Error("Resolve with both ID and Code set should fail")
	}
}

func TestModeRef_Resolve_NeitherSet(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	ref := ModeRef{}
	_, err := ref.Resolve(catalog)
	if err == nil {
		t.Error("Resolve with neither ID nor Code should fail")
	}
}

func TestModeRef_Resolve_NotFound(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	// Unknown ID
	ref := ModeRefFromID("nonexistent")
	_, err := ref.Resolve(catalog)
	if err == nil {
		t.Error("Resolve with unknown ID should fail")
	}

	// Unknown code
	ref = ModeRefFromCode("Z9")
	_, err = ref.Resolve(catalog)
	if err == nil {
		t.Error("Resolve with unknown code should fail")
	}
}

func TestModeRef_String(t *testing.T) {
	if got := ModeRefFromID("deductive").String(); got != "deductive" {
		t.Errorf("ModeRefFromID.String() = %q, want %q", got, "deductive")
	}
	if got := ModeRefFromCode("A1").String(); got != "code:A1" {
		t.Errorf("ModeRefFromCode.String() = %q, want %q", got, "code:A1")
	}
}

func TestResolveModeRefs(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Code: "A1"},
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	// Mix of ID and code refs
	refs := []ModeRef{
		ModeRefFromID("deductive"),
		ModeRefFromCode("C1"),
	}
	ids, err := ResolveModeRefs(refs, catalog)
	if err != nil {
		t.Fatalf("ResolveModeRefs failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ids))
	}
	if ids[0] != "deductive" || ids[1] != "bayesian" {
		t.Errorf("got %v, want [deductive bayesian]", ids)
	}
}

func TestResolveModeRefs_Duplicate(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Code: "A1"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	refs := []ModeRef{
		ModeRefFromID("deductive"),
		ModeRefFromCode("A1"), // resolves to same ID
	}
	_, err := ResolveModeRefs(refs, catalog)
	if err == nil {
		t.Error("ResolveModeRefs with duplicates should fail")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

func TestResolveModeRefs_Empty(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	ids, err := ResolveModeRefs(nil, catalog)
	if err != nil {
		t.Fatalf("ResolveModeRefs(nil) should not error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ResolveModeRefs(nil) = %v, want empty", ids)
	}
}

// =============================================================================
// CacheConfig Tests
// =============================================================================

func TestDefaultCacheConfig(t *testing.T) {
	cfg := DefaultCacheConfig()
	if !cfg.Enabled {
		t.Error("default cache should be enabled")
	}
	if cfg.TTL <= 0 {
		t.Error("TTL should be positive")
	}
	if cfg.MaxEntries <= 0 {
		t.Error("MaxEntries should be positive")
	}
	if !cfg.ShareAcrossModes {
		t.Error("ShareAcrossModes should be true by default")
	}
}

func TestCacheConfig_ZeroValue(t *testing.T) {
	var cfg CacheConfig
	if cfg.Enabled {
		t.Error("zero-value cache should not be enabled")
	}
	if cfg.TTL != 0 {
		t.Errorf("zero-value TTL = %v, want 0", cfg.TTL)
	}
	if cfg.MaxEntries != 0 {
		t.Errorf("zero-value MaxEntries = %d, want 0", cfg.MaxEntries)
	}
}

// =============================================================================
// AgentDistribution Tests
// =============================================================================

func TestDefaultAgentDistribution(t *testing.T) {
	dist := DefaultAgentDistribution()
	if dist.Strategy != "one-per-agent" {
		t.Errorf("default Strategy = %q, want %q", dist.Strategy, "one-per-agent")
	}
	if dist.MaxAgents != 0 {
		t.Errorf("default MaxAgents = %d, want 0", dist.MaxAgents)
	}
	if dist.PreferredAgentType != "" {
		t.Errorf("default PreferredAgentType = %q, want empty", dist.PreferredAgentType)
	}
}

func TestAgentDistribution_ZeroValue(t *testing.T) {
	var dist AgentDistribution
	if dist.Strategy != "" {
		t.Errorf("zero-value Strategy = %q, want empty", dist.Strategy)
	}
}

// =============================================================================
// EnsemblePreset Serialization Tests
// =============================================================================

func TestEnsemblePreset_JSONRoundTrip(t *testing.T) {
	preset := EnsemblePreset{
		Name:          "test-preset",
		DisplayName:   "Test Preset",
		Description:   "A test preset for roundtrip",
		Modes:         []ModeRef{ModeRefFromID("deductive"), ModeRefFromCode("C1")},
		Synthesis:     DefaultSynthesisConfig(),
		Budget:        DefaultBudgetConfig(),
		Cache:         DefaultCacheConfig(),
		AllowAdvanced: true,
		AgentDistribution: &AgentDistribution{
			Strategy:           "round-robin",
			MaxAgents:          4,
			PreferredAgentType: "cc",
		},
		Tags: []string{"test", "roundtrip"},
	}

	data, err := json.Marshal(preset)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded EnsemblePreset
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != preset.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, preset.Name)
	}
	if decoded.DisplayName != preset.DisplayName {
		t.Errorf("DisplayName = %q, want %q", decoded.DisplayName, preset.DisplayName)
	}
	if len(decoded.Modes) != 2 {
		t.Fatalf("Modes count = %d, want 2", len(decoded.Modes))
	}
	if decoded.Modes[0].ID != "deductive" {
		t.Errorf("Modes[0].ID = %q, want %q", decoded.Modes[0].ID, "deductive")
	}
	if decoded.Modes[1].Code != "C1" {
		t.Errorf("Modes[1].Code = %q, want %q", decoded.Modes[1].Code, "C1")
	}
	if !decoded.AllowAdvanced {
		t.Error("AllowAdvanced should be true")
	}
	if decoded.AgentDistribution == nil {
		t.Fatal("AgentDistribution should not be nil")
	}
	if decoded.AgentDistribution.Strategy != "round-robin" {
		t.Errorf("AgentDistribution.Strategy = %q, want %q", decoded.AgentDistribution.Strategy, "round-robin")
	}
	if decoded.Cache.TTL != preset.Cache.TTL {
		t.Errorf("Cache.TTL = %v, want %v", decoded.Cache.TTL, preset.Cache.TTL)
	}
}

func TestEnsemblePreset_ZeroValues(t *testing.T) {
	// Zero-value preset should serialize cleanly
	var preset EnsemblePreset
	data, err := json.Marshal(preset)
	if err != nil {
		t.Fatalf("Marshal zero preset failed: %v", err)
	}

	var decoded EnsemblePreset
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal zero preset failed: %v", err)
	}

	if decoded.Name != "" {
		t.Errorf("zero Name = %q, want empty", decoded.Name)
	}
	if decoded.AllowAdvanced {
		t.Error("zero AllowAdvanced should be false")
	}
	if decoded.AgentDistribution != nil {
		t.Error("zero AgentDistribution should be nil")
	}
	if len(decoded.Tags) != 0 {
		t.Errorf("zero Tags = %v, want nil", decoded.Tags)
	}
}

func TestEnsemblePreset_ResolveIDs(t *testing.T) {
	modes := []ReasoningMode{
		{ID: "deductive", Name: "Deductive", Category: CategoryFormal, ShortDesc: "Test", Code: "A1"},
		{ID: "bayesian", Name: "Bayesian", Category: CategoryUncertainty, ShortDesc: "Test", Code: "C1"},
	}
	catalog, _ := NewModeCatalog(modes, "1.0")

	preset := EnsemblePreset{
		Name:  "test",
		Modes: []ModeRef{ModeRefFromCode("A1"), ModeRefFromID("bayesian")},
	}

	ids, err := preset.ResolveIDs(catalog)
	if err != nil {
		t.Fatalf("ResolveIDs failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ids))
	}
	if ids[0] != "deductive" {
		t.Errorf("ids[0] = %q, want %q", ids[0], "deductive")
	}
	if ids[1] != "bayesian" {
		t.Errorf("ids[1] = %q, want %q", ids[1], "bayesian")
	}
}

func TestModeRef_JSONRoundTrip(t *testing.T) {
	refs := []ModeRef{
		ModeRefFromID("deductive"),
		ModeRefFromCode("C1"),
		{}, // empty
	}

	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded []ModeRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded) != 3 {
		t.Fatalf("decoded count = %d, want 3", len(decoded))
	}
	if decoded[0].ID != "deductive" {
		t.Errorf("decoded[0].ID = %q, want %q", decoded[0].ID, "deductive")
	}
	if decoded[1].Code != "C1" {
		t.Errorf("decoded[1].Code = %q, want %q", decoded[1].Code, "C1")
	}
	if decoded[2].ID != "" || decoded[2].Code != "" {
		t.Errorf("decoded[2] should be empty, got %+v", decoded[2])
	}
}

func TestCacheConfig_JSONRoundTrip(t *testing.T) {
	cfg := DefaultCacheConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded CacheConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Enabled != cfg.Enabled {
		t.Errorf("Enabled = %v, want %v", decoded.Enabled, cfg.Enabled)
	}
	if decoded.TTL != cfg.TTL {
		t.Errorf("TTL = %v, want %v", decoded.TTL, cfg.TTL)
	}
	if decoded.MaxEntries != cfg.MaxEntries {
		t.Errorf("MaxEntries = %d, want %d", decoded.MaxEntries, cfg.MaxEntries)
	}
	if decoded.ShareAcrossModes != cfg.ShareAcrossModes {
		t.Errorf("ShareAcrossModes = %v, want %v", decoded.ShareAcrossModes, cfg.ShareAcrossModes)
	}
}

// =============================================================================
// DefaultCatalog / EmbeddedModes Tests
// =============================================================================

func TestDefaultCatalog_Creates(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	if catalog == nil {
		t.Fatal("DefaultCatalog() returned nil")
	}
	if catalog.Version() != CatalogVersion {
		t.Errorf("catalog.Version() = %q, want %q", catalog.Version(), CatalogVersion)
	}
}

func TestDefaultCatalog_Has80Modes(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	if catalog.Count() != 80 {
		t.Errorf("DefaultCatalog has %d modes, want 80", catalog.Count())
	}
}

func TestDefaultCatalog_CoreTierCount(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	core := catalog.ListByTier(TierCore)
	if len(core) != 28 {
		t.Errorf("DefaultCatalog core tier has %d modes, want 28", len(core))
	}
}

func TestDefaultCatalog_AdvancedTierCount(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	advanced := catalog.ListByTier(TierAdvanced)
	if len(advanced) != 52 {
		t.Errorf("DefaultCatalog advanced tier has %d modes, want 52", len(advanced))
	}
}

func TestDefaultCatalog_AllModesValidate(t *testing.T) {
	for i, m := range EmbeddedModes {
		if err := m.Validate(); err != nil {
			t.Errorf("EmbeddedModes[%d] (%q) validation failed: %v", i, m.ID, err)
		}
	}
}

func TestDefaultCatalog_UniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, m := range EmbeddedModes {
		if seen[m.ID] {
			t.Errorf("duplicate mode ID: %q", m.ID)
		}
		seen[m.ID] = true
	}
}

func TestDefaultCatalog_UniqueCodes(t *testing.T) {
	seen := make(map[string]string) // code -> ID
	for _, m := range EmbeddedModes {
		if m.Code == "" {
			t.Errorf("mode %q has empty code", m.ID)
			continue
		}
		if prevID, exists := seen[m.Code]; exists {
			t.Errorf("duplicate code %q: used by both %q and %q", m.Code, prevID, m.ID)
		}
		seen[m.Code] = m.ID
	}
}

func TestDefaultCatalog_CategoryLetterConsistency(t *testing.T) {
	for _, m := range EmbeddedModes {
		if m.Code == "" {
			continue
		}
		// Extract the letter from the code
		codeLetter := string(m.Code[0])
		expectedLetter := m.Category.CategoryLetter()
		if codeLetter != expectedLetter {
			t.Errorf("mode %q: code %q starts with %q but category %q maps to letter %q",
				m.ID, m.Code, codeLetter, m.Category, expectedLetter)
		}
	}
}

func TestDefaultCatalog_AllCategoriesPresent(t *testing.T) {
	categories := make(map[ModeCategory]int)
	for _, m := range EmbeddedModes {
		categories[m.Category]++
	}

	expected := []ModeCategory{
		CategoryFormal, CategoryAmpliative, CategoryUncertainty,
		CategoryVagueness, CategoryChange, CategoryCausal,
		CategoryPractical, CategoryStrategic, CategoryDialectical,
		CategoryModal, CategoryDomain, CategoryMeta,
	}
	for _, cat := range expected {
		if categories[cat] == 0 {
			t.Errorf("category %q has no modes in the catalog", cat)
		}
	}
}

func TestDefaultCatalog_GetByCodeMatchesID(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	for _, m := range EmbeddedModes {
		if m.Code == "" {
			continue
		}
		found := catalog.GetModeByCode(m.Code)
		if found == nil {
			t.Errorf("GetModeByCode(%q) returned nil for mode %q", m.Code, m.ID)
			continue
		}
		if found.ID != m.ID {
			t.Errorf("GetModeByCode(%q).ID = %q, want %q", m.Code, found.ID, m.ID)
		}
	}
}

func TestDefaultCatalog_ListDefaultIsCore(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	defaults := catalog.ListDefault()
	for _, m := range defaults {
		if m.Tier != TierCore {
			t.Errorf("ListDefault() includes mode %q with tier %q, want %q", m.ID, m.Tier, TierCore)
		}
	}
	// ListDefault should match ListByTier(TierCore)
	core := catalog.ListByTier(TierCore)
	if len(defaults) != len(core) {
		t.Errorf("ListDefault() count = %d, ListByTier(core) count = %d, want equal", len(defaults), len(core))
	}
}
