package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestDeriveAgentTypeFromID_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    string
	}{
		{"myproject__cursor_1", "cursor"},
		{"myproject__windsurf_2", "windsurf"},
		{"myproject__ws_2", "windsurf"},
		{"myproject__aider_3", "aider"},
		{"myproject__ollama_4", "ollama"},
		{"myproject__cursor_1_variant", "cursor"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			got := deriveAgentTypeFromID(tt.agentID)
			if got != tt.want {
				t.Errorf("deriveAgentTypeFromID(%q) = %q, want %q", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestAgentTypeShort_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		want      string
	}{
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := agentTypeShort(tt.agentType)
			if got != tt.want {
				t.Errorf("agentTypeShort(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestAgentTypeLong_NewAgents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shortType string
		want      string
	}{
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.shortType, func(t *testing.T) {
			got := agentTypeLong(tt.shortType)
			if got != tt.want {
				t.Errorf("agentTypeLong(%q) = %q, want %q", tt.shortType, got, tt.want)
			}
		})
	}
}

func TestDefaultPaneSpawnerGetAgentCommand_NewAgents(t *testing.T) {
	t.Parallel()

	// Without config
	spawner := NewDefaultPaneSpawner(nil)
	defaults := config.DefaultAgentTemplates()

	tests := []struct {
		agentType string
		want      string
	}{
		{"cursor", defaults.Cursor},
		{"windsurf", defaults.Windsurf},
		{"ws", defaults.Windsurf},
		{"aider", defaults.Aider},
		{"ollama", defaults.Ollama},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := spawner.getAgentCommand(tt.agentType)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestDefaultPaneSpawner_RestoresLaunchSpec(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType string
		variant   string
		nilConfig bool
		want      []string
	}{
		{"claude_model_effort", "claude", "opus@high", false, []string{"claude --dangerously-skip-permissions", "--model '" + config.DefaultModels().Claude["opus"] + "'", "--effort 'high'"}},
		{"custom_codex_model", "codex", "private_model_2026@medium", false, []string{"codex --dangerously-bypass-approvals-and-sandbox", "-m 'private_model_2026'", "model_reasoning_effort='medium'"}},
		{"bare_custom_model", "codex", "private-model", false, []string{"-m 'private-model'"}},
		{"default_without_config", "codex", "", true, []string{"codex --dangerously-bypass-approvals-and-sandbox", "-m '" + config.DefaultCodexModel + "'"}},
		{"cursor_cli_without_config", "cursor", "", true, []string{"cursor-agent --yolo"}},
		{"omp_model_effort", "omp", "private-model@high", true, []string{"omp --auto-approve", "--model 'private-model'", "--thinking 'high'"}},
		{"opencode_cli", "oc", "custom-model", true, []string{"opencode", "'custom-model'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			if tc.nilConfig {
				cfg = nil
			}
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", tc.agentType, 7, tc.variant, workDir)
			if err != nil {
				t.Fatalf("SpawnAgent() error = %v", err)
			}
			if paneID != "%99" {
				t.Fatalf("SpawnAgent() pane = %q, want %%99", paneID)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(commands)
			if strings.Contains(log, "{{") || strings.Contains(log, "}}") {
				t.Errorf("unrendered template reached tmux: %s", log)
			}
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Errorf("launch missing %q:\n%s", want, log)
				}
			}
			if !strings.Contains(log, "test__"+agentTypeShort(tc.agentType)+"_7") {
				t.Errorf("replacement title lost logical agent index: %s", log)
			}
		})
	}
}

func TestDefaultPaneSpawner_RestoresPersona(t *testing.T) {
	workDir, logPath := setupRotationTmux(t)
	if err := os.MkdirAll(filepath.Join(workDir, ".ntm"), 0755); err != nil {
		t.Fatal(err)
	}
	personaConfig := `[[personas]]
name = "rotation-reviewer"
agent_type = "codex"
model = "private-model"
reasoning_effort = "high"
system_prompt = "Review changes against the project's correctness requirements."
`
	if err := os.WriteFile(filepath.Join(workDir, ".ntm", "personas.toml"), []byte(personaConfig), 0644); err != nil {
		t.Fatal(err)
	}
	spawner := NewDefaultPaneSpawner(config.Default())
	if _, err := spawner.SpawnAgent("test", "codex", 1, "rotation-reviewer", workDir); err != nil {
		t.Fatalf("SpawnAgent() error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-m 'private-model'", "model_reasoning_effort='high'", "CODEX_SYSTEM_PROMPT=", "rotation-reviewer.md"} {
		if !strings.Contains(string(commands), want) {
			t.Errorf("persona launch missing %q:\n%s", want, commands)
		}
	}
	if strings.Contains(string(commands), "-m 'rotation-reviewer'") {
		t.Errorf("persona name was launched as a model: %s", commands)
	}
}

func TestDefaultPaneSpawner_RejectsInvalidLaunchBeforePaneCreation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		variant string
		wantErr string
	}{
		{"invalid_template", "codex {{.Missing", "", "preparing replacement agent"},
		{"dropped_model", "codex --default", "private-model", "model override"},
		{"dropped_effort", "codex -m {{shellQuote .Model}}", "private-model@high", "reasoning effort"},
		{"empty_template", "{{if false}}codex{{end}}", "", "rendered empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			cfg.Agents.Codex = tc.command
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", "codex", 1, tc.variant, workDir)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("SpawnAgent() = %q, %v, want error containing %q", paneID, err, tc.wantErr)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("tmux was invoked before invalid launch was rejected: %v", err)
			}
		})
	}
}

func TestDefaultPaneSpawner_RejectsAmbiguousPersonaVariant(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentType string
		variant   string
		alias     string
		model     string
	}{
		{name: "default_architect_alias", agentType: "claude", variant: "architect"},
		{name: "custom_alias", agentType: "codex", variant: "rotation-reviewer", alias: "rotation-reviewer", model: "selected-model"},
		{name: "custom_full_model", agentType: "codex", variant: "rotation-reviewer", alias: "custom", model: "rotation-reviewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir, logPath := setupRotationTmux(t)
			cfg := config.Default()
			if tc.alias != "" {
				cfg.Models.Codex[tc.alias] = tc.model
				if err := os.MkdirAll(filepath.Join(workDir, ".ntm"), 0755); err != nil {
					t.Fatal(err)
				}
				const conflictingPersona = `[[personas]]
name = "rotation-reviewer"
agent_type = "codex"
model = "different-persona-model"
system_prompt = "This prompt must not be injected into a model-only agent."
`
				if err := os.WriteFile(filepath.Join(workDir, ".ntm", "personas.toml"), []byte(conflictingPersona), 0644); err != nil {
					t.Fatal(err)
				}
			}
			spawner := NewDefaultPaneSpawner(cfg)
			paneID, err := spawner.SpawnAgent("test", tc.agentType, 1, tc.variant, workDir)
			if err == nil || !strings.Contains(err.Error(), "ambiguous replacement variant") {
				t.Fatalf("SpawnAgent() = %q, %v, want explicit ambiguity error", paneID, err)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("tmux was invoked for an ambiguous launch selection: %v", err)
			}
		})
	}
}

// Exercise the production PaneSpawner against a recording tmux executable so
// tests cover the command actually sent to the pane, including preflight order.
func setupRotationTmux(t *testing.T) (workDir, logPath string) {
	t.Helper()
	workDir = t.TempDir()
	logPath = filepath.Join(workDir, "tmux.log")
	stub := filepath.Join(workDir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$ROTATION_TMUX_LOG"
case "$1" in
  list-windows) printf '0\n' ;;
  split-window) printf '%%99\n' ;;
  display-message) printf '0\n' ;;
esac
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", stub)
	t.Setenv("ROTATION_TMUX_LOG", logPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(workDir, "config"))
	return workDir, logPath
}
