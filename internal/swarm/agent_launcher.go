package swarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Agent command aliases used to start agents in panes.
const (
	AgentCC  = "cc"  // Claude Code alias
	AgentCOD = "cod" // Codex alias
	AgentGMI = "gmi" // Gemini-CLI alias
)

// LaunchResult represents the result of launching an agent in a pane.
type LaunchResult struct {
	SessionPane string `json:"session_pane"` // e.g., "cc_agents_1:1.5"
	AgentType   string `json:"agent_type"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
}

// AgentLauncherResult contains the results of launching agents.
type AgentLauncherResult struct {
	LaunchResults []LaunchResult `json:"launch_results"`
	TotalLaunched int            `json:"total_launched"`
	TotalFailed   int            `json:"total_failed"`
	Errors        []error        `json:"-"`
}

type agentLauncherProcessWaiter func(context.Context, string, string) (tmux.Pane, error)

// AgentLauncher handles launching agent commands in tmux panes.
type AgentLauncher struct {
	// TmuxClient is the tmux client used for sending keys.
	// If nil, the default tmux client is used.
	TmuxClient agentLauncherTmux

	// LaunchDelay is the delay between agent launches to avoid overwhelming
	// the terminal or hitting rate limits.
	LaunchDelay time.Duration

	// PostLaunchDelay is the delay after sending the command before sending Enter.
	// This gives the terminal time to process the command.
	PostLaunchDelay time.Duration

	// Logger for structured logging
	Logger *slog.Logger

	// waitForPaneProcessStart is injectable for focused launch verification.
	// A nil hook falls back to tmux.WaitForPaneProcessStartContext.
	waitForPaneProcessStart agentLauncherProcessWaiter
}

type agentLauncherTmux interface {
	SendKeys(target, keys string, enter bool) error
	GetPanes(session string) ([]tmux.Pane, error)
}

// formatPaneTarget formats a target string for tmux send-keys.
// It resolves the first window index of the session.
func formatPaneTarget(session string, pane int) string {
	firstWin, err := tmux.GetFirstWindow(session)
	if err != nil {
		firstWin = 1 // fallback
	}
	return fmt.Sprintf("%s:%d.%d", session, firstWin, pane)
}

type tmuxContextRunner interface {
	RunContext(ctx context.Context, args ...string) (string, error)
}

type swarmSessionTargeting struct {
	WindowIndex   int
	BasePaneIndex int
}

func resolveSwarmSessionTargeting(ctx context.Context, runner tmuxContextRunner, session string) (swarmSessionTargeting, error) {
	if runner == nil {
		return swarmSessionTargeting{}, errors.New("tmux runner is nil")
	}
	if session == "" {
		return swarmSessionTargeting{}, errors.New("session name required")
	}

	windowsOut, err := runner.RunContext(ctx, "list-windows", "-t", tmux.TargetSession(session), "-F", "#{window_index}")
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("list-windows: %w", err)
	}
	windowIndex, err := parsePrimaryWindowIndex(windowsOut)
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("parse window index: %w", err)
	}

	panesOut, err := runner.RunContext(ctx, "list-panes", "-t", tmux.ExactTarget(fmt.Sprintf("%s:%d", session, windowIndex)), "-F", "#{pane_index}")
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("list-panes: %w", err)
	}
	basePaneIndex, err := parseMinIntOutput(panesOut)
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("parse base pane index: %w", err)
	}

	return swarmSessionTargeting{WindowIndex: windowIndex, BasePaneIndex: basePaneIndex}, nil
}

func parsePrimaryWindowIndex(output string) (int, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return 0, errors.New("no values returned")
	}

	lines := strings.Split(trimmed, "\n")
	minVal := 0
	found := false
	hasWindowOne := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		val, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if val == 1 {
			hasWindowOne = true
		}
		if !found || val < minVal {
			minVal = val
			found = true
		}
	}

	if !found {
		return 0, errors.New("no valid integers returned")
	}
	if hasWindowOne {
		return 1, nil
	}
	return minVal, nil
}

func parseMinIntOutput(output string) (int, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return 0, errors.New("no values returned")
	}

	lines := strings.Split(trimmed, "\n")
	minVal := 0
	found := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		val, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if !found || val < minVal {
			minVal = val
			found = true
		}
	}

	if !found {
		return 0, errors.New("no valid integers returned")
	}
	return minVal, nil
}

func formatPaneTargetWithWindow(session string, windowIndex int, paneIndex int) string {
	return fmt.Sprintf("%s:%d.%d", session, windowIndex, paneIndex)
}

func swarmTmuxPaneIndex(basePaneIndex, planPaneIndex int) (int, error) {
	if planPaneIndex < 0 {
		return 0, fmt.Errorf("plan pane index must be >= 0, got %d", planPaneIndex)
	}
	if planPaneIndex == 0 {
		return basePaneIndex, nil
	}
	return basePaneIndex + (planPaneIndex - 1), nil
}

func swarmPaneTargetFromPlanIndex(session string, targeting swarmSessionTargeting, planPaneIndex int) (string, error) {
	tmuxPaneIndex, err := swarmTmuxPaneIndex(targeting.BasePaneIndex, planPaneIndex)
	if err != nil {
		return "", err
	}
	return formatPaneTargetWithWindow(session, targeting.WindowIndex, tmuxPaneIndex), nil
}

// DefaultAgentCommands maps agent types to their default binary names.
var DefaultAgentCommands = map[string]string{
	"cc":       "claude",       // Claude Code CLI
	"cod":      "codex",        // OpenAI Codex CLI
	"gmi":      "gemini",       // Google Gemini CLI
	"agy":      "agy",          // Antigravity CLI (resolved via config.AntigravityBinary at launch)
	"grok":     "grok",         // xAI Grok Build CLI
	"cursor":   "cursor-agent", // Cursor Agent CLI (not the GUI `cursor` launcher)
	"windsurf": "windsurf",     // Windsurf CLI
	"aider":    "aider",        // Aider CLI
	"ollama":   "ollama",       // Ollama CLI
}

// DefaultAgentArgs provides default arguments per agent type.
// When UseFullPaths is false (the default), agents are launched via shell
// aliases (cc, cod, gmi) that already include the correct flags, so no
// extra arguments are needed.  When UseFullPaths is true the binary name
// is used directly and the caller should supply arguments via
// WithAgentArgs or a custom LaunchCommandBuilder.
var DefaultAgentArgs = map[string][]string{
	"cc":  {},
	"cod": {},
	"gmi": {},
	// agy's model is hard-pinned (config.AntigravityRequiredModel); the shell
	// alias does NOT carry the pin, so it must ride the default args or a
	// swarm-launched agy pane silently runs whatever model the CLI defaults
	// to. The model is pre-quoted because ToShellCommand joins args with
	// spaces straight into a shell command.
	"agy":      {"--model", tmux.ShellQuote(config.AntigravityRequiredModel), "--dangerously-skip-permissions"},
	"grok":     agent.DefaultGrokAutomationShellArgs(),
	"cursor":   {"--yolo"},
	"windsurf": {},
	"aider":    {},
	"ollama":   {},
}

// LaunchCommand represents a complete agent launch specification.
type LaunchCommand struct {
	Binary    string   `json:"binary"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	WorkDir   string   `json:"work_dir,omitempty"`
	AgentType string   `json:"agent_type"`
}

// ToShellCommand converts the launch command to a shell command string for tmux.
func (lc LaunchCommand) ToShellCommand() string {
	result := lc.envPrefix() + lc.Binary
	for _, arg := range lc.Args {
		result += " " + arg
	}
	return result
}

// envPrefix renders Env as shell assignments ahead of the binary.
//
// BuildLaunchCommand has always populated Env from EnvVars and LOGGED the env
// keys as if they had been applied, but ToShellCommand rendered only
// Binary+Args — so every agent launched through this path ran with the
// operator's ambient environment instead. WithEnvVars was accepted, logged, and
// silently dropped: an API trap that would no-op the first time anyone used it
// (it is why a per-pane CODEX_HOME story cannot work through this launcher).
//
// Assignments are sorted because Env is built by ranging a map, which would
// otherwise make the rendered command nondeterministic and untestable, and
// values are shell-quoted because they reach a shell as text.
func (lc LaunchCommand) envPrefix() string {
	if len(lc.Env) == 0 {
		return ""
	}
	assignments := make([]string, 0, len(lc.Env))
	for _, entry := range lc.Env {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			// Not a KEY=VALUE assignment; passing it through would corrupt the
			// command line.
			continue
		}
		assignments = append(assignments, name+"="+tmux.ShellQuote(value))
	}
	if len(assignments) == 0 {
		return ""
	}
	sort.Strings(assignments)
	return strings.Join(assignments, " ") + " "
}

// ToSimpleCommand returns just the binary name without arguments.
// This is used when we want to rely on shell aliases.
func (lc LaunchCommand) ToSimpleCommand() string {
	return lc.Binary
}

// LaunchCommandBuilder generates agent launch commands with proper
// binary paths, arguments, and environment configuration.
type LaunchCommandBuilder struct {
	// AgentPaths maps agent types to custom binary paths.
	// If not specified, DefaultAgentCommands are used.
	AgentPaths map[string]string

	// AgentArgs maps agent types to custom arguments.
	// If not specified, DefaultAgentArgs are used.
	AgentArgs map[string][]string

	// EnvVars maps agent types to additional environment variables.
	EnvVars map[string]map[string]string

	// UseFullPaths determines whether to include full binary paths in commands.
	// If false, relies on shell aliases (cc, cod, gmi).
	UseFullPaths bool

	// Logger for structured logging.
	Logger *slog.Logger
}

// NewLaunchCommandBuilder creates a new LaunchCommandBuilder with default settings.
func NewLaunchCommandBuilder() *LaunchCommandBuilder {
	return &LaunchCommandBuilder{
		AgentPaths:   make(map[string]string),
		AgentArgs:    make(map[string][]string),
		EnvVars:      make(map[string]map[string]string),
		UseFullPaths: false, // Default to using shell aliases
		Logger:       slog.Default(),
	}
}

// WithAgentPath sets a custom binary path for an agent type.
func (b *LaunchCommandBuilder) WithAgentPath(agentType, path string) *LaunchCommandBuilder {
	b.AgentPaths[agentType] = path
	return b
}

// WithAgentArgs sets custom arguments for an agent type.
func (b *LaunchCommandBuilder) WithAgentArgs(agentType string, args []string) *LaunchCommandBuilder {
	b.AgentArgs[agentType] = args
	return b
}

// WithEnvVars sets environment variables for an agent type.
func (b *LaunchCommandBuilder) WithEnvVars(agentType string, env map[string]string) *LaunchCommandBuilder {
	b.EnvVars[agentType] = env
	return b
}

// WithLogger sets a custom logger.
func (b *LaunchCommandBuilder) WithLogger(logger *slog.Logger) *LaunchCommandBuilder {
	b.Logger = logger
	return b
}

// WithFullPaths enables using full binary paths instead of shell aliases.
func (b *LaunchCommandBuilder) WithFullPaths(enabled bool) *LaunchCommandBuilder {
	b.UseFullPaths = enabled
	return b
}

// logger returns the configured logger or the default logger.
func (b *LaunchCommandBuilder) loggerB() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return slog.Default()
}

// BuildLaunchCommand creates the launch command for an agent.
func (b *LaunchCommandBuilder) BuildLaunchCommand(spec PaneSpec, workDir string) LaunchCommand {
	rawAgentType := strings.TrimSpace(spec.AgentType)
	agentType := defaultSwarmLaunchCommand(rawAgentType)
	keys := launchLookupKeys(rawAgentType)

	// Determine binary
	var binary string
	if b.UseFullPaths {
		// Use custom path or default command
		for _, key := range keys {
			if customPath, ok := b.AgentPaths[key]; ok {
				binary = customPath
				break
			}
		}
		if binary == "" {
			for _, key := range keys {
				if defaultCmd, ok := DefaultAgentCommands[key]; ok {
					binary = defaultCmd
					break
				}
			}
		}
		if binary == "" {
			binary = agentType // Fallback to normalized or raw command name
		}
	} else {
		// Use shell aliases for cc/cod/gmi, but launch Cursor through its
		// headless agent binary rather than the GUI `cursor` launcher.
		binary = swarmLaunchBinary(agentType)
	}

	// Determine arguments
	var args []string
	for _, key := range keys {
		if customArgs, ok := b.AgentArgs[key]; ok {
			args = customArgs
			break
		}
	}
	if args == nil {
		for _, key := range keys {
			if defaultArgs, ok := DefaultAgentArgs[key]; ok {
				args = defaultArgs
				break
			}
		}
	}

	// Build environment variables
	var env []string
	for _, key := range keys {
		if agentEnv, ok := b.EnvVars[key]; ok {
			for k, v := range agentEnv {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
			break
		}
	}

	cmd := LaunchCommand{
		Binary:    binary,
		Args:      args,
		Env:       env,
		WorkDir:   workDir,
		AgentType: agentType,
	}

	// Log the command (env var names only for security)
	envKeys := make([]string, 0, len(env))
	for _, e := range env {
		idx := 0
		for i, c := range e {
			if c == '=' {
				idx = i
				break
			}
		}
		if idx > 0 {
			envKeys = append(envKeys, e[:idx])
		}
	}

	b.loggerB().Info("built launch command",
		"agent_type", agentType,
		"binary", binary,
		"args", args,
		"work_dir", workDir,
		"env_keys", envKeys)

	return cmd
}

// BuildSwarmCommands builds launch commands for all panes in a SwarmPlan.
func (b *LaunchCommandBuilder) BuildSwarmCommands(plan *SwarmPlan) []LaunchCommand {
	if plan == nil {
		return nil
	}

	var commands []LaunchCommand
	for _, session := range plan.Sessions {
		for _, pane := range session.Panes {
			workDir := pane.Project
			if workDir == "" {
				workDir = plan.ScanDir
			}
			cmd := b.BuildLaunchCommand(pane, workDir)
			commands = append(commands, cmd)
		}
	}

	return commands
}

func normalizedSwarmLaunchableAgentType(agentType string) string {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeAntigravity,
		agent.AgentTypeGrok,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama:
		return string(canonical)
	default:
		return ""
	}
}

// swarmLaunchBinary chooses the executable for a canonical swarm agent type.
// Most built-in types intentionally use their established shell aliases. Cursor
// is the exception: its `cursor` command is the GUI launcher, while
// `cursor-agent` is the headless CLI that can run inside a tmux pane.
func swarmLaunchBinary(agentType string) string {
	if agentType == "cursor" {
		return DefaultAgentCommands[agentType]
	}
	if agentType == "agy" {
		// `agy` is frequently a shell ALIAS for agy-locked; aliases do not
		// resolve in non-interactive launch shells, so prefer the real binary.
		return config.AntigravityBinary()
	}
	return agentType
}

func defaultSwarmLaunchCommand(agentType string) string {
	if normalized := normalizedSwarmLaunchableAgentType(agentType); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(agentType)
}

func launchLookupKeys(agentType string) []string {
	raw := strings.TrimSpace(agentType)
	normalized := normalizedSwarmLaunchableAgentType(raw)

	keys := make([]string, 0, 2)
	if normalized != "" {
		keys = append(keys, normalized)
	}
	if raw != "" && raw != normalized {
		keys = append(keys, raw)
	}
	return keys
}
