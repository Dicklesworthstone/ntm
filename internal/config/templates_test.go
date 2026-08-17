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
			vars:     AgentTemplateVars{Model: "opus", ModelRequested: true},
		},
		{
			name:     "codex command with model override",
			template: "codex -m gpt-4",
			vars:     AgentTemplateVars{Model: "gpt-4", ModelRequested: true},
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

func TestGenerateAgentCommand_TemplateModelOverrideGuard(t *testing.T) {
	_, err := GenerateAgentCommand(`agent --session {{.SessionName}}`, AgentTemplateVars{
		Model:          "claude-opus-4",
		ModelRequested: true,
		SessionName:    "demo",
	})
	if err == nil {
		t.Fatal("expected error for template that ignores requested model, got nil")
	}
	if !strings.Contains(err.Error(), "does not reference .Model or .ModelAlias") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGenerateAgentCommand_TemplateModelOverrideGuardHandlesAbsentElseList(t *testing.T) {
	got, err := GenerateAgentCommand(`agent{{if .Model}} --model {{.Model}}{{end}}`, AgentTemplateVars{
		Model:          "claude-opus-4",
		ModelRequested: true,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand returned an error: %v", err)
	}
	if got != "agent --model claude-opus-4" {
		t.Fatalf("GenerateAgentCommand = %q, want %q", got, "agent --model claude-opus-4")
	}
}

func TestGenerateAgentCommand_TemplateGuardsIgnoreNonRenderingFieldMentions(t *testing.T) {
	t.Run("model name in a template comment does not satisfy the model guard", func(t *testing.T) {
		_, err := GenerateAgentCommand(`agent {{/* .Model */}} --safe`, AgentTemplateVars{
			Model:          "gpt-5.6",
			ModelRequested: true,
		})
		if err == nil {
			t.Fatal("comment-only .Model reference was accepted; the requested model would be silently dropped")
		}
		if !strings.Contains(err.Error(), "does not reference .Model") {
			t.Fatalf("error = %v, want missing-model-placeholder error", err)
		}
	})

	t.Run("effort name in a quoted literal does not satisfy the effort guard", func(t *testing.T) {
		_, err := GenerateAgentCommand(`codex {{if true}}-c note='.ReasoningEffort'{{end}}`, AgentTemplateVars{
			AgentType:       "cod",
			ReasoningEffort: "high",
		})
		if err == nil {
			t.Fatal("literal-only .ReasoningEffort reference was accepted; the requested effort would be silently dropped")
		}
		if !strings.Contains(err.Error(), "does not reference .ReasoningEffort") {
			t.Fatalf("error = %v, want missing-effort-placeholder error", err)
		}
	})
}

func TestGenerateAgentCommand_DefaultModelDoesNotTriggerLegacyGuard(t *testing.T) {
	got, err := GenerateAgentCommand("claude --dangerously-skip-permissions", AgentTemplateVars{
		Model: "claude-opus-4-8",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "claude --dangerously-skip-permissions" {
		t.Fatalf("got %q, want legacy command unchanged", got)
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
			name:     "legacy NODE_OPTIONS template (backward compat)",
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
	codexCmd, err := GenerateAgentCommand(templates.Codex, AgentTemplateVars{})
	if err != nil {
		t.Errorf("Codex template failed: %v", err)
	}
	if !strings.Contains(codexCmd, "-m "+ShellQuote(DefaultCodexModel)) {
		t.Errorf("Codex default model missing from rendered command: %s", codexCmd)
	}
	if !strings.Contains(codexCmd, "model_reasoning_effort="+ShellQuote(DefaultCodexReasoningEffort)) {
		t.Errorf("Codex default reasoning effort missing from rendered command: %s", codexCmd)
	}

	_, err = GenerateAgentCommand(templates.Codex, vars)
	if err != nil {
		t.Errorf("Codex template with explicit vars failed: %v", err)
	}

	// Test Gemini template
	_, err = GenerateAgentCommand(templates.Gemini, vars)
	if err != nil {
		t.Errorf("Gemini template failed: %v", err)
	}

	// Grok Build owns its default model. A bare launch must not freeze a model
	// ID, while an explicit model and effort must be safely quoted.
	grokBare, err := GenerateAgentCommand(templates.Grok, AgentTemplateVars{})
	if err != nil {
		t.Fatalf("Grok template failed: %v", err)
	}
	if grokBare != "grok --always-approve" {
		t.Fatalf("Grok bare render = %q", grokBare)
	}
	grokOverride, err := GenerateAgentCommand(templates.Grok, AgentTemplateVars{
		Model: "account/model", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("Grok override template failed: %v", err)
	}
	if grokOverride != "grok --always-approve --model 'account/model' --effort 'high'" {
		t.Fatalf("Grok override render = %q", grokOverride)
	}

	// Opencode template (ntm#193): must carry a {{.Model}} placeholder so
	// `--oc=N:provider/model` is honored instead of rejected, and so Agent
	// Mail registration receives a non-empty model.
	if templates.Opencode == "" {
		t.Fatal("Opencode default template must not be empty")
	}
	if templates.Opencode != DefaultOpencodeCommand {
		t.Errorf("templates.Opencode = %q, want DefaultOpencodeCommand %q", templates.Opencode, DefaultOpencodeCommand)
	}
	if !IsTemplateCommand(templates.Opencode) {
		t.Error("Opencode template should be recognized as a template command (no {{...}} placeholder)")
	}
	// With a model, --model renders. The reasoning-effort hint is deliberately
	// NOT emitted as `--variant`: that flag is not accepted by the root
	// `opencode` TUI command an interactive pane launches (it exists only on
	// `opencode run`; anomalyco/opencode#7354 / PR #7358 still unmerged), so
	// emitting it would break the launch. ReasoningEffort is therefore inert
	// for opencode and must not leave a dangling flag.
	ocFull, err := GenerateAgentCommand(templates.Opencode, AgentTemplateVars{Model: "zai-coding-plan/glm-5.2", ReasoningEffort: "high"})
	if err != nil {
		t.Fatalf("Opencode template failed: %v", err)
	}
	if ocFull != "opencode --model 'zai-coding-plan/glm-5.2'" {
		t.Errorf("Opencode model+effort render = %q", ocFull)
	}
	// With no model, the command degrades to the bare binary (no dangling flags).
	ocBare, err := GenerateAgentCommand(templates.Opencode, AgentTemplateVars{})
	if err != nil {
		t.Fatalf("Opencode bare template failed: %v", err)
	}
	if ocBare != "opencode" {
		t.Errorf("Opencode no-model render = %q, want %q", ocBare, "opencode")
	}

	// Cursor template (GH#233): must launch the Cursor Agent CLI
	// (`cursor-agent`), never the `cursor` IDE binary, and must carry the
	// autonomous approval flag so unattended panes don't stall on tool
	// prompts. It must NOT inject `--trust` (interactive REPL rejects it) and
	// must NOT use the bare `agent` binary name (collides with Grok Build).
	cursorBare, err := GenerateAgentCommand(templates.Cursor, AgentTemplateVars{})
	if err != nil {
		t.Fatalf("Cursor bare template failed: %v", err)
	}
	if cursorBare != "cursor-agent --yolo" {
		t.Errorf("Cursor bare render = %q, want %q", cursorBare, "cursor-agent --yolo")
	}
	cursorModel, err := GenerateAgentCommand(templates.Cursor, AgentTemplateVars{Model: "cursor-grok-4.5-high"})
	if err != nil {
		t.Fatalf("Cursor model template failed: %v", err)
	}
	if cursorModel != "cursor-agent --yolo --model 'cursor-grok-4.5-high'" {
		t.Errorf("Cursor model render = %q", cursorModel)
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

	check("claude", templates.Claude)
	check("codex", templates.Codex)
	// Gemini delivers the persona via the GEMINI_SYSTEM_MD env var, agy via
	// --prompt-interactive; both must quote the file path (and the model).
	check("gemini", templates.Gemini)
	check("antigravity", templates.Antigravity)

	// Grok has no persona mechanism: rendering with a SystemPromptFile must
	// refuse loudly instead of silently dropping it
	// (bd-ws7-docs-ux-truth-tqh3l.5).
	if _, err := GenerateAgentCommand(templates.Grok, AgentTemplateVars{
		AgentType:        "grok",
		SystemPromptFile: vars.SystemPromptFile,
	}); err == nil || !strings.Contains(err.Error(), "persona ignored: grok has no persona mechanism") {
		t.Fatalf("grok template with persona: err = %v, want loud refusal", err)
	}
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

// bd-ywwam: the silent-drop guard covered the model but not the reasoning
// effort, so `--cod=N:model:effort` against a hardcoded command failed loudly
// on the model and lost the effort in silence. An operator who fixed their
// template for the model still got the default reasoning budget — the swarm's
// cost and quality changed with nothing to notice.
func TestGenerateAgentCommandGuardsDroppedReasoningEffort(t *testing.T) {
	t.Run("hardcoded command rejects an effort override", func(t *testing.T) {
		_, err := GenerateAgentCommand(
			`codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.6-sol -c model_reasoning_effort="ultra"`,
			AgentTemplateVars{AgentType: "cod", ReasoningEffort: "high"},
		)
		if err == nil {
			t.Fatal("effort override against a hardcoded command was accepted; it would be silently ignored")
		}
		if !strings.Contains(err.Error(), "ReasoningEffort") {
			t.Fatalf("error = %v, want it to name the missing placeholder", err)
		}
	})

	t.Run("templated command without the placeholder is rejected", func(t *testing.T) {
		_, err := GenerateAgentCommand(
			`codex -m {{shellQuote .Model}}`,
			AgentTemplateVars{AgentType: "cod", Model: "gpt-5.6-terra", ReasoningEffort: "high"},
		)
		if err == nil {
			t.Fatal("effort override against a template lacking .ReasoningEffort was accepted")
		}
	})

	t.Run("a template that renders the effort is accepted", func(t *testing.T) {
		got, err := GenerateAgentCommand(
			`codex -m {{shellQuote .Model}} -c model_reasoning_effort={{shellQuote .ReasoningEffort}}`,
			AgentTemplateVars{AgentType: "cod", Model: "gpt-5.6-terra", ReasoningEffort: "high"},
		)
		if err != nil {
			t.Fatalf("GenerateAgentCommand: %v", err)
		}
		if !strings.Contains(got, "model_reasoning_effort='high'") || !strings.Contains(got, "'gpt-5.6-terra'") {
			t.Fatalf("rendered = %q, want both the model and the effort", got)
		}
	})

	t.Run("effort is inert for agent types that cannot consume it", func(t *testing.T) {
		// opencode's root TUI command rejects the flag that would carry the
		// effort, so its template deliberately omits .ReasoningEffort and an
		// effort passed there must be dropped QUIETLY, not error.
		got, err := GenerateAgentCommand(
			DefaultOpencodeCommand,
			AgentTemplateVars{AgentType: "oc", Model: "zai-coding-plan/glm-5.2", ReasoningEffort: "high"},
		)
		if err != nil {
			t.Fatalf("opencode effort must be inert, not an error: %v", err)
		}
		if strings.Contains(got, "high") {
			t.Fatalf("rendered = %q, want no dangling effort flag", got)
		}
	})

	t.Run("no effort requested is always fine", func(t *testing.T) {
		if _, err := GenerateAgentCommand(`codex -m gpt-5.6-sol`, AgentTemplateVars{AgentType: "cod"}); err != nil {
			t.Fatalf("unexpected error with no effort override: %v", err)
		}
	})
}
