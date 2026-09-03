package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/completion"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

var (
	assignAuto         bool
	assignStrategy     string
	assignBeads        string
	assignLimit        int
	assignAgentType    string // Filter by agent type
	assignCCOnly       bool   // Alias for --agent=claude
	assignCodOnly      bool   // Alias for --agent=codex
	assignGmiOnly      bool   // Alias for --agent=gemini
	assignTemplate     string // Prompt template: impl, review, custom
	assignTemplateFile string // Custom template file path
	assignVerbose      bool
	assignQuiet        bool
	assignTimeout      time.Duration
	assignDryRun       bool // Alias for no --auto
	assignReserveFiles bool // Enable Agent Mail file reservations

	// Direct pane assignment flags
	assignPane       string // Direct pane assignment using canonical N, W.P, or %N grammar
	assignForce      bool   // Force assignment even if pane busy
	assignIgnoreDeps bool   // Ignore dependency checks
	assignPrompt     string // Custom prompt for direct assignment

	// Clear assignment flags
	assignClear       string // Clear specific bead assignments (comma-separated)
	assignClearPane   string // Clear all assignments for one canonical pane selector
	assignClearFailed bool   // Clear all failed assignments

	// Watch mode flags for continuous auto-assignment
	assignWatch         bool          // Enable watch mode for continuous auto-assignment on completion
	assignAutoReassign  bool          // Enable auto-reassignment of newly unblocked beads (default true in watch mode)
	assignWatchInterval time.Duration // How often to check for completions (default 30s)
	assignStopWhenDone  bool          // Exit watch mode when no more ready beads
	assignDelay         time.Duration // Optional pacing/backoff; never used as an ownership lock
	assignIdleThreshold time.Duration // Inactivity window before a watched assignment is stamped failed (0 = config/default)

	// Reassignment flags for moving beads between agents
	assignReassign string // Bead ID to reassign
	assignToPane   string // Target pane selector for reassignment/retry
	assignToType   string // Target agent type (auto-select idle agent of this type)

	// Retry flags for retrying failed assignments
	assignRetry       string // Bead ID to retry
	assignRetryFailed bool   // Retry all failed assignments

	// Repository binding (issue #123): explicitly pin the bead-source path so
	// `ntm assign <session>` returns the same ready-bead set regardless of the
	// caller's CWD. Used by long-running watchers (launchd/cron/systemd) where
	// CWD-walk discovery would otherwise pick up the wrong `.beads/`.
	assignRepoPath string
)

const (
	assignWatchOverlayKey = "F12"
	// Imperative dispatch gets more tmux scheduling headroom than status polling,
	// while retaining one second for the evidence to remain dispatch-current.
	assignObservationStageTimeout = statuspkg.DispatchObservationMaxAge - time.Second
)

var collectAssignAllocationPressure = collectLiveAssignAllocationPressure
var (
	claimBeadForAssignmentWithPolicy      = bv.ClaimBeadForAssignmentWithOperatorGatedLabels
	releaseBeadClaimForAssignment         = bv.ReleaseBeadClaim
	getBeadStatusForAssignment            = bv.GetBeadStatusContext
	getBeadAssignmentDetailsForAssignment = bv.GetBeadAssignmentDetailsContext
	runClearAssignmentsForCommand         = runClearAssignments
	getActionableRecommendationsForAssign = bv.GetActionableRecommendationsContext
	getActionableRecommendationsForWatch  = bv.GetActionableRecommendationsContext
	getIdleAgentsForWatchStop             = getIdleAgents
	startAssignWebhookForCommand          = func(projectDir, session string) (func() error, error) {
		if cfg == nil {
			return nil, nil
		}
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, err := webhook.StartBridgeFromProjectConfig(projectDir, session, events.DefaultBus, &redactCfg)
		if err != nil || bridge == nil {
			return nil, err
		}
		return bridge.Close, nil
	}
)

type assignSessionObserver interface {
	Observe(context.Context, string) (statuspkg.SessionObservation, error)
}

var newAssignSessionObserver = func() assignSessionObserver {
	detector := statuspkg.NewDetector()
	config := statuspkg.DefaultSessionObserverConfig(detector.Config())
	config.CaptureTimeout = assignObservationStageTimeout
	return statuspkg.NewSessionObserverWithDependencies(detector, config, statuspkg.SessionObserverDependencies{})
}

func observeAssignSession(ctx context.Context, session string) (statuspkg.SessionObservation, error) {
	if ctx == nil {
		return statuspkg.SessionObservation{}, errors.New("assignment observation context is required")
	}
	observationCtx, cancel := context.WithTimeout(ctx, assignObservationStageTimeout)
	defer cancel()

	observer := newAssignSessionObserver()
	if observer == nil {
		return statuspkg.SessionObservation{}, errors.New("assignment session observer is unavailable")
	}
	observation, err := observer.Observe(observationCtx, session)
	if err != nil {
		return observation, fmt.Errorf("observe assignment session %s: %w", session, err)
	}
	return observation, nil
}

func observeAssignSessionWithTimeout(
	ctx context.Context,
	session string,
	timeout time.Duration,
) (statuspkg.SessionObservation, error) {
	if ctx == nil {
		return statuspkg.SessionObservation{}, errors.New("assignment observation context is required")
	}
	observationCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(timeout))
	defer cancel()

	observation, observeErr := observeAssignSession(observationCtx, session)
	if observeErr != nil {
		return observation, preserveCommandContextError(observationCtx, observeErr)
	}
	if contextErr := observationCtx.Err(); contextErr != nil {
		return observation, contextErr
	}
	return observation, nil
}

func assignmentObservationFailureCode(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout
	}
	return "OBSERVATION_ERROR"
}

func currentAssignPaneObservation(observation statuspkg.SessionObservation, paneID string, now time.Time) (statuspkg.PaneObservation, error) {
	if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, now) {
		return statuspkg.PaneObservation{}, fmt.Errorf("assignment observation for pane %s is stale", paneID)
	}
	paneObservation, ok := observation.PaneByID(paneID)
	if !ok {
		return statuspkg.PaneObservation{}, fmt.Errorf("assignment observation has no unique pane %s", paneID)
	}
	if paneObservation.Current.Error != "" {
		return statuspkg.PaneObservation{}, fmt.Errorf("observe assignment pane %s: %s", paneID, paneObservation.Current.Error)
	}
	return paneObservation, nil
}

// assignAgentInfo holds information about an agent pane for assignment matching
type assignAgentInfo struct {
	pane              tmux.Pane
	agentType         string
	model             string
	state             string
	scrollback        string
	contextUsage      float64
	activeAssignments int
	resourceHeadroom  float64
}

// assignPaneRoleSet captures the persona roles a pane is explicitly bound to.
// A pane with neither flag set is untagged and eligible for every template.
type assignPaneRoleSet struct {
	implementer bool
	reviewer    bool
}

// assignPaneRoles resolves a pane's role bindings from (a) the pane title's
// user tags, (b) the persona profile named by the pane's durable variant —
// matching both the persona's name and its tags. Only the explicit role
// tokens "implementer" and "reviewer" count; broader descriptive tags
// ("review", "coding", …) are deliberately ignored so mixed-focus personas
// like the builtin architect stay eligible for both templates.
func assignPaneRoles(registry *persona.Registry, pane tmux.Pane) assignPaneRoleSet {
	var roles assignPaneRoleSet
	note := func(token string) {
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "implementer":
			roles.implementer = true
		case "reviewer":
			roles.reviewer = true
		}
	}
	for _, tag := range pane.Tags {
		note(tag)
	}
	if registry != nil {
		// Variant is a persona name for persona-spawned panes and model[@effort]
		// for model panes; ParsePaneVariant strips a reasoning-effort suffix.
		name, _ := tmux.ParsePaneVariant(pane.Variant)
		if profile, ok := registry.Get(name); ok {
			note(profile.Name)
			for _, tag := range profile.Tags {
				note(tag)
			}
		}
	}
	return roles
}

// filterAssignAgentsForTemplate drops panes explicitly bound to the opposite
// role of the impl/review templates: reviewer-only panes never receive
// implementation work and implementer-only panes never receive review work.
// Untagged, model-only, and dual-role panes stay eligible; other templates
// pass through unfiltered.
func filterAssignAgentsForTemplate(agents []assignAgentInfo, registry *persona.Registry, template string) []assignAgentInfo {
	template = strings.ToLower(strings.TrimSpace(template))
	if template != "impl" && template != "review" {
		return agents
	}
	eligible := make([]assignAgentInfo, 0, len(agents))
	for _, candidate := range agents {
		roles := assignPaneRoles(registry, candidate.pane)
		if template == "impl" && roles.reviewer && !roles.implementer {
			continue
		}
		if template == "review" && roles.implementer && !roles.reviewer {
			continue
		}
		eligible = append(eligible, candidate)
	}
	return eligible
}

// assignRoleEligibleAgents loads the project persona registry and applies the
// template role filter. A registry that fails to load fails closed: assigning
// review work to a reviewer-tagged pane requires knowing which panes those
// are, so a corrupt personas.toml must surface, not be skipped.
func assignRoleEligibleAgents(agents []assignAgentInfo, projectDir, template string) ([]assignAgentInfo, error) {
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load persona registry for role-aware assignment: %w", err)
	}
	return filterAssignAgentsForTemplate(agents, registry, template), nil
}

func newAssignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assign [session]",
		Short: "Intelligently assign work to agents based on BV triage",
		Long: `Analyze ready work from BV and recommend or execute task-to-agent assignments.

This command queries BV for prioritized ready work and matches tasks to idle agents
based on agent type strengths and the selected strategy.

Strategies:
  balanced    - Balance workload across agents (default)
  speed       - Prioritize quick task completion
  quality     - Prioritize agent-task match quality
  dependency  - Prioritize unblocking downstream work
  round-robin - Deterministic even distribution

Prompt Templates:
  impl   - "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first."
  review - "Review and verify bead {BEAD_ID}: {TITLE}. Run tests if applicable."
  custom - User provides template file (--template-file)

Direct Pane Assignment:
  Use --pane to assign a specific bead to a specific pane. This bypasses the
  normal strategy-based matching and directly assigns the bead to the pane.

	  ntm assign myproject --pane=3 --beads=bd-123     # Single-window pane 3
	  ntm assign myproject --pane=1.3 --beads=bd-123   # Window 1, pane 3
	  ntm assign myproject --pane=%7 --beads=bd-123    # Exact tmux pane ID
	  ntm assign myproject --pane=2 --beads=bd-123 --ignore-deps  # Skip dep checks

Clear Assignments:
  Use --clear to remove assignment from agents and release file reservations.
  Use --clear-pane to clear all assignments for a specific pane (agent crashed).
  Use --clear-failed to clear all failed assignments.
  Use --force to clear completed assignments.

  ntm assign myproject --clear bd-xyz             # Clear single assignment
  ntm assign myproject --clear bd-xyz,bd-abc      # Clear multiple assignments
  ntm assign myproject --clear-pane=3             # Clear all assignments for pane 3

Watch Mode (Dependency-Aware Auto-Assignment):
  Use --watch to enable continuous monitoring for task completions and automatic
  reassignment of newly unblocked beads to idle agents.

  ntm assign myproject --watch                      # Watch mode with auto-reassignment
  ntm assign myproject --watch --strategy=dependency # Watch with dependency-first strategy
  ntm assign myproject --watch --limit=2            # Limit to 2 assignments per cycle
  ntm assign myproject --watch --stop-when-done     # Exit when no more beads ready
  ntm assign myproject --watch --delay=5s           # 5s delay between assignments
  ntm assign myproject --watch --watch-interval=10s # Check every 10 seconds

Reassignment (Move Bead Between Agents):
  Use --reassign to move an assigned bead from one agent to another. This is useful
  when an agent is stuck, or when you want to redistribute work to a different agent.

  ntm assign myproject --reassign bd-xyz --to-pane=4         # Move to specific pane
  ntm assign myproject --reassign bd-xyz --to-type=codex     # Move to idle codex agent
  ntm assign myproject --reassign bd-xyz --to-pane=4 --prompt="Continue work"
  ntm assign myproject --reassign bd-xyz --to-pane=4 --force # Force even if pane busy

Retry Failed Assignments:
  Use --retry to retry a specific failed assignment, or --retry-failed to retry all
  failed assignments. Failed assignments are re-queued to idle agents.

  ntm assign myproject --retry bd-xyz                        # Retry specific bead
  ntm assign myproject --retry-failed                        # Retry all failed beads
  ntm assign myproject --retry bd-xyz --to-pane=4            # Retry to specific pane
  ntm assign myproject --retry-failed --to-type=claude       # Retry all to claude agents

Examples:
  ntm assign myproject                         # Show assignment recommendations
  ntm assign myproject --auto                  # Execute assignments without confirmation
  ntm assign myproject --strategy=quality      # Use quality-focused matching
  ntm assign myproject --strategy=round-robin  # Even distribution
  ntm assign myproject --beads=bd-123,bd-456   # Assign specific beads only
  ntm assign myproject --limit=5               # Limit to 5 assignments
  ntm assign myproject --cc-only               # Only assign to Claude agents
  ntm assign myproject --agent=codex           # Only assign to Codex agents
  ntm assign myproject --template=impl         # Use impl prompt template
  ntm assign myproject --json                  # Output as JSON
  ntm assign myproject --dry-run               # Preview without executing
  ntm assign myproject --clear bd-123          # Clear assignment for bead bd-123
  ntm assign myproject --clear-pane=3          # Clear all assignments for pane 3
  ntm assign myproject --clear-failed          # Clear all failed assignments
  ntm assign myproject --clear bd-123 --force  # Clear completed assignment
  ntm assign myproject --reassign bd-123 --to-pane=4   # Reassign to pane 4
  ntm assign myproject --reassign bd-123 --to-type=codex  # Reassign to idle codex
  ntm assign myproject --retry bd-123          # Retry failed bead bd-123
  ntm assign myproject --retry-failed          # Retry all failed assignments`,
		Args: cobra.MaximumNArgs(1),
		RunE: runAssign,
	}

	// Core flags
	cmd.Flags().BoolVar(&assignAuto, "auto", false, "Execute assignments without confirmation")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().StringVar(&assignBeads, "beads", "", "Comma-separated list of specific bead IDs to assign")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum number of assignments (0 = unlimited)")

	// Agent type filters
	cmd.Flags().StringVar(&assignAgentType, "agent", "", "Filter by agent type: any (no filter), claude, codex, gemini")
	cmd.Flags().BoolVar(&assignCCOnly, "cc-only", false, "Only assign to Claude agents (alias for --agent=claude)")
	cmd.Flags().BoolVar(&assignCodOnly, "cod-only", false, "Only assign to Codex agents (alias for --agent=codex)")
	cmd.Flags().BoolVar(&assignGmiOnly, "gmi-only", false, "Only assign to Gemini agents (alias for --agent=gemini)")

	// Prompt template flags
	cmd.Flags().StringVar(&assignTemplate, "template", "impl", "Prompt template: impl, review, custom")
	cmd.Flags().StringVar(&assignTemplateFile, "template-file", "", "Custom template file path (for --template=custom)")

	// Common flags
	cmd.Flags().BoolVarP(&assignVerbose, "verbose", "v", false, "Show detailed scoring/decision logs")
	cmd.Flags().BoolVarP(&assignQuiet, "quiet", "q", false, "Suppress non-essential output")
	cmd.Flags().DurationVar(&assignTimeout, "timeout", 30*time.Second, "Timeout for tmux observation and external calls (bv, br, Agent Mail)")
	cmd.Flags().BoolVar(&assignDryRun, "dry-run", false, "Preview mode (alias for no --auto)")
	cmd.Flags().BoolVar(&assignReserveFiles, "reserve-files", true, "Reserve file paths via Agent Mail before assignment")

	// Direct pane assignment flags
	cmd.Flags().StringVar(&assignPane, "pane", "", "Assign bead directly to exactly one N, W.P, or %N pane selector (requires --beads)")
	cmd.Flags().BoolVar(&assignForce, "force", false, "Force assignment even if pane is busy (also lets --clear remove completed or outcome-unknown assignments; forcing an outcome-unknown clear may duplicate an already-delivered dispatch)")
	cmd.Flags().BoolVar(&assignIgnoreDeps, "ignore-deps", false, "Ignore dependency checks for assignment")
	cmd.Flags().StringVar(&assignPrompt, "prompt", "", "Custom prompt for direct assignment")

	// Clear assignment flags
	cmd.Flags().StringVar(&assignClear, "clear", "", "Clear specific bead assignments (comma-separated bead IDs)")
	cmd.Flags().StringVar(&assignClearPane, "clear-pane", "", "Clear all assignments for one pane (N, W.P, or %N; use when agent crashed)")
	cmd.Flags().BoolVar(&assignClearFailed, "clear-failed", false, "Clear all failed assignments")

	// Watch mode flags
	cmd.Flags().BoolVar(&assignWatch, "watch", false, "Enable watch mode for continuous auto-assignment on completion")
	cmd.Flags().BoolVar(&assignAutoReassign, "auto-reassign", true, "Enable auto-reassignment of newly unblocked beads in watch mode")
	cmd.Flags().DurationVar(&assignWatchInterval, "watch-interval", 30*time.Second, "How often to check for completions in watch mode")
	cmd.Flags().BoolVar(&assignStopWhenDone, "stop-when-done", false, "Exit watch mode when no more beads are ready")
	cmd.Flags().DurationVar(&assignDelay, "delay", 0, "Pacing backoff between assignments in watch mode")
	cmd.Flags().DurationVar(&assignIdleThreshold, "idle-threshold", 0, "How long an agent may go silent before its watched assignment is stamped failed (default: [assign] idle_threshold config, else 15m)")

	// Reassignment flags for moving beads between agents
	cmd.Flags().StringVar(&assignReassign, "reassign", "", "Bead ID to reassign to a different agent")
	cmd.Flags().StringVar(&assignToPane, "to-pane", "", "Target pane N, W.P, or %N for reassignment/retry")
	cmd.Flags().StringVar(&assignToType, "to-type", "", "Target agent type for reassignment: claude, codex, gemini (auto-selects idle agent)")

	// Retry flags for retrying failed assignments
	cmd.Flags().StringVar(&assignRetry, "retry", "", "Retry a specific failed assignment (bead ID)")
	cmd.Flags().BoolVar(&assignRetryFailed, "retry-failed", false, "Retry all failed assignments")

	// Repository binding (issue #123)
	cmd.Flags().StringVar(&assignRepoPath, "repo", "", "Pin the bead-source repository path (overrides CWD discovery; required for daemon/cron use)")

	return cmd
}

func resolveAssignProjectDir(ctx context.Context, session string) (string, error) {
	if explicit := strings.TrimSpace(assignRepoPath); explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("--repo %q: %w", assignRepoPath, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("--repo %q is not accessible: %w", assignRepoPath, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("--repo %q is not a directory", assignRepoPath)
		}
		return filepath.Clean(abs), nil
	}

	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
		if err != nil {
			return "", err
		}
		session = resolved
	}

	return resolveExplicitProjectDirForSessionContext(ctx, session)
}

// configureAuthoritativeAssignmentPolicy strictly loads the selected global
// configuration merged with the project that actually owns the Beads data.
// Assignment must not inherit project gates from the caller's ambient CWD or
// continue after an invalid project overlay silently drops safety policy.
func configureAuthoritativeAssignmentPolicy(projectDir string) error {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return markCLIInvalidInput(errors.New("assignment safety policy requires an authoritative project directory"))
	}
	globalPath := selectedConfigPath()
	requireGlobal := cfgFile != "" || os.Getenv("NTM_CONFIG") != ""
	effective, err := config.LoadAssignmentPolicyStrict(projectDir, globalPath, requireGlobal)
	if err != nil {
		return markCLIInvalidInput(fmt.Errorf("load assignment safety policy for %s using %s: %w", projectDir, globalPath, err))
	}
	bv.ConfigureOperatorGatedLabels(effective.Assign.OperatorGatedLabels)
	if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, effective.Assign.OperatorGatedLabels); err != nil {
		return markCLIInvalidInput(fmt.Errorf("register assignment safety policy for %s: %w", projectDir, err))
	}
	return nil
}

// ensureAuthoritativeAssignmentPolicy installs assignment policy once for the
// exact authoritative project carried through a command path. Lower-level
// entry points still load defensively when called on their own.
func ensureAuthoritativeAssignmentPolicy(projectDir string, configuredProject *string) error {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir != "" {
		projectDir = filepath.Clean(projectDir)
	}
	if configuredProject != nil && *configuredProject == projectDir && projectDir != "" {
		return nil
	}
	if err := configureAuthoritativeAssignmentPolicy(projectDir); err != nil {
		return err
	}
	if configuredProject != nil {
		*configuredProject = projectDir
	}
	return nil
}

func isAutomatedAssignmentSafetyError(err error) bool {
	return errors.Is(err, errCLIInvalidInput) ||
		errors.Is(err, bv.ErrActionablePlanUnverified) ||
		errors.Is(err, bv.ErrActionableLabelsUnverified)
}

// prepareResolvedAssignCommand is the policy and webhook preflight shared by
// every non-clear CLI assignment mode. Clear is intentionally independent of
// config validity because it only removes durable assignment state.
func prepareResolvedAssignCommand(cmd *cobra.Command, session, projectDir string) (handled bool, policyProject string, closeWebhook func() error, err error) {
	if assignClear != "" || strings.TrimSpace(assignClearPane) != "" || assignClearFailed {
		return true, "", nil, runClearAssignmentsForCommand(cmd, session)
	}

	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &policyProject); err != nil {
		return false, "", nil, err
	}

	closeWebhook, bridgeErr := startAssignWebhookForCommand(policyProject, session)
	if bridgeErr != nil {
		slog.Default().Debug("webhook bridge init failed", "session", session, "error", bridgeErr)
		closeWebhook = nil
	}
	return false, policyProject, closeWebhook, nil
}

func runAssign(cmd *cobra.Command, args []string) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Resolve session
	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	// Resolve the project directory that backs this session's bead store.
	// `--repo` (issue #123) takes precedence over session/CWD discovery so
	// daemon callers (launchd, cron, systemd) can guarantee a stable bead
	// source regardless of where the watcher was launched from.
	projectDir, err := resolveAssignProjectDir(cmd.Context(), session)
	if err != nil {
		return err
	}
	handled, policyProject, closeWebhook, err := prepareResolvedAssignCommand(cmd, session, projectDir)
	if err != nil || handled {
		return err
	}
	if closeWebhook != nil {
		defer func() {
			if err := closeWebhook(); err != nil {
				slog.Default().Debug("webhook bridge close failed", "session", session, "error", err)
			}
		}()
	}

	// Apply config default for strategy if not explicitly set via flag
	if !cmd.Flags().Changed("strategy") {
		// Load config to get default strategy
		if cfg != nil && cfg.Assign.Strategy != "" {
			assignStrategy = cfg.Assign.Strategy
		}
	}

	// Validate strategy
	if !config.IsValidStrategy(assignStrategy) {
		return fmt.Errorf("unknown strategy %q. Valid strategies: %s",
			assignStrategy, strings.Join(config.ValidAssignStrategies, ", "))
	}

	// Handle reassignment operation
	if assignReassign != "" {
		return runReassignment(cmd.Context(), session)
	}

	// Handle retry operations
	if assignRetry != "" || assignRetryFailed {
		return runRetryAssignments(cmd.Context(), session)
	}

	// Handle watch mode for continuous auto-assignment
	if assignWatch {
		return runWatchMode(cmd, session, projectDir, policyProject)
	}

	// BV is preferred for dependency-aware assignment, but we can fall back to bd-ready
	// data when BV is unavailable.

	// Resolve agent type filter from flags
	agentTypeFilter := resolveAgentTypeFilter()

	// Parse beads if specified
	var beadIDs []string
	if assignBeads != "" {
		beadIDs = strings.Split(assignBeads, ",")
		for i := range beadIDs {
			beadIDs[i] = strings.TrimSpace(beadIDs[i])
		}
	}

	// --dry-run is an alias for no --auto
	if assignDryRun {
		assignAuto = false
	}

	// Build assign options
	assignOpts := &AssignCommandOptions{
		Session:         session,
		ProjectDir:      projectDir,
		BeadIDs:         beadIDs,
		Strategy:        assignStrategy,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		Verbose:         assignVerbose,
		Quiet:           assignQuiet,
		Auto:            assignAuto,
		Timeout:         assignTimeout,
		ReserveFiles:    assignReserveFiles,
		PaneSelector:    assignPane,
		Force:           assignForce,
		IgnoreDeps:      assignIgnoreDeps,
		Prompt:          assignPrompt,
		policyProject:   policyProject,
	}

	// Handle direct pane assignment if --pane is specified
	if strings.TrimSpace(assignPane) != "" {
		return runDirectPaneAssignment(cmd.Context(), assignOpts)
	}

	// For JSON output, use enhanced JSON output
	if IsJSONOutput() {
		return runAssignJSON(cmd.Context(), assignOpts)
	}

	// For text output, get the data and format it nicely
	assignOutput, err := getAssignOutputEnhanced(cmd.Context(), assignOpts)
	if err != nil {
		return err
	}

	// Display the recommendations
	if !assignQuiet {
		displayAssignOutputEnhanced(assignOutput, assignVerbose)
	}

	// If no recommendations, we're done
	if len(assignOutput.Assignments) == 0 {
		return nil
	}

	// If auto mode, execute assignments
	if assignAuto {
		return executeAssignmentsEnhanced(cmd.Context(), session, assignOutput, assignOpts)
	}

	// Otherwise, prompt for confirmation
	if !assignQuiet {
		fmt.Println()
		fmt.Print("Execute all assignments? [y/N] ")
	}
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "y" || response == "yes" {
		return executeAssignmentsEnhanced(cmd.Context(), session, assignOutput, assignOpts)
	}

	if !assignQuiet {
		fmt.Println("Assignments cancelled.")
	}
	return nil
}

// runWatchMode implements the --watch flag for continuous auto-assignment.
// It monitors for task completions and automatically assigns newly unblocked
// work to idle agents, with streaming output and graceful shutdown.
func runWatchMode(cmd *cobra.Command, session, projectDir, policyProject string) error {
	// Resolve agent type filter from flags
	agentTypeFilter := resolveAgentTypeFilter()

	// Resolve the completion-detector idle threshold: explicit flag wins,
	// then [assign] idle_threshold from config, then the package default
	// (GH#238 — 120s falsely failed quietly-working agents).
	idleThreshold := assignIdleThreshold
	if idleThreshold <= 0 {
		idleThreshold = loadSelectedConfigOrDefault().Assign.IdleThresholdDuration()
	}

	// Build auto-reassign options
	opts := &AutoReassignOptions{
		Session:         session,
		ProjectDir:      projectDir,
		Strategy:        assignStrategy,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		ReserveFiles:    assignReserveFiles,
		Verbose:         assignVerbose,
		Quiet:           assignQuiet,
		Timeout:         assignTimeout,
		AgentTypeFilter: agentTypeFilter,
		DryRun:          assignDryRun,
		IdleThreshold:   idleThreshold,
		policyProject:   policyProject,
	}

	// Load or create assignment store
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	// Create watch loop
	watchLoop := NewWatchLoop(session, store, opts)

	prepareAndAnnounceAssignWatchOverlay(
		watchLoop.logf,
		session,
		tmux.InTmux(),
		tmux.GetCurrentSession(),
		isOverlayKeyBound,
		setupOverlayBindingQuiet,
	)

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigDone := make(chan struct{})
	defer close(sigDone)

	go func() {
		select {
		case <-sigCh:
			watchLoop.logf("Received interrupt signal, shutting down gracefully...")
			cancel()
		case <-sigDone:
		}
	}()

	// Do initial assignment pass
	if assignDryRun {
		watchLoop.logf("Performing initial assignment pass... (dry-run: previewing only, no dispatch)")
	} else {
		watchLoop.logf("Performing initial assignment pass...")
	}

	assignOpts := &AssignCommandOptions{
		Session:         session,
		ProjectDir:      projectDir,
		Strategy:        assignStrategy,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		Verbose:         assignVerbose,
		Quiet:           true, // Suppress normal output during initial pass
		Timeout:         assignTimeout,
		ReserveFiles:    assignReserveFiles,
		policyProject:   policyProject,
	}

	initialOutput, err := getAssignOutputEnhanced(ctx, assignOpts)
	if err != nil {
		if isAutomatedAssignmentSafetyError(err) {
			return err
		}
		watchLoop.logf("Warning: Initial assignment failed: %v", err)
	} else if len(initialOutput.Assignments) > 0 {
		assignOpts.Quiet = assignQuiet // Restore quiet setting for execution
		if assignDryRun {
			// Dry-run: report the planned assignments, do not dispatch.
			watchLoop.logf("Initial assignment (dry-run): %d beads would be assigned (no dispatch)", len(initialOutput.Assignments))
			for _, assigned := range initialOutput.Assignments {
				watchLoop.logf("  Would assign %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
		} else if err := executeAssignmentsEnhanced(ctx, session, initialOutput, assignOpts); err != nil {
			watchLoop.logf("Warning: Failed to execute initial assignments: %v", err)
		} else {
			// FIX (d): count/log only beads actually SENT (PromptSent), not the
			// full planned set — some planned items may have been skipped inside
			// executeAssignmentsEnhanced (already handled, closed, or send-failed).
			sent := 0
			for _, assigned := range initialOutput.Assignments {
				if assigned.PromptSent {
					sent++
				}
			}
			watchLoop.logf("Initial assignment: %d beads dispatched", sent)
			for _, assigned := range initialOutput.Assignments {
				if !assigned.PromptSent {
					continue
				}
				watchLoop.mu.Lock()
				watchLoop.totalAssigned++
				watchLoop.lastAssignmentAt = time.Now()
				watchLoop.mu.Unlock()
				watchLoop.logf("  %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
		}
	} else {
		watchLoop.logf("Initial assignment: No beads to assign (no idle agents or no ready work)")
	}

	// Check stop-when-done before entering watch loop
	if assignStopWhenDone {
		stop, err := watchLoop.shouldStop(ctx)
		if err != nil {
			return fmt.Errorf("check initial watch completion: %w", err)
		}
		if stop {
			watchLoop.logf("No work available. Exiting watch mode.")
			fmt.Println(watchLoop.Summary())
			return nil
		}
	}

	// Run the watch loop
	if err := watchLoop.Run(ctx); err != nil && err != context.Canceled {
		return err
	}

	// Print summary
	fmt.Println()
	fmt.Println(watchLoop.Summary())

	// Save store state
	if err := store.Save(); err != nil {
		watchLoop.logf("Warning: Failed to save assignment store: %v", err)
	}

	return nil
}

type assignWatchOverlayPreparation struct {
	Hint    string
	Warning string
}

func prepareAndAnnounceAssignWatchOverlay(logf func(string, ...interface{}), session string, inTmux bool, currentSession string, isBound func(string) bool, ensureBinding func(string) error) assignWatchOverlayPreparation {
	prep := prepareAssignWatchOverlay(session, inTmux, currentSession, isBound, ensureBinding)
	announceAssignWatchOverlay(logf, prep)
	return prep
}

func announceAssignWatchOverlay(logf func(string, ...interface{}), prep assignWatchOverlayPreparation) {
	if logf == nil {
		return
	}
	if prep.Warning != "" {
		logf("%s", prep.Warning)
	}
	if prep.Hint != "" {
		logf("%s", prep.Hint)
	}
}

func prepareAssignWatchOverlay(session string, inTmux bool, currentSession string, isBound func(string) bool, ensureBinding func(string) error) assignWatchOverlayPreparation {
	if !shouldOfferAssignWatchOverlay(session, inTmux, currentSession) {
		return assignWatchOverlayPreparation{}
	}
	if isBound == nil {
		return assignWatchOverlayPreparation{}
	}

	if isBound(assignWatchOverlayKey) {
		return assignWatchOverlayPreparation{
			Hint: buildAssignWatchOverlayHint(assignWatchOverlayKey, false),
		}
	}
	if ensureBinding == nil {
		return assignWatchOverlayPreparation{}
	}

	if err := ensureBinding(assignWatchOverlayKey); err != nil {
		return assignWatchOverlayPreparation{
			Warning: buildAssignWatchOverlayWarning(assignWatchOverlayKey, err),
		}
	}

	return assignWatchOverlayPreparation{
		Hint: buildAssignWatchOverlayHint(assignWatchOverlayKey, true),
	}
}

func shouldOfferAssignWatchOverlay(session string, inTmux bool, currentSession string) bool {
	return inTmux && session != "" && currentSession != "" && currentSession == session
}

func buildAssignWatchOverlayHint(key string, installedNow bool) string {
	if installedNow {
		return fmt.Sprintf("Hint: installed the %s overlay binding. Press %s for the attention-aware dashboard overlay while assign --watch is running.", key, key)
	}
	return fmt.Sprintf("Hint: press %s for the attention-aware dashboard overlay while assign --watch is running.", key)
}

func buildAssignWatchOverlayWarning(key string, err error) string {
	return fmt.Sprintf("Warning: Could not auto-set up the %s overlay binding (%v); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.", key, err)
}

// resolveAgentTypeFilter determines the agent type filter from flags
func resolveAgentTypeFilter() string {
	// Explicit --agent flag takes precedence
	if assignAgentType != "" {
		return normalizeAgentTypeAlias(assignAgentType)
	}
	// Convenience flags
	if assignCCOnly {
		return "claude"
	}
	if assignCodOnly {
		return "codex"
	}
	if assignGmiOnly {
		return "gemini"
	}
	return "" // No filter
}

// normalizeAgentTypeAlias collapses operator-friendly "no filter" spellings
// (any/all/*) to the empty string and otherwise resolves provider aliases via
// robot.ResolveAgentType. Returning "" from this function unambiguously means
// "do not filter" — callers compare against "" to short-circuit filtering.
//
// Without this, `--agent any` propagated as the literal "any" through
// robot.ResolveAgentType (which is not an alias it recognizes), so every
// pane's provider was compared to the string "any" and excluded — making
// mixed-provider sessions report zero idle agents even with idle panes.
func normalizeAgentTypeAlias(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "any", "all", "*":
		return ""
	default:
		return robot.ResolveAgentType(raw)
	}
}

// normalizeBeadStatus folds case + delimiter variation (in-progress, In_Progress,
// IN PROGRESS) into a canonical lowercase underscore form so the assignment
// classifier can match against a small fixed set of statuses.
func normalizeBeadStatus(status string) string {
	canonical := strings.ToLower(strings.TrimSpace(status))
	canonical = strings.ReplaceAll(canonical, "-", "_")
	canonical = strings.ReplaceAll(canonical, " ", "_")
	return canonical
}

func classifyTriageRecForAssignmentForProject(projectDir string, rec bv.TriageRecommendation, activeAssignments map[string]struct{}) *SkippedItem {
	return classifyTriageRecForAssignmentWithGate(rec, activeAssignments, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func classifyTriageRecForAssignmentWithGate(rec bv.TriageRecommendation, activeAssignments map[string]struct{}, operatorGated func(string) bool) *SkippedItem {
	if len(rec.BlockedBy) > 0 {
		return &SkippedItem{
			BeadID:       rec.ID,
			BeadTitle:    rec.Title,
			Reason:       "blocked_by_dependency",
			BlockedByIDs: rec.BlockedBy,
		}
	}

	// Container types (epic) are grouping nodes, not implementation work: an
	// open unlabeled epic must never be dispatched to a worker pane. Checked
	// before the operator gate because the exclusion is intrinsic to the
	// issue type, not a policy that labels could lift.
	if bv.IsContainerBeadType(rec.Type) {
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "container_issue_type"}
	}

	for _, label := range rec.Labels {
		if operatorGated(label) {
			return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "operator_gated"}
		}
	}

	switch normalizeBeadStatus(rec.Status) {
	case "open", "ready":
		// assignable status; fall through to active-assignment check
	case "":
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "not_open_status"}
	case "in_progress":
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "already_in_progress"}
	case "blocked":
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "blocked_status"}
	case "closed", "resolved", "done":
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "closed_status"}
	default:
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "not_open_status"}
	}

	if _, ok := activeAssignments[rec.ID]; ok {
		return &SkippedItem{BeadID: rec.ID, BeadTitle: rec.Title, Reason: "already_assigned"}
	}

	return nil
}

func loadActionableRecommendationsForAssignment(ctx context.Context, projectDir string) ([]bv.TriageRecommendation, error) {
	return getActionableRecommendationsForAssign(ctx, projectDir, 0)
}

func partitionActionableRecommendationsForAssignment(
	recommendations []bv.TriageRecommendation,
	activeAssignments map[string]struct{},
	operatorGated func(string) bool,
) ([]bv.BeadPreview, []SkippedItem) {
	ready := make([]bv.BeadPreview, 0, len(recommendations))
	skipped := make([]SkippedItem, 0)
	for _, rec := range recommendations {
		if skip := classifyTriageRecForAssignmentWithGate(rec, activeAssignments, operatorGated); skip != nil {
			skipped = append(skipped, *skip)
			continue
		}
		ready = append(ready, bv.BeadPreview{
			ID:       rec.ID,
			Title:    rec.Title,
			Priority: fmt.Sprintf("P%d", rec.Priority),
		})
	}
	return ready, skipped
}

func classifyLiveAssignmentDetailsForProject(projectDir string, details *bv.BeadAssignmentDetails, activeAssignments map[string]struct{}) *SkippedItem {
	return classifyLiveAssignmentDetailsWithGate(details, activeAssignments, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func classifyLiveAssignmentDetailsWithGate(details *bv.BeadAssignmentDetails, activeAssignments map[string]struct{}, operatorGated func(string) bool) *SkippedItem {
	if details == nil {
		return &SkippedItem{Reason: "missing_live_details"}
	}
	if skip := classifyTriageRecForAssignmentWithGate(bv.TriageRecommendation{
		ID:        details.ID,
		Title:     details.Title,
		Type:      details.IssueType,
		Status:    details.Status,
		Labels:    append([]string(nil), details.Labels...),
		BlockedBy: append([]string(nil), details.BlockedBy...),
	}, nil, operatorGated); skip != nil {
		return skip
	}
	if details.DeferUntil != nil && details.DeferUntil.After(time.Now()) {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "deferred"}
	}
	if details.Pinned {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "pinned"}
	}
	if details.Ephemeral {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "ephemeral"}
	}
	if details.Template {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "template"}
	}
	if details.Wisp || strings.Contains(strings.ToLower(details.ID), "-wisp-") {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "wisp"}
	}
	if _, ok := activeAssignments[details.ID]; ok {
		return &SkippedItem{BeadID: details.ID, BeadTitle: details.Title, Reason: "already_assigned"}
	}
	return nil
}

// loadActiveAssignmentBeadIDs returns the set of bead IDs that have an active
// assignment record in this session's local assignment store. Used to suppress
// re-dispatch of beads that have already been claimed by a live pane but whose
// upstream status (bv/br) hasn't caught up yet.
//
// An unreadable store is terminal: treating unknown durable occupancy as empty
// could dispatch a second bead generation to an already-owned pane.
func loadActiveAssignmentBeadIDs(session string) (map[string]struct{}, error) {
	active := make(map[string]struct{})
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return nil, fmt.Errorf("load active assignment beads: %w", err)
	}
	for _, item := range store.ListActive() {
		active[item.BeadID] = struct{}{}
	}
	return active, nil
}

// loadActiveAssignmentPanes returns the stable identities of panes that
// currently hold an active assignment (StatusAssigned or StatusWorking).
//
// loadActiveAssignmentBeadIDs dedups by BEAD, which does not prevent a pane that
// is mid-flight on bead A — but momentarily showing an idle prompt between turns
// — from being handed a second bead B. Excluding any pane with an active
// assignment from the idle pool closes that double-dispatch window: a pane is
// dispatchable iff state=="idle" AND it is not busy AND it holds no active
// assignment.
//
// Errors are intentionally swallowed (an unreadable store is treated as empty,
// matching loadActiveAssignmentBeadIDs): briefly double-dispatching is less bad
// than blocking all assignment on a transient store-read failure.
func loadActiveAssignmentPanes(session string) (map[string]struct{}, error) {
	active := make(map[string]struct{})
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return active, fmt.Errorf("load active assignment panes: %w", err)
	}
	for _, item := range store.ListActive() {
		key, identityErr := assignment.CanonicalPaneIdentity(item)
		if identityErr != nil {
			return active, identityErr
		}
		active[key] = struct{}{}
	}
	return active, nil
}

func assignmentPaneIsActive(active map[string]struct{}, pane tmux.Pane) bool {
	keys := []string{
		assignmentPaneStableKey(pane),
		assignmentPaneTarget(pane),
	}
	for _, key := range keys {
		if _, ok := active[key]; ok {
			return true
		}
	}
	return false
}

// countSkippedByReason counts SkippedItems matching a specific reason. Used to
// keep BlockedCount in the assignment summary semantically narrow ("blocked by
// a dependency") even after the broader skipped-reason taxonomy was added.
func countSkippedByReason(items []SkippedItem, reason string) int {
	n := 0
	for i := range items {
		if items[i].Reason == reason {
			n++
		}
	}
	return n
}

func resolveAssignTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	if assignTimeout > 0 {
		return assignTimeout
	}
	return 30 * time.Second
}

func assignHumanDiagnostics(verbose bool) bool {
	return verbose && !IsJSONOutput()
}

// AssignCommandOptions holds all options for the assign command
type AssignCommandOptions struct {
	Session         string
	ProjectDir      string
	BeadIDs         []string
	Strategy        string
	Limit           int
	AgentTypeFilter string
	Template        string
	TemplateFile    string
	Verbose         bool
	Quiet           bool
	Auto            bool // Execute planned assignments without confirmation.
	Timeout         time.Duration
	ReserveFiles    bool // Reserve file paths via Agent Mail before assignment

	// Direct pane assignment options
	PaneSelector string // Direct pane assignment using N, W.P, or %N
	Force        bool   // Force assignment even if pane busy
	IgnoreDeps   bool   // Ignore dependency checks
	Prompt       string // Custom prompt for direct assignment

	// Clear assignment options
	Clear     string // Clear specific bead assignments (comma-separated)
	ClearPane string // Clear all assignments for one canonical pane selector

	// policyProject records the exact authoritative project whose assignment
	// policy was already installed during this command path.
	policyProject string
	// actionablePreflightVerified distinguishes a verified empty set from an
	// absent preflight so spawn can reuse admission evidence without refetching.
	actionablePreflightVerified bool
	verifiedActionable          []bv.TriageRecommendation
}

// AssignOutputEnhanced is the enhanced output structure matching the spec.
type AssignOutputEnhanced struct {
	Strategy    string                `json:"strategy"`
	Assignments []AssignmentItem      `json:"assignments"`
	Skipped     []SkippedItem         `json:"skipped"`
	Summary     AssignSummaryEnhanced `json:"summary"`
	Allocation  *AssignAllocationView `json:"allocation,omitempty"`
	Errors      []string              `json:"-"`
}

// AssignmentItem represents a single assignment in JSON output.
type AssignmentItem struct {
	BeadID          string `json:"bead_id"`
	BeadTitle       string `json:"bead_title"`
	rawBeadTitle    string
	Pane            int                               `json:"pane"` // Window-local display index; never use for identity.
	PaneTarget      string                            `json:"pane_target"`
	PaneID          string                            `json:"pane_id,omitempty"`
	AgentType       string                            `json:"agent_type"`
	AgentName       string                            `json:"agent_name"`
	Status          string                            `json:"status"`      // assigned|working|completed|failed
	PromptSent      bool                              `json:"prompt_sent"` // Whether prompt was sent
	AssignedAt      string                            `json:"assigned_at"` // ISO8601 timestamp
	Score           float64                           `json:"score,omitempty"`
	Reasoning       string                            `json:"reasoning,omitempty"`
	ReasonCodes     []string                          `json:"reason_codes,omitempty"`
	ScoreComponents *assign.AllocationScoreComponents `json:"score_components,omitempty"`
}

// AssignAllocationView is a compact JSON summary of the pressure-aware
// allocation planner used by balanced assignment recommendations.
type AssignAllocationView struct {
	SchemaVersion   string   `json:"schema_version"`
	Decision        string   `json:"decision"`
	PressureMissing bool     `json:"pressure_missing,omitempty"`
	BVMissing       bool     `json:"bv_missing,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

// SkippedItem represents a skipped bead
type SkippedItem struct {
	BeadID       string   `json:"bead_id"`
	BeadTitle    string   `json:"bead_title"`
	Reason       string   `json:"reason"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"` // Only set when reason is "blocked"
}

// AssignSummaryEnhanced contains summary statistics
type AssignSummaryEnhanced struct {
	TotalBeadCount    int `json:"total_bead_count"`
	ActionableCount   int `json:"actionable_count"` // Beads with no blockers
	BlockedCount      int `json:"blocked_count"`    // Beads blocked by dependencies
	AssignedCount     int `json:"assigned_count"`
	SkippedCount      int `json:"skipped_count"`
	IdleAgents        int `json:"idle_agent_count"`
	CycleWarningCount int `json:"cycle_warning_count,omitempty"` // Beads in dependency cycles
}

// AssignError represents an error in assign JSON output.
type AssignError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// AssignEnvelope is the standard JSON envelope for assign operations.
type AssignEnvelope[T any] struct {
	Command    string       `json:"command"`
	Subcommand string       `json:"subcommand,omitempty"`
	Session    string       `json:"session"`
	Timestamp  string       `json:"timestamp"`
	Success    bool         `json:"success"`
	Data       *T           `json:"data,omitempty"`
	Warnings   []string     `json:"warnings"`
	Error      *AssignError `json:"error,omitempty"`
}

// DirectAssignItem represents a single direct assignment in JSON output.
type DirectAssignItem struct {
	BeadID       string   `json:"bead_id"`
	BeadTitle    string   `json:"bead_title"`
	Pane         int      `json:"pane"` // Window-local display index.
	PaneTarget   string   `json:"pane_target"`
	PaneID       string   `json:"pane_id"`
	AgentType    string   `json:"agent_type"`
	Status       string   `json:"status"`
	Prompt       string   `json:"prompt"`
	PromptSent   bool     `json:"prompt_sent"`
	AssignedAt   string   `json:"assigned_at"`
	PaneWasBusy  bool     `json:"pane_was_busy,omitempty"`
	DepsIgnored  bool     `json:"deps_ignored,omitempty"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"`
}

// DirectAssignFileReservations holds file reservation details for direct assignment.
type DirectAssignFileReservations struct {
	Requested []string `json:"requested"`
	Granted   []string `json:"granted"`
	Denied    []string `json:"denied"`
}

// DirectAssignData holds the data for a direct pane assignment.
type DirectAssignData struct {
	Assignment       *DirectAssignItem             `json:"assignment"`
	FileReservations *DirectAssignFileReservations `json:"file_reservations,omitempty"`
	// Receipt is the wrapper-grade dispatch receipt — populated for
	// both real dispatches and `--dry-run` planning so wrappers can
	// drop their parallel dispatch log. See ntm#128.
	Receipt *DispatchReceipt `json:"receipt,omitempty"`
}

// detectAgentTypeFromTitle determines agent type from pane title
func detectAgentTypeFromTitle(title string) string {
	title = strings.ToLower(title)

	if idx := strings.LastIndex(title, "__"); idx >= 0 && idx+2 < len(title) {
		typePart := title[idx+2:]
		if underscore := strings.IndexByte(typePart, '_'); underscore >= 0 {
			typePart = typePart[:underscore]
		}
		switch agent.AgentType(typePart).Canonical() {
		case agent.AgentTypeClaudeCode:
			return "claude"
		case agent.AgentTypeCodex:
			return "codex"
		case agent.AgentTypeGemini:
			return "gemini"
		case agent.AgentTypeAntigravity:
			return "antigravity"
		case agent.AgentTypeCursor:
			return "cursor"
		case agent.AgentTypeWindsurf:
			return "windsurf"
		case agent.AgentTypeAider:
			return "aider"
		case agent.AgentTypeOpencode:
			return "oc"
		case agent.AgentTypeOllama:
			return "ollama"
		case agent.AgentTypeUser:
			return "user"
		}
	}

	switch {
	case strings.Contains(title, "claude"):
		return "claude"
	case strings.Contains(title, "codex"):
		return "codex"
	case strings.Contains(title, "antigravity"):
		return "antigravity"
	case strings.Contains(title, "gemini"):
		return "gemini"
	case strings.Contains(title, "cursor"):
		return "cursor"
	case strings.Contains(title, "windsurf"):
		return "windsurf"
	case strings.Contains(title, "aider"):
		return "aider"
	case strings.Contains(title, "opencode"):
		return "oc"
	case strings.Contains(title, "ollama"):
		return "ollama"
	case strings.Contains(title, "user"):
		return "user"
	}
	return "unknown"
}

// detectModelFromTitle extracts model variant from title
func detectModelFromTitle(agentType, title string) string {
	// Simplified model detection
	title = strings.ToLower(title)
	if strings.Contains(title, "opus") {
		return "opus"
	}
	if strings.Contains(title, "sonnet") {
		return "sonnet"
	}
	if strings.Contains(title, "haiku") {
		return "haiku"
	}
	return ""
}

func assignmentAgentName(session, agentType string, paneIndex int) string {
	if session == "" {
		return ""
	}
	return fmt.Sprintf("%s_%s_%d", session, agentType, paneIndex)
}

func assignmentAgentNameForPane(session, agentType string, pane tmux.Pane, multiWindow bool) string {
	if !multiWindow {
		return assignmentAgentName(session, agentType, pane.Index)
	}
	if session == "" {
		return ""
	}
	return fmt.Sprintf("%s_%s_%d_%d", session, agentType, pane.WindowIndex, pane.Index)
}

// assignmentAgentIdentityForPane returns the Agent Mail identity to address a
// pane by. It prefers the pane's REGISTERED spawn-time identity (written to
// the canonical per-pane identity file by registerSpawnedAgents), and only
// falls back to the synthetic Session_type_index name when no identity file
// exists. The synthetic fallback is NOT a registered agent, so Agent Mail
// rejects file reservations and message deliveries made under it ("Agent not
// found in project") whenever the pane was registered under its real
// adjective+noun name (GH#239).
func assignmentAgentIdentityForPane(projectDir, session, agentType string, pane tmux.Pane, multiWindow bool) string {
	if strings.TrimSpace(projectDir) != "" && strings.TrimSpace(pane.ID) != "" {
		if name, _ := agentmail.ResolveIdentity(projectDir, pane.ID); strings.TrimSpace(name) != "" {
			return name
		}
	}
	return assignmentAgentNameForPane(session, agentType, pane, multiWindow)
}

func assignmentPaneTarget(pane tmux.Pane) string {
	return pane.Ref().Physical()
}

func assignmentPaneStableKey(pane tmux.Pane) string {
	return pane.Ref().StableKey()
}

func assignmentAgentPanes(agents []assignAgentInfo) []tmux.Pane {
	panes := make([]tmux.Pane, 0, len(agents))
	for _, candidate := range agents {
		panes = append(panes, candidate.pane)
	}
	return panes
}

// calculateMatchConfidence calculates how well an agent matches a task
func calculateMatchConfidence(agentType string, bead bv.BeadPreview, strategy string) float64 {
	baseConfidence := 0.7

	// Task type inference
	title := strings.ToLower(bead.Title)
	taskType := "task"

	taskPatterns := map[string][]string{
		"bug":           {"bug", "fix", "broken", "error", "crash"},
		"testing":       {"test", "spec", "coverage"},
		"documentation": {"doc", "readme", "comment"},
		"refactor":      {"refactor", "cleanup", "improve"},
		"analysis":      {"analyze", "investigate", "research"},
		"feature":       {"feature", "implement", "add", "new"},
	}

	for tt, patterns := range taskPatterns {
		for _, p := range patterns {
			if strings.Contains(title, p) {
				taskType = tt
				break
			}
		}
	}

	// Agent strengths
	strengths := map[string]map[string]float64{
		"claude": {"analysis": 0.9, "refactor": 0.9, "documentation": 0.8, "feature": 0.8, "bug": 0.7},
		"codex":  {"feature": 0.9, "bug": 0.8, "task": 0.8, "refactor": 0.6},
		"gemini": {"documentation": 0.9, "analysis": 0.8, "feature": 0.8},
	}

	if agentStrengths, ok := strengths[agentType]; ok {
		if strength, ok := agentStrengths[taskType]; ok {
			baseConfidence = strength
		}
	}

	// Strategy adjustments
	switch strategy {
	case "speed":
		baseConfidence = (baseConfidence + 0.9) / 2
	case "dependency":
		priority := parsePriorityString(bead.Priority)
		if priority <= 1 {
			baseConfidence = min(baseConfidence+0.1, 0.95)
		}
	}

	return baseConfidence
}

// parsePriorityString converts "P0"-"P4" to integer
func parsePriorityString(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 {
			return int(n)
		}
	}
	return 2
}

// buildReasoning creates explanation for assignment
func buildReasoning(agentType string, bead bv.BeadPreview, strategy string) string {
	var reasons []string

	title := strings.ToLower(bead.Title)
	priority := parsePriorityString(bead.Priority)

	// Task-agent match
	if agentType == "claude" && (strings.Contains(title, "refactor") || strings.Contains(title, "analyze")) {
		reasons = append(reasons, "Claude excels at analysis/refactoring")
	} else if agentType == "codex" && (strings.Contains(title, "feature") || strings.Contains(title, "implement")) {
		reasons = append(reasons, "Codex excels at implementations")
	} else if agentType == "gemini" && strings.Contains(title, "doc") {
		reasons = append(reasons, "Gemini excels at documentation")
	}

	// Priority
	switch priority {
	case 0:
		reasons = append(reasons, "critical priority")
	case 1:
		reasons = append(reasons, "high priority")
	}

	// Strategy
	switch strategy {
	case "balanced":
		reasons = append(reasons, "balanced workload")
	case "speed":
		reasons = append(reasons, "optimizing for speed")
	case "quality":
		reasons = append(reasons, "optimizing for quality")
	case "dependency":
		reasons = append(reasons, "prioritizing unblocks")
	}

	if len(reasons) == 0 {
		return "available agent matched to available work"
	}

	return strings.Join(reasons, "; ")
}

// getAgentStyle returns a style for an agent type
func getAgentStyle(agentType string, th theme.Theme) lipgloss.Style {
	var color lipgloss.Color
	switch agentType {
	case "claude":
		color = th.Claude
	case "codex":
		color = th.Codex
	case "gemini":
		color = th.Gemini
	case "antigravity":
		color = th.Lavender
	default:
		color = th.Text
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

// runAssignJSON handles JSON output for the assign command
func runAssignJSON(ctx context.Context, opts *AssignCommandOptions) error {
	assignOutput, err := getAssignOutputEnhanced(ctx, opts)
	if err != nil {
		code := "ASSIGN_ERROR"
		if errors.Is(err, errCLIInvalidInput) {
			code = robot.ErrCodeInvalidFlag
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		// Return error as JSON using standard envelope
		envelope := AssignEnvelope[AssignOutputEnhanced]{
			Command:   "assign",
			Session:   opts.Session,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Success:   false,
			Data:      nil,
			Warnings:  []string{},
			Error: &AssignError{
				Code:    code,
				Message: err.Error(),
			},
		}
		// bd-oqwmf: signal non-zero exit after writing the success:false envelope.
		return emitJSONFailureEnvelope(envelope)
	}

	if opts.Auto && len(assignOutput.Assignments) > 0 {
		executionOpts := *opts
		executionOpts.Quiet = true
		if executionErr := executeAssignmentsEnhanced(ctx, opts.Session, assignOutput, &executionOpts); executionErr != nil {
			code := "ASSIGNMENT_FAILED"
			if errors.Is(executionErr, errCLIInvalidInput) {
				code = robot.ErrCodeInvalidFlag
			} else if errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			warnings := append([]string(nil), assignOutput.Errors...)
			if warnings == nil {
				warnings = []string{}
			}
			return emitJSONFailureEnvelope(AssignEnvelope[AssignOutputEnhanced]{
				Command:   "assign",
				Session:   opts.Session,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Success:   false,
				Data:      assignOutput,
				Warnings:  warnings,
				Error: &AssignError{
					Code:    code,
					Message: executionErr.Error(),
				},
			})
		}
	}

	// Collect warnings from errors field
	var warnings []string
	if len(assignOutput.Errors) > 0 {
		warnings = assignOutput.Errors
	}
	if warnings == nil {
		warnings = []string{}
	}

	// Build full JSON response using standard envelope
	envelope := AssignEnvelope[AssignOutputEnhanced]{
		Command:   "assign",
		Session:   opts.Session,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Success:   true,
		Data:      assignOutput,
		Warnings:  warnings,
		Error:     nil,
	}

	return json.NewEncoder(os.Stdout).Encode(envelope)
}

// getAssignOutputEnhanced builds the enhanced assignment output
func getAssignOutputEnhanced(ctx context.Context, opts *AssignCommandOptions) (*AssignOutputEnhanced, error) {
	if ctx == nil {
		return nil, errors.New("assignment planning context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("assignment planning canceled: %w", err)
	}
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, opts.Session)
		if err != nil {
			return nil, fmt.Errorf("resolve assignment project: %w", err)
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		return nil, err
	}
	exists, err := tmux.SessionExistsContext(ctx, opts.Session)
	if err != nil {
		return nil, fmt.Errorf("check assignment session: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Get panes from tmux
	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	// Build agent info and filter by type if needed
	var idleAgents []assignAgentInfo

	// Panes holding an active assignment must be excluded from the idle pool
	// even if they momentarily show an idle prompt between turns (FIX C). This
	// is the watch-dispatch path, so the guard is what keeps the periodic
	// ready-work re-scan (FIX B) from double-dispatching in-flight work.
	activePanes, err := loadActiveAssignmentPanes(opts.Session)
	if err != nil {
		return nil, err
	}
	observeCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
	defer cancel()
	observation, err := observeAssignSession(observeCtx, opts.Session)
	if err != nil {
		return nil, err
	}

	for _, pane := range panes {
		at := agentTypeForPane(pane)
		if at == "user" || at == "unknown" {
			continue
		}

		// Apply agent type filter
		if opts.AgentTypeFilter != "" && at != opts.AgentTypeFilter {
			continue
		}

		// Skip panes that already hold an active assignment.
		if assignmentPaneIsActive(activePanes, pane) {
			continue
		}

		paneObservation, observationErr := currentAssignPaneObservation(observation, pane.ID, time.Now())
		if observationErr != nil {
			return nil, observationErr
		}
		model := detectModelFromTitle(at, pane.Title)
		state := string(paneObservation.Current.Status.State)

		if paneObservation.SafeToDispatch() {
			idleAgents = append(idleAgents, assignAgentInfo{
				pane:       pane,
				agentType:  at,
				model:      model,
				state:      state,
				scrollback: paneObservation.RawOutput,
			})
		}
	}

	// Get beads from bv. Source candidates from the FULL dependency-aware
	// actionable set (bv --robot-plan), ranked by triage scoring — bv's
	// --robot-triage is capped at ≤10 recommendations, which silently starves
	// large/heavily-gated backlogs whose top-ranked rows are epics/gated/blocked
	// (issue #197). Triage still provides ordering and the rich BlockedBy/Labels
	// fields the filters below rely on.
	var allRecs []bv.TriageRecommendation
	if opts.actionablePreflightVerified {
		allRecs = cloneSpawnActionableRecommendations(opts.verifiedActionable)
	} else {
		allRecs, err = loadActionableRecommendationsForAssignment(ctx, projectDir)
	}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("assignment planning canceled while reading actionable work: %w", ctxErr)
		}
		return nil, fmt.Errorf("automated assignment stopped because actionable work could not be verified: %w", err)
	}

	// Plan membership and labels are already authoritative. Re-check local
	// occupancy and the remaining assignment invariants before allocation so a
	// tracker/cache race still fails closed.
	activeAssignments, err := loadActiveAssignmentBeadIDs(opts.Session)
	if err != nil {
		return nil, err
	}
	readyBeads, blockedBeads := partitionActionableRecommendationsForAssignment(allRecs, activeAssignments, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
	if opts.Verbose && !IsJSONOutput() {
		for _, skip := range blockedBeads {
			if len(skip.BlockedByIDs) > 0 {
				fmt.Fprintf(os.Stderr, "[DEP] Skipping %s - %s: %v\n", skip.BeadID, skip.Reason, skip.BlockedByIDs)
			} else {
				fmt.Fprintf(os.Stderr, "[DEP] Skipping %s - %s\n", skip.BeadID, skip.Reason)
			}
		}
	}
	if opts.Verbose && !IsJSONOutput() && len(blockedBeads) > 0 {
		fmt.Fprintf(os.Stderr, "[DEP] Filtered %d non-actionable beads, %d actionable\n", len(blockedBeads), len(readyBeads))
	}

	// Filter to specific beads if requested
	if len(opts.BeadIDs) > 0 {
		beadSet := make(map[string]bool)
		for _, b := range opts.BeadIDs {
			beadSet[b] = true
		}
		var filtered []bv.BeadPreview
		for _, b := range readyBeads {
			if beadSet[b.ID] {
				filtered = append(filtered, b)
			}
		}
		readyBeads = filtered
		// Also filter blockedBeads to only include requested ones
		var filteredBlocked []SkippedItem
		for _, b := range blockedBeads {
			if beadSet[b.BeadID] {
				filteredBlocked = append(filteredBlocked, b)
			}
		}
		blockedBeads = filteredBlocked
	}

	// Filter out beads in dependency cycles (with warning)
	var cycleWarnings int
	var cyclicBeads []SkippedItem
	cycles, err := CheckCycles(ctx, projectDir, false)
	if err != nil {
		return nil, fmt.Errorf("inspect assignment dependency cycles: %w", err)
	}
	if len(cycles) > 0 {
		var nonCyclic []bv.BeadPreview
		for _, bead := range readyBeads {
			if IsBeadInCycle(bead.ID, cycles) {
				cyclicBeads = append(cyclicBeads, SkippedItem{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Reason:    "in_dependency_cycle",
				})
				cycleWarnings++
				if assignHumanDiagnostics(opts.Verbose) {
					fmt.Fprintf(os.Stderr, "[DEP] Excluding %s from assignment (in dependency cycle)\n", bead.ID)
				}
			} else {
				nonCyclic = append(nonCyclic, bead)
			}
		}
		readyBeads = nonCyclic
	}

	// Limit ready beads to 50
	if len(readyBeads) > 50 {
		readyBeads = readyBeads[:50]
	}

	// Combine all skipped beads
	allSkipped := append(blockedBeads, cyclicBeads...)

	// Persona role gating: reviewer-only panes never receive impl work and
	// implementer-only panes never receive review work.
	idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, opts.Template)
	if err != nil {
		return nil, err
	}

	result := &AssignOutputEnhanced{
		Strategy:    opts.Strategy,
		Assignments: make([]AssignmentItem, 0),
		Skipped:     allSkipped, // Blocked + cyclic beads
		Summary: AssignSummaryEnhanced{
			TotalBeadCount:  len(readyBeads) + len(blockedBeads) + cycleWarnings,
			ActionableCount: len(readyBeads),
			// BlockedCount stays semantically narrow — only counts beads blocked
			// by an upstream dependency. Other non-actionable reasons
			// (in_progress, operator_gated, already_assigned) inflate the
			// generic Skipped slice but not this metric.
			BlockedCount:      countSkippedByReason(blockedBeads, "blocked_by_dependency"),
			IdleAgents:        len(idleAgents),
			CycleWarningCount: cycleWarnings,
		},
	}

	// No idle agents available
	if len(idleAgents) == 0 {
		for _, bead := range readyBeads {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: bead.ID,
				Reason: "no_idle_agents",
			})
		}
		result.Summary.SkippedCount = len(readyBeads)
		return redactAssignOutputForProjection(result), nil
	}

	// No beads to assign
	if len(readyBeads) == 0 {
		return redactAssignOutputForProjection(result), nil
	}

	// Generate assignments using strategy
	assignments, allocationPlan := generateAssignmentsEnhancedWithPlan(ctx, idleAgents, readyBeads, opts, true)
	result.Allocation = assignAllocationView(allocationPlan)
	if allocationPlan != nil && allocationPlan.Decision == assign.AllocationDecisionDefer && len(assignments) == 0 {
		for _, bead := range readyBeads {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID:    bead.ID,
				BeadTitle: bead.Title,
				Reason:    string(assign.AllocationReasonCriticalPressure),
			})
		}
	}

	// Apply limit
	if opts.Limit > 0 && len(assignments) > opts.Limit {
		// Mark excess as skipped
		for _, item := range assignments[opts.Limit:] {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: item.BeadID,
				Reason: "limit_reached",
			})
		}
		assignments = assignments[:opts.Limit]
	}

	result.Assignments = assignments
	result.Summary.AssignedCount = len(assignments)
	result.Summary.SkippedCount = len(result.Skipped)

	return redactAssignOutputForProjection(result), nil
}

func forcedRedactedAssignmentText(value string) string {
	redactionConfig := config.Default().Redaction.ToRedactionLibConfig()
	if cfg != nil {
		redactionConfig = cfg.Redaction.ToRedactionLibConfig()
	}
	redactionConfig.Mode = redaction.ModeRedact
	return redaction.ScanAndRedact(value, redactionConfig).Output
}

func redactAssignOutputForProjection(output *AssignOutputEnhanced) *AssignOutputEnhanced {
	if output == nil {
		return nil
	}
	for index := range output.Assignments {
		item := &output.Assignments[index]
		if item.rawBeadTitle == "" {
			item.rawBeadTitle = item.BeadTitle
		}
		item.BeadTitle = forcedRedactedAssignmentText(item.BeadTitle)
	}
	for index := range output.Skipped {
		output.Skipped[index].BeadTitle = forcedRedactedAssignmentText(output.Skipped[index].BeadTitle)
	}
	return output
}

// generateAssignmentsEnhanced creates assignment recommendations using the enhanced strategy logic.
func generateAssignmentsEnhanced(ctx context.Context, agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions) []AssignmentItem {
	assignments, _ := generateAssignmentsEnhancedWithPlan(ctx, agents, beads, opts, true)
	return assignments
}

func generateAssignmentsEnhancedWithPlan(ctx context.Context, agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions, bvAvailable bool) ([]AssignmentItem, *assign.AllocationPlan) {
	if usesAllocationPlanner(opts) {
		assignedAt := time.Now().UTC().Format(time.RFC3339)
		plan := assign.PlanAllocations(buildAssignAllocationInput(ctx, agents, beads, opts, bvAvailable))
		return assignmentItemsFromAllocationPlan(plan, assignedAt), &plan
	}
	return generateAssignmentsLegacy(agents, beads, opts), nil
}

func usesAllocationPlanner(opts *AssignCommandOptions) bool {
	if opts == nil {
		return true
	}
	strategy := strings.ToLower(strings.TrimSpace(opts.Strategy))
	return strategy == "" || strategy == "balanced"
}

// generateAssignmentsLegacy preserves the explicit non-balanced strategy behavior.
func generateAssignmentsLegacy(agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions) []AssignmentItem {
	var assignments []AssignmentItem
	assignedAt := time.Now().UTC().Format(time.RFC3339)
	defaultStatus := string(assignment.StatusAssigned)
	multiWindow := tmux.PanesSpanMultipleWindows(assignmentAgentPanes(agents))

	switch strings.ToLower(opts.Strategy) {
	case "round-robin":
		// Deterministic round-robin: bead[i] -> agent[i % N]
		// Score is always 1.0 (all assignments equally valid in round-robin)
		// Distribution: beads evenly spread, first agents get +1 if uneven
		if assignHumanDiagnostics(opts.Verbose) && len(agents) > 0 {
			// Log distribution plan
			base := len(beads) / len(agents)
			extra := len(beads) % len(agents)
			fmt.Fprintf(os.Stderr, "Round-robin distribution plan: %d beads across %d agents\n", len(beads), len(agents))
			for i, a := range agents {
				count := base
				if i < extra {
					count++
				}
				fmt.Fprintf(os.Stderr, "  Agent %d (%s): %d beads\n", a.pane.Index, a.agentType, count)
			}
		}
		for i, bead := range beads {
			if len(agents) == 0 {
				break
			}
			agent := agents[i%len(agents)]
			assignments = append(assignments, AssignmentItem{
				BeadID:     bead.ID,
				BeadTitle:  bead.Title,
				Pane:       agent.pane.Index,
				PaneTarget: assignmentPaneTarget(agent.pane),
				PaneID:     agent.pane.ID,
				AgentType:  agent.agentType,
				AgentName:  assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, agent.agentType, agent.pane, multiWindow),
				Status:     defaultStatus,
				PromptSent: false,
				AssignedAt: assignedAt,
				Score:      1.0, // Round-robin: all assignments equally valid
				Reasoning:  fmt.Sprintf("round-robin slot %d → agent %d", i+1, i%len(agents)),
			})
		}

	case "quality":
		// Quality: assign each bead to the best-matching available agent
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64

			for i := range agents {
				if usedAgents[assignmentPaneStableKey(agents[i].pane)] {
					continue
				}
				score := assign.GetAgentScoreByString(agents[i].agentType, inferTaskTypeFromBead(bead))
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignmentItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.pane.Index,
					PaneTarget: assignmentPaneTarget(bestAgent.pane),
					PaneID:     bestAgent.pane.ID,
					AgentType:  bestAgent.agentType,
					AgentName:  assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, bestAgent.agentType, bestAgent.pane, multiWindow),
					Status:     defaultStatus,
					PromptSent: false,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  buildReasoning(bestAgent.agentType, bead, "quality"),
				})
				usedAgents[assignmentPaneStableKey(bestAgent.pane)] = true
			}
		}

	case "speed":
		// Speed: assign to first available agent
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			for i := range agents {
				if usedAgents[assignmentPaneStableKey(agents[i].pane)] {
					continue
				}
				score := (calculateMatchConfidence(agents[i].agentType, bead, "speed") + 0.9) / 2
				assignments = append(assignments, AssignmentItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       agents[i].pane.Index,
					PaneTarget: assignmentPaneTarget(agents[i].pane),
					PaneID:     agents[i].pane.ID,
					AgentType:  agents[i].agentType,
					AgentName:  assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, agents[i].agentType, agents[i].pane, multiWindow),
					Status:     defaultStatus,
					PromptSent: false,
					AssignedAt: assignedAt,
					Score:      score,
					Reasoning:  buildReasoning(agents[i].agentType, bead, "speed"),
				})
				usedAgents[assignmentPaneStableKey(agents[i].pane)] = true
				break
			}
		}

	case "dependency":
		// Dependency: prioritize by unblocks count (already sorted by bv)
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64

			for i := range agents {
				if usedAgents[assignmentPaneStableKey(agents[i].pane)] {
					continue
				}
				score := calculateMatchConfidence(agents[i].agentType, bead, "dependency")
				// Boost for high priority
				priority := parsePriorityString(bead.Priority)
				if priority <= 1 {
					score = min(score+0.1, 0.95)
				}
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignmentItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.pane.Index,
					PaneTarget: assignmentPaneTarget(bestAgent.pane),
					PaneID:     bestAgent.pane.ID,
					AgentType:  bestAgent.agentType,
					AgentName:  assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, bestAgent.agentType, bestAgent.pane, multiWindow),
					Status:     defaultStatus,
					PromptSent: false,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  buildReasoning(bestAgent.agentType, bead, "dependency"),
				})
				usedAgents[assignmentPaneStableKey(bestAgent.pane)] = true
			}
		}

	default: // balanced
		// Balanced: spread work evenly, considering existing load from AssignmentStore
		agentAssignCounts := make(map[string]int)
		agentLastAssigned := make(map[string]time.Time)

		// Pre-populate counts from AssignmentStore (live tracking of active assignments)
		if opts.Session != "" {
			store, err := assignment.LoadStore(opts.Session)
			if err == nil && store != nil {
				for _, a := range store.ListActive() {
					paneKey, identityErr := assignment.CanonicalPaneIdentity(a)
					if identityErr != nil {
						continue
					}
					agentAssignCounts[paneKey]++
					// Track most recent assignment time for tie-breaking
					if agentLastAssigned[paneKey].Before(a.AssignedAt) {
						agentLastAssigned[paneKey] = a.AssignedAt
					}
				}
			}
		}

		for _, bead := range beads {
			var bestAgent *assignAgentInfo
			var bestScore float64
			minAssigns := int(^uint(0) >> 1)
			var leastRecentTime time.Time

			for i := range agents {
				paneKey := assignmentPaneStableKey(agents[i].pane)
				count := agentAssignCounts[paneKey]
				score := calculateMatchConfidence(agents[i].agentType, bead, "balanced")
				lastAssign := agentLastAssigned[paneKey]

				// Tie-breaker cascade:
				// 1. Prefer agents with fewer active assignments
				// 2. On tie: prefer higher capability score
				// 3. On tie: prefer least-recently assigned (or never assigned)
				// 4. On tie: prefer lower pane index (deterministic)
				shouldPick := false
				if count < minAssigns {
					shouldPick = true
				} else if count == minAssigns {
					if score > bestScore {
						shouldPick = true
					} else if score == bestScore {
						// Tie-breaker: least-recently assigned wins
						if bestAgent == nil {
							shouldPick = true
						} else if lastAssign.IsZero() && !leastRecentTime.IsZero() {
							// Never assigned beats previously assigned
							shouldPick = true
						} else if !lastAssign.IsZero() && !leastRecentTime.IsZero() && lastAssign.Before(leastRecentTime) {
							shouldPick = true
						} else if lastAssign.Equal(leastRecentTime) && assignmentPaneTarget(agents[i].pane) < assignmentPaneTarget(bestAgent.pane) {
							// Final tie-breaker: physical topology order for determinism.
							shouldPick = true
						}
					}
				}

				if shouldPick {
					minAssigns = count
					bestScore = score
					bestAgent = &agents[i]
					leastRecentTime = lastAssign
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, AssignmentItem{
					BeadID:     bead.ID,
					BeadTitle:  bead.Title,
					Pane:       bestAgent.pane.Index,
					PaneTarget: assignmentPaneTarget(bestAgent.pane),
					PaneID:     bestAgent.pane.ID,
					AgentType:  bestAgent.agentType,
					AgentName:  assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, bestAgent.agentType, bestAgent.pane, multiWindow),
					Status:     defaultStatus,
					PromptSent: false,
					AssignedAt: assignedAt,
					Score:      bestScore,
					Reasoning:  buildReasoning(bestAgent.agentType, bead, "balanced"),
				})
				bestKey := assignmentPaneStableKey(bestAgent.pane)
				agentAssignCounts[bestKey]++
				// Update last assigned time for this session's assignments
				now := time.Now()
				agentLastAssigned[bestKey] = now
			}
		}
	}

	return assignments
}

func buildAssignAllocationInput(ctx context.Context, agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions, bvAvailable bool) assign.AllocationInput {
	session := ""
	if opts != nil {
		session = opts.Session
	}
	stats := loadAssignAllocationStats(session)
	allocationAgents := make([]assign.AllocationAgent, 0, len(agents))
	totalActive := stats.totalActive
	totalHeadroom := 0.0

	for _, agentInfo := range agents {
		agentID := assignAllocationAgentID(session, agentInfo)
		paneKey := assignmentPaneStableKey(agentInfo.pane)
		active := agentInfo.activeAssignments + stats.activeByPane[paneKey]
		totalActive += agentInfo.activeAssignments
		headroom := assignAgentResourceHeadroom(agentInfo, active)
		totalHeadroom += headroom
		allocationAgents = append(allocationAgents, assign.AllocationAgent{
			ID:                agentID,
			Session:           session,
			PaneIndex:         agentInfo.pane.Index,
			PaneTarget:        assignmentPaneTarget(agentInfo.pane),
			PaneID:            agentInfo.pane.ID,
			AgentType:         tmux.AgentType(agentInfo.agentType).Canonical(),
			Idle:              agentInfo.state == "" || agentInfo.state == "idle",
			ContextUsage:      clampAssignScore(agentInfo.contextUsage),
			ActiveAssignments: active,
			AssignmentLimit:   1,
			ResourceHeadroom:  headroom,
		})
	}

	sessionHeadroom := 0.0
	if len(allocationAgents) > 0 {
		sessionHeadroom = totalHeadroom / float64(len(allocationAgents))
	}

	return assign.AllocationInput{
		ReadyBeads: buildAssignAllocationBeads(beads),
		Agents:     allocationAgents,
		Sessions: []assign.AllocationSession{{
			Name:              session,
			ActiveAssignments: totalActive,
			AssignmentLimit:   totalActive + len(allocationAgents),
			ResourceHeadroom:  sessionHeadroom,
		}},
		Pressure:    collectAssignAllocationPressure(ctx),
		Fairness:    assign.AllocationFairness{AgentRecentAssignments: stats.recentByAgent, SessionRecentAssignments: stats.recentBySession},
		BVAvailable: bvAvailable,
	}
}

type assignAllocationStats struct {
	activeByPane    map[string]int
	recentByAgent   map[string]int
	recentBySession map[string]int
	totalActive     int
}

func loadAssignAllocationStats(session string) assignAllocationStats {
	stats := assignAllocationStats{
		activeByPane:    make(map[string]int),
		recentByAgent:   make(map[string]int),
		recentBySession: make(map[string]int),
	}
	if strings.TrimSpace(session) == "" {
		return stats
	}
	store, err := assignment.LoadStoreStrict(session)
	if err != nil || store == nil {
		return stats
	}
	for _, item := range store.ListActive() {
		paneKey, identityErr := assignment.CanonicalPaneIdentity(item)
		if identityErr != nil {
			continue
		}
		stats.activeByPane[paneKey]++
		stats.totalActive++
		agentID := strings.TrimSpace(item.AgentName)
		if agentID == "" {
			agentID = fmt.Sprintf("%s_%s_%s", session, item.AgentType, paneKey)
		}
		stats.recentByAgent[agentID]++
		stats.recentBySession[session]++
	}
	return stats
}

func buildAssignAllocationBeads(beads []bv.BeadPreview) []assign.AllocationReadyBead {
	out := make([]assign.AllocationReadyBead, 0, len(beads))
	for _, bead := range beads {
		taskType := assign.TaskType(inferTaskTypeFromBead(bead))
		out = append(out, assign.AllocationReadyBead{
			ID:           bead.ID,
			Title:        bead.Title,
			TaskType:     taskType,
			Priority:     parsePriorityString(bead.Priority),
			ResourceCost: assignBeadResourceCost(bead, taskType),
		})
	}
	return out
}

func assignBeadResourceCost(bead bv.BeadPreview, taskType assign.TaskType) float64 {
	title := strings.ToLower(bead.Title)
	if strings.Contains(title, "performance") ||
		strings.Contains(title, "load") ||
		strings.Contains(title, "large") ||
		strings.Contains(title, "swarm") ||
		strings.Contains(title, "benchmark") {
		return 0.80
	}
	switch taskType {
	case assign.TaskBug, assign.TaskDocs, assign.TaskDocumentation, assign.TaskTesting, assign.TaskChore:
		return 0.35
	case assign.TaskFeature, assign.TaskRefactor, assign.TaskAnalysis:
		return 0.65
	default:
		return 0.50
	}
}

func assignAllocationAgentID(session string, agentInfo assignAgentInfo) string {
	physical := assignmentPaneTarget(agentInfo.pane)
	if session != "" {
		return fmt.Sprintf("%s_%s_%s", session, agentInfo.agentType, physical)
	}
	return fmt.Sprintf("%s_%s", agentInfo.agentType, physical)
}

func assignAgentResourceHeadroom(agentInfo assignAgentInfo, activeAssignments int) float64 {
	if agentInfo.resourceHeadroom > 0 {
		return clampAssignScore(agentInfo.resourceHeadroom)
	}
	contextPenalty := clampAssignScore(agentInfo.contextUsage) * 0.35
	backlogPenalty := assignScrollbackBacklog(agentInfo.scrollback) * 0.25
	activePenalty := min(float64(max(activeAssignments, 0))*0.20, 0.60)
	return clampAssignScore(1.0 - contextPenalty - backlogPenalty - activePenalty)
}

func assignScrollbackBacklog(scrollback string) float64 {
	if scrollback == "" {
		return 0
	}
	linePressure := min(float64(strings.Count(scrollback, "\n"))/800.0, 1.0)
	bytePressure := min(float64(len(scrollback))/200000.0, 1.0)
	return max(linePressure, bytePressure)
}

func collectLiveAssignAllocationPressure(ctx context.Context) assign.AllocationPressure {
	if ctx == nil || ctx.Err() != nil {
		return assign.AllocationPressure{}
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	g := pressure.New(pressure.Config{
		Mode:      pressure.ModeObserve,
		Providers: []pressure.Provider{pressure.NewSystemProvider()},
	})
	snapshot := g.Refresh(ctx)
	if len(snapshot.Readings) == 0 {
		return assign.AllocationPressure{}
	}
	return assign.AllocationPressure{
		Available:     true,
		Level:         snapshot.Overall.String(),
		Limiting:      assignPressureLimitingSources(snapshot.Limiting),
		AgentHeadroom: assignPressureAgentHeadroom(snapshot.Overall.String()),
	}
}

func assignPressureLimitingSources(sources []pressure.Source) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, string(source))
	}
	return out
}

func assignPressureAgentHeadroom(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical":
		return 0
	case "high":
		return 1
	case "elevated":
		return 2
	default:
		return 8
	}
}

func assignmentItemsFromAllocationPlan(plan assign.AllocationPlan, assignedAt string) []AssignmentItem {
	items := make([]AssignmentItem, 0, len(plan.Recommendations))
	for _, recommendation := range plan.Recommendations {
		components := recommendation.ScoreComponents
		items = append(items, AssignmentItem{
			BeadID:          recommendation.BeadID,
			BeadTitle:       recommendation.BeadTitle,
			Pane:            recommendation.PaneIndex,
			PaneTarget:      recommendation.PaneTarget,
			PaneID:          recommendation.PaneID,
			AgentType:       string(recommendation.AgentType),
			AgentName:       recommendation.AgentID,
			Status:          string(assignment.StatusAssigned),
			PromptSent:      false,
			AssignedAt:      assignedAt,
			Score:           recommendation.Score,
			Reasoning:       recommendation.Reason,
			ReasonCodes:     assignAllocationReasonStrings(recommendation.ReasonCodes),
			ScoreComponents: &components,
		})
	}
	return items
}

func assignAllocationReasonStrings(reasons []assign.AllocationReasonCode) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, string(reason))
	}
	return out
}

func assignAllocationView(plan *assign.AllocationPlan) *AssignAllocationView {
	if plan == nil {
		return nil
	}
	return &AssignAllocationView{
		SchemaVersion:   plan.SchemaVersion,
		Decision:        string(plan.Decision),
		PressureMissing: plan.Summary.PressureMissing,
		BVMissing:       plan.Summary.BVMissing,
		Warnings:        append([]string(nil), plan.Warnings...),
	}
}

func clampAssignScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// inferTaskTypeFromBead determines task type from bead metadata
func inferTaskTypeFromBead(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)
	rules := []struct {
		typ string
		kws []string
	}{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment"}},
		{"refactor", []string{"refactor", "cleanup", "improve"}},
		{"analysis", []string{"analyze", "investigate", "research"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}
	return "task"
}

// displayAssignOutputEnhanced renders the enhanced assignment output
func displayAssignOutputEnhanced(out *AssignOutputEnhanced, verbose bool) {
	th := theme.Current()

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Primary)
	subtitleStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	fmt.Println()
	fmt.Println(titleStyle.Render("Task Assignment Recommendations"))
	fmt.Println(strings.Repeat("━", 50))

	// Summary
	fmt.Println()
	fmt.Printf("Strategy: %s\n", out.Strategy)
	fmt.Printf("Idle Agents: %d | Actionable Beads: %d", out.Summary.IdleAgents, out.Summary.ActionableCount)
	if out.Summary.BlockedCount > 0 {
		fmt.Printf(" | Blocked: %d", out.Summary.BlockedCount)
	}
	fmt.Println()

	// Assignments
	if len(out.Assignments) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Recommended Assignments:"))
		fmt.Println()

		for _, item := range out.Assignments {
			agentStyle := getAgentStyle(item.AgentType, th)
			agentBadge := agentStyle.Render(fmt.Sprintf("[%s pane %d]", item.AgentType, item.Pane))
			confStr := fmt.Sprintf("(%.0f%% score)", item.Score*100)

			fmt.Printf("  %s → %s %s\n", agentBadge, item.BeadID, confStr)
			fmt.Printf("     %s\n", item.BeadTitle)
			if verbose && item.Reasoning != "" {
				fmt.Printf("     %s\n", subtitleStyle.Render(item.Reasoning))
			}
			fmt.Println()
		}
	} else {
		fmt.Println()
		fmt.Println(subtitleStyle.Render("No assignments to recommend."))
		if out.Summary.IdleAgents == 0 {
			fmt.Println(subtitleStyle.Render("  Reason: No idle agents available"))
		} else if out.Summary.TotalBeadCount == 0 {
			fmt.Println(subtitleStyle.Render("  Reason: No ready beads to assign"))
		}
	}

	// Blocked beads summary (always show if there are blocked beads)
	blockedCount := 0
	for _, s := range out.Skipped {
		if s.Reason == "blocked_by_dependency" {
			blockedCount++
		}
	}
	if blockedCount > 0 {
		fmt.Println()
		warnStyle := lipgloss.NewStyle().Foreground(th.Warning)
		fmt.Println(warnStyle.Render(fmt.Sprintf("Blocked by dependencies (%d):", blockedCount)))
		for _, s := range out.Skipped {
			if s.Reason == "blocked_by_dependency" {
				if len(s.BlockedByIDs) > 0 {
					fmt.Printf("  - %s (blocked by: %s)\n", s.BeadID, strings.Join(s.BlockedByIDs, ", "))
				} else {
					fmt.Printf("  - %s\n", s.BeadID)
				}
			}
		}
	}

	// Other skipped items (only in verbose mode)
	if verbose && len(out.Skipped) > blockedCount {
		fmt.Println()
		warnStyle := lipgloss.NewStyle().Foreground(th.Warning)
		fmt.Println(warnStyle.Render("Other skipped:"))
		for _, s := range out.Skipped {
			if s.Reason != "blocked_by_dependency" {
				fmt.Printf("  - %s: %s\n", s.BeadID, s.Reason)
			}
		}
	}
}

// handledBeadRecentWindow bounds how long a completed/failed assignment keeps
// suppressing re-dispatch of its bead. It mirrors the completion detector's
// dedup window order of magnitude but is wider, covering the gap between a
// completion firing and the plan side dropping the bead from "ready": a bead
// that just completed must not be re-dispatched within the same or next cycle
// (the double-dispatch race). Recently-completed beads older than this are no
// longer suppressed (a genuinely reopened bead should flow again).
const handledBeadRecentWindow = 90 * time.Second

func resolveAssignmentItemPane(panes []tmux.Pane, item AssignmentItem) (tmux.Pane, error) {
	selectors := make([]string, 0, 2)
	if paneID := strings.TrimSpace(item.PaneID); paneID != "" {
		selectors = append(selectors, paneID)
	}
	if target := strings.TrimSpace(item.PaneTarget); target != "" {
		selectors = append(selectors, target)
	}
	if len(selectors) > 0 {
		resolved, err := tmux.ResolvePaneSelectors(panes, selectors, true)
		if err != nil {
			return tmux.Pane{}, err
		}
		return resolved[0], nil
	}

	return tmux.Pane{}, &assignment.PaneIdentityMigrationError{BeadID: item.BeadID, Pane: item.Pane}
}

func resolvePendingAssignmentPane(panes []tmux.Pane, pending assignment.Assignment) (tmux.Pane, error) {
	physicalID, err := assignment.CanonicalPaneIdentity(&pending)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("pending assignment has no canonical physical pane ID: %w", err)
	}
	for _, pane := range panes {
		if pane.ID == physicalID {
			return pane, nil
		}
	}
	return tmux.Pane{}, fmt.Errorf("original physical pane %s is unavailable; refusing to transfer an atomic claim", physicalID)
}

func pendingCLIRecoveryIdentityError(projectDir, session string, pane tmux.Pane, pending assignment.Assignment, multiWindow bool) error {
	currentType := agentTypeForPane(pane)
	if currentType == "user" || currentType == "unknown" {
		return fmt.Errorf("pending atomic recovery target %s is not an agent pane (type: %s)", assignmentPaneTarget(pane), currentType)
	}
	if agent.AgentType(currentType).Canonical() != agent.AgentType(pending.AgentType).Canonical() {
		return fmt.Errorf("pending atomic recovery target %s changed agent type from %s to %s", assignmentPaneTarget(pane), pending.AgentType, currentType)
	}
	currentAgentName := assignmentAgentIdentityForPane(projectDir, session, currentType, pane, multiWindow)
	if strings.TrimSpace(pending.AgentName) != "" && currentAgentName != pending.AgentName {
		return fmt.Errorf("pending atomic recovery target %s changed Agent Mail identity from %s to %s", assignmentPaneTarget(pane), pending.AgentName, currentAgentName)
	}
	return nil
}

// executeAssignmentsEnhanced sends assignments to agents and tracks them
func executeAssignmentsEnhanced(ctx context.Context, session string, out *AssignOutputEnhanced, opts *AssignCommandOptions) error {
	if ctx == nil {
		return errors.New("assignment execution context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("assignment execution canceled: %w", err)
	}
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, session)
		if err != nil {
			return fmt.Errorf("resolve bead project for atomic claim: %w", err)
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		return err
	}
	if !opts.Quiet {
		fmt.Println()
		fmt.Println("Executing assignments...")
	}

	// Load the durable assignment ledger. Atomic assignment fails closed without
	// it because claim/lease/send recovery metadata must survive this process.
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return fmt.Errorf("load assignment store: %w", err)
	}

	// Set up file reservation manager if enabled
	var reservationMgr *assign.FileReservationManager
	if opts.ReserveFiles {
		amClient := newAgentMailClient(projectDir)
		if !amClient.IsAvailable() {
			return assignment.ErrReservationRequired
		}
		reservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		// Set TTL to 2x timeout, minimum 1 hour
		ttlSeconds := int(opts.Timeout.Seconds()) * 2
		if ttlSeconds < 3600 {
			ttlSeconds = 3600
		}
		reservationMgr.SetTTL(ttlSeconds)
		if !opts.Quiet && opts.Verbose {
			fmt.Println("  File reservation enabled via Agent Mail")
		}
	}
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, opts.Force)
	activeBeads := make(map[string]struct{})
	for _, active := range store.ListActive() {
		if active != nil && strings.TrimSpace(active.BeadID) != "" {
			activeBeads[active.BeadID] = struct{}{}
		}
	}

	var successCount, failCount, reservedCount int
	out.Summary.AssignedCount = 0

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}
	observer := newAssignSessionObserver()
	// FIX (d): iterate by index so PromptSent is written back into the caller's
	// slice. Callers (initial pass, ready-work scan) log/count "dispatched"
	// from out.Assignments; by persisting PromptSent only for items we actually
	// sent, those logs report SENT beads, not merely PLANNED ones.
	for i := range out.Assignments {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled before item %d: %w", i+1, err)
		}
		item := &out.Assignments[i]
		detailsCtx, detailsCancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		liveDetails, detailsErr := bv.GetBeadAssignmentDetailsContext(detailsCtx, projectDir, item.BeadID)
		detailsCancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while validating %s: %w", item.BeadID, err)
		}
		if detailsErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: live bead validation: %v", item.BeadID, detailsErr))
			failCount++
			continue
		}
		if skip := classifyLiveAssignmentDetailsForProject(projectDir, liveDetails, activeBeads); skip != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: live bead validation: %s", item.BeadID, skip.Reason))
			failCount++
			continue
		}
		item.rawBeadTitle = liveDetails.Title
		item.BeadTitle = forcedRedactedAssignmentText(liveDetails.Title)
		rawBeadTitle := liveDetails.Title

		// Build the prompt using template
		prompt := expandPromptTemplate(item.BeadID, rawBeadTitle, opts.Template, opts.TemplateFile)

		// Resolve the planner's stable pane identity against fresh topology.
		pn, resolveErr := resolveAssignmentItemPane(panes, *item)
		if resolveErr != nil {
			failure := fmt.Sprintf("%s: resolve pane: %v", item.BeadID, resolveErr)
			out.Errors = append(out.Errors, failure)
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, resolveErr)
			}
			failCount++
			continue
		}
		paneID := assignmentPaneStableKey(pn)
		item.Pane = pn.Index
		item.PaneTarget = assignmentPaneTarget(pn)
		item.PaneID = pn.ID
		observeCtx, observeCancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		observation, observeErr := observer.Observe(observeCtx, session)
		observeCancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while observing %s: %w", item.BeadID, err)
		}
		if observeErr != nil || pn.ID == "" ||
			!statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) ||
			!observation.SafeToDispatch(pn.ID) {
			if observeErr == nil {
				observeErr = fmt.Errorf("pane %s is not freshly and confidently idle", item.PaneTarget)
			}
			out.Errors = append(out.Errors, fmt.Sprintf("%s: observation gate: %v", item.BeadID, observeErr))
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, observeErr)
			}
			failCount++
			continue
		}

		// Stamp the per-pane NTM-Pane work-token instruction (#199). No-op when
		// the semantic feature is off, so the dispatched prompt is unchanged. Use
		// the same window.pane addressing --robot-is-working reports so the token
		// the agent commits matches what the reader greps for.
		promptForPane := stampMarchingOrders(prompt, session, pn.WindowIndex, pn.Index)

		actor := item.AgentName
		if strings.TrimSpace(actor) == "" {
			actor = fmt.Sprintf("%s-%s-pane-%d-%d", session, item.AgentType, pn.WindowIndex, pn.Index)
		}
		handoffAgent := item.AgentName
		if strings.TrimSpace(handoffAgent) == "" {
			handoffAgent = actor
		}
		idempotencyKey, keyErr := assignment.NewAssignmentIdempotencyKey()
		if keyErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: idempotency key: %v", item.BeadID, keyErr))
			if !opts.Quiet {
				fmt.Printf("  ✗ Failed to prepare assignment %s: %v\n", item.BeadID, keyErr)
			}
			failCount++
			continue
		}
		assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
		atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
			BeadID:                    item.BeadID,
			BeadTitle:                 rawBeadTitle,
			Target:                    paneID,
			OccupancyKey:              paneID,
			Pane:                      item.Pane,
			AgentType:                 item.AgentType,
			AgentName:                 handoffAgent,
			Actor:                     actor,
			Prompt:                    promptForPane,
			IdempotencyKey:            idempotencyKey,
			RequireReservation:        opts.ReserveFiles,
			AllowReservationDiscovery: opts.ReserveFiles,
			ReservationTTL:            time.Hour,
		})
		cancel()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("assignment execution canceled while assigning %s: %w", item.BeadID, err)
		}
		if assignErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: atomic assignment: %v", item.BeadID, assignErr))
			if !opts.Quiet {
				fmt.Printf("  Failed to assign %s to pane %s: %v\n", item.BeadID, item.PaneTarget, assignErr)
			}
			failCount++
			continue
		}
		if len(atomicResult.Lease.Granted) > 0 {
			reservedCount++
			if !opts.Quiet && opts.Verbose {
				fmt.Printf("    Reserved files: %v\n", atomicResult.Lease.Granted)
			}
		}
		if durable := atomicResult.Assignment; durable != nil && durable.BeadID == item.BeadID && durable.IdempotencyKey == idempotencyKey {
			item.BeadTitle = forcedRedactedAssignmentText(durable.BeadTitle)
			item.AgentType = durable.AgentType
			item.AgentName = durable.AgentName
			item.Status = string(durable.Status)
			item.AssignedAt = durable.AssignedAt.UTC().Format(time.RFC3339)
		}

		// FIX (d): only NOW is the prompt actually sent — persist the flag into
		// the caller's slice so dispatch logs/counts reflect SENT, not PLANNED.
		item.PromptSent = atomicResult.Sent
		activeBeads[item.BeadID] = struct{}{}
		successCount++
		out.Summary.AssignedCount = successCount

		// Best-effort secondary attribution: tag the bead with this pane's label
		// (gated + non-fatal; after delivery so it never blocks dispatch) (#199).
		if !atomicResult.Replayed {
			bestEffortStampBeadLabel(projectDir, item.BeadID, session, pn.WindowIndex, pn.Index)
		}

		if !opts.Quiet {
			fmt.Printf("  Assigned %s to pane %s (%s)\n", item.BeadID, item.PaneTarget, item.AgentType)
		}
	}

	if !opts.Quiet {
		fmt.Println()
		if failCount == 0 {
			fmt.Printf("✓ Successfully assigned %d beads\n", successCount)
		} else {
			fmt.Printf("Assigned %d beads (%d failed)\n", successCount, failCount)
		}
		if reservedCount > 0 {
			fmt.Printf("  File reservations: %d beads with reserved paths\n", reservedCount)
		}
		fmt.Println("Use 'ntm status --assignments' to monitor progress.")
	}
	if failCount > 0 {
		return fmt.Errorf("%d of %d assignments failed", failCount, successCount+failCount)
	}

	return nil
}

func newCLIAtomicAssignmentCoordinator(store *assignment.AssignmentStore, projectDir string, reservationMgr *assign.FileReservationManager, allowBusy ...bool) *assignment.AtomicCoordinator {
	operatorGatedLabels := bv.OperatorGatedLabelsForProject(projectDir)
	claimPort := assignment.ClaimFunc(func(ctx context.Context, beadID, actor string) (assignment.ClaimReceipt, error) {
		claim, err := claimBeadForAssignmentWithPolicy(ctx, projectDir, beadID, actor, operatorGatedLabels)
		if err != nil {
			switch {
			case errors.Is(err, bv.ErrBeadAssignmentIneligible):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
			case errors.Is(err, bv.ErrBeadAlreadyClaimed):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimConflict, err)
			}
			return assignment.ClaimReceipt{}, err
		}
		return assignment.ClaimReceipt{
			BeadID:    claim.ID,
			Actor:     claim.Actor,
			Status:    claim.Status,
			ClaimedAt: claim.ClaimedAt,
		}, nil
	})

	var reservationPort assignment.ReservationPort
	if reservationMgr != nil {
		reservationPort = &cliAtomicReservationPort{manager: reservationMgr}
	}

	redactionConfig := config.Default().Redaction.ToRedactionLibConfig()
	if cfg != nil {
		redactionConfig = cfg.Redaction.ToRedactionLibConfig()
	}
	bypassIdleGate := len(allowBusy) > 0 && allowBusy[0]
	dispatchPort := &cliAtomicPaneDispatchPort{
		session:         store.SessionName,
		redactionConfig: redactionConfig,
		observer:        newAssignSessionObserver(),
		bypassIdleGate:  bypassIdleGate,
	}
	readLiveDetails := func(detailsCtx context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		details, err := getBeadAssignmentDetailsForAssignment(detailsCtx, projectDir, beadID)
		if err != nil {
			return nil, err
		}
		if details == nil {
			return nil, errors.New("live work-item details are missing")
		}
		return details, nil
	}
	coordinator := assignment.NewAtomicCoordinator(store, claimPort, reservationPort, dispatchPort, dispatchPort).
		WithWorkItemStatusPort(assignment.WorkItemStatusFunc(func(statusCtx context.Context, beadID string) (string, error) {
			return getBeadStatusForAssignment(statusCtx, projectDir, beadID)
		})).
		WithAssignmentEligibilityAuthorizationPort(assignment.AssignmentEligibilityAuthorizationFunc(
			func(detailsCtx context.Context, request assignment.AssignmentEligibilityAuthorizationRequest) error {
				details, err := readLiveDetails(detailsCtx, request.BeadID)
				if err != nil {
					return err
				}
				return validateCLIAtomicAssignmentDetailsWithPolicy(details, request, operatorGatedLabels)
			},
		)).
		WithWorkingReplacementAuthorizationPort(assignment.WorkingReplacementAuthorizationFunc(
			func(statusCtx context.Context, beadID string) (assignment.WorkingReplacementAuthorization, error) {
				details, err := readLiveDetails(statusCtx, beadID)
				if err != nil {
					return assignment.WorkingReplacementAuthorization{}, err
				}
				if err := validateCLIAtomicAssignmentDetailsWithPolicy(details, assignment.AssignmentEligibilityAuthorizationRequest{
					BeadID: beadID, ClaimActor: details.Assignee, AllowOwnedInProgress: true,
				}, operatorGatedLabels); err != nil {
					return assignment.WorkingReplacementAuthorization{}, err
				}
				return assignment.WorkingReplacementAuthorization{Status: details.Status, Assignee: details.Assignee}, nil
			},
		))
	return coordinator.WithWorkingReplacementReleasePort(assignment.WorkingReplacementReleaseFunc(
		func(releaseCtx context.Context, current *assignment.Assignment) (assignment.WorkingReplacementReleaseReceipt, error) {
			released, err := releaseAssignmentLeases(releaseCtx, store.SessionName, current)
			return cliWorkingReplacementReleaseReceipt(current, released, err), err
		},
	))
}

func validateCLIAtomicAssignmentDetailsWithPolicy(details *bv.BeadAssignmentDetails, request assignment.AssignmentEligibilityAuthorizationRequest, operatorGatedLabels []string) error {
	err := bv.ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, bv.BeadAssignmentAuthorization{
		BeadID: request.BeadID, ExpectedAssignee: request.ClaimActor,
		AllowUnassignedOpen:  request.AllowUnassignedOpen,
		AllowOwnedOpen:       request.AllowOwnedOpen,
		AllowOwnedInProgress: request.AllowOwnedInProgress,
	}, operatorGatedLabels)
	if errors.Is(err, bv.ErrBeadAssignmentIneligible) {
		return fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
	}
	return err
}

func cliWorkingReplacementReleaseReceipt(current *assignment.Assignment, released []string, releaseErr error) assignment.WorkingReplacementReleaseReceipt {
	receipt := assignment.WorkingReplacementReleaseReceipt{ReleasedPaths: normalizedAssignmentReleasePaths(released)}
	if releaseErr != nil || current == nil {
		return receipt
	}
	receipt.ReleasedPaths = normalizedAssignmentReleasePaths(
		append(append(append([]string(nil), current.ReservedPaths...), current.ReservationRequested...), current.ReservationInputPaths...),
	)
	seenIDs := make(map[int]struct{}, len(current.ReservationIDs))
	for _, id := range current.ReservationIDs {
		if id <= 0 {
			continue
		}
		if _, duplicate := seenIDs[id]; duplicate {
			continue
		}
		seenIDs[id] = struct{}{}
		receipt.ReleasedReservationIDs = append(receipt.ReleasedReservationIDs, id)
	}
	return receipt
}

func normalizedAssignmentReleasePaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}

type cliAtomicReservationPort struct {
	manager *assign.FileReservationManager
}

func (p *cliAtomicReservationPort) Reserve(ctx context.Context, req assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	if p == nil || p.manager == nil {
		return assignment.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}, assignment.GuaranteeNoReservation(errors.New("file reservation manager is not configured"))
	}
	var result *assign.FileReservationResult
	var err error
	if len(req.RequestedPaths) > 0 {
		result, err = p.manager.ReservePathsForBead(ctx, req.BeadID, req.AgentName, req.RequestedPaths)
	} else {
		result, err = p.manager.ReserveForBead(ctx, req.BeadID, req.BeadTitle, "", req.AgentName)
	}
	return classifyCLIReservationAttempt(req, result, err)
}

func (p *cliAtomicReservationPort) ReconcileReservation(ctx context.Context, req assignment.ReservationRequest, _ assignment.LeaseReceipt) (assignment.ReservationReconciliation, error) {
	requested := append([]string(nil), req.RequestedPaths...)
	if len(requested) == 0 {
		requested = assign.ExtractFilePaths(req.BeadTitle, "")
	}
	if p == nil || p.manager == nil {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationUnknown}, errors.New("file reservation manager is not configured")
	}
	result, err := p.manager.ReconcileForBead(ctx, req.BeadID, req.AgentName, requested)
	lease, _ := classifyCLIReservationAttempt(req, result, err)
	if err != nil {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationUnknown, Lease: lease}, err
	}
	if len(lease.ReservationIDs) == 0 && len(lease.Granted) == 0 {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationAbsent, Lease: lease}, nil
	}
	// Any durable handle proves a lease exists. The atomic coordinator validates
	// completeness and persists partial handles as release-required state.
	return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationReserved, Lease: lease}, nil
}

func classifyCLIReservationAttempt(req assignment.ReservationRequest, result *assign.FileReservationResult, reservationErr error) (assignment.LeaseReceipt, error) {
	lease := assignment.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}
	var knownFailure error
	if result != nil {
		lease.Requested = append([]string(nil), result.RequestedPaths...)
		lease.Granted = append([]string(nil), result.GrantedPaths...)
		lease.ReservationIDs = append([]int(nil), result.ReservationIDs...)
		if result.ExpiresAt != nil {
			expiresAt := *result.ExpiresAt
			lease.ExpiresAt = &expiresAt
		}
		if len(result.Conflicts) > 0 {
			paths := make([]string, 0, len(result.Conflicts))
			for _, conflict := range result.Conflicts {
				paths = append(paths, conflict.Path)
			}
			knownFailure = fmt.Errorf("file reservation conflicts: %s", strings.Join(paths, ", "))
		} else if !result.Success && reservationErr == nil {
			message := strings.TrimSpace(result.Error)
			if message == "" {
				message = "file reservation was not granted"
			}
			knownFailure = errors.New(message)
		}
	}
	hasHandles := len(lease.ReservationIDs) > 0 || len(lease.Granted) > 0
	if knownFailure != nil {
		if hasHandles {
			return lease, assignment.RequireReservationRelease(knownFailure)
		}
		return lease, assignment.GuaranteeNoReservation(knownFailure)
	}
	if reservationErr != nil && hasHandles {
		return lease, assignment.RequireReservationRelease(reservationErr)
	}
	return lease, reservationErr
}

type cliAtomicPaneDispatchPort struct {
	session         string
	redactionConfig redaction.Config
	observer        assignSessionObserver
	bypassIdleGate  bool
	listPanes       func(context.Context, string) ([]tmux.Pane, error)
	deliverer       dispatchsvc.Deliverer
}

func (p *cliAtomicPaneDispatchPort) prepare(ctx context.Context, req assignment.DispatchRequest) (*dispatchsvc.Service, *dispatchsvc.Prepared, error) {
	listPanes := p.listPanes
	if listPanes == nil {
		listPanes = tmux.GetPanesContext
	}
	panes, err := listPanes(ctx, p.session)
	if err != nil {
		return nil, nil, fmt.Errorf("load dispatch topology: %w", err)
	}
	deliverer := p.deliverer
	if deliverer == nil {
		deliverer = dispatchsvc.TMUXDeliverer{}
	}
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor: shellFinalMessageRedactor(p.redactionConfig), Protocols: shellDispatchProtocolPlanner{},
		Deliverer: deliverer,
	})
	if err != nil {
		return nil, nil, err
	}
	prepared, err := service.Prepare(ctx, dispatchsvc.Request{
		Session: p.session, Panes: panes, Selectors: []string{req.Target}, RequireSingleSelector: true,
		IncludeUser: true, Message: req.Prompt, Submit: true, StopOnFailure: true,
	})
	if err != nil {
		return nil, prepared, err
	}
	return service, prepared, nil
}

func (p *cliAtomicPaneDispatchPort) Preflight(ctx context.Context, req assignment.DispatchRequest) (assignment.PromptPreflightResult, error) {
	titleResult := redaction.ScanAndRedact(req.BeadTitle, p.redactionConfig)
	if titleResult.Blocked {
		return assignment.PromptPreflightResult{}, &dispatchsvc.Error{
			Code: dispatchsvc.ErrRedactionBlocked,
			Err:  fmt.Errorf("assignment title blocked by redaction policy (%d findings)", len(titleResult.Findings)),
		}
	}
	for _, requestedPath := range req.RequestedPaths {
		pathResult := redaction.ScanAndRedact(requestedPath, p.redactionConfig)
		if len(pathResult.Findings) > 0 {
			return assignment.PromptPreflightResult{}, &dispatchsvc.Error{
				Code: dispatchsvc.ErrRedactionBlocked,
				Err:  fmt.Errorf("assignment reservation path blocked by redaction policy (%d findings)", len(pathResult.Findings)),
			}
		}
	}
	_, prepared, err := p.prepare(ctx, req)
	if err != nil {
		return assignment.PromptPreflightResult{}, err
	}
	dispatchPrompt, err := prepared.FinalMessageForSingleTarget()
	if err != nil {
		return assignment.PromptPreflightResult{}, err
	}
	durableConfig := p.redactionConfig.DeepCopy()
	durableConfig.Mode = redaction.ModeRedact
	durablePrompt := redaction.ScanAndRedact(req.Prompt, durableConfig).Output
	durableTitle := redaction.ScanAndRedact(req.BeadTitle, durableConfig).Output
	return assignment.PromptPreflightResult{DispatchPrompt: dispatchPrompt, DurablePrompt: durablePrompt, DurableTitle: durableTitle}, nil
}

func (p *cliAtomicPaneDispatchPort) Dispatch(ctx context.Context, req assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
	started := time.Now()
	if !p.bypassIdleGate {
		observer := p.observer
		if observer == nil {
			observer = newAssignSessionObserver()
		}
		observation, observeErr := observer.Observe(ctx, p.session)
		if observeErr != nil {
			return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(
				fmt.Errorf("fresh dispatch observation for pane %s: %w", req.Target, observeErr),
			)
		}
		if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) || !observation.SafeToDispatch(req.Target) {
			return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(
				fmt.Errorf("pane %s is not freshly and confidently idle at dispatch", req.Target),
			)
		}
	}
	service, prepared, prepareErr := p.prepare(ctx, req)
	if prepareErr != nil {
		return assignment.DispatchReceipt{Duration: time.Since(started)}, assignment.GuaranteeNoActuation(prepareErr)
	}
	result, dispatchErr := service.Dispatch(ctx, prepared)
	receipt := assignment.DispatchReceipt{Duration: time.Since(started)}
	if dispatchErr != nil {
		if dispatchsvc.IsGuaranteedNoDeliveryActuation(dispatchErr) {
			return receipt, assignment.GuaranteeNoActuation(dispatchErr)
		}
		return receipt, dispatchErr
	}
	delivery, err := validateSinglePaneDispatchResult(result, req.Target)
	if err != nil {
		return receipt, err
	}
	receipt.DeliveryID = assignment.DispatchDeliveryID(delivery.Target.Ref.StableKey(), string(delivery.Protocol), req.IdempotencyKey)
	return receipt, nil
}

func validateSinglePaneDispatchResult(result dispatchsvc.Result, requestedTarget string) (dispatchsvc.Receipt, error) {
	if result.Delivered != 1 || len(result.Receipts) != 1 || result.Receipts[0].Status != dispatchsvc.ReceiptDelivered {
		return dispatchsvc.Receipt{}, fmt.Errorf("dispatch delivered %d panes, want 1", result.Delivered)
	}
	delivery := result.Receipts[0]
	actualTarget := strings.TrimSpace(delivery.Target.Ref.StableKey())
	if actualTarget != strings.TrimSpace(requestedTarget) {
		// A delivered receipt proves actuation happened, but not on the pane this
		// assignment owns. Leave dispatch in the durable unknown state.
		return dispatchsvc.Receipt{}, fmt.Errorf("dispatch receipt target %s does not match requested pane %s", actualTarget, requestedTarget)
	}
	return delivery, nil
}

func promptTemplateSource(templateName, templateFile string) string {
	var template string

	switch strings.ToLower(templateName) {
	case "impl":
		template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	case "review":
		template = "Review and verify bead {BEAD_ID}: {TITLE}. Run tests if applicable."
	case "custom":
		if templateFile != "" {
			data, err := os.ReadFile(templateFile)
			if err == nil {
				template = string(data)
			} else {
				// Fall back to impl
				template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first."
			}
		} else {
			template = "Work on bead {BEAD_ID}: {TITLE}."
		}
	default:
		template = "Work on bead {BEAD_ID}: {TITLE}. Check dependencies first with `br dep tree {BEAD_ID}`."
	}
	return template
}

func renderPromptTemplate(beadID, title, template string) string {
	result := template
	result = strings.ReplaceAll(result, "{BEAD_ID}", beadID)
	result = strings.ReplaceAll(result, "{TITLE}", title)
	return result
}

// expandPromptTemplate expands a prompt template with bead variables.
func expandPromptTemplate(beadID, title, templateName, templateFile string) string {
	return renderPromptTemplate(beadID, title, promptTemplateSource(templateName, templateFile))
}

// ============================================================================
// Clear Assignments - --clear and --clear-pane functionality
// ============================================================================

const (
	clearErrNotAssigned       = "NOT_ASSIGNED"
	clearErrAlreadyCompleted  = "ALREADY_COMPLETED"
	clearErrPaneNotFound      = "PANE_NOT_FOUND"
	clearErrInvalidFlag       = "INVALID_FLAG"
	clearErrInternal          = "INTERNAL_ERROR"
	clearErrCompletionPending = "COMPLETION_EVENT_PENDING"
)

func clearAssignmentFailureCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return robot.ErrCodeTimeout
	case errors.Is(err, assignment.ErrCompletionEventPending):
		return clearErrCompletionPending
	default:
		return ""
	}
}

// ClearAssignmentResult represents the result of clearing a single assignment.
type ClearAssignmentResult struct {
	BeadID                   string   `json:"bead_id"`
	BeadTitle                string   `json:"bead_title,omitempty"`
	PreviousPane             int      `json:"previous_pane,omitempty"`
	PreviousAgent            string   `json:"previous_agent,omitempty"`
	PreviousAgentType        string   `json:"previous_agent_type,omitempty"`
	PreviousStatus           string   `json:"previous_status,omitempty"`
	AssignmentFound          bool     `json:"assignment_found"`
	FileReservationsReleased bool     `json:"file_reservations_released"`
	FilesReleased            []string `json:"files_released,omitempty"`
	Success                  bool     `json:"success"`
	Error                    string   `json:"error,omitempty"`
	ErrorCode                string   `json:"error_code,omitempty"`
}

// ClearAllResult represents result of clearing all assignments for a pane.
type ClearAllResult struct {
	Pane         int                     `json:"pane"`
	AgentType    string                  `json:"agent_type"`
	Success      bool                    `json:"success"`
	Error        string                  `json:"error,omitempty"`
	ClearedBeads []ClearAssignmentResult `json:"cleared_beads"`
}

// ClearAssignmentsSummary provides a summary of a clear operation.
type ClearAssignmentsSummary struct {
	ClearedCount         int `json:"cleared_count"`
	ReservationsReleased int `json:"reservations_released"`
	FailedCount          int `json:"failed_count,omitempty"`
}

// ClearAssignmentsData is the data payload for clear operations.
type ClearAssignmentsData struct {
	Cleared   []ClearAssignmentResult `json:"cleared"`
	Summary   ClearAssignmentsSummary `json:"summary"`
	Pane      *int                    `json:"pane,omitempty"`
	AgentType string                  `json:"agent_type,omitempty"`
}

// ClearAssignmentsError represents an error in the clear envelope.
type ClearAssignmentsError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ClearAssignmentsEnvelope is the standard JSON envelope for clear operations.
type ClearAssignmentsEnvelope struct {
	Command    string                 `json:"command"`
	Subcommand string                 `json:"subcommand"`
	Session    string                 `json:"session"`
	Timestamp  string                 `json:"timestamp"`
	Success    bool                   `json:"success"`
	Data       *ClearAssignmentsData  `json:"data,omitempty"`
	Warnings   []string               `json:"warnings"`
	Error      *ClearAssignmentsError `json:"error,omitempty"`
}

// ReassignData holds the successful reassignment data.
type ReassignData struct {
	BeadID                       string `json:"bead_id"`
	BeadTitle                    string `json:"bead_title"`
	Pane                         int    `json:"pane"`
	AgentType                    string `json:"agent_type"`
	AgentName                    string `json:"agent_name,omitempty"`
	Status                       string `json:"status"`
	PromptSent                   bool   `json:"prompt_sent"`
	AssignedAt                   string `json:"assigned_at"`
	PreviousPane                 int    `json:"previous_pane"`
	PreviousAgent                string `json:"previous_agent,omitempty"`
	PreviousAgentType            string `json:"previous_agent_type"`
	PreviousStatus               string `json:"previous_status"`
	FileReservationsTransferred  bool   `json:"file_reservations_transferred"`
	FileReservationsReleasedFrom int    `json:"file_reservations_released_from,omitempty"`
	FileReservationsCreatedFor   int    `json:"file_reservations_created_for,omitempty"`
}

// ReassignError represents an error in the reassignment envelope.
type ReassignError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ReassignEnvelope is the standard JSON envelope for reassignment operations.
type ReassignEnvelope struct {
	Command    string         `json:"command"`
	Subcommand string         `json:"subcommand"`
	Session    string         `json:"session"`
	Timestamp  string         `json:"timestamp"`
	Success    bool           `json:"success"`
	Data       *ReassignData  `json:"data,omitempty"`
	Warnings   []string       `json:"warnings"`
	Error      *ReassignError `json:"error,omitempty"`
}

// RetryItem holds data for a single retried assignment.
type RetryItem struct {
	BeadID             string `json:"bead_id"`
	BeadTitle          string `json:"bead_title"`
	Pane               int    `json:"pane"`
	AgentType          string `json:"agent_type"`
	AgentName          string `json:"agent_name,omitempty"`
	Status             string `json:"status"`
	PromptSent         bool   `json:"prompt_sent"`
	AssignedAt         string `json:"assigned_at"`
	PreviousPane       int    `json:"previous_pane"`
	PreviousAgent      string `json:"previous_agent,omitempty"`
	PreviousFailReason string `json:"previous_fail_reason,omitempty"`
	RetryCount         int    `json:"retry_count"`
}

// RetrySkippedItem holds data for a skipped retry.
type RetrySkippedItem struct {
	BeadID string `json:"bead_id"`
	Reason string `json:"reason"`
}

// RetrySummary provides summary statistics for retry operations.
type RetrySummary struct {
	TotalFailed  int `json:"total_failed"`
	RetriedCount int `json:"retried_count"`
	SkippedCount int `json:"skipped_count"`
}

// RetryData holds the data payload for retry operations.
type RetryData struct {
	Retried []RetryItem        `json:"retried"`
	Skipped []RetrySkippedItem `json:"skipped"`
	Summary RetrySummary       `json:"summary"`
}

// RetryError represents an error in the retry envelope.
var releaseReservations = releaseFileReservations
var releaseAssignmentLeases = releaseAssignmentReservationsForClear
var getClearPanePanes = tmux.GetPanesContext

// makeRetryEnvelope creates a standard RetryEnvelope for JSON output.
func makeRetryEnvelope(session string, success bool, data *RetryData, errCode, errMsg string, warnings []string) AssignEnvelope[RetryData] {
	envelope := AssignEnvelope[RetryData]{
		Command:    "assign",
		Subcommand: "retry",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    success,
		Data:       data,
		Warnings:   warnings,
	}
	if warnings == nil {
		envelope.Warnings = []string{}
	}
	if errCode != "" {
		envelope.Error = &AssignError{
			Code:    errCode,
			Message: errMsg,
		}
	}
	return envelope
}

func emitRetryFailure(session, defaultCode string, err error) error {
	if IsJSONOutput() {
		code := defaultCode
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		return errors.Join(
			emitJSONFailureEnvelope(makeRetryEnvelope(session, false, nil, code, err.Error(), nil)),
			err,
		)
	}
	return err
}

// runRetryAssignments handles --retry and --retry-failed operations.
func runRetryAssignments(ctx context.Context, session string) error {
	if ctx == nil {
		return emitRetryFailure(session, robot.ErrCodeInternalError, errors.New("assignment retry context is required"))
	}
	if err := ctx.Err(); err != nil {
		return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled: %w", err))
	}
	// Load assignment store
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, nil, "STORE_LOAD_ERROR",
				fmt.Sprintf("failed to load assignment store: %v", err), nil,
			))
		}
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	// Get all assignments including completed/failed
	allAssignments := store.GetAll()
	if len(allAssignments) == 0 {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, nil, "NO_ASSIGNMENTS", "no assignments found", nil,
			))
		}
		return fmt.Errorf("no assignments found")
	}

	// Filter to failed assignments plus known-unsent atomic claims. The latter
	// are recoverable with their original idempotency key and claim actor.
	var failedAssignments []assignment.Assignment
	for _, a := range allAssignments {
		if a.Status == assignment.StatusFailed ||
			(a.Status == assignment.StatusClaimed && a.DispatchState == assignment.DispatchPending) {
			failedAssignments = append(failedAssignments, a)
		}
	}

	// If --retry specified, filter to just that bead
	if assignRetry != "" {
		var found *assignment.Assignment
		for i := range failedAssignments {
			if failedAssignments[i].BeadID == assignRetry {
				found = &failedAssignments[i]
				break
			}
		}
		if found == nil {
			// Check if it exists but isn't failed
			for _, a := range allAssignments {
				if a.BeadID == assignRetry {
					if IsJSONOutput() {
						return emitJSONFailureEnvelope(makeRetryEnvelope(
							session, false, nil, "NOT_FAILED",
							fmt.Sprintf("bead %s is not in a retryable state (status: %s)", assignRetry, a.Status), nil,
						))
					}
					return fmt.Errorf("bead %s is not in a retryable state (status: %s)", assignRetry, a.Status)
				}
			}
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeRetryEnvelope(
					session, false, nil, "NOT_FOUND",
					fmt.Sprintf("bead %s not found in assignments", assignRetry), nil,
				))
			}
			return fmt.Errorf("bead %s not found in assignments", assignRetry)
		}
		failedAssignments = []assignment.Assignment{*found}
	}

	if len(failedAssignments) == 0 {
		if IsJSONOutput() {
			return json.NewEncoder(os.Stdout).Encode(makeRetryEnvelope(
				session, true, &RetryData{
					Summary: RetrySummary{TotalFailed: 0, RetriedCount: 0, SkippedCount: 0},
					Retried: []RetryItem{},
					Skipped: []RetrySkippedItem{},
				}, "", "", nil,
			))
		}
		fmt.Println("No failed assignments to retry")
		return nil
	}

	// Get all panes for --to-pane
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return emitRetryFailure(session, "PANE_ERROR", fmt.Errorf("failed to get panes: %w", err))
	}
	projectDir, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return emitRetryFailure(session, "PROJECT_ERROR", fmt.Errorf("resolve bead project for atomic retry: %w", err))
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	// Pending atomic retries are already bound to a physical pane, and an
	// explicit --to-pane selects one directly. Observe the whole idle pool only
	// when an ordinary failed assignment actually needs automatic selection.
	needsIdleAgent := false
	for _, failed := range failedAssignments {
		recoverAtomic := failed.Status == assignment.StatusClaimed &&
			failed.DispatchState == assignment.DispatchPending &&
			failed.IdempotencyKey != "" && failed.ClaimActor != "" && failed.PendingPrompt != ""
		if !recoverAtomic && strings.TrimSpace(assignToPane) == "" {
			needsIdleAgent = true
			break
		}
	}
	var idleAgents []assignAgentInfo
	if needsIdleAgent {
		idleAgents, err = getIdleAgents(ctx, session, assignToType, assignVerbose)
		if err != nil {
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeRetryEnvelope(
					session, false, nil, "AGENT_ERROR",
					fmt.Sprintf("failed to get idle agents: %v", err), nil,
				))
			}
			return fmt.Errorf("failed to get idle agents: %w", err)
		}
		idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, assignTemplate)
		if err != nil {
			return emitRetryFailure(session, "PERSONA_ERROR", err)
		}
	}

	// Process each failed assignment
	retriedItems := make([]RetryItem, 0, len(failedAssignments))
	skippedItems := make([]RetrySkippedItem, 0, len(failedAssignments))
	var warnings []string

	for _, failed := range failedAssignments {
		if err := ctx.Err(); err != nil {
			return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled before %s: %w", failed.BeadID, err))
		}
		// Find a target pane
		var targetPane *tmux.Pane
		var targetAgentType string
		recoverAtomic := failed.Status == assignment.StatusClaimed &&
			failed.DispatchState == assignment.DispatchPending &&
			failed.IdempotencyKey != "" && failed.ClaimActor != "" && failed.PendingPrompt != ""

		if recoverAtomic {
			resolvedPane, resolveErr := resolvePendingAssignmentPane(panes, failed)
			if resolveErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: resolveErr.Error(),
				})
				continue
			}
			if identityErr := pendingCLIRecoveryIdentityError(projectDir, session, resolvedPane, failed, multiWindow); identityErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: identityErr.Error(),
				})
				continue
			}
			if selector := strings.TrimSpace(assignToPane); selector != "" {
				requestedPane, _, selectorErr := resolveDirectAssignmentPane(panes, selector)
				if selectorErr != nil {
					skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: selectorErr.Error()})
					continue
				}
				if requestedPane.ID != resolvedPane.ID {
					skippedItems = append(skippedItems, RetrySkippedItem{
						BeadID: failed.BeadID,
						Reason: fmt.Sprintf("pending atomic recovery is bound to pane %s and cannot retarget to %s", assignmentPaneTarget(resolvedPane), assignmentPaneTarget(requestedPane)),
					})
					continue
				}
			}
			targetPane = &resolvedPane
			targetAgentType = failed.AgentType
		} else if selector := strings.TrimSpace(assignToPane); selector != "" {
			// Specific physical pane requested.
			resolvedPane, _, resolveErr := resolveDirectAssignmentPane(panes, selector)
			if resolveErr != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: resolveErr.Error(),
				})
				continue
			}
			targetPane = &resolvedPane
			targetAgentType = agentTypeForPane(resolvedPane)
		} else if assignToType != "" {
			// Specific agent type requested - find idle agent of that type
			for i := range idleAgents {
				if idleAgents[i].agentType == assignToType {
					targetPane = &idleAgents[i].pane
					targetAgentType = assignToType
					// Remove from idle list
					idleAgents = append(idleAgents[:i], idleAgents[i+1:]...)
					break
				}
			}
			if targetPane == nil {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: fmt.Sprintf("no idle agent of type %s", assignToType),
				})
				continue
			}
		} else {
			// Use first available idle agent
			if len(idleAgents) == 0 {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: "no idle agents available",
				})
				continue
			}
			targetPane = &idleAgents[0].pane
			targetAgentType = idleAgents[0].agentType
			idleAgents = idleAgents[1:]
		}
		if targetAgentType == "" {
			targetAgentType = agentTypeForPane(*targetPane)
		}
		if targetAgentType == "user" || targetAgentType == "unknown" {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: fmt.Sprintf("pane %s is not an agent pane (type: %s)", assignmentPaneTarget(*targetPane), targetAgentType),
			})
			continue
		}

		// Check if target pane already has an active assignment.
		existingAssignment, identityErr := findAssignmentForPhysicalPane(store, *targetPane)
		if identityErr != nil {
			wrapped := fmt.Errorf("resolve active assignment for pane %s: %w", targetPane.ID, identityErr)
			data := &RetryData{
				Summary: RetrySummary{TotalFailed: len(failedAssignments), RetriedCount: len(retriedItems), SkippedCount: len(skippedItems)},
				Retried: retriedItems,
				Skipped: skippedItems,
			}
			return emitTypedAssignmentFailure(wrapped, makeRetryEnvelope(session, false, data, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), warnings))
		}
		if existingAssignment != nil && existingAssignment.BeadID != failed.BeadID {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: fmt.Sprintf("pane %d already has assignment %s", targetPane.Index, existingAssignment.BeadID),
			})
			continue
		}

		// Get bead title if not stored
		beadTitle := failed.BeadTitle
		if beadTitle == "" && !recoverAtomic {
			beadTitle, err = getBeadTitle(ctx, projectDir, failed.BeadID)
			if err != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: fmt.Sprintf("look up bead title: %v", err)})
				continue
			}
		}

		// Recover known-unsent work with the original actor, key, and target.
		// A new attempt receives a fresh intent and must win the atomic claim.
		newAgentName := assignmentAgentIdentityForPane(projectDir, session, targetAgentType, *targetPane, multiWindow)
		prompt := expandPromptTemplate(failed.BeadID, beadTitle, assignTemplate, assignTemplateFile)
		actor := newAgentName
		idempotencyKey := ""
		if recoverAtomic {
			newAgentName = failed.AgentName
			actor = failed.ClaimActor
			idempotencyKey = failed.IdempotencyKey
			prompt = failed.PendingPrompt
		} else {
			idempotencyKey, err = assignment.NewAssignmentIdempotencyKey()
			if err != nil {
				skippedItems = append(skippedItems, RetrySkippedItem{BeadID: failed.BeadID, Reason: err.Error()})
				continue
			}
		}
		reservationRequired := assignReserveFiles
		reservationDiscovery := assignReserveFiles
		if recoverAtomic {
			reservationRequired = failed.ReservationRequired
			reservationDiscovery = failed.ReservationDiscovery
		}
		reservationNeedsRefresh := !failed.ReservationCompleted ||
			(failed.ReservationExpiresAt != nil && !failed.ReservationExpiresAt.After(time.Now().UTC()))
		var retryReservationMgr *assign.FileReservationManager
		if reservationRequired && reservationNeedsRefresh {
			amClient := newAgentMailClient(projectDir)
			if !amClient.IsAvailable() {
				skippedItems = append(skippedItems, RetrySkippedItem{
					BeadID: failed.BeadID,
					Reason: "Agent Mail is unavailable; refusing to bypass the pending file reservation",
				})
				continue
			}
			retryReservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		}
		atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, retryReservationMgr)
		assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
		recoveredIntentSHA256 := ""
		if recoverAtomic {
			recoveredIntentSHA256 = failed.IntentSHA256
			if recoveredIntentSHA256 == "" {
				recoveredIntentSHA256 = failed.PromptSHA256
			}
		}
		atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
			BeadID:                    failed.BeadID,
			BeadTitle:                 beadTitle,
			Target:                    targetPane.ID,
			OccupancyKey:              targetPane.ID,
			Pane:                      targetPane.Index,
			AgentType:                 targetAgentType,
			AgentName:                 newAgentName,
			Actor:                     actor,
			Prompt:                    prompt,
			IdempotencyKey:            idempotencyKey,
			RecoveredIntentSHA256:     recoveredIntentSHA256,
			RequireReservation:        reservationRequired,
			AllowReservationDiscovery: reservationDiscovery,
			ReservationTTL:            time.Hour,
		})
		cancel()
		if err := ctx.Err(); err != nil {
			return emitRetryFailure(session, robot.ErrCodeTimeout, fmt.Errorf("assignment retry canceled while assigning %s: %w", failed.BeadID, err))
		}
		if assignErr != nil {
			skippedItems = append(skippedItems, RetrySkippedItem{
				BeadID: failed.BeadID,
				Reason: assignErr.Error(),
			})
			continue
		}
		promptSent := atomicResult.Sent

		retryCount := failed.RetryCount + 1
		update := assignment.AssignmentUpdate{
			RetryCount: &retryCount,
		}
		if err := store.Update(failed.BeadID, update); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to update assignment metadata for %s: %v", failed.BeadID, err))
		}

		now := time.Now().UTC()
		retriedItems = append(retriedItems, RetryItem{
			BeadID:             failed.BeadID,
			BeadTitle:          forcedRedactedAssignmentText(beadTitle),
			Pane:               targetPane.Index,
			AgentType:          targetAgentType,
			AgentName:          newAgentName,
			Status:             string(assignment.StatusAssigned),
			PromptSent:         promptSent,
			AssignedAt:         now.Format(time.RFC3339),
			PreviousPane:       failed.Pane,
			PreviousAgent:      failed.AgentName,
			PreviousFailReason: failed.FailReason,
			RetryCount:         failed.RetryCount + 1,
		})
	}

	// Save any changes
	if err := store.Save(); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to save assignment store: %v", err))
	}
	retryErrCode, retryErr := classifyRetryOutcome(retriedItems, skippedItems)

	// Output results
	if IsJSONOutput() {
		data := &RetryData{
			Summary: RetrySummary{
				TotalFailed:  len(failedAssignments),
				RetriedCount: len(retriedItems),
				SkippedCount: len(skippedItems),
			},
			Retried: retriedItems,
			Skipped: skippedItems,
		}
		if retryErr != nil {
			return emitJSONFailureEnvelope(makeRetryEnvelope(
				session, false, data, retryErrCode, retryErr.Error(), warnings,
			))
		}
		return json.NewEncoder(os.Stdout).Encode(makeRetryEnvelope(
			session, true, data, "", "", warnings,
		))
	}

	// Text output
	if !assignQuiet {
		fmt.Printf("Retry summary: %d failed, %d retried, %d skipped\n",
			len(failedAssignments), len(retriedItems), len(skippedItems))
		for _, item := range retriedItems {
			fmt.Printf("  Retried %s: pane %d → pane %d (%s)\n",
				item.BeadID, item.PreviousPane, item.Pane, item.AgentType)
		}
		for _, item := range skippedItems {
			fmt.Printf("  Skipped %s: %s\n", item.BeadID, item.Reason)
		}
		for _, w := range warnings {
			fmt.Printf("  Warning: %s\n", w)
		}
	}
	if retryErr != nil {
		return retryErr
	}

	return nil
}

func classifyRetryOutcome(retried []RetryItem, skipped []RetrySkippedItem) (string, error) {
	if len(skipped) == 0 {
		return "", nil
	}
	if len(retried) == 0 {
		if len(skipped) == 1 {
			return "RETRY_SKIPPED", fmt.Errorf("retry %s was skipped: %s", skipped[0].BeadID, skipped[0].Reason)
		}
		return "RETRY_SKIPPED", fmt.Errorf("all %d retryable assignments were skipped", len(skipped))
	}
	return "RETRY_PARTIAL", fmt.Errorf("%d of %d retryable assignments were skipped", len(skipped), len(retried)+len(skipped))
}

// runClearAssignments handles exactly one clear operation.
func runClearAssignments(cmd *cobra.Command, session string) error {
	makeClearErrorEnvelope := func(code, msg string) ClearAssignmentsEnvelope {
		return ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    code,
				Message: msg,
			},
		}
	}

	operationCount := 0
	if strings.TrimSpace(assignClear) != "" {
		operationCount++
	}
	if strings.TrimSpace(assignClearPane) != "" {
		operationCount++
	}
	if assignClearFailed {
		operationCount++
	}
	if operationCount > 1 {
		err := fmt.Errorf("use only one of --clear, --clear-pane, or --clear-failed")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearErrorEnvelope("INVALID_ARGS", err.Error()))
		}
		return err
	}

	if assignClear != "" {
		return runClearSpecificBeads(cmd, session, assignClear)
	}

	if strings.TrimSpace(assignClearPane) != "" {
		return runClearPaneAssignments(cmd, session, assignClearPane)
	}

	if assignClearFailed {
		return runClearFailedAssignments(cmd, session)
	}

	err := fmt.Errorf("no clear operation specified")
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(makeClearErrorEnvelope("INVALID_ARGS", err.Error()))
	}
	return err
}

func clearStoredAssignment(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment) ([]string, error) {
	return clearStoredAssignmentIfStatus(ctx, store, session, current)
}

func clearStoredAssignmentIfStatus(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment, expected ...assignment.AssignmentStatus) ([]string, error) {
	if current == nil {
		return nil, errors.New("assignment is required")
	}
	if store == nil {
		return nil, errors.New("assignment store is required")
	}
	cleanupUnlock, err := store.AcquireExternalCleanupLock(ctx, current.BeadID)
	if err != nil {
		return nil, fmt.Errorf("lock assignment external cleanup %s: %w", current.BeadID, err)
	}
	defer cleanupUnlock()
	if err := store.LoadStrict(); err != nil {
		return nil, fmt.Errorf("refresh assignment external cleanup %s: %w", current.BeadID, err)
	}
	refreshed := store.Get(current.BeadID)
	if refreshed == nil {
		return nil, nil
	}
	if !assignment.SameAssignmentGeneration(current, refreshed) {
		return nil, fmt.Errorf("assignment %s generation changed while waiting to clear", current.BeadID)
	}
	if assignmentHasPendingTerminalReconciliation(refreshed) {
		return nil, fmt.Errorf("assignment %s is awaiting terminal reconciliation", current.BeadID)
	}
	if !assignmentStatusIsTerminalForCleanup(current.Status) && assignmentStatusIsTerminalForCleanup(refreshed.Status) {
		return nil, fmt.Errorf("assignment %s reached terminal status %s while waiting to clear", current.BeadID, refreshed.Status)
	}
	current = refreshed
	if assignForce && current.DispatchState == assignment.DispatchSending && !IsJSONOutput() {
		output.PrintWarningf(
			"Force-clearing %s while its dispatch outcome is unknown: the original message may already have been delivered, so re-assigning can duplicate work. Verify pane %d before re-dispatching.",
			current.BeadID, current.Pane,
		)
	}
	var clearing *assignment.Assignment
	switch {
	case assignForce && len(expected) == 0:
		// Operator escape hatch: --force also releases assignments stuck at
		// the outcome-unknown dispatch boundary (see BeginClearForce).
		clearing, err = store.BeginClearForce(ctx, current.BeadID, time.Now().UTC())
	case assignForce:
		clearing, err = store.BeginClearForceIfStatus(ctx, current.BeadID, time.Now().UTC(), expected...)
	case len(expected) == 0:
		clearing, err = store.BeginClear(ctx, current.BeadID, time.Now().UTC())
	default:
		clearing, err = store.BeginClearIfStatus(ctx, current.BeadID, time.Now().UTC(), expected...)
	}
	if err != nil {
		return nil, err
	}
	if assignmentHasPendingTerminalReconciliation(clearing) {
		return nil, fmt.Errorf("assignment %s is awaiting terminal reconciliation", current.BeadID)
	}
	var released []string
	if clearing.ClearState != assignment.ClearStateLeasesReleased {
		var releaseErr error
		released, releaseErr = releaseAssignmentLeases(ctx, session, clearing)
		if releaseErr != nil {
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, releaseErr); persistErr != nil {
				return nil, errors.Join(releaseErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return nil, releaseErr
		}
		clearing, err = store.RecordClearLeasesReleased(ctx, current.BeadID)
		if err != nil {
			return released, fmt.Errorf("persist completed reservation release: %w", err)
		}
	}
	if strings.TrimSpace(clearing.ClaimActor) != "" {
		projectDir, projectErr := resolveAssignProjectDir(ctx, session)
		if projectErr != nil {
			claimErr := fmt.Errorf("resolve project for Beads claim release: %w", projectErr)
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
				return released, errors.Join(claimErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return released, claimErr
		}
		if _, claimErr := releaseBeadClaimForAssignment(ctx, projectDir, clearing.BeadID, clearing.ClaimActor); claimErr != nil {
			claimErr = fmt.Errorf("release Beads claim for %s: %w", clearing.BeadID, claimErr)
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
				return released, errors.Join(claimErr, fmt.Errorf("persist clear failure: %w", persistErr))
			}
			return released, claimErr
		}
	}
	if err := store.CompleteClear(ctx, current.BeadID); err != nil {
		removalErr := fmt.Errorf("reservations released but assignment removal failed: %w", err)
		if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, removalErr); persistErr != nil {
			return released, errors.Join(removalErr, fmt.Errorf("persist clear failure: %w", persistErr))
		}
		return released, removalErr
	}
	return released, nil
}

func assignmentHasPendingTerminalReconciliation(current *assignment.Assignment) bool {
	return current != nil &&
		(current.PendingTerminalStatus == assignment.StatusCompleted || current.PendingTerminalStatus == assignment.StatusFailed)
}

func assignmentStatusIsTerminalForCleanup(status assignment.AssignmentStatus) bool {
	return status == assignment.StatusCompleted || status == assignment.StatusFailed || status == assignment.StatusReassigned
}

// runClearSpecificBeads handles --clear flag (clear specific bead assignments)
func runClearSpecificBeads(cmd *cobra.Command, session string, clearBeads string) error {
	beadIDs := strings.Split(clearBeads, ",")
	for i := range beadIDs {
		beadIDs[i] = strings.TrimSpace(beadIDs[i])
	}
	return runClearSelectedAssignments(cmd, session, beadIDs, "clear")
}

func runClearFailedAssignments(cmd *cobra.Command, session string) error {
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitClearStoreError(session, "clear-failed", err)
	}
	beadIDs := make([]string, 0)
	for _, current := range store.GetAll() {
		if current.Status == assignment.StatusFailed {
			beadIDs = append(beadIDs, current.BeadID)
		}
	}
	return runClearSelectedAssignmentsFromStore(cmd, store, session, beadIDs, "clear-failed")
}

func emitClearStoreError(session, subcommand string, storeErr error) error {
	err := fmt.Errorf("failed to load assignment store: %w", storeErr)
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: subcommand,
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    "STORE_ERROR",
				Message: err.Error(),
			},
		})
	}
	return err
}

func runClearSelectedAssignments(cmd *cobra.Command, session string, beadIDs []string, subcommand string) error {
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitClearStoreError(session, subcommand, err)
	}
	return runClearSelectedAssignmentsFromStore(cmd, store, session, beadIDs, subcommand)
}

func runClearSelectedAssignmentsFromStore(cmd *cobra.Command, store *assignment.AssignmentStore, session string, beadIDs []string, subcommand string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]ClearAssignmentResult, 0, len(beadIDs))
	successCount := 0
	var cancellationErr error
	var typedFailureCode string
	var firstFailureErr error

	for _, beadID := range beadIDs {
		result := ClearAssignmentResult{
			BeadID: beadID,
		}

		// Find the assignment
		assignments := store.GetAll()
		var foundAssignment *assignment.Assignment
		for _, a := range assignments {
			if a.BeadID == beadID && (a.Status != assignment.StatusCompleted || assignForce) {
				foundAssignment = &a
				break
			}
		}

		if foundAssignment == nil {
			result.Success = false
			result.Error = "assignment not found or already completed"
		} else {
			result.PreviousPane = foundAssignment.Pane
			result.PreviousAgent = foundAssignment.AgentName
			result.PreviousAgentType = foundAssignment.AgentType
			result.PreviousStatus = string(foundAssignment.Status)
			result.AssignmentFound = true

			var releasedFiles []string
			clearErr := cancellationErr
			if clearErr == nil {
				clearErr = ctx.Err()
			}
			if clearErr == nil {
				if subcommand == "clear-failed" {
					releasedFiles, clearErr = clearStoredAssignmentIfStatus(ctx, store, session, foundAssignment, assignment.StatusFailed)
				} else {
					releasedFiles, clearErr = clearStoredAssignment(ctx, store, session, foundAssignment)
				}
			}
			clearErr = preserveCommandContextError(ctx, clearErr)
			if clearErr != nil {
				if firstFailureErr == nil {
					firstFailureErr = clearErr
				}
				result.Success = false
				result.Error = clearErr.Error()
				result.ErrorCode = clearAssignmentFailureCode(clearErr)
				if typedFailureCode == "" && result.ErrorCode != "" {
					typedFailureCode = result.ErrorCode
				}
				if result.ErrorCode == robot.ErrCodeTimeout {
					if cancellationErr == nil {
						cancellationErr = clearErr
					}
				}
			} else {
				result.FilesReleased = releasedFiles
				result.FileReservationsReleased = len(releasedFiles) > 0
				result.Success = true
				successCount++
			}
		}

		results = append(results, result)
	}

	// Output results
	if IsJSONOutput() {
		// Calculate reservations released count
		reservationsReleased := 0
		for _, r := range results {
			if r.FileReservationsReleased {
				reservationsReleased += len(r.FilesReleased)
			}
		}

		envelope := ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: subcommand,
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    successCount == len(beadIDs),
			Data: &ClearAssignmentsData{
				Cleared: results,
				Summary: ClearAssignmentsSummary{
					ClearedCount:         successCount,
					ReservationsReleased: reservationsReleased,
					FailedCount:          len(beadIDs) - successCount,
				},
			},
			Warnings: []string{},
		}
		if !envelope.Success {
			code := "CLEAR_FAILED"
			message := fmt.Sprintf("%d of %d assignments failed to clear", len(beadIDs)-successCount, len(beadIDs))
			if cancellationErr != nil {
				code = robot.ErrCodeTimeout
				message = fmt.Sprintf("assignment clear canceled: %v", cancellationErr)
			} else if successCount == 0 && typedFailureCode != "" {
				code = typedFailureCode
				message = results[0].Error
			}
			envelope.Error = &ClearAssignmentsError{Code: code, Message: message}
		}
		// bd-oqwmf: clear-bulk is dynamic — envelope.Success may be false
		// (zero of N cleared). Encode then route through jsonFailureExit on
		// the failure branch to keep `$?` honest for partial/total failure.
		if encErr := json.NewEncoder(os.Stdout).Encode(envelope); encErr != nil {
			return encErr
		}
		if !envelope.Success {
			return errors.Join(jsonFailureExit(), firstFailureErr)
		}
		return nil
	}

	// Text output
	if !assignQuiet {
		fmt.Printf("Cleared %d of %d bead assignments:\n\n", successCount, len(beadIDs))
		for _, result := range results {
			if result.Success {
				fmt.Printf("  ✓ %s (pane %d, %s)\n", result.BeadID, result.PreviousPane, result.PreviousAgentType)
				if len(result.FilesReleased) > 0 {
					fmt.Printf("    Released files: %v\n", result.FilesReleased)
				}
			} else {
				fmt.Printf("  ✗ %s: %s\n", result.BeadID, result.Error)
			}
		}
	}

	if successCount != len(beadIDs) {
		if cancellationErr != nil {
			return fmt.Errorf("assignment clear canceled: %w", cancellationErr)
		}
		return errors.Join(fmt.Errorf("%d of %d assignments failed to clear", len(beadIDs)-successCount, len(beadIDs)), firstFailureErr)
	}
	return nil
}

func assignmentMatchesPhysicalPane(current assignment.Assignment, target tmux.Pane) (bool, error) {
	paneID, err := assignment.CanonicalPaneIdentity(&current)
	if err != nil {
		return false, err
	}
	return paneID == strings.TrimSpace(target.ID), nil
}

func resolveClearPaneTarget(panes []tmux.Pane, selector string) (tmux.Pane, error) {
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, err
	}
	return resolved[0], nil
}

// runClearPaneAssignments handles --clear-pane for one canonical physical pane.
func runClearPaneAssignments(cmd *cobra.Command, session, selector string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	paneDisplay := -1
	// Helper to create error envelope for clear-pane
	makeClearPaneErrorEnvelope := func(code, msg string) ClearAssignmentsEnvelope {
		panePtr := paneDisplay
		return ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear-pane",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    false,
			Data:       &ClearAssignmentsData{Pane: &panePtr},
			Warnings:   []string{},
			Error: &ClearAssignmentsError{
				Code:    code,
				Message: msg,
			},
		}
	}

	// Get panes to validate the pane exists
	panes, err := getClearPanePanes(ctx, session)
	if err != nil {
		err = preserveCommandContextError(ctx, fmt.Errorf("failed to get panes: %w", err))
		code := "TMUX_ERROR"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		if IsJSONOutput() {
			return emitTypedAssignmentFailure(err, makeClearPaneErrorEnvelope(code, err.Error()))
		}
		return err
	}

	targetPane, err := resolveClearPaneTarget(panes, selector)
	if err != nil {
		err = fmt.Errorf("resolve pane %q in session %s: %w", selector, session, err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearPaneErrorEnvelope(directPaneSelectorErrorCode(err), err.Error()))
		}
		return err
	}
	paneDisplay = targetPane.Index

	agentType := agentTypeForPane(targetPane)

	// Load assignment store
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		err = fmt.Errorf("failed to load assignment store: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeClearPaneErrorEnvelope("STORE_ERROR", err.Error()))
		}
		return err
	}

	// Find all assignments for this pane
	assignments := store.GetAll()
	var paneAssignments []assignment.Assignment
	for _, a := range assignments {
		matches, identityErr := assignmentMatchesPhysicalPane(a, targetPane)
		if identityErr != nil {
			wrapped := fmt.Errorf("resolve assignment %s physical pane: %w", a.BeadID, identityErr)
			return emitTypedAssignmentFailure(wrapped, makeClearPaneErrorEnvelope("PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error()))
		}
		if matches && (a.Status != assignment.StatusCompleted || assignForce) {
			paneAssignments = append(paneAssignments, a)
		}
	}

	result := ClearAllResult{
		Pane:      targetPane.Index,
		AgentType: agentType,
		Success:   true,
	}
	var cancellationErr error
	var typedFailureCode string
	var firstFailureErr error

	// Clear each assignment
	for _, a := range paneAssignments {
		beadResult := ClearAssignmentResult{
			BeadID:            a.BeadID,
			PreviousPane:      a.Pane,
			PreviousAgent:     a.AgentName,
			PreviousAgentType: a.AgentType,
			AssignmentFound:   true,
			PreviousStatus:    string(a.Status),
		}

		var releasedFiles []string
		clearErr := cancellationErr
		if clearErr == nil {
			clearErr = ctx.Err()
		}
		if clearErr == nil {
			releasedFiles, clearErr = clearStoredAssignment(ctx, store, session, &a)
		}
		clearErr = preserveCommandContextError(ctx, clearErr)
		if clearErr != nil {
			if firstFailureErr == nil {
				firstFailureErr = clearErr
			}
			beadResult.Success = false
			beadResult.Error = clearErr.Error()
			beadResult.ErrorCode = clearAssignmentFailureCode(clearErr)
			if typedFailureCode == "" && beadResult.ErrorCode != "" {
				typedFailureCode = beadResult.ErrorCode
			}
			if beadResult.ErrorCode == robot.ErrCodeTimeout {
				if cancellationErr == nil {
					cancellationErr = clearErr
				}
			}
			result.Success = false
		} else {
			beadResult.FilesReleased = releasedFiles
			beadResult.FileReservationsReleased = len(releasedFiles) > 0
			beadResult.Success = true
		}

		result.ClearedBeads = append(result.ClearedBeads, beadResult)
	}

	if len(paneAssignments) == 0 {
		result.Error = "no active assignments found for this pane"
	}

	// Output result
	if IsJSONOutput() {
		// Calculate reservations released count
		reservationsReleased := 0
		for _, r := range result.ClearedBeads {
			if r.FileReservationsReleased {
				reservationsReleased += len(r.FilesReleased)
			}
		}

		successCount := 0
		for _, cleared := range result.ClearedBeads {
			if cleared.Success {
				successCount++
			}
		}
		failedCount := len(result.ClearedBeads) - successCount
		panePtr := targetPane.Index
		envelope := ClearAssignmentsEnvelope{
			Command:    "assign",
			Subcommand: "clear-pane",
			Session:    session,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Success:    failedCount == 0,
			Data: &ClearAssignmentsData{
				Cleared:   result.ClearedBeads,
				Pane:      &panePtr,
				AgentType: agentType,
				Summary: ClearAssignmentsSummary{
					ClearedCount:         successCount,
					ReservationsReleased: reservationsReleased,
					FailedCount:          failedCount,
				},
			},
			Warnings: []string{},
		}
		if !envelope.Success {
			code := "CLEAR_FAILED"
			message := fmt.Sprintf("%d of %d pane assignments failed to clear", failedCount, len(result.ClearedBeads))
			if cancellationErr != nil {
				code = robot.ErrCodeTimeout
				message = fmt.Sprintf("pane assignment clear canceled: %v", cancellationErr)
			} else if successCount == 0 && typedFailureCode != "" {
				code = typedFailureCode
				message = result.ClearedBeads[0].Error
			}
			envelope.Error = &ClearAssignmentsError{Code: code, Message: message}
		}
		if err := json.NewEncoder(os.Stdout).Encode(envelope); err != nil {
			return err
		}
		if !envelope.Success {
			return errors.Join(jsonFailureExit(), firstFailureErr)
		}
		return nil
	}

	// Text output
	if !assignQuiet {
		if len(paneAssignments) == 0 {
			fmt.Printf("No active assignments found for pane %s (%s)\n", assignmentPaneTarget(targetPane), agentType)
		} else {
			fmt.Printf("Cleared all assignments for pane %s (%s):\n\n", assignmentPaneTarget(targetPane), agentType)
			for _, beadResult := range result.ClearedBeads {
				if beadResult.Success {
					fmt.Printf("  ✓ %s\n", beadResult.BeadID)
					if len(beadResult.FilesReleased) > 0 {
						fmt.Printf("    Released files: %v\n", beadResult.FilesReleased)
					}
				} else {
					fmt.Printf("  ✗ %s: %s\n", beadResult.BeadID, beadResult.Error)
				}
			}
		}
	}

	if !result.Success {
		err := fmt.Errorf("one or more assignments for pane %s failed to clear", assignmentPaneTarget(targetPane))
		return errors.Join(err, firstFailureErr)
	}
	return nil
}

// runReassignment handles the --reassign flag for moving a bead between agents.
func runReassignment(ctx context.Context, session string) error {
	if ctx == nil {
		return emitReassignFailure(session, robot.ErrCodeInternalError, "reassignment context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("reassignment canceled: %w", err)
		return emitTypedAssignmentFailure(cancelErr, makeReassignErrorEnvelope(session, robot.ErrCodeTimeout, cancelErr.Error(), nil))
	}
	beadID := strings.TrimSpace(assignReassign)
	if beadID == "" {
		return emitReassignFailure(session, "INVALID_ARGS", "bead ID required for --reassign", nil)
	}
	paneSelector := strings.TrimSpace(assignToPane)
	if paneSelector == "" && assignToType == "" {
		return emitReassignFailure(session, "INVALID_ARGS", "either --to-pane or --to-type must be specified with --reassign", nil)
	}
	if paneSelector != "" && assignToType != "" {
		return emitReassignFailure(session, "INVALID_ARGS", "cannot specify both --to-pane and --to-type", nil)
	}

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return emitReassignFailure(session, "STORE_ERROR", fmt.Sprintf("failed to load assignment store: %v", err), nil)
	}
	projectDir, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return emitReassignFailure(session, "PROJECT_ERROR", err.Error(), nil)
	}
	currentAssignment := store.Get(beadID)
	if currentAssignment == nil {
		return emitReassignFailure(session, "NOT_ASSIGNED", fmt.Sprintf("bead %s does not have an active assignment", beadID), nil)
	}
	if currentAssignment.Status != assignment.StatusWorking {
		return emitReassignFailure(session, "INVALID_STATE", fmt.Sprintf("bead %s assignment is not reassignable from status %s", beadID, currentAssignment.Status), map[string]interface{}{
			"current_status": string(currentAssignment.Status),
		})
	}

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return emitContextAwareReassignFailure(ctx, session, "TMUX_ERROR", fmt.Errorf("failed to get panes: %w", err), nil)
	}
	var targetPane *tmux.Pane
	var targetAgentType string
	if paneSelector != "" {
		resolvedPane, _, resolveErr := resolveDirectAssignmentPane(panes, paneSelector)
		if resolveErr != nil {
			return emitReassignFailure(session, directPaneSelectorErrorCode(resolveErr), resolveErr.Error(), nil)
		}
		targetPane = &resolvedPane
		targetAgentType = agentTypeForPane(resolvedPane)
		if targetAgentType == "user" || targetAgentType == "unknown" {
			return emitReassignFailure(session, "PANE_NOT_FOUND", fmt.Sprintf("pane %s is not an agent pane (type: %s)", assignmentPaneTarget(resolvedPane), targetAgentType), nil)
		}
	} else {
		idleAgents, idleErr := getIdleAgents(ctx, session, assignToType, assignVerbose)
		if idleErr != nil {
			return emitContextAwareReassignFailure(ctx, session, "TMUX_ERROR", idleErr, nil)
		}
		idleAgents, idleErr = assignRoleEligibleAgents(idleAgents, projectDir, assignTemplate)
		if idleErr != nil {
			return emitReassignFailure(session, "PERSONA_ERROR", idleErr.Error(), nil)
		}
		if len(idleAgents) == 0 {
			return emitReassignFailure(session, "NO_IDLE_AGENT", fmt.Sprintf("no idle %s agents available", assignToType), map[string]interface{}{"agent_type": assignToType})
		}
		targetPane = &idleAgents[0].pane
		targetAgentType = idleAgents[0].agentType
	}

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	alreadyAssigned, identityErr := assignmentMatchesPhysicalPane(*currentAssignment, *targetPane)
	if identityErr != nil {
		wrapped := fmt.Errorf("resolve current assignment %s physical pane: %w", beadID, identityErr)
		return emitTypedAssignmentFailure(wrapped, makeReassignErrorEnvelope(session, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), nil))
	}
	if alreadyAssigned {
		return emitReassignFailure(session, "ALREADY_ASSIGNED", fmt.Sprintf("bead %s is already assigned to pane %s", beadID, assignmentPaneTarget(*targetPane)), nil)
	}
	existingAssignment, identityErr := findAssignmentForPhysicalPane(store, *targetPane)
	if identityErr != nil {
		wrapped := fmt.Errorf("resolve active assignment for pane %s: %w", targetPane.ID, identityErr)
		return emitTypedAssignmentFailure(wrapped, makeReassignErrorEnvelope(session, "PANE_IDENTITY_MIGRATION_REQUIRED", wrapped.Error(), nil))
	}
	if existingAssignment != nil && existingAssignment.BeadID != beadID {
		return emitReassignFailure(session, "TARGET_BUSY", fmt.Sprintf("pane %s already has assignment %s", assignmentPaneTarget(*targetPane), existingAssignment.BeadID), map[string]interface{}{
			"current_bead":   existingAssignment.BeadID,
			"current_status": string(existingAssignment.Status),
		})
	}
	observation, observeErr := observeAssignSessionWithTimeout(ctx, session, assignTimeout)
	if observeErr != nil {
		return emitContextAwareReassignFailure(ctx, session, "OBSERVATION_ERROR", observeErr, nil)
	}
	paneObservation, observeErr := currentAssignPaneObservation(observation, targetPane.ID, time.Now())
	if observeErr != nil {
		return emitContextAwareReassignFailure(ctx, session, "OBSERVATION_ERROR", observeErr, map[string]interface{}{"pane_id": targetPane.ID})
	}
	state := string(paneObservation.Current.Status.State)
	if !paneObservation.SafeToDispatch() && !assignForce {
		return emitReassignFailure(session, "TARGET_BUSY", fmt.Sprintf("pane %s is busy (state: %s), use --force to override", assignmentPaneTarget(*targetPane), state), map[string]interface{}{"pane_state": state})
	}

	beadTitle := currentAssignment.BeadTitle
	if beadTitle == "" {
		beadTitle, err = getBeadTitle(ctx, projectDir, beadID)
		if err != nil {
			return emitContextAwareReassignFailure(ctx, session, "BEAD_LOOKUP_FAILED", err, nil)
		}
	}
	prompt := assignPrompt
	if prompt == "" {
		prompt = expandPromptTemplate(beadID, beadTitle, assignTemplate, assignTemplateFile)
	}
	newAgentName := assignmentAgentIdentityForPane(projectDir, session, targetAgentType, *targetPane, multiWindow)
	idempotencyKey, err := assignment.NewAssignmentIdempotencyKey()
	if err != nil {
		return emitReassignFailure(session, "STORE_ERROR", fmt.Sprintf("generate reassignment identity: %v", err), nil)
	}

	reservationRequired := currentAssignment.ReservationRequired || assignReserveFiles
	reservationDiscovery := currentAssignment.ReservationDiscovery || assignReserveFiles
	requestedPaths := append([]string(nil), currentAssignment.ReservationRequested...)
	if len(requestedPaths) == 0 {
		requestedPaths = append(requestedPaths, currentAssignment.ReservationInputPaths...)
	}
	if len(requestedPaths) > 0 {
		reservationDiscovery = false
	}
	var reservationMgr *assign.FileReservationManager
	if reservationRequired {
		amClient := newAgentMailClient(projectDir)
		if !amClient.IsAvailable() {
			return emitReassignFailure(session, "RESERVATION_REQUIRED", "Agent Mail is unavailable; refusing to transfer a reservation-required assignment", nil)
		}
		reservationMgr = assign.NewFileReservationManager(amClient, projectDir)
		reservationMgr.SetTTL(max(3600, int(resolveAssignTimeout(assignTimeout).Seconds())*2))
	}

	assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, assignForce)
	atomicResult, reassignErr := atomicCoordinator.Execute(assignmentCtx, assignment.AtomicRequest{
		BeadID:                    beadID,
		BeadTitle:                 beadTitle,
		Target:                    targetPane.ID,
		OccupancyKey:              targetPane.ID,
		Pane:                      targetPane.Index,
		AgentType:                 targetAgentType,
		AgentName:                 newAgentName,
		Actor:                     currentAssignment.ClaimActor,
		Prompt:                    prompt,
		IdempotencyKey:            idempotencyKey,
		RequireReservation:        reservationRequired,
		AllowReservationDiscovery: reservationDiscovery,
		RequestedPaths:            requestedPaths,
		ReservationTTL:            time.Hour,
		ReplaceWorkingAssignment:  true,
	})
	reassignErr = preserveCommandContextError(assignmentCtx, reassignErr)
	if reassignErr != nil {
		details := map[string]interface{}{"target": targetPane.ID}
		durableResult := matchingReassignmentDurableResult(atomicResult.Assignment, beadID, idempotencyKey)
		if durableResult != nil {
			details["durable_status"] = string(durableResult.Status)
			details["dispatch_state"] = string(durableResult.DispatchState)
			details["idempotency_key"] = durableResult.IdempotencyKey
		}
		return emitReassignFailure(session, classifyReassignError(reassignErr, durableResult), reassignErr.Error(), details)
	}
	if atomicResult.Assignment == nil || !atomicResult.Sent {
		return emitReassignFailure(session, "SEND_ERROR", "reassignment completed without a durable dispatch receipt", nil)
	}

	durable := atomicResult.Assignment
	releasedReservationCount := len(atomicResult.ReleasedPaths)
	if len(atomicResult.ReleasedReservationIDs) > 0 {
		releasedReservationCount = len(atomicResult.ReleasedReservationIDs)
	}
	data := &ReassignData{
		BeadID:                       beadID,
		BeadTitle:                    durable.BeadTitle,
		Pane:                         durable.Pane,
		AgentType:                    durable.AgentType,
		AgentName:                    durable.AgentName,
		Status:                       string(durable.Status),
		PromptSent:                   atomicResult.Sent,
		AssignedAt:                   durable.AssignedAt.UTC().Format(time.RFC3339),
		PreviousPane:                 currentAssignment.Pane,
		PreviousAgent:                currentAssignment.AgentName,
		PreviousAgentType:            currentAssignment.AgentType,
		PreviousStatus:               string(currentAssignment.Status),
		FileReservationsTransferred:  releasedReservationCount > 0 || len(atomicResult.Lease.Granted) > 0,
		FileReservationsReleasedFrom: releasedReservationCount,
		FileReservationsCreatedFor:   len(atomicResult.Lease.Granted),
	}
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(ReassignEnvelope{
			Command: "assign", Subcommand: "reassign", Session: session,
			Timestamp: time.Now().UTC().Format(time.RFC3339), Success: true, Data: data, Warnings: []string{},
		})
	}
	if !assignQuiet {
		fmt.Printf("Reassigned %s to pane %s (%s)\n", beadID, assignmentPaneTarget(*targetPane), targetAgentType)
		fmt.Printf("  Previous: pane %d (%s)\n", currentAssignment.Pane, currentAssignment.AgentType)
		displayPrompt := durable.PromptSent
		if displayPrompt == "" {
			displayPrompt = durable.PendingPrompt
		}
		fmt.Printf("  Prompt sent: %s...\n", truncateString(displayPrompt, 50))
		if data.FileReservationsTransferred {
			fmt.Println("  File reservations transferred")
		}
	}
	return nil
}

func matchingReassignmentDurableResult(recorded *assignment.Assignment, beadID, idempotencyKey string) *assignment.Assignment {
	if recorded == nil || recorded.BeadID != strings.TrimSpace(beadID) ||
		strings.TrimSpace(recorded.IdempotencyKey) == "" || recorded.IdempotencyKey != strings.TrimSpace(idempotencyKey) {
		return nil
	}
	return recorded
}

func emitReassignFailure(session, code, message string, details map[string]interface{}) error {
	err := errors.New(message)
	if IsJSONOutput() {
		return emitJSONFailureEnvelope(makeReassignErrorEnvelope(session, code, message, details))
	}
	return err
}

func emitTypedAssignmentFailure(err error, envelope interface{}) error {
	if !IsJSONOutput() {
		return err
	}
	return errors.Join(emitJSONFailureEnvelope(envelope), err)
}

func preserveCommandContextError(ctx context.Context, err error) error {
	if ctx == nil || err == nil {
		return err
	}
	ctxErr := ctx.Err()
	if ctxErr == nil || errors.Is(err, ctxErr) {
		return err
	}
	return errors.Join(err, ctxErr)
}

func emitContextAwareReassignFailure(ctx context.Context, session, fallbackCode string, err error, details map[string]interface{}) error {
	err = preserveCommandContextError(ctx, err)
	code := fallbackCode
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = robot.ErrCodeTimeout
	}
	return emitTypedAssignmentFailure(err, makeReassignErrorEnvelope(session, code, err.Error(), details))
}

func classifyReassignError(err error, durable *assignment.Assignment) string {
	var dispatchErr *dispatchsvc.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return robot.ErrCodeTimeout
	case errors.Is(err, assignment.ErrTargetOccupied):
		return "TARGET_BUSY"
	case errors.Is(err, assignment.ErrClaimIneligible):
		return "BEAD_INELIGIBLE"
	case errors.Is(err, assignment.ErrClaimConflict):
		return "CLAIM_CONFLICT"
	case errors.Is(err, assignment.ErrReservationRequired), errors.Is(err, assignment.ErrReservationReleaseRequired):
		return "RESERVATION_REQUIRED"
	case errors.Is(err, assignment.ErrDispatchOutcomeUnknown):
		return "DISPATCH_UNKNOWN"
	case errors.Is(err, assignment.ErrWorkingReplacementNotAllowed), errors.Is(err, assignment.ErrAssignmentStatusMismatch):
		return "INVALID_STATE"
	case errors.As(err, &dispatchErr) && dispatchErr.Code == dispatchsvc.ErrRedactionBlocked:
		return "REDACTION_BLOCKED"
	case durable != nil && durable.ClearState == assignment.ClearStateReservationReleasing && strings.TrimSpace(durable.ClearError) != "":
		return "RESERVATION_RELEASE_FAILED"
	case durable != nil && durable.DispatchAttempts > 0:
		return "SEND_ERROR"
	default:
		return "REASSIGN_ERROR"
	}
}

// makeReassignErrorEnvelope creates a standard error envelope for reassignment operations
func makeReassignErrorEnvelope(session, code, message string, details map[string]interface{}) AssignEnvelope[ReassignData] {
	return AssignEnvelope[ReassignData]{
		Command:    "assign",
		Subcommand: "reassign",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    false,
		Warnings:   []string{},
		Error: &AssignError{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

// findAssignmentForPhysicalPane finds the active assignment occupying one
// physical tmux pane. Every active row must carry a canonical tmux pane ID;
// ambiguous legacy rows fail closed because they may own the requested pane.
func findAssignmentForPhysicalPane(store *assignment.AssignmentStore, pane tmux.Pane) (*assignment.Assignment, error) {
	var found *assignment.Assignment
	for _, a := range store.ListActive() {
		if a == nil {
			continue
		}
		matches, err := assignmentMatchesPhysicalPane(*a, pane)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("physical tmux pane %s has multiple active assignments %s and %s", pane.ID, found.BeadID, a.BeadID)
		}
		found = a
	}
	return found, nil
}

// releaseFileReservationsWithIDs releases file reservations using stored reservation IDs
func releaseAssignmentReservationsForClear(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("assignment reservation release context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("assignment is required")
	}
	if current.ClearState != assignment.ClearStateReservationReleasing && current.ClearState != assignment.ClearStateLeasesReleased {
		return nil, fmt.Errorf("assignment %s has no durable release barrier", current.BeadID)
	}
	if current.ClearState == assignment.ClearStateLeasesReleased || current.ReservationState == assignment.ReservationReleased {
		return nil, nil
	}
	// Atomic rows explicitly record whether reservation was part of the intent.
	// With no requested reservation and no durable lease metadata, there is
	// nothing external to release; clearing must remain a local operation.
	if current.IdempotencyKey != "" && !current.ReservationRequired {
		return nil, nil
	}
	if current.IdempotencyKey != "" && current.ReservationRequired {
		if assignment.ReservationOutcomeNeedsReconciliation(current) {
			return reconcileAndReleaseAssignmentReservationsForClear(ctx, session, current)
		}
		switch current.ReservationState {
		case assignment.ReservationFailed:
			return nil, nil
		case assignment.ReservationPending:
			if current.ReservationAttempts == 0 {
				return nil, nil
			}
		}
	}
	if len(current.ReservationIDs) > 0 {
		paths := append([]string(nil), current.ReservedPaths...)
		if len(paths) == 0 {
			paths = append(paths, current.ReservationRequested...)
		}
		if len(paths) == 0 {
			paths = append(paths, current.ReservationInputPaths...)
		}
		return releaseFileReservationsWithIDs(ctx, session, current, current.ReservationIDs, paths)
	}
	if len(current.ReservedPaths) > 0 {
		return releaseFileReservationsByStoredPaths(ctx, session, current, current.ReservedPaths)
	}
	return releaseReservations(ctx, session, current)
}

func reconcileAndReleaseAssignmentReservationsForClear(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot reconcile reservation outcome for %s: Agent Mail is unavailable", current.BeadID)
	}
	requested := append([]string(nil), current.ReservationRequested...)
	if len(requested) == 0 {
		requested = append(requested, current.ReservationInputPaths...)
	}
	if len(requested) == 0 {
		requested = assign.ExtractFilePaths(current.BeadTitle, "")
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("cannot reconcile reservation outcome for %s without durable requested paths", current.BeadID)
	}
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	result, err := manager.ReconcileForBead(releaseCtx, current.BeadID, current.AgentName, requested)
	if err != nil {
		return nil, fmt.Errorf("reconcile reservation outcome for %s: %w", current.BeadID, err)
	}
	if result == nil || (len(result.ReservationIDs) == 0 && len(result.GrantedPaths) == 0) {
		return nil, nil
	}
	if len(result.ReservationIDs) > 0 {
		releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, result.ReservationIDs, requested)
		if err != nil {
			return nil, fmt.Errorf("release reconciled reservations for %s: %w", current.BeadID, err)
		}
		if len(releasedPaths) > 0 {
			return releasedPaths, nil
		}
		return []string{fmt.Sprintf("%d reservations", len(result.ReservationIDs))}, nil
	}
	releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, nil, result.GrantedPaths)
	if err != nil {
		return nil, fmt.Errorf("release reconciled reservation paths for %s: %w", current.BeadID, err)
	}
	return releasedPaths, nil
}

func releaseFileReservationsWithIDs(ctx context.Context, session string, current *assignment.Assignment, reservationIDs []int, paths []string) ([]string, error) {
	if len(reservationIDs) == 0 {
		return nil, nil
	}
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}

	// Create Agent Mail client
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot release %d stored reservations for %s: Agent Mail is unavailable", len(reservationIDs), beadID)
	}

	// Create a reservation manager
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()

	releasedPaths, releaseErr := manager.ReleaseExactForBead(releaseCtx, current, reservationIDs, paths)
	if releaseErr != nil {
		return nil, fmt.Errorf("failed to release reservations: %w", releaseErr)
	}
	if assignHumanDiagnostics(assignVerbose) {
		fmt.Fprintf(os.Stderr, "[RESERVE] Released %d reservations for %s\n", len(reservationIDs), beadID)
	}
	if len(releasedPaths) > 0 {
		return releasedPaths, nil
	}
	return []string{fmt.Sprintf("%d reservations", len(reservationIDs))}, nil
}

func releaseFileReservationsByStoredPaths(ctx context.Context, session string, current *assignment.Assignment, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, fmt.Errorf("cannot release stored reservation paths for %s: Agent Mail is unavailable", beadID)
	}
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	releasedPaths, releaseErr := manager.ReleaseExactForBead(releaseCtx, current, nil, paths)
	if releaseErr != nil {
		return nil, fmt.Errorf("failed to release reservation paths: %w", releaseErr)
	}
	return releasedPaths, nil
}

// releaseFileReservations releases file reservations for a bead via Agent Mail
// This is used when we don't have reservation IDs stored
func releaseFileReservations(ctx context.Context, session string, current *assignment.Assignment) ([]string, error) {
	if current == nil {
		return nil, errors.New("assignment release barrier is required")
	}
	beadID := current.BeadID
	projectKey, err := resolveAssignProjectDir(ctx, session)
	if err != nil {
		return nil, err
	}

	// Create Agent Mail client
	amClient := newAgentMailClient(projectKey)
	if !amClient.IsAvailable() {
		return nil, nil // No error if Agent Mail isn't available
	}

	// Get bead details to extract file paths
	beadTitle, titleErr := getBeadTitle(ctx, projectKey, beadID)
	if titleErr != nil {
		return nil, fmt.Errorf("look up bead %s title for reservation release: %w", beadID, titleErr)
	}
	if beadTitle == "" {
		if assignHumanDiagnostics(assignVerbose) {
			fmt.Fprintf(os.Stderr, "[RESERVE] No bead title found for %s, cannot determine paths to release\n", beadID)
		}
		return nil, nil
	}

	// Extract file paths that would have been reserved
	paths := assign.ExtractFilePaths(beadTitle, "")
	if len(paths) == 0 {
		if assignHumanDiagnostics(assignVerbose) {
			fmt.Fprintf(os.Stderr, "[RESERVE] No file paths found in bead %s title, nothing to release\n", beadID)
		}
		return nil, nil
	}

	// Create a reservation manager
	manager := assign.NewFileReservationManager(amClient, projectKey)
	releaseCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()

	releasedPaths, err := manager.ReleaseExactForBead(releaseCtx, current, nil, paths)
	if err != nil {
		return nil, fmt.Errorf("failed to release reservations by paths: %w", err)
	}

	if assignHumanDiagnostics(assignVerbose) {
		fmt.Fprintf(os.Stderr, "[RESERVE] Released reservations for paths: %v (bead: %s)\n", paths, beadID)
	}

	return releasedPaths, nil
}

// ============================================================================
// Dependency Awareness - Completion Detection and Auto-Reassignment
// ============================================================================

// UnblockedBead represents a bead that was previously blocked but is now actionable
type UnblockedBead struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Priority      int      `json:"priority"`
	PrevBlockers  []string `json:"previous_blockers"`
	UnblockedByID string   `json:"unblocked_by_id"` // The blocker that was completed
}

// DependencyAwareResult contains the result of an unblock check
type DependencyAwareResult struct {
	CompletedBeadID string          `json:"completed_bead_id"`
	NewlyUnblocked  []UnblockedBead `json:"newly_unblocked"`
	CyclesDetected  [][]string      `json:"cycles_detected,omitempty"`
	Errors          []string        `json:"errors,omitempty"`
}

// GetNewlyUnblockedBeads checks what beads are now unblocked after a bead completion.
// This is the core function for dependency-aware reassignment.
// It reads the completed bead's uncapped dependent list and identifies beads that:
// 1. Were previously blocked by the completed bead
// 2. Have no remaining blockers (all their dependencies are now completed)
func GetNewlyUnblockedBeads(ctx context.Context, projectDir, completedBeadID string, verbose bool) (*DependencyAwareResult, error) {
	if ctx == nil {
		return nil, errors.New("dependency refresh context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dependency refresh canceled: %w", err)
	}
	result := &DependencyAwareResult{
		CompletedBeadID: completedBeadID,
		NewlyUnblocked:  make([]UnblockedBead, 0),
	}

	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, errors.New("assignment project directory is required")
	}

	completedBeadID = strings.TrimSpace(completedBeadID)
	if completedBeadID == "" {
		return nil, errors.New("completed bead ID is required")
	}
	result.CompletedBeadID = completedBeadID

	if assignHumanDiagnostics(verbose) {
		fmt.Fprintf(os.Stderr, "[DEP] Checking for beads unblocked by %s\n", completedBeadID)
	}

	dependents, err := bv.GetBeadBlockingDependentsContext(ctx, projectDir, completedBeadID)
	if err != nil {
		return result, fmt.Errorf("read dependents for completed bead %s: %w", completedBeadID, err)
	}

	// Each local dependent is re-read by ID. The completed bead's dependents list
	// proves the historical relationship, while the dependent's current br show
	// state proves that every blocking relationship is now terminal and that the
	// work item itself remains assignable. No ranking or result cap is involved.
	for _, dependent := range dependents {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("dependency refresh canceled before validating %s: %w", dependent.ID, err)
		}
		details, detailsErr := bv.GetBeadAssignmentDetailsContext(ctx, projectDir, dependent.ID)
		if detailsErr != nil {
			return result, fmt.Errorf("read live dependency and eligibility state for %s: %w", dependent.ID, detailsErr)
		}
		candidate, skipReason, unresolved := newlyUnblockedCandidateForProject(projectDir, completedBeadID, dependent, details)
		if skipReason == "unrelated" {
			return result, fmt.Errorf("dependent %s no longer reports blocking relationship to %s", dependent.ID, completedBeadID)
		}
		if len(unresolved) > 0 {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] %s still blocked by: %v\n", dependent.ID, unresolved)
			}
			continue
		}
		if candidate == nil {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] %s is related but not assignable: %s\n", dependent.ID, skipReason)
			}
			continue
		}
		result.NewlyUnblocked = append(result.NewlyUnblocked, *candidate)

		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[UNBLOCK] %s now ready (was blocked by %s)\n", dependent.ID, completedBeadID)
		}
	}

	// Check for cycles using BV insights
	client := bv.NewBVClient()
	client.WorkspacePath = projectDir
	insights, err := client.GetInsightsContext(ctx)
	if err != nil {
		return result, fmt.Errorf("inspect dependency cycles after %s completed: %w", completedBeadID, err)
	}
	if insights != nil && len(insights.Cycles) > 0 {
		result.CyclesDetected = insights.Cycles
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] Warning: %d dependency cycles detected\n", len(insights.Cycles))
		}
	}

	return result, nil
}

func dependencyRelationToCompleted(states []bv.BeadDependencyState, completedBeadID string) (bool, []string) {
	related := false
	unresolved := make([]string, 0)
	for _, state := range states {
		if state.ID == completedBeadID {
			related = true
		}
		switch normalizeBeadStatus(state.Status) {
		case "closed", "tombstone":
		default:
			unresolved = append(unresolved, state.ID)
		}
	}
	sort.Strings(unresolved)
	return related, unresolved
}

func newlyUnblockedCandidateForProject(projectDir, completedBeadID string, dependent bv.BeadDependentState, details *bv.BeadAssignmentDetails) (*UnblockedBead, string, []string) {
	return newlyUnblockedCandidateWithGate(completedBeadID, dependent, details, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func newlyUnblockedCandidateWithGate(completedBeadID string, dependent bv.BeadDependentState, details *bv.BeadAssignmentDetails, operatorGated func(string) bool) (*UnblockedBead, string, []string) {
	if details == nil {
		return nil, "live_details_required", nil
	}
	related, unresolved := dependencyRelationToCompleted(details.BlockingDependencies, completedBeadID)
	if !related {
		return nil, "unrelated", nil
	}
	if len(unresolved) > 0 {
		return nil, "blocked_by_dependency", unresolved
	}
	if details.ID != dependent.ID {
		return nil, "live_details_mismatch", nil
	}
	if skip := classifyLiveAssignmentDetailsWithGate(details, nil, operatorGated); skip != nil {
		return nil, skip.Reason, nil
	}
	return &UnblockedBead{
		ID: dependent.ID, Title: details.Title, Priority: details.Priority,
		PrevBlockers: []string{completedBeadID}, UnblockedByID: completedBeadID,
	}, "", nil
}

// CheckCycles returns any dependency cycles detected in the current project.
// Beads in cycles should be excluded from automatic assignment.
// Enhanced with retry logic and comprehensive error handling.
func CheckCycles(ctx context.Context, projectDir string, verbose bool) ([][]string, error) {
	if ctx == nil {
		return nil, errors.New("cycle inspection context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cycle inspection canceled: %w", err)
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, errors.New("assignment project directory is required")
	}
	client := bv.NewBVClient()
	client.WorkspacePath = projectDir

	maxRetries := 2 // Lower than default for non-critical cycle check
	var insights *bv.Insights
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		insights, lastErr = client.GetInsightsContext(ctx)
		if lastErr == nil {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("cycle inspection canceled: %w", ctxErr)
		}

		if assignHumanDiagnostics(verbose) && attempt < maxRetries-1 {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights failed (attempt %d/%d): %v, retrying...\n",
				attempt+1, maxRetries, lastErr)
		}

		if attempt < maxRetries-1 {
			if waitErr := waitContextDelay(ctx, 500*time.Millisecond*time.Duration(attempt+1)); waitErr != nil {
				return nil, fmt.Errorf("cycle inspection canceled during retry backoff: %w", waitErr)
			}
		}
	}

	if lastErr != nil {
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] Failed to get insights after %d attempts: %v\n", maxRetries, lastErr)
			fmt.Fprintf(os.Stderr, "[DEP] Cycle detection unavailable - proceeding without cycle filtering\n")
		}
		return nil, fmt.Errorf("failed to get insights after %d attempts: %w", maxRetries, lastErr)
	}

	if insights == nil {
		if assignHumanDiagnostics(verbose) {
			fmt.Fprintf(os.Stderr, "[DEP] BV insights returned nil - no cycle information available\n")
		}
		return nil, nil
	}

	// Validate cycle data integrity
	var validCycles [][]string
	for i, cycle := range insights.Cycles {
		if len(cycle) < 2 {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] Warning: invalid cycle %d with < 2 nodes: %v\n", i+1, cycle)
			}
			continue
		}

		// Check for duplicate nodes in the cycle (indicates data corruption)
		seen := make(map[string]bool)
		valid := true
		for _, node := range cycle {
			if seen[node] {
				if assignHumanDiagnostics(verbose) {
					fmt.Fprintf(os.Stderr, "[DEP] Warning: cycle %d has duplicate node %s: %v\n", i+1, node, cycle)
				}
				valid = false
				break
			}
			seen[node] = true
		}

		if valid {
			validCycles = append(validCycles, cycle)
		}
	}

	if assignHumanDiagnostics(verbose) && len(validCycles) > 0 {
		fmt.Fprintf(os.Stderr, "[DEP] Detected %d valid dependency cycles:\n", len(validCycles))
		for i, cycle := range validCycles {
			fmt.Fprintf(os.Stderr, "  Cycle %d: %v\n", i+1, cycle)
		}
	}

	if len(validCycles) != len(insights.Cycles) && assignHumanDiagnostics(verbose) {
		fmt.Fprintf(os.Stderr, "[DEP] Filtered out %d invalid cycles\n",
			len(insights.Cycles)-len(validCycles))
	}

	return validCycles, nil
}

// IsBeadInCycle checks if a bead ID is part of any detected dependency cycle.
func IsBeadInCycle(beadID string, cycles [][]string) bool {
	for _, cycle := range cycles {
		for _, id := range cycle {
			if id == beadID {
				return true
			}
		}
	}
	return false
}

// FilterCyclicBeads removes beads that are part of dependency cycles from the list.
func FilterCyclicBeads(ctx context.Context, projectDir string, beads []bv.BeadPreview, verbose bool) ([]bv.BeadPreview, int, error) {
	cycles, err := CheckCycles(ctx, projectDir, false) // Don't log twice if verbose
	if err != nil {
		return nil, 0, err
	}
	returnValues, excluded := filterCyclicBeads(beads, cycles, verbose)
	return returnValues, excluded, nil
}

func filterCyclicBeads(beads []bv.BeadPreview, cycles [][]string, verbose bool) ([]bv.BeadPreview, int) {
	if len(cycles) == 0 {
		return beads, 0
	}

	var filtered []bv.BeadPreview
	excluded := 0

	for _, bead := range beads {
		if IsBeadInCycle(bead.ID, cycles) {
			excluded++
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[DEP] Excluding %s from assignment (in dependency cycle)\n", bead.ID)
			}
			continue
		}
		filtered = append(filtered, bead)
	}

	return filtered, excluded
}

// ============================================================================
// Direct Pane Assignment - ntm assign --pane
// ============================================================================

// DirectAssignResult is the result of a direct pane assignment
type DirectAssignResult struct {
	BeadID         string                        `json:"bead_id"`
	BeadTitle      string                        `json:"bead_title,omitempty"`
	Pane           int                           `json:"pane"` // Window-local display index.
	PaneTarget     string                        `json:"pane_target"`
	PaneID         string                        `json:"pane_id"`
	AgentType      string                        `json:"agent_type"`
	AgentName      string                        `json:"agent_name,omitempty"`
	PromptSent     string                        `json:"prompt_sent"`
	Success        bool                          `json:"success"`
	Error          string                        `json:"error,omitempty"`
	Reservations   *assign.FileReservationResult `json:"reservations,omitempty"`
	PaneWasBusy    bool                          `json:"pane_was_busy,omitempty"`
	DepsIgnored    bool                          `json:"deps_ignored,omitempty"`
	BlockedByBeads []string                      `json:"blocked_by_beads,omitempty"`
	// Receipt is the wrapper-grade dispatch receipt — pane identity,
	// prompt fingerprint, reservation outcome, transport status, and
	// timestamp. Lets downstream automation drop their parallel
	// dispatch log because the envelope itself is the proof of what
	// was sent. See ntm#128.
	Receipt *DispatchReceipt `json:"receipt,omitempty"`
}

// DispatchReceipt is the stable per-dispatch envelope wrappers consume.
// Emitted by `ntm assign --pane …` whenever transport to the pane was
// attempted (success OR transport failure). Refusals that abort before
// transport (PANE_BUSY, BLOCKED) do not carry a receipt — the
// envelope's Error/Assignment fields are the proof there. The watch
// loop's `--dry-run` planner output does not emit per-bead receipts;
// only the live `--pane` dispatch path does.
// See ntm#128.
type DispatchReceipt struct {
	WorkItemID  string                  `json:"work_item_id"`
	Pane        DispatchPaneRef         `json:"pane"`
	Prompt      DispatchPromptInfo      `json:"prompt"`
	Reservation *DispatchReservation    `json:"reservation,omitempty"`
	Transport   DispatchTransportStatus `json:"transport"`
	Timestamp   string                  `json:"timestamp"`
	DryRun      bool                    `json:"dry_run,omitempty"`
}

// DispatchPaneRef identifies the pane the work was dispatched to.
type DispatchPaneRef struct {
	Session     string `json:"session"`
	Target      string `json:"target"`       // Canonical N or W.P topology address.
	WindowIndex int    `json:"window_index"` // Current tmux window index.
	Index       int    `json:"index"`        // Window-local pane index.
	ID          string `json:"id"`           // Exact tmux %N pane identity.
	Title       string `json:"title,omitempty"`
}

// DispatchPromptInfo summarizes the prompt that was sent. The hash is
// SHA-256 over the exact bytes; callers can match against their own
// hash to confirm wire fidelity.
type DispatchPromptInfo struct {
	Length     int    `json:"length"`
	HashSHA256 string `json:"hash_sha256"`
	Source     string `json:"source,omitempty"` // e.g. "persona://implementer"
}

// DispatchReservation summarizes the file-reservation outcome at
// dispatch time.
type DispatchReservation struct {
	Requested []string `json:"requested,omitempty"`
	Granted   []string `json:"granted,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// DispatchTransportStatus captures whether the prompt actually reached
// the pane via tmux send-keys.
type DispatchTransportStatus struct {
	Sent       bool   `json:"sent"`
	DeliveryID string `json:"delivery_id,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// makeDirectAssignEnvelope creates a standard assign envelope for direct pane assignment JSON output.
func makeDirectAssignEnvelope(session string, success bool, data *DirectAssignData, errCode, errMsg string, warnings []string) AssignEnvelope[DirectAssignData] {
	if warnings == nil {
		warnings = []string{}
	}
	envelope := AssignEnvelope[DirectAssignData]{
		Command:    "assign",
		Subcommand: "pane",
		Session:    session,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    success,
		Data:       data,
		Warnings:   warnings,
		Error:      nil,
	}
	if errCode != "" {
		envelope.Error = &AssignError{
			Code:    errCode,
			Message: errMsg,
		}
	}
	return envelope
}

func resolveDirectAssignmentPane(panes []tmux.Pane, selector string) (tmux.Pane, string, error) {
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{selector}, true)
	if err != nil {
		return tmux.Pane{}, "", err
	}
	pane := resolved[0]
	return pane, tmux.PaneTargetKey(pane, tmux.PanesSpanMultipleWindows(panes)), nil
}

func directPaneSelectorErrorCode(err error) string {
	var selectorErr *tmux.PaneSelectorError
	if !errors.As(err, &selectorErr) {
		return "PANE_NOT_FOUND"
	}
	switch selectorErr.Kind {
	case tmux.PaneSelectorAmbiguous:
		return "PANE_AMBIGUOUS"
	case tmux.PaneSelectorNotFound:
		return "PANE_NOT_FOUND"
	default:
		return "INVALID_ARGS"
	}
}

type directAssignmentIntent struct {
	idempotencyKey string
	promptSource   string
	explicitPrompt bool
}

func snapshotDirectAssignmentIntent(opts *AssignCommandOptions, beadID, paneID string, clearedGeneration ...uint64) directAssignmentIntent {
	intent := directAssignmentIntent{
		promptSource:   opts.Prompt,
		explicitPrompt: opts.Prompt != "",
	}
	if !intent.explicitPrompt {
		intent.promptSource = promptTemplateSource(opts.Template, opts.TemplateFile)
	}
	keyParts := []string{
		"ntm/direct-assign/v3",
		opts.Session,
		beadID,
		paneID,
		assignment.PromptSHA256(intent.promptSource),
		strings.ToLower(strings.TrimSpace(opts.Template)),
		strings.TrimSpace(opts.TemplateFile),
		fmt.Sprintf("force=%t", opts.Force),
		fmt.Sprintf("ignore-deps=%t", opts.IgnoreDeps),
		fmt.Sprintf("reserve-files=%t", opts.ReserveFiles),
	}
	if len(clearedGeneration) > 0 && clearedGeneration[0] > 0 {
		keyParts = append(keyParts, fmt.Sprintf("cleared-generation=%d", clearedGeneration[0]))
	}
	intent.idempotencyKey = assignment.AssignmentIdempotencyKey(keyParts...)
	return intent
}

func (intent directAssignmentIntent) prompt(beadID, beadTitle string) string {
	if intent.explicitPrompt {
		return intent.promptSource
	}
	return renderPromptTemplate(beadID, beadTitle, intent.promptSource)
}

func directAssignmentReservationManager(opts *AssignCommandOptions, projectDir string) (*assign.FileReservationManager, error) {
	if !opts.ReserveFiles {
		return nil, nil
	}
	amClient := newAgentMailClient(projectDir)
	if !amClient.IsAvailable() {
		return nil, assignment.ErrReservationRequired
	}
	manager := assign.NewFileReservationManager(amClient, projectDir)
	ttlSeconds := int(resolveAssignTimeout(opts.Timeout).Seconds()) * 2
	if ttlSeconds < 3600 {
		ttlSeconds = 3600
	}
	manager.SetTTL(ttlSeconds)
	return manager, nil
}

func directAssignmentActor(session, paneID string) string {
	return fmt.Sprintf("%s-pane-%s", session, strings.TrimPrefix(strings.TrimSpace(paneID), "%"))
}

// runDirectPaneAssignment handles the --pane flag for direct bead-to-pane assignment
func runDirectPaneAssignment(ctx context.Context, opts *AssignCommandOptions) error {
	if ctx == nil {
		err := errors.New("direct assignment context is required")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeInternalError, err.Error(), nil))
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("direct assignment canceled: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeTimeout, cancelErr.Error(), nil))
		}
		return cancelErr
	}
	var warnings []string

	// Validate: exactly one bead must be specified
	if len(opts.BeadIDs) != 1 {
		err := fmt.Errorf("--pane requires exactly one bead (use --beads=bd-xxx)")
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "INVALID_ARGS", err.Error(), nil))
		}
		return err
	}

	beadID := opts.BeadIDs[0]
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, opts.Session)
		if err != nil {
			err = fmt.Errorf("resolve bead project for atomic claim: %w", err)
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "PROJECT_ERROR", err.Error(), warnings))
			}
			return err
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, robot.ErrCodeInvalidFlag, err.Error(), warnings))
		}
		return err
	}

	// Get panes from tmux
	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		err = fmt.Errorf("failed to get panes: %w", err)
		if IsJSONOutput() {
			code := "TMUX_ERROR"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, code, err.Error(), nil))
		}
		return err
	}

	targetPane, canonicalTarget, err := resolveDirectAssignmentPane(panes, opts.PaneSelector)
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, directPaneSelectorErrorCode(err), err.Error(), nil))
		}
		return err
	}
	store, err := assignment.LoadStoreStrict(opts.Session)
	if err != nil {
		err = fmt.Errorf("load assignment store: %w", err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "STORE_ERROR", err.Error(), warnings))
		}
		return err
	}
	intent := snapshotDirectAssignmentIntent(opts, beadID, targetPane.ID, store.ClearedGeneration(beadID))
	idempotencyKey := intent.idempotencyKey

	prior := store.Get(beadID)
	if prior != nil && prior.IdempotencyKey == idempotencyKey {
		switch prior.Status {
		case assignment.StatusCompleted, assignment.StatusFailed, assignment.StatusReassigned:
			idempotencyKey, err = assignment.NewAssignmentIdempotencyKey()
			if err != nil {
				err = fmt.Errorf("generate reopened assignment identity: %w", err)
				if IsJSONOutput() {
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "STORE_ERROR", err.Error(), warnings))
				}
				return err
			}
			intent.idempotencyKey = idempotencyKey
		}
	}
	sameIntent := prior != nil && prior.IdempotencyKey == idempotencyKey
	activeDifferentIntent := false
	if prior != nil && prior.IdempotencyKey != "" && !sameIntent {
		switch prior.Status {
		case assignment.StatusClaiming, assignment.StatusClaimed, assignment.StatusAssigned, assignment.StatusWorking:
			activeDifferentIntent = true
		}
	}
	executeBeforePreflight := sameIntent || activeDifferentIntent

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	agentType := agentTypeForPane(targetPane)
	agentName := assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, agentType, targetPane, multiWindow)
	beadTitle := ""
	if executeBeforePreflight {
		beadTitle = prior.BeadTitle
		if sameIntent {
			if prior.AgentType != "" {
				agentType = prior.AgentType
			}
			if prior.AgentName != "" {
				agentName = prior.AgentName
			}
		}
	}
	prompt := intent.prompt(beadID, beadTitle)

	// Build assignment item
	assignItem := &DirectAssignItem{
		BeadID:      beadID,
		BeadTitle:   "",
		Pane:        targetPane.Index,
		PaneTarget:  canonicalTarget,
		PaneID:      targetPane.ID,
		AgentType:   agentType,
		Status:      "planned",
		DepsIgnored: opts.IgnoreDeps,
	}

	if !executeBeforePreflight {
		if agentType == "user" || agentType == "unknown" {
			err = fmt.Errorf("pane %s is not an agent pane (type: %s)", canonicalTarget, agentType)
			if IsJSONOutput() {
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "NOT_AGENT_PANE", err.Error(), nil))
			}
			return err
		}

		observation, observationErr := observeAssignSessionWithTimeout(ctx, opts.Session, opts.Timeout)
		if observationErr != nil {
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitTypedAssignmentFailure(observationErr, makeDirectAssignEnvelope(
					opts.Session, false, data, assignmentObservationFailureCode(observationErr), observationErr.Error(), nil,
				))
			}
			return observationErr
		}
		paneObservation, observationErr := currentAssignPaneObservation(observation, targetPane.ID, time.Now())
		if observationErr != nil {
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitTypedAssignmentFailure(observationErr, makeDirectAssignEnvelope(
					opts.Session, false, data, assignmentObservationFailureCode(observationErr), observationErr.Error(), nil,
				))
			}
			return observationErr
		}
		state := string(paneObservation.Current.Status.State)
		if !paneObservation.SafeToDispatch() && !opts.Force {
			assignItem.PaneWasBusy = true
			errMsg := fmt.Sprintf("pane %s is busy (state: %s), use --force to override", canonicalTarget, state)
			if IsJSONOutput() {
				data := &DirectAssignData{Assignment: assignItem}
				return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "PANE_BUSY", errMsg, nil))
			}
			return errors.New(errMsg)
		}
		assignItem.PaneWasBusy = !paneObservation.SafeToDispatch()

		if !opts.IgnoreDeps {
			blockers, blockersErr := getBeadBlockers(ctx, projectDir, beadID)
			if blockersErr != nil {
				errMsg := fmt.Sprintf("could not verify dependencies for %s: %v; use --ignore-deps to override", beadID, blockersErr)
				if IsJSONOutput() {
					data := &DirectAssignData{Assignment: assignItem}
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "DEPENDENCY_LOOKUP_FAILED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
			if len(blockers) > 0 {
				assignItem.BlockedByIDs = blockers
				errMsg := fmt.Sprintf("bead %s is blocked by: %v, use --ignore-deps to override", beadID, blockers)
				if IsJSONOutput() {
					data := &DirectAssignData{Assignment: assignItem}
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, "BLOCKED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
		}

		if opts.Prompt == "" || opts.ReserveFiles {
			beadTitle, err = getBeadTitle(ctx, projectDir, beadID)
			if err != nil {
				errMsg := fmt.Sprintf("look up bead %s title: %v", beadID, err)
				if IsJSONOutput() {
					return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, &DirectAssignData{Assignment: assignItem}, "BEAD_LOOKUP_FAILED", errMsg, nil))
				}
				return errors.New(errMsg)
			}
		}
		prompt = intent.prompt(beadID, beadTitle)
	}

	request := assignment.AtomicRequest{
		BeadID:                    beadID,
		BeadTitle:                 beadTitle,
		Target:                    targetPane.ID,
		OccupancyKey:              targetPane.ID,
		Pane:                      targetPane.Index,
		AgentType:                 agentType,
		AgentName:                 agentName,
		Actor:                     directAssignmentActor(opts.Session, targetPane.ID),
		Prompt:                    prompt,
		IdempotencyKey:            idempotencyKey,
		RequireReservation:        opts.ReserveFiles,
		AllowReservationDiscovery: opts.ReserveFiles,
		ReservationTTL:            time.Hour,
	}
	if sameIntent {
		request.Pane = prior.Pane
		request.RecoveredIntentSHA256 = strings.TrimSpace(prior.IntentSHA256)
		if request.RecoveredIntentSHA256 == "" {
			request.RecoveredIntentSHA256 = strings.TrimSpace(prior.PromptSHA256)
		}
		if prior.OccupancyKey != "" {
			request.OccupancyKey = prior.OccupancyKey
			request.Target = prior.OccupancyKey
		} else if prior.DispatchTarget != "" {
			request.Target = prior.DispatchTarget
		}
	}

	var reservationMgr *assign.FileReservationManager
	needsReservationPort := !executeBeforePreflight || (sameIntent && prior.DispatchState != assignment.DispatchSent && prior.DispatchState != assignment.DispatchSending)
	if needsReservationPort {
		reservationMgr, err = directAssignmentReservationManager(opts, projectDir)
	}
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, nil, "RESERVATION_REQUIRED", err.Error(), warnings))
		}
		return err
	}
	atomicCoordinator := newCLIAtomicAssignmentCoordinator(store, projectDir, reservationMgr, opts.Force)
	assignmentCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(opts.Timeout))
	atomicResult, assignErr := atomicCoordinator.Execute(assignmentCtx, request)
	cancel()
	if err := ctx.Err(); err != nil {
		cancelErr := fmt.Errorf("direct assignment canceled while assigning %s: %w", beadID, err)
		if IsJSONOutput() {
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(
				opts.Session, false, &DirectAssignData{Assignment: assignItem}, robot.ErrCodeTimeout, cancelErr.Error(), warnings,
			))
		}
		return cancelErr
	}
	assignItem.PromptSent = atomicResult.Sent
	durablePrompt := ""
	durableAssignment := atomicResult.Assignment
	if durableAssignment != nil && (durableAssignment.BeadID != beadID || durableAssignment.IdempotencyKey != idempotencyKey) {
		// ErrTargetOccupied returns the authoritative owner so callers can
		// diagnose the conflict. That row is not the requested assignment and
		// must never be projected as its prompt, lease, receipt, or pane state.
		durableAssignment = nil
	}
	if durableAssignment != nil {
		assignItem.BeadTitle = forcedRedactedAssignmentText(durableAssignment.BeadTitle)
		assignItem.Pane = durableAssignment.Pane
		assignItem.AgentType = durableAssignment.AgentType
		assignItem.Status = string(durableAssignment.Status)
		assignItem.AssignedAt = durableAssignment.AssignedAt.UTC().Format(time.RFC3339)
		if durableAssignment.OccupancyKey != "" {
			assignItem.PaneID = durableAssignment.OccupancyKey
		}
		durablePrompt = durableAssignment.PromptSent
		if durablePrompt == "" {
			durablePrompt = durableAssignment.PendingPrompt
		}
	}
	assignItem.Prompt = forcedRedactedAssignmentText(durablePrompt)

	leaseRequested := atomicResult.Lease.Requested
	leaseGranted := atomicResult.Lease.Granted
	if durableAssignment != nil && len(leaseRequested) == 0 && len(leaseGranted) == 0 {
		leaseRequested = durableAssignment.ReservationRequested
		leaseGranted = durableAssignment.ReservedPaths
	}
	var fileReservations *DirectAssignFileReservations
	if len(leaseRequested) > 0 || len(leaseGranted) > 0 {
		grantedSet := make(map[string]struct{}, len(leaseGranted))
		for _, path := range leaseGranted {
			grantedSet[path] = struct{}{}
		}
		var denied []string
		for _, path := range leaseRequested {
			if _, ok := grantedSet[path]; !ok {
				denied = append(denied, path)
			}
		}
		fileReservations = &DirectAssignFileReservations{
			Requested: leaseRequested,
			Granted:   leaseGranted,
			Denied:    denied,
		}
	}
	var receipt *DispatchReceipt
	attemptedThisCall := prior == nil || prior.IdempotencyKey == "" || (sameIntent && prior.DispatchAttempts == 0)
	if durableAssignment != nil && durableAssignment.DispatchAttempts > 0 &&
		(atomicResult.Replayed || atomicResult.Sent || attemptedThisCall) {
		receipt = buildDispatchReceipt(
			opts.Session, beadID, targetPane, assignItem.PaneTarget, durablePrompt, opts.Template, fileReservations,
			atomicResult.Dispatch.DeliveryID, atomicResult.Sent, assignErr, atomicResult.Dispatch.Duration.Milliseconds(), atomicResult.Assignment.DispatchedAt, false,
		)
	}
	if assignErr != nil {
		code := "ASSIGN_ERROR"
		switch {
		case errors.Is(assignErr, context.Canceled), errors.Is(assignErr, context.DeadlineExceeded):
			code = robot.ErrCodeTimeout
		case errors.Is(assignErr, assignment.ErrClaimIneligible):
			code = "BEAD_INELIGIBLE"
		case errors.Is(assignErr, assignment.ErrClaimConflict):
			code = "CLAIM_CONFLICT"
		case errors.Is(assignErr, assignment.ErrPaneIdentityMigrationRequired):
			code = "PANE_IDENTITY_MIGRATION_REQUIRED"
		case errors.Is(assignErr, assignment.ErrTargetOccupied):
			code = "TARGET_BUSY"
		case errors.Is(assignErr, assignment.ErrTerminalAssignmentAttempt):
			code = "BEAD_NOT_REOPENED"
		case errors.Is(assignErr, assignment.ErrDispatchOutcomeUnknown):
			code = "DISPATCH_UNKNOWN"
		case durableAssignment != nil && durableAssignment.DispatchAttempts > 0:
			code = "SEND_ERROR"
		}
		if IsJSONOutput() {
			data := &DirectAssignData{Assignment: assignItem, FileReservations: fileReservations, Receipt: receipt}
			return emitJSONFailureEnvelope(makeDirectAssignEnvelope(opts.Session, false, data, code, assignErr.Error(), warnings))
		}
		return assignErr
	}

	// Output result
	if IsJSONOutput() {
		data := &DirectAssignData{
			Assignment:       assignItem,
			FileReservations: fileReservations,
			Receipt:          receipt,
		}
		return json.NewEncoder(os.Stdout).Encode(makeDirectAssignEnvelope(opts.Session, true, data, "", "", warnings))
	}

	// Text output
	if !opts.Quiet {
		fmt.Printf("✓ Assigned %s to pane %s (%s)\n", beadID, assignItem.PaneTarget, agentType)
		if assignItem.BeadTitle != "" {
			fmt.Printf("  Title: %s\n", assignItem.BeadTitle)
		}
		if assignItem.PaneWasBusy {
			fmt.Printf("  Note: Pane was busy (--force used)\n")
		}
		if assignItem.DepsIgnored {
			fmt.Printf("  Note: Dependencies ignored (--ignore-deps used)\n")
		}
		if fileReservations != nil && len(fileReservations.Granted) > 0 {
			fmt.Printf("  Reserved: %v\n", fileReservations.Granted)
		}
		fmt.Printf("  Prompt: %s\n", assignItem.Prompt)
	}

	return nil
}

// getBeadBlockers returns the list of beads blocking the given bead
func getBeadBlockers(ctx context.Context, projectDir, beadID string) ([]string, error) {
	details, err := bv.GetBeadAssignmentDetailsContext(ctx, projectDir, beadID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), details.BlockedBy...), nil
}

// getBeadTitle retrieves the title for a bead
func getBeadTitle(ctx context.Context, projectDir, beadID string) (string, error) {
	details, err := bv.GetBeadAssignmentDetailsContext(ctx, projectDir, beadID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(details.Title) == "" {
		return "", fmt.Errorf("br show returned an empty title for %s", beadID)
	}
	return details.Title, nil
}

// ============================================================================
// Auto-Reassignment Logic
// ============================================================================

// AutoReassignOptions contains options for auto-reassignment
type AutoReassignOptions struct {
	Session         string
	ProjectDir      string
	Strategy        string
	Template        string
	TemplateFile    string
	ReserveFiles    bool
	Verbose         bool
	Quiet           bool
	Timeout         time.Duration
	AgentTypeFilter string
	// DryRun, when true, computes assignments and reports them but never
	// dispatches to live panes. Honored by `--watch --dry-run` (issue #122).
	DryRun bool
	// IdleThreshold is the watch-loop inactivity window before an in-flight
	// assignment is stamped failed and its pane freed for new work (GH#238).
	// Zero means config.DefaultAssignIdleThreshold. A too-small value falsely
	// fails quietly-working agents (silent while an external subprocess runs)
	// and then injects the next bead's prompt into them mid-task.
	IdleThreshold time.Duration

	// policyProject records the exact authoritative project whose assignment
	// policy was already installed during this command path.
	policyProject string
}

// AutoReassignResult contains the result of an auto-reassignment operation
type AutoReassignResult struct {
	TriggerBeadID  string           `json:"trigger_bead_id"`
	NewlyUnblocked []UnblockedBead  `json:"newly_unblocked"`
	Assignments    []AssignmentItem `json:"assignments"`
	Skipped        []SkippedItem    `json:"skipped"`
	IdleAgents     int              `json:"idle_agents"`
	Errors         []string         `json:"errors,omitempty"`
	CyclesDetected [][]string       `json:"cycles_detected,omitempty"`
	CompletionTime time.Time        `json:"completion_time"`
}

// PerformAutoReassignment handles automatic reassignment when a bead completes.
// This is the main entry point for dependency-aware auto-reassignment.
// It:
// 1. Detects newly unblocked beads after the completion
// 2. Finds idle agents that can take new work
// 3. Assigns unblocked beads to idle agents using the specified strategy
// 4. Handles file reservations and prompt generation
func PerformAutoReassignment(ctx context.Context, completedBeadID string, opts *AutoReassignOptions) (*AutoReassignResult, error) {
	if ctx == nil {
		return nil, errors.New("auto-reassignment context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("auto-reassignment canceled: %w", err)
	}
	if opts == nil {
		return nil, errors.New("auto-reassignment options are required")
	}
	result := &AutoReassignResult{
		TriggerBeadID:  completedBeadID,
		CompletionTime: time.Now(),
		Assignments:    make([]AssignmentItem, 0),
		Skipped:        make([]SkippedItem, 0),
	}

	if assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Auto-reassignment triggered by completion of %s\n", completedBeadID)
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, opts.Session)
		if err != nil {
			return nil, fmt.Errorf("resolve auto-reassignment project: %w", err)
		}
		opts.ProjectDir = projectDir
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &opts.policyProject); err != nil {
		return nil, err
	}
	// Step 1: Get newly unblocked beads
	depResult, err := GetNewlyUnblockedBeads(ctx, projectDir, completedBeadID, opts.Verbose)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, fmt.Errorf("auto-reassignment canceled while checking newly unblocked work: %w", ctxErr)
	}
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to check unblocked beads: %v", err))
		return result, nil // Return partial result, not error
	}

	result.NewlyUnblocked = depResult.NewlyUnblocked
	result.CyclesDetected = depResult.CyclesDetected

	if len(depResult.Errors) > 0 {
		result.Errors = append(result.Errors, depResult.Errors...)
	}

	if len(result.NewlyUnblocked) == 0 {
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No beads were unblocked by completion of %s\n", completedBeadID)
		}
		return result, nil
	}

	verifiedActionable, err := getActionableRecommendationsForWatch(ctx, projectDir, 0)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("auto-reassignment canceled while verifying actionable work: %w", ctxErr)
		}
		return result, fmt.Errorf("automated assignment stopped because newly unblocked work could not be verified: %w", err)
	}
	activeAssignments, err := loadActiveAssignmentBeadIDs(opts.Session)
	if err != nil {
		return result, err
	}
	result.NewlyUnblocked, result.Skipped = filterNewlyUnblockedByVerifiedPlanForProject(
		projectDir,
		result.NewlyUnblocked,
		verifiedActionable,
		activeAssignments,
		result.Skipped,
	)
	if len(result.NewlyUnblocked) == 0 {
		return result, nil
	}

	if assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Found %d newly unblocked beads: %v\n",
			len(result.NewlyUnblocked),
			func() []string {
				var ids []string
				for _, ub := range result.NewlyUnblocked {
					ids = append(ids, ub.ID)
				}
				return ids
			}())
	}

	// Step 2: Get idle agents
	idleAgents, err := getIdleAgents(ctx, opts.Session, opts.AgentTypeFilter, opts.Verbose)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to get idle agents: %v", err))
		return result, nil
	}

	result.IdleAgents = len(idleAgents)

	if len(idleAgents) == 0 {
		// No idle agents - mark all unblocked beads as skipped
		for _, unblocked := range result.NewlyUnblocked {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: unblocked.ID,
				Reason: "no_idle_agents",
			})
		}
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No idle agents available for reassignment\n")
		}
		return result, nil
	}

	// Step 3: Convert unblocked beads to BeadPreview format for assignment
	var unblockedBeads []bv.BeadPreview
	for _, unblocked := range result.NewlyUnblocked {
		unblockedBeads = append(unblockedBeads, bv.BeadPreview{
			ID:       unblocked.ID,
			Title:    unblocked.Title,
			Priority: fmt.Sprintf("P%d", unblocked.Priority),
		})
	}

	// Step 4: Filter out beads in dependency cycles
	filteredBeads, excluded, err := FilterCyclicBeads(ctx, projectDir, unblockedBeads, opts.Verbose)
	if err != nil {
		return result, fmt.Errorf("filter auto-reassignment dependency cycles: %w", err)
	}
	for i := 0; i < excluded; i++ {
		// Find the excluded bead to add to skipped
		for _, unblocked := range result.NewlyUnblocked {
			if IsBeadInCycle(unblocked.ID, result.CyclesDetected) {
				result.Skipped = append(result.Skipped, SkippedItem{
					BeadID: unblocked.ID,
					Reason: "in_dependency_cycle",
				})
				break
			}
		}
	}

	if len(filteredBeads) == 0 {
		if assignHumanDiagnostics(opts.Verbose) {
			fmt.Fprintf(os.Stderr, "[AUTO] No assignable beads after filtering cycles\n")
		}
		return result, nil
	}

	// Step 5: Generate assignments using strategy
	assignOpts := &AssignCommandOptions{
		Session:         opts.Session,
		ProjectDir:      projectDir,
		Strategy:        opts.Strategy,
		Template:        opts.Template,
		TemplateFile:    opts.TemplateFile,
		AgentTypeFilter: opts.AgentTypeFilter,
		Verbose:         opts.Verbose,
		Quiet:           opts.Quiet,
		Timeout:         opts.Timeout,
		ReserveFiles:    opts.ReserveFiles,
		policyProject:   opts.policyProject,
	}

	idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, assignOpts.Template)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.IdleAgents = len(idleAgents)

	assignments := generateAssignmentsEnhanced(ctx, idleAgents, filteredBeads, assignOpts)
	result.Assignments = assignments

	// Step 6: Execute assignments (skipped under --dry-run; the planner output
	// is still returned so callers can preview what would have been dispatched).
	if len(assignments) > 0 && !opts.DryRun {
		// Create a mock enhanced output for execution
		enhancedOut := &AssignOutputEnhanced{
			Strategy:    opts.Strategy,
			Assignments: assignments,
			Skipped:     result.Skipped,
		}

		if err := executeAssignmentsEnhanced(ctx, opts.Session, enhancedOut, assignOpts); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, fmt.Errorf("auto-reassignment canceled during assignment execution: %w", ctxErr)
			}
			result.Errors = append(result.Errors, fmt.Sprintf("failed to execute assignments: %v", err))
		} else {
			if assignHumanDiagnostics(opts.Verbose) {
				fmt.Fprintf(os.Stderr, "[AUTO] Successfully assigned %d unblocked beads\n", len(assignments))
			}
		}
	} else if len(assignments) > 0 && opts.DryRun && assignHumanDiagnostics(opts.Verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Dry-run: would assign %d unblocked beads (no dispatch)\n", len(assignments))
	}

	return result, nil
}

func filterNewlyUnblockedByVerifiedPlanForProject(
	projectDir string,
	newlyUnblocked []UnblockedBead,
	verifiedActionable []bv.TriageRecommendation,
	activeAssignments map[string]struct{},
	skipped []SkippedItem,
) ([]UnblockedBead, []SkippedItem) {
	return filterNewlyUnblockedByVerifiedPlanWithGate(newlyUnblocked, verifiedActionable, activeAssignments, skipped, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func filterNewlyUnblockedByVerifiedPlanWithGate(
	newlyUnblocked []UnblockedBead,
	verifiedActionable []bv.TriageRecommendation,
	activeAssignments map[string]struct{},
	skipped []SkippedItem,
	operatorGated func(string) bool,
) ([]UnblockedBead, []SkippedItem) {
	verifiedByID := make(map[string]bv.TriageRecommendation, len(verifiedActionable))
	for _, recommendation := range verifiedActionable {
		id := strings.TrimSpace(recommendation.ID)
		if id == "" {
			continue
		}
		recommendation.ID = id
		verifiedByID[id] = recommendation
	}

	authorized := make([]UnblockedBead, 0, len(newlyUnblocked))
	for _, candidate := range newlyUnblocked {
		id := strings.TrimSpace(candidate.ID)
		recommendation, verified := verifiedByID[id]
		if !verified {
			skipped = append(skipped, SkippedItem{
				BeadID:    id,
				BeadTitle: candidate.Title,
				Reason:    "not_in_actionable_plan",
			})
			continue
		}
		if skip := classifyTriageRecForAssignmentWithGate(recommendation, activeAssignments, operatorGated); skip != nil {
			skipped = append(skipped, *skip)
			continue
		}
		candidate.ID = id
		candidate.Title = recommendation.Title
		candidate.Priority = recommendation.Priority
		authorized = append(authorized, candidate)
	}
	return authorized, skipped
}

// getIdleAgents returns a list of idle agents that can take new assignments
func getIdleAgents(ctx context.Context, session, agentTypeFilter string, verbose bool) ([]assignAgentInfo, error) {
	if ctx == nil {
		return nil, errors.New("idle-agent observation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("idle-agent observation canceled: %w", err)
	}
	normalizedFilter := normalizeAgentTypeAlias(agentTypeFilter)
	// normalizedFilter == "" means "no provider filter" (raw was "", any, all, *).
	// Reject only the sentinel values robot.ResolveAgentType emits for unknown
	// or non-agent inputs.
	if agentTypeFilter != "" && (normalizedFilter == "unknown" || normalizedFilter == "user") {
		return nil, fmt.Errorf("invalid agent type filter %q", agentTypeFilter)
	}

	// Get panes from tmux
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}
	observationCtx, cancel := context.WithTimeout(ctx, resolveAssignTimeout(assignTimeout))
	defer cancel()
	observation, err := observeAssignSession(observationCtx, session)
	if err != nil {
		return nil, err
	}

	var idleAgents []assignAgentInfo

	// Panes holding an active assignment must be excluded from the idle pool
	// even if they momentarily show an idle prompt between turns (FIX C).
	activePanes, err := loadActiveAssignmentPanes(session)
	if err != nil {
		return nil, err
	}

	for _, pane := range panes {
		agentType := agentTypeForPane(pane)
		if agentType == "user" || agentType == "unknown" {
			continue
		}

		// Apply agent type filter
		if normalizedFilter != "" && agentType != normalizedFilter {
			continue
		}

		// Skip panes that already hold an active assignment.
		if assignmentPaneIsActive(activePanes, pane) {
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[AUTO] Skipping pane %d (%s): holds an active assignment\n", pane.Index, agentType)
			}
			continue
		}

		paneObservation, observationErr := currentAssignPaneObservation(observation, pane.ID, time.Now())
		if observationErr != nil {
			return nil, observationErr
		}
		model := detectModelFromTitle(agentType, pane.Title)
		state := string(paneObservation.Current.Status.State)

		if paneObservation.SafeToDispatch() {
			idleAgents = append(idleAgents, assignAgentInfo{
				pane:       pane,
				agentType:  agentType,
				model:      model,
				state:      state,
				scrollback: paneObservation.RawOutput,
			})
			if assignHumanDiagnostics(verbose) {
				fmt.Fprintf(os.Stderr, "[AUTO] Found idle agent: pane %d (%s)\n", pane.Index, agentType)
			}
		}
	}

	if assignHumanDiagnostics(verbose) {
		fmt.Fprintf(os.Stderr, "[AUTO] Total idle agents available: %d\n", len(idleAgents))
	}

	return idleAgents, nil
}

// TriggerCompletionCheck manually triggers a completion check and auto-reassignment.
// This can be called from external completion notifications or manual triggers.
var performAutoReassignmentForTrigger = PerformAutoReassignment

func reconcilePendingTerminalAssignment(ctx context.Context, store *assignment.AssignmentStore, session string, current *assignment.Assignment) (bool, error) {
	if store == nil || current == nil {
		return false, errors.New("pending terminal assignment is required")
	}
	if current.PendingTerminalStatus != assignment.StatusCompleted && current.PendingTerminalStatus != assignment.StatusFailed {
		return false, nil
	}
	desiredStatus := current.PendingTerminalStatus
	desiredReason := current.PendingTerminalReason
	cleanupUnlock, err := store.AcquireExternalCleanupLock(ctx, current.BeadID)
	if err != nil {
		return false, fmt.Errorf("lock terminal external cleanup %s: %w", current.BeadID, err)
	}
	defer cleanupUnlock()
	if err := store.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh terminal external cleanup %s: %w", current.BeadID, err)
	}
	refreshed := store.Get(current.BeadID)
	if refreshed == nil {
		return true, nil
	}
	if !assignment.SameAssignmentGeneration(current, refreshed) {
		return false, nil
	}
	if refreshed.PendingTerminalStatus != desiredStatus || refreshed.PendingTerminalReason != desiredReason {
		return terminalReconciliationAlreadyCompleted(refreshed, desiredStatus, desiredReason), nil
	}
	current = refreshed
	if current.ClearState == assignment.ClearStateReservationReleasing {
		if _, err := releaseAssignmentLeases(ctx, session, current); err != nil {
			if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, err); persistErr != nil {
				return false, errors.Join(err, fmt.Errorf("persist terminal reservation-release failure: %w", persistErr))
			}
			return false, err
		}
		var err error
		current, err = store.RecordClearLeasesReleased(ctx, current.BeadID)
		if err != nil {
			return false, fmt.Errorf("persist terminal reservation-release checkpoint: %w", err)
		}
	}
	if current.ClearState != assignment.ClearStateLeasesReleased {
		return false, fmt.Errorf("assignment %s is not ready for terminal claim release", current.BeadID)
	}
	if !current.TerminalClaimReleased {
		if strings.TrimSpace(current.ClaimActor) != "" {
			projectDir, err := resolveAssignProjectDir(ctx, session)
			if err != nil {
				claimErr := fmt.Errorf("resolve project for terminal Beads claim release: %w", err)
				if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
					return false, errors.Join(claimErr, fmt.Errorf("persist terminal claim-release failure: %w", persistErr))
				}
				return false, claimErr
			}
			if _, err := releaseBeadClaimForAssignment(ctx, projectDir, current.BeadID, current.ClaimActor); err != nil {
				claimErr := fmt.Errorf("release terminal Beads claim for %s: %w", current.BeadID, err)
				if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, claimErr); persistErr != nil {
					return false, errors.Join(claimErr, fmt.Errorf("persist terminal claim-release failure: %w", persistErr))
				}
				return false, claimErr
			}
		}
		var err error
		current, err = store.RecordTerminalClaimReleased(ctx, current.BeadID)
		if err != nil {
			return false, fmt.Errorf("persist terminal claim-release checkpoint: %w", err)
		}
	}
	if err := store.CompleteTerminalReconciliation(ctx, current.BeadID, current.PendingTerminalStatus, current.PendingTerminalReason); err != nil {
		if persistErr := store.RecordClearReleaseFailed(ctx, current.BeadID, err); persistErr != nil {
			return false, errors.Join(err, fmt.Errorf("persist terminal completion failure: %w", persistErr))
		}
		return false, err
	}
	return true, nil
}

func terminalReconciliationAlreadyCompleted(current *assignment.Assignment, status assignment.AssignmentStatus, reason string) bool {
	if current == nil || current.ClearState != assignment.ClearStateNone || current.PendingTerminalStatus != "" || current.Status != status {
		return false
	}
	if status == assignment.StatusFailed {
		return current.FailReason == reason
	}
	return status == assignment.StatusCompleted
}

// WatchLoop manages the continuous auto-assignment watch mode
type WatchLoop struct {
	session  string
	strategy string
	store    *assignment.AssignmentStore
	detector *completion.CompletionDetector
	opts     *AutoReassignOptions

	// Configuration
	stopWhenDone bool
	delay        time.Duration
	limit        int
	quiet        bool
	verbose      bool
	// idleThreshold is the completion-detector inactivity window before an
	// in-flight assignment is stamped failed (GH#238). Zero falls back to
	// config.DefaultAssignIdleThreshold in Run.
	idleThreshold time.Duration

	// Periodic ready-work scan (FIX B). The watch loop is otherwise purely
	// completion-driven: dispatch happens only inside handleCompletion, which
	// fires only on a completion event for a bead THIS loop dispatched. If the
	// initial pass dispatched nothing (no idle agents OR no ready work at
	// startup), no completion event ever fires and the loop sits inert forever
	// even as ready work later appears (a gate unblocks, beads are created,
	// startup-busy agents go idle). scanInterval drives a ticker that re-runs
	// the SAME plan/dispatch pass the initial assignment uses. It is idempotent
	// — the idle pool excludes busy panes and panes holding an active
	// assignment (FIX C), so a re-scan never double-dispatches in-flight work.
	scanInterval time.Duration
	scanOpts     *AssignCommandOptions
	// scanFn performs one ready-work scan pass. Defaults to scanReadyWork;
	// overridable in tests to observe ticker-driven scans without tmux/bv.
	scanFn func(context.Context) error

	// Concurrency control
	completionCh            chan completion.CompletionEvent
	stopCh                  chan struct{}
	stopOnce                sync.Once
	wg                      sync.WaitGroup
	lifecycleMu             sync.Mutex
	runStarted              bool
	runDone                 chan struct{}
	handledCompletionEvents map[string]struct{}
	handleCompletionFn      func(context.Context, completion.CompletionEvent) error
	ackCompletionEventFn    func(context.Context, string, string, string) (bool, error)
	renewCompletionEventFn  func(context.Context, string, string, string, time.Duration) (bool, error)

	// Statistics
	mu               sync.Mutex
	totalAssigned    int
	totalCompleted   int
	totalFailed      int
	startTime        time.Time
	lastAssignmentAt time.Time
}

// NewWatchLoop creates a new watch loop for a session
func NewWatchLoop(session string, store *assignment.AssignmentStore, opts *AutoReassignOptions) *WatchLoop {
	// The periodic ready-work scan re-runs the same plan/dispatch pass the
	// initial assignment uses. Default its cadence to the configured watch
	// interval (the same cadence the completion detector polls at) with a
	// sane fallback so a zero value can't spin a hot loop.
	scanInterval := assignWatchInterval
	if scanInterval <= 0 {
		scanInterval = 30 * time.Second
	}

	return &WatchLoop{
		session:       session,
		strategy:      opts.Strategy,
		store:         store,
		opts:          opts,
		stopWhenDone:  assignStopWhenDone,
		delay:         assignDelay,
		limit:         assignLimit,
		quiet:         opts.Quiet,
		verbose:       opts.Verbose,
		idleThreshold: opts.IdleThreshold,
		scanInterval:  scanInterval,
		scanOpts: &AssignCommandOptions{
			Session:         session,
			ProjectDir:      opts.ProjectDir,
			Strategy:        opts.Strategy,
			Limit:           assignLimit,
			AgentTypeFilter: opts.AgentTypeFilter,
			Template:        opts.Template,
			TemplateFile:    opts.TemplateFile,
			Verbose:         opts.Verbose,
			Quiet:           opts.Quiet,
			Timeout:         opts.Timeout,
			ReserveFiles:    opts.ReserveFiles,
			policyProject:   opts.policyProject,
		},
		stopCh:                  make(chan struct{}),
		runDone:                 make(chan struct{}),
		startTime:               time.Now(),
		handledCompletionEvents: make(map[string]struct{}),
	}
}

// logf prints a timestamped log message
func (w *WatchLoop) logf(format string, args ...interface{}) {
	if w.quiet {
		return
	}
	timestamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] %s\n", timestamp, msg)
}

// Run starts the watch loop and blocks until stopped
func (w *WatchLoop) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("watch-loop context is required")
	}
	w.lifecycleMu.Lock()
	if w.runStarted {
		w.lifecycleMu.Unlock()
		return errors.New("watch loop is already running")
	}
	w.runStarted = true
	if w.runDone == nil {
		w.runDone = make(chan struct{})
	}
	runDone := w.runDone
	w.lifecycleMu.Unlock()
	defer close(runDone)

	// Create completion detector. The idle threshold deliberately defaults to
	// config.DefaultAssignIdleThreshold (15m), NOT the detector package's
	// historical 120s: a healthy agent waiting on an external subprocess
	// (multi-model review CLIs, long builds, SSH remote verification) emits no
	// pane output for many minutes, and a false "failed" here frees the pane
	// and lets the next ready-work scan inject a NEW prompt into the
	// still-working agent mid-task (GH#238). Tune via [assign] idle_threshold
	// or --idle-threshold.
	idleThreshold := w.idleThreshold
	if idleThreshold <= 0 {
		idleThreshold = config.DefaultAssignIdleThreshold
	}
	detectorCfg := completion.DetectionConfig{
		PollInterval:      assignWatchInterval,
		IdleThreshold:     idleThreshold,
		RetryOnError:      true,
		RetryInterval:     10 * time.Second,
		MaxRetries:        3,
		DedupWindow:       5 * time.Second,
		GracefulDegrading: true,
		CaptureLines:      50,
	}
	w.detector = completion.NewWithConfig(w.session, w.store, detectorCfg)
	if err := w.detector.InitializationError(); err != nil {
		return fmt.Errorf("initialize completion detector: %w", err)
	}
	w.detector.SetTerminalReconciler(func(ctx context.Context, current *assignment.Assignment) (bool, error) {
		return reconcilePendingTerminalAssignment(ctx, w.store, w.session, current)
	})

	// Start watching for completions
	watchCtx, watchCancel := context.WithCancel(ctx)

	w.completionCh = make(chan completion.CompletionEvent, 10)
	eventsCh := w.detector.Watch(watchCtx)

	// Forward events to our channel (allows select with other channels)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(w.completionCh)
		for {
			select {
			case event, ok := <-eventsCh:
				if !ok {
					return
				}
				select {
				case w.completionCh <- event:
				case <-watchCtx.Done():
					return
				case <-w.stopCh:
					return
				}
			case <-watchCtx.Done():
				return
			case <-w.stopCh:
				return
			}
		}
	}()
	var scanWG sync.WaitGroup
	defer func() {
		watchCancel()
		w.wg.Wait()
		scanWG.Wait()
	}()

	w.logf("Starting watch mode with strategy=%s", w.strategy)

	// Periodic ready-work scan (FIX B). Without this the loop only ever
	// dispatches in reaction to a completion event for a bead it previously
	// dispatched — so a startup with nothing to dispatch (no idle agents OR no
	// ready work) leaves it inert forever even as work later becomes ready. The
	// ticker re-runs the same idempotent plan/dispatch pass as the initial
	// assignment; FIX C's idle-pool guards keep it from double-dispatching.
	scanInterval := w.scanInterval
	if scanInterval <= 0 {
		scanInterval = 30 * time.Second
	}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	scanDone := make(chan error, 1)
	scanInFlight := false

	// Main watch loop
	for {
		select {
		case event, ok := <-w.completionCh:
			if !ok {
				if err := watchCtx.Err(); err != nil {
					return err
				}
				return nil
			}

			if err := w.consumeCompletionEvent(watchCtx, event); err != nil {
				if watchCtx.Err() != nil {
					return watchCtx.Err()
				}
				w.logf("Error handling completion: %v", err)
			}

			// Check stop-when-done condition
			if w.stopWhenDone {
				stop, stopErr := w.shouldStop(watchCtx)
				if stopErr != nil {
					return fmt.Errorf("check watch completion: %w", stopErr)
				}
				if stop {
					w.logf("All beads complete. Exiting watch mode.")
					return nil
				}
			}

		case <-ticker.C:
			if scanInFlight {
				continue
			}
			scanInFlight = true
			scan := w.scanFn
			if scan == nil {
				scan = w.scanReadyWork
			}
			scanWG.Add(1)
			go func() {
				defer scanWG.Done()
				err := scan(watchCtx)
				select {
				case scanDone <- err:
				case <-watchCtx.Done():
				case <-w.stopCh:
				}
			}()

		case scanErr := <-scanDone:
			scanInFlight = false
			if scanErr != nil {
				if watchCtx.Err() != nil {
					return watchCtx.Err()
				}
				if isAutomatedAssignmentSafetyError(scanErr) {
					return scanErr
				}
				w.logf("Error during ready-work scan: %v", scanErr)
			}

			// A scan can be the thing that drains the queue, so honor
			// stop-when-done here too.
			if w.stopWhenDone {
				stop, stopErr := w.shouldStop(watchCtx)
				if stopErr != nil {
					return fmt.Errorf("check watch completion after scan: %w", stopErr)
				}
				if stop {
					w.logf("All beads complete. Exiting watch mode.")
					return nil
				}
			}

		case <-watchCtx.Done():
			w.logf("Watch mode interrupted. Shutting down...")
			return watchCtx.Err()

		case <-w.stopCh:
			w.logf("Watch mode stopped.")
			return nil
		}
	}
}

// scanReadyWork re-runs the same plan/dispatch pass the initial assignment uses
// (getAssignOutputEnhanced + executeAssignmentsEnhanced) so the watch loop can
// pick up work that became ready after startup — a gate unblocking, new beads,
// or a startup-busy agent going idle — without waiting for a completion event
// that may never come.
//
// Planning filters avoid needless claim attempts, but they are not the
// ownership boundary: executeAssignmentsEnhanced atomically claims each bead
// in br before reservation and dispatch. The optional watch delay is pacing
// backoff only. In dry-run mode this plans and logs without claiming or sending.
func (w *WatchLoop) scanReadyWork(ctx context.Context) error {
	if w.scanOpts == nil {
		return nil
	}

	dryRun := w.opts != nil && w.opts.DryRun

	out, err := getAssignOutputEnhanced(ctx, w.scanOpts)
	if err != nil {
		return err
	}
	if out == nil || len(out.Assignments) == 0 {
		return nil
	}

	if dryRun {
		w.logf("Ready-work scan (dry-run): %d beads would be assigned (no dispatch)", len(out.Assignments))
		for _, assigned := range out.Assignments {
			w.logf("  Would assign %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
		}
		return nil
	}

	if err := executeAssignmentsEnhanced(ctx, w.session, out, w.scanOpts); err != nil {
		return err
	}

	// FIX (d): count/log only beads actually SENT, not the full planned set.
	w.mu.Lock()
	for _, assigned := range out.Assignments {
		if !assigned.PromptSent {
			continue
		}
		w.totalAssigned++
		w.lastAssignmentAt = time.Now()
		w.logf("Ready-work scan assigned: %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
	}
	w.mu.Unlock()

	return nil
}

func (w *WatchLoop) consumeCompletionEvent(ctx context.Context, event completion.CompletionEvent) error {
	eventID := strings.TrimSpace(event.EventID)
	consumerToken := strings.TrimSpace(event.ConsumerToken)
	if eventID != "" && (consumerToken == "" || event.LeaseDuration <= 0) {
		return fmt.Errorf("completion event %s has no durable consumer lease", eventID)
	}
	if w.handledCompletionEvents == nil {
		w.handledCompletionEvents = make(map[string]struct{})
	}
	_, alreadyHandled := w.handledCompletionEvents[eventID]
	if eventID == "" || !alreadyHandled {
		handle := w.handleCompletionFn
		if handle == nil {
			handle = w.handleCompletion
		}
		if eventID != "" {
			renewed, err := w.renewCompletionEventLease(ctx, event)
			if err != nil {
				return fmt.Errorf("validate completion event %s consumer lease: %w", eventID, err)
			}
			if !renewed {
				return fmt.Errorf("completion event %s consumer lease was lost before handling", eventID)
			}
		}
		handlerCtx := ctx
		cancelHandler := func() {}
		leaseDone := make(chan error, 1)
		if eventID != "" {
			handlerCtx, cancelHandler = context.WithCancel(ctx)
			go func() {
				leaseDone <- w.maintainCompletionEventLease(handlerCtx, cancelHandler, event)
			}()
		}
		handleErr := handle(handlerCtx, event)
		cancelHandler()
		if eventID != "" {
			if leaseErr := <-leaseDone; leaseErr != nil {
				return leaseErr
			}
		}
		if handleErr != nil {
			return handleErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if eventID != "" {
			w.handledCompletionEvents[eventID] = struct{}{}
		}
	}
	if eventID == "" {
		return nil
	}
	acknowledge := w.ackCompletionEventFn
	if acknowledge == nil {
		acknowledge = w.store.AcknowledgeCompletionEvent
	}
	acknowledged, err := acknowledge(ctx, event.BeadID, eventID, event.ConsumerToken)
	if err != nil {
		return fmt.Errorf("acknowledge completion event %s: %w", eventID, err)
	}
	if !acknowledged {
		return fmt.Errorf("completion event %s was superseded before acknowledgement", eventID)
	}
	return nil
}

func (w *WatchLoop) renewCompletionEventLease(ctx context.Context, event completion.CompletionEvent) (bool, error) {
	renew := w.renewCompletionEventFn
	if renew == nil {
		if w.store == nil {
			return false, errors.New("completion event lease store is required")
		}
		renew = w.store.RenewPendingCompletionEventLease
	}
	return renew(ctx, event.BeadID, event.EventID, event.ConsumerToken, event.LeaseDuration)
}

func (w *WatchLoop) maintainCompletionEventLease(ctx context.Context, cancelHandler context.CancelFunc, event completion.CompletionEvent) error {
	interval := event.LeaseDuration / 3
	if interval <= 0 {
		return fmt.Errorf("completion event %s has invalid lease duration %s", event.EventID, event.LeaseDuration)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewed, err := w.renewCompletionEventLease(ctx, event)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				cancelHandler()
				return fmt.Errorf("renew completion event %s lease: %w", event.EventID, err)
			}
			if !renewed {
				cancelHandler()
				return fmt.Errorf("completion event %s consumer lease was lost", event.EventID)
			}
		}
	}
}

// handleCompletion processes a single completion event
func (w *WatchLoop) handleCompletion(ctx context.Context, event completion.CompletionEvent) error {
	if rawDelay := strings.TrimSpace(os.Getenv("NTM_TEST_COMPLETION_HANDLER_DELAY")); rawDelay != "" {
		delay, err := time.ParseDuration(rawDelay)
		if err != nil || delay < 0 {
			return fmt.Errorf("invalid completion handler E2E delay %q", rawDelay)
		}
		if err := waitContextDelay(ctx, delay); err != nil {
			return fmt.Errorf("completion handler E2E delay canceled: %w", err)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	duration := event.Duration.Round(time.Second)

	if event.IsFailed {
		w.totalFailed++
		w.logf("Failed: %s by pane %d (%s) - %s", event.BeadID, event.Pane, event.AgentType, event.FailReason)
		return nil
	}

	w.totalCompleted++
	w.logf("Completion: %s by pane %d (%s, %v)", event.BeadID, event.Pane, event.AgentType, duration)

	dryRun := w.opts != nil && w.opts.DryRun

	// Check for delay between assignments. In dry-run mode the loop dispatches
	// nothing, so there's no point throttling between previews.
	if !dryRun && w.delay > 0 && !w.lastAssignmentAt.IsZero() {
		elapsed := time.Since(w.lastAssignmentAt)
		if elapsed < w.delay {
			sleepTime := w.delay - elapsed
			w.logf("Waiting %v before next assignment...", sleepTime.Round(time.Millisecond))
			if err := waitContextDelay(ctx, sleepTime); err != nil {
				return fmt.Errorf("assignment pacing canceled: %w", err)
			}
		}
	}

	// Perform auto-reassignment if enabled
	if assignAutoReassign {
		result, err := PerformAutoReassignment(ctx, event.BeadID, w.opts)
		if err != nil {
			return fmt.Errorf("auto-reassignment failed: %w", err)
		}

		// Log unblocked beads
		if len(result.NewlyUnblocked) > 0 {
			var ids []string
			for _, ub := range result.NewlyUnblocked {
				ids = append(ids, ub.ID)
			}
			w.logf("Unblocked: %s", strings.Join(ids, ", "))
		}

		// Log assignments. In dry-run mode the planner output is real but
		// nothing has been dispatched — distinguish the log line and skip the
		// counter/lastAssignmentAt updates so the end-of-session Summary()
		// doesn't claim work happened that didn't.
		//
		// bd-jqg0r: count emitted assignments in this cycle, not the planner
		// slice length. The previous condition `len(result.Assignments) >= w.limit`
		// fired on the very first iteration whenever the planner returned at
		// least limit assignments, so --limit=3 with three planned assignments
		// stopped after the first dispatch.
		processed := 0
		for _, assigned := range result.Assignments {
			if dryRun {
				w.logf("Would assign (dry-run): %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			} else {
				w.totalAssigned++
				w.lastAssignmentAt = time.Now()
				w.logf("Assigned: %s -> pane %d (%s)", assigned.BeadID, assigned.Pane, assigned.AgentType)
			}
			processed++

			if w.limit > 0 && processed >= w.limit {
				w.logf("Assignment limit (%d) reached for this cycle", w.limit)
				break
			}
		}

		// Log errors
		for _, errMsg := range result.Errors {
			w.logf("Warning: %s", errMsg)
		}
	}

	return nil
}

// shouldStop checks if watch mode should exit
func (w *WatchLoop) shouldStop(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, errors.New("watch completion context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Check if there are any active assignments
	active := w.store.ListActive()
	if len(active) > 0 {
		return false, nil // Still have work in progress
	}

	// Check the same dependency-aware, label-verified candidate surface used by
	// assignment planning. Raw ready rows include operator-gated work and omit
	// plan-only candidates, so they cannot decide whether the watch is drained.
	projectDir := ""
	if w.scanOpts != nil {
		projectDir = strings.TrimSpace(w.scanOpts.ProjectDir)
	}
	if projectDir == "" {
		var err error
		projectDir, err = resolveAssignProjectDir(ctx, w.session)
		if err != nil {
			return false, fmt.Errorf("resolve watch project: %w", err)
		}
	}
	var policyProject *string
	if w.scanOpts != nil {
		policyProject = &w.scanOpts.policyProject
	} else if w.opts != nil {
		policyProject = &w.opts.policyProject
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, policyProject); err != nil {
		return false, err
	}
	actionable, err := getActionableRecommendationsForWatch(ctx, projectDir, 0)
	if err != nil {
		if isAutomatedAssignmentSafetyError(err) {
			return false, fmt.Errorf("automated assignment stopped because actionable work could not be verified: %w", err)
		}
		return false, fmt.Errorf("read actionable work for watch completion: %w", err)
	}
	for _, rec := range actionable {
		if classifyTriageRecForAssignmentForProject(projectDir, rec, nil) == nil {
			return false, nil // Still have work available
		}
	}

	// Check if there are idle agents
	agentTypeFilter := ""
	if w.opts != nil {
		agentTypeFilter = w.opts.AgentTypeFilter
	}
	idleAgents, err := getIdleAgentsForWatchStop(ctx, w.session, agentTypeFilter, false)
	if err == nil && len(idleAgents) == 0 {
		w.logf("Warning: No idle agents available")
	}

	return true, nil // No active work, no ready beads
}

// Summary returns statistics about the watch session
func (w *WatchLoop) Summary() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	duration := time.Since(w.startTime).Round(time.Second)
	suffix := ""
	if w.opts != nil && w.opts.DryRun {
		suffix = " [dry-run: no panes were dispatched]"
	}
	return fmt.Sprintf("Watch session: %d assigned, %d completed, %d failed in %v%s",
		w.totalAssigned, w.totalCompleted, w.totalFailed, duration, suffix)
}

// buildDispatchReceipt constructs the wrapper-grade dispatch receipt
// surfaced to JSON callers of `ntm assign --pane`. Captures pane
// identity, prompt fingerprint (length + SHA-256), reservation
// outcome, and transport result with timing. See ntm#128.
func buildDispatchReceipt(
	session, workItemID string,
	pane tmux.Pane,
	paneTarget string,
	prompt, templateSource string,
	res *DirectAssignFileReservations,
	deliveryID string,
	sent bool,
	transportErr error,
	durationMs int64,
	dispatchedAt *time.Time,
	dryRun bool,
) *DispatchReceipt {
	timestamp := time.Now().UTC()
	if dispatchedAt != nil {
		timestamp = dispatchedAt.UTC()
	}
	r := &DispatchReceipt{
		WorkItemID: workItemID,
		Pane: DispatchPaneRef{
			Session:     session,
			Target:      paneTarget,
			WindowIndex: pane.WindowIndex,
			Index:       pane.Index,
			ID:          pane.ID,
			Title:       pane.Title,
		},
		Prompt: DispatchPromptInfo{
			Length:     len(prompt),
			HashSHA256: dispatchPromptHash(prompt),
			Source:     templateSource,
		},
		Transport: DispatchTransportStatus{
			Sent:       sent && transportErr == nil && !dryRun,
			DeliveryID: deliveryID,
			DurationMs: durationMs,
		},
		Timestamp: timestamp.Format(time.RFC3339Nano),
		DryRun:    dryRun,
	}
	if transportErr != nil {
		r.Transport.Error = transportErr.Error()
	}
	if res != nil {
		r.Reservation = &DispatchReservation{
			Requested: res.Requested,
			Granted:   res.Granted,
			Conflicts: res.Denied,
		}
	}
	return r
}

// dispatchPromptHash returns the SHA-256 of `prompt` hex-encoded so
// wrappers can prove byte-for-byte equality without storing the prompt
// itself in their logs.
func dispatchPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// runWatchMode implements the --watch flag for continuous auto-assignment
