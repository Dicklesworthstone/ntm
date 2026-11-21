package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

var (
	assignAuto         bool
	assignStrategy     string
	assignRelax        bool
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
	assignDryRun       bool // Preview recommendations or watch mode
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
	// caller's CWD.
	assignRepoPath string

	// Prompt-context enrichment flags.
	assignWithCASS   bool // force CASS history injection on
	assignNoCASS     bool // force CASS history injection off, overriding [cass.context] enabled=true
	assignWithMemory bool // inject CM rules even when [memory] send_injection=false
)

const (
	assignWatchOverlayKey         = "F12"
	assignObservationStageTimeout = statuspkg.DispatchObservationMaxAge - time.Second
	handledBeadRecentWindow       = 90 * time.Second
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

func observeAssignSessionWithTimeout(ctx context.Context, session string, timeout time.Duration) (statuspkg.SessionObservation, error) {
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

type assignAgentInfo struct {
	pane              tmux.Pane
	agentType         string
	model             string
	state             string
	scrollback        string
	contextUsage      float64
	activeAssignments int
	resourceHeadroom  float64
	relaxed           bool
}

type assignPaneRoleSet struct {
	implementer bool
	reviewer    bool
}

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

func assignRoleEligibleAgents(agents []assignAgentInfo, projectDir, template string) ([]assignAgentInfo, error) {
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load persona registry for role-aware assignment: %w", err)
	}
	return filterAssignAgentsForTemplate(agents, registry, template), nil
}

func isRelaxableThinkingPane(observation statuspkg.SessionObservation, pane tmux.Pane, agentType string, now time.Time) bool {
	paneObs, err := currentAssignPaneObservation(observation, pane.ID, now)
	if err != nil {
		return false
	}
	if paneObs.Current.Freshness != statuspkg.FreshnessFresh || paneObs.Current.Error != "" {
		return false
	}
	if paneObs.Current.Confidence < statuspkg.MinimumDispatchConfidence {
		return false
	}
	stateStr := strings.ToLower(strings.TrimSpace(string(paneObs.Current.Status.State)))
	if stateStr == strings.ToLower(string(robot.StateThinking)) {
		return true
	}
	if paneObs.Current.Status.State != statuspkg.StateWorking {
		return false
	}
	if strings.TrimSpace(paneObs.RawOutput) == "" {
		return false
	}
	activity, err := robot.ClassifyPaneOutputActivity(pane.ID, agentType, paneObs.RawOutput)
	if err != nil || activity == nil {
		return false
	}
	return activity.State == robot.StateThinking && !activity.RateLimited && !activity.UnsubmittedInput && !activity.QueuedMessages
}

func applyAssignRelaxation(
	panes []tmux.Pane,
	observation statuspkg.SessionObservation,
	idleAgents []assignAgentInfo,
	paneVerdicts []AssignPanePlaceability,
	projectDir string,
	template string,
	needed int,
	relax bool,
	now time.Time,
) ([]assignAgentInfo, bool, []string, float64, float64, error) {
	paneByID := make(map[string]tmux.Pane, len(panes))
	for _, p := range panes {
		paneByID[p.ID] = p
	}

	idleByID := make(map[string]assignAgentInfo, len(idleAgents))
	for _, a := range idleAgents {
		idleByID[a.pane.ID] = a
	}

	relaxCandidates := make(map[string]assignAgentInfo)
	scored := make([]robot.ScoredAgent, 0, len(paneVerdicts))
	for _, verdict := range paneVerdicts {
		if idle, ok := idleByID[verdict.PaneID]; ok {
			scored = append(scored, robot.BuildRelaxableScoredAgent(
				idle.pane.ID, idle.pane.Index, idle.agentType, robot.StateWaiting, false, "",
			))
			continue
		}
		if verdict.Verdict != PlaceabilityNotIdle {
			continue
		}
		pane, ok := paneByID[verdict.PaneID]
		if !ok {
			continue
		}
		if !isRelaxableThinkingPane(observation, pane, verdict.AgentType, now) {
			scored = append(scored, robot.BuildRelaxableScoredAgent(
				pane.ID, pane.Index, verdict.AgentType, robot.StateGenerating, true, PlaceabilityNotIdle,
			))
			continue
		}
		paneObs, _ := observation.PaneByID(pane.ID)
		candidate := assignAgentInfo{
			pane:       pane,
			agentType:  verdict.AgentType,
			model:      detectModelFromTitle(verdict.AgentType, pane.Title),
			state:      verdict.State,
			scrollback: paneObs.RawOutput,
			relaxed:    true,
		}
		eligible, err := assignRoleEligibleAgents([]assignAgentInfo{candidate}, projectDir, template)
		if err != nil {
			return nil, false, make([]string, 0), 0, 0, err
		}
		if len(eligible) == 0 {
			continue
		}
		relaxCandidates[pane.ID] = candidate
		scored = append(scored, robot.BuildRelaxableScoredAgent(
			pane.ID, pane.Index, verdict.AgentType, robot.StateThinking, true, PlaceabilityNotIdle,
		))
	}

	router := robot.NewRouter()
	strictRes := router.Route(scored, robot.StrategyLeastLoaded, robot.RoutingContext{})
	origScore := strictRes.OriginalScore
	relaxedScore := origScore
	constraints := make([]string, 0)

	candidates := append([]assignAgentInfo(nil), idleAgents...)
	if !relax || needed <= len(candidates) {
		return candidates, false, constraints, origScore, relaxedScore, nil
	}

	admittedIDs := make([]string, 0, len(candidates)+len(relaxCandidates))
	for _, a := range candidates {
		admittedIDs = append(admittedIDs, a.pane.ID)
	}

	relaxed := false
	for len(candidates) < needed {
		relRes := router.RouteWithRelaxation(scored, robot.StrategyLeastLoaded, robot.RoutingContext{
			ExcludePaneIDs: admittedIDs,
		})
		if relRes.Selected == nil {
			break
		}
		admittedIDs = append(admittedIDs, relRes.Selected.PaneID)
		candidate, ok := relaxCandidates[relRes.Selected.PaneID]
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
		if relRes.Relaxed && !relaxed {
			relaxed = true
			constraints = append([]string(nil), relRes.RelaxedConstraints...)
			relaxedScore = relRes.RelaxedScore
		}
	}

	return candidates, relaxed, constraints, origScore, relaxedScore, nil
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

	cmd.Flags().BoolVar(&assignAuto, "auto", false, "Execute assignments without confirmation")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().BoolVar(&assignRelax, "relax", false, "Retry an empty assignment plan with half its score floor; preserve availability, context, capacity, and safety gates")
	cmd.Flags().StringVar(&assignBeads, "beads", "", "Comma-separated list of specific bead IDs to assign")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum number of assignments (0 = unlimited)")

	cmd.Flags().StringVar(&assignAgentType, "agent", "", "Filter by agent type: any (no filter), claude, codex, gemini, antigravity, grok, omp, oc")
	cmd.Flags().BoolVar(&assignCCOnly, "cc-only", false, "Only assign to Claude agents (alias for --agent=claude)")
	cmd.Flags().BoolVar(&assignCodOnly, "cod-only", false, "Only assign to Codex agents (alias for --agent=codex)")
	cmd.Flags().BoolVar(&assignGmiOnly, "gmi-only", false, "Only assign to Gemini agents (alias for --agent=gemini)")

	cmd.Flags().StringVar(&assignTemplate, "template", "impl", "Prompt template: impl, review, custom")
	cmd.Flags().StringVar(&assignTemplateFile, "template-file", "", "Custom template file path (for --template=custom)")

	cmd.Flags().BoolVarP(&assignVerbose, "verbose", "v", false, "Show detailed scoring/decision logs")
	cmd.Flags().BoolVarP(&assignQuiet, "quiet", "q", false, "Suppress non-essential output")
	cmd.Flags().DurationVar(&assignTimeout, "timeout", 30*time.Second, "Timeout for tmux observation and external calls (bv, br, Agent Mail)")
	cmd.Flags().BoolVar(&assignDryRun, "dry-run", false, "Preview recommendations or watch mode without executing")
	cmd.Flags().BoolVar(&assignReserveFiles, "reserve-files", true, "Reserve file paths via Agent Mail before assignment")

	cmd.Flags().StringVar(&assignPane, "pane", "", "Assign bead directly to exactly one N, W.P, or %N pane selector (requires --beads)")
	cmd.Flags().BoolVar(&assignForce, "force", false, "Force assignment even if pane is busy (also lets --clear remove completed or outcome-unknown assignments; forcing an outcome-unknown clear may duplicate an already-delivered dispatch)")
	cmd.Flags().BoolVar(&assignIgnoreDeps, "ignore-deps", false, "Ignore dependency checks for assignment")
	cmd.Flags().StringVar(&assignPrompt, "prompt", "", "Custom prompt for direct assignment")

	cmd.Flags().StringVar(&assignClear, "clear", "", "Clear specific bead assignments (comma-separated bead IDs)")
	cmd.Flags().StringVar(&assignClearPane, "clear-pane", "", "Clear all assignments for one pane (N, W.P, or %N; use when agent crashed)")
	cmd.Flags().BoolVar(&assignClearFailed, "clear-failed", false, "Clear all failed assignments")

	cmd.Flags().BoolVar(&assignWatch, "watch", false, "Enable watch mode for continuous auto-assignment on completion")
	cmd.Flags().BoolVar(&assignAutoReassign, "auto-reassign", true, "Enable auto-reassignment of newly unblocked beads in watch mode")
	cmd.Flags().DurationVar(&assignWatchInterval, "watch-interval", 30*time.Second, "How often to check for completions in watch mode")
	cmd.Flags().BoolVar(&assignStopWhenDone, "stop-when-done", false, "Exit watch mode when no more beads are ready")
	cmd.Flags().DurationVar(&assignDelay, "delay", 0, "Pacing backoff between assignments in watch mode")
	cmd.Flags().DurationVar(&assignIdleThreshold, "idle-threshold", 0, "How long an agent may go silent before its watched assignment is stamped failed (default: [assign] idle_threshold config, else 15m)")

	cmd.Flags().StringVar(&assignReassign, "reassign", "", "Bead ID to reassign to a different agent")
	cmd.Flags().StringVar(&assignToPane, "to-pane", "", "Target pane N, W.P, or %N for reassignment/retry")
	cmd.Flags().StringVar(&assignToType, "to-type", "", "Target agent type for reassignment: claude, codex, gemini (auto-selects idle agent)")

	cmd.Flags().StringVar(&assignRetry, "retry", "", "Retry a specific failed assignment (bead ID)")
	cmd.Flags().BoolVar(&assignRetryFailed, "retry-failed", false, "Retry all failed assignments")

	cmd.Flags().StringVar(&assignRepoPath, "repo", "", "Pin the bead-source repository path (overrides CWD discovery; required for daemon/cron use)")

	registerAssignPromptContextFlags(cmd)

	return cmd
}

func registerAssignPromptContextFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&assignWithCASS, "with-cass", false, "Inject relevant CASS session history above each assignment prompt (query: bead title, labels, description); degrades gracefully when cass is unavailable. Config: [cass.context] enabled/max_sessions/lookback_days/max_tokens/min_relevance/skip_if_context_above/prefer_same_project")
	cmd.Flags().BoolVar(&assignNoCASS, "no-cass", false, "Disable CASS history injection for this assignment, overriding [cass.context] enabled=true")
	cmd.Flags().BoolVar(&assignWithMemory, "with-memory", false, "Inject relevant CM (cass-memory) rules above each assignment prompt; degrades gracefully when cm is unavailable. Config: [memory] send_injection/send_max_rules/send_budget_tokens")
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

func configureAuthoritativeAssignmentPolicy(projectDir string) error {
	projectDir = strings.TrimSpace(projectDir)
	effective, err := loadAuthoritativeAssignmentPolicy(projectDir)
	if err != nil {
		return markCLIInvalidInput(err)
	}
	bv.ConfigureOperatorGatedLabels(effective.Assign.OperatorGatedLabels)
	if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, effective.Assign.OperatorGatedLabels); err != nil {
		return markCLIInvalidInput(fmt.Errorf("register assignment safety policy for %s: %w", projectDir, err))
	}
	if err := bv.ConfigureProjectWorkSourcePolicy(projectDir, effective.Assign.WorkSource.Policy()); err != nil {
		return markCLIInvalidInput(fmt.Errorf("register work-source policy for %s: %w", projectDir, err))
	}
	return nil
}

func loadAuthoritativeAssignmentPolicy(projectDir string) (*config.Config, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, errors.New("assignment safety policy requires an authoritative project directory")
	}
	globalPath := selectedConfigPath()
	requireGlobal := cfgFile != "" || os.Getenv("NTM_CONFIG") != ""
	effective, err := config.LoadAssignmentPolicyStrict(projectDir, globalPath, requireGlobal)
	if err != nil {
		return nil, fmt.Errorf("load assignment safety policy for %s using %s: %w", projectDir, globalPath, err)
	}
	return effective, nil
}

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
		errors.Is(err, bv.ErrActionableLabelsUnverified) ||
		errors.Is(err, bv.ErrStaleWorkCoordination) ||
		errors.Is(err, bv.ErrBeadAlreadyClaimed) ||
		errors.Is(err, assignment.ErrClaimConflict) ||
		errors.Is(err, assignment.ErrTargetOccupied)
}

func isRetryableAssignmentSourceChange(err error) bool {
	if !errors.Is(err, worksource.ErrSourceChanged) {
		return false
	}
	var sourceChangeOnly func(error) bool
	sourceChangeOnly = func(cause error) bool {
		switch cause {
		case worksource.ErrSourceChanged, bv.ErrStaleWorkCoordination, bv.ErrActionablePlanUnverified:
			return true
		}
		if joined, ok := cause.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if !sourceChangeOnly(child) {
					return false
				}
			}
			return true
		}
		if child := errors.Unwrap(cause); child != nil {
			return sourceChangeOnly(child)
		}
		return false
	}
	return sourceChangeOnly(err)
}

func prepareResolvedAssignCommand(cmd *cobra.Command, session, projectDir string) (handled bool, policyProject string, closeWebhook func() error, err error) {
	if strings.TrimSpace(assignClear) != "" || strings.TrimSpace(assignClearPane) != "" || assignClearFailed {
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
	if assignDryRun && (strings.TrimSpace(assignPane) != "" || assignReassign != "" ||
		assignRetry != "" || assignRetryFailed || assignClear != "" ||
		strings.TrimSpace(assignClearPane) != "" || assignClearFailed) {
		return markCLIInvalidInput(errors.New("--dry-run supports assignment recommendations and --watch; it cannot be combined with direct, retry, reassign, or clear operations"))
	}

	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

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

	if !cmd.Flags().Changed("strategy") {
		if cfg != nil && cfg.Assign.Strategy != "" {
			assignStrategy = cfg.Assign.Strategy
		}
	}

	if !config.IsValidStrategy(assignStrategy) {
		return fmt.Errorf("unknown strategy %q. Valid strategies: %s",
			assignStrategy, strings.Join(config.ValidAssignStrategies, ", "))
	}

	if assignReassign != "" {
		return runReassignment(cmd.Context(), session)
	}
	if assignRetry != "" || assignRetryFailed {
		return runRetryAssignments(cmd.Context(), session)
	}
	if assignWatch {
		return runWatchMode(cmd, session, projectDir, policyProject)
	}

	agentTypeFilter := resolveAgentTypeFilter()

	var beadIDs []string
	if assignBeads != "" {
		beadIDs = strings.Split(assignBeads, ",")
		for i := range beadIDs {
			beadIDs[i] = strings.TrimSpace(beadIDs[i])
		}
	}

	if assignDryRun {
		assignAuto = false
	}

	assignOpts := &AssignCommandOptions{
		Session:         session,
		ProjectDir:      projectDir,
		BeadIDs:         beadIDs,
		Strategy:        assignStrategy,
		Relax:           assignRelax,
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
		WithCASS:        assignWithCASS,
		NoCASS:          assignNoCASS,
		WithMemory:      assignWithMemory,
		policyProject:   policyProject,
	}

	if strings.TrimSpace(assignPane) != "" {
		return runDirectPaneAssignment(cmd.Context(), assignOpts)
	}

	if IsJSONOutput() {
		return runAssignJSON(cmd.Context(), assignOpts)
	}

	assignOutput, err := getAssignOutputEnhanced(cmd.Context(), assignOpts)
	if err != nil {
		return err
	}

	if !assignQuiet {
		displayAssignOutputEnhanced(assignOutput, assignVerbose)
	}

	if len(assignOutput.Assignments) == 0 || assignDryRun {
		return nil
	}

	if assignAuto {
		return executeAssignmentsEnhanced(cmd.Context(), session, assignOutput, assignOpts)
	}

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

func resolveAgentTypeFilter() string {
	if assignAgentType != "" {
		return normalizeAgentTypeAlias(assignAgentType)
	}
	if assignCCOnly {
		return "claude"
	}
	if assignCodOnly {
		return "codex"
	}
	if assignGmiOnly {
		return "gemini"
	}
	return ""
}

func normalizeAgentTypeAlias(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "any", "all", "*":
		return ""
	default:
		return robot.ResolveAgentType(raw)
	}
}

func normalizeBeadStatus(status string) string {
	return assign.NormalizeBeadStatus(status)
}

func skippedItemFromDomain(skip *assign.SkippedBead) *SkippedItem {
	if skip == nil {
		return nil
	}
	return &SkippedItem{
		BeadID:       skip.BeadID,
		BeadTitle:    skip.BeadTitle,
		Reason:       skip.Reason,
		BlockedByIDs: skip.BlockedByIDs,
	}
}

func classifyTriageRecForAssignmentForProject(projectDir string, rec bv.TriageRecommendation, activeAssignments map[string]struct{}) *SkippedItem {
	return classifyTriageRecForAssignmentWithGate(rec, activeAssignments, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func classifyTriageRecForAssignmentWithGate(rec bv.TriageRecommendation, activeAssignments map[string]struct{}, operatorGated func(string) bool) *SkippedItem {
	return skippedItemFromDomain(assign.ClassifyWorkItem(assign.CandidateWorkItem{
		ID:        rec.ID,
		Title:     rec.Title,
		Type:      rec.Type,
		Status:    rec.Status,
		Priority:  rec.Priority,
		Labels:    rec.Labels,
		BlockedBy: rec.BlockedBy,
	}, activeAssignments, bv.IsContainerBeadType, operatorGated))
}

func readAssignmentPreflight[T any](ctx context.Context, projectDir string, execute bool, read func() (T, error)) (T, error) {
	if execute {
		return bv.ReadForMutationContext(ctx, projectDir, read)
	}
	return read()
}

func loadActionableRecommendationsForAssignment(ctx context.Context, projectDir string, execute bool) ([]bv.TriageRecommendation, error) {
	return readAssignmentPreflight(ctx, projectDir, execute, func() ([]bv.TriageRecommendation, error) {
		return getActionableRecommendationsForAssign(ctx, projectDir, 0)
	})
}

func readBeadAssignmentDetailsForMutation(ctx context.Context, projectDir, beadID string) (*bv.BeadAssignmentDetails, error) {
	return bv.ReadForMutationContext(ctx, projectDir, func() (*bv.BeadAssignmentDetails, error) {
		details, err := getBeadAssignmentDetailsForAssignment(ctx, projectDir, beadID)
		if err != nil {
			return nil, err
		}
		if details == nil {
			return nil, errors.New("live work-item details are missing")
		}
		return details, nil
	})
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
	return skippedItemFromDomain(assign.ClassifyLiveWorkItem(assign.CandidateWorkItem{
		ID:         details.ID,
		Title:      details.Title,
		Type:       details.IssueType,
		Status:     details.Status,
		Priority:   details.Priority,
		Labels:     append([]string(nil), details.Labels...),
		BlockedBy:  append([]string(nil), details.BlockedBy...),
		DeferUntil: details.DeferUntil,
		Pinned:     details.Pinned,
		Ephemeral:  details.Ephemeral,
		Template:   details.Template,
		Wisp:       details.Wisp,
	}, activeAssignments, bv.IsContainerBeadType, operatorGated, time.Now()))
}

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

func loadActiveAssignmentPanes(session string) (map[string]string, error) {
	active := make(map[string]string)
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		return active, fmt.Errorf("load active assignment panes: %w", err)
	}
	for _, item := range store.ListActive() {
		key, identityErr := assignment.CanonicalPaneIdentity(item)
		if identityErr != nil {
			return active, identityErr
		}
		active[key] = item.BeadID
	}
	return active, nil
}

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

// AssignCommandOptions holds all options for the assign command.
type AssignCommandOptions struct {
	Session         string
	ProjectDir      string
	BeadIDs         []string
	Strategy        string
	Relax           bool
	Limit           int
	AgentTypeFilter string
	Template        string
	TemplateFile    string
	Verbose         bool
	Quiet           bool
	Auto            bool
	Timeout         time.Duration
	ReserveFiles    bool

	PaneSelector string
	Force        bool
	IgnoreDeps   bool
	Prompt       string

	Clear     string
	ClearPane string

	WithCASS   bool
	NoCASS     bool
	WithMemory bool

	policyProject               string
	actionablePreflightVerified bool
	verifiedActionable          []bv.TriageRecommendation
}

// AssignOutputEnhanced is the enhanced output structure matching the spec.
type AssignOutputEnhanced struct {
	Strategy           string                   `json:"strategy"`
	Relaxed            bool                     `json:"relaxed"`
	RelaxedConstraints []string                 `json:"relaxed_constraints"`
	OriginalScore      float64                  `json:"original_score"`
	RelaxedScore       float64                  `json:"relaxed_score"`
	Assignments        []AssignmentItem         `json:"assignments"`
	Skipped            []SkippedItem            `json:"skipped"`
	Panes              []AssignPanePlaceability `json:"pane_placeability,omitempty"`
	Summary            AssignSummaryEnhanced    `json:"summary"`
	Allocation         *AssignAllocationView    `json:"allocation,omitempty"`
	Errors             []string                 `json:"-"`
}

// AssignmentItem represents a single assignment in JSON output.
type AssignmentItem struct {
	Relaxation      *assign.ScoreRelaxation `json:"relaxation,omitempty"`
	BeadID          string                  `json:"bead_id"`
	BeadTitle       string                  `json:"bead_title"`
	rawBeadTitle    string
	Pane            int                               `json:"pane"`
	PaneTarget      string                            `json:"pane_target"`
	PaneID          string                            `json:"pane_id,omitempty"`
	AgentType       string                            `json:"agent_type"`
	AgentName       string                            `json:"agent_name"`
	Status          string                            `json:"status"`
	PromptSent      bool                              `json:"prompt_sent"`
	AssignedAt      string                            `json:"assigned_at"`
	Score           float64                           `json:"score,omitempty"`
	Reasoning       string                            `json:"reasoning,omitempty"`
	ReasonCodes     []string                          `json:"reason_codes,omitempty"`
	ScoreComponents *assign.AllocationScoreComponents `json:"score_components,omitempty"`
	CASSInjection   *robot.CASSInjectionInfo          `json:"cass_injection,omitempty"`
	MemoryInjection *robot.CMInjectionInfo            `json:"memory_injection,omitempty"`
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

// SkippedItem represents a skipped bead.
type SkippedItem struct {
	BeadID       string   `json:"bead_id"`
	BeadTitle    string   `json:"bead_title"`
	Reason       string   `json:"reason"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"`
}

// AssignSummaryEnhanced contains summary statistics.
type AssignSummaryEnhanced struct {
	TotalBeadCount     int            `json:"total_bead_count"`
	ActionableCount    int            `json:"actionable_count"`
	BlockedCount       int            `json:"blocked_count"`
	AssignedCount      int            `json:"assigned_count"`
	SkippedCount       int            `json:"skipped_count"`
	IdleAgents         int            `json:"idle_agent_count"`
	CycleWarningCount  int            `json:"cycle_warning_count,omitempty"`
	PlaceabilityCounts map[string]int `json:"placeability_counts,omitempty"`
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

func detectAgentTypeFromTitle(title string) string {
	return assign.DetectAgentTypeFromTitle(title)
}

func detectModelFromTitle(_ string, title string) string {
	return assign.DetectModelFromTitle(title)
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

func runAssignJSON(ctx context.Context, opts *AssignCommandOptions) error {
	assignOutput, err := getAssignOutputEnhanced(ctx, opts)
	if err != nil {
		code := "ASSIGN_ERROR"
		if errors.Is(err, errCLIInvalidInput) {
			code = robot.ErrCodeInvalidFlag
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
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

	var warnings []string
	if len(assignOutput.Errors) > 0 {
		warnings = assignOutput.Errors
	}
	if warnings == nil {
		warnings = []string{}
	}

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

	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

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

	observedNow := time.Now()
	idleAgents, paneVerdicts, err := evaluateAssignPanePlaceability(panes, activePanes, observation, opts.AgentTypeFilter, observedNow)
	if err != nil {
		return nil, err
	}

	var allRecs []bv.TriageRecommendation
	if opts.actionablePreflightVerified {
		allRecs = cloneSpawnActionableRecommendations(opts.verifiedActionable)
	} else {
		allRecs, err = loadActionableRecommendationsForAssignment(ctx, projectDir, opts.Auto)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("assignment planning canceled while reading actionable work: %w", ctxErr)
		}
		return nil, fmt.Errorf("automated assignment stopped because actionable work could not be verified: %w", err)
	}

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
		if len(blockedBeads) > 0 {
			fmt.Fprintf(os.Stderr, "[DEP] Filtered %d non-actionable beads, %d actionable\n", len(blockedBeads), len(readyBeads))
		}
	}

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
		var filteredBlocked []SkippedItem
		for _, b := range blockedBeads {
			if beadSet[b.BeadID] {
				filteredBlocked = append(filteredBlocked, b)
			}
		}
		blockedBeads = filteredBlocked
	}

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

	if len(readyBeads) > 50 {
		readyBeads = readyBeads[:50]
	}

	allSkipped := append(blockedBeads, cyclicBeads...)

	idleAgents, err = assignRoleEligibleAgents(idleAgents, projectDir, opts.Template)
	if err != nil {
		return nil, err
	}
	markRoleIneligiblePanes(paneVerdicts, idleAgents, opts.Template)

	neededCandidates := len(readyBeads)
	if opts.Limit > 0 && opts.Limit < neededCandidates {
		neededCandidates = opts.Limit
	}
	candidateAgents, relaxed, relaxedConstraints, origScore, relaxedScore, err := applyAssignRelaxation(
		panes, observation, idleAgents, paneVerdicts, projectDir, opts.Template, neededCandidates, opts.Relax, observedNow,
	)
	if err != nil {
		return nil, err
	}

	result := &AssignOutputEnhanced{
		Strategy:           opts.Strategy,
		Relaxed:            relaxed,
		RelaxedConstraints: relaxedConstraints,
		OriginalScore:      origScore,
		RelaxedScore:       relaxedScore,
		Assignments:        make([]AssignmentItem, 0),
		Skipped:            allSkipped,
		Panes:              paneVerdicts,
		Summary: AssignSummaryEnhanced{
			TotalBeadCount:     len(readyBeads) + len(blockedBeads) + cycleWarnings,
			ActionableCount:    len(readyBeads),
			BlockedCount:       countSkippedByReason(blockedBeads, "blocked_by_dependency"),
			IdleAgents:         len(idleAgents),
			CycleWarningCount:  cycleWarnings,
			PlaceabilityCounts: placeabilityCounts(paneVerdicts),
		},
	}

	if len(candidateAgents) == 0 {
		for _, bead := range readyBeads {
			result.Skipped = append(result.Skipped, SkippedItem{
				BeadID: bead.ID,
				Reason: SkipReasonNoPlaceableAgents,
			})
		}
		result.Summary.SkippedCount = len(result.Skipped)
		return redactAssignOutputForProjection(result), nil
	}

	if len(readyBeads) == 0 {
		result.Summary.SkippedCount = len(result.Skipped)
		return redactAssignOutputForProjection(result), nil
	}

	assignments, allocationPlan := generateAssignmentsEnhancedWithPlan(ctx, candidateAgents, readyBeads, opts, true)
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

	if opts.Limit > 0 && len(assignments) > opts.Limit {
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

func generateAssignmentsLegacy(agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions) []AssignmentItem {
	multiWindow := tmux.PanesSpanMultipleWindows(assignmentAgentPanes(agents))

	if strings.EqualFold(opts.Strategy, "round-robin") && assignHumanDiagnostics(opts.Verbose) && len(agents) > 0 {
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

	planAgents := make([]assign.LegacyPlanAgent, 0, len(agents))
	for _, ag := range agents {
		planAgents = append(planAgents, assign.LegacyPlanAgent{
			Pane:      ag.pane,
			AgentType: ag.agentType,
			AgentName: assignmentAgentIdentityForPane(opts.ProjectDir, opts.Session, ag.agentType, ag.pane, multiWindow),
		})
	}
	planBeads := make([]assign.LegacyPlanBead, 0, len(beads))
	for _, bead := range beads {
		planBeads = append(planBeads, assign.LegacyPlanBead{
			ID:       bead.ID,
			Title:    bead.Title,
			Priority: bead.Priority,
		})
	}

	planOpts := assign.LegacyPlanOptions{
		Strategy:   opts.Strategy,
		AssignedAt: time.Now().UTC().Format(time.RFC3339),
		Session:    opts.Session,
	}
	if opts.Session != "" && !strings.EqualFold(opts.Strategy, "round-robin") &&
		!strings.EqualFold(opts.Strategy, "quality") &&
		!strings.EqualFold(opts.Strategy, "speed") &&
		!strings.EqualFold(opts.Strategy, "dependency") {
		if store, err := assignment.LoadStore(opts.Session); err == nil && store != nil {
			planOpts.InitialCounts = make(map[string]int)
			planOpts.InitialLastAssigned = make(map[string]time.Time)
			for _, a := range store.ListActive() {
				paneKey, identityErr := assignment.CanonicalPaneIdentity(a)
				if identityErr != nil {
					continue
				}
				planOpts.InitialCounts[paneKey]++
				if planOpts.InitialLastAssigned[paneKey].Before(a.AssignedAt) {
					planOpts.InitialLastAssigned[paneKey] = a.AssignedAt
				}
			}
		}
	}

	planned := assign.PlanLegacyStrategy(planAgents, planBeads, planOpts)
	assignments := make([]AssignmentItem, 0, len(planned))
	for _, item := range planned {
		assignments = append(assignments, AssignmentItem{
			BeadID:     item.BeadID,
			BeadTitle:  item.BeadTitle,
			Pane:       item.Pane,
			PaneTarget: item.PaneTarget,
			PaneID:     item.PaneID,
			AgentType:  item.AgentType,
			AgentName:  item.AgentName,
			Status:     item.Status,
			PromptSent: false,
			AssignedAt: item.AssignedAt,
			Score:      item.Score,
			Reasoning:  item.Reasoning,
		})
	}
	return assignments
}

func buildAssignAllocationInput(ctx context.Context, agents []assignAgentInfo, beads []bv.BeadPreview, opts *AssignCommandOptions, bvAvailable bool) assign.AllocationInput {
	session := ""
	projectDir := ""
	if opts != nil {
		session = opts.Session
		projectDir = opts.ProjectDir
	}
	multiWindow := tmux.PanesSpanMultipleWindows(assignmentAgentPanes(agents))
	stats := loadAssignAllocationStats(session)
	allocationAgents := make([]assign.AllocationAgent, 0, len(agents))
	totalActive := stats.totalActive
	totalHeadroom := 0.0

	for _, agentInfo := range agents {
		agentID := assignAllocationAgentID(projectDir, session, agentInfo, multiWindow)
		paneKey := assignmentPaneStableKey(agentInfo.pane)
		active := agentInfo.activeAssignments + stats.activeByPane[paneKey]
		totalActive += agentInfo.activeAssignments
		headroom := assign.EstimateAgentResourceHeadroom(agentInfo.resourceHeadroom, agentInfo.contextUsage, agentInfo.scrollback, active)
		totalHeadroom += headroom
		allocationAgents = append(allocationAgents, assign.AllocationAgent{
			ID:                agentID,
			Session:           session,
			PaneIndex:         agentInfo.pane.Index,
			PaneTarget:        assignmentPaneTarget(agentInfo.pane),
			PaneID:            agentInfo.pane.ID,
			AgentType:         tmux.AgentType(agentInfo.agentType).Canonical(),
			Idle:              agentInfo.state == "" || agentInfo.state == "idle" || (opts != nil && opts.Relax && agentInfo.relaxed),
			ContextUsage:      agentInfo.contextUsage,
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
		Relax:      opts != nil && opts.Relax,
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
		taskType := assign.InferTaskType(bead.Title)
		out = append(out, assign.AllocationReadyBead{
			ID:           bead.ID,
			Title:        bead.Title,
			TaskType:     taskType,
			Priority:     assign.ParsePriorityString(bead.Priority),
			ResourceCost: assign.EstimateBeadResourceCost(bead.Title, taskType),
		})
	}
	return out
}

func assignAllocationAgentID(projectDir, session string, agentInfo assignAgentInfo, multiWindow bool) string {
	if session != "" {
		return assignmentAgentIdentityForPane(projectDir, session, agentInfo.agentType, agentInfo.pane, multiWindow)
	}
	physical := assignmentPaneTarget(agentInfo.pane)
	return fmt.Sprintf("%s_%s", agentInfo.agentType, physical)
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
		AgentHeadroom: assign.PressureAgentHeadroom(snapshot.Overall.String()),
	}
}

func assignPressureLimitingSources(sources []pressure.Source) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, string(source))
	}
	return out
}

func assignmentItemsFromAllocationPlan(plan assign.AllocationPlan, assignedAt string) []AssignmentItem {
	items := make([]AssignmentItem, 0, len(plan.Recommendations))
	for _, recommendation := range plan.Recommendations {
		components := recommendation.ScoreComponents
		items = append(items, AssignmentItem{
			Relaxation:      recommendation.Relaxation,
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

func displayAssignOutputEnhanced(out *AssignOutputEnhanced, verbose bool) {
	th := theme.Current()

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Primary)
	subtitleStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	fmt.Println()
	fmt.Println(titleStyle.Render("Task Assignment Recommendations"))
	fmt.Println(strings.Repeat("━", 50))

	fmt.Println()
	fmt.Printf("Strategy: %s\n", out.Strategy)
	fmt.Printf("Idle Agents: %d | Actionable Beads: %d", out.Summary.IdleAgents, out.Summary.ActionableCount)
	if out.Summary.BlockedCount > 0 {
		fmt.Printf(" | Blocked: %d", out.Summary.BlockedCount)
	}
	fmt.Println()
	if out.Relaxed && len(out.RelaxedConstraints) > 0 {
		fmt.Printf("Relaxed Constraints: %s (original=%.1f, relaxed=%.1f)\n",
			strings.Join(out.RelaxedConstraints, ", "), out.OriginalScore, out.RelaxedScore)
	}

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
		if verbose {
			for _, line := range formatPanePlaceabilityLines(out.Panes) {
				fmt.Println(subtitleStyle.Render(line))
			}
		}
	} else {
		fmt.Println()
		fmt.Println(subtitleStyle.Render("No assignments to recommend."))
		if out.Summary.IdleAgents == 0 {
			reason := "  Reason: No placeable agents"
			if breakdown := placeabilityBreakdown(out.Summary.PlaceabilityCounts); breakdown != "" {
				reason += " (" + breakdown + ")"
			}
			fmt.Println(subtitleStyle.Render(reason))
			for _, line := range formatPanePlaceabilityLines(out.Panes) {
				fmt.Println(subtitleStyle.Render(line))
			}
		} else if out.Summary.TotalBeadCount == 0 {
			fmt.Println(subtitleStyle.Render("  Reason: No ready beads to assign"))
			if verbose {
				for _, line := range formatPanePlaceabilityLines(out.Panes) {
					fmt.Println(subtitleStyle.Render(line))
				}
			}
		}
	}

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
