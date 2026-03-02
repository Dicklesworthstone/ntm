package config

import (
	"strings"
	"testing"
)

func TestGenerateAgentCommand_LegacyMode(t *testing.T) {
	// Test that commands without template syntax are returned as-is
	// when no model override is specified
	tests := []struct {
		name     string
		template string
		vars     AgentTemplateVars
		want     string
	}{
		{
			name:     "plain command no model",
			template: "claude --dangerously-skip-permissions",
			vars:     AgentTemplateVars{},
			want:     "claude --dangerously-skip-permissions",
		},
		{
			name:     "codex command no model",
			template: "codex -m gpt-4",
			vars:     AgentTemplateVars{},
			want:     "codex -m gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateAgentCommand(tt.template, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateAgentCommand_LegacyModeModelOverrideGuard(t *testing.T) {
	// Test that legacy (non-template) commands fail fast when a model
	// override is specified, rather than silently dropping it.
	tests := []struct {
		name     string
		template string
		vars     AgentTemplateVars
	}{
		{
			name:     "plain command with model override",
			template: "claude --dangerously-skip-permissions",
			vars:     AgentTemplateVars{Model: "opus"},
		},
		{
			name:     "codex command with model override",
			template: "codex -m gpt-4",
			vars:     AgentTemplateVars{Model: "gpt-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateAgentCommand(tt.template, tt.vars)
			if err == nil {
				t.Fatal("expected error for model override on legacy command, got nil")
			}
			if !strings.Contains(err.Error(), "model override") {
				t.Errorf("error should mention 'model override', got: %v", err)
			}
			if !strings.Contains(err.Error(), tt.vars.Model) {
				t.Errorf("error should mention the model %q, got: %v", tt.vars.Model, err)
			}
		})
	}
}

func TestGenerateAgentCommand_TemplateMode(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     AgentTemplateVars
		want     string
	}{
		{
			name:     "model injection",
			template: `claude --model {{.Model}}`,
			vars:     AgentTemplateVars{Model: "claude-opus-4"},
			want:     "claude --model claude-opus-4",
		},
		{
			name:     "conditional model",
			template: `claude{{if .Model}} --model {{.Model}}{{end}}`,
			vars:     AgentTemplateVars{Model: "claude-sonnet-4"},
			want:     "claude --model claude-sonnet-4",
		},
		{
			name:     "conditional model empty",
			template: `claude{{if .Model}} --model {{.Model}}{{end}}`,
			vars:     AgentTemplateVars{Model: ""},
			want:     "claude",
		},
		{
			name:     "default function",
			template: `codex -m {{.Model | default "gpt-4"}}`,
			vars:     AgentTemplateVars{Model: ""},
			want:     "codex -m gpt-4",
		},
		{
			name:     "default function with value",
			template: `codex -m {{.Model | default "gpt-4"}}`,
			vars:     AgentTemplateVars{Model: "o1"},
			want:     "codex -m o1",
		},
		{
			name:     "session name",
			template: `agent --session {{.SessionName}}`,
			vars:     AgentTemplateVars{SessionName: "myproject"},
			want:     "agent --session myproject",
		},
		{
			name:     "pane index",
			template: `agent --pane {{.PaneIndex}}`,
			vars:     AgentTemplateVars{PaneIndex: 3},
			want:     "agent --pane 3",
		},
		{
			name:     "agent type",
			template: `agent --type {{.AgentType}}`,
			vars:     AgentTemplateVars{AgentType: "cc"},
			want:     "agent --type cc",
		},
		{
			name:     "eq function true",
			template: `gemini{{if eq .Model "flash"}} --fast{{end}}`,
			vars:     AgentTemplateVars{Model: "flash"},
			want:     "gemini --fast",
		},
		{
			name:     "eq function false",
			template: `gemini{{if eq .Model "flash"}} --fast{{end}}`,
			vars:     AgentTemplateVars{Model: "pro"},
			want:     "gemini",
		},
		{
			name:     "multiple variables",
			template: `agent --model {{.Model}} --session {{.SessionName}} --pane {{.PaneIndex}}`,
			vars: AgentTemplateVars{
				Model:       "claude-opus-4",
				SessionName: "test",
				PaneIndex:   2,
			},
			want: "agent --model claude-opus-4 --session test --pane 2",
		},
		{
			name:     "complex template",
			template: `NODE_OPTIONS="--max-old-space-size=32768" claude --dangerously-skip-permissions{{if .Model}} --model {{.Model}}{{end}}`,
			vars:     AgentTemplateVars{Model: "claude-opus-4-20250514"},
			want:     `NODE_OPTIONS="--max-old-space-size=32768" claude --dangerously-skip-permissions --model claude-opus-4-20250514`,
		},
		{
			name:     "contains function",
			template: `agent{{if contains .Model "opus"}} --premium{{end}}`,
			vars:     AgentTemplateVars{Model: "claude-opus-4"},
			want:     "agent --premium",
		},
		{
			name:     "lower function",
			template: `agent --model {{lower .Model}}`,
			vars:     AgentTemplateVars{Model: "OPUS"},
			want:     "agent --model opus",
		},
		{
			name:     "quoted argument with spaces",
			template: `echo '{{.Model}}'`,
			vars:     AgentTemplateVars{Model: "hello  world"},
			want:     "echo 'hello  world'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateAgentCommand(tt.template, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateAgentCommand_Error(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     AgentTemplateVars
	}{
		{
			name:     "invalid template syntax",
			template: "claude {{.InvalidSyntax",
			vars:     AgentTemplateVars{},
		},
		{
			name:     "unclosed action",
			template: "claude {{if .Model}}",
			vars:     AgentTemplateVars{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateAgentCommand(tt.template, tt.vars)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestIsTemplateCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"claude --skip-permissions", false},
		{"claude {{.Model}}", true},
		{"claude{{if .Model}} --model{{end}}", true},
		{"codex -m gpt-4", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := IsTemplateCommand(tt.cmd); got != tt.want {
				t.Errorf("IsTemplateCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDefaultAgentTemplates(t *testing.T) {
	templates := DefaultAgentTemplates()

	// Verify all templates are valid
	vars := AgentTemplateVars{
		Model:       "test-model",
		ModelAlias:  "test",
		SessionName: "testsession",
		PaneIndex:   1,
		AgentType:   "cc",
		ProjectDir:  "/test/dir",
	}

	// Test Claude template
	_, err := GenerateAgentCommand(templates.Claude, vars)
	if err != nil {
		t.Errorf("Claude template failed: %v", err)
	}

	// Test Codex template
	_, err = GenerateAgentCommand(templates.Codex, vars)
	if err != nil {
		t.Errorf("Codex template failed: %v", err)
	}

	// Test Gemini template
	_, err = GenerateAgentCommand(templates.Gemini, vars)
	if err != nil {
		t.Errorf("Gemini template failed: %v", err)
	}
}

func TestDefaultAgentTemplates_ShellQuoting(t *testing.T) {
	vars := AgentTemplateVars{
		Model:            "model name; rm -rf /",
		SystemPromptFile: "/tmp/prompt file's.txt",
	}

	templates := DefaultAgentTemplates()

	// check verifies that a template correctly shell-quotes both the model and
	// the system prompt file (for agents that support the system-prompt-file flag).
	check := func(name, tmpl string) {
		cmd, err := GenerateAgentCommand(tmpl, vars)
		if err != nil {
			t.Fatalf("%s template: %v", name, err)
		}
		quotedModel := ShellQuote(vars.Model)
		quotedFile := ShellQuote(vars.SystemPromptFile)

		if !strings.Contains(cmd, quotedModel) {
			t.Fatalf("%s template should contain quoted model %q, got: %s", name, quotedModel, cmd)
		}
		if !strings.Contains(cmd, quotedFile) {
			t.Fatalf("%s template should contain quoted system prompt file %q, got: %s", name, quotedFile, cmd)
		}
		// Ensure unquoted value is not present to guard against injection
		if strings.Contains(cmd, vars.Model) && !strings.Contains(cmd, quotedModel) {
			t.Fatalf("%s template appears to include unquoted model: %s", name, cmd)
		}
	}

	// checkModelOnly verifies shell-quoting of the model only — for agents where
	// system-prompt-file injection is not supported via CLI flags (e.g. Gemini,
	// which removed --system-instruction-file in v0.31.0).
	checkModelOnly := func(name, tmpl string) {
		cmd, err := GenerateAgentCommand(tmpl, vars)
		if err != nil {
			t.Fatalf("%s template: %v", name, err)
		}
		quotedModel := ShellQuote(vars.Model)
		if !strings.Contains(cmd, quotedModel) {
			t.Fatalf("%s template should contain quoted model %q, got: %s", name, quotedModel, cmd)
		}
		if strings.Contains(cmd, vars.Model) && !strings.Contains(cmd, quotedModel) {
			t.Fatalf("%s template appears to include unquoted model: %s", name, cmd)
		}
	}

	check("claude", templates.Claude)
	check("codex", templates.Codex)
	// Gemini dropped --system-instruction-file in v0.31.0; only model quoting applies.
	checkModelOnly("gemini", templates.Gemini)
}

// TestShellQuote_Branches tests both branches of ShellQuote.
func TestShellQuote_Branches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", "''"},
		{"plain text", "hello", "'hello'"},
		{"single quote", "it's", "'it'\\''s'"},
		{"only single quote", "'", "''\\'''"},
		{"multiple single quotes", "a''b", "'a'\\'''\\''b'"},
		{"no special chars", "abc123", "'abc123'"},
		{"with spaces", "hello world", "'hello world'"},
		{"with semicolons", "cmd; rm -rf /", "'cmd; rm -rf /'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShellQuote(tc.input)
			if got != tc.want {
				t.Errorf("ShellQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTemplateFunctions(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     AgentTemplateVars
		want     string
	}{
		{
			name:     "ne function true",
			template: `{{if ne .Model ""}}-m {{.Model}}{{end}}`,
			vars:     AgentTemplateVars{Model: "opus"},
			want:     "-m opus",
		},
		{
			name:     "ne function false",
			template: `{{if ne .Model ""}}-m {{.Model}}{{end}}`,
			vars:     AgentTemplateVars{Model: ""},
			want:     "",
		},
		{
			name:     "hasPrefix",
			template: `{{if hasPrefix .Model "claude"}}claude-family{{end}}`,
			vars:     AgentTemplateVars{Model: "claude-opus-4"},
			want:     "claude-family",
		},
		{
			name:     "hasSuffix",
			template: `{{if hasSuffix .Model "-4"}}v4{{end}}`,
			vars:     AgentTemplateVars{Model: "claude-opus-4"},
			want:     "v4",
		},
		{
			name:     "upper function",
			template: `{{upper .AgentType}}`,
			vars:     AgentTemplateVars{AgentType: "cc"},
			want:     "CC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateAgentCommand(tt.template, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
