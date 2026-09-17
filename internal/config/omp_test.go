package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultOmpCommand_Renders(t *testing.T) {
	if DefaultAgentTemplates().Omp != DefaultOmpCommand {
		t.Fatalf("DefaultAgentTemplates().Omp = %q, want DefaultOmpCommand", DefaultAgentTemplates().Omp)
	}
	tests := []struct {
		name string
		vars AgentTemplateVars
		want string
	}{
		{
			// omp owns its default model (modelRoles.default); a bare launch
			// must not inject --model.
			name: "bare",
			vars: AgentTemplateVars{AgentType: "omp"},
			want: "omp --auto-approve",
		},
		{
			name: "model",
			vars: AgentTemplateVars{AgentType: "omp", Model: "openrouter/stealth/union-alpha", ModelRequested: true},
			want: "omp --auto-approve --model 'openrouter/stealth/union-alpha'",
		},
		{
			name: "model and effort map to --thinking",
			vars: AgentTemplateVars{AgentType: "omp", Model: "opus", ModelRequested: true, ReasoningEffort: "high"},
			want: "omp --auto-approve --model 'opus' --thinking 'high'",
		},
		{
			name: "persona appends the system prompt file",
			vars: AgentTemplateVars{AgentType: "omp", SystemPromptFile: "/tmp/persona prompt.md"},
			want: "omp --auto-approve --append-system-prompt '/tmp/persona prompt.md'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateAgentCommand(DefaultOmpCommand, tc.vars)
			if err != nil {
				t.Fatalf("GenerateAgentCommand: %v", err)
			}
			if got != tc.want {
				t.Fatalf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOmpCommandOrDefault(t *testing.T) {
	if got := OmpCommandOrDefault(""); got != DefaultOmpCommand {
		t.Fatalf("empty override = %q, want default", got)
	}
	if got := OmpCommandOrDefault("   "); got != DefaultOmpCommand {
		t.Fatalf("blank override = %q, want default", got)
	}
	const custom = "omp --approval-mode yolo --model {{shellQuote .Model}}"
	if got := OmpCommandOrDefault(custom); got != custom {
		t.Fatalf("custom override = %q, want %q", got, custom)
	}
}

// TestOmp_EffortIsNeverSilentlyDropped pins that omp is an effort-consuming
// type: an [agents] omp override without {{.ReasoningEffort}} must reject a
// requested effort instead of launching on omp's default thinking level.
func TestOmp_EffortIsNeverSilentlyDropped(t *testing.T) {
	_, err := GenerateAgentCommand("omp --auto-approve{{if .Model}} --model {{shellQuote .Model}}{{end}}", AgentTemplateVars{
		AgentType: "omp", ReasoningEffort: "xhigh",
	})
	if err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("expected a dropped-effort error, got %v", err)
	}
	_, err = GenerateAgentCommand("omp --auto-approve", AgentTemplateVars{AgentType: "omp", ReasoningEffort: "xhigh"})
	if err == nil {
		t.Fatal("a non-template omp override must reject a requested effort")
	}
}

func TestOmpConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[agents]
omp = "omp --auto-approve --profile swarm{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .ReasoningEffort}} --thinking {{shellQuote .ReasoningEffort}}{{end}}"

[models]
default_omp = "openrouter/stealth/union-alpha"

[models.omp]
fast = "kimi-code/k3"

[prompts]
omp_default = "Read AGENTS.md first."

[spawn_pacing.agent_caps]
omp_max_concurrent = 8
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(cfg.Agents.Omp, "--profile swarm") {
		t.Fatalf("[agents] omp override not loaded: %q", cfg.Agents.Omp)
	}
	if got := OmpCommandOrDefault(cfg.Agents.Omp); got != cfg.Agents.Omp {
		t.Fatalf("override must win over the default: %q", got)
	}
	if got := cfg.Models.GetModelName("omp", ""); got != "openrouter/stealth/union-alpha" {
		t.Fatalf("default_omp = %q", got)
	}
	if got := cfg.Models.GetModelName("oh-my-pi", "fast"); got != "kimi-code/k3" {
		t.Fatalf("omp alias resolution = %q", got)
	}
	if got := cfg.Models.GetModelName("omp", "unknown-alias"); got != "unknown-alias" {
		t.Fatalf("unaliased omp model must pass through for omp's fuzzy matching, got %q", got)
	}
	if prompt, err := cfg.Prompts.ResolveForType("omp"); err != nil || prompt != "Read AGENTS.md first." {
		t.Fatalf("prompts.omp_default = (%q, %v)", prompt, err)
	}
	if cfg.SpawnPacing.AgentCaps.OmpMaxConcurrent != 8 {
		t.Fatalf("omp_max_concurrent = %d, want 8", cfg.SpawnPacing.AgentCaps.OmpMaxConcurrent)
	}

	for path, want := range map[string]interface{}{
		"agents.omp":          cfg.Agents.Omp,
		"models.default_omp":  "openrouter/stealth/union-alpha",
		"prompts.omp_default": "Read AGENTS.md first.",
		"spawn_pacing.agent_caps.omp_max_concurrent": 8,
	} {
		got, err := GetValue(cfg, path)
		if err != nil {
			t.Fatalf("GetValue(%s): %v", path, err)
		}
		if got != want {
			t.Fatalf("GetValue(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestOmpModelDefaultsDelegateToOmp(t *testing.T) {
	models := DefaultModels()
	if models.DefaultOmp != "" {
		t.Fatalf("DefaultOmp = %q; omp's own config must choose the default model", models.DefaultOmp)
	}
	if got := models.GetModelName("omp", ""); got != "" {
		t.Fatalf("GetModelName(omp, \"\") = %q, want empty (no --model injected)", got)
	}
	if models.AliasesFor("omp") == nil {
		t.Fatal("omp must have an (empty) alias table")
	}
}

func TestSpawnPacing_OmpCap(t *testing.T) {
	cfg := DefaultSpawnPacingConfig()
	if cfg.AgentCaps.OmpMaxConcurrent != 2 {
		t.Fatalf("OmpMaxConcurrent default = %d, want 2", cfg.AgentCaps.OmpMaxConcurrent)
	}
	cfg.AgentCaps.OmpMaxConcurrent = -1
	if err := ValidateSpawnPacingConfig(&cfg); err == nil || !strings.Contains(err.Error(), "omp_max_concurrent") {
		t.Fatalf("negative omp cap must fail validation naming the key, got %v", err)
	}
}
