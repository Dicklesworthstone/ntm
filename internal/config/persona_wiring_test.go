package config

import (
	"strings"
	"testing"
)

// TestPersonaWiringPerAgentType is the bd-ws7-docs-ux-truth-tqh3l.5 proof
// table: for every built-in agent type, a persona system-prompt file either
// visibly reaches the rendered launch command through the CLI's real
// mechanism, or the render REFUSES with a documented error. Silent drop —
// the pre-fix behavior for gmi/agy/grok — is the one outcome that must be
// impossible.
func TestPersonaWiringPerAgentType(t *testing.T) {
	templates := DefaultAgentTemplates()
	const promptFile = "/proj/.ntm/prompts/architect.md"

	tests := []struct {
		agentType string
		template  string
		vars      AgentTemplateVars
		// wantContains are substrings that prove the prompt file reached the
		// command via the type's real delivery mechanism.
		wantContains []string
		// wantErrContains non-empty means the render must fail loudly with
		// this documented message instead.
		wantErrContains string
	}{
		{
			agentType:    "cc",
			template:     templates.Claude,
			vars:         AgentTemplateVars{AgentType: "cc", SystemPromptFile: promptFile},
			wantContains: []string{"--system-prompt-file", promptFile},
		},
		{
			agentType:    "cod",
			template:     templates.Codex,
			vars:         AgentTemplateVars{AgentType: "cod", SystemPromptFile: promptFile},
			wantContains: []string{"CODEX_SYSTEM_PROMPT=", promptFile},
		},
		{
			// Gemini's persona mechanism is the GEMINI_SYSTEM_MD env var (a
			// path whose contents replace the core system prompt).
			agentType:    "gmi",
			template:     templates.Gemini,
			vars:         AgentTemplateVars{AgentType: "gmi", SystemPromptFile: promptFile},
			wantContains: []string{"GEMINI_SYSTEM_MD=", promptFile},
		},
		{
			// agy has no system-prompt flag/env; the persona prompt is
			// prepended as the first interactive prompt.
			agentType: "agy",
			template:  templates.Antigravity,
			vars: AgentTemplateVars{
				AgentType:        "agy",
				Model:            AntigravityRequiredModel,
				SystemPromptFile: promptFile,
			},
			wantContains: []string{"--prompt-interactive", promptFile},
		},
		{
			// grok: LOUD refusal — the Grok Build CLI has no persona
			// mechanism, and pretending otherwise (silent drop) is the sin.
			agentType:       "grok",
			template:        templates.Grok,
			vars:            AgentTemplateVars{AgentType: "grok", SystemPromptFile: promptFile},
			wantErrContains: "persona ignored: grok has no persona mechanism",
		},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			cmd, err := GenerateAgentCommand(tt.template, tt.vars)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("GenerateAgentCommand(%s) = %q, want loud refusal containing %q",
						tt.agentType, cmd, tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("GenerateAgentCommand(%s) error = %q, want it to contain %q",
						tt.agentType, err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateAgentCommand(%s) error: %v", tt.agentType, err)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(cmd, want) {
					t.Errorf("GenerateAgentCommand(%s) = %q, missing %q (persona would be dropped)",
						tt.agentType, cmd, want)
				}
			}
		})
	}
}

// TestPersonaWiringWithoutPersona pins that the new persona plumbing is inert
// when no persona is requested: no GEMINI_SYSTEM_MD prefix, no
// --prompt-interactive suffix, and grok renders normally.
func TestPersonaWiringWithoutPersona(t *testing.T) {
	templates := DefaultAgentTemplates()

	gmi, err := GenerateAgentCommand(templates.Gemini, AgentTemplateVars{AgentType: "gmi"})
	if err != nil {
		t.Fatalf("gemini template without persona: %v", err)
	}
	if strings.Contains(gmi, "GEMINI_SYSTEM_MD") {
		t.Errorf("gemini command without persona should not set GEMINI_SYSTEM_MD: %q", gmi)
	}

	agy, err := GenerateAgentCommand(templates.Antigravity, AgentTemplateVars{
		AgentType: "agy",
		Model:     AntigravityRequiredModel,
	})
	if err != nil {
		t.Fatalf("agy template without persona: %v", err)
	}
	if strings.Contains(agy, "--prompt-interactive") {
		t.Errorf("agy command without persona should not pass --prompt-interactive: %q", agy)
	}

	grok, err := GenerateAgentCommand(templates.Grok, AgentTemplateVars{AgentType: "grok"})
	if err != nil {
		t.Fatalf("grok template without persona: %v", err)
	}
	if grok != "grok --always-approve" {
		t.Errorf("grok bare command = %q, want %q", grok, "grok --always-approve")
	}
}

// TestGenerateAgentCommandGuardsDroppedPersona pins the generic silent-drop
// guard: any template (custom [agents] overrides included) that does not
// reference .SystemPromptFile must refuse a persona rather than drop it, in
// both template and legacy (no template syntax) modes.
func TestGenerateAgentCommandGuardsDroppedPersona(t *testing.T) {
	const promptFile = "/proj/.ntm/prompts/reviewer.md"

	_, err := GenerateAgentCommand(
		`mytool{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		AgentTemplateVars{AgentType: "cc", SystemPromptFile: promptFile},
	)
	if err == nil || !strings.Contains(err.Error(), "does not reference .SystemPromptFile") {
		t.Fatalf("template without .SystemPromptFile: err = %v, want silent-persona-drop refusal", err)
	}

	_, err = GenerateAgentCommand(
		"mytool --flag",
		AgentTemplateVars{AgentType: "cc", SystemPromptFile: promptFile},
	)
	if err == nil || !strings.Contains(err.Error(), "does not reference .SystemPromptFile") {
		t.Fatalf("legacy command with persona: err = %v, want silent-persona-drop refusal", err)
	}
}
