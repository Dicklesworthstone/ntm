package swarm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SessionOrchestrator handles creation and management of tmux sessions
// for the weighted multi-project swarm system.
//
// The orchestrator uses an explicit tmux binary path (via tmux.BinaryPath())
// to avoid issues with shell plugins (e.g., zsh tmux plugins) that may
// intercept or modify tmux commands when relying on PATH resolution.
type SessionOrchestrator struct {
	// TmuxClient is the tmux client used for session operations.
	// If nil, the default tmux client is used.
	TmuxClient *tmux.Client

	// StaggerDelay is the delay between pane creations to avoid rate limits.
	StaggerDelay time.Duration
}

// NewSessionOrchestrator creates a new SessionOrchestrator with default settings.
func NewSessionOrchestrator() *SessionOrchestrator {
	return &SessionOrchestrator{
		TmuxClient:   nil, // Use default client
		StaggerDelay: 300 * time.Millisecond,
	}
}

// NewSessionOrchestratorWithClient creates a SessionOrchestrator with a custom tmux client.
func NewSessionOrchestratorWithClient(client *tmux.Client) *SessionOrchestrator {
	return &SessionOrchestrator{
		TmuxClient:   client,
		StaggerDelay: 300 * time.Millisecond,
	}
}

// NewRemoteSessionOrchestrator creates a SessionOrchestrator configured for remote SSH execution.
// The host parameter should be in the format "user@host" (e.g., "ubuntu@192.168.1.100").
// All tmux operations will be executed on the remote host via SSH.
func NewRemoteSessionOrchestrator(host string) *SessionOrchestrator {
	return &SessionOrchestrator{
		TmuxClient:   tmux.NewClient(host),
		StaggerDelay: 300 * time.Millisecond,
	}
}

// NewRemoteSessionOrchestratorWithDelay creates a remote SessionOrchestrator with custom stagger delay.
// The host parameter should be in the format "user@host".
// The staggerDelay parameter controls the delay between pane creations.
func NewRemoteSessionOrchestratorWithDelay(host string, staggerDelay time.Duration) *SessionOrchestrator {
	return &SessionOrchestrator{
		TmuxClient:   tmux.NewClient(host),
		StaggerDelay: staggerDelay,
	}
}

// tmuxClient returns the configured tmux client or the default client.
func (o *SessionOrchestrator) tmuxClient() *tmux.Client {
	if o.TmuxClient != nil {
		return o.TmuxClient
	}
	return tmux.DefaultClient
}

// CreateSessionResult contains the result of creating a single session.
type CreateSessionResult struct {
	SessionSpec SessionSpec
	SessionName string
	PaneIDs     []string
	Error       error
}

// OrchestrationResult contains the complete result of session orchestration.
type OrchestrationResult struct {
	Sessions        []CreateSessionResult
	TotalPanes      int
	SuccessfulPanes int
	FailedPanes     int
	Errors          []error
}

// CreateSessions creates all sessions defined in the SwarmPlan.
// It creates sessions, splits panes, sets titles, and applies tiled layout.
func (o *SessionOrchestrator) CreateSessions(plan *SwarmPlan) (*OrchestrationResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	if len(plan.Sessions) == 0 {
		return &OrchestrationResult{}, nil
	}

	result := &OrchestrationResult{
		Sessions: make([]CreateSessionResult, 0, len(plan.Sessions)),
	}

	client := o.tmuxClient()

	for _, spec := range plan.Sessions {
		sessionResult := o.createSession(client, spec)
		result.Sessions = append(result.Sessions, sessionResult)

		if sessionResult.Error != nil {
			result.Errors = append(result.Errors, sessionResult.Error)
		}

		result.TotalPanes += spec.PaneCount
		result.SuccessfulPanes += len(sessionResult.PaneIDs)
		result.FailedPanes += spec.PaneCount - len(sessionResult.PaneIDs)
	}

	return result, nil
}

// createSession creates a single tmux session with its panes.
func (o *SessionOrchestrator) createSession(client *tmux.Client, spec SessionSpec) CreateSessionResult {
	result := CreateSessionResult{
		SessionSpec: spec,
		SessionName: spec.Name,
		PaneIDs:     make([]string, 0, spec.PaneCount),
	}

	// Validate session name
	if err := tmux.ValidateSessionName(spec.Name); err != nil {
		result.Error = fmt.Errorf("invalid session name %q: %w", spec.Name, err)
		return result
	}

	// Check if session already exists
	if client.SessionExists(spec.Name) {
		result.Error = fmt.Errorf("session %q already exists", spec.Name)
		return result
	}

	// Determine the directory for the session (use first pane's project or /tmp)
	directory := "/tmp"
	if len(spec.Panes) > 0 && spec.Panes[0].Project != "" {
		directory = spec.Panes[0].Project
	}

	// Create the session
	if err := client.CreateSession(spec.Name, directory); err != nil {
		result.Error = fmt.Errorf("failed to create session %q: %w", spec.Name, err)
		return result
	}

	// Get the initial pane ID
	panes, err := client.GetPanes(spec.Name)
	if err != nil || len(panes) == 0 {
		result.Error = fmt.Errorf("failed to get initial pane for session %q: %v", spec.Name, err)
		return result
	}

	// Set up the first pane — always track it even if spec.Panes is empty,
	// since tmux creates it unconditionally with CreateSession.
	firstPaneID := panes[0].ID
	if len(spec.Panes) > 0 {
		title := o.formatPaneTitle(spec.Name, spec.Panes[0])
		if err := o.setPaneTitleWithRetry(client, firstPaneID, title); err != nil {
			// Non-fatal, continue
		}
	}
	result.PaneIDs = append(result.PaneIDs, firstPaneID)

	// Create additional panes
	for i := 1; i < len(spec.Panes); i++ {
		paneSpec := spec.Panes[i]

		// Stagger pane creation to avoid rate limits
		if o.StaggerDelay > 0 && i > 0 {
			time.Sleep(o.StaggerDelay)
		}

		// Determine directory for this pane
		paneDir := "/tmp"
		if paneSpec.Project != "" {
			paneDir = paneSpec.Project
		}

		// Split the window to create a new pane
		paneID, err := client.SplitWindow(spec.Name, paneDir)
		if err != nil {
			slog.Warn("[SessionOrchestrator] split_window_failed",
				"session", spec.Name,
				"pane_index", i,
				"directory", paneDir,
				"error", err)
			continue
		}

		// Set pane title
		title := o.formatPaneTitle(spec.Name, paneSpec)
		if err := o.setPaneTitleWithRetry(client, paneID, title); err != nil {
			// Non-fatal, continue
		}

		result.PaneIDs = append(result.PaneIDs, paneID)
	}

	// Apply tiled layout for even pane distribution
	if err := client.ApplyTiledLayout(spec.Name); err != nil {
		// Non-fatal, panes are still created
	}

	return result
}

// formatPaneTitle formats a pane title according to NTM convention.
func (o *SessionOrchestrator) formatPaneTitle(sessionName string, pane PaneSpec) string {
	return tmux.FormatPaneName(sessionName, pane.AgentType, pane.Index, "")
}

// DestroySession destroys a single session by name.
func (o *SessionOrchestrator) DestroySession(sessionName string) error {
	client := o.tmuxClient()

	if !client.SessionExists(sessionName) {
		return fmt.Errorf("session %q does not exist", sessionName)
	}

	return client.KillSession(sessionName)
}

// DestroySessions destroys all sessions created from a SwarmPlan.
func (o *SessionOrchestrator) DestroySessions(plan *SwarmPlan) error {
	if plan == nil {
		return nil
	}

	var errs []error
	for _, spec := range plan.Sessions {
		if err := o.DestroySession(spec.Name); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to destroy %d session(s): %v", len(errs), errs[0])
	}

	return nil
}

// SessionExists checks if a session with the given name exists.
func (o *SessionOrchestrator) SessionExists(sessionName string) bool {
	return o.tmuxClient().SessionExists(sessionName)
}

// GetSessionPanes returns the panes in a session.
func (o *SessionOrchestrator) GetSessionPanes(sessionName string) ([]tmux.Pane, error) {
	return o.tmuxClient().GetPanes(sessionName)
}

// PaneGeometry represents the dimensions of a pane.
type PaneGeometry struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// GeometryResult contains the geometry verification result for a session.
type GeometryResult struct {
	SessionName    string         `json:"session_name"`
	PaneCount      int            `json:"pane_count"`
	Geometries     []PaneGeometry `json:"geometries"`
	IsUniform      bool           `json:"is_uniform"`
	MaxWidthDelta  int            `json:"max_width_delta"`
	MaxHeightDelta int            `json:"max_height_delta"`
}

// VerifyGeometry checks if panes in a session have uniform geometry.
// Returns geometry information and whether panes are uniformly sized.
// Tolerance allows for small variations (e.g., 1-2 cells difference).
func (o *SessionOrchestrator) VerifyGeometry(sessionName string, tolerance int) (*GeometryResult, error) {
	client := o.tmuxClient()

	panes, err := client.GetPanes(sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes for session %q: %w", sessionName, err)
	}

	if len(panes) == 0 {
		return &GeometryResult{
			SessionName: sessionName,
			PaneCount:   0,
			IsUniform:   true,
		}, nil
	}

	result := &GeometryResult{
		SessionName: sessionName,
		PaneCount:   len(panes),
		Geometries:  make([]PaneGeometry, len(panes)),
		IsUniform:   true,
	}

	// Collect geometries
	var minWidth, maxWidth, minHeight, maxHeight int
	for i, pane := range panes {
		result.Geometries[i] = PaneGeometry{
			Width:  pane.Width,
			Height: pane.Height,
		}

		if i == 0 {
			minWidth, maxWidth = pane.Width, pane.Width
			minHeight, maxHeight = pane.Height, pane.Height
		} else {
			if pane.Width < minWidth {
				minWidth = pane.Width
			}
			if pane.Width > maxWidth {
				maxWidth = pane.Width
			}
			if pane.Height < minHeight {
				minHeight = pane.Height
			}
			if pane.Height > maxHeight {
				maxHeight = pane.Height
			}
		}
	}

	result.MaxWidthDelta = maxWidth - minWidth
	result.MaxHeightDelta = maxHeight - minHeight

	// Check if within tolerance
	if result.MaxWidthDelta > tolerance || result.MaxHeightDelta > tolerance {
		result.IsUniform = false
	}

	return result, nil
}

// RebalanceGeometry reapplies tiled layout to ensure uniform pane sizes.
func (o *SessionOrchestrator) RebalanceGeometry(sessionName string) error {
	client := o.tmuxClient()

	if !client.SessionExists(sessionName) {
		return fmt.Errorf("session %q does not exist", sessionName)
	}

	return client.ApplyTiledLayout(sessionName)
}

// EnsureUniformGeometry verifies geometry and rebalances if needed.
// Returns the final geometry state after any corrections.
func (o *SessionOrchestrator) EnsureUniformGeometry(sessionName string, tolerance int) (*GeometryResult, error) {
	// First check current geometry
	result, err := o.VerifyGeometry(sessionName, tolerance)
	if err != nil {
		return nil, err
	}

	// If already uniform, return
	if result.IsUniform {
		return result, nil
	}

	// Rebalance and verify again
	if err := o.RebalanceGeometry(sessionName); err != nil {
		return result, fmt.Errorf("failed to rebalance geometry: %w", err)
	}

	// Re-verify after rebalancing
	return o.VerifyGeometry(sessionName, tolerance)
}

// GetAverageGeometry calculates the average pane dimensions for a session.
func (o *SessionOrchestrator) GetAverageGeometry(sessionName string) (*PaneGeometry, error) {
	panes, err := o.tmuxClient().GetPanes(sessionName)
	if err != nil {
		return nil, err
	}

	if len(panes) == 0 {
		return nil, fmt.Errorf("session %q has no panes", sessionName)
	}

	var totalWidth, totalHeight int
	for _, pane := range panes {
		totalWidth += pane.Width
		totalHeight += pane.Height
	}

	return &PaneGeometry{
		Width:  totalWidth / len(panes),
		Height: totalHeight / len(panes),
	}, nil
}

// TmuxBinaryPath returns the resolved path to the tmux binary being used.
// This uses an explicit binary path (preferring /usr/bin/tmux) to avoid
// issues with shell plugins that may intercept tmux commands.
func (o *SessionOrchestrator) TmuxBinaryPath() string {
	return tmux.BinaryPath()
}

// VerifyTmuxBinary checks if the tmux binary is accessible and functional.
// Returns the binary path if successful, or an error if tmux is not available.
func (o *SessionOrchestrator) VerifyTmuxBinary() (string, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return "", err
	}
	return tmux.BinaryPath(), nil
}

// TmuxBinaryInfo contains information about the tmux binary being used.
type TmuxBinaryInfo struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	IsRemote  bool   `json:"is_remote"`
}

// GetTmuxBinaryInfo returns detailed information about the tmux binary configuration.
func (o *SessionOrchestrator) GetTmuxBinaryInfo() *TmuxBinaryInfo {
	client := o.tmuxClient()
	isRemote := client.Remote != ""

	info := &TmuxBinaryInfo{
		Path:      tmux.BinaryPath(),
		Available: tmux.IsInstalled(),
		IsRemote:  isRemote,
	}

	return info
}

// IsRemote returns true if the orchestrator is configured for remote SSH execution.
func (o *SessionOrchestrator) IsRemote() bool {
	return o.tmuxClient().Remote != ""
}

// RemoteHost returns the remote host string (e.g., "user@host") if configured,
// or an empty string if the orchestrator operates locally.
func (o *SessionOrchestrator) RemoteHost() string {
	return o.tmuxClient().Remote
}

// TestConnection verifies connectivity to the remote host (or local tmux).
// For remote orchestrators, this tests SSH connectivity.
// For local orchestrators, this verifies tmux is installed and accessible.
// Returns nil if the connection test succeeds, or an error describing the failure.
func (o *SessionOrchestrator) TestConnection() error {
	client := o.tmuxClient()

	if client.Remote == "" {
		// Local: verify tmux is installed
		if !tmux.IsInstalled() {
			return fmt.Errorf("tmux is not installed locally")
		}
		return nil
	}

	// Remote: verify SSH connectivity and tmux availability
	if !client.IsInstalled() {
		return fmt.Errorf("cannot connect to remote host %q or tmux is not installed", client.Remote)
	}
	return nil
}

// VerifyRemoteTmux checks if tmux is available and functional on the target host.
// For local orchestrators, this behaves the same as VerifyTmuxBinary.
// For remote orchestrators, this verifies the remote tmux installation.
// Returns the tmux version string if successful, or an error if tmux is not available.
func (o *SessionOrchestrator) VerifyRemoteTmux() (string, error) {
	client := o.tmuxClient()

	// Get tmux version to verify it's working
	version, err := client.Run("-V")
	if err != nil {
		if client.Remote != "" {
			return "", fmt.Errorf("failed to verify tmux on remote host %q: %w", client.Remote, err)
		}
		return "", fmt.Errorf("failed to verify local tmux: %w", err)
	}

	return version, nil
}

// RemoteConnectionInfo contains detailed information about the remote connection.
type RemoteConnectionInfo struct {
	Host        string `json:"host"`            // Remote host (user@host) or empty for local
	IsRemote    bool   `json:"is_remote"`       // Whether this is a remote connection
	Connected   bool   `json:"connected"`       // Whether connection test succeeded
	TmuxVersion string `json:"tmux_version"`    // tmux version string if available
	Error       string `json:"error,omitempty"` // Error message if connection failed
}

// GetRemoteConnectionInfo returns detailed information about the remote connection status.
// This is useful for diagnostics and status reporting.
func (o *SessionOrchestrator) GetRemoteConnectionInfo() *RemoteConnectionInfo {
	client := o.tmuxClient()
	info := &RemoteConnectionInfo{
		Host:     client.Remote,
		IsRemote: client.Remote != "",
	}

	version, err := o.VerifyRemoteTmux()
	if err != nil {
		info.Connected = false
		info.Error = err.Error()
	} else {
		info.Connected = true
		info.TmuxVersion = version
	}

	return info
}

func (o *SessionOrchestrator) setPaneTitleWithRetry(client *tmux.Client, paneID, title string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = client.SetPaneTitle(paneID, title); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

type swarmSessionCreator interface {
	CreateSessions(plan *SwarmPlan) (*OrchestrationResult, error)
}

type swarmPaneLauncher interface {
	LaunchSwarm(ctx context.Context, plan *SwarmPlan, staggerDelay time.Duration) (*BatchLaunchResult, error)
}

type swarmPromptInjector interface {
	InjectSwarmWithContext(ctx context.Context, plan *SwarmPlan, prompt string) (*BatchInjectionResult, error)
}

// SwarmOrchestrator executes a full swarm launch workflow.
//
// Phases:
//  1. Create tmux sessions/panes from the plan
//  2. Launch agents in panes
//  3. (Optional) Inject an initial prompt to all panes
//
// This is intended to be used by CLI, robot mode, and future service layers without
// duplicating orchestration logic.
type SwarmOrchestrator struct {
	// SessionOrchestrator performs tmux session creation.
	// Defaults to NewSessionOrchestrator when nil.
	SessionOrchestrator swarmSessionCreator

	// PaneLauncher launches agents in panes.
	// Defaults to NewPaneLauncher when nil.
	PaneLauncher swarmPaneLauncher

	// PromptInjector injects prompts into panes.
	// Defaults to NewPromptInjector when nil.
	PromptInjector swarmPromptInjector

	// Logger for structured logs (optional).
	Logger *slog.Logger

	// StaggerDelay controls pacing between operations.
	// Used for LaunchSwarm; PromptInjector has its own internal pacing.
	StaggerDelay time.Duration
}

// SwarmOrchestrationResult is the aggregated output of a swarm execution.
type SwarmOrchestrationResult struct {
	StartedAt time.Time `json:"started_at"`
	Plan      *SwarmPlan

	// Phase results.
	Sessions   *OrchestrationResult  `json:"sessions,omitempty"`
	Launch     *BatchLaunchResult    `json:"launch,omitempty"`
	Injection  *BatchInjectionResult `json:"injection,omitempty"`
	ErrorCount int                   `json:"error_count"`

	// Errors holds concrete errors for internal callers; not JSON-serializable.
	Errors []error `json:"-"`
}

// NewSwarmOrchestrator creates a SwarmOrchestrator with default components.
func NewSwarmOrchestrator() *SwarmOrchestrator {
	return &SwarmOrchestrator{
		SessionOrchestrator: NewSessionOrchestrator(),
		PaneLauncher:        NewPaneLauncher(),
		PromptInjector:      NewPromptInjector(),
		Logger:              slog.Default(),
		StaggerDelay:        300 * time.Millisecond,
	}
}

func (o *SwarmOrchestrator) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

func (o *SwarmOrchestrator) sessionOrchestrator() swarmSessionCreator {
	if o.SessionOrchestrator != nil {
		return o.SessionOrchestrator
	}
	return NewSessionOrchestrator()
}

func (o *SwarmOrchestrator) paneLauncher() swarmPaneLauncher {
	if o.PaneLauncher != nil {
		return o.PaneLauncher
	}
	return NewPaneLauncher()
}

func (o *SwarmOrchestrator) promptInjector() swarmPromptInjector {
	if o.PromptInjector != nil {
		return o.PromptInjector
	}
	return NewPromptInjector()
}

func (o *SwarmOrchestrator) staggerDelay() time.Duration {
	if o.StaggerDelay > 0 {
		return o.StaggerDelay
	}
	return 300 * time.Millisecond
}

func planWithSuccessfulSessions(plan *SwarmPlan, result *OrchestrationResult) *SwarmPlan {
	if plan == nil || result == nil {
		return plan
	}

	ok := make(map[string]struct{}, len(result.Sessions))
	for _, sess := range result.Sessions {
		if sess.Error == nil {
			ok[sess.SessionName] = struct{}{}
		}
	}

	filtered := &SwarmPlan{
		CreatedAt:          plan.CreatedAt,
		ScanDir:            plan.ScanDir,
		Allocations:        plan.Allocations,
		AutoRotateAccounts: plan.AutoRotateAccounts,
		SessionsPerType:    plan.SessionsPerType,
		PanesPerSession:    plan.PanesPerSession,
		Ensemble:           plan.Ensemble,
	}

	for _, sess := range plan.Sessions {
		if _, keep := ok[sess.Name]; !keep {
			continue
		}
		filtered.Sessions = append(filtered.Sessions, sess)
		filtered.TotalAgents += sess.PaneCount
		switch sess.AgentType {
		case "cc":
			filtered.TotalCC += sess.PaneCount
		case "cod":
			filtered.TotalCod += sess.PaneCount
		case "gmi":
			filtered.TotalGmi += sess.PaneCount
		}
	}

	return filtered
}

// Execute runs the swarm orchestration workflow.
//
// If prompt is empty, the injection phase is skipped.
// The workflow is best-effort: session creation and agent launches proceed even if
// individual sessions/panes fail. Context cancellation aborts subsequent phases.
func (o *SwarmOrchestrator) Execute(ctx context.Context, plan *SwarmPlan, prompt string) (*SwarmOrchestrationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	logger := o.logger()
	result := &SwarmOrchestrationResult{
		StartedAt: time.Now().UTC(),
		Plan:      plan,
	}

	logger.Info("[SwarmOrchestrator] execute_start",
		"total_sessions", len(plan.Sessions),
		"total_agents", plan.TotalAgents,
		"inject_prompt", prompt != "")

	// Phase 1: sessions
	if err := ctx.Err(); err != nil {
		return result, err
	}
	logger.Info("[SwarmOrchestrator] phase_sessions_start", "sessions", len(plan.Sessions))
	sessionsResult, err := o.sessionOrchestrator().CreateSessions(plan)
	if err != nil {
		return result, err
	}
	if sessionsResult == nil {
		return result, fmt.Errorf("session orchestrator returned nil result")
	}
	result.Sessions = sessionsResult
	result.Errors = append(result.Errors, sessionsResult.Errors...)
	logger.Info("[SwarmOrchestrator] phase_sessions_complete",
		"successful_panes", sessionsResult.SuccessfulPanes,
		"failed_panes", sessionsResult.FailedPanes)

	execPlan := planWithSuccessfulSessions(plan, sessionsResult)

	// Phase 2: launch agents
	if err := ctx.Err(); err != nil {
		return result, err
	}
	logger.Info("[SwarmOrchestrator] phase_launch_start",
		"sessions", len(execPlan.Sessions),
		"total_agents", execPlan.TotalAgents,
		"stagger_delay_ms", o.staggerDelay().Milliseconds())
	launchResult, err := o.paneLauncher().LaunchSwarm(ctx, execPlan, o.staggerDelay())
	result.Launch = launchResult
	if launchResult != nil {
		result.Errors = append(result.Errors, launchResult.Errors...)
	}
	if err != nil {
		return result, err
	}
	if launchResult == nil {
		return result, fmt.Errorf("pane launcher returned nil result")
	}
	logger.Info("[SwarmOrchestrator] phase_launch_complete",
		"successful", launchResult.Successful,
		"failed", launchResult.Failed)

	// Phase 3: inject prompt (optional)
	if prompt != "" {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		logger.Info("[SwarmOrchestrator] phase_inject_start",
			"total_agents", execPlan.TotalAgents,
			"prompt_len", len(prompt))
		injectionResult, err := o.promptInjector().InjectSwarmWithContext(ctx, execPlan, prompt)
		result.Injection = injectionResult
		if injectionResult != nil && injectionResult.Failed > 0 {
			result.Errors = append(result.Errors, fmt.Errorf("prompt injection failed for %d/%d panes", injectionResult.Failed, injectionResult.TotalPanes))
		}
		if err != nil {
			return result, err
		}
		if injectionResult == nil {
			return result, fmt.Errorf("prompt injector returned nil result")
		}
		logger.Info("[SwarmOrchestrator] phase_inject_complete",
			"successful", injectionResult.Successful,
			"failed", injectionResult.Failed)
	} else {
		logger.Info("[SwarmOrchestrator] phase_inject_skipped")
	}

	result.ErrorCount = len(result.Errors)
	logger.Info("[SwarmOrchestrator] execute_complete",
		"errors", result.ErrorCount)

	return result, nil
}

// ShutdownConfig configures graceful shutdown behavior.
type ShutdownConfig struct {
	// GracefulTimeout is how long to wait for agents to exit gracefully.
	// Default: 5 seconds
	GracefulTimeout time.Duration

	// ForceKill forces immediate session destruction without graceful exit.
	// Default: false
	ForceKill bool
}

// DefaultShutdownConfig returns sensible defaults for graceful shutdown.
func DefaultShutdownConfig() ShutdownConfig {
	return ShutdownConfig{
		GracefulTimeout: 5 * time.Second,
		ForceKill:       false,
	}
}

// ShutdownResult contains the result of a graceful shutdown operation.
type ShutdownResult struct {
	SessionsDestroyed int           `json:"sessions_destroyed"`
	PanesKilled       int           `json:"panes_killed"`
	GracefulExits     int           `json:"graceful_exits"`
	ForceKills        int           `json:"force_kills"`
	Duration          time.Duration `json:"duration"`
	Errors            []error       `json:"-"`
}

// GracefulShutdown stops all agents and destroys all sessions in a swarm.
// It first attempts to gracefully exit agents, then force-kills if needed,
// and finally destroys all tmux sessions.
func (o *SwarmOrchestrator) GracefulShutdown(ctx context.Context, sessionNames []string, cfg ShutdownConfig) (*ShutdownResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	logger := o.logger()
	start := time.Now()
	result := &ShutdownResult{}

	if len(sessionNames) == 0 {
		return result, nil
	}

	logger.Info("[SwarmOrchestrator] shutdown_start",
		"sessions", len(sessionNames),
		"force", cfg.ForceKill,
		"timeout", cfg.GracefulTimeout)

	// Get the session orchestrator for session operations
	sessOrch, ok := o.sessionOrchestrator().(*SessionOrchestrator)
	if !ok {
		// If not a concrete SessionOrchestrator, use default
		sessOrch = NewSessionOrchestrator()
	}

	client := sessOrch.tmuxClient()

	// Phase 1: Send graceful exit signals to all agents (unless force kill)
	if cfg.ForceKill {
		// Count panes that will be force-killed (for stats)
		for _, sessionName := range sessionNames {
			if !sessOrch.SessionExists(sessionName) {
				continue
			}
			panes, err := sessOrch.GetSessionPanes(sessionName)
			if err != nil {
				continue
			}
			for _, pane := range panes {
				// Skip control panes (same logic as graceful path)
				if pane.Index == 1 {
					continue
				}
				result.ForceKills++
				result.PanesKilled++
			}
		}
	} else {
		for _, sessionName := range sessionNames {
			if err := ctx.Err(); err != nil {
				result.Duration = time.Since(start)
				return result, err
			}

			if !sessOrch.SessionExists(sessionName) {
				continue
			}

			panes, err := sessOrch.GetSessionPanes(sessionName)
			if err != nil {
				logger.Warn("[SwarmOrchestrator] get_panes_failed",
					"session", sessionName,
					"error", err)
				continue
			}

			for _, pane := range panes {
				// Skip control panes
				if pane.Index == 1 {
					continue
				}

				// Send graceful exit signal based on agent type
				if err := sendGracefulExit(client, pane); err != nil {
					logger.Warn("[SwarmOrchestrator] graceful_exit_failed",
						"pane", pane.ID,
						"agent", pane.Type,
						"error", err)
				} else {
					result.GracefulExits++
					result.PanesKilled++ // Only count as killed when signal was delivered
				}
			}
		}

		// Wait for graceful timeout
		if cfg.GracefulTimeout > 0 && result.GracefulExits > 0 {
			logger.Info("[SwarmOrchestrator] waiting_for_graceful_exit",
				"timeout", cfg.GracefulTimeout)

			select {
			case <-ctx.Done():
				result.Duration = time.Since(start)
				return result, ctx.Err()
			case <-time.After(cfg.GracefulTimeout):
			}
		}
	}

	// Phase 2: Destroy all sessions
	for _, sessionName := range sessionNames {
		if err := ctx.Err(); err != nil {
			result.Duration = time.Since(start)
			return result, err
		}

		if !sessOrch.SessionExists(sessionName) {
			logger.Debug("[SwarmOrchestrator] session_not_found",
				"session", sessionName)
			continue
		}

		if err := sessOrch.DestroySession(sessionName); err != nil {
			logger.Warn("[SwarmOrchestrator] destroy_session_failed",
				"session", sessionName,
				"error", err)
			result.Errors = append(result.Errors, err)
		} else {
			result.SessionsDestroyed++
			logger.Info("[SwarmOrchestrator] session_destroyed",
				"session", sessionName)
		}
	}

	result.Duration = time.Since(start)

	logger.Info("[SwarmOrchestrator] shutdown_complete",
		"sessions_destroyed", result.SessionsDestroyed,
		"panes_killed", result.PanesKilled,
		"graceful_exits", result.GracefulExits,
		"force_kills", result.ForceKills,
		"duration", result.Duration)

	return result, nil
}

// sendGracefulExit sends the appropriate exit signal for an agent type.
func sendGracefulExit(client *tmux.Client, pane tmux.Pane) error {
	agentType := string(pane.Type)

	switch agentType {
	case "cc", "claude", "claude-code":
		// Claude: Double Ctrl+C with 100ms gap
		if err := client.SendKeys(pane.ID, "\x03", false); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		return client.SendKeys(pane.ID, "\x03", false)

	case "cod", "codex":
		// Codex: /exit command
		return client.SendKeys(pane.ID, "/exit", true)

	case "gmi", "gemini":
		// Gemini: Escape then Ctrl+C
		if err := client.SendKeys(pane.ID, "\x1b", false); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		return client.SendKeys(pane.ID, "\x03", false)

	case "cursor", "windsurf", "aider", "ollama":
		// Just send Ctrl+C as a best effort graceful exit for these
		return client.SendKeys(pane.ID, "\x03", false)

	default:
		// Unknown agents: try standard Ctrl+C
		return client.SendKeys(pane.ID, "\x03", false)
	}
}

// ShutdownFromPlan gracefully shuts down all sessions defined in a swarm plan.
func (o *SwarmOrchestrator) ShutdownFromPlan(ctx context.Context, plan *SwarmPlan, cfg ShutdownConfig) (*ShutdownResult, error) {
	if plan == nil {
		return &ShutdownResult{}, nil
	}

	sessionNames := make([]string, 0, len(plan.Sessions))
	for _, sess := range plan.Sessions {
		sessionNames = append(sessionNames, sess.Name)
	}

	return o.GracefulShutdown(ctx, sessionNames, cfg)
}
