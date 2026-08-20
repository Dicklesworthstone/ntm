package models

import "testing"

func TestGetContextLimit_ExactMatch(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4", 200000},
		{"claude-sonnet-4-5", 200000},
		{"gpt-4", 128000},
		{"gpt-5", 256000},
		{"gpt-5-codex", 256000},
		{"gemini-2.0-flash", 1000000},
		{"gemini-pro", 32000},
		{"o3-mini", 200000},
		{"o4-mini", 128000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_Alias(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"opus", 200000},
		{"sonnet", 200000},
		{"haiku", 200000},
		{"gpt4", 128000},
		{"codex", 256000},
		{"gemini", 1000000},
		{"pro", 1000000},
		{"flash", 1000000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_DateSuffix(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-20260101", 200000},
		{"gpt-4o-20250901", 128000},
		{"claude-sonnet-4-5-20260315", 200000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_CaseInsensitive(t *testing.T) {
	got := GetContextLimit("Claude-Opus-4")
	if got != 200000 {
		t.Errorf("GetContextLimit(\"Claude-Opus-4\") = %d, want 200000", got)
	}
}

func TestGetContextLimit_PrefixMatch(t *testing.T) {
	// "gpt-5.3-codex" should prefix-match "gpt-5"
	got := GetContextLimit("gpt-5.3-codex")
	if got != 256000 {
		t.Errorf("GetContextLimit(\"gpt-5.3-codex\") = %d, want 256000", got)
	}
}

func TestGetContextLimit_Unknown(t *testing.T) {
	got := GetContextLimit("unknown-model-xyz")
	if got != DefaultContextLimit {
		t.Errorf("GetContextLimit(\"unknown-model-xyz\") = %d, want %d", got, DefaultContextLimit)
	}
}

func TestGetContextLimit_Empty(t *testing.T) {
	got := GetContextLimit("")
	if got != DefaultContextLimit {
		t.Errorf("GetContextLimit(\"\") = %d, want %d", got, DefaultContextLimit)
	}
}

func TestGetTokenBudget(t *testing.T) {
	tests := []struct {
		agentType string
		wantMin   int
		wantMax   int
	}{
		{"cc", 170000, 190000},           // 90% of 200K = 180K
		{"claude", 170000, 190000},       // Alias should canonicalize to cc
		{"cod", 220000, 260000},          // 94% of 256K ≈ 240K
		{"codex", 220000, 260000},        // Alias should canonicalize to cod
		{"openai-codex", 220000, 260000}, // Long-form alias should canonicalize to cod
		{"gmi", 90000, 110000},           // 10% of 1M = 100K
		{"gemini", 90000, 110000},        // Alias should canonicalize to gmi
		{"google-gemini", 90000, 110000}, // Long-form alias should canonicalize to gmi
		{"unknown", 90000, 110000},       // Default
	}
	for _, tt := range tests {
		got := GetTokenBudget(tt.agentType)
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("GetTokenBudget(%q) = %d, want [%d, %d]", tt.agentType, got, tt.wantMin, tt.wantMax)
		}
	}
}

// A non-positive context limit becomes a division by zero in every usage
// computation, and the resulting NaN/+Inf cannot be JSON-encoded — which
// replaced whole robot responses with an empty stdout and a nonzero exit.
func TestApplyOverridesRejectsNonPositiveLimits(t *testing.T) {
	original := GetContextLimit("claude-opus-4")
	if original <= 0 {
		t.Fatalf("built-in limit for claude-opus-4 = %d, want > 0", original)
	}
	t.Cleanup(func() { ApplyOverrides(map[string]int{"claude-opus-4": original}) })

	for _, bad := range []int{0, -1} {
		ApplyOverrides(map[string]int{"claude-opus-4": bad})
		if got := GetContextLimit("claude-opus-4"); got != original {
			t.Fatalf("override %d was applied: limit = %d, want the built-in %d", bad, got, original)
		}
	}

	// A valid override still takes effect.
	ApplyOverrides(map[string]int{"claude-opus-4": 12345})
	if got := GetContextLimit("claude-opus-4"); got != 12345 {
		t.Fatalf("valid override not applied: limit = %d, want 12345", got)
	}
}

func TestKnownModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"", false},
		{"claude-opus-5", true},          // exact
		{"opus-5", true},                 // alias
		{"Claude-Opus-5", true},          // case-insensitive
		{"claude-opus-5-20260101", true}, // date suffix strip
		{"claude-opus-4-8", true},        // prefix match on claude-opus-4
		{"claude-opus5", false},          // typo: not resolvable
		{"totally-custom-model", false},
	}
	for _, tt := range tests {
		if got := KnownModel(tt.model); got != tt.want {
			t.Errorf("KnownModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestSuggestModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"", ""},
		{"claude-opus-5", ""}, // known: no suggestion
		{"opus-5", ""},        // known alias: no suggestion
		{"claude-opus5", "claude-opus-5"},
		{"claude-sonet-5", "claude-sonnet-5"},
		{"gpt5-codex", "gpt-5-codex"},
		{"fabel-5", "claude-fable-5"},               // alias near-miss resolves to canonical
		{"totally-unrelated-model-name-xyz-42", ""}, // no confident match
	}
	for _, tt := range tests {
		if got := SuggestModel(tt.model); got != tt.want {
			t.Errorf("SuggestModel(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestSuggestModelDeterministicTies(t *testing.T) {
	// Repeated calls must return the same suggestion despite map iteration
	// order (ties break by longest common prefix, then lexicographically).
	first := SuggestModel("claude-opus5")
	for i := 0; i < 20; i++ {
		if got := SuggestModel("claude-opus5"); got != first {
			t.Fatalf("SuggestModel non-deterministic: %q then %q", first, got)
		}
	}
}
