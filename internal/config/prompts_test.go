package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptsConfig_ResolveForType_Unknown(t *testing.T) {
	t.Parallel()

	got, err := (PromptsConfig{}).ResolveForType("unknown")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestPromptsConfig_ResolveForType_InlineString(t *testing.T) {
	t.Parallel()

	p := PromptsConfig{
		CCDefault: "\nhello\n",
	}
	got, err := p.ResolveForType("cc")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if want := "hello"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptsConfig_ResolveForType_FileFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cc_default.txt")
	if err := os.WriteFile(path, []byte("\nfrom file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := PromptsConfig{
		CCDefaultFile: path,
	}
	got, err := p.ResolveForType("cc")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if want := "from file"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptsConfig_ResolveForType_InlineOverridesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cc_default.txt")
	if err := os.WriteFile(path, []byte("from file"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := PromptsConfig{
		CCDefault:     "from inline",
		CCDefaultFile: path,
	}
	got, err := p.ResolveForType("cc")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if want := "from inline"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptsConfig_ResolveForType_MissingFile(t *testing.T) {
	t.Parallel()

	p := PromptsConfig{
		CCDefaultFile: filepath.Join(t.TempDir(), "does_not_exist.txt"),
	}
	if _, err := p.ResolveForType("cc"); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestPromptsConfig_ResolveForType_MultiHarness(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	grokFile := filepath.Join(dir, "grok.md")
	ocFile := filepath.Join(dir, "oc.md")
	if err := os.WriteFile(grokFile, []byte("  grok file prompt\n"), 0o600); err != nil {
		t.Fatalf("WriteFile grok: %v", err)
	}
	if err := os.WriteFile(ocFile, []byte("\toc file prompt  "), 0o600); err != nil {
		t.Fatalf("WriteFile oc: %v", err)
	}

	inlineCfg := PromptsConfig{
		AgyDefault:  "agy inline",
		OmpDefault:  "omp inline",
		GrokDefault: "grok inline",
		OcDefault:   "oc inline",
	}
	for _, tc := range []struct {
		agentType string
		want      string
	}{
		{"agy", "agy inline"},
		{"antigravity", "agy inline"},
		{"omp", "omp inline"},
		{"oh-my-pi", "omp inline"},
		{"grok", "grok inline"},
		{"grok-build", "grok inline"},
		{"oc", "oc inline"},
		{"opencode", "oc inline"},
		{"open-code", "oc inline"},
	} {
		got, err := inlineCfg.ResolveForType(tc.agentType)
		if err != nil {
			t.Fatalf("ResolveForType(%q) unexpected error: %v", tc.agentType, err)
		}
		if got != tc.want {
			t.Fatalf("ResolveForType(%q) = %q, want %q", tc.agentType, got, tc.want)
		}
	}

	fileCfg := PromptsConfig{
		GrokDefaultFile: grokFile,
		OcDefaultFile:   ocFile,
	}
	for _, tc := range []struct {
		agentType string
		want      string
	}{
		{"grok", "grok file prompt"},
		{"grok-build", "grok file prompt"},
		{"oc", "oc file prompt"},
		{"opencode", "oc file prompt"},
	} {
		got, err := fileCfg.ResolveForType(tc.agentType)
		if err != nil {
			t.Fatalf("ResolveForType(%q) file fallback error: %v", tc.agentType, err)
		}
		if got != tc.want {
			t.Fatalf("ResolveForType(%q) file fallback = %q, want %q", tc.agentType, got, tc.want)
		}
	}

	missingCfg := PromptsConfig{
		GrokDefaultFile: filepath.Join(dir, "missing_grok.md"),
		OcDefaultFile:   filepath.Join(dir, "missing_oc.md"),
	}
	if _, err := missingCfg.ResolveForType("grok"); err == nil || !strings.Contains(err.Error(), "prompts.grok_default_file") {
		t.Fatalf("expected prompts.grok_default_file error, got %v", err)
	}
	if _, err := missingCfg.ResolveForType("opencode"); err == nil || !strings.Contains(err.Error(), "prompts.oc_default_file") {
		t.Fatalf("expected prompts.oc_default_file error for opencode alias, got %v", err)
	}
}

func TestPromptsConfig_GrokAndOpencodeConfigRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	body := `
[prompts]
grok_default = "Follow Grok rules."
grok_default_file = "/tmp/grok_rules.md"
oc_default = "Follow OpenCode rules."
oc_default_file = "/tmp/oc_rules.md"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for key, want := range map[string]string{
		"prompts.grok_default":      "Follow Grok rules.",
		"prompts.grok_default_file": "/tmp/grok_rules.md",
		"prompts.oc_default":        "Follow OpenCode rules.",
		"prompts.oc_default_file":   "/tmp/oc_rules.md",
	} {
		got, err := GetValue(loaded, key)
		if err != nil {
			t.Fatalf("GetValue(%s): %v", key, err)
		}
		if got != want {
			t.Fatalf("GetValue(%s) = %v, want %q", key, got, want)
		}
	}

	diffs := Diff(loaded)
	diffKeys := make(map[string]bool, len(diffs))
	for _, d := range diffs {
		diffKeys[d.Key] = true
	}
	for _, key := range []string{
		"prompts.grok_default",
		"prompts.grok_default_file",
		"prompts.oc_default",
		"prompts.oc_default_file",
	} {
		if !diffKeys[key] {
			t.Fatalf("Diff missing %s (got %+v)", key, diffs)
		}
	}

	var rendered bytes.Buffer
	if err := Print(loaded, &rendered); err != nil {
		t.Fatalf("Print: %v", err)
	}
	out := rendered.String()
	for _, snippet := range []string{
		`grok_default = "Follow Grok rules."`,
		`grok_default_file = "/tmp/grok_rules.md"`,
		`oc_default = "Follow OpenCode rules."`,
		`oc_default_file = "/tmp/oc_rules.md"`,
	} {
		if !strings.Contains(out, snippet) {
			t.Fatalf("WriteConfig output missing %q:\n%s", snippet, out)
		}
	}
}
