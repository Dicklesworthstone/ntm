package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// TestIsCodexFamilyModel verifies the rule for "is this Codex model id
// in the `gpt-*-codex` family that OpenAI rejects with HTTP 400 on
// ChatGPT-billed accounts" (ntm#147).
func TestIsCodexFamilyModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// gpt-*-codex family — must be flagged as unsafe on ChatGPT accounts.
		{"gpt-5-codex", true},
		{"gpt-5.2-codex", true},
		{"gpt-5.3-codex", true},
		{"GPT-5-CODEX", true},     // case-insensitive
		{"  gpt-5-codex  ", true}, // whitespace tolerated
		{"gpt-5.4-codex", true},   // future variants

		// Non-codex gpt models — must NOT be flagged.
		{"gpt-5", false},
		{"gpt-5.5", false}, // the #147 reporter's configured default
		{"gpt-5.3", false},
		{"gpt-4", false},
		{"gpt-4o", false},

		// Other agent families — must NOT be flagged.
		{"claude-opus-4.7", false},
		{"gemini-3-pro-preview", false},
		{"o3-mini", false},
		{"", false},
		{"   ", false},
	}
	for _, tc := range cases {
		got := isCodexFamilyModel(tc.model)
		if got != tc.want {
			t.Errorf("isCodexFamilyModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// TestEffectiveCodexModelPrecedence verifies the resolution chain
// CLI override > cfg default > compiled-in default. This is the
// resolution preflightCodexAccountSupport must use, otherwise bare
// `--cod=N` with a non-codex configured default is wrongly blocked
// (ntm#147).
func TestEffectiveCodexModelPrecedence(t *testing.T) {
	// Snapshot global cfg so we can restore after mutating it.
	origCfg := cfg
	defer func() { cfg = origCfg }()

	// Case 1: explicit CLI model wins over everything.
	cfg = &config.Config{}
	cfg.Models.DefaultCodex = "gpt-5-codex"
	if got := effectiveCodexModel("gpt-5.5"); got != "gpt-5.5" {
		t.Errorf("CLI override should win: got %q want gpt-5.5", got)
	}

	// Case 2: empty CLI -> cfg.Models.DefaultCodex.
	cfg = &config.Config{}
	cfg.Models.DefaultCodex = "gpt-5.5"
	if got := effectiveCodexModel(""); got != "gpt-5.5" {
		t.Errorf("cfg default should win when CLI empty: got %q want gpt-5.5", got)
	}

	// Case 3: empty CLI + cfg default empty -> compiled-in default.
	cfg = &config.Config{}
	want := config.DefaultModels().DefaultCodex
	if got := effectiveCodexModel(""); got != want {
		t.Errorf("compiled default should win when both CLI and cfg empty: got %q want %q", got, want)
	}

	// Case 4: cfg nil -> compiled-in default (defensive).
	cfg = nil
	if got := effectiveCodexModel(""); got != want {
		t.Errorf("compiled default should win when cfg nil: got %q want %q", got, want)
	}

	// Case 5: cfg nil + CLI override still wins.
	cfg = nil
	if got := effectiveCodexModel("gpt-5"); got != "gpt-5" {
		t.Errorf("CLI override should win even with nil cfg: got %q want gpt-5", got)
	}
}

// TestPreflightCounters_RespectsResolvedModel verifies the #147 fix:
// agents with bare `--cod=N` and a non-codex configured default must
// not be counted as default-codex (and therefore must not be blocked
// by the ChatGPT-account preflight).
func TestPreflightCounters_RespectsResolvedModel(t *testing.T) {
	origCfg := cfg
	defer func() { cfg = origCfg }()

	cfg = &config.Config{}
	cfg.Models.DefaultCodex = "gpt-5.5"

	// Mixed batch: two codex agents, one with bare flag (resolves to
	// gpt-5.5, safe) and one with an explicit codex model (unsafe).
	agents := []FlatAgent{
		{Type: AgentTypeCodex, Model: ""},
		{Type: AgentTypeCodex, Model: "gpt-5.3-codex"},
		{Type: AgentTypeClaude, Model: ""},
	}
	got := countDefaultCodex(agents)
	if got != 1 {
		t.Errorf("countDefaultCodex should only count the codex-family agent; got %d want 1", got)
	}

	// All-safe batch: bare codex resolves to gpt-5.5.
	agents = []FlatAgent{
		{Type: AgentTypeCodex, Model: ""},
		{Type: AgentTypeCodex, Model: "gpt-5"},
	}
	if got := countDefaultCodex(agents); got != 0 {
		t.Errorf("non-codex resolved models should not be counted; got %d want 0", got)
	}

	// Codex-family default in cfg: bare codex resolves to the unsafe model.
	cfg.Models.DefaultCodex = "gpt-5.3-codex"
	agents = []FlatAgent{
		{Type: AgentTypeCodex, Model: ""},
	}
	if got := countDefaultCodex(agents); got != 1 {
		t.Errorf("codex-family default should be counted; got %d want 1", got)
	}
}
