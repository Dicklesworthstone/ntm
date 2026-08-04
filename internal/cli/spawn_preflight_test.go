package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// Tests for validateSpawnAgentCommands (ntm-akaq): an explicit N:model spec
// against an [agents] command that hardcodes -m must abort before any
// worktree/session/pane mutation, and template-based configs must pass
// preflight so the variant is honored at launch.
func TestValidateSpawnAgentCommandsPreflight(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	hardcoded := `codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.6-sol -c model_reasoning_effort=ultra`

	cases := []struct {
		name      string
		codexCmd  string
		model     string
		wantError string
	}{
		{
			name:      "variant against hardcoded -m errors before mutation",
			codexCmd:  hardcoded,
			model:     "gpt-5.6-terra",
			wantError: "model override",
		},
		{
			name:     "no variant against hardcoded command passes",
			codexCmd: hardcoded,
			model:    "",
		},
		{
			name:     "variant against templated command passes",
			codexCmd: config.DefaultAgentTemplates().Codex,
			model:    "gpt-5.6-terra",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg = &config.Config{}
			cfg.Agents.Codex = tc.codexCmd

			opts := SpawnOptions{
				Session: "preflight-test",
				Agents: []FlatAgent{
					{Type: AgentTypeCodex, Index: 1, Model: tc.model},
				},
			}
			err := validateSpawnAgentCommands(opts, "")
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("expected preflight to pass, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected preflight error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantError)
			}
			if !strings.Contains(err.Error(), "preflight") {
				t.Fatalf("error should identify itself as a preflight failure: %q", err.Error())
			}
		})
	}
}

func TestSpawnAgentCommandTemplateUnknownType(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = &config.Config{}

	if _, _, err := spawnAgentCommandTemplate(AgentType("nope"), nil, ""); err == nil {
		t.Fatal("expected error for unknown agent type")
	}
}
