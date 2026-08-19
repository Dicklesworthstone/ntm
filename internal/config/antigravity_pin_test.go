package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAntigravityModelPin is the model-guard safety test (bd-47kjh.1.7): agy is
// HARD-pinned to "Gemini 3.7 Flash (High)" no matter what model/alias is requested,
// and the pin must not leak into the legacy gemini provider.
func TestAntigravityModelPin(t *testing.T) {
	if AntigravityRequiredModel != "Gemini 3.7 Flash (High)" {
		t.Fatalf("AntigravityRequiredModel = %q, want \"Gemini 3.7 Flash (High)\"", AntigravityRequiredModel)
	}

	m := &ModelsConfig{DefaultGemini: "gemini-3-pro-preview"}

	// Every agy alias/request resolves to the single allowed model — including
	// explicit attempts to select a Flash/other tier, which must be ignored.
	cases := []struct{ agentType, alias string }{
		{"agy", ""},
		{"agy", "flash"},
		{"agy", "gemini-3-flash"},
		{"agy", "pro-low"},
		{"antigravity", ""},
		{"antigravity", "anything"},
		{"antigravity-cli", "claude"},
	}
	for _, c := range cases {
		if got := m.GetModelName(c.agentType, c.alias); got != AntigravityRequiredModel {
			t.Errorf("GetModelName(%q, %q) = %q, want pinned %q",
				c.agentType, c.alias, got, AntigravityRequiredModel)
		}
	}

	// The agy pin must NOT affect the legacy gemini provider.
	if got := m.GetModelName("gmi", ""); got != "gemini-3-pro-preview" {
		t.Errorf("gemini default = %q, want gemini-3-pro-preview (agy pin leaked into gemini)", got)
	}
}

// TestAntigravityDefaultTemplate verifies the spawn template launches agy with
// the injected (pinned) model and agy's own autonomous flag — never gemini's --yolo.
func TestAntigravityDefaultTemplate(t *testing.T) {
	tmpl := DefaultAgentTemplates().Antigravity
	if tmpl == "" {
		t.Fatal("DefaultAgentTemplates().Antigravity is empty")
	}
	// The binary is resolved at render time via {{agyBinary}} (agy-locked when
	// present, else agy) — sharp edge #1 (ntm#210): `agy` is often a shell alias
	// that will not resolve in NTM's non-interactive launch shell.
	for _, want := range []string{"{{agyBinary}}", "--model", "{{shellQuote .Model}}", "--dangerously-skip-permissions"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("antigravity template %q missing %q", tmpl, want)
		}
	}
	if strings.Contains(tmpl, "--yolo") {
		t.Errorf("antigravity template must use --dangerously-skip-permissions, not gemini's --yolo: %q", tmpl)
	}
}

// TestAgyBinaryAutoDetect covers sharp edge #1 (ntm#210): the launch binary is
// resolved from PATH at render time, preferring the real `agy-locked` executable
// (the alias target) over the frequently-aliased `agy`.
func TestAgyBinaryAutoDetect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH executable-bit semantics differ on Windows")
	}

	// Isolate PATH to a dir we control so the host's real agy/agy-locked don't
	// influence the result.
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	// With neither binary present, fall back to plain `agy`.
	if got := agyBinary(); got != "agy" {
		t.Errorf("agyBinary() with empty PATH = %q, want \"agy\"", got)
	}

	// Install an executable agy-locked and confirm it is preferred.
	locked := filepath.Join(dir, "agy-locked")
	if err := os.WriteFile(locked, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write agy-locked: %v", err)
	}
	if got := agyBinary(); got != "agy-locked" {
		t.Errorf("agyBinary() with agy-locked on PATH = %q, want \"agy-locked\"", got)
	}
}

// TestAntigravityTemplateRendersLockedBinaryAndQuotedModel proves the full agy
// path end-to-end: the default template renders to the resolved binary with the
// pinned model — whose spaces and parentheses survive because {{shellQuote}}
// single-quotes it (sharp edges #1 and #2, ntm#210).
func TestAntigravityTemplateRendersLockedBinaryAndQuotedModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH executable-bit semantics differ on Windows")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	locked := filepath.Join(dir, "agy-locked")
	if err := os.WriteFile(locked, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write agy-locked: %v", err)
	}

	cmd, err := GenerateAgentCommand(DefaultAgentTemplates().Antigravity, AgentTemplateVars{
		Model:          AntigravityRequiredModel,
		ModelRequested: true,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand: %v", err)
	}
	want := "agy-locked --model 'Gemini 3.7 Flash (High)' --dangerously-skip-permissions"
	if cmd != want {
		t.Errorf("rendered agy command = %q, want %q", cmd, want)
	}
}
