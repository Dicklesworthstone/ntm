package models

import "testing"

func TestGetContextLimit_ExactMatch(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4", 200000},
		{"claude-sonnet-4-5", 200000},
		{"claude-haiku-4-5", 200000},
		// Current Claude models run a 1M window (GH #338).
		{"claude-opus-5-5", 1000000},
		{"claude-sonnet-5-5", 1000000},
		{"claude-fable-5-1", 1000000},
		{"claude-opus-4-8", 1000000},
		{"claude-sonnet-4-6", 1000000},
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

func TestProviderQualifiedContextLimits(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  int
	}{
		{"anthropic/claude-opus-4-6", 1000000},
		{"openrouter/anthropic/claude-opus-4-6", 1000000},
		{"openrouter/anthropic/claude-opus-4-6-20260101", 1000000},
		{"anthropic/opus-4.6", 1000000},
		{"openai/gpt-5.3-codex", 256000},
		{"google/gemini-pro", 32000},
		{"  OpenRouter/Anthropic/Claude-Opus-4-6  ", 1000000},
	} {
		t.Run(tt.model, func(t *testing.T) {
			if !KnownModel(tt.model) || GetContextLimit(tt.model) != tt.want {
				t.Fatalf("qualified model %q: known=%t limit=%d, want known limit=%d", tt.model, KnownModel(tt.model), GetContextLimit(tt.model), tt.want)
			}
			budget, err := GetTokenBudgetForModel("omp", tt.model, 0)
			if err != nil || budget != tt.want*78/100 {
				t.Fatalf("qualified OMP budget = %d, %v; want %d", budget, err, tt.want*78/100)
			}
		})
	}
	for _, model := range []string{
		"anthropic/unknown-model", "openrouter/stealth/union-alpha",
		"gpt-4/unknown", "gpt-4o/custom-unknown", "anthropic/", "/claude-opus-4-6",
		"anthropic//claude-opus-4-6", "anthropic/ /claude-opus-4-6", "anthropic/ claude-opus-4-6",
	} {
		t.Run(model, func(t *testing.T) {
			if KnownModel(model) || GetContextLimit(model) != DefaultContextLimit {
				t.Fatalf("unrecognized or malformed model %q received a known window", model)
			}
			if _, err := GetTokenBudgetForModel("omp", model, 0); err == nil {
				t.Fatalf("unrecognized model %q received an explicit model budget", model)
			}
		})
	}
}

func TestProviderQualifiedOverridesTakePrecedence(t *testing.T) {
	overrides := map[string]int{
		"anthropic/claude-opus-4-6":            4096,
		"openrouter/anthropic/claude-opus-4-6": 8192,
		"openrouter/stealth/union-alpha":      262144,
		"test-provider":                      1234,
	}
	registryMu.RLock()
	original := make(map[string]int)
	for key := range overrides {
		if limit, ok := ContextLimits[key]; ok {
			original[key] = limit
		}
	}
	registryMu.RUnlock()
	t.Cleanup(func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		for key := range overrides {
			if limit, ok := original[key]; ok {
				ContextLimits[key] = limit
			} else {
				delete(ContextLimits, key)
			}
		}
		rebuildSortedKeysLocked()
	})
	ApplyOverrides(overrides)
	for _, tt := range []struct {
		model string
		want  int
	}{
		{"anthropic/claude-opus-4-6", 4096},
		{"anthropic/claude-opus-4-6-20260101", 4096},
		{"anthropic/claude-opus-4-6-preview", 4096},
		{"openrouter/anthropic/claude-opus-4-6", 8192},
		{"openrouter/anthropic/claude-opus-4-6-20260101", 8192},
		{"proxy/openrouter/anthropic/claude-opus-4-6", 8192},
		{"proxy/anthropic/claude-opus-4-6", 4096},
		{"openrouter/stealth/union-alpha", 262144},
		{"claude-opus-4-6", 1000000},
		{"test-provider/claude-opus-4-6", 1000000},
	} {
		if !KnownModel(tt.model) || GetContextLimit(tt.model) != tt.want {
			t.Errorf("override resolution for %q: known=%t limit=%d; want %d", tt.model, KnownModel(tt.model), GetContextLimit(tt.model), tt.want)
		}
	}
	if KnownModel("test-provider/unknown") {
		t.Fatal("provider name override was used as the unknown model's window")
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
		{"x", ""},  // tiny input: rewriting the whole ID is noise, not a near miss
		{"zz", ""}, // tiny input with no distance-1 registry neighbor
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

func TestGetTokenBudgetForModel(t *testing.T) {
	tests := []struct {
		name, agentType, model string
		window, want          int
		wantErr               bool
	}{
		{"agent default", "claude", "", 0, 180000, false},
		{"current large model", "cc", "claude-opus-5", 0, 900000, false},
		{"smaller model", "gmi", "gemini-pro", 0, 3200, false},
		{"codex model", "openai-codex", "gpt-4o-mini", 0, 120320, false},
		{"model normalization", "cc", " Claude-Opus-5 ", 0, 900000, false},
		{"reported window wins", "cc", "claude-opus-5", 32000, 28800, false},
		{"custom reported window", "cc", "custom-provider-model", 8192, 7372, false},
		{"unknown agent with window", "unknown", "", 10000, 7800, false},
		{"legacy unknown agent", "unknown", "", 0, 100000, false},
		{"unknown model", "cc", "custom-provider-model", 0, 0, true},
		{"negative window", "cc", "claude-opus-5", -1, 0, true},
		{"no working space", "gmi", "gemini-pro", 1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetTokenBudgetForModel(tt.agentType, tt.model, tt.window)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("budget = %d, %v; want %d, error=%t", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestGetTokenBudgetForModelUsesOverrides(t *testing.T) {
	original := GetContextLimit("claude-opus-5")
	t.Cleanup(func() { ApplyOverrides(map[string]int{"claude-opus-5": original}) })
	ApplyOverrides(map[string]int{"claude-opus-5": 10000})
	got, err := GetTokenBudgetForModel("cc", "claude-opus-5", 0)
	if err != nil || got != 9000 {
		t.Fatalf("model override was ignored: %d, %v", got, err)
	}
	got, err = GetTokenBudgetForModel("cc", "claude-opus-5", 20000)
	if err != nil || got != 18000 {
		t.Fatalf("reported window did not override registry: %d, %v", got, err)
	}
}

func TestGetTokenBudgetForModelDoesNotOverflow(t *testing.T) {
	window := int(^uint(0) >> 1)
	want := window - window/10
	if window%10 != 0 {
		want-- // floor(90% of window), computed without multiplication
	}
	got, err := GetTokenBudgetForModel("cc", "custom-model", window)
	if err != nil || got != want {
		t.Fatalf("large window overflowed: %d, %v; want %d", got, err, want)
	}
}

func TestLookupContextLimitDistinguishesKnownCapacityFromDefault(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int
		known bool
	}{
		{"gpt-4", 128000, true},
		{"openrouter/anthropic/claude-opus-4-6", 1000000, true},
		{"anthropic/opus-4.6", 1000000, true},
		{"claude-opus-4-20250514", 200000, true},
		{"private-provider/new-model", 0, false},
		{"gpt-4/unknown-model", 0, false},
		{"anthropic//claude-opus-4-6", 0, false},
		{"", 0, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			limit, known := LookupContextLimit(tc.model)
			if limit != tc.want || known != tc.known {
				t.Fatalf("capacity = (%d, %t), want (%d, %t)", limit, known, tc.want, tc.known)
			}
			if !known && GetContextLimit(tc.model) != DefaultContextLimit {
				t.Fatal("display fallback changed while separating unknown capacity")
			}
		})
	}
}
