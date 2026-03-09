package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
)

// AgentTemplateVars contains variables available for agent command templates
type AgentTemplateVars struct {
	Model            string // Resolved full model name (e.g., "claude-opus-4-20250514")
	ModelAlias       string // Original alias as specified (e.g., "opus")
	ModelRequested   bool   // True when the user explicitly requested a non-default model
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

// systemMemoryMB returns total system RAM in MB, or 0 if unknown.
func systemMemoryMB() uint64 {
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, err := strconv.ParseUint(fields[1], 10, 64)
					if err == nil {
						return kb / 1024
					}
				}
			}
		}
	case "darwin":
		// On macOS, sysctl is the standard way but we avoid exec;
		// use a safe default for Macs (typically 16-64GB)
		return 0
	}
	return 0
}

// nodeHeapMB computes a safe Node.js heap size based on system RAM.
// Uses 25% of total RAM, clamped between 2048 MB and 16384 MB.
// Deprecated: Claude Code is a native binary; NODE_OPTIONS has no effect.
// Kept for backward compatibility with user-customized templates.
func nodeHeapMB() string {
	return memLimitMB()
}

// memLimitMB computes a per-agent memory limit based on system RAM.
// Uses 25% of total RAM, clamped between 2048 MB and 16384 MB.
func memLimitMB() string {
	totalMB := systemMemoryMB()
	if totalMB == 0 {
		return "8192" // safe default for unknown systems
	}
	limitMB := totalMB / 4
	if limitMB < 2048 {
		limitMB = 2048
	}
	if limitMB > 16384 {
		limitMB = 16384
	}
	return fmt.Sprintf("%d", limitMB)
}

// hasSystemdUserSession checks whether a systemd user session is available.
// In Docker containers, devcontainers, WSL1, etc., systemd-run --user fails
// because there is no user session bus. We detect this by checking for the
// per-user systemd private socket.
var hasSystemdUserSession = sync.OnceValue(func() bool {
	uid := os.Getuid()
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/systemd/private", uid))
	return err == nil
})

// memLimitPrefix returns a command prefix that enforces a real memory limit.
// On Linux with a systemd user session, uses systemd-run --user --scope -p
// MemoryMax= (cgroup v2). On other platforms or when systemd is unavailable
// (e.g., Docker containers, WSL1), returns an empty string.
func memLimitPrefix() string {
	if runtime.GOOS == "linux" && hasSystemdUserSession() {
		return fmt.Sprintf("systemd-run --user --scope -q -p MemoryMax=%sM", memLimitMB())
	}
	return ""
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
	// nodeHeapMB returns a safe Node.js heap size based on system RAM
	// Deprecated: kept for backward compat, use memLimitMB instead
	"nodeHeapMB": nodeHeapMB,
	// memLimitMB returns a per-agent memory limit in MB based on system RAM
	"memLimitMB": memLimitMB,
	// memLimitPrefix returns an OS-appropriate command prefix that enforces
	// a real memory limit (systemd-run on Linux, empty on other platforms)
	"memLimitPrefix": memLimitPrefix,
}

func templateReferencesModel(tmpl string) bool {
	return strings.Contains(tmpl, ".Model") || strings.Contains(tmpl, ".ModelAlias")
}

// GenerateAgentCommand renders an agent command template with the given variables.
// Legacy commands without template syntax are returned as-is unless they would
// silently drop an explicitly requested model selection.
// Returns an error if template parsing or execution fails.
func GenerateAgentCommand(tmpl string, vars AgentTemplateVars) (string, error) {
	if vars.ModelRequested && !templateReferencesModel(tmpl) {
		requestedModel := vars.Model
		if requestedModel == "" {
			requestedModel = vars.ModelAlias
		}
		if requestedModel == "" {
			requestedModel = "<requested>"
		}
		if !strings.Contains(tmpl, "{{") {
			return "", fmt.Errorf(
				"model override %q was specified but agent command has no template syntax (no {{.Model}} or {{.ModelAlias}} placeholder); "+
					"the model would be silently ignored. Convert the command to template format or remove the model override. "+
					"Command: %s", requestedModel, tmpl)
		}
		return "", fmt.Errorf(
			"model override %q was specified but agent command template does not reference .Model or .ModelAlias; "+
				"the model would be silently ignored. Update the template or remove the model override. "+
				"Command: %s", requestedModel, tmpl)
	}

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
		Claude:   `{{memLimitPrefix}} claude --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .SystemPromptFile}} --system-prompt-file {{shellQuote .SystemPromptFile}}{{end}}`,
		Codex:    `{{if .SystemPromptFile}}CODEX_SYSTEM_PROMPT="$(cat {{shellQuote .SystemPromptFile}})" {{end}}codex --dangerously-bypass-approvals-and-sandbox -m {{shellQuote (.Model | default "gpt-5.3-codex")}} -c model_reasoning_effort="xhigh" -c model_reasoning_summary_format=experimental --search`,
		Gemini:   `gemini{{if .Model}} --model {{shellQuote .Model}}{{end}} --yolo`,
		Ollama:   `ollama run {{shellQuote (.Model | default "codellama:latest")}}`,
		Cursor:   `cursor{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Windsurf: `windsurf{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Aider:    `aider{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
	}
}
