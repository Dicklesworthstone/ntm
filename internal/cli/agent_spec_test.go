package cli

import "testing"

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
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	// With an explicit model spec, ResolveModel should pass through unknown aliases
	got := ResolveModel(AgentTypeClaude, "my-custom-model-name")
	if got != "my-custom-model-name" {
		t.Errorf("ResolveModel with unknown alias = %q, want 'my-custom-model-name'", got)
	}
}

func TestResolveModel_EmptySpecReturnsDefault(t *testing.T) {
	t.Parallel()

	// With empty modelSpec, result depends on cfg; either empty or a default
	got := ResolveModel(AgentTypeClaude, "")
	// Just verify it doesn't panic; the result depends on whether cfg is set
	_ = got
}

func TestValidateModelAlias_EmptyAlias(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()

	invalids := []string{
		"",       // empty
		"abc",    // non-numeric count
		"0",      // zero count
		"-1",     // negative count
		"1:",     // empty model
	}

	for _, v := range invalids {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			_, err := ParseAgentSpec(v)
			if err == nil {
				t.Errorf("ParseAgentSpec(%q) expected error, got nil", v)
			}
		})
	}
}
