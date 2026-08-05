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

// NewAgentLauncher creates a new AgentLauncher with default settings.
func NewAgentLauncher() *AgentLauncher {
	return &AgentLauncher{
		TmuxClient:      nil, // Use default client
		LaunchDelay:     200 * time.Millisecond,
		PostLaunchDelay: 50 * time.Millisecond,
		Logger:          slog.Default(),
	}
}

// NewAgentLauncherWithClient creates an AgentLauncher with a custom tmux client.
func NewAgentLauncherWithClient(client agentLauncherTmux) *AgentLauncher {
	return &AgentLauncher{
		TmuxClient:      client,
		LaunchDelay:     200 * time.Millisecond,
		PostLaunchDelay: 50 * time.Millisecond,
		Logger:          slog.Default(),
	}
}

// NewAgentLauncherWithLogger creates an AgentLauncher with a custom logger.
func NewAgentLauncherWithLogger(logger *slog.Logger) *AgentLauncher {
	return &AgentLauncher{
		TmuxClient:      nil,
		LaunchDelay:     200 * time.Millisecond,
		PostLaunchDelay: 50 * time.Millisecond,
		Logger:          logger,
	}
}

// tmuxClient returns the configured tmux client or the default client.
func (l *AgentLauncher) tmuxClient() agentLauncherTmux {
	if l.TmuxClient != nil {
		return l.TmuxClient
	}
	return tmux.DefaultClient
}

// logger returns the configured logger or the default logger.
func (l *AgentLauncher) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

func (l *AgentLauncher) processStartWaiter() agentLauncherProcessWaiter {
	if l.waitForPaneProcessStart != nil {
		return l.waitForPaneProcessStart
	}
	return tmux.WaitForPaneProcessStartContext
}

func (l *AgentLauncher) resolvePane(session string, paneIndex int) (tmux.Pane, error) {
	panes, err := l.tmuxClient().GetPanes(session)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("get panes for session %q: %w", session, err)
	}
	for _, pane := range panes {
		if pane.Index != paneIndex {
			continue
		}
		if strings.TrimSpace(pane.ID) == "" {
			return tmux.Pane{}, fmt.Errorf("pane %d in session %q has no stable pane ID", paneIndex, session)
		}
		return pane, nil
	}
	return tmux.Pane{}, fmt.Errorf("pane %d not found in session %q", paneIndex, session)
}

func (l *AgentLauncher) launchAgentCommand(session string, pane int, agentType, launchCommand string) error {
	isGrok := agent.AgentType(agentType).Canonical() == agent.AgentTypeGrok
	target := ""
	if isGrok {
		resolvedPane, err := l.resolvePane(session, pane)
		if err != nil {
			return fmt.Errorf("resolve Grok Build pane before launch: %w", err)
		}
		if err := tmux.ValidatePaneLaunchBaseline(resolvedPane); err != nil {
			return fmt.Errorf("validate Grok Build pane before launch: %w", err)
		}
		target = resolvedPane.ID
	} else {
		target = formatPaneTarget(session, pane)
	}

	l.logger().Debug("launching agent",
		"session", session,
		"pane", pane,
		"target", target,
		"agent_type", agentType,
		"launch_command", launchCommand)

	client := l.tmuxClient()
	if err := client.SendKeys(target, launchCommand, false); err != nil {
		return fmt.Errorf("send agent command to %s: %w", target, err)
	}

	if l.PostLaunchDelay > 0 {
		time.Sleep(l.PostLaunchDelay)
	}

	if err := client.SendKeys(target, "", true); err != nil {
		return fmt.Errorf("send enter to %s: %w", target, err)
	}

	if isGrok {
		if _, err := l.processStartWaiter()(context.Background(), session, target); err != nil {
			return fmt.Errorf("phase-one Grok Build in pane %s did not start a stable process: %w", target, err)
		}
	}

	l.logger().Info("agent launched",
		"target", target,
		"agent_type", agentType,
		"launch_command", launchCommand)

	return nil
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

	windowsOut, err := runner.RunContext(ctx, "list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("list-windows: %w", err)
	}
	windowIndex, err := parsePrimaryWindowIndex(windowsOut)
	if err != nil {
		return swarmSessionTargeting{}, fmt.Errorf("parse window index: %w", err)
	}

	panesOut, err := runner.RunContext(ctx, "list-panes", "-t", fmt.Sprintf("%s:%d", session, windowIndex), "-F", "#{pane_index}")
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

// LaunchAgent starts an agent in a specific pane.
// session is the tmux session name, pane is the 1-based pane index,
// and agentType is the agent command (cc, cod, gmi, or grok).
func (l *AgentLauncher) LaunchAgent(session string, pane int, agentType string) error {
	launchCommand := interactiveSwarmLaunchCommand(agentType)
	return l.launchAgentCommand(session, pane, agentType, launchCommand)
}

// LaunchAllInSession launches the same agent type in all panes of a session.
// It gets the list of panes from tmux and sends the agent command to each.
func (l *AgentLauncher) LaunchAllInSession(session string, agentType string) error {
	client := l.tmuxClient()
	normalizedAgentType := normalizedSwarmLaunchableAgentType(agentType)
	if normalizedAgentType != "" {
		agentType = normalizedAgentType
	}

	panes, err := client.GetPanes(session)
	if err != nil {
		return fmt.Errorf("get panes for session %q: %w", session, err)
	}

	if len(panes) == 0 {
		return fmt.Errorf("session %q has no panes", session)
	}

	if agent.AgentType(agentType).Canonical() == agent.AgentTypeGrok {
		for _, pane := range panes {
			if pane.Index == 0 {
				continue
			}
			if err := tmux.ValidatePaneLaunchBaseline(pane); err != nil {
				return fmt.Errorf(
					"preflight Grok Build pane %d in session %q: %w",
					pane.Index,
					session,
					err,
				)
			}
		}
	}

	l.logger().Info("launching agents in session",
		"session", session,
		"agent_type", agentType,
		"pane_count", len(panes))

	for i, pane := range panes {
		// Skip user pane (index 0) - it's for manual commands
		if pane.Index == 0 {
			l.logger().Debug("skipping user pane",
				"session", session,
				"pane_index", 0)
			continue
		}

		// Stagger launches to avoid rate limits
		if i > 0 && l.LaunchDelay > 0 {
			time.Sleep(l.LaunchDelay)
		}

		if err := l.LaunchAgent(session, pane.Index, agentType); err != nil {
			l.logger().Error("failed to launch agent in pane",
				"session", session,
				"pane_index", pane.Index,
				"agent_type", agentType,
				"error", err)
			// Continue with other panes
		}
	}

	return nil
}

// LaunchSwarm launches all agents according to a SwarmPlan.
// It iterates through all sessions and panes in the plan, launching
// the appropriate agent in each pane.
func (l *AgentLauncher) LaunchSwarm(plan *SwarmPlan) (*AgentLauncherResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	result := &AgentLauncherResult{
		LaunchResults: make([]LaunchResult, 0),
	}

	for _, sessionSpec := range plan.Sessions {
		hasGrok := false
		for _, paneSpec := range sessionSpec.Panes {
			if agent.AgentType(paneSpec.AgentType).Canonical() == agent.AgentTypeGrok {
				hasGrok = true
				break
			}
		}
		if !hasGrok {
			continue
		}

		panes, err := l.tmuxClient().GetPanes(sessionSpec.Name)
		if err != nil {
			return result, fmt.Errorf("preflight Grok Build panes in session %q: %w", sessionSpec.Name, err)
		}
		panesByIndex := make(map[int]tmux.Pane, len(panes))
		for _, pane := range panes {
			panesByIndex[pane.Index] = pane
		}
		for _, paneSpec := range sessionSpec.Panes {
			if agent.AgentType(paneSpec.AgentType).Canonical() != agent.AgentTypeGrok {
				continue
			}
			pane, ok := panesByIndex[paneSpec.Index]
			if !ok {
				return result, fmt.Errorf(
					"preflight Grok Build pane %d in session %q: pane not found",
					paneSpec.Index,
					sessionSpec.Name,
				)
			}
			if strings.TrimSpace(pane.ID) == "" {
				return result, fmt.Errorf(
					"preflight Grok Build pane %d in session %q: pane has no stable pane ID",
					paneSpec.Index,
					sessionSpec.Name,
				)
			}
			if err := tmux.ValidatePaneLaunchBaseline(pane); err != nil {
				return result, fmt.Errorf(
					"preflight Grok Build pane %d in session %q: %w",
					paneSpec.Index,
					sessionSpec.Name,
					err,
				)
			}
		}
	}

	l.logger().Info("launching swarm agents",
		"total_sessions", len(plan.Sessions),
		"total_agents", plan.TotalAgents)

	for _, sessionSpec := range plan.Sessions {
		l.logger().Debug("processing session",
			"session", sessionSpec.Name,
			"agent_type", sessionSpec.AgentType,
			"pane_count", len(sessionSpec.Panes))

		for i, paneSpec := range sessionSpec.Panes {
			// Stagger launches
			if i > 0 && l.LaunchDelay > 0 {
				time.Sleep(l.LaunchDelay)
			}

			target := formatPaneTarget(sessionSpec.Name, paneSpec.Index)
			launchResult := LaunchResult{
				SessionPane: target,
				AgentType:   paneSpec.AgentType,
				Success:     true,
			}

			// Determine the launch command
			launchCmd := paneSpec.LaunchCmd
			if launchCmd == "" {
				launchCmd = interactiveSwarmLaunchCommand(paneSpec.AgentType)
			}

			if err := l.launchAgentCommand(sessionSpec.Name, paneSpec.Index, paneSpec.AgentType, launchCmd); err != nil {
				launchResult.Success = false
				launchResult.Error = err.Error()
				result.TotalFailed++
				result.Errors = append(result.Errors, err)

				l.logger().Error("failed to launch agent",
					"target", target,
					"agent_type", paneSpec.AgentType,
					"error", err)
			} else {
				result.TotalLaunched++
			}

			result.LaunchResults = append(result.LaunchResults, launchResult)
		}
	}

	l.logger().Info("swarm launch complete",
		"total_launched", result.TotalLaunched,
		"total_failed", result.TotalFailed)

	return result, nil
}

// LaunchAgentWithContext launches an agent with additional context for the pane.
// This is a convenience wrapper around LaunchAgent that logs more details.
func (l *AgentLauncher) LaunchAgentWithContext(session string, pane int, agentType string, project string) error {
	l.logger().Info("launching agent with context",
		"session", session,
		"pane", pane,
		"agent_type", agentType,
		"project", project)

	return l.LaunchAgent(session, pane, agentType)
}

// ValidateAgentType checks if the given agent type is valid.
func ValidateAgentType(agentType string) error {
	switch normalizedSwarmLaunchableAgentType(agentType) {
	case AgentCC, AgentCOD, AgentGMI, "grok", "cursor", "windsurf", "aider", "ollama":
		return nil
	default:
		return fmt.Errorf("invalid agent type %q: must resolve to one of %s, %s, %s, grok, cursor, windsurf, aider, ollama",
			agentType, AgentCC, AgentCOD, AgentGMI)
	}
}

// DefaultAgentCommands maps agent types to their default binary names.
var DefaultAgentCommands = map[string]string{
	"cc":       "claude",   // Claude Code CLI
	"cod":      "codex",    // OpenAI Codex CLI
	"gmi":      "gemini",   // Google Gemini CLI
	"grok":     "grok",     // xAI Grok Build CLI
	"cursor":   "cursor",   // Cursor CLI
	"windsurf": "windsurf", // Windsurf CLI
	"aider":    "aider",    // Aider CLI
	"ollama":   "ollama",   // Ollama CLI
}

// DefaultAgentArgs provides default arguments per agent type.
// When UseFullPaths is false (the default), agents are launched via shell
// aliases (cc, cod, gmi) that already include the correct flags, so no
// extra arguments are needed.  When UseFullPaths is true the binary name
// is used directly and the caller should supply arguments via
// WithAgentArgs or a custom LaunchCommandBuilder.
var DefaultAgentArgs = map[string][]string{
	"cc":       {},
	"cod":      {},
	"gmi":      {},
	"grok":     {"--always-approve"},
	"cursor":   {},
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
		// Use shell alias or canonical agent command token.
		binary = agentType
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

// LaunchAgentWithCommand launches an agent using a pre-built LaunchCommand.
func (l *AgentLauncher) LaunchAgentWithCommand(session string, pane int, cmd LaunchCommand) error {
	shellCmd := cmd.ToShellCommand()
	return l.launchAgentCommand(session, pane, cmd.AgentType, shellCmd)
}

func normalizedSwarmLaunchableAgentType(agentType string) string {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
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

func interactiveSwarmLaunchCommand(agentType string) string {
	if agent.AgentType(agentType).Canonical() == agent.AgentTypeGrok {
		return "grok --always-approve"
	}
	return defaultSwarmLaunchCommand(agentType)
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
