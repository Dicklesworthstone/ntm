package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestParseAgentSpec_ModelValidation(t *testing.T) {
	tests := []struct {
		value       string
		expectError bool
	}{
		{"1:claude-3-opus", false},
		{"2:gpt-4.1", false},
		{"1:vendor/model@2025", false},
		{"1:bad model", true},
		{"1:$(touch /tmp/pwn)", true},
		{"1:;rm -rf /", true},
	}

	for _, tt := range tests {
		_, err := ParseAgentSpec(tt.value)
		if tt.expectError && err == nil {
			t.Fatalf("expected error for %q", tt.value)
		}
		if !tt.expectError && err != nil {
			t.Fatalf("unexpected error for %q: %v", tt.value, err)
		}
	}
}

// =============================================================================
// TotalCount
// =============================================================================

func TestAgentSpecs_TotalCount(t *testing.T) {

	tests := []struct {
		name  string
		specs AgentSpecs
		want  int
	}{
		{"empty", AgentSpecs{}, 0},
		{"single", AgentSpecs{{Count: 3}}, 3},
		{"multiple", AgentSpecs{{Count: 2}, {Count: 3}, {Count: 1}}, 6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.specs.TotalCount()
			if got != tc.want {
				t.Errorf("TotalCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

// =============================================================================
// ByType
// =============================================================================

func TestAgentSpecs_ByType(t *testing.T) {

	specs := AgentSpecs{
		{Type: AgentTypeClaude, Count: 2},
		{Type: AgentTypeCodex, Count: 1},
		{Type: AgentTypeClaude, Count: 3, Model: "opus"},
		{Type: AgentTypeGemini, Count: 1},
	}

	claude := specs.ByType(AgentTypeClaude)
	if len(claude) != 2 {
		t.Fatalf("ByType(Claude) len = %d, want 2", len(claude))
	}
	if claude.TotalCount() != 5 {
		t.Errorf("ByType(Claude) TotalCount = %d, want 5", claude.TotalCount())
	}

	codex := specs.ByType(AgentTypeCodex)
	if len(codex) != 1 {
		t.Errorf("ByType(Codex) len = %d, want 1", len(codex))
	}

	cursor := specs.ByType(AgentTypeCursor)
	if len(cursor) != 0 {
		t.Errorf("ByType(Cursor) len = %d, want 0", len(cursor))
	}
}

// =============================================================================
// Flatten
// =============================================================================

func TestAgentSpecs_Flatten(t *testing.T) {

	specs := AgentSpecs{
		{Type: AgentTypeClaude, Count: 2, Model: "sonnet"},
		{Type: AgentTypeCodex, Count: 1, Model: "o3"},
	}

	flat := specs.Flatten()

	if len(flat) != 3 {
		t.Fatalf("Flatten() len = %d, want 3", len(flat))
	}

	// First two should be Claude with indices 1 and 2
	if flat[0].Type != AgentTypeClaude || flat[0].Index != 1 || flat[0].Model != "sonnet" {
		t.Errorf("flat[0] = %+v, want Claude index 1 model sonnet", flat[0])
	}
	if flat[1].Type != AgentTypeClaude || flat[1].Index != 2 {
		t.Errorf("flat[1] = %+v, want Claude index 2", flat[1])
	}

	// Third should be Codex with index 1
	if flat[2].Type != AgentTypeCodex || flat[2].Index != 1 || flat[2].Model != "o3" {
		t.Errorf("flat[2] = %+v, want Codex index 1 model o3", flat[2])
	}
}

func TestAgentSpecs_Flatten_Empty(t *testing.T) {

	specs := AgentSpecs{}
	flat := specs.Flatten()
	if len(flat) != 0 {
		t.Errorf("Flatten() on empty specs = %d, want 0", len(flat))
	}
}

// =============================================================================
// ResolveModel / ValidateModelAlias
// =============================================================================

func TestResolveModel_Passthrough(t *testing.T) {

	// With an explicit model spec, ResolveModel should pass through unknown aliases
	got := ResolveModel(AgentTypeClaude, "my-custom-model-name")
	if got != "my-custom-model-name" {
		t.Errorf("ResolveModel with unknown alias = %q, want 'my-custom-model-name'", got)
	}
}

func TestResolveModel_EmptySpecReturnsDefault(t *testing.T) {

	// With empty modelSpec, result depends on cfg; either empty or a default
	got := ResolveModel(AgentTypeClaude, "")
	// Just verify it doesn't panic; the result depends on whether cfg is set
	_ = got
}

func TestValidateModelAlias_EmptyAlias(t *testing.T) {

	// Empty alias should always be valid (nothing to validate)
	err := ValidateModelAlias(AgentTypeClaude, "")
	if err != nil {
		t.Errorf("ValidateModelAlias(empty) returned error: %v", err)
	}
}

// =============================================================================
// ParseAgentSpec (extended)
// =============================================================================

func TestParseAgentSpec_ValidFormats(t *testing.T) {

	tests := []struct {
		name      string
		value     string
		wantCount int
		wantModel string
	}{
		{"count only", "3", 3, ""},
		{"count with model", "2:opus-4.5", 2, "opus-4.5"},
		{"single agent", "1", 1, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseAgentSpec(tc.value)
			if err != nil {
				t.Fatalf("ParseAgentSpec(%q) error: %v", tc.value, err)
			}
			if spec.Count != tc.wantCount {
				t.Errorf("Count = %d, want %d", spec.Count, tc.wantCount)
			}
			if spec.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", spec.Model, tc.wantModel)
			}
		})
	}
}

func TestParseAgentSpec_InvalidFormats(t *testing.T) {

	invalids := []string{
		"",    // empty
		"abc", // non-numeric count
		"0",   // zero count
		"-1",  // negative count
		"1:",  // empty model
	}

	for _, v := range invalids {
		t.Run(v, func(t *testing.T) {
			_, err := ParseAgentSpec(v)
			if err == nil {
				t.Errorf("ParseAgentSpec(%q) expected error, got nil", v)
			}
		})
	}
}

// =============================================================================
// ValidateModelAlias (with config)
// =============================================================================

func TestValidateModelAlias_KnownAlias(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// "opus" is a known Claude alias
	err := ValidateModelAlias(AgentTypeClaude, "opus")
	if err != nil {
		t.Errorf("ValidateModelAlias(claude, opus) returned error: %v", err)
	}

	// "gpt5" is a known Codex alias
	err = ValidateModelAlias(AgentTypeCodex, "gpt5")
	if err != nil {
		t.Errorf("ValidateModelAlias(codex, gpt5) returned error: %v", err)
	}

	// "pro" is a known Gemini alias
	err = ValidateModelAlias(AgentTypeGemini, "pro")
	if err != nil {
		t.Errorf("ValidateModelAlias(gemini, pro) returned error: %v", err)
	}
}

func TestValidateModelAlias_UnknownAlias(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	err := ValidateModelAlias(AgentTypeClaude, "nonexistent-model")
	if err == nil {
		t.Error("expected error for unknown Claude alias")
	}
	if !strings.Contains(err.Error(), "unknown model alias") {
		t.Errorf("error should mention 'unknown model alias': %v", err)
	}
	if !strings.Contains(err.Error(), "opus") {
		t.Errorf("error should list available aliases including 'opus': %v", err)
	}
}

func TestValidateModelAlias_UnknownAliasCodex(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	err := ValidateModelAlias(AgentTypeCodex, "does-not-exist")
	if err == nil {
		t.Error("expected error for unknown Codex alias")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should reference the alias: %v", err)
	}
}

func TestValidateModelAlias_UnknownAliasGemini(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	err := ValidateModelAlias(AgentTypeGemini, "invalid-gem")
	if err == nil {
		t.Error("expected error for unknown Gemini alias")
	}
}

func TestValidateModelAlias_NoAliasesConfigured(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()
	// Clear aliases to simulate no aliases configured
	cfg.Models.Claude = nil

	err := ValidateModelAlias(AgentTypeClaude, "some-alias")
	if err != nil {
		t.Errorf("expected nil error when no aliases configured, got: %v", err)
	}
}

func TestValidateModelAlias_NilConfig(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = nil

	err := ValidateModelAlias(AgentTypeClaude, "opus")
	if err != nil {
		t.Errorf("expected nil error with nil config, got: %v", err)
	}
}

func TestValidateModelAlias_UnknownAgentType(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Unknown agent type has no aliases → should return nil
	err := ValidateModelAlias(AgentTypeCursor, "some-model")
	if err != nil {
		t.Errorf("expected nil for unknown agent type (no aliases), got: %v", err)
	}
}

// =============================================================================
// ResolveModel (with config)
// =============================================================================

func TestResolveModel_WithConfig(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	tests := []struct {
		name      string
		agentType AgentType
		modelSpec string
		want      string
	}{
		{"claude alias opus", AgentTypeClaude, "opus", "claude-opus-4-6"},
		{"claude alias sonnet", AgentTypeClaude, "sonnet", "claude-sonnet-4-6"},
		{"codex alias o3", AgentTypeCodex, "o3", "o3"},
		{"gemini alias flash", AgentTypeGemini, "flash", "gemini-3-flash"},
		{"unknown alias passthrough", AgentTypeClaude, "unknown-custom", "unknown-custom"},
		{"claude default", AgentTypeClaude, "", "claude-opus-4-6"},
		{"codex default", AgentTypeCodex, "", "gpt-5.3-codex"},
		{"gemini default", AgentTypeGemini, "", "gemini-3-pro-preview"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveModel(tc.agentType, tc.modelSpec)
			if got != tc.want {
				t.Errorf("ResolveModel(%s, %q) = %q, want %q",
					tc.agentType, tc.modelSpec, got, tc.want)
			}
		})
	}
}

func TestResolveModel_EmptySpecUnknownType(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Unknown agent type with empty spec returns ""
	got := ResolveModel(AgentTypeCursor, "")
	if got != "" {
		t.Errorf("expected empty string for unknown agent type, got %q", got)
	}
}

// =============================================================================
// expandPromptTemplate
// =============================================================================

func TestExpandPromptTemplate_Impl(t *testing.T) {
	result := expandPromptTemplate("bd-123", "Fix login bug", "impl", "")
	if !strings.Contains(result, "bd-123") {
		t.Errorf("impl template should contain bead ID: %s", result)
	}
	if !strings.Contains(result, "Fix login bug") {
		t.Errorf("impl template should contain title: %s", result)
	}
	if !strings.Contains(result, "br dep tree") {
		t.Errorf("impl template should mention dep tree: %s", result)
	}
}

func TestExpandPromptTemplate_Review(t *testing.T) {
	result := expandPromptTemplate("bd-456", "Audit auth", "review", "")
	if !strings.Contains(result, "bd-456") {
		t.Errorf("review template should contain bead ID: %s", result)
	}
	if !strings.Contains(result, "Review and verify") {
		t.Errorf("review template should contain review language: %s", result)
	}
}

func TestExpandPromptTemplate_Default(t *testing.T) {
	result := expandPromptTemplate("bd-789", "Some task", "unknown-template", "")
	if !strings.Contains(result, "bd-789") {
		t.Errorf("default template should contain bead ID: %s", result)
	}
	if !strings.Contains(result, "Some task") {
		t.Errorf("default template should contain title: %s", result)
	}
}

func TestExpandPromptTemplate_CustomWithFile(t *testing.T) {
	dir := t.TempDir()
	tmplFile := filepath.Join(dir, "custom.txt")
	os.WriteFile(tmplFile, []byte("Custom: {BEAD_ID} - {TITLE}"), 0644)

	result := expandPromptTemplate("bd-abc", "My feature", "custom", tmplFile)
	if result != "Custom: bd-abc - My feature" {
		t.Errorf("custom file template = %q, want 'Custom: bd-abc - My feature'", result)
	}
}

func TestExpandPromptTemplate_CustomWithMissingFile(t *testing.T) {
	result := expandPromptTemplate("bd-def", "Fallback task", "custom", "/nonexistent/path.txt")
	// Should fall back to impl-like template
	if !strings.Contains(result, "bd-def") {
		t.Errorf("fallback template should contain bead ID: %s", result)
	}
	if !strings.Contains(result, "Fallback task") {
		t.Errorf("fallback template should contain title: %s", result)
	}
}

func TestExpandPromptTemplate_CustomNoFile(t *testing.T) {
	result := expandPromptTemplate("bd-ghi", "Plain custom", "custom", "")
	if !strings.Contains(result, "bd-ghi") {
		t.Errorf("custom no-file template should contain bead ID: %s", result)
	}
	if !strings.Contains(result, "Plain custom") {
		t.Errorf("custom no-file template should contain title: %s", result)
	}
}

func TestExpandPromptTemplate_CaseInsensitive(t *testing.T) {
	result := expandPromptTemplate("bd-x", "Title", "IMPL", "")
	if !strings.Contains(result, "br dep tree") {
		t.Errorf("IMPL (uppercase) should resolve to impl template: %s", result)
	}

	result = expandPromptTemplate("bd-y", "Title", "Review", "")
	if !strings.Contains(result, "Review and verify") {
		t.Errorf("Review (mixed case) should resolve to review template: %s", result)
	}
}

// =============================================================================
// AgentSpecs.String()
// =============================================================================

func TestAgentSpecs_String(t *testing.T) {
	tests := []struct {
		name  string
		specs AgentSpecs
		want  string
	}{
		{"nil specs", nil, ""},
		{"empty specs", AgentSpecs{}, ""},
		{"count only", AgentSpecs{{Count: 3}}, "3"},
		{"with model", AgentSpecs{{Count: 2, Model: "opus"}}, "2:opus"},
		{"multiple", AgentSpecs{{Count: 1}, {Count: 3, Model: "fast"}}, "1,3:fast"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if tc.specs == nil {
				var s *AgentSpecs
				got = s.String()
			} else {
				got = tc.specs.String()
			}
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// =============================================================================
// agentSpecsValue
// =============================================================================

func TestAgentSpecsValue_SetWithType(t *testing.T) {
	var specs AgentSpecs
	v := NewAgentSpecsValue(AgentTypeClaude, &specs)

	if v.Type() != "N[:model]" {
		t.Errorf("Type() = %q, want N[:model]", v.Type())
	}

	err := v.Set("2:opus")
	if err != nil {
		t.Fatalf("Set error: %v", err)
	}

	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Type != AgentTypeClaude {
		t.Errorf("Type = %s, want cc", specs[0].Type)
	}
	if specs[0].Count != 2 {
		t.Errorf("Count = %d, want 2", specs[0].Count)
	}
	if specs[0].Model != "opus" {
		t.Errorf("Model = %q, want opus", specs[0].Model)
	}
}

func TestAgentSpecsValue_SetInvalid(t *testing.T) {
	var specs AgentSpecs
	v := NewAgentSpecsValue(AgentTypeCodex, &specs)

	err := v.Set("invalid")
	if err == nil {
		t.Error("expected error for invalid spec")
	}
}

func TestAgentSpecsValue_StringDelegatesToSpecs(t *testing.T) {
	specs := AgentSpecs{{Count: 5, Model: "turbo"}}
	v := NewAgentSpecsValue(AgentTypeCodex, &specs)

	got := v.String()
	if got != "5:turbo" {
		t.Errorf("String() = %q, want '5:turbo'", got)
	}
}
