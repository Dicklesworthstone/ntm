package swarm

import (
	"fmt"
	"log/slog"
	"time"

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
}

type agentLauncherTmux interface {
	SendKeys(target, keys string, enter bool) error
	GetPanes(session string) ([]tmux.Pane, error)
	GetFirstWindow(session string) (int, error)
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

// formatPaneTarget formats a target string for tmux send-keys.
// Uses the format "session:window.pane" with the actual window index from tmux.
// Uses the default tmux client - prefer formatPaneTargetWithClient when a custom
// client is available.
func formatPaneTarget(session string, pane int) (string, error) {
	return tmux.FormatPaneTarget(session, pane)
}

// formatPaneTargetWithClient formats a target string using the provided client's
// GetFirstWindow method to get the actual window index.
func formatPaneTargetWithClient(client agentLauncherTmux, session string, pane int) (string, error) {
	firstWin, err := client.GetFirstWindow(session)
	if err != nil {
		return "", fmt.Errorf("get first window for %s: %w", session, err)
	}
	return tmux.FormatPaneTargetWithWindow(session, firstWin, pane), nil
}

// LaunchAgent starts an agent in a specific pane.
// session is the tmux session name, pane is the 1-based pane index,
// and agentType is the agent command (cc, cod, or gmi).
func (l *AgentLauncher) LaunchAgent(session string, pane int, agentType string) error {
	target, err := formatPaneTargetWithClient(l.tmuxClient(), session, pane)
	if err != nil {
		return fmt.Errorf("format pane target: %w", err)
	}

	l.logger().Debug("launching agent",
		"session", session,
		"pane", pane,
		"target", target,
		"agent_type", agentType)

	client := l.tmuxClient()

	// Send agent command (the alias like "cc", "cod", or "gmi")
	if err := client.SendKeys(target, agentType, false); err != nil {
		return fmt.Errorf("send agent command to %s: %w", target, err)
	}

	// Small delay for terminal to process before sending Enter
	if l.PostLaunchDelay > 0 {
		time.Sleep(l.PostLaunchDelay)
	}

	// Send Enter to execute the command
	if err := client.SendKeys(target, "", true); err != nil {
		return fmt.Errorf("send enter to %s: %w", target, err)
	}

	l.logger().Info("agent launched",
		"target", target,
		"agent_type", agentType)

	return nil
}

// LaunchAllInSession launches the same agent type in all panes of a session.
// It gets the list of panes from tmux and sends the agent command to each.
func (l *AgentLauncher) LaunchAllInSession(session string, agentType string) error {
	client := l.tmuxClient()

	panes, err := client.GetPanes(session)
	if err != nil {
		return fmt.Errorf("get panes for session %q: %w", session, err)
	}

	if len(panes) == 0 {
		return fmt.Errorf("session %q has no panes", session)
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

			target, err := formatPaneTargetWithClient(l.tmuxClient(), sessionSpec.Name, paneSpec.Index)
			if err != nil {
				launchResult := LaunchResult{
					SessionPane: fmt.Sprintf("%s:?.%d", sessionSpec.Name, paneSpec.Index),
					AgentType:   paneSpec.AgentType,
					Success:     false,
					Error:       fmt.Sprintf("format pane target: %v", err),
				}
				result.LaunchResults = append(result.LaunchResults, launchResult)
				result.TotalFailed++
				result.Errors = append(result.Errors, err)
				continue
			}
			launchResult := LaunchResult{
				SessionPane: target,
				AgentType:   paneSpec.AgentType,
				Success:     true,
			}

			// Determine the launch command
			launchCmd := paneSpec.LaunchCmd
			if launchCmd == "" {
				launchCmd = paneSpec.AgentType
			}

			if err := l.LaunchAgent(sessionSpec.Name, paneSpec.Index, launchCmd); err != nil {
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
	switch agentType {
	case AgentCC, AgentCOD, AgentGMI:
		return nil
	default:
		return fmt.Errorf("invalid agent type %q: must be one of %s, %s, %s",
			agentType, AgentCC, AgentCOD, AgentGMI)
	}
}

// DefaultAgentCommands maps agent types to their default binary names.
var DefaultAgentCommands = map[string]string{
	"cc":  "claude", // Claude Code CLI
	"cod": "codex",  // OpenAI Codex CLI
	"gmi": "gemini", // Google Gemini CLI
}

// DefaultAgentArgs provides default arguments per agent type.
var DefaultAgentArgs = map[string][]string{
	"cc":  {"--dangerously-skip-permissions"},
	"cod": {"--quiet", "--auto-approve"},
	"gmi": {"--non-interactive"},
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
	if len(lc.Args) == 0 {
		return lc.Binary
	}
	result := lc.Binary
	for _, arg := range lc.Args {
		result += " " + arg
	}
	return result
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
	agentType := spec.AgentType

	// Determine binary
	var binary string
	if b.UseFullPaths {
		// Use custom path or default command
		if customPath, ok := b.AgentPaths[agentType]; ok {
			binary = customPath
		} else if defaultCmd, ok := DefaultAgentCommands[agentType]; ok {
			binary = defaultCmd
		} else {
			binary = agentType // Fallback to agent type as command
		}
	} else {
		// Use shell alias (cc, cod, gmi)
		binary = agentType
	}

	// Determine arguments
	var args []string
	if customArgs, ok := b.AgentArgs[agentType]; ok {
		args = customArgs
	} else if defaultArgs, ok := DefaultAgentArgs[agentType]; ok {
		args = defaultArgs
	}

	// Build environment variables
	var env []string
	if agentEnv, ok := b.EnvVars[agentType]; ok {
		for k, v := range agentEnv {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
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
	client := l.tmuxClient()
	target, err := formatPaneTargetWithClient(client, session, pane)
	if err != nil {
		return fmt.Errorf("format pane target: %w", err)
	}

	l.logger().Debug("launching agent with command",
		"session", session,
		"pane", pane,
		"target", target,
		"command", cmd.ToShellCommand())

	// Send the shell command
	shellCmd := cmd.ToShellCommand()
	if err := client.SendKeys(target, shellCmd, false); err != nil {
		return fmt.Errorf("send agent command to %s: %w", target, err)
	}

	// Small delay for terminal to process before sending Enter
	if l.PostLaunchDelay > 0 {
		time.Sleep(l.PostLaunchDelay)
	}

	// Send Enter to execute the command
	if err := client.SendKeys(target, "", true); err != nil {
		return fmt.Errorf("send enter to %s: %w", target, err)
	}

	l.logger().Info("agent launched with command",
		"target", target,
		"agent_type", cmd.AgentType,
		"command", shellCmd)

	return nil
}
