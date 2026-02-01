package swarm

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	ntmcontext "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AccountRotatorI provides account rotation for limit recovery (optional).
// If not provided, agents will be respawned with the same account.
// Note: The concrete AccountRotator struct in account_rotator.go implements this interface.
type AccountRotatorI interface {
	// RotateAccount switches to the next available account for the agent type.
	// Returns the new account identifier, or error if no accounts available.
	RotateAccount(agentType string) (newAccount string, err error)

	// CurrentAccount returns the current account for the agent type.
	CurrentAccount(agentType string) string

	// OnLimitHit handles a limit detection event and performs rotation with cooldown.
	// Returns a rotation record or an error if rotation was skipped or failed.
	OnLimitHit(event LimitHitEvent) (*RotationRecord, error)
}

// ProjectPathLookup is a callback to resolve project path from session:pane.
// Returns the project directory path, or empty string if not found.
type ProjectPathLookup func(sessionPane string) string

// RespawnResult tracks the result of a single respawn attempt.
type RespawnResult struct {
	Success         bool          `json:"success"`
	SessionPane     string        `json:"session_pane"`
	AgentType       string        `json:"agent_type"`
	AccountRotated  bool          `json:"account_rotated"`
	PreviousAccount string        `json:"previous_account,omitempty"`
	NewAccount      string        `json:"new_account,omitempty"`
	Duration        time.Duration `json:"duration"`
	Error           string        `json:"error,omitempty"`
	RespawnedAt     time.Time     `json:"respawned_at"`
}

// AutoRespawnerConfig holds configuration for the AutoRespawner.
type AutoRespawnerConfig struct {
	// GracefulExitDelay is how long to wait for the agent to exit gracefully.
	// Default: 2 seconds
	GracefulExitDelay time.Duration

	// AgentReadyDelay is how long to wait for a new agent to be ready.
	// Default: 5 seconds
	AgentReadyDelay time.Duration

	// MaxRetriesPerPane limits respawns per pane to prevent infinite loops.
	// Default: 3
	MaxRetriesPerPane int

	// RetryResetDuration resets the retry counter after this duration.
	// Default: 1 hour
	RetryResetDuration time.Duration

	// ClearPaneDelay is how long to wait after sending clear command.
	// Default: 100ms
	ClearPaneDelay time.Duration

	// ExitWaitTimeout is the maximum time to wait for agent exit verification.
	// Default: 5 seconds
	ExitWaitTimeout time.Duration

	// ExitPollInterval is the interval between exit verification checks.
	// Default: 500ms
	ExitPollInterval time.Duration

	// AutoRotateAccounts enables automatic account rotation on limit hit.
	// Default: false
	AutoRotateAccounts bool

	// MarchingOrders contains agent-specific prompt templates for post-respawn injection.
	// Keys are agent types (cc, cod, gmi) or "default" for fallback.
	// If nil or agent type not found, falls back to PromptInjector.GetTemplate("default").
	MarchingOrders map[string]string
}

// DefaultAutoRespawnerConfig returns sensible defaults.
func DefaultAutoRespawnerConfig() AutoRespawnerConfig {
	return AutoRespawnerConfig{
		GracefulExitDelay:  2 * time.Second,
		AgentReadyDelay:    5 * time.Second,
		MaxRetriesPerPane:  3,
		RetryResetDuration: 1 * time.Hour,
		ClearPaneDelay:     100 * time.Millisecond,
		ExitWaitTimeout:    5 * time.Second,
		ExitPollInterval:   500 * time.Millisecond,
		AutoRotateAccounts: false,
	}
}

// paneRetryState tracks retry attempts for a pane.
type paneRetryState struct {
	Count     int
	LastReset time.Time
}

// AutoRespawner handles automatic agent recovery when usage limits are hit.
type AutoRespawner struct {
	// Config holds respawn settings.
	Config AutoRespawnerConfig

	// LimitDetector provides limit events.
	LimitDetector *LimitDetector

	// AccountRotator switches accounts (optional).
	AccountRotator AccountRotatorI

	// PromptInjector re-sends marching orders.
	PromptInjector *PromptInjector

	// PaneSpawner from context package (REUSES EXISTING interface).
	PaneSpawner ntmcontext.PaneSpawner

	// TmuxClient for direct tmux operations.
	// If nil, the default tmux client is used.
	TmuxClient autoRespawnerTmux

	// ProjectPathLookup resolves project path from session:pane (optional).
	// If provided, respawn will cd to the project directory before launching.
	ProjectPathLookup ProjectPathLookup

	// Logger for structured logging.
	Logger *slog.Logger

	// eventChan emits respawn events.
	eventChan chan RespawnEvent

	// mu protects internal state.
	mu sync.RWMutex

	// retryState tracks retries per pane.
	retryState map[string]*paneRetryState

	// ctx is the context for all respawn goroutines.
	ctx context.Context

	// cancel stops all respawn goroutines.
	cancel context.CancelFunc

	// forceKillFn overrides forceKill behavior in tests (optional).
	forceKillFn func(sessionPane string) error

	// CommandBuilder generates launch commands (optional).
	// If provided, it is used to resolve the full command string for agents.
	CommandBuilder *LaunchCommandBuilder
}

type autoRespawnerTmux interface {
	SendKeys(target, keys string, enter bool) error
	CapturePaneOutput(target string, lines int) (string, error)
	Run(args ...string) (string, error)
}

// NewAutoRespawner creates a new AutoRespawner with default settings.
func NewAutoRespawner() *AutoRespawner {
	return &AutoRespawner{
		Config:         DefaultAutoRespawnerConfig(),
		LimitDetector:  nil,
		AccountRotator: nil,
		PromptInjector: nil,
		PaneSpawner:    nil,
		TmuxClient:     nil,
		Logger:         slog.Default(),
		eventChan:      make(chan RespawnEvent, 100),
		retryState:     make(map[string]*paneRetryState),
	}
}

// WithLimitDetector sets the limit detector.
func (r *AutoRespawner) WithLimitDetector(ld *LimitDetector) *AutoRespawner {
	r.LimitDetector = ld
	return r
}

// WithAccountRotator sets the account rotator (optional).
func (r *AutoRespawner) WithAccountRotator(ar AccountRotatorI) *AutoRespawner {
	r.AccountRotator = ar
	return r
}

// WithPromptInjector sets the prompt injector.
func (r *AutoRespawner) WithPromptInjector(pi *PromptInjector) *AutoRespawner {
	r.PromptInjector = pi
	return r
}

// WithPaneSpawner sets the pane spawner.
func (r *AutoRespawner) WithPaneSpawner(ps ntmcontext.PaneSpawner) *AutoRespawner {
	r.PaneSpawner = ps
	return r
}

// WithTmuxClient sets the tmux client.
func (r *AutoRespawner) WithTmuxClient(client autoRespawnerTmux) *AutoRespawner {
	r.TmuxClient = client
	return r
}

// WithCommandBuilder sets the command builder.
func (r *AutoRespawner) WithCommandBuilder(builder *LaunchCommandBuilder) *AutoRespawner {
	r.CommandBuilder = builder
	return r
}

// WithProjectPathLookup sets the project path lookup callback.
func (r *AutoRespawner) WithProjectPathLookup(lookup ProjectPathLookup) *AutoRespawner {
	r.ProjectPathLookup = lookup
	return r
}

// WithLogger sets a custom logger.
func (r *AutoRespawner) WithLogger(logger *slog.Logger) *AutoRespawner {
	r.Logger = logger
	return r
}

// WithConfig sets the configuration.
func (r *AutoRespawner) WithConfig(cfg AutoRespawnerConfig) *AutoRespawner {
	r.Config = cfg
	return r
}

// WithMarchingOrders sets agent-specific marching orders templates.
// Keys should be agent types (cc, cod, gmi) or "default" for fallback.
func (r *AutoRespawner) WithMarchingOrders(orders map[string]string) *AutoRespawner {
	r.Config.MarchingOrders = orders
	return r
}

// tmuxClient returns the configured tmux client or the default client.
func (r *AutoRespawner) tmuxClient() autoRespawnerTmux {
	if r.TmuxClient != nil {
		return r.TmuxClient
	}
	return tmux.DefaultClient
}

// logger returns the configured logger or the default logger.
func (r *AutoRespawner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Events returns the channel that emits respawn events.
func (r *AutoRespawner) Events() <-chan RespawnEvent {
	return r.eventChan
}

// Start begins listening for limit events and auto-respawning agents.
func (r *AutoRespawner) Start(ctx context.Context) (err error) {
	if r.LimitDetector == nil {
		return fmt.Errorf("LimitDetector is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Create a child context for the lifetime of the respawner.
	// If Start returns an error after creating the context, ensure we cancel it
	// to avoid leaking goroutines/resources.
	childCtx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
		}
	}()

	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return fmt.Errorf("AutoRespawner already started")
	}
	r.ctx, r.cancel = childCtx, cancel
	r.mu.Unlock()

	r.logger().Info("[AutoRespawner] starting",
		"auto_rotate", r.Config.AutoRotateAccounts,
		"max_retries", r.Config.MaxRetriesPerPane)

	// Start the event processing goroutine
	go r.processLimitEvents()

	return nil
}

// Stop halts the auto-respawner.
func (r *AutoRespawner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}

	r.logger().Info("[AutoRespawner] stopped")
}

// processLimitEvents listens for limit events and triggers respawns.
func (r *AutoRespawner) processLimitEvents() {
	events := r.LimitDetector.Events()

	for {
		select {
		case <-r.ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			r.logger().Info("[AutoRespawner] limit_event_received",
				"session_pane", event.SessionPane,
				"agent_type", event.AgentType,
				"pattern", event.Pattern)

			// Check retry limits
			if r.isRetryLimitExceeded(event.SessionPane) {
				r.logger().Warn("[AutoRespawner] retry_limit_exceeded",
					"session_pane", event.SessionPane,
					"max_retries", r.Config.MaxRetriesPerPane)
				continue
			}

			// Attempt respawn
			result := r.Respawn(event)
			if result.Success {
				r.recordRetryAttempt(event.SessionPane)
			}
		}
	}
}

// isRetryLimitExceeded checks if a pane has exceeded its retry limit.
func (r *AutoRespawner) isRetryLimitExceeded(sessionPane string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	state, exists := r.retryState[sessionPane]
	if !exists {
		return false
	}

	// Reset counter if enough time has passed
	if time.Since(state.LastReset) > r.Config.RetryResetDuration {
		return false
	}

	return state.Count >= r.Config.MaxRetriesPerPane
}

// recordRetryAttempt increments the retry counter for a pane.
func (r *AutoRespawner) recordRetryAttempt(sessionPane string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, exists := r.retryState[sessionPane]
	if !exists || time.Since(state.LastReset) > r.Config.RetryResetDuration {
		r.retryState[sessionPane] = &paneRetryState{
			Count:     1,
			LastReset: time.Now(),
		}
		return
	}

	state.Count++
}

// Respawn performs the full respawn sequence for an agent after a limit hit.
func (r *AutoRespawner) Respawn(event LimitEvent) *RespawnResult {
	sessionPane := event.SessionPane
	agentType := event.AgentType
	start := time.Now()
	result := &RespawnResult{
		SessionPane: sessionPane,
		AgentType:   agentType,
		RespawnedAt: start,
	}

	r.logger().Info("[AutoRespawner] respawn_start",
		"session_pane", sessionPane,
		"agent_type", agentType)

	// Step 1: Kill the stuck agent
	if err := r.killWithFallback(sessionPane, agentType); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("kill agent failed: %v", err)
		result.Duration = time.Since(start)

		r.logger().Error("[AutoRespawner] respawn_failed",
			"session_pane", sessionPane,
			"stage", "kill",
			"error", err,
			"duration", result.Duration)

		return result
	}

	// Step 2: (Optional) Rotate account
	if r.Config.AutoRotateAccounts && r.AccountRotator != nil {
		hit := LimitHitEvent{
			SessionPane: sessionPane,
			AgentType:   agentType,
			Pattern:     event.Pattern,
			DetectedAt:  event.DetectedAt,
			Project:     r.projectForPane(sessionPane),
		}
		record, err := r.AccountRotator.OnLimitHit(hit)
		if err != nil {
			r.logger().Warn("[AutoRespawner] account_rotation_failed",
				"session_pane", sessionPane,
				"agent_type", agentType,
				"error", err)
			// Continue without rotation - not fatal
		} else if record != nil {
			result.AccountRotated = true
			result.PreviousAccount = record.FromAccount
			result.NewAccount = record.ToAccount

			r.logger().Info("[AutoRespawner] account_rotated",
				"session_pane", sessionPane,
				"old", record.FromAccount,
				"new", record.ToAccount)
		}
	}

	// Step 3: Clear the pane
	if err := r.clearPane(sessionPane); err != nil {
		r.logger().Warn("[AutoRespawner] clear_pane_failed",
			"session_pane", sessionPane,
			"error", err)
		// Continue anyway - not fatal
	}

	// Step 4: Change to project directory (if configured)
	if err := r.cdToProject(sessionPane); err != nil {
		r.logger().Warn("[AutoRespawner] cd_project_failed",
			"session_pane", sessionPane,
			"error", err)
		// Continue anyway - agent may work from wrong directory
	}

	// Step 5: Respawn the agent
	if err := r.spawnAgent(sessionPane, agentType); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("spawn agent failed: %v", err)
		result.Duration = time.Since(start)

		r.logger().Error("[AutoRespawner] respawn_failed",
			"session_pane", sessionPane,
			"stage", "spawn",
			"error", err,
			"duration", result.Duration)

		return result
	}

	// Step 6: Wait for agent to be ready
	if err := r.waitForAgentReady(sessionPane, agentType); err != nil {
		r.logger().Warn("[AutoRespawner] ready_timeout",
			"session_pane", sessionPane,
			"agent_type", agentType,
			"error", err)
		// Continue anyway - agent might still be starting
	}

	// Step 7: Re-inject marching orders
	if r.PromptInjector != nil {
		prompt, source := r.getMarchingOrders(agentType)
		r.logger().Info("[AutoRespawner] marching_orders_selected",
			"session_pane", sessionPane,
			"agent_type", agentType,
			"source", source,
			"prompt_len", len(prompt))

		if err := r.PromptInjector.InjectPrompt(sessionPane, agentType, prompt); err != nil {
			r.logger().Warn("[AutoRespawner] marching_orders_failed",
				"session_pane", sessionPane,
				"error", err)
			// Continue - agent is running, just without marching orders
		} else {
			r.logger().Info("[AutoRespawner] marching_orders_injected",
				"session_pane", sessionPane,
				"prompt_len", len(prompt))
		}
	}

	// Step 8: Success!
	result.Success = true
	result.Duration = time.Since(start)

	r.logger().Info("[AutoRespawner] respawn_complete",
		"session_pane", sessionPane,
		"success", true,
		"duration", result.Duration,
		"account_rotated", result.AccountRotated)

	// Step 9: Emit respawn event
	r.emitEvent(result)

	return result
}

// killAgent sends the appropriate kill sequence for the agent type.
func (r *AutoRespawner) killAgent(sessionPane, agentType string) error {
	r.logger().Info("[AutoRespawner] killing_agent",
		"session_pane", sessionPane,
		"method", agentType)

	client := r.tmuxClient()

	switch agentType {
	case "cc", "claude", "claude-code":
		// Claude: Double Ctrl+C with 100ms gap (CRITICAL timing)
		if err := client.SendKeys(sessionPane, "\x03", false); err != nil {
			return fmt.Errorf("send first ctrl-c: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
		if err := client.SendKeys(sessionPane, "\x03", false); err != nil {
			return fmt.Errorf("send second ctrl-c: %w", err)
		}

	case "cod", "codex":
		// Codex: /exit command
		if err := client.SendKeys(sessionPane, "/exit", true); err != nil {
			return fmt.Errorf("send /exit: %w", err)
		}

	case "gmi", "gemini":
		// Gemini: Escape then Ctrl+C
		if err := client.SendKeys(sessionPane, "\x1b", false); err != nil {
			return fmt.Errorf("send escape: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
		if err := client.SendKeys(sessionPane, "\x03", false); err != nil {
			return fmt.Errorf("send ctrl-c: %w", err)
		}

	default:
		// Default: Double Ctrl+C (safe fallback)
		if err := client.SendKeys(sessionPane, "\x03", false); err != nil {
			return fmt.Errorf("send first ctrl-c: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
		if err := client.SendKeys(sessionPane, "\x03", false); err != nil {
			return fmt.Errorf("send second ctrl-c: %w", err)
		}
	}

	return nil
}

// killWithFallback tries graceful kill first, then force kill if needed.
func (r *AutoRespawner) killWithFallback(sessionPane, agentType string) error {
	if err := r.killAgent(sessionPane, agentType); err != nil {
		r.logger().Warn("[AutoRespawner] graceful_kill_failed",
			"session_pane", sessionPane,
			"error", err)
	}

	if r.waitForExit(sessionPane) {
		return nil
	}

	if err := r.forceKill(sessionPane); err != nil {
		return err
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// forceKill sends SIGKILL to the process in the pane.
func (r *AutoRespawner) forceKill(sessionPane string) error {
	if r.forceKillFn != nil {
		return r.forceKillFn(sessionPane)
	}

	r.logger().Warn("[AutoRespawner] force_kill_start",
		"session_pane", sessionPane)

	pid, err := r.getPanePID(sessionPane)
	if err != nil {
		r.logger().Warn("[AutoRespawner] force_kill_failed",
			"session_pane", sessionPane,
			"error", err)
		return fmt.Errorf("get pane pid: %w", err)
	}

	// Kill the process group first for thorough cleanup.
	if err := exec.Command("kill", "-9", fmt.Sprintf("-%d", pid)).Run(); err != nil {
		// Fall back to killing just the process.
		if err := exec.Command("kill", "-9", strconv.Itoa(pid)).Run(); err != nil {
			r.logger().Warn("[AutoRespawner] force_kill_failed",
				"session_pane", sessionPane,
				"error", err)
			return fmt.Errorf("kill -9 failed: %w", err)
		}
	}

	r.logger().Info("[AutoRespawner] force_kill_complete",
		"session_pane", sessionPane,
		"pid", pid)
	return nil
}

// getPanePID gets the foreground process PID from tmux.
func (r *AutoRespawner) getPanePID(sessionPane string) (int, error) {
	client := r.tmuxClient()

	output, err := client.Run("display-message", "-p", "-t", sessionPane, "#{pane_pid}")
	if err != nil {
		return 0, fmt.Errorf("display-message: %w", err)
	}

	pidStr := strings.TrimSpace(output)
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("parse pid %q: %w", pidStr, err)
	}

	r.logger().Info("[AutoRespawner] pid_lookup",
		"session_pane", sessionPane,
		"pid", pid)
	return pid, nil
}

// waitForExit waits for agent to terminate, returns true if exited.
// It checks the pane output for shell prompt indicators.
func (r *AutoRespawner) waitForExit(sessionPane string) bool {
	timeout := r.Config.ExitWaitTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	pollInterval := r.Config.ExitPollInterval
	if pollInterval == 0 {
		pollInterval = 500 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		<-ticker.C

		// Check if pane is back to shell prompt
		output := r.capturePaneOutput(sessionPane, 5)
		if output == "" {
			continue
		}

		if r.isShellPrompt(output) {
			r.logger().Info("[AutoRespawner] agent_exited",
				"session_pane", sessionPane)
			return true
		}
	}

	r.logger().Warn("[AutoRespawner] exit_timeout",
		"session_pane", sessionPane,
		"timeout", timeout)
	return false
}

// isShellPrompt checks if the output indicates a shell prompt.
func (r *AutoRespawner) isShellPrompt(output string) bool {
	if len(output) == 0 {
		return false
	}

	// Common shell prompt indicators
	prompts := []string{"$", "%", ">", "#", "➜", "❯"}

	// Check if the last non-whitespace line ends with a prompt
	lines := splitLines(output)
	for i := len(lines) - 1; i >= 0; i-- {
		line := trimWhitespace(lines[i])
		if line == "" {
			continue
		}
		// Check if line ends with a prompt character
		for _, prompt := range prompts {
			if len(line) > 0 && line[len(line)-1] == prompt[0] {
				return true
			}
		}
		// Also check for common prompt patterns anywhere in line
		if containsAny(line, prompts) {
			return true
		}
		// Only check the last non-empty line
		break
	}
	return false
}

// splitLines splits output into lines without importing strings.
func splitLines(s string) []string {
	var lines []string
	var current []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, string(current))
			current = current[:0]
		} else if s[i] != '\r' {
			current = append(current, s[i])
		}
	}
	if len(current) > 0 {
		lines = append(lines, string(current))
	}
	return lines
}

// trimWhitespace trims leading/trailing whitespace without importing strings.
func trimWhitespace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

// capturePaneOutput captures the last N lines from a pane.
func (r *AutoRespawner) capturePaneOutput(sessionPane string, lines int) string {
	client := r.tmuxClient()

	output, err := client.CapturePaneOutput(sessionPane, lines)
	if err != nil {
		r.logger().Debug("[AutoRespawner] capture_failed",
			"session_pane", sessionPane,
			"error", err)
		return ""
	}

	return output
}

// clearPane sends the clear command to reset the terminal.
func (r *AutoRespawner) clearPane(sessionPane string) error {
	client := r.tmuxClient()

	if err := client.SendKeys(sessionPane, "clear", true); err != nil {
		return fmt.Errorf("send clear: %w", err)
	}

	time.Sleep(r.Config.ClearPaneDelay)
	return nil
}

// projectForPane returns the project path for a pane, if available.
func (r *AutoRespawner) projectForPane(sessionPane string) string {
	if r.ProjectPathLookup == nil {
		return ""
	}
	return r.ProjectPathLookup(sessionPane)
}

// cdToProject changes to the project directory if a path is available.
func (r *AutoRespawner) cdToProject(sessionPane string) error {
	if r.ProjectPathLookup == nil {
		return nil // No lookup configured, skip
	}

	projectPath := r.ProjectPathLookup(sessionPane)
	if projectPath == "" {
		r.logger().Debug("[AutoRespawner] no_project_path",
			"session_pane", sessionPane)
		return nil // No project path found, skip
	}

	client := r.tmuxClient()

	// Use shell escaping for paths with spaces
	cdCmd := fmt.Sprintf("cd %q", projectPath)
	if err := client.SendKeys(sessionPane, cdCmd, true); err != nil {
		return fmt.Errorf("cd to project: %w", err)
	}

	r.logger().Info("[AutoRespawner] directory_changed",
		"session_pane", sessionPane,
		"path", projectPath)

	time.Sleep(r.Config.ClearPaneDelay) // Wait for cd to complete
	return nil
}

// spawnAgent launches the agent command in the pane.
func (r *AutoRespawner) spawnAgent(sessionPane, agentType string) error {
	r.logger().Info("[AutoRespawner] spawning_agent",
		"session_pane", sessionPane,
		"agent_type", agentType)

	client := r.tmuxClient()

	// Determine the command to run
	var cmd string
	if r.CommandBuilder != nil {
		// Use builder to construct full command (respects config/env)
		// We reconstruct a partial PaneSpec since we don't have the full original spec
		project := r.projectForPane(sessionPane)
		spec := PaneSpec{
			AgentType: agentType,
			Project:   project,
		}
		launchCmd := r.CommandBuilder.BuildLaunchCommand(spec, project)
		cmd = launchCmd.ToShellCommand()
	} else {
		// Fallback to simple alias lookup
		cmd = r.getAgentCommand(agentType)
	}

	if err := client.SendKeys(sessionPane, cmd, true); err != nil {
		return fmt.Errorf("send agent command: %w", err)
	}

	return nil
}

// getAgentCommand returns the command to launch an agent.
func (r *AutoRespawner) getAgentCommand(agentType string) string {
	switch agentType {
	case "cc", "claude", "claude-code":
		return "cc"
	case "cod", "codex":
		return "cod"
	case "gmi", "gemini":
		return "gmi"
	default:
		return agentType
	}
}

// getMarchingOrders returns the appropriate marching orders for the agent type.
// Returns the prompt text and a source indicator ("config/<type>", "config/default", or "injector").
func (r *AutoRespawner) getMarchingOrders(agentType string) (string, string) {
	// Normalize agent type for lookup
	normalizedType := r.normalizeAgentType(agentType)

	// Check config for agent-specific template
	if r.Config.MarchingOrders != nil {
		if tmpl, ok := r.Config.MarchingOrders[normalizedType]; ok && tmpl != "" {
			return tmpl, "config/" + normalizedType
		}
		if tmpl, ok := r.Config.MarchingOrders["default"]; ok && tmpl != "" {
			return tmpl, "config/default"
		}
	}

	// Fallback to PromptInjector's default template
	if r.PromptInjector != nil {
		return r.PromptInjector.GetTemplate("default"), "injector"
	}

	// Ultimate fallback if no injector configured
	return DefaultMarchingOrders, "builtin"
}

// normalizeAgentType converts agent type aliases to canonical forms.
func (r *AutoRespawner) normalizeAgentType(agentType string) string {
	switch agentType {
	case "cc", "claude", "claude-code":
		return "cc"
	case "cod", "codex":
		return "cod"
	case "gmi", "gemini":
		return "gmi"
	default:
		return agentType
	}
}

// agentReadyPatterns returns the patterns that indicate an agent is ready.
func agentReadyPatterns(agentType string) []string {
	switch agentType {
	case "cc", "claude", "claude-code":
		return []string{"Claude", "Opus", "Sonnet", "Haiku", ">"}
	case "cod", "codex":
		return []string{"Codex", "codex>", "?"}
	case "gmi", "gemini":
		return []string{"Gemini", ">"}
	default:
		return []string{">", "$", "%"} // Generic shell/agent prompts
	}
}

// waitForAgentReady waits for agent startup indicators in pane output.
func (r *AutoRespawner) waitForAgentReady(sessionPane, agentType string) error {
	timeout := r.Config.AgentReadyDelay
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	patterns := agentReadyPatterns(agentType)

	checkReady := func() bool {
		output := r.capturePaneOutput(sessionPane, 20)
		if output == "" {
			return false
		}

		for _, pattern := range patterns {
			if strings.Contains(output, pattern) {
				r.logger().Info("[AutoRespawner] agent_ready",
					"session_pane", sessionPane,
					"pattern", pattern)
				return true
			}
		}
		return false
	}

	if checkReady() {
		return nil
	}

	for time.Now().Before(deadline) {
		<-ticker.C
		if checkReady() {
			return nil
		}
	}

	return fmt.Errorf("agent not ready after %v", timeout)
}

// emitEvent sends a respawn event to the event channel.
func (r *AutoRespawner) emitEvent(result *RespawnResult) {
	event := RespawnEvent{
		SessionPane:     result.SessionPane,
		AgentType:       result.AgentType,
		RespawnedAt:     result.RespawnedAt,
		AccountRotated:  result.AccountRotated,
		PreviousAccount: result.PreviousAccount,
		NewAccount:      result.NewAccount,
	}

	// Non-blocking send
	select {
	case r.eventChan <- event:
		// Event sent successfully
	default:
		r.logger().Warn("[AutoRespawner] event_channel_full",
			"session_pane", result.SessionPane)
	}
}

// GetRetryCount returns the current retry count for a pane.
func (r *AutoRespawner) GetRetryCount(sessionPane string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	state, exists := r.retryState[sessionPane]
	if !exists {
		return 0
	}

	// Reset if expired
	if time.Since(state.LastReset) > r.Config.RetryResetDuration {
		return 0
	}

	return state.Count
}

// ResetRetryCount resets the retry counter for a pane.
func (r *AutoRespawner) ResetRetryCount(sessionPane string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.retryState, sessionPane)
}

// ResetAllRetryCounts clears all retry counters.
func (r *AutoRespawner) ResetAllRetryCounts() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.retryState = make(map[string]*paneRetryState)
}
