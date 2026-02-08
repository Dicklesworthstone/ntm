package config

import (
	"bytes"
	"strings"
	"text/template"
)

// AgentTemplateVars contains variables available for agent command templates
type AgentTemplateVars struct {
	Model            string // Resolved full model name (e.g., "claude-opus-4-20250514")
	ModelAlias       string // Original alias as specified (e.g., "opus")
	SessionName      string // NTM session name
	PaneIndex        int    // Pane number (1-based)
	AgentType        string // Agent type: "cc", "cod", "gmi"
	ProjectDir       string // Project directory path
	SystemPrompt     string // System prompt content (if any)
	SystemPromptFile string // Path to system prompt file (if any)
	PersonaName      string // Name of persona (if any)
}

// ShellQuote safely quotes a string for use in shell commands.
// It uses single quotes and escapes any single quotes within the string.
// Example: "hello 'world'" becomes "'hello '\”world'\”'"
func ShellQuote(s string) string {
	// Empty string gets empty quotes
	if s == "" {
		return "''"
	}
	// Replace single quotes with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// templateFuncs contains custom functions available in templates
var templateFuncs = template.FuncMap{
	// default returns the fallback if value is empty
	"default": func(fallback, value string) string {
		if value == "" {
			return fallback
		}
		return value
	},
	// eq checks string equality
	"eq": func(a, b string) bool {
		return a == b
	},
	// ne checks string inequality
	"ne": func(a, b string) bool {
		return a != b
	},
	// contains checks if string contains substring
	"contains": func(s, substr string) bool {
		return strings.Contains(s, substr)
	},
	// hasPrefix checks if string has prefix
	"hasPrefix": func(s, prefix string) bool {
		return strings.HasPrefix(s, prefix)
	},
	// hasSuffix checks if string has suffix
	"hasSuffix": func(s, suffix string) bool {
		return strings.HasSuffix(s, suffix)
	},
	// lower converts to lowercase
	"lower": func(s string) string {
		return strings.ToLower(s)
	},
	// upper converts to uppercase
	"upper": func(s string) string {
		return strings.ToUpper(s)
	},
	// shellQuote safely quotes a string for shell command usage
	// Use this when inserting untrusted values into shell commands
	"shellQuote": ShellQuote,
}

// GenerateAgentCommand renders an agent command template with the given variables.
// If the template contains no {{}} syntax, it's returned as-is (legacy mode).
// Returns an error if template parsing or execution fails.
func GenerateAgentCommand(tmpl string, vars AgentTemplateVars) (string, error) {
	// Fast path: if no template syntax, return as-is (legacy mode)
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	t, err := template.New("agent").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}

	result := strings.TrimSpace(buf.String())

	return result, nil
}

// IsTemplateCommand checks if a command string uses template syntax
func IsTemplateCommand(cmd string) bool {
	return strings.Contains(cmd, "{{")
}

// DefaultAgentTemplates returns default agent command templates with model injection support.
// These templates show the recommended format for model-aware agent commands.
// System prompt injection is supported via SystemPromptFile for persona agents.
func DefaultAgentTemplates() AgentConfig {
	return AgentConfig{
		Claude:   `NODE_OPTIONS="--max-old-space-size=32768" claude --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .SystemPromptFile}} --system-prompt-file {{shellQuote .SystemPromptFile}}{{end}}`,
		Codex:    `{{if .SystemPromptFile}}CODEX_SYSTEM_PROMPT="$(cat {{shellQuote .SystemPromptFile}})" {{end}}codex --dangerously-bypass-approvals-and-sandbox -m {{shellQuote (.Model | default "gpt-5.3-codex")}} -c model_reasoning_effort="xhigh" -c model_reasoning_summary_format=experimental --search`,
		Gemini:   `gemini{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .SystemPromptFile}} --system-instruction-file {{shellQuote .SystemPromptFile}}{{end}} --yolo`,
		Ollama:   `ollama run {{shellQuote (.Model | default "codellama:latest")}}`,
		Cursor:   `cursor{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Windsurf: `windsurf{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Aider:    `aider{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
	}
}
