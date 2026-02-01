package swarm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// PaneLauncher handles agent launching with directory setup.
// It ensures agents are launched in the correct project directory context.
type PaneLauncher struct {
	// TmuxClient for sending commands to panes.
	// If nil, the default tmux client is used.
	TmuxClient *tmux.Client

	// CmdBuilder generates agent launch commands.
	// If nil, a default builder is created.
	CmdBuilder *LaunchCommandBuilder

	// CDDelay is the delay after cd command before launching agent.
	// Default: 100ms
	CDDelay time.Duration

	// ValidatePaths determines whether to check project paths exist.
	// Default: true
	ValidatePaths bool

	// Logger for structured logging.
	Logger *slog.Logger

	// RateLimitTracker enables adaptive throttling for Codex.
	RateLimitTracker *ratelimit.RateLimitTracker

	targetingMu    sync.Mutex
	targetingCache map[string]swarmSessionTargeting
}

// NewPaneLauncher creates a new PaneLauncher with default settings.
func NewPaneLauncher() *PaneLauncher {
	return &PaneLauncher{
		TmuxClient:       nil,
		CmdBuilder:       nil,
		CDDelay:          100 * time.Millisecond,
		ValidatePaths:    true,
		Logger:           slog.Default(),
		RateLimitTracker: nil,
		targetingCache:   make(map[string]swarmSessionTargeting),
	}
}

// NewPaneLauncherWithClient creates a PaneLauncher with a custom tmux client.
func NewPaneLauncherWithClient(client *tmux.Client) *PaneLauncher {
	return &PaneLauncher{
		TmuxClient:       client,
		CmdBuilder:       nil,
		CDDelay:          100 * time.Millisecond,
		ValidatePaths:    true,
		Logger:           slog.Default(),
		RateLimitTracker: nil,
		targetingCache:   make(map[string]swarmSessionTargeting),
	}
}

// WithCmdBuilder sets a custom launch command builder.
func (pl *PaneLauncher) WithCmdBuilder(builder *LaunchCommandBuilder) *PaneLauncher {
	pl.CmdBuilder = builder
	return pl
}

// WithCDDelay sets the delay after cd command.
func (pl *PaneLauncher) WithCDDelay(delay time.Duration) *PaneLauncher {
	pl.CDDelay = delay
	return pl
}

// WithValidatePaths sets whether to validate project paths exist.
func (pl *PaneLauncher) WithValidatePaths(validate bool) *PaneLauncher {
	pl.ValidatePaths = validate
	return pl
}

// WithLogger sets a custom logger.
func (pl *PaneLauncher) WithLogger(logger *slog.Logger) *PaneLauncher {
	pl.Logger = logger
	return pl
}

// WithRateLimitTracker enables adaptive throttling based on rate limit history.
func (pl *PaneLauncher) WithRateLimitTracker(tracker *ratelimit.RateLimitTracker) *PaneLauncher {
	pl.RateLimitTracker = tracker
	return pl
}

// tmuxClient returns the configured tmux client or the default client.
func (pl *PaneLauncher) tmuxClient() *tmux.Client {
	if pl.TmuxClient != nil {
		return pl.TmuxClient
	}
	return tmux.DefaultClient
}

// cmdBuilder returns the configured command builder or creates a default one.
func (pl *PaneLauncher) cmdBuilder() *LaunchCommandBuilder {
	if pl.CmdBuilder != nil {
		return pl.CmdBuilder
	}
	return NewLaunchCommandBuilder()
}

// logger returns the configured logger or the default logger.
func (pl *PaneLauncher) logger() *slog.Logger {
	if pl.Logger != nil {
		return pl.Logger
	}
	return slog.Default()
}

func (pl *PaneLauncher) resolveSessionTargeting(ctx context.Context, sessionName string) (swarmSessionTargeting, bool) {
	if sessionName == "" {
		return swarmSessionTargeting{}, false
	}

	pl.targetingMu.Lock()
	if pl.targetingCache != nil {
		if cached, ok := pl.targetingCache[sessionName]; ok {
			pl.targetingMu.Unlock()
			return cached, true
		}
	}
	pl.targetingMu.Unlock()

	targeting, err := resolveSwarmSessionTargeting(ctx, pl.tmuxClient(), sessionName)
	if err != nil {
		pl.logger().Warn("[PaneLauncher] session_targeting_resolve_failed",
			"session", sessionName,
			"error", err)
		return swarmSessionTargeting{}, false
	}

	pl.targetingMu.Lock()
	if pl.targetingCache == nil {
		pl.targetingCache = make(map[string]swarmSessionTargeting)
	}
	pl.targetingCache[sessionName] = targeting
	pl.targetingMu.Unlock()

	return targeting, true
}

// PaneLaunchResult represents the result of launching an agent in a pane.
type PaneLaunchResult struct {
	SessionName string        `json:"session_name"`
	PaneIndex   int           `json:"pane_index"`
	PaneTarget  string        `json:"pane_target"`
	AgentType   string        `json:"agent_type"`
	Project     string        `json:"project"`
	Command     string        `json:"command"`
	Success     bool          `json:"success"`
	Error       string        `json:"error,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// LaunchAgentInPane sets up and launches an agent in a specific pane.
// It changes to the project directory before launching the agent.
func (pl *PaneLauncher) LaunchAgentInPane(ctx context.Context, sessionName string, paneSpec PaneSpec) (*PaneLaunchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	start := time.Now()
	result := &PaneLaunchResult{
		SessionName: sessionName,
		PaneIndex:   paneSpec.Index,
		AgentType:   paneSpec.AgentType,
		Project:     paneSpec.Project,
	}

	// Default target format (fallback if session targeting can't be resolved)
	paneTarget := formatPaneTarget(sessionName, paneSpec.Index)
	result.PaneTarget = paneTarget

	// Check for context cancellation
	select {
	case <-ctx.Done():
		result.Success = false
		result.Error = ctx.Err().Error()
		result.Duration = time.Since(start)
		return result, ctx.Err()
	default:
	}

	// Validate project path exists
	if pl.ValidatePaths && paneSpec.Project != "" {
		if _, err := os.Stat(paneSpec.Project); err != nil {
			pl.logger().Error("[PaneLauncher] project_path_invalid",
				"project", paneSpec.Project,
				"error", err)
			result.Success = false
			result.Error = fmt.Sprintf("project path %s: %v", paneSpec.Project, err)
			result.Duration = time.Since(start)
			return result, fmt.Errorf("project path %s: %w", paneSpec.Project, err)
		}
	}

	client := pl.tmuxClient()

	if targeting, ok := pl.resolveSessionTargeting(ctx, sessionName); ok {
		if resolved, err := swarmPaneTargetFromPlanIndex(sessionName, targeting, paneSpec.Index); err == nil {
			paneTarget = resolved
			result.PaneTarget = resolved
		} else {
			pl.logger().Warn("[PaneLauncher] pane_target_resolve_failed",
				"session", sessionName,
				"pane_index", paneSpec.Index,
				"error", err)
		}
	}

	pl.logger().Info("[PaneLauncher] launch_start",
		"session", sessionName,
		"pane_index", paneSpec.Index,
		"pane_target", paneTarget,
		"project", paneSpec.Project,
		"agent_type", paneSpec.AgentType)

	// Step 1: Change to project directory (if specified)
	if paneSpec.Project != "" {
		// Quote path to handle spaces
		cdCmd := fmt.Sprintf("cd %q", paneSpec.Project)
		if err := client.SendKeys(paneTarget, cdCmd, true); err != nil {
			pl.logger().Error("[PaneLauncher] cd_failed",
				"pane_target", paneTarget,
				"project", paneSpec.Project,
				"error", err)
			result.Success = false
			result.Error = fmt.Sprintf("cd to project: %v", err)
			result.Duration = time.Since(start)
			return result, fmt.Errorf("cd to project: %w", err)
		}

		pl.logger().Debug("[PaneLauncher] cd_success",
			"pane_target", paneTarget,
			"project", paneSpec.Project)

		// Brief pause to ensure cd completes
		if pl.CDDelay > 0 {
			time.Sleep(pl.CDDelay)
		}
	}

	// Check for context cancellation again
	select {
	case <-ctx.Done():
		result.Success = false
		result.Error = ctx.Err().Error()
		result.Duration = time.Since(start)
		return result, ctx.Err()
	default:
	}

	// Step 2: Build and send launch command
	launchCmd := pl.cmdBuilder().BuildLaunchCommand(paneSpec, paneSpec.Project)
	shellCmd := launchCmd.ToShellCommand()
	result.Command = shellCmd

	if err := client.SendKeys(paneTarget, shellCmd, true); err != nil {
		pl.logger().Error("[PaneLauncher] launch_failed",
			"pane_target", paneTarget,
			"command", shellCmd,
			"error", err)
		result.Success = false
		result.Error = fmt.Sprintf("launch agent: %v", err)
		result.Duration = time.Since(start)
		return result, fmt.Errorf("launch agent: %w", err)
	}

	if pl.RateLimitTracker != nil && isCodexProvider(paneSpec.AgentType) {
		pl.RateLimitTracker.RecordSuccess("openai")
		saveDir := paneSpec.Project
		if err := pl.RateLimitTracker.SaveToDir(saveDir); err != nil {
			pl.logger().Warn("[PaneLauncher] tracker_persist_failed",
				"provider", "openai",
				"error", err)
		}
	}

	result.Success = true
	result.Duration = time.Since(start)

	pl.logger().Info("[PaneLauncher] launch_success",
		"session", sessionName,
		"pane_target", paneTarget,
		"agent_type", paneSpec.AgentType,
		"project", filepath.Base(paneSpec.Project),
		"command", shellCmd,
		"duration", result.Duration)

	return result, nil
}

// BatchLaunchResult contains the results of launching multiple agents.
type BatchLaunchResult struct {
	TotalPanes int                `json:"total_panes"`
	Successful int                `json:"successful"`
	Failed     int                `json:"failed"`
	Results    []PaneLaunchResult `json:"results"`
	Duration   time.Duration      `json:"duration"`
	Errors     []error            `json:"-"`
}

// LaunchSession launches agents in all panes of a session spec.
// It handles staggered launching to avoid rate limits.
func (pl *PaneLauncher) LaunchSession(ctx context.Context, sessionSpec SessionSpec, staggerDelay time.Duration) (*BatchLaunchResult, error) {
	start := time.Now()
	result := &BatchLaunchResult{
		TotalPanes: len(sessionSpec.Panes),
		Results:    make([]PaneLaunchResult, 0, len(sessionSpec.Panes)),
	}
	openAICooldownWaited := false

	pl.logger().Info("[PaneLauncher] session_launch_start",
		"session", sessionSpec.Name,
		"pane_count", len(sessionSpec.Panes))

	for i, paneSpec := range sessionSpec.Panes {
		// Stagger launches (skip delay for first pane)
		if i > 0 && staggerDelay > 0 {
			select {
			case <-ctx.Done():
				result.Duration = time.Since(start)
				return result, ctx.Err()
			case <-time.After(staggerDelay):
			}
		}

		if pl.RateLimitTracker != nil && isCodexProvider(paneSpec.AgentType) && !openAICooldownWaited {
			cooldown := pl.RateLimitTracker.CooldownRemaining("openai")
			openAICooldownWaited = true
			if cooldown > 0 {
				pl.logger().Info("[PaneLauncher] codex_cooldown_wait",
					"session", sessionSpec.Name,
					"cooldown", ratelimit.FormatDelay(cooldown))
				select {
				case <-ctx.Done():
					result.Duration = time.Since(start)
					return result, ctx.Err()
				case <-time.After(cooldown):
				}
			}
		}

		launchResult, err := pl.LaunchAgentInPane(ctx, sessionSpec.Name, paneSpec)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, err)
		} else {
			result.Successful++
		}
		result.Results = append(result.Results, *launchResult)
	}

	result.Duration = time.Since(start)

	pl.logger().Info("[PaneLauncher] session_launch_complete",
		"session", sessionSpec.Name,
		"successful", result.Successful,
		"failed", result.Failed,
		"duration", result.Duration)

	return result, nil
}

// LaunchSwarm launches agents in all sessions of a swarm plan.
func (pl *PaneLauncher) LaunchSwarm(ctx context.Context, plan *SwarmPlan, staggerDelay time.Duration) (*BatchLaunchResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	start := time.Now()
	result := &BatchLaunchResult{
		TotalPanes: plan.TotalAgents,
		Results:    make([]PaneLaunchResult, 0, plan.TotalAgents),
	}

	pl.logger().Info("[PaneLauncher] swarm_launch_start",
		"total_sessions", len(plan.Sessions),
		"total_agents", plan.TotalAgents)

	for _, sessionSpec := range plan.Sessions {
		sessionResult, err := pl.LaunchSession(ctx, sessionSpec, staggerDelay)
		if err != nil {
			// Context cancelled - stop launching
			if ctx.Err() != nil {
				result.Successful += sessionResult.Successful
				result.Failed += sessionResult.Failed
				result.Results = append(result.Results, sessionResult.Results...)
				result.Errors = append(result.Errors, sessionResult.Errors...)
				result.Duration = time.Since(start)
				return result, ctx.Err()
			}
		}

		result.Successful += sessionResult.Successful
		result.Failed += sessionResult.Failed
		result.Results = append(result.Results, sessionResult.Results...)
		result.Errors = append(result.Errors, sessionResult.Errors...)
	}

	result.Duration = time.Since(start)

	pl.logger().Info("[PaneLauncher] swarm_launch_complete",
		"successful", result.Successful,
		"failed", result.Failed,
		"duration", result.Duration)

	return result, nil
}

// GetPaneTarget formats the tmux target string for a pane.
// Uses the format "session:window.pane" where window is typically 1.
func GetPaneTarget(sessionName string, paneIndex int) string {
	return formatPaneTarget(sessionName, paneIndex)
}

// ValidateProjectPath checks if a project path exists and is a directory.
func ValidateProjectPath(path string) error {
	if path == "" {
		return nil // Empty path is allowed
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("path does not exist: %s", path)
		}
		return fmt.Errorf("cannot access path %s: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}

	return nil
}

func isCodexProvider(agentType string) bool {
	switch agentType {
	case "cod", "codex", "openai", "gpt":
		return true
	default:
		return false
	}
}
