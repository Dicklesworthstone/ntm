package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// AgentTemplateVars contains variables available for agent command templates
type AgentTemplateVars struct {
	Model            string // Resolved full model name (e.g., "claude-opus-4-20250514")
	ModelAlias       string // Original alias as specified (e.g., "opus")
	ModelRequested   bool   // True when the user explicitly requested a non-default model
	SessionName      string // NTM session name
	PaneIndex        int    // Pane number (1-based)
	AgentType        string // Agent type: "cc", "cod", "gmi", "agy", "grok"
	ProjectDir       string // Project directory path
	SystemPrompt     string // System prompt content (if any)
	SystemPromptFile string // Path to system prompt file (if any)
	PersonaName      string // Name of persona (if any)
	// ReasoningEffort sets the model's reasoning budget. Currently
	// consumed by the Codex template (passes `-c
	// model_reasoning_effort=...`). Empty falls back to the
	// template-level default. See ntm#140.
	ReasoningEffort string
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

// agyBinary resolves the Antigravity CLI launch binary, evaluated at render
// (spawn) time so the choice reflects the launching shell's real PATH.
//
// Sharp edge: on many boxes `agy` is a shell ALIAS pointing at a wrapper such
// as ~/.local/bin/agy-locked (a Python launcher). Shell aliases do NOT resolve
// in NTM's non-interactive launch shell, so an `agy` command would fail to
// start the pane. When the real `agy-locked` binary is present on PATH we use
// it directly; otherwise we fall back to the plain `agy` binary (which is a
// real executable on installs that ship it un-aliased).
func agyBinary() string {
	if _, err := exec.LookPath("agy-locked"); err == nil {
		return "agy-locked"
	}
	return "agy"
}

// AntigravityBinary exposes the agy launch-binary resolution (agy-locked when
// on PATH, else agy) to launch paths outside the template engine — e.g. the
// swarm agent launcher — so every agy launch shares the same alias-safe
// binary choice.
func AntigravityBinary() string {
	return agyBinary()
}

// templateFuncs contains custom functions available in templates
var templateFuncs = template.FuncMap{
	// agyBinary resolves the Antigravity CLI binary (agy-locked if on PATH,
	// else agy) at render time — see agyBinary() for the alias sharp edge.
	"agyBinary": agyBinary,
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

// templateReferencesAnyField reports whether a parsed template reads one of
// the named AgentTemplateVars fields. It deliberately inspects the executable
// template AST instead of searching the source text: a field name in a comment
// or quoted literal does not carry a requested model or effort into the launch
// command and must not satisfy the silent-drop guard.
func templateReferencesAnyField(tmpl *template.Template, fields ...string) bool {
	if tmpl == nil {
		return false
	}
	wanted := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		wanted[field] = struct{}{}
	}
	for _, defined := range tmpl.Templates() {
		if defined.Tree != nil && parseNodeReferencesAnyField(defined.Tree.Root, wanted) {
			return true
		}
	}
	return false
}

func parseNodeReferencesAnyField(node parse.Node, wanted map[string]struct{}) bool {
	if node == nil || (reflect.ValueOf(node).Kind() == reflect.Ptr && reflect.ValueOf(node).IsNil()) {
		return false
	}
	switch n := node.(type) {
	case *parse.ListNode:
		for _, child := range n.Nodes {
			if parseNodeReferencesAnyField(child, wanted) {
				return true
			}
		}
	case *parse.ActionNode:
		return parsePipeReferencesAnyField(n.Pipe, wanted)
	case *parse.IfNode:
		return parsePipeReferencesAnyField(n.Pipe, wanted) ||
			parseNodeReferencesAnyField(n.List, wanted) ||
			parseNodeReferencesAnyField(n.ElseList, wanted)
	case *parse.RangeNode:
		return parsePipeReferencesAnyField(n.Pipe, wanted) ||
			parseNodeReferencesAnyField(n.List, wanted) ||
			parseNodeReferencesAnyField(n.ElseList, wanted)
	case *parse.WithNode:
		return parsePipeReferencesAnyField(n.Pipe, wanted) ||
			parseNodeReferencesAnyField(n.List, wanted) ||
			parseNodeReferencesAnyField(n.ElseList, wanted)
	case *parse.TemplateNode:
		return parsePipeReferencesAnyField(n.Pipe, wanted)
	}
	return false
}

func parsePipeReferencesAnyField(pipe *parse.PipeNode, wanted map[string]struct{}) bool {
	if pipe == nil {
		return false
	}
	for _, command := range pipe.Cmds {
		for _, arg := range command.Args {
			if parseArgumentReferencesAnyField(arg, wanted) {
				return true
			}
		}
	}
	return false
}

func parseArgumentReferencesAnyField(node parse.Node, wanted map[string]struct{}) bool {
	switch value := node.(type) {
	case *parse.FieldNode:
		if len(value.Ident) > 0 {
			_, ok := wanted[value.Ident[0]]
			return ok
		}
	case *parse.ChainNode:
		return parseArgumentReferencesAnyField(value.Node, wanted)
	case *parse.PipeNode:
		return parsePipeReferencesAnyField(value, wanted)
	}
	return false
}

// agentTypeConsumesReasoningEffort reports whether a reasoning-effort override
// is meaningful for an agent type.
//
// It gates the silent-drop guard below, because for some providers the effort
// is inert BY DESIGN rather than by omission: opencode's root TUI command
// rejects the flag that would carry it, so its template deliberately does not
// reference .ReasoningEffort and an effort passed there must be dropped
// quietly. Only types whose launch command actually takes an effort knob should
// make a dropped effort an error.
//
// Kept in sync with agentTypeSupportsEffortSuffix in internal/cli, which gates
// the `N:model@effort` spec shorthand for the same set.
func agentTypeConsumesReasoningEffort(agentType string) bool {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "cc", "claude", "cod", "codex", "grok":
		return true
	default:
		return false
	}
}

// personaDropError is the loud-refusal error for a persona system prompt that
// the launch command cannot deliver. Silent drop is the sin, not the
// limitation (bd-ws7-docs-ux-truth-tqh3l.5): a persona the operator asked for
// must either reach the agent or fail the launch with a documented error.
// Grok gets its own message because the omission there is BY DESIGN — the
// Grok Build CLI has no system-prompt flag or env var — matching the
// phase-one fail-closed pattern in validateGrokPhaseOneSpawn.
func personaDropError(agentType, tmpl string) error {
	if strings.ToLower(strings.TrimSpace(agentType)) == "grok" {
		return fmt.Errorf(
			"persona ignored: grok has no persona mechanism (the Grok Build CLI exposes no system-prompt flag or env var); "+
				"remove the persona from the grok agent spec. Command: %s", tmpl)
	}
	return fmt.Errorf(
		"persona system prompt was prepared but agent command template does not reference .SystemPromptFile; "+
			"the persona would be silently ignored. Update the template or remove the persona. "+
			"Command: %s", tmpl)
}

// GenerateAgentCommand renders an agent command template with the given variables.
// Legacy commands without template syntax are returned as-is unless they would
// silently drop an explicitly requested model selection.
// Returns an error if template parsing or execution fails.
func GenerateAgentCommand(tmpl string, vars AgentTemplateVars) (string, error) {
	// Fast path: if no template syntax, return as-is unless an explicit model,
	// effort, or persona system prompt would be silently dropped.
	if !strings.Contains(tmpl, "{{") {
		if strings.TrimSpace(vars.SystemPromptFile) != "" {
			return "", personaDropError(vars.AgentType, tmpl)
		}
		if !vars.ModelRequested && (strings.TrimSpace(vars.ReasoningEffort) == "" || !agentTypeConsumesReasoningEffort(vars.AgentType)) {
			return tmpl, nil
		}
		if vars.ModelRequested {
			requestedModel := vars.Model
			if requestedModel == "" {
				requestedModel = vars.ModelAlias
			}
			if requestedModel == "" {
				requestedModel = "<requested>"
			}
			return "", fmt.Errorf(
				"model override %q was specified but agent command has no template syntax (no {{.Model}} or {{.ModelAlias}} placeholder); "+
					"the model would be silently ignored. Convert the command to template format or remove the model override. "+
					"Command: %s", requestedModel, tmpl)
		}
		return "", fmt.Errorf(
			"reasoning effort %q was specified but agent command has no template syntax (no {{.ReasoningEffort}} placeholder); "+
				"the effort would be silently ignored. Convert the command to template format or remove the effort override. "+
				"Command: %s", vars.ReasoningEffort, tmpl)
	}

	t, err := template.New("agent").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return "", err
	}

	if vars.ModelRequested && !templateReferencesAnyField(t, "Model", "ModelAlias") {
		requestedModel := vars.Model
		if requestedModel == "" {
			requestedModel = vars.ModelAlias
		}
		if requestedModel == "" {
			requestedModel = "<requested>"
		}
		return "", fmt.Errorf(
			"model override %q was specified but agent command template does not reference .Model or .ModelAlias; "+
				"the model would be silently ignored. Update the template or remove the model override. "+
				"Command: %s", requestedModel, tmpl)
	}

	// The same guard for reasoning effort. Without it the model half of
	// `--cod=N:model:effort` failed loudly while the effort half vanished in
	// silence, so an operator who fixed their template for the model still got
	// the default reasoning budget — and a swarm's cost and quality changed
	// with nothing to notice (bd-ywwam).
	if strings.TrimSpace(vars.ReasoningEffort) != "" &&
		agentTypeConsumesReasoningEffort(vars.AgentType) &&
		!templateReferencesAnyField(t, "ReasoningEffort") {
		return "", fmt.Errorf(
			"reasoning effort %q was specified but agent command template does not reference .ReasoningEffort; "+
				"the effort would be silently ignored. Update the template or remove the effort override. "+
				"Command: %s", vars.ReasoningEffort, tmpl)
	}

	// The same guard for persona system prompts. Personas for gmi/agy/grok
	// used to be dropped silently because their templates never referenced
	// .SystemPromptFile — the operator got a generic agent while the UI said
	// a persona was applied (bd-ws7-docs-ux-truth-tqh3l.5).
	if strings.TrimSpace(vars.SystemPromptFile) != "" &&
		!templateReferencesAnyField(t, "SystemPromptFile") {
		return "", personaDropError(vars.AgentType, tmpl)
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
		Claude: `{{memLimitPrefix}} claude --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}} --effort {{shellQuote (.ReasoningEffort | default "` + DefaultClaudeReasoningEffort + `")}}{{if .SystemPromptFile}} --system-prompt-file {{shellQuote .SystemPromptFile}}{{end}}`,
		Codex:  `{{if .SystemPromptFile}}CODEX_SYSTEM_PROMPT="$(cat {{shellQuote .SystemPromptFile}})" {{end}}codex --dangerously-bypass-approvals-and-sandbox -m {{shellQuote (.Model | default "` + DefaultCodexModel + `")}} -c model_reasoning_effort={{shellQuote (.ReasoningEffort | default "` + DefaultCodexReasoningEffort + `")}} -c model_reasoning_summary_format=experimental --search`,
		// Gemini has no --system-prompt flag; the CLI's documented persona
		// mechanism is the GEMINI_SYSTEM_MD env var, a path whose file contents
		// REPLACE the core system prompt (getCoreSystemPrompt resolves it via
		// resolvePathFromEnv). Same env-prefix shape as the Codex template.
		Gemini: `{{if .SystemPromptFile}}GEMINI_SYSTEM_MD={{shellQuote .SystemPromptFile}} {{end}}gemini{{if .Model}} --model {{shellQuote .Model}}{{end}} --yolo`,
		// Antigravity (agy): the model is hard-pinned to "Gemini 3.7 Flash (High)"
		// by ResolveModel, so --model is always injected. --dangerously-skip-permissions
		// is agy's autonomous (auto-approve) flag — the equivalent of gemini's --yolo —
		// which the dcg agy guard (F5) backstops. {{agyBinary}} resolves the real
		// launch binary (agy-locked when present, else agy) because `agy` is often a
		// shell alias that will not resolve in NTM's non-interactive launch shell.
		// Persona delivery: the Antigravity CLI has no system-prompt flag or
		// env var (verified against `agy --help` and the binary), so the
		// persona system prompt is prepended as the session's initial prompt
		// via --prompt-interactive ("run an initial prompt interactively and
		// continue the session") — the bead-designed prepend-to-first-prompt
		// fallback for CLIs without a true system-prompt mechanism.
		Antigravity: `{{agyBinary}} --model {{shellQuote .Model}} --dangerously-skip-permissions{{if .SystemPromptFile}} --prompt-interactive "$(cat {{shellQuote .SystemPromptFile}})"{{end}}`,
		// Provider-native automation uses ACP. This TUI compatibility launch is
		// bound to a named least-privilege policy and never broad approval.
		Grok:       agent.DefaultGrokAutomationCommandTemplate,
		GrokPolicy: agent.DefaultGrokAutomationPolicyName,
		Ollama:     `ollama run {{shellQuote (.Model | default "codellama:latest")}}`,
		// Cursor: launch the Cursor Agent CLI (`cursor-agent`), NOT the `cursor`
		// IDE binary — on Linux `cursor` is the GUI editor launcher (useless in a
		// tmux pane) and often absent on headless hosts entirely (GH#233).
		// `--yolo` is the CLI's autonomous auto-approval flag (alias of --force).
		// Deliberately NOT injected:
		//   --trust        rejected by the interactive REPL ("--trust can only be
		//                  used with --print/headless mode") — it would fail the
		//                  pane at launch;
		//   --approve-mcps only meaningful with --plugin-dir.
		// Deliberately NOT used: the bare `agent` binary name some Cursor installs
		// ship — it collides with Grok Build's `agent` binary on multi-CLI boxes.
		// Override [agents].cursor to opt into different flags or a different
		// binary name.
		Cursor:   `cursor-agent --yolo{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Windsurf: `windsurf{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Aider:    `aider{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		// Opencode (oc): the upstream `opencode` binary takes `-m/--model
		// provider/model`. Without the {{.Model}} placeholder a
		// `--oc=N:provider/model` spawn is rejected ("agent command has no
		// template syntax") and Agent Mail registration fails ("model cannot
		// be empty"). NOTE: `--variant` (reasoning effort) is NOT a flag on the
		// root `opencode` TUI command an interactive pane launches — it exists
		// only on the `opencode run` subcommand (anomalyco/opencode#7354, PR
		// #7358 still open/unmerged), so injecting it here would make the pane
		// fail to launch whenever an effort is supplied. See ntm#116, ntm#193.
		Opencode: DefaultOpencodeCommand,
	}
}

// DefaultOpencodeCommand is the launch command used when [agents] oc is not
// configured. It mirrors DefaultAgentTemplates().Opencode so that the spawn,
// add, and restart dispatch paths inject the model the same way a freshly
// generated config does. Only `--model` is injected: it is the lone
// model/reasoning flag the root `opencode` TUI command accepts (the
// `--variant` effort flag lives on the `opencode run` subcommand only — see
// the note in DefaultAgentTemplates). See ntm#193.
const DefaultOpencodeCommand = `opencode{{if .Model}} --model {{shellQuote .Model}}{{end}}`
