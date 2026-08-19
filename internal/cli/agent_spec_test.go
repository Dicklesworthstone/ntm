package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
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

// ntm#140: the N:model:effort form threads a reasoning-effort hint
// through to the agent template.
func TestParseAgentSpec_ReasoningEffort(t *testing.T) {
	tests := []struct {
		value       string
		wantCount   int
		wantModel   string
		wantEffort  string
		expectError bool
	}{
		{"1", 1, "", "", false},
		{"2:gpt-5", 2, "gpt-5", "", false},
		{"1:gpt-5:medium", 1, "gpt-5", "medium", false},
		{"3:gpt-5-codex:xhigh", 3, "gpt-5-codex", "xhigh", false},
		{"1:gpt-5:", 1, "", "", true}, // empty effort segment rejected
		{"1:gpt-5:bad effort", 1, "", "", true},
		{"1:gpt-5:$(pwn)", 1, "", "", true},
	}
	for _, tt := range tests {
		spec, err := ParseAgentSpec(tt.value)
		if tt.expectError {
			if err == nil {
				t.Fatalf("expected error for %q; got spec=%+v", tt.value, spec)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", tt.value, err)
		}
		if spec.Count != tt.wantCount {
			t.Errorf("%q: count=%d want %d", tt.value, spec.Count, tt.wantCount)
		}
		if spec.Model != tt.wantModel {
			t.Errorf("%q: model=%q want %q", tt.value, spec.Model, tt.wantModel)
		}
		if spec.ReasoningEffort != tt.wantEffort {
			t.Errorf("%q: effort=%q want %q", tt.value, spec.ReasoningEffort, tt.wantEffort)
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

// TestResolveAgentModel_Precedence verifies the three-tier precedence for
// plugin-aware model resolution (ntm#203): explicit spec model, then the global
// config default for the agent type, then the plugin's declared default.
func TestResolveAgentModel_Precedence(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	hermes := plugins.AgentPlugin{Name: "hermes"}
	hermes.Defaults.Model = "google/gemini-2.5-flash"
	// A plugin entry keyed to a built-in type proves the global default wins
	// over a plugin default (this branch is never reached for built-ins).
	ccPlugin := plugins.AgentPlugin{Name: "cc"}
	ccPlugin.Defaults.Model = "plugin/should-not-win"
	pluginMap := map[string]plugins.AgentPlugin{
		"hermes": hermes,
		"cc":     ccPlugin,
	}

	// 1. Explicit model on the spec wins over the plugin default.
	if got := resolveAgentModel(AgentType("hermes"), "anthropic/claude-opus-4", pluginMap); got != "anthropic/claude-opus-4" {
		t.Errorf("explicit model: got %q, want %q", got, "anthropic/claude-opus-4")
	}

	// 2. Global config default for a built-in type wins over any plugin default.
	wantDefault := cfg.Models.DefaultClaude
	if wantDefault == "" {
		t.Fatal("expected a non-empty default claude model in config.Default()")
	}
	if got := resolveAgentModel(AgentTypeClaude, "", pluginMap); got != wantDefault {
		t.Errorf("global default: got %q, want %q", got, wantDefault)
	}

	// 3. Plugin default is used when there is no explicit model and no global
	//    default (the bare `--hermes=1` case that previously spawned empty).
	if got := resolveAgentModel(AgentType("hermes"), "", pluginMap); got != "google/gemini-2.5-flash" {
		t.Errorf("plugin default: got %q, want %q", got, "google/gemini-2.5-flash")
	}

	// 4. An agent type absent from the plugin map yields empty.
	if got := resolveAgentModel(AgentType("nonexistent"), "", pluginMap); got != "" {
		t.Errorf("missing plugin: got %q, want empty", got)
	}

	// A nil plugin map also yields empty for an otherwise-unknown plugin type.
	if got := resolveAgentModel(AgentType("hermes"), "", nil); got != "" {
		t.Errorf("nil plugin map: got %q, want empty", got)
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
		{"claude alias opus", AgentTypeClaude, "opus", "claude-opus-4-8"},
		{"claude alias sonnet", AgentTypeClaude, "sonnet", "claude-sonnet-4-6"},
		{"codex alias o3", AgentTypeCodex, "o3", "o3"},
		{"gemini alias flash", AgentTypeGemini, "flash", "gemini-3-flash"},
		{"grok exact account model", AgentTypeGrok, "account-current", "account-current"},
		{"grok default delegated", AgentTypeGrok, "", ""},
		{"unknown alias passthrough", AgentTypeClaude, "unknown-custom", "unknown-custom"},
		{"claude default", AgentTypeClaude, "", "claude-opus-4-8"},
		{"codex default", AgentTypeCodex, "", config.DefaultCodexModel},
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

	if v.Type() != "N[:model[:effort]]" {
		t.Errorf("Type() = %q, want N[:model[:effort]]", v.Type())
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

// TestAgentSpecsValue_AgyRelaxedModelCharset covers sharp edge #2 (ntm#210):
// Antigravity display model names contain spaces and parentheses (e.g.
// "Gemini 3.7 Flash (High)"), which the default model charset rejects. The agy
// flag path must accept them via the relaxed charset, while the strict charset
// still applies to every other agent type.
func TestAgentSpecsValue_AgyRelaxedModelCharset(t *testing.T) {
	// agy accepts the spaced/parenthesized display name.
	var agySpecs AgentSpecs
	agy := NewAgentSpecsValue(AgentTypeAntigravity, &agySpecs)
	if err := agy.Set("1:Gemini 3.7 Flash (High)"); err != nil {
		t.Fatalf("agy Set(%q) unexpected error: %v", "1:Gemini 3.7 Flash (High)", err)
	}
	if len(agySpecs) != 1 {
		t.Fatalf("agy specs len = %d, want 1", len(agySpecs))
	}
	if agySpecs[0].Type != AgentTypeAntigravity {
		t.Errorf("Type = %s, want agy", agySpecs[0].Type)
	}
	if agySpecs[0].Count != 1 {
		t.Errorf("Count = %d, want 1", agySpecs[0].Count)
	}
	if agySpecs[0].Model != "Gemini 3.7 Flash (High)" {
		t.Errorf("Model = %q, want %q", agySpecs[0].Model, "Gemini 3.7 Flash (High)")
	}

	// A charset-safe alias also parses for agy.
	var aliasSpecs AgentSpecs
	if err := NewAgentSpecsValue(AgentTypeAntigravity, &aliasSpecs).Set("2:gemini-3.7-flash-high"); err != nil {
		t.Errorf("agy Set(alias) unexpected error: %v", err)
	}

	// The SAME spaced model name is REJECTED for a non-agy type (strict charset).
	var geminiSpecs AgentSpecs
	if err := NewAgentSpecsValue(AgentTypeGemini, &geminiSpecs).Set("1:Gemini 3.7 Flash (High)"); err == nil {
		t.Error("gemini Set with spaced model should fail the strict charset, but succeeded")
	}

	// Even under the relaxed agy charset, shell metacharacters remain rejected
	// (defense-in-depth against injection).
	for _, bad := range []string{"1:a;b", "1:a|b", "1:a$b", "1:a`b`", "1:a&&b"} {
		var s AgentSpecs
		if err := NewAgentSpecsValue(AgentTypeAntigravity, &s).Set(bad); err == nil {
			t.Errorf("agy Set(%q) should be rejected (shell metachar), but succeeded", bad)
		}
	}
}

// Tests for the model@effort variant suffix (ntm-mjf7).
func TestParseAgentSpecEffortAtSuffix(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantCount  int
		wantModel  string
		wantEffort string
		wantErr    string
	}{
		{name: "model with effort suffix", input: "2:gpt-5.6-terra@high", wantCount: 2, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "colon effort still works", input: "2:gpt-5.6-terra:high", wantCount: 2, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "bare model unchanged", input: "3:opus", wantCount: 3, wantModel: "opus"},
		{name: "empty effort after at", input: "1:gpt-5.6-terra@", wantErr: "empty reasoning effort"},
		{name: "empty model before at", input: "1:@high", wantErr: "empty model"},
		{name: "both at and colon effort", input: "1:model@high:ultra", wantErr: "twice"},
		{name: "effort with slash rejected", input: "1:model@hi/gh", wantErr: "invalid characters in reasoning effort"},
		{name: "colon effort with slash rejected", input: "1:model:hi/gh", wantErr: "invalid characters in reasoning effort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseAgentSpec(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseAgentSpec(%q) expected error containing %q, got %+v", tc.input, tc.wantErr, spec)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAgentSpec(%q): %v", tc.input, err)
			}
			if spec.Count != tc.wantCount || spec.Model != tc.wantModel || spec.ReasoningEffort != tc.wantEffort {
				t.Errorf("ParseAgentSpec(%q) = {Count:%d Model:%q Effort:%q}, want {%d %q %q}",
					tc.input, spec.Count, spec.Model, spec.ReasoningEffort, tc.wantCount, tc.wantModel, tc.wantEffort)
			}
		})
	}
}

// The @effort shorthand must stay inert for agent types without an effort
// knob so '@' remains a literal model character (e.g. opencode provider
// model tags).
func TestEffortAtSuffixOnlyForEffortCapableTypes(t *testing.T) {
	var ocSpecs AgentSpecs
	oc := NewAgentSpecsValue(AgentTypeOpencode, &ocSpecs)
	if err := oc.Set("1:openrouter/some-model@beta"); err != nil {
		t.Fatalf("oc spec with literal @ rejected: %v", err)
	}
	if got := ocSpecs[len(ocSpecs)-1]; got.Model != "openrouter/some-model@beta" || got.ReasoningEffort != "" {
		t.Fatalf("oc spec split @ as effort: %+v", got)
	}

	var codSpecs AgentSpecs
	cod := NewAgentSpecsValue(AgentTypeCodex, &codSpecs)
	if err := cod.Set("1:gpt-5.6-terra@high"); err != nil {
		t.Fatalf("cod spec with @effort rejected: %v", err)
	}
	if got := codSpecs[len(codSpecs)-1]; got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "high" {
		t.Fatalf("cod spec did not split @ as effort: %+v", got)
	}
}
